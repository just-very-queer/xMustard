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
//! A file's answer is whole-file matching: the pattern's leftmost-first, non-overlapping
//! matches over its bytes, an empty match at the previous match's end skipped. That is the
//! iteration `regex::bytes` documents, as regex-automata's PikeVM computes it with no
//! literal optimisation, and the tests check the scan against that PikeVM. regex's own
//! `find_iter` can start a match later than that: regex-automata 0.4.14's reverse suffix
//! and inner strategies trust the first literal they find, for `(?:|ab.)c` among others.
//! A match may cross lines, and `^`/`$` anchor the file unless `(?m)` is set. When a cap
//! stops the reads inside a file, an occurrence is listed only if it is final: whole-file
//! matching reports exactly it, at that place, whatever the unread bytes hold. The first
//! occurrence that cannot be shown final ends the file's list, and `Report::unconfirmed`
//! names the byte from which its occurrences are not listed.
//!
//! A plain literal is found with memmem and every other pattern with a lazy DFA, except an
//! exact literal set whose NFA is past the lazy DFA's limit: regex answers that with a
//! literal search, and so does the scan, with Aho-Corasick. Nothing proportional to the
//! corpus is held: one file's bytes (never more than the byte cap), the lazy DFA's two
//! caches and the listed spans. A span's line and column are counted as it is listed, so
//! no per-line state exists. The lazy DFA steps one byte at a time and checks the deadline
//! every `SCAN_STEP` transitions, and the literal engines between `CHUNK_BYTES` windows,
//! so one expensive search step is interrupted rather than run to its end.

use std::ops::ControlFlow;

use aho_corasick::{AhoCorasick, AhoCorasickKind};
use memchr::memmem;
use regex::bytes::{Regex, RegexBuilder};
use regex_automata::nfa::thompson;
use regex_automata::util::syntax;
use regex_automata::{Anchored, Input, hybrid};
use regex_syntax::hir::literal::{Extractor, Seq};
use regex_syntax::hir::{Class, Hir, HirKind, Look};
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

/// Bytes per positional read, and the window of starts the literal engine searches
/// between two deadline checks.
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

/// The ceiling on the pattern's source length and on a span's reported `len`. A longer
/// pattern is refused at compile time; a longer occurrence is listed once with its length
/// capped, counted in `matches_cut` and named in `degradations`.
pub const MAX_MATCH_BYTES: usize = 4096;

/// The ceiling on line entries one scan may retain. Production retains none: a span's
/// line and column are counted as it is listed.
pub const MAX_CURSOR_LINES: usize = 1024;

/// Lazy-DFA transitions between two deadline checks: the most matching work the time cap
/// can lag behind.
const SCAN_STEP: u32 = 1024;

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

    /// The pattern `query` compiles to: escaped for `exact`, as written for `regex`.
    fn pattern(self, query: &str) -> String {
        match self {
            Matcher::Exact => regex::escape(query),
            Matcher::Regex => query.to_string(),
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
    /// Listed spans whose reported length was shortened to `MAX_MATCH_BYTES`.
    pub matches_cut: u64,
    /// The file a cap stopped inside, and the byte from which its occurrences are not
    /// listed. `None` when every file read was decided to its end.
    pub unconfirmed: Option<Unconfirmed>,
    /// Matching work done: lazy-DFA transitions plus bytes the literal engine examined.
    /// The tests read it to prove where a scan stopped.
    pub work: u64,
}

/// One file's occurrences that a cap left unconfirmed. Every listed span of the file is
/// the whole file's own; occurrences starting at or after `from` may be missing.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Unconfirmed {
    pub path: String,
    /// The first byte at which a whole-file occurrence missing from the list could start.
    pub from: usize,
    /// Bytes of the file that were read.
    pub read_to: usize,
    pub why: Why,
}

/// What stopped one file's matching short.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Why {
    ByteCap,
    TimeCap,
    /// The engine failed. Unreachable with the lane's configuration (no quit bytes, no
    /// give-up count), and reported rather than panicked on if it happens.
    Matcher(String),
}

impl Unconfirmed {
    /// The text `degradations` carries, naming the file and the byte it is missing from.
    pub fn message(&self) -> String {
        let Unconfirmed {
            path,
            from,
            read_to,
            why,
        } = self;
        match why {
            Why::ByteCap => format!(
                "{path}: the byte cap stopped the scan after {read_to} bytes of this file; occurrences starting at or after byte {from} were not confirmed and are not listed"
            ),
            Why::TimeCap => format!(
                "{path}: the time cap stopped the scan after {read_to} bytes of this file; occurrences starting at or after byte {from} were not searched"
            ),
            Why::Matcher(e) => format!(
                "{path}: the matcher failed after {read_to} bytes of this file ({e}); occurrences starting at or after byte {from} were not searched"
            ),
        }
    }
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

/// When the scan must stop. Production reads the wall clock; `never` and `after_checks`
/// are the deterministic seams the tests use, so no test races the clock.
#[derive(Debug, Clone)]
pub struct Deadline {
    clock: Clock,
    checks: u64,
    expired: bool,
}

#[derive(Debug, Clone, Copy)]
enum Clock {
    /// The instant the time cap ends.
    Wall(std::time::Instant),
    Never,
    /// Expire on the check after this many.
    After(u64),
}

impl Deadline {
    /// The time cap, counted from `started`.
    pub fn wall(started: std::time::Instant, timeout_ms: u64) -> Self {
        let end = started
            .checked_add(std::time::Duration::from_millis(timeout_ms))
            .unwrap_or(started);
        Deadline::on(Clock::Wall(end))
    }

    /// A deadline that never fires.
    pub fn never() -> Self {
        Deadline::on(Clock::Never)
    }

    /// A deadline that fires on check `n + 1`, whatever the clock says.
    pub fn after_checks(n: u64) -> Self {
        Deadline::on(Clock::After(n))
    }

    fn on(clock: Clock) -> Self {
        Deadline {
            clock,
            checks: 0,
            expired: false,
        }
    }

    /// Count one check. True from the first check that finds the deadline passed.
    pub fn expired(&mut self) -> bool {
        self.checks += 1;
        self.expired = self.expired
            || match self.clock {
                Clock::Wall(end) => std::time::Instant::now() >= end,
                Clock::Never => false,
                Clock::After(n) => self.checks > n,
            };
        self.expired
    }

    /// Checks counted so far.
    pub fn checks(&self) -> u64 {
        self.checks
    }
}

/// The match phase's view of the deadline: a check every `SCAN_STEP` lazy-DFA
/// transitions, and the work done.
struct Work<'d> {
    deadline: &'d mut Deadline,
    units: u64,
    until_check: u32,
}

impl<'d> Work<'d> {
    fn new(deadline: &'d mut Deadline) -> Self {
        Work {
            deadline,
            units: 0,
            until_check: SCAN_STEP,
        }
    }

    /// Count one lazy-DFA transition; true when the deadline fired.
    fn tick(&mut self) -> bool {
        self.units += 1;
        self.until_check -= 1;
        if self.until_check > 0 {
            return false;
        }
        self.until_check = SCAN_STEP;
        self.deadline.expired()
    }

    fn check(&mut self) -> bool {
        self.deadline.expired()
    }
}

/// A compiled query: the engine the scan runs and what the pattern's shape proves at a
/// cutoff.
#[derive(Debug)]
pub struct Pattern {
    engine: Engine,
    shape: Shape,
}

#[derive(Debug)]
enum Engine {
    /// A non-empty literal, found with memmem.
    Literal(Box<memmem::Finder<'static>>),
    /// An exact set of non-empty literals whose NFA is past the lazy DFA's limit, found
    /// with leftmost-first Aho-Corasick.
    Set(Box<AhoCorasick>),
    /// Everything else: a lazy DFA, forward leftmost-first and reverse for the start.
    Dfa(Box<hybrid::regex::Regex>),
}

impl Pattern {
    /// The matcher state one scan runs with, built once and reused across its files.
    pub fn scanner(&self) -> Scanner<'_> {
        let search = match &self.engine {
            Engine::Literal(finder) => Search::Literal(finder),
            Engine::Set(set) => Search::Set(set),
            Engine::Dfa(re) => Search::Dfa {
                re,
                cache: Box::new(re.create_cache()),
            },
        };
        Scanner {
            shape: self.shape,
            search,
        }
    }
}

/// Compile `query` for `matcher`, or the engine's complaint. A pattern the engine refuses
/// is bad input, not a query that matches nothing, so the caller reports it as one.
///
/// `exact` compiles the query through `regex::escape`, so a query carrying regex
/// metacharacters (`.`, `*`, `(`) matches the bytes it names rather than being read as a
/// pattern — which is what "exact" has to mean to differ from "regex".
pub fn compile(query: &str, matcher: Matcher) -> Result<Pattern, String> {
    let pattern = matcher.pattern(query);
    if pattern.len() > MAX_MATCH_BYTES {
        return Err(format!(
            "{} pattern is longer than the {MAX_MATCH_BYTES}-byte ceiling",
            matcher.name()
        ));
    }
    let invalid =
        |e: &dyn std::fmt::Display| format!("{} pattern is not valid: {e}", matcher.name());
    // The regex crate's build is the validation, with its messages and its size limit;
    // the scan never searches with it.
    crate_regex(&pattern).map_err(|e| invalid(&e))?;
    let hir = parse(&pattern).map_err(|e| invalid(&e))?;
    let unsupported = |e: String| {
        format!(
            "{} pattern is not supported by the scan: {e}",
            matcher.name()
        )
    };
    let engine = match hir.kind() {
        HirKind::Literal(lit) if !lit.0.is_empty() => {
            Engine::Literal(Box::new(memmem::Finder::new(&*lit.0).into_owned()))
        }
        _ => match lazy_dfa(&pattern) {
            Ok(re) => Engine::Dfa(Box::new(re)),
            // regex answers an exact literal set with a literal search and builds no NFA
            // for it, so a set past the lazy DFA's NFA limit is answered the same way
            Err(e) => Engine::Set(Box::new(literal_set(&hir).ok_or_else(|| unsupported(e))?)),
        },
    };
    Ok(Pattern {
        engine,
        shape: Shape::of(&hir),
    })
}

/// `regex::bytes` over `pattern` with the lane's options.
fn crate_regex(pattern: &str) -> Result<Regex, regex::Error> {
    RegexBuilder::new(pattern)
        // `unicode(false)` is `index::lexical`'s choice for the same reason: the Unicode
        // classes compile to automata of several MiB, and this lane reads bytes.
        .unicode(false)
        .dfa_size_limit(DFA_CACHE_BYTES)
        .size_limit(MAX_MATCH_BYTES * 8)
        .build()
}

/// `pattern` under the syntax options `regex::bytes` parses with, so the shape and the
/// engine read the pattern the crate validated.
fn parse(pattern: &str) -> Result<Hir, String> {
    regex_syntax::ParserBuilder::new()
        .unicode(false)
        .utf8(false)
        .build()
        .parse(pattern)
        .map_err(|e| e.to_string())
}

