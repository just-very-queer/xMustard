//! Lexical fallback for packs whose grammar is not compiled into this build and for
//! grammar files above the parse bounds: precompiled declaration patterns for symbols
//! (each pack's `Pack::lexical`), and a language-aware scanner
//! (`blank_comments_and_strings`) that blanks comments and string literals before
//! collecting identifier references, so prose and literals never become references
//! here either.
//!
//! The declaration patterns, import-line and inheritance heuristics are ported from
//! `symbolgraph.rs` and `repomap.rs` (the legacy lexical graph), compiled once per
//! process instead of once per file.

use std::collections::{HashMap, HashSet};
use std::sync::OnceLock;

use regex::bytes::Regex;

use super::extract::{Lang, MAX_REFS_PER_FILE, MAX_SYMBOLS_PER_FILE};
use super::facts::{FileFacts, ImportFact, RefFact, SymbolFact, flow, ref_kind};
use super::lang::PACKS;

pub const LEXICAL_REVISION: u32 = 4;

/// Longest span given to a fallback symbol (its end is the next declaration).
const MAX_FALLBACK_SPAN: u32 = 80;

pub(crate) type Patterns = Vec<(Regex, &'static str)>;

/// Lazy-DFA cache per pattern. The default (2 MiB each) let seven patterns over a
/// generated file add megabytes to the worker; declarations need far less.
const DFA_CACHE_BYTES: usize = 64 << 10;

/// Lines longer than this (minified or generated code) are not matched against the
/// declaration patterns; their identifiers are still scanned.
const MAX_DECL_LINE: usize = 400;

/// Byte-oriented, ASCII-class patterns: Unicode `\\s`/`\\w` classes compile to large
/// automata (measured: ~4 MiB of worker footprint for the seven TS/JS patterns).
fn compile(list: &[(&str, &'static str)]) -> Patterns {
    list.iter()
        .map(|(re, k)| {
            let re = regex::bytes::RegexBuilder::new(re)
                .unicode(false)
                .dfa_size_limit(DFA_CACHE_BYTES)
                .build()
                .expect("fallback pattern compiles");
            (re, *k)
        })
        .collect()
}

/// The pack's declaration patterns (`Pack::lexical`), compiled once per process on
/// first use. The first capture group is the declared name. Grammar packs use them for
/// files above the parse bounds; packs whose grammar feature is off, for every file.
pub(crate) fn patterns(lang: Lang) -> &'static Patterns {
    static COMPILED: [OnceLock<Patterns>; PACKS.len()] = [const { OnceLock::new() }; PACKS.len()];
    COMPILED[lang as usize].get_or_init(|| compile(lang.pack().lexical))
}

const KEYWORDS: &[&str] = &[
    "if",
    "else",
    "for",
    "while",
    "switch",
    "return",
    "sizeof",
    "catch",
    "new",
    "case",
    "do",
    "throw",
    "elif",
    "unless",
    "until",
    "function",
    "constructor",
    "super",
    "typeof",
    "await",
];

/// Nesting bound for template substitutions, interpolations and JSX elements.
const MAX_LITERAL_DEPTH: u32 = 64;

/// Parentheses tracked for `if (...) /re/` (deeper ones are plain parentheses).
const MAX_PAREN_DEPTH: usize = 256;

/// What the last significant token was, for the tokens whose meaning depends on it
/// (`/` regex versus division, `<` JSX versus comparison, Ruby `%` and `<<` literals).
#[derive(Clone, Copy, PartialEq, Eq)]
enum Prev {
    /// Start of input, an operator, an opening bracket or a keyword: an expression may
    /// start here.
    Op,
    /// An identifier, number, literal or closing bracket: an operator follows.
    Value,
}

/// A Ruby heredoc whose body starts on the next line.
struct Heredoc {
    id: Vec<u8>,
    /// `<<~` / `<<-`: the terminator may be indented.
    indented: bool,
}

/// Language-aware scanner that finds comment and string-literal spans (including JS
/// template literals with their `${}` code, regex literals, JSX text, Rust raw strings
/// and nested block comments, Java text blocks, C++ raw strings and preprocessor
/// diagnostics, Ruby heredocs and %-literals, Python triple quotes). Unrecognized input
/// is treated as code, and every ambiguous construct is resolved towards blanking, so
/// literal words do not become references.
struct Blanker<'a> {
    b: &'a [u8],
    lang: Lang,
    /// The output: the source with blanked spans.
    out: Vec<u8>,
    /// Spans recorded inside a JSX attempt (applied when the outermost attempt succeeds,
    /// dropped when it fails); outside attempts spans are applied at once.
    spans: Vec<(usize, usize)>,
    /// Open JSX attempts.
    journal: u32,
    prev: Prev,
    /// Whitespace since the last significant token (Ruby's `foo %w()` / `foo /re/`).
    spaced: bool,
    heredocs: Vec<Heredoc>,
    depth: u32,
    /// JSX attempts that failed consumed this many bytes; past a bound JSX is off, so a
    /// file of comparisons cannot make the scan quadratic.
    jsx_failed: usize,
    jsx_on: bool,
    /// A regex attempt found no closing `/` before this offset (its line end).
    regex_dead_until: usize,
    /// The last token was `if`, `while` or `for` (JS family): the `(` after it opens a
    /// condition, and a `/` after its `)` starts a regex, not a division.
    cond_keyword: bool,
}

fn is_word_start(c: u8) -> bool {
    c.is_ascii_alphabetic() || c == b'_' || c == b'$' || c >= 0x80
}

fn is_word_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_' || c == b'$' || c >= 0x80
}

fn utf8_len(c: u8) -> usize {
    match c {
        0xf0..=0xff => 4,
        0xe0..=0xef => 3,
        0xc0..=0xdf => 2,
        _ => 1,
    }
}

const JS_EXPR_KEYWORDS: &[&[u8]] = &[
    b"return",
    b"typeof",
    b"instanceof",
    b"in",
    b"of",
    b"new",
    b"delete",
    b"void",
    b"throw",
    b"case",
    b"do",
    b"else",
    b"yield",
    b"await",
];

const RUBY_EXPR_KEYWORDS: &[&[u8]] = &[
    b"return", b"if", b"unless", b"elsif", b"when", b"while", b"until", b"and", b"or", b"not",
    b"then", b"case", b"in", b"puts", b"print", b"raise",
];

impl<'a> Blanker<'a> {
    fn new(b: &'a [u8], lang: Lang) -> Self {
        Blanker {
            b,
            lang,
            out: b.to_vec(),
            spans: Vec::new(),
            journal: 0,
            prev: Prev::Op,
            spaced: true,
            heredocs: Vec::new(),
            depth: 0,
            jsx_failed: 0,
            jsx_on: matches!(lang, Lang::Tsx | Lang::JavaScript),
            regex_dead_until: 0,
            cond_keyword: false,
        }
    }

    fn c_comments(&self) -> bool {
        !matches!(self.lang, Lang::Python | Lang::Ruby | Lang::Bash)
    }

    fn js(&self) -> bool {
        matches!(self.lang, Lang::TypeScript | Lang::Tsx | Lang::JavaScript)
    }

    fn blank(&mut self, a: usize, e: usize) {
        let e = e.min(self.b.len());
        if e <= a {
            return;
        }
        if self.journal > 0 {
            self.spans.push((a, e));
        } else {
            Self::apply(&mut self.out, a, e);
        }
    }

    fn apply(out: &mut [u8], a: usize, e: usize) {
        for x in &mut out[a..e] {
            if *x != b'\n' {
                *x = b' ';
            }
        }
    }

    fn set_prev(&mut self, p: Prev) {
        self.prev = p;
        self.spaced = false;
        self.cond_keyword = false;
    }

    fn at_line_start(&self, i: usize) -> bool {
        i == 0 || self.b[i - 1] == b'\n'
    }

