//! Lexical fallback for the languages without a grammar in this build (Python, Java,
//! Ruby, C, C++): precompiled declaration patterns for symbols, and a scanner that
//! blanks comments and string literals before collecting identifier references, so
//! prose and literals never become references here either.
//!
//! The declaration patterns, import-line and inheritance heuristics are ported from
//! `symbolgraph.rs` and `repomap.rs` (the legacy lexical graph), compiled once per
//! process instead of once per file.

use std::collections::HashMap;
use std::sync::OnceLock;

use regex::bytes::Regex;

use super::extract::{Lang, MAX_REFS_PER_FILE, MAX_SYMBOLS_PER_FILE};
use super::facts::{FileFacts, ImportFact, RefFact, SymbolFact, flow, ref_kind};

pub const LEXICAL_REVISION: u32 = 1;

/// Longest span given to a fallback symbol (its end is the next declaration).
const MAX_FALLBACK_SPAN: u32 = 80;

type Patterns = Vec<(Regex, &'static str)>;

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

/// Declaration patterns per language family, compiled once per process. The first
/// capture group is the declared name. The TS/JS, Go and Rust sets serve files above
/// `max_parse_bytes`; Python and the def/class/func/fn forms follow the legacy regexes.
fn patterns(lang: Lang) -> &'static Patterns {
    static JS: OnceLock<Patterns> = OnceLock::new();
    static GO: OnceLock<Patterns> = OnceLock::new();
    static RUST: OnceLock<Patterns> = OnceLock::new();
    static PY: OnceLock<Patterns> = OnceLock::new();
    static RUBY: OnceLock<Patterns> = OnceLock::new();
    static JAVA: OnceLock<Patterns> = OnceLock::new();
    static C: OnceLock<Patterns> = OnceLock::new();
    match lang {
        Lang::TypeScript | Lang::Tsx | Lang::JavaScript => JS.get_or_init(|| {
            compile(&[
                (r"^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\*?\s+([A-Za-z_$][\w$]*)", "Function"),
                (r"^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)", "Class"),
                (r"^\s*(?:export\s+)?(?:declare\s+)?interface\s+([A-Za-z_$][\w$]*)", "Interface"),
                (r"^\s*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][\w$]*)\s*(?:<[^=]*>)?\s*=", "TypeAlias"),
                (r"^\s*(?:export\s+)?(?:declare\s+)?(?:const\s+)?enum\s+([A-Za-z_$][\w$]*)", "Enum"),
                (r"^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]*)?=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*(?::[^=]*)?=>|[A-Za-z_$][\w$]*\s*=>)", "Function"),
                (r"^\s+(?:(?:public|private|protected|static|async|readonly|override|abstract|get|set)\s+)*([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\([^)]*\)\s*(?::\s*[^{;]+)?\{\s*$", "Method"),
            ])
        }),
        Lang::Go => GO.get_or_init(|| {
            compile(&[
                (r"^\s*func\s+(?:\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*[\[(]", "Function"),
                (r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+struct\b", "Struct"),
                (r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+interface\b", "Interface"),
                (r"^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b", "TypeAlias"),
            ])
        }),
        Lang::Rust => RUST.get_or_init(|| {
            compile(&[
                (r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:const\s+)?(?:async\s+)?(?:unsafe\s+)?(?:extern\s+\S+\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)", "Function"),
                (r"^\s*(?:pub(?:\([^)]*\))?\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)", "Struct"),
                (r"^\s*(?:pub(?:\([^)]*\))?\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)", "Enum"),
                (r"^\s*(?:pub(?:\([^)]*\))?\s+)?(?:unsafe\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)", "Trait"),
                (r"^\s*(?:pub(?:\([^)]*\))?\s+)?type\s+([A-Za-z_][A-Za-z0-9_]*)", "TypeAlias"),
            ])
        }),
        Lang::Python => PY.get_or_init(|| {
            compile(&[
                (r"^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(", "Function"),
                (r"^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b", "Class"),
            ])
        }),
        Lang::Ruby => RUBY.get_or_init(|| {
            compile(&[
                (r"^\s*def\s+(?:self\.)?([A-Za-z_][A-Za-z0-9_]*[?!]?)", "Function"),
                (r"^\s*class\s+([A-Z][A-Za-z0-9_]*)\b", "Class"),
                (r"^\s*module\s+([A-Z][A-Za-z0-9_]*)\b", "Module"),
            ])
        }),
        Lang::Java => JAVA.get_or_init(|| {
            compile(&[
                (r"^\s*(?:(?:public|private|protected|static|final|abstract|sealed)\s+)*(?:class|interface|enum|record)\s+([A-Za-z_][A-Za-z0-9_]*)\b", "Class"),
                (r"^\s*(?:(?:public|private|protected|static|final|abstract|synchronized|native|default)\s+)+[A-Za-z_][A-Za-z0-9_<>\[\], ?]*\s+([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*$", "Method"),
            ])
        }),
        Lang::C | Lang::Cpp => C.get_or_init(|| {
            compile(&[
                (r"^\s*(?:typedef\s+)?(?:struct|union|enum|class)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^{]*)?\{", "Struct"),
                (r"^[A-Za-z_][A-Za-z0-9_\s\*&:<>,]*[\s\*&]([A-Za-z_][A-Za-z0-9_:]*)\s*\([^;]*\)\s*(?:const\s*)?\{?\s*$", "Function"),
            ])
        }),
    }
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

/// Comment and string syntax of a fallback language family.
#[derive(Clone, Copy)]
struct Syntax {
    line_comment: &'static [&'static str],
    block_comment: Option<(&'static str, &'static str)>,
    triple_quotes: bool,
    /// Backtick strings (JS template literals, Go raw strings, Ruby commands).
    backtick: bool,
    /// `'` starts a string only as a short char literal (`'a'`, `'\n'`): Rust
    /// lifetimes and labels use a lone `'`.
    char_quote_only: bool,
}

fn syntax(lang: Lang) -> Syntax {
    match lang {
        Lang::Python => Syntax {
            line_comment: &["#"],
            block_comment: None,
            triple_quotes: true,
            backtick: false,
            char_quote_only: false,
        },
        Lang::Ruby => Syntax {
            line_comment: &["#"],
            block_comment: Some(("=begin", "=end")),
            triple_quotes: false,
            backtick: true,
            char_quote_only: false,
        },
        Lang::Rust => Syntax {
            line_comment: &["//"],
            block_comment: Some(("/*", "*/")),
            triple_quotes: false,
            backtick: false,
            char_quote_only: true,
        },
        Lang::Go | Lang::TypeScript | Lang::Tsx | Lang::JavaScript => Syntax {
            line_comment: &["//"],
            block_comment: Some(("/*", "*/")),
            triple_quotes: false,
            backtick: true,
            char_quote_only: false,
        },
        _ => Syntax {
            line_comment: &["//"],
            block_comment: Some(("/*", "*/")),
            triple_quotes: false,
            backtick: false,
            char_quote_only: false,
        },
    }
}

/// Replace comment and string-literal bytes with spaces (newlines kept), so lines and
/// columns stay aligned with the source.
pub fn blank_comments_and_strings(text: &str, lang: Lang) -> String {
    let sx = syntax(lang);
    let b = text.as_bytes();
    let mut out = b.to_vec();
    let mut i = 0;
    let blank = |out: &mut Vec<u8>, a: usize, e: usize| {
        for x in out.iter_mut().take(e).skip(a) {
            if *x != b'\n' {
                *x = b' ';
            }
        }
    };
    while i < b.len() {
        let rest = &b[i..];
        if let Some(lc) = sx
            .line_comment
            .iter()
            .find(|lc| rest.starts_with(lc.as_bytes()))
        {
            let _ = lc;
            let e = rest
                .iter()
                .position(|c| *c == b'\n')
                .map_or(b.len(), |p| i + p);
            blank(&mut out, i, e);
            i = e;
            continue;
        }
        if let Some((open, close)) = sx.block_comment
            && rest.starts_with(open.as_bytes())
        {
            let e = find(&b[i + open.len()..], close.as_bytes())
                .map_or(b.len(), |p| i + open.len() + p + close.len());
            blank(&mut out, i, e);
            i = e;
            continue;
        }
        if sx.triple_quotes && (rest.starts_with(b"\"\"\"") || rest.starts_with(b"'''")) {
            let q = &rest[..3];
            let e = find(&b[i + 3..], q).map_or(b.len(), |p| i + 3 + p + 3);
            blank(&mut out, i, e);
            i = e;
            continue;
        }
        let c = b[i];
        if c == b'\'' && sx.char_quote_only {
            // 'x' or '\x' (a char literal); otherwise a lifetime/label: leave it.
            let close = if b.get(i + 1) == Some(&b'\\') {
                i + 3
            } else {
                i + 2
            };
            if b.get(close) == Some(&b'\'') {
                blank(&mut out, i, close + 1);
                i = close + 1;
            } else {
                i += 1;
            }
            continue;
        }
        if c == b'"' || c == b'\'' || (c == b'`' && sx.backtick) {
            let multiline = c == b'`';
            let mut j = i + 1;
            while j < b.len() && b[j] != c && (multiline || b[j] != b'\n') {
                if b[j] == b'\\' {
                    j += 1;
                }
                j += 1;
            }
            let e = (j + 1).min(b.len());
            blank(&mut out, i, e);
            i = e;
            continue;
        }
        i += 1;
    }
    String::from_utf8(out).unwrap_or_default()
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

/// Identifier spans `(word, start_col, end_col)` on one line.
fn identifier_spans(line: &str) -> Vec<(&str, usize, usize)> {
    let b = line.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < b.len() {
        if is_ident_start(b[i]) && (i == 0 || !is_ident_byte(b[i - 1])) {
            let s = i;
            while i < b.len() && is_ident_byte(b[i]) {
                i += 1;
            }
            out.push((&line[s..i], s, i));
        } else {
            i += 1;
        }
    }
    out
}

const BRANCH_KEYWORDS: &[&str] = &["if", "while", "match", "switch", "elif", "when", "case"];

/// Legacy `line_flows` semantics: an identifier after `return` returns, after a branch
/// keyword branches, and one followed by an assignment operator is written.
fn flow_of(line: &str, spans: &[(&str, usize, usize)], idx: usize) -> u8 {
    let (_, start, end) = spans[idx];
    if spans.iter().any(|(w, s, _)| *w == "return" && *s < start) {
        return flow::RETURNS;
    }
    if spans
        .iter()
        .any(|(w, s, _)| BRANCH_KEYWORDS.contains(w) && *s < start)
    {
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
        _ => t.starts_with("#include") || t.starts_with("#import") || t.starts_with("using "),
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
        _ => {
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
        let supers = inheritance_names(lang, line);
        let spans = identifier_spans(line);
        for (k, (word, s, _)) in spans.iter().enumerate() {
            if defs.get(&line_no).is_some_and(|d| d == word) || KEYWORDS.contains(word) {
                continue;
            }
            if refs.len() >= MAX_REFS_PER_FILE {
                facts.symbols_truncated = true;
                break;
            }
            let kind = if import {
                ref_kind::IMPORT
            } else if supers.iter().any(|x| x == word) {
                ref_kind::EXTENDS
            } else {
                ref_kind::WORD
            };
            let ni = *name_ix.entry((*word).to_string()).or_insert_with(|| {
                names.push((*word).to_string());
                (names.len() - 1) as u32
            });
            let off = offsets.get(i).copied().unwrap_or(0) + *s as u32;
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
                *s as u32,
                off,
                container,
                kind,
                flow_of(line, &spans, k),
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
