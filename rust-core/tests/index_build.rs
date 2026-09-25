//! WS-07 contracts of the code index store, exercised through the real
//! `xmustard-core index build|update|stats` binary against disposable Git repositories.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};

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
    fs::write(p, content).unwrap();
}

/// Backdate a file's mtime so its stat key is not racy (a file written within the
/// second before a scan is always re-hashed on the next update).
fn backdate(path: &Path) {
    let an_hour_ago = std::time::SystemTime::now() - std::time::Duration::from_secs(3600);
    fs::File::options()
        .write(true)
        .open(path)
        .unwrap()
        .set_modified(an_hour_ago)
        .unwrap();
}

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
        backdate(&dir.path().join(rel));
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn commit_all(root: &Path) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", "edit"]);
}

fn run(args: &[&str]) -> Output {
    Command::new(BIN).args(args).output().unwrap()
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
    ok_json(&run(&args))
}

fn db(report: &Value) -> Connection {
    let path = report["index_path"].as_str().unwrap();
    Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY).unwrap()
}

fn digest(root: &Path, extra: &[&str]) -> String {
    let mut args = vec!["--digest"];
    args.extend_from_slice(extra);
    index("stats", root, &args)["content_digest"]
        .as_str()
        .unwrap()
        .to_string()
}

/// (qualified_name, kind) → (uid, container uid, start_line, local) for one file.
type SymbolMap = BTreeMap<(String, String), (String, Option<String>, i64, bool)>;
/// (kind, module, name, alias, resolved path) per import, in source order.
type ImportRow = (
    String,
    String,
    Option<String>,
    Option<String>,
    Option<String>,
);

fn symbols_of(conn: &Connection, path: &str) -> SymbolMap {
    let mut st = conn
        .prepare(
            "SELECT s.qualified_name, s.kind, s.uid, c.uid, s.start_line, s.local
             FROM symbols s JOIN files f ON f.id = s.file_id
             LEFT JOIN symbols c ON c.id = s.container_id WHERE f.path = ?1",
        )
        .unwrap();
    st.query_map([path], |r| {
        Ok((
            (r.get(0)?, r.get(1)?),
            (r.get(2)?, r.get(3)?, r.get(4)?, r.get(5)?),
        ))
    })
    .unwrap()
    .map(Result::unwrap)
    .collect()
}

fn uids(conn: &Connection) -> BTreeSet<String> {
    let mut st = conn.prepare("SELECT uid FROM symbols").unwrap();
    st.query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

fn ref_names(conn: &Connection, path: &str) -> BTreeSet<String> {
    let mut st = conn
        .prepare(
            "SELECT n.name FROM refs r JOIN names n ON n.id = r.name_id
             JOIN files f ON f.id = r.file_id WHERE f.path = ?1",
        )
        .unwrap();
    st.query_map([path], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

fn imports_of(conn: &Connection, path: &str) -> Vec<ImportRow> {
    let mut st = conn
        .prepare(
            "SELECT i.kind, i.module, i.name, i.alias, t.path FROM imports i
             JOIN files f ON f.id = i.file_id LEFT JOIN files t ON t.id = i.resolved_file_id
             WHERE f.path = ?1 ORDER BY i.ord",
        )
        .unwrap();
    st.query_map([path], |r| {
        Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?))
    })
    .unwrap()
    .map(Result::unwrap)
    .collect()
}

/// (from, to, kind) → weight for the structure layer.
fn edges(conn: &Connection) -> BTreeMap<(String, String, String), i64> {
    let mut st = conn
        .prepare(
            "SELECT a.path, b.path, e.kind, e.weight FROM edges e
             JOIN files a ON a.id = e.src_file JOIN files b ON b.id = e.dst_file
             WHERE e.layer = 'structure'",
        )
        .unwrap();
    st.query_map([], |r| Ok(((r.get(0)?, r.get(1)?, r.get(2)?), r.get(3)?)))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

fn has(map: &SymbolMap, q: &str, kind: &str) -> bool {
    map.contains_key(&(q.to_string(), kind.to_string()))
}

fn get<'a>(map: &'a SymbolMap, q: &str, kind: &str) -> &'a (String, Option<String>, i64, bool) {
    map.get(&(q.to_string(), kind.to_string()))
        .unwrap_or_else(|| panic!("missing {kind} {q}: {map:#?}"))
}

const RUST_FIXTURE: &str = r#"//! Shapes.
pub mod geometry {
    pub struct Point {
        pub x: f64,
        pub y: f64,
    }

    impl Point {
        pub fn new(x: f64, y: f64) -> Self {
            Point { x, y }
        }

        pub fn norm(&self) -> f64 {
            let sq = |v: f64| v * v;
            (sq(self.x) + sq(self.y)).sqrt()
        }
    }

    pub mod inner {
        pub fn helper_inner() -> u32 {
            1
        }
    }
}

pub trait Area {
    fn area(&self) -> f64;
}

impl Area for geometry::Point {
    fn area(&self) -> f64 {
        0.0
    }
}

pub use geometry::inner::helper_inner;

pub enum Shape {
    Circle(f64),
    Square { side: f64 },
}
"#;

const GO_FIXTURE: &str = r#"package server

import (
	"fmt"
	h "net/http"
)

// Server handles requests.
type Server struct {
	Addr string
	mux  *h.ServeMux
}

type Handler interface {
	Serve(name string) error
}

func (s *Server) Start(port int) error {
	logf := func(msg string) { fmt.Println(msg) }
	logf("start")
	return nil
}

func NewServer(addr string) *Server { return &Server{Addr: addr} }
"#;

const TS_FIXTURE: &str = r#"import { Base } from './base';
export { helper as aliasHelper } from './util';
export * from './types';