    /// Only spaces or tabs between the previous newline and `i`.
    fn first_on_line(&self, i: usize) -> bool {
        let mut k = i;
        while k > 0 && matches!(self.b[k - 1], b' ' | b'\t') {
            k -= 1;
        }
        self.at_line_start(k)
    }

    fn eol(&self, i: usize) -> usize {
        self.b[i..]
            .iter()
            .position(|c| *c == b'\n')
            .map_or(self.b.len(), |p| i + p)
    }

    /// End of the logical line from `i`: C and C++ backslash-newline continuations
    /// included.
    fn logical_eol(&self, i: usize) -> usize {
        let b = self.b;
        let mut e = self.eol(i);
        while e < b.len()
            && e > 0
            && (b[e - 1] == b'\\' || (b[e - 1] == b'\r' && e > 1 && b[e - 2] == b'\\'))
        {
            e = self.eol(e + 1);
        }
        e
    }

    fn find_from(&self, i: usize, needle: &[u8]) -> Option<usize> {
        if i >= self.b.len() {
            return None;
        }
        find(&self.b[i..], needle).map(|p| i + p)
    }

    /// Scan code from `i`. With `nested`, stop at the `}` that closes the enclosing
    /// substitution and return its offset; otherwise run to the end.
    fn code(&mut self, mut i: usize, nested: bool) -> usize {
        let n = self.b.len();
        let mut braces = 0usize;
        // per open `(`: whether it opened an `if`/`while`/`for` condition (bounded; deeper
        // parentheses count as plain ones)
        let mut parens: Vec<bool> = Vec::new();
        let mut deep_parens = 0usize;
        while i < n {
            let c = self.b[i];
            match c {
                b'\n' => {
                    if self.heredocs.is_empty() {
                        i += 1;
                    } else {
                        i = self.heredoc_bodies(i);
                    }
                    self.spaced = true;
                    continue;
                }
                b' ' | b'\t' | b'\r' | 0x0b | 0x0c => {
                    self.spaced = true;
                    i += 1;
                    continue;
                }
                _ => {}
            }
            if let Some(e) = self.comment_at(i) {
                self.blank(i, e);
                self.spaced = true;
                i = e;
                continue;
            }
            if let Some(e) = self.literal_at(i) {
                self.set_prev(Prev::Value);
                i = e;
                continue;
            }
            if is_word_start(c) {
                i = self.word(i);
                continue;
            }
            if c.is_ascii_digit() {
                i = self.number(i);
                continue;
            }
            match c {
                b'{' => {
                    braces += 1;
                    self.set_prev(Prev::Op);
                }
                b'}' => {
                    if nested && braces == 0 {
                        return i;
                    }
                    braces = braces.saturating_sub(1);
                    self.set_prev(Prev::Op);
                }
                b'(' => {
                    if parens.len() < MAX_PAREN_DEPTH {
                        parens.push(self.cond_keyword);
                    } else {
                        deep_parens += 1;
                    }
                    self.set_prev(Prev::Op);
                }
                b')' => {
                    let condition = if deep_parens > 0 {
                        deep_parens -= 1;
                        false
                    } else {
                        parens.pop().unwrap_or(false)
                    };
                    // `if (x) /re/.test(s)`: a statement, not a division, follows
                    self.set_prev(if condition { Prev::Op } else { Prev::Value });
                }
                b']' => self.set_prev(Prev::Value),
                _ => self.set_prev(Prev::Op),
            }
            i += 1;
        }
        n
    }

    /// Code inside a substitution (`${...}`, `#{...}`, a JSX `{...}`): the offset of the
    /// closing `}`, or the end of input.
    fn nested_code(&mut self, i: usize) -> usize {
        if self.depth >= MAX_LITERAL_DEPTH {
            return self.b.len();
        }
        self.depth += 1;
        let saved = self.prev;
        self.prev = Prev::Op;
        let e = self.code(i, true);
        self.prev = saved;
        self.depth -= 1;
        e
    }

    /// End of the comment starting at `i`, if one does.
    fn comment_at(&self, i: usize) -> Option<usize> {
        let b = self.b;
        let rest = &b[i..];
        if self.c_comments() {
            if rest.starts_with(b"//") {
                // C and C++ splice a line ending in a backslash onto the next one, even
                // inside a `//` comment
                return Some(if matches!(self.lang, Lang::C | Lang::Cpp) {
                    self.logical_eol(i)
                } else {
                    self.eol(i)
                });
            }
            if rest.starts_with(b"/*") {
                if self.lang == Lang::Rust {
                    // Rust block comments nest.
                    let (mut d, mut j) = (1usize, i + 2);
                    while j < b.len() {
                        if b[j..].starts_with(b"/*") {
                            d += 1;
                            j += 2;
                        } else if b[j..].starts_with(b"*/") {
                            d -= 1;
                            j += 2;
                            if d == 0 {
                                return Some(j);
                            }
                        } else {
                            j += 1;
                        }
                    }
                    return Some(b.len());
                }
                return Some(self.find_from(i + 2, b"*/").map_or(b.len(), |p| p + 2));
            }
        }
        match self.lang {
            Lang::Python if b[i] == b'#' => Some(self.eol(i)),
            // PHP 8 attributes are `#[...]`
            Lang::Php if b[i] == b'#' && b.get(i + 1) != Some(&b'[') => Some(self.eol(i)),
            // `#` starts a comment at a word boundary (`$#` and `${#x}` are expansions)
            Lang::Bash
                if b[i] == b'#' && (i == 0 || matches!(b[i - 1], b' ' | b'\t' | b'\n' | b';')) =>
            {
                Some(self.eol(i))
            }
            Lang::Ruby => {
                if b[i] == b'#' {
                    return Some(self.eol(i));
                }
                if self.at_line_start(i) && rest.starts_with(b"=begin") {
                    let mut j = self.eol(i);
                    while j < b.len() {
                        let s = j + 1;
                        if b[s.min(b.len())..].starts_with(b"=end") {
                            return Some(self.eol(s));
                        }
                        j = self.eol(s);
                    }
                    return Some(b.len());
                }
                if self.at_line_start(i)
                    && rest.starts_with(b"__END__")
                    && matches!(rest.get(7), None | Some(b'\n') | Some(b'\r'))
                {
                    return Some(b.len());
                }
                None
            }
            Lang::C | Lang::Cpp if b[i] == b'#' && self.first_on_line(i) => {
                // diagnostic and pragma directives carry prose, not code.
                let mut j = i + 1;
                while j < b.len() && matches!(b[j], b' ' | b'\t') {
                    j += 1;
                }
                let s = j;
                while j < b.len() && b[j].is_ascii_alphabetic() {
                    j += 1;
                }
                if !matches!(
                    &b[s..j],
                    b"error" | b"warning" | b"pragma" | b"ident" | b"sccs" | b"line"
                ) {
                    return None;
                }
                Some(self.logical_eol(i))
            }
            _ if self.js() && i == 0 && rest.starts_with(b"#!") => Some(self.eol(i)),
            _ => None,
        }
    }

    /// A quoted run from `i` (the opening quote) to its closing `q`; `multiline` lets it
    /// cross newlines, `escapes` honours backslashes. Returns the end (exclusive).
    fn quoted(&self, i: usize, q: u8, multiline: bool, escapes: bool) -> usize {
        let b = self.b;
        let mut j = i + 1;
        while j < b.len() {
            let c = b[j];
            if escapes && c == b'\\' {
                j += 2;
                continue;
            }
            if c == q {
                return j + 1;
            }
            if c == b'\n' && !multiline {
                return j;
            }
            j += 1;
        }
        b.len()
    }

    /// A triple-quoted run (`"""` or `'''`) from `i`.
    fn triple(&self, i: usize, q: &[u8]) -> usize {
        let b = self.b;
        let mut j = i + 3;
        while j < b.len() {
            if b[j] == b'\\' {
                j += 2;
                continue;
            }
            if b[j..].starts_with(q) {
                return j + 3;
            }
            j += 1;
        }
        b.len()
    }

