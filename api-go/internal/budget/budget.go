// Package budget provides process-wide admission for transient in-flight bytes and for
// concurrently running helper children, shared by every subsystem that does large
// transient reads (HTTP decode, provider responses, Go↔Rust capture, ast-grep,
// subprocess capture). It is a leaf package (no internal imports) so both workspaceops
// and rustcore can draw on the same pools.
//
// Admission is enforced: a reservation that does not fit is refused with ErrOverloaded
// and nothing is allocated for it. Byte admission bounds what xMustard deliberately
// buffers; it is not a process RSS ceiling (allocator slack, decode amplification,
// runtime overhead and child processes are outside it). RSS is measured separately.
package budget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrOverloaded is returned when a reservation or child slot cannot be admitted.
// HTTP maps it to 503 + Retry-After; MCP maps it to JSON-RPC -32000.
var ErrOverloaded = errors.New("xmustard overloaded: transient budget exhausted; retry shortly")

// ByteBudget is a shared, weighted ceiling on TRANSIENT in-flight bytes across the whole
// process. Acquire reserves n bytes if they fit; Release returns them.
type ByteBudget struct {
	mu   sync.Mutex
	max  int64
	used int64
	peak int64
}

func NewByteBudget(max int64) *ByteBudget { return &ByteBudget{max: max} }

// Acquire reserves n bytes, returning false (reserving nothing) if they would exceed the
// ceiling — the caller then rejects/sheds rather than allocating. n<=0 is a no-op success.
func (b *ByteBudget) Acquire(n int64) bool {
	if n <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Compare against remaining capacity (never used+n, which can overflow int64).
	if b.max > 0 && (n > b.max || b.used > b.max-n) {
		return false
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return true
}

// Release returns n previously acquired bytes to the pool.
func (b *ByteBudget) Release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
}

// InUse / Peak / Max expose the budget for metrics + the RSS load test's assertions.
func (b *ByteBudget) InUse() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.used }
func (b *ByteBudget) Peak() int64  { b.mu.Lock(); defer b.mu.Unlock(); return b.peak }
func (b *ByteBudget) Max() int64   { return b.max }

// Scope is a reservation ledger: bytes acquired through it stay reserved until Close,
// so a request can hold its capture, decode and response bytes until the response is
// written. Close is idempotent and safe to defer (it runs on panic too).
type Scope struct {
	pool   *ByteBudget
	mu     sync.Mutex
	held   int64
	closed bool
}

// NewScope opens a ledger against pool (TransientBytes when nil).
func NewScope(pool *ByteBudget) *Scope {
	if pool == nil {
		pool = TransientBytes
	}
	return &Scope{pool: pool}
}

// Acquire reserves n more bytes in this scope. It returns ErrNeverFits when this scope's
// bytes plus n exceed the whole pool (the request can never be admitted, so retrying is
// pointless) and ErrOverloaded when they do not fit now because of other requests.
func (s *Scope) Acquire(n int64) error {
	if n <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w (scope closed)", ErrOverloaded)
	}
	if max := s.pool.Max(); max > 0 && (n > max || s.held > max-n) {
		return fmt.Errorf("%w (%d bytes held by this request + %d more > %d-byte pool)", ErrNeverFits, s.held, n, max)
	}
	if !s.pool.Acquire(n) {
		return ErrOverloaded
	}
	s.held += n
	return nil
}

// Release returns up to n of this scope's bytes before Close, for a copy the request no
// longer references (a raw frame once it has been decoded, for example).
func (s *Scope) Release(n int64) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	n = min(n, s.held)
	s.held -= n
	s.pool.Release(n)
}

// Held reports the bytes currently reserved by this scope.
func (s *Scope) Held() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.held }

// PoolMax is the size of the pool this scope reserves from (0 means unlimited). Per-item
// caps derived from it keep work that can never be admitted on the permanent path.
func (s *Scope) PoolMax() int64 { return s.pool.Max() }

// Close releases every byte the scope holds.
func (s *Scope) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pool.Release(s.held)
	s.held = 0
}

type scopeKey struct{}

// WithScope attaches a request-scoped ledger to ctx.
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFor returns the ledger attached to ctx, or a new one the caller must Close. The
// boolean reports whether the caller owns (must close) the returned scope.
func ScopeFor(ctx context.Context) (*Scope, bool) {
	if s, ok := ctx.Value(scopeKey{}).(*Scope); ok && s != nil {
		return s, false
	}
	return NewScope(nil), true
}

// admissionChunk is the reservation granularity for streamed captures: bytes are
// reserved in chunks BEFORE they are buffered, never after.
const admissionChunk = 256 << 10

// CaptureWriter buffers a child's output up to max bytes, reserving pool bytes in chunks
// before buffering them. When the cap or the pool refuses, it stops buffering and
// returns an error so the producer can be stopped; Over/Refused report why.
type CaptureWriter struct {
	scope    *Scope
	max      int
	reserved int64
	buf      []byte
	over     bool
	refused  bool
	limitErr error // set when the pool could never admit the output (Over is true)
	// OnStop, when set, runs once at the first overflow or refusal so the owner can
	// kill the producer. Returning an error only stops pipe copying; a child that
	// stops writing (e.g. floods, then sleeps) would otherwise keep running.
	OnStop  func()
	stopped bool
}

func (c *CaptureWriter) stop() {
	if !c.stopped {
		c.stopped = true
		if c.OnStop != nil {
			c.OnStop()
		}
	}
}

func NewCaptureWriter(scope *Scope, max int) *CaptureWriter {
	return &CaptureWriter{scope: scope, max: max}
}

// errCaptureStopped is returned from Write once capture has stopped.
var errCaptureStopped = errors.New("capture stopped")

