package workspaceops

import (
	"strconv"
	"sync"
	"time"
)

// Session-seen suppression (PAR-RCL-04): recall(session_id) leaves out entries it
// already returned to that caller's session, until their content, stale flag or rank
// state changes. The sets live in memory, keyed by workspace, caller and session, and
// are evicted after recallSeenTTL without use; a lost set only means an entry is shown
// again.
const (
	recallSeenTTL = 30 * time.Minute
	// maxSeenSessions and maxSeenEntries bound the memory the sets hold.
	maxSeenSessions = 1024
	maxSeenEntries  = 4096
)

type seenSet struct {
	used  time.Time
	shown map[string]string // entry id -> seen-print
}

type seenSets struct {
	mu   sync.Mutex
	now  func() time.Time
	sets map[string]*seenSet
}

var recallSeen = &seenSets{now: time.Now, sets: map[string]*seenSet{}}

// recallSeenKey is "" (no suppression) without a session.
func recallSeenKey(workspaceID, caller, session string) string {
	if session == "" {
		return ""
	}
	return workspaceID + "\x00" + caller + "\x00" + session
}

// seenPrint changes whenever the entry would read differently: another revision, a
// changed stale flag or another rank state.
func (c scoredEntry) seenPrint() string {
	return c.rank.ContentDigest + "|" + c.rank.State + "|" + strconv.FormatBool(c.entry.Stale)
}

// shown returns a copy of the session's seen-prints (nil for no session).
func (s *seenSets) shown(key string) map[string]string {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict()
	set, ok := s.sets[key]
	if !ok {
		return nil
	}
	out := make(map[string]string, len(set.shown))
	for id, p := range set.shown {
		out[id] = p
	}
	return out
}

// mark records entries as shown to the session.
func (s *seenSets) mark(key string, prints map[string]string) {
	if key == "" || len(prints) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.sets[key]
	if !ok {
		s.evict()
		if len(s.sets) >= maxSeenSessions {
			s.dropOldest()
		}
		set = &seenSet{shown: map[string]string{}}
		s.sets[key] = set
	}
	if len(set.shown)+len(prints) > maxSeenEntries {
		clear(set.shown) // start over: showing an entry twice is the safe failure
	}
	for id, p := range prints {
		set.shown[id] = p
	}
	set.used = s.now()
}

// evict drops the sets unused for recallSeenTTL.
func (s *seenSets) evict() {
	cutoff := s.now().Add(-recallSeenTTL)
	for k, set := range s.sets {
		if set.used.Before(cutoff) {
			delete(s.sets, k)
		}
	}
}

func (s *seenSets) dropOldest() {
	oldest := ""
	for k, set := range s.sets {
		if oldest == "" || set.used.Before(s.sets[oldest].used) {
			oldest = k
		}
	}
	delete(s.sets, oldest)
}
