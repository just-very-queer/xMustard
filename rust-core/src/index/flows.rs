//! Execution flows (WS-38c; PAR-SYM-09): where execution starts and what it reaches.
//!
//! Two steps. First, **score the entry points**: a symbol that calls many things and is
//! called by few looks like the start of something, so the score is
//! `calleeCount / (callerCount + 1)`, doubled when the symbol is `exported`, times a
//! multiplier for how entry-point-like its name is. The top candidates become the seeds
//! of the second step, a **bounded DFS** over the resolved CALLS projection, which is
//! what PAR-SYM-09 requires: the lexical `structure` and `flow` layers are a different
//! graph about a different thing and are never walked here.
//!
//! Memory is the constraint that shapes both steps. The resolved CALLS adjacency is a CSR
//! in the segment, so one node's callees cost one `Vec<Edge>` and the graph is two `u32`
//! vectors. Everything else the answer needs is one `Vec` per admitted symbol: the row
//! for the name and path, the callee count and the caller count. Nothing proportional to
//! the repository's text is held, and nothing is read back on every step.
//!
//! Determinism is the other constraint. Seeds rank by score and break ties on the
//! symbol's own `(path, line, name)`, a total order over data the index already stores, so
//! two runs over one index agree byte-for-byte without a seed. Two seeds with the same
//! reachability set are one flow, so the answer's shape does not depend on the order the
//! seeds were visited in.

use std::collections::{BTreeMap, BTreeSet};

use serde::Serialize;

use super::csr::{self, Segment};
use super::impact::Direction;

/// Seeds scored at most (PAR-SYM-09's "top 200 candidates").
pub const MAX_CANDIDATES: usize = 200;
/// Steps deep a flow is walked (PAR-SYM-09).
pub const MAX_DEPTH: u32 = 10;
/// Callees explored per node (PAR-SYM-09's "branching 4").
pub const MAX_BRANCHING: usize = 4;
/// Steps a reachability set needs before it is a flow (PAR-SYM-09's "at least 3 steps").
pub const MIN_STEPS: usize = 3;
/// Flows one answer lists.
pub const MAX_FLOWS: usize = 200;
/// Steps listed per flow.
///
/// A listing bound, not a walk bound: the walk behind it can be much longer. `expanded_at`
/// lets a symbol be expanded once per distinct SHORTER depth, so the number of expansions is
/// not bounded by `MAX_DEPTH` and a wide tree reaches far past this cap — the root of a
/// 4-ary fixture walks 340 steps and lists 50 here
/// (`a_flow_lists_at_most_fifty_steps_and_counts_the_rest`). What the walk does NOT do is grow
/// with the repository: `MAX_CANDIDATES` bounds the seeds and each seed's walk is bounded by
/// the depth, branching and re-expansion rules, so the retained step list stays small. The
/// steps past this cap are counted in `truncated_step_lists` rather than dropped in silence.
pub const MAX_STEPS_LISTED: usize = 50;
/// Steps that must share a name prefix before it is a flow's label.
pub const LABEL_MIN_MEMBERS: usize = 2;
/// The area of a symbol the community projection did not place: below the degree floor in
/// large-graph mode, or reachable only by edges under the confidence floor. It is a
/// sentinel rather than an area, so it is excluded from every flow's community count
/// instead of being reported as a community of its own.
pub const UNCLASSIFIED: u32 = u32::MAX;

/// How a flow's label was produced.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum LabelRule {
    /// The name prefix every listed step shares.
    NameStem,
    /// No shared prefix qualified, so the highest-scoring seed's own name.
    SeedName,
}

/// The label rules, in the order they are tried.
pub const LABEL_RULES: [&str; 2] = ["name_stem", "seed_name"];

/// Whether a flow stays inside one community or crosses into another.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum FlowClass {
    /// Every step sits in one community.
    Intra,
    /// The steps span two or more.
    Cross,
}

/// The classes, in the order the answer reports them.
pub const FLOW_CLASSES: [&str; 2] = ["intra", "cross"];

/// One row of the name-multiplier table: how entry-point-like a name looks. A table
/// rather than a ladder (§5.1 rule 10), published in the answer so a caller can read which
/// row scored which seed.
#[derive(Debug, Clone, Copy)]
pub struct NameRule {
    /// The published class name, and the answer's `name_classes[].name`.
    pub class: &'static str,
    /// The multiplier PAR-SYM-09's "times a name multiplier" applies.
    pub multiplier: f64,
    /// The prefix matched case-insensitively at the start of the name; `None` for the
    /// `main` row (matched exactly) and for the catch-all.
    pub prefix: Option<&'static str>,
}

