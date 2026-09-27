//! Which files the index covers, and a stat key that identifies unchanged ones.
//!
//! Git mode streams tracked files from `git ls-files -s -z` (index only: Git never
//! reads the worktree here) and stats each source file once. The listing is parsed as it
//! arrives and never buffered whole. Dirty detection is ours: a file whose stat key
//! (size, mtime, ctime, inode, device) matches the stored one, and whose mtime was safely
//! before the last scan, is unchanged without being read; any other file is read and
//! identified by its Git blob id. `git diff-files` is not used: on smudged or racily
//! clean index entries it re-reads every file (measured 33 MB RSS and 24 MB footprint on
//! a fresh cline clone). Non-Git mode (`allow_non_git`) walks the directory without
//! following symlinks and applies `.gitignore` and `.xmustardignore`.
//!
//! The scan also applies the file and byte bounds of the scale envelope (PAR-RT-09), so
//! its memory never grows with the number of tracked files: at most
//! `max_files + MAX_LOSS_ROWS` readable candidates and `MAX_LOSS_ROWS` candidates with a
//! pre-read loss are materialized (the smallest paths, kept in two bounded heaps, so
//! loss rows never displace readable files); every other eligible file is only counted,
//! by loss reason. In path order, the first `max_files` readable files whose sizes fit
//! `max_total_bytes` are indexed; a readable file that does not fit the byte budget is
//! an `envelope_bytes` loss, and every file after the `max_files`-th readable one is an
//! `envelope_files` loss. The kept set is therefore a pure function of the tree, so full
//! builds and incremental updates agree on it.
//!
//! Git's watchdog times Git, not this side: time spent classifying records (lstat,
//! ignore matching) while Git waits on the pipe is not counted against `git_timeout`, so
//! a slow filesystem or an expensive ignore file makes the scan slower, never failed.
//! Ignore matching itself is budgeted (`ignore::MATCH_BUDGET`); paths it cannot decide
//! are the `ignore_budget` loss.

use std::collections::{BTreeMap, BinaryHeap};
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, mpsc};
use std::time::{Duration, Instant};

use crate::indexcache::{git_timeout, run_git_bounded};

use super::config::IndexConfig;
use super::extract::Lang;
use super::ignore::{DirMemo, Ignore, Verdict, XMUSTARD_IGNORE};
use super::meta::MAX_LOSS_ENTRIES;

/// Candidates with a pre-read loss (symlink, oversized, envelope_bytes...) kept as rows;
/// past this many they are only counted.
pub const MAX_LOSS_ROWS: usize = 2000;
/// `.xmustardignore` files loaded at most.
const MAX_IGNORE_FILES: usize = 1000;
/// Longest listing record parsed (mode, blob, stage and path); longer ones are skipped.
const MAX_RECORD_BYTES: usize = 64 << 10;

#[derive(Debug, Clone)]
pub struct Candidate {
    pub path: String,
    pub lang: Lang,
    /// Stat taken during the scan, before any read (so a concurrent edit makes the next
    /// scan's key differ). Empty for `symlink`, `not_regular` and `unreadable`.
    pub stat: StatKey,
    /// Pre-read problem that makes the file unindexable: `symlink`, `not_regular`,
    /// `unreadable`, `oversized` (above `max_file_size`) or `envelope_bytes`.
    pub loss: Option<&'static str>,
}

/// What `lstat` says about a file; equal keys mean the bytes are assumed unchanged.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StatKey {
    pub size: u64,
    pub mtime_ns: i64,
    pub ctime_ns: i64,
    pub ino: u64,
    pub dev: u64,
}

impl StatKey {
    #[cfg(unix)]
    pub fn of(m: &std::fs::Metadata) -> StatKey {
        use std::os::unix::fs::MetadataExt;
        StatKey {
            size: m.len(),
            mtime_ns: m.mtime() * 1_000_000_000 + m.mtime_nsec(),
            ctime_ns: m.ctime() * 1_000_000_000 + m.ctime_nsec(),
            ino: m.ino(),
            dev: m.dev(),
        }
    }

    #[cfg(not(unix))]
    pub fn of(m: &std::fs::Metadata) -> StatKey {
        let mtime_ns = m
            .modified()
            .ok()
            .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
            .map(|d| d.as_nanos() as i64)
            .unwrap_or(0);
        StatKey {
            size: m.len(),
            mtime_ns,
            ..Default::default()
        }
    }

