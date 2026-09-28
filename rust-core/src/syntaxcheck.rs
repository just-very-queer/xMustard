//! Tree-sitter syntax errors of files an agent just changed (WS-23): the first layer
//! of the post-edit diagnostics delta. The Claude Code hook service asks the resident
//! worker for the errors of a file before an `Edit`/`Write` and again after it, and
//! reports only the errors the edit introduced (Cline's `getNewDiagnostics`, SWE-agent's
//! lint-on-edit). Compiler and language-server diagnostics are later layers.
//!
//! No file content leaves this module: an error is reported by position, by the node
//! tree-sitter could not parse or found missing, and by a digest of its trimmed source
//! line, which lets the caller match an error across line shifts. Paths are confined to
//! the root (no absolute path, no `..`, no symlink escape), secret-shaped paths are
//! never read, and the parse bounds of the index apply.

use std::fs;
use std::hash::Hasher;
use std::path::{Component, Path};

use serde::Serialize;
use tree_sitter::{Node, Parser};

use crate::index::extract::within_parse_bounds;
use crate::index::lang::Lang;

/// Files one call may check.
pub const MAX_FILES: usize = 16;
/// Errors reported per file; `truncated` says when more exist.
pub const MAX_ERRORS_PER_FILE: usize = 20;
/// Largest file read. The index's parse bounds (size, tokens, nesting) apply too.
pub const MAX_FILE_BYTES: u64 = 2 << 20;

/// The syntax errors of each requested file.
#[derive(Debug, Serialize)]
pub struct SyntaxReport {
    pub files: Vec<FileSyntax>,
}

/// One file's result. `status` is `checked` when the file was parsed; otherwise it says
/// why not, and `errors` is empty.
#[derive(Debug, Serialize)]
pub struct FileSyntax {
    pub path: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub language: Option<&'static str>,
    pub status: &'static str,
    pub errors: Vec<SyntaxError>,
    pub truncated: bool,
}

/// One syntax error: an `ERROR` node (text tree-sitter could not parse) or a `MISSING`
/// node (a token the grammar expected, such as a closing brace).
#[derive(Debug, Serialize, PartialEq, Eq)]
pub struct SyntaxError {
    /// 1-based line and column of the error's start.
    pub line: usize,
    pub column: usize,
    pub end_line: usize,
    /// `error` or `missing`.
    pub kind: &'static str,
    /// The missing node's kind, or the kind of the first child of an error node.
    pub node: String,
    /// FNV-1a (hex) of the trimmed source line the error starts on.
    pub line_hash: String,
}

/// Checks each relative path under `root`.
pub fn check(root: &Path, paths: &[String]) -> SyntaxReport {
    let root = fs::canonicalize(root).unwrap_or_else(|_| root.to_path_buf());
    SyntaxReport {
        files: paths.iter().take(MAX_FILES).map(|p| check_file(&root, p)).collect(),
    }
}

fn check_file(root: &Path, rel: &str) -> FileSyntax {
    let rel = rel.trim_start_matches("./").replace('\\', "/");
    let mut out = FileSyntax {
        path: rel.clone(),
        language: None,
        status: "checked",
        errors: Vec::new(),
        truncated: false,
    };
    let lang = Lang::for_path(&rel);
    out.language = lang.map(Lang::name);
    let refusal = refusal(root, &rel, lang);
    if let Some(status) = refusal {
        out.status = status;
        return out;
    }
    let (Some(lang), Some(bytes)) = (lang, read_confined(root, &rel)) else {
        out.status = "unreadable";
        return out;
    };
    if !within_parse_bounds(lang, &bytes) {
        out.status = "too_large";
        return out;
    }
    let Some(grammar) = lang.grammar() else {
        out.status = "unsupported";
        return out;
    };
    let mut parser = Parser::new();
    let tree = parser
        .set_language(&grammar)
        .ok()
        .and_then(|()| parser.parse(&bytes, None));
    let Some(tree) = tree else {
        out.status = "unreadable";
        return out;
    };
    let lines: Vec<&[u8]> = bytes.split(|&b| b == b'\n').collect();
    out.truncated = collect(tree.root_node(), &lines, &mut out.errors);
    out
}

/// Why a path is not checked, before anything is read.
fn refusal(root: &Path, rel: &str, lang: Option<Lang>) -> Option<&'static str> {
    let escapes = Path::new(rel)
        .components()
        .any(|c| !matches!(c, Component::Normal(_)));
    if rel.is_empty() || escapes {
        return Some("outside_root");
    }
    if crate::secretpath::is_secret_path(rel) {
        return Some("secret_path");
    }
    if !lang.is_some_and(Lang::has_grammar) {
        return Some("unsupported");
    }
    if !root.join(rel).is_file() {
        return Some("unreadable");
    }
    None
}

/// The file's bytes when it resolves inside `root` and is within the size bound.
fn read_confined(root: &Path, rel: &str) -> Option<Vec<u8>> {
    let path = fs::canonicalize(root.join(rel)).ok()?;
    if !path.starts_with(root) || fs::metadata(&path).ok()?.len() > MAX_FILE_BYTES {
        return None;
    }
    fs::read(path).ok()
}

