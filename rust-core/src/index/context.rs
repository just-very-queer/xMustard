//! A symbol's context over the code index (WS-36, PAR-SYM-04): who references it and
//! what it references, grouped by relation, layered to `chain_depth`, with the
//! epistemic envelope of how complete that is.
//!
//! Resolved symbol edges (`edges.layer = 'symbol'`, Go and TS/JS: CALLS, USES,
//! ACCESSES, EXTENDS, IMPLEMENTS, HAS_METHOD, HAS_PROPERTY) answer both directions and
//! the deeper layers. A symbol of a language the resolver does not cover gets its
//! direct incoming references from the reference table by name instead (provenance
//! `lexical`, relation by reference kind): leads, not proof. Incoming references carry
//! the first line that references the symbol and, for the first few, a ±1 snippet
//! sliced from the referring file.

use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::Path;

use rusqlite::{Connection, params};
use serde::Serialize;

use super::facts::ref_kind;
use super::namepath::{self, FindOptions, NamePath, Symbol, Texts};
use crate::symbolgraph::QuerySource;

/// Deepest layer past the direct one.
pub const MAX_CHAIN_DEPTH: u32 = 3;
/// Edges listed per direction (the counts cover them all).
pub const MAX_EDGES: usize = 200;
/// Incoming references that get a snippet.
const MAX_SNIPPETS: usize = 10;
/// Confidence of a name-only (lexical) reference.
const LEXICAL_CONFIDENCE: f64 = 0.5;

/// The relation a lexical reference kind stands for.
fn lexical_relation(kind: u8) -> &'static str {
    match kind {
        ref_kind::CALL | ref_kind::MEMBER_CALL => "CALLS",
        ref_kind::EXTENDS => "EXTENDS",
        ref_kind::IMPLEMENTS => "IMPLEMENTS",
        ref_kind::TYPE => "USES",
        ref_kind::WRITE => "ACCESSES",
        _ => "REFERENCES",
    }
}

/// Reference kinds that name a use of a symbol (not a binding, import or export).
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

/// One edge of the context: the symbol at its other end.
#[derive(Debug, Clone, Serialize)]
pub struct Edge {
    pub relation: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub access: Option<String>,
    pub layer: u32,
    pub name_path: String,
    pub kind: String,
    pub path: String,
    /// Incoming: the first line referencing the symbol; outgoing: the target's line.
    pub line: u32,
    pub uid: String,
    pub confidence: f64,
    pub provenance: String,
    pub weight: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub snippet: Option<String>,
}

/// How complete the context is.
#[derive(Debug, Clone, Serialize)]
pub struct Epistemic {
    /// `exact` when every incoming reference was resolved, else `lower_bound`.
    pub completeness: &'static str,
    /// The symbol's language has the scope resolver.
    pub resolved_language: bool,
    pub incoming_resolved: usize,
    pub incoming_lexical: usize,
    /// References to the symbol's name the resolver could not bind.
    pub unresolved_references: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub causes: Vec<String>,
}

/// A symbol's context.
#[derive(Debug, Clone, Serialize)]
pub struct Context {
    pub symbol: Symbol,
    pub chain_depth: u32,
    pub incoming: Vec<Edge>,
    pub outgoing: Vec<Edge>,
    pub incoming_by_relation: BTreeMap<String, usize>,
    pub outgoing_by_relation: BTreeMap<String, usize>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
    pub epistemic: Epistemic,
}

/// A symbol edge row: the symbol at the other end, the kind and its evidence.
struct Link {
    other: i64,
    kind: String,
    access: Option<String>,
    confidence: f64,
    provenance: String,
    weight: i64,
}

fn links(conn: &Connection, sql: &str, id: i64) -> rusqlite::Result<Vec<Link>> {
    let mut st = conn.prepare_cached(sql)?;
    st.query_map([id], |r| {
        Ok(Link {
            other: r.get(0)?,
            kind: r.get(1)?,
            access: r.get(2)?,
            confidence: r.get(3)?,
            provenance: r.get(4)?,
            weight: r.get(5)?,
        })
    })?
    .collect()
}

