//! Materialize one file's facts into `index.db` rows, and remove them again.
//!
//! Rows for one file are written together inside the caller's transaction. Chunk
//! postings are derived here from the bytes (or, for `content_retention=none`, from the
//! identifier facts alone), so the fact cache never needs source text. Unless retention
//! is `full`, a chunk's postings are an unordered bag of words (see `chunks::unordered`),
//! and under `symbol` credential-shaped words are left out.

use std::collections::HashMap;

use rusqlite::{Connection, OptionalExtension, params};

use super::chunks;
use super::config::ContentRetention;
use super::facts::FileFacts;
use super::uid;

/// Name-id lookups kept in memory before the map is cleared (bounded, ~200 KiB); the
/// `names` table's own index serves the rest.
const NAME_CACHE_MAX: usize = 4096;

/// The `files` row of one eligible file.
#[derive(Debug, Clone)]
pub struct FileRecord {
    pub path: String,
    pub lang: String,
    pub role: String,
    pub size: i64,
    pub mtime_ns: i64,
    /// `scan::StatKey::encode` of the stat taken before the read.
    pub stat_key: String,
    /// Git blob id of the indexed bytes; empty when the file was not read.
    pub content_hash: String,
    /// `tree_sitter` | `regex` | `none` for parsed files, otherwise the loss reason.
    pub parse_status: String,
    pub flags: i64,
    /// Symbols extraction found, before the symbol budget (the budget plan's input).
    pub fact_symbols: i64,
}

pub struct Writer<'c> {
    conn: &'c Connection,
    retention: ContentRetention,
    names: HashMap<String, i64>,
    next_symbol: i64,
    next_chunk: i64,
}

impl<'c> Writer<'c> {
    pub fn new(conn: &'c Connection, retention: ContentRetention) -> rusqlite::Result<Self> {
        let next_symbol: i64 =
            conn.query_row("SELECT coalesce(max(id), 0) + 1 FROM symbols", [], |r| {
                r.get(0)
            })?;
        let next_chunk: i64 =
            conn.query_row("SELECT coalesce(max(id), 0) + 1 FROM chunks", [], |r| {
                r.get(0)
            })?;
        // every statement of the write path stays prepared (the default cache holds 16).
        conn.set_prepared_statement_cache_capacity(32);
        Ok(Writer {
            conn,
            retention,
            names: HashMap::new(),
            next_symbol,
            next_chunk,
        })
    }

    pub fn name_id(&mut self, name: &str) -> rusqlite::Result<i64> {
        if let Some(&id) = self.names.get(name) {
            return Ok(id);
        }
        let mut ins = self
            .conn
            .prepare_cached("INSERT OR IGNORE INTO names(name) VALUES (?1)")?;
        ins.execute([name])?;
        let mut sel = self
            .conn
            .prepare_cached("SELECT id FROM names WHERE name = ?1")?;
        let id: i64 = sel.query_row([name], |r| r.get(0))?;
        if self.names.len() >= NAME_CACHE_MAX {
            self.names.clear();
        }
        self.names.insert(name.to_string(), id);
        Ok(id)
    }

