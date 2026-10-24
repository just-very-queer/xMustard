//! Repository source identity and the shared symbol-graph cache.
//!
//! `source_identity` is the one content identity for a working tree: canonical root +
//! `HEAD` + every `git status --porcelain=v1 -z --untracked-files=all` entry (status,
//! byte-exact path, rename origin) + the full SHA-256 of each dirty or untracked
//! file's bytes, read through the bounded no-follow reader. The status and the hidden
//! entry listings are streamed, never held whole (WS-RT3), and untracked entries past
//! `MAX_IDENTITY_UNTRACKED` enter the key by path only, with a limitation. Files that
//! cannot be hashed in full (oversized, unreadable, not regular, over the identity
//! byte budget) add a metadata token and an explicit limitation, so
//! `identity_complete` is false: a bounded read never claims identity for bytes it
//! did not read. A status entry that
//! is a nested repository (a directory with its own `.git`: an untracked `sub/`, a
//! submodule, a gitlink without a module entry) is not read: its token is the nested
//! HEAD, ref and Git index stat key, it is listed in `nested_repos`, and it leaves the
//! identity complete (WS-RT2). The same key drives graph-cache invalidation and
//! `xmustard-core repo-key`.
//!
//! Dirty and untracked files are hashed through a stat cache of their own
//! (`hashcache::identity_cache_file_for`, PAR-FRESH-01), so an unchanged dirty file is
//! not read again: `files_hashed` counts the files read, `files_reused` the ones the
//! cache vouched for. Inside `serve`, the watcher keeps the last [`RepoState`] per
//! watched root and reuses it while nothing under the root changed (`repo_state`).
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
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
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
pub(crate) const STALE_TEMP_AGE: Duration = Duration::from_secs(600);

/// Graph snapshots kept per scope (a few, so toggling between states stays warm).
const GRAPH_SNAPSHOTS_KEPT: usize = 3;

