//! Golden fixtures for the language packs (WS-16, PAR-SYM-05).
//!
//! Each `tests/fixtures/lang/<file>` is extracted through the provider contract and
//! rendered (symbols with containers and doc-comment starts, imports, and every
//! non-read reference); the rendering must equal `<file>.golden`. A pack whose grammar
//! feature is off in this build is skipped (its files are lexical, see the coverage
//! test). Regenerate after an intended change with `XM_BLESS=1 cargo test --test
//! lang_fixtures` and review the diff.

use std::fmt::Write as _;
use std::path::{Path, PathBuf};

use xmustard_core::index::extract::{DEFAULT_MAX_PARSE_BYTES, Lang, extract};
use xmustard_core::index::facts::{FileFacts, ref_kind};

const FIXTURES: &[&str] = &[
    "sample.py",
    "Sample.java",
    "sample.c",
    "sample.cpp",
    "Sample.cs",
    "sample.rb",
    "sample.php",
    "sample.kt",
    "sample.swift",
    "sample.sh",
];

fn dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/lang")
}

fn ref_label(kind: u8) -> &'static str {
    match kind {
        ref_kind::CALL => "call",
        ref_kind::TYPE => "type",
        ref_kind::WRITE => "write",
        ref_kind::IMPORT => "import",
        ref_kind::EXTENDS => "extends",
        ref_kind::IMPLEMENTS => "implements",
        ref_kind::BIND => "bind",
        ref_kind::MEMBER => "member",
        ref_kind::MEMBER_CALL => "member_call",
        ref_kind::EXPORT => "export",
        ref_kind::WORD => "word",
        _ => "read",
    }
}

fn render(f: &FileFacts) -> String {
    let mut out = String::new();
    let _ = writeln!(out, "engine {} parse_errors {}", f.engine, f.parse_errors);
    let _ = writeln!(out, "symbols");
    for s in &f.symbols {
        let _ = writeln!(
            out,
            "  {} {} arity={} lines={}-{} doc_from={} exported={} local={} depth={}",
            s.kind,
            s.qualified_name,
            s.arity.map(|a| a.to_string()).unwrap_or("-".into()),
            s.name_line,
            s.end_line,
            s.start_line,
            s.exported,
            s.local,
            s.depth,
        );
    }
    let _ = writeln!(out, "imports");
    for i in &f.imports {
        let _ = writeln!(
            out,
            "  {} {} {} name={} alias={}",
            i.line,
            i.kind,
            i.module,
            i.name.as_deref().unwrap_or("-"),
            i.alias.as_deref().unwrap_or("-"),
        );
    }
    let _ = writeln!(out, "references (non-read)");
    for r in f.refs.iter().filter(|r| r.kind() != ref_kind::READ) {
        let container = r
            .symbol()
            .map(|i| f.symbols[i as usize].qualified_name.as_str())
            .unwrap_or("-");
        let _ = writeln!(
            out,
            "  {} {} {} in {}",
            r.line(),
            ref_label(r.kind()),
            f.names[r.name_idx()],
            container
        );
    }
    out
}

#[test]
fn every_pack_matches_its_golden_fixture() {
    let bless = std::env::var_os("XM_BLESS").is_some();
    let mut checked = 0;
    for name in FIXTURES {
        let lang = Lang::for_path(name).expect("fixture has a pack");
        if !lang.has_grammar() {
            eprintln!("skip {name}: {} pack not compiled in", lang.name());
            continue;
        }
        let text = std::fs::read_to_string(dir().join(name)).unwrap();
        let facts = extract(lang, &text, false, DEFAULT_MAX_PARSE_BYTES);
        assert_eq!(facts.engine, "tree_sitter", "{name}");
        assert!(!facts.parse_errors, "{name} parses cleanly");
        // comments and string literals never yield references
        for word in ["COMMENT_ONLY_WORD", "STRING_ONLY_WORD"] {
            assert!(
                !facts.names.iter().any(|n| n == word),
                "{name}: {word} became a reference"
            );
        }
        assert!(
            !facts.symbols.is_empty() && !facts.imports.is_empty(),
            "{name}"
        );
        let got = render(&facts);
        let golden = dir().join(format!("{name}.golden"));
        if bless {
            std::fs::write(&golden, &got).unwrap();
        }
        let want = std::fs::read_to_string(&golden)
            .unwrap_or_else(|_| panic!("missing {golden:?}; run with XM_BLESS=1"));
        assert_eq!(got, want, "{name} differs from its golden rendering");
        checked += 1;
    }
    eprintln!("checked {checked} golden fixtures");
}

