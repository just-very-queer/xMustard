// Package evidence is xMustard's bounded, recoverable delivery of tool results.
//
// One narrow interface: Capture stores a scoped original (raw bytes on disk, never
// in model context) and returns a bounded deterministic projection plus, when
// anything was omitted, an opaque recovery handle; Read serves authorized byte pages
// of that exact original until it expires. Selection, admission, raw retention and
// expiry all live here. Captured evidence is an observation, not verified memory.
//
// Handles are "xm1." + base64url(32 random bytes). On disk an artifact is keyed by
// SHA-256(handle), so a directory listing never reveals a usable handle. A handle is
// bound to its workspace and (when authentication is enforced) to the issuing
// principal's stable ID; session and call IDs are audit metadata only.
package evidence

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/injection"
)

// Default limits. Configuration may LOWER them (XMUSTARD_EVIDENCE_*); an increase needs
// a new resource measurement and retention statement, so larger values are clamped.
//
// DefaultMaxOriginal bounds the spool, which streams to disk in O(window) memory, so it
// is not derived from the transient pool: posted results and streamed tool output reach
// it under any pool. A core-backed tool's result is decoded and re-encoded in memory
// before it reaches the spool, so its own bound is the pool-derived core output cap
// (rustcore.coreStdoutCap: about 7 MiB under the default 24 MiB pool, 16 MiB from a
// pool of about 52 MiB); past it the tool answers the permanent "output too large"
// error, never a truncated capture (WS-FIX-02).
const (
	DefaultMaxOriginal      = 16 << 20
	DefaultMaxProjection    = 1 << 20
	DefaultProjectionTarget = 64 << 10
	DefaultPageSize         = 64 << 10
	DefaultWorkspaceQuota   = 256 << 20
	DefaultRetention        = 24 * time.Hour
	HandlePrefix            = "xm1."
	DeliveryVersion         = "xmustard.evidence/v1"
	ResourceScheme          = "xmustard://evidence/"
)

// MinRetainedCharge is the least one retained original counts against its workspace
// quota. Each original also keeps a metadata file and a directory the byte count does
// not see; since a capture may ask for a 1 KiB target, charging small originals at
// least this much keeps the number of originals (and that overhead) per quota where it
// was when every retained original exceeded a 16 KiB client target.
const MinRetainedCharge = 16 << 10

// retainedCharge is what an original of raw bytes counts against the quota.
func retainedCharge(raw int64) int64 { return max(raw, MinRetainedCharge) }

var (
	ErrMissing       = errors.New("evidence not found")
	ErrExpired       = errors.New("evidence expired")
	ErrDenied        = errors.New("evidence access denied")
	ErrRevoked       = errors.New("evidence revoked")
	ErrInvalidHandle = errors.New("invalid evidence handle")
	ErrQuotaFull     = errors.New("evidence retention quota full; original not retained")
	ErrTooLarge      = errors.New("tool output exceeds the evidence capture limit")
	ErrCorrupt       = errors.New("retained evidence is corrupt or truncated")
	ErrUnsupported   = errors.New("unsupported or invalid structured tool output exceeds the projection limit")
)

// Limits bound one store.
type Limits struct {
	MaxOriginal      int64         // per captured original
	MaxProjection    int           // hard cap per delivered projection
	ProjectionTarget int           // results at or below this pass unchanged; larger are reduced to it
	PageSize         int           // max bytes per expansion page
	WorkspaceQuota   int64         // retained unexpired originals per workspace
	Retention        time.Duration // default expiry
}

// DefaultLimits returns the plan's initial limits.
func DefaultLimits() Limits {
	return Limits{DefaultMaxOriginal, DefaultMaxProjection, DefaultProjectionTarget, DefaultPageSize, DefaultWorkspaceQuota, DefaultRetention}
}

