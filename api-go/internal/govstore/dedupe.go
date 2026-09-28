package govstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"xmustard/api-go/internal/dedupe"
)

// Duplicate detection storage (WS-27, PAR-GOV-03). The postings of every entry's served
// content live in dedupe_keys, so finding candidates is an indexed read: nothing about
// the memories stays resident. Quotas read the counts they gate from the store too.

// maxCorroborationEvidence bounds the evidence handles kept per principal and entry.
const maxCorroborationEvidence = 8

// DedupeQuery looks up the active entries of a workspace whose served content shares a
// posting key with a text.
type DedupeQuery struct {
	WorkspaceID string
	Keys        dedupe.Keys
	Limit       int
}

// DedupeHit is one candidate: whether it shares the exact key, and how many band keys.
type DedupeHit struct {
	EntryID string `json:"entry_id"`
	Exact   bool   `json:"exact"`
	Bands   int    `json:"bands"`
}

// Corroboration is one principal's restatements of an entry.
type Corroboration struct {
	EntryID   string   `json:"entry_id"`
	Principal string   `json:"principal"`
	Count     int      `json:"count"`
	Evidence  []string `json:"evidence,omitempty"`
	FirstAt   string   `json:"first_at"`
	LastAt    string   `json:"last_at"`
}

// MemoryCounts are the counts the write quotas gate on.
type MemoryCounts struct {
	// Active counts the workspace's active entries, pending or promoted.
	Active int `json:"active"`
	// Pending counts the principal's own pending, active proposals.
	Pending int `json:"pending"`
}

// DedupeReader reads duplicate candidates, corroborations and quota counts.
type DedupeReader interface {
	DedupeCandidates(ctx context.Context, q DedupeQuery) ([]DedupeHit, error)
	Corroborations(ctx context.Context, entryID string) ([]Corroboration, error)
	MemoryCounts(ctx context.Context, workspaceID, principal string) (MemoryCounts, error)
}

// DedupeWriter records corroborations and fills in postings of older entries.
type DedupeWriter interface {
	// Corroborate records actor restating a served entry. It reports whether the
	// history took an event: the principal's first restatement, or new evidence.
	Corroborate(ctx context.Context, entryID string, actor Actor) (Corroboration, bool, error)
	// BackfillDedupeKeys computes the postings of up to max entries that existed before
	// migration 4 and reports how many it did.
	BackfillDedupeKeys(ctx context.Context, max int) (int, error)
}

// putDedupeKeys replaces an entry's postings with those of content.
func (t *txn) putDedupeKeys(ctx context.Context, entryPK int64, workspaceID, content string) error {
	if _, err := t.exec(ctx, "DELETE FROM dedupe_keys WHERE entry_pk = ?", entryPK); err != nil {
		return err
	}
	for _, k := range dedupe.KeysOf(workspaceID, dedupe.NewProfile(content)).All() {
		if _, err := t.exec(ctx, "INSERT INTO dedupe_keys (key, entry_pk) VALUES (?, ?) ON CONFLICT DO NOTHING",
			k, entryPK); err != nil {
			return err
		}
	}
	return nil
}

// maxPostingsPerKey bounds the postings one key contributes to a lookup, newest first:
// a band thousands of templated memories share says little about any one of them, and
// reading all of it would make a lookup cost grow with the store.
const maxPostingsPerKey = 64

