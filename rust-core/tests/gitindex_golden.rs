//! WS-RT18 golden tests: with the reader forced on, its status view, hidden entries,
//! listings and HEAD changes equal what Git reports on the C8 fixture matrix and after
//! seeded random edits; layouts it does not follow are refused, never misread.

mod gitfix;

use std::collections::BTreeSet;
use std::fs;
use std::io::Write;
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
fn unborn_submodule_head_still_reports_its_untracked_worktree() {
    let fx = clean();
    let child = fx.root.join("child");
    init(&child, &[]);
    write(&child, "loose.txt", b"untracked\n");
    let oid = String::from_utf8(git(&fx.root, &["rev-parse", "HEAD"]))
        .unwrap()
        .trim()
        .to_string();
    git(
        &fx.root,
        &[
            "update-index",
            "--add",
            "--cacheinfo",
            "160000",
            &oid,
            "child",
        ],
    );
    assert_same_view("unborn submodule with untracked content", &fx.root);
}

#[test]
fn auto_mode_does_not_fallback_through_a_submodule_refusal() {
    let fx = clean();
    let child = fx.root.join("child");
    init(&child, &[]);
    git(&child, &["config", "core.ignorecase", "true"]);
    for i in 0..100_000 {
        write(
            &child,
            &format!("tracked/file-{i:06}-abcdefghijklmnop.txt"),
            b"tracked\n",
        );
    }
    commit(&child, "large child index");
    let oid = String::from_utf8(git(&child, &["rev-parse", "HEAD"]))
        .unwrap()
        .trim()
        .to_owned();
    git(
        &fx.root,
        &[
            "update-index",
            "--add",
            "--cacheinfo",
            "160000",
            &oid,
            "child",
        ],
    );
    let identity = xmustard_core::indexcache::source_identity(&fx.root);
    assert!(!identity.identity_complete);
    assert!(identity.limitations.iter().any(|limitation| {
        limitation.detail.contains("submodule")
            && limitation.detail.contains("case-folded index paths")
    }));
}

#[test]
fn submodule_with_incomplete_untracked_walk_refuses_parent_status() {
    let fx = clean();
    let child = fx.root.join("child");
    init(&child, &[]);
    commit(&child, "submodule base");
    let oid = String::from_utf8(git(&child, &["rev-parse", "HEAD"]))
        .unwrap()
        .trim()
        .to_string();
    git(
        &fx.root,
        &[
            "update-index",
            "--add",
            "--cacheinfo",
            "160000",
            &oid,
            "child",
        ],
    );
    let wide = child.join("wide");
    fs::create_dir(&wide).unwrap();
    for i in 0..160_000 {
        fs::File::create(wide.join(format!("f{i}"))).unwrap();
    }
    let err = refusal(&fx.root);
    assert!(matches!(err, Refusal::Submodule(message)
        if message.contains("submodule") && message.contains("untracked walk incomplete")));
}

#[test]
fn core_symlinks_false_compares_the_link_text_as_file_content() {
    let fx = clean();
    symlink(&fx.root, "target.txt", "link");
    commit(&fx.root, "symlink");
    git(&fx.root, &["config", "core.symlinks", "false"]);
    fs::remove_file(fx.root.join("link")).unwrap();
    write(&fx.root, "link", b"target.txt");
    assert_same_view("core.symlinks=false", &fx.root);

    let config = fx.root.join(".git/config");
    fs::OpenOptions::new()
        .append(true)
        .open(&config)
        .unwrap()
        .write_all(b"\n[core]\n\tsymlinks\n")
        .unwrap();
    assert_same_view("core.symlinks valueless true", &fx.root);

    fs::OpenOptions::new()
        .append(true)
        .open(&config)
        .unwrap()
        .write_all(b"\n[core]\n\tsymlinks =\n")
        .unwrap();
    assert_same_view("core.symlinks empty false", &fx.root);
    fs::OpenOptions::new()
        .append(true)
        .open(&config)
        .unwrap()
        .write_all(b"\n[core]\n\tsymlinks = maybe\n")
        .unwrap();
    assert!(Repo::open(&fx.root).is_err());
}

#[test]
fn ignorecase_listing_does_not_walk_a_case_folded_sibling() {
    let fx = clean();
    git(&fx.root, &["config", "core.ignorecase", "true"]);
    fs::create_dir_all(fx.root.join("src")).unwrap();
    fs::create_dir_all(fx.root.join("Src")).unwrap();
    write(&fx.root, "src/wanted.txt", b"wanted\n");
    write(&fx.root, "Src/.gitignore", &vec![b'x'; 1 << 20 | 1]);
    write(&fx.root, "Src/other.txt", b"other\n");
    assert_eq!(Repo::open(&fx.root.join("src")).unwrap().prefix, b"src/");
    let got: Vec<Vec<u8>> = reader_list(&fx.root.join("src"), Listing::Untracked)
        .into_iter()
        .map(|(path, _)| path)
        .collect();
    assert_eq!(got, git_untracked(&fx.root.join("src")));
}

