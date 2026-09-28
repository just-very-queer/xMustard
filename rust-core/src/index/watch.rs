//! The watcher (PAR-FRESH-03) and the freshness loop it drives (PAR-RT-12).
//!
//! Inside `xmustard-core serve`, each registered repository root gets one native
//! watcher (notify: FSEvents on macOS, inotify on Linux) and one thread that turns its
//! events into [`refresh_queue`](super::refresh_queue) changes. The orchestrator (the
//! Go API) runs the refreshes: when a batch is due the service sends it a
//! `$/refresh.due` notification; it takes the batch (`watch take`), runs `index update
//! --paths ...` as heavy work in the governor's heavy slot, and reports the outcome
//! (`watch done`). The service then swaps its resident snapshot to the new generation.
//! A read that finds the index behind its identity takes the batch at once instead of
//! waiting for the debounce.
//!
//! What the watcher watches. The whole root except the directories Git ignores as a
//! whole (`git status --ignored=matching`, once per start and after an ignore file
//! changes): every path `git status` can report is seen, and `node_modules/`, `target/`
//! and the like cost no watch. FSEvents watches the root recursively with one stream;
//! inotify gets one watch per directory (at most [`MAX_WATCH_DIRS`]), added as new
//! directories appear. Inside the Git dir only HEAD, the Git index, refs, `logs/HEAD`,
//! `packed-refs` and `config` count (a Git state change), and `info/exclude` as an
//! ignore file.
//!
//! What an event becomes. A path is a pending refresh path (files over
//! [`MAX_WATCH_FILE_BYTES`] are dropped: the index never reads them). A change to a
//! `.gitignore`, `.xmustardignore` or `info/exclude`, a platform rescan (inotify queue
//! overflow, FSEvents must-scan-subdirs) or a watch error queues a full refresh.
//!
//! Sync. A batch is taken only after a cookie file written into the Git dir comes back
//! as an event: events arrive in order, so every change made before the take is in the
//! batch (the Watchman cookie scheme). Without the cookie (no Git dir, a timeout) the
//! batch says `synced: false` and the orchestrator refreshes the whole tree.
//!
//! `watcher_state` (the freshness envelope's word, see [`WatcherState`]):
//! - `ok`: the native watcher runs and no full refresh is pending;
//! - `overflow`: a full refresh is pending or running (start-up, rescan, overflow,
//!   an ignore file changed), so results may lag until it ends;
//! - `degraded`: the native watcher is down (an OS watch limit such as inotify's
//!   `max_user_watches`, ENOSPC, or another watch error) or refreshes keep failing. A
//!   full refresh is queued, the native watcher is retried with backoff, and the
//!   orchestrator falls back to refreshing on reads, so nothing is silently stale;
//! - `absent`: no watcher for this root in this process.
//!
//! Vouching. While a root's watcher is `ok`, two per-query costs go away: the commit
//! relation of the freshness envelope is reused until a Git state event (no Git spawn
//! per query), and change tracking reuses the last repository state observation until
//! any change under the root (`indexcache::repo_state`: no `git status` and no hashing
//! for ground on an unchanged tree). Both can lag the tree by the platform's event
//! latency (milliseconds).

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU8, AtomicU64, Ordering};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant};

use notify::event::{AccessKind, AccessMode, CreateKind, EventKind};
use notify::{Event, RecommendedWatcher, RecursiveMode, Watcher, WatcherKind};
use serde::Serialize;
use serde_json::{Value, json};

use super::refresh_queue::{Batch, Change, FullReason, RefreshQueue};
use crate::dispatch::{Args, CmdError, CmdResult, json as json_out};
use crate::indexcache::{self, RepoState};

/// Roots watched at once; starting another stops the least recently used.
pub const MAX_WATCHED_ROOTS: usize = 4;
/// Files above this size are not refreshed on their events (the index never reads
/// them; the periodic verification catches a file crossing the line).
pub const MAX_WATCH_FILE_BYTES: u64 = 32 << 20;
/// inotify watches (one per directory) per root. Past it the root is `degraded`, as
/// when the kernel's limit is hit.
pub const MAX_WATCH_DIRS: usize = 32_768;
/// How long a take waits for its cookie.
pub const SYNC_TIMEOUT: Duration = Duration::from_millis(500);
/// Full verification period (same-size edits within a coarse mtime pass a stat check).
pub const VERIFY_EVERY: Duration = Duration::from_secs(3600);
/// First retry of a native watcher that could not be kept; doubles to [`REWATCH_MAX`].
pub const REWATCH_AFTER: Duration = Duration::from_secs(60);
pub const REWATCH_MAX: Duration = Duration::from_secs(30 * 60);
/// A due batch nobody took is announced again after this long.
pub const RENOTIFY_AFTER: Duration = Duration::from_secs(10);
/// Longest sleep of a root's thread (its stop latency).
const TICK: Duration = Duration::from_millis(250);
/// Cookie file name prefix, in the Git dir.
const COOKIE_PREFIX: &str = "xm-watch-cookie-";
/// Ignore files anywhere in the tree.
const IGNORE_FILES: &[&str] = &[".gitignore", ".xmustardignore"];
/// Git dir entries that are Git state (HEAD, the Git index, refs and the origin).
const GIT_STATE_FILES: &[&str] = &["HEAD", "index", "packed-refs", "config", "logs/HEAD"];

/// The watcher state the freshness envelope reports.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum WatcherState {
    Absent = 0,
    Ok = 1,
    Overflow = 2,
    Degraded = 3,
}

