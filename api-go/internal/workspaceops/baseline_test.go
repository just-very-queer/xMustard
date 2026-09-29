package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/govstore"
)

// WS-22 (PAR-FRESH-06): the index baseline is built automatically at registration or
// the first ground and rebuilt automatically when HEAD moves, always as the committed
// state; otherwise only an explicit rebaseline rebuilds it. Every build is recorded in
// the history before it replaces the stored baseline.

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

// baselineCore installs a fake core whose baseline is a real file: `changetrack index`
// stages "<reason> <head>" beside the baseline path (as `--stage` does) and drift reads
// the published file. The current HEAD is the state file "head" (h1 when absent), so a
// test moves HEAD by writing it; a baseline file that is not "<reason> <head>" reads as
// unreadable. The state file "fail" makes index exit 1. Each finished drift appends a
// line to drift.log, each index its reason to index.log. It returns the state dir.
func baselineCore(t *testing.T, indexDelay string) string {
	t.Helper()
	useTestGovernor(t, 5*time.Second)
	state := t.TempDir()
	script := `#!/bin/sh
S=` + state + `
cur=$(cat "$S/head" 2>/dev/null || echo h1)
B="$3/workspaces/$5/index_baseline.json"
case "$1 $2" in
"changetrack drift")
  if [ ! -f "$B" ]; then
    printf '{"has_baseline":false,"stale":true,"head_changed":false,"baseline_head":null,"baseline_indexed_at":null,"baseline_reason":null,"baseline_dirty":null,"reasons":["no index baseline exists; results would be unindexed"]}'
  elif read r h < "$B" && [ -n "$h" ]; then
    moved=false; [ "$h" != "$cur" ] && moved=true
    printf '{"has_baseline":true,"stale":%s,"head_changed":%s,"baseline_head":"%s","baseline_indexed_at":"2026-09-28T00:00:00Z","baseline_reason":"%s","baseline_dirty":false,"reasons":[]}' $moved $moved "$h" "$r"
  else
    printf '{"has_baseline":false,"stale":true,"head_changed":false,"baseline_head":null,"baseline_indexed_at":null,"baseline_reason":null,"baseline_dirty":null,"baseline_error":"index baseline unreadable: torn","reasons":["index baseline unreadable: torn"]}'
  fi
  echo drift >> "$S/drift.log" ;;
"changetrack index")
  echo "$6" >> "$S/index.log"
  sleep ` + indexDelay + `
  if [ -f "$S/fail" ]; then echo 'changetrack index failed: boom' >&2; exit 1; fi
  r=${6#--reason=}
  replaced=false; prev=null
  if [ -f "$B" ]; then replaced=true; read pr ph < "$B"; [ -n "$ph" ] && prev="\"$ph\""; fi
  mkdir -p "$(dirname "$B")"
  printf '%s %s\n' "$r" "$cur" > "$3/workspaces/$5/index_baseline.staged.json"
  printf '{"workspace_id":"ignored","head":"%s","branch":"main","indexed_at":"2026-09-28T00:00:00Z","auto":true,"reason":"%s","dirty":false,"from_head":0,"tracked_files":3,"signatures":2,"replaced":%s,"previous_head":%s}' "$cur" "$r" $replaced "$prev" ;;
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
	return stateLog(t, state, "index.log")
}

func stateLog(t *testing.T, state, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func writeState(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
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

func eventData(t *testing.T, ev govstore.Event) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		t.Fatalf("history data %s: %v", ev.Data, err)
	}
	return data
}

func unknownReason(g *SessionGrounding, field string) string {
	for _, u := range g.Unknown {
		if u.Field == field {
			return u.Reason
		}
	}
	return ""
}

func waitRegistrationBaselines(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := WaitRegistrationBaselines(ctx); err != nil {
		t.Fatalf("registration baseline still running: %v", err)
	}
}

// Registration builds the baseline after it has answered; ground then reports it as
// {head, indexed_at, auto, dirty} and builds nothing more. A second registration while
// the first build runs starts no other.
func TestRegistrationCreatesTheBaselineAndGroundReportsIt(t *testing.T) {
	state := baselineCore(t, "0.3")
	dataDir, ws, root := seedBaselineWorkspace(t)
	if _, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{RootPath: root, PreferCachedSnapshot: true}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	StartRegistrationBaseline(dataDir, ws)
	StartRegistrationBaseline(dataDir, ws)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("registration waited %s for its baseline", d)
	}
	waitRegistrationBaselines(t)
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=registration" {
		t.Fatalf("registration builds the baseline once: %v", got)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	b := g.Baseline
	if b == nil || b.Head == nil || *b.Head != "h1" || b.IndexedAt != "2026-09-28T00:00:00Z" || !b.Auto || b.Reason != BaselineRegistration || b.Dirty || b.Held != "" {
		t.Fatalf("ground baseline = %+v (unknown %v)", b, g.Unknown)
	}
	raw, _ := json.Marshal(g)
	var wire struct {
		Baseline map[string]any `json:"baseline"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Baseline["head"] != "h1" || wire.Baseline["auto"] != true || wire.Baseline["dirty"] != false || wire.Baseline["indexed_at"] == nil {
		t.Fatalf("wire baseline: %s", raw)
	}
	if got := indexCalls(t, state); len(got) != 1 {
		t.Fatalf("ground over a current baseline builds nothing: %v", got)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 1 || evs[0].Principal != AutoBaselinePrincipal || evs[0].Note != BaselineRegistration || evs[0].HeadSHA != "h1" {
		t.Fatalf("history: %+v", evs)
	}
	if data := eventData(t, evs[0]); data["auto"] != true || data["reason"] != BaselineRegistration || data["replaced"] != false {
		t.Fatalf("history data: %s", evs[0].Data)
	}
	if _, err := os.Stat(stagedBaselinePath(dataDir, ws)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the staged build was put in place: %v", err)
	}
}

// A registration that found no baseline and waits for the lock while an explicit
// rebaseline (POST /index) holds it is answered by that build: it reads drift once and
// builds nothing, so no drift core of its own overlaps the requests that follow the
// rebaseline (WS-FIX-07). A holder that builds nothing leaves the registration to
// re-read drift under the lock and build, as before.
func TestRegistrationWaitingOutARebaselineReadsNoDriftAgain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rebuild   bool
		wantIndex []string
		wantDrift int
	}{
		{"a rebaseline meanwhile answers it", true, []string{"--reason=admin"}, 1},
		{"no build meanwhile", false, []string{"--reason=registration"}, 3}, // before, under the lock, after its build
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := baselineCore(t, "0")
			dataDir, ws, _ := seedBaselineWorkspace(t)
			unlock, err := lockStore(baselinePath(dataDir, ws)) // RebaselineIndex's lock
			if err != nil {
				t.Fatal(err)
			}
			held := make(chan string, 1)
			go func() { held <- EnsureRegistrationBaseline(context.Background(), dataDir, ws) }()
			for deadline := time.Now().Add(10 * time.Second); len(stateLog(t, state, "drift.log")) == 0; {
				if time.Now().After(deadline) {
					unlock()
					t.Fatal("registration never read drift")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if tc.rebuild {
				if _, err := buildBaseline(context.Background(), dataDir, ws, BaselineAdmin, "ops-admin"); err != nil {
					unlock()
					t.Fatal(err)
				}
			}
			unlock()
			if h := <-held; h != "" {
				t.Fatalf("held = %q", h)
			}
			if got := indexCalls(t, state); !slices.Equal(got, tc.wantIndex) {
				t.Fatalf("builds = %v, want %v", got, tc.wantIndex)
			}
			if got := len(stateLog(t, state, "drift.log")); got != tc.wantDrift {
				t.Fatalf("registration read drift %d times, want %d", got, tc.wantDrift)
			}
		})
	}
}

