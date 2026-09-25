//! Process-level contracts of `xmustard-core serve`, exercised through the real
//! binary: the resident worker answers what the one-shot CLI prints, keeps graph
//! snapshots only while they match the source identity, trims them when idle, and
//! exits when its supervisor's pipe closes.

use std::fs;
use std::io::{BufReader, Read};
use std::path::Path;
use std::process::{Child, ChildStdin, ChildStdout, Command, Stdio};
use std::time::{Duration, Instant};

use serde_json::{Value, json};
use tempfile::TempDir;
use xmustard_core::serve::{COMMAND_FAILED, METHOD_NOT_FOUND, read_header, write_frame};

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "git {args:?}");
}

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        let path = dir.path().join(rel);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(path, content).unwrap();
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn fixture() -> TempDir {
    repo(&[
        (
            "src/engine.go",
            "package engine\n\nfunc ComputeTotal(a int) int { return helperValue(a) }\n\nfunc helperValue(a int) int { return a + 1 }\n",
        ),
        (
            "src/handler.go",
            "package engine\n\nfunc HandleRequest() int { return ComputeTotal(2) }\n",
        ),
        (
            "web/view.ts",
            "export function renderWidget(): number { return 1 }\nexport class WidgetView {}\n",
        ),
        (
            "core/lib.rs",
            "pub fn parse_input(s: &str) -> usize { s.len() }\npub struct InputParser {}\n",
        ),
        ("README.md", "# Fixture\n\nComputeTotal adds one.\n"),
    ])
}

fn one_shot(args: &[&str]) -> std::process::Output {
    Command::new(BIN).args(args).output().unwrap()
}

struct Worker {
    child: Child,
    stdin: Option<ChildStdin>,
    stdout: BufReader<ChildStdout>,
    next: i64,
}

impl Worker {
    fn start(flags: &[&str]) -> Self {
        let mut child = Command::new(BIN)
            .arg("serve")
            .args(flags)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take();
        let stdout = BufReader::new(child.stdout.take().unwrap());
        Worker {
            child,
            stdin,
            stdout,
            next: 0,
        }
    }

    /// One request; Ok(result) or Err(error object).
    fn call(&mut self, method: &str, args: &[&str]) -> Result<Value, Value> {
        self.next += 1;
        let id = self.next;
        let body = json!({"jsonrpc": "2.0", "id": id, "method": method, "params": {"args": args}});
        write_frame(
            self.stdin.as_mut().unwrap(),
            Some(id),
            &[body.to_string().as_bytes()],
        )
        .unwrap();
        let header = read_header(&mut self.stdout).unwrap();
        assert_eq!(header.id, Some(id));
        let mut buf = vec![0; header.len];
        self.stdout.read_exact(&mut buf).unwrap();
        let v: Value = serde_json::from_slice(&buf).unwrap();
        assert_eq!(v["id"], id);
        match v.get("error") {
            Some(err) => Err(err.clone()),
            None => Ok(v["result"].clone()),
        }
    }

    fn stats(&mut self) -> Value {
        self.call("$/stats", &[]).unwrap()
    }
}

impl Drop for Worker {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// Drop fields that legitimately differ between two runs of the same query: clock
/// readings, per-call ids, and per-call cache accounting (graph cache hit or miss,
/// lock waits, timings, wiki pages regenerated versus reused).
fn normalize(v: &mut Value) {
    match v {
        Value::Object(map) => {
            map.retain(|k, _| {
                !(k.ends_with("_at")
                    || k == "elapsed_ms"
                    || k == "work"
                    || k == "result_id"
                    || k == "regenerated_slugs"
                    || k == "reused_slugs")
            });
            for child in map.values_mut() {
                normalize(child);
            }
        }
        Value::Array(items) => items.iter_mut().for_each(normalize),
        _ => {}
    }
}

fn normalized(mut v: Value) -> Value {
    normalize(&mut v);
    v
}

#[test]
fn worker_output_matches_one_shot_cli() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    let data = TempDir::new().unwrap();
    let data_dir = data.path().to_str().unwrap();
    // a baseline, then a dirty edit, so the change tracking reads have content.
    assert!(
        one_shot(&["changetrack", "index", data_dir, root, "ws"])
            .status
            .success()
    );
    fs::write(
        r.path().join("src/handler.go"),
        "package engine\n\nfunc HandleRequest() int { return ComputeTotal(3) }\n\nfunc HandleRetry() int { return HandleRequest() }\n",
    )
    .unwrap();
    let changes = r.path().join("changes.json");
    fs::write(
        &changes,
        json!([{"path": "src/handler.go", "status": "modified", "scope": "working_tree",
                "previous_path": null, "staged": false, "unstaged": true}])
        .to_string(),
    )
    .unwrap();
    let lcov = r.path().join("lcov.info");
    fs::write(
        &lcov,
        "SF:src/engine.go\nDA:3,1\nDA:5,0\nLF:2\nLH:1\nend_of_record\n",
    )
    .unwrap();
    let changes = changes.to_str().unwrap();
    let lcov = lcov.to_str().unwrap();

