package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/workspaceops"
)

// WS-22 (PAR-FRESH-06): an agent cannot reset the index baseline; an explicit
// rebaseline by the indexer or admin role is recorded in the history with its principal.
// roomyGovernor admits heavy work whatever this test binary's own memory is.
func roomyGovernor(t *testing.T) {
	t.Helper()
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40, HeavyWait: 30 * time.Second})
	t.Cleanup(func() { budget.Gov = prev })
}

func TestAgentCannotResetTheBaselineAndRebaselinesAreRecorded(t *testing.T) {
	roomyGovernor(t)
	srv, dir := securityServer(t, exposurePosture{})
	seedCoreWorkspace(t, dir, "wsIdx")
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `case "$1 $2" in
"changetrack index") printf '{"workspace_id":"wsIdx","head":"h2","branch":"main","indexed_at":"2026-09-28T00:00:00Z","auto":false,"reason":"admin","dirty":false,"tracked_files":1,"signatures":0,"replaced":true,"previous_head":"h1"}' ;;
*) echo '{}' ;;
esac
`))
	history := func() []string {
		t.Helper()
		evs, err := workspaceops.IndexBaselineHistory(context.Background(), dir, "wsIdx", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var who []string
		for _, ev := range evs {
			who = append(who, ev.Principal)
		}
		return who
	}
	url := srv.URL + "/api/workspaces/wsIdx/index"
	if code, body := call(t, "POST", url, mint(t, dir, "agentA", "agent"), "", nil); code != http.StatusForbidden || body["missing_role"] != "indexer" {
		t.Fatalf("agent reset: want 403 naming indexer, got %d %v", code, body)
	}
	if got := history(); len(got) != 0 {
		t.Fatalf("a denied reset is not a rebaseline: %v", got)
	}
	for i, who := range []struct{ id, role string }{{"opsAdmin", "admin"}, {"ciIndexer", "indexer"}} {
		code, body := call(t, "POST", url, mint(t, dir, who.id, who.role), "", nil)
		if code != http.StatusOK || body["auto"] != false || body["reason"] != "admin" || body["previous_head"] != "h1" {
			t.Fatalf("%s rebaseline: %d %v", who.role, code, body)
		}
		if seq, _ := body["history_seq"].(float64); seq <= 0 {
			t.Fatalf("%s rebaseline names its history event: %v", who.role, body)
		}
		if got := history(); len(got) != i+1 || got[i] != who.id {
			t.Fatalf("history after %s: %v", who.role, got)
		}
	}
}

// Acceptance: on a fresh install in the core profile, registering a repository
// baselines it, and ground and impact answer from that baseline with nothing unknown.
func TestFreshInstallGroundAndImpactInTheCoreProfile(t *testing.T) {
	core := realCoreBinary(t)
	roomyGovernor(t)
	srv, dir := securityServer(t, exposurePosture{})
	t.Setenv("XMUSTARD_CORE_BIN", core)
	t.Setenv("XMUSTARD_CORE_WORKER", "")
	root := t.TempDir()
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
	admin, agent := mint(t, dir, "opsAdmin", "admin"), mint(t, dir, "agentA", "agent")

	body, _ := json.Marshal(map[string]any{"root_path": root, "auto_scan": true, "prefer_cached_snapshot": true})
	code, snap := call(t, "POST", srv.URL+"/api/workspaces/load", admin, string(body), nil)
	if code != http.StatusOK {
		t.Fatalf("register: %d %v", code, snap)
	}
	ws, _ := snap["workspace"].(map[string]any)["workspace_id"].(string)
	base := srv.URL + "/api/workspaces/" + ws

	code, g := call(t, "GET", base+"/session-grounding", agent, "", nil)
	b, _ := g["baseline"].(map[string]any)
	if code != http.StatusOK || b["head"] != git("rev-parse", "HEAD") || b["auto"] != true || b["reason"] != "registration" || b["indexed_at"] == "" {
		t.Fatalf("ground on a fresh install: %d %v", code, g)
	}
	if g["unknown"] != nil || g["changed_files"] != 0.0 || g["contract_breaks"] != 0.0 {
		t.Fatalf("a fresh baseline grounds with nothing unknown: %v", g)
	}
	code, impact := call(t, "GET", base+"/changes/since-index", agent, "", nil)
	if code != http.StatusOK || impact["has_baseline"] != true || impact["changed_files_total"] != 0.0 || impact["contract_breaks"] != 0.0 || impact["truncation"] != nil {
		t.Fatalf("impact on a fresh install: %d %v", code, impact)
	}
}

// explain's clustering (its second core call, cluster-of) runs on the request context:
// cancelling the request kills that child too, not only the first call's.
func TestExplainClusteringIsCancelledWithTheRequest(t *testing.T) {
	srv, dir := newRouteServer(t)
	seedCoreWorkspace(t, dir, "wsExplain")
	var snap struct {
		Workspace struct {
			RootPath string `json:"root_path"`
		} `json:"workspace"`
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "workspaces", "wsExplain", "snapshot.json"))
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap.Workspace.RootPath, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pidDir := t.TempDir()
	t.Setenv("XMUSTARD_CORE_BIN", writeScript(t, `case "$1 $2" in
"symbolgraph cluster-of") echo $$ > "`+pidDir+`/$$.pid"; exec sleep 60 ;;
explain-path*) printf '{"workspace_id":"wsExplain","path":"a.go","role":"code","line_count":1}' ;;
*) echo '{}' ;;
esac
`))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/wsExplain/explain-path?path=a.go", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	pid := waitForChildPID(t, pidDir, 10*time.Second)
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	<-done
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("the clustering child %d outlived the cancelled explain request", pid)
	}
}
