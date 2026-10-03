//! Functional areas (WS-38b; PAR-SYM-08): communities at **symbol** granularity over
//! the resolved CALLS/EXTENDS/IMPLEMENTS projection of the code index.
//!
//! The file-level communities in `reader.rs` (and the legacy `symbolgraph::compute_clusters`
//! beside it) group whole files, so two unrelated functions in one file cannot be
//! separated. PAR-SYM-08 asks for the same idea one level down: seeded, deterministic
//! label propagation over the resolved *symbol* edges, with a large-graph mode above
//! 10k symbols, labels from the members' folders with generic folders skipped, and a
//! cohesion figure sampled over at most 50 members.
//!
//! Memory is the constraint that shapes the algorithm. The adjacency is a CSR of the
//! admitted edges (two `u32` per directed entry, built once), and everything else is one
//! small vector per admitted symbol: the label, the seeded sort key, the degree, the
//! symbol row. Per-pass scratch is one map over a single node's neighbours. Nothing
//! proportional to the repository's text is held, and nothing is read back per pass the way
//! the file-level reader re-reads its adjacency, because a symbol graph has ten times the
//! nodes and a CSR over it is a few bytes each.
//!
//! Determinism is the other constraint. Every order in the answer is a total order over
//! ids the index itself assigns, and the one genuinely arbitrary choice — which of two
//! equally heavy communities a node adopts — is broken by a seeded hash of the node, so
//! two runs with one seed are byte-identical and two seeds generally differ. The generator
//! is a splitmix64 step written here: the crate takes no RNG dependency, and a label
//! propagation needs one bit of reproducible order, not a distribution.

use std::collections::BTreeMap;

use serde::Serialize;

use super::csr::{self, Segment, SymRow};
use super::impact::Direction;

/// Symbols above which the large-graph mode applies (PAR-SYM-08).
pub const LARGE_GRAPH_SYMBOLS: u64 = 10_000;
/// Label-propagation resolution in large-graph mode: a node adopts a neighbouring
/// community only when the weight it shares with that community reaches
/// `1 / resolution` of its own total incident weight, which keeps a node from being
/// pulled out of its subsystem by one weakly-attached edge.
pub const LARGE_RESOLUTION: f64 = 2.0;
/// Passes large-graph mode runs at most (PAR-SYM-08). Standard mode runs to a fixpoint
/// like the file-level reader whose rules it mirrors.
pub const LARGE_MAX_ITERATIONS: u32 = 3;
/// Passes standard mode runs at most.
pub const STANDARD_MAX_ITERATIONS: u32 = 20;
/// A node of degree below this carries no community evidence and is dropped in
/// large-graph mode (PAR-SYM-08). Standard mode keeps it, like the file-level reader:
/// one isolated symbol is a real answer, not noise.
pub const MIN_DEGREE: u32 = 2;
/// Edge confidence below this (quantized `0..=255`) is dropped in large-graph mode.
pub const LARGE_MIN_CONFIDENCE: u8 = 128;
/// Members an area's cohesion and keywords are computed over.
pub const COHESION_SAMPLE: usize = 50;
/// Keywords per area.
pub const MAX_KEYWORDS: usize = 8;
/// Members listed per area.
pub const MAX_MEMBERS: usize = 200;
/// Members a folder must share before its name is the label.
pub const LABEL_MIN_MEMBERS: usize = 2;
/// The default seed.
pub const DEFAULT_SEED: u64 = 0;

/// Which rule produced an area's label.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum LabelRule {
    /// A non-generic folder shared by enough of the members.
    Folder,
    /// The most common leading word of the members' names, no folder qualifying.
    NamePrefix,
    /// Neither qualified, so the area's rank.
    Fallback,
}

/// The label rules, in the order they are tried; the same three as [`LabelRule`].
pub const LABEL_RULES: [&str; 3] = ["folder", "name_prefix", "cluster_n"];

/// Folders that name a layout convention rather than a subsystem, skipped when an area's
/// label is chosen (PAR-SYM-08: "folder-based labels with generic folders skipped"). A
/// table, not a ladder: the label is the most common non-generic folder shared by enough
/// of the members.
pub const GENERIC_FOLDERS: [&str; 21] = [
    "api", "app", "bin", "build", "cmd", "core", "dist", "examples", "gen", "internal", "lib",
    "main", "pkg", "public", "src", "test", "testdata", "tests", "tools", "util", "utils",
];

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

