package budget

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// The shared byte budget bounds AGGREGATE in-flight bytes: concurrent acquirers can't
// collectively exceed the ceiling, an over-budget acquire is rejected (reserving
// nothing), and release returns capacity. This is what a count-only semaphore cannot do.
func TestByteBudgetBoundsAggregate(t *testing.T) {
	b := NewByteBudget(100)

	if !b.Acquire(60) {
		t.Fatalf("first 60 should fit under 100")
	}
	if b.Acquire(50) {
		t.Fatalf("60+50 exceeds 100 — must be rejected")
	}
	if b.InUse() != 60 {
		t.Fatalf("a rejected acquire must reserve nothing, used=%d", b.InUse())
	}
	if !b.Acquire(40) {
		t.Fatalf("60+40 == 100 should just fit")
	}
	if b.InUse() != 100 || b.Peak() != 100 {
		t.Fatalf("used/peak should be 100, got used=%d peak=%d", b.InUse(), b.Peak())
	}
	b.Release(40)
	if b.InUse() != 60 {
		t.Fatalf("release should free capacity, used=%d", b.InUse())
	}
	if !b.Acquire(40) {
		t.Fatalf("after release 40 should fit again")
	}
}

// PAR-RT-04: the default transient pool is 24 MiB (was 64 MiB), deliberately inside
// the 16–24 MiB range, and it is what an unconfigured process gets.
func TestDefaultTransientPoolIs24MiB(t *testing.T) {
	if DefaultTransientBudgetBytes != 24<<20 {
		t.Fatalf("default transient pool = %d, want 24 MiB", DefaultTransientBudgetBytes)
	}
	t.Setenv("XMUSTARD_TRANSIENT_BYTE_BUDGET", "")
	if got := transientBudgetFromEnv(); got != 24<<20 {
		t.Fatalf("unconfigured pool = %d, want 24 MiB", got)
	}
	t.Setenv("XMUSTARD_TRANSIENT_BYTE_BUDGET", "1048576")
	if got := transientBudgetFromEnv(); got != 1<<20 {
		t.Fatalf("operator override ignored: %d", got)
	}
	// Four 16 MiB reservations filled the old pool; the new one admits one at a time.
	b := NewByteBudget(DefaultTransientBudgetBytes)
	if !b.Acquire(16<<20) || b.Acquire(16<<20) {
		t.Fatalf("a 24 MiB pool must admit one 16 MiB reservation and refuse a second (in use %d)", b.InUse())
	}
}

// A request cap above the pool is lowered to the pool, so a body that can never be
// admitted is refused as too large instead of as a retryable overload.
func TestCapToPool(t *testing.T) {
	prev := TransientBytes
	defer func() { TransientBytes = prev }()
	TransientBytes = NewByteBudget(24 << 20)
	if got := CapToPool(32 << 20); got != 24<<20 {
		t.Fatalf("32 MiB cap under a 24 MiB pool = %d", got)
	}
	if got := CapToPool(1 << 20); got != 1<<20 {
		t.Fatalf("a cap below the pool must be kept: %d", got)
	}
	TransientBytes = NewByteBudget(0)
	if got := CapToPool(32 << 20); got != 32<<20 {
		t.Fatalf("an unlimited pool must not lower the cap: %d", got)
	}
}

// A Scope holds reservations until Close (idempotent) and refuses, reserving nothing,
// once the pool is full — there is no unconditional charge path any more. Bytes held by
// other requests make a refusal retryable (ErrOverloaded); bytes this request holds
// itself make it permanent (ErrNeverFits), since they are not released before it ends.
func TestScopeEnforcesAndReleasesOnClose(t *testing.T) {
	b := NewByteBudget(100)
	s := NewScope(b)
	if err := s.Acquire(80); err != nil {
		t.Fatalf("80 should fit: %v", err)
	}
	other := NewScope(b)
	if err := other.Acquire(30); !errors.Is(err, ErrOverloaded) || errors.Is(err, ErrTooLarge) {
		t.Fatalf("30 more while another request holds 80 of 100 must be retryable ErrOverloaded, got %v", err)
	}
	if err := s.Acquire(30); !errors.Is(err, ErrNeverFits) || !errors.Is(err, ErrTooLarge) || errors.Is(err, ErrOverloaded) {
		t.Fatalf("80+30 in one request under a 100-byte pool can never fit: want ErrNeverFits, got %v", err)
	}
	if b.InUse() != 80 || s.Held() != 80 || other.Held() != 0 {
		t.Fatalf("refusal must reserve nothing: pool=%d scope=%d other=%d", b.InUse(), s.Held(), other.Held())
	}
	s.Close()
	s.Close()
	if b.InUse() != 0 {
		t.Fatalf("close must release everything once, in use=%d", b.InUse())
	}
	if err := s.Acquire(1); err == nil {
		t.Fatalf("a closed scope must not reserve")
	}
}

