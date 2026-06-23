package workspaceops

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// ByteBudget is a shared, weighted ceiling on TRANSIENT in-flight bytes across the whole
// process — HTTP request decode, provider/LLM responses, Go↔Rust captured output,
// ast-grep output, and subprocess capture all draw from ONE pool. A count-only semaphore
// can't bound aggregate memory (12 slots × 32 MiB = 384 MiB, far over the <100 MB lean
// target); a single shared byte budget can, because every subsystem's peak is charged to
// the same ceiling. acquire reserves n bytes if they fit; release returns them.
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
	if b.max > 0 && b.used+n > b.max {
		return false
	}
	b.used += n
	if b.used > b.peak {
		b.peak = b.used
	}
	return true
}

// Release returns n previously-acquired bytes to the pool.
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

// defaultTransientBudgetBytes keeps the AGGREGATE transient pool well under the 50–100 MB
// RSS target (the rest of the budget is the base process + caches). Tunable for operators
// who accept a higher ceiling. ONNX + LSP stay off in the lean profile this targets.
const defaultTransientBudgetBytes = 64 << 20 // 64 MiB

// TransientBytes is the process-wide transient-byte pool every large transient allocation
// should charge against (Acquire before, Release after) so concurrent big reads across
// subsystems can't collectively blow the RSS target.
var TransientBytes = NewByteBudget(transientBudgetFromEnv())

func transientBudgetFromEnv() int64 {
	if v := strings.TrimSpace(os.Getenv("XMUSTARD_TRANSIENT_BYTE_BUDGET")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultTransientBudgetBytes
}