export namespace Outer {
  export class Inner {
    method(): number {
      return 1;
    }
  }
}

export class Widget extends Base {
  private count = 0;
  render(a: string): string;
  render(a: number): string;
  render(a: any): string {
    const format = (v: unknown) => String(v);
    return format(a);
  }
  handle = () => {
    this.count += 1;
  };
}

export const makeWidget = (): Widget => new Widget();
"#;

const TSX_FIXTURE: &str = r#"import React from 'react';

export function View(props: { title: string }) {
  const onClick = () => console.log(props.title);
  return <button onClick={onClick}>{props.title}</button>;
}
"#;

const JSX_FIXTURE: &str = r#"const helpers = require('./util');

class Legacy extends React.Component {
  static defaultProps = {};
  render() {
    return <div>{helpers.helper(this.props)}</div>;
  }
}

module.exports = { Legacy };
"#;

const JS_FIXTURE: &str = r#"export function helper(x) {
  return x * 2;
}

export const api = {
  fetchAll() {
    return [];
  },
  name: 'api',
};
"#;

#[test]
fn extraction_fixtures_cover_nesting_impls_methods_closures_and_reexports() {
    let r = repo(&[
        ("src/shapes.rs", RUST_FIXTURE),
        ("server/server.go", GO_FIXTURE),
        ("web/widget.ts", TS_FIXTURE),
        ("web/base.ts", "export class Base {}\n"),
        ("web/types.ts", "export type Id = string;\n"),
        ("web/view.tsx", TSX_FIXTURE),
        ("web/legacy.jsx", JSX_FIXTURE),
        ("web/util.js", JS_FIXTURE),
    ]);
    let rep = index("build", r.path(), &[]);
    assert_eq!(rep["mode"], "full");
    assert_eq!(rep["coverage"]["eligible_files"], 8);
    assert_eq!(rep["coverage"]["indexed_files"], 8);
    assert_eq!(rep["coverage"]["extraction"]["tree_sitter"], 8);
    assert_eq!(rep["coverage"]["complete"], true, "{rep:#}");
    let conn = db(&rep);

    // ---- Rust: modules, impl blocks, trait impls, methods, closures, re-exports ----
    let rs = symbols_of(&conn, "src/shapes.rs");
    for (q, k) in [
        ("geometry", "Module"),
        ("geometry.Point", "Struct"),
        ("geometry.Point.x", "Property"),
        ("geometry.Point", "Impl"),
        ("geometry.inner", "Module"),
        ("Area", "Trait"),
        ("Area.area", "Method"),
        ("Point", "Impl"),
        ("Point.area", "Method"),
        ("Shape", "Enum"),
        ("Shape.Circle", "EnumMember"),
    ] {
        assert!(has(&rs, q, k), "rust missing {k} {q}: {rs:#?}");
    }
    let new = get(&rs, "geometry.Point.new", "Method");
    assert_eq!(new.0, "Method:src/shapes.rs:geometry.Point.new#2");
    assert_eq!(new.1.as_deref(), Some("Impl:src/shapes.rs:geometry.Point"));
    let sq = get(&rs, "geometry.Point.norm.sq", "Function");
    assert_eq!(sq.0, "Function:src/shapes.rs:geometry.Point.norm.sq#1");
    assert_eq!(
        sq.1.as_deref(),
        Some("Method:src/shapes.rs:geometry.Point.norm#0")
    );
    assert!(sq.3, "closure inside a method is local");
    let hi = get(&rs, "geometry.inner.helper_inner", "Function");
    assert_eq!(hi.1.as_deref(), Some("Module:src/shapes.rs:geometry.inner"));
    let rs_imports = imports_of(&conn, "src/shapes.rs");
    assert!(
        rs_imports.contains(&(
            "reexport".into(),
            "geometry::inner".into(),
            Some("helper_inner".into()),
            None,
            None
        )),
        "{rs_imports:?}"
    );

    // ---- Go: receiver methods attach to their type, closures, interface methods ----
    let go = symbols_of(&conn, "server/server.go");
    let server = get(&go, "Server", "Struct");
    assert_eq!(server.2, 8, "doc comment line starts the struct");
    let start = get(&go, "Server.Start", "Method");
    assert_eq!(start.0, "Method:server/server.go:Server.Start#1");
    assert_eq!(start.1.as_deref(), Some("Struct:server/server.go:Server"));
    let logf = get(&go, "Server.Start.logf", "Function");
    assert!(logf.3);
    assert!(has(&go, "Server.Addr", "Property"));
    assert!(has(&go, "Handler.Serve", "Method"));
    assert_eq!(
        get(&go, "NewServer", "Function").0,
        "Function:server/server.go:NewServer#1"
    );
    let go_imports = imports_of(&conn, "server/server.go");
    assert_eq!(go_imports.len(), 2);
    assert_eq!(go_imports[1].1, "net/http");
    assert_eq!(go_imports[1].3.as_deref(), Some("h"));

    // ---- TypeScript: nested namespace/class, overloads, closures, class fields ----
    let ts = symbols_of(&conn, "web/widget.ts");
    let inner = get(&ts, "Outer.Inner.method", "Method");
    assert_eq!(inner.1.as_deref(), Some("Class:web/widget.ts:Outer.Inner"));
    assert!(has(&ts, "Outer", "Namespace"));
    let renders: BTreeSet<String> = {
        let mut st = conn
            .prepare("SELECT uid FROM symbols WHERE qualified_name = 'Widget.render'")
            .unwrap();
        st.query_map([], |r| r.get(0))
            .unwrap()
            .map(Result::unwrap)
            .collect()
    };
    assert_eq!(
        renders.len(),
        3,
        "three distinct overload UIDs: {renders:?}"
    );
    assert!(
        renders
            .iter()
            .all(|u| u.starts_with("Method:web/widget.ts:Widget.render#1~"))
    );
    let fmt = get(&ts, "Widget.render.format", "Function");
    assert!(fmt.3 && fmt.0.ends_with("#1"));
    assert!(has(&ts, "Widget.handle", "Method"));
    assert!(has(&ts, "Widget.count", "Property"));
    assert_eq!(
        get(&ts, "makeWidget", "Function").0,
        "Function:web/widget.ts:makeWidget#0"
    );
    let ts_imports = imports_of(&conn, "web/widget.ts");
    assert_eq!(
        ts_imports,
        vec![
            (
                "import".into(),
                "./base".into(),
                Some("Base".into()),
                None,
                Some("web/base.ts".into())
            ),
            (
                "reexport".into(),
                "./util".into(),
                Some("helper".into()),
                Some("aliasHelper".into()),
                Some("web/util.js".into())
            ),
            (
                "reexport_all".into(),
                "./types".into(),
                Some("*".into()),
                None,
                Some("web/types.ts".into())
            ),
        ]
    );
    let exported: bool = conn
        .query_row(
            "SELECT exported FROM symbols WHERE uid = 'Function:web/widget.ts:makeWidget#0'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert!(exported);

    // ---- TSX / JSX / JS ----
    let tsx = symbols_of(&conn, "web/view.tsx");
    assert!(has(&tsx, "View", "Function"));
    assert!(get(&tsx, "View.onClick", "Function").3);
    let jsx = symbols_of(&conn, "web/legacy.jsx");
    assert!(has(&jsx, "Legacy", "Class"));
    assert!(has(&jsx, "Legacy.render", "Method"));
    assert!(has(&jsx, "Legacy.defaultProps", "Property"));
    assert_eq!(
        imports_of(&conn, "web/legacy.jsx")[0],
        (
            "require".into(),
            "./util".into(),
            None,
            None,
            Some("web/util.js".into())
        )
    );
    let js = symbols_of(&conn, "web/util.js");
    assert!(has(&js, "helper", "Function"));
    assert_eq!(
        get(&js, "api.fetchAll", "Method").1.as_deref(),
        Some("Const:web/util.js:api")
    );

    // ---- lexical edges: relative imports, inheritance, calls ----
    let e = edges(&conn);
    assert!(
        e.contains_key(&(
            "web/widget.ts".into(),
            "web/base.ts".into(),
            "imports".into()
        )),
        "{e:#?}"
    );
    assert!(
        e.contains_key(&(
            "web/widget.ts".into(),
            "web/base.ts".into(),
            "inherits".into()
        )),
        "{e:#?}"
    );
    assert!(
        e.contains_key(&(
            "web/legacy.jsx".into(),
            "web/util.js".into(),
            "calls".into()
        )),
        "{e:#?}"
    );
    let prov: String = conn
        .query_row("SELECT DISTINCT provenance FROM edges", [], |r| r.get(0))
        .unwrap();
    assert_eq!(prov, "lexical");
}

#[test]
fn uids_are_stable_across_unrelated_edits_and_overloads_get_suffixes() {
    let v1 = "export function alpha(a: number): number {\n  return a;\n}\n\
              export function over(a: string): void;\n\
              export function over(a: number): void;\n\
              export function over(a: any): void {}\n\
              export class K {\n  m(): void {}\n}\n";
    let r = repo(&[
        ("a.ts", v1),
        ("b.go", "package b\n\nfunc Beta(x int) int { return x }\n"),
    ]);
    let first = uids(&db(&index("build", r.path(), &[])));
    let overloads: Vec<&String> = first.iter().filter(|u| u.contains(":over#1")).collect();
    assert_eq!(overloads.len(), 3);
    assert!(overloads.iter().all(|u| u.contains("#1~")), "{overloads:?}");
    assert_eq!(overloads.iter().collect::<BTreeSet<_>>().len(), 3);
    assert!(
        first.contains("Function:a.ts:alpha#1"),
        "no suffix without a collision"
    );

    // unrelated edits: a new function above, a changed body, shifted lines.
    let v2 = format!(
        "export function inserted(): void {{}}\n\n{}",
        v1.replace("return a;", "return a + 1;")
    );
    write(r.path(), "a.ts", &v2);
    write(
        r.path(),
        "b.go",
        "package b\n\n// Beta doubles.\nfunc Beta(x int) int { return x * 2 }\n",
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    let second = uids(&db(&rep));
    assert!(
        first.is_subset(&second),
        "old UIDs changed: {:?}",
        first.difference(&second)
    );
    assert_eq!(
        second.difference(&first).cloned().collect::<Vec<_>>(),
        vec!["Function:a.ts:inserted#0".to_string()]
    );
}

#[test]
fn strings_and_comments_produce_no_references_or_edges() {
    let r = repo(&[
        (
            "b.ts",
            "export function ghostHelper(): number {\n  return 1;\n}\n",
        ),
        (
            "a.ts",
            "// ghostHelper is only mentioned in this comment\n\
             const msg = \"call ghostHelper later\";\n\
             /* block: ghostHelper */\n\
             export function other(): string {\n  return `template ghostHelper ${msg}`;\n}\n",
        ),
        (
            "c.ts",
            "import { ghostHelper } from './b';\nexport const v = ghostHelper();\n",
        ),
        (
            "g.go",
            "package g\n\n// UsesGhost mentions GhostThing\nfunc UsesGhost() string { return \"GhostThing\" }\n",
        ),
        ("t.go", "package g\n\ntype GhostThing struct{}\n"),
    ]);
    let rep = index("build", r.path(), &[]);
    let conn = db(&rep);
    let a_refs = ref_names(&conn, "a.ts");
    assert!(
        !a_refs.contains("ghostHelper"),
        "comment/string words became references: {a_refs:?}"
    );
    assert!(
        a_refs.contains("msg"),
        "template substitutions are code: {a_refs:?}"
    );
    assert!(!ref_names(&conn, "g.go").contains("GhostThing"));
    let e = edges(&conn);
    assert!(
        !e.keys().any(|(from, _, _)| from == "a.ts"),
        "false lexical edge: {e:#?}"
    );
    assert!(
        !e.keys().any(|(from, to, _)| from == "g.go" && to == "t.go"),
        "{e:#?}"
    );
    assert!(
        e.contains_key(&("c.ts".into(), "b.ts".into(), "calls".into())),
        "{e:#?}"
    );
    assert!(
        e.contains_key(&("c.ts".into(), "b.ts".into(), "imports".into())),
        "{e:#?}"
    );

    // the legacy lexical graph does create the false edges this index removes.
    let legacy = xmustard_core::symbolgraph::build_symbol_graph(r.path(), "ws");
    assert!(
        legacy
            .edges
            .iter()
            .any(|e| e.from_path == "a.ts" && e.to_path == "b.ts"),
        "legacy regression fixture no longer shows the false edge"
    );
}

#[test]
fn lexical_edges_match_the_legacy_graph_without_comments_or_strings() {
    let r = repo(&[
        (
            "core.rs",
            "pub fn compute_widget() -> i32 { 1 }\npub trait Renderable {}\npub struct Gadget {}\n",
        ),
        (
            "app.rs",
            "use crate::core::compute_widget;\nstruct Wrap {}\nimpl Renderable for Wrap {}\nfn run() -> i32 { let _g = Gadget {}; compute_widget() }\n",
        ),
        ("core_test.rs", "fn check() { let _ = compute_widget(); }\n"),
        (
            "web/a.ts",
            "import { make } from './b';\nexport function useMake() { if (make()) { return 1; } return 0; }\n",
        ),
        (
            "web/b.ts",
            "export function make(): boolean { return true; }\n",
        ),
    ]);
    let conn = db(&index("build", r.path(), &[]));
    let ours = edges(&conn);
    let legacy = xmustard_core::symbolgraph::build_symbol_graph(r.path(), "ws");
    let theirs: BTreeMap<(String, String, String), i64> = legacy
        .edges
        .iter()
        .map(|e| {
            (
                (e.from_path.clone(), e.to_path.clone(), e.kind.clone()),
                e.weight as i64,
            )
        })
        .collect();
    assert_eq!(ours, theirs);
    let flows: BTreeSet<(String, String, String)> = {
        let mut st = conn
            .prepare(
                "SELECT a.path, b.path, e.kind FROM edges e JOIN files a ON a.id = e.src_file
                 JOIN files b ON b.id = e.dst_file WHERE e.layer = 'flow'",
            )
            .unwrap();
        st.query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))
            .unwrap()
            .map(Result::unwrap)
            .collect()
    };
    let legacy_flows: BTreeSet<(String, String, String)> = legacy
        .flow_edges
        .iter()
        .map(|e| (e.from_path.clone(), e.to_path.clone(), e.kind.clone()))
        .collect();
    assert_eq!(flows, legacy_flows);
}

