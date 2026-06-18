//! Wiki generation (knowledge layer): a graph-backed repo wiki — an overview
//! page plus one page per subsystem (top-level directory) listing its files and
//! key symbols. Generated from the symbol graph so it stays anchored to real
//! structure, not hand-written prose.

use chrono::{SecondsFormat, Utc};
use serde::Serialize;
use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::Path;

use crate::symbolgraph;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

fn top_dir(path: &str) -> String {
    match path.split_once('/') {
        Some((head, _)) => head.to_string(),
        None => "(root)".to_string(),
    }
}

fn slugify(s: &str) -> String {
    let mut out = String::new();
    let mut prev_dash = false;
    for ch in s.to_lowercase().chars() {
        if ch.is_ascii_alphanumeric() {
            out.push(ch);
            prev_dash = false;
        } else if !prev_dash {
            out.push('-');
            prev_dash = true;
        }
    }
    out.trim_matches('-').to_string()
}

#[derive(Debug, Clone, Serialize)]
pub struct WikiPage {
    pub title: String,
    pub slug: String,
    pub markdown: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct RepoWiki {
    pub workspace_id: String,
    pub page_count: usize,
    pub pages: Vec<WikiPage>,
    pub generated_at: String,
}

/// Generate the repo wiki: overview + per-subsystem pages.
pub fn generate_wiki(root: &Path, workspace_id: &str) -> RepoWiki {
    let graph = symbolgraph::build_symbol_graph(root, workspace_id);
    let hotspots = symbolgraph::compute_hotspots(&graph, 15);

    // group symbols by file, and files by subsystem (top dir)
    let mut symbols_by_file: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for s in &graph.symbols {
        symbols_by_file.entry(s.path.clone()).or_default().push(s.name.clone());
    }
    let mut files_by_subsystem: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for f in &graph.files {
        files_by_subsystem.entry(top_dir(&f.path)).or_default().push(f.path.clone());
    }

    let mut pages = Vec::new();

    // overview
    let mut overview = String::new();
    let _ = writeln!(overview, "# Repo Wiki\n");
    let _ = writeln!(
        overview,
        "{} files · {} symbols · {} reference edges.\n",
        graph.file_count, graph.symbol_count, graph.edge_count
    );
    let _ = writeln!(overview, "## Hotspots (most-depended-on files)\n");
    for h in &hotspots {
        let _ = writeln!(
            overview,
            "- `{}` — {} inbound refs from {} files",
            h.path, h.inbound_weight, h.dependent_count
        );
    }
    let _ = writeln!(overview, "\n## Subsystems\n");
    for (sub, files) in &files_by_subsystem {
        let _ = writeln!(
            overview,
            "- [{}](#subsystem-{}) — {} files",
            sub,
            slugify(sub),
            files.len()
        );
    }
    pages.push(WikiPage {
        title: "Overview".to_string(),
        slug: "overview".to_string(),
        markdown: overview,
    });

    // per-subsystem pages
    for (sub, files) in &files_by_subsystem {
        let mut body = String::new();
        let _ = writeln!(body, "# Subsystem: {sub}\n");
        let symbol_total: usize = files
            .iter()
            .map(|f| symbols_by_file.get(f).map(|s| s.len()).unwrap_or(0))
            .sum();
        let _ = writeln!(body, "{} files · {} symbols.\n", files.len(), symbol_total);
        let _ = writeln!(body, "## Files\n");
        for f in files {
            let syms = symbols_by_file.get(f).cloned().unwrap_or_default();
            let preview: Vec<String> = syms.iter().take(10).cloned().collect();
            if preview.is_empty() {
                let _ = writeln!(body, "- `{f}`");
            } else {
                let _ = writeln!(body, "- `{}` — {} symbols: {}", f, syms.len(), preview.join(", "));
            }
        }
        pages.push(WikiPage {
            title: format!("Subsystem: {sub}"),
            slug: format!("subsystem-{}", slugify(sub)),
            markdown: body,
        });
    }

    RepoWiki {
        workspace_id: workspace_id.to_string(),
        page_count: pages.len(),
        pages,
        generated_at: now(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    #[test]
    fn wiki_has_overview_and_subsystem_pages() {
        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(dir.path().join("core")).unwrap();
        std::fs::write(dir.path().join("core/lib.rs"), "pub fn widget() {}\npub struct Gear {}\n").unwrap();
        std::fs::write(dir.path().join("main.rs"), "fn main() { let _ = widget(); }\n").unwrap();
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git").arg("-C").arg(dir.path()).args(&args).output().unwrap();
        }
        Command::new("git").arg("-C").arg(dir.path()).args(["add", "-A"]).output().unwrap();
        Command::new("git").arg("-C").arg(dir.path()).args(["commit", "-qm", "c"]).output().unwrap();

        let wiki = generate_wiki(dir.path(), "ws");
        assert!(wiki.page_count >= 2);
        assert_eq!(wiki.pages[0].slug, "overview");
        assert!(wiki.pages[0].markdown.contains("Hotspots"));
        assert!(wiki.pages.iter().any(|p| p.slug == "subsystem-core"));
    }
}
