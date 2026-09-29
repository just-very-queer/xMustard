package evidence

import "time"

// Retention classes (WS-41, PAR-CTX-09). Retained originals fall in three classes with
// their own TTL and quota share, so a burst of one kind cannot expire or starve
// another:
//   - hook: a client's native tool output captured through a hook or the command wrapper
//   - own: an xMustard tool's result retained for its handle
//   - snapshot: a point-in-time capture kept for later comparison
//
// A class's TTL is a multiple of Limits.Retention and its quota a share of
// Limits.WorkspaceQuota, so the operator's lowering overrides scale every class. The
// workspace quota still bounds the sum. An unexpired original is never evicted to make
// room: a full class refuses the new capture (ErrQuotaFull), which the wrapper answers
// with a bounded passthrough.

// RetentionClass names a retention policy row.
type RetentionClass string

const (
	ClassHook     RetentionClass = "hook"
	ClassOwn      RetentionClass = "own"
	ClassSnapshot RetentionClass = "snapshot"
)

type classPolicy struct {
	ttlFactor int64 // multiple of Limits.Retention
	quotaPerK int64 // share of Limits.WorkspaceQuota, in thousandths
}

// classPolicies is the closed set of classes. Hook captures are universal and the
// most numerous, so they get the smaller share; the promises xMustard made in its own
// results keep their room.
var classPolicies = map[RetentionClass]classPolicy{
	ClassHook:     {ttlFactor: 1, quotaPerK: 750},
	ClassOwn:      {ttlFactor: 1, quotaPerK: 1000},
	ClassSnapshot: {ttlFactor: 3, quotaPerK: 250},
}

// Classes lists the classes in policy-table order (tests, diagnostics).
func Classes() []RetentionClass { return []RetentionClass{ClassHook, ClassOwn, ClassSnapshot} }

// normalize maps an empty or unknown class to ClassOwn, the class every capture had
// before classes existed.
func (c RetentionClass) normalize() RetentionClass {
	if _, ok := classPolicies[c]; ok {
		return c
	}
	return ClassOwn
}

// TTL is how long an original of class c is kept under limits l.
func (l Limits) TTL(c RetentionClass) time.Duration {
	return l.Retention * time.Duration(classPolicies[c.normalize()].ttlFactor)
}

// ClassQuota is the retained bytes class c may hold under limits l.
func (l Limits) ClassQuota(c RetentionClass) int64 {
	return l.WorkspaceQuota * classPolicies[c.normalize()].quotaPerK / 1000
}

// charge is what the observation counts against its quotas: an original held by the
// client (an upstream reference) costs no bytes.
func (o Observation) charge() int64 {
	if o.Upstream != nil {
		return 0
	}
	return retainedCharge(o.RawBytes)
}

// admits reports whether n more bytes fit the workspace quota and class c's quota,
// given what is retained, pending in open spools, and own (the caller's bytes already
// counted in pending, which convert to the class's retained charge).
func (s *Store) admits(st *wsState, c RetentionClass, used, own, n int64) bool {
	return used+st.pending+n <= s.limits.WorkspaceQuota && st.byClass[c]+own+n <= s.limits.ClassQuota(c)
}