/// The name-multiplier table, most specific first. `main` is matched exactly, because a
/// symbol that merely contains "main" is not a program entry point.
pub const NAME_RULES: &[NameRule] = &[
    NameRule {
        class: "main",
        multiplier: 2.0,
        prefix: None,
    },
    NameRule {
        class: "test",
        multiplier: 2.0,
        prefix: Some("test"),
    },
    NameRule {
        class: "handler",
        multiplier: 1.5,
        prefix: Some("handle"),
    },
    NameRule {
        class: "command",
        multiplier: 1.5,
        prefix: Some("run"),
    },
    NameRule {
        class: "command",
        multiplier: 1.5,
        prefix: Some("start"),
    },
    NameRule {
        class: "command",
        multiplier: 1.5,
        prefix: Some("command"),
    },
    NameRule {
        class: "serve",
        multiplier: 1.25,
        prefix: Some("serve"),
    },
    NameRule {
        class: "plain",
        multiplier: 1.0,
        prefix: None,
    },
];

/// The multiplier of the class a name falls in.
pub fn name_multiplier(name: &str) -> f64 {
    class_of(name).multiplier
}

/// The name-multiplier row a name falls in: `main` exactly, then the first prefix row
/// that matches, then the catch-all. The catch-all is the table's last row and has no
/// prefix, so the fallback is only reached if the table is edited into a shape where no
/// row can match, which `class_of_is_total_over_the_table` guards.
fn class_of(name: &str) -> &'static NameRule {
    if name == "main" {
        return &NAME_RULES[0];
    }
    let lower = name.to_lowercase();
    for rule in NAME_RULES {
        if let Some(prefix) = rule.prefix
            && lower.starts_with(prefix)
        {
            return rule;
        }
    }
    NAME_RULES.last().expect("the table ends in a catch-all")
}

/// One symbol the walk may start from or reach.
struct Node {
    /// The index's node id for the symbol (`files + symbol id`).
    graph_node: u32,
    name: String,
    path: String,
    line: u32,
    kind: String,
    /// The symbol-granularity area this symbol belongs to, which is the partition the
    /// intra/cross classification reads.
    area: u32,
    /// PAR-SYM-09's exported doubling, read from the store rather than the segment.
    exported: bool,
}

/// One scored entry-point candidate.
#[derive(Debug, Clone, Serialize)]
pub struct Seed {
    /// The index's node id for the symbol.
    pub node: u32,
    pub name: String,
    pub path: String,
    pub line: u32,
    pub kind: String,
    /// Distinct symbols that resolve a call to this one.
    pub callers: u64,
    /// Distinct symbols this one resolves a call to.
    pub callees: u64,
    /// `callees / (callers + 1)`, the base of the score.
    pub callees_ratio: f64,
    pub exported: bool,
    /// The name class whose multiplier applied.
    pub name_class: String,
    pub name_multiplier: f64,
    /// `callees_ratio × (2 if exported) × name_multiplier`.
    pub score: f64,
}

/// One step of a flow.
#[derive(Debug, Clone, Serialize)]
pub struct Step {
    pub node: u32,
    pub name: String,
    pub path: String,
    pub line: u32,
}

/// One flow: one reachability set, and every seed that reaches it.
#[derive(Debug, Clone, Serialize)]
pub struct Flow {
    pub label: String,
    pub label_rule: LabelRule,
    pub class: FlowClass,
    /// The seeds that reach exactly this set, best-scoring first.
    pub seeds: Vec<Seed>,
    /// The symbols the walk visited, seed first, capped at [`MAX_STEPS_LISTED`].
    pub steps: Vec<Step>,
    /// Every symbol the walk visited, listed or not. Not serialized: the label and the
    /// class are computed over it and the freshness envelope is built from its paths, so
    /// the listed rows are not the whole answer.
    #[serde(skip)]
    pub all_steps: Vec<Step>,
    /// The steps the walk visited beyond the listed ones.
    pub steps_omitted: u64,
    /// The graph distance from a seed to the furthest visited step.
    pub depth: u32,
    /// Communities the visited steps span.
    pub communities: u64,
}

/// How many of each bound stopped something, so a short answer is never read as a
/// complete one.
#[derive(Debug, Clone, Copy, Default, Serialize)]
pub struct Truncation {
    /// Callees the branching cap cut off.
    pub truncated_at_branching: u64,
    /// Callees the depth cap cut off.
    pub truncated_at_depth: u64,
    /// Candidates past [`MAX_CANDIDATES`], so never scored as seeds.
    pub dropped_candidates: u64,
    /// Seeds whose reachability set was shorter than [`MIN_STEPS`].
    pub dropped_under_min_steps: u64,
    /// Flows past [`MAX_FLOWS`].
    pub dropped_flows: u64,
    /// Visited steps past [`MAX_STEPS_LISTED`] across every flow.
    pub truncated_step_lists: u64,
}

