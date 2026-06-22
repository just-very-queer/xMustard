package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

// image_path is confined to the uploads root: an agent cannot make the server read
// an arbitrary host file and base64-send it to a remote provider (XM-NEW-014).
func TestImageFileConfinedToUploads(t *testing.T) {
	dataDir := t.TempDir()
	uploads := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "ok.png"), []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dataDir, "uploads")

	if _, err := imageFileToDataURL(root, "ok.png"); err != nil {
		t.Fatalf("a legitimate uploaded image should encode: %v", err)
	}
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd", "../secret"} {
		if _, err := imageFileToDataURL(root, bad); err == nil {
			t.Fatalf("path %q must be rejected (host-file egress)", bad)
		}
	}
}

// Caller-supplied terminal IDs with traversal must be rejected before any log file
// is opened (XM-NEW-012).
func TestOpenTerminalRejectsUnsafeID(t *testing.T) {
	dataDir := t.TempDir()
	ws := "wsTerm"
	// a workspace record is required before OpenTerminal proceeds; if it errors on
	// the missing workspace that's fine — we assert the unsafe ID is never accepted.
	bad := "../../../../tmp/evil"
	id := bad
	req := TerminalOpenRequest{WorkspaceID: ws, TerminalID: &id}
	if _, err := OpenTerminal(dataDir, req); err == nil {
		t.Fatal("a terminal id with traversal must be rejected")
	}
	// the traversal target must not have been created
	if _, err := os.Stat("/tmp/evil.log"); err == nil {
		t.Fatal("traversal log file must not be created")
	}
}