impl WatcherState {
    const ALL: [WatcherState; 4] = [Self::Absent, Self::Ok, Self::Overflow, Self::Degraded];

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Absent => "absent",
            Self::Ok => "ok",
            Self::Overflow => "overflow",
            Self::Degraded => "degraded",
        }
    }

    fn from_u8(v: u8) -> Self {
        Self::ALL
            .into_iter()
            .find(|s| *s as u8 == v)
            .unwrap_or(Self::Absent)
    }
}

// ---------------------------------------------------------------------------
// Event classification (pure)
// ---------------------------------------------------------------------------

/// What a path in an event means.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Class {
    /// A root-relative worktree path.
    Path(String),
    /// A Git state file.
    Git,
    /// An ignore file changed.
    IgnoreFile,
    /// This process's cookie number n came back.
    Cookie(u64),
    /// Nothing the index or the identity reads.
    Skip,
}

/// Where a root's files and Git state live.
#[derive(Debug, Clone, Default)]
pub struct Layout {
    /// Canonical root.
    pub root: PathBuf,
    /// Canonical Git dir, and the common dir when it differs (a linked worktree).
    pub git_dirs: Vec<PathBuf>,
    /// Root-relative directories Git ignores as a whole; None when unknown (the
    /// built-in exclusions stand in, and change tracking gets no vouching).
    pub ignored: Option<BTreeSet<String>>,
}

impl Layout {
    fn git_dir(&self) -> Option<&Path> {
        self.git_dirs.first().map(PathBuf::as_path)
    }

    /// Whether the root-relative directory or file `rel` lies in an ignored directory.
    fn ignored(&self, rel: &str) -> bool {
        match &self.ignored {
            Some(set) => ancestors(rel).any(|dir| set.contains(dir)),
            None => super::ignore::is_default_excluded(rel),
        }
    }

    /// Classify an absolute event path.
    pub fn classify(&self, abs: &Path) -> Class {
        for dir in &self.git_dirs {
            if let Ok(rel) = abs.strip_prefix(dir) {
                return git_class(&rel.to_string_lossy());
            }
        }
        let Ok(rel) = abs.strip_prefix(&self.root) else {
            return Class::Skip;
        };
        let rel = rel.to_string_lossy().replace('\\', "/");
        let name = rel.rsplit('/').next().unwrap_or_default();
        match () {
            _ if rel.is_empty() || rel == ".git" || rel.starts_with(".git/") => Class::Skip,
            _ if IGNORE_FILES.contains(&name) => Class::IgnoreFile,
            _ if self.ignored(&rel) => Class::Skip,
            _ => Class::Path(rel),
        }
    }
}

/// `a/b/c` → `a`, `a/b`, `a/b/c`.
fn ancestors(rel: &str) -> impl Iterator<Item = &str> {
    rel.match_indices('/')
        .map(|(i, _)| &rel[..i])
        .chain(std::iter::once(rel))
}

fn git_class(rel: &str) -> Class {
    let rel = rel.replace('\\', "/");
    if let Some(tag) = rel.strip_prefix(COOKIE_PREFIX) {
        let mine = format!("{}-", std::process::id());
        return tag
            .strip_prefix(&mine)
            .and_then(|n| n.parse().ok())
            .map_or(Class::Skip, Class::Cookie);
    }
    match rel.as_str() {
        "info/exclude" => Class::IgnoreFile,
        r if GIT_STATE_FILES.contains(&r) || r.starts_with("refs/") => Class::Git,
        _ => Class::Skip,
    }
}

/// Event kinds that change nothing: opens, reads and closes after reading.
fn inert(kind: &EventKind) -> bool {
    matches!(kind, EventKind::Access(a) if !matches!(a, AccessKind::Close(AccessMode::Write)))
}

// ---------------------------------------------------------------------------
// One watched root
// ---------------------------------------------------------------------------

type Notifier = Arc<dyn Fn(&str, Value) + Send + Sync>;

#[derive(Debug, Clone, Default, Serialize)]
pub struct WatchCounters {
    pub events: u64,
    pub paths: u64,
    pub skipped: u64,
    /// Events for files over `MAX_WATCH_FILE_BYTES`, dropped.
    pub large_skipped: u64,
    pub git_events: u64,
    pub rescans: u64,
    pub errors: u64,
    /// inotify directory watches (0 with a recursive native stream).
    pub dirs_watched: usize,
    pub cookies: u64,
    pub cookie_timeouts: u64,
    /// Repository state observations reused while nothing changed.
    pub memo_hits: u64,
    pub memo_misses: u64,
    pub native_restarts: u64,
}

struct Inner {
    queue: RefreshQueue,
    native: Option<RecommendedWatcher>,
    /// The thread is (re)establishing the native watcher.
    establishing: bool,
    detail: String,
    due_notified: Option<Instant>,
    rewatch_at: Option<Instant>,
    rewatch_failures: u32,
    verify_at: Instant,
    reload_ignored: bool,
    next_cookie: u64,
    cookies_seen: u64,
    memo: Option<(u64, Arc<RepoState>)>,
    last_refresh: Option<Value>,
    counters: WatchCounters,
    last_state: WatcherState,
    /// Watch limit override (tests).
    max_dirs: usize,
}

