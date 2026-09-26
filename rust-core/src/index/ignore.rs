//! One ignore source for the index (PAR-FRESH-08): built-in vendored/generated
//! directory defaults, then `.gitignore` files (applied by Git itself in Git mode, and by
//! this matcher when indexing a non-Git directory), then `.xmustardignore` files. Later
//! rules win, and a `!pattern` in `.xmustardignore` can re-include a default.
//!
//! Supported syntax (the gitignore subset used in practice): blank lines and `#`
//! comments, `!` negation, a trailing `/` for directories only, a leading or inner `/`
//! anchoring the pattern to the ignore file's directory, `*`, `?`, `**` and `[...]`
//! character classes.
//!
//! Ignore files are repository content, so both their size and the matching work are
//! bounded by totals, not per rule: an ignore file above `MAX_IGNORE_FILE_BYTES` is
//! skipped whole, a line above `MAX_PATTERN_BYTES` is skipped, and at most `MAX_RULES`
//! rules (`MAX_RULES_PER_FILE` from one file) holding `MAX_PATTERN_TOTAL_BYTES` of
//! pattern are kept across all ignore files, defaults included. Everything skipped is
//! counted in `dropped_rules`, which the index reports. Each pattern compiles to a
//! bit-parallel automaton (one machine word per 64 pattern elements, with a 256-byte
//! class map, at most about 3 MiB for the bounded rule set), so a path check costs
//! O(rules + pattern bytes / 64) word operations per path byte and never backtracks.
//! Directory decisions are memoized across the sorted listing, and a scan's matching
//! work is budgeted (`DirMemo`, `MATCH_BUDGET`): paths the rules cannot decide within it
//! are reported, never waited for.

use std::collections::HashMap;
use std::path::Path;

/// Directory names skipped by default, anywhere in a path. The single list shared by the
/// legacy scanner, the repo map and the index, so all three agree on what is vendored or
/// generated; override for the index with `!name/` in `.xmustardignore`.
pub const DEFAULT_EXCLUDED_DIRS: &[&str] = &[
    ".git",
    ".hg",
    ".svn",
    ".venv",
    "venv",
    "__pycache__",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".turbo",
    ".next",
    "target",
    "node_modules",
    "dist",
    "build",
    "coverage",
    ".coverage",
    "tmp",
    "vendor",
    "third_party",
    "research",
];

/// Root-relative directories skipped by default (runtime data and build output of this
/// repository's own layout).
pub const DEFAULT_EXCLUDED_RELATIVE_DIRS: &[&str] = &["backend/data", "frontend/dist"];

/// Whether the built-in defaults exclude `relative_path` (a directory name in
/// `DEFAULT_EXCLUDED_DIRS` anywhere on the path, or a path under one of
/// `DEFAULT_EXCLUDED_RELATIVE_DIRS`). Used by the legacy scanner and repo map; the index
/// applies the same list through `Ignore::with_defaults`.
pub fn is_default_excluded(relative_path: &str) -> bool {
    let parts: Vec<&str> = relative_path
        .split('/')
        .filter(|part| !part.is_empty() && *part != ".")
        .collect();
    if parts.is_empty() {
        return false;
    }
    if parts
        .iter()
        .any(|part| DEFAULT_EXCLUDED_DIRS.contains(part))
    {
        return true;
    }
    let normalized = parts.join("/");
    DEFAULT_EXCLUDED_RELATIVE_DIRS.iter().any(|excluded| {
        normalized == *excluded
            || normalized
                .strip_prefix(excluded)
                .is_some_and(|rest| rest.starts_with('/'))
    })
}

pub const XMUSTARD_IGNORE: &str = ".xmustardignore";

/// Longest pattern line kept; longer lines are skipped and counted as dropped.
pub const MAX_PATTERN_BYTES: usize = 1024;
/// Rules kept from one ignore file.
pub const MAX_RULES_PER_FILE: usize = 500;
/// Rules kept across all ignore files, defaults included.
pub const MAX_RULES: usize = 1000;
/// Pattern bytes kept across all ignore files, defaults included.
pub const MAX_PATTERN_TOTAL_BYTES: usize = 64 << 10;
/// Ignore files larger than this are skipped whole and counted as dropped.
pub const MAX_IGNORE_FILE_BYTES: u64 = 64 << 10;

/// One pattern element before compilation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Tok {
    Lit(u8),
    /// `?`: one byte other than `/`.
    Any,
    /// `[...]`: one byte of the numbered class (never `/`).
    Class(u32),
    /// `*`: any run of bytes without `/`.
    Star,
    /// `**/` as a whole component: zero or more whole directories.
    DirStar,
    /// A trailing `**` component: everything that remains.
    AllStar,
}

/// A set of bytes, one bit each.
type ByteSet = [u64; 4];

fn set_has(s: &ByteSet, c: u8) -> bool {
    (s[(c >> 6) as usize] >> (c & 63)) & 1 == 1
}

fn set_bit(m: &mut [u64], i: usize) {
    m[i / 64] |= 1 << (i % 64);
}

