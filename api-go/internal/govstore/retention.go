package govstore

import (
	"context"
	"fmt"
	"time"
)

// Retention (PAR-STORE-04). Everything is deleted or compacted in bounded batches by
// indexed range, so no path reads a whole log and the write lock is never held long.
// The memory history keeps its tombstones: events are never deleted, and a dropped
// revision keeps its digest.

// KeepClass is the retention class that no policy deletes.
const KeepClass = "keep"

// RetentionPolicy sets a maximum age per kind of row. A zero duration keeps rows of
// that kind forever.
type RetentionPolicy struct {
	// SessionEvents maps a retention class to its maximum age.
	SessionEvents map[string]time.Duration
	// Sessions removes sessions last seen before the cutoff once they have no events.
	Sessions time.Duration
	// Deliveries is the maximum age of delivery-log rows.
	Deliveries time.Duration
	// EvidenceGrace keeps evidence metadata this long after the original expired.
	EvidenceGrace time.Duration
	// FinishedJobs is how long done, failed and cancelled jobs are kept.
	FinishedJobs time.Duration
	// RevisionContent drops the text of revisions that are no longer served nor
	// pending (older accepted, rejected, withdrawn) once older than this. Their
	// digests stay. Zero, the default, keeps every content version retrievable.
	RevisionContent time.Duration
	// BatchSize is the number of rows per transaction (default 500).
	BatchSize int
	// Vacuum returns freed pages to the file system and truncates the WAL afterwards.
	// When the pass removed transcript rows or revision content, it first rewrites the
	// matching search index ('optimize'): a contentless FTS5 delete only tombstones a
	// row, and its terms would otherwise stay in the file.
	Vacuum bool
}

// DefaultRetentionPolicy is the default operator policy.
func DefaultRetentionPolicy() RetentionPolicy {
	const day = 24 * time.Hour
	return RetentionPolicy{
		SessionEvents: map[string]time.Duration{"session": 30 * day, "transcript": 90 * day},
		Sessions:      90 * day,
		Deliveries:    30 * day,
		EvidenceGrace: 7 * day,
		FinishedJobs:  30 * day,
		BatchSize:     500,
	}
}

// RetentionReport counts what one pass removed.
type RetentionReport struct {
	SessionEvents   int64 `json:"session_events"`
	Sessions        int64 `json:"sessions"`
	Deliveries      int64 `json:"deliveries"`
	Evidence        int64 `json:"evidence"`
	Jobs            int64 `json:"jobs"`
	RevisionContent int64 `json:"revision_content"`
}

// ApplyRetention runs one retention pass.
func (s *SQLStore) ApplyRetention(ctx context.Context, p RetentionPolicy) (RetentionReport, error) {
	var rep RetentionReport
	if s.closed.Load() {
		return rep, ErrClosed
	}
	batch := p.BatchSize
	if batch <= 0 {
		batch = 500
	}
	now := s.opts.Now()
	cutoff := func(age time.Duration) string { return canonTime(now.Add(-age)) }
	type step struct {
		counter *int64
		age     time.Duration
		query   string
		args    func() []any
	}
	var steps []step
	for class, age := range p.SessionEvents {
		if class == KeepClass || age <= 0 {
			continue
		}
		steps = append(steps, step{&rep.SessionEvents, age,
			`DELETE FROM session_events WHERE seq IN (SELECT seq FROM session_events
				WHERE retention_class = ? AND at < ? ORDER BY at LIMIT ?)`,
			func() []any { return []any{class, cutoff(age), batch} }})
	}
	steps = append(steps,
		step{&rep.Sessions, p.Sessions,
			`DELETE FROM sessions WHERE pk IN (SELECT s.pk FROM sessions s
				WHERE s.retention_class <> 'keep' AND s.last_seen_at < ?
				  AND NOT EXISTS (SELECT 1 FROM session_events e WHERE e.session_id = s.id) LIMIT ?)`,
			func() []any { return []any{cutoff(p.Sessions), batch} }},
		step{&rep.Deliveries, p.Deliveries,
			`DELETE FROM deliveries WHERE seq IN (SELECT seq FROM deliveries
				WHERE retention_class <> 'keep' AND at < ? ORDER BY at LIMIT ?)`,
			func() []any { return []any{cutoff(p.Deliveries), batch} }},
		step{&rep.Evidence, p.EvidenceGrace,
			`DELETE FROM evidence_meta WHERE pk IN (SELECT pk FROM evidence_meta
				WHERE retention_class <> 'keep' AND expires_at < ? ORDER BY expires_at LIMIT ?)`,
			func() []any { return []any{cutoff(p.EvidenceGrace), batch} }},
		step{&rep.Jobs, p.FinishedJobs,
			`DELETE FROM jobs WHERE pk IN (SELECT pk FROM jobs
				WHERE finished_at IS NOT NULL AND finished_at < ? AND state IN ('done', 'failed', 'cancelled') LIMIT ?)`,
			func() []any { return []any{cutoff(p.FinishedJobs), batch} }},
		step{&rep.RevisionContent, p.RevisionContent,
			`UPDATE revisions SET content = NULL, content_dropped_at = ?1, content_dropped_reason = 'retention'
				WHERE pk IN (SELECT r.pk FROM revisions r JOIN entries e ON e.id = r.entry_id
					WHERE r.content IS NOT NULL AND r.created_at < ?2 AND r.revision <> e.revision
					  AND r.state <> 'pending' AND e.retention_class <> 'keep' LIMIT ?3)`,
			func() []any { return []any{canonTime(now), cutoff(p.RevisionContent), batch} }},
	)
	for _, st := range steps {
		if st.age <= 0 {
			continue
		}
		for {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			var n int64
			err := s.Update(ctx, func(tx Tx) error {
				res, err := tx.(*txn).exec(ctx, st.query, st.args()...)
				n = rowsAffected(res)
				return err
			})
			if err != nil {
				return rep, fmt.Errorf("retention: %w", err)
			}
			*st.counter += n
			if n < int64(batch) {
				break
			}
		}
	}
	if p.Vacuum {
		for _, ix := range []struct {
			table   string
			removed int64
		}{{"transcript_fts", rep.SessionEvents}, {"memory_fts", rep.RevisionContent}} {
			if ix.removed == 0 {
				continue
			}
			if err := s.Update(ctx, func(tx Tx) error {
				_, err := tx.(*txn).exec(ctx, "INSERT INTO "+ix.table+" ("+ix.table+") VALUES ('optimize')")
				return err
			}); err != nil {
				return rep, fmt.Errorf("retention: optimize %s: %w", ix.table, err)
			}
		}
		if _, err := s.writer.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
			return rep, mapErr(err)
		}
		if err := s.Checkpoint(ctx); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