    /// Stored form (`files.stat_key`).
    pub fn encode(&self) -> String {
        format!(
            "{}:{}:{}:{}:{}",
            self.size, self.mtime_ns, self.ctime_ns, self.ino, self.dev
        )
    }
}

/// Git index mode of a listed path.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Mode {
    Regular,
    Symlink,
    Gitlink,
}

impl Mode {
    fn parse(m: &[u8]) -> Mode {
        match m {
            b"120000" => Mode::Symlink,
            b"160000" => Mode::Gitlink,
            _ => Mode::Regular,
        }
    }
}

/// `lstat` of one listed path: its stat key and pre-read loss, or None when it is gone
/// from the worktree. The key is empty for `symlink`, `not_regular` and `unreadable`.
fn stat_file(
    root: &Path,
    path: &str,
    mode: Mode,
    max_file_size: u64,
) -> Option<(StatKey, Option<&'static str>)> {
    let meta = match std::fs::symlink_metadata(root.join(path)) {
        Ok(m) => m,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return None,
        Err(_) => return Some((StatKey::default(), Some("unreadable"))),
    };
    if mode == Mode::Symlink || meta.file_type().is_symlink() {
        return Some((StatKey::default(), Some("symlink")));
    }
    if mode == Mode::Gitlink || !meta.is_file() {
        return Some((StatKey::default(), Some("not_regular")));
    }
    let stat = StatKey::of(&meta);
    let loss = (stat.size > max_file_size).then_some("oversized");
    Some((stat, loss))
}

/// Classify one listed path by `lstat`: None when it is gone from the worktree.
fn stat_candidate(
    root: &Path,
    path: String,
    lang: Lang,
    mode: Mode,
    max_file_size: u64,
) -> Option<Candidate> {
    let (stat, loss) = stat_file(root, &path, mode, max_file_size)?;
    Some(Candidate {
        path,
        lang,
        stat,
        loss,
    })
}

/// The pre-read loss of a doc on a secret path (`secretpath`): listed, never read.
pub const SECRET_PATH: &str = "secret_path";

/// Tracked docs and guidance files the docs lane indexes at most (the smallest paths).
pub const MAX_DOC_FILES: usize = 2000;
/// Largest doc or guidance file read for the docs lane.
pub const MAX_DOC_BYTES: u64 = 1 << 20;

/// A doc or guidance file (`symbolgraph::repo_role` `doc` or `guide`) for the docs lane
/// (WS-18). Docs are not code candidates: they have no symbols, references or edges,
/// and the code envelope does not count them.
#[derive(Debug, Clone)]
pub struct DocCandidate {
    pub path: String,
    /// `doc` | `guide`
    pub role: &'static str,
    pub stat: StatKey,
    /// Pre-read problem that keeps the file out of the docs lane: `secret_path` or one
    /// of the code candidates' losses.
    pub loss: Option<&'static str>,
}

/// The docs-lane role of a path that no language pack claims.
fn doc_role(path: &str) -> Option<&'static str> {
    match crate::symbolgraph::repo_role(path) {
        role @ ("doc" | "guide") => Some(role),
        _ => None,
    }
}

#[derive(Debug, Clone)]
pub struct Scan {
    /// `git` | `non-git`
    pub repo_mode: &'static str,
    pub head: String,
    /// Where Git keeps this checkout (None outside Git).
    pub git_dir: Option<PathBuf>,
    /// Canonical index root.
    pub root: PathBuf,
    /// Eligible source files inside the envelope and loss rows, sorted by path.
    pub candidates: Vec<Candidate>,
    /// Eligible files past the envelope or the loss-row bound: count per loss reason.
    /// They have no `files` row.
    pub beyond: BTreeMap<String, usize>,
    /// The first of those files by path (at most MAX_LOSS_ENTRIES), with their reason.
    pub beyond_sample: Vec<(String, String)>,
    /// Source files excluded by the ignore rules.
    pub ignored: usize,
    /// Ignore-file lines (and ignore files) skipped by the matcher's bounds.
    pub ignore_rules_dropped: usize,
    /// Files listed but not UTF-8 paths.
    pub invalid_paths: usize,
    /// Tracked files deleted from the worktree (not indexed, not a loss).
    pub worktree_deleted: usize,
    /// Wall clock (ns since the epoch) before any file was stat'ed.
    pub started_ns: i64,
    /// Doc and guidance files for the docs lane, sorted by path (at most MAX_DOC_FILES).
    pub docs: Vec<DocCandidate>,
    /// Doc and guidance files past MAX_DOC_FILES or undecided by the ignore budget.
    pub docs_beyond: usize,
}

