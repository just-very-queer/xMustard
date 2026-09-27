//! Explainable rerank and snippets (WS-18, PAR-RET-02).
//!
//! Search reranks only its top fused candidates, from their declaration name and the
//! text of the one chunk each hit already names; it never lists or reads anything else.
//! Every feature that moves a hit is reported as a reason:
//!
//! - declaration identifier match: a query word equals the declared name (`exact`), or
//!   equals it once case, `_` and `-` are ignored, as does the whole query run together
//!   (`normalized`: `parse query string` names `parse_query_string`), or the query terms
//!   cover part of the name's camelCase/snake_case subtokens;
//! - window coverage: the most query words found within three consecutive lines, and
//!   whether they appear there in query order;
//! - dense line: a line holding two or more query words;
//! - symbol-like line: a matching line that opens with a declaration keyword.
//!
//! A query word is present on a line when every term the index makes of it is (so
//! `parseConfig` is found in `parse_config(...)`). Snippets are up to three windows of
//! ±1 line around the best matching lines, line-numbered and bounded.

use std::collections::HashSet;

use serde::Serialize;

use super::fts;

/// Lines a coverage window spans.
const WINDOW_LINES: usize = 3;
/// Snippet windows per hit.
pub const MAX_SNIPPETS: usize = 3;
/// Characters kept of one snippet line.
pub const MAX_SNIPPET_LINE_CHARS: usize = 160;

/// Feature weights of the rerank score (at most 1.8).
const W_DECL_EXACT: f64 = 1.0;
const W_DECL_NORMALIZED: f64 = 0.8;
const W_DECL_PARTIAL: f64 = 0.4;
const W_WINDOW: f64 = 0.5;
const W_ORDER: f64 = 0.1;
const W_DENSE: f64 = 0.1;
const W_SYMBOL_LINE: f64 = 0.1;

/// Keywords that open a declaration line in the indexed languages.
const DECL_KEYWORDS: &[&str] = &[
    "fn",
    "func",
    "def",
    "class",
    "struct",
    "interface",
    "type",
    "enum",
    "trait",
    "impl",
    "function",
    "const",
    "let",
    "var",
    "val",
    "pub",
    "export",
    "async",
    "static",
    "public",
    "private",
    "protected",
    "internal",
    "module",
    "namespace",
    "object",
    "record",
    "macro_rules",
];

/// A query as the rerank sees it.
#[derive(Debug, Clone)]
pub struct Query {
    /// The words as typed (whitespace and punctuation other than `_`, `-`, `.` split).
    words: Vec<String>,
    /// The index terms of each word.
    word_terms: Vec<Vec<String>>,
    /// Every term of the query (`fts::query_terms`).
    pub terms: Vec<String>,
    /// The whole query normalized (see [`normalize`]).
    joined: String,
}

/// An identifier without case, `_`, `-`, `.` or other punctuation.
pub fn normalize(ident: &str) -> String {
    ident
        .chars()
        .filter(|c| c.is_alphanumeric())
        .flat_map(char::to_lowercase)
        .collect()
}

impl Query {
    pub fn new(query: &str) -> Query {
        let words: Vec<String> = query
            .split(|c: char| !(c.is_alphanumeric() || matches!(c, '_' | '-' | '.')))
            .map(|w| w.trim_matches(|c| matches!(c, '-' | '.')))
            .filter(|w| !w.is_empty())
            .map(str::to_string)
            .collect();
        let word_terms: Vec<Vec<String>> = words.iter().map(|w| fts::query_terms(w)).collect();
        let (words, word_terms): (Vec<String>, Vec<Vec<String>>) = words
            .into_iter()
            .zip(word_terms)
            .filter(|(_, t)| !t.is_empty())
            .unzip();
        Query {
            terms: fts::query_terms(query),
            joined: normalize(query),
            words,
            word_terms,
        }
    }

    pub fn is_empty(&self) -> bool {
        self.words.is_empty()
    }
}

