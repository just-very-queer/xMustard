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
/// The confidence a same-language name match carries. The reference table has no binding
/// from a reference to the declaration it uses, so a row in a language the resolver owns
/// is a name match and not a resolved edge, and it carries the matcher's own confidence
/// rather than RESOLVED_CONFIDENCE.
pub const NAME_MATCH_CONFIDENCE: f64 = LEXICAL_CONFIDENCE;

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
    /// The containing symbol's own start line, which PAR-IMP-03 asks for beside the name
    /// path and kind. Absent only at file scope, where there is no containing symbol.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub symbol_line: Option<u32>,
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
    line: u32,
    kind: u8,
    /// The containing symbol: (qualified name, kind, start line), none at file scope.
    container: Option<(String, String, u32)>,
    file_id: i64,
    blob: String,
    /// `resolved` when the reference's language has a scope resolver, else `lexical`.
    /// The reference table keeps no per-row binding, so this is the strongest claim the
    /// row's own evidence supports and nothing more (PAR-IMP-03's per-edge provenance).
    provenance: &'static str,
    confidence: f64,
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
    /// The most source files this page's snippet cache held at once. Bounded by the
    /// rows that share a file rather than by the page, so a wide page does not retain
    /// `limit` whole files (§5.1 rule 8, WS-59d-r5-2).
    pub peak_files_held: usize,
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

/// The projection and predicate one occurrence operation reads, shared by every page so
/// the pages of a list can never disagree about what the list is. Both operations
/// project the same ten columns: a unique row key first, then path, lang, file id,
/// content hash, line, kind (-1 on a declaration), the containing symbol's name, kind and
/// start line. That key is projected and ordered on because OFFSET paging needs a total
/// order: two references on one line with one kind and one container tie on every other
/// column, and a tie across a page boundary repeats or skips a row (WS-59d-r5-4). It is
/// `s.id` for a declaration and `(file_id, start_byte)`, the refs primary key, otherwise.
fn statement(q: &Query) -> String {
    match q.op.as_str() {
        "definition" => "SELECT s.id, f.path, f.lang, f.id, f.content_hash, s.start_line, -1, \
                 s.qualified_name, s.kind, s.start_line \
                 FROM symbols s JOIN names n ON n.id = s.name_id JOIN files f ON f.id = s.file_id \
                 WHERE n.name = ?1"
            .to_string(),
        op => {
            let kinds: &[u8] = if op == "implementations" {
                &TYPE_KINDS
            } else {
                &USE_KINDS
            };
            format!(
                "SELECT r.start_byte, f.path, f.lang, f.id, f.content_hash, r.line, r.kind, \
                        s.qualified_name, s.kind, s.start_line \
                 FROM refs r JOIN names n ON n.id = r.name_id JOIN files f ON f.id = r.file_id \
                 LEFT JOIN symbols s ON s.id = r.symbol_id \
                 WHERE n.name = ?1 AND r.kind IN ({})",
                kinds_clause(kinds)
            )
        }
    }
}

