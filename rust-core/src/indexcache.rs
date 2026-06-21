//! Warm cache for the symbol graph (IndexEngine Phase 0). Building the graph is
//! O(repo) — re-crawl + re-parse + re-hash on every search. This caches the built
//! graph keyed by a CHEAP fingerprint (git HEAD + the dirty files' size/mtime, so
//! the key is O(dirty), not O(repo)); a warm search loads the cached graph and only
//! rebuilds when the key changes. The cache lives under the repo's `.git/` (already
//! ignored by git), or a temp dir when there is no `.git`.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::UNIX_EPOCH;

use sha2::{Digest, Sha256};

use crate::symbolgraph::SymbolGraph;

/// Atomically replace `path` with `bytes`: write to a per-process temp file in the
/// same directory, fsync it, then rename over the target. Rename is atomic on POSIX
/// same-filesystem, so a concurrent `xmustard-core` process reading the cache never
/// observes a torn/partial file (it sees either the old or the new bytes). A failed
/// or interrupted write leaves the old file intact rather than a truncated one.
fn atomic_write(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    let base = path.file_name().and_then(|s| s.to_str()).unwrap_or("cache");
    let tmp = dir.join(format!(".{base}.tmp.{}", std::process::id()));
    let write_result = (|| -> std::io::Result<()> {
        let mut f = fs::File::create(&tmp)?;
        f.write_all(bytes)?;
        f.sync_all()?;
        Ok(())
    })();
    if let Err(e) = write_result {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    if let Err(e) = fs::rename(&tmp, path) {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    Ok(())
}

fn git(root: &Path, args: &[&str]) -> Option<String> {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .ok()?;
    if !out.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if text.is_empty() { None } else { Some(text) }
}

fn sha_hex(input: &str) -> String {
    let mut h = Sha256::new();
    h.update(input.as_bytes());
    format!("{:x}", h.finalize())
}

/// A cheap content key: git HEAD plus each dirty file's path+size+mtime. This is
/// O(number of dirty files), so it is far cheaper than hashing the whole tree, yet
/// changes whenever any tracked file is edited.
pub fn cheap_key(root: &Path) -> String {
    let head = git(root, &["rev-parse", "HEAD"]).unwrap_or_else(|| "no-head".into());
    let mut sig = String::from(&head);
    if let Some(porc) = git(root, &["status", "--porcelain"]) {
        for line in porc.lines() {
            // porcelain lines look like " M path" / "?? path".
            let path = line.get(3..).unwrap_or("").trim();
            if path.is_empty() {
                continue;
            }
            sig.push('|');
            sig.push_str(path);
            if let Ok(meta) = fs::metadata(root.join(path)) {
                sig.push(':');
                sig.push_str(&meta.len().to_string());
                if let Ok(modified) = meta.modified()
                    && let Ok(dur) = modified.duration_since(UNIX_EPOCH)
                {
                    sig.push('@');
                    sig.push_str(&dur.as_millis().to_string());
                }
            }
        }
    }
    sha_hex(&sig)
}

/// Directory that holds cached graphs for this repo (per-repo, git-ignored).
fn cache_dir(root: &Path) -> PathBuf {
    let git_dir = root.join(".git");
    if git_dir.is_dir() {
        return git_dir.join("xmustard-cache");
    }
    std::env::temp_dir().join(format!(
        "xmustard-index-{}",
        sha_hex(&root.display().to_string())
    ))
}

fn graph_cache_file(root: &Path, workspace_id: &str, key: &str) -> PathBuf {
    cache_dir(root).join(format!("symbolgraph-{workspace_id}-{key}.json"))
}

/// sha256 of a tracked file's current content — the per-file key for incremental
/// reindex (only files whose hash changed need re-parsing).
pub fn file_hash(root: &Path, rel: &str) -> Option<String> {
    let bytes = fs::read(root.join(rel)).ok()?;
    let mut h = Sha256::new();
    h.update(&bytes);
    Some(format!("{:x}", h.finalize()))
}

/// Path to the per-workspace symbol cache (path → (hash, symbols)).
pub fn symbol_cache_path(root: &Path, workspace_id: &str) -> PathBuf {
    cache_dir(root).join(format!("symbols-{workspace_id}.json"))
}

/// Load the raw symbol-cache bytes (caller deserializes), or None if absent.
pub fn load_symbol_cache_bytes(root: &Path, workspace_id: &str) -> Option<Vec<u8>> {
    fs::read(symbol_cache_path(root, workspace_id)).ok()
}

/// Persist the symbol-cache bytes (best-effort).
pub fn store_symbol_cache_bytes(root: &Path, workspace_id: &str, bytes: &[u8]) {
    let dir = cache_dir(root);
    if fs::create_dir_all(&dir).is_ok() {
        let _ = atomic_write(&symbol_cache_path(root, workspace_id), bytes);
    }
}

/// Path to the per-workspace wiki page cache (subsystem slug → fingerprinted page).
pub fn wiki_cache_path(root: &Path, workspace_id: &str) -> PathBuf {
    cache_dir(root).join(format!("wiki-{workspace_id}.json"))
}

/// Load the raw wiki-cache bytes (caller deserializes), or None if absent.
pub fn load_wiki_cache_bytes(root: &Path, workspace_id: &str) -> Option<Vec<u8>> {
    fs::read(wiki_cache_path(root, workspace_id)).ok()
}

/// Persist the wiki-cache bytes (best-effort).
pub fn store_wiki_cache_bytes(root: &Path, workspace_id: &str, bytes: &[u8]) {
    let dir = cache_dir(root);
    if fs::create_dir_all(&dir).is_ok() {
        let _ = atomic_write(&wiki_cache_path(root, workspace_id), bytes);
    }
}

/// Load a cached symbol graph for the current cheap key, if present and valid.
pub fn load_cached_graph(root: &Path, workspace_id: &str, key: &str) -> Option<SymbolGraph> {
    let path = graph_cache_file(root, workspace_id, key);
    let bytes = fs::read(&path).ok()?;
    serde_json::from_slice::<SymbolGraph>(&bytes).ok()
}

/// Persist a freshly-built symbol graph under the current cheap key. Best-effort:
/// a failure to write the cache never fails the caller. Stale entries for other
/// keys are pruned so the cache does not grow unbounded.
pub fn store_cached_graph(root: &Path, workspace_id: &str, key: &str, graph: &SymbolGraph) {
    let dir = cache_dir(root);
    if fs::create_dir_all(&dir).is_err() {
        return;
    }
    prune_old(&dir, workspace_id, key);
    if let Ok(bytes) = serde_json::to_vec(graph) {
        let _ = atomic_write(&graph_cache_file(root, workspace_id, key), &bytes);
    }
}

fn prune_old(dir: &Path, workspace_id: &str, keep_key: &str) {
    let prefix = format!("symbolgraph-{workspace_id}-");
    let keep = format!("symbolgraph-{workspace_id}-{keep_key}.json");
    if let Ok(entries) = fs::read_dir(dir) {
        for entry in entries.flatten() {
            let name = entry.file_name().to_string_lossy().into_owned();
            if name.starts_with(&prefix) && name != keep {
                let _ = fs::remove_file(entry.path());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn atomic_write_replaces_and_leaves_no_temp() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("c.json");
        atomic_write(&target, b"first").unwrap();
        assert_eq!(fs::read(&target).unwrap(), b"first");
        // overwrite is atomic and complete (no partial)
        atomic_write(&target, b"second-longer").unwrap();
        assert_eq!(fs::read(&target).unwrap(), b"second-longer");
        // no leftover temp files in the directory
        let leftover: Vec<_> = fs::read_dir(dir.path())
            .unwrap()
            .flatten()
            .filter(|e| e.file_name().to_string_lossy().contains(".tmp."))
            .collect();
        assert!(
            leftover.is_empty(),
            "atomic_write left a temp file: {leftover:?}"
        );
    }

    #[test]
    fn cheap_key_changes_when_a_file_changes() {
        let dir = tempfile::TempDir::new().unwrap();
        let root = dir.path();
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git")
                .arg("-C")
                .arg(root)
                .args(&args)
                .output()
                .unwrap();
        }
        fs::write(root.join("a.rs"), "fn a() {}\n").unwrap();
        Command::new("git")
            .arg("-C")
            .arg(root)
            .args(["add", "-A"])
            .output()
            .unwrap();
        Command::new("git")
            .arg("-C")
            .arg(root)
            .args(["commit", "-qm", "c"])
            .output()
            .unwrap();

        let k1 = cheap_key(root);
        // editing a tracked file changes the key.
        fs::write(root.join("a.rs"), "fn a() { let _ = 1; }\n").unwrap();
        let k2 = cheap_key(root);
        assert_ne!(k1, k2, "key must change when a file is edited");
    }
}