pub struct Root {
    layout: Mutex<Layout>,
    root: PathBuf,
    tx: Sender<notify::Result<Event>>,
    state: AtomicU8,
    /// Bumped by any change under the root and by Git state changes.
    change_epoch: AtomicU64,
    /// Bumped by Git state changes (and rescans, which may have hidden one).
    git_epoch: AtomicU64,
    stop: AtomicBool,
    inner: Mutex<Inner>,
    cookie_seen: Condvar,
    used: Mutex<Instant>,
    notifier: Option<Notifier>,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// Native watchers that watch a tree recursively with one handle.
fn recursive_native() -> bool {
    RecommendedWatcher::kind() != WatcherKind::Inotify
}

/// Why the native watcher could not be established.
enum NativeError {
    /// The OS watch limit (ENOSPC) or `MAX_WATCH_DIRS`.
    Limit(String),
    Other(String),
}

impl Root {
    fn new(
        root: PathBuf,
        notifier: Option<Notifier>,
        max_dirs: usize,
    ) -> (Arc<Root>, Receiver<notify::Result<Event>>) {
        let (tx, rx) = mpsc::channel();
        let now = Instant::now();
        let mut queue = RefreshQueue::new();
        queue.push(Change::Full(FullReason::Startup), now);
        let r = Arc::new(Root {
            layout: Mutex::new(Layout {
                root: root.clone(),
                ..Default::default()
            }),
            root,
            tx,
            state: AtomicU8::new(WatcherState::Overflow as u8),
            change_epoch: AtomicU64::new(1),
            git_epoch: AtomicU64::new(1),
            stop: AtomicBool::new(false),
            inner: Mutex::new(Inner {
                queue,
                native: None,
                establishing: true,
                detail: "starting: the whole tree is refreshed once".into(),
                due_notified: None,
                rewatch_at: None,
                rewatch_failures: 0,
                verify_at: now + VERIFY_EVERY,
                reload_ignored: false,
                next_cookie: 0,
                cookies_seen: 0,
                memo: None,
                last_refresh: None,
                counters: WatchCounters::default(),
                last_state: WatcherState::Overflow,
                max_dirs,
            }),
            cookie_seen: Condvar::new(),
            used: Mutex::new(now),
            notifier,
        });
        (r, rx)
    }

    pub fn state(&self) -> WatcherState {
        WatcherState::from_u8(self.state.load(Ordering::Acquire))
    }

    /// The watcher is `ok` and sees the Git state (HEAD, the Git index, refs).
    fn vouches_git(&self) -> bool {
        self.state() == WatcherState::Ok && !lock(&self.layout).git_dirs.is_empty()
    }

    fn notify(&self, method: &str, params: Value) {
        if let Some(n) = &self.notifier {
            n(method, params);
        }
    }

    /// Stop watching (the thread ends within a tick) and tell the orchestrator the
    /// root is no longer watched, so its next read registers it again.
    fn retire(&self) {
        self.stop.store(true, Ordering::Release);
        self.notify("$/watch.state", absent_status(&self.root));
    }

    /// The thread body: establish the native watcher, then turn events into changes.
    fn run(self: Arc<Self>, rx: Receiver<notify::Result<Event>>) {
        self.establish(Instant::now());
        while !self.stop.load(Ordering::Acquire) {
            let wait = self.next_wakeup(Instant::now());
            match rx.recv_timeout(wait) {
                Ok(ev) => {
                    self.on_event(ev);
                    while let Ok(ev) = rx.try_recv() {
                        self.on_event(ev);
                    }
                }
                Err(RecvTimeoutError::Timeout) => {}
                Err(RecvTimeoutError::Disconnected) => break,
            }
            self.tick(Instant::now());
        }
        lock(&self.inner).native = None;
    }

    fn next_wakeup(&self, now: Instant) -> Duration {
        let inner = lock(&self.inner);
        [
            inner.queue.due_at(),
            inner.rewatch_at,
            Some(inner.verify_at),
        ]
        .into_iter()
        .flatten()
        .map(|at| at.saturating_duration_since(now))
        .fold(TICK, Duration::min)
    }

    /// Learn the layout and the ignored directories, then start the native watcher.
    fn establish(&self, now: Instant) {
        let layout = observe_layout(&self.root);
        *lock(&self.layout) = layout.clone();
        let max_dirs = lock(&self.inner).max_dirs;
        let outcome = start_native(&layout, self.tx.clone(), max_dirs);
        let mut inner = lock(&self.inner);
        inner.establishing = false;
        match outcome {
            Ok((watcher, dirs)) => {
                inner.native = Some(watcher);
                inner.counters.dirs_watched = dirs;
                inner.rewatch_at = None;
                inner.rewatch_failures = 0;
                inner.detail.clear();
                // anything may have changed while nobody watched
                inner.queue.push(Change::Full(FullReason::Startup), now);
            }
            Err(e) => self.degrade(&mut inner, e, now),
        }
        drop(inner);
        self.bump(true);
        self.publish();
    }

    /// Drop the native watcher, queue a full refresh and schedule a retry.
    fn degrade(&self, inner: &mut Inner, e: NativeError, now: Instant) {
        let (reason, detail) = match e {
            NativeError::Limit(d) => (FullReason::WatchLimit, d),
            NativeError::Other(d) => (FullReason::Rescan, d),
        };
        inner.native = None;
        inner.counters.dirs_watched = 0;
        inner.rewatch_failures += 1;
        let backoff = REWATCH_AFTER
            .saturating_mul(1 << inner.rewatch_failures.saturating_sub(1).min(8))
            .min(REWATCH_MAX);
        inner.rewatch_at = Some(now + backoff);
        inner.detail = format!(
            "{detail}; the whole tree is refreshed instead, reads refresh the index \
             themselves, and the watcher is retried in {} s",
            backoff.as_secs()
        );
        inner.queue.push(Change::Full(reason), now);
    }

    fn bump(&self, git: bool) {
        self.change_epoch.fetch_add(1, Ordering::AcqRel);
        if git {
            self.git_epoch.fetch_add(1, Ordering::AcqRel);
        }
    }

