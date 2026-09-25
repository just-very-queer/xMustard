package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Claim predicates: the fixed coding vocabulary of PAR-GOV-10.
const (
	PredicateRequires     = "requires"
	PredicateForbids      = "forbids"
	PredicateRunsWith     = "runs_with"
	PredicateConfiguredAs = "configured_as"
	PredicatePinnedTo     = "pinned_to"
	PredicateDeprecatedBy = "deprecated_by"
	PredicateOwnedBy      = "owned_by"
	PredicateReplaces     = "replaces"
)

// Relation kinds between memories.
const (
	RelationSupersedes  = "supersedes"
	RelationDuplicates  = "duplicates"
	RelationRefines     = "refines"
	RelationContradicts = "contradicts"
	RelationMergedInto  = "merged_into"
	RelationReferences  = "references"
)

var (
	validPredicates = set(PredicateRequires, PredicateForbids, PredicateRunsWith, PredicateConfiguredAs,
		PredicatePinnedTo, PredicateDeprecatedBy, PredicateOwnedBy, PredicateReplaces)
	validRelations = set(RelationSupersedes, RelationDuplicates, RelationRefines, RelationContradicts,
		RelationMergedInto, RelationReferences)
)

// Claim is a structured {subject, predicate, object} statement made by an entry.
type Claim struct {
	EntryID     string `json:"entry_id"`
	Revision    int64  `json:"revision"`
	Subject     string `json:"subject"`
	SubjectKind string `json:"subject_kind,omitempty"`
	Predicate   string `json:"predicate"`
	Object      string `json:"object"`
	CreatedAt   string `json:"created_at"`
}

// ClaimInput adds a claim to an entry's served revision.
type ClaimInput struct {
	EntryID     string
	Subject     string
	SubjectKind string
	Predicate   string
	Object      string
}

// ClaimFilter selects claims by entry, or by subject (and predicate) in a workspace.
type ClaimFilter struct {
	WorkspaceID string
	EntryID     string
	Subject     string
	Predicate   string
	ActiveOnly  bool
	Limit       int
}

// ClaimConflict is a deterministic contradiction: two active entries claim different
// objects for the same subject and predicate (tier T1 of PAR-GOV-11).
type ClaimConflict struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	A         Claim  `json:"a"`
	B         Claim  `json:"b"`
}

// Relation is a typed edge between two memories.
type Relation struct {
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	Kind      string `json:"kind"`
	Principal string `json:"principal,omitempty"`
	CreatedAt string `json:"created_at"`
}

// ClaimReader reads claims and relations.
type ClaimReader interface {
	ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error)
	ConflictingClaims(ctx context.Context, workspaceID, subject, predicate string) ([]ClaimConflict, error)
	ListRelations(ctx context.Context, entryID string) ([]Relation, error)
}

// ClaimWriter writes claims and relations. Both are idempotent.
type ClaimWriter interface {
	AddClaim(ctx context.Context, in ClaimInput, actor Actor) (Claim, error)
	AddRelation(ctx context.Context, fromID, toID, kind string, actor Actor) error
}