// LimitsFromEnv applies operator overrides, accepting only values that lower a limit.
func LimitsFromEnv() Limits {
	l := DefaultLimits()
	lower := func(name string, cur int64) int64 {
		if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64); err == nil && v > 0 && v < cur {
			return v
		}
		return cur
	}
	l.MaxOriginal = lower("XMUSTARD_EVIDENCE_MAX_ORIGINAL_BYTES", l.MaxOriginal)
	l.MaxProjection = int(lower("XMUSTARD_EVIDENCE_MAX_PROJECTION_BYTES", int64(l.MaxProjection)))
	l.ProjectionTarget = int(lower("XMUSTARD_EVIDENCE_PROJECTION_BYTES", int64(l.ProjectionTarget)))
	l.PageSize = int(lower("XMUSTARD_EVIDENCE_PAGE_BYTES", int64(l.PageSize)))
	l.WorkspaceQuota = lower("XMUSTARD_EVIDENCE_WORKSPACE_QUOTA_BYTES", l.WorkspaceQuota)
	if secs := lower("XMUSTARD_EVIDENCE_RETENTION_SECONDS", int64(l.Retention/time.Second)); secs > 0 {
		l.Retention = time.Duration(secs) * time.Second
	}
	if l.ProjectionTarget > l.MaxProjection {
		l.ProjectionTarget = l.MaxProjection
	}
	return l
}

// Observation is the persisted, audit-complete record of one captured original.
type Observation struct {
	Version        string `json:"version"`
	WorkspaceID    string `json:"workspace_id"`
	RepoScope      string `json:"repo_scope"` // canonical repository root (trust scope)
	Actor          string `json:"actor,omitempty"`
	AuthEnforced   bool   `json:"auth_enforced"`
	Issuer         string `json:"issuer"` // mcp | pi | http
	SessionID      string `json:"session_id,omitempty"`
	CallID         string `json:"call_id,omitempty"`
	Tool           string `json:"tool"`
	ToolVersion    string `json:"tool_version,omitempty"`
	ArgsDigest     string `json:"args_digest"`
	CapturedAt     string `json:"captured_at"`
	CapturedKey    string `json:"captured_key"`
	CapturedKeyOK  bool   `json:"captured_identity_complete"`
	Status         int    `json:"status"`
	IsError        bool   `json:"is_error"`
	ContentType    string `json:"content_type"`
	RawSHA256      string `json:"raw_sha256"`
	RawBytes       int64  `json:"raw_bytes"`
	ExpiresAt      string `json:"expires_at"`
	WorkspaceQuota int64  `json:"workspace_quota"`
	Revoked        bool   `json:"revoked,omitempty"`
	Projection     Record `json:"projection"`
	// Quarantine says why content derived from this capture is quarantined (WS-56): it
	// captured an untrusted tool (injection.CaptureQuarantine), or an xMustard result
	// that carried quarantined memory (injection.CarriedQuarantine). Empty when it is not.
	Quarantine string `json:"quarantine,omitempty"`
}

// CaptureRequest describes one tool result being delivered.
type CaptureRequest struct {
	WorkspaceID  string
	RepoScope    string
	Actor        string // stable Principal.ID when authenticated
	AuthEnforced bool
	Issuer       string
	SessionID    string
	CallID       string
	Tool         string
	ToolVersion  string
	ArgsDigest   string
	Status       int
	IsError      bool
	ContentType  string
	// BeforeKey is the identity sampled before the tool executed (nil when unknown).
	BeforeKey *Identity
	// RepoKey returns the repository identity at capture time. It is asked only when
	// BeforeKey is complete, because only then can the two bind.
	RepoKey func(ctx context.Context) Identity
	// Retain keeps the original even though no reduction ran: the caller projected
	// the result itself (ground's output budget) and names the returned handle in its
	// reply. The delivery then carries no projection.
	Retain bool
}

