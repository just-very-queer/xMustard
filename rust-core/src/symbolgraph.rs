//! Semantic symbol graph: the canonical symbol layer the cockpit reasons over.
//! Builds files → symbols → reference edges across the repo (lexical reference
//! resolution: a file that uses a symbol defined elsewhere gets an edge), then
//! derives hotspots (most-depended-on files) and blast radius (what a symbol
//! change can affect). This is the brain impact analysis and ownership key off.

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::path::Path;
use std::process::Command;

use crate::repomap;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

const SOURCE_EXTS: &[&str] = &[
    "rs", "go", "py", "ts", "tsx", "js", "jsx", "java", "rb", "c", "h", "cpp", "hpp", "cc",
];
const MAX_FILES: usize = 800;
const MIN_NAME_LEN: usize = 4;
// names this common produce noisy edges; skip as reference anchors.
const STOPWORD_SYMBOLS: &[&str] = &[
    "main", "test", "tests", "init", "new", "build", "run", "string", "error", "result",
    "value", "data", "name", "path", "self", "this", "type", "node", "item", "list",
];

fn is_source(path: &str) -> bool {
    Path::new(path)
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| SOURCE_EXTS.contains(&e))
        .unwrap_or(false)
}

fn tracked_source_files(root: &Path) -> Vec<String> {
    let out = Command::new("git").arg("-C").arg(root).args(["ls-files"]).output();
    let text = match out {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).into_owned(),
        _ => return Vec::new(),
    };
    text.lines()
        .filter(|l| is_source(l))
        .take(MAX_FILES)
        .map(|l| l.to_string())
        .collect()
}

fn word_set(content: &str) -> HashSet<String> {
    content
        .split(|c: char| !(c.is_alphanumeric() || c == '_'))
        .filter(|w| w.len() >= MIN_NAME_LEN)
        .map(|w| w.to_string())
        .collect()
}

/// A symbol definition: where it lives and what kind it is.
#[derive(Debug, Clone)]
struct SymbolDef {
    path: String,
    kind: String,
}

/// Test files reference code under test; they get "tests" edges, not "calls".
fn is_test_file(path: &str) -> bool {
    let lower = path.to_lowercase();
    lower.ends_with("_test.go")
        || lower.ends_with("_test.rs")
        || lower.ends_with("_test.py")
        || lower.ends_with(".test.ts")
        || lower.ends_with(".test.tsx")
        || lower.ends_with(".test.js")
        || lower.ends_with(".test.jsx")
        || lower.ends_with(".spec.ts")
        || lower.ends_with(".spec.tsx")
        || lower.ends_with(".spec.js")
        || lower.starts_with("test_")
        || lower.contains("/test_")
        || lower.contains("/tests/")
        || lower.contains("/__tests__/")
        || lower.starts_with("tests/")
        || lower.starts_with("test/")
}

/// Identifiers that appear on import/use lines — candidates for "imports" edges.
fn import_candidates(content: &str) -> HashSet<String> {
    let mut out = HashSet::new();
    for raw in content.lines() {
        let line = raw.trim_start();
        let is_import = line.starts_with("use ")
            || line.starts_with("import ")
            || line.starts_with("from ")
            || line.starts_with("pub use ")
            || line.starts_with("const ") && line.contains("require(")
            || line.contains(" require(");
        if !is_import {
            continue;
        }
        for tok in line.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
            if tok.len() >= MIN_NAME_LEN {
                out.insert(tok.to_string());
            }
        }
    }
    out
}

/// Relative module specifiers (./x, ../y) on import lines — resolved to repo files.
fn relative_import_specs(content: &str) -> Vec<String> {
    let mut out = Vec::new();
    for raw in content.lines() {
        let line = raw.trim_start();
        if !(line.starts_with("import ")
            || line.starts_with("export ")
            || line.contains("require(")
            || line.starts_with("from "))
        {
            continue;
        }
        // pull quoted specifiers that look relative
        for quote in ['\'', '"'] {
            let mut rest = line;
            while let Some(start) = rest.find(quote) {
                let after = &rest[start + 1..];
                if let Some(end) = after.find(quote) {
                    let spec = &after[..end];
                    if spec.starts_with("./") || spec.starts_with("../") {
                        out.push(spec.to_string());
                    }
                    rest = &after[end + 1..];
                } else {
                    break;
                }
            }
        }
    }
    out
}

