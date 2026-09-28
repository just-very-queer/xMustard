//! Import cycles (PAR-IMP-08; WS-35 closes the requirement, WS-47 renders the report as
//! diagnostics findings once the diagnostics owner hands off).
//!
//! The graph is the files of the resolver's languages (Go, TS/JS) and the IMPORTS edges
//! that force the imported module's initialization when the importer loads: static
//! imports, re-exports, top-level `require` and Go package imports. Type-only imports,
//! type-only re-exports, dynamic `import()` and `require` inside a function are left
//! out and counted by reason (`resolve::scope::IMPORT_KINDS`). Other languages are not
//! checked: their imports are lexical and cannot tell a lazy import from an eager one.
//!
//! An iterative Tarjan pass finds the strongly connected components; each one of two or
//! more files holds at least one cycle, and their number (`component_count`) is the
//! stable metric. A bounded Johnson enumeration then counts the elementary cycles. Past
//! its budget, or in a component too large to enumerate, `cycle_count` is null and each
//! component still shows one representative cycle: the shortest through its first file.

use std::collections::{BTreeMap, VecDeque};

use serde::Serialize;

use super::impact::{Direction, Graph};
use super::resolve::scope::FORCING_IMPORT_REASONS;

/// Components listed (the count stays exact).
const MAX_COMPONENTS: usize = 20;
/// Files listed per component.
const MAX_COMPONENT_FILES: usize = 20;
/// Enumerated cycles listed.
const MAX_LISTED_CYCLES: usize = 20;
/// Components larger than this are not enumerated (the count becomes null).
const MAX_ENUMERATED_SIZE: usize = 200;
/// Elementary cycles the enumeration may count in all, over every component.
const MAX_CYCLES: usize = 10_000;
/// Edge steps the enumeration may take in all.
const STEP_BUDGET: u64 = 2_000_000;

/// What the enumeration has spent, shared by every start of every component.
#[derive(Debug, Default)]
struct Budget {
    steps: u64,
    cycles: usize,
}

impl Budget {
    fn spent(&self) -> bool {
        self.steps > STEP_BUDGET || self.cycles > MAX_CYCLES
    }
}

/// One strongly connected component of the import graph.
#[derive(Debug, Clone, Serialize)]
pub struct Component {
    pub size: usize,
    /// Its files in path order, at most [`MAX_COMPONENT_FILES`].
    pub files: Vec<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub files_truncated: bool,
    /// The shortest cycle through its first file, closed (first file repeated last).
    pub representative: Vec<String>,
    /// Every member's file id.
    #[serde(skip)]
    pub members: Vec<u32>,
}

#[derive(Debug, Clone, Serialize, Default)]
pub struct CycleReport {
    /// Components of two or more files: the stable metric.
    pub component_count: usize,
    /// Elementary cycles; null when the enumeration was cut short.
    pub cycle_count: Option<usize>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated: bool,
    pub components: Vec<Component>,
    /// The first enumerated cycles, closed.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub cycles: Vec<Vec<String>>,
    /// Files of the resolver's languages checked.
    pub files_checked: usize,
    /// Initialization-forcing import edges between two checked files.
    pub edges: usize,
    /// Import edges left out, by reason (type-only, lazy, dynamic).
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub excluded: BTreeMap<String, usize>,
    /// Every component, listed or not (for impact's per-target view).
    #[serde(skip)]
    pub all: Vec<Component>,
}

/// A compact summary for ground's coverage block.
#[derive(Debug, Clone, Serialize)]
pub struct CycleSummary {
    pub component_count: usize,
    pub cycle_count: Option<usize>,
    pub largest: usize,
}

impl CycleReport {
    pub fn summary(&self) -> CycleSummary {
        CycleSummary {
            component_count: self.component_count,
            cycle_count: self.cycle_count,
            largest: self.all.iter().map(|c| c.size).max().unwrap_or(0),
        }
    }
}

