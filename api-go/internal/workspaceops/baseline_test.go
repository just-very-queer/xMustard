package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
)

// WS-22 (PAR-FRESH-06): the index baseline is built automatically at registration or
// the first ground, rebuilt automatically when HEAD moves on a clean worktree, rebuilt
// otherwise only by an explicit rebaseline, and every build is kept in the history.

// useTestGovernor installs a governor that samples a fixed 10 MiB tree, so heavy
// admission (the baseline build) does not depend on this test binary's own memory.
func useTestGovernor(t *testing.T, heavyWait time.Duration) {
	t.Helper()
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{
		HeavyWait:    heavyWait,
		FreeOSMemory: func() {},
		Sampler: func() (budget.TreeSample, error) {
			return budget.TreeSample{At: time.Now(), Supported: true, Basis: "test", Processes: 1, RSSBytes: 10 << 20, SelfRSSBytes: 10 << 20}, nil
		},
	})
	t.Cleanup(func() { budget.Gov = prev })
}

// baselineCore installs a fake core whose drift answers from state: no baseline until
// `changetrack index` runs, which records its reason there. The files head_moved and
// dirty in state make drift report a HEAD move and a dirty worktree; index clears
// head_moved; fail makes index exit 1. It returns the state dir.
func baselineCore(t *testing.T, indexDelay string) string {
	t.Helper()
	useTestGovernor(t, 5*time.Second)
	state := t.TempDir()
	script := `#!/bin/sh
S=` + state + `
flag() { if [ -f "$S/$1" ]; then printf true; else printf false; fi; }
case "$1 $2" in
"changetrack drift")
  if [ -f "$S/reason" ]; then
    printf '{"has_baseline":true,"stale":%s,"head_changed":%s,"dirty":%s,"baseline_head":"h1","baseline_indexed_at":"2026-09-28T00:00:00Z","baseline_reason":"%s","reasons":[]}' \
      "$(flag head_moved)" "$(flag head_moved)" "$(flag dirty)" "$(cat "$S/reason")"
  else
    printf '{"has_baseline":false,"stale":true,"head_changed":false,"dirty":%s,"baseline_head":null,"baseline_indexed_at":null,"baseline_reason":null,"reasons":["no index baseline exists; results would be unindexed"]}' "$(flag dirty)"
  fi ;;
"changetrack index")
  echo "$6" >> "$S/index.log"
  sleep ` + indexDelay + `
  if [ -f "$S/fail" ]; then echo 'changetrack index failed: boom' >&2; exit 1; fi
  r=${6#--reason=}
  replaced=false; [ -f "$S/reason" ] && replaced=true
  printf '%s' "$r" > "$S/reason"; rm -f "$S/head_moved"
  printf '{"workspace_id":"ignored","head":"h1","branch":"main","indexed_at":"2026-09-28T00:00:00Z","auto":true,"reason":"%s","dirty":false,"tracked_files":3,"signatures":2,"replaced":%s,"previous_head":null}' "$r" "$replaced" ;;
"changetrack working-changes") echo '{"has_baseline":true,"changed_files":[],"changed_files_total":0,"dirty_symbols":[],"contract_breaks":0}' ;;
"symbolgraph coverage") echo '{"complete":true,"languages":{"go":{"supported":1,"unsupported":0,"failed":0}}}' ;;
*) echo '{}' ;;
esac
`
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	return state
}

// seedBaselineWorkspace registers a workspace with a cached snapshot over a plain root.
func seedBaselineWorkspace(t *testing.T) (dataDir, ws, root string) {
	t.Helper()
	dataDir, ws, root = t.TempDir(), "wsBaseline", t.TempDir()
	rec := workspaceRecord{WorkspaceID: ws, Name: "baseline", RootPath: root}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "snapshot.json"), workspaceSnapshot{ScannerVersion: scannerVersion, Workspace: rec}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(workspacesPath(dataDir), []workspaceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	return dataDir, ws, root
}

