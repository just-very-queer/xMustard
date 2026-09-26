//! D-02 probe: SQLite FTS5 (rusqlite, bundled) versus tantivy for code-chunk BM25.
//!
//! Both engines receive identical input: every tracked source file of a Git checkout,
//! cut into declaration-aligned chunks of at most 80 lines, pre-tokenized into
//! lowercase identifiers plus their camelCase/snake_case subtokens. Each phase runs in
//! its own process so the peak RSS belongs to that phase alone.
//!
//!   d02-probe queries      <root> <queries.json> [n]
//!   d02-probe fts5-build   <root> <index.db> [full|column]
//!   d02-probe fts5-query   <index.db> <queries.json> [or|and]
//!   d02-probe tantivy-build <root> <dir> [heap_mb]
//!   d02-probe tantivy-query <dir> <queries.json> [or|and]
//!
//! Queries are known-item: a function/type name defined exactly once in the corpus,
//! queried by its subtokens ("parse config file"); the relevant chunk is the one that
//! holds the definition line. Every phase prints one JSON line.

use std::collections::HashMap;
use std::path::Path;
use std::process::Command;
use std::time::Instant;

const EXTS: &[&str] = &[
    "rs", "go", "ts", "tsx", "js", "jsx", "mjs", "cjs", "py", "java", "rb", "c", "h", "cpp",
    "hpp", "cc",
];
const MAX_CHUNK_LINES: usize = 80;
const MAX_FILE_BYTES: u64 = 8 << 20;

fn maxrss_bytes() -> u64 {
    // SAFETY: getrusage writes into a zeroed struct we own.
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut ru) };
    if cfg!(target_os = "macos") {
        ru.ru_maxrss as u64
    } else {
        ru.ru_maxrss as u64 * 1024
    }
}

fn mib(b: u64) -> f64 {
    b as f64 / (1u64 << 20) as f64
}

fn tracked_sources(root: &Path) -> Vec<String> {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(["ls-files", "-z"])
        .output()
        .expect("git ls-files");
    let mut files: Vec<String> = out
        .stdout
        .split(|b| *b == 0)
        .filter_map(|p| String::from_utf8(p.to_vec()).ok())
        .filter(|p| {
            Path::new(p)
                .extension()
                .and_then(|e| e.to_str())
                .is_some_and(|e| EXTS.contains(&e))
        })
        .collect();
    files.sort();
    files
}

fn read_text(root: &Path, rel: &str) -> Option<String> {
    let p = root.join(rel);
    let meta = std::fs::symlink_metadata(&p).ok()?;
    if !meta.is_file() || meta.len() > MAX_FILE_BYTES {
        return None;
    }
    let bytes = std::fs::read(&p).ok()?;
    Some(String::from_utf8_lossy(&bytes).into_owned())
}

struct Chunk {
    start: usize, // 1-based inclusive
    end: usize,
    body: String,
}

const DECL_WORDS: &[&str] = &[
    "fn", "func", "function", "def", "class", "interface", "type", "struct", "enum", "trait",
    "impl", "mod", "const", "let", "var", "export", "pub", "async", "static", "public",
    "private", "protected",
];

fn starts_declaration(line: &str) -> bool {
    let t = line.trim_start();
    let first = t
        .split(|c: char| !(c.is_alphanumeric() || c == '_'))
        .next()
        .unwrap_or("");
    DECL_WORDS.contains(&first)
}

/// Declaration-aligned chunks: a new chunk starts at a declaration line once the
/// current chunk has at least 5 lines, and every chunk is capped at 80 lines.
fn chunk_file(text: &str) -> Vec<Chunk> {
    let lines: Vec<&str> = text.lines().collect();
    let mut out = Vec::new();
    let mut start = 0usize;
    let mut i = 0usize;
    while i < lines.len() {
        let len = i - start;
        if len >= MAX_CHUNK_LINES || (len >= 5 && starts_declaration(lines[i])) {
            out.push(make_chunk(&lines, start, i));
            start = i;
        }
        i += 1;
    }
    if start < lines.len() {
        out.push(make_chunk(&lines, start, lines.len()));
    }
    out
}

fn make_chunk(lines: &[&str], start: usize, end: usize) -> Chunk {
    let mut body = String::new();
    for l in &lines[start..end] {
        tokenize_into(l, &mut body);
    }
    Chunk {
        start: start + 1,
        end,
        body,
    }
}

