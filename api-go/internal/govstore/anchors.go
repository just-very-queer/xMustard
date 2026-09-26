package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Anchor kinds (PAR-RCL-02). Paths are repo-relative.
const (
	AnchorPath       = "path"
	AnchorSymbol     = "symbol"
	AnchorIdentifier = "identifier"
	AnchorCommand    = "command"
	AnchorEnv        = "env"
	AnchorConfig     = "config"
	AnchorErrorCode  = "error_code"
	AnchorURL        = "url"
)

var validAnchorKinds = set(AnchorPath, AnchorSymbol, AnchorIdentifier, AnchorCommand, AnchorEnv, AnchorConfig,
	AnchorErrorCode, AnchorURL)

// Baseline states: a hash, or the path was missing when the baseline was taken (so its
// later appearance is drift too).
const (
	BaselineNone    = "none"
	BaselineHash    = "hash"
	BaselineMissing = "missing"
)

// AnchorInput is an extracted or declared anchor.
type AnchorInput struct {
	Kind      string
	Value     string
	SymbolUID string // resolved against the code index, when known
}

// Anchor is a stored anchor with its drift baseline.
type Anchor struct {
	EntryID        string `json:"entry_id"`
	Ordinal        int64  `json:"ordinal"`
	Kind           string `json:"kind"`
	Value          string `json:"value"`
	Declared       bool   `json:"declared"`
	SymbolUID      string `json:"symbol_uid,omitempty"`
	BaselineState  string `json:"baseline_state"`
	BaselineHash   string `json:"baseline_hash,omitempty"`
	BaselineKind   string `json:"baseline_kind,omitempty"`
	BaselineCommit string `json:"baseline_commit,omitempty"`
	BaselineAt     string `json:"baseline_at,omitempty"`
	StaleSince     string `json:"stale_since,omitempty"`
	StaleCommit    string `json:"stale_commit,omitempty"`
}

// Baseline is the verified-against state of one anchor, captured at promotion.
type Baseline struct {
	Kind         string
	Value        string
	State        string // hash | missing
	Hash         string
	BaselineKind string // file | symbol_body
}

// AnchorRef names one anchor of an entry.
type AnchorRef struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// AnchorQuery looks memories up through the anchor inverted index.
type AnchorQuery struct {
	WorkspaceID string
	Kind        string
	Values      []string
	ServedOnly  bool
	Limit       int
}

// AnchorHit is one memory attached to a queried anchor. Fanout is how many entries
// share that anchor, for the fan-out-damped boost.
type AnchorHit struct {
	EntryID       string `json:"entry_id"`
	Kind          string `json:"kind"`
	Value         string `json:"value"`
	BaselineState string `json:"baseline_state"`
	StaleSince    string `json:"stale_since,omitempty"`
	Fanout        int    `json:"fanout"`
}

// AnchorReader reads anchors.
type AnchorReader interface {
	ListAnchors(ctx context.Context, entryID string) ([]Anchor, error)
	// AnchorsFor returns ListAnchors for many entries at once, keyed by entry id.
	AnchorsFor(ctx context.Context, ids []string) (map[string][]Anchor, error)
	EntriesByAnchor(ctx context.Context, q AnchorQuery) ([]AnchorHit, error)
}

// AnchorWriter writes anchors, baselines and drift observations.
type AnchorWriter interface {
	// ReplaceAnchors swaps an entry's anchor set, for example after an edit.
	ReplaceAnchors(ctx context.Context, entryID string, paths []string, anchors []AnchorInput, actor Actor) error
	// SetBaselines records the verified-against state of anchors. An anchor not yet
	// present is added as undeclared.
	SetBaselines(ctx context.Context, entryID string, baselines []Baseline, commit string, actor Actor) error
	// MarkStale persists drift (PAR-PROV-03): it flags the anchors and the entry and
	// appends one stale_observed event for the anchors that were not stale yet.
	MarkStale(ctx context.Context, entryID string, refs []AnchorRef, commit string, actor Actor) (int, error)
	// ClearDrift resets drift flags, for example after re-verification.
	ClearDrift(ctx context.Context, entryID string, actor Actor) error
}

// CleanPaths trims, strips a leading "./", drops empties and de-duplicates, keeping
// order. It matches the legacy store's cleanPaths.
func CleanPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = strings.TrimPrefix(strings.TrimSpace(p), "./")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func validateAnchor(a AnchorInput) error {
	if !validAnchorKinds[a.Kind] {
		return fmt.Errorf("%w: anchor kind %q", ErrInvalid, a.Kind)
	}
	if a.Value == "" || len(a.Value) > maxPathLen || strings.ContainsRune(a.Value, 0) {
		return fmt.Errorf("%w: anchor value %q", ErrInvalid, a.Value)
	}
	return nil
}