/// Parser identity: the index format, tree-sitter ABI and grammar crate versions, and
/// the regex-fallback revision. Part of the cache scope, reported by `repo-key`.
pub fn parser_version() -> String {
    format!(
        "{INDEX_FORMAT_VERSION};ts-abi-{};{};regex-3",
        tree_sitter::LANGUAGE_VERSION,
        crate::index::lang::grammar_ids()
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
pub(crate) fn atomic_write_with(
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

/// Count one Git child started by this process. Every Git spawn outside the tests
/// calls this (`run_git_bounded*`, the index scan's streamed listings, change
/// tracking's `git` helper, `ownership owners`), so `serve`'s `$/stats` can show that
/// queries spawn none while the watcher vouches for the tree (PAR-FRESH-03).
pub fn note_git_spawn() {
    crate::passcount::note(crate::passcount::Pass::GitSpawns, 1);
}

/// Git children this process has started.
pub fn git_spawns() -> u64 {
    crate::passcount::totals().get(crate::passcount::Pass::GitSpawns)
}

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
    run_git_bounded_input(root, args, Vec::new(), max_bytes, timeout)
}

/// `run_git_bounded` with `input` written to the command's stdin (a `cat-file --batch`
/// request); empty input leaves stdin closed.
pub fn run_git_bounded_input(
    root: &Path,
    args: &[&str],
    input: Vec<u8>,
    max_bytes: usize,
    timeout: Duration,
) -> Result<Vec<u8>, GitRunError> {
    use std::io::Read;
    use std::process::Stdio;
    let stdin = if input.is_empty() {
        Stdio::null()
    } else {
        Stdio::piped()
    };
    note_git_spawn();
    let mut child = Command::new("git")
        .arg("--no-optional-locks")
        .arg("-C")
        .arg(root)
        .args(args)
        .stdin(stdin)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| GitRunError::Spawn(e.to_string()))?;
    if let Some(mut w) = child.stdin.take() {
        // a writer of its own, so a large request cannot deadlock against the reader;
        // a child that dies or is killed ends the write with an error, ignored here.
        std::thread::spawn(move || {
            let _ = w.write_all(&input);
        });
    }
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
    match &result {
        Ok(out) => crate::passcount::note(crate::passcount::Pass::FilesListed, listed(args, out)),
        Err(_) => {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
    result
}

/// Path records in a NUL-separated listing (`ls-files -z`, `status -z`); 0 for any other
/// command. A status rename's origin path is a record of its own.
fn listed(args: &[&str], out: &[u8]) -> u64 {
    let sub = args.iter().find(|a| !a.starts_with('-')).copied();
    match sub {
        Some("ls-files" | "status") if args.contains(&"-z") => {
            out.split(|b| *b == 0).filter(|r| !r.is_empty()).count() as u64
        }
        _ => 0,
    }
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

/// Incremental `git status --porcelain=v1 -z` parser: records in, entries out. A rename
/// or copy waits for the record after it, its origin.
#[derive(Default)]
pub(crate) struct PorcelainParser {
    pending: Option<StatusEntry>,
}

impl PorcelainParser {
    /// The entry `rec` completes, if any. Malformed records are skipped.
    pub(crate) fn push(&mut self, rec: &[u8]) -> Option<StatusEntry> {
        if let Some(mut e) = self.pending.take() {
            e.orig_path = Some(rec.to_vec());
            return Some(e);
        }
        if rec.len() < 4 || rec[2] != b' ' {
            return None;
        }
        let xy = [rec[0], rec[1]];
        let e = StatusEntry {
            xy,
            path: rec[3..].to_vec(),
            orig_path: None,
        };
        if matches!(xy[0], b'R' | b'C') || matches!(xy[1], b'R' | b'C') {
            self.pending = Some(e);
            return None;
        }
        Some(e)
    }

    /// A rename or copy the output ended before the origin of.
    pub(crate) fn finish(self) -> Option<StatusEntry> {
        self.pending
    }
}

/// Parse `git status --porcelain=v1 -z` output: `XY SP path NUL`, followed by
/// `orig NUL` for renames and copies. Malformed records are skipped.
pub fn parse_porcelain_v1_z(out: &[u8]) -> Vec<StatusEntry> {
    let mut parser = PorcelainParser::default();
    let mut entries: Vec<StatusEntry> = out
        .split(|b| *b == 0)
        .filter_map(|rec| parser.push(rec))
        .collect();
    entries.extend(parser.finish());
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

pub(crate) fn git_layout(root: &Path) -> Option<GitLayout> {
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
    /// A nested repository (see [`NestedRepo`]) of `kind`: not read; `token` covers
    /// its HEAD, its ref and its Git index stat key, so it is complete for this
    /// repository's identity.
    NestedRepo { kind: &'static str, token: String },
}

/// A directory inside the worktree with a Git dir of its own (`.git` as a directory
/// or a `gitdir:` file). It is a repository of its own: never read as a file, outside
/// this repository's identity and index, and reported instead of being dropped.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
pub struct NestedRepo {
    /// Top-level relative, without a trailing `/`.
    pub path: String,
    /// [`NESTED_SUBMODULE`], [`NESTED_GITLINK_UNREGISTERED`] or [`NESTED_UNTRACKED`].
    pub kind: String,
}

/// A tracked gitlink with a `.gitmodules` entry.
pub const NESTED_SUBMODULE: &str = "submodule";
/// A tracked gitlink with no `.gitmodules` entry (Git cannot clone it).
pub const NESTED_GITLINK_UNREGISTERED: &str = "gitlink_unregistered";
/// An untracked directory Git reports as a repository of its own (`?? sub/`).
pub const NESTED_UNTRACKED: &str = "nested_repo";

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
    /// Untracked entries past `MAX_IDENTITY_UNTRACKED`: counted in `untracked_entries`,
    /// keyed by path, absent from the entries a `RepoState` carries.
    #[serde(skip)]
    pub untracked_beyond: usize,
    /// Assume-unchanged / skip-worktree index entries hashed directly because
    /// `git status` does not report their worktree edits.
    pub hidden_entries: usize,
    /// Dirty or untracked files read and hashed by this call.
    pub files_hashed: usize,
    pub bytes_hashed: u64,
    /// Dirty or untracked files whose hash the stat cache vouched for (not read).
    pub files_reused: usize,
    /// Status entries that are nested repositories, by path; omitted when none. They
    /// are covered by their own token, so they never make the identity incomplete.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub nested_repos: Vec<NestedRepo>,
    #[serde(skip)]
    pub dirty: BTreeMap<Vec<u8>, DirtyContent>,
    #[serde(skip)]
    pub layout: Option<GitLayout>,
    /// Tracked (non-untracked) status entries skipped by the identity budget. Untracked
    /// entries are never read by the graph, so only these block graph caching.
    #[serde(skip)]
    pub tracked_unhashed: Vec<String>,
    /// `repo-key` only: the directories `git status --ignored=matching` reports as
    /// ignored as a whole (top-level relative, trailing `/`). Git never descends into
    /// them, so nothing inside can change the key; the Go identity cache skips them
    /// when it fingerprints the working tree. None when not requested or not fully
    /// reportable (a non-UTF-8 path, or more than `MAX_IGNORED_DIRS` or
    /// `MAX_IGNORED_DIR_BYTES`). Never part of the key.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ignored_dirs: Option<Vec<String>>,
}

impl SourceIdentity {
    /// Whether the graph for this identity may be served from, and stored to, the
    /// cache: see `graph_bypass_reason`.
    pub fn graph_cacheable(&self) -> bool {
        self.graph_bypass_reason().is_none()
    }

    /// Why this identity does not determine the graph's input, or None when it does:
    /// Git mode, status and the hidden-entry listing (or the index reader) succeeded, and every *tracked*
    /// dirty file has a known identity (hashed, deleted, symlink, or excluded from the
    /// graph by size with its size/ctime in the key). Untracked files never feed the
    /// graph, so an untracked-only limitation leaves the graph cacheable while
    /// `identity_complete` stays false.
    pub fn graph_bypass_reason(&self) -> Option<String> {
        if let Some(l) = self.limitations.iter().find(|l| {
            matches!(
                l.reason.as_str(),
                "git_status_failed"
                    | "git_unavailable"
                    | "git_index_flags_failed"
                    | "git_index_unsupported"
                    | "git_index_failed"
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

/// Bounds on the ignored-directory listing `repo-key` reports (count and total bytes).
pub const MAX_IGNORED_DIRS: usize = 8192;
pub const MAX_IGNORED_DIR_BYTES: usize = 256 << 10;

fn push_field(h: &mut Sha256, bytes: &[u8]) {
    h.update((bytes.len() as u64).to_le_bytes());
    h.update(bytes);
}

/// Bytes read from a Git dir's small files (`HEAD`, a loose ref, a `gitdir:` pointer).
const GIT_POINTER_BYTES: u64 = 4 << 10;
/// Bytes of `.gitmodules` read to tell a submodule from an unregistered gitlink.
const MAX_GITMODULES_BYTES: u64 = 1 << 20;

fn read_small(path: &Path, cap: u64) -> Option<Vec<u8>> {
    use std::io::Read;
    let mut buf = Vec::new();
    fs::File::open(path)
        .ok()?
        .take(cap)
        .read_to_end(&mut buf)
        .ok()?;
    Some(buf)
}

/// The stat key of `path` for a key token, `absent` when it cannot be stat'ed.
fn stat_token(path: &Path) -> String {
    fs::symlink_metadata(path).map_or_else(|_| "absent".into(), |m| change_token(&m))
}

/// Whether the directory `dir` holds a `.git` of its own (a directory or a file).
pub(crate) fn has_own_git(dir: &Path) -> bool {
    crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
    fs::symlink_metadata(dir.join(".git")).is_ok_and(|m| m.is_dir() || m.is_file())
}

/// The Git dir of the repository whose worktree is `dir`: `dir/.git`, or the target of
/// a `gitdir:` file there (relative to `dir`). None when neither leads to a directory.
fn nested_git_dir(dir: &Path) -> Option<PathBuf> {
    let dot = dir.join(".git");
    let meta = fs::symlink_metadata(&dot).ok()?;
    if meta.is_dir() {
        return Some(dot);
    }
    let text = read_small(&dot, GIT_POINTER_BYTES)?;
    let target = text.strip_prefix(b"gitdir:")?.trim_ascii();
    let git_dir = dir.join(path_from_bytes(target));
    git_dir.is_dir().then_some(git_dir)
}

/// Bytes of a nested repository's `packed-refs` scanned for one ref.
const MAX_PACKED_REFS_BYTES: u64 = 64 << 20;

/// The object id `packed-refs` at `path` holds for ref `name`: Some(None) when the file
/// is absent or does not name it (an unborn branch), None when it cannot be opened or
/// read to its end within MAX_PACKED_REFS_BYTES. Read in lines of at most
/// GIT_POINTER_BYTES (a longer line names no ref and is skipped); nothing else is kept.
fn packed_ref(path: &Path, name: &[u8]) -> Option<Option<Vec<u8>>> {
    use std::io::{BufRead, Read};
    let f = match fs::File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Some(None),
        Err(_) => return None,
    };
    let mut r = std::io::BufReader::new(f.take(MAX_PACKED_REFS_BYTES));
    let (mut line, mut total, mut long) = (Vec::new(), 0u64, false);
    loop {
        line.clear();
        let n = (&mut r)
            .take(GIT_POINTER_BYTES)
            .read_until(b'\n', &mut line)
            .ok()?;
        if n == 0 {
            return (total < MAX_PACKED_REFS_BYTES).then_some(None);
        }
        total += n as u64;
        let ended = line.last() == Some(&b'\n');
        // the rest of an over-long line, or its head, is not a ref line
        let skip = std::mem::replace(&mut long, !ended) || !ended;
        let l = line.trim_ascii_end();
        if !skip
            && let Some(sp) = l.iter().position(|b| *b == b' ')
            && &l[sp + 1..] == name
        {
            return Some(Some(l[..sp].to_vec()));
        }
    }
}

/// The token of a nested repository at `dir`, read without a Git child: its HEAD, the
/// object id of the ref HEAD names (the loose ref, else its `packed-refs` line) and
/// the stat key of its Git index. A commit, a checkout or staging inside it changes the token; an
/// unstaged edit in its worktree belongs to that repository and does not. None when
/// its Git dir does not resolve or its HEAD cannot be read: then nothing keys it.
fn nested_repo_token(dir: &Path) -> Option<String> {
    let mut h = Sha256::new();
    match nested_git_dir(dir) {
        None => return None,
        Some(git_dir) => {
            let head =
                read_small(&git_dir.join("HEAD"), GIT_POINTER_BYTES).filter(|h| !h.is_empty())?;
            push_field(&mut h, &head);
            let common = read_small(&git_dir.join("commondir"), GIT_POINTER_BYTES).map_or_else(
                || git_dir.clone(),
                |c| git_dir.join(path_from_bytes(c.trim_ascii())),
            );
            let named = head
                .strip_prefix(b"ref: ")
                .map(<[u8]>::trim_ascii)
                .filter(|r| {
                    r.starts_with(b"refs/") && !r.split(|b| *b == b'/').any(|c| c == b"..")
                });
            if let Some(name) = named {
                let rel = path_from_bytes(name);
                let loose = read_small(&git_dir.join(&rel), GIT_POINTER_BYTES)
                    .or_else(|| read_small(&common.join(&rel), GIT_POINTER_BYTES));
                let oid = match loose {
                    Some(oid) => Some(oid),
                    None => packed_ref(&common.join("packed-refs"), name)?,
                };
                push_field(&mut h, oid.as_deref().unwrap_or(b"unborn"));
            }
            push_field(&mut h, stat_token(&git_dir.join("index")).as_bytes());
        }
    }
    Some(format!("nested_repo:{:x}", h.finalize()))
}

/// The `path` values of the `[submodule "..."]` sections of `<toplevel>/.gitmodules`
/// within its first MAX_GITMODULES_BYTES (a gitlink named only past them reads as
/// unregistered); empty when absent. A value is decoded
/// as Git config does: quotes removed, `\"`, `\\`, `\t` and `\n` unescaped, and an
/// unquoted `#` or `;` starts a comment.
pub(crate) fn submodule_paths(toplevel: &Path) -> std::collections::BTreeSet<String> {
    use std::io::BufRead;
    let mut paths = std::collections::BTreeSet::new();
    let Ok(f) = fs::File::open(toplevel.join(".gitmodules")) else {
        return paths;
    };
    let f = std::io::Read::take(f, MAX_GITMODULES_BYTES);
    let mut in_submodule = false;
    for line in std::io::BufReader::new(f).lines().map_while(Result::ok) {
        let line = line.trim();
        if let Some(section) = line.strip_prefix('[') {
            // Git section names are case-insensitive: `[Submodule "a"]` counts
            let s = section.trim_start();
            let n = "submodule".len();
            in_submodule = s
                .get(..n)
                .is_some_and(|p| p.eq_ignore_ascii_case("submodule"))
                && s[n..].starts_with([' ', '\t', '"', ']']);
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        if in_submodule && key.trim().eq_ignore_ascii_case("path") {
            paths.insert(config_value(value));
        }
    }
    paths
}

/// A Git config value as Git reads it (see `submodule_paths`).
fn config_value(raw: &str) -> String {
    let mut out = String::new();
    let (mut quoted, mut chars) = (false, raw.trim().chars());
    while let Some(c) = chars.next() {
        match (c, quoted) {
            ('"', _) => quoted = !quoted,
            ('\\', _) => match chars.next() {
                Some('t') => out.push('\t'),
                Some('n') => out.push('\n'),
                Some(other) => out.push(other),
                None => {}
            },
            ('#' | ';', false) => break,
            _ => out.push(c),
        }
    }
    out.trim_end().to_string()
}

/// The kind of the nested repository at top-level relative `rel`: an untracked one,
/// or a tracked gitlink with or without its `.gitmodules` entry.
pub(crate) fn nested_kind(
    tracked: bool,
    rel: &str,
    modules: &std::collections::BTreeSet<String>,
) -> &'static str {
    match (tracked, modules.contains(rel)) {
        (false, _) => NESTED_UNTRACKED,
        (true, true) => NESTED_SUBMODULE,
        (true, false) => NESTED_GITLINK_UNREGISTERED,
    }
}

/// A status entry that is a directory, classified without reading it: a nested
/// repository, or `not_regular`. None for anything else (a file, a symlink, a path
/// gone from the worktree).
fn directory_entry(
    toplevel: &Path,
    e: &StatusEntry,
    modules: &mut Option<std::collections::BTreeSet<String>>,
) -> Option<DirtyContent> {
    let trimmed = e.path.strip_suffix(b"/").unwrap_or(&e.path);
    let dir = toplevel.join(path_from_bytes(trimmed));
    crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
    if !fs::symlink_metadata(&dir).is_ok_and(|m| m.is_dir()) {
        return None;
    }
    if !has_own_git(&dir) {
        return Some(DirtyContent::Incomplete(SourceReadFailure::NotRegular));
    }
    // a `.git` whose Git dir or HEAD cannot be read has no state to key
    let Some(token) = nested_repo_token(&dir) else {
        return Some(DirtyContent::Incomplete(SourceReadFailure::Unreadable(
            "nested repository: its Git dir or HEAD cannot be read".into(),
        )));
    };
    let rel = String::from_utf8_lossy(trimmed);
    let modules = modules.get_or_insert_with(|| submodule_paths(toplevel));
    let kind = nested_kind(!e.is_untracked(), &rel, modules);
    Some(DirtyContent::NestedRepo { kind, token })
}

/// Compute the source identity of the working tree at `root` (see module docs).
pub fn source_identity(root: &Path) -> SourceIdentity {
    source_identity_with(root, false).0
}

/// `source_identity` plus the ignored-directory listing (`ignored_dirs`) the
/// `repo-key` command reports. The key is the same as `source_identity`'s.
pub fn repo_key_identity(root: &Path) -> SourceIdentity {
    source_identity_with(root, true).0
}

/// One observation of a working tree: its source identity, the status entries the
/// identity was computed from (porcelain entries, untracked included, plus the hidden
/// assume-unchanged/skip-worktree entries with `xy[0] == b'!'`), and its branch and
/// origin remote. Change tracking (drift, working changes) reads it instead of running
/// its own Git listings (PAR-FRESH-01: one identity scheme).
#[derive(Debug)]
pub struct RepoState {
    pub identity: SourceIdentity,
    pub status: Vec<StatusEntry>,
    pub branch: Option<String>,
    pub remote_url: Option<String>,
}

/// Observe `root` now: the source identity, its status entries, branch and remote.
pub fn observe_repo_state(root: &Path) -> RepoState {
    let (identity, status) = source_identity_with(root, false);
    let line = |args: &[&str]| {
        run_git_bounded(root, args, 64 << 10, git_timeout())
            .ok()
            .map(|b| String::from_utf8_lossy(&b).trim().to_string())
            .filter(|s| !s.is_empty())
    };
    let (branch, remote_url) = match identity.layout {
        Some(_) => (
            line(&["rev-parse", "--abbrev-ref", "HEAD"]),
            line(&["remote", "get-url", "origin"]),
        ),
        None => (None, None),
    };
    RepoState {
        identity,
        status,
        branch,
        remote_url,
    }
}

/// The repository state of `root`: the watcher's last observation while it vouches
/// that nothing under the root changed since (inside `serve`, a watched root with no
/// Git spawn and no hashing), else a new observation. The flag says whether the
/// watcher's copy answered.
pub fn repo_state(root: &Path) -> (Arc<RepoState>, bool) {
    crate::index::watch::memo(root, || Arc::new(observe_repo_state(root)))
}

/// Collect the whole-directory `!!` entries of a `--ignored=matching` status, or None
/// when they cannot all be reported.
pub(crate) fn ignored_dir_listing(entries: &[StatusEntry]) -> Option<Vec<String>> {
    let mut dirs = Vec::new();
    let mut bytes = 0usize;
    for e in entries.iter().filter(|e| e.xy == *b"!!") {
        if e.path.last() != Some(&b'/') {
            continue; // an individually ignored file: its bytes never enter the key
        }
        let dir = String::from_utf8(e.path.clone()).ok()?;
        bytes += dir.len();
        if dirs.len() >= MAX_IGNORED_DIRS || bytes > MAX_IGNORED_DIR_BYTES {
            return None;
        }
        dirs.push(dir);
    }
    Some(dirs)
}

/// Untracked status entries identified per call, in Git's order (PAR-RT-11). Each one
/// past the bound enters the key by its path only and is counted in one
/// `identity_untracked_budget` limitation.
pub const MAX_IDENTITY_UNTRACKED: usize = 10_000;

/// One streamed `git status` listing: the entries the identity keeps, the untracked
/// ones past `MAX_IDENTITY_UNTRACKED` folded into a digest of their paths, and (for
/// `repo-key`) the whole-directory ignored entries within the listing's bounds.
struct StatusWalk {
    parser: PorcelainParser,
    entries: Vec<StatusEntry>,
    untracked_kept: usize,
    beyond: usize,
    beyond_paths: Sha256,
    list_ignored: bool,
    ignored: Vec<StatusEntry>,
    ignored_bytes: usize,
    ignored_overflow: bool,
}

impl StatusWalk {
    fn new(list_ignored: bool) -> Self {
        StatusWalk {
            parser: PorcelainParser::default(),
            entries: Vec::new(),
            untracked_kept: 0,
            beyond: 0,
            beyond_paths: Sha256::new(),
            list_ignored,
            ignored: Vec::new(),
            ignored_bytes: 0,
            ignored_overflow: false,
        }
    }

    fn record(&mut self, rec: &[u8]) {
        if let Some(e) = self.parser.push(rec) {
            self.entry(e);
        }
    }

    fn entry(&mut self, e: StatusEntry) {
        match (e.xy == *b"!!", e.is_untracked()) {
            // ignored entries are not working state: they never enter the key
            (true, _) => self.ignored_dir(e),
            (false, true) if self.untracked_kept >= MAX_IDENTITY_UNTRACKED => {
                self.beyond += 1;
                push_field(&mut self.beyond_paths, &e.path);
            }
            (false, true) => {
                self.untracked_kept += 1;
                self.entries.push(e);
            }
            (false, false) => self.entries.push(e),
        }
    }

    /// Keep a whole-directory ignored entry for `ignored_dir_listing` while the listing
    /// stays within its bounds; past them the listing is not reportable.
    fn ignored_dir(&mut self, e: StatusEntry) {
        if !self.list_ignored || self.ignored_overflow || e.path.last() != Some(&b'/') {
            return;
        }
        self.ignored_bytes += e.path.len();
        self.ignored_overflow =
            self.ignored.len() >= MAX_IGNORED_DIRS || self.ignored_bytes > MAX_IGNORED_DIR_BYTES;
        if !self.ignored_overflow {
            self.ignored.push(e);
        }
    }

    fn finish(mut self) -> Self {
        if let Some(e) = std::mem::take(&mut self.parser).finish() {
            self.entry(e);
        }
        self
    }

    fn ignored_dirs(&self) -> Option<Vec<String>> {
        (!self.ignored_overflow)
            .then(|| ignored_dir_listing(&self.ignored))
            .flatten()
    }
}

/// What the identity is computed from: the streamed status listing, the hidden
/// (assume-unchanged / skip-worktree) entries, and why the untracked part is
/// incomplete, if it is.
struct IdentityListing {
    walk: StatusWalk,
    hidden: Result<Vec<StatusEntry>, String>,
    untracked_incomplete: Option<String>,
}

/// The identity's listing from the source `XMUSTARD_GIT_INDEX_READER` picks (WS-RT18):
/// the in-process reader (`gitindex::status`), or `git status` and `git ls-files -v`.
/// Under `auto` a reader refusal goes to Git only while an index-loading Git child is
/// predicted within the Git line; otherwise it is the error, as (reason, detail).
fn identity_listing(
    root: &Path,
    layout: &GitLayout,
    list_ignored: bool,
) -> Result<IdentityListing, (&'static str, String)> {
    use crate::gitindex::{Setting, status};
    let via_reader = || -> Result<IdentityListing, crate::gitindex::Refusal> {
        let repo = status::Repo::open(root)?;
        let mut walk = StatusWalk::new(list_ignored);
        let opts = status::Options {
            untracked: true,
            list_ignored,
        };
        let out = status::status(&repo, opts, &mut |e| {
            walk.entry(e);
            std::ops::ControlFlow::Continue(())
        })?;
        Ok(IdentityListing {
            walk: walk.finish(),
            hidden: Ok(out.hidden),
            untracked_incomplete: out.untracked_incomplete,
        })
    };
    let via_git = || -> Result<IdentityListing, (&'static str, String)> {
        // explicit flags override config that would hide working state
        // (`status.showUntrackedFiles`, `submodule.<name>.ignore`,
        // `diff.ignoreSubmodules`, `status.renames`); the reader reports the same view.
        let mut args = vec![
            "status",
            "--porcelain=v1",
            "-z",
            "--untracked-files=all",
            "--ignore-submodules=none",
            "--no-renames",
        ];
        if list_ignored {
            // `matching` lists a directory that matches an ignore pattern without
            // descending into it, so the listing costs no extra traversal.
            args.push("--ignored=matching");
        }
        let mut walk = StatusWalk::new(list_ignored);
        crate::index::scan::stream_git_records(root, &args, |rec| walk.record(rec))
            .map_err(|e| ("git_status_failed", e))?;
        Ok(IdentityListing {
            walk: walk.finish(),
            hidden: hidden_index_entries(&layout.toplevel),
            untracked_incomplete: None,
        })
    };
    let setting = Setting::from_env();
    if setting == Setting::Git {
        return via_git();
    }
    match via_reader() {
        Ok(listed) => Ok(listed),
        Err(error)
            if !crate::gitindex::is_submodule_refusal(&error)
                && crate::gitindex::falls_back(setting, &layout.git_dir) =>
        {
            via_git()
        }
        Err(r) => Err((r.reason(), r.to_string())),
    }
}

fn source_identity_with(root: &Path, list_ignored: bool) -> (SourceIdentity, Vec<StatusEntry>) {
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
        untracked_beyond: 0,
        hidden_entries: 0,
        files_hashed: 0,
        bytes_hashed: 0,
        files_reused: 0,
        nested_repos: Vec::new(),
        dirty: BTreeMap::new(),
        layout: None,
        tracked_unhashed: Vec::new(),
        ignored_dirs: None,
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
        return (id, Vec::new());
    };
    id.head = git_output(root, &["rev-parse", "-q", "--verify", "HEAD^{commit}"])
        .map(|b| String::from_utf8_lossy(&b).trim().to_string())
        .unwrap_or_default();
    push_field(&mut h, id.head.as_bytes());

    // streamed record by record: no listing is held whole, so no listing size fails the
    // identity; only the kept entries stay
    let listed = match identity_listing(root, &layout, list_ignored) {
        Ok(listed) => listed,
        Err((reason, detail)) => {
            id.identity_complete = false;
            id.limitations.push(IdentityLimitation {
                path: None,
                reason: reason.into(),
                detail: format!("{detail}; dirty content is not identified"),
            });
            h.update(b"status-failed");
            id.key = format!("{:x}", h.finalize());
            id.layout = Some(layout);
            return (id, Vec::new());
        }
    };
    let walk = listed.walk;
    if list_ignored {
        id.ignored_dirs = walk.ignored_dirs();
    }
    let (mut entries, beyond) = (walk.entries, (walk.beyond, walk.beyond_paths));
    // `git status` hides worktree edits to assume-unchanged / skip-worktree entries;
    // identify their bytes directly so no known working-state content is omitted.
    match listed.hidden {
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
    if let Some(why) = listed.untracked_incomplete {
        id.limitations.push(IdentityLimitation {
            path: None,
            reason: "untracked_walk_incomplete".into(),
            detail: format!("{why}; untracked paths past it are not identified"),
        });
    }
    // tracked changes claim the byte budget before untracked files.
    entries.sort_by(|a, b| {
        a.is_untracked()
            .cmp(&b.is_untracked())
            .then_with(|| a.path.cmp(&b.path))
    });
    // Regular files come from the identity's stat cache: only new, changed or racy ones
    // are read. Tracked entries claim the byte budget first (the entries' order), and
    // reused files are charged too, so the key does not depend on the cache.
    // `sub/` (Git's form for an untracked nested repository) is a directory: it never
    // reaches the hasher.
    let rels: Vec<String> = entries
        .iter()
        .filter(|e| e.path.last() != Some(&b'/'))
        .filter_map(|e| std::str::from_utf8(&e.path).ok().map(str::to_string))
        .collect();
    let cache = crate::hashcache::identity_cache_file_for(&layout);
    let (cached, pass) = crate::hashcache::hash_files_budgeted(
        &layout.toplevel,
        rels,
        Some(&cache),
        MAX_IDENTITY_BYTES,
    );
    id.files_hashed = pass.hashed;
    id.bytes_hashed = pass.bytes_hashed;
    id.files_reused = pass.reused;
    let mut budget = MAX_IDENTITY_BYTES.saturating_sub(pass.bytes_hashed + pass.bytes_reused);
    let mut modules = None;
    for e in &entries {
        if e.is_untracked() {
            id.untracked_entries += 1;
        } else if e.xy[0] == b'!' {
            id.hidden_entries += 1;
        } else {
            id.dirty_entries += 1;
        }
        let rel = path_from_bytes(&e.path);
        let from_cache = std::str::from_utf8(&e.path)
            .ok()
            .and_then(|p| cached.get(p));
        let content = if let Some(hex) = from_cache {
            DirtyContent::Sha256(hex.clone())
        } else if let Some(dir) = directory_entry(&layout.toplevel, e, &mut modules) {
            dir
        } else if budget == 0 {
            DirtyContent::Unhashed
        } else {
            match read_source_beneath(&layout.toplevel, &rel, MAX_REPO_FILE_BYTES.min(budget)) {
                Ok(bytes) => {
                    budget -= bytes.len() as u64;
                    id.files_hashed += 1;
                    id.bytes_hashed += bytes.len() as u64;
                    crate::passcount::hashed(bytes.len() as u64);
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
            DirtyContent::NestedRepo { kind, token } => format!("{kind}:{token}"),
            DirtyContent::Incomplete(_) | DirtyContent::Unhashed => {
                // metadata only: changes on most edits, but never claimed as identity.
                let what = match &content {
                    DirtyContent::Incomplete(f) => f.reason(),
                    _ => "identity_budget",
                };
                crate::passcount::note(crate::passcount::Pass::FilesStatted, 1);
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
            DirtyContent::NestedRepo { kind, .. } => {
                let trimmed = e.path.strip_suffix(b"/").unwrap_or(&e.path);
                id.nested_repos.push(NestedRepo {
                    path: String::from_utf8_lossy(trimmed).into_owned(),
                    kind: (*kind).to_string(),
                })
            }
            _ => {}
        }
        id.dirty.insert(e.path.clone(), content);
    }
    let (beyond, beyond_paths) = beyond;
    if beyond > 0 {
        h.update(b"untracked-beyond");
        push_field(&mut h, &(beyond as u64).to_le_bytes());
        push_field(&mut h, &beyond_paths.finalize());
        id.untracked_entries += beyond;
        id.untracked_beyond = beyond;
        id.limitations.push(IdentityLimitation {
            path: None,
            reason: "identity_untracked_budget".into(),
            detail: format!(
                "{beyond} untracked entries past the first {MAX_IDENTITY_UNTRACKED} are keyed by \
                 path only; their content is not identified"
            ),
        });
    }
    id.identity_complete = id.limitations.is_empty();
    id.nested_repos.sort();
    id.key = format!("{:x}", h.finalize());
    id.layout = Some(layout);
    (id, entries)
}

/// The source-identity key (kept for callers of the former size/mtime key).
pub fn cheap_key(root: &Path) -> String {
    source_identity(root).key
}

/// Index entries whose worktree state `git status` does not report: assume-unchanged
/// (`git ls-files -v` tag in lower case) and skip-worktree (`S`/`s`). Returned as
/// status entries with `xy = ['!', tag]`, a marker no porcelain status uses, so the
/// key distinguishes them. `toplevel` must be the worktree top (paths are relative).
/// The listing is streamed and only the hidden entries are kept.
fn hidden_index_entries(toplevel: &Path) -> Result<Vec<StatusEntry>, String> {
    let mut hidden: Vec<StatusEntry> = Vec::new();
    crate::index::scan::stream_git_records(toplevel, &["ls-files", "-v", "-z"], |rec| {
        if rec.len() < 3 || rec[1] != b' ' {
            return;
        }
        let tag = rec[0];
        // unmerged entries repeat a path; keep one.
        let repeated = hidden.last().is_some_and(|e| e.path == rec[2..]);
        if (tag.is_ascii_lowercase() || tag == b'S') && !repeated {
            hidden.push(StatusEntry {
                xy: [b'!', tag],
                path: rec[2..].to_vec(),
                orig_path: None,
            });
        }
    })?;
    Ok(hidden)
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

/// `<git-dir>/xmustard-cache`, the parent of every per-repository xMustard cache.
pub(crate) fn xmustard_cache_dir(layout: &GitLayout) -> PathBuf {
    layout.git_dir.join("xmustard-cache")
}

/// The cache scope for `id`, or None outside Git.
pub fn cache_scope(id: &SourceIdentity) -> Option<CacheScope> {
    let layout = id.layout.as_ref()?;
    let mut h = Sha256::new();
    push_field(&mut h, id.root.as_bytes());
    push_field(&mut h, trust_scope().as_bytes());
    push_field(&mut h, id.parser_version.as_bytes());
    let scope = format!("{:x}", h.finalize());
    let base = xmustard_cache_dir(layout);
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
        if let Err(e) = fs::create_dir_all(&self.dir) {
            return (None, LockOutcome::Unavailable(e.to_string()));
        }
        lock_file(&self.dir.join("build.lock"), timeout)
    }
}

/// Take an OS advisory lock on `path` (created if absent; its parent must exist),
/// waiting at most `timeout` (`Duration::ZERO` tries once). Released when the returned
/// guard drops or the holder exits, so a crashed holder never leaves a stale lock.
pub(crate) fn lock_file(path: &Path, timeout: Duration) -> (Option<BuildLock>, LockOutcome) {
    let start = Instant::now();
    let file = match fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(path)
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
    fn repo_key_lists_whole_ignored_dirs_without_changing_the_key() {
        let r = repo_with(&[
            (".gitignore", b"build/\n*.log\nlogs/*.tmp\n"),
            ("a.rs", b"fn a() {}\n"),
        ]);
        assert!(source_identity(r.path()).ignored_dirs.is_none());
        fs::create_dir_all(r.path().join("build/deep")).unwrap();
        fs::write(r.path().join("build/deep/x.o"), b"obj").unwrap();
        fs::write(r.path().join("app.log"), b"log").unwrap();
        fs::create_dir_all(r.path().join("logs")).unwrap();
        fs::write(r.path().join("logs/a.tmp"), b"t").unwrap();
        fs::create_dir_all(r.path().join("new/build")).unwrap();
        fs::write(r.path().join("new/build/y.o"), b"obj").unwrap();
        fs::write(r.path().join("new/keep.rs"), b"fn k() {}\n").unwrap();
        let listed = repo_key_identity(r.path());
        let plain = source_identity(r.path());
        assert_eq!(listed.key, plain.key, "the listing never enters the key");
        assert_eq!(listed.untracked_entries, 1, "only new/keep.rs is untracked");
        assert!(listed.identity_complete);
        let mut dirs = listed.ignored_dirs.clone().unwrap();
        dirs.sort();
        // logs/ is not listed: only files inside it match, so git descends into it
        assert_eq!(dirs, vec!["build/".to_string(), "new/build/".to_string()]);
        fs::write(r.path().join("build/deep/x.o"), b"changed").unwrap();
        assert_eq!(repo_key_identity(r.path()).key, listed.key);
        let json = serde_json::to_value(&listed).unwrap();
        assert!(json["ignored_dirs"].is_array());
        assert!(
            serde_json::to_value(&plain)
                .unwrap()
                .get("ignored_dirs")
                .is_none()
        );
    }

    #[test]
    fn ignored_dir_listing_is_all_or_nothing() {
        let e = |p: &[u8]| StatusEntry {
            xy: *b"!!",
            path: p.to_vec(),
            orig_path: None,
        };
        assert_eq!(
            ignored_dir_listing(&[e(b"a/"), e(b"f.log")]),
            Some(vec!["a/".to_string()])
        );
        assert_eq!(ignored_dir_listing(&[e(b"a/"), e(b"\xff/")]), None);
        let many: Vec<_> = (0..=MAX_IGNORED_DIRS)
            .map(|i| e(format!("d{i}/").as_bytes()))
            .collect();
        assert_eq!(ignored_dir_listing(&many), None);
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
    // worktree must still surface, as a nested repository that changes the key and
    // leaves the identity complete (WS-RT2; before it, a `not_regular` limitation).
    #[test]
    fn ignored_dirty_submodule_is_a_nested_repository() {
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
        let clean = source_identity(outer.path());
        assert!(clean.identity_complete && clean.nested_repos.is_empty());
        fs::write(outer.path().join("sub/lib.rs"), b"fn inner_edited() {}\n").unwrap();
        let id = source_identity(outer.path());
        assert!(id.identity_complete, "{:?}", id.limitations);
        assert_eq!(
            id.nested_repos,
            vec![NestedRepo {
                path: "sub".into(),
                kind: NESTED_SUBMODULE.into()
            }]
        );
        assert_ne!(id.key, clean.key, "the dirty submodule is part of the key");
    }

    // WS-RT2: a directory entry is classified without a read; a `.git` that does not
    // resolve to a Git dir leaves nothing to key, so it is `not_regular`, not nested.
    #[test]
    fn a_directory_entry_is_nested_only_with_a_resolvable_git_dir() {
        let top = tempfile::TempDir::new().unwrap();
        let entry = |path: &str| StatusEntry {
            xy: *b" M",
            path: path.as_bytes().to_vec(),
            orig_path: None,
        };
        fs::create_dir_all(top.path().join("good/.git")).unwrap();
        fs::write(top.path().join("good/.git/HEAD"), b"ref: refs/heads/main\n").unwrap();
        fs::create_dir_all(top.path().join("broken")).unwrap();
        fs::write(top.path().join("broken/.git"), b"gitdir: /nonexistent/xm\n").unwrap();
        fs::create_dir_all(top.path().join("headless/.git")).unwrap();
        fs::create_dir_all(top.path().join("plain")).unwrap();
        fs::write(top.path().join("file.rs"), b"fn f() {}\n").unwrap();
        let mut modules = None;
        let mut kind = |p: &str| match directory_entry(top.path(), &entry(p), &mut modules) {
            Some(DirtyContent::NestedRepo { kind, .. }) => kind.to_string(),
            Some(DirtyContent::Incomplete(f)) => f.reason().to_string(),
            Some(_) => "other".into(),
            None => "none".into(),
        };
        assert_eq!(kind("good"), NESTED_GITLINK_UNREGISTERED);
        assert_eq!(kind("broken"), "unreadable");
        assert_eq!(kind("headless"), "unreadable");
        assert_eq!(kind("plain"), "not_regular");
        assert_eq!(kind("file.rs"), "none");
        assert_eq!(kind("gone"), "none");
    }

    // A ref kept only in `packed-refs` keys the nested repository by its object id, so
    // a rewrite that keeps the file's size changes the token.
    #[test]
    fn a_packed_ref_keys_the_nested_repository_by_its_object_id() {
        let top = tempfile::TempDir::new().unwrap();
        let git = top.path().join("sub/.git");
        fs::create_dir_all(&git).unwrap();
        fs::write(git.join("HEAD"), b"ref: refs/heads/main\n").unwrap();
        let packed = |oid: &str| {
            fs::write(
                git.join("packed-refs"),
                format!("# pack-refs with: peeled\n{oid} refs/heads/main\n"),
            )
            .unwrap()
        };
        let dir = top.path().join("sub");
        packed("aaaa");
        let a = nested_repo_token(&dir).unwrap();
        packed("bbbb");
        let b = nested_repo_token(&dir).unwrap();
        assert_ne!(a, b);
        // a ref line inside an over-long line is not read as one
        let long = format!("{} {}\naaaa refs/heads/main\n", "x".repeat(8 << 10), "y");
        fs::write(git.join("packed-refs"), long).unwrap();
        assert_eq!(nested_repo_token(&dir).unwrap(), a);
        fs::write(
            git.join("packed-refs"),
            format!("{}aaaa refs/heads/main\n", "z".repeat(8 << 10)),
        )
        .unwrap();
        let tail = nested_repo_token(&dir).unwrap();
        // a packed-refs that exists but cannot be opened (a symlink loop) keys nothing
        fs::remove_file(git.join("packed-refs")).unwrap();
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink("packed-refs", git.join("packed-refs")).unwrap();
            assert_eq!(nested_repo_token(&dir), None);
            fs::remove_file(git.join("packed-refs")).unwrap();
        }
        let unborn = nested_repo_token(&dir).unwrap();
        assert!(unborn != a && unborn != b);
        assert_eq!(tail, unborn, "the tail of an over-long line names no ref");
    }

    #[test]
    fn submodule_paths_reads_only_submodule_sections_as_git_does() {
        let top = tempfile::TempDir::new().unwrap();
        fs::write(
            top.path().join(".gitmodules"),
            concat!(
                "[core]\n\tpath = not/a/module\n",
                "[submodule \"a\"]\n\tpath = libs/a ; comment\n\turl = ../a\n",
                "[submodule \"b\"]\n\tPath = \"with space/b\"\n",
                "[submodule \"c\"]\n\tpath = \"quote\\\"d\" # trailing\n",
                "[submodule-extra \"x\"]\n\tpath = not/either\n",
                "[SubModule \"d\"]\n\tpath = mixed/case\n",
            ),
        )
        .unwrap();
        let got: Vec<String> = submodule_paths(top.path()).into_iter().collect();
        assert_eq!(got, ["libs/a", "mixed/case", "quote\"d", "with space/b"]);
        assert!(submodule_paths(&top.path().join("absent")).is_empty());
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