    fn on_event(&self, ev: notify::Result<Event>) {
        let now = Instant::now();
        let ev = match ev {
            Ok(ev) => ev,
            Err(e) => return self.on_error(e, now),
        };
        if ev.need_rescan() {
            let mut inner = lock(&self.inner);
            inner.counters.rescans += 1;
            inner.queue.push(Change::Full(FullReason::Rescan), now);
            drop(inner);
            return self.bump(true);
        }
        if inert(&ev.kind) {
            return;
        }
        let created_dir = matches!(ev.kind, EventKind::Create(CreateKind::Folder))
            || matches!(
                ev.kind,
                EventKind::Modify(notify::event::ModifyKind::Name(_))
            );
        let layout = lock(&self.layout).clone();
        for abs in &ev.paths {
            let class = layout.classify(abs);
            let mut inner = lock(&self.inner);
            inner.counters.events += 1;
            match class {
                Class::Cookie(n) => {
                    inner.cookies_seen = inner.cookies_seen.max(n);
                    self.cookie_seen.notify_all();
                }
                Class::Skip => inner.counters.skipped += 1,
                Class::Git => {
                    inner.counters.git_events += 1;
                    inner.queue.push(Change::Git, now);
                    drop(inner);
                    self.bump(true);
                }
                Class::IgnoreFile => {
                    inner.reload_ignored = true;
                    inner.queue.push(Change::Full(FullReason::IgnoreFile), now);
                    drop(inner);
                    self.bump(false);
                }
                Class::Path(rel) => {
                    let meta = std::fs::symlink_metadata(abs).ok();
                    if meta
                        .as_ref()
                        .is_some_and(|m| m.is_file() && m.len() > MAX_WATCH_FILE_BYTES)
                    {
                        inner.counters.large_skipped += 1;
                    } else {
                        inner.counters.paths += 1;
                        inner.queue.push(Change::Path(rel), now);
                    }
                    let new_dir = created_dir && meta.is_some_and(|m| m.is_dir());
                    drop(inner);
                    self.bump(false);
                    if new_dir && !recursive_native() {
                        self.add_dir(&layout, abs, now);
                    }
                }
            }
        }
    }

    /// inotify: watch a directory that appeared (and its subdirectories), and queue
    /// the files already in it, whose own events came before the watch.
    fn add_dir(&self, layout: &Layout, dir: &Path, now: Instant) {
        let mut inner = lock(&self.inner);
        let max = inner.max_dirs;
        let Some(watcher) = inner.native.as_mut() else {
            return;
        };
        let mut added = 0usize;
        let mut files = Vec::new();
        let mut failure = None;
        for (path, is_dir) in walk_tree(layout, dir) {
            if !is_dir {
                if let Class::Path(rel) = layout.classify(&path) {
                    files.push(rel);
                }
                continue;
            }
            match add_watch(watcher, &path, RecursiveMode::NonRecursive) {
                Ok(()) => added += 1,
                Err(None) => {}
                Err(Some(e)) => {
                    failure = Some(e);
                    break;
                }
            }
        }
        inner.counters.dirs_watched += added;
        for rel in files {
            inner.queue.push(Change::Path(rel), now);
        }
        let over = inner.counters.dirs_watched > max;
        match (failure, over) {
            (Some(e), _) => self.degrade(&mut inner, e, now),
            (None, true) => self.degrade(&mut inner, limit_error(max), now),
            (None, false) => {}
        }
    }

    fn on_error(&self, e: notify::Error, now: Instant) {
        let mut inner = lock(&self.inner);
        inner.counters.errors += 1;
        match e.kind {
            // a directory that vanished before its watch: its removal event follows
            notify::ErrorKind::PathNotFound | notify::ErrorKind::WatchNotFound => {}
            notify::ErrorKind::MaxFilesWatch => {
                let detail = format!("the OS watch limit was reached: {e}");
                self.degrade(&mut inner, NativeError::Limit(detail), now);
            }
            _ => {
                // events may be lost: refresh the whole tree
                inner.queue.push(Change::Full(FullReason::Rescan), now);
                drop(inner);
                self.bump(true);
            }
        }
    }

    fn tick(&self, now: Instant) {
        let mut inner = lock(&self.inner);
        inner.queue.expire(now);
        if now >= inner.verify_at {
            inner.verify_at = now + VERIFY_EVERY;
            inner.queue.push(Change::Full(FullReason::Verify), now);
        }
        let rewatch = inner.native.is_none()
            && !inner.establishing
            && inner.rewatch_at.is_some_and(|at| now >= at);
        let reload = std::mem::take(&mut inner.reload_ignored);
        if rewatch || reload {
            inner.native = None;
            inner.establishing = true;
            inner.counters.native_restarts += u64::from(rewatch);
            drop(inner);
            // an ignore file changed: re-learn the ignored directories and rewatch
            self.establish(now);
            inner = lock(&self.inner);
        }
        let due = inner.queue.is_due(now)
            && inner
                .due_notified
                .is_none_or(|at| now.duration_since(at) >= RENOTIFY_AFTER);
        if due {
            inner.due_notified = Some(now);
        }
        drop(inner);
        if due {
            self.notify("$/refresh.due", self.status());
        }
        self.publish();
    }

    /// Recompute the state (and its detail) and announce a change.
    fn publish(&self) {
        let mut inner = lock(&self.inner);
        let full = inner.queue.full_reason();
        let (state, detail) = match () {
            _ if inner.establishing => (WatcherState::Overflow, None),
            // the detail says why the native watcher is down (see degrade)
            _ if inner.native.is_none() => (WatcherState::Degraded, None),
            _ if inner.queue.gave_up() => (
                WatcherState::Degraded,
                Some(format!(
                    "the last {} refreshes failed; reads refresh the index until the next change",
                    inner.queue.failures()
                )),
            ),
            _ if full.is_some() => (
                WatcherState::Overflow,
                full.map(|r| {
                    format!(
                        "a full refresh is pending ({})",
                        json!(r).as_str().unwrap_or("")
                    )
                }),
            ),
            _ => (WatcherState::Ok, Some(String::new())),
        };
        if let Some(detail) = detail {
            inner.detail = detail;
        }
        self.state.store(state as u8, Ordering::Release);
        let changed = inner.last_state != state;
        inner.last_state = state;
        drop(inner);
        if changed {
            self.notify("$/watch.state", self.status());
        }
    }

