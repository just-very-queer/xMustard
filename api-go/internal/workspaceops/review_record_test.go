//go:build review

package workspaceops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

func claim(path string, line int, code string) ReviewFindingClaim {
	return ReviewFindingClaim{Path: path, StartLine: line, EndLine: line, Category: "bug", Severity: "high",
		Content: "wrong constant", ExistingCode: code}
}

func recordReviewOK(t *testing.T, dir, ws string, who ContextActor, sub ReviewSubmission) *ReviewRecordResult {
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

// A record binds the change merge approval digests, and its coverage denominator is
// every file that change touches, whatever the reviewer reports.
func TestReviewRecordBindsTheChangeAndEveryChangedFile(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, git := reviewRepo(t)
	res := recordReviewOK(t, dir, ws, reviewer, ReviewSubmission{
		Findings: []ReviewFindingClaim{claim("a.go", 3, "func A() int { return 2 }"), {Path: "b.go", Category: "style",
			Severity: "low", Content: "no doc comment"}},
		Coverage: []govstore.ReviewCoverage{{Path: "a.go", Status: govstore.CoverageReviewed}},
	})
	change, err := DiffReviewedChange(ctx, dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rec := res.Record
	if rec.Change.DiffSHA256 != change.DiffSHA256 || rec.Change.Head != git("rev-parse", "HEAD") ||
		rec.Lineage != "main@"+change.MergeBase || rec.Author != "rev-1" || rec.AuthorOwner != "rev-1" || rec.Producer != "agent" ||
		res.Label != ReviewEvidenceLabel {
		t.Fatalf("record = %+v", rec)
	}
	wantCoverage := []govstore.ReviewCoverage{{Path: "a.go", Status: govstore.CoverageReviewed},
		{Path: "b.go", Status: govstore.CoverageNotReviewed, Reason: govstore.ReasonNotReported}}
	if !slices.Equal(rec.Coverage, wantCoverage) || rec.CoverageRate != 0.5 || rec.TerminalState != govstore.ReviewPartial {
		t.Fatalf("coverage = %+v rate %v state %s", rec.Coverage, rec.CoverageRate, rec.TerminalState)
	}
	f := res.Findings
	if len(f) != 2 || f[0].AnchorStatus != govstore.AnchorClaimed || f[0].StartLine != 3 || f[0].Side != "new" ||
		f[1].AnchorStatus != govstore.AnchorUnanchored || f[1].StartLine != 0 || f[0].Status != govstore.FindingOpen {
		t.Fatalf("findings = %+v", f)
	}
	shown, err := ShowReviewRecord(ctx, dir, ws, rec.ID)
	if err != nil || shown.Record.ID != rec.ID || len(shown.Findings) != 2 {
		t.Fatalf("show = %+v, %v", shown, err)
	}
	for name, sub := range map[string]ReviewSubmission{
		"coverage outside the change": {Coverage: []govstore.ReviewCoverage{{Path: "z.go", Status: govstore.CoverageReviewed}}},
		"head already in base":        {BaseRef: "feature", HeadRef: "HEAD"},
		"option-shaped ref":           {BaseRef: "--output=/tmp/x", HeadRef: "HEAD"},
		"escaping finding path":       {Findings: []ReviewFindingClaim{claim("../x.go", 1, "x")}},
	} {
		if sub.BaseRef == "" {
			sub.BaseRef, sub.HeadRef = "main", "HEAD"
		}
		if _, err := RecordReview(ctx, dir, ws, reviewer, sub); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v, want ErrInvalidInput", name, err)
		}
	}
}

// A later head of the same lineage moves the earlier findings first: one whose file did
// not change keeps its lines, so the same finding at the new head is its duplicate; one
// whose file changed stays unresolved until a locator (WS-65) is plugged in.
func TestReviewRecordReanchorsTheLineageLazily(t *testing.T) {
	dir, ws, root, git := reviewRepo(t)
	first := recordReviewOK(t, dir, ws, reviewer, ReviewSubmission{Findings: []ReviewFindingClaim{
		claim("a.go", 3, "func A() int { return 2 }"), claim("b.go", 3, "func B() {}"),
	}})
	commit := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", "edit "+name)
	}
	commit("b.go", "package a\n\n// B does nothing.\nfunc B() {}\n")
	second := recordReviewOK(t, dir, ws, ContextActor{ID: "rev-2"}, ReviewSubmission{Findings: []ReviewFindingClaim{
		claim("a.go", 3, "func A() int { return 2 }"), claim("b.go", 4, "func B() {}"),
	}})
	if r := second.Reanchored; r.Kept != 1 || r.Unresolved != 1 || r.Moved != 0 {
		t.Fatalf("reanchored = %+v", r)
	}
	if d := second.Findings[0].Dedupe; d.Result != govstore.DedupeDuplicateOf || d.Of != first.Findings[0].ID {
		t.Fatalf("a.go finding at the new head: %+v", d)
	}
	if d := second.Findings[1].Dedupe; d.Result != govstore.DedupeCreated {
		t.Fatalf("b.go finding, its earlier one unresolved: %+v", d)
	}

	// With a locator plugged in, the changed file's finding moves and dedupes.
	relocateFindings = func(_ context.Context, _, _ string, fs []govstore.ReviewFinding) ([]govstore.ReviewReanchor, error) {
		var out []govstore.ReviewReanchor
		for _, f := range fs {
			out = append(out, govstore.ReviewReanchor{FindingID: f.ID, StartLine: f.StartLine + 1, EndLine: f.EndLine + 1, AnchorStatus: "file"})
		}
		return out, nil
	}
	defer func() { relocateFindings = nil }()
	commit("c.go", "package a\n")
	third := recordReviewOK(t, dir, ws, ContextActor{ID: "rev-3"}, ReviewSubmission{Findings: []ReviewFindingClaim{claim("b.go", 4, "func B() {}")}})
	// Kept: the first a.go finding and the second b.go one (neither file changed since
	// the second head); moved: the first b.go finding, whose file changed since the first.
	if r := third.Reanchored; r.Kept != 2 || r.Moved != 1 || r.Unresolved != 0 {
		t.Fatalf("reanchored with a locator = %+v", r)
	}
	// Both b.go findings now sit at line 4 with the same code; the oldest is the original.
	if d := third.Findings[0].Dedupe; d.Result != govstore.DedupeDuplicateOf || d.Of != first.Findings[1].ID {
		t.Fatalf("b.go finding after its file changed: %+v", d)
	}
}

