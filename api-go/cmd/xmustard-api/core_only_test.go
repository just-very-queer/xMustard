package main

import "testing"

func TestIsCorePath(t *testing.T) {
	core := []string{
		"/api/health",
		"/api/workspaces",
		"/api/auth/whoami",
		"/api/workspaces/ws1/session-grounding",
		"/api/workspaces/ws1/context/active",
		"/api/workspaces/ws1/context/ctx_1/verify",
		"/api/workspaces/ws1/search",
		"/api/workspaces/ws1/explain-path",
		"/api/workspaces/ws1/changes/since-index",
		"/api/workspaces/ws1/diagnostics",
		"/api/workspaces/ws1/runs/run-1/why-failed",
	}
	for _, p := range core {
		if !isCorePath(p) {
			t.Errorf("expected %q to be a core path", p)
		}
	}
	platform := []string{
		"/api/workspaces/ws1/subsystems",
		"/api/workspaces/ws1/eval-timeline",
		"/api/providers",
		"/api/route",
		"/api/workspaces/ws1/hotspots",
	}
	for _, p := range platform {
		if isCorePath(p) {
			t.Errorf("expected %q to be blocked in CORE_ONLY mode", p)
		}
	}
}
