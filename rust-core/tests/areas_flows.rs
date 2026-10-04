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
#[derive(Debug)]
struct Area {
    id: u64,
    label: String,
    rule: String,
    size: u64,
    cohesion: f64,
    cohesion_sample: u64,
    keywords: Vec<String>,
    members: BTreeSet<String>,
    /// The member rows in the order the answer lists them, as (path, line, name) — the
    /// key the answer claims they are sorted by.
    member_rows: Vec<(String, u32, String)>,
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
            rule: a["label_rule"].as_str().unwrap().to_string(),
            size: a["size"].as_u64().unwrap(),
            cohesion: a["cohesion"].as_f64().unwrap(),
            cohesion_sample: a["cohesion_sample"].as_u64().unwrap(),
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
            member_rows: a["members"]
                .as_array()
                .unwrap()
                .iter()
                .map(|m| {
                    (
                        m["path"].as_str().unwrap().to_string(),
                        m["line"].as_u64().unwrap() as u32,
                        m["name"].as_str().unwrap().to_string(),
                    )
                })
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
    // pass_counters is the call's own accounting (the first run loads rows the second
    // finds resident on disk), so the answer is compared without it.
    assert_eq!(
        without_pass_counters(&first.stdout),
        without_pass_counters(&second.stdout),
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
    // The whole answer, not just the membership: a rebuild that kept the area ids but
    // moved a label, a cohesion figure, the keyword list, the member order, the
    // truncation flag or the mode would pass a membership comparison.
    assert_eq!(
        without_freshness(&before),
        without_freshness(&after),
        "the same seed gave a different answer after a rebuild"
    );
}

/// The answer with its freshness block removed: that block carries the index generation and
/// identity age, which differ between two builds over the same bytes and are not part of
/// what "the same areas" means.
fn without_freshness(v: &Value) -> Value {
    let mut c = v.clone();
    if let Some(o) = c.as_object_mut() {
        o.remove("freshness");
    }
    c
}

/// A shared word is only a prefix when it leads both names. `InvoiceStore` and
/// `PaymentStore` share the word `store`, and a rule that matches anywhere in a name
/// would label the area `store`; neither name leads with it, so the area falls through to
/// the cluster rule.
#[test]
fn a_shared_suffix_is_not_a_name_prefix() {
    let d = repo(&[(
        "src/store.go",
        "package src\n\nfunc InvoiceStore() int { return PaymentStore() }\n\nfunc PaymentStore() int { return 0 }\n",
    )]);
    build(d.path());
    let v = areas(d.path(), &["--seed=3"]);
    let areas = read_areas(&v);
    let multi: Vec<&Area> = areas.iter().filter(|a| a.size > 1).collect();
    assert_eq!(
        multi.len(),
        1,
        "the fixture must form exactly one multi-symbol area: {v:#}"
    );
    assert_eq!(
        multi[0].rule, "fallback",
        "a shared suffix was taken for a prefix: {v:#}"
    );
}

/// Standard mode has to actually cluster. PAR-SYM-08's resolution floor is a large-graph
/// rule; applied at standard mode's resolution of 1.0 the adoption threshold is a node's
/// entire incident weight, so no node could ever leave the community it started in and
/// every area stayed a singleton. This is the regression: a three-symbol call ring, the
/// smallest shape in which the difference is visible.
#[test]
fn standard_mode_forms_one_area_from_a_call_ring() {
    let d = repo(&[(
        "billing/cycle.go",
        "package billing\n\nfunc Alpha() int { return Beta() }\n\nfunc Beta() int { return Gamma() }\n\nfunc Gamma() int { return Alpha() }\n",
    )]);
    build(d.path());
    let v = areas(d.path(), &["--seed=3"]);
    assert_eq!(v["mode"], "standard", "{v:#}");
    let m = membership(&v);
    assert_eq!(
        m.get("Alpha"),
        m.get("Beta"),
        "a call ring was not clustered in standard mode: {v:#}"
    );
    assert_eq!(
        m.get("Beta"),
        m.get("Gamma"),
        "the ring's third symbol landed elsewhere: {v:#}"
    );
    assert_eq!(
        v["areas"].as_array().unwrap().len(),
        1,
        "a three-symbol ring must be one area: {v:#}"
    );
}

/// The name-prefix rule reads every member, not the first one. `rows` is ordered by
/// (path, line, name), so seeding candidates from the first member missed a prefix that
/// two later members share whenever the first name was unrelated to them.
#[test]
fn a_name_prefix_is_found_when_the_first_member_does_not_share_it() {
    // One file in a generic folder: Go drops a call to a name in another package as external
    // (the resolver's `global: false` rule), so a fixture that spanned packages would not
    // form an area at all; and the folder rule runs before the name-prefix rule, so a
    // non-generic folder would label the area before the prefix was ever tried. `Alpha` is
    // first by (path, line, name) and shares no leading word with the other two.
    let d = repo(&[(
        "src/store.go",
        "package src\n\nfunc Alpha() int { return InvoiceStore() }\n\nfunc InvoiceStore() int { return InvoiceService() }\n\nfunc InvoiceService() int { return 0 }\n",
    )]);
    build(d.path());
    let v = areas(d.path(), &["--seed=3"]);
    // The three symbols form one area, and its first member by (path, line, name) is
    // `Alpha`, which shares no leading word with the other two.
    let labelled: Vec<(String, String)> = read_areas(&v)
        .into_iter()
        .filter(|a| a.size > 1)
        .map(|a| (a.rule.clone(), a.label.clone()))
        .collect();
    assert!(
        labelled
            .iter()
            .any(|(rule, label)| rule == "name_prefix" && label == "invoice"),
        "the shared `invoice` prefix was not found when the first member was unrelated: \
         {labelled:?} in {v:#}"
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
    // 4,200 files of 4 symbols each, each file a RING of four calls: 16,800 symbols over
    // the resolved CALLS projection, above PAR-SYM-08's 10k threshold, and every file
    // under the index's own 4,096-symbol per-file cap (a single file this size is
    // truncated, which the first run of this test measured). A ring rather than a chain
    // because PAR-SYM-08's degree rule is applied to a fixpoint: every symbol of a chain
    // ends up with no edge left, which is the rule working, and a ring is the shape that
    // survives it.
    let mut list: Vec<(String, String)> = Vec::with_capacity(4_200);
    for f in 0..4_200 {
        list.push((
            format!("big/g{f:05}.go"),
            format!(
                "package big\n\nfunc A{f}() int {{ return B{f}() }}\n\nfunc B{f}() int {{ return C{f}() }}\n\nfunc C{f}() int {{ return D{f}() }}\n\nfunc D{f}() int {{ return A{f}() }}\n"
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
    assert_eq!(
        v["dropped_low_degree"].as_u64().unwrap(),
        0,
        "every symbol of a ring has degree 2, so none is dropped: {v:#}"
    );
    assert_eq!(
        v["singleton_symbols"].as_u64().unwrap(),
        0,
        "no symbol is left without an edge: {v:#}"
    );
    assert_eq!(
        v["dropped_low_confidence_edges"].as_u64().unwrap(),
        0,
        "a resolved Go call is above the confidence floor: {v:#}"
    );
    assert_eq!(
        v["iterations"].as_u64().unwrap(),
        2,
        "a ring settles as soon as a pass changes no label: {v:#}"
    );
    assert_eq!(
        v["areas"].as_array().unwrap().len(),
        4_200,
        "each ring of four is one community: {v:#}"
    );
}

/// PAR-SYM-08's degree rule applied to a fixpoint: every symbol of a four-symbol chain is
/// pruned, because each one's only edges lead to another that is pruned too. This is the
/// rule working as written, and it is why the large-graph fixture above is a ring.
#[test]
fn large_graph_mode_prunes_a_path_to_nothing() {
    let mut list: Vec<(String, String)> = Vec::with_capacity(2_700);
    for f in 0..2_700 {
        list.push((
            format!("path/p{f:05}.go"),
            format!(
                "package path\n\nfunc A{f}() int {{ return 0 }}\n\nfunc B{f}() int {{ return A{f}() }}\n\nfunc C{f}() int {{ return B{f}() }}\n\nfunc D{f}() int {{ return C{f}() }}\n"
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
    assert_eq!(
        v["symbols"].as_u64().unwrap(),
        10_800,
        "the fixture must clear the 10k threshold: {v:#}"
    );
    assert_eq!(v["mode"], "large", "{v:#}");
    // 2,700 chains of four: each chain's two middle symbols are of degree 2 and the two
    // ends of degree 1. Pruning runs to a fixpoint, so the ends go first and the middles
    // are left with no edge and go too: every symbol is dropped, which is the rule applied
    // to a fixpoint rather than in one pass. The count answers for the whole pruning.
    assert_eq!(
        v["dropped_low_degree"].as_u64().unwrap(),
        10_800,
        "every symbol of every chain is pruned once the pruning reaches its fixpoint: {v:#}"
    );
    // A dropped node is not reported: PAR-SYM-08 drops degree-below-2 nodes, so none of
    // them may come back as a singleton area.
    assert_eq!(
        v["areas"].as_array().unwrap().len(),
        0,
        "pruned symbols are still reported as areas: {v:#}"
    );
    assert_eq!(
        v["singleton_symbols"].as_u64().unwrap(),
        0,
        "a pruned symbol is not a singleton either: {v:#}"
    );
}

/// The precondition of the confidence floor, and the closest any fixture gets to it.
///
/// A below-floor edge is a global-tier one (confidence 0.5, quantized to 127, one step
/// below `LARGE_MIN_CONFIDENCE`), and Go cannot produce one: its resolver rules set
/// `global: false`, so a call to a name in another package with no import is dropped as
/// external. That is why every Go fixture in this file reports
/// `dropped_low_confidence_edges: 0` — the floor is never reached from Go.
///
/// TypeScript does produce the edge. This asserts that it forms at all, so the floor's
/// input is pinned and a future change to the global tier or to the tier table is caught
/// here rather than silently making `LARGE_MIN_CONFIDENCE` dead code. It does **not**
/// assert a drop count: the counter is non-zero only above the 10k large-graph threshold,
/// and at that scale the projection no longer admits the two-file TypeScript bridge into
/// the same admitted node set as 16,800 Go symbols (measured: 16,802 symbols,
/// `singletons: 2`, `dropped_low_confidence_edges: 0`). That gap is FU's, not this row's.
#[test]
fn a_global_tier_edge_is_the_only_shape_that_reaches_the_confidence_floor() {
    let d = repo(&[
        (
            "bridge/reach.ts",
            "export function reach(): number { return zonlyhere(); }\n",
        ),
        (
            "bridge/zonlyhere.ts",
            "export function zonlyhere(): number { return 0; }\n",
        ),
    ]);
    build(d.path());
    let v = areas(d.path(), &["--seed=3"]);
    assert_eq!(
        v["mode"], "standard",
        "a small graph is not large mode: {v:#}"
    );
    // The unique-name call resolves across files with no import and no qualifier, so the
    // two symbols land in one community — the edge exists, and it is the below-floor one.
    let m = membership(&v);
    assert_eq!(
        m.get("reach"),
        m.get("zonlyhere"),
        "the global-tier edge did not form, so the confidence floor has no input to drop: {v:#}"
    );
    // Go reaches the same shape only through scope, so it never produces a low-confidence
    // edge; the count is zero in every Go fixture in this file for that reason.
    let g = repo(&[
        (
            "billing/invoice.go",
            "package billing\n\nfunc Alpha() int { return Zeta() }\n",
        ),
        (
            "other/zeta.go",
            "package other\n\nfunc Zeta() int { return 0 }\n",
        ),
    ]);
    build(g.path());
    let gv = areas(g.path(), &["--seed=3"]);
    let gm = membership(&gv);
    assert!(
        !gm.contains_key("Alpha") || !gm.contains_key("Zeta") || gm.get("Alpha") != gm.get("Zeta"),
        "a Go call across packages with no import resolved anyway, which contradicts the \
         resolver's `global: false` rule: {gv:#}"
    );
    assert_eq!(
        gv["dropped_low_confidence_edges"].as_u64().unwrap(),
        0,
        "Go produced a below-floor edge, which the global tier says it cannot: {gv:#}"
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

/// The tail of the label chain, asserted rather than described. A subsystem whose members
/// share no folder and no leading name word must land on the cluster rule, and the answer
/// has to say so per area: `label` is `Cluster_<rank>` and `label_rule` is the rule name,
/// so a reader can tell a real name from a positional placeholder.
#[test]
fn the_fallback_rule_names_a_cluster_and_says_it_fired() {
    // Five symbols in one generic folder, chained by calls, with five distinct leading
    // name words. The folder rule skips `src` as generic and the name-prefix rule finds no
    // word that two members share, so the chain has to reach the fallback.
    let d = repo(&[
        (
            "src/alpha.go",
            "package src\n\nfunc Alpha() int { return Beta() }\n",
        ),
        (
            "src/beta.go",
            "package src\n\nfunc Beta() int { return Gamma() }\n",
        ),
        (
            "src/gamma.go",
            "package src\n\nfunc Gamma() int { return Delta() }\n",
        ),
        (
            "src/delta.go",
            "package src\n\nfunc Delta() int { return Epsilon() }\n",
        ),
        (
            "src/epsilon.go",
            "package src\n\nfunc Epsilon() int { return 0 }\n",
        ),
    ]);
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let areas = read_areas(&v);
    let labelled: Vec<&Area> = areas.iter().filter(|a| a.size > 1).collect();
    assert!(
        !labelled.is_empty(),
        "the fixture must produce a multi-member area: {v:#}"
    );
    for (rank, a) in areas.iter().enumerate() {
        if a.rule == "fallback" {
            // The label is the rank, so two runs that ranked the same way agree and a
            // renamed placeholder (mutation M10) cannot pass.
            assert_eq!(
                a.label,
                format!("Cluster_{rank}"),
                "a fallback label is not Cluster_<its rank in the answer>: {a:?}"
            );
        } else {
            assert!(
                !a.label.starts_with("Cluster_"),
                "a named rule {:?} produced the placeholder label {:?}",
                a.rule,
                a.label
            );
        }
    }
    // at least one area really did take the fallback, so the loop above is not vacuous
    assert!(
        areas.iter().any(|a| a.rule == "fallback" && a.size > 1),
        "no multi-member area reached the fallback rule, so this test proves nothing: {v:#}"
    );
}

#[test]
fn cohesion_is_bounded_and_keywords_are_a_bounded_list() {
    let d = two_subsystems();
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    // each subsystem is a chain of three symbols, so 2 of its 3 pairs share an edge: the
    // figure is pinned, not merely bounded, so a constant cannot pass as cohesion.
    let by_label: BTreeMap<String, f64> = read_areas(&v)
        .into_iter()
        .map(|a| (a.label, a.cohesion))
        .collect();
    assert_eq!(by_label.get("billing"), Some(&0.6666666666666666), "{v:#}");
    assert_eq!(by_label.get("shipping"), Some(&0.6666666666666666), "{v:#}");
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
        if a.label == "billing" {
            assert_eq!(
                a.keywords,
                vec!["billing".to_string()],
                "the billing area's keywords are pinned: {v:#}"
            );
        }
    }
}

/// A subsystem whose members share only `src`, a generic folder: the label must skip it
/// and fall through the rest of the chain.
#[test]
fn a_generic_folder_is_never_a_label() {
    let d = repo(&[(
        "src/only.go",
        "package only\n\nfunc A() int { return 0 }\n\nfunc B() int { return A() }\n\nfunc C() int { return B() }\n",
    )]);
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    let labels: BTreeSet<String> = read_areas(&v).into_iter().map(|a| a.label).collect();
    assert!(
        !labels.contains("src"),
        "a generic folder became a label: {labels:?}"
    );
    // The same filter governs the keywords, so a layout name cannot crowd out a subsystem
    // term there either.
    for a in read_areas(&v) {
        assert!(
            !a.keywords.iter().any(|k| k == "src"),
            "a generic folder became a keyword: {:?} in {v:#}",
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
    // The sample the answer reports, not only the constant: cohesion computed over all
    // 200 members would still report 50 here.
    assert_eq!(
        widest.cohesion_sample, 50,
        "the reported sample is not the cap: {v:#}"
    );
    // And the figure is the one a 50-member sample gives, which a 200-member count cannot
    // reproduce: the 200 members form 199 edges, so the full count is 1.0 while the first
    // 50 members by (path, line, name) are Hub plus M0..M48, of which 49 pairs share an
    // edge.
    assert_eq!(
        widest.cohesion,
        49.0 / 1225.0,
        "cohesion was not computed over the capped sample: {v:#}"
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

/// The name-prefix rule: members sharing no non-generic folder but a common leading word.
#[test]
fn a_name_prefix_is_the_label_when_no_folder_qualifies() {
    let d = repo(&[(
        "zoo.go",
        "package zoo\n\ntype Feeder struct{}\n\nfunc (f Feeder) feed() int { return 0 }\n\nfunc (f Feeder) feeder() int { return f.feed() }\n\nfunc (f Feeder) feeds() int { return f.feeder() }\n",
    )]);
    build(d.path());
    let v = areas(d.path(), &["--seed=7"]);
    // The three unexported methods are one area (their receiver is the same type, so Go
    // binds the calls to them) and its label is the longest leading word they all start
    // with, `feed`, rather than the shorter stem `fee`. The exported type they hang off
    // keeps its own area: nothing resolved calls it, so it has no area to share.
    let feed = read_areas(&v).into_iter().find(|a| a.label == "feed");
    assert_eq!(
        feed.as_ref().map(|a| a.rule.as_str()),
        Some("name_prefix"),
        "the name-prefix rule did not fire: {v:#}"
    );
    assert_eq!(
        feed.as_ref().map(|a| a.members.len()),
        Some(3),
        "the feed area does not hold the three methods: {v:#}"
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
        // (path, line, name) order: a reader of the file expects them in file order,
        // and two symbols on one source line come back in the index's name order.
        let mut sorted = a.member_rows.clone();
        sorted.sort();
        assert_eq!(
            a.member_rows, sorted,
            "members are not in (path, line, name) order: {v:#}"
        );
        let paths: Vec<&str> = a.member_rows.iter().map(|(p, _, _)| p.as_str()).collect();
        let mut sorted_paths = paths.clone();
        sorted_paths.sort();
        assert_eq!(paths, sorted_paths, "members are not in path order: {v:#}");
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

/// `out` up to the trailing `"pass_counters"` field the CLI appends, byte for byte.
fn without_pass_counters(out: &[u8]) -> &[u8] {
    let key = b",\"pass_counters\":";
    let at = out
        .windows(key.len())
        .rposition(|w| w == key)
        .expect("a JSON object result ends with pass_counters");
    &out[..at]
}
