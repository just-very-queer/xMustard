//! Index meta keys, the coverage report and the content digest.
//!
//! Meta keys: `schema_version`, `schema_fingerprint`, `analyzer_version`,
//! `content_retention`, `config`, `incremental_in_progress` (the crash dirty flag),
//! `last_commit`, `repo_mode`, `root`, `ignored_files`, `invalid_paths`,
//! `worktree_deleted`, `ignore_rules_dropped`, `envelope_beyond` (JSON: count per loss
//! reason of eligible files that have no row because they are past the envelope) and
//! `envelope_beyond_sample` (JSON: the first of them by path), `generation` (monotonic
//! per store directory, across full rebuilds too), `graph_segment`, `identity_key`, `coverage`
//! (JSON, as of the last build or update) and `last_run` (that run's counters, JSON).

use std::collections::BTreeMap;

use rusqlite::{Connection, OptionalExtension, params};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use super::config::IndexConfig;
use super::lang::{LanguageCoverage, Support};

pub const DIRTY_FLAG: &str = "incremental_in_progress";

/// `files.parse_status` values for files whose content was indexed.
pub const INDEXED_STATUSES: &[&str] = &["tree_sitter", "regex", "none"];

/// Losses listed by path in the report (counts stay exact).
pub const MAX_LOSS_ENTRIES: usize = 200;

/// Meta key: count per reason of eligible files past the envelope (no `files` row).
pub const BEYOND: &str = "envelope_beyond";
/// Meta key: the first of those files by path, `[[path, reason], ...]`.
pub const BEYOND_SAMPLE: &str = "envelope_beyond_sample";
/// Meta key: the file name of this generation's graph segment (`csr`), written by the
/// index writer before the generation commits.
pub const GRAPH_SEGMENT: &str = "graph_segment";
/// Meta key: the repository identity key (`repo-key`) the orchestrator observed when it
/// ran the last update (`index update --identity-key`); readers report it as the
/// identity their answers reflect.
pub const IDENTITY_KEY: &str = "identity_key";

pub fn get(conn: &Connection, key: &str) -> rusqlite::Result<Option<String>> {
    conn.query_row("SELECT value FROM meta WHERE key = ?1", [key], |r| r.get(0))
        .optional()
}

pub fn set(conn: &Connection, key: &str, value: &str) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO meta(key, value) VALUES (?1, ?2)
         ON CONFLICT(key) DO UPDATE SET value = excluded.value",
        params![key, value],
    )?;
    Ok(())
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
pub struct Loss {
    pub path: String,
    /// `oversized` | `unreadable` | `symlink` | `not_regular` | `missing` |
    /// `envelope_files` | `envelope_bytes` | `symbol_budget` | `symbols_truncated` |
    /// `nesting_truncated` | `parse_errors` | `invalid_utf8` | `lexical_fallback` |
    /// `ignore_budget` (the ignore rules could not decide the file within the scan's
    /// matching budget)
    pub reason: String,
    /// Whether the file's content still contributed rows (partial, not absent).
    pub content_indexed: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
#[serde(default)]
pub struct Envelope {
    pub max_files: usize,
    pub max_symbols: usize,
    pub max_total_bytes: u64,
    pub max_file_size: u64,
    /// Which bounds cut this index (`files`, `bytes`, `symbols`); empty when inside.
    pub exceeded: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq, Eq)]