#[test]
fn nested_scopes_qualify_names_and_mark_methods() {
    if !Lang::Python.has_grammar() {
        return;
    }
    let text = std::fs::read_to_string(dir().join("sample.py")).unwrap();
    let f = extract(Lang::Python, &text, false, DEFAULT_MAX_PARSE_BYTES);
    let find = |q: &str| f.symbols.iter().find(|s| s.qualified_name == q).unwrap();
    assert_eq!(find("Outer.method").kind, "Method");
    assert_eq!(find("Outer.Inner.deep").kind, "Method");
    assert_eq!(find("Outer.Inner.deep").depth, 2);
    let local = find("Outer.method.local_fn");
    assert_eq!(local.kind, "Function");
    assert!(local.local && !local.exported);
    assert!(!find("_private").exported);
    // the decorator line opens `top`'s span
    let top = find("top");
    assert!(top.start_line < top.name_line);
}

#[test]
fn a_pack_without_its_grammar_is_lexical() {
    // every pack keeps its lexical patterns, whether or not its grammar is compiled in
    for name in FIXTURES {
        let lang = Lang::for_path(name).unwrap();
        let text = std::fs::read_to_string(dir().join(name)).unwrap();
        // max_parse_bytes 0 forces the lexical path, as a grammar-less build does
        let facts = extract(lang, &text, false, 0);
        assert_eq!(facts.engine, "regex", "{name}");
        assert!(
            !facts.symbols.is_empty(),
            "{name}: lexical patterns found nothing"
        );
        for word in ["COMMENT_ONLY_WORD", "STRING_ONLY_WORD"] {
            assert!(
                !facts.names.iter().any(|n| n == word),
                "{name}: {word} became a reference"
            );
        }
    }
}

// ---- coverage and worker footprint through the real `index build` ----

const BIN: &str = env!("CARGO_BIN_EXE_xmustard-core");

fn git(root: &Path, args: &[&str]) {
    let out = std::process::Command::new("git")
        .arg("-C")
        .arg(root)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "git {args:?}");
}

/// A committed repository holding `files`.
fn repo(files: &[(String, String)]) -> tempfile::TempDir {
    let dir = tempfile::TempDir::new().unwrap();
    for (rel, content) in files {
        let p = dir.path().join(rel);
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, content).unwrap();
    }
    git(dir.path(), &["init", "-q"]);
    git(dir.path(), &["config", "user.email", "t@t"]);
    git(dir.path(), &["config", "user.name", "t"]);
    git(dir.path(), &["add", "-A"]);
    git(dir.path(), &["commit", "-qm", "c"]);
    dir
}

/// One file per pack: the ten fixtures plus a small file for each walker language.
fn one_per_pack() -> Vec<(String, String)> {
    let mut files: Vec<(String, String)> = FIXTURES
        .iter()
        .map(|n| {
            (
                n.to_string(),
                std::fs::read_to_string(dir().join(n)).unwrap(),
            )
        })
        .collect();
    for (rel, text) in [
        ("main.go", "package main\n\nfunc Run() int { return 1 }\n"),
        ("lib.rs", "pub fn run() -> u32 { 1 }\n"),
        ("app.ts", "export function run(): number { return 1; }\n"),
        ("view.tsx", "export function View() { return <b>x</b>; }\n"),
        ("util.js", "export function run() { return 1; }\n"),
    ] {
        files.push((rel.to_string(), text.to_string()));
    }
    files
}

#[test]
fn coverage_reports_every_language_by_support() {
    let dir = repo(&one_per_pack());
    let out = std::process::Command::new(BIN)
        .args(["index", "build", dir.path().to_str().unwrap(), "--no-cache"])
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    let rep: serde_json::Value = serde_json::from_slice(&out.stdout).unwrap();
    let support = &rep["coverage"]["language_support"];
    let packs = one_per_pack();
    assert_eq!(
        support.as_object().unwrap().len(),
        packs.len(),
        "{support:#}"
    );
    for (rel, _) in &packs {
        let lang = Lang::for_path(rel).unwrap();
        let row = &support[lang.name()];
        let want = if lang.has_grammar() {
            "supported"
        } else {
            "unsupported"
        };
        assert_eq!(row[want], 1, "{rel}: {row}");
        assert_eq!(row["failed"], 0, "{rel}: {row}");
    }
}

