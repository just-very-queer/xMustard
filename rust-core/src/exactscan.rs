//! The `mode=exact|regex` scan lane (WS-59c, PAR-RET-09).
//!
//! The hybrid and text lanes rank indexed chunks. This lane reads the working tree's
//! bytes through the same no-follow fd-walk every other repo read uses, and matches them
//! against the query as written, so a caller looking for a literal string or a pattern
//! gets the occurrences rather than a ranking.
//!
//! Three independent caps bound it, and each is reported rather than implied: the lines
//! the answer may list, the bytes the scan may read, and the wall-clock it may spend. A cap
//! that fires sets `cut` with its name, so a partial answer never reads as a complete one.
//!
//! Nothing proportional to the corpus is held. Files are read in `CHUNK_BYTES` slices
//! through a positional read at an explicit offset, so a corpus-resident map is never built.
//! The scan retains one slice and the ordinary carry tail. An incomplete boundary match is
//! deferred only through `MAX_MATCH_BYTES`; a longer match gets a capped span and a visible
//! degradation rather than unbounded carry.

use regex::bytes::{Regex, RegexBuilder};
use serde::Serialize;

use crate::symbolgraph::MAX_REPO_FILE_BYTES;

/// The line cap's default and its ceiling: enough to see every occurrence in a small
/// repository, few enough that one answer stays inside the output budget. A caller lowers
/// it and never raises it past this.
pub const DEFAULT_MAX_LINES: usize = 200;

/// The byte cap's default and its ceiling (4 MiB). This is the bound that keeps the lane
/// off the budget line: the scan reads what it reads, so a tree it cannot finish is
/// reported as unfinished instead of being loaded.
pub const DEFAULT_MAX_BYTES: u64 = 4 << 20;

/// The time cap's default and its ceiling (2 s): the same scale as `impact`'s
/// `DEFAULT_TIMEOUT_MS`, and far below the core-call timeout the Go side applies.
pub const DEFAULT_TIMEOUT_MS: u64 = 2_000;

/// `DFA_CACHE_BYTES` of `index::lexical`, reused rather than re-declared: it is the
/// crate's existing bound on regex automaton memory, and `lexical.rs` records why it
/// exists (Unicode classes compile to automata of several MiB). One number to reason
/// about, not two that drift.
const DFA_CACHE_BYTES: usize = 64 << 10;

/// Bytes read per positional read. Also the granularity of the time cap's check, which is
/// why it is small enough to interrupt a slow match quickly.
const CHUNK_BYTES: usize = 64 << 10;

/// Read `slice_bytes` at a time instead of `CHUNK_BYTES`, for a test that has to force a
/// read boundary at a chosen offset. Production passes `None`.
///
/// It is a parameter rather than a constant because the boundary case is otherwise
/// unreachable: production reads files larger than one slice, but no such file fits in a
/// test fixture, so a test that wanted a straddling match could not create one. A case
/// that cannot be reached is a case that is never exercised.
fn slice_bytes(n: Option<usize>) -> usize {
    n.unwrap_or(CHUNK_BYTES).clamp(1, CHUNK_BYTES)
}

/// The regular carry tail and the bound on the compiled pattern's source length. A longer
/// pattern is refused at compile time. An unbounded match is deferred across slices; when
/// it exceeds this window its reported length is capped and the result names that loss.
pub const MAX_MATCH_BYTES: usize = 4096;

/// Former cursor-entry ceiling, retained as the regression fixture's threshold. Runtime
/// storage is bounded by one read slice plus the carried match window, not this count.
pub const MAX_CURSOR_LINES: usize = 1024;

/// The two matchers the lane compiles from one query.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Matcher {
    /// The query as written, matched byte for byte and case-sensitively.
    Exact,
    /// The query as a regular expression, matched over bytes.
    Regex,
}

impl Matcher {
    pub fn name(self) -> &'static str {
        match self {
            Matcher::Exact => "exact",
            Matcher::Regex => "regex",
        }
    }
}

/// One occurrence. `line` and `column` are 1-based so a caller can jump to it; `byte` is
/// the offset of the match in the file, which is what a follow-up positional read takes.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Span {
    pub path: String,
    pub line: usize,
    pub column: usize,
    pub byte: usize,
    pub len: usize,
}

/// Which cap stopped the scan. One value, so a caller reads which bound cut its answer
/// rather than inferring it from a smaller number.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub enum Cut {
    /// The reported-line cap was reached.
    Lines,
    /// The byte cap was reached.
    Bytes,
    /// The time cap was reached.
    Time,
}

