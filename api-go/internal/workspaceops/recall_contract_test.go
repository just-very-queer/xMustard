package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// seedPromoted writes n promoted single-agent entries (e0000 oldest … newest last).
func seedPromoted(t *testing.T, dir, ws string, n int) []ContextEntry {
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
	}
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		writeContextContentFile(dir, ws, e.ID, e.Content)
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
		if _, err := UpdateContextContent(dir, ws, target.ID, "REVISED_UNVERIFIED_TEXT"); err != nil {
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

// writeRawMetaCache writes a recall meta cache from raw JSON maps (so the test can
// express legacy and adversarial shapes) and makes it strictly fresher than the source.
func writeRawMetaCache(t *testing.T, dir, ws string, metas []map[string]any) {
	t.Helper()
	if err := writeJSON(contextMetaCachePath(dir, ws), metas); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(contextMetaCachePath(dir, ws), future, future); err != nil {
		t.Fatal(err)
	}
}

func rawMeta(e ContextEntry, shortHash string) map[string]any {
	return map[string]any{
		"id": e.ID, "workspace_id": e.WorkspaceID, "title": e.Title, "source": e.Source,
		"permission": e.Permission, "status": e.Status, "promoted": e.Promoted,
		"verifications": e.Verifications, "required_verifications": e.RequiredVerifications,
		"created_at": e.CreatedAt, "updated_at": e.UpdatedAt, "search_tokens": e.SearchTokens,
		"content_hash": shortHash,
	}
}

// Root hardening: a legacy meta cache (64-bit filename hash, no full digest) must not
// be trusted to bind content to an approval. Recall falls back to the source and the
// cache is migrated to full digests.
func TestRecallLegacyShortHashCacheFailsClosedAndMigrates(t *testing.T) {
	dir := t.TempDir()
	ws := "wsLegacyCache"
	entries := seedPromoted(t, dir, ws, 1)
	src := entries[0]
	// the source holds the current body; a stale body's content file still exists.
	const stale = "STALE_BODY_FROM_OLD_VERSION"
	writeContextContentFile(dir, ws, src.ID, stale)
	legacy := rawMeta(src, hashContent(stale))
	writeRawMetaCache(t, dir, ws, []map[string]any{legacy})

	res, err := RecallContext(dir, ws, "", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	got := res["entries"].([]ContextEntry)
	if len(got) != 1 || got[0].Content != src.Content {
		t.Fatalf("legacy cache must fall back to the source body %q, got %+v", src.Content, got)
	}
	var metas []contextEntryMeta
	if err := readJSON(contextMetaCachePath(dir, ws), &metas); err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ContentDigest != contentDigest(src.Content) {
		t.Fatalf("legacy cache was not migrated to a full digest: %+v", metas)
	}
}

// Root hardening: acceptance compares the FULL SHA-256 digest recorded with the ranked
// metadata. A body whose 64-bit filename hash matches but whose full digest does not is
// withheld, never returned under the approval.
func TestRecallTrustComparisonUsesFullDigest(t *testing.T) {
	dir := t.TempDir()
	ws := "wsFullDigest"
	entries := seedPromoted(t, dir, ws, 1)
	src := entries[0]
	m := rawMeta(src, hashContent(src.Content))
	m["content_digest"] = contentDigestForTest("the approved version had different text")
	writeRawMetaCache(t, dir, ws, []map[string]any{m})

	res, err := RecallContext(dir, ws, "", nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := res["entries"].([]ContextEntry); len(got) != 0 {
		t.Fatalf("body with matching short hash but different full digest was returned: %+v", got)
	}
	if res["consistency_withheld"] != 1 {
		t.Fatalf("expected the mismatched entry to be withheld, got %v", res["consistency_withheld"])
	}
}

func contentDigestForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Fable review #1: with no query/paths, the current working changes must actually
// boost matching memories (they were computed and then discarded).
func TestPlainRecallBoostsMemoryAboutChangedFiles(t *testing.T) {
	dir := t.TempDir()
	ws := "wsRecallFocus"
	entries := seedPromoted(t, dir, ws, 12)
	entries[0].Paths = []string{"changed.go"} // the OLDEST memory is about the changed file
	if err := saveContextEntries(dir, ws, entries); err != nil {
		t.Fatal(err)
	}
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