// insertAnchors adds paths then other anchors in order and returns the text the
// search index holds for them.
func (t *txn) insertAnchors(ctx context.Context, entryID string, paths []string, anchors []AnchorInput) (string, error) {
	all := make([]AnchorInput, 0, len(paths)+len(anchors))
	for _, p := range CleanPaths(paths) {
		all = append(all, AnchorInput{Kind: AnchorPath, Value: p})
	}
	all = append(all, anchors...)
	seen := map[AnchorRef]bool{}
	values := make([]string, 0, len(all))
	var ordinal int64
	for _, a := range all {
		if err := validateAnchor(a); err != nil {
			return "", err
		}
		ref := AnchorRef{a.Kind, a.Value}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if _, err := t.exec(ctx, `INSERT INTO anchors (entry_id, ordinal, kind, value, declared, symbol_uid)
			VALUES (?, ?, ?, ?, 1, ?)`, entryID, ordinal, a.Kind, a.Value, a.SymbolUID); err != nil {
			return "", err
		}
		ordinal++
		values = append(values, a.Value)
	}
	return strings.Join(values, " "), nil
}

const anchorCols = `entry_id, ordinal, kind, value, declared, symbol_uid, baseline_state, baseline_hash, baseline_kind,
	baseline_commit, coalesce(baseline_at, ''), coalesce(stale_since, ''), stale_commit`

