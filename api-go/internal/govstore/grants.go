package govstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Grant permissions.
const (
	PermissionRead      = "ro"
	PermissionReadWrite = "rw"
)

// Applicability of a shared entry in a target workspace (PAR-SHARE-02). A fact
// verified in repo A has no authority in repo B until rechecked there.
const (
	ApplicabilityVerifiedHere     = "verified_here"
	ApplicabilityForeignUnchecked = "foreign_unchecked"
	ApplicabilityStale            = "stale"
)

var validApplicability = set(ApplicabilityVerifiedHere, ApplicabilityForeignUnchecked, ApplicabilityStale)

// Collection is a named set of shared memories with its own policy.
type Collection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	OwnerWorkspace string            `json:"owner_workspace"`
	Policy         map[string]string `json:"policy,omitempty"`
	CreatedBy      string            `json:"created_by"`
	CreatedAt      string            `json:"created_at"`
}

// CollectionInput creates a collection. ID "" generates one.
type CollectionInput struct {
	ID             string
	Name           string
	OwnerWorkspace string
	Policy         map[string]string
}

// Grant lets a target workspace (or a collection) read or write a source workspace's
// memories of one scope. Revoked grants stay as history.
type Grant struct {
	ID              string `json:"id"`
	SourceWorkspace string `json:"source_workspace"`
	TargetWorkspace string `json:"target_workspace,omitempty"`
	CollectionID    string `json:"collection_id,omitempty"`
	Scope           string `json:"scope"`
	Permission      string `json:"permission"`
	GrantedBy       string `json:"granted_by"`
	GrantedAt       string `json:"granted_at"`
	RevokedBy       string `json:"revoked_by,omitempty"`
	RevokedAt       string `json:"revoked_at,omitempty"`
}

// GrantInput requests a grant. At most one active grant exists per (source, target,
// collection, scope); asking for the same one again returns it, and asking for a
// different permission is a conflict (revoke first).
type GrantInput struct {
	SourceWorkspace string
	TargetWorkspace string
	CollectionID    string
	Scope           string // default "workspace"
	Permission      string // ro | rw
}

// GrantFilter lists grants by source, target or collection.
type GrantFilter struct {
	SourceWorkspace string
	TargetWorkspace string
	CollectionID    string
	IncludeRevoked  bool
	Limit           int
}

// Applicability is the standing of one entry in one target workspace.
type Applicability struct {
	EntryID         string `json:"entry_id"`
	TargetWorkspace string `json:"target_workspace"`
	State           string `json:"state"`
	Revision        int64  `json:"revision"`
	CheckedBy       string `json:"checked_by,omitempty"`
	CheckedAt       string `json:"checked_at"`
}

// GrantReader reads collections, grants and applicability.
type GrantReader interface {
	GetCollection(ctx context.Context, id string) (Collection, error)
	ListGrants(ctx context.Context, f GrantFilter) ([]Grant, error)
	ListApplicability(ctx context.Context, entryID string) ([]Applicability, error)
}

// GrantWriter writes collections, grants and applicability.
type GrantWriter interface {
	CreateCollection(ctx context.Context, in CollectionInput, actor Actor) (Collection, error)
	Grant(ctx context.Context, in GrantInput, actor Actor) (Grant, error)
	Revoke(ctx context.Context, grantID string, actor Actor) (Grant, error)
	SetApplicability(ctx context.Context, entryID, targetWorkspace, state string, actor Actor) (Applicability, error)
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("govstore: crypto/rand: %v", err))
	}
	return prefix + hex.EncodeToString(b[:])
}

