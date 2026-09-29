//! WS-39 contracts of the Rust, Python and Java resolvers (PAR-IMP-01, PAR-IMP-05),
//! exercised through the real `xmustard-core index build|update` binary against
//! disposable Git repositories built from `tests/fixtures/resolve/more/{py,java,rust}`.

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

fn fixtures() -> std::path::PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/resolve/more")
}

/// A fresh repository holding the fixture trees `(fixture, prefix)` (each fixture's
/// `truth` folder left out), every file under its prefix.
fn repo_of(fixture: &[(&str, &str)]) -> TempDir {
    fn walk(base: &Path, dir: &Path, out: &mut Vec<String>) {
        for e in fs::read_dir(dir).unwrap() {
            let p = e.unwrap().path();
            match p.is_dir() {
                true => walk(base, &p, out),
                false => out.push(p.strip_prefix(base).unwrap().to_string_lossy().into()),
            }
        }
    }
    let dir = TempDir::new().unwrap();
    for (name, prefix) in fixture {
        let base = fixtures().join(name);
        let mut rels = Vec::new();
        walk(&base, &base, &mut rels);
        for rel in rels.iter().filter(|r| !r.starts_with("truth/")) {
            let to = [*prefix, rel.as_str()]
                .into_iter()
                .filter(|s| !s.is_empty())
                .collect::<Vec<_>>()
                .join("/");
            write(
                dir.path(),
                &to,
                &fs::read_to_string(base.join(rel)).unwrap(),
            );
        }
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

fn single(name: &str) -> TempDir {
    repo_of(&[(name, "")])
}

/// One resolved edge: (src path, src qualified name or `<file>`, kind, dst path, dst
/// qualified name or `<file>`) → (confidence, reason).
type Edges = BTreeMap<(String, String, String, String, String), (f64, String)>;

fn edges(conn: &Connection) -> Edges {
    let mut st = conn
        .prepare(
            "SELECT a.path, coalesce(s.qualified_name, '<file>'), e.kind,
                    b.path, coalesce(d.qualified_name, '<file>'), e.confidence, e.reason
             FROM edges e JOIN files a ON a.id = e.src_file JOIN files b ON b.id = e.dst_file
             LEFT JOIN symbols s ON s.id = e.src_symbol LEFT JOIN symbols d ON d.id = e.dst_symbol
             WHERE e.layer = 'symbol'",
        )
        .unwrap();
    st.query_map([], |r| {
        Ok((
            (r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?),
            (r.get(5)?, r.get(6)?),
        ))
    })
    .unwrap()
    .map(Result::unwrap)
    .collect()
}

fn has(e: &Edges, from: (&str, &str), kind: &str, to: (&str, &str)) -> Option<(f64, String)> {
    let key = (
        from.0.to_string(),
        from.1.to_string(),
        kind.to_string(),
        to.0.to_string(),
        to.1.to_string(),
    );
    e.get(&key).cloned()
}

#[track_caller]
fn edge(e: &Edges, from: (&str, &str), kind: &str, to: (&str, &str), conf: f64, reason: &str) {
    let got = has(e, from, kind, to)
        .unwrap_or_else(|| panic!("missing {from:?} {kind} {to:?}\nhave: {:#?}", e.keys()));
    assert_eq!(got.0, conf, "confidence of {from:?} {kind} {to:?}");
    assert_eq!(got.1, reason, "reason of {from:?} {kind} {to:?}");
}

/// (path, name, cause) → count
fn drops(conn: &Connection) -> BTreeSet<(String, String, String)> {
    let mut st = conn
        .prepare(
            "SELECT f.path, n.name, r.cause FROM resolve_drops r
             JOIN files f ON f.id = r.file_id JOIN names n ON n.id = r.name_id",
        )
        .unwrap();
    st.query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

fn drop_of(d: &BTreeSet<(String, String, String)>, path: &str, name: &str, cause: &str) -> bool {
    d.contains(&(path.into(), name.into(), cause.into()))
}

#[test]
fn python_binds_imports_relative_paths_namespaces_reexports_and_c3() {
    let r = single("py");
    let rep = index("build", r.path(), &[]);
    let conn = db(&rep);
    let e = edges(&conn);
    let (main, models, util, mro) = ("main.py", "pkg/models.py", "pkg/util.py", "pkg/mro.py");

    // `import pkg.util as u`: a module alias; `from pkg import twice`: a name re-exported
    // by `pkg/__init__.py` through `from .util import *`
    edge(
        &e,
        (main, "run"),
        "CALLS",
        (util, "format_value"),
        0.9,
        "module-import",
    );
    edge(&e, (main, "run"), "CALLS", (util, "twice"), 0.9, "import");
    // a namespace package (`ns/` has no `__init__.py`): `from ns import thing`
    edge(
        &e,
        (main, "run"),
        "CALLS",
        ("ns/thing.py", "make"),
        0.9,
        "module-import",
    );
    // `from pkg import Child` binds through the package's re-export to its class
    edge(
        &e,
        (main, "<file>"),
        "IMPORTS",
        (models, "Child"),
        0.9,
        "py-import",
    );
    // a receiver typed by its constructor (`c = Child()` in a function)
    edge(
        &e,
        (main, "run"),
        "CALLS",
        (models, "Child.run"),
        0.9,
        "receiver-typed",
    );
    // relative imports: `from .util import format_value`, `from . import util`
    edge(
        &e,
        (models, "<file>"),
        "IMPORTS",
        (util, "format_value"),
        0.9,
        "py-import",
    );
    edge(
        &e,
        (mro, "A.base_only"),
        "CALLS",
        (util, "twice"),
        0.9,
        "module-import",
    );
    edge(
        &e,
        (mro, "<file>"),
        "IMPORTS",
        (util, "<file>"),
        0.9,
        "py-import",
    );
    // `self.greet()` in a subclass: the base's method, found on the enclosing type
    edge(
        &e,
        (models, "Child.run"),
        "CALLS",
        (models, "Base.shout"),
        0.95,
        "this",
    );
    edge(
        &e,
        (models, "Child"),
        "EXTENDS",
        (models, "Base"),
        0.95,
        "same-file",
    );
    edge(
        &e,
        (models, "Base"),
        "HAS_METHOD",
        (models, "Base.greet"),
        0.95,
        "declaration",
    );
    // C3: D(B, C) with `who` on A and C is searched D, B, C, A: C wins (a depth-first
    // first-wins search would find A.who first)
    edge(
        &e,
        (mro, "probe"),
        "CALLS",
        (mro, "C.who"),
        0.95,
        "receiver-typed",
    );
    assert!(
        has(&e, (mro, "probe"), "CALLS", (mro, "A.who")).is_none(),
        "{e:#?}"
    );
    edge(
        &e,
        (mro, "probe"),
        "CALLS",
        (mro, "A.base_only"),
        0.95,
        "receiver-typed",
    );

    // an untyped receiver is a boundary, not an edge
    let d = drops(&conn);
    assert!(drop_of(&d, models, "upper", "receiver_untyped"), "{d:#?}");

    // every edge is a scope proof: no lexical (name-only) edge in this fixture
    let provenance: BTreeSet<String> = conn
        .prepare("SELECT DISTINCT provenance FROM edges WHERE layer = 'symbol'")
        .unwrap()
        .query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect();
    assert_eq!(provenance, BTreeSet::from(["scope".to_string()]));
}

#[test]
fn java_binds_packages_static_imports_inheritance_interfaces_and_overloads() {
    let r = single("java");
    let rep = index("build", r.path(), &[]);
    let conn = db(&rep);
    let e = edges(&conn);
    let j = "src/main/java/com/acme/";
    let f = |n: &str| format!("{j}{n}");
    let (main, circle, base, shape, strings) = (
        f("app/Main.java"),
        f("Circle.java"),
        f("Base.java"),
        f("Shape.java"),
        f("util/Strings.java"),
    );

    // single-type import, package wildcard (`com.acme.util.*`) and static import
    edge(
        &e,
        (&main, "<file>"),
        "IMPORTS",
        (&circle, "Circle"),
        0.9,
        "java-import",
    );
    edge(
        &e,
        (&main, "<file>"),
        "IMPORTS",
        (&strings, "Strings.twice"),
        0.9,
        "java-import",
    );
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&strings, "Strings.twice"),
        0.9,
        "import",
    );
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&strings, "Strings.pad"),
        0.9,
        "static-member",
    );
    edge(
        &e,
        (&main, "Main.run"),
        "USES",
        (&strings, "Strings"),
        0.9,
        "wildcard-import",
    );
    // a receiver typed by its declaration; a method inherited from the superclass
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&circle, "Circle.describe"),
        0.9,
        "receiver-typed",
    );
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&base, "Base.greet"),
        0.9,
        "receiver-typed",
    );
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&circle, "Circle.unit"),
        0.9,
        "static-member",
    );
    // an interface-typed receiver binds to the interface member, a dispatch boundary
    edge(
        &e,
        (&main, "Main.run"),
        "CALLS",
        (&shape, "Shape.area"),
        0.9,
        "receiver-typed",
    );
    // implicit `this`: members of the class and of its superclass, same package
    edge(
        &e,
        (&circle, "Circle.describe"),
        "CALLS",
        (&circle, "Circle.area"),
        0.95,
        "implicit-this",
    );
    edge(
        &e,
        (&circle, "Circle.describe"),
        "CALLS",
        (&base, "Base.greet"),
        0.9,
        "implicit-this",
    );
    edge(
        &e,
        (&circle, "Circle"),
        "EXTENDS",
        (&base, "Base"),
        0.9,
        "same-package",
    );
    edge(
        &e,
        (&base, "Base"),
        "IMPLEMENTS",
        (&shape, "Shape"),
        0.9,
        "same-package",
    );
    edge(
        &e,
        (&strings, "Strings.twice"),
        "CALLS",
        (&strings, "Strings.pad"),
        0.95,
        "implicit-this",
    );

    // the overloaded `scale` binds to a declaration but is recorded as ambiguous
    edge(
        &e,
        (&circle, "Circle.describe"),
        "CALLS",
        (&circle, "Circle.scale"),
        0.95,
        "implicit-this",
    );
    let d = drops(&conn);
    assert!(drop_of(&d, &circle, "scale", "ambiguous"), "{d:#?}");
    assert!(drop_of(&d, &main, "area", "dispatch_boundary"), "{d:#?}");
}

