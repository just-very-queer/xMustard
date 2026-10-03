//! Nested repositories (WS-RT2): a directory with its own Git dir inside the workspace
//! is classified and reported as `nested_repos`, never read as a file and never a
//! reason for an incomplete identity. The fixtures follow the WS-RT1 nested set
//! (`scripts/bench/rt_fixtures.sh`): n1..n5, g1 and s1..s3.

use std::fs;
use std::path::Path;
use std::process::Command;

use serde_json::{Value, json};
use tempfile::TempDir;
use xmustard_core::changetrack::compute_fingerprint;
use xmustard_core::repomap::build_repo_map;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-c")
        .arg("protocol.file.allow=always")
        .arg("-c")
        .arg("advice.addEmbeddedRepo=false")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
}

fn write(root: &Path, rel: &str, body: &str) {
    let p = root.join(rel);
    fs::create_dir_all(p.parent().unwrap()).unwrap();
    fs::write(p, body).unwrap();
}

fn init(root: &Path) {
    git(root, &["init", "-q"]);
    git(root, &["config", "user.email", "t@t"]);
    git(root, &["config", "user.name", "t"]);
}

fn commit_all(root: &Path, msg: &str) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", msg]);
}

/// The outer workspace: three committed Go files.
fn outer() -> TempDir {
    let dir = TempDir::new().unwrap();
    write(
        dir.path(),
        "a.go",
        "package p\n\nfunc Alpha() int { return 1 }\n",
    );
    write(
        dir.path(),
        "b.go",
        "package p\n\nfunc Beta() int { return Alpha() }\n",
    );
    write(
        dir.path(),
        "c.go",
        "package p\n\nfunc Gamma() int { return Beta() }\n",
    );
    init(dir.path());
    commit_all(dir.path(), "outer");
    dir
}

/// A repository of its own at `dir` with two committed Go files.
fn sub_repo(dir: &Path) {
    write(
        dir,
        "x.go",
        "package sub\n\nfunc SubOnly() int { return 1 }\n",
    );
    write(
        dir,
        "y.go",
        "package sub\n\nfunc SubToo() int { return 2 }\n",
    );
    init(dir);
    commit_all(dir, "sub");
}

