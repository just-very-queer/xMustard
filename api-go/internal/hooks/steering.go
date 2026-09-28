package hooks

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// Steering (PAR-HAR-02): per-session state the hook service keeps in memory, bounded,
// and lost on restart (a lost session only means a nudge or a memory is shown again).
// A session is keyed by the caller's principal and the client's session id, so one
// principal cannot read or reset another's state by reusing a session id.

// Limits are the steering thresholds.
type Limits struct {
	// A burst is BurstSearches native searches within BurstWindow; the nudge toward
	// search and impact is then given at most once per Cooldown.
	BurstSearches int
	BurstWindow   time.Duration
	Cooldown      time.Duration
	// BatchSearches searches in one parallel batch nudge too (same cooldown).
	BatchSearches int
	// GitCooldown spaces the post-git freshness notice.
	GitCooldown time.Duration
	// BusyCooldown spaces the note that hooks were skipped while the daemon was busy.
	BusyCooldown time.Duration
}

// DefaultLimits are the shipped thresholds: a 2-minute nudge cooldown (PAR-HAR-02).
var DefaultLimits = Limits{BurstSearches: 4, BurstWindow: time.Minute, Cooldown: 2 * time.Minute,
	BatchSearches: 3, GitCooldown: 30 * time.Second, BusyCooldown: 5 * time.Minute}

const (
	maxSessions       = 1024
	maxInjectedPerSes = 512
	maxBaselines      = 32
)

// Session is one client session's steering state.
type Session struct {
	searches  []time.Time
	lastNudge time.Time
	lastGit   time.Time
	busy      int
	lastBusy  time.Time
	injected  map[string]bool
	baselines map[string][]string
	// Agents maps a subagent id to its type while it runs (attribution).
	agents map[string]string
	used   time.Time
}

// Sessions holds the sessions. The zero value is not usable; use NewSessions.
type Sessions struct {
	mu     sync.Mutex
	now    func() time.Time
	limits Limits
	m      map[string]*Session
}

// NewSessions returns an empty table; now is the clock (time.Now outside tests).
func NewSessions(limits Limits, now func() time.Time) *Sessions {
	return &Sessions{now: now, limits: limits, m: map[string]*Session{}}
}

// Key names a session: the principal and the client's session id. An empty session id
// has no state: every call is a fresh session.
func Key(principal, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return principal + "\x00" + sessionID
}

// with runs fn on the session under the lock, creating it; a keyless call gets a
// throwaway session.
func (s *Sessions) with(key string, fn func(*Session, time.Time)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if key == "" {
		fn(&Session{}, now)
		return
	}
	ses := s.m[key]
	if ses == nil {
		if len(s.m) >= maxSessions {
			s.evict()
		}
		ses = &Session{}
		s.m[key] = ses
	}
	ses.used = now
	fn(ses, now)
}

func (s *Sessions) evict() {
	var oldest string
	for k, v := range s.m {
		if oldest == "" || v.used.Before(s.m[oldest].used) {
			oldest = k
		}
	}
	delete(s.m, oldest)
}

// Len is the number of sessions held.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// NoteSearch records one native search and reports whether the burst nudge is due.
func (s *Sessions) NoteSearch(key string) (nudge bool) {
	s.with(key, func(ses *Session, now time.Time) {
		ses.searches = append(slices.DeleteFunc(ses.searches, func(t time.Time) bool {
			return now.Sub(t) > s.limits.BurstWindow
		}), now)
		nudge = len(ses.searches) >= s.limits.BurstSearches && s.nudgeDue(ses, now)
	})
	return nudge
}

// NoteBatch reports whether a batch with that many searches earns the nudge.
func (s *Sessions) NoteBatch(key string, searches int) (nudge bool) {
	s.with(key, func(ses *Session, now time.Time) {
		nudge = searches >= s.limits.BatchSearches && s.nudgeDue(ses, now)
	})
	return nudge
}

func (s *Sessions) nudgeDue(ses *Session, now time.Time) bool {
	if !ses.lastNudge.IsZero() && now.Sub(ses.lastNudge) < s.limits.Cooldown {
		return false
	}
	ses.lastNudge = now
	return true
}

