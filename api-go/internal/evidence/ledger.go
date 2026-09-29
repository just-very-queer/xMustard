package evidence

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The repeat guard (PAR-CTX-06): an identity-bound, per-session ledger of the results
// delivered to an agent. When a read-only call repeats with the same canonical
// arguments against the same repository and memory revisions, and its stable content
// digest (render_key.go) matches the earlier result, the caller may deliver
// "[xmustard repeat] unchanged since call X; handle H" instead of the result again. The
// earlier original stays readable through its handle, so nothing is lost.
//
// The guard never suppresses a call whose arguments, repository identity, memory
// revision or result changed; never applies to side-effecting tools; never points at a
// handle that has expired; and never replaces a result smaller than the notice. Failing
// calls are classified (validation, not_found, permission, timeout, tool_error) and a
// repeated failure is annotated, never suppressed: the same call failing the same way
// more than MaxRepeat times in a row (strict), or one tool failing with one class more
// than 3× MaxRepeat times (6× for exploratory tools) since its last success (coarse).
//
// Ported from cursor-bridge bridge_loop_guard.go (research/cursor-bridge has no git
// history of its own; the source is pinned by SHA-256
// 57d40a97abac47aa61a480782be7830fac8ef6210f4989555ccb166d471ebbc9). Changes from
// upstream: the key adds the repository and memory revisions and the result digest
// (upstream keyed failures by call alone); repeated failures are annotated, not
// blocked; classification also reads the HTTP status; state is bounded (sessions,
// fingerprints per session, fingerprints in all) with LRU eviction.

// ErrorClass classifies one tool result.
type ErrorClass uint8

const (
	ClassSuccess ErrorClass = iota
	ClassValidation
	ClassNotFound
	ClassPermission
	ClassTimeout
	ClassToolError
)

var classNames = [...]string{"success", "validation", "not_found", "permission", "timeout", "tool_error"}

func (c ErrorClass) String() string {
	if int(c) < len(classNames) {
		return classNames[c]
	}
	return classNames[ClassToolError]
}

// statusClasses classify an error by its HTTP status first.
var statusClasses = map[int]ErrorClass{
	http.StatusBadRequest: ClassValidation, http.StatusUnprocessableEntity: ClassValidation,
	http.StatusRequestEntityTooLarge: ClassValidation,
	http.StatusNotFound:              ClassNotFound, http.StatusGone: ClassNotFound,
	http.StatusUnauthorized: ClassPermission, http.StatusForbidden: ClassPermission,
	http.StatusRequestTimeout: ClassTimeout, http.StatusGatewayTimeout: ClassTimeout,
}

// textClasses classify an error by its text when its status says nothing; the first
// rule with a matching phrase wins.
var textClasses = []struct {
	class   ErrorClass
	phrases []string
}{
	{ClassValidation, []string{"is required", "payload is empty", "invalid character", "cannot unmarshal", "decode",
		"invalid argument", "invalid params", "invalid input", "unknown argument", "must be"}},
	{ClassNotFound, []string{"not found", "no such file", "unknown task", "unknown session", "unknown mcp server",
		"unsupported tool", "does not exist"}},
	{ClassPermission, []string{"permission", "unauthorized", "forbidden", "refusing", "access denied"}},
	{ClassTimeout, []string{"timeout", "timed out", "deadline exceeded"}},
}

// ClassifyError classifies a failed result by its HTTP status (0 when none) and error
// text. A result that is not an error is ClassSuccess.
func ClassifyError(isError bool, status int, text string) ErrorClass {
	if !isError && status < http.StatusBadRequest {
		return ClassSuccess
	}
	if c, ok := statusClasses[status]; ok {
		return c
	}
	lower := strings.ToLower(text)
	for _, rule := range textClasses {
		for _, p := range rule.phrases {
			if strings.Contains(lower, p) {
				return rule.class
			}
		}
	}
	return ClassToolError
}

