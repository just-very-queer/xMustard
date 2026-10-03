//! Per-call pass counters (WS-RT1): how much of the repository one call touched.
//!
//! Six counters, each bumped where the work happens:
//! - `files_listed`: path records a Git listing (`ls-files -z`, `status -z`, the index
//!   scan's streamed listings) or a filesystem walk returned;
//! - `files_statted`: per-file `lstat`/`fstat` of a repository path;
//! - `files_hashed` / `bytes_hashed`: file contents fed through SHA-256 or the Git blob id;
//! - `git_spawns`: Git children started;
//! - `rows_loaded`: stored rows read back (index `files` rows, the resident file table,
//!   stat-cache entries).
//!
//! A call is a `Call` scope on the thread that runs it: the CLI and every `serve` job
//! run inside one (`dispatch::run_counted`). Work on that thread counts into the call
//! and into the process totals; work on a thread outside any call (the watcher) counts
//! into the totals only. A helper thread joins its caller's call through `current` and
//! `within`. Each bump is one relaxed atomic add, or two inside a call.

use std::cell::RefCell;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

use serde::ser::{Serialize, SerializeMap, Serializer};

/// One counted kind of work.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Pass {
    FilesListed,
    FilesStatted,
    FilesHashed,
    BytesHashed,
    GitSpawns,
    RowsLoaded,
}

const KINDS: usize = 6;

/// JSON names, in `Pass` order.
const NAMES: [&str; KINDS] = [
    "files_listed",
    "files_statted",
    "files_hashed",
    "bytes_hashed",
    "git_spawns",
    "rows_loaded",
];

type Cells = [AtomicU64; KINDS];

static TOTALS: Cells = [const { AtomicU64::new(0) }; KINDS];

thread_local! {
    static CALL: RefCell<Option<Arc<Cells>>> = const { RefCell::new(None) };
}

/// Count `n` units of `pass` for the process and for this thread's call, if any.
pub fn note(pass: Pass, n: u64) {
    if n == 0 {
        return;
    }
    TOTALS[pass as usize].fetch_add(n, Ordering::Relaxed);
    CALL.with(|c| {
        if let Some(cells) = c.borrow().as_ref() {
            cells[pass as usize].fetch_add(n, Ordering::Relaxed);
        }
    });
}

/// One file of `bytes` bytes fed through a content hash.
pub fn hashed(bytes: u64) {
    note(Pass::FilesHashed, 1);
    note(Pass::BytesHashed, bytes);
}

/// A snapshot of the six counters.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Counts([u64; KINDS]);

impl Counts {
    fn of(cells: &Cells) -> Counts {
        Counts(std::array::from_fn(|i| cells[i].load(Ordering::Relaxed)))
    }

    pub fn get(&self, pass: Pass) -> u64 {
        self.0[pass as usize]
    }
}

impl Serialize for Counts {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let mut m = s.serialize_map(Some(KINDS))?;
        for (name, n) in NAMES.iter().zip(self.0) {
            m.serialize_entry(name, &n)?;
        }
        m.end()
    }
}

/// What this process counted since it started.
pub fn totals() -> Counts {
    Counts::of(&TOTALS)
}

/// The counting scope of one call on the current thread. A scope opened inside another
/// adds its counts to the outer one when it ends.
pub struct Call {
    cells: Arc<Cells>,
    outer: Option<Arc<Cells>>,
}

impl Call {
    pub fn begin() -> Call {
        let cells = Arc::new([const { AtomicU64::new(0) }; KINDS]);
        let outer = CALL.with(|c| c.borrow_mut().replace(Arc::clone(&cells)));
        Call { cells, outer }
    }

    /// What the call has counted so far.
    pub fn counts(&self) -> Counts {
        Counts::of(&self.cells)
    }
}

impl Drop for Call {
    fn drop(&mut self) {
        if let Some(outer) = &self.outer {
            for (o, c) in outer.iter().zip(self.cells.iter()) {
                o.fetch_add(c.load(Ordering::Relaxed), Ordering::Relaxed);
            }
        }
        let outer = self.outer.take();
        CALL.with(|c| *c.borrow_mut() = outer);
    }
}

/// The current thread's call, for a helper thread to count into.
pub struct Handle(Option<Arc<Cells>>);

pub fn current() -> Handle {
    Handle(CALL.with(|c| c.borrow().clone()))
}

