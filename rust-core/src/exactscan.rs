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
//! Nothing proportional to the corpus is held. The scan-wide byte budget bounds the
//! current file's match buffer, and positional reads keep the resident bytes independent
//! of the file size. An occurrence longer than `MAX_MATCH_BYTES` gets a capped span and a
//! visible degradation rather than an unbounded reported length.

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

/// Bound on the test cursor's retained line starts; production resolves positions while
/// iterating matches and retains no per-line entries.
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

/// Advance a 1-based line and byte-column position through a slice.
fn advance_position(bytes: &[u8], line: &mut usize, column: &mut usize) {
    for byte in bytes {
        if *byte == b'\n' {
            *line += 1;
            *column = 1;
        } else {
            *column += 1;
        }
    }
}

/// Whether a pattern can treat an artificial buffer end as an assertion boundary.
fn has_end_assertion(pattern: &str) -> bool {
    let mut escaped = false;
    let mut in_class = false;
    for ch in pattern.chars() {
        if escaped {
            if !in_class && matches!(ch, 'z' | 'b' | 'B') {
                return true;
            }
            escaped = false;
            continue;
        }
        match ch {
            '\\' => escaped = true,
            '[' => in_class = true,
            ']' => in_class = false,
            '$' if !in_class => return true,
            _ => {}
        }
    }
    false
}

/// Whether a pattern can vary a match's extent at the scan cutoff.
fn may_extend_match(pattern: &str) -> bool {
    let mut escaped = false;
    let mut in_class = false;
    for (index, ch) in pattern.char_indices() {
        if escaped {
            escaped = false;
            continue;
        }
        match ch {
            '\\' => escaped = true,
            '[' => in_class = true,
            ']' => in_class = false,
            '*' | '+' if !in_class => return true,
            '{' if !in_class => {
                if pattern[index + 1..]
                    .find('}')
                    .map(|end| pattern[index + 1..index + 1 + end].contains(','))
                    .unwrap_or(false)
                {
                    return true;
                }
            }
            _ => {}
        }
    }
    false
}

/// A bounded line-start cursor used to test line and column bookkeeping.
#[cfg(test)]
#[derive(Debug, Clone)]
struct Cursor {
    /// Byte offset of each recent line start, ascending. Never empty.
    starts: Vec<usize>,
    /// The 1-based line number `starts[0]` belongs to. The file's own first line is 1.
    base_line: usize,
}

