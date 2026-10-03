package rustcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/budget"
)

func passCore(t *testing.T, stdout string) {
	t.Helper()
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := "#!/bin/sh\ncat <<'EOF'\n" + stdout + "\nEOF\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
}

// WS-RT1: the pass counters a core result ends with are added to the health block's
// core_passes once per call; the result's bytes are returned unchanged.
func TestCorePassCountersReachTheHealthCounters(t *testing.T) {
	withPool(t, 64<<20)
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	const result = `{"ok":true,"nested":{"pass_counters":{"files_listed":1000}},"pass_counters":{"files_listed":4,"files_statted":5,"files_hashed":2,"bytes_hashed":100,"git_spawns":3,"rows_loaded":7}}`
	passCore(t, result)
	before := budget.Counters().CorePasses
	for range 2 {
		out, err := runCoreCtx(context.Background(), "search", "x")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(out); got != result+"\n" {
			t.Fatalf("the result changed: %q", got)
		}
	}
	after := budget.Counters().CorePasses
	want := budget.CorePasses{Calls: 2, Counted: 2, FilesListed: 8, FilesStatted: 10, FilesHashed: 4, BytesHashed: 200, GitSpawns: 6, RowsLoaded: 14}
	if got := after.Minus(before); got != want {
		t.Fatalf("core_passes grew by %+v, want %+v", got, want)
	}
}

// WS-RT1: a result without counters (an array, or an object that carries none) is a
// call with nothing counted, never a guess.
func TestCoreResultsWithoutPassCountersCountOnlyTheCall(t *testing.T) {
	withPool(t, 64<<20)
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	for _, result := range []string{
		`[{"kind":"todo"}]`,
		`{"ok":true}`,
		`{"ok":true,"pass_counters":"not an object"}`,
	} {
		passCore(t, result)
		before := budget.Counters().CorePasses
		if _, err := runCoreCtx(context.Background(), "scan-signals", "x"); err != nil {
			t.Fatal(err)
		}
		got := budget.Counters().CorePasses.Minus(before)
		if want := (budget.CorePasses{Calls: 1}); got != want {
			t.Fatalf("%s: core_passes grew by %+v, want %+v", result, got, want)
		}
	}
}
