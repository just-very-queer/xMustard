//! The text lanes' query side (WS-18, PAR-RET-01): BM25 over the code chunks
//! (`chunk_fts`: path, declared names and body words, doc comments included) and the doc
//! chunks (`doc_fts`) of `index.db`, and bounded reads of the chunk text that the rerank
//! and the snippets use.
//!
//! Queries go through one read-only connection per snapshot (`reader::Snapshot::
//! with_text`): no mmap, so every page is a `pread` through SQLite's page cache of
//! `TEXT_CACHE_KIB`. A query runs AND over its terms first and falls back to OR only
//! when AND finds fewer than the lane wants (D-02: AND is ~10x cheaper than OR in
//! FTS5). Chunk text is never stored under the default retention, so the rerank and
//! the snippets read a hit's byte range from the file, once, and use it only when its
//! hash still equals the indexed chunk's; nothing here lists or rereads the repository.
//! A secret path (`secretpath`) is refused before any read: its hits carry no text.

use std::cell::Cell;
use std::path::Path;

use rusqlite::{Connection, OptionalExtension, params};

use super::chunks;

/// Page cache of the text connection, in KiB. Part of the BM25 cache budget.
pub const TEXT_CACHE_KIB: i64 = 2048;
/// Query terms kept (longer queries lose their tail).
pub const MAX_TERMS: usize = 12;
/// Longest chunk read for the rerank and snippets; larger chunks get neither.
pub const MAX_CHUNK_READ: u32 = 64 << 10;

/// BM25 column weights of `chunk_fts(path, symbols, body)`: names declared in a chunk
/// count double.
const CODE_WEIGHTS: &str = "bm25(1.0, 2.0, 1.0)";
/// BM25 column weights of `doc_fts(path, heading, body)`.
const DOC_WEIGHTS: &str = "bm25(0.5, 2.0, 1.0)";

thread_local! {
    /// Chunk-range reads (file opens) made on this thread; see [`chunk_reads`].
    static CHUNK_READS: Cell<u64> = const { Cell::new(0) };
}

/// File range reads the text lanes made on the calling thread so far (tests measure a
/// query's reads as the difference around it).
pub fn chunk_reads() -> u64 {
    CHUNK_READS.with(Cell::get)
}

/// Every term of `query` as the index tokenized chunk text: every alphanumeric run,
/// lowercased, plus the camelCase and snake_case subtokens of each word, without
/// repeats, in query order. One-character terms are dropped.
fn all_terms(query: &str) -> Vec<String> {
    let mut raw = String::new();
    chunks::tokenize_into(query, &mut raw);
    let mut out: Vec<String> = Vec::new();
    for t in raw.split([' ', '_']) {
        if t.chars().count() >= 2 && !out.iter().any(|o| o == t) {
            out.push(t.to_string());
        }
    }
    out
}

/// The first MAX_TERMS terms of `query` (see [`all_terms`]): what the lanes search.
pub fn query_terms(query: &str) -> Vec<String> {
    let mut out = all_terms(query);
    out.truncate(MAX_TERMS);
    out
}

/// Terms of `query` past MAX_TERMS, which the lanes do not search.
pub fn terms_dropped(query: &str) -> usize {
    all_terms(query).len().saturating_sub(MAX_TERMS)
}

/// An FTS5 query joining quoted `terms` with `op` (` AND ` or ` OR `). Terms are
/// alphanumeric, so quoting only keeps words like `and` from reading as operators.
fn match_expr(terms: &[String], op: &str) -> String {
    terms
        .iter()
        .map(|t| format!("\"{t}\""))
        .collect::<Vec<_>>()
        .join(op)
}

/// A superset of `glob` (gitignore semantics: `*` and `?` stop at `/`, `**` crosses
/// it) in SQLite GLOB syntax, where `*` crosses `/`: the SQL filter narrows the rows and
/// the exact match runs on what it returns. Every wildcard run becomes `*` and swallows a
/// following `/` (so `a/**/b` also admits `a/b`); a class or an escaped character
/// becomes `?`.
pub fn sqlite_glob_superset(glob: &str) -> String {
    let mut out = String::new();
    let mut chars = glob.chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            '*' => {
                while chars.next_if(|n| *n == '*').is_some() {}
                chars.next_if(|n| *n == '/');
                if !out.ends_with('*') {
                    out.push('*');
                }
            }
            '\\' => {
                chars.next();
                out.push('?');
            }
            '[' => {
                // a class matches one character: skip to its closing `]` (a `]` first
                // in the class is literal); an unterminated `[` is one character too.
                let rest: Vec<char> = chars.clone().collect();
                let first = usize::from(matches!(rest.first(), Some('!' | '^')));
                let first = first + usize::from(rest.get(first) == Some(&']'));
                if let Some(end) = rest.iter().skip(first).position(|c| *c == ']') {
                    for _ in 0..=first + end {
                        chars.next();
                    }
                }
                out.push('?');
            }
            c => out.push(c),
        }
    }
    out
}

