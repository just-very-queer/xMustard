package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

// Resource profile (scripts/bench/rss.sh): every workspace scan ran `opencode models`,
// an external agent CLI (~90 MB RSS, no timeout, unbounded output), just to list model
// names in the snapshot. A scan must not launch agent CLIs; explicit runtime detection
// still lists live models, and later scans reuse that cached list.
func TestWorkspaceScanDoesNotLaunchAgentCLIs(t *testing.T) {
	dataDir, workspaceID, _ := writeSemanticIndexFixture(t)
	marker := filepath.Join(t.TempDir(), "launched")
	bin := writeExecutableScript(t, "opencode", "#!/bin/sh\necho launched >> "+marker+"\necho fake/model-a\n")
	settings, err := loadSettings(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	settings.OpencodeBin = &bin
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), settings); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanWorkspace(dataDir, workspaceID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("workspace scan launched the opencode CLI")
	}
	runtimes, err := DetectRuntimes(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeHasModel(runtimes, "opencode", "fake/model-a") {
		t.Fatalf("explicit detection must list live models: %+v", runtimes)
	}
	_ = os.Remove(marker)
	snapshot, err := ScanWorkspace(dataDir, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a later scan launched the opencode CLI")
	}
	found := false
	for _, rt := range snapshot.Runtimes {
		for _, m := range rt.Models {
			if m.ID == "fake/model-a" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("scan should report the cached model list: %+v", snapshot.Runtimes)
	}
}

func runtimeHasModel(rts []RuntimeCapabilities, runtime, model string) bool {
	for _, rt := range rts {
		if rt.Runtime != runtime {
			continue
		}
		for _, m := range rt.Models {
			if m.ID == model {
				return true
			}
		}
	}
	return false
}
