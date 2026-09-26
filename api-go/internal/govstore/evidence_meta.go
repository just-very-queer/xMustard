package govstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EvidenceMeta is the metadata of one captured evidence original. The original bytes
// stay files owned by the evidence store; only this record lives here. It mirrors
// evidence.Observation without the projection text.
type EvidenceMeta struct {
	Handle                   string          `json:"handle"`
	WorkspaceID              string          `json:"workspace_id"`
	RepoScope                string          `json:"repo_scope,omitempty"`
	Actor                    string          `json:"actor,omitempty"`
	AuthEnforced             bool            `json:"auth_enforced"`
	Issuer                   string          `json:"issuer,omitempty"`
	SessionID                string          `json:"session_id,omitempty"`
	CallID                   string          `json:"call_id,omitempty"`
	Tool                     string          `json:"tool"`
	ToolVersion              string          `json:"tool_version,omitempty"`
	ArgsDigest               string          `json:"args_digest,omitempty"`
	CapturedAt               string          `json:"captured_at"`
	CapturedKey              string          `json:"captured_key,omitempty"`
	CapturedIdentityComplete bool            `json:"captured_identity_complete"`
	Status                   int             `json:"status"`
	IsError                  bool            `json:"is_error"`
	ContentType              string          `json:"content_type,omitempty"`
	RawSHA256                string          `json:"raw_sha256"`
	RawBytes                 int64           `json:"raw_bytes"`
	ExpiresAt                string          `json:"expires_at"`
	Revoked                  bool            `json:"revoked,omitempty"`
	RetentionClass           string          `json:"retention_class"`
	Projection               json.RawMessage `json:"projection,omitempty"`
	Cursor                   int64           `json:"cursor"`
}

// EvidenceFilter pages a workspace's evidence, newest first.
type EvidenceFilter struct {
	WorkspaceID  string
	SessionID    string
	Tool         string
	BeforeCursor int64
	Limit        int
}

// EvidenceReader reads evidence metadata.
type EvidenceReader interface {
	GetEvidenceMeta(ctx context.Context, handle string) (EvidenceMeta, error)
	ListEvidenceMeta(ctx context.Context, f EvidenceFilter) ([]EvidenceMeta, error)
	// ExpiredEvidence lists handles whose originals are past expiry at before, oldest
	// first, so the evidence store can delete the files.
	ExpiredEvidence(ctx context.Context, before string, limit int) ([]string, error)
}

// EvidenceWriter writes evidence metadata. A record is immutable except for revoke.
type EvidenceWriter interface {
	PutEvidenceMeta(ctx context.Context, m EvidenceMeta) error
	RevokeEvidence(ctx context.Context, handle string, actor Actor) error
	DeleteEvidenceMeta(ctx context.Context, handles []string) (int, error)
}

const evidenceCols = `handle, workspace_id, repo_scope, actor, auth_enforced, issuer, session_id, call_id, tool,
	tool_version, args_digest, captured_at, captured_key, captured_identity_complete, status, is_error, content_type,
	raw_sha256, raw_bytes, expires_at, revoked, retention_class, projection, pk`

func scanEvidence(s scanner) (EvidenceMeta, error) {
	var m EvidenceMeta
	var auth, complete, isErr, revoked int
	var projection string
	err := s.Scan(&m.Handle, &m.WorkspaceID, &m.RepoScope, &m.Actor, &auth, &m.Issuer, &m.SessionID, &m.CallID, &m.Tool,
		&m.ToolVersion, &m.ArgsDigest, &m.CapturedAt, &m.CapturedKey, &complete, &m.Status, &isErr, &m.ContentType,
		&m.RawSHA256, &m.RawBytes, &m.ExpiresAt, &revoked, &m.RetentionClass, &projection, &m.Cursor)
	if err != nil {
		return EvidenceMeta{}, err
	}
	m.AuthEnforced, m.CapturedIdentityComplete, m.IsError, m.Revoked = auth == 1, complete == 1, isErr == 1, revoked == 1
	if projection != "" && projection != "{}" {
		m.Projection = json.RawMessage(projection)
	}
	return m, nil
}

// GetEvidenceMeta returns one record.
func (r *reader) GetEvidenceMeta(ctx context.Context, handle string) (EvidenceMeta, error) {
	m, err := scanEvidence(r.queryRow(ctx, "SELECT "+evidenceCols+" FROM evidence_meta WHERE handle = ?", handle))
	if err != nil {
		return EvidenceMeta{}, fmt.Errorf("evidence %s: %w", handle, err)
	}
	return m, nil
}

