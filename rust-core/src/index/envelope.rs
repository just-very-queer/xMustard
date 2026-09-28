//! The freshness envelope (PAR-FRESH-05) that search, explain and impact carry: the
//! commit the index reflects, where `HEAD` is now and how the two relate, which result
//! paths changed on disk since they were indexed, the index version and snapshot
//! generation, how old the observation is, and the watcher state.
//!
//! The commit relation costs one or two Git children. The resident service caches it
//! per index for [`TTL`] and recomputes it early when the store's files change (an
//! update commits); concurrent readers of one index share one computation. While the
//! root's watcher is `ok` the relation is kept past the TTL until the watcher sees a
//! Git state change (HEAD, the Git index, refs), so queries spawn no Git (WS-15). Git runs
//! with a [`GIT_TIMEOUT`]; a failure or timeout makes the status `unknown`, never an
//! error (fail open). The dirty-path check reads no Git state: it compares each result
//! path's current size and mtime with the stat the index recorded.

use std::path::Path;
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};

use crate::indexcache::run_git_bounded;

/// How long a commit relation is reused while the store is unchanged.
pub const TTL: Duration = Duration::from_secs(2);
/// Bound on each Git child of the relation (fail open past it).
pub const GIT_TIMEOUT: Duration = Duration::from_secs(5);
/// Result paths checked and listed as dirty at most.
pub const MAX_DIRTY_PATHS: usize = 50;

/// How the indexed commit relates to `HEAD`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    /// `HEAD` is the indexed commit.
    Current,
    /// The indexed commit is an ancestor of `HEAD`, `behind_by` commits back.
    Behind,
    /// Neither contains the other (or `HEAD` moved back past the indexed commit).
    Diverged,
    /// Git failed, timed out, or no commit is recorded.
    Unknown,
}

/// The indexed commit's relation to `HEAD`, observed at `at`.
#[derive(Debug, Clone)]
pub struct Relation {
    pub indexed_commit: String,
    pub head: String,
    pub status: Status,
    pub behind_by: Option<u64>,
    pub at: Instant,
    /// The watcher's Git state epoch when this was observed; None when no watcher
    /// vouched for the root then.
    pub git_epoch: Option<u64>,
}

impl Relation {
    /// Whether this observation still stands: younger than `TTL`, or the root's
    /// watcher has seen no Git state change since it was taken.
    pub fn fresh(&self, root: &Path) -> bool {
        self.at.elapsed() < TTL
            || self
                .git_epoch
                .is_some_and(|e| super::watch::git_epoch(root) == Some(e))
    }
}

fn git_line(root: &Path, args: &[&str]) -> Option<String> {
    let out = run_git_bounded(root, args, 4 << 10, GIT_TIMEOUT).ok()?;
    Some(String::from_utf8_lossy(&out).trim().to_string())
}

/// Relate the indexed commit to the current `HEAD` of `root`.
pub fn relate(root: &Path, indexed_commit: &str) -> Relation {
    // read before Git runs, so a HEAD move during the call invalidates the result
    let git_epoch = super::watch::git_epoch(root);
    let head = git_line(root, &["rev-parse", "-q", "--verify", "HEAD"]);
    let (status, behind_by) = match head.as_deref() {
        None | Some("") if indexed_commit.is_empty() => (Status::Current, None), // both unborn
        None | Some("") => (Status::Unknown, None),
        Some(_) if indexed_commit.is_empty() => (Status::Unknown, None),
        Some(h) if h == indexed_commit => (Status::Current, None),
        Some(h) => ahead_behind(root, indexed_commit, h),
    };
    Relation {
        indexed_commit: indexed_commit.to_string(),
        head: head.unwrap_or_default(),
        status,
        behind_by,
        at: Instant::now(),
        git_epoch,
    }
}

