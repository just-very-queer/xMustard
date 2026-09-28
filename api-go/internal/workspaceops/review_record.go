//go:build review

package workspaceops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/redact"
)

// Review records and findings (WS-66; PAR-REV-06), built only with the review build tag.
// A record binds one review to the change merge approval digests (the merge base of a
// base ref and a head): its coverage over every file the change touches and its findings,
// which the store deduplicates within the record's lineage. Findings of the lineage
// recorded at an earlier head are first moved to this head, lazily, so their lines are
// comparable. A record is evidence for the human's merge decision; nothing here approves
// a change.

// ReviewEvidenceLabel is carried by every review result.
const ReviewEvidenceLabel = "evidence only: a review record informs the human's merge decision; " +
	"no review result approves a change"

// Review input bounds: a findings or coverage file, and the findings one file holds
// (requirements §7: at most 50 findings per call).
const (
	MaxReviewInputBytes   = 4 << 20
	MaxReviewFindingsCall = 50
	maxFindingContentRune = 2000
)

// ReviewFindingClaim is a finding as its producer states it. start_line and end_line are
// the producer's claim (open-code-review's comments carry them); placeFindings decides
// where the finding is.
type ReviewFindingClaim struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExistingCode   string `json:"existing_code"`
	SuggestionCode string `json:"suggestion_code"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
}

// ReviewSubmission is one review of the change from the merge base of BaseRef to
// HeadRef. Lineage defaults to "<base_ref>@<merge_base>", so a branch's later heads on
// the same merge base share one; Producer defaults to agent.
type ReviewSubmission struct {
	BaseRef  string
	HeadRef  string
	Lineage  string
	Producer string
	Findings []ReviewFindingClaim
	Coverage []govstore.ReviewCoverage
	// Normalized records what decoding changed, carried into the result.
	Normalized []string
}

// ReviewReanchorReport counts how the lineage's findings from earlier heads moved to
// this head: kept (their file did not change, so their lines stand), moved or outdated
// (by a locator), and unresolved (their file changed and no locator is plugged in; they
// stay at their head, out of positional dedupe).
type ReviewReanchorReport struct {
	Kept       int      `json:"kept"`
	Moved      int      `json:"moved"`
	Outdated   int      `json:"outdated"`
	Unresolved int      `json:"unresolved"`
	Errors     []string `json:"errors,omitempty"`
}

// ReviewRecordResult is a record with its findings.
type ReviewRecordResult struct {
	Record     govstore.ReviewRecord    `json:"record"`
	Findings   []govstore.ReviewFinding `json:"findings"`
	Reanchored *ReviewReanchorReport    `json:"reanchored,omitempty"`
	Normalized []string                 `json:"normalized,omitempty"`
	Redactions *redact.Report           `json:"redactions,omitempty"`
	Label      string                   `json:"label"`
}

// placeFindings anchors a submission's findings in the change. WS-65's anchoring (its
// review.Anchor over the diff this record digests) plugs in here; until then a finding
// keeps its producer's line range, labeled claimed, or is unanchored when it names none.
var placeFindings = claimedPlacement

// relocateFindings finds, at head, findings whose file changed since the head they were
// recorded at, returning a move or an outdated mark for each one it can decide. WS-65's
// anchor.Reanchor over a `git cat-file --batch` read of head plugs in here; while it is
// nil such findings stay unresolved.
var relocateFindings func(ctx context.Context, root, head string, fs []govstore.ReviewFinding) ([]govstore.ReviewReanchor, error)

// claimedPlacement keeps each finding where its producer put it: a range read as
// open-code-review reads one (a missing or equal bound is a single line), on the new side.
func claimedPlacement(_ context.Context, _ ReviewedChange, claims []ReviewFindingClaim) ([]govstore.ReviewFindingInput, error) {
	out := make([]govstore.ReviewFindingInput, 0, len(claims))
	for _, c := range claims {
		f := govstore.ReviewFindingInput{Path: c.Path, Category: c.Category, Severity: c.Severity, Content: c.Content,
			ExistingCode: c.ExistingCode, SuggestionCode: c.SuggestionCode, AnchorStatus: govstore.AnchorUnanchored}
		start, end := min(c.StartLine, c.EndLine), max(c.StartLine, c.EndLine)
		if start <= 0 {
			start = end
		}
		if start > 0 {
			f.StartLine, f.EndLine, f.Side, f.AnchorStatus = start, end, "new", govstore.AnchorClaimed
		}
		out = append(out, f)
	}
	return out, nil
}

// reviewEnum is a closed vocabulary with the value an unknown one becomes.
type reviewEnum struct {
	field, fallback string
	values          []string
}

var (
	reviewCategoryEnum = reviewEnum{"category", "other", []string{"bug", "security", "performance", "maintainability", "test", "style", "documentation", "other"}}
	reviewSeverityEnum = reviewEnum{"severity", "low", []string{"critical", "high", "medium", "low"}}
)

// normalize folds case and maps an unknown value to the fallback, noting the change.
func (e reviewEnum) normalize(i int, v string, notes *[]string) string {
	low := strings.ToLower(strings.TrimSpace(v))
	if slices.Contains(e.values, low) {
		return low
	}
	*notes = append(*notes, fmt.Sprintf("finding %d: %s %q recorded as %s", i, e.field, v, e.fallback))
	return e.fallback
}

// DecodeReviewFindings reads one findings file: a JSON array of findings or an object
// {"findings": [...]}, each finding under a closed schema. It refuses input over
// MaxReviewInputBytes or MaxReviewFindingsCall findings. It normalizes, and notes: an
// unknown category or severity (other, low), content over 2,000 characters (cut) and
// quoted or suggested code over the snippet bound (dropped).
func DecodeReviewFindings(data []byte) ([]ReviewFindingClaim, []string, error) {
	if len(data) > MaxReviewInputBytes {
		return nil, nil, fmt.Errorf("findings are over %d bytes: %w", MaxReviewInputBytes, ErrInvalidInput)
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var wrap struct {
			Findings json.RawMessage `json:"findings"`
		}
		if err := strictJSON(data, &wrap); err != nil {
			return nil, nil, err
		}
		data = wrap.Findings
	}
	var claims []ReviewFindingClaim
	if err := strictJSON(data, &claims); err != nil {
		return nil, nil, err
	}
	if len(claims) > MaxReviewFindingsCall {
		return nil, nil, fmt.Errorf("%d findings, at most %d per call: %w", len(claims), MaxReviewFindingsCall, ErrInvalidInput)
	}
	var notes []string
	for i := range claims {
		c := &claims[i]
		c.Path = strings.TrimPrefix(strings.TrimSpace(c.Path), "./")
		c.Category = reviewCategoryEnum.normalize(i, c.Category, &notes)
		c.Severity = reviewSeverityEnum.normalize(i, c.Severity, &notes)
		if utf8.RuneCountInString(c.Content) > maxFindingContentRune {
			c.Content = string([]rune(c.Content)[:maxFindingContentRune])
			notes = append(notes, fmt.Sprintf("finding %d: content cut to %d characters", i, maxFindingContentRune))
		}
		for _, code := range []*string{&c.ExistingCode, &c.SuggestionCode} {
			if len(*code) > reviewSnippetBytes {
				*code = ""
				notes = append(notes, fmt.Sprintf("finding %d: code over %d bytes dropped", i, reviewSnippetBytes))
			}
		}
	}
	return claims, notes, nil
}

// reviewSnippetBytes is the bound on quoted and suggested code (WS-65's snippet bound).
const reviewSnippetBytes = 4 << 10

// DecodeReviewCoverage reads a coverage file: a JSON array of {path, status, reason}.
func DecodeReviewCoverage(data []byte) ([]govstore.ReviewCoverage, error) {
	if len(data) > MaxReviewInputBytes {
		return nil, fmt.Errorf("coverage is over %d bytes: %w", MaxReviewInputBytes, ErrInvalidInput)
	}
	var out []govstore.ReviewCoverage
	if err := strictJSON(data, &out); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Path = strings.TrimPrefix(strings.TrimSpace(out[i].Path), "./")
	}
	return out, nil
}

// strictJSON decodes one JSON value with no unknown members and nothing after it.
func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode: %v: %w", err, ErrInvalidInput)
	}
	if dec.More() {
		return fmt.Errorf("decode: trailing data: %w", ErrInvalidInput)
	}
	return nil
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
// to sub.HeadRef. The coverage denominator is every file that change touches, computed
// here from the same commits; the lineage's findings from earlier heads are moved to the
// head first (reanchorLineage), then the store records and deduplicates.
func RecordReview(ctx context.Context, dataDir, workspaceID string, actor ContextActor, sub ReviewSubmission) (*ReviewRecordResult, error) {
	change, err := DiffReviewedChange(ctx, dataDir, workspaceID, sub.BaseRef, sub.HeadRef)
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
	findings, err := placeFindings(ctx, change, sub.Findings)
	if err != nil {
		return nil, err
	}
	in := govstore.ReviewRecordInput{WorkspaceID: workspaceID, Lineage: fallbackString(sub.Lineage, change.BaseRef+"@"+change.MergeBase),
		Change: govstore.ReviewChange{Repository: change.Repository, BaseRef: change.BaseRef, MergeBase: change.MergeBase,
			Head: change.Head, DiffSHA256: change.DiffSHA256, DiffBytes: change.DiffBytes},
		ChangedFiles: files, Coverage: sub.Coverage, Producer: fallbackString(sub.Producer, "agent"), Findings: findings}
	// Secrets are redacted after anchoring, which needs the code as the file holds it,
	// and before the store hashes the quoted code.
	var red ingestRedaction
	for i := range in.Findings {
		red.scrub(&in.Findings[i].Content, &in.Findings[i].ExistingCode, &in.Findings[i].SuggestionCode)
	}
	moves, report, err := reanchorLineage(ctx, dataDir, workspaceID, in.Lineage, change)
	if err != nil {
		return nil, err
	}
	out := &ReviewRecordResult{Reanchored: report, Normalized: sub.Normalized, Label: ReviewEvidenceLabel}
	if red.rep.Redacted {
		out.Redactions = &red.rep
	}
	sa := actor.storeActor(change.Repository)
	err = memoryUpdate(ctx, dataDir, workspaceID, func(tx govstore.Tx) error {
		if _, err := tx.ReanchorReviewFindings(ctx, workspaceID, change.Head, moves, sa); err != nil {
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

// reanchorLineage computes the moves that bring the lineage's findings recorded at
// earlier heads to change.Head. A finding whose file did not change between its head and
// this one keeps its lines; the others go to relocateFindings when one is plugged in,
// and are unresolved otherwise. Duplicates and outdated findings do not move. One diff
// runs per earlier head; a head git cannot diff (rewritten away) leaves its findings
// unresolved, with the error in the report.
func reanchorLineage(ctx context.Context, dataDir, workspaceID, lineage string, change ReviewedChange) ([]govstore.ReviewReanchor, *ReviewReanchorReport, error) {
	byHead := map[string][]govstore.ReviewFinding{}
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		for after := int64(0); ; {
			page, err := r.ListReviewFindings(ctx, govstore.ReviewFindingFilter{WorkspaceID: workspaceID, Lineage: lineage,
				AfterSeq: after, Limit: storeListPage})
			if err != nil || len(page) == 0 {
				return err
			}
			for _, f := range page {
				if f.Head != change.Head && f.Status != govstore.FindingDuplicate && f.OutdatedAt == "" {
					byHead[f.Head] = append(byHead[f.Head], f)
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
	var pending []govstore.ReviewFinding
	for _, head := range slices.Sorted(maps.Keys(byHead)) {
		changed, err := reviewChangedFiles(ctx, change.Repository, head, change.Head)
		if err != nil {
			report.Unresolved += len(byHead[head])
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		for _, f := range byHead[head] {
			if slices.Contains(changed, f.Path) {
				pending = append(pending, f)
				continue
			}
			moves = append(moves, govstore.ReviewReanchor{FindingID: f.ID, StartLine: f.StartLine, EndLine: f.EndLine, AnchorStatus: f.AnchorStatus})
			report.Kept++
		}
	}
	if relocateFindings == nil || len(pending) == 0 {
		report.Unresolved += len(pending)
		return moves, report, nil
	}
	located, err := relocateFindings(ctx, change.Repository, change.Head, pending)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range located {
		if m.Outdated {
			report.Outdated++
		} else {
			report.Moved++
		}
	}
	report.Unresolved += len(pending) - len(located)
	return append(moves, located...), report, nil
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
	mine := fallbackString(actor.Owner, actor.ID)
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
	out := &ReviewRecordResult{Label: ReviewEvidenceLabel}
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