// ListEvidenceMeta pages a workspace's records newest first.
func (r *reader) ListEvidenceMeta(ctx context.Context, f EvidenceFilter) ([]EvidenceMeta, error) {
	if err := validID("workspace", f.WorkspaceID); err != nil {
		return nil, err
	}
	where := []string{"workspace_id = ?"}
	args := []any{f.WorkspaceID}
	if f.SessionID != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.SessionID)
	}
	if f.Tool != "" {
		where = append(where, "tool = ?")
		args = append(args, f.Tool)
	}
	if f.BeforeCursor > 0 {
		where = append(where, "pk < ?")
		args = append(args, f.BeforeCursor)
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, "SELECT "+evidenceCols+" FROM evidence_meta WHERE "+strings.Join(where, " AND ")+
		" ORDER BY pk DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceMeta
	for rows.Next() {
		m, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ExpiredEvidence lists expired handles oldest first.
func (r *reader) ExpiredEvidence(ctx context.Context, before string, limit int) ([]string, error) {
	cutoff, err := inputTime("before", before)
	if err != nil || cutoff == "" {
		return nil, fmt.Errorf("%w: before must be an RFC 3339 time", ErrInvalid)
	}
	rows, err := r.query(ctx, "SELECT handle FROM evidence_meta WHERE expires_at < ? ORDER BY expires_at LIMIT ?",
		cutoff, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// PutEvidenceMeta inserts a record. Re-putting an identical record is a no-op; a
// different record under the same handle is a conflict.
func (t *txn) PutEvidenceMeta(ctx context.Context, m EvidenceMeta) error {
	if err := validID("evidence handle", m.Handle); err != nil {
		return err
	}
	if err := validID("workspace", m.WorkspaceID); err != nil {
		return err
	}
	if strings.TrimSpace(m.Tool) == "" || m.RawSHA256 == "" || m.RawBytes < 0 {
		return fmt.Errorf("%w: evidence needs a tool, a raw digest and a size", ErrInvalid)
	}
	capturedAt, err := inputTime("captured_at", m.CapturedAt)
	if err != nil {
		return err
	}
	expiresAt, err := inputTime("expires_at", m.ExpiresAt)
	if err != nil {
		return err
	}
	if capturedAt == "" || expiresAt == "" {
		return fmt.Errorf("%w: evidence needs captured_at and expires_at", ErrInvalid)
	}
	projection := "{}"
	if len(m.Projection) > 0 {
		if !json.Valid(m.Projection) {
			return fmt.Errorf("%w: projection is not JSON", ErrInvalid)
		}
		projection = string(m.Projection)
	}
	retention := m.RetentionClass
	if retention == "" {
		retention = "evidence"
	}
	existing, err := t.GetEvidenceMeta(ctx, m.Handle)
	switch {
	case err == nil:
		if existing.RawSHA256 == m.RawSHA256 && existing.WorkspaceID == m.WorkspaceID && existing.RawBytes == m.RawBytes {
			return nil
		}
		return fmt.Errorf("%w: evidence handle %s already names different content", ErrConflict, m.Handle)
	case !isNotFound(err):
		return err
	}
	_, err = t.exec(ctx, `INSERT INTO evidence_meta (handle, workspace_id, repo_scope, actor, auth_enforced, issuer,
		session_id, call_id, tool, tool_version, args_digest, captured_at, captured_key, captured_identity_complete,
		status, is_error, content_type, raw_sha256, raw_bytes, expires_at, revoked, retention_class, projection)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Handle, m.WorkspaceID, m.RepoScope, m.Actor, boolInt(m.AuthEnforced), m.Issuer, m.SessionID, m.CallID, m.Tool,
		m.ToolVersion, m.ArgsDigest, capturedAt, m.CapturedKey, boolInt(m.CapturedIdentityComplete), m.Status,
		boolInt(m.IsError), m.ContentType, m.RawSHA256, m.RawBytes, expiresAt, boolInt(m.Revoked), retention, projection)
	return err
}

// RevokeEvidence marks a record revoked. It is idempotent.
func (t *txn) RevokeEvidence(ctx context.Context, handle string, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	if _, err := t.GetEvidenceMeta(ctx, handle); err != nil {
		return err
	}
	_, err := t.exec(ctx, "UPDATE evidence_meta SET revoked = 1 WHERE handle = ?", handle)
	return err
}

// DeleteEvidenceMeta removes records whose originals are gone.
func (t *txn) DeleteEvidenceMeta(ctx context.Context, handles []string) (int, error) {
	n := 0
	for start := 0; start < len(handles); start += maxInArgs {
		chunk := handles[start:min(start+maxInArgs, len(handles))]
		args := make([]any, len(chunk))
		for i, h := range chunk {
			args[i] = h
		}
		res, err := t.exec(ctx, "DELETE FROM evidence_meta WHERE handle IN ("+placeholders(len(chunk))+")", args...)
		if err != nil {
			return n, err
		}
		n += int(rowsAffected(res))
	}
	return n, nil
}