#[test]
fn rust_binds_use_paths_trait_impls_and_associated_functions() {
    let r = single("rust");
    let rep = index("build", r.path(), &[]);
    let conn = db(&rep);
    let e = edges(&conn);
    let (app, shapes, util) = ("src/app.rs", "src/shapes.rs", "src/util.rs");

    // `use crate::shapes::{Circle, Shape, Square}`, `use crate::util`, an aliased `use`
    edge(
        &e,
        (app, "<file>"),
        "IMPORTS",
        (shapes, "Circle"),
        0.9,
        "use",
    );
    edge(&e, (app, "<file>"), "IMPORTS", (util, "helper"), 0.9, "use");
    edge(&e, (app, "run"), "CALLS", (util, "helper"), 0.9, "import");
    // `util::twice` through the module `use crate::util` names
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (util, "twice"),
        0.9,
        "module-import",
    );
    // `Circle::new` and a local typed by it: `c.grow()`, `c.area()`
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (shapes, "Circle.new"),
        0.9,
        "static-member",
    );
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (shapes, "Circle.grow"),
        0.9,
        "receiver-typed",
    );
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (shapes, "Circle.area"),
        0.9,
        "receiver-typed",
    );
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (shapes, "Square.area"),
        0.9,
        "receiver-typed",
    );
    // `c.describe()` is the trait's default method, found through `impl Shape for Circle`
    edge(
        &e,
        (app, "run"),
        "CALLS",
        (shapes, "Shape.describe"),
        0.9,
        "receiver-typed",
    );
    // `self.area()` in a trait default method: the trait's own (dispatched) member
    edge(
        &e,
        (shapes, "Shape.describe"),
        "CALLS",
        (shapes, "Shape.area"),
        0.95,
        "this",
    );
    // an impl block's methods and trait belong to the type
    edge(
        &e,
        (shapes, "Circle"),
        "HAS_METHOD",
        (shapes, "Circle.new"),
        0.95,
        "declaration",
    );
    edge(
        &e,
        (shapes, "Circle"),
        "HAS_METHOD",
        (shapes, "Circle.area"),
        0.95,
        "declaration",
    );
    edge(
        &e,
        (shapes, "Circle"),
        "IMPLEMENTS",
        (shapes, "Shape"),
        0.95,
        "same-file",
    );
    edge(
        &e,
        (shapes, "Circle.grow"),
        "ACCESSES",
        (shapes, "Circle.r"),
        0.95,
        "this",
    );
    // `pub mod shapes;` in lib.rs loads the module's file
    edge(
        &e,
        ("src/lib.rs", "<file>"),
        "IMPORTS",
        (shapes, "<file>"),
        0.9,
        "use",
    );

    let d = drops(&conn);
    assert!(drop_of(&d, shapes, "area", "dispatch_boundary"), "{d:#?}");
    assert!(drop_of(&d, app, "len", "receiver_untyped"), "{d:#?}");
}

