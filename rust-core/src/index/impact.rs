//! Impact v2 (PAR-IMP-04, PAR-IMP-05; WS-35): the dependents (upstream) or
//! dependencies (downstream) of a symbol, walked breadth-first over typed edges and
//! grouped by depth, with a risk level and an epistemic completeness envelope.
//!
//! The walk runs on a [`Graph`]: the code index's graph segment (resident in `serve`,
//! `index::reader::Snapshot`) or, when the root has no usable index, the legacy per-call
//! graph (`LegacyGraph`). A node of a language the resolver owns (Go, TS/JS) walks the
//! resolved `symbol` layer; any other node walks the lexical `structure` layer between
//! files, whose kinds stand for relation types by [`LEXICAL_RELATIONS`].
//!
//! - Filters: `relation_types` (default CALLS, IMPORTS, EXTENDS, IMPLEMENTS; USES,
//!   ACCESSES, HAS_METHOD and HAS_PROPERTY on request), `min_confidence` on each edge,
//!   and `include_tests` (test files are left out by default and counted).
//! - Seeds: a UID, a file path, `Container.name` or a plain name. A type target is also
//!   seeded upstream with its constructors and its file (callers reach a class through
//!   `new` and its module), and downstream with its members; a file with the symbols it
//!   declares.
//! - Tiers: depth 1 WILL BREAK, 2 LIKELY AFFECTED, 3 and deeper MAY NEED TESTING
//!   (downstream: DIRECT and TRANSITIVE dependencies), each paged by `limit`/`offset`
//!   in (path, line, name) order.
//! - Risk ([`RISK_RULES`]): scored on upstream dependents only. UNKNOWN when the walk
//!   finds none. Affected flows are not computed yet (WS-38), so the flow thresholds
//!   never fire.
//! - Completeness: `exact` only when nothing could hide a dependent (upstream: no
//!   resolver drop on the name of any symbol in the result; downstream: none in the
//!   files walked), the graph is resolved, the index lost no file, the seed set was
//!   not cut short by [`MAX_TARGETS`] and the walk was not cut short. Otherwise
//!   `lower_bound`, with machine-readable `causes` and the first `boundaries`.

use std::collections::{BTreeMap, BTreeSet};
use std::time::{Duration, Instant};

use serde::Serialize;

use super::csr::{self, Edge};

// ---------------------------------------------------------------------------
// The query
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Direction {
    /// Dependents: who calls, imports, extends or uses the target.
    Upstream,
    /// Dependencies: what the target calls, imports, extends or uses.
    Downstream,
}

impl Direction {
    pub fn parse(v: &str) -> Option<Direction> {
        match v {
            "upstream" => Some(Direction::Upstream),
            "downstream" => Some(Direction::Downstream),
            _ => None,
        }
    }

    pub fn reverse(self) -> Direction {
        match self {
            Direction::Upstream => Direction::Downstream,
            Direction::Downstream => Direction::Upstream,
        }
    }

    /// The CSR direction a walk in this direction reads.
    pub fn adjacency(self) -> csr::Adj {
        match self {
            Direction::Upstream => csr::IN,
            Direction::Downstream => csr::OUT,
        }
    }
}

/// Relation types, a closed set; bit `i` of [`Relations`] is `RELATIONS[i]`.
pub const RELATIONS: [&str; 8] = [
    "CALLS",
    "IMPORTS",
    "EXTENDS",
    "IMPLEMENTS",
    "USES",
    "ACCESSES",
    "HAS_METHOD",
    "HAS_PROPERTY",
];

/// The lexical (`structure` layer) edge kinds and the relation types each stands for.
/// A test file's references are `tests` edges: calls from tests.
pub const LEXICAL_RELATIONS: &[(&str, &[&str])] = &[
    ("imports", &["IMPORTS"]),
    ("inherits", &["EXTENDS", "IMPLEMENTS"]),
    ("calls", &["CALLS"]),
    ("references", &["USES"]),
    ("tests", &["CALLS"]),
];

/// A set of relation types.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Relations(u16);

impl Relations {
    /// CALLS, IMPORTS, EXTENDS, IMPLEMENTS (PAR-IMP-04's default).
    pub const DEFAULT: Relations = Relations(0b1111);
    /// Every relation a dependency can travel (trace's default): all but the member
    /// declarations.
    pub const DEPENDENCIES: Relations = Relations(0b11_1111);

    fn bit(name: &str) -> Option<u16> {
        RELATIONS.iter().position(|r| *r == name).map(|i| 1 << i)
    }

    /// A comma-separated list of relation types (any case); empty is the default.
    pub fn parse(list: &str) -> Result<Relations, String> {
        let mut bits = 0u16;
        for name in list.split(',').map(str::trim).filter(|n| !n.is_empty()) {
            bits |= Self::bit(&name.to_ascii_uppercase()).ok_or_else(|| {
                format!(
                    "unknown relation type {name:?}; use {}",
                    RELATIONS.join(", ")
                )
            })?;
        }
        Ok(match bits {
            0 => Relations::DEFAULT,
            b => Relations(b),
        })
    }

    /// The relation types an edge of `kind` stands for (typed or lexical), as bits.
    pub fn of_kind(kind: &str) -> u16 {
        Self::bit(kind).unwrap_or_else(|| {
            LEXICAL_RELATIONS
                .iter()
                .find(|(k, _)| *k == kind)
                .map_or(0, |(_, rels)| {
                    rels.iter()
                        .filter_map(|r| Self::bit(r))
                        .fold(0, |a, b| a | b)
                })
        })
    }

    pub fn admits(self, bits: u16) -> bool {
        self.0 & bits != 0
    }

    pub fn names(self) -> Vec<&'static str> {
        RELATIONS
            .iter()
            .enumerate()
            .filter(|(i, _)| self.0 & (1 << i) != 0)
            .map(|(_, r)| *r)
            .collect()
    }
}