/// `git rev-list --left-right --count indexed...head`: commits only in the indexed
/// commit (left) and only in `HEAD` (right).
fn ahead_behind(root: &Path, indexed: &str, head: &str) -> (Status, Option<u64>) {
    let range = format!("{indexed}...{head}");
    let counts = git_line(root, &["rev-list", "--left-right", "--count", &range]).and_then(|l| {
        let mut it = l.split_whitespace().map(|n| n.parse::<u64>().ok());
        Some((it.next()??, it.next()??))
    });
    match counts {
        Some((0, n)) => (Status::Behind, Some(n)),
        Some(_) => (Status::Diverged, None),
        None => (Status::Unknown, None),
    }
}

/// The envelope attached to one result.
#[derive(Debug, Clone, Serialize)]
pub struct Freshness {
    /// `resident_index` (the serve process's snapshot), `index` (a one-shot read of the
    /// graph segment) or `legacy_graph` (no index: the per-call symbol graph).
    pub source: &'static str,
    pub indexed_commit: String,
    pub head: String,
    pub status: Status,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub behind_by: Option<u64>,
    /// Result paths whose size or mtime differs from what the index recorded (edited
    /// or deleted since indexing): their facts may be stale.
    pub dirty_paths_touching_result: Vec<String>,
    /// More result paths were checked than listed, or more were dirty than listed.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub dirty_paths_truncated: bool,
    pub index_version: String,
    /// The index generation of the snapshot that answered; it grows with every
    /// update, and the resident service swaps snapshots when it changes.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snapshot_generation: Option<i64>,
    /// Age of the commit observation.
    pub identity_age_ms: u64,
    /// The root's watcher in this process (`ok`, `overflow`, `degraded` or `absent`;
    /// see `index::watch::WatcherState`).
    pub watcher_state: &'static str,
    /// The work of the index run that produced the answering generation (PAR-FRESH-04);
    /// absent when the legacy graph answered.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub refresh: Option<RefreshCounters>,
}

/// What the last index run (build or update) did, from its stored counters.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RefreshCounters {
    /// Files parsed and extracted.
    pub reparsed: usize,
    /// Files whose outgoing edges were recomputed.
    pub reresolved: usize,
    /// The write set crossed the escalation gate and the run rebuilt the index whole.
    pub escalated: bool,
}

impl Freshness {
    pub fn from_relation(
        source: &'static str,
        root: &Path,
        rel: &Relation,
        index_version: String,
        generation: Option<i64>,
    ) -> Self {
        Freshness {
            source,
            indexed_commit: rel.indexed_commit.clone(),
            head: rel.head.clone(),
            status: rel.status,
            behind_by: rel.behind_by,
            dirty_paths_touching_result: Vec::new(),
            dirty_paths_truncated: false,
            index_version,
            snapshot_generation: generation,
            identity_age_ms: rel.at.elapsed().as_millis() as u64,
            watcher_state: super::watch::state_of(root).as_str(),
            refresh: None,
        }
    }

    /// Mark the result paths whose stat differs from the indexed one. `indexed` gives a
    /// path's recorded `(size, mtime_ns)`, or None when the index has no row for it.
    pub fn check_paths<'a>(
        &mut self,
        root: &Path,
        paths: impl IntoIterator<Item = &'a str>,
        indexed: impl Fn(&str) -> Option<(u64, i64)>,
    ) {
        let mut seen = std::collections::BTreeSet::new();
        for p in paths {
            if !seen.insert(p) {
                continue;
            }
            if seen.len() > MAX_DIRTY_PATHS {
                self.dirty_paths_truncated = true;
                break;
            }
            let Some(recorded) = indexed(p) else { continue };
            let now = std::fs::symlink_metadata(root.join(p)).ok().map(|m| {
                let k = super::scan::StatKey::of(&m);
                (k.size, k.mtime_ns)
            });
            if now != Some(recorded) {
                self.dirty_paths_touching_result.push(p.to_string());
            }
        }
    }
}