/// Pattern elements and the byte sets of its classes. Runs of `*` merge, and `**/`
/// repeated or before a trailing `**` adds nothing, so skip chains stay short.
fn parse(p: &[u8]) -> (Vec<Tok>, Vec<ByteSet>) {
    let mut toks: Vec<Tok> = Vec::new();
    let mut classes: Vec<ByteSet> = Vec::new();
    let mut i = 0;
    while i < p.len() {
        match p[i] {
            b'*' => {
                let mut j = i;
                while j < p.len() && p[j] == b'*' {
                    j += 1;
                }
                let component_start = i == 0 || p[i - 1] == b'/';
                if j - i >= 2 && component_start && j == p.len() {
                    if toks.last() == Some(&Tok::DirStar) {
                        toks.pop();
                    }
                    toks.push(Tok::AllStar);
                } else if j - i >= 2 && component_start && p[j] == b'/' {
                    if toks.last() != Some(&Tok::DirStar) {
                        toks.push(Tok::DirStar);
                    }
                    j += 1;
                } else if toks.last() != Some(&Tok::Star) {
                    toks.push(Tok::Star);
                }
                i = j;
            }
            b'?' => {
                toks.push(Tok::Any);
                i += 1;
            }
            b'[' => {
                let Some(close) = p[i + 1..]
                    .iter()
                    .position(|c| *c == b']')
                    .map(|k| i + 1 + k)
                else {
                    toks.push(Tok::Lit(b'['));
                    i += 1;
                    continue;
                };
                let body = &p[i + 1..close];
                let (neg, body) = match body.first() {
                    Some(b'!') | Some(b'^') => (true, &body[1..]),
                    _ => (false, body),
                };
                let mut set: ByteSet = [0; 4];
                let mut k = 0;
                while k < body.len() {
                    let (a, z) = if k + 2 < body.len() && body[k + 1] == b'-' {
                        k += 3;
                        (body[k - 3], body[k - 1])
                    } else {
                        k += 1;
                        (body[k - 1], body[k - 1])
                    };
                    for c in a..=z {
                        set_bit(&mut set, c as usize);
                    }
                }
                if neg {
                    set.iter_mut().for_each(|w| *w = !*w);
                }
                set[(b'/' >> 6) as usize] &= !(1u64 << (b'/' & 63));
                toks.push(Tok::Class(classes.len() as u32));
                classes.push(set);
                i = close + 1;
            }
            b'\\' if i + 1 < p.len() => {
                toks.push(Tok::Lit(p[i + 1]));
                i += 2;
            }
            c => {
                toks.push(Tok::Lit(c));
                i += 1;
            }
        }
    }
    (toks, classes)
}

/// A compiled glob with gitignore semantics: `*` and `?` stop at `/`, `**` as a whole
/// path component crosses directories (`**` elsewhere is a plain `*`).
#[derive(Debug, Clone)]
pub struct Glob(Matcher);

#[derive(Debug, Clone)]
enum Matcher {
    /// No wildcard at all: a byte comparison.
    Literal(Box<[u8]>),
    Nfa(Box<Nfa>),
}

/// Bit-parallel (shift-and) automaton over the pattern elements: bit `i` of the state
/// means elements `..i` have matched, bit `n` accepts. The masks hold one bit per
/// element, in `words` machine words.
#[derive(Debug, Clone)]
struct Nfa {
    n: usize,
    words: usize,
    /// Elements that consume exactly one byte: the text is at least this long.
    min_len: usize,
    /// No `*` or `**`: the text is exactly `min_len` bytes.
    fixed_len: bool,
    /// Literal runs the text must start and end with.
    prefix: Box<[u8]>,
    suffix: Box<[u8]>,
    /// Byte to its row of `adv` (bytes no element tells apart share a row).
    class_of: Box<[u8; 256]>,
    /// Per byte class: the elements that consume such a byte and advance.
    adv: Box<[u64]>,
    /// The other element masks, word by word.
    masks: Box<[Masks]>,
}

/// One word of the element masks that do not depend on the byte class.
#[derive(Debug, Clone, Copy, Default)]
struct Masks {
    /// Elements that stay on any byte (`**/`, trailing `**`).
    stay_any: u64,
    /// Elements that stay on a byte other than `/` (`*`).
    stay_name: u64,
    /// Elements that may match nothing (`*`, trailing `**`).
    skip: u64,
    /// Elements that may match nothing at a component boundary (`**/`).
    skip_at_boundary: u64,
}