pub const DEFAULT_DEPTH: usize = 3;
pub const MAX_DEPTH: usize = 32;
pub const DEFAULT_LIMIT: usize = 50;
pub const MAX_LIMIT: usize = 500;
pub const DEFAULT_TIMEOUT_MS: u64 = 10_000;
pub const MAX_TIMEOUT_MS: u64 = 60_000;
/// Nodes one walk visits at most (the walk stops and says so).
pub const MAX_VISITED: usize = 50_000;
/// Targets a query seeds, at most.
pub const MAX_TARGETS: usize = 20;
/// Boundary rows listed (causes stay exact).
pub const MAX_BOUNDARIES: usize = 50;
/// Modules listed.
const MAX_MODULES: usize = 10;

#[derive(Debug, Clone)]
pub struct Query {
    pub target: String,
    pub direction: Direction,
    pub max_depth: usize,
    pub relations: Relations,
    pub min_confidence: f64,
    pub include_tests: bool,
    /// Items per depth tier.
    pub limit: usize,
    /// Items skipped per depth tier.
    pub offset: usize,
    pub summary_only: bool,
    pub timeout: Duration,
}

impl Query {
    pub fn new(target: &str) -> Query {
        Query {
            target: target.to_string(),
            direction: Direction::Upstream,
            max_depth: DEFAULT_DEPTH,
            relations: Relations::DEFAULT,
            min_confidence: 0.0,
            include_tests: false,
            limit: DEFAULT_LIMIT,
            offset: 0,
            summary_only: false,
            timeout: Duration::from_millis(DEFAULT_TIMEOUT_MS),
        }
    }

    /// Apply `--name=value` flags (the `symbolgraph impact` and `index impact` CLIs).
    pub fn apply_flags<I: IntoIterator<Item = String>>(&mut self, flags: I) -> Result<(), String> {
        for flag in flags {
            let (name, value) = flag
                .strip_prefix("--")
                .and_then(|f| f.split_once('='))
                .ok_or_else(|| format!("expected --name=value, got {flag:?}"))?;
            let set = QUERY_FLAGS
                .iter()
                .find(|(n, _)| *n == name)
                .map(|(_, set)| *set)
                .ok_or_else(|| format!("unknown impact flag --{name}"))?;
            set(self, value).map_err(|e| format!("--{name}: {e}"))?;
        }
        Ok(())
    }
}

type Setter = fn(&mut Query, &str) -> Result<(), String>;

/// The query flags and what each sets.
const QUERY_FLAGS: &[(&str, Setter)] = &[
    ("direction", set_direction),
    ("max-depth", set_max_depth),
    ("relation-types", set_relations),
    ("min-confidence", set_min_confidence),
    ("include-tests", set_include_tests),
    ("limit", set_limit),
    ("offset", set_offset),
    ("summary-only", set_summary_only),
    ("timeout-ms", set_timeout),
];

fn set_direction(q: &mut Query, v: &str) -> Result<(), String> {
    q.direction = Direction::parse(v).ok_or("use upstream or downstream")?;
    Ok(())
}

fn set_max_depth(q: &mut Query, v: &str) -> Result<(), String> {
    q.max_depth = bounded(v, 1, MAX_DEPTH as u64)? as usize;
    Ok(())
}

fn set_relations(q: &mut Query, v: &str) -> Result<(), String> {
    q.relations = Relations::parse(v)?;
    Ok(())
}

fn set_min_confidence(q: &mut Query, v: &str) -> Result<(), String> {
    let c: f64 = v.parse().map_err(|_| "not a number")?;
    if !(0.0..=1.0).contains(&c) {
        return Err("must be between 0 and 1".into());
    }
    q.min_confidence = c;
    Ok(())
}

fn set_include_tests(q: &mut Query, v: &str) -> Result<(), String> {
    q.include_tests = boolean(v)?;
    Ok(())
}

fn set_limit(q: &mut Query, v: &str) -> Result<(), String> {
    q.limit = bounded(v, 1, MAX_LIMIT as u64)? as usize;
    Ok(())
}

fn set_offset(q: &mut Query, v: &str) -> Result<(), String> {
    q.offset = bounded(v, 0, MAX_VISITED as u64)? as usize;
    Ok(())
}

fn set_summary_only(q: &mut Query, v: &str) -> Result<(), String> {
    q.summary_only = boolean(v)?;
    Ok(())
}

fn set_timeout(q: &mut Query, v: &str) -> Result<(), String> {
    q.timeout = Duration::from_millis(bounded(v, 1, MAX_TIMEOUT_MS)?);
    Ok(())
}

pub(super) fn bounded(v: &str, lo: u64, hi: u64) -> Result<u64, String> {
    let n: u64 = v.parse().map_err(|_| "not a whole number")?;
    if !(lo..=hi).contains(&n) {
        return Err(format!("must be between {lo} and {hi}"));
    }
    Ok(n)
}

pub(super) fn boolean(v: &str) -> Result<bool, String> {
    match v {
        "true" => Ok(true),
        "false" => Ok(false),
        _ => Err("use true or false".into()),
    }
}

// ---------------------------------------------------------------------------
// The graph a walk reads
// ---------------------------------------------------------------------------

/// A symbol node's facts.
#[derive(Debug, Clone)]
pub struct SymbolFacts {
    pub name: String,
    /// The graph's id of the name (drops are keyed on it).
    pub name_id: u32,
    pub kind: String,
    pub line: u32,
}

/// Where a node sits, as ids: its file, its line (0 for a file node) and its name.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct NodeKey {
    pub file: u32,
    pub line: u32,
    pub name: Option<u32>,
}

/// Table labels of an edge.
pub struct Labels<'a> {
    pub kind: &'a str,
    pub layer: &'a str,
    pub provenance: &'a str,
    pub reason: &'a str,
    pub access: &'a str,
}

/// How a target was found.
#[derive(Debug, Clone, Default)]
pub struct Lookup {
    pub nodes: Vec<u32>,
    /// `uid`, `path`, `qualified`, `name`, or empty when nothing matched.
    pub matched: &'static str,
    pub truncated: bool,
}

