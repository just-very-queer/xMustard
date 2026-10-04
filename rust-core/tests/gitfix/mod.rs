//! Git repositories for the WS-RT18 golden tests (the critique C8 matrix) and the Git
//! commands that are their oracle. Every builder runs Git itself; nothing here reads
//! the index.

#![allow(dead_code)]

use std::collections::BTreeSet;
use std::fs;
use std::io::Write;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::time::{Duration, SystemTime};

use tempfile::TempDir;

/// A built repository: its worktree root, and the directories it needs kept alive.
pub struct Fx {
    pub root: PathBuf,
    _keep: Vec<TempDir>,
}

pub fn run_git(root: &Path, args: &[&str], input: &[u8]) -> Output {
    let mut child = Command::new("git")
        .arg("-C")
        .arg(root)
        .args([
            "-c",
            "protocol.file.allow=always",
            "-c",
            "core.fsmonitor=false",
        ])
        .args(args)
        .env("GIT_AUTHOR_NAME", "t")
        .env("GIT_AUTHOR_EMAIL", "t@t")
        .env("GIT_COMMITTER_NAME", "t")
        .env("GIT_COMMITTER_EMAIL", "t@t")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let input = input.to_vec();
    let writer = std::thread::spawn(move || stdin.write_all(&input));
    let out = child.wait_with_output().unwrap();
    let _ = writer.join();
    out
}

/// Run Git and require success; its stdout.
pub fn git(root: &Path, args: &[&str]) -> Vec<u8> {
    let out = run_git(root, args, b"");
    assert!(
        out.status.success(),
        "git {args:?} in {}: {}",
        root.display(),
        String::from_utf8_lossy(&out.stderr)
    );
    out.stdout
}

pub fn init(root: &Path, extra: &[&str]) {
    fs::create_dir_all(root).unwrap();
    let args = [&["-c", "init.defaultBranch=main", "init", "-q"][..], extra].concat();
    git(root, &args);
}

pub fn write(root: &Path, rel: &str, bytes: &[u8]) {
    let p = root.join(rel);
    fs::create_dir_all(p.parent().unwrap()).unwrap();
    fs::write(p, bytes).unwrap();
}

pub fn commit(root: &Path, msg: &str) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", msg, "--allow-empty"]);
}

pub fn chmod(root: &Path, rel: &str, mode: u32) {
    fs::set_permissions(root.join(rel), fs::Permissions::from_mode(mode)).unwrap();
}

pub fn symlink(root: &Path, target: &str, rel: &str) {
    std::os::unix::fs::symlink(target, root.join(rel)).unwrap();
}

/// Move `rel`'s mtime by `secs` (a stat change with the same bytes).
pub fn touch(root: &Path, rel: &str, secs: i64) {
    let f = fs::File::options()
        .write(true)
        .open(root.join(rel))
        .unwrap();
    let now = SystemTime::now();
    let t = match secs >= 0 {
        true => now + Duration::from_secs(secs as u64),
        false => now - Duration::from_secs((-secs) as u64),
    };
    f.set_modified(t).unwrap();
}

fn repo() -> (TempDir, PathBuf) {
    let dir = TempDir::new().unwrap();
    let root = dir.path().join("r");
    init(&root, &[]);
    (dir, root)
}

fn fx(dir: TempDir, root: PathBuf) -> Fx {
    Fx {
        root,
        _keep: vec![dir],
    }
}

pub fn clean() -> Fx {
    let (d, r) = repo();
    write(&r, "a.txt", b"a\n");
    write(&r, "src/lib.rs", b"fn a() {}\n");
    write(&r, "src/deep/x.go", b"package x\n");
    commit(&r, "init");
    fx(d, r)
}