/// Resolve a relative import spec (from `from_path`) to a tracked file path.
fn resolve_relative_import(from_path: &str, spec: &str, tracked: &HashSet<String>) -> Option<String> {
    let from_dir = Path::new(from_path).parent().unwrap_or_else(|| Path::new(""));
    let mut joined = from_dir.to_path_buf();
    for part in spec.split('/') {
        match part {
            "." | "" => {}
            ".." => {
                joined.pop();
            }
            other => joined.push(other),
        }
    }
    let base = joined.to_string_lossy().replace('\\', "/");
    let candidates = [
        base.clone(),
        format!("{base}.ts"),
        format!("{base}.tsx"),
        format!("{base}.js"),
        format!("{base}.jsx"),
        format!("{base}/index.ts"),
        format!("{base}/index.tsx"),
        format!("{base}/index.js"),
    ];
    candidates.into_iter().find(|c| tracked.contains(c))
}

/// Supertype names declared on inheritance lines — candidates for "inherits" edges.
/// Covers `extends`/`implements` (TS/JS/Java/PHP), `impl Trait for` (Rust),
/// and `class X(Base)` (Python).
fn inheritance_candidates(content: &str) -> HashSet<String> {
    let mut out = HashSet::new();
    for raw in content.lines() {
        let line = raw.trim();
        // TS/JS/Java: ... extends A implements B, C ...
        for kw in ["extends ", "implements "] {
            if let Some(idx) = line.find(kw) {
                let tail = &line[idx + kw.len()..];
                for tok in tail.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                    if tok.len() >= MIN_NAME_LEN {
                        out.insert(tok.to_string());
                    }
                    // stop at the block start
                    if tail.starts_with('{') {
                        break;
                    }
                }
            }
        }
        // Rust: impl Trait for Type  -> Trait is the supertype
        if line.starts_with("impl ")
            && let Some(for_idx) = line.find(" for ")
        {
            let trait_part = &line["impl ".len()..for_idx];
            for tok in trait_part.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                if tok.len() >= MIN_NAME_LEN {
                    out.insert(tok.to_string());
                }
            }
        }
        // Python: class X(Base1, Base2):
        if line.starts_with("class ")
            && let (Some(open), Some(close)) = (line.find('('), line.find(')'))
            && close > open
        {
            let bases = &line[open + 1..close];
            for tok in bases.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
                if tok.len() >= MIN_NAME_LEN {
                    out.insert(tok.to_string());
                }
            }
        }
    }
    out
}

