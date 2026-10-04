//! WS-RT18 golden tests: with the reader forced on, its status view, hidden entries,
//! listings and HEAD changes equal what Git reports on the C8 fixture matrix and after
//! seeded random edits; layouts it does not follow are refused, never misread.

mod gitfix;

use std::collections::BTreeSet;
use std::fs;
use std::ops::ControlFlow;
use std::path::Path;

use gitfix::*;
use xmustard_core::gitindex::status::{Options, Repo, status};
use xmustard_core::gitindex::{self, Listing, Refusal, Setting};

type View = (BTreeSet<(String, Vec<u8>)>, Vec<(u8, Vec<u8>)>);

fn reader_view(root: &Path) -> View {
    let repo = Repo::open(root).unwrap();
    let mut set = BTreeSet::new();
    let opts = Options {
        untracked: true,
        list_ignored: true,
    };
    let out = status(&repo, opts, &mut |c| {
        set.insert((String::from_utf8_lossy(&c.xy).into_owned(), c.path));
        ControlFlow::Continue(())
    })
    .unwrap_or_else(|e| panic!("{}: {e}", root.display()));
    assert_eq!(out.untracked_incomplete, None, "{}", root.display());
    let hidden = out.hidden.into_iter().map(|e| (e.xy[1], e.path)).collect();
    (set, hidden)
}

fn git_view(root: &Path) -> View {
    (git_status(root), git_hidden(root))
}

fn show(set: &BTreeSet<(String, Vec<u8>)>) -> Vec<String> {
    set.iter()
        .map(|(xy, p)| format!("{xy} {}", String::from_utf8_lossy(p)))
        .collect()
}

fn assert_same_view(what: &str, root: &Path) {
    let (want, want_hidden) = git_view(root);
    let (got, got_hidden) = reader_view(root);
    assert_eq!(show(&got), show(&want), "{what}: status (reader vs git)");
    assert_eq!(got_hidden, want_hidden, "{what}: hidden entries");
}

#[test]
fn the_reader_equals_git_status_on_the_matrix() {
    for (name, build) in MATRIX {
        let fx = build();
        assert_same_view(name, &fx.root);
    }
}

fn reader_list(root: &Path, which: Listing<'_>) -> Vec<(Vec<u8>, u32)> {
    let mut got = Vec::new();
    gitindex::list_using(root, Setting::Reader, which, &mut |p, m| {
        got.push((p.to_vec(), m))
    })
    .unwrap();
    got
}

#[test]
fn the_listings_equal_ls_files_on_the_matrix() {
    for (name, build) in MATRIX {
        let fx = build();
        let mut roots = vec![fx.root.clone()];
        roots.extend(
            ["sub", "src", "d1"]
                .iter()
                .map(|d| fx.root.join(d))
                .filter(|d| d.is_dir()),
        );
        for root in roots {
            assert_eq!(
                reader_list(&root, Listing::Tracked),
                git_tracked(&root),
                "{name} {}: tracked",
                root.display()
            );
            let untracked: Vec<Vec<u8>> = reader_list(&root, Listing::Untracked)
                .into_iter()
                .map(|(p, _)| p)
                .collect();
            assert_eq!(
                untracked,
                git_untracked(&root),
                "{name} {}: untracked",
                root.display()
            );
        }
    }
}

#[test]
fn head_changes_equal_git_diff_head_on_the_matrix() {
    for (name, build) in MATRIX {
        let fx = build();
        let width = git(&fx.root, &["rev-parse", "HEAD"]).len() - 1;
        let got: Vec<(Vec<u8>, u32, String)> =
            gitindex::head_changes_using(&fx.root, Setting::Reader)
                .unwrap()
                .unwrap()
                .into_iter()
                .map(|c| {
                    (
                        c.path,
                        c.mode,
                        c.oid.map_or_else(|| "0".repeat(width), |o| o.hex()),
                    )
                })
                .collect();
        assert_eq!(
            got,
            git_head_changes(&fx.root),
            "{name}: changes against HEAD"
        );
    }
}

/// Rewrite `.git/index` as `edit` makes it, with a correct SHA-1 trailer.
fn rewrite_index(root: &Path, edit: impl FnOnce(&mut Vec<u8>)) {
    use sha1::Digest;
    let path = root.join(".git/index");
    let mut bytes = fs::read(&path).unwrap();
    bytes.truncate(bytes.len() - 20);
    edit(&mut bytes);
    let sum = sha1::Sha1::digest(&bytes);
    bytes.extend_from_slice(&sum);
    fs::write(&path, bytes).unwrap();
}

fn refusal(root: &Path) -> Refusal {
    let repo = Repo::open(root).unwrap();
    let opts = Options {
        untracked: true,
        list_ignored: false,
    };
    status(&repo, opts, &mut |_| ControlFlow::Continue(())).unwrap_err()
}

#[test]
fn an_unknown_required_extension_is_refused() {
    let fx = clean();
    rewrite_index(&fx.root, |b| b.extend_from_slice(b"zqrx\0\0\0\0"));
    assert!(
        matches!(refusal(&fx.root), Refusal::Unsupported(ref m) if m.contains("zqrx")),
        "{:?}",
        refusal(&fx.root)
    );
}

#[test]
fn an_unknown_optional_extension_is_skipped() {
    let fx = dirty();
    let want = git_view(&fx.root);
    rewrite_index(&fx.root, |b| b.extend_from_slice(b"ZQRX\0\0\0\x03abc"));
    assert_eq!(reader_view(&fx.root), want);
}