impl Truncation {
    /// Whether any bound stopped anything.
    pub fn any(&self) -> bool {
        self.truncated_at_branching > 0
            || self.truncated_at_depth > 0
            || self.dropped_candidates > 0
            || self.dropped_under_min_steps > 0
            || self.dropped_flows > 0
            || self.truncated_step_lists > 0
    }
}

/// The answer of `index flows`.
#[derive(Debug, Clone, Serialize)]
pub struct Flows {
    pub command: &'static str,
    pub entry_points: Vec<Seed>,
    pub flows: Vec<Flow>,
    /// Every path that contributed to this answer: each entry point's file, and every step
    /// of every flow built — including the steps past the listing cap and the flows
    /// dropped by the MAX_FLOWS cut. Not serialized: the dispatch computes freshness from
    /// it rather than from the listed rows, which would leave the answer claiming `current`
    /// while its scores came from a file the walk never listed. `areas()` does the same
    /// with `total_paths` for the same reason.
    #[serde(skip)]
    pub total_paths: Vec<String>,
    /// Admitted symbols: not local to a function body, in a file with a resolver.
    pub symbols_considered: u64,
    /// Admitted symbols the community projection did not place, so a reader can tell an
    /// `intra` flow whose steps were all classified from one that also had steps the
    /// partition declined to place. Excluded from every flow's `communities` count.
    pub unclassified_symbols: u64,
    /// Admitted symbols whose `exported` flag was true, so a caller can see how much of
    /// the index the ×2 doubling could reach.
    pub exported_symbols: u64,
    /// Scored candidates before [`MAX_CANDIDATES`], so the cap is legible.
    pub candidates: u64,
    /// Resolved CALLS edges the walks read.
    pub call_edges: u64,
    /// The bounds the run used, whether or not they bit.
    pub caps: Caps,
    /// Whether any bound stopped anything: the one-line reading of [`Truncation`], so a
    /// caller that only needs to know whether the answer is complete does not have to read
    /// six counters.
    pub truncated: bool,
    pub truncation: Truncation,
    /// The name-multiplier table, one row per class.
    pub name_classes: Vec<NameClass>,
}

/// The bounds a run used.
#[derive(Debug, Clone, Copy, Serialize)]
pub struct Caps {
    pub max_candidates: usize,
    pub max_depth: u32,
    pub max_branching: usize,
    pub min_steps: usize,
    pub max_flows: usize,
    pub max_steps_listed: usize,
}

/// One published row of the name-multiplier table.
#[derive(Debug, Clone, Serialize)]
pub struct NameClass {
    pub name: String,
    /// The prefix the class matches, empty for the exact and catch-all rows.
    pub prefix: String,
    pub multiplier: f64,
}

/// Every limit of one run, so a caller can bound a run and a test can reach a cap the
/// fixture cannot afford to build.
#[derive(Debug, Clone, Copy)]
pub struct Limits {
    pub max_candidates: usize,
    pub max_flows: usize,
    pub max_steps_listed: usize,
    pub max_depth: u32,
    pub max_branching: usize,
    pub min_steps: usize,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_candidates: MAX_CANDIDATES,
            max_flows: MAX_FLOWS,
            max_steps_listed: MAX_STEPS_LISTED,
            max_depth: MAX_DEPTH,
            max_branching: MAX_BRANCHING,
            min_steps: MIN_STEPS,
        }
    }
}

/// The admitted symbols and their resolved CALLS adjacency: a CSR of callees over
/// [`Node`]s, plus the caller count derived from the same CSR.
struct Graph {
    nodes: Vec<Node>,
    /// `off[i]..off[i+1]` is node `i`'s callees, as node indices.
    off: Vec<u32>,
    dst: Vec<u32>,
    /// Distinct resolved callees per node, which is the CSR's own degree.
    callees: Vec<u32>,
    /// Distinct resolved callers per node, counted in the same pass that built the CSR.
    callers: Vec<u32>,
}

impl Graph {
    fn callee_slice(&self, i: u32) -> &[u32] {
        &self.dst[self.off[i as usize] as usize..self.off[i as usize + 1] as usize]
    }
}

