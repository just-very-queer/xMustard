//! Identity without whole-tree buffers (WS-RT3): the source identity streams its Git
//! listings, bounds the untracked entries it identifies, the legacy graph names the
//! true cause of a failed listing, a behind code index answers when that graph cannot,
//! and the registration signal scan reads a bounded number of files.

use std::fs;
use std::io::Write;
use std::path::Path;
use std::process::{Command, Stdio};

use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// Entries whose listings pass the 32 MiB Git output cap the identity used to buffer
/// under (`MAX_GIT_OUTPUT_BYTES`): 36,000 records of about 980 bytes is 35 MB.
const LONG_ENTRIES: usize = 36_000;

fn git_in(root: &Path, args: &[&str], input: &[u8]) -> Vec<u8> {
    let mut child = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let input = input.to_vec();
    let writer = std::thread::spawn(move || stdin.write_all(&input));
    let out = child.wait_with_output().unwrap();
    writer.join().unwrap().unwrap();
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    out.stdout
}

fn git(root: &Path, args: &[&str]) -> Vec<u8> {
    git_in(root, args, b"")
}

fn core(args: &[&str]) -> Value {
    let out = Command::new(BIN).args(args).output().unwrap();
    assert!(
        out.status.success(),
        "{args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

/// A committed repository holding `a.go` (Alpha) and `b.go` (calls Alpha) plus
/// LONG_ENTRIES committed `.bin` paths of about 980 bytes that the worktree does not
/// hold: deleted in the worktree, or marked skip-worktree (a sparse checkout).
fn long_path_repo(skip_worktree: bool) -> TempDir {
    let dir = TempDir::new().unwrap();
    let root = dir.path();
    git(root, &["init", "-q"]);
    git(root, &["config", "user.email", "t@t"]);
    git(root, &["config", "user.name", "t"]);
    fs::write(
        root.join("a.go"),
        "package p\n\nfunc Alpha() int { return 1 }\n",
    )
    .unwrap();
    fs::write(
        root.join("b.go"),
        "package p\n\nfunc Beta() int { return Alpha() }\n",
    )
    .unwrap();
    git(root, &["add", "a.go", "b.go"]);
    let blob = String::from_utf8(git_in(root, &["hash-object", "-w", "--stdin"], b"x\n")).unwrap();
    // 220 characters per component: long enough that the listing of LONG_ENTRIES
    // paths is far past the old output cap, and short enough that the whole path
    // fits macOS's PATH_MAX of 1024 (Linux allows 4096, and a path built to the
    // Linux limit is refused with ENAMETOOLONG on a Mac, which is the platform's
    // limit and not a reader's defect).
    let dirs: Vec<String> = ["a", "b", "c", "d"].iter().map(|c| c.repeat(220)).collect();
    let paths: Vec<String> = (0..LONG_ENTRIES)
        .map(|i| format!("{}/f{i:06}.bin", dirs.join("/")))
        .collect();
    let info: String = paths
        .iter()
        .map(|p| format!("100644 {} 0\t{p}\n", blob.trim()))
        .collect();
    git_in(
        root,
        &["update-index", "--add", "--index-info"],
        info.as_bytes(),
    );
    git(root, &["commit", "-qm", "long paths"]);
    if skip_worktree {
        let list: String = paths.iter().map(|p| format!("{p}\n")).collect();
        git_in(
            root,
            &["update-index", "--skip-worktree", "--stdin"],
            list.as_bytes(),
        );
    }
    dir
}

fn reasons(id: &Value) -> Vec<String> {
    id["limitations"]
        .as_array()
        .unwrap()
        .iter()
        .map(|l| l["reason"].as_str().unwrap().to_string())
        .collect()
}

#[test]
fn a_status_listing_past_the_old_output_cap_still_identifies_the_tree() {
    let r = long_path_repo(false);
    let id = core(&["repo-key", r.path().to_str().unwrap()]);
    assert!(
        !reasons(&id).contains(&"git_status_failed".to_string()),
        "{}",
        id["limitations"]
    );
    assert_eq!(id["dirty_entries"], LONG_ENTRIES, "{}", id["limitations"]);
    assert_eq!(id["identity_complete"], true, "{}", id["limitations"]);
}

#[test]
fn a_hidden_entry_listing_past_the_old_output_cap_keeps_the_hidden_entries() {
    let r = long_path_repo(true);
    let id = core(&["repo-key", r.path().to_str().unwrap()]);
    assert!(
        !reasons(&id).contains(&"git_index_flags_failed".to_string()),
        "{}",
        id["limitations"]
    );
    assert_eq!(id["hidden_entries"], LONG_ENTRIES);
    assert_eq!(id["dirty_entries"], 0);
}

/// Untracked entries past the identity's count bound (10,000).
#[test]
fn untracked_entries_past_the_bound_are_counted_and_keyed_by_path_only() {
    let dir = TempDir::new().unwrap();
    let root = dir.path();
    git(root, &["init", "-q"]);
    fs::create_dir(root.join("u")).unwrap();
    for i in 0..10_050 {
        fs::write(root.join(format!("u/f{i:05}.txt")), "x").unwrap();
    }
    let rs = root.to_str().unwrap();
    let id = core(&["repo-key", rs]);
    assert_eq!(id["untracked_entries"], 10_050);
    let budget: Vec<&Value> = id["limitations"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|l| l["reason"] == "identity_untracked_budget")
        .collect();
    assert_eq!(budget.len(), 1, "{}", id["limitations"]);
    assert!(budget[0]["path"].is_null(), "{}", budget[0]);
    assert!(
        budget[0]["detail"]
            .as_str()
            .unwrap()
            .contains("50 untracked"),
        "{}",
        budget[0]
    );
    assert_eq!(id["identity_complete"], false);
    assert!(id["files_hashed"].as_u64().unwrap() <= 10_000, "{id}");

    // change tracking counts the entries the identity keeps no entry for
    let data = TempDir::new().unwrap();
    let ds = data.path().to_str().unwrap();
    let w = core(&["changetrack", "working-changes", ds, rs, "ws"]);
    assert_eq!(w["changed_files_total"], 10_050, "{}", w["truncation"]);
    assert!(
        w["truncation"]["reason"]
            .as_str()
            .unwrap()
            .contains("50 untracked entries past"),
        "{}",
        w["truncation"]
    );
    assert!(w["dirty_symbols_total"].is_null(), "{}", w["unknown"]);
    core(&["changetrack", "index", ds, rs, "ws"]);
    let d = core(&["changetrack", "drift", ds, rs, "ws"]);
    assert!(
        d["reasons"]
            .as_array()
            .unwrap()
            .contains(&Value::from("10050 uncommitted dirty path(s)")),
        "{}",
        d["reasons"]
    );

    // past the bound only the path enters the key: a new entry moves it, an edit not
    fs::write(root.join("u/f10049.txt"), "y").unwrap();
    assert_eq!(core(&["repo-key", rs])["key"], id["key"]);
    fs::write(root.join("u/f10050.txt"), "x").unwrap();
    assert_ne!(core(&["repo-key", rs])["key"], id["key"]);
}

#[test]
fn the_legacy_graph_names_a_listing_past_the_cap_instead_of_git_unavailable() {
    let r = long_path_repo(true);
    let g = core(&["symbolgraph", "build", r.path().to_str().unwrap(), "ws"]);
    let cov = &g["coverage"];
    assert_eq!(cov["repo_mode"], "git", "{cov}");
    assert_eq!(cov["loss_counts"]["git_listing_cap"], 1, "{cov}");
    assert!(
        cov["degraded_reason"]
            .as_str()
            .unwrap()
            .contains("output cap"),
        "{cov}"
    );
}

#[test]
fn a_behind_index_answers_when_the_legacy_listing_fails() {
    let r = long_path_repo(true);
    let rs = r.path().to_str().unwrap();
    core(&["index", "build", rs]);
    // the caller observed an identity the index was not brought to
    let s = core(&[
        "search",
        "--identity-key=elsewhere",
        rs,
        "ws",
        "Alpha",
        "10",
    ]);
    assert!(s["total"].as_u64().unwrap() >= 1, "{s}");
    assert_eq!(s["freshness"]["status"], "unknown", "{}", s["freshness"]);
    assert_eq!(s["coverage"]["complete"], false);
    let why = s["coverage"]["degraded_reason"].as_str().unwrap();
    assert!(
        why.contains("behind") && why.contains("git_listing_cap"),
        "{why}"
    );

    let i = core(&[
        "symbolgraph",
        "impact-file",
        "--identity-key=elsewhere",
        rs,
        "ws",
        "a.go",
        "4",
    ]);
    assert_eq!(i["found"], true, "{i}");
    assert_eq!(i["freshness"]["status"], "unknown", "{}", i["freshness"]);
    assert_eq!(i["coverage"]["complete"], false, "{}", i["coverage"]);
}

/// More scannable files than the signal scan reads (10,000), with a marker in the
/// first and the last by name.
#[test]
fn the_signal_scan_reads_a_bounded_number_of_files_in_name_order() {
    let dir = TempDir::new().unwrap();
    let src = dir.path().join("src");
    fs::create_dir(&src).unwrap();
    for i in 0..10_010 {
        let body = match i {
            0 | 10_009 => "raise NotImplementedError()\n",
            _ => "x = 1\n",
        };
        fs::write(src.join(format!("f{i:05}.py")), body).unwrap();
    }
    let root = dir.path().to_str().unwrap();
    let v = core(&["scan-signals", root]);
    assert_eq!(v["files_read"], 10_000, "{}", v["reason"]);
    assert_eq!(v["truncated"], true);
    assert!(
        v["reason"].as_str().unwrap().contains("10000"),
        "{}",
        v["reason"]
    );
    let paths: Vec<&str> = v["signals"]
        .as_array()
        .unwrap()
        .iter()
        .map(|s| s["file_path"].as_str().unwrap())
        .collect();
    assert_eq!(paths, ["src/f00000.py"]);
    assert_eq!(core(&["scan-signals", root])["signals"], v["signals"]);
}