fn now_ns() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i64)
        .unwrap_or(0)
}

/// Git blob id of `bytes` (hex SHA-1 of `blob <len>\0<bytes>`): the one content key for
/// clean and dirty files alike.
pub fn blob_id(bytes: &[u8]) -> String {
    use sha1::{Digest, Sha1};
    let mut h = Sha1::new();
    h.update(format!("blob {}\0", bytes.len()).as_bytes());
    h.update(bytes);
    format!("{:x}", h.finalize())
}

/// Where the index root lives: resolved before the index lock is taken, so the lock can
/// be held across the whole scan.
#[derive(Debug, Clone)]
pub struct Layout {
    /// Canonical index root.
    pub root: PathBuf,
    pub git_dir: Option<PathBuf>,
}

pub fn layout(root: &Path, cfg: &IndexConfig) -> Result<Layout, String> {
    let root = std::fs::canonicalize(root).map_err(|e| format!("{}: {e}", root.display()))?;
    match git_layout(&root) {
        Some(git_dir) => Ok(Layout {
            root,
            git_dir: Some(git_dir),
        }),
        None if cfg.allow_non_git => Ok(Layout {
            root,
            git_dir: None,
        }),
        None => Err(format!(
            "{} is not inside a Git work tree (set allow_non_git to index it anyway)",
            root.display()
        )),
    }
}

pub fn scan(layout: &Layout, cfg: &IndexConfig) -> Result<Scan, String> {
    match &layout.git_dir {
        Some(g) => scan_git(&layout.root, g.clone(), cfg),
        None => Ok(scan_walk(&layout.root, cfg)),
    }
}

fn git_layout(root: &Path) -> Option<PathBuf> {
    let out = run_git_bounded(
        root,
        &["rev-parse", "--is-inside-work-tree", "--absolute-git-dir"],
        64 << 10,
        git_timeout(),
    )
    .ok()?;
    let text = String::from_utf8(out).ok()?;
    let mut lines = text.lines();
    if lines.next()? != "true" {
        return None;
    }
    Some(PathBuf::from(lines.next()?))
}

fn head(root: &Path) -> String {
    run_git_bounded(
        root,
        &["rev-parse", "-q", "--verify", "HEAD"],
        64 << 10,
        git_timeout(),
    )
    .ok()
    .and_then(|b| String::from_utf8(b).ok())
    .map(|s| s.trim().to_string())
    .unwrap_or_default()
}