const INCOMING: &str = "SELECT src_symbol, kind, access, confidence, provenance, weight FROM edges \
     WHERE layer = 'symbol' AND dst_symbol = ?1 AND src_symbol IS NOT NULL ORDER BY src_symbol, kind";
const OUTGOING: &str = "SELECT dst_symbol, kind, access, confidence, provenance, weight FROM edges \
     WHERE layer = 'symbol' AND src_symbol = ?1 AND dst_symbol IS NOT NULL ORDER BY dst_symbol, kind";

/// The context of `target`: incoming and outgoing edges to `chain_depth` extra layers.
pub fn context(
    conn: &Connection,
    root: &Path,
    mut target: Symbol,
    chain_depth: u32,
    include_content: bool,
) -> rusqlite::Result<Context> {
    let chain_depth = chain_depth.min(MAX_CHAIN_DEPTH);
    let mut texts = Texts::default();
    namepath::slice(&mut texts, root, &mut target, true, include_content);
    let resolved_language = super::resolve::resolves(&target.row.lang);
    let mut truncated = false;
    let mut incoming = layered(conn, target.row.id, INCOMING, chain_depth, &mut truncated)?;
    let lexical = if resolved_language {
        Vec::new()
    } else {
        lexical_incoming(conn, &target, &mut truncated)?
    };
    let (incoming_resolved, incoming_lexical) = (
        incoming.iter().filter(|e| e.layer == 1).count(),
        lexical.len(),
    );
    incoming.extend(lexical);
    let outgoing = layered(conn, target.row.id, OUTGOING, chain_depth, &mut truncated)?;
    reference_lines(conn, &target, &mut incoming)?;
    for e in incoming
        .iter_mut()
        .filter(|e| e.layer == 1)
        .take(MAX_SNIPPETS)
    {
        if let Some((id, blob)) = file_of(conn, &e.path)?
            && let Ok(text) = texts.file(root, id, &e.path, &blob)
        {
            e.snippet = Some(namepath::snippet(text, e.line));
        }
    }
    let unresolved_references: i64 = conn
        .prepare_cached(
            "SELECT COALESCE(SUM(d.count), 0) FROM resolve_drops d JOIN names n ON n.id = d.name_id \
             WHERE n.name = ?1",
        )?
        .query_row([&target.name], |r| r.get(0))?;
    let mut causes = Vec::new();
    if !resolved_language {
        causes.push(format!(
            "{} has no scope resolver: incoming references are name matches",
            target.row.lang
        ));
    }
    if unresolved_references > 0 {
        causes.push(format!(
            "{unresolved_references} reference(s) to the name {} were not bound",
            target.name
        ));
    }
    if truncated {
        causes.push(format!(
            "edges past {MAX_EDGES} per direction are not listed"
        ));
    }
    Ok(Context {
        incoming_by_relation: by_relation(&incoming),
        outgoing_by_relation: by_relation(&outgoing),
        symbol: target,
        chain_depth,
        truncated,
        epistemic: Epistemic {
            completeness: if causes.is_empty() {
                "exact"
            } else {
                "lower_bound"
            },
            resolved_language,
            incoming_resolved,
            incoming_lexical,
            unresolved_references,
            causes,
        },
        incoming,
        outgoing,
    })
}

fn file_of(conn: &Connection, path: &str) -> rusqlite::Result<Option<(i64, String)>> {
    use rusqlite::OptionalExtension;
    conn.prepare_cached("SELECT id, content_hash FROM files WHERE path = ?1")?
        .query_row([path], |r| Ok((r.get(0)?, r.get(1)?)))
        .optional()
}

fn by_relation(edges: &[Edge]) -> BTreeMap<String, usize> {
    let mut out = BTreeMap::new();
    for e in edges {
        *out.entry(e.relation.clone()).or_default() += 1;
    }
    out
}