/// How a declared name matches the query.
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub enum DeclMatch {
    #[default]
    None,
    /// The query terms cover `matched` of the name's `of` subtokens.
    Partial {
        matched: usize,
        of: usize,
    },
    Normalized,
    Exact,
}

impl DeclMatch {
    fn score(self) -> f64 {
        match self {
            DeclMatch::None => 0.0,
            DeclMatch::Partial { matched, of } => W_DECL_PARTIAL * matched as f64 / of as f64,
            DeclMatch::Normalized => W_DECL_NORMALIZED,
            DeclMatch::Exact => W_DECL_EXACT,
        }
    }

    fn reason(self, name: &str) -> Option<String> {
        match self {
            DeclMatch::None => None,
            DeclMatch::Partial { matched, of } => Some(format!(
                "declaration {name} shares {matched}/{of} name parts with the query"
            )),
            DeclMatch::Normalized => Some(format!("normalized identifier match: {name}")),
            DeclMatch::Exact => Some(format!("exact declaration match: {name}")),
        }
    }
}

/// Match a declared `name` against the query.
pub fn decl_match(q: &Query, name: &str) -> DeclMatch {
    if name.is_empty() || q.is_empty() {
        return DeclMatch::None;
    }
    if q.words.iter().any(|w| w == name) {
        return DeclMatch::Exact;
    }
    let norm = normalize(name);
    if !norm.is_empty() && (q.joined == norm || q.words.iter().any(|w| normalize(w) == norm)) {
        return DeclMatch::Normalized;
    }
    let parts = fts::query_terms(name);
    let parts: Vec<&String> = match parts.len() {
        // a one-part name is itself; a compound keeps only its subtokens
        0 | 1 => parts.iter().collect(),
        _ => parts.iter().skip(usize::from(parts[0] == norm)).collect(),
    };
    let matched = parts.iter().filter(|p| q.terms.contains(p)).count();
    match matched {
        0 => DeclMatch::None,
        m => DeclMatch::Partial {
            matched: m,
            of: parts.len(),
        },
    }
}

/// The best coverage window of a chunk.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Window {
    /// First line of the window (1-based file line).
    pub line: u32,
    pub covered: usize,
    pub of: usize,
    pub in_order: bool,
}

/// Rerank features of one candidate.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Features {
    pub decl: DeclMatch,
    pub window: Option<Window>,
    pub dense_line: Option<u32>,
    pub symbol_line: Option<u32>,
}

impl Features {
    pub fn score(&self) -> f64 {
        let window = self.window.map_or(0.0, |w| {
            W_WINDOW * w.covered as f64 / w.of as f64
                + if w.in_order && w.covered > 1 {
                    W_ORDER
                } else {
                    0.0
                }
        });
        self.decl.score()
            + window
            + self.dense_line.map_or(0.0, |_| W_DENSE)
            + self.symbol_line.map_or(0.0, |_| W_SYMBOL_LINE)
    }

    /// The reasons these features give, strongest first.
    pub fn reasons(&self, decl_name: &str, out: &mut Vec<String>) {
        out.extend(self.decl.reason(decl_name));
        if let Some(w) = self.window {
            let order = match (w.in_order, w.covered > 1) {
                (true, true) => ", in query order",
                _ => "",
            };
            out.push(format!(
                "window coverage {}/{} query words at lines {}-{}{order}",
                w.covered,
                w.of,
                w.line,
                w.line as usize + WINDOW_LINES - 1
            ));
        }
        out.extend(self.dense_line.map(|l| format!("dense line {l}")));
        out.extend(self.symbol_line.map(|l| format!("symbol-like line {l}")));
    }
}

/// One line of a chunk: its query-word hits and their first positions.
struct LineHits {
    /// Bit i: query word i is present.
    words: u64,
    /// First token position of each present word (query order), for the order check.
    first: Vec<(usize, usize)>,
    declares: bool,
}