// DedupeCandidates returns active entries sharing a posting key, those sharing the
// exact key first, then by shared bands, then the newest. Each key contributes at most
// maxPostingsPerKey postings, so a lookup reads at most 9 short index ranges.
func (r *reader) DedupeCandidates(ctx context.Context, q DedupeQuery) ([]DedupeHit, error) {
	if err := validID("workspace", q.WorkspaceID); err != nil {
		return nil, err
	}
	type hit struct {
		exact bool
		bands int
	}
	hits := map[int64]*hit{}
	for i, k := range q.Keys.All() {
		rows, err := r.query(ctx, "SELECT entry_pk FROM dedupe_keys WHERE key = ? ORDER BY entry_pk DESC LIMIT ?",
			k, maxPostingsPerKey)
		if err != nil {
			return nil, err
		}
		var pk int64
		if err := eachRow(rows, func() {
			h := hits[pk]
			if h == nil {
				h = &hit{}
				hits[pk] = h
			}
			h.exact, h.bands = h.exact || i == 0, h.bands+min(i, 1)
		}, func() error { return rows.Scan(&pk) }); err != nil {
			return nil, err
		}
	}
	pks := slices.Collect(maps.Keys(hits))
	slices.SortFunc(pks, func(a, b int64) int {
		x, y := hits[a], hits[b]
		return cmp.Or(-cmp.Compare(boolInt(x.exact), boolInt(y.exact)), -cmp.Compare(x.bands, y.bands), -cmp.Compare(a, b))
	})
	ids, err := r.activeIDs(ctx, q.WorkspaceID, pks)
	if err != nil {
		return nil, err
	}
	out := make([]DedupeHit, 0, min(len(pks), clampLimit(q.Limit)))
	for _, pk := range pks {
		if id, ok := ids[pk]; ok && len(out) < clampLimit(q.Limit) {
			out = append(out, DedupeHit{EntryID: id, Exact: hits[pk].exact, Bands: hits[pk].bands})
		}
	}
	return out, nil
}