/// One resolver drop that bounds an answer.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct Boundary {
    pub cause: String,
    /// The dropped name (upstream), or empty for a file's drops (downstream).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    pub path: String,
    pub count: u32,
}

/// The graph behind search, explain and impact, as a walk reads it. Nodes are files
/// `0..file_count()` then symbols. Two implementations: the code index's snapshot and
/// the legacy per-call graph (file nodes only).
pub trait Graph {
    fn file_count(&self) -> u32;
    /// Whether this graph has a symbol-bearing code-index snapshot rather than only
    /// legacy file nodes.
    fn has_symbol_snapshot(&self) -> bool {
        true
    }
    fn path(&self, file: u32) -> &str;
    /// The file a node belongs to.
    fn file_of(&self, node: u32) -> Result<u32, String>;
    /// The symbol behind a node; None for a file node.
    fn symbol(&self, node: u32) -> Result<Option<SymbolFacts>, String>;
    /// A node's file, line and name id without reading its strings: the walk's sort
    /// key (files and names are numbered in path and name order).
    fn key(&self, node: u32) -> Result<NodeKey, String> {
        let file = self.file_of(node)?;
        Ok(match self.symbol(node)? {
            Some(s) => NodeKey {
                file,
                line: s.line,
                name: Some(s.name_id),
            },
            None => NodeKey {
                file,
                line: 0,
                name: None,
            },
        })
    }
    /// A node's edges in `dir` (every layer; the walk filters).
    fn edges(&self, node: u32, dir: Direction) -> Result<Vec<Edge>, String>;
    fn labels(&self, e: &Edge) -> Labels<'_>;
    /// Whether a file's code is walked on resolved symbol edges (its language has a
    /// resolver) rather than lexical file edges.
    fn resolved(&self, file: u32) -> bool;
    fn is_test(&self, file: u32) -> bool;
    /// The nodes a target names.
    fn lookup(&self, target: &str) -> Result<Lookup, String>;
    /// The file node of a repository-relative path.
    fn file_node(&self, path: &str) -> Result<Option<u32>, String>;
    /// The non-local symbol nodes a file declares (none in a graph without symbol nodes).
    fn file_symbols(&self, file: u32) -> Result<Vec<u32>, String>;
    /// Resolver drops that could hide a dependent: upstream, of references to `names`;
    /// downstream, in `files`.
    fn drops(
        &self,
        _names: &BTreeSet<u32>,
        _files: &BTreeSet<u32>,
        _dir: Direction,
    ) -> Result<Vec<Boundary>, String> {
        Ok(Vec::new())
    }
    /// The import-cycle report, when this graph computes one.
    fn import_cycles(&self) -> Option<&super::cycles::CycleReport> {
        None
    }
}

/// The layer a node's own edges are walked on.
fn layer_for<G: Graph + ?Sized>(g: &G, file: u32) -> &'static str {
    match g.resolved(file) {
        true => "symbol",
        false => "structure",
    }
}

/// The symbol kinds that seed through their members.
const TYPE_KINDS: &[&str] = &["Class", "Interface", "Struct"];

/// A walk's start: a node and why it is one.
#[derive(Debug, Clone, Copy)]
struct Seed {
    node: u32,
    /// None for a named target, else what the type target added.
    by: Option<&'static str>,
}

/// The seeds of `lookup`: a symbol of an unresolved language walks from its file; a type
/// is also seeded by [`type_members`].
fn seeds<G: Graph + ?Sized>(g: &G, lookup: &Lookup, dir: Direction) -> Result<Vec<Seed>, String> {
    let mut out: Vec<Seed> = Vec::new();
    let mut seen = BTreeSet::new();
    let mut add = |out: &mut Vec<Seed>, node: u32, by| {
        if seen.insert(node) {
            out.push(Seed { node, by });
        }
    };
    for &n in &lookup.nodes {
        let file = g.file_of(n)?;
        if !g.resolved(file) {
            add(&mut out, file, None);
            continue;
        }
        add(&mut out, n, None);
        let extra = match g.symbol(n)? {
            // a file walks with what it declares: named imports point at symbols
            None => g
                .file_symbols(file)?
                .into_iter()
                .map(|s| (s, "declared"))
                .collect(),
            Some(s) if TYPE_KINDS.contains(&s.kind.as_str()) => type_members(g, n, file, dir)?,
            Some(_) => Vec::new(),
        };
        for (node, by) in extra {
            add(&mut out, node, Some(by));
        }
    }
    Ok(out)
}

/// The seed nodes of `lookup` walking in `dir` (trace's ends).
pub(super) fn seed_nodes<G: Graph + ?Sized>(
    g: &G,
    lookup: &Lookup,
    dir: Direction,
) -> Result<Vec<u32>, String> {
    Ok(seeds(g, lookup, dir)?.into_iter().map(|s| s.node).collect())
}

/// What a type target adds to its seeds: upstream, its constructors and its file (the
/// ways callers reach a type); downstream, its members (what it depends on is what they
/// use).
fn type_members<G: Graph + ?Sized>(
    g: &G,
    ty: u32,
    file: u32,
    dir: Direction,
) -> Result<Vec<(u32, &'static str)>, String> {
    let mut out = Vec::new();
    for e in g.edges(ty, Direction::Downstream)? {
        let l = g.labels(&e);
        if l.layer != "symbol" || !matches!(l.kind, "HAS_METHOD" | "HAS_PROPERTY") {
            continue;
        }
        let member = g.symbol(e.node)?;
        match dir {
            Direction::Upstream if member.is_some_and(|m| m.kind == "Constructor") => {
                out.push((e.node, "constructor"))
            }
            Direction::Downstream => out.push((e.node, "member")),
            Direction::Upstream => {}
        }
    }
    if dir == Direction::Upstream {
        out.push((file, "file"));
    }
    Ok(out)
}

// ---------------------------------------------------------------------------
// The walk
// ---------------------------------------------------------------------------

