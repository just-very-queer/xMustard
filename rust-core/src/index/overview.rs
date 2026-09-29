//! File and directory overview over the code index (WS-36, PAR-SYM-03).
//!
//! A file's overview lists its top-level symbols (locals excluded) with their line
//! ranges and, to `depth` levels, their children, plus counts by kind. A directory's
//! lists every indexed file under it in path order, each with the same outline, plus
//! its immediate subdirectories and counts by kind and language. The staged fallback
//! (children, then symbols, then counts) is the output budget's ladder
//! (`api-go/internal/toolbudget`): the counts are always here to fall back to.

use std::collections::BTreeMap;

use rusqlite::{Connection, OptionalExtension, params};
use serde::Serialize;

use super::namepath::{self, Symbol};
use crate::symbolgraph::QuerySource;

/// Deepest level of children an overview lists.
pub const MAX_DEPTH: u32 = namepath::MAX_DEPTH;
/// Top-level symbols listed per file (symbols_total counts them all).
pub const MAX_FILE_SYMBOLS: usize = 500;
/// Top-level symbols listed per file of a directory.
pub const MAX_DIR_FILE_SYMBOLS: usize = 50;
/// Files a directory overview lists (files_total counts them all).
pub const MAX_DIR_FILES: usize = 2000;
/// Symbols (children included) one overview lists in total.
const MAX_SYMBOLS: usize = 20_000;

/// One symbol of an outline.
#[derive(Debug, Clone, Serialize)]
pub struct Outline {
    pub name_path: String,
    pub kind: String,
    pub lines: [u32; 2],
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub exported: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub children: Option<Vec<Outline>>,
}

impl From<Symbol> for Outline {
    fn from(s: Symbol) -> Outline {
        Outline {
            name_path: s.name_path,
            kind: s.kind,
            lines: s.lines,
            exported: s.exported,
            // no children and children not asked for read the same: none listed
            children: s
                .children
                .filter(|c| !c.is_empty())
                .map(|c| c.into_iter().map(Outline::from).collect()),
        }
    }
}

/// A file's outline.
#[derive(Debug, Clone, Serialize)]
pub struct FileOutline {
    pub path: String,
    pub lang: String,
    pub role: String,
    pub line_count: u32,
    pub symbols_total: usize,
    pub symbols_by_kind: BTreeMap<String, usize>,
    pub symbols: Vec<Outline>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub symbols_truncated: bool,
}

/// An immediate subdirectory of a directory overview.
#[derive(Debug, Clone, Serialize)]
pub struct Subdir {
    pub path: String,
    pub files: usize,
}

/// A directory's overview.
#[derive(Debug, Clone, Serialize)]
pub struct DirOutline {
    pub path: String,
    /// One line: files by language and top-level symbols by kind.
    pub summary: String,
    pub files_total: usize,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub files_truncated: bool,
    pub symbols_by_kind: BTreeMap<String, usize>,
    pub languages: BTreeMap<String, usize>,
    pub dirs: Vec<Subdir>,
    pub files: Vec<FileOutline>,
}

/// What an overview of `path` found.
#[derive(Debug, Clone)]
pub enum Overview {
    File(FileOutline),
    Directory(DirOutline),
    /// The index holds no code file at or under the path.
    NotIndexed(String),
}

impl Overview {
    /// The result members: `kind` (file, directory or not_indexed), `depth` and the
    /// outline's own.
    pub fn to_json(&self, depth: u32) -> serde_json::Value {
        let (kind, v) = match self {
            Overview::File(f) => ("file", serde_json::to_value(f)),
            Overview::Directory(d) => ("directory", serde_json::to_value(d)),
            Overview::NotIndexed(path) => ("not_indexed", Ok(serde_json::json!({ "path": path }))),
        };
        let mut v = v.unwrap_or_default();
        if let Some(o) = v.as_object_mut() {
            o.insert("kind".into(), kind.into());
            o.insert("depth".into(), depth.into());
        }
        v
    }
}

struct FileRow {
    id: i64,
    path: String,
    lang: String,
    role: String,
    line_count: u32,
}

fn file_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<FileRow> {
    Ok(FileRow {
        id: r.get(0)?,
        path: r.get(1)?,
        lang: r.get(2)?,
        role: r.get(3)?,
        line_count: r.get(4)?,
    })
}

const FILE_COLUMNS: &str = "SELECT id, path, lang, role, line_count FROM files";