/// One member symbol of an area.
#[derive(Debug, Clone, Serialize)]
pub struct Member {
    /// The graph node of the symbol (files + symbol id).
    pub node: u32,
    pub name: String,
    pub path: String,
    pub line: u32,
}

/// One functional area.
#[derive(Debug, Clone, Serialize)]
pub struct Area {
    /// Rank by size, ties by smallest member node: stable across runs.
    pub area_id: u32,
    pub label: String,
    pub label_rule: LabelRule,
    /// Members in (path, line, name-id) order, capped at [`MAX_MEMBERS`]. The last key
    /// is the index's interned name id, which is alphabetical, so two symbols on one
    /// source line come back in name order rather than the order the extractor saw them.
    pub members: Vec<Member>,
    /// `members.len()`.
    pub size: usize,
    /// Every member, which exceeds `size` only when `members` was capped.
    pub total_size: usize,
    /// Internal edge weight over possible internal edges, over at most
    /// [`COHESION_SAMPLE`] members: 1.0 is a clique, 0.0 no internal edge at all.
    pub cohesion: f64,
    /// What the members share: their folder and the common leading words of their names.
    pub keywords: Vec<String>,
    /// Members the cohesion sample covered.
    pub cohesion_sample: usize,
    /// `true` when [`MAX_MEMBERS`] capped the listed members.
    pub members_truncated: bool,
    /// Every member's path, whether or not the member is listed. Not serialized: the
    /// dispatch uses it to compute freshness over the whole answer rather than the capped
    /// member rows, and the answer's shape must not grow a field for it.
    #[serde(skip)]
    pub total_paths: Vec<String>,
}

/// The areas of one index generation.
#[derive(Debug, Clone, Serialize)]
pub struct Areas {
    pub command: &'static str,
    pub seed: u64,
    /// `standard` or `large`.
    pub mode: &'static str,
    pub resolution: f64,
    /// Passes run, whether or not they reached a fixpoint.
    pub iterations: u32,
    /// Global symbols the projection considered.
    pub symbols: u64,
    /// Symbols left with no admitted edge: each is its own area of size 1.
    pub singleton_symbols: u64,
    pub areas: Vec<Area>,
    /// Nodes of degree below [`MIN_DEGREE`], dropped in large-graph mode.
    pub dropped_low_degree: u64,
    /// Edges below [`LARGE_MIN_CONFIDENCE`], dropped in large-graph mode.
    pub dropped_low_confidence_edges: u64,
    /// Members a cohesion sample covers.
    pub cohesion_sample_size: usize,
    /// The label rules, in the order they are tried.
    pub label_rules: [&'static str; 3],
}

// ---------------------------------------------------------------------------
// The seeded tie-break
// ---------------------------------------------------------------------------

/// One splitmix64 step: the smallest generator that is still exactly reproducible across
/// platforms and runs. No RNG dependency is added for it — §5.1 rule 12 would ask a
/// dependency's licence to earn one bit of order, and it does not.
fn splitmix64(state: &mut u64) -> u64 {
    *state = state.wrapping_add(0x9E37_79B9_7F4A_7C15);
    let mut z = *state;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

/// The seed's hash of one graph node: the only place the seed is read.
fn node_key(seed: u64, node: u32) -> u64 {
    let mut s = seed.wrapping_add((node as u64).wrapping_mul(0x9E37_79B9_7F4A_7C15));
    splitmix64(&mut s)
}

/// The word a set of members' names share, for the name-prefix label.
///
/// `invoice`, `invoice_service` and `InvoiceStore` all start with the word `invoice`;
/// `feed`, `feeder` and `feeds` share the stem `fee`. The longest word that at least two
/// members start with wins, and ties go to the alphabetically first, so the rule is a
/// total order over the members rather than a guess about one name.
fn shared_prefix(names: &[String]) -> Option<String> {
    if names.len() < LABEL_MIN_MEMBERS {
        return None;
    }
    let mut best: Option<String> = None;
    // Candidates come from every member, not from the first one: `rows` is ordered by
    // (path, line, name), so the first member shares no leading word with the rest often
    // enough that seeding from it alone missed a prefix two later members do share.
    let stems: Vec<Vec<String>> = names.iter().map(|n| word_stems(n.as_str())).collect();
    let mut candidates: Vec<String> = stems.iter().flat_map(|s| s.iter().cloned()).collect();
    // Longest first, so the longest shared stem wins; duplicates collapse.
    candidates.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a.cmp(b)));
    candidates.dedup();
    for candidate in candidates {
        let shared = stems.iter().filter(|s| s.contains(&candidate)).count();
        if shared >= LABEL_MIN_MEMBERS {
            best = Some(candidate);
            break;
        }
    }
    best
}

