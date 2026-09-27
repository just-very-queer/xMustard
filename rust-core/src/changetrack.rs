//! Change tracking (gitnexus-style): per-file content hashing, a repo
//! fingerprint (head SHA + remote + content hash), a durable index baseline,
//! stale-index + sibling-clone drift detection, changed-since computation, and
//! dirty *symbols* (not just dirty files). This is the honesty layer the
//! semantic index keys off of — it refuses to give stale answers silently.
//!
//! Per-call work is bounded (PAR-RT-11). A change set lists at most
//! `ChangeBounds::listed_files` files and `symbols` dirty symbols, still counting every
//! one, and reads at most `symbol_files` changed source files, each once whatever its
//! symbol count; `truncation` says what was cut. A count it could not determine (a
//! failed Git listing, changed source files left unread, no baseline to compare
//! signatures against) is null and named in `unknown`, never 0.

use chrono::{SecondsFormat, Utc};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::cell::Cell;
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::process::Command;

use crate::repomap::{self, RustChangedSymbolRecord};
use crate::symbolgraph::{MAX_REPO_FILE_BYTES, SourceReadFailure, read_source_beneath};

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
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

/// A result field that could not be determined, and why. It stands in for a count
/// that would otherwise read as 0.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct Unknown {
    pub field: String,
    pub reason: String,
}

impl Unknown {
    fn new(field: &str, reason: impl Into<String>) -> Self {
        Unknown {
            field: field.to_string(),
            reason: reason.into(),
        }
    }
}

/// A bounded Git listing; a failure (not a repository, git missing, a timeout, output
/// past the cap) is an error, never an empty listing.
fn git_listing(root: &Path, args: &[&str]) -> Result<Vec<u8>, String> {
    crate::indexcache::run_git_bounded(
        root,
        args,
        crate::indexcache::MAX_GIT_OUTPUT_BYTES,
        crate::indexcache::git_timeout(),
    )
    .map_err(|e| format!("git {}: {e}", args[0]))
}

fn tracked_files(root: &Path) -> Result<Vec<String>, String> {
    // NUL-delimited and untrimmed, so whitespace/newline names stay exact.
    let out = git_listing(root, &["ls-files", "-z"])?;
    Ok(out
        .split(|b| *b == 0)
        .filter(|p| !p.is_empty())
        .filter_map(|p| String::from_utf8(p.to_vec()).ok())
        .collect())
}

