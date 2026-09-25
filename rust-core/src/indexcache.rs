//! Repository source identity and the shared symbol-graph cache.
//!
//! `source_identity` is the one content identity for a working tree: canonical root +
//! `HEAD` + every `git status --porcelain=v1 -z --untracked-files=all` entry (status,
//! byte-exact path, rename origin) + the full SHA-256 of each dirty or untracked
//! file's bytes, read through the bounded no-follow reader. Files that cannot be hashed
//! in full (oversized, unreadable, not regular, over the identity byte budget) add a
//! metadata token and an explicit limitation, so `identity_complete` is false: a
//! bounded read never claims identity for bytes it did not read. The same key drives
//! graph-cache invalidation and `xmustard-core repo-key`.
//!
//! Caches live in `<git-dir>/xmustard-cache/index-v2/<scope>/`, where the scope hashes
//! the canonical repository root, the caller's trust scope and the parser version.
//! Workspace IDs never select a cache: agents in the same repo and trust scope share
//! one snapshot. Builders for a scope are serialized across processes by an advisory
//! lock with a bounded wait; atomic-write temp files left by crashed writers are swept.

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde::Serialize;
use sha2::{Digest, Sha256};

use crate::symbolgraph::{
    MAX_REPO_FILE_BYTES, SourceReadFailure, SymbolGraph, read_source_beneath,
};

/// Bump when the cached graph/feature layout or extraction semantics change.
pub const INDEX_FORMAT_VERSION: &str = "xm-index-2";

/// Total bytes `source_identity` hashes per call. Tracked dirty files are hashed first;
/// entries past the budget become `identity_budget` limitations.
pub const MAX_IDENTITY_BYTES: u64 = 64 << 20;

/// Default bound on waiting for another process's build of the same scope.
pub const DEFAULT_LOCK_TIMEOUT_MS: u64 = 60_000;

/// Temp files older than this are crash leftovers (no write takes this long).
const STALE_TEMP_AGE: Duration = Duration::from_secs(600);

/// Graph snapshots kept per scope (a few, so toggling between states stays warm).
const GRAPH_SNAPSHOTS_KEPT: usize = 3;

/// Parser identity: the index format, tree-sitter ABI and grammar crate versions, and
/// the regex-fallback revision. Part of the cache scope, reported by `repo-key`.
pub fn parser_version() -> String {
    format!(
        "{INDEX_FORMAT_VERSION};ts-abi-{};rust-0.24;go-0.25;typescript-0.23;javascript-0.25;regex-2",
        tree_sitter::LANGUAGE_VERSION
    )
}

fn sha_hex(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))
}

/// Atomically replace `path` with `bytes`: write a uniquely named temp file in the
/// same directory, fsync it, then rename over the target. Readers see the old or the
/// new bytes, never a torn file; a failed write leaves the old file intact.
fn atomic_write(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    atomic_write_with(path, |w| w.write_all(bytes))
}

/// `atomic_write` with the content produced by `fill` straight into a buffered temp
/// file, so large snapshots are streamed instead of first built as one byte vector.
fn atomic_write_with(
    path: &Path,
    fill: impl FnOnce(&mut dyn Write) -> std::io::Result<()>,
) -> std::io::Result<()> {
    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    let base = path.file_name().and_then(|s| s.to_str()).unwrap_or("cache");
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let tmp = dir.join(format!(".{base}.tmp.{}.{nanos}", std::process::id()));
    let write_result = (|| -> std::io::Result<()> {
        let mut w = std::io::BufWriter::new(fs::File::create(&tmp)?);
        fill(&mut w)?;
        let f = w.into_inner().map_err(|e| e.into_error())?;
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

/// Remove atomic-write temp files (`.<name>.tmp.*`) older than `max_age` from `dir`.
/// Returns how many were removed.
pub fn sweep_stale_temps(dir: &Path, max_age: Duration) -> usize {
    let Ok(entries) = fs::read_dir(dir) else {
        return 0;
    };
    let now = SystemTime::now();
    let mut removed = 0;
    for entry in entries.flatten() {
        let name = entry.file_name().to_string_lossy().into_owned();
        if !(name.starts_with('.') && name.contains(".tmp.")) {
            continue;
        }
        let Ok(meta) = entry.metadata() else { continue };
        let old = meta
            .modified()
            .ok()
            .and_then(|m| now.duration_since(m).ok())
            .is_some_and(|age| age >= max_age);
        if meta.is_file() && old && fs::remove_file(entry.path()).is_ok() {
            removed += 1;
        }
    }
    removed
}

/// Stdout cap for index/identity Git commands (`status -z`, `ls-files -s -z`).
pub const MAX_GIT_OUTPUT_BYTES: usize = 32 << 20;

/// Why a bounded Git command produced no usable output.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum GitRunError {
    Spawn(String),
    /// Nonzero exit.
    Failed,
    /// Stdout exceeded the cap; the child was killed and the output discarded.
    Overflow(usize),
    TimedOut(Duration),
}

impl std::fmt::Display for GitRunError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GitRunError::Spawn(e) => write!(f, "git could not start: {e}"),
            GitRunError::Failed => write!(f, "git exited nonzero"),
            GitRunError::Overflow(cap) => write!(f, "git output exceeded {cap} bytes"),
            GitRunError::TimedOut(t) => write!(f, "git timed out after {} ms", t.as_millis()),
        }
    }
}