pub fn dirty() -> Fx {
    let (d, r) = repo();
    for f in ["a", "b", "c", "d", "e", "f", "sub/g"] {
        write(&r, &format!("{f}.txt"), format!("{f} body\n").as_bytes());
    }
    commit(&r, "init");
    write(&r, "a.txt", b"a body, longer now\n");
    write(&r, "b.txt", b"B BODY\n");
    fs::remove_file(r.join("c.txt")).unwrap();
    write(&r, "new.txt", b"new\n");
    git(&r, &["add", "new.txt"]);
    write(&r, "d.txt", b"d staged\n");
    git(&r, &["add", "d.txt"]);
    write(&r, "d.txt", b"d staged then edited\n");
    git(&r, &["rm", "-q", "--cached", "e.txt"]);
    git(&r, &["rm", "-q", "f.txt"]);
    write(&r, "u.txt", b"untracked\n");
    write(&r, "ud/x.txt", b"x\n");
    write(&r, "ud/y/z.txt", b"z\n");
    fs::create_dir_all(r.join("empty_dir/inner")).unwrap();
    fx(d, r)
}

pub fn filter() -> Fx {
    let (d, r) = repo();
    git(&r, &["config", "filter.up.clean", "tr a-z A-Z"]);
    git(&r, &["config", "filter.up.smudge", "tr A-Z a-z"]);
    write(&r, ".gitattributes", b"*.up filter=up\n");
    write(&r, "x.up", b"hello\n");
    write(&r, "y.up", b"world\n");
    write(&r, "z.up", b"third\n");
    commit(&r, "init");
    touch(&r, "x.up", 10);
    // different bytes, the same cleaned content: Git calls it clean
    write(&r, "y.up", b"World\n");
    write(&r, "z.up", b"changed text\n");
    fx(d, r)
}

pub fn autocrlf() -> Fx {
    let (d, r) = repo();
    write(&r, "crlf_in_index.txt", b"x\r\ny\r\n");
    commit(&r, "crlf blob");
    git(&r, &["config", "core.autocrlf", "true"]);
    write(&r, "lf.txt", b"a\nb\n");
    write(&r, "same.txt", b"s\nt\n");
    commit(&r, "lf blob");
    touch(&r, "crlf_in_index.txt", 10);
    write(&r, "lf.txt", b"a\r\nb\r\n");
    touch(&r, "same.txt", 10);
    fx(d, r)
}

pub fn lfs() -> Fx {
    let (d, r) = repo();
    write(
        &r,
        ".gitattributes",
        b"*.bin filter=lfs diff=lfs merge=lfs -text\n",
    );
    let pointer = |n: u8| {
        format!(
            "version https://git-lfs.github.com/spec/v1\noid sha256:{}\nsize 12\n",
            format!("{n:02x}").repeat(32)
        )
    };
    write(&r, "a.bin", pointer(1).as_bytes());
    write(&r, "b.bin", pointer(2).as_bytes());
    commit(&r, "pointers");
    touch(&r, "a.bin", 10);
    write(&r, "b.bin", pointer(3).as_bytes());
    fx(d, r)
}

pub fn exec_bit() -> Fx {
    let (d, r) = repo();
    write(&r, "run.sh", b"#!/bin/sh\n");
    chmod(&r, "run.sh", 0o755);
    write(&r, "plain.txt", b"p\n");
    write(&r, "other.txt", b"o\n");
    commit(&r, "init");
    chmod(&r, "plain.txt", 0o755);
    chmod(&r, "run.sh", 0o644);
    fx(d, r)
}

pub fn exec_bit_ignored() -> Fx {
    let (d, r) = repo();
    git(&r, &["config", "core.filemode", "false"]);
    write(&r, "plain.txt", b"p\n");
    commit(&r, "init");
    chmod(&r, "plain.txt", 0o755);
    fx(d, r)
}

pub fn symlinks() -> Fx {
    let (d, r) = repo();
    write(&r, "target.txt", b"t\n");
    write(&r, "other.txt", b"o\n");
    symlink(&r, "target.txt", "link");
    write(&r, "d/x.txt", b"x\n");
    symlink(&r, "d", "dirlink");
    write(&r, "f.txt", b"f\n");
    write(&r, "dd/y.txt", b"y\n");
    commit(&r, "init");
    fs::remove_file(r.join("link")).unwrap();
    symlink(&r, "other.txt", "link");
    fs::remove_file(r.join("f.txt")).unwrap();
    symlink(&r, "target.txt", "f.txt");
    fs::remove_file(r.join("dirlink")).unwrap();
    write(&r, "dirlink", b"now a file\n");
    fs::remove_dir_all(r.join("dd")).unwrap();
    symlink(&r, "d", "dd");
    fx(d, r)
}