/// The flows of an opened snapshot, read-only like `impact`.
pub fn flows_for(snapshot: &super::reader::Snapshot, limits: Limits) -> Result<Flows, String> {
    let exported = snapshot.exported_by_symbol()?;
    flows_of(
        &snapshot.seg,
        snapshot.resolved_files(),
        &area_by_symbol(snapshot)?,
        limits,
        Some(&|s| exported.get(s as usize).copied()),
    )
}

/// Each admitted symbol's area id, keyed by the index's node id.
///
/// PAR-SYM-09 classifies a flow against the *community* layer, and WS-38b's areas are that
/// layer at symbol granularity. The file-level communities the reader computes cannot serve
/// here: they are the connected components of the file-level `structure` graph, and a
/// resolved CALL needs an import to exist, so its two ends are always in the SAME component.
/// Every flow would therefore be `intra` and the classification would carry no information.
/// Areas are communities over resolved symbol edges, so a call from one subsystem into
/// another really does cross one.
fn area_by_symbol(snapshot: &super::reader::Snapshot) -> Result<BTreeMap<u32, u32>, String> {
    let areas = super::areas::areas_for(snapshot, super::areas::DEFAULT_SEED)?;
    let mut out = BTreeMap::new();
    // `member_nodes`, not `members`: the listed rows are capped at MAX_MEMBERS, and a
    // partition read from them leaves every symbol past the cap with no area at all — which
    // on a large repository is most of them, and turns `cross` into an artefact of the cap.
    // `member_nodes` is every member the propagation found, listed or not.
    for (rank, area) in areas.areas.iter().enumerate() {
        for node in &area.member_nodes {
            out.insert(*node, rank as u32);
        }
    }
    Ok(out)
}

/// The flows of one stored generation's graph segment.
///
/// `resolved` is the snapshot's per-file table of which files have a scope resolver, so a
/// symbol whose file cannot resolve a call is not admitted at all. `areas_of` maps an index
/// node id to the symbol-granularity area that holds it, which is the partition the
/// intra/cross classification reads; it comes from WS-38b's `areas.rs` and not from the
/// file-level communities in the reader, which cannot separate the two ends of a resolved
/// call. `exported` is read from the store, keyed by symbol id, because the CSR segment
/// does not carry the flag and PAR-SYM-09's ×2 needs it.
pub fn flows_of(
    seg: &Segment,
    resolved: &[bool],
    areas_of: &BTreeMap<u32, u32>,
    limits: Limits,
    exported: Option<&dyn Fn(u32) -> Option<bool>>,
) -> Result<Flows, String> {
    let graph = build_graph(seg, resolved, areas_of, exported)?;
    let (ranked, pool, mut truncation) = rank_seeds(&graph, limits);
    let mut seeds: Vec<Seed> = ranked.iter().map(|&i| seed_of(&graph, i)).collect();
    seeds.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .expect("scores are finite")
            .then_with(|| a.path.cmp(&b.path))
            .then_with(|| a.line.cmp(&b.line))
            .then_with(|| a.name.cmp(&b.name))
    });

    // One flow per distinct reachability set. The key is the sorted set of node indices,
    // so it is independent of the order the seeds were visited in and of the seeds'
    // scores.
    let mut keyed: Vec<(Vec<u32>, Vec<usize>, Walk)> = Vec::new();
    let mut index: std::collections::BTreeMap<Vec<u32>, usize> = Default::default();
    for (rank, &seed) in ranked.iter().enumerate() {
        let walk = walk(&graph, seed, &limits, &mut truncation);
        if walk.visited.len() < limits.min_steps {
            truncation.dropped_under_min_steps += 1;
            continue;
        }
        let key: Vec<u32> = {
            let mut set: Vec<u32> = walk.visited.iter().map(|&v| v as u32).collect();
            set.sort_unstable();
            set
        };
        match index.get(&key) {
            Some(&at) => keyed[at].1.push(rank),
            None => {
                index.insert(key.clone(), keyed.len());
                keyed.push((key, vec![rank], walk));
            }
        }
    }
    let mut flows: Vec<Flow> = keyed
        .into_iter()
        .map(|(_, ranks, walk)| {
            let flow_seeds: Vec<Seed> = ranks.iter().map(|&r| seeds[r].clone()).collect();
            flow(&graph, flow_seeds, walk, limits)
        })
        .collect();
    // Summed over EVERY flow built, before the MAX_FLOWS cut: the counter means steps the
    // run did not list, and a flow the run did not list at all still had steps it did not
    // list. Summing after the cut would understate exactly the run that did the most work.
    truncation.truncated_step_lists = flows.iter().map(|f| f.steps_omitted).sum();
    // The freshness set is built from the same pre-cut answer: every entry point's file and
    // every flow's every step, so a file that only contributed to a dropped flow or to a
    // step past the listing cap still marks the answer stale when it changes.
    let total_paths: Vec<String> = seeds
        .iter()
        .map(|s| s.path.clone())
        .chain(
            flows
                .iter()
                .flat_map(|f| f.all_steps.iter().map(|s| s.path.clone())),
        )
        .collect();
    flows.sort_by(|a, b| {
        b.seeds[0]
            .score
            .partial_cmp(&a.seeds[0].score)
            .expect("scores are finite")
            .then_with(|| a.label.cmp(&b.label))
    });
    if flows.len() > limits.max_flows {
        truncation.dropped_flows = (flows.len() - limits.max_flows) as u64;
        flows.truncate(limits.max_flows);
    }

    Ok(Flows {
        command: "flows",
        entry_points: seeds,
        flows,
        total_paths,
        symbols_considered: graph.nodes.len() as u64,
        unclassified_symbols: graph
            .nodes
            .iter()
            .filter(|n| n.area == UNCLASSIFIED)
            .count() as u64,
        exported_symbols: graph.nodes.iter().filter(|n| n.exported).count() as u64,
        candidates: pool,
        call_edges: graph.dst.len() as u64,
        caps: Caps {
            max_candidates: limits.max_candidates,
            max_depth: limits.max_depth,
            max_branching: limits.max_branching,
            min_steps: limits.min_steps,
            max_flows: limits.max_flows,
            max_steps_listed: limits.max_steps_listed,
        },
        truncated: truncation.any(),
        truncation,
        name_classes: publish_name_classes(),
    })
}