#[test]
fn ignorecase_cursor_matches_a_lowercase_index_entry_to_an_uppercase_worktree_name() {
    let fx = clean();
    write(&fx.root, "zeta.txt", b"tracked\n");
    commit(&fx.root, "lowercase path");
    git(&fx.root, &["config", "core.ignorecase", "true"]);
    write(&fx.root, "ZETA.txt", b"tracked\n");
    assert_same_view("core.ignorecase case alias", &fx.root);
}

#[test]
fn ignorecase_exclusion_patterns_match_ascii_case_insensitively() {
    let fx = clean();
    write(
        &fx.root,
        ".gitignore",
        b"CACHE/\n*.TMP\n[!A-z].range\n[!A-Z].neg\n[A-Z].pos\n",
    );
    commit(&fx.root, "ignore rules");
    git(&fx.root, &["config", "core.ignorecase", "true"]);
    write(&fx.root, "cache/file", b"ignored\n");
    write(&fx.root, "scratch.tmp", b"ignored\n");
    write(&fx.root, "_.range", b"not ignored by the A-z range\n");
    write(&fx.root, "_.neg", b"matches the negated class\n");
    write(&fx.root, "a.neg", b"excluded lowercase letter\n");
    write(&fx.root, "A.neg", b"excluded uppercase letter\n");
    write(&fx.root, "a.pos", b"ignored by the positive A-Z class\n");
    write(&fx.root, "A.POS", b"ignored by the positive A-Z class\n");
    assert_same_view(
        "core.ignorecase ignore rules and character ranges",
        &fx.root,
    );
}