/// The lazy DFA for `pattern` under the lane's options. No captures, no prefilter, no
/// quit bytes and no give-up count, so a search never fails; each cache is held to
/// `DFA_CACHE_BYTES`, or the engine's minimum when that is larger. It refuses a Unicode
/// word boundary, which a lazy DFA cannot evaluate, and an NFA past the limit regex holds
/// its own to, which a pattern regex builds reaches only as an exact literal set.
fn lazy_dfa(pattern: &str) -> Result<hybrid::regex::Regex, String> {
    hybrid::regex::Regex::builder()
        .syntax(syntax::Config::new().unicode(false).utf8(false))
        .thompson(
            thompson::Config::new()
                .utf8(false)
                .which_captures(thompson::WhichCaptures::None)
                .nfa_size_limit(Some(MAX_MATCH_BYTES * 8)),
        )
        .dfa(
            hybrid::dfa::Config::new()
                .cache_capacity(DFA_CACHE_BYTES)
                .skip_cache_capacity_check(true)
                .prefilter(None),
        )
        .build(pattern)
        .map_err(|e| match std::error::Error::source(&e) {
            Some(cause) => format!("{e}: {cause}"),
            None => e.to_string(),
        })
}

/// Leftmost-first Aho-Corasick over the literals `hir` matches, when it matches exactly a
/// finite set of non-empty strings and asserts nothing. regex-syntax guarantees that a
/// preference-order search of such a set is a leftmost-first search of the pattern. The
/// set is extracted and minimized as regex-automata's literal strategy extracts it, so
/// every pattern regex answers with a literal search alone has one, and the extraction's
/// limits (250 literals of at most 100 bytes) bound the automaton.
fn literal_set(hir: &Hir) -> Option<AhoCorasick> {
    if !hir.properties().look_set().is_empty() {
        return None;
    }
    let mut seq = Seq::empty();
    seq.union(&mut Extractor::new().extract(hir));
    seq.optimize_for_prefix_by_preference();
    let lits = seq.literals().filter(|_| seq.is_exact())?;
    if lits.is_empty() || lits.iter().any(|lit| lit.is_empty()) {
        return None;
    }
    AhoCorasick::builder()
        .kind(Some(AhoCorasickKind::ContiguousNFA))
        .match_kind(aho_corasick::MatchKind::LeftmostFirst)
        .build(lits)
        .ok()
}

/// What the pattern's syntax proves about an occurrence the bytes read cannot see past.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Shape {
    /// No thread starting at `s` reads a byte at or past `s + horizon`: the longest match,
    /// or one past the furthest look-ahead assertion. `None` when unbounded.
    horizon: Option<usize>,
    /// No literal or class can consume `\n`, so every thread dies at its line's end.
    line_local: bool,
}

impl Shape {
    fn of(hir: &Hir) -> Shape {
        let horizon = match (hir.properties().maximum_len(), ahead(hir)) {
            (Some(longest), Reach::None) => Some(longest),
            (Some(longest), Reach::At(r)) => Some(longest.max(r.saturating_add(1))),
            _ => None,
        };
        Shape {
            horizon,
            line_local: !consumes_newline(hir),
        }
    }

    /// Whether an occurrence starting at `s` is decided by the bytes read: every thread
    /// starting at or before it dies or settles inside them.
    fn final_at(self, s: usize, buf: Buffer<'_>) -> bool {
        self.horizon
            .is_some_and(|h| s.saturating_add(h) <= buf.hay.len())
            || (self.line_local && buf.last_nl.is_some_and(|l| l >= s))
    }

    /// The first byte a whole-file occurrence after `p` could start at without the bytes
    /// read having decided it.
    fn frontier(self, p: usize, buf: Buffer<'_>) -> usize {
        let past_horizon = self
            .horizon
            .map_or(0, |h| (buf.hay.len() + 1).saturating_sub(h));
        let past_line = match (self.line_local, buf.last_nl) {
            (true, Some(l)) => l + 1,
            _ => 0,
        };
        p.max(past_horizon).max(past_line)
    }
}

/// The furthest offset from a match start at which a look-ahead assertion is evaluated.
/// Ordered: no assertion, a bounded offset, no bound.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum Reach {
    None,
    At(usize),
    Unbounded,
}

fn ahead(hir: &Hir) -> Reach {
    match hir.kind() {
        HirKind::Empty | HirKind::Literal(_) | HirKind::Class(_) => Reach::None,
        HirKind::Look(look) if reads_ahead(*look) => Reach::At(0),
        HirKind::Look(_) => Reach::None,
        HirKind::Capture(cap) => ahead(&cap.sub),
        // an assertion inside a repetition can sit at the end of its last iteration
        HirKind::Repetition(rep) if ahead(&rep.sub) != Reach::None => hir
            .properties()
            .maximum_len()
            .map_or(Reach::Unbounded, Reach::At),
        HirKind::Repetition(_) => Reach::None,
        HirKind::Alternation(subs) => subs.iter().map(ahead).max().unwrap_or(Reach::None),
        HirKind::Concat(subs) => {
            let mut offset = Some(0usize);
            let mut reach = Reach::None;
            for sub in subs {
                let here = match (ahead(sub), offset) {
                    (Reach::None, _) => Reach::None,
                    (Reach::At(r), Some(o)) => o.checked_add(r).map_or(Reach::Unbounded, Reach::At),
                    _ => Reach::Unbounded,
                };
                reach = reach.max(here);
                offset = offset
                    .zip(sub.properties().maximum_len())
                    .and_then(|(o, longest)| o.checked_add(longest));
            }
            reach
        }
    }
}

/// Whether `look` reads the byte at its own position, not only the one before it. No
/// wildcard arm, so an assertion added upstream fails to compile here.
fn reads_ahead(look: Look) -> bool {
    match look {
        Look::Start | Look::StartLF | Look::WordStartHalfAscii | Look::WordStartHalfUnicode => {
            false
        }
        Look::End
        | Look::EndLF
        | Look::StartCRLF
        | Look::EndCRLF
        | Look::WordAscii
        | Look::WordAsciiNegate
        | Look::WordUnicode
        | Look::WordUnicodeNegate
        | Look::WordStartAscii
        | Look::WordEndAscii
        | Look::WordStartUnicode
        | Look::WordEndUnicode
        | Look::WordEndHalfAscii
        | Look::WordEndHalfUnicode => true,
    }
}

/// Whether some literal or class in `hir` can consume `\n`.
fn consumes_newline(hir: &Hir) -> bool {
    match hir.kind() {
        HirKind::Empty | HirKind::Look(_) => false,
        HirKind::Literal(lit) => lit.0.contains(&b'\n'),
        HirKind::Class(Class::Bytes(class)) => class
            .ranges()
            .iter()
            .any(|r| (r.start()..=r.end()).contains(&b'\n')),
        HirKind::Class(Class::Unicode(class)) => class
            .ranges()
            .iter()
            .any(|r| (r.start()..=r.end()).contains(&'\n')),
        HirKind::Repetition(rep) => rep.max != Some(0) && consumes_newline(&rep.sub),
        HirKind::Capture(cap) => consumes_newline(&cap.sub),
        HirKind::Concat(subs) | HirKind::Alternation(subs) => subs.iter().any(consumes_newline),
    }
}

/// One scan's matcher state, built once and reused across its files.
pub struct Scanner<'p> {
    shape: Shape,
    search: Search<'p>,
}

enum Search<'p> {
    Literal(&'p memmem::Finder<'static>),
    Set(&'p AhoCorasick),
    Dfa {
        re: &'p hybrid::regex::Regex,
        /// The forward and reverse caches, each at most `DFA_CACHE_BYTES` or the engine's
        /// minimum.
        cache: Box<hybrid::regex::Cache>,
    },
}

impl Scanner<'_> {
    /// Heap bytes the lazy DFA's two caches hold; zero for a literal or a literal set,
    /// whose searches keep no state.
    pub fn memory_usage(&self) -> usize {
        match &self.search {
            Search::Literal(_) | Search::Set(_) => 0,
            Search::Dfa { cache, .. } => cache.memory_usage(),
        }
    }

    /// The next whole-file occurrence at or after `p`, as far as `buf` decides it.
    fn step(&mut self, buf: Buffer<'_>, p: usize, work: &mut Work<'_>) -> Result<Next, String> {
        let shape = self.shape;
        match &mut self.search {
            Search::Literal(finder) => {
                let len = finder.needle().len();
                literal_step(len, shape, buf, p, work, |q, end| {
                    Ok(finder.find(&buf.hay[q..end]).map(|i| (q + i, q + i + len)))
                })
            }
            Search::Set(set) => {
                literal_step(set.max_pattern_len(), shape, buf, p, work, |q, end| {
                    set.try_find(aho_corasick::Input::new(buf.hay).span(q..end))
                        .map(|m| m.map(|m| (m.start(), m.end())))
                        .map_err(|e| e.to_string())
                })
            }
            Search::Dfa { re, cache } => dfa_step(re, cache, shape, buf, p, work),
        }
    }
}

/// How a file's buffer ends: at the file's end, or where a cap stopped the reads.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Ending {
    Eof,
    Cutoff,
}

/// One file's bytes as the matcher sees them.
#[derive(Clone, Copy)]
struct Buffer<'h> {
    hay: &'h [u8],
    ending: Ending,
    /// The last `\n` read: a line-local pattern's threads before it have all died.
    last_nl: Option<usize>,
}

/// What matching one file's buffer settled.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Outcome {
    /// Every occurrence was emitted: the file ended, or the matcher proved no occurrence
    /// starts in the unread rest.
    Complete,
    /// `emit` stopped the scan.
    Stopped,
    /// The next occurrence depends on unread bytes. No whole-file occurrence starting
    /// before `from` is missing from what was emitted.
    Unconfirmed { from: usize },
    /// The deadline fired while searching from `from`.
    Interrupted { from: usize },
    /// The engine failed while searching from `from`.
    Halt { from: usize, error: String },
}

/// The next occurrence at or after a search start, as far as the bytes read decide it.
#[derive(Debug)]
enum Next {
    /// Whole-file matching reports exactly this occurrence next.
    Final(usize, usize),
    /// No occurrence starts at or after the search start, anywhere in the file.
    None,
    /// The next occurrence depends on bytes not read.
    Open,
    /// The deadline fired inside the step.
    Interrupted,
}

/// `find_iter` over `hay`, emitting an occurrence only once it is final. At `Eof` every
/// occurrence is. At a `Cutoff` one is when the forward automaton died on a byte read, or
/// when the pattern's shape bounds, inside the bytes read, every thread that could change
/// it; the first one that is not ends the list.
fn match_buffer(
    sc: &mut Scanner<'_>,
    hay: &[u8],
    ending: Ending,
    work: &mut Work<'_>,
    mut emit: impl FnMut(usize, usize) -> ControlFlow<()>,
) -> Outcome {
    let buf = Buffer {
        hay,
        ending,
        last_nl: memchr::memrchr(b'\n', hay),
    };
    let mut p = 0;
    let mut last_end = None;
    loop {
        if p > hay.len() {
            return match ending {
                Ending::Eof => Outcome::Complete,
                Ending::Cutoff => Outcome::Unconfirmed { from: p },
            };
        }
        if work.check() {
            return Outcome::Interrupted { from: p };
        }
        let (s, e) = match sc.step(buf, p, work) {
            Ok(Next::Final(s, e)) => (s, e),
            Ok(Next::None) => return Outcome::Complete,
            Ok(Next::Open) => {
                return Outcome::Unconfirmed {
                    from: sc.shape.frontier(p, buf),
                };
            }
            Ok(Next::Interrupted) => return Outcome::Interrupted { from: p },
            Err(error) => return Outcome::Halt { from: p, error },
        };
        // find_iter skips an empty match at the previous match's end and retries one byte
        // on. Only a final candidate gets here: skipping an open one could hide a longer
        // match at the same start.
        if s == e && last_end == Some(e) {
            p = e + 1;
            continue;
        }
        if emit(s, e).is_break() {
            return Outcome::Stopped;
        }
        last_end = Some(e);
        p = e;
    }
}

