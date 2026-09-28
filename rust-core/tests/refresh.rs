//! WS-15: the watcher, its serialized refresh queue and the freshness loop, driven the
//! way the orchestrator drives them inside `serve` (take a due batch, run `index update
//! --paths ...`, report it done), against disposable Git repositories and the platform's
//! native watcher (inotify on Linux, FSEvents on macOS).
//!
//! The watch registry, the resident snapshots and the Git spawn counter are
//! process-wide, so the tests take one lock and run one at a time.

use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::mpsc::{self, Receiver};
use std::sync::{Barrier, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant};

use serde_json::Value;
use tempfile::TempDir;
use xmustard_core::changetrack::{self as ct, BaselineReason};
use xmustard_core::index::config::IndexConfig;
use xmustard_core::index::refresh_queue::{FullReason, MAX_PENDING_PATHS};
use xmustard_core::index::watch::{self, WatcherState};
use xmustard_core::index::{self, reader};
use xmustard_core::indexcache;

static SERIAL: Mutex<()> = Mutex::new(());

type Notes = Mutex<Receiver<(String, Value)>>;
static NOTES: OnceLock<Notes> = OnceLock::new();

/// One test at a time, with watching enabled and its notifications collected.
fn serial() -> MutexGuard<'static, ()> {
    let guard = SERIAL.lock().unwrap_or_else(|e| e.into_inner());
    NOTES.get_or_init(|| {
        let (tx, rx) = mpsc::channel();
        watch::enable(move |method, params| {
            let _ = tx.send((method.to_string(), params));
        });
        reader::enable_resident(4);
        Mutex::new(rx)
    });
    drain_notes();
    guard
}

fn drain_notes() {
    let rx = NOTES.get().unwrap().lock().unwrap();
    while rx.try_recv().is_ok() {}
}

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

fn repo(files: &[(String, String)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
    }
    for args in [
        &["init", "-q"][..],
        &["config", "user.email", "t@t"],
        &["config", "user.name", "t"],
        &["add", "-A"],
        &["commit", "-qm", "c"],
    ] {
        git(dir.path(), args);
    }
    dir
}

fn ts_files(n: usize) -> Vec<(String, String)> {
    (0..n)
        .map(|i| {
            (
                format!("pkg/m{i:03}.ts"),
                format!("import {{ shared }} from './shared';\nexport function fn{i:03}(): number {{\n  return shared() + {i};\n}}\n"),
            )
        })
        .chain([(
            "pkg/shared.ts".to_string(),
            "export function shared(): number { return 1; }\n".to_string(),
        )])
        .collect()
}

fn canon(p: &Path) -> PathBuf {
    fs::canonicalize(p).unwrap()
}

fn cfg() -> IndexConfig {
    IndexConfig::default()
}

/// Wait for `root`'s next `$/refresh.due` notification.
fn wait_due(root: &Path, within: Duration) -> Value {
    let want = canon(root).to_string_lossy().into_owned();
    let rx = NOTES.get().unwrap().lock().unwrap();
    let deadline = Instant::now() + within;
    loop {
        let left = deadline.saturating_duration_since(Instant::now());
        match rx.recv_timeout(left) {
            Ok((m, p)) if m == "$/refresh.due" && p["root"] == want.as_str() => return p,
            Ok(_) => continue,
            Err(_) => panic!("no $/refresh.due for {want} within {within:?}"),
        }
    }
}

fn wait_state(root: &Path, want: WatcherState, within: Duration) {
    let deadline = Instant::now() + within;
    while watch::state_of(root) != want {
        assert!(
            Instant::now() < deadline,
            "watcher state {:?}, wanted {want:?}: {}",
            watch::state_of(root),
            watch::registry().status(Some(root))
        );
        std::thread::sleep(Duration::from_millis(10));
    }
}

