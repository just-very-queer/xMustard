package rustcore

import (
	"context"
	"fmt"
	"log"

	"xmustard/api-go/internal/budget"
)

// Heavy Rust work (WS-06B, PAR-RT-04). The whole-repository builds run one-shot (their
// transient heap leaves with the process) and inside the governor's single heavy slot:
// a caller waits for a busy slot up to its bound, then gets budget.ErrOverloaded (HTTP
// 503 + Retry-After), and the watchdog refuses the work when the measured tree plus its
// declaration would pass the soft ceiling, after first asking the resident worker for
// its memory. A hook or capture path never waits: it is refused at once.
//
// Each build declares its measured peak, rounded up. Max RSS of one run with
// /usr/bin/time -l on macOS (see the WS-06B record in
// docs/plans/2026-09-25-parity-build-plan.md):
//
//	symbolgraph build:  pi-mono 42.4 MiB cold / 36.7 warm; cline 40.0 / 38.3
//	changetrack index:  pi-mono 25.6 / 22.7; cline 38.5 / 41.8
//
// WS-07's streaming index (`index build`, and `index update`, which turns into a full
// rebuild when enough files changed) declares the design's 25 MiB heavy line: on macOS
// a cold build peaked at 20.8-21.0 MiB ps RSS on pi-mono and 23.0-23.1 on cline, an
// update at 18.3-18.8. `index stats` is a read and never takes the slot.
//
// Captures never take the slot (they stream to the spool in O(window)), and neither
// do the graph queries (impact, trace, clusters, hotspots, blast-radius): they are
// query paths bounded by the child limit.
const (
	heavyBuildBytes int64 = 44 << 20
	heavyIndexBytes int64 = 25 << 20
)

var heavyCoreOps = map[string]map[string]int64{
	"symbolgraph": {"build": heavyBuildBytes},
	"changetrack": {"index": heavyBuildBytes},
	"index":       {"build": heavyIndexBytes, "update": heavyIndexBytes},
}

// heavyCoreOp reports whether sub with args is heavy work, with its heavy-slot owner
// label and declaration.
func heavyCoreOp(sub string, args []string) (owner string, declared int64, ok bool) {
	if len(args) == 0 {
		return "", 0, false
	}
	declared, ok = heavyCoreOps[sub][args[0]]
	if !ok {
		return "", 0, false
	}
	return "rust:" + sub + "/" + args[0], declared, true
}

// acquireHeavyCore takes the heavy slot for heavy work, and returns a nil release for
// anything else. Refusals keep their type: budget.ErrOverloaded (retry later),
// budget.ErrTooLarge (a declaration the soft ceiling can never admit) or the caller's
// context error.
func acquireHeavyCore(ctx context.Context, sub string, args []string) (release func(), err error) {
	owner, declared, ok := heavyCoreOp(sub, args)
	if !ok {
		return nil, nil
	}
	release, err = budget.AcquireHeavy(ctx, owner, declared)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("rust-core %s %s: heavy slot refused: %v", sub, args[0], err)
		}
		return nil, fmt.Errorf("rust-core %s %s: %w", sub, args[0], err)
	}
	return release, nil
}
