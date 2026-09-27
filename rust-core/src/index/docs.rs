//! The docs lane's storage (WS-18, PAR-RT-11): tracked doc and guidance files, their
//! heading-section chunks and contentless FTS5 postings in `index.db`.
//!
//! Search used to reread every tracked doc on every query. The index worker now keeps
//! them next to the code: every build and update compares the scan's doc files with the
//! `docs` rows by stat key (the same racy-key rule as code files), rereads only the ones
//! whose key changed, and rewrites the chunks of a doc whose bytes changed, in one
//! transaction. Docs have no symbols, references or edges, so a doc-only change never
//! starts a new graph generation. Postings follow the content retention: `symbol`
//! leaves credential-shaped words out and stores an unordered bag, `full` keeps word
//! order, and `none` indexes no prose (the rows say `retention_none`). A doc on a secret
//! path (`secretpath`) is listed with status `secret_path` and never read.

use std::collections::{HashMap, HashSet};
use std::path::Path;

use rusqlite::{Connection, params};
use serde::Serialize;

use super::chunks;
use super::config::ContentRetention;
use super::scan::{self, DocCandidate, Scan};
use super::{RACY_PREFIX, stat_key_for};

/// `docs.status` of a doc whose chunks are indexed.
pub const INDEXED: &str = "indexed";
/// `docs.status` under `content_retention=none`: the doc is listed, its prose is not.
pub const RETENTION_NONE: &str = "retention_none";

#[derive(Debug, Clone, Serialize, Default, PartialEq, Eq)]
pub struct DocCounters {
    /// Doc and guidance files the scan listed.
    pub scanned: usize,
    /// Docs whose rows were (re)written.
    pub written: usize,
    pub unchanged: usize,
    pub deleted: usize,
    /// Docs past `scan::MAX_DOC_FILES` or undecided by the ignore budget.
    pub beyond: usize,
    pub bytes_read: u64,
}

struct Stored {
    id: i64,
    stat_key: String,
    content_hash: String,
    status: String,
}

fn stored_docs(conn: &Connection) -> rusqlite::Result<HashMap<String, Stored>> {
    let mut st = conn.prepare("SELECT id, path, stat_key, content_hash, status FROM docs")?;
    let rows = st.query_map([], |r| {
        Ok((
            r.get::<_, String>(1)?,
            Stored {
                id: r.get(0)?,
                stat_key: r.get(2)?,
                content_hash: r.get(3)?,
                status: r.get(4)?,
            },
        ))
    })?;
    rows.collect()
}

/// What one scanned doc needs.
enum Action {
    Keep,
    /// Same bytes under a new stat key.
    Restat(String),
    Write {
        status: &'static str,
        bytes: Option<Vec<u8>>,
    },
}

fn read_doc(root: &Path, d: &DocCandidate, c: &mut DocCounters) -> (&'static str, Option<Vec<u8>>) {
    if let Some(loss) = d.loss {
        return (loss, None);
    }
    match crate::symbolgraph::read_repo_bytes_beneath_capped(root, &d.path, scan::MAX_DOC_BYTES) {
        Ok(b) => {
            c.bytes_read += b.len() as u64;
            (INDEXED, Some(b))
        }
        Err(_) => ("unreadable", None),
    }
}

fn plan(
    root: &Path,
    d: &DocCandidate,
    key: &str,
    ex: Option<&Stored>,
    c: &mut DocCounters,
) -> Action {
    let trusted = ex.is_some_and(|e| !e.stat_key.starts_with(RACY_PREFIX) && e.stat_key == key);
    match (ex, d.loss) {
        (Some(e), Some(loss)) if e.status == loss => return Action::Keep,
        (Some(e), None) if trusted && e.status != "unreadable" => return Action::Keep,
        _ => {}
    }
    let (status, bytes) = read_doc(root, d, c);
    let same = ex.is_some_and(|e| {
        e.status == status
            && bytes
                .as_deref()
                .is_some_and(|b| scan::blob_id(b) == e.content_hash)
    });
    match same {
        true => Action::Restat(key.to_string()),
        false => Action::Write { status, bytes },
    }
}

/// Delete a doc's chunks and postings, and its row unless `keep_row`.
fn delete_doc_rows(conn: &Connection, id: i64, keep_row: bool) -> rusqlite::Result<()> {
    conn.prepare_cached(
        "DELETE FROM doc_fts WHERE rowid IN (SELECT id FROM doc_chunks WHERE doc_id = ?1)",
    )?
    .execute([id])?;
    conn.prepare_cached("DELETE FROM doc_chunks WHERE doc_id = ?1")?
        .execute([id])?;
    if !keep_row {
        conn.prepare_cached("DELETE FROM docs WHERE id = ?1")?
            .execute([id])?;
    }
    Ok(())
}

/// Postings of `text` under `retention` (see the module docs).
fn postings(text: &str, retention: ContentRetention) -> String {
    let mut out = String::new();
    match retention {
        ContentRetention::Full => chunks::tokenize_into(text, &mut out),
        ContentRetention::Symbol => {
            chunks::tokenize_without_secrets(text, &mut out);
            out = chunks::unordered(&out);
        }
        ContentRetention::None => {}
    }
    out
}