/// Resolver CALLS edges between callables against ground truth, per language: Python's is
/// the calls that actually run (`sys.setprofile`); Java's and Rust's are the callee each
/// call binds to by the language rules, checked to compile with javac and rustc.
/// Precision is what EVAL-09 records; recall is reported alongside it.
/// A repository of `(path, content)` files, built and indexed.
fn built(files: &[(&str, &str)]) -> (TempDir, Connection) {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    let conn = db(&index("build", dir.path(), &[]));
    (dir, conn)
}

#[test]
fn rust_same_name_types_keep_their_own_methods_and_traits() {
    let (_r, conn) = built(&[
        (
            "Cargo.toml",
            "[package]\nname = \"x\"\nversion = \"0.1.0\"\n",
        ),
        (
            "src/lib.rs",
            "pub mod a;\npub mod b;\npub mod app;\npub mod shapes;\npub mod shape_impl;\n",
        ),
        (
            "src/a.rs",
            "pub trait Named { fn label(&self) -> u8; }\npub struct Config {}\nimpl Config { pub fn fresh() {} }\n",
        ),
        (
            "src/b.rs",
            "pub trait Named { fn label(&self) -> u8; }\npub struct Config {}\nimpl Config { pub fn reload(&self) {} }\nimpl Named for Config { fn label(&self) -> u8 { 1 } }\n",
        ),
        (
            "src/app.rs",
            "use crate::a::Config;\npub fn run(c: Config) { c.reload(); c.label(); }\n",
        ),
        ("src/shapes.rs", "pub struct Circle { pub r: f64 }\n"),
        (
            "src/shape_impl.rs",
            "use crate::shapes::Circle;\nimpl Circle {\n  pub fn make() -> Circle { Circle { r: 1.0 } }\n  pub fn scale(&self) -> f64 { self.r }\n}\n",
        ),
    ]);
    let e = edges(&conn);
    // a::Config has neither `reload` nor `label`: nothing binds them to b::Config
    let wrong = |to: &str| {
        e.keys()
            .any(|k| k.0 == "src/app.rs" && k.2 == "CALLS" && k.3 == "src/b.rs" && k.4 == to)
    };
    assert!(!wrong("Config.reload"), "{:#?}", e.keys());
    assert!(!wrong("Config.label"), "{:#?}", e.keys());
    // an impl block in another file: the methods belong to the struct, and `Circle { r }`
    // names the struct
    let owner: String = conn
        .query_row(
            "SELECT s.kind FROM edges e JOIN symbols s ON s.id = e.src_symbol
             JOIN symbols d ON d.id = e.dst_symbol
             WHERE e.kind = 'HAS_METHOD' AND d.qualified_name = 'Circle.make'",
            [],
            |r| r.get(0),
        )
        .unwrap();
    assert_eq!(owner, "Struct");
    edge(
        &e,
        ("src/shape_impl.rs", "Circle.make"),
        "USES",
        ("src/shapes.rs", "Circle"),
        0.9,
        "import",
    );
}