/// Git command timeout: `XMUSTARD_GIT_TIMEOUT_MS`, default 30 s.
pub fn git_timeout() -> Duration {
    Duration::from_millis(
        std::env::var("XMUSTARD_GIT_TIMEOUT_MS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(30_000),
    )
}

/// Run `git --no-optional-locks -C root <args>` with stdout streamed into a buffer of
/// at most `max_bytes` and a wall-clock timeout. Overflow or timeout kills the child
/// and is an error, never a truncated result. The child stays in this process's
/// process group, so a caller that kills the group (the Go bridge does, on
/// cancellation) also ends it; stderr is discarded.
pub fn run_git_bounded(
    root: &Path,
    args: &[&str],
    max_bytes: usize,
    timeout: Duration,
) -> Result<Vec<u8>, GitRunError> {
    use std::io::Read;
    use std::process::Stdio;
    let mut child = Command::new("git")
        .arg("--no-optional-locks")
        .arg("-C")
        .arg(root)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| GitRunError::Spawn(e.to_string()))?;
    let mut stdout = child.stdout.take().expect("stdout is piped");
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let mut buf = Vec::new();
        let mut chunk = [0u8; 64 * 1024];
        let result = loop {
            match stdout.read(&mut chunk) {
                Ok(0) => break Ok(buf),
                Ok(n) if buf.len() + n > max_bytes => break Err(()),
                Ok(n) => buf.extend_from_slice(&chunk[..n]),
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
                Err(_) => break Ok(buf),
            }
        };
        let _ = tx.send(result);
    });
    let outcome = rx.recv_timeout(timeout);
    let result = match outcome {
        Ok(Ok(bytes)) => match child.wait() {
            Ok(status) if status.success() => Ok(bytes),
            _ => Err(GitRunError::Failed),
        },
        Ok(Err(())) => Err(GitRunError::Overflow(max_bytes)),
        Err(_) => Err(GitRunError::TimedOut(timeout)),
    };
    if result.is_err() {
        let _ = child.kill();
        let _ = child.wait();
    }
    result
}

fn git_output(root: &Path, args: &[&str]) -> Option<Vec<u8>> {
    run_git_bounded(root, args, 64 << 10, git_timeout()).ok()
}

/// One `git status --porcelain=v1 -z` record. `path` and `orig_path` are the exact
/// bytes Git printed (relative to the repository top level), never trimmed or split.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusEntry {
    pub xy: [u8; 2],
    pub path: Vec<u8>,
    /// Rename/copy source, present when X or Y is `R` or `C`.
    pub orig_path: Option<Vec<u8>>,
}

impl StatusEntry {
    pub fn is_untracked(&self) -> bool {
        self.xy == *b"??"
    }
}

/// Parse `git status --porcelain=v1 -z` output: `XY SP path NUL`, followed by
/// `orig NUL` for renames and copies. Malformed records are skipped.
pub fn parse_porcelain_v1_z(out: &[u8]) -> Vec<StatusEntry> {
    let mut records = out.split(|b| *b == 0);
    let mut entries = Vec::new();
    while let Some(rec) = records.next() {
        if rec.len() < 4 || rec[2] != b' ' {
            continue;
        }
        let xy = [rec[0], rec[1]];
        let orig_path = if matches!(xy[0], b'R' | b'C') || matches!(xy[1], b'R' | b'C') {
            records.next().map(<[u8]>::to_vec)
        } else {
            None
        };
        entries.push(StatusEntry {
            xy,
            path: rec[3..].to_vec(),
            orig_path,
        });
    }
    entries
}

#[cfg(unix)]
pub(crate) fn path_from_bytes(bytes: &[u8]) -> PathBuf {
    use std::os::unix::ffi::OsStrExt;
    PathBuf::from(std::ffi::OsStr::from_bytes(bytes))
}

#[cfg(not(unix))]
pub(crate) fn path_from_bytes(bytes: &[u8]) -> PathBuf {
    PathBuf::from(String::from_utf8_lossy(bytes).into_owned())
}

/// Where Git keeps this checkout and how `root` sits inside it.
#[derive(Debug, Clone)]
pub struct GitLayout {
    pub toplevel: PathBuf,
    pub git_dir: PathBuf,
    /// `root` relative to the top level, with a trailing `/`, or empty at the top.
    pub prefix: String,
}

