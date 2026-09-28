//! Shortest-path trace (PAR-IMP-07; WS-35): how `from` reaches `to` over typed edges,
//! with each hop's edge kind, confidence, provenance and reason.
//!
//! The search runs breadth-first downstream from `from` (what `from` calls, imports,
//! extends or uses) and, when that finds no path, upstream (what depends on `from`), on
//! the same [`Graph`] and layer rule as impact. Its caps: depth 10 by default and 30 at
//! most, 200 edges followed per node, 5,000 nodes per level and 50,000 visited. When no
//! path exists within them, the answer names the furthest node the downstream search
//! reached and its depth, and says which caps either search hit.

use std::collections::{BTreeSet, HashMap};

use serde::Serialize;

use super::csr::{self, Edge};
use super::impact::{self, Direction, Graph, Relations};

pub const DEFAULT_DEPTH: usize = 10;
pub const MAX_DEPTH: usize = 30;
pub const FANOUT: usize = 200;
pub const LEVEL_ROWS: usize = 5_000;
pub const VISITED: usize = 50_000;

#[derive(Debug, Clone)]
pub struct TraceQuery {
    pub from: String,
    pub to: String,
    pub max_depth: usize,
    pub relations: Relations,
    pub min_confidence: f64,
    pub include_tests: bool,
}

impl TraceQuery {
    pub fn new(from: &str, to: &str) -> TraceQuery {
        TraceQuery {
            from: from.to_string(),
            to: to.to_string(),
            max_depth: DEFAULT_DEPTH,
            relations: Relations::DEPENDENCIES,
            min_confidence: 0.0,
            include_tests: true,
        }
    }

    /// Apply `--name=value` flags (the `symbolgraph trace` CLI).
    pub fn apply_flags<I: IntoIterator<Item = String>>(&mut self, flags: I) -> Result<(), String> {
        for flag in flags {
            let (name, value) = flag
                .strip_prefix("--")
                .and_then(|f| f.split_once('='))
                .ok_or_else(|| format!("expected --name=value, got {flag:?}"))?;
            let set = TRACE_FLAGS
                .iter()
                .find(|(n, _)| *n == name)
                .map(|(_, set)| *set)
                .ok_or_else(|| format!("unknown trace flag --{name}"))?;
            set(self, value).map_err(|e| format!("--{name}: {e}"))?;
        }
        Ok(())
    }
}

type Setter = fn(&mut TraceQuery, &str) -> Result<(), String>;

const TRACE_FLAGS: &[(&str, Setter)] = &[
    ("max-depth", |q, v| {
        q.max_depth = impact::bounded(v, 1, MAX_DEPTH as u64)? as usize;
        Ok(())
    }),
    ("relation-types", |q, v| {
        q.relations = match v.trim() {
            "" => Relations::DEPENDENCIES,
            list => Relations::parse(list)?,
        };
        Ok(())
    }),
    ("min-confidence", |q, v| {
        let c: f64 = v.parse().map_err(|_| "not a number")?;
        if !(0.0..=1.0).contains(&c) {
            return Err("must be between 0 and 1".into());
        }
        q.min_confidence = c;
        Ok(())
    }),
    ("include-tests", |q, v| {
        q.include_tests = impact::boolean(v)?;
        Ok(())
    }),
];