// Identity is a repository identity observation (Rust `repo-key` contract: key plus
// whether it covers the complete working state). An incomplete or failed identity can
// never make evidence look current. Cached and AgeMs say whether it was served from
// the identity cache and how long ago it was sampled.
type Identity struct {
	Key         string   `json:"key"`
	Complete    bool     `json:"identity_complete"`
	Limitations []string `json:"limitations,omitempty"`
	Cached      bool     `json:"cached,omitempty"`
	AgeMs       int64    `json:"age_ms,omitempty"`
}

// Delivery is what travels in the tool result: the projection and how to recover the rest.
type Delivery struct {
	Delivery       string     `json:"delivery"`
	Tool           string     `json:"tool"`
	CallID         string     `json:"call_id,omitempty"`
	Status         int        `json:"status"`
	IsError        bool       `json:"is_error"`
	ContentType    string     `json:"content_type"`
	Reduced        bool       `json:"reduced"`
	Projection     string     `json:"projection"`
	Handle         string     `json:"handle,omitempty"`
	ResourceURI    string     `json:"resource_uri,omitempty"`
	ExpiresAt      string     `json:"expires_at,omitempty"`
	CapturedKey    string     `json:"captured_key,omitempty"`
	RawBytes       int64      `json:"raw_bytes"`
	RawSHA256      string     `json:"raw_sha256"`
	ProjectedBytes int        `json:"projected_bytes"`
	Reducer        string     `json:"reducer"`
	Omissions      []Omission `json:"omissions,omitempty"`
	PageSize       int        `json:"page_size,omitempty"`
	ProjectionMode string     `json:"projection_mode"`
	// CapturedIdentity is "bound" when complete identities before and after execution
	// agree, otherwise "unknown".
	CapturedIdentity string `json:"captured_identity"`
	// InjectionFlags are the instruction patterns the projection matches (WS-56): tool
	// output is data, and a client should present flagged output as such.
	InjectionFlags []string `json:"injection_flags,omitempty"`
}

// Page is one authorized byte range of a retained original, base64-encoded (standard
// encoding) so arbitrary bytes — split UTF-8 sequences, invalid UTF-8 — round-trip.
type Page struct {
	Handle      string `json:"handle"`
	Tool        string `json:"tool"`
	CallID      string `json:"call_id,omitempty"`
	ContentType string `json:"content_type"`
	Offset      int64  `json:"offset"`
	Length      int    `json:"length"`
	TotalBytes  int64  `json:"total_bytes"`
	NextOffset  int64  `json:"next_offset"`
	EOF         bool   `json:"eof"`
	Encoding    string `json:"encoding"`
	Data        string `json:"data"`
	RawSHA256   string `json:"raw_sha256"`
	CapturedKey string `json:"captured_key"`
	CurrentKey  string `json:"current_key"`
	// Freshness is "current" only when both identities are complete and equal,
	// "stale" when they differ, and "unknown" when either is incomplete or failed.
	// Stale is true unless Freshness is "current".
	Freshness string `json:"freshness"`
	Stale     bool   `json:"stale"`
	ExpiresAt string `json:"expires_at"`
	// Quarantine is the capture's Observation.Quarantine.
	Quarantine string `json:"quarantine,omitempty"`
	// CurrentKeyCached / CurrentKeyAgeMs: the current identity came from the identity
	// cache (no repo-key run for this page) and was sampled that long ago. A capture
	// whose identity is not bound never needs the current identity, so none is read.
	CurrentKeyCached bool  `json:"current_key_cached"`
	CurrentKeyAgeMs  int64 `json:"current_key_age_ms"`
}

// ReadRequest asks for one page.
type ReadRequest struct {
	WorkspaceID  string
	Handle       string
	Actor        string
	AuthEnforced bool
	Offset       int64
	Length       int
	RepoKey      func(ctx context.Context) Identity
}

// Store is the on-disk evidence store rooted at <dataDir>/evidence.
type Store struct {
	root   string
	limits Limits
	now    func() time.Time

	mu     sync.Mutex          // guards states and active only
	states map[string]*wsState // per-workspace accounting
	active map[string]struct{} // spool files currently being written (never swept)
}