/// The orchestrator's side of one refresh: take the batch (due, or forced), run the
/// update for its paths (the whole tree for a full or unsynced batch), report it.
/// Returns the batch and the update's report.
fn refresh(root: &Path, force: bool) -> (Value, Value) {
    let taken = watch::registry().take(root, force).unwrap();
    let batch = taken.batch.clone().expect("a batch");
    let whole = batch.full.is_some() || !taken.synced;
    let paths = (!whole).then_some(batch.paths.clone());
    let report = index::update(root, &cfg(), paths.as_deref()).unwrap();
    let report = serde_json::to_value(report).unwrap();
    watch::registry()
        .done(root, batch.id, true, Some(report.clone()))
        .unwrap();
    (serde_json::to_value(&taken).unwrap(), report)
}

/// Start watching `root` and run its start-up refresh.
fn watched(root: &Path) {
    watch::registry().start(root).unwrap();
    wait_due(root, Duration::from_secs(20));
    let (taken, _) = refresh(root, false);
    assert_eq!(taken["batch"]["full"], "startup", "{taken}");
    wait_state(root, WatcherState::Ok, Duration::from_secs(5));
}

struct Unwatch<'a>(&'a Path);

impl Drop for Unwatch<'_> {
    fn drop(&mut self) {
        watch::registry().stop(self.0);
    }
}

fn digest_of_full_build(root: &Path) -> String {
    let other = TempDir::new().unwrap();
    let mut c = cfg();
    c.index_dir = Some(other.path().join("idx"));
    index::build(root, &c).unwrap();
    index::stats(root, &c, true).unwrap()["content_digest"]
        .as_str()
        .unwrap()
        .to_string()
}

fn digest(root: &Path) -> String {
    index::stats(root, &cfg(), true).unwrap()["content_digest"]
        .as_str()
        .unwrap()
        .to_string()
}

// Edit, rename, delete and create reach the index through the watcher's batch, and the
// refreshed index equals a full build of the same tree.
#[test]
fn edits_renames_deletes_and_creates_refresh_to_a_full_build() {
    let _g = serial();
    let r = repo(&ts_files(8));
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());

    write(
        r.path(),
        "pkg/m001.ts",
        "export function fn001(): number {\n  return 42;\n}\n",
    );
    fs::rename(r.path().join("pkg/m002.ts"), r.path().join("pkg/moved.ts")).unwrap();
    fs::remove_file(r.path().join("pkg/m003.ts")).unwrap();
    write(
        r.path(),
        "pkg/fresh.ts",
        "export function freshOne(): number { return 7; }\n",
    );
    git(r.path(), &["add", "-A"]);
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, report) = refresh(r.path(), false);
    let paths: BTreeSet<&str> = taken["batch"]["paths"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p.as_str().unwrap())
        .collect();
    for p in [
        "pkg/m001.ts",
        "pkg/m002.ts",
        "pkg/moved.ts",
        "pkg/m003.ts",
        "pkg/fresh.ts",
    ] {
        assert!(paths.contains(p), "{p} missing from {paths:?}");
    }
    assert_eq!(taken["synced"], true);
    assert_eq!(taken["batch"]["git"], true, "git add moved the Git index");
    assert_eq!(report["mode"], "incremental", "{report}");
    assert_eq!(digest(r.path()), digest_of_full_build(r.path()));

    // the envelope reports the watcher and the refresh's counters
    let opened = reader::open(r.path()).unwrap();
    let f = opened.freshness(std::iter::empty());
    assert_eq!(f.watcher_state, "ok");
    let counters = f.refresh.expect("refresh counters");
    // the rename's facts come from the content-addressed cache: two files parsed
    assert!(
        counters.reparsed >= 2 && counters.reresolved >= 3,
        "{counters:?}"
    );
    assert!(!counters.escalated);
    let status = watch::registry().status(Some(r.path()));
    assert_eq!(status["last_refresh"]["mode"], "incremental", "{status}");
}