/// The leading words of one name, longest first: split on `_`, on the camel-case
/// humps, and on the letters-versus-digits boundary, each stem lowercased and with a
/// trailing plural `s` dropped so `feeds` and `feed` share `fee`.
fn word_stems(name: &str) -> Vec<String> {
    let mut words: Vec<String> = Vec::new();
    for part in name.split('_').filter(|p| !p.is_empty()) {
        let mut cur = String::new();
        let mut prev_lower = false;
        for c in part.chars() {
            if c.is_ascii_uppercase() && prev_lower {
                words.push(std::mem::take(&mut cur));
            }
            prev_lower = c.is_ascii_lowercase() || c.is_ascii_digit();
            cur.push(c);
        }
        if !cur.is_empty() {
            words.push(cur);
        }
    }
    let mut out: Vec<String> = Vec::new();
    for w in words {
        let lower = w.to_ascii_lowercase();
        out.push(lower.clone());
        if let Some(stem) = lower.strip_suffix('s').filter(|s| !s.is_empty()) {
            out.push(stem.to_string());
        }
    }
    out.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a.cmp(b)));
    out.dedup();
    out
}

// ---------------------------------------------------------------------------
// The projection
// ---------------------------------------------------------------------------

/// The symbol projection the communities are computed over: admitted nodes, one CSR of
/// the undirected admitted edges, and the per-node facts the answer needs.
struct Projection<'a> {
    /// The segment the nodes and their names come from.
    seg: &'a Segment,
    /// The graph node of each admitted symbol, in slot order.
    nodes: Vec<u32>,
    /// The symbol row behind each slot.
    rows: Vec<SymRow>,
    /// The seed's sort key per slot.
    key: Vec<u64>,
    /// CSR offsets per slot, `nodes.len() + 1` long.
    off: Vec<u32>,
    /// The other slot of each directed adjacency entry.
    dst: Vec<u32>,
    /// The weight of each directed entry, parallel to `dst`.
    wgt: Vec<u32>,
    /// Degree per slot over the admitted edges.
    degree: Vec<u32>,
    /// Total incident weight per slot.
    strength: Vec<u64>,
    /// The order propagation visits slots in, under the seed.
    order: Vec<u32>,
    /// Whether each slot survived large-graph pruning. A dropped slot has no edges and no
    /// community evidence, so it is not reported as an area.
    kept: Vec<bool>,
}

impl<'a> Projection<'a> {
    /// The neighbours of slot `i`, with their weights.
    fn neighbours(&self, i: u32) -> (&[u32], &[u32]) {
        let (a, b) = (
            self.off[i as usize] as usize,
            self.off[i as usize + 1] as usize,
        );
        (&self.dst[a..b], &self.wgt[a..b])
    }
}

