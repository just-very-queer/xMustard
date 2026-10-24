//! The exact scan's heap is bounded by its caps, measured on production code: one file's
//! bytes, the lazy DFA's caches and the listed spans, never an entry per line
//! (batch43 #1, batch51 #6). A 4 MiB file of newlines would cost about 32 MiB of line
//! starts if positions were looked up rather than counted.
//!
//! The counting allocator is test code only (the library keeps `#![forbid(unsafe_code)]`),
//! and this is the only test in its binary, so no other test's allocations reach the peak.
//! The default `realloc` counts both buffers while it copies, which is one reason the scan
//! reserves its buffer once.

use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicUsize, Ordering};

use xmustard_core::exactscan::{
    DEFAULT_MAX_BYTES, Deadline, FileScan, Limits, MAX_CURSOR_LINES, Matcher, Report, compile,
};

struct Counting;

static LIVE: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);

unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { System.alloc(layout) };
        if !ptr.is_null() {
            let live = LIVE.fetch_add(layout.size(), Ordering::SeqCst) + layout.size();
            PEAK.fetch_max(live, Ordering::SeqCst);
        }
        ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) };
        LIVE.fetch_sub(layout.size(), Ordering::SeqCst);
    }
}

#[global_allocator]
static ALLOCATOR: Counting = Counting;

#[test]
fn a_dense_newline_file_is_scanned_within_its_byte_cap_and_caches() {
    let td = tempfile::tempdir().unwrap();
    let mut body = vec![b'\n'; DEFAULT_MAX_BYTES as usize - 6];
    body.extend_from_slice(b"needle");
    std::fs::write(td.path().join("a.txt"), &body).unwrap();
    drop(body);
    for (query, matcher) in [("needle", Matcher::Exact), ("ne+dle", Matcher::Regex)] {
        let pattern = compile(query, matcher).unwrap();
        let mut sc = pattern.scanner();
        let mut fs = FileScan::open(td.path(), "a.txt").unwrap();
        let limits = Limits {
            max_lines: 1,
            ..Limits::default()
        };
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let mut deadline = Deadline::never();

        let base = LIVE.load(Ordering::SeqCst);
        PEAK.store(base, Ordering::SeqCst);
        fs.scan(
            &mut sc,
            &limits,
            &mut budget,
            &mut report,
            &mut deadline,
            false,
        );
        let growth = PEAK.load(Ordering::SeqCst) - base;

        let span = &report.spans[..];
        assert_eq!(span.len(), 1, "{query}: {report:?}");
        assert_eq!(
            (span[0].line, span[0].column),
            (DEFAULT_MAX_BYTES as usize - 5, 1),
            "{query}"
        );
        assert_eq!(report.cut, None, "{query}");
        let bound = DEFAULT_MAX_BYTES as usize
            + MAX_CURSOR_LINES * size_of::<usize>()
            + sc.memory_usage()
            + (64 << 10);
        assert!(
            growth <= bound,
            "{query}: the scan's heap grew {growth} bytes, past {bound} (the byte cap, \
             {MAX_CURSOR_LINES} line entries, {} bytes of DFA caches and 64 KiB)",
            sc.memory_usage()
        );
    }
}
