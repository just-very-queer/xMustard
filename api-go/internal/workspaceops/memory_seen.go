package workspaceops

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// Session-seen suppression (PAR-RCL-04): recall(session_id) leaves out entries it
// already returned to that caller's session, until their content, stale flag or rank
// state changes. The sets live in memory, keyed by a digest of workspace, caller and
// session, and are evicted after recallSeenTTL without use; a lost set only means an
// entry is shown again.
const (
	recallSeenTTL = 30 * time.Minute
	// RecallMaxSessionID bounds session_id in bytes (HTTP 400, MCP maxLength).
	RecallMaxSessionID = 128
	// maxSeenSessions and maxSeenEntries bound the memory the sets hold: at most this
	// many sets, and this many entries across all of them. The least recently used set
	// is dropped to make room.
	maxSeenSessions = 1024
	maxSeenEntries  = 1 << 16
)

// seenKey names one session's set: a fixed-size digest, so a set costs the same
// whatever the length of the ids it is keyed by.
type seenKey [sha256.Size]byte

// seenPrint changes whenever the entry would read differently: another revision, a
// changed stale flag or another rank state. It is a fixed-size digest for the same
// reason.
type seenPrint [16]byte

type seenSet struct {
	used  time.Time
	shown map[string]seenPrint // entry id -> seen-print
}

type seenSets struct {
	mu   sync.Mutex
	now  func() time.Time
	sets map[seenKey]*seenSet
	// entries counts the entries across all sets.
	entries int
}

var recallSeen = newSeenSets(time.Now)

func newSeenSets(now func() time.Time) *seenSets {
	return &seenSets{now: now, sets: map[seenKey]*seenSet{}}
}

// recallSeenKey is the session's set key; ok is false (no suppression) without a session.
func recallSeenKey(workspaceID, caller, session string) (key seenKey, ok bool) {
	if session == "" {
		return key, false
	}
	b, _ := json.Marshal([]string{workspaceID, caller, session}) // unambiguous for any ids
	return sha256.Sum256(b), true
}

func (c scoredEntry) seenPrint() seenPrint {
	sum := sha256.Sum256([]byte(c.rank.ContentDigest + "|" + c.rank.State + "|" + strconv.FormatBool(c.entry.Stale)))
	return seenPrint(sum[:len(seenPrint{})])
}

// shown returns the seen-prints the session holds for the given candidates only, so a
// lookup costs the page's window, not the set's size.
func (s *seenSets) shown(key seenKey, candidates []scoredEntry) map[string]seenPrint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict()
	set, ok := s.sets[key]
	if !ok {
		return nil
	}
	out := map[string]seenPrint{}
	for _, c := range candidates {
		if p, ok := set.shown[c.rank.ID]; ok {
			out[c.rank.ID] = p
		}
	}
	return out
}

// mark records entries as shown to the session, dropping the least recently used other
// sets while the entries would pass maxSeenEntries.
func (s *seenSets) mark(key seenKey, prints map[string]seenPrint) {
	if len(prints) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict()
	set, ok := s.sets[key]
	if !ok {
		if len(s.sets) >= maxSeenSessions {
			s.drop(s.oldest(key))
		}
		set = &seenSet{shown: map[string]seenPrint{}}
		s.sets[key] = set
	}
	set.used = s.now()
	added := 0
	for id := range prints {
		if _, ok := set.shown[id]; !ok {
			added++
		}
	}
	if len(set.shown)+added > maxSeenEntries {
		// start over: showing an entry twice is the safe failure
		s.entries -= len(set.shown)
		clear(set.shown)
		added = len(prints)
	}
	for s.entries+added > maxSeenEntries && len(s.sets) > 1 {
		s.drop(s.oldest(key))
	}
	for id, p := range prints {
		set.shown[id] = p
	}
	s.entries += added
}

// evict drops the sets unused for recallSeenTTL.
func (s *seenSets) evict() {
	cutoff := s.now().Add(-recallSeenTTL)
	for k, set := range s.sets {
		if set.used.Before(cutoff) {
			s.drop(k)
		}
	}
}

// oldest is the least recently used set other than keep.
func (s *seenSets) oldest(keep seenKey) seenKey {
	var oldest seenKey
	var at time.Time
	found := false
	for k, set := range s.sets {
		if k != keep && (!found || set.used.Before(at)) {
			oldest, at, found = k, set.used, true
		}
	}
	return oldest
}

func (s *seenSets) drop(k seenKey) {
	if set, ok := s.sets[k]; ok {
		s.entries -= len(set.shown)
		delete(s.sets, k)
	}
}
