package rustcore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/budget"
)

// PAR-EVAL-04: every started rust-core child is counted as a core spawn where it is
// started, and a command that never started is not counted. TrackChild itself counts
// nothing: its callers know the kind (the resident worker, ast-grep, agent CLI probes)
// and count it there, so a generic tracking path can never mislabel a spawn.
func TestCoreSpawnsAreCountedAtTheirCallSites(t *testing.T) {
	withPool(t, 64<<20)
	// the one-shot call sites; with the resident worker on, its start is one more core
	// spawn (TestWorkerGetsTheCoreEnvironmentAndCountsOneSpawn)
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	before := budget.Counters()
	if _, err := runCoreCtx(context.Background(), "search", "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := runBoundedCmd(coreCommandContext(context.Background(), "goal", "list")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", filepath.Join(t.TempDir(), "missing-core"))
	if _, err := runCoreCtx(context.Background(), "search", "x"); err == nil {
		t.Fatal("a missing core binary must fail")
	}
	helper := exec.Command("true")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	untrack := TrackChild(helper)
	_ = helper.Wait()
	untrack()
	after := budget.Counters()
	if got := after.Spawns["core"] - before.Spawns["core"]; got != 2 {
		t.Fatalf("core spawns counted %d, want 2 (a failed start is not a spawn)", got)
	}
	if after.SpawnsTotal-before.SpawnsTotal != 2 {
		t.Fatalf("TrackChild must not count a spawn of its own: %v -> %v", before.Spawns, after.Spawns)
	}
}
