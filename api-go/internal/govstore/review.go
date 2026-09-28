package govstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Review findings on the shared tables (WS-66; PAR-REV-06, as the critic shrank it in
// requirements §13.6). A review record and each finding it holds are subjects, as a
// memory is, and no table is review-only:
//   - events hold what a record binds (the change, its coverage, its author) and what a
//     finding says, plus every triage verdict and re-anchoring, append-only;
//   - anchors hold a finding's place in the code: its path and line range, the head the
//     lines count in, and the SHA-256 of its normalized quoted code as the baseline;
//   - outcomes hold each principal's latest triage verdict on a finding;
//   - jobs hold a possible duplicate for someone to decide.
//
// subject_kind (migration 3) says which kind of subject a row's entry_id names.
//
// A finding's identity is its id. A new finding is a duplicate of an earlier one only
// on open-code-review's exact rule plus the quoted code: the same path and side, line
// ranges counted at the same head whose intersection over union is strictly above 0.6
// (a single-line range never matches a multi-line one), and the same normalized quoted
// code. A positional overlap alone makes it a possible duplicate: it is kept as its own
// finding and a review_duplicate job asks someone to decide. Neither is corroboration,
// which only an explicit confirm by a principal other than the author gives. Nothing
// here approves a change: a record is evidence that informs the human's merge decision.

// Subject kinds: what a row's entry_id names.
const (
	SubjectMemory        = "memory"
	SubjectReviewRecord  = "review_record"
	SubjectReviewFinding = "review_finding"
)

// AnchorReviewRange is a finding's anchor kind: its value is the file path and the row
// carries the line range. It is not a memory anchor kind.
const AnchorReviewRange = "review_range"

// Finding statuses. A finding is open until someone triages it; a duplicate stays one;
// an open finding whose quoted code is gone at a later head is outdated, never deleted.
const (
	FindingOpen      = "open"
	FindingConfirmed = "confirmed"
	FindingDismissed = "dismissed"
	FindingFixed     = "fixed"
	FindingWontFix   = "wont_fix"
	FindingOutdated  = "outdated"
	FindingDuplicate = "duplicate"
)

// Triage verdicts (the outcomes a principal records on a finding) and the status each
// one sets. A finding's author may mark it fixed or wont_fix, but never confirm or
// dismiss it.
var triageVerdicts = map[string]struct {
	status    string
	authorMay bool
}{
	"confirm":  {FindingConfirmed, false},
	"dismiss":  {FindingDismissed, false},
	"fixed":    {FindingFixed, true},
	"wont_fix": {FindingWontFix, true},
}

// Dedupe results of a new finding.
const (
	DedupeCreated           = "created"
	DedupeDuplicateOf       = "duplicate_of"
	DedupePossibleDuplicate = "possible_duplicate"
)

// dedupeIoU is open-code-review's overlap threshold; an IoU must exceed it.
const dedupeIoU = 0.6

// Coverage: every changed file is reviewed or not_reviewed with a reason. A changed file
// the reviewer did not report is not_reviewed with ReasonNotReported.
const (
	CoverageReviewed    = "reviewed"
	CoverageNotReviewed = "not_reviewed"
	ReasonNotReported   = "not_reported"
)

// Terminal states, derived from coverage and never taken from the reviewer's claim.
const (
	ReviewComplete = "complete"
	ReviewPartial  = "partial"
	ReviewSkipped  = "skipped"
)

// Anchor statuses: WS-65's resolver's, plus claimed for a producer's own line range that
// no resolver checked.
const (
	AnchorClaimed    = "claimed"
	AnchorUnanchored = "unanchored"
)

var (
	reviewCategories     = set("bug", "security", "performance", "maintainability", "test", "style", "documentation", "other")
	reviewSeverities     = set("critical", "high", "medium", "low")
	reviewProducers      = set("agent", "ocr", "human")
	reviewSides          = set("new", "old")
	reviewAnchorStatuses = set("exact_new", "exact_old", "file", "relocated", AnchorClaimed, AnchorUnanchored)
)

// Bounds (requirements §7 quotas: at most 500 findings per record).
const (
	MaxReviewFindings     = 500
	MaxReviewChangedFiles = 20000
	maxFindingContent     = 8 << 10 // 2,000 characters of up to four bytes
	maxFindingCode        = 4 << 10 // WS-65's snippet bound
	maxReviewLabel        = 256     // lineage, a coverage reason
)

// ErrSelfTriage: a finding's author may not confirm or dismiss it.
var ErrSelfTriage = errors.New("govstore: a finding's author cannot confirm or dismiss it")

// ReviewChange is the change a record binds to: the repository, the merge base of the
// base ref and the head, and the SHA-256 of the diff between them (WS-57's digest).
type ReviewChange struct {
	Repository string `json:"repository"`
	BaseRef    string `json:"base_ref"`
	MergeBase  string `json:"merge_base"`
	Head       string `json:"head"`
	DiffSHA256 string `json:"diff_sha256"`
	DiffBytes  int64  `json:"diff_bytes"`
}

// ReviewCoverage is one changed file's coverage.
type ReviewCoverage struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// ReviewFindingInput is one finding, already anchored: Path, the line range and Side
// say where it is at the record's head and AnchorStatus how it was placed (0/0 when
// unanchored). Category and Severity are the closed sets; a surface normalizes unknown
// values before the store sees them.
type ReviewFindingInput struct {
	Path           string `json:"path"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	Side           string `json:"side,omitempty"`
	AnchorStatus   string `json:"anchor_status"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	Content        string `json:"content"`
	ExistingCode   string `json:"existing_code,omitempty"`
	SuggestionCode string `json:"suggestion_code,omitempty"`
}

// ReviewRecordInput records one review of one change. ChangedFiles is every file the
// change touches: the coverage denominator, which the caller computes from the diff
// and the reviewer cannot shrink. Coverage is the reviewer's report; a changed file it
// leaves out is not_reviewed(not_reported), and a path outside the change is refused.
// Lineage groups the records of one change as it moves from head to head; findings are
// deduplicated within it.
type ReviewRecordInput struct {
	WorkspaceID  string
	Lineage      string
	Change       ReviewChange
	ChangedFiles []string
	Coverage     []ReviewCoverage
	Producer     string
	Findings     []ReviewFindingInput
}

// ReviewRecord is a stored record. Author, AuthorOwner and AuthorKind are the writing
// principal as the store recorded it.
type ReviewRecord struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Author      string `json:"author"`
	AuthorOwner string `json:"author_owner,omitempty"`
	AuthorKind  string `json:"author_kind,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Seq         int64  `json:"seq"`
	At          string `json:"at"`
	reviewRecordData
}

type reviewRecordData struct {
	Lineage       string           `json:"lineage"`
	Change        ReviewChange     `json:"change"`
	Producer      string           `json:"producer"`
	Coverage      []ReviewCoverage `json:"coverage"`
	ChangedFiles  int              `json:"changed_files"`
	Reviewed      int              `json:"reviewed"`
	NotReviewed   int              `json:"not_reviewed"`
	CoverageRate  float64          `json:"coverage_rate"`
	TerminalState string           `json:"terminal_state"`
	FindingIDs    []string         `json:"findings"`
}

// ReviewDedupe is how a finding compared with the lineage's earlier findings when it
// was recorded. JobID names the review_duplicate job a possible duplicate opened.
type ReviewDedupe struct {
	Result string  `json:"result"`
	Of     string  `json:"of,omitempty"`
	IoU    float64 `json:"iou,omitempty"`
	JobID  string  `json:"job_id,omitempty"`
}

// ReviewFinding is a stored finding with its current anchor (Head is the commit its
// lines count in), status and corroborations: the principals other than its author and
// the open-mode identity whose verdict is confirm.
type ReviewFinding struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	reviewFindingData
	Head           string `json:"head"`
	Status         string `json:"status"`
	Corroborations int    `json:"corroborations"`
	OutdatedAt     string `json:"outdated_at,omitempty"`
	Author         string `json:"author"`
	AuthorOwner    string `json:"author_owner,omitempty"`
	Seq            int64  `json:"seq"`
	At             string `json:"at"`
}

type reviewFindingData struct {
	RecordID string `json:"record_id"`
	Lineage  string `json:"lineage"`
	ReviewFindingInput
	// CodeSHA256 digests the normalized quoted code ("" when there is none).
	CodeSHA256 string `json:"code_sha256,omitempty"`
	// Fingerprint is sha256(path|category|normalized code), as SARIF partialFingerprints
	// take it: a dedupe hint for exports, never the finding's identity.
	Fingerprint string       `json:"fingerprint"`
	Dedupe      ReviewDedupe `json:"dedupe"`
	AuthorKey   string       `json:"author_key"`
}

// ReviewFindingFilter lists a workspace's findings: those of one record, or of one
// lineage and path. Statuses keeps only those statuses; AfterSeq pages by creation.
type ReviewFindingFilter struct {
	WorkspaceID string
	RecordID    string
	Lineage     string
	Path        string
	Statuses    []string
	AfterSeq    int64
	Limit       int
}

// ReviewTriageInput is one principal's verdict on a finding: confirm, dismiss, fixed or
// wont_fix.
type ReviewTriageInput struct {
	WorkspaceID string
	FindingID   string
	Verdict     string
	Note        string
}

// ReviewReanchor moves a finding to a later head: to StartLine..EndLine (placed as
// AnchorStatus), or, when Outdated, records that its quoted code is gone there.
type ReviewReanchor struct {
	FindingID    string
	Outdated     bool
	StartLine    int
	EndLine      int
	AnchorStatus string
}

// ReviewReader reads review records and findings.
type ReviewReader interface {
	GetReviewRecord(ctx context.Context, workspaceID, id string) (ReviewRecord, error)
	GetReviewFinding(ctx context.Context, workspaceID, id string) (ReviewFinding, error)
	ListReviewFindings(ctx context.Context, f ReviewFindingFilter) ([]ReviewFinding, error)
}

// ReviewWriter records reviews, triage verdicts and re-anchorings.
type ReviewWriter interface {
	// RecordReview stores a record and its findings, deduplicating each finding against
	// the lineage's earlier findings anchored at the same head (and the ones before it
	// in the same call).
	RecordReview(ctx context.Context, in ReviewRecordInput, actor Actor) (ReviewRecord, []ReviewFinding, error)
	// TriageReviewFinding records actor's verdict on a finding and returns it updated.
	TriageReviewFinding(ctx context.Context, in ReviewTriageInput, actor Actor) (ReviewFinding, error)
	// ReanchorReviewFindings moves findings to head, or marks them outdated there. It
	// returns how many it changed; a finding already at head is left alone.
	ReanchorReviewFindings(ctx context.Context, workspaceID, head string, moves []ReviewReanchor, actor Actor) (int, error)
}

// AuthorMayTriage reports whether a finding's author may cast verdict on it: fixed and
// wont_fix yes, confirm and dismiss (the verdicts that judge it) never. An unknown
// verdict is false.
func AuthorMayTriage(verdict string) bool {
	rule, ok := triageVerdicts[verdict]
	return ok && rule.authorMay
}

// NormalizedCode is quoted code as the dedupe hash reads it: each line trimmed and
// stripped of one leading + and one leading - diff marker, blank lines dropped (WS-65's
// snippet normalization).
func NormalizedCode(code string) string {
	var lines []string
	for l := range strings.SplitSeq(code, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(l), "+"), "-"))
		if l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// SpanIoU is open-code-review's comparison of two line ranges of one file (its
// post-review-comments.js sameCommentSpan): single-line ranges are the same only on the
// same line, a single-line range never matches a multi-line one, and multi-line ranges
// score their intersection over union. A range starting at 0 is unanchored and matches
// nothing. Callers count a match only strictly above dedupeIoU.
func SpanIoU(a, b [2]int) float64 {
	multiA, multiB := a[0] != a[1], b[0] != b[1]
	switch {
	case a[0] <= 0 || b[0] <= 0, multiA != multiB:
		return 0
	case !multiA && a[0] == b[0]:
		return 1
	case !multiA:
		return 0
	}
	overlap := min(a[1], b[1]) - max(a[0], b[0]) + 1
	if overlap <= 0 {
		return 0
	}
	return float64(overlap) / float64(a[1]-a[0]+1+b[1]-b[0]+1-overlap)
}

// eventObject renders v as the JSON object an event records, so the store adds the
// write's provenance beside its fields rather than nesting them under "data".
func eventObject(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(b, &out)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// validReviewPath refuses a path that is empty, absolute, not clean, escaping or too long.
func validReviewPath(kind, p string) error {
	if p == "" || len(p) > maxPathLen || strings.ContainsRune(p, 0) || path.IsAbs(p) || path.Clean(p) != p ||
		p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("%w: %s path %q", ErrInvalid, kind, p)
	}
	return nil
}

func validReviewLabel(kind, s string, required bool) error {
	if (required && strings.TrimSpace(s) == "") || len(s) > maxReviewLabel || strings.ContainsRune(s, 0) {
		return fmt.Errorf("%w: %s %q", ErrInvalid, kind, s)
	}
	return nil
}

func (f ReviewFindingInput) validate() error {
	if err := validReviewPath("finding", f.Path); err != nil {
		return err
	}
	for _, c := range [...]struct {
		field, v string
		ok       bool
	}{
		{"category", f.Category, reviewCategories[f.Category]},
		{"severity", f.Severity, reviewSeverities[f.Severity]},
		{"anchor_status", f.AnchorStatus, reviewAnchorStatuses[f.AnchorStatus]},
	} {
		if !c.ok {
			return fmt.Errorf("%w: finding %s %q", ErrInvalid, c.field, c.v)
		}
	}
	for _, b := range [...]struct {
		field string
		n     int
		max   int
	}{
		{"content", len(f.Content), maxFindingContent},
		{"existing_code", len(f.ExistingCode), maxFindingCode},
		{"suggestion_code", len(f.SuggestionCode), maxFindingCode},
	} {
		if b.n > b.max {
			return fmt.Errorf("%w: finding %s is over %d bytes", ErrInvalid, b.field, b.max)
		}
	}
	if strings.TrimSpace(f.Content) == "" {
		return fmt.Errorf("%w: a finding needs content", ErrInvalid)
	}
	// An anchored finding has a side and a range; an unanchored one has neither.
	placed := f.AnchorStatus != AnchorUnanchored
	if placed != (f.StartLine > 0) || f.EndLine < f.StartLine || placed != reviewSides[f.Side] {
		return fmt.Errorf("%w: finding on %s: lines %d-%d, side %q do not fit anchor_status %s", ErrInvalid, f.Path,
			f.StartLine, f.EndLine, f.Side, f.AnchorStatus)
	}
	return nil
}

// reviewCoverage builds the manifest: one entry per changed file, in the order given,
// with the reviewer's report or not_reviewed(not_reported). It refuses a report on a
// path outside the change, a report twice on one path, and a not_reviewed without a
// reason.
func reviewCoverage(changed []string, reports []ReviewCoverage) ([]ReviewCoverage, error) {
	if len(changed) == 0 || len(changed) > MaxReviewChangedFiles {
		return nil, fmt.Errorf("%w: a record covers 1 to %d changed files, not %d", ErrInvalid, MaxReviewChangedFiles, len(changed))
	}
	byPath := make(map[string]ReviewCoverage, len(reports))
	for _, c := range reports {
		reason := strings.TrimSpace(c.Reason)
		switch _, dup := byPath[c.Path]; {
		case c.Status != CoverageReviewed && c.Status != CoverageNotReviewed:
			return nil, fmt.Errorf("%w: coverage status %q", ErrInvalid, c.Status)
		case dup:
			return nil, fmt.Errorf("%w: coverage reports %s twice", ErrInvalid, c.Path)
		case c.Status == CoverageNotReviewed && reason == "":
			return nil, fmt.Errorf("%w: %s is not_reviewed without a reason", ErrInvalid, c.Path)
		}
		if err := validReviewLabel("coverage reason", reason, false); err != nil {
			return nil, err
		}
		byPath[c.Path] = ReviewCoverage{Path: c.Path, Status: c.Status, Reason: reason}
	}
	out := make([]ReviewCoverage, 0, len(changed))
	inChange := make(map[string]bool, len(changed))
	for _, p := range changed {
		if err := validReviewPath("changed file", p); err != nil {
			return nil, err
		}
		if inChange[p] {
			return nil, fmt.Errorf("%w: changed file %s listed twice", ErrInvalid, p)
		}
		inChange[p] = true
		c, ok := byPath[p]
		if !ok {
			c = ReviewCoverage{Path: p, Status: CoverageNotReviewed, Reason: ReasonNotReported}
		}
		out = append(out, c)
	}
	for _, c := range reports {
		if !inChange[c.Path] {
			return nil, fmt.Errorf("%w: coverage reports %s, which the change does not touch", ErrInvalid, c.Path)
		}
	}
	return out, nil
}

// withCounts derives the counts, the rate and the terminal state from the coverage.
func (d *reviewRecordData) withCounts() {
	d.ChangedFiles, d.Reviewed = len(d.Coverage), 0
	for _, c := range d.Coverage {
		if c.Status == CoverageReviewed {
			d.Reviewed++
		}
	}
	d.NotReviewed = d.ChangedFiles - d.Reviewed
	d.CoverageRate = float64(d.Reviewed) / float64(d.ChangedFiles)
	switch d.Reviewed {
	case d.ChangedFiles:
		d.TerminalState = ReviewComplete
	case 0:
		d.TerminalState = ReviewSkipped
	default:
		d.TerminalState = ReviewPartial
	}
}

// RecordReview stores a record and its findings (see ReviewWriter).
func (t *txn) RecordReview(ctx context.Context, in ReviewRecordInput, actor Actor) (ReviewRecord, []ReviewFinding, error) {
	if err := actor.validate(); err != nil {
		return ReviewRecord{}, nil, err
	}
	data, err := validateReviewRecord(in)
	if err != nil {
		return ReviewRecord{}, nil, err
	}
	recordID := newID("rvr_")
	data.FindingIDs = make([]string, len(in.Findings))
	for i := range in.Findings {
		data.FindingIDs[i] = newID("rvf_")
	}
	obj, err := eventObject(data)
	if err != nil {
		return ReviewRecord{}, nil, err
	}
	if _, err := t.insertEvent(ctx, actor, eventRow{WorkspaceID: in.WorkspaceID, EntryID: recordID,
		SubjectKind: SubjectReviewRecord, Type: EventReviewRecord, NewDigest: in.Change.DiffSHA256, Data: obj}); err != nil {
		return ReviewRecord{}, nil, err
	}
	for i, f := range in.Findings {
		if err := t.insertFinding(ctx, in, recordID, data.FindingIDs[i], f, actor); err != nil {
			return ReviewRecord{}, nil, err
		}
	}
	rec, err := t.GetReviewRecord(ctx, in.WorkspaceID, recordID)
	if err != nil {
		return ReviewRecord{}, nil, err
	}
	findings, err := t.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: in.WorkspaceID, RecordID: recordID})
	return rec, findings, err
}

func validateReviewRecord(in ReviewRecordInput) (*reviewRecordData, error) {
	if err := validID("workspace", in.WorkspaceID); err != nil {
		return nil, err
	}
	if err := validReviewLabel("lineage", in.Lineage, true); err != nil {
		return nil, err
	}
	c := in.Change
	for _, f := range [...]struct{ field, v string }{
		{"repository", c.Repository}, {"base_ref", c.BaseRef}, {"merge_base", c.MergeBase}, {"head", c.Head}, {"diff_sha256", c.DiffSHA256},
	} {
		if err := validReviewLabel("change "+f.field, f.v, true); err != nil {
			return nil, err
		}
	}
	if !reviewProducers[in.Producer] {
		return nil, fmt.Errorf("%w: producer %q", ErrInvalid, in.Producer)
	}
	if len(in.Findings) > MaxReviewFindings {
		return nil, fmt.Errorf("%w: a record holds at most %d findings", ErrInvalid, MaxReviewFindings)
	}
	for _, f := range in.Findings {
		if err := f.validate(); err != nil {
			return nil, err
		}
	}
	coverage, err := reviewCoverage(in.ChangedFiles, in.Coverage)
	if err != nil {
		return nil, err
	}
	d := &reviewRecordData{Lineage: in.Lineage, Change: c, Producer: in.Producer, Coverage: coverage}
	d.withCounts()
	return d, nil
}

// insertFinding deduplicates f against the lineage and stores its creation event, its
// anchor and, for a possible duplicate, the job that asks about it.
func (t *txn) insertFinding(ctx context.Context, in ReviewRecordInput, recordID, id string, f ReviewFindingInput, actor Actor) error {
	code := NormalizedCode(f.ExistingCode)
	d := reviewFindingData{RecordID: recordID, Lineage: in.Lineage, ReviewFindingInput: f,
		Fingerprint: sha256Hex(f.Path + "|" + f.Category + "|" + code), AuthorKey: principalKey(actor.Principal)}
	if code != "" {
		d.CodeSHA256 = sha256Hex(code)
	}
	var err error
	if d.Dedupe, err = t.dedupeFinding(ctx, in.WorkspaceID, in.Lineage, in.Change.Head, f, d.CodeSHA256); err != nil {
		return err
	}
	if d.Dedupe.Result == DedupePossibleDuplicate {
		job, _, err := t.EnqueueJob(ctx, JobInput{WorkspaceID: in.WorkspaceID, Kind: JobReviewDuplicate,
			ConflictSignature: "review_duplicate:" + d.Dedupe.Of + ":" + id, CandidateIDs: []string{d.Dedupe.Of, id},
			Instructions: "These findings overlap in place but quote different code. Decide whether they describe one " +
				"defect; triage each with its own verdict. An overlap is never corroboration.",
			Payload: map[string]any{"path": f.Path, "iou": d.Dedupe.IoU, "lineage": in.Lineage}}, actor)
		if err != nil {
			return err
		}
		d.Dedupe.JobID = job.ID
	}
	obj, err := eventObject(d)
	if err != nil {
		return err
	}
	if _, err := t.insertEvent(ctx, actor, eventRow{WorkspaceID: in.WorkspaceID, EntryID: id, SubjectKind: SubjectReviewFinding,
		Type: EventReviewFinding, NewDigest: d.CodeSHA256, Data: obj}); err != nil {
		return err
	}
	baseline := BaselineNone
	if d.CodeSHA256 != "" {
		baseline = BaselineHash
	}
	_, err = t.exec(ctx, `INSERT INTO anchors (subject_kind, entry_id, ordinal, kind, value, declared, baseline_state,
		baseline_hash, baseline_kind, baseline_commit, baseline_at, start_line, end_line, side, anchor_status)
		VALUES (?, ?, 0, ?, ?, 1, ?, ?, 'existing_code', ?, ?, ?, ?, ?, ?)`,
		SubjectReviewFinding, id, AnchorReviewRange, f.Path, baseline, d.CodeSHA256, in.Change.Head, t.nowText(),
		f.StartLine, f.EndLine, f.Side, f.AnchorStatus)
	return err
}

// dedupeFinding compares f with the lineage's findings on its path and side whose lines
// count at head (earlier heads are incomparable until re-anchored), leaving out
// duplicates and outdated anchors. The best exact match (IoU above the threshold and
// the same quoted code) makes f a duplicate; otherwise the best positional match makes
// it a possible duplicate.
func (t *txn) dedupeFinding(ctx context.Context, ws, lineage, head string, f ReviewFindingInput, code string) (ReviewDedupe, error) {
	out := ReviewDedupe{Result: DedupeCreated}
	if f.StartLine == 0 {
		return out, nil
	}
	cands, err := t.ListReviewFindings(ctx, ReviewFindingFilter{WorkspaceID: ws, Lineage: lineage, Path: f.Path,
		Limit: maxListLimit})
	if err != nil {
		return out, err
	}
	var exact, near ReviewDedupe
	for _, g := range cands {
		if g.Status == FindingDuplicate || g.OutdatedAt != "" || g.Head != head || g.Side != f.Side {
			continue
		}
		iou := SpanIoU([2]int{f.StartLine, f.EndLine}, [2]int{g.StartLine, g.EndLine})
		if iou <= dedupeIoU {
			continue
		}
		if code != "" && g.CodeSHA256 == code && iou > exact.IoU {
			exact = ReviewDedupe{Result: DedupeDuplicateOf, Of: g.ID, IoU: iou}
		}
		if iou > near.IoU {
			near = ReviewDedupe{Result: DedupePossibleDuplicate, Of: g.ID, IoU: iou}
		}
	}
	switch {
	case exact.Of != "":
		return exact, nil
	case near.Of != "":
		return near, nil
	}
	return out, nil
}

// TriageReviewFinding records actor's verdict on a finding (see ReviewWriter). Guards,
// in order: a known verdict, a finding of the workspace, not a duplicate (its original
// is triaged instead), and not the author confirming or dismissing their own finding.
func (t *txn) TriageReviewFinding(ctx context.Context, in ReviewTriageInput, actor Actor) (ReviewFinding, error) {
	if err := actor.validate(); err != nil {
		return ReviewFinding{}, err
	}
	rule, ok := triageVerdicts[in.Verdict]
	if !ok {
		return ReviewFinding{}, fmt.Errorf("%w: triage verdict %q", ErrInvalid, in.Verdict)
	}
	if len(in.Note) > maxNoteLen {
		return ReviewFinding{}, fmt.Errorf("%w: note longer than %d bytes", ErrInvalid, maxNoteLen)
	}
	f, err := t.GetReviewFinding(ctx, in.WorkspaceID, in.FindingID)
	if err != nil {
		return ReviewFinding{}, err
	}
	principal := strings.TrimSpace(actor.Principal)
	switch {
	case f.Status == FindingDuplicate:
		return ReviewFinding{}, fmt.Errorf("%w: %s duplicates %s; triage that finding", ErrInvalid, f.ID, f.Dedupe.Of)
	case !rule.authorMay && principalKey(principal) == f.AuthorKey:
		return ReviewFinding{}, fmt.Errorf("%w: %w: %s wrote %s", ErrInvalid, ErrSelfTriage, principal, f.ID)
	}
	if _, err := t.exec(ctx, `INSERT INTO outcomes (subject_kind, entry_id, revision, principal, principal_key, outcome,
		note, session_id, at) VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id, principal_key) DO UPDATE SET principal = excluded.principal, outcome = excluded.outcome,
		note = excluded.note, session_id = excluded.session_id, at = excluded.at`,
		SubjectReviewFinding, f.ID, principal, principalKey(principal), in.Verdict, in.Note, actor.SessionID, t.nowText()); err != nil {
		return ReviewFinding{}, err
	}
	if _, err := t.insertEvent(ctx, actor, eventRow{WorkspaceID: f.WorkspaceID, EntryID: f.ID, SubjectKind: SubjectReviewFinding,
		Type: EventReviewTriage, Note: in.Note,
		Data: map[string]any{"verdict": in.Verdict, "from": f.Status, "status": rule.status}}); err != nil {
		return ReviewFinding{}, err
	}
	return t.GetReviewFinding(ctx, f.WorkspaceID, f.ID)
}

// ReanchorReviewFindings moves findings to head (see ReviewWriter). Only a finding that
// is neither a duplicate nor already outdated moves; an outdated one keeps its last
// place and records where its code went missing.
func (t *txn) ReanchorReviewFindings(ctx context.Context, workspaceID, head string, moves []ReviewReanchor, actor Actor) (int, error) {
	if err := actor.validate(); err != nil {
		return 0, err
	}
	if err := validReviewLabel("head", head, true); err != nil {
		return 0, err
	}
	changed := 0
	for _, m := range moves {
		f, err := t.GetReviewFinding(ctx, workspaceID, m.FindingID)
		if err != nil {
			return changed, err
		}
		if f.Head == head || f.Status == FindingDuplicate || f.OutdatedAt != "" {
			continue
		}
		to := f.ReviewFindingInput
		to.StartLine, to.EndLine, to.AnchorStatus = m.StartLine, m.EndLine, m.AnchorStatus
		if !m.Outdated {
			if err := to.validate(); err != nil {
				return changed, err
			}
		}
		now := t.nowText()
		query, args := `UPDATE anchors SET stale_since = ?, stale_commit = ?`, []any{now, head}
		if !m.Outdated {
			query, args = `UPDATE anchors SET start_line = ?, end_line = ?, anchor_status = ?, baseline_commit = ?, baseline_at = ?`,
				[]any{to.StartLine, to.EndLine, to.AnchorStatus, head, now}
		}
		if _, err := t.exec(ctx, query+` WHERE subject_kind = ? AND entry_id = ? AND kind = ?`,
			append(args, SubjectReviewFinding, f.ID, AnchorReviewRange)...); err != nil {
			return changed, err
		}
		data := map[string]any{"from_head": f.Head, "to_head": head, "from": [2]int{f.StartLine, f.EndLine}, "outdated": m.Outdated}
		if !m.Outdated {
			data["to"], data["anchor_status"] = [2]int{to.StartLine, to.EndLine}, to.AnchorStatus
		}
		if _, err := t.insertEvent(ctx, actor, eventRow{WorkspaceID: workspaceID, EntryID: f.ID, SubjectKind: SubjectReviewFinding,
			Type: EventReviewReanchor, Data: data}); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// GetReviewRecord returns one record of the workspace.
func (r *reader) GetReviewRecord(ctx context.Context, workspaceID, id string) (ReviewRecord, error) {
	var rec ReviewRecord
	var data string
	err := r.queryRow(ctx, `SELECT entry_id, workspace_id, principal, session_id, seq, at, coalesce(data, '')
		FROM events WHERE entry_id = ? AND workspace_id = ? AND subject_kind = ? AND type = ?`,
		id, workspaceID, SubjectReviewRecord, EventReviewRecord).Scan(&rec.ID, &rec.WorkspaceID, &rec.Author, &rec.SessionID,
		&rec.Seq, &rec.At, &data)
	if errors.Is(err, ErrNotFound) {
		return rec, fmt.Errorf("review record %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return rec, err
	}
	var withProv struct {
		reviewRecordData
		Provenance struct {
			Owner string `json:"owner"`
			Kind  string `json:"kind"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal([]byte(data), &withProv); err != nil {
		return rec, fmt.Errorf("govstore: review record %s: %w", id, err)
	}
	rec.reviewRecordData, rec.AuthorOwner, rec.AuthorKind = withProv.reviewRecordData, withProv.Provenance.Owner, withProv.Provenance.Kind
	return rec, nil
}

