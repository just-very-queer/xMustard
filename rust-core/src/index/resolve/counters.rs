//! Drop causes and resolver counters: the inputs of the epistemic envelope
//! (PAR-IMP-05). A reference the resolver cannot bind exactly is recorded per file,
//! name and cause in `resolve_drops`, so a query about a symbol can say whether its
//! dependents are exact or a lower bound, and why. External bindings are only counted:
//! they never name an in-repo symbol.

use std::collections::BTreeMap;

use rusqlite::{Connection, params};
use serde::Serialize;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Cause {
    /// A member access whose receiver's type is unknown (`x.f()` with `x` untyped).
    ReceiverUntyped,
    /// Bound outside the index: an external module, the standard library, a builtin.
    External,
    /// A name-only (global tier) match with several definers, or too common to bind.
    Ambiguous,
    /// An in-repo binding whose target was not found (missing member, unnamed default
    /// export, broken re-export chain).
    Unresolved,
    /// Bound to an interface member: the implementations that run are not enumerated.
    DispatchBoundary,
    /// A Go interface: which types satisfy it implicitly is not decided.
    UndecidedInterface,
    /// A function or method used as a value (callback): its calls happen elsewhere.
    CallableValue,
}

impl Cause {
    pub fn as_str(self) -> &'static str {
        match self {
            Cause::ReceiverUntyped => "receiver_untyped",
            Cause::External => "external",
            Cause::Ambiguous => "ambiguous",
            Cause::Unresolved => "unresolved",
            Cause::DispatchBoundary => "dispatch_boundary",
            Cause::UndecidedInterface => "undecided_interface",
            Cause::CallableValue => "callable_value",
        }
    }
}

/// Resolver totals for one build or update, reported under `counters.resolve`.
#[derive(Debug, Clone, Serialize, Default)]
pub struct ResolveCounters {
    /// Go and TS/JS files whose symbol edges were (re)computed.
    pub files: usize,
    /// Symbol-layer edges written.
    pub edges: usize,
    /// Edges by confidence tier: `same_file`, `import_scoped`, `global`.
    pub tiers: BTreeMap<&'static str, usize>,
    /// References bound to a local binding (never an edge, never a drop).
    pub locals: usize,
    /// Drops and boundaries by cause (occurrences).
    pub drops: BTreeMap<&'static str, usize>,
}

/// One file's drops: (name id, cause) → occurrences.
#[derive(Default)]
pub struct FileDrops(BTreeMap<(i64, Cause), i64>);

impl FileDrops {
    pub fn note(&mut self, name_id: i64, cause: Cause) {
        *self.0.entry((name_id, cause)).or_default() += 1;
    }

    pub fn write(
        self,
        conn: &Connection,
        fid: i64,
        totals: &mut ResolveCounters,
    ) -> rusqlite::Result<()> {
        let mut ins = conn.prepare_cached(
            "INSERT INTO resolve_drops(file_id, name_id, cause, count) VALUES (?1, ?2, ?3, ?4)",
        )?;
        for ((name_id, cause), n) in self.0 {
            *totals.drops.entry(cause.as_str()).or_default() += n as usize;
            // an external binding never hides an in-repo dependent: counted, not kept.
            if cause != Cause::External {
                ins.execute(params![fid, name_id, cause.as_str(), n])?;
            }
        }
        Ok(())
    }
}