fn many_files(n: usize) -> Vec<(String, String)> {
    (0..n)
        .map(|i| {
            (
                format!("pkg/m{i:03}.ts"),
                format!(
                    "import {{ shared }} from './shared';\nexport function fn{i:03}Worker(): number {{\n  return shared() + {i};\n}}\n"
                ),
            )
        })
        .chain(std::iter::once((
            "pkg/shared.ts".to_string(),
            "export function shared(): number { return 1; }\n".to_string(),
        )))
        .collect()
}

fn repo_owned(files: &[(String, String)]) -> TempDir {
    let refs: Vec<(&str, &str)> = files
        .iter()
        .map(|(a, b)| (a.as_str(), b.as_str()))
        .collect();
    repo(&refs)
}

#[test]
fn incremental_update_touches_only_changed_files() {
    let r = repo_owned(&many_files(20));
    let full = index("build", r.path(), &[]);
    assert_eq!(full["counters"]["reparsed"], 21);

    let noop = index("update", r.path(), &[]);
    assert_eq!(noop["mode"], "noop");
    assert_eq!(noop["counters"]["reparsed"], 0);
    assert_eq!(noop["counters"]["unchanged"], 21);
    assert_eq!(
        noop["counters"]["bytes_read"], 0,
        "unchanged stat keys skip the read"
    );

    // touched but identical: hashed once, then its refreshed stat key skips the read
    let touched = r.path().join("pkg/m003.ts");
    fs::write(&touched, fs::read(&touched).unwrap()).unwrap();
    backdate(&touched);
    let t1 = index("update", r.path(), &[]);
    assert_eq!(t1["mode"], "noop");
    assert!(t1["counters"]["bytes_read"].as_u64().unwrap() > 0);
    let t2 = index("update", r.path(), &[]);
    assert_eq!(t2["counters"]["bytes_read"], 0);

    // a body edit: only that file is reparsed and re-resolved.
    write(
        r.path(),
        "pkg/m007.ts",
        "import { shared } from './shared';\nexport function fn007Worker(): number {\n  return shared() * 7;\n}\n",
    );
    let up = index("update", r.path(), &[]);
    assert_eq!(up["mode"], "incremental");
    assert_eq!(up["counters"]["reparsed"], 1);
    assert_eq!(up["counters"]["written"], 1);
    assert_eq!(up["counters"]["unchanged"], 20);
    assert_eq!(up["counters"]["reresolved"], 1);

    // a rename of an exported function re-resolves only the files that name it.
    write(
        r.path(),
        "pkg/shared.ts",
        "export function sharedRenamed(): number { return 1; }\n",
    );
    let up2 = index("update", r.path(), &["--paths", "pkg/shared.ts"]);
    assert_eq!(up2["counters"]["files_scanned"], 1);
    assert_eq!(up2["counters"]["reparsed"], 1);
    // shared.ts itself plus the 20 importers that mention `shared`.
    assert_eq!(up2["counters"]["reresolved"], 21);
    let conn = db(&up2);
    // the fact cache keeps every live file's facts after the update's GC.
    let cached: i64 = conn
        .query_row("SELECT count(*) FROM fact_cache", [], |r| r.get(0))
        .unwrap();
    assert!(cached >= 21, "live facts were collected: {cached}");
    let calls: i64 = conn
        .query_row("SELECT count(*) FROM edges WHERE kind = 'calls'", [], |r| {
            r.get(0)
        })
        .unwrap();
    assert_eq!(calls, 0, "edges to the removed name are gone");
    assert_eq!(digest(r.path(), &[]), {
        let other = TempDir::new().unwrap();
        let dir = other.path().join("idx");
        index("build", r.path(), &["--index-dir", dir.to_str().unwrap()]);
        digest(r.path(), &["--index-dir", dir.to_str().unwrap()])
    });
}