// Scope is who a ledger session belongs to: the workspace, the stable principal ID
// ("" when authentication is not enforced) and the client's session id. Sessions of
// different principals never share entries, whatever their session ids.
type Scope struct {
	Workspace string
	Principal string
	Session   string
}

func (s Scope) key() [16]byte {
	h := sha256.New()
	writeFields(h, "xm-ledger-scope/1", s.Workspace, s.Principal, s.Session)
	var k [16]byte
	copy(k[:], h.Sum(nil))
	return k
}

// Delivered is one delivered result, as the ledger sees it.
type Delivered struct {
	Scope Scope
	Tool  string
	// Args are the call's arguments (any JSON object; canonicalized for the key).
	Args []byte
	Rev  Revisions
	// ReadOnly declares another MCP server's tool free of side effects (its caller
	// trusts that server's readOnlyHint). It never overrides a native or xMustard tool.
	ReadOnly bool
	// CallID is the client's id for the call (tool_use_id, JSON-RPC id); the notice
	// names the earlier call by it.
	CallID string
	// Digest is the delivered result's ContentDigest (render_key.go).
	Digest [32]byte
	// Bytes is the size of the delivered result the notice would replace.
	Bytes int
	// IsError, Status and ErrorText describe a failure (ClassifyError).
	IsError   bool
	Status    int
	ErrorText string
	// Handle and ExpiresAt are the recovery handle of the result's original and its
	// expiry; a result without a live handle is never replaced by a notice.
	Handle    string
	ExpiresAt time.Time
}

// Decision is the ledger's answer for one delivered result.
type Decision struct {
	// Exempt is set when the ledger did not consider the call; Reason says why
	// (side_effecting, identity_incomplete, revision_missing, args_invalid,
	// digest_missing).
	Exempt bool
	Reason string
	// Unchanged means the result repeats an earlier one: deliver Notice instead.
	Unchanged bool
	// SinceCall and SinceSeq name the earlier call (its client id, and its ordinal in
	// the session); Handle and ExpiresAt recover its original; Repeats counts the
	// unchanged repeats of it so far.
	SinceCall string
	SinceSeq  uint32
	Handle    string
	ExpiresAt time.Time
	Repeats   int
	// Class is the result's error class; Failing is set when the failure repeats past a
	// limit (an annotation to deliver with the result, never instead of it).
	Class   ErrorClass
	Failing *FailingRepeat
}

// FailingRepeat describes a failure repeated past its limit.
type FailingRepeat struct {
	Class ErrorClass
	// Scope is "strict" (this call, these arguments, this revision) or "coarse" (this
	// tool, this class, since its last success).
	Scope string
	Count int
	Limit int
}

// NoticePrefix starts every repeat notice.
const NoticePrefix = "[xmustard repeat] "

// Notice is the text delivered for d: the unchanged-result notice (with the handle's
// resource URI in workspace) and/or the failing-repeat annotation; "" when neither.
func (d Decision) Notice(workspace string) string {
	var parts []string
	if d.Unchanged {
		m := map[string]any{
			"unchanged": true, "since_call": d.SinceCall, "since_seq": d.SinceSeq, "repeats": d.Repeats,
			"handle": d.Handle, "resource_uri": ResourceURI(d.Handle, workspace),
			"expires_at": d.ExpiresAt.UTC().Format(time.RFC3339),
			"note": "same arguments, repository and memory revisions and result as the earlier call; " +
				"read its original through resource_uri",
		}
		if d.SinceCall == "" {
			delete(m, "since_call")
		}
		parts = append(parts, noticeJSON(m))
	}
	if f := d.Failing; f != nil {
		parts = append(parts, noticeJSON(map[string]any{
			"repeated_failure": f.Class.String(), "scope": f.Scope, "count": f.Count, "limit": f.Limit,
			"advice": failureAdvice[f.Class],
		}))
	}
	return strings.Join(parts, "\n")
}