    /// Insert (or, with `file_id`, replace in place) the file row and all fact rows.
    /// Returns the file id and the number of symbols written.
    pub fn write_file(
        &mut self,
        file_id: Option<i64>,
        rec: &FileRecord,
        facts: Option<&FileFacts>,
        bytes: Option<&[u8]>,
    ) -> rusqlite::Result<i64> {
        let symbol_count = facts.map_or(0, |f| f.symbols.len()) as i64;
        let line_count = facts.map_or(0, |f| f.line_count) as i64;
        let flags = rec.flags | facts.map_or(0, FileFacts::flags);
        let fid = match file_id {
            Some(id) => {
                self.conn
                    .prepare_cached(
                        "UPDATE files SET path=?2, lang=?3, role=?4, size=?5, mtime_ns=?6,
                         content_hash=?7, parse_status=?8, flags=?9, symbol_count=?10,
                         line_count=?11, stat_key=?12, fact_symbols=?13 WHERE id=?1",
                    )?
                    .execute(params![
                        id,
                        rec.path,
                        rec.lang,
                        rec.role,
                        rec.size,
                        rec.mtime_ns,
                        rec.content_hash,
                        rec.parse_status,
                        flags,
                        symbol_count,
                        line_count,
                        rec.stat_key,
                        rec.fact_symbols
                    ])?;
                id
            }
            None => {
                self.conn
                    .prepare_cached(
                        "INSERT INTO files(path, lang, role, size, mtime_ns, content_hash,
                         parse_status, flags, symbol_count, line_count, stat_key, fact_symbols)
                         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12)",
                    )?
                    .execute(params![
                        rec.path,
                        rec.lang,
                        rec.role,
                        rec.size,
                        rec.mtime_ns,
                        rec.content_hash,
                        rec.parse_status,
                        flags,
                        symbol_count,
                        line_count,
                        rec.stat_key,
                        rec.fact_symbols
                    ])?;
                self.conn.last_insert_rowid()
            }
        };
        if let Some(f) = facts {
            self.write_facts(fid, &rec.path, f, bytes)?;
        }
        Ok(fid)
    }

    fn write_facts(
        &mut self,
        fid: i64,
        path: &str,
        f: &FileFacts,
        bytes: Option<&[u8]>,
    ) -> rusqlite::Result<()> {
        let base = self.next_symbol;
        self.next_symbol += f.symbols.len() as i64;
        let sym_id = |i: u32| base + i as i64;
        for (i, s) in f.symbols.iter().enumerate() {
            let name_id = self.name_id(&s.name)?;
            self.conn
                .prepare_cached(
                    "INSERT INTO symbols(id, file_id, ord, uid, name_id, qualified_name, kind,
                     container_id, arity, start_byte, end_byte, start_line, end_line, name_line,
                     name_col, signature_hash, exported, depth, local)
                     VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19)",
                )?
                .execute(params![
                    sym_id(i as u32),
                    fid,
                    i as i64,
                    uid::uid(path, s),
                    name_id,
                    s.qualified_name,
                    s.kind,
                    s.container.map(sym_id),
                    s.arity,
                    s.start_byte,
                    s.end_byte,
                    s.start_line,
                    s.end_line,
                    s.name_line,
                    s.name_col,
                    (!s.signature_hash.is_empty()).then_some(s.signature_hash.as_str()),
                    s.exported,
                    s.depth,
                    s.local
                ])?;
        }
        // name table ids for this file's reference names
        let mut ref_names: Vec<i64> = Vec::with_capacity(f.names.len());
        for n in &f.names {
            ref_names.push(self.name_id(n)?);
        }
        {
            let mut ins = self.conn.prepare_cached(
                "INSERT OR IGNORE INTO refs(file_id, start_byte, name_id, symbol_id, line, col, kind, flow, qual_id)
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9)",
            )?;
            for r in &f.refs {
                ins.execute(params![
                    fid,
                    r.start_byte(),
                    ref_names[r.name_idx()],
                    r.symbol().map(sym_id),
                    r.line(),
                    r.col(),
                    r.kind(),
                    r.flow(),
                    r.qual_idx().map(|q| ref_names[q])
                ])?;
            }
        }
        {
            let mut ins = self.conn.prepare_cached(
                "INSERT INTO imports(file_id, ord, line, kind, module, name, alias, target_key)
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8)",
            )?;
            for (i, im) in f.imports.iter().enumerate() {
                ins.execute(params![
                    fid,
                    i as i64,
                    im.line,
                    im.kind,
                    im.module,
                    im.name,
                    im.alias,
                    relative_target_key(path, &im.module)
                ])?;
            }
        }
        self.write_chunks(fid, path, f, bytes, &sym_id)
    }

    fn write_chunks(
        &mut self,
        fid: i64,
        path: &str,
        f: &FileFacts,
        bytes: Option<&[u8]>,
        sym_id: &dyn Fn(u32) -> i64,
    ) -> rusqlite::Result<()> {
        let path_tokens = chunks::path_tokens(path);
        let refs_sorted = f.refs.is_sorted_by_key(|r| r.line());
        for (ord, c) in f.chunks.iter().enumerate() {
            let id = self.next_chunk;
            self.next_chunk += 1;
            self.conn
                .prepare_cached(
                    "INSERT INTO chunks(id, file_id, ord, symbol_id, kind, start_line, end_line,
                     start_byte, end_byte, content_hash) VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10)",
                )?
                .execute(params![
                    id,
                    fid,
                    ord as i64,
                    c.symbol.map(sym_id),
                    c.kind,
                    c.start_line,
                    c.end_line,
                    c.start_byte,
                    c.end_byte,
                    c.content_hash
                ])?;
            // symbols declared in the chunk: each one's own name and its container's
            // (never the whole path, whose size grows with nesting depth).
            let mut sym_tokens = String::new();
            for s in f
                .symbols
                .iter()
                .filter(|s| c.start_line <= s.name_line && s.name_line <= c.end_line)
            {
                for seg in s
                    .qualified_name
                    .rsplit('.')
                    .take(2)
                    .filter(|seg| !seg.starts_with('~'))
                {
                    chunks::push_word_tokens(seg, &mut sym_tokens);
                }
            }
            let text = bytes
                .and_then(|b| b.get(c.start_byte as usize..c.end_byte as usize))
                .map(String::from_utf8_lossy);
            let mut body = String::new();
            match (self.retention, &text) {
                (ContentRetention::None, _) | (_, None) => {
                    // identifiers only: reference names on the chunk's lines.
                    let from = if refs_sorted {
                        f.refs.partition_point(|r| r.line() < c.start_line)
                    } else {
                        0
                    };
                    for r in f.refs[from..]
                        .iter()
                        .take_while(|r| !refs_sorted || r.line() <= c.end_line)
                        .filter(|r| c.start_line <= r.line() && r.line() <= c.end_line)
                    {
                        chunks::push_word_tokens(&f.names[r.name_idx()], &mut body);
                    }
                }
                (ContentRetention::Symbol, Some(t)) => {
                    chunks::tokenize_without_secrets(t, &mut body)
                }
                (ContentRetention::Full, Some(t)) => chunks::tokenize_into(t, &mut body),
            }
            // Without full retention the postings are a bag of words: the source's word
            // order is not kept anywhere in the index.
            if self.retention != ContentRetention::Full {
                body = chunks::unordered(&body);
            }
            self.conn
                .prepare_cached(
                    "INSERT INTO chunk_fts(rowid, path, symbols, body) VALUES (?1, ?2, ?3, ?4)",
                )?
                .execute(params![id, path_tokens, sym_tokens, body])?;
            if self.retention == ContentRetention::Full
                && let Some(t) = text
            {
                self.conn
                    .prepare_cached("INSERT INTO chunk_text(chunk_id, text) VALUES (?1, ?2)")?
                    .execute(params![id, t.as_ref()])?;
            }
        }
        Ok(())
    }
}

