package workspaceops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// confineFixture is a repo root holding a regular file, a symlinked file and a
// symlinked directory that both point outside it.
func confineFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture needs a unix filesystem")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "ok.go"), []byte("package ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "vendor")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestConfineWorkspacePath(t *testing.T) {
	root := confineFixture(t)
	good := map[string]string{
		"src/ok.go":        "src/ok.go",
		"./src/ok.go":      "src/ok.go",
		"src/../src/ok.go": "src/ok.go",
		"src/new_file.go":  "src/new_file.go", // need not exist yet
		".":                ".",
	}
	for in, want := range good {
		got, err := ConfineWorkspacePath(root, in)
		if err != nil || got != want {
			t.Errorf("ConfineWorkspacePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	escapes := []string{
		"/etc/passwd",             // absolute
		filepath.Join(root, "x"),  // absolute, even inside the root
		"../secret.txt",           // climbs out
		"src/../../secret.txt",    // climbs out after a legit prefix
		"leak.txt",                // symlinked file pointing outside
		"vendor/secret.txt",       // through a symlinked directory
		"vendor/not-yet-there.go", // nearest existing ancestor is outside
	}
	for _, in := range escapes {
		_, err := ConfineWorkspacePath(root, in)
		if !errors.Is(err, ErrPathEscapesWorkspace) || !errors.Is(err, ErrInvalidInput) {
			t.Errorf("ConfineWorkspacePath(%q) must be rejected as an escape, got %v", in, err)
		}
	}
	// with no known root the lexical checks still apply
	if _, err := ConfineWorkspacePath("", "/etc/passwd"); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("absolute path without a root: %v", err)
	}
	if _, err := ConfineWorkspacePath("", "../x"); !errors.Is(err, ErrPathEscapesWorkspace) {
		t.Fatalf("parent escape without a root: %v", err)
	}
	if _, err := ConfineWorkspacePath(root, ""); err == nil {
		t.Fatal("an empty path must be rejected")
	}
}

// remember's anchors are confined to the repository (PAR-SEC-05): an absolute or
// symlinked path is refused, not stored as a memory anchor.
func TestProposeContextRejectsEscapingAnchors(t *testing.T) {
	root := confineFixture(t)
	dataDir := t.TempDir()
	reg, _ := json.Marshal([]map[string]any{{"workspace_id": "wsconf", "name": "conf", "root_path": root}})
	if err := os.WriteFile(filepath.Join(dataDir, "workspaces.json"), reg, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc/passwd", "../outside.go", "leak.txt", "vendor/secret.txt"} {
		_, err := ProposeContext(dataDir, "wsconf", ProposeContextRequest{Content: "fact", Paths: []string{"src/ok.go", p}})
		if !errors.Is(err, ErrPathEscapesWorkspace) {
			t.Fatalf("anchor %q must be refused as an escape, got %v", p, err)
		}
	}
	entries, _ := loadContextEntries(dataDir, "wsconf")
	if len(entries) != 0 {
		t.Fatalf("a refused proposal must store nothing: %v", entries)
	}
	e, err := ProposeContext(dataDir, "wsconf", ProposeContextRequest{Content: "fact", Paths: []string{"./src/ok.go", "src/ok.go"}})
	if err != nil || len(e.Paths) != 1 || e.Paths[0] != "src/ok.go" {
		t.Fatalf("in-repo anchors must be cleaned and kept: %+v %v", e, err)
	}
}
