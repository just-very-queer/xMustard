package govstore

import (
	"context"
	"fmt"
	"strings"
)

// Rank states: which part of a workspace a ranking view reads. Served is what recall
// returns by default; the others are opt-in views of the same entries.
const (
	RankServed     = "served"     // promoted, active, unexpired
	RankPending    = "pending"    // an active proposal awaiting verification
	RankSuperseded = "superseded" // once promoted, then replaced by a newer promoted entry
	RankExpired    = "expired"    // promoted and active, past its expiry
)

// rankStateCases classify an entry into its rank state; ?1 is now. The conditions
// exclude each other, so a view ORs the ones it reads instead of evaluating the CASE
// per row. An entry matching none (rejected, retracted, merged, archived, purged, or
// superseded before it was ever promoted) is never ranked: a pending or rejected
// proposal stays unverified text after a promoted entry replaces it.
var rankStateCases = []struct{ state, when string }{
	{RankServed, `e.promoted = 1 AND e.lifecycle = 'active' AND (e.expires_at IS NULL OR e.expires_at > ?1)`},
	{RankExpired, `e.promoted = 1 AND e.lifecycle = 'active' AND e.expires_at <= ?1`},
	{RankPending, `e.promoted = 0 AND e.status = 'pending' AND e.lifecycle = 'active' AND (e.expires_at IS NULL OR e.expires_at > ?1)`},
	{RankSuperseded, `e.promoted = 1 AND e.lifecycle = 'superseded'`},
}

// rankStateExpr is the SQL CASE that labels an entry with its rank state.
var rankStateExpr = func() string {
	var b strings.Builder
	b.WriteString("CASE")
	for _, c := range rankStateCases {
		fmt.Fprintf(&b, " WHEN %s THEN '%s'", c.when, c.state)
	}
	b.WriteString(" ELSE '' END")
	return b.String()
}()

// RankEntry is the lean head state recall ranks an entry on: no content, no vote rows,
// only what scoring and filtering read. ContentDigest names the served revision, so the
// caller can bind content it loads later to exactly the version it ranked.
type RankEntry struct {
	ID               string
	Title            string
	VerificationMode string
	UpdatedAt        string
	ContentDigest    string
	// State is the rank state (RankServed, RankPending, ...).
	State  string
	Status string
	Kind   string
	Topic  string
	Tier   string
	Tags   []string
	Author string
	// AuthorOwner is the owner recorded for the author at write time ("" for writes
	// that recorded none).
	AuthorOwner           string
	RequiredVerifications int
	SupersededBy          string
	// Approvals counts distinct principals approving the served revision in the
	// current vote epoch (any principal: a ranking signal, not a trust label).
	Approvals int
	// PeerApprovals leaves out the author and the open-mode identity, as promotion does.
	PeerApprovals int
	// ByCaller: the caller authored the entry. VotedByCaller: the caller cast a verdict
	// on the served revision in the current epoch.
	ByCaller      bool
	VotedByCaller bool
	// Helpful, Misleading and StaleHarm count the latest outcome of each principal on
	// the served revision (PAR-GOV-16).
	Helpful, Misleading, StaleHarm int
	// Paths are the entry's declared path anchors, in order.
	Paths []string
	// Baselined: at least one path anchor carries a drift baseline.
	Baselined bool
}

// RankQuery selects a ranking view: the rank states to read and the caller the
// ByCaller and VotedByCaller flags are computed for.
type RankQuery struct {
	WorkspaceID string
	States      []string // default: served only
	Caller      string
}

// RankingReader reads the ranking view of a workspace.
type RankingReader interface {
	// ServedRanking returns every served entry of the workspace in insertion order.
	ServedRanking(ctx context.Context, workspaceID string) ([]RankEntry, error)
	// Ranking returns the entries in q's rank states in insertion order, in four
	// queries whatever the entry count.
	Ranking(ctx context.Context, q RankQuery) ([]RankEntry, error)
}

// ServedRanking is Ranking over the served state.
func (r *reader) ServedRanking(ctx context.Context, workspaceID string) ([]RankEntry, error) {
	return r.Ranking(ctx, RankQuery{WorkspaceID: workspaceID})
}