/// A node the walk reached.
#[derive(Debug, Clone, Copy)]
struct Reached {
    node: u32,
    key: NodeKey,
    depth: usize,
    /// The node it was reached from.
    parent: u32,
    edge: Edge,
    /// Product of the edge confidences along the path.
    confidence: f64,
    /// Every hop scope-proven.
    scoped: bool,
}

#[derive(Default)]
struct Walk {
    reached: Vec<Reached>,
    tests_excluded: usize,
    below_confidence: usize,
    truncated: bool,
    timed_out: bool,
    lexical: bool,
    resolved: bool,
}

struct Visited(Vec<u64>);

impl Visited {
    fn insert(&mut self, n: u32) -> bool {
        let (w, b) = ((n / 64) as usize, 1u64 << (n % 64));
        if w >= self.0.len() {
            self.0.resize(w + 1, 0);
        }
        let fresh = self.0[w] & b == 0;
        self.0[w] |= b;
        fresh
    }
}

/// Breadth-first from `seeds` in `dir`, depth by depth, within the query's filters and
/// the visit and time caps.
fn walk<G: Graph + ?Sized>(g: &G, q: &Query, seeds: &[Seed]) -> Result<Walk, String> {
    let started = Instant::now();
    let min_q = csr::quantize_confidence(q.min_confidence);
    let mut w = Walk::default();
    let mut visited = Visited(Vec::new());
    let mut frontier: Vec<(u32, f64, bool)> = Vec::new();
    for s in seeds {
        visited.insert(s.node);
        frontier.push((s.node, 1.0, true));
        note_layer(g, &mut w, g.file_of(s.node)?);
    }
    let mut seen_count = seeds.len();
    'depths: for depth in 1..=q.max_depth {
        let mut next = Vec::new();
        for &(node, conf, scoped) in &frontier {
            if started.elapsed() > q.timeout {
                w.timed_out = true;
                break 'depths;
            }
            let layer = layer_for(g, g.file_of(node)?);
            for e in g.edges(node, q.direction)? {
                let l = g.labels(&e);
                if l.layer != layer || !q.relations.admits(Relations::of_kind(l.kind)) {
                    continue;
                }
                if e.confidence < min_q {
                    w.below_confidence += 1;
                    continue;
                }
                if !visited.insert(e.node) {
                    continue;
                }
                let key = g.key(e.node)?;
                if !q.include_tests && g.is_test(key.file) {
                    w.tests_excluded += 1;
                    continue;
                }
                seen_count += 1;
                if seen_count > MAX_VISITED {
                    w.truncated = true;
                    break 'depths;
                }
                note_layer(g, &mut w, key.file);
                let c = conf * csr::confidence_of(e.confidence);
                let s = scoped && l.provenance == "scope";
                w.reached.push(Reached {
                    node: e.node,
                    key,
                    depth,
                    parent: node,
                    edge: e,
                    confidence: c,
                    scoped: s,
                });
                next.push((e.node, c, s));
            }
        }
        if next.is_empty() {
            break;
        }
        frontier = next;
    }
    Ok(w)
}

fn note_layer<G: Graph + ?Sized>(g: &G, w: &mut Walk, file: u32) {
    match g.resolved(file) {
        true => w.resolved = true,
        false => w.lexical = true,
    }
}

// ---------------------------------------------------------------------------
// The answer
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "UPPERCASE")]
pub enum Risk {
    Critical,
    High,
    Medium,
    Low,
    Unknown,
}

impl Risk {
    pub fn as_str(self) -> &'static str {
        match self {
            Risk::Critical => "CRITICAL",
            Risk::High => "HIGH",
            Risk::Medium => "MEDIUM",
            Risk::Low => "LOW",
            Risk::Unknown => "UNKNOWN",
        }
    }
}

/// What risk is scored on.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Metrics {
    /// Dependents at depth 1.
    pub direct: usize,
    /// Affected execution flows; None until flows exist (WS-38).
    pub flows: Option<usize>,
    pub modules: usize,
    pub impacted: usize,
}

/// A risk level and the thresholds that reach it: any one metric at or over its
/// threshold does (PAR-IMP-04).
pub struct RiskRule {
    pub level: Risk,
    pub direct: usize,
    pub flows: usize,
    pub modules: usize,
    pub impacted: usize,
}

/// The risk levels above LOW, highest first.
pub const RISK_RULES: &[RiskRule] = &[
    RiskRule {
        level: Risk::Critical,
        direct: 30,
        flows: 5,
        modules: 5,
        impacted: 200,
    },
    RiskRule {
        level: Risk::High,
        direct: 15,
        flows: 3,
        modules: 3,
        impacted: 100,
    },
    RiskRule {
        level: Risk::Medium,
        direct: 5,
        flows: usize::MAX,
        modules: usize::MAX,
        impacted: 30,
    },
];

/// The level `m` reaches; LOW when no rule does.
pub fn risk_level(m: Metrics) -> Risk {
    RISK_RULES
        .iter()
        .find(|r| {
            m.direct >= r.direct
                || m.flows.is_some_and(|f| f >= r.flows)
                || m.modules >= r.modules
                || m.impacted >= r.impacted
        })
        .map_or(Risk::Low, |r| r.level)
}

/// Depth-tier labels per direction; the last one covers every deeper tier.
fn tier_label(dir: Direction, depth: usize) -> &'static str {
    const UP: &[&str] = &["WILL BREAK", "LIKELY AFFECTED", "MAY NEED TESTING"];
    const DOWN: &[&str] = &["DIRECT DEPENDENCY", "TRANSITIVE DEPENDENCY"];
    let labels = match dir {
        Direction::Upstream => UP,
        Direction::Downstream => DOWN,
    };
    labels[(depth - 1).min(labels.len() - 1)]
}

/// The module of a path: its first two directories (`.` for the top level). Areas
/// (SYM-08, WS-38) replace this once they exist.
pub fn module_of(path: &str) -> &str {
    let dir = path.rsplit_once('/').map_or("", |(d, _)| d);
    if dir.is_empty() {
        return ".";
    }
    match dir.match_indices('/').nth(1) {
        Some((i, _)) => &dir[..i],
        None => dir,
    }
}

