//! The serialized, debounced refresh queue behind the watcher (PAR-FRESH-03).
//!
//! The watcher pushes what it saw: a changed path, a Git state change (HEAD, the Git
//! index, refs) or a reason to refresh the whole tree. The queue coalesces them into
//! one batch that becomes due [`DEBOUNCE`] after the last change, and no later than
//! [`MAX_WAIT`] after the first, so a steady stream of edits still refreshes at least
//! that often. More than [`MAX_PENDING_PATHS`] pending paths turn the batch into a full
//! refresh (overflow).
//!
//! Refreshes never overlap: while a batch is in flight nothing else is due, and changes
//! keep accumulating for the next one. A failed batch goes back into the queue and is
//! retried with exponential backoff; after [`MAX_RETRIES`] consecutive failures the
//! queue stops scheduling (the watcher then reports `degraded` and reads refresh the
//! index themselves) until a new change arrives.
//!
//! The queue is pure: every method takes the clock reading, so tests drive time.

use std::collections::BTreeSet;
use std::time::{Duration, Instant};

use serde::Serialize;

/// Quiet period after the last change before a batch is due.
pub const DEBOUNCE: Duration = Duration::from_millis(300);
/// A batch is due at most this long after its first change, however busy the tree.
pub const MAX_WAIT: Duration = Duration::from_secs(1);
/// Pending paths kept at most; past them the batch refreshes the whole tree.
pub const MAX_PENDING_PATHS: usize = 1000;
/// First retry delay after a failed batch; doubles per consecutive failure.
pub const RETRY_BASE: Duration = Duration::from_millis(500);
/// Longest retry delay.
pub const RETRY_MAX: Duration = Duration::from_secs(30);
/// Consecutive failures after which the queue stops retrying until the next change.
pub const MAX_RETRIES: u32 = 6;
/// A batch in flight longer than this is treated as failed (its refresh was lost).
/// Longer than the orchestrator's bound on one index update (10 minutes).
pub const IN_FLIGHT_LIMIT: Duration = Duration::from_secs(11 * 60);

/// Why a batch refreshes the whole tree instead of the paths it names.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum FullReason {
    /// The watcher (re)started: changes while nobody watched are unknown.
    Startup,
    /// More than `MAX_PENDING_PATHS` paths were pending.
    Overflow,
    /// The platform dropped or coalesced events (inotify queue overflow, FSEvents
    /// must-scan-subdirs, a watch error).
    Rescan,
    /// A `.gitignore`, `.xmustardignore` or `info/exclude` changed what is eligible.
    IgnoreFile,
    /// The native watcher could not be kept (an OS watch limit such as inotify's
    /// `max_user_watches`, ENOSPC): the tree is refreshed whole instead.
    WatchLimit,
    /// Periodic full verification: same-size edits within a coarse mtime and
    /// preserved mtimes pass a stat check, so the whole tree is re-checked now and then.
    Verify,
}

/// One change the watcher saw.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Change {
    /// A root-relative path was created, modified, renamed or removed.
    Path(String),
    /// HEAD, the Git index or a ref moved.
    Git,
    /// The whole tree must be refreshed.
    Full(FullReason),
}

/// What one refresh covers.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Batch {
    pub id: u64,
    /// Changed paths, sorted; empty for a full refresh.
    pub paths: Vec<String>,
    /// Set when the whole tree is refreshed, with the first reason.
    pub full: Option<FullReason>,
    /// HEAD, the Git index or a ref moved.
    pub git: bool,
}

#[derive(Debug, Clone, Default, Serialize)]
pub struct QueueCounters {
    /// Changes pushed.
    pub changes: u64,
    /// Changes that named a path already pending (or pending in a full refresh).
    pub coalesced: u64,
    /// Batches taken.
    pub batches: u64,
    /// Batches that refreshed the whole tree.
    pub full_refreshes: u64,
    /// Batches turned full because too many paths were pending.
    pub overflows: u64,
    pub failures: u64,
    /// Failed batches scheduled again.
    pub retries: u64,
    /// Times the queue stopped retrying after `MAX_RETRIES` failures.
    pub gave_up: u64,
    /// In-flight batches taken over by a newer take (their caller lost them).
    pub superseded: u64,
}

