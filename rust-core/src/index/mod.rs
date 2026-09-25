//! Per-workspace code index store (PAR-STORE-03) and its transient worker
//! (`xmustard-core index build|update|stats`, PAR-RT-02).
//!
//! One SQLite file, `<git-dir>/xmustard-cache/index-v3/<scope>/index.db`, holds files,
//! symbols with nested qualified names and stable UIDs, references (never from comments
//! or string literals), imports, function-aligned chunks with contentless FTS5 postings,
//! lexical file edges, the content-addressed per-file fact cache and meta.
//!
//! The worker streams one file at a time on one thread: read, parse, extract, drop the
//! tree, write the rows in batched transactions. A full build writes a fresh file and
//! swaps it in; an incremental update rewrites only changed files in place under the
//! `incremental_in_progress` dirty flag and re-resolves edges only for changed files
//! and the files that name a declaration whose definer changed (PAR-FRESH-04). A leftover
//! dirty flag, a schema, analyzer or configuration change, or a write set above 50% of
//! the files and at least 50 files forces a full rebuild. The declared scale envelope
//! (PAR-RT-09) bounds files, bytes and symbols; beyond it the index is partial and says
//! so in `coverage`, never by growing the worker.
//!
//! No consumer reads this store yet; WS-14 switches the query side to it.

pub mod chunks;
pub mod config;
pub mod edges;
pub mod extract;
pub mod facts;
pub mod ignore;
pub mod lexical;
pub mod meta;
pub mod scan;
pub mod schema;
pub mod uid;
pub mod writer;

use std::collections::{BTreeSet, HashMap, HashSet};
use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant, SystemTime};

use rusqlite::{Connection, OpenFlags, OptionalExtension, params};
use serde::Serialize;
use sha2::{Digest, Sha256};

use config::{ContentRetention, IndexConfig};
use facts::{FileFacts, file_flag};
use scan::{Candidate, Scan};
use writer::{FileRecord, Writer};

pub const INDEX_DIR_NAME: &str = "index-v3";
pub const DB_FILE: &str = "index.db";
/// Files per write transaction.
const BATCH_FILES: usize = 256;
/// Escalation gate: an update whose write set exceeds half of the files and has at
/// least this many files becomes a full rebuild.
pub const ESCALATE_MIN_FILES: usize = 50;
/// Unreferenced fact-cache entries kept at least (superseded versions, for reverts and
/// branch switches).
const FACT_CACHE_KEEP_MIN: i64 = 256;

/// Analyzer identity: extraction rules and grammar versions. A change forces a full
/// rebuild and invalidates the fact cache.
pub fn analyzer_version() -> String {
    format!("xm-analyzer-1;{}", extract::grammar_versions())
}

#[derive(Debug, Clone, Serialize, Default)]
pub struct Counters {
    /// Eligible files considered by this run.
    pub files_scanned: usize,
    /// Files skipped because their content key matched the stored one.
    pub unchanged: usize,
    /// Files parsed and extracted this run.
    pub reparsed: usize,
    /// Files whose facts came from the content-addressed cache (no parse).
    pub reused_from_cache: usize,
    /// Files whose rows were (re)written.
    pub written: usize,
    pub deleted: usize,
    /// Files whose outgoing edges were recomputed.
    pub reresolved: usize,
    /// The update's write set crossed the escalation gate and ran as a full rebuild.
    pub escalated: bool,
    pub bytes_read: u64,
    pub edges_written: usize,
}

#[derive(Debug, Clone, Serialize, Default)]
pub struct Timing {
    pub elapsed_ms: u64,
    pub scan_ms: u64,
    pub edges_ms: u64,
}

#[derive(Debug, Clone, Serialize)]
pub struct IndexReport {
    /// `build` | `update`
    pub command: String,
    /// `full` | `incremental` | `noop`
    pub mode: String,
    /// Why this mode ran: `requested`, `no_index`, `schema_changed`, `analyzer_changed`,
    /// `retention_changed`, `config_changed`, `dirty_flag`, `escalated`, `envelope`,
    /// `unreadable_index`, `changes` or `unchanged`.
    pub reason: String,
    pub root: String,
    pub index_path: String,
    pub repo_mode: String,
    pub last_commit: String,
    pub schema_version: u32,
    pub schema_fingerprint: String,
    pub analyzer_version: String,
    pub content_retention: String,
    pub counters: Counters,
    pub coverage: meta::Coverage,
    /// Wall-clock timings; the only nondeterministic fields of the report.
    pub timing: Timing,
}

/// The directory holding `index.db` for a scan's root.
pub fn index_dir(root: &Path, git_dir: Option<&Path>, cfg: &IndexConfig) -> PathBuf {
    if let Some(d) = &cfg.index_dir {
        return d.clone();
    }
    let mut h = Sha256::new();
    h.update(root.to_string_lossy().as_bytes());
    h.update([0]);
    h.update(crate::indexcache::trust_scope().as_bytes());
    let scope = format!("{:x}", h.finalize())[..32].to_string();
    match git_dir {
        Some(g) => g.join("xmustard-cache").join(INDEX_DIR_NAME).join(scope),
        None => std::env::temp_dir()
            .join(format!("xmustard-{INDEX_DIR_NAME}"))
            .join(scope),
    }
}

/// Advisory lock serializing index writers of one directory across processes.
struct IndexLock {
    _file: fs::File,
}

