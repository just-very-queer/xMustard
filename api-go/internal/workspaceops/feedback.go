package workspaceops

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Agent-feedback layer (IndexEngine Phase 2): the bidirectional half of the search
// engine. Agent actions write back into a feedback segment — which paths search
// returned, which were verified into memory, which runs touched on success/failure —
// and that signal is fused into later search ranking with recency decay. Rank by
// durable outcomes, not token volume.

type FeedbackEntry struct {
	Path           string `json:"path"`
	RetrievalCount int    `json:"retrieval_count"`
	VerifyCount    int    `json:"verify_count"`
	RunSuccess     int    `json:"run_success"`
	RunFail        int    `json:"run_fail"`
	LastUsed       string `json:"last_used"`
}

func feedbackPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "agent_feedback.json")
}

func loadFeedback(dataDir, workspaceID string) (map[string]*FeedbackEntry, error) {
	var list []*FeedbackEntry
	if err := readJSON(feedbackPath(dataDir, workspaceID), &list); err != nil {
		if os.IsNotExist(err) {
			return map[string]*FeedbackEntry{}, nil
		}
		return nil, err
	}
	out := make(map[string]*FeedbackEntry, len(list))
	for _, e := range list {
		out[e.Path] = e
	}
	return out, nil
}

func saveFeedback(dataDir, workspaceID string, m map[string]*FeedbackEntry) error {
	list := make([]*FeedbackEntry, 0, len(m))
	for _, e := range m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	return writeJSON(feedbackPath(dataDir, workspaceID), list)
}

// feedbackCountCap bounds every per-path counter.
const feedbackCountCap = 10_000

// feedbackDelta is a not-yet-persisted increment to one path's entry. The search hot
// path coalesces these in memory (feedback_recorder.go); RecordFeedback folds a
// batch straight into the store.
type feedbackDelta struct {
	Retrieval, Verify, RunSuccess, RunFail int
	LastUsed                               string
}

// add bumps the counter for kind ("retrieval", "verify", "run_success", "run_fail";
// an unknown kind only refreshes LastUsed) as of timestamp at.
func (d *feedbackDelta) add(kind, at string) {
	switch kind {
	case "retrieval":
		d.Retrieval = minInt(d.Retrieval+1, feedbackCountCap)
	case "verify":
		d.Verify = minInt(d.Verify+1, feedbackCountCap)
	case "run_success":
		d.RunSuccess = minInt(d.RunSuccess+1, feedbackCountCap)
	case "run_fail":
		d.RunFail = minInt(d.RunFail+1, feedbackCountCap)
	}
	d.LastUsed = laterTimestamp(d.LastUsed, at)
}

// merge adds o into d (counts capped, latest LastUsed wins).
func (d *feedbackDelta) merge(o feedbackDelta) {
	d.Retrieval = minInt(d.Retrieval+o.Retrieval, feedbackCountCap)
	d.Verify = minInt(d.Verify+o.Verify, feedbackCountCap)
	d.RunSuccess = minInt(d.RunSuccess+o.RunSuccess, feedbackCountCap)
	d.RunFail = minInt(d.RunFail+o.RunFail, feedbackCountCap)
	d.LastUsed = laterTimestamp(d.LastUsed, o.LastUsed)
}

// applyFeedbackDeltas folds deltas into m, creating entries as needed.
func applyFeedbackDeltas(m map[string]*FeedbackEntry, deltas map[string]feedbackDelta) {
	for p, d := range deltas {
		e := m[p]
		if e == nil {
			e = &FeedbackEntry{Path: p}
			m[p] = e
		}
		e.RetrievalCount = minInt(e.RetrievalCount+d.Retrieval, feedbackCountCap)
		e.VerifyCount = minInt(e.VerifyCount+d.Verify, feedbackCountCap)
		e.RunSuccess = minInt(e.RunSuccess+d.RunSuccess, feedbackCountCap)
		e.RunFail = minInt(e.RunFail+d.RunFail, feedbackCountCap)
		e.LastUsed = laterTimestamp(e.LastUsed, d.LastUsed)
	}
}

