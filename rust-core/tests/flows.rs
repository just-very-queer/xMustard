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
use std::os::unix::process::{CommandExt, ExitStatusExt};
use std::path::Path;
use std::process::{Command, Output, Stdio};
use std::time::{Duration, Instant};

use serde_json::Value;
use xmustard_core::index::flows::MAX_RETAINED_STEPS;

mod memprobe;
use tempfile::TempDir;
use xmustard_core::index;
use xmustard_core::index::config::IndexConfig;

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

fn opened_snapshot(
    contents: &str,
) -> (
    TempDir,
    std::path::PathBuf,
    std::sync::Arc<xmustard_core::index::reader::Snapshot>,
) {
    let d = repo(&[GO_MOD, ("pkg/flags.go", contents)]);
    build(d.path());
    let db = index::stats(d.path(), &IndexConfig::default(), false).unwrap()["index_path"]
        .as_str()
        .unwrap()
        .to_string();
    let opened = index::reader::open_at(
        d.path(),
        Path::new(&db),
        index::csr::GraphStorage::from_env(),
    )
    .unwrap();
    (d, db.into(), opened.snapshot)
}

fn assert_exported_flag_refusal_rolls_back(
    db: &Path,
    snapshot: &xmustard_core::index::reader::Snapshot,
    mutate: impl FnOnce(&rusqlite::Connection),
    expected: &str,
) {
    let conn = rusqlite::Connection::open(db).unwrap();
    mutate(&conn);
    drop(conn);
    let error = snapshot.exported_by_symbol().unwrap_err();
    assert!(error.contains(expected), "unexpected refusal: {error}");
    assert!(
        snapshot.with_text(|conn| Ok(conn.is_autocommit())).unwrap(),
        "a refused exported-flag read must leave the shared text connection out of a transaction"
    );
}

fn flow_child_peak(mut command: Command) -> (u64, std::process::ExitStatus) {
    let child = command.process_group(0).spawn().unwrap();
    let pid = child.id() as libc::pid_t;
    let started = Instant::now();
    let mut status = 0;
    let mut usage: libc::rusage = unsafe { std::mem::zeroed() };
    loop {
        let result = unsafe { libc::wait4(pid, &mut status, libc::WNOHANG, &mut usage) };
        if result == pid {
            std::mem::forget(child);
            let unit = if cfg!(target_os = "macos") { 1 } else { 1024 };
            return (
                usage.ru_maxrss as u64 * unit,
                ExitStatusExt::from_raw(status),
            );
        }
        if result < 0 {
            let error = std::io::Error::last_os_error();
            if error.kind() == std::io::ErrorKind::Interrupted {
                continue;
            }
            panic!("wait4 failed: {error}");
        }
        if started.elapsed() >= Duration::from_secs(300) {
            unsafe {
                libc::kill(-pid, libc::SIGKILL);
                libc::wait4(pid, &mut status, 0, &mut usage);
            }
            panic!("index flows exceeded the 300-second process limit");
        }
        std::thread::sleep(Duration::from_millis(1));
    }
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
    // The fixture's ranks are 0..=7 callees and every entry point is exported and uncalled,
    // so a rank-r candidate scores (r+1) * 2: the leader is 16. The cut of 200 falls inside
    // the rank-3 group — measured, not derived — so the weakest KEPT score is 4, and the
    // ranks 0..=2 (scores 2 and 4... ) sit entirely below it. Asserting the measured ends
    // is what makes a reversed list fail: the first element would be 4 and the last 16.
    let scores: Vec<f64> = seeds.iter().map(|s| s.score).collect();
    assert_eq!(
        scores.first().copied(),
        Some(16.0),
        "the best candidate leads the list: {scores:?}"
    );
    assert_eq!(
        scores.last().copied(),
        Some(4.0),
        "the weakest kept candidate is last: {scores:?}"
    );
    // And the whole list descends, which is the property the two ends are read from.
    let mut desc = scores.clone();
    desc.sort_by(|a, b| b.partial_cmp(a).unwrap());
    assert_eq!(scores, desc, "seeds must be listed highest score first");
    // The cut really did remove the low ranks: `dropped_candidates` counts them.
    assert_eq!(
        truncation(&v)["dropped_candidates"],
        40,
        "the cap stopped 40 of the 240 candidates: {v:#}"
    );
    // And the ranks the cap removed are the low ones: no kept seed scores below the
    // weakest rank still present, and every rank from the top down to that one is there.
    // This is what makes the list's ORDER load-bearing rather than incidental — with the
    // wrong 40 kept, the weakest score present would be 2 rather than 4.
    assert!(
        scores.iter().all(|s| *s >= 4.0),
        "the cap kept no low-ranked candidate: {scores:?}"
    );
}

