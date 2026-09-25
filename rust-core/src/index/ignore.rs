//! One ignore source for the index (PAR-FRESH-08): built-in vendored/generated
//! directory defaults, then `.gitignore` files (applied by Git itself in Git mode, and by
//! this matcher when indexing a non-Git directory), then `.xmustardignore` files. Later
//! rules win, and a `!pattern` in `.xmustardignore` can re-include a default.
//!
//! Supported syntax (the gitignore subset used in practice): blank lines and `#`
//! comments, `!` negation, a trailing `/` for directories only, a leading or inner `/`
//! anchoring the pattern to the ignore file's directory, `*`, `?`, `**` and `[...]`
//! character classes.
//!
//! Ignore files are repository content, so matching is bounded: patterns compile to a
//! small automaton simulated in O(pattern × path) time (no backtracking), lines longer
//! than `MAX_PATTERN_BYTES` are skipped, and at most `MAX_RULES_PER_FILE` rules per file
//! and `MAX_RULES` in total are kept. Rules beyond the caps are counted in
//! `dropped_rules`, which the index reports.

use std::path::Path;

/// Directory names skipped by default, anywhere in a path. The single list shared by the
/// legacy scanner, the repo map and the index, so all three agree on what is vendored or
/// generated; override for the index with `!name/` in `.xmustardignore`.
pub const DEFAULT_EXCLUDED_DIRS: &[&str] = &[
    ".git",
    ".hg",
    ".svn",
    ".venv",
    "venv",
    "__pycache__",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".turbo",
    ".next",
    "target",
    "node_modules",
    "dist",
    "build",
    "coverage",
    ".coverage",
    "tmp",
    "vendor",
    "third_party",
    "research",
];

/// Root-relative directories skipped by default (runtime data and build output of this
/// repository's own layout).
pub const DEFAULT_EXCLUDED_RELATIVE_DIRS: &[&str] = &["backend/data", "frontend/dist"];

/// Whether the built-in defaults exclude `relative_path` (a directory name in
/// `DEFAULT_EXCLUDED_DIRS` anywhere on the path, or a path under one of
/// `DEFAULT_EXCLUDED_RELATIVE_DIRS`). Used by the legacy scanner and repo map; the index
/// applies the same list through `Ignore::with_defaults`.
pub fn is_default_excluded(relative_path: &str) -> bool {
    let parts: Vec<&str> = relative_path
        .split('/')
        .filter(|part| !part.is_empty() && *part != ".")
        .collect();
    if parts.is_empty() {
        return false;
    }
    if parts
        .iter()
        .any(|part| DEFAULT_EXCLUDED_DIRS.contains(part))
    {
        return true;
    }
    let normalized = parts.join("/");
    DEFAULT_EXCLUDED_RELATIVE_DIRS.iter().any(|excluded| {
        normalized == *excluded
            || normalized
                .strip_prefix(excluded)
                .is_some_and(|rest| rest.starts_with('/'))
    })
}

pub const XMUSTARD_IGNORE: &str = ".xmustardignore";

/// Longest pattern line kept; longer lines are skipped and counted as dropped.
pub const MAX_PATTERN_BYTES: usize = 1024;
/// Rules kept per ignore file.
pub const MAX_RULES_PER_FILE: usize = 2000;
/// Rules kept across all ignore files (defaults included).
pub const MAX_RULES: usize = 10_000;

/// One compiled pattern element.
#[derive(Debug, Clone, PartialEq, Eq)]
enum Tok {
    Lit(u8),
    /// `?`: one byte other than `/`.
    Any,
    /// `[...]`: one byte other than `/` in (or, negated, not in) the ranges.
    Class {
        neg: bool,
        ranges: Vec<(u8, u8)>,
    },
    /// `*`: any run of bytes without `/`.
    Star,
    /// `**/` as a whole component: zero or more whole directories.
    DirStar,
    /// A trailing `**` component: everything that remains.
    AllStar,
}

/// A compiled glob with gitignore semantics: `*` and `?` stop at `/`, `**` as a whole
/// path component crosses directories (`**` elsewhere is a plain `*`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Glob {
    toks: Vec<Tok>,
    /// No wildcard at all: a byte comparison.
    literal: Option<Vec<u8>>,
}

