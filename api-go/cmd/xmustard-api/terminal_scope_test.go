package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// A workspace-scoped token must be denied terminal access to a workspace outside
// its scope, even though terminal routes live under /api/terminal (where the
// path-based middleware scope check can't fire) — XM-PRO-001.
func TestRequireTerminalScope(t *testing.T) {
	dd := t.TempDir()
	scoped := &workspaceops.Principal{ID: "agent-a", Role: "agent", Workspaces: []string{"ws-a"}}
	req := func(p *workspaceops.Principal) *http.Request {
		r := httptest.NewRequest("POST", "/api/terminal/open", nil)
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalCtxKey, p))
		}
		return r
	}

	// scoped token, foreign workspace -> 403, handled
	rec := httptest.NewRecorder()
	if requireTerminalScope(rec, req(scoped), dd, "ws-b") {
		t.Fatal("scoped token must be denied for a foreign workspace")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign workspace: want 403, got %d", rec.Code)
	}

	// scoped token, own workspace -> allowed
	if !requireTerminalScope(httptest.NewRecorder(), req(scoped), dd, "ws-a") {
		t.Fatal("scoped token must be allowed for its own workspace")
	}

	// no principal (open mode) -> allowed (backward-compatible)
	if !requireTerminalScope(httptest.NewRecorder(), req(nil), dd, "ws-x") {
		t.Fatal("open-mode (no principal) must pass")
	}

	// empty workspace id -> 400
	rec = httptest.NewRecorder()
	if requireTerminalScope(rec, req(scoped), dd, "  ") {
		t.Fatal("empty workspace id must be rejected")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty workspace: want 400, got %d", rec.Code)
	}
}
