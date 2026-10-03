//! Functional areas (WS-38b; PAR-SYM-08): seeded deterministic communities at symbol
//! granularity over the resolved CALLS/EXTENDS/IMPLEMENTS projection, with large-graph
//! mode, folder labels, sampled cohesion and keywords.
//!
//! Exercised through the real `xmustard-core` binary against disposable Git
//! repositories, as `tests/impact_v2.rs` does.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::process::{Command, Output};

use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

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

fn files(list: &[(&str, &str)]) -> Vec<(String, String)> {
    list.iter()
        .map(|(a, b)| (a.to_string(), b.to_string()))
        .collect()
}

fn repo(list: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files(list) {
        write(dir.path(), &rel, &content);
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

/// `index areas <root> [--seed=N]`: the index family takes no workspace id.
fn areas(root: &Path, flags: &[&str]) -> Value {
    let mut args = vec!["index", "areas", root.to_str().unwrap()];
    args.extend_from_slice(flags);
    ok_json(&args)
}

/// One area row, with the fields this row's contract names.
struct Area {
    id: u64,
    label: String,
    size: u64,
    cohesion: f64,
    keywords: Vec<String>,
    members: BTreeSet<String>,
    paths: BTreeSet<String>,
}

fn read_areas(v: &Value) -> Vec<Area> {
    v["areas"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .map(|a| Area {
            id: a["area_id"].as_u64().unwrap(),
            label: a["label"].as_str().unwrap().to_string(),
            size: a["size"].as_u64().unwrap(),
            cohesion: a["cohesion"].as_f64().unwrap(),
            keywords: a["keywords"]
                .as_array()
                .unwrap()
                .iter()
                .map(|k| k.as_str().unwrap().to_string())
                .collect(),
            members: a["members"]
                .as_array()
                .unwrap()
                .iter()
                .map(|m| m["name"].as_str().unwrap().to_string())
                .collect(),
            paths: a["members"]
                .as_array()
                .unwrap()
                .iter()
                .map(|m| m["path"].as_str().unwrap().to_string())
                .collect(),
        })
        .collect()
}

/// Area ids by symbol name: which area each named symbol landed in.
fn membership(v: &Value) -> BTreeMap<String, u64> {
    let mut out = BTreeMap::new();
    for a in read_areas(v) {
        for m in a.members {
            out.insert(m, a.id);
        }
    }
    out
}

/// A repository with two disjoint subsystems under non-generic folders, each a small
/// call graph, plus a type hierarchy, so the symbol projection has structure to find.
fn two_subsystems() -> TempDir {
    repo(&[
        (
            "billing/invoice.go",
            "package billing\n\ntype Invoice struct{ Cents int }\n\nfunc (i *Invoice) Total() int { return i.Cents }\n\nfunc PriceOf(i *Invoice) int { return i.Total() }\n",
        ),
        (
            "billing/refund.go",
            "package billing\n\nfunc Refund(i *Invoice) int { return PriceOf(i) }\n",
        ),
        (
            "shipping/parcel.go",
            "package shipping\n\ntype Parcel struct{ Grams int }\n\nfunc (p *Parcel) Weight() int { return p.Grams }\n\nfunc Label(p *Parcel) int { return p.Weight() }\n",
        ),
        (
            "shipping/track.go",
            "package shipping\n\nfunc Track(p *Parcel) int { return Label(p) }\n",
        ),
    ])
}

#[test]
fn the_same_seed_over_the_same_index_is_byte_identical_across_runs() {
    let d = two_subsystems();
    build(d.path());
    let first = run(&["index", "areas", d.path().to_str().unwrap(), "--seed=7"]);
    let second = run(&["index", "areas", d.path().to_str().unwrap(), "--seed=7"]);
    assert!(
        first.status.success(),
        "{}",
        String::from_utf8_lossy(&first.stderr)
    );
    assert!(second.status.success());
    assert_eq!(
        first.stdout, second.stdout,
        "the same seed over the same index gave two different answers"
    );
    let v: Value = serde_json::from_slice(&first.stdout).unwrap();
    assert_eq!(v["seed"], 7);
    assert!(!read_areas(&v).is_empty(), "{v:#}");
}

#[test]
fn the_same_seed_gives_the_same_areas_after_a_full_rebuild() {
    let d = two_subsystems();
    build(d.path());
    let before = areas(d.path(), &["--seed=7"]);
    // a second build from scratch: the same bytes, a new generation on disk
    std::fs::remove_dir_all(d.path().join(".git/xmustard-cache").join("index-v3")).unwrap();
    build(d.path());
    let after = areas(d.path(), &["--seed=7"]);
    assert_eq!(
        membership(&before),
        membership(&after),
        "the same seed gave different areas after a rebuild"
    );
}

/// A ring of N symbols, each calling exactly two others: every node has a neighbour it
/// shares half its weight with and one it shares half with, so a node's fate depends on
/// the order the seed visits it in and two seeds generally disagree.
fn ring(n: usize) -> TempDir {
    let mut src = String::from("package ring\n\n");
    for i in 0..n {
        src.push_str(&format!(
            "func N{i}(a, b int) int {{ return N{}(a) + N{}(b) }}\n",
            (i + 1) % n,
            (i + n - 1) % n
        ));
    }
    let d = repo(&[("ring/gen.go", &src)]);
    build(d.path());
    d
}

#[test]
fn a_different_seed_changes_the_result_so_the_seed_is_load_bearing() {
    let d = ring(24);
    let a = areas(d.path(), &["--seed=1"]);
    let b = areas(d.path(), &["--seed=2"]);
    assert_ne!(
        a["areas"], b["areas"],
        "the seed is decorative: seed=1 and seed=2 gave identical areas"
    );
}

#[test]
fn the_default_mode_agrees_with_the_base_of_a_large_graph() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &[]);
    assert_eq!(v["mode"], "standard", "{v:#}");
    assert_eq!(
        v["resolution"], 1.0,
        "standard mode uses resolution 1.0: {v:#}"
    );
    assert_eq!(v["seed"], 0, "the default seed is 0: {v:#}");
}

#[test]
fn large_graph_mode_triggers_above_ten_thousand_symbols() {
    // 4,200 files of 4 chained symbols each: 16,800 symbols over the resolved CALLS
    // projection, above PAR-SYM-08's 10k threshold, and every file under the index's own
    // 4,096-symbol per-file cap (a single file this size is truncated, which the first
    // run of this test measured).
    let mut list: Vec<(String, String)> = Vec::with_capacity(4_200);
    for f in 0..4_200 {
        list.push((
            format!("big/g{f:05}.go"),
            format!(
                "package big\n\nfunc A{f}() int {{ return 0 }}\n\nfunc B{f}() int {{ return A{f}() }}\n\nfunc C{f}() int {{ return B{f}() }}\n\nfunc D{f}() int {{ return C{f}() }}\n"
            ),
        ));
    }
    let dir = TempDir::new().unwrap();
    for (rel, content) in &list {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    build(dir.path());
    let v = areas(dir.path(), &["--seed=3"]);
    assert!(
        v["symbols"].as_u64().unwrap() > 10_000,
        "the fixture must exceed the 10k threshold: {v:#}"
    );
    assert_eq!(v["mode"], "large", "the mode did not switch: {v:#}");
    assert_eq!(
        v["resolution"], 2.0,
        "large mode uses resolution 2.0: {v:#}"
    );
    assert!(v["iterations"].as_u64().unwrap() <= 3, "{v:#}");
    assert!(
        v["dropped_low_degree"].as_u64().unwrap() > 0,
        "large mode drops nodes of degree below 2 and says how many: {v:#}"
    );
    assert_eq!(
        v["dropped_low_confidence_edges"].as_u64().unwrap(),
        v["dropped_low_confidence_edges"].as_u64().unwrap(),
        "the drop count is a number: {v:#}"
    );
}

#[test]
fn a_label_is_a_member_folder_when_it_is_not_generic() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let labels: BTreeSet<String> = read_areas(&v).into_iter().map(|a| a.label).collect();
    assert!(
        labels.contains("billing") || labels.contains("shipping"),
        "no folder label among {labels:?}"
    );
}

#[test]
fn a_label_falls_back_to_a_name_prefix_and_then_to_cluster_n() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    for a in read_areas(&v) {
        assert!(!a.label.is_empty(), "an area has no label: {v:#}");
        assert!(
            a.label
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || "-_./".contains(c)),
            "label {:?} is not a plain name",
            a.label
        );
    }
    // the fallback chain is visible in the output, so a reader can tell which rule fired
    assert!(
        v["label_rules"].is_array(),
        "the label rules are not reported: {v:#}"
    );
}