struct DocWriter<'c> {
    conn: &'c Connection,
    retention: ContentRetention,
    next_chunk: i64,
}

impl DocWriter<'_> {
    fn write(
        &mut self,
        prior: Option<i64>,
        d: &DocCandidate,
        key: &str,
        status: &'static str,
        bytes: Option<&[u8]>,
    ) -> rusqlite::Result<()> {
        let status = match (status, self.retention) {
            (INDEXED, ContentRetention::None) => RETENTION_NONE,
            (s, _) => s,
        };
        let hash = bytes.map(scan::blob_id).unwrap_or_default();
        let starts = bytes.map(chunks::line_starts).unwrap_or_default();
        let lines = chunks::line_count(&starts);
        let id = match prior {
            Some(id) => {
                self.conn
                    .prepare_cached(
                        "UPDATE docs SET role = ?2, stat_key = ?3, content_hash = ?4, status = ?5,
                         line_count = ?6 WHERE id = ?1",
                    )?
                    .execute(params![id, d.role, key, hash, status, lines])?;
                id
            }
            None => {
                self.conn
                    .prepare_cached(
                        "INSERT INTO docs(path, role, stat_key, content_hash, status, line_count)
                         VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                    )?
                    .execute(params![d.path, d.role, key, hash, status, lines])?;
                self.conn.last_insert_rowid()
            }
        };
        let Some(bytes) = bytes.filter(|_| status == INDEXED) else {
            return Ok(());
        };
        let markdown = matches!(
            Path::new(&d.path)
                .extension()
                .and_then(|e| e.to_str())
                .map(str::to_ascii_lowercase)
                .as_deref(),
            Some("md" | "markdown" | "mdx" | "mdc")
        );
        let path_tokens = chunks::path_tokens(&d.path);
        let line = |l: u32| {
            String::from_utf8_lossy(
                &bytes[starts[(l - 1) as usize] as usize..starts[l as usize] as usize],
            )
        };
        for (ord, c) in chunks::plan_doc_chunks(bytes, &starts, markdown)
            .iter()
            .enumerate()
        {
            let cid = self.next_chunk;
            self.next_chunk += 1;
            self.conn
                .prepare_cached(
                    "INSERT INTO doc_chunks(id, doc_id, ord, start_line, end_line, start_byte,
                     end_byte, content_hash) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
                )?
                .execute(params![
                    cid,
                    id,
                    ord as i64,
                    c.start_line,
                    c.end_line,
                    c.start_byte,
                    c.end_byte,
                    c.content_hash
                ])?;
            let heading = c
                .heading
                .map(|h| postings(&line(h), self.retention))
                .unwrap_or_default();
            let text = String::from_utf8_lossy(&bytes[c.start_byte as usize..c.end_byte as usize]);
            self.conn
                .prepare_cached(
                    "INSERT INTO doc_fts(rowid, path, heading, body) VALUES (?1, ?2, ?3, ?4)",
                )?
                .execute(params![
                    cid,
                    path_tokens,
                    heading,
                    postings(&text, self.retention)
                ])?;
        }
        Ok(())
    }
}

/// Bring the docs tables to the scan's doc files in one transaction.
pub fn sync(
    conn: &Connection,
    scan: &Scan,
    retention: ContentRetention,
) -> rusqlite::Result<DocCounters> {
    let mut c = DocCounters {
        beyond: scan.docs_beyond,
        ..Default::default()
    };
    let stored = stored_docs(conn)?;
    let listed: HashSet<&str> = scan.docs.iter().map(|d| d.path.as_str()).collect();
    let next_chunk: i64 =
        conn.query_row("SELECT coalesce(max(id), 0) + 1 FROM doc_chunks", [], |r| {
            r.get(0)
        })?;
    let mut w = DocWriter {
        conn,
        retention,
        next_chunk,
    };
    conn.execute_batch("BEGIN")?;
    for d in &scan.docs {
        c.scanned += 1;
        let ex = stored.get(&d.path);
        let key = match d.loss {
            Some(_) => String::new(),
            None => stat_key_for(&d.stat, scan.started_ns),
        };
        match plan(&scan.root, d, &key, ex, &mut c) {
            Action::Keep => c.unchanged += 1,
            Action::Restat(key) => {
                c.unchanged += 1;
                if let Some(e) = ex.filter(|e| e.stat_key != key) {
                    conn.prepare_cached("UPDATE docs SET stat_key = ?2 WHERE id = ?1")?
                        .execute(params![e.id, key])?;
                }
            }
            Action::Write { status, bytes } => {
                if let Some(e) = ex {
                    delete_doc_rows(conn, e.id, true)?;
                }
                w.write(ex.map(|e| e.id), d, &key, status, bytes.as_deref())?;
                c.written += 1;
            }
        }
    }
    let mut gone: Vec<(&String, i64)> = stored
        .iter()
        .filter(|(p, _)| !listed.contains(p.as_str()))
        .map(|(p, e)| (p, e.id))
        .collect();
    gone.sort();
    for (_, id) in gone {
        delete_doc_rows(conn, id, false)?;
        c.deleted += 1;
    }
    conn.execute_batch("COMMIT")?;
    Ok(c)
}
