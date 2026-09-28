package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A request mixing impact forms, or out of range, is a 400 before any core runs.
func TestImpactRouteRefusesMixedFormsBeforeRunningTheCore(t *testing.T) {
	srv, dir := newRouteServer(t)
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "core-ran")
	core := filepath.Join(bin, "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\ntouch "+ran+"\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpact", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"?from=a",
		"?symbol=x&scope=all",
		"?symbol=x&depth=33",
		"?from=a&to=b&direction=upstream",
		"?scope=range&base=-x",
		"?depth=2",
		// what the MCP tool forwards when a caller mixes forms (every argument reaches the API)
		"?symbol=X&scope=staged",
		"?from=a&to=b&direction=downstream&limit=5",
		"?scope=all&direction=downstream",
		"?direction=downstream",
		// a scope's revision rules, a NaN confidence and a relative worktree
		"?scope=compare",
		"?scope=staged&head=HEAD",
		"?scope=commit&base=main",
		"?symbol=x&min_confidence=NaN",
		"?scope=compare&base=main&worktree=wt",
	} {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", q, resp.StatusCode)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a refused impact request ran the core")
	}
}

// The core's refusal of a scope= request (a revision that names no commit) is a 400
// that names it; any other core failure stays a sanitized 500.
func TestImpactScopeRefusedByTheCoreIsABadRequest(t *testing.T) {
	srv, dir := newRouteServer(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := `#!/bin/sh
if [ "$1 $2" = "symbolgraph changes" ]; then
  case "$*" in
  *--base=nope*) echo 'symbolgraph changes: revision "nope" does not name a commit' >&2; exit 2 ;;
  esac
  echo 'boom /secret/host/path' >&2; exit 1
fi
echo '{}'
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpactRefused", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q, has, hasNot string
		status         int
	}{
		{"?scope=range&base=nope", `does not name a commit`, "", http.StatusBadRequest},
		{"?scope=range&base=main", "", "/secret/host/path", http.StatusInternalServerError},
	} {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index" + c.q)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || !strings.Contains(string(body), c.has) ||
			(c.hasNot != "" && strings.Contains(string(body), c.hasNot)) {
			t.Fatalf("%s: %d %s", c.q, resp.StatusCode, body)
		}
	}
}
