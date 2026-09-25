package govstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

const fixtureWS = "repo-2d5d3dd8af"

func loadFixture(t *testing.T) ([]byte, []LegacyEntry) {
	t.Helper()
	raw, err := os.ReadFile("testdata/context_entries.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []LegacyEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	return raw, entries
}

func exportAll(t *testing.T, s Store, ws string) ([]byte, []LegacyEntry) {
	t.Helper()
	var buf bytes.Buffer
	if err := s.ExportContextEntriesJSON(context.Background(), ws, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	var out []LegacyEntry
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("export is not valid JSON: %v\n%s", err, buf.String())
	}
	return buf.Bytes(), out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestImportRoundTripsLegacyEntries(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	raw, want := loadFixture(t)
	rep, err := s.ImportContextEntriesJSON(ctx, fixtureWS, bytes.NewReader(raw), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	sum := sha256.Sum256(raw)
	if rep.Imported != len(want) || rep.Unchanged != 0 || len(rep.Conflicts) != 0 || len(rep.Relabelled) != 0 ||
		len(rep.Skipped) != 0 || rep.SourceDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("report = %+v", rep)
	}

	// Export gives back exactly the fixture: content, verifications (order, notes,
	// times), path hashes including the missing-file sentinel, modes, and the full
	// content digest (derived when the fixture lacks it).
	_, got := exportAll(t, s, fixtureWS)
	if len(got) != len(want) {
		t.Fatalf("exported %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		w := want[i]
		w.ContentDigest = Digest(w.Content)
		if a, b := mustJSON(t, got[i]), mustJSON(t, w); a != b {
			t.Fatalf("entry %d round trip differs\n got: %s\nwant: %s", i, a, b)
		}
	}

	// The store holds the state natively, not as a blob.
	peer, _ := s.GetEntry(ctx, "ctx_3f9a1c2e4b5d")
	if !peer.Promoted || peer.VerificationMode != ModePeerVerified || peer.Source != "agent-a" || peer.Revision != 1 ||
		peer.CreatedAt != "2026-09-20T08:15:30.123456789Z" || peer.UpdatedAt != "2026-09-20T08:17:12.123456789Z" {
		t.Fatalf("imported entry = %+v", peer)
	}
	tl, _ := s.Tally(ctx, "ctx_3f9a1c2e4b5d", 0)
	if tl.PeerApprovals != 2 {
		t.Fatalf("tally = %+v", tl)
	}
	anchors, _ := s.ListAnchors(ctx, "ctx_3f9a1c2e4b5d")
	if len(anchors) != 3 || anchors[2].BaselineState != BaselineMissing || anchors[0].BaselineHash != Digest("makefile v1") {
		t.Fatalf("anchors = %+v", anchors)
	}
	if hits, _ := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: fixtureWS, Text: "clippy", ServedOnly: true}); len(hits) != 1 {
		t.Fatalf("imported entry not searchable: %+v", hits)
	}
	if c, _ := s.EntryContents(ctx, []string{"ctx_openmode0001"}); !strings.Contains(c["ctx_openmode0001"].Content, "résumé") {
		t.Fatalf("unicode content = %+v", c)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{WorkspaceID: fixtureWS, Types: []string{EventImport}})
	if len(evs) != len(want)+1 || evs[len(evs)-1].EntryID != "" {
		t.Fatalf("import events = %+v", evs)
	}

	// Idempotent: a second import changes nothing.
	entryEvents := countRows(t, s, "SELECT count(*) FROM events WHERE entry_id IS NOT NULL")
	rep2, err := s.ImportContextEntriesJSON(ctx, fixtureWS, bytes.NewReader(raw), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Imported != 0 || rep2.Unchanged != len(want) || len(rep2.Conflicts) != 0 {
		t.Fatalf("second import = %+v", rep2)
	}
	if n := countRows(t, s, "SELECT count(*) FROM entries"); n != len(want) {
		t.Fatalf("entries after re-import = %d", n)
	}
	if n := countRows(t, s, "SELECT count(*) FROM events WHERE entry_id IS NOT NULL"); n != entryEvents {
		t.Fatalf("re-import added entry events: %d -> %d", entryEvents, n)
	}

	// Export -> import into a fresh store -> export is byte-identical.
	first, _ := exportAll(t, s, fixtureWS)
	s2 := openTestStore(t, newClock())
	if _, err := s2.ImportContextEntriesJSON(ctx, fixtureWS, bytes.NewReader(first), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	second, _ := exportAll(t, s2, fixtureWS)
	if !bytes.Equal(first, second) {
		t.Fatalf("export not stable across re-import\n%s\n---\n%s", first, second)
	}

	// A diverged stored entry is reported, never overwritten.
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RecordVote(ctx, VoteInput{EntryID: "ctx_pending00001", Verdict: VerdictApprove}, Actor{Principal: "agent-d"})
		return err
	})
	rep3, err := s.ImportContextEntriesJSON(ctx, fixtureWS, bytes.NewReader(raw), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep3.Conflicts) != 1 || rep3.Conflicts[0].EntryID != "ctx_pending00001" || rep3.Unchanged != len(want)-1 {
		t.Fatalf("diverged import = %+v", rep3)
	}
	if votes, _ := s.ListVotes(ctx, "ctx_pending00001", 0); len(votes) != 2 {
		t.Fatalf("import overwrote a diverged entry: %+v", votes)
	}
}

// Legacy files written before verification_mode existed, or holding labels the votes
// do not support, import with the conservative label: nothing unverified may appear
// peer-verified.
func TestImportLabelsConservatively(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	legacy := `[
	  {"id": "pre_w0_open", "workspace_id": "ws1", "title": "t", "content": "a", "source": "author",
	   "permission": "readonly", "status": "verified", "promoted": true, "required_verifications": 2,
	   "verifications": [{"agent": "author", "approve": true, "at": "2026-01-01T00:00:00Z"},
	                     {"agent": "Anonymous", "approve": true, "at": "2026-01-01T00:00:00Z"},
	                     {"agent": "peer-1", "approve": true, "at": "2026-01-01T00:00:00Z"}],
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "claims_peer", "workspace_id": "ws1", "title": "t", "content": "b", "source": "author",
	   "permission": "readonly", "status": "verified", "promoted": true, "required_verifications": 2,
	   "verification_mode": "peer_verified",
	   "verifications": [{"agent": "author", "approve": true, "at": "2026-01-01T00:00:00Z"},
	                     {"agent": "AUTHOR ", "approve": true, "at": "2026-01-01T00:00:01Z"}],
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "single_one_peer", "workspace_id": "ws1", "title": "t", "content": "c", "source": "author",
	   "permission": "readonly", "status": "verified", "promoted": true, "required_verifications": 1,
	   "verification_mode": "peer_verified",
	   "verifications": [{"agent": "peer-1", "approve": true, "at": "2026-01-01T00:00:00Z"}],
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "digest_broken", "workspace_id": "ws1", "title": "t", "content": "tampered", "source": "author",
	   "permission": "readonly", "status": "verified", "promoted": true, "required_verifications": 2,
	   "verification_mode": "peer_verified", "content_digest": "` + Digest("original") + `",
	   "verifications": [{"agent": "peer-1", "approve": true, "at": "2026-01-01T00:00:00Z"},
	                     {"agent": "peer-2", "approve": true, "at": "2026-01-01T00:00:00Z"}],
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "real_peer", "workspace_id": "ws1", "title": "t", "content": "d", "source": "author",
	   "permission": "readonly", "status": "verified", "promoted": true, "required_verifications": 2,
	   "verifications": [{"agent": "peer-1", "approve": true, "at": "2026-01-01T00:00:00Z"},
	                     {"agent": "peer-2", "approve": true, "at": "2026-01-01T00:00:00Z"}],
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	]`
	rep, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(legacy), ImportOptions{Threshold: 2})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if rep.Imported != 5 || len(rep.Relabelled) != 5 {
		t.Fatalf("report = %+v", rep)
	}
	for id, want := range map[string]struct {
		promoted bool
		mode     string
	}{
		"pre_w0_open":     {true, ModeSelfAssertedOpenMode}, // one peer of two needed; open-mode vote present
		"claims_peer":     {true, ModeSingleAgent},          // only the author (twice, case-folded)
		"single_one_peer": {true, ModeSingleAgent},          // threshold 2 applies to a one-vote quorum
		"digest_broken":   {false, ""},                      // content no longer matches what was approved
		"real_peer":       {true, ModePeerVerified},         // derived, and supported
	} {
		e, err := s.GetEntry(ctx, id)
		if err != nil || e.Promoted != want.promoted || e.VerificationMode != want.mode {
			t.Fatalf("%s = promoted %t mode %q (err %v), want %+v", id, e.Promoted, e.VerificationMode, err, want)
		}
	}
	if votes, _ := s.ListVotes(ctx, "claims_peer", 0); len(votes) != 1 {
		t.Fatalf("case-folded duplicate verdicts not merged: %+v", votes)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
}

func TestImportRejectsMalformedEntriesAtomically(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	legacy := `[
	  {"id": "good", "title": "t", "content": "fine", "source": "a", "permission": "readonly", "status": "pending",
	   "promoted": false, "verifications": [], "required_verifications": 2,
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "bad", "title": "t", "content": "contradiction", "source": "a", "permission": "readonly",
	   "status": "verified", "promoted": false, "verifications": [], "required_verifications": 2,
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "../escape", "title": "t", "content": "x", "source": "a", "permission": "readonly", "status": "pending",
	   "promoted": false, "verifications": [], "required_verifications": 2,
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"},
	  {"id": "other_ws", "workspace_id": "ws-other", "title": "t", "content": "x", "source": "a", "permission": "readonly",
	   "status": "pending", "promoted": false, "verifications": [], "required_verifications": 2,
	   "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	]`
	if _, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(legacy), ImportOptions{}); !errors.Is(err, errInvalidEntry) {
		t.Fatalf("import of malformed file: %v", err)
	}
	if n := countRows(t, s, "SELECT count(*) FROM entries"); n != 0 {
		t.Fatalf("failed import left %d entries", n)
	}
	rep, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(legacy), ImportOptions{SkipInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Imported != 1 || len(rep.Skipped) != 3 {
		t.Fatalf("skip report = %+v", rep)
	}
	for _, bad := range []string{`{}`, `[{"id": 1}]`, `[`} {
		if _, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(bad), ImportOptions{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("import %q: %v", bad, err)
		}
	}
	if rep, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader("null"), ImportOptions{}); err != nil || rep.Imported != 0 {
		t.Fatalf("null file: %+v %v", rep, err)
	}
}

func TestImportLegacyFeedbackIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	legacy := `[{"path": "api-go/go.mod", "retrieval_count": 4, "verify_count": 2, "run_success": 1, "run_fail": 0,
	  "last_used": "2026-09-01T00:00:00Z"}, {"path": "./Makefile", "retrieval_count": 99999, "last_used": "bad"}]`
	for range 2 {
		n, err := s.ImportFeedbackJSON(ctx, "ws1", strings.NewReader(legacy))
		if err != nil || n != 2 {
			t.Fatalf("import feedback: %d %v", n, err)
		}
	}
	fb, _ := s.GetFeedback(ctx, "ws1", []string{"api-go/go.mod", "Makefile"})
	if fb["api-go/go.mod"].RetrievalCount != 4 || fb["api-go/go.mod"].VerifyCount != 2 || fb["Makefile"].RetrievalCount != feedbackCap {
		t.Fatalf("feedback = %+v", fb)
	}
}

// A legacy path hash for a path the entry does not list becomes an undeclared anchor:
// it round-trips, and it is searchable.
func TestImportKeepsUndeclaredBaselineAnchors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	legacy := `[{"id": "undeclared", "title": "t", "content": "c", "source": "a", "permission": "readonly",
	  "status": "pending", "promoted": false, "verifications": [], "required_verifications": 2,
	  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
	  "paths": ["listed.go"], "path_hashes": {"listed.go": "h1", "zzunlisted.go": "h2"}}]`
	if _, err := s.ImportContextEntriesJSON(ctx, "ws1", strings.NewReader(legacy), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	_, got := exportAll(t, s, "ws1")
	if len(got) != 1 || len(got[0].Paths) != 1 || got[0].PathHashes["zzunlisted.go"] != "h2" {
		t.Fatalf("export = %+v", got)
	}
	if hits, _ := s.SearchMemories(ctx, MemoryQuery{WorkspaceID: "ws1", Text: "zzunlisted"}); len(hits) != 1 {
		t.Fatalf("undeclared anchor not indexed: %+v", hits)
	}
}
