//! WS-14 contracts of the index query side: the graph segment equals the edges table,
//! queries on it equal the legacy graph algorithms on the same edges, snapshot swaps
//! never tear a read, results carry freshness and honest coverage, and the resident
//! service stays within its memory line on a 100k-symbol resolved graph.
//!
//! Set `XMUSTARD_PARITY_FIXTURES` to a directory holding `cline` and `pi-mono` clones
//! (Apache-2.0 / MIT) to run the legacy-equality check on them too; the indexes are
//! written to a temporary directory, never into the fixtures.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::io::{BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rusqlite::{Connection, OpenFlags, params};
use serde_json::{Value, json};
use tempfile::TempDir;
use xmustard_core::index::config::IndexConfig;
use xmustard_core::index::csr::{
    self, Edge, FILE_IN, FILE_OUT, FileEdge, GraphStorage, IN, OUT, Segment,
};
use xmustard_core::index::{self, meta, reader, schema};
use xmustard_core::serve::{read_header, write_frame};
use xmustard_core::symbolgraph::{
    self as sg, GraphEdge, GraphFileNode, GraphSymbolNode, QueryGraph, SymbolGraph,
};

mod memprobe;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");
const INDEXED: &str = "'tree_sitter','regex','none'";

fn git(root: &Path, args: &[&str]) -> String {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

fn write(root: &Path, rel: &str, content: &str) {
    let p = root.join(rel);
    fs::create_dir_all(p.parent().unwrap()).unwrap();
    fs::write(p, content).unwrap();
}

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    commit(dir.path(), "c");
    dir
}

fn commit(root: &Path, msg: &str) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", msg]);
}

/// Several languages, imports, calls, inheritance, a test file and an isolated file.
fn fixture() -> TempDir {
    repo(&[
        (
            "web/src/total.ts",
            "import { helperValue } from './helper';\nexport function computeTotal(x: number): number { return helperValue(x) + 1; }\nexport class BaseWidget { render(): number { return computeTotal(1); } }\n",
        ),
        (
            "web/src/helper.ts",
            "export function helperValue(x: number): number { return x * 2; }\nexport const unusedThing = 3;\n",
        ),
        (
            "web/src/view.ts",
            "import { BaseWidget } from './total';\nexport class FancyWidget extends BaseWidget { draw(): number { return this.render(); } }\n",
        ),
        (
            "web/src/handler.ts",
            "import { computeTotal } from './total';\nexport function handleRequest(): number { return computeTotal(2); }\n",
        ),
        (
            "web/src/total.test.ts",
            "import { computeTotal } from './total';\nexport function checkTotal(): boolean { return computeTotal(1) === 3; }\n",
        ),
        (
            "svc/engine.go",
            "package svc\n\nfunc EngineStart() int { return EngineHelper(1) }\n\nfunc EngineHelper(a int) int { return a + 1 }\n",
        ),
        (
            "svc/server.go",
            "package svc\n\nfunc ServeRequests() int { return EngineStart() }\n",
        ),
        (
            "core/src/lib.rs",
            "pub fn parse_input(s: &str) -> usize { s.len() }\npub struct InputParser {}\n",
        ),
        (
            "core/src/main.rs",
            "fn run_parser() -> usize { parse_input(\"x\") }\n",
        ),
        (
            "lonely/alone.go",
            "package lonely\n\nfunc LonelyFunction() {}\n",
        ),
    ])
}

fn cfg_at(dir: &Path) -> IndexConfig {
    IndexConfig {
        index_dir: Some(dir.to_path_buf()),
        ..IndexConfig::default()
    }
}

/// Build the index of `root` into a fresh temporary directory; returns the store path.
fn build(root: &Path, dir: &Path) -> PathBuf {
    let r = index::build(root, &cfg_at(dir)).unwrap();
    PathBuf::from(r.index_path)
}

fn ro(db: &Path) -> Connection {
    Connection::open_with_flags(db, OpenFlags::SQLITE_OPEN_READ_ONLY).unwrap()
}

// ---------------------------------------------------------------------------
// CSR equals the edges table
// ---------------------------------------------------------------------------

type EdgeKey = (u32, u32, String, String, u32, String, u8);

/// Every edge of the store, keyed by segment node ids (files by path rank, symbols by
/// id rank after the files), from SQL alone.
fn db_edges(conn: &Connection) -> Vec<EdgeKey> {
    let files: Vec<i64> = conn
        .prepare(&format!(
            "SELECT id FROM files WHERE parse_status IN ({INDEXED}) ORDER BY path"
        ))
        .unwrap()
        .query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let file_node: BTreeMap<i64, u32> = files
        .iter()
        .enumerate()
        .map(|(i, id)| (*id, i as u32))
        .collect();
    let syms: Vec<i64> = conn
        .prepare(&format!(
            "SELECT s.id FROM symbols s JOIN files f ON f.id = s.file_id
             WHERE f.parse_status IN ({INDEXED}) ORDER BY s.id"
        ))
        .unwrap()
        .query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let sym_node: BTreeMap<i64, u32> = syms
        .iter()
        .enumerate()
        .map(|(i, id)| (*id, files.len() as u32 + i as u32))
        .collect();
    let node = |f: i64, s: Option<i64>| match s {
        Some(s) => sym_node[&s],
        None => file_node[&f],
    };
    let mut st = conn
        .prepare(
            "SELECT src_file, src_symbol, dst_file, dst_symbol, kind, layer, weight, provenance, confidence FROM edges",
        )
        .unwrap();
    let mut out: Vec<EdgeKey> = st
        .query_map([], |r| {
            Ok((
                node(r.get(0)?, r.get(1)?),
                node(r.get(2)?, r.get(3)?),
                r.get::<_, String>(4)?,
                r.get::<_, String>(5)?,
                r.get::<_, i64>(6)? as u32,
                r.get::<_, String>(7)?,
                csr::quantize_confidence(r.get(8)?),
            ))
        })
        .unwrap()
        .map(Result::unwrap)
        .collect();
    out.sort();
    out
}