// reviewFindingQuery reads findings with their current anchor. The anchors drive the
// join (CROSS JOIN fixes the order), so a record's ids or a path are index lookups
// whatever the workspace holds. The status is derived: a duplicate stays one, else the
// latest triage verdict decides, else an outdated anchor, else open.
const reviewFindingQuery = `SELECT id, workspace_id, principal, seq, at, data, head, outdated_at, start_line, end_line, side,
	anchor_status, corroborations, status FROM (
	SELECT ev.entry_id AS id, ev.workspace_id, ev.principal, ev.seq, ev.at, ev.data, a.baseline_commit AS head,
		coalesce(a.stale_since, '') AS outdated_at, a.start_line, a.end_line, a.side, a.anchor_status,
		(SELECT count(*) FROM outcomes o WHERE o.entry_id = ev.entry_id AND o.subject_kind = 'review_finding'
			AND o.outcome = 'confirm' AND o.principal_key <> json_extract(ev.data, '$.author_key')
			AND o.principal_key <> ?) AS corroborations,
		CASE
			WHEN json_extract(ev.data, '$.dedupe.result') = 'duplicate_of' THEN 'duplicate'
			ELSE coalesce((SELECT json_extract(t.data, '$.status') FROM events t WHERE t.entry_id = ev.entry_id
					AND t.subject_kind = 'review_finding' AND t.type = 'review_triage' ORDER BY t.seq DESC LIMIT 1),
				CASE WHEN a.stale_since IS NOT NULL THEN 'outdated' ELSE 'open' END)
		END AS status
	FROM anchors a CROSS JOIN events ev ON ev.entry_id = a.entry_id AND ev.subject_kind = 'review_finding'
		AND ev.type = 'review_finding'
	WHERE a.subject_kind = 'review_finding' AND a.kind = 'review_range' AND ev.workspace_id = ?`

