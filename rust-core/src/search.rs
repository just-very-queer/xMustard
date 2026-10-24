//! Hybrid repo search (knowledge layer, WS-18). Up to seven lanes are fused with
//! Reciprocal Rank Fusion, then the top candidates are reranked by features that each
//! hit reports as reasons:
//!
//! - `bm25`: FTS5 BM25 over the code index's function-aligned chunks: body words, doc
//!   comments, the names each chunk declares and its path (`index::fts`). AND over the
//!   query terms first, OR for the rest, so a body-only term finds the function that
//!   uses it.
//! - `docs`: FTS5 BM25 over tracked docs and guidance, by heading section
//!   (`index::docs`), read from the index instead of the files.
//! - `name`: identifier match on symbol names (camelCase/snake_case subtokens weighted
//!   by IDF; a path token counts 0.3) and on file paths.
//! - `fuzzy`: char-trigram similarity of symbol names, a model-free hashing sketch: typo
//!   tolerance, not meaning. Built with `semantic-onnx` and a model configured in
//!   XMUSTARD_EMBED_MODEL, the same lane uses the model's vectors and reports itself as
//!   `semantic`.
//! - `structural`: inbound reference weight of the hit's file.
//! - `proximity`: graph distance to a seed symbol (explicit, or the exact name match).
//! - `intent`: opt-in typed decisions over bounded, fresh index excerpts; its probability
//!   is exposed as `scores.intent`.
//!
//! Fusion: a candidate scores the sum of weight/(RRF_K + rank) over the lanes that rank
//! it. RRF_K = 60 is a deliberate choice: the constant of Cormack, Clarke and Buettcher
//! (SIGIR 2009) that this search has used since its first lane; it damps the head of
//! each lane, so agreement across lanes beats one loud lane, and it is what the
//! retrieval gate's thresholds were measured with. The flip side of K = 60 is that
//! membership alone is worth nearly a lane's head (1/61 at rank 1, 1/70 at rank 10), so
//! lanes are weighted (`Lane::weight`): the evidence lanes (bm25, name, docs) count
//! fully, the fuzzy lane half and only above its similarity gate, and the prior lanes
//! (structural, proximity) re-rank rather than outvote the evidence.
//!
//! Rerank: the top RERANK_K fused candidates gain RERANK_WEIGHT times their rerank
//! score (`index::rerank`: declaration identifier match, window coverage and order,
//! dense and symbol-like lines) and are reordered; the rest keep their fused order and
//! report a rerank of 0 with no rerank reasons.
//! Every ordering breaks ties by path, line, kind and name, so the same inputs render
//! the same bytes.
//!
//! Pages: `offset` skips that many hits of the final order; `omitted` counts the ranked
//! hits after the page. The API turns them into a bounded cursor.
//!
//! The text lanes, UIDs, spans and snippets need the code index. When the legacy graph
//! answers (no index yet, or the index lags the caller's identity), search runs the
//! name, fuzzy, structural and proximity lanes and lists what is missing under
//! `degradations`.

use chrono::{SecondsFormat, Utc};
use serde::Serialize;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::Path;
use std::rc::Rc;

use rusqlite::Connection;

use crate::index::fts::{self, ChunkLoc, Decl, TextHit, TextLanes, TextMiss};
use crate::index::ignore::Glob;
use crate::index::reader::Opened;
use crate::index::rerank::{self, Features, Query, Snippet};
use crate::symbolgraph::{self, QuerySource};

/// The RRF constant (see the module docs).
pub const RRF_K: f64 = 60.0;
/// Candidates the rerank reads and reorders.
pub const RERANK_K: usize = 40;
/// Weight of the rerank score against the fused score. The head of an evidence lane
/// adds 1/61 = 0.0164, so an exact declaration match (rerank 1.0) is worth a little
/// more than one lane's head.
pub const RERANK_WEIGHT: f64 = 0.02;
/// Chunks the bm25 lane reads per query.
pub const BM25_K: usize = 100;
/// Doc sections the docs lane reads per query.
pub const DOCS_K: usize = 30;
/// Deepest result a page may reach (offset + limit); the API rejects cursors past it.
pub const MAX_WINDOW: usize = 200;
/// The fuzzy lane ranks only candidates at least this similar to the query.
const FUZZY_GATE: f32 = 0.30;
/// Distance to the seed (BFS depth over the reference graph) collapses to a proximity
/// weight: the seed's own file scores 1.0, its direct dependents 0.5, and so on.
const PROXIMITY_DEPTH: usize = 3;
/// Query tokens the name lane matches (a bit set per name holds them).
const MAX_NAME_TOKENS: usize = 128;
/// Minimum fixture/provider probability admitted to the semantic ranking lane.
const MIN_INTENT_PROBABILITY: f64 = 0.5;
/// Dimensions of the trigram sketch.
const SKETCH_DIM: usize = 256;

fn intent_degradations(
    stale: bool,
    provider_failed: bool,
    budget_limited: bool,
) -> &'static [&'static str] {
    match (stale, provider_failed, budget_limited) {
        (true, _, _) => &["semantic_stale"],
        (false, true, true) => &["semantic_unavailable", "semantic_budget"],
        (false, true, false) => &["semantic_unavailable"],
        (false, false, true) => &["semantic_budget"],
        (false, false, false) => &["semantic_unavailable"],
    }
}

fn append_intent_degradations(
    degradations: &mut Vec<String>,
    stale: bool,
    provider_failed: bool,
    budget_limited: bool,
) {
    degradations.extend(
        intent_degradations(stale, provider_failed, budget_limited)
            .iter()
            .map(|degradation| (*degradation).to_string()),
    );
}

#[cfg(test)]
struct UnsharedTextProvider<'a> {
    snapshot: &'a crate::index::reader::Snapshot,
    inner: &'a dyn crate::intent::DecisionProvider,
}

#[cfg(test)]
impl std::fmt::Debug for UnsharedTextProvider<'_> {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("UnsharedTextProvider")
            .finish_non_exhaustive()
    }
}

#[cfg(test)]
impl crate::intent::DecisionProvider for UnsharedTextProvider<'_> {
    fn decide(
        &self,
        question: &crate::intent::Question,
        candidates: &[crate::intent::Candidate],
    ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
        assert!(
            self.snapshot.shared_text_lock_available(),
            "intent provider called while the shared snapshot text mutex is held"
        );
        self.inner.decide(question, candidates)
    }
}

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

fn share_path(shared_paths: &mut HashSet<Rc<str>>, path: &str) -> Rc<str> {
    if let Some(existing) = shared_paths.get(path) {
        return existing.clone();
    }
    let shared: Rc<str> = path.into();
    shared_paths.insert(shared.clone());
    shared
}

fn camel_split(word: &str, out: &mut Vec<String>) {
    let chars: Vec<char> = word.chars().collect();
    let mut start = 0usize;
    for i in 1..chars.len() {
        // boundary at lower/digit -> Upper (camelCase / PascalCase)
        if chars[i].is_uppercase() && !chars[i - 1].is_uppercase() {
            let piece: String = chars[start..i].iter().collect();
            if piece.len() >= 2 {
                out.push(piece.to_lowercase());
            }
            start = i;
        }
    }
    let tail: String = chars[start..].iter().collect();
    if tail.len() >= 2 {
        out.push(tail.to_lowercase());
    }
}

/// Name-lane tokens: identifiers split into subtokens (snake_case, camelCase, paths),
/// so a query like "dashboard" matches "BuildWorkspaceDashboard".
fn tokens(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    for word in text.split(|c: char| !c.is_alphanumeric()) {
        if word.len() < 2 {
            continue;
        }
        out.push(word.to_lowercase());
        camel_split(word, &mut out);
    }
    out
}

/// Bit i set: `qtokens[i]` is a name-lane token of `text`.
fn token_bits(qtokens: &[String], text: &str) -> u128 {
    let set: HashSet<String> = tokens(text).into_iter().collect();
    qtokens
        .iter()
        .enumerate()
        .filter(|(_, t)| set.contains(*t))
        .fold(0, |bits, (i, _)| bits | 1 << i)
}

fn fnv1a(s: &str) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in s.bytes() {
        h ^= b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    h
}

/// The fuzzy lane's vector for a name: by default the model-free trigram sketch; with
/// the `semantic-onnx` feature and a configured model, the model's vector (XM-PRO-012).
fn name_vector(text: &str) -> Vec<f32> {
    #[cfg(feature = "semantic-onnx")]
    {
        if let Some(v) = onnx_embed::try_embed(text) {
            return v;
        }
    }
    trigram_sketch(text)
}

/// The fuzzy lane's reported name: `semantic` only when a model produces its vectors.
fn fuzzy_lane() -> &'static str {
    #[cfg(feature = "semantic-onnx")]
    {
        if onnx_embed::active() {
            return "semantic";
        }
    }
    "fuzzy"
}

#[cfg(feature = "semantic-onnx")]
mod onnx_embed {
    use fastembed::{
        InitOptionsUserDefined, Pooling, TextEmbedding, TokenizerFiles, UserDefinedEmbeddingModel,
    };
    use std::path::{Path, PathBuf};
    use std::sync::{Mutex, OnceLock};

    // The model is loaded once (multi-MB, links an onnxruntime session) and reused under a
    // Mutex — the inference session is not assumed Sync. None means "no model configured /
    // load failed", cached so we don't retry a broken config on every query.
    static MODEL: OnceLock<Option<Mutex<TextEmbedding>>> = OnceLock::new();

    fn read(dir: &Path, name: &str) -> Option<Vec<u8>> {
        std::fs::read(dir.join(name)).ok()
    }

    fn load() -> Option<Mutex<TextEmbedding>> {
        load_user_defined(&PathBuf::from(std::env::var_os("XMUSTARD_EMBED_MODEL")?))
    }

    // Build the embedder from a model directory, or None on any missing file / init
    // error (so the trigram sketch runs — retrieval is never broken by a bad config).
    // Separated from env-reading so it is deterministically testable.
    pub(super) fn load_user_defined(dir: &Path) -> Option<Mutex<TextEmbedding>> {
        // prefer a quantized/int8 graph (smaller RSS) when the operator shipped one.
        let onnx = ["model_quantized.onnx", "model_int8.onnx", "model.onnx"]
            .iter()
            .map(|n| dir.join(n))
            .find(|p| p.is_file())
            .and_then(|p| std::fs::read(p).ok())?;
        let tokenizer_files = TokenizerFiles {
            tokenizer_file: read(dir, "tokenizer.json")?,
            config_file: read(dir, "config.json")?,
            special_tokens_map_file: read(dir, "special_tokens_map.json")?,
            tokenizer_config_file: read(dir, "tokenizer_config.json")?,
        };
        let model =
            UserDefinedEmbeddingModel::new(onnx, tokenizer_files).with_pooling(Pooling::Mean);
        TextEmbedding::try_new_from_user_defined(model, InitOptionsUserDefined::default())
            .ok()
            .map(Mutex::new)
    }

    /// Whether a model is configured and loads (the lane is then reported `semantic`).
    pub fn active() -> bool {
        MODEL.get_or_init(load).is_some()
    }

    pub fn try_embed(text: &str) -> Option<Vec<f32>> {
        let model = MODEL.get_or_init(load).as_ref()?;
        let mut v = model.lock().ok()?.embed(vec![text], None).ok()?.pop()?;
        // L2-normalize so cosine() (a plain dot product) is valid for this lane too.
        let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        if norm > 0.0 {
            for x in &mut v {
                *x /= norm;
            }
        }
        Some(v)
    }
}

/// A model-free, deterministic char-trigram sketch: tokens and their padded trigrams
/// are feature-hashed (FNV-1a, with sign hashing) into SKETCH_DIM dimensions and
/// L2-normalized, so the dot product of two sketches measures shared subtokens and
/// trigrams ("dashbord" lands near "dashboard"). It carries no meaning.
fn trigram_sketch(text: &str) -> Vec<f32> {
    let mut v = vec![0f32; SKETCH_DIM];
    let mut add = |feat: &str, w: f32| {
        let h = fnv1a(feat);
        let idx = (h % SKETCH_DIM as u64) as usize;
        let sign = if (h >> 33) & 1 == 0 { 1.0 } else { -1.0 };
        v[idx] += w * sign;
    };
    for t in tokens(text) {
        add(&t, 1.0);
        let padded = format!("^{t}$");
        let chars: Vec<char> = padded.chars().collect();
        for w in chars.windows(3) {
            let tri: String = w.iter().collect();
            add(&tri, 0.5);
        }
    }
    let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
    if norm > 0.0 {
        for x in &mut v {
            *x /= norm;
        }
    }
    v
}

/// Cosine similarity of two already-L2-normalized vectors (just the dot product).
fn cosine(a: &[f32], b: &[f32]) -> f32 {
    a.iter().zip(b).map(|(x, y)| x * y).sum()
}

/// Scores are rounded for rendering, so equal rankings print equal bytes.
fn round6(x: f64) -> f64 {
    (x * 1e6).round() / 1e6
}

/// `serde`'s `skip_serializing_if` for a boolean that stays off unless it is on: `false`
/// is the default state of a flag that only appears when something was cut, so an
/// untruncated answer says nothing rather than saying `truncated: false`.
fn is_false(b: &bool) -> bool {
    !*b
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Lane {
    Bm25,
    Name,
    Fuzzy,
    Structural,
    Proximity,
    Docs,
    Intent,
}

impl Lane {
    const ALL: [Lane; 7] = [
        Lane::Bm25,
        Lane::Name,
        Lane::Fuzzy,
        Lane::Structural,
        Lane::Proximity,
        Lane::Docs,
        Lane::Intent,
    ];

    /// `mode=text` fuses the body lane alone. The other lanes read declared names,
    /// paths, inbound edges and graph distance, none of which is chunk text, so letting
    /// them score would rank a name match above a stronger body match — the hybrid
    /// dilution this mode exists to avoid.
    const BODY_ONLY: [Lane; 1] = [Lane::Bm25];

    fn name(self) -> &'static str {
        match self {
            Lane::Bm25 => "bm25",
            Lane::Name => "name",
            Lane::Fuzzy => fuzzy_lane(),
            Lane::Structural => "structural",
            Lane::Proximity => "proximity",
            Lane::Docs => "docs",
            Lane::Intent => "intent",
        }
    }

    /// RRF weight of the lane (see the module docs).
    fn weight(self) -> f64 {
        match self {
            Lane::Bm25 | Lane::Name | Lane::Docs => 1.0,
            Lane::Fuzzy | Lane::Proximity | Lane::Intent => 0.5,
            Lane::Structural => 0.25,
        }
    }

    fn bit(self) -> u8 {
        1 << self as u8
    }
}

fn score_key(lane: Lane, lane_name: &'static str) -> &'static str {
    match lane {
        Lane::Intent => "intent",
        _ => lane_name,
    }
}

/// What a hit is.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum Kind {
    /// A declaration.
    Symbol,
    /// Code between declarations (imports, top-level statements).
    Chunk,
    /// A file matched by its path.
    File,
    /// A doc or guidance section.
    Doc,
}

impl Kind {
    fn name(self) -> &'static str {
        match self {
            Kind::Symbol => "symbol",
            Kind::Chunk => "chunk",
            Kind::File => "file",
            Kind::Doc => "doc",
        }
    }
}

#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct SearchHit {
    /// `symbol` | `chunk` | `file` | `doc`
    pub kind: String,
    pub name: String,
    pub path: String,
    /// 1-based line to jump to: a declaration's name line, a chunk's first line.
    pub line: Option<usize>,
    /// First and last line of the declaration or chunk.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub lines: Option<[u32; 2]>,
    /// Stable UID of the declaration (code index only).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub uid: Option<String>,
    /// Final score: fused (`scores.rrf`) plus RERANK_WEIGHT × `scores.rerank`.
    pub score: f64,
    /// Raw score of every lane that ranked the hit, and `rrf` / `rerank`.
    pub scores: BTreeMap<&'static str, f64>,
    pub lanes_matched: Vec<&'static str>,
    /// Why the hit ranks where it does, strongest evidence first.
    pub reasons: Vec<String>,
    /// `reasons` joined with " · ".
    pub reason: String,
    /// Up to three ±1-line windows around the best matching lines, line-numbered.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub snippet: Vec<Snippet>,
}

#[derive(Debug, Clone, Serialize)]
pub struct SearchResult {
    pub workspace_id: String,
    pub query: String,
    /// Candidates the lanes ranked.
    pub total: usize,
    /// Hits of the final order skipped before this page.
    pub offset: usize,
    /// Ranked hits after this page.
    pub omitted: usize,
    pub hits: Vec<SearchHit>,
    /// Occurrences of the query as written, in `mode=exact|regex` (WS-59c). The scan
    /// modes rank nothing, so they report every occurrence here and leave `hits` empty;
    /// the hybrid and text modes report the reverse. One list per mode, not a hit shaped
    /// half as a span.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub spans: Vec<crate::exactscan::Span>,
    /// Bytes the scan read (WS-59c). Never more than the byte cap, and never more than
    /// the files it lists, so an answer that stopped short says so by this number.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub bytes_scanned: Option<u64>,
    /// The scan did not reach the end of the corpus: a cap fired or a file could not be
    /// read. Named in `cut` and `degradations`; a scan that finished leaves it false.
    #[serde(skip_serializing_if = "is_false")]
    pub truncated: bool,
    /// Which cap stopped the scan, or the files it could not read (`WS-59c`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cut: Option<crate::exactscan::Cut>,
    /// Files the scan listed but never opened, because a cap fired first.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub files_skipped: Option<u64>,
    /// Lanes or fields this answer lacks, and why.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub degradations: Vec<String>,
    /// Index coverage: lets an agent tell complete results from a degraded/empty/
    /// truncated graph (no Git, past the declared envelope) instead of trusting a
    /// partial answer.
    #[serde(default)]
    pub coverage: symbolgraph::IndexCoverage,
    /// Freshness of the graph that answered (PAR-FRESH-05).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub freshness: Option<crate::index::envelope::Freshness>,
    pub generated_at: String,
}