impl Nfa {
    fn new(toks: &[Tok], classes: &[ByteSet]) -> Nfa {
        let n = toks.len();
        let words = (n + 1).div_ceil(64);
        let mut masks = vec![Masks::default(); words];
        let bit = |i: usize| 1u64 << (i % 64);
        // the advancing elements of every byte, then deduplicated into byte classes
        let mut per_byte = vec![0u64; 256 * words];
        let row = |per_byte: &mut [u64], c: usize, i: usize| {
            set_bit(&mut per_byte[c * words..(c + 1) * words], i)
        };
        for (i, t) in toks.iter().enumerate() {
            match *t {
                Tok::Lit(c) => row(&mut per_byte, c as usize, i),
                Tok::Any => (0..256)
                    .filter(|c| *c != b'/' as usize)
                    .for_each(|c| row(&mut per_byte, c, i)),
                Tok::Class(k) => (0..256)
                    .filter(|c| set_has(&classes[k as usize], *c as u8))
                    .for_each(|c| row(&mut per_byte, c, i)),
                Tok::Star => {
                    masks[i / 64].stay_name |= bit(i);
                    masks[i / 64].skip |= bit(i);
                }
                Tok::DirStar => {
                    masks[i / 64].stay_any |= bit(i);
                    masks[i / 64].skip_at_boundary |= bit(i);
                }
                Tok::AllStar => {
                    masks[i / 64].stay_any |= bit(i);
                    masks[i / 64].skip |= bit(i);
                }
            }
        }
        let mut class_of = Box::new([0u8; 256]);
        let mut adv: Vec<u64> = Vec::new();
        let mut ids: HashMap<&[u64], u8> = HashMap::new();
        for (c, id_slot) in class_of.iter_mut().enumerate() {
            let r = &per_byte[c * words..(c + 1) * words];
            *id_slot = *ids.entry(r).or_insert_with(|| {
                let id = (adv.len() / words) as u8;
                adv.extend_from_slice(r);
                id
            });
        }
        let lit = |t: &Tok| match t {
            Tok::Lit(c) => Some(*c),
            _ => None,
        };
        let prefix: Vec<u8> = toks.iter().map_while(lit).collect();
        let mut suffix: Vec<u8> = toks.iter().rev().map_while(lit).collect();
        suffix.reverse();
        Nfa {
            n,
            words,
            min_len: toks
                .iter()
                .filter(|t| matches!(t, Tok::Lit(_) | Tok::Any | Tok::Class(_)))
                .count(),
            fixed_len: !toks
                .iter()
                .any(|t| matches!(t, Tok::Star | Tok::DirStar | Tok::AllStar)),
            prefix: prefix.into(),
            suffix: suffix.into(),
            class_of,
            adv: adv.into(),
            masks: masks.into(),
        }
    }

    /// Match `t`, adding the word operations spent to `work`.
    fn matches(&self, t: &[u8], work: &mut u64) -> bool {
        *work += 1;
        if t.len() < self.min_len
            || (self.fixed_len && t.len() != self.min_len)
            || !t.starts_with(&self.prefix)
            || !t.ends_with(&self.suffix)
        {
            return false;
        }
        if self.words == 1 {
            self.matches_one_word(t, work)
        } else {
            self.matches_wide(t, work)
        }
    }

    /// Up to 63 elements: the whole state is one word.
    fn matches_one_word(&self, t: &[u8], work: &mut u64) -> bool {
        let m = self.masks[0];
        let (stay_any, stay_name, skip, skip_b) =
            (m.stay_any, m.stay_name, m.skip, m.skip_at_boundary);
        // one addition follows every run of skippable elements (see `close_wide`)
        let close = |d: u64, boundary: bool| {
            let e = if boundary { skip | skip_b } else { skip };
            d | (e.wrapping_add(d & e) ^ e)
        };
        let mut d = close(1, true);
        for (i, &c) in t.iter().enumerate() {
            let a = d & self.adv[self.class_of[c as usize] as usize];
            let stay = if c == b'/' {
                stay_any
            } else {
                stay_any | stay_name
            };
            d = (a << 1) | (d & stay);
            if d == 0 {
                *work += i as u64 + 1;
                return false;
            }
            d = close(d, c == b'/');
        }
        *work += t.len() as u64;
        (d >> self.n) & 1 == 1
    }

    fn matches_wide(&self, t: &[u8], work: &mut u64) -> bool {
        let w = self.words;
        let (mut d_buf, mut next_buf) = ([0u64; STACK_WORDS], [0u64; STACK_WORDS]);
        let (mut d_heap, mut next_heap) = (Vec::new(), Vec::new());
        let (mut d, mut next): (&mut [u64], &mut [u64]) = if w <= STACK_WORDS {
            (&mut d_buf[..w], &mut next_buf[..w])
        } else {
            d_heap.resize(w, 0);
            next_heap.resize(w, 0);
            (&mut d_heap, &mut next_heap)
        };
        d[0] = 1;
        let masks = &self.masks[..w];
        Self::close_wide(d, masks, true);
        for (i, &c) in t.iter().enumerate() {
            let adv = &self.adv[self.class_of[c as usize] as usize * w..][..w];
            let slash = c == b'/';
            let (mut carry, mut any) = (0u64, 0u64);
            for ((dk, nk), (ak, m)) in d.iter().zip(next.iter_mut()).zip(adv.iter().zip(masks)) {
                let a = dk & ak;
                let stay = if slash {
                    m.stay_any
                } else {
                    m.stay_any | m.stay_name
                };
                *nk = (a << 1) | carry | (dk & stay);
                carry = a >> 63;
                any |= *nk;
            }
            if any == 0 {
                *work += (i as u64 + 1) * w as u64;
                return false;
            }
            std::mem::swap(&mut d, &mut next);
            Self::close_wide(d, masks, slash);
        }
        *work += t.len() as u64 * w as u64;
        (d[self.n / 64] >> (self.n % 64)) & 1 == 1
    }

    /// Take every skip (`*` or `**` matching nothing) in one pass: for a run of
    /// skippable elements `e` holding active states `x`, `(e + x) ^ e` sets every
    /// element from the first active one to just past the run.
    fn close_wide(d: &mut [u64], masks: &[Masks], boundary: bool) {
        let mut carry = false;
        for (dk, m) in d.iter_mut().zip(masks) {
            let e = if boundary {
                m.skip | m.skip_at_boundary
            } else {
                m.skip
            };
            let (s1, c1) = e.overflowing_add(*dk & e);
            let (s2, c2) = s1.overflowing_add(carry as u64);
            carry = c1 || c2;
            *dk |= s2 ^ e;
        }
    }
}

