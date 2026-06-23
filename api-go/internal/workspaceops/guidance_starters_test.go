package workspaceops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateGuidanceStarterCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	ws := "ws1"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	// 1. create a new AGENTS.md
	res, err := GenerateGuidanceStarter(dir, ws, "agents", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !res.Created || res.Overwritten {
		t.Fatalf("first generate should be created=true overwritten=false, got %+v", res)
	}
	body, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(string(body), guidancePlaceholderMarker) {
		t.Fatalf("starter must carry the customization marker, got %q", body)
	}

	// 2. overwrite=false on an existing file -> no-op, content unchanged
	tamper := []byte("CUSTOMIZED BY OPERATOR")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), tamper, 0o644); err != nil {
		t.Fatal(err)
	}
	res2, err := GenerateGuidanceStarter(dir, ws, "agents", false)
	if err != nil {
		t.Fatalf("no-overwrite: %v", err)
	}
	if res2.Created || res2.Overwritten {
		t.Fatalf("overwrite=false on existing must be created=false overwritten=false, got %+v", res2)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(got) != string(tamper) {
		t.Fatalf("overwrite=false must not modify the existing file, got %q", got)
	}

	// 3. overwrite=true regenerates
	res3, err := GenerateGuidanceStarter(dir, ws, "agents", true)
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if res3.Created || !res3.Overwritten {
		t.Fatalf("overwrite=true on existing must be overwritten=true, got %+v", res3)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); !strings.Contains(string(got), guidancePlaceholderMarker) {
		t.Fatalf("overwrite should restore the template, got %q", got)
	}
}

func TestGenerateGuidanceStarterCreatesNestedPath(t *testing.T) {
	dir := t.TempDir()
	ws := "ws1"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	res, err := GenerateGuidanceStarter(dir, ws, "openhands_repo", false)
	if err != nil {
		t.Fatalf("nested create: %v", err)
	}
	if res.Path != ".openhands/microagents/repo.md" || !res.Created {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, ".openhands", "microagents", "repo.md")); err != nil {
		t.Fatalf("nested starter file not created: %v", err)
	}
}

func TestGenerateGuidanceStarterRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	ws := "ws1"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	// an agent plants a symlink at the target path pointing outside the repo.
	if err := os.Symlink(secret, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	// even with overwrite=true, the no-follow create must refuse — never writing through
	// the symlink into the host file (arbitrary-file-write prevented).
	if _, err := GenerateGuidanceStarter(dir, ws, "agents", true); err == nil {
		t.Fatalf("writing a starter through a symlinked target must be refused")
	}
	if got, _ := os.ReadFile(secret); string(got) != "ORIGINAL" {
		t.Fatalf("the outside file must be untouched, got %q", got)
	}
}

func TestGenerateGuidanceStarterRejectsUnknownTemplate(t *testing.T) {
	dir := t.TempDir()
	ws := "ws1"
	root := t.TempDir()
	writeSnapshotWithRoot(t, dir, ws, root)
	if _, err := GenerateGuidanceStarter(dir, ws, "../etc/passwd", false); err == nil {
		t.Fatalf("unknown template id must be rejected")
	}
}
