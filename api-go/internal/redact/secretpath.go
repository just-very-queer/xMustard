package redact

import "strings"

// Secret paths are files whose content is a credential store: SSH keys and
// configuration, netrc, package-registry tokens, Docker auth and .env files. A
// surface that reads a file for an agent, or injects file content into its
// context, refuses one by path before reading it, whatever redaction would
// later find in it.
//
// The list is open-code-review's built-in secret paths (alibaba/open-code-review,
// Apache-2.0: internal/config/allowlist/default_secret_patterns.json and the
// .env rule of secret_path.go, commit 486022d). The Rust core holds a copy
// (rust-core/src/secretpath.rs) for its own file reads; both are tested
// against rust-core/src/testdata/secret_path_golden.tsv.
var (
	secretPathPatterns = []string{
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
	}
	// secretPathExceptions are templates that the .env patterns would match.
	// An exception exempts only the patterns whose file name it is an instance
	// of, so a template under a secret directory (".ssh/.env.example") stays
	// secret, as in open-code-review, where it exempts only the .env rule.
	secretPathExceptions = []string{
		"**/.env.example",
		"**/.env.sample",
		"**/.env.template",
	}
)

// SecretPathPatterns returns the secret-path patterns and the template
// exceptions to them. In a pattern, "**" stands for any number of directories
// and "*" for any run of bytes within one name.
func SecretPathPatterns() (patterns, exceptions []string) {
	return append([]string(nil), secretPathPatterns...), append([]string(nil), secretPathExceptions...)
}

// IsSecretPath reports whether path names a secret file or lies under a secret
// directory. See MatchSecretPath.
func IsSecretPath(path string) bool {
	_, ok := MatchSecretPath(path)
	return ok
}

// MatchSecretPath returns the pattern that makes path a secret path. The check
// reads the path only, never the file. '/' and '\\' both separate names, and
// each name is compared as a case-insensitive file system opens it (see
// foldName). "." and ".." are dropped rather than resolved, so a path that
// passes through a secret directory is refused however it resolves
// ("repo/../.ssh/config", ".ssh/../notes"). A caller that follows symbolic
// links checks the resolved path as well.
func MatchSecretPath(path string) (string, bool) {
	var names []string
	for _, name := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if name = foldName(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	matches := func(p string) bool { return globNames(strings.Split(p, "/"), names) }
	for _, p := range secretPathPatterns {
		if matches(p) && !exempt(p, matches) {
			return p, true
		}
	}
	return "", false
}

// exempt reports whether a matching exception is an instance of pattern's file
// name: ".env.example" exempts "**/.env.*", never "**/.ssh/**".
func exempt(pattern string, matches func(string) bool) bool {
	fileName := func(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }
	name := fileName(pattern)
	for _, e := range secretPathExceptions {
		if name != "**" && globName(name, fileName(e)) && matches(e) {
			return true
		}
	}
	return false
}

// foldName returns the name a file system may open for name: ASCII letters in
// lower case, and the two other characters that case-fold to one (KELVIN SIGN
// to 'k' and LONG S to 's', as case-insensitive APFS folds them), with what
// Windows ignores cut: a stream suffix (":$DATA") and trailing dots and spaces.
func foldName(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return strings.Map(func(r rune) rune {
		switch {
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		case r == '\u212A':
			return 'k'
		case r == '\u017F':
			return 's'
		}
		return r
	}, strings.TrimRight(name, ". "))
}

// globNames matches path names against pattern names: "**" matches any number
// of names, and a name matches with "*" standing for any run of bytes.
func globNames(pattern, names []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := len(names); i >= 0; i-- {
				if globNames(pattern[1:], names[i:]) {
					return true
				}
			}
			return false
		}
		if len(names) == 0 || !globName(pattern[0], names[0]) {
			return false
		}
		pattern, names = pattern[1:], names[1:]
	}
	return len(names) == 0
}

// globName matches one name against a pattern in which '*' stands for any run
// of bytes.
func globName(pattern, name string) bool {
	star := strings.IndexByte(pattern, '*')
	if star < 0 {
		return pattern == name
	}
	if !strings.HasPrefix(name, pattern[:star]) {
		return false
	}
	rest := pattern[star+1:]
	for i := star; i <= len(name); i++ {
		if globName(rest, name[i:]) {
			return true
		}
	}
	return false
}
