package govstore

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// reviewChange is the change the review tests record against, at head.
func reviewChange(head string) ReviewChange {
	return ReviewChange{Repository: "/repo", BaseRef: "main", MergeBase: "mb0", Head: head, DiffSHA256: "d-" + head, DiffBytes: 42}
}

// checked are the checks of a finding whose code WS-65 found in a changed hunk.
var checked = ReviewChecks{CodePresent: "yes", InChangedHunk: "yes", InScope: "yes", SymbolResolved: "unknown"}

func finding(path string, start, end int, code string) ReviewFindingInput {
	return ReviewFindingInput{Path: path, StartLine: start, EndLine: end, Side: "new", AnchorStatus: "exact_new",
		Checks: checked, Support: "supported", Category: "bug", Severity: "high", Content: "nil map write", ExistingCode: code}
}

// unplaced is a finding the resolver could not place.
func unplaced(path string) ReviewFindingInput {
	return ReviewFindingInput{Path: path, AnchorStatus: AnchorUnanchored, AnchorReason: "no_snippet",
		Checks:  ReviewChecks{CodePresent: "no", InChangedHunk: "no", InScope: "yes", SymbolResolved: "unknown"},
		Support: "unsupported", Category: "bug", Severity: "low", Content: "x"}
}

func recordReview(t *testing.T, s Store, who Actor, in ReviewRecordInput) (ReviewRecord, []ReviewFinding) {
	t.Helper()
	if in.WorkspaceID == "" {
		in.WorkspaceID = "ws1"
	}
	if in.Lineage == "" {
		in.Lineage = "main@mb0"
	}
	if in.Change.Head == "" {
		in.Change = reviewChange("h1")
	}
	if in.ChangedFiles == nil {
		in.ChangedFiles = []string{"a.go", "b.go"}
	}
	if in.Producer == "" {
		in.Producer = "agent"
	}
	var rec ReviewRecord
	var fs []ReviewFinding
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		rec, fs, err = tx.RecordReview(context.Background(), in, who)
		return err
	})
	return rec, fs
}

// with is f changed by edit.
func with(f ReviewFindingInput, edit func(*ReviewFindingInput)) ReviewFindingInput {
	edit(&f)
	return f
}

func triage(s Store, who Actor, id, verdict string) (ReviewFinding, error) {
	var f ReviewFinding
	err := s.Update(context.Background(), func(tx Tx) error {
		var err error
		f, err = tx.TriageReviewFinding(context.Background(), ReviewTriageInput{WorkspaceID: "ws1", FindingID: id, Verdict: verdict}, who)
		return err
	})
	return f, err
}

// The duplicate rule is open-code-review's: strict IoU above 0.6, and a single-line
// range never matches a multi-line one.
func TestSpanIoUFollowsOpenCodeReview(t *testing.T) {
	for _, c := range []struct {
		a, b [2]int
		want float64
		dup  bool
	}{
		{[2]int{7, 7}, [2]int{7, 7}, 1, true},
		{[2]int{7, 7}, [2]int{8, 8}, 0, false},
		{[2]int{7, 7}, [2]int{7, 9}, 0, false},   // single-line never equals multi-line
		{[2]int{1, 5}, [2]int{1, 3}, 0.6, false}, // exactly at the threshold is not a duplicate
		{[2]int{1, 4}, [2]int{1, 3}, 0.75, true},
		{[2]int{10, 14}, [2]int{12, 20}, 3.0 / 11, false},
		{[2]int{1, 3}, [2]int{5, 9}, 0, false},
		{[2]int{0, 0}, [2]int{0, 0}, 0, false}, // unanchored matches nothing
	} {
		got := SpanIoU(c.a, c.b)
		if got != c.want || (got > dedupeIoU) != c.dup || SpanIoU(c.b, c.a) != got {
			t.Errorf("SpanIoU(%v, %v) = %v, want %v (duplicate %t)", c.a, c.b, got, c.want, c.dup)
		}
	}
	if got := NormalizedCode("  +  m[k] = v\r\n\n-\tdelete(m, k)  \n   "); got != "m[k] = v\ndelete(m, k)" {
		t.Errorf("NormalizedCode = %q", got)
	}
}