#[test]
fn a_corrupt_checksum_is_a_failure_not_an_answer() {
    let fx = clean();
    let path = fx.root.join(".git/index");
    let mut bytes = fs::read(&path).unwrap();
    let last = bytes.len() - 1;
    bytes[last] ^= 0xff;
    fs::write(&path, bytes).unwrap();
    assert!(
        matches!(refusal(&fx.root), Refusal::Failed(ref m) if m.contains("checksum")),
        "{:?}",
        refusal(&fx.root)
    );
}

#[test]
fn a_null_trailer_is_accepted() {
    let fx = dirty();
    git(&fx.root, &["config", "index.skipHash", "true"]);
    write(&fx.root, "skip.txt", b"s\n");
    git(&fx.root, &["add", "skip.txt"]);
    let bytes = fs::read(fx.root.join(".git/index")).unwrap();
    assert!(
        bytes[bytes.len() - 20..].iter().all(|b| *b == 0),
        "no null trailer"
    );
    assert_same_view("skipHash", &fx.root);
}

/// A small deterministic generator (xorshift64*).
struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545_F491_4F6C_DD1D)
    }

    fn pick<'a, T>(&mut self, items: &'a [T]) -> &'a T {
        &items[(self.next() % items.len() as u64) as usize]
    }
}

const NAMES: &[&str] = &[
    "a.txt",
    "b.txt",
    "d1/c.txt",
    "d1/d2/e.txt",
    "d3/f.txt",
    "g.log",
    "h.txt",
    "d4/i.txt",
];

type Op = fn(&Path, &str, &mut Rng);

fn op_write(r: &Path, f: &str, g: &mut Rng) {
    let _ = fs::remove_dir_all(r.join(f));
    let _ = fs::remove_file(r.join(f));
    let len = 1 + g.next() % 40;
    write(r, f, "x".repeat(len as usize).as_bytes());
}

fn op_same_size(r: &Path, f: &str, g: &mut Rng) {
    if let Ok(b) = fs::read(r.join(f)) {
        let c = b'a' + (g.next() % 26) as u8;
        fs::write(r.join(f), vec![c; b.len()]).unwrap();
    }
}

fn op_delete(r: &Path, f: &str, _: &mut Rng) {
    let _ = fs::remove_file(r.join(f));
}

fn op_chmod(r: &Path, f: &str, g: &mut Rng) {
    if fs::symlink_metadata(r.join(f)).is_ok_and(|m| m.is_file()) {
        chmod(r, f, if g.next() % 2 == 0 { 0o755 } else { 0o644 });
    }
}

fn op_symlink(r: &Path, f: &str, g: &mut Rng) {
    let _ = fs::remove_file(r.join(f));
    if let Some(parent) = r.join(f).parent() {
        fs::create_dir_all(parent).unwrap();
    }
    let _ = std::os::unix::fs::symlink(*g.pick(NAMES), r.join(f));
}

fn op_add(r: &Path, f: &str, _: &mut Rng) {
    run_git(r, &["add", "--", f], b"");
}

fn op_intent(r: &Path, f: &str, _: &mut Rng) {
    run_git(r, &["add", "-N", "--", f], b"");
}

fn op_rm_cached(r: &Path, f: &str, _: &mut Rng) {
    run_git(
        r,
        &["rm", "-q", "--cached", "--ignore-unmatch", "--", f],
        b"",
    );
}

fn op_commit(r: &Path, _: &str, _: &mut Rng) {
    run_git(r, &["add", "-A"], b"");
    run_git(r, &["commit", "-qm", "fuzz", "--allow-empty"], b"");
}

fn op_ignore(r: &Path, _: &str, g: &mut Rng) {
    let pattern = *g.pick(&["*.log", "d3/", "/h.txt", "d1/d2/", "!g.log", "i.*"]);
    let mut text = fs::read_to_string(r.join(".gitignore")).unwrap_or_default();
    text.push_str(pattern);
    text.push('\n');
    fs::write(r.join(".gitignore"), text).unwrap();
}

fn op_touch(r: &Path, f: &str, _: &mut Rng) {
    if fs::symlink_metadata(r.join(f)).is_ok_and(|m| m.is_file()) {
        touch(r, f, 0);
    }
}

fn op_flag(r: &Path, f: &str, g: &mut Rng) {
    let flag = *g.pick(&[
        "--assume-unchanged",
        "--skip-worktree",
        "--no-assume-unchanged",
        "--no-skip-worktree",
    ]);
    run_git(r, &["update-index", flag, "--", f], b"");
}

const OPS: &[(&str, Op)] = &[
    ("write", op_write),
    ("same_size", op_same_size),
    ("delete", op_delete),
    ("chmod", op_chmod),
    ("symlink", op_symlink),
    ("add", op_add),
    ("intent", op_intent),
    ("rm_cached", op_rm_cached),
    ("commit", op_commit),
    ("ignore", op_ignore),
    ("touch", op_touch),
    ("flag", op_flag),
];

#[test]
fn random_edits_keep_the_reader_equal_to_git() {
    for seed in [
        0x9E37_79B9_7F4A_7C15u64,
        0xD1B5_4A32_D192_ED03,
        0x2545_F491_4F6C_DD1D,
    ] {
        let fx = clean();
        let r = &fx.root;
        for f in NAMES {
            write(r, f, f.as_bytes());
        }
        commit(r, "fuzz base");
        let mut g = Rng(seed);
        let mut history = Vec::new();
        for round in 0..40 {
            let (name, op) = g.pick(OPS);
            let file = *g.pick(NAMES);
            op(r, file, &mut g);
            history.push(format!("{name} {file}"));
            let (want, want_hidden) = git_view(r);
            let (got, got_hidden) = reader_view(r);
            assert_eq!(
                (show(&got), got_hidden),
                (show(&want), want_hidden),
                "seed {seed:#x} round {round}: {history:?}"
            );
        }
    }
}