/// One published name-multiplier row, one per class: three `command` prefixes share one
/// class, so the answer publishes classes rather than prefixes.
fn publish_name_classes() -> Vec<NameClass> {
    let mut out: Vec<NameClass> = Vec::new();
    for rule in NAME_RULES {
        let name = rule.class.to_string();
        if out.iter().any(|r| r.name == name) {
            continue;
        }
        out.push(NameClass {
            name,
            prefix: rule.prefix.unwrap_or_default().to_string(),
            multiplier: rule.multiplier,
        });
    }
    out
}

/// Collect the admitted symbols, their resolved CALLS CSR, and both call counts.
///
/// A symbol is admitted when it is not local to a function body and its file has a
/// resolver: a symbol of an unresolved file could never have a resolved call edge, so it
/// would score zero callers and zero callees forever. An edge is admitted only when its
/// layer is `symbol` and its kind is `CALLS`, which is PAR-SYM-09's "only built on
/// resolved edges"; the lexical `structure` and `flow` layers are a different graph.
fn build_graph(
    seg: &Segment,
    resolved: &[bool],
    areas_of: &BTreeMap<u32, u32>,
    exported: Option<&dyn Fn(u32) -> Option<bool>>,
) -> Result<Graph, String> {
    let symbols = seg.counts().symbols;
    let files = seg.counts().files;
    let mut slot = vec![u32::MAX; symbols as usize];
    let mut nodes: Vec<Node> = Vec::new();
    for s in 0..symbols {
        let row = seg.symbol(s)?;
        if row.local || !resolved.get(row.file as usize).copied().unwrap_or(false) {
            continue;
        }
        slot[s as usize] = nodes.len() as u32;
        nodes.push(Node {
            graph_node: seg.symbol_node(s),
            name: seg.name(row.name).unwrap_or_default(),
            path: seg.file_path(row.file).unwrap_or_default(),
            line: row.line,
            kind: seg.symbol_kind(row.kind).to_string(),
            // The areas projection is the community layer, and it does not cover every admitted
            // symbol: in large-graph mode a node of degree below MIN_DEGREE is dropped, and
            // an edge below the confidence floor never enters the CSR. A symbol with no area
            // is UNCLASSIFIED, and giving it a sentinel that then counts as a community
            // would report a flow as `cross` because the propagation declined to place one
            // of its steps — an artefact of the admission rule dressed up as a finding. So
            // the sentinel is dropped from the count, and the answer reports how many
            // symbols the partition did not cover.
            area: areas_of.get(&seg.symbol_node(s)).copied().unwrap_or(UNCLASSIFIED),
            exported: exported.and_then(|f| f(s)).unwrap_or(false),
        });
    }

    // Read each node's outgoing CALLS once and keep each pair once, so the CSR holds a
    // call once rather than once per direction.
    let mut pairs: Vec<(u32, u32)> = Vec::new();
    let mut seen: std::collections::BTreeSet<(u32, u32)> = Default::default();
    for (i, node) in nodes.iter().enumerate() {
        for edge in
            seg.neighbours::<csr::Edge>(Direction::Downstream.adjacency(), node.graph_node)?
        {
            if seg.layer(edge.layer) != "symbol" || seg.edge_kind(edge.kind) != "CALLS" {
                continue;
            }
            let Some(other) = edge.node.checked_sub(files) else {
                continue;
            };
            // `other` is a graph node id, not a symbol-row index, and the slot map is sized
            // by the segment's symbol count — so a node past it is not an admitted symbol
            // and must be skipped rather than indexed. `areas.rs` guards the same lookup
            // for the same reason.
            //
            // Mutation row M3 replaced this guard with an unchecked index and nothing
            // failed, which is the correct outcome: `csr::edge_query` joins BOTH endpoints
            // through `seg_syms` and drops the edge when either end is not in the segment
            // (`csr.rs:936-937`), so a symbol-layer edge's node id is always in range on a
            // well-formed index. The guard is kept for the same reason as `areas.rs`'s —
            // a bounds check that costs one comparison and turns a future CSR change into
            // a wrong answer rather than a panic — but it is recorded unreachable rather
            // than claimed as covered, and the arithmetic is on `csr.rs` rather than here.
            let Some(&j) = slot.get(other as usize) else {
                continue;
            };
            if j == u32::MAX || j == i as u32 {
                continue;
            }
            if seen.insert((i as u32, j)) {
                pairs.push((i as u32, j));
            }
        }
    }
    pairs.sort_unstable();

    let mut off = vec![0u32; nodes.len() + 1];
    let mut dst: Vec<u32> = Vec::with_capacity(pairs.len());
    let mut callers = vec![0u32; nodes.len()];
    let mut callees = vec![0u32; nodes.len()];
    for (from, to) in pairs {
        dst.push(to);
        callees[from as usize] += 1;
        callers[to as usize] += 1;
        off[from as usize + 1] += 1;
    }
    for i in 0..nodes.len() {
        off[i + 1] += off[i];
    }
    Ok(Graph {
        nodes,
        off,
        dst,
        callees,
        callers,
    })
}