/// A repository with four submodules: one with a new commit, one with modified
/// content, one with only an untracked file, one clean.
pub fn submodules() -> Fx {
    let dir = TempDir::new().unwrap();
    let src = dir.path().join("src");
    init(&src, &[]);
    write(&src, "lib.txt", b"lib\n");
    commit(&src, "lib");
    let r = dir.path().join("r");
    init(&r, &[]);
    write(&r, "a.txt", b"a\n");
    commit(&r, "init");
    let src_s = src.to_str().unwrap();
    for m in ["m_commit", "m_dirty", "m_untracked", "m_clean"] {
        git(&r, &["submodule", "-q", "add", src_s, m]);
    }
    commit(&r, "submodules");
    write(&r, "m_commit/next.txt", b"n\n");
    commit(&r.join("m_commit"), "next");
    write(&r, "m_dirty/lib.txt", b"lib edited\n");
    write(&r, "m_untracked/extra.txt", b"e\n");
    fx(dir, r)
}

/// A clone of `submodules()` that never initialised its submodules.
pub fn submodules_uninit() -> Fx {
    let base = submodules();
    let dir = TempDir::new().unwrap();
    let clone = dir.path().join("clone");
    git(
        dir.path(),
        &["clone", "-q", base.root.to_str().unwrap(), "clone"],
    );
    Fx {
        root: clone,
        _keep: vec![dir],
    }
}

pub fn sha256() -> Fx {
    let dir = TempDir::new().unwrap();
    let r = dir.path().join("r");
    init(&r, &["--object-format=sha256"]);
    write(&r, "a.txt", b"a\n");
    write(&r, "b/c.txt", b"c\n");
    write(&r, "keep.txt", b"k\n");
    commit(&r, "init");
    write(&r, "a.txt", b"a changed\n");
    write(&r, "n.txt", b"n\n");
    git(&r, &["add", "n.txt"]);
    write(&r, "u.txt", b"u\n");
    touch(&r, "keep.txt", 10);
    fx(dir, r)
}

/// A split index whose own file holds a replacement, a deletion and an addition.
pub fn split_index() -> Fx {
    let (d, r) = repo();
    for f in ["a", "b", "c", "d", "e", "f"] {
        write(&r, &format!("{f}.txt"), format!("{f}\n").as_bytes());
    }
    commit(&r, "init");
    git(&r, &["update-index", "--split-index"]);
    write(&r, "a.txt", b"a replaced\n");
    git(&r, &["add", "a.txt"]);
    git(&r, &["rm", "-q", "--cached", "b.txt"]);
    write(&r, "n.txt", b"n\n");
    git(&r, &["add", "n.txt"]);
    write(&r, "c.txt", b"c edited\n");
    let shared = fs::read_dir(r.join(".git"))
        .unwrap()
        .filter_map(Result::ok)
        .any(|e| e.file_name().to_string_lossy().starts_with("sharedindex."));
    assert!(shared, "the fixture has no shared index");
    fx(d, r)
}

pub fn index_v4() -> Fx {
    let (d, r) = repo();
    for f in [
        "src/a/b/c1.rs",
        "src/a/b/c2.rs",
        "src/a/d.rs",
        "src/z.rs",
        "top.rs",
    ] {
        write(&r, f, b"x\n");
    }
    commit(&r, "init");
    git(&r, &["update-index", "--index-version", "4"]);
    write(&r, "src/a/b/c2.rs", b"edited\n");
    write(&r, "src/a/b/new.rs", b"n\n");
    git(&r, &["add", "src/a/b/new.rs"]);
    fx(d, r)
}

