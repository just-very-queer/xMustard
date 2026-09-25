package workspaceops

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/budget"
)

// PAR-EVAL-04: git children started by Go and bytes hashed for memory anchors are
// counted.
func TestGitSpawnsAndAnchorHashingAreCounted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	content := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	before := budget.Counters()
	if st := readWorktreeStatus(root); !st.IsGitRepo {
		t.Fatalf("worktree status: %+v", st)
	}
	readGoChangeRecords(root, "HEAD")
	if _, ok := hashFileContent(root, "main.go"); !ok {
		t.Fatal("anchor hash failed")
	}
	after := budget.Counters()
	if got := after.Spawns["git"] - before.Spawns["git"]; got != 3 {
		t.Fatalf("git spawns counted %d, want 3 (status + diff + status)", got)
	}
	if got := after.BytesHashed - before.BytesHashed; got != int64(len(content)) {
		t.Fatalf("hashed bytes counted %d, want %d", got, len(content))
	}
}

// PAR-EVAL-04: short-lived helpers are counted where they are started (TrackChild
// counts nothing), and semantic materialization's file hashing is counted.
func TestHelperSpawnsAndSemanticHashingAreCounted(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\necho 'provider/model-a'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	content := []byte("export const x = 1;\n")
	if err := os.WriteFile(filepath.Join(root, "x.ts"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	before := budget.Counters()
	detectOpencodeModels(probe)
	detectOpencodeModels(filepath.Join(t.TempDir(), "missing"))
	if _, _, err := semanticFileMetadata(root, "x.ts"); err != nil {
		t.Fatal(err)
	}
	after := budget.Counters()
	if got := after.Spawns["helper"] - before.Spawns["helper"]; got != 1 {
		t.Fatalf("helper spawns counted %d, want 1 (a probe that never started is not a spawn)", got)
	}
	if got := after.BytesHashed - before.BytesHashed; got != int64(len(content)) {
		t.Fatalf("hashed bytes counted %d, want %d", got, len(content))
	}
}
