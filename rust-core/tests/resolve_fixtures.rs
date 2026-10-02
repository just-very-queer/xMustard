//! WS-17 contracts of the scope/import resolver (PAR-IMP-01, PAR-IMP-05), exercised
//! through the real `xmustard-core index build|update|impact` binary against disposable
//! Git repositories.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::process::{Command, Output};

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
    // an hour old: never inside the racy stat window of the next scan
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

fn ok_json(out: &Output) -> Value {
    assert!(
        out.status.success(),
        "exit {:?}: {}",
        out.status,
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn index(cmd: &str, root: &Path, extra: &[&str]) -> Value {
    let mut args = vec!["index", cmd, root.to_str().unwrap()];
    args.extend_from_slice(extra);
    ok_json(&Command::new(BIN).args(&args).output().unwrap())
}

fn db(report: &Value) -> Connection {
    let path = report["index_path"].as_str().unwrap();
    Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY).unwrap()
}

fn digest(root: &Path) -> String {
    index("stats", root, &["--digest"])["content_digest"]
        .as_str()
        .unwrap()
        .to_string()
}

/// One resolved edge: (src path, src qualified name or `<file>`, kind, access, dst path,
/// dst qualified name or `<file>`).
type EdgeKey = (String, String, String, String, String, String);
/// (confidence, provenance, reason, weight)
type EdgeVal = (f64, String, String, i64);

fn symbol_edges(conn: &Connection) -> BTreeMap<EdgeKey, EdgeVal> {
    let mut st = conn
        .prepare(
            "SELECT a.path, coalesce(s.qualified_name, '<file>'), e.kind, coalesce(e.access, ''),
                    b.path, coalesce(d.qualified_name, '<file>'),
                    e.confidence, e.provenance, e.reason, e.weight
             FROM edges e JOIN files a ON a.id = e.src_file JOIN files b ON b.id = e.dst_file
             LEFT JOIN symbols s ON s.id = e.src_symbol LEFT JOIN symbols d ON d.id = e.dst_symbol
             WHERE e.layer = 'symbol'",
        )
        .unwrap();
    st.query_map([], |r| {
        Ok((
            (
                r.get(0)?,
                r.get(1)?,
                r.get(2)?,
                r.get(3)?,
                r.get(4)?,
                r.get(5)?,
            ),
            (r.get(6)?, r.get(7)?, r.get(8)?, r.get(9)?),
        ))
    })
    .unwrap()
    .map(Result::unwrap)
    .collect()
}

fn edge<'m>(
    m: &'m BTreeMap<EdgeKey, EdgeVal>,
    from: (&str, &str),
    kind: &str,
    access: &str,
    to: (&str, &str),
) -> &'m EdgeVal {
    let key = (
        from.0.to_string(),
        from.1.to_string(),
        kind.to_string(),
        access.to_string(),
        to.0.to_string(),
        to.1.to_string(),
    );
    m.get(&key)
        .unwrap_or_else(|| panic!("missing edge {key:?}\nhave: {:#?}", m.keys()))
}