// failureAdvice is what a repeated failure of each class asks the agent to change.
var failureAdvice = map[ErrorClass]string{
	ClassValidation: "the arguments are rejected the same way each time; fix them against the tool's schema before retrying",
	ClassNotFound:   "the target does not exist at this revision; search for it or change the target before retrying",
	ClassPermission: "the caller lacks the permission; retrying will not change it",
	ClassTimeout:    "the call keeps timing out; narrow it (path, limit) before retrying",
	ClassToolError:  "the tool keeps failing the same way; use another tool or change the arguments before retrying",
}

func noticeJSON(m map[string]any) string {
	b, _ := canonicalJSON(m)
	return NoticePrefix + string(b)
}

// LedgerLimits bound one ledger.
type LedgerLimits struct {
	// Sessions is the number of sessions kept; the least recently used is dropped.
	Sessions int
	// PerSession is the number of fingerprints one session keeps (LRU).
	PerSession int
	// MaxEntries bounds fingerprints across all sessions: past it, whole least recently
	// used sessions are dropped.
	MaxEntries int
	// MaxRepeat is the strict failing-repeat limit; the coarse limits are 3× and (for
	// exploratory tools) 6× it.
	MaxRepeat int
	// MinBytes is the smallest result a notice replaces (a notice is about 400 bytes).
	MinBytes int
	// MaxCallID bounds a stored call id; a longer id is not kept (the notice then names
	// the earlier call by its ordinal only).
	MaxCallID int
}

// DefaultLedgerLimits are the limits measured against the WS-42 budget line: a
// fingerprint costs about 220 bytes of live heap (entry, call id, map slot), so 2,048
// fingerprints hold about 0.43 MiB (TestEvidenceCachesStayWithinTheirBudgetLine).
func DefaultLedgerLimits() LedgerLimits {
	return LedgerLimits{Sessions: 64, PerSession: 256, MaxEntries: 2048, MaxRepeat: 2, MinBytes: 512, MaxCallID: 64}
}

// noticeMinTTL is the least time (seconds) the earlier handle must still live for a
// notice to point at it; past that the new result is delivered and its handle kept.
const noticeMinTTL = 60

// maxCoarse bounds a session's coarse failure counters (one per failing tool and class).
const maxCoarse = 64

// coarse repeat limits, as multiples of MaxRepeat.
const (
	coarseFactor            = 3
	exploratoryCoarseFactor = 6
)

// Ledger is the repeat guard. It is safe for concurrent use.
type Ledger struct {
	limits LedgerLimits
	now    func() time.Time

	mu       sync.Mutex
	sessions map[[16]byte]*ledgerSession
	lru      ledgerSession // sentinel of the session LRU list (most recent after it)
	entries  int
}

// NewLedger returns an empty ledger; zero limits take their defaults.
func NewLedger(limits LedgerLimits) *Ledger {
	def := DefaultLedgerLimits()
	for _, f := range []struct{ v, d *int }{
		{&limits.Sessions, &def.Sessions}, {&limits.PerSession, &def.PerSession}, {&limits.MaxEntries, &def.MaxEntries},
		{&limits.MaxRepeat, &def.MaxRepeat}, {&limits.MinBytes, &def.MinBytes}, {&limits.MaxCallID, &def.MaxCallID},
	} {
		if *f.v <= 0 {
			*f.v = *f.d
		}
	}
	limits.MaxEntries = max(limits.MaxEntries, limits.PerSession)
	l := &Ledger{limits: limits, now: time.Now, sessions: map[[16]byte]*ledgerSession{}}
	l.lru.prev, l.lru.next = &l.lru, &l.lru
	return l
}

// SetClock replaces the ledger clock (tests).
func (l *Ledger) SetClock(now func() time.Time) { l.now = now }

// ledgerSession is one scope's fingerprints, most recent first.
type ledgerSession struct {
	key        [16]byte
	prev, next *ledgerSession
	entries    map[[16]byte]*ledgerEntry
	head       ledgerEntry // sentinel of the entry LRU list
	seq        uint32
	// coarse counts failures per (tool, class) since the tool's last success.
	coarse map[coarseKey]uint16
}