/// Run git and hand each NUL-terminated record of its stdout to `on_record` as it
/// arrives. The output is never held whole. Git is killed once it has run for
/// `git_timeout` not counting the time `on_record` takes (Git only waits on the pipe
/// then), so slow record handling never fails the listing.
fn stream_git_records(
    root: &Path,
    args: &[&str],
    mut on_record: impl FnMut(&[u8]),
) -> Result<(), String> {
    let what = || format!("git {}", args.join(" "));
    let mut child = Command::new("git")
        .arg("--no-optional-locks")
        .arg("-C")
        .arg(root)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| format!("{}: {e}", what()))?;
    let mut stdout = child.stdout.take().expect("stdout is piped");
    let child = Arc::new(Mutex::new(child));
    let (done_tx, done_rx) = mpsc::channel::<()>();
    let timeout = git_timeout();
    // nanoseconds spent in `on_record`, excluded from Git's run time
    let handling = Arc::new(AtomicU64::new(0));
    let watchdog = {
        let child = Arc::clone(&child);
        let handling = Arc::clone(&handling);
        std::thread::spawn(move || {
            let started = Instant::now();
            loop {
                let ran = started
                    .elapsed()
                    .saturating_sub(Duration::from_nanos(handling.load(Ordering::Relaxed)));
                if ran >= timeout {
                    if let Ok(mut c) = child.lock() {
                        let _ = c.kill();
                    }
                    return true;
                }
                match done_rx.recv_timeout((timeout - ran).min(Duration::from_millis(250))) {
                    Err(mpsc::RecvTimeoutError::Timeout) => {}
                    _ => return false,
                }
            }
        })
    };
    let mut buf = vec![0u8; 64 << 10];
    let mut partial: Vec<u8> = Vec::new();
    let mut skipping = false;
    let mut read_error = None;
    loop {
        let n = match stdout.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => n,
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(e) => {
                read_error = Some(e.to_string());
                break;
            }
        };
        let t = Instant::now();
        let mut start = 0;
        for i in 0..n {
            if buf[i] != 0 {
                continue;
            }
            if skipping {
                skipping = false;
            } else if partial.is_empty() {
                if i > start {
                    on_record(&buf[start..i]);
                }
            } else {
                partial.extend_from_slice(&buf[start..i]);
                on_record(&partial);
                partial.clear();
            }
            start = i + 1;
        }
        if start < n && !skipping {
            if partial.len() + (n - start) > MAX_RECORD_BYTES {
                partial.clear();
                skipping = true;
            } else {
                partial.extend_from_slice(&buf[start..n]);
            }
        }
        handling.fetch_add(t.elapsed().as_nanos() as u64, Ordering::Relaxed);
    }
    if !partial.is_empty() && !skipping {
        on_record(&partial);
    }
    drop(stdout);
    let _ = done_tx.send(());
    let timed_out = watchdog.join().unwrap_or(false);
    let status = child.lock().map_err(|_| what())?.wait();
    if timed_out {
        return Err(format!("{}: timed out after {timeout:?}", what()));
    }
    if let Some(e) = read_error {
        return Err(format!("{}: {e}", what()));
    }
    match status {
        Ok(s) if s.success() => Ok(()),
        _ => Err(format!("{}: failed", what())),
    }
}

/// Order candidates by path in the bounded heap (largest on top, evicted first).
struct ByPath(Candidate);

impl PartialEq for ByPath {
    fn eq(&self, o: &Self) -> bool {
        self.0.path == o.0.path
    }
}
impl Eq for ByPath {}
impl PartialOrd for ByPath {
    fn partial_cmp(&self, o: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(o))
    }
}
impl Ord for ByPath {
    fn cmp(&self, o: &Self) -> std::cmp::Ordering {
        self.0.path.cmp(&o.0.path)
    }
}

/// A finished collection.
struct Collected {
    candidates: Vec<Candidate>,
    beyond: BTreeMap<String, usize>,
    beyond_sample: Vec<(String, String)>,
    worktree_deleted: usize,
    docs: Vec<DocCandidate>,
    docs_beyond: usize,
}

/// Order doc candidates by path in their bounded heap (largest on top, evicted first).
struct DocByPath(DocCandidate);

impl PartialEq for DocByPath {
    fn eq(&self, o: &Self) -> bool {
        self.0.path == o.0.path
    }
}
impl Eq for DocByPath {}
impl PartialOrd for DocByPath {
    fn partial_cmp(&self, o: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(o))
    }
}
impl Ord for DocByPath {
    fn cmp(&self, o: &Self) -> std::cmp::Ordering {
        self.0.path.cmp(&o.0.path)
    }
}

/// Collects eligible files in any order with memory bounded by the envelope, then
/// applies the envelope in path order.
struct Collector<'a> {
    root: &'a Path,
    cfg: &'a IndexConfig,
    /// The smallest readable candidates: at most `max_files + MAX_LOSS_ROWS` (the kept
    /// files plus `envelope_bytes` rows).
    readable: BinaryHeap<ByPath>,
    /// The smallest candidates with a pre-read loss: at most `MAX_LOSS_ROWS`.
    lost: BinaryHeap<ByPath>,
    beyond: BTreeMap<String, usize>,
    /// The smallest paths beyond the bound (max-heap of at most MAX_LOSS_ENTRIES).
    sample: BinaryHeap<(String, String)>,
    worktree_deleted: usize,
    /// The smallest doc paths: at most MAX_DOC_FILES.
    docs: BinaryHeap<DocByPath>,
    docs_beyond: usize,
}

