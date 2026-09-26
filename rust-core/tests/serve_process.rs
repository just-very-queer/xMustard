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
/// readings (including the freshness observation's age), per-call ids, and per-call cache accounting (graph cache hit or miss,
/// lock waits, timings, wiki pages regenerated versus reused).
fn normalize(v: &mut Value) {
    match v {
        Value::Object(map) => {
            map.retain(|k, _| {
                !(k.ends_with("_at")
                    || k == "elapsed_ms"
                    || k == "identity_age_ms"
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
        vec!["symbolgraph", "hotspots", root, "ws", "5"],
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

/// Every resident invocation the Go bridge sends (api-go/internal/rustcore), with
/// the first argument for command families.
const GO_BRIDGE_CALLS: &[&[&str]] = &[
    &["scan-signals"],
    &["build-repo-map"],
    &["semantic-impact"],
    &["path-symbols"],
    &["explain-path"],
    &["normalize-diagnostics"],
    &["archive-diagnostics-payload"],
    &["link-diagnostic-symbol"],
    &["normalize-lsp-definition"],
    &["normalize-lsp-references"],
    &["normalize-lsp-document-symbols"],
    &["normalize-lsp-workspace-symbols"],
    &["parse-coverage-lcov"],
    &["parse-coverage"],
    &["symbolgraph", "hotspots"],
    &["symbolgraph", "impact"],
    &["symbolgraph", "trace"],
    &["symbolgraph", "clusters"],
    &["changetrack", "fingerprint"],
    &["changetrack", "drift"],
    &["changetrack", "changed-since"],
    &["changetrack", "working-changes"],
    &["changetrack", "incorporate"],
    &["changetrack", "lineage"],
    &["ownership", "subsystems"],
    &["ownership", "owners"],
    &["search"],
    &["repo-key"],
    &["wiki"],
];

#[test]
fn every_go_bridge_method_dispatches_to_its_handler() {
    // Called without its remaining arguments, each method must reach its own handler,
    // which answers with its own usage line (exit code 2), exactly as the CLI does.
    let mut worker = Worker::start(&[]);
    for call in GO_BRIDGE_CALLS {
        let err = worker.call(call[0], &call[1..]).unwrap_err();
        let cli = one_shot(call);
        assert_eq!(err["code"], COMMAND_FAILED, "{call:?}: {err}");
        assert_eq!(err["data"]["exit_code"], 2, "{call:?}: {err}");
        let message = err["message"].as_str().unwrap();
        assert!(
            message.starts_with(&format!("usage: xmustard-core {}", call.join(" "))),
            "{call:?} reached the wrong handler: {message}"
        );
        assert_eq!(cli.status.code(), Some(2), "{call:?}");
        assert_eq!(
            message,
            String::from_utf8_lossy(&cli.stderr).trim_end(),
            "{call:?}"
        );
    }
    assert_eq!(
        worker.stats()["requests"],
        GO_BRIDGE_CALLS.len(),
        "every call ran in-process"
    );
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
        vec!["symbolgraph", "blast-radius", root, "ws", "ComputeTotal"],
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
    for name in ["goal", "lsp-hover", "run-managed-command"] {
        assert!(!methods.contains(&json!(name)), "{name} must be one-shot");
    }
    assert_eq!(
        init["one_shot_subcommands"],
        json!({"changetrack": ["index"], "symbolgraph": ["build", "blast-radius"]})
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

/// A child the worker starts must not inherit the protocol stream. The supervisor
/// learns that the worker ended from end of output on its stdout, so a descendant
/// that outlives the worker (a git fsmonitor daemon, a detached helper) must not
/// keep that pipe open.
#[test]
#[cfg(unix)]
fn a_descendant_that_outlives_the_worker_does_not_hold_its_output_open() {
    use std::os::unix::fs::PermissionsExt;
    use std::sync::mpsc;

    let r = fixture();
    let root = r.path().to_str().unwrap();
    let real_git = Command::new("sh")
        .args(["-c", "command -v git"])
        .output()
        .unwrap();
    let real_git = String::from_utf8(real_git.stdout).unwrap();
    let real_git = real_git.trim();
    assert!(!real_git.is_empty(), "git must be on PATH");
    // a `git` that leaves a long-lived process behind, with its stdio detached, as a
    // daemon would, then runs the real git.
    let bin = TempDir::new().unwrap();
    let pids = bin.path().join("pids");
    let fake = bin.path().join("git");
    fs::write(
        &fake,
        format!(
            "#!/bin/sh\nsleep 30 </dev/null >/dev/null 2>&1 &\necho $! >> '{}'\nexec '{}' \"$@\"\n",
            pids.display(),
            real_git
        ),
    )
    .unwrap();
    fs::set_permissions(&fake, fs::Permissions::from_mode(0o755)).unwrap();
    let path = format!(
        "{}:{}",
        bin.path().display(),
        std::env::var("PATH").unwrap_or_default()
    );
    let mut child = Command::new(BIN)
        .arg("serve")
        .env("PATH", path)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let mut stdout = BufReader::new(child.stdout.take().unwrap());
    let body = json!({"jsonrpc": "2.0", "id": 1, "method": "repo-key", "params": {"args": [root]}});
    write_frame(&mut stdin, Some(1), &[body.to_string().as_bytes()]).unwrap();
    let header = read_header(&mut stdout).unwrap();
    let mut buf = vec![0; header.len];
    stdout.read_exact(&mut buf).unwrap();
    let reply: Value = serde_json::from_slice(&buf).unwrap();
    assert!(reply.get("result").is_some(), "{reply}");
    let left_behind: Vec<String> = fs::read_to_string(&pids)
        .unwrap_or_default()
        .split_whitespace()
        .map(str::to_string)
        .collect();
    assert!(
        !left_behind.is_empty(),
        "repo-key ran no git child, so this test proves nothing"
    );

    drop(stdin);
    let status = child.wait().unwrap();
    assert!(status.success(), "{status}");
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let mut rest = Vec::new();
        let _ = stdout.read_to_end(&mut rest);
        let _ = tx.send(rest.len());
    });
    let eof = rx.recv_timeout(Duration::from_secs(5));
    for pid in &left_behind {
        let _ = Command::new("kill").arg(pid).status();
    }
    assert!(
        eof.is_ok(),
        "the worker exited but its output never ended: a descendant holds the protocol stream"
    );
}
