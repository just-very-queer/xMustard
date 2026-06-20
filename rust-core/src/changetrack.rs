//! Change tracking (gitnexus-style): per-file content hashing, a repo
//! fingerprint (head SHA + remote + content hash), a durable index baseline,
//! stale-index + sibling-clone drift detection, changed-since computation, and
//! dirty *symbols* (not just dirty files). This is the honesty layer the
//! semantic index keys off of — it refuses to give stale answers silently.

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::process::Command;

use crate::repomap;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

fn git(root: &Path, args: &[&str]) -> Option<String> {
    let out = Command::new("git").arg("-C").arg(root).args(args).output().ok()?;
    if !out.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if text.is_empty() { None } else { Some(text) }
}

fn hash_file(path: &Path) -> Option<String> {
    let bytes = fs::read(path).ok()?;
    let mut hasher = Sha256::new();
    hasher.update(&bytes);
    Some(format!("{:x}", hasher.finalize()))
}

fn tracked_files(root: &Path) -> Vec<String> {
    git(root, &["ls-files"])
        .map(|s| s.lines().map(|l| l.to_string()).collect())
        .unwrap_or_default()
}

/// `git status --porcelain` parsed into (status_code, path).
fn dirty_paths(root: &Path) -> Vec<(String, String)> {
    let mut out = Vec::new();
    if let Some(text) = git(root, &["status", "--porcelain"]) {
        for line in text.lines() {
            if line.len() < 4 {
                continue;
            }
            let code = line[..2].trim().to_string();
            // porcelain is `XY<space>path`; trim from the status field so 1- or
            // 2-char codes and extra padding never shift the path.
            let path = line[2..].trim_start().to_string();
            out.push((code, path));
        }
    }
    out
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RepoFingerprint {
    pub root: String,
    pub head_sha: Option<String>,
    pub branch: Option<String>,
    pub remote_url: Option<String>,
    pub tracked_file_count: usize,
    pub content_hash: String,
    pub dirty: bool,
    pub dirty_path_count: usize,
    pub generated_at: String,
}

/// Build the file-hash map (path -> sha256) over tracked files.
fn file_hash_map(root: &Path) -> BTreeMap<String, String> {
    let mut map = BTreeMap::new();
    for rel in tracked_files(root) {
        if let Some(h) = hash_file(&root.join(&rel)) {
            map.insert(rel, h);
        }
    }
    map
}

fn content_hash_of(map: &BTreeMap<String, String>) -> String {
    let mut hasher = Sha256::new();
    for (path, hash) in map {
        hasher.update(path.as_bytes());
        hasher.update([0u8]);
        hasher.update(hash.as_bytes());
        hasher.update([b'\n']);
    }
    format!("{:x}", hasher.finalize())
}

/// Compute the current repo fingerprint.
pub fn compute_fingerprint(root: &Path) -> RepoFingerprint {
    let map = file_hash_map(root);
    let dirty = dirty_paths(root);
    RepoFingerprint {
        root: root.display().to_string(),
        head_sha: git(root, &["rev-parse", "HEAD"]),
        branch: git(root, &["rev-parse", "--abbrev-ref", "HEAD"]),
        remote_url: git(root, &["remote", "get-url", "origin"]),
        tracked_file_count: map.len(),
        content_hash: content_hash_of(&map),
        dirty: !dirty.is_empty(),
        dirty_path_count: dirty.len(),
        generated_at: now(),
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct IndexBaseline {
    pub workspace_id: String,
    pub fingerprint: RepoFingerprint,
    pub file_hashes: BTreeMap<String, String>,
    /// Per-symbol declaration signatures captured at index time, keyed by
    /// `path \x1f scope \x1f symbol`. Diffing a current signature against this is
    /// how a contract break (changed params / return type) is detected on a
    /// modified file. `serde(default)` so pre-R2 baselines still load.
    #[serde(default)]
    pub signatures: BTreeMap<String, String>,
    pub indexed_at: String,
}

/// Stable key for a symbol's signature across re-indexes: path + enclosing scope
/// (so two methods named `new` in different impl blocks don't collide) + name.
/// Line numbers are deliberately excluded — they shift when code above changes.
fn signature_key(path: &str, scope: Option<&str>, symbol: &str) -> String {
    format!("{path}\u{1f}{}\u{1f}{symbol}", scope.unwrap_or(""))
}

/// Best-effort declaration signature for a symbol: the text from its start line up
/// to the body opener (`{`) or a terminating `;`/`:`, with whitespace normalized.
/// This captures the parameter list and return type so a change to either is
/// detectable, while a change to the body (after `{`) leaves the signature
/// unchanged — exactly the contract-vs-implementation distinction we want.
fn symbol_signature(root: &Path, rel_path: &str, line_start: Option<usize>) -> Option<String> {
    let ls = line_start?; // 1-based
    if ls == 0 {
        return None;
    }
    let full = fs::read_to_string(root.join(rel_path)).ok()?;
    let lines: Vec<&str> = full.lines().collect();
    if ls > lines.len() {
        return None;
    }
    let mut sig = String::new();
    for line in lines.iter().skip(ls - 1).take(6) {
        if let Some(idx) = line.find('{') {
            sig.push_str(&line[..idx]);
            break; // brace languages: the body starts here, stop before it
        }
        sig.push_str(line);
        sig.push(' ');
        let t = line.trim_end();
        if t.ends_with(';') || t.ends_with(':') {
            break; // bodyless decl (trait method) or python `def ...:`
        }
    }
    let normalized = sig.split_whitespace().collect::<Vec<_>>().join(" ");
    if normalized.is_empty() {
        None
    } else {
        Some(normalized)
    }
}

/// Only function-like symbols carry a "contract" worth diffing.
fn is_contract_kind(kind: &str) -> bool {
    matches!(kind, "function" | "method")
}

/// Classify how two signatures differ, for a concise human reason.
fn classify_signature_change(old: &str, new: &str) -> String {
    let param_count = |s: &str| -> Option<usize> {
        let lp = s.find('(')?;
        let rp = s.rfind(')')?;
        if rp <= lp {
            return None;
        }
        let inner = s[lp + 1..rp].trim();
        if inner.is_empty() {
            Some(0)
        } else {
            Some(inner.split(',').count())
        }
    };
    let return_part = |s: &str| -> String {
        match s.rfind(')') {
            Some(rp) => s[rp + 1..].trim().trim_start_matches("->").trim().to_string(),
            None => String::new(),
        }
    };
    let mut parts = Vec::new();
    if let (Some(po), Some(pn)) = (param_count(old), param_count(new))
        && po != pn
    {
        parts.push(format!("params {po}→{pn}"));
    }
    let (ro, rn) = (return_part(old), return_part(new));
    if ro != rn {
        parts.push(format!("return `{ro}`→`{rn}`"));
    }
    if parts.is_empty() {
        "signature changed".to_string()
    } else {
        parts.join(", ")
    }
}

/// Capture a signature map over all tracked source files for the baseline.
fn collect_signatures(root: &Path, workspace_id: &str) -> BTreeMap<String, String> {
    let mut sigs = BTreeMap::new();
    for path in file_hash_map(root).keys() {
        if !is_source(path) {
            continue;
        }
        if let Ok(result) = repomap::extract_path_symbols(root, workspace_id, path) {
            for sym in &result.symbols {
                if !is_contract_kind(&sym.kind) {
                    continue;
                }
                if let Some(sig) = symbol_signature(root, path, sym.line_start) {
                    sigs.insert(
                        signature_key(path, sym.enclosing_scope.as_deref(), &sym.symbol),
                        sig,
                    );
                }
            }
        }
    }
    sigs
}

fn baseline_path(data_dir: &Path, workspace_id: &str) -> std::path::PathBuf {
    data_dir
        .join("workspaces")
        .join(workspace_id)
        .join("index_baseline.json")
}

/// Build and persist an index baseline for the current repo state.
pub fn build_index_baseline(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
) -> std::io::Result<IndexBaseline> {
    let map = file_hash_map(root);
    let dirty = dirty_paths(root);
    let fingerprint = RepoFingerprint {
        root: root.display().to_string(),
        head_sha: git(root, &["rev-parse", "HEAD"]),
        branch: git(root, &["rev-parse", "--abbrev-ref", "HEAD"]),
        remote_url: git(root, &["remote", "get-url", "origin"]),
        tracked_file_count: map.len(),
        content_hash: content_hash_of(&map),
        dirty: !dirty.is_empty(),
        dirty_path_count: dirty.len(),
        generated_at: now(),
    };
    let signatures = collect_signatures(root, workspace_id);
    let baseline = IndexBaseline {
        workspace_id: workspace_id.to_string(),
        fingerprint,
        file_hashes: map,
        signatures,
        indexed_at: now(),
    };
    let path = baseline_path(data_dir, workspace_id);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let body = serde_json::to_vec_pretty(&baseline).map_err(std::io::Error::other)?;
    fs::write(&path, body)?;
    Ok(baseline)
}

pub fn load_index_baseline(data_dir: &Path, workspace_id: &str) -> Option<IndexBaseline> {
    let bytes = fs::read(baseline_path(data_dir, workspace_id)).ok()?;
    serde_json::from_slice(&bytes).ok()
}

#[derive(Debug, Clone, Serialize)]
pub struct DriftReport {
    pub workspace_id: String,
    pub has_baseline: bool,
    pub stale: bool,
    pub head_changed: bool,
    pub content_changed: bool,
    pub sibling_clone: bool,
    pub baseline_head: Option<String>,
    pub current_head: Option<String>,
    pub baseline_remote: Option<String>,
    pub current_remote: Option<String>,
    pub reasons: Vec<String>,
    pub generated_at: String,
}

/// Detect whether the index baseline has drifted from the current worktree.
pub fn detect_drift(data_dir: &Path, root: &Path, workspace_id: &str) -> DriftReport {
    let current = compute_fingerprint(root);
    let baseline = load_index_baseline(data_dir, workspace_id);
    let mut reasons = Vec::new();
    let Some(baseline) = baseline else {
        return DriftReport {
            workspace_id: workspace_id.to_string(),
            has_baseline: false,
            stale: true,
            head_changed: false,
            content_changed: false,
            sibling_clone: false,
            baseline_head: None,
            current_head: current.head_sha.clone(),
            baseline_remote: None,
            current_remote: current.remote_url.clone(),
            reasons: vec!["no index baseline exists; results would be unindexed".to_string()],
            generated_at: now(),
        };
    };
    let head_changed = baseline.fingerprint.head_sha != current.head_sha;
    let content_changed = baseline.fingerprint.content_hash != current.content_hash;
    // sibling clone: same logical repo (remote) but the baseline was built from a
    // different checkout/path, or the remote differs entirely.
    let sibling_clone = match (&baseline.fingerprint.remote_url, &current.remote_url) {
        (Some(b), Some(c)) => b != c,
        _ => baseline.fingerprint.root != current.root && head_changed,
    };
    if head_changed {
        reasons.push(format!(
            "HEAD moved {} -> {}",
            baseline.fingerprint.head_sha.clone().unwrap_or_else(|| "?".into()),
            current.head_sha.clone().unwrap_or_else(|| "?".into())
        ));
    }
    if content_changed {
        reasons.push("tracked file content changed since indexing".to_string());
    }
    if sibling_clone {
        reasons.push("index baseline came from a different clone/remote".to_string());
    }
    if current.dirty {
        reasons.push(format!("{} uncommitted dirty path(s)", current.dirty_path_count));
    }
    DriftReport {
        workspace_id: workspace_id.to_string(),
        has_baseline: true,
        stale: head_changed || content_changed || sibling_clone,
        head_changed,
        content_changed,
        sibling_clone,
        baseline_head: baseline.fingerprint.head_sha,
        current_head: current.head_sha,
        baseline_remote: baseline.fingerprint.remote_url,
        current_remote: current.remote_url,
        reasons,
        generated_at: now(),
    }
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct ChangedFile {
    pub path: String,
    pub change: String, // added | modified | deleted
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct DirtySymbol {
    pub path: String,
    pub symbol: String,
    pub kind: String,
    pub change: String,
    /// True when this symbol's declaration signature differs from the indexed
    /// baseline (params or return type changed) — a downstream-caller-breaking
    /// change, distinct from a body-only edit.
    #[serde(default)]
    pub contract_break: bool,
    /// A concise description of the signature delta, when `contract_break`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub signature_change: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChangeSet {
    pub workspace_id: String,
    pub since: String,
    pub changed_files: Vec<ChangedFile>,
    pub dirty_symbols: Vec<DirtySymbol>,
    /// Count of dirty symbols whose signature broke vs the baseline (surfaced
    /// prominently by `impact`/`ground`).
    pub contract_breaks: usize,
    pub generated_at: String,
}

const SOURCE_EXTS: &[&str] = &[
    "rs", "go", "py", "ts", "tsx", "js", "jsx", "java", "rb", "c", "h", "cpp", "hpp", "cc",
];

fn is_source(path: &str) -> bool {
    Path::new(path)
        .extension()
        .and_then(|e| e.to_str())
        .map(|e| SOURCE_EXTS.contains(&e))
        .unwrap_or(false)
}

/// Pure diff of two file-hash maps into added/modified/deleted.
fn diff_hash_maps(
    baseline: &BTreeMap<String, String>,
    current: &BTreeMap<String, String>,
) -> Vec<ChangedFile> {
    let mut out = Vec::new();
    for (path, hash) in current {
        match baseline.get(path) {
            None => out.push(ChangedFile { path: path.clone(), change: "added".into() }),
            Some(b) if b != hash => {
                out.push(ChangedFile { path: path.clone(), change: "modified".into() })
            }
            _ => {}
        }
    }
    for path in baseline.keys() {
        if !current.contains_key(path) {
            out.push(ChangedFile { path: path.clone(), change: "deleted".into() });
        }
    }
    out.sort_by(|a, b| a.path.cmp(&b.path));
    out
}

/// Collect symbols in the changed (added/modified) source files, best-effort.
/// When `baseline_sigs` is provided, each function/method symbol's current
/// signature is diffed against the baseline to flag contract breaks (only on
/// `modified` files — an `added` file has no prior contract to break).
fn dirty_symbols_for(
    root: &Path,
    workspace_id: &str,
    changed: &[ChangedFile],
    baseline_sigs: Option<&BTreeMap<String, String>>,
) -> Vec<DirtySymbol> {
    let mut out = Vec::new();
    for cf in changed {
        if cf.change == "deleted" || !is_source(&cf.path) {
            continue;
        }
        if let Ok(result) = repomap::extract_path_symbols(root, workspace_id, &cf.path) {
            for sym in result.symbols {
                let mut contract_break = false;
                let mut signature_change = None;
                if cf.change == "modified"
                    && is_contract_kind(&sym.kind)
                    && let Some(sigs) = baseline_sigs
                {
                    let key = signature_key(&cf.path, sym.enclosing_scope.as_deref(), &sym.symbol);
                    if let (Some(old), Some(new)) =
                        (sigs.get(&key), symbol_signature(root, &cf.path, sym.line_start))
                        && old != &new
                    {
                        contract_break = true;
                        signature_change = Some(classify_signature_change(old, &new));
                    }
                }
                out.push(DirtySymbol {
                    path: cf.path.clone(),
                    symbol: sym.symbol,
                    kind: sym.kind,
                    change: cf.change.clone(),
                    contract_break,
                    signature_change,
                });
            }
        }
    }
    out
}

fn count_breaks(symbols: &[DirtySymbol]) -> usize {
    symbols.iter().filter(|s| s.contract_break).count()
}

/// Compute what changed since the index baseline, including dirty symbols.
pub fn changed_since_baseline(data_dir: &Path, root: &Path, workspace_id: &str) -> ChangeSet {
    let current = file_hash_map(root);
    let baseline = load_index_baseline(data_dir, workspace_id);
    let changed_files = match &baseline {
        Some(baseline) => diff_hash_maps(&baseline.file_hashes, &current),
        None => current
            .keys()
            .map(|p| ChangedFile { path: p.clone(), change: "added".into() })
            .collect(),
    };
    let dirty_symbols = dirty_symbols_for(
        root,
        workspace_id,
        &changed_files,
        baseline.as_ref().map(|b| &b.signatures),
    );
    ChangeSet {
        workspace_id: workspace_id.to_string(),
        since: "baseline".into(),
        contract_breaks: count_breaks(&dirty_symbols),
        dirty_symbols,
        changed_files,
        generated_at: now(),
    }
}

/// Compute uncommitted working-tree changes (git status) + dirty symbols.
/// Loads the index baseline (via `data_dir`) so it can flag contract breaks on
/// modified files — the session-grounding view an agent sees on reconnect.
pub fn working_tree_changes(data_dir: &Path, root: &Path, workspace_id: &str) -> ChangeSet {
    let mut changed_files = Vec::new();
    for (code, path) in dirty_paths(root) {
        let change = if code.contains('D') {
            "deleted"
        } else if code.contains('A') || code == "??" {
            "added"
        } else {
            "modified"
        };
        changed_files.push(ChangedFile { path, change: change.to_string() });
    }
    changed_files.sort_by(|a, b| a.path.cmp(&b.path));
    let baseline = load_index_baseline(data_dir, workspace_id);
    let dirty_symbols = dirty_symbols_for(
        root,
        workspace_id,
        &changed_files,
        baseline.as_ref().map(|b| &b.signatures),
    );
    ChangeSet {
        workspace_id: workspace_id.to_string(),
        since: "working-tree".into(),
        contract_breaks: count_breaks(&dirty_symbols),
        dirty_symbols,
        changed_files,
        generated_at: now(),
    }
}

// ---------------------------------------------------------------------------
// Incorporation lineage: an append-only chain of when each file was first
// indexed and each time its content changed, with the hash and head SHA at that
// point. This is the durable "how it was worked" lineage beyond a single diff.
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct IncorporationEvent {
    pub path: String,
    pub hash: String,
    pub head_sha: Option<String>,
    pub event: String, // indexed | changed | deleted
    pub at: String,
}

fn incorporation_path(data_dir: &Path, workspace_id: &str) -> std::path::PathBuf {
    workspace_dir_ct(data_dir, workspace_id).join("incorporation.json")
}

fn workspace_dir_ct(data_dir: &Path, workspace_id: &str) -> std::path::PathBuf {
    data_dir.join("workspaces").join(workspace_id)
}

fn load_incorporation(data_dir: &Path, workspace_id: &str) -> Vec<IncorporationEvent> {
    fs::read(incorporation_path(data_dir, workspace_id))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or_default()
}

/// Record incorporation events for any newly-added or changed (and deleted)
/// tracked files since the last recording, appending to the durable lineage.
/// Returns the events recorded this run.
pub fn record_incorporation(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
) -> std::io::Result<Vec<IncorporationEvent>> {
    let mut log = load_incorporation(data_dir, workspace_id);
    // last known hash per path (events are appended in order).
    let mut last_hash: BTreeMap<String, String> = BTreeMap::new();
    let mut known: BTreeSet<String> = BTreeSet::new();
    for e in &log {
        if e.event == "deleted" {
            last_hash.remove(&e.path);
            known.remove(&e.path);
        } else {
            last_hash.insert(e.path.clone(), e.hash.clone());
            known.insert(e.path.clone());
        }
    }

    let current = file_hash_map(root);
    let head = git(root, &["rev-parse", "HEAD"]);
    let at = now();
    let mut new_events = Vec::new();
    for (path, hash) in &current {
        match last_hash.get(path) {
            Some(prev) if prev == hash => {}
            Some(_) => new_events.push(IncorporationEvent {
                path: path.clone(),
                hash: hash.clone(),
                head_sha: head.clone(),
                event: "changed".to_string(),
                at: at.clone(),
            }),
            None => new_events.push(IncorporationEvent {
                path: path.clone(),
                hash: hash.clone(),
                head_sha: head.clone(),
                event: "indexed".to_string(),
                at: at.clone(),
            }),
        }
    }
    for path in &known {
        if !current.contains_key(path) {
            new_events.push(IncorporationEvent {
                path: path.clone(),
                hash: String::new(),
                head_sha: head.clone(),
                event: "deleted".to_string(),
                at: at.clone(),
            });
        }
    }

    if !new_events.is_empty() {
        log.extend(new_events.clone());
        let path = incorporation_path(data_dir, workspace_id);
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent)?;
        }
        let body = serde_json::to_vec_pretty(&log).map_err(std::io::Error::other)?;
        fs::write(&path, body)?;
    }
    Ok(new_events)
}

#[derive(Debug, Clone, Serialize)]
pub struct FileLineage {
    pub workspace_id: String,
    pub path: String,
    pub events: Vec<IncorporationEvent>,
    pub change_count: usize,
    pub generated_at: String,
}

/// The incorporation lineage (event chain) for one file.
pub fn file_lineage(data_dir: &Path, workspace_id: &str, path: &str) -> FileLineage {
    let events: Vec<IncorporationEvent> = load_incorporation(data_dir, workspace_id)
        .into_iter()
        .filter(|e| e.path == path)
        .collect();
    let change_count = events.iter().filter(|e| e.event == "changed").count();
    FileLineage {
        workspace_id: workspace_id.to_string(),
        path: path.to_string(),
        events,
        change_count,
        generated_at: now(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    fn git_init(dir: &Path) {
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git").arg("-C").arg(dir).args(&args).output().unwrap();
        }
    }
    fn git_commit(dir: &Path) {
        Command::new("git").arg("-C").arg(dir).args(["add", "-A"]).output().unwrap();
        Command::new("git").arg("-C").arg(dir).args(["commit", "-qm", "c"]).output().unwrap();
    }

    #[test]
    fn fingerprint_is_stable_and_changes_with_content() {
        let dir = TempDir::new().unwrap();
        git_init(dir.path());
        fs::write(dir.path().join("a.rs"), "pub fn one() {}\n").unwrap();
        git_commit(dir.path());
        let fp1 = compute_fingerprint(dir.path());
        let fp2 = compute_fingerprint(dir.path());
        assert_eq!(fp1.content_hash, fp2.content_hash);
        assert!(fp1.head_sha.is_some());
        assert_eq!(fp1.tracked_file_count, 1);

        fs::write(dir.path().join("a.rs"), "pub fn one() {}\npub fn two() {}\n").unwrap();
        let fp3 = compute_fingerprint(dir.path());
        assert_ne!(fp1.content_hash, fp3.content_hash);
        assert!(fp3.dirty);
    }

    #[test]
    fn baseline_drift_and_changed_since() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(repo.path().join("a.rs"), "pub fn one() {}\n").unwrap();
        git_commit(repo.path());

        let baseline = build_index_baseline(data.path(), repo.path(), "ws").unwrap();
        assert_eq!(baseline.file_hashes.len(), 1);

        // no drift right after indexing
        let drift0 = detect_drift(data.path(), repo.path(), "ws");
        assert!(!drift0.stale, "fresh baseline should not be stale: {:?}", drift0.reasons);

        // modify + commit -> head + content change -> stale
        fs::write(repo.path().join("a.rs"), "pub fn one() {}\npub fn two() {}\n").unwrap();
        git_commit(repo.path());
        let drift1 = detect_drift(data.path(), repo.path(), "ws");
        assert!(drift1.stale && drift1.head_changed && drift1.content_changed);

        let cs = changed_since_baseline(data.path(), repo.path(), "ws");
        assert!(cs.changed_files.iter().any(|c| c.path == "a.rs" && c.change == "modified"));
        // dirty symbols include the new function
        assert!(cs.dirty_symbols.iter().any(|s| s.symbol == "two"));
        // adding a NEW function is not a contract break of an existing symbol.
        assert_eq!(cs.contract_breaks, 0, "a new symbol is not a break: {:?}", cs.dirty_symbols);
    }

    #[test]
    fn contract_break_on_signature_change_not_on_body() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(
            repo.path().join("m.rs"),
            "pub fn add(a: u32, b: u32) -> u32 {\n    a + b\n}\npub fn keep(x: u32) -> u32 {\n    x\n}\n",
        )
        .unwrap();
        git_commit(repo.path());
        let baseline = build_index_baseline(data.path(), repo.path(), "ws").unwrap();
        assert!(
            baseline.signatures.values().any(|s| s.contains("fn add")),
            "baseline should capture the add signature: {:?}",
            baseline.signatures
        );

        // change `add`'s arity (a real contract break) but only touch `keep`'s body.
        fs::write(
            repo.path().join("m.rs"),
            "pub fn add(a: u32, b: u32, c: u32) -> u32 {\n    a + b + c\n}\npub fn keep(x: u32) -> u32 {\n    x + 0\n}\n",
        )
        .unwrap();
        git_commit(repo.path());

        let cs = changed_since_baseline(data.path(), repo.path(), "ws");
        assert_eq!(cs.contract_breaks, 1, "only `add` should break: {:?}", cs.dirty_symbols);
        let add = cs.dirty_symbols.iter().find(|s| s.symbol == "add").unwrap();
        assert!(add.contract_break, "add must be flagged: {add:?}");
        assert!(
            add.signature_change.as_deref().unwrap_or("").contains("params 2→3"),
            "reason should name the arity change: {:?}",
            add.signature_change
        );
        let keep = cs.dirty_symbols.iter().find(|s| s.symbol == "keep").unwrap();
        assert!(!keep.contract_break, "body-only edit is not a break: {keep:?}");
    }

    #[test]
    fn contract_break_surfaces_in_working_tree_changes() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(repo.path().join("api.rs"), "pub fn handler(req: u32) -> bool {\n    req > 0\n}\n")
            .unwrap();
        git_commit(repo.path());
        build_index_baseline(data.path(), repo.path(), "ws").unwrap();

        // change the return type WITHOUT committing → working-tree (grounding) view.
        fs::write(repo.path().join("api.rs"), "pub fn handler(req: u32) -> String {\n    req.to_string()\n}\n")
            .unwrap();
        let cs = working_tree_changes(data.path(), repo.path(), "ws");
        assert_eq!(cs.contract_breaks, 1, "return-type change should break: {:?}", cs.dirty_symbols);
        let h = cs.dirty_symbols.iter().find(|s| s.symbol == "handler").unwrap();
        assert!(
            h.signature_change.as_deref().unwrap_or("").contains("return"),
            "reason should name the return change: {:?}",
            h.signature_change
        );
    }

    #[test]
    fn diff_hash_maps_classifies() {
        let mut base = BTreeMap::new();
        base.insert("keep".to_string(), "h".to_string());
        base.insert("gone".to_string(), "h".to_string());
        base.insert("mod".to_string(), "h1".to_string());
        let mut cur = BTreeMap::new();
        cur.insert("keep".to_string(), "h".to_string());
        cur.insert("mod".to_string(), "h2".to_string());
        cur.insert("new".to_string(), "h".to_string());
        let diff = diff_hash_maps(&base, &cur);
        let by: BTreeMap<_, _> = diff.iter().map(|c| (c.path.as_str(), c.change.as_str())).collect();
        assert_eq!(by.get("new"), Some(&"added"));
        assert_eq!(by.get("mod"), Some(&"modified"));
        assert_eq!(by.get("gone"), Some(&"deleted"));
        assert_eq!(by.get("keep"), None);
    }

    #[test]
    fn incorporation_lineage_chains_changes() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(repo.path().join("a.rs"), "pub fn one() {}\n").unwrap();
        git_commit(repo.path());

        // first record -> "indexed"
        let e1 = record_incorporation(data.path(), repo.path(), "ws").unwrap();
        assert!(e1.iter().any(|e| e.path == "a.rs" && e.event == "indexed"));
        // no change -> no new events
        let e2 = record_incorporation(data.path(), repo.path(), "ws").unwrap();
        assert!(e2.is_empty());
        // change + record -> "changed"
        fs::write(repo.path().join("a.rs"), "pub fn one() {}\npub fn two() {}\n").unwrap();
        git_commit(repo.path());
        let e3 = record_incorporation(data.path(), repo.path(), "ws").unwrap();
        assert!(e3.iter().any(|e| e.path == "a.rs" && e.event == "changed"));

        let lineage = file_lineage(data.path(), "ws", "a.rs");
        assert_eq!(lineage.events.len(), 2); // indexed + changed
        assert_eq!(lineage.change_count, 1);
    }
}
