//! WS-59d: `impact op=refs|definition|implementations` (PAR-IMP-03). Every occurrence
//! carries the containing symbol's name path, kind and line, a ±1-line snippet, and
//! the edge's own provenance and confidence, paged by limit/cursor.
//!
//! Driven through the real `xmustard-core index build|symbolgraph occurrences` binary
//! against disposable Git repositories, like every other index contract test.

use std::fs;
use std::path::Path;
use std::process::Command;

use rusqlite::{Connection, OpenFlags};
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
fn occ(root: &Path, op: &str, symbol: &str, flags: &[String]) -> Value {
    let mut args = vec!["symbolgraph".to_string(), "occurrences".to_string()];
    args.push(format!("--op={op}"));
    args.extend_from_slice(flags);
    args.push(root.to_str().unwrap().to_string());
    args.push("ws".to_string());
    args.push(symbol.to_string());
    ok_json(&args.iter().map(String::as_str).collect::<Vec<_>>())
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

func (s *Store) Get(k string) int { return len(s.items) }

// Caching is a Store that also answers Getter's method itself.
type Caching struct {
	Store
	seen map[string]bool
}
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

    // the index records a reference row only for a use, so the declaration in store.go
    // is not a row at all: the whole answer is the one call site
    assert_eq!(
        got.len(),
        1,
        "refs listed something other than the single use: {got:?}"
    );
    assert!(
        !got.iter().any(|(n, _, _, _)| n == "NewStore"),
        "the declaration counted as a reference: {got:?}"
    );
    // and the call site carries its own containing symbol and line
    let call = got
        .iter()
        .find(|(n, _k, p, _)| n == "run" && p.ends_with("main.go"))
        .unwrap_or_else(|| panic!("no run occurrence in {got:?}"));
    assert_eq!(call.1, "Function");
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
        snippet.contains(&format!("{}: ", line - 1))
            && snippet.contains(&format!("{}: ", line + 1)),
        "the snippet is not a ±1-line window: {snippet:?}"
    );
    assert!(
        snippet.contains("store.NewStore()"),
        "the snippet does not show the reference: {snippet:?}"
    );
    // a secret-shaped word on a referencing line is masked, not printed: the same
    // `chunks::mask_secret_words` every other snippet in the core goes through
    let r2 = repo(&[
        ("go.mod", GO_MOD),
        ("pkg/a.go", "package a\n\nfunc F() int { return 1 }\n"),
        (
            "pkg/b.go",
            "package a\n\nfunc G() int { tok := \"AKIAIOSFODNN7EXAMPLE\"; _ = tok; return F() }\n",
        ),
    ]);
    build(r2.path());
    let masked = rows(&occ(r2.path(), "refs", "F", &[]));
    let row = masked
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("b.go"))
        .unwrap_or_else(|| {
            panic!("no occurrence in b.go, so the mask check proves nothing: {masked:#?}")
        });
    let snippet = row["snippet"].as_str().expect("no snippet on the row");
    assert!(
        snippet.contains("F()"),
        "the snippet does not show the reference: {snippet:?}"
    );
    assert!(
        !snippet.contains("AKIAIOSFODNN7EXAMPLE"),
        "a secret-shaped word was printed in a snippet: {snippet:?}"
    );
}

#[test]
fn definition_lists_the_symbols_that_declare_the_name_and_no_others() {
    let r = go_repo();
    build(r.path());
    let def = rows(&occ(r.path(), "definition", "Store", &[]));
    let names: Vec<&str> = def.iter().filter_map(|o| o["name"].as_str()).collect();
    assert_eq!(
        names,
        vec!["Store"],
        "definition lists the symbols that declare the name itself, not its members"
    );
    assert!(
        def.iter().all(|o| o["provenance"] == "name_match"),
        "a declaration is not a bound occurrence either: {def:#?}"
    );
    for o in &def {
        assert_eq!(
            o["kind"].as_str(),
            Some("Struct"),
            "definition listed a kind that is not the declaration: {o:#}"
        );
    }
    // and a member is not a declaration of the type
    assert!(
        !def.iter().any(|o| o["name"] == "Store.Put"),
        "a member counted as a declaration of the type: {def:#?}"
    );
}