/// Which lanes one search runs (WS-59b: `search mode=hybrid|text`, WS-59c:
/// `mode=exact|regex`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum SearchMode {
    /// The default fusion: bm25 over path/symbols/body, the docs lane, the name,
    /// structural, fuzzy and proximity lanes.
    #[default]
    Hybrid,
    /// The body lane alone: BM25 over a chunk's text, not its path, its declared names
    /// or the doc chunks. Every hit therefore carries a term in code the query asked
    /// for, which the hybrid's fusion can rank below a name or path match.
    Text,
    /// The query as written, matched against the working tree's bytes (WS-59c,
    /// PAR-RET-09). No ranking and no index: every occurrence becomes a span.
    Exact,
    /// The query as a regular expression, matched against the working tree's bytes over
    /// the `regex` crate's linear-time automata (WS-59c, PAR-RET-09).
    Regex,
    /// Deterministic indexed search with the optional intent lane requested. This build
    /// has no configured provider, so it preserves deterministic hits and reports why.
    Intent,
}

impl SearchMode {
    /// True for the two scan modes, which read the working tree and report spans rather
    /// than ranking chunks. A closed set gets one predicate, not two comparisons at each
    /// use.
    pub fn is_scan(self) -> bool {
        matches!(self, SearchMode::Exact | SearchMode::Regex)
    }

    /// The core's CLI spelling of this mode, or None for the default the flag need not
    /// carry.
    pub fn flag_value(self) -> Option<&'static str> {
        match self {
            SearchMode::Hybrid => None,
            SearchMode::Text => Some("text"),
            SearchMode::Exact => Some("exact"),
            SearchMode::Regex => Some("regex"),
            SearchMode::Intent => Some("intent"),
        }
    }

    /// The matcher the scan modes compile their query with.
    pub fn matcher(self) -> crate::exactscan::Matcher {
        match self {
            SearchMode::Exact => crate::exactscan::Matcher::Exact,
            _ => crate::exactscan::Matcher::Regex,
        }
    }
}

/// Search arguments beyond the query.
#[derive(Debug, Clone, Default)]
pub struct SearchOptions<'a> {
    pub limit: usize,
    pub offset: usize,
    /// Anchor symbol of the proximity lane.
    pub seed: Option<&'a str>,
    /// Only paths matching this glob (gitignore semantics).
    pub path_glob: Option<&'a str>,
    /// Only files of this language (`files.lang`, the pack's name). Empty admits every
    /// file; a name no file carries matches nothing.
    pub lang: Option<&'a str>,
    pub mode: SearchMode,
    /// The scan modes' three caps (WS-59c). Zero takes each default; a value past its
    /// ceiling is refused by `exactscan::limits`, never clamped.
    pub scan_lines: usize,
    pub scan_bytes: u64,
    pub scan_timeout_ms: u64,
    /// Bytes per positional read in the scan modes. None is the core's own slice size;
    /// a test sets it to force a read boundary at a chosen offset, because a file large
    /// enough to cross a real boundary does not fit in a fixture.
    pub scan_slice: Option<usize>,
    /// Fixture or operator-selected typed provider for an explicit intent call.
    pub intent_provider: Option<&'a dyn crate::intent::DecisionProvider>,
    /// The one bounded yes/no question for the intent call.
    pub intent_question: Option<&'a crate::intent::Question>,
}

/// The strongest deterministic evidence for one file's intent-preview priority.
#[derive(Clone, Copy)]
struct SeedEvidence {
    name_score: f64,
    fuzzy: f32,
    bm25_rank: u32,
}

/// One candidate of the fused pool. The pool can hold every symbol of a large
/// repository (a common query token), so a candidate is kept compact: paths are
/// shared, matched name tokens are a bit set, and hit strings are built only for the
/// page returned.
struct Cand {
    kind: Kind,
    name: Box<str>,
    path: Rc<str>,
    line: Option<u32>,
    name_score: f64,
    /// Bit i set: name-lane token i matched the name.
    matched: u128,
    fuzzy: f32,
    structural: usize,
    /// 1/(graph distance + 1) to the seed; 0 when no seed or unreachable.
    proximity: f64,
    /// Row of the bm25 lane (its rank).
    code: Option<u32>,
    /// Row of the docs lane (its rank).
    doc: Option<u32>,
    hotspot: bool,
}

impl Cand {
    /// The candidate's key in `lane` (larger ranks first), None when the lane does not
    /// rank it.
    fn lane_key(&self, lane: Lane, intent_score: Option<f64>) -> Option<f64> {
        let positive = |x: f64| (x > 0.0).then_some(x);
        match lane {
            Lane::Bm25 => self.code.map(|i| -f64::from(i)),
            Lane::Docs => self.doc.map(|i| -f64::from(i)),
            Lane::Name => positive(self.name_score),
            Lane::Fuzzy => (self.fuzzy >= FUZZY_GATE).then_some(f64::from(self.fuzzy)),
            Lane::Structural => positive(self.structural as f64),
            Lane::Proximity => positive(self.proximity),
            Lane::Intent => intent_score,
        }
    }

    /// The raw lane score reported in `scores`.
    fn lane_score(&self, lane: Lane, text: &TextLanes, intent_score: Option<f64>) -> f64 {
        match lane {
            Lane::Bm25 => self.code.map_or(0.0, |i| text.code[i as usize].bm25),
            Lane::Docs => self.doc.map_or(0.0, |i| text.docs[i as usize].bm25),
            Lane::Name => self.name_score,
            Lane::Fuzzy => f64::from(self.fuzzy),
            Lane::Structural => self.structural as f64,
            Lane::Proximity => self.proximity,
            Lane::Intent => intent_score.unwrap_or(0.0),
        }
    }

    /// The tie-break order: path, line, kind, name.
    fn order(&self, o: &Cand) -> std::cmp::Ordering {
        self.path
            .cmp(&o.path)
            .then(self.line.cmp(&o.line))
            .then(self.kind.cmp(&o.kind))
            .then(self.name.cmp(&o.name))
    }
}

/// What the rerank and the hit read about one candidate. The chunk text itself is
/// dropped once its features and snippet are taken, so a query holds at most one
/// chunk's text at a time.
#[derive(Default)]
struct Ctx {
    decl: Option<Decl>,
    loc: Option<ChunkLoc>,
    /// Why the chunk's text could not be used.
    miss: Option<TextMiss>,
    features: Features,
    snippet: Vec<Snippet>,
}

fn generation_is_stale(conn: &Connection, expected: i64) -> rusqlite::Result<bool> {
    let generation: String = conn.query_row(
        "SELECT value FROM meta WHERE key = 'generation'",
        [],
        |row| row.get(0),
    )?;
    Ok(generation.parse::<i64>().ok() != Some(expected))
}

fn latest_generation_is_stale(snapshot: &crate::index::reader::Snapshot, expected: i64) -> bool {
    snapshot
        .with_unshared_text(|conn| generation_is_stale(conn, expected))
        .unwrap_or(true)
}

fn bounded_excerpt(text: &str) -> String {
    let mut end = text.len().min(crate::intent::MAX_EXCERPT_BYTES);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    text[..end].to_string()
}

/// Read a candidate's declaration, chunk and text, and compute its rerank features.
fn context(
    conn: Option<&Connection>,
    root: &Path,
    q: &Query,
    c: &Cand,
    text: &TextLanes,
    text_mode: bool,
) -> Ctx {
    let row = |h: &TextHit| (h.decl.clone(), Some(h.loc.clone()));
    let (decl, loc) = match (c.kind, c.code, c.doc, conn, c.line) {
        (_, Some(i), _, _, _) => row(&text.code[i as usize]),
        (_, _, Some(i), _, _) => row(&text.docs[i as usize]),
        (Kind::Symbol, None, None, Some(conn), Some(line)) => {
            match fts::locate(conn, &c.path, &c.name, line) {
                Ok(Some((d, l))) => (Some(d), l),
                _ => (None, None),
            }
        }
        _ => (None, None),
    };
    let body = conn
        .zip(loc.as_ref())
        .map(|(conn, loc)| fts::chunk_text(conn, root, loc));
    let decl_name = match (&decl, c.kind) {
        (Some(d), _) => d.name.as_str(),
        (None, Kind::Symbol) => &c.name,
        _ => "",
    };
    let (mut features, mut anchors) = match (&body, &loc) {
        (Some(Ok(t)), Some(l)) => rerank::text_features(q, t, l.start_line),
        _ => (Features::default(), Vec::new()),
    };
    // `mode=text` does not answer from a declared name, so the rerank's declaration
    // feature stays off here: it weighs an exact name match at 1.0 against a body window's
    // 0.5, which would put a name match above a stronger body match — the dilution this
    // mode exists to avoid. The window, dense-line and symbol-line features read the
    // chunk's own text and still apply.
    if !text_mode {
        features.decl = rerank::decl_match(q, decl_name);
    } else {
        features.decl = rerank::DeclMatch::None;
    }
    // a declaration with no matching line still shows its own line
    if let (true, Some(d), Some(l)) = (anchors.is_empty(), &decl, &loc)
        && (l.start_line..=l.end_line).contains(&d.name_line)
    {
        anchors.push(d.name_line);
    }
    let (snippet, miss) = match (body, &loc) {
        (Some(Ok(t)), Some(l)) => (rerank::snippets(&t, l.start_line, &anchors), None),
        (Some(Err(m)), _) => (Vec::new(), Some(m)),
        _ => (Vec::new(), None),
    };
    Ctx {
        decl,
        loc,
        miss,
        features,
        snippet,
    }
}

/// Hybrid search over the repository's code index (or its legacy graph).
///
/// `seed` is an optional anchor symbol: when set (or auto-derived from an exact
/// query→symbol match), graph distance from the seed becomes the proximity lane, which
/// only re-ranks the already-matched pool.
pub fn hybrid_search(
    root: &Path,
    workspace_id: &str,
    query: &str,
    limit: usize,
    seed: Option<&str>,
) -> SearchResult {
    hybrid_search_for(root, workspace_id, None, query, limit, seed)
}

/// [`hybrid_search`] for a read that observed the repository identity `identity_key`
/// (see [`symbolgraph::query_source_for`]).
pub fn hybrid_search_for(
    root: &Path,
    workspace_id: &str,
    identity_key: Option<&str>,
    query: &str,
    limit: usize,
    seed: Option<&str>,
) -> SearchResult {
    search(
        root,
        workspace_id,
        identity_key,
        query,
        &SearchOptions {
            limit,
            seed,
            ..Default::default()
        },
    )
}

/// The `mode=exact|regex` scan (WS-59c, PAR-RET-09): the query matched against the
/// working tree's bytes, under three reported caps.
///
/// The lane reads no index table and runs no ranking, so it needs neither `text_index`
/// nor the fusion below: it lists the files the graph knows, applies the caller's
/// `path_glob`/`lang` filters through the same `admit` the ranked lanes use (so one
/// filter cannot mean two things in two modes), and scans each admitted file once,
/// positionally, releasing it before the next open.
///
/// Returns the same `SearchResult` the ranked modes return, with `spans` filled and
/// `hits` empty, so the Go side and the budget ladder see one result shape.
fn scan(
    root: &Path,
    workspace_id: &str,
    identity_key: Option<&str>,
    query: &str,
    opts: &SearchOptions<'_>,
) -> SearchResult {
    let source = symbolgraph::query_source_for(root, workspace_id, identity_key);
    // the scan reads the working tree and lists it from Git, so it needs the source's
    // coverage and freshness but not its graph
    let mut coverage = source.coverage.clone();
    let mut degradations: Vec<String> = Vec::new();
    let mut read_errors: Vec<(&str, String)> = Vec::new();
    // An over-ceiling cap is refused, not clamped: a caller told it may read 32 MiB and
    // given a 4 MiB bound would read a bounded answer as a complete one. `text_index`
    // and this are the only two places a search can fail before it answers.
    let limits =
        match crate::exactscan::limits(opts.scan_lines, opts.scan_bytes, opts.scan_timeout_ms) {
            Ok(l) => l,
            Err(e) => {
                return SearchResult {
                    workspace_id: workspace_id.to_string(),
                    query: query.to_string(),
                    total: 0,
                    offset: opts.offset,
                    omitted: 0,
                    hits: Vec::new(),
                    spans: Vec::new(),
                    bytes_scanned: Some(0),
                    truncated: true,
                    cut: None,
                    files_skipped: None,
                    degradations: vec![e],
                    coverage,
                    freshness: Some(source.freshness([])),
                    generated_at: now(),
                };
            }
        };
    let matcher = match crate::exactscan::compile(query, opts.mode.matcher()) {
        Ok(re) => re,
        Err(e) => {
            return SearchResult {
                workspace_id: workspace_id.to_string(),
                query: query.to_string(),
                total: 0,
                offset: opts.offset,
                omitted: 0,
                hits: Vec::new(),
                spans: Vec::new(),
                bytes_scanned: Some(0),
                truncated: false,
                cut: None,
                files_skipped: None,
                degradations: vec![e],
                coverage,
                freshness: Some(source.freshness([])),
                generated_at: now(),
            };
        }
    };

    // The same filters the ranked lanes apply, read once and shared, so `path_glob` and
    // `lang` cannot mean one thing in the hybrid and another in the scan.
    let glob = opts
        .path_glob
        .filter(|g| !g.is_empty())
        .map(|g| Glob::compile(g.as_bytes()));
    let lang = opts
        .lang
        .filter(|l| !l.is_empty())
        .map(|l| l.to_ascii_lowercase());
    let admits_lang = |p: &str| match &lang {
        None => true,
        Some(want) => crate::index::lang::Lang::for_path(p).is_some_and(|l| l.name() == want),
    };
    let admit = |p: &str| glob.as_ref().is_none_or(|g| g.matches(p.as_bytes())) && admits_lang(p);

    let started = std::time::Instant::now();
    let mut report = crate::exactscan::Report::default();
    let mut budget = limits.max_bytes;
    let mut losses: Vec<crate::exactscan::Loss> = Vec::new();
    // The scan lists the TRACKED TREE, not the graph's files: the graph carries only the
    // files a language pack claims, and a grep that skipped `.md`, `.txt`, `.yaml` and a
    // `.env.example` would answer about code while claiming to answer about a repository.
    let (listed, listed_paths) = match symbolgraph::tracked_paths(root) {
        Ok(all) => (all.len() as u64, all),
        Err(e) => {
            // No listing means no answer can be complete, and saying so is the whole
            // point of this lane's `degradations`.
            read_errors.push(("files", e));
            (0, Vec::new())
        }
    };
    // `listed` counts what Git listed, BEFORE `admit`: `files_skipped` is a statement
    // about the corpus the scan was asked about, not about the filter it applied.
    let mut paths: Vec<String> = listed_paths.into_iter().filter(|p| admit(p)).collect();
    // Order is Git's own, so the same corpus produces the same bytes twice (the property
    // `search_hybrid`'s byte-identity test asserts for the hybrid).
    paths.sort();
    for rel in &paths {
        let mut scan_file =
            match crate::exactscan::FileScan::open_with_slice(root, rel, opts.scan_slice) {
                Ok(s) => s,
                Err(loss) => {
                    report.files_skipped_unreadable += 1;
                    losses.push(loss);
                    continue;
                }
            };
        let more = scan_file.scan(&matcher, &limits, &mut budget, &mut report, started);
        report.files_scanned += 1;
        // No pass counter is noted for the scan's own reads: `bytes_hashed` counts bytes
        // fed through a digest, and the scan never makes one (its own `bytes_scanned`
        // is the count it reports); the stats it does make are noted by the open.
        if !more {
            break;
        }
    }
    if report.cut.is_some() {
        // The corpus is not exhausted, so the files after this one were never opened.
        report.files_skipped = listed.saturating_sub(report.files_scanned as u64);
    }
    for loss in &losses {
        *coverage
            .loss_counts
            .entry(loss.count_key().to_string())
            .or_default() += 1;
        coverage.complete = false;
        degradations.push(loss.message());
    }
    if let Some(cut) = report.cut {
        degradations.push(match cut {
            crate::exactscan::Cut::Lines => format!(
                "the scan stopped at its line cap ({} lines); matches after it were not searched",
                limits.max_lines
            ),
            crate::exactscan::Cut::Bytes => format!(
                "the scan stopped at its byte cap ({} bytes); files after it were not searched",
                limits.max_bytes
            ),
            crate::exactscan::Cut::Time => format!(
                "the scan stopped at its time cap ({} ms); files after it were not searched",
                limits.timeout_ms
            ),
        });
    } else if report.files_skipped_unreadable > 0 {
        degradations.push(format!(
            "{} file(s) could not be read and were not searched",
            report.files_skipped_unreadable
        ));
    }

    if report.positions_lost > 0 {
        degradations.push(format!(
            "{} match(es) were found but their line could not be resolved",
            report.positions_lost
        ));
    }
    // A page of a scan's spans, by the same `offset`/`limit` the ranked modes use, so one
    // cursor shape serves every mode.
    let total = report.spans.len();
    let page_end = opts.offset.saturating_add(opts.limit.max(1)).min(total);
    let spans: Vec<crate::exactscan::Span> = report
        .spans
        .iter()
        .skip(opts.offset)
        .take(opts.limit.max(1))
        .cloned()
        .collect();
    let omitted = total.saturating_sub(page_end);

    for (what, e) in &read_errors {
        coverage.note_read_error(what, e);
    }
    let freshness = source.freshness(spans.iter().map(|s| s.path.as_str()));
    SearchResult {
        workspace_id: workspace_id.to_string(),
        query: query.to_string(),
        total,
        offset: opts.offset,
        omitted,
        hits: Vec::new(),
        spans,
        bytes_scanned: Some(report.bytes_scanned),
        truncated: report.cut.is_some()
            || report.files_skipped_unreadable > 0
            || report.positions_lost > 0,
        cut: report.cut,
        files_skipped: (report.files_skipped > 0).then_some(report.files_skipped),
        degradations,
        coverage,
        freshness: Some(freshness),
        generated_at: now(),
    }
}