// The denominator is every changed file: an excluded file is listed not_reviewed with
// its reason, and a file the reviewer did not mention is not_reviewed(not_reported).
func TestReviewCoverageCountsEveryChangedFile(t *testing.T) {
	s := openTestStore(t, newClock())
	rec, fs := recordReview(t, s, alice, ReviewRecordInput{
		ChangedFiles: []string{"a.go", "go.sum", "vendor/x.go", "c.go"},
		Coverage: []ReviewCoverage{
			{Path: "a.go", Status: CoverageReviewed},
			{Path: "go.sum", Status: CoverageNotReviewed, Reason: "excluded: lockfile"},
			{Path: "c.go", Status: CoverageReviewed},
		},
	})
	want := []ReviewCoverage{
		{Path: "a.go", Status: CoverageReviewed},
		{Path: "go.sum", Status: CoverageNotReviewed, Reason: "excluded: lockfile"},
		{Path: "vendor/x.go", Status: CoverageNotReviewed, Reason: ReasonNotReported},
		{Path: "c.go", Status: CoverageReviewed},
	}
	if !slices.Equal(rec.Coverage, want) || rec.ChangedFiles != 4 || rec.Reviewed != 2 || rec.NotReviewed != 2 ||
		rec.CoverageRate != 0.5 || rec.TerminalState != ReviewPartial || len(fs) != 0 || rec.Author != "alice" {
		t.Fatalf("record = %+v, findings %d", rec, len(fs))
	}
	got, err := s.GetReviewRecord(context.Background(), "ws1", rec.ID)
	if err != nil || got.Change != reviewChange("h1") || got.Lineage != "main@mb0" || !slices.Equal(got.Coverage, want) {
		t.Fatalf("GetReviewRecord = %+v, %v", got, err)
	}
	all, _ := recordReview(t, s, alice, ReviewRecordInput{ChangedFiles: []string{"a.go"},
		Coverage: []ReviewCoverage{{Path: "a.go", Status: CoverageReviewed}}})
	none, _ := recordReview(t, s, alice, ReviewRecordInput{ChangedFiles: []string{"a.go"}})
	if all.TerminalState != ReviewComplete || all.CoverageRate != 1 || none.TerminalState != ReviewSkipped || none.CoverageRate != 0 {
		t.Fatalf("terminal states: %s %v, %s %v", all.TerminalState, all.CoverageRate, none.TerminalState, none.CoverageRate)
	}
	for name, in := range map[string]ReviewRecordInput{
		"report outside the change": {Coverage: []ReviewCoverage{{Path: "z.go", Status: CoverageReviewed}}},
		"report twice":              {Coverage: []ReviewCoverage{{Path: "a.go", Status: CoverageReviewed}, {Path: "a.go", Status: CoverageReviewed}}},
		"not_reviewed, no reason":   {Coverage: []ReviewCoverage{{Path: "a.go", Status: CoverageNotReviewed}}},
		"waived is not a state":     {Coverage: []ReviewCoverage{{Path: "a.go", Status: "waived", Reason: "ok"}}},
		"no changed files":          {ChangedFiles: []string{}},
		"escaping changed file":     {ChangedFiles: []string{"../etc/passwd"}},
		"changed file twice":        {ChangedFiles: []string{"a.go", "a.go"}},
		"unknown producer":          {Producer: "bot"},
		"unknown category":          {Findings: []ReviewFindingInput{with(unplaced("a.go"), func(f *ReviewFindingInput) { f.Category = "nit" })}},
		"lines without an anchor":   {Findings: []ReviewFindingInput{with(unplaced("a.go"), func(f *ReviewFindingInput) { f.StartLine, f.EndLine = 3, 3 })}},
		"anchor without a side":     {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.Side = "" })}},
		"a claimed range":           {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.AnchorStatus = "claimed" })}},
		"unknown support":           {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.Support = "plausible" })}},
		"a check that is no fact":   {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.Checks.InScope = "" })}},
		"escaping refiled_from":     {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.RefiledFrom = "../a.go" })}},
		"too many notes":            {Findings: []ReviewFindingInput{with(finding("a.go", 3, 3, "x"), func(f *ReviewFindingInput) { f.Normalized = make([]string, maxReviewNotes+1) })}},
		"empty content":             {Findings: []ReviewFindingInput{with(unplaced("a.go"), func(f *ReviewFindingInput) { f.Content = " " })}},
		"absolute finding path":     {Findings: []ReviewFindingInput{finding("/a.go", 1, 1, "x")}},
		"no lineage":                {Lineage: " "},
		"a source without a digest": {Sources: []ReviewSource{{Kind: "findings_file", Ref: "f.json"}}},
	} {
		if in.ChangedFiles == nil {
			in.ChangedFiles = []string{"a.go"}
		}
		if in.Lineage == "" {
			in.Lineage = "main@mb0"
		}
		if in.Producer == "" {
			in.Producer = "agent"
		}
		in.WorkspaceID, in.Change = "ws1", reviewChange("h1")
		err := s.Update(context.Background(), func(tx Tx) error {
			_, _, err := tx.RecordReview(context.Background(), in, alice)
			return err
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.GetReviewRecord(context.Background(), "ws1", "rvr_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing record: %v", err)
	}
}

// Dedupe: same path, side and head, strict IoU > 0.6 and the same normalized quoted
// code is a duplicate; the overlap alone is only a possible duplicate, with a job, and
// neither is corroboration.
func TestReviewDedupeIsExactOrPossible(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	_, first := recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{finding("a.go", 10, 14, "m[k] = v\nreturn m")}})
	f1 := first[0]
	if f1.Dedupe.Result != DedupeCreated || f1.Status != FindingOpen || f1.Commit != "h1" || f1.CodeSHA256 == "" ||
		f1.Fingerprint == "" || f1.RecordID == "" {
		t.Fatalf("first finding = %+v", f1)
	}
	old := finding("a.go", 10, 14, "m[k] = v")
	old.Side = "old"
	in := []ReviewFindingInput{
		finding("a.go", 10, 14, "+ m[k] = v\n\n  return m  "), // same code after normalization: duplicate
		finding("a.go", 10, 12, "m[k] = v\nreturn m"),         // IoU exactly 0.6: new
		finding("a.go", 10, 13, "m[k] = w"),                   // IoU 0.8 (and 0.75 with the one above), other code: possible duplicate
		finding("a.go", 10, 10, "m[k] = v\nreturn m"),         // single-line vs multi-line: new
		finding("b.go", 10, 14, "m[k] = v\nreturn m"),         // another path: new
		old, // another side: new
	}
	rec, got := recordReview(t, s, bob, ReviewRecordInput{Findings: in})
	want := []ReviewDedupe{
		{Result: DedupeDuplicateOf, Of: f1.ID, IoU: 1},
		{Result: DedupeCreated},
		{Result: DedupePossibleDuplicate, Of: f1.ID, IoU: 0.8},
		{Result: DedupeCreated}, {Result: DedupeCreated}, {Result: DedupeCreated},
	}
	for i, f := range got {
		d := f.Dedupe
		d.JobID = ""
		if d != want[i] || f.RecordID != rec.ID {
			t.Errorf("finding %d dedupe = %+v, want %+v", i, f.Dedupe, want[i])
		}
	}
	if got[0].Status != FindingDuplicate || got[2].Status != FindingOpen || got[2].Dedupe.JobID == "" {
		t.Fatalf("statuses %s %s, job %q", got[0].Status, got[2].Status, got[2].Dedupe.JobID)
	}
	jobs, err := s.ListJobs(ctx, JobFilter{WorkspaceID: "ws1", Kinds: []string{JobReviewDuplicate}})
	if err != nil || len(jobs) != 1 || jobs[0].ID != got[2].Dedupe.JobID || jobs[0].SubjectKind != SubjectReviewFinding ||
		!slices.Equal(jobs[0].CandidateIDs, []string{f1.ID, got[2].ID}) {
		t.Fatalf("review_duplicate jobs = %+v, %v", jobs, err)
	}
	// Other lineages and other heads are not compared (lines from another head are
	// incomparable until re-anchored).
	_, other := recordReview(t, s, bob, ReviewRecordInput{Lineage: "main@mb9", Findings: []ReviewFindingInput{finding("a.go", 10, 14, "m[k] = v\nreturn m")}})
	_, later := recordReview(t, s, bob, ReviewRecordInput{Change: reviewChange("h2"), Findings: []ReviewFindingInput{finding("a.go", 10, 14, "m[k] = v\nreturn m")}})
	if other[0].Dedupe.Result != DedupeCreated || later[0].Dedupe.Result != DedupeCreated {
		t.Fatalf("other lineage %+v, later head %+v", other[0].Dedupe, later[0].Dedupe)
	}
	// Duplicates never corroborate: the first finding has no corroboration.
	now, _ := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb0", Path: "a.go", Statuses: []string{FindingOpen}})
	for _, f := range now {
		if f.Corroborations != 0 {
			t.Fatalf("%s has %d corroborations from dedupe", f.ID, f.Corroborations)
		}
	}
	if len(now) != 6 { // f1, the 0.6 one, the possible duplicate, the single-line, the old-side one and the h2 one
		t.Fatalf("open findings on a.go in the lineage = %d", len(now))
	}
	// Old-side lines count in the merge base, which a later head of the lineage shares:
	// the same deleted code quoted at h2 is a duplicate of the h1 finding.
	if got[5].Commit != "mb0" {
		t.Fatalf("an old-side finding's lines count in %s, want the merge base", got[5].Commit)
	}
	_, again := recordReview(t, s, carol, ReviewRecordInput{Change: reviewChange("h2"), Findings: []ReviewFindingInput{old}})
	if d := again[0].Dedupe; d.Result != DedupeDuplicateOf || d.Of != got[5].ID {
		t.Fatalf("the old-side finding at a later head: %+v", d)
	}
}