// ListAnchors returns an entry's anchors in order.
func (r *reader) ListAnchors(ctx context.Context, entryID string) ([]Anchor, error) {
	rows, err := r.query(ctx, "SELECT "+anchorCols+" FROM anchors WHERE entry_id = ? ORDER BY ordinal, pk", entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Anchor
	for rows.Next() {
		a, err := scanAnchor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAnchor(s scanner) (Anchor, error) {
	var a Anchor
	var declared int
	err := s.Scan(&a.EntryID, &a.Ordinal, &a.Kind, &a.Value, &declared, &a.SymbolUID, &a.BaselineState,
		&a.BaselineHash, &a.BaselineKind, &a.BaselineCommit, &a.BaselineAt, &a.StaleSince, &a.StaleCommit)
	a.Declared = declared == 1
	return a, err
}

// AnchorsFor reads the anchors of many entries in bounded batches.
func (r *reader) AnchorsFor(ctx context.Context, ids []string) (map[string][]Anchor, error) {
	out := make(map[string][]Anchor, len(ids))
	for start := 0; start < len(ids); start += maxInArgs {
		chunk := ids[start:min(start+maxInArgs, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := r.query(ctx, "SELECT "+anchorCols+" FROM anchors WHERE entry_id IN ("+placeholders(len(chunk))+
			") ORDER BY entry_id, ordinal, pk", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			a, err := scanAnchor(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[a.EntryID] = append(out[a.EntryID], a)
		}
		err = rows.Err()
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EntriesByAnchor finds the memories attached to any of q.Values.
func (r *reader) EntriesByAnchor(ctx context.Context, q AnchorQuery) ([]AnchorHit, error) {
	if err := validID("workspace", q.WorkspaceID); err != nil {
		return nil, err
	}
	if !validAnchorKinds[q.Kind] {
		return nil, fmt.Errorf("%w: anchor kind %q", ErrInvalid, q.Kind)
	}
	if len(q.Values) == 0 {
		return nil, nil
	}
	if len(q.Values) > maxInArgs {
		return nil, fmt.Errorf("%w: at most %d anchor values per query", ErrInvalid, maxInArgs)
	}
	args := []any{q.Kind}
	for _, v := range q.Values {
		args = append(args, v)
	}
	args = append(args, q.WorkspaceID)
	served := ""
	if q.ServedOnly {
		served = " AND e.promoted = 1 AND e.lifecycle = 'active' AND (e.expires_at IS NULL OR e.expires_at > ?)"
		args = append(args, r.nowText())
	}
	args = append(args, clampLimit(q.Limit))
	rows, err := r.query(ctx, `SELECT a.entry_id, a.kind, a.value, a.baseline_state, coalesce(a.stale_since, ''),
		count(*) OVER (PARTITION BY a.kind, a.value)
		FROM anchors a JOIN entries e ON e.id = a.entry_id
		WHERE a.kind = ? AND a.value IN (`+placeholders(len(q.Values))+`) AND e.workspace_id = ?`+served+`
		ORDER BY a.value, e.pk LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AnchorHit
	for rows.Next() {
		var h AnchorHit
		if err := rows.Scan(&h.EntryID, &h.Kind, &h.Value, &h.BaselineState, &h.StaleSince, &h.Fanout); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (t *txn) liveEntry(ctx context.Context, id string) (Entry, error) {
	e, err := t.GetEntry(ctx, id)
	if err != nil {
		return Entry{}, err
	}
	if e.Lifecycle == LifecyclePurged {
		return Entry{}, fmt.Errorf("%w: entry %s is purged", ErrInvalid, id)
	}
	return e, nil
}

// ReplaceAnchors swaps the anchor set. Baselines of anchors are dropped with them.
func (t *txn) ReplaceAnchors(ctx context.Context, entryID string, paths []string, anchors []AnchorInput, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	e, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return err
	}
	if _, err := t.exec(ctx, "DELETE FROM anchors WHERE entry_id = ?", entryID); err != nil {
		return err
	}
	if _, err := t.insertAnchors(ctx, entryID, paths, anchors); err != nil {
		return err
	}
	if err := t.refreshFTS(ctx, entryID); err != nil {
		return err
	}
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: entryID, Type: EventAnchors, Revision: e.Revision,
		Data: map[string]any{"paths": len(CleanPaths(paths)), "anchors": len(anchors)},
	})
}

// SetBaselines records the verified-against state of anchors.
func (t *txn) SetBaselines(ctx context.Context, entryID string, baselines []Baseline, commit string, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	e, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return err
	}
	return t.setBaselines(ctx, e, baselines, commit, t.nowText(), actor)
}

func (t *txn) setBaselines(ctx context.Context, e Entry, baselines []Baseline, commit, at string, actor Actor) error {
	if len(baselines) == 0 {
		return nil
	}
	added := false
	for _, b := range baselines {
		if err := validateAnchor(AnchorInput{Kind: b.Kind, Value: b.Value}); err != nil {
			return err
		}
		switch {
		case b.State == BaselineHash && b.Hash != "":
		case b.State == BaselineMissing && b.Hash == "":
		default:
			return fmt.Errorf("%w: baseline for %s %q needs state hash (with a hash) or missing", ErrInvalid, b.Kind, b.Value)
		}
		res, err := t.exec(ctx, `UPDATE anchors SET baseline_state = ?, baseline_hash = ?, baseline_kind = ?,
			baseline_commit = ?, baseline_at = ?, stale_since = NULL, stale_commit = ''
			WHERE entry_id = ? AND kind = ? AND value = ?`,
			b.State, b.Hash, b.BaselineKind, commit, at, e.ID, b.Kind, b.Value)
		if err != nil {
			return err
		}
		if rowsAffected(res) > 0 {
			continue
		}
		if _, err := t.exec(ctx, `INSERT INTO anchors (entry_id, ordinal, kind, value, declared, baseline_state,
			baseline_hash, baseline_kind, baseline_commit, baseline_at)
			VALUES (?, (SELECT coalesce(max(ordinal), -1) + 1 FROM anchors WHERE entry_id = ?), ?, ?, 0, ?, ?, ?, ?, ?)`,
			e.ID, e.ID, b.Kind, b.Value, b.State, b.Hash, b.BaselineKind, commit, at); err != nil {
			return err
		}
		added = true
	}
	if added {
		if err := t.refreshFTS(ctx, e.ID); err != nil {
			return err
		}
	}
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: e.ID, Type: EventBaseline, Revision: e.Revision,
		NewDigest: e.ContentDigest, Data: map[string]any{"commit": commit, "anchors": len(baselines)},
	})
}

// MarkStale flags drifted anchors and the entry, once per anchor.
func (t *txn) MarkStale(ctx context.Context, entryID string, refs []AnchorRef, commit string, actor Actor) (int, error) {
	if err := actor.validate(); err != nil {
		return 0, err
	}
	e, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return 0, err
	}
	now := t.nowText()
	var fresh []AnchorRef
	for _, ref := range refs {
		res, err := t.exec(ctx, `UPDATE anchors SET stale_since = ?, stale_commit = ?
			WHERE entry_id = ? AND kind = ? AND value = ? AND stale_since IS NULL`, now, commit, entryID, ref.Kind, ref.Value)
		if err != nil {
			return 0, err
		}
		if rowsAffected(res) > 0 {
			fresh = append(fresh, ref)
		}
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	if _, err := t.exec(ctx, "UPDATE entries SET needs_reverify = 1, stale_since = coalesce(stale_since, ?), updated_at = ? WHERE id = ?",
		now, now, entryID); err != nil {
		return 0, err
	}
	if err := t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: entryID, Type: EventStaleObserved, Revision: e.Revision,
		OldDigest: e.ContentDigest, Data: map[string]any{"commit": commit, "anchors": fresh},
	}); err != nil {
		return 0, err
	}
	return len(fresh), nil
}

// ClearDrift resets drift flags on the entry and its anchors.
func (t *txn) ClearDrift(ctx context.Context, entryID string, actor Actor) error {
	if err := actor.validate(); err != nil {
		return err
	}
	e, err := t.liveEntry(ctx, entryID)
	if err != nil {
		return err
	}
	res1, err := t.exec(ctx, "UPDATE anchors SET stale_since = NULL, stale_commit = '' WHERE entry_id = ? AND stale_since IS NOT NULL", entryID)
	if err != nil {
		return err
	}
	res2, err := t.exec(ctx, "UPDATE entries SET needs_reverify = 0, stale_since = NULL, updated_at = ? WHERE id = ? AND needs_reverify = 1",
		t.nowText(), entryID)
	if err != nil {
		return err
	}
	if rowsAffected(res1)+rowsAffected(res2) == 0 {
		return nil
	}
	return t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: entryID, Type: EventDriftCleared, Revision: e.Revision,
		NewDigest: e.ContentDigest, Data: map[string]any{"anchors": rowsAffected(res1)},
	})
}
