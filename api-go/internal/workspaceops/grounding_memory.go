package workspaceops

import "sort"

// groundingMemory is the memory section of `ground`: promoted memory whose
// referenced files drifted, and how promoted memory is trusted.
type groundingMemory struct {
	StaleMemory int `json:"stale_memory"`
	// StaleMemoryChecked / Total / Complete make the bounded drift check explicit:
	// only the most recent groundStaleWindow memories with path baselines are hashed.
	StaleMemoryChecked  int  `json:"stale_memory_checked"`
	StaleMemoryTotal    int  `json:"stale_memory_total"`
	StaleMemoryComplete bool `json:"stale_memory_complete"`
	// MemoryVerificationModes counts promoted memories by trust basis (peer_verified,
	// self_asserted_open_mode, single_agent), so an agent can tell peer-verified
	// shared memory from self-asserted memory before it relies on recall.
	MemoryVerificationModes map[string]int `json:"memory_verification_modes"`
}

// build runs the stale verified-memory check (drift-on-recall): memory whose
// referenced files changed. Bounded: only the most recent groundStaleWindow
// baselined memories are hashed, and the result says whether that covered every one.
// When the memory store cannot be read, nothing was checked: stale_memory_complete
// is false, memory_verification_modes is null, and the fields are listed unknown.
func (s *groundingMemory) build(dataDir, workspaceID string) []GroundingUnknown {
	var err error
	s.StaleMemory, s.StaleMemoryChecked, s.StaleMemoryTotal, s.StaleMemoryComplete, s.MemoryVerificationModes, err = boundedStaleMemory(dataDir, workspaceID, groundStaleWindow)
	if err != nil {
		reason := "memory store unreadable: " + err.Error()
		return []GroundingUnknown{{Field: "stale_memory", Reason: reason}, {Field: "memory_verification_modes", Reason: reason}}
	}
	return nil
}

// groundStaleWindow bounds how many promoted memories `ground` drift-checks.
const groundStaleWindow = 64

// boundedStaleMemory drift-checks at most window promoted memories that carry path
// baselines, most recently updated first, from content-free metadata. It returns the
// stale count, how many were checked, the promoted total, whether every baselined
// memory was checked, the promoted count per verification mode, and the error that
// prevented the check.
func boundedStaleMemory(dataDir, workspaceID string, window int) (stale, checked, total int, complete bool, modes map[string]int, err error) {
	promoted, ok := loadPromotedMetaCached(dataDir, workspaceID)
	if !ok {
		if promoted, err = loadPromotedMeta(dataDir, workspaceID); err != nil {
			return 0, 0, 0, false, nil, err
		}
	}
	_, threshold := contextDefaults(dataDir)
	modes = labelVerificationModes(promoted, threshold)
	sort.SliceStable(promoted, func(a, b int) bool { return promoted[a].UpdatedAt > promoted[b].UpdatedAt })
	root := contextRoot(dataDir, workspaceID)
	baselined := 0
	for i := range promoted {
		if len(promoted[i].PathHashes) == 0 {
			continue
		}
		baselined++
		if checked >= window {
			continue
		}
		computeStaleness(root, &promoted[i])
		checked++
		if promoted[i].Stale {
			stale++
		}
	}
	return stale, checked, len(promoted), checked == baselined, modes, nil
}