/// The declaration a chunk belongs to.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Decl {
    pub uid: String,
    pub name: String,
    pub kind: String,
    pub name_line: u32,
    pub start_line: u32,
    pub end_line: u32,
}

/// A chunk's location and identity: enough to read and verify its text.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChunkLoc {
    /// `chunks.id` for code, `doc_chunks.id` for docs.
    pub id: i64,
    pub doc: bool,
    pub path: String,
    pub start_line: u32,
    pub end_line: u32,
    pub start_byte: u32,
    pub end_byte: u32,
    pub content_hash: String,
}

/// One BM25 row of a text lane.
#[derive(Debug, Clone)]
pub struct TextHit {
    pub loc: ChunkLoc,
    /// Code: `symbol` | `window` | `gap`; docs: `doc` | `guide` (the file's role).
    pub kind: String,
    /// Code: the file's role (`code` | `test`).
    pub role: String,
    pub decl: Option<Decl>,
    /// -bm25: higher is better.
    pub bm25: f64,
    /// Every query term matched (the AND query found it).
    pub all_terms: bool,
}

/// The text lanes of one query and what degraded them.
#[derive(Debug, Default)]
pub struct TextLanes {
    pub code: Vec<TextHit>,
    pub docs: Vec<TextHit>,
    /// The index generation the rows were read from.
    pub generation: i64,
}

const CODE_SQL: &str = "
SELECT c.id, f.path, f.role, c.kind, c.start_line, c.end_line, c.start_byte, c.end_byte,
       c.content_hash, s.uid, n.name, s.kind, s.name_line, s.start_line, s.end_line, chunk_fts.rank
FROM chunk_fts
JOIN chunks c ON c.id = chunk_fts.rowid
JOIN files f ON f.id = c.file_id
LEFT JOIN symbols s ON s.id = c.symbol_id
LEFT JOIN names n ON n.id = s.name_id
WHERE chunk_fts MATCH ?1 AND chunk_fts.rank MATCH ?2 AND f.path GLOB ?3
ORDER BY chunk_fts.rank, c.id LIMIT ?4";

const DOC_SQL: &str = "
SELECT c.id, d.path, d.role, 'doc', c.start_line, c.end_line, c.start_byte, c.end_byte,
       c.content_hash, NULL, NULL, NULL, NULL, NULL, NULL, doc_fts.rank
FROM doc_fts
JOIN doc_chunks c ON c.id = doc_fts.rowid
JOIN docs d ON d.id = c.doc_id
WHERE doc_fts MATCH ?1 AND doc_fts.rank MATCH ?2 AND d.path GLOB ?3
ORDER BY doc_fts.rank, c.id LIMIT ?4";

/// Which lane a statement serves.
#[derive(Clone, Copy)]
struct Lane {
    sql: &'static str,
    weights: &'static str,
    doc: bool,
}

const CODE: Lane = Lane {
    sql: CODE_SQL,
    weights: CODE_WEIGHTS,
    doc: false,
};
const DOCS: Lane = Lane {
    sql: DOC_SQL,
    weights: DOC_WEIGHTS,
    doc: true,
};

fn run(
    conn: &Connection,
    lane: Lane,
    expr: &str,
    glob: &str,
    k: usize,
    all_terms: bool,
) -> rusqlite::Result<Vec<TextHit>> {
    let mut st = conn.prepare_cached(lane.sql)?;
    let rows = st.query_map(params![expr, lane.weights, glob, k as i64], |r| {
        let uid: Option<String> = r.get(9)?;
        let decl = match uid {
            Some(uid) => Some(Decl {
                uid,
                name: r.get(10)?,
                kind: r.get(11)?,
                name_line: r.get(12)?,
                start_line: r.get(13)?,
                end_line: r.get(14)?,
            }),
            None => None,
        };
        let role: String = r.get(2)?;
        let kind: String = r.get(3)?;
        Ok(TextHit {
            loc: ChunkLoc {
                id: r.get(0)?,
                doc: lane.doc,
                path: r.get(1)?,
                start_line: r.get(4)?,
                end_line: r.get(5)?,
                start_byte: r.get(6)?,
                end_byte: r.get(7)?,
                content_hash: r.get(8)?,
            },
            kind: if lane.doc { role.clone() } else { kind },
            role,
            decl,
            bm25: -r.get::<_, f64>(15)?,
            all_terms,
        })
    })?;
    rows.collect()
}

