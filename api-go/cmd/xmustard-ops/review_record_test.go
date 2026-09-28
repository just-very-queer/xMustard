//go:build review

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// opsReviewRepo registers a workspace over a repository whose feature branch (checked
// out) adds b.go on top of main.
func opsReviewRepo(t *testing.T) (dataDir, ws string) {
	t.Helper()
	dataDir, root := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
			"GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.go", "package a\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	write("b.go", "package a\n\nfunc B() {}\n")
	git("add", ".")
	git("commit", "-q", "-m", "change")
	loaded, err := workspaceops.LoadWorkspace(dataDir, workspaceops.WorkspaceLoadRequest{RootPath: root, AutoScan: true})
	if err != nil {
		t.Fatal(err)
	}
	return dataDir, loaded.Workspace.WorkspaceID
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// record stores a review under one agent's token, triage takes another's verdict (the
// author's confirm is refused), show prints the record, and a human approval can cite it.
func TestReviewRecordShowAndTriageCommands(t *testing.T) {
	dataDir, ws := opsReviewRepo(t)
	mintAgent := func(id string) string {
		t.Helper()
		raw, err := workspaceops.MintIdentityToken(dataDir, id, "agent", 0, nil, workspaceops.TokenIdentity{})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	author, peer := mintAgent("rev-a"), mintAgent("rev-b")
	findings := writeFile(t, `[{"path": "b.go", "start_line": 3, "end_line": 3, "category": "Bug", "severity": "high",
		"content": "B is exported and undocumented", "existing_code": "func B() {}"}]`)
	coverage := writeFile(t, `[{"path": "b.go", "status": "reviewed"}]`)
	e, out, errOut := testEnv(map[string]string{"XMUSTARD_API_TOKEN": author}, "")
	if code := runOps(e, "review", "record", ws, "--base", "main", "--findings", findings, "--coverage", coverage,
		"--data-dir", dataDir); code != 0 {
		t.Fatalf("record exit %d: %s", code, errOut)
	}
	res := decodeOut(t, out)
	rec := res["record"].(map[string]any)
	f := res["findings"].([]any)[0].(map[string]any)
	if rec["author"] != "rev-a" || rec["changed_files"] != float64(1) || rec["terminal_state"] != "complete" ||
		f["anchor_status"] != "exact_new" || f["start_line"] != float64(3) || f["support"] != "supported" || f["category"] != "bug" ||
		!strings.Contains(res["label"].(string), "evidence only") {
		t.Fatalf("record: %v", res)
	}
	recordID, findingID := rec["id"].(string), f["id"].(string)

	if code := runOps(e, "review", "triage", ws, "--finding", findingID, "--verdict", "confirm", "--data-dir", dataDir); code != exitError ||
		!strings.Contains(errOut.String(), "cannot confirm or dismiss") {
		t.Fatalf("the author's confirm: exit %d: %s", code, errOut)
	}
	peerFile := writeFile(t, peer)
	if code := runOps(e, "review", "triage", ws, "--finding", findingID, "--verdict", "confirm", "--token-file", peerFile,
		"--data-dir", dataDir); code != 0 {
		t.Fatalf("a peer's confirm: exit %d: %s", code, errOut)
	}
	if got := decodeOut(t, out)["finding"].(map[string]any); got["status"] != "confirmed" || got["corroborations"] != float64(1) {
		t.Fatalf("triage: %v", got)
	}
	if code := runOps(e, "review", "show", ws, "--record", recordID, "--data-dir", dataDir); code != 0 {
		t.Fatalf("show exit %d: %s", code, errOut)
	}
	if shown := decodeOut(t, out); shown["findings"].([]any)[0].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("show: %v", shown)
	}

	approver := mintApprover(t, dataDir, "alice", workspaceops.PrincipalHuman)
	h, hOut, hErr := testEnv(map[string]string{"XMUSTARD_APPROVER_TOKEN": approver}, "")
	if code := runOps(h, "review", "approve", ws, "--base", "main", "--review", recordID, "--data-dir", dataDir); code != 0 {
		t.Fatalf("approve exit %d: %s", code, hErr)
	}
	if a := decodeOut(t, hOut); a["review_records_unknown"] != nil {
		t.Fatalf("a known record listed as unknown: %v", a)
	}

	for _, args := range [][]string{
		{"review", "record", ws, "--data-dir", dataDir},                         // no --base
		{"review", "triage", ws, "--finding", findingID, "--data-dir", dataDir}, // no --verdict
		{"review", "show", ws, "--data-dir", dataDir},                           // no --record
	} {
		if code := runOps(e, args...); code != exitUsage {
			t.Fatalf("%v: want usage exit, got %d", args, code)
		}
	}
	reader, err := workspaceops.MintIdentityToken(dataDir, "reader-1", workspaceops.RoleReader, 0, nil, workspaceops.TokenIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	r, _, rErr := testEnv(map[string]string{"XMUSTARD_API_TOKEN": reader}, "")
	if code := runOps(r, "review", "record", ws, "--base", "main", "--data-dir", dataDir); code != exitError ||
		!strings.Contains(rErr.String(), "proposer") {
		t.Fatalf("a reader recording: exit %d: %s", code, rErr)
	}
}
