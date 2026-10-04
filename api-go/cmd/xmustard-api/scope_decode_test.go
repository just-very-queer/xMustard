package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// The scope keys must reach the derivation over the real transport. The parked session's
// finding (round 3, r3-2) was that RememberRequest's own run_id and session_id shadow
// the embedded proposal's, so an HTTP remember with visibility=run or visibility=session
// is REFUSED while the tests, which build the embedded fields directly, pass. Every case
// here goes through a JSON body, so it is the decode path that is pinned rather than the
// struct.
func TestTheHTTPRememberDecodeReachesTheScopeKeys(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsScopeDecode"
	raw, err := workspaceops.MintToken(dir, "alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) (int, map[string]any) {
		t.Helper()
		return sendJSON(t, "POST", base+"/context", raw, body)
	}
	// The scope keys are their own JSON names, distinct from the provenance run_id and
	// session_id that shadow the embedded proposal's fields; a run scope also names a run
	// that must exist, so the run record is written first, and a run or session key is
	// ASSERTED, so the principal has to be one the operator trusts.
	writeTestRunRecord(t, dir, "wsScopeDecode", "run_1")
	if _, err := workspaceops.UpdateSettings(dir, workspaceops.AppSettings{
		TrustedRunKeyPrincipals: []string{"alice"}}); err != nil {
		t.Fatalf("trust alice for run keys: %v", err)
	}

	for _, c := range []struct {
		name, body, wantScope, wantKey string
	}{
		{"private", `{"content":"the staging credentials live in vault omega","visibility":"private"}`,
			workspaceops.ScopePrivate, "alice"},
		{"dir", `{"content":"the resolver drops unexported edges omega","visibility":"dir","dir":"api-go/internal"}`,
			workspaceops.ScopeDir, "api-go/internal"},
		{"session", `{"content":"the session found the leak omega","visibility":"session","scope_session_id":"claude:s1"}`,
			workspaceops.ScopeSession, "claude:s1"},
		{"run", `{"content":"the run found the leak omega","visibility":"run","run_id":"run_1","scope_run_id":"run_1"}`,
			workspaceops.ScopeRun, "run_1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out := call(c.body)
			if code != http.StatusOK {
				t.Fatalf("the %s key did not reach the derivation over HTTP: %d %v", c.name, code, out)
			}
			if out["scope"] != c.wantScope || out["scope_key"] != c.wantKey {
				t.Fatalf("stored %q/%q, want %q/%q", out["scope"], out["scope_key"], c.wantScope, c.wantKey)
			}
		})
	}
}

// The refusal survives the decode: a run scope with no key is refused, and it is refused
// as bad input rather than stored workspace-wide, which is the direction the derivation
// takes when it cannot honour the request.
func TestTheHTTPRememberDecodeStillRefusesAScopeWithNoKey(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsScopeDecodeRefuse"
	raw, err := workspaceops.MintToken(dir, "alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"content":"a run scope with no key omega","visibility":"run"}`,
		`{"content":"a session scope with no key omega","visibility":"session"}`,
		`{"content":"a dir scope with no key omega","visibility":"dir"}`,
	} {
		code, out := sendJSON(t, "POST", base+"/context", raw, body)
		if code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d %v", body, code, out)
		}
	}
}

// writeTestRunRecord writes the run record a run-scoped key names, so the provenance
// check has a run to resolve.
func writeTestRunRecord(t *testing.T, dir, ws, runID string) {
	t.Helper()
	rec := map[string]any{"run_id": runID, "workspace_id": ws, "status": "succeeded"}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workspaces", ws, "runs", runID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