/// Build the CSR from the admitted edges, with the per-node counts each mode reports.
fn assemble<'a>(
    seg: &'a Segment,
    nodes: Vec<u32>,
    rows: Vec<SymRow>,
    key: Vec<u64>,
    mut edges: Vec<(u32, u32, u32)>,
    large: bool,
) -> (Projection<'a>, u64) {
    let n = nodes.len();
    // Degree over the edges admitted so far, which is the count PAR-SYM-08's rule speaks
    // about: the first pass counts every admitted edge, the second what survives the drop.
    let mut pre = vec![0u32; n];
    for &(a, b, _) in &edges {
        pre[a as usize] += 1;
        pre[b as usize] += 1;
    }
    let mut dropped_low_degree = 0u64;
    let mut kept: Vec<bool> = vec![true; n];
    if large {
        // A node of degree below 2 carries no community evidence. Its edges go with it,
        // which can leave a neighbour below 2 as well: the drop is repeated until it
        // reaches a fixpoint, or pruning a path one edge at a time would empty it. Every
        // node that loses its last edge in any pass is dropped, so the count answers for
        // the whole pruning rather than for its first pass.
        loop {
            let before: Vec<bool> = kept.clone();
            edges.retain(|&(a, b, _)| {
                pre[a as usize] >= MIN_DEGREE && pre[b as usize] >= MIN_DEGREE
            });
            pre.fill(0);
            for &(a, b, _) in &edges {
                pre[a as usize] += 1;
                pre[b as usize] += 1;
            }
            for (i, k) in kept.iter_mut().enumerate() {
                if *k && pre[i] < MIN_DEGREE {
                    *k = false;
                }
            }
            if kept == before {
                break;
            }
        }
        dropped_low_degree = kept.iter().filter(|k| !**k).count() as u64;
    }
    let mut degree = vec![0u32; n];
    let mut strength = vec![0u64; n];
    for &(a, b, w) in &edges {
        degree[a as usize] += 1;
        degree[b as usize] += 1;
        strength[a as usize] += w as u64;
        strength[b as usize] += w as u64;
    }
    let mut counts = vec![0u32; n + 1];
    for &(a, b, _) in &edges {
        counts[a as usize + 1] += 1;
        counts[b as usize + 1] += 1;
    }
    for i in 0..n {
        counts[i + 1] += counts[i];
    }
    let mut off = counts.clone();
    let mut dst = vec![0u32; counts[n] as usize];
    let mut wgt = vec![0u32; counts[n] as usize];
    for &(a, b, w) in &edges {
        for (from, to) in [(a, b), (b, a)] {
            let at = off[from as usize] as usize;
            dst[at] = to;
            wgt[at] = w;
            off[from as usize] += 1;
        }
    }
    let mut order: Vec<u32> = (0..n as u32).collect();
    order.sort_by_key(|&i| (key[i as usize], i));
    (
        Projection {
            seg,
            nodes,
            rows,
            key,
            off: counts,
            dst,
            wgt,
            degree,
            strength,
            order,
            kept,
        },
        dropped_low_degree,
    )
}

/// Collect the projection over the resolved CALLS, EXTENDS and IMPLEMENTS symbol edges.
///
/// An edge's weight is its stored occurrences times its stored confidence (quantized
/// `0..=255`, `0` meaning unresolved), so a resolved edge outweighs a lexical guess by two
/// orders of magnitude and propagation follows the resolved graph.
fn project<'a>(
    seg: &'a Segment,
    resolved: &[bool],
    seed: u64,
    large: bool,
) -> Result<(Projection<'a>, Stats), String> {
    let files = seg.counts().files;
    let mut slot = vec![u32::MAX; seg.counts().symbols as usize];
    let mut nodes: Vec<u32> = Vec::new();
    let mut rows: Vec<SymRow> = Vec::new();
    let mut key: Vec<u64> = Vec::new();
    let mut symbols = 0u64;
    for s in 0..seg.counts().symbols {
        let row = seg.symbol(s)?;
        if !row.local {
            symbols += 1;
        }
        if row.local || !resolved.get(row.file as usize).copied().unwrap_or(false) {
            continue;
        }
        slot[s as usize] = nodes.len() as u32;
        nodes.push(seg.symbol_node(s));
        rows.push(row);
        key.push(node_key(seed, seg.symbol_node(s)));
    }
    // Both directions are read per node and a pair is kept once, so the CSR holds one
    // undirected edge per pair rather than two.
    let mut pairs: BTreeMap<(u32, u32), (u64, u8)> = BTreeMap::new();
    let mut dropped_low_confidence = 0u64;
    for (i, &node) in nodes.iter().enumerate() {
        for dir in [Direction::Downstream, Direction::Upstream] {
            for edge in seg.neighbours::<csr::Edge>(dir.adjacency(), node)? {
                if seg.layer(edge.layer) != "symbol" {
                    continue;
                }
                if !matches!(seg.edge_kind(edge.kind), "CALLS" | "EXTENDS" | "IMPLEMENTS") {
                    continue;
                }
                let Some(other) = edge.node.checked_sub(files) else {
                    continue;
                };
                let j = slot[other as usize];
                if j == u32::MAX || j == i as u32 {
                    continue;
                }
                let pair = if i < j as usize {
                    (i as u32, j)
                } else {
                    (j, i as u32)
                };
                let slot = pairs.entry(pair).or_insert((0u64, u8::MAX));
                // u64, not u32: this accumulates over every matching record in both
                // directions, and a u32 would overflow (and panic in debug builds) on a
                // pair with a large number of repeated edges.
                slot.0 += edge.weight as u64 * edge.confidence as u64;
                slot.1 = slot.1.min(edge.confidence);
            }
        }
    }
    // PAR-SYM-08's confidence rule, applied after the pairs are known so one drop count
    // answers for both directions of every low-confidence pair.
    let mut edges: Vec<(u32, u32, u32)> = Vec::with_capacity(pairs.len());
    for ((a, b), (w, conf)) in pairs {
        if large && conf < LARGE_MIN_CONFIDENCE {
            dropped_low_confidence += 1;
            continue;
        }
        // The CSR's per-entry weight is u32; the pair total is accumulated in u64 above so
        // the sum cannot overflow on the way here.
        edges.push((a, b, w.min(u64::from(u32::MAX)) as u32));
    }
    let (p, dropped_low_degree) = assemble(seg, nodes, rows, key, edges, large);
    Ok((
        p,
        Stats {
            symbols,
            dropped_low_degree,
            dropped_low_confidence_edges: dropped_low_confidence,
        },
    ))
}