/// The best `k` chunks of a lane: AND over every term, then OR for the rest.
fn lane_hits(
    conn: &Connection,
    lane: Lane,
    terms: &[String],
    glob: &str,
    k: usize,
) -> rusqlite::Result<Vec<TextHit>> {
    if terms.is_empty() || k == 0 {
        return Ok(Vec::new());
    }
    let mut hits = run(conn, lane, &match_expr(terms, " AND "), glob, k, true)?;
    if hits.len() < k && terms.len() > 1 {
        let seen: std::collections::HashSet<i64> = hits.iter().map(|h| h.loc.id).collect();
        let more = run(conn, lane, &match_expr(terms, " OR "), glob, k, false)?;
        hits.extend(more.into_iter().filter(|h| !seen.contains(&h.loc.id)));
        hits.truncate(k);
    }
    Ok(hits)
}

/// Both text lanes of one query, read in one transaction.
pub fn lanes(
    conn: &Connection,
    terms: &[String],
    path_glob: Option<&str>,
    code_k: usize,
    doc_k: usize,
) -> rusqlite::Result<TextLanes> {
    let glob = path_glob.map_or_else(|| "*".to_string(), sqlite_glob_superset);
    conn.execute_batch("BEGIN")?;
    let read = || -> rusqlite::Result<TextLanes> {
        let generation: i64 = conn
            .query_row("SELECT value FROM meta WHERE key = 'generation'", [], |r| {
                r.get::<_, String>(0)
            })
            .optional()?
            .and_then(|g| g.parse().ok())
            .unwrap_or(0);
        Ok(TextLanes {
            code: lane_hits(conn, CODE, terms, &glob, code_k)?,
            docs: lane_hits(conn, DOCS, terms, &glob, doc_k)?,
            generation,
        })
    };
    let out = read();
    conn.execute_batch("COMMIT")?;
    out
}

/// The declaration named `name` at `name_line` of `path` and the chunk holding that
/// line: what a name-lane hit needs for its UID, span and text.
pub fn locate(
    conn: &Connection,
    path: &str,
    name: &str,
    name_line: u32,
) -> rusqlite::Result<Option<(Decl, Option<ChunkLoc>)>> {
    conn.prepare_cached(
        "SELECT s.uid, n.name, s.kind, s.name_line, s.start_line, s.end_line,
                c.id, c.start_line, c.end_line, c.start_byte, c.end_byte, c.content_hash
         FROM files f
         JOIN symbols s ON s.file_id = f.id
         JOIN names n ON n.id = s.name_id
         LEFT JOIN chunks c ON c.file_id = f.id AND c.start_line <= s.name_line
                            AND s.name_line <= c.end_line
         WHERE f.path = ?1 AND s.name_line = ?2 AND n.name = ?3
         ORDER BY s.local, s.start_line, c.start_line LIMIT 1",
    )?
    .query_row(params![path, name_line, name], |r| {
        let decl = Decl {
            uid: r.get(0)?,
            name: r.get(1)?,
            kind: r.get(2)?,
            name_line: r.get(3)?,
            start_line: r.get(4)?,
            end_line: r.get(5)?,
        };
        let loc = match r.get::<_, Option<i64>>(6)? {
            Some(id) => Some(ChunkLoc {
                id,
                doc: false,
                path: path.to_string(),
                start_line: r.get(7)?,
                end_line: r.get(8)?,
                start_byte: r.get(9)?,
                end_byte: r.get(10)?,
                content_hash: r.get(11)?,
            }),
            None => None,
        };
        Ok((decl, loc))
    })
    .optional()
}

/// Why a chunk's text is not available.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TextMiss {
    /// The file is a credential store (`secretpath`): its text never reaches an agent.
    SecretPath,
    /// Larger than MAX_CHUNK_READ.
    TooLarge,
    /// The file is gone, unreadable, or its bytes no longer hash to the indexed chunk.
    Changed,
}

impl TextMiss {
    pub fn reason(self) -> &'static str {
        match self {
            TextMiss::SecretPath => "secret path",
            TextMiss::TooLarge => "chunk larger than the snippet read bound",
            TextMiss::Changed => "file changed since it was indexed",
        }
    }
}

/// The text of `loc`, which the rerank reads and the snippets show. A secret path is
/// refused before anything is read. Under `content_retention=full` the stored text
/// answers while the file's stat key still equals the indexed one; otherwise the byte
/// range is read from the file (no symlink followed) and used only when it still hashes
/// to the indexed chunk.
pub fn chunk_text(conn: &Connection, root: &Path, loc: &ChunkLoc) -> Result<String, TextMiss> {
    if crate::secretpath::is_secret_path(&loc.path) {
        return Err(TextMiss::SecretPath);
    }
    if let Some(t) = stored_text(conn, root, loc) {
        return Ok(t);
    }
    let len = loc.end_byte.saturating_sub(loc.start_byte);
    if len > MAX_CHUNK_READ {
        return Err(TextMiss::TooLarge);
    }
    CHUNK_READS.with(|c| c.set(c.get() + 1));
    let bytes = read_range(root, &loc.path, loc.start_byte as u64, len as usize)
        .map_err(|_| TextMiss::Changed)?;
    if chunks::chunk_hash(&bytes) != loc.content_hash {
        return Err(TextMiss::Changed);
    }
    Ok(String::from_utf8_lossy(&bytes).into_owned())
}

