package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// XM_ROUTE_DUMP=<file> writes the answer of every route registerRoutes mounts to a
// fixed set of requests: the status, the content type and the body, with directories,
// the workspace id and timestamps masked. Two trees' dumps compare line by line, so a
// change to the route tables can be checked to keep every route's answers. Each route is asked three times (no query or body, a query naming every
// parameter with its own value plus an empty JSON object, a malformed body) in two
// data directories: an empty one, and one with a registered git work tree (scanned
// when XM_ROUTE_DUMP_CORE names a core binary). Skipped unless XM_ROUTE_DUMP is set.
func TestRouteAnswerDump(t *testing.T) {
	out := os.Getenv("XM_ROUTE_DUMP")
	if out == "" {
		t.Skip("set XM_ROUTE_DUMP=<file> to dump every route's answers")
	}
	var b strings.Builder
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core") // no core spawns
	dumpRouteAnswers(t, &b, "empty", "w1", t.TempDir(), nil)

	dir, repo := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=dump", "-c", "user.email=dump@example.invalid", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if msg, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, msg)
		}
	}
	core := os.Getenv("XM_ROUTE_DUMP_CORE")
	if core != "" {
		t.Setenv("XMUSTARD_CORE_BIN", core)
	}
	snap, err := workspaceops.LoadWorkspace(dir, workspaceops.WorkspaceLoadRequest{RootPath: repo, AutoScan: core != ""})
	id := ""
	if snap != nil {
		id = snap.Workspace.WorkspaceID
	} else if list, lerr := workspaceops.ListWorkspacesScoped(dir, nil); lerr == nil && len(list) == 1 {
		id = list[0].WorkspaceID
	} else {
		t.Fatalf("register the dump workspace: %v %v", err, lerr)
	}
	t.Setenv("XMUSTARD_CORE_BIN", "/nonexistent/xmustard-core")
	dumpRouteAnswers(t, &b, "loaded", id, dir, strings.NewReplacer(repo, "<REPO>", id, "<WS>"))
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dumpRouteAnswers(t *testing.T, b *strings.Builder, pass, workspaceID, dir string, masks *strings.Replacer) {
	t.Helper()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	mux := newGatedMux()
	registerRoutes(mux)
	patterns := make([]string, 0, len(mux.gates))
	for p := range mux.gates {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	query := "?" + strings.Join([]string{
		"q=qq", "query=qry", "path=main.go", "relative_path=.", "symbol=sym", "from=f1", "to=t1", "limit=3", "offset=2",
		"line=4", "column=5", "language=go", "lang=go", "pattern=pat", "path_glob=*.go", "base_ref=HEAD", "status=st",
		"filter=all", "issue_id=issue2", "run_id=run2", "profile_id=profile2", "runbook_id=runbook2", "scenario_id=scenario2",
		"diagnostic_run_id=diag2", "report_path=cover.out", "workspace_id=" + workspaceID, "prompt=pr", "model=mo",
		"task_hint=th", "event=ev", "message=msg", "provider=github", "repo=o/r", "state=open", "severity=high",
		"issue_status=open", "source=src", "label=lb", "promoted=true", "needs_followup=true", "review_ready_only=true",
		"drift_only=true", "include_declaration=false", "embed_model=em", "seed=sd", "cursor=cu", "depth=2",
	}, "&")
	variants := []struct{ name, query, body string }{
		{"bare", "", ""},
		{"query", query, "{}"},
		{"malformed", "", "{"},
	}
	param := regexp.MustCompile(`\{([^}]+)\}`)
	stamp := regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	randomID := regexp.MustCompile(`"((int|src|semanticq)_[0-9a-f]{10,16}|untitled-view-[0-9a-f]{8}|[0-9a-f]{40})"`) // per-run ids and hashes
	for _, p := range patterns {
		method := patternMethod(p)
		if method == "" {
			method = "GET"
		}
		path := param.ReplaceAllStringFunc(patternPath(p), func(m string) string {
			if m == "{workspace_id}" {
				return workspaceID
			}
			return strings.TrimSuffix(strings.Trim(m, "{}"), "_id") + "1"
		})
		for _, v := range variants {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(method, path+v.query, strings.NewReader(v.body)))
			body := strings.ReplaceAll(strings.TrimSpace(rec.Body.String()), dir, "<DATA>")
			if masks != nil {
				body = masks.Replace(body)
			}
			if p == "/api/health" {
				body = "<live process figures>"
			}
			fmt.Fprintf(b, "%s | %s | %s | %d | %s | %s\n", pass, p, v.name, rec.Code, rec.Header().Get("Content-Type"), randomID.ReplaceAllString(stamp.ReplaceAllString(body, "<TIME>"), `"<ID>"`))
		}
	}
}