// GetCollection returns one collection.
func (r *reader) GetCollection(ctx context.Context, id string) (Collection, error) {
	var c Collection
	var policy string
	err := r.queryRow(ctx, `SELECT id, name, owner_workspace, policy, created_by, created_at FROM collections WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.OwnerWorkspace, &policy, &c.CreatedBy, &c.CreatedAt)
	if err != nil {
		return Collection{}, fmt.Errorf("collection %s: %w", id, err)
	}
	if err := decodeJSONColumn(policy, &c.Policy); err != nil {
		return Collection{}, err
	}
	if len(c.Policy) == 0 {
		c.Policy = nil
	}
	return c, nil
}

const grantCols = `id, source_workspace, target_workspace, coalesce(collection_id, ''), scope, permission, granted_by,
	granted_at, revoked_by, coalesce(revoked_at, '')`

func scanGrant(s scanner) (Grant, error) {
	var g Grant
	err := s.Scan(&g.ID, &g.SourceWorkspace, &g.TargetWorkspace, &g.CollectionID, &g.Scope, &g.Permission, &g.GrantedBy,
		&g.GrantedAt, &g.RevokedBy, &g.RevokedAt)
	return g, err
}

// ListGrants returns grants matching every non-empty field of f.
func (r *reader) ListGrants(ctx context.Context, f GrantFilter) ([]Grant, error) {
	var where []string
	var args []any
	for _, c := range []struct{ col, val string }{
		{"source_workspace", f.SourceWorkspace}, {"target_workspace", f.TargetWorkspace}, {"collection_id", f.CollectionID},
	} {
		if c.val != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if len(where) == 0 {
		return nil, fmt.Errorf("%w: ListGrants needs a source, target or collection", ErrInvalid)
	}
	if !f.IncludeRevoked {
		where = append(where, "revoked_at IS NULL")
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, "SELECT "+grantCols+" FROM grants WHERE "+strings.Join(where, " AND ")+" ORDER BY pk LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListApplicability returns an entry's standing in each target workspace.
func (r *reader) ListApplicability(ctx context.Context, entryID string) ([]Applicability, error) {
	rows, err := r.query(ctx, `SELECT entry_id, target_workspace, state, revision, checked_by, checked_at FROM applicability
		WHERE entry_id = ? ORDER BY target_workspace LIMIT ?`, entryID, maxListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Applicability
	for rows.Next() {
		var a Applicability
		if err := rows.Scan(&a.EntryID, &a.TargetWorkspace, &a.State, &a.Revision, &a.CheckedBy, &a.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateCollection creates a shared collection owned by a workspace.
func (t *txn) CreateCollection(ctx context.Context, in CollectionInput, actor Actor) (Collection, error) {
	if err := actor.validate(); err != nil {
		return Collection{}, err
	}
	id := in.ID
	if id == "" {
		id = newID("col_")
	}
	if err := validID("collection", id); err != nil {
		return Collection{}, err
	}
	if err := validID("workspace", in.OwnerWorkspace); err != nil {
		return Collection{}, err
	}
	if strings.TrimSpace(in.Name) == "" || validName("name", in.Name) != nil {
		return Collection{}, fmt.Errorf("%w: collection name %q", ErrInvalid, in.Name)
	}
	if err := validateMetadata(in.Policy); err != nil {
		return Collection{}, err
	}
	policy, err := encodeMap(in.Policy)
	if err != nil {
		return Collection{}, err
	}
	if _, err := t.exec(ctx, `INSERT INTO collections (id, name, owner_workspace, policy, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, strings.TrimSpace(in.Name), in.OwnerWorkspace, policy,
		strings.TrimSpace(actor.Principal), t.nowText()); err != nil {
		return Collection{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: in.OwnerWorkspace, Type: EventCollection, Data: map[string]any{"collection_id": id, "name": in.Name},
	}); err != nil {
		return Collection{}, err
	}
	return t.GetCollection(ctx, id)
}

// Grant creates an active grant, or returns the identical active one.
func (t *txn) Grant(ctx context.Context, in GrantInput, actor Actor) (Grant, error) {
	if err := actor.validate(); err != nil {
		return Grant{}, err
	}
	if err := validID("workspace", in.SourceWorkspace); err != nil {
		return Grant{}, err
	}
	if in.TargetWorkspace == "" && in.CollectionID == "" {
		return Grant{}, fmt.Errorf("%w: a grant needs a target workspace or a collection", ErrInvalid)
	}
	if in.TargetWorkspace != "" {
		if err := validID("workspace", in.TargetWorkspace); err != nil {
			return Grant{}, err
		}
	}
	if in.CollectionID != "" {
		if _, err := t.GetCollection(ctx, in.CollectionID); err != nil {
			return Grant{}, err
		}
	}
	scope := in.Scope
	if scope == "" {
		scope = "workspace"
	}
	if !validScopes[scope] {
		return Grant{}, fmt.Errorf("%w: scope %q", ErrInvalid, scope)
	}
	if in.Permission != PermissionRead && in.Permission != PermissionReadWrite {
		return Grant{}, fmt.Errorf("%w: permission %q (ro or rw)", ErrInvalid, in.Permission)
	}
	existing, err := scanGrant(t.queryRow(ctx, "SELECT "+grantCols+` FROM grants WHERE source_workspace = ?
		AND target_workspace = ? AND coalesce(collection_id, '') = ? AND scope = ? AND revoked_at IS NULL`,
		in.SourceWorkspace, in.TargetWorkspace, in.CollectionID, scope))
	switch {
	case err == nil && existing.Permission == in.Permission:
		return existing, nil
	case err == nil:
		return Grant{}, fmt.Errorf("%w: an active %s grant %s exists; revoke it first", ErrConflict, existing.Permission, existing.ID)
	case !isNotFound(err):
		return Grant{}, err
	}
	id := newID("grant_")
	if _, err := t.exec(ctx, `INSERT INTO grants (id, source_workspace, target_workspace, collection_id, scope, permission,
		granted_by, granted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, in.SourceWorkspace, in.TargetWorkspace,
		nullText(in.CollectionID), scope, in.Permission, strings.TrimSpace(actor.Principal), t.nowText()); err != nil {
		return Grant{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: in.SourceWorkspace, Type: EventGrant, Data: map[string]any{
			"grant_id": id, "target_workspace": in.TargetWorkspace, "collection_id": in.CollectionID,
			"scope": scope, "permission": in.Permission,
		},
	}); err != nil {
		return Grant{}, err
	}
	return scanGrant(t.queryRow(ctx, "SELECT "+grantCols+" FROM grants WHERE id = ?", id))
}

// Revoke ends an active grant; the row stays as history.
func (t *txn) Revoke(ctx context.Context, grantID string, actor Actor) (Grant, error) {
	if err := actor.validate(); err != nil {
		return Grant{}, err
	}
	g, err := scanGrant(t.queryRow(ctx, "SELECT "+grantCols+" FROM grants WHERE id = ?", grantID))
	if err != nil {
		return Grant{}, fmt.Errorf("grant %s: %w", grantID, err)
	}
	if g.RevokedAt != "" {
		return g, nil
	}
	if _, err := t.exec(ctx, "UPDATE grants SET revoked_by = ?, revoked_at = ? WHERE id = ?",
		strings.TrimSpace(actor.Principal), t.nowText(), grantID); err != nil {
		return Grant{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: g.SourceWorkspace, Type: EventRevoke, Data: map[string]any{"grant_id": grantID},
	}); err != nil {
		return Grant{}, err
	}
	return scanGrant(t.queryRow(ctx, "SELECT "+grantCols+" FROM grants WHERE id = ?", grantID))
}

// SetApplicability records an entry's standing in a target workspace, bound to the
// entry's served revision.
func (t *txn) SetApplicability(ctx context.Context, entryID, targetWorkspace, state string, actor Actor) (Applicability, error) {
	if err := actor.validate(); err != nil {
		return Applicability{}, err
	}
	if !validApplicability[state] {
		return Applicability{}, fmt.Errorf("%w: applicability %q", ErrInvalid, state)
	}
	if err := validID("workspace", targetWorkspace); err != nil {
		return Applicability{}, err
	}
	e, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return Applicability{}, err
	}
	now := t.nowText()
	principal := strings.TrimSpace(actor.Principal)
	if _, err := t.exec(ctx, `INSERT INTO applicability (entry_id, target_workspace, state, revision, checked_by, checked_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (entry_id, target_workspace) DO UPDATE SET state = excluded.state,
		revision = excluded.revision, checked_by = excluded.checked_by, checked_at = excluded.checked_at`,
		entryID, targetWorkspace, state, e.Revision, principal, now); err != nil {
		return Applicability{}, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: entryID, Type: EventApplicability, Revision: e.Revision,
		Data: map[string]any{"target_workspace": targetWorkspace, "state": state},
	}); err != nil {
		return Applicability{}, err
	}
	return Applicability{EntryID: entryID, TargetWorkspace: targetWorkspace, State: state, Revision: e.Revision,
		CheckedBy: principal, CheckedAt: now}, nil
}