/// Walks only the subtrees that hold an error and records each error or missing node
/// once (an error node's own descendants are not reported again). Returns whether more
/// errors existed than were kept.
fn collect(root: Node<'_>, lines: &[&[u8]], out: &mut Vec<SyntaxError>) -> bool {
    let mut stack = vec![root];
    while let Some(node) = stack.pop() {
        let kind = match (node.is_missing(), node.is_error()) {
            (true, _) => "missing",
            (_, true) => "error",
            _ => {
                if node.has_error() {
                    let mut cursor = node.walk();
                    let kids: Vec<Node<'_>> = node.children(&mut cursor).collect();
                    stack.extend(kids.into_iter().rev());
                }
                continue;
            }
        };
        if out.len() == MAX_ERRORS_PER_FILE {
            return true;
        }
        let start = node.start_position();
        let named = match kind {
            "missing" => node.kind().to_string(),
            _ => node
                .child(0)
                .map_or_else(|| "ERROR".to_string(), |c| c.kind().to_string()),
        };
        out.push(SyntaxError {
            line: start.row + 1,
            column: start.column + 1,
            end_line: node.end_position().row + 1,
            kind,
            node: named,
            line_hash: line_hash(lines.get(start.row).copied().unwrap_or_default()),
        });
    }
    false
}

fn line_hash(line: &[u8]) -> String {
    let mut h = fnv::FnvHasher::default();
    h.write(line.trim_ascii());
    format!("{:016x}", h.finish())
}

mod fnv {
    /// 64-bit FNV-1a: a stable, dependency-free digest for matching lines.
    pub struct FnvHasher(u64);

    impl Default for FnvHasher {
        fn default() -> Self {
            Self(0xcbf2_9ce4_8422_2325)
        }
    }

    impl std::hash::Hasher for FnvHasher {
        fn finish(&self) -> u64 {
            self.0
        }
        fn write(&mut self, bytes: &[u8]) {
            for b in bytes {
                self.0 ^= u64::from(*b);
                self.0 = self.0.wrapping_mul(0x0100_0000_01b3);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn write(dir: &Path, rel: &str, text: &str) {
        let p = dir.join(rel);
        fs::create_dir_all(p.parent().unwrap()).unwrap();
        fs::write(p, text).unwrap();
    }

    #[test]
    fn reports_missing_and_error_nodes_by_position() {
        let dir = tempfile::tempdir().unwrap();
        write(dir.path(), "ok.go", "package a\n\nfunc F() int { return 1 }\n");
        write(dir.path(), "bad.go", "package a\n\nfunc F() int {\n\treturn 1 +\n");
        let r = check(dir.path(), &["ok.go".into(), "bad.go".into()]);
        assert_eq!(r.files[0].status, "checked");
        assert!(r.files[0].errors.is_empty());
        let bad = &r.files[1];
        assert_eq!(bad.language, Some("go"));
        assert!(!bad.errors.is_empty(), "{bad:?}");
        assert!(bad.errors.iter().all(|e| e.line >= 3 && e.line_hash.len() == 16));
    }

    #[test]
    fn refuses_escapes_secrets_and_unsupported_files() {
        let dir = tempfile::tempdir().unwrap();
        write(dir.path(), ".env", "A=1\n");
        write(dir.path(), "notes.txt", "hello\n");
        let r = check(
            dir.path(),
            &["../x.go".into(), "/etc/passwd".into(), ".env".into(), "notes.txt".into(), "gone.go".into()],
        );
        let statuses: Vec<&str> = r.files.iter().map(|f| f.status).collect();
        assert_eq!(statuses, ["outside_root", "outside_root", "secret_path", "unsupported", "unreadable"]);
    }

    #[test]
    fn a_symlink_out_of_the_root_is_not_read() {
        let dir = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        write(outside.path(), "x.go", "package x\nfunc {\n");
        #[cfg(unix)]
        std::os::unix::fs::symlink(outside.path().join("x.go"), dir.path().join("link.go")).unwrap();
        #[cfg(unix)]
        {
            let r = check(dir.path(), &["link.go".into()]);
            assert_eq!(r.files[0].status, "unreadable");
        }
    }

    #[test]
    fn line_hash_ignores_indentation() {
        assert_eq!(line_hash(b"\treturn 1 +"), line_hash(b"    return 1 +  "));
        assert_ne!(line_hash(b"return 1"), line_hash(b"return 2"));
    }

    #[test]
    fn errors_are_bounded_per_file() {
        let dir = tempfile::tempdir().unwrap();
        let mut src = String::from("package a\n");
        for i in 0..60 {
            src.push_str(&format!("func f{i}( {{\n"));
        }
        write(dir.path(), "many.go", &src);
        let r = check(dir.path(), &["many.go".into()]);
        assert!(r.files[0].errors.len() <= MAX_ERRORS_PER_FILE);
    }
}