// A burst is one batch; an ignore file, too many paths and an injected watch limit
// each refresh the whole tree.
#[test]
fn bursts_coalesce_and_full_refreshes_have_their_reasons() {
    let _g = serial();
    let r = repo(&ts_files(4));
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());

    let t0 = Instant::now();
    for i in 0..20 {
        write(r.path(), "pkg/m001.ts", &format!("export const v = {i};\n"));
        std::thread::sleep(Duration::from_millis(20));
    }
    write(r.path(), "pkg/m002.ts", "export const w = 1;\n");
    wait_due(r.path(), Duration::from_secs(5));
    assert!(
        t0.elapsed() >= Duration::from_millis(300),
        "due before the quiet period"
    );
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(
        taken["batch"]["paths"],
        serde_json::json!(["pkg/m001.ts", "pkg/m002.ts"])
    );
    let q = &watch::registry().status(Some(r.path()))["queue"];
    assert!(q["coalesced"].as_u64().unwrap() >= 19, "{q}");

    write(r.path(), ".gitignore", "*.log\n");
    wait_due(r.path(), Duration::from_secs(5));
    assert_eq!(watch::state_of(r.path()), WatcherState::Overflow);
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(taken["batch"]["full"], "ignore_file");
    wait_state(r.path(), WatcherState::Ok, Duration::from_secs(10));

    for i in 0..=MAX_PENDING_PATHS {
        write(
            r.path(),
            &format!("gen/f{i:04}.ts"),
            "export const x = 1;\n",
        );
    }
    wait_due(r.path(), Duration::from_secs(10));
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(taken["batch"]["full"], "overflow", "{taken}");
    wait_state(r.path(), WatcherState::Ok, Duration::from_secs(10));

    // an OS watch limit (inotify ENOSPC) drops the native watcher: degraded, a full
    // refresh queued, never silently stale
    assert!(watch::registry().inject(
        r.path(),
        Err(notify::Error::new(notify::ErrorKind::MaxFilesWatch))
    ));
    wait_state(r.path(), WatcherState::Degraded, Duration::from_secs(5));
    let status = watch::registry().status(Some(r.path()));
    assert!(
        status["detail"].as_str().unwrap().contains("watch limit"),
        "{status}"
    );
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(taken["batch"]["full"], "watch_limit");
    assert_eq!(taken["synced"], false, "no native watcher to sync with");
    assert_eq!(watch::state_of(r.path()), WatcherState::Degraded);
    let _ = FullReason::Verify; // periodic verification is covered by the queue's tests
}

// inotify: more directories than the per-root limit is the same `degraded` state as
// the kernel's ENOSPC.
#[test]
#[cfg(target_os = "linux")]
fn more_directories_than_the_watch_limit_degrade() {
    let _g = serial();
    let files: Vec<(String, String)> = (0..6)
        .map(|i| (format!("d{i}/a.ts"), "export const a = 1;\n".to_string()))
        .collect();
    let r = repo(&files);
    let _u = Unwatch(r.path());
    watch::registry().set_max_dirs(3);
    let started = watch::registry().start(r.path());
    watch::registry().set_max_dirs(watch::MAX_WATCH_DIRS);
    started.unwrap();
    wait_state(r.path(), WatcherState::Degraded, Duration::from_secs(10));
    let status = watch::registry().status(Some(r.path()));
    assert!(
        status["detail"].as_str().unwrap().contains("directories"),
        "{status}"
    );
    let due = wait_due(r.path(), Duration::from_secs(5));
    assert_eq!(due["watcher_state"], "degraded");
}

// Concurrent starts for one root register one watcher: the worker runs calls on
// several threads, and parallel first reads (or ground beside a read) all ask to watch.
#[test]
fn concurrent_starts_register_one_root() {
    let _g = serial();
    let r = repo(&ts_files(2));
    let _u = Unwatch(r.path());
    let barrier = Barrier::new(8);
    std::thread::scope(|s| {
        for _ in 0..8 {
            s.spawn(|| {
                barrier.wait();
                watch::registry().start(r.path()).unwrap();
            });
        }
    });
    let want = canon(r.path()).to_string_lossy().into_owned();
    let all = watch::registry().status(None);
    let registered = all["roots"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|s| s["root"] == want.as_str())
        .count();
    assert_eq!(registered, 1, "{all}");
}

fn batch_paths(taken: &Value) -> BTreeSet<String> {
    taken["batch"]["paths"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p.as_str().unwrap().to_string())
        .collect()
}