/// What one scan found, and what it could not afford to look at.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Report {
    pub spans: Vec<Span>,
    /// Files whose bytes the scan read, whether or not they matched.
    pub files_scanned: usize,
    /// Bytes the scan read, over every file. Never more than `Limits::max_bytes`.
    pub bytes_scanned: u64,
    /// Which cap stopped the scan, if any. `None` means the corpus was exhausted.
    pub cut: Option<Cut>,
    /// Files listed but not read because a cap fired first. `u64` because it counts files
    /// on a tree that may hold more than `usize` paths on a 32-bit host.
    pub files_skipped: u64,
    /// Files listed and not read because their bytes could not be had.
    pub files_skipped_unreadable: usize,
    /// Matches whose unbounded extent exceeded the retained match window and whose
    /// reported length was capped.
    pub matches_cut: u64,
    /// Matches found whose position could not be resolved, because their start fell out
    /// of the cursor's bounded window. Always zero while the window covers
    /// `MAX_MATCH_BYTES` (which it does), and reported rather than dropped silently when
    /// it does not: a span with a byte offset but a guessed line would be a lie about
    /// where it is, and a silently missing span would be a lie about what was found.
    pub positions_lost: u64,
}

/// The caps one scan runs under.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Limits {
    /// Lines the answer may list.
    pub max_lines: usize,
    /// Bytes the scan may read in total.
    pub max_bytes: u64,
    /// Wall-clock the scan may spend.
    pub timeout_ms: u64,
}

impl Default for Limits {
    fn default() -> Self {
        Limits {
            max_lines: DEFAULT_MAX_LINES,
            max_bytes: DEFAULT_MAX_BYTES,
            timeout_ms: DEFAULT_TIMEOUT_MS,
        }
    }
}

/// Clamp a caller's three caps into `Limits`. Each value above its ceiling is refused at
/// this boundary rather than clamped, because a cap the caller believes is in force and
/// is not is worse than an error: `Ok(None)` names the one that was too large. A zero is
/// clamped to the default, since zero would answer "nothing was searched" and read as a
/// result.
pub fn limits(max_lines: usize, max_bytes: u64, timeout_ms: u64) -> Result<Limits, String> {
    let mut l = Limits::default();
    if max_lines > DEFAULT_MAX_LINES {
        return Err(format!(
            "max_lines is over the {DEFAULT_MAX_LINES} ceiling: {max_lines}"
        ));
    }
    if max_bytes > DEFAULT_MAX_BYTES {
        return Err(format!(
            "max_bytes is over the {DEFAULT_MAX_BYTES}-byte ceiling"
        ));
    }
    if timeout_ms > DEFAULT_TIMEOUT_MS {
        return Err(format!(
            "timeout_ms is over the {DEFAULT_TIMEOUT_MS} ceiling"
        ));
    }
    if max_lines > 0 {
        l.max_lines = max_lines;
    }
    if max_bytes > 0 {
        l.max_bytes = max_bytes;
    }
    if timeout_ms > 0 {
        l.timeout_ms = timeout_ms;
    }
    Ok(l)
}

/// Compile `query` for `matcher`, or the engine's complaint. A pattern the engine refuses
/// is bad input, not a query that matches nothing, so the caller reports it as one.
///
/// `exact` compiles the query through `regex::escape`, so a query carrying regex
/// metacharacters (`.`, `*`, `(`) matches the bytes it names rather than being read as a
/// pattern — which is what "exact" has to mean to differ from "regex".
pub fn compile(query: &str, matcher: Matcher) -> Result<Regex, String> {
    let pattern = match matcher {
        Matcher::Exact => regex::escape(query),
        Matcher::Regex => query.to_string(),
    };
    if pattern.len() > MAX_MATCH_BYTES {
        return Err(format!(
            "{} pattern is longer than the {MAX_MATCH_BYTES}-byte ceiling",
            matcher.name()
        ));
    }
    RegexBuilder::new(&pattern)
        // `unicode(false)` is `index::lexical`'s choice for the same reason: the Unicode
        // classes compile to automata of several MiB, and this lane reads bytes.
        .unicode(false)
        .dfa_size_limit(DFA_CACHE_BYTES)
        .size_limit(MAX_MATCH_BYTES * 8)
        .build()
        .map_err(|e| format!("{} pattern is not valid: {e}", matcher.name()))
}