    let cases: Vec<Vec<&str>> = vec![
        vec!["repo-key", root],
        vec!["search", root, "ws", "ComputeTotal", "10"],
        vec!["search", root, "ws", "widget render", "5", "ComputeTotal"],
        vec!["symbolgraph", "impact", root, "ws", "ComputeTotal", "3"],
        vec![
            "symbolgraph",
            "trace",
            root,
            "ws",
            "HandleRequest",
            "helperValue",
        ],
        vec!["symbolgraph", "clusters", root, "ws"],
        vec!["symbolgraph", "blast-radius", root, "ws", "ComputeTotal"],
        vec!["symbolgraph", "flow", root, "ws"],
        vec!["explain-path", "ws", root, "src/engine.go"],
        vec!["path-symbols", "ws", root, "web/view.ts"],
        vec!["build-repo-map", "ws", root],
        vec!["changetrack", "fingerprint", root],
        vec!["changetrack", "drift", data_dir, root, "ws"],
        vec!["changetrack", "changed-since", data_dir, root, "ws"],
        vec!["changetrack", "working-changes", data_dir, root, "ws"],
        vec!["changetrack", "lineage", data_dir, "ws", "src/handler.go"],
        vec!["scan-signals", root],
        vec!["semantic-impact", "ws", root, changes],
        vec!["ownership", "subsystems", root, "ws"],
        vec!["ownership", "owners", root, "src/engine.go"],
        vec!["parse-coverage-lcov", "ws", lcov],
        vec!["wiki", root, "ws"],
    ];
    let mut worker = Worker::start(&["--trim-idle-ms=0"]);
    for case in &cases {
        // twice through the worker: a cold call and a warm (resident) one.
        for pass in 0..2 {
            let cli = one_shot(case);
            assert!(
                cli.status.success(),
                "{case:?}: {}",
                String::from_utf8_lossy(&cli.stderr)
            );
            let expected: Value = serde_json::from_slice(&cli.stdout).unwrap();
            let got = worker
                .call(case[0], &case[1..])
                .unwrap_or_else(|e| panic!("{case:?}: {e}"));
            assert_eq!(
                normalized(got),
                normalized(expected),
                "{case:?} pass {pass}: worker and CLI differ"
            );
        }
    }
    let stats = worker.stats();
    assert!(
        stats["snapshots"]["hits"].as_u64().unwrap() > 0,
        "graph queries never used a resident snapshot: {stats}"
    );
}

#[test]
fn failures_carry_the_cli_message_and_exit_code() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    let mut worker = Worker::start(&[]);
    for case in [
        vec!["explain-path", "ws", root, "missing.go"],
        vec!["symbolgraph", "nope"],
        vec!["search", root],
    ] {
        let cli = one_shot(&case);
        let err = worker.call(case[0], &case[1..]).unwrap_err();
        assert_eq!(err["code"], COMMAND_FAILED, "{case:?}");
        assert_eq!(
            err["data"]["exit_code"].as_i64(),
            cli.status.code().map(i64::from),
            "{case:?}"
        );
        assert_eq!(
            err["message"].as_str().unwrap(),
            String::from_utf8_lossy(&cli.stderr).trim_end(),
            "{case:?}"
        );
    }
}

