//! Name-path symbol lookup over the code index (WS-36, PAR-SYM-02).
//!
//! A name path addresses a symbol through its containers:
//! - `name` matches every non-local symbol so named; with `substring`, every name that
//!   contains it (case-insensitive);
//! - `A/b` matches a symbol `b` whose containers end with `A` (relative);
//! - `/A/b` matches the top-level `A` and its member `b` exactly (absolute);
//! - `b[i]` keeps the i-th (0-based, in source order) symbol of one name path within a
//!   file: overloads and redeclarations.
//!
//! The index stores containers dotted (`A.b`); name paths join them with `/`. Matches
//! are ranked by relevance (exact name, then case-folded, then substring coverage;
//! exported and shallower first), ties by path and line. Signatures, doc comments and
//! bodies are sliced from the working tree, never stored: a file is read only while it
//! still hashes to its indexed blob, never on a secret path, and credential-shaped words
//! are masked as snippets mask them.

use std::collections::{BTreeMap, HashMap};
use std::path::Path;

use rusqlite::{Connection, params};
use serde::Serialize;
use serde_json::{Value, json};

use super::chunks::mask_secret_words;
use super::ignore::Glob;
use super::scan;
use crate::symbolgraph::QuerySource;

/// Matches returned when the caller names no max_matches.
pub const DEFAULT_MAX_MATCHES: usize = 20;
/// The most matches one page returns.
pub const MAX_MATCHES: usize = 200;
/// Deepest level of children returned.
pub const MAX_DEPTH: u32 = 3;
/// Children listed per symbol and level.
pub const MAX_CHILDREN: usize = 200;
/// Longest body returned; a longer one is cut at a character boundary.
pub const MAX_BODY_BYTES: usize = 32 << 10;
/// Files and names per file the refine map lists.
pub const MAX_REFINE_FILES: usize = 100;
pub const MAX_REFINE_NAMES: usize = 20;
/// Candidates a lookup ranks before paging (a common name in a large repository).
const MAX_CANDIDATES: usize = 5000;
const MAX_SIGNATURE_CHARS: usize = 240;
const MAX_DOC_LINES: usize = 40;

/// A parsed name path.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NamePath {
    pub absolute: bool,
    pub segments: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub overload: Option<usize>,
}

impl NamePath {
    pub fn parse(text: &str) -> Result<NamePath, String> {
        let text = text.trim();
        let (absolute, rest) = match text.strip_prefix('/') {
            Some(rest) => (true, rest),
            None => (false, text),
        };
        let mut segments: Vec<String> = rest.split('/').map(|s| s.trim().to_string()).collect();
        if segments.iter().any(String::is_empty) {
            return Err(format!("name path {text:?} has an empty segment"));
        }
        let mut overload = None;
        let last = segments.last_mut().expect("split yields one segment");
        if let Some(open) = last.rfind('[').filter(|_| last.ends_with(']')) {
            let index = &last[open + 1..last.len() - 1];
            overload = Some(
                index
                    .parse::<usize>()
                    .map_err(|_| format!("overload index {index:?} is not a number"))?,
            );
            last.truncate(open);
            if last.is_empty() {
                return Err(format!("name path {text:?} has no name before its index"));
            }
        }
        Ok(NamePath {
            absolute,
            segments,
            overload,
        })
    }

    pub fn name(&self) -> &str {
        self.segments.last().map_or("", String::as_str)
    }

    /// Whether the containers of a dotted qualified name match this path's leading
    /// segments: the whole chain when absolute, its tail when relative.
    fn containers_match(&self, qualified: &str) -> bool {
        let parts: Vec<&str> = qualified.split('.').collect();
        let want = &self.segments[..self.segments.len() - 1];
        let n = self.segments.len();
        if parts.len() < n || (self.absolute && parts.len() != n) {
            return false;
        }
        let have = &parts[parts.len() - n..parts.len() - 1];
        have.iter().zip(want).all(|(h, w)| *h == w.as_str())
    }
}

/// Kind filters: `Function,Method` includes, `-Variable` excludes (case-insensitive).
#[derive(Debug, Clone, Default)]
pub struct Kinds {
    include: Vec<String>,
    exclude: Vec<String>,
}