/// A node as the answer lists it.
#[derive(Debug, Clone, Serialize)]
pub struct Item {
    /// `Container.name` for a member, the name otherwise; absent for file-level code.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    /// The symbol kind, or `File` for a file's top-level code.
    pub kind: String,
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub line: Option<u32>,
    /// The kind of the edge that reached it.
    pub edge: String,
    /// Product of the edge confidences along the path.
    pub confidence: f64,
    /// `scope` when every hop is scope-proven, else `lexical`.
    pub provenance: &'static str,
    pub reason: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub access: Option<String>,
    /// The node it was reached from.
    pub via: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct Target {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub kind: String,
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub line: Option<u32>,
    /// What a type target added (`constructor`, `file`, `member`) or a file target
    /// (`declared`: its symbols).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub seeded_by: Option<&'static str>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Tier {
    pub depth: usize,
    pub label: &'static str,
    pub total: usize,
    pub offset: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub items: Option<Vec<Item>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_offset: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Default)]
pub struct Counts {
    pub total: usize,
    /// Nodes per depth, from depth 1.
    pub by_depth: Vec<usize>,
    pub direct: usize,
    pub files: usize,
    pub modules: usize,
    /// Test-file nodes left out (`include_tests=false`).
    pub tests_excluded: usize,
    /// Edges below `min_confidence`.
    pub below_confidence: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct ModuleCount {
    pub module: String,
    pub count: usize,
}

/// The epistemic completeness envelope (PAR-IMP-05).
#[derive(Debug, Clone, Serialize, Default)]
pub struct Epistemic {
    /// `exact` | `lower_bound`
    pub completeness: &'static str,
    /// Occurrences by cause; coverage losses appear as `coverage:<reason>`.
    pub causes: BTreeMap<String, u64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub boundaries: Vec<Boundary>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Impact {
    /// The query.
    pub symbol: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub targets: Vec<Target>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub targets_truncated: bool,
    /// Files of the named targets.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub defined_in: Vec<String>,
    pub direction: Direction,
    pub max_depth: usize,
    pub relation_types: Vec<&'static str>,
    #[serde(skip_serializing_if = "is_zero")]
    pub min_confidence: f64,
    pub include_tests: bool,
    /// `resolved` (symbol edges), `lexical` (file edges), `mixed`, or `none` when no
    /// target was found.
    pub graph: &'static str,
    pub impacted_count: Option<usize>,
    pub counts: Counts,
    pub by_depth: Vec<Tier>,
    /// Scored on upstream dependents; absent downstream.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub risk: Option<Risk>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub risk_note: Option<String>,
    pub affected_modules: Vec<ModuleCount>,
    /// Execution flows (WS-38) are not computed yet: always null.
    pub affected_flows: Option<Vec<String>>,
    pub epistemic: Epistemic,
    /// Import cycles (PAR-IMP-08) through a target's file.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub import_cycles: Vec<super::cycles::Component>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
    /// The walk hit its time budget.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub partial: bool,
    pub generated_at: String,
}

fn is_zero(v: &f64) -> bool {
    *v == 0.0
}

fn now() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

/// Display name of a node: `Container.name` for a member, the name, or the path.
fn display<G: Graph + ?Sized>(g: &G, node: u32) -> Result<(Option<String>, String, u32), String> {
    let file = g.file_of(node)?;
    let Some(s) = g.symbol(node)? else {
        return Ok((None, "File".into(), file));
    };
    let container = g
        .edges(node, Direction::Upstream)?
        .into_iter()
        .find(|e| matches!(g.labels(e).kind, "HAS_METHOD" | "HAS_PROPERTY"))
        .map(|e| g.symbol(e.node))
        .transpose()?
        .flatten();
    let name = match container {
        Some(c) => format!("{}.{}", c.name, s.name),
        None => s.name,
    };
    Ok((Some(name), s.kind, file))
}

fn line_of<G: Graph + ?Sized>(g: &G, node: u32) -> Result<Option<u32>, String> {
    Ok(g.symbol(node)?.map(|s| s.line))
}

fn item<G: Graph + ?Sized>(g: &G, r: &Reached) -> Result<Item, String> {
    let (name, kind, file) = display(g, r.node)?;
    let (parent_name, _, parent_file) = display(g, r.parent)?;
    let l = g.labels(&r.edge);
    Ok(Item {
        name,
        kind,
        path: g.path(file).to_string(),
        line: line_of(g, r.node)?,
        edge: l.kind.to_string(),
        confidence: (r.confidence * 1000.0).round() / 1000.0,
        provenance: if r.scoped { "scope" } else { "lexical" },
        reason: l.reason.to_string(),
        access: (!l.access.is_empty()).then(|| l.access.to_string()),
        via: parent_name.unwrap_or_else(|| g.path(parent_file).to_string()),
    })
}

/// Run `q` on `g`. `losses` is the answering index's coverage loss counts.
pub fn run<G: Graph + ?Sized>(
    g: &G,
    q: &Query,
    losses: &BTreeMap<String, usize>,
) -> Result<Impact, String> {
    run_lookup(g, q, g.lookup(&q.target)?, losses)
}

/// Run `q` from given nodes instead of a looked-up target (detect_changes seeds the
/// walk with the changed symbols).
pub fn run_nodes<G: Graph + ?Sized>(
    g: &G,
    q: &Query,
    nodes: Vec<u32>,
    losses: &BTreeMap<String, usize>,
) -> Result<Impact, String> {
    let lookup = Lookup {
        nodes,
        matched: "changes",
        truncated: false,
    };
    run_lookup(g, q, lookup, losses)
}

/// Dependents one hop upstream of `node` within `q`'s filters.
pub fn direct_dependents<G: Graph + ?Sized>(g: &G, q: &Query, node: u32) -> Result<usize, String> {
    let layer = layer_for(g, g.file_of(node)?);
    let min_q = csr::quantize_confidence(q.min_confidence);
    let mut seen = BTreeSet::new();
    for e in g.edges(node, Direction::Upstream)? {
        let l = g.labels(&e);
        if l.layer == layer
            && q.relations.admits(Relations::of_kind(l.kind))
            && e.confidence >= min_q
            && (q.include_tests || !g.is_test(g.file_of(e.node)?))
        {
            seen.insert(e.node);
        }
    }
    Ok(seen.len())
}

fn run_lookup<G: Graph + ?Sized>(
    g: &G,
    q: &Query,
    lookup: Lookup,
    losses: &BTreeMap<String, usize>,
) -> Result<Impact, String> {
    let seeds = seeds(g, &lookup, q.direction)?;
    let w = walk(g, q, &seeds)?;

    let mut targets = Vec::new();
    let mut defined_in = BTreeSet::new();
    let listed_seeds = seeds.len().min(MAX_TARGETS);
    for s in seeds.iter().filter(|s| s.by.is_none()) {
        defined_in.insert(g.path(g.file_of(s.node)?).to_string());
    }
    for s in &seeds[..listed_seeds] {
        let (name, kind, file) = display(g, s.node)?;
        targets.push(Target {
            name,
            kind,
            path: g.path(file).to_string(),
            line: line_of(g, s.node)?,
            seeded_by: s.by,
        });
    }

    // tiers, each in (path, line, name) order: ids are ranks in path and name order
    let mut per_depth: Vec<Vec<(NodeKey, usize)>> = Vec::new();
    for (i, r) in w.reached.iter().enumerate() {
        if per_depth.len() < r.depth {
            per_depth.resize(r.depth, Vec::new());
        }
        per_depth[r.depth - 1].push((r.key, i));
    }
    let mut files: Vec<u32> = w.reached.iter().map(|r| r.key.file).collect();
    files.sort_unstable();
    files.dedup();
    let mut modules: BTreeMap<&str, usize> = BTreeMap::new();
    for f in &files {
        *modules.entry(module_of(g.path(*f))).or_default() += 1;
    }
    let mut by_depth = Vec::new();
    for (d, mut rows) in per_depth.into_iter().enumerate() {
        rows.sort_unstable();
        let total = rows.len();
        let page: Vec<usize> = rows
            .iter()
            .skip(q.offset)
            .take(q.limit)
            .map(|r| r.1)
            .collect();
        let end = q.offset + page.len();
        let items = match q.summary_only {
            true => None,
            false => Some(
                page.iter()
                    .map(|i| item(g, &w.reached[*i]))
                    .collect::<Result<Vec<_>, _>>()?,
            ),
        };
        by_depth.push(Tier {
            depth: d + 1,
            label: tier_label(q.direction, d + 1),
            total,
            offset: q.offset,
            items,
            next_offset: (end < total).then_some(end),
        });
    }
    let counts = Counts {
        total: w.reached.len(),
        by_depth: by_depth.iter().map(|t| t.total).collect(),
        direct: by_depth.first().map_or(0, |t| t.total),
        files: files.len(),
        modules: modules.len(),
        tests_excluded: w.tests_excluded,
        below_confidence: w.below_confidence,
    };
    let mut affected: Vec<ModuleCount> = modules
        .into_iter()
        .map(|(module, count)| ModuleCount {
            module: module.to_string(),
            count,
        })
        .collect();
    affected.sort_by(|a, b| b.count.cmp(&a.count).then_with(|| a.module.cmp(&b.module)));
    affected.truncate(MAX_MODULES);

    // `lookup.truncated` is the only thing that means seeds were DROPPED: a target that
    // expands to more than MAX_TARGETS seeds (a file walking with what it declares, a type
    // with its members) keeps every one of them, so the listed-seed cap below is a display
    // cap and must not make the answer a lower bound.
    let epistemic = envelope(g, q, lookup.truncated, &seeds, &w, losses)?;
    let (risk, risk_note) = score(q, &lookup, &counts, &epistemic);
    let import_cycles = cycles_through(g, &seeds)?;

    Ok(Impact {
        symbol: q.target.clone(),
        targets,
        targets_truncated: lookup.truncated || seeds.len() > listed_seeds,
        defined_in: defined_in.into_iter().collect(),
        direction: q.direction,
        max_depth: q.max_depth,
        relation_types: q.relations.names(),
        min_confidence: q.min_confidence,
        include_tests: q.include_tests,
        graph: match (w.resolved, w.lexical) {
            (true, true) => "mixed",
            (true, false) => "resolved",
            (false, true) => "lexical",
            (false, false) => "none",
        },
        impacted_count: (!lookup.nodes.is_empty()).then_some(counts.total),
        counts,
        by_depth,
        risk,
        risk_note,
        affected_modules: affected,
        affected_flows: None,
        epistemic,
        import_cycles,
        truncated: w.truncated,
        partial: w.timed_out,
        generated_at: now(),
    })
}

/// Risk is scored upstream; a missing target is UNKNOWN for either direction.
fn score(q: &Query, lookup: &Lookup, c: &Counts, e: &Epistemic) -> (Option<Risk>, Option<String>) {
    if lookup.nodes.is_empty() {
        return (
            Some(Risk::Unknown),
            Some(format!(
                "no symbol, file or UID named {:?} in the graph",
                q.target
            )),
        );
    }
    if q.direction == Direction::Downstream {
        return (None, None);
    }
    let bounded = e.completeness != "exact";
    let causes = || e.causes.keys().cloned().collect::<Vec<_>>().join(", ");
    if c.direct == 0 {
        let mut note = "no dependents found: the symbol may be unused, an entry point, or reached \
                        only through dynamic dispatch, reflection or code outside the graph"
            .to_string();
        if bounded {
            note.push_str(&format!("; the graph is a lower bound here ({})", causes()));
        }
        return (Some(Risk::Unknown), Some(note));
    }
    let level = risk_level(Metrics {
        direct: c.direct,
        flows: None,
        modules: c.modules,
        impacted: c.total,
    });
    let note = bounded.then(|| {
        format!(
            "at least {}: the dependents found are a lower bound ({})",
            level.as_str(),
            causes()
        )
    });
    (Some(level), note)
}

/// The completeness envelope of a walk.
fn envelope<G: Graph + ?Sized>(
    g: &G,
    q: &Query,
    seeds_truncated: bool,
    seeds: &[Seed],
    w: &Walk,
    losses: &BTreeMap<String, usize>,
) -> Result<Epistemic, String> {
    let mut causes: BTreeMap<String, u64> = BTreeMap::new();
    let mut keys: Vec<NodeKey> = w.reached.iter().map(|r| r.key).collect();
    for s in seeds {
        keys.push(g.key(s.node)?);
    }
    let names: BTreeSet<u32> = keys.iter().filter_map(|k| k.name).collect();
    let files: BTreeSet<u32> = keys.iter().map(|k| k.file).collect();
    let mut boundaries = Vec::new();
    for b in g.drops(&names, &files, q.direction)? {
        *causes.entry(b.cause.clone()).or_default() += u64::from(b.count);
        if boundaries.len() < MAX_BOUNDARIES {
            boundaries.push(b);
        }
    }
    let flags = [
        // A seed set the target cap cut short is a lower bound: the definers it dropped
        // were never walked, so a dependent behind one is invisible here.
        (seeds_truncated, "targets_truncated"),
        (w.lexical, "lexical_graph"),
        (w.truncated, "truncated"),
        (w.timed_out, "timeout"),
    ];
    for (_, cause) in flags.iter().filter(|(on, _)| *on) {
        causes.insert(cause.to_string(), 1);
    }
    for (reason, n) in losses {
        *causes.entry(format!("coverage:{reason}")).or_default() += *n as u64;
    }
    Ok(Epistemic {
        completeness: if causes.is_empty() {
            "exact"
        } else {
            "lower_bound"
        },
        causes,
        boundaries,
    })
}

/// The import-cycle components through a seed's file: every component, listed in the
/// report or not, matched on all its members (its listed files are capped).
fn cycles_through<G: Graph + ?Sized>(
    g: &G,
    seeds: &[Seed],
) -> Result<Vec<super::cycles::Component>, String> {
    let Some(report) = g.import_cycles() else {
        return Ok(Vec::new());
    };
    let mut files = BTreeSet::new();
    for s in seeds {
        files.insert(g.file_of(s.node)?);
    }
    Ok(report
        .all
        .iter()
        .filter(|c| files.iter().any(|f| c.members.binary_search(f).is_ok()))
        .cloned()
        .collect())
}

// ---------------------------------------------------------------------------
// The legacy graph: file nodes over lexical edges
// ---------------------------------------------------------------------------

/// The legacy per-call graph as a [`Graph`]: its files are the nodes and its edges the
/// lexical kinds; it has no symbol nodes, no confidence (every edge 0.5) and no drops.
pub struct LegacyGraph {
    paths: Vec<String>,
    out: Vec<Vec<Edge>>,
    inn: Vec<Vec<Edge>>,
    kinds: Vec<String>,
    /// name → the files defining it.
    definers: BTreeMap<String, BTreeSet<u32>>,
}

impl LegacyGraph {
    pub fn new(g: &crate::symbolgraph::SymbolGraph) -> LegacyGraph {
        let mut paths: Vec<String> = g.files.iter().map(|f| f.path.clone()).collect();
        for e in &g.edges {
            paths.push(e.from_path.clone());
            paths.push(e.to_path.clone());
        }
        paths.sort();
        paths.dedup();
        let id = |p: &str| paths.binary_search_by(|x| x.as_str().cmp(p)).ok();
        let mut kinds: Vec<String> = Vec::new();
        let n = paths.len();
        let (mut out, mut inn) = (vec![Vec::new(); n], vec![Vec::new(); n]);
        for e in &g.edges {
            let (Some(a), Some(b)) = (id(&e.from_path), id(&e.to_path)) else {
                continue;
            };
            if a == b {
                continue;
            }
            let k = match kinds.iter().position(|k| *k == e.kind) {
                Some(k) => k,
                None => {
                    kinds.push(e.kind.clone());
                    kinds.len() - 1
                }
            };
            let rec = |node: usize| Edge {
                node: node as u32,
                weight: e.weight.min(u16::MAX as usize) as u16,
                kind: k.min(255) as u8,
                confidence: csr::quantize_confidence(super::edges::LEXICAL_CONFIDENCE),
                ..Default::default()
            };
            out[a].push(rec(b));
            inn[b].push(rec(a));
        }
        for list in out.iter_mut().chain(inn.iter_mut()) {
            list.sort();
            list.dedup();
        }
        let mut definers: BTreeMap<String, BTreeSet<u32>> = BTreeMap::new();
        for s in &g.symbols {
            if let Some(f) = id(&s.path) {
                definers.entry(s.name.clone()).or_default().insert(f as u32);
            }
        }
        LegacyGraph {
            paths,
            out,
            inn,
            kinds,
            definers,
        }
    }
}

impl Graph for LegacyGraph {
    fn has_symbol_snapshot(&self) -> bool {
        false
    }