/// The stored text of code chunk `loc` (`content_retention=full`) while its file is
/// unchanged: the file's current stat key equals the indexed key, and that key was not
/// racy. None sends the caller to the file.
fn stored_text(conn: &Connection, root: &Path, loc: &ChunkLoc) -> Option<String> {
    if loc.doc {
        return None;
    }
    let (key, text): (String, String) = conn
        .prepare_cached(
            "SELECT f.stat_key, t.text FROM chunk_text t
             JOIN chunks c ON c.id = t.chunk_id JOIN files f ON f.id = c.file_id
             WHERE t.chunk_id = ?1",
        )
        .and_then(|mut st| {
            st.query_row([loc.id], |r| Ok((r.get(0)?, r.get(1)?)))
                .optional()
        })
        .ok()??;
    let meta = std::fs::symlink_metadata(root.join(&loc.path)).ok()?;
    let current = meta.is_file() && super::scan::StatKey::of(&meta).encode() == key;
    (current && !key.starts_with(super::RACY_PREFIX)).then_some(text)
}

#[cfg(unix)]
fn read_range(root: &Path, rel: &str, offset: u64, len: usize) -> std::io::Result<Vec<u8>> {
    use std::os::unix::fs::FileExt;
    let (f, _) = crate::symbolgraph::open_repo_regular_file_with_meta(root, rel)?;
    let mut buf = vec![0u8; len];
    f.read_exact_at(&mut buf, offset)?;
    Ok(buf)
}

#[cfg(not(unix))]
fn read_range(root: &Path, rel: &str, offset: u64, len: usize) -> std::io::Result<Vec<u8>> {
    use std::io::{Read, Seek, SeekFrom};
    let (mut f, _) = crate::symbolgraph::open_repo_regular_file_with_meta(root, rel)?;
    f.seek(SeekFrom::Start(offset))?;
    let mut buf = vec![0u8; len];
    f.read_exact(&mut buf)?;
    Ok(buf)
}

/// Open the read-only text connection of the store at `db`.
pub fn open(db: &Path) -> rusqlite::Result<Connection> {
    let conn = Connection::open_with_flags(db, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)?;
    conn.execute_batch(&format!(
        "PRAGMA cache_size=-{TEXT_CACHE_KIB}; PRAGMA mmap_size=0; PRAGMA query_only=1;"
    ))?;
    conn.busy_timeout(std::time::Duration::from_secs(2))?;
    Ok(conn)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::index::ignore::glob_match;

    #[test]
    fn query_terms_match_the_index_tokenizer() {
        assert_eq!(
            query_terms("ValidateSession session token"),
            ["validatesession", "validate", "session", "token"]
        );
        assert_eq!(
            query_terms("parse_query_string"),
            ["parse", "query", "string"]
        );
        assert_eq!(query_terms("a b"), Vec::<String>::new());
        assert_eq!(
            match_expr(&query_terms("and or"), " AND "),
            "\"and\" AND \"or\""
        );
        let long: String = (b'a'..=b't')
            .map(|c| format!("x{0}{0} ", c as char))
            .collect();
        assert_eq!(
            (query_terms(&long).len(), terms_dropped(&long)),
            (MAX_TERMS, 8)
        );
    }

    #[test]
    fn sqlite_glob_is_a_superset_of_the_gitignore_glob() {
        let cases = [
            ("src/**/*.rs", "src/main.rs"),
            ("src/**/*.rs", "src/a/b/main.rs"),
            ("**/*_test.go", "a/b_test.go"),
            ("**/*_test.go", "b_test.go"),
            ("*.md", "README.md"),
            ("file[!0-9].txt", "filex.txt"),
            ("a/**/b", "a/b"),
            ("\\*x", "*x"),
        ];
        for (g, path) in cases {
            assert!(glob_match(g.as_bytes(), path.as_bytes()), "{g} {path}");
            let sql = sqlite_glob_superset(g);
            let conn = Connection::open_in_memory().unwrap();
            let ok: bool = conn
                .query_row("SELECT ?1 GLOB ?2", params![path, sql], |r| r.get(0))
                .unwrap();
            assert!(ok, "{g} -> {sql} must admit {path}");
        }
    }
}