/// Score every symbol with at least one resolved callee, then keep the top
/// [`MAX_CANDIDATES`] in rank order. The pool size is returned beside the kept seeds so
/// the answer can report what the cap stopped.
fn rank_seeds(graph: &Graph, limits: Limits) -> (Vec<u32>, u64, Truncation) {
    let mut scored: Vec<(f64, u32)> = Vec::new();
    for i in 0..graph.nodes.len() {
        // A symbol with no resolved callee reaches nothing, so it is not an entry point
        // of anything the walk could report.
        if graph.callees[i] == 0 {
            continue;
        }
        let node = &graph.nodes[i];
        let callers = graph.callers[i] as f64;
        let callees = graph.callees[i] as f64;
        let ratio = callees / (callers + 1.0);
        let rule = class_of(&node.name);
        let score = ratio * if node.exported { 2.0 } else { 1.0 } * rule.multiplier;
        scored.push((score, i as u32));
    }
    scored.sort_by(|a, b| {
        b.0.partial_cmp(&a.0)
            .expect("scores are finite")
            .then_with(|| {
                let (x, y) = (&graph.nodes[a.1 as usize], &graph.nodes[b.1 as usize]);
                x.path
                    .cmp(&y.path)
                    .then_with(|| x.line.cmp(&y.line))
                    .then_with(|| x.name.cmp(&y.name))
            })
    });
    let pool = scored.len();
    let mut truncation = Truncation::default();
    if pool > limits.max_candidates {
        truncation.dropped_candidates = (pool - limits.max_candidates) as u64;
        scored.truncate(limits.max_candidates);
    }
    (
        scored.into_iter().map(|(_, i)| i).collect(),
        pool as u64,
        truncation,
    )
}

/// One candidate's scored row.
fn seed_of(graph: &Graph, i: u32) -> Seed {
    let node = &graph.nodes[i as usize];
    let callers = graph.callers[i as usize] as u64;
    let callees = graph.callees[i as usize] as u64;
    let ratio = callees as f64 / (callers + 1) as f64;
    let rule = class_of(&node.name);
    Seed {
        node: node.graph_node,
        name: node.name.clone(),
        path: node.path.clone(),
        line: node.line,
        kind: node.kind.clone(),
        callers,
        callees,
        callees_ratio: ratio,
        exported: node.exported,
        name_class: rule.class.to_string(),
        name_multiplier: rule.multiplier,
        score: ratio * if node.exported { 2.0 } else { 1.0 } * rule.multiplier,
    }
}