/// Classify a code-level reference edge from `from_path` to a symbol of `def_kind`.
fn reference_edge_kind(from_path: &str, def_kind: &str) -> &'static str {
    if is_test_file(from_path) {
        "tests"
    } else if def_kind == "function" || def_kind == "method" {
        "calls"
    } else {
        "references"
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphFileNode {
    pub path: String,
    pub symbol_count: usize,
    /// Precomputed authority = total inbound reference weight (how depended-on this
    /// file is). Computed at index time so search/ranking need not recompute it.
    #[serde(default)]
    pub authority: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphSymbolNode {
    pub name: String,
    pub kind: String,
    pub path: String,
    pub line_start: Option<usize>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphEdge {
    pub from_path: String,
    pub to_path: String,
    pub kind: String, // "imports" | "calls" | "inherits" | "tests" | "references"
    pub weight: usize,
    pub via_symbols: Vec<String>,
    /// How the edge was resolved: "lexical" (identifier-name matching) or "lsp"
    /// (a real language-server reference). Defaults to lexical for older caches.
    #[serde(default = "default_resolution")]
    pub resolution: String,
}

fn default_resolution() -> String {
    "lexical".to_string()
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SymbolGraph {
    pub workspace_id: String,
    pub file_count: usize,
    pub symbol_count: usize,
    pub edge_count: usize,
    pub files: Vec<GraphFileNode>,
    pub symbols: Vec<GraphSymbolNode>,
    pub edges: Vec<GraphEdge>,
    pub generated_at: String,
}

/// A community of files that reference each other more than the rest of the repo —
/// a functional cluster that can cross directory boundaries (unlike top-dir
/// grouping). `label` is the most common directory among members (a readable name).
#[derive(Debug, Clone, Serialize)]
pub struct FileCluster {
    pub cluster_id: usize,
    pub label: String,
    pub files: Vec<String>,
    pub size: usize,
}

/// Detect file communities by label propagation over the reference-edge graph: each
/// file starts in its own community and repeatedly adopts the highest-edge-weight
/// community among its neighbours until stable. Deterministic (files processed in
/// sorted order, ties broken by smaller community id). Clusters can span directories
/// — two tightly-coupled files in different folders land together, which top-dir
/// grouping (ownership.rs) cannot do.
pub fn compute_clusters(graph: &SymbolGraph) -> Vec<FileCluster> {
    // undirected weighted adjacency over files.
    let mut adj: HashMap<String, HashMap<String, usize>> = HashMap::new();
    for e in &graph.edges {
        if e.from_path == e.to_path {
            continue;
        }
        *adj.entry(e.from_path.clone()).or_default().entry(e.to_path.clone()).or_insert(0) += e.weight;
        *adj.entry(e.to_path.clone()).or_default().entry(e.from_path.clone()).or_insert(0) += e.weight;
    }
    // every file is a node; isolated files keep their own community.
    let mut nodes: Vec<String> = graph.files.iter().map(|f| f.path.clone()).collect();
    nodes.sort();
    let mut community: HashMap<String, String> =
        nodes.iter().map(|n| (n.clone(), n.clone())).collect();

    for _ in 0..20 {
        let mut changed = false;
        for n in &nodes {
            let Some(neighbours) = adj.get(n) else { continue };
            // tally neighbour communities by total edge weight.
            let mut weight_by_comm: BTreeMap<String, usize> = BTreeMap::new();
            for (nbr, w) in neighbours {
                if let Some(c) = community.get(nbr) {
                    *weight_by_comm.entry(c.clone()).or_insert(0) += *w;
                }
            }
            // pick the heaviest community (ties → smallest id via BTreeMap order).
            if let Some((best, _)) = weight_by_comm
                .iter()
                .max_by(|a, b| a.1.cmp(b.1).then_with(|| b.0.cmp(a.0)))
                && community.get(n) != Some(best)
            {
                community.insert(n.clone(), best.clone());
                changed = true;
            }
        }
        if !changed {
            break;
        }
    }

    // group files by final community label, then relabel to compact ids by size.
    let mut groups: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for n in &nodes {
        groups.entry(community[n].clone()).or_default().push(n.clone());
    }
    let mut clusters: Vec<Vec<String>> = groups.into_values().collect();
    clusters.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a[0].cmp(&b[0])));
    clusters
        .into_iter()
        .enumerate()
        .map(|(i, mut files)| {
            files.sort();
            FileCluster {
                cluster_id: i,
                label: dominant_directory(&files),
                size: files.len(),
                files,
            }
        })
        .collect()
}

// the most common top-level directory among a cluster's files (a readable name).
fn dominant_directory(files: &[String]) -> String {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    for f in files {
        let dir = f.split('/').next().unwrap_or("(root)");
        *counts.entry(dir.to_string()).or_insert(0) += 1;
    }
    counts
        .into_iter()
        .max_by(|a, b| a.1.cmp(&b.1).then_with(|| b.0.cmp(&a.0)))
        .map(|(d, _)| d)
        .unwrap_or_else(|| "(root)".to_string())
}

/// Upgrade a (lexically-built) graph with scope-resolved CALLS edges from a real
/// language server. For the most-connected files first, within a bounded budget of
/// reference calls, it asks the LSP for each callable symbol's references and adds
/// `calls` edges (resolution = "lsp") that a language server actually resolved —
/// not name matches. Degrades to the lexical graph unchanged when no server is
/// installed or any request fails. Bounded so it never blocks on a huge repo.
pub fn upgrade_graph_with_lsp(root: &Path, mut graph: SymbolGraph, budget: usize) -> SymbolGraph {
    use crate::lsp_session::{LspSessionError, LspWorkspaceSession};

    // hard caps so the pass never blocks a large repo: a wall-clock deadline (a hung
    // server can't run it forever) and a fixed pre-open ceiling (independent of the
    // reference budget).
    const MAX_PREOPEN: usize = 300;
    let phase_deadline = std::time::Instant::now() + std::time::Duration::from_secs(120);

    // process files by inbound authority (hotspots first) so the budget is spent
    // where resolution matters most. Dedup with a set to stay O(n).
    let hot: Vec<String> = compute_hotspots(&graph, graph.files.len())
        .into_iter()
        .map(|h| h.path)
        .collect();
    let mut seen: HashSet<String> = hot.iter().cloned().collect();
    let mut ordered: Vec<String> = hot;
    for f in &graph.files {
        if seen.insert(f.path.clone()) {
            ordered.push(f.path.clone());
        }
    }

    // files grouped by their language server, so we can pre-open each language's
    // files before querying (cross-file references need the project loaded).
    let mut by_lang: std::collections::HashMap<&'static str, Vec<String>> =
        std::collections::HashMap::new();
    for f in &graph.files {
        if let Some(cmd) = crate::lsp_session::server_command_for(&f.path) {
            by_lang.entry(cmd).or_default().push(f.path.clone());
        }
    }

    let mut sessions: std::collections::HashMap<String, Option<LspWorkspaceSession>> =
        std::collections::HashMap::new();
    let mut remaining = budget;
    // collected lsp edges: (from, to) -> via symbols
    let mut lsp_edges: HashMap<(String, String), BTreeSet<String>> = HashMap::new();
    let mut lsp_resolved = false;

    'files: for rel in ordered {
        if remaining == 0 || std::time::Instant::now() >= phase_deadline {
            break;
        }
        let lang = match crate::lsp_session::server_command_for(&rel) {
            Some(cmd) => cmd,
            None => continue,
        };
        // lazily start one session per language; pre-open that language's files (up
        // to a fixed cap) so the server has the whole project in view.
        let session = sessions.entry(lang.to_string()).or_insert_with(|| {
            let mut s = LspWorkspaceSession::start(root, &rel, 20).ok();
            if let (Some(sess), Some(paths)) = (s.as_mut(), by_lang.get(lang)) {
                for p in paths.iter().take(MAX_PREOPEN) {
                    sess.open(p);
                }
            }
            s
        });
        if session.is_none() {
            continue;
        }

        let content = match std::fs::read_to_string(root.join(&rel)) {
            Ok(c) => c,
            Err(_) => continue,
        };
        let Some(symbols) = crate::treesitter::extract_symbols(&rel, &content) else { continue };
        let mut session_died = false;
        for sym in symbols {
            if remaining == 0 || std::time::Instant::now() >= phase_deadline {
                break 'files;
            }
            // only callable symbols anchor CALLS edges.
            if sym.kind != "function" && sym.kind != "method" {
                continue;
            }
            let Some(session) = sessions.get_mut(lang).and_then(|s| s.as_mut()) else { break };
            let line = (sym.line_start.saturating_sub(1)) as u32;
            match session.references(&rel, line, sym.name_column as u32) {
                Ok(refs) => {
                    remaining -= 1; // only consume budget on a successful call
                    lsp_resolved = true;
                    for (ref_path, _) in refs {
                        if ref_path == rel {
                            continue;
                        }
                        lsp_edges
                            .entry((ref_path, rel.clone()))
                            .or_default()
                            .insert(sym.symbol.clone());
                    }
                }
                // a timeout/disconnect likely corrupts the server: drop the session
                // so we don't keep querying a broken connection.
                Err(LspSessionError::Failed(_)) => {
                    session_died = true;
                    break;
                }
                Err(_) => continue,
            }
        }
        if session_died {
            sessions.insert(lang.to_string(), None);
        }
    }

    if !lsp_resolved {
        return graph; // no server resolved anything → leave the lexical graph as-is
    }
    // merge: an LSP-confirmed call edge supersedes the lexical edge for the same
    // (from, to) pair regardless of the lexical kind (calls/tests/references), so we
    // upgrade it in place to a resolved "calls" edge instead of adding a duplicate.
    for ((from, to), via) in lsp_edges {
        if let Some(existing) = graph
            .edges
            .iter_mut()
            .find(|e| e.from_path == from && e.to_path == to)
        {
            existing.kind = "calls".to_string();
            existing.resolution = "lsp".to_string();
            for v in &via {
                if existing.via_symbols.len() < 8 && !existing.via_symbols.contains(v) {
                    existing.via_symbols.push(v.clone());
                }
            }
        } else {
            graph.edges.push(GraphEdge {
                from_path: from,
                to_path: to,
                kind: "calls".to_string(),
                weight: via.len(),
                via_symbols: via.into_iter().take(8).collect(),
                resolution: "lsp".to_string(),
            });
        }
    }
    graph.edge_count = graph.edges.len();
    graph
}

/// Build the symbol graph, using the warm cache when the repo is unchanged. The
/// cache key is cheap (git HEAD + dirty-file mtimes), so a warm search loads the
/// cached graph instead of re-crawling the whole repo. Falls back to a fresh build
/// (and refreshes the cache) on a miss.
pub fn build_symbol_graph_cached(root: &Path, workspace_id: &str) -> SymbolGraph {
    let key = crate::indexcache::cheap_key(root);
    if let Some(graph) = crate::indexcache::load_cached_graph(root, workspace_id, &key) {
        return graph;
    }
    let graph = build_symbol_graph(root, workspace_id);
    crate::indexcache::store_cached_graph(root, workspace_id, &key, &graph);
    graph
}

/// Build the symbol graph over tracked source files.
pub fn build_symbol_graph(root: &Path, workspace_id: &str) -> SymbolGraph {
    let files = tracked_source_files(root);
    let tracked: HashSet<String> = files.iter().cloned().collect();
    let mut file_nodes = Vec::new();
    let mut symbols = Vec::new();
    // name -> ALL definitions. A name defined in multiple files is AMBIGUOUS:
    // lexical matching can't tell which one a reference means, so previously the
    // first-definer won and every reference was silently misrouted to it. We now
    // record all definers and skip ambiguous names for edge-anchoring (see
    // unique_definer) rather than route them wrong.
    let mut name_to_defs: HashMap<String, Vec<SymbolDef>> = HashMap::new();
    let mut content_cache: BTreeMap<String, String> = BTreeMap::new();

    // incremental reindex: per-file symbol cache keyed by content hash, so only
    // files whose content changed are re-parsed (tree-sitter parse is the dominant
    // cost). Clean files reuse their cached symbols.
    #[derive(serde::Serialize, serde::Deserialize, Clone)]
    struct CachedFileSymbols {
        hash: String,
        symbols: Vec<repomap::RustPathSymbolRecord>,
    }
    let mut sym_cache: HashMap<String, CachedFileSymbols> =
        crate::indexcache::load_symbol_cache_bytes(root, workspace_id)
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or_default();
    let mut next_cache: HashMap<String, CachedFileSymbols> = HashMap::new();

    for rel in &files {
        let content = std::fs::read_to_string(root.join(rel)).unwrap_or_default();
        let hash = crate::indexcache::file_hash(root, rel).unwrap_or_default();
        let syms = match sym_cache.remove(rel) {
            Some(c) if c.hash == hash && !hash.is_empty() => c.symbols, // reuse: clean file
            _ => repomap::extract_path_symbols(root, workspace_id, rel)
                .map(|r| r.symbols)
                .unwrap_or_default(),
        };
        next_cache.insert(rel.clone(), CachedFileSymbols { hash, symbols: syms.clone() });
        file_nodes.push(GraphFileNode { path: rel.clone(), symbol_count: syms.len(), authority: 0 });
        for s in &syms {
            symbols.push(GraphSymbolNode {
                name: s.symbol.clone(),
                kind: s.kind.clone(),
                path: rel.clone(),
                line_start: s.line_start,
            });
            let lname = s.symbol.to_lowercase();
            if s.symbol.len() >= MIN_NAME_LEN && !STOPWORD_SYMBOLS.contains(&lname.as_str()) {
                let defs = name_to_defs.entry(s.symbol.clone()).or_default();
                if !defs.iter().any(|d| d.path == *rel) {
                    defs.push(SymbolDef { path: rel.clone(), kind: s.kind.clone() });
                }
            }
        }
        content_cache.insert(rel.clone(), content);
    }
    // resolve a name to its single defining file, or None when ambiguous.
    let unique_definer = |name: &str| -> Option<&SymbolDef> {
        match name_to_defs.get(name) {
            Some(defs) if defs.len() == 1 => Some(&defs[0]),
            _ => None,
        }
    };

    // Typed edges, aggregated by (from, to, kind): a symbol that is imported AND
    // called yields both an "imports" and a "calls" edge (distinct relationships).
    let mut agg: HashMap<(String, String, &'static str), (usize, BTreeSet<String>)> = HashMap::new();
    let add_edge =
        |from: &str, to: &str, kind: &'static str, via: &str, agg: &mut HashMap<_, _>| {
            if from == to {
                return;
            }
            let entry: &mut (usize, BTreeSet<String>) = agg
                .entry((from.to_string(), to.to_string(), kind))
                .or_insert((0, BTreeSet::new()));
            entry.0 += 1;
            if entry.1.len() < 8 {
                entry.1.insert(via.to_string());
            }
        };

    for (path, content) in &content_cache {
        let imports = import_candidates(content);
        let inherits = inheritance_candidates(content);
        // Resolved relative-path imports (TS/JS) — file-level "imports" edges.
        for spec in relative_import_specs(content) {
            if let Some(target) = resolve_relative_import(path, &spec, &tracked) {
                add_edge(path, &target, "imports", &spec, &mut agg);
            }
        }
        // Inheritance: extends/implements/impl-for/class(Base) → "inherits".
        for name in &inherits {
            if let Some(def) = unique_definer(name)
                && &def.path != path
            {
                add_edge(path, &def.path, "inherits", name, &mut agg);
            }
        }
        // Symbol-level imports: an import/use line naming a symbol defined elsewhere.
        for name in &imports {
            if let Some(def) = unique_definer(name)
                && &def.path != path
            {
                add_edge(path, &def.path, "imports", name, &mut agg);
            }
        }
        // References across the file body → calls / tests / references (by def kind).
        let words = word_set(content);
        for word in &words {
            if inherits.contains(word) {
                continue; // already captured as a typed inheritance edge
            }
            if let Some(def) = unique_definer(word) {
                if &def.path == path {
                    continue;
                }
                let kind = reference_edge_kind(path, &def.kind);
                add_edge(path, &def.path, kind, word, &mut agg);
            }
        }
    }
    let mut edges: Vec<GraphEdge> = agg
        .into_iter()
        .map(|((from, to, kind), (weight, via))| GraphEdge {
            from_path: from,
            to_path: to,
            kind: kind.to_string(),
            weight,
            via_symbols: via.into_iter().collect(),
            resolution: "lexical".to_string(),
        })
        .collect();
    edges.sort_by(|a, b| {
        b.weight
            .cmp(&a.weight)
            .then(a.from_path.cmp(&b.from_path))
            .then(a.kind.cmp(&b.kind))
    });

    // precompute authority (total inbound reference weight) per file at index time.
    let mut inbound: HashMap<&str, usize> = HashMap::new();
    for e in &edges {
        *inbound.entry(e.to_path.as_str()).or_insert(0) += e.weight;
    }
    for node in &mut file_nodes {
        node.authority = inbound.get(node.path.as_str()).copied().unwrap_or(0);
    }

    // persist the per-file symbol cache for the next (incremental) build.
    if let Ok(bytes) = serde_json::to_vec(&next_cache) {
        crate::indexcache::store_symbol_cache_bytes(root, workspace_id, &bytes);
    }

    SymbolGraph {
        workspace_id: workspace_id.to_string(),
        file_count: file_nodes.len(),
        symbol_count: symbols.len(),
        edge_count: edges.len(),
        files: file_nodes,
        symbols,
        edges,
        generated_at: now(),
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct Hotspot {
    pub path: String,
    pub inbound_weight: usize,
    pub dependent_count: usize,
}

/// Most-depended-on files (high inbound reference weight) — risky to touch.
/// `dependent_count` is the number of distinct files that depend on the target,
/// regardless of how many typed edges connect them.
pub fn compute_hotspots(graph: &SymbolGraph, limit: usize) -> Vec<Hotspot> {
    let mut inbound_weight: HashMap<String, usize> = HashMap::new();
    let mut dependents: HashMap<String, BTreeSet<String>> = HashMap::new();
    for e in &graph.edges {
        *inbound_weight.entry(e.to_path.clone()).or_insert(0) += e.weight;
        dependents
            .entry(e.to_path.clone())
            .or_default()
            .insert(e.from_path.clone());
    }
    let mut out: Vec<Hotspot> = inbound_weight
        .into_iter()
        .map(|(path, w)| {
            let d = dependents.get(&path).map(BTreeSet::len).unwrap_or(0);
            Hotspot { path, inbound_weight: w, dependent_count: d }
        })
        .collect();
    out.sort_by(|a, b| b.inbound_weight.cmp(&a.inbound_weight).then(a.path.cmp(&b.path)));
    out.truncate(limit);
    out
}

#[derive(Debug, Clone, Serialize)]
pub struct BlastRadius {
    pub workspace_id: String,
    pub symbol: String,
    pub defined_in: Vec<String>,
    pub referencing_files: Vec<String>,
    pub referencing_file_count: usize,
    pub generated_at: String,
}

/// What files reference a given symbol — the blast radius of changing it.
pub fn blast_radius(root: &Path, workspace_id: &str, symbol: &str) -> BlastRadius {
    let files = tracked_source_files(root);
    let mut defined_in = Vec::new();
    let mut referencing = BTreeSet::new();
    for rel in &files {
        let content = std::fs::read_to_string(root.join(rel)).unwrap_or_default();
        if word_set(&content).contains(symbol) {
            referencing.insert(rel.clone());
        }
        if let Ok(result) = repomap::extract_path_symbols(root, workspace_id, rel)
            && result.symbols.iter().any(|s| s.symbol == symbol)
        {
            defined_in.push(rel.clone());
        }
    }
    for d in &defined_in {
        referencing.remove(d);
    }
    let referencing_files: Vec<String> = referencing.into_iter().collect();
    BlastRadius {
        workspace_id: workspace_id.to_string(),
        symbol: symbol.to_string(),
        defined_in,
        referencing_file_count: referencing_files.len(),
        referencing_files,
        generated_at: now(),
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
            let path = dir.path().join(rel);
            if let Some(parent) = path.parent() {
                std::fs::create_dir_all(parent).unwrap();
            }
            std::fs::write(path, content).unwrap();
        }
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git").arg("-C").arg(dir.path()).args(&args).output().unwrap();
        }
        Command::new("git").arg("-C").arg(dir.path()).args(["add", "-A"]).output().unwrap();
        Command::new("git").arg("-C").arg(dir.path()).args(["commit", "-qm", "c"]).output().unwrap();
        dir
    }

    #[test]
    fn graph_builds_edges_and_hotspots() {
        let repo = git_repo(&[
            ("lib.rs", "pub fn compute_widget() -> i32 { 1 }\npub struct WidgetFactory {}\n"),
            ("a.rs", "use crate::lib; fn run() { let _ = compute_widget(); let _f = WidgetFactory{}; }\n"),
            ("b.rs", "fn other() { let _ = compute_widget(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        assert_eq!(graph.file_count, 3);
        assert!(graph.symbol_count >= 2);
        // a.rs and b.rs reference compute_widget defined in lib.rs -> edges to lib.rs
        assert!(graph.edges.iter().any(|e| e.from_path == "a.rs" && e.to_path == "lib.rs"));
        assert!(graph.edges.iter().any(|e| e.from_path == "b.rs" && e.to_path == "lib.rs"));
        let hot = compute_hotspots(&graph, 5);
        assert_eq!(hot[0].path, "lib.rs", "lib.rs should be the top hotspot: {hot:?}");
        assert!(hot[0].dependent_count >= 2);
    }

    #[test]
    fn incremental_symbol_cache_is_correct_and_sets_authority() {
        let repo = git_repo(&[
            ("lib.rs", "pub fn helper() {}\n"),
            ("a.rs", "fn run() { helper(); }\n"),
            ("b.rs", "fn go() { helper(); }\n"),
        ]);
        // first build populates the per-file symbol cache.
        let g1 = build_symbol_graph(repo.path(), "ws");
        // second build reuses the cache — must produce the identical graph.
        let g2 = build_symbol_graph(repo.path(), "ws");
        assert_eq!(g1.symbol_count, g2.symbol_count);
        assert_eq!(g1.edge_count, g2.edge_count);
        // authority is precomputed: lib.rs is referenced by a.rs and b.rs.
        let lib = g2.files.iter().find(|f| f.path == "lib.rs").unwrap();
        assert!(lib.authority >= 2, "lib.rs authority should reflect 2 dependents: {lib:?}");
    }

    #[test]
    fn clusters_span_directories() {
        // core/engine.rs and api/handler.rs reference each other heavily (cross-dir);
        // they should land in one cluster, while an unrelated file stays separate —
        // which top-directory grouping could never produce.
        let repo = git_repo(&[
            ("core/engine.rs", "pub fn run_engine() {}\npub fn engine_step() {}\n"),
            ("api/handler.rs", "fn handle() { run_engine(); engine_step(); }\npub fn dispatch() {}\n"),
            ("core/engine_caller.rs", "fn go() { run_engine(); engine_step(); dispatch(); }\n"),
            ("misc/lonely.rs", "pub fn alone() {}\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let clusters = compute_clusters(&graph);
        // find the cluster containing core/engine.rs
        let eng = clusters.iter().find(|c| c.files.iter().any(|f| f == "core/engine.rs")).unwrap();
        assert!(
            eng.files.iter().any(|f| f == "api/handler.rs"),
            "cross-directory coupled files should cluster together: {clusters:?}"
        );
        // the unrelated file is not in that cluster.
        assert!(!eng.files.iter().any(|f| f == "misc/lonely.rs"));
    }

    #[test]
    fn ambiguous_name_does_not_misroute_edges() {
        // `handle` is defined in BOTH a.rs and b.rs. The old first-definer-wins
        // routed every reference to whichever file was scanned first. Now the name
        // is ambiguous → no lexical edge is anchored on it, so c.rs does not get a
        // false edge to an arbitrary definer.
        let repo = git_repo(&[
            ("a.rs", "pub fn handle_request() {}\n"),
            ("b.rs", "pub fn handle_request() {}\n"),
            ("c.rs", "fn run() { handle_request(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let edges_from_c: Vec<_> =
            graph.edges.iter().filter(|e| e.from_path == "c.rs").collect();
        assert!(
            edges_from_c.is_empty(),
            "ambiguous handle_request must not anchor a misrouted edge: {edges_from_c:?}"
        );
        // a uniquely-defined symbol still resolves.
        assert!(graph.edges.iter().all(|e| e.resolution == "lexical"));
    }

    #[test]
    fn graph_classifies_edge_kinds() {
        let repo = git_repo(&[
            ("core.rs", "pub fn compute_widget() -> i32 { 1 }\npub trait Renderable {}\n"),
            ("user.rs", "use crate::core::compute_widget;\nfn run() { let _ = compute_widget(); }\n"),
            ("impl.rs", "struct Panel {}\nimpl Renderable for Panel {}\n"),
            ("core_test.rs", "fn check() { let _ = compute_widget(); }\n"),
        ]);
        let graph = build_symbol_graph(repo.path(), "ws");
        let has = |from: &str, to: &str, kind: &str| {
            graph
                .edges
                .iter()
                .any(|e| e.from_path == from && e.to_path == to && e.kind == kind)
        };
        // user.rs imports + calls compute_widget from core.rs
        assert!(has("user.rs", "core.rs", "imports"), "imports edge: {:?}", graph.edges);
        assert!(has("user.rs", "core.rs", "calls"), "calls edge: {:?}", graph.edges);
        // impl.rs implements the Renderable trait defined in core.rs
        assert!(has("impl.rs", "core.rs", "inherits"), "inherits edge: {:?}", graph.edges);
        // core_test.rs is a test file referencing code under test
        assert!(has("core_test.rs", "core.rs", "tests"), "tests edge: {:?}", graph.edges);
    }

    #[test]
    fn blast_radius_finds_referencers() {
        let repo = git_repo(&[
            ("lib.rs", "pub fn compute_widget() {}\n"),
            ("a.rs", "fn run() { compute_widget(); }\n"),
            ("b.rs", "fn nope() {}\n"),
        ]);
        let br = blast_radius(repo.path(), "ws", "compute_widget");
        assert_eq!(br.defined_in, vec!["lib.rs".to_string()]);
        assert!(br.referencing_files.contains(&"a.rs".to_string()));
        assert!(!br.referencing_files.contains(&"b.rs".to_string()));
    }
}
