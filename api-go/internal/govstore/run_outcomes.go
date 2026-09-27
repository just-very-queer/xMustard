package govstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Run-independent outcomes (WS-21; PAR-HAR-06). A why_failed command run, a pasted
// log, an evidence original or a captured failing test/build output becomes one row:
// its status, the command or tool that produced it and the failure analysis made from
// the bounded tail of its output. Rows are written once and never edited, except that
// a later outcome of the same subject resolves an earlier open failure.

// Run outcome sources and statuses.
const (
	RunSourceCommand  = "command"
	RunSourceEvidence = "evidence"
	RunSourceLog      = "log"
	RunSourceCapture  = "capture"

	RunPassed   = "passed"
	RunFailed   = "failed"
	RunTimedOut = "timed_out"

	// RunOutcomePrefix starts every run outcome id, so a run id names either a platform
	// run or a run outcome without a lookup.
	RunOutcomePrefix = "oc_"
)

var (
	validRunSources  = set(RunSourceCommand, RunSourceEvidence, RunSourceLog, RunSourceCapture)
	validRunStatuses = set(RunPassed, RunFailed, RunTimedOut)
)

// Bounds on the stored text of one outcome.
const (
	maxRunCommand  = 4 << 10
	maxRunTail     = 16 << 10
	maxRunAnalysis = 64 << 10
)