// A directory Git ignored as a whole when the watcher started holds a tracked file
// after `git add -f`: the Git index change makes the watcher cover it, so the file is
// refreshed and its later edits reach the batches.
#[test]
fn a_force_added_file_in_an_ignored_directory_is_watched() {
    let _g = serial();
    let mut files = ts_files(2);
    files.push((".gitignore".into(), "build/\n".into()));
    let r = repo(&files);
    write(r.path(), "build/gen.ts", "export const gen = 1;\n");
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());

    git(r.path(), &["add", "-f", "build/gen.ts"]);
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, _) = refresh(r.path(), false);
    assert!(batch_paths(&taken).contains("build/gen.ts"), "{taken}");

    write(r.path(), "build/gen.ts", "export const gen = 2;\n");
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(
        batch_paths(&taken),
        BTreeSet::from(["build/gen.ts".to_string()])
    );
    assert_eq!(digest(r.path()), digest_of_full_build(r.path()));
}

// inotify: the directory watch count follows the tree. notify answers Ok for a
// directory it already watches (`mkdir -p` reports each level), and it drops the
// watches of a directory renamed or removed; neither may move the count.
#[test]
#[cfg(target_os = "linux")]
fn the_directory_watch_count_follows_the_tree() {
    let _g = serial();
    let r = repo(&ts_files(2));
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());
    let dirs = || {
        watch::registry().status(Some(r.path()))["counters"]["dirs_watched"]
            .as_u64()
            .unwrap()
    };
    let wait_dirs = |want: u64| {
        let deadline = Instant::now() + Duration::from_secs(5);
        while dirs() != want {
            assert!(
                Instant::now() < deadline,
                "dirs_watched {}, wanted {want}: {}",
                dirs(),
                watch::registry().status(Some(r.path()))
            );
            std::thread::sleep(Duration::from_millis(20));
        }
    };
    let base = dirs();
    for _ in 0..3 {
        fs::create_dir_all(r.path().join("a/b/c")).unwrap();
        write(r.path(), "a/b/c/x.ts", "export const x = 1;\n");
        wait_dirs(base + 3);
        fs::rename(r.path().join("a"), r.path().join("moved")).unwrap();
        wait_dirs(base + 3);
        fs::remove_dir_all(r.path().join("moved")).unwrap();
        wait_dirs(base);
    }
    // settle: the count stays put once the events are in
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(dirs(), base);
}

// A crash mid-update (the dirty flag left set) turns the next refresh into a full
// rebuild; a write set over half the files (and at least 50) escalates. The counters
// reach the envelope.
#[test]
fn crash_and_escalation_through_the_refresh_loop() {
    let _g = serial();
    let r = repo(&ts_files(99));
    let built = index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());

    let conn = rusqlite::Connection::open(&built.index_path).unwrap();
    index::meta::set(&conn, index::meta::DIRTY_FLAG, "1").unwrap();
    drop(conn);
    write(r.path(), "pkg/m001.ts", "export const crashed = 1;\n");
    wait_due(r.path(), Duration::from_secs(5));
    let (_, report) = refresh(r.path(), false);
    assert_eq!(
        (report["mode"].as_str(), report["reason"].as_str()),
        (Some("full"), Some("dirty_flag"))
    );

    for i in 0..60 {
        write(
            r.path(),
            &format!("pkg/m{i:03}.ts"),
            &format!("export const e{i} = {i};\n"),
        );
    }
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, report) = refresh(r.path(), false);
    assert!(taken["batch"]["full"].is_null(), "{taken}");
    assert_eq!(report["reason"], "escalated", "{report}");
    assert_eq!(report["counters"]["escalated"], true);
    let f = reader::open(r.path())
        .unwrap()
        .freshness(std::iter::empty());
    assert!(f.refresh.unwrap().escalated);
    assert_eq!(
        watch::registry().status(Some(r.path()))["last_refresh"]["escalated"],
        true
    );
}

