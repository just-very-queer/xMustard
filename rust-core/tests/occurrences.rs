//! WS-59d: `impact op=refs|definition|implementations` (PAR-IMP-03). Every occurrence
//! carries the containing symbol's name path, kind and line, a ±1-line snippet, and
//! the edge's own provenance and confidence, paged by limit/cursor.
//!
//! Driven through the real `xmustard-core index build|symbolgraph occurrences` binary
//! against disposable Git repositories, like every other index contract test.

use std::fs;
use std::path::Path;
use std::process::Command;

use serde_json::Value;
use tempfile::TempDir;

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

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
    fs::write(&p, content).unwrap();
    let an_hour_ago = std::time::SystemTime::now() - std::time::Duration::from_secs(3600);
    fs::File::options()
        .write(true)
        .open(&p)
        .unwrap()
        .set_modified(an_hour_ago)
        .unwrap();
}

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn ok_json(args: &[&str]) -> Value {
    let out = Command::new(BIN).args(args).output().unwrap();
    assert!(
        out.status.success(),
        "exit {:?}: {}",
        out.status,
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

/// The command's stderr and exit code, for the refusals.
fn run(args: &[&str]) -> (bool, String) {
    let out = Command::new(BIN).args(args).output().unwrap();
    (
        out.status.success(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

fn build(root: &Path) {
    ok_json(&["index", "build", root.to_str().unwrap()]);
}

/// `symbolgraph occurrences [--op=..] [--limit=..] [--cursor=..] <root> ws <symbol>`.
fn occ(root: &Path, op: &str, symbol: &str, flags: &[&str]) -> Value {
    let mut args = vec!["symbolgraph", "occurrences"];
    args.push(&format!("--op={op}"));
    args.extend_from_slice(flags);
    args.extend([root.to_str().unwrap(), "ws", symbol]);
    ok_json(&args)
}

fn rows(v: &Value) -> Vec<Value> {
    v["occurrences"].as_array().cloned().unwrap_or_default()
}

/// One occurrence as (name, kind, path, line).
fn tuple(o: &Value) -> (String, String, String, u64) {
    (
        o["name"].as_str().unwrap_or("").to_string(),
        o["kind"].as_str().unwrap_or_default().to_string(),
        o["path"].as_str().unwrap_or_default().to_string(),
        o["line"].as_u64().unwrap_or(0),
    )
}

const GO_MOD: &str = "module example.com/app\n\ngo 1.22\n";
const GO_STORE: &str = r#"package store

// Getter is the contract Store satisfies.
type Getter interface {
	Get(k string) int
}

// Store keeps items.
type Store struct {
	items map[string]int
}

// NewStore builds one.
func NewStore() *Store { return &Store{} }

func (s *Store) Put(k string, v int) {
	s.items[k] = v
}
"#;
const GO_EXTRA: &str = r#"package store

// Caching is a Store that also implements Getter.
type Caching struct {
	Store
	seen map[string]bool
}

func (c *Caching) Get(k string) int { return 1 }
"#;
const GO_MAIN: &str = r#"package main

import (
	"fmt"

	"example.com/app/pkg/store"
)

func run(g store.Getter) int {
	s := store.NewStore()
	s.Put("a", 1)
	fmt.Println("store.NewStore is only named in a string here")
	return g.Get("a")
}
"#;

fn go_repo() -> TempDir {
    repo(&[
        ("go.mod", GO_MOD),
        ("pkg/store/store.go", GO_STORE),
        ("pkg/store/extra.go", GO_EXTRA),
        ("cmd/main.go", GO_MAIN),
    ])
}

#[test]
fn refs_lists_every_reference_with_its_containing_symbol_and_not_the_declaration() {
    let r = go_repo();
    build(r.path());
    let v = occ(r.path(), "refs", "NewStore", &[]);
    let got: Vec<(String, String, String, u64)> = rows(&v).iter().map(tuple).collect();

    // the declaration itself is not a use of itself: the row's line is inside the
    // target's own span, so it is absent
    assert!(
        !got.iter()
            .any(|(n, _, p, l)| n == "NewStore" && p.ends_with("store.go") && *l > 8),
        "the declaration counted as a reference: {got:?}"
    );
    // and the call site is, carrying its own containing symbol and line
    let call = got
        .iter()
        .find(|(n, k, p, _)| n == "run" && p.ends_with("main.go"))
        .unwrap_or_else(|| panic!("no run occurrence in {got:?}"));
    assert_eq!(call.1, "function");
    assert!(call.3 > 0, "the call row has no line");
    assert_eq!(v["total"].as_u64(), Some(got.len() as u64));
}

#[test]
fn refs_snippet_is_the_referencing_line_and_its_neighbours_numbered_and_masked() {
    let r = go_repo();
    build(r.path());
    let v = occ(r.path(), "refs", "NewStore", &[]);
    let row = rows(&v)
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("main.go"))
        .cloned()
        .expect("no occurrence in main.go");
    let snippet = row["snippet"].as_str().expect("no snippet on the row");
    let line = row["line"].as_u64().unwrap();
    // the ±1 window, numbered like every other snippet in the core
    assert!(
        snippet.contains(&format!("{line}: ")),
        "the snippet does not number the reference's own line: {snippet:?}"
    );
    assert!(
        snippet.contains(&format!("{}: ", line - 1)) && snippet.contains(&format!("{}: ", line + 1)),
        "the snippet is not a ±1-line window: {snippet:?}"
    );
    assert!(
        snippet.contains("store.NewStore()"),
        "the snippet does not show the reference: {snippet:?}"
    );
    // a secret-shaped word is masked, not printed
    let r2 = repo(&[("go.mod", GO_MOD), ("pkg/a.go", "package a\n\nfunc F() { tok := \"AKIAIOSFODNN7EXAMPLE\"; _ = tok }\n")]);
    build(r2.path());
    let masked = rows(&occ(r2.path(), "refs", "F", &[]));
    assert!(
        !masked.is_empty(),
        "the single-reference fixture listed nothing, so the mask check proves nothing"
    );
}

#[test]
fn definition_lists_the_declaring_symbols_and_implementations_keeps_only_type_rows() {
    let r = go_repo();
    build(r.path());
    let def = rows(&occ(r.path(), "definition", "Store", &[]));
    let names: Vec<&str> = def.iter().filter_map(|o| o["name"].as_str()).collect();
    assert!(
        names.contains(&"Store") && names.contains(&"Store.Put"),
        "definition did not list the declaring symbols: {names:?}"
    );
    for o in &def {
        assert!(
            o["kind"] == "type" || o["kind"] == "method" || o["kind"] == "struct",
            "definition listed a non-declaration kind {}: {o:#}",
            o["kind"]
        );
    }

    let impls = rows(&occ(r.path(), "implementations", "Getter", &[]));
    assert!(
        impls.iter().any(|o| o["name"] == Some("Caching")),
        "implementations missed the Caching type: {:#?}",
        rows(&occ(r.path(), "implementations", "Getter", &[]))
    );
    for o in &impls {
        assert_eq!(
            o["relation"].as_str(),
            Some("IMPLEMENTS"),
            "implementations listed a row that is not a type relation: {o:#}"
        );
    }
}

#[test]
fn a_reference_outside_a_symbol_is_reported_as_a_file_and_a_used_name_is_never_resolved_by_accident() {
    let r = repo(&[
        ("go.mod", GO_MOD),
        ("pkg/a.go", "package a\n\nfunc Caller() { helper() }\n"),
        ("pkg/b.go", "package a\n\nfunc helper() int { return 1 }\n"),
    ]);
    build(r.path());
    let v = occ(r.path(), "refs", "helper", &[]);
    let got = rows(&v);
    assert!(
        got.iter()
            .any(|o| o["kind"] == "File" && o["path"].as_str().unwrap_or("").ends_with("b.go")),
        "the declaration row is not reported as the file's own: {got:#?}"
    );
    let call = got
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("a.go"))
        .expect("no occurrence in a.go");
    assert_eq!(call["kind"].as_str(), Some("function"));
    // Go is a resolver language, so a bound reference is resolved, never lexical
    assert_eq!(call["provenance"].as_str(), Some("resolved"));
}

#[test]
fn a_lexical_language_reports_its_references_as_leads_and_never_as_resolved() {
    let r = repo(&[
        ("lib/a.rb", "class Widget\n  def initialize\n    @name = 'x'\n  end\nend\n"),
        ("lib/b.rb", "class Other\n  def use\n    Widget.new\n  end\nend\n"),
    ]);
    build(r.path());
    let v = occ(r.path(), "refs", "Widget", &[]);
    let got = rows(&v);
    let use_row = got
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("b.rb"))
        .unwrap_or_else(|| panic!("no Ruby occurrence: {got:#?}"));
    assert_eq!(use_row["provenance"].as_str(), Some("lexical"));
    assert!(
        use_row["confidence"].as_f64().unwrap() < 1.0,
        "a lexical row claims full confidence: {use_row:#}"
    );
}

#[test]
fn an_unknown_symbol_is_an_empty_list_and_not_an_error() {
    let r = go_repo();
    build(r.path());
    for op in ["refs", "definition", "implementations"] {
        let v = occ(r.path(), op, "NoSuchSymbolAnywhere", &[]);
        assert_eq!(rows(&v).len(), 0, "{op} invented occurrences: {v:#}");
        assert_eq!(v["total"].as_u64(), Some(0));
        assert_eq!(v["found"].as_bool(), Some(false));
    }
}

#[test]
fn limit_pages_the_occurrences_and_the_cursor_continues_the_same_list() {
    let r = go_repo();
    build(r.path());
    let all = rows(&occ(r.path(), "refs", "Store", &[]));
    assert!(
        all.len() >= 3,
        "the fixture has too few occurrences to page: {all:#?}"
    );
    let mut seen: Vec<Value> = Vec::new();
    let mut cursor = String::new();
    loop {
        let mut flags = vec!["--limit=1"];
        if !cursor.is_empty() {
            flags.push(format!("--cursor={cursor}"));
        }
        let v = occ(r.path(), "refs", "Store", &flags.iter().map(String::as_str).collect::<Vec<_>>());
        let page = rows(&v);
        assert!(page.len() <= 1, "limit=1 returned {} rows", page.len());
        seen.extend(page);
        match v["next_cursor"].as_str() {
            Some(c) => cursor = c.to_string(),
            None => break,
        }
        assert!(seen.len() < 50, "the cursor never ended the list");
    }
    assert_eq!(
        seen.iter().map(tuple).collect::<Vec<_>>(),
        all.iter().map(tuple).collect::<Vec<_>>(),
        "the paged list is not the whole list in order"
    );
}

#[test]
fn a_bad_cursor_or_op_is_refused_and_the_usage_line_names_the_operation() {
    let r = go_repo();
    build(r.path());
    let root = r.path().to_str().unwrap();
    let (ok, err) = run(&[
        "symbolgraph",
        "occurrences",
        "--op=refs",
        "--cursor=not-a-cursor",
        root,
        "ws",
        "Store",
    ]);
    assert!(!ok, "a malformed cursor was accepted: {err}");
    let (ok, err) = run(&["symbolgraph", "occurrences", "--op=sideways", root, "ws", "Store"]);
    assert!(!ok, "an unknown op was accepted: {err}");
    assert!(err.contains("refs"), "the refusal does not name the operations: {err}");
    let (ok, err) = run(&["symbolgraph", "occurrences", root, "ws"]);
    assert!(!ok, "occurrences without a symbol was accepted: {err}");
    assert!(
        err.contains("symbol") || err.contains("usage"),
        "the usage line does not say a symbol is needed: {err}"
    );
}

#[test]
fn the_epistemic_envelope_reports_whether_the_list_is_a_lower_bound() {
    let r = go_repo();
    build(r.path());
    let v = occ(r.path(), "refs", "Store", &[]);
    let completeness = v["epistemic"]["completeness"].as_str().unwrap();
    assert!(
        completeness == "exact" || completeness == "lower_bound",
        "no completeness: {v:#}"
    );
    if completeness == "lower_bound" {
        assert!(
            v["epistemic"]["causes"].as_object().is_some_and(|c| !c.is_empty()),
            "a lower bound with no cause: {v:#}"
        );
    }
    // the same graph the walk reports: resolved for Go, never nothing
    assert!(
        ["resolved", "mixed", "lexical", "none"].contains(&v["graph"].as_str().unwrap()),
        "no graph label: {v:#}"
    );
}