/// The cursor retains line starts from the current read window and its carried tail.
/// Pruning happens only after matches in a window have been resolved, so a dense slice
/// can contain any number of short lines without losing positions.
#[derive(Debug, Clone)]
struct Cursor {
    /// Byte offset of each recent line start, ascending. Never empty.
    starts: Vec<usize>,
    /// The 1-based line number `starts[0]` belongs to. The file's own first line is 1.
    base_line: usize,
}

impl Cursor {
    fn new() -> Self {
        Cursor {
            starts: vec![0],
            base_line: 1,
        }
    }

    /// The position of `offset`, a file-relative byte offset, as (line, column), both
    /// 1-based. An offset between two starts belongs to the line that began first.
    ///
    /// `None` only when the offset precedes every start the cursor still holds. A deferred
    /// match may be older than this window; its saved position checkpoint resolves that case
    /// without asking the cursor to guess.
    fn at(&self, offset: usize) -> Option<(usize, usize)> {
        let i = match self.starts.binary_search(&offset) {
            Ok(i) => i,
            Err(i) => i.checked_sub(1)?,
        };
        Some((self.base_line + i, offset - self.starts[i] + 1))
    }

    /// Record the line starts in `bytes`, which begins at file offset `from`.
    fn advance(&mut self, bytes: &[u8], from: usize) {
        for (i, b) in bytes.iter().enumerate() {
            if *b == b'\n' {
                self.starts.push(from + i + 1);
            }
        }
    }

    /// Retain line starts from the next window's beginning. Pruning happens only after
    /// the current window's matches have been resolved; otherwise early matches in a
    /// large slice can lose their line starts before they are visited.
    fn retain_from(&mut self, keep_from: usize) {
        let cut = self
            .starts
            .partition_point(|s| *s < keep_from)
            .saturating_sub(1);
        if cut > 0 {
            self.starts.drain(..cut);
            self.base_line += cut;
        }
    }
}

/// One open file, read positionally so the scan never seeks and never maps. The fd came
/// from `open_repo_regular_file_with_meta`, so no symlink component was followed and the
/// held descriptor is a regular file.
pub struct FileScan<'a> {
    file: std::fs::File,
    /// Bytes of this file already read.
    pub read: u64,
    file_len: u64,
    /// The tail of the previous slice, so a match straddling a read boundary is found.
    carry: Vec<u8>,
    /// The file offset `carry[0]` came from, so a match found in the window is placed in
    /// file coordinates rather than window coordinates.
    carry_from: u64,
    /// Every byte below this offset has been scanned by a completed match or skipped over,
    /// so a match ending at or below it has already been reported. This is what makes a
    /// straddling match report exactly once: it is reported by the slice holding its last
    /// byte, and skipped by every later slice.
    last_reported_to: u64,
    /// Position checkpoint for a regex match deferred across more than the regular carry.
    deferred_position: Option<(usize, usize, usize)>,
    cursor: Cursor,
    /// Bytes per positional read; `slice_bytes(None)` in production, and a chosen smaller
    /// size in the test that forces a read boundary.
    slice: usize,
    root: &'a std::path::Path,
    path: String,
}

impl<'a> FileScan<'a> {
    /// Open `rel` for scanning, refusing the shapes the lane must not read: a path
    /// `secretpath` refuses, a non-regular file (a fifo or socket read could never
    /// finish), and a file past the per-file ceiling.
    pub fn open(root: &'a std::path::Path, rel: &str) -> Result<Self, Loss> {
        Self::open_with_slice(root, rel, None)
    }

    /// [`FileScan::open`] that reads `slice` bytes at a time, for a test that has to force
    /// a read boundary at a chosen offset.
    pub fn open_with_slice(
        root: &'a std::path::Path,
        rel: &str,
        slice: Option<usize>,
    ) -> Result<Self, Loss> {
        if crate::secretpath::is_secret_path(rel) {
            return Err(Loss::Secret(rel.to_string()));
        }
        let (file, meta) = crate::symbolgraph::open_repo_regular_file_with_meta(root, rel)
            .map_err(|e| Loss::Io(rel.to_string(), e.to_string()))?;
        if !meta.is_file() {
            return Err(Loss::NotRegular(rel.to_string()));
        }
        if meta.len() > MAX_REPO_FILE_BYTES {
            return Err(Loss::TooLarge(rel.to_string()));
        }
        Ok(FileScan {
            file,
            read: 0,
            file_len: meta.len(),
            carry: Vec::new(),
            carry_from: 0,
            last_reported_to: 0,
            deferred_position: None,
            cursor: Cursor::new(),
            slice: slice_bytes(slice),
            root,
            path: rel.to_string(),
        })
    }