/// The full search: a page of the final order under `opts`. The two scan modes
/// (`exact`, `regex`) answer from `scan` above and never reach the fusion below.
pub fn search(
    root: &Path,
    workspace_id: &str,
    identity_key: Option<&str>,
    query: &str,
    opts: &SearchOptions<'_>,
) -> SearchResult {
    // The scan modes read the working tree, not the index, so they dispatch before any
    // text lane opens a store: an exact search must not report an index degradation it
    // never depended on.
    if opts.mode.is_scan() {
        return scan(root, workspace_id, identity_key, query, opts);
    }
    let source = symbolgraph::query_source_for(root, workspace_id, identity_key);
    let graph = source.graph.as_ref();
    let mut coverage = source.coverage.clone();
    let mut degradations: Vec<String> = Vec::new();
    // A graph read that fails leaves its lane empty and marks the result's coverage
    // incomplete (`graph_read_error`), so a partial answer never reads as complete.
    let mut read_errors: Vec<(&str, String)> = Vec::new();
    let glob = opts
        .path_glob
        .filter(|g| !g.is_empty())
        .map(|g| Glob::compile(g.as_bytes()));
    // The `lang` filter reads the same `Lang::for_path` the writer stored in
    // `files.lang`, so a name the index never wrote matches nothing rather than
    // everything. Every lane goes through `admit`, not just the bm25 lane: a filter that
    // left the name and file lanes unfiltered would answer with symbols in a language it
    // was told to exclude.
    let lang = opts
        .lang
        .filter(|l| !l.is_empty())
        .map(|l| l.to_ascii_lowercase());
    let admits_lang = |p: &str| match &lang {
        None => true,
        Some(want) => crate::index::lang::Lang::for_path(p).is_some_and(|l| l.name() == want),
    };
    let admit = |p: &str| glob.as_ref().is_none_or(|g| g.matches(p.as_bytes())) && admits_lang(p);

    let mut qtokens: Vec<String> = tokens(query);
    qtokens.sort();
    qtokens.dedup();
    qtokens.truncate(MAX_NAME_TOKENS);
    let q = Query::new(query);
    let empty = |coverage, degradations| SearchResult {
        workspace_id: workspace_id.to_string(),
        query: query.to_string(),
        total: 0,
        offset: opts.offset,
        omitted: 0,
        hits: Vec::new(),
        // the ranked modes never scan, so the scan members stay absent rather than
        // reporting a budget that was never applied (WS-59c).
        spans: Vec::new(),
        bytes_scanned: None,
        truncated: false,
        cut: None,
        files_skipped: None,
        degradations,
        coverage,
        freshness: Some(source.freshness([])),
        generated_at: now(),
    };
    if qtokens.is_empty() && q.terms.is_empty() {
        return empty(coverage, degradations);
    }
    if fts::terms_dropped(query) > 0 {
        degradations.push(format!(
            "query terms past the first {} are not searched",
            fts::MAX_TERMS
        ));
    }

    // ---- text lanes (code index only) ----
    // `mode=text` reads the body lane alone: the docs lane answers prose the caller did
    // not ask for, and the name and file lanes are dropped below, so the only candidates
    // left are the chunks whose body carries a term.
    let body_only = opts.mode == SearchMode::Text;
    let index = text_index(&source, &mut degradations);
    let text: TextLanes = match index {
        Some(opened) => {
            let read = opened.snapshot.with_text(|conn| {
                fts::lanes(
                    conn,
                    &q.terms,
                    opts.path_glob,
                    BM25_K,
                    if body_only { 0 } else { DOCS_K },
                    // the normalized name, not the caller's spelling: `files.lang` holds
                    // lowercase pack names, so `lang=Go` would keep the graph hits and lose
                    // every body hit.
                    lang.as_deref(),
                    body_only,
                )
            });
            match read {
                Ok(mut t) => {
                    t.code.retain(|h| admit(&h.loc.path));
                    t.docs.retain(|h| admit(&h.loc.path));
                    if t.generation != opened.snapshot.generation {
                        degradations.push(format!(
                            "text lanes read index generation {}, the graph is generation {}",
                            t.generation, opened.snapshot.generation
                        ));
                    }
                    t
                }
                Err(e) => {
                    degradations.push(format!("bm25 and docs lanes unavailable: {e}"));
                    read_errors.push(("text lanes", e));
                    TextLanes::default()
                }
            }
        }
        None => TextLanes::default(),
    };

    let hotspots: HashSet<String> = match graph.hotspots(30) {
        Ok(h) => h.into_iter().map(|h| h.path).collect(),
        Err(e) => {
            read_errors.push(("hotspots", e));
            HashSet::new()
        }
    };
    let query_lc = query.trim().to_lowercase();
    let mut check = |what: &'static str, r: Result<(), String>| {
        if let Err(e) = r {
            read_errors.push((what, e));
        }
    };

    // document frequency of each query token across symbol names (idf). Snapshots list
    // symbols grouped by name, so the per-name work runs once per distinct name. Text mode
    // fuses the body lane alone, so no name score reads idf and the traversal is skipped:
    // it is repository-scale work whose result nothing would consume.
    let n = graph.symbol_count().max(1) as f64;
    let mut df = vec![0usize; qtokens.len()];
    if !body_only {
        let mut last: (String, u128) = (String::new(), 0);
        check(
            "symbols",
            graph.for_each_symbol(&mut |s| {
                if last.0 != s.name {
                    last = (s.name.to_string(), token_bits(&qtokens, s.name));
                }
                for (i, d) in df.iter_mut().enumerate() {
                    *d += usize::from(last.1 & (1 << i) != 0);
                }
            }),
        );
    }
    let idf = |i: usize| -> f64 { ((n + 1.0) / (df[i] as f64 + 1.0)).ln() + 1.0 };
    let q_vec = name_vector(query);

    // bm25 rows by the declaration they belong to (a long function's windows share one)
    let mut by_decl: HashMap<(&str, u32), u32> = HashMap::new();
    for (i, h) in text.code.iter().enumerate() {
        if let Some(d) = &h.decl {
            by_decl
                .entry((h.loc.path.as_str(), d.name_line))
                .or_insert(i as u32);
        }
    }
    let mut used_rows: HashSet<u32> = HashSet::new();
    let mut shared_paths: HashSet<Rc<str>> = HashSet::new();
    let mut exact: Vec<(Rc<str>, u32, Box<str>)> = Vec::new();
    let mut cands: Vec<Cand> = Vec::new();
    let mut name_memo: (String, u128, f32) = (String::new(), 0, 0.0);
    let mut path_memo: (String, u128) = (String::new(), 0);
    check(
        "symbols",
        graph.for_each_symbol(&mut |sym| {
            if !admit(sym.path) {
                return;
            }
            let line = sym.line.map(|l| l as u32);
            let code = line.and_then(|l| by_decl.get(&(sym.path, l)).copied());
            // In text mode a symbol is a candidate only when the body lane put it there:
            // without `code` it matched on its name or path, which this mode does not
            // answer from.
            if body_only && code.is_none() {
                return;
            }
            if name_memo.0 != sym.name {
                // the fuzzy lane compares the symbol NAME only; path tokens dilute it.
                let fuzzy = cosine(&q_vec, &name_vector(sym.name));
                name_memo = (sym.name.to_string(), token_bits(&qtokens, sym.name), fuzzy);
            }
            if path_memo.0 != sym.path {
                path_memo = (sym.path.to_string(), token_bits(&qtokens, sym.path));
            }
            let (matched, fuzzy) = (name_memo.1, name_memo.2);
            let name_score: f64 = (0..qtokens.len())
                .map(
                    |i| match (matched & (1 << i) != 0, path_memo.1 & (1 << i) != 0) {
                        (true, _) => idf(i),
                        (false, true) => 0.3 * idf(i),
                        (false, false) => 0.0,
                    },
                )
                .sum();
            if name_score <= 0.0 && fuzzy < FUZZY_GATE && code.is_none() {
                return;
            }
            let path = share_path(&mut shared_paths, sym.path);
            if sym.name.to_lowercase() == query_lc {
                exact.push((path.clone(), line.unwrap_or(0), sym.name.into()));
            }
            used_rows.extend(code);
            cands.push(Cand {
                kind: Kind::Symbol,
                name: sym.name.into(),
                path,
                line,
                name_score,
                matched,
                fuzzy,
                structural: sym.file_inbound,
                proximity: 0.0,
                code,
                doc: None,
                hotspot: hotspots.contains(sym.path),
            });
        }),
    );
    // file lane, and the inbound weight of the files text-lane hits land in
    let text_paths: HashSet<&str> = text
        .code
        .iter()
        .chain(&text.docs)
        .map(|h| h.loc.path.as_str())
        .collect();
    let mut inbound: HashMap<String, usize> = HashMap::new();
    check(
        "files",
        graph.for_each_file(&mut |f| {
            if text_paths.contains(f.path) && !body_only {
                // the inbound map feeds the structural lane's score, which text mode does
                // not run, so the per-file entry is built for nothing there
                inbound.insert(f.path.to_string(), f.inbound);
            }
            if !admit(f.path) {
                return;
            }
            // A file candidate is a path match, which text mode does not answer from.
            if body_only {
                return;
            }
            let bits = token_bits(&qtokens, f.path);
            let name_score: f64 = (0..qtokens.len())
                .filter(|i| bits & (1 << i) != 0)
                .map(|i| 0.4 * idf(i))
                .sum();
            let fuzzy = cosine(&q_vec, &name_vector(f.path));
            if name_score <= 0.0 && fuzzy < FUZZY_GATE {
                return;
            }
            cands.push(Cand {
                kind: Kind::File,
                name: f.path.into(),
                path: share_path(&mut shared_paths, f.path),
                line: None,
                name_score,
                matched: 0,
                fuzzy,
                structural: f.inbound,
                proximity: 0.0,
                code: None,
                doc: None,
                hotspot: hotspots.contains(f.path),
            });
        }),
    );
    // bm25 rows no name-lane symbol took: declarations the name lane did not match
    // (a body-only term), and code between declarations.
    for (i, h) in text.code.iter().enumerate() {
        let i = i as u32;
        let first_of_decl = h
            .decl
            .as_ref()
            .is_none_or(|d| by_decl.get(&(h.loc.path.as_str(), d.name_line)) == Some(&i));
        if used_rows.contains(&i) || !first_of_decl {
            continue;
        }
        let (kind, name, line) = match &h.decl {
            Some(d) => (Kind::Symbol, d.name.as_str(), d.name_line),
            None => (Kind::Chunk, h.loc.path.as_str(), h.loc.start_line),
        };
        cands.push(Cand {
            kind,
            name: name.into(),
            path: share_path(&mut shared_paths, &h.loc.path),
            line: Some(line),
            name_score: 0.0,
            matched: 0,
            fuzzy: 0.0,
            structural: inbound.get(&h.loc.path).copied().unwrap_or(0),
            proximity: 0.0,
            code: Some(i),
            doc: None,
            hotspot: hotspots.contains(&h.loc.path),
        });
    }
    for (i, h) in text.docs.iter().enumerate() {
        cands.push(Cand {
            kind: Kind::Doc,
            name: h.loc.path.as_str().into(),
            path: share_path(&mut shared_paths, &h.loc.path),
            line: Some(h.loc.start_line),
            name_score: 0.0,
            matched: 0,
            fuzzy: 0.0,
            structural: 0,
            proximity: 0.0,
            code: None,
            doc: Some(i as u32),
            hotspot: false,
        });
    }
    drop(by_decl);

    // Proximity lane: the seed (explicit, else the exact query→symbol match first in
    // path order) and its blast radius over the reference graph, by file.
    exact.sort();
    let effective_seed: Option<String> = match opts.seed {
        Some(s) if !s.trim().is_empty() => Some(s.trim().to_string()),
        _ => exact.first().map(|(_, _, name)| name.to_string()),
    };
    // The proximity lane does not run in text mode, so its blast-radius walk is not read:
    // the traversal is bounded by the graph depth but scales with the seed's reach.
    let seed_impact = if body_only {
        None
    } else {
        effective_seed.as_ref().and_then(|seed_name| {
            graph
                .impact(seed_name, PROXIMITY_DEPTH)
                .inspect_err(|e| read_errors.push(("seed impact", e.clone())))
                .ok()
        })
    };
    let mut prox: HashMap<String, f64> = HashMap::new();
    if let Some(impact) = seed_impact {
        // The proximity map is keyed by path and read straight off the seed's blast
        // radius, so it must go through `admit` like every other lane: a `lang` or a
        // `path_glob` filter that left it out would rank a hit the filter excluded.
        for d in &impact.defined_in {
            if admit(d) {
                prox.insert(d.clone(), 1.0); // the seed's own file: distance 0
            }
        }
        for f in &impact.impacted {
            if admit(&f.path) {
                prox.entry(f.path.clone())
                    .or_insert(1.0 / (f.distance as f64 + 1.0));
            }
        }
        for c in cands.iter_mut().filter(|c| c.kind != Kind::Doc) {
            if let Some(&p) = prox.get(&*c.path) {
                c.proximity = p;
            }
        }
    }

    let mut intent_scores: HashMap<usize, f64> = HashMap::new();
    if opts.mode == SearchMode::Intent {
        match (opts.intent_provider, opts.intent_question, index) {
            (Some(provider), Some(question), Some(opened)) => {
                let deterministic_len = cands.len();
                #[cfg(test)]
                let provider = &UnsharedTextProvider {
                    snapshot: &opened.snapshot,
                    inner: provider,
                };
                let mut cap_exhausted = false;
                let mut provider_failed = false;
                let mut query_budget = crate::intent::QueryBudget::default();
                let decision = opened.snapshot.with_unshared_text(|conn| {
                    conn.execute_batch("BEGIN")?;
                    let generation: String = conn.query_row(
                        "SELECT value FROM meta WHERE key = 'generation'",
                        [],
                        |row| row.get(0),
                    )?;
                    if generation.parse::<i64>().ok() != Some(opened.snapshot.generation) {
                        return Ok::<_, rusqlite::Error>((
                            Err(crate::intent::ProviderError::Fixture(
                                "stale generation".into(),
                            )),
                            HashMap::new(),
                            true,
                            HashSet::new(),
                        ));
                    }
                    // Classify bounded file previews before opening declaration excerpts.
                    let mut file_candidates = Vec::new();
                    let mut file_targets: HashMap<String, Vec<usize>> = HashMap::new();
                    let mut file_paths = HashMap::new();
                    let mut stale_excerpt = false;
                    let mut seed_evidence: HashMap<&str, SeedEvidence> = HashMap::new();
                    for candidate in cands.iter().filter(|candidate| candidate.kind != Kind::Doc) {
                        let evidence =
                            seed_evidence
                                .entry(candidate.path.as_ref())
                                .or_insert(SeedEvidence {
                                    name_score: 0.0,
                                    fuzzy: 0.0,
                                    bm25_rank: u32::MAX,
                                });
                        evidence.name_score = evidence.name_score.max(candidate.name_score);
                        // Only fuzzy evidence that passes the lane's gate counts, as in
                        // `lane_key`: trigram noise below the gate must not rank a seed.
                        let fuzzy = candidate.lane_key(Lane::Fuzzy, None).unwrap_or(0.0) as f32;
                        evidence.fuzzy = evidence.fuzzy.max(fuzzy);
                        if let Some(rank) = candidate.code {
                            evidence.bm25_rank = evidence.bm25_rank.min(rank);
                        }
                    }
                    // Seeds are ordered as the fusion weighs the lanes: name and BM25 (weight
                    // 1.0) before gated fuzzy (0.5), then path, so the attempt cap keeps the
                    // strongest deterministic evidence.
                    let mut seed_paths: Vec<&str> = seed_evidence.keys().copied().collect();
                    seed_paths.sort_by(|left, right| {
                        let left_evidence = seed_evidence[left];
                        let right_evidence = seed_evidence[right];
                        right_evidence
                            .name_score
                            .total_cmp(&left_evidence.name_score)
                            .then_with(|| left_evidence.bm25_rank.cmp(&right_evidence.bm25_rank))
                            .then_with(|| right_evidence.fuzzy.total_cmp(&left_evidence.fuzzy))
                            .then_with(|| left.cmp(right))
                    });
                    for (attempt, path) in seed_paths.into_iter().enumerate() {
                        if attempt >= crate::intent::MAX_CANDIDATE_FILES {
                            cap_exhausted = true;
                            break;
                        }
                        match fts::file_identity_matches(conn, root, path)? {
                            Ok(true) => {}
                            Ok(false) | Err(fts::TextMiss::Changed) => {
                                stale_excerpt = true;
                                continue;
                            }
                            Err(fts::TextMiss::TooLarge) => {
                                cap_exhausted = true;
                                continue;
                            }
                            Err(fts::TextMiss::SecretPath) => continue,
                        }
                        let loc = match fts::file_preview(conn, path) {
                            Ok(Some(loc)) => loc,
                            Ok(None) => continue,
                            Err(_) => {
                                stale_excerpt = true;
                                continue;
                            }
                        };
                        match fts::chunk_text(conn, root, &loc) {
                            Ok(excerpt) => {
                                if excerpt.len() > crate::intent::MAX_EXCERPT_BYTES {
                                    cap_exhausted = true;
                                }
                                let id = format!("file:{path}");
                                file_candidates.push(crate::intent::Candidate {
                                    id: id.clone(),
                                    kind: crate::intent::CandidateKind::File,
                                    excerpt: bounded_excerpt(
                                        &crate::index::chunks::mask_secret_words(&excerpt),
                                    ),
                                });
                                file_paths.insert(id.clone(), path.to_string());
                                file_targets.entry(id).or_default().extend(
                                    cands
                                        .iter()
                                        .enumerate()
                                        .filter(|(_, candidate)| {
                                            candidate.path.as_ref() == path
                                                && candidate.kind == Kind::File
                                        })
                                        .map(|(index, _)| index),
                                );
                            }
                            Err(fts::TextMiss::Changed) => stale_excerpt = true,
                            Err(fts::TextMiss::TooLarge) => cap_exhausted = true,
                            Err(_) => {}
                        }
                    }
                    file_candidates.sort_by(|left, right| left.id.cmp(&right.id));
                    let file_decision = if stale_excerpt {
                        Err(crate::intent::ProviderError::Fixture("stale source".into()))
                    } else if file_candidates.is_empty() {
                        Err(crate::intent::ProviderError::CandidateMismatch)
                    } else {
                        let batch = crate::intent::decide_batched_with_budget(
                            provider,
                            question,
                            &file_candidates,
                            &mut query_budget,
                        );
                        cap_exhausted |= batch.error.as_ref().is_some_and(|error| {
                            matches!(
                                error,
                                crate::intent::ProviderError::BudgetLimited
                                    | crate::intent::ProviderError::QuestionTooLarge
                            )
                        });
                        provider_failed |= batch.error.as_ref().is_some_and(|error| {
                            !matches!(
                                error,
                                crate::intent::ProviderError::BudgetLimited
                                    | crate::intent::ProviderError::QuestionTooLarge
                            )
                        });
                        if batch.decision.probabilities.is_empty() {
                            Err(batch
                                .error
                                .unwrap_or(crate::intent::ProviderError::CandidateMismatch))
                        } else {
                            Ok(batch.decision)
                        }
                    };
                    // A provider may take time; discard its decision if any excerpt went stale meanwhile.
                    stale_excerpt |=
                        latest_generation_is_stale(&opened.snapshot, opened.snapshot.generation)
                            || file_paths.values().any(|path| {
                                !matches!(
                                    fts::file_identity_matches(conn, root, path),
                                    Ok(Ok(true))
                                )
                            });
                    let file_decision = match file_decision {
                        Ok(decision) => decision,
                        Err(error) => {
                            return Ok::<_, rusqlite::Error>((
                                Err(error),
                                file_targets,
                                stale_excerpt,
                                HashSet::new(),
                            ));
                        }
                    };
                    if stale_excerpt {
                        return Ok::<_, rusqlite::Error>((
                            Err(crate::intent::ProviderError::Fixture("stale source".into())),
                            file_targets,
                            true,
                            HashSet::new(),
                        ));
                    }
                    let positive_paths: HashSet<String> = file_decision
                        .probabilities
                        .iter()
                        .filter(|(_, probability)| **probability >= MIN_INTENT_PROBABILITY)
                        .filter_map(|(id, _)| file_paths.get(id).cloned())
                        .collect();
                    let mut probabilities = file_decision.probabilities;
                    for (id, path) in &file_paths {
                        if !positive_paths.contains(path) {
                            continue;
                        }
                        let targets = file_targets.entry(id.clone()).or_default();
                        if !targets.is_empty() {
                            continue;
                        }
                        let Some(indexed_path) = cands
                            .iter()
                            .find(|candidate| candidate.path.as_ref() == path)
                            .map(|candidate| candidate.path.clone())
                        else {
                            continue;
                        };
                        let index = cands.len();
                        cands.push(Cand {
                            kind: Kind::File,
                            name: indexed_path.to_string().into(),
                            path: indexed_path,
                            line: None,
                            name_score: 0.0,
                            matched: 0,
                            fuzzy: 0.0,
                            structural: cands
                                .iter()
                                .filter(|candidate| candidate.path.as_ref() == path)
                                .map(|candidate| candidate.structural)
                                .max()
                                .or_else(|| inbound.get(path).copied())
                                .unwrap_or(0),
                            proximity: prox.get(path).copied().unwrap_or(0.0),
                            code: None,
                            doc: None,
                            hotspot: hotspots.contains(path),
                        });
                        targets.push(index);
                    }
                    let mut declaration_candidates = Vec::new();
                    let mut declaration_targets: HashMap<String, Vec<usize>> = HashMap::new();
                    let mut newly_discovered = HashSet::new();
                    let mut seen_declarations = HashSet::new();
                    let mut declaration_paths: Vec<&String> = positive_paths.iter().collect();
                    declaration_paths.sort();
                    for path in declaration_paths {
                        let remaining = crate::intent::MAX_DECLARATIONS
                            .saturating_sub(declaration_candidates.len());
                        let declarations =
                            fts::declarations(conn, path, remaining.saturating_add(1))?;
                        if declarations.len() > remaining {
                            cap_exhausted = true;
                        }
                        for (decl, loc) in declarations.into_iter().take(remaining) {
                            if !seen_declarations.insert(decl.uid.clone()) {
                                continue;
                            }
                            let Some(loc) = loc else { continue };
                            if declaration_candidates.len() >= crate::intent::MAX_DECLARATIONS {
                                cap_exhausted = true;
                                break;
                            }
                            let excerpt = match fts::chunk_text(conn, root, &loc) {
                                Ok(excerpt) => excerpt,
                                Err(fts::TextMiss::Changed) => {
                                    stale_excerpt = true;
                                    continue;
                                }
                                Err(fts::TextMiss::TooLarge) => {
                                    cap_exhausted = true;
                                    continue;
                                }
                                Err(_) => continue,
                            };
                            let id = format!("decl:{}:{}:{}", path, decl.name_line, decl.name);
                            if excerpt.len() > crate::intent::MAX_EXCERPT_BYTES {
                                cap_exhausted = true;
                            }
                            declaration_candidates.push(crate::intent::Candidate {
                                id: id.clone(),
                                kind: crate::intent::CandidateKind::Declaration,
                                excerpt: bounded_excerpt(&crate::index::chunks::mask_secret_words(
                                    &excerpt,
                                )),
                            });
                            let existing = cands.iter().position(|candidate| {
                                candidate.kind == Kind::Symbol
                                    && candidate.path.as_ref() == path
                                    && candidate.line == Some(decl.name_line)
                                    && candidate.name.as_ref() == decl.name
                            });
                            let target = match existing {
                                Some(index) => index,
                                None => {
                                    let index = cands.len();
                                    cands.push(Cand {
                                        kind: Kind::Symbol,
                                        name: decl.name.into(),
                                        path: share_path(&mut shared_paths, path),
                                        line: Some(decl.name_line),
                                        name_score: 0.0,
                                        matched: 0,
                                        fuzzy: 0.0,
                                        structural: cands
                                            .iter()
                                            .filter(|candidate| candidate.path.as_ref() == path)
                                            .map(|candidate| candidate.structural)
                                            .max()
                                            .or_else(|| inbound.get(path).copied())
                                            .unwrap_or(0),
                                        proximity: prox.get(path).copied().unwrap_or(0.0),
                                        code: None,
                                        doc: None,
                                        hotspot: hotspots.contains(path),
                                    });
                                    newly_discovered.insert(index);
                                    index
                                }
                            };
                            declaration_targets.entry(id).or_default().push(target);
                        }
                    }
                    if stale_excerpt {
                        return Ok::<_, rusqlite::Error>((
                            Err(crate::intent::ProviderError::Fixture("stale source".into())),
                            file_targets,
                            true,
                            HashSet::new(),
                        ));
                    }
                    if !declaration_candidates.is_empty() {
                        let batch = crate::intent::decide_batched_with_budget(
                            provider,
                            question,
                            &declaration_candidates,
                            &mut query_budget,
                        );
                        cap_exhausted |= batch.error.as_ref().is_some_and(|error| {
                            matches!(
                                error,
                                crate::intent::ProviderError::BudgetLimited
                                    | crate::intent::ProviderError::QuestionTooLarge
                            )
                        });
                        provider_failed |= batch.error.as_ref().is_some_and(|error| {
                            !matches!(
                                error,
                                crate::intent::ProviderError::BudgetLimited
                                    | crate::intent::ProviderError::QuestionTooLarge
                            )
                        });
                        if batch.decision.probabilities.is_empty() {
                            match batch.error {
                                Some(
                                    crate::intent::ProviderError::BudgetLimited
                                    | crate::intent::ProviderError::QuestionTooLarge,
                                ) => {}
                                Some(error) => {
                                    return Ok::<_, rusqlite::Error>((
                                        Err(error),
                                        file_targets,
                                        stale_excerpt,
                                        HashSet::new(),
                                    ));
                                }
                                None => {
                                    return Ok::<_, rusqlite::Error>((
                                        Err(crate::intent::ProviderError::CandidateMismatch),
                                        file_targets,
                                        stale_excerpt,
                                        HashSet::new(),
                                    ));
                                }
                            }
                        } else {
                            stale_excerpt |= latest_generation_is_stale(
                                &opened.snapshot,
                                opened.snapshot.generation,
                            ) || file_paths.values().any(|path| {
                                !matches!(
                                    fts::file_identity_matches(conn, root, path),
                                    Ok(Ok(true))
                                )
                            });
                            if stale_excerpt {
                                return Ok::<_, rusqlite::Error>((
                                    Err(crate::intent::ProviderError::Fixture(
                                        "stale source".into(),
                                    )),
                                    file_targets,
                                    true,
                                    HashSet::new(),
                                ));
                            }
                            probabilities.extend(batch.decision.probabilities);
                        }
                    }
                    file_targets.extend(declaration_targets);
                    conn.execute_batch("COMMIT")?;
                    Ok::<_, rusqlite::Error>((
                        Ok(crate::intent::Decision {
                            question_id: question.id.clone(),
                            probabilities,
                        }),
                        file_targets,
                        stale_excerpt,
                        newly_discovered,
                    ))
                });
                match decision {
                    Ok((Ok(decision), targets, stale_excerpt, newly_discovered)) => {
                        if stale_excerpt {
                            cands.truncate(deterministic_len);
                            append_intent_degradations(&mut degradations, true, false, false);
                        } else {
                            if provider_failed || cap_exhausted {
                                append_intent_degradations(
                                    &mut degradations,
                                    false,
                                    provider_failed,
                                    cap_exhausted,
                                );
                            }
                            for (id, probability) in decision.probabilities {
                                if probability >= MIN_INTENT_PROBABILITY
                                    && let Some(indices) = targets.get(&id)
                                {
                                    for &i in indices {
                                        intent_scores.insert(i, probability);
                                    }
                                }
                            }
                            let mut candidate_index = 0;
                            let mut retained_index = 0;
                            let mut retained_scores = HashMap::new();
                            cands.retain(|_| {
                                let old_index = candidate_index;
                                let keep = !newly_discovered.contains(&old_index)
                                    || intent_scores.contains_key(&old_index);
                                if keep {
                                    if let Some(score) = intent_scores.get(&old_index) {
                                        retained_scores.insert(retained_index, *score);
                                    }
                                    retained_index += 1;
                                }
                                candidate_index += 1;
                                keep
                            });
                            intent_scores = retained_scores;
                        }
                    }
                    Ok((
                        Err(
                            crate::intent::ProviderError::BudgetLimited
                            | crate::intent::ProviderError::QuestionTooLarge,
                        ),
                        _,
                        stale_excerpt,
                        _,
                    )) => {
                        cands.truncate(deterministic_len);
                        append_intent_degradations(
                            &mut degradations,
                            stale_excerpt,
                            provider_failed,
                            true,
                        );
                    }
                    Ok((
                        Err(crate::intent::ProviderError::InvalidQuestion),
                        _,
                        stale_excerpt,
                        _,
                    )) => {
                        cands.truncate(deterministic_len);
                        append_intent_degradations(
                            &mut degradations,
                            stale_excerpt,
                            true,
                            cap_exhausted,
                        );
                    }
                    Ok((Err(_), _, true, _)) => {
                        cands.truncate(deterministic_len);
                        append_intent_degradations(&mut degradations, true, false, false);
                    }
                    Ok((Err(_), _, false, _)) => {
                        cands.truncate(deterministic_len);
                        append_intent_degradations(
                            &mut degradations,
                            false,
                            provider_failed,
                            cap_exhausted,
                        );
                    }
                    Err(_) => {
                        cands.truncate(deterministic_len);
                        append_intent_degradations(&mut degradations, true, false, false);
                    }
                }
            }
            (Some(_), Some(_), None) => {
                append_intent_degradations(&mut degradations, false, false, false)
            }
            _ => append_intent_degradations(&mut degradations, false, false, false),
        }
    }
    drop(prox);

    drop(shared_paths);

    // ---- Reciprocal Rank Fusion ----
    let mut rrf = vec![0f64; cands.len()];
    let mut in_lanes = vec![0u8; cands.len()];
    for lane in if body_only {
        Lane::BODY_ONLY.as_slice()
    } else {
        Lane::ALL.as_slice()
    } {
        let mut ranked: Vec<(f64, u32)> = cands
            .iter()
            .enumerate()
            .filter_map(|(i, c)| {
                c.lane_key(*lane, intent_scores.get(&i).copied())
                    .map(|k| (k, i as u32))
            })
            .collect();
        ranked.sort_by(|a, b| {
            b.0.total_cmp(&a.0)
                .then_with(|| cands[a.1 as usize].order(&cands[b.1 as usize]))
        });
        for (rank, &(_, i)) in ranked.iter().enumerate() {
            rrf[i as usize] += lane.weight() / (RRF_K + rank as f64 + 1.0);
            in_lanes[i as usize] |= lane.bit();
        }
    }
    let mut order: Vec<usize> = (0..cands.len()).collect();
    sort_by_score(&cands, &rrf, &mut order);

    // ---- rerank the top RERANK_K, then read what the page shows ----
    let total = cands.len();
    let page_end = (opts.offset + opts.limit).min(total);
    let mut ctx: HashMap<usize, Ctx> = HashMap::new();
    let mut final_score = rrf.clone();
    let mut read = |conn: Option<&Connection>| {
        for &i in order.iter().take(RERANK_K) {
            let c = context(conn, root, &q, &cands[i], &text, body_only);
            final_score[i] = rrf[i] + RERANK_WEIGHT * c.features.score();
            ctx.insert(i, c);
        }
        let top = RERANK_K.min(order.len());
        sort_by_score(&cands, &final_score, &mut order[..top]);
        // a page past the reranked head reads its hits for the span and snippet only:
        // no rerank moved them, so none is reported
        for &i in order.get(opts.offset..page_end).unwrap_or(&[]) {
            ctx.entry(i).or_insert_with(|| Ctx {
                features: Features::default(),
                ..context(conn, root, &q, &cands[i], &text, body_only)
            });
        }
    };
    let ran = match index {
        Some(opened) => opened.snapshot.with_text(|conn| {
            read(Some(conn));
            Ok(())
        }),
        None => {
            read(None);
            Ok(())
        }
    };
    if let Err(e) = ran {
        degradations.push(format!("rerank text and snippets unavailable: {e}"));
        read(None);
    }

    let render = Render {
        qtokens: &qtokens,
        text: &text,
        seed: effective_seed.as_deref().unwrap_or(""),
        body_only,
        intent_scores: &intent_scores,
    };
    let hits: Vec<SearchHit> = order
        .get(opts.offset..page_end)
        .unwrap_or(&[])
        .iter()
        .map(|&i| {
            let cx = ctx.remove(&i).unwrap_or_default();
            render.hit(i, &cands[i], cx, (rrf[i], final_score[i]), in_lanes[i])
        })
        .collect();
    let omitted = total.saturating_sub(page_end.max(opts.offset));
    drop(cands);

    for (what, e) in &read_errors {
        coverage.note_read_error(what, e);
    }
    let freshness = source.freshness(
        hits.iter()
            .filter(|h| h.kind != "doc")
            .map(|h| h.path.as_str()),
    );
    SearchResult {
        workspace_id: workspace_id.to_string(),
        query: query.to_string(),
        total,
        offset: opts.offset,
        omitted,
        hits,
        // the ranked modes never scan, so the scan members stay absent (WS-59c).
        spans: Vec::new(),
        bytes_scanned: None,
        truncated: false,
        cut: None,
        files_skipped: None,
        degradations,
        coverage,
        freshness: Some(freshness),
        generated_at: now(),
    }
}