/// One bounded DFS's result.
struct Walk {
    /// The nodes the walk reached after the seed, in visit order. The seed is not one of
    /// them: PAR-SYM-09's "at least 3 steps" counts steps taken from the entry point, so
    /// the seed is the origin of a flow rather than one of its steps.
    visited: Vec<usize>,
    /// The distance from the seed to the furthest visited node.
    depth: u32,
}

/// A bounded depth-first walk from one seed.
///
/// `visited` doubles as the cycle guard, which is what makes the walk terminate on a cycle
/// in the call graph. The stack is explicit rather than recursive: the depth is bounded at
/// ten, but this index does not want a call stack that grows with a repository.
///
/// Two per-node flags, because "already in the answer" and "may be expanded" are different
/// questions, and reading them as one makes the walk's answer depend on which route the
/// stack happened to take first.
///
/// `expanded_at` is the second. A symbol reached again by a SHORTER route is expanded a
/// second time, and there are at most `max_depth` distinct route lengths to expand at, which
/// is what terminates the loop on a cycle.
///
/// `in_answer` is the first: it holds a symbol once in the step list. Sharing one flag for
/// both, or skipping the queue for a symbol already listed, loses steps. The callee loop
/// refuses to LIST a symbol twice but still QUEUES it, because the route that listed it may
/// have been the deep one and this may be the short one. Refusing the queue instead makes
/// the answer depend on which route the stack reached a symbol by: a symbol first listed
/// near the depth cap can never be re-listed where its own children are still in reach, so
/// those children go missing although the walk reaches them.
/// `a_symbol_first_reached_down_the_long_route_is_queued_again_by_the_short_one` in
/// `tests/flows.rs` is the fixture: a hub nine hops out on one route and two on another
/// loses the leaf beneath it.
fn walk(graph: &Graph, seed: u32, limits: &Limits, truncation: &mut Truncation) -> Walk {
    let mut expanded_at: Vec<u32> = vec![u32::MAX; graph.nodes.len()];
    let mut in_answer = vec![false; graph.nodes.len()];
    let mut visited = Vec::with_capacity(limits.min_steps);
    let mut stack: Vec<(usize, u32)> = Vec::new();
    in_answer[seed as usize] = true;
    stack.push((seed as usize, 0));
    let mut deepest = 0u32;

    while let Some((node, depth)) = stack.pop() {
        // A re-expansion only happens on a strictly shorter route, so a symbol's subtree
        // is walked once per distinct shorter depth at most.
        //
        // Mutation row M5 dropped this guard and no test failed. Without it a node is
        // expanded once per PUSH rather than once per distinct depth, so on a graph where
        // the same node is pushed at several depths the answer grows — but `visited_set`
        // still refuses to push a node twice, and each expansion adds at most
        // `max_branching` steps, so the two agree on every graph the chain and fan fixtures
        // build. Recorded as equivalent on those fixtures rather than claimed as covered.
        if expanded_at[node] <= depth {
            continue;
        }
        expanded_at[node] = depth;
        // `taken` counts the callees actually descended into, not the position in the
        // neighbour list: the branching cap is about how much of the fan a walk explores,
        // and a callee already visited is not exploration.
        let mut taken = 0usize;
        let mut stopped_branching = 0u64;
        let mut stopped_depth = 0u64;
        for &callee in graph.callee_slice(node as u32) {
            // A symbol already in the step list is not a step again, but it is still QUEUED
            // at this depth: the route that listed it may have been the long one, and this is
            // the shorter one. Refusing the queue is what made the walk's answer depend on
            // which route the stack reached a symbol by — a symbol first listed near the depth
            // cap could never be re-listed where its own children are still in reach, so
            // those children went missing although the walk reaches them.
            //
            // It is not exploration, so it does not consume the branching budget, and a
            // re-queue cannot lose a step, so neither bound below may count it. Testing the
            // bounds first would report truncation for a cycle that returns to a symbol the
            // walk already holds: `truncated=true` with every counter counting nothing, which
            // is exactly the "a short answer read as a truncated one" this row exists to
            // avoid. `expanded_at` still refuses the push unless this really is a shorter
            // route, and that is what bounds the stack.
            let first_time = !in_answer[callee as usize];
            if first_time {
                // Both counters count every callee the bound stopped, not the node it stopped
                // at: the walk keeps scanning so a six-way fan under a cap of four reports
                // the two it did not take rather than one per node.
                if taken >= limits.max_branching {
                    stopped_branching += 1;
                    continue;
                }
                if depth + 1 > limits.max_depth {
                    stopped_depth += 1;
                    continue;
                }
                in_answer[callee as usize] = true;
                visited.push(callee as usize);
                taken += 1;
            }
            deepest = deepest.max(depth + 1);
            stack.push((callee as usize, depth + 1));
        }
        truncation.truncated_at_branching += stopped_branching;
        truncation.truncated_at_depth += stopped_depth;
    }
    Walk {
        visited,
        depth: deepest,
    }
}