#[test]
fn java_single_type_import_shadows_the_same_package() {
    let (_r, conn) = built(&[
        (
            "src/com/acme/util/Strings.java",
            "package com.acme.util;\npublic class Strings { public static String shout(String s) { return s; } }\n",
        ),
        (
            "src/com/acme/app/Strings.java",
            "package com.acme.app;\npublic class Strings { public static String shout(String s) { return s; } }\n",
        ),
        (
            "src/com/acme/app/Main.java",
            "package com.acme.app;\nimport com.acme.util.Strings;\npublic class Main { void main() { Strings.shout(\"x\"); } }\n",
        ),
        (
            "src/com/acme/app/Other.java",
            "package com.acme.app;\npublic class Other { void go() { Strings.shout(\"x\"); } }\n",
        ),
    ]);
    let e = edges(&conn);
    let main = "src/com/acme/app/Main.java";
    assert!(
        has(
            &e,
            (main, "Main.main"),
            "CALLS",
            ("src/com/acme/util/Strings.java", "Strings.shout")
        )
        .is_some(),
        "{:#?}",
        e.keys()
    );
    assert!(
        has(
            &e,
            (main, "Main.main"),
            "CALLS",
            ("src/com/acme/app/Strings.java", "Strings.shout")
        )
        .is_none()
    );
    // without an import the file's own package binds
    edge(
        &e,
        ("src/com/acme/app/Other.java", "Other.go"),
        "CALLS",
        ("src/com/acme/app/Strings.java", "Strings.shout"),
        0.9,
        "static-member",
    );
}