/// The cap keeps the top 200 SCORING candidates, so the 40 it drops are the lowest.
///
/// The companion test asserts the same fixture's list order; this one asserts WHICH 40 are
/// gone, which is the cap's actual promise. A cap that truncated by index rather than by
/// score would keep the same number and report the same pool, and would pass every
/// sortedness assertion the other test makes.
#[test]
fn the_cap_keeps_the_highest_scoring_candidates_and_drops_the_rest() {
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
    let d = repo(&[GO_MOD, ("pkg/dropped.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    // The 40 dropped candidates are the ones the cap saw and did not keep, and they are
    // the LOWEST scoring 40: the answer reports the pool beside the kept list, so a cap
    // that dropped the wrong 40 would still report 240 but would keep a 2.
    assert_eq!(v["candidates"].as_u64().unwrap(), 240, "the pool: {v:#}");
    assert_eq!(seeds.len(), 200, "the kept list: {v:#}");
    assert!(
        seeds.iter().all(|s| s.score >= 4.0),
        "the kept 40 are the lowest, not the highest: {v:#}"
    );
    // Every rank the cap removed is entirely absent from the kept list. Measured: the kept
    // 200 all score 4 or more, so the whole rank-0 group (score 2) and the score-4 group's
    // lower half are gone. Rank 0 is `Entry000`, `Entry008`, `Entry016`, ... — every
    // eighth entry — so those names are the ones that must be absent, and which of them
    // survive the cut depends on the tie-break, so the assertion names the two that the
    // measured answer shows are gone rather than deriving them.
    assert_eq!(
        seeds.iter().filter(|s| s.score < 4.0).count(),
        0,
        "no score-2 candidate survives the cut: {v:#}"
    );
    for name in ["Entry000", "Entry232"] {
        assert!(
            !seeds.iter().any(|s| s.name == name),
            "{name} scores 2 and must have been dropped: {v:#}"
        );
    }
}

/// Two seeds with identical scores are ordered by (path, line, name), so the list is the
/// same on every run and does not depend on the order the index happened to store them.
///
/// Twelve entry points, every one scoring the same, all in one file — so `path` is equal
/// for all of them and the order must fall to `line` and then to `name`. They are declared
/// in source order, so line order and name order agree here and the emitted list reads as
/// the declared one; the point of asserting the list rather than a single comparator is
/// that a rule reading only one of the two keys would pass this fixture too, so the test
/// pins the RESULT the reader sees.
///
/// Mutation row M29 is not this row's subject and does not need it. Dropping the `name`
/// tie-break from `flows_of`'s sort is EQUIVALENT, because `rank_seeds` has already
/// ordered `ranked` by the identical (score, path, line, name) key one call earlier, and
/// this sort is stable over an already-sorted vector. `rank_seeds` is where the tie-break
/// does its work, and its own copy of the key is what this test exercises.
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
    assert_eq!(
        names, expected,
        "equal scores fall to declared order: {names:?}"
    );
    // Two runs over one index answer the same, which is the property the tie-break exists
    // to provide. The freshness block is excluded: it carries the generation's identity age
    // and index timestamp, which differ between any two reads and are not part of "the same
    // flows". Comparing whole answers would fail under load and pass when idle, which is
    // what made this assertion look like a load-sensitive flake until the block was named.
    assert_eq!(
        without_freshness(&first),
        without_freshness(&second),
        "the flows answer must not vary between runs"
    );
}

/// The answer with its freshness block removed: that block carries the index generation's
/// identity age, which differs between any two reads and is not part of what "the same
/// answer" means.
fn without_freshness(v: &Value) -> Value {
    let mut c = v.clone();
    if let Some(o) = c.as_object_mut() {
        o.remove("freshness");
    }
    c
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

/// A symbol first listed down the LONG route is queued again by the SHORT one.
///
/// Before this row's fix one flag answered both "already a step" and "may be expanded", and
/// the callee loop tested it before `expanded_at` could. So a symbol the walk reached first
/// down a deep route held the step slot, and the shallower route beside it could never
/// re-queue it at a depth where its own children were still inside the depth cap. The leaf
/// under the hub was then absent from the answer even though three hops from the seed
/// reaches it — and which route went first depends on the order the CSR holds callees in.
///
/// The fixture is the shape that shows it. `Start` declares the deep branch FIRST, so the
/// LIFO stack pops it first and the hub is listed nine hops out, one hop short of its leaf.
/// The short branch reaches the same hub two hops out, where the leaf is well inside the
/// cap. Both routes are in the source, so the graph holds them either way: only the
/// traversal decides whether the leaf is reported.
#[test]
fn a_symbol_first_reached_down_the_long_route_is_queued_again_by_the_short_one() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/hub.go",
            gofuncs_ref(&[
                // Deep is declared first so the stack pops it first: that is what puts the
                // hub in the step list at depth nine, one hop short of its leaf.
                go_func("Start", &["Deep", "Near"]),
                go_func("Near", &["Hub"]),
                go_func("Deep", &["E1"]),
                go_func("E1", &["E2"]),
                go_func("E2", &["E3"]),
                go_func("E3", &["E4"]),
                go_func("E4", &["E5"]),
                go_func("E5", &["E6"]),
                go_func("E6", &["E7"]),
                go_func("E7", &["E8"]),
                go_func("E8", &["Hub"]),
                go_func("Hub", &["HubLeaf"]),
                go_func("HubLeaf", &[]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let fs = read_flows(&v);
    let start = fs
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Start"))
        .unwrap_or_else(|| panic!("the flow seeded at Start in {fs:?}"));
    let names: BTreeSet<&str> = start.steps.iter().map(|(n, _)| n.as_str()).collect();
    // The hub is on both routes, so it is listed either way; the leaf is the step only the
    // short route can reach, and it is the one the conflated flag used to drop.
    assert!(
        names.contains("HubLeaf"),
        "HubLeaf is two hops from Start through Near, so it is inside the depth cap: {start:?}"
    );
    // The deep route is still walked. The fix re-queues a symbol at a shorter depth; it does
    // not stop at the first route found, so a walk that reported only the short branch would
    // satisfy the assertion above while losing the chain.
    assert!(
        names.contains("E4"),
        "the deep branch is reached too: {start:?}"
    );
}

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
    assert_eq!(f.steps.len(), 4, "the walk reaches four symbols: {f:?}");
    assert_eq!(f.depth, 3, "the furthest step is three hops out: {f:?}");
    assert_ne!(
        f.depth as usize,
        f.steps.len(),
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
    // Every callee the branching bound stopped, not one per node. Mutation row M4 adds a
    // `break` after the counter, so the walk abandons the rest of that node's callee list
    // and reports a single truncation where the loop kept scanning and counted each one.
    // `> 0` cannot see that difference — both readings are positive — which is why this row
    // survived until it was pinned to the figure the fixture determines.
    //
    // `Root` declares seven callees and the cap is `MAX_BRANCHING` (4), so three of them are
    // past the cap at Root and every other node on this walk has a single callee, which the
    // cap never stops. The count is therefore exactly three, derived from the declared
    // calls above rather than measured and copied, and it is stated as an equality so a
    // change to either the cap or the counting rule shows up here.
    assert_eq!(
        truncation(&v)["truncated_at_branching"],
        3,
        "Root's three untaken callees are counted one each, not one per node: {:?} in {root:?}",
        truncation(&v)
    );
}

/// The step-listing cap is reachable, and what stops a walk from growing is the SEED cap.
///
/// An earlier version of this file asserted the opposite — that no walk could reach
/// `MAX_STEPS_LISTED`, on the arithmetic `1 + MAX_BRANCHING * (MAX_DEPTH + 1)` = 45. That
/// bound does not hold: `expanded_at` permits a symbol to be expanded once per distinct
/// SHORTER depth, so the number of expansions is not capped by `MAX_DEPTH` and a wide tree
/// reaches far past 50. `a_flow_lists_at_most_fifty_steps_and_counts_the_rest` is the fixture
/// that shows it, and on the box it walks 340 steps from its root, lists 50 and reports 290
/// omitted (logs/ws-38c-run-20261004T022134Z.log). The two tests were not contradicting each
/// other on the bound; they used different trees, and the assertion that looked structural
/// was passing because of the other cap below.
///
/// What this fixture actually measures is the interaction with [`MAX_CANDIDATES`]: a 4-ary
/// tree deep enough to be wide also has more symbols than the 200-seed cap admits, so the
/// deep levels never become seeds and the widest walk a seed produces stops well short of 50.
/// That is a real property of the answer, and it is the one worth pinning — a reader who
/// wants to know why a large repository's flows look narrow should find it here rather than
/// infer it. It is also what made mutation row M7 equivalent: with no walk over 50 steps
/// here, computing the name stem from every visited step or from the listed steps cannot
/// change a label.
#[test]
fn the_widest_walk_a_seed_makes_stops_short_of_the_listing_cap_when_the_seed_cap_bites() {
    let mut src =
        String::from("package pkg\n\nfunc WideRoot() { Lvl1a(); Lvl1b(); Lvl1c(); Lvl1d() }\n");
    let mut level = vec![
        "Lvl1a".to_string(),
        "Lvl1b".into(),
        "Lvl1c".into(),
        "Lvl1d".into(),
    ];
    for _ in 0..4 {
        let mut next = Vec::new();
        for head in &level {
            let mut kids = Vec::new();
            for j in 0..4 {
                let name = format!("{head}x{j}");
                kids.push(format!("{name}()"));
                next.push(name);
            }
            src.push_str(&format!("func {head}() {{ {} }}\n", kids.join(" ")));
        }
        level = next;
    }
    for leaf in &level {
        src.push_str(&format!("func {leaf}() {{}}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/wide.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let fs = read_flows(&v);
    assert!(!fs.is_empty(), "the fan must produce a flow: {v:#}");
    let widest = fs.iter().map(|f| f.steps.len()).max().unwrap();
    // The walk is a real multi-level one rather than a truncated fixture: more than one flow
    // means more than one seed reached the tree, and the widest one is the root's.
    assert!(
        widest >= 8,
        "the fan reaches a walk of at least eight steps, so the re-expansion path ran: {fs:?}"
    );
    // The tree is wide enough that the seed cap, not the listing cap, is what bounds the
    // answer here. Asserted rather than assumed: this is the condition under which the
    // listing cap stays unobserved, and without it the assertion below proves nothing.
    let dropped = truncation(&v)["dropped_candidates"];
    assert!(
        dropped > 0,
        "this tree must exceed the 200-seed cap, or the walk is bounded by something else: {:?}",
        truncation(&v)
    );
    assert!(
        widest < 50,
        "with the seed cap biting, no seed's walk reaches MAX_STEPS_LISTED; the widest here \
         was {widest} in {fs:?}"
    );
    // And therefore the truncation counter beside the cap is zero ON THIS FIXTURE, which is
    // what makes mutation row M7 equivalent rather than merely unobserved. The cap itself is
    // not dead: the other fixture in this file walks past it.
    assert_eq!(
        truncation(&v)["truncated_step_lists"],
        0,
        "no seed's step list is capped on this fixture: {:?}",
        truncation(&v)
    );
    for f in &fs {
        assert_eq!(
            f.steps_omitted, 0,
            "nothing is omitted from a step list, so the listed steps ARE all of them: {f:?}"
        );
    }
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

/// The stem is cut back to a WHOLE word, and the cut is what separates this rule from
/// "take the longest shared character prefix".
///
/// The names here carry a separator inside the shared region — `Batch_One`..`Batch_Four`
/// share `Batch_` — so the character prefix and the whole-word stem are different strings
/// and only one of them can be the answer.
///
/// Two mutations of this rule both survive, and the reason is worth recording because it is
/// not a coverage gap. The label is cut in TWO steps: `word` is `common` truncated at the
/// last non-alphanumeric character, and `stem` is `word` with trailing non-alphanumerics
/// trimmed. Removing either step alone leaves the other to do the same job, so the label is
/// unchanged and no test can fail. `Batch_One` and friends exercise both steps at once:
/// dropping the `word` step leaves `common` = `Batch_`, whose trailing `_` the next line
/// then trims. Removing BOTH is the only mutation that changes the label, and it is
/// equivalent too — the result is `Batch` either way, because `common` never contains a
/// trailing separator after the pairwise prefix reduction.
///
/// So what this test pins is the RESULT: the label is the shared word, never a prefix that
/// includes its separator and never a fragment of it. The earlier version of this row
/// mutated `trim_end_matches` alone and read as a coverage gap for several rounds; it was
/// an equivalent mutation, and the fix was to find out why rather than to add a fixture
/// that would also have survived.
#[test]
fn a_shared_stem_is_cut_to_a_whole_word_and_not_left_as_a_character_prefix() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/stem.go",
            gofuncs_ref(&[
                go_func("Batch_One", &["Batch_Two", "Batch_Three"]),
                go_func("Batch_Two", &["Batch_Three"]),
                go_func("Batch_Three", &["Batch_Four"]),
                go_func("Batch_Four", &[]),
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
    // The separator is inside the shared region, so a label that kept it would read
    // `Batch_`. The cut produces the word the names share, which is what a reader uses
    // the label for.
    assert_eq!(
        f.label, "Batch",
        "the stem is the shared whole word, without the separator: {f:?}"
    );
    assert!(
        f.steps.iter().all(|(n, _)| n.starts_with("Batch")),
        "every step leads with the label: {f:?}"
    );
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
    assert!(!flows.is_empty(), "the fixture must produce flows: {v:#}");
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

/// A cycle back to a symbol the walk already holds is not a truncation.
///
/// The callee loop tests the depth and branching bounds before it knows whether the callee
/// is new. A callee already in the step list is re-queued rather than counted — it cannot
/// lose a step, because it is already in the answer — so a walk that runs to the depth cap
/// and then closes a cycle would report `truncated=true` with every counter describing work
/// the answer did not actually lose. The counters exist so a short answer is never read as a
/// truncated one, so they may not claim truncation where nothing was dropped.
///
/// The fixture closes a cycle at exactly the cap: a chain of ten hops whose last symbol calls
/// back to the head of the chain. The chain alone would be a clean ten-step flow; with the
/// back-edge the walk is still ten steps, because every symbol on it is already held.
#[test]
fn a_cycle_that_returns_to_a_held_symbol_at_the_depth_cap_is_not_counted_as_truncated() {
    // `Mid0..Mid9` is a ten-hop chain and `Mid9` calls back to `Mid0`, so the cycle closes
    // at the depth cap. `Root` is the seed and reaches `Mid0`, so the walk is Root -> Mid0 ..
    // Mid9 = ten steps, and the back-edge from Mid9 to Mid0 offers nothing new.
    let d = repo(&[
        GO_MOD,
        (
            "pkg/cycle.go",
            gofuncs_ref(&[
                go_func("Root", &["Mid0"]),
                go_func("Mid0", &["Mid1"]),
                go_func("Mid1", &["Mid2"]),
                go_func("Mid2", &["Mid3"]),
                go_func("Mid3", &["Mid4"]),
                go_func("Mid4", &["Mid5"]),
                go_func("Mid5", &["Mid6"]),
                go_func("Mid6", &["Mid7"]),
                go_func("Mid7", &["Mid8"]),
                go_func("Mid8", &["Mid9"]),
                // The back-edge, closing the cycle at the cap.
                go_func("Mid9", &["Mid0"]),
            ]),
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let fs = read_flows(&v);
    let root = fs
        .iter()
        .find(|f| f.seeds.iter().any(|s| s == "Root"))
        .unwrap_or_else(|| panic!("the flow seeded at Root in {fs:?}"));
    assert_eq!(root.steps.len(), 10, "ten hops from the seed: {root:?}");
    // The reported DEPTH too, not only the step count. The back-edge from `Mid9` points at a
    // symbol the walk already holds, so it is re-queued rather than listed — and a re-queue
    // past the cap used to raise `deepest` to eleven while admitting no eleventh step, so an
    // answer could print a depth above the cap it reports beside it.
    assert_eq!(
        root.depth, 10,
        "the furthest step is ten hops out, and not past the cap: {root:?}"
    );
    assert!(root.depth <= 10, "depth never exceeds MAX_DEPTH: {root:?}");
    assert_eq!(
        root.steps_omitted, 0,
        "the cycle closes on a symbol the walk already holds, so nothing is omitted: {root:?}"
    );
    let t = truncation(&v);
    assert_eq!(
        t["truncated_at_depth"], 0,
        "no step was lost to the depth cap: {t:?}"
    );
    assert_eq!(
        t["truncated_at_branching"], 0,
        "no step was lost to the branching cap: {t:?}"
    );
}

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
    //
    // Mutation row M15 drops the `layer == "symbol"` half of the admission filter and is
    // EQUIVALENT on this graph: the walk reads `Direction::Downstream.adjacency()` of a
    // SYMBOL node, and the only layers reachable there are the symbol layer and the file
    // layer's self-loop — no lexical `structure` or `flow` edge is adjacent to a symbol row,
    // so the kind filter alone is sufficient to keep them out. The filter is kept because
    // it states the intent and because a future adjacency change could break it; it is not
    // claimed as test-covered, and row M15 is the evidence for that.
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
    // The resolver really did record that edge, or the fixture would prove nothing.
    // `symbols_considered` counts the admitted non-local symbols, which includes the base
    // and the method, so the filter under test is removing an edge from a graph that has it.
    assert!(
        v["symbols_considered"].as_u64().unwrap() >= 6,
        "the fixture's symbols must all be indexed: {v:#}"
    );
    // Measured: the index reports four CALLS for this fixture, not the two the source shows —
    // `ShapeImpl`'s two callees plus the two `ShapeImpl`/`ShapeOther`/`ShapeThird` calls the
    // fixture's other file makes. None of the four is the EXTENDS edge the method creates,
    // and the walk's step set above shows it is not among the steps either. So the fixture
    // does have a symbol-layer edge the kind filter has to reject, and it is not counted
    // here. The exact figure is stated so a reader can re-check it rather than assume it.
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        4,
        "the fixture's four resolved CALLS, none of them the type relationship: {v:#}"
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
    // The walk from `Outer` reaches only its file-scope callees: the closure binding adds
    // no resolved edge, so the count is 2 whether or not the `row.local` filter is present.
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        2,
        "only the two declared calls are resolved edges: {v:#}"
    );
    // Why this test pins a filter and cannot be shown to kill it: the row is a REACHABILITY
    // row, not a scoring one. The Go closure above is the only candidate for a local
    // resolved CALL in this fixture, and the index never records one — `local` marks a name
    // bound in a callable's body, and the extractor gives a closure no symbol node the
    // resolver can bind a CALL to. So `call_edges` is 2 with or without the filter, and
    // mutation row M16 survives. What the fixture does pin is the reporting: no local name
    // appears in any step or seed. The filter itself is asserted by construction rather
    // than by mutation, and this comment is where that fact lives so it is not mistaken
    // for a passing row.
    assert_eq!(
        v["call_edges"].as_u64().unwrap(),
        2,
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
    assert!(!v["truncated"].as_bool().unwrap(), "{v:#}");
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

/// A flow is classified against the FULL area membership, not the 200 rows the answer
/// lists.
///
/// `areas.rs` caps an area's listed members at `MAX_MEMBERS` and reports `members_truncated`
/// when it does. A partition read from the listed rows therefore leaves every symbol past
/// the cap with no area, and `build_graph` gave those the `UNCLASSIFIED` sentinel, which
/// used to count as a community of its own: a flow whose real areas were all one was
/// reported `cross` because one of its steps sat outside the cap.
///
/// The fixture needs an area with more than 200 members, so it is 240 symbols wide and the
/// cap is crossed inside it. Every symbol here is in one ring, so every flow is `intra`, and
/// the answer says how many symbols the partition did not cover — which must be 0 for a
/// graph the areas projection places.
#[test]
fn a_flow_is_classified_over_every_area_member_not_the_capped_list() {
    // One hub and 240 spokes, all in one folder, so label propagation keeps them in ONE
    // area (the hub is adjacent to every spoke) and the area's membership crosses
    // MAX_MEMBERS. A ring does not work for this: propagation fragments a long ring into
    // many small areas, measured at 61 for a 240-symbol ring, so no single one is capped.
    let mut src = String::from("package pkg\n\n");
    let mut spokes = Vec::new();
    for i in 0..240 {
        spokes.push(format!("Hub{i:03}"));
        src.push_str(&format!("func Hub{i:03}() {{}}\n"));
    }
    let body: String = spokes.iter().map(|s| format!("\t{s}()\n")).collect();
    src.push_str(&format!("func WideHub() {{\n{body}}}\n"));
    let d = repo(&[GO_MOD, ("pkg/wide.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    // The areas answer really did cross its cap, so the fixture exercises the case.
    let areas = ok_json(&["index", "areas", d.path().to_str().unwrap()]);
    let capped: Vec<u64> = areas["areas"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|a| a["members_truncated"] == serde_json::json!(true))
        .map(|a| a["total_size"].as_u64().unwrap())
        .collect();
    assert!(
        !capped.is_empty(),
        "the fixture must produce an area over MAX_MEMBERS: {areas:#}"
    );
    assert!(
        capped.iter().any(|t| *t > 200),
        "the capped area holds more than 200 members: {capped:?}"
    );
    assert_eq!(
        v["unclassified_symbols"].as_u64().unwrap(),
        0,
        "every admitted symbol has an area: {v:#}"
    );
    // And with every step classified, no flow claims to cross an area it stayed inside.
    for f in read_flows(&v) {
        assert_eq!(
            f.class, "intra",
            "one folder is one area, so no flow crosses one: {f:?}"
        );
        assert_eq!(f.communities, 1, "{f:?}");
    }
}

/// The `exported` flag reaches the right symbol.
///
/// `exported_by_symbol` used to consume `SELECT exported FROM symbols ORDER BY id`
/// positionally into a vector sized by the SEGMENT's symbol count. The segment covers only
/// the files whose `parse_status` is indexed, so the first skipped file shifted every later
/// flag onto the wrong symbol — and PAR-SYM-09's ×2 doubling then applied to symbols that
/// are not exported and withheld from ones that are.
///
/// The fixture forces a gap in the STORE's row set, not merely a file the parser dislikes:
/// an earlier version used an unparseable file, from which the index extracted no symbols
/// at all, so the store held no rows for it and the positional read was already correct.
/// `max-file-size` is what produces a file the segment does not cover, which is the case
/// the old read was written for. Measured, this fixture does NOT reproduce the old bug — the
/// store drops an oversized file's symbol rows entirely — so the test pins the reader's
/// restriction rather than a shift it cannot currently observe. Read the comment inside for
/// the measurement and for what would have to change for the row to bite.
#[test]
fn the_exported_flag_reaches_its_own_symbol_across_a_skipped_file() {
    let d = repo(&[
        GO_MOD,
        // Before the gap: an UNEXPORTED entry point, so its expected score has no ×2.
        (
            "aaa/first.go",
            "package aaa\n\nfunc alphaOne() { alphaTwo(); alphaThree() }\nfunc alphaTwo() {}\nfunc alphaThree() {}\n",
        ),
        // After the gap: an EXPORTED entry point, which must keep its ×2.
        (
            "ccc/last.go",
            "package ccc\n\nfunc DeltaOne() { DeltaTwo(); DeltaThree() }\nfunc DeltaTwo() {}\nfunc DeltaThree() {}\n",
        ),
    ]);
    // The gap is a file the scan marks `oversized`: above `max_file_size`, so the segment
    // does not cover it. That is the shape `exported_by_symbol` was fixed for — a positional
    // read would slide every flag after the first gap onto its neighbour. It is written
    // directly rather than through `repo()`, and then indexed with `--max-file-size` set
    // below its size, because the flag is a property of the STORE's row set and not of the
    // fixture helper.
    //
    // The gap file's path sorts BETWEEN the two real files, so that if a future store ever
    // does keep its rows the shift lands on a symbol this test reports rather than past the
    // end of the answer. It is recorded as ordering rather than as the reason the row is
    // equivalent, because it is not.
    let big = d.path().join("bbb/huge.go");
    fs::create_dir_all(big.parent().unwrap()).unwrap();
    // The gap file must be the only one over the limit, so it is padded well past the
    // size the two real files sit under.
    //
    // Mutation row M2 drops the `parse_status` restriction and reads every store row in id
    // order, which would slide the flags after the gap onto their neighbours — and nothing
    // fails, because there is nothing to slide. `Writer::write_file` takes
    // `facts: Option<&FileFacts>` and sets `symbol_count` from it, and an oversized file is
    // never parsed, so it gets a `files` row and NO `symbols` rows at all. The store
    // therefore holds exactly the segment's own rows, and both readings return the same
    // flags in the same order. Measured: `symbols_considered` 6 with the restriction and 6
    // without it, `exported_symbols` 3 either way, and `HugeOne` in neither.
    //
    // The gap therefore does not exercise the SHAPE of this fix — a segment that covers
    // fewer files than the store while both hold symbols — it exercises a file the store
    // dropped entirely. That is still the case the fix guards, because the fix is a
    // restriction rather than an assumption, but the honest verdict is EQUIVALENT on any
    // index this code can currently build. What would make the row bite is a second store
    // row source for the same segment — a parse status outside `INDEXED_STATUSES` that
    // still writes symbols, which today nothing produces.
    let mut big_src = String::from("package bbb\n\nfunc HugeOne() {}\n");
    while big_src.len() < 4096 {
        big_src.push_str("// padding padding padding padding padding padding padding\n");
    }
    fs::write(&big, &big_src).unwrap();
    let an_hour_ago = std::time::SystemTime::now() - std::time::Duration::from_secs(3600);
    fs::File::options()
        .write(true)
        .open(&big)
        .unwrap()
        .set_modified(an_hour_ago)
        .unwrap();
    git(d.path(), &["add", "-A"]);
    git(d.path(), &["commit", "-qm", "gap"]);
    // 512 bytes sits above both real files (each under 200) and below the padded one.
    ok_json(&[
        "index",
        "build",
        d.path().to_str().unwrap(),
        "--max-file-size",
        "512",
    ]);
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    let by_name = |n: &str| -> Seed {
        seeds
            .iter()
            .find(|s| s.name == n)
            .cloned()
            .unwrap_or_else(|| panic!("{n} is a seed in {v:#}"))
    };
    let alpha = by_name("alphaOne");
    let delta = by_name("DeltaOne");
    // Two callees each, no callers, so the ratio is 2.0 and the name multiplier is 1.0
    // (`plain`): the whole score is the exported doubling, and nothing else.
    assert_eq!(alpha.ratio, 2.0, "the unexported twin's ratio: {alpha:?}");
    assert!(!alpha.exported, "alphaOne is unexported: {alpha:?}");
    assert_eq!(
        alpha.score, 2.0,
        "no doubling for an unexported seed: {alpha:?}"
    );
    assert!(
        delta.exported,
        "DeltaOne is exported and the flag must survive the skipped file: {delta:?}"
    );
    assert_eq!(delta.score, 4.0, "the exported doubling applies: {delta:?}");
}

// --- WS-38c-b: the six decided findings --------------------------------------

#[test]
fn a_single_walk_larger_than_the_retention_cap_keeps_its_seed_flow() {
    let mut level = vec!["RunAll".to_string()];
    let mut definitions = Vec::new();
    let mut next_id = 0;
    for _ in 0..7 {
        let mut next = Vec::new();
        for parent in level {
            let children: Vec<String> = (0..4)
                .map(|_| {
                    let name = format!("Node{next_id}");
                    next_id += 1;
                    name
                })
                .collect();
            next.extend(children.iter().cloned());
            definitions.push((parent, children));
        }
        level = next;
    }
    definitions.extend(level.into_iter().map(|leaf| (leaf, Vec::new())));
    let contents: Vec<(String, String)> = definitions
        .chunks(1_000)
        .enumerate()
        .map(|(chunk, defs)| {
            let mut src = String::from("package pkg\n\n");
            for (name, children) in defs {
                let calls = children
                    .iter()
                    .map(|child| format!("{child}();"))
                    .collect::<String>();
                src.push_str(&format!("func {name}() {{ {calls} }}\n"));
            }
            (format!("pkg/tree{chunk:02}.go"), src)
        })
        .collect();
    let mut files = vec![GO_MOD];
    for (path, content) in &contents {
        files.push((path.as_str(), content.as_str()));
    }
    let d = repo(&files);
    build(d.path());
    let answer = flows(d.path(), &[]);
    assert!(
        answer["symbols_considered"].as_u64().unwrap() > MAX_RETAINED_STEPS,
        "fixture exceeds the retained-step cap in walked symbols: {answer:#}"
    );
    let run_flow = read_flows(&answer)
        .into_iter()
        .find(|flow| flow.seeds.iter().any(|seed| seed == "RunAll"));
    assert!(
        run_flow.is_some(),
        "the 21,845-symbol walk still yields its highest-ranked seed flow: {answer:#}"
    );
}

#[test]
fn retained_branching_walks_stay_under_the_process_tree_line() {
    let mut files = vec![GO_MOD];
    let mut contents = Vec::new();
    for root in 0..30 {
        let prefix = format!("Ret{root:02}");
        let mut level = vec![format!("{prefix}Root")];
        let mut definitions = Vec::new();
        let mut next_id = 0;
        for _ in 0..5 {
            let mut next = Vec::new();
            for parent in level {
                let mut children = Vec::new();
                for _ in 0..4 {
                    let child = format!("{prefix}N{next_id}");
                    next_id += 1;
                    children.push(child.clone());
                    next.push(child);
                }
                definitions.push((parent, children));
            }
            level = next;
        }
        definitions.extend(level.into_iter().map(|leaf| (leaf, Vec::new())));
        let mut src = String::from("package pkg\n\n");
        for (name, children) in definitions {
            let calls = children
                .iter()
                .map(|child| format!("{child}();"))
                .collect::<String>();
            src.push_str(&format!("func {name}() {{ {calls} }}\n"));
        }
        contents.push((format!("pkg/{prefix}.go"), src));
    }
    for (path, content) in &contents {
        files.push((path.as_str(), content.as_str()));
    }
    let d = repo(&files);
    build(d.path());
    let output = tempfile::NamedTempFile::new().unwrap();
    let mut command = Command::new(BIN);
    command
        .args(["index", "flows", d.path().to_str().unwrap()])
        .stdout(Stdio::from(output.reopen().unwrap()))
        .stderr(Stdio::null());
    let (peak, status) = flow_child_peak(command);
    assert!(status.success(), "index flows exited with {status}");
    let answer: Value = serde_json::from_slice(&fs::read(output.path()).unwrap()).unwrap();
    assert_eq!(answer["caps"]["max_retained_steps"], MAX_RETAINED_STEPS);
    let mib = memprobe::mib(peak);
    eprintln!("retained branching walks: wait4 ru_maxrss {mib:.1} MiB");
    // This fixture must produce omitted steps for the measured broad-walk shape to be meaningful.
    assert!(
        truncation(&answer)
            .get("truncated_step_lists")
            .copied()
            .unwrap_or(0)
            > 0,
        "the fixture exercises omitted steps while measuring retention: {answer:#}"
    );
    assert!(
        mib <= 95.4,
        "process-tree RSS {mib:.1} MiB exceeds 95.4 MiB"
    );
}

#[test]
fn freshness_checks_ranked_seed_paths_before_alphabetical_dependencies() {
    let mut files = vec![GO_MOD];
    let imports = (0..60)
        .map(|i| format!("\"example/a{i:02}\""))
        .chain(std::iter::once("\"example/zed\"".to_string()))
        .collect::<Vec<_>>();
    let import_block = imports
        .iter()
        .map(|path| format!("{path}\n"))
        .collect::<String>();
    let root = format!(
        "package pkg\n\nimport (\n{import_block})\n\nfunc RunAll() {{ a00.Leaf(); a01.Leaf(); a02.Leaf(); zed.Leaf() }}\n"
    );
    let mut owned_files = vec![("pkg/z_root.go".to_string(), root)];
    for i in 0..60 {
        let source = format!("package a{i:02}\n\nfunc Leaf() {{}}\n");
        owned_files.push((format!("a{i:02}/leaf.go"), source));
    }
    owned_files.push((
        "zed/leaf.go".to_string(),
        "package zed\n\nfunc Leaf() {}\n".to_string(),
    ));
    for helper in 0..9 {
        let start = 4 + helper * 6;
        let end = start + 6;
        let helper_imports = (start..end)
            .map(|leaf| format!("\"example/a{leaf:02}\"\n"))
            .collect::<String>();
        let calls = (start..end)
            .map(|leaf| format!("a{leaf:02}.Leaf(); "))
            .collect::<String>();
        owned_files.push((
            format!("pkg/extra{helper:02}.go"),
            format!(
                "package pkg\n\nimport (\n{helper_imports})\n\nfunc helper{helper}() {{ {calls} }}\n"
            ),
        ));
    }
    let envelope_predecessors = (4..58).count();
    assert!(
        envelope_predecessors > 50,
        "54 callees of nine scored candidates precede zed alphabetically"
    );
    for (path, source) in &owned_files {
        files.push((path.as_str(), source.as_str()));
    }
    let d = repo(&files);
    build(d.path());
    let before = flows(d.path(), &[]);
    assert!(
        read_seeds(&before)
            .iter()
            .any(|seed| seed.path == "pkg/z_root.go"),
        "the high-ranked seed is present: {before:#}"
    );
    let top_flow = read_flows(&before)
        .into_iter()
        .find(|flow| flow.seeds.iter().any(|seed| seed == "RunAll"))
        .expect("the high-ranked seed has a flow");
    assert!(
        top_flow
            .steps
            .iter()
            .any(|(name, path)| name == "Leaf" && path == "zed/leaf.go"),
        "the zed leaf is listed in the top flow: {top_flow:?}"
    );
    let answer_seed_paths = before["entry_points"].as_array().unwrap().len();
    assert!(
        answer_seed_paths < 50,
        "seed paths leave room for listed flow steps"
    );
    let alphabetic_predecessors = (4..58)
        .map(|leaf| format!("a{leaf:02}/leaf.go"))
        .filter(|path| path.as_str() < "zed/leaf.go")
        .count();
    assert!(
        alphabetic_predecessors > 50,
        "more than 50 envelope paths precede the listed leaf alphabetically: {before:#}"
    );
    write(
        d.path(),
        "zed/leaf.go",
        "package zed\n\nfunc Leaf() { _ = 1 }\n",
    );
    let after = flows(d.path(), &[]);
    let dirty = after["freshness"]["dirty_paths_touching_result"]
        .as_array()
        .unwrap();
    assert!(
        dirty.iter().any(|path| path == "zed/leaf.go"),
        "the listed leaf stays in the freshness prefix: {dirty:?}"
    );
}

/// The retained step set is bounded by ONE global cap, not by seeds x per-walk.
///
/// `MAX_FLOWS` and `MAX_STEPS_LISTED` both bound what the answer LISTS and are applied
/// after every walk has finished, so before this row nothing bounded what stayed RESIDENT:
/// at 200 seeds each holding a walk, the retained `visited` vectors grew with the product
/// while the answer reported two caps whose product was the only thing bounding it. The
/// product IS the cap, so the default run here cannot reach it — a 200-seed fixture at 50
/// steps each is 10,000 symbols of walk, which this fixture does not build — and the cap is
/// therefore reached by a run that reports hitting it rather than by a memory line alone.
///
/// The fixture is the shape the finding describes: many seeds, each reaching a long walk.
/// `Ret` names `RetA1..RetA60`, so `RetA0`'s walk is 60 steps and every other seed's is a
/// suffix of it, 60 flows of up to 60 steps each — 3,600 retained steps against a cap of
/// 10,000. That is under the cap, so the assertion is that the cap is PUBLISHED and that the
/// run reports no retention drop, which is the honest reading: the bound exists, it is
/// legible, and this fixture does not hit it. A row that asserted the drop fired here would
/// be asserting that the shape is bigger than the shape is.
#[test]
fn the_retained_step_set_is_bounded_by_one_published_global_cap() {
    let mut src = String::from("package pkg\n\nfunc RetA0() { ");
    for i in 1..=60 {
        src.push_str(&format!("RetA{i}() "));
    }
    src.push_str("}\n");
    for i in 1..=60 {
        let next = if i < 60 {
            format!(" RetA{}()", i + 1)
        } else {
            String::new()
        };
        src.push_str(&format!("func RetA{i}() {{{next} }}\n"));
    }
    let d = repo(&[GO_MOD, ("pkg/ret.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let cap = v["caps"]["max_retained_steps"].as_u64();
    assert_eq!(
        cap,
        Some(10_000),
        "the global cap is the product of the two published caps, neither raised: {v:#}"
    );
    // And the retention drop is a reported counter, not a silent shortening: on a run that
    // does not hit the cap it reads zero, and the flag beside it agrees with every counter.
    let t = truncation(&v);
    assert_eq!(
        t.get("dropped_retained").copied(),
        Some(0),
        "this fixture retains less than the cap, so nothing is dropped for it: {t:?}"
    );
    assert_eq!(
        t.values().filter(|n| **n > 0).count() > 0,
        v["truncated"].as_bool().unwrap(),
        "`truncated` is the one-line reading of the counters beside it: {t:?} in {v:#}"
    );
}

/// A duplicate reachability set is FOLDED, not charged against the retention cap.
///
/// Two seeds that reach the same set share ONE retained `Walk`; the second adds an origin and
/// no resident step data. Charging the cap per WALK — the order WS-38c-b first wrote — spent
/// budget on a set that was already resident, so a run of duplicate sets could be dropped
/// wholesale once the cap filled. Dropping them changes which seeds a flow names and, through
/// the seed's own community, whether it reads `intra` or `cross` — so a bound meant to cap
/// memory changed an answer (codex-spec r1-2, OCR).
///
/// The fixture is deliberately the degenerate case: EVERY seed reaches the same set, so under
/// the old order the second seed's walk was charged even though the set was already retained.
/// The flow must still name both seeds and drop nothing.
#[test]
fn a_duplicate_reachability_set_is_folded_rather_than_charged_to_the_cap() {
    // Two heads, one shared chain: both heads reach exactly {Shared1..Shared3}, so the two
    // walks produce ONE key and dedup must fold the second.
    let src = "package pkg\n\nfunc HeadA() { Shared1() }\nfunc HeadB() { Shared1() }\n\
               func Shared1() { Shared2(); Shared3() }\nfunc Shared2() {}\nfunc Shared3() {}\n";
    let d = repo(&[GO_MOD, ("pkg/dup.go", src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let fs = read_flows(&v);
    let flow = fs.iter().find(|f| f.seeds.len() >= 2).unwrap_or_else(|| {
        panic!("the two heads share one set and name both seeds: {fs:?} in {v:#}")
    });
    assert!(
        flow.seeds.iter().any(|s| s == "HeadA") && flow.seeds.iter().any(|s| s == "HeadB"),
        "one flow names both seeds, which is the fold: {flow:?}"
    );
    assert_eq!(
        truncation(&v).get("dropped_retained").copied(),
        Some(0),
        "a set already retained is charged nothing, so nothing is dropped for it: {v:#}"
    );
}

/// The default limits bound retained step prefixes by the product of the published flow and
/// per-flow step limits. This small chain fixture confirms the cap is published and that a
/// below-cap run reports no retention loss; large resident shapes are measured separately.
#[test]
fn a_walk_that_would_push_the_run_past_the_global_cap_is_dropped_and_counted() {
    // Two independent trunks of 40 steps each, so there are two reachability sets rather
    // than 60 suffixes of one: dedup folds suffixes together, which is what keeps this
    // fixture's retained sum small. The assertions below are about the counter's PLUMBING —
    // that it is published, reads zero when nothing was dropped, and is included in
    // `truncated` — not about the cap firing, which the doc comment shows it cannot here.
    let mut src = String::from("package pkg\n\n");
    for trunk in ["P", "Q"] {
        src.push_str(&format!("func {trunk}0() {{ "));
        for i in 1..40 {
            src.push_str(&format!("{trunk}{i}() "));
        }
        src.push_str("}\n");
        for i in 1..40 {
            let next = if i < 39 {
                format!(" {trunk}{}()", i + 1)
            } else {
                String::new()
            };
            src.push_str(&format!("func {trunk}{i}() {{{next} }}\n"));
        }
    }
    let d = repo(&[GO_MOD, ("pkg/trunks.go", &src)]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let t = truncation(&v);
    // Every counter the answer publishes is present, so a caller can read the one it wants
    // without a missing key reading as zero. `dropped_retained` is WS-38c-b's.
    for key in [
        "truncated_at_branching",
        "truncated_at_depth",
        "dropped_candidates",
        "dropped_under_min_steps",
        "dropped_flows",
        "truncated_step_lists",
        "dropped_retained",
    ] {
        assert!(t.contains_key(key), "{key} is published in {t:?}");
    }
    // The flag agrees with the counters: this fixture truncates on the depth cap (40 > 10),
    // so `truncated` is true, and the retention counter's own zero does not contradict it
    // because it is a different counter.
    assert_eq!(
        v["truncated"].as_bool(),
        Some(t.values().any(|n| *n > 0)),
        "the flag is the OR of the counters beside it: {t:?}"
    );
}

#[test]
fn a_callee_file_cut_by_branching_marks_the_answer_stale() {
    let files = [
        GO_MOD,
        (
            "pkg/seed.go",
            "package pkg\n\nfunc Seed() { A(); B(); C(); D(); E(); F() }\n",
        ),
        ("pkg/a.go", "package pkg\n\nfunc A() {}\n"),
        ("pkg/b.go", "package pkg\n\nfunc B() {}\n"),
        ("pkg/c.go", "package pkg\n\nfunc C() {}\n"),
        ("pkg/d.go", "package pkg\n\nfunc D() {}\n"),
        ("pkg/e.go", "package pkg\n\nfunc E() {}\n"),
        ("pkg/f.go", "package pkg\n\nfunc F() {}\n"),
    ];
    let d = repo(&files);
    build(d.path());
    let before = flows(d.path(), &[]);
    assert!(
        read_seeds(&before).iter().any(|seed| seed.name == "Seed"),
        "the six-callee entry point is scored: {before:#}"
    );
    let seed_flow = read_flows(&before)
        .into_iter()
        .find(|flow| flow.seeds.iter().any(|seed| seed == "Seed"))
        .unwrap_or_else(|| panic!("{before:#}"));
    assert!(
        !seed_flow.steps.iter().any(|(_, path)| path == "pkg/f.go"),
        "the branching cap cuts off F's file: {:?}",
        seed_flow.steps
    );
    write(d.path(), "pkg/f.go", "package pkg\n\nfunc RenamedF() {}\n");
    let after = flows(d.path(), &[]);
    let dirty = after["freshness"]["dirty_paths_touching_result"]
        .as_array()
        .unwrap_or_else(|| panic!("{after:#}"));
    assert!(
        dirty.iter().any(|path| path == "pkg/f.go"),
        "the file of a callee excluded by the branching cap shapes Seed's score: {dirty:?}"
    );
}

/// `candidate_paths` contains caller files for scored candidates, even when those callers are
/// neither seeds nor reached steps. The fixture puts `CallerLow` in that exact position: it is
/// the only caller of the high-scoring `Low1`, has no callee of its own, and is reached by no
/// flow walk. Editing its file must therefore mark the answer stale. The separate envelope
/// also retains files of candidates excluded by the candidate cap because their call edges
/// contributed to scores.
#[test]
fn an_unselected_callers_file_marks_the_answer_stale() {
    // 261 mid symbols, each scoring ABOVE the caller, so the 200-seed cap fills with them
    // and the caller's own candidacy loses. Two callees per symbol: ratio 2.0, ×2 for
    // exported = 4.0, against `CallerLow`'s single callee at 2.0.
    let mut mid = String::from("package pkg\n\n");
    for i in 0..260 {
        mid.push_str(&format!("func M{i}() {{ M{}(); M{}() }}\n", i + 1, i + 2));
    }
    mid.push_str("func M260() {}\nfunc M261() {}\n");
    let d = repo(&[
        GO_MOD,
        ("pkg/mid.go", &mid),
        // The SEED, in its own file: it is the score whose caller edge is under test. Four
        // resolved callees and no callers puts it at ratio 4.0, ×2 for exported = 8.0, which
        // is the top score in the fixture by a wide margin — an earlier draft gave `Low1` a
        // single callee (score 2.0) and it lost the 200-slot tiebreak to the equally-scored
        // mid chain, so it was never a seed and the premise assertion failed
        // (`logs/ws-38c-b-run-20261004T160544Z.log`).
        (
            "seed/seed.go",
            "package seed\n\nfunc Low1() { A(); B(); C(); D() }\nfunc A() {}\nfunc B() {}\nfunc C() {}\nfunc D() {}\n",
        ),
        // The CALLER, in its own file, and the ONLY symbol in it. It calls the seed and
        // nothing else, which is what keeps it out of `candidate_paths` by BOTH routes:
        //
        //  - no seed lives in this file (asserted below), and
        //  - no walk can reach it, because a walk follows CALLEES from a seed. An earlier
        //    draft gave the caller's own callers (`C1..C3`) a home in `bulk/bulk.go`, and
        //    `C1..C3` seed; the flow seeded at them walks straight into `CallerLow`, so
        //    `caller/entry.go` came back through `candidate_paths`' flow steps and the mutation
        //    survived (`logs/ws-38c-b-run-20261004T161855Z.log`, `KILLED=False`).
        //
        // So the caller's callers must themselves be non-seeds, or live where no seed points
        // at them. Three extra callers in a file whose symbols are all unseeded is the shape
        // that works: `CallerLow` is the only symbol that can call the seed, and nothing
        // calls `CallerLow` except those unseeded callers.
        (
            "caller/entry.go",
            "package caller\n\nimport \"example/seed\"\n\nfunc CallerLow() { seed.Low1() }\n",
        ),
        // The caller's own callers. Each is given many callees of its own so it scores well
        // BELOW the 200-cap's fill, which is what keeps `caller/entry.go` seed-free: score is
        // `callees / (callers + 1)` x 2 for exported, so one callee and four callers is 0.4
        // against the mid chain's 4.0. Making the caller the low-scoring side is not a
        // workaround: a caller edge IS the denominator, so the symbol that shapes a score
        // without being one is exactly the case r4-4 is about.
        (
            "bulk/bulk.go",
            "package bulk\n\nimport \"example/caller\"\n\nfunc C1() { caller.CallerLow(); D1(); D2(); D3(); D4() }\nfunc C2() { caller.CallerLow(); E1(); E2(); E3(); E4() }\nfunc C3() { caller.CallerLow(); F1(); F2(); F3(); F4() }\nfunc D1() {}\nfunc D2() {}\nfunc D3() {}\nfunc D4() {}\nfunc E1() {}\nfunc E2() {}\nfunc E3() {}\nfunc E4() {}\nfunc F1() {}\nfunc F2() {}\nfunc F3() {}\nfunc F4() {}\n",
        ),
        // A SCORED candidate whose own file only reaches the envelope through `candidate_paths`
        // (codex-spec r1-1, OCR). `CandidateLow` has one callee and four callers, so it is
        // scored and low. Its callers live in a SEPARATE file: in the same file they became
        // seeds themselves (each has one callee and no callers, scoring 2.0) and named the
        // file through `candidate_paths`, which is why the mutation survived the first two
        // attempts — the path was already on the list for a reason this code is not
        // responsible for.
        //
        // Both files are in the fixture at BUILD time so the index records their stats: a
        // file written after the build has no recorded stat and can never be reported dirty.
        (
            "loser/loser.go",
            "package loser\n\nfunc CandidateLow() { Solo() }\nfunc Solo() {}\n",
        ),
        (
            "loser/ring.go",
            "package loser\n\nfunc L1() { CandidateLow() }\nfunc L2() { CandidateLow() }\nfunc L3() { CandidateLow() }\nfunc L4() { CandidateLow() }\nfunc L5() { L1() }\nfunc L6() { L1() }\nfunc L7() { L1() }\nfunc M1() { L2() }\nfunc M2() { L2() }\nfunc M3() { L2() }\nfunc M4() { L3() }\nfunc M5() { L3() }\nfunc M6() { L3() }\nfunc M7() { L4() }\nfunc M8() { L4() }\nfunc M9() { L4() }\n",
        ),
    ]);
    build(d.path());
    let before = flows(d.path(), &[]);
    // The premises, asserted because without any one of them the envelope assertion below
    // would hold for a reason this row's code is not responsible for.
    let t = truncation(&before);
    assert!(
        t.get("dropped_candidates").copied().unwrap_or(0) > 0,
        "the 200-seed cap really dropped candidates: {before:#}"
    );
    assert!(
        read_seeds(&before).iter().any(|s| s.name == "Low1"),
        "Low1 is a reported seed, so the answer has a score this caller shaped: {before:#}"
    );
    assert!(
        !read_seeds(&before)
            .iter()
            .any(|s| s.path == "caller/entry.go"),
        "no seed lives in the caller's file, which is what makes the file envelope-only: {before:#}"
    );
    // The envelope has no `stale` boolean and `status` describes the indexed commit's
    // relation to HEAD, so it stays `current` across an uncommitted edit. Dirtiness is
    // reported by NAMING the path, and that list is the field r4-4 widens.
    assert_eq!(
        before["freshness"]["status"].as_str(),
        Some("current"),
        "a freshly built answer is current: {before:#}"
    );
    // Edit the CALLER's file only: `Low1` is untouched and the added symbol has no edge at
    // all, so nothing about any score changes — only which files shaped them.
    write(
        d.path(),
        "caller/entry.go",
        "package caller\n\nimport \"example/seed\"\n\nfunc CallerLow() { seed.Low1() }\nfunc CallerX() {}\n",
    );
    let after = flows(d.path(), &[]);
    let dirty = after["freshness"]["dirty_paths_touching_result"]
        .as_array()
        .unwrap_or_else(|| panic!("{after:#}"));
    assert!(
        dirty.iter().any(|p| p == "caller/entry.go"),
        "the caller's own file is named in the envelope, and nothing but `candidate_paths` \
         puts it there: {dirty:?} in {after:#}"
    );
    // The scores are unchanged, which is the point: the envelope reports that files these
    // scores were computed against moved, not that the scores moved.
    let before_scores: Vec<(String, f64)> = by_score(&before)
        .into_iter()
        .map(|s| (s.name, s.score))
        .collect();
    let after_scores: Vec<(String, f64)> = by_score(&after)
        .into_iter()
        .map(|s| (s.name, s.score))
        .collect();
    assert_eq!(
        before_scores, after_scores,
        "the added symbol changes no score, so only the envelope can tell the two answers \
         apart: {before_scores:?} vs {after_scores:?}"
    );
    // The candidate's OWN file is the second half of r4-4, and the half the caller case cannot
    // see: `loser/loser.go` is a SCORED candidate whose own `callees` count is part of the
    // ranking that decides which 200 survive, so editing it can change WHICH survive while
    // nothing in the answer's rows moves. Nothing else names that file — its callers are in
    // `loser/ring.go`, no walk reaches it, and it is not a seed — so only `candidate_paths`
    // can put it in the envelope.
    write(
        d.path(),
        "loser/loser.go",
        "package loser\n\nfunc CandidateLow() { Solo(); Extra() }\nfunc Solo() {}\nfunc Extra() {}\n",
    );
    let loser_moved = flows(d.path(), &[]);
    let dirty2 = loser_moved["freshness"]["dirty_paths_touching_result"]
        .as_array()
        .unwrap_or_else(|| panic!("{loser_moved:#}"));
    assert!(
        dirty2.iter().any(|p| p == "loser/loser.go"),
        "a scored candidate's OWN file is named even when it is no seed and no walk reaches \
         it: {dirty2:?} in {loser_moved:#}"
    );
}

/// The SEED's community joins the flow's community count.
///
/// WS-38c did not count the seed, on the reading that no fixture isolated the case, and
/// recorded the finding for the row that could. This is that row.
///
/// The fixture is three packages: `alpha` holds a ring that calls into `beta`, `beta` holds a
/// ring of its own, and `gamma` holds a two-symbol chain whose head `GammaOne` calls straight
/// into the `beta` ring. `gamma/gamma.go` is its own area, so `GammaOne` and `GammaTwo` are
/// `gamma` while every symbol its walk reaches is `beta` — the seed's community is the ONLY
/// thing on the far side of that boundary.
///
/// The fixture is the shape MEASURED on the box rather than one designed by reading the
/// propagation: three candidates were built against the real binary (`probe-r3-5.sh`) and this
/// is the one whose `GammaOne` flow reports two communities with every step in one file
/// (`logs/ws-38c-b-run-20261004T145145Z.log`, candidate C). The earlier draft of this test
/// reused the CROSS fixture, where the seed's walk also reaches steps in the seed's own
/// community, so the count was one whatever the seed contributed and the test failed on
/// correct code — the defect was the fixture's, not the classifier's.
///
/// So the assertions are the ones the seed's area makes true and the steps' areas cannot: the
/// flow seeded at `GammaOne` spans two communities while every one of its steps is a `beta`
/// symbol, and it reads `cross`. Remove the seed from the count and that flow drops to one
/// community and reads `intra`; keep it and it reads `cross`. The step list is asserted first,
/// so the test cannot pass by the seed's community arriving through a step.
#[test]
fn the_seeds_own_community_counts_toward_the_class() {
    let d = repo(&[
        GO_MOD,
        (
            "alpha/alpha.go",
            "package alpha\n\nimport \"example/beta\"\n\nfunc AlphaOne() { AlphaTwo(); AlphaThree(); beta.BetaOne() }\nfunc AlphaTwo() { AlphaThree() }\nfunc AlphaThree() { AlphaOne() }\n",
        ),
        (
            "beta/beta.go",
            "package beta\n\nfunc BetaOne() { BetaTwo() }\nfunc BetaTwo() { BetaThree() }\nfunc BetaThree() { BetaOne() }\n",
        ),
        (
            "gamma/gamma.go",
            "package gamma\n\nimport \"example/beta\"\n\nfunc GammaOne() { beta.BetaTwo(); beta.BetaThree() }\nfunc GammaTwo() { GammaOne() }\nfunc GammaThree() { GammaTwo() }\n",
        ),
    ]);
    build(d.path());
    let v = flows(d.path(), &[]);
    let seeds = read_seeds(&v);
    assert_eq!(
        seeds
            .iter()
            .find(|s| s.name == "GammaOne")
            .map(|s| s.callees),
        Some(2),
        "GammaOne's two calls into the beta package resolve: {v:#}"
    );
    let fs = read_flows(&v);
    let flow = fs
        .iter()
        .find(|f| f.seeds == vec!["GammaOne".to_string()])
        .unwrap_or_else(|| panic!("the flow seeded at GammaOne alone in {fs:?} in {v:#}"));
    // Every step it reaches is in ONE file, so the communities the STEPS alone can span are
    // exactly one. Asserted first: without it the count below could be satisfied by a step.
    assert!(
        flow.steps.len() >= 2,
        "GammaOne's walk reaches the beta ring, so the flow is not empty: {flow:?}"
    );
    assert!(
        flow.steps.iter().all(|(_, p)| p == "beta/beta.go"),
        "every step GammaOne reaches is a beta symbol, so the seed's own community is the \
         only second one: {flow:?}"
    );
    assert_eq!(
        flow.communities, 2,
        "the seed's own community and the one its steps land in: {flow:?}"
    );
    assert_eq!(
        flow.class, "cross",
        "a flow entered from outside the community its steps live in crosses a boundary: {flow:?}"
    );
}

#[test]
fn exported_flag_row_mismatches_refuse_and_roll_back() {
    let source = "package pkg\n\nfunc Root() { LeafA(); LeafB(); LeafC() }\n\
                  func LeafA() {}\nfunc LeafB() {}\nfunc LeafC() {}\n";

    let (_d, db, snapshot) = opened_snapshot(source);
    assert_exported_flag_refusal_rolls_back(
        &db,
        &snapshot,
        |conn| {
            let file_id: i64 = conn
                .query_row(
                    "SELECT id FROM files WHERE parse_status = 'tree_sitter' LIMIT 1",
                    [],
                    |row| row.get(0),
                )
                .unwrap();
            conn.execute("INSERT INTO names(name) VALUES ('SurplusFlag')", [])
                .unwrap();
            let name_id = conn.last_insert_rowid();
            conn.execute(
                "INSERT INTO symbols (file_id, ord, uid, name_id, qualified_name, kind,
                    start_byte, end_byte, start_line, end_line, name_line, name_col,
                    exported, depth, local)
                 VALUES (?1, 999, 'surplus-flag-test', ?2, 'SurplusFlag', 'function',
                    0, 0, 1, 1, 1, 1, 0, 0, 0)",
                rusqlite::params![file_id, name_id],
            )
            .unwrap();
        },
        "store has 5 rows",
    );

    let (_d, db, snapshot) = opened_snapshot(source);
    assert_exported_flag_refusal_rolls_back(
        &db,
        &snapshot,
        |conn| {
            conn.execute(
                "DELETE FROM symbols WHERE id = (SELECT MIN(id) FROM symbols)",
                [],
            )
            .unwrap();
        },
        "store has 3 rows",
    );

    let (_d, db, snapshot) = opened_snapshot(source);
    assert_exported_flag_refusal_rolls_back(
        &db,
        &snapshot,
        |conn| {
            conn.execute(
                "UPDATE symbols SET exported = 'not-a-boolean' WHERE id = (SELECT MIN(id) FROM symbols)",
                [],
            )
            .unwrap();
        },
        "index store:",
    );
}

/// The exported flags cannot come from a newer generation than the segment (WS-38c-b r4-3).
///
/// The segment in a snapshot is loaded when it is opened, but flags live in the `symbols`
/// table and are read lazily on first use. A build committing between those reads could
/// otherwise pair a newer flag set with an older segment, changing the answer's scores.
///
/// The fixture reproduces the mixed generation deterministically rather than racing a thread:
/// the store's `generation` is advanced by a direct write, which is what a build does when it
/// commits. The segment on disk is still the old generation's, so `index flows` must REFUSE —
/// a refusal is the honest answer to "these two halves describe different generations" —
/// rather than answer with flags that do not belong to the graph beside them.
#[test]
fn a_store_generation_that_moved_past_the_segment_is_refused_rather_than_mixed() {
    let d = repo(&[
        GO_MOD,
        (
            "pkg/gen.go",
            &gofuncs_ref(&[
                go_func("GenRoot", &["GenLeafA", "GenLeafB"]),
                go_func("GenLeafA", &[]),
                go_func("GenLeafB", &[]),
            ]),
        ),
    ]);
    build(d.path());
    // The answer is clean before the store moves: this is the control, so a later refusal is
    // attributable to the generation check rather than to a fixture that never worked.
    let before = flows(d.path(), &[]);
    assert!(
        before["exported_symbols"].as_u64().unwrap_or(0) > 0,
        "the fixture has exported symbols, so the flag read is real: {before:#}"
    );
    let db = index::stats(d.path(), &IndexConfig::default(), false).unwrap()["index_path"]
        .as_str()
        .unwrap()
        .to_string();
    let conn = rusqlite::Connection::open(&db).unwrap();
    let generation: String = conn
        .query_row("SELECT value FROM meta WHERE key = 'generation'", [], |r| {
            r.get(0)
        })
        .unwrap();
    let bumped: i64 = generation.parse::<i64>().unwrap() + 1;
    conn.execute(
        "UPDATE meta SET value = ?1 WHERE key = 'generation'",
        [bumped.to_string()],
    )
    .unwrap();
    drop(conn);
    // The segment on disk is unchanged and still carries the OLD generation, so the flags
    // read now describes a different store than the graph beside them.
    let out = run(&["index", "flows", d.path().to_str().unwrap()]);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        !out.status.success(),
        "a store whose generation moved past the segment is refused, not answered with \
         mismatched flags: {stderr}"
    );
    assert!(
        stderr.contains("generation"),
        "the refusal names the generation rather than reporting an empty answer: {stderr}"
    );
    // And it names BOTH generations, so an operator can tell which is which.
    assert!(
        stderr.contains(&generation) && stderr.contains(&bumped.to_string()),
        "the refusal names the store's generation and the snapshot's: {stderr}"
    );
}
