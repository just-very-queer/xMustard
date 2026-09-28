package workspaceops

import (
	"context"
	"sort"

	"xmustard/api-go/internal/govstore"
)

// groundingMemory is the memory section of `ground`: promoted memory whose
// referenced files drifted, how promoted memory is trusted, and the verification
// queue waiting on the caller.
type groundingMemory struct {
	// StaleMemory is null when the memory store could not be read (see unknown).
	StaleMemory *int `json:"stale_memory"`
	// StaleMemoryChecked / Total / Complete make the bounded drift check explicit:
	// only the most recent groundStaleWindow memories with path baselines are hashed.
	// Total is null with StaleMemory; Checked is then 0 (nothing was checked).
	StaleMemoryChecked  int  `json:"stale_memory_checked"`
	StaleMemoryTotal    *int `json:"stale_memory_total"`
	StaleMemoryComplete bool `json:"stale_memory_complete"`
	// MemoryVerificationModes counts promoted memories by trust basis (peer_verified,
	// self_asserted_open_mode, single_agent), so an agent can tell peer-verified
	// shared memory from self-asserted memory before it relies on recall.
	MemoryVerificationModes map[string]int `json:"memory_verification_modes"`
	// PendingForYou counts the pending entries awaiting the caller's verdict: what
	// recall(status=awaiting_me) lists (PAR-GOV-02).
	PendingForYou *int `json:"pending_for_you"`
	// MemoryPressure sizes the store (PAR-RCL-06).
	MemoryPressure *MemoryPressure `json:"memory_pressure"`
}

// MemoryPressure is how full shared memory is: the percentage of served memory in the
// core tier (always delivered), and how many proposals wait for verification.
type MemoryPressure struct {
	CorePct      int `json:"core_pct"`
	PendingCount int `json:"pending_count"`
}

// build runs the stale verified-memory check (drift-on-recall): memory whose
// referenced files changed. Bounded: only the most recent groundStaleWindow
// baselined memories are hashed, and the result says whether that covered every one.
// When the memory store cannot be read, nothing was checked: stale_memory,
// stale_memory_total, memory_verification_modes, pending_for_you and memory_pressure
// are null, stale_memory_complete is false, and the fields are listed unknown.
func (s *groundingMemory) build(dataDir, workspaceID, caller string) []GroundingUnknown {
	scan, err := scanGroundMemory(dataDir, workspaceID, caller, groundStaleWindow)
	s.StaleMemoryChecked, s.StaleMemoryComplete, s.MemoryVerificationModes = scan.checked, scan.complete, scan.modes
	if err != nil {
		reason := "memory store unreadable: " + err.Error()
		var out []GroundingUnknown
		for _, f := range []string{"stale_memory", "stale_memory_total", "memory_verification_modes", "pending_for_you", "memory_pressure"} {
			out = append(out, GroundingUnknown{Field: f, Reason: reason})
		}
		return out
	}
	s.StaleMemory, s.StaleMemoryTotal, s.PendingForYou = &scan.stale, &scan.total, &scan.forYou
	s.MemoryPressure = &scan.pressure
	return nil
}

// groundStaleWindow bounds how many promoted memories `ground` drift-checks.
const groundStaleWindow = 64

// groundMemoryScan is what ground reads from the memory store.
type groundMemoryScan struct {
	stale, checked, total int
	complete              bool
	modes                 map[string]int
	forYou                int
	pressure              MemoryPressure
}

// scanGroundMemory reads served and pending memory from content-free metadata in one
// ranking view. It drift-checks at most window served memories that carry path
// baselines, most recently updated first, counts the served memory per verification
// mode and in the core tier, and counts the pending entries awaiting the caller.
func scanGroundMemory(dataDir, workspaceID, caller string, window int) (groundMemoryScan, error) {
	ctx := context.Background()
	var view []govstore.RankEntry
	var checkedEntries []ContextEntry
	baselined := 0
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		var err error
		view, err = r.Ranking(ctx, govstore.RankQuery{WorkspaceID: workspaceID, Caller: caller,
			States: []string{govstore.RankServed, govstore.RankPending}})
		if err != nil {
			return err
		}
		sort.SliceStable(view, func(a, b int) bool { return view[a].UpdatedAt > view[b].UpdatedAt })
		var ids []string
		for _, e := range view {
			if e.State != govstore.RankServed || !e.Baselined {
				continue
			}
			if baselined++; len(ids) < window {
				ids = append(ids, e.ID)
			}
		}
		checkedEntries, err = projectByID(ctx, r, ids)
		return err
	})
	if err != nil {
		return groundMemoryScan{}, err
	}
	scan := groundMemoryScan{modes: newModeCounts()}
	awaits := awaitingCaller(dataDir, caller)
	core := 0
	for _, e := range view {
		if e.State == govstore.RankPending {
			scan.pressure.PendingCount++
			if awaits(e) {
				scan.forYou++
			}
			continue
		}
		scan.total++
		if e.VerificationMode != "" {
			scan.modes[e.VerificationMode]++
		}
		if e.Tier == govstore.TierCore {
			core++
		}
	}
	if scan.total > 0 {
		scan.pressure.CorePct = core * 100 / scan.total
	}
	root := contextRoot(dataDir, workspaceID)
	for i := range checkedEntries {
		computeStaleness(root, &checkedEntries[i])
		if checkedEntries[i].Stale {
			scan.stale++
		}
	}
	scan.checked, scan.complete = len(checkedEntries), len(checkedEntries) == baselined
	return scan, nil
}