// Corroboration comes only from an explicit confirm by a principal other than the
// author (and the open-mode identity); the author cannot confirm or dismiss.
func TestReviewTriageAndCorroboration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	_, fs := recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{
		finding("a.go", 3, 4, "x := 1\ny := 2"), finding("a.go", 3, 4, "x := 1\ny := 2"),
	}})
	id, dup := fs[0].ID, fs[1].ID
	for _, verdict := range []string{"confirm", "dismiss"} {
		if _, err := triage(s, Actor{Principal: " Alice "}, id, verdict); !errors.Is(err, ErrSelfTriage) || !errors.Is(err, ErrInvalid) {
			t.Fatalf("author %s: %v", verdict, err)
		}
	}
	if _, err := triage(s, bob, dup, "confirm"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("triaging a duplicate: %v", err)
	}
	if _, err := triage(s, bob, id, "approve"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown verdict: %v", err)
	}
	if _, err := triage(s, bob, "rvf_missing", "confirm"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing finding: %v", err)
	}
	steps := []struct {
		who            Actor
		verdict        string
		status         string
		corroborations int
	}{
		{bob, "confirm", FindingConfirmed, 1},
		{bob, "confirm", FindingConfirmed, 1}, // the latest verdict per principal counts once
		{anon, "confirm", FindingConfirmed, 1},
		{carol, "dismiss", FindingDismissed, 1},
		{carol, "confirm", FindingConfirmed, 2},
		{alice, "fixed", FindingFixed, 2}, // the author may mark it fixed
	}
	for i, st := range steps {
		f, err := triage(s, st.who, id, st.verdict)
		if err != nil || f.Status != st.status || f.Corroborations != st.corroborations {
			t.Fatalf("step %d (%s %s): status %s, corroborations %d, %v", i, st.who.Principal, st.verdict, f.Status, f.Corroborations, err)
		}
	}
	evs, _ := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", EntryID: id, Types: []string{EventReviewTriage}})
	if len(evs) != len(steps) || evs[0].Principal != "bob" || evs[len(evs)-1].Principal != "alice" {
		t.Fatalf("triage events = %+v", evs)
	}
	if n := countRows(t, s, "SELECT count(*) FROM outcomes WHERE entry_id = ? AND subject_kind = 'review_finding'", id); n != 4 {
		t.Fatalf("outcome rows = %d, want one per principal", n)
	}
}

