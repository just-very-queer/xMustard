//! `index.db` layout, the schema fingerprint and connection setup.
//!
//! One SQLite file per workspace scope holds every extracted fact. Rows reference files
//! and symbols by integer id; names are interned once in `names`. The chunk postings
//! live in a contentless FTS5 table, so the index keeps term postings but never the
//! chunk text unless `content_retention=full` fills `chunk_text`. With `symbol` or
//! `none` retention each chunk's postings are an unordered bag of words (sorted before
//! insertion), so FTS5's positions carry no source order and `fts5vocab` cannot rebuild
//! the text; `symbol` also leaves credential-shaped words out, and `none` posts only
//! code identifiers.
//!
//! Tracked docs and guidance (`docs`, `doc_chunks`, `doc_fts`, WS-18) are indexed apart
//! from code: they have no symbols, references or edges, and their chunks are heading
//! sections. The same retention rule applies to their postings, and `none` indexes no
//! prose at all.

use rusqlite::Connection;
use sha2::{Digest, Sha256};

use super::config::ContentRetention;

/// Bump on any table or column change. Part of the schema fingerprint.
pub const SCHEMA_VERSION: u32 = 4;

/// Table definitions. Secondary indexes are in `INDEX_DDL` so a full build can create
/// them after the bulk insert.
pub const TABLE_DDL: &str = r#"
CREATE TABLE meta(
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE files(
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  lang TEXT NOT NULL,
  role TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  stat_key TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  parse_status TEXT NOT NULL,
  flags INTEGER NOT NULL,
  symbol_count INTEGER NOT NULL,
  line_count INTEGER NOT NULL,
  fact_symbols INTEGER NOT NULL
);

CREATE TABLE names(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE symbols(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL,
  ord INTEGER NOT NULL,
  uid TEXT NOT NULL UNIQUE,
  name_id INTEGER NOT NULL,
  qualified_name TEXT NOT NULL,
  kind TEXT NOT NULL,
  container_id INTEGER,
  arity INTEGER,
  start_byte INTEGER NOT NULL,
  end_byte INTEGER NOT NULL,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  name_line INTEGER NOT NULL,
  name_col INTEGER NOT NULL,
  signature_hash TEXT,
  exported INTEGER NOT NULL,
  depth INTEGER NOT NULL,
  local INTEGER NOT NULL
);

CREATE TABLE refs(
  file_id INTEGER NOT NULL,
  start_byte INTEGER NOT NULL,
  name_id INTEGER NOT NULL,
  symbol_id INTEGER,
  line INTEGER NOT NULL,
  col INTEGER NOT NULL,
  kind INTEGER NOT NULL,
  flow INTEGER NOT NULL,
  qual_id INTEGER,
  PRIMARY KEY(file_id, start_byte)
) WITHOUT ROWID;

CREATE TABLE imports(
  file_id INTEGER NOT NULL,
  ord INTEGER NOT NULL,
  line INTEGER NOT NULL,
  kind TEXT NOT NULL,
  module TEXT NOT NULL,
  name TEXT,
  alias TEXT,
  target_key TEXT,
  resolved_file_id INTEGER,
  PRIMARY KEY(file_id, ord)
) WITHOUT ROWID;

CREATE TABLE chunks(
  id INTEGER PRIMARY KEY,
  file_id INTEGER NOT NULL,
  ord INTEGER NOT NULL,
  symbol_id INTEGER,
  kind TEXT NOT NULL,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  start_byte INTEGER NOT NULL,
  end_byte INTEGER NOT NULL,
  content_hash TEXT NOT NULL
);

CREATE VIRTUAL TABLE chunk_fts USING fts5(
  path, symbols, body,
  content='', contentless_delete=1, detail=full,
  tokenize='unicode61 remove_diacritics 0'
);

CREATE TABLE chunk_text(
  chunk_id INTEGER PRIMARY KEY,
  text TEXT NOT NULL
);

CREATE TABLE edges(
  src_file INTEGER NOT NULL,
  dst_file INTEGER NOT NULL,
  src_symbol INTEGER,
  dst_symbol INTEGER,
  kind TEXT NOT NULL,
  layer TEXT NOT NULL,
  weight INTEGER NOT NULL,
  confidence REAL NOT NULL,
  provenance TEXT NOT NULL,
  via TEXT NOT NULL,
  reason TEXT NOT NULL,
  access TEXT
);

CREATE TABLE resolve_drops(
  file_id INTEGER NOT NULL,
  name_id INTEGER NOT NULL,
  cause TEXT NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY(file_id, name_id, cause)
) WITHOUT ROWID;

CREATE TABLE docs(
  id INTEGER PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL,
  stat_key TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  status TEXT NOT NULL,
  line_count INTEGER NOT NULL
);

CREATE TABLE doc_chunks(
  id INTEGER PRIMARY KEY,
  doc_id INTEGER NOT NULL,
  ord INTEGER NOT NULL,
  start_line INTEGER NOT NULL,
  end_line INTEGER NOT NULL,
  start_byte INTEGER NOT NULL,
  end_byte INTEGER NOT NULL,
  content_hash TEXT NOT NULL
);

CREATE VIRTUAL TABLE doc_fts USING fts5(
  path, heading, body,
  content='', contentless_delete=1, detail=full,
  tokenize='unicode61 remove_diacritics 0'
);

CREATE TABLE fact_cache(
  content_key TEXT PRIMARY KEY,
  facts BLOB NOT NULL,
  last_used INTEGER NOT NULL
) WITHOUT ROWID;
"#;

pub const INDEX_DDL: &str = r#"
CREATE INDEX IF NOT EXISTS files_hash ON files(content_hash);
CREATE INDEX IF NOT EXISTS symbols_file ON symbols(file_id);
CREATE INDEX IF NOT EXISTS symbols_name ON symbols(name_id);
CREATE INDEX IF NOT EXISTS refs_name ON refs(name_id);
CREATE INDEX IF NOT EXISTS imports_target ON imports(target_key);
CREATE INDEX IF NOT EXISTS imports_resolved ON imports(resolved_file_id);
CREATE INDEX IF NOT EXISTS chunks_file ON chunks(file_id);
CREATE INDEX IF NOT EXISTS edges_src ON edges(src_file);
CREATE INDEX IF NOT EXISTS edges_dst ON edges(dst_file);
CREATE INDEX IF NOT EXISTS edges_dst_symbol ON edges(dst_symbol);
CREATE INDEX IF NOT EXISTS resolve_drops_name ON resolve_drops(name_id);
CREATE INDEX IF NOT EXISTS fact_cache_used ON fact_cache(last_used);
CREATE INDEX IF NOT EXISTS doc_chunks_doc ON doc_chunks(doc_id);
"#;

/// The schema fingerprint: schema version, both DDL strings and the content retention
/// (retention changes what the tables hold). A stored fingerprint that differs from
/// this one forces a full rebuild.
pub fn fingerprint(retention: ContentRetention) -> String {
    let mut h = Sha256::new();
    h.update(SCHEMA_VERSION.to_le_bytes());
    h.update(TABLE_DDL.as_bytes());
    h.update(INDEX_DDL.as_bytes());
    h.update(retention.as_str().as_bytes());
    format!("{:x}", h.finalize())[..32].to_string()
}

/// SQLite page cache per connection, in KiB (negative `cache_size`). Two MiB keeps the
/// writer inside the heavy-slot line; pages beyond it spill to disk.
pub const CACHE_KIB: i64 = 2048;

/// Pragmas for every connection: bounded page cache, no mmap (mapped pages count toward
/// ps-RSS), a busy timeout for the rare concurrent reader.
pub fn configure(conn: &Connection) -> rusqlite::Result<()> {
    conn.execute_batch(&format!(
        "PRAGMA cache_size=-{CACHE_KIB};
         PRAGMA mmap_size=0;
         PRAGMA temp_store=FILE;
         PRAGMA foreign_keys=OFF;"
    ))?;
    conn.busy_timeout(std::time::Duration::from_secs(10))?;
    Ok(())
}

/// Page cache for a full build: the bulk insert appends in key order, so a small cache
/// costs little and keeps the worker's peak down (512 KiB measured ~1 MiB lower peak
/// than 1 MiB on the cline fixture, with no measurable slowdown).
pub const BULK_CACHE_KIB: i64 = 512;

/// FTS5 flushes pending postings to a segment once they exceed this many bytes (default
/// 1 MiB). A smaller buffer lowers the writer's peak; `automerge` keeps segment counts
/// bounded.
pub const FTS_HASHSIZE: i64 = 256 << 10;

/// Fast settings for a full build into a fresh temporary file: a crash discards the
/// file, so no journal and no fsync while it loads. The worker fsyncs the finished file
/// before the swap renames it over `index.db`.
pub fn configure_bulk(conn: &Connection) -> rusqlite::Result<()> {
    configure(conn)?;
    conn.execute_batch(&format!(
        "PRAGMA cache_size=-{BULK_CACHE_KIB}; PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF;"
    ))
}

/// Settings for an in-place incremental update and for the finished file: WAL so a
/// reader keeps its snapshot while one writer updates.
pub fn configure_durable(conn: &Connection) -> rusqlite::Result<()> {
    configure(conn)?;
    conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;")
}

pub fn create(conn: &Connection) -> rusqlite::Result<()> {
    conn.execute_batch(TABLE_DDL)?;
    // persisted in the FTS5 config table, so incremental updates use it too.
    for table in ["chunk_fts", "doc_fts"] {
        conn.execute(
            &format!("INSERT INTO {table}({table}, rank) VALUES('hashsize', ?1)"),
            [FTS_HASHSIZE],
        )?;
    }
    Ok(())
}

pub fn create_indexes(conn: &Connection) -> rusqlite::Result<()> {
    conn.execute_batch(INDEX_DDL)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fingerprint_changes_with_retention_and_is_stable() {
        let a = fingerprint(ContentRetention::Symbol);
        assert_eq!(a, fingerprint(ContentRetention::Symbol));
        assert_ne!(a, fingerprint(ContentRetention::None));
        assert_ne!(a, fingerprint(ContentRetention::Full));
        assert_eq!(a.len(), 32);
    }

    #[test]
    fn schema_creates_in_memory() {
        let conn = Connection::open_in_memory().unwrap();
        configure(&conn).unwrap();
        create(&conn).unwrap();
        create_indexes(&conn).unwrap();
        let n: i64 = conn
            .query_row(
                "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='chunk_fts'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1);
    }
}