/// `git status --porcelain=v1 -z` parsed into (status_code, path), untrimmed; a rename
/// reports its new path.
fn dirty_paths(root: &Path) -> Result<Vec<(String, String)>, String> {
    let out = git_listing(root, &["status", "--porcelain=v1", "-z"])?;
    Ok(crate::indexcache::parse_porcelain_v1_z(&out)
        .into_iter()
        .map(|e| {
            (
                String::from_utf8_lossy(&e.xy).trim().to_string(),
                String::from_utf8_lossy(&e.path).into_owned(),
            )
        })
        .collect())
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

/// Build the file-hash map (path -> sha256) over tracked files. Files are hashed through
/// the no-follow, bounded, regular-file-checked opener; unchanged ones come from the
/// stat cache in the Git dir, so only new, edited or racy files are read.
fn file_hash_map(root: &Path) -> Result<BTreeMap<String, String>, String> {
    file_hash_map_counted(root).map(|(map, _)| map)
}

fn file_hash_map_counted(
    root: &Path,
) -> Result<(BTreeMap<String, String>, crate::hashcache::HashPass), String> {
    // resolve the cache location (one `git rev-parse`) while `git ls-files` runs.
    let (rels, cache) = std::thread::scope(|s| {
        let cache = s.spawn(|| crate::hashcache::cache_file(root));
        (tracked_files(root), cache.join().ok().flatten())
    });
    Ok(crate::hashcache::hash_files(root, rels?, cache.as_deref()))
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

fn fingerprint_of(
    root: &Path,
    map: &BTreeMap<String, String>,
    dirty: &[(String, String)],
) -> RepoFingerprint {
    RepoFingerprint {
        root: root.display().to_string(),
        head_sha: git(root, &["rev-parse", "HEAD"]),
        branch: git(root, &["rev-parse", "--abbrev-ref", "HEAD"]),
        remote_url: git(root, &["remote", "get-url", "origin"]),
        tracked_file_count: map.len(),
        content_hash: content_hash_of(map),
        dirty: !dirty.is_empty(),
        dirty_path_count: dirty.len(),
        generated_at: now(),
    }
}

/// Compute the current repo fingerprint. A worktree Git cannot list is an error, not
/// the fingerprint of an empty repository.
pub fn compute_fingerprint(root: &Path) -> Result<RepoFingerprint, String> {
    let map = file_hash_map(root)?;
    let dirty = dirty_paths(root)?;
    Ok(fingerprint_of(root, &map, &dirty))
}

/// Why an index baseline was built (PAR-FRESH-06). Only `Admin` is an explicit request
/// (POST /index, which needs the indexer or admin role); the others are automatic.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum BaselineReason {
    /// The workspace was registered.
    Registration,
    /// The first ground found no baseline.
    FirstGround,
    /// HEAD moved while the worktree was clean.
    HeadChanged,
    /// An explicit rebaseline; also every baseline written before reasons were recorded,
    /// when POST /index was the only writer.
    #[default]
    Admin,
}

impl BaselineReason {
    pub const ALL: [BaselineReason; 4] = [
        BaselineReason::Registration,
        BaselineReason::FirstGround,
        BaselineReason::HeadChanged,
        BaselineReason::Admin,
    ];

    pub fn as_str(self) -> &'static str {
        match self {
            BaselineReason::Registration => "registration",
            BaselineReason::FirstGround => "first_ground",
            BaselineReason::HeadChanged => "head_changed",
            BaselineReason::Admin => "admin",
        }
    }

    pub fn parse(s: &str) -> Option<Self> {
        Self::ALL.into_iter().find(|r| r.as_str() == s)
    }

    /// Whether the baseline was built without an explicit request.
    pub fn auto(self) -> bool {
        self != BaselineReason::Admin
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
    /// Why this baseline was built; baselines written before WS-22 read as `admin`.
    #[serde(default)]
    pub reason: BaselineReason,
    pub indexed_at: String,
}

/// What `changetrack index` reports: the new baseline's identity, not its per-file maps
/// (those grow with the repository and stay in the baseline file).
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct BaselineSummary {
    pub workspace_id: String,
    pub head: Option<String>,
    pub branch: Option<String>,
    pub indexed_at: String,
    pub auto: bool,
    pub reason: BaselineReason,
    /// The worktree had uncommitted changes; their content is part of the baseline.
    pub dirty: bool,
    pub tracked_files: usize,
    pub signatures: usize,
    /// Whether a readable baseline existed before this build, and its HEAD.
    pub replaced: bool,
    pub previous_head: Option<String>,
}

impl From<&IndexBaseline> for BaselineSummary {
    fn from(b: &IndexBaseline) -> Self {
        BaselineSummary {
            workspace_id: b.workspace_id.clone(),
            head: b.fingerprint.head_sha.clone(),
            branch: b.fingerprint.branch.clone(),
            indexed_at: b.indexed_at.clone(),
            auto: b.reason.auto(),
            reason: b.reason,
            dirty: b.fingerprint.dirty,
            tracked_files: b.file_hashes.len(),
            signatures: b.signatures.len(),
            replaced: false,
            previous_head: None,
        }
    }
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
/// `lines` is the file's text, read once for all of its symbols.
fn symbol_signature(lines: &[&str], line_start: Option<usize>) -> Option<String> {
    let start = line_start.filter(|&ls| ls > 0 && ls <= lines.len())? - 1; // 1-based
    let mut sig = String::new();
    for line in lines.iter().skip(start).take(6) {
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
            Some(rp) => s[rp + 1..]
                .trim()
                .trim_start_matches("->")
                .trim()
                .to_string(),
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

thread_local! {
    static SOURCE_READS: Cell<u64> = const { Cell::new(0) };
}

/// How many source files this thread has read for symbols and signatures. A pass reads
/// each file at most once, whatever its symbol count (PAR-RT-11); tests hold it to that.
pub fn source_reads_on_this_thread() -> u64 {
    SOURCE_READS.with(Cell::get)
}

/// A symbol of a source file, with its declaration signature when it is a contract kind.
struct FileSymbol {
    record: RustChangedSymbolRecord,
    signature: Option<String>,
}

/// Why a source file contributed no symbols.
enum SourceSkip {
    /// A path or file the extractor never reads: excluded, oversized, a symlink, not a
    /// regular file, gone, or not UTF-8.
    Policy,
    /// A read error.
    Error(String),
}

/// Read `rel` once and extract its symbols, each contract symbol with its signature
/// taken from the same text.
fn read_file_symbols(root: &Path, rel: &str) -> Result<Vec<FileSymbol>, SourceSkip> {
    if !repomap::should_scan_file(rel) {
        return Err(SourceSkip::Policy);
    }
    SOURCE_READS.with(|n| n.set(n.get() + 1));
    let bytes =
        read_source_beneath(root, Path::new(rel), MAX_REPO_FILE_BYTES).map_err(|f| match f {
            SourceReadFailure::Unreadable(e) => SourceSkip::Error(e),
            _ => SourceSkip::Policy,
        })?;
    let text = String::from_utf8(bytes).map_err(|_| SourceSkip::Policy)?;
    let lines: Vec<&str> = text.lines().collect();
    let extracted =
        repomap::extract_source_symbols(rel, &text, crate::treesitter::MAX_SYMBOLS_PER_FILE);
    Ok(extracted
        .symbols
        .into_iter()
        .map(|record| FileSymbol {
            signature: is_contract_kind(&record.kind)
                .then(|| symbol_signature(&lines, record.line_start))
                .flatten(),
            record,
        })
        .collect())
}

/// The signature map of the contract symbols in `paths`' source files, each read once.
fn collect_signatures<'a>(
    root: &Path,
    paths: impl Iterator<Item = &'a String>,
) -> BTreeMap<String, String> {
    let mut sigs = BTreeMap::new();
    for path in paths.filter(|p| is_source(p)) {
        let Ok(symbols) = read_file_symbols(root, path) else {
            continue;
        };
        for s in symbols {
            if let Some(sig) = s.signature {
                sigs.insert(
                    signature_key(path, s.record.enclosing_scope.as_deref(), &s.record.symbol),
                    sig,
                );
            }
        }
    }
    sigs
}

fn baseline_path(data_dir: &Path, workspace_id: &str) -> std::path::PathBuf {
    workspace_dir_ct(data_dir, workspace_id).join("index_baseline.json")
}

/// Build and persist an explicit (admin) index baseline for the current repo state.
pub fn build_index_baseline(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
) -> std::io::Result<IndexBaseline> {
    rebaseline(data_dir, root, workspace_id, BaselineReason::Admin)
}

/// Build an index baseline for the current repo state and replace the stored one
/// atomically. A worktree Git cannot list fails the build: an empty baseline would
/// silently reset every later diff.
pub fn rebaseline(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
    reason: BaselineReason,
) -> std::io::Result<IndexBaseline> {
    let map = file_hash_map(root).map_err(std::io::Error::other)?;
    let dirty = dirty_paths(root).map_err(std::io::Error::other)?;
    let baseline = IndexBaseline {
        workspace_id: workspace_id.to_string(),
        fingerprint: fingerprint_of(root, &map, &dirty),
        signatures: collect_signatures(root, map.keys()),
        file_hashes: map,
        reason,
        indexed_at: now(),
    };
    let path = baseline_path(data_dir, workspace_id);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    crate::indexcache::atomic_write_with(&path, |w| {
        serde_json::to_writer(w, &baseline).map_err(std::io::Error::other)
    })?;
    Ok(baseline)
}

/// `rebaseline`, reported as its summary with the baseline it replaced, if any.
pub fn rebaseline_summary(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
    reason: BaselineReason,
) -> std::io::Result<BaselineSummary> {
    let previous = stored_baseline_head(data_dir, workspace_id);
    let baseline = rebaseline(data_dir, root, workspace_id, reason)?;
    Ok(BaselineSummary {
        replaced: previous.is_some(),
        previous_head: previous.flatten(),
        ..BaselineSummary::from(&baseline)
    })
}

/// The HEAD the stored baseline was taken at; None when there is no readable baseline.
/// Only the fingerprint's head is decoded, not the per-file maps.
fn stored_baseline_head(data_dir: &Path, workspace_id: &str) -> Option<Option<String>> {
    #[derive(Deserialize)]
    struct Head {
        head_sha: Option<String>,
    }
    #[derive(Deserialize)]
    struct Stored {
        fingerprint: Head,
    }
    let bytes = fs::read(baseline_path(data_dir, workspace_id)).ok()?;
    serde_json::from_slice::<Stored>(&bytes)
        .ok()
        .map(|s| s.fingerprint.head_sha)
}

/// The stored baseline: Ok(None) when there is none, Err when it cannot be read.
fn read_baseline(data_dir: &Path, workspace_id: &str) -> Result<Option<IndexBaseline>, String> {
    let bytes = match fs::read(baseline_path(data_dir, workspace_id)) {
        Ok(bytes) => bytes,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(format!("index baseline unreadable: {e}")),
    };
    serde_json::from_slice(&bytes)
        .map(Some)
        .map_err(|e| format!("index baseline unreadable: {e}"))
}

pub fn load_index_baseline(data_dir: &Path, workspace_id: &str) -> Option<IndexBaseline> {
    read_baseline(data_dir, workspace_id).ok().flatten()
}

/// Why there is no usable baseline: none exists, or it cannot be read.
fn missing_baseline_reason(read: &Result<Option<IndexBaseline>, String>) -> String {
    match read {
        Err(e) => format!("{e} (treated as missing)"),
        _ => "no index baseline exists".to_string(),
    }
}

/// The reasons that are present, joined; None when there are none.
fn join_reasons(parts: impl IntoIterator<Item = Option<String>>) -> Option<String> {
    let parts: Vec<String> = parts.into_iter().flatten().collect();
    (!parts.is_empty()).then(|| parts.join("; "))
}

#[derive(Debug, Clone, Serialize)]
pub struct DriftReport {
    pub workspace_id: String,
    pub has_baseline: bool,
    pub stale: bool,
    pub head_changed: bool,
    pub content_changed: bool,
    pub sibling_clone: bool,
    /// The worktree has uncommitted changes now.
    pub dirty: bool,
    pub baseline_head: Option<String>,
    pub current_head: Option<String>,
    pub baseline_remote: Option<String>,
    pub current_remote: Option<String>,
    /// When the baseline was built and why (null without one); every reason but
    /// `admin` is automatic.
    pub baseline_indexed_at: Option<String>,
    pub baseline_reason: Option<BaselineReason>,
    pub reasons: Vec<String>,
    /// Why the worktree could not be fingerprinted; the report is then stale and its
    /// comparison flags are not determined.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub generated_at: String,
}

/// Detect whether the index baseline has drifted from the current worktree.
pub fn detect_drift(data_dir: &Path, root: &Path, workspace_id: &str) -> DriftReport {
    let read = read_baseline(data_dir, workspace_id);
    let missing = missing_baseline_reason(&read);
    let baseline = read.ok().flatten();
    let meta = baseline.as_ref();
    let mut report = DriftReport {
        workspace_id: workspace_id.to_string(),
        has_baseline: baseline.is_some(),
        stale: true,
        head_changed: false,
        content_changed: false,
        sibling_clone: false,
        dirty: false,
        baseline_head: meta.and_then(|b| b.fingerprint.head_sha.clone()),
        current_head: None,
        baseline_remote: meta.and_then(|b| b.fingerprint.remote_url.clone()),
        current_remote: None,
        baseline_indexed_at: meta.map(|b| b.indexed_at.clone()),
        baseline_reason: meta.map(|b| b.reason),
        reasons: Vec::new(),
        error: None,
        generated_at: now(),
    };
    let current = match compute_fingerprint(root) {
        Ok(current) => current,
        Err(e) => {
            report
                .reasons
                .push(format!("cannot fingerprint the worktree: {e}"));
            report.error = Some(e);
            return report;
        }
    };
    report.current_head = current.head_sha.clone();
    report.current_remote = current.remote_url.clone();
    report.dirty = current.dirty;
    let Some(baseline) = baseline else {
        report
            .reasons
            .push(format!("{missing}; results would be unindexed"));
        return report;
    };
    report.head_changed = baseline.fingerprint.head_sha != current.head_sha;
    report.content_changed = baseline.fingerprint.content_hash != current.content_hash;
    // sibling clone: same logical repo (remote) but the baseline was built from a
    // different checkout/path, or the remote differs entirely.
    report.sibling_clone = match (&baseline.fingerprint.remote_url, &current.remote_url) {
        (Some(b), Some(c)) => b != c,
        _ => baseline.fingerprint.root != current.root && report.head_changed,
    };
    report.stale = report.head_changed || report.content_changed || report.sibling_clone;
    if report.head_changed {
        report.reasons.push(format!(
            "HEAD moved {} -> {}",
            baseline.fingerprint.head_sha.as_deref().unwrap_or("?"),
            current.head_sha.as_deref().unwrap_or("?")
        ));
    }
    if report.content_changed {
        report
            .reasons
            .push("tracked file content changed since indexing".to_string());
    }
    if report.sibling_clone {
        report
            .reasons
            .push("index baseline came from a different clone/remote".to_string());
    }
    if current.dirty {
        report.reasons.push(format!(
            "{} uncommitted dirty path(s)",
            current.dirty_path_count
        ));
    }
    report
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

/// Caps on one changed-since or working-changes pass (PAR-RT-11).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ChangeBounds {
    /// Changed files listed in `changed_files`; every one is still counted.
    pub listed_files: usize,
    /// Changed source files read for dirty symbols (modified files first).
    pub symbol_files: usize,
    /// Dirty symbols listed; every one is still counted.
    pub symbols: usize,
}

impl ChangeBounds {
    /// The bounds of every changed-since and working-changes call.
    pub const DEFAULT: ChangeBounds = ChangeBounds {
        listed_files: 1000,
        symbol_files: 200,
        symbols: 2000,
    };
}

/// How a change set was cut short.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct Truncation {
    pub reason: String,
    pub files_listed: usize,
    pub files_total: usize,
    /// Changed source files read for symbols, of those that could be.
    pub symbol_files_read: usize,
    pub symbol_files_total: usize,
    /// Dirty symbols listed, of those found in the files read.
    pub symbols_listed: usize,
    pub symbols_total: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChangeSet {
    pub workspace_id: String,
    pub since: String,
    /// Whether an index baseline was compared against. Without one, changed-since
    /// lists tracked files as added (capped, no symbols) and contract breaks are unknown.
    pub has_baseline: bool,
    /// The changed files, at most `ChangeBounds::listed_files`; null when Git could
    /// not list them.
    pub changed_files: Option<Vec<ChangedFile>>,
    /// Every changed file, listed or not; null when Git could not list them.
    pub changed_files_total: Option<usize>,
    /// Symbols of the changed source files read this pass, at most
    /// `ChangeBounds::symbols` (contract breaks first); a partial pass is named in
    /// `unknown`, and null means none could be determined.
    pub dirty_symbols: Option<Vec<DirtySymbol>>,
    /// Every dirty symbol, listed or not; null when the pass was partial.
    pub dirty_symbols_total: Option<usize>,
    /// Count of dirty symbols whose signature broke vs the baseline (surfaced
    /// prominently by `impact`/`ground`); null when the symbol pass was partial or
    /// there is no baseline to compare against.
    pub contract_breaks: Option<usize>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub truncation: Option<Truncation>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub unknown: Vec<Unknown>,
    pub generated_at: String,
}

impl ChangeSet {
    fn new(workspace_id: &str, since: &str, has_baseline: bool) -> Self {
        ChangeSet {
            workspace_id: workspace_id.to_string(),
            since: since.to_string(),
            has_baseline,
            changed_files: None,
            changed_files_total: None,
            dirty_symbols: None,
            dirty_symbols_total: None,
            contract_breaks: None,
            truncation: None,
            unknown: Vec::new(),
            generated_at: now(),
        }
    }

    /// Git could not list the changes: every count is unknown, for the same reason.
    fn unlisted(mut self, reason: &str) -> Self {
        self.unknown = ["changed_files", "dirty_symbols", "contract_breaks"]
            .into_iter()
            .map(|field| Unknown::new(field, reason))
            .collect();
        self
    }
}

// Change-tracking treats the same files the symbol graph does (code + tests) as
// "source", so it delegates to the canonical repo_role classifier rather than
// keeping its own duplicate extension list (XM-PRO-012).
fn is_source(path: &str) -> bool {
    matches!(crate::symbolgraph::repo_role(path), "code" | "test")
}

/// Pure diff of two file-hash maps into added/modified/deleted.
fn diff_hash_maps(
    baseline: &BTreeMap<String, String>,
    current: &BTreeMap<String, String>,
) -> Vec<ChangedFile> {
    let mut out = Vec::new();
    for (path, hash) in current {
        match baseline.get(path) {
            None => out.push(ChangedFile {
                path: path.clone(),
                change: "added".into(),
            }),
            Some(b) if b != hash => out.push(ChangedFile {
                path: path.clone(),
                change: "modified".into(),
            }),
            _ => {}
        }
    }
    for path in baseline.keys() {
        if !current.contains_key(path) {
            out.push(ChangedFile {
                path: path.clone(),
                change: "deleted".into(),
            });
        }
    }
    out.sort_by(|a, b| a.path.cmp(&b.path));
    out
}

/// A dirty symbol of changed file `cf`. With the baseline's signatures, a modified
/// file's contract symbol whose signature changed is a contract break (an added file
/// has no prior contract to break).
fn dirty_symbol(
    cf: &ChangedFile,
    s: FileSymbol,
    sigs: Option<&BTreeMap<String, String>>,
) -> DirtySymbol {
    let key = signature_key(
        &cf.path,
        s.record.enclosing_scope.as_deref(),
        &s.record.symbol,
    );
    let old = match sigs {
        Some(sigs) if cf.change == "modified" => sigs.get(&key),
        _ => None,
    };
    let signature_change = match (old, &s.signature) {
        (Some(old), Some(new)) if old != new => Some(classify_signature_change(old, new)),
        _ => None,
    };
    DirtySymbol {
        path: cf.path.clone(),
        symbol: s.record.symbol,
        kind: s.record.kind,
        change: cf.change.clone(),
        contract_break: signature_change.is_some(),
        signature_change,
    }
}

/// What the dirty-symbol pass found.
struct SymbolPass {
    /// The listed symbols, at most `ChangeBounds::symbols`, contract breaks first, in
    /// changed-file order.
    listed: Vec<DirtySymbol>,
    /// Every symbol, and every contract break, of the files read.
    symbols: usize,
    breaks: usize,
    files_read: usize,
    files_total: usize,
    /// Why the pass did not read every changed source file, if it did not.
    partial: Option<String>,
}

/// The dirty-symbol pass over `changed` within `bounds`. Modified files are read first
/// and a listed contract break is never displaced by a plain symbol, so the caps keep
/// contract-break detection. Counts stay exact past the listing cap; only a file left
/// unread makes the pass partial.
fn symbol_pass(
    root: &Path,
    changed: &[ChangedFile],
    sigs: Option<&BTreeMap<String, String>>,
    bounds: ChangeBounds,
) -> SymbolPass {
    let mut candidates: Vec<&ChangedFile> = changed
        .iter()
        .filter(|cf| cf.change != "deleted" && is_source(&cf.path))
        .collect();
    candidates.sort_by_key(|cf| cf.change != "modified"); // stable: modified first
    let mut pass = SymbolPass {
        listed: Vec::new(),
        symbols: 0,
        breaks: 0,
        files_read: 0,
        files_total: candidates.len(),
        partial: None,
    };
    // (discovery order, symbol); breaks and the rest are capped apart
    let (mut breaks, mut rest) = (Vec::new(), Vec::new());
    let mut errors = Vec::new();
    for cf in candidates.iter().take(bounds.symbol_files) {
        pass.files_read += 1;
        let found = match read_file_symbols(root, &cf.path) {
            Ok(found) => found,
            Err(SourceSkip::Error(e)) => {
                errors.push(format!("{}: {e}", cf.path));
                continue;
            }
            Err(SourceSkip::Policy) => continue,
        };
        for s in found {
            let d = dirty_symbol(cf, s, sigs);
            pass.symbols += 1;
            pass.breaks += usize::from(d.contract_break);
            let kept = if d.contract_break {
                &mut breaks
            } else {
                &mut rest
            };
            if kept.len() < bounds.symbols {
                kept.push((pass.symbols, d));
            }
        }
    }
    let room = bounds.symbols - breaks.len();
    let mut listed: Vec<(usize, DirtySymbol)> = breaks
        .into_iter()
        .chain(rest.into_iter().take(room))
        .collect();
    listed.sort_by(|(ai, a), (bi, b)| a.path.cmp(&b.path).then(ai.cmp(bi)));
    pass.listed = listed.into_iter().map(|(_, d)| d).collect();
    pass.partial = join_reasons([
        (pass.files_read < candidates.len()).then(|| {
            format!(
                "symbols were read from {} of {} changed source files (cap {})",
                pass.files_read,
                candidates.len(),
                bounds.symbol_files
            )
        }),
        (!errors.is_empty()).then(|| {
            format!(
                "{} changed source file(s) could not be read ({})",
                errors.len(),
                errors[0]
            )
        }),
    ]);
    pass
}

/// A change set over `listed` changed files: at most `bounds.listed_files` listed, the
/// rest counted, and dirty symbols from the symbol pass. `baseline` is the compared
/// baseline's signatures, or why there is none.
fn change_set(
    root: &Path,
    mut set: ChangeSet,
    listed: Result<Vec<ChangedFile>, String>,
    baseline: Result<&BTreeMap<String, String>, String>,
    bounds: ChangeBounds,
) -> ChangeSet {
    let mut changed = match listed {
        Ok(changed) => changed,
        Err(e) => return set.unlisted(&e),
    };
    let pass = symbol_pass(root, &changed, baseline.as_ref().ok().copied(), bounds);
    let total = changed.len();
    changed.truncate(bounds.listed_files);
    let cuts = [
        (total > changed.len())
            .then(|| format!("listed {} of {total} changed files", changed.len())),
        (pass.symbols > pass.listed.len()).then(|| {
            format!(
                "listed {} of {} dirty symbols, contract breaks first",
                pass.listed.len(),
                pass.symbols
            )
        }),
        pass.partial.clone(),
    ];
    if let Some(reason) = join_reasons(cuts) {
        set.truncation = Some(Truncation {
            reason,
            files_listed: changed.len(),
            files_total: total,
            symbol_files_read: pass.files_read,
            symbol_files_total: pass.files_total,
            symbols_listed: pass.listed.len(),
            symbols_total: pass.symbols,
        });
    }
    match &pass.partial {
        Some(why) => set
            .unknown
            .push(Unknown::new("dirty_symbols", format!("partial: {why}"))),
        None => set.dirty_symbols_total = Some(pass.symbols),
    }
    // a contract-break count is exact only from a complete pass against a baseline
    match baseline.err().or(pass.partial) {
        Some(why) => set.unknown.push(Unknown::new("contract_breaks", why)),
        None => set.contract_breaks = Some(pass.breaks),
    }
    set.changed_files_total = Some(total);
    set.changed_files = Some(changed);
    set.dirty_symbols = Some(pass.listed);
    set
}

/// Compute what changed since the index baseline, including dirty symbols.
pub fn changed_since_baseline(data_dir: &Path, root: &Path, workspace_id: &str) -> ChangeSet {
    changed_since_baseline_bounded(data_dir, root, workspace_id, ChangeBounds::DEFAULT)
}

/// `changed_since_baseline` within `bounds`. Without a baseline every tracked file
/// counts as added: they are listed up to the cap and not read for symbols (there is
/// nothing to diff them against), so dirty symbols and contract breaks are unknown.
pub fn changed_since_baseline_bounded(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
    bounds: ChangeBounds,
) -> ChangeSet {
    let read = read_baseline(data_dir, workspace_id);
    if let Ok(Some(baseline)) = &read {
        let listed =
            file_hash_map(root).map(|current| diff_hash_maps(&baseline.file_hashes, &current));
        let set = ChangeSet::new(workspace_id, "baseline", true);
        return change_set(root, set, listed, Ok(&baseline.signatures), bounds);
    }
    let missing = missing_baseline_reason(&read);
    let set = ChangeSet::new(workspace_id, "baseline", false);
    let paths = match tracked_files(root) {
        Ok(paths) => paths,
        Err(e) => return set.unlisted(&e),
    };
    unbaselined_changes(set, paths, &missing, bounds)
}

/// The no-baseline change set: tracked files listed as added up to the cap, none read.
fn unbaselined_changes(
    mut set: ChangeSet,
    paths: Vec<String>,
    missing: &str,
    bounds: ChangeBounds,
) -> ChangeSet {
    let total = paths.len();
    let sources = paths.iter().filter(|p| is_source(p)).count();
    let listed: Vec<ChangedFile> = paths
        .into_iter()
        .take(bounds.listed_files)
        .map(|path| ChangedFile {
            path,
            change: "added".into(),
        })
        .collect();
    set.truncation = Some(Truncation {
        reason: format!(
            "{missing}: tracked files are listed as added, at most {}, and not read for symbols",
            bounds.listed_files
        ),
        files_listed: listed.len(),
        files_total: total,
        symbol_files_read: 0,
        symbol_files_total: sources,
        symbols_listed: 0,
        symbols_total: 0,
    });
    set.unknown = ["dirty_symbols", "contract_breaks"]
        .into_iter()
        .map(|field| Unknown::new(field, format!("{missing}: nothing to diff against")))
        .collect();
    set.changed_files_total = Some(total);
    set.changed_files = Some(listed);
    set
}

/// How a porcelain status code reads as a change.
fn status_change(code: &str) -> &'static str {
    match code {
        c if c.contains('D') => "deleted",
        "??" => "added",
        c if c.contains('A') => "added",
        _ => "modified",
    }
}

/// Compute uncommitted working-tree changes (git status) + dirty symbols.
/// Loads the index baseline (via `data_dir`) so it can flag contract breaks on
/// modified files — the session-grounding view an agent sees on reconnect.
pub fn working_tree_changes(data_dir: &Path, root: &Path, workspace_id: &str) -> ChangeSet {
    working_tree_changes_bounded(data_dir, root, workspace_id, ChangeBounds::DEFAULT)
}

/// `working_tree_changes` within `bounds`. Without a baseline the dirty symbols are
/// listed but contract breaks are unknown: there are no signatures to compare.
pub fn working_tree_changes_bounded(
    data_dir: &Path,
    root: &Path,
    workspace_id: &str,
    bounds: ChangeBounds,
) -> ChangeSet {
    let listed = dirty_paths(root).map(|paths| {
        let mut changed: Vec<ChangedFile> = paths
            .into_iter()
            .map(|(code, path)| ChangedFile {
                path,
                change: status_change(&code).to_string(),
            })
            .collect();
        changed.sort_by(|a, b| a.path.cmp(&b.path));
        changed
    });
    let read = read_baseline(data_dir, workspace_id);
    let missing = missing_baseline_reason(&read);
    let baseline = read.ok().flatten();
    let sigs = baseline
        .as_ref()
        .map(|b| &b.signatures)
        .ok_or_else(|| format!("{missing}: no signatures to compare against"));
    let set = ChangeSet::new(workspace_id, "working-tree", baseline.is_some());
    change_set(root, set, listed, sigs, bounds)
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
/// Returns the events recorded this run. A worktree Git cannot list is an error, not
/// every file deleted.
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

    let current = file_hash_map(root).map_err(std::io::Error::other)?;
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
            Command::new("git")
                .arg("-C")
                .arg(dir)
                .args(&args)
                .output()
                .unwrap();
        }
    }
    fn git_commit(dir: &Path) {
        Command::new("git")
            .arg("-C")
            .arg(dir)
            .args(["add", "-A"])
            .output()
            .unwrap();
        Command::new("git")
            .arg("-C")
            .arg(dir)
            .args(["commit", "-qm", "c"])
            .output()
            .unwrap();
    }

    // Fable F4: whitespace/newline names must hash the exact file Git lists (not a
    // trimmed decoy), and a rename is one dirty path, not "old -> new".
    #[test]
    fn fingerprint_preserves_exact_paths() {
        let dir = TempDir::new().unwrap();
        git_init(dir.path());
        fs::write(dir.path().join(" lead.rs"), "REAL\n").unwrap();
        fs::write(dir.path().join("lead.rs"), "DECOY\n").unwrap();
        fs::write(dir.path().join("new\nline.rs"), "NL\n").unwrap();
        fs::write(dir.path().join("old.rs"), "OLD\n").unwrap();
        git_commit(dir.path());
        let fp = compute_fingerprint(dir.path()).unwrap();
        assert_eq!(fp.tracked_file_count, 4, "odd names dropped: {fp:?}");
        let before = fp.content_hash;
        fs::write(dir.path().join(" lead.rs"), "REAL2\n").unwrap();
        let after = compute_fingerprint(dir.path()).unwrap();
        assert_ne!(before, after.content_hash, "edit to ' lead.rs' not seen");
        Command::new("git")
            .arg("-C")
            .arg(dir.path())
            .args(["mv", "old.rs", "renamed.rs"])
            .output()
            .unwrap();
        let paths: Vec<String> = dirty_paths(dir.path())
            .unwrap()
            .into_iter()
            .map(|(_, p)| p)
            .collect();
        assert!(paths.contains(&"renamed.rs".to_string()), "{paths:?}");
        assert!(paths.contains(&" lead.rs".to_string()), "{paths:?}");
    }

    // w0-drift: a repeat fingerprint/drift over an untouched tree reads no file bytes
    // (hashes come from the stat cache in the Git dir), reports the same content hash
    // as hashing every file directly, and still sees a same-size edit.
    #[test]
    fn repeat_fingerprint_reads_no_unchanged_files() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(repo.path().join("a.rs"), "pub fn one() {}\n").unwrap();
        fs::write(repo.path().join("b.rs"), "pub fn two() {}\n").unwrap();
        git_commit(repo.path());
        let direct: BTreeMap<String, String> = ["a.rs", "b.rs"]
            .iter()
            .map(|p| {
                let h = crate::symbolgraph::hash_repo_file_beneath(repo.path(), p).unwrap();
                (p.to_string(), h)
            })
            .collect();
        // leave the racy window so the first pass records entries a later pass trusts.
        std::thread::sleep(crate::hashcache::RACY_WINDOW + std::time::Duration::from_millis(200));
        let (first, p1) = file_hash_map_counted(repo.path()).unwrap();
        assert_eq!((p1.hashed, p1.cache_written), (2, true), "{p1:?}");
        build_index_baseline(data.path(), repo.path(), "ws").unwrap();
        let (second, p2) = file_hash_map_counted(repo.path()).unwrap();
        assert_eq!(
            (p2.hashed, p2.reused),
            (0, 2),
            "unchanged files re-read: {p2:?}"
        );
        assert_eq!(first, direct);
        assert_eq!(second, direct);
        assert_eq!(
            compute_fingerprint(repo.path()).unwrap().content_hash,
            content_hash_of(&direct)
        );
        assert!(!detect_drift(data.path(), repo.path(), "ws").stale);

        fs::write(repo.path().join("a.rs"), "pub fn uno() {}\n").unwrap(); // same size
        let drift = detect_drift(data.path(), repo.path(), "ws");
        assert!(drift.content_changed, "same-size edit missed: {drift:?}");
    }

    // During a merge conflict `git ls-files` lists the unmerged path once per stage. The
    // stat cache must still settle: a repeat pass reads nothing and rewrites nothing.
    #[test]
    fn merge_conflict_duplicates_do_not_rewrite_the_hash_cache() {
        let repo = TempDir::new().unwrap();
        let git = |args: &[&str]| {
            Command::new("git")
                .arg("-C")
                .arg(repo.path())
                .args(args)
                .output()
                .unwrap()
        };
        git_init(repo.path());
        fs::write(repo.path().join("a.txt"), "base\n").unwrap();
        fs::write(repo.path().join("b.txt"), "b\n").unwrap();
        git_commit(repo.path());
        git(&["checkout", "-qb", "side"]);
        fs::write(repo.path().join("a.txt"), "side\n").unwrap();
        git_commit(repo.path());
        git(&["checkout", "-q", "-"]);
        fs::write(repo.path().join("a.txt"), "main\n").unwrap();
        git_commit(repo.path());
        git(&["merge", "side"]);
        let listed = tracked_files(repo.path()).unwrap();
        assert_eq!(
            listed.iter().filter(|p| *p == "a.txt").count(),
            3,
            "{listed:?}"
        );

        std::thread::sleep(crate::hashcache::RACY_WINDOW + std::time::Duration::from_millis(200));
        let (first, p1) = file_hash_map_counted(repo.path()).unwrap();
        assert_eq!(
            (first.len(), p1.hashed, p1.cache_written),
            (2, 2, true),
            "{p1:?}"
        );
        let (second, p2) = file_hash_map_counted(repo.path()).unwrap();
        assert_eq!(
            (p2.hashed, p2.reused, p2.cache_written),
            (0, 2, false),
            "{p2:?}"
        );
        assert_eq!(second, first);
    }

    #[test]
    fn fingerprint_is_stable_and_changes_with_content() {
        let dir = TempDir::new().unwrap();
        git_init(dir.path());
        fs::write(dir.path().join("a.rs"), "pub fn one() {}\n").unwrap();
        git_commit(dir.path());
        let fp1 = compute_fingerprint(dir.path()).unwrap();
        let fp2 = compute_fingerprint(dir.path()).unwrap();
        assert_eq!(fp1.content_hash, fp2.content_hash);
        assert!(fp1.head_sha.is_some());
        assert_eq!(fp1.tracked_file_count, 1);

        fs::write(
            dir.path().join("a.rs"),
            "pub fn one() {}\npub fn two() {}\n",
        )
        .unwrap();
        let fp3 = compute_fingerprint(dir.path()).unwrap();
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
        assert!(
            !drift0.stale,
            "fresh baseline should not be stale: {:?}",
            drift0.reasons
        );

        // modify + commit -> head + content change -> stale
        fs::write(
            repo.path().join("a.rs"),
            "pub fn one() {}\npub fn two() {}\n",
        )
        .unwrap();
        git_commit(repo.path());
        let drift1 = detect_drift(data.path(), repo.path(), "ws");
        assert!(drift1.stale && drift1.head_changed && drift1.content_changed);

        let cs = changed_since_baseline(data.path(), repo.path(), "ws");
        assert!(
            cs.changed_files
                .as_ref()
                .unwrap()
                .iter()
                .any(|c| c.path == "a.rs" && c.change == "modified")
        );
        // dirty symbols include the new function
        assert!(
            cs.dirty_symbols
                .as_ref()
                .unwrap()
                .iter()
                .any(|s| s.symbol == "two")
        );
        // adding a NEW function is not a contract break of an existing symbol.
        assert_eq!(
            cs.contract_breaks,
            Some(0),
            "a new symbol is not a break: {:?}",
            cs.dirty_symbols
        );
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
        assert_eq!(
            cs.contract_breaks,
            Some(1),
            "only `add` should break: {:?}",
            cs.dirty_symbols
        );
        let add = cs
            .dirty_symbols
            .as_ref()
            .unwrap()
            .iter()
            .find(|s| s.symbol == "add")
            .unwrap();
        assert!(add.contract_break, "add must be flagged: {add:?}");
        assert!(
            add.signature_change
                .as_deref()
                .unwrap_or("")
                .contains("params 2→3"),
            "reason should name the arity change: {:?}",
            add.signature_change
        );
        let keep = cs
            .dirty_symbols
            .as_ref()
            .unwrap()
            .iter()
            .find(|s| s.symbol == "keep")
            .unwrap();
        assert!(
            !keep.contract_break,
            "body-only edit is not a break: {keep:?}"
        );
    }

    #[test]
    fn contract_break_surfaces_in_working_tree_changes() {
        let data = TempDir::new().unwrap();
        let repo = TempDir::new().unwrap();
        git_init(repo.path());
        fs::write(
            repo.path().join("api.rs"),
            "pub fn handler(req: u32) -> bool {\n    req > 0\n}\n",
        )
        .unwrap();
        git_commit(repo.path());
        build_index_baseline(data.path(), repo.path(), "ws").unwrap();

        // change the return type WITHOUT committing → working-tree (grounding) view.
        fs::write(
            repo.path().join("api.rs"),
            "pub fn handler(req: u32) -> String {\n    req.to_string()\n}\n",
        )
        .unwrap();
        let cs = working_tree_changes(data.path(), repo.path(), "ws");
        assert_eq!(
            cs.contract_breaks,
            Some(1),
            "return-type change should break: {:?}",
            cs.dirty_symbols
        );
        let h = cs
            .dirty_symbols
            .as_ref()
            .unwrap()
            .iter()
            .find(|s| s.symbol == "handler")
            .unwrap();
        assert!(
            h.signature_change
                .as_deref()
                .unwrap_or("")
                .contains("return"),
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
        let by: BTreeMap<_, _> = diff
            .iter()
            .map(|c| (c.path.as_str(), c.change.as_str()))
            .collect();
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
        fs::write(
            repo.path().join("a.rs"),
            "pub fn one() {}\npub fn two() {}\n",
        )
        .unwrap();
        git_commit(repo.path());
        let e3 = record_incorporation(data.path(), repo.path(), "ws").unwrap();
        assert!(e3.iter().any(|e| e.path == "a.rs" && e.event == "changed"));

        let lineage = file_lineage(data.path(), "ws", "a.rs");
        assert_eq!(lineage.events.len(), 2); // indexed + changed
        assert_eq!(lineage.change_count, 1);
    }
}