func (c *CaptureWriter) Write(p []byte) (int, error) {
	if c.over || c.refused {
		return 0, errCaptureStopped
	}
	if len(c.buf)+len(p) > c.max {
		c.over = true
		c.stop()
		return 0, errCaptureStopped
	}
	need := int64(len(c.buf)+len(p)) - c.reserved
	for need > 0 {
		chunk := int64(admissionChunk)
		if room := int64(c.max) - c.reserved; chunk > room {
			chunk = room
		}
		if chunk < need {
			chunk = need
		}
		err := c.scope.Acquire(chunk)
		if err != nil && chunk > need && errors.Is(err, ErrNeverFits) {
			chunk = need // a whole chunk would never fit, but the bytes needed might
			err = c.scope.Acquire(chunk)
		}
		if err != nil {
			if errors.Is(err, ErrNeverFits) {
				c.over, c.limitErr = true, err // permanent: as if past the cap
			} else {
				c.refused = true
			}
			c.stop()
			return 0, errCaptureStopped
		}
		c.reserved += chunk
		need -= chunk
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *CaptureWriter) Bytes() []byte  { return c.buf }
func (c *CaptureWriter) Len() int       { return len(c.buf) }
func (c *CaptureWriter) String() string { return string(c.buf) }

// Over reports output past max, or past what the pool could ever admit for this
// request: a permanent failure. Refused reports a transient pool refusal.
func (c *CaptureWriter) Over() bool    { return c.over }
func (c *CaptureWriter) Refused() bool { return c.refused }

// ReadAllAdmitted reads r up to max bytes, reserving pool bytes before buffering. It
// returns ErrOverloaded when the pool refuses for now, and an ErrTooLarge error past max
// or past what the pool could ever admit (ErrNeverFits).
func ReadAllAdmitted(scope *Scope, r io.Reader, max int) ([]byte, error) {
	w := NewCaptureWriter(scope, max)
	_, err := io.Copy(w, r)
	switch {
	case w.Refused():
		return nil, ErrOverloaded
	case w.Over() && w.limitErr != nil:
		return nil, w.limitErr
	case w.Over():
		return nil, ErrTooLarge
	case err != nil:
		return nil, err
	}
	return w.Bytes(), nil
}

// ErrTooLarge reports a read that exceeded its per-item cap.
var ErrTooLarge = errors.New("payload exceeds size limit")

// ErrNeverFits reports a reservation that an empty pool could not admit: the request
// alone would hold more than the whole transient budget, so a retry cannot succeed. It
// wraps ErrTooLarge, so callers answer it permanently (HTTP 413, a JSON-RPC
// invalid-request or tool error), never with 503 + Retry-After or -32000.
var ErrNeverFits = fmt.Errorf("%w: more than the whole transient budget, so a retry cannot succeed", ErrTooLarge)

// DefaultTransientBudgetBytes is the AGGREGATE transient pool (PAR-RT-04: 16–24 MiB,
// lowered from 64 MiB, where four 16 MiB captures held the whole pool). It sits inside
// the Go daemon's share of the 95.4 MiB process-tree gate; heavy work has its own slot
// (governor.go). Operators may lower it; raising it needs a new resource measurement.
const DefaultTransientBudgetBytes = 24 << 20 // 24 MiB

// TransientBytes is the process-wide transient-byte pool every large transient allocation
// reserves against.
var TransientBytes = NewByteBudget(transientBudgetFromEnv())

func transientBudgetFromEnv() int64 {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_TRANSIENT_BYTE_BUDGET")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultTransientBudgetBytes
}

// CapToPool lowers a per-request byte cap to the transient pool's size. A request larger
// than the whole pool can never be admitted, so it must be refused as too large (413),
// not as a retryable overload.
func CapToPool(n int64) int64 {
	if m := TransientBytes.Max(); m > 0 && n > m {
		return m
	}
	return n
}

// ChildLimit bounds how many helper children (Rust core, ast-grep, verification
// captures) run at once, separately from byte admission.
type ChildLimit struct {
	slots chan struct{}
	wait  time.Duration
	peak  atomic.Int64
}

func NewChildLimit(n int, wait time.Duration) *ChildLimit {
	if n <= 0 {
		n = 1
	}
	return &ChildLimit{slots: make(chan struct{}, n), wait: wait}
}

// Acquire takes a slot, waiting at most the configured bound (or until ctx ends). It
// returns a release func, or ErrOverloaded / ctx's error.
func (l *ChildLimit) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
		l.notePeak()
		return l.release, nil
	default:
	}
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	select {
	case l.slots <- struct{}{}:
		l.notePeak()
		return l.release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("%w (helper child limit %d reached)", ErrOverloaded, cap(l.slots))
	}
}

func (l *ChildLimit) release() { <-l.slots }

func (l *ChildLimit) notePeak() {
	n := int64(len(l.slots))
	for {
		p := l.peak.Load()
		if n <= p || l.peak.CompareAndSwap(p, n) {
			return
		}
	}
}

// InUse / Peak report current and highest concurrent children (metrics, bench).
func (l *ChildLimit) InUse() int { return len(l.slots) }
func (l *ChildLimit) Peak() int  { return int(l.peak.Load()) }

// Cap reports the configured maximum number of concurrent children.
func (l *ChildLimit) Cap() int { return cap(l.slots) }

// Children is the process-wide helper-child limit (XMUSTARD_MAX_CORE_CHILDREN, default
// 4; queued work waits up to XMUSTARD_CORE_CHILD_WAIT_MS, default 10 s).
var Children = NewChildLimit(envInt("XMUSTARD_MAX_CORE_CHILDREN", 4), time.Duration(envInt("XMUSTARD_CORE_CHILD_WAIT_MS", 10000))*time.Millisecond)

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