#[test]
fn ignorecase_reader_matches_git_with_byte_sorted_mixed_case_paths() {
    let fx = clean();
    write(&fx.root, "README.md", b"readme\n");
    write(&fx.root, "api/lib.rs", b"pub fn tracked() {}\n");
    write(&fx.root, ".gitignore", b"CACHE/\n*.TMP\n");
    commit(&fx.root, "mixed case");
    git(&fx.root, &["config", "core.ignorecase", "true"]);
    write(&fx.root, "readme.MD", b"readme\n");
    write(&fx.root, "API/new.rs", b"new\n");
    write(&fx.root, "cache/file", b"ignored\n");
    write(&fx.root, "scratch.tmp", b"ignored\n");
    assert_same_view("core.ignorecase mixed case", &fx.root);
    assert_eq!(
        reader_list(&fx.root, Listing::Tracked),
        git_tracked(&fx.root)
    );
    let got: Vec<Vec<u8>> = reader_list(&fx.root, Listing::Untracked)
        .into_iter()
        .map(|(path, _)| path)
        .collect();
    assert_eq!(got, git_untracked(&fx.root));
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
fn staged_submodule_bump_keeps_git_diff_head_semantics_after_worktree_revert() {
    let fx = submodules();
    let old = String::from_utf8(git(&fx.root, &["rev-parse", "HEAD:m_commit"]))
        .unwrap()
        .trim()
        .to_string();
    git(&fx.root, &["add", "m_commit"]);
    git(&fx.root.join("m_commit"), &["reset", "--hard", &old]);
    let current = String::from_utf8(git(&fx.root.join("m_commit"), &["rev-parse", "HEAD"]))
        .unwrap()
        .trim()
        .to_string();
    let staged = String::from_utf8(git(&fx.root, &["rev-parse", ":m_commit"]))
        .unwrap()
        .trim()
        .to_string();
    assert_eq!(current, old, "submodule worktree is at HEAD's gitlink");
    assert_ne!(staged, old, "the parent index still holds the staged bump");
    let got = gitindex::head_changes_using(&fx.root, Setting::Reader)
        .unwrap()
        .unwrap();
    let width = git(&fx.root, &["rev-parse", "HEAD"]).len() - 1;
    let want = git_head_changes(&fx.root);
    assert_eq!(
        got.iter()
            .map(|change| (
                change.path.clone(),
                change.mode,
                change
                    .oid
                    .map_or_else(|| "0".repeat(width), |oid| oid.hex())
            ))
            .collect::<Vec<_>>(),
        want,
        "staged gitlink bump reverted in the worktree"
    );
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
fn ignorecase_reader_refuses_an_index_that_is_not_byte_sorted() {
    let fx = clean();
    git(&fx.root, &["config", "core.ignorecase", "true"]);
    rewrite_index(&fx.root, |bytes| {
        let count = u32::from_be_bytes(bytes[8..12].try_into().unwrap());
        assert!(count >= 2);
        let mut ranges = Vec::new();
        let mut at = 12;
        for _ in 0..count {
            let flags = u16::from_be_bytes([bytes[at + 60], bytes[at + 61]]);
            let len = usize::from(flags & 0x0fff);
            let end = at + ((63 + len + 7) & !7);
            ranges.push(at..end);
            at = end;
        }
        let first = ranges[0].clone();
        let second = ranges[1].clone();
        let a = bytes[first.clone()].to_vec();
        let b = bytes[second.clone()].to_vec();
        assert!(first.end <= second.start);
        bytes.splice(second.clone(), a);
        bytes.splice(first, b);
    });
    assert!(
        matches!(refusal(&fx.root), Refusal::Failed(ref message) if message.contains("byte-sorted"))
    );
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
        chmod(
            r,
            f,
            if g.next().is_multiple_of(2) {
                0o755
            } else {
                0o644
            },
        );
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

/// A split index whose link extension declares more delete-bitmap bits than its
/// shared index has entries is refused before anything is allocated for them.
#[test]
fn a_split_bitmap_past_the_shared_index_is_refused() {
    let fx = split_index();
    rewrite_index(&fx.root, |b| {
        let at = b
            .windows(4)
            .rposition(|w| w == b"link")
            .expect("a link extension");
        // signature, size, shared index id; then the delete bitmap's bit count
        let bits = at + 8 + 20;
        b[bits..bits + 4].copy_from_slice(&u32::MAX.to_be_bytes());
    });
    assert!(
        matches!(refusal(&fx.root), Refusal::Failed(ref m) if m.contains("bitmaps")),
        "{:?}",
        refusal(&fx.root)
    );
}

/// The Git fallback's cost estimate counts a split index's shared entries: a shared
/// index of 100 entries beside an index of 3 is at least 100 entries for Git to load.
#[test]
fn the_fallback_estimate_counts_the_shared_index() {
    let small = clean();
    let big = clean();
    for i in 0..97 {
        write(&big.root, &format!("many/f{i:03}.txt"), b"m\n");
    }
    commit(&big.root, "many");
    assert_eq!(git_tracked(&big.root).len(), 100);
    let git_dir = small.root.join(".git");
    fs::copy(
        big.root.join(".git/index"),
        git_dir.join(format!("sharedindex.{}", "0".repeat(40))),
    )
    .unwrap();
    let counted = gitindex::loaded_entries(&git_dir).unwrap();
    assert!(counted >= 100, "{counted} entries counted");
}

/// A config Git lists past the reader's bound is a refusal, not an empty config that
/// would skip `core.ignorecase` and `core.excludesFile`.
#[test]
fn a_config_the_reader_cannot_take_is_a_refusal() {
    let fx = clean();
    let mut config = fs::read_to_string(fx.root.join(".git/config")).unwrap();
    config.push_str("[xmpad]\n");
    for i in 0..40_000 {
        config.push_str(&format!("\tkey{i} = {}\n", "v".repeat(24)));
    }
    fs::write(fx.root.join(".git/config"), config).unwrap();
    assert!(
        matches!(Repo::open(&fx.root), Err(Refusal::Failed(ref m)) if m.contains("git config")),
        "{:?}",
        Repo::open(&fx.root).map(|_| ())
    );
}

/// A directory whose listing is past the walk's bound (counted with each child's own
/// cost, not only its name) leaves the untracked walk incomplete with its reason.
#[test]
fn a_directory_past_the_walk_bound_is_reported_incomplete() {
    let fx = clean();
    let dir = fx.root.join("wide");
    fs::create_dir(&dir).unwrap();
    // 160,000 names of 6-7 bytes: about 1 MiB of names, past 8 MiB with their slots
    for i in 0..160_000 {
        fs::File::create(dir.join(format!("f{i}"))).unwrap();
    }
    let repo = Repo::open(&fx.root).unwrap();
    let opts = Options {
        untracked: true,
        list_ignored: false,
    };
    let out = status(&repo, opts, &mut |_| ControlFlow::Continue(())).unwrap();
    assert!(
        out.untracked_incomplete
            .as_deref()
            .is_some_and(|w| w.contains("past")),
        "{:?}",
        out.untracked_incomplete
    );
}