impl Kinds {
    pub fn parse(list: &str) -> Kinds {
        let mut k = Kinds::default();
        for part in list.split(',').map(str::trim).filter(|p| !p.is_empty()) {
            match part.strip_prefix('-') {
                Some(ex) => k.exclude.push(ex.to_ascii_lowercase()),
                None => k.include.push(part.to_ascii_lowercase()),
            }
        }
        k
    }

    pub fn admits(&self, kind: &str) -> bool {
        let kind = kind.to_ascii_lowercase();
        (self.include.is_empty() || self.include.contains(&kind)) && !self.exclude.contains(&kind)
    }
}

/// The signature line and leading doc comment of a symbol.
#[derive(Debug, Clone, Serialize)]
pub struct Info {
    pub signature: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub doc: String,
}

/// The index row behind a symbol.
#[derive(Debug, Clone, Default)]
pub struct Row {
    pub id: i64,
    pub file_id: i64,
    pub qualified: String,
    pub start_byte: u32,
    pub end_byte: u32,
    pub name_line: u32,
    pub depth: u32,
    pub content_hash: String,
    pub lang: String,
}

/// One symbol as navigation returns it.
#[derive(Debug, Clone, Serialize)]
pub struct Symbol {
    pub name_path: String,
    pub name: String,
    pub kind: String,
    pub path: String,
    pub lines: [u32; 2],
    pub line: u32,
    pub uid: String,
    pub exported: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub relevance: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub info: Option<Info>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub body: Option<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub body_truncated: bool,
    /// Why info or body could not be read.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unavailable: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub children: Option<Vec<Symbol>>,
    #[serde(skip)]
    pub row: Row,
}

/// The columns every symbol query selects, in `from_row` order.
pub const SELECT: &str = "SELECT s.id, s.file_id, f.path, n.name, s.qualified_name, s.kind, \
     s.start_line, s.end_line, s.name_line, s.uid, s.exported, s.start_byte, s.end_byte, s.depth, \
     f.content_hash, f.lang \
     FROM symbols s JOIN names n ON n.id = s.name_id JOIN files f ON f.id = s.file_id";

pub fn from_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Symbol> {
    let qualified: String = r.get(4)?;
    Ok(Symbol {
        name_path: qualified.replace('.', "/"),
        name: r.get(3)?,
        kind: r.get(5)?,
        path: r.get(2)?,
        lines: [r.get(6)?, r.get(7)?],
        line: r.get(8)?,
        uid: r.get(9)?,
        exported: r.get(10)?,
        relevance: None,
        info: None,
        body: None,
        body_truncated: false,
        unavailable: None,
        children: None,
        row: Row {
            id: r.get(0)?,
            file_id: r.get(1)?,
            qualified,
            start_byte: r.get(11)?,
            end_byte: r.get(12)?,
            name_line: r.get(8)?,
            depth: r.get(13)?,
            content_hash: r.get(14)?,
            lang: r.get(15)?,
        },
    })
}

/// Symbols matching `sql` (a WHERE clause over SELECT's aliases) in source order.
pub fn query(
    conn: &Connection,
    sql: &str,
    args: impl rusqlite::Params,
) -> rusqlite::Result<Vec<Symbol>> {
    let mut st = conn.prepare_cached(&format!("{SELECT} WHERE {sql}"))?;
    st.query_map(args, from_row)?.collect()
}

/// The symbol with this UID.
pub fn by_uid(conn: &Connection, uid: &str) -> rusqlite::Result<Option<Symbol>> {
    Ok(query(conn, "s.uid = ?1", [uid])?.into_iter().next())
}

/// Lookup options.
#[derive(Debug, Clone, Default)]
pub struct FindOptions<'a> {
    pub substring: bool,
    pub kinds: Kinds,
    pub path_glob: Option<&'a str>,
}