fn seg_edges(seg: &Segment, forward: bool) -> Vec<EdgeKey> {
    let adj = if forward { OUT } else { IN };
    let mut out = Vec::new();
    for (n, edges) in csr::all_edges(seg, adj).unwrap() {
        for e in edges {
            let (a, b) = if forward { (n, e.node) } else { (e.node, n) };
            out.push(key(seg, a, b, &e));
        }
    }
    out.sort();
    out
}

fn key(seg: &Segment, a: u32, b: u32, e: &Edge) -> EdgeKey {
    (
        a,
        b,
        seg.edge_kind(e.kind).to_string(),
        seg.layer(e.layer).to_string(),
        e.weight,
        seg.provenance(e.provenance).to_string(),
        e.confidence,
    )
}

#[test]
fn csr_equals_the_edges_table_and_reverse_edges_mirror_it() {
    let r = fixture();
    let dir = TempDir::new().unwrap();
    let db = build(r.path(), dir.path());
    // add typed symbol-level edges (what the resolver will write) next to the lexical
    // ones, so both node kinds are exercised
    {
        let conn = Connection::open(&db).unwrap();
        let syms: Vec<(i64, i64)> = conn
            .prepare("SELECT id, file_id FROM symbols ORDER BY id")
            .unwrap()
            .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))
            .unwrap()
            .map(Result::unwrap)
            .collect();
        assert!(syms.len() > 10, "fixture has symbols");
        for (i, (s, f)) in syms.iter().enumerate() {
            for k in 1..=5 {
                let (t, tf) = syms[(i * 7 + k * 3) % syms.len()];
                conn.execute(
                    "INSERT INTO edges(src_file, dst_file, src_symbol, dst_symbol, kind, layer, weight,
                     confidence, provenance, via, reason, access)
                     VALUES (?1, ?2, ?3, ?4, ?5, 'structure', ?6, 0.93, 'resolved', '[]', 'test', NULL)",
                    params![f, tf, s, t, ["calls", "accesses", "has_method"][k % 3], k as i64],
                )
                .unwrap();
            }
        }
        conn.execute(
            "UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'generation'",
            [],
        )
        .unwrap();
    }
    index::ensure_graph_segment(&db).unwrap();
    let conn = ro(&db);
    for storage in [GraphStorage::Mem, GraphStorage::File] {
        let (seg, m) = reader::load_segment(&db, storage).unwrap();
        assert_eq!(seg.footer.generation, m.generation);
        let want = db_edges(&conn);
        assert!(want.len() > 50);
        assert_eq!(seg_edges(&seg, true), want, "forward CSR = edges table");
        assert_eq!(seg_edges(&seg, false), want, "reverse CSR mirrors it");
        assert_eq!(seg.counts().edges as usize, want.len());

        // the file projection: structure layer, self pairs dropped, weights summed
        let mut proj: BTreeMap<(u32, u32), u32> = BTreeMap::new();
        let nf = seg.counts().files;
        let file_of = |n: u32| {
            if n < nf {
                n
            } else {
                seg.symbol(n - nf).unwrap().file
            }
        };
        for (a, b, _, layer, w, _, _) in &want {
            let (fa, fb) = (file_of(*a), file_of(*b));
            if layer == "structure" && fa != fb {
                *proj.entry((fa, fb)).or_default() += w;
            }
        }
        let mut fwd = BTreeMap::new();
        let mut rev = BTreeMap::new();
        for f in 0..nf {
            for e in seg.neighbours::<FileEdge>(FILE_OUT, f).unwrap() {
                fwd.insert((f, e.file), e.weight);
            }
            for e in seg.neighbours::<FileEdge>(FILE_IN, f).unwrap() {
                rev.insert((e.file, f), e.weight);
            }
        }
        assert_eq!(fwd, proj);
        assert_eq!(rev, proj);

        // name index: every non-local symbol is found by its name
        let names: Vec<(String, String)> = conn
            .prepare(&format!(
                "SELECT n.name, f.path FROM symbols s JOIN names n ON n.id = s.name_id
                 JOIN files f ON f.id = s.file_id WHERE f.parse_status IN ({INDEXED})"
            ))
            .unwrap()
            .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))
            .unwrap()
            .map(Result::unwrap)
            .collect();
        for (name, path) in names {
            let n = seg.name_id(&name).unwrap().expect("name interned");
            let files: BTreeSet<String> = seg
                .symbols_named(n)
                .unwrap()
                .into_iter()
                .map(|s| seg.file_path(seg.symbol(s).unwrap().file).unwrap())
                .collect();
            assert!(files.contains(&path), "{name} in {path}");
            assert_eq!(
                seg.file_id(&path)
                    .unwrap()
                    .map(|f| seg.file_path(f).unwrap()),
                Some(path)
            );
        }
        assert_eq!(seg.name_id("no_such_name_anywhere").unwrap(), None);
    }
}

// ---------------------------------------------------------------------------
// Queries equal the legacy algorithms on the same edges
// ---------------------------------------------------------------------------

/// The legacy graph over the store's own rows: indexed files, non-local symbols and
/// the structure-layer edges.
fn legacy_graph(conn: &Connection) -> SymbolGraph {
    let files: Vec<GraphFileNode> = conn
        .prepare(&format!(
            "SELECT path FROM files WHERE parse_status IN ({INDEXED}) ORDER BY path"
        ))
        .unwrap()
        .query_map([], |r| {
            Ok(GraphFileNode {
                path: r.get(0)?,
                symbol_count: 0,
                authority: 0,
            })
        })
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let symbols: Vec<GraphSymbolNode> = conn
        .prepare(&format!(
            "SELECT n.name, s.kind, f.path, s.name_line FROM symbols s JOIN names n ON n.id = s.name_id
             JOIN files f ON f.id = s.file_id WHERE s.local = 0 AND f.parse_status IN ({INDEXED})
             ORDER BY f.path, s.ord"
        ))
        .unwrap()
        .query_map([], |r| {
            Ok(GraphSymbolNode {
                name: r.get(0)?,
                kind: r.get(1)?,
                path: r.get(2)?,
                line_start: Some(r.get::<_, i64>(3)? as usize),
            })
        })
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let edges: Vec<GraphEdge> = conn
        .prepare(
            "SELECT a.path, b.path, e.kind, e.weight FROM edges e JOIN files a ON a.id = e.src_file
             JOIN files b ON b.id = e.dst_file WHERE e.layer = 'structure'",
        )
        .unwrap()
        .query_map([], |r| {
            Ok(GraphEdge {
                from_path: r.get(0)?,
                to_path: r.get(1)?,
                kind: r.get(2)?,
                weight: r.get::<_, i64>(3)? as usize,
                via_symbols: Vec::new(),
                resolution: "lexical".into(),
            })
        })
        .unwrap()
        .map(Result::unwrap)
        .collect();
    SymbolGraph {
        workspace_id: "ws".into(),
        file_count: files.len(),
        symbol_count: symbols.len(),
        edge_count: edges.len(),
        files,
        symbols,
        edges,
        flow_edges: Vec::new(),
        flow_edge_count: 0,
        coverage: Default::default(),
        generated_at: String::new(),
    }
}