    pub fn path(&self) -> &str {
        &self.path
    }

    pub fn root(&self) -> &std::path::Path {
        self.root
    }

    /// The next `want` bytes at the current position, or `None` at end of file.
    #[cfg(unix)]
    pub fn slice(&mut self, want: u64) -> std::io::Result<Option<Vec<u8>>> {
        use std::os::unix::fs::FileExt;
        if want == 0 {
            return Ok(None);
        }
        let n = want.min(self.slice as u64) as usize;
        let mut buf = vec![0u8; n];
        let got = self.file.read_at(&mut buf, self.read)?;
        if got == 0 {
            return Ok(None);
        }
        buf.truncate(got);
        self.read += got as u64;
        Ok(Some(buf))
    }

    /// Non-unix fallback: the same bounded positional read through `Seek`.
    #[cfg(not(unix))]
    pub fn slice(&mut self, want: u64) -> std::io::Result<Option<Vec<u8>>> {
        use std::io::{Read, Seek, SeekFrom};
        if want == 0 {
            return Ok(None);
        }
        self.file.seek(SeekFrom::Start(self.read))?;
        let n = want.min(self.slice as u64) as usize;
        let mut buf = vec![0u8; n];
        let got = self.file.read(&mut buf)?;
        if got == 0 {
            return Ok(None);
        }
        buf.truncate(got);
        self.read += got as u64;
        Ok(Some(buf))
    }

