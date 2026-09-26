//! Secret paths: files whose content is a credential store (SSH keys and
//! configuration, netrc, package-registry tokens, Docker auth, `.env` files).
//! A surface that reads a file for an agent, or injects file content into its
//! context, refuses one by path before reading it.
//!
//! The Go redact package owns the list (`api-go/internal/redact/secretpath.go`);
//! this is its copy for the core's own file reads. Both are tested against
//! `testdata/secret_path_golden.tsv`. The list is open-code-review's built-in
//! secret paths (alibaba/open-code-review, Apache-2.0, commit 486022d).

/// Secret-path patterns: `**` stands for any number of directories and `*` for
/// any run of bytes within one name.
pub const SECRET_PATH_PATTERNS: &[&str] = &[
    "**/.ssh/**",
    "**/id_rsa",
    "**/id_dsa",
    "**/id_ecdsa",
    "**/id_ed25519",
    "**/.netrc",
    "**/_netrc",
    "**/.npmrc",
    "**/.pypirc",
    "**/.dockercfg",
    "**/.env",
    "**/.env.*",
];

/// Templates that the `.env` patterns would match. An exception exempts only
/// the patterns whose file name it is an instance of, so a template under a
/// secret directory (`.ssh/.env.example`) stays secret, as in open-code-review.
pub const SECRET_PATH_EXCEPTIONS: &[&str] =
    &["**/.env.example", "**/.env.sample", "**/.env.template"];

/// Whether `path` names a secret file or lies under a secret directory.
pub fn is_secret_path(path: &str) -> bool {
    match_secret_path(path).is_some()
}

/// The pattern that makes `path` a secret path. Only the path is read. `/` and
/// `\\` both separate names, and each name is compared as a case-insensitive
/// file system opens it (see `fold_name`). `.` and `..` are dropped rather than
/// resolved, so a path that passes through a secret directory is refused
/// however it resolves. A caller that follows symbolic links checks the
/// resolved path as well.
pub fn match_secret_path(path: &str) -> Option<&'static str> {
    let names: Vec<String> = path
        .split(['/', '\\'])
        .map(fold_name)
        .filter(|n| !n.is_empty())
        .collect();
    if names.is_empty() {
        return None;
    }
    let names: Vec<&str> = names.iter().map(String::as_str).collect();
    let matches = |p: &str| glob_names(&p.split('/').collect::<Vec<_>>(), &names);
    SECRET_PATH_PATTERNS
        .iter()
        .copied()
        .find(|p| matches(p) && !exempt(p, &matches))
}

/// Whether a matching exception is an instance of `pattern`'s file name:
/// `.env.example` exempts `**/.env.*`, never `**/.ssh/**`.
fn exempt(pattern: &str, matches: &impl Fn(&str) -> bool) -> bool {
    fn file_name(p: &str) -> &str {
        p.rsplit('/').next().unwrap_or(p)
    }
    let name = file_name(pattern);
    name != "**"
        && SECRET_PATH_EXCEPTIONS
            .iter()
            .any(|e| glob_name(name.as_bytes(), file_name(e).as_bytes()) && matches(e))
}

/// The name a file system may open for `name`: ASCII letters in lower case, and
/// the two other characters that case-fold to one (KELVIN SIGN to `k` and LONG
/// S to `s`), with what Windows ignores cut: a stream suffix (`:$DATA`) and
/// trailing dots and spaces.
fn fold_name(name: &str) -> String {
    let name = name.split(':').next().unwrap_or_default();
    name.trim_end_matches(['.', ' '])
        .chars()
        .map(|c| match c {
            'A'..='Z' => c.to_ascii_lowercase(),
            '\u{212A}' => 'k',
            '\u{017F}' => 's',
            _ => c,
        })
        .collect()
}

fn glob_names(pattern: &[&str], names: &[&str]) -> bool {
    match pattern.split_first() {
        None => names.is_empty(),
        Some((&"**", rest)) => (0..=names.len())
            .rev()
            .any(|i| glob_names(rest, &names[i..])),
        Some((first, rest)) => match names.split_first() {
            Some((name, tail)) => {
                glob_name(first.as_bytes(), name.as_bytes()) && glob_names(rest, tail)
            }
            None => false,
        },
    }
}

fn glob_name(pattern: &[u8], name: &[u8]) -> bool {
    match pattern.iter().position(|&c| c == b'*') {
        None => pattern == name,
        Some(star) => {
            name.starts_with(&pattern[..star])
                && (star..=name.len()).any(|i| glob_name(&pattern[star + 1..], &name[i..]))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secret_paths_match_golden_spec() {
        let spec = include_str!("testdata/secret_path_golden.tsv");
        let (mut deny, mut allow, mut paths) = (Vec::new(), Vec::new(), 0);
        for line in spec.lines().filter(|l| !l.starts_with('#')) {
            let (kind, value) = line.split_once('\t').expect("kind<TAB>value");
            match kind {
                "deny" => deny.push(value),
                "allow" => allow.push(value),
                "secret" | "public" => {
                    paths += 1;
                    assert_eq!(
                        is_secret_path(value),
                        kind == "secret",
                        "{value:?} should be {kind}"
                    );
                }
                other => panic!("unknown row kind {other:?}"),
            }
        }
        assert_eq!(deny, SECRET_PATH_PATTERNS, "deny rows");
        assert_eq!(allow, SECRET_PATH_EXCEPTIONS, "allow rows");
        assert!(paths > 20, "golden paths");
        assert_eq!(match_secret_path("home/.ssh/id_rsa"), Some("**/.ssh/**"));
    }
}