fn plain<T: serde::Serialize>(v: &T) -> Value {
    let mut v = serde_json::to_value(v).unwrap();
    if let Value::Object(m) = &mut v {
        m.remove("generated_at");
    }
    v
}

/// Compare impact, trace, hotspots and clusters of the index snapshot with the legacy
/// functions over the same store. `max_names` bounds the symbols checked.
fn assert_legacy_equal(root: &Path, db: &Path, max_names: usize) -> usize {
    let conn = ro(db);
    let legacy = legacy_graph(&conn);
    let opened = reader::open_at(root, db, GraphStorage::File).unwrap();
    let snap = opened.snapshot.as_ref();
    let mut names: Vec<String> = legacy.symbols.iter().map(|s| s.name.clone()).collect();
    names.sort();
    names.dedup();
    // spread the sample over the sorted names
    let step = (names.len() / max_names.max(1)).max(1);
    let sample: Vec<&String> = names.iter().step_by(step).take(max_names).collect();
    for name in &sample {
        for depth in [1, 2, 4] {
            assert_eq!(
                plain(&snap.impact(name, depth).unwrap()),
                plain(&sg::symbol_impact(&legacy, name, depth)),
                "impact {name} depth {depth}"
            );
        }
    }
    for pair in sample.windows(2).take(60) {
        assert_eq!(
            plain(&snap.trace(pair[0], pair[1]).unwrap()),
            plain(&sg::trace_symbols(&legacy, pair[0], pair[1])),
            "trace {} -> {}",
            pair[0],
            pair[1]
        );
    }
    assert_eq!(
        plain(&snap.hotspots(50).unwrap()),
        plain(&sg::compute_hotspots(&legacy, 50))
    );
    assert_eq!(
        plain(&snap.clusters().unwrap()),
        plain(&sg::compute_clusters(&legacy))
    );
    for c in sg::compute_clusters(&legacy).iter().take(20) {
        let got = snap.cluster_of(&c.files[0]).unwrap().unwrap();
        assert_eq!(plain(&got), plain(c));
    }
    // search's inputs: the same symbols and per-file inbound weight
    let mut from_snap = Vec::new();
    snap.for_each_symbol(&mut |s| {
        from_snap.push((
            s.name.to_string(),
            s.path.to_string(),
            s.line,
            s.file_inbound,
        ))
    })
    .unwrap();
    let mut from_legacy = Vec::new();
    legacy
        .for_each_symbol(&mut |s| {
            from_legacy.push((
                s.name.to_string(),
                s.path.to_string(),
                s.line,
                s.file_inbound,
            ))
        })
        .unwrap();
    from_snap.sort();
    from_legacy.sort();
    assert_eq!(from_snap, from_legacy);
    assert_eq!(snap.symbol_count(), legacy.symbol_count());
    sample.len()
}

#[test]
fn bfs_trace_and_blast_radius_equal_the_legacy_graph_on_the_same_edges() {
    let r = fixture();
    let dir = TempDir::new().unwrap();
    let db = build(r.path(), dir.path());
    let checked = assert_legacy_equal(r.path(), &db, 1000);
    assert!(checked > 10);
    // spot checks that the fixture exercises real paths, not empty answers
    let opened = reader::open_at(r.path(), &db, GraphStorage::File).unwrap();
    let impact = opened.snapshot.impact("helperValue", 4).unwrap();
    assert_eq!(impact.defined_in, vec!["web/src/helper.ts"]);
    let impacted: Vec<(&str, usize)> = impact
        .impacted
        .iter()
        .map(|i| (i.path.as_str(), i.distance))
        .collect();
    assert!(impacted.contains(&("web/src/total.ts", 1)), "{impacted:?}");
    assert!(
        impacted.contains(&("web/src/handler.ts", 2)),
        "{impacted:?}"
    );
    let trace = opened
        .snapshot
        .trace("ServeRequests", "EngineHelper")
        .unwrap();
    assert!(trace.found, "{trace:?}");
    assert_eq!(trace.path, vec!["svc/server.go", "svc/engine.go"]);
}

/// The same equality on the pinned Apache/MIT fixtures, when present.
#[test]
fn legacy_equality_holds_on_parity_fixtures() {
    let Some(base) = std::env::var_os("XMUSTARD_PARITY_FIXTURES").map(PathBuf::from) else {
        eprintln!("XMUSTARD_PARITY_FIXTURES unset; skipping the cline/pi-mono check");
        return;
    };
    for name in ["cline", "pi-mono"] {
        let root = base.join(name);
        if !root.join(".git").exists() {
            eprintln!("{} missing; skipped", root.display());
            continue;
        }
        let dir = TempDir::new().unwrap();
        let db = build(&root, dir.path());
        let n = assert_legacy_equal(&root, &db, 400);
        eprintln!("{name}: legacy-equal on {n} sampled symbols");
    }
}

// ---------------------------------------------------------------------------
// Snapshot swap under concurrent readers
// ---------------------------------------------------------------------------

