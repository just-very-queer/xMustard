//! Hybrid repo search (knowledge layer): lexical (BM25-style idf) ranking over
//! the symbol graph fused with structural signal (hotspot boost). This is the
//! in-process retrieval floor; the Postgres `search_document` tsvector tables in
//! the Go foundation are the scale path, and embeddings layer on later.

use chrono::{SecondsFormat, Utc};
use serde::Serialize;
use std::collections::{HashMap, HashSet};
use std::path::Path;

use crate::symbolgraph;

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

fn tokens(text: &str) -> Vec<String> {
    // split identifiers into subtokens (snake_case, camelCase, paths) so a query
    // like "dashboard" matches "widget_factory" and "BuildWorkspaceDashboard".
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

#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct SearchHit {
    pub kind: String, // "symbol" | "file"
    pub name: String,
    pub path: String,
    pub score: f64,
    pub reason: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct SearchResult {
    pub workspace_id: String,
    pub query: String,
    pub total: usize,
    pub hits: Vec<SearchHit>,
    pub generated_at: String,
}

/// Hybrid lexical + structural search over the repo's symbol graph.
pub fn hybrid_search(root: &Path, workspace_id: &str, query: &str, limit: usize) -> SearchResult {
    let graph = symbolgraph::build_symbol_graph(root, workspace_id);
    let hotspots: HashSet<String> = symbolgraph::compute_hotspots(&graph, 30)
        .into_iter()
        .map(|h| h.path)
        .collect();

    let mut qtokens: Vec<String> = tokens(query);
    qtokens.sort();
    qtokens.dedup();
    if qtokens.is_empty() {
        return SearchResult {
            workspace_id: workspace_id.to_string(),
            query: query.to_string(),
            total: 0,
            hits: Vec::new(),
            generated_at: now(),
        };
    }
    let query_lc = query.trim().to_lowercase();

    // document frequency of each query token across symbol-name tokens (idf).
    let n = graph.symbols.len().max(1) as f64;
    let mut df: HashMap<String, usize> = HashMap::new();
    let symbol_token_sets: Vec<HashSet<String>> = graph
        .symbols
        .iter()
        .map(|s| tokens(&s.name).into_iter().collect::<HashSet<String>>())
        .collect();
    for set in &symbol_token_sets {
        for t in &qtokens {
            if set.contains(t) {
                *df.entry(t.clone()).or_insert(0) += 1;
            }
        }
    }
    let idf = |t: &str| -> f64 {
        let d = *df.get(t).unwrap_or(&0) as f64;
        ((n + 1.0) / (d + 1.0)).ln() + 1.0
    };

    let mut hits: Vec<SearchHit> = Vec::new();
    for (i, sym) in graph.symbols.iter().enumerate() {
        let name_tokens = &symbol_token_sets[i];
        let path_tokens: HashSet<String> = tokens(&sym.path).into_iter().collect();
        let mut score = 0.0;
        let mut matched = Vec::new();
        for t in &qtokens {
            if name_tokens.contains(t) {
                score += idf(t);
                matched.push(t.clone());
            } else if path_tokens.contains(t) {
                score += 0.3 * idf(t);
            }
        }
        if score <= 0.0 {
            continue;
        }
        let mut reason = format!("matched {}", matched.join("+"));
        if sym.name.to_lowercase() == query_lc {
            score += 5.0;
            reason = "exact symbol match".to_string();
        }
        if hotspots.contains(&sym.path) {
            score += 0.5;
            reason.push_str(" · hotspot");
        }
        hits.push(SearchHit {
            kind: "symbol".to_string(),
            name: sym.name.clone(),
            path: sym.path.clone(),
            score,
            reason,
        });
    }

    // file path matches (lower weight, dedup against symbol hits' files)
    for f in &graph.files {
        let path_tokens: HashSet<String> = tokens(&f.path).into_iter().collect();
        let mut score = 0.0;
        for t in &qtokens {
            if path_tokens.contains(t) {
                score += 0.4 * idf(t);
            }
        }
        if score > 0.0 {
            hits.push(SearchHit {
                kind: "file".to_string(),
                name: f.path.clone(),
                path: f.path.clone(),
                score,
                reason: "path match".to_string(),
            });
        }
    }

    hits.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.name.cmp(&b.name))
    });
    let total = hits.len();
    hits.truncate(limit);
    SearchResult {
        workspace_id: workspace_id.to_string(),
        query: query.to_string(),
        total,
        hits,
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
    fn search_ranks_matching_symbol_first() {
        let repo = git_repo(&[
            ("a.rs", "pub fn compute_widget_total() {}\npub fn unrelated_thing() {}\n"),
            ("b.rs", "pub fn other_helper() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "compute_widget_total", 10);
        assert!(res.total >= 1);
        assert_eq!(res.hits[0].name, "compute_widget_total");
        assert_eq!(res.hits[0].reason, "exact symbol match");
    }

    #[test]
    fn search_token_match_and_empty_query() {
        let repo = git_repo(&[("a.rs", "pub fn widget_factory() {}\n")]);
        let res = hybrid_search(repo.path(), "ws", "widget", 10);
        assert!(res.hits.iter().any(|h| h.name == "widget_factory"));
        let empty = hybrid_search(repo.path(), "ws", "   ", 10);
        assert_eq!(empty.total, 0);
    }
}