#[test]
fn cohesion_is_bounded_and_keywords_are_a_bounded_list() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    for a in read_areas(&v) {
        assert!(
            (0.0..=1.0).contains(&a.cohesion),
            "cohesion {} is out of range",
            a.cohesion
        );
        assert!(a.size > 0);
        assert!(a.keywords.len() <= 8, "{:?} is not bounded", a.keywords);
        assert!(
            a.keywords.iter().all(|k| !k.is_empty()),
            "an empty keyword: {:?}",
            a.keywords
        );
    }
}

#[test]
fn cohesion_samples_at_most_fifty_members() {
    // one area of 200 members: a full internal-edge count would differ from a 50-member
    // sample, so the reported `cohesion_sampled` flag and the cap are both observable.
    let mut src = String::from("package wide\n\nfunc Hub() int { return 0 }\n");
    for i in 0..200 {
        src.push_str(&format!("func M{i}() int {{ return Hub() }}\n"));
    }
    let d = repo(&[("wide/gen.go", &src)]);
    build(d.path());
    let v = areas(d.path(), &["--seed=5"]);
    assert_eq!(v["cohesion_sample_size"], 50, "{v:#}");
    let widest = read_areas(&v)
        .into_iter()
        .max_by_key(|a| a.size)
        .expect("at least one area");
    assert!(
        widest.size > 50,
        "the fixture must produce an area above the sample size, got {}",
        widest.size
    );
}

