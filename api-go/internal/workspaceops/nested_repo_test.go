package workspaceops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
)

// WS-RT2: a repository nested in the workspace is outside its identity, so it does not
// make the identity incomplete, and the evidence key that labels results current and
// keys the result cache (evidence.NewKey refuses an incomplete identity) is derivable.
func TestANestedRepositoryLeavesTheIdentityCompleteWithTheRealCore(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_BIN", rustCoreBin(t))
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	root := t.TempDir()
	repo := func(dir string, files map[string]string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{
			{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
			{"add", "-A"}, {"commit", "-qm", "c"},
		} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	repo(root, map[string]string{"a.go": "package p\n\nfunc Alpha() int { return 1 }\n"})
	repo(filepath.Join(root, "sub"), map[string]string{"x.go": "package sub\n\nfunc SubOnly() int { return 2 }\n"})

	id, _ := CurrentRepoIdentity(context.Background(), canonicalRoot(root))
	if id.Source != "repo-key" || !id.Complete || len(id.Limitations) != 0 {
		t.Fatalf("identity with a nested repository = %+v", id)
	}
	rev := evidence.Revisions{Repo: evidence.Identity{Key: id.Key, Complete: id.Complete}, Memory: "1", Parser: id.ParserVersion}
	if _, err := evidence.NewKey("search", []byte(`{"query":"Alpha"}`), rev); err != nil {
		t.Fatalf("a search under that identity is not cacheable: %v", err)
	}
}

// A ground that arrives while the registration build runs waits for it, so it reports
// the registration baseline rather than finding none (EVIDENCE.md headline 8).
func TestGroundRightAfterRegistrationReportsTheRegistrationBaseline(t *testing.T) {
	state := baselineCore(t, "0.5")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	StartRegistrationBaseline(dataDir, ws)
	start := time.Now()
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > codeIndexWait {
		t.Fatalf("ground waited %s, past codeIndexWait", d)
	}
	if b := g.Baseline; b == nil || b.Reason != BaselineRegistration || b.Held != "" {
		t.Fatalf("ground right after registration: baseline %+v (unknown %v)", b, g.Unknown)
	}
	if got := indexCalls(t, state); !slices.Equal(got, []string{"--reason=registration"}) {
		t.Fatalf("builds = %v, want only the registration one", got)
	}
}