func indexCalls(t *testing.T, state string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "index.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func touch(t *testing.T, state, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func baselineHistory(t *testing.T, dataDir, ws string) []govstore.Event {
	t.Helper()
	evs, err := IndexBaselineHistory(context.Background(), dataDir, ws, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func unknownReason(g *SessionGrounding, field string) string {
	for _, u := range g.Unknown {
		if u.Field == field {
			return u.Reason
		}
	}
	return ""
}

// Registering a workspace creates the baseline; ground then reports it as
// {head, indexed_at, auto} and builds nothing more.
func TestRegistrationCreatesTheBaselineAndGroundReportsIt(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, root := seedBaselineWorkspace(t)
	if _, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{RootPath: root, PreferCachedSnapshot: true}); err != nil {
		t.Fatal(err)
	}
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=registration" {
		t.Fatalf("registration builds the baseline once: %v", got)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	b := g.Baseline
	if b == nil || b.Head == nil || *b.Head != "h1" || b.IndexedAt != "2026-09-28T00:00:00Z" || !b.Auto || b.Reason != BaselineRegistration || b.Held != "" {
		t.Fatalf("ground baseline = %+v (unknown %v)", b, g.Unknown)
	}
	raw, _ := json.Marshal(g)
	var wire struct {
		Baseline map[string]any `json:"baseline"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Baseline["head"] != "h1" || wire.Baseline["auto"] != true || wire.Baseline["indexed_at"] == nil {
		t.Fatalf("wire baseline: %s", raw)
	}
	if got := indexCalls(t, state); len(got) != 1 {
		t.Fatalf("ground over a current baseline builds nothing: %v", got)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 1 || evs[0].Principal != AutoBaselinePrincipal || evs[0].Note != BaselineRegistration || evs[0].HeadSHA != "h1" {
		t.Fatalf("history: %+v", evs)
	}
	var data map[string]any
	if json.Unmarshal(evs[0].Data, &data) != nil || data["auto"] != true || data["reason"] != BaselineRegistration || data["replaced"] != false {
		t.Fatalf("history data: %s", evs[0].Data)
	}
}

// A workspace registered before WS-22 (no baseline) gets one at its first ground, once,
// even when several grounds race for it.
func TestFirstGroundBuildsTheMissingBaselineOnce(t *testing.T) {
	state := baselineCore(t, "0.3")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	var wg sync.WaitGroup
	grounds := make([]*SessionGrounding, 6)
	for i := range grounds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := BuildSessionGrounding(dataDir, ws)
			if err != nil {
				t.Error(err)
				return
			}
			grounds[i] = g
		}()
	}
	wg.Wait()
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=first_ground" {
		t.Fatalf("concurrent first grounds build once: %v", got)
	}
	for _, g := range grounds {
		if g == nil || g.Baseline == nil || g.Baseline.Reason != BaselineFirstGround || !g.Baseline.Auto {
			t.Fatalf("every ground reports the new baseline: %+v", g)
		}
	}
	if evs := baselineHistory(t, dataDir, ws); len(evs) != 1 || evs[0].Note != BaselineFirstGround {
		t.Fatalf("history: %+v", evs)
	}
}

// A HEAD move rebuilds the baseline automatically only on a clean worktree: with
// uncommitted changes their content would become the baseline and hide their contract
// breaks, so the baseline is held and ground says why.
func TestHeadMoveRebaselinesOnlyACleanWorktree(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	if err := os.WriteFile(filepath.Join(state, "reason"), []byte(BaselineRegistration), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, state, "head_moved")
	touch(t, state, "dirty")
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := indexCalls(t, state); len(got) != 0 {
		t.Fatalf("a dirty worktree holds the rebaseline: %v", got)
	}
	if b := g.Baseline; b == nil || b.Reason != BaselineRegistration || b.Held != dirtyHeadHold {
		t.Fatalf("held baseline = %+v", b)
	}

	if err := os.Remove(filepath.Join(state, "dirty")); err != nil {
		t.Fatal(err)
	}
	g, err = BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=head_changed" {
		t.Fatalf("a clean HEAD move rebaselines: %v", got)
	}
	if b := g.Baseline; b == nil || b.Reason != BaselineHeadChanged || !b.Auto || b.Held != "" {
		t.Fatalf("rebaselined = %+v", b)
	}
	var drift struct {
		HeadChanged bool `json:"head_changed"`
	}
	if json.Unmarshal(g.Drift, &drift) != nil || drift.HeadChanged {
		t.Fatalf("ground reports the drift after the rebuild: %s", g.Drift)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 1 || evs[0].Note != BaselineHeadChanged {
		t.Fatalf("history: %+v", evs)
	}
	var data map[string]any
	if json.Unmarshal(evs[0].Data, &data) != nil || data["replaced"] != true {
		t.Fatalf("a rebaseline records what it replaced: %s", evs[0].Data)
	}
}

// Nothing is rebuilt on a guess: a drift that could not fingerprint the worktree, or
// does not say whether a baseline exists, keeps the baseline, and ground reports the
// baseline unknown with the reason. A failed build is reported, not swallowed.
func TestAutomaticBaselineNeverGuessesAndReportsFailure(t *testing.T) {
	dataDir, ws, _ := seedBaselineWorkspace(t)
	for _, tc := range []struct{ drift, reason string }{
		{`{"has_baseline":false,"stale":true,"error":"git ls-files: git exited nonzero"}`, "could not fingerprint"},
		{`{"stale":false}`, "does not say whether a baseline exists"},
	} {
		state := t.TempDir()
		core := filepath.Join(t.TempDir(), "xmustard-core")
		script := "#!/bin/sh\ncase \"$1 $2\" in\n\"changetrack drift\") echo '" + tc.drift + "' ;;\n\"changetrack index\") echo x >> " + state + "/index.log; echo '{}' ;;\n*) echo '{}' ;;\nesac\n"
		if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XMUSTARD_CORE_BIN", core)
		g, err := BuildSessionGrounding(dataDir, ws)
		if err != nil {
			t.Fatal(err)
		}
		if got := indexCalls(t, state); len(got) != 0 {
			t.Fatalf("%s: rebuilt on a guess", tc.drift)
		}
		if g.Baseline != nil || !strings.Contains(unknownReason(g, "baseline"), tc.reason) {
			t.Fatalf("%s: baseline %+v, unknown %v", tc.drift, g.Baseline, g.Unknown)
		}
	}

	state := baselineCore(t, "0")
	touch(t, state, "fail")
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if g.Baseline != nil || !strings.Contains(unknownReason(g, "baseline"), "automatic first_ground rebaseline failed") {
		t.Fatalf("a failed build must be reported: %+v %v", g.Baseline, g.Unknown)
	}
	if evs := baselineHistory(t, dataDir, ws); len(evs) != 0 {
		t.Fatalf("a failed build is not history: %+v", evs)
	}
}

// ground never waits for the heavy slot: behind heavy work the rebuild is held to a
// later call at once, and ground still answers.
func TestGroundNeverWaitsForTheHeavySlot(t *testing.T) {
	state := baselineCore(t, "0") // its governor waits 5 s for a busy slot
	dataDir, ws, _ := seedBaselineWorkspace(t)
	release, err := budget.AcquireHeavy(context.Background(), "test_holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ground waited %s for the heavy slot", d)
	}
	if got := indexCalls(t, state); len(got) != 0 {
		t.Fatalf("a refused build must not start the core: %v", got)
	}
	if !strings.Contains(unknownReason(g, "baseline"), "overloaded") {
		t.Fatalf("the held rebuild is reported: %v", g.Unknown)
	}
}

// An explicit rebaseline (POST /index; the route admits indexer and admin only) is not
// automatic and is recorded with its principal.
func TestAdminRebaselineIsRecordedInHistory(t *testing.T) {
	baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	b, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin")
	if err != nil {
		t.Fatal(err)
	}
	if b.Auto || b.Reason != BaselineAdmin || b.WorkspaceID != ws || b.HistorySeq <= 0 {
		t.Fatalf("admin rebaseline = %+v", b)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 1 || evs[0].Principal != "ops-admin" || evs[0].Seq != b.HistorySeq || evs[0].Type != govstore.EventIndexBaseline {
		t.Fatalf("history: %+v", evs)
	}
	var data map[string]any
	if json.Unmarshal(evs[0].Data, &data) != nil || data["auto"] != false || data["reason"] != BaselineAdmin {
		t.Fatalf("history data: %s", evs[0].Data)
	}
	if _, err := RebaselineIndex(context.Background(), dataDir, "wsMissing", "ops-admin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unknown workspace: %v", err)
	}
}

// End to end with the real core: registration baselines a git repository, ground and
// impact read it, a commit moves HEAD and the next ground rebaselines, and each build
// is in the history.
func TestBaselineLifecycleWithTheRealCore(t *testing.T) {
	t.Setenv("XMUSTARD_CORE_BIN", rustCoreBin(t))
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	useTestGovernor(t, 30*time.Second)
	dataDir, root := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "api.go"), []byte("package api\n\nfunc Handle(a int) int {\n\treturn a\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "one")
	snap, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{RootPath: root, AutoScan: true, PreferCachedSnapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	ws := snap.Workspace.WorkspaceID
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	head := git("rev-parse", "HEAD")
	if b := g.Baseline; b == nil || b.Head == nil || *b.Head != head || !b.Auto || b.Reason != BaselineRegistration || b.IndexedAt == "" {
		t.Fatalf("ground baseline = %+v (unknown %v)", b, g.Unknown)
	}
	if len(g.Unknown) != 0 || g.ChangedFiles == nil || *g.ChangedFiles != 0 || g.ContractBreaks == nil || *g.ContractBreaks != 0 {
		t.Fatalf("a fresh baseline grounds clean: %+v", g)
	}
	raw, err := WorkspaceChangesSinceIndexCtx(context.Background(), dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	var impact struct {
		HasBaseline       bool            `json:"has_baseline"`
		ChangedFilesTotal *int            `json:"changed_files_total"`
		ContractBreaks    *int            `json:"contract_breaks"`
		Truncation        json.RawMessage `json:"truncation"`
	}
	if json.Unmarshal(raw, &impact) != nil || !impact.HasBaseline || impact.ChangedFilesTotal == nil || *impact.ChangedFilesTotal != 0 ||
		impact.ContractBreaks == nil || impact.Truncation != nil {
		t.Fatalf("impact on a fresh install: %s", raw)
	}

	if err := os.WriteFile(filepath.Join(root, "api.go"), []byte("package api\n\nfunc Handle(a, b int) int {\n\treturn a + b\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-qam", "two")
	g, err = BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if b := g.Baseline; b == nil || *b.Head != git("rev-parse", "HEAD") || b.Reason != BaselineHeadChanged {
		t.Fatalf("a clean HEAD move rebaselines: %+v (unknown %v)", b, g.Unknown)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 2 || evs[0].Note != BaselineRegistration || evs[1].Note != BaselineHeadChanged {
		t.Fatalf("history: %+v", evs)
	}
	var data map[string]any
	if json.Unmarshal(evs[1].Data, &data) != nil || data["previous_head"] != head {
		t.Fatalf("the rebaseline records the HEAD it replaced: %s", evs[1].Data)
	}
}
