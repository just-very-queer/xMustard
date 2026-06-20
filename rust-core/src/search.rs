//! Hybrid repo search (knowledge layer): four retrieval lanes — lexical
//! (BM25-style idf over symbol-name subtokens), semantic (a model-free
//! hashing-trick embedding with char-trigram fuzziness), structural (inbound
//! reference weight from the symbol graph), and proximity (graph distance to an
//! optional seed symbol) — fused with Reciprocal Rank Fusion. This is the
//! in-process retrieval floor; the Postgres tsvector tables in the Go foundation
//! are the scale path and reuse the same RRF idea.

use chrono::{SecondsFormat, Utc};
use serde::Serialize;
use std::collections::{HashMap, HashSet};
use std::path::Path;

use crate::symbolgraph;

fn now() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

fn camel_split(word: &str, out: &mut Vec<String>) {
    let chars: Vec<char> = word.chars().collect();
    let mut start = 0usize;
    for i in 1..chars.len() {
        // boundary at lower/digit -> Upper (camelCase / PascalCase)
        if chars[i].is_uppercase() && !chars[i - 1].is_uppercase() {
            let piece: String = chars[start..i].iter().collect();
            if piece.len() >= 2 {
                out.push(piece.to_lowercase());
            }
            start = i;
        }
    }
    let tail: String = chars[start..].iter().collect();
    if tail.len() >= 2 {
        out.push(tail.to_lowercase());
    }
}

fn tokens(text: &str) -> Vec<String> {
    // split identifiers into subtokens (snake_case, camelCase, paths) so a query
    // like "dashboard" matches "widget_factory" and "BuildWorkspaceDashboard".
    let mut out = Vec::new();
    for word in text.split(|c: char| !c.is_alphanumeric()) {
        if word.len() < 2 {
            continue;
        }
        out.push(word.to_lowercase());
        camel_split(word, &mut out);
    }
    out
}

const EMBED_DIM: usize = 256;

fn fnv1a(s: &str) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in s.bytes() {
        h ^= b as u64;
        h = h.wrapping_mul(0x0000_0100_0000_01b3);
    }
    h
}

/// Deterministic lexical "hashing-trick" embedding: tokens plus char trigrams are
/// feature-hashed (with sign hashing) into a fixed-dim, L2-normalized vector. This
/// is not a neural embedding — it is a model-free, dependency-free vector lane that
/// captures fuzzy sub-token overlap (so "dashbord" lands near "dashboard"), giving
/// RRF fusion a third signal distinct from exact-token BM25 and structural weight.
fn embed(text: &str) -> Vec<f32> {
    let mut v = vec![0f32; EMBED_DIM];
    let mut add = |feat: &str, w: f32| {
        let h = fnv1a(feat);
        let idx = (h % EMBED_DIM as u64) as usize;
        let sign = if (h >> 33) & 1 == 0 { 1.0 } else { -1.0 };
        v[idx] += w * sign;
    };
    for t in tokens(text) {
        add(&t, 1.0);
        let padded = format!("^{t}$");
        let chars: Vec<char> = padded.chars().collect();
        for w in chars.windows(3) {
            let tri: String = w.iter().collect();
            add(&tri, 0.5);
        }
    }
    let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
    if norm > 0.0 {
        for x in &mut v {
            *x /= norm;
        }
    }
    v
}

/// Cosine similarity of two already-L2-normalized vectors (just the dot product).
fn cosine(a: &[f32], b: &[f32]) -> f32 {
    a.iter().zip(b).map(|(x, y)| x * y).sum()
}

#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct SearchHit {
    pub kind: String, // "symbol" | "file"
    pub name: String,
    pub path: String,
    pub line: Option<usize>, // 1-based line of a symbol hit, so the agent jumps to the slice
    pub score: f64,
    pub reason: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct SearchResult {
    pub workspace_id: String,
    pub query: String,
    pub total: usize,
    pub hits: Vec<SearchHit>,
    pub generated_at: String,
}

/// Distance to the seed (BFS depth over the reference graph) collapses to a
/// proximity weight: the seed's own file scores 1.0, its direct dependents 0.5,
/// and so on. `1/(depth+1)` so the lane stays bounded and decays smoothly.
const PROXIMITY_DEPTH: usize = 3;