#[test]
fn snapshot_swap_under_concurrent_readers_never_tears_a_read() {
    let r = fixture();
    let root = fs::canonicalize(r.path()).unwrap();
    // the default location (inside this repository's .git), as the service finds it
    index::build(&root, &IndexConfig::default()).unwrap();
    let snaps = Arc::new(reader::Snapshots::new(2, GraphStorage::File));
    let stop = Arc::new(AtomicBool::new(false));
    // generation -> files the store held when it committed that generation
    let expected: Arc<Mutex<BTreeMap<i64, u32>>> = Arc::default();
    let observed: Arc<Mutex<Vec<(i64, u32)>>> = Arc::default();
    let readers: Vec<_> = (0..4)
        .map(|_| {
            let (snaps, stop, observed, root) =
                (snaps.clone(), stop.clone(), observed.clone(), root.clone());
            std::thread::spawn(move || {
                while !stop.load(Ordering::Relaxed) {
                    let o = snaps.open(&root).expect("resident snapshot");
                    let s = o.snapshot.as_ref();
                    let seg = &s.seg;
                    assert_eq!(
                        seg.footer.generation, s.generation,
                        "one generation per snapshot"
                    );
                    // forward and reverse agree within the snapshot
                    let (mut out_w, mut in_w) = (0u64, 0u64);
                    for f in 0..seg.counts().files {
                        out_w += seg
                            .neighbours::<FileEdge>(FILE_OUT, f)
                            .unwrap()
                            .iter()
                            .map(|e| e.weight as u64)
                            .sum::<u64>();
                        in_w += seg
                            .neighbours::<FileEdge>(FILE_IN, f)
                            .unwrap()
                            .iter()
                            .map(|e| e.weight as u64)
                            .sum::<u64>();
                    }
                    assert_eq!(out_w, in_w, "torn projection");
                    // a query runs to completion on the snapshot it started with
                    let (_, offsets) = seg.path_heap().unwrap();
                    assert_eq!(offsets.len() as u32, seg.counts().files + 1);
                    let impact = s.impact("computeTotal", 4).unwrap();
                    let fresh = o.freshness(impact.impacted.iter().map(|i| i.path.as_str()));
                    assert_eq!(fresh.snapshot_generation, Some(s.generation));
                    observed
                        .lock()
                        .unwrap()
                        .push((s.generation, seg.counts().files));
                }
            })
        })
        .collect();
    let db_gen = |root: &Path| {
        let o = reader::open_at(root, &locate_db(root), GraphStorage::File).unwrap();
        (o.snapshot.generation, o.snapshot.seg.counts().files)
    };
    let (g0, f0) = db_gen(&root);
    expected.lock().unwrap().insert(g0, f0);
    for i in 0..6 {
        // each generation adds one file that calls into the previous one
        write(
            &root,
            &format!("web/src/gen{i}.ts"),
            &format!(
                "import {{ computeTotal }} from './total';\nexport function generation{i}(): number {{ return computeTotal({i}); }}\n"
            ),
        );
        commit(&root, &format!("gen {i}"));
        index::update(&root, &IndexConfig::default(), None).unwrap();
        let (g, f) = db_gen(&root);
        expected.lock().unwrap().insert(g, f);
        std::thread::sleep(Duration::from_millis(30));
    }
    std::thread::sleep(Duration::from_millis(100));
    stop.store(true, Ordering::Relaxed);
    for t in readers {
        t.join().expect("a reader panicked");
    }
    let expected = expected.lock().unwrap();
    let observed = observed.lock().unwrap();
    let gens: BTreeSet<i64> = observed.iter().map(|(g, _)| *g).collect();
    assert!(gens.len() >= 2, "readers saw swaps: {gens:?}");
    for (g, files) in observed.iter() {
        assert_eq!(expected.get(g), Some(files), "generation {g} read whole");
    }
    let stats = snaps.stats();
    assert!(stats["swaps"].as_u64().unwrap() >= 1, "{stats}");
}

fn locate_db(root: &Path) -> PathBuf {
    let root = fs::canonicalize(root).unwrap();
    let git_dir = PathBuf::from(git(&root, &["rev-parse", "--absolute-git-dir"]));
    index::index_dir(&root, Some(&git_dir), &IndexConfig::default()).join(index::DB_FILE)
}

// ---------------------------------------------------------------------------
// Freshness and coverage on the tool paths
// ---------------------------------------------------------------------------

