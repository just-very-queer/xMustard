//! Function-aligned chunks and the code tokenizer for their FTS5 postings.
//!
//! Every outermost callable (function, method, constructor, named closure) is one chunk,
//! split into windows when it is long. The lines between callables (imports, type
//! headers, fields, top-level statements) become `gap` chunks. Postings are pre-tokenized
//! here: each word is indexed lowercased, plus its camelCase/snake_case subtokens, so
//! `parseConfigFile` matches `parse config file` (measured in D-02).

use sha2::{Digest, Sha256};

use super::facts::{ChunkFact, SymbolFact};

/// Longest callable chunk; longer bodies are split into windows of this many lines.
pub const MAX_SYMBOL_CHUNK_LINES: u32 = 150;
/// Longest gap chunk.
pub const MAX_GAP_CHUNK_LINES: u32 = 60;

/// Byte offset of the start of each line, plus a final entry equal to `bytes.len()`.
/// Line `n` (1-based) spans `starts[n-1]..starts[n]`.
pub fn line_starts(bytes: &[u8]) -> Vec<u32> {
    let mut v = Vec::with_capacity(bytes.len() / 32 + 2);
    v.push(0);
    for (i, b) in bytes.iter().enumerate() {
        if *b == b'\n' && i + 1 < bytes.len() {
            v.push((i + 1) as u32);
        }
    }
    v.push(bytes.len() as u32);
    v
}

/// Number of lines in `bytes` (a trailing newline does not start a new line).
pub fn line_count(starts: &[u32]) -> u32 {
    if starts.len() <= 1 || starts[starts.len() - 1] == 0 {
        0
    } else {
        (starts.len() - 1) as u32
    }
}

fn is_blank(bytes: &[u8], starts: &[u32], line: u32) -> bool {
    let (a, b) = (
        starts[(line - 1) as usize] as usize,
        starts[line as usize] as usize,
    );
    bytes[a..b].iter().all(|c| c.is_ascii_whitespace())
}

fn nested_in_callable(symbols: &[SymbolFact], i: usize) -> bool {
    let mut cur = symbols[i].container;
    let mut guard = 0;
    while let Some(c) = cur {
        let c = c as usize;
        if c >= symbols.len() || guard > 256 {
            return false;
        }
        if symbols[c].is_callable() {
            return true;
        }
        cur = symbols[c].container;
        guard += 1;
    }
    false
}

/// The innermost symbol whose line range contains `[a, b]`.
fn enclosing(symbols: &[SymbolFact], a: u32, b: u32) -> Option<u32> {
    let mut best: Option<(u32, u32)> = None; // (span, index)
    for (i, s) in symbols.iter().enumerate() {
        if s.start_line <= a && b <= s.end_line {
            let span = s.end_line - s.start_line;
            if best.is_none_or(|(bs, _)| span < bs) {
                best = Some((span, i as u32));
            }
        }
    }
    best.map(|(_, i)| i)
}

fn hash16(bytes: &[u8]) -> String {
    format!("{:x}", Sha256::digest(bytes))[..16].to_string()
}

/// Plan the chunks of one file from its symbols and bytes.
pub fn plan_chunks(symbols: &[SymbolFact], bytes: &[u8], starts: &[u32]) -> Vec<ChunkFact> {
    let n = line_count(starts);
    if n == 0 {
        return Vec::new();
    }
    let mut spans: Vec<(u32, u32, u32)> = symbols
        .iter()
        .enumerate()
        .filter(|(i, s)| s.is_callable() && !nested_in_callable(symbols, *i))
        .map(|(i, s)| (s.start_line.max(1), s.end_line.min(n), i as u32))
        .filter(|(a, b, _)| a <= b)
        .collect();
    spans.sort_unstable();
    let mut out = Vec::new();
    let mut cursor = 1u32;
    for (a, b, i) in spans {
        if a < cursor {
            continue; // inside or overlapping an earlier callable span
        }
        if a > cursor {
            push_gap(&mut out, symbols, bytes, starts, cursor, a - 1);
        }
        if b - a < MAX_SYMBOL_CHUNK_LINES {
            push_chunk(&mut out, bytes, starts, (a, b), "symbol", Some(i));
        } else {
            let mut s = a;
            while s <= b {
                let e = (s + MAX_SYMBOL_CHUNK_LINES - 1).min(b);
                push_chunk(&mut out, bytes, starts, (s, e), "window", Some(i));
                s = e + 1;
            }
        }
        cursor = b + 1;
    }
    if cursor <= n {
        push_gap(&mut out, symbols, bytes, starts, cursor, n);
    }
    out
}

fn push_chunk(
    out: &mut Vec<ChunkFact>,
    bytes: &[u8],
    starts: &[u32],
    (a, b): (u32, u32),
    kind: &str,
    symbol: Option<u32>,
) {
    let sb = starts[(a - 1) as usize];
    let eb = starts[b as usize];
    out.push(ChunkFact {
        symbol,
        kind: kind.to_string(),
        start_line: a,
        end_line: b,
        start_byte: sb,
        end_byte: eb,
        content_hash: hash16(&bytes[sb as usize..eb as usize]),
    });
}