// A workspace registered before WS-22 (no baseline) gets one at its first ground, once,
// even when several grounds race for it: the one that takes the lock builds, the rest
// answer at once saying a build is in progress, and a later ground reports it.
func TestFirstGroundBuildsTheMissingBaselineOnce(t *testing.T) {
	state := baselineCore(t, "0.5")
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
	built := 0
	for _, g := range grounds {
		switch {
		case g == nil:
			t.Fatal("a ground failed")
		case g.Baseline != nil && g.Baseline.Reason == BaselineFirstGround && g.Baseline.Auto:
			built++
		case g.Baseline == nil && unknownReason(g, "baseline") == buildBusyHold:
		default:
			t.Fatalf("a ground neither reports the baseline nor the build in progress: %+v %v", g.Baseline, g.Unknown)
		}
	}
	if built == 0 {
		t.Fatal("no ground reported the baseline it built")
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil || g.Baseline == nil || g.Baseline.Reason != BaselineFirstGround {
		t.Fatalf("a later ground reports the baseline: %+v %v", g, err)
	}
	if evs := baselineHistory(t, dataDir, ws); len(evs) != 1 || evs[0].Note != BaselineFirstGround {
		t.Fatalf("history: %+v", evs)
	}
}

// A HEAD move rebuilds the baseline automatically whatever the worktree holds: an
// automatic baseline is the committed state, so uncommitted changes stay visible
// against the new one (the real-core lifecycle test shows their contract breaks).
func TestHeadMoveRebaselines(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	if _, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin"); err != nil {
		t.Fatal(err)
	}
	writeState(t, state, "head", "h2")
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := indexCalls(t, state); !slices.Equal(got, []string{"--reason=admin", "--reason=head_changed"}) {
		t.Fatalf("a HEAD move rebaselines: %v", got)
	}
	if b := g.Baseline; b == nil || b.Reason != BaselineHeadChanged || *b.Head != "h2" || !b.Auto || b.Held != "" {
		t.Fatalf("rebaselined = %+v (unknown %v)", b, g.Unknown)
	}
	var drift struct {
		HeadChanged bool `json:"head_changed"`
	}
	if json.Unmarshal(g.Drift, &drift) != nil || drift.HeadChanged {
		t.Fatalf("ground reports the drift after the rebuild: %s", g.Drift)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 2 || evs[1].Note != BaselineHeadChanged {
		t.Fatalf("history: %+v", evs)
	}
	if data := eventData(t, evs[1]); data["replaced"] != true || data["previous_head"] != "h1" {
		t.Fatalf("a rebaseline records what it replaced: %s", evs[1].Data)
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
	writeState(t, state, "fail", "")
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

// A stored baseline that exists but cannot be read is not replaced automatically (it
// may be an admin one): registration and ground hold, ground says why, and an explicit
// rebaseline replaces it and is recorded.
func TestUnreadableBaselineIsKeptForAnIndexer(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	stored := baselinePath(dataDir, ws)
	if err := os.WriteFile(stored, []byte("torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if held := EnsureRegistrationBaseline(context.Background(), dataDir, ws); !strings.HasPrefix(held, unreadableHold) {
		t.Fatalf("registration over an unreadable baseline: held %q", held)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := indexCalls(t, state); len(got) != 0 {
		t.Fatalf("an unreadable baseline was replaced automatically: %v", got)
	}
	if g.Baseline != nil || !strings.Contains(unknownReason(g, "baseline"), "index baseline unreadable: torn") {
		t.Fatalf("ground must say the baseline is unreadable: %+v %v", g.Baseline, g.Unknown)
	}
	if raw, _ := os.ReadFile(stored); string(raw) != "torn" {
		t.Fatalf("the unreadable baseline was touched: %q", raw)
	}
	if _, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin"); err != nil {
		t.Fatal(err)
	}
	if evs := baselineHistory(t, dataDir, ws); len(evs) != 1 || evs[0].Principal != "ops-admin" {
		t.Fatalf("history: %+v", evs)
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

// ground never waits on another baseline build either, in this process (the lock's
// mutex) or another (its flock, which the ops CLI or a second API holds): it answers at
// once and says a build is in progress.
func TestGroundNeverWaitsForAnotherBaselineBuild(t *testing.T) {
	for name, hold := range map[string]func(key string) (func(), error){
		"in process":    lockStore,
		"other process": func(key string) (func(), error) { return lockFile(key+".lock", true) },
	} {
		t.Run(name, func(t *testing.T) {
			state := baselineCore(t, "0")
			dataDir, ws, _ := seedBaselineWorkspace(t)
			unlock, err := hold(baselinePath(dataDir, ws))
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			g, err := BuildSessionGrounding(dataDir, ws)
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("ground waited %s for the baseline lock", d)
			}
			if got := indexCalls(t, state); len(got) != 0 {
				t.Fatalf("ground built under another holder: %v", got)
			}
			if got := unknownReason(g, "baseline"); got != buildBusyHold {
				t.Fatalf("ground must say a build is in progress: %q", got)
			}
		})
	}
}

// A build whose history cannot be recorded is discarded: the stored baseline stays as
// it was and nothing is staged, so the next attempt builds and records it again.
func TestBaselineIsNotReplacedUnrecorded(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	// the governance store cannot be opened: a directory stands where its file goes
	if err := os.Mkdir(memoryStorePath(dataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin"); err == nil || !strings.Contains(err.Error(), "discarded") {
		t.Fatalf("an unrecorded rebaseline must fail and say it was discarded: %v", err)
	}
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unknownReason(g, "baseline"), "history could not be recorded") {
		t.Fatalf("ground must report the discarded build: %v", g.Unknown)
	}
	for _, p := range []string{baselinePath(dataDir, ws), stagedBaselinePath(dataDir, ws)} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after unrecorded builds: %v", filepath.Base(p), err)
		}
	}
	if got := indexCalls(t, state); len(got) != 2 {
		t.Fatalf("each attempt builds: %v", got)
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
	if data := eventData(t, evs[0]); data["auto"] != false || data["reason"] != BaselineAdmin {
		t.Fatalf("history data: %s", evs[0].Data)
	}
	if _, err := RebaselineIndex(context.Background(), dataDir, "wsMissing", "ops-admin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unknown workspace: %v", err)
	}
}

// End to end with the real core. A workspace registered with an uncommitted signature
// change is baselined at HEAD, so ground reports that contract break instead of 0.
// Once the change is committed, the HEAD move rebaselines even with an untracked file
// and a new uncommitted edit, which is then reported against the new HEAD.
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
	handle := func(params string) {
		src := "package api\n\nfunc Handle(" + params + " int) int {\n\treturn 0\n}\n"
		if err := os.WriteFile(filepath.Join(root, "api.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	handle("a")
	git("add", "-A")
	git("commit", "-qm", "one")
	head := git("rev-parse", "HEAD")
	handle("a, b") // uncommitted when the workspace is registered

	snap, err := LoadWorkspace(dataDir, WorkspaceLoadRequest{RootPath: root, AutoScan: true, PreferCachedSnapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	ws := snap.Workspace.WorkspaceID
	StartRegistrationBaseline(dataDir, ws)
	waitRegistrationBaselines(t)
	g, err := BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if b := g.Baseline; b == nil || b.Head == nil || *b.Head != head || !b.Auto || b.Dirty || b.Reason != BaselineRegistration || b.IndexedAt == "" {
		t.Fatalf("ground baseline = %+v (unknown %v)", b, g.Unknown)
	}
	if g.ContractBreaks == nil || *g.ContractBreaks != 1 || !slices.Equal(g.BrokenContracts, []string{"Handle in api.go (params 1→2)"}) {
		t.Fatalf("the uncommitted signature change is a contract break, not 0: %+v (unknown %v)", g, g.Unknown)
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
	if json.Unmarshal(raw, &impact) != nil || !impact.HasBaseline || impact.ChangedFilesTotal == nil || *impact.ChangedFilesTotal != 1 ||
		impact.ContractBreaks == nil || *impact.ContractBreaks != 1 || impact.Truncation != nil {
		t.Fatalf("impact against the registration baseline: %s", raw)
	}

	git("commit", "-qam", "two")
	handle("a, b, c")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = BuildSessionGrounding(dataDir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if b := g.Baseline; b == nil || *b.Head != git("rev-parse", "HEAD") || b.Reason != BaselineHeadChanged || b.Dirty || b.Held != "" {
		t.Fatalf("a HEAD move rebaselines despite untracked and uncommitted files: %+v (unknown %v)", b, g.Unknown)
	}
	if g.ContractBreaks == nil || *g.ContractBreaks != 1 {
		t.Fatalf("the new uncommitted change is a contract break against the new HEAD: %+v (unknown %v)", g, g.Unknown)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 2 || evs[0].Note != BaselineRegistration || evs[1].Note != BaselineHeadChanged {
		t.Fatalf("history: %+v", evs)
	}
	if data := eventData(t, evs[0]); data["dirty"] != false || data["from_head"] != 1.0 {
		t.Fatalf("the registration baseline took the edited path from HEAD: %s", evs[0].Data)
	}
	if data := eventData(t, evs[1]); data["previous_head"] != head {
		t.Fatalf("the rebaseline records the HEAD it replaced: %s", evs[1].Data)
	}
}