    fn file_count(&self) -> u32 {
        self.paths.len() as u32
    }

    fn path(&self, file: u32) -> &str {
        self.paths.get(file as usize).map_or("", String::as_str)
    }

    fn file_of(&self, node: u32) -> Result<u32, String> {
        Ok(node)
    }

    fn symbol(&self, _node: u32) -> Result<Option<SymbolFacts>, String> {
        Ok(None)
    }

    fn edges(&self, node: u32, dir: Direction) -> Result<Vec<Edge>, String> {
        let lists = match dir {
            Direction::Upstream => &self.inn,
            Direction::Downstream => &self.out,
        };
        Ok(lists.get(node as usize).cloned().unwrap_or_default())
    }

    fn labels(&self, e: &Edge) -> Labels<'_> {
        Labels {
            kind: self.kinds.get(e.kind as usize).map_or("", String::as_str),
            layer: "structure",
            provenance: "lexical",
            reason: "unique-name",
            access: "",
        }
    }

    fn resolved(&self, _file: u32) -> bool {
        false
    }

    fn is_test(&self, file: u32) -> bool {
        super::edges::is_test_file(self.path(file))
    }

    fn file_node(&self, path: &str) -> Result<Option<u32>, String> {
        Ok(self
            .paths
            .binary_search_by(|p| p.as_str().cmp(path))
            .ok()
            .map(|f| f as u32))
    }