#[serde(default)]
pub struct Coverage {
    /// Source files the ignore rules admit (those past the envelope have no row).
    pub eligible_files: usize,
    /// Eligible files whose content was read and extracted.
    pub indexed_files: usize,
    pub ignored_files: usize,
    /// Ignore-file lines and files skipped by the matcher's bounds (too long, too many
    /// rules or pattern bytes, an ignore file above 64 KiB).
    pub ignore_rules_dropped: usize,
    pub invalid_paths: usize,
    /// Tracked files deleted from the worktree (not eligible, not a loss).
    pub worktree_deleted_files: usize,
    pub indexed_bytes: u64,
    /// Indexed files per language.
    pub languages: BTreeMap<String, usize>,
    /// Indexed files per language by support: `supported` (grammar), `unsupported` (no
    /// grammar in this build) and `failed` (lexical fallback or parse errors).
    pub language_support: BTreeMap<String, LanguageCoverage>,
    /// Indexed files per extraction engine (`tree_sitter`, `regex`, `none`).
    pub extraction: BTreeMap<String, usize>,
    /// Exact count per loss reason.
    pub loss_counts: BTreeMap<String, usize>,
    /// The first MAX_LOSS_ENTRIES losses by path.
    pub losses: Vec<Loss>,
    pub losses_truncated: bool,
    /// No losses of any kind.
    pub complete: bool,
    pub symbols: usize,
    pub references: usize,
    pub imports: usize,
    pub imports_resolved: usize,
    pub chunks: usize,
    pub edges: usize,
    pub envelope: Envelope,
}

fn count(conn: &Connection, sql: &str) -> rusqlite::Result<usize> {
    conn.query_row(sql, [], |r| r.get::<_, i64>(0))
        .map(|n| n as usize)
}

pub fn coverage(conn: &Connection, cfg: &IndexConfig) -> rusqlite::Result<Coverage> {
    use super::facts::file_flag;
    let mut c = Coverage::default();
    // Losses are counted exactly but only the first MAX_LOSS_ENTRIES by path are kept:
    // rows arrive in path order, and the sample of files past the envelope is sorted.
    let mut losses: Vec<Loss> = Vec::new();
    let mut total_losses = 0usize;
    let mut note = |c: &mut Coverage, losses: &mut Vec<Loss>, l: Loss| {
        *c.loss_counts.entry(l.reason.clone()).or_default() += 1;
        total_losses += 1;
        if losses.len() < MAX_LOSS_ENTRIES {
            losses.push(l);
        }
    };
    {
        let mut st =
            conn.prepare("SELECT path, lang, parse_status, flags, size FROM files ORDER BY path")?;
        let mut rows = st.query([])?;
        while let Some(r) = rows.next()? {
            c.eligible_files += 1;
            let path: String = r.get(0)?;
            let lang: String = r.get(1)?;
            let status: String = r.get(2)?;
            let flags: i64 = r.get(3)?;
            let size: i64 = r.get(4)?;
            let indexed = INDEXED_STATUSES.contains(&status.as_str());
            if indexed {
                c.indexed_files += 1;
                c.indexed_bytes += size.max(0) as u64;
                let fell_back =
                    flags & (file_flag::LEXICAL_FALLBACK | file_flag::PARSE_ERRORS) != 0;
                let support = match (fell_back, status.as_str()) {
                    (true, _) => Support::Failed,
                    (false, "tree_sitter") => Support::Supported,
                    (false, _) => Support::Unsupported,
                };
                c.language_support
                    .entry(lang.clone())
                    .or_default()
                    .add(support);
                *c.languages.entry(lang).or_default() += 1;
                *c.extraction.entry(status.clone()).or_default() += 1;
            } else {
                note(
                    &mut c,
                    &mut losses,
                    Loss {
                        path: path.clone(),
                        reason: status.clone(),
                        content_indexed: false,
                    },
                );
            }
            for (bit, reason) in [
                (file_flag::INVALID_UTF8, "invalid_utf8"),
                (file_flag::SYMBOLS_TRUNCATED, "symbols_truncated"),
                (file_flag::NESTING_TRUNCATED, "nesting_truncated"),
                (file_flag::PARSE_ERRORS, "parse_errors"),
                (file_flag::SYMBOL_BUDGET, "symbol_budget"),
                (file_flag::LEXICAL_FALLBACK, "lexical_fallback"),
            ] {
                if flags & bit != 0 {
                    note(
                        &mut c,
                        &mut losses,
                        Loss {
                            path: path.clone(),
                            reason: reason.into(),
                            content_indexed: true,
                        },
                    );
                }
            }
        }
    }
    let beyond: BTreeMap<String, usize> = get(conn, BEYOND)?
        .and_then(|v| serde_json::from_str(&v).ok())
        .unwrap_or_default();
    let sample: Vec<(String, String)> = get(conn, BEYOND_SAMPLE)?
        .and_then(|v| serde_json::from_str(&v).ok())
        .unwrap_or_default();
    for (reason, n) in &beyond {
        c.eligible_files += n;
        *c.loss_counts.entry(reason.clone()).or_default() += n;
        total_losses += n;
    }
    losses.extend(sample.into_iter().map(|(path, reason)| Loss {
        path,
        reason,
        content_indexed: false,
    }));
    losses.sort_by(|a, b| a.path.cmp(&b.path));
    losses.truncate(MAX_LOSS_ENTRIES);
    c.losses_truncated = total_losses > losses.len();
    c.losses = losses;
    c.complete = c.loss_counts.is_empty();
    c.symbols = count(conn, "SELECT count(*) FROM symbols")?;
    c.references = count(conn, "SELECT count(*) FROM refs")?;
    c.imports = count(conn, "SELECT count(*) FROM imports")?;
    c.imports_resolved = count(
        conn,
        "SELECT count(*) FROM imports WHERE resolved_file_id IS NOT NULL",
    )?;
    c.chunks = count(conn, "SELECT count(*) FROM chunks")?;
    c.edges = count(conn, "SELECT count(*) FROM edges")?;
    c.ignored_files = get(conn, "ignored_files")?
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    c.ignore_rules_dropped = get(conn, "ignore_rules_dropped")?
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    c.invalid_paths = get(conn, "invalid_paths")?
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    c.worktree_deleted_files = get(conn, "worktree_deleted")?
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    let mut exceeded = Vec::new();
    if c.loss_counts.contains_key("envelope_files") {
        exceeded.push("files".to_string());
    }
    if c.loss_counts.contains_key("envelope_bytes") {
        exceeded.push("bytes".to_string());
    }
    if c.loss_counts.contains_key("symbol_budget") {
        exceeded.push("symbols".to_string());
    }
    c.envelope = Envelope {
        max_files: cfg.max_files,
        max_symbols: cfg.max_symbols,
        max_total_bytes: cfg.max_total_bytes,
        max_file_size: cfg.max_file_size,
        exceeded,
    };
    Ok(c)
}