fn git_layout(root: &Path) -> Option<GitLayout> {
    let out = git_output(
        root,
        &[
            "rev-parse",
            "--show-toplevel",
            "--absolute-git-dir",
            "--show-prefix",
        ],
    )?;
    let text = String::from_utf8(out).ok()?;
    let mut lines = text.split('\n');
    let toplevel = PathBuf::from(lines.next()?);
    let git_dir = PathBuf::from(lines.next()?);
    let prefix = lines.next().unwrap_or("").to_string();
    Some(GitLayout {
        toplevel,
        git_dir,
        prefix,
    })
}

/// A file named in `git status` whose bytes could not be hashed in full.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct IdentityLimitation {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    /// `oversized` | `unreadable` | `not_regular` | `identity_budget` |
    /// `git_unavailable` | `git_status_failed` | `git_index_flags_failed`
    pub reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
}

/// What `source_identity` knows about one dirty or untracked path's bytes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DirtyContent {
    /// Full SHA-256 of every byte (complete identity).
    Sha256(String),
    /// Absent from the worktree (complete: absence is the content).
    Deleted,
    /// A symlink; the hash of its target string (complete; never followed).
    Symlink(String),
    /// Not hashed in full; only a metadata token entered the key.
    Incomplete(SourceReadFailure),
    /// Skipped because the identity byte budget was spent.
    Unhashed,
}

/// The repository source identity. Serialized as the `repo-key` JSON contract.
#[derive(Debug, Clone, Serialize)]
pub struct SourceIdentity {
    /// SHA-256 hex over root, HEAD, status entries and dirty/untracked content tokens.
    pub key: String,
    /// `HEAD` commit, or "" when unborn or Git is unavailable.
    pub head: String,
    pub parser_version: String,
    /// True only when every status entry's bytes were hashed in full.
    pub identity_complete: bool,
    pub limitations: Vec<IdentityLimitation>,
    /// `git` | `git-unavailable`
    pub repo_mode: String,
    /// Canonical absolute repository root the key is scoped to.
    pub root: String,
    pub dirty_entries: usize,
    pub untracked_entries: usize,
    /// Assume-unchanged / skip-worktree index entries hashed directly because
    /// `git status` does not report their worktree edits.
    pub hidden_entries: usize,
    pub files_hashed: usize,
    pub bytes_hashed: u64,
    #[serde(skip)]
    pub dirty: BTreeMap<Vec<u8>, DirtyContent>,
    #[serde(skip)]
    pub layout: Option<GitLayout>,
    /// Tracked (non-untracked) status entries skipped by the identity budget. Untracked
    /// entries are never read by the graph, so only these block graph caching.
    #[serde(skip)]
    pub tracked_unhashed: Vec<String>,
}

impl SourceIdentity {
    /// Whether the graph for this identity may be served from, and stored to, the
    /// cache: see `graph_bypass_reason`.
    pub fn graph_cacheable(&self) -> bool {
        self.graph_bypass_reason().is_none()
    }

    /// Why this identity does not determine the graph's input, or None when it does:
    /// Git mode, status and the hidden-entry listing succeeded, and every *tracked*
    /// dirty file has a known identity (hashed, deleted, symlink, or excluded from the
    /// graph by size with its size/ctime in the key). Untracked files never feed the
    /// graph, so an untracked-only limitation leaves the graph cacheable while
    /// `identity_complete` stays false.
    pub fn graph_bypass_reason(&self) -> Option<String> {
        if let Some(l) = self.limitations.iter().find(|l| {
            matches!(
                l.reason.as_str(),
                "git_status_failed" | "git_unavailable" | "git_index_flags_failed"
            )
        }) {
            return Some(format!("identity limitation {}: {}", l.reason, l.detail));
        }
        if self.layout.is_none() {
            return Some("identity limitation git_unavailable".into());
        }
        if let Some(first) = self.tracked_unhashed.first() {
            return Some(format!(
                "identity_budget: {} tracked dirty file(s) not hashed (first: {first})",
                self.tracked_unhashed.len()
            ));
        }
        None
    }
}

#[cfg(unix)]
fn change_token(meta: &fs::Metadata) -> String {
    use std::os::unix::fs::MetadataExt;
    format!(
        "{}:{}.{}:{}.{}",
        meta.len(),
        meta.mtime(),
        meta.mtime_nsec(),
        meta.ctime(),
        meta.ctime_nsec()
    )
}

#[cfg(not(unix))]
fn change_token(meta: &fs::Metadata) -> String {
    let m = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    format!("{}:{m}", meta.len())
}

fn push_field(h: &mut Sha256, bytes: &[u8]) {
    h.update((bytes.len() as u64).to_le_bytes());
    h.update(bytes);
}

