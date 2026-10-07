//! WS-RT18 at the command line: the source identity, change tracking and the index
//! scan read `.git/index` in-process and start no Git child that loads it, and their
//! answers equal the Git path's (`XMUSTARD_GIT_INDEX_READER=git`) on the C8 matrix.

mod gitfix;

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

use gitfix::*;
use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

fn core_with(env: &[(&str, &str)], args: &[&str]) -> Value {
    let out = Command::new(BIN)
        .envs(env.iter().copied())
        .args(args)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn core_as(source: &str, args: &[&str]) -> Value {
    core_with(&[("XMUSTARD_GIT_INDEX_READER", source)], args)
}

/// `v` without the fields that depend on time or on the stat cache's warmth.
fn stable(mut v: Value) -> Value {
    for k in [
        "files_hashed",
        "bytes_hashed",
        "files_reused",
        "pass_counters",
        "generated_at",
        "indexed_at",
        "baseline_indexed_at",
    ] {
        if let Some(o) = v.as_object_mut() {
            o.remove(k);
        }
    }
    // how much a pass hashed depends on the stat cache the other run warmed
    if let Some(o) = v.get_mut("drift_checked").and_then(Value::as_object_mut) {
        o.remove("hashed");
        o.remove("stat_skipped");
    }
    if let Some(fp) = v.get_mut("fingerprint") {
        *fp = stable(fp.take());
    }
    v
}

fn s(p: &Path) -> &str {
    p.to_str().unwrap()
}

/// The stored baseline under `data`, without its timestamps.
fn baseline_file(data: &Path) -> Value {
    let mut stack = vec![data.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in fs::read_dir(&d).unwrap().filter_map(Result::ok) {
            let p = e.path();
            if p.is_dir() {
                stack.push(p);
            } else if p.file_name().is_some_and(|n| n == "index_baseline.json") {
                return stable(serde_json::from_slice(&fs::read(&p).unwrap()).unwrap());
            }
        }
    }
    panic!("no baseline under {}", data.display())
}

/// Every answer the identity and change tracking give for `root`, under `source`.
fn answers(root: &Path, source: &str) -> Vec<(String, Value)> {
    let r = s(root);
    let mut got = vec![
        (
            "repo-key".to_string(),
            stable(core_as(source, &["repo-key", r])),
        ),
        (
            "fingerprint".into(),
            stable(core_as(source, &["changetrack", "fingerprint", r])),
        ),
    ];
    for (name, reason) in [
        ("registration", "--reason=registration"),
        ("admin", "--reason=admin"),
    ] {
        let data = TempDir::new().unwrap();
        let d = s(data.path());
        let summary = core_as(source, &["changetrack", "index", d, r, "ws", reason]);
        got.push((format!("index {name}"), stable(summary)));
        got.push((format!("baseline {name}"), baseline_file(data.path())));
        got.push((
            format!("working-changes {name}"),
            stable(core_as(
                source,
                &["changetrack", "working-changes", d, r, "ws"],
            )),
        ));
        got.push((
            format!("drift {name}"),
            stable(core_as(source, &["changetrack", "drift", d, r, "ws"])),
        ));
    }
    got
}

#[test]
fn identity_and_change_tracking_equal_the_git_path_on_the_matrix() {
    for (name, build) in MATRIX {
        let fx = build();
        let via_git = answers(&fx.root, "git");
        let via_reader = answers(&fx.root, "reader");
        for ((what, g), (_, r)) in via_git.iter().zip(&via_reader) {
            assert_eq!(r, g, "{name}: {what} differs (reader vs git)");
        }
    }
}

/// A `git` on `PATH` that logs each call (and whether it ran on a private index set
/// through `GIT_INDEX_FILE`) and then runs the real Git.
struct Shim {
    _dir: TempDir,
    path: String,
    log: PathBuf,
}

impl Shim {
    fn new() -> Shim {
        let real = Command::new("sh")
            .args(["-c", "command -v git"])
            .output()
            .unwrap();
        let real = String::from_utf8(real.stdout).unwrap().trim().to_string();
        assert!(!real.is_empty(), "no git on PATH");
        let dir = TempDir::new().unwrap();
        let log = dir.path().join("calls.log");
        let script = format!(
            "#!/bin/sh\nprintf '%s %s\\n' \"${{GIT_INDEX_FILE:+private-index}}\" \"$*\" >> '{}'\nexec '{real}' \"$@\"\n",
            log.display()
        );
        fs::write(dir.path().join("git"), script).unwrap();
        chmod(dir.path(), "git", 0o755);
        let path = format!(
            "{}:{}",
            dir.path().display(),
            std::env::var("PATH").unwrap_or_default()
        );
        Shim {
            _dir: dir,
            path,
            log,
        }
    }

    /// (private index?, subcommand) per logged call.
    fn calls(&self) -> Vec<(bool, String)> {
        let text = fs::read_to_string(&self.log).unwrap_or_default();
        text.lines()
            .map(|line| {
                let private = line.starts_with("private-index ");
                let mut words = line.split_whitespace().skip(private as usize);
                let mut sub = String::new();
                while let Some(w) = words.next() {
                    match w {
                        "-C" | "-c" => {
                            words.next();
                        }
                        w if w.starts_with('-') => {}
                        w => {
                            sub = w.to_string();
                            break;
                        }
                    }
                }
                (private, sub)
            })
            .collect()
    }
}

/// The Git subcommands that never read the repository's index.
const OBJECT_ONLY: &[&str] = &["rev-parse", "config", "cat-file", "remote", "symbolic-ref"];

#[test]
fn the_reader_starts_no_index_loading_git_child() {
    for fx in [dirty(), submodules(), ignores()] {
        let shim = Shim::new();
        let env = [("PATH", shim.path.as_str())];
        let r = s(&fx.root);
        let data = TempDir::new().unwrap();
        let d = s(data.path());
        core_with(&env, &["repo-key", r]);
        core_with(&env, &["changetrack", "fingerprint", r]);
        core_with(
            &env,
            &["changetrack", "index", d, r, "ws", "--reason=registration"],
        );
        core_with(&env, &["changetrack", "working-changes", d, r, "ws"]);
        core_with(&env, &["changetrack", "drift", d, r, "ws"]);
        core_with(
            &env,
            &["changetrack", "index", d, r, "ws", "--reason=admin"],
        );
        core_with(&env, &["index", "build", r]);
        let calls = shim.calls();
        assert!(!calls.is_empty(), "the shim logged nothing");
        let loading: Vec<&(bool, String)> = calls
            .iter()
            .filter(|(private, sub)| {
                !(OBJECT_ONLY.contains(&sub.as_str()) || *private && sub == "update-index")
            })
            .collect();
        assert!(
            loading.is_empty(),
            "{}: Git children that read the index: {loading:?}",
            fx.root.display()
        );
    }
}

fn reasons(id: &Value) -> Vec<String> {
    id["limitations"]
        .as_array()
        .unwrap()
        .iter()
        .map(|l| l["reason"].as_str().unwrap().to_string())
        .collect()
}

/// A sparse index: the reader refuses it (a named limitation, no Git fallback when
/// forced); `auto` falls back to Git on an index this small.
#[test]
fn a_sparse_index_is_a_named_refusal() {
    let fx = clean();
    let r = &fx.root;
    write(r, "other/o.txt", b"o\n");
    commit(r, "outside the cone");
    git(r, &["sparse-checkout", "init", "--cone", "--sparse-index"]);
    git(r, &["sparse-checkout", "set", "src"]);
    let forced = core_as("reader", &["repo-key", s(r)]);
    assert_eq!(forced["identity_complete"], false, "{forced}");
    assert!(
        reasons(&forced).contains(&"git_index_unsupported".to_string()),
        "{forced}"
    );
    let auto = core_as("auto", &["repo-key", s(r)]);
    let via_git = core_as("git", &["repo-key", s(r)]);
    assert_eq!(stable(auto), stable(via_git));
}
