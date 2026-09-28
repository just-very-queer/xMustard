package govstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Full-text search over memories (title, body, anchors) and session transcripts. Both
// FTS5 tables are contentless: they hold only the inverted index, keyed on
// entries.pk and session_events.seq, and the text is read from its own table.

// bm25 column weights for memory_fts: a title hit counts three times a body hit, and
// an anchor hit twice.
const memoryBM25 = "bm25(memory_fts, 3.0, 1.0, 2.0)"

const maxQueryTerms = 32

var ftsTermPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)

// ftsQuery turns free text into a safe FTS5 query: each word becomes a quoted term,
// OR-ed together. User input can therefore never inject FTS5 syntax.
func ftsQuery(text string) (string, []string) {
	seen := map[string]bool{}
	var terms []string
	for _, w := range ftsTermPattern.FindAllString(strings.ToLower(text), -1) {
		if seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
		if len(terms) == maxQueryTerms {
			break
		}
	}
	if len(terms) == 0 {
		return "", nil
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " OR "), terms
}

// MemoryQuery searches a workspace's memories.
type MemoryQuery struct {
	WorkspaceID string
	Text        string
	// ServedOnly keeps promoted, active, unexpired entries. Otherwise active entries of
	// any status match (pending ones too), labelled by their status.
	ServedOnly bool
	// IncludeInactive also matches superseded, retracted, merged and archived entries.
	IncludeInactive bool
	Limit           int
}

// MemoryHit is one ranked match. Score is the negated bm25: higher is better.
type MemoryHit struct {
	EntryID          string  `json:"entry_id"`
	Title            string  `json:"title"`
	Score            float64 `json:"score"`
	Status           string  `json:"status"`
	Promoted         bool    `json:"promoted"`
	VerificationMode string  `json:"verification_mode,omitempty"`
	Lifecycle        string  `json:"lifecycle"`
}

// TranscriptQuery searches session ledger text.
type TranscriptQuery struct {
	WorkspaceID string
	Text        string
	SessionID   string
	Since       string
	Until       string
	Limit       int
}

// TranscriptHit is one ranked ledger row with a snippet around the first match.
type TranscriptHit struct {
	Seq       int64   `json:"seq"`
	SessionID string  `json:"session_id"`
	Kind      string  `json:"kind"`
	Role      string  `json:"role,omitempty"`
	At        string  `json:"at"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// SearchReader runs full-text queries.
type SearchReader interface {
	SearchMemories(ctx context.Context, q MemoryQuery) ([]MemoryHit, error)
	// MemoryScores returns the negated BM25 of every workspace entry matching text,
	// keyed by entry id, whatever its state: the lexical signal recall fuses.
	MemoryScores(ctx context.Context, workspaceID, text string) (map[string]float64, error)
	SearchTranscripts(ctx context.Context, q TranscriptQuery) ([]TranscriptHit, error)
}

func (t *txn) ftsPut(ctx context.Context, pk int64, title, body, anchors string) error {
	if _, err := t.exec(ctx, "INSERT INTO memory_fts (rowid, title, body, anchors) VALUES (?, ?, ?, ?)",
		pk, title, body, anchors); err != nil {
		return fmt.Errorf("index entry: %w", err)
	}
	return nil
}

func (t *txn) ftsDelete(ctx context.Context, pk int64) error {
	if _, err := t.exec(ctx, "DELETE FROM memory_fts WHERE rowid = ?", pk); err != nil {
		return fmt.Errorf("unindex entry: %w", err)
	}
	return nil
}

// ftsOptimize merges every memory_fts segment into one. A contentless_delete table
// deletes by tombstone, so a deleted row's terms stay in the segment blobs until a
// merge rewrites them; purge needs them gone from the file, not just unmatched.
func (t *txn) ftsOptimize(ctx context.Context) error {
	if _, err := t.exec(ctx, "INSERT INTO memory_fts (memory_fts) VALUES ('optimize')"); err != nil {
		return fmt.Errorf("optimize memory index: %w", err)
	}
	return nil
}

// refreshFTS re-indexes an entry from its served revision and anchors. Purged entries
// stay out of the index.
func (t *txn) refreshFTS(ctx context.Context, entryID string) error {
	var pk int64
	var lifecycle, title, description, content, anchors string
	err := t.queryRow(ctx, `SELECT e.pk, e.lifecycle, e.title, e.description, coalesce(r.content, ''),
		coalesce((SELECT group_concat(value, ' ') FROM
			(SELECT value FROM anchors WHERE entry_id = e.id ORDER BY ordinal, pk)), '')
		FROM entries e LEFT JOIN revisions r ON r.entry_id = e.id AND r.revision = e.revision
		WHERE e.id = ?`, entryID).Scan(&pk, &lifecycle, &title, &description, &content, &anchors)
	if err != nil {
		return fmt.Errorf("reindex %s: %w", entryID, err)
	}
	if err := t.ftsDelete(ctx, pk); err != nil {
		return err
	}
	if lifecycle == LifecyclePurged {
		return nil
	}
	return t.ftsPut(ctx, pk, title, joinBody(description, content), anchors)
}

// SearchMemories ranks a workspace's memories by BM25 over title, body and anchors.
func (r *reader) SearchMemories(ctx context.Context, q MemoryQuery) ([]MemoryHit, error) {
	if err := validID("workspace", q.WorkspaceID); err != nil {
		return nil, err
	}
	match, _ := ftsQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	where := []string{"memory_fts MATCH ?", "e.workspace_id = ?"}
	args := []any{match, q.WorkspaceID}
	switch {
	case q.ServedOnly:
		where = append(where, "e.promoted = 1", "e.lifecycle = 'active'", "(e.expires_at IS NULL OR e.expires_at > ?)")
		args = append(args, r.nowText())
	case !q.IncludeInactive:
		where = append(where, "e.lifecycle = 'active'")
	}
	args = append(args, clampLimit(q.Limit))
	rows, err := r.query(ctx, `SELECT e.id, e.title, -`+memoryBM25+`, e.status, e.promoted, e.verification_mode, e.lifecycle
		FROM memory_fts JOIN entries e ON e.pk = memory_fts.rowid
		WHERE `+strings.Join(where, " AND ")+` ORDER BY `+memoryBM25+`, e.pk LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryHit
	for rows.Next() {
		var h MemoryHit
		var promoted int
		if err := rows.Scan(&h.EntryID, &h.Title, &h.Score, &h.Status, &promoted, &h.VerificationMode, &h.Lifecycle); err != nil {
			return nil, err
		}
		h.Promoted = promoted == 1
		out = append(out, h)
	}
	return out, rows.Err()
}

