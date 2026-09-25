//! One ignore source for the index (PAR-FRESH-08): built-in vendored/generated
//! directory defaults, then `.gitignore` files (applied by Git itself in Git mode, and by
//! this matcher when indexing a non-Git directory), then `.xmustardignore` files. Later
//! rules win, and a `!pattern` in `.xmustardignore` can re-include a default.
//!
//! Supported syntax (the gitignore subset used in practice): blank lines and `#`
//! comments, `!` negation, a trailing `/` for directories only, a leading or inner `/`
//! anchoring the pattern to the ignore file's directory, `*`, `?`, `**` and `[...]`
//! character classes.

use std::path::Path;

/// Directory names skipped by default. Ported from the scanner/repo-map exclusion list
/// so the index agrees with the legacy graph; override with `!name/` in
/// `.xmustardignore`.
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

pub const XMUSTARD_IGNORE: &str = ".xmustardignore";

#[derive(Debug, Clone)]
struct Rule {
    /// Directory of the ignore file, relative to the index root ("" at the root).
    base: String,
    pattern: String,
    negate: bool,
    dir_only: bool,
    /// Pattern contains a slash (other than trailing): matched against the path
    /// relative to `base` rather than against any single name.
    anchored: bool,
}

#[derive(Debug, Clone, Default)]
pub struct Ignore {
    rules: Vec<Rule>,
}

impl Ignore {
    /// The defaults only.
    pub fn with_defaults() -> Ignore {
        let mut ig = Ignore::default();
        for d in DEFAULT_EXCLUDED_DIRS {
            ig.add_line("", &format!("{d}/"));
        }
        ig
    }

    /// Add the rules of one ignore file located in directory `base` (relative).
    pub fn add_file(&mut self, base: &str, content: &str) {
        for line in content.lines() {
            self.add_line(base, line);
        }
    }

    fn add_line(&mut self, base: &str, line: &str) {
        let mut l = line.trim_end_matches(['\r', '\n']);
        // trailing spaces are insignificant unless escaped
        while l.ends_with(' ') && !l.ends_with("\\ ") {
            l = &l[..l.len() - 1];
        }
        if l.is_empty() || l.starts_with('#') {
            return;
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
            return;
        }
        self.rules.push(Rule {
            base: base.trim_matches('/').to_string(),
            pattern: p.to_string(),
            negate,
            dir_only,
            anchored,
        });
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
            glob_match(r.pattern.as_bytes(), rel.as_bytes())
        } else {
            let name = rel.rsplit('/').next().unwrap_or(rel);
            glob_match(r.pattern.as_bytes(), name.as_bytes())
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
        let mut acc = String::new();
        let parts: Vec<&str> = rel.split('/').collect();
        for (i, part) in parts.iter().enumerate() {
            if !acc.is_empty() {
                acc.push('/');
            }
            acc.push_str(part);
            let last = i + 1 == parts.len();
            let dir = !last || is_dir;
            if self.decide(&acc, dir) == Some(true) {
                return true;
            }
        }
        false
    }
}

/// Glob match with gitignore semantics: `*` and `?` stop at `/`, `**` crosses it.
pub fn glob_match(p: &[u8], t: &[u8]) -> bool {
    if p.is_empty() {
        return t.is_empty();
    }
    if p.starts_with(b"**") {
        let rest = &p[2..];
        let rest = rest.strip_prefix(b"/").unwrap_or(rest);
        // `**/x` matches x at any depth, including zero directories.
        for i in 0..=t.len() {
            if (i == 0 || t[i - 1] == b'/') && glob_match(rest, &t[i..]) {
                return true;
            }
        }
        return rest.is_empty();
    }
    match p[0] {
        b'*' => {
            for i in 0..=t.len() {
                if glob_match(&p[1..], &t[i..]) {
                    return true;
                }
                if i < t.len() && t[i] == b'/' {
                    break;
                }
            }
            false
        }
        b'?' => !t.is_empty() && t[0] != b'/' && glob_match(&p[1..], &t[1..]),
        b'[' => {
            let Some(close) = p.iter().skip(1).position(|c| *c == b']').map(|i| i + 1) else {
                return !t.is_empty() && t[0] == b'[' && glob_match(&p[1..], &t[1..]);
            };
            if t.is_empty() || t[0] == b'/' {
                return false;
            }
            let class = &p[1..close];
            let (neg, class) = match class.first() {
                Some(b'!') | Some(b'^') => (true, &class[1..]),
                _ => (false, class),
            };
            let c = t[0];
            let mut hit = false;
            let mut i = 0;
            while i < class.len() {
                if i + 2 < class.len() && class[i + 1] == b'-' {
                    if class[i] <= c && c <= class[i + 2] {
                        hit = true;
                    }
                    i += 3;
                } else {
                    if class[i] == c {
                        hit = true;
                    }
                    i += 1;
                }
            }
            hit != neg && glob_match(&p[close + 1..], &t[1..])
        }
        b'\\' if p.len() > 1 => !t.is_empty() && t[0] == p[1] && glob_match(&p[2..], &t[1..]),
        c => !t.is_empty() && t[0] == c && glob_match(&p[1..], &t[1..]),
    }
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