/// The file-level import graph of `g`: forcing IMPORTS edges between checked files,
/// sorted and deduplicated per file.
fn import_graph<G: Graph + ?Sized>(
    g: &G,
    report: &mut CycleReport,
) -> Result<Vec<Vec<u32>>, String> {
    let n = g.file_count();
    let mut adj = vec![Vec::new(); n as usize];
    for f in 0..n {
        if !g.resolved(f) {
            continue;
        }
        report.files_checked += 1;
        for e in g.edges(f, Direction::Downstream)? {
            let l = g.labels(&e);
            if l.kind != "IMPORTS" || l.layer != "symbol" {
                continue;
            }
            if !FORCING_IMPORT_REASONS.contains(&l.reason) {
                *report.excluded.entry(l.reason.to_string()).or_default() += 1;
                continue;
            }
            let t = g.file_of(e.node)?;
            if t != f && g.resolved(t) {
                adj[f as usize].push(t);
            }
        }
        let list = &mut adj[f as usize];
        list.sort_unstable();
        list.dedup();
        report.edges += list.len();
    }
    Ok(adj)
}

/// Tarjan's bookkeeping, with an explicit call stack of (node, next edge).
struct Tarjan {
    index: Vec<u32>,
    low: Vec<u32>,
    on: Vec<bool>,
    stack: Vec<usize>,
    call: Vec<(usize, usize)>,
    next: u32,
}

impl Tarjan {
    const UNSEEN: u32 = u32::MAX;

    fn enter(&mut self, v: usize) {
        (self.index[v], self.low[v]) = (self.next, self.next);
        self.next += 1;
        self.stack.push(v);
        self.on[v] = true;
        self.call.push((v, 0));
    }
}

/// Strongly connected components (iterative Tarjan), each sorted.
pub fn tarjan(adj: &[Vec<u32>]) -> Vec<Vec<u32>> {
    let n = adj.len();
    let mut t = Tarjan {
        index: vec![Tarjan::UNSEEN; n],
        low: vec![0; n],
        on: vec![false; n],
        stack: Vec::new(),
        call: Vec::new(),
        next: 0,
    };
    let mut out = Vec::new();
    for root in 0..n {
        if t.index[root] != Tarjan::UNSEEN {
            continue;
        }
        t.enter(root);
        while let Some(&(v, i)) = t.call.last() {
            if let Some(&w) = adj[v].get(i) {
                let w = w as usize;
                if let Some(top) = t.call.last_mut() {
                    top.1 += 1;
                }
                if t.index[w] == Tarjan::UNSEEN {
                    t.enter(w);
                } else if t.on[w] {
                    t.low[v] = t.low[v].min(t.index[w]);
                }
                continue;
            }
            t.call.pop();
            if let Some(&(p, _)) = t.call.last() {
                t.low[p] = t.low[p].min(t.low[v]);
            }
            if t.low[v] == t.index[v] {
                let mut comp = Vec::new();
                while let Some(w) = t.stack.pop() {
                    t.on[w] = false;
                    comp.push(w as u32);
                    if w == v {
                        break;
                    }
                }
                comp.sort_unstable();
                out.push(comp);
            }
        }
    }
    out
}

/// Johnson's elementary-cycle enumeration over one component, in local indexes.
struct Johnson<'a> {
    adj: &'a [Vec<usize>],
    start: usize,
    blocked: Vec<bool>,
    b: Vec<Vec<usize>>,
    path: Vec<usize>,
    /// The cycles kept for the listing, at most `room`.
    found: &'a mut Vec<Vec<usize>>,
    room: usize,
    budget: &'a mut Budget,
    blown: bool,
}

impl Johnson<'_> {
    fn unblock(&mut self, u: usize) {
        self.blocked[u] = false;
        for w in std::mem::take(&mut self.b[u]) {
            if self.blocked[w] {
                self.unblock(w);
            }
        }
    }

    /// Cycles through `start` using only vertices at or above it.
    fn circuit(&mut self, v: usize) -> bool {
        let mut closed = false;
        self.path.push(v);
        self.blocked[v] = true;
        for i in 0..self.adj[v].len() {
            let w = self.adj[v][i];
            self.budget.steps += 1;
            if self.blown || self.budget.spent() {
                self.blown = true;
                break;
            }
            if w < self.start {
                continue;
            }
            if w == self.start {
                self.budget.cycles += 1;
                if self.found.len() < self.room {
                    self.found.push(self.path.clone());
                }
                closed = true;
            } else if !self.blocked[w] && self.circuit(w) {
                closed = true;
            }
        }
        if closed {
            self.unblock(v);
        } else {
            for &w in &self.adj[v] {
                if w >= self.start && !self.b[w].contains(&v) {
                    self.b[w].push(v);
                }
            }
        }
        self.path.pop();
        closed
    }
}

