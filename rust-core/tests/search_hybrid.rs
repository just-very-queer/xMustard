//! WS-18 contracts of hybrid search: the BM25 lane over function-aligned chunks finds
//! bodies, the docs lane is served from the index, hits carry bounded line-numbered
//! snippets and deterministic reasons, pages concatenate to the one-shot ranking, and a
//! query reads only the chunks it ranks.

use std::fs;
use std::path::Path;
use std::process::Command;

use serde_json::Value;
use tempfile::TempDir;
use xmustard_core::index::config::{ContentRetention, IndexConfig};
use xmustard_core::index::{self, fts, rerank};
use xmustard_core::search::{self, SearchOptions, SearchResult};

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

fn repo(files: &[(&str, &str)]) -> TempDir {
    let dir = TempDir::new().unwrap();
    for (rel, content) in files {
        write(dir.path(), rel, content);
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    commit(dir.path());
    dir
}

fn commit(root: &Path) {
    git(root, &["add", "-A"]);
    git(root, &["commit", "-qm", "c"]);
}

fn build(root: &Path, retention: ContentRetention) {
    let mut cfg = IndexConfig::load(root).unwrap();
    cfg.content_retention = retention;
    index::build(root, &cfg).unwrap();
}

fn update(root: &Path) -> index::IndexReport {
    let cfg = IndexConfig::load(root).unwrap();
    index::update(root, &cfg, None).unwrap()
}

fn run(root: &Path, query: &str, opts: SearchOptions<'_>) -> SearchResult {
    search::search(root, "ws", None, query, &opts)
}

fn query(root: &Path, q: &str, limit: usize) -> SearchResult {
    run(
        root,
        q,
        SearchOptions {
            limit,
            ..Default::default()
        },
    )
}

/// A small service: the retry and bucket bodies hold words their names do not.
fn fixture() -> TempDir {
    repo(&[
        (
            "store/retry.go",
            "package store\n\nimport \"time\"\n\n// RetryWithBackoff calls fn until it succeeds.\nfunc RetryWithBackoff(attempts int, base time.Duration, fn func() error) error {\n\tvar err error\n\tdelay := base\n\tfor i := 0; i < attempts; i++ {\n\t\tif err = fn(); err == nil {\n\t\t\treturn nil\n\t\t}\n\t\ttime.Sleep(delay)\n\t\tdelay *= 2\n\t}\n\treturn err\n}\n",
        ),
        (
            "ratelimit/bucket.go",
            "package ratelimit\n\n// TokenBucket is a rate limiter.\ntype TokenBucket struct {\n\ttokens int\n}\n\n// Allow consumes one token.\nfunc (b *TokenBucket) Allow() bool {\n\tif b.tokens == 0 {\n\t\treturn false\n\t}\n\tb.tokens--\n\treturn true\n}\n",
        ),
        (
            "web/src/debounce.ts",
            "// debounce delays calls to fn.\nexport function debounce(fn: () => void, wait: number) {\n  let timer: ReturnType<typeof setTimeout> | undefined;\n  return () => {\n    if (timer) clearTimeout(timer);\n    timer = setTimeout(() => fn(), wait);\n  };\n}\n",
        ),
        (
            "docs/OPERATIONS.md",
            "# Operations\n\nRun the service behind a load balancer.\n\n## Retry policy\n\nSync jobs retry with a doubling delay, five attempts at most.\n",
        ),
        (
            "README.md",
            "# Fixture\n\nA tiny service for search tests.\n",
        ),
    ])
}

fn names(res: &SearchResult) -> Vec<(String, String)> {
    res.hits
        .iter()
        .map(|h| (h.path.clone(), h.name.clone()))
        .collect()
}

/// Body-term queries find the function whose name holds none of the terms.
#[test]
fn body_terms_find_functions_by_their_bodies() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    for (q, path, name) in [
        ("time.Sleep delay", "store/retry.go", "RetryWithBackoff"),
        ("clearTimeout setTimeout", "web/src/debounce.ts", "debounce"),
        ("return false when empty", "ratelimit/bucket.go", "Allow"),
    ] {
        let res = query(r.path(), q, 5);
        let top = &res.hits[0];
        assert_eq!(
            (top.path.as_str(), top.name.as_str()),
            (path, name),
            "{q}: {:?}",
            names(&res)
        );
        assert!(
            top.lanes_matched.contains(&"bm25"),
            "{q}: {:?}",
            top.lanes_matched
        );
        assert!(
            !top.lanes_matched.contains(&"name"),
            "{q}: the name holds no term"
        );
        assert!(top.uid.as_deref().is_some_and(|u| !u.is_empty()));
        assert!(top.scores["bm25"] > 0.0 && top.scores["rrf"] > 0.0);
    }
}