// Ranking reads the ranking view: the lean entry columns, the vote and outcome counts
// grouped in SQL, and the path anchors with their baseline flags.
func (r *reader) Ranking(ctx context.Context, q RankQuery) ([]RankEntry, error) {
	if err := validID("workspace", q.WorkspaceID); err != nil {
		return nil, err
	}
	states := q.States
	if len(states) == 0 {
		states = []string{RankServed}
	}
	// the conditions are spliced into SQL, so only the fixed states pass
	whens := make([]string, len(states))
	for i, s := range states {
		when, ok := rankStateWhen(s)
		if !ok {
			return nil, fmt.Errorf("%w: rank state %q", ErrInvalid, s)
		}
		whens[i] = "(" + when + ")"
	}
	// ?1 now, ?2 workspace, ?3 caller key, ?4 open-mode identity. SQLite counts the
	// parameters up to the highest index a statement uses, so each query passes a
	// prefix: where, the outcome and the anchor queries use ?1 and ?2 only.
	where := `e.workspace_id = ?2 AND (` + strings.Join(whens, " OR ") + `)`
	args := []any{r.nowText(), q.WorkspaceID, principalKey(q.Caller), OpenModeIdentity}
	rows, err := r.query(ctx, `SELECT e.id, e.title, e.verification_mode, e.updated_at, e.content_digest,
		`+rankStateExpr+`, e.status, e.kind, e.topic, e.tier, e.tags, e.source, e.source_owner, e.required_verifications,
		coalesce(e.superseded_by, ''), e.source_key = ?3 AND ?3 <> ''
		FROM entries e WHERE `+where+` ORDER BY e.pk`, args[:3]...)
	if err != nil {
		return nil, err
	}
	var out []RankEntry
	index := map[string]int{}
	var e RankEntry
	var tags string
	err = eachRow(rows, func() {
		index[e.ID] = len(out)
		out = append(out, e)
	}, func() error {
		e = RankEntry{}
		if err := rows.Scan(&e.ID, &e.Title, &e.VerificationMode, &e.UpdatedAt, &e.ContentDigest, &e.State,
			&e.Status, &e.Kind, &e.Topic, &e.Tier, &tags, &e.Author, &e.AuthorOwner, &e.RequiredVerifications, &e.SupersededBy, &e.ByCaller); err != nil {
			return err
		}
		return decodeJSONColumn(tags, &e.Tags)
	})
	if err != nil {
		return nil, err
	}
	var id string
	var approvals, peers int
	var voted bool
	rows, err = r.query(ctx, `SELECT v.entry_id, coalesce(sum(v.verdict = 'approve'), 0),
		coalesce(sum(v.verdict = 'approve' AND v.principal_key NOT IN (e.source_key, ?4)), 0),
		coalesce(max(v.principal_key = ?3), 0)
		FROM votes v JOIN entries e ON e.id = v.entry_id AND v.revision = e.revision AND v.epoch = e.vote_epoch
		WHERE `+where+` GROUP BY v.entry_id`, args...)
	if err != nil {
		return nil, err
	}
	if err := eachRow(rows, func() {
		if i, ok := index[id]; ok {
			out[i].Approvals, out[i].PeerApprovals, out[i].VotedByCaller = approvals, peers, voted
		}
	}, func() error { return rows.Scan(&id, &approvals, &peers, &voted) }); err != nil {
		return nil, err
	}
	var helpful, misleading, harm int
	rows, err = r.query(ctx, `SELECT o.entry_id, coalesce(sum(o.outcome = 'helpful'), 0),
		coalesce(sum(o.outcome = 'misleading'), 0), coalesce(sum(o.outcome = 'stale_harm'), 0)
		FROM outcomes o JOIN entries e ON e.id = o.entry_id AND o.revision = e.revision
		WHERE `+where+` GROUP BY o.entry_id`, args[:2]...)
	if err != nil {
		return nil, err
	}
	if err := eachRow(rows, func() {
		if i, ok := index[id]; ok {
			out[i].Helpful, out[i].Misleading, out[i].StaleHarm = helpful, misleading, harm
		}
	}, func() error { return rows.Scan(&id, &helpful, &misleading, &harm) }); err != nil {
		return nil, err
	}
	var value string
	var declared, baselined bool
	rows, err = r.query(ctx, `SELECT a.entry_id, a.value, a.declared, a.baseline_state <> 'none'
		FROM anchors a JOIN entries e ON e.id = a.entry_id
		WHERE `+where+` AND a.kind = 'path' ORDER BY a.entry_id, a.ordinal, a.pk`, args[:2]...)
	if err != nil {
		return nil, err
	}
	if err := eachRow(rows, func() {
		i, ok := index[id]
		if !ok {
			return
		}
		if declared {
			out[i].Paths = append(out[i].Paths, value)
		}
		out[i].Baselined = out[i].Baselined || baselined
	}, func() error { return rows.Scan(&id, &value, &declared, &baselined) }); err != nil {
		return nil, err
	}
	return out, nil
}

// rankStateWhen is the condition that selects state, or false for an unknown state.
func rankStateWhen(state string) (string, bool) {
	for _, c := range rankStateCases {
		if c.state == state {
			return c.when, true
		}
	}
	return "", false
}

// eachRow scans every row and hands it to use, then closes. A failed step is reported
// instead of a partial result.
func eachRow(rows *resultRows, use func(), scan func() error) error {
	for rows.Next() {
		if err := scan(); err != nil {
			rows.Close()
			return err
		}
		use()
	}
	return closeRows(rows)
}

// closeRows reports a failed step instead of a partial result, then closes.
func closeRows(rows *resultRows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
