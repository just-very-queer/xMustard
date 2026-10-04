//! WS-RT6: `index update --paths` reads only the named paths and their stored rows, and
//! lists the tree only when it cannot prove the result equals a full build's. Every
//! case compares the updated index with a fresh full build of the same tree: the content
//! digest, the coverage report and the scan meta a full listing writes.

use std::fs;
use std::path::Path;
use std::process::Command;

use rusqlite::{Connection, OpenFlags};
use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// The meta a listing writes; a paths update must leave the same values.
const SCAN_META: &[&str] = &[
    "envelope_beyond",
    "envelope_beyond_sample",
    "worktree_deleted",
    "ignored_files",
    "invalid_paths",
    "nested_repos",
    "last_commit",
];

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
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

fn write(root: &Path, rel: &str, content: &str) {
    let p = root.join(rel);
    fs::create_dir_all(p.parent().unwrap()).unwrap();
    fs::write(p, content).unwrap();
}

fn body(i: usize) -> String {
    format!("export function f{i}(): number {{ return {i}; }}\n")
}

/// m0.ts..m9.ts, a README and a doc under docs/, committed.
fn repo() -> TempDir {
    let dir = TempDir::new().unwrap();
    for i in 0..10 {
        write(dir.path(), &format!("m{i}.ts"), &body(i));
    }
    write(dir.path(), "README.md", "# Title\n\nIntro.\n");
    write(dir.path(), "docs/guide.md", "# Guide\n");
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn index(cmd: &str, root: &Path, extra: &[&str]) -> Value {
    let mut args = vec!["index", cmd, root.to_str().unwrap()];
    args.extend_from_slice(extra);
    let out = Command::new(BIN).args(&args).output().unwrap();
    assert!(
        out.status.success(),
        "{args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

/// `index update` with `flags` and `--paths` naming `paths`.
fn update_paths(root: &Path, flags: &[&str], paths: &[&str]) -> Value {
    let mut args = flags.to_vec();
    args.push("--paths");
    args.extend_from_slice(paths);
    index("update", root, &args)
}

fn scan_meta(report: &Value) -> Vec<(String, Option<String>)> {
    let conn = Connection::open_with_flags(
        report["index_path"].as_str().unwrap(),
        OpenFlags::SQLITE_OPEN_READ_ONLY,
    )
    .unwrap();
    SCAN_META
        .iter()
        .map(|k| {
            let v = conn
                .query_row("SELECT value FROM meta WHERE key = ?1", [k], |r| r.get(0))
                .ok();
            (k.to_string(), v)
        })
        .collect()
}

fn digest(root: &Path, flags: &[&str]) -> String {
    let mut args = flags.to_vec();
    args.push("--digest");
    index("stats", root, &args)["content_digest"]
        .as_str()
        .unwrap()
        .to_string()
}

/// Asserts the index `report` describes equals a fresh full build of the tree.
fn assert_matches_full_build(root: &Path, flags: &[&str], report: &Value, what: &str) {
    let other = TempDir::new().unwrap();
    let d = other.path().join("idx");
    let d = d.to_str().unwrap();
    let mut args = flags.to_vec();
    args.extend(["--index-dir", d, "--no-cache"]);
    let fresh = index("build", root, &args);
    let mut args = flags.to_vec();
    args.extend(["--index-dir", d]);
    assert_eq!(digest(root, flags), digest(root, &args), "{what}: digest");
    assert_eq!(report["coverage"], fresh["coverage"], "{what}: coverage");
    assert_eq!(scan_meta(report), scan_meta(&fresh), "{what}: scan meta");
}

fn counter(report: &Value, name: &str) -> u64 {
    report["pass_counters"][name]
        .as_u64()
        .unwrap_or_else(|| panic!("no pass_counters.{name}: {report:#}"))
}

/// The update read only its batch: no listing, at most three stats per named path (its
/// `lstat`, the read that identifies it, the read that extracts it), at most one stored
/// row per named path, and no Git child besides the two `rev-parse` calls. The tree has
/// 12 files, so a listing would stat more.
fn assert_paths_only(report: &Value, batch: u64, what: &str) {
    assert_eq!(report["listing"]["scope"], "paths", "{what}: {report:#}");
    assert_eq!(counter(report, "files_listed"), 0, "{what}: files_listed");
    assert!(
        counter(report, "files_statted") <= 3 * batch,
        "{what}: files_statted {report:#}"
    );
    assert!(
        counter(report, "rows_loaded") <= batch,
        "{what}: rows_loaded {report:#}"
    );
    assert!(
        counter(report, "git_spawns") <= 2,
        "{what}: git_spawns {report:#}"
    );
}

fn assert_listed(report: &Value, reason: &str, what: &str) {
    assert_eq!(report["listing"]["scope"], "tree", "{what}: {report:#}");
    assert_eq!(report["listing"]["reason"], reason, "{what}: {report:#}");
    assert!(counter(report, "files_listed") > 0, "{what}: files_listed");
}

const CUT: &[&str] = &["--max-files", "3"];

#[test]
fn an_edit_inside_the_envelope_reads_only_the_named_file() {
    for flags in [CUT, &[][..]] {
        let r = repo();
        index("build", r.path(), flags);
        write(r.path(), "m1.ts", &body(41));
        let up = update_paths(r.path(), flags, &["m1.ts"]);
        assert_paths_only(&up, 1, &format!("{flags:?}"));
        assert_eq!(up["mode"], "incremental", "{flags:?}: {up:#}");
        assert_eq!(up["counters"]["reparsed"], 1, "{flags:?}");
        assert_matches_full_build(r.path(), flags, &up, &format!("{flags:?} edit"));
    }
}

#[test]
fn an_edit_past_the_cut_changes_no_row_and_lists_nothing() {
    let r = repo();
    index("build", r.path(), CUT);
    write(r.path(), "m7.ts", &body(77));
    let up = update_paths(r.path(), CUT, &["m7.ts"]);
    assert_paths_only(&up, 1, "past the cut");
    assert_eq!(up["mode"], "noop", "{up:#}");
    assert_matches_full_build(r.path(), CUT, &up, "past the cut");
}

#[test]
fn an_untracked_new_file_needs_no_listing() {
    for flags in [CUT, &[][..]] {
        let r = repo();
        index("build", r.path(), flags);
        write(r.path(), "a_new.ts", &body(5));
        let up = update_paths(r.path(), flags, &["a_new.ts"]);
        assert_paths_only(&up, 1, &format!("{flags:?} untracked"));
        assert_eq!(up["mode"], "noop", "{flags:?}: {up:#}");
        assert_matches_full_build(r.path(), flags, &up, &format!("{flags:?} untracked"));
    }
}

#[test]
fn a_file_past_the_cut_deleted_then_restored_keeps_the_counts_exact() {
    let r = repo();
    index("build", r.path(), CUT);
    fs::remove_file(r.path().join("m7.ts")).unwrap();
    // nothing stored says whether m7.ts was tracked: the tree is listed once
    let up = update_paths(r.path(), CUT, &["m7.ts"]);
    assert_listed(&up, "unknown_prior", "deleted past the cut");
    assert_matches_full_build(r.path(), CUT, &up, "deleted past the cut");
    // that listing recorded it as deleted, so restoring it is decided from the batch
    write(r.path(), "m7.ts", &body(7));
    let up = update_paths(r.path(), CUT, &["m7.ts"]);
    assert_paths_only(&up, 1, "restored past the cut");
    assert_matches_full_build(r.path(), CUT, &up, "restored past the cut");
}

#[test]
fn a_deleted_kept_file_without_a_cut_is_removed_from_the_batch_alone() {
    let r = repo();
    index("build", r.path(), &[]);
    fs::remove_file(r.path().join("m1.ts")).unwrap();
    let up = update_paths(r.path(), &[], &["m1.ts"]);
    assert_paths_only(&up, 1, "deleted");
    assert_eq!(up["counters"]["deleted"], 1, "{up:#}");
    assert_matches_full_build(r.path(), &[], &up, "deleted");
    write(r.path(), "m1.ts", &body(1));
    let up = update_paths(r.path(), &[], &["m1.ts"]);
    assert_paths_only(&up, 1, "restored");
    assert_matches_full_build(r.path(), &[], &up, "restored");
}

#[test]
fn a_deleted_kept_file_while_the_envelope_is_cut_lists_the_tree() {
    let r = repo();
    index("build", r.path(), CUT);
    fs::remove_file(r.path().join("m0.ts")).unwrap();
    let up = update_paths(r.path(), CUT, &["m0.ts"]);
    assert_listed(&up, "envelope_shift", "kept file deleted");
    assert_matches_full_build(r.path(), CUT, &up, "kept file deleted");
}

#[test]
fn a_doc_edit_reads_only_the_doc() {
    let r = repo();
    index("build", r.path(), &[]);
    write(r.path(), "README.md", "# Title\n\nA longer intro.\n");
    let up = update_paths(r.path(), &[], &["README.md"]);
    assert_paths_only(&up, 1, "doc edit");
    assert_eq!(up["counters"]["docs"]["written"], 1, "{up:#}");
    assert_matches_full_build(r.path(), &[], &up, "doc edit");
}

#[test]
fn a_head_jump_lists_the_tree() {
    let r = repo();
    index("build", r.path(), &[]);
    write(r.path(), "m2.ts", &body(42));
    git(r.path(), &["commit", "-qam", "next"]);
    let up = update_paths(r.path(), &[], &["m2.ts"]);
    assert_listed(&up, "head_moved", "commit");
    assert_matches_full_build(r.path(), &[], &up, "commit");
}

#[test]
fn a_staged_file_lists_the_tree() {
    let r = repo();
    index("build", r.path(), &[]);
    write(r.path(), "n.ts", &body(8));
    git(r.path(), &["add", "n.ts"]);
    let up = update_paths(r.path(), &[], &["m3.ts"]);
    assert_listed(&up, "git_index_changed", "staged");
    assert_matches_full_build(r.path(), &[], &up, "staged");
    // the listing stored the new Git index state: the next batch is decided alone
    write(r.path(), "n.ts", &body(9));
    let up = update_paths(r.path(), &[], &["n.ts"]);
    assert_paths_only(&up, 1, "after the listing");
    assert_matches_full_build(r.path(), &[], &up, "after the listing");
}

#[test]
fn an_ignore_file_in_the_batch_lists_the_tree() {
    let r = repo();
    index("build", r.path(), &[]);
    write(r.path(), ".xmustardignore", "m4.ts\n");
    let up = update_paths(r.path(), &[], &[".xmustardignore"]);
    assert_listed(&up, "ignore_file", "ignore file");
    assert_matches_full_build(r.path(), &[], &up, "ignore file");
}

#[test]
fn untracked_indexing_lists_the_tree() {
    let r = repo();
    let flags = &["--include-untracked"][..];
    index("build", r.path(), flags);
    write(r.path(), "m1.ts", &body(51));
    let up = update_paths(r.path(), flags, &["m1.ts"]);
    assert_listed(&up, "include_untracked", "untracked indexing");
    assert_matches_full_build(r.path(), flags, &up, "untracked indexing");
}

#[test]
fn an_index_without_listing_state_lists_once() {
    let r = repo();
    let built = index("build", r.path(), &[]);
    {
        let conn = Connection::open(built["index_path"].as_str().unwrap()).unwrap();
        let n = conn
            .execute("DELETE FROM meta WHERE key = 'listing_state'", [])
            .unwrap();
        assert_eq!(n, 1, "a build stores the listing state");
    }
    write(r.path(), "m5.ts", &body(55));
    let up = update_paths(r.path(), &[], &["m5.ts"]);
    assert_listed(&up, "no_listing_state", "older index");
    write(r.path(), "m5.ts", &body(56));
    let up = update_paths(r.path(), &[], &["m5.ts"]);
    assert_paths_only(&up, 1, "after the listing");
    assert_matches_full_build(r.path(), &[], &up, "after the listing");
}

#[test]
fn an_update_without_paths_lists_the_tree() {
    let r = repo();
    index("build", r.path(), &[]);
    let up = index("update", r.path(), &[]);
    assert_listed(&up, "no_paths", "no batch");
}

#[test]
fn a_renamed_directory_lists_the_tree() {
    let r = repo();
    write(r.path(), "src/a.ts", &body(20));
    write(r.path(), "src/b.ts", &body(21));
    git(r.path(), &["add", "-A"]);
    git(r.path(), &["commit", "-qm", "src"]);
    index("build", r.path(), &[]);
    // a directory rename reports the two directories, not the files inside them
    fs::rename(r.path().join("src"), r.path().join("lib")).unwrap();
    let up = update_paths(r.path(), &[], &["src", "lib"]);
    assert_listed(&up, "directory", "renamed directory");
    assert_matches_full_build(r.path(), &[], &up, "renamed directory");
    // a new, empty directory holds nothing the batch could miss
    fs::create_dir(r.path().join("empty")).unwrap();
    let up = update_paths(r.path(), &[], &["empty"]);
    assert_paths_only(&up, 1, "new directory");
}

#[test]
#[cfg(unix)]
fn a_tracked_symlink_replaced_by_a_file_lists_the_tree() {
    let r = repo();
    std::os::unix::fs::symlink("m0.ts", r.path().join("link.ts")).unwrap();
    git(r.path(), &["add", "link.ts"]);
    git(r.path(), &["commit", "-qm", "link"]);
    index("build", r.path(), &[]);
    // the Git index still records a symlink, so a full listing keeps the symlink loss
    fs::remove_file(r.path().join("link.ts")).unwrap();
    write(r.path(), "link.ts", &body(30));
    let up = update_paths(r.path(), &[], &["link.ts"]);
    assert_listed(&up, "symlink_mode", "symlink replaced");
    assert_matches_full_build(r.path(), &[], &up, "symlink replaced");
}

#[test]
fn a_directory_recreated_without_its_files_named_lists_the_tree() {
    let r = repo();
    write(r.path(), "src/a.ts", &body(20));
    write(r.path(), "src/b.ts", &body(21));
    git(r.path(), &["add", "-A"]);
    git(r.path(), &["commit", "-qm", "src"]);
    index("build", r.path(), &[]);
    fs::remove_dir_all(r.path().join("src")).unwrap();
    fs::create_dir(r.path().join("src")).unwrap();
    let up = update_paths(r.path(), &[], &["src"]);
    assert_listed(&up, "directory", "recreated directory");
    assert_matches_full_build(r.path(), &[], &up, "recreated directory");
}

#[test]
#[cfg(unix)]
fn a_deleted_tracked_symlink_restored_as_a_file_lists_the_tree() {
    let r = repo();
    std::os::unix::fs::symlink("m0.ts", r.path().join("link.ts")).unwrap();
    git(r.path(), &["add", "link.ts"]);
    git(r.path(), &["commit", "-qm", "link"]);
    index("build", r.path(), &[]);
    fs::remove_file(r.path().join("link.ts")).unwrap();
    let up = update_paths(r.path(), &[], &["link.ts"]);
    assert_paths_only(&up, 1, "symlink deleted");
    assert_matches_full_build(r.path(), &[], &up, "symlink deleted");
    // the deletion kept the symlink mode, so a regular file in its place is not trusted
    write(r.path(), "link.ts", &body(31));
    let up = update_paths(r.path(), &[], &["link.ts"]);
    assert_listed(&up, "symlink_mode", "symlink restored as a file");
    assert_matches_full_build(r.path(), &[], &up, "symlink restored as a file");
}

#[test]
fn a_renamed_directory_with_a_source_extension_lists_the_tree() {
    let r = repo();
    write(r.path(), "src.ts/a.ts", &body(22));
    write(r.path(), "src.ts/b.ts", &body(23));
    git(r.path(), &["add", "-A"]);
    git(r.path(), &["commit", "-qm", "src.ts"]);
    index("build", r.path(), &[]);
    fs::rename(r.path().join("src.ts"), r.path().join("lib.ts")).unwrap();
    let up = update_paths(r.path(), &[], &["src.ts", "lib.ts"]);
    assert_listed(&up, "directory", "renamed src.ts");
    assert_matches_full_build(r.path(), &[], &up, "renamed src.ts");
}