fn lock(dir: &Path) -> Result<IndexLock, String> {
    fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let path = dir.join("index.lock");
    let file = fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .write(true)
        .open(&path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    let start = Instant::now();
    let timeout = crate::indexcache::lock_timeout();
    loop {
        match file.try_lock() {
            Ok(()) => return Ok(IndexLock { _file: file }),
            Err(fs::TryLockError::WouldBlock) => {
                if start.elapsed() >= timeout {
                    return Err(format!(
                        "another index writer holds {} (waited {} ms)",
                        path.display(),
                        start.elapsed().as_millis()
                    ));
                }
                std::thread::sleep(Duration::from_millis(25));
            }
            Err(fs::TryLockError::Error(e)) => return Err(format!("{}: {e}", path.display())),
        }
    }
}

fn sql_err(e: rusqlite::Error) -> String {
    format!("index store: {e}")
}

/// Facts by content key: the new store first, then (full builds) the previous index.
struct FactCache<'c> {
    conn: &'c Connection,
    old: Option<&'c Connection>,
    generation: i64,
    reuse: bool,
}

impl FactCache<'_> {
    fn get(&self, key: &str) -> rusqlite::Result<Option<FileFacts>> {
        if !self.reuse {
            return Ok(None);
        }
        let hit: Option<Vec<u8>> = self
            .conn
            .prepare_cached("SELECT facts FROM fact_cache WHERE content_key = ?1")?
            .query_row([key], |r| r.get(0))
            .optional()?;
        if let Some(bytes) = hit {
            self.conn
                .prepare_cached("UPDATE fact_cache SET last_used = ?2 WHERE content_key = ?1")?
                .execute(params![key, self.generation])?;
            return Ok(FileFacts::decode(&bytes));
        }
        let Some(old) = self.old else { return Ok(None) };
        let prev: Option<Vec<u8>> = old
            .prepare_cached("SELECT facts FROM fact_cache WHERE content_key = ?1")?
            .query_row([key], |r| r.get(0))
            .optional()?;
        match prev.as_deref().and_then(FileFacts::decode) {
            Some(f) => {
                self.put_raw(key, prev.as_deref().unwrap_or_default())?;
                Ok(Some(f))
            }
            None => Ok(None),
        }
    }

    fn put_raw(&self, key: &str, bytes: &[u8]) -> rusqlite::Result<()> {
        self.conn
            .prepare_cached(
                "INSERT INTO fact_cache(content_key, facts, last_used) VALUES (?1, ?2, ?3)
                 ON CONFLICT(content_key) DO UPDATE SET facts = excluded.facts, last_used = excluded.last_used",
            )?
            .execute(params![key, bytes, self.generation])?;
        Ok(())
    }

    fn put(&self, key: &str, facts: &FileFacts) -> rusqlite::Result<()> {
        self.put_raw(key, &facts.encode())
    }
}

/// Running totals for the envelope.
#[derive(Default)]
struct Totals {
    files_indexed: usize,
    bytes: u64,
    symbols: usize,
}

struct Processed {
    rec: FileRecord,
    facts: Option<FileFacts>,
    bytes: Option<Vec<u8>>,
}

/// The stat key stored for a file scanned at `started_ns`. A file modified within the
/// second before the scan may be modified again without changing its timestamp on a
/// coarse filesystem (Git's "racily clean" case), so its key is marked racy and never
/// matches: the next update hashes it and stores a trusted key once the content is
/// confirmed.
fn stat_key_for(stat: &scan::StatKey, started_ns: i64) -> String {
    const RACY_WINDOW_NS: i64 = 1_000_000_000;
    if stat.mtime_ns >= started_ns - RACY_WINDOW_NS {
        format!("racy:{}", stat.encode())
    } else {
        stat.encode()
    }
}

fn record_for(cand: &Candidate, started_ns: i64) -> FileRecord {
    FileRecord {
        path: cand.path.clone(),
        lang: cand.lang.name().to_string(),
        role: if edges::is_test_file(&cand.path) {
            "test".into()
        } else {
            "code".into()
        },
        size: cand.stat.size as i64,
        mtime_ns: cand.stat.mtime_ns,
        stat_key: stat_key_for(&cand.stat, started_ns),
        content_hash: String::new(),
        parse_status: String::new(),
        flags: 0,
    }
}

fn strip_symbols(f: &mut FileFacts) {
    f.symbols.clear();
    for r in f.refs.iter_mut() {
        r.4 = -1;
    }
    for c in f.chunks.iter_mut() {
        c.symbol = None;
    }
}