// wsState is one workspace's quota accounting. Every field is guarded by mu.
type wsState struct {
	mu      sync.Mutex
	loaded  bool  // used reflects disk (rebuilt lazily after restart or on pressure)
	used    int64 // quota charge of retained unexpired originals (retainedCharge)
	pending int64 // bytes in open spools
}

// NewStore opens (without scanning) the store under dataDir.
func NewStore(dataDir string, limits Limits) *Store {
	return &Store{root: filepath.Join(dataDir, "evidence"), limits: limits, now: time.Now,
		states: map[string]*wsState{}, active: map[string]struct{}{}}
}

// Limits reports the store's limits.
func (s *Store) Limits() Limits { return s.limits }

// SetClock replaces the store clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

var safeWorkspace = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func (s *Store) wsDir(ws string) (string, error) {
	if ws == "" || strings.Contains(ws, "..") || !safeWorkspace.MatchString(ws) {
		return "", fmt.Errorf("%w: workspace", ErrInvalidHandle)
	}
	return filepath.Join(s.root, ws), nil
}

func (s *Store) state(ws string) *wsState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[ws]
	if !ok {
		st = &wsState{}
		s.states[ws] = st
	}
	return st
}

// NewHandle returns a fresh "xm1." handle with 256 random bits.
func NewHandle() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return HandlePrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// handleKey validates a handle's format and returns its on-disk key.
func handleKey(handle string) (string, error) {
	if !strings.HasPrefix(handle, HandlePrefix) {
		return "", ErrInvalidHandle
	}
	raw, err := base64.RawURLEncoding.DecodeString(handle[len(HandlePrefix):])
	if err != nil || len(raw) != 32 {
		return "", ErrInvalidHandle
	}
	sum := sha256.Sum256([]byte(handle))
	return hex.EncodeToString(sum[:]), nil
}

// ResourceURI is the MCP resource URI for a handle. It carries the workspace so the
// exact issued URI recovers the original after a client or shim restart (the server
// still enforces workspace and principal scope on every read).
func ResourceURI(handle, workspaceID string) string {
	return ResourceScheme + handle + "?workspace_id=" + url.QueryEscape(workspaceID)
}

// Spool is a size-capped on-disk buffer for a tool result being captured.
type Spool struct {
	f         *os.File
	n         int64
	max       int64
	over      bool
	quotaFull bool
	store     *Store
	ws        string
	st        *wsState
	discarded bool
}

// NewSpool opens a spool file in the workspace's evidence directory.
func (s *Store) NewSpool(ws string) (*Spool, error) {
	dir, err := s.wsDir(ws)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".spool-*")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.active[f.Name()] = struct{}{}
	s.mu.Unlock()
	return &Spool{f: f, max: s.limits.MaxOriginal, store: s, ws: ws, st: s.state(ws)}, nil
}

// Write appends to the spool; past the capture cap or the workspace quota it stops
// storing and reports why. Spooled bytes count against the workspace quota while
// pending, except a projection-target-sized allowance that never needs retention.
func (sp *Spool) Write(p []byte) (int, error) {
	if sp.over || sp.quotaFull {
		return len(p), nil
	}
	if sp.n+int64(len(p)) > sp.max {
		sp.over = true
		return len(p), nil
	}
	if !sp.store.admitPending(sp, int64(len(p))) {
		sp.quotaFull = true
		return len(p), nil
	}
	n, err := sp.f.Write(p)
	sp.n += int64(n)
	return n, err
}

// admitPending charges n spool bytes to the workspace, reclaiming expired originals
// before refusing. Unexpired originals are never evicted.
func (s *Store) admitPending(sp *Spool, n int64) bool {
	st := sp.st
	st.mu.Lock()
	defer st.mu.Unlock()
	free := sp.n+n <= int64(s.limits.ProjectionTarget)
	if !free {
		used, err := s.retainedLocked(sp.ws, st, false)
		if err != nil {
			return false
		}
		if used+st.pending+n > s.limits.WorkspaceQuota {
			if used, err = s.retainedLocked(sp.ws, st, true); err != nil || used+st.pending+n > s.limits.WorkspaceQuota {
				return false
			}
		}
	}
	st.pending += n
	return true
}