#[test]
fn two_files_that_never_reference_each_other_land_in_different_areas() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let m = membership(&v);
    let billing = m.get("Refund").copied();
    let shipping = m.get("Track").copied();
    assert!(
        billing.is_some() && shipping.is_some(),
        "the fixture's symbols are missing from the areas: {m:?}"
    );
    assert_ne!(
        billing, shipping,
        "the symbol projection merged two disconnected subsystems: {v:#}"
    );
}

#[test]
fn a_resolved_call_across_files_carries_a_symbol_into_the_other_area() {
    // The import only resolves inside a module: without a go.mod the path is external and
    // the resolver drops it (measured, 3 external drops in the build report).
    let d = repo(&[
        ("go.mod", "module example\n\ngo 1.21\n"),
        (
            "zoo/animals.go",
            "package zoo\n\nfunc Speak() string { return \"woof\" }\n",
        ),
        (
            "kennel/kennel.go",
            "package kennel\n\nimport \"example/zoo\"\n\nfunc Bark() string { return zoo.Speak() }\n",
        ),
    ]);
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let m = membership(&v);
    assert!(
        m.contains_key("Speak") && m.contains_key("Bark"),
        "the fixture's symbols are missing from the areas: {m:?}"
    );
    assert_eq!(
        m["Speak"], m["Bark"],
        "a resolved CALL across two packages did not carry Bark into Speak's area: {m:?}"
    );
}

#[test]
fn areas_are_reported_at_symbol_granularity_with_their_paths() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let paths: BTreeSet<String> = read_areas(&v).into_iter().flat_map(|a| a.paths).collect();
    assert!(
        paths.contains("billing/invoice.go") && paths.contains("shipping/parcel.go"),
        "member paths are missing: {paths:?}"
    );
    for a in read_areas(&v) {
        assert_eq!(
            a.size as usize,
            a.members.len(),
            "size {} does not match {} members",
            a.size,
            a.members.len()
        );
    }
}

#[test]
fn a_missing_index_is_refused_rather_than_answered_from_nothing() {
    let d = repo(&[("a/a.go", "package a\n\nfunc A() int { return 1 }\n")]);
    let out = run(&["index", "areas", d.path().to_str().unwrap()]);
    assert!(
        !out.status.success(),
        "{:?}",
        String::from_utf8_lossy(&out.stdout)
    );
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("no index at"),
        "the refusal does not name the missing index: {err}"
    );
}

#[test]
fn a_non_numeric_seed_is_refused() {
    let d = two_subsystems();
    build(d.path());
    let out = run(&["index", "areas", d.path().to_str().unwrap(), "--seed=nope"]);
    assert!(
        !out.status.success(),
        "{:?}",
        String::from_utf8_lossy(&out.stdout)
    );
    let err = String::from_utf8_lossy(&out.stderr);
    assert!(
        err.contains("--seed"),
        "the refusal does not name the flag: {err}"
    );
}