/// Read, identify and extract one candidate, applying the envelope. The file's tree and
/// text are dropped before this returns; only its facts and bytes go to the writer.
fn process(
    root: &Path,
    started_ns: i64,
    cand: &Candidate,
    cfg: &IndexConfig,
    cache: &FactCache<'_>,
    totals: &mut Totals,
    counters: &mut Counters,
) -> Result<Processed, String> {
    let mut rec = record_for(cand, started_ns);
    let none = |rec: FileRecord| Processed {
        rec,
        facts: None,
        bytes: None,
    };
    if let Some(loss) = cand.loss {
        rec.parse_status = loss.into();
        return Ok(none(rec));
    }
    if totals.files_indexed >= cfg.max_files {
        rec.parse_status = "envelope_files".into();
        return Ok(none(rec));
    }
    // the scan's stat (taken before this read) is what `rec` records.
    if cand.stat.size > cfg.max_file_size {
        rec.parse_status = "oversized".into();
        return Ok(none(rec));
    }
    if totals.bytes + cand.stat.size > cfg.max_total_bytes {
        rec.parse_status = "envelope_bytes".into();
        return Ok(none(rec));
    }
    let bytes = match crate::symbolgraph::read_source_beneath(
        root,
        Path::new(&cand.path),
        cfg.max_file_size,
    ) {
        Ok(b) => b,
        Err(f) => {
            rec.parse_status = f.reason().into();
            return Ok(none(rec));
        }
    };
    counters.bytes_read += bytes.len() as u64;
    rec.content_hash = scan::blob_id(&bytes);
    let mode = extract::extraction_mode(cand.lang, bytes.len(), cfg.max_parse_bytes);
    let key = format!("{}:{}:{mode}", rec.content_hash, cand.lang.name());
    let len = bytes.len() as u64;
    // Facts, chunk ranges and postings all refer to the decoded text: for invalid UTF-8
    // that is the lossy decoding, so the writer slices the same bytes extraction saw.
    let (text, invalid) = match String::from_utf8(bytes) {
        Ok(t) => (t, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    };
    let mut facts = match cache.get(&key).map_err(sql_err)? {
        Some(f) => {
            counters.reused_from_cache += 1;
            f
        }
        None => {
            let f = extract::extract(cand.lang, &text, invalid, cfg.max_parse_bytes);
            counters.reparsed += 1;
            cache.put(&key, &f).map_err(sql_err)?;
            f
        }
    };
    totals.files_indexed += 1;
    totals.bytes += len;
    if totals.symbols + facts.symbols.len() > cfg.max_symbols {
        strip_symbols(&mut facts);
        rec.flags |= file_flag::SYMBOL_BUDGET;
    } else {
        totals.symbols += facts.symbols.len();
    }
    rec.parse_status = facts.engine.clone();
    Ok(Processed {
        rec,
        facts: Some(facts),
        bytes: Some(text.into_bytes()),
    })
}

fn set_meta_common(
    conn: &Connection,
    scan: &Scan,
    cfg: &IndexConfig,
    generation: i64,
) -> rusqlite::Result<()> {
    meta::set(conn, "schema_version", &schema::SCHEMA_VERSION.to_string())?;
    meta::set(
        conn,
        "schema_fingerprint",
        &schema::fingerprint(cfg.content_retention),
    )?;
    meta::set(conn, "analyzer_version", &analyzer_version())?;
    meta::set(conn, "content_retention", cfg.content_retention.as_str())?;
    meta::set(conn, "config", &cfg.eligibility_fingerprint())?;
    meta::set(conn, "last_commit", &scan.head)?;
    meta::set(conn, "repo_mode", scan.repo_mode)?;
    meta::set(conn, "root", &scan.root.to_string_lossy())?;
    meta::set(conn, "ignored_files", &scan.ignored.to_string())?;
    meta::set(conn, "invalid_paths", &scan.invalid_paths.to_string())?;
    meta::set(conn, "worktree_deleted", &scan.worktree_deleted.to_string())?;
    meta::set(conn, "generation", &generation.to_string())?;
    Ok(())
}

/// Open the previous index read-only when its facts are reusable (same analyzer and
/// schema); a dirty flag does not matter here because cache entries are keyed by content
/// and written whole.
fn open_compatible(path: &Path, cfg: &IndexConfig) -> Option<Connection> {
    if !path.exists() {
        return None;
    }
    let conn = Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY).ok()?;
    schema::configure(&conn).ok()?;
    let ok = meta::get(&conn, "analyzer_version").ok()?? == analyzer_version()
        && meta::get(&conn, "schema_fingerprint").ok()??
            == schema::fingerprint(cfg.content_retention);
    ok.then_some(conn)
}

/// `(command, mode, reason)` of a finished run.
type RunKind<'a> = (&'a str, &'a str, &'a str);

fn report(
    (command, mode, reason): RunKind<'_>,
    scan: &Scan,
    dir: &Path,
    cfg: &IndexConfig,
    counters: Counters,
    coverage: meta::Coverage,
    timing: Timing,
) -> IndexReport {
    IndexReport {
        command: command.into(),
        mode: mode.into(),
        reason: reason.into(),
        root: scan.root.to_string_lossy().into_owned(),
        index_path: dir.join(DB_FILE).to_string_lossy().into_owned(),
        repo_mode: scan.repo_mode.into(),
        last_commit: scan.head.clone(),
        schema_version: schema::SCHEMA_VERSION,
        schema_fingerprint: schema::fingerprint(cfg.content_retention),
        analyzer_version: analyzer_version(),
        content_retention: cfg.content_retention.as_str().into(),
        counters,
        coverage,
        timing,
    }
}

/// `index build`: a full rebuild into a fresh file, swapped in atomically.
pub fn build(root: &Path, cfg: &IndexConfig) -> Result<IndexReport, String> {
    let t0 = Instant::now();
    let scan = scan::scan(root, cfg)?;
    let scan_ms = t0.elapsed().as_millis() as u64;
    let dir = index_dir(&scan.root, scan.git_dir.as_deref(), cfg);
    let _lock = lock(&dir)?;
    full_build(
        &scan,
        cfg,
        &dir,
        "build",
        "requested",
        Counters::default(),
        t0,
        scan_ms,
    )
}