#[derive(Debug, Default)]
pub struct RefreshQueue {
    paths: BTreeSet<String>,
    full: Option<FullReason>,
    git: bool,
    first: Option<Instant>,
    last: Option<Instant>,
    in_flight: Option<(Batch, Instant)>,
    next_id: u64,
    failures: u32,
    retry_at: Option<Instant>,
    pub counters: QueueCounters,
}

/// Delay before retry number `failures` (1-based).
pub fn backoff(failures: u32) -> Duration {
    let shift = failures.saturating_sub(1).min(16);
    RETRY_BASE.saturating_mul(1 << shift).min(RETRY_MAX)
}

impl RefreshQueue {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn push(&mut self, change: Change, now: Instant) {
        self.counters.changes += 1;
        match change {
            Change::Path(p) if self.full.is_none() => {
                if !self.paths.insert(p) {
                    self.counters.coalesced += 1;
                }
                if self.paths.len() > MAX_PENDING_PATHS {
                    self.counters.overflows += 1;
                    self.escalate(FullReason::Overflow);
                }
            }
            Change::Path(_) => self.counters.coalesced += 1,
            Change::Git => self.git = true,
            Change::Full(reason) => self.escalate(reason),
        }
        self.first.get_or_insert(now);
        self.last = Some(now);
        if self.gave_up() {
            // a new change is a new chance: retries start over
            self.failures = 0;
            self.retry_at = None;
        }
    }

    fn escalate(&mut self, reason: FullReason) {
        self.full.get_or_insert(reason);
        self.paths.clear();
    }

    /// Changes are waiting for a batch.
    pub fn pending(&self) -> bool {
        self.first.is_some()
    }

    /// Why a full refresh is pending or in flight, if one is.
    pub fn full_reason(&self) -> Option<FullReason> {
        self.full
            .or_else(|| self.in_flight.as_ref().and_then(|(b, _)| b.full))
    }

    /// A full refresh is pending or in flight.
    pub fn full_pending(&self) -> bool {
        self.full_reason().is_some()
    }

    pub fn in_flight(&self) -> bool {
        self.in_flight.is_some()
    }

    /// Consecutive failures reached `MAX_RETRIES`: nothing is scheduled until a change.
    pub fn gave_up(&self) -> bool {
        self.failures >= MAX_RETRIES
    }

    pub fn failures(&self) -> u32 {
        self.failures
    }

    /// When the pending changes become due: `DEBOUNCE` after the last change and at
    /// most `MAX_WAIT` after the first, not before a retry backoff ends. None while a
    /// batch is in flight, when nothing is pending, or after giving up.
    pub fn due_at(&self) -> Option<Instant> {
        if self.in_flight.is_some() || self.gave_up() {
            return None;
        }
        let due = (self.last? + DEBOUNCE).min(self.first? + MAX_WAIT);
        Some(self.retry_at.map_or(due, |r| due.max(r)))
    }

    pub fn is_due(&self, now: Instant) -> bool {
        self.due_at().is_some_and(|at| at <= now)
    }

    /// Take the pending changes as the next batch: when due (never while a batch is in
    /// flight), or at once with `force`, for a read that will not wait for the
    /// debounce (the batch may then be empty). The orchestrator runs one refresh per
    /// root at a time, so a forced take while a batch is in flight means that batch was
    /// lost: it is superseded and its changes merged back. None when not forced and
    /// nothing is due.
    pub fn take(&mut self, now: Instant, force: bool) -> Option<Batch> {
        if !force && !self.is_due(now) {
            return None;
        }
        if let Some((stale, _)) = self.in_flight.take() {
            self.counters.superseded += 1;
            self.merge_back(stale, now);
        }
        self.next_id += 1;
        let batch = Batch {
            id: self.next_id,
            paths: std::mem::take(&mut self.paths).into_iter().collect(),
            full: self.full.take(),
            git: std::mem::take(&mut self.git),
        };
        self.first = None;
        self.last = None;
        self.counters.batches += 1;
        self.counters.full_refreshes += u64::from(batch.full.is_some());
        self.in_flight = Some((batch.clone(), now));
        Some(batch)
    }