/// The leftmost-first occurrence of a literal or a literal set at or after `p`, searched
/// `CHUNK_BYTES` starts at a time with a deadline check between windows. `find` searches
/// the window `q..end`, which holds every literal starting in its first `CHUNK_BYTES`
/// bytes. A literal found wholly inside the bytes read can still lose, at a cutoff, to a
/// longer one preferred at or before its start, so the shape decides it there; a single
/// literal's own length always settles it.
fn literal_step(
    longest: usize,
    shape: Shape,
    buf: Buffer<'_>,
    p: usize,
    work: &mut Work<'_>,
    mut find: impl FnMut(usize, usize) -> Result<Option<(usize, usize)>, String>,
) -> Result<Next, String> {
    let n = buf.hay.len();
    let mut q = p;
    loop {
        let end = n.min(q + CHUNK_BYTES + longest - 1);
        work.units += (end - q) as u64;
        match find(q, end)? {
            // every literal starting at or before `s` fits in the window, or the window
            // ends where the bytes read do
            Some((s, e)) if s + longest <= end || end == n => {
                let decided = buf.ending == Ending::Eof;
                return Ok(if decided || shape.final_at(s, buf) {
                    Next::Final(s, e)
                } else {
                    Next::Open
                });
            }
            None if end == n => {
                return Ok(match buf.ending {
                    Ending::Eof => Next::None,
                    Ending::Cutoff => Next::Open,
                });
            }
            // no occurrence starts in the window's first `CHUNK_BYTES` bytes
            _ => {}
        }
        q += CHUNK_BYTES;
        if work.check() {
            return Ok(Next::Interrupted);
        }
    }
}

/// One `find` of the lazy-DFA regex from `p`: the forward search for the end, then the
/// reverse search for the start, as regex-automata's `hybrid::regex::Regex::try_search`
/// runs them, with finality decided before the candidate is returned.
fn dfa_step(
    re: &hybrid::regex::Regex,
    cache: &mut hybrid::regex::Cache,
    shape: Shape,
    buf: Buffer<'_>,
    p: usize,
    work: &mut Work<'_>,
) -> Result<Next, String> {
    let (fcache, rcache) = cache.as_parts_mut();
    let (e, decided) = match forward(re.forward(), fcache, buf, p, work)? {
        Forward::Interrupted => return Ok(Next::Interrupted),
        Forward::Decided(None) => return Ok(Next::None),
        Forward::Decided(Some(e)) => (e, true),
        Forward::Pending {
            eoi_match: true, ..
        } => (buf.hay.len(), false),
        Forward::Pending { mat: Some(e), .. } => (e, false),
        Forward::Pending { mat: None, .. } => return Ok(Next::Open),
    };
    let Some(s) = reverse(re, rcache, buf.hay, p, e, work)? else {
        return Ok(Next::Interrupted);
    };
    Ok(if decided || shape.final_at(s, buf) {
        Next::Final(s, e)
    } else {
        Next::Open
    })
}

/// What a forward search settled.
enum Forward {
    /// The search ended inside the bytes read (on a dead state, or at the file's end):
    /// the end of the next match, or that there is none, is the whole file's.
    Decided(Option<usize>),
    /// Alive at the cutoff: the last match end seen, and whether the cutoff would end a
    /// match were it the end of the file.
    Pending {
        mat: Option<usize>,
        eoi_match: bool,
    },
    Interrupted,
}

/// A leftmost-first forward search from `p`, one transition at a time: regex-automata
/// 0.4's `find_fwd_imp` and `eoi_fwd` without the prefilter and the unrolled loop.
///
/// Only the most recent state ID a cache returned is valid, so every call below takes
/// the ID the previous call returned; nothing branches from an older state.
fn forward(
    dfa: &hybrid::dfa::DFA,
    cache: &mut hybrid::dfa::Cache,
    buf: Buffer<'_>,
    p: usize,
    work: &mut Work<'_>,
) -> Result<Forward, String> {
    let hay = buf.hay;
    let mut sid = dfa
        .start_state_forward(cache, &Input::new(hay).range(p..))
        .map_err(|e| e.to_string())?;
    if sid.is_dead() {
        return Ok(Forward::Decided(None));
    }
    let mut mat = None;
    for (at, &byte) in (p..).zip(&hay[p..]) {
        if work.tick() {
            return Ok(Forward::Interrupted);
        }
        sid = dfa
            .next_state(cache, sid, byte)
            .map_err(|e| e.to_string())?;
        // matches are delayed a byte, so a match state here ends a match at `at`
        if sid.is_match() {
            mat = Some(at);
        } else if sid.is_dead() {
            return Ok(Forward::Decided(mat));
        } else if sid.is_quit() {
            return Err(format!("the lazy DFA quit at byte {at}"));
        }
    }
    if work.tick() {
        return Ok(Forward::Interrupted);
    }
    let eoi_match = dfa
        .next_eoi_state(cache, sid)
        .map_err(|e| e.to_string())?
        .is_match();
    Ok(match buf.ending {
        Ending::Eof => Forward::Decided(if eoi_match { Some(hay.len()) } else { mat }),
        Ending::Cutoff => Forward::Pending { mat, eoi_match },
    })
}

/// The start of the match a forward search from `p` found ending at `e`: the reverse
/// DFA anchored at `e`, with `hay[e]` as its context (regex-automata 0.4's `try_search`,
/// `find_rev_imp` and `eoi_rev`). `None` when the deadline fired.
fn reverse(
    re: &hybrid::regex::Regex,
    cache: &mut hybrid::dfa::Cache,
    hay: &[u8],
    p: usize,
    e: usize,
    work: &mut Work<'_>,
) -> Result<Option<usize>, String> {
    // an empty match at the search start, or an always-anchored pattern, starts at `p`
    if e == p || re.forward().get_nfa().is_always_start_anchored() {
        return Ok(Some(p));
    }
    let dfa = re.reverse();
    let input = Input::new(hay).range(p..e).anchored(Anchored::Yes);
    let mut sid = dfa
        .start_state_reverse(cache, &input)
        .map_err(|e| e.to_string())?;
    let mut mat = None;
    let mut alive = true;
    for at in (p..e).rev() {
        if work.tick() {
            return Ok(None);
        }
        sid = dfa
            .next_state(cache, sid, hay[at])
            .map_err(|e| e.to_string())?;
        if sid.is_match() {
            mat = Some(at + 1);
        } else if sid.is_dead() {
            alive = false;
            break;
        } else if sid.is_quit() {
            return Err(format!("the lazy DFA quit at byte {at}"));
        }
    }
    if alive {
        if work.tick() {
            return Ok(None);
        }
        sid = match p.checked_sub(1) {
            Some(before) => dfa.next_state(cache, sid, hay[before]),
            None => dfa.next_eoi_state(cache, sid),
        }
        .map_err(|e| e.to_string())?;
        if sid.is_match() {
            mat = Some(p);
        }
    }
    mat.map(Some)
        .ok_or_else(|| format!("the reverse search found no start for the match ending at {e}"))
}

/// The 1-based line and byte column of an offset that only moves forward. Constant size:
/// the line number is carried, not looked up.
struct Tracker {
    line: usize,
    column: usize,
    at: usize,
}

impl Default for Tracker {
    fn default() -> Self {
        Tracker {
            line: 1,
            column: 1,
            at: 0,
        }
    }
}

impl Tracker {
    /// Move to `to`, never behind the last offset, and return its position.
    fn advance(&mut self, hay: &[u8], to: usize) -> (usize, usize) {
        let gap = &hay[self.at..to];
        match memchr::memrchr(b'\n', gap) {
            Some(last) => {
                self.line += memchr::memchr_iter(b'\n', gap).count();
                self.column = gap.len() - last;
            }
            None => self.column += gap.len(),
        }
        self.at = to;
        (self.line, self.column)
    }
}

