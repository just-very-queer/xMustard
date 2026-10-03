//! Symbol-level occurrences for `impact op=refs|definition|implementations`
//! (PAR-IMP-03). One row per place the name occurs: the containing symbol's name
//! path, kind and line, a ±1-line snippet of the reference itself, and the edge's own
//! provenance and confidence.
//!
//! The read is over the index's `refs` table (disk-backed, keyed by file and byte
//! offset), which is where the occurrence *lines* live — the blast walk's graph
//! carries symbols but no reference lines, so the two answers cannot be derived from
//! one another. Rows are read in pages ordered by (path, line, col), so `limit` and a
//! cursor continue the same list; nothing proportional to the repository is held in
//! the heap beyond one page plus the file texts a page's snippets need.
//!
//! - `refs`: every use of the name. The index records a reference row only for a
//!   *use*, so a declaration is already absent and needs no filter: what a name is
//!   declared by is `definition`'s answer, not a row here.
//! - `definition`: the symbols that declare the name.
//! - `implementations`: the `refs` rows of kind EXTENDS or IMPLEMENTS, which only a
//!   language that writes the relation down produces (TypeScript's `implements`); a Go
//!   struct satisfies an interface by having its methods, which is no reference to the
//!   interface's name, so an implementation list is empty there.
//!
//! Provenance is `resolved` when the reference's file is in a language the scope
//! resolver owns and its `symbol_id` names a symbol, else `lexical` — a name match,
//! a lead, never proof. A reference with no containing symbol is reported with
//! `kind: "File"`, as the blast form's own items are.

use std::collections::BTreeMap;
use std::path::Path;

use rusqlite::{Connection, OptionalExtension};
use serde::Serialize;

use super::facts::ref_kind;
use super::namepath::{self, Texts};
use super::resolve::resolves;

/// The operations `impact op=` names, in the order the tool documents them.
pub const OPERATIONS: [&str; 3] = ["refs", "definition", "implementations"];

/// Reference kinds that are a use of a name (not a binding, import or export).
const USE_KINDS: [u8; 9] = [
    ref_kind::READ,
    ref_kind::CALL,
    ref_kind::TYPE,
    ref_kind::WRITE,
    ref_kind::EXTENDS,
    ref_kind::IMPLEMENTS,
    ref_kind::MEMBER,
    ref_kind::MEMBER_CALL,
    ref_kind::WORD,
];

/// The kinds `implementations` keeps.
const TYPE_KINDS: [u8; 2] = [ref_kind::EXTENDS, ref_kind::IMPLEMENTS];

/// The confidence a resolved occurrence carries. A resolved reference is a bound edge,
/// not a guess, so it is exact; every lexical occurrence carries the matcher's own
/// confidence, which is below it.
pub const RESOLVED_CONFIDENCE: f64 = 1.0;
/// A lexical name match's confidence, the same value the legacy graph's lexical edges
/// carry (index::edges).
pub const LEXICAL_CONFIDENCE: f64 = 0.5;

/// Occurrences listed at most, so one call cannot stream a whole repository's rows.
pub const MAX_OCCURRENCES: usize = 5000;

/// What one occurrence is.
#[derive(Debug, Clone, Serialize)]
pub struct Occurrence {
    /// The containing symbol's name path (`Container.name`), or the file path when the
    /// reference is at file scope.
    pub name: String,
    /// The containing symbol's kind, or `File`.
    pub kind: String,
    pub path: String,
    /// The reference's own line (not the containing symbol's declaration line).
    pub line: u32,
    /// The reference kind: `call`, `read`, `extends`, `implementations` and so on.
    pub kind_of_reference: &'static str,
    /// `IMPLEMENTS` or `EXTENDS` on an implementations row; absent otherwise.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub relation: Option<&'static str>,
    /// The ±1 lines around `line`, numbered, secret-masked and clipped. Absent when the
    /// file could not be read, and `snippet_unavailable` says why.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snippet: Option<String>,
    /// Why the snippet is absent (`file unreadable`, `secret path`, …).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snippet_unavailable: Option<&'static str>,
    /// `resolved` when the resolver bound the reference, else `lexical`.
    pub provenance: &'static str,
    pub confidence: f64,
}