/// Split an identifier into camelCase / snake_case / digit-boundary subtokens.
fn subtokens(word: &str) -> Vec<String> {
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

fn tokenize_into(text: &str, out: &mut String) {
    for word in text.split(|c: char| !(c.is_alphanumeric() || c == '_')) {
        if word.is_empty() || word.chars().all(|c| c.is_ascii_digit() || c == '_') {
            continue;
        }
        let lower = word.to_lowercase();
        out.push_str(&lower);
        out.push(' ');
        let subs = subtokens(word);
        if subs.len() > 1 {
            for s in subs {
                out.push_str(&s);
                out.push(' ');
            }
        }
    }
}

fn fnv(s: &str) -> u64 {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in s.bytes() {
        h ^= b as u64;
        h = h.wrapping_mul(0x100000001b3);
    }
    h
}

const DEF_KEYWORDS: &[&str] = &[
    "function", "func", "fn", "def", "class", "interface", "struct", "enum", "trait", "type",
];

fn cmd_queries(root: &Path, out: &Path, n: usize) {
    // name -> (path, line, count)
    let mut defs: HashMap<String, (String, usize, usize)> = HashMap::new();
    for rel in tracked_sources(root) {
        let Some(text) = read_text(root, &rel) else {
            continue;
        };
        for (ln, line) in text.lines().enumerate() {
            let words: Vec<&str> = line
                .split(|c: char| !(c.is_alphanumeric() || c == '_'))
                .filter(|w| !w.is_empty())
                .collect();
            for w in words.windows(2) {
                if !DEF_KEYWORDS.contains(&w[0]) {
                    continue;
                }
                let name = w[1];
                if name.len() < 8 || subtokens(name).len() < 2 {
                    continue;
                }
                let e = defs
                    .entry(name.to_string())
                    .or_insert((rel.clone(), ln + 1, 0));
                e.2 += 1;
            }
        }
    }
    let mut unique: Vec<(String, String, usize)> = defs
        .into_iter()
        .filter(|(_, v)| v.2 == 1)
        .map(|(k, v)| (k, v.0, v.1))
        .collect();
    unique.sort_by_key(|(name, path, _)| (fnv(&format!("{path}:{name}")), name.clone()));
    unique.truncate(n);
    let qs: Vec<serde_json::Value> = unique
        .into_iter()
        .map(|(name, path, line)| {
            serde_json::json!({
                "name": name,
                "query": subtokens(&name).join(" "),
                "path": path,
                "line": line,
            })
        })
        .collect();
    std::fs::write(out, serde_json::to_vec_pretty(&qs).unwrap()).unwrap();
    println!(
        "{}",
        serde_json::json!({"phase": "queries", "count": qs.len()})
    );
}

fn load_queries(p: &Path) -> Vec<(String, String, usize)> {
    let v: Vec<serde_json::Value> = serde_json::from_slice(&std::fs::read(p).unwrap()).unwrap();
    v.into_iter()
        .map(|q| {
            (
                q["query"].as_str().unwrap().to_string(),
                q["path"].as_str().unwrap().to_string(),
                q["line"].as_u64().unwrap() as usize,
            )
        })
        .collect()
}

fn dir_bytes(p: &Path) -> u64 {
    if p.is_file() {
        return std::fs::metadata(p).map(|m| m.len()).unwrap_or(0);
    }
    std::fs::read_dir(p)
        .map(|rd| rd.flatten().map(|e| dir_bytes(&e.path())).sum())
        .unwrap_or(0)
}

struct Stats {
    files: usize,
    chunks: usize,
    token_bytes: u64,
}

fn for_each_chunk(root: &Path, mut f: impl FnMut(&str, &Chunk)) -> Stats {
    let mut st = Stats {
        files: 0,
        chunks: 0,
        token_bytes: 0,
    };
    for rel in tracked_sources(root) {
        let Some(text) = read_text(root, &rel) else {
            continue;
        };
        st.files += 1;
        for c in chunk_file(&text) {
            st.chunks += 1;
            st.token_bytes += c.body.len() as u64;
            f(&rel, &c);
        }
    }
    st
}

fn cmd_fts5_build(root: &Path, db: &Path, detail: &str) {
    let _ = std::fs::remove_file(db);
    let t0 = Instant::now();
    let conn = rusqlite::Connection::open(db).unwrap();
    conn.execute_batch(&format!(
        "PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; PRAGMA cache_size=-2048;
         PRAGMA mmap_size=0; PRAGMA temp_store=FILE;
         CREATE TABLE chunk(id INTEGER PRIMARY KEY, path TEXT NOT NULL, start INTEGER, end INTEGER);
         CREATE VIRTUAL TABLE fts USING fts5(body, content='', contentless_delete=1,
           detail={detail}, tokenize='unicode61 remove_diacritics 0');
         BEGIN;"
    ))
    .unwrap();
    let mut id: i64 = 0;
    let mut in_batch = 0usize;
    let st = {
        let mut ins_meta = conn
            .prepare("INSERT INTO chunk(id, path, start, end) VALUES (?1, ?2, ?3, ?4)")
            .unwrap();
        let mut ins_fts = conn
            .prepare("INSERT INTO fts(rowid, body) VALUES (?1, ?2)")
            .unwrap();
        for_each_chunk(root, |path, c| {
            id += 1;
            ins_meta
                .execute(rusqlite::params![id, path, c.start as i64, c.end as i64])
                .unwrap();
            ins_fts.execute(rusqlite::params![id, c.body]).unwrap();
            in_batch += 1;
            if in_batch >= 2000 {
                conn.execute_batch("COMMIT; BEGIN;").unwrap();
                in_batch = 0;
            }
        })
    };
    conn.execute_batch("COMMIT; INSERT INTO fts(fts) VALUES('optimize');")
        .unwrap();
    drop(conn);
    println!(
        "{}",
        serde_json::json!({
            "phase": "fts5-build", "detail": detail, "files": st.files, "chunks": st.chunks,
            "token_mib": mib(st.token_bytes), "elapsed_ms": t0.elapsed().as_millis() as u64,
            "disk_mib": mib(dir_bytes(db)), "maxrss_mib": mib(maxrss_bytes()),
        })
    );
}

fn fts_match_expr(q: &str, op: &str) -> String {
    q.split_whitespace()
        .map(|t| format!("\"{}\"", t.replace('"', "")))
        .collect::<Vec<_>>()
        .join(if op == "and" { " AND " } else { " OR " })
}

struct Eval {
    rr_sum: f64,
    hits10: usize,
    n: usize,
    lat_us: Vec<u128>,
    top: Vec<Vec<String>>,
}

impl Eval {
    fn new() -> Self {
        Eval {
            rr_sum: 0.0,
            hits10: 0,
            n: 0,
            lat_us: Vec::new(),
            top: Vec::new(),
        }
    }
    fn record(&mut self, results: &[(String, usize, usize)], path: &str, line: usize, us: u128) {
        self.n += 1;
        self.lat_us.push(us);
        if let Some(rank) = results
            .iter()
            .position(|(p, s, e)| p == path && *s <= line && line <= *e)
        {
            self.rr_sum += 1.0 / (rank as f64 + 1.0);
            self.hits10 += 1;
        }
        self.top.push(
            results
                .iter()
                .map(|(p, s, _)| format!("{p}:{s}"))
                .collect(),
        );
    }
    fn json(&mut self, phase: &str, extra: serde_json::Value) -> serde_json::Value {
        self.lat_us.sort();
        let pct = |p: f64| {
            self.lat_us
                .get(((self.lat_us.len() as f64 - 1.0) * p) as usize)
                .copied()
                .unwrap_or(0)
        };
        let mut v = serde_json::json!({
            "phase": phase, "queries": self.n,
            "mrr_at_10": if self.n > 0 { self.rr_sum / self.n as f64 } else { 0.0 },
            "recall_at_10": if self.n > 0 { self.hits10 as f64 / self.n as f64 } else { 0.0 },
            "p50_us": pct(0.5), "p95_us": pct(0.95), "maxrss_mib": mib(maxrss_bytes()),
        });
        if let (Some(o), Some(e)) = (v.as_object_mut(), extra.as_object()) {
            for (k, val) in e {
                o.insert(k.clone(), val.clone());
            }
        }
        v
    }
}

fn cmd_fts5_query(db: &Path, queries: &Path, op: &str) {
    let qs = load_queries(queries);
    let conn =
        rusqlite::Connection::open_with_flags(db, rusqlite::OpenFlags::SQLITE_OPEN_READ_ONLY)
            .unwrap();
    conn.execute_batch("PRAGMA cache_size=-2048; PRAGMA mmap_size=0;")
        .unwrap();
    let opened_rss = mib(maxrss_bytes());
    let mut top = conn
        .prepare("SELECT rowid FROM fts WHERE fts MATCH ?1 ORDER BY rank LIMIT 10")
        .unwrap();
    let mut meta = conn
        .prepare("SELECT path, start, end FROM chunk WHERE id = ?1")
        .unwrap();
    let mut ev = Eval::new();
    for (q, path, line) in &qs {
        let t = Instant::now();
        let ids: Vec<i64> = top
            .query_map([fts_match_expr(q, op)], |r| r.get(0))
            .unwrap()
            .map(|r| r.unwrap())
            .collect();
        let results: Vec<(String, usize, usize)> = ids
            .into_iter()
            .map(|id| {
                meta.query_row([id], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, i64>(1)? as usize,
                        r.get::<_, i64>(2)? as usize,
                    ))
                })
                .unwrap()
            })
            .collect();
        ev.record(&results, path, *line, t.elapsed().as_micros());
    }
    let top_path = queries.with_extension(format!("fts5-{op}-top.json"));
    std::fs::write(&top_path, serde_json::to_vec(&ev.top).unwrap()).unwrap();
    println!(
        "{}",
        ev.json("fts5-query", serde_json::json!({"opened_maxrss_mib": opened_rss, "op": op}))
    );
}