#[test]
fn implementations_keeps_only_the_type_relations_and_names_the_implementing_type() {
    // Go has no `implements` clause: a struct satisfies an interface by having the
    // methods, which is not a reference to the interface's name. TypeScript writes the
    // relation down, so it is the language that can produce these rows at all.
    let r = repo(&[
        (
            "web/base.ts",
            "export interface Getter { get(k: string): number }\n\
             export interface Sealed { seal(): void }\n",
        ),
        (
            "web/caching.ts",
            "import { Getter } from './base';\n\
             \n\
             export class Caching implements Getter {\n\
             \tget(k: string): number { return 1 }\n\
             }\n",
        ),
        (
            "web/plain.ts",
            "export class Plain {\n\
             \tget(k: string): number { return 2 }\n\
             }\n",
        ),
        (
            "web/uses.ts",
            "import { Getter } from './base';\n\
             \n\
             export function take(g: Getter): number { return g.get('a'); }\n",
        ),
    ]);
    build(r.path());

    let impls = rows(&occ(r.path(), "implementations", "Getter", &[]));
    assert_eq!(
        impls.len(),
        1,
        "implementations listed more than the one implements clause: {impls:#?}"
    );
    let row = &impls[0];
    assert_eq!(row["name"].as_str(), Some("Caching"));
    assert_eq!(row["relation"].as_str(), Some("IMPLEMENTS"));
    assert_eq!(
        row["path"].as_str(),
        Some("web/caching.ts"),
        "the row is not at the implements clause"
    );
    // `Plain` has the method without claiming the interface, and `take` only names
    // Getter as a parameter type: neither is an implementation
    assert!(
        !impls.iter().any(|o| o["name"] == "Plain"),
        "a class that merely has the method was listed: {impls:#?}"
    );
    assert!(
        rows(&occ(r.path(), "implementations", "Sealed", &[])).is_empty(),
        "an interface no type implements listed rows"
    );
}

#[test]
fn a_reference_outside_a_symbol_is_reported_as_a_file_and_a_used_name_is_never_resolved_by_accident()
 {
    let r = repo(&[
        ("go.mod", GO_MOD),
        (
            "pkg/a.go",
            "package a\n\nvar atFileScope = helper()\n\nfunc Caller() int { return helper() }\n",
        ),
        ("pkg/b.go", "package a\n\nfunc helper() int { return 1 }\n"),
    ]);
    build(r.path());
    let v = occ(r.path(), "refs", "helper", &[]);
    let got = rows(&v);
    // a package-level initializer names the function with no enclosing symbol, so the
    // row carries the file rather than a containing symbol
    let file_scope = got
        .iter()
        .find(|o| o["line"] == 3 && o["path"].as_str().unwrap_or("").ends_with("a.go"))
        .unwrap_or_else(|| panic!("no file-scope row: {got:#?}"));
    assert_eq!(file_scope["kind"].as_str(), Some("File"));
    assert_eq!(file_scope["name"].as_str(), Some("pkg/a.go"));
    let call = got
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("a.go") && o["line"] == 5)
        .expect("no occurrence inside Caller");
    assert_eq!(call["kind"].as_str(), Some("Function"));
    // Go is a resolver language, so the row is a same-language name match: the
    // reference table carries no binding from a reference to the declaration it uses,
    // so nothing here is a resolved edge (adjudication-WS-59d-r4-3)
    assert_eq!(call["provenance"].as_str(), Some("name_match"));
    // and b.go holds no row at all: a declaration is not a use of itself
    assert!(
        !got.iter()
            .any(|o| o["path"].as_str().unwrap_or("").ends_with("b.go")),
        "the declaration counted as a reference: {got:#?}"
    );
}