    /// Write a cookie into the Git dir and wait until its event comes back, so every
    /// change made before this call has been queued.
    fn sync(&self, timeout: Duration) -> bool {
        let Some(dir) = lock(&self.layout).git_dir().map(Path::to_path_buf) else {
            return false;
        };
        let n = {
            let mut inner = lock(&self.inner);
            if inner.native.is_none() {
                return false;
            }
            inner.next_cookie += 1;
            inner.counters.cookies += 1;
            inner.next_cookie
        };
        let cookie = dir.join(format!("{COOKIE_PREFIX}{}-{n}", std::process::id()));
        if std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&cookie)
            .is_err()
        {
            return false;
        }
        let deadline = Instant::now() + timeout;
        let mut inner = lock(&self.inner);
        while inner.cookies_seen < n {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                break;
            }
            inner = self
                .cookie_seen
                .wait_timeout(inner, left)
                .unwrap_or_else(|e| e.into_inner())
                .0;
        }
        let seen = inner.cookies_seen >= n;
        inner.counters.cookie_timeouts += u64::from(!seen);
        drop(inner);
        let _ = std::fs::remove_file(&cookie);
        seen
    }

    /// Take the next batch (see `Registry::take`).
    fn take(&self, force: bool) -> TakenBatch {
        let synced = self.sync(SYNC_TIMEOUT);
        let now = Instant::now();
        let mut inner = lock(&self.inner);
        let batch = inner.queue.take(now, force);
        if batch.is_some() {
            inner.due_notified = None;
        }
        drop(inner);
        self.publish();
        TakenBatch {
            batch,
            synced,
            state: self.state(),
        }
    }

    fn done(&self, id: u64, ok: bool, report: Option<Value>) -> bool {
        let now = Instant::now();
        let mut inner = lock(&self.inner);
        let known = inner.queue.finish(id, ok, now);
        if known && ok {
            inner.last_refresh = report.map(|r| summarize_refresh(&r));
        }
        drop(inner);
        self.publish();
        known
    }

    pub fn status(&self) -> Value {
        let inner = lock(&self.inner);
        let q = &inner.queue;
        json!({
            "root": self.root.to_string_lossy(),
            "watcher_state": self.state().as_str(),
            "detail": inner.detail,
            "backend": format!("{:?}", RecommendedWatcher::kind()).to_lowercase(),
            "pending": q.pending(),
            "in_flight": q.in_flight(),
            "due": q.is_due(Instant::now()),
            "failures": q.failures(),
            "queue": q.counters,
            "counters": inner.counters,
            "last_refresh": inner.last_refresh,
        })
    }
}

/// The counters of an `index update` report that describe the refresh: reparsed,
/// reresolved and escalated (PAR-FRESH-04), with the mode and why.
fn summarize_refresh(report: &Value) -> Value {
    let c = &report["counters"];
    json!({
        "mode": report["mode"],
        "reason": report["reason"],
        "reparsed": c["reparsed"],
        "reresolved": c["reresolved"],
        "escalated": c["escalated"],
        "deleted": c["deleted"],
        "elapsed_ms": report["timing"]["elapsed_ms"],
    })
}

fn limit_error(max: usize) -> NativeError {
    NativeError::Limit(format!(
        "more than {max} directories to watch (the watch limit per root)"
    ))
}

/// The Git dirs (canonical) and the ignored directories of `root`.
fn observe_layout(root: &Path) -> Layout {
    let mut layout = Layout {
        root: root.to_path_buf(),
        ..Default::default()
    };
    let out = indexcache::run_git_bounded(
        root,
        &[
            "rev-parse",
            "--path-format=absolute",
            "--git-dir",
            "--git-common-dir",
            "--show-prefix",
        ],
        16 << 10,
        indexcache::git_timeout(),
    );
    let Ok(out) = out else {
        return layout; // not a Git worktree: the built-in exclusions apply
    };
    let text = String::from_utf8_lossy(&out);
    let mut lines = text.lines();
    for dir in lines.by_ref().take(2) {
        if let Ok(dir) = std::fs::canonicalize(dir.trim())
            && !layout.git_dirs.contains(&dir)
        {
            layout.git_dirs.push(dir);
        }
    }
    let prefix = lines.next().unwrap_or_default().trim().to_string();
    let status = indexcache::run_git_bounded(
        root,
        &[
            "status",
            "--porcelain=v1",
            "-z",
            "--untracked-files=all",
            "--ignored=matching",
        ],
        indexcache::MAX_GIT_OUTPUT_BYTES,
        indexcache::git_timeout(),
    );
    layout.ignored = status.ok().and_then(|out| {
        let dirs = indexcache::ignored_dir_listing(&indexcache::parse_porcelain_v1_z(&out))?;
        Some(
            dirs.into_iter()
                .filter_map(|d| {
                    d.trim_end_matches('/')
                        .strip_prefix(prefix.as_str())
                        .map(str::to_string)
                })
                .filter(|d| !d.is_empty())
                .collect(),
        )
    });
    layout
}

/// Every directory and file under `from` that the watcher covers: not in a Git dir,
/// not ignored, symlinks not followed. (path, is_dir) pairs, `from` first.
fn walk_tree<'a>(layout: &'a Layout, from: &Path) -> impl Iterator<Item = (PathBuf, bool)> + 'a {
    walkdir::WalkDir::new(from)
        .follow_links(false)
        .into_iter()
        .filter_entry(move |e| {
            let covered = e.depth() == 0 || !matches!(layout.classify(e.path()), Class::Skip);
            covered && e.file_name() != ".git"
        })
        .filter_map(Result::ok)
        .map(|e| {
            let is_dir = e.file_type().is_dir();
            (e.into_path(), is_dir)
        })
}

