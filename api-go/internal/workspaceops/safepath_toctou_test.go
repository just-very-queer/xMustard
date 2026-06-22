package workspaceops

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// readWorkspaceRegularFile must never read a file outside the workspace, even under
// an adversarial check-to-open race where a concurrent writer swaps a validated
// regular file for a symlink pointing at a secret outside the repo (XM-PRO-007).
func TestReadWorkspaceRegularFileResistsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("IN-REPO"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}

	// normal read works
	if data, ok := readWorkspaceRegularFile(root, "doc.txt"); !ok || string(data) != "IN-REPO" {
		t.Fatalf("normal read failed: ok=%v data=%q", ok, data)
	}
	// a symlink (even to an in-repo file) is refused outright — the race-free policy
	if err := os.Symlink(filepath.Join(root, "doc.txt"), filepath.Join(root, "link.txt")); err == nil {
		if _, ok := readWorkspaceRegularFile(root, "link.txt"); ok {
			t.Fatal("a symlink final component must be refused")
		}
	}
	// a symlink to the outside secret is refused (never leaks it)
	if err := os.Symlink(secret, filepath.Join(root, "escape.txt")); err == nil {
		if d, ok := readWorkspaceRegularFile(root, "escape.txt"); ok {
			t.Fatalf("symlink to outside must be refused; leaked %q", d)
		}
	}

	// SWAP-LOOP: race the reader against a writer that flips "swap.txt" between the
	// in-repo regular file and a symlink to the outside secret. The read must only
	// ever return IN-REPO content or fail — never SECRET-OUTSIDE.
	swap := filepath.Join(root, "swap.txt")
	_ = os.WriteFile(swap, []byte("IN-REPO"), 0o644)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(swap)
			_ = os.Symlink(secret, swap)
			_ = os.Remove(swap)
			_ = os.WriteFile(swap, []byte("IN-REPO"), 0o644)
		}
	}()
	// The only forbidden outcome is returning the OUTSIDE secret. A transient empty
	// in-repo file (the writer is mid-WriteFile) is benign — still inside the repo.
	for i := 0; i < 8000; i++ {
		if d, ok := readWorkspaceRegularFile(root, "swap.txt"); ok && string(d) == "SECRET-OUTSIDE" {
			close(stop)
			wg.Wait()
			t.Fatalf("TOCTOU leak: read returned the out-of-repo secret")
		}
	}
	close(stop)
	wg.Wait()
}
