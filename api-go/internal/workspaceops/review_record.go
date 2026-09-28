//go:build review

package workspaceops

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"xmustard/api-go/internal/anchor"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
	"xmustard/api-go/internal/review"
)

// Review records and findings (WS-66; PAR-REV-06 and the coverage of PAR-REV-07), built
// only with the review build tag. A record binds one review to the change merge approval
// digests (the merge base of a base ref and a head): its coverage over every file that
// change touches, and its findings, anchored by WS-65 against the same diff bytes and
// deduplicated by the store within the record's lineage. Before a record at a new head is
// compared with its lineage, the lineage's earlier findings are re-anchored to it, lazily
// and in one batch. A record is evidence for the human's merge decision; nothing here
// approves a change.

// ReviewRecordLabel is carried by every review record result.
const ReviewRecordLabel = "evidence only: a review record informs the human's merge decision; " +
	"no review result approves a change"

// maxReviewNote bounds one normalization note as a record stores it (a note can quote an
// unknown category or severity at any length).
const maxReviewNote = 256

// ReviewSubmission is one review of the change from the merge base of BaseRef to
// HeadRef: its findings inputs, decoded by WS-65's review package, and the reviewer's
// coverage report. Lineage defaults to "<base_ref>@<merge_base>", so a branch's later
// heads on the same merge base share one; Producer defaults to agent.
type ReviewSubmission struct {
	BaseRef  string
	HeadRef  string
	Lineage  string
	Producer string
	Batches  []*review.Batch
	Coverage []review.Coverage
}

// ReviewReanchorReport counts how the lineage's earlier findings came to this change:
// kept (their file did not change, so their lines stand), moved (their quoted code was
// found once in the file's new version), outdated (it was found nowhere) and unresolved
// (found several times, in a file not read, or with no code to search for; they stay
// where they were, out of positional dedupe).
type ReviewReanchorReport struct {
	Kept       int      `json:"kept"`
	Moved      int      `json:"moved"`
	Outdated   int      `json:"outdated"`
	Unresolved int      `json:"unresolved"`
	Errors     []string `json:"errors,omitempty"`
}

// ReviewRecordResult is a record with its findings, and, when it holds findings, how
// they were anchored.
type ReviewRecordResult struct {
	Record     govstore.ReviewRecord    `json:"record"`
	Findings   []govstore.ReviewFinding `json:"findings"`
	Counts     *review.Counts           `json:"counts,omitempty"`
	HeadReads  *HeadReads               `json:"head_reads,omitempty"`
	Reanchored *ReviewReanchorReport    `json:"reanchored,omitempty"`
	Normalized []string                 `json:"normalized,omitempty"`
	Redactions *redact.Report           `json:"redactions,omitempty"`
	Label      string                   `json:"label"`
}

// ReviewActor resolves the token a review command runs as, holding one of roles. With
// no credentials configured it is the open-mode identity. Otherwise, in order and fail
// closed: a token is given and resolves, it is not presence-only (that token is typed at
// the approval prompt only), it is not the reserved open-mode identity, it is scoped to
// the workspace, and it holds one of roles.
func ReviewActor(dataDir, workspaceID, raw string, roles ...string) (ContextActor, error) {
	p, configured := ResolveAuth(dataDir, raw)
	switch {
	case !configured:
		return ContextActor{ID: OpenModeIdentity, OpenMode: true, SessionID: "xmustard-ops"}, nil
	case p == nil:
		return ContextActor{}, errors.New("a review command needs a valid token when auth is configured (--token-file or XMUSTARD_API_TOKEN)")
	case p.PresenceOnly:
		return ContextActor{}, errors.New("a presence-only token is accepted only at the approval prompt")
	case IsOpenModeIdentity(p.ID):
		return ContextActor{}, fmt.Errorf("principal id %q is reserved for open mode", p.ID)
	case !p.AllowsWorkspace(workspaceID):
		return ContextActor{}, fmt.Errorf("the token is not scoped to workspace %s", workspaceID)
	case !slices.ContainsFunc(roles, p.Has):
		return ContextActor{}, fmt.Errorf("%s needs the %s role", p.ID, strings.Join(roles, " or "))
	}
	return ContextActor{ID: p.ID, Owner: p.Owner, Kind: p.Kind, SessionID: "xmustard-ops", Verifier: p.Has(RoleVerifier)}, nil
}