// Scope release runs on panic when deferred, as the HTTP middleware relies on.
func TestScopeReleasedOnPanic(t *testing.T) {
	b := NewByteBudget(100)
	func() {
		defer func() { _ = recover() }()
		s := NewScope(b)
		defer s.Close()
		_ = s.Acquire(50)
		panic("handler panic")
	}()
	if b.InUse() != 0 {
		t.Fatalf("panic leaked %d bytes", b.InUse())
	}
}

// CaptureWriter never buffers past max, never reserves past max, reserves before it
// buffers, and stops (rather than pretending to accept) once over the cap or refused.
func TestCaptureWriterBoundsReservesAndStops(t *testing.T) {
	b := NewByteBudget(1 << 30)
	s := NewScope(b)
	c := NewCaptureWriter(s, 1024)
	chunk := bytes.Repeat([]byte("y"), 300)
	var err error
	for i := 0; i < 10 && err == nil; i++ {
		_, err = c.Write(chunk)
	}
	if err == nil || !c.Over() || c.Len() > 1024 || s.Held() > 1024 {
		t.Fatalf("over=%v len=%d held=%d err=%v", c.Over(), c.Len(), s.Held(), err)
	}
	// output that could never fit the pool is permanent (Over), not a refusal
	small := NewByteBudget(500)
	c2 := NewCaptureWriter(NewScope(small), 1<<20)
	_, err = c2.Write(bytes.Repeat([]byte("z"), 600))
	if err == nil || !c2.Over() || c2.Refused() || c2.Len() != 0 || small.InUse() != 0 {
		t.Fatalf("600 bytes under a 500-byte pool: over=%v refused=%v len=%d inuse=%d", c2.Over(), c2.Refused(), c2.Len(), small.InUse())
	}
	// output that would fit once other requests finish is a retryable refusal
	busy := NewByteBudget(1000)
	holder := NewScope(busy)
	if err := holder.Acquire(600); err != nil {
		t.Fatal(err)
	}
	c3 := NewCaptureWriter(NewScope(busy), 1<<20)
	_, err = c3.Write(bytes.Repeat([]byte("z"), 600))
	if err == nil || !c3.Refused() || c3.Over() || c3.Len() != 0 || busy.InUse() != 600 {
		t.Fatalf("600 bytes while 600 of 1000 are held elsewhere: refused=%v over=%v len=%d inuse=%d", c3.Refused(), c3.Over(), c3.Len(), busy.InUse())
	}
}

// A capture near the pool's size is not failed by chunk rounding: when a whole
// reservation chunk would never fit but the bytes actually written do, they are admitted.
func TestCaptureWriterNearPoolEdgeReservesOnlyWhatFits(t *testing.T) {
	pool := NewByteBudget(admissionChunk + 1000)
	s := NewScope(pool)
	c := NewCaptureWriter(s, 1<<30)
	if _, err := c.Write(bytes.Repeat([]byte("a"), admissionChunk)); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if _, err := c.Write(bytes.Repeat([]byte("b"), 900)); err != nil || c.Over() || c.Refused() {
		t.Fatalf("900 more bytes fit the pool's last 1000: err=%v over=%v refused=%v", err, c.Over(), c.Refused())
	}
	if s.Held() != admissionChunk+900 || c.Len() != admissionChunk+900 {
		t.Fatalf("held %d len %d", s.Held(), c.Len())
	}
	if _, err := c.Write(bytes.Repeat([]byte("c"), 101)); err == nil || !c.Over() {
		t.Fatalf("one byte past the pool must be permanent: err=%v over=%v", err, c.Over())
	}
}