fn run_json(args: &[&str]) -> Value {
    let out = Command::new(BIN).args(args).output().unwrap();
    assert!(
        out.status.success(),
        "{args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn tool_results(root: &str) -> Vec<(&'static str, Value)> {
    vec![
        (
            "search",
            run_json(&["search", root, "ws", "computeTotal", "10"]),
        ),
        (
            "impact",
            run_json(&["symbolgraph", "impact", root, "ws", "computeTotal", "4"]),
        ),
        (
            "trace",
            run_json(&[
                "symbolgraph",
                "trace",
                root,
                "ws",
                "handleRequest",
                "helperValue",
            ]),
        ),
        (
            "explain",
            run_json(&["symbolgraph", "cluster-of", root, "ws", "web/src/total.ts"]),
        ),
    ]
}

fn statuses(root: &str) -> Vec<(&'static str, Value)> {
    tool_results(root)
        .into_iter()
        .map(|(tool, v)| (tool, v["freshness"].clone()))
        .collect()
}

#[test]
fn freshness_envelope_on_search_explain_and_impact_reports_the_commit_relation() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    // no index yet: the legacy graph answers and says so
    for (tool, f) in statuses(root) {
        assert_eq!(f["source"], "legacy_graph", "{tool}: {f}");
        assert_eq!(f["status"], "current", "{tool}: {f}");
        assert!(f.get("snapshot_generation").is_none(), "{tool}: {f}");
    }
    run_json(&["index", "build", root]);
    let indexed = git(r.path(), &["rev-parse", "HEAD"]);
    for (tool, f) in statuses(root) {
        assert_eq!(f["source"], "index", "{tool}: {f}");
        assert_eq!(f["status"], "current", "{tool}: {f}");
        assert_eq!(f["indexed_commit"], indexed.as_str());
        assert_eq!(f["head"], indexed.as_str());
        assert_eq!(f["watcher_state"], "absent");
        assert!(
            f["snapshot_generation"].as_i64().unwrap() >= 1,
            "{tool}: {f}"
        );
        assert!(f["index_version"].as_str().unwrap().starts_with("schema-"));
        assert!(f["identity_age_ms"].is_u64());
    }

    // an edit to an indexed file touches the results that list it
    fs::OpenOptions::new()
        .append(true)
        .open(r.path().join("web/src/total.ts"))
        .and_then(|mut f| std::io::Write::write_all(&mut f, b"\nexport const extra = 1;\n"))
        .unwrap();
    for (tool, f) in statuses(root) {
        let dirty: Vec<&str> = f["dirty_paths_touching_result"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        assert!(dirty.contains(&"web/src/total.ts"), "{tool}: {f}");
    }

    // two commits past the index: behind by two
    commit(r.path(), "one");
    write(r.path(), "web/src/more.ts", "export const more = 1;\n");
    commit(r.path(), "two");
    for (tool, f) in statuses(root) {
        assert_eq!(f["status"], "behind", "{tool}: {f}");
        assert_eq!(f["behind_by"], 2, "{tool}: {f}");
    }

    // index the tip, then look from a branch that forked before it: diverged
    run_json(&["index", "update", root]);
    for (tool, f) in statuses(root) {
        assert_eq!(f["status"], "current", "{tool}: {f}");
    }
    git(r.path(), &["checkout", "-q", "-b", "side", &indexed]);
    write(r.path(), "web/src/side.ts", "export const side = 1;\n");
    commit(r.path(), "side");
    for (tool, f) in statuses(root) {
        assert_eq!(f["status"], "diverged", "{tool}: {f}");
        assert!(f.get("behind_by").is_none(), "{tool}: {f}");
    }

    // an indexed commit Git does not know: unknown, never an error
    let db = locate_db(r.path());
    Connection::open(&db)
        .unwrap()
        .execute(
            "UPDATE meta SET value = 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef' WHERE key = 'last_commit'",
            [],
        )
        .unwrap();
    for (tool, f) in statuses(root) {
        assert_eq!(f["status"], "unknown", "{tool}: {f}");
    }
}

// A read that observed another repository identity than the index was brought to (its
// refresh was refused, failed or is still running) is answered by the graph for the
// current tree, so an edit is searchable before the index catches up.
#[test]
fn an_index_behind_the_reads_identity_falls_back_so_edits_stay_visible() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    run_json(&["index", "update", root, "--identity-key", "key-a"]);
    fs::OpenOptions::new()
        .append(true)
        .open(r.path().join("web/src/total.ts"))
        .and_then(|mut f| {
            std::io::Write::write_all(
                &mut f,
                b"\nexport function brandNewWidgetTotal() { return 1; }\n",
            )
        })
        .unwrap();
    let found = |v: &Value| {
        v["hits"]
            .as_array()
            .unwrap()
            .iter()
            .any(|h| h["name"] == "brandNewWidgetTotal")
    };
    let search = |key: &str| {
        run_json(&[
            "search",
            &format!("--identity-key={key}"),
            root,
            "ws",
            "brandNewWidgetTotal",
            "10",
        ])
    };

    // the index is at this read's identity: it answers (and lacks the edit)
    let at_index = search("key-a");
    assert_eq!(at_index["freshness"]["source"], "index", "{at_index}");
    assert!(!found(&at_index), "{at_index}");

    // the tree moved on (key-b) and the index did not: the current graph answers
    let behind = search("key-b");
    assert_eq!(behind["freshness"]["source"], "legacy_graph", "{behind}");
    assert!(found(&behind), "the edit is searchable: {behind}");
    let detail = behind["coverage"]["work"]["graph_cache_detail"]
        .as_str()
        .unwrap();
    assert!(detail.contains("behind this read"), "{detail}");

    // impact and explain take the same flag, after the subcommand
    let impact = run_json(&[
        "symbolgraph",
        "impact",
        "--identity-key=key-b",
        root,
        "ws",
        "brandNewWidgetTotal",
        "4",
    ]);
    assert_eq!(impact["freshness"]["source"], "legacy_graph", "{impact}");
    assert_eq!(
        impact["defined_in"],
        json!(["web/src/total.ts"]),
        "{impact}"
    );
    let explain = run_json(&[
        "symbolgraph",
        "cluster-of",
        "--identity-key=key-a",
        root,
        "ws",
        "web/src/total.ts",
    ]);
    assert_eq!(explain["freshness"]["source"], "index", "{explain}");

    // once the index is brought to key-b, it answers again, edit included
    run_json(&["index", "update", root, "--identity-key", "key-b"]);
    let caught_up = search("key-b");
    assert_eq!(caught_up["freshness"]["source"], "index", "{caught_up}");
    assert!(found(&caught_up), "{caught_up}");
}