    /// The literal starting at `i`, if any: its spans are recorded and its end returned.
    fn literal_at(&mut self, i: usize) -> Option<usize> {
        let b = self.b;
        let c = b[i];
        let rest = &b[i..];
        let e = match (self.lang, c) {
            (Lang::Python, b'"' | b'\'') => {
                if rest.starts_with(b"\"\"\"") || rest.starts_with(b"'''") {
                    self.triple(i, &rest[..3])
                } else {
                    self.quoted(i, c, false, true)
                }
            }
            (Lang::Java | Lang::Kotlin | Lang::Swift | Lang::CSharp, b'"')
                if rest.starts_with(b"\"\"\"") =>
            {
                self.triple(i, b"\"\"\"")
            }
            (Lang::Bash, b'"' | b'\'') => self.quoted(i, c, true, c == b'"'),
            (Lang::Rust, b'"') => self.quoted(i, b'"', true, true),
            (Lang::Rust, b'\'') => return Some(self.rust_quote(i)),
            (Lang::Ruby, b'"' | b'`') => return Some(self.interpolated(i, c, c)),
            (Lang::Ruby, b'\'') => self.quoted(i, b'\'', true, true),
            (Lang::Go, b'`') => self.quoted(i, b'`', true, false),
            (Lang::TypeScript | Lang::Tsx | Lang::JavaScript, b'`') => {
                return Some(self.template(i));
            }
            (_, b'"' | b'\'') => self.quoted(i, c, false, true),
            (Lang::TypeScript | Lang::Tsx | Lang::JavaScript, b'/') if self.prev == Prev::Op => {
                return self.regex(i);
            }
            (Lang::Ruby, b'/')
                if self.prev == Prev::Op
                    || (self.spaced && !matches!(b.get(i + 1), Some(b' ' | b'=' | b'\t'))) =>
            {
                return self.regex(i);
            }
            (Lang::Tsx | Lang::JavaScript, b'<')
                if self.jsx_on
                    && self.prev == Prev::Op
                    && b.get(i + 1)
                        .is_some_and(|n| is_word_start(*n) || *n == b'>') =>
            {
                return self.jsx(i);
            }
            (Lang::Ruby, b'<') if rest.starts_with(b"<<") => return self.heredoc_marker(i),
            (Lang::Ruby, b'%') => return self.percent(i),
            _ => return None,
        };
        self.blank(i, e);
        Some(e)
    }

    /// Rust `'x'`, `'\n'`, `'\u{1F600}'` char literals; otherwise a lifetime or label,
    /// which is not a reference either.
    fn rust_quote(&mut self, i: usize) -> usize {
        let b = self.b;
        let n = b.len();
        if b.get(i + 1) == Some(&b'\\') {
            let lim = (i + 14).min(n);
            if let Some(k) = (i + 3..lim).find(|&k| b[k] == b'\'' || b[k] == b'\n')
                && b[k] == b'\''
            {
                self.blank(i, k + 1);
                return k + 1;
            }
        } else if let Some(&c) = b.get(i + 1)
            && c != b'\''
            && c != b'\n'
        {
            let l = utf8_len(c);
            if b.get(i + 1 + l) == Some(&b'\'') {
                self.blank(i, i + 2 + l);
                return i + 2 + l;
            }
        }
        let mut j = i + 1;
        while j < n && is_word_byte(b[j]) {
            j += 1;
        }
        self.blank(i, j);
        j
    }

    /// JS template literal from the backtick at `i`: text is blanked, `${...}` is code.
    fn template(&mut self, i: usize) -> usize {
        let b = self.b;
        let n = b.len();
        let (mut j, mut seg) = (i + 1, i);
        while j < n {
            match b[j] {
                b'\\' => j += 2,
                b'`' => {
                    self.blank(seg, j + 1);
                    return j + 1;
                }
                b'$' if b.get(j + 1) == Some(&b'{') => {
                    self.blank(seg, j + 2);
                    let k = self.nested_code(j + 2);
                    if k >= n {
                        return n;
                    }
                    seg = k;
                    j = k + 1;
                }
                _ => j += 1,
            }
        }
        self.blank(seg, n);
        n
    }

    /// Ruby double-quoted or backtick string (and %Q-style bodies) from the opener at
    /// `i` to `close`: text is blanked, `#{...}` is code. `open != close` nests.
    fn interpolated(&mut self, i: usize, open: u8, close: u8) -> usize {
        let b = self.b;
        let n = b.len();
        let (mut j, mut seg, mut depth) = (i + 1, i, 0usize);
        while j < n {
            let c = b[j];
            if c == b'\\' {
                j += 2;
                continue;
            }
            if c == b'#' && b.get(j + 1) == Some(&b'{') {
                self.blank(seg, j + 2);
                let k = self.nested_code(j + 2);
                if k >= n {
                    return n;
                }
                seg = k;
                j = k + 1;
                continue;
            }
            if open != close && c == open {
                depth += 1;
            } else if c == close {
                if depth == 0 {
                    self.blank(seg, j + 1);
                    return j + 1;
                }
                depth -= 1;
            }
            j += 1;
        }
        self.blank(seg, n);
        n
    }

    /// A regex literal from the `/` at `i` (single line; a character class may hold a
    /// `/`), with its flags. None when the line has no closing `/`: then it was division.
    fn regex(&mut self, i: usize) -> Option<usize> {
        let b = self.b;
        let n = b.len();
        if i < self.regex_dead_until || matches!(b.get(i + 1), Some(b'/' | b'*')) {
            return None;
        }
        let (mut j, mut class) = (i + 1, false);
        loop {
            if j >= n || b[j] == b'\n' {
                self.regex_dead_until = j;
                return None;
            }
            match b[j] {
                b'\\' => j += 1,
                b'[' => class = true,
                b']' => class = false,
                b'/' if !class => break,
                _ => {}
            }
            j += 1;
        }
        j += 1;
        while j < n && b[j].is_ascii_alphabetic() {
            j += 1;
        }
        self.blank(i, j);
        Some(j)
    }

    /// A JSX element (TSX, JS/JSX) at `i`; its text children and attribute strings are
    /// blanked, `{...}` stays code. None (and nothing recorded) when `<` was not JSX.
    fn jsx(&mut self, i: usize) -> Option<usize> {
        let save = self.spans.len();
        let prev = self.prev;
        self.journal += 1;
        let r = self.jsx_element(i);
        self.journal -= 1;
        match r {
            Ok(e) => {
                if self.journal == 0 {
                    for (a, e) in std::mem::take(&mut self.spans) {
                        Self::apply(&mut self.out, a, e);
                    }
                }
                Some(e)
            }
            Err(at) => {
                self.spans.truncate(save);
                self.prev = prev;
                self.jsx_failed += at.saturating_sub(i) + 1;
                if self.jsx_failed > 4 * self.b.len() + 4096 {
                    self.jsx_on = false;
                }
                None
            }
        }
    }

    fn skip_ws(&self, mut j: usize) -> usize {
        while j < self.b.len() && self.b[j].is_ascii_whitespace() {
            j += 1;
        }
        j
    }

    fn jsx_name_end(&self, mut j: usize) -> usize {
        while j < self.b.len()
            && (is_word_byte(self.b[j]) || matches!(self.b[j], b'.' | b':' | b'-'))
        {
            j += 1;
        }
        j
    }

    fn jsx_element(&mut self, i: usize) -> Result<usize, usize> {
        if self.depth >= MAX_LITERAL_DEPTH {
            return Err(i);
        }
        self.depth += 1;
        let r = self.jsx_element_inner(i);
        self.depth -= 1;
        r
    }