// ReadAllAdmitted answers a body that no pool state could admit with ErrNeverFits (an
// ErrTooLarge), and a body blocked by other requests with ErrOverloaded.
func TestReadAllAdmittedSeparatesNeverFitsFromOverload(t *testing.T) {
	pool := NewByteBudget(1 << 20)
	_, err := ReadAllAdmitted(NewScope(pool), bytes.NewReader(make([]byte, 2<<20)), 4<<20)
	if !errors.Is(err, ErrNeverFits) || !errors.Is(err, ErrTooLarge) || errors.Is(err, ErrOverloaded) {
		t.Fatalf("2 MiB under a 1 MiB pool: want ErrNeverFits, got %v", err)
	}
	holder := NewScope(pool)
	if err := holder.Acquire(900 << 10); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAllAdmitted(NewScope(pool), bytes.NewReader(make([]byte, 512<<10)), 4<<20); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("512 KiB while 900 KiB of 1 MiB are held elsewhere: want ErrOverloaded, got %v", err)
	}
	holder.Close()
	if raw, err := ReadAllAdmitted(NewScope(pool), bytes.NewReader(make([]byte, 512<<10)), 4<<20); err != nil || len(raw) != 512<<10 {
		t.Fatalf("after release it fits: %d %v", len(raw), err)
	}
	if pool.InUse() != 512<<10 {
		t.Fatalf("the admitted read stays reserved until its scope closes: %d", pool.InUse())
	}
}

// Release hands back part of a scope's reservation early (a decoded frame's raw bytes),
// never more than the scope holds, and nothing after Close.
func TestScopeReleaseReturnsHeldBytesEarly(t *testing.T) {
	pool := NewByteBudget(100)
	s := NewScope(pool)
	if err := s.Acquire(60); err != nil {
		t.Fatal(err)
	}
	s.Release(20)
	if s.Held() != 40 || pool.InUse() != 40 {
		t.Fatalf("after releasing 20: held %d pool %d", s.Held(), pool.InUse())
	}
	if err := s.Acquire(60); err != nil {
		t.Fatalf("40 + 60 fits the pool again: %v", err)
	}
	s.Release(1000)
	if s.Held() != 0 || pool.InUse() != 0 {
		t.Fatalf("release is clamped to what the scope holds: held %d pool %d", s.Held(), pool.InUse())
	}
	_ = s.Acquire(10)
	s.Close()
	s.Release(10)
	if pool.InUse() != 0 || s.PoolMax() != 100 {
		t.Fatalf("release after close: pool %d max %d", pool.InUse(), s.PoolMax())
	}
}

// ChildLimit admits at most n concurrent holders and refuses after the bounded wait.
func TestChildLimitBoundsConcurrency(t *testing.T) {
	l := NewChildLimit(2, 20*time.Millisecond)
	r1, err1 := l.Acquire(context.Background())
	r2, err2 := l.Acquire(context.Background())
	if err1 != nil || err2 != nil {
		t.Fatalf("two slots should be free: %v %v", err1, err2)
	}
	if _, err := l.Acquire(context.Background()); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third holder must be refused with ErrOverloaded, got %v", err)
	}
	r1()
	r3, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("released slot must be reusable: %v", err)
	}
	r2()
	r3()
}

// Under heavy concurrency the invariant holds: InUse never exceeds Max, and the number of
// simultaneous holders is bounded by the budget (each holding a fixed weight).
func TestByteBudgetConcurrentNeverExceedsMax(t *testing.T) {
	const max = 1000
	const weight = 100 // at most 10 concurrent holders
	b := NewByteBudget(max)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Acquire(weight) {
				if u := b.InUse(); u > max {
					t.Errorf("budget exceeded: used=%d > max=%d", u, max)
				}
				b.Release(weight)
			}
		}()
	}
	wg.Wait()
	if b.InUse() != 0 {
		t.Fatalf("all acquirers released; used should be 0, got %d", b.InUse())
	}
	if b.Peak() > max {
		t.Fatalf("peak exceeded max: %d > %d", b.Peak(), max)
	}
}

// Root review: used+n can overflow int64, admitting a MaxInt64 reservation once any
// byte is in use (HTTP ContentLength is int64). Capacity must be compared safely.
func TestAcquireRejectsOverflowingReservation(t *testing.T) {
	b := NewByteBudget(100)
	if !b.Acquire(1) {
		t.Fatal("1 byte should fit")
	}
	if b.Acquire(math.MaxInt64) {
		t.Fatalf("MaxInt64 admitted after 1 byte: used=%d", b.InUse())
	}
	if b.InUse() != 1 {
		t.Fatalf("refusal must reserve nothing, used=%d", b.InUse())
	}
	s := NewScope(b)
	if err := s.Acquire(math.MaxInt64); !errors.Is(err, ErrNeverFits) || s.Held() != 0 || b.InUse() != 1 {
		t.Fatalf("scope must refuse MaxInt64 as never fitting, reserving nothing: %v", err)
	}
}
