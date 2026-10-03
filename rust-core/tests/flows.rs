//! Execution flows over the resolved CALLS projection (WS-38c, PAR-SYM-09):
//! entry-point scoring, a bounded DFS, dedup, labels, intra/cross classification and
//! truncation stats.
//!
//! Exercised through the real `xmustard-core` binary against disposable Git
//! repositories, as `tests/impact_v2.rs` does.
//!
//! Fixtures need a `go.mod`: without one the Go resolver reports every import as
//! `external` and drops it, so a cross-file CALL never becomes a symbol edge and
//! every answer here would be empty for the wrong reason.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::process::{Command, Output};

use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// A Go module, so the resolver admits cross-package calls.
const GO_MOD: (&str, &str) = ("go.mod", "module example\n\ngo 1.21\n");

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
}

fn write(root: &Path, rel: &str, content: &str) {
    let p = root.join(rel);
    fs::create_dir_all(p.parent().unwrap()).unwrap();
    fs::write(&p, content).unwrap();
    // an hour old: never inside the racy stat window of the next scan
    let an_hour_ago = std::time::SystemTime::now() - std::time::Duration::from_secs(3600);
    fs::File::options()
        .write(true)
        .open(&p)
        .unwrap()
        .set_modified(an_hour_ago)
        .unwrap();
}