/// One open file, read positionally so the scan never seeks and never maps. The fd came
/// from `open_repo_regular_file_with_meta`, so no symlink component was followed and the
/// held descriptor is a regular file.
pub struct FileScan<'a> {
    file: std::fs::File,
    /// Bytes of this file already read.
    pub read: u64,
    /// The file's length when it was opened: the buffer's reservation, never proof of its
    /// end, which only a read or the open descriptor's length gives.
    len_hint: u64,
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
            len_hint: meta.len(),
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

    /// Match this file against `sc`'s pattern, appending to `report` and returning `false`
    /// once `limits` or `deadline` cut the scan. `budget` is the whole scan's remaining
    /// byte allowance, so the byte cap spans files rather than restarting per file.
    ///
    /// The bytes read are matched as one haystack, so greedy extents, non-overlapping
    /// iteration and the file's assertions hold across read boundaries. At a cutoff only
    /// final occurrences are listed, and the rest is recorded in `report.unconfirmed`.
    pub fn scan(
        &mut self,
        sc: &mut Scanner<'_>,
        limits: &Limits,
        budget: &mut u64,
        report: &mut Report,
        deadline: &mut Deadline,
        more_files: bool,
    ) -> bool {
        let Some((body, eof)) = self.fill(budget, report, deadline) else {
            report.files_skipped_unreadable += 1;
            return true;
        };
        let ending = if eof { Ending::Eof } else { Ending::Cutoff };
        let mut work = Work::new(deadline);
        let mut position = Tracker::default();
        let path = &self.path;
        let outcome = match_buffer(sc, &body, ending, &mut work, |s, e| {
            if report.spans.len() >= limits.max_lines {
                report.cut = Some(Cut::Lines);
                return ControlFlow::Break(());
            }
            let (line, column) = position.advance(&body, s);
            report.spans.push(Span {
                path: path.clone(),
                line,
                column,
                byte: s,
                len: (e - s).min(MAX_MATCH_BYTES),
            });
            if e - s > MAX_MATCH_BYTES {
                report.matches_cut += 1;
            }
            ControlFlow::Continue(())
        });
        report.work += work.units;
        let decided = eof || outcome == Outcome::Complete;
        let unconfirmed = |from, why| {
            Some(Unconfirmed {
                path: path.clone(),
                from,
                read_to: body.len(),
                why,
            })
        };
        match outcome {
            Outcome::Complete => {}
            Outcome::Stopped => return false,
            Outcome::Unconfirmed { from } => {
                let why = match report.cut {
                    Some(Cut::Time) => Why::TimeCap,
                    _ => Why::ByteCap,
                };
                report.unconfirmed = unconfirmed(from, why);
            }
            Outcome::Interrupted { from } => {
                report.cut = Some(Cut::Time);
                report.unconfirmed = unconfirmed(from, Why::TimeCap);
            }
            Outcome::Halt { from, error } => {
                report.unconfirmed = unconfirmed(from, Why::Matcher(error));
                return false;
            }
        }
        // The byte cap cut the answer when it left this file undecided, or files remain.
        if report.cut.is_none()
            && (!decided || more_files)
            && report.bytes_scanned >= limits.max_bytes
        {
            report.cut = Some(Cut::Bytes);
        }
        report.cut.is_none()
    }

    /// This file's bytes, read positionally into one buffer of at most `budget` bytes, and
    /// whether the reads reached its end. `None` when a read failed.
    fn fill(
        &mut self,
        budget: &mut u64,
        report: &mut Report,
        deadline: &mut Deadline,
    ) -> Option<(Vec<u8>, bool)> {
        // Reserved once, at the length the file had when it was opened, so the buffer is
        // never copied to grow unless the file grew.
        let mut body = Vec::with_capacity(usize::try_from((*budget).min(self.len_hint)).ok()?);
        loop {
            let want = usize::try_from(*budget).map_or(self.slice, |b| b.min(self.slice));
            if want == 0 {
                // The budget ended here: the open file's length says whether the file did.
                let eof = self.file.metadata().is_ok_and(|m| m.len() <= self.read);
                return Some((body, eof));
            }
            let at = body.len();
            let got = if at < body.capacity() {
                body.resize(at + want.min(body.capacity() - at), 0);
                let got = self.read_at(&mut body[at..]).ok()?;
                body.truncate(at + got);
                got
            } else {
                // Full at the length the file had when it was opened: one byte says whether
                // it grew since.
                let mut probe = [0u8; 1];
                let got = self.read_at(&mut probe).ok()?;
                if got == 1 {
                    let left = usize::try_from(*budget).unwrap_or(usize::MAX);
                    body.reserve_exact(want.max(at).min(left));
                    body.push(probe[0]);
                }
                got
            };
            if got == 0 {
                return Some((body, true));
            }
            self.read += got as u64;
            *budget -= got as u64;
            report.bytes_scanned += got as u64;
            if deadline.expired() {
                report.cut = Some(Cut::Time);
                return Some((body, false));
            }
        }
    }

    /// One positional read at the current offset.
    #[cfg(unix)]
    fn read_at(&self, buf: &mut [u8]) -> std::io::Result<usize> {
        use std::os::unix::fs::FileExt;
        self.file.read_at(buf, self.read)
    }

    /// Non-unix fallback: the same bounded read through `Seek`.
    #[cfg(not(unix))]
    fn read_at(&self, buf: &mut [u8]) -> std::io::Result<usize> {
        use std::io::{Read, Seek, SeekFrom};
        (&self.file).seek(SeekFrom::Start(self.read))?;
        (&self.file).read(buf)
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
    use regex_automata::nfa::thompson::pikevm::{self, PikeVM};

    /// No occurrence, typed for `assert_eq!`.
    const NOTHING: [(usize, usize); 0] = [];

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
        assert_eq!(
            found_in(&exact, b"abc"),
            NOTHING,
            "a.c is literal, so it does not match abc"
        );
        assert_eq!(
            found_in(&exact, b"a.c"),
            [(0, 3)],
            "a.c is literal, so it matches a.c"
        );
        let re = compile("a.c", Matcher::Regex).unwrap();
        assert_eq!(
            found_in(&re, b"abc"),
            [(0, 3)],
            "a.c is a pattern, so it matches abc"
        );
        assert_eq!(
            found_in(&re, b"a.c"),
            [(0, 3)],
            "a.c is a pattern, so it matches a.c"
        );
    }

    // A pattern the engine refuses is bad input, not a query matching nothing, so the
    // error carries the engine's own message rather than a bare "no match".
    #[test]
    fn an_uncompilable_pattern_is_refused_with_the_engines_message() {
        let e = compile("a(", Matcher::Regex).unwrap_err();
        assert!(e.starts_with("regex pattern is not valid"), "{e}");
    }

    // The pattern's source is bounded, so the compiled automaton is too: a longer one is
    // refused at compile time rather than built.
    #[test]
    fn an_over_long_pattern_is_refused_before_any_byte_is_read() {
        let long = "a".repeat(MAX_MATCH_BYTES + 1);
        let e = compile(&long, Matcher::Regex).unwrap_err();
        assert!(e.contains(&format!("{MAX_MATCH_BYTES}")), "{e}");
    }

    #[test]
    fn case_sensitivity_follows_the_matcher_and_is_not_inferred() {
        let exact = compile("Needle", Matcher::Exact).unwrap();
        assert_eq!(found_in(&exact, b"a Needle here"), [(2, 8)]);
        assert_eq!(
            found_in(&exact, b"a needle here"),
            NOTHING,
            "exact is case-sensitive, so it does not match a lowercase needle"
        );
        let re = compile("(?i)needle", Matcher::Regex).unwrap();
        assert_eq!(
            found_in(&re, b"a Needle here"),
            [(2, 8)],
            "regex takes its own flags, so (?i) matches a capital N"
        );
    }

    // The lazy DFA cannot evaluate a Unicode word boundary, so the scan refuses one as it
    // refuses any pattern it cannot answer, rather than answering it wrongly.
    #[test]
    fn a_unicode_word_boundary_is_refused() {
        let e = compile(r"(?u)\bfoo", Matcher::Regex).unwrap_err();
        assert!(e.contains("not supported by the scan"), "{e}");
        assert!(e.contains("Unicode word boundar"), "{e}");
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

    /// `a.txt` holding `body`, scanned once under `limits` and `deadline`: whether the
    /// scan may go on, and what it reported.
    fn run(
        body: &[u8],
        query: &str,
        matcher: Matcher,
        slice: Option<usize>,
        limits: Limits,
        deadline: &mut Deadline,
    ) -> (bool, Report) {
        let td = tempfile::tempdir().unwrap();
        std::fs::write(td.path().join("a.txt"), body).unwrap();
        let mut fs = FileScan::open_with_slice(td.path(), "a.txt", slice).unwrap();
        let pattern = compile(query, matcher).unwrap();
        let mut sc = pattern.scanner();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let more = fs.scan(&mut sc, &limits, &mut budget, &mut report, deadline, false);
        (more, report)
    }

    /// [`run`] under `max_bytes` and the other caps' defaults, with a deadline that never
    /// fires.
    fn scan_fixture(
        body: &[u8],
        query: &str,
        matcher: Matcher,
        slice: Option<usize>,
        max_bytes: u64,
    ) -> Report {
        let limits = Limits {
            max_bytes,
            ..Limits::default()
        };
        run(body, query, matcher, slice, limits, &mut Deadline::never()).1
    }

    /// Each span's (byte, len).
    fn found(report: &Report) -> Vec<(usize, usize)> {
        report.spans.iter().map(|s| (s.byte, s.len)).collect()
    }

    /// The (start, end) of each occurrence `match_buffer` lists in a whole file.
    fn found_in(pattern: &Pattern, hay: &[u8]) -> Vec<(usize, usize)> {
        run_buffer(&mut pattern.scanner(), hay, Ending::Eof).0
    }

    /// The record a cap leaves for the fixture file `a.txt`.
    fn unconfirmed(from: usize, read_to: usize, why: Why) -> Option<Unconfirmed> {
        Some(Unconfirmed {
            path: "a.txt".into(),
            from,
            read_to,
            why,
        })
    }

    /// `match_buffer` over `hay` with no line cap and a deadline that never fires.
    fn run_buffer(
        sc: &mut Scanner<'_>,
        hay: &[u8],
        ending: Ending,
    ) -> (Vec<(usize, usize)>, Outcome) {
        let mut deadline = Deadline::never();
        let mut listed = Vec::new();
        let outcome = match_buffer(sc, hay, ending, &mut Work::new(&mut deadline), |s, e| {
            listed.push((s, e));
            ControlFlow::Continue(())
        });
        (listed, outcome)
    }

    /// The whole-file answer, as (start, end): regex-automata's PikeVM, leftmost-first
    /// with no literal optimisation, under the options `regex::bytes` builds with.
    struct Oracle {
        vm: PikeVM,
        cache: pikevm::Cache,
    }

    impl Oracle {
        fn new(query: &str, matcher: Matcher) -> Oracle {
            let vm = PikeVM::builder()
                .syntax(syntax::Config::new().unicode(false).utf8(false))
                .thompson(thompson::Config::new().utf8(false).nfa_size_limit(None))
                .build(&matcher.pattern(query))
                .unwrap();
            let cache = vm.create_cache();
            Oracle { vm, cache }
        }

        fn find_all(&mut self, hay: &[u8]) -> Vec<(usize, usize)> {
            self.vm
                .find_iter(&mut self.cache, hay)
                .map(|m| (m.start(), m.end()))
                .collect()
        }
    }

    impl Engine {
        /// The engine's name, for an assertion message.
        fn name(&self) -> &'static str {
            match self {
                Engine::Literal(_) => "memmem",
                Engine::Set(_) => "aho-corasick",
                Engine::Dfa(_) => "the lazy DFA",
            }
        }
    }

    /// `query` on the literal-set engine whatever the size of its NFA, when it is an
    /// exact literal set: the lazy DFA's patterns are the set engine's controls.
    fn as_set(query: &str, matcher: Matcher) -> Option<Pattern> {
        let hir = parse(&matcher.pattern(query)).unwrap();
        Some(Pattern {
            engine: Engine::Set(Box::new(literal_set(&hir)?)),
            shape: Shape::of(&hir),
        })
    }

    /// The engines `query` can run on: the one `compile` picks, then the literal-set
    /// engine when the pattern is an exact literal set the compiled one is not.
    fn engines(query: &str, matcher: Matcher) -> Vec<Pattern> {
        let compiled = compile(query, matcher).unwrap();
        let set = match compiled.engine {
            Engine::Set(_) => None,
            _ => as_set(query, matcher),
        };
        std::iter::once(compiled).chain(set).collect()
    }

    // Position bookkeeping is the production scan's own, tested through it at the
    // boundaries: the first byte, the byte after a newline, and the end of input.
    #[test]
    fn positions_number_lines_and_columns_from_the_first_byte() {
        let body = b"ab\ncd\n";
        for (query, matcher, want) in [
            ("c", Matcher::Exact, (2, 1, 3, 1)),
            ("d", Matcher::Exact, (2, 2, 4, 1)),
            (r"\z", Matcher::Regex, (3, 1, 6, 0)),
        ] {
            let report = scan_fixture(body, query, matcher, None, DEFAULT_MAX_BYTES);
            let s = &report.spans[..];
            assert_eq!(s.len(), 1, "{query}: {report:?}");
            assert_eq!(
                (s[0].line, s[0].column, s[0].byte, s[0].len),
                want,
                "{query}"
            );
        }
    }

    // A line far past many read slices keeps its number: the count is carried, not
    // recovered from retained line starts.
    #[test]
    fn a_line_after_a_long_gap_is_numbered_correctly() {
        let mut body = b"xxxxxxxx\n".to_vec();
        body.extend_from_slice(&vec![b'y'; 8192]);
        body.extend_from_slice(b"zzzz\nneedle");
        let report = scan_fixture(&body, "needle", Matcher::Exact, Some(64), DEFAULT_MAX_BYTES);
        let s = &report.spans[..];
        assert_eq!(s.len(), 1, "{report:?}");
        assert_eq!((s[0].line, s[0].column, s[0].byte), (3, 1, 8206));
    }

    // A long line crosses many read slices and its column stays exact.
    #[test]
    fn a_long_line_keeps_an_accurate_column() {
        let mut body = vec![b'x'; 12_288];
        body.extend_from_slice(b"needle");
        let report = scan_fixture(&body, "needle", Matcher::Exact, Some(64), DEFAULT_MAX_BYTES);
        let s = &report.spans[..];
        assert_eq!(s.len(), 1, "{report:?}");
        assert_eq!((s[0].line, s[0].column, s[0].byte), (1, 12_289, 12_288));
    }

    // A column is a byte offset within its line, because the lane matches over bytes.
    // A multi-byte character therefore advances the column by its encoded length, which
    // is the honest answer for a byte lane and the reason the span also carries `byte`.
    #[test]
    fn a_column_counts_bytes_so_a_multibyte_character_advances_it_by_its_length() {
        let report = scan_fixture(
            "é needle".as_bytes(),
            "needle",
            Matcher::Exact,
            None,
            DEFAULT_MAX_BYTES,
        );
        let s = &report.spans[..];
        assert_eq!(s.len(), 1, "{report:?}");
        assert_eq!(
            (s[0].byte, s[0].line, s[0].column),
            (3, 1, 4),
            "e-acute is two bytes"
        );
    }

    // Four times `MAX_CURSOR_LINES` one-byte lines before the match: the line number is
    // exact with no line entry retained (tests/exactscan_memory.rs bounds the heap on a
    // 4 MiB file of newlines).
    #[test]
    fn line_bookkeeping_stays_bounded_on_a_dense_newline_file() {
        let mut body = vec![b'\n'; MAX_CURSOR_LINES * 4];
        body.extend_from_slice(b"needle");
        let report = scan_fixture(&body, "needle", Matcher::Exact, Some(64), DEFAULT_MAX_BYTES);
        let s = &report.spans[..];
        assert_eq!(s.len(), 1, "{report:?}");
        assert_eq!(
            (s[0].line, s[0].column, s[0].byte),
            (MAX_CURSOR_LINES * 4 + 1, 1, MAX_CURSOR_LINES * 4)
        );
    }

    #[test]
    fn matches_in_a_large_slice_resolve_before_line_starts_are_pruned() {
        let mut body = b"needle\n".to_vec();
        for _ in 0..MAX_CURSOR_LINES + 20 {
            body.extend_from_slice(b"x\n");
        }
        let (more, report) = run(
            &body,
            "needle",
            Matcher::Exact,
            None,
            Limits::default(),
            &mut Deadline::never(),
        );
        assert!(more);
        assert_eq!(report.spans.len(), 1);
        assert_eq!(report.spans[0].line, 1);
    }

    #[test]
    fn matches_near_the_start_middle_and_end_of_a_multislice_file_are_preserved() {
        let mut body = vec![b'x'; CHUNK_BYTES * 2 + 100];
        let offsets = [8, CHUNK_BYTES + 9, CHUNK_BYTES * 2 + 84];
        for at in offsets {
            body[at..at + 6].copy_from_slice(b"needle");
        }
        let (more, report) = run(
            &body,
            "needle",
            Matcher::Exact,
            None,
            Limits::default(),
            &mut Deadline::never(),
        );
        assert!(more);
        assert_eq!(
            report.spans.iter().map(|s| s.byte).collect::<Vec<_>>(),
            offsets
        );
    }

    #[test]
    fn exhausting_the_byte_budget_at_eof_is_not_a_truncation() {
        let body = b"needle\n";
        let limits = Limits {
            max_bytes: body.len() as u64,
            ..Limits::default()
        };
        let (more, report) = run(
            body,
            "needle",
            Matcher::Exact,
            None,
            limits,
            &mut Deadline::never(),
        );
        assert!(more);
        assert_eq!(report.bytes_scanned, body.len() as u64);
        assert_eq!(report.cut, None);
        assert_eq!(report.unconfirmed, None);
    }

    #[test]
    fn an_unbounded_regex_match_crossing_a_slice_is_reported_once_at_full_length() {
        let body = format!("a{}z", "x".repeat(127));
        let report = scan_fixture(
            body.as_bytes(),
            "a.*",
            Matcher::Regex,
            Some(64),
            DEFAULT_MAX_BYTES,
        );
        assert_eq!(found(&report), [(0, body.len())], "{report:?}");
    }

    #[test]
    fn an_unbounded_match_past_the_window_is_reported_once_with_a_counted_cap() {
        let body = format!("a{}z", "x".repeat(MAX_MATCH_BYTES * 2));
        let report = scan_fixture(
            body.as_bytes(),
            "a.*",
            Matcher::Regex,
            Some(64),
            DEFAULT_MAX_BYTES,
        );
        assert_eq!(found(&report), [(0, MAX_MATCH_BYTES)], "{report:?}");
        assert_eq!(report.matches_cut, 1);
    }

    // batch34 #1: a greedy extent that ends before a read boundary is not published
    // early; the whole file decides it.
    #[test]
    fn greedy_extent_before_slice_boundary_is_not_published_early() {
        let mut body = vec![b'x'; 65_500];
        body.extend_from_slice(b"az");
        body.extend_from_slice(&[b'x'; 64]);
        body.push(b'z');
        let report = scan_fixture(&body, "a.*z", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(
            report.spans,
            [Span {
                path: "a.txt".into(),
                line: 1,
                column: 65_501,
                byte: 65_500,
                len: 67,
            }]
        );
        assert_eq!(report.cut, None);
        assert_eq!(report.matches_cut, 0);
        assert_eq!(report.unconfirmed, None);
    }

    // batch34 #2: non-overlapping iteration keeps the whole file's alignment across read
    // boundaries, checked offset by offset against whole-file matching.
    #[test]
    fn repeated_literal_occurrences_keep_whole_file_nonoverlap_alignment() {
        let needle = "a".repeat(511);
        let body = vec![b'a'; 70_000];
        let report = scan_fixture(&body, &needle, Matcher::Exact, None, DEFAULT_MAX_BYTES);
        let want: Vec<(usize, usize)> = (0..=135).map(|k| (k * 511, 511)).collect();
        assert_eq!(found(&report), want);
        let whole: Vec<(usize, usize)> = Oracle::new(&needle, Matcher::Exact)
            .find_all(&body)
            .into_iter()
            .map(|(s, e)| (s, e - s))
            .collect();
        assert_eq!(found(&report), whole);
        assert_eq!(report.cut, None);

        let mut sparse = vec![b'x'; 70_000];
        sparse[65_000..65_000 + 511 * 2].fill(b'a');
        let report = scan_fixture(&sparse, &needle, Matcher::Exact, None, DEFAULT_MAX_BYTES);
        assert_eq!(found(&report), [(65_000, 511), (65_511, 511)]);
    }

    // batch34 #3: an occurrence longer than the reported window is one capped span, not a
    // second occurrence restarted where a read boundary fell.
    #[test]
    fn a_capped_unbounded_occurrence_is_not_restarted_in_a_later_slice() {
        let body = vec![b'x'; 100_000];
        let report = scan_fixture(&body, "x+", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(found(&report), [(0, MAX_MATCH_BYTES)], "{report:?}");
        assert_eq!(report.matches_cut, 1);
        assert_eq!(report.cut, None);
        assert_eq!(report.unconfirmed, None);
    }

    // batch34 #4: `^` is the file's start and `\b` reads the file's own neighbours, not a
    // read slice's.
    #[test]
    fn beginning_of_file_and_word_boundary_assertions_keep_file_context() {
        let mut body = vec![b'x'; 65_537];
        body[61_440..61_446].copy_from_slice(b"needle");
        let report = scan_fixture(&body, "^needle", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(found(&report), NOTHING, "{report:?}");
        assert_eq!(report.cut, None);
        assert_eq!(report.unconfirmed, None);

        let mut spaced = vec![b' '; 70_000];
        spaced[65_533..65_539].copy_from_slice(b"needle");
        let report = scan_fixture(
            &spaced,
            r"\bneedle\b",
            Matcher::Regex,
            None,
            DEFAULT_MAX_BYTES,
        );
        assert_eq!(found(&report), [(65_533, 6)], "{report:?}");
        spaced[65_532] = b'x';
        let report = scan_fixture(
            &spaced,
            r"\bneedle\b",
            Matcher::Regex,
            None,
            DEFAULT_MAX_BYTES,
        );
        assert_eq!(found(&report), NOTHING, "{report:?}");
    }

    // batch34 #5: empty matches at word boundaries are listed once each.
    #[test]
    fn zero_length_word_boundary_matches_are_unique() {
        let mut body = vec![b' '; 65_534];
        body.extend_from_slice(b"x  ");
        let report = scan_fixture(&body, r"\b", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(found(&report), [(65_534, 0), (65_535, 0)]);
    }

    // batch34 #6 and batch43 #7: a literal read whole is final, so the byte cap ending
    // after it keeps it at its full length.
    #[test]
    fn a_complete_literal_is_reported_even_when_the_byte_budget_ends_after_it() {
        let report = scan_fixture(b"needlex", "needle", Matcher::Exact, Some(64), 6);
        assert_eq!(found(&report), [(0, 6)], "{report:?}");
        assert_eq!(report.cut, Some(Cut::Bytes));
        assert_eq!(report.matches_cut, 0);
        assert_eq!(report.unconfirmed, unconfirmed(6, 6, Why::ByteCap));
    }

    // batch34 #7: the reads, not the length captured at open, say where the file ends.
    #[test]
    fn actual_eof_finalizes_matches_after_the_open_file_is_truncated() {
        let td = tempfile::tempdir().unwrap();
        let path = td.path().join("a.txt");
        std::fs::write(&path, b"needlezz").unwrap();
        let mut fs = FileScan::open(td.path(), "a.txt").unwrap();
        std::fs::write(&path, b"needle").unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let pattern = compile("needle", Matcher::Regex).unwrap();
        let mut sc = pattern.scanner();
        assert!(fs.scan(
            &mut sc,
            &limits,
            &mut budget,
            &mut report,
            &mut Deadline::never(),
            false
        ));
        assert_eq!(found(&report), [(0, 6)], "{report:?}");
        assert_eq!(report.cut, None);
        assert_eq!(report.unconfirmed, None);
    }

    // batch34 #8: a match whose extent runs past the window is one capped span; and when
    // the byte cap stops the reads before it is decided, the empty answer says so rather
    // than reading as complete.
    #[test]
    fn viable_regex_prefix_survives_production_sized_read_boundaries() {
        let mut body = b"a".to_vec();
        body.extend_from_slice(&vec![b'x'; 70_000]);
        body.push(b'z');
        let report = scan_fixture(&body, "a.*z", Matcher::Regex, None, DEFAULT_MAX_BYTES);
        assert_eq!(found(&report), [(0, MAX_MATCH_BYTES)], "{report:?}");
        assert_eq!(report.matches_cut, 1);
        assert_eq!(report.cut, None);

        let report = scan_fixture(&body, "a.*z", Matcher::Regex, None, 65_536);
        assert_eq!(found(&report), NOTHING, "{report:?}");
        assert_eq!(report.cut, Some(Cut::Bytes));
        assert_eq!(report.unconfirmed, unconfirmed(0, 65_536, Why::ByteCap));
    }

    // batch43 #2: one search step that would run to the end of a 64 KiB buffer is
    // interrupted inside the step. The deadline fires on its third check: after the read,
    // before the first step, then on the `SCAN_STEP`th transition.
    #[test]
    fn a_pathological_search_step_is_interrupted_inside_the_step() {
        interrupted_inside_one_step("a.*z|a");
    }

    // batch51 #3: the same for a preferred alternative that stays alive over every byte
    // before its fallback is reported.
    #[test]
    fn one_expensive_search_step_is_interrupted() {
        interrupted_inside_one_step("(?:a{1000})*b|a");
    }

    fn interrupted_inside_one_step(pattern: &str) {
        let body = vec![b'a'; CHUNK_BYTES];
        assert!(
            compile(pattern, Matcher::Regex).is_ok(),
            "{pattern} must compile under the lane's limits"
        );
        let (more, report) = run(
            &body,
            pattern,
            Matcher::Regex,
            None,
            Limits::default(),
            &mut Deadline::after_checks(2),
        );
        assert!(!more);
        assert_eq!(report.cut, Some(Cut::Time), "{pattern}");
        assert_eq!(found(&report), NOTHING, "{pattern}");
        assert_eq!(report.work, u64::from(SCAN_STEP), "{pattern}");
        assert_eq!(
            report.unconfirmed,
            unconfirmed(0, CHUNK_BYTES, Why::TimeCap),
            "{pattern}"
        );

        // The control: with no deadline the first step alone runs the whole buffer.
        let limits = Limits {
            max_lines: 1,
            ..Limits::default()
        };
        let (_, control) = run(
            &body,
            pattern,
            Matcher::Regex,
            None,
            limits,
            &mut Deadline::never(),
        );
        assert_eq!(found(&control), [(0, 1)], "{pattern}");
        assert_eq!(control.cut, Some(Cut::Lines), "{pattern}");
        assert!(
            control.work > CHUNK_BYTES as u64,
            "{pattern}: {}",
            control.work
        );
    }

    // batch51 #4: the deadline is checked before every search step, deterministically:
    // after the read, then before steps one to three; the fifth check, before step four,
    // finds it passed.
    #[test]
    fn the_deadline_is_checked_between_occurrences() {
        let body = b"needle\n".repeat(10);
        let (more, report) = run(
            &body,
            "needle",
            Matcher::Exact,
            None,
            Limits::default(),
            &mut Deadline::after_checks(4),
        );
        assert!(!more);
        assert_eq!(found(&report), [(0, 6), (7, 6), (14, 6)]);
        assert_eq!(report.cut, Some(Cut::Time));
        assert_eq!(report.unconfirmed, unconfirmed(20, 70, Why::TimeCap));
    }

    // batch43 #3: an artificial cutoff is not the file's end, so end assertions are not
    // satisfied by it. `needle\B` does match the whole file at 0, which is why the
    // unconfirmed offset, not the empty list, is what keeps the answer honest.
    #[test]
    fn artificial_cutoff_does_not_satisfy_end_assertions() {
        for pattern in ["needle$", r"needle\z", r"needle\b", r"needle\B"] {
            let report = scan_fixture(b"needlex", pattern, Matcher::Regex, Some(64), 6);
            assert_eq!(found(&report), NOTHING, "{pattern}: {report:?}");
            assert_eq!(report.cut, Some(Cut::Bytes), "{pattern}");
            assert_eq!(
                report.unconfirmed,
                unconfirmed(0, 6, Why::ByteCap),
                "{pattern}"
            );
        }
        let mut whole = Oracle::new(r"needle\B", Matcher::Regex);
        assert_eq!(whole.find_all(b"needlex"), [(0, 6)]);

        let report = scan_fixture(b"needle\nx", "needle$", Matcher::Regex, Some(64), 7);
        assert_eq!(found(&report), NOTHING, "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(7, 7, Why::ByteCap));

        let report = scan_fixture(b"$x", "[$]", Matcher::Regex, Some(64), 1);
        assert_eq!(found(&report), [(0, 1)], "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(1, 1, Why::ByteCap));
    }

    // batch43 #4: a greedy extent the cutoff could still lengthen is withheld, not
    // published short and not counted as capped.
    #[test]
    fn greedy_regex_extent_is_degraded_at_byte_cutoff() {
        for (body, pattern, cap) in [(&b"azxxz"[..], "a.*z", 3u64), (&b"aax"[..], "a{1,2}", 1)] {
            let report = scan_fixture(body, pattern, Matcher::Regex, Some(64), cap);
            assert_eq!(found(&report), NOTHING, "{pattern}: {report:?}");
            assert_eq!(report.matches_cut, 0, "{pattern}");
            assert_eq!(report.cut, Some(Cut::Bytes), "{pattern}");
            assert_eq!(
                report.unconfirmed,
                unconfirmed(0, cap as usize, Why::ByteCap),
                "{pattern}"
            );
        }
    }

    // batch43 #5: a bounded match wholly inside the bytes read is final.
    #[test]
    fn bounded_regex_match_is_kept_at_cutoff() {
        let report = scan_fixture(b"ax", "[ab]", Matcher::Regex, Some(64), 1);
        assert_eq!(found(&report), [(0, 1)], "{report:?}");
        assert_eq!(report.cut, Some(Cut::Bytes));
        assert_eq!(report.unconfirmed, unconfirmed(1, 1, Why::ByteCap));
        let report = scan_fixture(b"aax", "a{2}", Matcher::Regex, Some(64), 2);
        assert_eq!(found(&report), [(0, 2)], "{report:?}");
        assert_eq!(report.cut, Some(Cut::Bytes));
        assert_eq!(report.unconfirmed, unconfirmed(2, 2, Why::ByteCap));
    }

    // batch43 #7: a complete literal at the cutoff keeps its length and is not counted as
    // capped.
    #[test]
    fn complete_literal_at_cutoff_is_not_length_capped() {
        let report = scan_fixture(b"needlex", "needle", Matcher::Exact, Some(64), 6);
        assert_eq!(found(&report), [(0, 6)], "{report:?}");
        assert_eq!(report.matches_cut, 0);
    }

    // batch43 #6: the length at open is a reservation, not the end: a file that grew
    // after it was opened is read to its real end.
    #[test]
    fn eof_is_what_the_reads_reached() {
        for (prefix, at) in [(b"prefix".to_vec(), 6), (vec![b'x'; 65_536], 65_536)] {
            let td = tempfile::tempdir().unwrap();
            let path = td.path().join("a.txt");
            std::fs::write(&path, &prefix).unwrap();
            let mut fs = FileScan::open(td.path(), "a.txt").unwrap();
            use std::io::Write;
            std::fs::OpenOptions::new()
                .append(true)
                .open(&path)
                .unwrap()
                .write_all(b"needle")
                .unwrap();
            let limits = Limits::default();
            let mut budget = limits.max_bytes;
            let mut report = Report::default();
            let pattern = compile("needle", Matcher::Exact).unwrap();
            let mut sc = pattern.scanner();
            assert!(fs.scan(
                &mut sc,
                &limits,
                &mut budget,
                &mut report,
                &mut Deadline::never(),
                false
            ));
            assert_eq!(found(&report), [(at, 6)], "{report:?}");
            assert_eq!(report.bytes_scanned, at as u64 + 6);
            assert_eq!(report.cut, None);
        }
    }

    // batch51 #1: an alternative or an optional continuation the unread bytes could still
    // take is unresolved, so nothing at or after its start is published.
    #[test]
    fn optional_continuations_and_alternatives_are_withheld_at_cutoff() {
        for (body, pattern, cap) in [(&b"abc"[..], "abc|b", 2u64), (&b"ab"[..], "ab?", 1)] {
            let report = scan_fixture(body, pattern, Matcher::Regex, Some(64), cap);
            assert_eq!(found(&report), NOTHING, "{pattern}: {report:?}");
            assert_eq!(report.cut, Some(Cut::Bytes), "{pattern}");
            assert_eq!(
                report.unconfirmed,
                unconfirmed(0, cap as usize, Why::ByteCap),
                "{pattern}"
            );
        }
    }

    // batch51 #2: finality is decided per occurrence. `foo.*` cannot cross the newline
    // read at byte 3, and `\bneedle` reads nothing past its own six bytes.
    #[test]
    fn final_occurrences_before_the_cutoff_are_kept() {
        let report = scan_fixture(b"foo\nremaining", "foo.*", Matcher::Regex, Some(64), 4);
        assert_eq!(
            report.spans,
            [Span {
                path: "a.txt".into(),
                line: 1,
                column: 1,
                byte: 0,
                len: 3,
            }]
        );
        assert_eq!(report.unconfirmed, unconfirmed(4, 4, Why::ByteCap));
        let report = scan_fixture(b"needlex", r"\bneedle", Matcher::Regex, Some(64), 6);
        assert_eq!(found(&report), [(0, 6)], "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(6, 6, Why::ByteCap));
    }

    // An empty pattern's matches are final as soon as their position is read; `x*` with
    // unread `x` bytes is not, and its empty-match skip is never applied to an open
    // candidate.
    #[test]
    fn empty_and_star_patterns_at_the_cutoff() {
        let report = scan_fixture(b"abc", "", Matcher::Regex, Some(64), 2);
        assert_eq!(found(&report), [(0, 0), (1, 0), (2, 0)], "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(3, 2, Why::ByteCap));
        let report = scan_fixture(b"xxx", "x*", Matcher::Regex, Some(64), 2);
        assert_eq!(found(&report), NOTHING, "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(0, 2, Why::ByteCap));
    }

    // A pattern that can consume a newline is final once its automaton died on a byte
    // read: `\s+` died on `c` at byte 4, before the cutoff at 5.
    #[test]
    fn a_decided_match_of_a_newline_crossing_pattern_is_kept() {
        let report = scan_fixture(b"a  bcd", r"\s+", Matcher::Regex, Some(64), 5);
        assert_eq!(found(&report), [(1, 2)], "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(3, 5, Why::ByteCap));
    }

    // `(?m)$` reads the newline after `foo`, which was read, so the match is final.
    #[test]
    fn a_multiline_end_anchor_is_kept_when_its_newline_was_read() {
        let report = scan_fixture(b"foo\nbar", "(?m)foo$", Matcher::Regex, Some(64), 4);
        assert_eq!(found(&report), [(0, 3)], "{report:?}");
        assert_eq!(report.unconfirmed, unconfirmed(4, 4, Why::ByteCap));
    }

    // An anchored pattern's automaton dies on the first byte after its match, which
    // proves no occurrence can start in the unread rest: the file is complete.
    #[test]
    fn an_anchored_pattern_proves_the_unread_rest_cannot_match() {
        let mut body = b"needle".to_vec();
        body.extend_from_slice(&vec![b'x'; 70_000]);
        let limits = Limits {
            max_bytes: 65_536,
            ..Limits::default()
        };
        let (more, report) = run(
            &body,
            r"\Aneedle",
            Matcher::Regex,
            None,
            limits,
            &mut Deadline::never(),
        );
        assert!(more);
        assert_eq!(found(&report), [(0, 6)], "{report:?}");
        assert_eq!(report.cut, None);
        assert_eq!(report.unconfirmed, None);
    }

    // The time cap is the scan's own wall clock: a scan that would exceed it stops and
    // names the cut. The deadline is handed a start two seconds in the past and a 1 ms
    // cap, so the first check after the first one-byte read finds it passed.
    #[test]
    fn the_time_cap_stops_a_scan_in_flight_and_names_its_cut() {
        let body = vec![b'x'; CHUNK_BYTES * 2 + 1];
        let limits = Limits {
            max_lines: 200,
            max_bytes: u64::MAX,
            timeout_ms: 1,
        };
        let started = std::time::Instant::now() - std::time::Duration::from_secs(2);
        let (more, report) = run(
            &body,
            "needle",
            Matcher::Exact,
            Some(1),
            limits,
            &mut Deadline::wall(started, limits.timeout_ms),
        );
        assert!(!more, "the time cap stops the scan before its corpus ends");
        assert_eq!(report.cut, Some(Cut::Time), "{report:?}");
        assert_eq!(
            report.bytes_scanned, 1,
            "one slice, then the cap: {report:?}"
        );
        assert_eq!(report.unconfirmed, unconfirmed(0, 1, Why::TimeCap));
    }

    // The literal engines check the deadline between `CHUNK_BYTES` windows: three reads
    // and the check before the first step pass, and the check after the first window, the
    // fifth, finds it passed.
    #[test]
    fn the_literal_engines_check_the_deadline_between_windows() {
        let body = vec![b'x'; CHUNK_BYTES * 3];
        for (query, matcher) in [
            ("needle", Matcher::Exact),
            ("needle|foo|abc", Matcher::Regex),
        ] {
            for pattern in engines(query, matcher) {
                let longest = match &pattern.engine {
                    Engine::Literal(finder) => finder.needle().len(),
                    Engine::Set(set) => set.max_pattern_len(),
                    Engine::Dfa(_) => continue,
                };
                let td = tempfile::tempdir().unwrap();
                std::fs::write(td.path().join("a.txt"), &body).unwrap();
                let mut fs = FileScan::open(td.path(), "a.txt").unwrap();
                let limits = Limits::default();
                let mut budget = limits.max_bytes;
                let mut report = Report::default();
                let mut deadline = Deadline::after_checks(4);
                let more = fs.scan(
                    &mut pattern.scanner(),
                    &limits,
                    &mut budget,
                    &mut report,
                    &mut deadline,
                    false,
                );
                assert!(!more, "{query}");
                assert_eq!(report.cut, Some(Cut::Time), "{query}");
                assert_eq!(report.work, (CHUNK_BYTES + longest - 1) as u64, "{query}");
                assert_eq!(
                    report.unconfirmed,
                    unconfirmed(0, body.len(), Why::TimeCap),
                    "{query}"
                );
            }
        }
    }

    // The regex crate answers an exact literal set with a literal search and builds no
    // NFA for it, so a set past the lazy DFA's NFA limit (about 80 words of 10 letters) is
    // answered on the set engine, not refused: every pattern here the crate builds, the
    // scan answers.
    #[test]
    fn a_literal_set_past_the_nfa_limit_is_answered() {
        let mut rng = Rng(0x5851_f42d_4c95_7f2d);
        let shapes = [
            (10, 10),
            (60, 10),
            (80, 10),
            (100, 10),
            (30, 20),
            (60, 6),
            (120, 6),
            (200, 19),
        ];
        for (count, len) in shapes {
            let words: Vec<String> = (0..count).map(|_| rng.word(len)).collect();
            let set = words.join("|");
            let planted = &words[count / 2];
            for (query, body, at) in [
                (set.clone(), format!("xx {planted} yy"), (3, len)),
                (
                    format!("(?:{set})!"),
                    format!("xx {planted}! yy"),
                    (3, len + 1),
                ),
                (
                    format!("(?:{set})[!?]"),
                    format!("xx {planted}? yy"),
                    (3, len + 1),
                ),
            ] {
                let builds = crate_regex(&query).is_ok();
                let ctx = format!("{count} words of {len}, {} pattern bytes", query.len());
                if query == set && count >= 80 {
                    assert!(builds, "{ctx}: the regex crate builds this set");
                    let engine = compile(&query, Matcher::Regex).map(|p| p.engine);
                    assert!(matches!(engine, Ok(Engine::Set(_))), "{ctx}");
                }
                match compile(&query, Matcher::Regex) {
                    Err(e) => assert!(!builds, "{ctx}: the regex crate builds it: {e}"),
                    Ok(_) => {
                        let report = scan_fixture(
                            body.as_bytes(),
                            &query,
                            Matcher::Regex,
                            None,
                            DEFAULT_MAX_BYTES,
                        );
                        assert_eq!(found(&report), [at], "{ctx}");
                        assert_eq!(report.unconfirmed, None, "{ctx}");
                    }
                }
            }
        }
    }

    // The set engine against the PikeVM where only it can answer: 200 words, every tenth
    // also as its first three letters ahead of it (a shorter literal preferred) and, in the
    // second set, as itself, a newline and `zz` (a literal that crosses lines). Files of
    // those pieces, at the file's end and at every cutoff.
    #[test]
    fn the_set_engine_is_whole_file_matching_past_the_nfa_limit() {
        let mut rng = Rng(0x3c6e_f372_fe94_f82b);
        let words: Vec<String> = (0..200).map(|_| rng.word(6)).collect();
        for crossing in [false, true] {
            let mut alternatives = Vec::new();
            for (i, word) in words.iter().enumerate() {
                if i % 10 == 0 {
                    alternatives.push(word[..3].to_string());
                }
                alternatives.push(word.clone());
                if crossing && i % 10 == 5 {
                    alternatives.push(format!(r"{word}\nzz"));
                }
            }
            let query = alternatives.join("|");
            let pattern = compile(&query, Matcher::Regex).unwrap();
            assert!(
                matches!(pattern.engine, Engine::Set(_)),
                "crossing {crossing}"
            );
            assert_eq!(pattern.shape.line_local, !crossing);
            let mut whole = Oracle::new(&query, Matcher::Regex);
            let mut sc = pattern.scanner();
            let mut pieces: Vec<String> = words.iter().step_by(5).cloned().collect();
            pieces.extend(words.iter().take(20).map(|w| w[..4].to_string()));
            pieces.extend([" ", "\n", "zz", "x"].map(String::from));
            for _ in 0..32 {
                let count = 1 + rng.below(10);
                let file: Vec<u8> = (0..count)
                    .flat_map(|_| pieces[rng.below(pieces.len())].bytes())
                    .collect();
                let label = format!("{} literals, crossing {crossing}", alternatives.len());
                assert_eq!(
                    run_buffer(&mut sc, &file, Ending::Eof).0,
                    whole.find_all(&file),
                    "{label} over {}",
                    show(&file)
                );
                assert_final_at_every_cutoff(&mut sc, &mut whole, &label, &file, &mut rng);
            }
        }
    }

    // Whole-file matching is leftmost-first. The regex crate's own `find_iter` misses it
    // for these (regex-automata 0.4.14's reverse suffix and inner strategies take the
    // first literal they find): `ab.` matches `abc` at byte 2 and `c` follows, so the
    // leftmost match is (2, 6), not (4, 5).
    #[test]
    fn whole_file_matching_is_leftmost_first_where_the_crates_literal_strategies_are_not() {
        let mut rng = Rng(0x7f4a_7c15_9e37_79b9);
        for (query, body, want) in [
            ("(?:|ab.)c", &b"akabccabbab\rneedle"[..], &[(2, 6)][..]),
            (
                r"(?:\b|foo.)[\r\n]",
                b"foo\r\naabk\r_x\r",
                &[(0, 5), (9, 10), (12, 13)],
            ),
            (
                r"(?:x|\b|\b{start}foo(?u:.))[\r\n]",
                b"foo\r\n$\xc3\xa90za",
                &[(0, 5)],
            ),
        ] {
            let mut whole = Oracle::new(query, Matcher::Regex);
            assert_eq!(whole.find_all(body), want, "{query}: the oracle");
            let report = scan_fixture(body, query, Matcher::Regex, None, DEFAULT_MAX_BYTES);
            let spans: Vec<(usize, usize)> = want.iter().map(|&(s, e)| (s, e - s)).collect();
            assert_eq!(found(&report), spans, "{query}");
            assert_eq!(report.unconfirmed, None, "{query}");
            let pattern = compile(query, Matcher::Regex).unwrap();
            assert_final_at_every_cutoff(&mut pattern.scanner(), &mut whole, query, body, &mut rng);
        }
    }

    /// Every shape the cutoff rules tell apart.
    const PROPERTY_PATTERNS: &[(&str, Matcher)] = &[
        ("needle", Matcher::Regex),
        ("abc|b", Matcher::Regex),
        ("ab?", Matcher::Regex),
        ("a.*z", Matcher::Regex),
        ("foo.*", Matcher::Regex),
        (r"\bneedle", Matcher::Regex),
        ("needle$", Matcher::Regex),
        (r"needle\z", Matcher::Regex),
        (r"needle\b", Matcher::Regex),
        (r"needle\B", Matcher::Regex),
        ("[ab]", Matcher::Regex),
        ("a{2}", Matcher::Regex),
        ("a{1,2}", Matcher::Regex),
        ("x+", Matcher::Regex),
        ("x*", Matcher::Regex),
        (r"\b", Matcher::Regex),
        (r"\B", Matcher::Regex),
        ("(?m)^", Matcher::Regex),
        ("(?m)$", Matcher::Regex),
        ("^", Matcher::Regex),
        ("$", Matcher::Regex),
        (r"\s+", Matcher::Regex),
        ("(?s)a.*z", Matcher::Regex),
        ("a.*z|a", Matcher::Regex),
        ("(?:ab)+|a", Matcher::Regex),
        ("", Matcher::Regex),
        ("[^z]+", Matcher::Regex),
        ("(a|ab)(c|bcd)", Matcher::Regex),
        (r"a\nb", Matcher::Regex),
        ("(?m)^a.*$", Matcher::Regex),
        ("(?:|ab.)c", Matcher::Regex),
        (r"(?:\b|foo.)[\r\n]", Matcher::Regex),
        (r"(?:x|\b|\b{start}foo(?u:.))[\r\n]", Matcher::Regex),
        ("ab|abcd|b\nx|x", Matcher::Regex),
        ("ab", Matcher::Exact),
        ("a\nb", Matcher::Exact),
    ];

    const TOKENS: [&[u8]; 13] = [
        b"a",
        b"b",
        b"x",
        b"z",
        b" ",
        b"\n",
        b"\r",
        b"_",
        b"$",
        b"\xc3\xa9",
        b"needle",
        b"foo",
        b"abc",
    ];

    /// A seeded xorshift64 stream: the same corpus on every run, without a crate.
    struct Rng(u64);

    impl Rng {
        fn below(&mut self, n: usize) -> usize {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            (self.0 % n as u64) as usize
        }

        fn token(&mut self) -> &'static [u8] {
            TOKENS[self.below(TOKENS.len())]
        }

        /// A lowercase word of `len` letters.
        fn word(&mut self, len: usize) -> String {
            (0..len)
                .map(|_| char::from(b'a' + self.below(26) as u8))
                .collect()
        }

        /// `count` tokens.
        fn tokens(&mut self, count: usize) -> Vec<u8> {
            (0..count)
                .flat_map(|_| self.token().iter().copied())
                .collect()
        }

        /// Tokens to exactly `len` bytes.
        fn bytes(&mut self, len: usize) -> Vec<u8> {
            let mut out = Vec::with_capacity(len + 6);
            while out.len() < len {
                out.extend_from_slice(self.token());
            }
            out.truncate(len);
            out
        }

        /// A file of at most `max` bytes.
        fn file(&mut self, max: usize) -> Vec<u8> {
            let len = self.below(max + 1);
            self.bytes(len)
        }
    }

    /// Bytes as an escaped string, at most 200 of them, for an assertion message.
    fn show(bytes: &[u8]) -> String {
        format!(
            "{:?}",
            String::from_utf8_lossy(&bytes[..bytes.len().min(200)])
        )
    }

    /// The span whole-file matching gives for (s, e) in `body`, positions counted from
    /// the bytes themselves.
    fn span_at(body: &[u8], s: usize, e: usize) -> Span {
        let before = &body[..s];
        let line_start = before
            .iter()
            .rposition(|&b| b == b'\n')
            .map_or(0, |i| i + 1);
        Span {
            path: "a.txt".into(),
            line: 1 + before.iter().filter(|&&b| b == b'\n').count(),
            column: s - line_start + 1,
            byte: s,
            len: (e - s).min(MAX_MATCH_BYTES),
        }
    }

    /// Scan `a.txt` holding `body` under the default caps and assert it is whole-file
    /// matching: the first `DEFAULT_MAX_LINES` occurrences, positioned and capped.
    fn assert_scan_is_whole_file(
        dir: &std::path::Path,
        pattern: &Pattern,
        whole: &mut Oracle,
        query: &str,
        body: &[u8],
        slice: Option<usize>,
    ) {
        std::fs::write(dir.join("a.txt"), body).unwrap();
        let mut fs = FileScan::open_with_slice(dir, "a.txt", slice).unwrap();
        let limits = Limits::default();
        let mut budget = limits.max_bytes;
        let mut report = Report::default();
        let mut sc = pattern.scanner();
        fs.scan(
            &mut sc,
            &limits,
            &mut budget,
            &mut report,
            &mut Deadline::never(),
            false,
        );
        let all = whole.find_all(body);
        let listed = &all[..all.len().min(DEFAULT_MAX_LINES)];
        let want: Vec<Span> = listed.iter().map(|&(s, e)| span_at(body, s, e)).collect();
        let ctx = || {
            format!(
                "{query:?} on {} over {} ({} bytes, slice {slice:?})",
                pattern.engine.name(),
                show(body),
                body.len()
            )
        };
        assert_eq!(report.spans, want, "{}", ctx());
        assert_eq!(
            report.cut,
            (all.len() > DEFAULT_MAX_LINES).then_some(Cut::Lines),
            "{}",
            ctx()
        );
        assert_eq!(report.unconfirmed, None, "{}", ctx());
        let capped = listed
            .iter()
            .filter(|(s, e)| e - s > MAX_MATCH_BYTES)
            .count();
        assert_eq!(report.matches_cut, capped as u64, "{}", ctx());
    }

    /// The finality rule at every cutoff of `file`: what is listed is a prefix of
    /// whole-file matching for any rest of the file, nothing that starts before the
    /// unconfirmed offset is missing, and a complete answer is all of it.
    fn assert_final_at_every_cutoff(
        sc: &mut Scanner<'_>,
        whole: &mut Oracle,
        query: &str,
        file: &[u8],
        rng: &mut Rng,
    ) {
        for n in 1..file.len() {
            let read = &file[..n];
            let (listed, outcome) = run_buffer(sc, read, Ending::Cutoff);
            let from = match &outcome {
                Outcome::Complete => None,
                Outcome::Unconfirmed { from } => Some(*from),
                other => panic!("{query:?} over {}: {other:?}", show(read)),
            };
            assert!(
                from.is_none_or(|f| f <= n + 1),
                "{query:?} over {}: {outcome:?}",
                show(read)
            );
            let tail = rng.tokens(8);
            let rests: [&[u8]; 5] = [&file[n..], b"", b"\n", b"x", &tail];
            for rest in rests {
                let all = whole.find_all(&[read, rest].concat());
                let ctx = || {
                    format!(
                        "{query:?}: read {}, rest {}: listed {listed:?}, whole file {all:?}, {outcome:?}",
                        show(read),
                        show(rest)
                    )
                };
                assert!(all.starts_with(&listed), "{}", ctx());
                match from {
                    None => assert_eq!(listed, all, "{}", ctx()),
                    Some(from) => assert!(
                        all.iter().filter(|(s, _)| *s < from).count() <= listed.len(),
                        "{}",
                        ctx()
                    ),
                }
            }
        }
    }

    // A differential check of every engine against the PikeVM: at the file's end the scan
    // is whole-file matching, over small files of every shape and over files three read
    // slices long.
    #[test]
    fn eof_scans_equal_whole_file_matching() {
        let td = tempfile::tempdir().unwrap();
        let mut rng = Rng(0x9e37_79b9_7f4a_7c15);
        let small: Vec<Vec<u8>> = (0..64).map(|_| rng.file(64)).collect();
        for &(query, matcher) in PROPERTY_PATTERNS {
            let mut whole = Oracle::new(query, matcher);
            for pattern in engines(query, matcher) {
                for body in &small {
                    assert_scan_is_whole_file(td.path(), &pattern, &mut whole, query, body, None);
                }
            }
        }
        let large: Vec<Vec<u8>> = (0..2).map(|_| rng.bytes(CHUNK_BYTES * 2 + 1000)).collect();
        for query in [
            "needle",
            "x+",
            r"\b",
            "a.*z",
            r"\s+",
            "[^z]+",
            "abc|b|needle",
        ] {
            let mut whole = Oracle::new(query, Matcher::Regex);
            for pattern in engines(query, Matcher::Regex) {
                let mut sc = pattern.scanner();
                for body in &large {
                    for slice in [None, Some(64)] {
                        assert_scan_is_whole_file(
                            td.path(),
                            &pattern,
                            &mut whole,
                            query,
                            body,
                            slice,
                        );
                    }
                    // every occurrence, past the line cap
                    let (listed, outcome) = run_buffer(&mut sc, body, Ending::Eof);
                    assert_eq!(listed, whole.find_all(body), "{query:?}");
                    assert_eq!(outcome, Outcome::Complete, "{query:?}");
                }
            }
        }
    }

    // The finality rule, for every cutoff of every small file, every shape and every
    // engine the shape can run on.
    #[test]
    fn cutoff_publishes_only_spans_final_for_every_remainder() {
        let mut rng = Rng(0x2545_f491_4f6c_dd1d);
        for &(query, matcher) in PROPERTY_PATTERNS {
            let mut whole = Oracle::new(query, matcher);
            for pattern in engines(query, matcher) {
                let label = format!("{query} on {}", pattern.engine.name());
                let mut sc = pattern.scanner();
                for _ in 0..64 {
                    let file = rng.file(48);
                    assert_final_at_every_cutoff(&mut sc, &mut whole, &label, &file, &mut rng);
                }
            }
        }
    }

    // memmem, Aho-Corasick and the lazy DFA answer the same literal identically, at the
    // file's end and at every cutoff.
    #[test]
    fn literal_and_dfa_engines_agree() {
        let literal = compile("needle", Matcher::Exact).unwrap();
        let set = as_set("needle", Matcher::Exact).unwrap();
        let dfa = compile("(needle)", Matcher::Regex).unwrap();
        assert!(matches!(literal.engine, Engine::Literal(_)));
        assert!(matches!(dfa.engine, Engine::Dfa(_)));
        let mut scanners = [literal.scanner(), set.scanner(), dfa.scanner()];
        let mut rng = Rng(0x0123_4567_89ab_cdef);
        let mut files: Vec<(Vec<u8>, usize)> = (0..64).map(|_| (rng.file(48), 1)).collect();
        files.push((rng.bytes(CHUNK_BYTES * 2 + 1000), 997));
        for (file, stride) in &files {
            let ends = std::iter::once((file.len(), Ending::Eof)).chain(
                (1..file.len())
                    .step_by(*stride)
                    .map(|n| (n, Ending::Cutoff)),
            );
            for (n, ending) in ends {
                let read = &file[..n];
                let [by_literal, by_set, by_dfa] =
                    scanners.each_mut().map(|sc| run_buffer(sc, read, ending));
                assert_eq!(by_literal, by_dfa, "{} {ending:?}", show(read));
                assert_eq!(by_set, by_dfa, "{} {ending:?}", show(read));
            }
        }
    }

    // Where both can answer, the set engine gives the lazy DFA's answer at the file's end,
    // and at a cutoff a prefix of it: the set decides finality by the shape alone, the lazy
    // DFA also by its automaton dying on a byte read.
    #[test]
    fn the_set_engine_lists_a_prefix_of_the_lazy_dfas_answer() {
        let mut rng = Rng(0x6a09_e667_f3bc_c908);
        for query in ["abc|b", "ab|abcd|b\nx|x", "(a|ab)(c|bcd)", "needle|foo|abc"] {
            let dfa = compile(query, Matcher::Regex).unwrap();
            assert!(matches!(dfa.engine, Engine::Dfa(_)), "{query:?}");
            let set = as_set(query, Matcher::Regex).unwrap();
            let (mut by_dfa, mut by_set) = (dfa.scanner(), set.scanner());
            for _ in 0..64 {
                let file = rng.file(48);
                assert_eq!(
                    run_buffer(&mut by_set, &file, Ending::Eof),
                    run_buffer(&mut by_dfa, &file, Ending::Eof),
                    "{query:?} over {}",
                    show(&file)
                );
                for n in 1..file.len() {
                    let read = &file[..n];
                    let (set_listed, _) = run_buffer(&mut by_set, read, Ending::Cutoff);
                    let (dfa_listed, _) = run_buffer(&mut by_dfa, read, Ending::Cutoff);
                    assert!(
                        dfa_listed.starts_with(&set_listed),
                        "{query:?} over {}: set {set_listed:?}, lazy DFA {dfa_listed:?}",
                        show(read)
                    );
                }
            }
        }
    }
}