/// Peak memory of one child run: (dirty peak, total RSS peak). The dirty peak is the
/// process's own writable memory (macOS: lifetime maximum physical footprint; Linux:
/// RssAnon), sampled while it runs; the RSS peak (wait4) also counts clean, shareable
/// pages of the binary, such as grammar parse tables.
fn child_peaks(cmd: &mut std::process::Command) -> (u64, u64, std::process::ExitStatus) {
    let child = cmd.spawn().unwrap();
    let pid = child.id() as libc::pid_t;
    let mut dirty = 0u64;
    let mut status: libc::c_int = 0;
    // SAFETY: zeroed rusage/rusage_info are valid out-parameters; `pid` is our unreaped
    // child until wait4 returns it.
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    loop {
        #[cfg(target_os = "macos")]
        {
            let mut info: libc::rusage_info_v4 = unsafe { std::mem::zeroed() };
            let rc = unsafe {
                libc::proc_pid_rusage(
                    pid,
                    libc::RUSAGE_INFO_V4,
                    &mut info as *mut _ as *mut libc::rusage_info_t,
                )
            };
            if rc == 0 {
                dirty = dirty.max(info.ri_lifetime_max_phys_footprint);
            }
        }
        #[cfg(target_os = "linux")]
        if let Ok(s) = std::fs::read_to_string(format!("/proc/{pid}/status"))
            && let Some(kb) = s
                .lines()
                .find_map(|l| l.strip_prefix("RssAnon:"))
                .and_then(|v| v.trim().trim_end_matches("kB").trim().parse::<u64>().ok())
        {
            dirty = dirty.max(kb * 1024);
        }
        let rc = unsafe { libc::wait4(pid, &mut status, libc::WNOHANG, &mut ru) };
        if rc == pid {
            break;
        }
        assert_eq!(rc, 0, "wait4 failed");
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    std::mem::forget(child);
    let rss = if cfg!(target_os = "macos") {
        ru.ru_maxrss as u64
    } else {
        ru.ru_maxrss as u64 * 1024
    };
    use std::os::unix::process::ExitStatusExt;
    (dirty, rss, std::process::ExitStatus::from_raw(status))
}

/// Every pack parsed in one worker. Compiling a tag query scans its grammar's whole
/// parse table (tree-sitter's query analysis), so each language present adds its clean
/// parse-table pages to RSS (measured: 49-54 MiB RSS with all 15 packs) while the worker's
/// own memory stays under the 25 MiB line (measured: 16-21 MiB). The spec fixture of
/// `index_build.rs` (5,000 Go/Rust/TS/JS files, every pack compiled in) keeps the
/// 25 MiB RSS line.
#[test]
fn build_of_5000_files_across_all_packs_stays_under_25_mib_dirty() {
    let packs = one_per_pack();
    // distinct bytes per file (leading blank lines), so no two files share facts
    let files: Vec<(String, String)> = (0..5000)
        .map(|i| {
            let (rel, text) = &packs[i % packs.len()];
            let ext = rel.rsplit_once('.').unwrap().1;
            (
                format!("m{:02}/f{i}.{ext}", i % 50),
                format!("{}{text}", "\n".repeat(i / packs.len() % 64)),
            )
        })
        .collect();
    let dir = repo(&files);
    let out_path = dir.path().join("report.json");
    let (dirty, rss, status) = child_peaks(
        std::process::Command::new(BIN)
            .args(["index", "build", dir.path().to_str().unwrap(), "--no-cache"])
            .stdout(std::process::Stdio::from(
                std::fs::File::create(&out_path).unwrap(),
            )),
    );
    assert!(status.success());
    let rep: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&out_path).unwrap()).unwrap();
    assert_eq!(
        rep["coverage"]["indexed_files"], 5000,
        "{:#}",
        rep["coverage"]
    );
    let mib = |b: u64| b as f64 / (1u64 << 20) as f64;
    eprintln!(
        "index build of 5,000 files over every pack: dirty peak {:.1} MiB, RSS peak {:.1} MiB",
        mib(dirty),
        mib(rss)
    );
    assert!(dirty > 0, "no dirty-memory sample");
    assert!(
        mib(dirty) <= 25.0,
        "dirty peak {:.1} MiB exceeds 25 MiB",
        mib(dirty)
    );
    // clean grammar pages are bounded by the packs compiled in
    assert!(
        mib(rss) <= 64.0,
        "RSS peak {:.1} MiB exceeds 64 MiB",
        mib(rss)
    );
}

#[test]
fn deep_indentation_nesting_stays_bounded() {
    if !Lang::Python.has_grammar() {
        return;
    }
    // indentation nests without brackets, so the bracket guards admit this file
    let text: String = (0..2500)
        .map(|d| format!("{}def f{d}():\n", " ".repeat(d)))
        .chain(std::iter::once(format!("{}pass\n", " ".repeat(2500))))
        .collect();
    let started = std::time::Instant::now();
    let f = extract(Lang::Python, &text, false, 1 << 20);
    assert!(started.elapsed() < std::time::Duration::from_secs(20));
    if f.engine == "tree_sitter" {
        assert_eq!(f.symbols.len(), 2500);
        assert!(f.nesting_truncated, "deep qualified names are hashed");
        assert!(f.symbols.iter().all(|s| s.qualified_name.len() <= 300));
    }
}