impl<'a> Collector<'a> {
    fn new(root: &'a Path, cfg: &'a IndexConfig) -> Self {
        Collector {
            root,
            cfg,
            readable: BinaryHeap::new(),
            lost: BinaryHeap::new(),
            beyond: BTreeMap::new(),
            sample: BinaryHeap::new(),
            worktree_deleted: 0,
            docs: BinaryHeap::new(),
            docs_beyond: 0,
        }
    }

    /// A doc or guidance file for the docs lane; gone files are simply not indexed. A
    /// secret path is listed with the `secret_path` loss, so its bytes are never read.
    fn add_doc(&mut self, path: String, role: &'static str, mode: Mode) {
        let cap = MAX_DOC_BYTES.min(self.cfg.max_file_size);
        let Some((stat, loss)) = stat_file(self.root, &path, mode, cap) else {
            return;
        };
        let loss = crate::secretpath::is_secret_path(&path)
            .then_some(SECRET_PATH)
            .or(loss);
        self.docs.push(DocByPath(DocCandidate {
            path,
            role,
            stat,
            loss,
        }));
        if self.docs.len() > MAX_DOC_FILES {
            self.docs.pop();
            self.docs_beyond += 1;
        }
    }

    fn add(&mut self, path: String, lang: Lang, mode: Mode) {
        match stat_candidate(self.root, path, lang, mode, self.cfg.max_file_size) {
            None => self.worktree_deleted += 1,
            Some(c) => self.push(c),
        }
    }

    fn push(&mut self, c: Candidate) {
        let (heap, cap) = if c.loss.is_none() {
            (
                &mut self.readable,
                self.cfg.max_files.saturating_add(MAX_LOSS_ROWS),
            )
        } else {
            (&mut self.lost, MAX_LOSS_ROWS)
        };
        heap.push(ByPath(c));
        if heap.len() > cap
            && let Some(ByPath(ev)) = heap.pop()
        {
            // `cap` smaller paths of its kind exist, so this one is past any row
            // `finish` keeps.
            let reason = ev.loss.unwrap_or("envelope_files");
            self.push_beyond(ev.path, reason);
        }
    }

    fn push_beyond(&mut self, path: String, reason: &str) {
        *self.beyond.entry(reason.to_string()).or_default() += 1;
        if self.sample.len() < MAX_LOSS_ENTRIES {
            self.sample.push((path, reason.to_string()));
        } else if self.sample.peek().is_some_and(|(p, _)| path < *p) {
            self.sample.pop();
            self.sample.push((path, reason.to_string()));
        }
    }

    fn finish(mut self) -> Collected {
        let mut all: Vec<Candidate> = std::mem::take(&mut self.readable)
            .into_vec()
            .into_iter()
            .chain(std::mem::take(&mut self.lost).into_vec())
            .map(|ByPath(c)| c)
            .collect();
        all.sort_by(|a, b| a.path.cmp(&b.path));
        all.dedup_by(|a, b| a.path == b.path);
        let mut out = Vec::with_capacity(all.len());
        let (mut kept, mut bytes, mut loss_rows, mut cut) = (0usize, 0u64, 0usize, false);
        for mut c in all {
            if cut {
                let reason = c.loss.unwrap_or("envelope_files");
                self.push_beyond(c.path, reason);
                continue;
            }
            if c.loss.is_none() {
                if kept >= self.cfg.max_files {
                    cut = true;
                    self.push_beyond(c.path, "envelope_files");
                    continue;
                }
                if bytes + c.stat.size > self.cfg.max_total_bytes {
                    c.loss = Some("envelope_bytes");
                } else {
                    kept += 1;
                    bytes += c.stat.size;
                    out.push(c);
                    continue;
                }
            }
            if loss_rows < MAX_LOSS_ROWS {
                loss_rows += 1;
                out.push(c);
            } else {
                let reason = c.loss.unwrap_or("envelope_files");
                self.push_beyond(c.path, reason);
            }
        }
        let mut sample = std::mem::take(&mut self.sample).into_vec();
        sample.sort();
        let mut docs: Vec<DocCandidate> = std::mem::take(&mut self.docs)
            .into_vec()
            .into_iter()
            .map(|DocByPath(d)| d)
            .collect();
        docs.sort_by(|a, b| a.path.cmp(&b.path));
        docs.dedup_by(|a, b| a.path == b.path);
        Collected {
            candidates: out,
            beyond: self.beyond,
            beyond_sample: sample,
            worktree_deleted: self.worktree_deleted,
            docs,
            docs_beyond: self.docs_beyond,
        }
    }
}