// ListClaims returns claims ordered by entry and insertion.
func (r *reader) ListClaims(ctx context.Context, f ClaimFilter) ([]Claim, error) {
	var where []string
	var args []any
	switch {
	case f.EntryID != "":
		where = append(where, "c.entry_id = ?")
		args = append(args, f.EntryID)
	case f.WorkspaceID != "" && f.Subject != "":
		where = append(where, "e.workspace_id = ?", "c.subject = ?")
		args = append(args, f.WorkspaceID, f.Subject)
	default:
		return nil, fmt.Errorf("%w: ListClaims needs an entry, or a workspace and a subject", ErrInvalid)
	}
	if f.Predicate != "" {
		where = append(where, "c.predicate = ?")
		args = append(args, f.Predicate)
	}
	if f.ActiveOnly {
		where = append(where, "e.lifecycle = 'active'")
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := r.query(ctx, `SELECT c.entry_id, c.revision, c.subject, c.subject_kind, c.predicate, c.object, c.created_at
		FROM claims c JOIN entries e ON e.id = c.entry_id WHERE `+strings.Join(where, " AND ")+
		` ORDER BY e.pk, c.pk LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var c Claim
		if err := rows.Scan(&c.EntryID, &c.Revision, &c.Subject, &c.SubjectKind, &c.Predicate, &c.Object, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConflictingClaims pairs active claims on the same subject and predicate whose
// objects differ. Predicate "" checks every predicate of the subject.
func (r *reader) ConflictingClaims(ctx context.Context, workspaceID, subject, predicate string) ([]ClaimConflict, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return nil, err
	}
	if subject == "" {
		return nil, fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	pred := ""
	args := []any{subject, workspaceID, workspaceID}
	if predicate != "" {
		pred = " AND a.predicate = ?"
		args = append(args, predicate)
	}
	args = append(args, maxListLimit)
	rows, err := r.query(ctx, `SELECT a.entry_id, a.revision, a.subject, a.subject_kind, a.predicate, a.object, a.created_at,
		b.entry_id, b.revision, b.subject, b.subject_kind, b.predicate, b.object, b.created_at
		FROM claims a
		JOIN claims b ON b.subject = a.subject AND b.predicate = a.predicate AND b.object <> a.object AND b.entry_id > a.entry_id
		JOIN entries ea ON ea.id = a.entry_id
		JOIN entries eb ON eb.id = b.entry_id
		WHERE a.subject = ? AND ea.workspace_id = ? AND eb.workspace_id = ?
		  AND ea.lifecycle = 'active' AND eb.lifecycle = 'active'`+pred+`
		ORDER BY a.predicate, a.entry_id, b.entry_id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaimConflict
	for rows.Next() {
		var c ClaimConflict
		if err := rows.Scan(&c.A.EntryID, &c.A.Revision, &c.A.Subject, &c.A.SubjectKind, &c.A.Predicate, &c.A.Object, &c.A.CreatedAt,
			&c.B.EntryID, &c.B.Revision, &c.B.Subject, &c.B.SubjectKind, &c.B.Predicate, &c.B.Object, &c.B.CreatedAt); err != nil {
			return nil, err
		}
		c.Subject, c.Predicate = c.A.Subject, c.A.Predicate
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListRelations returns the edges touching an entry, in both directions.
func (r *reader) ListRelations(ctx context.Context, entryID string) ([]Relation, error) {
	rows, err := r.query(ctx, `SELECT from_id, to_id, kind, principal, created_at FROM relations
		WHERE from_id = ?1 OR to_id = ?1 ORDER BY pk LIMIT ?2`, entryID, maxListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var rel Relation
		if err := rows.Scan(&rel.FromID, &rel.ToID, &rel.Kind, &rel.Principal, &rel.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

// AddClaim attaches a claim to the entry's served revision.
func (t *txn) AddClaim(ctx context.Context, in ClaimInput, actor Actor) (Claim, error) {
	if err := actor.validate(); err != nil {
		return Claim{}, err
	}
	if !validPredicates[in.Predicate] {
		return Claim{}, fmt.Errorf("%w: predicate %q", ErrInvalid, in.Predicate)
	}
	for _, v := range []struct{ kind, val string }{{"subject", in.Subject}, {"object", in.Object}} {
		if strings.TrimSpace(v.val) == "" || len(v.val) > 1024 || strings.ContainsRune(v.val, 0) {
			return Claim{}, fmt.Errorf("%w: claim %s %q", ErrInvalid, v.kind, v.val)
		}
	}
	e, err := t.liveEntry(ctx, in.EntryID)
	if err != nil {
		return Claim{}, err
	}
	now := t.nowText()
	res, err := t.exec(ctx, `INSERT INTO claims (entry_id, revision, subject, subject_kind, predicate, object, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (entry_id, subject, predicate, object) DO NOTHING`,
		in.EntryID, e.Revision, in.Subject, in.SubjectKind, in.Predicate, in.Object, now)
	if err != nil {
		return Claim{}, err
	}
	if rowsAffected(res) > 0 {
		if err := t.appendEvent(ctx, actor, eventRow{
			WorkspaceID: e.WorkspaceID, EntryID: in.EntryID, Type: EventClaim, Revision: e.Revision,
			Data: map[string]any{"subject": in.Subject, "predicate": in.Predicate, "object": in.Object},
		}); err != nil {
			return Claim{}, err
		}
	}
	var c Claim
	err = t.queryRow(ctx, `SELECT entry_id, revision, subject, subject_kind, predicate, object, created_at FROM claims
		WHERE entry_id = ? AND subject = ? AND predicate = ? AND object = ?`,
		in.EntryID, in.Subject, in.Predicate, in.Object).Scan(&c.EntryID, &c.Revision, &c.Subject, &c.SubjectKind,
		&c.Predicate, &c.Object, &c.CreatedAt)
	return c, err
}

// AddRelation records a typed edge between two entries of one workspace.
func (t *txn) AddRelation(ctx context.Context, fromID, toID, kind string, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	return t.addRelation(ctx, fromID, toID, kind, actor)
}

func (t *txn) addRelation(ctx context.Context, fromID, toID, kind string, actor Actor) error {
	if !validRelations[kind] {
		return fmt.Errorf("%w: relation kind %q", ErrInvalid, kind)
	}
	if fromID == toID {
		return fmt.Errorf("%w: an entry cannot relate to itself", ErrInvalid)
	}
	from, err := t.GetEntry(ctx, fromID)
	if err != nil {
		return err
	}
	to, err := t.GetEntry(ctx, toID)
	if err != nil {
		return err
	}
	if from.WorkspaceID != to.WorkspaceID {
		return fmt.Errorf("%w: %s and %s are in different workspaces", ErrInvalid, fromID, toID)
	}
	if from.Lifecycle == LifecyclePurged || to.Lifecycle == LifecyclePurged {
		return fmt.Errorf("%w: a purged entry takes no new relations", ErrInvalid)
	}
	res, err := t.exec(ctx, `INSERT INTO relations (from_id, to_id, kind, principal, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (from_id, to_id, kind) DO NOTHING`, fromID, toID, kind, strings.TrimSpace(actor.Principal), t.nowText())
	if err != nil {
		return err
	}
	if rowsAffected(res) == 0 {
		return nil
	}
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: from.WorkspaceID, EntryID: fromID, Type: EventRelation, Revision: from.Revision,
		Data: map[string]any{"kind": kind, "to": toID},
	})
}