fn cmd_tantivy_build(root: &Path, dir: &Path, heap_mb: usize) {
    use tantivy::schema::{Schema, STORED, STRING, TEXT};
    use tantivy::{doc, Index, IndexWriter};
    let _ = std::fs::remove_dir_all(dir);
    std::fs::create_dir_all(dir).unwrap();
    let t0 = Instant::now();
    let mut sb = Schema::builder();
    let path_f = sb.add_text_field("path", STRING | STORED);
    let start_f = sb.add_u64_field("start", STORED);
    let end_f = sb.add_u64_field("end", STORED);
    let body_f = sb.add_text_field("body", TEXT);
    let index = Index::create_in_dir(dir, sb.build()).unwrap();
    let mut w: IndexWriter = index
        .writer_with_num_threads(1, heap_mb * 1_000_000)
        .unwrap();
    let st = for_each_chunk(root, |path, c| {
        w.add_document(doc!(
            path_f => path.to_string(),
            start_f => c.start as u64,
            end_f => c.end as u64,
            body_f => c.body.clone(),
        ))
        .unwrap();
    });
    w.commit().unwrap();
    w.wait_merging_threads().unwrap();
    println!(
        "{}",
        serde_json::json!({
            "phase": "tantivy-build", "heap_mb": heap_mb, "files": st.files, "chunks": st.chunks,
            "elapsed_ms": t0.elapsed().as_millis() as u64, "disk_mib": mib(dir_bytes(dir)),
            "maxrss_mib": mib(maxrss_bytes()),
        })
    );
}