/// Delete every fact row of a file, including its outgoing edges and chunk postings.
/// With `keep_file_row`, the `files` row (and so its id) survives for an in-place update.
pub fn delete_file_rows(conn: &Connection, fid: i64, keep_file_row: bool) -> rusqlite::Result<()> {
    conn.prepare_cached(
        "DELETE FROM chunk_fts WHERE rowid IN (SELECT id FROM chunks WHERE file_id = ?1)",
    )?
    .execute([fid])?;
    conn.prepare_cached(
        "DELETE FROM chunk_text WHERE chunk_id IN (SELECT id FROM chunks WHERE file_id = ?1)",
    )?
    .execute([fid])?;
    for sql in [
        "DELETE FROM chunks WHERE file_id = ?1",
        "DELETE FROM refs WHERE file_id = ?1",
        "DELETE FROM imports WHERE file_id = ?1",
        "DELETE FROM symbols WHERE file_id = ?1",
        "DELETE FROM edges WHERE src_file = ?1",
        "DELETE FROM resolve_drops WHERE file_id = ?1",
    ] {
        conn.prepare_cached(sql)?.execute([fid])?;
    }
    if !keep_file_row {
        conn.prepare_cached("DELETE FROM edges WHERE dst_file = ?1")?
            .execute([fid])?;
        conn.prepare_cached(
            "UPDATE imports SET resolved_file_id = NULL WHERE resolved_file_id = ?1",
        )?
        .execute([fid])?;
        conn.prepare_cached("DELETE FROM files WHERE id = ?1")?
            .execute([fid])?;
    }
    Ok(())
}

/// For a relative JS/TS specifier (`./x`, `../y/z`), the repository path it names
/// before extension resolution; None for bare module names.
pub fn relative_target_key(from_path: &str, spec: &str) -> Option<String> {
    if !(spec.starts_with("./") || spec.starts_with("../") || spec == "." || spec == "..") {
        return None;
    }
    let mut parts: Vec<&str> = from_path.split('/').collect();
    parts.pop(); // the file name
    for seg in spec.split('/') {
        match seg {
            "" | "." => {}
            ".." => {
                parts.pop()?;
            }
            s => parts.push(s),
        }
    }
    Some(parts.join("/"))
}

const RESOLVE_EXTS: &[&str] = &["ts", "tsx", "js", "jsx", "mjs", "cjs", "mts", "cts"];

