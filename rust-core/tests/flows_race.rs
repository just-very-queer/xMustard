//! Generation isolation for the lazy exported-symbol read used by `index flows`.

use std::fs;
use std::path::Path;
use std::process::Command;

use tempfile::TempDir;
use xmustard_core::index::{self, config::IndexConfig, reader};

fn git(root: &Path, args: &[&str]) {
    let output = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
}

fn fixture() -> TempDir {
    let root = TempDir::new().unwrap();
    fs::write(root.path().join("go.mod"), "module example\n\ngo 1.21\n").unwrap();
    fs::create_dir_all(root.path().join("pkg")).unwrap();
    fs::write(
        root.path().join("pkg/main.go"),
        "package pkg\n\nfunc Run() { Leaf() }\nfunc Leaf() {}\n",
    )
    .unwrap();
    git(root.path(), &["init", "-q"]);
    git(root.path(), &["config", "user.email", "t@t"]);
    git(root.path(), &["config", "user.name", "t"]);
    git(root.path(), &["add", "-A"]);
    git(root.path(), &["commit", "-qm", "fixture"]);
    root
}

#[test]
fn real_build_between_segment_load_and_lazy_exported_read_is_refused() {
    let root = fixture();
    index::build(root.path(), &IndexConfig::default()).unwrap();
    let opened = reader::open(root.path()).unwrap();
    let old_generation = opened.snapshot.generation;

    fs::write(
        root.path().join("pkg/main.go"),
        "package pkg\n\nfunc Run() { Leaf(); Extra() }\nfunc Leaf() {}\nfunc Extra() {}\n",
    )
    .unwrap();
    git(root.path(), &["add", "-A"]);
    git(root.path(), &["commit", "-qm", "next generation"]);
    index::build(root.path(), &IndexConfig::default()).unwrap();

    let refreshed = reader::open(root.path()).unwrap();
    assert!(refreshed.snapshot.generation > old_generation);
    let err = opened.snapshot.exported_by_symbol().unwrap_err();
    assert!(
        err.contains("generation"),
        "expected generation refusal: {err}"
    );
    assert!(
        opened.snapshot.generation < refreshed.snapshot.generation,
        "the retained segment must be the pre-build generation"
    );
    assert!(
        opened
            .snapshot
            .with_text(|conn| Ok(conn.is_autocommit()))
            .unwrap(),
        "refusal must roll back the lazy read transaction"
    );
}
