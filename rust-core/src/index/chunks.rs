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

/// Longest doc chunk (WS-18); a longer heading section is split into windows.
pub const MAX_DOC_CHUNK_LINES: u32 = 40;

/// One chunk of a doc or guidance file: a heading section, or a window of a long one,
/// with blank lines trimmed at both ends. Byte offsets index the file's raw bytes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DocChunk {
    pub start_line: u32,
    pub end_line: u32,
    pub start_byte: u32,
    pub end_byte: u32,
    /// Line of the heading that opens the chunk's section, if any.
    pub heading: Option<u32>,
    pub content_hash: String,
}

/// Whether `line` is a Markdown ATX heading: up to three spaces, one to six `#`, then a
/// space or the end of the line.
fn is_atx_heading(line: &[u8]) -> bool {
    let indent = line.iter().take_while(|c| **c == b' ').count();
    if indent > 3 {
        return false;
    }
    let rest = &line[indent..];
    let hashes = rest.iter().take_while(|c| **c == b'#').count();
    (1..=6).contains(&hashes)
        && rest
            .get(hashes)
            .is_none_or(|c| matches!(c, b' ' | b'\t' | b'\r' | b'\n'))
}

/// Whether `line` opens or closes a fenced code block.
fn is_fence(line: &[u8]) -> bool {
    let t = line.trim_ascii_start();
    t.starts_with(b"```") || t.starts_with(b"~~~")
}

/// Plan the chunks of one doc: sections start at Markdown headings outside fenced code
/// (`markdown`), and every section is cut into windows of at most MAX_DOC_CHUNK_LINES.
pub fn plan_doc_chunks(bytes: &[u8], starts: &[u32], markdown: bool) -> Vec<DocChunk> {
    let n = line_count(starts);
    if n == 0 {
        return Vec::new();
    }
    let text = |l: u32| &bytes[starts[(l - 1) as usize] as usize..starts[l as usize] as usize];
    // (first line, heading line) of every section
    let mut sections: Vec<(u32, Option<u32>)> = vec![(1, None)];
    let mut fenced = false;
    for l in 1..=n {
        let line = text(l);
        if markdown && is_fence(line) {
            fenced = !fenced;
        } else if markdown && !fenced && is_atx_heading(line) {
            match l {
                1 => sections[0].1 = Some(1),
                _ => sections.push((l, Some(l))),
            }
        }
    }
    let mut out = Vec::new();
    for (i, &(a, heading)) in sections.iter().enumerate() {
        let b = sections.get(i + 1).map_or(n, |next| next.0 - 1);
        let (mut a, mut b) = (a, b);
        while a <= b && is_blank(bytes, starts, a) {
            a += 1;
        }
        while b >= a && is_blank(bytes, starts, b) {
            b -= 1;
        }
        let mut s = a;
        while s <= b {
            let e = (s + MAX_DOC_CHUNK_LINES - 1).min(b);
            let (sb, eb) = (starts[(s - 1) as usize], starts[e as usize]);
            out.push(DocChunk {
                start_line: s,
                end_line: e,
                start_byte: sb,
                end_byte: eb,
                heading,
                content_hash: hash16(&bytes[sb as usize..eb as usize]),
            });
            s = e + 1;
        }
    }
    out
}

/// The content hash a chunk row stores for its bytes (a snippet read compares it).
pub fn chunk_hash(bytes: &[u8]) -> String {
    hash16(bytes)
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

/// Like `tokenize_into`, but credential-shaped words (see `is_secret_like`) are left out
/// together with their subtokens.
pub fn tokenize_without_secrets(text: &str, out: &mut String) {
    for word in text.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
        if !is_secret_like(word) {
            push_word_tokens(word, out);
        }
    }
}

/// What a snippet shows in place of a credential-shaped word.
pub const SECRET_MASK: &str = "[redacted]";

/// `text` with every credential-shaped word (see `is_secret_like`) replaced by
/// SECRET_MASK: what a snippet line shows, under every retention.
pub fn mask_secret_words(text: &str) -> std::borrow::Cow<'_, str> {
    let mut out = String::new();
    let (mut copied, mut word) = (0, None);
    // a sentinel non-word character closes a word that ends the text
    for (i, c) in text.char_indices().chain([(text.len(), ' ')]) {
        match (c.is_alphanumeric() || c == '_', word) {
            (true, None) => word = Some(i),
            (false, Some(start)) => {
                word = None;
                if is_secret_like(&text[start..i]) {
                    out.push_str(&text[copied..start]);
                    out.push_str(SECRET_MASK);
                    copied = i;
                }
            }
            _ => {}
        }
    }
    match copied {
        0 => text.into(),
        _ => (out + &text[copied..]).into(),
    }
}