fn sweep_stale_builds(dir: &Path) {
    let Ok(rd) = fs::read_dir(dir) else { return };
    let now = SystemTime::now();
    for e in rd.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        if !name.starts_with(&format!("{DB_FILE}.building-")) {
            continue;
        }
        let old = e
            .metadata()
            .ok()
            .and_then(|m| m.modified().ok())
            .and_then(|m| now.duration_since(m).ok())
            .is_some_and(|age| age > Duration::from_secs(600));
        if old {
            let _ = fs::remove_file(e.path());
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn full_build(
    scan: &Scan,
    cfg: &IndexConfig,
    dir: &Path,
    command: &str,
    reason: &str,
    mut counters: Counters,
    t0: Instant,
    scan_ms: u64,
) -> Result<IndexReport, String> {
    sweep_stale_builds(dir);
    let final_path = dir.join(DB_FILE);
    let tmp_path = dir.join(format!("{DB_FILE}.building-{}", std::process::id()));
    let _ = fs::remove_file(&tmp_path);
    let old = if cfg.use_fact_cache {
        open_compatible(&final_path, cfg)
    } else {
        None
    };
    let generation = old
        .as_ref()
        .and_then(|c| meta::get(c, "generation").ok().flatten())
        .and_then(|g| g.parse::<i64>().ok())
        .unwrap_or(0)
        + 1;
    let coverage;
    let edges_ms;
    {
        let conn = Connection::open(&tmp_path).map_err(sql_err)?;
        schema::configure_bulk(&conn).map_err(sql_err)?;
        schema::create(&conn).map_err(sql_err)?;
        let cache = FactCache {
            conn: &conn,
            old: old.as_ref(),
            generation,
            reuse: cfg.use_fact_cache,
        };
        let mut w = Writer::new(&conn, cfg.content_retention).map_err(sql_err)?;
        let mut totals = Totals::default();
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        for (i, cand) in scan.candidates.iter().enumerate() {
            counters.files_scanned += 1;
            let p = process(
                &scan.root,
                scan.started_ns,
                cand,
                cfg,
                &cache,
                &mut totals,
                &mut counters,
            )?;
            w.write_file(None, &p.rec, p.facts.as_ref(), p.bytes.as_deref())
                .map_err(sql_err)?;
            counters.written += 1;
            drop(p);
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
        conn.execute_batch("COMMIT").map_err(sql_err)?;
        drop(old);
        schema::create_indexes(&conn).map_err(sql_err)?;
        let te = Instant::now();
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        writer::resolve_imports(&conn, None).map_err(sql_err)?;
        let defs = edges::Definers::load(&conn).map_err(sql_err)?;
        let files = indexed_files(&conn).map_err(sql_err)?;
        for (i, (fid, path)) in files.iter().enumerate() {
            counters.edges_written +=
                edges::rebuild_file_edges(&conn, &defs, *fid, path).map_err(sql_err)?;
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
        counters.reresolved = files.len();
        drop(defs);
        edges_ms = te.elapsed().as_millis() as u64;
        set_meta_common(&conn, scan, cfg, generation).map_err(sql_err)?;
        meta::set(&conn, meta::DIRTY_FLAG, "0").map_err(sql_err)?;
        coverage = meta::coverage(&conn, cfg).map_err(sql_err)?;
        meta::set(
            &conn,
            "coverage",
            &serde_json::to_string(&coverage).unwrap_or_default(),
        )
        .map_err(sql_err)?;
        meta::set(
            &conn,
            "last_run",
            &serde_json::to_string(&counters).unwrap_or_default(),
        )
        .map_err(sql_err)?;
        conn.execute_batch("COMMIT").map_err(sql_err)?;
        conn.execute_batch("INSERT INTO chunk_fts(chunk_fts) VALUES('optimize');")
            .map_err(sql_err)?;
        // the finished file runs in WAL mode so readers keep a snapshot during updates.
        conn.execute_batch("PRAGMA journal_mode=WAL;")
            .map_err(sql_err)?;
        conn.close().map_err(|(_, e)| sql_err(e))?;
    }
    swap_into_place(&tmp_path, &final_path)?;
    let timing = Timing {
        elapsed_ms: t0.elapsed().as_millis() as u64,
        scan_ms,
        edges_ms,
    };
    Ok(report(
        (command, "full", reason),
        scan,
        dir,
        cfg,
        counters,
        coverage,
        timing,
    ))
}

/// Replace `final_path` with the finished build. The previous file is checkpointed and
/// its WAL/SHM removed first, so the new file never meets a stale WAL.
fn swap_into_place(tmp: &Path, final_path: &Path) -> Result<(), String> {
    let wal = PathBuf::from(format!("{}-wal", final_path.display()));
    let shm = PathBuf::from(format!("{}-shm", final_path.display()));
    if final_path.exists()
        && let Ok(c) = Connection::open(final_path)
    {
        let _ = c.execute_batch("PRAGMA wal_checkpoint(TRUNCATE);");
        let _ = c.close();
    }
    let _ = fs::remove_file(&wal);
    let _ = fs::remove_file(&shm);
    fs::rename(tmp, final_path).map_err(|e| format!("swap {}: {e}", final_path.display()))?;
    if let Some(dir) = final_path.parent()
        && let Ok(d) = fs::File::open(dir)
    {
        let _ = d.sync_all();
    }
    Ok(())
}

fn indexed_files(conn: &Connection) -> rusqlite::Result<Vec<(i64, String)>> {
    let statuses = meta::INDEXED_STATUSES
        .iter()
        .map(|s| format!("'{s}'"))
        .collect::<Vec<_>>()
        .join(",");
    let mut st = conn.prepare(&format!(
        "SELECT id, path FROM files WHERE parse_status IN ({statuses}) ORDER BY id"
    ))?;
    let rows = st.query_map([], |r| Ok((r.get(0)?, r.get(1)?)))?;
    rows.collect()
}

/// Why the stored index cannot be updated in place, if it cannot.
fn full_rebuild_reason(
    conn: &Connection,
    cfg: &IndexConfig,
) -> rusqlite::Result<Option<&'static str>> {
    let get = |k: &str| meta::get(conn, k);
    if get("schema_version")?.as_deref() != Some(&schema::SCHEMA_VERSION.to_string()) {
        return Ok(Some("schema_changed"));
    }
    if get("content_retention")?.as_deref() != Some(cfg.content_retention.as_str()) {
        return Ok(Some("retention_changed"));
    }
    if get("schema_fingerprint")?.as_deref()
        != Some(schema::fingerprint(cfg.content_retention).as_str())
    {
        return Ok(Some("schema_changed"));
    }
    if get("analyzer_version")?.as_deref() != Some(analyzer_version().as_str()) {
        return Ok(Some("analyzer_changed"));
    }
    if get("config")?.as_deref() != Some(cfg.eligibility_fingerprint().as_str()) {
        return Ok(Some("config_changed"));
    }
    if get(meta::DIRTY_FLAG)?.as_deref() != Some("0") {
        return Ok(Some("dirty_flag"));
    }
    let partial: i64 = conn.query_row(
        "SELECT count(*) FROM files WHERE parse_status IN ('envelope_files','envelope_bytes')
         OR (flags & ?1) != 0",
        [file_flag::SYMBOL_BUDGET],
        |r| r.get(0),
    )?;
    if partial > 0 {
        // beyond the envelope the kept set depends on path order; rebuild to stay exact.
        return Ok(Some("envelope"));
    }
    Ok(None)
}

struct Existing {
    id: i64,
    stat_key: String,
    content_hash: String,
    parse_status: String,
    lang: String,
}

/// `index update [--paths ...]`: bring the index to the working tree, incrementally
/// when possible. With `paths`, only those files are considered (a watcher's batch).
pub fn update(
    root: &Path,
    cfg: &IndexConfig,
    paths: Option<&[String]>,
) -> Result<IndexReport, String> {
    let t0 = Instant::now();
    let scan = scan::scan(root, cfg)?;
    let scan_ms = t0.elapsed().as_millis() as u64;
    let dir = index_dir(&scan.root, scan.git_dir.as_deref(), cfg);
    let _lock = lock(&dir)?;
    let final_path = dir.join(DB_FILE);
    if !final_path.exists() {
        return full_build(
            &scan,
            cfg,
            &dir,
            "update",
            "no_index",
            Counters::default(),
            t0,
            scan_ms,
        );
    }
    let conn = match Connection::open(&final_path) {
        Ok(c) => c,
        Err(_) => {
            return full_build(
                &scan,
                cfg,
                &dir,
                "update",
                "unreadable_index",
                Counters::default(),
                t0,
                scan_ms,
            );
        }
    };
    let reason =
        match schema::configure_durable(&conn).and_then(|_| full_rebuild_reason(&conn, cfg)) {
            Ok(r) => r,
            Err(_) => Some("unreadable_index"),
        };
    if let Some(reason) = reason {
        drop(conn);
        return full_build(
            &scan,
            cfg,
            &dir,
            "update",
            reason,
            Counters::default(),
            t0,
            scan_ms,
        );
    }
    if scan.candidates.len() > cfg.max_files {
        drop(conn);
        return full_build(
            &scan,
            cfg,
            &dir,
            "update",
            "envelope",
            Counters::default(),
            t0,
            scan_ms,
        );
    }
    incremental(conn, &scan, cfg, &dir, paths, t0, scan_ms)
}

fn normalize_paths(root: &Path, paths: &[String]) -> HashSet<String> {
    paths
        .iter()
        .map(|p| {
            let pb = Path::new(p);
            let rel = if pb.is_absolute() {
                pb.strip_prefix(root).unwrap_or(pb).to_path_buf()
            } else {
                pb.to_path_buf()
            };
            rel.to_string_lossy()
                .trim_start_matches("./")
                .replace('\\', "/")
        })
        .collect()
}

#[allow(clippy::too_many_arguments)]
fn incremental(
    conn: Connection,
    scan: &Scan,
    cfg: &IndexConfig,
    dir: &Path,
    paths: Option<&[String]>,
    t0: Instant,
    scan_ms: u64,
) -> Result<IndexReport, String> {
    let mut counters = Counters::default();
    let filter = paths.map(|p| normalize_paths(&scan.root, p));
    let considered = |p: &str| filter.as_ref().is_none_or(|f| f.contains(p));

    let mut existing: HashMap<String, Existing> = HashMap::new();
    {
        let mut st = conn
            .prepare("SELECT id, path, content_hash, parse_status, lang, stat_key FROM files")
            .map_err(sql_err)?;
        let rows = st
            .query_map([], |r| {
                Ok((
                    r.get::<_, String>(1)?,
                    Existing {
                        id: r.get(0)?,
                        content_hash: r.get(2)?,
                        parse_status: r.get(3)?,
                        lang: r.get(4)?,
                        stat_key: r.get(5)?,
                    },
                ))
            })
            .map_err(sql_err)?;
        for row in rows {
            let (p, e) = row.map_err(sql_err)?;
            if considered(&p) {
                existing.insert(p, e);
            }
        }
    }
    let db_total: i64 = conn
        .query_row("SELECT count(*) FROM files", [], |r| r.get(0))
        .map_err(sql_err)?;

    // ---- classify without extracting: unchanged stat keys skip the read; other files
    // are identified by hashing ----
    let mut changed: Vec<&Candidate> = Vec::new();
    // same bytes under a new stat key (touched, checked out again): refresh the key.
    let mut restat: Vec<(i64, String)> = Vec::new();
    let mut seen: HashSet<&str> = HashSet::new();
    for cand in scan.candidates.iter().filter(|c| considered(&c.path)) {
        counters.files_scanned += 1;
        seen.insert(cand.path.as_str());
        let Some(ex) = existing.get(&cand.path) else {
            changed.push(cand);
            continue;
        };
        if ex.lang != cand.lang.name() {
            changed.push(cand);
            continue;
        }
        if let Some(loss) = cand.loss {
            if ex.parse_status == loss {
                counters.unchanged += 1;
            } else {
                changed.push(cand);
            }
            continue;
        }
        let indexed = meta::INDEXED_STATUSES.contains(&ex.parse_status.as_str());
        let key = stat_key_for(&cand.stat, scan.started_ns);
        if indexed && key == ex.stat_key {
            counters.unchanged += 1;
            continue;
        }
        // stat changed, racy, or previously unreadable: identify by content
        let same = crate::symbolgraph::read_source_beneath(
            &scan.root,
            Path::new(&cand.path),
            cfg.max_file_size,
        )
        .ok()
        .map(|b| {
            counters.bytes_read += b.len() as u64;
            scan::blob_id(&b)
        })
        .is_some_and(|h| indexed && h == ex.content_hash);
        if same {
            counters.unchanged += 1;
            if key != ex.stat_key {
                restat.push((ex.id, key));
            }
        } else {
            changed.push(cand);
        }
    }
    if !restat.is_empty() {
        let mut st = conn
            .prepare_cached("UPDATE files SET stat_key = ?2 WHERE id = ?1")
            .map_err(sql_err)?;
        for (id, key) in &restat {
            st.execute(params![id, key]).map_err(sql_err)?;
        }
    }
    let deleted: Vec<(String, i64)> = existing
        .iter()
        .filter(|(p, _)| !seen.contains(p.as_str()))
        .map(|(p, e)| (p.clone(), e.id))
        .collect();

    let head_changed =
        meta::get(&conn, "last_commit").map_err(sql_err)?.as_deref() != Some(scan.head.as_str());
    if changed.is_empty() && deleted.is_empty() {
        if head_changed {
            meta::set(&conn, "last_commit", &scan.head).map_err(sql_err)?;
        }
        let coverage = meta::coverage(&conn, cfg).map_err(sql_err)?;
        let timing = Timing {
            elapsed_ms: t0.elapsed().as_millis() as u64,
            scan_ms,
            edges_ms: 0,
        };
        return Ok(report(
            ("update", "noop", "unchanged"),
            scan,
            dir,
            cfg,
            counters,
            coverage,
            timing,
        ));
    }

    // ---- escalation gate ----
    let write_set = changed.len() + deleted.len();
    let total = (db_total as usize).max(scan.candidates.len());
    if write_set * 2 > total && write_set >= ESCALATE_MIN_FILES {
        drop(conn);
        let c = Counters {
            escalated: true,
            ..Default::default()
        };
        return full_build(scan, cfg, dir, "update", "escalated", c, t0, scan_ms);
    }

    // ---- apply in place under the dirty flag ----
    meta::set(&conn, meta::DIRTY_FLAG, "1").map_err(sql_err)?;
    let generation = meta::get(&conn, "generation")
        .map_err(sql_err)?
        .and_then(|g| g.parse::<i64>().ok())
        .unwrap_or(0)
        + 1;
    let defs_before = edges::Definers::load(&conn).map_err(sql_err)?;
    let mut candidate_names: BTreeSet<i64> = BTreeSet::new();
    let mut touched_paths: Vec<String> = Vec::new();
    let mut changed_ids: Vec<i64> = Vec::new();
    {
        let cache = FactCache {
            conn: &conn,
            old: None,
            generation,
            reuse: cfg.use_fact_cache,
        };
        let mut w = Writer::new(&conn, cfg.content_retention).map_err(sql_err)?;
        // totals for the envelope: everything not being rewritten stays as stored.
        let mut totals = Totals::default();
        {
            let rewritten: HashSet<&str> = changed
                .iter()
                .map(|c| c.path.as_str())
                .chain(deleted.iter().map(|(p, _)| p.as_str()))
                .collect();
            let mut st = conn
                .prepare("SELECT path, parse_status, size, symbol_count FROM files")
                .map_err(sql_err)?;
            let rows = st
                .query_map([], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, i64>(2)?,
                        r.get::<_, i64>(3)?,
                    ))
                })
                .map_err(sql_err)?;
            for row in rows {
                let (p, status, size, syms) = row.map_err(sql_err)?;
                if rewritten.contains(p.as_str())
                    || !meta::INDEXED_STATUSES.contains(&status.as_str())
                {
                    continue;
                }
                totals.files_indexed += 1;
                totals.bytes += size.max(0) as u64;
                totals.symbols += syms.max(0) as usize;
            }
        }
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        for (path, id) in &deleted {
            candidate_names.extend(edges::defined_names(&conn, *id).map_err(sql_err)?);
            writer::delete_file_rows(&conn, *id, false).map_err(sql_err)?;
            counters.deleted += 1;
            touched_paths.push(path.clone());
        }
        for (i, cand) in changed.iter().enumerate() {
            let prior = existing.get(&cand.path).map(|e| e.id);
            if let Some(id) = prior {
                candidate_names.extend(edges::defined_names(&conn, id).map_err(sql_err)?);
                writer::delete_file_rows(&conn, id, true).map_err(sql_err)?;
            } else {
                touched_paths.push(cand.path.clone());
            }
            let p = process(
                &scan.root,
                scan.started_ns,
                cand,
                cfg,
                &cache,
                &mut totals,
                &mut counters,
            )?;
            if p.rec.flags & file_flag::SYMBOL_BUDGET != 0
                || p.rec.parse_status.starts_with("envelope")
            {
                // the update crosses the envelope: rebuild so the kept set is exact.
                conn.execute_batch("COMMIT").map_err(sql_err)?;
                drop(w);
                drop(conn);
                return full_build(
                    scan,
                    cfg,
                    dir,
                    "update",
                    "envelope",
                    Counters::default(),
                    t0,
                    scan_ms,
                );
            }
            let fid = w
                .write_file(prior, &p.rec, p.facts.as_ref(), p.bytes.as_deref())
                .map_err(sql_err)?;
            counters.written += 1;
            changed_ids.push(fid);
            candidate_names.extend(edges::defined_names(&conn, fid).map_err(sql_err)?);
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
    }

    // ---- re-resolve: changed files, importers of added/removed paths, and files that
    // name a declaration whose unique definer changed ----
    let te = Instant::now();
    let mut affected: BTreeSet<i64> = changed_ids.iter().copied().collect();
    {
        let mut st = conn
            .prepare_cached("SELECT DISTINCT file_id FROM imports WHERE target_key = ?1")
            .map_err(sql_err)?;
        for p in &touched_paths {
            for key in writer::keys_resolving_to(p) {
                for f in st
                    .query_map([&key], |r| r.get::<_, i64>(0))
                    .map_err(sql_err)?
                {
                    affected.insert(f.map_err(sql_err)?);
                }
            }
        }
    }
    let resolve_ids: Vec<i64> = affected.iter().copied().collect();
    writer::resolve_imports(&conn, Some(&resolve_ids)).map_err(sql_err)?;
    let defs_after = edges::Definers::load(&conn).map_err(sql_err)?;
    let before = defs_before.snapshot(&candidate_names);
    let after = defs_after.snapshot(&candidate_names);
    let changed_names: BTreeSet<i64> = candidate_names
        .iter()
        .copied()
        .filter(|n| before.get(n) != after.get(n))
        .collect();
    drop(defs_before);
    affected.extend(edges::files_referencing(&conn, &changed_names).map_err(sql_err)?);
    let mut path_of = conn
        .prepare_cached("SELECT path, parse_status FROM files WHERE id = ?1")
        .map_err(sql_err)?;
    for fid in &affected {
        let Some((path, status)) = path_of
            .query_row([fid], |r| {
                Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
            })
            .optional()
            .map_err(sql_err)?
        else {
            continue;
        };
        if !meta::INDEXED_STATUSES.contains(&status.as_str()) {
            continue;
        }
        counters.edges_written +=
            edges::rebuild_file_edges(&conn, &defs_after, *fid, &path).map_err(sql_err)?;
        counters.reresolved += 1;
    }
    drop(path_of);
    let edges_ms = te.elapsed().as_millis() as u64;

    set_meta_common(&conn, scan, cfg, generation).map_err(sql_err)?;
    let coverage = meta::coverage(&conn, cfg).map_err(sql_err)?;
    meta::set(
        &conn,
        "coverage",
        &serde_json::to_string(&coverage).unwrap_or_default(),
    )
    .map_err(sql_err)?;
    meta::set(
        &conn,
        "last_run",
        &serde_json::to_string(&counters).unwrap_or_default(),
    )
    .map_err(sql_err)?;
    meta::set(&conn, meta::DIRTY_FLAG, "0").map_err(sql_err)?;
    conn.execute_batch("COMMIT").map_err(sql_err)?;
    gc_fact_cache(&conn).map_err(sql_err)?;
    drop(conn);
    let timing = Timing {
        elapsed_ms: t0.elapsed().as_millis() as u64,
        scan_ms,
        edges_ms,
    };
    Ok(report(
        ("update", "incremental", "changes"),
        scan,
        dir,
        cfg,
        counters,
        coverage,
        timing,
    ))
}

/// Drop fact-cache entries no file uses, keeping the most recently used superseded ones.
fn gc_fact_cache(conn: &Connection) -> rusqlite::Result<usize> {
    let files: i64 = conn.query_row("SELECT count(*) FROM files", [], |r| r.get(0))?;
    let keep = FACT_CACHE_KEEP_MIN.max(files / 4);
    // live keys are `<blob>:<lang>:<mode>` for either extraction mode.
    conn.execute(
        "DELETE FROM fact_cache
         WHERE content_key NOT IN (
                 SELECT content_hash || ':' || lang || ':ts' FROM files WHERE content_hash != ''
                 UNION ALL
                 SELECT content_hash || ':' || lang || ':lex' FROM files WHERE content_hash != '')
           AND content_key NOT IN (SELECT content_key FROM fact_cache ORDER BY last_used DESC, content_key LIMIT ?1)",
        [keep],
    )
}

/// `index stats`: the stored index's identity, freshness inputs and coverage; with
/// `digest`, its content digest.
pub fn stats(root: &Path, cfg: &IndexConfig, digest: bool) -> Result<serde_json::Value, String> {
    let root = fs::canonicalize(root).map_err(|e| format!("{}: {e}", root.display()))?;
    let git_dir = crate::indexcache::run_git_bounded(
        &root,
        &["rev-parse", "--absolute-git-dir"],
        64 << 10,
        crate::indexcache::git_timeout(),
    )
    .ok()
    .and_then(|b| String::from_utf8(b).ok())
    .map(|s| PathBuf::from(s.trim()));
    let dir = index_dir(&root, git_dir.as_deref(), cfg);
    let path = dir.join(DB_FILE);
    if !path.exists() {
        return Ok(serde_json::json!({
            "command": "stats",
            "exists": false,
            "root": root.to_string_lossy(),
            "index_path": path.to_string_lossy(),
        }));
    }
    let conn =
        Connection::open_with_flags(&path, OpenFlags::SQLITE_OPEN_READ_ONLY).map_err(sql_err)?;
    schema::configure(&conn).map_err(sql_err)?;
    let get = |k: &str| meta::get(&conn, k).ok().flatten().unwrap_or_default();
    let stored_retention =
        ContentRetention::parse(&get("content_retention")).unwrap_or(cfg.content_retention);
    let mut eff = cfg.clone();
    eff.content_retention = stored_retention;
    let rebuild_reason = full_rebuild_reason(&conn, &eff).map_err(sql_err)?;
    let coverage = meta::coverage(&conn, &eff).map_err(sql_err)?;
    let db_bytes = fs::metadata(&path).map(|m| m.len()).unwrap_or(0);
    let fact_cache_entries: i64 = conn
        .query_row("SELECT count(*) FROM fact_cache", [], |r| r.get(0))
        .map_err(sql_err)?;
    let mut v = serde_json::json!({
        "command": "stats",
        "exists": true,
        "root": root.to_string_lossy(),
        "index_path": path.to_string_lossy(),
        "db_bytes": db_bytes,
        "repo_mode": get("repo_mode"),
        "last_commit": get("last_commit"),
        "schema_version": get("schema_version"),
        "schema_fingerprint": get("schema_fingerprint"),
        "analyzer_version": get("analyzer_version"),
        "analyzer_current": get("analyzer_version") == analyzer_version(),
        "content_retention": get("content_retention"),
        "incremental_in_progress": get(meta::DIRTY_FLAG) == "1",
        "needs_full_rebuild": rebuild_reason,
        "fact_cache_entries": fact_cache_entries,
        "last_run": serde_json::from_str::<serde_json::Value>(&get("last_run")).unwrap_or(serde_json::Value::Null),
        "coverage": coverage,
    });
    if digest {
        // the digest uses a temporary FTS5 vocabulary table: open a writable handle
        // (the temp schema only; the index itself is not modified).
        let rw = Connection::open(&path).map_err(sql_err)?;
        schema::configure(&rw).map_err(sql_err)?;
        let d = meta::content_digest(&rw).map_err(sql_err)?;
        v["content_digest"] = serde_json::Value::String(d);
    }
    Ok(v)
}

/// `xmustard-core index <build|update|stats> <root> [flags]`; returns the exit code.
pub fn run_cli(args: impl Iterator<Item = String>) -> i32 {
    let usage = "usage: xmustard-core index <build|update|stats> <root> \
        [--content-retention full|symbol|none] [--max-file-size N] [--max-files N] \
        [--max-symbols N] [--max-total-bytes N] [--index-dir DIR] [--allow-non-git] \
        [--include-untracked] [--no-cache] [--paths P ...] [--digest]";
    let args: Vec<String> = args.collect();
    let (Some(sub), Some(root)) = (args.first(), args.get(1)) else {
        eprintln!("{usage}");
        return 2;
    };
    let root = PathBuf::from(root);
    let mut cfg = match IndexConfig::load(&root) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("index: config: {e}");
            return 2;
        }
    };
    let mut paths: Option<Vec<String>> = None;
    let mut digest = false;
    let mut i = 2;
    while i < args.len() {
        let a = args[i].as_str();
        if a == "--paths" {
            let mut list = Vec::new();
            i += 1;
            while i < args.len() && !args[i].starts_with("--") {
                list.push(args[i].clone());
                i += 1;
            }
            paths = Some(list);
            continue;
        }
        if a == "--digest" {
            digest = true;
            i += 1;
            continue;
        }
        if cfg.apply_switch(a) {
            i += 1;
            continue;
        }
        match cfg.apply_flag(a, args.get(i + 1).map(String::as_str)) {
            Ok(true) => i += 2,
            Ok(false) => {
                eprintln!("index: unknown flag {a}\n{usage}");
                return 2;
            }
            Err(e) => {
                eprintln!("index: {e}");
                return 2;
            }
        }
    }
    let out = match sub.as_str() {
        "build" => {
            build(&root, &cfg).and_then(|r| serde_json::to_value(r).map_err(|e| e.to_string()))
        }
        "update" => update(&root, &cfg, paths.as_deref())
            .and_then(|r| serde_json::to_value(r).map_err(|e| e.to_string())),
        "stats" => stats(&root, &cfg, digest),
        _ => {
            eprintln!("{usage}");
            return 2;
        }
    };
    match out {
        Ok(v) => {
            println!("{v}");
            0
        }
        Err(e) => {
            eprintln!("index {sub} failed: {e}");
            1
        }
    }
}