type coarseKey struct {
	tool  [8]byte
	class ErrorClass
}

// ledgerEntry is one fingerprint: the last result of one key in one session. It is kept
// small (the ledger holds up to MaxEntries of them): digests are truncated to 128 bits
// and the handle is kept as its 32 decoded bytes.
type ledgerEntry struct {
	key, digest [16]byte
	handle      [32]byte
	expires     int64 // unix seconds; 0 when there is no handle
	callID      string
	seq         uint32
	repeats     uint16
	fails       uint16
	class       ErrorClass
	prev, next  *ledgerEntry
}

// Observe records one delivered result and decides whether it repeats an earlier one.
func (l *Ledger) Observe(o Delivered) Decision {
	pol := ToolPolicyFor(o.Tool)
	if pol.SideEffecting && !(o.ReadOnly && foreignTool(o.Tool)) {
		return Decision{Exempt: true, Reason: "side_effecting"}
	}
	k, err := NewKey(o.Tool, o.Args, o.Rev)
	if err != nil {
		return Decision{Exempt: true, Reason: keyErrorReason(err)}
	}
	if o.Digest == ([32]byte{}) {
		// A consumer that left the content digest unset would otherwise compare equal
		// to every other keyless observation, and a changed result at the same
		// revision would be reported unchanged. The guard is keyed on the raw sha, so
		// it refuses to trust a zero digest rather than guess it is a real one.
		return Decision{Exempt: true, Reason: "digest_missing"}
	}
	class := ClassifyError(o.IsError, o.Status, o.ErrorText)
	var key, digest [16]byte
	kd := k.Digest()
	copy(key[:], kd[:])
	copy(digest[:], o.Digest[:])
	handle, hasHandle := decodeHandle(o.Handle)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.session(o.Scope.key())
	s.seq++
	d := Decision{Class: class}
	e := s.entries[key]
	switch {
	case e != nil && class == ClassSuccess && e.digest == digest && e.expires-now.Unix() >= noticeMinTTL && o.Bytes >= l.limits.MinBytes:
		e.repeats++
		d.Unchanged, d.SinceCall, d.SinceSeq, d.Repeats = true, e.callID, e.seq, int(e.repeats)
		d.Handle, d.ExpiresAt = encodeHandle(e.handle), time.Unix(e.expires, 0)
	case e != nil:
		e.digest, e.seq, e.repeats = digest, s.seq, 0
		e.callID = l.callID(o.CallID)
		e.setHandle(handle, hasHandle, o.ExpiresAt)
	default:
		e = &ledgerEntry{key: key, digest: digest, seq: s.seq, callID: l.callID(o.CallID)}
		e.setHandle(handle, hasHandle, o.ExpiresAt)
		s.entries[key] = e
		l.entries++
	}
	s.touch(e)
	d.Failing = l.countFailure(s, e, pol, o.Tool, class)
	l.evict(s)
	return d
}

// countFailure updates the strict and coarse failure counts and reports a failure past
// its limit.
func (l *Ledger) countFailure(s *ledgerSession, e *ledgerEntry, pol ToolPolicy, tool string, class ErrorClass) *FailingRepeat {
	toolKey := coarseKey{class: class}
	th := sha256.Sum256([]byte(ownToolName(tool)))
	copy(toolKey.tool[:], th[:])
	if class == ClassSuccess {
		e.fails, e.class = 0, ClassSuccess
		for c := range classNames {
			delete(s.coarse, coarseKey{tool: toolKey.tool, class: ErrorClass(c)})
		}
		return nil
	}
	if e.class != class {
		e.fails = 0
	}
	e.class, e.fails = class, min(e.fails+1, 1<<15)
	if _, ok := s.coarse[toolKey]; !ok && len(s.coarse) >= maxCoarse {
		clear(s.coarse) // bounded: a session failing with this many tools starts counting afresh
	}
	n := min(s.coarse[toolKey]+1, 1<<15)
	s.coarse[toolKey] = n
	coarseLimit := l.limits.MaxRepeat * coarseFactor
	if pol.Exploratory {
		coarseLimit = l.limits.MaxRepeat * exploratoryCoarseFactor
	}
	switch {
	case int(e.fails) > l.limits.MaxRepeat:
		return &FailingRepeat{Class: class, Scope: "strict", Count: int(e.fails), Limit: l.limits.MaxRepeat}
	case int(n) > coarseLimit:
		return &FailingRepeat{Class: class, Scope: "coarse", Count: int(n), Limit: coarseLimit}
	}
	return nil
}