// QuotaFull reports whether the spool was stopped by the workspace quota.
func (sp *Spool) QuotaFull() bool { return sp.quotaFull }

// PendingBytes reports bytes held by open spools of a workspace (diagnostics/tests).
func (s *Store) PendingBytes(ws string) int64 { return s.pendingBytes(ws) }

func (s *Store) pendingBytes(ws string) int64 {
	st := s.state(ws)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.pending
}

// Over reports whether the spooled output exceeded the capture cap.
func (sp *Spool) Over() bool { return sp.over }

// Discard removes the spool file.
func (sp *Spool) Discard() {
	if sp.discarded {
		return
	}
	sp.discarded = true
	name := sp.f.Name()
	_ = sp.f.Close()
	_ = os.Remove(name)
	sp.store.mu.Lock()
	delete(sp.store.active, name)
	sp.store.mu.Unlock()
	sp.st.mu.Lock()
	sp.st.pending = max(0, sp.st.pending-sp.n)
	sp.st.mu.Unlock()
}

// Capture stores the spooled original (when reduction omits anything, or the caller
// asks to Retain it) and returns the delivery. It always consumes the spool.
func (s *Store) Capture(ctx context.Context, sp *Spool, req CaptureRequest) (*Delivery, error) {
	defer sp.Discard()
	if sp.ws != req.WorkspaceID {
		return nil, fmt.Errorf("%w: spool belongs to another workspace", ErrInvalidHandle)
	}
	if req.AuthEnforced && req.Actor == "" {
		return nil, fmt.Errorf("%w: enforced authentication requires a stable principal", ErrDenied)
	}
	if sp.over {
		return nil, fmt.Errorf("%w (%d bytes)", ErrTooLarge, s.limits.MaxOriginal)
	}
	if sp.quotaFull {
		return nil, fmt.Errorf("%w (workspace %s quota %d bytes)", ErrQuotaFull, req.WorkspaceID, s.limits.WorkspaceQuota)
	}
	if err := ctx.Err(); err != nil {
		return nil, err // a cancelled call never issues a handle
	}
	sum, carried, err := digestFile(ctx, sp.f, sp.n, injection.OwnTool(req.Tool))
	if err != nil {
		return nil, err
	}
	budget.NoteCapture(sp.n)
	// Captures stream in O(window) memory and never use the heavy slot; anything this
	// capture calls (reduction, identity sampling) must not wait for it either.
	ctx = budget.WithoutHeavyWait(ctx)
	d := &Delivery{Delivery: DeliveryVersion, Tool: req.Tool, CallID: req.CallID, Status: req.Status,
		IsError: req.IsError, ContentType: req.ContentType, RawBytes: sp.n, RawSHA256: sum, Reducer: ReducerVersion}
	var proj string
	rec := Record{Reducer: ReducerVersion, Mode: "retained", RawBytes: sp.n}
	if !req.Retain {
		if proj, rec, err = Reduce(ctx, sp.f, sp.n, req.ContentType, s.limits.ProjectionTarget, s.limits.MaxProjection); err != nil {
			return nil, err
		}
	}
	d.Projection, d.ProjectedBytes, d.Omissions, d.Reduced, d.ProjectionMode = proj, len(proj), rec.Omissions, rec.Reduced, rec.Mode
	d.InjectionFlags = injection.ScanText(proj).Flags
	if !rec.Reduced && !req.Retain {
		// nothing omitted: nothing retained, no handle, identity not sampled
		d.CapturedIdentity = "unknown"
		return d, nil
	}
	handle, err := NewHandle()
	if err != nil {
		return nil, err
	}
	key, _ := handleKey(handle)
	now := s.now().UTC()
	obs := Observation{
		Version: DeliveryVersion, WorkspaceID: req.WorkspaceID, RepoScope: req.RepoScope,
		Actor: req.Actor, AuthEnforced: req.AuthEnforced, Issuer: req.Issuer, SessionID: req.SessionID,
		CallID: req.CallID, Tool: req.Tool, ToolVersion: req.ToolVersion, ArgsDigest: req.ArgsDigest,
		CapturedAt: now.Format(time.RFC3339Nano), Status: req.Status, IsError: req.IsError,
		ContentType: req.ContentType, RawSHA256: sum, RawBytes: sp.n,
		ExpiresAt: now.Add(s.limits.Retention).Format(time.RFC3339Nano), WorkspaceQuota: s.limits.WorkspaceQuota,
		Projection: rec, Quarantine: cmp.Or(injection.CaptureQuarantine(req.Tool), carried),
	}
	// identity is bound only when complete identities sampled before and after
	// execution agree; otherwise freshness of this evidence is unknown forever. An
	// incomplete or missing before-identity can never bind, so the after-identity is
	// not sampled for it.
	if b := req.BeforeKey; b != nil && b.Complete && b.Key != "" && req.RepoKey != nil {
		after := req.RepoKey(ctx)
		obs.CapturedKey = after.Key
		obs.CapturedKeyOK = after.Complete && after.Key != "" && b.Key == after.Key
	} else if b != nil {
		obs.CapturedKey = b.Key
	}
	d.CapturedIdentity = "unknown"
	if obs.CapturedKeyOK {
		d.CapturedIdentity = "bound"
	}
	st := sp.st
	st.mu.Lock()
	defer st.mu.Unlock()
	used, err := s.retainedLocked(req.WorkspaceID, st, false)
	if err != nil {
		return nil, err
	}
	// this spool's bytes are already inside pending; converting them to retained adds
	// only the charge beyond them (re-checked after a reclaim).
	extra := retainedCharge(sp.n) - sp.n
	if used+st.pending+extra > s.limits.WorkspaceQuota {
		if used, err = s.retainedLocked(req.WorkspaceID, st, true); err != nil || used+st.pending+extra > s.limits.WorkspaceQuota {
			return nil, fmt.Errorf("%w (%d of %d bytes retained for workspace %s)", ErrQuotaFull, used, s.limits.WorkspaceQuota, req.WorkspaceID)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err // cancelled before the retention commit: nothing is retained
	}
	// durable before it becomes readable (only retained originals pay for the sync)
	if err := sp.f.Sync(); err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(sp.f.Name()), key)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	// raw is stored (renamed into place) before the metadata that makes it readable.
	if err := os.Rename(sp.f.Name(), filepath.Join(dir, "raw.bin")); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := writeJSONAtomic(filepath.Join(dir, "meta.json"), obs); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	st.used += retainedCharge(sp.n)
	st.pending = max(0, st.pending-sp.n)
	sp.n = 0 // ownership moved to retained; Discard must not release it again
	d.Handle, d.ResourceURI, d.ExpiresAt, d.CapturedKey, d.PageSize = handle, ResourceURI(handle, req.WorkspaceID), obs.ExpiresAt, obs.CapturedKey, s.limits.PageSize
	return d, nil
}