fn run_json(args: &[&str]) -> Value {
    let out = Command::new(BIN).args(args).output().unwrap();
    assert!(
        out.status.success(),
        "xmustard-core {args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

/// `repo-key` for the repository at `root`.
fn repo_key(root: &Path) -> Value {
    run_json(&["repo-key", root.to_str().unwrap()])
}

/// `id` is complete, has no limitation and lists exactly `want` (path, kind) as its
/// nested repositories.
fn assert_complete_with(id: &Value, want: &[(&str, &str)]) {
    assert_eq!(id["identity_complete"], true, "{id}");
    assert_eq!(id["limitations"], json!([]), "{id}");
    let rows: Vec<Value> = want
        .iter()
        .map(|(path, kind)| json!({"path": path, "kind": kind}))
        .collect();
    let got = id.get("nested_repos").cloned().unwrap_or(json!([]));
    assert_eq!(got, Value::Array(rows), "{id}");
}

// n1: an unregistered repository at `sub/` (Git reports `?? sub/`).
#[test]
fn an_unregistered_nested_repository_is_reported_and_leaves_the_identity_complete() {
    let r = outer();
    sub_repo(&r.path().join("sub"));
    let first = repo_key(r.path());
    assert_complete_with(&first, &[("sub", "nested_repo")]);
    assert_eq!(first["files_hashed"], 0, "nothing inside sub/ is read");
    assert_eq!(repo_key(r.path())["key"], first["key"], "the key is stable");

    // an edit inside the nested worktree belongs to that repository
    write(
        &r.path().join("sub"),
        "x.go",
        "package sub\n\nfunc SubEdited() int { return 3 }\n",
    );
    assert_eq!(repo_key(r.path())["key"], first["key"]);

    // a commit inside it moves its HEAD, which is part of the outer key
    commit_all(&r.path().join("sub"), "inner change");
    let moved = repo_key(r.path());
    assert_complete_with(&moved, &[("sub", "nested_repo")]);
    assert_ne!(moved["key"], first["key"]);
}

// n2: `a/b/sub` with a repository inside it; Git reports only the outer one.
#[test]
fn a_nested_repository_two_levels_deep_is_reported_once() {
    let r = outer();
    write(
        r.path(),
        "a/keep.go",
        "package a\n\nfunc Keep() int { return 1 }\n",
    );
    commit_all(r.path(), "a");
    sub_repo(&r.path().join("a/b/sub"));
    sub_repo(&r.path().join("a/b/sub/inner"));
    assert_complete_with(&repo_key(r.path()), &[("a/b/sub", "nested_repo")]);
}

// n3: a repository inside a folder Git ignores never shows up.
#[test]
fn a_nested_repository_inside_an_ignored_folder_is_not_reported() {
    let r = outer();
    write(r.path(), ".gitignore", "vendor/\n");
    commit_all(r.path(), "ignore vendor");
    sub_repo(&r.path().join("vendor/sub"));
    assert_complete_with(&repo_key(r.path()), &[]);
}

// n4: Git does not treat a bare repository as nested: its files are untracked content
// and are identified like any other untracked file.
#[test]
fn a_bare_repository_is_untracked_content_not_a_nested_repository() {
    let r = outer();
    git(r.path(), &["init", "-q", "--bare", "bare.git"]);
    let id = repo_key(r.path());
    assert_complete_with(&id, &[]);
    assert!(id["untracked_entries"].as_u64().unwrap() > 0, "{id}");
    assert_eq!(id["files_hashed"], id["untracked_entries"], "{id}");
}

// n5: `sub2/.git` is a `gitdir:` file pointing outside the tree; `broken/.git` points
// nowhere, so Git lists `broken/` file by file.
#[test]
fn a_gitdir_file_is_a_nested_repository_and_a_broken_pointer_is_not() {
    let r = outer();
    let gitdirs = TempDir::new().unwrap();
    let sep = gitdirs.path().join("sub2.git");
    fs::create_dir_all(r.path().join("sub2")).unwrap();
    git(
        &r.path().join("sub2"),
        &["init", "-q", "--separate-git-dir", sep.to_str().unwrap()],
    );
    git(&r.path().join("sub2"), &["config", "user.email", "t@t"]);
    git(&r.path().join("sub2"), &["config", "user.name", "t"]);
    write(
        &r.path().join("sub2"),
        "z.go",
        "package s2\n\nfunc SubOnly() int { return 2 }\n",
    );
    commit_all(&r.path().join("sub2"), "s2");
    write(r.path(), "broken/.git", "gitdir: /nonexistent/xm-gitdir\n");
    write(
        r.path(),
        "broken/w.go",
        "package br\n\nfunc Broken() int { return 3 }\n",
    );

    let first = repo_key(r.path());
    assert_complete_with(&first, &[("sub2", "nested_repo")]);
    // sub2/ and broken/w.go are untracked; only w.go is read
    assert_eq!(
        (
            first["untracked_entries"].clone(),
            first["files_hashed"].clone()
        ),
        (json!(2), json!(1)),
        "{first}"
    );

    write(
        &r.path().join("sub2"),
        "z.go",
        "package s2\n\nfunc SubMoved() int { return 4 }\n",
    );
    commit_all(&r.path().join("sub2"), "s2 again");
    assert_ne!(repo_key(r.path())["key"], first["key"]);
}

// g1: an embedded repository added as a gitlink with no `.gitmodules` entry.
#[test]
fn a_gitlink_without_a_module_entry_is_an_unregistered_gitlink() {
    let r = outer();
    sub_repo(&r.path().join("sub"));
    git(r.path(), &["add", "sub"]);
    assert_complete_with(&repo_key(r.path()), &[("sub", "gitlink_unregistered")]);
    git(r.path(), &["commit", "-qm", "gitlink"]);
    let clean = repo_key(r.path());
    assert_complete_with(&clean, &[]);

    write(
        &r.path().join("sub"),
        "x.go",
        "package sub\n\nfunc Moved() int { return 5 }\n",
    );
    commit_all(&r.path().join("sub"), "inner");
    let moved = repo_key(r.path());
    assert_complete_with(&moved, &[("sub", "gitlink_unregistered")]);
    assert_ne!(moved["key"], clean["key"]);
}

/// An outer repository with `src` added as a submodule at `mod`.
fn with_submodule(src: &Path) -> TempDir {
    let r = outer();
    git(
        r.path(),
        &["submodule", "add", "-q", src.to_str().unwrap(), "mod"],
    );
    git(r.path(), &["commit", "-qm", "addsub"]);
    r
}

// s1: a registered submodule with a modified and a new file.
#[test]
fn a_dirty_submodule_is_reported_and_leaves_the_identity_complete() {
    let src = TempDir::new().unwrap();
    sub_repo(src.path());
    let r = with_submodule(src.path());
    let clean = repo_key(r.path());
    assert_complete_with(&clean, &[]);
    write(
        &r.path().join("mod"),
        "x.go",
        "package sub\n\nfunc SubOnly() int { return 8 }\n",
    );
    write(&r.path().join("mod"), "new.txt", "junk\n");
    let dirty = repo_key(r.path());
    assert_complete_with(&dirty, &[("mod", "submodule")]);
    assert_ne!(
        dirty["key"], clean["key"],
        "the dirty state is part of the key"
    );
}

// s2: a fresh clone whose submodule was never initialised is clean.
#[test]
fn an_uninitialised_submodule_is_clean_and_complete() {
    let src = TempDir::new().unwrap();
    sub_repo(src.path());
    let r = with_submodule(src.path());
    let clone = TempDir::new().unwrap();
    let dest = clone.path().join("c");
    git(
        clone.path(),
        &[
            "clone",
            "-q",
            r.path().to_str().unwrap(),
            dest.to_str().unwrap(),
        ],
    );
    assert_complete_with(&repo_key(&dest), &[]);
}

// s3: `ignore = all` in `.gitmodules` hides the dirty submodule from plain `git status`;
// the identity and change tracking both still see it.
#[test]
fn a_dirty_ignore_all_submodule_is_seen_by_identity_and_change_tracking_alike() {
    let src = TempDir::new().unwrap();
    sub_repo(src.path());
    let r = with_submodule(src.path());
    git(
        r.path(),
        &["config", "-f", ".gitmodules", "submodule.mod.ignore", "all"],
    );
    git(r.path(), &["add", ".gitmodules"]);
    git(r.path(), &["commit", "-qm", "ignore all"]);
    write(
        &r.path().join("mod"),
        "x.go",
        "package sub\n\nfunc SubOnly() int { return 9 }\n",
    );
    assert_complete_with(&repo_key(r.path()), &[("mod", "submodule")]);
    let fp = compute_fingerprint(r.path()).unwrap();
    assert_eq!(fp.dirty_path_count, 1, "{fp:?}");
}

#[test]
fn index_coverage_lists_nested_repositories_without_a_loss() {
    let r = outer();
    sub_repo(&r.path().join("sub"));
    git(r.path(), &["add", "sub"]);
    git(r.path(), &["commit", "-qm", "gitlink"]);
    sub_repo(&r.path().join("loose"));
    let root = r.path().to_str().unwrap();

    let tracked = run_json(&["index", "build", root]);
    let cov = &tracked["coverage"];
    assert_eq!(
        cov["nested_repos"],
        json!({"total": 1, "repos": [{"path": "sub", "kind": "gitlink_unregistered"}]}),
        "{cov}"
    );
    assert_eq!(cov["complete"], true, "{cov}");
    assert_eq!(cov["loss_counts"], json!({}), "{cov}");

    let all = run_json(&["index", "build", root, "--include-untracked"]);
    assert_eq!(
        all["coverage"]["nested_repos"],
        json!({"total": 2, "repos": [
            {"path": "loose", "kind": "nested_repo"},
            {"path": "sub", "kind": "gitlink_unregistered"},
        ]}),
        "{}",
        all["coverage"]
    );
    assert_eq!(all["coverage"]["complete"], true);
}

// The API reads the identity the index was brought to: complete only when the
// orchestrator stamped it with no limitation.
#[test]
fn readers_report_the_stamped_identity_completeness() {
    let r = outer();
    let root = r.path().to_str().unwrap();
    let identity = |key: &str| {
        let v = run_json(&[
            "search",
            &format!("--identity-key={key}"),
            root,
            "ws",
            "Alpha",
            "10",
        ]);
        assert_eq!(v["freshness"]["source"], "index", "{v}");
        v["coverage"]["source_identity"].clone()
    };
    run_json(&[
        "index",
        "update",
        root,
        "--identity-key",
        "k1",
        "--identity-limitations",
        "1",
    ]);
    let s = identity("k1");
    assert_eq!(
        (s["identity_complete"].clone(), s["limitations"].clone()),
        (json!(false), json!(1)),
        "{s}"
    );

    run_json(&[
        "index",
        "update",
        root,
        "--identity-key",
        "k1",
        "--identity-limitations",
        "0",
    ]);
    let s = identity("k1");
    assert_eq!(
        (s["identity_complete"].clone(), s["limitations"].clone()),
        (json!(true), json!(0)),
        "{s}"
    );

    // a key stamped without a count is not known to be complete
    run_json(&["index", "update", root, "--identity-key", "k2"]);
    assert_eq!(identity("k2")["identity_complete"], false);
}

#[test]
fn the_repo_map_counts_the_files_git_lists() {
    let r = outer();
    write(r.path(), ".gitignore", "gen/\n");
    write(r.path(), "gen/out.go", "package gen\n");
    commit_all(r.path(), "ignore gen");
    sub_repo(&r.path().join("sub"));
    write(
        r.path(),
        "new.go",
        "package p\n\nfunc New() int { return 0 }\n",
    );
    let map = build_repo_map(r.path(), "ws").unwrap();
    // a.go, b.go, c.go and the untracked new.go; not sub/x.go, sub/y.go or gen/out.go
    assert_eq!(map.source_files, 4, "{map:?}");
}