/// Breadth-first layers over resolved symbol edges from `start`: layer 1 is direct,
/// each further layer (to 1 + chain_depth) the edges of the previous layer's symbols.
/// A symbol sits in the nearest layer that reaches it, with one edge per relation.
fn layered(
    conn: &Connection,
    start: i64,
    sql: &str,
    chain_depth: u32,
    truncated: &mut bool,
) -> rusqlite::Result<Vec<Edge>> {
    let mut out: Vec<Edge> = Vec::new();
    let mut visited: HashSet<i64> = HashSet::from([start]);
    let mut frontier = vec![start];
    for layer in 1..=1 + chain_depth {
        let mut next = Vec::new();
        let mut emitted: HashSet<(i64, String, Option<String>)> = HashSet::new();
        for id in &frontier {
            for l in links(conn, sql, *id)? {
                if visited.contains(&l.other)
                    || !emitted.insert((l.other, l.kind.clone(), l.access.clone()))
                {
                    continue;
                }
                if out.len() >= MAX_EDGES {
                    *truncated = true;
                    return Ok(out);
                }
                let Some(s) = namepath::query(conn, "s.id = ?1", [l.other])?
                    .into_iter()
                    .next()
                else {
                    continue;
                };
                if !next.contains(&l.other) {
                    next.push(l.other);
                }
                out.push(edge(s, l, layer));
            }
        }
        visited.extend(next.iter().copied());
        frontier = next;
    }
    Ok(out)
}

fn edge(s: Symbol, l: Link, layer: u32) -> Edge {
    Edge {
        relation: l.kind,
        access: l.access,
        layer,
        name_path: s.name_path,
        kind: s.kind,
        line: s.line,
        path: s.path,
        uid: s.uid,
        confidence: l.confidence,
        provenance: l.provenance,
        weight: l.weight,
        snippet: None,
    }
}