/// Snippets are bounded (at most three windows of ±1 line), line-numbered, clipped to
/// the chunk, and point at the lines that matched.
#[test]
fn snippets_are_bounded_line_numbered_and_on_the_match() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    let res = query(r.path(), "time.Sleep delay", 5);
    let top = &res.hits[0];
    // the declaration's span includes its doc comment
    assert_eq!(top.lines, Some([5, 17]));
    assert!(!top.snippet.is_empty() && top.snippet.len() <= rerank::MAX_SNIPPETS);
    for s in &top.snippet {
        let lines: Vec<&str> = s.text.lines().collect();
        assert!(lines.len() <= 3, "{:?}", s.text);
        for (k, l) in lines.iter().enumerate() {
            let n: u32 = l.split(':').next().unwrap().parse().unwrap();
            assert!((5..=17).contains(&n), "{l}");
            assert_eq!(n, s.line - 1 + k as u32 + u32::from(s.line == 5), "{l}");
        }
    }
    assert!(
        top.snippet
            .iter()
            .any(|s| s.text.contains("13: \t\ttime.Sleep(delay)")),
        "{:?}",
        top.snippet
    );
    // every returned hit stays within the per-hit bound
    let wide = query(r.path(), "delay token timer retry", 25);
    for h in &wide.hits {
        assert!(h.snippet.len() <= rerank::MAX_SNIPPETS);
        assert!(h.snippet.iter().all(|s| s.text.lines().count() <= 3));
    }
}

/// reasons[] explain every hit, and the same query renders the same bytes.
#[test]
fn reasons_are_present_and_deterministic() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    let render = |res: &SearchResult| {
        let mut v: Value = serde_json::to_value(res).unwrap();
        v.as_object_mut().unwrap().remove("generated_at");
        v.as_object_mut().unwrap().remove("freshness");
        serde_json::to_string(&v).unwrap()
    };
    let a = query(r.path(), "retry delay token", 25);
    let b = query(r.path(), "retry delay token", 25);
    assert_eq!(render(&a), render(&b));
    assert!(!a.hits.is_empty());
    for h in &a.hits {
        assert!(!h.reasons.is_empty(), "{h:?}");
        assert_eq!(h.reason, h.reasons.join(" · "));
        assert!(!h.lanes_matched.is_empty());
        for lane in &h.lanes_matched {
            assert!(h.scores.contains_key(lane), "{lane} has no score: {h:?}");
        }
    }
    let exact = query(r.path(), "RetryWithBackoff", 5);
    assert_eq!(
        exact.hits[0].reasons[0],
        "exact declaration match: RetryWithBackoff"
    );
    // the hashing lane is named for what it is
    assert!(!render(&a).contains("semantic"));
}

/// Pages concatenate to the one-shot ranking without repeats, and omitted counts what
/// is left after each page.
#[test]
fn pages_concatenate_to_the_one_shot_ranking() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    let q = "retry delay token timer bucket";
    let all = query(r.path(), q, 50);
    assert!(all.hits.len() >= 6, "{:?}", names(&all));
    let mut paged = Vec::new();
    let mut offset = 0;
    loop {
        let page = run(
            r.path(),
            q,
            SearchOptions {
                limit: 3,
                offset,
                ..Default::default()
            },
        );
        assert_eq!(page.total, all.total);
        assert_eq!(page.offset, offset);
        paged.extend(
            page.hits
                .iter()
                .map(|h| (h.path.clone(), h.name.clone(), h.line)),
        );
        offset += page.hits.len();
        assert_eq!(page.omitted, all.total - offset);
        if page.omitted == 0 || page.hits.is_empty() {
            break;
        }
    }
    let one_shot: Vec<_> = all
        .hits
        .iter()
        .map(|h| (h.path.clone(), h.name.clone(), h.line))
        .collect();
    assert_eq!(paged[..one_shot.len()], one_shot[..]);
}