// Triage: the author may not confirm or dismiss; under the owner-distinct policy neither
// may a principal of the author's owner; a distinct confirm corroborates.
func TestReviewTriageKeepsReviewersDistinct(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, _ := reviewRepo(t)
	author := ContextActor{ID: "rev-1", Owner: "team"}
	res := recordReviewOK(t, dir, ws, author, ReviewSubmission{Findings: []ReviewFindingClaim{claim("a.go", 3, "func A() int { return 2 }")}})
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

// Decoding is closed and bounded, and normalizes what open-code-review output varies in.
func TestDecodeReviewFindingsIsClosedAndBounded(t *testing.T) {
	claims, notes, err := DecodeReviewFindings([]byte(`{"findings": [{"path": "./a.go", "content": "x", "category": "BUG",
		"severity": "blocker", "start_line": 9, "end_line": 7, "existing_code": "` + strings.Repeat("y", reviewSnippetBytes+1) + `"}]}`))
	if err != nil || len(claims) != 1 || claims[0].Path != "a.go" || claims[0].Category != "bug" || claims[0].Severity != "low" ||
		claims[0].ExistingCode != "" || len(notes) != 2 {
		t.Fatalf("decode = %+v, %v, %v", claims, notes, err)
	}
	placed, _ := claimedPlacement(context.Background(), ReviewedChange{}, claims)
	if placed[0].StartLine != 7 || placed[0].EndLine != 9 || placed[0].AnchorStatus != govstore.AnchorClaimed {
		t.Fatalf("placement = %+v", placed[0])
	}
	for name, in := range map[string]string{
		"unknown member": `[{"path": "a.go", "content": "x", "thinking": "..."}]`,
		"trailing data":  `[] []`,
		"not an array":   `{"findings": {}}`,
		"too many":       "[" + strings.TrimSuffix(strings.Repeat(`{"path": "a.go", "content": "x"},`, MaxReviewFindingsCall+1), ",") + "]",
		"wrapper member": `{"findings": [], "summary": "ok"}`,
	} {
		if _, _, err := DecodeReviewFindings([]byte(in)); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := DecodeReviewCoverage([]byte(`[{"path": "a.go", "status": "reviewed", "extra": 1}]`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("coverage with an unknown member: %v", err)
	}
}
