package govstore

import (
	"context"
	"fmt"
	"sync/atomic"

	"xmustard/api-go/internal/budget"
)

// The store under the budget governor (WS-06B, PAR-STORE-01 §7.2). An open store is a
// resident component of the process that opened it: its page caches are capped at
// about 2 MiB (see DefaultCacheKiB) and the RSS probe (rss_probe_test.go) holds an open
// store to at most 3 MiB over the process before Open, inside §7.2's steady 3-5 MiB.
// A legacy import is the store's bulk write: the probe holds a 100k-row import to at
// most 14 MiB above the same baseline, so it runs in the governor's heavy slot with that
// declaration. The component is listed while no store is open but reserves nothing.
const (
	storeSteadyBytes int64 = 3 << 20
	storePeakBytes   int64 = 5 << 20
	importHeavyBytes int64 = 14 << 20
)

// openStores counts the stores this process holds open.
var openStores atomic.Int64

func init() {
	budget.RegisterProcessComponent(budget.Component{
		Name:        "govstore",
		Kind:        budget.ComponentResident,
		SteadyBytes: storeSteadyBytes,
		PeakBytes:   storePeakBytes,
		Enabled:     func() bool { return openStores.Load() > 0 },
		// SQLite's page caches and schema live on the C heap, outside the Go heap the
		// daemon line measures, so the store reports its own usage.
		UsedBasis: "sqlite_heap",
		Used:      func() (int64, bool) { return HeapInUse(), true },
	})
}

// acquireImportSlot takes the heavy slot for a legacy import of source. A busy slot
// means a bounded wait and then budget.ErrOverloaded; a tree without room for the import
// is refused with ErrOverloaded too, and the refusal keeps its type through %w.
func acquireImportSlot(ctx context.Context, source string) (func(), error) {
	release, err := budget.AcquireHeavy(ctx, "govstore:import/"+source, importHeavyBytes)
	if err != nil {
		return nil, fmt.Errorf("govstore: import %s: %w", source, err)
	}
	return release, nil
}