/// Hybrid lexical + structural + proximity search over the repo's symbol graph.
///
/// `seed` is an optional anchor symbol: when set (or auto-derived from an exact
/// query→symbol match), graph distance from the seed becomes a 4th RRF lane that
/// pulls structurally-nearby symbols up. It only re-ranks the already-matched
/// pool — proximity never widens recall on its own.
pub fn hybrid_search(
    root: &Path,
    workspace_id: &str,
    query: &str,
    limit: usize,
    seed: Option<&str>,
) -> SearchResult {
    let graph = symbolgraph::build_symbol_graph_cached(root, workspace_id);
    let hotspots: HashSet<String> = symbolgraph::compute_hotspots(&graph, 30)
        .into_iter()
        .map(|h| h.path)
        .collect();

    let mut qtokens: Vec<String> = tokens(query);
    qtokens.sort();
    qtokens.dedup();
    if qtokens.is_empty() {
        return SearchResult {
            workspace_id: workspace_id.to_string(),
            query: query.to_string(),
            total: 0,
            hits: Vec::new(),
            generated_at: now(),
        };
    }
    let query_lc = query.trim().to_lowercase();

    // document frequency of each query token across symbol-name tokens (idf).
    let n = graph.symbols.len().max(1) as f64;
    let mut df: HashMap<String, usize> = HashMap::new();
    let symbol_token_sets: Vec<HashSet<String>> = graph
        .symbols
        .iter()
        .map(|s| tokens(&s.name).into_iter().collect::<HashSet<String>>())
        .collect();
    for set in &symbol_token_sets {
        for t in &qtokens {
            if set.contains(t) {
                *df.entry(t.clone()).or_insert(0) += 1;
            }
        }
    }
    let idf = |t: &str| -> f64 {
        let d = *df.get(t).unwrap_or(&0) as f64;
        ((n + 1.0) / (d + 1.0)).ln() + 1.0
    };

    // Query embedding (semantic lane) and full inbound weight (structural lane).
    let q_emb = embed(query);
    let mut inbound: HashMap<String, usize> = HashMap::new();
    for e in &graph.edges {
        *inbound.entry(e.to_path.clone()).or_insert(0) += e.weight;
    }

    // A retrieval candidate carries the raw per-lane signals; RRF fuses them below.
    struct Cand {
        kind: &'static str,
        name: String,
        path: String,
        line: Option<usize>,
        lexical: f64,
        matched: Vec<String>,
        exact: bool,
        hotspot: bool,
        emb: f32,
        structural: usize,
        proximity: f64, // 1/(graph-distance+1) to the seed; 0.0 when no seed/unreachable
    }
    const EMB_GATE: f32 = 0.30; // a pure-semantic hit must clear this to enter the pool

    let mut cands: Vec<Cand> = Vec::new();
    for (i, sym) in graph.symbols.iter().enumerate() {
        let name_tokens = &symbol_token_sets[i];
        let path_tokens: HashSet<String> = tokens(&sym.path).into_iter().collect();
        let mut lexical = 0.0;
        let mut matched = Vec::new();
        for t in &qtokens {
            if name_tokens.contains(t) {
                lexical += idf(t);
                matched.push(t.clone());
            } else if path_tokens.contains(t) {
                lexical += 0.3 * idf(t);
            }
        }
        // semantic lane uses the symbol NAME only — path tokens dilute the signal.
        let emb = cosine(&q_emb, &embed(&sym.name));
        if lexical <= 0.0 && emb < EMB_GATE {
            continue;
        }
        cands.push(Cand {
            kind: "symbol",
            name: sym.name.clone(),
            path: sym.path.clone(),
            line: sym.line_start,
            lexical,
            matched,
            exact: sym.name.to_lowercase() == query_lc,
            hotspot: hotspots.contains(&sym.path),
            emb,
            structural: *inbound.get(&sym.path).unwrap_or(&0),
            proximity: 0.0,
        });
    }
    for f in &graph.files {
        let path_tokens: HashSet<String> = tokens(&f.path).into_iter().collect();
        let mut lexical = 0.0;
        for t in &qtokens {
            if path_tokens.contains(t) {
                lexical += 0.4 * idf(t);
            }
        }
        let emb = cosine(&q_emb, &embed(&f.path));
        if lexical <= 0.0 && emb < EMB_GATE {
            continue;
        }
        cands.push(Cand {
            kind: "file",
            name: f.path.clone(),
            path: f.path.clone(),
            line: None,
            lexical,
            matched: Vec::new(),
            exact: false,
            hotspot: hotspots.contains(&f.path),
            emb,
            structural: *inbound.get(&f.path).unwrap_or(&0),
            proximity: 0.0,
        });
    }

    // Proximity lane: resolve the seed (explicit, else auto-seed from the top
    // exact query→symbol match), BFS its blast radius over the reference graph,
    // and fold distance→weight onto each candidate's defining file.
    let effective_seed: Option<String> = match seed {
        Some(s) if !s.trim().is_empty() => Some(s.trim().to_string()),
        _ => cands.iter().find(|c| c.exact).map(|c| c.name.clone()),
    };
    if let Some(seed_name) = &effective_seed {
        let impact = symbolgraph::symbol_impact(&graph, seed_name, PROXIMITY_DEPTH);
        let mut prox: HashMap<String, f64> = HashMap::new();
        for d in &impact.defined_in {
            prox.insert(d.clone(), 1.0); // the seed's own file: distance 0
        }
        for f in &impact.impacted {
            prox.entry(f.path.clone()).or_insert(1.0 / (f.distance as f64 + 1.0));
        }
        if !prox.is_empty() {
            for c in &mut cands {
                if let Some(&p) = prox.get(&c.path) {
                    c.proximity = p;
                }
            }
        }
    }

    // Reciprocal Rank Fusion across up to four lanes. Each lane ranks the pool by
    // its own signal; a candidate's fused score sums 1/(k + rank) over the lanes it
    // appears in, so agreement across diverse signals beats one loud signal.
    let rank_by = |key: &dyn Fn(&Cand) -> f64| -> Vec<usize> {
        let mut idxs: Vec<usize> = (0..cands.len()).filter(|&i| key(&cands[i]) > 0.0).collect();
        idxs.sort_by(|&a, &b| {
            key(&cands[b])
                .partial_cmp(&key(&cands[a]))
                .unwrap_or(std::cmp::Ordering::Equal)
                .then(cands[a].name.cmp(&cands[b].name))
        });
        idxs
    };
    let lex_rank = rank_by(&|c| c.lexical);
    let emb_rank = rank_by(&|c| c.emb as f64);
    let str_rank = rank_by(&|c| c.structural as f64);
    let prox_rank = rank_by(&|c| c.proximity);

    let k = 60.0f64;
    let mut rrf = vec![0.0f64; cands.len()];
    let mut lanes = vec![Vec::<&'static str>::new(); cands.len()];
    for (label, ranking) in [
        ("lexical", &lex_rank),
        ("semantic", &emb_rank),
        ("structural", &str_rank),
        ("proximity", &prox_rank),
    ] {
        for (rank, &i) in ranking.iter().enumerate() {
            rrf[i] += 1.0 / (k + rank as f64 + 1.0);
            lanes[i].push(label);
        }
    }

    let mut hits: Vec<SearchHit> = cands
        .iter()
        .enumerate()
        .map(|(i, c)| {
            let mut score = rrf[i];
            if c.exact {
                score += 1.0; // an exact name match dominates the fused ranking
            }
            let reason = if c.exact {
                "exact symbol match".to_string()
            } else {
                let base = if !c.matched.is_empty() {
                    format!("matched {}", c.matched.join("+"))
                } else if c.kind == "file" {
                    "path match".to_string()
                } else {
                    "semantic match".to_string()
                };
                let mut r = format!("{base} · {}", lanes[i].join("+"));
                if c.hotspot {
                    r.push_str(" · hotspot");
                }
                r
            };
            SearchHit {
                kind: c.kind.to_string(),
                name: c.name.clone(),
                path: c.path.clone(),
                line: c.line,
                score,
                reason,
            }
        })
        .collect();

    hits.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.name.cmp(&b.name))
    });
    let total = hits.len();
    hits.truncate(limit);
    SearchResult {
        workspace_id: workspace_id.to_string(),
        query: query.to_string(),
        total,
        hits,
        generated_at: now(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Command;
    use tempfile::TempDir;

    fn git_repo(files: &[(&str, &str)]) -> TempDir {
        let dir = TempDir::new().unwrap();
        for (rel, content) in files {
            std::fs::write(dir.path().join(rel), content).unwrap();
        }
        for args in [
            vec!["init", "-q"],
            vec!["config", "user.email", "t@t"],
            vec!["config", "user.name", "t"],
        ] {
            Command::new("git").arg("-C").arg(dir.path()).args(&args).output().unwrap();
        }
        Command::new("git").arg("-C").arg(dir.path()).args(["add", "-A"]).output().unwrap();
        Command::new("git").arg("-C").arg(dir.path()).args(["commit", "-qm", "c"]).output().unwrap();
        dir
    }

    #[test]
    fn search_ranks_matching_symbol_first() {
        let repo = git_repo(&[
            ("a.rs", "pub fn compute_widget_total() {}\npub fn unrelated_thing() {}\n"),
            ("b.rs", "pub fn other_helper() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "compute_widget_total", 10, None);
        assert!(res.total >= 1);
        assert_eq!(res.hits[0].name, "compute_widget_total");
        assert_eq!(res.hits[0].reason, "exact symbol match");
        // a symbol hit carries its line so the agent jumps to the slice, not a dump.
        assert_eq!(res.hits[0].line, Some(1));
    }

    #[test]
    fn search_token_match_and_empty_query() {
        let repo = git_repo(&[("a.rs", "pub fn widget_factory() {}\n")]);
        let res = hybrid_search(repo.path(), "ws", "widget", 10, None);
        assert!(res.hits.iter().any(|h| h.name == "widget_factory"));
        let empty = hybrid_search(repo.path(), "ws", "   ", 10, None);
        assert_eq!(empty.total, 0);
    }

    #[test]
    fn semantic_lane_recovers_fuzzy_token() {
        // "dashbord" (misspelled, no exact token) should still surface
        // render_dashboard via the char-trigram embedding lane under RRF.
        let repo = git_repo(&[
            ("ui.rs", "pub fn render_dashboard() {}\npub fn save_invoice() {}\n"),
            ("b.rs", "pub fn unrelated_widget() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "dashbord", 10, None);
        assert!(
            res.hits.iter().any(|h| h.name == "render_dashboard"),
            "semantic lane should recover the fuzzy match: {:?}",
            res.hits
        );
    }

    #[test]
    fn rrf_reason_notes_fused_lanes() {
        let repo = git_repo(&[("a.rs", "pub fn compute_widget() {}\n")]);
        let res = hybrid_search(repo.path(), "ws", "widget", 10, None);
        let hit = res.hits.iter().find(|h| h.name == "compute_widget").unwrap();
        assert!(hit.reason.contains("lexical") || hit.reason.contains("semantic"), "{}", hit.reason);
    }

    #[test]
    fn proximity_lane_reranks_graph_neighbour_up() {
        // Two symbols both match the query "token". Without a seed, the
        // alphabetically-earlier far symbol wins the name tiebreak. With the seed
        // `resolve_token`, the near symbol (in a file that calls the seed → graph
        // distance 1) is pulled above the far one by the proximity lane.
        let repo = git_repo(&[
            ("auth.rs", "pub fn resolve_token() {}\n"),
            ("near.rs", "pub fn zzz_token_near() { resolve_token(); }\n"),
            ("far.rs", "pub fn aaa_token_far() {}\n"),
        ]);
        let pos = |res: &SearchResult, name: &str| {
            res.hits.iter().position(|h| h.name == name)
        };

        let plain = hybrid_search(repo.path(), "ws", "token", 10, None);
        let near_plain = pos(&plain, "zzz_token_near").expect("near present");
        let far_plain = pos(&plain, "aaa_token_far").expect("far present");
        // baseline: with no seed, the far symbol is not below the near one.
        assert!(far_plain < near_plain, "baseline order: {:?}", plain.hits);

        let seeded = hybrid_search(repo.path(), "ws", "token", 10, Some("resolve_token"));
        let near_seeded = pos(&seeded, "zzz_token_near").expect("near present");
        let far_seeded = pos(&seeded, "aaa_token_far").expect("far present");
        assert!(
            near_seeded < far_seeded,
            "seed should re-rank the graph-near symbol above the far one: {:?}",
            seeded.hits
        );
        let near_hit = &seeded.hits[near_seeded];
        assert!(
            near_hit.reason.contains("proximity"),
            "near hit should credit the proximity lane: {}",
            near_hit.reason
        );
    }

    #[test]
    fn auto_seed_from_exact_match_activates_proximity() {
        // No explicit seed, but the query "token" exactly names a symbol → that
        // symbol auto-seeds the proximity lane. A caller that also matches "token"
        // (so it's in the pool) and calls the seed (graph distance 1) is credited
        // by proximity; an unrelated "token" symbol is not.
        let repo = git_repo(&[
            ("auth.rs", "pub fn token() {}\n"),
            ("caller.rs", "pub fn token_handler() { token(); }\n"),
            ("far.rs", "pub fn token_orphan() {}\n"),
        ]);
        let res = hybrid_search(repo.path(), "ws", "token", 10, None);
        let handler = res.hits.iter().find(|h| h.name == "token_handler");
        assert!(
            handler.map(|h| h.reason.contains("proximity")).unwrap_or(false),
            "exact-match auto-seed should credit the caller via proximity: {:?}",
            res.hits
        );
        let orphan = res.hits.iter().find(|h| h.name == "token_orphan").unwrap();
        assert!(
            !orphan.reason.contains("proximity"),
            "an unrelated symbol must not get a proximity credit: {}",
            orphan.reason
        );
    }

    #[test]
    fn embed_is_normalized_and_fuzzy() {
        let a = embed("dashboard");
        let norm: f32 = a.iter().map(|x| x * x).sum::<f32>().sqrt();
        assert!((norm - 1.0).abs() < 1e-4, "embedding must be L2-normalized, got {norm}");
        // a near-miss spelling should be closer than an unrelated token
        let near = cosine(&embed("dashboard"), &embed("dashbord"));
        let far = cosine(&embed("dashboard"), &embed("invoice"));
        assert!(near > far, "fuzzy near={near} should beat far={far}");
    }
}