/// Add one watch; Ok(None) when the directory is gone or unreadable (it cannot be
/// indexed either), Err(Some) when the watcher cannot go on.
fn add_watch(
    w: &mut RecommendedWatcher,
    path: &Path,
    mode: RecursiveMode,
) -> Result<(), Option<NativeError>> {
    match w.watch(path, mode) {
        Ok(()) => Ok(()),
        Err(e) => match e.kind {
            notify::ErrorKind::PathNotFound => Err(None),
            notify::ErrorKind::Io(ref io) if io.kind() == std::io::ErrorKind::PermissionDenied => {
                Err(None)
            }
            notify::ErrorKind::MaxFilesWatch => Err(Some(NativeError::Limit(format!(
                "the OS watch limit was reached (inotify max_user_watches, ENOSPC) at {}",
                path.display()
            )))),
            _ => Err(Some(NativeError::Other(format!(
                "watching {} failed: {e}",
                path.display()
            )))),
        },
    }
}

/// Start the native watcher for `layout`: one recursive stream where the platform has
/// one, else a watch per covered directory plus the Git state directories.
fn start_native(
    layout: &Layout,
    tx: Sender<notify::Result<Event>>,
    max_dirs: usize,
) -> Result<(RecommendedWatcher, usize), NativeError> {
    let mut w = RecommendedWatcher::new(tx, notify::Config::default())
        .map_err(|e| NativeError::Other(format!("the native watcher did not start: {e}")))?;
    let fatal = |e: Option<NativeError>| {
        e.unwrap_or_else(|| {
            NativeError::Other(format!("{} cannot be watched", layout.root.display()))
        })
    };
    if recursive_native() {
        add_watch(&mut w, &layout.root, RecursiveMode::Recursive).map_err(fatal)?;
        for dir in layout
            .git_dirs
            .iter()
            .filter(|d| !d.starts_with(&layout.root))
        {
            add_watch(&mut w, dir, RecursiveMode::Recursive).map_err(fatal)?;
        }
        return Ok((w, 0));
    }
    let mut dirs = 0usize;
    // HEAD, the Git index, packed-refs, config and the cookies in the Git dir itself;
    // logs/HEAD; info/exclude; every refs directory
    let git_state_dirs = layout.git_dirs.iter().flat_map(|g| {
        [g.clone(), g.join("logs"), g.join("info")]
            .into_iter()
            .chain(walkdir_dirs(g.join("refs")))
    });
    let tree = walk_tree(layout, &layout.root)
        .filter(|(_, is_dir)| *is_dir)
        .map(|(p, _)| p);
    for dir in tree.chain(git_state_dirs) {
        match add_watch(&mut w, &dir, RecursiveMode::NonRecursive) {
            Ok(()) => dirs += 1,
            Err(None) if dir != layout.root => {}
            Err(e) => return Err(fatal(e)),
        }
        if dirs > max_dirs {
            return Err(limit_error(max_dirs));
        }
    }
    Ok((w, dirs))
}

fn walkdir_dirs(dir: PathBuf) -> impl Iterator<Item = PathBuf> {
    walkdir::WalkDir::new(dir)
        .follow_links(false)
        .into_iter()
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_dir())
        .map(walkdir::DirEntry::into_path)
}

// ---------------------------------------------------------------------------
// The registry
// ---------------------------------------------------------------------------

/// A batch handed to the orchestrator.
#[derive(Debug, Clone, Serialize)]
pub struct TakenBatch {
    /// None when nothing was due (and the take was not forced).
    pub batch: Option<Batch>,
    /// The batch holds every change made before the take (its cookie came back).
    pub synced: bool,
    pub state: WatcherState,
}

/// The watched roots of this process (`serve` enables it).
pub struct Registry {
    roots: Mutex<Vec<Arc<Root>>>,
    notifier: Mutex<Option<Notifier>>,
    enabled: AtomicBool,
    max_dirs: AtomicU64,
}

static REGISTRY: OnceLock<Registry> = OnceLock::new();

pub fn registry() -> &'static Registry {
    REGISTRY.get_or_init(|| Registry {
        roots: Mutex::new(Vec::new()),
        notifier: Mutex::new(None),
        enabled: AtomicBool::new(false),
        max_dirs: AtomicU64::new(MAX_WATCH_DIRS as u64),
    })
}

/// Turn watching on in this process, with `notifier` sending the service's
/// notifications (`serve` only; the one-shot CLI never watches).
pub fn enable(notifier: impl Fn(&str, Value) + Send + Sync + 'static) {
    let r = registry();
    *lock(&r.notifier) = Some(Arc::new(notifier));
    r.enabled.store(true, Ordering::Release);
}

fn canonical(root: &Path) -> Result<PathBuf, String> {
    let c = std::fs::canonicalize(root).map_err(|e| format!("{}: {e}", root.display()))?;
    if !c.is_dir() {
        return Err(format!("{}: not a directory", c.display()));
    }
    Ok(c)
}

impl Registry {
    /// Watch limit per root (tests lower it to reach the limit path).
    pub fn set_max_dirs(&self, n: usize) {
        self.max_dirs.store(n as u64, Ordering::Release);
    }

    fn find(&self, root: &Path) -> Option<Arc<Root>> {
        let roots = lock(&self.roots);
        let hit = roots
            .iter()
            .find(|r| r.root == root)
            .or_else(|| {
                let c = std::fs::canonicalize(root).ok()?;
                roots.iter().find(|r| r.root == c)
            })?
            .clone();
        *lock(&hit.used) = Instant::now();
        Some(hit)
    }