/// One occurrence page, read in (path, line, kind, name, row id) order and bounded by SQL,
/// so a name with a hundred thousand references costs one page of rows rather than all of
/// them (§5.1 rule 8: nothing proportional to the repository in a heap). The LIMIT is one
/// more than the page asks for, so the read learns whether more rows exist without
/// counting them all, and `more` says whether it did. The last ORDER BY term is the row's
/// unique key, so every page of a list is a stable window over one order.
fn rows(conn: &Connection, q: &Query, name: &str) -> rusqlite::Result<(Vec<Row>, bool)> {
    let sql = format!(
        "{} ORDER BY 2, 6, 7, 8, 1 LIMIT {} OFFSET {}",
        statement(q),
        q.limit + 1,
        q.offset,
    );
    let mut st = conn.prepare_cached(&sql)?;
    let mapped = st.query_map([name], |r| {
        // column 0 is the row key, which orders the page and is not part of the row
        Ok((
            r.get::<_, String>(1)?,
            r.get::<_, String>(2)?,
            r.get::<_, i64>(3)?,
            r.get::<_, String>(4)?,
            r.get::<_, u32>(5)?,
            r.get::<_, i64>(6)?,
            r.get::<_, Option<String>>(7)?,
            r.get::<_, Option<String>>(8)?,
            r.get::<_, Option<u32>>(9)?,
        ))
    })?;
    let mut out = Vec::new();
    for row in mapped {
        let (path, lang, file_id, blob, line, kind, qualified, skind, sline) = row?;
        let container = qualified.map(|q| (q, skind.unwrap_or_default(), sline.unwrap_or(line)));
        // The reference table records which symbol contains a reference, not that the
        // reference binds to the queried symbol: `symbol_id` is the container, and the
        // query matched `n.name`, so a same-language namesake is carried back exactly as
        // a bound one is. The claim this row can support is the name match, never a
        // resolved edge, so it is labelled `name_match` and carries the matcher's
        // confidence: labelling it `resolved` would put a guess in the envelope's
        // resolved count and let completeness read `exact` while a namesake's use is in
        // the list (adjudication-WS-59d-r4-3).
        let matched = resolves(&lang);
        out.push(Row {
            path,
            line,
            kind: if kind < 0 { ref_kind::BIND } else { kind as u8 },
            container,
            file_id,
            blob,
            provenance: if matched { "name_match" } else { "lexical" },
            confidence: if matched {
                NAME_MATCH_CONFIDENCE
            } else {
                LEXICAL_CONFIDENCE
            },
        });
    }
    let more = out.len() > q.limit;
    out.truncate(q.limit);
    Ok((out, more))
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
    // The most source files this page held at once. It is bounded by the rows that share
    // a file rather than by the page, which is the property the budget line needs to see;
    // without it a reader of a wide page cannot tell one file from five hundred
    // (WS-59d-r5-2, §5.1 rule 8).
    out["peak_files_held"] = serde_json::json!(read.peak_files_held);
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
    // The page is read in SQL at the cursor's offset, so a common name in a large
    // repository costs one page rather than every row of it. `total` is what the page
    // knows: rows below the cursor plus the page, and one more when more exist, so a
    // caller is told the list is longer than it can say exactly rather than given a
    // number that costs a full scan to compute.
    let (page, more) = rows(conn, q, &name)?;
    let found = declares(conn, &name)?;
    let mut texts = Texts::default();
    // One page can hold impact::MAX_LIMIT rows in that many distinct files, and each
    // file is up to MAX_REPO_FILE_BYTES, so keeping every text until the query ends would
    // retain gigabytes of source for a single page. A file's text is dropped as soon as
    // its last row is converted, which bounds the cache by the files a single row needs
    // (WS-59d-r5-2).
    let mut occurrences: Vec<Occurrence> = Vec::with_capacity(page.len());
    for (i, row) in page.iter().enumerate() {
        occurrences.push(occurrence(&mut texts, root, row));
        if page[i + 1..].iter().all(|r| r.file_id != row.file_id) {
            texts.forget(row.file_id);
        }
    }
    let end = q.offset + occurrences.len();
    let total = if more { end + 1 } else { end };
    // Reaching the offset ceiling is only truncation when the list actually continues
    // past it. A page whose window ends exactly at the ceiling and which the core says
    // has no more rows is the whole list, and calling it truncated would add a cause to
    // the envelope and turn a complete answer into a lower_bound one (ocr-review:3).
    let truncated = more || (end >= MAX_OCCURRENCES && q.offset + q.limit >= MAX_OCCURRENCES);
    // A row is one of three claims, and only a bound edge is `resolved`. A same-language
    // name match is counted separately because it is not one, and it is a lower-bound
    // cause of its own: without it a list holding only namesakes would report
    // completeness exact (adjudication-WS-59d-r4-3).
    let (resolved, lexical, name_match) =
        occurrences.iter().fold((0, 0, 0), |(r, l, m), o| match o.provenance {
            "name_match" => (r, l, m + 1),
            "resolved" => (r + 1, l, m),
            _ => (r, l + 1, m),
        });
    let mut causes: BTreeMap<String, u64> = BTreeMap::new();
    if lexical > 0 {
        causes.insert("lexical_occurrences".to_string(), lexical as u64);
    }
    // A name match is not a bound edge either, so it is a lower-bound cause of its own
    // rather than being counted as resolved: without it a list holding only namesakes
    // would report completeness exact (adjudication-WS-59d-r4-3).
    if name_match > 0 {
        causes.insert("name_match_occurrences".to_string(), name_match as u64);
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
        // The same vocabulary the blast form reports, over the same rows: a list of
        // name matches is a lexical list, never a resolved one, because no row in it is
        // a bound edge (adjudication-WS-59d-r4-3).
        graph: match (resolved > 0, lexical + name_match > 0) {
            (true, true) => "mixed",
            (true, false) => "resolved",
            (false, true) => "lexical",
            (false, false) => "none",
        },
        total,
        offset: q.offset,
        occurrences,
        // The cursor never advertises a position the flag parser would refuse: the
        // offset bound is the same one, so a list that reaches it ends here rather than
        // issuing a continuation that cannot be followed.
        next_cursor: (more && end < MAX_OCCURRENCES).then(|| end.to_string()),
        truncated,
        peak_files_held: texts.peak_files(),
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

/// The name an occurrence query is about.
///
/// The reference table is keyed by the bare name and its rows carry no symbol id, so a
/// uid, a `Container.name` path and a bare name all reduce to the name the reference rows
/// are keyed by, and every namesake's use comes back. Narrowing a read to one of several
/// same-named declarations would need a binding the table does not hold: a package or class
/// qualifier is not a symbol in this index (measured 2026-10-04 on the box: no symbol is
/// named `alpha` for package `alpha`), and a method's recorded qualifier is its receiver
/// variable (`s.Put` records `s`, not `Store`), so neither can be checked against the
/// container a name path names. The rows are therefore reported as the name match they
/// are, each carrying its own provenance and the snippet the caller reads to judge it, and
/// the envelope's lexical cause is what tells a caller the list is not a proof. A target
/// the index does not hold reads as the name itself, which is an empty answer, not an
/// error: a symbol outside the index is a fact about the index, not a malformed request.
fn resolve_target(conn: &Connection, target: &str) -> rusqlite::Result<String> {
    if let Some(name) = name_of_symbol(conn, target)? {
        return Ok(name);
    }
    Ok(match target.rsplit_once('.') {
        Some((_, member)) => member.to_string(),
        None => match target.rsplit_once('/') {
            Some((_, base)) => base.to_string(),
            None => target.to_string(),
        },
    })
}

/// The name of the symbol a uid identifies, if the index holds it.
fn name_of_symbol(conn: &Connection, uid: &str) -> rusqlite::Result<Option<String>> {
    conn.prepare_cached(
        "SELECT n.name FROM symbols s JOIN names n ON n.id = s.name_id WHERE s.uid = ?1 LIMIT 1",
    )?
    .query_row([uid], |r| r.get(0))
    .optional()
}

fn occurrence(texts: &mut Texts, root: &Path, row: &Row) -> Occurrence {
    let (name, kind, symbol_line) = match &row.container {
        Some((qualified, kind, start)) => (qualified.clone(), kind.clone(), Some(*start)),
        None => (row.path.clone(), "File".to_string(), None),
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
        symbol_line,
        kind_of_reference: reference_kind(row.kind),
        relation: match row.kind {
            ref_kind::EXTENDS => Some("EXTENDS"),
            ref_kind::IMPLEMENTS => Some("IMPLEMENTS"),
            _ => None,
        },
        snippet,
        snippet_unavailable,
        provenance: row.provenance,
        confidence: row.confidence,
    }
}

/// The operations, for the core's usage line.
pub fn usage_operations() -> String {
    OPERATIONS.join("|")
}
