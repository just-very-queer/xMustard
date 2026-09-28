//! Impact v2 contracts (WS-35; PAR-IMP-04 to PAR-IMP-08, WS-64's hardened diff),
//! exercised through the real `xmustard-core` binary against disposable Git
//! repositories: typed filters, depth tiers and paging, risk, the completeness envelope,
//! class seeding, trace with per-hop edges and caps, diff-to-symbol scopes under a
//! hostile Git configuration, and import cycles.

use std::collections::BTreeSet;
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

fn commit(root: &Path, msg: &str) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", msg]);
}

fn repo(files: &[(String, String)]) -> TempDir {
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

fn files(list: &[(&str, &str)]) -> Vec<(String, String)> {
    list.iter()
        .map(|(a, b)| (a.to_string(), b.to_string()))
        .collect()
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

/// `symbolgraph <sub> [flags] <root> ws [positional]`.
fn sg(sub: &str, root: &Path, flags: &[&str], positional: &[&str]) -> Value {
    let mut args = vec!["symbolgraph", sub];
    args.extend_from_slice(flags);
    args.extend([root.to_str().unwrap(), "ws"]);
    args.extend_from_slice(positional);
    ok_json(&args)
}

fn items(v: &Value) -> Vec<(u64, Value)> {
    v["by_depth"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .flat_map(|t| {
            let d = t["depth"].as_u64().unwrap();
            t["items"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .into_iter()
                .map(move |i| (d, i))
        })
        .collect()
}

fn names(v: &Value) -> BTreeSet<String> {
    items(v)
        .into_iter()
        .filter_map(|(_, i)| i["name"].as_str().map(str::to_string))
        .collect()
}

/// A hub called from 40 files, a test file, a global-tier caller, a class with a
/// constructor and a state property, and a chain `start → middle → end`.
fn graph_fixture() -> TempDir {
    let mut list = files(&[
        (
            "src/hub.ts",
            "export function hub(): number { return 1; }\n",
        ),
        (
            "src/hub.test.ts",
            "import { hub } from './hub';\nexport function testsHub(): number { return hub(); }\n",
        ),
        (
            "src/globals.ts",
            "function globalOnly(): number { return 3; }\n",
        ),
        (
            "src/uses_global.ts",
            "export function usesGlobal(): number { return globalOnly(); }\n",
        ),
        (
            "src/widget.ts",
            "export class Widget {\n  count: number = 0;\n  constructor(n: number) { this.count = n; }\n  bump(): void { this.count = this.count + 1; }\n}\n",
        ),
        (
            "src/make.ts",
            "import { Widget } from './widget';\nexport function makeWidget(): Widget { const w = new Widget(1); w.bump(); return w; }\nexport function readCount(w: Widget): number { return w.count; }\n",
        ),
        (
            "src/chain.ts",
            "export function end(): number { return 1; }\nexport function middle(): number { return end(); }\nexport function start(): number { return middle(); }\n",
        ),
        (
            "src/island.ts",
            "export function island(): number { return 7; }\n",
        ),
    ]);
    for i in 0..40 {
        list.push((
            format!("src/callers/c{i:02}.ts"),
            format!("import {{ hub }} from '../hub';\nexport function caller{i:02}(): number {{ return hub(); }}\n"),
        ));
    }
    let mut fan = String::from("export function fan(): number {\n  let t = 0;\n");
    for i in 0..205 {
        fan.push_str(&format!("  t += leaf{i}();\n"));
    }
    fan.push_str("  return t;\n}\n");
    for i in 0..205 {
        fan.push_str(&format!("function leaf{i}(): number {{ return {i}; }}\n"));
    }
    list.push(("src/fan.ts".into(), fan));
    let r = repo(&list);
    build(r.path());
    r
}

#[test]
fn tiers_page_stably_and_summary_counts_match() {
    let r = graph_fixture();
    let full = sg("impact", r.path(), &["--limit=500"], &["hub"]);
    assert_eq!(full["graph"], "resolved", "{full:#}");
    assert_eq!(full["by_depth"][0]["label"], "WILL BREAK");
    let all: Vec<Value> = items(&full).into_iter().map(|(_, i)| i).collect();
    let total = full["counts"]["total"].as_u64().unwrap() as usize;
    assert_eq!(all.len(), total);
    let direct = full["counts"]["direct"].as_u64().unwrap();
    assert!(direct >= 40, "40 callers at depth 1: {full:#}");
    // the callers reach the hub by CALLS, at 0.9 through their import
    let c00 = all.iter().find(|i| i["name"] == "caller00").unwrap();
    assert_eq!(
        (c00["edge"].as_str(), c00["confidence"].as_f64()),
        (Some("CALLS"), Some(0.9))
    );
    assert_eq!(c00["via"], "hub");

    // pages of 7 cover depth 1 exactly once, in the same order
    let depth1: Vec<Value> = full["by_depth"][0]["items"].as_array().unwrap().clone();
    let mut paged = Vec::new();
    let mut offset = Some(0u64);
    while let Some(o) = offset {
        let page = sg(
            "impact",
            r.path(),
            &["--limit=7", &format!("--offset={o}")],
            &["hub"],
        );
        paged.extend(page["by_depth"][0]["items"].as_array().unwrap().clone());
        offset = page["by_depth"][0]["next_offset"].as_u64();
    }
    assert_eq!(paged, depth1, "pagination per depth is stable");

    let summary = sg("impact", r.path(), &["--summary-only=true"], &["hub"]);
    assert_eq!(
        summary["counts"], full["counts"],
        "summary_only counts match"
    );
    assert!(summary["by_depth"][0].get("items").is_none());
    assert_eq!(summary["risk"], full["risk"]);
    // 40 direct dependents: CRITICAL by the direct threshold (>= 30)
    assert_eq!(full["risk"], "CRITICAL", "{full:#}");
}

#[test]
fn typed_filters_tests_confidence_and_relation_types() {
    let r = graph_fixture();
    // test files are left out by default and counted
    let hub = sg("impact", r.path(), &["--limit=500"], &["hub"]);
    assert!(!names(&hub).contains("testsHub"));
    assert!(
        hub["counts"]["tests_excluded"].as_u64().unwrap() >= 1,
        "{hub:#}"
    );
    let with_tests = sg(
        "impact",
        r.path(),
        &["--limit=500", "--include-tests=true"],
        &["hub"],
    );
    assert!(names(&with_tests).contains("testsHub"));

    // a global-tier (0.5, lexical) edge falls below min_confidence
    let g = sg("impact", r.path(), &[], &["globalOnly"]);
    let caller = items(&g)
        .into_iter()
        .find(|(_, i)| i["name"] == "usesGlobal");
    let (_, caller) = caller.unwrap_or_else(|| panic!("{g:#}"));
    assert_eq!(caller["provenance"], "lexical");
    let strict = sg(
        "impact",
        r.path(),
        &["--min-confidence=0.9"],
        &["globalOnly"],
    );
    assert!(items(&strict).is_empty(), "{strict:#}");
    assert!(strict["counts"]["below_confidence"].as_u64().unwrap() >= 1);
    assert_eq!(strict["risk"], "UNKNOWN", "no dependents left: {strict:#}");

    // ACCESSES is opt-in, and carries its access mode
    let count = sg("impact", r.path(), &[], &["Widget.count"]);
    assert!(!names(&count).contains("readCount"), "{count:#}");
    let accesses = sg(
        "impact",
        r.path(),
        &["--relation-types=ACCESSES"],
        &["Widget.count"],
    );
    let read = items(&accesses)
        .into_iter()
        .find(|(_, i)| i["name"] == "readCount")
        .unwrap_or_else(|| panic!("{accesses:#}"));
    assert_eq!(
        (read.1["edge"].as_str(), read.1["access"].as_str()),
        (Some("ACCESSES"), Some("read"))
    );
    assert_eq!(accesses["relation_types"], serde_json::json!(["ACCESSES"]));

    // a value outside its range is refused, never clamped
    let bad = run(&[
        "symbolgraph",
        "impact",
        "--max-depth=33",
        r.path().to_str().unwrap(),
        "ws",
        "hub",
    ]);
    assert_eq!(
        bad.status.code(),
        Some(2),
        "{}",
        String::from_utf8_lossy(&bad.stderr)
    );
}

#[test]
fn a_class_is_seeded_with_its_constructor_and_file_and_downstream_with_members() {
    let r = graph_fixture();
    let up = sg("impact", r.path(), &[], &["Widget"]);
    let seeded: BTreeSet<&str> = up["targets"]
        .as_array()
        .unwrap()
        .iter()
        .filter_map(|t| t["seeded_by"].as_str())
        .collect();
    assert!(
        seeded.contains("constructor") && seeded.contains("file"),
        "{up:#}"
    );
    assert!(names(&up).contains("makeWidget"), "{up:#}");
    assert_eq!(up["defined_in"], serde_json::json!(["src/widget.ts"]));

    let down = sg("impact", r.path(), &["--direction=downstream"], &["start"]);
    assert_eq!(down["by_depth"][0]["label"], "DIRECT DEPENDENCY");
    assert!(
        down.get("risk").is_none(),
        "risk is scored upstream only: {down:#}"
    );
    let chain: Vec<(u64, String)> = items(&down)
        .into_iter()
        .map(|(d, i)| (d, i["name"].as_str().unwrap().to_string()))
        .collect();
    assert_eq!(chain, vec![(1, "middle".into()), (2, "end".into())]);
}

// A file target walks with the symbols it declares, since named imports point at them.
#[test]
fn a_file_target_is_seeded_with_its_declared_symbols() {
    let r = graph_fixture();
    let v = sg("impact", r.path(), &["--limit=500"], &["src/hub.ts"]);
    assert!(v["targets"].to_string().contains("\"declared\""), "{v:#}");
    assert!(names(&v).contains("caller07"), "{v:#}");
    assert_eq!(v["defined_in"], serde_json::json!(["src/hub.ts"]));
}

// A path alias the resolver does not map (tsconfig `paths`) hides in-repo dependents:
// the binding is kept as an `unresolved_module` drop, so the answer is a lower bound
// that names the importing file, never an exact zero.
#[test]
fn an_unmapped_import_alias_makes_the_answer_a_lower_bound() {
    let r = repo(&files(&[
        ("src/core/api.ts", "export function buildHandler(): number { return 1; }\n"),
        (
            "src/app.ts",
            "import { buildHandler } from '@core/api';\nexport function start(): number { return buildHandler(); }\n",
        ),
    ]));
    build(r.path());
    let v = sg("impact", r.path(), &[], &["buildHandler"]);
    assert_eq!(v["epistemic"]["completeness"], "lower_bound", "{v:#}");
    assert_eq!(v["epistemic"]["causes"]["unresolved_module"], 1, "{v:#}");
    assert!(
        v["epistemic"]["boundaries"]
            .as_array()
            .unwrap()
            .iter()
            .any(|b| b["path"] == "src/app.ts" && b["cause"] == "unresolved_module"),
        "{v:#}"
    );
    assert_eq!(v["risk"], "UNKNOWN");
}

#[test]
fn zero_upstream_callers_is_unknown_with_a_note() {
    let r = graph_fixture();
    let v = sg("impact", r.path(), &[], &["island"]);
    assert_eq!(v["risk"], "UNKNOWN", "{v:#}");
    assert!(
        v["risk_note"]
            .as_str()
            .unwrap()
            .contains("no dependents found")
    );
    let missing = sg("impact", r.path(), &[], &["noSuchSymbolAnywhere"]);
    assert_eq!(missing["risk"], "UNKNOWN");
    assert_eq!(missing["graph"], "none");
}

#[test]
fn trace_reports_hops_direction_furthest_and_caps() {
    let r = graph_fixture();
    let t = sg("trace", r.path(), &[], &["start", "end"]);
    assert_eq!(t["found"], true, "{t:#}");
    assert_eq!(t["direction"], "downstream");
    assert_eq!(t["length"], 2);
    let hop = &t["hops"][0];
    assert_eq!(
        (hop["from"]["name"].as_str(), hop["to"]["name"].as_str()),
        (Some("start"), Some("middle"))
    );
    assert_eq!(hop["edge"], "CALLS");
    assert_eq!(hop["provenance"], "scope");
    assert!(hop["confidence"].as_f64().unwrap() > 0.9);

    // reversed ends are found upstream
    let back = sg("trace", r.path(), &[], &["end", "start"]);
    assert_eq!(
        (back["found"].as_bool(), back["direction"].as_str()),
        (Some(true), Some("upstream"))
    );

    // no path: the furthest node and its depth
    let none = sg("trace", r.path(), &[], &["start", "island"]);
    assert_eq!(none["found"], false);
    assert_eq!(none["furthest"]["name"], "end", "{none:#}");
    assert_eq!(none["furthest"]["depth"], 2);
    assert_eq!(none["truncated"], false);

    // 205 callees: the per-node fan-out cap is hit and said
    let fan = sg("trace", r.path(), &[], &["fan", "island"]);
    assert_eq!(fan["found"], false);
    assert_eq!(fan["caps_hit"]["fanout"], 1, "{fan:#}");
    assert_eq!(fan["truncated"], true);
    assert_eq!(fan["caps"]["fanout"], 200);
    // a shallow cap
    let shallow = sg("trace", r.path(), &["--max-depth=1"], &["start", "end"]);
    assert_eq!(shallow["found"], false);
    assert_eq!(shallow["caps_hit"]["depth"], true, "{shallow:#}");
}

// ---------------------------------------------------------------------------
// detect_changes
// ---------------------------------------------------------------------------

const SVC: &str = "export function greet(name: string): string {\n  return 'hi ' + name;\n}\n\nexport function other(): number {\n  return 2;\n}\n";
const SVC_EDITED: &str = "// a new first line moves every span down\nexport function greet(name: string, loud: boolean): string {\n  return 'hi ' + name;\n}\n\nexport function other(): number {\n  return 3;\n}\n\nexport function added(): number { return 4; }\n";
const APP: &str =
    "import { greet } from './svc';\nexport function main(): string { return greet('x'); }\n";

fn changes_fixture() -> TempDir {
    let r = repo(&files(&[("src/svc.ts", SVC), ("src/app.ts", APP)]));
    build(r.path());
    r
}

fn changes(root: &Path, flags: &[&str]) -> Value {
    sg("changes", root, flags, &[])
}

/// (name, change, contract_break, line) of each changed symbol.
fn changed(v: &Value) -> BTreeSet<(String, String, bool, u64)> {
    v["changed_symbols"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .map(|s| {
            (
                s["name"].as_str().unwrap().to_string(),
                s["change"].as_str().unwrap().to_string(),
                s["contract_break"].as_bool().unwrap(),
                s["line"].as_u64().unwrap(),
            )
        })
        .collect()
}

fn edit_without_reindexing(root: &Path) {
    write(root, "src/svc.ts", SVC_EDITED);
    write(
        root,
        "src/fresh.ts",
        "export function fresh(): number { return 5; }\n",
    );
}

#[test]
fn changes_map_hunks_to_current_spans_with_untracked_files_and_contract_breaks() {
    let r = changes_fixture();
    edit_without_reindexing(r.path());
    let all = changes(r.path(), &["--scope=all"]);
    let want: BTreeSet<(String, String, bool, u64)> = [
        ("added", "added", false, 10),
        ("fresh", "added", false, 1),
        ("greet", "modified", true, 2),
        ("other", "modified", false, 6),
    ]
    .into_iter()
    .map(|(n, c, b, l)| (n.to_string(), c.to_string(), b, l))
    .collect();
    assert_eq!(changed(&all), want, "{all:#}");
    assert_eq!(all["scope"], "all");
    assert_eq!(all["untracked"], true);
    assert_eq!(all["contract_breaks"], 1);
    let greet = all["changed_symbols"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["name"] == "greet")
        .unwrap();
    assert_eq!(greet["signature_change"], "signature");
    // main calls it and src/app.ts imports it
    assert_eq!(greet["direct_dependents"], 2, "{all:#}");
    assert_eq!(greet["graph_match"], "moved", "the index still has line 1");
    let fresh = all["changed_symbols"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["name"] == "fresh")
        .unwrap();
    assert_eq!(fresh["graph_match"], "none", "untracked and unindexed");
    // the callers of the changed symbols, by depth, with risk
    let affected = &all["affected"];
    assert!(names(affected).contains("main"), "{affected:#}");
    assert!(affected["risk"].is_string());

    // staged sees nothing yet; unstaged sees the edits and the untracked file
    let staged = changes(r.path(), &["--scope=staged"]);
    assert_eq!(staged["changed_files_total"], 0, "{staged:#}");
    let unstaged = changes(r.path(), &["--scope=unstaged"]);
    assert_eq!(changed(&unstaged), want);
}

#[test]
fn changes_read_commits_ranges_and_compare_and_refuse_bad_revisions() {
    let r = changes_fixture();
    write(r.path(), "src/svc.ts", SVC_EDITED);
    commit(r.path(), "edit");
    let commit_scope = changes(r.path(), &["--scope=commit"]);
    let names: BTreeSet<String> = changed(&commit_scope).into_iter().map(|c| c.0).collect();
    assert_eq!(
        names,
        ["added", "greet", "other"].map(String::from).into(),
        "{commit_scope:#}"
    );
    assert!(commit_scope["head"].as_str().unwrap().len() >= 40);
    let range = changes(r.path(), &["--scope=range", "--base=HEAD~1"]);
    assert_eq!(changed(&range), changed(&commit_scope));
    assert!(range["merge_base"].is_string());
    let compare = changes(r.path(), &["--scope=compare", "--base=HEAD~1"]);
    assert_eq!(changed(&compare), changed(&commit_scope));
    // a removed exported symbol breaks its callers' contract
    write(
        r.path(),
        "src/svc.ts",
        "export function greet(name: string, loud: boolean): string {\n  return name;\n}\n",
    );
    let removed = changes(r.path(), &["--scope=all"]);
    let gone = removed["changed_symbols"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["name"] == "other")
        .unwrap_or_else(|| panic!("{removed:#}"));
    assert_eq!(
        (gone["change"].as_str(), gone["contract_break"].as_bool()),
        (Some("removed"), Some(true))
    );

    let root = r.path().to_str().unwrap();
    for bad in [
        vec!["--scope=range"],
        vec!["--scope=compare", "--base=-x"],
        vec!["--scope=range", "--base=no-such-ref"],
        vec!["--scope=staged", "--base=HEAD"],
        vec!["--scope=sideways"],
    ] {
        let mut args = vec!["symbolgraph", "changes"];
        args.extend(bad.iter().copied());
        args.extend([root, "ws"]);
        let out = run(&args);
        assert!(!out.status.success(), "{bad:?} must be refused");
    }
}

/// A repository configuration that changes what `git diff` prints.
fn make_hostile(root: &Path) {
    fs::write(root.join(".git/order"), "src/svc.ts\n").unwrap();
    fs::write(root.join(".git/info/attributes"), "*.ts diff=boom\n").unwrap();
    for (k, v) in [
        ("diff.noprefix", "true"),
        ("diff.mnemonicPrefix", "true"),
        ("diff.algorithm", "histogram"),
        ("diff.indentHeuristic", "false"),
        ("diff.context", "10"),
        ("diff.interHunkContext", "20"),
        ("diff.renames", "copies"),
        ("diff.relative", "true"),
        ("diff.external", "false"),
        ("diff.boom.textconv", "false"),
        ("diff.suppressBlankEmpty", "true"),
        ("color.ui", "always"),
        ("color.diff", "always"),
        ("core.quotePath", "true"),
        ("core.bigFileThreshold", "1"),
        ("diff.orderFile", root.join(".git/order").to_str().unwrap()),
    ] {
        git(root, &["config", k, v]);
    }
}

// PAR-IMP-06 / WS-64: the same changed files and symbols under a repository config and
// an environment that each change what `git diff` prints.
#[test]
fn pinned_git_flags_parse_files_under_a_hostile_git_config() {
    let r = changes_fixture();
    edit_without_reindexing(r.path());
    // a non-ASCII name, quoted by core.quotePath=true
    write(
        r.path(),
        "src/grüße.ts",
        "export function gruss(): number { return 6; }\n",
    );
    let clean = changes(r.path(), &["--scope=all"]);
    make_hostile(r.path());
    let root = r.path().to_str().unwrap().to_string();
    let out = Command::new(BIN)
        .args(["symbolgraph", "changes", "--scope=all", root.as_str(), "ws"])
        .env("GIT_DIR", "/nonexistent/elsewhere")
        .env("GIT_EXTERNAL_DIFF", "false")
        .env("GIT_DIFF_OPTS", "--unified=9")
        .env("GIT_INDEX_FILE", "/nonexistent/index")
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let hostile: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert_eq!(changed(&hostile), changed(&clean), "{hostile:#}");
    assert!(changed(&clean).iter().any(|c| c.0 == "gruss"), "{clean:#}");
    let paths = |v: &Value| -> Vec<String> {
        v["changed_files"]
            .as_array()
            .unwrap()
            .iter()
            .map(|f| f["path"].as_str().unwrap().to_string())
            .collect()
    };
    assert_eq!(paths(&hostile), paths(&clean));
    assert!(paths(&clean).contains(&"src/grüße.ts".to_string()));
}

// The diff module never runs a repository's fsmonitor hook (other core Git reads are
// outside it, so this drives the module itself).
#[test]
fn the_diff_module_never_runs_the_fsmonitor_hook() {
    use xmustard_core::gitdiff::{self, Limits, Mode, Scope};
    let r = changes_fixture();
    edit_without_reindexing(r.path());
    let marker = TempDir::new().unwrap();
    let marker = marker.path().join("fsmonitor-ran");
    let hook = r.path().join(".git/fsmonitor.sh");
    fs::write(&hook, format!("#!/bin/sh\ntouch {}\n", marker.display())).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&hook, fs::Permissions::from_mode(0o755)).unwrap();
    }
    git(
        r.path(),
        &["config", "core.fsmonitor", hook.to_str().unwrap()],
    );
    for scope in [Scope::Unstaged, Scope::Staged, Scope::All] {
        let res = gitdiff::resolve(r.path(), scope, None, None).unwrap();
        let d = gitdiff::diff(r.path(), &res, Mode::Symbols, Limits::default()).unwrap();
        if scope != Scope::Staged {
            assert!(d.files.iter().any(|f| f.path == "src/svc.ts"), "{scope:?}");
        }
    }
    let (untracked, _) = gitdiff::untracked(r.path(), 10).unwrap();
    assert_eq!(untracked, vec!["src/fresh.ts".to_string()]);
    let blobs = gitdiff::read_objects(
        r.path(),
        &[":src/svc.ts".into(), "HEAD:nope".into()],
        1 << 20,
    )
    .unwrap();
    assert_eq!(blobs[0].as_deref(), Some(SVC.as_bytes()));
    assert!(blobs[1].is_none());
    assert!(!marker.exists(), "the repository's fsmonitor hook ran");
}

#[test]
fn a_worktree_must_be_a_checkout_of_the_same_repository() {
    let r = changes_fixture();
    let parent = TempDir::new().unwrap();
    let wt = parent.path().join("wt2");
    git(r.path(), &["worktree", "add", "-q", wt.to_str().unwrap()]);
    write(&wt, "src/svc.ts", SVC_EDITED);
    let flag = format!("--worktree={}", wt.display());
    let v = changes(r.path(), &["--scope=all", &flag]);
    assert!(changed(&v).iter().any(|c| c.0 == "greet" && c.2), "{v:#}");
    assert!(v["notes"].to_string().contains("worktree"));

    let other = repo(&files(&[("x.ts", "export function x() {}\n")]));
    let root = r.path().to_str().unwrap();
    for bad in [other.path().to_path_buf(), parent.path().join("missing")] {
        let flag = format!("--worktree={}", bad.display());
        let out = run(&["symbolgraph", "changes", "--scope=all", &flag, root, "ws"]);
        assert!(!out.status.success(), "{} must be refused", bad.display());
    }
}

// ---------------------------------------------------------------------------
// import cycles
// ---------------------------------------------------------------------------

fn imp(from: &str, to: &str, kind: &str) -> String {
    match kind {
        "value" => format!("import {{ v_{to} }} from './{to}';\n"),
        "type" => format!("import type {{ T_{to} }} from './{to}';\n"),
        "dynamic" => format!("export async function load_{to}() {{ return import('./{to}'); }}\n"),
        "lazy" => format!("export function req_{to}() {{ return require('./{to}'); }}\n"),
        _ => unreachable!("{from}"),
    }
}

fn module(name: &str, imports: &[(&str, &str)]) -> (String, String) {
    let mut body: String = imports.iter().map(|(to, k)| imp(name, to, k)).collect();
    body.push_str(&format!(
        "export const v_{name} = 1;\nexport type T_{name} = number;\n"
    ));
    (format!("{name}.ts"), body)
}

#[test]
fn import_cycles_count_components_and_skip_lazy_and_type_only_imports() {
    let r = repo(&[
        // a 3-cycle and a 2-cycle of value imports
        module("a", &[("b", "value")]),
        module("b", &[("c", "value")]),
        module("c", &[("a", "value")]),
        module("d", &[("e", "value")]),
        module("e", &[("d", "value")]),
        // cycles only through type-only, dynamic or in-function imports: none
        module("f", &[("g", "type")]),
        module("g", &[("f", "type")]),
        module("h", &[("i", "dynamic")]),
        module("i", &[("h", "value")]),
        module("j", &[("k", "lazy")]),
        module("k", &[("j", "value")]),
    ]);
    build(r.path());
    let cov = sg("coverage", r.path(), &[], &[]);
    let cycles = &cov["import_cycles"];
    assert_eq!(cycles["component_count"], 2, "{cov:#}");
    assert_eq!(cycles["cycle_count"], 2);
    assert_eq!(cycles["largest"], 3);
    // impact through a cyclic file lists its component with a closed representative
    let v = sg("impact", r.path(), &[], &["v_a"]);
    let comp = &v["import_cycles"][0];
    assert_eq!(comp["size"], 3, "{v:#}");
    let rep: Vec<&str> = comp["representative"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p.as_str().unwrap())
        .collect();
    assert_eq!(rep, vec!["a.ts", "b.ts", "c.ts", "a.ts"]);
}

#[test]
fn a_cycle_blowup_keeps_the_component_count_and_one_representative() {
    // eight modules that all import each other: 16,064 elementary cycles, past the cap
    let names: Vec<String> = (0..8).map(|i| format!("m{i}")).collect();
    let list: Vec<(String, String)> = names
        .iter()
        .map(|n| {
            let others: Vec<(&str, &str)> = names
                .iter()
                .filter(|o| *o != n)
                .map(|o| (o.as_str(), "value"))
                .collect();
            module(n, &others)
        })
        .collect();
    let r = repo(&list);
    build(r.path());
    let cov = sg("coverage", r.path(), &[], &[]);
    let cycles = &cov["import_cycles"];
    assert_eq!(cycles["component_count"], 1, "{cov:#}");
    assert!(cycles["cycle_count"].is_null(), "truncated: {cov:#}");
    assert_eq!(cycles["largest"], 8);
    let v = sg("impact", r.path(), &[], &["v_m0"]);
    let rep = v["import_cycles"][0]["representative"].as_array().unwrap();
    assert_eq!(rep.first(), rep.last(), "a closed cycle: {v:#}");
    assert_eq!(
        rep.len(),
        3,
        "the shortest cycle through m0 is m0 -> m1 -> m0"
    );
}