    /// End batch `id`. A success clears the failure count; a failure puts its changes
    /// back and schedules a retry after `backoff`. Returns false for a batch that is
    /// not the one in flight (superseded or unknown), which changes nothing.
    pub fn finish(&mut self, id: u64, ok: bool, now: Instant) -> bool {
        if self.in_flight.as_ref().is_none_or(|(b, _)| b.id != id) {
            return false;
        }
        let (batch, _) = self.in_flight.take().expect("checked above");
        if ok {
            self.failures = 0;
            self.retry_at = None;
            return true;
        }
        self.counters.failures += 1;
        self.failures += 1;
        self.merge_back(batch, now);
        if self.gave_up() {
            self.counters.gave_up += 1;
            self.retry_at = None;
        } else {
            self.counters.retries += 1;
            self.retry_at = Some(now + backoff(self.failures));
        }
        true
    }

    /// Fail a batch in flight longer than `IN_FLIGHT_LIMIT`; returns whether one was.
    pub fn expire(&mut self, now: Instant) -> bool {
        let stale = self
            .in_flight
            .as_ref()
            .filter(|(_, since)| now.duration_since(*since) >= IN_FLIGHT_LIMIT)
            .map(|(b, _)| b.id);
        stale.is_some_and(|id| self.finish(id, false, now))
    }

    /// Put a batch's changes back in the queue (without counting them again).
    fn merge_back(&mut self, batch: Batch, now: Instant) {
        match batch.full {
            Some(reason) => self.escalate(reason),
            None if self.full.is_none() => {
                self.paths.extend(batch.paths);
                if self.paths.len() > MAX_PENDING_PATHS {
                    self.counters.overflows += 1;
                    self.escalate(FullReason::Overflow);
                }
            }
            None => {}
        }
        self.git |= batch.git;
        self.first.get_or_insert(now);
        self.last.get_or_insert(now);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ms(n: u64) -> Duration {
        Duration::from_millis(n)
    }

    fn path(p: &str) -> Change {
        Change::Path(p.to_string())
    }

    #[test]
    fn a_burst_is_one_batch_after_the_quiet_period() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        for (i, p) in ["a.go", "b.go", "a.go", "c.go"].iter().enumerate() {
            q.push(path(p), t0 + ms(50 * i as u64));
        }
        let last = t0 + ms(150);
        assert!(q.take(last + ms(299), false).is_none());
        assert_eq!(q.due_at(), Some(last + DEBOUNCE));
        let b = q.take(last + DEBOUNCE, false).unwrap();
        assert_eq!(b.paths, ["a.go", "b.go", "c.go"]);
        assert_eq!((b.full, b.git), (None, false));
        assert_eq!(q.counters.coalesced, 1);
        assert!(!q.pending());
    }