/// Version 3 flags: intent-to-add, skip-worktree and assume-unchanged entries.
pub fn flags_v3() -> Fx {
    let (d, r) = repo();
    for f in ["x", "y", "z", "w"] {
        write(&r, &format!("{f}.txt"), format!("{f}\n").as_bytes());
    }
    commit(&r, "init");
    write(&r, "n.txt", b"n\n");
    git(&r, &["add", "-N", "n.txt"]);
    git(&r, &["update-index", "--skip-worktree", "x.txt"]);
    git(&r, &["update-index", "--assume-unchanged", "y.txt"]);
    write(&r, "x.txt", b"x edited\n");
    write(&r, "y.txt", b"y edited\n");
    write(&r, "z.txt", b"z edited\n");
    fs::remove_file(r.join("w.txt")).unwrap();
    fx(d, r)
}

/// Racy entries: an edit in the index's own second with the size kept, and an entry
/// whose mtime is ahead of the index (smudged when the index was written).
pub fn racy() -> Fx {
    let (d, r) = repo();
    write(&r, "r1.txt", b"aaaa");
    write(&r, "r2.txt", b"bbbb");
    write(&r, "r3.txt", b"dddd");
    touch(&r, "r3.txt", 100);
    commit(&r, "init");
    write(&r, "r1.txt", b"cccc");
    fx(d, r)
}

pub fn unmerged() -> Fx {
    let (d, r) = repo();
    write(&r, "a.txt", b"base\n");
    write(&r, "b.txt", b"b\n");
    write(&r, "c.txt", b"c\n");
    commit(&r, "base");
    git(&r, &["checkout", "-qb", "side"]);
    write(&r, "a.txt", b"side\n");
    fs::remove_file(r.join("b.txt")).unwrap();
    write(&r, "d.txt", b"side d\n");
    commit(&r, "side");
    git(&r, &["checkout", "-q", "main"]);
    write(&r, "a.txt", b"main\n");
    write(&r, "b.txt", b"b main\n");
    write(&r, "d.txt", b"main d\n");
    commit(&r, "main");
    let merged = run_git(&r, &["merge", "-q", "side"], b"");
    assert!(!merged.status.success(), "the fixture needs a conflict");
    fx(d, r)
}

pub fn nested() -> Fx {
    let (d, r) = repo();
    write(&r, "a.txt", b"a\n");
    commit(&r, "init");
    let inner = r.join("inner");
    init(&inner, &[]);
    write(&inner, "i.txt", b"i\n");
    commit(&inner, "inner");
    write(&r, "broken/.git", b"gitdir: /nonexistent/xm-gitdir\n");
    write(&r, "broken/w.txt", b"w\n");
    fs::create_dir_all(r.join("notrepo/.git")).unwrap();
    write(&r, "notrepo/f.txt", b"f\n");
    fx(d, r)
}

pub fn ignores() -> Fx {
    let (d, r) = repo();
    let global = d.path().join("global-ignore");
    fs::write(&global, b"*.bak\n").unwrap();
    git(
        &r,
        &["config", "core.excludesFile", global.to_str().unwrap()],
    );
    fs::write(r.join(".git/info/exclude"), b"secret*\n").unwrap();
    write(
        &r,
        ".gitignore",
        b"*.log\n!keep.log\nbuild/\n/anchored.txt\ndocs/**/*.tmp\nlogs/\n",
    );
    write(&r, "sub/.gitignore", b"*.gen\n!special.gen\n");
    write(&r, "logs/keep.txt", b"k\n");
    write(&r, "src/a.rs", b"a\n");
    git(&r, &["add", "-f", "logs/keep.txt"]);
    commit(&r, "init");
    for f in [
        "a.log",
        "keep.log",
        "build/x.o",
        "sub/build/y.o",
        "anchored.txt",
        "sub/anchored.txt",
        "docs/a/b/c.tmp",
        "docs/a/c.md",
        "sub/x.gen",
        "sub/special.gen",
        "secret.txt",
        "z.bak",
        "logs/new.txt",
        "ok.txt",
        "sub/ok.txt",
    ] {
        write(&r, f, b"u\n");
    }
    fx(d, r)
}

