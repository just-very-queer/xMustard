//! `index impact`: the dependents of a symbol over the resolved symbol edges, with the
//! epistemic completeness envelope (PAR-IMP-01, PAR-IMP-05).
//!
//! Dependents are walked backwards over the symbol layer (CALLS, USES, ACCESSES,
//! EXTENDS, IMPLEMENTS, IMPORTS). A symbol in a language the resolver does not own
//! falls back to the lexical file edges (provenance `lexical`). The result is `exact`
//! only when nothing could hide a dependent: no resolver drop on the name of any symbol
//! in the result (untyped receivers, ambiguous or unresolved bindings, interface
//! dispatch, callable values, undecided Go interfaces) and no coverage loss in the
//! index. Otherwise it is a `lower_bound` and `causes`/`boundaries` say why.

use std::collections::{BTreeMap, BTreeSet, VecDeque};

use rusqlite::{Connection, params};
use serde::Serialize;

use super::config::IndexConfig;
use super::meta;
use super::resolve;

/// Symbols matched by a query, at most.
const MAX_TARGETS: usize = 20;
/// Dependents reported, at most (the walk stops there and says so).
const MAX_IMPACTED: usize = 500;
/// Boundary rows listed (causes stay exact).
const MAX_BOUNDARIES: usize = 50;

#[derive(Debug, Clone, Serialize)]
pub struct SymbolRef {
    pub uid: String,
    pub qualified_name: String,
    pub kind: String,
    pub path: String,
    pub line: i64,
}

