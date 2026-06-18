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
	RecentFailedRuns             []string        `json:"recent_failed_runs"`
	BlockedByDirtyState          bool            `json:"blocked_by_dirty_state"`
	BlockedByFailingVerification bool            `json:"blocked_by_failing_verification"`
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
		ChangedFiles []json.RawMessage `json:"changed_files"`
		DirtySymbols []json.RawMessage `json:"dirty_symbols"`
	}
	_ = json.Unmarshal(changesRaw, &changes)

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
		RecentFailedRuns:             failed,
		BlockedByDirtyState:          len(changes.ChangedFiles) > 0,
		BlockedByFailingVerification: len(failed) > 0,
		GeneratedAt:                  time.Now().UTC().Format(time.RFC3339),
	}
	g.Summary = fmt.Sprintf("%d changed file(s), %d dirty symbol(s), %d failed run(s).",
		g.ChangedFiles, g.DirtySymbols, len(failed))
	return g, nil
}