/// SHA-256 over the index content in a rowid-independent order: files by path, symbols
/// by UID, references, imports, chunks, chunk text, edges (by paths), docs and their
/// chunks, the FTS5 term statistics of both tables, and the stable meta keys. Two
/// indexes of the same tree built by any sequence of full builds and incremental
/// updates have the same digest. Timestamps, mtimes, counters and the fact cache are
/// excluded.
pub fn content_digest(conn: &Connection) -> rusqlite::Result<String> {
    let mut h = Sha256::new();
    let feed = |sql: &str, h: &mut Sha256| -> rusqlite::Result<()> {
        h.update(sql.as_bytes());
        let mut st = conn.prepare(sql)?;
        let cols = st.column_count();
        let mut rows = st.query([])?;
        while let Some(r) = rows.next()? {
            for i in 0..cols {
                let v: rusqlite::types::Value = r.get(i)?;
                let s = match v {
                    rusqlite::types::Value::Null => "\u{0}N".to_string(),
                    rusqlite::types::Value::Integer(n) => n.to_string(),
                    rusqlite::types::Value::Real(f) => format!("{f:.6}"),
                    rusqlite::types::Value::Text(t) => t,
                    rusqlite::types::Value::Blob(b) => format!("{:x}", Sha256::digest(&b)),
                };
                h.update((s.len() as u64).to_le_bytes());
                h.update(s.as_bytes());
            }
            h.update(b"\n");
        }
        Ok(())
    };
    feed(
        "SELECT key, value FROM meta WHERE key IN
         ('schema_version','schema_fingerprint','analyzer_version','content_retention','config','last_commit','repo_mode')
         ORDER BY key",
        &mut h,
    )?;
    feed(
        "SELECT path, lang, role, size, content_hash, parse_status, flags, symbol_count, line_count
         FROM files ORDER BY path",
        &mut h,
    )?;
    feed(
        "SELECT s.uid, f.path, s.ord, n.name, s.qualified_name, s.kind, c.uid, s.arity,
                s.start_byte, s.end_byte, s.start_line, s.end_line, s.name_line, s.name_col,
                s.signature_hash, s.exported, s.depth, s.local
         FROM symbols s JOIN files f ON f.id = s.file_id JOIN names n ON n.id = s.name_id
         LEFT JOIN symbols c ON c.id = s.container_id ORDER BY s.uid",
        &mut h,
    )?;
    feed(
        "SELECT f.path, r.start_byte, n.name, r.kind, r.flow, r.line, r.col, s.uid, q.name
         FROM refs r JOIN files f ON f.id = r.file_id JOIN names n ON n.id = r.name_id
         LEFT JOIN symbols s ON s.id = r.symbol_id LEFT JOIN names q ON q.id = r.qual_id
         ORDER BY f.path, r.start_byte, n.name, r.kind",
        &mut h,
    )?;
    feed(
        "SELECT f.path, i.ord, i.line, i.kind, i.module, i.name, i.alias, i.target_key, t.path
         FROM imports i JOIN files f ON f.id = i.file_id LEFT JOIN files t ON t.id = i.resolved_file_id
         ORDER BY f.path, i.ord",
        &mut h,
    )?;
    feed(
        "SELECT f.path, c.ord, c.kind, c.start_line, c.end_line, c.start_byte, c.end_byte,
                c.content_hash, s.uid, t.text
         FROM chunks c JOIN files f ON f.id = c.file_id LEFT JOIN symbols s ON s.id = c.symbol_id
         LEFT JOIN chunk_text t ON t.chunk_id = c.id ORDER BY f.path, c.ord",
        &mut h,
    )?;
    feed(
        "SELECT a.path, b.path, e.kind, e.layer, e.weight, e.confidence, e.provenance, e.via,
                e.reason, e.access, s.uid, d.uid
         FROM edges e JOIN files a ON a.id = e.src_file JOIN files b ON b.id = e.dst_file
         LEFT JOIN symbols s ON s.id = e.src_symbol LEFT JOIN symbols d ON d.id = e.dst_symbol
         ORDER BY a.path, b.path, e.layer, e.kind, s.uid, d.uid",
        &mut h,
    )?;
    feed(
        "SELECT f.path, n.name, r.cause, r.count
         FROM resolve_drops r JOIN files f ON f.id = r.file_id JOIN names n ON n.id = r.name_id
         ORDER BY f.path, n.name, r.cause",
        &mut h,
    )?;
    feed(
        "SELECT path, role, content_hash, status, line_count FROM docs ORDER BY path",
        &mut h,
    )?;
    feed(
        "SELECT d.path, c.ord, c.start_line, c.end_line, c.start_byte, c.end_byte, c.content_hash
         FROM doc_chunks c JOIN docs d ON d.id = c.doc_id ORDER BY d.path, c.ord",
        &mut h,
    )?;
    for table in ["chunk_fts", "doc_fts"] {
        conn.execute_batch(&format!(
            "CREATE VIRTUAL TABLE IF NOT EXISTS temp.vocab USING fts5vocab(main, {table}, col);"
        ))?;
        feed(
            "SELECT term, col, doc, cnt FROM temp.vocab ORDER BY term, col",
            &mut h,
        )?;
        conn.execute_batch("DROP TABLE IF EXISTS temp.vocab;")?;
    }
    Ok(format!("{:x}", h.finalize()))
}
