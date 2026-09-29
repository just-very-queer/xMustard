//! WS-36 contracts of symbol navigation: the name-path grammar (relative, absolute,
//! overload index, substring, kinds, path scope), bodies and doc comments sliced from
//! the working tree and never stored, the ambiguity envelope and refine map, file and
//! directory overviews with depth, and a symbol's context grouped by relation with its
//! reference lines, snippets and epistemic envelope.

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
    fs::write(p, content).unwrap();
}

const STORE_GO: &str = "package svc\n\n// Store keeps entries.\ntype Store struct {\n\titems map[string]int\n}\n\n// Capture records one entry.\n// It returns the new count.\nfunc (s *Store) Capture(key string) int {\n\ts.items[key]++\n\treturn len(s.items)\n}\n\n// Capture is also a free function.\nfunc Capture(key string) int { return NewStore().Capture(key) }\n\nfunc NewStore() *Store { return &Store{items: map[string]int{}} }\n";

/// Go (resolved), TypeScript (resolved) and Python (name matches only).
fn fixture() -> TempDir {
    let dir = TempDir::new().unwrap();
    let root = dir.path();
    write(root, "svc/store.go", STORE_GO);
    write(
        root,
        "svc/server.go",
        "package svc\n\nfunc Serve() int {\n\tst := NewStore()\n\treturn st.Capture(\"a\") + Capture(\"b\")\n}\n",
    );
    write(
        root,
        "web/widget.ts",
        "export class Widget {\n  render(): number { return 1; }\n}\nexport function render(): number { return new Widget().render(); }\n",
    );
    write(
        root,
        "rb/app.rb",
        "def handler(x)\n  x\nend\n\ndef handler(x, y)\n  x + y\nend\n\nclass Service\n  def run\n    handler(1, 2)\n  end\n\n  def helper\n    self.run\n  end\nend\n",
    );
    git(root, &["init", "-q"]);
    git(root, &["config", "user.email", "t@t"]);
    git(root, &["config", "user.name", "t"]);
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", "c"]);
    run(&["index", "build", root.to_str().unwrap()]);
    dir
}

