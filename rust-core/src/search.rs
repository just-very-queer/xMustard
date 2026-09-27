//! Hybrid repo search (knowledge layer, WS-18). Up to six lanes are fused with
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
/// Dimensions of the trigram sketch.
const SKETCH_DIM: usize = 256;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Lane {
    Bm25,
    Name,
    Fuzzy,
    Structural,
    Proximity,
    Docs,
}

impl Lane {
    const ALL: [Lane; 6] = [
        Lane::Bm25,
        Lane::Name,
        Lane::Fuzzy,
        Lane::Structural,
        Lane::Proximity,
        Lane::Docs,
    ];

    fn name(self) -> &'static str {
        match self {
            Lane::Bm25 => "bm25",
            Lane::Name => "name",
            Lane::Fuzzy => fuzzy_lane(),
            Lane::Structural => "structural",
            Lane::Proximity => "proximity",
            Lane::Docs => "docs",
        }
    }

    /// RRF weight of the lane (see the module docs).
    fn weight(self) -> f64 {
        match self {
            Lane::Bm25 | Lane::Name | Lane::Docs => 1.0,
            Lane::Fuzzy | Lane::Proximity => 0.5,
            Lane::Structural => 0.25,
        }
    }

    fn bit(self) -> u8 {
        1 << self as u8
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

/// Search arguments beyond the query.
#[derive(Debug, Clone, Default)]
pub struct SearchOptions<'a> {
    pub limit: usize,
    pub offset: usize,
    /// Anchor symbol of the proximity lane.
    pub seed: Option<&'a str>,
    /// Only paths matching this glob (gitignore semantics).
    pub path_glob: Option<&'a str>,
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
    fn lane_key(&self, lane: Lane) -> Option<f64> {
        let positive = |x: f64| (x > 0.0).then_some(x);
        match lane {
            Lane::Bm25 => self.code.map(|i| -f64::from(i)),
            Lane::Docs => self.doc.map(|i| -f64::from(i)),
            Lane::Name => positive(self.name_score),
            Lane::Fuzzy => (self.fuzzy >= FUZZY_GATE).then_some(f64::from(self.fuzzy)),
            Lane::Structural => positive(self.structural as f64),
            Lane::Proximity => positive(self.proximity),
        }
    }

    /// The raw lane score reported in `scores`.
    fn lane_score(&self, lane: Lane, text: &TextLanes) -> f64 {
        match lane {
            Lane::Bm25 => self.code.map_or(0.0, |i| text.code[i as usize].bm25),
            Lane::Docs => self.doc.map_or(0.0, |i| text.docs[i as usize].bm25),
            Lane::Name => self.name_score,
            Lane::Fuzzy => f64::from(self.fuzzy),
            Lane::Structural => self.structural as f64,
            Lane::Proximity => self.proximity,
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

/// Read a candidate's declaration, chunk and text, and compute its rerank features.
fn context(conn: Option<&Connection>, root: &Path, q: &Query, c: &Cand, text: &TextLanes) -> Ctx {
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
    features.decl = rerank::decl_match(q, decl_name);
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

/// The full search: a page of the final order under `opts`.
pub fn search(
    root: &Path,
    workspace_id: &str,
    identity_key: Option<&str>,
    query: &str,
    opts: &SearchOptions<'_>,
) -> SearchResult {
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
    let admit = |p: &str| glob.as_ref().is_none_or(|g| g.matches(p.as_bytes()));

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
    let index = text_index(&source, &mut degradations);
    let text: TextLanes = match index {
        Some(opened) => {
            let read = opened
                .snapshot
                .with_text(|conn| fts::lanes(conn, &q.terms, opts.path_glob, BM25_K, DOCS_K));
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
    // symbols grouped by name, so the per-name work runs once per distinct name.
    let n = graph.symbol_count().max(1) as f64;
    let mut df = vec![0usize; qtokens.len()];
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
    let mut share = |p: &str| -> Rc<str> {
        if let Some(r) = shared_paths.get(p) {
            return r.clone();
        }
        let r: Rc<str> = p.into();
        shared_paths.insert(r.clone());
        r
    };
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
            let path = share(sym.path);
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
            if text_paths.contains(f.path) {
                inbound.insert(f.path.to_string(), f.inbound);
            }
            if !admit(f.path) {
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
                path: share(f.path),
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
            path: share(&h.loc.path),
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
            path: share(&h.loc.path),
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
    drop(shared_paths);
    drop(by_decl);

    // Proximity lane: the seed (explicit, else the exact query→symbol match first in
    // path order) and its blast radius over the reference graph, by file.
    exact.sort();
    let effective_seed: Option<String> = match opts.seed {
        Some(s) if !s.trim().is_empty() => Some(s.trim().to_string()),
        _ => exact.first().map(|(_, _, name)| name.to_string()),
    };
    let seed_impact = effective_seed.as_ref().and_then(|seed_name| {
        graph
            .impact(seed_name, PROXIMITY_DEPTH)
            .inspect_err(|e| read_errors.push(("seed impact", e.clone())))
            .ok()
    });
    if let Some(impact) = seed_impact {
        let mut prox: HashMap<String, f64> = HashMap::new();
        for d in &impact.defined_in {
            prox.insert(d.clone(), 1.0); // the seed's own file: distance 0
        }
        for f in &impact.impacted {
            prox.entry(f.path.clone())
                .or_insert(1.0 / (f.distance as f64 + 1.0));
        }
        for c in cands.iter_mut().filter(|c| c.kind != Kind::Doc) {
            if let Some(&p) = prox.get(&*c.path) {
                c.proximity = p;
            }
        }
    }

    // ---- Reciprocal Rank Fusion ----
    let mut rrf = vec![0f64; cands.len()];
    let mut in_lanes = vec![0u8; cands.len()];
    for lane in Lane::ALL {
        let mut ranked: Vec<(f64, u32)> = cands
            .iter()
            .enumerate()
            .filter_map(|(i, c)| c.lane_key(lane).map(|k| (k, i as u32)))
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
            let c = context(conn, root, &q, &cands[i], &text);
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
                ..context(conn, root, &q, &cands[i], &text)
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
    };
    let hits: Vec<SearchHit> = order
        .get(opts.offset..page_end)
        .unwrap_or(&[])
        .iter()
        .map(|&i| {
            let cx = ctx.remove(&i).unwrap_or_default();
            render.hit(&cands[i], cx, (rrf[i], final_score[i]), in_lanes[i])
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
}

impl Render<'_> {
    /// Build the returned hit of one candidate from its context, its (fused, final)
    /// scores and the lanes that ranked it.
    fn hit(&self, c: &Cand, cx: Ctx, (rrf, score): (f64, f64), lanes: u8) -> SearchHit {
        let (qtokens, text, seed) = (self.qtokens, self.text, self.seed);
        let decl_name = cx
            .decl
            .as_ref()
            .map_or_else(|| c.name.to_string(), |d| d.name.clone());
        let mut reasons: Vec<String> = Vec::new();
        cx.features.reasons(&decl_name, &mut reasons);
        let matched: Vec<&str> = Lane::ALL
            .into_iter()
            .filter(|l| lanes & l.bit() != 0)
            .map(Lane::name)
            .collect();
        let row = |i: Option<u32>, rows: &[TextHit]| i.map(|i| rows[i as usize].clone());
        let (code_row, doc_row) = (row(c.code, &text.code), row(c.doc, &text.docs));
        for lane in Lane::ALL.into_iter().filter(|l| lanes & l.bit() != 0) {
            let reason = match lane {
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
            .map(|l| (l.name(), round6(c.lane_score(l, text))))
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
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
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
}
