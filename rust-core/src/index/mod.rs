//! Per-workspace code index store (PAR-STORE-03) and its transient worker
//! (`xmustard-core index build|update|stats|impact`, PAR-RT-02).
//!
//! One SQLite file, `<git-dir>/xmustard-cache/index-v3/<scope>/index.db`, holds files,
//! symbols with nested qualified names and stable UIDs, references (never from comments
//! or string literals), imports, function-aligned chunks with contentless FTS5 postings,
//! lexical file edges, resolved Go and TS/JS symbol edges with their drop counters (see
//! `resolve`), the content-addressed per-file fact cache and meta.
//!
//! The worker takes the index lock, then scans and streams one file at a time on one
//! thread: read, parse, extract, drop the tree, write the rows in batched transactions.
//! A full build writes a fresh file, fsyncs it and swaps it in; an incremental update
//! rewrites only changed files in place under the `incremental_in_progress` dirty flag
//! and re-resolves edges only for changed files and the dependents of changed exports:
//! files naming an added, removed or changed declaration, importers of changed
//! re-exports, and files whose edges pointed at a symbol that is gone (PAR-FRESH-04).
//! Symbol edges into a rewritten file are carried over by UID. A leftover dirty flag, a
//! schema, analyzer or configuration change, a corrupt store, or a write set above 50%
//! of the files and at least 50 files forces a full rebuild.
//!
//! The declared scale envelope (PAR-RT-09) bounds files, bytes and symbols; beyond it
//! the index is partial and says so in `coverage`, never by growing the worker. Which
//! files and symbols are kept is a pure function of the tree in path order (see
//! `scan` for files and bytes, `plan_symbol_budget` for symbols), so an index past the
//! envelope still exits early when nothing changed and updates the rest incrementally.
//!
//! Readers: the full build renames a new file over `index.db` (after removing the old
//! WAL and SHM by name). A reader that keeps a connection open across a rebuild keeps
//! reading the old, unlinked file; long-lived readers must reopen when `meta`
//! `generation` changes, and must never write. Every writer holds the index lock.
//!
//! The query side (WS-14) reads the graph segment (`csr`), which every build and update
//! writes for its generation before the generation becomes visible (meta
//! `graph_segment` names it); `reader` holds segments as snapshots and `envelope`
//! reports their freshness.

pub mod chunks;
pub mod config;
pub mod csr;
pub mod edges;
pub mod envelope;
pub mod extract;
pub mod facts;
pub mod ignore;
pub mod impact;
pub mod lang;
pub mod lexical;
pub mod meta;
pub mod names;
pub mod reader;
pub mod resolve;
pub mod scan;
pub mod schema;
pub mod uid;
pub mod writer;

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
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
/// Prefix of stored stat keys taken inside the racy window (see `stat_key_for`).
const RACY_PREFIX: &str = "racy:";
/// The loss status of a readable file the byte budget left out.
const ENVELOPE_BYTES: &str = "envelope_bytes";
/// Prefix of store errors that mean the file is damaged (a full rebuild replaces it).
const CORRUPT_ERROR: &str = "index store corrupt";

/// Analyzer identity: extraction rules and grammar versions. A change forces a full
/// rebuild and invalidates the fact cache.
pub fn analyzer_version() -> String {
    format!(
        "xm-analyzer-1;{};resolver-{}",
        extract::grammar_versions(),
        resolve::RESOLVER_REVISION
    )
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
    /// Unchanged files rewritten because the symbol-budget boundary moved over them.
    pub rebudgeted: usize,
    /// Files whose outgoing edges were recomputed.
    pub reresolved: usize,
    /// The update's write set crossed the escalation gate and ran as a full rebuild.
    pub escalated: bool,
    pub bytes_read: u64,
    pub edges_written: usize,
    /// Scope resolver totals: symbol edges by tier and drops by cause.
    pub resolve: resolve::counters::ResolveCounters,
}

#[derive(Debug, Clone, Serialize, Default)]
pub struct Timing {
    pub elapsed_ms: u64,
    pub scan_ms: u64,
    pub edges_ms: u64,
    /// Writing the graph segment the resident service reads (WS-14).
    pub segment_ms: u64,
}

#[derive(Debug, Clone, Serialize)]
pub struct IndexReport {
    /// `build` | `update`
    pub command: String,
    /// `full` | `incremental` | `noop`
    pub mode: String,
    /// Why this mode ran: `requested`, `no_index`, `schema_changed`, `analyzer_changed`,
    /// `retention_changed`, `config_changed`, `dirty_flag`, `escalated`,
    /// `unreadable_index`, `corrupt_index`, `changes` or `unchanged`.
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

/// Per-user cache directory for indexes of non-Git directories: `$XDG_CACHE_HOME`,
/// `~/Library/Caches` (macOS) or `~/.cache`; never the shared temp directory unless no
/// home is known, and then a per-user subdirectory of it.
fn user_cache_dir() -> PathBuf {
    let env = |k: &str| {
        std::env::var_os(k)
            .map(PathBuf::from)
            .filter(|p| p.is_absolute())
    };
    if let Some(x) = env("XDG_CACHE_HOME") {
        return x;
    }
    if let Some(home) = env("HOME") {
        return if cfg!(target_os = "macos") {
            home.join("Library").join("Caches")
        } else {
            home.join(".cache")
        };
    }
    #[cfg(unix)]
    let who = rustix::process::geteuid().as_raw().to_string();
    #[cfg(not(unix))]
    let who = "user".to_string();
    std::env::temp_dir().join(format!("xmustard-{who}"))
}

/// The directory holding `index.db` for an index root.
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
        None => user_cache_dir()
            .join("xmustard")
            .join(INDEX_DIR_NAME)
            .join(scope),
    }
}