/// A heuristic for credential-shaped words in chunk text (not a redaction guarantee;
/// PAR-SEC-04 owns the full pattern set): AWS access key ids, common token prefixes
/// (`ghp_`, `github_pat_`, `xoxb`, `sk_live_`...) and long random-looking runs that mix
/// letters and digits (API keys, hex digests, base64 segments).
pub fn is_secret_like(word: &str) -> bool {
    let b = word.as_bytes();
    if b.len() == 20
        && (word.starts_with("AKIA") || word.starts_with("ASIA"))
        && b.iter()
            .all(|c| c.is_ascii_uppercase() || c.is_ascii_digit())
    {
        return true;
    }
    const PREFIXES: &[&str] = &[
        "ghp_",
        "gho_",
        "ghu_",
        "ghs_",
        "ghr_",
        "github_pat_",
        "xoxb",
        "xoxp",
        "xoxa",
        "xoxs",
        "sk_live_",
        "sk_test_",
        "rk_live_",
        "pk_live_",
        "npm_",
        "pypi_",
        "hf_",
    ];
    if b.len() >= 16 && PREFIXES.iter().any(|p| word.starts_with(p)) {
        return true;
    }
    if b.len() < 20 {
        return false;
    }
    let digits = b.iter().filter(|c| c.is_ascii_digit()).count();
    let letters = b.iter().filter(|c| c.is_ascii_alphabetic()).count();
    if digits < 3 || letters < 3 {
        return false;
    }
    let transitions = b
        .windows(2)
        .filter(|w| w[0].is_ascii_digit() != w[1].is_ascii_digit())
        .count();
    if transitions < 4 {
        return false;
    }
    let mut freq = [0u32; 256];
    for c in b {
        freq[*c as usize] += 1;
    }
    let n = b.len() as f64;
    let entropy: f64 = freq
        .iter()
        .filter(|f| **f > 0)
        .map(|f| {
            let p = *f as f64 / n;
            -p * p.log2()
        })
        .sum();
    entropy >= 3.0
}

/// The postings of a chunk as an unordered bag: the tokens sorted, with repeats kept so
/// BM25 term frequencies and lengths are unchanged. FTS5 still records positions, but
/// they no longer follow the source, so the text cannot be read back from the index.
/// Tokens are sorted as 8-byte offset pairs (a minified one-line chunk holds hundreds of
/// thousands).
pub fn unordered(tokens: &str) -> String {
    let mut spans: Vec<(u32, u32)> = Vec::new();
    let mut start = 0usize;
    for part in tokens.split(' ') {
        if !part.is_empty() {
            spans.push((start as u32, part.len() as u32));
        }
        start += part.len() + 1;
    }
    let word = |(s, l): (u32, u32)| &tokens[s as usize..(s + l) as usize];
    spans.sort_unstable_by(|a, b| word(*a).cmp(word(*b)));
    let mut out = String::with_capacity(tokens.len());
    for sp in spans {
        out.push_str(word(sp));
        out.push(' ');
    }
    out
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
    fn secret_shaped_words_are_recognized() {
        for w in [
            "zq9xk2lmvbp7wr4tnd8hs3fy6gc1ej5a",
            "AKIAIOSFODNN7EXAMPLE",
            "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
            "ce013625030ba8dba906f756967f9e9ca394464a",
        ] {
            assert!(is_secret_like(w), "{w}");
        }
        for w in [
            "parseConfigFileFromDisk2",
            "renderWidget",
            "HTTPServer2Handler",
            "MAX_SYMBOLS_PER_FILE",
            "sha256",
        ] {
            assert!(!is_secret_like(w), "{w}");
        }
        let mut out = String::new();
        tokenize_without_secrets("token = zq9xk2lmvbp7wr4tnd8hs3fy6gc1ej5a; ok", &mut out);
        assert_eq!(out, "token ok ");
        assert_eq!(unordered("b a c a "), "a a b c ");
        assert_eq!(
            mask_secret_words("let t = \"ghp_16C7e42F292c6912E7710c838347Ae178B4a\"; // é"),
            "let t = \"[redacted]\"; // é"
        );
        assert_eq!(
            mask_secret_words("AKIAIOSFODNN7EXAMPLE"),
            SECRET_MASK,
            "a word at both ends of the text"
        );
        assert!(matches!(
            mask_secret_words("fn render_widget() {}"),
            std::borrow::Cow::Borrowed(_)
        ));
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
    fn doc_chunks_follow_headings_outside_fences() {
        let src = "# Title\nintro\n\n## Retry\nbackoff doubles\n```\n# not a heading\n```\n\n## Other\ntext\n";
        let starts = line_starts(src.as_bytes());
        let chunks = plan_doc_chunks(src.as_bytes(), &starts, true);
        let spans: Vec<(u32, u32, Option<u32>)> = chunks
            .iter()
            .map(|c| (c.start_line, c.end_line, c.heading))
            .collect();
        assert_eq!(
            spans,
            vec![(1, 2, Some(1)), (4, 8, Some(4)), (10, 11, Some(10))]
        );
        assert_eq!(
            &src[chunks[1].start_byte as usize..chunks[1].end_byte as usize],
            "## Retry\nbackoff doubles\n```\n# not a heading\n```\n"
        );
        // plain text has no headings: one window per MAX_DOC_CHUNK_LINES lines
        let long: String = (0..90).map(|i| format!("line {i}\n")).collect();
        let starts = line_starts(long.as_bytes());
        let spans: Vec<(u32, u32)> = plan_doc_chunks(long.as_bytes(), &starts, false)
            .iter()
            .map(|c| (c.start_line, c.end_line))
            .collect();
        assert_eq!(spans, vec![(1, 40), (41, 80), (81, 90)]);
        assert!(is_atx_heading(b"### x"));
        assert!(!is_atx_heading(b"#hashtag"));
        assert!(!is_atx_heading(b"    # indented code"));
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
