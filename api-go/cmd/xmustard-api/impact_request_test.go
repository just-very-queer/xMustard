package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

func TestImpactFormTable(t *testing.T) {
	cases := []struct {
		params map[string]string
		want   workspaceops.ImpactForm
		bad    bool
	}{
		{map[string]string{"from": "a", "to": "b", "symbol": "s"}, workspaceops.ImpactTrace, false},
		{map[string]string{"symbol": "s", "scope": "all"}, workspaceops.ImpactBlast, false},
		{map[string]string{"scope": "all"}, workspaceops.ImpactChanges, false},
		{map[string]string{}, workspaceops.ImpactSinceIndex, false},
		{map[string]string{"from": "a"}, "", true},
		{map[string]string{"to": "b", "symbol": "s"}, "", true},
	}
	for _, c := range cases {
		got, err := impactForm(c.params)
		if (err != nil) != c.bad || got != c.want {
			t.Fatalf("%v: got %q %v", c.params, got, err)
		}
	}
}

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
