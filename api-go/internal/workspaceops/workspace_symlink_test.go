package workspaceops

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An in-repo symlink that points outside the workspace must be rejected by
// normalizeWorkspaceFile — the lexical containment check is not enough because
// os.Stat (and downstream reads) follow symlinks (XM-NEW-023).
func TestNormalizeWorkspaceFileRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak")); err != nil {
		t.Fatal(err)
	}

	if _, err := normalizeWorkspaceFile(root, "real.go"); err != nil {
		t.Fatalf("a real in-repo file should normalize: %v", err)
	}
	if _, err := normalizeWorkspaceFile(root, "leak"); err == nil {
		t.Fatal("an in-repo symlink pointing outside the root must be rejected")
	}
	if _, err := normalizeWorkspaceFile(root, "../../etc/passwd"); err == nil {
		t.Fatal("lexical traversal must be rejected")
	}
}