/// Create `dir` (and missing parents) readable by its owner only, and refuse a
/// directory that is a symlink or belongs to another user: the index holds identifiers
/// and postings of the source, and with `full` retention the source itself. An existing
/// directory the operator named (`operator_named`: `--index-dir`, `XMUSTARD_INDEX_DIR`)
/// keeps its mode, with a warning when others can list it (the index files themselves
/// are always owner-only); the default per-scope directory is ours and is tightened.
fn ensure_private_dir(dir: &Path, operator_named: bool) -> Result<(), String> {
    let err = |e: std::io::Error| format!("{}: {e}", dir.display());
    let existed = fs::symlink_metadata(dir).is_ok();
    let mut b = fs::DirBuilder::new();
    b.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        b.mode(0o700);
    }
    b.create(dir).map_err(err)?;
    let meta = fs::symlink_metadata(dir).map_err(err)?;
    if meta.file_type().is_symlink() || !meta.is_dir() {
        return Err(format!(
            "{}: index directory is not a plain directory",
            dir.display()
        ));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        if meta.uid() != rustix::process::geteuid().as_raw() {
            return Err(format!(
                "{}: index directory belongs to another user",
                dir.display()
            ));
        }
        if meta.mode() & 0o077 != 0 {
            if existed && operator_named {
                eprintln!(
                    "index: warning: {} is accessible to other users (mode {:o}); the \
                     index files in it are owner-only",
                    dir.display(),
                    meta.mode() & 0o777
                );
            } else {
                fs::set_permissions(dir, fs::Permissions::from_mode(0o700)).map_err(err)?;
            }
        }
    }
    Ok(())
}

/// Open (creating when missing) a file of the index directory with owner-only access.
fn open_private_file(path: &Path, truncate: bool) -> Result<fs::File, String> {
    let mut o = fs::OpenOptions::new();
    o.create(true).write(true).truncate(truncate);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    o.open(path).map_err(|e| format!("{}: {e}", path.display()))
}

/// Advisory lock serializing index writers of one directory across processes.
struct IndexLock {
    _file: fs::File,
}