// RunOutcome is one stored outcome.
type RunOutcome struct {
	ID             string          `json:"id"`
	WorkspaceID    string          `json:"workspace_id"`
	Source         string          `json:"source"`
	SubjectKey     string          `json:"subject_key,omitempty"`
	Status         string          `json:"status"`
	Failed         bool            `json:"failed"`
	ExitCode       *int            `json:"exit_code,omitempty"`
	Command        string          `json:"command,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	Tool           string          `json:"tool,omitempty"`
	EvidenceHandle string          `json:"evidence_handle,omitempty"`
	OutputBytes    int64           `json:"output_bytes"`
	AnalyzedBytes  int64           `json:"analyzed_bytes"`
	OutputSHA256   string          `json:"output_sha256,omitempty"`
	Analysis       json.RawMessage `json:"analysis"`
	Tail           string          `json:"tail,omitempty"`
	Principal      string          `json:"principal"`
	SessionID      string          `json:"session_id,omitempty"`
	HeadSHA        string          `json:"head_sha,omitempty"`
	CreatedAt      string          `json:"created_at"`
	ResolvedBy     string          `json:"resolved_by,omitempty"`
	ResolvedAt     string          `json:"resolved_at,omitempty"`
}

// RunOutcomeInput records one outcome. SourceKey makes the write idempotent within a
// workspace; SubjectKey, when set, resolves earlier open failures of the same subject.
type RunOutcomeInput struct {
	WorkspaceID    string
	Source         string
	SourceKey      string
	SubjectKey     string
	Status         string
	ExitCode       *int
	Command        string
	Cwd            string
	Tool           string
	EvidenceHandle string
	OutputBytes    int64
	AnalyzedBytes  int64
	OutputSHA256   string
	Analysis       json.RawMessage
	Tail           string
}

// RunOutcomeFilter pages a workspace's outcomes, newest first.
type RunOutcomeFilter struct {
	WorkspaceID string
	// Open selects failures no later outcome of their subject resolved.
	Open bool
	// SubjectKey selects one subject's outcomes.
	SubjectKey string
	// Since selects outcomes recorded at or after this RFC 3339 time.
	Since string
	Limit int
}

// RunOutcomeReader reads run outcomes.
type RunOutcomeReader interface {
	GetRunOutcome(ctx context.Context, workspaceID, id string) (RunOutcome, error)
	ListRunOutcomes(ctx context.Context, f RunOutcomeFilter) ([]RunOutcome, error)
}

// RunOutcomeWriter records and removes run outcomes.
type RunOutcomeWriter interface {
	// RecordRunOutcome stores in and reports whether it is new. A second report with the
	// same source key returns the stored outcome unchanged (created false).
	RecordRunOutcome(ctx context.Context, in RunOutcomeInput, actor Actor) (out RunOutcome, created bool, err error)
	// DeleteRunOutcome removes one outcome; ErrNotFound when the workspace has none by id.
	DeleteRunOutcome(ctx context.Context, workspaceID, id string) error
	// DeleteEvidenceOutcomes removes the outcomes made from the evidence original handle,
	// or with handle "" from any original, and reports how many it removed.
	DeleteEvidenceOutcomes(ctx context.Context, workspaceID, handle string) (int, error)
}

const runOutcomeColumns = `id, workspace_id, source, subject_key, status, failed, exit_code, command, cwd, tool,
	evidence_handle, output_bytes, analyzed_bytes, output_sha256, analysis, tail, principal, session_id, head_sha,
	created_at, resolved_by, coalesce(resolved_at, '')`

func scanRunOutcome(row interface{ Scan(...any) error }) (RunOutcome, error) {
	var o RunOutcome
	var exit sql.NullInt64
	var analysis string
	err := row.Scan(&o.ID, &o.WorkspaceID, &o.Source, &o.SubjectKey, &o.Status, &o.Failed, &exit, &o.Command, &o.Cwd,
		&o.Tool, &o.EvidenceHandle, &o.OutputBytes, &o.AnalyzedBytes, &o.OutputSHA256, &analysis, &o.Tail, &o.Principal,
		&o.SessionID, &o.HeadSHA, &o.CreatedAt, &o.ResolvedBy, &o.ResolvedAt)
	if err != nil {
		return RunOutcome{}, err
	}
	if exit.Valid {
		code := int(exit.Int64)
		o.ExitCode = &code
	}
	o.Analysis = json.RawMessage(analysis)
	o.CreatedAt, o.ResolvedAt = displayTime(o.CreatedAt), displayTime(o.ResolvedAt)
	return o, nil
}

// GetRunOutcome returns one outcome of the workspace.
func (r *reader) GetRunOutcome(ctx context.Context, workspaceID, id string) (RunOutcome, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return RunOutcome{}, err
	}
	if err := validID("run outcome", id); err != nil {
		return RunOutcome{}, err
	}
	return scanRunOutcome(r.queryRow(ctx, `SELECT `+runOutcomeColumns+` FROM run_outcomes
		WHERE workspace_id = ? AND id = ?`, workspaceID, id))
}

// ListRunOutcomes pages a workspace's outcomes, newest first.
func (r *reader) ListRunOutcomes(ctx context.Context, f RunOutcomeFilter) ([]RunOutcome, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	since, err := inputTime("since", f.Since)
	if err != nil {
		return nil, err
	}
	where := []string{"workspace_id = ?"}
	args := []any{f.WorkspaceID}
	if f.Open {
		where = append(where, "failed = 1 AND resolved_at IS NULL")
	}
	if f.SubjectKey != "" {
		where, args = append(where, "subject_key = ?"), append(args, f.SubjectKey)
	}
	if since != "" {
		where, args = append(where, "created_at >= ?"), append(args, since)
	}
	rows, err := r.query(ctx, `SELECT `+runOutcomeColumns+` FROM run_outcomes WHERE `+strings.Join(where, " AND ")+
		` ORDER BY created_at DESC, pk DESC LIMIT ?`, append(args, clampLimit(f.Limit))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunOutcome
	for rows.Next() {
		o, err := scanRunOutcome(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// validate checks the input's closed sets and bounds.
func (in RunOutcomeInput) validate() error {
	if err := validID("workspace", in.WorkspaceID); err != nil {
		return err
	}
	switch {
	case !validRunSources[in.Source]:
		return fmt.Errorf("%w: run outcome source %q", ErrInvalid, in.Source)
	case !validRunStatuses[in.Status]:
		return fmt.Errorf("%w: run outcome status %q", ErrInvalid, in.Status)
	case strings.TrimSpace(in.SourceKey) == "" || len(in.SourceKey) > maxPathLen || len(in.SubjectKey) > maxPathLen:
		return fmt.Errorf("%w: run outcome source or subject key", ErrInvalid)
	case len(in.Command) > maxRunCommand || len(in.Cwd) > maxPathLen || len(in.Tool) > maxNameLen || len(in.EvidenceHandle) > maxNameLen:
		return fmt.Errorf("%w: run outcome command, cwd, tool or handle too long", ErrInvalid)
	case len(in.Tail) > maxRunTail || len(in.Analysis) > maxRunAnalysis:
		return fmt.Errorf("%w: run outcome tail or analysis too long", ErrInvalid)
	case in.OutputBytes < 0 || in.AnalyzedBytes < 0 || in.AnalyzedBytes > in.OutputBytes:
		return fmt.Errorf("%w: run outcome byte counts", ErrInvalid)
	case len(in.Analysis) > 0 && !json.Valid(in.Analysis):
		return fmt.Errorf("%w: run outcome analysis is not JSON", ErrInvalid)
	}
	return nil
}

// RecordRunOutcome stores one outcome. A new outcome with a subject resolves the
// subject's earlier open failures: the newest outcome of a command is what it says now.
func (t *txn) RecordRunOutcome(ctx context.Context, in RunOutcomeInput, actor Actor) (RunOutcome, bool, error) {
	if err := actor.validate(); err != nil {
		return RunOutcome{}, false, err
	}
	if err := in.validate(); err != nil {
		return RunOutcome{}, false, err
	}
	analysis := string(in.Analysis)
	if analysis == "" {
		analysis = "{}"
	}
	var exit any
	if in.ExitCode != nil {
		exit = *in.ExitCode
	}
	id, now := newID(RunOutcomePrefix), t.nowText()
	res, err := t.exec(ctx, `INSERT INTO run_outcomes (id, workspace_id, source, source_key, subject_key, status, failed,
		exit_code, command, cwd, tool, evidence_handle, output_bytes, analyzed_bytes, output_sha256, analysis, tail,
		principal, session_id, head_sha, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, source_key) DO NOTHING`,
		id, in.WorkspaceID, in.Source, in.SourceKey, in.SubjectKey, in.Status, boolInt(in.Status != RunPassed), exit,
		in.Command, in.Cwd, in.Tool, in.EvidenceHandle, in.OutputBytes, in.AnalyzedBytes, in.OutputSHA256, analysis,
		in.Tail, strings.TrimSpace(actor.Principal), actor.SessionID, actor.HeadSHA, now)
	if err != nil {
		return RunOutcome{}, false, err
	}
	if rowsAffected(res) == 0 {
		o, err := scanRunOutcome(t.queryRow(ctx, `SELECT `+runOutcomeColumns+` FROM run_outcomes
			WHERE workspace_id = ? AND source_key = ?`, in.WorkspaceID, in.SourceKey))
		return o, false, err
	}
	if in.SubjectKey != "" {
		if _, err := t.exec(ctx, `UPDATE run_outcomes SET resolved_by = ?, resolved_at = ?
			WHERE workspace_id = ? AND subject_key = ? AND failed = 1 AND resolved_at IS NULL AND id <> ?`,
			id, now, in.WorkspaceID, in.SubjectKey, id); err != nil {
			return RunOutcome{}, false, err
		}
	}
	o, err := t.GetRunOutcome(ctx, in.WorkspaceID, id)
	if errors.Is(err, ErrNotFound) {
		return RunOutcome{}, false, fmt.Errorf("%w: run outcome %s vanished inside its transaction", ErrInvariant, id)
	}
	return o, true, err
}

// DeleteRunOutcome removes one outcome of the workspace. Outcomes it resolved stay
// resolved: the later outcome was recorded, whatever became of its row.
func (t *txn) DeleteRunOutcome(ctx context.Context, workspaceID, id string) error {
	if err := validID("workspace", workspaceID); err != nil {
		return err
	}
	if err := validID("run outcome", id); err != nil {
		return err
	}
	res, err := t.exec(ctx, `DELETE FROM run_outcomes WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	if err != nil {
		return err
	}
	if rowsAffected(res) == 0 {
		return fmt.Errorf("%w: run outcome %s", ErrNotFound, id)
	}
	return nil
}

// DeleteEvidenceOutcomes removes the workspace's outcomes made from the evidence original
// handle, or with handle "" from any original: a revoked or purged original takes the
// tail, error lines and failing tests its outcomes copied with it.
func (t *txn) DeleteEvidenceOutcomes(ctx context.Context, workspaceID, handle string) (int, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return 0, err
	}
	res, err := t.exec(ctx, `DELETE FROM run_outcomes WHERE workspace_id = ? AND evidence_handle <> ''
		AND (? = '' OR evidence_handle = ?)`, workspaceID, handle, handle)
	if err != nil {
		return 0, err
	}
	return int(rowsAffected(res)), nil
}