#[cfg(test)]
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
    /// `None` when the offset precedes the starts the bounded test cursor retains.
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
                if self.starts.len() > MAX_CURSOR_LINES {
                    self.starts.remove(0);
                    self.base_line += 1;
                }
            }
        }
    }

    /// Retain line starts from the next window's beginning. Pruning happens only after
    /// the current window's matches have been resolved; otherwise early matches in a
    /// large slice can lose their line starts before they are visited.
    #[cfg(test)]
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

    /// Match `re` against the bytes read for this file, appending to `report` and returning
    /// `false` once `limits` cut the scan. `budget` is the whole scan's remaining byte
    /// allowance, so the byte cap spans files rather than restarting per file.
    ///
    /// The match buffer is bounded by that scan-wide byte budget. Matching one stable
    /// haystack preserves greedy extents, non-overlapping iteration and file assertions
    /// across positional-read boundaries.
    pub fn scan(
        &mut self,
        re: &Regex,
        limits: &Limits,
        budget: &mut u64,
        report: &mut Report,
        started: std::time::Instant,
        more_files: bool,
    ) -> bool {
        // A scan's global byte cap bounds this buffer independently of repository size.
        // Matching one stable haystack preserves regex greediness, non-overlap and BOF
        // assertions across positional read boundaries.
        let mut body = Vec::new();
        let mut eof = false;
        loop {
            let want = (*budget).min(self.slice as u64);
            if want == 0 {
                eof = self
                    .file
                    .metadata()
                    .map(|meta| self.read >= meta.len())
                    .unwrap_or(false);
                break;
            }
            match self.slice(want) {
                Ok(Some(bytes)) => {
                    *budget -= bytes.len() as u64;
                    report.bytes_scanned += bytes.len() as u64;
                    body.extend_from_slice(&bytes);
                }
                Ok(None) => {
                    eof = true;
                    break;
                }
                Err(_) => {
                    report.files_skipped_unreadable += 1;
                    return true;
                }
            }
            if started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms) {
                report.cut = Some(Cut::Time);
                break;
            }
        }

        // A regex occurrence ending at an artificial byte boundary may still grow; do
        // not publish that provisional extent. Fixed literals are complete immediately.
        let scan_len = body.len();
        let end_sensitive = has_end_assertion(re.as_str());
        let may_extend = may_extend_match(re.as_str());
        let mut position_offset = 0;
        let mut line = 1;
        let mut column = 1;
        let mut matches = re.find_iter(&body);
        loop {
            if started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms) {
                report.cut = Some(Cut::Time);
                break;
            }
            let Some(m) = matches.next() else {
                break;
            };
            if !eof && may_extend {
                continue;
            }
            if !eof
                && end_sensitive
                && (m.end() == scan_len
                    || (m.end() + 1 == scan_len && body.get(m.end()) == Some(&b'\n')))
            {
                continue;
            }
            if m.start() == m.end() && m.start() == scan_len && !eof {
                continue;
            }
            let start = m.start();
            let end = m.end();
            advance_position(&body[position_offset..start], &mut line, &mut column);
            let match_line = line;
            let match_column = column;
            advance_position(&body[start..end], &mut line, &mut column);
            position_offset = end;
            if report.spans.len() >= limits.max_lines {
                report.cut = Some(Cut::Lines);
                return false;
            }
            let len = end.saturating_sub(start).min(MAX_MATCH_BYTES);
            report.spans.push(Span {
                path: self.path.clone(),
                line: match_line,
                column: match_column,
                byte: start,
                len,
            });
            if end - start > MAX_MATCH_BYTES {
                report.matches_cut += 1;
            }
        }
        if report.cut.is_none()
            && started.elapsed() >= std::time::Duration::from_millis(limits.timeout_ms)
        {
            report.cut = Some(Cut::Time);
        } else if report.cut.is_none()
            && (!eof || more_files)
            && report.bytes_scanned >= limits.max_bytes
        {
            report.cut = Some(Cut::Bytes);
        }
        report.cut.is_none()
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
    fn a_cursor_bounds_line_starts_and_keeps_the_final_line_number() {
        let mut c = Cursor::new();
        // Four times the former entry limit, all within one small read window.
        let body = vec![b'\n'; MAX_CURSOR_LINES * 4];
        c.advance(&body, 0);
        assert!(
            c.starts.len() <= MAX_CURSOR_LINES,
            "cursor line starts remain bounded"
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

    fn scan_fixture(
        body: &[u8],
        query: &str,
        matcher: Matcher,
        slice: Option<usize>,
        max_bytes: u64,
    ) -> Report {
        let td = tempfile::tempdir().unwrap();
        std::fs::write(td.path().join("a.txt"), body).unwrap();
        let mut fs = FileScan::open_with_slice(td.path(), "a.txt", slice).unwrap();
        let limits = Limits {
            max_bytes,
            ..Limits::default()
        };
        let mut budget = max_bytes;
        let mut report = Report::default();
        let re = compile(query, matcher).unwrap();
        fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false,
        );
        report
    }

    #[test]
    fn greedy_extent_before_slice_boundary_is_not_published_early() {
        let mut body = vec![b'x'; 65_500];
        body.extend_from_slice(b"az");
        body.extend_from_slice(&[b'x'; 64]);
        body.push(b'z');
        let report = scan_fixture(&body, "a.*z", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].byte, 65_500);
        assert_eq!(report.spans[0].len, 67);
    }

    #[test]
    fn repeated_literal_occurrences_keep_whole_file_nonoverlap_alignment() {
        let mut body = vec![b'x'; 70_000];
        body[65_000..65_000 + 511 * 2].fill(b'a');
        let report = scan_fixture(
            &body,
            &"a".repeat(511),
            Matcher::Exact,
            None,
            DEFAULT_MAX_BYTES,
        );
        let want = [65_000, 65_511];
        assert_eq!(
            report.spans.iter().map(|s| s.byte).collect::<Vec<_>>(),
            want
        );
    }

    #[test]
    fn a_capped_unbounded_occurrence_is_not_restarted_in_a_later_slice() {
        let body = vec![b'x'; 100_000];
        let report = scan_fixture(&body, "x+", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].byte, 0);
        assert_eq!(report.spans[0].len, MAX_MATCH_BYTES);
        assert_eq!(report.matches_cut, 1);
    }

    #[test]
    fn beginning_of_file_assertion_keeps_file_context() {
        let mut body = vec![b'x'; 65_537];
        body[61_440..61_446].copy_from_slice(b"needle");
        let report = scan_fixture(&body, "^needle", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert!(report.spans.is_empty(), "{report:?}");
    }

    #[test]
    fn zero_length_word_boundary_matches_are_unique() {
        let mut body = vec![b' '; 65_534];
        body.extend_from_slice(b"x  ");
        let report = scan_fixture(&body, r"\b", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(
            report.spans.iter().map(|s| s.byte).collect::<Vec<_>>(),
            [65_534, 65_535]
        );
    }

    #[test]
    fn a_complete_literal_is_reported_even_when_the_byte_budget_ends_after_it() {
        let report = scan_fixture(b"needlex", "needle", Matcher::Exact, Some(64), 6);
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].byte, 0);
        assert_eq!(report.cut, Some(Cut::Bytes));
    }

    #[test]
    fn actual_eof_finalizes_matches_after_the_open_file_is_truncated() {
        let td = tempfile::tempdir().unwrap();
        let root = td.path();
        let path = root.join("a.txt");
        std::fs::write(&path, b"needlezz").unwrap();
        let mut fs = FileScan::open(root, "a.txt").unwrap();
        std::fs::write(&path, b"needle").unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Regex).unwrap();
        assert!(fs.scan(
            &re,
            &limits,
            &mut budget,
            &mut report,
            std::time::Instant::now(),
            false
        ));
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].len, 6);
    }

    #[test]
    fn viable_regex_prefix_survives_production_sized_read_boundaries() {
        let mut body = b"a".to_vec();
        body.extend_from_slice(&vec![b'x'; 70_000]);
        body.push(b'z');
        let report = scan_fixture(&body, "a.*z", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].byte, 0);
        assert_eq!(report.spans[0].len, MAX_MATCH_BYTES);
        assert_eq!(report.matches_cut, 1);
    }

    #[test]
    fn regex_match_iteration_observes_the_scan_deadline() {
        let body = vec![b'a'; 70_000];
        let report = scan_fixture(
            &body,
            &"a".repeat(511),
            Matcher::Exact,
            None,
            DEFAULT_MAX_BYTES,
        );
        assert_eq!(report.cut, Some(Cut::Time), "{report:?}");
        assert!(
            report.spans.len() < (0..70_000 - 511).step_by(511).count(),
            "matching stopped at the deadline rather than exhausting all occurrences"
        );
    }

    #[test]
    fn artificial_cutoff_does_not_satisfy_end_assertions() {
        for pattern in ["needle$", r"needle\z"] {
            let report = scan_fixture(b"needlex", pattern, Matcher::Regex, Some(64), 6);
            assert!(report.spans.is_empty(), "{pattern}: {report:?}");
        }
        let trailing_newline = scan_fixture(b"needle\nx", "needle$", Matcher::Regex, Some(64), 7);
        assert!(trailing_newline.spans.is_empty(), "{trailing_newline:?}");
        let literal_dollar = scan_fixture(b"$x", "[$]", Matcher::Regex, Some(64), 1);
        assert_eq!(literal_dollar.spans.len(), 1, "{literal_dollar:?}");
    }

    #[test]
    fn bounded_regex_match_is_kept_at_cutoff() {
        let report = scan_fixture(b"ax", "[ab]", Matcher::Regex, Some(64), 1);
        assert_eq!(report.spans.len(), 1, "{report:?}");
        assert_eq!(report.spans[0].len, 1);
        let bounded = scan_fixture(b"aax", "a{2}", Matcher::Regex, Some(64), 2);
        assert_eq!(bounded.spans.len(), 1, "{bounded:?}");
        assert_eq!(bounded.spans[0].len, 2);
        assert_eq!(bounded.cut, Some(Cut::Bytes));
        assert_eq!(report.cut, Some(Cut::Bytes));
    }

    #[test]
    fn greedy_regex_extent_is_degraded_at_byte_cutoff() {
        let report = scan_fixture(b"azxxz", "a.*z", Matcher::Regex, Some(64), 3);
        assert!(report.spans.is_empty(), "{report:?}");
        assert_eq!(report.matches_cut, 0);
        assert_eq!(report.cut, Some(Cut::Bytes));
        let variable = scan_fixture(b"aax", "a{1,2}", Matcher::Regex, Some(64), 1);
        assert!(variable.spans.is_empty(), "{variable:?}");
        assert_eq!(variable.cut, Some(Cut::Bytes));
    }

    #[test]
    fn complete_literal_at_cutoff_is_not_length_capped() {
        let report = scan_fixture(b"needlex", "needle", Matcher::Exact, Some(64), 6);
        assert_eq!(report.spans[0].len, 6);
        assert_eq!(report.matches_cut, 0);
    }

    #[test]
    fn a_growing_file_is_read_past_its_open_length() {
        let td = tempfile::tempdir().unwrap();
        let path = td.path().join("a.txt");
        std::fs::write(&path, b"prefix").unwrap();
        let mut fs = FileScan::open(td.path(), "a.txt").unwrap();
        use std::io::Write;
        std::fs::OpenOptions::new().append(true).open(&path).unwrap().write_all(b"needle").unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let re = compile("needle", Matcher::Exact).unwrap();
        assert!(fs.scan(&re, &limits, &mut budget, &mut report, std::time::Instant::now(), false));
        assert_eq!(report.spans.len(), 1, "{report:?}");
    }

    #[test]
    fn line_position_lookup_does_not_retain_line_entries() {
        let body = vec![b'\n'; DEFAULT_MAX_BYTES as usize];
        let mut line = 1;
        let mut column = 1;
        advance_position(&body, &mut line, &mut column);
        assert_eq!((line, column), (body.len() + 1, 1));
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