    fn jsx_element_inner(&mut self, i: usize) -> Result<usize, usize> {
        let b = self.b;
        let n = b.len();
        let mut j = i + 1;
        if b.get(j) == Some(&b'>') {
            j += 1; // fragment
        } else {
            if !b.get(j).is_some_and(|c| is_word_start(*c)) {
                return Err(j);
            }
            j = self.jsx_name_end(j);
            loop {
                j = self.skip_ws(j);
                match b.get(j) {
                    None => return Err(n),
                    Some(b'/') => {
                        return if b.get(j + 1) == Some(&b'>') {
                            Ok(j + 2)
                        } else {
                            Err(j)
                        };
                    }
                    Some(b'>') => {
                        j += 1;
                        break;
                    }
                    Some(b'{') => {
                        let k = self.nested_code(j + 1);
                        if k >= n {
                            return Err(k);
                        }
                        j = k + 1;
                    }
                    Some(c) if is_word_start(*c) => {
                        j = self.jsx_name_end(j);
                        let k = self.skip_ws(j);
                        if b.get(k) == Some(&b'=') {
                            let v = self.skip_ws(k + 1);
                            match b.get(v) {
                                Some(&q @ (b'"' | b'\'')) => {
                                    let e = self.quoted(v, q, true, false);
                                    if b.get(e - 1) != Some(&q) || e <= v + 1 {
                                        return Err(e);
                                    }
                                    self.blank(v, e);
                                    j = e;
                                }
                                Some(b'{') => {
                                    let e = self.nested_code(v + 1);
                                    if e >= n {
                                        return Err(e);
                                    }
                                    j = e + 1;
                                }
                                Some(b'<') => j = self.jsx_element(v)?,
                                _ => return Err(v),
                            }
                        }
                    }
                    Some(_) => return Err(j),
                }
            }
        }
        // children: text is blanked, `{...}` is code, `<...>` nests, `</...>` closes.
        let mut text = j;
        loop {
            match b.get(j) {
                None => return Err(n),
                Some(b'<') => {
                    self.blank(text, j);
                    if b.get(j + 1) == Some(&b'/') {
                        let mut k = self.skip_ws(j + 2);
                        k = self.jsx_name_end(k);
                        k = self.skip_ws(k);
                        return if b.get(k) == Some(&b'>') {
                            Ok(k + 1)
                        } else {
                            Err(k)
                        };
                    }
                    match b.get(j + 1) {
                        Some(c) if is_word_start(*c) || *c == b'>' => {
                            j = self.jsx_element(j)?;
                            text = j;
                        }
                        _ => return Err(j),
                    }
                }
                Some(b'{') => {
                    self.blank(text, j);
                    let k = self.nested_code(j + 1);
                    if k >= n {
                        return Err(k);
                    }
                    j = k + 1;
                    text = j;
                }
                Some(_) => j += 1,
            }
        }
    }

    /// Ruby `<<~ID`, `<<-ID`, `<<ID`, `<<~'ID'`: record the heredoc (its body starts on
    /// the next line) and blank the marker. None for the shift/append operator.
    fn heredoc_marker(&mut self, i: usize) -> Option<usize> {
        let b = self.b;
        let mut j = i + 2;
        let indented = matches!(b.get(j), Some(b'~' | b'-'));
        if indented {
            j += 1;
        }
        let quote = match b.get(j) {
            Some(&q @ (b'\'' | b'"' | b'`')) => {
                j += 1;
                Some(q)
            }
            _ => None,
        };
        let s = j;
        while j < b.len() && (b[j].is_ascii_alphanumeric() || b[j] == b'_') {
            j += 1;
        }
        if j == s || b[s].is_ascii_digit() {
            return None;
        }
        let id = b[s..j].to_vec();
        if let Some(q) = quote {
            if b.get(j) != Some(&q) {
                return None;
            }
            j += 1;
        }
        let expression_position = self.prev == Prev::Op || self.spaced;
        let plausible = indented || quote.is_some() || (b[s].is_ascii_uppercase() || b[s] == b'_');
        if !expression_position || !plausible {
            return None;
        }
        self.heredocs.push(Heredoc { id, indented });
        self.blank(i, j);
        Some(j)
    }

    /// At the newline `nl` that ends a line with pending heredocs: blank each body up to
    /// and including its terminator line; returns the offset after the last one.
    fn heredoc_bodies(&mut self, nl: usize) -> usize {
        let b = self.b;
        let n = b.len();
        let mut j = nl + 1;
        for h in std::mem::take(&mut self.heredocs) {
            let start = j;
            loop {
                if j >= n {
                    self.blank(start, n);
                    return n;
                }
                let e = self.eol(j);
                let mut line = &b[j..e];
                if h.indented {
                    while let [b' ' | b'\t', rest @ ..] = line {
                        line = rest;
                    }
                }
                if let [head @ .., b'\r'] = line {
                    line = head;
                }
                if line == h.id.as_slice() {
                    self.blank(start, e);
                    j = e + 1;
                    break;
                }
                j = e + 1;
            }
        }
        // resume on the newline that ends the last terminator line
        j.saturating_sub(1).min(n)
    }

    /// Ruby `%q(...)`, `%w[...]`, `%i{...}`, `%r<...>`, `%(...)`...: blanked whole.
    fn percent(&mut self, i: usize) -> Option<usize> {
        let b = self.b;
        let is_delim = |c: u8| !c.is_ascii_alphanumeric() && !c.is_ascii_whitespace() && c != b'=';
        let (j, typed) = match (b.get(i + 1), b.get(i + 2)) {
            (Some(t), Some(d)) if b"qQwWiIrsx".contains(t) && is_delim(*d) => (i + 2, true),
            (Some(d), _) if is_delim(*d) => (i + 1, false),
            _ => return None,
        };
        let expression_position = self.prev == Prev::Op || self.spaced;
        if !expression_position || (!typed && !b"([{<|!/^".contains(&b[j])) {
            return None;
        }
        let open = b[j];
        let close = match open {
            b'(' => b')',
            b'[' => b']',
            b'{' => b'}',
            b'<' => b'>',
            c => c,
        };
        let (mut k, mut depth) = (j + 1, 0usize);
        while k < b.len() {
            let c = b[k];
            if c == b'\\' {
                k += 2;
                continue;
            }
            if open != close && c == open {
                depth += 1;
            } else if c == close {
                if depth == 0 {
                    k += 1;
                    break;
                }
                depth -= 1;
            }
            k += 1;
        }
        let e = k.min(b.len());
        self.blank(i, e);
        Some(e)
    }

    /// An identifier or keyword at `i`; string prefixes and raw strings that start with
    /// letters are handled here.
    fn word(&mut self, i: usize) -> usize {
        let b = self.b;
        let n = b.len();
        let mut j = i + 1;
        while j < n && is_word_byte(b[j]) {
            j += 1;
        }
        let w = &b[i..j];
        let next = b.get(j).copied();
        let prefixed = match (self.lang, next) {
            (Lang::Rust, Some(b'#' | b'"')) if matches!(w, b"r" | b"br" | b"cr") => {
                self.rust_raw(i, j)
            }
            (Lang::Rust, Some(b'"')) if matches!(w, b"b" | b"c") => {
                let e = self.quoted(j, b'"', true, true);
                self.blank(i, e);
                Some(e)
            }
            (Lang::Rust, Some(b'\'')) if w == b"b" => {
                let e = self.rust_quote(j);
                self.blank(i, j);
                Some(e)
            }
            (Lang::C | Lang::Cpp, Some(b'"'))
                if matches!(w, b"R" | b"u8R" | b"uR" | b"UR" | b"LR") =>
            {
                self.cpp_raw(i, j)
            }
            (Lang::C | Lang::Cpp, Some(b'"' | b'\''))
                if matches!(w, b"u8" | b"u" | b"U" | b"L") =>
            {
                let e = self.quoted(j, b[j], false, true);
                self.blank(i, e);
                Some(e)
            }
            (Lang::Python, Some(b'"' | b'\''))
                if w.len() <= 2 && w.iter().all(|c| b"rRbBfFuU".contains(c)) =>
            {
                let q = b[j];
                let rest = &b[j..];
                let e = if rest.starts_with(b"\"\"\"") || rest.starts_with(b"'''") {
                    self.triple(j, &rest[..3])
                } else {
                    self.quoted(j, q, false, true)
                };
                self.blank(i, e);
                Some(e)
            }
            _ => None,
        };
        if let Some(e) = prefixed {
            self.set_prev(Prev::Value);
            return e;
        }
        let keyword = match self.lang {
            Lang::TypeScript | Lang::Tsx | Lang::JavaScript => JS_EXPR_KEYWORDS.contains(&w),
            Lang::Ruby => RUBY_EXPR_KEYWORDS.contains(&w),
            _ => false,
        };
        self.set_prev(if keyword { Prev::Op } else { Prev::Value });
        self.cond_keyword = self.js() && matches!(w, b"if" | b"while" | b"for");
        j
    }