/// The C8 fixture matrix, by name.
pub const MATRIX: &[(&str, fn() -> Fx)] = &[
    ("clean", clean),
    ("dirty", dirty),
    ("filter", filter),
    ("autocrlf", autocrlf),
    ("lfs", lfs),
    ("exec_bit", exec_bit),
    ("exec_bit_ignored", exec_bit_ignored),
    ("symlinks", symlinks),
    ("submodules", submodules),
    ("submodules_uninit", submodules_uninit),
    ("sha256", sha256),
    ("split_index", split_index),
    ("index_v4", index_v4),
    ("flags_v3", flags_v3),
    ("racy", racy),
    ("unmerged", unmerged),
    ("nested", nested),
    ("ignores", ignores),
];

/// NUL-separated records.
pub fn records(out: &[u8]) -> Vec<Vec<u8>> {
    out.split(|b| *b == 0)
        .filter(|r| !r.is_empty())
        .map(<[u8]>::to_vec)
        .collect()
}

/// Git's status view: (xy, path) per entry, ignored entries only for whole
/// directories (the identity reads only those).
pub fn git_status(root: &Path) -> BTreeSet<(String, Vec<u8>)> {
    let out = git(
        root,
        &[
            "--no-optional-locks",
            "status",
            "--porcelain=v1",
            "-z",
            "--untracked-files=all",
            "--ignore-submodules=none",
            "--no-renames",
            "--ignored=matching",
        ],
    );
    records(&out)
        .into_iter()
        .map(|rec| {
            (
                String::from_utf8_lossy(&rec[..2]).into_owned(),
                rec[3..].to_vec(),
            )
        })
        .filter(|(xy, p)| xy != "!!" || p.ends_with(b"/"))
        .collect()
}

/// `ls-files -v`'s hidden entries: (tag, path), once per path.
pub fn git_hidden(root: &Path) -> Vec<(u8, Vec<u8>)> {
    let mut hidden: Vec<(u8, Vec<u8>)> = Vec::new();
    for rec in records(&git(root, &["--no-optional-locks", "ls-files", "-v", "-z"])) {
        let (tag, path) = (rec[0], rec[2..].to_vec());
        let repeated = hidden.last().is_some_and(|(_, p)| *p == path);
        if (tag.is_ascii_lowercase() || tag == b'S') && !repeated {
            hidden.push((tag, path));
        }
    }
    hidden
}

/// `ls-files -s -z`: (path, mode) per entry.
pub fn git_tracked(root: &Path) -> Vec<(Vec<u8>, u32)> {
    records(&git(root, &["--no-optional-locks", "ls-files", "-s", "-z"]))
        .into_iter()
        .map(|rec| {
            let tab = rec.iter().position(|b| *b == b'\t').unwrap();
            let mode = std::str::from_utf8(&rec[..6]).unwrap();
            (
                rec[tab + 1..].to_vec(),
                u32::from_str_radix(mode, 8).unwrap(),
            )
        })
        .collect()
}

pub fn git_untracked(root: &Path) -> Vec<Vec<u8>> {
    records(&git(
        root,
        &[
            "--no-optional-locks",
            "ls-files",
            "-o",
            "--exclude-standard",
            "-z",
        ],
    ))
}

/// `git diff --raw HEAD` as change tracking runs it: (path, HEAD mode, HEAD id).
pub fn git_head_changes(root: &Path) -> Vec<(Vec<u8>, u32, String)> {
    let out = git(
        root,
        &[
            "--no-optional-locks",
            "diff",
            "--raw",
            "-z",
            "--no-renames",
            "--relative",
            "--no-abbrev",
            "--no-ext-diff",
            "--no-color",
            "--ignore-submodules=none",
            "HEAD",
            "--",
        ],
    );
    let recs = records(&out);
    recs.chunks(2)
        .map(|pair| {
            let meta = String::from_utf8_lossy(&pair[0]).into_owned();
            let parts: Vec<&str> = meta.trim_start_matches(':').split(' ').collect();
            (
                pair[1].clone(),
                u32::from_str_radix(parts[0], 8).unwrap(),
                parts[2].to_string(),
            )
        })
        .collect()
}