/// State words kept on the stack (patterns of up to 1,087 elements, which covers every
/// ignore-file line).
const STACK_WORDS: usize = 17;

impl Glob {
    pub fn compile(p: &[u8]) -> Glob {
        let (toks, classes) = parse(p);
        let literal: Option<Vec<u8>> = toks
            .iter()
            .map(|t| match t {
                Tok::Lit(c) => Some(*c),
                _ => None,
            })
            .collect();
        Glob(match literal {
            Some(l) => Matcher::Literal(l.into()),
            None => Matcher::Nfa(Box::new(Nfa::new(&toks, &classes))),
        })
    }

    /// O(|text| × (1 + |pattern| / 64)) word operations, whatever the wildcards.
    pub fn matches(&self, t: &[u8]) -> bool {
        self.matches_counting(t, &mut 0)
    }

    /// `matches`, adding the word operations spent to `work`.
    fn matches_counting(&self, t: &[u8], work: &mut u64) -> bool {
        match &self.0 {
            Matcher::Literal(l) => {
                *work += 1;
                l.as_ref() == t
            }
            Matcher::Nfa(nfa) => nfa.matches(t, work),
        }
    }
}

#[derive(Debug, Clone)]
struct Rule {
    /// Directory of the ignore file, relative to the index root ("" at the root).
    base: String,
    glob: Glob,
    negate: bool,
    dir_only: bool,
    /// Pattern contains a slash (other than trailing): matched against the path
    /// relative to `base` rather than against any single name.
    anchored: bool,
}

/// One ignore-file line.
enum Line<'a> {
    /// Blank, a comment, or an empty pattern.
    NotARule,
    TooLong,
    Rule {
        pattern: &'a str,
        negate: bool,
        dir_only: bool,
        anchored: bool,
    },
}

fn parse_line(line: &str) -> Line<'_> {
    if !is_rule_line(line) {
        return Line::NotARule;
    }
    if line.len() > MAX_PATTERN_BYTES {
        return Line::TooLong;
    }
    let mut l = line.trim_end_matches(['\r', '\n']);
    // trailing spaces are insignificant unless escaped
    while l.ends_with(' ') && !l.ends_with("\\ ") {
        l = &l[..l.len() - 1];
    }
    let (negate, mut p) = match l.strip_prefix('!') {
        Some(rest) => (true, rest),
        None => (false, l.strip_prefix('\\').unwrap_or(l)),
    };
    let dir_only = p.ends_with('/');
    if dir_only {
        p = &p[..p.len() - 1];
    }
    let anchored = p.contains('/');
    let p = p.strip_prefix('/').unwrap_or(p);
    if p.is_empty() {
        return Line::NotARule;
    }
    Line::Rule {
        pattern: p,
        negate,
        dir_only,
        anchored,
    }
}

#[derive(Debug, Clone, Default)]
pub struct Ignore {
    rules: Vec<Rule>,
    /// Pattern bytes held by `rules`.
    pattern_bytes: usize,
    /// Pattern lines and ignore files skipped by the bounds.
    pub dropped_rules: usize,
}

/// Automaton word operations a scan may spend on ignore matching before its first path,
/// plus `PATH_ALLOWANCE` more for every path checked. A path the rules cannot decide
/// within what is left is `Verdict::OverBudget`: the index leaves it out and reports it
/// as the `ignore_budget` coverage loss (repository content can exclude any file anyway,
/// so an expensive ignore file gains nothing but a bounded scan). Realistic rule sets
/// spend a few to a few hundred operations per path; the bound binds only rule sets
/// built to be slow (measured 7-19 ns per operation on a loaded 8-core machine, so at
/// most about 5 s plus 80 us per path).
pub const MATCH_BUDGET: u64 = 1 << 28;
/// Budget added for every path checked (see `MATCH_BUDGET`).
pub const PATH_ALLOWANCE: u64 = 4096;

/// The outcome of an ignore check.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    Included,
    Ignored,
    /// The scan's matching budget ran out before the rules decided this path.
    OverBudget,
}

/// Per-scan matching state: the directory decisions of the last path checked, reused by
/// the next path that shares its leading directories (neighbours in a sorted listing
/// share most of them), and what is left of the matching budget.
#[derive(Debug)]
pub struct DirMemo {
    /// Directory part of the last path.
    dir: String,
    /// End offset in `dir` of each leading directory decided, and whether it is ignored.
    ends: Vec<(usize, bool)>,
    budget: u64,
    /// Added to `budget` at every check.
    allowance: u64,
}

impl Default for DirMemo {
    fn default() -> Self {
        DirMemo::with_budget(MATCH_BUDGET, PATH_ALLOWANCE)
    }
}

impl DirMemo {
    pub fn with_budget(budget: u64, allowance: u64) -> Self {
        DirMemo {
            dir: String::new(),
            ends: Vec::new(),
            budget,
            allowance,
        }
    }
}

impl Ignore {
    /// The defaults only.
    pub fn with_defaults() -> Ignore {
        let mut ig = Ignore::default();
        let mut defaults = String::new();
        for d in DEFAULT_EXCLUDED_DIRS {
            defaults.push_str(&format!("{d}/\n"));
        }
        for d in DEFAULT_EXCLUDED_RELATIVE_DIRS {
            defaults.push_str(&format!("/{d}/\n"));
        }
        ig.add_file("", &defaults);
        ig
    }