    #[test]
    fn a_steady_stream_is_due_at_the_max_wait() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        let mut t = t0;
        while t < t0 + ms(1500) {
            q.push(path("hot.go"), t);
            t += ms(100);
        }
        assert_eq!(q.due_at(), Some(t0 + MAX_WAIT));
    }

    #[test]
    fn overflow_ignore_files_and_rescans_refresh_the_whole_tree() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        for i in 0..=MAX_PENDING_PATHS {
            q.push(Change::Path(format!("f{i}.go")), t0);
        }
        let b = q.take(t0 + DEBOUNCE, false).unwrap();
        assert_eq!(b.full, Some(FullReason::Overflow));
        assert!(b.paths.is_empty());
        assert_eq!(q.counters.overflows, 1);
        assert!(q.finish(b.id, true, t0 + DEBOUNCE));

        // the first reason stays; later paths are absorbed
        q.push(Change::Full(FullReason::IgnoreFile), t0);
        q.push(path("x.go"), t0);
        q.push(Change::Full(FullReason::Rescan), t0);
        q.push(Change::Git, t0);
        let b = q.take(t0 + DEBOUNCE, false).unwrap();
        assert_eq!(b.full, Some(FullReason::IgnoreFile));
        assert!(b.git && b.paths.is_empty());
        assert_eq!(q.counters.full_refreshes, 2);
    }

    #[test]
    fn batches_never_overlap() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        q.push(path("a.go"), t0);
        let b1 = q.take(t0 + DEBOUNCE, false).unwrap();
        q.push(path("b.go"), t0 + DEBOUNCE);
        // nothing is due while b1 is in flight, however long it runs
        assert_eq!(q.due_at(), None);
        assert!(q.take(t0 + ms(5000), false).is_none());
        assert!(q.finish(b1.id, true, t0 + ms(5000)));
        let b2 = q.take(t0 + ms(5000), false).unwrap();
        assert_eq!(b2.paths, ["b.go"]);
    }

    #[test]
    fn a_failure_is_retried_with_exponential_backoff_then_given_up() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        q.push(path("a.go"), t0);
        let mut now = t0 + DEBOUNCE;
        let mut delays = Vec::new();
        for _ in 0..MAX_RETRIES {
            let b = q.take(now, false).expect("due");
            assert_eq!(b.paths, ["a.go"], "the failed paths come back");
            assert!(q.finish(b.id, false, now));
            if let Some(at) = q.due_at() {
                delays.push(at - now);
                now = at;
            }
        }
        assert_eq!(
            delays,
            [ms(500), ms(1000), ms(2000), ms(4000), ms(8000)],
            "doubling from RETRY_BASE"
        );
        assert!(q.gave_up());
        assert_eq!(q.due_at(), None);
        assert_eq!(q.counters.gave_up, 1);
        // a new change resumes scheduling
        q.push(path("b.go"), now);
        assert!(!q.gave_up());
        assert_eq!(
            q.take(now + DEBOUNCE, false).unwrap().paths,
            ["a.go", "b.go"]
        );
        assert_eq!(backoff(40), RETRY_MAX);
    }

    #[test]
    fn a_forced_take_skips_the_debounce_and_may_be_empty() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        let empty = q.take(t0, true).unwrap();
        assert!(empty.paths.is_empty() && empty.full.is_none());
        assert!(q.finish(empty.id, true, t0));
        q.push(path("a.go"), t0);
        assert_eq!(q.take(t0 + ms(1), true).unwrap().paths, ["a.go"]);
    }

    #[test]
    fn a_lost_batch_is_superseded_or_expired_and_its_changes_kept() {
        let t0 = Instant::now();
        let mut q = RefreshQueue::new();
        q.push(path("a.go"), t0);
        let lost = q.take(t0 + DEBOUNCE, false).unwrap();
        q.push(path("b.go"), t0 + DEBOUNCE);
        let b = q.take(t0 + ms(400), true).unwrap();
        assert_eq!(b.paths, ["a.go", "b.go"]);
        assert_eq!(q.counters.superseded, 1);
        // the superseded batch's late answer changes nothing
        assert!(!q.finish(lost.id, true, t0 + ms(500)));
        assert!(q.in_flight());
        assert!(!q.expire(t0 + ms(500)));
        assert!(q.expire(t0 + ms(400) + IN_FLIGHT_LIMIT));
        assert_eq!(
            q.take(t0 + IN_FLIGHT_LIMIT * 2, false).unwrap().paths,
            ["a.go", "b.go"]
        );
    }
}
