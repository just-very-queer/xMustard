package govstore

import (
	"context"
)

// RankEntry is the lean head state recall ranks a served entry on: no content, no
// vote rows, only what scoring reads. ContentDigest names the served revision, so the
// caller can bind content it loads later to exactly the version it ranked.
type RankEntry struct {
	ID               string
	Title            string
	VerificationMode string
	UpdatedAt        string
	ContentDigest    string
	SearchTokens     []string
	// Approvals counts distinct principals approving the served revision in the
	// current vote epoch (any principal: a ranking signal, not a trust label).
	Approvals int
	// Paths are the entry's declared path anchors, in order.
	Paths []string
	// Baselined: at least one path anchor carries a drift baseline.
	Baselined bool
}

// RankingReader reads the ranking view of a workspace.
type RankingReader interface {
	// ServedRanking returns every served entry of the workspace in insertion order,
	// in three queries whatever the entry count.
	ServedRanking(ctx context.Context, workspaceID string) ([]RankEntry, error)
}

const servedWhere = `e.workspace_id = ? AND e.promoted = 1 AND e.lifecycle = 'active'
	AND (e.expires_at IS NULL OR e.expires_at > ?)`

// ServedRanking reads the ranking view: the lean entry columns, the approval counts
// grouped in SQL, and the path anchors with their baseline flags.
func (r *reader) ServedRanking(ctx context.Context, workspaceID string) ([]RankEntry, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return nil, err
	}
	now := r.nowText()
	rows, err := r.query(ctx, `SELECT e.id, e.title, e.verification_mode, e.updated_at, e.content_digest, e.search_tokens
		FROM entries e WHERE `+servedWhere+` ORDER BY e.pk`, workspaceID, now)
	if err != nil {
		return nil, err
	}
	var out []RankEntry
	index := map[string]int{}
	for rows.Next() {
		var e RankEntry
		var tokens string
		if err := rows.Scan(&e.ID, &e.Title, &e.VerificationMode, &e.UpdatedAt, &e.ContentDigest, &tokens); err != nil {
			rows.Close()
			return nil, err
		}
		if err := decodeJSONColumn(tokens, &e.SearchTokens); err != nil {
			rows.Close()
			return nil, err
		}
		index[e.ID] = len(out)
		out = append(out, e)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = r.query(ctx, `SELECT v.entry_id, count(*) FROM votes v JOIN entries e ON e.id = v.entry_id
		AND v.revision = e.revision AND v.epoch = e.vote_epoch
		WHERE `+servedWhere+` AND v.verdict = 'approve' GROUP BY v.entry_id`, workspaceID, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Approvals = n
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = r.query(ctx, `SELECT a.entry_id, a.value, a.declared, a.baseline_state <> 'none'
		FROM anchors a JOIN entries e ON e.id = a.entry_id
		WHERE `+servedWhere+` AND a.kind = 'path' ORDER BY a.entry_id, a.ordinal, a.pk`, workspaceID, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, value string
		var declared, baselined bool
		if err := rows.Scan(&id, &value, &declared, &baselined); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		if declared {
			out[i].Paths = append(out[i].Paths, value)
		}
		out[i].Baselined = out[i].Baselined || baselined
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return out, nil
}

// closeRows reports a failed step instead of a partial result, then closes.
func closeRows(rows *resultRows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