fn cmd_tantivy_query(dir: &Path, queries: &Path, op: &str) {
    use tantivy::schema::Value;
    use tantivy::TantivyDocument;
    let qs = load_queries(queries);
    let index = tantivy::Index::open_in_dir(dir).unwrap();
    let schema = index.schema();
    let path_f = schema.get_field("path").unwrap();
    let start_f = schema.get_field("start").unwrap();
    let end_f = schema.get_field("end").unwrap();
    let body_f = schema.get_field("body").unwrap();
    let reader = index.reader().unwrap();
    let searcher = reader.searcher();
    let opened_rss = mib(maxrss_bytes());
    let mut qp = tantivy::query::QueryParser::for_index(&index, vec![body_f]);
    if op == "and" {
        qp.set_conjunction_by_default();
    }
    let mut ev = Eval::new();
    for (q, path, line) in &qs {
        let t = Instant::now();
        let query = qp.parse_query(q).unwrap();
        let hits = searcher
            .search(&query, &tantivy::collector::TopDocs::with_limit(10))
            .unwrap();
        let results: Vec<(String, usize, usize)> = hits
            .into_iter()
            .map(|(_, addr)| {
                let d: TantivyDocument = searcher.doc(addr).unwrap();
                (
                    d.get_first(path_f).and_then(|v| v.as_str()).unwrap().to_string(),
                    d.get_first(start_f).and_then(|v| v.as_u64()).unwrap() as usize,
                    d.get_first(end_f).and_then(|v| v.as_u64()).unwrap() as usize,
                )
            })
            .collect();
        ev.record(&results, path, *line, t.elapsed().as_micros());
    }
    let top_path = queries.with_extension(format!("tantivy-{op}-top.json"));
    std::fs::write(&top_path, serde_json::to_vec(&ev.top).unwrap()).unwrap();
    println!(
        "{}",
        ev.json("tantivy-query", serde_json::json!({"opened_maxrss_mib": opened_rss, "op": op}))
    );
}

fn op(s: &str) -> &str {
    if s == "and" {
        "and"
    } else {
        "or"
    }
}

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let a = |i: usize| args.get(i).map(String::as_str).unwrap_or("");
    match a(0) {
        "queries" => cmd_queries(
            Path::new(a(1)),
            Path::new(a(2)),
            a(3).parse().unwrap_or(400),
        ),
        "fts5-build" => cmd_fts5_build(
            Path::new(a(1)),
            Path::new(a(2)),
            if a(3).is_empty() { "full" } else { a(3) },
        ),
        "fts5-query" => cmd_fts5_query(Path::new(a(1)), Path::new(a(2)), op(a(3))),
        "tantivy-build" => cmd_tantivy_build(
            Path::new(a(1)),
            Path::new(a(2)),
            a(3).parse().unwrap_or(15),
        ),
        "tantivy-query" => cmd_tantivy_query(Path::new(a(1)), Path::new(a(2)), op(a(3))),
        _ => {
            eprintln!("usage: see module docs");
            std::process::exit(2);
        }
    }
}