    /// Rust `r"..."`, `r#"..."#`, `br##"..."##` (prefix from `i`, hashes from `j`).
    /// None for a raw identifier such as `r#type`.
    fn rust_raw(&mut self, i: usize, j: usize) -> Option<usize> {
        let b = self.b;
        let mut k = j;
        while k < b.len() && b[k] == b'#' {
            k += 1;
        }
        if b.get(k) != Some(&b'"') {
            return None;
        }
        let mut close = vec![b'"'];
        close.extend(std::iter::repeat_n(b'#', k - j));
        let e = self
            .find_from(k + 1, &close)
            .map_or(b.len(), |p| p + close.len());
        self.blank(i, e);
        Some(e)
    }

    /// C++ `R"delim(...)delim"` (prefix from `i`, quote at `j`).
    fn cpp_raw(&mut self, i: usize, j: usize) -> Option<usize> {
        let b = self.b;
        let lim = (j + 18).min(b.len());
        let paren = (j + 1..lim).find(|&k| b[k] == b'(')?;
        let delim = &b[j + 1..paren];
        if delim
            .iter()
            .any(|c| matches!(c, b' ' | b')' | b'\\' | b'\n' | b'\t'))
        {
            return None;
        }
        let mut close = vec![b')'];
        close.extend_from_slice(delim);
        close.push(b'"');
        let e = self
            .find_from(paren + 1, &close)
            .map_or(b.len(), |p| p + close.len());
        self.blank(i, e);
        Some(e)
    }

    fn number(&mut self, i: usize) -> usize {
        let b = self.b;
        let mut j = i + 1;
        while j < b.len() {
            let c = b[j];
            let separator = c == b'\''
                && matches!(self.lang, Lang::C | Lang::Cpp)
                && b.get(j + 1).is_some_and(|d| d.is_ascii_alphanumeric());
            if c.is_ascii_alphanumeric() || c == b'_' || c == b'.' || separator {
                j += 1;
            } else {
                break;
            }
        }
        self.set_prev(Prev::Value);
        j
    }

    fn finish(self) -> String {
        debug_assert!(self.journal == 0 && self.spans.is_empty());
        match String::from_utf8(self.out) {
            Ok(s) => s,
            Err(e) => String::from_utf8_lossy(e.as_bytes()).into_owned(),
        }
    }
}

/// Replace comment and string-literal bytes with spaces (newlines kept), so lines and
/// columns stay aligned with the source.
pub fn blank_comments_and_strings(text: &str, lang: Lang) -> String {
    let mut bl = Blanker::new(text.as_bytes(), lang);
    bl.code(0, false);
    bl.finish()
}

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    hay.windows(needle.len()).position(|w| w == needle)
}

fn is_ident_start(c: u8) -> bool {
    c.is_ascii_alphabetic() || c == b'_'
}

fn is_ident_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

/// Identifier spans `(word, start_col, end_col)` on one line, lazily (a minified line
/// holds hundreds of thousands).
fn identifier_spans(line: &str) -> impl Iterator<Item = (&str, usize, usize)> + '_ {
    let b = line.as_bytes();
    let mut i = 0;
    std::iter::from_fn(move || {
        while i < b.len() {
            if is_ident_start(b[i]) && (i == 0 || !is_ident_byte(b[i - 1])) {
                let s = i;
                while i < b.len() && is_ident_byte(b[i]) {
                    i += 1;
                }
                return Some((&line[s..i], s, i));
            }
            i += 1;
        }
        None
    })
}

const BRANCH_KEYWORDS: &[&str] = &["if", "while", "match", "switch", "elif", "when", "case"];

/// Legacy `line_flows` semantics: an identifier after `return` returns, after a branch
/// keyword branches, and one followed by an assignment operator is written. The
/// keyword positions are the line's first `return` and first branch keyword
/// (`line_keywords`), found once per line so long lines stay linear.
fn flow_of(line: &str, keywords: (Option<usize>, Option<usize>), start: usize, end: usize) -> u8 {
    let (first_return, first_branch) = keywords;
    if first_return.is_some_and(|s| s < start) {
        return flow::RETURNS;
    }
    if first_branch.is_some_and(|s| s < start) {
        return flow::BRANCHES;
    }
    let after = line.get(end..).unwrap_or("").trim_start();
    if (after.starts_with('=') && !after.starts_with("=="))
        || ["+=", "-=", "*=", "/="]
            .iter()
            .any(|op| after.starts_with(op))
    {
        return flow::WRITES;
    }
    flow::NONE
}

/// Start columns of the first `return` and the first branch keyword on a line.
fn line_keywords(line: &str) -> (Option<usize>, Option<usize>) {
    let (mut ret, mut branch) = (None, None);
    for (w, s, _) in identifier_spans(line) {
        if ret.is_none() && w == "return" {
            ret = Some(s);
        }
        if branch.is_none() && BRANCH_KEYWORDS.contains(&w) {
            branch = Some(s);
        }
        if ret.is_some() && branch.is_some() {
            break;
        }
    }
    (ret, branch)
}

fn is_import_line(lang: Lang, line: &str) -> bool {
    let t = line.trim_start();
    match lang {
        Lang::Python => t.starts_with("import ") || t.starts_with("from "),
        Lang::Java => t.starts_with("import ") || t.starts_with("package "),
        Lang::Ruby => {
            t.starts_with("require ")
                || t.starts_with("require_relative ")
                || t.starts_with("load ")
        }
        Lang::TypeScript | Lang::Tsx | Lang::JavaScript => {
            t.starts_with("import ") || (t.starts_with("export ") && t.contains(" from "))
        }
        Lang::Go => t.starts_with("import ") && !t.starts_with("import ("),
        Lang::Rust => {
            t.starts_with("use ") || t.starts_with("pub use ") || t.starts_with("extern crate ")
        }
        Lang::Kotlin | Lang::Swift => t.starts_with("import "),
        Lang::Php => t.starts_with("use ") || t.starts_with("require") || t.starts_with("include"),
        Lang::Bash => t.starts_with("source ") || t.starts_with(". "),
        Lang::C | Lang::Cpp | Lang::CSharp => {
            t.starts_with("#include") || t.starts_with("#import") || t.starts_with("using ")
        }
    }
}