fn line_hits(q: &Query, line: &str) -> LineHits {
    let mut raw = String::new();
    super::chunks::tokenize_into(line, &mut raw);
    let tokens: Vec<&str> = raw.split([' ', '_']).filter(|t| !t.is_empty()).collect();
    let set: HashSet<&str> = tokens.iter().copied().collect();
    let mut words = 0u64;
    let mut first = Vec::new();
    for (i, terms) in q.word_terms.iter().enumerate().take(64) {
        if terms.iter().all(|t| set.contains(t.as_str())) {
            words |= 1 << i;
            let pos = tokens
                .iter()
                .position(|t| terms.iter().any(|w| w == t))
                .unwrap_or(0);
            first.push((i, pos));
        }
    }
    let lead = line
        .split(|c: char| !(c.is_alphanumeric() || c == '_'))
        .find(|w| !w.is_empty())
        .unwrap_or("");
    LineHits {
        words,
        first,
        declares: DECL_KEYWORDS.contains(&lead),
    }
}

/// Text features and the snippet anchors of a chunk starting at file line `first`.
pub fn text_features(q: &Query, text: &str, first: u32) -> (Features, Vec<u32>) {
    let lines: Vec<&str> = text.lines().collect();
    let hits: Vec<LineHits> = lines.iter().map(|l| line_hits(q, l)).collect();
    let of = q.words.len().min(64);
    let mut f = Features::default();
    if of == 0 {
        return (f, Vec::new());
    }
    let mut best: Option<Window> = None;
    for start in 0..hits.len() {
        let span = &hits[start..(start + WINDOW_LINES).min(hits.len())];
        let covered = span.iter().fold(0u64, |a, h| a | h.words).count_ones() as usize;
        if covered == 0 || best.is_some_and(|b| b.covered >= covered) {
            continue;
        }
        // query order: first position of each word in the window, by (line, token)
        let mut firsts: Vec<(usize, (usize, usize))> = Vec::new();
        for (dl, h) in span.iter().enumerate() {
            for &(w, pos) in &h.first {
                if !firsts.iter().any(|(fw, _)| *fw == w) {
                    firsts.push((w, (dl, pos)));
                }
            }
        }
        firsts.sort_by_key(|(w, _)| *w);
        let in_order = firsts.windows(2).all(|p| p[0].1 <= p[1].1);
        best = Some(Window {
            line: first + start as u32,
            covered,
            of,
            in_order,
        });
    }
    f.window = best;
    let line_no = |i: usize| first + i as u32;
    f.dense_line = hits
        .iter()
        .position(|h| of > 1 && h.words.count_ones() >= 2)
        .map(line_no);
    f.symbol_line = hits
        .iter()
        .position(|h| h.declares && h.words != 0)
        .map(line_no);
    // snippet anchors: the lines with the most query words, then declaring lines,
    // earliest first; windows (±1 line) never overlap.
    let mut order: Vec<usize> = (0..hits.len()).filter(|&i| hits[i].words != 0).collect();
    order.sort_by_key(|&i| {
        (
            std::cmp::Reverse(hits[i].words.count_ones()),
            !hits[i].declares,
            i,
        )
    });
    let mut anchors: Vec<u32> = Vec::new();
    for i in order {
        if anchors.len() == MAX_SNIPPETS {
            break;
        }
        let l = line_no(i);
        if anchors.iter().all(|a| a.abs_diff(l) > 2) {
            anchors.push(l);
        }
    }
    anchors.sort_unstable();
    (f, anchors)
}

/// One snippet window: the anchor line and its ±1 lines, each numbered.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct Snippet {
    pub line: u32,
    pub text: String,
}