impl Glob {
    pub fn compile(p: &[u8]) -> Glob {
        let mut toks: Vec<Tok> = Vec::new();
        let mut i = 0;
        while i < p.len() {
            match p[i] {
                b'*' => {
                    let mut j = i;
                    while j < p.len() && p[j] == b'*' {
                        j += 1;
                    }
                    let component_start = i == 0 || p[i - 1] == b'/';
                    if j - i >= 2 && component_start && j == p.len() {
                        toks.push(Tok::AllStar);
                    } else if j - i >= 2 && component_start && p[j] == b'/' {
                        toks.push(Tok::DirStar);
                        j += 1;
                    } else if toks.last() != Some(&Tok::Star) {
                        toks.push(Tok::Star);
                    }
                    i = j;
                }
                b'?' => {
                    toks.push(Tok::Any);
                    i += 1;
                }
                b'[' => {
                    let Some(close) = p[i + 1..]
                        .iter()
                        .position(|c| *c == b']')
                        .map(|k| i + 1 + k)
                    else {
                        toks.push(Tok::Lit(b'['));
                        i += 1;
                        continue;
                    };
                    let class = &p[i + 1..close];
                    let (neg, class) = match class.first() {
                        Some(b'!') | Some(b'^') => (true, &class[1..]),
                        _ => (false, class),
                    };
                    let mut ranges = Vec::new();
                    let mut k = 0;
                    while k < class.len() {
                        if k + 2 < class.len() && class[k + 1] == b'-' {
                            ranges.push((class[k], class[k + 2]));
                            k += 3;
                        } else {
                            ranges.push((class[k], class[k]));
                            k += 1;
                        }
                    }
                    toks.push(Tok::Class { neg, ranges });
                    i = close + 1;
                }
                b'\\' if i + 1 < p.len() => {
                    toks.push(Tok::Lit(p[i + 1]));
                    i += 2;
                }
                c => {
                    toks.push(Tok::Lit(c));
                    i += 1;
                }
            }
        }
        let literal = toks
            .iter()
            .map(|t| match t {
                Tok::Lit(c) => Some(*c),
                _ => None,
            })
            .collect::<Option<Vec<u8>>>();
        Glob { toks, literal }
    }

    /// Simulate the pattern as an automaton over its token positions: O(|pattern| ×
    /// |text|) time and O(|pattern|) space, whatever the wildcards.
    pub fn matches(&self, t: &[u8]) -> bool {
        if let Some(lit) = &self.literal {
            return lit.as_slice() == t;
        }
        let n = self.toks.len();
        let mut cur = vec![false; n + 1];
        let mut next = vec![false; n + 1];
        cur[0] = true;
        self.close(&mut cur, true);
        for &c in t {
            next.iter_mut().for_each(|x| *x = false);
            let mut any = false;
            for i in 0..n {
                if !cur[i] {
                    continue;
                }
                let adv = match &self.toks[i] {
                    Tok::Lit(x) => c == *x,
                    Tok::Any => c != b'/',
                    Tok::Class { neg, ranges } => {
                        c != b'/' && ranges.iter().any(|(a, b)| *a <= c && c <= *b) != *neg
                    }
                    Tok::Star => {
                        if c != b'/' {
                            next[i] = true;
                            any = true;
                        }
                        false
                    }
                    Tok::DirStar | Tok::AllStar => {
                        next[i] = true;
                        any = true;
                        false
                    }
                };
                if adv {
                    next[i + 1] = true;
                    any = true;
                }
            }
            if !any {
                return false;
            }
            std::mem::swap(&mut cur, &mut next);
            self.close(&mut cur, c == b'/');
        }
        cur[n]
    }

    /// Epsilon moves (always forward): `*` and a trailing `**` may match nothing, and
    /// `**/` may end at a component boundary.
    fn close(&self, states: &mut [bool], at_boundary: bool) {
        for i in 0..self.toks.len() {
            if states[i] {
                match self.toks[i] {
                    Tok::Star | Tok::AllStar => states[i + 1] = true,
                    Tok::DirStar if at_boundary => states[i + 1] = true,
                    _ => {}
                }
            }
        }
    }
}

#[derive(Debug, Clone)]
struct Rule {
    /// Directory of the ignore file, relative to the index root ("" at the root).
    base: String,
    glob: Glob,
    negate: bool,
    dir_only: bool,
    /// Pattern contains a slash (other than trailing): matched against the path
    /// relative to `base` rather than against any single name.
    anchored: bool,
}

#[derive(Debug, Clone, Default)]
pub struct Ignore {
    rules: Vec<Rule>,
    /// Pattern lines skipped by the caps (too long, or past the rule limits).
    pub dropped_rules: usize,
}