/// Supertype names on an inheritance line (ported from the legacy heuristic).
fn inheritance_names(lang: Lang, line: &str) -> Vec<String> {
    let t = line.trim();
    let mut out = Vec::new();
    let mut take = |s: &str| {
        for tok in s.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
            if !tok.is_empty()
                && tok
                    .chars()
                    .next()
                    .is_some_and(|c| c.is_alphabetic() || c == '_')
            {
                out.push(tok.to_string());
            }
        }
    };
    for kw in ["extends ", "implements "] {
        if let Some(i) = t.find(kw) {
            let tail = &t[i + kw.len()..];
            take(tail.split('{').next().unwrap_or(""));
        }
    }
    if matches!(lang, Lang::Python)
        && t.starts_with("class ")
        && let (Some(o), Some(c)) = (t.find('('), t.find(')'))
        && c > o
    {
        take(&t[o + 1..c]);
    }
    if matches!(lang, Lang::Ruby)
        && t.starts_with("class ")
        && let Some(i) = t.find('<')
    {
        take(&t[i + 1..]);
    }
    if matches!(lang, Lang::Cpp)
        && (t.starts_with("class ") || t.starts_with("struct "))
        && let Some(i) = t.find(':')
    {
        take(
            &t[i + 1..]
                .replace("public", " ")
                .replace("private", " ")
                .replace("protected", " ")
                .replace("virtual", " "),
        );
    }
    out.retain(|w| !matches!(w.as_str(), "extends" | "implements" | "object"));
    out
}

fn parse_imports(lang: Lang, line_no: u32, raw: &str, out: &mut Vec<ImportFact>) {
    let t = raw.trim().trim_end_matches(';').trim();
    let push = |out: &mut Vec<ImportFact>,
                kind: &str,
                module: &str,
                name: Option<&str>,
                alias: Option<&str>| {
        out.push(ImportFact {
            line: line_no,
            kind: kind.to_string(),
            module: module.trim().to_string(),
            name: name.map(|s| s.trim().to_string()).filter(|s| !s.is_empty()),
            alias: alias
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty()),
        })
    };
    match lang {
        Lang::Python => {
            if let Some(rest) = t.strip_prefix("from ") {
                if let Some((module, names)) = rest.split_once(" import ") {
                    for item in names.trim_matches(|c| c == '(' || c == ')').split(',') {
                        let (name, alias) = match item.split_once(" as ") {
                            Some((n, a)) => (n, Some(a)),
                            None => (item, None),
                        };
                        push(out, "import", module, Some(name), alias);
                    }
                }
            } else if let Some(rest) = t.strip_prefix("import ") {
                for item in rest.split(',') {
                    let (module, alias) = match item.split_once(" as ") {
                        Some((m, a)) => (m, Some(a)),
                        None => (item, None),
                    };
                    push(out, "import", module, None, alias);
                }
            }
        }
        Lang::Java => {
            if let Some(rest) = t.strip_prefix("import ") {
                let rest = rest.strip_prefix("static ").unwrap_or(rest);
                let (module, name) = rest.rsplit_once('.').unwrap_or(("", rest));
                push(out, "import", module, Some(name), None);
            }
        }
        Lang::Ruby => {
            for kw in ["require_relative ", "require ", "load "] {
                if let Some(rest) = t.strip_prefix(kw) {
                    let m = rest
                        .trim()
                        .trim_matches(|c| c == '\'' || c == '"' || c == '(' || c == ')');
                    push(out, "require", m, None, None);
                    break;
                }
            }
        }
        Lang::TypeScript | Lang::Tsx | Lang::JavaScript => {
            let kind = if t.starts_with("export ") {
                "reexport"
            } else {
                "import"
            };
            let quoted = |s: &str| -> Option<String> {
                let i = s.find(['\'', '"', '`'])?;
                let q = s.as_bytes()[i] as char;
                let rest = &s[i + 1..];
                rest.find(q).map(|e| rest[..e].to_string())
            };
            let module = t
                .rfind(" from ")
                .and_then(|i| quoted(&t[i..]))
                .or_else(|| t.strip_prefix("import ").and_then(quoted));
            if let Some(m) = module {
                push(out, kind, &m, None, None);
            }
        }
        Lang::Go => {
            let path = t.trim_start_matches("import").trim();
            let (alias, path) = match path.split_once(char::is_whitespace) {
                Some((a, p)) if !a.starts_with('"') => (Some(a), p.trim()),
                _ => (None, path),
            };
            let m = path.trim_matches(|c| c == '"' || c == '(' || c == ')' || c == '`');
            if !m.is_empty() {
                let name = m.rsplit('/').next();
                push(out, "import", m, name, alias);
            }
        }
        Lang::Rust => {
            let body = t
                .trim_start_matches("pub ")
                .trim_start_matches("use ")
                .trim_start_matches("extern crate ");
            let kind = if t.starts_with("pub ") {
                "reexport"
            } else {
                "use"
            };
            let (module, name) = match body.split_once("::{") {
                Some((m, _)) => (m, None),
                None => match body.rsplit_once("::") {
                    Some((m, n)) => (m, Some(n)),
                    None => ("", Some(body)),
                },
            };
            push(out, kind, module, name, None);
        }
        Lang::Kotlin | Lang::Swift | Lang::Php => {
            let body = ["import ", "use "].iter().find_map(|kw| t.strip_prefix(kw));
            let quoted = t
                .split(['\'', '"'])
                .nth(1)
                .filter(|_| t.starts_with("require") || t.starts_with("include"));
            match (body, quoted) {
                (Some(b), _) => {
                    let (module, alias) = match b.split_once(" as ") {
                        Some((m, a)) => (m, Some(a)),
                        None => (b, None),
                    };
                    push(out, "import", module, None, alias);
                }
                (None, Some(m)) => push(out, "require", m, None, None),
                (None, None) => {}
            }
        }
        Lang::Bash => {
            let rest = t.strip_prefix("source ").or_else(|| t.strip_prefix(". "));
            if let Some(m) = rest.and_then(|r| r.split_whitespace().next()) {
                push(
                    out,
                    "require",
                    m.trim_matches(|c| c == '"' || c == '\''),
                    None,
                    None,
                );
            }
        }
        Lang::C | Lang::Cpp | Lang::CSharp => {
            for kw in ["#include", "#import"] {
                if let Some(rest) = t.strip_prefix(kw) {
                    let m = rest
                        .trim()
                        .trim_matches(|c| c == '<' || c == '>' || c == '"');
                    push(out, "include", m, None, None);
                    return;
                }
            }
            if let Some(rest) = t.strip_prefix("using ") {
                push(
                    out,
                    "use",
                    rest.trim_start_matches("namespace "),
                    None,
                    None,
                );
            }
        }
    }
}