// Re-anchoring moves a finding to a later head, so the next record there dedupes
// against it; code that is gone makes an open finding outdated, never deleted.
func TestReviewReanchorMovesOrOutdates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	_, fs := recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{
		finding("a.go", 10, 12, "one()\ntwo()"), finding("a.go", 30, 30, "three()"), finding("b.go", 5, 5, "four()"), unplaced("c.go"),
	}})
	if _, err := triage(s, bob, fs[2].ID, "confirm"); err != nil {
		t.Fatal(err)
	}
	var n int
	mustUpdate(t, s, func(tx Tx) error {
		var err error
		n, err = tx.ReanchorReviewFindings(ctx, "ws1", []ReviewReanchor{
			{FindingID: fs[0].ID, Commit: "h2", StartLine: 20, EndLine: 22, AnchorStatus: "file"},
			{FindingID: fs[1].ID, Commit: "h2", Outdated: true},
			{FindingID: fs[2].ID, Commit: "h2", Outdated: true},
			{FindingID: fs[3].ID, Commit: "h2", StartLine: 1, EndLine: 1, AnchorStatus: "file"}, // unanchored: left alone
		}, alice)
		return err
	})
	got, _ := s.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb0"})
	if n != 3 || got[0].Commit != "h2" || got[0].StartLine != 20 || got[0].EndLine != 22 || got[0].AnchorStatus != "file" ||
		got[0].Status != FindingOpen || got[1].Status != FindingOutdated || got[1].OutdatedAt == "" || got[1].Commit != "h1" ||
		got[2].Status != FindingConfirmed || got[2].OutdatedAt == "" || got[3].Commit != "h1" || got[3].StartLine != 0 {
		t.Fatalf("after re-anchoring (%d): %+v", n, got)
	}
	mustUpdate(t, s, func(tx Tx) error { // a finding already at head, or outdated, is left alone
		var err error
		n, err = tx.ReanchorReviewFindings(ctx, "ws1", []ReviewReanchor{{FindingID: fs[0].ID, Commit: "h2", StartLine: 1, EndLine: 1, AnchorStatus: "file"},
			{FindingID: fs[1].ID, Commit: "h2", Outdated: true}}, alice)
		return err
	})
	if n != 0 {
		t.Fatalf("re-anchoring again changed %d", n)
	}
	_, next := recordReview(t, s, bob, ReviewRecordInput{Change: reviewChange("h2"), Findings: []ReviewFindingInput{
		finding("a.go", 20, 22, "one()\ntwo()"), finding("a.go", 30, 30, "three()"),
	}})
	if next[0].Dedupe.Result != DedupeDuplicateOf || next[0].Dedupe.Of != fs[0].ID || next[1].Dedupe.Result != DedupeCreated {
		t.Fatalf("dedupe after re-anchoring: %+v, %+v", next[0].Dedupe, next[1].Dedupe)
	}
	evs, _ := s.ListEvents(ctx, EventFilter{WorkspaceID: "ws1", EntryID: fs[0].ID, Types: []string{EventReviewReanchor}})
	if len(evs) != 1 {
		t.Fatalf("re-anchor events = %d", len(evs))
	}
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.ReanchorReviewFindings(ctx, "ws1", []ReviewReanchor{{FindingID: next[1].ID, Commit: "h3", StartLine: 5, EndLine: 4, AnchorStatus: "file"}}, alice)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a reversed range: %v", err)
	}
	err = s.Update(ctx, func(tx Tx) error {
		_, err := tx.ReanchorReviewFindings(ctx, "ws1", []ReviewReanchor{{FindingID: next[1].ID, StartLine: 5, EndLine: 5, AnchorStatus: "file"}}, alice)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a move without a commit: %v", err)
	}
}

