package workspaceops

import (
	"path/filepath"
	"testing"
)

func writeTestSettings(t *testing.T, dir string, s appSettings) {
	t.Helper()
	if err := writeJSON(filepath.Join(dir, "settings.json"), s); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestContextMultiAgentPromotion(t *testing.T) {
	dir := t.TempDir()
	ws := "ws1"
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "API base path", Content: "the api base is /api", Source: "agent-a", Permission: "readonly",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if entry.Promoted || entry.Status != "pending" {
		t.Fatalf("multi-agent entry should start pending, got status=%s promoted=%v", entry.Status, entry.Promoted)
	}

	// first approval — still below threshold of 2
	e, _ := VerifyContext(dir, ws, entry.ID, "agent-b", true, "looks right")
	if e.Promoted {
		t.Fatalf("one approval should not promote (threshold 2)")
	}
	// same agent voting again must NOT count twice
	e, _ = VerifyContext(dir, ws, entry.ID, "agent-b", true, "still right")
	if e.Promoted {
		t.Fatalf("duplicate agent vote must not satisfy multi-agent gate")
	}
	// a second distinct agent approves — now promoted
	e, _ = VerifyContext(dir, ws, entry.ID, "agent-c", true, "confirmed")
	if !e.Promoted || e.Status != "verified" {
		t.Fatalf("two distinct approvals should promote, got status=%s promoted=%v", e.Status, e.Promoted)
	}

	active, _ := GetActiveContext(dir, ws)
	if active["active_count"].(int) != 1 {
		t.Fatalf("expected 1 active entry, got %v", active["active_count"])
	}
}

func TestContextSingleAgentMode(t *testing.T) {
	dir := t.TempDir()
	ws := "ws2"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, err := ProposeContext(dir, ws, ProposeContextRequest{
		Content: "single agent fact", Source: "solo",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !entry.Promoted {
		t.Fatalf("single-agent mode should promote immediately, got %v", entry.Status)
	}

	// per-proposal override: force multi-agent even though global is off
	on := true
	entry2, _ := ProposeContext(dir, ws, ProposeContextRequest{
		Content: "needs review", Source: "solo", RequireVerification: &on,
	})
	if entry2.Promoted {
		t.Fatalf("per-proposal RequireVerification=true should keep it pending")
	}
}

func TestReadonlyEntryRejectsEdit(t *testing.T) {
	dir := t.TempDir()
	ws := "ws3"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})

	entry, _ := ProposeContext(dir, ws, ProposeContextRequest{
		Content: "frozen fact", Source: "solo", Permission: "readonly",
	})
	if !entry.Promoted {
		t.Fatalf("expected promoted in single-agent mode")
	}
	if _, err := UpdateContextContent(dir, ws, entry.ID, "tampered"); err == nil {
		t.Fatalf("readonly verified entry must reject edits")
	}

	// a readwrite entry CAN be edited (and edit resets verification)
	rw, _ := ProposeContext(dir, ws, ProposeContextRequest{
		Content: "mutable", Source: "solo", Permission: "readwrite",
	})
	updated, err := UpdateContextContent(dir, ws, rw.ID, "new content")
	if err != nil {
		t.Fatalf("readwrite edit should succeed: %v", err)
	}
	if updated.Content != "new content" {
		t.Fatalf("content not updated")
	}
}

func TestPerRequestOverrideCannotLoosen(t *testing.T) {
	dir := t.TempDir()
	ws := "wsClamp"
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})
	// an untrusted proposer tries to self-promote by forcing single-agent mode
	off := false
	entry, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "sneaky", Source: "attacker", RequireVerification: &off})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Promoted || entry.Status != "pending" {
		t.Fatalf("require_verification:false must NOT loosen a multi-agent-required policy; got status=%s promoted=%v", entry.Status, entry.Promoted)
	}
	// but it CAN tighten: when global is single-agent, forcing verification keeps it pending
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})
	on := true
	e2, _ := ProposeContext(dir, "wsClamp2", ProposeContextRequest{Content: "careful", Source: "a", RequireVerification: &on})
	if e2.Promoted {
		t.Fatalf("require_verification:true should tighten even when global is single-agent")
	}
}

func TestContextRejectsPathTraversalID(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../../etc", "..", "a/b", "x\x00y", ""} {
		if _, err := ProposeContext(dir, bad, ProposeContextRequest{Content: "x", Source: "s"}); err == nil {
			t.Fatalf("expected rejection for workspace id %q", bad)
		}
		if _, err := ListContextEntries(dir, bad, ""); err == nil {
			t.Fatalf("expected list rejection for workspace id %q", bad)
		}
	}
	// a normal id is accepted
	if _, err := ListContextEntries(dir, "co-titan_123", ""); err != nil {
		t.Fatalf("valid id should be accepted: %v", err)
	}
}

func TestProposerCannotSelfApproveMultiAgent(t *testing.T) {
	dir := t.TempDir()
	ws := "wsSelf"
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})
	e, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "x", Source: "author"})
	// author verifies its OWN entry + one other agent — author must not count
	VerifyContext(dir, ws, e.ID, "author", true, "")
	got, _ := VerifyContext(dir, ws, e.ID, "other", true, "")
	if got.Promoted {
		t.Fatalf("author self-approval must not count toward a 2-distinct gate; got promoted with author+1")
	}
	// a second NON-author approval promotes
	got2, _ := VerifyContext(dir, ws, e.ID, "other2", true, "")
	if !got2.Promoted {
		t.Fatalf("two non-author approvals should promote")
	}
}

func TestRejectionBlocksPromotion(t *testing.T) {
	dir := t.TempDir()
	ws := "ws4"
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})
	entry, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "doubtful", Source: "a"})
	VerifyContext(dir, ws, entry.ID, "b", false, "wrong")
	e, _ := VerifyContext(dir, ws, entry.ID, "c", false, "also wrong")
	if e.Promoted || e.Status != "rejected" {
		t.Fatalf("two rejections should reject, got status=%s", e.Status)
	}
}
