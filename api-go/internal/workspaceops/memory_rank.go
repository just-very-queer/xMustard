package workspaceops

import (
	"fmt"
	"math"
	"strings"
	"time"

	"xmustard/api-go/internal/govstore"
)

// Recall ranking (PAR-RCL-01): deterministic, no model. Each entry's score is the sum
// of signed signals, and recall(explain=true) returns them as score_details with the
// reasons behind each non-zero signal.
//
//   - bm25: FTS5 BM25 over title, body and anchors (porter-stemmed, IDF-weighted),
//     scaled so the best match in the view scores bm25Weight.
//   - path: each declared path that is a focus path (the query paths, else the files
//     being worked on). anchor: each declared path under a focus directory, or a
//     declared directory holding a focus path.
//   - trust: the verification mode, the approvals, and the rank state.
//   - recency: decays with age behind the newest entry in the view.
//   - feedback: the helpful, misleading and stale_harm outcomes on the served revision.
//   - stale_penalty: the entry's files drifted since it was verified (candidate window).
const (
	bm25Weight     = 3.0
	pathWeight     = 1.5
	anchorWeight   = 0.5
	approvalWeight = 0.25
	maxApprovals   = 4
	recencyWeight  = 0.5
	recencyDays    = 7.0
	stalePenalty   = 1.0
	// minRelevance gates an explicit query or path signal: an entry whose bm25, path
	// and anchor signals sum below it is dropped, not ranked last.
	minRelevance = 0.15
	// feedback: a helpful report adds, a misleading or stale_harm one subtracts more
	// (a wrong memory costs more than a right one saves), within the bounds.
	helpfulWeight    = 0.2
	misleadingWeight = -0.5
	staleHarmWeight  = -0.75
	maxFeedback      = 1.0
	minFeedback      = -3.0
)

// modeTrust is the trust a verification mode earns; stateTrust shifts it by rank state
// so replaced and expired memory ranks below what is served.
var (
	modeTrust = map[string]float64{
		VerificationPeer: 0.5, VerificationSingleAgent: 0.25, VerificationSelfAssertedOpen: 0.1,
	}
	stateTrust = map[string]float64{govstore.RankSuperseded: -1.0, govstore.RankExpired: -0.5}
)

// ScoreDetails is an entry's ranking, signal by signal. Total is their sum.
type ScoreDetails struct {
	BM25         float64  `json:"bm25"`
	Path         float64  `json:"path"`
	Anchor       float64  `json:"anchor"`
	Trust        float64  `json:"trust"`
	Recency      float64  `json:"recency"`
	Feedback     float64  `json:"feedback"`
	StalePenalty float64  `json:"stale_penalty"`
	Total        float64  `json:"total"`
	Reasons      []string `json:"reasons,omitempty"`
}

// relevance is the task-match part of the score: what an explicit signal gates on.
func (d ScoreDetails) relevance() float64 { return d.BM25 + d.Path + d.Anchor }

func (d *ScoreDetails) sum() {
	d.Total = round3(d.BM25 + d.Path + d.Anchor + d.Trust + d.Recency + d.Feedback + d.StalePenalty)
}

func (d *ScoreDetails) because(format string, args ...any) {
	d.Reasons = append(d.Reasons, fmt.Sprintf(format, args...))
}

// stale applies the stale penalty once the drift check found changed paths.
func (d *ScoreDetails) stale(paths []string) {
	if len(paths) == 0 {
		return
	}
	d.StalePenalty = -stalePenalty
	d.because("stale: %s changed since verification", strings.Join(paths, ", "))
	d.sum()
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// recallSignals is what scoring compares every entry against.
type recallSignals struct {
	bm25     map[string]float64
	bestBM25 float64
	focus    map[string]bool
	newest   time.Time
}

func newRecallSignals(view []govstore.RankEntry, bm25 map[string]float64, focus []string) recallSignals {
	s := recallSignals{bm25: bm25, focus: map[string]bool{}}
	for _, v := range bm25 {
		s.bestBM25 = math.Max(s.bestBM25, v)
	}
	for _, p := range focus {
		s.focus[p] = true
	}
	for _, e := range view {
		if t, ok := storeTime(e.UpdatedAt); ok && t.After(s.newest) {
			s.newest = t
		}
	}
	return s
}

// nested reports whether p lies under a focus path or holds one.
func (s recallSignals) nested(p string) bool {
	for f := range s.focus {
		if strings.HasPrefix(p, f+"/") || strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// score rates one entry on every signal but the stale penalty.
func (s recallSignals) score(e govstore.RankEntry) ScoreDetails {
	var d ScoreDetails
	if b := s.bm25[e.ID]; b > 0 && s.bestBM25 > 0 {
		d.BM25 = round3(bm25Weight * b / s.bestBM25)
		d.because("text match %.0f%% of the best", 100*b/s.bestBM25)
	}
	for _, p := range e.Paths {
		switch {
		case s.focus[p]:
			d.Path += pathWeight
			d.because("path %s", p)
		case s.nested(p):
			d.Anchor += anchorWeight
			d.because("anchor %s nests with a focus path", p)
		}
	}
	approvals := min(e.Approvals, maxApprovals)
	d.Trust = round3(modeTrust[e.VerificationMode] + stateTrust[e.State] + approvalWeight*float64(approvals))
	d.because("%s, %d approvals", trustLabel(e), e.Approvals)
	if e.State != govstore.RankServed {
		d.because("state %s", e.State)
	}
	if t, ok := storeTime(e.UpdatedAt); ok && !s.newest.IsZero() {
		age := s.newest.Sub(t).Hours() / 24
		d.Recency = round3(recencyWeight / (1 + age/recencyDays))
	}
	if e.Helpful+e.Misleading+e.StaleHarm > 0 {
		fb := helpfulWeight*float64(e.Helpful) + misleadingWeight*float64(e.Misleading) + staleHarmWeight*float64(e.StaleHarm)
		d.Feedback = round3(math.Min(math.Max(fb, minFeedback), maxFeedback))
		d.because("feedback: %d helpful, %d misleading, %d stale_harm", e.Helpful, e.Misleading, e.StaleHarm)
	}
	d.sum()
	return d
}

// trustLabel is the trust recall labels an entry with: its verification mode, or
// unverified while it waits for votes.
func trustLabel(e govstore.RankEntry) string {
	if e.State == govstore.RankPending || e.VerificationMode == "" {
		return "unverified"
	}
	return e.VerificationMode
}

func storeTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}