/// What one occurrence query is.
#[derive(Debug, Clone)]
pub struct Query {
    /// The symbol, `Container.name` or UID the occurrences are of.
    pub target: String,
    /// One of [`OPERATIONS`].
    pub op: String,
    /// Occurrences listed; the cursor continues past it.
    pub limit: usize,
    /// Where the cursor continues, as a row ordinal within the whole list.
    pub offset: usize,
}

impl Query {
    pub fn new(target: &str, op: &str) -> Query {
        Query {
            target: target.to_string(),
            op: op.to_string(),
            limit: super::impact::DEFAULT_LIMIT,
            offset: 0,
        }
    }

    /// Apply `--name=value` flags.
    pub fn apply_flags<I: IntoIterator<Item = String>>(&mut self, flags: I) -> Result<(), String> {
        for flag in flags {
            let (name, value) = flag
                .strip_prefix("--")
                .and_then(|f| f.split_once('='))
                .ok_or_else(|| format!("expected --name=value, got {flag:?}"))?;
            match name {
                "op" => {
                    if !OPERATIONS.contains(&value) {
                        return Err(format!(
                            "unknown operation {value:?}; use {}",
                            OPERATIONS.join(", ")
                        ));
                    }
                    self.op = value.to_string();
                }
                "limit" => {
                    self.limit = bounded(value, 1, super::impact::MAX_LIMIT as u64)? as usize;
                }
                "offset" => {
                    self.offset = bounded(value, 0, MAX_OCCURRENCES as u64)? as usize;
                }
                // the cursor is an offset in the occurrence list, and its own signature
                // is checked by the caller (the Go side, cursorCodec)
                "cursor" => {
                    self.offset = bounded(value, 0, MAX_OCCURRENCES as u64)? as usize;
                }
                other => return Err(format!("unknown occurrences flag --{other}")),
            }
        }
        Ok(())
    }
}

fn bounded(v: &str, lo: u64, hi: u64) -> Result<u64, String> {
    let n: u64 = v
        .parse()
        .map_err(|_| format!("{v:?} is not a whole number"))?;
    if n < lo || n > hi {
        return Err(format!("must be between {lo} and {hi}, got {n}"));
    }
    Ok(n)
}

/// The reference kind's name, as the row reports it.
fn reference_kind(kind: u8) -> &'static str {
    match kind {
        ref_kind::READ => "read",
        ref_kind::CALL => "call",
        ref_kind::TYPE => "type",
        ref_kind::WRITE => "write",
        ref_kind::IMPORT => "import",
        ref_kind::EXTENDS => "extends",
        ref_kind::IMPLEMENTS => "implements",
        ref_kind::BIND => "bind",
        ref_kind::MEMBER => "member",
        ref_kind::MEMBER_CALL => "member_call",
        ref_kind::EXPORT => "export",
        ref_kind::WORD => "word",
        _ => "unknown",
    }
}

/// One row of the occurrence table, before its snippet is read.
struct Row {
    path: String,
    lang: String,
    line: u32,
    kind: u8,
    /// The containing symbol: (qualified name, kind, line), none at file scope.
    container: Option<(String, String, u32)>,
    file_id: i64,
    blob: String,
}

