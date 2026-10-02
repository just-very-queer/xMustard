package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Issue and view ids are validated at create, so both POST routes answer 400 for an
// id the read layer would refuse, and still answer 200 for the ids other producers
// mint. POST /issues routes errors through respondError; POST /views routes through
// notFoundWorkspace, which answers 404 for a missing workspace and 500 for anything
// else, so the second route is the one that would silently report a refusal as a
// server fault.
func TestIssueAndViewCreateRoutesRefuseAnUnusableIdWith400(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{Profile: profilePlatform})
	ws := "wsA"
	seedIssueWorkspace(t, dir, ws)
	token := mint(t, dir, "owner", roleAdmin)
	base := srv.URL + "/api/workspaces/" + ws

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"issue traversal", base + "/issues", `{"title":"t","severity":"p2","bug_id":"../../escape"}`, http.StatusBadRequest},
		{"issue nul", base + "/issues", `{"title":"t","severity":"p2","bug_id":"TRK_0001\u0000"}`, http.StatusBadRequest},
		{"issue ok", base + "/issues", `{"title":"t","severity":"p2","bug_id":"BSS-42"}`, http.StatusOK},
		{"issue generated", base + "/issues", `{"title":"t","severity":"p2"}`, http.StatusOK},
		{"view traversal", base + "/views", `{"name":"../../escape"}`, http.StatusBadRequest},
		{"view nul", base + "/views", `{"name":"view\u0000name"}`, http.StatusBadRequest},
		{"view ok", base + "/views", `{"name":"Agent Review"}`, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := call(t, "POST", c.path, token, c.body, nil)
			if code != c.want {
				t.Fatalf("POST %s: want %d, got %d: %v", c.path, c.want, code, body)
			}
		})
	}
}

// The id a create returns must be one the read routes serve: a stored id the URL
// layer refuses would leave an object that can be created but never read again. The
// view list answers a bare JSON array, so it is read with its own request.
func TestCreatedIssueAndViewAreReadableThroughTheirRoutes(t *testing.T) {
	srv, dir := securityServer(t, exposurePosture{Profile: profilePlatform})
	ws := "wsA"
	seedIssueWorkspace(t, dir, ws)
	token := mint(t, dir, "owner", roleAdmin)
	base := srv.URL + "/api/workspaces/" + ws

	code, body := call(t, "POST", base+"/issues", token, `{"title":"t","severity":"p2","bug_id":"BSS-42"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("create issue: %d %v", code, body)
	}
	issueID, _ := body["bug_id"].(string)
	if issueID == "" {
		t.Fatalf("create issue returned no bug_id: %v", body)
	}
	if code, body := call(t, "GET", base+"/issues/"+issueID, token, "", nil); code != http.StatusOK {
		t.Fatalf("read back issue %q: want 200, got %d: %v", issueID, code, body)
	}

	code, body = call(t, "POST", base+"/views", token, `{"name":"Agent Review"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("create view: %d %v", code, body)
	}
	viewID, _ := body["view_id"].(string)
	if viewID == "" {
		t.Fatalf("create view returned no view_id: %v", body)
	}
	// Update and delete resolve the stored view by its id, so both answer 200 only
	// when the id the create minted is one the URL layer carries.
	if code, body := call(t, "PUT", base+"/views/"+viewID, token, `{"name":"Agent Review"}`, nil); code != http.StatusOK {
		t.Fatalf("update view %q: want 200, got %d: %v", viewID, code, body)
	}
	if code, body := call(t, "DELETE", base+"/views/"+viewID, token, "", nil); code != http.StatusOK {
		t.Fatalf("delete view %q: want 200, got %d: %v", viewID, code, body)
	}
}

// seedIssueWorkspace writes the snapshot CreateIssue and CreateSavedView load.
func seedIssueWorkspace(t *testing.T, dir, ws string) {
	t.Helper()
	snap, _ := json.Marshal(map[string]any{
		"workspace": map[string]any{"workspace_id": ws, "root_path": t.TempDir()},
		"summary":   map[string]int{"issues": 0},
	})
	path := filepath.Join(dir, "workspaces", ws, "snapshot.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, snap, 0o644); err != nil {
		t.Fatal(err)
	}
}