/// The docs lane answers from the index: a doc edited after indexing still answers
/// with its indexed section (the snippet is withheld, never stale), and a query reads
/// only the chunks it ranks.
#[test]
fn docs_come_from_the_index_and_queries_read_only_ranked_chunks() {
    let mut files: Vec<(String, String)> = (0..40)
        .map(|i| {
            (
                format!("docs/note{i:02}.md"),
                format!("# Note {i}\n\nfiller text about topic{i}\n"),
            )
        })
        .collect();
    files.push((
        "docs/runbook.md".into(),
        "# Runbook\n\n## Failover\n\nPromote the zebracorn replica first.\n".into(),
    ));
    files.push(("lib.rs".into(), "pub fn unrelated() {}\n".into()));
    let refs: Vec<(&str, &str)> = files
        .iter()
        .map(|(a, b)| (a.as_str(), b.as_str()))
        .collect();
    let r = repo(&refs);
    build(r.path(), ContentRetention::Symbol);

    let before = fts::chunk_reads();
    let res = query(r.path(), "zebracorn replica", 10);
    let reads = fts::chunk_reads() - before;
    let doc = res
        .hits
        .iter()
        .find(|h| h.kind == "doc")
        .unwrap_or_else(|| panic!("{:?}", names(&res)));
    assert_eq!(doc.path, "docs/runbook.md");
    assert_eq!(doc.lines, Some([3, 5]));
    assert!(
        doc.reasons.iter().any(|r| r == "docs: doc section"),
        "{:?}",
        doc.reasons
    );
    // one read per ranked chunk at most: never the 40 other docs
    assert!(
        reads <= res.total.min(search::RERANK_K) as u64,
        "{reads} reads for {} candidates",
        res.total
    );

    // an edit the index has not seen: the indexed section still answers, without text
    write(
        r.path(),
        "docs/runbook.md",
        "# Runbook\n\nRewritten entirely.\n",
    );
    let stale = query(r.path(), "zebracorn replica", 10);
    let doc = stale.hits.iter().find(|h| h.kind == "doc").unwrap();
    assert!(doc.snippet.is_empty());
    assert!(
        doc.reasons
            .iter()
            .any(|r| r == "no snippet: file changed since it was indexed"),
        "{:?}",
        doc.reasons
    );

    // the update rewrites only that doc; the old words are gone, the new ones found
    commit(r.path());
    let rep = update(r.path());
    assert_eq!(rep.counters.docs.written, 1, "{:?}", rep.counters.docs);
    assert_eq!(rep.counters.docs.unchanged, 40);
    assert!(
        query(r.path(), "zebracorn", 10)
            .hits
            .iter()
            .all(|h| h.kind != "doc")
    );
    let fresh = query(r.path(), "rewritten entirely", 10);
    assert!(fresh.hits.iter().any(|h| h.path == "docs/runbook.md"));
}

/// A doc-only change never starts a new graph generation, and incremental doc updates
/// leave the same index as a full build.
#[test]
fn doc_updates_match_a_full_build() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    let digest = |root: &Path| {
        let cfg = IndexConfig::load(root).unwrap();
        index::stats(root, &cfg, true).unwrap()["content_digest"]
            .as_str()
            .unwrap()
            .to_string()
    };
    write(
        r.path(),
        "docs/OPERATIONS.md",
        "# Operations\n\n## Scaling\n\nAdd replicas.\n",
    );
    write(r.path(), "docs/NEW.md", "# New\n\nfresh words\n");
    fs::remove_file(r.path().join("README.md")).unwrap();
    commit(r.path());
    let rep = update(r.path());
    assert_eq!(rep.mode, "noop", "code did not change: {rep:?}");
    assert_eq!(
        (rep.counters.docs.written, rep.counters.docs.deleted),
        (2, 1)
    );
    let incremental = digest(r.path());
    build(r.path(), ContentRetention::Symbol);
    assert_eq!(incremental, digest(r.path()));
}

