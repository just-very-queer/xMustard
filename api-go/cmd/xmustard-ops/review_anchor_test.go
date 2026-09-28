//go:build review

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/evidence"
	"xmustard/api-go/internal/workspaceops"
)

const anchorFindings = `{"status":"complete","comments":[{"path":"a.go","content":"returns the wrong value",` +
	`"existing_code":"func A() int { return 2 }","category":"bug","severity":"high","start_line":3,"end_line":3,"thinking":"t"}]}`

// anchorWorkspace registers a repository whose feature branch changes a.go.
func anchorWorkspace(t *testing.T) (dir, ws string) {
	t.Helper()
	dir, root := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("package a\n\nfunc A() int { return 1 }\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	write("package a\n\nfunc A() int { return 2 }\n")
	git("commit", "-q", "-am", "change")
	snap, err := workspaceops.LoadWorkspace(dir, workspaceops.WorkspaceLoadRequest{RootPath: root, AutoScan: true})
	if err != nil {
		t.Fatal(err)
	}
	return dir, snap.Workspace.WorkspaceID
}

// capture retains body in the workspace's evidence store, issued to actor ("" for an
// unauthenticated capture).
func capture(t *testing.T, dir, ws, actor, body string) string {
	t.Helper()
	s := evidence.NewStore(dir, evidence.DefaultLimits())
	sp, err := s.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	d, err := s.Capture(context.Background(), sp, evidence.CaptureRequest{WorkspaceID: ws, Tool: "Bash", ContentType: "application/json",
		Actor: actor, AuthEnforced: actor != "", Retain: true})
	if err != nil || d.Handle == "" {
		t.Fatalf("capture: %v %+v", err, d)
	}
	return d.Handle
}

func TestReviewAnchorFromAFindingsFile(t *testing.T) {
	dir, ws := anchorWorkspace(t)
	file := filepath.Join(t.TempDir(), "ocr.json")
	if err := os.WriteFile(file, []byte(anchorFindings), 0o600); err != nil {
		t.Fatal(err)
	}
	e, out, errOut := testEnv(nil, "")
	if code := runOps(e, "review", "anchor", ws, "--base", "main", "--findings", file, "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	res := decodeOut(t, out)
	f := res["findings"].([]any)[0].(map[string]any)
	a := f["anchor"].(map[string]any)
	if a["anchor_status"] != "exact_new" || a["start_line"] != float64(3) || f["support"] != "supported" {
		t.Fatalf("finding = %v", f)
	}
	src := res["source"].(map[string]any)
	if src["kind"] != "findings_file" || src["ref"] != file || res["change"].(map[string]any)["diff_sha256"] == "" {
		t.Fatalf("result = %v", res)
	}
	if notes := res["normalized"].([]any); len(notes) != 1 || notes[0] != "thinking dropped from 1 findings" {
		t.Fatalf("normalized = %v", notes)
	}
}

func TestReviewAnchorFromEvidence(t *testing.T) {
	t.Run("open mode reads at workspace scope", func(t *testing.T) {
		dir, ws := anchorWorkspace(t)
		handle := capture(t, dir, ws, "", anchorFindings)
		e, out, errOut := testEnv(nil, "")
		if code := runOps(e, "review", "anchor", ws, "--base", "main", "--evidence", handle, "--data-dir", dir); code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		if src := decodeOut(t, out)["source"].(map[string]any); src["kind"] != "evidence_handle" || src["ref"] != handle {
			t.Fatalf("source = %v", src)
		}
	})
	t.Run("auth reads as the capturing principal only", func(t *testing.T) {
		dir, ws := anchorWorkspace(t)
		owner, err := workspaceops.MintToken(dir, "agent-a", "agent")
		if err != nil {
			t.Fatal(err)
		}
		other, err := workspaceops.MintToken(dir, "agent-b", "agent")
		if err != nil {
			t.Fatal(err)
		}
		elsewhere, err := workspaceops.MintScopedToken(dir, "agent-c", "agent", 0, []string{"another-ws"})
		if err != nil {
			t.Fatal(err)
		}
		presence := mintIdentity(t, dir, "erin", workspaceops.TokenIdentity{Kind: workspaceops.PrincipalHuman, PresenceOnly: true})
		handle := capture(t, dir, ws, "agent-a", anchorFindings)
		args := []string{"review", "anchor", ws, "--base", "main", "--evidence", handle, "--data-dir", dir}
		for name, tok := range map[string]string{"no token": "", "an unknown token": "nope", "another principal": other,
			"another workspace": elsewhere, "a presence-only token": presence} {
			e, _, errOut := testEnv(map[string]string{"XMUSTARD_API_TOKEN": tok}, "")
			if code := runOps(e, args...); code != exitError {
				t.Errorf("%s: exit %d, want %d (%s)", name, code, exitError, errOut)
			}
		}
		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte(owner+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		e, out, errOut := testEnv(map[string]string{"XMUSTARD_API_TOKEN": other}, "")
		if code := runOps(e, append(args, "--token-file", tokenFile)...); code != 0 {
			t.Fatalf("the capturing principal: exit %d: %s", code, errOut)
		}
		if c := decodeOut(t, out)["counts"].(map[string]any); c["by_support"].(map[string]any)["supported"] != float64(1) {
			t.Fatalf("counts = %v", c)
		}
	})
	t.Run("an original over the bound is refused", func(t *testing.T) {
		dir, ws := anchorWorkspace(t)
		handle := capture(t, dir, ws, "", anchorFindings+strings.Repeat(" ", 4<<20))
		e, _, errOut := testEnv(nil, "")
		if code := runOps(e, "review", "anchor", ws, "--base", "main", "--evidence", handle, "--data-dir", dir); code != exitError ||
			!strings.Contains(errOut.String(), "over") {
			t.Fatalf("exit %d: %s", code, errOut)
		}
	})
}

func TestReviewAnchorUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no source":    {"review", "anchor", "ws", "--base", "main"},
		"both sources": {"review", "anchor", "ws", "--base", "main", "--findings", "f", "--evidence", "h"},
		"no base":      {"review", "anchor", "ws", "--findings", "f"},
		"a stray arg":  {"review", "anchor", "ws", "--base", "main", "--findings", "f", "extra"},
		"no workspace": {"review", "anchor"},
		"unknown verb": {"review", "annotate", "ws"},
	} {
		e, _, errOut := testEnv(nil, "")
		if code := runOps(e, args...); code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
		if name == "unknown verb" && !strings.Contains(errOut.String(), "<anchor|approve|gate|revoke>") {
			t.Errorf("usage lists %q", errOut)
		}
	}
}
