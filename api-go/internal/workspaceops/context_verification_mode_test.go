package workspaceops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quorumSettings(t *testing.T, dir string) {
	t.Helper()
	require := true
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &require, ContextVerificationThreshold: 2})
}

func recallModes(t *testing.T, res map[string]any) map[string]int {
	t.Helper()
	modes, ok := res["verification_modes"].(map[string]int)
	if !ok {
		t.Fatalf("recall result has no verification_modes count: %v", res)
	}
	return modes
}

// Open mode: with no credentials every caller is one identity, so the default quorum
// (require=true, threshold=2) can never form. A proposal is promoted at once and
// labelled self-asserted.
func TestOpenModeProposalPromotesSelfAsserted(t *testing.T) {
	dir := t.TempDir()
	ws := "wsOpen"
	quorumSettings(t, dir)

	e, err := ProposeContext(dir, ws, ProposeContextRequest{
		Title: "api base", Content: "the api base is /api", Source: "claimed-name", OpenMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !e.Promoted || e.Status != "verified" {
		t.Fatalf("open-mode proposal must be promoted, got status=%s promoted=%v", e.Status, e.Promoted)
	}
	if e.VerificationMode != VerificationSelfAssertedOpen {
		t.Fatalf("verification_mode = %q, want %q", e.VerificationMode, VerificationSelfAssertedOpen)
	}
	if e.Source != OpenModeIdentity {
		t.Fatalf("open-mode author must be %q, not a caller-asserted name; got %q", OpenModeIdentity, e.Source)
	}

	res, err := RecallContext(dir, ws, "api base", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].VerificationMode != VerificationSelfAssertedOpen {
		t.Fatalf("recall must return the self-asserted entry with its mode, got %+v", got)
	}
	if m := recallModes(t, res); m[VerificationSelfAssertedOpen] != 1 || m[VerificationPeer] != 0 {
		t.Fatalf("recall mode counts = %v", m)
	}
	active, err := GetActiveContext(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	if m := active["verification_modes"].(map[string]int); m[VerificationSelfAssertedOpen] != 1 {
		t.Fatalf("active context mode counts = %v", m)
	}
	// run prompts inject promoted memory; a self-asserted fact must say so there too
	if p := applyActiveContextToPrompt(dir, ws, "BASE"); !strings.Contains(p, "api base (self-asserted in open mode, not peer-verified): the api base is /api") {
		t.Fatalf("prompt must tag the self-asserted entry, got %q", p)
	}
}

// An explicit require_verification:true still tightens in open mode: the entry waits
// for distinct authenticated verifiers instead of self-promoting.
func TestOpenModeTightenedProposalStaysPending(t *testing.T) {
	dir := t.TempDir()
	quorumSettings(t, dir)
	on := true
	e, err := ProposeContext(dir, "wsOpenTight", ProposeContextRequest{Content: "careful", OpenMode: true, RequireVerification: &on})
	if err != nil {
		t.Fatal(err)
	}
	if e.Promoted || e.Status != "pending" || e.VerificationMode != "" {
		t.Fatalf("tightened open-mode proposal must stay pending with no mode, got status=%s promoted=%v mode=%q", e.Status, e.Promoted, e.VerificationMode)
	}
}

// OpenMode is set by the HTTP layer only; a client body cannot claim it.
func TestProposeRequestOpenModeIsNotClientDecodable(t *testing.T) {
	var req ProposeContextRequest
	if err := json.Unmarshal([]byte(`{"content":"x","OpenMode":true,"open_mode":true}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.OpenMode {
		t.Fatalf("OpenMode must not be decodable from a request body")
	}
}

// Authenticated behavior is unchanged: distinct principals, author excluded. The
// promoted entry is labelled peer_verified; the single-agent setting is labelled too.
func TestAuthenticatedModesAreLabelled(t *testing.T) {
	dir := t.TempDir()
	ws := "wsAuthModes"
	quorumSettings(t, dir)
	e, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "quorum fact", Source: "alice"})
	if e.Promoted || e.VerificationMode != "" {
		t.Fatalf("authenticated proposal must start pending with no mode, got promoted=%v mode=%q", e.Promoted, e.VerificationMode)
	}
	VerifyContext(dir, ws, e.ID, "alice", true, "")
	got, _ := VerifyContext(dir, ws, e.ID, "bob", true, "")
	if got.Promoted {
		t.Fatalf("author + one peer must not promote at threshold 2")
	}
	got, _ = VerifyContext(dir, ws, e.ID, "carol", true, "")
	if !got.Promoted || got.VerificationMode != VerificationPeer {
		t.Fatalf("two distinct peers must promote as peer_verified, got promoted=%v mode=%q", got.Promoted, got.VerificationMode)
	}
	// a peer withdrawing demotes it and clears the trust label
	got, _ = VerifyContext(dir, ws, e.ID, "carol", false, "")
	if got.Promoted || got.VerificationMode != "" {
		t.Fatalf("demoted entry must lose its mode, got promoted=%v mode=%q", got.Promoted, got.VerificationMode)
	}

	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	solo, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "solo fact", Source: "solo"})
	if !solo.Promoted || solo.VerificationMode != VerificationSingleAgent {
		t.Fatalf("single-agent setting must promote as single_agent, got promoted=%v mode=%q", solo.Promoted, solo.VerificationMode)
	}
}

// A self-asserted open-mode fact that later collects a full quorum of authenticated
// peers is upgraded to peer_verified; one peer is not enough.
func TestSelfAssertedEntryUpgradesOnPeerQuorum(t *testing.T) {
	dir := t.TempDir()
	ws := "wsUpgrade"
	quorumSettings(t, dir)
	e, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "open fact", OpenMode: true})
	got, _ := VerifyContext(dir, ws, e.ID, "bob", true, "")
	if got.VerificationMode != VerificationSelfAssertedOpen {
		t.Fatalf("one peer must not upgrade a self-asserted fact, got %q", got.VerificationMode)
	}
	got, _ = VerifyContext(dir, ws, e.ID, "carol", true, "")
	if !got.Promoted || got.VerificationMode != VerificationPeer {
		t.Fatalf("a full peer quorum must upgrade to peer_verified, got promoted=%v mode=%q", got.Promoted, got.VerificationMode)
	}
}

// Entries persisted before verification_mode existed are labelled on read.
func TestLegacyEntriesInferVerificationMode(t *testing.T) {
	dir := t.TempDir()
	ws := "wsLegacyMode"
	quorumSettings(t, dir)
	approve := func(agents ...string) []map[string]any {
		out := []map[string]any{}
		for _, a := range agents {
			out = append(out, map[string]any{"agent": a, "approve": true, "at": "2026-01-01T00:00:00Z"})
		}
		return out
	}
	legacy := []map[string]any{
		{"id": "peer", "source": "alice", "required_verifications": 2, "verifications": approve("bob", "carol")},
		{"id": "solo", "source": "solo", "required_verifications": 1, "verifications": approve("solo")},
		{"id": "open", "source": OpenModeIdentity, "required_verifications": 1, "verifications": approve(OpenModeIdentity)},
		// promoted before author exclusion existed: the author's own vote carried it
		{"id": "authorvote", "source": "dave", "required_verifications": 2, "verifications": approve("dave", "erin")},
	}
	for i, e := range legacy {
		e["workspace_id"], e["title"], e["content"] = ws, "legacy fact", "legacy fact body"
		e["permission"], e["status"], e["promoted"] = "readonly", "verified", true
		e["search_tokens"] = []string{"legacy", "fact", "body"}
		e["created_at"] = "2026-01-01T00:00:0" + string(rune('0'+i)) + "Z"
		e["updated_at"] = e["created_at"]
	}
	legacy = append(legacy, map[string]any{
		"id": "pending", "workspace_id": ws, "title": "legacy fact", "content": "pending body", "source": "alice",
		"permission": "readonly", "status": "pending", "promoted": false, "required_verifications": 2,
		"verifications": approve("bob"), "created_at": "2026-01-01T00:00:09Z", "updated_at": "2026-01-01T00:00:09Z",
	})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(contextEntriesPath(dir, ws), legacy); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"peer":       VerificationPeer,
		"solo":       VerificationSingleAgent,
		"open":       VerificationSelfAssertedOpen,
		"authorvote": VerificationSingleAgent,
		"pending":    "",
	}

	all, err := ListContextEntries(dir, ws, "all")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if e.VerificationMode != want[e.ID] {
			t.Fatalf("list: legacy %s mode = %q, want %q", e.ID, e.VerificationMode, want[e.ID])
		}
	}
	res, err := RecallContext(dir, ws, "legacy", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 4 {
		t.Fatalf("recall returned %d entries, want the 4 promoted", len(got))
	}
	for _, e := range got {
		if e.VerificationMode != want[e.ID] {
			t.Fatalf("recall: legacy %s mode = %q, want %q", e.ID, e.VerificationMode, want[e.ID])
		}
	}
	if m := recallModes(t, res); m[VerificationPeer] != 1 || m[VerificationSingleAgent] != 2 || m[VerificationSelfAssertedOpen] != 1 {
		t.Fatalf("recall mode counts = %v", m)
	}
	// the meta-cache path (a cache written without the field) labels the same way
	entries, _ := loadContextEntries(dir, ws)
	writeContextMetaCache(dir, ws, entries)
	if _, ok := loadPromotedMetaCached(dir, ws); !ok {
		t.Fatal("expected recall to use the meta cache")
	}
	res, err = RecallContext(dir, ws, "legacy", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if m := recallModes(t, res); m[VerificationPeer] != 1 || m[VerificationSingleAgent] != 2 || m[VerificationSelfAssertedOpen] != 1 {
		t.Fatalf("cached recall mode counts = %v", m)
	}
}

// Content edits are bound to the author or an admin, and still reset verification.
func TestUpdateContextContentAuthorBinding(t *testing.T) {
	dir := t.TempDir()
	ws := "wsEditBind"
	off := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &off})
	e, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "v1", Source: "alice", Permission: "readwrite"})

	for _, ed := range []ContextEditor{{ID: "mallory"}, {ID: ""}, {ID: "ALICE"}} {
		if _, err := UpdateContextContent(dir, ws, e.ID, "hijacked", ed); !errors.Is(err, ErrNotEntryAuthor) {
			t.Fatalf("editor %+v: want ErrNotEntryAuthor, got %v", ed, err)
		}
	}
	got, err := UpdateContextContent(dir, ws, e.ID, "v2", ContextEditor{ID: "alice"})
	if err != nil {
		t.Fatalf("author edit: %v", err)
	}
	if got.Content != "v2" || got.Promoted || len(got.Verifications) != 0 || got.VerificationMode != "" {
		t.Fatalf("author edit must reset verification, got promoted=%v verifications=%d mode=%q", got.Promoted, len(got.Verifications), got.VerificationMode)
	}
	if _, err := UpdateContextContent(dir, ws, e.ID, "v3", ContextEditor{ID: "root", Admin: true}); err != nil {
		t.Fatalf("admin edit: %v", err)
	}
}

// ground reports the promoted-memory count per verification mode.
func TestGroundingReportsMemoryVerificationModes(t *testing.T) {
	dir := t.TempDir()
	ws := "wsGroundModes"
	writeSnapshotWithRoot(t, dir, ws, t.TempDir())
	core := filepath.Join(t.TempDir(), "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	quorumSettings(t, dir)
	if _, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "open fact", OpenMode: true}); err != nil {
		t.Fatal(err)
	}
	peer, _ := ProposeContext(dir, ws, ProposeContextRequest{Content: "peer fact", Source: "alice"})
	VerifyContext(dir, ws, peer.ID, "bob", true, "")
	VerifyContext(dir, ws, peer.ID, "carol", true, "")

	g, err := BuildSessionGrounding(dir, ws)
	if err != nil {
		t.Fatal(err)
	}
	m := g.MemoryVerificationModes
	if m[VerificationSelfAssertedOpen] != 1 || m[VerificationPeer] != 1 || m[VerificationSingleAgent] != 0 {
		t.Fatalf("ground memory_verification_modes = %v", m)
	}
}