    /// Whether the bounds are exhausted: further ignore files add nothing.
    pub fn is_full(&self) -> bool {
        self.rules.len() >= MAX_RULES || self.pattern_bytes >= MAX_PATTERN_TOTAL_BYTES
    }

    /// Add the rules of one ignore file located in directory `base` (relative).
    pub fn add_file(&mut self, base: &str, content: &str) {
        let mut kept = 0usize;
        for line in content.lines() {
            match parse_line(line) {
                Line::NotARule => {}
                Line::TooLong => self.dropped_rules += 1,
                Line::Rule {
                    pattern,
                    negate,
                    dir_only,
                    anchored,
                } => {
                    if kept >= MAX_RULES_PER_FILE
                        || self.rules.len() >= MAX_RULES
                        || self.pattern_bytes + pattern.len() > MAX_PATTERN_TOTAL_BYTES
                    {
                        self.dropped_rules += 1;
                        continue;
                    }
                    self.pattern_bytes += pattern.len();
                    self.rules.push(Rule {
                        base: base.trim_matches('/').to_string(),
                        glob: Glob::compile(pattern.as_bytes()),
                        negate,
                        dir_only,
                        anchored,
                    });
                    kept += 1;
                }
            }
        }
    }

    /// Read and add the ignore file `rel` (relative to `root`) whose directory is
    /// `base`. A file above `MAX_IGNORE_FILE_BYTES`, or any file once the bounds are
    /// exhausted, is skipped and counted; a missing file is not.
    pub fn load_file(&mut self, root: &Path, rel: &str, base: &str) {
        if self.is_full() {
            if std::fs::symlink_metadata(root.join(rel)).is_ok() {
                self.dropped_rules += 1;
            }
            return;
        }
        match crate::symbolgraph::read_repo_bytes_beneath_capped(root, rel, MAX_IGNORE_FILE_BYTES) {
            Ok(bytes) => self.add_file(base, &String::from_utf8_lossy(&bytes)),
            Err(e) if e.kind() == std::io::ErrorKind::InvalidInput => self.dropped_rules += 1,
            Err(_) => {}
        }
    }

    /// Load `.xmustardignore` files named in `ignore_files` (paths relative to `root`,
    /// e.g. from `git ls-files`), plus the root one when present on disk.
    pub fn load_xmustard(&mut self, root: &Path, ignore_files: &[String]) {
        let mut files: Vec<String> = ignore_files.to_vec();
        if !files.iter().any(|f| f == XMUSTARD_IGNORE) {
            files.push(XMUSTARD_IGNORE.to_string());
        }
        // shallow directories first, so deeper files override.
        files.sort_by_key(|f| (f.matches('/').count(), f.clone()));
        files.dedup();
        for f in files {
            let base = Path::new(&f)
                .parent()
                .map(|p| p.to_string_lossy().into_owned())
                .unwrap_or_default();
            self.load_file(root, &f, &base);
        }
    }

    /// Whether rule `r` matches exactly `path`, whose last component is `name`; None
    /// once `budget` is spent.
    fn rule_matches(
        r: &Rule,
        path: &str,
        name: &str,
        is_dir: bool,
        budget: &mut u64,
    ) -> Option<bool> {
        if r.dir_only && !is_dir {
            return Some(false);
        }
        let rel = if r.base.is_empty() {
            path
        } else if let Some(rest) = path.strip_prefix(&r.base).and_then(|s| s.strip_prefix('/')) {
            rest
        } else {
            return Some(false);
        };
        let text = if r.anchored { rel } else { name };
        if *budget == 0 {
            return None;
        }
        let mut work = 0u64;
        let hit = r.glob.matches_counting(text.as_bytes(), &mut work);
        *budget = budget.saturating_sub(work);
        Some(hit)
    }

    /// Last matching rule for exactly this path (not its parents): `Some(ignored)`,
    /// `None` when no rule matches, `Err` once the budget is spent.
    fn decide(&self, path: &str, is_dir: bool, budget: &mut u64) -> Result<Option<bool>, ()> {
        let name = path.rsplit('/').next().unwrap_or(path);
        for r in self.rules.iter().rev() {
            match Self::rule_matches(r, path, name, is_dir, budget) {
                None => return Err(()),
                Some(true) => return Ok(Some(!r.negate)),
                Some(false) => {}
            }
        }
        Ok(None)
    }

    /// Whether `rel` (a file path relative to the index root) is ignored: its own last
    /// matching rule, or an ignored parent directory (which a file rule cannot undo).
    /// Unbudgeted: for single checks and tests; scans use `check`.
    pub fn is_ignored(&self, rel: &str, is_dir: bool) -> bool {
        self.check(&mut DirMemo::with_budget(u64::MAX, 0), rel, is_dir) == Verdict::Ignored
    }