// Read returns one authorized page of a retained original.
func (s *Store) Read(ctx context.Context, req ReadRequest) (*Page, error) {
	// Revoke/expiry is observed as revoked/expired (410), never as a torn read (tail.go).
	f, obs, err := s.openAuthorized(req)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	length := req.Length
	if length <= 0 || length > s.limits.PageSize {
		length = s.limits.PageSize
	}
	if rem := obs.RawBytes - req.Offset; int64(length) > rem {
		length = int(rem)
	}
	buf := make([]byte, length)
	if m, err := f.ReadAt(buf, req.Offset); m != length || (err != nil && !errors.Is(err, io.EOF)) {
		return nil, ErrCorrupt
	}
	p := &Page{Handle: req.Handle, Tool: obs.Tool, CallID: obs.CallID, ContentType: obs.ContentType,
		Offset: req.Offset, Length: length, TotalBytes: obs.RawBytes, NextOffset: req.Offset + int64(length),
		Encoding: "base64", Data: base64.StdEncoding.EncodeToString(buf), RawSHA256: obs.RawSHA256,
		CapturedKey: obs.CapturedKey, ExpiresAt: obs.ExpiresAt, Quarantine: obs.Quarantine}
	p.EOF = p.NextOffset >= obs.RawBytes
	var cur Identity
	if obs.CapturedKeyOK && req.RepoKey != nil {
		cur = req.RepoKey(ctx)
	}
	p.CurrentKey, p.CurrentKeyCached, p.CurrentKeyAgeMs = cur.Key, cur.Cached, cur.AgeMs
	switch {
	case !obs.CapturedKeyOK || !cur.Complete || cur.Key == "":
		p.Freshness = "unknown"
	case cur.Key == obs.CapturedKey:
		p.Freshness = "current"
	default:
		p.Freshness = "stale"
	}
	p.Stale = p.Freshness != "current"
	return p, nil
}