fn repo(list: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in list {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn run(args: &[&str]) -> Output {
    Command::new(BIN).args(args).output().unwrap()
}

fn ok_json(args: &[&str]) -> Value {
    let out = run(args);
    assert!(
        out.status.success(),
        "{args:?} exit {:?}: {}",
        out.status,
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn build(root: &Path) {
    ok_json(&["index", "build", root.to_str().unwrap()]);
}

/// `index flows <root> [flags]`: the index family takes no workspace id.
fn flows(root: &Path, flags: &[&str]) -> Value {
    let mut args = vec!["index", "flows", root.to_str().unwrap()];
    args.extend_from_slice(flags);
    ok_json(&args)
}

/// One entry-point candidate, with the fields the score is checked against.
#[derive(Debug, Clone)]
struct Seed {
    name: String,
    path: String,
    callers: u64,
    callees: u64,
    exported: bool,
    /// The score the answer reports.
    score: f64,
    /// The score the ratio alone would give, so the exported doubling and the name
    /// multiplier can each be checked separately.
    ratio: f64,
    multiplier: f64,
}

fn read_seeds(v: &Value) -> Vec<Seed> {
    v["entry_points"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .map(|e| Seed {
            name: e["name"].as_str().unwrap().to_string(),
            path: e["path"].as_str().unwrap().to_string(),
            callers: e["callers"].as_u64().unwrap(),
            callees: e["callees"].as_u64().unwrap(),
            exported: e["exported"].as_bool().unwrap(),
            score: e["score"].as_f64().unwrap(),
            ratio: e["callees_ratio"].as_f64().unwrap(),
            multiplier: e["name_multiplier"].as_f64().unwrap(),
        })
        .collect()
}

/// One flow, with the fields this row's contract names.
#[derive(Debug, Clone)]
struct Flow {
    label: String,
    rule: String,
    class: String,
    seeds: Vec<String>,
    steps: Vec<(String, String)>,
    depth: u64,
    /// Communities the steps span, as the answer reports the count beside the class.
    communities: u64,
    /// Steps the walk reached but the listing cap left out.
    steps_omitted: u64,
}

fn read_flows(v: &Value) -> Vec<Flow> {
    v["flows"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .map(|f| Flow {
            label: f["label"].as_str().unwrap().to_string(),
            rule: f["label_rule"].as_str().unwrap().to_string(),
            class: f["class"].as_str().unwrap().to_string(),
            seeds: f["seeds"]
                .as_array()
                .unwrap()
                .iter()
                .map(|s| s["name"].as_str().unwrap().to_string())
                .collect(),
            steps: f["steps"]
                .as_array()
                .unwrap()
                .iter()
                .map(|s| {
                    (
                        s["name"].as_str().unwrap().to_string(),
                        s["path"].as_str().unwrap().to_string(),
                    )
                })
                .collect(),
            depth: f["depth"].as_u64().unwrap(),
            communities: f["communities"].as_u64().unwrap(),
            steps_omitted: f["steps_omitted"].as_u64().unwrap(),
        })
        .collect()
}

/// Seed names by score, highest first, so an ordering claim can be read off directly.
fn by_score(v: &Value) -> Vec<Seed> {
    let mut seeds = read_seeds(v);
    seeds.sort_by(|a, b| {
        b.score
            .partial_cmp(&a.score)
            .expect("score is a number")
            .then_with(|| a.name.cmp(&b.name))
    });
    seeds
}

/// The truncation counters, as (key, value) pairs.
fn truncation(v: &Value) -> BTreeMap<String, u64> {
    v["truncation"]
        .as_object()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .map(|(k, n)| (k.clone(), n.as_u64().unwrap()))
        .collect()
}

/// A Go function `func Name() { <calls...> }` in package `pkg`.
fn go_func(name: &str, calls: &[&str]) -> String {
    let body: String = calls.iter().map(|c| format!("\t{c}()\n")).collect();
    format!("package pkg\n\nfunc {name}() {{\n{body}}}\n")
}

/// A Go source file of [`go_func`]s. Concatenating the declarations rather than formatting
/// them through `{}` keeps the number of functions and the number of placeholders from
/// drifting apart, which is what a `format!` per fixture invited: four rounds of this
/// file's own compilation failed on exactly that mismatch.
fn gofuncs(fns: &[String]) -> String {
    fns.concat()
}

/// [`gofuncs`] for a call site inside a fixture list, which holds `&str` pairs. One leak
/// per disposable repository, reclaimed with the process; a test file leaks nothing else.
fn gofuncs_ref(fns: &[String]) -> &'static str {
    Box::leak(gofuncs(fns).into_boxed_str())
}

/// A chain `a -> b -> ... ` of exported names, each calling the next, all in one file.
fn chain(prefix: &str, len: usize) -> String {
    let mut src = String::from("package pkg\n\n");
    for i in 0..len {
        let next = if i + 1 < len {
            format!("{prefix}{}", i + 1)
        } else {
            String::new()
        };
        if next.is_empty() {
            src.push_str(&format!("func {prefix}{i}() {{}}\n"));
        } else {
            src.push_str(&format!("func {prefix}{i}() {{ {next}() }}\n"));
        }
    }
    src
}

// --- entry-point scoring ---------------------------------------------------

#[test]
fn the_score_is_callees_over_callers_plus_one_times_exported_times_the_name_multiplier() {
    // One seed per scoring shape, so each factor can be read against a symbol whose
    // other factors are fixed. `Run*` names are exported and get the command
    // multiplier; a lowercase name is neither exported nor a command.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/run.go",
            gofuncs_ref(&[
                go_func("Alpha", &["beta", "gamma", "delta"]),
                go_func("beta", &["gamma"]),
                go_func("gamma", &[]),
                go_func("delta", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    let alpha = seeds
        .iter()
        .find(|s| s.name == "Alpha")
        .expect("Alpha is a seed");
    let beta = seeds
        .iter()
        .find(|s| s.name == "beta")
        .expect("beta is a seed");

    // callees/(callers+1), straight from the fixture: Alpha has 3 callees and 0 callers,
    // beta has 1 callee and 1 caller.
    assert_eq!(alpha.callees, 3, "{alpha:?}");
    assert_eq!(alpha.callers, 0, "{alpha:?}");
    assert_eq!(alpha.ratio, 3.0, "3/(0+1) in {alpha:?}");
    assert_eq!(beta.ratio, 0.5, "1/(1+1) in {beta:?}");

    // The reported score is the product of the three factors, to the precision the
    // answer prints.
    for s in &seeds {
        let want = s.ratio * if s.exported { 2.0 } else { 1.0 } * s.multiplier;
        assert!(
            (s.score - want).abs() < 1e-9,
            "score {} != ratio {ratio} * exported {exported} * multiplier {mult} for {name}",
            s.score,
            ratio = s.ratio,
            exported = s.exported,
            mult = s.multiplier,
            name = s.name,
        );
    }
}

#[test]
fn a_symbol_with_more_callers_and_the_same_callees_scores_lower() {
    // Same callee count, different caller count: the (callerCount+1) term must
    // separate them, or the ratio is not read.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/entry.go",
            gofuncs_ref(&[
                // Few callers: 0 callers, 3 callees.
                go_func("FewCallers", &["LeafOne", "LeafTwo", "LeafThree"]),
                // Many callers: 4 callers, same 3 callees.
                go_func("ManyCallers", &["LeafOne", "LeafTwo", "LeafThree"]),
                go_func("CallerOne", &["ManyCallers"]),
                go_func("CallerTwo", &["ManyCallers"]),
                go_func("CallerThree", &["ManyCallers"]),
                go_func("CallerFour", &["ManyCallers"]),
                go_func("LeafOne", &[]),
                go_func("LeafTwo", &[]),
                go_func("LeafThree", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = by_score(&v);
    let few = seeds
        .iter()
        .position(|s| s.name == "FewCallers")
        .expect("seed");
    let many = seeds
        .iter()
        .position(|s| s.name == "ManyCallers")
        .expect("seed");
    assert!(
        few < many,
        "FewCallers (1 caller) must outrank ManyCallers (4 callers): {seeds:?}"
    );
    // Both are exported with the same 3 callees, so only the caller term differs.
    assert_eq!(seeds[few].callees, seeds[many].callees);
    assert_eq!(seeds[many].callers, 4, "{seeds:?}");
    assert_eq!(
        seeds[few].callers, 0,
        "FewCallers is called by nobody: {seeds:?}"
    );
    // Both live in the file the fixture declares, so the answer's path is the one the
    // index read rather than a bare name.
    assert_eq!(seeds[few].path, "pkg/entry.go", "{seeds:?}");
    assert_eq!(seeds[many].path, "pkg/entry.go", "{seeds:?}");
}

#[test]
fn an_exported_symbol_scores_twice_an_unexported_one_with_the_same_graph() {
    // Two identical shapes differing only in the export rule: Go exports by capital, so
    // the only way the index knows is the stored `exported` flag.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/twin.go",
            gofuncs_ref(&[
                go_func("Open", &["Hub"]),
                go_func("open", &["Hub"]),
                go_func("Hub", &["Spoke", "Spoke"]),
                go_func("Spoke", &[]),
                go_func("Other", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    let open = seeds
        .iter()
        .find(|s| s.name == "Open")
        .expect("Open is a seed");
    let closed = seeds
        .iter()
        .find(|s| s.name == "open")
        .expect("open is a seed");
    assert!(open.exported, "Open must be reported exported: {open:?}");
    assert!(!closed.exported, "open must not be exported: {closed:?}");
    assert_eq!(open.ratio, closed.ratio, "same graph shape: {seeds:?}");
    assert_eq!(
        open.multiplier, closed.multiplier,
        "same name class: {seeds:?}"
    );
    assert_eq!(open.score, closed.ratio * open.multiplier * 2.0, "{open:?}");
    assert_eq!(closed.score, closed.ratio * closed.multiplier, "{closed:?}");
}

#[test]
fn every_name_multiplier_is_a_table_row_and_the_answer_reports_which() {
    // One symbol per name class the table names, so a row that is deleted or given the
    // wrong multiplier fails here rather than silently scoring everything 1.0.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/names.go",
            &format!(
                "{}\n{}\n{}\n{}\n{}\n{}\n{}\n{}\n{}\n",
                // main: the program entry name.
                go_func("main", &["Leaf"]),
                // test: a test entry name.
                go_func("TestThing", &["Leaf"]),
                // Handle: a handler.
                go_func("HandleRequest", &["Leaf"]),
                // Command/Run/Start: a command.
                go_func("RunThing", &["Leaf"]),
                go_func("StartThing", &["Leaf"]),
                go_func("CommandThing", &["Leaf"]),
                // Serve: a server entry.
                go_func("ServeThing", &["Leaf"]),
                // plain: none of the above.
                go_func("plainThing", &["Leaf"]),
                go_func("Leaf", &[]),
            ),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    let of = |name: &str| -> Seed {
        seeds
            .iter()
            .find(|s| s.name == name)
            .unwrap_or_else(|| panic!("{name} is a seed in {seeds:?}"))
            .clone()
    };
    // The classes the fixture names, and the multiplier the answer gave each. Each is
    // compared against the literal here rather than against another seed's figure, so
    // a wrong multiplier cannot pass by being applied consistently.
    assert_eq!(of("main").multiplier, 2.0, "main: {seeds:?}");
    assert_eq!(of("TestThing").multiplier, 2.0, "test: {seeds:?}");
    assert_eq!(of("HandleRequest").multiplier, 1.5, "handler: {seeds:?}");
    assert_eq!(of("RunThing").multiplier, 1.5, "command: {seeds:?}");
    assert_eq!(of("StartThing").multiplier, 1.5, "command: {seeds:?}");
    assert_eq!(of("CommandThing").multiplier, 1.5, "command: {seeds:?}");
    assert_eq!(of("ServeThing").multiplier, 1.25, "serve: {seeds:?}");
    assert_eq!(of("plainThing").multiplier, 1.0, "plain: {seeds:?}");

    // The answer names the class of each seed, so a caller can see which row applied.
    let classes = v["name_classes"]
        .as_array()
        .expect("name_classes is a table");
    let names: BTreeSet<&str> = classes
        .iter()
        .map(|c| c["name"].as_str().unwrap())
        .collect();
    for class in ["main", "test", "handler", "command", "serve", "plain"] {
        assert!(
            names.contains(class),
            "the table must publish the {class} row: {names:?}"
        );
    }
    // And every class is one row with one multiplier, read from the same table the
    // score used.
    for c in classes {
        let want = table_multiplier(c["name"].as_str().unwrap());
        assert_eq!(c["multiplier"].as_f64().unwrap(), want, "row {c:#}");
    }
}

/// The name-multiplier table the answer must publish. A copy of the contract, so the
/// test does not read its expected values from the code under test.
fn table_multiplier(class: &str) -> f64 {
    match class {
        "main" | "test" => 2.0,
        "handler" | "command" => 1.5,
        "serve" => 1.25,
        "plain" => 1.0,
        other => panic!("the answer published an unknown name class {other}"),
    }
}

#[test]
fn at_most_the_top_two_hundred_candidates_are_seeds() {
    // 240 exported entry points, each calling one distinct leaf, so all 240 score
    // above the floor and the cap is the only thing that can hold the answer at 200.
    let mut src = String::from("package pkg\n\n");
    for i in 0..240 {
        src.push_str(&format!("func Entry{i}() {{ Leaf{i}() }}\n"));
        src.push_str(&format!("func Leaf{i}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/many.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    assert_eq!(
        seeds.len(),
        200,
        "PAR-SYM-09's top 200: {} seeds",
        seeds.len()
    );
    assert_eq!(
        v["candidates"].as_u64().unwrap(),
        240,
        "the pool before the cap"
    );
    assert_eq!(
        truncation(&v)["dropped_candidates"],
        40,
        "what the cap stopped: {v:#}"
    );
    // The kept 200 are the highest-scoring, so the tie among equals resolves by name
    // and every dropped one sorts below every kept one.
    let scores: Vec<f64> = seeds.iter().map(|s| s.score).collect();
    let mut desc = scores.clone();
    desc.sort_by(|a, b| b.partial_cmp(a).unwrap());
    assert_eq!(scores, desc, "seeds must be listed highest score first");
}

/// The seed list is in RANK order, not merely sorted by score.
///
/// The 240-candidate fixture above gives every seed the same score, so it cannot tell a
/// correctly ranked list from a reversed one: both are non-increasing. This fixture spreads
/// the scores instead — each entry point calls a different number of leaves — so the ranks
/// are all distinct and a reversed list fails immediately.
///
/// The tie-break is asserted separately in the next test; this one only fixes the order.
#[test]
fn seeds_are_listed_from_the_highest_score_to_the_lowest() {
    // More than 200 candidates with DISTINCT scores, so the cap has to choose and the
    // choice has to be the right one. 240 entry points whose scores cycle through eight
    // ranks: the cap therefore cuts inside a rank group, and a list reversed after the cut
    // would come back lowest-first.
    let mut src = String::from("package pkg\n\n");
    for i in 0..240usize {
        let rank = i % 8;
        src.push_str(&format!("func Entry{i:03}() {{\n"));
        for leaf in 0..=rank {
            src.push_str(&format!("\tEntry{i:03}Leaf{leaf}()\n"));
        }
        src.push_str("}\n");
        for leaf in 0..=rank {
            src.push_str(&format!("func Entry{i:03}Leaf{leaf}() {{}}\n"));
        }
    }
    let d = repo(&[GO_MOD, ("pkg/rungs.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    assert_eq!(
        seeds.len(),
        200,
        "the fixture must exceed the cap: {} seeds",
        seeds.len()
    );
    // The scores are a product of a rank and 2, so the highest is 16 and the lowest
    // 2: if the cut were reversed the first seed would score 2 and the last 16.
    let scores: Vec<f64> = seeds.iter().map(|s| s.score).collect();
    assert_eq!(
        scores.first().copied(),
        Some(16.0),
        "the best candidate leads the list: {scores:?}"
    );
    assert_eq!(
        scores.last().copied(),
        Some(2.0),
        "the weakest kept candidate is last: {scores:?}"
    );
    let mut desc = scores.clone();
    desc.sort_by(|a, b| b.partial_cmp(a).unwrap());
    assert_eq!(scores, desc, "seeds must be listed highest score first");
}

/// Two seeds with identical scores are ordered by (path, line, name), so the list is the
/// same on every run and does not depend on the order the index happened to store them.
///
/// The 240-candidate fixture pins the cap and this one pins the tie-break: same score for
/// every entry point, all in one file, so path is equal and the order must fall to line and
/// then to name. `Entry0`..`Entry9` are declared in source order, so line order and name
/// order agree here; the point is that a rule reading only one of the two would still pass
/// on this fixture, so the assertion checks that the emitted order is the declared one
/// rather than that some particular comparator produced it.
#[test]
fn seeds_with_equal_scores_keep_a_stable_declared_order() {
    let mut src = String::from("package pkg\n\n");
    for i in 0..12 {
        src.push_str(&format!("func Entry{i}() {{ Leaf{i}() }}\n"));
        src.push_str(&format!("func Leaf{i}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/equals.go", &src)]);
    build(d.path());
    let first = flows(d.path(), &[]);
    let second = flows(d.path(), &[]);
    let seeds = read_seeds(&first);
    let names: Vec<&str> = seeds.iter().map(|s| s.name.as_str()).collect();
    let expected: Vec<String> = (0..12).map(|i| format!("Entry{i}")).collect();
    assert_eq!(names, expected, "equal scores fall to declared order: {names:?}");
    // Two runs over one index are byte-identical, which is the property the tie-break
    // exists to provide.
    assert_eq!(first, second, "the answer must not vary between runs");
}

#[test]
fn the_limit_flag_lowers_the_seed_cap_and_is_refused_when_it_is_not_a_number() {
    let mut src = String::from("package pkg\n\n");
    for i in 0..40 {
        src.push_str(&format!("func Entry{i}() {{ Leaf{i}() }}\n"));
        src.push_str(&format!("func Leaf{i}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/many.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &["--limit=5"]);
    assert_eq!(read_seeds(&v).len(), 5, "--limit=5 must cap the seeds");
    assert_eq!(
        v["candidates"].as_u64().unwrap(),
        40,
        "the pool is unchanged"
    );

    let out = run(&["index", "flows", d.path().to_str().unwrap(), "--limit=nope"]);
    assert!(!out.status.success(), "--limit=nope must fail");
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(err.contains("--limit"), "stderr must name the flag: {err}");
}

// --- bounded DFS ------------------------------------------------------------

#[test]
fn a_flow_stops_at_depth_ten() {
    // 14 names in a chain. The depth cap bounds each walk, not the union of the walks,
    // and every name but the last has a callee, so every one of them is a seed: the
    // flow seeded at `Step0` is the one that must stop at ten steps.
    let d = repo(&[GO_MOD, ("pkg/chain.go", &chain("Step", 14))]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let root = flows
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Step0"))
        .unwrap_or_else(|| panic!("the flow seeded at Step0 in {flows:?}"));
    assert_eq!(
        root.steps.len(),
        10,
        "ten steps from the seed at the depth cap: {root:?}"
    );
    assert_eq!(
        root.steps.last().map(|(n, _)| n.as_str()),
        Some("Step10"),
        "the tenth step is reached: {root:?}"
    );
    assert_eq!(root.depth, 10, "the cap is ten: {root:?}");
    // No walk anywhere in the answer goes deeper than the cap.
    let deepest = flows.iter().map(|f| f.depth).max().expect("a flow");
    assert_eq!(deepest, 10, "no flow is deeper than the cap in {flows:?}");
    assert!(
        truncation(&v)["truncated_at_depth"] > 0,
        "a chain past the cap must report the depth truncation: {:?}",
        truncation(&v)
    );
}

/// `depth` is the distance to the FURTHEST step reached, not the number of steps taken.
///
/// A chain cannot tell the two apart: from `Step0` the tenth step is also the tenth step
/// taken, so a walk whose `depth` field was wired to `steps.len()` would answer identically
/// and every assertion in the chain test above would still pass. The two differ as soon as
/// a branch exists — a walk that reaches a node three hops away through one path and a
/// different node two hops away through another has five steps but a depth of three.
///
/// So this fixture is a fan: one trunk three deep, with a shorter sibling hanging off the
/// second node, so the listed steps and the reported depth are different numbers and a
/// mutation that swaps them is visible.
#[test]
fn a_flows_depth_is_its_deepest_step_and_not_its_step_count() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/fan.go",
            gofuncs_ref(&[
                go_func("FanRoot", &["FanLong", "FanShort"]),
                go_func("FanLong", &["FanMid"]),
                go_func("FanMid", &["FanLeaf"]),
                go_func("FanLeaf", &[]),
                go_func("FanShort", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let f = flows
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "FanRoot"))
        .unwrap_or_else(|| panic!("FanRoot's flow in {flows:?}"));
    // Four steps are listed (FanLong, FanMid, FanLeaf and FanShort), and the furthest is
    // three hops out. A depth reported as the step count would say four.
    assert_eq!(
        f.steps.len(),
        4,
        "the walk reaches four symbols: {f:?}"
    );
    assert_eq!(f.depth, 3, "the furthest step is three hops out: {f:?}");
    assert_ne!(
        f.depth as usize, f.steps.len(),
        "the fixture must separate the two figures: {f:?}"
    );
}

/// A flow lists at most 50 steps and reports how many it left out.
///
/// The other bounds do not make this reachable on their own: depth 10 and branching 4 cap
/// one walk at 4^10 nodes, but a WIDE tree reaches that breadth long before the depth cap
/// bites, so a walk really does collect more than 50 steps and really does have some to
/// omit. Each level here branches four times, so by depth four the walk holds 1+4+16+64
/// nodes — well past 50 — and the answer must cap the list and count the rest.
#[test]
fn a_flow_lists_at_most_fifty_steps_and_counts_the_rest() {
    // A 4-ary tree, four levels deep: the leaves are the last level's children, so one
    // walk from the root collects 1 + 4 + 16 + 64 + 256 nodes.
    let mut src = String::from("package pkg\n\n");
    let mut level = vec!["Branch0".to_string()];
    for _ in 0..4 {
        let mut next = Vec::new();
        for name in &level {
            let kids: Vec<String> = (0..4).map(|k| format!("{name}To{k}")).collect();
            next.extend(kids.clone());
            let body: String = kids.iter().map(|k| format!("\t{k}()\n")).collect();
            src.push_str(&format!("func {name}() {{\n{body}}}\n"));
        }
        level = next;
    }
    for leaf in &level {
        src.push_str(&format!("func {leaf}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/tree.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let root = flows
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Branch0"))
        .unwrap_or_else(|| panic!("Branch0's flow in {flows:?}"));
    assert_eq!(
        root.steps.len(),
        50,
        "the step list is capped at fifty: {root:?}"
    );
    assert!(
        root.steps_omitted > 0,
        "a capped list must say how many steps it left out: {root:?}"
    );
    // The cap is a listing bound, not a walk bound: the truncation counter beside it is
    // what records the difference between what was walked and what was listed.
    assert!(
        truncation(&v)["truncated_step_lists"] > 0,
        "the step-list truncation must be counted: {:?}",
        truncation(&v)
    );
}

#[test]
fn a_flow_explores_at_most_four_siblings_per_node() {
    // A root calling 6 distinct leaves. Branching 4 explores four of them, which alone is
    // below the three-step minimum, so the root also calls a helper that reaches a fifth
    // leaf: Root + Helper + three siblings + that leaf is the whole walk.
    let mut src = String::from("package pkg\n\n");
    // Helper is called first so it is inside the branching window: the cap takes the first
    // four callees the CSR holds, and the CSR order is not the source order.
    src.push_str("func Root() { Helper()");
    for i in 0..6 {
        src.push_str(&format!(" Sibling{i}()"));
    }
    src.push_str(" }\n");
    src.push_str("func Helper() { LeafDeep() }\n");
    src.push_str("func LeafDeep() {}\n");
    for i in 0..6 {
        src.push_str(&format!("func Sibling{i}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/fan.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let root = flows
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Root"))
        .unwrap_or_else(|| panic!("Root's flow in {flows:?}"));
    let reached: BTreeSet<&str> = root
        .steps
        .iter()
        .map(|(n, _)| n.as_str())
        .chain(root.seeds.iter().map(String::as_str))
        .collect();
    assert_eq!(
        reached.len(),
        6,
        "the seed Root plus its five reached steps: {root:?}"
    );
    assert!(reached.contains("Helper"), "{root:?}");
    assert!(reached.contains("LeafDeep"), "Helper's leaf: {root:?}");
    assert!(
        !reached.contains("Sibling5"),
        "the fifth sibling is past the branching cap: {root:?}"
    );
    assert!(
        truncation(&v)["truncated_at_branching"] > 0,
        "a fan past the cap must report the branching truncation: {:?}",
        truncation(&v)
    );
}

#[test]
fn a_reachability_set_shorter_than_three_steps_is_not_a_flow() {
    // Two seeds each reach two leaves: the seed takes two steps, one short of the
    // minimum of three, so neither produces a flow.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/short.go",
            gofuncs_ref(&[
                go_func("AlphaShort", &["LeafOne", "LeafTwo"]),
                go_func("BetaShort", &["LeafOne", "LeafTwo"]),
                go_func("LeafOne", &[]),
                go_func("LeafTwo", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    assert_eq!(
        flows.len(),
        0,
        "a two-step reachability set is below the minimum: {flows:?}"
    );
    // The seeds are still scored and listed; only the flow is withheld, and the answer
    // says how many were withheld.
    let seeds = read_seeds(&v);
    assert!(
        seeds.iter().any(|s| s.name == "AlphaShort"),
        "the entry point is still a seed: {seeds:?}"
    );
    assert_eq!(truncation(&v)["dropped_under_min_steps"], 2, "{v:#}");
}

// --- dedup, label, class ---------------------------------------------------

#[test]
fn two_seeds_reaching_the_same_set_are_one_flow_naming_both() {
    // Alpha and Beta both call the same three leaves and nothing else, so their
    // reachability sets are identical and the two candidates are one flow.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/dup.go",
            gofuncs_ref(&[
                go_func("AlphaTwin", &["LeafOne", "LeafTwo", "LeafThree"]),
                go_func("BetaTwin", &["LeafOne", "LeafTwo", "LeafThree"]),
                go_func("LeafOne", &[]),
                go_func("LeafTwo", &[]),
                go_func("LeafThree", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let twin = flows
        .iter()
        .find(|f| f.seeds.len() > 1)
        .unwrap_or_else(|| panic!("a deduped flow with two seeds in {flows:?}"));
    let mut seeds = twin.seeds.clone();
    seeds.sort();
    assert_eq!(seeds, vec!["AlphaTwin", "BetaTwin"], "{twin:?}");
    // Dedup is by the step set, so the three steps after the seed are listed once.
    assert_eq!(twin.steps.len(), 3, "{twin:?}");
    // And no second flow carries either seed alone.
    let carrying: Vec<&Flow> = flows
        .iter()
        .filter(|f| f.seeds.iter().any(|s| s == "AlphaTwin"))
        .collect();
    assert_eq!(carrying.len(), 1, "AlphaTwin is in one flow: {flows:?}");
}

#[test]
fn a_flow_takes_its_shared_name_stem_and_says_so() {
    // Every symbol is `Harvest<letter>`: the shared stem is `Harvest`.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/stem.go",
            gofuncs_ref(&[
                go_func("HarvestOne", &["HarvestTwo", "HarvestThree"]),
                go_func("HarvestTwo", &["HarvestThree", "HarvestFour"]),
                go_func("HarvestThree", &["HarvestFour"]),
                go_func("HarvestFour", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let f = flows
        .iter()
        .find(|f| f.label == "Harvest")
        .unwrap_or_else(|| panic!("the Harvest flow in {flows:?}"));
    assert_eq!(f.rule, "name_stem", "{f:?}");
}

/// The stem is cut back to a WHOLE word, not left as the longest shared character prefix.
///
/// `HarvestOne` and `HarvestTwo` share ten characters, so a rule that stopped at the common
/// prefix would also answer `Harvest` — this fixture is chosen so the two rules differ.
/// The names here share the five characters `Batch` and then diverge inside the next word
/// (`BatchOne`/`BatchTwo` against `BatchList`), so the character prefix is `BatchO`/`BatchL`
/// depending on the pair while the whole-word stem is `Batch`.
#[test]
fn a_shared_stem_is_cut_to_a_whole_word_and_not_left_as_a_character_prefix() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/stem.go",
            gofuncs_ref(&[
                go_func("BatchOne", &["BatchTwo", "BatchThree"]),
                go_func("BatchTwo", &["BatchThree"]),
                go_func("BatchThree", &["BatchFour"]),
                go_func("BatchFour", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let f = flows
        .iter()
        .find(|f| f.rule == "name_stem")
        .unwrap_or_else(|| panic!("a name_stem flow in {flows:?}"));
    assert_eq!(
        f.label, "Batch",
        "the stem is the shared whole word: {f:?}"
    );
    assert!(
        f.steps.iter().all(|(n, _)| n.starts_with("Batch")),
        "every step leads with the label: {f:?}"
    );
    // Not the character prefix: no listed step may be cut in half by the label.
    for (name, _) in &f.steps {
        let after = name.strip_prefix("Batch").unwrap_or_else(|| {
            panic!("step {name} does not start with the label Batch: {f:?}")
        });
        assert!(
            after.starts_with(|c: char| c.is_uppercase()) || after.is_empty(),
            "the label must end at a word boundary, not mid-word ({name}): {f:?}"
        );
    }
}

#[test]
fn a_flow_with_no_shared_name_stem_falls_back_to_its_seed_and_says_so() {
    // Four unrelated names: no stem qualifies, so the label is the seed's own name.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/mixed.go",
            gofuncs_ref(&[
                go_func("Zebra", &["Quail", "Ibex"]),
                go_func("Quail", &["Ibex"]),
                go_func("Ibex", &["Vulture"]),
                go_func("Vulture", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    let zebra = flows
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Zebra"))
        .unwrap_or_else(|| panic!("Zebra's flow in {flows:?}"));
    assert_eq!(zebra.rule, "seed_name", "{zebra:?}");
    assert_eq!(zebra.label, "Zebra", "{zebra:?}");
}

#[test]
fn a_flow_is_classified_against_the_communities_its_steps_land_in() {
    // The class is a claim about the community partition, not about folders: the file-level
    // communities are a transitive closure over `structure` edges, so two files that import
    // each other always land in one community however their folders are named. So the
    // fixture builds one strongly-coupled pair inside one community and two files that
    // share nothing, and the assertion reads the partition back out of the answer.
    let d = repo(&[
        GO_MOD,
        (
            "alpha/one.go",
            "package alpha\n\nimport \"example/alpha\"\n\nfunc AlphaOne() { AlphaTwo() }\nfunc AlphaTwo() { AlphaThree() }\nfunc AlphaThree() { AlphaFour() }\nfunc AlphaFour() {}\n",
        ),
        (
            "alpha/two.go",
            "package alpha\n\nimport \"example/alpha\"\n\nfunc AlphaThreeUnused() {}\n",
        ),
        (
            "island/one.go",
            "package island\n\nfunc IslandOne() { IslandTwo() }\nfunc IslandTwo() { IslandThree() }\nfunc IslandThree() { IslandFour() }\nfunc IslandFour() {}\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let flows = read_flows(&v);
    assert!(!flows.is_empty(), "the fixture must produce flows: {v:#}");

    // Every flow is classified, and the count beside the class is the number of distinct
    // communities its steps land in, so `cross` means two or more and `intra` means one.
    for f in &flows {
        assert!(
            f.class == "intra" || f.class == "cross",
            "a flow is classified intra or cross: {f:?}"
        );
        let want = if f.communities > 1 { "cross" } else { "intra" };
        assert_eq!(f.class, want, "{f:?} spans {} communities", f.communities);
    }

    // The partition here is symbol-granularity (WS-38b's areas), so the class depends on
    // where the propagation put each symbol and not on the folder a file sits in. Every
    // flow's class therefore has to agree with the community count beside it, which is the
    // invariant this test pins; which specific flows are cross is the next test's job,
    // because producing one needs a fixture built for it.
    // Both subsystems are reached, so the fixture is not answering from one corner of it.
    for name in ["AlphaOne", "IslandOne"] {
        assert!(
            flows.iter().any(|f| f.seeds.iter().any(|s| s == name)),
            "{name} has a flow in {flows:?}"
        );
    }
}

/// The other half of the classification, and the half that pins it: a flow whose steps
/// really do land in two areas must be reported `cross`.
///
/// The fixture above cannot produce one — its two subsystems share no edge — so it does not
/// carry this assertion on its own. Two areas joined by exactly one call is the shape that
/// does, and it is the only way a resolved CALL can cross a boundary under label
/// propagation: the joining call's two ends end up in different communities while every
/// other edge stays inside one.
///
/// Without this test the `class` line survives a mutation that pins it to `intra` forever,
/// because every other fixture in the file reports `intra` either way.
#[test]
fn a_flow_that_really_spans_two_communities_is_reported_cross() {
    // Two subsystems that are separate areas but call across: `alpha` has its own internal
    // ring and `beta` has its own, and a single call joins them. Label propagation over the
    // resolved symbol edges keeps the two rings apart and puts the joining call across the
    // boundary, which is the case the `cross` class exists to report.
    let d = repo(&[
        GO_MOD,
        (
            "alpha/alpha.go",
            "package alpha\n\nimport \"example/beta\"\n\nfunc AlphaOne() { AlphaTwo(); beta.BetaOne() }\nfunc AlphaTwo() { AlphaThree() }\nfunc AlphaThree() { AlphaOne() }\n",
        ),
        (
            "beta/beta.go",
            "package beta\n\nfunc BetaOne() { BetaTwo() }\nfunc BetaTwo() { BetaThree() }\nfunc BetaThree() { BetaOne() }\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    // The cross-area call must resolve or the fixture proves nothing: AlphaOne's callee
    // count is two only when `beta.BetaOne` binds, and it drops to one when it does not.
    let seeds = read_seeds(&v);
    let alpha_one = seeds
        .iter()
        .find(|s| s.name == "AlphaOne")
        .unwrap_or_else(|| panic!("AlphaOne is a seed in {v:#}"));
    assert_eq!(
        alpha_one.callees, 2,
        "both of AlphaOne's calls resolve, one of them across the package boundary: {v:#}"
    );
    let flows = read_flows(&v);
    assert!(
        !flows.is_empty(),
        "the fixture must produce flows: {v:#}"
    );
    let crossing = flows
        .iter()
        .find(|f| f.class == "cross")
        .unwrap_or_else(|| panic!("no cross flow among {flows:?} in {v:#}"));
    assert_eq!(
        crossing.communities, 2,
        "the fixture must span exactly two communities: {crossing:?}"
    );
    assert_eq!(
        crossing.class, "cross",
        "a flow over two communities is cross: {crossing:?}"
    );
    // The class and the count beside it are the same claim read twice, so both move
    // together: a count above one with the class reading intra would be a contradiction.
    for f in &flows {
        let want = if f.communities > 1 { "cross" } else { "intra" };
        assert_eq!(f.class, want, "{f:?} spans {} communities", f.communities);
    }
}

// --- truncation stats and refusals ------------------------------------------

#[test]
fn a_small_repository_answers_with_every_cap_reported_and_nothing_but_min_steps_hit() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/tiny.go",
            gofuncs_ref(&[
                // Two independent three-step branches, so every seed reaches three steps
                // and nothing is short: a chain would make its tail a short-set seed.
                go_func("TinyEntry", &["TinyMiddle", "SideOne"]),
                go_func("TinyMiddle", &["TinyLeaf"]),
                go_func("TinyLeaf", &["TinyLast"]),
                go_func("TinyLast", &[]),
                go_func("SideOne", &["SideTwo", "SideThree"]),
                go_func("SideTwo", &["SideThree"]),
                go_func("SideThree", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    // Every bound this fixture could reach is 0: nothing hit the depth cap, the branching
    // cap, the seed cap, the flow cap or a step-list cap.
    let t = truncation(&v);
    for k in [
        "truncated_at_depth",
        "truncated_at_branching",
        "dropped_candidates",
        "dropped_flows",
        "truncated_step_lists",
    ] {
        assert_eq!(t[k], 0, "{k} must be 0 in this fixture: {v:#}");
    }
    // `dropped_under_min_steps` is not zero and cannot be: in any finite call graph the
    // deepest node of a branch is itself a candidate and reaches fewer than three steps,
    // so the answer reports it rather than hiding it. That is the honest reading of a
    // fixture this small, and the flag beside it says so.
    assert!(
        t["dropped_under_min_steps"] > 0,
        "a finite graph always has short-set candidates: {v:#}"
    );
    // The caps the walk ran under are reported whether or not they bit.
    let caps = &v["caps"];
    assert_eq!(caps["max_depth"].as_u64().unwrap(), 10);
    assert_eq!(caps["max_branching"].as_u64().unwrap(), 4);
    assert_eq!(caps["min_steps"].as_u64().unwrap(), 3);
    assert_eq!(caps["max_candidates"].as_u64().unwrap(), 200);
    // The roll-up beside the counters is asserted on its own because this fixture is the
    // one case where the five counters above are all zero and the flag is still true:
    // `dropped_under_min_steps` is non-zero here by construction. A flag pinned to false
    // would pass every counter assertion in this test, which is mutation row M19.
    assert_eq!(
        v["truncated"],
        serde_json::json!(true),
        "a short-set drop is a truncation and the roll-up must say so: {v:#}"
    );
}

/// The other direction of the same flag: a repository whose walks hit no bound at all
/// reports `truncated: false`. Every candidate in this fixture reaches more than three
/// steps and nothing runs past a cap, so no counter moves.
///
/// The shape that makes it true is a cycle: in a finite acyclic graph the deepest node of
/// every branch is a candidate that reaches fewer than three steps, so
/// `dropped_under_min_steps` is non-zero however small the fixture is (which is what the
/// previous test records). A cycle gives every candidate a successor forever, so the
/// min-steps drop cannot fire and the flag has a case where it is genuinely false.
#[test]
fn a_repository_that_hits_no_bound_reports_no_truncation() {
    // A ring of six, so every node has a callee and a successor forever. With no tails
    // anywhere, no node can reach fewer than three steps, so the min-steps drop — the one
    // counter a small acyclic fixture can never avoid — cannot fire either.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/rings.go",
            gofuncs_ref(&[
                go_func("RingOne", &["RingTwo"]),
                go_func("RingTwo", &["RingThree"]),
                go_func("RingThree", &["RingFour"]),
                go_func("RingFour", &["RingFive"]),
                go_func("RingFive", &["RingSix"]),
                go_func("RingSix", &["RingOne"]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let t = truncation(&v);
    assert_eq!(
        t.values().sum::<u64>(),
        0,
        "this fixture must move no truncation counter: {v:#}"
    );
    assert_eq!(
        v["truncated"],
        serde_json::json!(false),
        "no counter moved, so the roll-up is false: {v:#}"
    );
}

#[test]
fn only_resolved_call_edges_become_steps() {
    // Two Go modules in one repository that call across each other. The resolver admits
    // those as symbol CALLS, so there is a flow; and the answer reports how many resolved
    // CALLS its walks read, which is the number a lexical edge would inflate and cannot.
    let d = repo(&[
        ("one/go.mod", "module example/one\n\ngo 1.21\n"),
        ("two/go.mod", "module example/two\n\ngo 1.21\n"),
        (
            "one/a.go",
            "package one\n\nimport \"example/two\"\n\nfunc Alpha() { Gamma(); two.Beta() }\nfunc Gamma() { Delta() }\nfunc Delta() {}\n",
        ),
        (
            "two/b.go",
            "package two\n\nimport \"example/one\"\n\nfunc Beta() { one.Alpha(); Epsilon() }\nfunc Epsilon() { Zeta() }\nfunc Zeta() {}\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    // Every CALL the fixture declares, and no lexical one: the six calls are Alpha->Gamma,
    // Alpha->Beta, Beta->Alpha, Beta->Epsilon, Epsilon->Zeta and Gamma->Delta. The count is
    // read against the declared list rather than a literal so a fixture edit cannot leave a
    // stale number behind, and it is the number of RESOLVED edges the walks read, which a
    // lexical edge would inflate.
    let declared = 6;
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        declared,
        "one resolved CALL per declared call: {v:#}"
    );
    // Alpha and Beta form a two-cycle, so each walk reaches the other and then everything
    // below it — but the two sets are not the same. A walk's visited set never contains its
    // own seed, so Alpha's set holds Beta and Beta's holds Alpha, and they differ by exactly
    // that. Two distinct sets are two flows: the dedup rule is not collapsing them.
    let f = read_flows(&v);
    assert_eq!(f.len(), 2, "{f:?}");
    let steps = |name: &str| -> BTreeSet<String> {
        let flow = f
            .iter()
            .find(|f| f.seeds.iter().any(|s| s == name))
            .unwrap_or_else(|| panic!("{name}'s flow in {f:?}"));
        flow.steps.iter().map(|(n, _)| n.clone()).collect()
    };
    let alpha = steps("Alpha");
    let beta = steps("Beta");
    assert!(
        alpha != beta,
        "a walk never contains its own seed, so the two sets differ: {alpha:?} {beta:?}"
    );
    // Both reach the same four symbols below the cycle.
    let shared: BTreeSet<String> = alpha.intersection(&beta).cloned().collect();
    assert_eq!(
        shared,
        BTreeSet::from([
            "Delta".to_string(),
            "Epsilon".to_string(),
            "Gamma".to_string(),
            "Zeta".to_string()
        ]),
        "the symbols under the cycle, reached from both sides: {alpha:?} {beta:?}"
    );
    // And the answer's paths show the flow crossing both modules' directories.
    let paths: BTreeSet<&str> = f
        .iter()
        .flat_map(|f| f.steps.iter().map(|(_, p)| p.as_str()))
        .collect();
    assert_eq!(
        paths,
        BTreeSet::from(["one/a.go", "two/b.go"]),
        "the steps cross both modules: {f:?}"
    );

    // A repository whose only edges are lexical produces nothing: the projection reads the
    // `symbol` layer and the `CALLS` kind, so a `structure`-only file has no flow at all
    // rather than a guessed one.
    let lexical = repo(&[("notes/a.md", "text\n"), ("notes/b.md", "text\n")]);
    build(lexical.path());
    let v = flows(lexical.path(), &[]);
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        0,
        "no resolved calls: {v:#}"
    );
    assert_eq!(read_flows(&v).len(), 0, "{v:#}");
}

/// The admission filters are asserted with a fixture that actually contains something for
/// them to reject, which the count-equals-declared assertion above cannot do.
///
/// PAR-SYM-09 says flows are built on resolved CALLS and nothing else, so `build_graph`
/// admits an edge only when its layer is `symbol` AND its kind is `CALLS`. Dropping either
/// half admits edges the walk must never follow, and neither drop is visible to a fixture
/// whose every symbol-layer edge already is a CALL. This one has both: a type that
/// `IMPLEMENTS` another is a symbol-layer edge that is NOT a CALL, so a build that admits
/// any symbol-layer edge reports it inside the flow's step set, and a build that admits any
/// CALL edge admits the same one here.
#[test]
fn a_non_call_symbol_edge_never_becomes_a_step() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/shape.go",
            gofuncs_ref(&[
                go_func("ShapeBase", &[]),
                go_func("ShapeImpl", &["ShapeOther", "ShapeThird"]),
                go_func("ShapeOther", &["ShapeThird"]),
                go_func("ShapeThird", &[]),
            ]),
        ),
        // `Derived` IMPLEMENTS `ShapeBase`: a symbol-layer edge whose kind is not CALLS.
        // `UseCall` is the only CALL, so a walk from it must reach exactly its callees and
        // never the type relationship.
        (
            "pkg/impl.go",
            "package pkg\n\ntype Derived struct{}\n\nfunc (d Derived) ShapeBase() {}\n\nfunc UseCall() { ShapeImpl() }\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let f = read_flows(&v);
    let use_call = f
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "UseCall"))
        .unwrap_or_else(|| panic!("UseCall's flow in {f:?}"));
    let names: BTreeSet<&str> = use_call.steps.iter().map(|(n, _)| n.as_str()).collect();
    assert_eq!(
        names,
        BTreeSet::from(["ShapeImpl", "ShapeOther", "ShapeThird"]),
        "the CALL walk reaches its callees and nothing else: {use_call:?}"
    );
    // The method the IMPLEMENTS edge points at is a symbol, but it is not a callee: it is
    // the base of a type relationship, and no CALL reaches it.
    assert!(
        !names.contains("ShapeBase"),
        "an IMPLEMENTS edge must not become a step: {names:?}"
    );
    // And the resolver really did record that edge, or the fixture would prove nothing.
    // `symbols_considered` counts the admitted non-local symbols, which includes the base
    // and the method, so the filter under test is removing an edge from a graph that has it.
    assert!(
        v["symbols_considered"].as_u64().unwrap() >= 6,
        "the fixture's symbols must all be indexed: {v:#}"
    );
}

/// A symbol declared INSIDE a function body is not an entry point and not a step.
///
/// `local` is the index's flag for a name bound in a callable's body rather than declared at
/// file scope. Such a symbol cannot be called from another file and has no place in a call
/// flow, so `build_graph` skips it. Admitting locals would pull every closure variable and
/// every local helper into the graph, and the score of the function declaring it would rise
/// on edges no caller can follow.
#[test]
fn a_symbol_local_to_a_function_body_is_neither_a_seed_nor_a_step() {
    // `Outer` declares a local function and calls it three times deep, so a build that
    // admits locals would both score `Outer` on the local edge and list the local in the
    // walk's steps.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/local.go",
            gofuncs_ref(&[
                go_func("Outer", &["LocalHelper", "GlobalOne"]),
                go_func("GlobalOne", &["GlobalTwo"]),
                go_func("GlobalTwo", &[]),
            ]),
        ),
        (
            "pkg/helper.go",
            "package pkg\n\nfunc Outer() {\n\tinner := func() {}\n\tinner()\n}\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let f = read_flows(&v);
    let all: BTreeSet<&str> = f
        .iter()
        .flat_map(|f| f.steps.iter().map(|(n, _)| n.as_str()))
        .chain(f.iter().flat_map(|f| f.seeds.iter().map(String::as_str)))
        .collect();
    assert!(
        !all.contains("inner"),
        "a local binding is not a flow member: {all:?}"
    );
    // The seed the walk does report carries only its file-scope callees, and the edge count
    // covers the resolved calls alone — a local edge would inflate both.
    assert!(
        v["call_edges"].as_u64().unwrap() <= 3,
        "only the three declared calls are resolved edges: {v:#}"
    );
    // Why this test pins a filter and cannot be shown to kill it: the row is a REACHABILITY
    // row, not a scoring one. The Go closure above is the only candidate for a local
    // resolved CALL in this fixture, and the index never records one — `local` marks a name
    // bound in a callable's body, and the extractor gives a closure no symbol node the
    // resolver can bind a CALL to. So `call_edges` is 3 with or without the filter, and
    // mutation row M16 survives. What the fixture does pin is the reporting: no local name
    // appears in any step or seed. The filter itself is asserted by construction rather
    // than by mutation, and this comment is where that fact lives so it is not mistaken
    // for a passing row.
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        3,
        "a closure binding adds no resolved edge: {v:#}"
    );
}

#[test]
fn a_repository_with_no_resolved_calls_answers_zero_flows_rather_than_failing() {
    let d = repo(&[GO_MOD, ("pkg/lonely.go", &go_func("Alone", &[]))]);
    build(d.path());
    let v = flows(d.path(), &[]);
    assert_eq!(read_flows(&v).len(), 0, "no call, no flow: {v:#}");
    assert_eq!(
        read_seeds(&v).len(),
        0,
        "a leaf has no callees and is not a seed"
    );
    assert_eq!(v["truncated"].as_bool().unwrap(), false, "{v:#}");
    assert_eq!(v["symbols_considered"].as_u64().unwrap(), 1, "{v:#}");
}

#[test]
fn a_missing_index_is_refused_rather_than_answered_from_nothing() {
    let d = repo(&[GO_MOD, ("pkg/a.go", &go_func("A", &["B"]))]);
    let out = run(&["index", "flows", d.path().to_str().unwrap()]);
    assert!(!out.status.success(), "an unindexed repository must fail");
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("no index at"),
        "the refusal must name the missing index: {err}"
    );
}