/// path_glob keeps only matching paths, in every lane.
#[test]
fn path_glob_filters_every_lane() {
    let r = fixture();
    build(r.path(), ContentRetention::Symbol);
    let res = run(
        r.path(),
        "retry delay token timer",
        SearchOptions {
            limit: 25,
            path_glob: Some("**/*.go"),
            ..Default::default()
        },
    );
    assert!(!res.hits.is_empty());
    assert!(
        res.hits.iter().all(|h| h.path.ends_with(".go")),
        "{:?}",
        names(&res)
    );
}

/// With `content_retention=none` the docs lane indexes no prose.
#[test]
fn retention_none_indexes_no_prose() {
    let r = fixture();
    build(r.path(), ContentRetention::None);
    let res = query(r.path(), "load balancer", 10);
    assert!(
        res.hits.iter().all(|h| h.kind != "doc"),
        "{:?}",
        names(&res)
    );
}

// ---------------------------------------------------------------------------
// BM25 cache measurement (resident service, real corpora)
// ---------------------------------------------------------------------------

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

/// Resident set size of a process in bytes (`ps`, the gate's basis).
fn rss_of(pid: u32) -> u64 {
    let out = Command::new("ps")
        .args(["-o", "rss=", "-p", &pid.to_string()])
        .output()
        .unwrap();
    String::from_utf8_lossy(&out.stdout)
        .trim()
        .parse::<u64>()
        .unwrap_or(0)
        * 1024
}

/// A resident `serve` process answering framed JSON-RPC calls one at a time.
struct Serve {
    child: std::process::Child,
    stdin: std::process::ChildStdin,
    stdout: std::io::BufReader<std::process::ChildStdout>,
    next: i64,
}

impl Serve {
    fn start(index_dir: &Path, text_lanes: &str) -> Serve {
        use std::process::Stdio;
        let mut child = Command::new(BIN)
            .args(["serve", "--max-inflight=1", "--trim-idle-ms=0"])
            .env("XMUSTARD_INDEX_DIR", index_dir)
            .env("XMUSTARD_SEARCH_TEXT_LANES", text_lanes)
            // as the Go supervisor starts the resident worker: one glibc arena
            .env("MALLOC_ARENA_MAX", "1")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take().unwrap();
        let stdout = std::io::BufReader::new(child.stdout.take().unwrap());
        let mut s = Serve {
            child,
            stdin,
            stdout,
            next: 0,
        };
        s.call("initialize", &[]);
        s
    }

    fn call(&mut self, method: &str, args: &[&str]) -> Value {
        use std::io::Read;
        use xmustard_core::serve::{read_header, write_frame};
        self.next += 1;
        let body = serde_json::json!({"jsonrpc": "2.0", "id": self.next, "method": method, "params": {"args": args}});
        write_frame(
            &mut self.stdin,
            Some(self.next),
            &[body.to_string().as_bytes()],
        )
        .unwrap();
        let header = read_header(&mut self.stdout).unwrap();
        let mut buf = vec![0; header.len];
        self.stdout.read_exact(&mut buf).unwrap();
        let v: Value = serde_json::from_slice(&buf).unwrap();
        assert!(v.get("error").is_none(), "{v}");
        v["result"].clone()
    }
}