// laterTimestamp returns whichever RFC3339 timestamp is later, preferring a
// parseable one over an unparseable one.
func laterTimestamp(a, b string) string {
	tb, errB := time.Parse(time.RFC3339, b)
	if errB != nil {
		return a
	}
	if ta, errA := time.Parse(time.RFC3339, a); errA == nil && ta.After(tb) {
		return a
	}
	return b
}

// mergeFeedback folds deltas into the on-disk store in one locked read-modify-write.
func mergeFeedback(dataDir, workspaceID string, deltas map[string]feedbackDelta) error {
	unlock := lockStore(feedbackPath(dataDir, workspaceID))
	defer unlock()
	m, err := loadFeedback(dataDir, workspaceID)
	if err != nil {
		return err
	}
	applyFeedbackDeltas(m, deltas)
	return saveFeedback(dataDir, workspaceID, m)
}

// RecordFeedback synchronously bumps a signal for the given paths. kind is one of
// "retrieval", "verify", "run_success", "run_fail". Bounded (caps per path). Used by
// the low-rate verify/run outcome paths; search retrievals go through the coalescing
// recorder instead so they never write the store on the request path.
func RecordFeedback(dataDir, workspaceID, kind string, paths []string) error {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return err
	}
	clean := cleanPaths(paths)
	if len(clean) == 0 {
		return nil
	}
	now := nowUTC()
	deltas := make(map[string]feedbackDelta, len(clean))
	for _, p := range clean {
		var d feedbackDelta
		d.add(kind, now)
		deltas[p] = d
	}
	return mergeFeedback(dataDir, workspaceID, deltas)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// feedbackBoosts returns a per-path boost in roughly [0, 1], combining the signals
// with a recency decay (a path unused for ~30 days contributes ~1/e of its raw
// score). A failing run suppresses a path. Feedback the recorder has not flushed yet
// is merged over the on-disk state, so a search sees retrievals recorded moments ago.
func feedbackBoosts(dataDir, workspaceID string) map[string]float64 {
	// Snapshot the buffer before reading the store: a flush racing this read can then
	// only count its batch twice for this one ranking, never drop it.
	pending := feedbackRec.pending(dataDir, workspaceID)
	m, err := loadFeedback(dataDir, workspaceID)
	if err != nil {
		return nil
	}
	applyFeedbackDeltas(m, pending)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]float64, len(m))
	for p, e := range m {
		raw := 2.0*float64(e.VerifyCount) + 0.5*float64(e.RetrievalCount) +
			3.0*float64(e.RunSuccess) - 2.0*float64(e.RunFail)
		decay := 1.0
		if t, perr := time.Parse(time.RFC3339, e.LastUsed); perr == nil {
			ageDays := time.Since(t).Hours() / 24.0
			decay = math.Exp(-ageDays / 30.0)
		}
		// squash into a bounded boost so feedback nudges, never dominates.
		score := raw * decay
		out[p] = math.Tanh(score / 5.0)
	}
	return out
}

// applyFeedbackToHits re-ranks search hits by adding a small feedback boost to each
// hit's score, then re-sorting. Paths the agent has verified / succeeded on rise;
// paths tied to failures sink. The boost is capped so lexical/semantic relevance
// still dominates.
func applyFeedbackToHits(dataDir, workspaceID string, hits []searchHit) []searchHit {
	boosts := feedbackBoosts(dataDir, workspaceID)
	if len(boosts) == 0 {
		return hits
	}
	for i := range hits {
		if b, ok := boosts[hits[i].Path]; ok && b != 0 {
			hits[i].Score += 0.1 * b
			if !strings.Contains(hits[i].Reason, "feedback") {
				hits[i].Reason += " · feedback"
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].Score > hits[b].Score })
	return hits
}