#[derive(Debug, Clone, Serialize)]
pub struct Impacted {
    /// None for file-level code (imports, top-level statements).
    pub symbol: Option<SymbolRef>,
    pub path: String,
    pub distance: usize,
    /// Kind of the edge that reached it from the previous hop.
    pub edge_kind: String,
    /// Product of the edge confidences along the path.
    pub confidence: f64,
    /// `scope` when every hop is scope-proven, else `lexical`.
    pub provenance: String,
    pub reason: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct Boundary {
    pub cause: String,
    pub name: String,
    pub path: String,
    pub count: i64,
}

#[derive(Debug, Clone, Serialize)]
pub struct Impact {
    pub query: String,
    pub targets: Vec<SymbolRef>,
    /// `resolved` (symbol edges) or `lexical` (file edges: a language the resolver
    /// does not own).
    pub graph: String,
    pub impacted: Vec<Impacted>,
    pub truncated: bool,
    /// `exact` | `lower_bound`
    pub completeness: String,
    /// Occurrences by cause; coverage losses appear as `coverage:<reason>`.
    pub causes: BTreeMap<String, i64>,
    pub boundaries: Vec<Boundary>,
}

struct Node {
    id: i64,
    name_id: i64,
    file: i64,
    lang: String,
}

fn symbol_ref(conn: &Connection, id: i64) -> rusqlite::Result<SymbolRef> {
    conn.prepare_cached(
        "SELECT s.uid, s.qualified_name, s.kind, f.path, s.name_line FROM symbols s
         JOIN files f ON f.id = s.file_id WHERE s.id = ?1",
    )?
    .query_row([id], |r| {
        Ok(SymbolRef {
            uid: r.get(0)?,
            qualified_name: r.get(1)?,
            kind: r.get(2)?,
            path: r.get(3)?,
            line: r.get(4)?,
        })
    })
}

fn path_of(conn: &Connection, fid: i64) -> rusqlite::Result<String> {
    conn.prepare_cached("SELECT path FROM files WHERE id = ?1")?
        .query_row([fid], |r| r.get(0))
}

/// Symbols a query names: a UID, else a qualified name, else a plain name.
fn targets(conn: &Connection, query: &str) -> rusqlite::Result<Vec<Node>> {
    const COLS: &str = "SELECT s.id, s.name_id, s.file_id, f.lang FROM symbols s
         JOIN files f ON f.id = s.file_id JOIN names n ON n.id = s.name_id";
    for filter in [
        "s.uid = ?1",
        "s.qualified_name = ?1",
        "n.name = ?1 AND s.local = 0",
    ] {
        let sql = format!("{COLS} WHERE {filter} ORDER BY f.path, s.ord LIMIT ?2");
        let found = conn
            .prepare(&sql)?
            .query_map(params![query, MAX_TARGETS as i64], |r| {
                Ok(Node {
                    id: r.get(0)?,
                    name_id: r.get(1)?,
                    file: r.get(2)?,
                    lang: r.get(3)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        if !found.is_empty() {
            return Ok(found);
        }
    }
    Ok(Vec::new())
}

pub fn impact(
    conn: &Connection,
    cfg: &IndexConfig,
    query: &str,
    depth: usize,
) -> rusqlite::Result<Impact> {
    let roots = targets(conn, query)?;
    let resolved = !roots.is_empty() && roots.iter().all(|n| resolve::resolves(&n.lang));
    let mut out = Impact {
        query: query.to_string(),
        targets: roots
            .iter()
            .map(|n| symbol_ref(conn, n.id))
            .collect::<rusqlite::Result<_>>()?,
        graph: if resolved { "resolved" } else { "lexical" }.to_string(),
        impacted: Vec::new(),
        truncated: false,
        completeness: "exact".to_string(),
        causes: BTreeMap::new(),
        boundaries: Vec::new(),
    };
    if roots.is_empty() {
        return Ok(out);
    }
    let names: BTreeSet<i64> = if resolved {
        walk_symbols(conn, &roots, depth, &mut out)?
    } else {
        walk_files(conn, &roots, depth, &mut out)?;
        roots.iter().map(|n| n.name_id).collect()
    };
    envelope(conn, cfg, &names, resolved, &mut out)?;
    Ok(out)
}

/// Breadth-first over reverse symbol edges; returns the name ids of every symbol in
/// the result (targets included), whose drops bound the answer.
fn walk_symbols(
    conn: &Connection,
    roots: &[Node],
    depth: usize,
    out: &mut Impact,
) -> rusqlite::Result<BTreeSet<i64>> {
    let mut names: BTreeSet<i64> = roots.iter().map(|n| n.name_id).collect();
    let mut seen: BTreeSet<i64> = roots.iter().map(|n| n.id).collect();
    let mut seen_files: BTreeSet<i64> = BTreeSet::new();
    // (symbol id, distance, confidence, all hops scope-proven)
    let mut queue: VecDeque<(i64, usize, f64, bool)> =
        roots.iter().map(|n| (n.id, 0, 1.0, true)).collect();
    let mut incoming = conn.prepare_cached(
        "SELECT e.src_symbol, e.src_file, e.kind, e.confidence, e.provenance, e.reason, s.name_id
         FROM edges e LEFT JOIN symbols s ON s.id = e.src_symbol
         WHERE e.dst_symbol = ?1 AND e.layer = 'symbol'
           AND e.kind NOT IN ('HAS_METHOD', 'HAS_PROPERTY')
         ORDER BY e.src_file, e.src_symbol",
    )?;
    while let Some((id, dist, conf, scoped)) = queue.pop_front() {
        if dist >= depth {
            continue;
        }
        let rows = incoming
            .query_map([id], |r| {
                Ok((
                    r.get::<_, Option<i64>>(0)?,
                    r.get::<_, i64>(1)?,
                    r.get::<_, String>(2)?,
                    r.get::<_, f64>(3)?,
                    r.get::<_, String>(4)?,
                    r.get::<_, String>(5)?,
                    r.get::<_, Option<i64>>(6)?,
                ))
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        for (src, file, kind, c, prov, reason, name_id) in rows {
            let fresh = match src {
                Some(s) => seen.insert(s),
                None => seen_files.insert(file),
            };
            if !fresh {
                continue;
            }
            if out.impacted.len() >= MAX_IMPACTED {
                out.truncated = true;
                return Ok(names);
            }
            let scoped = scoped && prov == "scope";
            let conf = conf * c;
            out.impacted.push(Impacted {
                symbol: src.map(|s| symbol_ref(conn, s)).transpose()?,
                path: path_of(conn, file)?,
                distance: dist + 1,
                edge_kind: kind,
                confidence: (conf * 1000.0).round() / 1000.0,
                provenance: if scoped { "scope" } else { "lexical" }.to_string(),
                reason,
            });
            if let (Some(s), Some(n)) = (src, name_id) {
                names.insert(n);
                queue.push_back((s, dist + 1, conf, scoped));
            }
        }
    }
    Ok(names)
}

/// Lexical fallback: files whose structure edges point at a target's file.
fn walk_files(
    conn: &Connection,
    roots: &[Node],
    depth: usize,
    out: &mut Impact,
) -> rusqlite::Result<()> {
    let mut seen: BTreeSet<i64> = roots.iter().map(|n| n.file).collect();
    let mut queue: VecDeque<(i64, usize)> = seen.iter().map(|f| (*f, 0)).collect();
    let mut incoming = conn.prepare_cached(
        "SELECT src_file, kind, max(confidence) FROM edges
         WHERE dst_file = ?1 AND layer = 'structure' AND src_file != dst_file
         GROUP BY src_file, kind ORDER BY src_file, kind",
    )?;
    while let Some((file, dist)) = queue.pop_front() {
        if dist >= depth {
            continue;
        }
        let rows = incoming
            .query_map([file], |r| Ok((r.get::<_, i64>(0)?, r.get(1)?, r.get(2)?)))?
            .collect::<rusqlite::Result<Vec<(i64, String, f64)>>>()?;
        for (src, kind, c) in rows {
            if !seen.insert(src) {
                continue;
            }
            if out.impacted.len() >= MAX_IMPACTED {
                out.truncated = true;
                return Ok(());
            }
            out.impacted.push(Impacted {
                symbol: None,
                path: path_of(conn, src)?,
                distance: dist + 1,
                edge_kind: kind,
                confidence: c,
                provenance: "lexical".to_string(),
                reason: "file-edge".to_string(),
            });
            queue.push_back((src, dist + 1));
        }
    }
    Ok(())
}

fn envelope(
    conn: &Connection,
    cfg: &IndexConfig,
    names: &BTreeSet<i64>,
    resolved: bool,
    out: &mut Impact,
) -> rusqlite::Result<()> {
    if !resolved {
        // name-matched file edges are leads, never a complete answer
        *out.causes.entry("lexical_graph".to_string()).or_default() += 1;
    }
    let mut drops = conn.prepare_cached(
        "SELECT r.cause, n.name, f.path, r.count FROM resolve_drops r
         JOIN names n ON n.id = r.name_id JOIN files f ON f.id = r.file_id
         WHERE r.name_id = ?1 ORDER BY f.path, r.cause",
    )?;
    for id in names {
        for row in drops.query_map([id], |r| {
            Ok(Boundary {
                cause: r.get(0)?,
                name: r.get(1)?,
                path: r.get(2)?,
                count: r.get(3)?,
            })
        })? {
            let b = row?;
            *out.causes.entry(b.cause.clone()).or_default() += b.count;
            if out.boundaries.len() < MAX_BOUNDARIES {
                out.boundaries.push(b);
            }
        }
    }
    for (reason, n) in meta::coverage(conn, cfg)?.loss_counts {
        *out.causes.entry(format!("coverage:{reason}")).or_default() += n as i64;
    }
    if !out.causes.is_empty() {
        out.completeness = "lower_bound".to_string();
    }
    Ok(())
}
