package workspaceops

import (
	"encoding/json"
	"fmt"
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
	out, err := rustcore.RunOwnership("subsystems", root, workspaceID)
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
	out, err := rustcore.RunOwnership("owners", root, path)
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
	out, err := rustcore.RunChangetrack("incorporate", absData, root, workspaceID)
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
	out, err := rustcore.RunChangetrack("lineage", absData, workspaceID, path)
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
	Summary                      string          `json:"summary"`
	GeneratedAt                  string          `json:"generated_at"`
}

// BuildSessionGrounding answers "what changed / what's broken / what's blocked"
// since the indexed baseline — the session-grounding layer for a reconnecting agent.
func BuildSessionGrounding(dataDir, workspaceID string) (*SessionGrounding, error) {
	drift, err := WorkspaceDrift(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	changesRaw, err := WorkspaceWorkingChanges(dataDir, workspaceID)
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
	if active, err := GetActiveContext(dataDir, workspaceID); err == nil {
		if n, ok := active["stale_count"].(int); ok {
			g.StaleMemory = n
		}
	}
	g.Summary = fmt.Sprintf("%d changed file(s), %d dirty symbol(s), %d contract break(s), %d failed run(s), %d stale memory.",
		g.ChangedFiles, g.DirtySymbols, g.ContractBreaks, len(failed), g.StaleMemory)
	return g, nil
}
