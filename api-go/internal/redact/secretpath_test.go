package redact

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The secret-path list and its verdicts match the golden spec that the Rust
// core's copy (rust-core/src/secretpath.rs) is tested against as well.
func TestSecretPathsMatchGoldenSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "rust-core", "src", "testdata", "secret_path_golden.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var deny, allow []string
	paths := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(spec), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		kind, value, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("row without a tab: %q", line)
		}
		switch kind {
		case "deny":
			deny = append(deny, value)
		case "allow":
			allow = append(allow, value)
		case "secret", "public":
			paths++
			if got := IsSecretPath(value); got != (kind == "secret") {
				t.Errorf("IsSecretPath(%q) = %v, want %s", value, got, kind)
			}
		default:
			t.Fatalf("unknown row kind %q", kind)
		}
	}
	patterns, exceptions := SecretPathPatterns()
	if !slices.Equal(deny, patterns) || !slices.Equal(allow, exceptions) {
		t.Errorf("patterns differ from the golden spec:\n got %q %q\nwant %q %q", patterns, exceptions, deny, allow)
	}
	if paths < 20 {
		t.Fatalf("golden spec lists %d paths", paths)
	}
	if p, ok := MatchSecretPath("home/.ssh/id_rsa"); !ok || p != "**/.ssh/**" {
		t.Errorf("MatchSecretPath names %q", p)
	}
	patterns[0] = "changed"
	if again, _ := SecretPathPatterns(); again[0] != "**/.ssh/**" {
		t.Error("SecretPathPatterns returned the package's own slice")
	}
}

// A template file name exempts the .env patterns and nothing else, so a
// template under a secret directory is refused by the directory rule itself.
// The pattern is the evidence: a boolean verdict alone cannot tell the
// directory rule winning from the .env rule being applied to the same name.
func TestSecretPathTemplateUnderASecretDirectoryNamesTheDirectoryRule(t *testing.T) {
	for _, c := range []struct{ path, pattern string }{
		{".ssh/.env.example", "**/.ssh/**"},
		{".ssh/.env.sample", "**/.ssh/**"},
		{".ssh/.env.template", "**/.ssh/**"},
		{"home/u/.ssh/.env.example", "**/.ssh/**"},
		{`C:\Users\u\.ssh\.env.template`, "**/.ssh/**"},
		{".SSH/.ENV.EXAMPLE", "**/.ssh/**"},
		{"repo/../.ssh/.env.example", "**/.ssh/**"},
	} {
		if p, ok := MatchSecretPath(c.path); !ok || p != c.pattern {
			t.Errorf("MatchSecretPath(%q) = %q, %v; want %q, true", c.path, p, ok, c.pattern)
		}
	}
	// The counterpart: the same file name outside a secret directory stays
	// public, so the rule is the file name's directory and not "never exempt".
	for _, path := range []string{".env.example", "app/.env.sample", "docs/.env.template"} {
		if p, ok := MatchSecretPath(path); ok {
			t.Errorf("MatchSecretPath(%q) = %q, true; want public", path, p)
		}
	}
}
