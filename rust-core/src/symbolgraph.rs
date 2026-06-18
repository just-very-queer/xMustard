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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GraphFileNode {
    pub path: String,
    pub symbol_count: usize,
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
    pub kind: String, // "references"
    pub weight: usize,
    pub via_symbols: Vec<String>,
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

/// Build the symbol graph over tracked source files.
pub fn build_symbol_graph(root: &Path, workspace_id: &str) -> SymbolGraph {
    let files = tracked_source_files(root);
    let mut file_nodes = Vec::new();
    let mut symbols = Vec::new();
    // name -> defining path (first definer wins); only distinctive names anchor edges.
    let mut name_to_def: HashMap<String, String> = HashMap::new();
    let mut content_cache: BTreeMap<String, String> = BTreeMap::new();

    for rel in &files {
        let content = std::fs::read_to_string(root.join(rel)).unwrap_or_default();
        let syms = repomap::extract_path_symbols(root, workspace_id, rel)
            .map(|r| r.symbols)
            .unwrap_or_default();
        file_nodes.push(GraphFileNode { path: rel.clone(), symbol_count: syms.len() });
        for s in &syms {
            symbols.push(GraphSymbolNode {
                name: s.symbol.clone(),
                kind: s.kind.clone(),
                path: rel.clone(),
                line_start: s.line_start,
            });
            let lname = s.symbol.to_lowercase();
            if s.symbol.len() >= MIN_NAME_LEN && !STOPWORD_SYMBOLS.contains(&lname.as_str()) {
                name_to_def.entry(s.symbol.clone()).or_insert_with(|| rel.clone());
            }
        }
        content_cache.insert(rel.clone(), content);
    }

    // reference edges: a file that contains a symbol defined in a different file.
    let mut agg: HashMap<(String, String), (usize, BTreeSet<String>)> = HashMap::new();
    for (path, content) in &content_cache {
        let words = word_set(content);
        for word in &words {
            if let Some(def_path) = name_to_def.get(word) {
                if def_path == path {
                    continue;
                }
                let entry = agg
                    .entry((path.clone(), def_path.clone()))
                    .or_insert((0, BTreeSet::new()));
                entry.0 += 1;
                if entry.1.len() < 8 {
                    entry.1.insert(word.clone());
                }
            }
        }
    }
    let mut edges: Vec<GraphEdge> = agg
        .into_iter()
        .map(|((from, to), (weight, via))| GraphEdge {
            from_path: from,
            to_path: to,
            kind: "references".to_string(),
            weight,
            via_symbols: via.into_iter().collect(),
        })
        .collect();
    edges.sort_by(|a, b| b.weight.cmp(&a.weight).then(a.from_path.cmp(&b.from_path)));

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
pub fn compute_hotspots(graph: &SymbolGraph, limit: usize) -> Vec<Hotspot> {
    let mut inbound: HashMap<String, (usize, usize)> = HashMap::new();
    for e in &graph.edges {
        let entry = inbound.entry(e.to_path.clone()).or_insert((0, 0));
        entry.0 += e.weight;
        entry.1 += 1;
    }
    let mut out: Vec<Hotspot> = inbound
        .into_iter()
        .map(|(path, (w, d))| Hotspot { path, inbound_weight: w, dependent_count: d })
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
        if let Ok(result) = repomap::extract_path_symbols(root, workspace_id, rel) {
            if result.symbols.iter().any(|s| s.symbol == symbol) {
                defined_in.push(rel.clone());
            }
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
            std::fs::write(dir.path().join(rel), content).unwrap();
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