fn basename(path: &str) -> &str {
    path.rsplit('/').next().unwrap_or(path)
}

fn scan_git(root: &Path, git_dir: PathBuf, cfg: &IndexConfig) -> Result<Scan, String> {
    let started_ns = now_ns();
    // The ignore files first (a small listing), so the main listing is filtered as it
    // streams. Untracked ignore files count only when untracked files are indexed.
    let mut ignore_files: Vec<String> = Vec::new();
    let mut ignore_listings: Vec<&[&str]> = vec![&[
        "--glob-pathspecs",
        "ls-files",
        "-z",
        "--",
        "**/.xmustardignore",
    ]];
    if cfg.include_untracked {
        ignore_listings.push(&[
            "--glob-pathspecs",
            "ls-files",
            "-o",
            "--exclude-standard",
            "-z",
            "--",
            "**/.xmustardignore",
        ]);
    }
    let mut ignore_files_dropped = 0usize;
    for args in ignore_listings {
        stream_git_records(root, args, |rec| {
            if let Ok(p) = std::str::from_utf8(rec)
                && basename(p) == XMUSTARD_IGNORE
            {
                if ignore_files.len() < MAX_IGNORE_FILES {
                    ignore_files.push(p.to_string());
                } else {
                    ignore_files_dropped += 1;
                }
            }
        })?;
    }
    let mut ig = Ignore::with_defaults();
    ig.load_xmustard(root, &ignore_files);

    let mut col = Collector::new(root, cfg);
    let mut memo = DirMemo::default();
    let mut ignored = 0usize;
    let mut invalid_paths = 0usize;
    let mut on_entry = |raw: &[u8], mode: Mode, col: &mut Collector<'_>| {
        let Ok(path) = std::str::from_utf8(raw) else {
            if Lang::for_path(&String::from_utf8_lossy(raw)).is_some() {
                invalid_paths += 1;
            }
            return;
        };
        match (Lang::for_path(path), doc_role(path)) {
            (Some(lang), _) => {
                if admit(&ig, &mut memo, path, &mut ignored, col) {
                    col.add(path.to_string(), lang, mode);
                }
            }
            (None, Some(role)) => admit_doc(&ig, &mut memo, path, role, mode, col),
            (None, None) => {}
        }
    };
    let mut last: Vec<u8> = Vec::new();
    stream_git_records(root, &["ls-files", "-s", "-z"], |rec| {
        // "<mode> <blob> <stage>\t<path>"
        let Some(tab) = rec.iter().position(|b| *b == b'\t') else {
            return;
        };
        let mode = Mode::parse(rec[..tab].split(|b| *b == b' ').next().unwrap_or(b""));
        let raw = &rec[tab + 1..];
        // unmerged paths appear once per stage, consecutively
        if raw == last.as_slice() {
            return;
        }
        last.clear();
        last.extend_from_slice(raw);
        on_entry(raw, mode, &mut col);
    })?;
    if cfg.include_untracked {
        stream_git_records(
            root,
            &["ls-files", "-o", "--exclude-standard", "-z"],
            |raw| on_entry(raw, Mode::Regular, &mut col),
        )?;
    }
    let c = col.finish();
    Ok(Scan {
        repo_mode: "git",
        head: head(root),
        git_dir: Some(git_dir),
        root: root.to_path_buf(),
        candidates: c.candidates,
        beyond: c.beyond,
        beyond_sample: c.beyond_sample,
        ignored,
        ignore_rules_dropped: ig.dropped_rules + ignore_files_dropped,
        invalid_paths,
        worktree_deleted: c.worktree_deleted,
        started_ns,
        docs: c.docs,
        docs_beyond: c.docs_beyond,
    })
}

