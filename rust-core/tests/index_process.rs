//! Process-level contracts of the shared index cache and `repo-key`, exercised through
//! the real `xmustard-core` binary against disposable Git repositories.

use std::fs;
use std::path::Path;
use std::process::{Command, Output, Stdio};
use std::time::Duration;

use serde_json::Value;
use tempfile::TempDir;
use xmustard_core::indexcache::{cache_scope, source_identity};

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
        fs::write(dir.path().join(rel), content).unwrap();
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn run(args: &[&str], env: &[(&str, &str)]) -> Output {
    let mut cmd = Command::new(BIN);
    cmd.args(args);
    for (k, v) in env {
        cmd.env(k, v);
    }
    cmd.output().unwrap()
}

fn json(out: &Output) -> Value {
    assert!(
        out.status.success(),
        "exit {:?}: {}",
        out.status,
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn search(root: &Path, ws: &str, query: &str, env: &[(&str, &str)]) -> Value {
    json(&run(
        &["search", root.to_str().unwrap(), ws, query, "5"],
        env,
    ))
}

#[test]
fn repo_key_contract_matches_cache_identity() {
    let r = repo(&[("a.rs", "pub fn alpha_symbol() {}\n")]);
    let root = r.path().to_str().unwrap();
    let k0 = json(&run(&["repo-key", root], &[]));
    for field in [
        "key",
        "head",
        "parser_version",
        "identity_complete",
        "limitations",
    ] {
        assert!(k0.get(field).is_some(), "repo-key missing {field}: {k0}");
    }
    assert_eq!(k0["identity_complete"], true);
    assert_eq!(k0["limitations"], serde_json::json!([]));
    assert_eq!(k0["head"].as_str().unwrap().len(), 40);

    // the key search reports is the key repo-key reports for the same tree.
    let s = search(r.path(), "ws", "alpha", &[]);
    assert_eq!(s["coverage"]["source_identity"]["key"], k0["key"]);
    assert_eq!(s["coverage"]["source_identity"]["stable"], true);

    // dirty edit and untracked bytes both change it.
    fs::write(r.path().join("a.rs"), "pub fn alphb_symbol() {}\n").unwrap();
    let k1 = json(&run(&["repo-key", root], &[]));
    fs::write(r.path().join("notes.txt"), "one").unwrap();
    let k2 = json(&run(&["repo-key", root], &[]));
    fs::write(r.path().join("notes.txt"), "two").unwrap();
    let k3 = json(&run(&["repo-key", root], &[]));
    assert!(k0["key"] != k1["key"] && k1["key"] != k2["key"] && k2["key"] != k3["key"]);

    // an oversized untracked file is an explicit limitation object.
    fs::write(r.path().join("huge.bin"), vec![b'x'; (8 << 20) + 1]).unwrap();
    let k4 = json(&run(&["repo-key", root], &[]));
    assert_eq!(k4["identity_complete"], false);
    let lim = &k4["limitations"][0];
    assert_eq!(lim["path"], "huge.bin");
    assert_eq!(lim["reason"], "oversized");
    assert!(lim["detail"].as_str().unwrap().contains("not read"));

    // outside Git: exit 0, incomplete, reason git_unavailable.
    let plain = TempDir::new().unwrap();
    let k5 = json(&run(&["repo-key", plain.path().to_str().unwrap()], &[]));
    assert_eq!(k5["identity_complete"], false);
    assert_eq!(k5["limitations"][0]["reason"], "git_unavailable");

    // usage error keeps exit code 2.
    assert_eq!(run(&["repo-key"], &[]).status.code(), Some(2));
}

#[test]
fn snapshot_is_shared_across_workspaces_but_not_trust_scopes() {
    let r = repo(&[("a.rs", "pub fn shared_symbol() {}\n")]);
    let first = search(r.path(), "agent-one", "shared_symbol", &[]);
    assert_eq!(first["coverage"]["work"]["graph_cache"], "miss");
    let second = search(r.path(), "agent-two", "shared_symbol", &[]);
    assert_eq!(second["coverage"]["work"]["graph_cache"], "hit");
    assert_eq!(second["workspace_id"], "agent-two");
    assert_eq!(second["coverage"]["work"]["files_read"], 0);

    let other = search(
        r.path(),
        "agent-two",
        "shared_symbol",
        &[("XMUSTARD_INDEX_TRUST_SCOPE", "tenant-b")],
    );
    assert_eq!(
        other["coverage"]["work"]["graph_cache"], "miss",
        "a different trust scope must not reuse the snapshot"
    );
}

#[test]
fn competing_builders_are_serialized_across_processes() {
    let r = repo(&[("a.rs", "pub fn locked_symbol() {}\n")]);
    let id = source_identity(r.path());
    let scope = cache_scope(&id).unwrap();
    let (held, _) = scope.lock_build(Duration::from_secs(5));
    assert!(held.is_some());

    let root = r.path().to_str().unwrap();
    let spawn = || {
        Command::new(BIN)
            .args(["search", root, "ws", "locked_symbol", "5"])
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap()
    };
    let mut a = spawn();
    let mut b = spawn();
    // readiness handshake: release only once both builders hold build.lock open,
    // i.e. they reached the lock loop (not a scheduling-dependent sleep).
    let lock_path = fs::canonicalize(scope.dir.join("build.lock")).unwrap();
    let deadline = std::time::Instant::now() + Duration::from_secs(20);
    while !(holds_open(a.id(), &lock_path) && holds_open(b.id(), &lock_path)) {
        assert!(
            a.try_wait().unwrap().is_none(),
            "builder A finished while the lock was held"
        );
        assert!(
            b.try_wait().unwrap().is_none(),
            "builder B finished while the lock was held"
        );
        assert!(
            std::time::Instant::now() < deadline,
            "builders never reached the lock"
        );
        std::thread::sleep(Duration::from_millis(20));
    }
    std::thread::sleep(Duration::from_millis(100));
    assert!(a.try_wait().unwrap().is_none() && b.try_wait().unwrap().is_none());
    drop(held);

    let outs: Vec<Value> = [a, b]
        .into_iter()
        .map(|c| json(&c.wait_with_output().unwrap()))
        .collect();
    let caches: Vec<&str> = outs
        .iter()
        .map(|o| o["coverage"]["work"]["graph_cache"].as_str().unwrap())
        .collect();
    let mut sorted = caches.clone();
    sorted.sort();
    assert_eq!(
        sorted,
        ["hit", "miss"],
        "exactly one process builds: {caches:?}"
    );
    for o in &outs {
        assert_eq!(o["coverage"]["work"]["lock"], "acquired");
        // each builder was observed in the lock loop before release, so both waited.
        assert_eq!(o["coverage"]["work"]["lock_contended"], true, "{o}");
        assert!(
            o["coverage"]["work"]["lock_wait_ms"].as_u64().unwrap() >= 100,
            "{o}"
        );
        assert_eq!(o["hits"][0]["name"], "locked_symbol");
    }
    let graphs = fs::read_dir(&scope.dir)
        .unwrap()
        .flatten()
        .filter(|e| e.file_name().to_string_lossy().starts_with("graph-"))
        .count();
    assert_eq!(graphs, 1, "exactly one valid snapshot");
}

/// Whether process `pid` has `path` open: `/proc/<pid>/fd` on Linux, `lsof` elsewhere.
/// (Portability: needs `/proc` or `lsof`; macOS ships `lsof`. Without either the
/// handshake fails at its deadline rather than passing silently.)
fn holds_open(pid: u32, path: &Path) -> bool {
    let proc_fd = format!("/proc/{pid}/fd");
    if let Ok(entries) = fs::read_dir(&proc_fd) {
        return entries
            .flatten()
            .any(|e| fs::read_link(e.path()).is_ok_and(|t| t == path));
    }
    Command::new("lsof")
        .args(["-a", "-p", &pid.to_string(), "-Fn"])
        .output()
        .is_ok_and(|o| {
            String::from_utf8_lossy(&o.stdout)
                .lines()
                .any(|l| l.strip_prefix('n').is_some_and(|p| Path::new(p) == path))
        })
}

#[test]
fn lock_wait_is_bounded_and_reported() {
    let r = repo(&[("a.rs", "pub fn timeout_symbol() {}\n")]);
    let scope = cache_scope(&source_identity(r.path())).unwrap();
    let (held, _) = scope.lock_build(Duration::from_secs(5));
    let out = search(
        r.path(),
        "ws",
        "timeout_symbol",
        &[("XMUSTARD_INDEX_LOCK_TIMEOUT_MS", "200")],
    );
    drop(held);
    let work = &out["coverage"]["work"];
    assert_eq!(work["lock"], "timeout");
    assert_eq!(work["graph_cache"], "bypass");
    assert!(work["lock_wait_ms"].as_u64().unwrap() >= 200);
    assert_eq!(
        out["hits"][0]["name"], "timeout_symbol",
        "answer still served"
    );
    assert!(
        scope.load_graph(&source_identity(r.path()).key).is_none(),
        "stored without the lock"
    );
}

/// A PATH shim that fails `git status` and delegates every other Git command.
#[cfg(unix)]
fn status_failing_git(dir: &Path) -> String {
    use std::os::unix::fs::PermissionsExt;
    let real = String::from_utf8(Command::new("which").arg("git").output().unwrap().stdout)
        .unwrap()
        .trim()
        .to_string();
    let shim = dir.join("git");
    fs::write(
        &shim,
        format!(
            "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = status ]; then echo 'shim: status disabled' >&2; exit 128; fi\ndone\nexec '{real}' \"$@\"\n"
        ),
    )
    .unwrap();
    fs::set_permissions(&shim, fs::Permissions::from_mode(0o755)).unwrap();
    format!("{}:{}", dir.display(), std::env::var("PATH").unwrap())
}

/// Fable F3: when `git status` fails the identity is incomplete, the overall coverage
/// must not read `complete=true`, and no cached (possibly stale) graph is served.
#[cfg(unix)]
#[test]
fn git_status_failure_is_incomplete_and_never_serves_stale() {
    let r = repo(&[("a.rs", "pub fn version_one_symbol() {}\n")]);
    let warm = search(r.path(), "ws", "version_one_symbol", &[]);
    assert_eq!(warm["coverage"]["work"]["graph_cache"], "miss");
    fs::write(r.path().join("a.rs"), "pub fn version_two_symbol() {}\n").unwrap();

    let shim_dir = TempDir::new().unwrap();
    let path = status_failing_git(shim_dir.path());
    let env = [("PATH", path.as_str())];
    let key = json(&run(&["repo-key", r.path().to_str().unwrap()], &env));
    assert_eq!(key["identity_complete"], false);
    assert_eq!(key["limitations"][0]["reason"], "git_status_failed");

    let out = search(r.path(), "ws", "version_two_symbol", &env);
    let cov = &out["coverage"];
    assert_eq!(cov["source_identity"]["identity_complete"], false);
    assert_eq!(
        cov["complete"], false,
        "complete=true with an incomplete identity: {cov}"
    );
    assert_eq!(cov["work"]["graph_cache"], "bypass");
    assert!(
        cov["work"]["graph_cache_detail"]
            .as_str()
            .unwrap()
            .contains("git_status_failed"),
        "bypass reason not named: {}",
        cov["work"]
    );
    assert_eq!(
        out["hits"][0]["name"], "version_two_symbol",
        "stale graph served"
    );
}