/// What the projection contributes to the answer's counts.
struct Stats {
    /// Global symbols considered, which is what the mode switches on.
    symbols: u64,
    dropped_low_degree: u64,
    dropped_low_confidence_edges: u64,
}

// ---------------------------------------------------------------------------
// Propagation
// ---------------------------------------------------------------------------

/// Label propagation over the projection, in the seeded visit order.
///
/// A node adopts the neighbouring community holding the most of its incident weight. In
/// standard mode the heaviest wins outright, ties broken by the seeded key of the
/// community's smallest member. In large-graph mode a community must also hold at least
/// `1 / resolution` of the node's incident weight, which is what stops a node leaving its
/// subsystem for one weakly-attached edge; a node whose own community already holds that
/// share keeps it.
fn propagate(p: &Projection, resolution: f64, max_iterations: u32) -> (Vec<u32>, u32) {
    let mut community: Vec<u32> = (0..p.nodes.len() as u32).collect();
    // Each entry is the weight a community holds of one node's incident weight, and the
    // smallest slot in it: the seeded tie-break reads that slot's key, so it is the one
    // community representative that must be the lowest under the seed.
    let mut by_comm: BTreeMap<u32, (u64, u32)> = BTreeMap::new();
    let mut passes = 0;
    for _ in 0..max_iterations {
        passes += 1;
        let mut changed = false;
        for &i in &p.order {
            if p.degree[i as usize] == 0 {
                continue;
            }
            by_comm.clear();
            let (dst, wgt) = p.neighbours(i);
            for (&d, &w) in dst.iter().zip(wgt) {
                let e = by_comm
                    .entry(community[d as usize])
                    .or_insert((0, u32::MAX));
                e.0 += w as u64;
                e.1 = e.1.min(d);
            }
            let mine = community[i as usize];
            // PAR-SYM-08's resolution: in large-graph mode a community must hold at
            // least 1/resolution of the node's incident weight to be adopted, which is
            // what keeps one weakly-attached edge from moving a node out of its
            // subsystem. Standard mode has no such floor: at resolution 1.0 the
            // threshold would be the node's entire incident weight, so no node could
            // ever leave the community it started in and standard-mode clustering
            // would be a no-op. Standard mode adopts the heaviest community outright.
            let threshold = if resolution > 1.0 {
                (p.strength[i as usize] as f64 / resolution).ceil() as u64
            } else {
                1
            };
            let mut best: Option<(u64, u64, u32, u32)> = None;
            for (&c, &(weight, first)) in &by_comm {
                if weight < threshold.max(1) {
                    continue;
                }
                // heaviest, then the community whose smallest member sorts first under
                // the seed, then the smallest community id: a total order.
                let cand = (weight, p.key[first as usize], c, first);
                best = Some(match best {
                    Some(b) if (b.0, b.1, b.2) <= (cand.0, cand.1, cand.2) => b,
                    _ => cand,
                });
            }
            if let Some((_, _, c, _)) = best {
                if c != mine {
                    community[i as usize] = c;
                    changed = true;
                }
            }
        }
        if !changed {
            break;
        }
    }
    (community, passes)
}

// ---------------------------------------------------------------------------
// Areas from the labels
// ---------------------------------------------------------------------------