// ListReviewFindings lists findings (see ReviewReader), oldest first.
func (r *reader) ListReviewFindings(ctx context.Context, f ReviewFindingFilter) ([]ReviewFinding, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	var ids []string
	if f.RecordID != "" {
		rec, err := r.GetReviewRecord(ctx, f.WorkspaceID, f.RecordID)
		if err != nil || len(rec.FindingIDs) == 0 {
			return nil, err
		}
		ids = rec.FindingIDs
	}
	query, args := reviewFindingsSQL(f, ids)
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewFinding
	for rows.Next() {
		fd, err := scanReviewFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fd)
	}
	return out, rows.Err()
}

// reviewFindingsSQL is the query ListReviewFindings runs: ids are the record's findings
// when f names a record.
func reviewFindingsSQL(f ReviewFindingFilter, ids []string) (string, []any) {
	args := []any{OpenModeIdentity, f.WorkspaceID}
	query := reviewFindingQuery
	if len(ids) > 0 {
		query += " AND a.entry_id IN (" + placeholders(len(ids)) + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	for _, c := range [...]struct {
		clause string
		v      any
		set    bool
	}{
		{"json_extract(ev.data, '$.lineage') = ?", f.Lineage, f.Lineage != ""},
		{"a.value = ?", f.Path, f.Path != ""},
		{"ev.seq > ?", f.AfterSeq, f.AfterSeq > 0},
	} {
		if c.set {
			query += " AND " + c.clause
			args = append(args, c.v)
		}
	}
	query += ")"
	if len(f.Statuses) > 0 {
		query += " WHERE status IN (" + placeholders(len(f.Statuses)) + ")"
		for _, s := range f.Statuses {
			args = append(args, s)
		}
	}
	limit := clampLimit(f.Limit)
	if len(ids) > 0 {
		limit = MaxReviewFindings
	}
	return query + " ORDER BY seq LIMIT ?", append(args, limit)
}

// GetReviewFinding returns one finding of the workspace.
func (r *reader) GetReviewFinding(ctx context.Context, workspaceID, id string) (ReviewFinding, error) {
	f, err := scanReviewFinding(r.queryRow(ctx, reviewFindingQuery+" AND a.entry_id = ?)", OpenModeIdentity, workspaceID, id))
	if errors.Is(err, ErrNotFound) {
		return f, fmt.Errorf("review finding %s: %w", id, ErrNotFound)
	}
	return f, err
}

// scanReviewFinding reads one row of reviewFindingQuery. The anchor's current place
// replaces the one recorded at creation.
func scanReviewFinding(s scanner) (ReviewFinding, error) {
	var f ReviewFinding
	var data string
	var place ReviewFindingInput
	if err := s.Scan(&f.ID, &f.WorkspaceID, &f.Author, &f.Seq, &f.At, &data, &f.Head, &f.OutdatedAt, &place.StartLine,
		&place.EndLine, &place.Side, &place.AnchorStatus, &f.Corroborations, &f.Status); err != nil {
		return f, err
	}
	var withProv struct {
		reviewFindingData
		Provenance struct {
			Owner string `json:"owner"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal([]byte(data), &withProv); err != nil {
		return f, fmt.Errorf("govstore: review finding %s: %w", f.ID, err)
	}
	f.reviewFindingData, f.AuthorOwner = withProv.reviewFindingData, withProv.Provenance.Owner
	f.StartLine, f.EndLine, f.Side, f.AnchorStatus = place.StartLine, place.EndLine, place.Side, place.AnchorStatus
	return f, nil
}
