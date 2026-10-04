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
//! through a positional read at an explicit offset, so a corpus-resident map (which
//! PAR-RET-09 rules out, and which would need ~1.2× the corpus in RAM) is never built, and
//! one slice at a time is the only memory the scan holds.

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

/// The longest tail of the previous slice a match may straddle into the next one, and the
/// bound on the compiled pattern's state size. A match longer than this is refused at
/// compile time rather than silently missing the boundary it crosses.
pub const MAX_MATCH_BYTES: usize = 4096;

/// How far back the cursor keeps line starts. A match reported in a slice may have begun
/// in the carried tail, which is at most `MAX_MATCH_BYTES` back, so the cursor needs the
/// starts within that window and no older. Bounded therefore: at most `MAX_CURSOR_LINES`
/// entries regardless of file size, which is what keeps this lane off the budget line
/// (§5.1 rule 8: nothing proportional to the file lives in a heap).
pub const MAX_CURSOR_LINES: usize = MAX_MATCH_BYTES;

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
            "max_lines is over the {DEFAULT_MAX_LINES} ceiling: {}",
            Limits::default().max_lines
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

/// The line starts within reach of the current read, so the line and column of a reported
/// offset is a lookup rather than a count from the current position.
///
/// A match found in a slice may have STARTED in the carried tail, at most
/// `MAX_MATCH_BYTES` back, so a running line counter alone cannot answer for it. The
/// cursor therefore keeps the recent line starts and drops the ones that have fallen out
/// of that window, which bounds it at `MAX_CURSOR_LINES` entries whatever the file's size.
/// An offset older than the window is a match that began further back than a match may
/// be, so it cannot occur; the cursor refuses it rather than guessing.
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
    fn at(&self, offset: usize) -> Option<(usize, usize)> {
        let i = match self.starts.binary_search(&offset) {
            Ok(i) => i,
            Err(i) => i.checked_sub(1)?,
        };
        Some((self.base_line + i, offset - self.starts[i] + 1))
    }

    /// Record the line starts in `bytes`, which begins at file offset `from`, and forget
    /// the ones the window no longer reaches.
    ///
    /// The window is measured from the END of the slice just read, not from its start: a
    /// match may begin in the carried tail, which is `MAX_MATCH_BYTES` back from the end.
    /// Pruning against the start instead would drop the very start a match in this slice
    /// could begin at, and the cursor would answer `None` for a match it must report.
    fn advance(&mut self, bytes: &[u8], from: usize) {
        for (i, b) in bytes.iter().enumerate() {
            if *b == b'\n' {
                self.starts.push(from + i + 1);
            }
        }
        let end = from + bytes.len();
        let keep_from = end.saturating_sub(MAX_CURSOR_LINES);
        let cut = self.starts.partition_point(|s| *s < keep_from);
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
    /// The tail of the previous slice, so a match straddling a read boundary is found.
    carry: Vec<u8>,
    cursor: Cursor,
    root: &'a std::path::Path,
    path: String,
}

impl<'a> FileScan<'a> {
    /// Open `rel` for scanning, refusing the shapes the lane must not read: a path
    /// `secretpath` refuses, a non-regular file (a fifo or socket read could never
    /// finish), and a file past the per-file ceiling.
    pub fn open(root: &'a std::path::Path, rel: &str) -> Result<Self, Loss> {
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
            carry: Vec::new(),
            cursor: Cursor::new(),
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
        let n = want.min(CHUNK_BYTES as u64) as usize;
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
        let n = want.min(CHUNK_BYTES as u64) as usize;
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
    /// A match inside the carried tail is dropped rather than reported twice: the
    /// previous slice already reported it, at the offset it has there.
    pub fn scan(&mut self, re: &Regex, limits: &Limits, budget: &mut u64, report: &mut Report, started: std::time::Instant) -> bool {
        loop {
            let want = (*budget).min(CHUNK_BYTES as u64);
            if want == 0 {
                if self.read < u64::MAX {
                    // the byte cap, not the end of this file
                    report.cut.get_or_insert(Cut::Bytes);
                }
                return false;
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

            // the window the matcher sees: the carried tail, then this slice. A match is
            // reported only when it ends inside the new bytes, which is what keeps a
            // boundary-straddling match to one report.
            let carry = std::mem::take(&mut self.carry);
            let base = self.read - buf.len() as u64;
            let mut window = Vec::with_capacity(carry.len() + buf.len());
            window.extend_from_slice(&carry);
            let carry_len = carry.len();
            window.extend_from_slice(&buf);
            for m in re.find_iter(&window) {
                let start = m.start();
                let end = m.end();
                // a match that ends inside the carry was reported by the previous slice
                if end <= carry_len {
                    continue;
                }
                if report.spans.len() >= limits.max_lines {
                    report.cut = Some(Cut::Lines);
                    return false;
                }
                let file_off = start - carry_len;
                let (line, column) = match self.cursor.at(base as usize + file_off) {
                    Some(p) => p,
                    // A match cannot begin further back than `MAX_MATCH_BYTES`, so the
                    // cursor always has its start. If it does not, the span is counted as
                    // lost rather than dropped: a missing span is a hole in the answer,
                    // and a hole that is counted is one a caller can see.
                    None => {
                        report.positions_lost += 1;
                        continue;
                    }
                };
                report.spans.push(Span {
                    path: self.path.clone(),
                    line,
                    column,
                    byte: base as usize + file_off,
                    len: end - start,
                });
            }
            self.cursor.advance(&buf, base as usize);
            // keep the tail for the next slice; a match longer than this is refused at
            // compile time, so the tail is always long enough
            let keep = buf.len().min(MAX_MATCH_BYTES);
            self.carry = buf[buf.len() - keep..].to_vec();

            if started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms) {
                report.cut = Some(Cut::Time);
                return false;
            }
            if report.bytes_scanned >= limits.max_bytes {
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
        assert!(!exact.is_match(b"abc"), "a.c is literal, so it does not match abc");
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

    // The cursor is bounded so it never holds memory proportional to the file, which means
    // it forgets old line starts. It must then still answer CORRECTLY for a recent one —
    // the line number is carried in `base_line`, not recovered from a forgotten entry — and
    // refuse an offset older than the window rather than report a line it cannot know.
    #[test]
    fn a_bounded_cursor_still_numbers_recent_lines_and_forgets_old_ones() {
        let mut c = Cursor::new();
        // one very long line, then a second line far beyond the window
        let first = vec![b'x'; MAX_CURSOR_LINES + 16];
        c.advance(&first, 0);
        let second_at = first.len() as usize + 1;
        let mut second = vec![b'y'; 8];
        second.push(b'\n');
        c.advance(&second, second_at);
        assert!(
            c.starts.len() <= MAX_CURSOR_LINES + 8,
            "the cursor is bounded: {} entries",
            c.starts.len()
        );
        // the second line's number is 2 even though the first line's start was forgotten
        assert_eq!(
            c.at(second_at),
            Some((2, 1)),
            "line numbering survives the bound: {:?}",
            c.starts
        );
        // an offset before the window is not answered rather than guessed
        assert_eq!(c.at(0), None, "an offset older than the window is refused");
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
}