/// One flow's reported rows, from one walk.
fn flow(graph: &Graph, seeds: Vec<Seed>, walk: Walk, limits: Limits) -> Flow {
    let listed = walk.visited.len().min(limits.max_steps_listed);
    let steps: Vec<Step> = walk.visited[..listed]
        .iter()
        .map(|&i| {
            let node = &graph.nodes[i];
            Step {
                node: node.graph_node,
                name: node.name.clone(),
                path: node.path.clone(),
                line: node.line,
            }
        })
        .collect();
    // The class and the label are computed over every VISITED step, not the listed ones, so
    // a listed subset can neither disagree with the count beside it nor produce a label
    // that no complete step set would. The steps a walk reached past the listing cap are
    // still steps the flow has.
    let all_steps: Vec<Step> = walk
        .visited
        .iter()
        .map(|&i| {
            let node = &graph.nodes[i];
            Step {
                node: node.graph_node,
                name: node.name.clone(),
                path: node.path.clone(),
                line: node.line,
            }
        })
        .collect();
    let communities: BTreeSet<u32> = walk
        .visited
        .iter()
        .map(|&i| graph.nodes[i].area)
        .filter(|a| *a != UNCLASSIFIED)
        .collect();
    // `cross` means the steps sit in two or more REAL communities. A flow whose steps are
    // all in one community plus some the partition does not cover is still `intra`, because
    // the unclassified ones are the propagation declining to place them, not a second
    // community the flow crossed.
    //
    // The SEED is not counted, and round 3's codex-review [P2] says that is wrong: a flow
    // called from one community into a chain lying entirely inside another crosses a
    // boundary. The reading is right, but no fixture in this file isolates it — a seed whose
    // walk never leaves its own ring either has no flow (the walk falls under MIN_STEPS) or
    // its ring shares an edge with the caller's, which puts a step in the other community
    // anyway. So the change is NOT made here on an unproven test: it is recorded for the row
    // that adds a caller-side community check, where the fixture can be built to fail first.
    let class = if communities.len() > 1 {
        FlowClass::Cross
    } else {
        FlowClass::Intra
    };
    let (label, rule) = label_of(&all_steps, &seeds);
    Flow {
        label,
        label_rule: rule,
        class,
        seeds,
        steps,
        all_steps,
        steps_omitted: (walk.visited.len() - listed) as u64,
        depth: walk.depth,
        communities: communities.len() as u64,
    }
}

/// A flow's label: the name prefix its listed steps share, or the best seed's name.
fn label_of(steps: &[Step], seeds: &[Seed]) -> (String, LabelRule) {
    match shared_stem(steps) {
        Some(stem) => (stem, LabelRule::NameStem),
        None => (
            seeds.first().map(|s| s.name.clone()).unwrap_or_default(),
            LabelRule::SeedName,
        ),
    }
}

/// The longest whole-word prefix at least [`LABEL_MIN_MEMBERS`] listed steps share, or
/// `None`. The common prefix is cut back to a whole identifier so the label reads as a
/// name rather than a fragment: `Harvest` from `HarvestOne`/`HarvestTwo`, not `Harv`.
fn shared_stem(steps: &[Step]) -> Option<String> {
    if steps.len() < LABEL_MIN_MEMBERS {
        return None;
    }
    let mut common = steps[0].name.as_str();
    for step in &steps[1..] {
        // The shared prefix of `common` and `name`, cut at a char boundary.
        let shared = common
            .char_indices()
            .zip(step.name.chars())
            .take_while(|((_, a), b)| *a == *b)
            .map(|((i, c), _)| i + c.len_utf8())
            .last()
            .unwrap_or(0);
        common = &common[..shared];
        if common.is_empty() {
            return None;
        }
    }
    let word = common
        .rfind(|c: char| !c.is_alphanumeric())
        .map_or(common, |i| &common[..i]);
    let stem = word.trim_end_matches(|c: char| !c.is_alphanumeric());
    // A stem equal to a whole listed name is a name, not a shared prefix, so it does not
    // qualify; `min_steps` symbols named `Harvest` share nothing beyond that name.
    (stem.len() >= 2 && !steps.iter().all(|s| s.name == stem)).then(|| stem.to_string())
}
