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

// RecordFeedback bumps a signal for the given paths. kind is one of "retrieval",
// "verify", "run_success", "run_fail". Best-effort and bounded (caps per path).
func RecordFeedback(dataDir, workspaceID, kind string, paths []string) error {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return err
	}
	clean := cleanPaths(paths)
	if len(clean) == 0 {
		return nil
	}
	unlock := lockStore(feedbackPath(dataDir, workspaceID))
	defer unlock()
	m, err := loadFeedback(dataDir, workspaceID)
	if err != nil {
		return err
	}
	now := nowUTC()
	for _, p := range clean {
		e := m[p]
		if e == nil {
			e = &FeedbackEntry{Path: p}
			m[p] = e
		}
		switch kind {
		case "retrieval":
			e.RetrievalCount = minInt(e.RetrievalCount+1, 10_000)
		case "verify":
			e.VerifyCount = minInt(e.VerifyCount+1, 10_000)
		case "run_success":
			e.RunSuccess = minInt(e.RunSuccess+1, 10_000)
		case "run_fail":
			e.RunFail = minInt(e.RunFail+1, 10_000)
		}
		e.LastUsed = now
	}
	return saveFeedback(dataDir, workspaceID, m)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// feedbackBoosts returns a per-path boost in roughly [0, 1], combining the signals
// with a recency decay (a path unused for ~30 days contributes ~1/e of its raw
// score). A failing run suppresses a path.
func feedbackBoosts(dataDir, workspaceID string) map[string]float64 {
	m, err := loadFeedback(dataDir, workspaceID)
	if err != nil || len(m) == 0 {
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