// GitNoticeDue reports whether the post-git notice may be given now.
func (s *Sessions) GitNoticeDue(key string) (due bool) {
	s.with(key, func(ses *Session, now time.Time) {
		if due = ses.lastGit.IsZero() || now.Sub(ses.lastGit) >= s.limits.GitCooldown; due {
			ses.lastGit = now
		}
	})
	return due
}

// NoteBusy counts a hook skipped because the daemon was busy or out of time.
func (s *Sessions) NoteBusy(key string) {
	s.with(key, func(ses *Session, _ time.Time) { ses.busy++ })
}

// BusyNote returns the throttled note about skipped hooks, or "".
func (s *Sessions) BusyNote(key string) (note string) {
	s.with(key, func(ses *Session, now time.Time) {
		if ses.busy == 0 || !ses.lastBusy.IsZero() && now.Sub(ses.lastBusy) < s.limits.BusyCooldown {
			return
		}
		note = fmt.Sprintf("[xmustard] %d hook call(s) in this session returned no context because the daemon was busy or past its time budget.", ses.busy)
		ses.busy, ses.lastBusy = 0, now
	})
	return note
}

// Unseen returns the ids not yet pushed into this session, in order.
func (s *Sessions) Unseen(key string, ids []string) (out []string) {
	s.with(key, func(ses *Session, _ time.Time) {
		for _, id := range ids {
			if !ses.injected[id] {
				out = append(out, id)
			}
		}
	})
	return out
}

// MarkInjected records ids as pushed into this session.
func (s *Sessions) MarkInjected(key string, ids []string) {
	s.with(key, func(ses *Session, _ time.Time) {
		if ses.injected == nil {
			ses.injected = map[string]bool{}
		}
		for _, id := range ids {
			if len(ses.injected) >= maxInjectedPerSes {
				return
			}
			ses.injected[id] = true
		}
	})
}

// Compacted forgets what was pushed: compaction summarized it away, so it may be
// pushed again.
func (s *Sessions) Compacted(key string) {
	s.with(key, func(ses *Session, _ time.Time) { ses.injected = nil })
}

// SetBaseline keeps a file's syntax errors from before an edit.
func (s *Sessions) SetBaseline(key, path string, errs []string) {
	s.with(key, func(ses *Session, _ time.Time) {
		if ses.baselines == nil {
			ses.baselines = map[string][]string{}
		}
		if _, ok := ses.baselines[path]; !ok && len(ses.baselines) >= maxBaselines {
			clear(ses.baselines)
		}
		ses.baselines[path] = errs
	})
}

// TakeBaseline returns and forgets a file's pre-edit syntax errors.
func (s *Sessions) TakeBaseline(key, path string) (errs []string, ok bool) {
	s.with(key, func(ses *Session, _ time.Time) {
		errs, ok = ses.baselines[path]
		delete(ses.baselines, path)
	})
	return errs, ok
}

// AgentStarted records a subagent of the session; AgentStopped forgets it.
func (s *Sessions) AgentStarted(key, agentID, agentType string) {
	s.with(key, func(ses *Session, _ time.Time) {
		if ses.agents == nil {
			ses.agents = map[string]string{}
		}
		if len(ses.agents) < maxBaselines*4 {
			ses.agents[agentID] = agentType
		}
	})
}

// AgentStopped forgets a subagent.
func (s *Sessions) AgentStopped(key, agentID string) {
	s.with(key, func(ses *Session, _ time.Time) { delete(ses.agents, agentID) })
}

// Agents returns how many subagents of the session are running.
func (s *Sessions) Agents(key string) (n int) {
	s.with(key, func(ses *Session, _ time.Time) { n = len(ses.agents) })
	return n
}

// End drops a session.
func (s *Sessions) End(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

// Nudge is the text of the burst nudge.
const Nudge = "[xmustard] Several text searches ran in a short time. The xmustard search tool ranks code by BM25 over bodies, names, paths and docs with graph proximity in one call, and impact lists what depends on a file or symbol."

// GitNotice is the post-git freshness notice for a git subcommand.
func GitNotice(verb string) string {
	return fmt.Sprintf("[xmustard] `git %s` changed HEAD or the working tree. The code index, drift and memory staleness are re-read on the next ground, search, explain or impact call; hook hits from the index are as of its last refresh until then.", verb)
}
