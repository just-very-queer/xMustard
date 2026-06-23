package workspaceops

import (
	"sync"
	"testing"
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