    /// Watch `root` (idempotent); the least recently used root beyond
    /// `MAX_WATCHED_ROOTS` stops.
    pub fn start(&self, root: &Path) -> Result<Value, String> {
        if !self.enabled.load(Ordering::Acquire) {
            return Err("the watcher runs only inside xmustard-core serve".into());
        }
        let root = canonical(root)?;
        if let Some(r) = self.find(&root) {
            return Ok(r.status());
        }
        let notifier = lock(&self.notifier).clone();
        let max_dirs = self.max_dirs.load(Ordering::Acquire) as usize;
        let (r, rx) = Root::new(root, notifier, max_dirs);
        let evicted = {
            let mut roots = lock(&self.roots);
            roots.push(r.clone());
            let mut evicted = Vec::new();
            while roots.len() > MAX_WATCHED_ROOTS {
                let oldest = roots
                    .iter()
                    .enumerate()
                    .min_by_key(|(_, r)| *lock(&r.used))
                    .map(|(i, _)| i)
                    .expect("non-empty");
                evicted.push(roots.remove(oldest));
            }
            evicted
        };
        evicted.iter().for_each(|r| r.retire());
        let thread = r.clone();
        std::thread::Builder::new()
            .name("xm-watch".into())
            .spawn(move || thread.run(rx))
            .map_err(|e| format!("cannot start the watcher thread: {e}"))?;
        Ok(r.status())
    }

    pub fn stop(&self, root: &Path) -> bool {
        let Ok(root) = canonical(root) else {
            return false;
        };
        let stopped: Vec<Arc<Root>> = {
            let mut roots = lock(&self.roots);
            let (gone, kept) = roots.drain(..).partition(|r| r.root == root);
            *roots = kept;
            gone
        };
        stopped.iter().for_each(|r| r.retire());
        !stopped.is_empty()
    }

    /// Take `root`'s next batch after syncing with the watcher: the due one, or with
    /// `force` whatever is pending (possibly nothing).
    pub fn take(&self, root: &Path, force: bool) -> Result<TakenBatch, String> {
        let r = self
            .find(root)
            .ok_or_else(|| format!("{} is not watched", root.display()))?;
        Ok(r.take(force))
    }

    /// Report batch `id` done. On success the resident index snapshot of the root is
    /// swapped to the new generation before the next read asks for it.
    pub fn done(
        &self,
        root: &Path,
        id: u64,
        ok: bool,
        report: Option<Value>,
    ) -> Result<Value, String> {
        let r = self
            .find(root)
            .ok_or_else(|| format!("{} is not watched", root.display()))?;
        let known = r.done(id, ok, report);
        if known && ok {
            // swap now rather than on the next read; a root whose snapshot is not
            // resident is left alone (loading it could evict another root's)
            if let Some(snapshots) = super::reader::resident() {
                snapshots.reload_if_resident(&r.root);
            }
        }
        let mut status = r.status();
        status["batch_known"] = json!(known);
        Ok(status)
    }

    pub fn status(&self, root: Option<&Path>) -> Value {
        match root {
            Some(root) => self
                .find(root)
                .map(|r| r.status())
                .unwrap_or_else(|| absent_status(root)),
            None => {
                let roots = lock(&self.roots).clone();
                json!({
                    "enabled": self.enabled.load(Ordering::Acquire),
                    "roots": roots.iter().map(|r| r.status()).collect::<Vec<_>>(),
                })
            }
        }
    }

    /// Feed `ev` to `root`'s watcher as if its native watcher had sent it (tests reach
    /// the error paths, such as an OS watch limit, this way). False when not watched.
    #[doc(hidden)]
    pub fn inject(&self, root: &Path, ev: notify::Result<Event>) -> bool {
        self.find(root).is_some_and(|r| r.tx.send(ev).is_ok())
    }

    /// The watcher state of `root` (cheap: the freshness envelope asks per query).
    pub fn state_of(&self, root: &Path) -> WatcherState {
        self.find(root).map_or(WatcherState::Absent, |r| r.state())
    }

    /// The root's Git state epoch while its watcher is `ok` and watches a Git dir:
    /// equal readings bracket a span with no HEAD, Git index or ref change. None when
    /// the watcher cannot vouch.
    pub fn git_epoch(&self, root: &Path) -> Option<u64> {
        let r = self.find(root)?;
        r.vouches_git().then(|| r.git_epoch.load(Ordering::Acquire))
    }
}

fn absent_status(root: &Path) -> Value {
    json!({
        "root": root.to_string_lossy(),
        "watcher_state": WatcherState::Absent.as_str(),
        "detail": "not watched by this process",
    })
}

/// The watcher state for `root`, `absent` outside `serve`.
pub fn state_of(root: &Path) -> WatcherState {
    REGISTRY
        .get()
        .map_or(WatcherState::Absent, |r| r.state_of(root))
}

/// See [`Registry::git_epoch`].
pub fn git_epoch(root: &Path) -> Option<u64> {
    REGISTRY.get()?.git_epoch(root)
}

/// `root`'s repository state: the watcher's last observation while it vouches that
/// nothing changed since (its state is `ok`, it sees every path Git can report, and no
/// event arrived since the observation began), else `observe()`, kept for the next
/// call. The flag says whether the watcher's copy answered.
pub fn memo(root: &Path, observe: impl FnOnce() -> Arc<RepoState>) -> (Arc<RepoState>, bool) {
    let Some(r) = REGISTRY.get().and_then(|reg| reg.find(root)) else {
        return (observe(), false);
    };
    let vouches = r.vouches_git() && (recursive_native() || lock(&r.layout).ignored.is_some());
    let epoch = r.change_epoch.load(Ordering::Acquire);
    {
        let mut inner = lock(&r.inner);
        match &inner.memo {
            Some((at, state)) if vouches && *at == epoch => {
                let state = state.clone();
                inner.counters.memo_hits += 1;
                return (state, true);
            }
            _ => inner.counters.memo_misses += 1,
        }
    }
    let state = observe();
    if vouches {
        lock(&r.inner).memo = Some((epoch, state.clone()));
    }
    (state, false)
}