fn lock(dir: &Path, operator_named: bool) -> Result<IndexLock, String> {
    ensure_private_dir(dir, operator_named)?;
    let path = dir.join("index.lock");
    let file = open_private_file(&path, false)?;
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

fn is_corrupt(e: &rusqlite::Error) -> bool {
    matches!(
        e,
        rusqlite::Error::SqliteFailure(f, _)
            if matches!(
                f.code,
                rusqlite::ErrorCode::DatabaseCorrupt | rusqlite::ErrorCode::NotADatabase
            )
    )
}

fn sql_err(e: rusqlite::Error) -> String {
    if is_corrupt(&e) {
        format!("{CORRUPT_ERROR}: {e}")
    } else {
        format!("index store: {e}")
    }
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

struct Processed {
    rec: FileRecord,
    facts: Option<FileFacts>,
    bytes: Option<Vec<u8>>,
}

/// The stat key stored for a file scanned at `started_ns`. A file modified shortly
/// before the scan may be modified again without changing its timestamp on a coarse
/// filesystem (Git's "racily clean" case), so its key is marked racy and is never
/// trusted: the next update hashes the file, and stores a trusted key once its mtime is
/// safely in the past. The window covers the coarsest common timestamp granularity
/// (FAT's 2 s: a stored mtime of `m` hides rewrites until `m + 2 s`, and the file was
/// read after `started_ns`) with a second of margin.
fn stat_key_for(stat: &scan::StatKey, started_ns: i64) -> String {
    if stat.mtime_ns >= started_ns - RACY_WINDOW_NS {
        format!("{RACY_PREFIX}{}", stat.encode())
    } else {
        stat.encode()
    }
}

/// How long before a scan a modification makes a stat key racy (see `stat_key_for`).
const RACY_WINDOW_NS: i64 = 3_000_000_000;

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
        fact_symbols: 0,
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

/// Apply the symbol-budget decision to a processed file.
fn apply_budget(p: &mut Processed, keep: bool) {
    if let Some(f) = p.facts.as_mut()
        && !keep
    {
        strip_symbols(f);
        p.rec.flags |= file_flag::SYMBOL_BUDGET;
    }
}

/// Read, identify and extract one candidate. The file's tree and text are dropped
/// before this returns; only its facts and bytes go to the writer. `rec.fact_symbols`
/// is the extracted symbol count (the budget is applied by the caller).
fn process(
    root: &Path,
    started_ns: i64,
    cand: &Candidate,
    cfg: &IndexConfig,
    cache: &FactCache<'_>,
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
    // Facts, chunk ranges and postings all refer to the decoded text: for invalid UTF-8
    // that is the lossy decoding, so the writer slices the same bytes extraction saw.
    let (text, invalid) = match String::from_utf8(bytes) {
        Ok(t) => (t, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    };
    let mode = extract::extraction_mode(cand.lang, text.as_bytes(), cfg.max_parse_bytes);
    let key = format!("{}:{}:{mode}", rec.content_hash, cand.lang.name());
    let facts = match cache.get(&key).map_err(sql_err)? {
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
    rec.parse_status = facts.engine.clone();
    rec.fact_symbols = facts.symbols.len() as i64;
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
    meta::set(conn, "repo_mode", scan.repo_mode)?;
    meta::set(conn, "root", &scan.root.to_string_lossy())?;
    meta::set(conn, "generation", &generation.to_string())?;
    set_scan_meta(conn, scan)?;
    Ok(())
}

/// Meta derived from the scan alone (files without rows, the ignore outcome, HEAD).
/// Written only when it differs, so a no-change update writes nothing.
fn set_scan_meta(conn: &Connection, scan: &Scan) -> rusqlite::Result<()> {
    let beyond = serde_json::to_string(&scan.beyond).unwrap_or_default();
    let sample = serde_json::to_string(&scan.beyond_sample).unwrap_or_default();
    for (k, v) in [
        ("last_commit", scan.head.clone()),
        ("ignored_files", scan.ignored.to_string()),
        (
            "ignore_rules_dropped",
            scan.ignore_rules_dropped.to_string(),
        ),
        ("invalid_paths", scan.invalid_paths.to_string()),
        ("worktree_deleted", scan.worktree_deleted.to_string()),
        (meta::BEYOND, beyond),
        (meta::BEYOND_SAMPLE, sample),
    ] {
        if meta::get(conn, k)?.as_deref() != Some(v.as_str()) {
            meta::set(conn, k, &v)?;
        }
    }
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

/// Resolve the index directory and take its lock, then scan: a writer that waited for
/// the lock scans the tree as it is after the previous writer finished.
fn locked_scan(root: &Path, cfg: &IndexConfig) -> Result<(Scan, PathBuf, IndexLock, u64), String> {
    let layout = scan::layout(root, cfg)?;
    let dir = index_dir(&layout.root, layout.git_dir.as_deref(), cfg);
    let lock = lock(&dir, cfg.index_dir.is_some())?;
    let t = Instant::now();
    let scan = scan::scan(&layout, cfg)?;
    Ok((scan, dir, lock, t.elapsed().as_millis() as u64))
}

/// `index build`: a full rebuild into a fresh file, swapped in atomically.
pub fn build(root: &Path, cfg: &IndexConfig) -> Result<IndexReport, String> {
    let t0 = Instant::now();
    let (scan, dir, _lock, scan_ms) = locked_scan(root, cfg)?;
    full_build(
        scan,
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
    mut scan: Scan,
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
    // owner-only from the start: SQLite gives the WAL and SHM the database file's mode.
    drop(open_private_file(&tmp_path, true)?);
    let old = if cfg.use_fact_cache {
        open_compatible(&final_path, cfg)
    } else {
        None
    };
    let generation = previous_generation(&final_path) + 1;
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
        // the symbol budget in path order (the same rule as `plan_symbol_budget`).
        let mut symbols = 0usize;
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        for (i, cand) in scan.candidates.iter().enumerate() {
            counters.files_scanned += 1;
            let mut p = process(
                &scan.root,
                scan.started_ns,
                cand,
                cfg,
                &cache,
                &mut counters,
            )?;
            if p.facts.is_some() {
                let n = p.rec.fact_symbols as usize;
                let keep = symbols + n <= cfg.max_symbols;
                if keep {
                    symbols += n;
                }
                apply_budget(&mut p, keep);
            }
            w.write_file(None, &p.rec, p.facts.as_ref(), p.bytes.as_deref())
                .map_err(sql_err)?;
            counters.written += 1;
            drop(p);
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
        conn.execute_batch("COMMIT").map_err(sql_err)?;
        // the edge and FTS merge phases set the build's peak: free what the file loop
        // no longer needs first.
        drop(old);
        drop(w);
        scan.candidates = Vec::new();
        schema::create_indexes(&conn).map_err(sql_err)?;
        let te = Instant::now();
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        writer::resolve_imports(&conn, None).map_err(sql_err)?;
        let defs = edges::Definers::load(&conn).map_err(sql_err)?;
        let files = indexed_files(&conn).map_err(sql_err)?;
        let mut resolver = resolve::Resolver::new(&conn, &scan.root);
        for (i, (fid, path)) in files.iter().enumerate() {
            counters.edges_written +=
                edges::rebuild_file_edges(&conn, &defs, *fid, path).map_err(sql_err)?;
            counters.edges_written += resolver.resolve_file(*fid).map_err(sql_err)?;
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
        counters.reresolved = files.len();
        counters.resolve = std::mem::take(&mut resolver.counters);
        drop(resolver);
        drop(defs);
        edges_ms = te.elapsed().as_millis() as u64;
        set_meta_common(&conn, &scan, cfg, generation).map_err(sql_err)?;
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
        conn.close().map_err(|(_, e)| sql_err(e))?;
    }
    // The graph segment goes in before the file is published, from a fresh connection:
    // the build's connection memory is free by then, so the two phases do not stack.
    let (segment, segment_ms) = {
        let conn = Connection::open(&tmp_path).map_err(sql_err)?;
        schema::configure_bulk(&conn).map_err(sql_err)?;
        let written = write_graph_segment(&conn, dir)?;
        // the finished file runs in WAL mode so readers keep a snapshot during updates.
        conn.execute_batch("PRAGMA journal_mode=WAL;")
            .map_err(sql_err)?;
        conn.close().map_err(|(_, e)| sql_err(e))?;
        written
    };
    // The bulk load ran without journal or fsync: make the file durable before the
    // rename can publish it (sync_all is F_FULLFSYNC on macOS).
    fs::File::open(&tmp_path)
        .and_then(|f| f.sync_all())
        .map_err(|e| format!("sync {}: {e}", tmp_path.display()))?;
    swap_into_place(&tmp_path, &final_path)?;
    csr::sweep_segments(dir, &segment);
    let timing = Timing {
        elapsed_ms: t0.elapsed().as_millis() as u64,
        scan_ms,
        edges_ms,
        segment_ms,
    };
    Ok(report(
        (command, "full", reason),
        &scan,
        dir,
        cfg,
        counters,
        coverage,
        timing,
    ))
}

/// The stored generation of an existing store, compatible or not: generations only grow
/// within one store directory, so a reader never takes a rebuilt store's state for an
/// older one it has seen.
fn previous_generation(path: &Path) -> i64 {
    if !path.exists() {
        return 0;
    }
    Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY)
        .ok()
        .and_then(|c| meta::get(&c, "generation").ok().flatten())
        .and_then(|g| g.parse().ok())
        .unwrap_or(0)
}

/// Write the graph segment of the state `conn` sees (inside its open transaction, if
/// any) and point meta at it. Returns the segment's name and the milliseconds taken.
fn write_graph_segment(conn: &Connection, dir: &Path) -> Result<(String, u64), String> {
    let t = Instant::now();
    let name = csr::write_segment(conn, dir)?;
    meta::set(conn, meta::GRAPH_SEGMENT, &name).map_err(sql_err)?;
    Ok((name, t.elapsed().as_millis() as u64))
}

/// Write the stored generation's segment when meta names none or it no longer opens (a
/// store from before WS-14, a removed file). The caller holds the writer lock.
fn ensure_segment_locked(conn: &Connection, dir: &Path) -> Result<u64, String> {
    let generation: i64 = meta::get(conn, "generation")
        .map_err(sql_err)?
        .and_then(|g| g.parse().ok())
        .unwrap_or(0);
    let current = meta::get(conn, meta::GRAPH_SEGMENT)
        .map_err(sql_err)?
        .and_then(|n| csr::Segment::open(&dir.join(n), csr::GraphStorage::File).ok())
        .is_some_and(|seg| seg.footer.generation == generation);
    if current {
        return Ok(0);
    }
    let (name, ms) = write_graph_segment(conn, dir)?;
    csr::sweep_segments(dir, &name);
    Ok(ms)
}

/// Make sure the store at `db` has the graph segment of its generation (the writer
/// side of `reader::load_segment`), under the writer lock.
pub fn ensure_graph_segment(db: &Path) -> Result<(), String> {
    let dir = db.parent().ok_or("index store has no directory")?;
    let _lock = lock(dir, true)?;
    let conn = Connection::open(db).map_err(sql_err)?;
    schema::configure(&conn).map_err(sql_err)?;
    ensure_segment_locked(&conn, dir).map(drop)
}

/// Replace `final_path` with the finished (fsynced) build. The previous file is
/// checkpointed and its WAL/SHM removed first, so the new file never meets a stale WAL;
/// the directory is fsynced after the rename.
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
    if let Some(dir) = final_path.parent() {
        fs::File::open(dir)
            .and_then(|d| d.sync_all())
            .map_err(|e| format!("sync {}: {e}", dir.display()))?;
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
    Ok(None)
}

struct Existing {
    id: i64,
    stat_key: String,
    content_hash: String,
    parse_status: String,
    lang: String,
    flags: i64,
    fact_symbols: i64,
}

/// `index update [--paths ...]`: bring the index to the working tree, incrementally
/// when possible. With `paths` (a watcher's batch), only those files are read for
/// changes; files the envelope admits or drops and rows the scan no longer lists are
/// handled either way, so the result equals a full build whenever the batch names every
/// changed file.
pub fn update(
    root: &Path,
    cfg: &IndexConfig,
    paths: Option<&[String]>,
) -> Result<IndexReport, String> {
    let t0 = Instant::now();
    let (scan, dir, _lock, scan_ms) = locked_scan(root, cfg)?;
    let final_path = dir.join(DB_FILE);
    let full = |scan: Scan, reason: &str, counters: Counters| {
        full_build(scan, cfg, &dir, "update", reason, counters, t0, scan_ms)
    };
    if !final_path.exists() {
        return full(scan, "no_index", Counters::default());
    }
    let conn = match Connection::open(&final_path) {
        Ok(c) => c,
        Err(_) => return full(scan, "unreadable_index", Counters::default()),
    };
    let reason =
        match schema::configure_durable(&conn).and_then(|_| full_rebuild_reason(&conn, cfg)) {
            Ok(r) => r,
            Err(e) if is_corrupt(&e) => Some("corrupt_index"),
            Err(_) => Some("unreadable_index"),
        };
    if let Some(reason) = reason {
        drop(conn);
        return full(scan, reason, Counters::default());
    }
    // a damaged page found mid-update (the dirty flag is still set) is replaced by a
    // full rebuild rather than failing every later update.
    match incremental(conn, &scan, cfg, &dir, paths, t0, scan_ms) {
        Ok(Step::Done(r)) => Ok(*r),
        Ok(Step::Rebuild(reason, counters)) => full(scan, reason, counters),
        Err(e) if e.starts_with(CORRUPT_ERROR) => full(scan, "corrupt_index", Counters::default()),
        Err(e) => Err(e),
    }
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

/// Outcome of the in-place path: finished, or a full rebuild is needed.
enum Step {
    Done(Box<IndexReport>),
    Rebuild(&'static str, Counters),
}

/// One file in the symbol-budget plan.
struct PlanEntry {
    /// Symbols extraction found (before the budget).
    symbols: usize,
    /// The file's content is indexed (it can hold symbols at all).
    indexed: bool,
    /// Stored decision for a file not being rewritten: kept its symbols.
    stored_keep: Option<bool>,
}

/// The symbol budget in path order: a file keeps its symbols when they fit the budget
/// left by the files before it, otherwise its symbols are dropped (`symbol_budget`).
/// Returns the keep decision per path. Full builds apply the same rule inline.
fn plan_symbol_budget<'p>(
    entries: &BTreeMap<&'p str, PlanEntry>,
    max_symbols: usize,
) -> HashMap<&'p str, bool> {
    let mut total = 0usize;
    let mut out = HashMap::with_capacity(entries.len());
    for (path, e) in entries {
        if !e.indexed {
            continue;
        }
        let keep = total + e.symbols <= max_symbols;
        if keep {
            total += e.symbols;
        }
        out.insert(*path, keep);
    }
    out
}

/// Read and extract `cand` (or take its facts from the cache) for its symbol count only;
/// None when it cannot be indexed. The facts go to the cache, so writing the file later
/// costs a read but no second parse (unless `--no-cache` disabled reuse).
fn symbol_count(
    root: &Path,
    cand: &Candidate,
    cfg: &IndexConfig,
    cache: &FactCache<'_>,
    counters: &mut Counters,
) -> Result<Option<usize>, String> {
    if cand.loss.is_some() {
        return Ok(None);
    }
    let Ok(bytes) =
        crate::symbolgraph::read_source_beneath(root, Path::new(&cand.path), cfg.max_file_size)
    else {
        return Ok(None);
    };
    counters.bytes_read += bytes.len() as u64;
    let blob = scan::blob_id(&bytes);
    let (text, invalid) = match String::from_utf8(bytes) {
        Ok(t) => (t, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    };
    let mode = extract::extraction_mode(cand.lang, text.as_bytes(), cfg.max_parse_bytes);
    let key = format!("{blob}:{}:{mode}", cand.lang.name());
    if let Some(f) = cache.get(&key).map_err(sql_err)? {
        counters.reused_from_cache += 1;
        return Ok(Some(f.symbols.len()));
    }
    let f = extract::extract(cand.lang, &text, invalid, cfg.max_parse_bytes);
    counters.reparsed += 1;
    cache.put(&key, &f).map_err(sql_err)?;
    Ok(Some(f.symbols.len()))
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
) -> Result<Step, String> {
    let mut counters = Counters::default();
    let filter = paths.map(|p| normalize_paths(&scan.root, p));
    let considered = |p: &str| filter.as_ref().is_none_or(|f| f.contains(p));

    // every stored row: bounded by the scan's row bound (max_files + MAX_LOSS_ROWS).
    let mut existing: HashMap<String, Existing> = HashMap::new();
    {
        let mut st = conn
            .prepare(
                "SELECT id, path, content_hash, parse_status, lang, stat_key, flags, fact_symbols
                 FROM files",
            )
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
                        flags: r.get(6)?,
                        fact_symbols: r.get(7)?,
                    },
                ))
            })
            .map_err(sql_err)?;
        for row in rows {
            let (p, e) = row.map_err(sql_err)?;
            existing.insert(p, e);
        }
    }
    let db_total = existing.len();

    // ---- classify without extracting: trusted unchanged stat keys skip the read; other
    // files are identified by hashing (or by the same read failure as before). Envelope
    // decisions depend on the files before this one, not on its own stat, so a file that
    // enters or leaves the kept set, or the byte budget, is re-evaluated even outside a
    // watcher batch. ----
    let mut changed: Vec<&Candidate> = Vec::new();
    // same bytes under a new stat key (touched, checked out again): refresh the key.
    let mut restat: Vec<(i64, String)> = Vec::new();
    let mut seen: HashSet<&str> = HashSet::with_capacity(scan.candidates.len());
    for cand in &scan.candidates {
        seen.insert(cand.path.as_str());
        let ex = existing.get(&cand.path);
        let envelope_moved = ex.is_none_or(|e| {
            (e.parse_status == ENVELOPE_BYTES) != (cand.loss == Some(ENVELOPE_BYTES))
        });
        if !envelope_moved && !considered(&cand.path) {
            continue;
        }
        counters.files_scanned += 1;
        let Some(ex) = ex else {
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
        if ex.parse_status == ENVELOPE_BYTES {
            // back inside the byte budget with the same stat key (the other pre-read
            // losses follow the file's own stat, and read failures share their names)
            changed.push(cand);
            continue;
        }
        let key = stat_key_for(&cand.stat, scan.started_ns);
        if !ex.stat_key.starts_with(RACY_PREFIX) && key == ex.stat_key {
            counters.unchanged += 1;
            continue;
        }
        // stat changed or racy: identify by content, or by the same read failure
        let indexed = meta::INDEXED_STATUSES.contains(&ex.parse_status.as_str());
        let same = match crate::symbolgraph::read_source_beneath(
            &scan.root,
            Path::new(&cand.path),
            cfg.max_file_size,
        ) {
            Ok(b) => {
                counters.bytes_read += b.len() as u64;
                indexed && scan::blob_id(&b) == ex.content_hash
            }
            Err(f) => !indexed && f.reason() == ex.parse_status,
        };
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
    // a row the scan no longer lists is gone from the tree or past the envelope; either
    // way it goes, batch or not.
    let mut deleted: Vec<(String, i64)> = existing
        .iter()
        .filter(|(p, _)| !seen.contains(p.as_str()))
        .map(|(p, e)| (p.clone(), e.id))
        .collect();
    deleted.sort();

    if changed.is_empty() && deleted.is_empty() {
        set_scan_meta(&conn, scan).map_err(sql_err)?;
        let segment_ms = ensure_segment_locked(&conn, dir)?;
        let coverage = meta::coverage(&conn, cfg).map_err(sql_err)?;
        let timing = Timing {
            elapsed_ms: t0.elapsed().as_millis() as u64,
            scan_ms,
            edges_ms: 0,
            segment_ms,
        };
        return Ok(Step::Done(Box::new(report(
            ("update", "noop", "unchanged"),
            scan,
            dir,
            cfg,
            counters,
            coverage,
            timing,
        ))));
    }

    let generation = meta::get(&conn, "generation")
        .map_err(sql_err)?
        .and_then(|g| g.parse::<i64>().ok())
        .unwrap_or(0)
        + 1;
    let cache = FactCache {
        conn: &conn,
        old: None,
        generation,
        reuse: cfg.use_fact_cache,
    };
    // files the budget plan already read and extracted (counted there, not again).
    let mut precounted: HashSet<&str> = HashSet::new();

    // ---- symbol budget: which files keep their symbols after this change. Needed only
    // when the budget can bind: some file is already over it, or the changed files could
    // push the total past it. ----
    let changed_paths: HashSet<&str> = changed.iter().map(|c| c.path.as_str()).collect();
    let deleted_paths: HashSet<&str> = deleted.iter().map(|(p, _)| p.as_str()).collect();
    let retained = existing.iter().filter(|(p, _)| {
        !changed_paths.contains(p.as_str()) && !deleted_paths.contains(p.as_str())
    });
    let (mut stored_total, mut any_stripped) = (0usize, false);
    for (_, e) in retained.clone() {
        if meta::INDEXED_STATUSES.contains(&e.parse_status.as_str()) {
            stored_total += e.fact_symbols.max(0) as usize;
            any_stripped |= e.flags & file_flag::SYMBOL_BUDGET != 0;
        }
    }
    let worst_case = changed
        .len()
        .saturating_mul(extract::MAX_SYMBOLS_PER_FILE)
        .saturating_add(stored_total);
    let mut keep_of: HashMap<String, bool> = HashMap::new();
    let mut rebudget: Vec<&Candidate> = Vec::new();
    if any_stripped || worst_case > cfg.max_symbols {
        let mut entries: BTreeMap<&str, PlanEntry> = BTreeMap::new();
        for (p, e) in retained {
            entries.insert(
                p.as_str(),
                PlanEntry {
                    symbols: e.fact_symbols.max(0) as usize,
                    indexed: meta::INDEXED_STATUSES.contains(&e.parse_status.as_str()),
                    stored_keep: Some(e.flags & file_flag::SYMBOL_BUDGET == 0),
                },
            );
        }
        for cand in &changed {
            let n = symbol_count(&scan.root, cand, cfg, &cache, &mut counters)?;
            precounted.insert(cand.path.as_str());
            entries.insert(
                cand.path.as_str(),
                PlanEntry {
                    symbols: n.unwrap_or(0),
                    indexed: n.is_some(),
                    stored_keep: None,
                },
            );
        }
        let plan = plan_symbol_budget(&entries, cfg.max_symbols);
        let cand_of: HashMap<&str, &Candidate> = scan
            .candidates
            .iter()
            .map(|c| (c.path.as_str(), c))
            .collect();
        for (path, e) in &entries {
            let keep = plan.get(path).copied().unwrap_or(true);
            match e.stored_keep {
                None => {
                    keep_of.insert(path.to_string(), keep);
                }
                Some(stored) if stored != keep && e.symbols > 0 => {
                    // the boundary moved over an unchanged file: rewrite it too.
                    if let Some(c) = cand_of.get(path) {
                        keep_of.insert(path.to_string(), keep);
                        rebudget.push(c);
                    }
                }
                Some(_) => {}
            }
        }
    }

    // ---- escalation gate ----
    let write_set = changed.len() + rebudget.len() + deleted.len();
    let total = db_total.max(scan.candidates.len());
    if write_set * 2 > total && write_set >= ESCALATE_MIN_FILES {
        drop(conn);
        let c = Counters {
            escalated: true,
            ..Default::default()
        };
        return Ok(Step::Rebuild("escalated", c));
    }

    // ---- apply in place under the dirty flag ----
    meta::set(&conn, meta::DIRTY_FLAG, "1").map_err(sql_err)?;
    // what other files' edges read from the rewritten files: export signatures (their
    // difference names the dependents) and symbol ids (remapped by UID).
    let mut deps = resolve::Dependents::default();
    let mut orphaned: BTreeSet<i64> = BTreeSet::new();
    let mut touched_paths: Vec<String> = Vec::new();
    let mut changed_ids: Vec<i64> = Vec::new();
    counters.rebudgeted = rebudget.len();
    let mut writes: Vec<&Candidate> = changed.iter().chain(rebudget.iter()).copied().collect();
    writes.sort_by(|a, b| a.path.cmp(&b.path));
    {
        let mut w = Writer::new(&conn, cfg.content_retention).map_err(sql_err)?;
        conn.execute_batch("BEGIN").map_err(sql_err)?;
        let mut into_file = conn
            .prepare_cached(
                "SELECT DISTINCT src_file FROM edges WHERE dst_file = ?1 AND src_file != ?1",
            )
            .map_err(sql_err)?;
        for (path, id) in &deleted {
            let before = resolve::signature(&conn, *id).map_err(sql_err)?;
            deps.note_change(&conn, *id, path, &before, &resolve::Signature::new())
                .map_err(sql_err)?;
            for f in into_file
                .query_map([id], |r| r.get::<_, i64>(0))
                .map_err(sql_err)?
            {
                orphaned.insert(f.map_err(sql_err)?);
            }
            writer::delete_file_rows(&conn, *id, false).map_err(sql_err)?;
            counters.deleted += 1;
            touched_paths.push(path.clone());
        }
        drop(into_file);
        for (i, cand) in writes.iter().enumerate() {
            let prior = existing.get(&cand.path).map(|e| e.id);
            let (before, old_ids) = match prior {
                Some(id) => {
                    let before = resolve::signature(&conn, id).map_err(sql_err)?;
                    let old_ids = resolve::symbol_ids(&conn, id).map_err(sql_err)?;
                    writer::delete_file_rows(&conn, id, true).map_err(sql_err)?;
                    (before, old_ids)
                }
                None => {
                    touched_paths.push(cand.path.clone());
                    (resolve::Signature::new(), Vec::new())
                }
            };
            let mut uncounted = Counters::default();
            let tally = if precounted.contains(cand.path.as_str()) {
                &mut uncounted
            } else {
                &mut counters
            };
            let mut p = process(&scan.root, scan.started_ns, cand, cfg, &cache, tally)?;
            if p.facts.is_some() {
                apply_budget(&mut p, keep_of.get(&cand.path).copied().unwrap_or(true));
            }
            let fid = w
                .write_file(prior, &p.rec, p.facts.as_ref(), p.bytes.as_deref())
                .map_err(sql_err)?;
            counters.written += 1;
            changed_ids.push(fid);
            let after = resolve::signature(&conn, fid).map_err(sql_err)?;
            deps.note_change(&conn, fid, &cand.path, &before, &after)
                .map_err(sql_err)?;
            orphaned.extend(resolve::remap_symbol_ids(&conn, fid, &old_ids).map_err(sql_err)?);
            if (i + 1) % BATCH_FILES == 0 {
                conn.execute_batch("COMMIT; BEGIN").map_err(sql_err)?;
            }
        }
    }

    // ---- re-resolve only the dependents of changed exports: the changed files,
    // importers of added/removed paths, files naming a changed export, files bound
    // through a changed re-export or owning a Go type whose methods changed, and files
    // whose edges pointed at a symbol that is gone ----
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
    affected.extend(edges::files_referencing(&conn, &deps.names).map_err(sql_err)?);
    affected.extend(&deps.files);
    affected.extend(&orphaned);
    let defs_after = edges::Definers::load(&conn).map_err(sql_err)?;
    let mut resolver = resolve::Resolver::new(&conn, &scan.root);
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
        counters.edges_written += resolver.resolve_file(*fid).map_err(sql_err)?;
        counters.reresolved += 1;
    }
    counters.resolve = std::mem::take(&mut resolver.counters);
    drop(resolver);
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
    // the new generation's segment exists before the generation commits
    let (segment, segment_ms) = write_graph_segment(&conn, dir)?;
    conn.execute_batch("COMMIT").map_err(sql_err)?;
    csr::sweep_segments(dir, &segment);
    gc_fact_cache(&conn).map_err(sql_err)?;
    drop(conn);
    let timing = Timing {
        elapsed_ms: t0.elapsed().as_millis() as u64,
        scan_ms,
        edges_ms,
        segment_ms,
    };
    Ok(Step::Done(Box::new(report(
        ("update", "incremental", "changes"),
        scan,
        dir,
        cfg,
        counters,
        coverage,
        timing,
    ))))
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

/// The stored index of `root`, opened read-only (never locked, never written):
/// (canonical root, index path, connection when the file exists).
fn open_read_only(
    root: &Path,
    cfg: &IndexConfig,
) -> Result<(PathBuf, PathBuf, Option<Connection>), String> {
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
    let path = index_dir(&root, git_dir.as_deref(), cfg).join(DB_FILE);
    if !path.exists() {
        return Ok((root, path, None));
    }
    let conn =
        Connection::open_with_flags(&path, OpenFlags::SQLITE_OPEN_READ_ONLY).map_err(sql_err)?;
    schema::configure(&conn).map_err(sql_err)?;
    Ok((root, path, Some(conn)))
}

/// `index impact`: dependents of a symbol with the completeness envelope. Read-only.
pub fn impact(
    root: &Path,
    cfg: &IndexConfig,
    symbol: &str,
    depth: usize,
) -> Result<serde_json::Value, String> {
    let (root, path, conn) = open_read_only(root, cfg)?;
    let Some(conn) = conn else {
        return Err(format!(
            "no index at {} (run `index build` first)",
            path.display()
        ));
    };
    let result = impact::impact(&conn, cfg, symbol, depth).map_err(sql_err)?;
    let mut v = serde_json::to_value(result).map_err(|e| e.to_string())?;
    v["command"] = "impact".into();
    v["root"] = root.to_string_lossy().into();
    Ok(v)
}

/// `index stats`: the stored index's identity, freshness inputs and coverage; with
/// `digest`, its content digest. Read-only: it never takes the writer lock and never
/// opens the store for writing.
pub fn stats(root: &Path, cfg: &IndexConfig, digest: bool) -> Result<serde_json::Value, String> {
    let (root, path, conn) = open_read_only(root, cfg)?;
    let Some(conn) = conn else {
        return Ok(serde_json::json!({
            "command": "stats",
            "exists": false,
            "root": root.to_string_lossy(),
            "index_path": path.to_string_lossy(),
        }));
    };
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
        "generation": get("generation"),
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
        // the FTS5 vocabulary table lives in the connection's temp schema, which a
        // read-only connection may create.
        let d = meta::content_digest(&conn).map_err(sql_err)?;
        v["content_digest"] = serde_json::Value::String(d);
    }
    Ok(v)
}

/// Reverse hops `index impact` walks by default.
const DEFAULT_IMPACT_DEPTH: usize = 4;

const USAGE: &str = "xmustard-core index <build|update|stats|impact> <root> \
    [--content-retention full|symbol|none] [--max-file-size N] [--max-files N] \
    [--max-symbols N] [--max-total-bytes N] [--max-parse-bytes N] [--index-dir DIR] \
    [--allow-non-git] [--include-untracked] [--no-cache] [--paths P ...] [--digest] \
    [--identity-key KEY] [--symbol NAME|QUALIFIED|UID] [--depth N]";

/// Record the repository identity key a build or update was run for (`meta::IDENTITY_KEY`),
/// under the writer lock; written only when it changed.
fn stamp_identity(report: &IndexReport, key: &str, cfg: &IndexConfig) -> Result<(), String> {
    let db = Path::new(&report.index_path);
    let dir = db.parent().ok_or("index store has no directory")?;
    let _lock = lock(dir, cfg.index_dir.is_some())?;
    let conn = Connection::open(db).map_err(sql_err)?;
    schema::configure(&conn).map_err(sql_err)?;
    if meta::get(&conn, meta::IDENTITY_KEY)
        .map_err(sql_err)?
        .as_deref()
        != Some(key)
    {
        meta::set(&conn, meta::IDENTITY_KEY, key).map_err(sql_err)?;
    }
    Ok(())
}

/// `xmustard-core index <build|update|stats> <root> [flags]`: the subcommand-table
/// handler. It returns its JSON or error as a value and never prints.
pub fn run(args: crate::dispatch::Args) -> crate::dispatch::CmdResult {
    use crate::dispatch::{CmdError, Output};

    let args: Vec<String> = args.collect();
    let (Some(sub), Some(root)) = (args.first(), args.get(1)) else {
        return Err(CmdError::usage(USAGE));
    };
    let root = PathBuf::from(root);
    let mut cfg =
        IndexConfig::load(&root).map_err(|e| CmdError::new(2, format!("index: config: {e}")))?;
    let mut paths: Option<Vec<String>> = None;
    let mut digest = false;
    let mut identity: Option<String> = None;
    let mut symbol: Option<String> = None;
    let mut depth = DEFAULT_IMPACT_DEPTH;
    let mut i = 2;
    while i < args.len() {
        let a = args[i].as_str();
        i += match a {
            "--paths" => {
                let list: Vec<String> = args[i + 1..]
                    .iter()
                    .take_while(|p| !p.starts_with("--"))
                    .cloned()
                    .collect();
                let taken = list.len();
                paths = Some(list);
                1 + taken
            }
            "--digest" => {
                digest = true;
                1
            }
            "--identity-key" => {
                identity = args.get(i + 1).cloned();
                2
            }
            "--symbol" => {
                symbol = args.get(i + 1).cloned();
                2
            }
            "--depth" => {
                depth = args
                    .get(i + 1)
                    .and_then(|d| d.parse().ok())
                    .ok_or_else(|| CmdError::new(2, "index: --depth needs a number"))?;
                2
            }
            _ if cfg.apply_switch(a) => 1,
            _ => match cfg.apply_flag(a, args.get(i + 1).map(String::as_str)) {
                Ok(true) => 2,
                Ok(false) => {
                    return Err(CmdError::new(
                        2,
                        format!("index: unknown flag {a}\nusage: {USAGE}"),
                    ));
                }
                Err(e) => return Err(CmdError::new(2, format!("index: {e}"))),
            },
        };
    }
    let to_value = |r: Result<IndexReport, String>| {
        r.and_then(|r| serde_json::to_value(r).map_err(|e| e.to_string()))
    };
    let stamped = |r: Result<IndexReport, String>| match &identity {
        Some(key) => r.and_then(|r| stamp_identity(&r, key, &cfg).map(|()| r)),
        None => r,
    };
    let out = match sub.as_str() {
        "build" => to_value(stamped(build(&root, &cfg))),
        "update" => to_value(stamped(update(&root, &cfg, paths.as_deref()))),
        "stats" => stats(&root, &cfg, digest),
        "impact" => match symbol.as_deref() {
            Some(sym) => impact(&root, &cfg, sym, depth),
            None => return Err(CmdError::usage(USAGE)),
        },
        _ => return Err(CmdError::usage(USAGE)),
    };
    out.map(|v| Output::Json(v.to_string()))
        .map_err(|e| CmdError::failed(format!("index {sub} failed: {e}")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_stat_key_inside_fat_timestamp_granularity_is_racy() {
        let started = 1_000_000_000_000_000_000i64;
        let key = |mtime_ns: i64| {
            stat_key_for(
                &scan::StatKey {
                    size: 1,
                    mtime_ns,
                    ..Default::default()
                },
                started,
            )
        };
        // FAT stores even seconds: a file written at an even second and scanned 1.2 s
        // later can be rewritten at +1.3 s without its mtime changing.
        assert!(key(started - 1_200_000_000).starts_with(RACY_PREFIX));
        assert!(key(started - 2_000_000_000).starts_with(RACY_PREFIX));
        assert!(key(started + 5_000_000_000).starts_with(RACY_PREFIX));
        assert!(!key(started - 3_500_000_000).starts_with(RACY_PREFIX));
    }
}