// session returns the scope's session, creating it when new, and marks it most
// recently used (evict drops sessions past the limits).
func (l *Ledger) session(k [16]byte) *ledgerSession {
	s, ok := l.sessions[k]
	if !ok {
		s = &ledgerSession{key: k, entries: map[[16]byte]*ledgerEntry{}, coarse: map[coarseKey]uint16{}}
		s.head.prev, s.head.next = &s.head, &s.head
		l.sessions[k] = s
	} else {
		s.prev.next, s.next.prev = s.next, s.prev
	}
	s.prev, s.next = &l.lru, l.lru.next
	l.lru.next.prev = s
	l.lru.next = s
	return s
}

// evict applies the limits after an insert into s: its own fingerprint LRU, then the
// session count and the total, dropping whole least recently used sessions (never s).
func (l *Ledger) evict(s *ledgerSession) {
	for len(s.entries) > l.limits.PerSession {
		s.drop(s.head.prev)
		l.entries--
	}
	for len(l.sessions) > l.limits.Sessions || l.entries > l.limits.MaxEntries {
		old := l.lru.prev
		if old == s || old == &l.lru {
			return
		}
		old.prev.next, old.next.prev = old.next, old.prev
		delete(l.sessions, old.key)
		l.entries -= len(old.entries)
	}
}

// Len reports the sessions and fingerprints held (tests, diagnostics).
func (l *Ledger) Len() (sessions, entries int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sessions), l.entries
}

func (l *Ledger) callID(id string) string {
	if len(id) > l.limits.MaxCallID {
		return ""
	}
	return strings.Clone(id)
}

// touch moves e to the front of its session's LRU list.
func (s *ledgerSession) touch(e *ledgerEntry) {
	if e.next != nil {
		e.prev.next, e.next.prev = e.next, e.prev
	}
	e.prev, e.next = &s.head, s.head.next
	s.head.next.prev = e
	s.head.next = e
}

func (s *ledgerSession) drop(e *ledgerEntry) {
	e.prev.next, e.next.prev = e.next, e.prev
	delete(s.entries, e.key)
}

func (e *ledgerEntry) setHandle(h [32]byte, ok bool, expires time.Time) {
	e.handle, e.expires = h, 0
	if ok && !expires.IsZero() {
		e.expires = expires.Unix()
	}
}

// keyErrorReasons name why a call has no key.
var keyErrorReasons = []struct {
	err    error
	reason string
}{
	{ErrIdentityIncomplete, "identity_incomplete"}, {ErrRevisionMissing, "revision_missing"}, {ErrArgsInvalid, "args_invalid"},
}

func keyErrorReason(err error) string {
	for _, r := range keyErrorReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return "not_keyable"
}

// decodeHandle returns the 32 random bytes of an "xm1." handle.
func decodeHandle(h string) ([32]byte, bool) {
	var out [32]byte
	rest, ok := strings.CutPrefix(h, HandlePrefix)
	if !ok || len(rest) != base64.RawURLEncoding.EncodedLen(len(out)) {
		return out, false
	}
	n, err := base64.RawURLEncoding.Decode(out[:], []byte(rest))
	return out, err == nil && n == len(out)
}

func encodeHandle(b [32]byte) string {
	return HandlePrefix + base64.RawURLEncoding.EncodeToString(b[:])
}

// MarshalJSON renders a class by name.
func (c ErrorClass) MarshalJSON() ([]byte, error) { return json.Marshal(c.String()) }