fn scan_walk(root: &Path, cfg: &IndexConfig) -> Scan {
    let started_ns = now_ns();
    let mut col = Collector::new(root, cfg);
    let mut ignored = 0usize;
    let mut invalid_paths = 0usize;
    let mut ig = Ignore::with_defaults();
    let mut memo = DirMemo::default();
    // .gitignore files are applied top-down as directories are entered.
    let mut it = walkdir::WalkDir::new(root)
        .follow_links(false)
        .sort_by_file_name()
        .into_iter();
    while let Some(entry) = it.next() {
        let Ok(entry) = entry else { continue };
        let Ok(rel) = entry.path().strip_prefix(root) else {
            continue;
        };
        let Some(rel) = rel.to_str().map(|s| s.replace('\\', "/")) else {
            invalid_paths += 1;
            continue;
        };
        if rel.is_empty() {
            load_dir_ignores(root, "", &mut ig);
            continue;
        }
        let ft = entry.file_type();
        if ft.is_dir() {
            // an undecided directory is entered: its files are then reported one by one
            if ig.check(&mut memo, &rel, true) == Verdict::Ignored {
                it.skip_current_dir();
                continue;
            }
            load_dir_ignores(root, &rel, &mut ig);
            continue;
        }
        match (Lang::for_path(&rel), doc_role(&rel)) {
            (Some(lang), _) => {
                if admit(&ig, &mut memo, &rel, &mut ignored, &mut col) {
                    col.add(rel, lang, Mode::Regular);
                }
            }
            (None, Some(role)) => admit_doc(&ig, &mut memo, &rel, role, Mode::Regular, &mut col),
            (None, None) => {}
        }
    }
    let c = col.finish();
    Scan {
        repo_mode: "non-git",
        head: String::new(),
        git_dir: None,
        root: root.to_path_buf(),
        candidates: c.candidates,
        beyond: c.beyond,
        beyond_sample: c.beyond_sample,
        ignored,
        ignore_rules_dropped: ig.dropped_rules,
        invalid_paths,
        worktree_deleted: 0,
        started_ns,
        docs: c.docs,
        docs_beyond: c.docs_beyond,
    }
}

/// Whether the ignore rules admit source file `path`; an ignored file is counted, and a
/// file they cannot decide within the scan's budget is an `ignore_budget` loss.
fn admit(
    ig: &Ignore,
    memo: &mut DirMemo,
    path: &str,
    ignored: &mut usize,
    col: &mut Collector<'_>,
) -> bool {
    match ig.check(memo, path, false) {
        Verdict::Included => true,
        Verdict::Ignored => {
            *ignored += 1;
            false
        }
        Verdict::OverBudget => {
            col.push_beyond(path.to_string(), "ignore_budget");
            false
        }
    }
}

/// Collect doc `path` when the ignore rules include it. Ignored docs are not counted
/// with ignored source files; a doc the rules cannot decide within budget is beyond.
fn admit_doc(
    ig: &Ignore,
    memo: &mut DirMemo,
    path: &str,
    role: &'static str,
    mode: Mode,
    col: &mut Collector<'_>,
) {
    match ig.check(memo, path, false) {
        Verdict::Included => col.add_doc(path.to_string(), role, mode),
        Verdict::Ignored => {}
        Verdict::OverBudget => col.docs_beyond += 1,
    }
}

