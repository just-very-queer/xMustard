//! Per-call pass counters (WS-RT1): each source of a whole-repository pass is counted
//! for the call that caused it, and the CLI and the `serve` worker report the counts
//! as a trailing `pass_counters` object.

use std::fs;
use std::io::{BufReader, Read};
use std::path::Path;
use std::process::{Command, Stdio};

use serde_json::{Value, json};
use tempfile::TempDir;
use xmustard_core::index::{self, config::IndexConfig, scan};
use xmustard_core::indexcache::{git_timeout, run_git_bounded};
use xmustard_core::passcount::{Call, Pass};
use xmustard_core::serve::{read_header, write_frame};

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

const NAMES: [&str; 6] = [
    "files_listed",
    "files_statted",
    "files_hashed",
    "bytes_hashed",
    "git_spawns",
    "rows_loaded",
];

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "git {args:?}");
}

/// A committed repository holding three Go files and nothing else.
fn three_go_files() -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, body) in [
        ("a.go", "package p\n\nfunc Alpha() int { return 1 }\n"),
        ("b.go", "package p\n\nfunc Beta() int { return Alpha() }\n"),
        ("c.go", "package p\n\nfunc Gamma() int { return Beta() }\n"),
    ] {
        fs::write(dir.path().join(rel), body).unwrap();
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

#[test]
fn a_bounded_listing_counts_one_spawn_and_its_records() {
    let r = three_go_files();
    let call = Call::begin();
    run_git_bounded(r.path(), &["ls-files", "-z"], 1 << 20, git_timeout()).unwrap();
    let c = call.counts();
    assert_eq!(c.get(Pass::GitSpawns), 1);
    assert_eq!(c.get(Pass::FilesListed), 3);
    assert_eq!(c.get(Pass::FilesHashed), 0);
}

#[test]
fn a_non_listing_git_command_lists_nothing() {
    let r = three_go_files();
    let call = Call::begin();
    run_git_bounded(r.path(), &["rev-parse", "HEAD"], 1 << 20, git_timeout()).unwrap();
    let c = call.counts();
    assert_eq!(c.get(Pass::GitSpawns), 1);
    assert_eq!(c.get(Pass::FilesListed), 0);
}

#[test]
fn hashing_two_files_counts_each_file_and_its_bytes() {
    let dir = TempDir::new().unwrap();
    fs::write(dir.path().join("five.txt"), b"12345").unwrap();
    fs::write(dir.path().join("seven.txt"), b"1234567").unwrap();
    let call = Call::begin();
    let (map, _) = xmustard_core::hashcache::hash_files(
        dir.path(),
        vec!["five.txt".into(), "seven.txt".into()],
        None,
    );
    assert_eq!(map.len(), 2);
    let c = call.counts();
    assert_eq!(c.get(Pass::FilesHashed), 2);
    assert_eq!(c.get(Pass::BytesHashed), 12);
    assert_eq!(c.get(Pass::FilesStatted), 2);
    assert_eq!(c.get(Pass::GitSpawns), 0);
}

#[test]
fn the_index_scan_lists_and_stats_each_tracked_file() {
    let r = three_go_files();
    let cfg = IndexConfig::default();
    let layout = scan::layout(r.path(), &cfg).unwrap();
    let call = Call::begin();
    let s = scan::scan(&layout, &cfg).unwrap();
    assert_eq!(s.candidates.len(), 3);
    let c = call.counts();
    assert_eq!(c.get(Pass::FilesListed), 3);
    assert_eq!(c.get(Pass::FilesStatted), 3);
    assert_eq!(c.get(Pass::FilesHashed), 0);
}

#[test]
fn an_update_loads_the_stored_file_rows() {
    let r = three_go_files();
    let cfg = IndexConfig::default();
    index::build(r.path(), &cfg).unwrap();
    let call = Call::begin();
    let report = index::update(r.path(), &cfg, None).unwrap();
    assert_eq!(report.mode, "noop");
    assert_eq!(call.counts().get(Pass::RowsLoaded), 3);
}

#[test]
fn a_full_build_hashes_each_file_it_parses() {
    let r = three_go_files();
    let bytes: u64 = ["a.go", "b.go", "c.go"]
        .iter()
        .map(|f| fs::metadata(r.path().join(f)).unwrap().len())
        .sum();
    let call = Call::begin();
    index::build(r.path(), &IndexConfig::default()).unwrap();
    let c = call.counts();
    assert_eq!(c.get(Pass::FilesHashed), 3);
    assert_eq!(c.get(Pass::BytesHashed), bytes);
}

fn counters_of(v: &Value) -> &serde_json::Map<String, Value> {
    let pc = v["pass_counters"]
        .as_object()
        .unwrap_or_else(|| panic!("no pass_counters object in {v}"));
    let keys: Vec<&str> = pc.keys().map(String::as_str).collect();
    let mut want = NAMES.to_vec();
    want.sort_unstable();
    let mut got = keys.clone();
    got.sort_unstable();
    assert_eq!(got, want, "pass_counters keys");
    pc
}

#[test]
fn the_cli_reports_the_calls_counters_last() {
    let r = three_go_files();
    let out = Command::new(BIN)
        .args(["index", "build", r.path().to_str().unwrap()])
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let text = String::from_utf8(out.stdout).unwrap();
    let v: Value = serde_json::from_str(&text).unwrap();
    let pc = counters_of(&v);
    assert_eq!(pc["files_listed"], 3);
    assert_eq!(pc["files_hashed"], 3);
    assert!(pc["git_spawns"].as_u64().unwrap() >= 2, "{v}");
    let tail = text.trim_end();
    let start = tail.rfind("\"pass_counters\":").unwrap();
    assert_eq!(
        &tail[tail.len() - 2..],
        "}}",
        "pass_counters is the last field"
    );
    assert!(
        !tail[start..].contains('['),
        "pass_counters is the last field"
    );
}

#[test]
fn an_array_result_carries_no_counters() {
    let r = three_go_files();
    let out = Command::new(BIN)
        .args(["scan-signals", r.path().to_str().unwrap()])
        .output()
        .unwrap();
    assert!(out.status.success());
    let v: Value = serde_json::from_slice(&out.stdout).unwrap();
    assert!(v.is_array(), "{v}");
}

#[test]
fn each_worker_response_carries_its_own_counters_and_stats_the_totals() {
    let r = three_go_files();
    fs::write(r.path().join("d.go"), b"package p").unwrap();
    let root = r.path().to_str().unwrap();
    let mut child = Command::new(BIN)
        .arg("serve")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let mut stdout = BufReader::new(child.stdout.take().unwrap());
    let mut call = |id: i64, method: &str, args: &[&str]| -> Value {
        let body = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": {"args": args}});
        write_frame(&mut stdin, Some(id), &[body.to_string().as_bytes()]).unwrap();
        let header = read_header(&mut stdout).unwrap();
        let mut buf = vec![0; header.len];
        stdout.read_exact(&mut buf).unwrap();
        let v: Value = serde_json::from_slice(&buf).unwrap();
        v["result"].clone()
    };
    let first = call(1, "repo-key", &[root]);
    let one = counters_of(&first).clone();
    assert_eq!(one["files_hashed"], 1, "the untracked d.go: {first}");
    assert_eq!(one["bytes_hashed"], 9, "{first}");
    assert!(one["git_spawns"].as_u64().unwrap() >= 1, "{first}");
    let second = call(2, "repo-key", &[root]);
    let two = counters_of(&second).clone();
    let stats = call(3, "$/stats", &[]);
    assert!(
        stats.get("pass_counters").is_none(),
        "$/stats is not a call: {stats}"
    );
    for name in NAMES {
        let sum = one[name].as_u64().unwrap() + two[name].as_u64().unwrap();
        assert_eq!(
            stats["passes"][name].as_u64(),
            Some(sum),
            "{name}: the totals are the two calls' counts: {stats}"
        );
    }
    drop(stdin);
    let _ = child.wait();
}