/// The occurrence answer.
#[derive(Debug, Clone, Serialize)]
pub struct Occurrences {
    /// The query.
    pub target: String,
    /// The operation that was read.
    pub op: &'static str,
    /// The name the occurrences were read for (a UID and a path resolve to the name).
    pub resolved_name: String,
    /// Whether the name is in the index at all.
    pub found: bool,
    /// `resolved`, `lexical`, `mixed` or `none`, as the blast form reports it.
    pub graph: &'static str,
    /// Every occurrence, before `limit`.
    pub total: usize,
    /// Where this page starts, so a client can say what it holds.
    pub offset: usize,
    /// The page.
    pub occurrences: Vec<Occurrence>,
    /// Continues the list at `offset + occurrences.len()` as a bare offset, or absent
    /// when the page is the last one. The Go layer wraps this in its own signed
    /// cursor (cursorCodec) before it reaches a client, so a client can neither edit a
    /// number nor reuse it for another query; the core's own offset is not a cursor.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_cursor: Option<String>,
    /// Occurrences dropped because the read is bounded (`MAX_OCCURRENCES`).
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
    pub epistemic: Envelope,
}

/// How complete the occurrence list is (PAR-IMP-05's envelope, over the same causes
/// the blast walk reports: a name match, an unbound reference, a cut list).
#[derive(Debug, Clone, Serialize, Default)]
pub struct Envelope {
    /// `exact` | `lower_bound`.
    pub completeness: &'static str,
    /// Occurrences by cause; coverage losses appear as `coverage:<reason>`.
    pub causes: BTreeMap<String, u64>,
}

fn kinds_clause(kinds: &[u8]) -> String {
    kinds
        .iter()
        .map(|k| k.to_string())
        .collect::<Vec<_>>()
        .join(",")
}

/// Every occurrence row of the query, before paging: (path, line, col) order, so the
/// same index answers the same page every time and the cursor means a position.
fn rows(conn: &Connection, q: &Query, name: &str) -> rusqlite::Result<Vec<Row>> {
    let mut out = Vec::new();
    match q.op.as_str() {
        "definition" => {
            let mut st = conn.prepare_cached(
                "SELECT f.path, f.lang, f.id, f.content_hash, s.start_line, s.kind, s.qualified_name \
                 FROM symbols s JOIN names n ON n.id = s.name_id JOIN files f ON f.id = s.file_id \
                 WHERE n.name = ?1 ORDER BY f.path, s.start_line, s.kind, s.qualified_name",
            )?;
            let rows = st.query_map([name], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, i64>(2)?,
                    r.get::<_, String>(3)?,
                    r.get::<_, u32>(4)?,
                    r.get::<_, String>(5)?,
                    r.get::<_, String>(6)?,
                ))
            })?;
            for row in rows {
                let (path, lang, file_id, blob, line, kind, qualified) = row?;
                out.push(Row {
                    path,
                    lang,
                    line,
                    kind: ref_kind::BIND,
                    container: Some((qualified, kind, line)),
                    file_id,
                    blob,
                });
            }
        }
        op => {
            let kinds: &[u8] = if op == "implementations" {
                &TYPE_KINDS
            } else {
                &USE_KINDS
            };
            let sql = format!(
                "SELECT f.path, f.lang, f.id, f.content_hash, r.line, r.kind, r.symbol_id, \
                        s.qualified_name, s.kind, s.start_line \
                 FROM refs r JOIN names n ON n.id = r.name_id JOIN files f ON f.id = r.file_id \
                 LEFT JOIN symbols s ON s.id = r.symbol_id \
                 WHERE n.name = ?1 AND r.kind IN ({}) \
                 ORDER BY f.path, r.line, r.col, r.start_byte",
                kinds_clause(kinds)
            );
            let mut st = conn.prepare_cached(&sql)?;
            let rows = st.query_map([name], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, i64>(2)?,
                    r.get::<_, String>(3)?,
                    r.get::<_, u32>(4)?,
                    r.get::<_, u8>(5)?,
                    r.get::<_, Option<i64>>(6)?,
                    r.get::<_, Option<String>>(7)?,
                    r.get::<_, Option<String>>(8)?,
                    r.get::<_, Option<u32>>(9)?,
                ))
            })?;
            for row in rows {
                let (path, lang, file_id, blob, line, kind, symbol, qualified, skind, sline) = row?;
                out.push(Row {
                    path,
                    lang,
                    line,
                    kind,
                    container: match (symbol, qualified, skind, sline) {
                        (Some(_), Some(q), Some(k), Some(l)) => Some((q, k, l)),
                        _ => None,
                    },
                    file_id,
                    blob,
                });
            }
        }
    }
    Ok(out)
}