// Review subjects share the tables with memory without mixing: their ids never name an
// entry, memory reads by entry id see memory rows only, and the rebuilt anchors and
// outcomes still refuse a row that names no subject.
func TestReviewSubjectsStayApartFromMemory(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, newClock())
	propose(t, s, "m1", "t", "c", "a.go")
	_, fs := recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{finding("a.go", 1, 1, "x")}})
	err := s.Update(ctx, func(tx Tx) error {
		_, err := tx.InsertEntry(ctx, NewEntry{ID: fs[0].ID, WorkspaceID: "ws1", Title: "t", Content: "c"}, alice)
		return err
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("an entry named after a finding: %v", err)
	}
	if as, _ := s.ListAnchors(ctx, "m1"); len(as) != 1 || as[0].Kind != AnchorPath {
		t.Fatalf("memory anchors = %+v", as)
	}
	mustUpdate(t, s, func(tx Tx) error {
		_, err := tx.RecordOutcome(ctx, OutcomeInput{EntryID: "m1", Outcome: OutcomeHelpful}, bob)
		return err
	})
	if _, err := triage(s, bob, fs[0].ID, "confirm"); err != nil {
		t.Fatal(err)
	}
	if sum, _ := s.SummarizeOutcomes(ctx, "m1"); sum.Helpful != 1 {
		t.Fatalf("memory outcomes = %+v", sum)
	}
	for _, q := range []string{
		"INSERT INTO anchors (entry_id, ordinal, kind, value) VALUES ('ghost', 0, 'path', 'x')",
		"INSERT INTO anchors (subject_kind, entry_id, ordinal, kind, value) VALUES ('review_finding', 'ghost', 0, 'review_range', 'x')",
		"INSERT INTO outcomes (entry_id, revision, principal, principal_key, outcome, at) VALUES ('ghost', 1, 'b', 'b', 'helpful', 'now')",
		"INSERT INTO outcomes (subject_kind, entry_id, revision, principal, principal_key, outcome, at) VALUES ('memory', 'm1', 1, 'c', 'c', 'confirm', 'now')",
		"INSERT INTO events (workspace_id, entry_id, type, at, subject_kind) VALUES ('ws1', 'm1', 'note', 'now', 'review_record')",
		"UPDATE anchors SET entry_id = 'm1' WHERE subject_kind = 'review_finding'",
		"UPDATE events SET subject_kind = 'memory' WHERE subject_kind = 'review_finding'",
	} {
		if err := rawExec(t, s.path, q); err == nil {
			t.Errorf("%s: accepted", q)
		}
	}
	if n := countRows(t, s, "SELECT count(*) FROM events WHERE entry_id = ? AND subject_kind = 'review_finding'", fs[0].ID); n != 2 {
		t.Fatalf("finding events = %d", n)
	}
	// A caller-authored event writes memory history only: it cannot forge a review
	// subject's creation or triage.
	for _, typ := range []string{EventReviewRecord, EventReviewFinding, EventReviewTriage, EventReviewReanchor} {
		err := s.Update(ctx, func(tx Tx) error {
			_, err := tx.AppendEvent(ctx, EventInput{WorkspaceID: "ws1", Type: typ, Data: map[string]any{"status": "confirmed"}}, bob)
			return err
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("AppendEvent(%s): %v", typ, err)
		}
	}
}

// A file at migration 2 upgrades in place: its anchors, outcomes and jobs survive the
// rebuild as memory rows, and the checks the foreign keys made still hold.
func TestMigrationUpgradesV2FileToReviewSubjects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gov.db")
	all := migrations
	defer func() { migrations = all }()
	migrations = all[:2]
	old, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	// This build writes the migration 3 columns, so the v2 rows are written as SQL.
	for _, q := range []string{
		`INSERT INTO entries (id, workspace_id, source, source_key, created_at, updated_at) VALUES ('m1', 'ws1', 'alice', 'alice', 'now', 'now')`,
		`INSERT INTO anchors (entry_id, ordinal, kind, value) VALUES ('m1', 0, 'path', 'a.go'), ('m1', 1, 'path', 'b.go')`,
		`INSERT INTO outcomes (entry_id, revision, principal, principal_key, outcome, at) VALUES ('m1', 1, 'bob', 'bob', 'misleading', 'now')`,
		`INSERT INTO events (workspace_id, entry_id, type, at) VALUES ('ws1', 'm1', 'propose', 'now')`,
		`INSERT INTO jobs (id, workspace_id, kind, conflict_signature, created_at, updated_at) VALUES ('job_1', 'ws1', 'stale_anchor', 'stale:m1', 'now', 'now')`,
	} {
		if err := rawExec(t, path, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	migrations = all

	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()
	if info, err := s.SchemaInfo(ctx); err != nil || info.Version != 3 {
		t.Fatalf("schema after upgrade = %+v, %v", info, err)
	}
	as, _ := s.ListAnchors(ctx, "m1")
	sum, _ := s.SummarizeOutcomes(ctx, "m1")
	jobs, _ := s.ListJobs(ctx, JobFilter{WorkspaceID: "ws1"})
	if len(as) != 2 || sum.Misleading != 1 || len(jobs) != 1 || jobs[0].SubjectKind != SubjectMemory {
		t.Fatalf("v2 data after the upgrade: anchors %+v, outcomes %+v, jobs %+v", as, sum, jobs)
	}
	for _, table := range []string{"anchors", "outcomes", "events"} {
		if n := countRows(t, s, "SELECT count(*) FROM "+table+" WHERE subject_kind <> 'memory'"); n != 0 {
			t.Fatalf("%s: %d rows are not memory rows", table, n)
		}
	}
	if err := rawExec(t, path, "INSERT INTO anchors (entry_id, ordinal, kind, value) VALUES ('ghost', 0, 'path', 'x')"); err == nil {
		t.Fatal("an anchor naming no entry was accepted after the rebuild")
	}
	if _, fs := recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{finding("a.go", 1, 1, "x")}}); len(fs) != 1 {
		t.Fatal("review findings are writable after the upgrade")
	}
	_ = s.Close()
	again, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	_ = again.Close()
}