// While the watcher is ok, queries reuse the commit relation past its TTL: no Git
// spawn per query. A HEAD move is seen and the next query relates again.
#[test]
fn queries_spawn_no_git_until_head_moves() {
    let _g = serial();
    let r = repo(&ts_files(4));
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    watched(r.path());
    let root = canon(r.path());

    let first = reader::open(&root).unwrap();
    let head0 = first.relation.head.clone();
    std::thread::sleep(index::envelope::TTL + Duration::from_millis(200));
    let before = indexcache::git_spawns();
    for _ in 0..20 {
        let o = reader::open(&root).unwrap();
        assert_eq!(o.freshness(["pkg/m001.ts"]).watcher_state, "ok");
    }
    assert_eq!(
        indexcache::git_spawns(),
        before,
        "a query spawned Git while the watcher vouched"
    );

    // an edit is no HEAD move: its refresh runs Git (the index scan), queries do not
    write(r.path(), "pkg/m001.ts", "export const edited = 1;\n");
    wait_due(r.path(), Duration::from_secs(5));
    refresh(r.path(), false);
    let before = indexcache::git_spawns();
    reader::open(&root).unwrap();
    assert_eq!(indexcache::git_spawns(), before);

    // a HEAD move is seen: the refresh's snapshot swap relates the new commit
    git(r.path(), &["commit", "-qam", "move HEAD"]);
    wait_due(r.path(), Duration::from_secs(5));
    let (taken, _) = refresh(r.path(), false);
    assert_eq!(taken["batch"]["git"], true);
    let o = reader::open(&root).unwrap();
    assert_ne!(
        o.relation.head, head0,
        "the HEAD move was not related again"
    );
    assert_eq!(o.relation.indexed_commit, o.relation.head);
    assert_eq!(o.relation.status, index::envelope::Status::Current);
    let before = indexcache::git_spawns();
    reader::open(&root).unwrap();
    assert_eq!(indexcache::git_spawns(), before);
}

// ground on an unchanged tree hashes no file: the identity's stat cache vouches for
// the dirty and untracked files, and under the watcher nothing is observed at all.
#[test]
fn ground_on_an_unchanged_tree_hashes_nothing() {
    let _g = serial();
    let r = repo(&ts_files(3));
    let data = TempDir::new().unwrap();
    ct::rebaseline(data.path(), r.path(), "ws", BaselineReason::FirstGround).unwrap();
    write(
        r.path(),
        "pkg/m001.ts",
        "export function fn001(a: string): number { return 1; }\n",
    );
    write(r.path(), "notes.txt", "untracked\n");
    // out of the racy window: the next pass records entries a later pass trusts
    std::thread::sleep(Duration::from_millis(2300));
    let warm = ct::detect_drift(data.path(), r.path(), "ws");
    assert!(warm.content_changed && warm.dirty, "{warm:?}");
    assert_eq!(warm.drift_checked.mode, "identity");
    let again = ct::detect_drift(data.path(), r.path(), "ws");
    assert_eq!(again.drift_checked.mode, "identity");
    assert_eq!(again.drift_checked.hashed, 0, "{:?}", again.drift_checked);
    assert_eq!(
        again.drift_checked.stat_skipped, 2,
        "the dirty and the untracked file"
    );
    assert!(again.content_changed);

    // a clean tree at the baseline's HEAD compares nothing and hashes nothing
    git(r.path(), &["checkout", "-q", "--", "pkg/m001.ts"]);
    fs::remove_file(r.path().join("notes.txt")).unwrap();
    let clean = ct::detect_drift(data.path(), r.path(), "ws");
    assert!(!clean.stale, "{clean:?}");
    assert_eq!(
        (clean.drift_checked.hashed, clean.drift_checked.compared),
        (0, 0)
    );

    // under the watcher the observation itself is reused: no Git, nothing read
    let _u = Unwatch(r.path());
    watched(r.path());
    ct::detect_drift(data.path(), r.path(), "ws");
    let before = indexcache::git_spawns();
    let ground = ct::detect_drift(data.path(), r.path(), "ws");
    let changes = ct::working_tree_changes(data.path(), r.path(), "ws");
    assert_eq!(ground.drift_checked.mode, "watcher", "{ground:?}");
    assert_eq!(ground.drift_checked.hashed, 0);
    assert_eq!(changes.changed_files_total, Some(0));
    assert_eq!(
        indexcache::git_spawns(),
        before,
        "ground spawned Git under the watcher"
    );

    // and an edit is seen at once
    write(r.path(), "pkg/m002.ts", "export const changed = 2;\n");
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        let d = ct::detect_drift(data.path(), r.path(), "ws");
        if d.content_changed {
            assert_ne!(d.drift_checked.mode, "watcher");
            break;
        }
        assert!(Instant::now() < deadline, "the edit was never seen: {d:?}");
        std::thread::sleep(Duration::from_millis(20));
    }
}