// RecordReview records actor's review of the change from the merge base of sub.BaseRef
// to sub.HeadRef. The findings are anchored against that change (WS-65), the coverage
// denominator is every file it touches, the lineage's earlier findings are re-anchored to
// it, and then the store records and deduplicates.
func RecordReview(ctx context.Context, dataDir, workspaceID string, actor ContextActor, sub ReviewSubmission) (*ReviewRecordResult, error) {
	batch, sources, err := mergeBatches(sub.Batches)
	if err != nil {
		return nil, err
	}
	change, anchored, err := anchorSubmission(ctx, dataDir, workspaceID, sub.BaseRef, sub.HeadRef, batch)
	if err != nil {
		return nil, err
	}
	if change.MergeBase == change.Head {
		return nil, fmt.Errorf("%s is already in %s; there is no change to review: %w", sub.HeadRef, sub.BaseRef, ErrInvalidInput)
	}
	files, err := reviewChangedFiles(ctx, change.Repository, change.MergeBase, change.Head)
	if err != nil {
		return nil, err
	}
	in := govstore.ReviewRecordInput{WorkspaceID: workspaceID, Lineage: cmp.Or(sub.Lineage, change.BaseRef+"@"+change.MergeBase),
		Change: govstore.ReviewChange{Repository: change.Repository, BaseRef: change.BaseRef, MergeBase: change.MergeBase,
			Head: change.Head, DiffSHA256: change.DiffSHA256, DiffBytes: change.DiffBytes},
		ChangedFiles: files, Producer: cmp.Or(sub.Producer, "agent"), Sources: sources}
	out := &ReviewRecordResult{Normalized: batch.Normalized, Label: ReviewRecordLabel}
	var red ingestRedaction
	for _, c := range sub.Coverage {
		red.scrub(&c.Reason)
		in.Coverage = append(in.Coverage, govstore.ReviewCoverage{Path: c.Path, Status: c.Status, Reason: c.Reason})
	}
	if anchored != nil {
		out.Counts, out.HeadReads = &anchored.Counts, &anchored.HeadReads
		for _, a := range anchored.Findings {
			f := findingInput(a)
			red.finding(&f)
			in.Findings = append(in.Findings, f)
		}
	}
	if red.rep.Redacted {
		out.Redactions = &red.rep
	}
	moves, report, err := reanchorLineage(ctx, dataDir, workspaceID, in.Lineage, in.Change)
	if err != nil {
		return nil, err
	}
	out.Reanchored = report
	sa := actor.storeActor(change.Repository)
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		if _, err := tx.ReanchorReviewFindings(ctx, workspaceID, moves, sa); err != nil {
			return err
		}
		var err error
		out.Record, out.Findings, err = tx.RecordReview(ctx, in, sa)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mergeBatches joins a review's inputs into one batch, anchored together so re-filing
// sees every finding's file. Each input is kept as a source and names its own notes.
func mergeBatches(batches []*review.Batch) (*review.Batch, []govstore.ReviewSource, error) {
	merged := &review.Batch{}
	var sources []govstore.ReviewSource
	for _, b := range batches {
		merged.Findings = append(merged.Findings, b.Findings...)
		for _, n := range b.Normalized {
			merged.Normalized = append(merged.Normalized, b.Source.Ref+": "+n)
		}
		sources = append(sources, govstore.ReviewSource{Kind: b.Source.Kind, Ref: b.Source.Ref, Bytes: b.Source.Bytes, SHA256: b.Source.SHA256})
	}
	if n := len(merged.Findings); n > govstore.MaxReviewFindings {
		return nil, nil, fmt.Errorf("%d findings, at most %d per record: %w", n, govstore.MaxReviewFindings, ErrInvalidInput)
	}
	return merged, sources, nil
}

// anchorSubmission seals the change and anchors the findings in its diff (WS-65). A
// review without findings needs only the change's digest, so its diff is streamed into
// the digest and never held.
func anchorSubmission(ctx context.Context, dataDir, workspaceID, baseRef, headRef string, batch *review.Batch) (ReviewedChange, *ReviewAnchoring, error) {
	if len(batch.Findings) == 0 {
		change, err := DiffReviewedChange(ctx, dataDir, workspaceID, baseRef, headRef)
		return change, nil, err
	}
	a, err := AnchorReviewFindings(ctx, dataDir, workspaceID, baseRef, headRef, batch)
	if err != nil {
		return ReviewedChange{}, nil, err
	}
	return a.Change, a, nil
}

// findingInput is an anchored finding as the store records it: at its anchor (a
// re-filed finding under the file that holds its code), or, unanchored, under the path it
// was filed against with no lines.
func findingInput(a review.Anchored) govstore.ReviewFindingInput {
	f := govstore.ReviewFindingInput{Path: a.Path, AnchorStatus: string(a.Anchor.Status), AnchorReason: a.Anchor.Reason,
		RefiledFrom: a.Anchor.RefiledFrom, Support: a.Support, Category: a.Category, Severity: a.Severity, Content: a.Content,
		ExistingCode: a.ExistingCode, SuggestionCode: a.SuggestionCode,
		Checks: govstore.ReviewChecks{CodePresent: string(a.Checks.CodePresent), InChangedHunk: string(a.Checks.InChangedHunk),
			InScope: string(a.Checks.InScope), SymbolResolved: string(a.Checks.SymbolResolved)}}
	if a.Anchor.Status != anchor.Unanchored {
		f.Path, f.StartLine, f.EndLine, f.Side = a.Anchor.Path, a.Anchor.StartLine, a.Anchor.EndLine, string(a.Anchor.Side)
	}
	for _, n := range a.Normalized {
		if len(n) > maxReviewNote {
			n = cutRunes(n, maxReviewNote-len("…")) + "…"
		}
		f.Normalized = append(f.Normalized, n)
	}
	return f
}

// finding scrubs secrets from a finding's text after anchoring, which needs the code as
// the file holds it, and before the store hashes the quoted code. A marker can be longer
// than the secret it replaces, so a field is cut back to the store's bound.
func (ir *ingestRedaction) finding(f *govstore.ReviewFindingInput) {
	quoted := f.ExistingCode
	for _, field := range [...]struct {
		v   *string
		max int
	}{{&f.Content, govstore.MaxFindingContent}, {&f.ExistingCode, govstore.MaxFindingCode}, {&f.SuggestionCode, govstore.MaxFindingCode}} {
		ir.scrub(field.v)
		if len(*field.v) > field.max {
			*field.v = cutRunes(*field.v, field.max)
		}
	}
	f.CodeRedacted = f.ExistingCode != quoted
}

// hop is a finding's move from the commit its lines count in to the one they count in
// for the new change.
type hop struct{ from, to string }

// reanchorLineage computes the moves that bring the lineage's earlier findings to the
// change, so the new findings are compared with lines of the same commit: the head for
// the new side, the merge base for the old. A finding whose file did not change between
// its commit and the target keeps its lines; the others are searched for in the file at
// the target (reanchorAt). Duplicates and outdated or unanchored findings do not move.
// One name-only diff runs per (from, to) pair and one batch reader per target; a commit
// git cannot diff (rewritten away) leaves its findings unresolved, with the error in the
// report.
func reanchorLineage(ctx context.Context, dataDir, workspaceID, lineage string, change govstore.ReviewChange) ([]govstore.ReviewReanchor, *ReviewReanchorReport, error) {
	hops := map[hop][]govstore.ReviewFinding{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		for after := int64(0); ; {
			page, err := r.ListReviewFindings(ctx, govstore.ReviewFindingFilter{WorkspaceID: workspaceID, Lineage: lineage,
				AfterSeq: after, Limit: storeListPage})
			if err != nil || len(page) == 0 {
				return err
			}
			for _, f := range page {
				h := hop{f.Commit, change.LinesAt(f.Side)}
				if h.from != h.to && f.StartLine > 0 && f.Status != govstore.FindingDuplicate && f.OutdatedAt == "" {
					hops[h] = append(hops[h], f)
				}
			}
			after = page[len(page)-1].Seq
		}
	})
	if err != nil {
		return nil, nil, err
	}
	report := &ReviewReanchorReport{}
	var moves []govstore.ReviewReanchor
	search := map[string][]govstore.ReviewFinding{}
	for _, h := range slices.SortedFunc(maps.Keys(hops), func(a, b hop) int { return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.to, b.to)) }) {
		files, err := reviewChangedFiles(ctx, change.Repository, h.from, h.to)
		if err != nil {
			report.Unresolved += len(hops[h])
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		changed := make(map[string]bool, len(files))
		for _, p := range files {
			changed[p] = true
		}
		for _, f := range hops[h] {
			if changed[f.Path] {
				search[h.to] = append(search[h.to], f)
				continue
			}
			moves = append(moves, govstore.ReviewReanchor{FindingID: f.ID, Commit: h.to, StartLine: f.StartLine, EndLine: f.EndLine,
				AnchorStatus: f.AnchorStatus})
			report.Kept++
		}
	}
	for _, commit := range slices.Sorted(maps.Keys(search)) {
		found, err := reanchorAt(ctx, change.Repository, commit, search[commit])
		if err != nil {
			return nil, nil, err
		}
		for _, m := range found {
			if m.Outdated {
				report.Outdated++
			} else {
				report.Moved++
			}
		}
		report.Unresolved += len(search[commit]) - len(found)
		moves = append(moves, found...)
	}
	return moves, report, nil
}