    /// `is_ignored` within a scan: reuses the decisions `memo` holds for the leading
    /// directories this path shares with the previous one, and spends `memo`'s budget.
    /// One memo serves one rule set; rules added later for a directory never apply to
    /// the directory itself, so a walk that loads ignore files as it descends may keep
    /// one memo.
    pub fn check(&self, memo: &mut DirMemo, rel: &str, is_dir: bool) -> Verdict {
        memo.budget = memo.budget.saturating_add(memo.allowance);
        let rel = rel.trim_matches('/');
        let dir = rel.rfind('/').map_or("", |k| &rel[..k]);
        let common = dir
            .bytes()
            .zip(memo.dir.bytes())
            .take_while(|(a, b)| a == b)
            .count();
        let keep = memo
            .ends
            .iter()
            .take_while(|(end, _)| {
                *end <= common && (*end == dir.len() || dir.as_bytes()[*end] == b'/')
            })
            .count();
        memo.ends.truncate(keep);
        memo.dir.clear();
        memo.dir.push_str(dir);
        if memo.ends.iter().any(|(_, ignored)| *ignored) {
            return Verdict::Ignored;
        }
        if !dir.is_empty() {
            let mut pos = memo.ends.last().map_or(0, |(end, _)| end + 1);
            while pos <= dir.len() {
                let end = dir[pos..].find('/').map_or(dir.len(), |k| pos + k);
                let Ok(d) = self.decide(&dir[..end], true, &mut memo.budget) else {
                    return Verdict::OverBudget;
                };
                let ignored = d == Some(true);
                memo.ends.push((end, ignored));
                if ignored {
                    return Verdict::Ignored;
                }
                pos = end + 1;
            }
        }
        match self.decide(rel, is_dir, &mut memo.budget) {
            Err(()) => Verdict::OverBudget,
            Ok(Some(true)) => Verdict::Ignored,
            Ok(_) => Verdict::Included,
        }
    }
}

fn is_rule_line(line: &str) -> bool {
    let l = line.trim_end_matches(['\r', '\n', ' ']);
    !(l.is_empty() || l.starts_with('#'))
}