    /// Match `re` across the whole of this file, appending to `report` and returning
    /// `false` once `limits` cut the scan. `budget` is the whole scan's remaining byte
    /// allowance, so the byte cap spans files rather than restarting per file.
    ///
    /// Every byte is matched exactly once: a match wholly inside the carried tail was
    /// reported by the previous slice and is skipped here, and one straddling the boundary
    /// is found because the tail is still in the window. An incomplete boundary match is
    /// deferred only through `MAX_MATCH_BYTES`; longer matches are reported once with a
    /// capped length.
    pub fn scan(
        &mut self,
        re: &Regex,
        limits: &Limits,
        budget: &mut u64,
        report: &mut Report,
        started: std::time::Instant,
        more_files: bool,
    ) -> bool {
        // The window is the carried tail plus this slice. A deferred match can extend it
        // beyond the ordinary carry length, up to `MAX_MATCH_BYTES`.
        let mut window: Vec<u8> = Vec::with_capacity(MAX_MATCH_BYTES + CHUNK_BYTES);
        loop {
            let want = (*budget).min(self.slice as u64);
            if want == 0 {
                if self.read < self.file_len || more_files {
                    report.cut.get_or_insert(Cut::Bytes);
                    return false;
                }
                return true;
            }
            let buf = match self.slice(want) {
                Ok(Some(b)) => b,
                Ok(None) => return true,
                Err(_) => {
                    report.files_skipped_unreadable += 1;
                    return true;
                }
            };
            *budget -= buf.len() as u64;
            report.bytes_scanned += buf.len() as u64;
            let base = self.read - buf.len() as u64;

            // The cursor must know this slice's line starts BEFORE any match in it is
            // placed, because a match on line 40 of this slice resolves against a line
            // start inside it. Advancing after the match loop would leave every match past
            // the first line unresolvable, and the answer would silently lose them.
            self.cursor.advance(&buf, base as usize);

            // The window is rebuilt from the PREVIOUS carry plus this slice, and the
            // previous carry is dropped from the FRONT of it once reported, so every byte
            // is matched exactly once: a match wholly inside the previous carry was
            // already reported by the previous iteration, and dropping that prefix is what
            // stops it being reported twice. It also stops the file from being re-matched
            // in full on every slice, which a naive "match the whole window" would do.
            let carry_from = self.carry_from;
            window.clear();
            window.extend_from_slice(&self.carry);
            window.extend_from_slice(&buf);
            let new_from = base as usize;
            // Matches ending before the new bytes began belong to the previous window and
            // were reported then.
            let last_reported_to = self.last_reported_to;
            let mut deferred_from = None;
            for m in re.find_iter(&window) {
                let file_start = carry_from as usize + m.start();
                let file_end = carry_from as usize + m.end();
                // Matches that overlap an already reported span are not emitted again.
                if file_start < last_reported_to as usize {
                    continue;
                }
                // A match ending at the current window boundary may continue in the next
                // slice (for example `a.*`). Keep its start and wait for more input rather
                // than publishing a partial span that will be repeated with a new length.
                if m.end() == window.len() && self.read < self.file_len {
                    if self
                        .deferred_position
                        .is_none_or(|(offset, _, _)| file_start < offset)
                    {
                        self.deferred_position = self
                            .cursor
                            .at(file_start)
                            .map(|(line, column)| (file_start, line, column));
                    }
                    if self.read.saturating_sub(file_start as u64) >= MAX_MATCH_BYTES as u64 {
                        if report.spans.len() >= limits.max_lines {
                            report.cut = Some(Cut::Lines);
                            return false;
                        }
                        if let Some((_, line, column)) = self
                            .deferred_position
                            .filter(|(offset, _, _)| *offset == file_start)
                        {
                            report.spans.push(Span {
                                path: self.path.clone(),
                                line,
                                column,
                                byte: file_start,
                                len: MAX_MATCH_BYTES,
                            });
                            report.matches_cut += 1;
                            self.last_reported_to = (file_start + MAX_MATCH_BYTES) as u64;
                        } else {
                            report.positions_lost += 1;
                        }
                        self.deferred_position = None;
                    } else {
                        deferred_from = Some(
                            deferred_from.map_or(file_start, |old: usize| old.min(file_start)),
                        );
                    }
                    continue;
                }
                if report.spans.len() >= limits.max_lines {
                    report.cut = Some(Cut::Lines);
                    return false;
                }
                let deferred = self
                    .deferred_position
                    .filter(|(offset, _, _)| *offset == file_start)
                    .map(|(_, line, column)| (line, column));
                let (line, column) = match deferred.or_else(|| self.cursor.at(file_start)) {
                    Some(p) => p,
                    // A match cannot begin further back than the retained carry unless it
                    // has this scan's deferred-position checkpoint.
                    None => {
                        report.positions_lost += 1;
                        continue;
                    }
                };
                if self
                    .deferred_position
                    .is_some_and(|(offset, _, _)| offset == file_start)
                {
                    self.deferred_position = None;
                }
                report.spans.push(Span {
                    path: self.path.clone(),
                    line,
                    column,
                    byte: file_start,
                    len: m.end() - m.start(),
                });
                // Every match up to the end of this window has now been reported or
                // skipped, so a later slice must not report one that ends below this.
                self.last_reported_to = file_end as u64;
            }

            // Retain a boundary-ending match from its start so unbounded quantifiers can
            // finish across later slices. The scan-wide byte cap bounds this extension.
            if let Some(from) = deferred_from {
                let start = from.saturating_sub(carry_from as usize);
                self.carry = window[start..].to_vec();
                self.carry_from = from as u64;
            } else {
                let keep = buf.len().min(MAX_MATCH_BYTES);
                self.carry_from = (new_from + buf.len() - keep) as u64;
                self.carry = buf[buf.len() - keep..].to_vec();
            }
            self.cursor
                .retain_from(self.read.saturating_sub(MAX_MATCH_BYTES as u64) as usize);

            // Bytes past the last match were scanned and found empty, so a later slice
            // must not re-report a match ending inside them. The carry's start is where
            // the next window begins, and every byte before it has been looked at.
            self.last_reported_to = self.last_reported_to.max(self.carry_from);

            if started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms) {
                report.cut = Some(Cut::Time);
                return false;
            }
            if report.bytes_scanned >= limits.max_bytes && (self.read < self.file_len || more_files)
            {
                report.cut = Some(Cut::Bytes);
                return false;
            }
        }
    }
}

/// Why one file's bytes were not read. Every variant is reported and counted; none is
/// treated as an empty file.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Loss {
    /// `secretpath` refuses this path, so its bytes are never read and never reported.
    Secret(String),
    /// Past `MAX_REPO_FILE_BYTES`, so reading it whole would be the corpus-sized heap
    /// this lane exists to avoid.
    TooLarge(String),
    /// Not a regular file: reading it could never finish.
    NotRegular(String),
    /// The bytes could not be read.
    Io(String, String),
}

impl Loss {
    pub fn path(&self) -> &str {
        match self {
            Loss::Secret(p) | Loss::TooLarge(p) | Loss::NotRegular(p) | Loss::Io(p, _) => p,
        }
    }