/// The overview of a repo-relative file or directory ("" or "." is the root).
pub fn overview(conn: &Connection, path: &str, depth: u32) -> rusqlite::Result<Overview> {
    let path = path.trim().trim_start_matches("./").trim_end_matches('/');
    let path = if path == "." { "" } else { path };
    let depth = depth.min(MAX_DEPTH);
    let mut budget = MAX_SYMBOLS;
    if let Some(f) = conn
        .prepare_cached(&format!("{FILE_COLUMNS} WHERE path = ?1"))?
        .query_row([path], file_row)
        .optional()?
    {
        return Ok(Overview::File(outline(
            conn,
            &f,
            depth,
            MAX_FILE_SYMBOLS,
            &mut budget,
        )?));
    }
    // files under the directory, by a range on the path (prefix + '/' up to '0', the
    // byte after '/')
    let (lo, hi) = if path.is_empty() {
        (String::new(), "\u{10ffff}".to_string())
    } else {
        (format!("{path}/"), format!("{path}0"))
    };
    let files: Vec<FileRow> = conn
        .prepare_cached(&format!(
            "{FILE_COLUMNS} WHERE path >= ?1 AND path < ?2 ORDER BY path"
        ))?
        .query_map(params![lo, hi], file_row)?
        .collect::<rusqlite::Result<_>>()?;
    if files.is_empty() {
        return Ok(Overview::NotIndexed(path.to_string()));
    }
    let mut dirs: BTreeMap<String, usize> = BTreeMap::new();
    let mut languages: BTreeMap<String, usize> = BTreeMap::new();
    for f in &files {
        let rest = &f.path[lo.len()..];
        if let Some((sub, _)) = rest.split_once('/') {
            *dirs.entry(format!("{lo}{sub}")).or_default() += 1;
        }
        *languages.entry(f.lang.clone()).or_default() += 1;
    }
    let mut outlines = Vec::new();
    for f in files.iter().take(MAX_DIR_FILES) {
        outlines.push(outline(conn, f, depth, MAX_DIR_FILE_SYMBOLS, &mut budget)?);
    }
    // counted over every file under the directory, listed or not
    let symbols_by_kind: BTreeMap<String, usize> = conn
        .prepare_cached(
            "SELECT s.kind, count(*) FROM symbols s JOIN files f ON f.id = s.file_id \
             WHERE f.path >= ?1 AND f.path < ?2 AND s.local = 0 AND s.container_id IS NULL GROUP BY s.kind",
        )?
        .query_map(params![lo, hi], |r| Ok((r.get::<_, String>(0)?, r.get::<_, i64>(1)? as usize)))?
        .collect::<rusqlite::Result<_>>()?;
    let counts = |m: &BTreeMap<String, usize>| {
        m.iter()
            .map(|(k, n)| format!("{k} {n}"))
            .collect::<Vec<_>>()
            .join(", ")
    };
    let summary = format!(
        "{}: {} indexed file(s) ({}); {} top-level symbol(s) ({})",
        if path.is_empty() { "." } else { path },
        files.len(),
        counts(&languages),
        symbols_by_kind.values().sum::<usize>(),
        counts(&symbols_by_kind),
    );
    Ok(Overview::Directory(DirOutline {
        path: path.to_string(),
        summary,
        files_total: files.len(),
        files_truncated: files.len() > MAX_DIR_FILES,
        symbols_by_kind,
        languages,
        dirs: dirs
            .into_iter()
            .map(|(path, files)| Subdir { path, files })
            .collect(),
        files: outlines,
    }))
}

/// A file's top-level symbols (at most `max`, children to `depth`) and counts by kind.
fn outline(
    conn: &Connection,
    f: &FileRow,
    depth: u32,
    max: usize,
    budget: &mut usize,
) -> rusqlite::Result<FileOutline> {
    let top = namepath::query(
        conn,
        "s.file_id = ?1 AND s.local = 0 AND s.container_id IS NULL ORDER BY s.start_line, s.id",
        [f.id],
    )?;
    let mut symbols_by_kind: BTreeMap<String, usize> = BTreeMap::new();
    for s in &top {
        *symbols_by_kind.entry(s.kind.clone()).or_default() += 1;
    }
    let total = top.len();
    let keep = max.min(*budget);
    *budget = budget.saturating_sub(total.min(keep));
    let mut symbols = Vec::with_capacity(total.min(keep));
    for mut s in top.into_iter().take(keep) {
        if depth > 0 {
            s.children = Some(namepath::children(conn, s.row.id, depth, budget)?);
        }
        symbols.push(Outline::from(s));
    }
    Ok(FileOutline {
        path: f.path.clone(),
        lang: f.lang.clone(),
        role: f.role.clone(),
        line_count: f.line_count,
        symbols_truncated: total > symbols.len(),
        symbols_total: total,
        symbols_by_kind,
        symbols,
    })
}

/// `explain path=`: the overview of a file or directory with the index envelope; a
/// read the index does not answer says so under degradations.
pub fn answer(
    src: &QuerySource,
    workspace_id: &str,
    path: &str,
    depth: u32,
) -> Result<serde_json::Value, String> {
    let Some(opened) = src.index() else {
        let mut out = serde_json::json!({ "path": path, "kind": "unavailable", "degradations": [namepath::NEEDS_INDEX] });
        namepath::envelope(&mut out, src, workspace_id, []);
        return Ok(out);
    };
    let depth = depth.min(MAX_DEPTH);
    let ov = opened
        .snapshot
        .with_text(|conn| overview(conn, path, depth))?;
    let mut out = ov.to_json(depth);
    let paths: Vec<&str> = match &ov {
        Overview::File(f) => vec![f.path.as_str()],
        Overview::Directory(d) => d.files.iter().map(|f| f.path.as_str()).collect(),
        Overview::NotIndexed(_) => Vec::new(),
    };
    namepath::envelope(&mut out, src, workspace_id, paths);
    Ok(out)
}