/// (path, path) file pairs holding an edge of the given layer.
fn layer_edges(conn: &Connection, layer: &str) -> BTreeSet<(String, String)> {
    let mut st = conn
        .prepare(
            "SELECT a.path, b.path FROM edges e JOIN files a ON a.id = e.src_file
             JOIN files b ON b.id = e.dst_file WHERE e.layer = ?1",
        )
        .unwrap();
    st.query_map([layer], |r| Ok((r.get(0)?, r.get(1)?)))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

/// (path, name, cause) → count
fn drops(conn: &Connection) -> BTreeMap<(String, String, String), i64> {
    let mut st = conn
        .prepare(
            "SELECT f.path, n.name, r.cause, r.count FROM resolve_drops r
             JOIN files f ON f.id = r.file_id JOIN names n ON n.id = r.name_id",
        )
        .unwrap();
    st.query_map([], |r| Ok(((r.get(0)?, r.get(1)?, r.get(2)?), r.get(3)?)))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

const TS_BASE: &str = "export class Base {\n  greet(): string { return 'hi'; }\n}\n\
    export function helperFn(): number { return 1; }\n";
const TS_UTIL: &str = "export function formatValue(x: number): string { return String(x); }\n\
    export const LIMIT = 10;\n";
const TS_BARREL: &str =
    "export { formatValue as fmtValue } from './util';\nexport * from './base';\n";
const TS_WIDGET: &str = r#"import { Base } from './base';
import { fmtValue, helperFn } from './index';
import * as util from './util';
import React from 'react';

export class Widget extends Base {
  count: number = 0;
  render(): string {
    this.count = this.count + 1;
    const g = this.greet();
    return fmtValue(helperFn()) + util.formatValue(util.LIMIT) + g + this.local();
  }
  local(): string { return "x"; }
}

function sameFileHelper(): number { return 2; }

export function makeWidget(): Widget {
  const w = new Widget();
  w.render();
  const other = getAny();
  other.render();
  React.createElement();
  // sameFileHelper and formatValue are only named in this comment
  const s = "sameFileHelper formatValue";
  return w;
}

export function useHelper(): number { return sameFileHelper(); }
"#;
const TS_GLOBALS: &str = "function globalOnlyThing(): number { return 3; }\n";
const TS_USES_GLOBAL: &str = "export function useGlobal(): number { return globalOnlyThing(); }\n";
const GO_MOD: &str = "module example.com/app\n\ngo 1.22\n";
const GO_STORE: &str = r#"package store

// Store keeps items. NewStore and bump are named in this doc comment only.
type Store struct {
	items map[string]int
}

func NewStore() *Store { return &Store{} }

func (s *Store) Put(k string, v int) {
	s.items[k] = v
	s.bump()
}

type Getter interface {
	Get(k string) int
}
"#;
const GO_EXTRA: &str = r#"package store

func (s *Store) bump() {}

func (s *Store) Clear() { s.items = nil }

func (s *Store) Get(k string) int { return s.items[k] + helper() }

func helper() int { return 1 }
"#;
const GO_MAIN: &str = r#"package main

import (
	"fmt"

	"example.com/app/pkg/store"
)

func run(g store.Getter) int {
	s := store.NewStore()
	s.Put("a", 1)
	fmt.Println("store.NewStore and helper are only named in a string")
	return g.Get("a")
}
"#;

fn fixture() -> TempDir {
    repo(&[
        ("web/base.ts", TS_BASE),
        ("web/util.ts", TS_UTIL),
        ("web/index.ts", TS_BARREL),
        ("web/widget.ts", TS_WIDGET),
        ("web/globals.ts", TS_GLOBALS),
        ("web/uses_global.ts", TS_USES_GLOBAL),
        ("go.mod", GO_MOD),
        ("pkg/store/store.go", GO_STORE),
        ("pkg/store/extra.go", GO_EXTRA),
        ("cmd/main.go", GO_MAIN),
    ])
}

#[test]
fn resolution_tiers_imports_reexports_namespaces_packages_and_receivers() {
    let r = fixture();
    let conn = db(&index("build", r.path(), &[]));
    let e = symbol_edges(&conn);
    let w = "web/widget.ts";

    // same file: 0.95, scope-proven
    let same = edge(&e, (w, "useHelper"), "CALLS", "", (w, "sameFileHelper"));
    assert_eq!(
        (same.0, same.1.as_str(), same.2.as_str()),
        (0.95, "scope", "same-file")
    );
    // named import: 0.9
    let base = edge(&e, (w, "Widget"), "EXTENDS", "", ("web/base.ts", "Base"));
    assert_eq!(
        (base.0, base.1.as_str(), base.2.as_str()),
        (0.9, "scope", "import")
    );
    // aliased re-export through the barrel, and `export *`
    let fmt = edge(
        &e,
        (w, "Widget.render"),
        "CALLS",
        "",
        ("web/util.ts", "formatValue"),
    );
    assert_eq!((fmt.0, fmt.2.as_str(), fmt.3), (0.9, "re-export", 2));
    edge(
        &e,
        (w, "Widget.render"),
        "CALLS",
        "",
        ("web/base.ts", "helperFn"),
    );
    edge(
        &e,
        (w, "<file>"),
        "IMPORTS",
        "",
        ("web/base.ts", "helperFn"),
    );
    // namespace import member
    let lim = edge(
        &e,
        (w, "Widget.render"),
        "USES",
        "",
        ("web/util.ts", "LIMIT"),
    );
    assert_eq!(lim.2, "namespace-import");
    // this-typed members, inherited first-wins, read and write access
    edge(
        &e,
        (w, "Widget.render"),
        "CALLS",
        "",
        ("web/base.ts", "Base.greet"),
    );
    edge(&e, (w, "Widget.render"), "CALLS", "", (w, "Widget.local"));
    edge(
        &e,
        (w, "Widget.render"),
        "ACCESSES",
        "write",
        (w, "Widget.count"),
    );
    edge(
        &e,
        (w, "Widget.render"),
        "ACCESSES",
        "read",
        (w, "Widget.count"),
    );
    // constructed local: `const w = new Widget(); w.render()`
    let recv = edge(&e, (w, "makeWidget"), "CALLS", "", (w, "Widget.render"));
    assert_eq!(recv.2, "receiver-typed");
    // declarations
    edge(&e, (w, "Widget"), "HAS_METHOD", "", (w, "Widget.render"));
    edge(&e, (w, "Widget"), "HAS_PROPERTY", "", (w, "Widget.count"));
    // global fallback: 0.5, provenance lexical
    let g = edge(
        &e,
        ("web/uses_global.ts", "useGlobal"),
        "CALLS",
        "",
        ("web/globals.ts", "globalOnlyThing"),
    );
    assert_eq!(
        (g.0, g.1.as_str(), g.2.as_str()),
        (0.5, "lexical", "global-unique-name")
    );

    // Go: package-qualified call, constructor-typed and parameter-typed receivers
    let m = "cmd/main.go";
    let s = "pkg/store/store.go";
    let x = "pkg/store/extra.go";
    let pkg = edge(&e, (m, "run"), "CALLS", "", (s, "NewStore"));
    assert_eq!((pkg.0, pkg.2.as_str()), (0.9, "package"));
    edge(&e, (m, "run"), "CALLS", "", (s, "Store.Put"));
    edge(&e, (m, "run"), "CALLS", "", (s, "Getter.Get"));
    edge(&e, (m, "run"), "USES", "", (s, "Getter"));
    edge(&e, (m, "<file>"), "IMPORTS", "", (s, "<file>"));
    edge(&e, (m, "<file>"), "IMPORTS", "", (x, "<file>"));
    // method receivers: a method on the same type declared in another package file
    let bump = edge(&e, (s, "Store.Put"), "CALLS", "", (x, "Store.bump"));
    assert_eq!((bump.0, bump.2.as_str()), (0.9, "receiver-typed"));
    // `s.items[k] = v` is a WRITE. This assertion read "read" until WS-FIX-19: it pinned
    // the bug the row was opened for, because the extractor stopped at the
    // `selector_expression` and never saw the assignment through the `index_expression`
    // above it.
    let items = edge(
        &e,
        (s, "Store.Put"),
        "ACCESSES",
        "write",
        (s, "Store.items"),
    );
    assert_eq!(items.0, 0.95);
    edge(&e, (x, "Store.Get"), "ACCESSES", "read", (s, "Store.items"));
    edge(
        &e,
        (x, "Store.Clear"),
        "ACCESSES",
        "write",
        (s, "Store.items"),
    );
    edge(&e, (x, "Store.Get"), "CALLS", "", (x, "helper"));
    edge(&e, (s, "Store"), "HAS_METHOD", "", (s, "Store.Put"));
    let far = edge(&e, (s, "Store"), "HAS_METHOD", "", (x, "Store.bump"));
    assert_eq!(far.2, "same-package");

    // every tier sets its confidence and provenance
    for ((_, _, kind, _, _, _), (conf, prov, reason, _)) in &e {
        let want = match reason.as_str() {
            "global-unique-name" => (0.5, "lexical"),
            "same-file" | "declaration" => (0.95, "scope"),
            _ => (if *conf == 0.95 { 0.95 } else { 0.9 }, "scope"),
        };
        assert_eq!((*conf, prov.as_str()), want, "{kind} {reason}");
    }
}

#[test]
fn names_in_strings_and_comments_never_create_symbol_edges() {
    let r = fixture();
    let conn = db(&index("build", r.path(), &[]));
    let e = symbol_edges(&conn);
    // widget.ts names sameFileHelper/formatValue in a comment and a string inside
    // makeWidget; main.go names NewStore/helper in a string; store.go's doc comment
    // names NewStore and bump.
    let from = |path: &str, sym: &str| {
        e.keys()
            .filter(|k| k.0 == path && k.1 == sym)
            .map(|k| k.5.clone())
            .collect::<BTreeSet<_>>()
    };
    let make = from("web/widget.ts", "makeWidget");
    assert!(
        !make.contains("sameFileHelper") && !make.contains("formatValue"),
        "{make:?}"
    );
    assert!(!from("cmd/main.go", "run").contains("helper"));
    assert_eq!(
        e.keys()
            .filter(|k| k.0 == "cmd/main.go" && k.5 == "NewStore")
            .count(),
        1,
        "one call, not the string mention"
    );
    assert!(
        !e.keys()
            .any(|k| k.0 == "pkg/store/store.go" && k.1 == "Store" && k.2 == "CALLS"),
        "doc comment names made an edge: {e:#?}"
    );
}

#[test]
fn drop_counters_record_untyped_receivers_external_calls_and_boundaries() {
    let r = fixture();
    let rep = index("build", r.path(), &[]);
    let counters = &rep["counters"]["resolve"];
    assert!(
        counters["drops"]["receiver_untyped"].as_u64().unwrap() >= 1,
        "{counters}"
    );
    assert!(
        counters["drops"]["external"].as_u64().unwrap() >= 3,
        "{counters}"
    );
    assert_eq!(
        counters["files"],
        10 - 1,
        "every Go and TS file but go.mod: {counters}"
    );
    for tier in ["same_file", "import_scoped", "global"] {
        assert!(counters["tiers"][tier].as_u64().unwrap() >= 1, "{counters}");
    }
    let d = drops(&db(&rep));
    let has = |p: &str, n: &str, c: &str| d.contains_key(&(p.into(), n.into(), c.into()));
    // `other.render()` with `other` untyped
    assert!(has("web/widget.ts", "render", "receiver_untyped"), "{d:#?}");
    // an interface call and an interface nobody is decided to implement
    assert!(has("cmd/main.go", "Get", "dispatch_boundary"), "{d:#?}");
    assert!(
        has("pkg/store/store.go", "Getter", "undecided_interface"),
        "{d:#?}"
    );
    // external bindings are counted but not kept (they never hide an in-repo dependent)
    assert!(!d.keys().any(|k| k.2 == "external"), "{d:#?}");
}

/// Every listed item of an impact v2 answer, with its depth.
fn impact_items(v: &Value) -> Vec<(u64, &Value)> {
    v["by_depth"]
        .as_array()
        .unwrap_or_else(|| panic!("{v:#}"))
        .iter()
        .flat_map(|t| {
            let d = t["depth"].as_u64().unwrap();
            t["items"].as_array().unwrap().iter().map(move |i| (d, i))
        })
        .collect()
}

// `index impact` reads the graph segment (WS-35): the resolved symbol edges with their
// confidence, provenance and reason, and the completeness envelope from the segment's
// drop sections.
#[test]
fn impact_returns_resolved_edges_with_confidence_provenance_and_completeness() {
    let r = fixture();
    index("build", r.path(), &[]);
    let put = index("impact", r.path(), &["--symbol", "Store.Put"]);
    assert_eq!(put["graph"], "resolved");
    assert_eq!(put["epistemic"]["completeness"], "exact", "{put:#}");
    let hits = impact_items(&put);
    assert_eq!(hits.len(), 1, "{put:#}");
    let (depth, run) = hits[0];
    assert_eq!(depth, 1);
    assert_eq!(run["name"], "run");
    assert_eq!(run["edge"], "CALLS");
    assert_eq!(run["confidence"], 0.9);
    assert_eq!(run["provenance"], "scope");
    assert!(!run["reason"].as_str().unwrap().is_empty(), "{run:#}");
    assert_eq!(put["risk"], "LOW");

    // Store.Get is reachable through the Getter interface: a lower bound, with why, and
    // no dependent found is UNKNOWN risk
    let get = index("impact", r.path(), &["--symbol", "Store.Get"]);
    assert_eq!(get["epistemic"]["completeness"], "lower_bound", "{get:#}");
    assert_eq!(
        get["epistemic"]["causes"]["dispatch_boundary"], 1,
        "{get:#}"
    );
    assert!(
        get["epistemic"]["boundaries"]
            .as_array()
            .unwrap()
            .iter()
            .any(|b| b["path"] == "cmd/main.go" && b["name"] == "Get")
    );
    assert_eq!(get["risk"], "UNKNOWN");
    assert!(get["risk_note"].as_str().unwrap().contains("lower bound"));

    // two hops, confidence multiplies along the path
    let fv = index("impact", r.path(), &["--symbol", "formatValue"]);
    let (depth, two) = impact_items(&fv)
        .into_iter()
        .find(|(_, i)| i["name"] == "makeWidget")
        .unwrap_or_else(|| panic!("{fv:#}"));
    assert_eq!(depth, 2);
    assert_eq!(two["confidence"], 0.855);
    assert_eq!(two["via"], "Widget.render");
    // makeWidget calls render on an untyped receiver elsewhere: lower bound
    assert_eq!(fv["epistemic"]["completeness"], "lower_bound");

    // a language the resolver does not own falls back to lexical file edges
    let rr = repo(&[
        ("core.rb", "def compute_widget\n  1\nend\n"),
        ("app.rb", "def run\n  compute_widget()\nend\n"),
    ]);
    index("build", rr.path(), &[]);
    let lex = index("impact", rr.path(), &["--symbol", "compute_widget"]);
    assert_eq!(lex["graph"], "lexical");
    assert_eq!(lex["epistemic"]["completeness"], "lower_bound");
    assert_eq!(lex["epistemic"]["causes"]["lexical_graph"], 1);
    let (_, app) = impact_items(&lex)[0];
    assert_eq!(app["path"], "app.rb");
    assert_eq!(app["kind"], "File");
    assert_eq!(app["provenance"], "lexical");
}

#[test]
fn updates_re_resolve_only_the_dependents_of_changed_exports() {
    let r = fixture();
    index("build", r.path(), &[]);

    // a body edit changes no export: only the edited file re-resolves
    write(
        r.path(),
        "web/util.ts",
        &TS_UTIL.replace("String(x)", "String(x + 1)"),
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    assert_eq!(rep["counters"]["reresolved"], 1, "{:#}", rep["counters"]);
    assert_eq!(rep["counters"]["resolve"]["files"], 1);
    // edges into the rewritten file still point at its (re-inserted) symbols
    let e = symbol_edges(&db(&rep));
    edge(
        &e,
        ("web/widget.ts", "Widget.render"),
        "CALLS",
        "",
        ("web/util.ts", "formatValue"),
    );
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(
        incremental,
        digest(r.path()),
        "update drifted from a full build"
    );

    // renaming an export re-resolves the files that name it (the barrel re-exports it,
    // the widget calls it through a namespace import) and nothing else
    write(
        r.path(),
        "web/util.ts",
        &TS_UTIL.replace("formatValue", "formatNumber"),
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["counters"]["reresolved"], 3, "{:#}", rep["counters"]);
    let e = symbol_edges(&db(&rep));
    assert!(
        !e.keys().any(|k| k.5 == "formatValue"),
        "stale edge: {e:#?}"
    );
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(incremental, digest(r.path()));

    // a Go method added away from its type: the type's file owns HAS_METHOD
    write(
        r.path(),
        "pkg/store/extra.go",
        &format!("{GO_EXTRA}\nfunc (s *Store) Reset() {{}}\n"),
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["counters"]["reresolved"], 2, "{:#}", rep["counters"]);
    edge(
        &symbol_edges(&db(&rep)),
        ("pkg/store/store.go", "Store"),
        "HAS_METHOD",
        "",
        ("pkg/store/extra.go", "Store.Reset"),
    );
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(incremental, digest(r.path()));

    // deleting a file re-resolves the files whose edges pointed into it
    fs::remove_file(r.path().join("web/globals.ts")).unwrap();
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["counters"]["reresolved"], 1, "{:#}", rep["counters"]);
    assert!(
        !symbol_edges(&db(&rep))
            .keys()
            .any(|k| k.5 == "globalOnlyThing")
    );
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(incremental, digest(r.path()));
}

#[test]
fn changing_a_base_class_re_resolves_callers_of_inherited_members() {
    let r = repo(&[
        (
            "web/base.ts",
            "export class Parent {\n  greetPeople(): string { return 'p'; }\n}\n\
             export class Other {\n  greetPeople(): string { return 'o'; }\n}\n",
        ),
        (
            "web/child.ts",
            "import { Parent, Other } from './base';\nexport class Child extends Parent {}\n",
        ),
        (
            "web/use.ts",
            "import { Child } from './child';\n\
             export function caller(c: Child): string { return c.greetPeople(); }\n",
        ),
    ]);
    let (u, b) = (("web/use.ts", "caller"), "web/base.ts");
    let e = symbol_edges(&db(&index("build", r.path(), &[])));
    edge(&e, u, "CALLS", "", (b, "Parent.greetPeople"));

    let child = "import { Parent, Other } from './base';\nexport class Child extends Other {}\n";
    write(r.path(), "web/child.ts", child);
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    let e = symbol_edges(&db(&rep));
    edge(&e, u, "CALLS", "", (b, "Other.greetPeople"));
    assert!(
        !e.keys()
            .any(|k| k.0 == "web/use.ts" && k.5 == "Parent.greetPeople"),
        "stale edge: {e:#?}"
    );
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(
        incremental,
        digest(r.path()),
        "update drifted from a full build"
    );
}

#[test]
fn es_modules_hide_unexported_names_and_many_rows_in_one_file_stay_ambiguous() {
    let overloads = "function dupeName(x: number): void;\n".repeat(9);
    let r = repo(&[
        ("web/cjs.js", "function hiddenHelper() { return 1; }\n"),
        (
            "web/esm.ts",
            "export const shown = 1;\nfunction hiddenHelper() { return 2; }\n",
        ),
        (
            "web/use.ts",
            "import { hiddenHelper } from './esm';\nimport { hiddenHelper as h } from './cjs';\n\
             export function useThem() { return hiddenHelper() + h() + dupeName(1); }\n",
        ),
        (
            "web/a.ts",
            &format!("{overloads}function dupeName(x: number): void {{}}\n"),
        ),
        ("web/b.ts", "function dupeName(x: number): void {}\n"),
    ]);
    let rep = index("build", r.path(), &[]);
    let e = symbol_edges(&db(&rep));
    let to = |path: &str| {
        e.keys()
            .any(|k| k.0 == "web/use.ts" && k.2 == "CALLS" && k.4 == path)
    };
    // an ES module's unexported declaration is no export; a CommonJS one is
    assert!(!to("web/esm.ts") && to("web/cjs.js"), "{e:#?}");
    let d = drops(&db(&rep));
    let has = |n: &str, c: &str| d.contains_key(&("web/use.ts".into(), n.into(), c.into()));
    assert!(has("hiddenHelper", "unresolved"), "{d:#?}");
    // nine rows in a.ts do not hide b.ts: ambiguous, not a unique global
    assert!(!to("web/a.ts") && !to("web/b.ts"), "{e:#?}");
    assert!(has("dupeName", "ambiguous"), "{d:#?}");

    // dropping the module's last export makes its declarations CommonJS exports again
    write(
        r.path(),
        "web/esm.ts",
        "function hiddenHelper() { return 2; }\n",
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    let incremental = digest(r.path());
    index("build", r.path(), &[]);
    assert_eq!(
        incremental,
        digest(r.path()),
        "update drifted from a full build"
    );
}

/// Copy a fixture tree into a fresh repository.
fn repo_from(dir: &Path) -> TempDir {
    let mut files = Vec::new();
    fn walk(base: &Path, d: &Path, out: &mut Vec<(String, String)>) {
        for e in fs::read_dir(d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                walk(base, &p, out);
            } else {
                let rel = p
                    .strip_prefix(base)
                    .unwrap()
                    .to_string_lossy()
                    .replace('\\', "/");
                out.push((rel, fs::read_to_string(&p).unwrap()));
            }
        }
    }
    walk(dir, dir, &mut files);
    let refs: Vec<(&str, &str)> = files
        .iter()
        .map(|(a, b)| (a.as_str(), b.as_str()))
        .collect();
    repo(&refs)
}

/// Resolver CALLS edges on a precision fixture against the type checker's ground truth
/// (go/types, which gopls uses; the TypeScript checker, which tsserver uses): the
/// ground truth is regenerated by `tests/fixtures/resolve/precision/truth/gen_*`.
/// Precision is what EVAL-09 records; recall is reported alongside it.
#[test]
fn precision_against_type_checker_ground_truth() {
    let base = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/resolve/precision");
    let mut total = (0usize, 0usize, 0usize);
    for lang in ["go", "ts"] {
        let r = repo_from(&base.join(lang));
        let conn = db(&index("build", r.path(), &[]));
        let ours: BTreeSet<(String, String)> = symbol_edges(&conn)
            .into_keys()
            .filter(|k| k.2 == "CALLS")
            .map(|k| (format!("{}::{}", k.0, k.1), format!("{}::{}", k.4, k.5)))
            .collect();
        let truth: BTreeSet<(String, String)> = serde_json::from_str::<Vec<(String, String)>>(
            &fs::read_to_string(base.join(format!("truth/{lang}_calls.json"))).unwrap(),
        )
        .unwrap()
        .into_iter()
        .collect();
        let hit = ours.intersection(&truth).count();
        let precision = hit as f64 / ours.len().max(1) as f64;
        let recall = hit as f64 / truth.len().max(1) as f64;
        eprintln!(
            "resolver precision {lang}: {hit}/{} = {precision:.3}, recall {hit}/{} = {recall:.3}\n  \
             false: {:?}\n  missed: {:?}",
            ours.len(),
            truth.len(),
            ours.difference(&truth).collect::<Vec<_>>(),
            truth.difference(&ours).collect::<Vec<_>>(),
        );
        assert!(precision >= 0.9, "{lang} precision {precision:.3}");
        total = (total.0 + hit, total.1 + ours.len(), total.2 + truth.len());
    }
    eprintln!(
        "resolver precision overall: {}/{} = {:.3}, recall {}/{} = {:.3}",
        total.0,
        total.1,
        total.0 as f64 / total.1 as f64,
        total.0,
        total.2,
        total.0 as f64 / total.2 as f64
    );
}

// ---------------------------------------------------------------------------
// WS-FIX-19: the WS-17 minors whose status was unknown. Each test here measures the
// behaviour the row names. Where the behaviour is already correct the test pins it,
// so a later change that reintroduces the finding fails instead of passing silently.
// ---------------------------------------------------------------------------

// Item 2, "Go selector writes read as `read`": NOT REPRODUCED. A plain selector write,
// a compound assignment, an index-write through a selector, a package-level var and a
// cross-file receiver all classify as `write`, and a read of the same symbol stays `read`.
//
// Each read assertion sits in a function whose ONLY mention of the symbol is a read, so
// it cannot be satisfied by a write that was misread: `ReadsOnly` names each symbol once.
// Without that separation a write classified as `read` would pass the read assertion and
// the test could not see the finding it is named for.
#[test]
fn a_go_selector_write_is_a_write_and_not_a_read() {
    let r = repo(&[
        ("go.mod", GO_MOD),
        (
            "pkg/s/s.go",
            r#"package s

type Box struct {
	Count int
	Name  string
	Items map[string]int
}

var Global int

// WritesOnly names each symbol exactly once, in write position.
func WritesOnly(b *Box, n int) {
	b.Count = n
	b.Name = "x"
	b.Items["k"] = n
	Global = n
}

// ReadsOnly names each symbol exactly once, in read position.
func ReadsOnly(b *Box) (int, int, int, int) {
	return b.Count + len(b.Name) + len(b.Items) + Global
}

// CompoundsOnly: `b.Count += 1` is a WRITE. The RHS is also a read of `b.Count`,
    // but the extractor emits one reference per name occurrence and the write
    // classification wins, so only the write edge exists.
func CompoundsOnly(b *Box) { b.Count += 1 }
"#,
        ),
        (
            "pkg/s/u.go",
            "package s\n\nfunc Other(b *Box) { b.Count = 9 }\n",
        ),
    ]);
    let conn = db(&index("build", r.path(), &[]));
    let e = symbol_edges(&conn);
    let s = "pkg/s/s.go";
    let x = "pkg/s/u.go";
    // plain selector writes, an index-write and a package-level var
    edge(&e, (s, "WritesOnly"), "ACCESSES", "write", (s, "Box.Count"));
    edge(&e, (s, "WritesOnly"), "ACCESSES", "write", (s, "Box.Name"));
    edge(&e, (s, "WritesOnly"), "ACCESSES", "write", (s, "Box.Items"));
    edge(&e, (s, "WritesOnly"), "ACCESSES", "write", (s, "Global"));
    // the reads: only ReadsOnly mentions these symbols, and it reads them
    let c = edge(&e, (s, "ReadsOnly"), "ACCESSES", "read", (s, "Box.Count"));
    assert_eq!(c.0, 0.95);
    edge(&e, (s, "ReadsOnly"), "ACCESSES", "read", (s, "Box.Name"));
    edge(&e, (s, "ReadsOnly"), "ACCESSES", "read", (s, "Box.Items"));
    edge(&e, (s, "ReadsOnly"), "ACCESSES", "read", (s, "Global"));
    // `b.Count += 1`: a compound assignment is a write
    edge(
        &e,
        (s, "CompoundsOnly"),
        "ACCESSES",
        "write",
        (s, "Box.Count"),
    );
    // a write through a receiver typed in ANOTHER file of the same package
    let far = edge(&e, (x, "Other"), "ACCESSES", "write", (s, "Box.Count"));
    assert_eq!((far.0, far.2.as_str()), (0.9, "receiver-typed"));
}

// Item 3, "Go/TS lexical file edges": NOT REPRODUCED as a defect. The WS-17 Decision
// (plan line 1319) keeps the lexical `structure`/`flow` layers beside the resolved
// `symbol` layer for exactly these two languages, as data for the legacy consumers.
// This test pins that: a Go package-qualified call and a TS named import BOTH resolve
// to symbol edges, and both files still keep their file-level edge, so `index impact`
// on a resolved symbol walks `symbol` while a file-level consumer still has `lexical`.
#[test]
fn a_resolved_go_or_ts_edge_keeps_its_lexical_file_edge() {
    let r = repo(&[
        (
            "web/a.ts",
            "export function alpha(): number { return 1; }\n",
        ),
        (
            "web/b.ts",
            "import { alpha } from './a';\nexport function beta(): number { return alpha(); }\n",
        ),
        ("go.mod", GO_MOD),
        ("pkg/x/x.go", "package x\n\nfunc Alpha() int { return 1 }\n"),
        (
            "pkg/y/y.go",
            "package y\n\nimport \"example.com/app/pkg/x\"\n\nfunc Beta() int { return x.Alpha() }\n",
        ),
    ]);
    let conn = db(&index("build", r.path(), &[]));
    // the resolved symbol layer
    let e = symbol_edges(&conn);
    let ts = edge(&e, ("web/b.ts", "beta"), "CALLS", "", ("web/a.ts", "alpha"));
    assert_eq!(
        (ts.0, ts.1.as_str(), ts.2.as_str()),
        (0.9, "scope", "import")
    );
    let go = edge(
        &e,
        ("pkg/y/y.go", "Beta"),
        "CALLS",
        "",
        ("pkg/x/x.go", "Alpha"),
    );
    assert_eq!(
        (go.0, go.1.as_str(), go.2.as_str()),
        (0.9, "scope", "package")
    );
    // and the lexical file edges the Decision keeps beside them
    let lexical = layer_edges(&conn, "structure");
    assert!(
        lexical.contains(&("web/b.ts".into(), "web/a.ts".into())),
        "the TS importer lost its lexical file edge: {lexical:?}"
    );
    assert!(
        lexical.contains(&("pkg/y/y.go".into(), "pkg/x/x.go".into())),
        "the Go importer lost its lexical file edge: {lexical:?}"
    );
}

// Item 4, "the TS global-tier candidate limit": NOT REPRODUCED. The global tier
// resolves a unique top-level definition for every referencing file, not only the
// first, so a nine-caller fixture must produce nine edges at 0.5/lexical. The lookup
// is bounded (`top_level_files(..., 2)` decides uniqueness however many rows one file
// holds), so the candidate set cannot grow with the number of callers.
#[test]
fn a_global_tier_name_resolves_for_every_calling_file() {
    let mut list: Vec<(String, String)> = vec![(
        "src/uniq.ts".to_string(),
        "export function oneAndOnlyUniqueName(): number { return 1; }\n".to_string(),
    )];
    // nine caller files, each calling the name with NO import: the global tier, not the
    // import tier (which would resolve at 0.9 and prove nothing about the candidate set)
    for i in 1..=9 {
        list.push((
            format!("src/c{i}.ts"),
            format!("export function c{i}(): number {{ return oneAndOnlyUniqueName(); }}\n"),
        ));
    }
    let dir = TempDir::new().unwrap();
    for (rel, content) in &list {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    let conn = db(&index("build", dir.path(), &[]));
    let e = symbol_edges(&conn);
    let mut callers = 0;
    for ((_, _, kind, _, dst_path, dst_name), (conf, prov, reason, _)) in &e {
        if kind == "CALLS" && dst_name == "oneAndOnlyUniqueName" && dst_path == "src/uniq.ts" {
            assert_eq!(
                (*conf, prov.as_str(), reason.as_str()),
                (0.5, "lexical", "global-unique-name")
            );
            callers += 1;
        }
    }
    assert_eq!(callers, 9, "one global-tier caller is missing: {e:?}");
}

// Item 5, "`find_export` falling back to a non-exported declaration": NOT REPRODUCED
// as a defect. `imports_ts::find` falls back to a file's top-level declaration only
// when the file uses NO ES export syntax at all (`has_es_exports`), which is the
// documented CommonJS rule: a CommonJS module exports without the keyword. Two tests,
// both halves: a name a file really re-exports resolves, and a name an ES module does
// not export is NOT reachable even when a barrel claims it.
#[test]
fn find_export_follows_a_real_re_export_and_stops_at_an_es_module_boundary() {
    // (a) `deep.ts` declares `secret` and re-exports it by name: a true positive.
    let r = repo(&[
        (
            "src/deep.ts",
            "function secret(): number { return 1; }\nexport { secret };\n",
        ),
        ("src/barrel.ts", "export { secret } from './deep';\n"),
        (
            "src/use.ts",
            "import { secret } from './barrel';\nexport function go(): number { return secret(); }\n",
        ),
    ]);
    let conn = db(&index("build", r.path(), &[]));
    let e = symbol_edges(&conn);
    let hit = edge(
        &e,
        ("src/use.ts", "go"),
        "CALLS",
        "",
        ("src/deep.ts", "secret"),
    );
    assert_eq!((hit.0, hit.2.as_str()), (0.9, "re-export"));

    // (b) the documented CommonJS boundary, the half this test is named for: a file
    // with NO ES export syntax exports its top-level declarations without the keyword,
    // so the barrel's claim IS honoured. `plain.js` is that file and the resolution is
    // correct -- this pins the rule the fallback implements.
    let r2 = repo(&[
        ("src/plain.js", "function helper() { return 1; }\n"),
        ("src/barrel2.js", "export { helper } from './plain';\n"),
        (
            "src/use2.js",
            "import { helper } from './barrel2';\nexport function go2() { return helper(); }\n",
        ),
    ]);
    let e2 = symbol_edges(&db(&index("build", r2.path(), &[])));
    let cjs = edge(
        &e2,
        ("src/use2.js", "go2"),
        "CALLS",
        "",
        ("src/plain.js", "helper"),
    );
    assert_eq!((cjs.0, cjs.2.as_str()), (0.9, "re-export"));

    // (c) and the guard really is the boundary: an ES module that exports something ELSE
    // does NOT export `hidden`, so the barrel's claim is refused even though the
    // declaration is there. This is the half that regresses if the `!has_es_exports`
    // guard is dropped.
    let r3 = repo(&[
        (
            "src/mod.ts",
            "export const shown = 1;\nfunction hidden(): number { return 2; }\n",
        ),
        ("src/barrel3.ts", "export { hidden } from './mod';\n"),
        (
            "src/use3.ts",
            "import { hidden } from './barrel3';\nexport function go3(): number { return hidden(); }\n",
        ),
    ]);
    let e3 = symbol_edges(&db(&index("build", r3.path(), &[])));
    let leaked = e3.iter().any(|((_, _, _, _, dst_path, dst_name), _)| {
        dst_path == "src/mod.ts" && dst_name == "hidden"
    });
    assert!(
        !leaked,
        "an unexported declaration resolved through a barrel: {e3:?}"
    );
}
