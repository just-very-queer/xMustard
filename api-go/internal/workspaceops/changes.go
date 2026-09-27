package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/rustcore"
)

// Change-state delivery: resolves a workspace's repo root + absolute data dir and
// delegates gitnexus-style change tracking to the Rust core. Feeds the cockpit
// UI's "change state" pane and the MCP `changed_since` tool.

// resolveChangeRoot returns the workspace's repository root and the absolute data
// dir from the workspace registry (no snapshot parse).
func resolveChangeRoot(dataDir, workspaceID string) (root string, absData string, err error) {
	ws, err := resolveWorkspace(dataDir, workspaceID)
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve data dir: %w", err)
	}
	return ws.Root, abs, nil
}

// resolveChangeRootCtx is resolveChangeRoot that reuses the workspace already
// resolved for this request (request_context.go) when ctx carries one.
func resolveChangeRootCtx(ctx context.Context, dataDir, workspaceID string) (root string, absData string, err error) {
	if rc := RequestContextFrom(ctx); rc != nil && rc.DataDir == dataDir && rc.WorkspaceID == workspaceID {
		ws, err := rc.Workspace()
		if err != nil {
			return "", "", err
		}
		abs, err := filepath.Abs(dataDir)
		if err != nil {
			return "", "", fmt.Errorf("resolve data dir: %w", err)
		}
		return ws.Root, abs, nil
	}
	return resolveChangeRoot(dataDir, workspaceID)
}

// WorkspaceFingerprint returns the current repo fingerprint (head/remote/content hash).
func WorkspaceFingerprint(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(context.Background(), "fingerprint", root)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// Index baseline governance (PAR-FRESH-06). The change-tracking baseline is what drift,
// ground's contract breaks and impact's changed-since compare against. It is built
// automatically when a workspace is registered or at the first ground, rebuilt
// automatically when HEAD moves on a clean worktree, and otherwise only by an explicit
// rebaseline (POST /index, which admits the indexer and admin roles only). Every build
// is appended to the governance history (govstore event index_baseline) with its reason
// and who triggered it, so the baseline is never reset silently.

// Baseline reasons, as the core records them (changetrack::BaselineReason). Only
// BaselineAdmin is an explicit request; the others are automatic.
const (
	BaselineRegistration = "registration"
	BaselineFirstGround  = "first_ground"
	BaselineHeadChanged  = "head_changed"
	BaselineAdmin        = "admin"
)

// AutoBaselinePrincipal is the history principal of an automatic build: the policy
// decided it, not the caller whose request observed the need.
const AutoBaselinePrincipal = "xmustard:auto-baseline"

// IndexBaseline is what a baseline build reports: the core's summary of the new
// baseline and the history event that records it.
type IndexBaseline struct {
	WorkspaceID  string  `json:"workspace_id"`
	Head         *string `json:"head"`
	Branch       *string `json:"branch"`
	IndexedAt    string  `json:"indexed_at"`
	Auto         bool    `json:"auto"`
	Reason       string  `json:"reason"`
	Dirty        bool    `json:"dirty"`
	TrackedFiles int     `json:"tracked_files"`
	Signatures   int     `json:"signatures"`
	// Replaced reports whether a baseline existed before this build, and PreviousHead
	// the HEAD it was taken at.
	Replaced     bool    `json:"replaced"`
	PreviousHead *string `json:"previous_head"`
	HistorySeq   int64   `json:"history_seq"`
}

// RebaselineIndex is the explicit rebaseline (POST /index; the route gate admits only
// the indexer and admin roles). principal is recorded in the history.
func RebaselineIndex(ctx context.Context, dataDir, workspaceID, principal string) (*IndexBaseline, error) {
	if _, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID); err != nil {
		return nil, err
	}
	unlock, err := lockBaseline(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return buildBaseline(ctx, dataDir, workspaceID, BaselineAdmin, principal)
}

// lockBaseline serializes the baseline builds of one workspace in this process and
// across processes (the store lock on the baseline file), so concurrent callers that
// find it missing build it once.
func lockBaseline(dataDir, workspaceID string) (func(), error) {
	return lockStore(filepath.Join(dataDir, "workspaces", workspaceID, "index_baseline.json"))
}

// buildBaseline runs the core's baseline build for reason and appends it to the
// history. The core replaces the baseline atomically; when the history write fails
// after it, the error says so, so a rebuild is never unrecorded silently.
func buildBaseline(ctx context.Context, dataDir, workspaceID, reason, principal string) (*IndexBaseline, error) {
	root, absData, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "index", absData, root, workspaceID, "--reason="+reason)
	if err != nil {
		return nil, err
	}
	var b IndexBaseline
	if err := json.Unmarshal(out, &b); err != nil {
		return nil, fmt.Errorf("index baseline built but its summary is undecodable: %w", err)
	}
	b.WorkspaceID, b.Reason, b.Auto = workspaceID, reason, reason != BaselineAdmin
	if b.HistorySeq, err = recordBaselineHistory(ctx, dataDir, &b, principal); err != nil {
		return &b, fmt.Errorf("index baseline rebuilt (%s) but not recorded in history: %w", reason, err)
	}
	return &b, nil
}