// maxScoredMatches bounds how many matches MemoryScores reads; the best-scoring ones
// are kept.
const maxScoredMatches = 20_000

// MemoryScores scores every matching entry of a workspace by BM25 over title, body and
// anchors (porter-stemmed, IDF-weighted).
func (r *reader) MemoryScores(ctx context.Context, workspaceID, text string) (map[string]float64, error) {
	if err := validID("workspace", workspaceID); err != nil {
		return nil, err
	}
	match, _ := ftsQuery(text)
	if match == "" {
		return nil, nil
	}
	rows, err := r.query(ctx, `SELECT e.id, -`+memoryBM25+` FROM memory_fts JOIN entries e ON e.pk = memory_fts.rowid
		WHERE memory_fts MATCH ? AND e.workspace_id = ? ORDER BY `+memoryBM25+`, e.pk LIMIT ?`, match, workspaceID, maxScoredMatches)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	var id string
	var score float64
	err = eachRow(rows, func() { out[id] = score }, func() error { return rows.Scan(&id, &score) })
	return out, err
}

// SearchTranscripts ranks session ledger text by BM25, with date and session filters.
func (r *reader) SearchTranscripts(ctx context.Context, q TranscriptQuery) ([]TranscriptHit, error) {
	if err := validID("workspace", q.WorkspaceID); err != nil {
		return nil, err
	}
	match, terms := ftsQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	where := []string{"transcript_fts MATCH ?", "s.workspace_id = ?"}
	args := []any{match, q.WorkspaceID}
	if q.SessionID != "" {
		where = append(where, "s.session_id = ?")
		args = append(args, q.SessionID)
	}
	for _, b := range []struct{ op, val, field string }{{">=", q.Since, "since"}, {"<", q.Until, "until"}} {
		v, err := inputTime(b.field, b.val)
		if err != nil {
			return nil, err
		}
		if v != "" {
			where = append(where, "s.at "+b.op+" ?")
			args = append(args, v)
		}
	}
	args = append(args, clampLimit(q.Limit))
	rows, err := r.query(ctx, `SELECT s.seq, s.session_id, s.kind, s.role, s.at, -bm25(transcript_fts), coalesce(s.body, '')
		FROM transcript_fts JOIN session_events s ON s.seq = transcript_fts.rowid
		WHERE `+strings.Join(where, " AND ")+` ORDER BY bm25(transcript_fts), s.seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptHit
	for rows.Next() {
		var h TranscriptHit
		var body string
		if err := rows.Scan(&h.Seq, &h.SessionID, &h.Kind, &h.Role, &h.At, &h.Score, &body); err != nil {
			return nil, err
		}
		h.Snippet = snippet(body, terms, 160)
		out = append(out, h)
	}
	return out, rows.Err()
}

// snippet returns up to width bytes of body around the first case-insensitive hit of
// any term, cut on rune boundaries.
func snippet(body string, terms []string, width int) string {
	lower := strings.ToLower(body)
	at := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 || len(lower) != len(body) {
		// no literal hit (the stemmer matched), or lowering changed byte offsets
		at = 0
	}
	start := max(at-width/4, 0)
	end := min(start+width, len(body))
	for start > 0 && !utf8.RuneStart(body[start]) {
		start--
	}
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end++
	}
	s := body[start:end]
	if start > 0 {
		s = "…" + s
	}
	if end < len(body) {
		s += "…"
	}
	return s
}