#[test]
fn whole_repository_builds_are_left_to_one_shot_processes() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    let data = TempDir::new().unwrap();
    let data_dir = data.path().to_str().unwrap();
    let mut worker = Worker::start(&[]);
    for case in [
        vec!["changetrack", "index", data_dir, root, "ws"],
        vec!["symbolgraph", "build", root, "ws"],
        vec!["symbolgraph", "hotspots", root, "ws", "5"],
        vec!["symbolgraph", "build-lsp", root, "ws"],
        vec!["goal", "list", data_dir, "ws"],
        vec!["lsp-hover", root, "a.go", "1", "1"],
    ] {
        let err = worker.call(case[0], &case[1..]).unwrap_err();
        assert_eq!(err["code"], METHOD_NOT_FOUND, "{case:?}");
        assert_eq!(err["data"]["reason"], "not_resident", "{case:?}");
    }
    assert_eq!(worker.stats()["requests"], 0, "nothing ran in-process");
    let init = worker.call("initialize", &[]).unwrap();
    let methods = init["methods"].as_array().unwrap();
    for name in [
        "search",
        "symbolgraph",
        "changetrack",
        "explain-path",
        "repo-key",
    ] {
        assert!(methods.contains(&json!(name)), "{name} must be resident");
    }
    for name in ["goal", "swarm", "lsp-hover", "run-managed-command"] {
        assert!(!methods.contains(&json!(name)), "{name} must be one-shot");
    }
    assert_eq!(
        init["one_shot_subcommands"],
        json!({"changetrack": ["index"], "symbolgraph": ["build", "hotspots", "build-lsp"]})
    );
}

#[test]
fn a_resident_snapshot_is_never_served_after_the_tree_changes() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    let mut worker = Worker::start(&["--trim-idle-ms=0"]);
    let before = worker
        .call("symbolgraph", &["impact", root, "ws", "ComputeTotal", "3"])
        .unwrap();
    assert!(before["impacted"].to_string().contains("src/handler.go"));
    let warm = worker
        .call("symbolgraph", &["impact", root, "ws", "ComputeTotal", "3"])
        .unwrap();
    assert_eq!(normalized(warm), normalized(before.clone()));
    let hits = worker.stats()["snapshots"]["hits"].as_u64().unwrap();
    assert!(hits >= 1);
    // the only caller stops referencing the symbol: a stale snapshot would still list it.
    fs::write(
        r.path().join("src/handler.go"),
        "package engine\n\nfunc HandleRequest() int { return 2 }\n",
    )
    .unwrap();
    let after = worker
        .call("symbolgraph", &["impact", root, "ws", "ComputeTotal", "3"])
        .unwrap();
    assert!(
        !after["impacted"].to_string().contains("src/handler.go"),
        "served a stale snapshot after an edit: {after}"
    );
    let cli: Value = serde_json::from_slice(
        &one_shot(&["symbolgraph", "impact", root, "ws", "ComputeTotal", "3"]).stdout,
    )
    .unwrap();
    assert_eq!(normalized(after), normalized(cli));
    let search = worker
        .call("search", &[root, "ws", "HandleRequest", "5"])
        .unwrap();
    assert_eq!(
        search["coverage"]["source_identity"]["key"],
        worker.call("repo-key", &[root]).unwrap()["key"],
        "search must report the current identity"
    );
}

#[test]
fn idle_trim_drops_resident_snapshots() {
    let r = fixture();
    let root = r.path().to_str().unwrap();
    let mut worker = Worker::start(&["--trim-idle-ms=100"]);
    worker
        .call("symbolgraph", &["clusters", root, "ws"])
        .unwrap();
    assert_eq!(worker.stats()["snapshots"]["resident"], 1);
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        std::thread::sleep(Duration::from_millis(150));
        let stats = worker.stats();
        if stats["snapshots"]["resident"] == 0 {
            assert!(stats["trims"].as_u64().unwrap() >= 1);
            break;
        }
        assert!(
            Instant::now() < deadline,
            "snapshots never trimmed: {stats}"
        );
    }
    // still serves after a trim.
    assert!(
        worker
            .call("symbolgraph", &["clusters", root, "ws"])
            .is_ok()
    );
}

#[test]
fn worker_exits_when_its_input_closes() {
    let mut worker = Worker::start(&[]);
    assert!(worker.call("initialize", &[]).is_ok());
    drop(worker.stdin.take());
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if let Some(status) = worker.child.try_wait().unwrap() {
            assert!(status.success(), "{status}");
            break;
        }
        assert!(Instant::now() < deadline, "worker outlived its stdin");
        std::thread::sleep(Duration::from_millis(20));
    }
}

#[test]
fn serve_rejects_unknown_flags() {
    let out = one_shot(&["serve", "--bogus=1"]);
    assert_eq!(out.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&out.stderr).starts_with("usage: xmustard-core serve"));
}