/// Turn the per-node labels into ranked areas with their labels, cohesion and keywords.
fn areas(p: &Projection, community: &[u32]) -> Vec<Area> {
    let seg = p.seg;
    let mut groups: BTreeMap<u32, Vec<u32>> = BTreeMap::new();
    for (i, c) in community.iter().enumerate() {
        // A slot large-graph pruning dropped has no edges left and no community evidence,
        // so it is not an area: PAR-SYM-08 drops those nodes rather than reporting them.
        if !p.kept[i] {
            continue;
        }
        groups.entry(*c).or_default().push(i as u32);
    }
    let mut ranked: Vec<Vec<u32>> = groups.into_values().collect();
    // Size, then the seeded key of the smallest member, then the slot: a total order, so
    // the ranking is the same on every run with one seed.
    ranked.sort_by_key(|m| {
        (
            std::cmp::Reverse(m.len()),
            p.key[m.iter().copied().min().unwrap_or(u32::MAX) as usize],
            m.iter().copied().min().unwrap_or(u32::MAX),
        )
    });
    let mut out = Vec::with_capacity(ranked.len());
    for (rank, members) in ranked.into_iter().enumerate() {
        // Members in (path, line, name) order: file order, with one source line's symbols
        // in name order. The name is read once per member rather than compared by its
        // interned id, which sorts by deduplicated name and so is not alphabetical.
        let mut rows: Vec<(String, u32, String, u32)> = members
            .iter()
            .map(|&i| {
                let row = p.rows[i as usize];
                (
                    seg.file_path(row.file).unwrap_or_default(),
                    row.line,
                    seg.name(row.name).unwrap_or_default(),
                    i,
                )
            })
            .collect();
        rows.sort();
        let sample = rows.len().min(COHESION_SAMPLE);
        let listed = rows.len().min(MAX_MEMBERS);
        let mut out_members = Vec::with_capacity(listed);
        for (_, line, name, i) in &rows[..listed] {
            let row = p.rows[*i as usize];
            out_members.push(Member {
                node: p.nodes[*i as usize],
                name: name.clone(),
                path: seg.file_path(row.file).unwrap_or_default(),
                line: *line,
            });
        }
        let (label, rule) = label_for(&rows, rank);
        out.push(Area {
            area_id: rank as u32,
            label,
            label_rule: rule,
            cohesion: cohesion(p, &rows[..sample]),
            keywords: keywords(&rows, sample),
            cohesion_sample: sample,
            members_truncated: listed < rows.len(),
            size: listed,
            total_size: members.len(),
            members: out_members,
            total_paths: rows
                .iter()
                .map(|r| seg.file_path(p.rows[r.3 as usize].file).unwrap_or_default())
                .collect(),
        });
    }
    out
}

/// An area's label and the rule that chose it: the most common non-generic folder shared
/// by enough members, else the members' common name prefix, else the area's rank.
fn label_for(rows: &[(String, u32, String, u32)], rank: usize) -> (String, LabelRule) {
    let mut folders: BTreeMap<String, usize> = BTreeMap::new();
    for (path, _, _, _) in rows {
        let folder = path
            .split('/')
            .rev()
            .skip(1)
            .find(|p| !GENERIC_FOLDERS.contains(p));
        if let Some(f) = folder {
            *folders.entry(f.to_string()).or_default() += 1;
        }
    }
    // The folder rule: the most common non-generic folder enough members share, ties to
    // the alphabetically first, so the label is a total order over the members.
    let folder = folders
        .iter()
        .filter(|(_, n)| **n >= LABEL_MIN_MEMBERS)
        .max_by(|a, b| a.1.cmp(b.1).then_with(|| b.0.cmp(a.0)))
        .map(|(k, _)| k.clone());
    if let Some(f) = folder {
        return (f, LabelRule::Folder);
    }
    // The name-prefix rule: the longest leading word enough members share.
    let names: Vec<String> = rows.iter().map(|(_, _, n, _)| n.clone()).collect();
    if let Some(p) = shared_prefix(&names) {
        return (p, LabelRule::NamePrefix);
    }
    (format!("Cluster_{rank}"), LabelRule::Fallback)
}