/// Extract facts without a grammar. `text` is the full source.
pub fn extract(lang: Lang, text: &str) -> FileFacts {
    let stripped = blank_comments_and_strings(text, lang);
    let raw_lines: Vec<&str> = text.lines().collect();
    let code_lines: Vec<&str> = stripped.lines().collect();
    let mut facts = FileFacts {
        engine: "regex".into(),
        ..Default::default()
    };
    // byte offset of each line start, for symbol and reference offsets.
    let mut offsets = Vec::with_capacity(raw_lines.len());
    let mut acc = 0u32;
    for l in text.split_inclusive('\n') {
        offsets.push(acc);
        acc += l.len() as u32;
    }

    // ---- symbols (from code lines, so commented-out declarations do not count) ----
    let mut symbol_lines: Vec<u32> = Vec::new();
    for (i, line) in code_lines.iter().enumerate() {
        if line.len() > MAX_DECL_LINE {
            continue;
        }
        for (re, kind) in patterns(lang) {
            let Some(cap) = re.captures(line.as_bytes()) else {
                continue;
            };
            let Some(m) = cap.get(1) else { continue };
            let Ok(name) = std::str::from_utf8(m.as_bytes()) else {
                continue;
            };
            if KEYWORDS.contains(&name) {
                continue;
            }
            if facts.symbols.len() >= MAX_SYMBOLS_PER_FILE {
                facts.symbols_truncated = true;
                break;
            }
            let line_no = i as u32 + 1;
            let indent = line.len() - line.trim_start().len();
            let kind =
                if *kind == "Function" && indent > 0 && matches!(lang, Lang::Python | Lang::Ruby) {
                    "Method"
                } else {
                    kind
                };
            let off = offsets.get(i).copied().unwrap_or(0);
            facts.symbols.push(SymbolFact {
                name: name.to_string(),
                qualified_name: name.to_string(),
                kind: kind.to_string(),
                container: None,
                arity: matches!(kind, "Function" | "Method").then_some(0),
                start_byte: off,
                end_byte: off + line.len() as u32,
                start_line: line_no,
                end_line: line_no,
                name_line: line_no,
                name_col: m.start() as u32,
                signature_hash: String::new(),
                exported: !name.starts_with('_'),
                depth: (indent > 0) as u32,
                local: false,
                uid_suffix: String::new(),
            });
            symbol_lines.push(line_no);
            break;
        }
    }
    // a fallback symbol spans to the line before the next declaration.
    let n_lines = raw_lines.len() as u32;
    for i in 0..facts.symbols.len() {
        let next = symbol_lines.get(i + 1).copied().unwrap_or(n_lines + 1);
        let s = &mut facts.symbols[i];
        s.end_line = (next - 1)
            .max(s.start_line)
            .min(s.start_line + MAX_FALLBACK_SPAN);
        let end_ix = s.end_line as usize;
        s.end_byte = offsets.get(end_ix).copied().unwrap_or(acc);
    }

    // ---- references, imports, inheritance ----
    let mut name_ix: HashMap<String, u32> = HashMap::new();
    let defs: HashMap<u32, &str> = facts
        .symbols
        .iter()
        .map(|s| (s.name_line, s.name.as_str()))
        .collect();
    let mut refs: Vec<RefFact> = Vec::new();
    let mut names: Vec<String> = Vec::new();
    let mut imports = Vec::new();
    let mut go_import_block = false;
    for (i, line) in code_lines.iter().enumerate() {
        let line_no = i as u32 + 1;
        let raw = raw_lines.get(i).copied().unwrap_or("");
        let mut import = is_import_line(lang, line);
        if lang == Lang::Go {
            let rt = raw.trim();
            if rt.starts_with("import (") {
                go_import_block = true;
                import = false;
            } else if go_import_block {
                if rt.starts_with(')') {
                    go_import_block = false;
                } else if !rt.is_empty() {
                    import = true;
                }
            }
        }
        if import {
            parse_imports(lang, line_no, raw, &mut imports);
        }
        let supers: HashSet<String> = inheritance_names(lang, line).into_iter().collect();
        let keywords = line_keywords(line);
        for (word, s, e) in identifier_spans(line) {
            if defs.get(&line_no).is_some_and(|d| *d == word) || KEYWORDS.contains(&word) {
                continue;
            }
            if refs.len() >= MAX_REFS_PER_FILE {
                facts.symbols_truncated = true;
                break;
            }
            let kind = if import {
                ref_kind::IMPORT
            } else if supers.contains(word) {
                ref_kind::EXTENDS
            } else {
                ref_kind::WORD
            };
            let ni = *name_ix.entry(word.to_string()).or_insert_with(|| {
                names.push(word.to_string());
                (names.len() - 1) as u32
            });
            let off = offsets.get(i).copied().unwrap_or(0) + s as u32;
            // fallback symbols are sorted and do not overlap: the last one starting at
            // or before this line contains it when its span reaches the line.
            let at = facts
                .symbols
                .partition_point(|sym| sym.start_line <= line_no);
            let container = at
                .checked_sub(1)
                .filter(|&p| line_no <= facts.symbols[p].end_line)
                .map(|p| p as i32)
                .unwrap_or(-1);
            refs.push(RefFact(
                ni,
                line_no,
                s as u32,
                off,
                container,
                kind,
                flow_of(line, keywords, s, e),
                -1,
            ));
        }
    }
    facts.names = names;
    facts.refs = refs;
    facts.imports = imports;
    facts
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pack_patterns_compile_once_per_process() {
        // PAR-RT-11: the fallback compiled its patterns per file; now each pack's set is
        // compiled on first use and every later file reuses it.
        for p in PACKS.iter() {
            let first = patterns(p.lang) as *const Patterns;
            let started = std::time::Instant::now();
            for _ in 0..1000 {
                assert!(
                    std::ptr::eq(patterns(p.lang), first),
                    "{} recompiled",
                    p.name
                );
            }
            // 1,000 lookups of a compiled set take microseconds; one compile takes more
            assert!(started.elapsed() < std::time::Duration::from_millis(50));
            assert_eq!(patterns(p.lang).len(), p.lexical.len());
        }
    }

    #[test]
    fn comments_and_strings_are_blanked() {
        let src = "x = \"call hidden_fn\" # also mentioned_fn\ny = real_fn()\n'''doc other_fn'''\n";
        let out = blank_comments_and_strings(src, Lang::Python);
        assert_eq!(out.len(), src.len());
        assert!(
            !out.contains("hidden_fn")
                && !out.contains("mentioned_fn")
                && !out.contains("other_fn")
        );
        assert!(out.contains("real_fn"));
        let c = blank_comments_and_strings("a(); // b_fn\n/* c_fn */ d(\"e_fn\");\n", Lang::C);
        assert!(
            !c.contains("b_fn") && !c.contains("c_fn") && !c.contains("e_fn") && c.contains("d(")
        );
    }

    /// `src` blanked for `lang` keeps every `code` word and no `ghost` word, and keeps
    /// the byte length and line structure.
    fn check(lang: Lang, src: &str, code: &[&str], ghosts: &[&str]) {
        let out = blank_comments_and_strings(src, lang);
        assert_eq!(out.len(), src.len(), "{lang:?}");
        assert_eq!(
            out.matches('\n').count(),
            src.matches('\n').count(),
            "{lang:?}"
        );
        let words: std::collections::HashSet<&str> = out
            .lines()
            .flat_map(|l| identifier_spans(l).map(|(w, _, _)| w))
            .collect();
        for w in code {
            assert!(words.contains(w), "{lang:?}: code word {w} lost in\n{out}");
        }
        for w in ghosts {
            assert!(
                !out.contains(w),
                "{lang:?}: literal word {w} kept in\n{out}"
            );
        }
    }

    #[test]
    fn rust_literals_are_blanked() {
        check(
            Lang::Rust,
            "fn run<'a>(x: &'a str) -> char {\n    let s = \"first line\n GhostTarget end\";\n    let r = r#\"raw \"GhostRaw\" here\"#;\n    let b = br##\"GhostBytes\"##;\n    /* outer /* GhostNested */ still GhostComment */\n    let c = '\\u{1F600}';\n    let q = '\"';\n    'outer: loop { break 'outer; }\n    real_call(s, r, b, c, q)\n}\n",
            &["run", "x", "real_call", "loop", "s", "r", "b", "c", "q"],
            &[
                "GhostTarget",
                "GhostRaw",
                "GhostBytes",
                "GhostNested",
                "GhostComment",
                "outer",
                "first",
            ],
        );
        // a raw identifier is code, not a string
        check(
            Lang::Rust,
            "let r#type = 1; use_it(r#type);\n",
            &["use_it", "type"],
            &[],
        );
    }

    #[test]
    fn js_ts_literals_regex_and_jsx_text_are_blanked() {
        check(
            Lang::TypeScript,
            "const re = /GhostRegex [a-z/]+/gi;\nconst t = `tpl GhostTpl ${inner(`nested GhostNest ${deep}`)} tail`;\nconst ratio = total / count / 2;\n",
            &["re", "t", "inner", "deep", "ratio", "total", "count"],
            &["GhostRegex", "GhostTpl", "GhostNest", "tpl", "tail", "gi"],
        );
        check(
            Lang::Tsx,
            "export function View(p: Props) {\n  const f = <T,>(x: T) => x;\n  return (\n    <div className=\"GhostClass\" onClick={() => handle(p)}>\n      Open GhostPanel here\n      {p.items.map((it) => <Item key={it.id} label='GhostLabel' />)}\n      <>fragment GhostFrag</>\n    </div>\n  );\n}\nconst less = a < b && c > d;\n",
            &[
                "View", "handle", "items", "map", "Item", "it", "id", "less", "a", "b", "c", "d",
                "f", "x",
            ],
            &[
                "GhostClass",
                "GhostPanel",
                "GhostLabel",
                "GhostFrag",
                "Open",
                "fragment",
            ],
        );
        // .ts has no JSX: a type assertion stays code
        check(
            Lang::TypeScript,
            "const v = <Widget>thing;\n",
            &["Widget", "thing"],
            &[],
        ); // a regex after the condition of `if`/`while`/`for` is a regex; after a call's
        // `)` a `/` is still division
        check(
            Lang::Tsx,
            "function f(props, s) {\n  if (props.x) /GhostRe2/.test(s);\n  while (next(s)) /GhostRe3/g.exec(s);\n  for (let i = 0; i < n(s); i++) /GhostRe4/.test(s);\n  return size(s) / scale / 2;\n}\n",
            &[
                "props", "x", "test", "next", "exec", "i", "n", "size", "scale",
            ],
            &["GhostRe2", "GhostRe3", "GhostRe4"],
        );
    }

    #[test]
    fn java_c_and_cpp_literals_are_blanked() {
        check(
            Lang::Java,
            "class Legacy {\n  String s = \"\"\"\n    text block GhostJava AKIASECRETJAVA\n    \"\"\";\n  char c = '\"';\n  void run() { helper(s, c); }\n}\n",
            &["Legacy", "String", "run", "helper", "s", "c"],
            &["GhostJava", "AKIASECRETJAVA", "block"],
        );
        check(
            Lang::Cpp,
            "#include <vector>\n#error cpreprocessorsecret words\n#pragma message(\"GhostPragma\")\nauto s = R\"x(raw \" GhostRawCpp)x\";\nint n = 1'000'000;\nauto w = L\"GhostWide\";\nint main() { return use(s, n, w); }\n",
            &["include", "vector", "main", "use", "s", "n", "w"],
            &[
                "cpreprocessorsecret",
                "GhostPragma",
                "GhostRawCpp",
                "GhostWide",
            ],
        ); // a `//` comment ending in a backslash continues on the next line
        for lang in [Lang::C, Lang::Cpp] {
            check(
                lang,
                "int a = 1; // note \\\ncontinued GhostCont \\\r\nGhostCont2 more\nint b = use(a);\n",
                &["a", "b", "use"],
                &["continued", "GhostCont", "GhostCont2", "more", "note"],
            );
        }
    }

    #[test]
    fn ruby_heredocs_percent_literals_and_regex_are_blanked() {
        check(
            Lang::Ruby,
            "class R\n  def run(items)\n    doc = <<~EOS\n      rubyheredocsecret words here\n    EOS\n    raw = <<-'RAW'\n      GhostRaw\n      RAW\n    q = %q{rubypercentsecret {inside}}\n    w = %w[GhostWord other]\n    s = \"interp #{helper(items)} GhostInterp\"\n    m = items.select { |x| x =~ /GhostRegex/ }\n    items << other_item\n    total = count % 3\n    finish(doc, raw, q, w, s, m, total)\n  end\nend\n=begin\nGhostBegin\n=end\n__END__\nGhostData\n",
            &[
                "R",
                "run",
                "items",
                "doc",
                "raw",
                "helper",
                "select",
                "x",
                "other_item",
                "count",
                "finish",
                "total",
            ],
            &[
                "rubyheredocsecret",
                "GhostRaw",
                "rubypercentsecret",
                "inside",
                "GhostWord",
                "GhostInterp",
                "GhostRegex",
                "GhostBegin",
                "GhostData",
                "EOS",
                "RAW",
            ],
        );
    }

    #[test]
    fn python_and_go_literals_are_blanked() {
        check(
            Lang::Python,
            "def run(x):\n    s = f\"GhostF {x}\"\n    b = rb'GhostRb'\n    d = '''doc\n    GhostTriple'''\n    return helper(s, b, d)  # GhostComment\n",
            &["run", "x", "s", "b", "d", "helper"],
            &["GhostF", "GhostRb", "GhostTriple", "GhostComment"],
        );
        check(
            Lang::Go,
            "func Run() string {\n\tr := `raw \\ GhostGoRaw`\n\tc := '\"'\n\treturn helper(r, c) // GhostGo\n}\n",
            &["Run", "r", "c", "helper"],
            &["GhostGoRaw", "GhostGo"],
        );
    }

    #[test]
    fn failed_jsx_attempts_stay_linear() {
        // thousands of `<x` in expression position that never close
        let mut src = String::new();
        for i in 0..20_000 {
            src.push_str(&format!("v{i} = (<x{i} \n"));
        }
        let t = std::time::Instant::now();
        let out = blank_comments_and_strings(&src, Lang::Tsx);
        assert_eq!(out.len(), src.len());
        assert!(
            t.elapsed() < std::time::Duration::from_secs(5),
            "{:?}",
            t.elapsed()
        );
    }

    #[test]
    fn long_single_lines_extract_in_linear_time() {
        // minified-bundle shapes on one line (the reference flow of each identifier used
        // to scan the whole line: 67-270 s at 1 MB in release builds); a quarter of the
        // size in debug builds, where the old quadratic cost still takes minutes
        let n = if cfg!(debug_assertions) { 4 } else { 1 };
        let shapes: [(Lang, String); 4] = [
            (Lang::Rust, "'a".repeat(500_000 / n)),
            (Lang::Tsx, "a <b && <c ".repeat(90_000 / n)),
            (Lang::JavaScript, "<a>{".repeat(250_000 / n)),
            (
                Lang::Java,
                format!("class A extends {}B {{}}", "Base, ".repeat(160_000 / n)),
            ),
        ];
        for (lang, src) in shapes {
            let t = std::time::Instant::now();
            let f = extract(lang, &src);
            let limit = if cfg!(debug_assertions) { 30 } else { 5 };
            assert!(
                t.elapsed() < std::time::Duration::from_secs(limit),
                "{lang:?}: {:?} for {} bytes ({} refs)",
                t.elapsed(),
                src.len(),
                f.refs.len()
            );
        }
    }

    #[test]
    fn python_fallback_extracts_symbols_refs_and_imports() {
        let src = "from pkg.mod import helper as h, other\nimport os\n\nclass Widget(BaseWidget):\n    def render(self):\n        # calls ghost_fn\n        return compute_total(self)\n";
        let f = extract(Lang::Python, src);
        let syms: Vec<(&str, &str)> = f
            .symbols
            .iter()
            .map(|s| (s.name.as_str(), s.kind.as_str()))
            .collect();
        assert_eq!(syms, vec![("Widget", "Class"), ("render", "Method")]);
        let names: Vec<&str> = f
            .refs
            .iter()
            .map(|r| f.names[r.name_idx()].as_str())
            .collect();
        assert!(names.contains(&"compute_total"));
        assert!(!names.contains(&"ghost_fn"));
        let total = f
            .refs
            .iter()
            .find(|r| f.names[r.name_idx()] == "compute_total")
            .unwrap();
        assert_eq!(total.flow(), flow::RETURNS);
        let base = f
            .refs
            .iter()
            .find(|r| f.names[r.name_idx()] == "BaseWidget")
            .unwrap();
        assert_eq!(base.kind(), ref_kind::EXTENDS);
        assert_eq!(f.imports.len(), 3);
        assert_eq!(f.imports[0].module, "pkg.mod");
        assert_eq!(f.imports[0].name.as_deref(), Some("helper"));
        assert_eq!(f.imports[0].alias.as_deref(), Some("h"));
    }
}