/// Compute the source identity of the working tree at `root` (see module docs).
pub fn source_identity(root: &Path) -> SourceIdentity {
    let canonical = fs::canonicalize(root).unwrap_or_else(|_| root.to_path_buf());
    let root_str = canonical.to_string_lossy().into_owned();
    let mut id = SourceIdentity {
        key: String::new(),
        head: String::new(),
        parser_version: parser_version(),
        identity_complete: true,
        limitations: Vec::new(),
        repo_mode: "git".into(),
        root: root_str.clone(),
        dirty_entries: 0,
        untracked_entries: 0,
        hidden_entries: 0,
        files_hashed: 0,
        bytes_hashed: 0,
        dirty: BTreeMap::new(),
        layout: None,
        tracked_unhashed: Vec::new(),
    };
    let mut h = Sha256::new();
    h.update(b"xm-source-key-v1\0");
    push_field(&mut h, root_str.as_bytes());

    let Some(layout) = git_layout(root) else {
        id.repo_mode = "git-unavailable".into();
        id.identity_complete = false;
        id.limitations.push(IdentityLimitation {
            path: None,
            reason: "git_unavailable".into(),
            detail: "not a Git worktree (or git is missing/unsafe); content is not identified"
                .into(),
        });
        h.update(b"git-unavailable");
        id.key = format!("{:x}", h.finalize());
        return id;
    };
    id.head = git_output(root, &["rev-parse", "-q", "--verify", "HEAD^{commit}"])
        .map(|b| String::from_utf8_lossy(&b).trim().to_string())
        .unwrap_or_default();
    push_field(&mut h, id.head.as_bytes());

    let status = match run_git_bounded(
        root,
        // explicit flags override config that would hide working state
        // (`status.showUntrackedFiles`, `submodule.<name>.ignore`, `diff.ignoreSubmodules`).
        &[
            "status",
            "--porcelain=v1",
            "-z",
            "--untracked-files=all",
            "--ignore-submodules=none",
        ],
        MAX_GIT_OUTPUT_BYTES,
        git_timeout(),
    ) {
        Ok(status) => status,
        Err(e) => {
            id.identity_complete = false;
            id.limitations.push(IdentityLimitation {
                path: None,
                reason: "git_status_failed".into(),
                detail: format!("{e}; dirty content is not identified"),
            });
            h.update(b"status-failed");
            id.key = format!("{:x}", h.finalize());
            id.layout = Some(layout);
            return id;
        }
    };
    let mut entries = parse_porcelain_v1_z(&status);
    // `git status` hides worktree edits to assume-unchanged / skip-worktree entries;
    // identify their bytes directly so no known working-state content is omitted.
    match hidden_index_entries(&layout.toplevel) {
        Ok(hidden) => {
            let listed: std::collections::HashSet<Vec<u8>> =
                entries.iter().map(|e| e.path.clone()).collect();
            entries.extend(hidden.into_iter().filter(|e| !listed.contains(&e.path)));
        }
        Err(e) => id.limitations.push(IdentityLimitation {
            path: None,
            reason: "git_index_flags_failed".into(),
            detail: format!("{e}; assume-unchanged/skip-worktree entries may hide worktree edits"),
        }),
    }
    // tracked changes claim the byte budget before untracked files.
    entries.sort_by(|a, b| {
        a.is_untracked()
            .cmp(&b.is_untracked())
            .then_with(|| a.path.cmp(&b.path))
    });
    let mut budget = MAX_IDENTITY_BYTES;
    for e in &entries {
        if e.is_untracked() {
            id.untracked_entries += 1;
        } else if e.xy[0] == b'!' {
            id.hidden_entries += 1;
        } else {
            id.dirty_entries += 1;
        }
        let rel = path_from_bytes(&e.path);
        let content = if budget == 0 {
            DirtyContent::Unhashed
        } else {
            match read_source_beneath(&layout.toplevel, &rel, MAX_REPO_FILE_BYTES.min(budget)) {
                Ok(bytes) => {
                    budget -= bytes.len() as u64;
                    id.files_hashed += 1;
                    id.bytes_hashed += bytes.len() as u64;
                    DirtyContent::Sha256(sha_hex(&bytes))
                }
                Err(SourceReadFailure::Missing) => DirtyContent::Deleted,
                Err(SourceReadFailure::Symlink) => {
                    match fs::read_link(layout.toplevel.join(&rel)) {
                        Ok(target) => {
                            DirtyContent::Symlink(sha_hex(target.as_os_str().as_encoded_bytes()))
                        }
                        Err(e) => {
                            DirtyContent::Incomplete(SourceReadFailure::Unreadable(e.to_string()))
                        }
                    }
                }
                Err(SourceReadFailure::Oversized(n)) if n <= MAX_REPO_FILE_BYTES => {
                    // fits the per-file cap but not the remaining budget.
                    DirtyContent::Unhashed
                }
                Err(f) => DirtyContent::Incomplete(f),
            }
        };
        h.update(e.xy);
        push_field(&mut h, &e.path);
        push_field(&mut h, e.orig_path.as_deref().unwrap_or(b"\0none"));
        let token = match &content {
            DirtyContent::Sha256(x) => format!("sha256:{x}"),
            DirtyContent::Deleted => "deleted".into(),
            DirtyContent::Symlink(x) => format!("symlink:{x}"),
            DirtyContent::Incomplete(_) | DirtyContent::Unhashed => {
                // metadata only: changes on most edits, but never claimed as identity.
                let what = match &content {
                    DirtyContent::Incomplete(f) => f.reason(),
                    _ => "identity_budget",
                };
                match fs::symlink_metadata(layout.toplevel.join(&rel)) {
                    Ok(m) => format!("{what}:{}", change_token(&m)),
                    Err(_) => format!("{what}:no-metadata"),
                }
            }
        };
        push_field(&mut h, token.as_bytes());
        match &content {
            DirtyContent::Incomplete(f) => id.limitations.push(IdentityLimitation {
                path: Some(String::from_utf8_lossy(&e.path).into_owned()),
                reason: f.reason().into(),
                detail: f.detail(),
            }),
            DirtyContent::Unhashed => {
                let path = String::from_utf8_lossy(&e.path).into_owned();
                if !e.is_untracked() {
                    id.tracked_unhashed.push(path.clone());
                }
                id.limitations.push(IdentityLimitation {
                    path: Some(path),
                    reason: "identity_budget".into(),
                    detail: format!(
                        "identity byte budget ({MAX_IDENTITY_BYTES} bytes per call) spent; not hashed"
                    ),
                })
            }
            _ => {}
        }
        id.dirty.insert(e.path.clone(), content);
    }
    id.identity_complete = id.limitations.is_empty();
    id.key = format!("{:x}", h.finalize());
    id.layout = Some(layout);
    id
}