/// A snippet line: credential-shaped words masked (`chunks::mask_secret_words`), then
/// clipped to MAX_SNIPPET_LINE_CHARS.
fn snippet_line(line: &str) -> String {
    let line = super::chunks::mask_secret_words(line);
    match line.char_indices().nth(MAX_SNIPPET_LINE_CHARS) {
        Some((cut, _)) => format!("{}…", &line[..cut]),
        None => line.to_string(),
    }
}

/// Render up to MAX_SNIPPETS windows of `text` (a chunk starting at file line `first`)
/// around `anchors`, clipped to the chunk, with credential-shaped words masked.
pub fn snippets(text: &str, first: u32, anchors: &[u32]) -> Vec<Snippet> {
    let lines: Vec<&str> = text.lines().collect();
    let last = first + lines.len() as u32;
    anchors
        .iter()
        .take(MAX_SNIPPETS)
        .filter(|a| (first..last).contains(a))
        .map(|&a| {
            let (lo, hi) = (a.saturating_sub(1).max(first), (a + 1).min(last - 1));
            let text = (lo..=hi)
                .map(|l| format!("{l}: {}", snippet_line(lines[(l - first) as usize])))
                .collect::<Vec<_>>()
                .join("\n");
            Snippet { line: a, text }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn declaration_matches_are_graded() {
        let q = Query::new("ValidateSession session token");
        assert_eq!(decl_match(&q, "ValidateSession"), DeclMatch::Exact);
        assert_eq!(
            decl_match(&Query::new("parse query string"), "parse_query_string"),
            DeclMatch::Normalized
        );
        assert_eq!(
            decl_match(&Query::new("parseQueryString"), "parse_query_string"),
            DeclMatch::Normalized
        );
        assert_eq!(
            decl_match(&Query::new("retry backoff"), "RetryWithBackoff"),
            DeclMatch::Partial { matched: 2, of: 3 }
        );
        assert_eq!(decl_match(&q, "Unrelated"), DeclMatch::None);
        assert!(DeclMatch::Exact.score() > DeclMatch::Normalized.score());
    }

    #[test]
    fn window_coverage_order_and_snippets() {
        let q = Query::new("sleep delay doubling");
        let text = "func RetryWithBackoff() {\n\tfor {\n\t\ttime.Sleep(delay)\n\t\tdelay *= 2 // doubling\n\t}\n}\n";
        let (f, anchors) = text_features(&q, text, 10);
        let w = f.window.unwrap();
        assert_eq!((w.covered, w.of), (3, 3));
        assert!(w.in_order);
        assert_eq!(w.line, 11);
        assert_eq!(f.dense_line, Some(12));
        assert_eq!(f.symbol_line, None);
        assert_eq!(anchors, vec![12]);
        let s = snippets(text, 10, &anchors);
        assert_eq!(s.len(), 1);
        assert_eq!(
            s[0].text,
            "11: \tfor {\n12: \t\ttime.Sleep(delay)\n13: \t\tdelay *= 2 // doubling"
        );
        let mut reasons = Vec::new();
        f.reasons("RetryWithBackoff", &mut reasons);
        assert_eq!(
            reasons,
            [
                "window coverage 3/3 query words at lines 11-13, in query order",
                "dense line 12"
            ]
        );
    }

    #[test]
    fn snippets_are_bounded_and_do_not_overlap() {
        let q = Query::new("alpha");
        let text: String = (0..20)
            .map(|i| format!("let alpha{i} = alpha; {}\n", "x".repeat(300)))
            .collect();
        let (_, anchors) = text_features(&q, &text, 1);
        assert_eq!(anchors.len(), MAX_SNIPPETS);
        let s = snippets(&text, 1, &anchors);
        assert!(s.len() <= MAX_SNIPPETS);
        for w in &s {
            assert!(w.text.lines().count() <= 3);
            for l in w.text.lines() {
                assert!(l.chars().count() <= MAX_SNIPPET_LINE_CHARS + 8, "{l}");
            }
        }
        for p in anchors.windows(2) {
            assert!(p[1] - p[0] > 2);
        }
    }
}
