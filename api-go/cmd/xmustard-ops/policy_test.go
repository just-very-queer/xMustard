package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

func runGov(e opsEnv, args ...string) int { return governanceCommands[args[0]](e, args[1:]) }

// policy set needs an admin who is a human approver; policy show reads without a token;
// memory tier needs a human approver and a served entry; memory tokens reports the
// projection.
func TestPolicyAndTierCommands(t *testing.T) {
	dir, ws := t.TempDir(), "ws"
	approver := mintApprover(t, dir, "alice", workspaceops.PrincipalHuman)
	admin, err := workspaceops.MintIdentityToken(dir, "root", workspaceops.RoleAdmin, 0, nil, workspaceops.TokenIdentity{Kind: workspaceops.PrincipalHuman})
	if err != nil {
		t.Fatal(err)
	}
	e, out, errOut := testEnv(nil, "")
	if code := runGov(e, "policy", "show", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("show: %d %s", code, errOut)
	}
	if shown := decodeOut(t, out); shown["policy"].(map[string]any)["max_entry_chars"] != float64(20000) {
		t.Fatalf("default policy: %v", shown)
	}
	file := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(file, []byte(`{"max_entry_chars": 500, "core_budget_chars": 1000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	notAdmin, out, errOut := testEnv(map[string]string{"XMUSTARD_APPROVER_TOKEN": approver}, "")
	if code := runGov(notAdmin, "policy", "set", ws, "--file", file, "--data-dir", dir); code != exitError || out.Len() != 0 ||
		!strings.Contains(errOut.String(), "admin") {
		t.Fatalf("a non-admin set the policy: %d %s %s", code, out, errOut)
	}
	root, out, errOut := testEnv(nil, admin)
	if code := runGov(root, "policy", "set", ws, "--file", file, "--data-dir", dir); code != 0 {
		t.Fatalf("admin set: %d %s", code, errOut)
	}
	if res := decodeOut(t, out); res["changed"] != true || res["approval"] != "ops/advisory" || res["approver"] != "root" {
		t.Fatalf("admin set: %v", res)
	}
	if err := os.WriteFile(file, []byte(`{"max_entry_chars": 500, "unknown": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runGov(root, "policy", "set", ws, "--file", file, "--data-dir", dir); code != exitError {
		t.Fatalf("an unknown policy field was accepted: %d", code)
	}

	entry, err := workspaceops.Remember(dir, ws, workspaceops.RememberRequest{ProposeContextRequest: workspaceops.ProposeContextRequest{
		Title: "release", Content: "tags come from main"}}, workspaceops.ContextActor{ID: "author"})
	if err != nil {
		t.Fatal(err)
	}
	human, out, errOut := testEnv(nil, approver)
	if code := runGov(human, "memory", "tier", ws, entry.ID, "core", "--reason", "standing", "--data-dir", dir); code != exitError ||
		!strings.Contains(errOut.String(), "not served") {
		t.Fatalf("a pending entry entered the core tier: %d %s %s", code, out, errOut)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := workspaceops.VerifyContext(dir, ws, entry.ID, peer, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if code := runGov(human, "memory", "tier", ws, entry.ID, "core", "--data-dir", dir); code != exitUsage {
		t.Fatalf("tier without a reason: %d", code)
	}
	errOut.Reset()
	if code := runGov(human, "memory", "tier", ws, entry.ID, "core", "--reason", "standing", "--data-dir", dir); code != 0 {
		t.Fatalf("tier: %d %s", code, errOut)
	}
	if res := decodeOut(t, out); res["entry"].(map[string]any)["tier"] != "core" || res["approval"] != "ops/advisory" {
		t.Fatalf("tier: %v", res)
	}
	// agents approved it, no human did: the report says it is withheld
	if code := runGov(human, "memory", "tokens", ws, "--data-dir", dir); code != 0 {
		t.Fatalf("tokens: %d %s", code, errOut)
	}
	rep := decodeOut(t, out)
	if w := rep["withheld"].([]any); len(w) != 1 || w[0].(map[string]any)["reason"] != "needs_human_approved" || rep["core_budget"] != float64(1000) {
		t.Fatalf("token report: %v", rep)
	}
	if code := runGov(human, "memory", "bogus", ws); code != exitUsage {
		t.Fatalf("unknown subcommand: %d", code)
	}
	if code := runGov(human, "guidance", "write", ws, "--file", "README.md", "--data-dir", dir); code != exitUsage {
		t.Fatalf("guidance write outside the closed file set: %d", code)
	}
}