/// The source-identity key (kept for callers of the former size/mtime key).
pub fn cheap_key(root: &Path) -> String {
    source_identity(root).key
}

/// Index entries whose worktree state `git status` does not report: assume-unchanged
/// (`git ls-files -v` tag in lower case) and skip-worktree (`S`/`s`). Returned as
/// status entries with `xy = ['!', tag]`, a marker no porcelain status uses, so the
/// key distinguishes them. `toplevel` must be the worktree top (paths are relative).
fn hidden_index_entries(toplevel: &Path) -> Result<Vec<StatusEntry>, GitRunError> {
    let out = run_git_bounded(
        toplevel,
        &["ls-files", "-v", "-z"],
        MAX_GIT_OUTPUT_BYTES,
        git_timeout(),
    )?;
    let mut hidden: Vec<StatusEntry> = Vec::new();
    for rec in out.split(|b| *b == 0) {
        if rec.len() < 3 || rec[1] != b' ' {
            continue;
        }
        let tag = rec[0];
        if !(tag.is_ascii_lowercase() || tag == b'S') {
            continue;
        }
        // unmerged entries repeat a path; keep one.
        if hidden.last().is_some_and(|e| e.path == rec[2..]) {
            continue;
        }
        hidden.push(StatusEntry {
            xy: [b'!', tag],
            path: rec[2..].to_vec(),
            orig_path: None,
        });
    }
    Ok(hidden)
}

/// sha256 of a tracked file's current content through the bounded no-follow opener.
pub fn file_hash(root: &Path, rel: &str) -> Option<String> {
    crate::symbolgraph::hash_repo_file_beneath(root, rel)
}

/// The trust scope a cache is shared within. Set `XMUSTARD_INDEX_TRUST_SCOPE` to the
/// authorization scope the caller has resolved; the default is `local`.
pub fn trust_scope() -> String {
    std::env::var("XMUSTARD_INDEX_TRUST_SCOPE")
        .ok()
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "local".into())
}

/// A repo/trust/parser-scoped cache directory.
#[derive(Debug, Clone)]
pub struct CacheScope {
    pub dir: PathBuf,
    /// The pre-v2 per-workspace cache directory, swept of stale temps and legacy files.
    legacy_dir: PathBuf,
}

/// The cache scope for `id`, or None outside Git.
pub fn cache_scope(id: &SourceIdentity) -> Option<CacheScope> {
    let layout = id.layout.as_ref()?;
    let mut h = Sha256::new();
    push_field(&mut h, id.root.as_bytes());
    push_field(&mut h, trust_scope().as_bytes());
    push_field(&mut h, id.parser_version.as_bytes());
    let scope = format!("{:x}", h.finalize());
    let base = layout.git_dir.join("xmustard-cache");
    Some(CacheScope {
        dir: base.join("index-v2").join(&scope[..32]),
        legacy_dir: base,
    })
}