#[test]
fn python_last_import_wins_and_a_package_does_not_import_its_own_siblings_as_roots() {
    let (_r, conn) = built(&[
        ("app/__init__.py", ""),
        ("app/first.py", "def helper():\n    return 1\n"),
        ("app/second.py", "def helper():\n    return 2\n"),
        ("app/json.py", "def loads(s):\n    return s\n"),
        (
            "app/use.py",
            "from app.first import helper\nfrom app.second import helper\nimport json\n\ndef run():\n    helper()\n    json.loads('1')\n",
        ),
    ]);
    let e = edges(&conn);
    assert!(
        has(
            &e,
            ("app/use.py", "run"),
            "CALLS",
            ("app/second.py", "helper")
        )
        .is_some(),
        "{:#?}",
        e.keys()
    );
    assert!(
        has(
            &e,
            ("app/use.py", "run"),
            "CALLS",
            ("app/first.py", "helper")
        )
        .is_none()
    );
    assert!(has(&e, ("app/use.py", "run"), "CALLS", ("app/json.py", "loads")).is_none());
}

#[test]
fn precision_against_ground_truth() {
    let mut total = (0usize, 0usize, 0usize);
    for lang in ["py", "java", "rust"] {
        let r = single(lang);
        let conn = db(&index("build", r.path(), &[]));
        let ours: BTreeSet<(String, String)> = conn
            .prepare(
                "SELECT a.path || '::' || s.qualified_name, b.path || '::' || d.qualified_name
                 FROM edges e JOIN files a ON a.id = e.src_file JOIN files b ON b.id = e.dst_file
                 JOIN symbols s ON s.id = e.src_symbol JOIN symbols d ON d.id = e.dst_symbol
                 WHERE e.layer = 'symbol' AND e.kind = 'CALLS'
                   AND d.kind IN ('Function', 'Method')",
            )
            .unwrap()
            .query_map([], |r| Ok((r.get(0)?, r.get(1)?)))
            .unwrap()
            .map(Result::unwrap)
            .collect();
        let truth_file = fixtures()
            .join(lang)
            .join(format!("truth/{lang}_calls.json"));
        let truth: BTreeSet<(String, String)> =
            serde_json::from_str::<Vec<(String, String)>>(&fs::read_to_string(truth_file).unwrap())
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
        assert!(recall >= 0.9, "{lang} recall {recall:.3}");
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

#[test]
fn coverage_is_reported_per_language() {
    let r = repo_of(&[("py", "py"), ("java", "java"), ("rust", "rust")]);
    let rep = index("build", r.path(), &[]);
    let langs = &rep["counters"]["resolve"]["languages"];
    for (family, files) in [("python", 6), ("java", 5), ("rust", 4)] {
        assert_eq!(langs[family]["files"], files, "{family}: {langs:#}");
        assert!(langs[family]["edges"].as_u64().unwrap() > 0, "{langs:#}");
    }
    assert_eq!(rep["counters"]["resolve"]["files"], 15);
    // the same trees resolved apart give the same edges: paths under a prefix bind alike
    let apart = edges(&db(&index("build", single("py").path(), &[])));
    let together = edges(&db(&rep));
    let stripped: Edges = together
        .into_iter()
        .filter(|(k, _)| k.0.starts_with("py/"))
        .map(|(k, v)| {
            (
                (
                    k.0.trim_start_matches("py/").into(),
                    k.1,
                    k.2,
                    k.3.trim_start_matches("py/").into(),
                    k.4,
                ),
                v,
            )
        })
        .collect();
    assert_eq!(apart, stripped);
}

#[test]
fn update_after_an_export_or_a_module_change_matches_a_full_build() {
    let r = single("py");
    index("build", r.path(), &[]);
    // rename an export the importers name, and import modules that do not exist yet
    write(
        r.path(),
        "pkg/util.py",
        "def format_value(x):\n    return str(x)\n\n\ndef double(x):\n    return format_value(x) * 2\n",
    );
    write(
        r.path(),
        "main.py",
        "import pkg.util as u\nimport late\nimport sidefx\nfrom pkg import Child, twice\n\n\n\
         def run():\n    late.go()\n    twice(3)\n    return u.double(1)\n",
    );
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    // a module with a symbol, and one with none (only its import edge tells)
    write(r.path(), "late.py", "def go():\n    return 1\n");
    write(r.path(), "sidefx.py", "print('loaded')\n");
    git(r.path(), &["add", "-A"]);
    let rep = index("update", r.path(), &[]);
    assert_eq!(rep["mode"], "incremental");
    let incremental = edges(&db(&rep));
    assert!(
        has(&incremental, ("main.py", "run"), "CALLS", ("late.py", "go")).is_some(),
        "{incremental:#?}"
    );
    assert!(
        has(
            &incremental,
            ("main.py", "<file>"),
            "IMPORTS",
            ("sidefx.py", "<file>")
        )
        .is_some(),
        "{incremental:#?}"
    );
    let digest_after = digest(r.path());
    let full = edges(&db(&index("build", r.path(), &[])));
    assert_eq!(
        digest_after,
        digest(r.path()),
        "update drifted from a full build"
    );
    assert_eq!(incremental, full, "update edges drifted from a full build");

    // removing the modules again unbinds the imports
    fs::remove_file(r.path().join("late.py")).unwrap();
    fs::remove_file(r.path().join("sidefx.py")).unwrap();
    git(r.path(), &["add", "-A"]);
    git(r.path(), &["commit", "-qm", "rm"]);
    let rep = index("update", r.path(), &[]);
    let incremental = edges(&db(&rep));
    let full = edges(&db(&index("build", r.path(), &[])));
    assert_eq!(incremental, full, "delete drifted from a full build");
    assert!(
        has(&full, ("main.py", "run"), "CALLS", ("late.py", "go")).is_none(),
        "{full:#?}"
    );
}