// openOriginalLocked authorizes a read and opens raw.bin. Caller holds st.mu.
func (s *Store) openOriginalLocked(req ReadRequest, dir string, st *wsState) (*os.File, Observation, error) {
	var obs Observation
	if err := readJSON(filepath.Join(dir, "meta.json"), &obs); err != nil {
		return nil, obs, ErrMissing // unknown, tampered or other-workspace handle
	}
	if obs.WorkspaceID != req.WorkspaceID {
		return nil, obs, ErrMissing
	}
	if obs.Revoked {
		return nil, obs, ErrRevoked
	}
	// principal binding: an original issued to an authenticated principal is readable
	// only by that stable principal ID; one issued without authentication is readable
	// at workspace scope only while authentication is still not enforced.
	if obs.Actor != "" || req.AuthEnforced {
		if req.Actor == "" || req.Actor != obs.Actor {
			return nil, obs, ErrDenied
		}
	}
	exp, _ := time.Parse(time.RFC3339Nano, obs.ExpiresAt)
	if !s.now().Before(exp) {
		if err := os.RemoveAll(dir); err == nil && st.loaded {
			st.used = max(0, st.used-retainedCharge(obs.RawBytes))
		}
		return nil, obs, ErrExpired
	}
	if req.Offset < 0 || req.Offset > obs.RawBytes {
		return nil, obs, fmt.Errorf("%w: offset %d outside 0..%d", ErrInvalidRange, req.Offset, obs.RawBytes)
	}
	if readBeforeRawOpen != nil {
		readBeforeRawOpen()
	}
	f, err := os.Open(filepath.Join(dir, "raw.bin"))
	if err != nil {
		return nil, obs, ErrCorrupt
	}
	// fail closed: the stored original must still be exactly the captured length,
	// and a page must be read in full — never zero-padded under the captured hash.
	if info, err := f.Stat(); err != nil || info.Size() != obs.RawBytes {
		f.Close()
		return nil, obs, ErrCorrupt
	}
	return f, obs, nil
}

// ErrInvalidRange reports an offset outside the original.
var ErrInvalidRange = errors.New("invalid evidence range")

// readBeforeRawOpen is a test seam between Read's metadata check and opening raw.bin.
var readBeforeRawOpen func()