/// One end of a hop.
#[derive(Debug, Clone, Serialize)]
pub struct End {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub kind: String,
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub line: Option<u32>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Hop {
    pub from: End,
    pub to: End,
    pub edge: String,
    pub confidence: f64,
    pub provenance: String,
    pub reason: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub access: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct Furthest {
    #[serde(flatten)]
    pub end: End,
    pub depth: usize,
}

/// Which caps a search hit.
#[derive(Debug, Clone, Copy, Serialize, Default)]
pub struct CapHits {
    /// Nodes whose edges were cut at [`FANOUT`].
    pub fanout: usize,
    pub rows_per_level: bool,
    pub visited: bool,
    pub depth: bool,
}

impl CapHits {
    fn any(&self) -> bool {
        self.fanout > 0 || self.rows_per_level || self.visited || self.depth
    }

    /// The caps either of two searches hit.
    fn merge(self, other: CapHits) -> CapHits {
        CapHits {
            fanout: self.fanout + other.fanout,
            rows_per_level: self.rows_per_level || other.rows_per_level,
            visited: self.visited || other.visited,
            depth: self.depth || other.depth,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
pub struct Caps {
    pub max_depth: usize,
    pub fanout: usize,
    pub rows_per_level: usize,
    pub visited: usize,
}

#[derive(Debug, Clone, Serialize)]
pub struct Trace {
    pub from: String,
    pub to: String,
    pub found: bool,
    /// Hops on the path.
    pub length: usize,
    /// The files along the path, consecutive repeats collapsed.
    pub path: Vec<String>,
    /// `downstream`: each hop's `from` depends on its `to`; `upstream`: each hop's `to`
    /// depends on its `from`. Absent when no path was found.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub direction: Option<Direction>,
    pub hops: Vec<Hop>,
    /// The furthest node the downstream search reached, when no path was found.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub furthest: Option<Furthest>,
    /// A cap cut a search short, so a longer path may exist.
    pub truncated: bool,
    /// The caps hit by the searches that ran (downstream, then upstream when downstream
    /// found no path).
    pub caps_hit: CapHits,
    pub caps: Caps,
    pub from_targets: usize,
    pub to_targets: usize,
    pub generated_at: String,
}

/// One search's outcome.
struct Search {
    hit: Option<u32>,
    parent: HashMap<u32, (u32, Edge)>,
    furthest: Option<(u32, usize)>,
    caps: CapHits,
}

fn search<G: Graph + ?Sized>(
    g: &G,
    q: &TraceQuery,
    dir: Direction,
    from: &[u32],
    to: &BTreeSet<u32>,
) -> Result<Search, String> {
    let min_q = csr::quantize_confidence(q.min_confidence);
    let mut s = Search {
        hit: from.iter().copied().find(|n| to.contains(n)),
        parent: HashMap::new(),
        furthest: None,
        caps: CapHits::default(),
    };
    if s.hit.is_some() {
        return Ok(s);
    }
    let mut seen: BTreeSet<u32> = from.iter().copied().collect();
    let mut frontier: Vec<u32> = from.to_vec();
    for depth in 1..=q.max_depth {
        let mut next = Vec::new();
        for &node in &frontier {
            let layer = match g.resolved(g.file_of(node)?) {
                true => "symbol",
                false => "structure",
            };
            let mut followed = 0;
            for e in g.edges(node, dir)? {
                let l = g.labels(&e);
                if l.layer != layer
                    || !q.relations.admits(Relations::of_kind(l.kind))
                    || e.confidence < min_q
                    || (!q.include_tests && g.is_test(g.file_of(e.node)?))
                {
                    continue;
                }
                if followed == FANOUT {
                    s.caps.fanout += 1;
                    break;
                }
                followed += 1;
                if !seen.insert(e.node) {
                    continue;
                }
                s.parent.insert(e.node, (node, e));
                if to.contains(&e.node) {
                    s.hit = Some(e.node);
                    return Ok(s);
                }
                if s.furthest.is_none_or(|(_, d)| d < depth) {
                    s.furthest = Some((e.node, depth));
                }
                if seen.len() >= VISITED {
                    s.caps.visited = true;
                    return Ok(s);
                }
                if next.len() == LEVEL_ROWS {
                    s.caps.rows_per_level = true;
                    continue;
                }
                next.push(e.node);
            }
        }
        if next.is_empty() {
            return Ok(s);
        }
        frontier = next;
    }
    s.caps.depth = true;
    Ok(s)
}

fn end<G: Graph + ?Sized>(g: &G, node: u32) -> Result<End, String> {
    let file = g.file_of(node)?;
    let sym = g.symbol(node)?;
    Ok(End {
        name: sym.as_ref().map(|s| s.name.clone()),
        kind: sym.as_ref().map_or("File".into(), |s| s.kind.clone()),
        path: g.path(file).to_string(),
        line: sym.map(|s| s.line),
    })
}

/// Trace `q` on `g`.
pub fn run<G: Graph + ?Sized>(g: &G, q: &TraceQuery) -> Result<Trace, String> {
    let from: Vec<u32> = impact::seed_nodes(g, &g.lookup(&q.from)?, Direction::Downstream)?;
    let to: BTreeSet<u32> = impact::seed_nodes(g, &g.lookup(&q.to)?, Direction::Upstream)?
        .into_iter()
        .collect();
    let mut out = Trace {
        from: q.from.clone(),
        to: q.to.clone(),
        found: false,
        length: 0,
        path: Vec::new(),
        direction: None,
        hops: Vec::new(),
        furthest: None,
        truncated: false,
        caps_hit: CapHits::default(),
        caps: Caps {
            max_depth: q.max_depth,
            fanout: FANOUT,
            rows_per_level: LEVEL_ROWS,
            visited: VISITED,
        },
        from_targets: from.len(),
        to_targets: to.len(),
        generated_at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Secs, true),
    };
    if from.is_empty() || to.is_empty() {
        return Ok(out);
    }
    let down = search(g, q, Direction::Downstream, &from, &to)?;
    // caps_hit names the caps of every search that ran
    let (found, dir) = match down.hit {
        Some(_) => (down, Direction::Downstream),
        None => {
            let mut up = search(g, q, Direction::Upstream, &from, &to)?;
            up.caps = down.caps.merge(up.caps);
            match up.hit {
                Some(_) => (up, Direction::Upstream),
                None => {
                    out.caps_hit = up.caps;
                    out.truncated = up.caps.any();
                    out.furthest = down
                        .furthest
                        .map(|(n, depth)| end(g, n).map(|end| Furthest { end, depth }))
                        .transpose()?;
                    return Ok(out);
                }
            }
        }
    };
    let Some(hit) = found.hit else {
        return Ok(out);
    };
    // walk back from the hit to a start node
    let mut chain = vec![hit];
    let mut edges = Vec::new();
    let mut at = hit;
    while let Some(&(p, e)) = found.parent.get(&at) {
        edges.push(e);
        chain.push(p);
        at = p;
    }
    chain.reverse();
    edges.reverse();
    for (pair, e) in chain.windows(2).zip(edges) {
        let l = g.labels(&e);
        out.hops.push(Hop {
            from: end(g, pair[0])?,
            to: end(g, pair[1])?,
            edge: l.kind.to_string(),
            confidence: (csr::confidence_of(e.confidence) * 1000.0).round() / 1000.0,
            provenance: l.provenance.to_string(),
            reason: l.reason.to_string(),
            access: (!l.access.is_empty()).then(|| l.access.to_string()),
        });
    }
    for n in &chain {
        let p = g.path(g.file_of(*n)?).to_string();
        if out.path.last() != Some(&p) {
            out.path.push(p);
        }
    }
    out.found = true;
    out.length = out.hops.len();
    out.direction = Some(dir);
    out.caps_hit = found.caps;
    Ok(out)
}