// A record's findings, a lineage's findings, a path's findings in a lineage and one
// finding are index lookups, whatever the store holds: no plan step scans the anchors,
// events or outcomes tables, a lineage is found through its own index, not by reading
// every review anchor's event, and each anchor's event through its entry id, not the
// workspace's events.
func TestReviewFindingQueriesUseIndexes(t *testing.T) {
	s := openTestStore(t, newClock())
	recordReview(t, s, alice, ReviewRecordInput{Findings: []ReviewFindingInput{finding("a.go", 1, 2, "x\ny")}})
	for name, c := range map[string]struct {
		f     ReviewFindingFilter
		ids   []string
		index string
	}{
		"a record's findings":        {ReviewFindingFilter{WorkspaceID: "ws1", Statuses: []string{FindingOpen}}, []string{"rvf_1", "rvf_2"}, ""},
		"a path's findings (dedupe)": {ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb0", Path: "a.go"}, nil, "anchors_review_lineage"},
		"a lineage page":             {ReviewFindingFilter{WorkspaceID: "ws1", Lineage: "main@mb0", AfterSeq: 3, Limit: 500}, nil, "anchors_review_lineage"},
		"one finding":                {ReviewFindingFilter{}, nil, ""},
	} {
		q, args := reviewFindingsSQL(c.f, c.ids)
		if name == "one finding" {
			q, args = reviewFindingQuery+" AND a.entry_id = ?)", []any{OpenModeIdentity, "ws1", "rvf_1"}
		}
		rows, err := s.readers.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		for _, index := range []string{c.index, "events_entry"} {
			if index != "" && !slices.ContainsFunc(plan, func(step string) bool { return strings.Contains(step, "INDEX "+index+" ") }) {
				t.Errorf("%s: plan does not use %s: %v", name, index, plan)
			}
		}
		for _, step := range plan {
			for _, table := range []string{"a", "ev", "t", "o"} {
				if step == "SCAN "+table || strings.HasPrefix(step, "SCAN "+table+" ") {
					t.Errorf("%s: plan scans %s: %v", name, table, plan)
				}
			}
		}
	}
}