impl CacheScope {
    fn graph_file(&self, key: &str) -> PathBuf {
        self.dir.join(format!("graph-{key}.json"))
    }

    pub fn features_file(&self) -> PathBuf {
        self.dir.join("files.json")
    }

    /// Load the graph snapshot stored for `key`.
    pub fn load_graph(&self, key: &str) -> Option<SymbolGraph> {
        load_json(&self.graph_file(key))
    }

    /// Store a graph snapshot for `key` (best effort), keep the newest few, and sweep
    /// stale temps and legacy per-workspace files. Returns stale temps removed.
    pub fn store_graph(&self, key: &str, graph: &SymbolGraph) -> usize {
        if fs::create_dir_all(&self.dir).is_err() {
            return 0;
        }
        let _ = store_json(&self.graph_file(key), graph);
        self.prune_graphs();
        self.sweep()
    }

    /// Load the per-file feature cache (streamed; None if absent or unreadable).
    pub fn load_features<T: serde::de::DeserializeOwned>(&self) -> Option<T> {
        load_json(&self.features_file())
    }

    /// Store the per-file feature cache, streamed (best effort). Returns stale temps
    /// removed.
    pub fn store_features<T: Serialize>(&self, features: &T) -> usize {
        if fs::create_dir_all(&self.dir).is_err() {
            return 0;
        }
        let _ = store_json(&self.features_file(), features);
        self.sweep()
    }

    fn sweep(&self) -> usize {
        let mut removed = sweep_stale_temps(&self.dir, STALE_TEMP_AGE);
        removed += sweep_stale_temps(&self.legacy_dir, STALE_TEMP_AGE);
        // pre-v2 per-workspace graph/symbol caches are never read again.
        if let Ok(entries) = fs::read_dir(&self.legacy_dir) {
            for entry in entries.flatten() {
                let name = entry.file_name().to_string_lossy().into_owned();
                if (name.starts_with("symbolgraph-") || name.starts_with("symbols-"))
                    && name.ends_with(".json")
                {
                    let _ = fs::remove_file(entry.path());
                }
            }
        }
        removed
    }

    fn prune_graphs(&self) {
        let Ok(entries) = fs::read_dir(&self.dir) else {
            return;
        };
        let mut graphs: Vec<(SystemTime, PathBuf)> = entries
            .flatten()
            .filter(|e| {
                let n = e.file_name().to_string_lossy().into_owned();
                n.starts_with("graph-") && n.ends_with(".json")
            })
            .filter_map(|e| Some((e.metadata().ok()?.modified().ok()?, e.path())))
            .collect();
        graphs.sort_by(|a, b| b.0.cmp(&a.0));
        for (_, path) in graphs.into_iter().skip(GRAPH_SNAPSHOTS_KEPT) {
            let _ = fs::remove_file(path);
        }
    }

    /// Acquire the scope's cross-process build lock, waiting at most `timeout`.
    /// The lock is an OS advisory lock, released when the holder exits or drops it,
    /// so a crashed builder never leaves a stale lock.
    pub fn lock_build(&self, timeout: Duration) -> (Option<BuildLock>, LockOutcome) {
        let start = Instant::now();
        if let Err(e) = fs::create_dir_all(&self.dir) {
            return (None, LockOutcome::Unavailable(e.to_string()));
        }
        let file = match fs::OpenOptions::new()
            .create(true)
            .truncate(false)
            .write(true)
            .open(self.dir.join("build.lock"))
        {
            Ok(f) => f,
            Err(e) => return (None, LockOutcome::Unavailable(e.to_string())),
        };
        let mut contended = false;
        loop {
            match file.try_lock() {
                Ok(()) => {
                    let waited_ms = start.elapsed().as_millis() as u64;
                    return (
                        Some(BuildLock { _file: file }),
                        LockOutcome::Acquired {
                            waited_ms,
                            contended,
                        },
                    );
                }
                Err(fs::TryLockError::WouldBlock) => {
                    contended = true;
                    if start.elapsed() >= timeout {
                        return (
                            None,
                            LockOutcome::TimedOut {
                                waited_ms: start.elapsed().as_millis() as u64,
                            },
                        );
                    }
                    std::thread::sleep(Duration::from_millis(20));
                }
                Err(fs::TryLockError::Error(e)) => {
                    return (None, LockOutcome::Unavailable(e.to_string()));
                }
            }
        }
    }
}

/// Stream a JSON value from `path` (no whole-file buffer).
fn load_json<T: serde::de::DeserializeOwned>(path: &Path) -> Option<T> {
    let f = fs::File::open(path).ok()?;
    serde_json::from_reader(std::io::BufReader::new(f)).ok()
}

/// Stream a JSON value into `path` atomically.
fn store_json<T: Serialize>(path: &Path, value: &T) -> std::io::Result<()> {
    atomic_write_with(path, |w| {
        serde_json::to_writer(w, value).map_err(std::io::Error::from)
    })
}