/// The index snapshot the text lanes (and UIDs, spans and snippets) read, or None with
/// the reason added to `degradations`. In order: the code index answers this read (not
/// the legacy graph), its store has this schema's text tables, and the operator did not
/// turn the lanes off (`XMUSTARD_SEARCH_TEXT_LANES=off`, for measurement and as a kill
/// switch).
fn text_index<'a>(source: &'a QuerySource, degradations: &mut Vec<String>) -> Option<&'a Opened> {
    let Some(opened) = source.index() else {
        degradations.push(
            "bm25, docs, uid and snippets need the code index, which does not answer this read \
             (see coverage.work)"
                .to_string(),
        );
        return None;
    };
    let schema = crate::index::schema::SCHEMA_VERSION.to_string();
    if opened.meta.schema_version != schema {
        degradations.push(format!(
            "bm25, docs, uid and snippets need index schema {schema}; the store is schema {} \
             until its next update rebuilds it",
            opened.meta.schema_version
        ));
        return None;
    }
    let off = std::env::var("XMUSTARD_SEARCH_TEXT_LANES").is_ok_and(|v| {
        matches!(
            v.trim().to_ascii_lowercase().as_str(),
            "off" | "0" | "false"
        )
    });
    if off {
        degradations.push(
            "bm25, docs, uid and snippets are off (XMUSTARD_SEARCH_TEXT_LANES=off)".to_string(),
        );
        return None;
    }
    Some(opened)
}

/// Order `idx` by `score` (highest first), ties by path, line, kind and name.
fn sort_by_score(cands: &[Cand], score: &[f64], idx: &mut [usize]) {
    idx.sort_by(|&a, &b| {
        score[b]
            .total_cmp(&score[a])
            .then_with(|| cands[a].order(&cands[b]))
    });
}

/// What every hit of a page is rendered against.
struct Render<'a> {
    qtokens: &'a [String],
    text: &'a TextLanes,
    seed: &'a str,
    /// `mode=text`: the bm25 lane read the body column, so a hit names `body` and says
    /// so, rather than claiming the whole-chunk bm25 it did not run.
    body_only: bool,
    intent_scores: &'a HashMap<usize, f64>,
}