/// The elementary cycles of component `members` (sorted file ids), within the shared
/// budget: their count and the first `room` of them, or None when the budget ran out.
fn enumerate(
    members: &[u32],
    adj: &[Vec<u32>],
    budget: &mut Budget,
    room: usize,
) -> Option<(usize, Vec<Vec<u32>>)> {
    if members.len() > MAX_ENUMERATED_SIZE {
        return None;
    }
    let local = |f: u32| members.binary_search(&f).ok();
    let ladj: Vec<Vec<usize>> = members
        .iter()
        .map(|f| adj[*f as usize].iter().filter_map(|t| local(*t)).collect())
        .collect();
    let (before, k) = (budget.cycles, members.len());
    let mut found = Vec::new();
    for start in 0..k {
        let mut j = Johnson {
            adj: &ladj,
            start,
            blocked: vec![false; k],
            b: vec![Vec::new(); k],
            path: Vec::new(),
            found: &mut found,
            room,
            budget,
            blown: false,
        };
        j.circuit(start);
        if j.blown {
            return None;
        }
    }
    let found = found
        .into_iter()
        .map(|c| c.into_iter().map(|i| members[i]).collect())
        .collect();
    Some((budget.cycles - before, found))
}

/// The shortest cycle through `members[0]` within the component.
fn representative(members: &[u32], adj: &[Vec<u32>]) -> Vec<u32> {
    let start = members[0];
    let inside = |f: u32| members.binary_search(&f).is_ok();
    let mut parent: BTreeMap<u32, u32> = BTreeMap::new();
    let mut queue = VecDeque::from([start]);
    while let Some(u) = queue.pop_front() {
        for &w in &adj[u as usize] {
            if w == start {
                let mut cycle = vec![u];
                let mut at = u;
                while let Some(&p) = parent.get(&at) {
                    cycle.push(p);
                    at = p;
                }
                if at != start {
                    cycle.push(start);
                }
                cycle.reverse();
                cycle.push(start);
                return cycle;
            }
            if inside(w) && w != start && !parent.contains_key(&w) {
                parent.insert(w, u);
                queue.push_back(w);
            }
        }
    }
    vec![start]
}

