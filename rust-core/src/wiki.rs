//! Wiki generation (knowledge layer): a graph-backed repo wiki — an overview
//! page plus one page per subsystem (top-level directory) listing its files and
//! key symbols. Generated from the symbol graph so it stays anchored to real
//! structure, not hand-written prose.

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::path::Path;

use crate::indexcache;
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

#[derive(Debug, Clone, Serialize, Deserialize)]
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
    /// Slugs whose source (files + symbols) changed and were re-rendered this run.
    pub regenerated_slugs: Vec<String>,
    /// Slugs served byte-identical from the warm page cache (no source change).
    pub reused_slugs: Vec<String>,
    pub generated_at: String,
}

/// A cached subsystem page plus the fingerprint of the inputs that produced it.
#[derive(Debug, Clone, Serialize, Deserialize)]
struct CachedWikiPage {
    fingerprint: String,
    page: WikiPage,
}

/// Fingerprint a subsystem's rendered inputs: its sorted files, each file's symbol
/// count, and its sorted symbol names. A page is a pure function of exactly this, so
/// an unchanged fingerprint means the rendered markdown is byte-identical — safe to
/// reuse. Body-only edits that don't change the symbol set don't change it either.
fn subsystem_fingerprint(files: &[String], symbols_by_file: &BTreeMap<String, Vec<String>>) -> String {
    let mut h = Sha256::new();
    for f in files {
        h.update(f.as_bytes());
        h.update(b"\0");
        if let Some(syms) = symbols_by_file.get(f) {
            h.update(syms.len().to_string().as_bytes());
            h.update(b":");
            for s in syms {
                h.update(s.as_bytes());
                h.update(b",");
            }
        }
        h.update(b"\n");
    }
    format!("{:x}", h.finalize())
}

/// Generate the repo wiki: overview + per-subsystem pages. Incremental: the symbol
/// graph comes from the warm cache (only dirty files re-parsed), and each subsystem
/// page is re-rendered only when its fingerprint changed — unchanged subsystems are
/// served byte-identical from the page cache. The overview always re-renders (it
/// reflects global counts/hotspots and is a single small page).
pub fn generate_wiki(root: &Path, workspace_id: &str) -> RepoWiki {
    let graph = symbolgraph::build_symbol_graph_cached(root, workspace_id);
    let hotspots = symbolgraph::compute_hotspots(&graph, 15);

    // group symbols by file, and files by subsystem (top dir). Sort for a stable
    // render order so the fingerprint cache is correct (same inputs → same bytes).
    let mut symbols_by_file: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for s in &graph.symbols {
        symbols_by_file.entry(s.path.clone()).or_default().push(s.name.clone());
    }
    for syms in symbols_by_file.values_mut() {
        syms.sort();
    }
    let mut files_by_subsystem: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for f in &graph.files {
        files_by_subsystem.entry(top_dir(&f.path)).or_default().push(f.path.clone());
    }
    for files in files_by_subsystem.values_mut() {
        files.sort();
    }

    // load the warm page cache (subsystem slug → fingerprinted page).
    let mut cache: BTreeMap<String, CachedWikiPage> = indexcache::load_wiki_cache_bytes(root, workspace_id)
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or_default();

    let mut pages = Vec::new();
    let mut regenerated_slugs = Vec::new();
    let mut reused_slugs = Vec::new();

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
    regenerated_slugs.push("overview".to_string());

    // per-subsystem pages — re-rendered only when the subsystem's fingerprint moved.
    let mut next_cache: BTreeMap<String, CachedWikiPage> = BTreeMap::new();
    for (sub, files) in &files_by_subsystem {
        let slug = format!("subsystem-{}", slugify(sub));
        let fingerprint = subsystem_fingerprint(files, &symbols_by_file);

        let page = match cache.remove(&slug) {
            Some(cached) if cached.fingerprint == fingerprint => {
                reused_slugs.push(slug.clone());
                cached.page
            }
            _ => {
                regenerated_slugs.push(slug.clone());
                render_subsystem_page(sub, files, &symbols_by_file)
            }
        };
        next_cache.insert(slug, CachedWikiPage { fingerprint, page: page.clone() });
        pages.push(page);
    }

    // persist the rebuilt cache (stale subsystems naturally dropped — only current
    // slugs are inserted into next_cache).
    if let Ok(bytes) = serde_json::to_vec(&next_cache) {
        indexcache::store_wiki_cache_bytes(root, workspace_id, &bytes);
    }

    RepoWiki {
        workspace_id: workspace_id.to_string(),
        page_count: pages.len(),
        pages,
        regenerated_slugs,
        reused_slugs,
        generated_at: now(),
    }
}

/// Render a single subsystem page from its files + symbols (the cached unit).
fn render_subsystem_page(
    sub: &str,
    files: &[String],
    symbols_by_file: &BTreeMap<String, Vec<String>>,
) -> WikiPage {
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
    WikiPage {
        title: format!("Subsystem: {sub}"),
        slug: format!("subsystem-{}", slugify(sub)),
        markdown: body,
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

    fn git_init_commit(dir: &Path) {
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git").arg("-C").arg(dir).args(&args).output().unwrap();
        }
        Command::new("git").arg("-C").arg(dir).args(["add", "-A"]).output().unwrap();
        Command::new("git").arg("-C").arg(dir).args(["commit", "-qm", "c"]).output().unwrap();
    }

    #[test]
    fn wiki_regenerates_only_changed_subsystem() {
        let dir = TempDir::new().unwrap();
        std::fs::create_dir_all(dir.path().join("core")).unwrap();
        std::fs::create_dir_all(dir.path().join("util")).unwrap();
        std::fs::write(dir.path().join("core/lib.rs"), "pub fn widget() {}\n").unwrap();
        std::fs::write(dir.path().join("util/helpers.rs"), "pub fn format_path() {}\n").unwrap();
        git_init_commit(dir.path());

        // first run: cold cache → both subsystems regenerate, nothing reused.
        let first = generate_wiki(dir.path(), "ws");
        assert!(first.regenerated_slugs.contains(&"subsystem-core".to_string()));
        assert!(first.regenerated_slugs.contains(&"subsystem-util".to_string()));
        assert!(first.reused_slugs.is_empty(), "cold run reuses nothing: {:?}", first.reused_slugs);
        let util_first = first.pages.iter().find(|p| p.slug == "subsystem-util").unwrap().markdown.clone();

        // edit only core/ (add a symbol) → util/ is untouched.
        std::fs::write(dir.path().join("core/lib.rs"), "pub fn widget() {}\npub fn sprocket() {}\n").unwrap();

        let second = generate_wiki(dir.path(), "ws");
        assert!(
            second.regenerated_slugs.contains(&"subsystem-core".to_string()),
            "changed subsystem must regenerate: {:?}",
            second.regenerated_slugs
        );
        assert!(
            second.reused_slugs.contains(&"subsystem-util".to_string()),
            "untouched subsystem must be reused, not regenerated: regen={:?} reused={:?}",
            second.regenerated_slugs,
            second.reused_slugs
        );
        // the reused page is served byte-identical from the warm cache.
        let util_second = second.pages.iter().find(|p| p.slug == "subsystem-util").unwrap().markdown.clone();
        assert_eq!(util_first, util_second, "reused page must be byte-identical");
        // the regenerated core page reflects the new symbol.
        let core_second = second.pages.iter().find(|p| p.slug == "subsystem-core").unwrap();
        assert!(core_second.markdown.contains("sprocket"), "core page should show the new symbol");
    }
}
