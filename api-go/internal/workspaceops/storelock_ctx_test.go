package workspaceops

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A POST /index whose caller goes away must stop waiting on the baseline lock instead
// of parking for the length of the build that holds it. The wait has two locks to give
// up — the in-process mutex and the cross-process flock — and a fix that gives up only
// the first passes the in-process case, because the mutex is uncontended when the
// holder is another process, so both holders are driven here.
func TestRebaselineIndexGivesUpTheBaselineLockWhenItsCallerGoesAway(t *testing.T) {
	for name, hold := range map[string]func(key string) (func(), error){
		"in process":    lockStore,
		"other process": func(key string) (func(), error) { return lockFile(nil, key+".lock", true) },
	} {
		t.Run(name, func(t *testing.T) {
			state := baselineCore(t, "0")
			dataDir, ws, _ := seedBaselineWorkspace(t)
			unlock, err := hold(baselinePath(dataDir, ws))
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()

			ctx, cancel := context.WithCancel(context.Background())
			type answer struct {
				err error
			}
			done := make(chan answer, 1)
			go func() { _, err := RebaselineIndex(ctx, dataDir, ws, "ops-admin"); done <- answer{err} }()

			// The waiter is inside the lock wait, not yet past it: give it long enough
			// that an answer before the holder is released would mean it did not wait.
			time.Sleep(200 * time.Millisecond)
			cancel()

			select {
			case got := <-done:
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("a cancelled rebaseline must answer context.Canceled, got %v", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("a cancelled rebaseline still waits on the baseline lock 3s after its caller went away")
			}

			if got := indexCalls(t, state); len(got) != 0 {
				t.Fatalf("a cancelled rebaseline built: %v", got)
			}
			if evs := baselineHistory(t, dataDir, ws); len(evs) != 0 {
				t.Fatalf("a cancelled rebaseline recorded history: %d events", len(evs))
			}
		})
	}
}

// A cancelled call must give the lock back rather than take it and return early: the
// store stays usable for the next caller, which is what keeps the cancellation from
// becoming an outage.
func TestACancelledRebaselineLeavesTheBaselineLockFreeForTheNextCaller(t *testing.T) {
	baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	key := baselinePath(dataDir, ws)

	held, err := lockStore(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already gone before the call
	if _, err := RebaselineIndex(ctx, dataDir, ws, "ops-admin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a call with an already-cancelled context must answer context.Canceled, got %v", err)
	}
	held() // the test's own holder, still proving the waiter never took it

	unlock, err := lockStore(key)
	if err != nil {
		t.Fatalf("the lock is still held after a cancelled call: %v", err)
	}
	unlock()
}

// The uncontended path is what every existing caller depends on, and a fix that broke
// it would refuse a build nobody cancelled.
func TestRebaselineIndexOnAFreeLockStillBuilds(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)

	b, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin")
	if err != nil {
		t.Fatal(err)
	}
	if b.Reason != BaselineAdmin || b.Auto {
		t.Fatalf("the answer is not an admin rebaseline: %+v", b)
	}
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=admin" {
		t.Fatalf("builds = %v, want [--reason=admin]", got)
	}
	evs := baselineHistory(t, dataDir, ws)
	if len(evs) != 1 || evs[0].Principal != "ops-admin" {
		t.Fatalf("history = %+v", evs)
	}
}

// A context that had already ended when the call started is refused as a cancellation,
// and where it is refused is the point: the lock is free, so it is taken and released,
// and the refusal comes from the build, which will not spawn a core on a dead context.
// A test that only checked the error would pass a fix that refused at the lock, so this
// one also asserts nothing was built, nothing was recorded, and the key is still free.
func TestRebaselineIndexWithAnAlreadyEndedContextIsRefusedAndLeavesNothing(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := RebaselineIndex(ctx, dataDir, ws, "ops-admin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("an already-ended context must be refused as a cancellation, got %v", err)
	}
	if evs := baselineHistory(t, dataDir, ws); len(evs) != 0 {
		t.Fatalf("a refused call recorded history: %+v", evs)
	}
	if got := indexCalls(t, state); len(got) != 0 {
		t.Fatalf("a refused call built: %v", got)
	}
	unlock, err := tryLockStore(baselinePath(dataDir, ws))
	if err != nil {
		t.Fatalf("a refused call left the lock claimed: %v", err)
	}
	unlock()
}

// Giving up is per caller, not per workspace: one cancelled waiter must not deny the
// lock to a live one behind it, or a cancellation would poison the store for everyone
// queued behind it. The second half is the same property from the other side — the
// cancelled waiter left no claim behind, so the key is free again the moment the live
// waiter has finished.
func TestACancelledWaiterDoesNotDenyTheLockToTheNextOne(t *testing.T) {
	state := baselineCore(t, "0")
	dataDir, ws, _ := seedBaselineWorkspace(t)
	key := baselinePath(dataDir, ws)

	held, err := lockStore(key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() { _, err := RebaselineIndex(ctx, dataDir, ws, "ops-cancelled"); waiterErr <- err }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-waiterErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the first waiter must answer context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the cancelled waiter never answered")
	}

	built := make(chan error, 1)
	go func() { _, err := RebaselineIndex(context.Background(), dataDir, ws, "ops-admin"); built <- err }()
	held() // released only once both waiters are queued behind it

	select {
	case err := <-built:
		if err != nil {
			t.Fatalf("the live waiter behind a cancelled one failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the live waiter behind a cancelled one never got the lock")
	}
	if got := indexCalls(t, state); len(got) != 1 || got[0] != "--reason=admin" {
		t.Fatalf("builds = %v, want exactly one admin build", got)
	}
	// A claim left behind by the cancelled waiter, or a second one taken by the build
	// and never released, would still be sitting on the key: the store stays usable.
	unlock, err := tryLockStore(key)
	if err != nil {
		t.Fatalf("the key is not free after the build and the cancellation: %v", err)
	}
	unlock()
}

// A free gate is taken even when the caller's context has already ended, and that is
// the whole point of the two-part acquire: a select over a ready send and a ready
// ctx.Done() picks at random, so the uncontended path would otherwise be a coin flip
// (measured 509 taken / 491 refused in 1000 tries). The test asks the gate directly
// because a RebaselineIndex call cannot separate "refused at the lock" from "refused
// at the build" — both answer context.Canceled — and only the gate's own answer tells
// the two apart.
func TestAFreeGateIsTakenEvenWhenTheContextHasAlreadyEnded(t *testing.T) {
	const tries = 200
	for i := range tries {
		dir := t.TempDir()
		key := filepath.Join(dir, "store.json")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		unlock, err := lockStoreCtx(ctx, key)
		if err != nil {
			t.Fatalf("try %d: a free gate refused a caller whose context had ended: %v", i, err)
		}
		// Taken and released, so the key is free for the next caller and nothing is
		// left claimed by a caller that never did any work.
		unlock()
		next, err := tryLockStore(key)
		if err != nil {
			t.Fatalf("try %d: the gate was left claimed: %v", i, err)
		}
		next()
	}
}

// The wait still gives up when the gate is held, which is the other half of the pair
// above: preferring a free slot must not turn into preferring the slot over the context.
func TestAHeldGateIsGivenUpWhenTheContextEnds(t *testing.T) {
	g := make(storeGate, 1)
	g <- struct{}{} // held
	defer func() { <-g }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.acquire(ctx) {
		t.Fatal("a held gate was taken by a caller whose context had ended")
	}
}
