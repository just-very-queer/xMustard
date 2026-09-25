//! Regressions for the 2026-09-24 Rust audit (docs/reviews/2026-09-24-rust-audit.md):
//! warm-cache freshness under dirty edits (finding 1) and honest symbol/file coverage
//! (finding 2). Every fixture is a disposable temporary Git repository.

use std::fs;
use std::path::Path;
use std::process::Command;
use std::time::{Duration, SystemTime};

use tempfile::TempDir;
use xmustard_core::symbolgraph::{SymbolGraph, build_symbol_graph, build_symbol_graph_cached};

fn git(root: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .expect("git runs");
    assert!(
        out.status.success(),
        "git {args:?} failed: {}",
        String::from_utf8_lossy(&out.stderr)
    );
}

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        let path = dir.path().join(rel);
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(path, content).unwrap();
    }
    git(dir.path(), &["init", "-q", "-b", "main"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["config", "commit.gpgsign", "false"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn has_symbol(g: &SymbolGraph, name: &str) -> bool {
    g.symbols.iter().any(|s| s.name == name)
}

fn set_mtime(path: &Path, t: SystemTime) {
    fs::File::options()
        .write(true)
        .open(path)
        .unwrap()
        .set_modified(t)
        .unwrap();
}

/// Audit finding 1, exact reproduction: the first porcelain row lost its leading
/// status space, so repeated edits to an already-dirty file reused the stale graph.
#[test]
fn dirty_to_dirty_edit_invalidates_warm_graph() {
    let r = repo(&[("a.rs", "pub fn initial_symbol() {}\n")]);
    fs::write(r.path().join("a.rs"), "pub fn first_dirty_symbol() {}\n").unwrap();
    let g1 = build_symbol_graph_cached(r.path(), "audit");
    assert!(has_symbol(&g1, "first_dirty_symbol"));
    fs::write(
        r.path().join("a.rs"),
        "pub fn second_dirty_symbol_added_later() {}\n",
    )
    .unwrap();
    let g2 = build_symbol_graph_cached(r.path(), "audit");
    assert!(
        has_symbol(&g2, "second_dirty_symbol_added_later")
            && !has_symbol(&g2, "first_dirty_symbol"),
        "warm cache served a stale graph: {:?}",
        g2.symbols.iter().map(|s| &s.name).collect::<Vec<_>>()
    );
}

/// Same size, same mtime, dirty -> dirty: only content hashing can see this edit.
#[test]
fn same_size_same_mtime_dirty_edit_invalidates_warm_graph() {
    let r = repo(&[
        ("z.rs", "pub fn zzz_anchor() {}\n"),
        ("b.rs", "pub fn base_symbol() {}\n"),
    ]);
    let file = r.path().join("b.rs");
    let pinned = SystemTime::now() - Duration::from_secs(3600);
    fs::write(&file, "pub fn edit_one_aaaa() {}\n").unwrap();
    set_mtime(&file, pinned);
    let g1 = build_symbol_graph_cached(r.path(), "ws");
    assert!(has_symbol(&g1, "edit_one_aaaa"));
    fs::write(&file, "pub fn edit_two_bbbb() {}\n").unwrap();
    set_mtime(&file, pinned);
    let g2 = build_symbol_graph_cached(r.path(), "ws");
    assert!(
        has_symbol(&g2, "edit_two_bbbb") && !has_symbol(&g2, "edit_one_aaaa"),
        "same-size/same-mtime edit reused a stale graph"
    );
}

/// Filenames with leading spaces, embedded newlines and tabs must round-trip through
/// status parsing (the old parser trimmed and split on newlines).
#[test]
fn unusual_filenames_invalidate_warm_graph() {
    let names = [" lead.rs", "new\nline.rs", "tab\there.rs", "trail .rs"];
    let files: Vec<(&str, &str)> = names
        .iter()
        .map(|n| (*n, "pub fn orig_sym() {}\n"))
        .collect();
    let r = repo(&files);
    for (i, name) in names.iter().enumerate() {
        let first = format!("pub fn first_{i}_name() {{}}\n");
        let second = format!("pub fn second_{i}_name_longer() {{}}\n");
        fs::write(r.path().join(name), &first).unwrap();
        let g1 = build_symbol_graph_cached(r.path(), "ws");
        assert!(
            has_symbol(&g1, &format!("first_{i}_name")),
            "{name:?} first edit"
        );
        fs::write(r.path().join(name), &second).unwrap();
        let g2 = build_symbol_graph_cached(r.path(), "ws");
        assert!(
            has_symbol(&g2, &format!("second_{i}_name_longer")),
            "{name:?}: repeated dirty edit served stale graph"
        );
    }
}

#[test]
fn rename_delete_branch_and_untracked_are_current() {
    let r = repo(&[
        ("keep.rs", "pub fn keep_symbol() {}\n"),
        ("old.rs", "pub fn renamed_symbol() {}\n"),
        ("gone.rs", "pub fn deleted_symbol() {}\n"),
    ]);
    let g0 = build_symbol_graph_cached(r.path(), "ws");
    assert!(has_symbol(&g0, "deleted_symbol"));

    // staged rename, then edit the renamed file while it is dirty
    git(r.path(), &["mv", "old.rs", "new.rs"]);
    let g1 = build_symbol_graph_cached(r.path(), "ws");
    assert!(g1.files.iter().any(|f| f.path == "new.rs"));
    assert!(
        !g1.files.iter().any(|f| f.path == "old.rs"),
        "renamed-away path still indexed"
    );
    fs::write(r.path().join("new.rs"), "pub fn renamed_then_edited() {}\n").unwrap();
    let g2 = build_symbol_graph_cached(r.path(), "ws");
    assert!(has_symbol(&g2, "renamed_then_edited") && !has_symbol(&g2, "renamed_symbol"));

    // deletion (unstaged) removes the symbols
    fs::remove_file(r.path().join("gone.rs")).unwrap();
    let g3 = build_symbol_graph_cached(r.path(), "ws");
    assert!(
        !has_symbol(&g3, "deleted_symbol"),
        "deleted file still served from cache"
    );

    // untracked file changes the repository identity
    let k_before = xmustard_core::indexcache::cheap_key(r.path());
    fs::write(r.path().join("untracked.rs"), "pub fn untracked_one() {}\n").unwrap();
    let k_mid = xmustard_core::indexcache::cheap_key(r.path());
    fs::write(r.path().join("untracked.rs"), "pub fn untracked_two() {}\n").unwrap();
    let k_after = xmustard_core::indexcache::cheap_key(r.path());
    assert_ne!(
        k_before, k_mid,
        "adding an untracked file must change the key"
    );
    assert_ne!(
        k_mid, k_after,
        "editing an untracked file must change the key"
    );

    // branch switch
    git(r.path(), &["add", "-A"]);
    git(r.path(), &["commit", "-qm", "d"]);
    git(r.path(), &["checkout", "-q", "-b", "other"]);
    fs::write(
        r.path().join("keep.rs"),
        "pub fn branch_other_symbol() {}\n",
    )
    .unwrap();
    git(r.path(), &["commit", "-qam", "o"]);
    let g4 = build_symbol_graph_cached(r.path(), "ws");
    assert!(has_symbol(&g4, "branch_other_symbol"));
    git(r.path(), &["checkout", "-q", "main"]);
    let g5 = build_symbol_graph_cached(r.path(), "ws");
    assert!(
        has_symbol(&g5, "keep_symbol") && !has_symbol(&g5, "branch_other_symbol"),
        "branch switch served the other branch's graph"
    );
}

fn many_symbols(n: usize) -> String {
    (1..=n)
        .map(|i| format!("pub fn symbol_{i:03}() {{}}\n"))
        .collect()
}

/// Audit finding 2: symbols beyond the old 32/64 caps must be indexed.
#[test]
fn symbols_beyond_prior_caps_are_indexed() {
    let r = repo(&[
        ("many.rs", &many_symbols(70)),
        ("user.rs", "fn go() { symbol_070(); }\n"),
    ]);
    let g = build_symbol_graph(r.path(), "coverage-audit");
    let many = g.files.iter().find(|f| f.path == "many.rs").unwrap();
    assert_eq!(many.symbol_count, 70, "per-file symbol count capped");
    assert!(
        has_symbol(&g, "symbol_070"),
        "symbol_070 missing from graph"
    );
    assert!(
        g.edges
            .iter()
            .any(|e| e.from_path == "user.rs" && e.to_path == "many.rs"),
        "reference to a symbol beyond the old cap produced no edge"
    );
}

/// Audit finding 2: unreadable, oversized and invalid-UTF-8 inputs were silently
/// counted as indexed with no degradation.
#[test]
fn unreadable_oversized_invalid_utf8_are_reported() {
    let big = format!(
        "pub fn big_symbol() {{}}\n{}",
        "// pad\n".repeat((9 << 20) / 7)
    );
    let r = repo(&[
        ("ok.rs", "pub fn fine_symbol() {}\n"),
        ("big.rs", &big),
        ("secret.rs", "pub fn locked_symbol() {}\n"),
    ]);
    fs::write(
        r.path().join("bad.rs"),
        b"pub fn latin_sym() {} // caf\xe9\n",
    )
    .unwrap();
    git(r.path(), &["add", "bad.rs"]);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(
            r.path().join("secret.rs"),
            fs::Permissions::from_mode(0o000),
        )
        .unwrap();
    }
    let g = build_symbol_graph(r.path(), "ws");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(
            r.path().join("secret.rs"),
            fs::Permissions::from_mode(0o644),
        )
        .unwrap();
    }
    let json = serde_json::to_value(&g.coverage).unwrap();
    assert!(
        g.coverage.degraded_reason.is_some(),
        "coverage hid read failures: {json}"
    );
    assert!(
        g.coverage.indexed_files < g.coverage.eligible_files,
        "unreadable/oversized files counted as indexed: {json}"
    );
    // invalid UTF-8 is decoded lossily, still indexed, and reported.
    assert!(
        has_symbol(&g, "latin_sym"),
        "invalid-UTF-8 file lost all symbols"
    );
    let text = json.to_string();
    for reason in ["oversized", "unreadable", "invalid_utf8"] {
        assert!(text.contains(reason), "coverage missing {reason}: {json}");
    }
    let c = &g.coverage;
    for reason in ["oversized", "unreadable", "invalid_utf8"] {
        assert_eq!(c.loss_counts.get(reason), Some(&1), "{json}");
    }
    assert_eq!((c.eligible_files, c.indexed_files), (4, 2), "{json}");
    assert!(!c.complete && c.degraded_reason.is_some());
    let loss = |p: &str| c.losses.iter().find(|l| l.path == p).unwrap();
    assert!(!loss("big.rs").content_indexed && !loss("secret.rs").content_indexed);
    assert!(loss("bad.rs").content_indexed);
    assert!(!has_symbol(&g, "big_symbol") && !has_symbol(&g, "locked_symbol"));
}

/// Past the per-file extraction bound the file is visibly truncated, not silently cut.
#[test]
fn per_file_symbol_bound_is_reported() {
    let n = xmustard_core::treesitter::MAX_SYMBOLS_PER_FILE + 5;
    let r = repo(&[
        ("gen.rs", &many_symbols_wide(n)),
        ("ok.rs", "pub fn ok_symbol() {}\n"),
    ]);
    let g = build_symbol_graph(r.path(), "ws");
    let c = &g.coverage;
    assert_eq!(c.symbols_truncated_files, 1);
    assert!(c.truncated && !c.complete);
    assert_eq!(c.loss_counts.get("symbols_truncated"), Some(&1));
    let gen_node = g.files.iter().find(|f| f.path == "gen.rs").unwrap();
    assert_eq!(
        gen_node.symbol_count,
        xmustard_core::treesitter::MAX_SYMBOLS_PER_FILE
    );
}

fn many_symbols_wide(n: usize) -> String {
    (1..=n)
        .map(|i| format!("pub fn gen_{i:05}() {{}}\n"))
        .collect()
}

/// An unchanged file is neither read nor parsed after a one-file edit: its features
/// come from the per-file cache keyed by its Git blob identity. Dependent references
/// are re-resolved every build, so a definition moving files re-routes the unchanged
/// caller's edge without reparsing the caller. (The red run used a chmod-000 proxy;
/// Git itself reports such a file as modified, so the counters are the direct proof.)
#[test]
fn one_file_edit_does_not_reread_unchanged_files() {
    let r = repo(&[
        ("caller.rs", "fn run() { moving_target(); }\n"),
        ("stable.rs", "pub fn stable_symbol() {}\n"),
        ("def_a.rs", "pub fn moving_target() {}\n"),
        ("def_b.rs", "pub fn other_thing() {}\n"),
    ]);
    let cold = build_symbol_graph_cached(r.path(), "ws");
    let w = &cold.coverage.work;
    assert_eq!(
        (w.graph_cache.as_str(), w.files_parsed, w.files_reused),
        ("miss", 4, 0)
    );
    assert!(
        cold.edges
            .iter()
            .any(|e| e.from_path == "caller.rs" && e.to_path == "def_a.rs")
    );

    let warm = build_symbol_graph_cached(r.path(), "ws");
    let w = &warm.coverage.work;
    assert_eq!(
        (w.graph_cache.as_str(), w.files_read, w.files_parsed),
        ("hit", 0, 0)
    );

    // move the definition: two files change, caller.rs and stable.rs do not.
    fs::write(r.path().join("def_a.rs"), "pub fn renamed_away() {}\n").unwrap();
    fs::write(r.path().join("def_b.rs"), "pub fn moving_target() {}\n").unwrap();
    let edited = build_symbol_graph_cached(r.path(), "ws");
    let w = &edited.coverage.work;
    assert_eq!(w.graph_cache, "miss");
    assert_eq!(
        (w.files_read, w.files_parsed, w.files_reused),
        (2, 2, 2),
        "{w:?}"
    );
    assert!(
        edited
            .edges
            .iter()
            .any(|e| e.from_path == "caller.rs" && e.to_path == "def_b.rs"),
        "dependent edge was not re-resolved: {:?}",
        edited.edges
    );
    assert!(!edited.edges.iter().any(|e| e.to_path == "def_a.rs"));
    assert!(edited.coverage.complete && edited.coverage.source_identity.stable);
}

/// `git status` hides worktree edits to assume-unchanged and skip-worktree entries, so
/// those bytes must be identified directly: the key changes and the graph is current.
#[test]
fn assume_unchanged_and_skip_worktree_edits_are_identified() {
    let r = repo(&[
        ("au.rs", "pub fn au_original() {}\n"),
        ("sw.rs", "pub fn sw_original() {}\n"),
    ]);
    git(r.path(), &["update-index", "--assume-unchanged", "au.rs"]);
    git(r.path(), &["update-index", "--skip-worktree", "sw.rs"]);
    let k0 = xmustard_core::indexcache::source_identity(r.path());
    let g0 = build_symbol_graph_cached(r.path(), "ws");
    assert!(has_symbol(&g0, "au_original"));
    fs::write(r.path().join("au.rs"), "pub fn au_edited_hidden() {}\n").unwrap();
    fs::write(r.path().join("sw.rs"), "pub fn sw_edited_hidden() {}\n").unwrap();
    let k1 = xmustard_core::indexcache::source_identity(r.path());
    assert_ne!(
        k0.key, k1.key,
        "hidden-entry edit left the identity unchanged"
    );
    assert!(
        k1.identity_complete,
        "hidden bytes were hashed, so identity is complete"
    );
    assert_eq!((k1.hidden_entries, k1.dirty_entries), (2, 0));
    let g1 = build_symbol_graph_cached(r.path(), "ws");
    assert!(
        has_symbol(&g1, "au_edited_hidden") && has_symbol(&g1, "sw_edited_hidden"),
        "graph served index content for hidden-dirty entries"
    );
    assert!(!has_symbol(&g1, "au_original"));
    // sparse-checkout shape: a skip-worktree file absent from the worktree.
    fs::remove_file(r.path().join("sw.rs")).unwrap();
    let k2 = xmustard_core::indexcache::source_identity(r.path());
    assert!(k2.identity_complete && k2.key != k1.key);
    let g2 = build_symbol_graph_cached(r.path(), "ws");
    assert!(!has_symbol(&g2, "sw_edited_hidden") && !has_symbol(&g2, "sw_original"));
    assert_eq!(g2.coverage.worktree_deleted_files, 1);
}

/// Fable F1: untracked bytes past the identity budget are never read by the graph, so
/// they must not block the graph cache. The identity still reports the budget loss.
#[test]
fn untracked_bytes_over_budget_do_not_block_graph_cache() {
    let r = repo(&[("a.rs", "pub fn tracked_symbol() {}\n")]);
    let per_file = 8 << 20;
    let files = (xmustard_core::indexcache::MAX_IDENTITY_BYTES as usize) / per_file + 1;
    for i in 0..files {
        let mut blob = vec![b'u'; per_file];
        blob[0] = i as u8;
        fs::write(r.path().join(format!("blob{i}.dat")), blob).unwrap();
    }
    let id = xmustard_core::indexcache::source_identity(r.path());
    assert!(!id.identity_complete);
    assert!(id.limitations.iter().any(|l| l.reason == "identity_budget"));
    let g1 = build_symbol_graph_cached(r.path(), "ws");
    assert_eq!(
        g1.coverage.work.graph_cache, "miss",
        "{:?}",
        g1.coverage.work
    );
    let g2 = build_symbol_graph_cached(r.path(), "ws");
    assert_eq!(
        g2.coverage.work.graph_cache, "hit",
        "{:?}",
        g2.coverage.work
    );
    assert!(has_symbol(&g2, "tracked_symbol"));
    assert!(!g2.coverage.source_identity.identity_complete);
    // `complete` describes the indexed (tracked) input, which is fully identified;
    // the untracked budget loss is reported by the identity only.
    assert!(g2.coverage.complete, "{:?}", g2.coverage.degraded_reason);
    // the metadata token still re-keys on an edit to an unhashed untracked file.
    fs::write(r.path().join(format!("blob{}.dat", files - 1)), b"shrunk").unwrap();
    let g3 = build_symbol_graph_cached(r.path(), "ws");
    assert_ne!(
        g3.coverage.source_identity.key,
        g2.coverage.source_identity.key
    );
}

/// Fable F2: only changed files are reparsed, including dirty files whose SHA-256 the
/// identity already computed; the reused features still match the reported identity.
#[test]
fn unchanged_dirty_files_are_not_reparsed() {
    let r = repo(&[
        ("f1.rs", "pub fn f1_v0() {}\n"),
        ("f2.rs", "pub fn f2_v0() {}\n"),
        ("f3.rs", "pub fn f3_v0() {}\n"),
        ("f4.rs", "pub fn f4_v0() {}\n"),
        ("f5.rs", "pub fn f5_v0() {}\n"),
    ]);
    fs::write(r.path().join("f1.rs"), "pub fn f1_v1() {}\n").unwrap();
    fs::write(r.path().join("f2.rs"), "pub fn f2_v1() {}\n").unwrap();
    let g1 = build_symbol_graph(r.path(), "ws");
    assert_eq!(g1.coverage.work.files_parsed, 5);
    fs::write(r.path().join("f3.rs"), "pub fn f3_v1() {}\n").unwrap();
    let g2 = build_symbol_graph(r.path(), "ws");
    let w = &g2.coverage.work;
    assert_eq!(
        (w.files_read, w.files_parsed, w.files_reused),
        (1, 1, 4),
        "{w:?}"
    );
    for s in ["f1_v1", "f2_v1", "f3_v1", "f4_v0", "f5_v0"] {
        assert!(has_symbol(&g2, s), "{s} missing");
    }
    assert!(g2.coverage.source_identity.stable && g2.coverage.complete);
}

/// Crash-leftover atomic-write temp files are swept on the next cache store.
#[test]
fn stale_cache_temp_files_are_swept() {
    let r = repo(&[("a.rs", "pub fn a_symbol() {}\n")]);
    let dir = r.path().join(".git/xmustard-cache");
    fs::create_dir_all(&dir).unwrap();
    let stale = dir.join(".symbolgraph-ws-deadbeef.json.tmp.999999");
    fs::write(&stale, b"partial").unwrap();
    set_mtime(&stale, SystemTime::now() - Duration::from_secs(24 * 3600));
    fs::write(r.path().join("a.rs"), "pub fn a_symbol_dirty() {}\n").unwrap();
    let _ = build_symbol_graph_cached(r.path(), "ws");
    assert!(
        !stale.exists(),
        "stale atomic-write temp file was not swept"
    );
}