func recordBaselineHistory(ctx context.Context, dataDir string, b *IndexBaseline, principal string) (int64, error) {
	var seq int64
	err := memoryUpdate(ctx, dataDir, b.WorkspaceID, func(tx govstore.Tx) error {
		ev, err := tx.AppendEvent(ctx, govstore.EventInput{
			WorkspaceID: b.WorkspaceID, Type: govstore.EventIndexBaseline, Note: b.Reason,
			Data: map[string]any{
				"auto": b.Auto, "reason": b.Reason, "head": b.Head, "indexed_at": b.IndexedAt, "dirty": b.Dirty,
				"replaced": b.Replaced, "previous_head": b.PreviousHead, "tracked_files": b.TrackedFiles, "signatures": b.Signatures,
			},
		}, govstore.Actor{Principal: principal, HeadSHA: firstNonEmptyPtr(b.Head), Branch: firstNonEmptyPtr(b.Branch)})
		seq = ev.Seq
		return err
	})
	return seq, err
}

// IndexBaselineHistory lists the workspace's baseline builds after afterSeq, oldest
// first, at most limit (the store's default and cap apply).
func IndexBaselineHistory(ctx context.Context, dataDir, workspaceID string, afterSeq int64, limit int) ([]govstore.Event, error) {
	var out []govstore.Event
	err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) error {
		evs, err := r.ListEvents(ctx, govstore.EventFilter{
			WorkspaceID: workspaceID, Types: []string{govstore.EventIndexBaseline}, AfterSeq: afterSeq, Limit: limit,
		})
		out = evs
		return err
	})
	return out, err
}

// baselineState is what a drift result says about the baseline.
type baselineState struct {
	HasBaseline *bool   `json:"has_baseline"`
	HeadChanged bool    `json:"head_changed"`
	Dirty       bool    `json:"dirty"`
	Error       string  `json:"error"`
	Head        *string `json:"baseline_head"`
	IndexedAt   *string `json:"baseline_indexed_at"`
	Reason      *string `json:"baseline_reason"`
}

// dirtyHeadHold is why a HEAD move does not rebuild the baseline automatically: the
// uncommitted content would become the baseline, hiding its contract breaks.
const dirtyHeadHold = "HEAD moved on a dirty worktree: kept until it is clean or an indexer rebaselines"

// autoRebuild applies the automatic policy: the reason to rebuild ("" to keep the
// baseline) and why a due rebuild is held. missing is the trigger for a missing
// baseline (registration or first ground). Nothing is rebuilt on a guess: a drift
// that could not fingerprint the worktree, or does not say whether a baseline exists,
// keeps what is there.
func (d baselineState) autoRebuild(missing string) (reason, held string) {
	switch {
	case d.Error != "" || d.HasBaseline == nil:
		return "", ""
	case !*d.HasBaseline:
		return missing, ""
	case d.HeadChanged && d.Dirty:
		return "", dirtyHeadHold
	case d.HeadChanged:
		return BaselineHeadChanged, ""
	}
	return "", ""
}

// maintainBaseline applies the automatic policy to drift. When a rebuild is due it
// takes the baseline lock, re-reads drift (another caller may have rebuilt it), builds
// it and returns the drift after the build. held says why a due rebuild did not happen.
func maintainBaseline(ctx context.Context, dataDir, workspaceID string, drift json.RawMessage, missing string) (json.RawMessage, string) {
	var st baselineState
	if json.Unmarshal(drift, &st) != nil {
		return drift, ""
	}
	if reason, held := st.autoRebuild(missing); reason == "" {
		return drift, held
	}
	unlock, err := lockBaseline(dataDir, workspaceID)
	if err != nil {
		return drift, "automatic rebaseline not run: " + err.Error()
	}
	defer unlock()
	fresh, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return drift, "automatic rebaseline not run: drift failed: " + err.Error()
	}
	st = baselineState{}
	if json.Unmarshal(fresh, &st) != nil {
		return fresh, ""
	}
	reason, held := st.autoRebuild(missing)
	if reason == "" {
		return fresh, held
	}
	if _, err := buildBaseline(ctx, dataDir, workspaceID, reason, AutoBaselinePrincipal); err != nil {
		return fresh, fmt.Sprintf("automatic %s rebaseline failed: %v", reason, err)
	}
	after, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return fresh, "rebaselined, but drift after it failed: " + err.Error()
	}
	return after, ""
}

// ensureLoadBaseline is the registration trigger: a loaded workspace gets its baseline
// now (or a rebuild when HEAD moved on a clean worktree). It is best effort: the first
// ground builds it instead. A failed drift is already logged by the core bridge; a held
// or failed build is logged here.
func ensureLoadBaseline(dataDir, workspaceID string) {
	ctx := context.Background()
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return
	}
	if _, held := maintainBaseline(ctx, dataDir, workspaceID, drift, BaselineRegistration); held != "" {
		log.Printf("workspace %s: registration baseline: %s", workspaceID, held)
	}
}

// WorkspaceDrift reports stale-index / sibling-clone drift vs the baseline.
func WorkspaceDrift(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceDriftCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceDriftCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func WorkspaceDriftCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "drift", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// WorkspaceChangesSinceIndex returns files + dirty symbols changed since the baseline.
func WorkspaceChangesSinceIndex(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceChangesSinceIndexCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceChangesSinceIndexCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func WorkspaceChangesSinceIndexCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "changed-since", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// WorkspaceWorkingChanges returns uncommitted working-tree changes + dirty symbols,
// including contract-break flags (vs the index baseline) on modified symbols.
func WorkspaceWorkingChanges(dataDir, workspaceID string) (json.RawMessage, error) {
	return WorkspaceWorkingChangesCtx(context.Background(), dataDir, workspaceID)
}

// WorkspaceWorkingChangesCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
func WorkspaceWorkingChangesCtx(ctx context.Context, dataDir, workspaceID string) (json.RawMessage, error) {
	root, absData, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunChangetrack(ctx, "working-changes", absData, root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