/// Run `f` on this thread counting into the call `h` came from.
pub fn within<T>(h: Handle, f: impl FnOnce() -> T) -> T {
    let prev = CALL.with(|c| std::mem::replace(&mut *c.borrow_mut(), h.0));
    let out = f();
    CALL.with(|c| *c.borrow_mut() = prev);
    out
}

/// `body` with `"pass_counters":<counts>` appended as its last field when it is a JSON
/// object; any other document is returned unchanged. The object is edited in place, so
/// a large result is not copied.
pub fn attach(mut body: String, counts: &Counts) -> String {
    let doc = body.trim_end();
    if !(doc.starts_with('{') && doc.ends_with('}')) {
        return body;
    }
    let fields = doc[..doc.len() - 1].trim_end();
    let (keep, empty) = (fields.len(), fields.ends_with('{'));
    body.truncate(keep);
    if !empty {
        body.push(',');
    }
    body.push_str("\"pass_counters\":");
    body.push_str(&serde_json::to_string(counts).expect("counts serialize"));
    body.push('}');
    body
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_call_counts_only_its_own_threads_work() {
        let call = Call::begin();
        note(Pass::FilesListed, 3);
        std::thread::spawn(|| note(Pass::FilesListed, 100))
            .join()
            .unwrap();
        assert_eq!(call.counts().get(Pass::FilesListed), 3);
    }

    #[test]
    fn a_helper_thread_counts_into_the_call_it_was_handed() {
        let call = Call::begin();
        let h = current();
        std::thread::scope(|s| {
            s.spawn(|| within(h, || note(Pass::GitSpawns, 2)));
        });
        assert_eq!(call.counts().get(Pass::GitSpawns), 2);
    }

    #[test]
    fn a_nested_call_adds_into_its_parent_and_restores_it() {
        let outer = Call::begin();
        note(Pass::RowsLoaded, 1);
        {
            let inner = Call::begin();
            note(Pass::RowsLoaded, 4);
            assert_eq!(inner.counts().get(Pass::RowsLoaded), 4);
            assert_eq!(outer.counts().get(Pass::RowsLoaded), 1);
        }
        note(Pass::RowsLoaded, 10);
        assert_eq!(outer.counts().get(Pass::RowsLoaded), 15);
    }

    #[test]
    fn work_outside_a_call_reaches_only_the_totals() {
        let before = totals().get(Pass::BytesHashed);
        std::thread::spawn(|| hashed(7)).join().unwrap();
        assert!(totals().get(Pass::BytesHashed) >= before + 7);
        let call = Call::begin();
        assert_eq!(call.counts(), Counts::default());
    }

    #[test]
    fn hashing_one_file_counts_the_file_and_its_bytes() {
        let call = Call::begin();
        hashed(12);
        hashed(0);
        assert_eq!(call.counts().get(Pass::FilesHashed), 2);
        assert_eq!(call.counts().get(Pass::BytesHashed), 12);
    }

    #[test]
    fn attach_appends_the_counts_as_the_last_field() {
        let c = Counts([1, 2, 3, 4, 5, 6]);
        let tail = "\"pass_counters\":{\"files_listed\":1,\"files_statted\":2,\"files_hashed\":3,\"bytes_hashed\":4,\"git_spawns\":5,\"rows_loaded\":6}}";
        assert_eq!(
            attach("{\"a\":{}}".into(), &c),
            format!("{{\"a\":{{}},{tail}")
        );
        assert_eq!(attach("{}".into(), &c), format!("{{{tail}"));
        assert_eq!(attach("{ }\n".into(), &c), format!("{{{tail}"));
        for v in ["[1,{}]", "\"x\"", "3"] {
            assert_eq!(attach(v.into(), &c), v);
        }
        let mut roomy = String::with_capacity(4096);
        roomy.push_str("{\"a\":1}");
        let at = roomy.as_ptr();
        assert_eq!(
            attach(roomy, &c).as_ptr(),
            at,
            "the result is edited in place"
        );
        let parsed: serde_json::Value =
            serde_json::from_str(&attach("{\"a\":[1]}".into(), &c)).unwrap();
        assert_eq!(parsed["pass_counters"]["rows_loaded"], 6);
        assert_eq!(parsed["a"][0], 1);
    }
}
