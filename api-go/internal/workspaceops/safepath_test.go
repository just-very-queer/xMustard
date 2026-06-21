package workspaceops

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWorkspacePathConfinement(t *testing.T) {
	root := t.TempDir()
	// a normal in-repo file
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a secret outside the root + a symlink inside pointing to it
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Symlink(outside, filepath.Join(root, "evil-link"))
	}

	good := []string{"ok.txt"}
	for _, g := range good {
		if _, err := resolveWorkspacePath(root, g); err != nil {
			t.Errorf("expected %q to resolve, got %v", g, err)
		}
	}
	bad := []string{"", "../etc/passwd", "../../etc/passwd", "/etc/passwd", "a/../../b"}
	if runtime.GOOS != "windows" {
		bad = append(bad, "evil-link")
	}
	for _, b := range bad {
		if p, err := resolveWorkspacePath(root, b); err == nil {
			t.Errorf("expected %q to be rejected, got %q", b, p)
		}
	}
}

func TestHashFileContentRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "in.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := hashFileContent(root, "in.txt"); !ok {
		t.Fatal("in-repo file should hash")
	}
	if _, ok := hashFileContent(root, "../../etc/passwd"); ok {
		t.Fatal("traversal path must not hash")
	}
	if _, ok := hashFileContent(root, "/etc/hosts"); ok {
		t.Fatal("absolute path must not hash")
	}
}

func TestReadWorkspaceRegularFileSizeCap(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, maxRefFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readWorkspaceRegularFile(root, "big.bin"); ok {
		t.Fatal("oversized file must be rejected")
	}
	// a non-regular target (directory) is rejected
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := readWorkspaceRegularFile(root, "dir"); ok {
		t.Fatal("directory must be rejected")
	}
}