/// What an area's members share: the folder of the sample's members and the leading words
/// of their names, most frequent first, bounded to [`MAX_KEYWORDS`].
fn keywords(rows: &[(String, u32, String, u32)], sample: usize) -> Vec<String> {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    let mut folder: BTreeMap<String, usize> = BTreeMap::new();
    for (path, _, name, _) in &rows[..sample] {
        // The same generic-folder filter `label_for` applies, so one rule governs the
        // label and the keywords: `src` or `tests` cannot become a keyword either.
        if let Some(f) = path
            .split('/')
            .rev()
            .skip(1)
            .find(|p| !GENERIC_FOLDERS.contains(p))
        {
            *folder.entry(f.to_string()).or_default() += 1;
        }
        *counts.entry(name.clone()).or_default() += 1;
    }
    let mut out: Vec<String> = folder
        .into_iter()
        .filter(|(_, n)| *n >= LABEL_MIN_MEMBERS)
        .map(|(f, _)| f)
        .collect();
    for (n, c) in counts {
        if out.len() >= MAX_KEYWORDS {
            break;
        }
        if c >= 2 && !out.contains(&n) {
            out.push(n);
        }
    }
    out
}

/// Internal weight over possible internal edges, over the sampled members: the fraction
/// of the sample's pairs that share an admitted edge. A singleton scores 0.0 rather than
/// 1.0, because one member has no pair to be cohesive with.
fn cohesion(p: &Projection, sample: &[(String, u32, String, u32)]) -> f64 {
    let n = sample.len();
    if n < 2 {
        return 0.0;
    }
    let slots: Vec<u32> = sample.iter().map(|r| r.3).collect();
    let in_sample: std::collections::BTreeSet<u32> = slots.iter().copied().collect();
    // One pass over each sampled member's adjacency, counting the edges whose other end is
    // also in the sample. Asking weight_between per pair instead scanned one member's whole
    // adjacency once per pair, so a dense area cost sample-pairs times degree.
    let mut internal = 0u64;
    for &s in &slots {
        let (dst, _) = p.neighbours(s);
        internal += dst.iter().filter(|d| in_sample.contains(d)).count() as u64;
    }
    // Each internal edge is counted from both of its ends.
    let present = internal / 2;
    let n = u64::try_from(n).unwrap_or(0);
    let possible = n * n.saturating_sub(1) / 2;
    if possible == 0 {
        return 0.0;
    }
    present as f64 / possible as f64
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

/// The areas of one index generation: `index areas`, read-only.
///
/// The mode follows the symbol count rather than a flag (PAR-SYM-08's "above 10k
/// symbols"), and every bound the two modes apply is reported, so a reader can tell which
/// rules produced the answer.
pub fn areas_of(seg: &Segment, resolved: &[bool], seed: u64) -> Result<Areas, String> {
    let large = global_symbols(seg, resolved) > LARGE_GRAPH_SYMBOLS;
    let (p, stats) = project(seg, resolved, seed, large)?;
    let (mode, resolution, cap) = if large {
        ("large", LARGE_RESOLUTION, LARGE_MAX_ITERATIONS)
    } else {
        ("standard", 1.0, STANDARD_MAX_ITERATIONS)
    };
    let (community, iterations) = propagate(&p, resolution, cap);
    // A kept slot with no edge left is an isolated symbol; a dropped one is not reported
    // at all, so it is not counted here either.
    let singleton_symbols = p
        .degree
        .iter()
        .zip(p.kept.iter())
        .filter(|pair| *pair.0 == 0 && *pair.1)
        .count() as u64;
    Ok(Areas {
        command: "areas",
        seed,
        mode,
        resolution,
        iterations,
        symbols: stats.symbols,
        singleton_symbols,
        areas: areas(&p, &community),
        dropped_low_degree: stats.dropped_low_degree,
        dropped_low_confidence_edges: stats.dropped_low_confidence_edges,
        cohesion_sample_size: COHESION_SAMPLE,
        label_rules: LABEL_RULES,
    })
}

/// The global symbols of the files this projection would consider: the count the mode
/// switches on, and the denominator a reader compares against `singleton_symbols`.
fn global_symbols(seg: &Segment, resolved: &[bool]) -> u64 {
    (0..seg.counts().symbols)
        .filter_map(|s| seg.symbol(s).ok())
        .filter(|row| !row.local && resolved.get(row.file as usize).copied().unwrap_or(false))
        .count() as u64
}

/// The areas of an opened snapshot: its file table says which files have a resolver, and
/// its segment holds the symbol edges.
pub fn areas_for(snapshot: &super::reader::Snapshot, seed: u64) -> Result<Areas, String> {
    areas_of(&snapshot.seg, snapshot.resolved_files(), seed)
}
