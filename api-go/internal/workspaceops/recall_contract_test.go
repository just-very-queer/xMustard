package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// seedPromoted writes n promoted single-agent entries (e0000 oldest … newest last).
func seedPromoted(t *testing.T, dir, ws string, n int) []ContextEntry {
	t.Helper()
	return seedPromotedWith(t, dir, ws, n, nil)
}

// seedPromotedWith is seedPromoted with a hook that adjusts each entry before it is
// stored.
func seedPromotedWith(t *testing.T, dir, ws string, n int, adjust func(*ContextEntry)) []ContextEntry {
	t.Helper()
	entries := make([]ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("memory body number %d about topic%d", i, i)
		entries = append(entries, ContextEntry{
			ID:                    fmt.Sprintf("e%04d", i),
			WorkspaceID:           ws,
			Title:                 fmt.Sprintf("entry %d", i),
			Content:               content,
			Source:                "author",
			Permission:            "readwrite",
			Status:                "verified",
			Promoted:              true,
			RequiredVerifications: 1,
			Verifications:         []ContextVerification{{Agent: "peer", Approve: true}},
			SearchTokens:          memoryTokenList(fmt.Sprintf("entry %d ", i) + content),
			CreatedAt:             fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
			UpdatedAt:             fmt.Sprintf("2026-06-01T00:00:00.%09dZ", i),
		})
		if adjust != nil {
			adjust(&entries[i])
		}
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

// Audit (Go §additional): a content update that lands between recall's metadata pass
// and its content load must not attach the revised, unverified text to the old
// verified metadata. The interleaving is forced deterministically through the
// recallBeforeContentLoad seam.
func TestRecallNeverAttachesRevisedContentToOldApproval(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallRace"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})
	entries := seedPromoted(t, dir, ws, 3)
	target := entries[2] // newest → first in recency order

	fired := 0
	recallBeforeContentLoad = func() {
		if fired > 0 {
			return
		}
		fired++
		// Multi-agent threshold so the revision is NOT re-promoted by the update.
		enable := true
		writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &enable, ContextVerificationThreshold: 2})
		if _, err := UpdateContextContent(dir, ws, target.ID, "REVISED_UNVERIFIED_TEXT", ContextActor{Admin: true}); err != nil {
			t.Errorf("update: %v", err)
		}
	}
	defer func() { recallBeforeContentLoad = nil }()

	res, err := RecallContext(dir, ws, "", nil, 8)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if fired != 1 {
		t.Fatalf("interleaving hook did not fire")
	}
	// the retry must see the revision (entry no longer promoted) and return the two
	// untouched memories — not withhold everything and not skip the retry.
	if res["consistency_attempts"] != 2 || res["returned"] != 2 || res["consistency_withheld"] != nil {
		t.Fatalf("want attempts=2 returned=2 withheld=nil, got attempts=%v returned=%v withheld=%v",
			res["consistency_attempts"], res["returned"], res["consistency_withheld"])
	}
	for _, e := range res["entries"].([]ContextEntry) {
		if strings.Contains(e.Content, "REVISED_UNVERIFIED_TEXT") {
			t.Fatalf("revised unverified content returned with approval state status=%s promoted=%v verifications=%v",
				e.Status, e.Promoted, e.Verifications)
		}
		if e.ID == target.ID && e.Content != target.Content {
			t.Fatalf("entry %s returned content %q that does not match its promoted version", e.ID, e.Content)
		}
	}
}

// Audit Go #1 (workspaceops half): a no-argument recall on a dirty tree must still
// return recency-ranked memory. Implicit working-change paths boost relevance; they
// must not gate every unrelated memory out of the result.
func TestPlainRecallWithDirtyTreeStillReturnsRecencyTopN(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallDirty"
	seedPromoted(t, dir, ws, 12)
	recallChangedFiles = func(context.Context, string, string) []string { return []string{"unrelated/changed.go"} }
	defer func() { recallChangedFiles = currentChangedFiles }()

	res, err := RecallContext(dir, ws, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != defaultRecallLimit {
		t.Fatalf("plain recall on a dirty tree returned %d entries, want %d", len(got), defaultRecallLimit)
	}
	if got[0].ID != "e0011" {
		t.Fatalf("expected newest entry first, got %s", got[0].ID)
	}
}

// Root hardening: acceptance compares the FULL SHA-256 digest of the revision each
// entry was ranked with. When the store keeps changing the served revision between
// ranking and content load, the mismatched entry is withheld after the bounded
// retries, never returned under the approval of another version.
func TestRecallWithholdsContentWhenStoreKeepsMoving(t *testing.T) {
	dir := t.TempDir()
	ws := "wsFullDigest"
	disable := false
	writeTestSettings(t, dir, appSettings{RequireMultiAgentVerification: &disable})
	entry, err := ProposeContext(dir, ws, ProposeContextRequest{Content: "approved text", Source: "solo", Permission: "readwrite"})
	if err != nil {
		t.Fatal(err)
	}
	edits := 0
	recallBeforeContentLoad = func() {
		edits++
		// edit and re-approve, so the entry is promoted again at every ranking pass but
		// serves a different revision by the time its content is read
		if _, err := UpdateContextContent(dir, ws, entry.ID, fmt.Sprintf("moving text %d", edits), ContextActor{Admin: true}); err != nil {
			t.Errorf("update: %v", err)
		}
		if _, err := VerifyContext(dir, ws, entry.ID, "solo", true, ""); err != nil {
			t.Errorf("verify: %v", err)
		}
	}
	defer func() { recallBeforeContentLoad = nil }()

	res, err := RecallContext(dir, ws, "", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := res["entries"].([]ContextEntry); len(got) != 0 {
		t.Fatalf("content of a revision other than the ranked one was returned: %+v", got)
	}
	if res["consistency_withheld"] != 1 || res["consistency_attempts"] != recallConsistencyAttempts {
		t.Fatalf("expected the mismatched entry to be withheld after %d attempts, got %v", recallConsistencyAttempts, res)
	}
}

func contentDigestForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// The binding digest is the full SHA-256 of the served content.
func TestContentDigestIsFullSHA256(t *testing.T) {
	if contentDigest("x") != contentDigestForTest("x") {
		t.Fatal("content digest must be the full SHA-256")
	}
}

// Fable review #1: with no query/paths, the current working changes must actually
// boost matching memories (they were computed and then discarded).
func TestPlainRecallBoostsMemoryAboutChangedFiles(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallFocus"
	entries := seedPromotedWith(t, dir, ws, 12, func(e *ContextEntry) {
		if e.ID == "e0000" {
			e.Paths = []string{"changed.go"} // the OLDEST memory is about the changed file
		}
	})
	recallChangedFiles = func(context.Context, string, string) []string { return []string{"changed.go"} }
	defer func() { recallChangedFiles = currentChangedFiles }()
	res, err := RecallContext(dir, ws, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != defaultRecallLimit || got[0].ID != entries[0].ID {
		ids := []string{}
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		t.Fatalf("memory about the changed file should rank first in the top-%d, got %v", defaultRecallLimit, ids)
	}
}