impl Ignore {
    /// The defaults only.
    pub fn with_defaults() -> Ignore {
        let mut ig = Ignore::default();
        for d in DEFAULT_EXCLUDED_DIRS {
            ig.add_line("", &format!("{d}/"));
        }
        for d in DEFAULT_EXCLUDED_RELATIVE_DIRS {
            ig.add_line("", &format!("/{d}/"));
        }
        ig
    }

    /// Add the rules of one ignore file located in directory `base` (relative).
    pub fn add_file(&mut self, base: &str, content: &str) {
        let mut kept = 0usize;
        for line in content.lines() {
            if kept >= MAX_RULES_PER_FILE || self.rules.len() >= MAX_RULES {
                if is_rule_line(line) {
                    self.dropped_rules += 1;
                }
                continue;
            }
            if self.add_line(base, line) {
                kept += 1;
            }
        }
    }

    /// Returns whether a rule was added.
    fn add_line(&mut self, base: &str, line: &str) -> bool {
        if !is_rule_line(line) {
            return false;
        }
        if line.len() > MAX_PATTERN_BYTES {
            self.dropped_rules += 1;
            return false;
        }
        let mut l = line.trim_end_matches(['\r', '\n']);
        // trailing spaces are insignificant unless escaped
        while l.ends_with(' ') && !l.ends_with("\\ ") {
            l = &l[..l.len() - 1];
        }
        let (negate, mut p) = match l.strip_prefix('!') {
            Some(rest) => (true, rest),
            None => (false, l.strip_prefix('\\').unwrap_or(l)),
        };
        let dir_only = p.ends_with('/');
        if dir_only {
            p = &p[..p.len() - 1];
        }
        let anchored = p.contains('/');
        let p = p.strip_prefix('/').unwrap_or(p);
        if p.is_empty() {
            return false;
        }
        self.rules.push(Rule {
            base: base.trim_matches('/').to_string(),
            glob: Glob::compile(p.as_bytes()),
            negate,
            dir_only,
            anchored,
        });
        true
    }

    /// Load `.xmustardignore` files named in `ignore_files` (paths relative to `root`,
    /// e.g. from `git ls-files`), plus the root one when present on disk.
    pub fn load_xmustard(&mut self, root: &Path, ignore_files: &[String]) {
        let mut files: Vec<String> = ignore_files.to_vec();
        if !files.iter().any(|f| f == XMUSTARD_IGNORE) {
            files.push(XMUSTARD_IGNORE.to_string());
        }
        // shallow directories first, so deeper files override.
        files.sort_by_key(|f| (f.matches('/').count(), f.clone()));
        files.dedup();
        for f in files {
            if let Ok(bytes) = crate::symbolgraph::read_repo_bytes_beneath_capped(root, &f, 1 << 20)
            {
                let base = Path::new(&f)
                    .parent()
                    .map(|p| p.to_string_lossy().into_owned())
                    .unwrap_or_default();
                self.add_file(&base, &String::from_utf8_lossy(&bytes));
            }
        }
    }

    fn rule_matches(r: &Rule, path: &str, is_dir: bool) -> bool {
        if r.dir_only && !is_dir {
            return false;
        }
        let rel = if r.base.is_empty() {
            path
        } else if let Some(rest) = path.strip_prefix(&r.base).and_then(|s| s.strip_prefix('/')) {
            rest
        } else {
            return false;
        };
        if r.anchored {
            r.glob.matches(rel.as_bytes())
        } else {
            let name = rel.rsplit('/').next().unwrap_or(rel);
            r.glob.matches(name.as_bytes())
        }
    }

    /// Last matching rule for exactly this path (not its parents).
    fn decide(&self, path: &str, is_dir: bool) -> Option<bool> {
        self.rules
            .iter()
            .rev()
            .find(|r| Self::rule_matches(r, path, is_dir))
            .map(|r| !r.negate)
    }

    /// Whether `rel` (a file path relative to the index root) is ignored: its own last
    /// matching rule, or an ignored parent directory (which a file rule cannot undo).
    pub fn is_ignored(&self, rel: &str, is_dir: bool) -> bool {
        let rel = rel.trim_matches('/');
        let mut end = 0;
        let parts: Vec<&str> = rel.split('/').collect();
        for (i, part) in parts.iter().enumerate() {
            end += part.len() + usize::from(i > 0);
            let last = i + 1 == parts.len();
            let dir = !last || is_dir;
            if self.decide(&rel[..end], dir) == Some(true) {
                return true;
            }
        }
        false
    }
}

fn is_rule_line(line: &str) -> bool {
    let l = line.trim_end_matches(['\r', '\n', ' ']);
    !(l.is_empty() || l.starts_with('#'))
}