#[test]
fn coverage_reports_envelope_losses_instead_of_silent_capping() {
    let r = fixture();
    // the repository may lower the envelope: 4 of the 10 source files fit
    write(r.path(), ".xmustard.json", r#"{"index":{"max_files":4}}"#);
    commit(r.path(), "narrow");
    let root = r.path().to_str().unwrap();
    let check = |cov: &Value, what: &str| {
        assert_eq!(cov["truncated"], true, "{what}: {cov}");
        assert_eq!(cov["max_files"], 4, "{what}: {cov}");
        assert_eq!(cov["complete"], false, "{what}: {cov}");
        assert!(
            cov["eligible_files"].as_u64().unwrap() >= 10,
            "{what}: {cov}"
        );
        let lost = cov["loss_counts"]["envelope_files"].as_u64().unwrap_or(0);
        assert!(lost >= 6, "{what}: losses counted, not dropped: {cov}");
        assert!(
            cov["loss_counts"].get("file_cap").is_none(),
            "{what}: no fixed cap: {cov}"
        );
    };
    // legacy graph (no index) honours the same envelope
    let search = run_json(&["search", root, "ws", "computeTotal", "10"]);
    check(&search["coverage"], "legacy search");
    run_json(&["index", "build", root]);
    for (tool, v) in tool_results(root) {
        check(&v["coverage"], tool);
        let exceeded = &v["coverage"]["envelope"]["exceeded"];
        assert_eq!(exceeded, &json!(["files"]), "{tool}: {v}");
    }
    let search = run_json(&["search", root, "ws", "computeTotal", "10"]);
    let losses = search["coverage"]["losses"].as_array().unwrap();
    assert!(
        losses.iter().any(|l| l["reason"] == "envelope_files"),
        "{search}"
    );
}

// ---------------------------------------------------------------------------
// Resident memory on a 100k-symbol resolved graph
// ---------------------------------------------------------------------------

const FILES: usize = 10_000;
const VERBS: &[&str] = &[
    "parse", "build", "load", "store", "fetch", "render", "handle", "resolve", "create", "update",
    "delete", "compute", "format", "validate", "merge", "split", "encode", "decode", "open",
    "close", "read", "write", "send", "receive", "track", "index", "search", "match", "apply",
    "check", "emit", "watch", "queue", "drain", "flush", "scan", "hash", "sign", "verify",
    "report",
];
const NOUNS: &[&str] = &[
    "User", "Config", "Request", "Response", "Session", "Token", "Cache", "Index", "Graph", "Node",
    "Edge", "Symbol", "File", "Path", "Buffer", "Stream", "Queue", "Event", "Message", "Channel",
    "Worker", "Task", "Job", "Plan", "Run", "Report", "Memory", "Record", "Entry", "Table", "Row",
    "Column", "Schema", "Query", "Result", "Error", "Status", "State", "Store", "Model", "View",
    "Widget", "Layout", "Theme", "Style", "Color", "Font", "Image", "Frame", "Window", "Panel",
    "Button", "Input", "Output", "Handler", "Router", "Client", "Server", "Socket", "Header",
];
const SUFFIXES: &[&str] = &[
    "", "Async", "Sync", "Once", "All", "Many", "Batch", "Safe", "Fast", "Lazy", "Local", "Remote",
    "Internal", "External", "Default", "Legacy", "Next", "Prev", "First", "Last", "Inner", "Outer",
    "Raw", "Typed", "Bound",
];

/// Distinct camelCase names from a small vocabulary, so a query matches the few
/// percent of symbols sharing a word, as in real code.
fn synthetic_name(i: usize) -> String {
    let (v, n, s) = (
        i % VERBS.len(),
        (i / VERBS.len()) % NOUNS.len(),
        i / (VERBS.len() * NOUNS.len()),
    );
    format!("{}{}{}", VERBS[v], NOUNS[n], SUFFIXES[s % SUFFIXES.len()])
}
const SYMS_PER_FILE: usize = 10;
const EDGES_PER_SYM: usize = 5;

/// A resolved-graph store (symbol-level edges, five per symbol, mostly within a
/// package as real call graphs are) written straight into `index.db`.
fn synthetic_store(db: &Path, head: &str, generation: i64) {
    let conn = Connection::open(db).unwrap();
    schema::configure_bulk(&conn).unwrap();
    schema::create(&conn).unwrap();
    let cfg = IndexConfig::default();
    conn.execute_batch("BEGIN").unwrap();
    let n_syms = FILES * SYMS_PER_FILE;
    let distinct_names = n_syms * 6 / 10;
    {
        let mut name = conn
            .prepare("INSERT INTO names(id, name) VALUES (?1, ?2)")
            .unwrap();
        for i in 0..distinct_names {
            name.execute(params![i as i64 + 1, synthetic_name(i)])
                .unwrap();
        }
        let mut file = conn
            .prepare(
                "INSERT INTO files VALUES (?1, ?2, 'typescript', 'code', 2048, 1, 'k', 'h', 'tree_sitter', 0, ?3, 100, ?3)",
            )
            .unwrap();
        let mut sym = conn
            .prepare(
                "INSERT INTO symbols VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, NULL, NULL, 0, 10, ?8, ?8, ?8, 0, NULL, 1, 0, 0)",
            )
            .unwrap();
        for f in 0..FILES {
            file.execute(params![
                f as i64 + 1,
                format!("pkg{:03}/module{:02}/file{f:05}.ts", f / 100, (f / 10) % 10),
                SYMS_PER_FILE as i64
            ])
            .unwrap();
            for k in 0..SYMS_PER_FILE {
                let s = f * SYMS_PER_FILE + k;
                sym.execute(params![
                    s as i64 + 1,
                    f as i64 + 1,
                    k as i64,
                    format!("uid{s}"),
                    (s * 7 % distinct_names) as i64 + 1,
                    format!("q{s}"),
                    ["Function", "Method", "Class", "Interface"][k % 4],
                    (k * 10 + 1) as i64
                ])
                .unwrap();
            }
        }
        let mut edge = conn
            .prepare(
                "INSERT INTO edges(src_file, dst_file, src_symbol, dst_symbol, kind, layer, weight,
                 confidence, provenance, via, reason, access)
                 VALUES (?1, ?2, ?3, ?4, ?5, 'structure', 1, 0.9, 'resolved', '[]', 'test', NULL)",
            )
            .unwrap();
        for s in 0..n_syms {
            let f = s / SYMS_PER_FILE;
            for k in 0..EDGES_PER_SYM {
                // 3 of 5 edges stay in the package (100 files), 2 go anywhere
                let t = if k < 3 {
                    (f / 100 * 100 + (s * 31 + k * 17) % 100) * SYMS_PER_FILE
                        + (s + k) % SYMS_PER_FILE
                } else {
                    (s * 7919 + k * 104_729 + generation as usize) % n_syms
                };
                edge.execute(params![
                    f as i64 + 1,
                    (t / SYMS_PER_FILE) as i64 + 1,
                    s as i64 + 1,
                    t as i64 + 1,
                    ["calls", "accesses", "has_method", "imports", "calls"][k]
                ])
                .unwrap();
            }
        }
    }
    let coverage = json!({"eligible_files": FILES, "indexed_files": FILES, "complete": true,
        "symbols": n_syms, "edges": n_syms * EDGES_PER_SYM,
        "envelope": {"max_files": cfg.max_files, "max_symbols": cfg.max_symbols,
                     "max_total_bytes": cfg.max_total_bytes, "max_file_size": cfg.max_file_size, "exceeded": []}});
    for (k, v) in [
        ("schema_version", schema::SCHEMA_VERSION.to_string()),
        (
            "schema_fingerprint",
            schema::fingerprint(cfg.content_retention),
        ),
        ("analyzer_version", index::analyzer_version()),
        ("content_retention", "symbol".to_string()),
        ("config", cfg.eligibility_fingerprint()),
        ("repo_mode", "git".to_string()),
        ("generation", generation.to_string()),
        ("last_commit", head.to_string()),
        (meta::DIRTY_FLAG, "0".to_string()),
        ("coverage", coverage.to_string()),
    ] {
        meta::set(&conn, k, &v).unwrap();
    }
    conn.execute_batch("COMMIT").unwrap();
    schema::create_indexes(&conn).unwrap();
}