/// Lock wait bound: `XMUSTARD_INDEX_LOCK_TIMEOUT_MS`, default 60 s.
pub fn lock_timeout() -> Duration {
    Duration::from_millis(
        std::env::var("XMUSTARD_INDEX_LOCK_TIMEOUT_MS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(DEFAULT_LOCK_TIMEOUT_MS),
    )
}

/// A held build lock; dropping it (or process exit) releases the lock.
pub struct BuildLock {
    _file: fs::File,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LockOutcome {
    /// `contended`: another holder was observed, so this builder actually waited.
    Acquired {
        waited_ms: u64,
        contended: bool,
    },
    TimedOut {
        waited_ms: u64,
    },
    Unavailable(String),
}

/// Path to the per-workspace wiki page cache (subsystem slug → fingerprinted page).
pub fn wiki_cache_path(root: &Path, workspace_id: &str) -> PathBuf {
    legacy_cache_dir(root).join(format!("wiki-{workspace_id}.json"))
}

/// Load the raw wiki-cache bytes (caller deserializes), or None if absent.
pub fn load_wiki_cache_bytes(root: &Path, workspace_id: &str) -> Option<Vec<u8>> {
    fs::read(wiki_cache_path(root, workspace_id)).ok()
}

/// Persist the wiki-cache bytes (best-effort).
pub fn store_wiki_cache_bytes(root: &Path, workspace_id: &str, bytes: &[u8]) {
    let dir = legacy_cache_dir(root);
    if fs::create_dir_all(&dir).is_ok() {
        let _ = atomic_write(&wiki_cache_path(root, workspace_id), bytes);
    }
}

fn legacy_cache_dir(root: &Path) -> PathBuf {
    let git_dir = root.join(".git");
    if git_dir.is_dir() {
        return git_dir.join("xmustard-cache");
    }
    std::env::temp_dir().join(format!(
        "xmustard-index-{}",
        sha_hex(root.display().to_string().as_bytes())
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn git(root: &Path, args: &[&str]) {
        let out = Command::new("git")
            .arg("-C")
            .arg(root)
            .args(args)
            .output()
            .unwrap();
        assert!(out.status.success(), "git {args:?}");
    }

    fn repo_with(files: &[(&str, &[u8])]) -> tempfile::TempDir {
        let dir = tempfile::TempDir::new().unwrap();
        for (rel, content) in files {
            fs::write(dir.path().join(rel), content).unwrap();
        }
        git(dir.path(), &["init", "-q"]);
        git(dir.path(), &["config", "user.email", "t@t"]);
        git(dir.path(), &["config", "user.name", "t"]);
        git(dir.path(), &["add", "-A"]);
        git(dir.path(), &["commit", "-qm", "c"]);
        dir
    }

    #[test]
    fn atomic_write_replaces_and_leaves_no_temp() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("c.json");
        atomic_write(&target, b"first").unwrap();
        assert_eq!(fs::read(&target).unwrap(), b"first");
        atomic_write(&target, b"second-longer").unwrap();
        assert_eq!(fs::read(&target).unwrap(), b"second-longer");
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
    fn sweep_removes_only_old_temps() {
        let dir = tempfile::TempDir::new().unwrap();
        let old = dir.path().join(".graph-x.json.tmp.1.2");
        let fresh = dir.path().join(".graph-y.json.tmp.3.4");
        let keep = dir.path().join("graph-z.json");
        for p in [&old, &fresh, &keep] {
            fs::write(p, b"x").unwrap();
        }
        fs::File::options()
            .write(true)
            .open(&old)
            .unwrap()
            .set_modified(SystemTime::now() - Duration::from_secs(3600))
            .unwrap();
        assert_eq!(sweep_stale_temps(dir.path(), STALE_TEMP_AGE), 1);
        assert!(!old.exists() && fresh.exists() && keep.exists());
    }

    #[test]
    fn porcelain_z_parses_untrimmed_renames_and_odd_names() {
        let raw = b" M  lead.rs\0R  new name.rs\0old\nname.rs\0?? tab\there.rs\0 D gone.rs\0";
        let e = parse_porcelain_v1_z(raw);
        assert_eq!(e.len(), 4);
        assert_eq!(e[0].xy, *b" M");
        assert_eq!(e[0].path, b" lead.rs");
        assert_eq!(e[1].path, b"new name.rs");
        assert_eq!(e[1].orig_path.as_deref(), Some(&b"old\nname.rs"[..]));
        assert!(e[2].is_untracked());
        assert_eq!(e[2].path, b"tab\there.rs");
        assert_eq!(e[3].xy, *b" D");
    }

    #[test]
    fn identity_changes_on_dirty_to_dirty_and_untracked_edits() {
        let r = repo_with(&[("a.rs", b"fn a() {}\n")]);
        let clean = source_identity(r.path());
        assert!(clean.identity_complete && !clean.head.is_empty());
        assert_eq!(clean.dirty_entries, 0);
        fs::write(r.path().join("a.rs"), b"fn b() {}\n").unwrap();
        let d1 = source_identity(r.path());
        fs::write(r.path().join("a.rs"), b"fn c() {}\n").unwrap();
        let d2 = source_identity(r.path());
        assert_ne!(clean.key, d1.key);
        assert_ne!(
            d1.key, d2.key,
            "same-size dirty->dirty edit must change the key"
        );
        fs::write(r.path().join("u.txt"), b"one").unwrap();
        let u1 = source_identity(r.path());
        fs::write(r.path().join("u.txt"), b"two").unwrap();
        let u2 = source_identity(r.path());
        assert_ne!(d2.key, u1.key);
        assert_ne!(u1.key, u2.key, "untracked bytes are part of the identity");
        assert_eq!((u2.dirty_entries, u2.untracked_entries), (1, 1));
        assert!(u2.identity_complete);
        // reverting restores the original identity exactly.
        fs::remove_file(r.path().join("u.txt")).unwrap();
        fs::write(r.path().join("a.rs"), b"fn a() {}\n").unwrap();
        assert_eq!(source_identity(r.path()).key, clean.key);
    }

    #[test]
    fn oversized_dirty_file_is_an_explicit_limitation_not_a_prefix_hash() {
        let r = repo_with(&[("big.bin", b"small\n")]);
        let mut big = vec![b'a'; (MAX_REPO_FILE_BYTES as usize) + 10];
        fs::write(r.path().join("big.bin"), &big).unwrap();
        let k1 = source_identity(r.path());
        assert!(!k1.identity_complete);
        let lim = &k1.limitations[0];
        assert_eq!(lim.reason, "oversized");
        assert_eq!(lim.path.as_deref(), Some("big.bin"));
        assert_eq!(
            k1.bytes_hashed, 0,
            "no bytes of an over-cap file are hashed"
        );
        // a size-changing edit still changes the key through the metadata token.
        big.push(b'b');
        fs::write(r.path().join("big.bin"), &big).unwrap();
        assert_ne!(source_identity(r.path()).key, k1.key);
    }

    // A submodule configured `ignore = all` is hidden from plain `git status`; its dirty
    // worktree must still surface (as an explicit `not_regular` limitation).
    #[test]
    fn ignored_dirty_submodule_is_not_complete() {
        let inner = repo_with(&[("lib.rs", b"fn inner() {}\n")]);
        let outer = repo_with(&[("a.rs", b"fn a() {}\n")]);
        let url = inner.path().to_str().unwrap();
        git(
            outer.path(),
            &[
                "-c",
                "protocol.file.allow=always",
                "submodule",
                "add",
                "-q",
                url,
                "sub",
            ],
        );
        git(outer.path(), &["commit", "-qm", "sub"]);
        git(outer.path(), &["config", "submodule.sub.ignore", "all"]);
        assert!(source_identity(outer.path()).identity_complete);
        fs::write(outer.path().join("sub/lib.rs"), b"fn inner_edited() {}\n").unwrap();
        let id = source_identity(outer.path());
        assert!(
            !id.identity_complete,
            "ignored dirty submodule reported complete"
        );
        assert!(
            id.limitations
                .iter()
                .any(|l| l.path.as_deref() == Some("sub"))
        );
    }

    #[test]
    fn non_git_root_is_incomplete() {
        let dir = tempfile::TempDir::new().unwrap();
        let id = source_identity(dir.path());
        assert_eq!(id.repo_mode, "git-unavailable");
        assert!(!id.identity_complete);
        assert_eq!(id.limitations[0].reason, "git_unavailable");
        assert!(!id.graph_cacheable());
    }

    #[test]
    fn scope_separates_roots_and_lock_is_exclusive() {
        let a = repo_with(&[("a.rs", b"fn a() {}\n")]);
        let b = repo_with(&[("a.rs", b"fn a() {}\n")]);
        let sa = cache_scope(&source_identity(a.path())).unwrap();
        let sb = cache_scope(&source_identity(b.path())).unwrap();
        assert_ne!(
            sa.dir, sb.dir,
            "identical content in two repos must not share a cache"
        );
        let (held, outcome) = sa.lock_build(Duration::from_secs(1));
        assert!(held.is_some() && matches!(outcome, LockOutcome::Acquired { .. }));
        let (none, timed_out) = sa.lock_build(Duration::from_millis(100));
        assert!(none.is_none() && matches!(timed_out, LockOutcome::TimedOut { .. }));
        drop(held);
        let (again, _) = sa.lock_build(Duration::from_secs(1));
        assert!(again.is_some(), "lock released on drop");
    }
}
