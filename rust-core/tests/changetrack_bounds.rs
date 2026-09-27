//! WS-22 (PAR-RT-11, PAR-FRESH-06): change tracking's per-call work is bounded, each
//! source file is read once per pass, and what cannot be determined is reported unknown
//! instead of 0. The baseline records why it was built, and drift reports it.

use std::fs;
use std::path::Path;
use std::process::Command;

use tempfile::TempDir;
use xmustard_core::changetrack::{
    self as ct, BaselineReason, ChangeBounds, ChangeSet, source_reads_on_this_thread,
};

fn git(dir: &Path, args: &[&str]) {
    let out = Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "git {args:?}: {out:?}");
}

fn repo(files: &[(&str, String)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    write(dir.path(), files);
    commit(dir.path());
    dir
}

fn write(dir: &Path, files: &[(&str, String)]) {
    for (path, body) in files {
        fs::write(dir.join(path), body).unwrap();
    }
}

fn commit(dir: &Path) {
    git(dir, &["add", "-A"]);
    git(dir, &["commit", "-qm", "c"]);
}

/// `n` functions of arity `params`, one per line block.
fn functions(n: usize, params: usize) -> String {
    let args: Vec<String> = (0..params).map(|i| format!("a{i}: u32")).collect();
    (0..n)
        .map(|i| format!("pub fn f{i}({}) -> u32 {{\n    {i}\n}}\n", args.join(", ")))
        .collect()
}

fn unknown_fields(cs: &ChangeSet) -> Vec<&str> {
    cs.unknown.iter().map(|u| u.field.as_str()).collect()
}

fn reads_during<T>(f: impl FnOnce() -> T) -> (T, u64) {
    let before = source_reads_on_this_thread();
    let out = f();
    (out, source_reads_on_this_thread() - before)
}

// The no-baseline branch used to list every tracked file and extract symbols from all
// of them with no cap. It now lists at most the cap, reads nothing, and says so.
#[test]
fn no_baseline_changed_since_is_capped_and_reports_truncation() {
    let files: Vec<(String, String)> = (0..12)
        .map(|i| (format!("m{i:02}.rs"), functions(3, 1)))
        .collect();
    let refs: Vec<(&str, String)> = files.iter().map(|(p, b)| (p.as_str(), b.clone())).collect();
    let r = repo(&refs);
    let data = TempDir::new().unwrap();
    let bounds = ChangeBounds {
        listed_files: 5,
        ..ChangeBounds::DEFAULT
    };

    let (cs, reads) =
        reads_during(|| ct::changed_since_baseline_bounded(data.path(), r.path(), "ws", bounds));
    assert_eq!(reads, 0, "no source file is read without a baseline");
    assert!(!cs.has_baseline);
    assert_eq!(cs.changed_files.as_ref().map(Vec::len), Some(5));
    assert_eq!(cs.changed_files_total, Some(12));
    let t = cs
        .truncation
        .as_ref()
        .expect("the capped listing reports truncation");
    assert_eq!(
        (
            t.files_listed,
            t.files_total,
            t.symbol_files_read,
            t.symbol_files_total
        ),
        (5, 12, 0, 12)
    );
    assert!(t.reason.contains("no index baseline"), "{}", t.reason);
    assert_eq!(cs.dirty_symbols, None);
    assert_eq!(cs.contract_breaks, None, "no baseline is not 0 breaks");
    assert_eq!(unknown_fields(&cs), ["dirty_symbols", "contract_breaks"]);

    // the default bounds still report the missing baseline as truncation, not a diff
    let cs = ct::changed_since_baseline(data.path(), r.path(), "ws");
    assert_eq!(cs.changed_files_total, Some(12));
    assert!(cs.truncation.is_some() && cs.contract_breaks.is_none());
}

// symbol_signature used to re-read the file for every symbol. A baseline build and a
// changed-since pass now read each source file once, whatever its symbol count.
#[test]
fn signature_pass_reads_each_file_once() {
    let r = repo(&[
        ("m.rs", functions(40, 1)),
        ("other.rs", functions(3, 1)),
        ("README.md", "# readme\n".to_string()),
    ]);
    let data = TempDir::new().unwrap();
    let (baseline, reads) = reads_during(|| {
        ct::rebaseline(data.path(), r.path(), "ws", BaselineReason::Registration).unwrap()
    });
    assert_eq!(reads, 2, "one read per source file");
    assert_eq!(baseline.signatures.len(), 43);

    write(r.path(), &[("m.rs", functions(40, 2))]); // every signature changes arity
    let (cs, reads) = reads_during(|| ct::working_tree_changes(data.path(), r.path(), "ws"));
    assert_eq!(reads, 1, "only the changed file is read, once");
    assert_eq!(cs.contract_breaks, Some(40), "{:?}", cs.unknown);

    commit(r.path());
    let (cs, reads) = reads_during(|| ct::changed_since_baseline(data.path(), r.path(), "ws"));
    assert_eq!(reads, 1);
    assert_eq!(
        (cs.contract_breaks, cs.dirty_symbols_total),
        (Some(40), Some(40))
    );
    assert!(cs.truncation.is_none() && cs.unknown.is_empty(), "{cs:?}");
    let first = &cs.dirty_symbols.as_ref().unwrap()[0];
    assert_eq!(first.signature_change.as_deref(), Some("params 1→2"));
}

// Past the symbol-file cap the pass is partial: modified files are read before added
// ones, and the contract-break count is unknown rather than a smaller exact number.
// The symbol cap only limits the listing: counts stay exact and breaks are listed first.
#[test]
fn symbol_caps_keep_contract_breaks_and_exact_counts() {
    let r = repo(&[("b1.rs", functions(2, 1)), ("b2.rs", functions(2, 1))]);
    let data = TempDir::new().unwrap();
    ct::build_index_baseline(data.path(), r.path(), "ws").unwrap();
    write(
        r.path(),
        &[
            ("a1.rs", functions(2, 1)),
            ("a2.rs", functions(2, 1)),
            ("a3.rs", functions(2, 1)),
            ("b1.rs", functions(2, 2)),
            ("b2.rs", functions(2, 2)),
        ],
    );
    commit(r.path());
    let bounds = ChangeBounds {
        symbol_files: 2,
        ..ChangeBounds::DEFAULT
    };
    let (cs, reads) =
        reads_during(|| ct::changed_since_baseline_bounded(data.path(), r.path(), "ws", bounds));
    assert_eq!(reads, 2);
    let t = cs
        .truncation
        .as_ref()
        .expect("a partial symbol pass is truncation");
    assert_eq!((t.symbol_files_read, t.symbol_files_total), (2, 5));
    let paths: Vec<&str> = cs
        .dirty_symbols
        .as_ref()
        .unwrap()
        .iter()
        .map(|s| s.path.as_str())
        .collect();
    assert_eq!(
        paths,
        ["b1.rs", "b1.rs", "b2.rs", "b2.rs"],
        "modified files first"
    );
    assert_eq!((cs.contract_breaks, cs.dirty_symbols_total), (None, None));
    assert_eq!(unknown_fields(&cs), ["dirty_symbols", "contract_breaks"]);
    assert_eq!(
        cs.changed_files_total,
        Some(5),
        "every changed file is still counted"
    );

    // 4 breaks (b1, b2) and 6 added symbols; a 5-symbol listing keeps every break
    let listing = ChangeBounds {
        symbols: 5,
        ..ChangeBounds::DEFAULT
    };
    let cs = ct::changed_since_baseline_bounded(data.path(), r.path(), "ws", listing);
    let listed = cs.dirty_symbols.as_ref().unwrap();
    assert_eq!(listed.len(), 5);
    assert_eq!(
        listed.iter().filter(|s| s.contract_break).count(),
        4,
        "{listed:?}"
    );
    assert_eq!(
        (cs.contract_breaks, cs.dirty_symbols_total),
        (Some(4), Some(10))
    );
    assert!(
        cs.unknown.is_empty(),
        "a listing cap leaves the counts exact: {:?}",
        cs.unknown
    );
    let t = cs
        .truncation
        .as_ref()
        .expect("a capped listing is truncation");
    assert_eq!((t.symbols_listed, t.symbols_total), (5, 10));
    let order: Vec<&str> = listed.iter().map(|s| s.path.as_str()).collect();
    assert!(
        order.windows(2).all(|w| w[0] <= w[1]),
        "changed-file order: {order:?}"
    );
}

// A worktree Git cannot list used to read as "nothing changed" (and a baseline built
// from it as an empty repository). Now its counts are unknown, drift says why, and no
// baseline is written.
#[test]
fn unlistable_worktree_is_unknown_not_zero() {
    let plain = TempDir::new().unwrap();
    fs::write(plain.path().join("a.rs"), functions(1, 1)).unwrap();
    let data = TempDir::new().unwrap();

    for cs in [
        ct::changed_since_baseline(data.path(), plain.path(), "ws"),
        ct::working_tree_changes(data.path(), plain.path(), "ws"),
    ] {
        assert_eq!(cs.changed_files, None, "{cs:?}");
        assert_eq!(cs.changed_files_total, None);
        assert_eq!(cs.contract_breaks, None);
        assert_eq!(
            unknown_fields(&cs),
            ["changed_files", "dirty_symbols", "contract_breaks"]
        );
        assert!(cs.unknown[0].reason.starts_with("git "), "{:?}", cs.unknown);
    }
    let drift = ct::detect_drift(data.path(), plain.path(), "ws");
    assert!(drift.stale && drift.error.is_some(), "{drift:?}");
    assert!(ct::rebaseline(data.path(), plain.path(), "ws", BaselineReason::FirstGround).is_err());
    assert!(
        ct::load_index_baseline(data.path(), "ws").is_none(),
        "no empty baseline"
    );
}

// The baseline records why it was built; drift reports when, whether automatically and
// why, and whether the worktree is dirty. A baseline from before reasons reads as admin.
#[test]
fn drift_reports_the_baseline_reason() {
    let r = repo(&[("a.rs", functions(1, 1))]);
    let data = TempDir::new().unwrap();
    let drift = ct::detect_drift(data.path(), r.path(), "ws");
    assert!(!drift.has_baseline && drift.baseline_reason.is_none() && drift.error.is_none());

    let b = ct::rebaseline(data.path(), r.path(), "ws", BaselineReason::FirstGround).unwrap();
    let summary = ct::BaselineSummary::from(&b);
    assert!(summary.auto && summary.head.is_some() && !summary.dirty);
    let drift = ct::detect_drift(data.path(), r.path(), "ws");
    assert!(drift.has_baseline && !drift.stale, "{drift:?}");
    assert_eq!(drift.baseline_reason, Some(BaselineReason::FirstGround));
    assert!(BaselineReason::FirstGround.auto() && !BaselineReason::Admin.auto());
    assert_eq!(
        drift.baseline_indexed_at.as_deref(),
        Some(b.indexed_at.as_str())
    );
    assert_eq!(drift.baseline_head, b.fingerprint.head_sha);

    write(r.path(), &[("a.rs", functions(1, 2))]);
    assert!(ct::detect_drift(data.path(), r.path(), "ws").dirty);

    let path = data.path().join("workspaces/ws/index_baseline.json");
    let mut legacy: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
    legacy.as_object_mut().unwrap().remove("reason");
    fs::write(&path, serde_json::to_vec(&legacy).unwrap()).unwrap();
    let drift = ct::detect_drift(data.path(), r.path(), "ws");
    assert_eq!(drift.baseline_reason, Some(BaselineReason::Admin));

    fs::write(&path, b"{not json").unwrap();
    let drift = ct::detect_drift(data.path(), r.path(), "ws");
    assert!(
        !drift.has_baseline && drift.reasons[0].contains("unreadable"),
        "{drift:?}"
    );
    for r in BaselineReason::ALL {
        assert_eq!(BaselineReason::parse(r.as_str()), Some(r));
    }
}