    fn file_symbols(&self, _file: u32) -> Result<Vec<u32>, String> {
        Ok(Vec::new())
    }

    fn lookup(&self, target: &str) -> Result<Lookup, String> {
        if let Ok(f) = self.paths.binary_search_by(|p| p.as_str().cmp(target)) {
            return Ok(Lookup {
                nodes: vec![f as u32],
                matched: "path",
                truncated: false,
            });
        }
        let name = target.rsplit('.').next().unwrap_or(target);
        let files = self.definers.get(name).cloned().unwrap_or_default();
        Ok(Lookup {
            truncated: files.len() > MAX_TARGETS,
            nodes: files.into_iter().take(MAX_TARGETS).collect(),
            matched: "name",
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn legacy_graph_has_no_symbol_snapshot() {
        let graph = crate::symbolgraph::SymbolGraph {
            workspace_id: "ws".into(),
            file_count: 0,
            symbol_count: 0,
            edge_count: 0,
            files: Vec::new(),
            symbols: Vec::new(),
            edges: Vec::new(),
            flow_edges: Vec::new(),
            flow_edge_count: 0,
            coverage: Default::default(),
            generated_at: String::new(),
        };
        assert!(!LegacyGraph::new(&graph).has_symbol_snapshot());
    }

    fn m(direct: usize, modules: usize, impacted: usize) -> Metrics {
        Metrics {
            direct,
            flows: None,
            modules,
            impacted,
        }
    }

    // PAR-IMP-04's thresholds: any one metric at its threshold reaches the level.
    #[test]
    fn risk_thresholds_table() {
        let cases = [
            (m(30, 0, 30), Risk::Critical),
            (m(1, 5, 1), Risk::Critical),
            (m(1, 1, 200), Risk::Critical),
            (m(29, 4, 199), Risk::High),
            (m(15, 0, 15), Risk::High),
            (m(1, 3, 1), Risk::High),
            (m(1, 1, 100), Risk::High),
            (m(14, 2, 99), Risk::Medium),
            (m(5, 0, 5), Risk::Medium),
            (m(1, 1, 30), Risk::Medium),
            (m(4, 2, 29), Risk::Low),
            (m(1, 1, 1), Risk::Low),
        ];
        for (metrics, want) in cases {
            assert_eq!(risk_level(metrics), want, "{metrics:?}");
        }
        let flows = |f| Metrics {
            flows: Some(f),
            ..m(1, 1, 1)
        };
        assert_eq!(risk_level(flows(5)), Risk::Critical);
        assert_eq!(risk_level(flows(3)), Risk::High);
        assert_eq!(risk_level(flows(2)), Risk::Low);
    }

    #[test]
    fn relations_parse_default_and_lexical_kinds() {
        assert_eq!(Relations::parse("").unwrap(), Relations::DEFAULT);
        assert_eq!(
            Relations::parse("calls, accesses").unwrap().names(),
            vec!["CALLS", "ACCESSES"]
        );
        assert!(Relations::parse("CALLZ").is_err());
        let d = Relations::DEFAULT;
        assert!(d.admits(Relations::of_kind("CALLS")));
        assert!(!d.admits(Relations::of_kind("ACCESSES")));
        assert!(d.admits(Relations::of_kind("inherits")));
        assert!(!d.admits(Relations::of_kind("references")));
        assert!(
            !d.admits(Relations::of_kind("returns")),
            "flow edges are never walked"
        );
    }

    #[test]
    fn modules_are_two_directories_deep() {
        assert_eq!(module_of("main.go"), ".");
        assert_eq!(module_of("src/a.ts"), "src");
        assert_eq!(module_of("src/auth/login.ts"), "src/auth");
        assert_eq!(module_of("packages/core/src/x.ts"), "packages/core");
    }

    #[test]
    fn tier_labels_cover_every_depth() {
        assert_eq!(tier_label(Direction::Upstream, 1), "WILL BREAK");
        assert_eq!(tier_label(Direction::Upstream, 2), "LIKELY AFFECTED");
        assert_eq!(tier_label(Direction::Upstream, 7), "MAY NEED TESTING");
        assert_eq!(tier_label(Direction::Downstream, 1), "DIRECT DEPENDENCY");
        assert_eq!(
            tier_label(Direction::Downstream, 9),
            "TRANSITIVE DEPENDENCY"
        );
    }

    #[test]
    fn query_flags_are_validated() {
        let mut q = Query::new("x");
        q.apply_flags(
            [
                "--direction=downstream",
                "--max-depth=5",
                "--relation-types=CALLS,USES",
                "--min-confidence=0.9",
                "--include-tests=true",
                "--limit=7",
                "--offset=3",
                "--summary-only=true",
                "--timeout-ms=500",
            ]
            .map(String::from),
        )
        .unwrap();
        assert_eq!(q.direction, Direction::Downstream);
        assert_eq!((q.max_depth, q.limit, q.offset), (5, 7, 3));
        assert!(q.include_tests && q.summary_only);
        for bad in [
            "--max-depth=0",
            "--max-depth=33",
            "--min-confidence=1.5",
            "--direction=both",
            "--nope=1",
            "--limit",
        ] {
            assert!(
                Query::new("x").apply_flags([bad.to_string()]).is_err(),
                "{bad}"
            );
        }
    }
}