struct Serve {
    child: Child,
    stdin: ChildStdin,
    stdout: BufReader<ChildStdout>,
    next: i64,
}

impl Serve {
    fn start(index_dir: &Path, storage: &str) -> Serve {
        let mut child = Command::new(BIN)
            .args(["serve", "--max-inflight=4", "--trim-idle-ms=0"])
            .env("XMUSTARD_INDEX_DIR", index_dir)
            .env("XMUSTARD_GRAPH_STORAGE", storage)
            // as the Go supervisor starts the resident worker (rustcore.coreChildEnv):
            // one glibc arena on Linux, no effect elsewhere
            .env("MALLOC_ARENA_MAX", "1")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take().unwrap();
        let stdout = BufReader::new(child.stdout.take().unwrap());
        let mut s = Serve {
            child,
            stdin,
            stdout,
            next: 0,
        };
        s.call("initialize", &[]);
        s
    }

    fn call(&mut self, method: &str, args: &[&str]) -> Value {
        let args = args.iter().map(|a| a.to_string()).collect();
        self.pipelined(&[(method, args)]).remove(0)
    }

    /// Send every call before reading any answer, so up to `--max-inflight` of them
    /// run in the service at once. Answers arrive in completion order and are matched
    /// by id. The requests are small enough to sit in the pipe while answers queue.
    fn pipelined(&mut self, calls: &[(&str, Vec<String>)]) -> Vec<Value> {
        let first = self.next + 1;
        for (method, args) in calls {
            self.next += 1;
            let body = json!({"jsonrpc": "2.0", "id": self.next, "method": method, "params": {"args": args}});
            write_frame(
                &mut self.stdin,
                Some(self.next),
                &[body.to_string().as_bytes()],
            )
            .unwrap();
        }
        let mut out = vec![Value::Null; calls.len()];
        for _ in calls {
            let header = read_header(&mut self.stdout).unwrap();
            let mut buf = vec![0; header.len];
            self.stdout.read_exact(&mut buf).unwrap();
            let v: Value = serde_json::from_slice(&buf).unwrap();
            assert!(v.get("error").is_none(), "{v}");
            out[(v["id"].as_i64().unwrap() - first) as usize] = v["result"].clone();
        }
        out
    }

    fn mem(&self) -> memprobe::Mem {
        memprobe::sample(self.child.id()).expect("a memory sample of the service")
    }
}

