//! Typed symbol edges: the kind a resolved reference produces, aggregation per
//! (source, target, kind) and the `edges` rows (layer `symbol`).

use std::collections::BTreeMap;

use rusqlite::{Connection, params};

use super::counters::ResolveCounters;
use super::scope::{Sym, Tier};
use crate::index::facts::ref_kind;

/// Edge kind and access mode of a reference of kind `rk` bound to `target`; None for
/// reference kinds that never make an edge (bindings, import names, export lists).
pub fn edge_kind(rk: u8, target: &Sym) -> Option<(&'static str, Option<&'static str>)> {
    let state = matches!(target.kind.as_str(), "Property" | "Variable");
    Some(match rk {
        ref_kind::CALL | ref_kind::MEMBER_CALL => ("CALLS", None),
        ref_kind::EXTENDS => ("EXTENDS", None),
        ref_kind::IMPLEMENTS => ("IMPLEMENTS", None),
        ref_kind::WRITE if state => ("ACCESSES", Some("write")),
        ref_kind::READ | ref_kind::MEMBER | ref_kind::TYPE if state => ("ACCESSES", Some("read")),
        ref_kind::READ | ref_kind::MEMBER | ref_kind::TYPE | ref_kind::WRITE => ("USES", None),
        _ => return None,
    })
}

/// Structural edge from a type to a member it declares.
pub fn member_kind(member: &Sym) -> Option<&'static str> {
    match member.kind.as_str() {
        "Method" | "Constructor" => Some("HAS_METHOD"),
        "Property" => Some("HAS_PROPERTY"),
        _ => None,
    }
}

#[derive(PartialEq, Eq, PartialOrd, Ord)]
struct Key {
    src: Option<i64>,
    dst_file: i64,
    dst: Option<i64>,
    kind: &'static str,
    access: Option<&'static str>,
}

struct Agg {
    weight: i64,
    tier: Tier,
    reason: &'static str,
    via: String,
}

/// Where an edge points and why.
pub struct Target<'a> {
    pub file: i64,
    pub symbol: Option<i64>,
    pub tier: Tier,
    pub reason: &'static str,
    /// The name (or module specifier) the edge was bound through.
    pub via: &'a str,
}

/// One file's outgoing symbol edges, aggregated by (source, target, kind, access):
/// weight counts occurrences, and the best tier sets confidence, provenance and reason.
#[derive(Default)]
pub struct Edges(BTreeMap<Key, Agg>);

impl Edges {
    pub fn add(
        &mut self,
        src: Option<i64>,
        kind: &'static str,
        access: Option<&'static str>,
        t: Target<'_>,
    ) {
        if src.is_some() && src == t.symbol {
            return; // recursion is not a dependency
        }
        let key = Key {
            src,
            dst_file: t.file,
            dst: t.symbol,
            kind,
            access,
        };
        let e = self.0.entry(key).or_insert_with(|| Agg {
            weight: 0,
            tier: t.tier,
            reason: t.reason,
            via: t.via.to_string(),
        });
        e.weight += 1;
        if t.tier > e.tier {
            e.tier = t.tier;
            e.reason = t.reason;
        }
    }

    pub fn write(
        self,
        conn: &Connection,
        fid: i64,
        totals: &mut ResolveCounters,
    ) -> rusqlite::Result<usize> {
        let mut ins = conn.prepare_cached(
            "INSERT INTO edges(src_file, dst_file, src_symbol, dst_symbol, kind, layer, weight,
             confidence, provenance, via, reason, access)
             VALUES (?1, ?2, ?3, ?4, ?5, 'symbol', ?6, ?7, ?8, ?9, ?10, ?11)",
        )?;
        let n = self.0.len();
        for (k, a) in self.0 {
            let via = serde_json::to_string(&[a.via]).unwrap_or_else(|_| "[]".into());
            ins.execute(params![
                fid,
                k.dst_file,
                k.src,
                k.dst,
                k.kind,
                a.weight,
                a.tier.confidence(),
                a.tier.provenance(),
                via,
                a.reason,
                k.access
            ])?;
            *totals.tiers.entry(a.tier.label()).or_default() += 1;
        }
        totals.edges += n;
        Ok(n)
    }
}
