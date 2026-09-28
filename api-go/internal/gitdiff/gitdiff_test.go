package gitdiff

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Go copy renders exactly the golden lines it implements (every line except the
// symbols mode, which only the Rust core runs), in the golden's order.
func TestPolicyMatchesTheGoldenSpec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "rust-core", "src", "testdata", "gitdiff_policy.golden"))
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "symbols") {
			continue
		}
		want.WriteString(line + "\n")
	}
	if got := RenderPolicy(); got != want.String() {
		t.Fatalf("policy differs from the golden spec:\n got:\n%s\nwant:\n%s", got, want.String())
	}
}

func TestRefsShapedLikeOptionsAreRefused(t *testing.T) {
	for _, bad := range []string{"", "-x", "--output=/tmp/x", "a b", "a\nb", "a\x00b", strings.Repeat("x", MaxRefLen+1)} {
		if err := CheckRef(bad); !errors.Is(err, ErrBadRef) {
			t.Fatalf("%q: want ErrBadRef, got %v", bad, err)
		}
	}
	for _, good := range []string{"HEAD", "main", "origin/main", "HEAD~2", "v1^{commit}"} {
		if err := CheckRef(good); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
}

// A Git child never sees the caller's GIT_* or XMUSTARD_* variables, and it ignores the
// global configuration.
func TestEnvScrubsAndIsolates(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("XMUSTARD_APPROVER_TOKEN", "secret")
	env := Env()
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "XMUSTARD_") {
			t.Fatalf("leaked %q", kv)
		}
	}
	for _, kv := range IsolationEnv {
		if !slices.Contains(env, kv) {
			t.Fatalf("missing %q", kv)
		}
	}
}

func TestResolveCommitReadsOnlyCommits(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(Env(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "one")
	ctx := context.Background()
	id, err := ResolveCommit(ctx, dir, "HEAD", time.Minute)
	if err != nil || len(id) < 40 {
		t.Fatalf("HEAD: %q %v", id, err)
	}
	for _, bad := range []string{"HEAD^{tree}", "no-such-ref", "-h"} {
		if _, err := ResolveCommit(ctx, dir, bad, time.Minute); !errors.Is(err, ErrBadRef) {
			t.Fatalf("%q: want ErrBadRef, got %v", bad, err)
		}
	}
}