// activeIDs maps the pks of the workspace's active entries among pks to their ids. The
// rows are read by rowid and filtered here: a workspace or lifecycle condition in SQL
// would let the planner walk the whole workspace's index instead.
func (r *reader) activeIDs(ctx context.Context, workspaceID string, pks []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(pks))
	for start := 0; start < len(pks); start += maxInArgs {
		chunk := pks[start:min(start+maxInArgs, len(pks))]
		args := make([]any, 0, len(chunk))
		for _, pk := range chunk {
			args = append(args, pk)
		}
		rows, err := r.query(ctx, "SELECT pk, id, workspace_id, lifecycle FROM entries WHERE pk IN ("+
			placeholders(len(chunk))+")", args...)
		if err != nil {
			return nil, err
		}
		var pk int64
		var id, ws, lifecycle string
		if err := eachRow(rows, func() {
			if ws == workspaceID && lifecycle == LifecycleActive {
				out[pk] = id
			}
		}, func() error { return rows.Scan(&pk, &id, &ws, &lifecycle) }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Corroborate records one restatement of a served entry by actor.
func (t *txn) Corroborate(ctx context.Context, entryID string, actor Actor) (Corroboration, bool, error) {
	if err := actor.validate(); err != nil {
		return Corroboration{}, false, err
	}
	e, err := t.GetEntry(ctx, entryID)
	if err != nil {
		return Corroboration{}, false, err
	}
	if !e.Served(t.now()) {
		return Corroboration{}, false, fmt.Errorf("%w: only a served entry is corroborated; %s is not", ErrInvalid, entryID)
	}
	key, now := principalKey(actor.Principal), t.nowText()
	prev, err := t.corroboration(ctx, entryID, key)
	first := errors.Is(err, ErrNotFound)
	if err != nil && !first {
		return Corroboration{}, false, err
	}
	evidence := prev.Evidence
	for _, h := range actor.Evidence {
		if len(evidence) < maxCorroborationEvidence && h != "" && !slices.Contains(evidence, h) {
			evidence = append(evidence, h)
		}
	}
	grew := len(evidence) > len(prev.Evidence)
	encoded, err := encodeStrings(evidence)
	if err != nil {
		return Corroboration{}, false, err
	}
	if _, err := t.exec(ctx, `INSERT INTO corroborations (entry_id, principal_key, principal, evidence, first_at, last_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (entry_id, principal_key)
		DO UPDATE SET count = count + 1, evidence = excluded.evidence, last_at = excluded.last_at`,
		entryID, key, strings.TrimSpace(actor.Principal), encoded, now, now); err != nil {
		return Corroboration{}, false, err
	}
	out, err := t.corroboration(ctx, entryID, key)
	if err != nil || !(first || grew) {
		return out, false, err
	}
	return out, true, t.appendEvent(ctx, actor, eventRow{
		WorkspaceID: e.WorkspaceID, EntryID: entryID, Type: EventCorroborate, Revision: e.Revision,
		NewDigest: e.ContentDigest, Data: map[string]any{"count": out.Count, "evidence": len(out.Evidence)},
	})
}

const corroborationCols = "entry_id, principal, count, evidence, first_at, last_at"

func scanCorroboration(s rowScanner) (Corroboration, error) {
	var c Corroboration
	var evidence string
	if err := s.Scan(&c.EntryID, &c.Principal, &c.Count, &evidence, &c.FirstAt, &c.LastAt); err != nil {
		return Corroboration{}, err
	}
	return c, decodeJSONColumn(evidence, &c.Evidence)
}

func (r *reader) corroboration(ctx context.Context, entryID, key string) (Corroboration, error) {
	return scanCorroboration(r.queryRow(ctx, "SELECT "+corroborationCols+
		" FROM corroborations WHERE entry_id = ? AND principal_key = ?", entryID, key))
}

// Corroborations lists an entry's corroborating principals, the earliest first.
func (r *reader) Corroborations(ctx context.Context, entryID string) ([]Corroboration, error) {
	rows, err := r.query(ctx, "SELECT "+corroborationCols+" FROM corroborations WHERE entry_id = ? ORDER BY first_at, principal_key LIMIT ?",
		entryID, maxListLimit)
	if err != nil {
		return nil, err
	}
	var out []Corroboration
	var c Corroboration
	err = eachRow(rows, func() { out = append(out, c) }, func() (err error) { c, err = scanCorroboration(rows); return err })
	return out, err
}

// MemoryCounts counts the workspace's active entries and principal's pending ones.
func (r *reader) MemoryCounts(ctx context.Context, workspaceID, principal string) (MemoryCounts, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return MemoryCounts{}, err
	}
	var c MemoryCounts
	err := r.queryRow(ctx, `SELECT
		(SELECT count(*) FROM entries WHERE workspace_id = ?1 AND lifecycle = 'active'),
		(SELECT count(*) FROM entries WHERE workspace_id = ?1 AND source_key = ?2 AND status = 'pending' AND lifecycle = 'active')`,
		workspaceID, principalKey(principal)).Scan(&c.Active, &c.Pending)
	return c, err
}

// BackfillDedupeKeys computes the postings of the next entries that predate migration
// 4, in pk order, and advances the watermark past them. A purged entry has no content
// and gets none.
func (t *txn) BackfillDedupeKeys(ctx context.Context, max int) (int, error) {
	next, end, err := t.backfillRange(ctx)
	if err != nil || next >= end {
		return 0, err
	}
	rows, err := t.query(ctx, `SELECT e.pk, e.workspace_id, coalesce(r.content, '')
		FROM entries e JOIN revisions r ON r.entry_id = e.id AND r.revision = e.revision
		WHERE e.pk > ? AND e.pk <= ? ORDER BY e.pk LIMIT ?`, next, end, clampLimit(max))
	if err != nil {
		return 0, err
	}
	type todo struct {
		pk          int64
		ws, content string
	}
	var batch []todo
	var d todo
	if err := eachRow(rows, func() { batch = append(batch, d) }, func() error { return rows.Scan(&d.pk, &d.ws, &d.content) }); err != nil {
		return 0, err
	}
	for _, d := range batch {
		if d.content == "" {
			continue
		}
		if err := t.putDedupeKeys(ctx, d.pk, d.ws, d.content); err != nil {
			return 0, err
		}
	}
	mark := end
	if len(batch) == clampLimit(max) {
		mark = batch[len(batch)-1].pk
	}
	_, err = t.exec(ctx, "UPDATE store_meta SET value = ? WHERE key = 'dedupe_backfill_next'", strconv.FormatInt(mark, 10))
	return len(batch), err
}

// backfillRange reads the backfill watermark and its end.
func (r *reader) backfillRange(ctx context.Context) (next, end int64, err error) {
	var n, e string
	err = r.queryRow(ctx, `SELECT (SELECT value FROM store_meta WHERE key = 'dedupe_backfill_next'),
		(SELECT value FROM store_meta WHERE key = 'dedupe_backfill_end')`).Scan(&n, &e)
	if err != nil {
		return 0, 0, err
	}
	if next, err = strconv.ParseInt(n, 10, 64); err == nil {
		end, err = strconv.ParseInt(e, 10, 64)
	}
	return next, end, err
}