// reanchorAt looks for each finding's quoted code in its file at commit, read through
// one `git cat-file --batch` under the anchoring's head bounds (anchor.Reanchor, which
// keeps a finding whose code is still at its line when the code occurs more than once).
// One match moves the finding; none, or a file commit does not hold, makes it outdated.
// It returns a move for each finding it can decide: several matches, a file over the
// bounds, and code it cannot search for (none quoted, or a secret redacted from it) are
// left undecided.
func reanchorAt(ctx context.Context, root, commit string, fs []govstore.ReviewFinding) ([]govstore.ReviewReanchor, error) {
	blobs := &headBlobs{ctx: ctx, root: root, head: commit, left: headTotalLimit, linesLeft: headLinesLimit}
	defer blobs.close()
	type version struct {
		content string
		read    bool
	}
	versions := map[string]version{}
	var out []govstore.ReviewReanchor
	for _, f := range fs {
		snippet, err := anchor.NewSnippet(f.ExistingCode)
		if err != nil || f.CodeRedacted {
			continue
		}
		v, ok := versions[f.Path]
		if !ok {
			v.content, v.read = blobs.read(f.Path)
			versions[f.Path] = v
		}
		if !v.read {
			continue
		}
		a := anchor.Reanchor(anchor.Anchor{Path: f.Path, StartLine: f.StartLine, EndLine: f.EndLine}, v.content, snippet)
		switch {
		case a.Status != anchor.Unanchored:
			out = append(out, govstore.ReviewReanchor{FindingID: f.ID, Commit: commit, StartLine: a.StartLine, EndLine: a.EndLine,
				AnchorStatus: string(a.Status)})
		case a.Reason == anchor.ReasonNotFound:
			out = append(out, govstore.ReviewReanchor{FindingID: f.ID, Commit: commit, Outdated: true})
		}
	}
	if blobs.err != nil {
		return nil, blobs.err
	}
	return out, nil
}