#[test]
fn a_lexical_language_reports_its_references_as_leads_and_never_as_resolved() {
    let r = repo(&[
        (
            "lib/a.rb",
            "class Widget\n  def initialize\n    @name = 'x'\n  end\nend\n",
        ),
        (
            "lib/b.rb",
            "class Other\n  def use\n    Widget.new\n  end\nend\n",
        ),
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
        let mut flags = vec!["--limit=1".to_string()];
        if !cursor.is_empty() {
            flags.push(format!("--cursor={cursor}"));
        }
        let v = occ(r.path(), "refs", "Store", &flags);
        let page = rows(&v);
        assert!(page.len() <= 1, "limit=1 returned {} rows", page.len());
        seen.extend(page);
        let previous = cursor.clone();
        match v["next_cursor"].as_str() {
            Some(c) => cursor = c.to_string(),
            None => break,
        }
        // the cursor advances: a next_cursor equal to the one just used would page the
        // same row forever
        assert_ne!(
            cursor, previous,
            "the cursor did not advance past {previous}"
        );
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
    let (ok, err) = run(&[
        "symbolgraph",
        "occurrences",
        "--op=sideways",
        root,
        "ws",
        "Store",
    ]);
    assert!(!ok, "an unknown op was accepted: {err}");
    assert!(
        err.contains("refs"),
        "the refusal does not name the operations: {err}"
    );
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
            v["epistemic"]["causes"]
                .as_object()
                .is_some_and(|c| !c.is_empty()),
            "a lower bound with no cause: {v:#}"
        );
    }
    // the same vocabulary the blast form reports
    assert!(
        ["resolved", "mixed", "lexical", "none"].contains(&v["graph"].as_str().unwrap()),
        "no graph label: {v:#}"
    );
}

// The reference table carries no binding from a reference to the declaration it uses,
// so a Go list is a list of name matches and must never be reported as a resolved one
// or as an exact one: an envelope that read exact over namesake uses would tell a caller
// the list is complete when it is a lower bound (adjudication-WS-59d-r4-3).
#[test]
fn a_same_language_name_match_is_never_reported_as_a_resolved_or_exact_list() {
    let r = go_repo();
    build(r.path());
    let v = occ(r.path(), "refs", "Store", &[]);
    let rows = rows(&v);
    assert!(!rows.is_empty(), "the fixture has no occurrences: {v:#}");
    for o in &rows {
        assert_eq!(
            o["provenance"].as_str(),
            Some("name_match"),
            "a Go occurrence claims a bound edge: {o:#}"
        );
        assert!(
            o["confidence"].as_f64().unwrap() < 1.0,
            "a name match claims full confidence: {o:#}"
        );
    }
    assert_eq!(
        v["graph"].as_str(),
        Some("lexical"),
        "a list of name matches is not a resolved list: {v:#}"
    );
    assert_eq!(
        v["epistemic"]["completeness"].as_str(),
        Some("lower_bound"),
        "a list of name matches is not complete: {v:#}"
    );
    assert!(
        v["epistemic"]["causes"]["name_match_occurrences"].as_u64().unwrap_or(0) > 0,
        "the lower bound does not name its cause: {v:#}"
    );
}

// A name only the resolver could not bind is a lower bound, and the envelope says how
// many, so a caller can tell "there are none" from "some were not found".
#[test]
fn a_lexical_only_list_is_a_lower_bound_naming_the_lexical_occurrences() {
    let r = repo(&[
        ("lib/a.rb", "class Alpha\n  def run\n    1\n  end\nend\n"),
        (
            "lib/b.rb",
            "class Beta\n  def use\n    Alpha.new\n  end\nend\n",
        ),
        (
            "lib/c.rb",
            "class Gamma\n  def use\n    Alpha.new\n  end\nend\n",
        ),
    ]);
    build(r.path());
    let v = occ(r.path(), "refs", "Alpha", &[]);
    assert_eq!(v["graph"].as_str(), Some("lexical"));
    let lexical = rows(&v).len() as u64;
    assert!(
        lexical > 1,
        "the fixture produced {lexical} occurrence(s), so a fixed count of one would pass: {v:#}"
    );
    assert_eq!(v["epistemic"]["completeness"].as_str(), Some("lower_bound"));
    assert_eq!(
        v["epistemic"]["causes"]["lexical_occurrences"].as_u64(),
        Some(lexical),
        "the cause does not count the lexical rows: {:#}",
        v["epistemic"]
    );
}

