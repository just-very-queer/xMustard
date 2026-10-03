package main

import (
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/workspaceops"
)

// `memory jobs` is the operator surface for the consolidation queue, and it is the only
// production caller of the pass: without it the loop WS-32a delivers cannot run for anyone.
// This test drives the real command against a real store.
func TestMemoryJobsPlanRunAndList(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	// Two memories on one declared path, so the pass has a dedupe candidate to derive.
	for i, title := range []string{"left", "right"} {
		_, err := workspaceops.Remember(dir, ws, workspaceops.RememberRequest{
			ProposeContextRequest: workspaceops.ProposeContextRequest{Title: title, Content: "a shared thing",
				Permission: "readwrite", Paths: []string{"src/shared.go"}}}, workspaceops.ContextActor{ID: "op" + string(rune('a'+i))})
		if err != nil {
			t.Fatalf("propose %s: %v", title, err)
		}
	}

	e, out, errOut := testEnv(nil, "")
	if code := runGov(e, "memory", "jobs-plan", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("plan: %d %s", code, errOut)
	}
	plan := decodeOut(t, out)
	prepared, _ := plan["prepared"].([]any)
	if len(prepared) == 0 {
		t.Fatalf("plan derived no candidate from two memories on one path: %v", plan)
	}

	// run queues them and reports the pass's own figures.
	if code := runGov(e, "memory", "jobs-run", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("run: %d %s", code, errOut)
	}
	ran := decodeOut(t, out)
	jobs, _ := ran["jobs"].([]any)
	if len(jobs) == 0 {
		t.Fatalf("run queued nothing: %v", ran)
	}
	if ran["seen"] == nil {
		t.Fatalf("run reported no reflected count: %v", ran)
	}

	// list reads the queue back, and the job is claimable by the agent that will do it.
	if code := runGov(e, "memory", "jobs-list", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("list: %d %s", code, errOut)
	}
	listed := decodeOut(t, out)
	rows, _ := listed["jobs"].([]any)
	if len(rows) != len(jobs) {
		t.Fatalf("list returned %d jobs, run queued %d", len(rows), len(jobs))
	}
	first, _ := rows[0].(map[string]any)
	if first["state"] != govstore.JobQueued {
		t.Fatalf("the queued job is %v, want %s", first["state"], govstore.JobQueued)
	}
}

// The commands are a closed set: an unknown one is a usage error that names what exists,
// not a silent success.
func TestMemoryJobsRejectsAnUnknownCommand(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	e, _, errOut := testEnv(nil, "")
	if code := runGov(e, "memory", "jobs-delete", ws, "--data-dir", dir); code == 0 {
		t.Fatal("an unknown memory command exited 0")
	}
	if !strings.Contains(errOut.String(), "jobs-run") {
		t.Fatalf("the usage does not name the commands: %q", errOut.String())
	}
}

// A pass over a workspace with nothing in it succeeds and says so: an empty queue is not an
// error, and the figures name what the pass reflected over so a caller can tell the two
// apart. A refusal is tested where it belongs, on a workspace id the store refuses.
func TestMemoryJobsRunOverAnEmptyWorkspaceIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	e, out, errOut := testEnv(nil, "")
	if code := runGov(e, "memory", "jobs-run", "empty-ws", "--data-dir", dir); code != 0 {
		t.Fatalf("jobs-run over an empty workspace: %d %s", code, errOut)
	}
	ran := decodeOut(t, out)
	if ran["seen"] != float64(0) {
		t.Fatalf("an empty workspace reflected over %v entries: %v", ran["seen"], ran)
	}
	if jobs, _ := ran["jobs"].([]any); len(jobs) != 0 {
		t.Fatalf("an empty workspace queued %d jobs: %v", len(jobs), ran)
	}
}

// A workspace id the store refuses is a refusal, not an empty result.
func TestMemoryJobsRunRefusesAMalformedWorkspace(t *testing.T) {
	dir := t.TempDir()
	e, _, errOut := testEnv(nil, "")
	if code := runGov(e, "memory", "jobs-run", "../escape", "--data-dir", dir); code == 0 {
		t.Fatal("jobs-run accepted a workspace id with a path separator")
	}
	if !strings.Contains(errOut.String(), "invalid workspace id") {
		t.Fatalf("the error does not name the refusal: %q", errOut.String())
	}
}
