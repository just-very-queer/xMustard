//go:build review

package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/review"
)

// claim is a finding as a reviewer states it, quoting code.
func claim(path, code string) map[string]any {
	return map[string]any{"path": path, "content": "wrong constant", "existing_code": code, "category": "bug", "severity": "high"}
}

// batchOf decodes findings as a findings file holds them (WS-65's decoder).
func batchOf(t testing.TB, fs ...map[string]any) *review.Batch {
	t.Helper()
	raw, err := json.Marshal(fs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := review.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	b.Source.Kind, b.Source.Ref = "findings_file", "findings.json"
	return b
}

func recordReviewOK(t testing.TB, dir, ws string, who ContextActor, sub ReviewSubmission) *ReviewRecordResult {
	t.Helper()
	if sub.BaseRef == "" {
		sub.BaseRef, sub.HeadRef = "main", "HEAD"
	}
	res, err := RecordReview(context.Background(), dir, ws, who, sub)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var reviewer = ContextActor{ID: "rev-1", Owner: "rev-1", Kind: PrincipalAgent}

// A record binds the change merge approval digests, its findings are anchored against
// that change (WS-65), and its coverage denominator is every file the change touches,
// whatever the reviewer reports.
func TestReviewRecordBindsTheChangeAndEveryChangedFile(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, git := reviewRepo(t)
	secret := "ghp_" + strings.Repeat("aB3dE5", 6)
	res := recordReviewOK(t, dir, ws, reviewer, ReviewSubmission{
		Batches: []*review.Batch{batchOf(t,
			claim("a.go", "func A() int { return 2 }"), // in a.go's hunk, new side
			claim("a.go", "func B() {}"),               // not in a.go: re-filed to b.go, the only file holding it
			claim("a.go", "func A() int { return 1 }"), // deleted: a.go's old side
			map[string]any{"path": "b.go", "category": "Style", "severity": "trivial " + secret, "content": "no doc comment; token " + secret},
		)},
		Coverage: []review.Coverage{{Path: "a.go", Status: govstore.CoverageReviewed}},
	})
	change, err := DiffReviewedChange(ctx, dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rec := res.Record
	if rec.Change.DiffSHA256 != change.DiffSHA256 || rec.Change.Head != git("rev-parse", "HEAD") ||
		rec.Lineage != "main@"+change.MergeBase+":refs/heads/feature" || rec.Author != "rev-1" || rec.AuthorOwner != "rev-1" || rec.Producer != "agent" ||
		len(rec.Sources) != 1 || rec.Sources[0].Ref != "findings.json" || len(rec.Sources[0].SHA256) != 64 || res.Label != ReviewRecordLabel {
		t.Fatalf("record = %+v", rec)
	}
	wantCoverage := []govstore.ReviewCoverage{{Path: "a.go", Status: govstore.CoverageReviewed},
		{Path: "b.go", Status: govstore.CoverageNotReviewed, Reason: govstore.ReasonNotReported}}
	if !slices.Equal(rec.Coverage, wantCoverage) || rec.CoverageRate != 0.5 || rec.TerminalState != govstore.ReviewPartial {
		t.Fatalf("coverage = %+v rate %v state %s", rec.Coverage, rec.CoverageRate, rec.TerminalState)
	}
	f := res.Findings
	type place struct {
		path, side, status, refiled, support string
		start                                int
		commit                               string
	}
	want := []place{
		{"a.go", "new", "exact_new", "", "supported", 3, change.Head},
		{"b.go", "new", "relocated", "a.go", "supported", 3, change.Head},
		{"a.go", "old", "exact_old", "", "supported", 3, change.MergeBase},
		{"b.go", "", "unanchored", "", "unsupported", 0, change.Head},
	}
	for i, w := range want {
		got := place{f[i].Path, f[i].Side, f[i].AnchorStatus, f[i].RefiledFrom, f[i].Support, f[i].StartLine, f[i].Commit}
		if got != w || f[i].Status != govstore.FindingOpen {
			t.Errorf("finding %d = %+v, want %+v", i, got, w)
		}
	}
	if f[0].Checks.InChangedHunk != "yes" || f[0].Checks.SymbolResolved != "unknown" || f[3].AnchorReason != "no_snippet" {
		t.Fatalf("checks %+v, reason %q", f[0].Checks, f[3].AnchorReason)
	}
	// The note quoting the unknown severity is scrubbed as the content is, in the stored
	// finding and in the notes the result echoes.
	if f[3].Category != "style" || f[3].Severity != "low" || len(f[3].Normalized) != 1 || strings.Contains(f[3].Normalized[0], secret) ||
		strings.Contains(strings.Join(res.Normalized, " "), secret) || strings.Contains(f[3].Content, secret) || res.Redactions == nil ||
		res.Counts == nil || res.Counts.Findings != 4 {
		t.Fatalf("normalized %v (%v), content %q, redactions %+v, counts %+v", f[3].Normalized, res.Normalized, f[3].Content,
			res.Redactions, res.Counts)
	}
	shown, err := ShowReviewRecord(ctx, dir, ws, rec.ID)
	if err != nil || shown.Record.ID != rec.ID || len(shown.Findings) != 4 {
		t.Fatalf("show = %+v, %v", shown, err)
	}
	// A record without findings needs no anchoring; it is coverage evidence only.
	bare := recordReviewOK(t, dir, ws, reviewer, ReviewSubmission{Coverage: []review.Coverage{
		{Path: "a.go", Status: govstore.CoverageReviewed}, {Path: "b.go", Status: govstore.CoverageReviewed}}})
	if bare.Record.TerminalState != govstore.ReviewComplete || len(bare.Findings) != 0 || bare.Counts != nil {
		t.Fatalf("a record without findings: %+v", bare)
	}
	fifty := make([]map[string]any, review.MaxFindings)
	for i := range fifty {
		fifty[i] = claim("a.go", "func A() int { return 2 }")
	}
	full := batchOf(t, fifty...)
	for name, sub := range map[string]ReviewSubmission{
		"coverage outside the change":  {Coverage: []review.Coverage{{Path: "z.go", Status: govstore.CoverageReviewed}}},
		"waived is no coverage state":  {Coverage: []review.Coverage{{Path: "a.go", Status: "waived", Reason: "ok"}}},
		"head already in base":         {BaseRef: "feature", HeadRef: "HEAD"},
		"option-shaped ref":            {BaseRef: "--output=/tmp/x", HeadRef: "HEAD"},
		"over 500 findings":            {Batches: slices.Repeat([]*review.Batch{full}, govstore.MaxReviewFindings/review.MaxFindings+1)},
		"a commit with no lineage":     {BaseRef: "main", HeadRef: change.Head},
		"producer human from an agent": {Producer: "human"},
	} {
		if sub.BaseRef == "" {
			sub.BaseRef, sub.HeadRef = "main", "HEAD"
		}
		if _, err := RecordReview(ctx, dir, ws, reviewer, sub); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		}
	}
}

// A later head of the same lineage re-anchors the earlier findings before the new ones
// are compared with them: a finding whose file did not change keeps its lines, one whose
// code moved follows it, one whose code is gone becomes outdated (never deleted), and one
// whose code now occurs twice elsewhere is left where it was. Old-side lines count in the
// merge base, which the lineage's heads share, so they never move.
func TestReviewRecordReanchorsTheLineageLazily(t *testing.T) {
	ctx := context.Background()
	dir, ws, root, git := reviewRepo(t)
	commit := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", ".")
		git("commit", "-q", "-m", "edit")
	}
	aNew, aOld, b := claim("a.go", "func A() int { return 2 }"), claim("a.go", "func A() int { return 1 }"), claim("b.go", "func B() {}")
	first := recordReviewOK(t, dir, ws, reviewer, ReviewSubmission{Batches: []*review.Batch{batchOf(t, aNew, b, aOld)}})
	if r := first.Reanchored; r.Kept+r.Moved+r.Outdated+r.Unresolved != 0 || r.Errors != nil {
		t.Fatalf("a first record moved %+v", r)
	}

	commit(map[string]string{"b.go": "package a\n\n// B does nothing.\nfunc B() {}\n"})
	second := recordReviewOK(t, dir, ws, ContextActor{ID: "rev-2"}, ReviewSubmission{Batches: []*review.Batch{batchOf(t, aNew, b, aOld)}})
	if r := second.Reanchored; r.Kept != 1 || r.Moved != 1 || r.Outdated != 0 || r.Unresolved != 0 {
		t.Fatalf("reanchored = %+v", r)
	}
	for i, d := range []govstore.ReviewDedupe{second.Findings[0].Dedupe, second.Findings[1].Dedupe, second.Findings[2].Dedupe} {
		if d.Result != govstore.DedupeDuplicateOf || d.Of != first.Findings[i].ID {
			t.Errorf("finding %d at the second head: %+v, want a duplicate of %s", i, d, first.Findings[i].ID)
		}
	}

	// b.go loses B; a.go's line moves and now occurs twice, neither at its old line.
	commit(map[string]string{"b.go": "package a\n", "a.go": "package a\n\n// A twice.\nfunc A() int { return 2 }\n\nfunc A() int { return 2 }\n"})
	third := recordReviewOK(t, dir, ws, ContextActor{ID: "rev-3"}, ReviewSubmission{})
	if r := third.Reanchored; r.Kept != 0 || r.Moved != 0 || r.Outdated != 1 || r.Unresolved != 1 {
		t.Fatalf("reanchored at the third head = %+v", r)
	}
	shown, err := ShowReviewRecord(ctx, dir, ws, first.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	head2 := second.Record.Change.Head
	type state struct {
		status, commit string
		start          int
		current        bool
	}
	want := []state{
		{govstore.FindingOpen, head2, 3, false},                        // ambiguous at the third head: left at the second
		{govstore.FindingOutdated, head2, 4, false},                    // B is gone: outdated where it was last seen
		{govstore.FindingOpen, first.Record.Change.MergeBase, 3, true}, // old side: the merge base never moved
	}
	for i, w := range want {
		f := shown.Findings[i]
		if got := (state{f.Status, f.Commit, f.StartLine, f.AnchorCurrent}); got != w {
			t.Errorf("first record's finding %d = %+v, want %+v", i, got, w)
		}
	}
}

// Sibling branches cut from one commit share a merge base, not a line of history. By
// default their lineages differ, since the head's branch is part of the name. In a
// lineage they are made to share, a finding at one branch's head is never re-anchored to
// the other's (its commit is no ancestor of that head), so recording them alternately
// leaves every finding open where it was, and each branch's repeat is a duplicate of its
// own first finding. A shared lineage stays bound to its base ref.
func TestReviewRecordKeepsSiblingBranchesApart(t *testing.T) {
	ctx := context.Background()
	dir, ws, root, git := reviewRepo(t)
	git("checkout", "-q", "-b", "other", "main")
	if err := os.WriteFile(filepath.Join(root, "c.go"), []byte("package a\n\nfunc C() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "sibling")
	mergeBase := git("merge-base", "main", "feature")
	x := ReviewSubmission{BaseRef: "main", HeadRef: "feature", Batches: []*review.Batch{batchOf(t, claim("b.go", "func B() {}"))}}
	y := ReviewSubmission{BaseRef: "main", HeadRef: "other", Batches: []*review.Batch{batchOf(t, claim("c.go", "func C() {}"))}}
	for _, lineage := range []string{"", "shared"} {
		x.Lineage, y.Lineage = lineage, lineage
		var firsts []*ReviewRecordResult
		for round := range 2 {
			for i, sub := range []ReviewSubmission{x, y} {
				res := recordReviewOK(t, dir, ws, reviewer, sub)
				r, d := res.Reanchored, res.Findings[0].Dedupe
				wantLineage, diverged := cmp.Or(lineage, "main@"+mergeBase+":refs/heads/"+sub.HeadRef), 0
				if lineage != "" && (round > 0 || i > 0) {
					diverged = 1 // the sibling's finding
				}
				if res.Record.Lineage != wantLineage || r.Diverged != diverged || r.Kept+r.Moved+r.Outdated+r.Unresolved != 0 {
					t.Fatalf("lineage %q round %d, %s: lineage %s, reanchored %+v", lineage, round, sub.HeadRef, res.Record.Lineage, r)
				}
				if round == 0 {
					firsts = append(firsts, res)
				} else if d.Result != govstore.DedupeDuplicateOf || d.Of != firsts[i].Findings[0].ID {
					t.Fatalf("lineage %q, %s again: %+v, want a duplicate of %s", lineage, sub.HeadRef, d, firsts[i].Findings[0].ID)
				}
			}
		}
		for i, first := range firsts {
			shown, err := ShowReviewRecord(ctx, dir, ws, first.Record.ID)
			if err != nil {
				t.Fatal(err)
			}
			f := shown.Findings[0]
			// y was recorded last, so in the shared lineage only its findings are current.
			current := lineage == "" || i == 1
			if f.Status != govstore.FindingOpen || f.OutdatedAt != "" || f.Commit != first.Record.Change.Head || f.AnchorCurrent != current {
				t.Fatalf("lineage %q: %s's first finding is %+v", lineage, first.Record.Change.Head, f)
			}
		}
	}
	x.BaseRef, x.Lineage = "main~0", "shared"
	if _, err := RecordReview(ctx, dir, ws, reviewer, x); !errors.Is(err, govstore.ErrConflict) {
		t.Fatalf("the shared lineage from another base ref: %v", err)
	}
}

// Triage: the author may not confirm or dismiss; under the owner-distinct policy neither
// may a principal of the author's owner; a distinct confirm corroborates.
func TestReviewTriageKeepsReviewersDistinct(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, _ := reviewRepo(t)
	author := ContextActor{ID: "rev-1", Owner: "team"}
	res := recordReviewOK(t, dir, ws, author, ReviewSubmission{Batches: []*review.Batch{batchOf(t, claim("a.go", "func A() int { return 2 }"))}})
	id := res.Findings[0].ID
	if _, err := TriageReviewFinding(ctx, dir, ws, author, id, "confirm", ""); !errors.Is(err, ErrInvalidInput) ||
		!strings.Contains(err.Error(), "cannot confirm or dismiss") {
		t.Fatalf("the author confirming: %v", err)
	}
	writeTestSettings(t, dir, appSettings{PrincipalDistinctness: DistinctOwner})
	teammate := ContextActor{ID: "rev-2", Owner: "team"}
	if _, err := TriageReviewFinding(ctx, dir, ws, teammate, id, "dismiss", ""); !errors.Is(err, ErrSameOwner) {
		t.Fatalf("a same-owner dismiss under the owner policy: %v", err)
	}
	f, err := TriageReviewFinding(ctx, dir, ws, teammate, id, "fixed", "fixed in 1234")
	if err != nil || f.Status != govstore.FindingFixed || f.Corroborations != 0 {
		t.Fatalf("a same-owner fixed: %+v, %v", f, err)
	}
	f, err = TriageReviewFinding(ctx, dir, ws, ContextActor{ID: "rev-3", Owner: "other"}, id, "confirm", "")
	if err != nil || f.Status != govstore.FindingConfirmed || f.Corroborations != 1 {
		t.Fatalf("a distinct confirm: %+v, %v", f, err)
	}
	if _, err := TriageReviewFinding(ctx, dir, ws, author, "../x", "fixed", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unsafe id: %v", err)
	}
}

// A review command runs as a token that resolves, is not presence-only, is scoped to the
// workspace and holds a needed role; with no credentials it is the open-mode identity.
func TestReviewActorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if a, err := ReviewActor(dir, "ws", "", RoleProposer); err != nil || a.ID != OpenModeIdentity || !a.OpenMode {
		t.Fatalf("open mode: %+v, %v", a, err)
	}
	mint := func(id, role string, scope []string, ident TokenIdentity) string {
		raw, err := MintIdentityToken(dir, id, role, 0, scope, ident)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	agent := mint("agent-1", "agent", nil, TokenIdentity{Owner: "ops"})
	reader := mint("reader-1", RoleReader, nil, TokenIdentity{})
	scoped := mint("scoped-1", "agent", []string{"other"}, TokenIdentity{})
	presence := mint("human-1", RoleHumanApprover, nil, TokenIdentity{Kind: PrincipalHuman, PresenceOnly: true})
	for name, c := range map[string]struct{ raw, want string }{
		"no token":       {"", "needs a valid token"},
		"unknown token":  {"nope", "needs a valid token"},
		"presence-only":  {presence, "presence-only"},
		"another scope":  {scoped, "not scoped"},
		"lacking a role": {reader, "needs the proposer role"},
	} {
		if _, err := ReviewActor(dir, "ws", c.raw, RoleProposer); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if a, err := ReviewActor(dir, "ws", agent, RoleVerifier, RoleHumanApprover); err != nil || a.ID != "agent-1" || a.Owner != "ops" {
		t.Fatalf("an agent token: %+v, %v", a, err)
	}
}

// BenchmarkReviewRecord records 50 findings at a head whose lineage already holds the
// same 50 at an earlier head, over a change of 200 files of 300 lines: 25 of the earlier
// findings' files did not change (kept), 25 gained a line above the finding (searched at
// the new head and moved), and every new finding is then a duplicate. Each iteration's
// earlier record is written, untimed, under a lineage of its own.
func BenchmarkReviewRecord(b *testing.B) {
	dir, ws, root, git := reviewRepo(b)
	body := func(i int, edited, shifted bool) string {
		var s strings.Builder
		if shifted && i%2 == 0 {
			s.WriteString("// shifted\n")
		}
		for j := range 300 {
			fmt.Fprintf(&s, "\tv%03d_%03d := step(%d, %d)\n", i, j, i, j)
			if edited && (j == 100 || j == 200) {
				fmt.Fprintf(&s, "\tcheck%03d_%03d()\n", i, j)
			}
		}
		return s.String()
	}
	commit := func(edited, shifted bool) {
		for i := range 200 {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte(body(i, edited, shifted)), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		git("add", ".")
		git("commit", "-q", "-m", "files")
	}
	git("checkout", "-q", "main")
	commit(false, false)
	git("checkout", "-q", "feature")
	git("rebase", "-q", "main")
	commit(true, false)
	earlier := git("rev-parse", "HEAD")
	commit(true, true)
	var claims []map[string]any
	for i := range 50 {
		claims = append(claims, claim(fmt.Sprintf("f%03d.go", i), fmt.Sprintf("check%03d_100()", i)))
	}
	batch := batchOf(b, claims...)
	var res *ReviewRecordResult
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		sub := ReviewSubmission{BaseRef: "main", HeadRef: earlier, Lineage: fmt.Sprintf("bench-%d", i), Batches: []*review.Batch{batch}}
		b.StopTimer()
		recordReviewOK(b, dir, ws, reviewer, sub)
		b.StartTimer()
		sub.HeadRef = "HEAD"
		var err error
		if res, err = RecordReview(context.Background(), dir, ws, ContextActor{ID: "rev-2"}, sub); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	dups := 0
	for _, f := range res.Findings {
		if f.Dedupe.Result == govstore.DedupeDuplicateOf {
			dups++
		}
	}
	if r := res.Reanchored; r.Kept != 25 || r.Moved != 25 || dups != 50 {
		b.Fatalf("reanchored %+v, %d duplicates", r, dups)
	}
	b.ReportMetric(float64(res.Record.Change.DiffBytes), "diff_bytes")
	b.ReportMetric(float64(res.HeadReads.Bytes), "head_bytes")
}