/// The import-cycle report of `g`.
pub fn compute<G: Graph + ?Sized>(g: &G) -> Result<CycleReport, String> {
    let mut report = CycleReport::default();
    let adj = import_graph(g, &mut report)?;
    let mut comps: Vec<Vec<u32>> = tarjan(&adj).into_iter().filter(|c| c.len() > 1).collect();
    // files are numbered in path order, so this is path order of first files
    comps.sort();
    report.component_count = comps.len();
    let mut budget = Budget::default();
    let mut total = Some(0usize);
    let paths =
        |ids: &[u32]| -> Vec<String> { ids.iter().map(|f| g.path(*f).to_string()).collect() };
    for members in comps {
        let room = MAX_LISTED_CYCLES - report.cycles.len();
        match enumerate(&members, &adj, &mut budget, room) {
            Some((count, found)) => {
                total = total.map(|t| t + count);
                for c in found {
                    let mut closed = paths(&c);
                    closed.push(g.path(c[0]).to_string());
                    report.cycles.push(closed);
                }
            }
            None => total = None,
        }
        report.all.push(Component {
            size: members.len(),
            files: paths(&members[..members.len().min(MAX_COMPONENT_FILES)]),
            files_truncated: members.len() > MAX_COMPONENT_FILES,
            representative: paths(&representative(&members, &adj)),
            members,
        });
    }
    report.cycle_count = total;
    report.truncated = total.is_none();
    report.components = report.all.iter().take(MAX_COMPONENTS).cloned().collect();
    Ok(report)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn g(edges: &[(u32, u32)], n: usize) -> Vec<Vec<u32>> {
        let mut adj = vec![Vec::new(); n];
        for (a, b) in edges {
            adj[*a as usize].push(*b);
        }
        adj
    }

    #[test]
    fn tarjan_finds_components() {
        // 0 -> 1 -> 2 -> 0, 2 -> 3, 3 -> 4 -> 3, 5 alone
        let adj = g(&[(0, 1), (1, 2), (2, 0), (2, 3), (3, 4), (4, 3)], 6);
        let mut comps: Vec<Vec<u32>> = tarjan(&adj).into_iter().filter(|c| c.len() > 1).collect();
        comps.sort();
        assert_eq!(comps, vec![vec![0, 1, 2], vec![3, 4]]);
    }

    #[test]
    fn tarjan_survives_a_long_chain_without_recursion() {
        let n = 200_000;
        let mut adj: Vec<Vec<u32>> = (0..n).map(|i| vec![(i + 1) as u32 % n as u32]).collect();
        adj[n - 1] = vec![0];
        let comps = tarjan(&adj);
        assert_eq!(comps.len(), 1);
        assert_eq!(comps[0].len(), n);
    }

    #[test]
    fn johnson_counts_elementary_cycles() {
        // a complete digraph on 4 nodes has 20 elementary cycles
        let mut e = Vec::new();
        for a in 0..4u32 {
            for b in 0..4u32 {
                if a != b {
                    e.push((a, b));
                }
            }
        }
        let adj = g(&e, 4);
        let mut budget = Budget::default();
        let (count, found) =
            enumerate(&[0, 1, 2, 3], &adj, &mut budget, MAX_LISTED_CYCLES).unwrap();
        assert_eq!(count, 20);
        assert_eq!(found.len(), MAX_LISTED_CYCLES);
        // the listing keeps only the room it is given, over every start
        let (_, few) = enumerate(&[0, 1, 2, 3], &adj, &mut Budget::default(), 3).unwrap();
        assert_eq!(few.len(), 3);
    }

    // The cycle cap is shared by every start and every component, as the step budget
    // is: components that each stay under it are cut short together.
    #[test]
    fn the_cycle_cap_is_shared_across_starts_and_components() {
        // six disjoint complete digraphs on 7 nodes, 2,365 elementary cycles each: every
        // one fits under the cap alone, the six together do not
        let k = 7u32;
        let comps = 6u32;
        let mut e = Vec::new();
        for c in 0..comps {
            for a in 0..k {
                for b in 0..k {
                    if a != b {
                        e.push((c * k + a, c * k + b));
                    }
                }
            }
        }
        let adj = g(&e, (k * comps) as usize);
        let mut budget = Budget::default();
        let mut total = 0;
        let mut cut = false;
        for c in 0..comps {
            let members: Vec<u32> = (c * k..(c + 1) * k).collect();
            match enumerate(&members, &adj, &mut budget, 0) {
                Some((n, _)) => total += n,
                None => cut = true,
            }
        }
        assert!(cut, "{total} cycles counted past the shared cap");
        assert_eq!(budget.cycles, MAX_CYCLES + 1, "cut by the cycle cap");
        assert!(budget.steps <= STEP_BUDGET);
    }

    #[test]
    fn a_component_too_large_is_not_enumerated_but_keeps_a_representative() {
        let n = MAX_ENUMERATED_SIZE + 5;
        let adj: Vec<Vec<u32>> = (0..n).map(|i| vec![((i + 1) % n) as u32]).collect();
        let members: Vec<u32> = (0..n as u32).collect();
        assert!(enumerate(&members, &adj, &mut Budget::default(), 0).is_none());
        let rep = representative(&members, &adj);
        assert_eq!(rep.len(), n + 1);
        assert_eq!((rep[0], rep[n]), (0, 0));
    }

    #[test]
    fn representative_is_the_shortest_cycle_through_the_first_member() {
        // 0 -> 1 -> 2 -> 0 and 0 -> 2 -> 0
        let adj = g(&[(0, 1), (1, 2), (2, 0), (0, 2)], 3);
        assert_eq!(representative(&[0, 1, 2], &adj), vec![0, 2, 0]);
    }
}