// A UID and a `Container.name` name path name the same symbol as the bare name, so a
// caller holding either reads the same occurrences.
#[test]
fn a_uid_and_a_name_path_resolve_to_the_bare_names_occurrences() {
    let r = go_repo();
    build(r.path());
    let by_name = occ(r.path(), "refs", "NewStore", &[]);
    let total = by_name["total"].as_u64().unwrap();
    assert!(
        total > 0,
        "the fixture has no occurrence, so resolution proves nothing"
    );

    // the UID the index itself reports for the same symbol
    let uid = uid_of(r.path(), "NewStore");
    let by_uid = occ(r.path(), "refs", &uid, &[]);
    assert_eq!(
        by_uid["total"].as_u64(),
        Some(total),
        "a uid read a different number of occurrences"
    );
    assert_eq!(by_uid["resolved_name"].as_str(), Some("NewStore"));

    let by_path = occ(r.path(), "refs", "Store.NewStore", &[]);
    assert_eq!(by_path["resolved_name"].as_str(), Some("NewStore"));
    assert_eq!(
        by_path["total"].as_u64(),
        Some(total),
        "a name path read a different number of occurrences"
    );
}

/// The uid the index stores for the symbol declaring `name`.
fn uid_of(root: &Path, name: &str) -> String {
    let index = ok_json(&["index", "stats", root.to_str().unwrap(), "--digest"]);
    let conn = Connection::open_with_flags(
        index["index_path"].as_str().unwrap(),
        OpenFlags::SQLITE_OPEN_READ_ONLY,
    )
    .unwrap();
    let mut st = conn
        .prepare(
            "SELECT s.uid FROM symbols s JOIN names n ON n.id = s.name_id WHERE n.name = ?1 LIMIT 1",
        )
        .unwrap();
    st.query_row([name], |r| r.get::<_, String>(0)).unwrap()
}

/// Each row names the kind of reference that produced it, so a caller can tell a call
/// from a type position without reading the snippet.
#[test]
fn each_row_names_the_kind_of_reference_that_produced_it() {
    let r = go_repo();
    build(r.path());
    let got = rows(&occ(r.path(), "refs", "NewStore", &[]));
    let call = got
        .iter()
        .find(|o| o["path"].as_str().unwrap_or("").ends_with("main.go"))
        .expect("no occurrence in main.go");
    assert_eq!(
        call["kind_of_reference"].as_str(),
        Some("member_call"),
        "a qualified call is not reported as a member call: {call:#}"
    );

    let r2 = repo(&[
        (
            "web/base.ts",
            "export interface Getter { get(k: string): number }\n",
        ),
        (
            "web/c.ts",
            "import { Getter } from './base';\n\
             \n\
             export class C implements Getter {\n\
             \tget(k: string): number { return 1 }\n\
             }\n",
        ),
    ]);
    build(r2.path());
    let impls = rows(&occ(r2.path(), "implementations", "Getter", &[]));
    assert_eq!(
        impls.len(),
        1,
        "the fixture produced no implementation: {impls:#?}"
    );
    assert_eq!(impls[0]["kind_of_reference"].as_str(), Some("implements"));
}

/// Two containers can declare a member of the same name, and the reference table is keyed
/// by the bare name with no symbol id on its rows, so every target form reads both uses.
/// Narrowing would need a binding the table does not hold: a package or class qualifier is
/// not a symbol in this index, and a method's recorded qualifier is its receiver variable.
/// So the answer reports the name match it is, each row carrying the snippet a caller judges
/// it by, rather than silently dropping a use.
#[test]
fn a_name_a_name_path_and_a_uid_read_the_same_namesakes_because_the_table_keys_the_bare_name() {
    let r = repo(&[
        ("go.mod", GO_MOD),
        (
            "alpha/alpha.go",
            "package alpha\n\n// Handle is alpha's own.\nfunc Handle() {}\n",
        ),
        (
            "beta/beta.go",
            "package beta\n\n// Handle is beta's own.\nfunc Handle() {}\n",
        ),
        (
            "user/user.go",
            "package user\n\n\
             import (\n\t\"example.com/app/alpha\"\n\t\"example.com/app/beta\"\n)\n\n\
             func run() {\n\talpha.Handle()\n\tbeta.Handle()\n}\n",
        ),
    ]);
    build(r.path());

    let by_name = occ(r.path(), "refs", "Handle", &[]);
    let by_path = occ(r.path(), "refs", "alpha.Handle", &[]);
    let uid = uid_of(r.path(), "Handle");
    let by_uid = occ(r.path(), "refs", &uid, &[]);
    for (label, v) in [("name path", &by_path), ("uid", &by_uid)] {
        assert_eq!(
            v["total"].as_u64(),
            by_name["total"].as_u64(),
            "a {label} read a different number of occurrences than the bare name"
        );
        assert_eq!(
            v["resolved_name"].as_str(),
            Some("Handle"),
            "a {label} did not resolve to the name the reference table is keyed by"
        );
    }

    // both uses come back, and each row's snippet names the receiver, so a caller tells
    // the two apart without the answer guessing which declaration was meant
    let got = rows(&by_name);
    assert_eq!(
        got.len(),
        2,
        "the fixture must give both call sites: {got:#?}"
    );
    // The snippet is a ±1 window, so two neighbouring rows can share a line. The
    // reference's own line is the one the window is centred on, which is what
    // distinguishes one use from the other.
    let receivers: std::collections::BTreeSet<String> = got
        .iter()
        .filter_map(|o| {
            let s = o["snippet"].as_str()?;
            let want = o["line"].as_u64()?;
            s.lines()
                .find(|l| l.starts_with(&format!("{want}:")))
                .and_then(|l| l.split_once('\t').map(|(_, rest)| rest.trim().to_string()))
        })
        .collect();
    assert_eq!(
        receivers,
        ["alpha.Handle()".to_string(), "beta.Handle()".to_string()]
            .into_iter()
            .collect(),
        "a use is missing from the name match: {got:#?}"
    );
}