/// Every non-local symbol matching the name path, ranked.
pub fn find(
    conn: &Connection,
    np: &NamePath,
    opts: &FindOptions<'_>,
) -> rusqlite::Result<Vec<Symbol>> {
    let glob = opts
        .path_glob
        .filter(|g| !g.is_empty())
        .map(|g| Glob::compile(g.as_bytes()));
    let mut found = if opts.substring {
        query(
            conn,
            "instr(lower(n.name), lower(?1)) > 0 AND s.local = 0 ORDER BY f.path, s.start_line LIMIT ?2",
            params![np.name(), MAX_CANDIDATES as i64],
        )?
    } else {
        query(
            conn,
            "n.name = ?1 AND s.local = 0 ORDER BY f.path, s.start_line LIMIT ?2",
            params![np.name(), MAX_CANDIDATES as i64],
        )?
    };
    found.retain(|s| {
        np.containers_match(&s.row.qualified)
            && opts.kinds.admits(&s.kind)
            && glob.as_ref().is_none_or(|g| g.matches(s.path.as_bytes()))
    });
    if let Some(i) = np.overload {
        let mut seen: HashMap<(String, String), usize> = HashMap::new();
        found.retain(|s| {
            let n = seen
                .entry((s.path.clone(), s.name_path.clone()))
                .or_default();
            *n += 1;
            *n - 1 == i
        });
    }
    for s in &mut found {
        s.relevance = Some(relevance(np.name(), s));
    }
    found.sort_by(|a, b| {
        b.relevance
            .unwrap_or(0.0)
            .total_cmp(&a.relevance.unwrap_or(0.0))
            .then_with(|| a.path.cmp(&b.path))
            .then(a.lines[0].cmp(&b.lines[0]))
    });
    Ok(found)
}

/// Exact name 1.0, case-folded 0.9, a substring by how much of the name it covers;
/// exported symbols a little higher, nested ones a little lower.
fn relevance(query: &str, s: &Symbol) -> f64 {
    let name = s.name.as_str();
    let base = match () {
        _ if name == query => 1.0,
        _ if name.eq_ignore_ascii_case(query) => 0.9,
        _ => 0.5 + 0.4 * query.len() as f64 / name.len().max(1) as f64,
    };
    let r = base + if s.exported { 0.05 } else { 0.0 } - 0.02 * f64::from(s.row.depth.min(5));
    (r * 1e6).round() / 1e6
}

/// Whether the matches name more than one symbol (not only overloads of one name
/// path in one file).
pub fn ambiguous(matches: &[Symbol]) -> bool {
    matches
        .iter()
        .any(|s| s.path != matches[0].path || s.name_path != matches[0].name_path)
}

/// Every match's name path by file, bounded: the map a caller narrows a query with.
pub fn refine_map(matches: &[Symbol]) -> (BTreeMap<String, Vec<String>>, bool) {
    let mut map: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut truncated = false;
    for s in matches {
        if !map.contains_key(&s.path) && map.len() >= MAX_REFINE_FILES {
            truncated = true;
            continue;
        }
        let names = map.entry(s.path.clone()).or_default();
        if names.contains(&s.name_path) {
            continue;
        }
        if names.len() >= MAX_REFINE_NAMES {
            truncated = true;
            continue;
        }
        names.push(s.name_path.clone());
    }
    (map, truncated)
}

/// The children of `parent`, `depth` levels down, in source order; `budget` bounds
/// the symbols listed in total.
pub fn children(
    conn: &Connection,
    parent: i64,
    depth: u32,
    budget: &mut usize,
) -> rusqlite::Result<Vec<Symbol>> {
    if depth == 0 || *budget == 0 {
        return Ok(Vec::new());
    }
    let mut kids = query(
        conn,
        "s.container_id = ?1 AND s.local = 0 ORDER BY s.start_line, s.id LIMIT ?2",
        params![parent, MAX_CHILDREN.min(*budget) as i64],
    )?;
    *budget = budget.saturating_sub(kids.len());
    for k in &mut kids {
        if depth > 1 {
            k.children = Some(children(conn, k.row.id, depth - 1, budget)?);
        }
    }
    Ok(kids)
}

/// Working-tree text of indexed files, each read once per call.
#[derive(Default)]
pub struct Texts {
    files: HashMap<i64, Result<String, &'static str>>,
}

impl Texts {
    /// The text of the file behind `s`, while it still hashes to its indexed blob.
    pub fn of(&mut self, root: &Path, s: &Symbol) -> Result<&str, &'static str> {
        self.file(root, s.row.file_id, &s.path, &s.row.content_hash)
    }

    /// The text of indexed file `id` at `path`, while it still hashes to `blob`.
    pub fn file(
        &mut self,
        root: &Path,
        id: i64,
        path: &str,
        blob: &str,
    ) -> Result<&str, &'static str> {
        self.files
            .entry(id)
            .or_insert_with(|| read_indexed(root, path, blob))
            .as_deref()
            .map_err(|e| *e)
    }
}