impl Drop for Serve {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// Queries every tool path runs, several times over.
/// One agent's query round: search, impact, trace and explain (cluster-of).
fn round_calls(root: &str, i: usize) -> Vec<(&'static str, Vec<String>)> {
    let name = synthetic_name(i * 37 % 60_000);
    let other = synthetic_name(i * 53 % 60_000 + 1);
    let v = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    vec![
        ("search", v(&[root, "ws", &name, "10"])),
        ("symbolgraph", v(&["impact", root, "ws", &name, "4"])),
        ("symbolgraph", v(&["trace", root, "ws", &name, &other])),
        (
            "symbolgraph",
            v(&["cluster-of", root, "ws", "pkg001/module02/file00120.ts"]),
        ),
    ]
}

/// How the clients' requests reach the service.
#[derive(Clone, Copy, Debug)]
enum Load {
    /// One request in flight at a time, the clients' rounds interleaved.
    Sequential,
    /// Every client's round sent at once, so up to `--max-inflight=4` run together.
    Overlapping,
}

/// One round per client; returns each client's search result.
fn client_rounds(s: &mut Serve, root: &str, load: Load, i: usize) -> Vec<Value> {
    let calls: Vec<_> = (0..CLIENTS)
        .flat_map(|c| round_calls(root, c * 100 + i))
        .collect();
    let answers: Vec<Value> = match load {
        Load::Overlapping => s.pipelined(&calls),
        Load::Sequential => calls
            .iter()
            .flat_map(|c| s.pipelined(std::slice::from_ref(c)))
            .collect(),
    };
    answers.into_iter().step_by(calls.len() / CLIENTS).collect()
}

/// MiB above the service's base, on both readings (see `memprobe`).
#[derive(Clone, Copy, Debug)]
struct Above {
    rss: f64,
    dirty: f64,
}

struct Measured {
    /// After the first query rounds.
    steady: Above,
    /// The highest sampled while the swap happened under load.
    swap_peak: Above,
    /// After the swap settled.
    after_swap: Above,
    generations: BTreeSet<i64>,
}

/// Agents querying the service at once.
const CLIENTS: usize = 4;

/// Bounds above the service's base, in MiB: `dirty` on its own memory (Linux
/// `RssAnon + RssShmem`), `rss` on its resident set (see `memprobe`).
struct Lines {
    dirty: f64,
    rss: f64,
}

/// Queries arriving one at a time. Measured on the Linux build box (WS-FIX-06, load
/// average 5-12, one run beside another index test binary), steady to after the swap:
/// dirty +9.4 to +10.0 MiB in four debug and two release runs; RSS +13.4 to +15.6 in
/// six debug runs and +12.9 to +13.4 in the release ones. The dirty bound is the
/// strict one. The RSS bound is the 15 MiB acceptance line in a release build and
/// looser in a debug build, whose code pages count in RSS (about 5 MiB of the debug
/// delta is file-backed).
const SEQUENTIAL: Lines = Lines {
    dirty: 12.0,
    rss: memprobe::rss_line(15.0, 20.0),
};
/// Queries in flight together (the same runs): dirty +10.2 to +14.8 MiB and RSS +13.5
/// to +19.2 in both builds. A regression guard, not the acceptance line.
const OVERLAPPING: Lines = Lines {
    dirty: 18.0,
    rss: 32.0,
};

/// Measure the service on the synthetic store: base after the handshake, then after
/// queries, then during and after a snapshot swap with queries in flight.
fn measure(storage: &str, load: Load) -> Measured {
    let r = repo(&[("README.md", "synthetic\n")]);
    let root = r.path().to_str().unwrap().to_string();
    let head = git(r.path(), &["rev-parse", "HEAD"]);
    let dir = TempDir::new().unwrap();
    let db = dir.path().join(index::DB_FILE);
    synthetic_store(&db, &head, 1);
    // the index worker writes the segment; the service only reads it
    index::ensure_graph_segment(&db).unwrap();

    let mut s = Serve::start(dir.path(), storage);
    let base = s.mem();
    let mut gens = BTreeSet::new();
    for i in 0..4 {
        for v in client_rounds(&mut s, &root, load, i) {
            gens.insert(v["freshness"]["snapshot_generation"].as_i64().unwrap());
            assert_eq!(
                v["freshness"]["source"], "resident_index",
                "{}",
                v["freshness"]
            );
        }
    }
    let steady = s.mem();

    // swap: a new generation lands while four clients keep querying
    let pid = s.child.id();
    let stop = Arc::new(AtomicBool::new(false));
    let sampler = {
        let stop = stop.clone();
        std::thread::spawn(move || {
            let mut peak = memprobe::Mem::default();
            while !stop.load(Ordering::Relaxed) {
                if let Some(m) = memprobe::sample(pid) {
                    peak.rss = peak.rss.max(m.rss);
                    peak.dirty = peak.dirty.max(m.dirty);
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            peak
        })
    };
    let clients = {
        let (stop, root) = (stop.clone(), root.clone());
        std::thread::spawn(move || {
            let mut seen = BTreeSet::new();
            let mut i = 10;
            while !stop.load(Ordering::Relaxed) {
                for v in client_rounds(&mut s, &root, load, i) {
                    seen.insert(v["freshness"]["snapshot_generation"].as_i64().unwrap());
                }
                i += 1;
            }
            (s, seen)
        })
    };
    let next = dir.path().join("next.db");
    synthetic_store(&next, &head, 2);
    {
        let conn = Connection::open(&next).unwrap();
        conn.execute_batch("PRAGMA journal_mode=WAL;").unwrap();
    }
    index::ensure_graph_segment(&next).unwrap();
    fs::rename(&next, &db).unwrap();
    std::thread::sleep(Duration::from_secs(3));
    stop.store(true, Ordering::Relaxed);
    let (mut s, seen) = clients.join().unwrap();
    gens.extend(seen);
    let peak = sampler.join().unwrap();
    for i in 0..4 {
        for v in client_rounds(&mut s, &root, load, i + 500) {
            gens.insert(v["freshness"]["snapshot_generation"].as_i64().unwrap());
        }
    }
    let swapped = s.mem();
    let stats = s.call("$/stats", &[]);
    let above = |m: memprobe::Mem| Above {
        rss: memprobe::mib(m.rss.saturating_sub(base.rss)),
        dirty: memprobe::mib(m.dirty.saturating_sub(base.dirty)),
    };
    let m = Measured {
        steady: above(steady),
        swap_peak: above(peak),
        after_swap: above(swapped),
        generations: gens,
    };
    let row = |a: Above| format!("+{:.1} (dirty +{:.1})", a.rss, a.dirty);
    eprintln!(
        "{storage}, {load:?}: base {:.1} MiB (dirty {:.1}), steady {}, swap peak {}, after swap {}; index {}",
        memprobe::mib(base.rss),
        memprobe::mib(base.dirty),
        row(m.steady),
        row(m.swap_peak),
        row(m.after_swap),
        stats["index"]
    );
    m
}

/// PAR-IMP-02 / WS-14 acceptance: with file-backed segments (the default), the
/// service's RSS on a 100k-symbol graph at five resolved edges per symbol stays within
/// 15 MiB of its base while four clients' queries arrive one at a time, including a
/// snapshot swap under that load.
///
/// With the four clients' queries in flight together, each pool thread holds its own
/// query transients (impact's file list, search's candidate pool): +13.4 to +15.2 MiB
/// on Linux with one glibc arena, +22 to +27 MiB on macOS. That reaches or passes the
/// line and is recorded as open in the benchmark note; the bound here only guards
/// against regressions past it.
/// The in-memory layout is measured for the record (see the benchmark note).
///
/// Both readings come from the service's own `/proc/<pid>/status`, sampled from this
/// process, so other processes' load moves neither. The strict bounds are on its dirty
/// memory: on a loaded box the file-backed part of RSS moved by more than a MiB
/// between runs (debug, sequential: +13.4 to +15.6 MiB against the 15 MiB line), while
/// the dirty delta stayed within 0.6 MiB. See `SEQUENTIAL` and `OVERLAPPING`.
///
/// Linux is the reference platform for these lines. Elsewhere the test is ignored,
/// since macOS's allocator measures above them; `--ignored` still runs it there.
#[test]
#[cfg_attr(
    not(target_os = "linux"),
    ignore = "RSS lines are set on Linux; macOS's allocator measures above them"
)]
fn resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line() {
    for (load, lines) in [
        (Load::Sequential, SEQUENTIAL),
        (Load::Overlapping, OVERLAPPING),
    ] {
        let m = measure("file", load);
        assert!(
            m.generations.contains(&1) && m.generations.contains(&2),
            "{load:?}: the service swapped snapshots under load: {:?}",
            m.generations
        );
        for (what, above) in [
            ("steady", m.steady),
            ("swap peak", m.swap_peak),
            ("after swap", m.after_swap),
        ] {
            assert!(
                above.dirty <= lines.dirty,
                "{load:?}: {what} dirty +{:.1} MiB over {} MiB",
                above.dirty,
                lines.dirty
            );
            assert!(
                above.rss <= lines.rss,
                "{load:?}: {what} RSS +{:.1} MiB over {} MiB",
                above.rss,
                lines.rss
            );
        }
    }
    if std::env::var_os("XMUSTARD_MEASURE_MEM_STORAGE").is_some() {
        measure("mem", Load::Sequential);
    }
}