/// Direct references to the target's name from symbols of other spans, grouped by
/// referring symbol (a reference at file scope has none and is left out).
fn lexical_incoming(
    conn: &Connection,
    target: &Symbol,
    truncated: &mut bool,
) -> rusqlite::Result<Vec<Edge>> {
    let kinds = USE_KINDS.map(|k| k.to_string()).join(",");
    let mut st = conn.prepare_cached(&format!(
        "SELECT r.symbol_id, r.kind, MIN(r.line), COUNT(*) FROM refs r JOIN names n ON n.id = r.name_id \
         WHERE n.name = ?1 AND r.symbol_id IS NOT NULL AND r.kind IN ({kinds}) \
         AND NOT (r.file_id = ?2 AND r.line BETWEEN ?3 AND ?4) \
         GROUP BY r.symbol_id, r.kind ORDER BY r.symbol_id, r.kind"
    ))?;
    let rows: Vec<(i64, u8, u32, i64)> = st
        .query_map(
            params![
                target.name,
                target.row.file_id,
                target.lines[0],
                target.lines[1]
            ],
            |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
        )?
        .collect::<rusqlite::Result<_>>()?;
    let mut out: Vec<Edge> = Vec::new();
    let mut seen: HashMap<(i64, &'static str), usize> = HashMap::new();
    for (id, kind, line, count) in rows {
        let relation = lexical_relation(kind);
        if let Some(&i) = seen.get(&(id, relation)) {
            out[i].weight += count;
            out[i].line = out[i].line.min(line);
            continue;
        }
        if out.len() >= MAX_EDGES {
            *truncated = true;
            break;
        }
        let Some(s) = namepath::query(conn, "s.id = ?1", [id])?.into_iter().next() else {
            continue;
        };
        seen.insert((id, relation), out.len());
        let mut e = edge(
            s,
            Link {
                other: id,
                kind: relation.to_string(),
                access: None,
                confidence: LEXICAL_CONFIDENCE,
                provenance: "lexical".to_string(),
                weight: count,
            },
            1,
        );
        e.line = line;
        out.push(e);
    }
    Ok(out)
}

/// For each direct resolved incoming edge, the first line in the referrer that names
/// the target (the edge row has no line).
fn reference_lines(
    conn: &Connection,
    target: &Symbol,
    incoming: &mut [Edge],
) -> rusqlite::Result<()> {
    let mut st = conn.prepare_cached(
        "SELECT MIN(r.line) FROM refs r JOIN names n ON n.id = r.name_id JOIN symbols s ON s.id = r.symbol_id \
         WHERE s.uid = ?1 AND n.name = ?2",
    )?;
    for e in incoming
        .iter_mut()
        .filter(|e| e.layer == 1 && e.provenance != "lexical")
    {
        let line: Option<u32> = st.query_row(params![e.uid, target.name], |r| r.get(0))?;
        if let Some(line) = line {
            e.line = line;
        }
    }
    Ok(())
}

/// Ambiguity candidates listed when a name path names more than one symbol.
const MAX_CANDIDATES: usize = 20;

/// What a context target names.
enum Target {
    Found(Box<Symbol>),
    Ambiguous(Vec<Symbol>),
    Missing,
}

/// A UID names one symbol; a name path is looked up and must name one (overloads of
/// one name path in one file take the first).
fn resolve_target(conn: &Connection, target: &str) -> rusqlite::Result<Result<Target, String>> {
    if let Some(s) = namepath::by_uid(conn, target)? {
        return Ok(Ok(Target::Found(Box::new(s))));
    }
    let np = match NamePath::parse(target) {
        Ok(np) => np,
        Err(e) => return Ok(Err(e)),
    };
    let (found, _) = namepath::find(conn, &np, &FindOptions::default())?;
    Ok(Ok(match found.first() {
        None => Target::Missing,
        Some(_) if namepath::ambiguous(&found) => Target::Ambiguous(found),
        Some(first) => Target::Found(Box::new(first.clone())),
    }))
}

/// `explain symbol=` (a name path or a UID): the symbol's context, or the ranked
/// candidates to pick a UID from when the name path is ambiguous.
pub fn answer(
    src: &QuerySource,
    workspace_id: &str,
    target: &str,
    chain_depth: u32,
    include_content: bool,
) -> Result<serde_json::Value, String> {
    let Some(opened) = src.index() else {
        let mut out = serde_json::json!({ "query": target, "found": false, "degradations": [namepath::NEEDS_INDEX] });
        namepath::envelope(&mut out, src, workspace_id, []);
        return Ok(out);
    };
    let snap = &opened.snapshot;
    let resolved = snap.with_text(|conn| resolve_target(conn, target))??;
    let mut out = match resolved {
        Target::Missing => serde_json::json!({ "query": target, "found": false }),
        Target::Ambiguous(all) => {
            let total = all.len();
            let candidates: Vec<Symbol> = all.into_iter().take(MAX_CANDIDATES).collect();
            serde_json::json!({
                "query": target, "found": false, "ambiguous": true, "total_candidates": total,
                "candidates_truncated": total > candidates.len(), "candidates": candidates,
                "hint": "pass one candidate's uid as symbol=, or a longer name path",
            })
        }
        Target::Found(s) => {
            let ctx = snap
                .with_text(|conn| context(conn, snap.root(), *s, chain_depth, include_content))?;
            let mut v = serde_json::to_value(&ctx).map_err(|e| e.to_string())?;
            v["query"] = target.into();
            v["found"] = true.into();
            v
        }
    };
    let paths: Vec<String> = ["symbol", "incoming", "outgoing"]
        .iter()
        .flat_map(|k| match &out[*k] {
            serde_json::Value::Array(a) => a
                .iter()
                .filter_map(|e| e["path"].as_str().map(String::from))
                .collect::<Vec<_>>(),
            v => v["path"]
                .as_str()
                .map(String::from)
                .into_iter()
                .collect::<Vec<_>>(),
        })
        .collect();
    namepath::envelope(
        &mut out,
        src,
        workspace_id,
        paths.iter().map(String::as_str),
    );
    Ok(out)
}