    /// The `coverage.loss_counts` key this loss is counted under.
    pub fn count_key(&self) -> &'static str {
        match self {
            Loss::Secret(_) => "exact_scan_secret_path",
            Loss::TooLarge(_) => "exact_scan_file_too_large",
            Loss::NotRegular(_) => "exact_scan_not_regular",
            Loss::Io(_, _) => "exact_scan_read_error",
        }
    }

    /// The text `degradations` carries, so a caller reading only that field still learns
    /// the path was not searched.
    pub fn message(&self) -> String {
        match self {
            Loss::Secret(p) => format!("{p} is a secret path and was not searched"),
            Loss::TooLarge(p) => format!(
                "{p} is past the {MAX_REPO_FILE_BYTES}-byte per-file ceiling and was not searched"
            ),
            Loss::NotRegular(p) => format!("{p} is not a regular file and was not searched"),
            Loss::Io(p, e) => format!("{p} could not be read and was not searched: {e}"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The ceiling each cap is clamped to, asserted against the constants rather than
    /// restated, so a change to a ceiling fails here instead of passing silently.
    #[test]
    fn limits_default_to_the_documented_ceilings() {
        let d = Limits::default();
        assert_eq!(d.max_lines, DEFAULT_MAX_LINES);
        assert_eq!(d.max_bytes, DEFAULT_MAX_BYTES);
        assert_eq!(d.timeout_ms, DEFAULT_TIMEOUT_MS);
    }

    // An over-ceiling cap is refused, not clamped: a caller told "4 MiB" and given 32 MiB
    // would read a bounded answer as a complete one. This is the shape of refusal
    // `impact`'s `bounded()` already uses for its own timeout.
    #[test]
    fn an_over_ceiling_cap_is_refused_rather_than_clamped() {
        let e = limits(DEFAULT_MAX_LINES + 1, DEFAULT_MAX_BYTES, DEFAULT_TIMEOUT_MS).unwrap_err();
        assert!(e.contains("max_lines"), "{e}");
        assert!(e.contains(&(DEFAULT_MAX_LINES + 1).to_string()), "{e}");
        let e = limits(DEFAULT_MAX_LINES, DEFAULT_MAX_BYTES + 1, DEFAULT_TIMEOUT_MS).unwrap_err();
        assert!(e.contains("max_bytes"), "{e}");
        let e = limits(DEFAULT_MAX_LINES, DEFAULT_MAX_BYTES, DEFAULT_TIMEOUT_MS + 1).unwrap_err();
        assert!(e.contains("timeout_ms"), "{e}");
        assert!(limits(DEFAULT_MAX_LINES, DEFAULT_MAX_BYTES, DEFAULT_TIMEOUT_MS).is_ok());
    }

    // A zero cap would answer "nothing was searched" and read as a result, so it takes
    // the default instead. This is the one value clamped rather than refused, and the
    // test says why.
    #[test]
    fn a_zero_cap_takes_the_default_rather_than_answering_nothing() {
        let l = limits(0, 0, 0).unwrap();
        assert_eq!(l, Limits::default());
    }

    // "Exact" has to mean the query as written. Without the escape, a query carrying a
    // metacharacter would be read as a pattern and match bytes the caller never named,
    // which is the whole difference between the two modes.
    #[test]
    fn exact_escapes_the_query_and_regex_does_not() {
        let exact = compile("a.c", Matcher::Exact).unwrap();
        assert!(
            !exact.is_match(b"abc"),
            "a.c is literal, so it does not match abc"
        );
        assert!(exact.is_match(b"a.c"), "a.c is literal, so it matches a.c");
        let re = compile("a.c", Matcher::Regex).unwrap();
        assert!(re.is_match(b"abc"), "a.c is a pattern, so it matches abc");
        assert!(re.is_match(b"a.c"), "a.c is a pattern, so it matches a.c");
    }

    // A pattern the engine refuses is bad input, not a query matching nothing, so the
    // error carries the engine's own message rather than a bare "no match".
    #[test]
    fn an_uncompilable_pattern_is_refused_with_the_engines_message() {
        let e = compile("a(", Matcher::Regex).unwrap_err();
        assert!(e.starts_with("regex pattern is not valid"), "{e}");
    }

    // A pattern longer than the cross-boundary tail could not be found at all once it
    // straddled a read, so it is refused at compile time rather than silently missing.
    #[test]
    fn an_over_long_pattern_is_refused_before_any_byte_is_read() {
        let long = "a".repeat(MAX_MATCH_BYTES + 1);
        let e = compile(&long, Matcher::Regex).unwrap_err();
        assert!(e.contains(&format!("{MAX_MATCH_BYTES}")), "{e}");
    }

    #[test]
    fn case_sensitivity_follows_the_matcher_and_is_not_inferred() {
        let exact = compile("Needle", Matcher::Exact).unwrap();
        assert!(exact.is_match(b"a Needle here"));
        assert!(
            !exact.is_match(b"a needle here"),
            "exact is case-sensitive, so it does not match a lowercase needle"
        );
        let re = compile("(?i)needle", Matcher::Regex).unwrap();
        assert!(
            re.is_match(b"a Needle here"),
            "regex takes its own flags, so (?i) matches a capital N"
        );
    }

    // The cursor is the lane's only line bookkeeping, and a span's `line`/`column` are
    // read from it, so it is tested directly at the boundaries: the first byte, the byte
    // after a newline, and a match at the very end of the input.
    #[test]
    fn the_cursor_numbers_lines_and_columns_from_the_first_byte() {
        let mut c = Cursor::new();
        assert_eq!(c.at(0), Some((1, 1)));
        let body = b"ab\ncd\n";
        c.advance(body, 0);
        assert_eq!(c.at(3), Some((2, 1)), "the byte after the first newline");
        assert_eq!(c.at(4), Some((2, 2)));
        assert_eq!(c.at(6), Some((3, 1)), "the position at end of input");
    }

    // Pruning happens after a slice's matches have been resolved. The running base line
    // remains correct after old starts are discarded, and an old-line checkpoint remains
    // available when that line extends into the retained window.
    #[test]
    fn a_pruned_cursor_still_numbers_lines_correctly() {
        let mut c = Cursor::new();
        let mut first = vec![b'x'; 8];
        first.push(b'\n');
        c.advance(&first, 0); // line 1 ends here, so line 2 begins at 9
        // a slice far past the byte window, which prunes the start at 9
        let gap = vec![b'y'; MAX_MATCH_BYTES * 2];
        let second_at = first.len() + gap.len();
        c.advance(&gap, first.len());
        c.retain_from(second_at);
        assert!(
            c.starts.first() != Some(&0),
            "this fixture must actually prune something, or it proves nothing: starts={:?}",
            c.starts
        );
        // the newline at the end of this slice ends line 2 and begins line 3, whose start
        // is the newest and must be answerable however much was forgotten before it
        let mut second = vec![b'z'; 4];
        second.push(b'\n');
        c.advance(&second, second_at);
        let after = second_at + second.len();
        assert_eq!(
            c.at(after),
            Some((3, 1)),
            "the pruned cursor numbers the line it just recorded: {:?}",
            c.starts
        );
    }

    // A long line can cross multiple read windows. Its start remains as one checkpoint,
    // allowing the lane to report an accurate column without retaining every byte offset.
    #[test]
    fn a_cursor_keeps_the_start_of_a_long_line_for_accurate_columns() {
        let mut c = Cursor::new();
        c.advance(&vec![b'x'; MAX_MATCH_BYTES * 3], 0);
        c.retain_from(MAX_MATCH_BYTES * 2);
        assert_eq!(c.at(0), Some((1, 1)));
        assert_eq!(
            c.at(MAX_MATCH_BYTES * 2),
            Some((1, MAX_MATCH_BYTES * 2 + 1))
        );
    }

    #[test]
    fn a_cursor_keeps_more_than_the_old_entry_ceiling_without_losing_lines() {
        let mut c = Cursor::new();
        // Four times the former entry limit, all within one small read window.
        let body = vec![b'\n'; MAX_CURSOR_LINES * 4];
        c.advance(&body, 0);
        assert!(
            c.starts.len() > MAX_CURSOR_LINES,
            "every line start in this bounded slice remains available"
        );
        // The final short line still has its exact number.
        let last = body.len() - 1;
        assert_eq!(
            c.at(last),
            Some((MAX_CURSOR_LINES * 4, 1)),
            "the newest line is still numbered correctly: base_line={}",
            c.base_line
        );
    }

    // A column is a byte offset within its line, because the lane matches over bytes.
    // A multi-byte character therefore advances the column by its encoded length, which
    // is the honest answer for a byte lane and the reason the span also carries `byte`.
    #[test]
    fn a_column_counts_bytes_so_a_multibyte_character_advances_it_by_its_length() {
        let mut c = Cursor::new();
        let body = "é\n".as_bytes();
        c.advance(body, 0);
        assert_eq!(c.at(2), Some((1, 3)), "e-acute is two bytes");
    }

    #[test]
    fn opening_a_file_counts_one_stat_and_no_hashes() {
        let td = tempfile::tempdir().unwrap();
        std::fs::write(td.path().join("a.txt"), "needle\n").unwrap();
        let call = crate::passcount::Call::begin();
        let fs = FileScan::open(td.path(), "a.txt").unwrap();
        let counts = call.counts();
        drop(call);
        drop(fs);
        assert_eq!(
            counts.get(crate::passcount::Pass::FilesStatted),
            1,
            "{counts:?}"
        );
        assert_eq!(
            counts.get(crate::passcount::Pass::FilesHashed),
            0,
            "{counts:?}"
        );
        assert_eq!(
            counts.get(crate::passcount::Pass::BytesHashed),
            0,
            "{counts:?}"
        );
    }

    #[test]
    fn matches_in_a_large_slice_resolve_before_line_starts_are_pruned() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let mut body = b"needle\n".to_vec();
        for _ in 0..MAX_CURSOR_LINES + 20 {
            body.extend_from_slice(b"x\n");
        }
        std::fs::write(root.join("a.txt"), &body).unwrap();
        let mut fs = FileScan::open(root, "a.txt").unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Exact).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.positions_lost, 0);
        assert_eq!(report.spans.len(), 1);
        assert_eq!(report.spans[0].line, 1);
    }

    #[test]
    fn matches_near_the_start_middle_and_end_of_a_multislice_file_are_preserved() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let mut body = vec![b'x'; CHUNK_BYTES * 2 + 100];
        let offsets = [8, CHUNK_BYTES + 9, CHUNK_BYTES * 2 + 84];
        for at in offsets {
            body[at..at + 6].copy_from_slice(b"needle");
        }
        std::fs::write(root.join("a.txt"), &body).unwrap();
        let mut fs = FileScan::open(root, "a.txt").unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Exact).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.positions_lost, 0);
        assert_eq!(
            report.spans.iter().map(|s| s.byte).collect::<Vec<_>>(),
            offsets
        );
    }

    #[test]
    fn exhausting_the_byte_budget_at_eof_is_not_a_truncation() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let body = b"needle\n";
        std::fs::write(root.join("a.txt"), body).unwrap();
        let mut fs = FileScan::open(root, "a.txt").unwrap();
        let limits = Limits {
            max_lines: DEFAULT_MAX_LINES,
            max_bytes: body.len() as u64,
            timeout_ms: DEFAULT_TIMEOUT_MS,
        };
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Exact).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.bytes_scanned, body.len() as u64);
        assert_eq!(report.cut, None);
    }

    #[test]
    fn an_unbounded_regex_match_crossing_a_slice_is_reported_once_at_full_length() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let body = format!("a{}z", "x".repeat(127));
        std::fs::write(root.join("a.txt"), &body).unwrap();
        let mut fs = FileScan::open_with_slice(root, "a.txt", Some(64)).unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("a.*", Matcher::Regex).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].byte, 0);
        assert_eq!(report.spans[0].len, body.len());
    }

    #[test]
    fn an_unbounded_match_past_the_window_is_reported_once_with_a_counted_cap() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let body = format!("a{}z", "x".repeat(MAX_MATCH_BYTES * 2));
        std::fs::write(root.join("a.txt"), &body).unwrap();
        let mut fs = FileScan::open_with_slice(root, "a.txt", Some(64)).unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("a.*", Matcher::Regex).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].len, MAX_MATCH_BYTES);
        assert_eq!(report.matches_cut, 1);
    }

    // The time cap is the scan's own wall clock, and this row's remaining uncovered
    // requirement (tests required (6)): a scan that would exceed it stops and names the
    // cut. `scan` takes its `started` as a parameter, so the test hands it one two seconds
    // in the past and drives read boundaries with a 1-byte slice; the cap fires on the
    // first slice's check, which no machine races.
    #[test]
    fn the_time_cap_stops_a_scan_in_flight_and_names_its_cut() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        // Two chunks long, so with a 1-byte slice the file has scans left to do when the
        // cap fires; the scanner never sees its own end of file.
        let body = vec![b'x'; CHUNK_BYTES * 2 + 1];
        std::fs::write(root.join("a.txt"), &body).unwrap();
        let mut fs = FileScan::open_with_slice(root, "a.txt", Some(1)).unwrap();
        let started = std::time::Instant::now() - std::time::Duration::from_secs(2);
        let limits = Limits {
            max_lines: 200,
            max_bytes: u64::MAX,
            timeout_ms: 1,
        };
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Exact).unwrap();
        let more = fs.scan(&re, &limits, &mut budget, &mut report, started, false);
        assert!(!more, "the time cap stops the scan before its corpus ends");
        assert_eq!(report.cut, Some(Cut::Time), "{:?}", report);
        assert_eq!(
            report.bytes_scanned, 1,
            "one slice, then the cap: {:?}",
            report
        );
    }
}
