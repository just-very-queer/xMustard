package workspaceops

import (
	"context"
	"encoding/json"

	"xmustard/api-go/internal/rustcore"
)

// Ownership + incorporation lineage delegators, and the session-grounding
// aggregator that answers "what changed / what's broken / what's blocked since
// the baseline" by combining change tracking with run history.
//
// `ground` is assembled from section files so later work owns disjoint files:
// grounding_index.go (index drift and working changes), grounding_runs.go (run
// history), grounding_memory.go (stale and trust-labelled memory) and
// grounding_session.go (session summary). Each section declares its own fields in
// an embedded struct; this file only orders the sections and derives the blocked flags.

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

// SessionGrounding is the `ground` result. The embedded sections are flattened into
// one JSON object in this field order.
type SessionGrounding struct {
	WorkspaceID string `json:"workspace_id"`
	groundingIndex
	groundingRuns
	// The blocked flags are null when the count they derive from is unknown.
	BlockedByDirtyState          *bool `json:"blocked_by_dirty_state"`
	BlockedByFailingVerification *bool `json:"blocked_by_failing_verification"`
	groundingMemory
	groundingSession
	// Unknown lists every field that could not be determined (PAR-RT-11): a
	// decode, read or listing failure is reported here, never as 0.
	Unknown []GroundingUnknown `json:"unknown,omitempty"`
}

// BuildSessionGrounding answers "what changed / what's broken / what's blocked"
// since the indexed baseline — the session-grounding layer for a reconnecting agent.
func BuildSessionGrounding(dataDir, workspaceID string) (*SessionGrounding, error) {
	return BuildSessionGroundingCtx(context.Background(), dataDir, workspaceID)
}

// BuildSessionGroundingCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func BuildSessionGroundingCtx(ctx context.Context, dataDir, workspaceID string) (*SessionGrounding, error) {
	g := &SessionGrounding{WorkspaceID: workspaceID}
	unknown, err := g.groundingIndex.build(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	g.Unknown = append(g.Unknown, unknown...)
	if g.ChangedFiles != nil {
		dirty := *g.ChangedFiles > 0
		g.BlockedByDirtyState = &dirty
	}
	unknown, failuresKnown := g.groundingRuns.build(dataDir, workspaceID)
	g.Unknown = append(g.Unknown, unknown...)
	// a listed failure blocks even when other records are unreadable
	if failing := len(g.RecentFailedRuns) > 0; failing || failuresKnown {
		g.BlockedByFailingVerification = &failing
	}
	g.stampGenerated()
	g.Unknown = append(g.Unknown, g.groundingMemory.build(dataDir, workspaceID)...)
	g.summarize()
	return g, nil
}