/// Gap lines `[a, b]` minus leading/trailing blank lines, in pieces of at most
/// MAX_GAP_CHUNK_LINES.
fn push_gap(
    out: &mut Vec<ChunkFact>,
    symbols: &[SymbolFact],
    bytes: &[u8],
    starts: &[u32],
    a: u32,
    b: u32,
) {
    let (mut a, mut b) = (a, b);
    while a <= b && is_blank(bytes, starts, a) {
        a += 1;
    }
    while b >= a && is_blank(bytes, starts, b) {
        b -= 1;
    }
    let mut s = a;
    while s <= b {
        let e = (s + MAX_GAP_CHUNK_LINES - 1).min(b);
        let sym = enclosing(symbols, s, e);
        push_chunk(out, bytes, starts, (s, e), "gap", sym);
        s = e + 1;
    }
}

/// Split an identifier into camelCase / snake_case / digit-boundary subtokens.
pub fn subtokens(word: &str) -> Vec<String> {
    let mut out = Vec::new();
    for part in word.split('_').filter(|p| !p.is_empty()) {
        let chars: Vec<char> = part.chars().collect();
        let mut cur = String::new();
        for (i, &c) in chars.iter().enumerate() {
            let boundary = i > 0 && {
                let prev = chars[i - 1];
                (c.is_uppercase() && prev.is_lowercase())
                    || (c.is_uppercase()
                        && prev.is_uppercase()
                        && chars.get(i + 1).is_some_and(|n| n.is_lowercase()))
                    || (c.is_ascii_digit() != prev.is_ascii_digit())
            };
            if boundary && !cur.is_empty() {
                out.push(cur.to_lowercase());
                cur.clear();
            }
            cur.push(c);
        }
        if !cur.is_empty() {
            out.push(cur.to_lowercase());
        }
    }
    out
}

/// Append the index tokens of one word: the lowercased word, then its subtokens when
/// there are two or more. Pure numbers are skipped.
pub fn push_word_tokens(word: &str, out: &mut String) {
    if word.is_empty() || word.chars().all(|c| c.is_ascii_digit() || c == '_') {
        return;
    }
    out.push_str(&word.to_lowercase());
    out.push(' ');
    let subs = subtokens(word);
    if subs.len() > 1 {
        for s in subs {
            out.push_str(&s);
            out.push(' ');
        }
    }
}

/// Tokenize free text: every run of letters, digits and `_`.
pub fn tokenize_into(text: &str, out: &mut String) {
    for word in text.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
        push_word_tokens(word, out);
    }
}

/// Tokens of a repository path: its segments, the stem and the extension.
pub fn path_tokens(path: &str) -> String {
    let mut out = String::new();
    tokenize_into(path, &mut out);
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn callable(q: &str, a: u32, b: u32, container: Option<u32>) -> SymbolFact {
        SymbolFact {
            name: q.to_string(),
            qualified_name: q.to_string(),
            kind: "Function".into(),
            container,
            arity: Some(0),
            start_byte: 0,
            end_byte: 0,
            start_line: a,
            end_line: b,
            name_line: a,
            name_col: 0,
            signature_hash: String::new(),
            exported: false,
            depth: 0,
            local: false,
            uid_suffix: String::new(),
        }
    }

    #[test]
    fn subtokens_split_camel_snake_and_acronyms() {
        assert_eq!(subtokens("parseConfigFile"), ["parse", "config", "file"]);
        assert_eq!(subtokens("HTTPServer2"), ["http", "server", "2"]);
        assert_eq!(subtokens("snake_case_name"), ["snake", "case", "name"]);
        let mut out = String::new();
        tokenize_into("let x = parseConfig(42);", &mut out);
        assert_eq!(out, "let x parseconfig parse config ");
    }

    #[test]
    fn chunks_align_to_callables_and_cover_gaps() {
        let src = "import a\n\nfn one() {\n  1\n}\n\nconst X = 1\nfn two() {\n}\n";
        let bytes = src.as_bytes();
        let starts = line_starts(bytes);
        assert_eq!(line_count(&starts), 9);
        let syms = vec![
            callable("one", 3, 5, None),
            callable("two", 8, 9, None),
            callable("inner", 4, 4, Some(0)),
        ];
        let chunks = plan_chunks(&syms, bytes, &starts);
        let lines: Vec<(u32, u32, &str)> = chunks
            .iter()
            .map(|c| (c.start_line, c.end_line, c.kind.as_str()))
            .collect();
        assert_eq!(
            lines,
            vec![
                (1, 1, "gap"),
                (3, 5, "symbol"),
                (7, 7, "gap"),
                (8, 9, "symbol")
            ]
        );
        assert_eq!(
            &src[chunks[1].start_byte as usize..chunks[1].end_byte as usize],
            "fn one() {\n  1\n}\n"
        );
    }

    #[test]
    fn long_callables_split_into_windows() {
        let src: String = (0..320).map(|i| format!("line{i}\n")).collect();
        let starts = line_starts(src.as_bytes());
        let syms = vec![callable("big", 1, 320, None)];
        let chunks = plan_chunks(&syms, src.as_bytes(), &starts);
        let spans: Vec<(u32, u32)> = chunks.iter().map(|c| (c.start_line, c.end_line)).collect();
        assert_eq!(spans, vec![(1, 150), (151, 300), (301, 320)]);
        assert!(
            chunks
                .iter()
                .all(|c| c.kind == "window" && c.symbol == Some(0))
        );
    }
}