/// The index's own record of the name: the spans and whether any symbol declares it.
fn declares(conn: &Connection, name: &str) -> rusqlite::Result<bool> {
    conn.prepare_cached(
        "SELECT EXISTS(SELECT 1 FROM symbols s JOIN names n ON n.id = s.name_id WHERE n.name = ?1)",
    )?
    .query_row([name], |r| r.get(0))
}

/// Answer an occurrence query over `src`'s index, with the source's freshness and
/// coverage. Occurrences live in the index's reference table, so a root with no index
/// answers `found: false` with the same degradation the explain read reports rather
/// than walking the legacy graph, which has no reference lines (PAR-IMP-03's note on
/// `GraphEdge`).
pub fn answer(
    src: &crate::symbolgraph::QuerySource,
    workspace_id: &str,
    q: &Query,
) -> Result<serde_json::Value, String> {
    let Some(opened) = src.index() else {
        let mut out = serde_json::json!({
            "target": q.target, "op": q.op, "found": false,
            "total": 0, "occurrences": [],
            "degradations": [namepath::NEEDS_INDEX],
        });
        namepath::envelope(&mut out, src, workspace_id, []);
        return Ok(out);
    };
    let snap = &opened.snapshot;
    let losses = snap.coverage.loss_counts.clone();
    let read = snap.with_text(|conn| read(conn, snap.root(), q, &losses))?;
    let mut out = serde_json::to_value(&read).map_err(|e| e.to_string())?;
    let paths: Vec<&str> = read.occurrences.iter().map(|o| o.path.as_str()).collect();
    namepath::envelope(&mut out, src, workspace_id, paths);
    Ok(out)
}

/// Read `q`'s occurrences. `root` is where the snippets' text is read from. The
/// operation is checked again here, not only by the flag parser, because this is the
/// read that chooses what a row is and a closed set must fail closed (§5.1 rule 7).
pub fn read(
    conn: &Connection,
    root: &Path,
    q: &Query,
    losses: &BTreeMap<String, usize>,
) -> rusqlite::Result<Occurrences> {
    if !OPERATIONS.contains(&q.op.as_str()) {
        return Err(rusqlite::Error::InvalidParameterName(q.op.clone()));
    }
    // The target may be a name, a `Container.name` name path or a UID; the reference
    // table is keyed by the bare name, so resolve the path or UID to one.
    let name = resolve_target(conn, &q.target)?;
    let all = rows(conn, q, &name)?;
    let total = all.len();
    let truncated = total > MAX_OCCURRENCES;
    let page = &all[q.offset.min(total)..(q.offset + q.limit).min(total)];
    let found = declares(conn, &name)?;
    let mut texts = Texts::default();
    let occurrences: Vec<Occurrence> = page
        .iter()
        .map(|row| occurrence(&mut texts, root, row))
        .collect();
    let end = q.offset + occurrences.len();
    let (bound, lexical) = occurrences.iter().fold((0, 0), |(r, l), o| {
        if o.provenance == "resolved" {
            (r + 1, l)
        } else {
            (r, l + 1)
        }
    });
    let mut causes: BTreeMap<String, u64> = BTreeMap::new();
    if lexical > 0 {
        causes.insert("lexical_occurrences".to_string(), lexical as u64);
    }
    let unresolved = unresolved_references(conn, &name)?;
    if unresolved > 0 {
        causes.insert("unresolved_references".to_string(), unresolved as u64);
    }
    if truncated {
        causes.insert("truncated".to_string(), 1);
    }
    for (reason, n) in losses {
        *causes.entry(format!("coverage:{reason}")).or_default() += *n as u64;
    }
    let epistemic = Envelope {
        completeness: if causes.is_empty() {
            "exact"
        } else {
            "lower_bound"
        },
        causes,
    };
    Ok(Occurrences {
        target: q.target.clone(),
        op: OPERATIONS
            .iter()
            .copied()
            .find(|o| *o == q.op)
            .unwrap_or("refs"),
        resolved_name: name,
        found,
        graph: match (bound > 0, lexical > 0) {
            (true, true) => "mixed",
            (true, false) => "resolved",
            (false, true) => "lexical",
            (false, false) => "none",
        },
        total,
        offset: q.offset,
        occurrences,
        next_cursor: (end < total).then(|| end.to_string()),
        truncated,
        epistemic,
    })
}

