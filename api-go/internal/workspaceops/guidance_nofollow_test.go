package workspaceops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// P0-B: guidance discovery reads file CONTENT through the no-follow workspace opener, so
// a guidance path that is a symlink pointing OUTSIDE the repo is refused — its (secret)
// content never lands in a RepoGuidanceRecord that would be injected into a prompt.
func TestCollectWorkspaceGuidanceRefusesSymlinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP_SECRET_HOST_DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	// AGENTS.md is a recognized always-on guidance candidate; make it a symlink to the
	// outside secret (an agent could plant this in a worktree).
	if err := os.Symlink(secret, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	// a legitimate in-repo guidance file, to prove discovery still works.
	if err := os.WriteFile(filepath.Join(root, "CONVENTIONS.md"), []byte("run: go test ./..."), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := collectWorkspaceGuidance(root, "ws1")
	if err != nil {
		t.Fatalf("collect guidance: %v", err)
	}
	for _, r := range records {
		if r.Excerpt != nil && strings.Contains(*r.Excerpt, "TOP_SECRET") {
			t.Fatalf("symlinked guidance leaked outside-repo content: %q", *r.Excerpt)
		}
		if strings.Contains(r.Summary, "TOP_SECRET") {
			t.Fatalf("symlinked guidance leaked outside-repo content in summary: %q", r.Summary)
		}
	}
	// the real in-repo file is still discovered.
	foundReal := false
	for _, r := range records {
		if r.Path == "CONVENTIONS.md" {
			foundReal = true
		}
	}
	if !foundReal {
		t.Fatalf("legitimate in-repo guidance file was not discovered: %+v", records)
	}
}