// reviewChangedFiles lists the files that differ between two commits, with the options
// the attested diff pins (no renames, so a rename counts both paths; no relative paths;
// submodules included), NUL-separated so no path is quoted.
func reviewChangedFiles(ctx context.Context, root, from, to string) ([]string, error) {
	var out bytes.Buffer
	if err := reviewGit(ctx, root, &out, "diff", "--name-only", "-z", "--no-color", "--no-ext-diff", "--no-textconv",
		"--no-renames", "--no-relative", "--ignore-submodules=none", from, to, "--"); err != nil {
		return nil, fmt.Errorf("changed files %s..%s: %w", from, to, err)
	}
	var files []string
	for p := range strings.SplitSeq(strings.TrimSuffix(out.String(), "\x00"), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	if len(files) > govstore.MaxReviewChangedFiles {
		return nil, fmt.Errorf("%d changed files, at most %d per record: %w", len(files), govstore.MaxReviewChangedFiles, ErrInvalidInput)
	}
	return files, nil
}

// TriageReviewFinding records actor's verdict (confirm, dismiss, fixed or wont_fix) on a
// finding. The store refuses the author confirming or dismissing their own finding; under
// the owner-distinct policy those verdicts are refused as well when the finding was
// written under actor's owner (WS-19B).
func TriageReviewFinding(ctx context.Context, dataDir, workspaceID string, actor ContextActor, findingID, verdict, note string) (*govstore.ReviewFinding, error) {
	if err := validateSafeID("review finding", findingID); err != nil {
		return nil, err
	}
	var red ingestRedaction
	red.scrub(&note)
	ownerPolicy := !govstore.AuthorMayTriage(verdict) && principalDistinctness(dataDir) == DistinctOwner
	mine := cmp.Or(actor.Owner, actor.ID)
	var out govstore.ReviewFinding
	err := memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		f, err := tx.GetReviewFinding(ctx, workspaceID, findingID)
		if err != nil {
			return err
		}
		if ownerPolicy && !sameOwner(f.Author, actor.ID) && sameOwner(recordedOwner(dataDir, f.AuthorOwner, f.Author), mine) {
			return fmt.Errorf("%w: %s is owned by %s, like the author of finding %s", ErrSameOwner, actor.ID, mine, findingID)
		}
		out, err = tx.TriageReviewFinding(ctx, govstore.ReviewTriageInput{WorkspaceID: workspaceID, FindingID: findingID,
			Verdict: verdict, Note: note}, actor.storeActor(WorkspaceRepoScope(dataDir, workspaceID)))
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ShowReviewRecord returns a record with its findings as they stand.
func ShowReviewRecord(ctx context.Context, dataDir, workspaceID, recordID string) (*ReviewRecordResult, error) {
	if err := validateSafeID("review record", recordID); err != nil {
		return nil, err
	}
	out := &ReviewRecordResult{Label: ReviewRecordLabel}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		if out.Record, err = r.GetReviewRecord(ctx, workspaceID, recordID); err != nil {
			return err
		}
		out.Findings, err = r.ListReviewFindings(ctx, govstore.ReviewFindingFilter{WorkspaceID: workspaceID, RecordID: recordID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
