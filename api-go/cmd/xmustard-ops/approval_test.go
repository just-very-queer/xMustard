package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// testEnv is an opsEnv with captured output, a fixed environment and a terminal that
// "types" typed (or has none when typed is "").
func testEnv(env map[string]string, typed string) (opsEnv, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return opsEnv{
		stdout: &out, stderr: &errOut,
		getenv: func(k string) string { return env[k] },
		prompt: func(string) (string, error) {
			if typed == "" {
				return "", errors.New("no controlling terminal")
			}
			return typed, nil
		},
	}, &out, &errOut
}

func runOps(e opsEnv, args ...string) int { return approvalCommands[args[0]](e, args[1:]) }

func mintApprover(t *testing.T, dir, id, kind string) string {
	t.Helper()
	raw, err := workspaceops.MintIdentityToken(dir, id, workspaceops.RoleHumanApprover, 0, nil,
		workspaceops.TokenIdentity{Owner: id, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeOut(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	out.Reset()
	return v
}

func TestApproveRejectAndQueueNeedAHumanApprover(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	token := mintApprover(t, dir, "alice", workspaceops.PrincipalHuman)
	agentToken := mintApprover(t, dir, "bot", workspaceops.PrincipalAgent)
	var ids []string
	for _, content := range []string{"the build uses make", "tests need docker", "lint with golangci"} {
		e, err := workspaceops.Remember(dir, ws, workspaceops.RememberRequest{ProposeContextRequest: workspaceops.ProposeContextRequest{
			Title: "t", Content: content}}, workspaceops.ContextActor{ID: "author"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}

	typed, out, _ := testEnv(nil, token)
	if code := runOps(typed, "queue", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("queue exit %d", code)
	}
	if q := decodeOut(t, out); q["total"] != float64(3) || q["approver"] != "alice" {
		t.Fatalf("queue: %v", q)
	}

	// a typed token is user presence; the environment and a file are advisory
	if code := runOps(typed, "approve", ws, ids[0], "--note", "read it", "--data-dir", dir); code != 0 {
		t.Fatalf("approve exit %d", code)
	}
	if res := decodeOut(t, out); res["approval"] != "ops/user_presence" || res["verdict"] != "approve" {
		t.Fatalf("approve: %v", res)
	}
	fromEnv, out, _ := testEnv(map[string]string{"XMUSTARD_APPROVER_TOKEN": token}, "")
	if code := runOps(fromEnv, "reject", ws, ids[1], "--data-dir", dir); code != 0 {
		t.Fatalf("reject exit %d", code)
	}
	if res := decodeOut(t, out); res["approval"] != "ops/advisory" || res["verdict"] != "reject" {
		t.Fatalf("reject: %v", res)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, out, _ := testEnv(nil, "")
	if code := runOps(fromFile, "approve", ws, ids[2], "--token-file", file, "--data-dir", dir); code != 0 {
		t.Fatalf("approve from a file exit %d", code)
	}
	if res := decodeOut(t, out); res["approval"] != "ops/advisory" {
		t.Fatalf("file token: %v", res)
	}

	// refused: an agent-kind token, no terminal and no token, and bad usage
	agent, out, errOut := testEnv(map[string]string{"XMUSTARD_APPROVER_TOKEN": agentToken}, "")
	if code := runOps(agent, "approve", ws, ids[0], "--data-dir", dir); code != exitError || out.Len() != 0 ||
		!strings.Contains(errOut.String(), "of kind agent, not human") {
		t.Fatalf("an agent token approved: exit %d %s %s", code, out, errOut)
	}
	view, err := workspaceops.GetContextEntry(dir, ws, ids[0], false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range view["entry"].(workspaceops.ContextEntry).Verifications {
		if v.Agent == "bot" {
			t.Fatalf("a refused approver's vote was recorded: %+v", v)
		}
	}
	none, _, errOut := testEnv(nil, "")
	if code := runOps(none, "queue", ws, "--data-dir", dir); code != exitError || !strings.Contains(errOut.String(), "no controlling terminal") {
		t.Fatalf("no token: exit %d %s", code, errOut)
	}
	for _, args := range [][]string{{"approve", ws}, {"reject"}, {"queue"}, {"approve", ws, ids[0], "extra"}, {"queue", ws, "--limit", "x"}} {
		if code := runOps(typed, args...); code != exitUsage {
			t.Fatalf("%v: want usage exit, got %d", args, code)
		}
	}
}

func TestReviewApproveRevokeAndGate(t *testing.T) {
	dataDir, root := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
			"GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	commit := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", name)
	}
	git("init", "-q", "-b", "main")
	commit("a.go", "package a\n")
	git("checkout", "-q", "-b", "feature")
	commit("b.go", "package a\n\nfunc B() {}\n")
	loaded, err := workspaceops.LoadWorkspace(dataDir, workspaceops.WorkspaceLoadRequest{RootPath: root, AutoScan: true})
	if err != nil {
		t.Fatal(err)
	}
	ws := loaded.Workspace.WorkspaceID
	token := mintApprover(t, dataDir, "alice", workspaceops.PrincipalHuman)
	e, out, _ := testEnv(map[string]string{"XMUSTARD_APPROVER_TOKEN": token}, "")
	gate := func(want int) map[string]any {
		t.Helper()
		if code := runOps(e, "review", "gate", ws, "--data-dir", dataDir); code != want {
			t.Fatalf("gate exit %d, want %d: %s", code, want, out)
		}
		return decodeOut(t, out)
	}
	if st := gate(3); st["status"] != "none" {
		t.Fatalf("gate: %v", st)
	}
	if code := runOps(e, "review", "approve", ws, "--base", "main", "--review", "r-1", "--data-dir", dataDir); code != 0 {
		t.Fatalf("review approve exit %d", code)
	}
	a := decodeOut(t, out)
	if a["assurance"] != "advisory" || a["head"] != git("rev-parse", "HEAD") || a["approver_kind"] != "human" ||
		!strings.Contains(a["enforcement"].(string), "branch protection") {
		t.Fatalf("attestation: %v", a)
	}
	if st := gate(0); st["status"] != "current" {
		t.Fatalf("gate: %v", st)
	}
	commit("c.go", "package a\n")
	if st := gate(4); st["status"] != "stale" {
		t.Fatalf("gate after a new commit: %v", st)
	}
	seq := strconv.FormatInt(int64(a["seq"].(float64)), 10)
	if code := runOps(e, "review", "revoke", ws, "--approval", seq, "--reason", "superseded", "--data-dir", dataDir); code != 0 {
		t.Fatalf("revoke exit %d", code)
	}
	if r := decodeOut(t, out); r["revoked"].(map[string]any)["reason"] != "superseded" {
		t.Fatalf("revoke: %v", r)
	}
	if st := gate(3); st["status"] != "none" || st["revoked"] != float64(1) {
		t.Fatalf("gate after revoking: %v", st)
	}
	for _, args := range [][]string{
		{"review", "approve", ws, "--data-dir", dataDir},                   // no --base
		{"review", "revoke", ws, "--approval", "1", "--data-dir", dataDir}, // no reason
		{"review", "merge", ws},                                            // unknown subcommand
		{"review", "gate"},                                                 // no workspace
	} {
		if code := runOps(e, args...); code != exitUsage {
			t.Fatalf("%v: want usage exit, got %d", args, code)
		}
	}
}