// Several uses of one name inside one function, on lines that share a kind and a
// container, so they tie on every column the page used to order by except the row's own
// key. An OFFSET page across such a tie could repeat or skip a row; the unique key is the
// last ORDER BY term, so walking the list one row at a time returns each use exactly once
// and in a stable order (WS-59d-r5-4).
#[test]
fn many_uses_in_one_file_page_without_repeating_or_skipping() {
    // Every use shares one file, one container, one kind and one line, so they tie on
    // every column the page used to order by and only the row key tells them apart. That
    // is the case an OFFSET page cannot order; repo commits the tree, so the file is part
    // of the fixture rather than written after the index build.
    let body: String = (0..6)
        .map(|_| "NewStore()".to_string())
        .collect::<Vec<_>>()
        .join(" + ");
    let src = format!(
        "package store\n\nfunc NewStore() {{}}\n\nfunc Uses() int {{\n\treturn {body}\n}}\n"
    );
    let r = repo(&[("go.mod", GO_MOD), ("pkg/store/store.go", GO_STORE), ("pkg/store/uses.go", &src)]);
    build(r.path());

    let all = rows(&occ(r.path(), "refs", "NewStore", &[format!("--limit=50")]));
    assert!(all.len() >= 6, "the fixture indexed too few uses: {all:#?}");

    let mut seen: Vec<String> = Vec::new();
    let mut offset = 0usize;
    loop {
        let v = occ(
            r.path(),
            "refs",
            "NewStore",
            &[format!("--limit=1"), format!("--offset={offset}")],
        );
        let got = rows(&v);
        if got.is_empty() {
            break;
        }
        // Every use shares a path and a line, and two tied rows are byte-identical in the
        // answer, so the row count is what proves the walk neither repeated nor skipped
        // one: a skipped row shows as a short walk and a repeated one as a long one.
        for o in &got {
            seen.push(o["path"].as_str().unwrap_or_default().to_string());
        }
        offset += got.len();
        assert!(offset <= 32, "the walk did not terminate: {seen:?}");
    }
    assert_eq!(
        seen.len(),
        all.len(),
        "paging one row at a time returned {} of {} rows: {seen:?}",
        seen.len(),
        all.len()
    );
}