/// Glob match with gitignore semantics: `*` and `?` stop at `/`, `**` crosses it.
pub fn glob_match(p: &[u8], t: &[u8]) -> bool {
    Glob::compile(p).matches(t)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn glob_semantics() {
        assert!(glob_match(b"*.rs", b"main.rs"));
        assert!(!glob_match(b"*.rs", b"src/main.rs"));
        assert!(glob_match(b"src/**/*.rs", b"src/a/b/main.rs"));
        assert!(glob_match(b"src/**/*.rs", b"src/main.rs"));
        assert!(glob_match(b"**/gen", b"a/b/gen"));
        assert!(glob_match(b"file[0-9].txt", b"file7.txt"));
        assert!(!glob_match(b"file[!0-9].txt", b"file7.txt"));
        assert!(glob_match(b"a?c", b"abc"));
    }

    #[test]
    fn double_star_components_and_plain_stars() {
        assert!(glob_match(b"a/**/b", b"a/b"));
        assert!(glob_match(b"a/**/b", b"a/x/y/b"));
        assert!(!glob_match(b"a/**/b", b"a/xb"));
        assert!(glob_match(b"foo/**", b"foo/x/y"));
        assert!(!glob_match(b"foo/**", b"foo"));
        assert!(glob_match(b"**", b"any/thing"));
        // `**` inside a component is a plain `*`
        assert!(glob_match(b"a**b", b"axxb"));
        assert!(!glob_match(b"a**b", b"a/b"));
        // an escaped `*` is literal
        assert!(glob_match(b"\\*x", b"*x"));
        assert!(!glob_match(b"\\*x", b"yx"));
    }

    #[test]
    fn pathological_patterns_match_in_linear_time() {
        // `*a*a*a...*b` against a long run of `a`: exponential for a backtracking matcher
        // (measured 81 s at 10 wildcards), O(pattern x path) here.
        let pat = format!("{}*b", "*a".repeat(64));
        let name = format!("{}.ts", "a".repeat(4000));
        let t = std::time::Instant::now();
        assert!(!glob_match(pat.as_bytes(), name.as_bytes()));
        let mut ig = Ignore::with_defaults();
        ig.add_file("", &format!("{pat}\n**/{pat}\n"));
        assert!(!ig.is_ignored(&format!("src/{name}"), false));
        assert!(
            t.elapsed() < std::time::Duration::from_secs(2),
            "{:?}",
            t.elapsed()
        );
    }

    #[test]
    fn rule_bounds_are_enforced_and_counted() {
        let mut ig = Ignore::default();
        let long = "x".repeat(MAX_PATTERN_BYTES + 1);
        let mut file = format!("{long}\n# comment\n\n");
        for i in 0..MAX_RULES_PER_FILE + 10 {
            file.push_str(&format!("gen{i}/\n"));
        }
        ig.add_file("", &file);
        assert_eq!(ig.rules.len(), MAX_RULES_PER_FILE);
        assert_eq!(ig.dropped_rules, 11);
        assert!(ig.is_ignored("gen0/a.ts", false));
    }

    #[test]
    fn totals_bound_rules_and_pattern_bytes_across_files() {
        // rule count across files
        let mut ig = Ignore::with_defaults();
        let defaults = ig.rules.len();
        let file: String = (0..MAX_RULES_PER_FILE).map(|i| format!("g{i}\n")).collect();
        for d in 0..4 {
            ig.add_file(&format!("d{d}"), &file);
        }
        assert_eq!(ig.rules.len(), MAX_RULES);
        assert_eq!(
            ig.dropped_rules,
            4 * MAX_RULES_PER_FILE - (MAX_RULES - defaults)
        );
        assert!(ig.is_full());
        // pattern bytes across files: 1,000-byte patterns fill the byte budget first
        let mut ig = Ignore::with_defaults();
        let big: String = (0..MAX_RULES_PER_FILE)
            .map(|i| format!("{}{i:03}\n", "*a".repeat(498)))
            .collect();
        ig.add_file("", &big);
        ig.add_file("sub", &big);
        assert!(ig.pattern_bytes <= MAX_PATTERN_TOTAL_BYTES);
        assert!(ig.pattern_bytes > MAX_PATTERN_TOTAL_BYTES - MAX_PATTERN_BYTES);
        assert!(ig.rules.len() < 100, "{}", ig.rules.len());
    }

    #[test]
    fn oversized_ignore_files_are_skipped_whole_and_counted() {
        let dir = tempfile::TempDir::new().unwrap();
        let big = "*.gen.ts\n".repeat((MAX_IGNORE_FILE_BYTES as usize / 9) + 1);
        std::fs::write(dir.path().join(".xmustardignore"), &big).unwrap();
        std::fs::create_dir(dir.path().join("sub")).unwrap();
        std::fs::write(dir.path().join("sub/.xmustardignore"), "*.pb.ts\n").unwrap();
        let mut ig = Ignore::with_defaults();
        ig.load_xmustard(dir.path(), &["sub/.xmustardignore".to_string()]);
        assert_eq!(ig.dropped_rules, 1);
        assert!(!ig.is_ignored("a.gen.ts", false));
        assert!(ig.is_ignored("sub/x.pb.ts", false));
        // once the bounds are exhausted, present ignore files are counted, not read
        let mut full = Ignore::default();
        let file: String = (0..MAX_RULES_PER_FILE).map(|i| format!("g{i}\n")).collect();
        full.add_file("", &file);
        full.add_file("x", &file);
        let before = full.dropped_rules;
        full.load_file(dir.path(), "sub/.xmustardignore", "sub");
        full.load_file(dir.path(), "missing/.xmustardignore", "missing");
        assert_eq!(full.dropped_rules, before + 1);
    }

    /// The per-element simulation the bit-parallel automaton replaced (reference).
    fn reference_match(p: &[u8], t: &[u8]) -> bool {
        let (toks, classes) = parse(p);
        let n = toks.len();
        let close = |s: &mut Vec<bool>, boundary: bool| {
            for i in 0..n {
                if s[i] {
                    match toks[i] {
                        Tok::Star | Tok::AllStar => s[i + 1] = true,
                        Tok::DirStar if boundary => s[i + 1] = true,
                        _ => {}
                    }
                }
            }
        };
        let mut cur = vec![false; n + 1];
        cur[0] = true;
        close(&mut cur, true);
        for &c in t {
            let mut next = vec![false; n + 1];
            for i in 0..n {
                if !cur[i] {
                    continue;
                }
                match toks[i] {
                    Tok::Lit(x) => next[i + 1] |= c == x,
                    Tok::Any => next[i + 1] |= c != b'/',
                    Tok::Class(k) => next[i + 1] |= set_has(&classes[k as usize], c),
                    Tok::Star => next[i] |= c != b'/',
                    Tok::DirStar | Tok::AllStar => next[i] = true,
                }
            }
            cur = next;
            close(&mut cur, c == b'/');
        }
        cur[n]
    }

    #[test]
    fn bit_parallel_matcher_agrees_with_the_reference() {
        let mut seed = 0x9e37_79b9_7f4a_7c15u64;
        let mut rnd = |m: u64| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            seed % m
        };
        let atoms: &[&str] = &[
            "a", "b", "/", "*", "**", "**/", "?", "[ab]", "[!a]", "[a-b]", "\\*", ".",
        ];
        let text_bytes = b"ab/.*";
        for round in 0..4_000 {
            // short patterns use one word; every 40th is long enough to need several
            let len = if round % 40 == 0 {
                60 + rnd(90)
            } else {
                1 + rnd(7)
            };
            let p: String = (0..len)
                .map(|_| atoms[rnd(atoms.len() as u64) as usize])
                .collect();
            for _ in 0..6 {
                let tlen = rnd(if len > 20 { 100 } else { 12 });
                let t: Vec<u8> = (0..tlen)
                    .map(|_| text_bytes[rnd(text_bytes.len() as u64) as usize])
                    .collect();
                assert_eq!(
                    glob_match(p.as_bytes(), &t),
                    reference_match(p.as_bytes(), &t),
                    "pattern {p:?} text {:?}",
                    String::from_utf8_lossy(&t)
                );
            }
        }
    }

    #[test]
    fn memoized_decisions_agree_with_uncached_ones() {
        let mut ig = Ignore::with_defaults();
        ig.add_file("", "gen/\n!keep.ts\n*.pb.ts\n/top/\n");
        ig.add_file("a", "b/\n!vendor/\nx*.ts\n");
        let paths = [
            "a/b/c.ts",
            "a/b/d.ts",
            "a/c/x1.ts",
            "a/c/y.ts",
            "a/vendor/v.ts",
            "gen/k.ts",
            "top/t.ts",
            "src/top/t.ts",
            "src/gen/keep.ts",
            "src/p.pb.ts",
            "keep.ts",
            "z.ts",
            "a/b",
            "vendor/x/y.ts",
            "src/vendor/x.ts",
            "a/c/d/e/f.ts",
        ];
        let mut memo = DirMemo::default();
        for round in 0..3 {
            let mut order: Vec<&str> = paths.to_vec();
            if round == 1 {
                order.sort();
            } else if round == 2 {
                order.reverse();
            }
            for p in order {
                assert_eq!(
                    ig.check(&mut memo, p, false) == Verdict::Ignored,
                    ig.is_ignored(p, false),
                    "{p}"
                );
            }
        }
    }

    #[test]
    fn an_exhausted_budget_leaves_paths_undecided_but_keeps_cached_decisions() {
        let mut ig = Ignore::with_defaults();
        ig.add_file("", "gen/\n*x*y*z*\n");
        let mut probe = DirMemo::with_budget(u64::MAX, 0);
        assert_eq!(ig.check(&mut probe, "gen/a.ts", false), Verdict::Ignored);
        let cost = u64::MAX - probe.budget;
        let mut memo = DirMemo::with_budget(cost, 0);
        assert_eq!(ig.check(&mut memo, "gen/a.ts", false), Verdict::Ignored);
        assert_eq!(memo.budget, 0);
        // a directory decided before the budget ran out still decides its files
        assert_eq!(ig.check(&mut memo, "gen/b.ts", false), Verdict::Ignored);
        let long = format!("src/{}.ts", "x".repeat(64));
        assert_eq!(ig.check(&mut memo, &long, false), Verdict::OverBudget);
        assert_eq!(ig.check(&mut memo, "gen/c.ts", false), Verdict::OverBudget);
        // the per-path allowance decides cheap paths again
        let mut memo = DirMemo::with_budget(0, cost);
        assert_eq!(ig.check(&mut memo, "gen/a.ts", false), Verdict::Ignored);
        assert_eq!(ig.check(&mut memo, &long, false), Verdict::OverBudget);
        assert!(
            !ig.is_ignored(&long, false),
            "unbudgeted checks always decide"
        );
    }

    #[test]
    fn max_size_rule_sets_match_in_bounded_time() {
        // the bounds filled with patterns that keep every automaton state alive, from
        // nested ignore files that all apply to the paths checked
        let mut ig = Ignore::with_defaults();
        let mut base = String::new();
        for d in 0..10 {
            let file: String = (0..MAX_RULES_PER_FILE)
                .map(|i| format!("{}*b{i:03}*\n", "*a".repeat(30)))
                .collect();
            ig.add_file(&base, &file);
            base = if base.is_empty() {
                format!("d{d}")
            } else {
                format!("{base}/d{d}")
            };
        }
        assert!(ig.rules.len() >= MAX_RULES - 50, "{}", ig.rules.len());
        assert!(ig.pattern_bytes >= 60_000, "{}", ig.pattern_bytes);
        // about 405k operations per path: the budget decides the first few hundred
        // paths, the allowance keeps every later one bounded
        let t = std::time::Instant::now();
        let mut memo = DirMemo::default();
        let paths: u64 = if cfg!(debug_assertions) { 100 } else { 3000 };
        let (mut decided, mut undecided) = (0u64, 0u64);
        for i in 0..paths {
            let p = format!("{base}/{}{i:04}.ts", "a".repeat(200));
            match ig.check(&mut memo, &p, false) {
                Verdict::Included => decided += 1,
                Verdict::OverBudget => undecided += 1,
                Verdict::Ignored => panic!("{p} ignored"),
            }
        }
        let spent = MATCH_BUDGET + paths * PATH_ALLOWANCE - memo.budget;
        eprintln!(
            "{paths} paths: {:?}, {spent} word operations, {decided} decided",
            t.elapsed()
        );
        assert!(decided > 0);
        if cfg!(not(debug_assertions)) {
            assert!(undecided > 0, "the budget never bound");
        }
        // one rule check may overrun the budget by its own cost
        assert!(
            spent <= MATCH_BUDGET + paths * PATH_ALLOWANCE + 2 * 205,
            "{spent}"
        );
        let limit = if cfg!(debug_assertions) { 120 } else { 30 };
        assert!(
            t.elapsed() < std::time::Duration::from_secs(limit),
            "{:?}",
            t.elapsed()
        );
    }

    #[test]
    fn shared_default_exclusions() {
        assert!(is_default_excluded("backend/data/x.py"));
        assert!(is_default_excluded("a/target/debug/x.rs"));
        assert!(!is_default_excluded("backend/database.py"));
        let ig = Ignore::with_defaults();
        assert!(ig.is_ignored("backend/data/x.py", false));
        assert!(ig.is_ignored("frontend/dist/app.js", false));
        assert!(!ig.is_ignored("src/backend/data/x.py", false));
    }

    #[test]
    fn defaults_negation_anchoring_and_parents() {
        let mut ig = Ignore::with_defaults();
        assert!(ig.is_ignored("node_modules/x/index.js", false));
        assert!(ig.is_ignored("pkg/vendor/lib.go", false));
        assert!(!ig.is_ignored("src/vendors.go", false));
        ig.add_file("", "!vendor/\n/generated/\n*.pb.go\n");
        assert!(!ig.is_ignored("pkg/vendor/lib.go", false));
        assert!(ig.is_ignored("generated/a.ts", false));
        assert!(!ig.is_ignored("src/generated/a.ts", false));
        assert!(ig.is_ignored("api/x.pb.go", false));
        ig.add_file("api", "!keep.pb.go\n");
        assert!(!ig.is_ignored("api/keep.pb.go", false));
        // a file rule cannot re-include a file under an ignored directory
        ig.add_file("", "!generated/a.ts\n");
        assert!(ig.is_ignored("generated/a.ts", false));
    }
}