/// A 5,000-file tree like WS-07's scale fixture (Go, Rust, TS, JS, TSX).
fn synthetic_file(i: usize) -> (String, String) {
    let dir = format!("mod{:02}", i % 50);
    match i % 5 {
        0 => (
            format!("{dir}/svc{i}.go"),
            format!(
                "package mod{:02}\n\ntype Service{i} struct {{\n\tName string\n}}\n\nfunc (s *Service{i}) Handle{i}(in string) string {{\n\treturn s.Name + in\n}}\n",
                i % 50
            ),
        ),
        1 => (
            format!("{dir}/lib{i}.rs"),
            format!(
                "pub struct Store{i} {{ total: u64 }}\n\nimpl Store{i} {{\n    pub fn add(&mut self, v: u64) -> u64 {{\n        self.total += v;\n        self.total\n    }}\n}}\n"
            ),
        ),
        2 => (
            format!("{dir}/widget{i}.ts"),
            format!(
                "import {{ helper{p} }} from './util{p}';\n\nexport class Widget{i} {{\n  render(): number {{\n    return helper{p}({i});\n  }}\n}}\n",
                p = i + 1
            ),
        ),
        3 => (
            format!("{dir}/util{i}.js"),
            format!("export function helper{i}(value) {{\n  return value * 2 + {i};\n}}\n"),
        ),
        _ => (
            format!("{dir}/view{i}.tsx"),
            format!(
                "export function View{i}(props: {{ title: string }}) {{\n  return <b>{{props.title}}</b>;\n}}\n"
            ),
        ),
    }
}

// Acceptance: an edit is in the resident index within 1 s on a 5,000-file tree (the
// watcher's debounce, the batch take and the incremental update with its snapshot
// swap). Run with `--release -- --ignored`; prints the samples.
#[test]
#[ignore]
fn fresh_within_a_second_on_5000_files() {
    let _g = serial();
    let files: Vec<(String, String)> = (0..5000).map(synthetic_file).collect();
    let r = repo(&files);
    index::build(r.path(), &cfg()).unwrap();
    let _u = Unwatch(r.path());
    let t = Instant::now();
    watched(r.path());
    println!(
        "watch start + start-up refresh: {} ms",
        t.elapsed().as_millis()
    );
    println!("status: {}", watch::registry().status(Some(r.path())));
    let root = canon(r.path());
    let mut samples = Vec::new();
    for n in 0..10 {
        let generation = reader::open(&root).unwrap().snapshot.generation;
        let rel = format!("mod{:02}/util{}.js", (3 + 5 * n) % 50, 3 + 5 * n);
        let body = format!(
            "export function helper{}(value) {{\n  return value + {n};\n}}\nexport function added{n}() {{ return {n}; }}\n",
            3 + 5 * n
        );
        let t0 = Instant::now();
        write(r.path(), &rel, &body);
        wait_due(r.path(), Duration::from_secs(5));
        let (_, report) = refresh(r.path(), false);
        let o = reader::open(&root).unwrap();
        let fresh = t0.elapsed();
        assert!(o.snapshot.generation > generation);
        assert_eq!(report["counters"]["reparsed"], 1, "{report}");
        println!(
            "edit {n}: fresh after {} ms (update {}, reresolved {})",
            fresh.as_millis(),
            report["timing"],
            report["counters"]["reresolved"]
        );
        samples.push(fresh);
        std::thread::sleep(Duration::from_millis(200));
    }
    samples.sort();
    println!(
        "p50 {} ms, max {} ms",
        samples[5].as_millis(),
        samples[9].as_millis()
    );
    assert!(samples[9] <= Duration::from_secs(1), "{samples:?}");
}