// One page can hold limit rows in that many files, each up to
// symbolgraph::MAX_REPO_FILE_BYTES, so a cache that kept every text until the query ended
// retained gigabytes for one page. Each file's text is dropped as soon as its last row is
// converted, so the peak is bounded by the file one row needs (WS-59d-r5-2).
#[test]
fn a_page_of_rows_in_distinct_files_does_not_hold_every_file() {
    let mut paths: Vec<String> = Vec::new();
    let mut bodies: Vec<String> = vec![GO_MOD.to_string()];
    for i in 0..24 {
        paths.push(format!("pkg/p{i}/f.go"));
        bodies.push(format!("package p{i}\n\nfunc Use() {{\n\ts := NewStore()\n\t_ = s\n}}\n"));
    }
    let files: Vec<(&str, &str)> = paths
        .iter()
        .map(|p| p.as_str())
        .zip(bodies.iter().map(|b| b.as_str()))
        .collect();
    let r = repo(&files);
    build(r.path());

    let v = occ(r.path(), "refs", "NewStore", &[format!("--limit=24")]);
    let got = rows(&v);
    assert!(got.len() >= 20, "the fixture spread its rows too thin: {v:#}");
    // every row kept its snippet, which is only possible if the file was read before it
    // was dropped: the drop is per file's last row, never before the row is converted
    for o in &got {
        assert!(
            o["snippet"].as_str().is_some_and(|s| !s.is_empty()),
            "a row lost its snippet: {o:#}"
        );
    }
    let distinct: std::collections::HashSet<&str> =
        got.iter().filter_map(|o| o["path"].as_str()).collect();
    assert!(
        distinct.len() >= 20,
        "the rows did not span many files, so the cache was not exercised: {distinct:?}"
    );
    // the page spans many files, so holding every text would hold every one of them at
    // once: the peak must stay near one file, not near the page
    let peak = v["peak_files_held"].as_u64().unwrap_or_else(|| {
        panic!("the answer does not report its peak file count: {v:#}")
    });
    assert!(
        peak <= 2,
        "a page of {} files held {peak} of them at once",
        distinct.len()
    );
}

// A page whose window ends exactly at the offset ceiling, on a list the core says has no
// more rows, is the WHOLE list and is not truncated. Reading the ceiling alone as
// truncation adds a cause to the envelope and turns a complete answer into a lower_bound
// one, which is a worse answer than the one the client asked for (ocr-review:3).
#[test]
fn a_page_ending_exactly_at_the_offset_ceiling_is_not_truncated() {
    let r = go_repo();
    build(r.path());
    // offset 4999 with limit 1 puts the window's end exactly on MAX_OCCURRENCES
    let v = occ(
        r.path(),
        "refs",
        "Store",
        &["--offset=4999".to_string(), "--limit=1".to_string()],
    );
    assert!(
        v["truncated"].as_bool().is_none_or(|t| !t),
        "a complete page at the ceiling was reported as truncated: {v:#}"
    );
    assert_eq!(
        v["epistemic"]["causes"].get("truncated"),
        None,
        "the ceiling alone added a truncated cause: {v:#}"
    );
    // and the page that genuinely runs past the ceiling is still truncated
    let past = occ(
        r.path(),
        "refs",
        "Store",
        &["--offset=4998".to_string(), "--limit=2".to_string()],
    );
    assert_ne!(
        past["total"].as_u64(),
        None,
        "the page past the ceiling did not answer: {past:#}"
    );
}

// PAR-IMP-03 asks each hit for the containing symbol's "name path, kind, line". The
// reference's own line is `line`; the containing symbol's own start line is `symbol_line`,
// and the two differ whenever a reference sits below its container's declaration. At file
// scope there is no containing symbol, so the member is absent rather than zero.
#[test]
fn each_row_carries_the_containing_symbols_own_start_line() {
    let r = go_repo();
    build(r.path());
    let v = occ(r.path(), "refs", "Store", &[]);
    let hits = rows(&v);
    assert!(!hits.is_empty(), "no rows to read a symbol_line from: {v:#}");
    let mut saw_a_line_differing_from_the_reference = false;
    for h in &hits {
        if h["kind"].as_str() == Some("File") {
            assert!(
                h["symbol_line"].is_null(),
                "a file-scope row claims a containing symbol's line: {h:#}"
            );
            continue;
        }
        let sym = h["symbol_line"]
            .as_u64()
            .unwrap_or_else(|| panic!("a row inside a symbol has no symbol_line: {h:#}"));
        let line = h["line"].as_u64().unwrap();
        assert!(
            sym <= line,
            "the containing symbol starts at {sym}, after the reference at {line}: {h:#}"
        );
        if sym < line {
            saw_a_line_differing_from_the_reference = true;
        }
    }
    assert!(
        saw_a_line_differing_from_the_reference,
        "no row had a container above the reference, so the two lines were never distinguished: {v:#}"
    );
}