impl Render<'_> {
    /// Build the returned hit of one candidate from its context, its (fused, final)
    /// scores and the lanes that ranked it.
    fn hit(
        &self,
        index: usize,
        c: &Cand,
        cx: Ctx,
        (rrf, score): (f64, f64),
        lanes: u8,
    ) -> SearchHit {
        let (qtokens, text, seed) = (self.qtokens, self.text, self.seed);
        let decl_name = cx
            .decl
            .as_ref()
            .map_or_else(|| c.name.to_string(), |d| d.name.clone());
        let mut reasons: Vec<String> = Vec::new();
        cx.features.reasons(&decl_name, &mut reasons);
        let lanes_run = if self.body_only {
            Lane::BODY_ONLY.as_slice()
        } else {
            Lane::ALL.as_slice()
        };
        let matched: Vec<&str> = lanes_run
            .iter()
            .filter(|l| lanes & l.bit() != 0)
            .map(|l| self.lane_name(*l))
            .collect();
        let row = |i: Option<u32>, rows: &[TextHit]| i.map(|i| rows[i as usize].clone());
        let (code_row, doc_row) = (row(c.code, &text.code), row(c.doc, &text.docs));
        for lane in lanes_run.iter().copied().filter(|l| lanes & l.bit() != 0) {
            let reason = match lane {
                Lane::Bm25 if self.body_only => code_row.as_ref().map(|h| match h.all_terms {
                    true => "body: every query term in the chunk body".to_string(),
                    false => "body: some query terms in the chunk body".to_string(),
                }),
                Lane::Bm25 => code_row.as_ref().map(|h| match h.all_terms {
                    true => "bm25: every query term in the chunk".to_string(),
                    false => "bm25: some query terms in the chunk".to_string(),
                }),
                Lane::Docs => doc_row
                    .as_ref()
                    .map(|h| format!("docs: {} section", h.kind)),
                Lane::Name => Some(match c.matched {
                    0 => "name: path match".to_string(),
                    m => {
                        let names: Vec<&str> = qtokens
                            .iter()
                            .enumerate()
                            .filter(|(t, _)| m & (1 << t) != 0)
                            .map(|(_, t)| t.as_str())
                            .collect();
                        format!("name: matched {}", names.join("+"))
                    }
                }),
                Lane::Intent => Some(format!(
                    "intent_probability: {:.2}",
                    self.intent_scores.get(&index).copied().unwrap_or(0.0)
                )),
                // the fuzzy lane ranks most candidates; it is a reason only when it alone
                // brought the hit in
                Lane::Fuzzy => (c.name_score <= 0.0 && c.code.is_none() && c.doc.is_none())
                    .then(|| format!("{}: name similarity {:.2}", lane.name(), c.fuzzy)),
                Lane::Structural => Some(format!(
                    "structural: file inbound reference weight {}",
                    c.structural
                )),
                Lane::Proximity => Some(format!(
                    "proximity: graph distance {} from seed {seed}",
                    (1.0 / c.proximity - 1.0).round() as i64
                )),
            };
            reasons.extend(reason);
        }
        if c.hotspot {
            reasons.push("hotspot file".to_string());
        }
        if code_row.as_ref().is_some_and(|h| h.role == "test") {
            reasons.push("test file".to_string());
        }
        if let Some(miss) = cx.miss {
            reasons.push(format!("no snippet: {}", miss.reason()));
        }
        let mut scores: BTreeMap<&'static str, f64> = Lane::ALL
            .into_iter()
            .filter(|l| lanes & l.bit() != 0)
            .map(|l| {
                (
                    score_key(l, self.lane_name(l)),
                    round6(c.lane_score(l, text, self.intent_scores.get(&index).copied())),
                )
            })
            .collect();
        scores.insert("rrf", round6(rrf));
        scores.insert("rerank", round6(cx.features.score()));
        let (line, lines) = match (&cx.decl, &cx.loc, c.kind) {
            (Some(d), _, Kind::Symbol) => (Some(d.name_line), Some([d.start_line, d.end_line])),
            (_, Some(l), Kind::Chunk | Kind::Doc) => {
                (Some(l.start_line), Some([l.start_line, l.end_line]))
            }
            _ => (c.line, c.line.map(|l| [l, l])),
        };
        SearchHit {
            kind: c.kind.name().to_string(),
            name: c.name.to_string(),
            path: c.path.to_string(),
            line: line.map(|l| l as usize),
            lines,
            uid: cx.decl.map(|d| d.uid),
            score: round6(score),
            scores,
            lanes_matched: matched,
            reason: reasons.join(" · "),
            reasons,
            snippet: cx.snippet,
        }
    }

    /// The name a lane reports under. In text mode the bm25 lane read only the `body`
    /// column, so it is named for what it searched.
    fn lane_name(&self, lane: Lane) -> &'static str {
        match (lane, self.body_only) {
            (Lane::Bm25, true) => "body",
            (l, _) => l.name(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use std::sync::{Mutex, mpsc};
    use std::time::Duration;
    use tempfile::TempDir;

    #[derive(Debug, Default)]
    struct RecordingProvider(Mutex<Vec<Vec<crate::intent::Candidate>>>);

    impl crate::intent::DecisionProvider for RecordingProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            self.0.lock().unwrap().push(candidates.to_vec());
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    #[derive(Debug, Default)]
    struct FailSecondBatchProvider(Mutex<(usize, Vec<Vec<crate::intent::Candidate>>)>);

    impl crate::intent::DecisionProvider for FailSecondBatchProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            let mut state = self.0.lock().unwrap();
            let call = state.0;
            state.0 += 1;
            state.1.push(candidates.to_vec());
            drop(state);
            if call == 1 {
                return Err(crate::intent::ProviderError::Fixture(
                    "later batch failed".into(),
                ));
            }
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    #[derive(Debug)]
    struct TargetProvider {
        positive_ids: Vec<String>,
        requests: Mutex<Vec<Vec<crate::intent::Candidate>>>,
    }

    impl crate::intent::DecisionProvider for TargetProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            self.requests.lock().unwrap().push(candidates.to_vec());
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| {
                        let probability = if self.positive_ids.contains(&candidate.id) {
                            0.95
                        } else {
                            0.1
                        };
                        (candidate.id.clone(), probability)
                    })
                    .collect(),
            })
        }
    }

    #[derive(Debug)]
    struct MutatingProvider {
        path: std::path::PathBuf,
        changed: Mutex<bool>,
    }

    impl crate::intent::DecisionProvider for MutatingProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            let mut changed = self.changed.lock().unwrap();
            if !*changed {
                std::fs::write(&self.path, "pub fn changed_after_preview() {}\n")
                    .map_err(|error| crate::intent::ProviderError::Fixture(error.to_string()))?;
                *changed = true;
            }
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    #[derive(Debug)]
    struct GenerationChangingProvider {
        db_path: std::path::PathBuf,
        changed: Mutex<bool>,
    }

    impl crate::intent::DecisionProvider for GenerationChangingProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            let mut changed = self.changed.lock().unwrap();
            if !*changed {
                let conn = Connection::open(&self.db_path)
                    .map_err(|error| crate::intent::ProviderError::Fixture(error.to_string()))?;
                conn.execute(
                    "UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'generation'",
                    [],
                )
                .map_err(|error| crate::intent::ProviderError::Fixture(error.to_string()))?;
                *changed = true;
            }
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    #[derive(Debug)]
    struct LateMutatingProvider {
        path: std::path::PathBuf,
        updated_source: String,
        changed: Mutex<bool>,
    }

    impl crate::intent::DecisionProvider for LateMutatingProvider {
        fn decide(
            &self,
            question: &crate::intent::Question,
            candidates: &[crate::intent::Candidate],
        ) -> Result<crate::intent::Decision, crate::intent::ProviderError> {
            let mut changed = self.changed.lock().unwrap();
            if !*changed {
                std::fs::write(&self.path, &self.updated_source)
                    .map_err(|error| crate::intent::ProviderError::Fixture(error.to_string()))?;
                *changed = true;
            }
            Ok(crate::intent::Decision {
                question_id: question.id.clone(),
                probabilities: candidates
                    .iter()
                    .map(|candidate| (candidate.id.clone(), 0.9))
                    .collect(),
            })
        }
    }

    fn git_repo(files: &[(&str, &str)]) -> TempDir {
        let dir = TempDir::new().unwrap();
        for (rel, content) in files {
            if let Some(parent) = Path::new(rel).parent() {
                std::fs::create_dir_all(dir.path().join(parent)).unwrap();
            }
            std::fs::write(dir.path().join(rel), content).unwrap();
        }
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
            vec!["add", "-A"],
            vec!["commit", "-qm", "c"],
        ] {
            Command::new("git")
                .arg("-C")
                .arg(dir.path())
                .args(&args)
                .output()
                .unwrap();
        }
        dir
    }

    /// A repository with its code index built, so every lane answers.
    fn indexed_repo(files: &[(&str, &str)]) -> TempDir {
        let dir = git_repo(files);
        let cfg = crate::index::config::IndexConfig::load(dir.path()).unwrap();
        crate::index::build(dir.path(), &cfg).unwrap();
        dir
    }

    #[test]
    fn query_local_text_connection_does_not_wait_for_the_shared_snapshot_lock() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn reconcile() {}\n")]);
        let opened = crate::index::reader::open(repo.path()).unwrap();
        let snapshot = opened.snapshot.clone();
        let (locked_tx, locked_rx) = mpsc::channel();
        let (release_tx, release_rx) = mpsc::channel();
        let holder = std::thread::spawn(move || {
            snapshot
                .with_text(|_| {
                    locked_tx.send(()).unwrap();
                    release_rx.recv().unwrap();
                    Ok(())
                })
                .unwrap();
        });
        locked_rx
            .recv_timeout(Duration::from_secs(2))
            .expect("the shared connection should be locked");

        let (reader_tx, reader_rx) = mpsc::channel();
        let snapshot = opened.snapshot.clone();
        let reader = std::thread::spawn(move || {
            let succeeded = snapshot.with_unshared_text(|_| Ok(())).is_ok();
            reader_tx.send(succeeded).unwrap();
        });
        let progressed_during_lock = reader_rx.recv_timeout(Duration::from_millis(200));
        release_tx.send(()).unwrap();
        holder.join().unwrap();
        reader.join().unwrap();

        assert!(progressed_during_lock.is_ok_and(|progressed| progressed));
    }

    #[test]
    fn intent_degradation_mapping_is_exact_and_stale_takes_priority() {
        assert_eq!(
            intent_degradations(false, false, false),
            &["semantic_unavailable"]
        );
        assert_eq!(
            intent_degradations(false, true, false),
            &["semantic_unavailable"]
        );
        assert_eq!(
            intent_degradations(false, false, true),
            &["semantic_budget"]
        );
        assert_eq!(
            intent_degradations(false, true, true),
            &["semantic_unavailable", "semantic_budget"]
        );
        assert_eq!(intent_degradations(true, false, false), &["semantic_stale"]);
        assert_eq!(intent_degradations(true, true, true), &["semantic_stale"]);
    }

    #[test]
    fn intent_score_keys_preserve_legacy_onnx_and_text_contracts() {
        assert_eq!(score_key(Lane::Fuzzy, "semantic"), "semantic");
        assert_eq!(score_key(Lane::Intent, "intent"), "intent");
        assert_eq!(score_key(Lane::Bm25, "body"), "body");
    }

    #[test]
    fn intent_lane_has_a_distinct_public_identity_from_fuzzy_embeddings() {
        assert_eq!(Lane::Intent.name(), "intent");
        assert_ne!(fuzzy_lane(), Lane::Intent.name());
    }

    #[test]
    fn oversized_intent_request_is_refused_before_any_provider_call() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn reconcile() {}\n")]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-large".into(),
            text: "x".repeat(crate::intent::MAX_BATCH_BYTES + 1),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let baseline = hybrid_search(repo.path(), "ws", "reconcile", 10, None);
        assert_eq!(result.hits, baseline.hits);
        assert!(provider.0.lock().unwrap().is_empty());
        assert!(result.degradations.iter().any(|d| d == "semantic_budget"));
    }

    fn intent_excerpt_fixture(contents: &str) -> (SearchResult, RecordingProvider) {
        let repo = indexed_repo(&[("src/reconcile.rs", contents)]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-truncated".into(),
            text: "Does this reconcile state?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        (result, provider)
    }

    #[test]
    fn oversized_file_preview_reports_incomplete_coverage_without_oversized_declaration() {
        let prelude = format!("use crate::{}Thing;\n", "part::".repeat(2_000));
        let contents = format!("{prelude}pub fn reconcile() {{}}\n");
        let (result, provider) = intent_excerpt_fixture(&contents);
        assert!(
            result
                .degradations
                .iter()
                .any(|item| item == "semantic_budget")
        );
        let requests = provider.0.lock().unwrap();
        assert!(requests.iter().flatten().any(|candidate| {
            candidate.kind == crate::intent::CandidateKind::File
                && candidate.excerpt.len() == crate::intent::MAX_EXCERPT_BYTES
        }));
        let has_oversized_declaration = requests.iter().flatten().any(|candidate| {
            candidate.kind == crate::intent::CandidateKind::Declaration
                && candidate.excerpt.len() == crate::intent::MAX_EXCERPT_BYTES
        });
        assert!(!has_oversized_declaration);
    }

    #[test]
    fn oversized_declaration_reports_incomplete_coverage_with_small_file_preview() {
        let contents = format!(
            "const SETUP: usize = 1;\n\npub fn reconcile() {{ let _data = \"{}\"; }}\n",
            "x".repeat(crate::intent::MAX_EXCERPT_BYTES + 100)
        );
        let (result, provider) = intent_excerpt_fixture(&contents);
        assert!(
            result
                .degradations
                .iter()
                .any(|item| item == "semantic_budget")
        );
        let requests = provider.0.lock().unwrap();
        assert!(requests.iter().flatten().any(|candidate| {
            candidate.kind == crate::intent::CandidateKind::File
                && candidate.excerpt.len() < crate::intent::MAX_EXCERPT_BYTES
        }));
        assert!(requests.iter().flatten().any(|candidate| {
            candidate.kind == crate::intent::CandidateKind::Declaration
                && candidate.excerpt.len() == crate::intent::MAX_EXCERPT_BYTES
        }));
    }

    #[test]
    fn oversized_candidate_stage_batches_partial_coverage() {
        let contents = format!(
            "pub fn local_work() {{ let _data = \"{}\"; }}\n",
            "x".repeat(crate::intent::MAX_EXCERPT_BYTES + 100)
        );
        let files: Vec<(String, String)> = (0..9)
            .map(|index| (format!("src/needle{index}.rs"), contents.clone()))
            .collect();
        let file_refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(path, contents)| (path.as_str(), contents.as_str()))
            .collect();
        let repo = indexed_repo(&file_refs);
        let provider = FailSecondBatchProvider::default();
        let question = crate::intent::Question {
            id: "q-bytes".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 20,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let semantic_degradations: Vec<&str> = result
            .degradations
            .iter()
            .filter(|degradation| degradation.starts_with("semantic_"))
            .map(String::as_str)
            .collect();
        assert_eq!(
            semantic_degradations,
            ["semantic_unavailable", "semantic_budget"]
        );
        let state = provider.0.lock().unwrap();
        assert_eq!(state.1.len(), crate::intent::MAX_PROVIDER_REQUESTS);
        assert!(state.1.iter().all(|batch| {
            batch
                .iter()
                .all(|candidate| candidate.excerpt.len() <= crate::intent::MAX_EXCERPT_BYTES)
        }));
        let first_file = state.1[0]
            .iter()
            .find(|candidate| candidate.kind == crate::intent::CandidateKind::File)
            .unwrap()
            .id
            .strip_prefix("file:")
            .unwrap();
        assert!(
            result
                .hits
                .iter()
                .all(|hit| { hit.path != first_file || !hit.lanes_matched.contains(&"intent") })
        );
    }

    #[test]
    fn intent_provider_receives_masked_file_and_declaration_excerpts() {
        let credential = ["gh", "p_", "12345678901234567890", "12345678901234567890"].concat();
        let source = format!("pub fn reconcile() {{ let token = \"{credential}\"; }}\n");
        let repo = indexed_repo(&[("src/lib.rs", &source)]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-secret".into(),
            text: "Does this reconcile state?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert!(!result.hits.is_empty());
        let requests = provider.0.lock().unwrap();
        assert_eq!(requests.len(), 2);
        assert!(requests.iter().flatten().all(|candidate| {
            !candidate.excerpt.contains(&credential) && candidate.excerpt.contains("[redacted]")
        }));
        assert!(
            requests
                .iter()
                .flatten()
                .any(|candidate| { candidate.kind == crate::intent::CandidateKind::Declaration })
        );
    }

    #[test]
    fn intent_declaration_discovery_excludes_nested_local_functions() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn outer() { fn nested_local() {} }\n")]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-nested".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let _ = search(
            repo.path(),
            "ws",
            None,
            "outer",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let requests = provider.0.lock().unwrap();
        assert!(
            !requests
                .iter()
                .flatten()
                .any(|candidate| { candidate.id.contains("nested_local") })
        );
    }

    #[test]
    fn path_seed_is_previewed_even_when_its_chunk_is_outside_bm25_window() {
        let mut files: Vec<(String, String)> = (0..BM25_K)
            .map(|index| {
                (
                    format!("src/z{index:03}.rs"),
                    format!(
                        "pub fn common_{index:03}() {{ {} }}\n",
                        "needle(); ".repeat(8)
                    ),
                )
            })
            .collect();
        files.push((
            "src/zz_needle.rs".into(),
            "pub fn relevant_behavior() {}\n".into(),
        ));
        let file_refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(path, contents)| (path.as_str(), contents.as_str()))
            .collect();
        let repo = indexed_repo(&file_refs);
        let provider = TargetProvider {
            positive_ids: vec![
                "file:src/zz_needle.rs".into(),
                "decl:src/zz_needle.rs:1:relevant_behavior".into(),
            ],
            requests: Mutex::new(Vec::new()),
        };
        let question = crate::intent::Question {
            id: "q-path".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 200,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let requests = provider.requests.lock().unwrap();
        assert!(
            requests[0]
                .iter()
                .any(|candidate| candidate.id == "file:src/zz_needle.rs")
        );
        assert!(result.hits.iter().any(|hit| {
            hit.name == "relevant_behavior" && hit.lanes_matched.contains(&"intent")
        }));
    }

    #[test]
    fn bm25_evidence_outranks_ungated_fuzzy_noise_when_choosing_intent_seeds() {
        // More noise files than the attempt cap, each named to share only trigrams with the
        // query (fuzzy below the gate) and mentioning it once in a long body, and one gold
        // file whose name shares nothing with the query but holds the strongest BM25
        // evidence. The gold file sorts last by path, so an order that ranked raw fuzzy
        // ahead of BM25 would spend every attempt on noise and never preview it.
        let noise = crate::intent::MAX_CANDIDATE_FILES + 8;
        let filler = "let unrelated_value = 1; ".repeat(40);
        let mut files: Vec<(String, String)> = (0..noise)
            .map(|index| {
                (
                    format!("src/n{index:03}.rs"),
                    format!("pub fn neexyz_{index:03}() {{ {filler}needle(); }}\n"),
                )
            })
            .collect();
        files.push((
            "src/zz_gold.rs".into(),
            "pub fn gold_target() { needle(); needle(); needle(); needle(); }\n".into(),
        ));
        let query = name_vector("needle");
        let gold_fuzzy = cosine(&query, &name_vector("gold_target"));
        assert!(
            (0..noise).all(|index| {
                let fuzzy = cosine(&query, &name_vector(&format!("neexyz_{index:03}")));
                fuzzy > gold_fuzzy && fuzzy < FUZZY_GATE
            }),
            "premise: every noise name carries more raw fuzzy evidence than the gold name, \
             all of it below the gate"
        );
        let file_refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(path, contents)| (path.as_str(), contents.as_str()))
            .collect();
        let repo = indexed_repo(&file_refs);
        let provider = TargetProvider {
            positive_ids: vec![
                "file:src/zz_gold.rs".into(),
                "decl:src/zz_gold.rs:1:gold_target".into(),
            ],
            requests: Mutex::new(Vec::new()),
        };
        let question = crate::intent::Question {
            id: "q-bm25".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 200,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let requests = provider.requests.lock().unwrap();
        assert!(
            requests
                .iter()
                .flatten()
                .any(|candidate| candidate.id == "file:src/zz_gold.rs"),
            "the file with the strongest BM25 evidence was never previewed"
        );
        assert!(
            result
                .hits
                .iter()
                .any(|hit| { hit.name == "gold_target" && hit.lanes_matched.contains(&"intent") })
        );
    }

    #[test]
    fn positive_file_expands_into_nonmatching_indexed_declarations() {
        let repo = indexed_repo(&[(
            "src/needle.rs",
            "pub fn needle() {}\npub fn reconcile_workspace_registration() {}\n",
        )]);
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q":{"file:src/needle.rs":0.9,"decl:src/needle.rs:1:needle":0.1,"decl:src/needle.rs:2:reconcile_workspace_registration":0.95}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q".into(),
            text: "Does it maintain workspace registration?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let declaration = result
            .hits
            .iter()
            .find(|hit| hit.name == "reconcile_workspace_registration")
            .expect("positive declaration discovered from its positive file");
        assert!(declaration.lanes_matched.contains(&"intent"));
        assert_eq!(declaration.scores["intent"], 0.95);
    }

    #[test]
    fn rejected_new_declaration_does_not_enter_the_result_pool() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn needle() {}\npub fn unrelated() {}\n")]);
        let provider = TargetProvider {
            positive_ids: vec!["file:src/lib.rs".into()],
            requests: Mutex::new(Vec::new()),
        };
        let question = crate::intent::Question {
            id: "q".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let baseline = hybrid_search(repo.path(), "ws", "needle", 10, None);
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert_eq!(result.total, baseline.total + 1);
        assert!(result.hits.iter().any(|hit| hit.name == "needle"));
        assert!(result.hits.iter().all(|hit| hit.name != "unrelated"));
        let requests = provider.requests.lock().unwrap();
        assert!(
            requests
                .iter()
                .flatten()
                .any(|candidate| candidate.id == "decl:src/lib.rs:2:unrelated")
        );
    }

    #[test]
    fn declaration_provider_failure_rolls_back_semantic_only_candidates() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn reconcile() {}\n")]);
        let provider =
            crate::intent::FixtureProvider::from_json(r#"{"q":{"file:src/lib.rs":0.9}}"#).unwrap();
        let question = crate::intent::Question {
            id: "q".into(),
            text: "Does this reconcile state?".into(),
        };
        let regular = hybrid_search(repo.path(), "ws", "reconcile", 10, None);
        let intent = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert_eq!(intent.hits, regular.hits);
        for hit in &regular.hits {
            assert!(
                hit.lanes_matched
                    .iter()
                    .all(|lane| hit.scores.contains_key(lane))
            );
        }
        for hit in &intent.hits {
            assert!(
                hit.lanes_matched
                    .iter()
                    .all(|lane| hit.scores.contains_key(lane))
            );
        }
        assert_eq!(
            intent
                .degradations
                .iter()
                .filter(|degradation| degradation.starts_with("semantic_"))
                .map(String::as_str)
                .collect::<Vec<_>>(),
            ["semantic_unavailable"]
        );
    }

    #[test]
    fn intent_provider_adds_probability_ranked_semantic_lane() {
        let repo = indexed_repo(&[(
            "src/lib.rs",
            "/// Refreshes repository metadata after edits.\npub fn reconcile() {}\n",
        )]);
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q-behavior":{"file:src/lib.rs":0.9,"decl:src/lib.rs:2:reconcile":0.9}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "Does this update repository state after edits?".into(),
        };
        let intent = search(
            repo.path(),
            "ws",
            None,
            "update repository state after edits",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let hit = intent
            .hits
            .iter()
            .find(|hit| hit.path == "src/lib.rs")
            .unwrap();
        assert!(hit.lanes_matched.contains(&"intent"));
        assert_eq!(hit.scores["intent"], 0.9);
        assert!(
            hit.lanes_matched
                .iter()
                .all(|lane| hit.scores.contains_key(lane))
        );
        assert!(
            hit.reasons
                .iter()
                .any(|reason| reason.contains("intent_probability"))
        );
        assert!(
            !intent
                .degradations
                .iter()
                .any(|d| d == "semantic_unavailable")
        );
    }

    #[test]
    fn positive_file_preview_can_rank_a_file_hit_without_positive_symbols() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn rebuild_registry() {}\n")]);
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q-behavior":{"file:src/lib.rs":0.9,"decl:src/lib.rs:1:rebuild_registry":0.1}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "Does this rebuild the registry?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "rebuild registry",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let hit = result.hits.iter().find(|hit| hit.kind == "file").unwrap();
        assert!(hit.lanes_matched.contains(&"intent"));
        assert_eq!(hit.scores["intent"], 0.9);
    }

    #[test]
    fn equal_intent_probabilities_sort_files_stably() {
        let repo = indexed_repo(&[
            (
                "src/b.rs",
                "/// Refreshes repository metadata after edits.\npub fn reconcile_b() {}\n",
            ),
            (
                "src/a.rs",
                "/// Refreshes repository metadata after edits.\npub fn reconcile_a() {}\n",
            ),
        ]);
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q-behavior":{"file:src/a.rs":0.8,"file:src/b.rs":0.8,"decl:src/a.rs:2:reconcile_a":0.8,"decl:src/b.rs:2:reconcile_b":0.8}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "Does this update repository state after edits?".into(),
        };
        let run = || {
            search(
                repo.path(),
                "ws",
                None,
                "update repository state after edits",
                &SearchOptions {
                    limit: 10,
                    mode: SearchMode::Intent,
                    intent_provider: Some(&provider),
                    intent_question: Some(&question),
                    ..Default::default()
                },
            )
        };
        let first = run();
        let second = run();
        let file_paths: Vec<&str> = first
            .hits
            .iter()
            .filter(|hit| hit.kind == "file" && hit.lanes_matched.contains(&"intent"))
            .map(|hit| hit.path.as_str())
            .collect();
        assert_eq!(file_paths, ["src/a.rs", "src/b.rs"]);
        assert_eq!(first.hits, second.hits);
    }

    #[test]
    fn over_budget_question_keeps_deterministic_hits() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn rebuild_registry() {}\n")]);
        let provider =
            crate::intent::FixtureProvider::from_json(r#"{"q-behavior":{"file:src/lib.rs":0.9}}"#)
                .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "x".repeat(crate::intent::MAX_BATCH_BYTES + 1),
        };
        let regular = hybrid_search(repo.path(), "ws", "rebuild registry", 10, None);
        let intent = search(
            repo.path(),
            "ws",
            None,
            "rebuild registry",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert_eq!(intent.hits, regular.hits);
        assert!(intent.degradations.iter().any(|d| d == "semantic_budget"));
    }

    #[test]
    fn intent_skips_declarations_when_the_file_preview_is_negative() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn rebuild_registry() {}\n")]);
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q-behavior":{"file:src/lib.rs":0.25,"decl:src/lib.rs:1:rebuild_registry":0.9}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "Does this rebuild the registry?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "rebuild registry",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert!(
            result
                .hits
                .iter()
                .all(|hit| !hit.lanes_matched.contains(&"intent"))
        );
        assert!(
            !result
                .degradations
                .iter()
                .any(|d| d == "semantic_unavailable")
        );
    }

    #[test]
    fn intent_file_cap_reports_partial_discovery() {
        let files: Vec<(String, String)> = (0..=crate::intent::MAX_CANDIDATE_FILES)
            .map(|index| {
                (
                    format!("src/needle{index:02}.rs"),
                    format!("pub fn work_{index:02}() {{}}\n"),
                )
            })
            .collect();
        let file_refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(path, contents)| (path.as_str(), contents.as_str()))
            .collect();
        let repo = indexed_repo(&file_refs);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-cap".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert!(result.degradations.iter().any(|d| d == "semantic_budget"));
        assert!(provider.0.lock().unwrap()[0].len() <= crate::intent::MAX_CANDIDATE_FILES);
    }

    #[test]
    fn intent_file_read_cap_bounds_attempts_when_candidates_are_stale() {
        let files: Vec<(String, String)> = (0..40)
            .map(|index| {
                (
                    format!("src/needle{index:02}.rs"),
                    format!("pub fn work_{index:02}() {{}}\n"),
                )
            })
            .collect();
        let file_refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(path, contents)| (path.as_str(), contents.as_str()))
            .collect();
        let repo = indexed_repo(&file_refs);
        for (path, _) in &files {
            std::fs::write(repo.path().join(path), "pub fn changed() {}\n").unwrap();
        }
        let before = crate::index::fts::file_identity_reads();
        let question = crate::intent::Question {
            id: "q-read-cap".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Intent,
                intent_provider: Some(&RecordingProvider::default()),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert_eq!(
            crate::index::fts::file_identity_reads() - before,
            crate::intent::MAX_CANDIDATE_FILES as u64
        );
        assert!(result.degradations.iter().any(|d| d == "semantic_stale"));
    }

    #[test]
    fn oversized_file_preview_is_reported_with_other_candidates_classified() {
        let huge = format!(
            "pub fn huge() {{ let value = \"{}\"; }}\n",
            "x".repeat(crate::index::fts::MAX_CHUNK_READ as usize + 1)
        );
        let repo = indexed_repo(&[
            ("src/needle_huge.rs", &huge),
            ("src/needle_small.rs", "pub fn small() {}\n"),
        ]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-oversized-file".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert!(result.degradations.iter().any(|d| d == "semantic_budget"));
        let requests = provider.0.lock().unwrap();
        assert!(
            requests[0]
                .iter()
                .any(|candidate| candidate.id == "file:src/needle_small.rs")
        );
    }

    #[test]
    fn oversized_declaration_excerpt_is_reported_with_other_candidates_classified() {
        let mut source = String::from("pub fn seed() {\n");
        for _ in 0..160 {
            source.push_str("    let value = 0;\n");
        }
        source.push_str("}\npub fn needle() {}\n");
        source.push_str(&format!(
            "pub fn huge() {{ let value = \"{}\"; }}\n",
            "x".repeat(crate::index::fts::MAX_CHUNK_READ as usize + 1)
        ));
        let repo = indexed_repo(&[("src/needle.rs", &source)]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-oversized-decl".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert!(result.degradations.iter().any(|d| d == "semantic_budget"));
        let requests = provider.0.lock().unwrap();
        assert_eq!(requests.len(), 2);
        assert!(
            requests[1]
                .iter()
                .any(|candidate| candidate.id == "decl:src/needle.rs:1:seed")
        );
        assert!(
            !requests[1]
                .iter()
                .any(|candidate| candidate.id.contains(":huge"))
        );
    }

    #[test]
    fn intent_declaration_cap_reports_partial_discovery() {
        let source = (0..=crate::intent::MAX_DECLARATIONS)
            .map(|index| format!("pub fn indexed_work_{index:02}() {{}}\n"))
            .collect::<String>();
        let repo = indexed_repo(&[("src/needle.rs", &source)]);
        let provider = RecordingProvider::default();
        let question = crate::intent::Question {
            id: "q-cap".into(),
            text: "Does this implement the requested behavior?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 100,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert!(result.degradations.iter().any(|d| d == "semantic_budget"));
        let requests = provider.0.lock().unwrap();
        assert_eq!(requests.len(), 2);
        assert!(requests[1].len() <= crate::intent::MAX_DECLARATIONS);
    }

    #[test]
    fn source_changed_after_file_decision_discards_all_intent_hits() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn reconcile() {}\n")]);
        let baseline = hybrid_search(repo.path(), "ws", "reconcile", 10, None);
        let provider = MutatingProvider {
            path: repo.path().join("src/lib.rs"),
            changed: Mutex::new(false),
        };
        let question = crate::intent::Question {
            id: "q".into(),
            text: "Does this reconcile state?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        let identity = |hits: &[SearchHit]| {
            hits.iter()
                .map(|hit| {
                    (
                        hit.path.clone(),
                        hit.name.clone(),
                        hit.kind.clone(),
                        hit.line,
                    )
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(identity(&result.hits), identity(&baseline.hits));
        assert!(result.degradations.iter().any(|d| d == "semantic_stale"));
        assert!(
            result
                .hits
                .iter()
                .all(|hit| !hit.lanes_matched.contains(&"intent"))
        );
    }

    #[test]
    fn intent_refuses_candidates_after_source_hash_changes() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn rebuild_registry() {}\n")]);
        std::fs::write(
            repo.path().join("src/lib.rs"),
            "pub fn erase_registry() {}\n",
        )
        .unwrap();
        let provider = crate::intent::FixtureProvider::from_json(
            r#"{"q-behavior":{"file:src/lib.rs":0.9,"decl:src/lib.rs:1:rebuild_registry":0.9}}"#,
        )
        .unwrap();
        let question = crate::intent::Question {
            id: "q-behavior".into(),
            text: "Does this rebuild the registry?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "rebuild registry",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );
        assert!(result.degradations.iter().any(|d| d == "semantic_stale"));
        assert!(
            result
                .hits
                .iter()
                .all(|hit| !hit.lanes_matched.contains(&"intent"))
        );
    }

    #[test]
    fn generation_change_during_provider_call_discards_intent_decisions() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn reconcile() {}\n")]);
        let baseline = hybrid_search(repo.path(), "ws", "reconcile", 10, None);
        let cfg = crate::index::config::IndexConfig::load(repo.path()).unwrap();
        let git_dir = repo.path().join(".git");
        let db_path =
            crate::index::index_dir(repo.path(), Some(&git_dir), &cfg).join(crate::index::DB_FILE);
        let provider = GenerationChangingProvider {
            db_path,
            changed: Mutex::new(false),
        };
        let question = crate::intent::Question {
            id: "q-generation".into(),
            text: "Does this reconcile state?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert_eq!(result.hits, baseline.hits);
        assert!(result.degradations.iter().any(|d| d == "semantic_stale"));
        assert!(
            result
                .hits
                .iter()
                .all(|hit| !hit.lanes_matched.contains(&"intent"))
        );
    }

    #[test]
    fn changes_beyond_the_preview_window_invalidate_file_identity() {
        let mut source = String::from("pub fn reconcile() {\n");
        for line in 0..220 {
            if line == 200 {
                source.push_str("    let changed_late_window = 0;\n");
            } else {
                source.push_str(&format!("    let stable_value_{line} = {line};\n"));
            }
        }
        source.push_str("}\n");
        let updated_source = source.replace("changed_late_window", "mutated_late_window");
        let repo = indexed_repo(&[("src/lib.rs", &source)]);
        let provider = LateMutatingProvider {
            path: repo.path().join("src/lib.rs"),
            updated_source,
            changed: Mutex::new(false),
        };
        let question = crate::intent::Question {
            id: "q-late-source".into(),
            text: "Does this reconcile state?".into(),
        };
        let result = search(
            repo.path(),
            "ws",
            None,
            "reconcile",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                intent_provider: Some(&provider),
                intent_question: Some(&question),
                ..Default::default()
            },
        );

        assert!(result.degradations.iter().any(|d| d == "semantic_stale"));
        assert!(
            result
                .hits
                .iter()
                .all(|hit| !hit.lanes_matched.contains(&"intent"))
        );
    }

    #[test]
    fn intent_mode_without_provider_preserves_deterministic_hits_and_degrades() {
        let repo = indexed_repo(&[("src/lib.rs", "pub fn refresh_workspace_index() {}\n")]);
        let regular = hybrid_search(repo.path(), "ws", "refresh workspace", 10, None);
        let intent = search(
            repo.path(),
            "ws",
            None,
            "refresh workspace",
            &SearchOptions {
                limit: 10,
                mode: SearchMode::Intent,
                ..Default::default()
            },
        );
        assert_eq!(intent.hits, regular.hits);
        for hit in &regular.hits {
            assert!(
                hit.lanes_matched
                    .iter()
                    .all(|lane| hit.scores.contains_key(lane))
            );
        }
        for hit in &intent.hits {
            assert!(
                hit.lanes_matched
                    .iter()
                    .all(|lane| hit.scores.contains_key(lane))
            );
        }
        assert_eq!(
            intent
                .degradations
                .iter()
                .filter(|degradation| degradation.starts_with("semantic_"))
                .map(String::as_str)
                .collect::<Vec<_>>(),
            ["semantic_unavailable"]
        );
    }

    #[test]
    fn repo_role_classifies_canonically() {
        use crate::symbolgraph::repo_role;
        assert_eq!(repo_role("src/main.rs"), "code");
        assert_eq!(repo_role("apps/api/handlers_test.go"), "test");
        assert_eq!(repo_role("docs/DESIGN.md"), "doc");
        assert_eq!(repo_role("notes.txt"), "doc");
        assert_eq!(repo_role("AGENTS.md"), "guide");
        assert_eq!(repo_role("README.md"), "guide");
        assert_eq!(repo_role("CLAUDE.md"), "guide");
        // path-based guidance roots (mirror the Go guidance walk roots)
        assert_eq!(repo_role(".cursor/rules/style.md"), "guide");
        assert_eq!(repo_role(".openhands/microagents/repo.md"), "guide");
        assert_eq!(repo_role("package.json"), "config");
        assert_eq!(repo_role("logo.png"), "other");
    }

    #[test]
    fn search_returns_doc_hits_for_prose_only_concepts() {
        let repo = indexed_repo(&[
            ("lib.rs", "pub fn compute_widget_total() {}\n"),
            (
                "README.md",
                "# Guide\n\nThe spend guardrail enforces a daily budget ceiling.\nUnique marker zebra_protocol_v7 is documented only here.\n",
            ),
        ]);
        // a concept that exists ONLY in prose, never as a code symbol
        let res = hybrid_search(repo.path(), "ws", "zebra_protocol_v7", 10, None);
        let h = res
            .hits
            .iter()
            .find(|h| h.kind == "doc")
            .unwrap_or_else(|| panic!("expected a doc hit for a prose-only term: {:?}", res.hits));
        assert_eq!(h.path, "README.md");
        assert_eq!(h.lines, Some([1, 4]));
        assert_eq!(h.lanes_matched, vec!["docs"]);
        assert!(
            h.snippet
                .iter()
                .any(|s| s.text.contains("4: Unique marker")),
            "{:?}",
            h.snippet
        );

        // code symbol search still works alongside docs
        let code = hybrid_search(repo.path(), "ws", "compute widget total", 10, None);
        assert!(
            code.hits.iter().any(|h| h.kind == "symbol"),
            "code search must still return code hits: {:?}",
            code.hits
        );
    }

    #[test]
    fn search_ranks_matching_symbol_first() {
        let repo = indexed_repo(&[
            (
                "a.rs",
                "pub fn compute_widget_total() {}\npub fn unrelated_thing() {}\n",
            ),
            ("b.rs", "pub fn other_helper() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "compute_widget_total", 10, None);
        assert!(res.total >= 1);
        let top = &res.hits[0];
        assert_eq!(top.name, "compute_widget_total");
        assert_eq!(
            top.reasons[0],
            "exact declaration match: compute_widget_total"
        );
        // a symbol hit carries its line, span and UID so the agent jumps to the slice.
        assert_eq!(top.line, Some(1));
        assert_eq!(top.lines, Some([1, 1]));
        assert!(top.uid.as_deref().is_some_and(|u| !u.is_empty()));
        assert_eq!(top.snippet[0].text, "1: pub fn compute_widget_total() {}");
    }

    #[test]
    fn search_token_match_and_empty_query() {
        let repo = git_repo(&[("a.rs", "pub fn widget_factory() {}\n")]);
        let res = hybrid_search(repo.path(), "ws", "widget", 10, None);
        assert!(res.hits.iter().any(|h| h.name == "widget_factory"));
        let empty = hybrid_search(repo.path(), "ws", "   ", 10, None);
        assert_eq!(empty.total, 0);
    }

    #[test]
    fn fuzzy_lane_recovers_a_misspelled_name() {
        // "dashbord" (misspelled, no exact token) still surfaces render_dashboard via
        // the trigram sketch; the lane is named for what it is.
        let repo = git_repo(&[
            (
                "ui.rs",
                "pub fn render_dashboard() {}\npub fn save_invoice() {}\n",
            ),
            ("b.rs", "pub fn unrelated_widget() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "dashbord", 10, None);
        let h = res
            .hits
            .iter()
            .find(|h| h.name == "render_dashboard")
            .unwrap_or_else(|| panic!("fuzzy lane should recover the name: {:?}", res.hits));
        assert!(h.lanes_matched.contains(&"fuzzy"), "{:?}", h.lanes_matched);
        assert!(h.reason.contains("fuzzy: name similarity"), "{}", h.reason);
        assert!(!serde_json::to_string(&res).unwrap().contains("semantic"));
    }

    #[test]
    fn legacy_answers_report_the_missing_lanes() {
        let repo = git_repo(&[("a.rs", "pub fn compute_widget() {}\n")]);
        let res = hybrid_search(repo.path(), "ws", "widget", 10, None);
        let hit = res
            .hits
            .iter()
            .find(|h| h.name == "compute_widget")
            .unwrap();
        assert!(
            hit.lanes_matched.contains(&"name"),
            "{:?}",
            hit.lanes_matched
        );
        assert!(hit.uid.is_none() && hit.snippet.is_empty());
        assert!(
            res.degradations
                .iter()
                .any(|d| d.contains("need the code index")),
            "{:?}",
            res.degradations
        );
    }

    #[test]
    fn proximity_lane_reranks_graph_neighbour_up() {
        // Two symbols both match the query "token". Without a seed, the far symbol
        // ranks first (it wins the path tie-break and its shorter name the fuzzy lane).
        // With the seed `resolve_token`, the near symbol (in a file that calls the seed →
        // graph distance 1) is pulled above the far one by the proximity lane.
        let repo = git_repo(&[
            ("auth.rs", "pub fn resolve_token() {}\n"),
            ("near.rs", "pub fn zzz_token_near() { resolve_token(); }\n"),
            ("far.rs", "pub fn token_far() {}\n"),
        ]);
        let pos = |res: &SearchResult, name: &str| res.hits.iter().position(|h| h.name == name);

        let plain = hybrid_search(repo.path(), "ws", "token", 10, None);
        let near_plain = pos(&plain, "zzz_token_near").expect("near present");
        let far_plain = pos(&plain, "token_far").expect("far present");
        assert!(far_plain < near_plain, "baseline order: {:?}", plain.hits);

        let seeded = hybrid_search(repo.path(), "ws", "token", 10, Some("resolve_token"));
        let near_seeded = pos(&seeded, "zzz_token_near").expect("near present");
        let far_seeded = pos(&seeded, "token_far").expect("far present");
        assert!(
            near_seeded < far_seeded,
            "seed should re-rank the graph-near symbol above the far one: {:?}",
            seeded.hits
        );
        let near_hit = &seeded.hits[near_seeded];
        assert!(
            near_hit
                .reason
                .contains("proximity: graph distance 1 from seed resolve_token"),
            "near hit should credit the proximity lane: {}",
            near_hit.reason
        );
    }

    #[test]
    fn auto_seed_from_exact_match_activates_proximity() {
        let repo = git_repo(&[
            ("auth.rs", "pub fn token() {}\n"),
            ("caller.rs", "pub fn token_handler() { token(); }\n"),
            ("far.rs", "pub fn token_orphan() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "token", 10, None);
        let handler = res.hits.iter().find(|h| h.name == "token_handler");
        assert!(
            handler.is_some_and(|h| h.lanes_matched.contains(&"proximity")),
            "exact-match auto-seed should credit the caller via proximity: {:?}",
            res.hits
        );
        let orphan = res.hits.iter().find(|h| h.name == "token_orphan").unwrap();
        assert!(
            !orphan.lanes_matched.contains(&"proximity"),
            "an unrelated symbol must not get a proximity credit: {}",
            orphan.reason
        );
    }

    #[test]
    fn trigram_sketch_is_normalized_and_fuzzy() {
        let a = trigram_sketch("dashboard");
        let norm: f32 = a.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!(
            (norm - 1.0).abs() < 1e-4,
            "sketch must be L2-normalized, got {norm}"
        );
        let near = cosine(&trigram_sketch("dashboard"), &trigram_sketch("dashbord"));
        let far = cosine(&trigram_sketch("dashboard"), &trigram_sketch("invoice"));
        assert!(near > far, "fuzzy near={near} should beat far={far}");
    }

    // With the neural lane compiled in, a missing/empty/incomplete model directory must
    // load to None so name_vector() transparently falls back to the trigram sketch —
    // enabling the feature without shipping a valid model never breaks retrieval.
    #[cfg(feature = "semantic-onnx")]
    #[test]
    fn onnx_lane_falls_back_when_model_dir_invalid() {
        use std::path::Path;
        assert!(onnx_embed::load_user_defined(Path::new("/nonexistent/xmustard/model")).is_none());
        let empty = tempfile::tempdir().unwrap();
        assert!(onnx_embed::load_user_defined(empty.path()).is_none());
        let partial = tempfile::tempdir().unwrap();
        std::fs::write(partial.path().join("model.onnx"), b"not a real graph").unwrap();
        assert!(onnx_embed::load_user_defined(partial.path()).is_none());
        let v = name_vector("dashboard ledger");
        let norm: f32 = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!(
            (norm - 1.0).abs() < 1e-4,
            "fallback vector must be normalized"
        );
    }

    /// A Go and a Rust file for each place a term can sit, with a distinct term per file
    /// so every hit is attributable to exactly one place: the chunk's own text or the
    /// `path` column. The one term in a chunk's text appears in a Go *and* a Rust file
    /// (`zeppelinprobe`), so a single query crosses both languages.
    ///
    /// A chunk's `body` column is `tokenize_without_secrets` of the chunk's own bytes, and
    /// chunks are cut on declarations, so the `body` column of a declaration includes its
    /// signature line and its leading doc comment. A term only in the `path` column is
    /// therefore the case the body lane excludes. The two path terms share no token with
    /// the body term, because the lanes fall back to OR over the query's tokens and a
    /// shared stem would make a path query match the body files.
    ///
    /// `flimflamtarget` exists only as a declared name, in Go and in Rust, and its file's
    /// body never repeats it. Only the name lane can find it, so it is the case that
    /// proves a `lang` filter reaches the graph-side lanes and not just the bm25 SQL.
    fn lang_and_body_repo() -> TempDir {
        indexed_repo(&[
            // the chunk-text term, in a function body, in Go and in Rust
            (
                "svc/handler.go",
                "package svc\n\n// unrelated prose\nfunc serve() {\n\tzeppelinprobe()\n}\n",
            ),
            ("src/lib.rs", "pub fn serve() {\n    zeppelinprobe();\n}\n"),
            // a term in the `path` column only
            ("svc/quixoticroute.go", "package svc\n\nfunc other() {}\n"),
            ("src/quixoticroute.rs", "pub fn other() {}\n"),
            // a term in the declared name only, in both languages, with no other match
            (
                "svc/flimflam.go",
                "package svc\n\nfunc flimflamtarget() { work() }\n",
            ),
            ("src/flimflam.rs", "pub fn flimflamtarget() { work() }\n"),
        ])
    }

    fn text_search(repo: &TempDir, query: &str, lang: Option<&str>) -> SearchResult {
        search(
            repo.path(),
            "ws",
            None,
            query,
            &SearchOptions {
                limit: 20,
                mode: SearchMode::Text,
                lang,
                ..Default::default()
            },
        )
    }

    // Text mode scores the body lane alone. Both chunks below carry the term in their own
    // text, and one of them also has it in its declared name. The name lane is what would
    // let a declared name add weight on top of a body match, so a text hit must report
    // and score `body` and nothing else — a name match is not extra evidence here.
    #[test]
    fn text_mode_ranks_on_the_body_lane_alone() {
        let repo = indexed_repo(&[
            // the term in this one's body and in its declared name as well
            (
                "svc/widget.go",
                "package svc\n\n// unrelated prose\nfunc zeppelinprobe() { settle() }\n",
            ),
            // the term in the body only, with no name or path match anywhere
            (
                "svc/other.go",
                "package svc\n\nfunc settle() {\n\tzeppelinprobe()\n}\n",
            ),
        ]);

        let res = text_search(&repo, "zeppelinprobe", None);
        assert_eq!(res.hits.len(), 2, "{:?}", res.hits);
        for h in &res.hits {
            assert_eq!(
                h.lanes_matched,
                vec!["body"],
                "text mode reports only the lane it ran: {h:?}"
            );
            assert_eq!(h.scores.get("name"), None, "{h:?}");
            assert_eq!(h.scores.get("fuzzy"), None, "{h:?}");
            assert_eq!(h.scores.get("structural"), None, "{h:?}");
            assert_eq!(h.scores.get("proximity"), None, "{h:?}");
            assert!(
                !h.reason.contains("declaration"),
                "a declared name is not evidence in text mode: {}",
                h.reason
            );
        }

        // A declared name is not extra evidence, but it is not an exclusion either: a
        // declaration chunk's body includes its signature line, so this fixture's own
        // signature is a body match. What the test pins is that nothing beyond the body
        // lane contributed.
    }

    // `files.lang` holds lowercase pack names. A caller writing `Go` must filter exactly as
    // `go` does: the graph lanes lowercase the value, so if the SQL lane read the caller's
    // spelling the two would disagree and the body hits would vanish.
    #[test]
    fn lang_filter_is_case_insensitive() {
        let repo = lang_and_body_repo();
        let lower = text_search(&repo, "zeppelinprobe", Some("go"));
        let mixed = text_search(&repo, "zeppelinprobe", Some("Go"));
        assert_eq!(lower.hits.len(), 1, "{:?}", lower.hits);
        assert_eq!(
            mixed.hits.len(),
            lower.hits.len(),
            "lang=Go must filter as lang=go: {:?}",
            mixed.hits
        );
        assert_eq!(mixed.hits[0].path, lower.hits[0].path);
    }

    // A language filter does not merely drop doc hits after the fact: the docs lane is not
    // asked for at all. `DOCS_K` reads 30 rows plus the OR fallback, and every one of them
    // is discarded because a docs path has no code language pack. The observable
    // difference is that a filtered search reads no doc row, which a hit-level assertion
    // cannot distinguish from a lane that read them and dropped them.
    #[test]
    fn a_language_filter_asks_for_no_docs_rows() {
        let repo = indexed_repo(&[
            (
                "svc/handler.go",
                "package svc\n\nfunc settle() {\n\tledger()\n}\n",
            ),
            (
                "docs/PLAN.md",
                "# Plan\n\nUnique marker zephyr_quota is documented only here.\n",
            ),
        ]);
        let unfiltered = search(
            repo.path(),
            "ws",
            None,
            "zephyr_quota",
            &SearchOptions {
                limit: 20,
                ..Default::default()
            },
        );
        assert!(
            unfiltered.hits.iter().any(|h| h.kind == "doc"),
            "the fixture must reach the docs lane unfiltered: {:?}",
            unfiltered.hits
        );

        // Read the lane window directly. `fts::lanes` is the only place the docs k is
        // decided, so this asserts on it rather than on a hit the search would drop.
        let source = crate::symbolgraph::query_source_for(repo.path(), "ws", None);
        let opened = text_index(&source, &mut Vec::new()).expect("the fixture indexes");
        let terms = vec!["zephyr_quota".to_string()];
        opened
            .snapshot
            .with_text(|conn| {
                let both = crate::index::fts::lanes(conn, &terms, None, 10, 30, None, false)
                    .expect("the unfiltered lanes read");
                assert!(!both.docs.is_empty(), "the fixture reaches the docs lane");
                let filtered =
                    crate::index::fts::lanes(conn, &terms, None, 10, 30, Some("go"), false)
                        .expect("the filtered lanes read");
                assert!(
                    filtered.docs.is_empty(),
                    "a language filter reads no docs rows: {:?}",
                    filtered.docs
                );
                Ok(())
            })
            .expect("the text snapshot closes");
    }

    // `lang` filters the docs lane too. The docs lane has no `files.lang` to read, so a
    // filtered hybrid used to bind an empty language there and keep serving prose from
    // documents in every language beside code hits narrowed to one.
    #[test]
    fn lang_filter_drops_the_docs_lane() {
        let repo = indexed_repo(&[
            (
                "svc/handler.go",
                "package svc\n\nfunc settle() {\n\tledger()\n}\n",
            ),
            (
                "docs/PLAN.md",
                "# Plan\n\nUnique marker zephyr_quota is documented only here.\n",
            ),
        ]);
        let unfiltered = search(
            repo.path(),
            "ws",
            None,
            "zephyr_quota",
            &SearchOptions {
                limit: 20,
                ..Default::default()
            },
        );
        assert!(
            unfiltered.hits.iter().any(|h| h.kind == "doc"),
            "the fixture must reach the docs lane unfiltered: {:?}",
            unfiltered.hits
        );

        let filtered = search(
            repo.path(),
            "ws",
            None,
            "zephyr_quota",
            &SearchOptions {
                limit: 20,
                lang: Some("go"),
                ..Default::default()
            },
        );
        for h in &filtered.hits {
            assert_ne!(
                h.kind, "doc",
                "lang=go must not answer with a doc hit: {h:?}"
            );
        }
    }

    // `mode=text` answers from the chunk's own text: a term that exists only in the
    // `path` column is not a text hit, in either language, and a chunk-text hit names the
    // `body` lane rather than the whole-chunk bm25 it did not run.
    #[test]
    fn text_mode_matches_the_chunk_text_and_not_the_path() {
        let repo = lang_and_body_repo();

        let res = text_search(&repo, "zeppelinprobe", None);
        let mut paths: Vec<&str> = res.hits.iter().map(|h| h.path.as_str()).collect();
        paths.sort_unstable();
        assert_eq!(paths, ["src/lib.rs", "svc/handler.go"], "{:?}", res.hits);
        for h in &res.hits {
            assert_eq!(h.kind, "symbol", "a body hit names its declaration: {h:?}");
            assert!(h.lanes_matched.contains(&"body"), "{:?}", h.lanes_matched);
            assert!(
                h.reason.contains("body:"),
                "the reason must name the lane it ran: {}",
                h.reason
            );
        }

        // a term in the `path` column alone is not a body hit
        for query in ["quixoticroute"] {
            let res = text_search(&repo, query, None);
            assert!(
                res.hits.is_empty(),
                "{query} is a path match, not a chunk-text match: {:?}",
                res.hits
            );
        }
    }

    // The same term in a Go chunk and a Rust chunk: `lang` keeps one and drops the
    // other, an unknown language matches nothing, and an empty one is today's behaviour.
    #[test]
    fn lang_filter_keeps_only_that_language_s_body_hits() {
        let repo = lang_and_body_repo();

        // one term in a Go and a Rust chunk body, so one query is the shared case
        let both = "zeppelinprobe";
        let all = text_search(&repo, both, None);
        assert_eq!(all.hits.len(), 2, "{:?}", all.hits);

        let go_only = text_search(&repo, both, Some("go"));
        assert_eq!(go_only.hits.len(), 1, "{:?}", go_only.hits);
        assert_eq!(go_only.hits[0].path, "svc/handler.go");

        let rust_only = text_search(&repo, both, Some("rust"));
        assert_eq!(rust_only.hits.len(), 1, "{:?}", rust_only.hits);
        assert_eq!(rust_only.hits[0].path, "src/lib.rs");

        // an unknown language is a filter that matches nothing, not one that is ignored
        assert!(text_search(&repo, both, Some("cobol")).hits.is_empty());
        // an empty filter is today's behaviour
        assert_eq!(text_search(&repo, both, Some("")).hits.len(), 2);
    }

    // The hybrid's own `lang` filter: it narrows the answer to one language and every
    // hit it keeps is a hit the unfiltered query also found. The hit's own reason is not
    // compared, because a smaller lane window can return a different chunk of a long
    // declaration, and the bm25 row a hit cites is a row of that window.
    #[test]
    fn lang_filter_narrows_the_hybrid_without_inventing_hits() {
        let repo = lang_and_body_repo();
        let unfiltered = hybrid_search(repo.path(), "ws", "zeppelinprobe", 20, None);
        let go_only = search(
            repo.path(),
            "ws",
            None,
            "zeppelinprobe",
            &SearchOptions {
                limit: 20,
                lang: Some("go"),
                ..Default::default()
            },
        );
        assert!(!go_only.hits.is_empty(), "lang=go must match the Go files");
        assert!(
            go_only.hits.iter().all(|h| h.path.ends_with(".go")),
            "every hit must be a Go file: {:?}",
            go_only.hits
        );
        assert!(
            unfiltered.hits.len() >= go_only.hits.len(),
            "the filtered answer must be a subset: {} vs {}",
            unfiltered.hits.len(),
            go_only.hits.len()
        );
        for h in &go_only.hits {
            assert!(
                unfiltered
                    .hits
                    .iter()
                    .any(|u| u.path == h.path && u.name == h.name && u.line == h.line),
                "{} is not a hit of the unfiltered query",
                h.name
            );
        }
    }

    // `path_glob` and `lang` compose: each narrows, and together they intersect.
    #[test]
    fn lang_filter_composes_with_path_glob() {
        let repo = lang_and_body_repo();
        let res = search(
            repo.path(),
            "ws",
            None,
            "zeppelinprobe",
            &SearchOptions {
                limit: 20,
                lang: Some("go"),
                path_glob: Some("src/**"),
                ..Default::default()
            },
        );
        assert!(
            res.hits.is_empty(),
            "no file is both Go and under src/: {:?}",
            res.hits
        );
    }

    // A `lang` filter reaches the graph-side lanes and not only the bm25 SQL.
    // `flimflamtarget` is a declaration name, so a filter that skipped the name and file
    // lanes would answer `lang=go` with a Rust symbol and a Rust file row.
    #[test]
    fn lang_filter_reaches_the_name_lane_and_not_only_the_bm25_sql() {
        let repo = lang_and_body_repo();

        let both = search(
            repo.path(),
            "ws",
            None,
            "flimflamtarget",
            &SearchOptions {
                limit: 20,
                ..Default::default()
            },
        );
        assert_eq!(
            both.hits.iter().filter(|h| h.kind == "symbol").count(),
            2,
            "one symbol per language: {:?}",
            both.hits
        );
        // the file lane sees it too, so the filter has a subject on that lane as well
        assert!(
            both.hits.iter().any(|h| h.kind == "file"),
            "the fixture must exercise the file lane: {:?}",
            both.hits
        );

        for (lang, want) in [("go", ".go"), ("rust", ".rs")] {
            let filtered = search(
                repo.path(),
                "ws",
                None,
                "flimflamtarget",
                &SearchOptions {
                    limit: 20,
                    lang: Some(lang),
                    ..Default::default()
                },
            );
            assert!(
                !filtered.hits.is_empty(),
                "lang={lang} must still answer: {:?}",
                filtered.hits
            );
            for h in &filtered.hits {
                assert!(
                    h.path.ends_with(want),
                    "lang={lang} returned {}: {:?}",
                    h.path,
                    filtered.hits
                );
            }
            assert_eq!(
                filtered.hits.iter().filter(|h| h.kind == "symbol").count(),
                1,
                "lang={lang}: {:?}",
                filtered.hits
            );
        }
    }
}

/// The `mode=exact|regex` lane's own tests (WS-59c, PAR-RET-09). They sit in their own
/// module because they assert the SCAN's contract, which is not the fusion's: spans, the
/// three caps and the scan's degradation names. `git_repo` is the unindexed fixture the
/// scan needs, and indexing is deliberately NOT done — a scan reads the working tree, so a
/// test that indexed first would be testing the index.
#[cfg(test)]
mod scan_tests {
    use super::*;
    use tempfile::TempDir;

    fn git_repo(files: &[(&str, &str)]) -> TempDir {
        let dir = TempDir::new().unwrap();
        for (rel, content) in files {
            if let Some(parent) = Path::new(rel).parent() {
                std::fs::create_dir_all(dir.path().join(parent)).unwrap();
            }
            std::fs::write(dir.path().join(rel), content).unwrap();
        }
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
            vec!["add", "-A"],
            vec!["commit", "-qm", "c"],
        ] {
            std::process::Command::new("git")
                .arg("-C")
                .arg(dir.path())
                .args(&args)
                .output()
                .unwrap();
        }
        dir
    }

    fn scan_repo(repo: &TempDir, query: &str, mode: SearchMode) -> SearchResult {
        search(
            repo.path(),
            "ws",
            None,
            query,
            &SearchOptions {
                limit: 50,
                mode,
                ..Default::default()
            },
        )
    }

    // The lane's whole point: the query as written, found where it is, once per
    // occurrence. The oracle is the file's own bytes, not the lane's output: the term is
    // planted on two known lines of a known file and the span must name both.
    #[test]
    fn exact_mode_reports_a_span_per_occurrence_with_its_path_line_byte_and_column() {
        let repo = git_repo(&[(
            "svc/handler.go",
            "package svc\n\nfunc serve() {\n\tzeppelinprobe()\n\tzeppelinprobe()\n}\n",
        )]);
        let res = scan_repo(&repo, "zeppelinprobe", SearchMode::Exact);
        assert_eq!(res.hits.len(), 0, "the scan ranks nothing: {:?}", res.hits);
        assert_eq!(res.spans.len(), 2, "{:?}", res.spans);
        assert!(
            res.spans.iter().all(|s| s.path == "svc/handler.go"),
            "{:?}",
            res.spans
        );
        // line 4 and line 5, 1-based, in the file as written above
        assert_eq!(res.spans[0].line, 4, "{:?}", res.spans[0]);
        assert_eq!(res.spans[1].line, 5, "{:?}", res.spans[1]);
        assert_eq!(
            res.spans[0].column, 2,
            "a tab then the call: {:?}",
            res.spans[0]
        );
        assert_eq!(res.spans[0].len, "zeppelinprobe".len());
        // `byte` is the file offset, which is what a follow-up positional read takes. The
        // oracle is the literal: the file's own index_of the first occurrence.
        let body = "package svc\n\nfunc serve() {\n\tzeppelinprobe()\n\tzeppelinprobe()\n}\n";
        assert_eq!(
            res.spans[0].byte,
            body.find("zeppelinprobe").unwrap(),
            "{:?}",
            res.spans[0]
        );
        assert!(!res.truncated, "nothing was cut: {:?}", res.degradations);
        assert_eq!(res.cut, None);
        // `bytes_scanned` reports what was read, so it covers the whole file that was
        // scanned: the fixture's length, and no more.
        let body_len = body.len() as u64;
        assert_eq!(
            res.bytes_scanned,
            Some(body_len),
            "the scan read the one file it listed"
        );
    }

    // A term twice on ONE line is two spans, not one: the lane reports occurrences, so
    // collapsing them would make a count of occurrences a count of lines.
    #[test]
    fn two_occurrences_on_one_line_are_two_spans() {
        let repo = git_repo(&[("a.txt", "needle and needle again\n")]);
        let res = scan_repo(&repo, "needle", SearchMode::Exact);
        assert_eq!(res.spans.len(), 2, "{:?}", res.spans);
        assert_eq!(
            res.spans[0].line, res.spans[1].line,
            "both on line 1: {:?}",
            res.spans
        );
        assert_ne!(res.spans[0].byte, res.spans[1].byte);
        assert!(res.spans[0].byte < res.spans[1].byte, "ordered by offset");
    }

    // "Exact" is case-sensitive and matches inside a line, not only at its start. Both
    // halves matter: an index lane tokenizes and casefolds, so a case-sensitive literal
    // match is exactly what the indexed lanes cannot do.
    #[test]
    fn exact_mode_is_case_sensitive_and_matches_inside_a_line() {
        let repo = git_repo(&[("a.txt", "let Needle = 1;\nlet needle = 2;\n")]);
        let upper = scan_repo(&repo, "Needle", SearchMode::Exact);
        assert_eq!(
            upper.spans.len(),
            1,
            "only the capital N: {:?}",
            upper.spans
        );
        assert_eq!(upper.spans[0].line, 1);
        let lower = scan_repo(&repo, "needle", SearchMode::Exact);
        assert_eq!(
            lower.spans.len(),
            1,
            "only the lowercase n: {:?}",
            lower.spans
        );
        assert_eq!(lower.spans[0].line, 2);
    }

    // A regex the exact lane cannot express. The oracle is the planted value: the span's
    // line must be the line carrying the digits, and its `len` the digit run's length.
    #[test]
    fn regex_mode_matches_what_the_exact_lane_cannot_express() {
        let repo = git_repo(&[("a.txt", "version 4242 shipped\nversion 7 shipped\n")]);
        let res = scan_repo(&repo, r"version \d+", SearchMode::Regex);
        assert_eq!(res.spans.len(), 2, "{:?}", res.spans);
        assert_eq!(res.spans[0].line, 1);
        assert_eq!(res.spans[0].len, "version 4242".len());
        // the exact lane takes the pattern literally, so it finds nothing: this is the
        // pair that shows the two modes are different surfaces, not two spellings.
        let exact = scan_repo(&repo, r"version \d+", SearchMode::Exact);
        assert_eq!(exact.spans.len(), 0, "{:?}", exact.spans);
    }

    // A pattern the engine refuses is bad input, not a query matching nothing. The lane
    // reports it and searches nothing, and the reported message carries the engine's own
    // complaint so a caller can act on it.
    #[test]
    fn an_uncompilable_pattern_is_reported_and_searches_nothing() {
        let repo = git_repo(&[("a.txt", "some text\n")]);
        let res = scan_repo(&repo, "a(", SearchMode::Regex);
        assert!(res.spans.is_empty(), "{:?}", res.spans);
        assert_eq!(res.bytes_scanned, Some(0), "nothing was read");
        assert!(
            res.degradations
                .iter()
                .any(|d| d.contains("regex pattern is not valid")),
            "{:?}",
            res.degradations
        );
        assert!(!res.truncated, "a refused query is not a truncated answer");
    }

    // The line cap is honoured and NAMED. The oracle is the fixture: with a cap of 1 and
    // three occurrences, exactly one span is listed, `truncated` is true and `cut` says
    // which cap did it, so a caller reads the bound rather than inferring it.
    #[test]
    fn the_line_cap_fires_and_names_itself() {
        let repo = git_repo(&[("a.txt", "needle\nneedle\nneedle\n")]);
        let res = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                scan_lines: 1,
                ..Default::default()
            },
        );
        assert_eq!(res.spans.len(), 1, "{:?}", res.spans);
        assert!(res.truncated, "a cap fired: {:?}", res.degradations);
        assert_eq!(res.cut, Some(crate::exactscan::Cut::Lines));
        assert_eq!(res.total, 1, "only what it listed is counted");
        assert!(
            res.degradations.iter().any(|d| d.contains("line cap")),
            "the cap is named in prose too: {:?}",
            res.degradations
        );
    }

    // The byte cap is honoured and `bytes_scanned` reports what was read, never more than
    // the cap. The fixture is one file past the cap, so the scan must stop inside it.
    #[test]
    fn the_byte_cap_fires_and_bytes_scanned_never_exceeds_it() {
        let body = "needle ".repeat(400);
        let repo = git_repo(&[("a.txt", &body)]);
        let cap = 64u64;
        let res = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                scan_bytes: cap,
                ..Default::default()
            },
        );
        assert!(res.truncated, "the cap fired: {:?}", res.degradations);
        assert_eq!(res.cut, Some(crate::exactscan::Cut::Bytes));
        let scanned = res.bytes_scanned.unwrap();
        assert!(scanned <= cap, "read {scanned} against a {cap}-byte cap");
        assert!(scanned > 0, "it read something before the cap");
    }

    // A secret path is never read and never appears in the spans, and it is counted as an
    // explicit loss rather than skipped silently. This is the guard's refusal path
    // (§5.1 rule 7): the test asserts the bytes were not searched, not just that they were
    // absent from the answer.
    #[test]
    fn a_secret_path_is_never_read_and_is_an_explicit_loss() {
        let repo = git_repo(&[
            ("ok.txt", "needle here\n"),
            (".env", "NEEDLE=supersecretvalue\n"),
        ]);
        let res = scan_repo(&repo, "NEEDLE", SearchMode::Exact);
        assert!(
            res.spans.iter().all(|s| s.path != ".env"),
            "a secret path must never appear in a span: {:?}",
            res.spans
        );
        assert!(
            res.truncated,
            "a file could not be read: {:?}",
            res.degradations
        );
        assert!(
            res.degradations
                .iter()
                .any(|d| d.contains(".env") && d.contains("secret path")),
            "the loss names the path: {:?}",
            res.degradations
        );
        assert!(
            res.coverage
                .loss_counts
                .get("exact_scan_secret_path")
                .copied()
                .unwrap_or(0)
                >= 1,
            "the loss is counted under its own key: {:?}",
            res.coverage.loss_counts
        );
    }

    // The filters mean the same thing in the scan as in the hybrid, and the scan invents
    // nothing: its answer is a subset of the unfiltered one, intersected with the glob.
    #[test]
    fn the_scan_composes_with_path_glob_and_lang_and_invents_nothing() {
        let repo = git_repo(&[
            (
                "svc/handler.go",
                "package svc\n\nfunc serve() { zeppelinprobe() }\n",
            ),
            ("src/lib.rs", "pub fn serve() { zeppelinprobe(); }\n"),
        ]);
        let all = scan_repo(&repo, "zeppelinprobe", SearchMode::Exact);
        assert_eq!(all.spans.len(), 2, "{:?}", all.spans);
        let globbed = search(
            repo.path(),
            "ws",
            None,
            "zeppelinprobe",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                path_glob: Some("**/*.go"),
                ..Default::default()
            },
        );
        assert_eq!(globbed.spans.len(), 1, "{:?}", globbed.spans);
        assert!(
            globbed.spans[0].path.ends_with(".go"),
            "{:?}",
            globbed.spans
        );
        let rust_only = search(
            repo.path(),
            "ws",
            None,
            "zeppelinprobe",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                lang: Some("rust"),
                ..Default::default()
            },
        );
        assert_eq!(rust_only.spans.len(), 1, "{:?}", rust_only.spans);
        assert!(
            rust_only.spans[0].path.ends_with(".rs"),
            "{:?}",
            rust_only.spans
        );
    }

    // An over-ceiling cap is refused by the library boundary, and the refusal reaches the
    // caller as a degradation rather than a clamped scan.
    #[test]
    fn an_over_ceiling_cap_is_refused_and_searches_nothing() {
        let repo = git_repo(&[("a.txt", "needle\n")]);
        let res = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                scan_bytes: crate::exactscan::DEFAULT_MAX_BYTES + 1,
                ..Default::default()
            },
        );
        assert!(res.spans.is_empty(), "{:?}", res.spans);
        assert_eq!(res.bytes_scanned, Some(0), "nothing was read");
        assert!(
            res.degradations.iter().any(|d| d.contains("max_bytes")),
            "{:?}",
            res.degradations
        );
    }

    // The scan reads the working tree, not the index: an unindexed repository answers, and
    // it reports no index degradation because it never opened a store. This is the shape
    // `search`'s own docs promise for the ranked lanes, inverted.
    #[test]
    fn the_scan_answers_from_an_unindexed_repository_without_an_index_degradation() {
        let repo = git_repo(&[("a.txt", "needle\n")]);
        let res = scan_repo(&repo, "needle", SearchMode::Exact);
        assert_eq!(res.spans.len(), 1, "{:?}", res.spans);
        assert!(
            !res.degradations.iter().any(|d| d.contains("bm25")),
            "a scan did not run a text lane, so it owes no bm25 degradation: {:?}",
            res.degradations
        );
    }

    // The scan lists the TRACKED TREE, not the files a language pack claims. `.txt` is not
    // in any pack, so a scan that took its file list from the symbol graph would find
    // nothing here — and a grep that cannot reach a README, a Makefile, a `.yaml` or a
    // `.env.example` is not a grep. This is the case that decides the listing source, and
    // it is why `tracked_paths` exists.
    #[test]
    fn the_scan_reaches_a_file_no_language_pack_claims() {
        let repo = git_repo(&[
            ("notes.txt", "needle in plain text\n"),
            ("config.yaml", "key: needle\n"),
            ("src/lib.rs", "pub fn f() {}\n"),
        ]);
        let res = scan_repo(&repo, "needle", SearchMode::Exact);
        let mut paths: Vec<&str> = res.spans.iter().map(|s| s.path.as_str()).collect();
        paths.sort();
        assert_eq!(
            paths,
            vec!["config.yaml", "notes.txt"],
            "the scan must reach files outside the language packs: {:?}",
            res.degradations
        );
    }

    // The same inputs produce the same bytes twice. A scan's order comes from Git's own file
    // order, and this is the property the retrieval gate and the hybrid's own
    // byte-identity test rely on.
    #[test]
    fn the_scan_is_byte_identical_across_runs() {
        let repo = git_repo(&[
            ("b.txt", "needle b\n"),
            ("a.txt", "needle a\n"),
            ("c.txt", "needle c\n"),
        ]);
        let one = scan_repo(&repo, "needle", SearchMode::Exact);
        let two = scan_repo(&repo, "needle", SearchMode::Exact);
        assert_eq!(one.spans, two.spans, "the same corpus, the same spans");
    }

    // The scan reads bytes without feeding them through a digest, so both hash counters
    // remain zero. Its own bytes_scanned member is the read count for this lane.
    #[test]
    fn its_pass_counters_show_it_stats_its_opens_and_hashes_nothing() {
        let repo = git_repo(&[
            ("a.go", "needle\n"),
            ("b.go", "needle\n"),
            ("c.txt", "needle\n"),
        ]);
        let call = crate::passcount::Call::begin();
        let r = scan_repo(&repo, "needle", SearchMode::Exact);
        let counts = call.counts();
        drop(call);
        assert_eq!(r.spans.len(), 3, "{:?}", r.spans);
        assert_eq!(
            counts.get(crate::passcount::Pass::FilesHashed),
            0,
            "the scan hashes nothing: {:?}",
            counts
        );
        assert_eq!(
            counts.get(crate::passcount::Pass::BytesHashed),
            0,
            "the scan does not hold the hashing counters: {:?}",
            counts
        );
        assert_eq!(
            r.bytes_scanned,
            Some(21),
            "three 7-byte files, each read once"
        );
    }

    // A match carried from the prior slice is not reported twice, and one that straddles
    // the boundary is still found. `scan_slice: Some(64)` forces both cases into two reads.
    #[test]
    fn a_match_across_a_read_boundary_is_found_once() {
        let body = format!("needle{}needle{}", "x".repeat(54), "\n");
        let repo = git_repo(&[("a.txt", &body)]);
        let res = search(
            repo.path(),
            "ws",
            None,
            "needle",
            &SearchOptions {
                limit: 50,
                mode: SearchMode::Exact,
                // the slice size, not the cap, is what forces the boundary: the needle
                // runs 60..66 and the slice ends at 64
                scan_slice: Some(64),
                ..Default::default()
            },
        );
        assert!(
            res.bytes_scanned.unwrap() > 64,
            "the fixture must really read past the first slice, or it proves nothing: {:?}",
            res
        );
        let found: Vec<usize> = res.spans.iter().map(|s| s.byte).collect();
        assert_eq!(found, vec![0, 60], "each planted match is reported once");
    }
}