fn read_indexed(root: &Path, path: &str, blob: &str) -> Result<String, &'static str> {
    if crate::secretpath::is_secret_path(path) {
        return Err("secret path");
    }
    let bytes = crate::symbolgraph::read_repo_bytes_beneath_capped(
        root,
        path,
        crate::symbolgraph::MAX_REPO_FILE_BYTES,
    )
    .map_err(|_| "file unreadable")?;
    if scan::blob_id(&bytes) != blob {
        return Err("file changed since it was indexed");
    }
    String::from_utf8(bytes).map_err(|_| "file is not UTF-8")
}

/// Lines `from..=to` (1-based) of `text`, each masked; at most `max` lines.
pub fn lines(text: &str, from: u32, to: u32, max: usize) -> Vec<(u32, String)> {
    text.lines()
        .enumerate()
        .map(|(i, l)| (i as u32 + 1, l))
        .skip_while(|(n, _)| *n < from)
        .take_while(|(n, _)| *n <= to)
        .take(max)
        .map(|(n, l)| (n, mask_secret_words(l).into_owned()))
        .collect()
}

/// A snippet: lines around `line` (±1), numbered like search snippets.
pub fn snippet(text: &str, line: u32) -> String {
    lines(text, line.saturating_sub(1).max(1), line + 1, 3)
        .into_iter()
        .map(|(n, l)| format!("{n}: {}", clip(&l, MAX_SIGNATURE_CHARS)))
        .collect::<Vec<_>>()
        .join("\n")
}

fn clip(s: &str, max: usize) -> String {
    match s.char_indices().nth(max) {
        Some((cut, _)) => format!("{}…", &s[..cut]),
        None => s.to_string(),
    }
}

/// Fill a symbol's signature and doc comment (`info`) and its body from the file.
pub fn slice(texts: &mut Texts, root: &Path, s: &mut Symbol, info: bool, body: bool) {
    if !info && !body {
        return;
    }
    let text = match texts.of(root, s) {
        Ok(t) => t,
        Err(why) => {
            s.unavailable = Some(why.to_string());
            return;
        }
    };
    if info {
        let signature = lines(text, s.row.name_line, s.row.name_line, 1)
            .into_iter()
            .map(|(_, l)| clip(l.trim(), MAX_SIGNATURE_CHARS))
            .next()
            .unwrap_or_default();
        let doc = lines(
            text,
            s.lines[0],
            s.row.name_line.saturating_sub(1),
            MAX_DOC_LINES,
        )
        .into_iter()
        .map(|(_, l)| l.trim().to_string())
        .collect::<Vec<_>>()
        .join("\n");
        s.info = Some(Info { signature, doc });
    }
    if body {
        let (start, end) = (s.row.start_byte as usize, s.row.end_byte as usize);
        let Some(raw) = text.get(start..end.min(text.len())) else {
            s.unavailable = Some("body is not on a character boundary".to_string());
            return;
        };
        let mut cut = raw.len().min(MAX_BODY_BYTES);
        while !raw.is_char_boundary(cut) {
            cut -= 1;
        }
        s.body_truncated = cut < raw.len();
        s.body = Some(
            raw[..cut]
                .lines()
                .map(|l| mask_secret_words(l).into_owned())
                .collect::<Vec<_>>()
                .join("\n"),
        );
    }
}

/// Why a navigation answer has no symbols when the code index does not answer.
pub const NEEDS_INDEX: &str =
    "symbol navigation needs the code index, which does not answer this read (see coverage)";

/// When a navigation answer was generated.
pub fn generated_at() -> String {
    chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true)
}

/// Add the envelope every navigation answer carries: the workspace, the freshness of
/// the result's paths, the coverage and the time.
pub fn envelope<'a>(
    out: &mut Value,
    src: &QuerySource,
    workspace_id: &str,
    paths: impl IntoIterator<Item = &'a str>,
) {
    let (freshness, coverage) = src.annotate(paths);
    if let Some(o) = out.as_object_mut() {
        o.insert("workspace_id".into(), workspace_id.into());
        o.insert("freshness".into(), json!(freshness));
        o.insert("coverage".into(), json!(coverage));
        o.insert("generated_at".into(), generated_at().into());
    }
}

/// Arguments of a name-path lookup beyond the name path.
#[derive(Debug, Clone, Default)]
pub struct Lookup<'a> {
    pub find: FindOptions<'a>,
    pub depth: u32,
    pub include_body: bool,
    pub include_info: bool,
    pub max_matches: usize,
    pub offset: usize,
}