fn unresolved_references(conn: &Connection, name: &str) -> rusqlite::Result<i64> {
    conn.prepare_cached(
        "SELECT COALESCE(SUM(r.count), 0) FROM resolve_drops r JOIN names n ON n.id = r.name_id \
         WHERE n.name = ?1",
    )?
    .query_row([name], |r| r.get(0))
}

/// The name an occurrence query is about. The reference table is keyed by the bare
/// name, so a UID, a `Container.name` name path and a path each reduce to one. A
/// target the index does not hold is its own name and an empty answer, not an error:
/// a symbol outside the index is a fact about the index, not a malformed request.
fn resolve_target(conn: &Connection, target: &str) -> rusqlite::Result<String> {
    let by_uid = |t: &str| -> rusqlite::Result<Option<String>> {
        conn.prepare_cached(
            "SELECT n.name FROM symbols s JOIN names n ON n.id = s.name_id WHERE s.uid = ?1 LIMIT 1",
        )?
        .query_row([t], |r| r.get(0))
        .optional()
    };
    if let Some(name) = by_uid(target)? {
        return Ok(name);
    }
    // the name the target names: the trailing member of a name path, or the base name
    // of a path. Both are the same lookup over one column, so they are one table.
    let base = || match (target.rsplit_once('.'), target.rsplit_once('/')) {
        (Some((_, tail)), _) => Some(tail),
        (None, Some((_, base))) => Some(base),
        (None, None) => None,
    };
    match base().filter(|b| *b != target) {
        Some(b) => Ok(by_uid(b)?.unwrap_or_else(|| b.to_string())),
        None => Ok(target.to_string()),
    }
}

fn occurrence(texts: &mut Texts, root: &Path, row: &Row) -> Occurrence {
    let resolved = row.container.is_some() && resolves(&row.lang);
    let (name, kind) = match &row.container {
        Some((qualified, kind, _)) => (qualified.clone(), kind.clone()),
        None => (row.path.clone(), "File".to_string()),
    };
    let (snippet, snippet_unavailable) = match texts.file(root, row.file_id, &row.path, &row.blob) {
        Ok(text) => {
            let s = namepath::snippet(text, row.line);
            if s.is_empty() {
                (None, Some("line not in the indexed text"))
            } else {
                (Some(s), None)
            }
        }
        Err(why) => (None, Some(why)),
    };
    Occurrence {
        name,
        kind,
        path: row.path.clone(),
        line: row.line,
        kind_of_reference: reference_kind(row.kind),
        relation: match row.kind {
            ref_kind::EXTENDS => Some("EXTENDS"),
            ref_kind::IMPLEMENTS => Some("IMPLEMENTS"),
            _ => None,
        },
        snippet,
        snippet_unavailable,
        provenance: if resolved { "resolved" } else { "lexical" },
        confidence: if resolved {
            RESOLVED_CONFIDENCE
        } else {
            LEXICAL_CONFIDENCE
        },
    }
}

/// The operations, for the core's usage line.
pub fn usage_operations() -> String {
    OPERATIONS.join("|")
}
