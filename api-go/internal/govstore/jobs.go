package govstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Job states. A crashed lease (expired while leased) is claimable again until the
// attempts run out; a failed job is reported, never relaunched (PAR-GOV-13).
const (
	JobQueued    = "queued"
	JobLeased    = "leased"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job kinds the consolidation queue prepares.
const (
	JobDedupeCluster     = "dedupe_cluster"
	JobStaleAnchor       = "stale_anchor"
	JobOrphanedAnchor    = "orphaned_anchor"
	JobSupersessionChain = "supersession_chain"
	JobAgeingProposal    = "ageing_proposal"
	JobSessionDigest     = "session_digest"
	JobInitSeed          = "init_seed"
	// JobReviewDuplicate asks whether two review findings that overlap in place but not
	// in quoted code describe one defect (WS-66). Its candidates are finding ids.
	JobReviewDuplicate = "review_duplicate"
)

// jobKindSubjects is every job kind and the kind of subject its candidate ids name.
var jobKindSubjects = map[string]string{
	JobDedupeCluster: SubjectMemory, JobStaleAnchor: SubjectMemory, JobOrphanedAnchor: SubjectMemory,
	JobSupersessionChain: SubjectMemory, JobAgeingProposal: SubjectMemory, JobSessionDigest: SubjectMemory,
	JobInitSeed: SubjectMemory, JobReviewDuplicate: SubjectReviewFinding,
}

// Job is one consolidation work item.
type Job struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	Kind              string          `json:"kind"`
	SubjectKind       string          `json:"subject_kind"`
	State             string          `json:"state"`
	ConflictSignature string          `json:"conflict_signature"`
	CandidateIDs      []string        `json:"candidate_ids"`
	EvidenceRefs      []string        `json:"evidence_refs"`
	Instructions      string          `json:"instructions,omitempty"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	LeaseOwner        string          `json:"lease_owner,omitempty"`
	LeaseExpiresAt    string          `json:"lease_expires_at,omitempty"`
	LeaseVersion      int64           `json:"lease_version"`
	Attempts          int             `json:"attempts"`
	MaxAttempts       int             `json:"max_attempts"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	FinishedAt        string          `json:"finished_at,omitempty"`
}

// JobInput enqueues a job. The conflict signature identifies the work: enqueueing a
// signature that already has a job (in any state but cancelled) returns that job.
type JobInput struct {
	WorkspaceID       string
	Kind              string
	ConflictSignature string
	CandidateIDs      []string
	EvidenceRefs      []string
	Instructions      string
	Payload           any
	MaxAttempts       int // default 3
}

// ClaimRequest leases the oldest claimable job of a workspace.
type ClaimRequest struct {
	WorkspaceID string
	Kinds       []string
	Lease       time.Duration // default 5m
}

// Lease identifies a held lease: every lease operation compares-and-sets its version.
type Lease struct {
	JobID   string
	Owner   string
	Version int64
}

// JobFilter lists a workspace's jobs.
type JobFilter struct {
	WorkspaceID string
	States      []string
	Kinds       []string
	Limit       int
}

// Watermark tracks how far consolidation has reflected over one source (a session or
// transcript). It advances only on success (Letta's v3_assistant_steps shape).
type Watermark struct {
	WorkspaceID      string `json:"workspace_id"`
	Source           string `json:"source"`
	ReflectedThrough string `json:"reflected_through"`
	TotalCount       int64  `json:"total_count"`
	ReflectedCount   int64  `json:"reflected_count"`
	Version          int64  `json:"version"`
	LastStartedAt    string `json:"last_started_at,omitempty"`
	LastSucceededAt  string `json:"last_succeeded_at,omitempty"`
}

// WatermarkAdvance moves a watermark after a successful pass, compare-and-set on
// ExpectVersion (0 for a watermark that does not exist yet).
type WatermarkAdvance struct {
	WorkspaceID      string
	Source           string
	ExpectVersion    int64
	ReflectedThrough string
	TotalCount       int64
	ReflectedCount   int64
}

// JobReader reads jobs and watermarks.
type JobReader interface {
	GetJob(ctx context.Context, id string) (Job, error)
	ListJobs(ctx context.Context, f JobFilter) ([]Job, error)
	GetWatermark(ctx context.Context, workspaceID, source string) (Watermark, error)
}