/// `search mode=symbol`: one page of the ranked matches, with children to `depth`,
/// info and body when asked; the refine map when there are more matches than a page
/// holds; and the ambiguity envelope.
pub fn answer(
    src: &QuerySource,
    workspace_id: &str,
    query: &str,
    lk: &Lookup<'_>,
) -> Result<Value, String> {
    let np = NamePath::parse(query)?;
    let mut out = json!({ "query": query, "name_path": np });
    let Some(opened) = src.index() else {
        out["total"] = 0.into();
        out["symbols"] = json!([]);
        out["degradations"] = json!([NEEDS_INDEX]);
        envelope(&mut out, src, workspace_id, []);
        return Ok(out);
    };
    let snap = &opened.snapshot;
    let found = snap.with_text(|conn| find(conn, &np, &lk.find))?;
    let max = lk.max_matches.clamp(1, MAX_MATCHES);
    let mut page: Vec<Symbol> = found.iter().skip(lk.offset).take(max).cloned().collect();
    let depth = lk.depth.min(MAX_DEPTH);
    if depth > 0 {
        let mut budget = MAX_CHILDREN * max;
        snap.with_text(|conn| {
            for s in &mut page {
                s.children = Some(children(conn, s.row.id, depth, &mut budget)?);
            }
            Ok(())
        })?;
    }
    let mut texts = Texts::default();
    for s in &mut page {
        slice(&mut texts, snap.root(), s, lk.include_info, lk.include_body);
    }
    let total = found.len();
    out["total"] = total.into();
    out["offset"] = lk.offset.into();
    out["omitted"] = total.saturating_sub(lk.offset + page.len()).into();
    out["ambiguous"] = (!found.is_empty() && ambiguous(&found)).into();
    out["total_candidates"] = total.into();
    out["candidates_truncated"] = (total > max).into();
    if total > max {
        let (refine, truncated) = refine_map(&found);
        out["refine"] = json!(refine);
        if truncated {
            out["refine_truncated"] = true.into();
        }
    }
    let paths: Vec<String> = page.iter().map(|s| s.path.clone()).collect();
    out["symbols"] = json!(page);
    envelope(
        &mut out,
        src,
        workspace_id,
        paths.iter().map(String::as_str),
    );
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn name_paths_parse() {
        let p = NamePath::parse("Store/Capture").unwrap();
        assert_eq!((p.absolute, p.segments.len(), p.overload), (false, 2, None));
        let p = NamePath::parse("/Outer/Inner/m[2]").unwrap();
        assert_eq!(p.absolute, true);
        assert_eq!(p.segments, ["Outer", "Inner", "m"]);
        assert_eq!(p.overload, Some(2));
        assert!(NamePath::parse("a//b").is_err());
        assert!(NamePath::parse("/").is_err());
        assert!(NamePath::parse("[1]").is_err());
        assert!(NamePath::parse("m[x]").is_err());
    }

    #[test]
    fn containers_match_relative_and_absolute() {
        let rel = NamePath::parse("Inner/m").unwrap();
        assert!(rel.containers_match("Outer.Inner.m"));
        assert!(rel.containers_match("Inner.m"));
        assert!(!rel.containers_match("Other.m"));
        let abs = NamePath::parse("/Inner/m").unwrap();
        assert!(abs.containers_match("Inner.m"));
        assert!(!abs.containers_match("Outer.Inner.m"));
        let bare = NamePath::parse("m").unwrap();
        assert!(bare.containers_match("Outer.Inner.m"));
        assert!(!NamePath::parse("/m").unwrap().containers_match("Outer.m"));
    }

    #[test]
    fn kinds_include_and_exclude() {
        let k = Kinds::parse("function, Method");
        assert!(k.admits("Function") && k.admits("method") && !k.admits("Class"));
        let k = Kinds::parse("-Variable");
        assert!(k.admits("Function") && !k.admits("variable"));
        assert!(Kinds::parse("").admits("Anything"));
    }

    #[test]
    fn snippets_are_numbered_and_masked() {
        let text = "a\nlet key = \"AKIAABCDEFGHIJKLMNOP\";\nc\nd\n";
        assert_eq!(snippet(text, 2), "1: a\n2: let key = \"[redacted]\";\n3: c");
        assert_eq!(snippet(text, 1), "1: a\n2: let key = \"[redacted]\";");
    }
}