#[test]
fn delete_rename_and_create_sequences_match_a_full_build() {
    let r = repo(&[
        (
            "src/lib.rs",
            "pub fn compute_total(x: i32) -> i32 { x }\npub struct Ledger {}\n",
        ),
        ("src/use_total.rs", "fn go() -> i32 { compute_total(1) }\n"),
        (
            "src/old_name.rs",
            "pub fn legacy_entry() { let _l = Ledger {}; }\n",
        ),
        ("src/gone.rs", "pub fn deleted_symbol() {}\n"),
        (
            "src/caller.rs",
            "fn c() { deleted_symbol(); legacy_entry(); }\n",
        ),
        (
            "web/index.ts",
            "import { later } from './later';\nexport const x = later;\n",
        ),
    ]);
    index("build", r.path(), &[]);
    let fresh_digest = |root: &Path| {
        let other = TempDir::new().unwrap();
        let dir = other.path().join("idx");
        let d = dir.to_str().unwrap().to_string();
        index("build", root, &["--index-dir", &d, "--no-cache"]);
        digest(root, &["--index-dir", &d])
    };

    // delete + rename (content unchanged) + create + dirty edit, all uncommitted
    fs::remove_file(r.path().join("src/gone.rs")).unwrap();
    git(r.path(), &["mv", "src/old_name.rs", "src/new_name.rs"]);
    write(r.path(), "web/later.ts", "export const later = 1;\n");
    write(
        r.path(),
        "src/lib.rs",
        "pub fn compute_total(x: i32) -> i32 { x + 1 }\npub struct Ledger {}\n",
    );
    let up = index("update", r.path(), &[]);
    assert_eq!(up["mode"], "incremental", "{up:#}");
    assert_eq!(
        up["counters"]["deleted"], 2,
        "gone.rs and the old rename path"
    );
    assert!(
        up["counters"]["reused_from_cache"].as_u64().unwrap() >= 1,
        "rename reuses facts: {up:#}"
    );
    assert_eq!(
        up["counters"]["reparsed"], 1,
        "only lib.rs is parsed: {up:#}"
    );
    assert_eq!(digest(r.path(), &[]), fresh_digest(r.path()));

    // a created file resolves a previously dangling relative import
    git(r.path(), &["add", "-A"]);
    let up = index("update", r.path(), &[]);
    assert_eq!(up["mode"], "incremental");
    let conn = db(&up);
    let resolved: Option<String> = conn
        .query_row(
            "SELECT t.path FROM imports i JOIN files f ON f.id = i.file_id
             LEFT JOIN files t ON t.id = i.resolved_file_id WHERE f.path = 'web/index.ts'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(resolved.as_deref(), Some("web/later.ts"));
    assert!(edges(&conn).contains_key(&(
        "web/index.ts".into(),
        "web/later.ts".into(),
        "imports".into()
    )));
    assert_eq!(digest(r.path(), &[]), fresh_digest(r.path()));

    // commit, then delete the importer target again, announced as a watcher batch
    commit_all(r.path());
    fs::remove_file(r.path().join("web/later.ts")).unwrap();
    let up = index("update", r.path(), &["--paths", "web/later.ts"]);
    assert_eq!(up["counters"]["deleted"], 1);
    assert_eq!(up["counters"]["files_scanned"], 0);
    assert_eq!(digest(r.path(), &[]), fresh_digest(r.path()));

    // reverting to a previously indexed version is served from the fact cache
    git(r.path(), &["checkout", "--", "web/later.ts"]);
    let up = index("update", r.path(), &[]);
    assert_eq!(up["counters"]["reused_from_cache"], 1, "{up:#}");
    assert_eq!(up["counters"]["reparsed"], 0);
}

#[test]
fn untracked_files_are_indexed_only_when_configured() {
    let r = repo(&[("a.ts", "export function tracked() {}\n")]);
    write(r.path(), "b.ts", "export function untracked() {}\n");
    write(r.path(), "ignored.ts", "export function nope() {}\n");
    write(r.path(), ".gitignore", "ignored.ts\n");
    let rep = index("build", r.path(), &[]);
    assert_eq!(rep["coverage"]["indexed_files"], 1);
    let rep = index("update", r.path(), &["--include-untracked"]);
    assert_eq!(rep["reason"], "config_changed");
    assert_eq!(
        rep["coverage"]["indexed_files"], 2,
        "untracked, not ignored"
    );
}

#[test]
fn dirty_flag_and_fingerprint_changes_force_a_full_rebuild() {
    let r = repo_owned(&many_files(4));
    let rep = index("build", r.path(), &[]);
    let path = rep["index_path"].as_str().unwrap().to_string();
    let set = |k: &str, v: &str| {
        let c = Connection::open(&path).unwrap();
        c.execute("UPDATE meta SET value = ?2 WHERE key = ?1", [k, v])
            .unwrap();
    };
    set("incremental_in_progress", "1");
    let up = index("update", r.path(), &[]);
    assert_eq!(
        (up["mode"].as_str(), up["reason"].as_str()),
        (Some("full"), Some("dirty_flag"))
    );
    set("schema_fingerprint", "stale");
    let up = index("update", r.path(), &[]);
    assert_eq!(up["reason"], "schema_changed");
    set("analyzer_version", "xm-analyzer-0");
    let up = index("update", r.path(), &[]);
    assert_eq!(up["reason"], "analyzer_changed");
    let up = index("update", r.path(), &["--content-retention", "none"]);
    assert_eq!(up["reason"], "retention_changed");
    // a healthy index updates in place
    let up = index("update", r.path(), &["--content-retention", "none"]);
    assert_eq!(up["mode"], "noop");
    let stats = index("stats", r.path(), &[]);
    assert_eq!(stats["incremental_in_progress"], false);
    assert!(stats["needs_full_rebuild"].is_null(), "{stats:#}");
}

#[test]
fn escalation_gate_needs_half_the_files_and_at_least_fifty() {
    let edit_first = |root: &Path, n: usize| {
        for i in 0..n {
            write(
                root,
                &format!("pkg/m{i:03}.ts"),
                &format!(
                    "import {{ shared }} from './shared';\nexport function fn{i:03}Worker(): number {{\n  return shared() - {i};\n}}\n"
                ),
            );
        }
    };
    let r = repo_owned(&many_files(99)); // 100 files with shared.ts
    index("build", r.path(), &[]);
    edit_first(r.path(), 49);
    let up = index("update", r.path(), &[]);
    assert_eq!(up["mode"], "incremental", "49 of 100 stays incremental");
    assert_eq!(up["counters"]["escalated"], false);
    commit_all(r.path());
    index("update", r.path(), &[]);
    let r2 = repo_owned(&many_files(99));
    index("build", r2.path(), &[]);
    edit_first(r2.path(), 51);
    let up = index("update", r2.path(), &[]);
    assert_eq!(up["mode"], "full");
    assert_eq!(up["reason"], "escalated");
    assert_eq!(up["counters"]["escalated"], true);

    // more than half, but fewer than 50 files: still incremental
    let r3 = repo_owned(&many_files(9));
    index("build", r3.path(), &[]);
    edit_first(r3.path(), 8);
    let up = index("update", r3.path(), &[]);
    assert_eq!(up["mode"], "incremental");
}

fn contains(hay: &[u8], needle: &str) -> bool {
    hay.windows(needle.len()).any(|w| w == needle.as_bytes())
}

fn db_bytes(report: &Value) -> Vec<u8> {
    let p = PathBuf::from(report["index_path"].as_str().unwrap());
    let mut all = fs::read(&p).unwrap();
    for suffix in ["-wal", "-shm"] {
        if let Ok(b) = fs::read(format!("{}{suffix}", p.display())) {
            all.extend(b);
        }
    }
    all
}

#[test]
fn content_retention_none_stores_no_source_text() {
    let src = "// zebracomment explains the quokka pathway\n\
               export function renderWidget(id: number): string {\n\
               \x20 const secretToken = \"hunter2 open sesame\";\n\
               \x20 return `${secretToken}-${id}`;\n\
               }\n";
    let r = repo(&[("w.ts", src)]);
    let line = "const secretToken = \"hunter2 open sesame\";";

    let none = index("build", r.path(), &["--content-retention", "none"]);
    assert_eq!(none["content_retention"], "none");
    let bytes = db_bytes(&none);
    for needle in [
        "zebracomment",
        "quokka",
        "hunter2",
        "sesame",
        line,
        "renderWidget(id",
    ] {
        assert!(
            !contains(&bytes, needle),
            "content_retention=none stored {needle:?}"
        );
    }
    let conn = db(&none);
    let texts: i64 = conn
        .query_row("SELECT count(*) FROM chunk_text", [], |r| r.get(0))
        .unwrap();
    assert_eq!(texts, 0);
    // identifiers stay searchable through the contentless postings
    let hits: i64 = conn
        .query_row(
            "SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'secret AND token'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(hits, 1);
    let lit: i64 = conn
        .query_row(
            "SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'hunter2'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(lit, 0);
    drop(conn);

    // symbol: still no source text, but literal words are postings
    let sym = index("build", r.path(), &["--content-retention", "symbol"]);
    let bytes = db_bytes(&sym);
    assert!(!contains(&bytes, line));
    assert!(!contains(&bytes, "hunter2 open sesame"));
    let conn = db(&sym);
    let texts: i64 = conn
        .query_row("SELECT count(*) FROM chunk_text", [], |r| r.get(0))
        .unwrap();
    assert_eq!(texts, 0);
    let lit: i64 = conn
        .query_row(
            "SELECT count(*) FROM chunk_fts WHERE chunk_fts MATCH 'hunter2'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(lit, 1);
    drop(conn);

    // full: chunk text is kept for snippets
    let full = index("build", r.path(), &["--content-retention", "full"]);
    let conn = db(&full);
    let text: String = conn
        .query_row("SELECT group_concat(text, '') FROM chunk_text", [], |r| {
            r.get(0)
        })
        .unwrap();
    assert!(text.contains(line));
}

#[test]
fn output_is_deterministic() {
    let r = repo(&[
        ("src/shapes.rs", RUST_FIXTURE),
        ("server/server.go", GO_FIXTURE),
        ("web/widget.ts", TS_FIXTURE),
        ("web/util.js", JS_FIXTURE),
    ]);
    let run_once = || {
        let t = TempDir::new().unwrap();
        let d = t.path().join("idx");
        let d = d.to_str().unwrap().to_string();
        let mut rep = index("build", r.path(), &["--index-dir", &d, "--no-cache"]);
        let dig = digest(r.path(), &["--index-dir", &d]);
        rep.as_object_mut().unwrap().remove("timing");
        rep.as_object_mut().unwrap().remove("index_path");
        (rep, dig)
    };
    let (a, da) = run_once();
    let (b, db_) = run_once();
    assert_eq!(a, b);
    assert_eq!(da, db_);
}

#[test]
fn envelope_bounds_are_reported_as_coverage_losses() {
    let r = repo_owned(&many_files(4)); // 5 files
    let rep = index("build", r.path(), &["--max-files", "3"]);
    let cov = &rep["coverage"];
    assert_eq!(cov["eligible_files"], 5);
    assert_eq!(cov["indexed_files"], 3);
    assert_eq!(cov["loss_counts"]["envelope_files"], 2);
    assert_eq!(cov["envelope"]["exceeded"], serde_json::json!(["files"]));
    assert_eq!(cov["complete"], false);

    let rep = index("build", r.path(), &["--max-symbols", "2"]);
    assert_eq!(rep["coverage"]["symbols"], 2);
    assert_eq!(rep["coverage"]["loss_counts"]["symbol_budget"], 3);

    let big = "x".repeat(2048);
    write(
        r.path(),
        "pkg/huge.ts",
        &format!("export const blob = \"{big}\";\n"),
    );
    commit_all(r.path());
    let rep = index("build", r.path(), &["--max-file-size", "1024"]);
    assert_eq!(rep["coverage"]["loss_counts"]["oversized"], 1);
    // a grammar file above max_parse_bytes is extracted lexically and says so
    let rep = index("build", r.path(), &["--max-parse-bytes", "1024"]);
    assert_eq!(rep["coverage"]["loss_counts"]["lexical_fallback"], 1);
    let conn = db(&rep);
    let lex: String = conn
        .query_row(
            "SELECT parse_status FROM files WHERE path = 'pkg/huge.ts'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(lex, "regex");
}

#[test]
fn ignore_files_and_non_git_mode() {
    let r = repo(&[
        ("src/keep.ts", "export function keep() {}\n"),
        ("gen/out.ts", "export function generated() {}\n"),
        ("vendor/lib.go", "package lib\n\nfunc Vendored() {}\n"),
        ("third_party/x/y.go", "package y\n\nfunc Third() {}\n"),
        (".xmustardignore", "gen/\n!vendor/\n"),
    ]);
    let rep = index("build", r.path(), &[]);
    let conn = db(&rep);
    let mut st = conn
        .prepare("SELECT path FROM files ORDER BY path")
        .unwrap();
    let paths: Vec<String> = st
        .query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect();
    assert_eq!(paths, vec!["src/keep.ts", "vendor/lib.go"]);
    assert_eq!(rep["coverage"]["ignored_files"], 2);

    // outside Git: refused unless allowed; then .gitignore applies
    let plain = TempDir::new().unwrap();
    write(plain.path(), "a.ts", "export function a() {}\n");
    write(plain.path(), "skip/b.ts", "export function b() {}\n");
    write(plain.path(), ".gitignore", "skip/\n");
    let d = plain.path().join("idx");
    let out = run(&[
        "index",
        "build",
        plain.path().to_str().unwrap(),
        "--index-dir",
        d.to_str().unwrap(),
    ]);
    assert!(!out.status.success());
    let rep = index(
        "build",
        plain.path(),
        &["--allow-non-git", "--index-dir", d.to_str().unwrap()],
    );
    assert_eq!(rep["repo_mode"], "non-git");
    assert_eq!(rep["coverage"]["indexed_files"], 1);
}

/// Peak RSS of one child run, from wait4(2).
fn child_peak_rss(cmd: &mut Command) -> (u64, std::process::ExitStatus) {
    let child = cmd.spawn().unwrap();
    let pid = child.id() as libc::pid_t;
    let mut status: libc::c_int = 0;
    // SAFETY: a zeroed rusage is a valid out-parameter; `pid` is our unreaped child.
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    let rc = unsafe { libc::wait4(pid, &mut status, 0, &mut ru) };
    assert_eq!(rc, pid);
    std::mem::forget(child);
    let bytes = if cfg!(target_os = "macos") {
        ru.ru_maxrss as u64
    } else {
        ru.ru_maxrss as u64 * 1024
    };
    use std::os::unix::process::ExitStatusExt;
    (bytes, std::process::ExitStatus::from_raw(status))
}

fn synthetic_file(i: usize) -> (String, String) {
    let dir = format!("mod{:02}", i % 50);
    match i % 5 {
        0 => (
            format!("{dir}/svc{i}.go"),
            format!(
                "package mod{m:02}\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\n// Service{i} serves requests.\ntype Service{i} struct {{\n\tName string\n\tcount int\n}}\n\n// Handle{i} handles one request.\nfunc (s *Service{i}) Handle{i}(input string) (string, error) {{\n\tclean := func(v string) string {{ return strings.TrimSpace(v) }}\n\tif clean(input) == \"\" {{\n\t\treturn \"\", fmt.Errorf(\"empty input for service {i}\")\n\t}}\n\ts.count++\n\treturn fmt.Sprintf(\"%s:%d\", s.Name, s.count), nil\n}}\n\nfunc NewService{i}(name string) *Service{i} {{\n\treturn &Service{i}{{Name: name}}\n}}\n",
                m = i % 50
            ),
        ),
        1 => (
            format!("{dir}/lib{i}.rs"),
            format!(
                "use std::collections::HashMap;\n\n/// Store{i} keeps values.\npub struct Store{i} {{\n    items: HashMap<String, u64>,\n}}\n\nimpl Store{i} {{\n    pub fn new() -> Self {{\n        Store{i} {{ items: HashMap::new() }}\n    }}\n\n    pub fn put(&mut self, key: &str, value: u64) -> Option<u64> {{\n        // keep the previous value\n        self.items.insert(key.to_string(), value)\n    }}\n\n    pub fn total(&self) -> u64 {{\n        let add = |acc: u64, v: &u64| acc + v;\n        self.items.values().fold(0, add)\n    }}\n}}\n\npub fn helper_{i}(x: u64) -> u64 {{\n    if x > {i} {{ return x - 1; }}\n    x + 1\n}}\n"
            ),
        ),
        2 => (
            format!("{dir}/widget{i}.ts"),
            format!(
                "import {{ helper{p} }} from './util{p}';\n\nexport interface Props{i} {{\n  title: string;\n  count?: number;\n}}\n\n/** Widget{i} renders things. */\nexport class Widget{i} {{\n  private cache = new Map<string, number>();\n\n  constructor(private readonly props: Props{i}) {{}}\n\n  render(prefix: string): string {{\n    const format = (v: number) => `${{prefix}}-${{v}}`;\n    return format(this.props.count ?? {i});\n  }}\n\n  update(key: string): void {{\n    this.cache.set(key, helper{p}({i}));\n  }}\n}}\n\nexport const make{i} = (p: Props{i}) => new Widget{i}(p);\n",
                p = i + 1
            ),
        ),
        3 => (
            format!("{dir}/util{i}.js"),
            format!(
                "export function helper{i}(value) {{\n  // double the value\n  return value * 2 + {i};\n}}\n\nexport const api{i} = {{\n  fetchAll() {{\n    return fetch('/api/{i}').then((r) => r.json());\n  }},\n  name: 'api{i}',\n}};\n"
            ),
        ),
        _ => (
            format!("{dir}/view{i}.tsx"),
            format!(
                "import React from 'react';\n\nexport function View{i}(props: {{ title: string }}) {{\n  const onClick = () => console.log(props.title);\n  return <button onClick={{onClick}}>{{props.title}}</button>;\n}}\n"
            ),
        ),
    }
}

#[test]
fn build_of_5000_files_has_no_file_cap_and_peaks_under_25_mib() {
    let dir = TempDir::new().unwrap();
    for i in 0..5000 {
        let (rel, content) = synthetic_file(i);
        write(dir.path(), &rel, &content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    let out_path = dir.path().join("report.json");
    let (peak, status) = child_peak_rss(
        Command::new(BIN)
            .args(["index", "build", dir.path().to_str().unwrap(), "--no-cache"])
            .stdout(Stdio::from(fs::File::create(&out_path).unwrap()))
            .stderr(Stdio::inherit()),
    );
    assert!(status.success());
    let rep: Value = serde_json::from_slice(&fs::read(&out_path).unwrap()).unwrap();
    let cov = &rep["coverage"];
    assert_eq!(cov["eligible_files"], 5000);
    assert_eq!(cov["indexed_files"], 5000, "no file cap: {cov:#}");
    assert_eq!(cov["complete"], true, "{cov:#}");
    assert!(cov["symbols"].as_u64().unwrap() > 20_000);
    let mib = peak as f64 / (1u64 << 20) as f64;
    eprintln!(
        "index build of 5,000 files: peak RSS {mib:.1} MiB, report {:?}",
        rep["timing"]
    );
    assert!(
        mib <= 25.0,
        "peak RSS {mib:.1} MiB exceeds the 25 MiB heavy-slot line"
    );
}

#[test]
fn invalid_utf8_files_are_indexed_from_their_lossy_text() {
    let r = repo(&[("ok.ts", "export function fine() {}\n")]);
    let mut bytes = b"export function before\xff\xfeName(): number {\n  return 1;\n}\nexport function after(): void {}\n".to_vec();
    bytes.extend_from_slice(b"// trailing \xc3\x28 comment\n");
    fs::write(r.path().join("bad.ts"), &bytes).unwrap();
    backdate(&r.path().join("bad.ts"));
    commit_all(r.path());
    let rep = index("build", r.path(), &["--content-retention", "full"]);
    assert_eq!(rep["coverage"]["loss_counts"]["invalid_utf8"], 1, "{rep:#}");
    let conn = db(&rep);
    let (after_sym, text): (i64, String) = conn
        .query_row(
            "SELECT (SELECT count(*) FROM symbols WHERE uid = 'Function:bad.ts:after#0'),
                    (SELECT group_concat(t.text, '') FROM chunk_text t JOIN chunks c ON c.id = t.chunk_id
                     JOIN files f ON f.id = c.file_id WHERE f.path = 'bad.ts')",
            [],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .unwrap();
    assert_eq!(after_sym, 1);
    assert!(
        text.contains("export function after(): void {}"),
        "{text:?}"
    );
    assert!(text.contains('\u{fffd}'));
}