// JobWriter runs the job lifecycle.
type JobWriter interface {
	EnqueueJob(ctx context.Context, in JobInput, actor Actor) (Job, bool, error)
	ClaimJob(ctx context.Context, req ClaimRequest, actor Actor) (Job, bool, error)
	RenewLease(ctx context.Context, lease Lease, d time.Duration, actor Actor) (Job, error)
	CompleteJob(ctx context.Context, lease Lease, result any, actor Actor) (Job, error)
	FailJob(ctx context.Context, lease Lease, reason string, actor Actor) (Job, error)
	CancelJob(ctx context.Context, id string, actor Actor) (Job, error)
	StartWatermark(ctx context.Context, workspaceID, source string, actor Actor) (Watermark, error)
	AdvanceWatermark(ctx context.Context, in WatermarkAdvance, actor Actor) (Watermark, error)
}

const jobCols = `id, workspace_id, kind, subject_kind, state, conflict_signature, candidate_ids, evidence_refs, instructions, payload,
	lease_owner, coalesce(lease_expires_at, ''), lease_version, attempts, max_attempts, coalesce(result, ''), error,
	created_at, updated_at, coalesce(finished_at, '')`

func scanJob(s scanner) (Job, error) {
	var j Job
	var cands, refs, payload, result string
	err := s.Scan(&j.ID, &j.WorkspaceID, &j.Kind, &j.SubjectKind, &j.State, &j.ConflictSignature, &cands, &refs, &j.Instructions, &payload,
		&j.LeaseOwner, &j.LeaseExpiresAt, &j.LeaseVersion, &j.Attempts, &j.MaxAttempts, &result, &j.Error,
		&j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if err != nil {
		return Job{}, err
	}
	if err := decodeJSONColumn(cands, &j.CandidateIDs); err != nil {
		return Job{}, err
	}
	if err := decodeJSONColumn(refs, &j.EvidenceRefs); err != nil {
		return Job{}, err
	}
	if payload != "" && payload != "{}" {
		j.Payload = json.RawMessage(payload)
	}
	if result != "" {
		j.Result = json.RawMessage(result)
	}
	return j, nil
}

// GetJob returns one job.
func (r *reader) GetJob(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(r.queryRow(ctx, "SELECT "+jobCols+" FROM jobs WHERE id = ?", id))
	if err != nil {
		return Job{}, fmt.Errorf("job %s: %w", id, err)
	}
	return j, nil
}

// ListJobs lists a workspace's jobs oldest first.
func (r *reader) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	where := []string{"workspace_id = ?"}
	args := []any{f.WorkspaceID}
	for _, in := range []struct {
		col  string
		vals []string
	}{{"state", f.States}, {"kind", f.Kinds}} {
		if len(in.vals) > 0 {
			where = append(where, in.col+" IN ("+placeholders(len(in.vals))+")")
			for _, v := range in.vals {
				args = append(args, v)
			}
		}
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, "SELECT "+jobCols+" FROM jobs WHERE "+strings.Join(where, " AND ")+" ORDER BY pk LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetWatermark returns a watermark; a missing one reads as version 0.
func (r *reader) GetWatermark(ctx context.Context, workspaceID, source string) (Watermark, error) {
	w := Watermark{WorkspaceID: workspaceID, Source: source}
	err := r.queryRow(ctx, `SELECT reflected_through, total_count, reflected_count, version, coalesce(last_started_at, ''),
		coalesce(last_succeeded_at, '') FROM watermarks WHERE workspace_id = ? AND source = ?`, workspaceID, source).
		Scan(&w.ReflectedThrough, &w.TotalCount, &w.ReflectedCount, &w.Version, &w.LastStartedAt, &w.LastSucceededAt)
	if isNotFound(err) {
		return w, nil
	}
	return w, err
}

// EnqueueJob adds a job, or returns the existing job with the same signature.
func (t *txn) EnqueueJob(ctx context.Context, in JobInput, actor Actor) (Job, bool, error) {
	if err := actor.validate(); err != nil {
		return Job{}, false, err
	}
	if err := validID("workspace", in.WorkspaceID); err != nil {
		return Job{}, false, err
	}
	subject, ok := jobKindSubjects[in.Kind]
	if !ok {
		return Job{}, false, fmt.Errorf("%w: job kind %q", ErrInvalid, in.Kind)
	}
	if strings.TrimSpace(in.ConflictSignature) == "" || validName("conflict signature", in.ConflictSignature) != nil {
		return Job{}, false, fmt.Errorf("%w: conflict signature %q", ErrInvalid, in.ConflictSignature)
	}
	existing, err := scanJob(t.queryRow(ctx, "SELECT "+jobCols+
		" FROM jobs WHERE workspace_id = ? AND conflict_signature = ? AND state <> 'cancelled'", in.WorkspaceID, in.ConflictSignature))
	switch {
	case err == nil:
		return existing, false, nil
	case !isNotFound(err):
		return Job{}, false, err
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	cands, err := encodeStrings(in.CandidateIDs)
	if err != nil {
		return Job{}, false, err
	}
	refs, err := encodeStrings(in.EvidenceRefs)
	if err != nil {
		return Job{}, false, err
	}
	payload := "{}"
	if in.Payload != nil {
		if payload, err = encodeJSON(in.Payload); err != nil {
			return Job{}, false, err
		}
	}
	id := newID("job_")
	now := t.nowText()
	if _, err := t.exec(ctx, `INSERT INTO jobs (id, workspace_id, kind, subject_kind, conflict_signature, candidate_ids,
		evidence_refs, instructions, payload, max_attempts, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.WorkspaceID, in.Kind, subject, in.ConflictSignature, cands, refs, in.Instructions, payload, maxAttempts, now, now); err != nil {
		return Job{}, false, err
	}
	j, err := t.GetJob(ctx, id)
	return j, err == nil, err
}

// ClaimJob leases the oldest queued job, or a leased job whose lease expired (its
// holder crashed). A job whose expired leases used up max_attempts becomes failed.
func (t *txn) ClaimJob(ctx context.Context, req ClaimRequest, actor Actor) (Job, bool, error) {
	if err := actor.validate(); err != nil {
		return Job{}, false, err
	}
	if err := validID("workspace", req.WorkspaceID); err != nil {
		return Job{}, false, err
	}
	lease := req.Lease
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	owner := strings.TrimSpace(actor.Principal)
	kindFilter := ""
	var kindArgs []any
	if len(req.Kinds) > 0 {
		kindFilter = " AND kind IN (" + placeholders(len(req.Kinds)) + ")"
		for _, k := range req.Kinds {
			kindArgs = append(kindArgs, k)
		}
	}
	for range maxListLimit {
		nowT := t.now()
		now := canonTime(nowT)
		args := append([]any{req.WorkspaceID, now}, kindArgs...)
		var id, state string
		var version int64
		var attempts, maxAttempts int
		err := t.queryRow(ctx, `SELECT id, state, lease_version, attempts, max_attempts FROM jobs
			WHERE workspace_id = ? AND (state = 'queued' OR (state = 'leased' AND lease_expires_at <= ?))`+kindFilter+`
			ORDER BY pk LIMIT 1`, args...).Scan(&id, &state, &version, &attempts, &maxAttempts)
		if isNotFound(err) {
			return Job{}, false, nil
		}
		if err != nil {
			return Job{}, false, err
		}
		if state == JobLeased && attempts >= maxAttempts {
			if _, err := t.exec(ctx, `UPDATE jobs SET state = 'failed', error = ?, finished_at = ?, updated_at = ?,
				lease_owner = '', lease_expires_at = NULL WHERE id = ? AND lease_version = ?`,
				fmt.Sprintf("lease expired after %d attempts", attempts), now, now, id, version); err != nil {
				return Job{}, false, err
			}
			continue
		}
		res, err := t.exec(ctx, `UPDATE jobs SET state = 'leased', lease_owner = ?, lease_expires_at = ?,
			lease_version = lease_version + 1, attempts = attempts + 1, updated_at = ?
			WHERE id = ? AND lease_version = ?`, owner, canonTime(nowT.Add(lease)), now, id, version)
		if err != nil {
			return Job{}, false, err
		}
		if rowsAffected(res) == 0 {
			continue
		}
		j, err := t.GetJob(ctx, id)
		return j, err == nil, err
	}
	return Job{}, false, fmt.Errorf("%w: too many expired jobs to scan", ErrBusy)
}

func (t *txn) leaseUpdate(ctx context.Context, lease Lease, set string, args ...any) (Job, error) {
	all := append(args, lease.JobID, lease.Owner, lease.Version)
	res, err := t.exec(ctx, "UPDATE jobs SET "+set+
		" WHERE id = ? AND state = 'leased' AND lease_owner = ? AND lease_version = ?", all...)
	if err != nil {
		return Job{}, err
	}
	if rowsAffected(res) == 0 {
		if _, err := t.GetJob(ctx, lease.JobID); err != nil {
			return Job{}, err
		}
		return Job{}, fmt.Errorf("%w: lease %s@%d on job %s is no longer held", ErrConflict, lease.Owner, lease.Version, lease.JobID)
	}
	return t.GetJob(ctx, lease.JobID)
}

// RenewLease extends a held lease.
func (t *txn) RenewLease(ctx context.Context, lease Lease, d time.Duration, actor Actor) (Job, error) {
	if err := actor.validate(); err != nil {
		return Job{}, err
	}
	if d <= 0 {
		return Job{}, fmt.Errorf("%w: lease duration %s", ErrInvalid, d)
	}
	now := t.now()
	return t.leaseUpdate(ctx, lease, "lease_expires_at = ?, updated_at = ?", canonTime(now.Add(d)), canonTime(now))
}

// CompleteJob finishes a held job successfully with its result.
func (t *txn) CompleteJob(ctx context.Context, lease Lease, result any, actor Actor) (Job, error) {
	if err := actor.validate(); err != nil {
		return Job{}, err
	}
	var res any
	if result != nil {
		s, err := encodeJSON(result)
		if err != nil {
			return Job{}, err
		}
		res = s
	}
	now := t.nowText()
	return t.leaseUpdate(ctx, lease, `state = 'done', result = ?, finished_at = ?, updated_at = ?, lease_owner = '',
		lease_expires_at = NULL`, res, now, now)
}

// FailJob finishes a held job as failed. It will be reported, not relaunched.
func (t *txn) FailJob(ctx context.Context, lease Lease, reason string, actor Actor) (Job, error) {
	if err := actor.validate(); err != nil {
		return Job{}, err
	}
	if len(reason) > maxNoteLen {
		reason = reason[:maxNoteLen]
	}
	now := t.nowText()
	return t.leaseUpdate(ctx, lease, `state = 'failed', error = ?, finished_at = ?, updated_at = ?, lease_owner = '',
		lease_expires_at = NULL`, reason, now, now)
}

// CancelJob cancels a queued or leased job; its signature becomes free again.
func (t *txn) CancelJob(ctx context.Context, id string, actor Actor) (Job, error) {
	if err := actor.validate(); err != nil {
		return Job{}, err
	}
	now := t.nowText()
	res, err := t.exec(ctx, `UPDATE jobs SET state = 'cancelled', finished_at = ?, updated_at = ?, lease_owner = '',
		lease_expires_at = NULL WHERE id = ? AND state IN ('queued', 'leased')`, now, now, id)
	if err != nil {
		return Job{}, err
	}
	j, err := t.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if rowsAffected(res) == 0 && j.State != JobCancelled {
		return Job{}, fmt.Errorf("%w: job %s is %s", ErrInvalid, id, j.State)
	}
	return j, nil
}

// StartWatermark stamps the start of a consolidation pass without advancing it.
func (t *txn) StartWatermark(ctx context.Context, workspaceID, source string, actor Actor) (Watermark, error) {
	if err := actor.validate(); err != nil {
		return Watermark{}, err
	}
	if err := validID("workspace", workspaceID); err != nil {
		return Watermark{}, err
	}
	if strings.TrimSpace(source) == "" || validName("source", source) != nil {
		return Watermark{}, fmt.Errorf("%w: watermark source %q", ErrInvalid, source)
	}
	if _, err := t.exec(ctx, `INSERT INTO watermarks (workspace_id, source, last_started_at) VALUES (?, ?, ?)
		ON CONFLICT (workspace_id, source) DO UPDATE SET last_started_at = excluded.last_started_at`,
		workspaceID, source, t.nowText()); err != nil {
		return Watermark{}, err
	}
	return t.GetWatermark(ctx, workspaceID, source)
}

// AdvanceWatermark records a successful pass, compare-and-set on the version.
func (t *txn) AdvanceWatermark(ctx context.Context, in WatermarkAdvance, actor Actor) (Watermark, error) {
	if err := actor.validate(); err != nil {
		return Watermark{}, err
	}
	cur, err := t.GetWatermark(ctx, in.WorkspaceID, in.Source)
	if err != nil {
		return Watermark{}, err
	}
	if cur.Version != in.ExpectVersion {
		return Watermark{}, fmt.Errorf("%w: watermark %s/%s is at version %d, expected %d", ErrConflict,
			in.WorkspaceID, in.Source, cur.Version, in.ExpectVersion)
	}
	if in.ReflectedCount < cur.ReflectedCount || in.TotalCount < in.ReflectedCount {
		return Watermark{}, fmt.Errorf("%w: a watermark only moves forward and never past its total", ErrInvalid)
	}
	now := t.nowText()
	if _, err := t.exec(ctx, `INSERT INTO watermarks (workspace_id, source, reflected_through, total_count, reflected_count,
		version, last_succeeded_at) VALUES (?, ?, ?, ?, ?, 1, ?)
		ON CONFLICT (workspace_id, source) DO UPDATE SET reflected_through = excluded.reflected_through,
		total_count = excluded.total_count, reflected_count = excluded.reflected_count, version = watermarks.version + 1,
		last_succeeded_at = excluded.last_succeeded_at`,
		in.WorkspaceID, in.Source, in.ReflectedThrough, in.TotalCount, in.ReflectedCount, now); err != nil {
		return Watermark{}, err
	}
	return t.GetWatermark(ctx, in.WorkspaceID, in.Source)
}
