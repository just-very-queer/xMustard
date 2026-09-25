package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"xmustard/api-go/internal/rustcore"
)

// Ownership + incorporation lineage delegators, and the session-grounding
// aggregator that answers "what changed / what's broken / what's blocked since
// the baseline" by combining change tracking with run history.

func WorkspaceSubsystems(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunOwnership(context.Background(), "subsystems", root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func FileOwners(dataDir, workspaceID, path string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunOwnership(context.Background(), "owners", root, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func RecordIncorporation(dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(context.Background(), "incorporate", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func FileLineage(dataDir, workspaceID, path string) (json.RawMessage, error) {
	_, absData, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(context.Background(), "lineage", absData, workspaceID, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

type SessionGrounding struct {
	WorkspaceID                  string          `json:"workspace_id"`
	Drift                        json.RawMessage `json:"drift"`
	ChangedFiles                 int             `json:"changed_files"`
	DirtySymbols                 int             `json:"dirty_symbols"`
	ContractBreaks               int             `json:"contract_breaks"`
	BrokenContracts              []string        `json:"broken_contracts,omitempty"`
	RecentFailedRuns             []string        `json:"recent_failed_runs"`
	BlockedByDirtyState          bool            `json:"blocked_by_dirty_state"`
	BlockedByFailingVerification bool            `json:"blocked_by_failing_verification"`
	StaleMemory                  int             `json:"stale_memory"`
	// StaleMemoryChecked / Total / Complete make the bounded drift check explicit:
	// only the most recent groundStaleWindow memories with path baselines are hashed.
	StaleMemoryChecked  int    `json:"stale_memory_checked"`
	StaleMemoryTotal    int    `json:"stale_memory_total"`
	StaleMemoryComplete bool   `json:"stale_memory_complete"`
	Summary             string `json:"summary"`
	GeneratedAt         string `json:"generated_at"`
}

// BuildSessionGrounding answers "what changed / what's broken / what's blocked"
// since the indexed baseline — the session-grounding layer for a reconnecting agent.
func BuildSessionGrounding(dataDir, workspaceID string) (*SessionGrounding, error) {
	return BuildSessionGroundingCtx(context.Background(), dataDir, workspaceID)
}

// BuildSessionGroundingCtx is the request-scoped variant: cancelling ctx kills its Rust/tool children.
func BuildSessionGroundingCtx(ctx context.Context, dataDir, workspaceID string) (*SessionGrounding, error) {
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	changesRaw, err := WorkspaceWorkingChangesCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	var changes struct {
		ChangedFiles   []json.RawMessage `json:"changed_files"`
		ContractBreaks int               `json:"contract_breaks"`
		DirtySymbols   []struct {
			Path            string `json:"path"`
			Symbol          string `json:"symbol"`
			ContractBreak   bool   `json:"contract_break"`
			SignatureChange string `json:"signature_change"`
		} `json:"dirty_symbols"`
	}
	_ = json.Unmarshal(changesRaw, &changes)
	broken := []string{}
	for _, s := range changes.DirtySymbols {
		if s.ContractBreak {
			broken = append(broken, fmt.Sprintf("%s in %s (%s)", s.Symbol, s.Path, s.SignatureChange))
		}
	}

	failed := []string{}
	if runs, err := ListRuns(dataDir, workspaceID); err == nil {
		for _, run := range runs {
			status := strings.ToLower(strings.TrimSpace(run.Status))
			bad := status == "failed" || status == "error" || status == "cancelled"
			if run.ExitCode != nil && *run.ExitCode != 0 {
				bad = true
			}
			if bad {
				failed = append(failed, run.RunID)
			}
		}
	}

	g := &SessionGrounding{
		WorkspaceID:                  workspaceID,
		Drift:                        drift,
		ChangedFiles:                 len(changes.ChangedFiles),
		DirtySymbols:                 len(changes.DirtySymbols),
		ContractBreaks:               changes.ContractBreaks,
		BrokenContracts:              broken,
		RecentFailedRuns:             failed,
		BlockedByDirtyState:          len(changes.ChangedFiles) > 0,
		BlockedByFailingVerification: len(failed) > 0,
		GeneratedAt:                  time.Now().UTC().Format(time.RFC3339),
	}
	// stale verified-memory (drift-on-recall) — memory whose referenced files changed.
	// Bounded: only the most recent groundStaleWindow baselined memories are hashed,
	// and the result says whether that covered every one.
	g.StaleMemory, g.StaleMemoryChecked, g.StaleMemoryTotal, g.StaleMemoryComplete = boundedStaleMemory(dataDir, workspaceID, groundStaleWindow)
	g.Summary = fmt.Sprintf("%d changed file(s), %d dirty symbol(s), %d contract break(s), %d failed run(s), %d stale memory.",
		g.ChangedFiles, g.DirtySymbols, g.ContractBreaks, len(failed), g.StaleMemory)
	return g, nil
}

// groundStaleWindow bounds how many promoted memories `ground` drift-checks.
const groundStaleWindow = 64

// boundedStaleMemory drift-checks at most window promoted memories that carry path
// baselines, most recently updated first, from content-free metadata. It returns the
// stale count, how many were checked, the promoted total and whether every baselined
// memory was checked.
func boundedStaleMemory(dataDir, workspaceID string, window int) (stale, checked, total int, complete bool) {
	promoted, ok := loadPromotedMetaCached(dataDir, workspaceID)
	if !ok {
		var err error
		if promoted, err = loadPromotedMeta(dataDir, workspaceID); err != nil {
			return 0, 0, 0, false
		}
	}
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
	return stale, checked, len(promoted), checked == baselined
}