impl Drop for Serve {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// Queries shaped like an agent's: identifiers split into words (as D-02's known-item
/// queries), identifiers as written, and body/prose phrases.
fn corpus_queries(db: &Path) -> Vec<String> {
    let conn = rusqlite::Connection::open(db).unwrap();
    let names: Vec<String> = conn
        .prepare(
            "SELECT n.name FROM symbols s JOIN names n ON n.id = s.name_id
             WHERE length(n.name) >= 8 AND s.local = 0 ORDER BY s.uid LIMIT 4000",
        )
        .unwrap()
        .query_map([], |r| r.get(0))
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let mut out: Vec<String> = names
        .iter()
        .step_by((names.len() / 30).max(1))
        .take(30)
        .enumerate()
        .map(|(i, n)| match i % 2 {
            0 => n.clone(),
            _ => fts::query_terms(n)
                .into_iter()
                .skip(1)
                .collect::<Vec<_>>()
                .join(" "),
        })
        .collect();
    out.extend(
        [
            "retry with exponential backoff",
            "parse json response body",
            "read file contents from disk",
            "format error message",
            "render component props",
            "http request headers",
            "cache invalidation",
            "token expiry refresh",
            "workspace folder path",
            "stream chunk buffer",
        ]
        .map(str::to_string),
    );
    out
}

/// WS-18 acceptance (BM25 cache, 5-8 MiB): the resident service's RSS growth over a
/// query mix on a real corpus, with the text lanes on minus off. Runs when
/// XMUSTARD_PARITY_FIXTURES holds `cline` and `pi-mono` clones (Apache-2.0 / MIT); the
/// index is written to a temporary directory, never into the fixtures. Results are
/// recorded in docs/benchmarks/2026-09-28-ws18-hybrid-search.md.
#[test]
fn resident_bm25_cache_stays_within_its_line() {
    let Some(base) = std::env::var_os("XMUSTARD_PARITY_FIXTURES").map(std::path::PathBuf::from)
    else {
        eprintln!("XMUSTARD_PARITY_FIXTURES unset; skipping the BM25 cache measurement");
        return;
    };
    const MIB: f64 = (1 << 20) as f64;
    for name in ["cline", "pi-mono"] {
        let root = base.join(name);
        if !root.join(".git").exists() {
            eprintln!("{} missing; skipped", root.display());
            continue;
        }
        let dir = TempDir::new().unwrap();
        let out = Command::new(BIN)
            .args(["index", "build", root.to_str().unwrap()])
            .env("XMUSTARD_INDEX_DIR", dir.path())
            .output()
            .unwrap();
        assert!(
            out.status.success(),
            "{}",
            String::from_utf8_lossy(&out.stderr)
        );
        let queries = corpus_queries(&dir.path().join(index::DB_FILE));
        let root_s = root.to_str().unwrap();
        let mut growth = std::collections::BTreeMap::new();
        for lanes in ["off", "on"] {
            let mut s = Serve::start(dir.path(), lanes);
            let start = rss_of(s.child.id());
            let mut peak = start;
            let mut texts = 0usize;
            for round in 0..3 {
                for q in &queries {
                    let v = s.call("search", &[root_s, "ws", q, "25"]);
                    texts += v["hits"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .filter(|h| h["lanes_matched"].to_string().contains("bm25"))
                        .count();
                    peak = peak.max(rss_of(s.child.id()));
                    assert_eq!(v["freshness"]["source"], "resident_index", "{round}");
                }
            }
            let steady = rss_of(s.child.id());
            eprintln!(
                "{name}: text lanes {lanes}: start {:.1} MiB, steady +{:.1}, peak +{:.1} over {} queries x3 ({texts} bm25 hits)",
                start as f64 / MIB,
                steady.saturating_sub(start) as f64 / MIB,
                peak.saturating_sub(start) as f64 / MIB,
                queries.len()
            );
            growth.insert(
                lanes,
                (steady.saturating_sub(start), peak.saturating_sub(start)),
            );
        }
        let (on, off) = (growth["on"], growth["off"]);
        let steady = on.0.saturating_sub(off.0) as f64 / MIB;
        let peak = on.1.saturating_sub(off.1) as f64 / MIB;
        eprintln!("{name}: BM25 cache: steady +{steady:.1} MiB, peak +{peak:.1} MiB");
        assert!(
            peak <= 8.0,
            "{name}: BM25 lanes add {peak:.1} MiB at peak, over the 8 MiB line"
        );
    }
}