// Revoke deletes one original. Only its issuing principal (or, for unauthenticated
// issuance, a workspace caller) may revoke it; a tombstone keeps the answer "revoked".
func (s *Store) Revoke(req ReadRequest) error {
	key, err := handleKey(req.Handle)
	if err != nil {
		return err
	}
	wsd, err := s.wsDir(req.WorkspaceID)
	if err != nil {
		return err
	}
	dir := filepath.Join(wsd, key)
	st := s.state(req.WorkspaceID)
	st.mu.Lock()
	defer st.mu.Unlock()
	var obs Observation
	if err := readJSON(filepath.Join(dir, "meta.json"), &obs); err != nil || obs.WorkspaceID != req.WorkspaceID {
		return ErrMissing
	}
	if obs.Actor != "" || req.AuthEnforced {
		if req.Actor == "" || req.Actor != obs.Actor {
			return ErrDenied
		}
	}
	if obs.Revoked {
		return nil
	}
	if err := os.Remove(filepath.Join(dir, "raw.bin")); err != nil && !os.IsNotExist(err) {
		return err
	}
	obs.Revoked = true
	if st.loaded {
		st.used = max(0, st.used-retainedCharge(obs.RawBytes))
	}
	return writeJSONAtomic(filepath.Join(dir, "meta.json"), obs)
}

// RevokeWorkspace removes every original of a deleted/revoked workspace.
func (s *Store) RevokeWorkspace(ws string) error {
	dir, err := s.wsDir(ws)
	if err != nil {
		return err
	}
	st := s.state(ws)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loaded, st.used = false, 0
	return os.RemoveAll(dir)
}

// Retained reports the quota charge of a workspace's retained unexpired originals
// (retainedCharge each), sweeping expired ones.
func (s *Store) Retained(ws string) (int64, error) {
	st := s.state(ws)
	st.mu.Lock()
	defer st.mu.Unlock()
	return s.retainedLocked(ws, st, true)
}

// retainedLocked returns retained bytes, rebuilding from disk (sweeping expired
// originals and abandoned spools) on first use after a restart or when rescan is set
// (admission pressure). Caller holds st.mu.
func (s *Store) retainedLocked(ws string, st *wsState, rescan bool) (int64, error) {
	if st.loaded && !rescan {
		return st.used, nil
	}
	dir, err := s.wsDir(ws)
	if err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	now := s.now()
	var used int64
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), ".spool-") {
			// abandoned by a crashed capture: not open here and untouched for an hour
			// (file age uses the real clock; the store clock only governs expiry)
			s.mu.Lock()
			_, open := s.active[p]
			s.mu.Unlock()
			if info, err := e.Info(); err == nil && !open && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(p)
			}
			continue
		}
		var obs Observation
		if !e.IsDir() || readJSON(filepath.Join(p, "meta.json"), &obs) != nil || obs.Revoked {
			continue
		}
		if exp, err := time.Parse(time.RFC3339Nano, obs.ExpiresAt); err != nil || !now.Before(exp) {
			_ = os.RemoveAll(p)
			continue
		}
		used += retainedCharge(obs.RawBytes)
	}
	st.used, st.loaded = used, true
	return used, nil
}

// digestFile hashes the n-byte original in f. When carries is set (a result of one of
// xMustard's own tools) it also returns the first quarantine the original carries
// (injection.CarriedQuarantine), found across chunk boundaries by keeping the tail of
// each chunk in front of the next.
func digestFile(ctx context.Context, f *os.File, n int64, carries bool) (sum, quarantine string, err error) {
	h := sha256.New()
	const chunk, tail = 256 << 10, injection.MaxQuarantineMember
	buf := make([]byte, tail+chunk)
	kept := 0 // bytes of the previous chunk's tail at the front of buf
	for off := int64(0); off < n; {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		m, err := f.ReadAt(buf[kept:kept+int(min(chunk, n-off))], off)
		h.Write(buf[kept : kept+m])
		budget.NoteHashed(int64(m))
		off += int64(m)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		if m == 0 {
			return "", "", ErrCorrupt
		}
		if carries && quarantine == "" {
			quarantine = injection.CarriedQuarantine(buf[:kept+m])
			kept = copy(buf, buf[max(0, kept+m-tail):kept+m])
		}
	}
	return hex.EncodeToString(h.Sum(nil)), quarantine, nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSONAtomic(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
