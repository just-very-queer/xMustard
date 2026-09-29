package workspaceops

import (
	"cmp"
	"context"
	"slices"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// New-session bootstrap (WS-34; PAR-RCL-08): the sessions a new session should know of.
// Descriptions come from handoff capsules (or the session's own summary), never from a
// model.

// BootstrapLabel labels the bootstrap sessions.
const BootstrapLabel = "lightweight context, not confirmed facts"

// Bootstrap bounds and thresholds. A session's relevance is the reciprocal-rank fusion
// of its rank by text score and by recency: 1/(rrfK+rank) summed over the two lists.
const (
	bootstrapRecent   = 5
	bootstrapRelevant = 5
	bootstrapHits     = 200 // transcript rows ranked to find relevant sessions
	rrfK              = 60
	rrfMin            = 0.012 // a relevant session must reach this fused score...
	rrfTopRatio       = 0.93  // ...and this share of the best one's
)

// BootstrapSession is one prior session as the bootstrap lists it.
type BootstrapSession struct {
	ID          string  `json:"id"`
	Client      string  `json:"client,omitempty"`
	Status      string  `json:"status"`
	LastSeenAt  string  `json:"last_seen_at"`
	Description string  `json:"description,omitempty"`
	From        string  `json:"description_from,omitempty"` // handoff_capsule | summary
	Score       float64 `json:"rrf,omitempty"`
	// Handle expands the best matching message: recall source=sessions cursor=Handle.
	Handle string `json:"handle,omitempty"`
	// InjectionFlags withhold the description: it matched instruction patterns.
	InjectionFlags []string `json:"injection_flags,omitempty"`
}

// bootstrap fills recent_sessions and relevant_sessions. own is the caller's execution
// key, left out. The recent sessions are read here, imported transcripts included, so
// they are dated by their own last message and never displace the live sessions the
// handoff reads.
func (s *groundingSession) bootstrap(ctx context.Context, r govstore.Reader, ws, own, query string) error {
	ownRow := ""
	if own != "" {
		ownRow = sessionRow(ws, own)
	}
	sessions, err := r.ListSessions(ctx, govstore.SessionFilter{WorkspaceID: ws, Limit: 4 * bootstrapRecent})
	if err != nil {
		return err
	}
	prior := slices.DeleteFunc(slices.Clone(sessions), func(x govstore.Session) bool { return x.ParentID != "" || x.ID == ownRow })
	for _, x := range prior[:min(len(prior), bootstrapRecent)] {
		s.RecentSessions = append(s.RecentSessions, describeSession(ctx, r, ws, x))
	}
	if query != "" {
		rel, err := relevantSessions(ctx, r, ws, ownRow, query)
		if err != nil {
			return err
		}
		s.RelevantSessions = rel
	}
	if len(s.RecentSessions)+len(s.RelevantSessions) > 0 {
		s.BootstrapLabel = BootstrapLabel
	}
	return nil
}

// describeSession is a session with its handoff capsule's state (else its summary).
func describeSession(ctx context.Context, r govstore.Reader, ws string, x govstore.Session) BootstrapSession {
	b := BootstrapSession{ID: x.ExecutionKey, Client: x.Client, Status: x.Status, LastSeenAt: x.LastSeenAt}
	text, from := x.Summary, "summary"
	if capsule, _, ok := sessionCapsule(ctx, r, ws, x.ExecutionKey, false); ok && capsule.CurrentState != "" {
		text, from = capsule.CurrentState, "handoff_capsule"
	}
	text = cutBytes(text, groundViewTextMax)
	if flags := injection.Scan(text).Flags; len(flags) > 0 {
		b.InjectionFlags = flags
		return b
	}
	if b.Description = text; text != "" {
		b.From = from
	}
	return b
}

// sessionScore is a session's best transcript hit.
type sessionScore struct {
	row    string
	handle string
	score  float64
	at     string
	rrf    float64
}

// relevantSessions ranks the sessions the query matches: rank by best text score and by
// most recent matching message, fused (rrf), then kept when the fused score reaches
// rrfMin and rrfTopRatio of the best.
func relevantSessions(ctx context.Context, r govstore.Reader, ws, ownRow, query string) ([]BootstrapSession, error) {
	hits, err := r.SearchTranscripts(ctx, govstore.TranscriptQuery{WorkspaceID: ws, Text: query, Limit: bootstrapHits})
	if err != nil || len(hits) == 0 {
		return nil, err
	}
	best := map[string]*sessionScore{}
	for _, h := range hits {
		b, ok := best[h.SessionID]
		if !ok {
			b = &sessionScore{row: h.SessionID}
			best[h.SessionID] = b
		}
		if h.Score > b.score || !ok {
			b.score, b.handle = h.Score, sessionHandle(h.SessionID, h.Seq)
		}
		b.at = max(b.at, h.At)
	}
	delete(best, ownRow)
	list := make([]*sessionScore, 0, len(best))
	for _, b := range best {
		list = append(list, b)
	}
	fuse(list)
	kept := keepFused(list)
	out := []BootstrapSession{}
	for _, b := range kept[:min(len(kept), bootstrapRelevant)] {
		x, err := r.GetSession(ctx, b.row)
		if err != nil {
			continue
		}
		d := describeSession(ctx, r, ws, x)
		d.Score, d.Handle = b.rrf, b.handle
		out = append(out, d)
	}
	return out, nil
}

// fuse sets each score's reciprocal-rank fusion over its text-score and recency ranks.
func fuse(list []*sessionScore) {
	byScore := func(a, b *sessionScore) int { return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.row, b.row)) }
	byTime := func(a, b *sessionScore) int { return cmp.Or(cmp.Compare(b.at, a.at), cmp.Compare(a.row, b.row)) }
	for _, order := range []func(a, b *sessionScore) int{byScore, byTime} {
		slices.SortFunc(list, order)
		for i, b := range list {
			b.rrf += 1 / float64(rrfK+i+1)
		}
	}
}

// keepFused is the scores that reach rrfMin and rrfTopRatio of the best, best first.
func keepFused(list []*sessionScore) []*sessionScore {
	slices.SortFunc(list, func(a, b *sessionScore) int { return cmp.Or(cmp.Compare(b.rrf, a.rrf), cmp.Compare(a.row, b.row)) })
	if len(list) == 0 {
		return nil
	}
	floor := max(rrfMin, rrfTopRatio*list[0].rrf)
	i := slices.IndexFunc(list, func(b *sessionScore) bool { return b.rrf < floor })
	if i < 0 {
		return list
	}
	return list[:i]
}