/// Glob match with gitignore semantics: `*` and `?` stop at `/`, `**` crosses it.
pub fn glob_match(p: &[u8], t: &[u8]) -> bool {
    Glob::compile(p).matches(t)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn glob_semantics() {
        assert!(glob_match(b"*.rs", b"main.rs"));
        assert!(!glob_match(b"*.rs", b"src/main.rs"));
        assert!(glob_match(b"src/**/*.rs", b"src/a/b/main.rs"));
        assert!(glob_match(b"src/**/*.rs", b"src/main.rs"));
        assert!(glob_match(b"**/gen", b"a/b/gen"));
        assert!(glob_match(b"file[0-9].txt", b"file7.txt"));
        assert!(!glob_match(b"file[!0-9].txt", b"file7.txt"));
        assert!(glob_match(b"a?c", b"abc"));
    }

    #[test]
    fn double_star_components_and_plain_stars() {
        assert!(glob_match(b"a/**/b", b"a/b"));
        assert!(glob_match(b"a/**/b", b"a/x/y/b"));
        assert!(!glob_match(b"a/**/b", b"a/xb"));
        assert!(glob_match(b"foo/**", b"foo/x/y"));
        assert!(!glob_match(b"foo/**", b"foo"));
        assert!(glob_match(b"**", b"any/thing"));
        // `**` inside a component is a plain `*`
        assert!(glob_match(b"a**b", b"axxb"));
        assert!(!glob_match(b"a**b", b"a/b"));
        // an escaped `*` is literal
        assert!(glob_match(b"\\*x", b"*x"));
        assert!(!glob_match(b"\\*x", b"yx"));
    }

    #[test]
    fn pathological_patterns_match_in_linear_time() {
        // `*a*a*a...*b` against a long run of `a`: exponential for a backtracking matcher
        // (measured 81 s at 10 wildcards), O(pattern x path) here.
        let pat = format!("{}*b", "*a".repeat(64));
        let name = format!("{}.ts", "a".repeat(4000));
        let t = std::time::Instant::now();
        assert!(!glob_match(pat.as_bytes(), name.as_bytes()));
        let mut ig = Ignore::with_defaults();
        ig.add_file("", &format!("{pat}\n**/{pat}\n"));
        assert!(!ig.is_ignored(&format!("src/{name}"), false));
        assert!(
            t.elapsed() < std::time::Duration::from_secs(2),
            "{:?}",
            t.elapsed()
        );
    }

    #[test]
    fn rule_bounds_are_enforced_and_counted() {
        let mut ig = Ignore::default();
        let long = "x".repeat(MAX_PATTERN_BYTES + 1);
        let mut file = format!("{long}\n# comment\n\n");
        for i in 0..MAX_RULES_PER_FILE + 10 {
            file.push_str(&format!("gen{i}/\n"));
        }
        ig.add_file("", &file);
        assert_eq!(ig.rules.len(), MAX_RULES_PER_FILE);
        assert_eq!(ig.dropped_rules, 11);
        assert!(ig.is_ignored("gen0/a.ts", false));
    }

    #[test]
    fn shared_default_exclusions() {
        assert!(is_default_excluded("backend/data/x.py"));
        assert!(is_default_excluded("a/target/debug/x.rs"));
        assert!(!is_default_excluded("backend/database.py"));
        let ig = Ignore::with_defaults();
        assert!(ig.is_ignored("backend/data/x.py", false));
        assert!(ig.is_ignored("frontend/dist/app.js", false));
        assert!(!ig.is_ignored("src/backend/data/x.py", false));
    }

    #[test]
    fn defaults_negation_anchoring_and_parents() {
        let mut ig = Ignore::with_defaults();
        assert!(ig.is_ignored("node_modules/x/index.js", false));
        assert!(ig.is_ignored("pkg/vendor/lib.go", false));
        assert!(!ig.is_ignored("src/vendors.go", false));
        ig.add_file("", "!vendor/\n/generated/\n*.pb.go\n");
        assert!(!ig.is_ignored("pkg/vendor/lib.go", false));
        assert!(ig.is_ignored("generated/a.ts", false));
        assert!(!ig.is_ignored("src/generated/a.ts", false));
        assert!(ig.is_ignored("api/x.pb.go", false));
        ig.add_file("api", "!keep.pb.go\n");
        assert!(!ig.is_ignored("api/keep.pb.go", false));
        // a file rule cannot re-include a file under an ignored directory
        ig.add_file("", "!generated/a.ts\n");
        assert!(ig.is_ignored("generated/a.ts", false));
    }
}