// ---------------------------------------------------------------------------
// `xmustard-core watch ...`
// ---------------------------------------------------------------------------

const USAGE: &str = "xmustard-core watch <start|stop|take|done|status> [<root>] \
    [--force] [<batch-id> <ok|failed> [<report-json>]]";

/// `watch <start|stop|take|done|status>`: the orchestrator's side of the loop. Runs
/// inside `serve`; one-shot, `status` answers `absent` and the rest fail.
pub fn run(args: Args) -> CmdResult {
    let args: Vec<String> = args.collect();
    let reg = registry();
    let failed = |e: String| CmdError::failed(format!("watch: {e}"));
    let root = args.get(1).map(PathBuf::from);
    let need_root = || root.clone().ok_or_else(|| CmdError::usage(USAGE));
    let out = match args.first().map(String::as_str) {
        Some("start") => reg.start(&need_root()?).map_err(failed)?,
        Some("stop") => json!({ "stopped": reg.stop(&need_root()?) }),
        Some("take") => {
            let force = args.iter().any(|a| a == "--force");
            let taken = reg.take(&need_root()?, force).map_err(failed)?;
            serde_json::to_value(taken).map_err(|e| failed(e.to_string()))?
        }
        Some("done") => {
            let id = args
                .get(2)
                .and_then(|v| v.parse().ok())
                .ok_or_else(|| CmdError::usage(USAGE))?;
            let ok = match args.get(3).map(String::as_str) {
                Some("ok") => true,
                Some("failed") => false,
                _ => return Err(CmdError::usage(USAGE)),
            };
            let report = args.get(4).and_then(|r| serde_json::from_str(r).ok());
            reg.done(&need_root()?, id, ok, report).map_err(failed)?
        }
        Some("status") => reg.status(root.as_deref()),
        _ => return Err(CmdError::usage(USAGE)),
    };
    json_out(&out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn layout(ignored: Option<&[&str]>) -> Layout {
        Layout {
            root: PathBuf::from("/r"),
            git_dirs: vec![PathBuf::from("/r/.git")],
            ignored: ignored.map(|d| d.iter().map(|s| s.to_string()).collect()),
        }
    }

    #[test]
    fn events_are_classified_by_where_they_land() {
        let l = layout(Some(&["node_modules", "out/gen"]));
        let cases: &[(&str, Class)] = &[
            ("/r/src/a.go", Class::Path("src/a.go".into())),
            ("/r/.gitignore", Class::IgnoreFile),
            ("/r/sub/.xmustardignore", Class::IgnoreFile),
            ("/r/node_modules/x/index.js", Class::Skip),
            ("/r/out/gen/a.go", Class::Skip),
            ("/r/out/keep.go", Class::Path("out/keep.go".into())),
            ("/r/.git/HEAD", Class::Git),
            ("/r/.git/index", Class::Git),
            ("/r/.git/refs/heads/main", Class::Git),
            ("/r/.git/logs/HEAD", Class::Git),
            ("/r/.git/info/exclude", Class::IgnoreFile),
            ("/r/.git/objects/ab/cdef", Class::Skip),
            ("/r/.git/index.lock", Class::Skip),
            ("/r/.git/xmustard-cache/index-v3/x/index.db", Class::Skip),
            ("/r", Class::Skip),
            ("/elsewhere/a.go", Class::Skip),
        ];
        for (path, want) in cases {
            assert_eq!(&l.classify(Path::new(path)), want, "{path}");
        }
        let cookie = format!("/r/.git/{COOKIE_PREFIX}{}-7", std::process::id());
        assert_eq!(l.classify(Path::new(&cookie)), Class::Cookie(7));
        let foreign = format!("/r/.git/{COOKIE_PREFIX}1-7");
        if std::process::id() != 1 {
            assert_eq!(l.classify(Path::new(&foreign)), Class::Skip);
        }
    }

    #[test]
    fn without_an_ignored_listing_the_builtin_exclusions_apply() {
        let l = layout(None);
        assert_eq!(l.classify(Path::new("/r/target/debug/x")), Class::Skip);
        assert_eq!(l.classify(Path::new("/r/node_modules/a.js")), Class::Skip);
        assert_eq!(
            l.classify(Path::new("/r/src/a.rs")),
            Class::Path("src/a.rs".into())
        );
    }

    #[test]
    fn reads_and_opens_are_inert() {
        use notify::event::{DataChange, ModifyKind};
        assert!(inert(&EventKind::Access(AccessKind::Open(
            notify::event::AccessMode::Any
        ))));
        assert!(inert(&EventKind::Access(AccessKind::Close(
            AccessMode::Read
        ))));
        assert!(!inert(&EventKind::Access(AccessKind::Close(
            AccessMode::Write
        ))));
        assert!(!inert(&EventKind::Modify(ModifyKind::Data(
            DataChange::Any
        ))));
        assert!(!inert(&EventKind::Any));
    }

    #[test]
    fn watcher_states_round_trip_and_off_words_stay_off() {
        for s in WatcherState::ALL {
            assert_eq!(WatcherState::from_u8(s as u8), s);
        }
        assert_eq!(WatcherState::from_u8(200), WatcherState::Absent);
        assert_eq!(WatcherState::Absent.as_str(), "absent");
    }
}