fn run(args: &[&str]) -> Value {
    let out = Command::new(BIN).args(args).output().unwrap();
    assert!(
        out.status.success(),
        "{args:?}: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    serde_json::from_slice(&out.stdout).unwrap()
}

fn nav(sub: &str, root: &Path, flags: &[&str], arg: &str) -> Value {
    let mut args = vec!["symbolgraph", sub];
    args.extend_from_slice(flags);
    args.extend_from_slice(&[root.to_str().unwrap(), "ws", arg]);
    run(&args)
}

fn name_paths(v: &Value, key: &str) -> Vec<String> {
    v[key]
        .as_array()
        .unwrap_or(&Vec::new())
        .iter()
        .map(|s| s["name_path"].as_str().unwrap().to_string())
        .collect()
}

#[test]
fn name_path_grammar_fixtures() {
    let r = fixture();
    let root = r.path();
    // a bare name matches every container, ranked; two symbols is ambiguous
    let all = nav("find-symbol", root, &[], "Capture");
    assert_eq!(all["total"], 2, "{all}");
    assert_eq!(all["ambiguous"], true);
    let mut got = name_paths(&all, "symbols");
    got.sort();
    assert_eq!(got, ["Capture", "Store/Capture"]);
    for s in all["symbols"].as_array().unwrap() {
        assert!(s["uid"].as_str().unwrap().contains(":svc/store.go:"), "{s}");
        assert!(s["relevance"].as_f64().unwrap() > 0.9, "{s}");
        assert!(s.get("body").is_none() && s.get("info").is_none(), "{s}");
    }
    assert_eq!(all["freshness"]["source"], "index", "{all}");
    // relative: the container chain ends with Store
    let rel = nav("find-symbol", root, &[], "Store/Capture");
    assert_eq!(name_paths(&rel, "symbols"), ["Store/Capture"]);
    assert_eq!(rel["ambiguous"], false);
    // absolute: the top-level Capture only
    let abs = nav("find-symbol", root, &[], "/Capture");
    assert_eq!(name_paths(&abs, "symbols"), ["Capture"]);
    // overload index: the second handler in source order
    let second = nav("find-symbol", root, &[], "handler[1]");
    assert_eq!(second["total"], 1, "{second}");
    assert_eq!(second["symbols"][0]["lines"][0], 5, "{second}");
    let first = nav("find-symbol", root, &[], "handler[0]");
    assert_eq!(first["symbols"][0]["lines"][0], 1, "{first}");
    // substring, case-insensitive, on the last segment
    let sub = nav("find-symbol", root, &["--substring"], "captur");
    assert_eq!(sub["total"], 2, "{sub}");
    assert!(nav("find-symbol", root, &[], "captur")["total"] == 0);
    // kinds include and exclude
    let only_fn = nav("find-symbol", root, &["--kinds=-Method"], "Capture");
    assert_eq!(name_paths(&only_fn, "symbols"), ["Capture"]);
    let only_method = nav("find-symbol", root, &["--kinds=method"], "Capture");
    assert_eq!(name_paths(&only_method, "symbols"), ["Store/Capture"]);
    // path scope
    let web = nav("find-symbol", root, &["--path-glob=web/**"], "render");
    assert!(web["total"].as_u64().unwrap() >= 1, "{web}");
    assert!(
        web["symbols"]
            .as_array()
            .unwrap()
            .iter()
            .all(|s| s["path"] == "web/widget.ts")
    );
    // depth lists children
    let deep = nav("find-symbol", root, &["--depth=1"], "Service");
    assert_eq!(
        name_paths(&deep["symbols"][0], "children"),
        ["Service/run", "Service/helper"]
    );
}

#[test]
fn bodies_are_sliced_from_the_working_tree() {
    let r = fixture();
    let root = r.path();
    let v = nav(
        "find-symbol",
        root,
        &["--include-body", "--include-info"],
        "Store/Capture",
    );
    let s = &v["symbols"][0];
    assert!(
        s["body"].as_str().unwrap().contains("s.items[key]++"),
        "{s}"
    );
    assert!(
        s["body"]
            .as_str()
            .unwrap()
            .starts_with("func (s *Store) Capture"),
        "{s}"
    );
    assert!(
        s["info"]["signature"]
            .as_str()
            .unwrap()
            .starts_with("func (s *Store) Capture(key string) int"),
        "{s}"
    );
    assert!(
        s["info"]["doc"]
            .as_str()
            .unwrap()
            .contains("Capture records one entry."),
        "{s}"
    );
    // an edit the index has not seen withholds the body instead of showing stale bytes
    write(
        root,
        "svc/store.go",
        &STORE_GO.replace("s.items[key]++", "s.items[key] += 2"),
    );
    let v = nav("find-symbol", root, &["--include-body"], "Store/Capture");
    let s = &v["symbols"][0];
    assert!(s.get("body").is_none(), "{s}");
    assert_eq!(s["unavailable"], "file changed since it was indexed");
    // the store holds no body text under the default retention
    let db = run(&["index", "stats", root.to_str().unwrap()]);
    let path = db["index_path"].as_str().unwrap().to_string();
    let conn = rusqlite::Connection::open(path).unwrap();
    let stored: i64 = conn
        .query_row("SELECT count(*) FROM chunk_text", [], |r| r.get(0))
        .unwrap();
    assert_eq!(stored, 0);
}

#[test]
fn ambiguous_names_return_ranked_candidates_and_a_refine_map() {
    let r = fixture();
    let v = nav("find-symbol", r.path(), &["--max-matches=1"], "Capture");
    assert_eq!(v["total"], 2, "{v}");
    assert_eq!(v["total_candidates"], 2);
    assert_eq!(v["candidates_truncated"], true);
    assert_eq!(v["symbols"].as_array().unwrap().len(), 1);
    assert_eq!(v["omitted"], 1);
    let mut refine: Vec<String> = v["refine"]["svc/store.go"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap().to_string())
        .collect();
    refine.sort();
    assert_eq!(refine, ["Capture", "Store/Capture"]);
    let next = nav(
        "find-symbol",
        r.path(),
        &["--max-matches=1", "--offset=1"],
        "Capture",
    );
    assert_eq!(next["symbols"].as_array().unwrap().len(), 1);
    assert_ne!(next["symbols"][0]["uid"], v["symbols"][0]["uid"]);
    // the context of an ambiguous name lists candidates with their UIDs
    let ctx = nav("context", r.path(), &[], "Capture");
    assert_eq!(ctx["found"], false, "{ctx}");
    assert_eq!(ctx["ambiguous"], true);
    assert_eq!(ctx["candidates"].as_array().unwrap().len(), 2);
}

#[test]
fn file_and_directory_overviews() {
    let r = fixture();
    let root = r.path();
    let f = nav("overview", root, &["--depth=1"], "svc/store.go");
    assert_eq!(f["kind"], "file", "{f}");
    assert_eq!(f["depth"], 1);
    assert_eq!(f["symbols_by_kind"]["Function"], 2, "{f}");
    let store = f["symbols"]
        .as_array()
        .unwrap()
        .iter()
        .find(|s| s["name_path"] == "Store")
        .unwrap();
    assert_eq!(
        name_paths(store, "children"),
        ["Store/items", "Store/Capture"],
        "{f}"
    );
    assert!(store["lines"][0].as_u64().unwrap() <= 4);
    let flat = nav("overview", root, &["--depth=0"], "svc/store.go");
    assert!(
        flat["symbols"]
            .as_array()
            .unwrap()
            .iter()
            .all(|s| s.get("children").is_none()),
        "{flat}"
    );
    let d = nav("overview", root, &[], "svc");
    assert_eq!(d["kind"], "directory", "{d}");
    assert_eq!(d["files_total"], 2);
    let files: Vec<&str> = d["files"]
        .as_array()
        .unwrap()
        .iter()
        .map(|f| f["path"].as_str().unwrap())
        .collect();
    assert_eq!(files, ["svc/server.go", "svc/store.go"]);
    assert!(
        d["files"][1]["symbols_by_kind"]["Function"]
            .as_u64()
            .unwrap()
            >= 2
    );
    let top = nav("overview", root, &[], ".");
    let dirs: Vec<&str> = top["dirs"]
        .as_array()
        .unwrap()
        .iter()
        .map(|x| x["path"].as_str().unwrap())
        .collect();
    assert_eq!(dirs, ["rb", "svc", "web"], "{top}");
    assert_eq!(nav("overview", root, &[], "nope")["kind"], "not_indexed");
    assert_eq!(nav("overview", root, &[], "sv")["kind"], "not_indexed");
}

#[test]
fn context_groups_references_by_relation() {
    let r = fixture();
    let root = r.path();
    let ctx = nav("context", root, &["--include-content"], "Store/Capture");
    assert_eq!(ctx["found"], true, "{ctx}");
    assert!(ctx["symbol"]["content"].is_null());
    assert!(
        ctx["symbol"]["body"]
            .as_str()
            .unwrap()
            .contains("s.items[key]++"),
        "{ctx}"
    );
    let serve = ctx["incoming"]
        .as_array()
        .unwrap()
        .iter()
        .find(|e| e["name_path"] == "Serve")
        .unwrap_or_else(|| panic!("Serve calls Store.Capture: {ctx}"));
    assert_eq!(serve["relation"], "CALLS");
    assert_eq!(serve["layer"], 1);
    assert_eq!(serve["line"], 5, "{serve}");
    assert!(
        serve["snippet"]
            .as_str()
            .unwrap()
            .contains("5: \treturn st.Capture"),
        "{serve}"
    );
    assert_eq!(serve["provenance"], "scope");
    assert!(ctx["incoming_by_relation"]["CALLS"].as_u64().unwrap() >= 1);
    assert_eq!(ctx["epistemic"]["resolved_language"], true);
    // by UID
    let uid = ctx["symbol"]["uid"].as_str().unwrap().to_string();
    let by_uid = nav("context", root, &[], &uid);
    assert_eq!(by_uid["symbol"]["uid"], uid.as_str());
    // outgoing and deeper layers
    let chain = nav("context", root, &["--chain-depth=1"], "/Capture");
    assert!(
        chain["outgoing"]
            .as_array()
            .unwrap()
            .iter()
            .any(|e| e["name_path"] == "NewStore"),
        "{chain}"
    );
    assert!(
        chain["incoming"]
            .as_array()
            .unwrap()
            .iter()
            .all(|e| e["layer"].as_u64().unwrap() <= 2)
    );
    // a language without the resolver (Ruby): name matches, a lower bound
    let py = nav("context", root, &[], "Service/run");
    let helper = py["incoming"]
        .as_array()
        .unwrap()
        .iter()
        .find(|e| e["name_path"] == "Service/helper")
        .unwrap_or_else(|| panic!("helper calls run: {py}"));
    assert_eq!(helper["provenance"], "lexical");
    assert_eq!(helper["relation"], "CALLS");
    assert_eq!(py["epistemic"]["completeness"], "lower_bound");
    assert_eq!(py["epistemic"]["resolved_language"], false);
    assert_eq!(nav("context", root, &[], "Nope")["found"], false);
    // a type's members are its outgoing HAS_METHOD and HAS_PROPERTY edges
    let store = nav("context", root, &[], "/Store");
    let rel = |name: &str| -> Vec<String> {
        store["outgoing"]
            .as_array()
            .unwrap()
            .iter()
            .filter(|e| e["name_path"] == name)
            .map(|e| e["relation"].as_str().unwrap().to_string())
            .collect()
    };
    assert!(
        rel("Store/Capture").contains(&"HAS_METHOD".to_string()),
        "{store}"
    );
    assert!(
        rel("Store/items").contains(&"HAS_PROPERTY".to_string()),
        "{store}"
    );
    assert!(
        store["outgoing_by_relation"]["HAS_METHOD"]
            .as_u64()
            .unwrap()
            >= 1
    );
}