/// The tracked files a relative target key may name, in resolution order: the exact
/// path, `.ts`-family and `.js`-family extensions, an ESM `.js` specifier naming a `.ts`
/// source, then `index.*` inside a directory.
pub fn resolution_candidates(key: &str) -> Vec<String> {
    let mut out = vec![key.to_string()];
    for e in RESOLVE_EXTS {
        out.push(format!("{key}.{e}"));
    }
    if let Some((stem, ext)) = key.rsplit_once('.')
        && matches!(ext, "js" | "jsx" | "mjs" | "cjs")
        && !stem.ends_with('/')
    {
        for e in ["ts", "tsx", "mts", "cts"] {
            out.push(format!("{stem}.{e}"));
        }
    }
    for e in RESOLVE_EXTS {
        out.push(format!("{key}/index.{e}"));
    }
    out
}

/// Every target key that would resolve to `path` (inverse of `resolution_candidates`),
/// used to find importers affected when `path` appears or disappears.
pub fn keys_resolving_to(path: &str) -> Vec<String> {
    let mut out = vec![path.to_string()];
    if let Some((stem, ext)) = path.rsplit_once('.')
        && RESOLVE_EXTS.contains(&ext)
    {
        out.push(stem.to_string());
        if matches!(ext, "ts" | "tsx" | "mts" | "cts") {
            for e in ["js", "jsx", "mjs", "cjs"] {
                out.push(format!("{stem}.{e}"));
            }
        }
        if let Some(dir) = stem.strip_suffix("/index") {
            out.push(dir.to_string());
        } else if stem == "index" {
            out.push(String::new());
        }
    }
    out
}

/// Resolve every relative import with a target key against the file table.
/// `only_files`: restrict to imports of these files (None = all).
pub fn resolve_imports(conn: &Connection, only_files: Option<&[i64]>) -> rusqlite::Result<usize> {
    let mut find = conn.prepare_cached("SELECT id FROM files WHERE path = ?1")?;
    let mut rows: Vec<(i64, i64, String)> = Vec::new();
    {
        let row = |r: &rusqlite::Row<'_>| -> rusqlite::Result<(i64, i64, String)> {
            Ok((r.get(0)?, r.get(1)?, r.get(2)?))
        };
        let mut collect = |sql: &str, arg: Option<i64>| -> rusqlite::Result<()> {
            let mut st = conn.prepare_cached(sql)?;
            let mapped = match arg {
                Some(a) => st
                    .query_map([a], row)?
                    .collect::<rusqlite::Result<Vec<_>>>()?,
                None => st
                    .query_map([], row)?
                    .collect::<rusqlite::Result<Vec<_>>>()?,
            };
            rows.extend(mapped);
            Ok(())
        };
        match only_files {
            None => collect(
                "SELECT file_id, ord, target_key FROM imports WHERE target_key IS NOT NULL",
                None,
            )?,
            Some(ids) => {
                for id in ids {
                    collect(
                        "SELECT file_id, ord, target_key FROM imports WHERE target_key IS NOT NULL AND file_id = ?1",
                        Some(*id),
                    )?;
                }
            }
        }
    }
    let mut upd = conn.prepare_cached(
        "UPDATE imports SET resolved_file_id = ?3 WHERE file_id = ?1 AND ord = ?2",
    )?;
    let mut resolved = 0;
    for (file_id, ord, key) in rows {
        let mut hit: Option<i64> = None;
        for cand in resolution_candidates(&key) {
            if let Some(id) = find.query_row([&cand], |r| r.get(0)).optional()? {
                hit = Some(id);
                break;
            }
        }
        if hit.is_some() {
            resolved += 1;
        }
        upd.execute(params![file_id, ord, hit])?;
    }
    Ok(resolved)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn relative_keys_and_candidates() {
        assert_eq!(
            relative_target_key("src/a/b.ts", "./c").as_deref(),
            Some("src/a/c")
        );
        assert_eq!(
            relative_target_key("src/a/b.ts", "../x/y.js").as_deref(),
            Some("src/x/y.js")
        );
        assert_eq!(relative_target_key("b.ts", "react"), None);
        assert_eq!(relative_target_key("b.ts", "../../escape"), None);
        let c = resolution_candidates("src/x/y.js");
        assert!(c.contains(&"src/x/y.ts".to_string()));
        assert!(keys_resolving_to("src/x/y.ts").contains(&"src/x/y.js".to_string()));
        assert!(keys_resolving_to("src/x/index.ts").contains(&"src/x".to_string()));
        assert!(keys_resolving_to("src/x/y.ts").contains(&"src/x/y".to_string()));
    }
}