fn load_dir_ignores(root: &Path, rel_dir: &str, ig: &mut Ignore) {
    for name in [".gitignore", XMUSTARD_IGNORE] {
        let rel = if rel_dir.is_empty() {
            name.to_string()
        } else {
            format!("{rel_dir}/{name}")
        };
        ig.load_file(root, &rel, rel_dir);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blob_id_matches_git() {
        // `printf 'hello\n' | git hash-object --stdin`
        assert_eq!(
            blob_id(b"hello\n"),
            "ce013625030ba8dba906f756967f9e9ca394464a"
        );
        assert_eq!(blob_id(b""), "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391");
    }

    fn cand(path: &str, size: u64, loss: Option<&'static str>) -> Candidate {
        Candidate {
            path: path.into(),
            lang: Lang::TypeScript,
            stat: StatKey {
                size,
                ..Default::default()
            },
            loss,
        }
    }

    #[test]
    fn envelope_is_applied_in_path_order_with_bounded_rows() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = IndexConfig {
            max_files: 3,
            max_total_bytes: 25,
            ..Default::default()
        };
        let mut col = Collector::new(dir.path(), &cfg);
        // arrival order does not matter; c.ts does not fit the byte budget after a.ts.
        for (p, size, loss) in [
            ("e.ts", 1, None),
            ("a.ts", 10, None),
            ("c.ts", 20, None),
            ("b.ts", 0, Some("symlink")),
            ("d.ts", 5, None),
            ("g.ts", 1, None),
            ("f.ts", 1, None),
        ] {
            col.push(cand(p, size, loss));
        }
        let Collected {
            candidates: out,
            beyond,
            beyond_sample: sample,
            ..
        } = col.finish();
        let got: Vec<(&str, Option<&str>)> =
            out.iter().map(|c| (c.path.as_str(), c.loss)).collect();
        assert_eq!(
            got,
            vec![
                ("a.ts", None),
                ("b.ts", Some("symlink")),
                ("c.ts", Some("envelope_bytes")),
                ("d.ts", None),
                ("e.ts", None),
            ]
        );
        assert_eq!(beyond.get("envelope_files"), Some(&2));
        assert_eq!(
            sample,
            vec![
                ("f.ts".to_string(), "envelope_files".to_string()),
                ("g.ts".to_string(), "envelope_files".to_string())
            ]
        );
    }

    #[test]
    fn heap_evicts_the_largest_paths_first() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = IndexConfig {
            max_files: 1,
            ..Default::default()
        };
        let mut col = Collector::new(dir.path(), &cfg);
        // more loss rows than MAX_LOSS_ROWS + max_files, in reverse order
        let n = MAX_LOSS_ROWS + 5;
        for i in (0..n).rev() {
            col.push(cand(&format!("p{i:05}.ts"), 1, Some("symlink")));
        }
        assert_eq!(col.lost.len(), MAX_LOSS_ROWS);
        let Collected {
            candidates: out,
            beyond,
            beyond_sample: sample,
            ..
        } = col.finish();
        assert_eq!(out.len(), MAX_LOSS_ROWS);
        assert_eq!(out[0].path, "p00000.ts");
        assert_eq!(beyond.get("symlink"), Some(&5));
        assert_eq!(sample.len(), 5);
        assert_eq!(sample[0].0, format!("p{:05}.ts", MAX_LOSS_ROWS));
    }

    #[test]
    fn loss_rows_never_displace_readable_files() {
        // more pre-read losses than MAX_LOSS_ROWS sort before every readable file
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = IndexConfig {
            max_files: 60,
            ..Default::default()
        };
        let mut col = Collector::new(dir.path(), &cfg);
        for i in 0..MAX_LOSS_ROWS + 100 {
            col.push(cand(&format!("a{i:05}.ts"), 0, Some("symlink")));
        }
        for i in 0..50 {
            col.push(cand(&format!("z{i:05}.ts"), 10, None));
        }
        let Collected {
            candidates: out,
            beyond,
            ..
        } = col.finish();
        let kept = out.iter().filter(|c| c.loss.is_none()).count();
        assert_eq!(kept, 50, "{beyond:?}");
        assert_eq!(beyond.get("envelope_files"), None);
        assert_eq!(beyond.get("symlink"), Some(&100));
        assert_eq!(out.len(), MAX_LOSS_ROWS + 50);
    }

    #[test]
    fn undecided_paths_are_reported_as_ignore_budget_losses() {
        let dir = tempfile::TempDir::new().unwrap();
        let cfg = IndexConfig::default();
        let mut col = Collector::new(dir.path(), &cfg);
        let mut ig = Ignore::with_defaults();
        ig.add_file("", "*x*y*z*\n");
        let (mut ignored, mut memo) = (0usize, DirMemo::with_budget(0, 0));
        assert!(!admit(&ig, &mut memo, "src/a.ts", &mut ignored, &mut col));
        let mut memo = DirMemo::default();
        assert!(admit(&ig, &mut memo, "src/a.ts", &mut ignored, &mut col));
        assert!(!admit(
            &ig,
            &mut memo,
            "node_modules/b.ts",
            &mut ignored,
            &mut col
        ));
        let Collected {
            beyond,
            beyond_sample: sample,
            ..
        } = col.finish();
        assert_eq!(beyond.get("ignore_budget"), Some(&1));
        assert_eq!(sample, vec![("src/a.ts".into(), "ignore_budget".into())]);
        assert_eq!(ignored, 1);
    }
}
