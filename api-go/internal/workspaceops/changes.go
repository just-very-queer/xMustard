package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

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
// automatically when HEAD moves, and otherwise only by an explicit rebaseline (POST
// /index, which admits the indexer and admin roles only). An automatic baseline is the
// committed state: uncommitted changes to tracked files are taken as HEAD has them, so
// they stay visible against it. Every build is recorded in the governance history
// (govstore event index_baseline) with its reason and who triggered it before it
// replaces the stored baseline, so the baseline is never reset silently.

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
	WorkspaceID string  `json:"workspace_id"`
	Head        *string `json:"head"`
	Branch      *string `json:"branch"`
	IndexedAt   string  `json:"indexed_at"`
	Auto        bool    `json:"auto"`
	Reason      string  `json:"reason"`
	// Dirty reports that the baseline took in uncommitted changes to tracked files (an
	// explicit rebaseline of a dirty worktree); FromHead counts the uncommitted paths an
	// automatic baseline took as HEAD has them instead.
	Dirty        bool `json:"dirty"`
	FromHead     int  `json:"from_head"`
	TrackedFiles int  `json:"tracked_files"`
	Signatures   int  `json:"signatures"`
	// Replaced reports whether a baseline file existed before this build, PreviousHead
	// the HEAD it was taken at, and PreviousError why it could not be read.
	Replaced      bool    `json:"replaced"`
	PreviousHead  *string `json:"previous_head"`
	PreviousError string  `json:"previous_error,omitempty"`
	HistorySeq    int64   `json:"history_seq"`
}

// RebaselineIndex is the explicit rebaseline (POST /index; the route gate admits only
// the indexer and admin roles). principal is recorded in the history.
func RebaselineIndex(ctx context.Context, dataDir, workspaceID, principal string) (*IndexBaseline, error) {
	if _, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID); err != nil {
		return nil, err
	}
	unlock, err := lockStore(baselinePath(dataDir, workspaceID))
	if err != nil {
		return nil, err
	}
	defer unlock()
	return buildBaseline(ctx, dataDir, workspaceID, BaselineAdmin, principal)
}

// baselinePath is the stored baseline; its store lock serializes the baseline builds
// of one workspace in this process and across processes, so concurrent callers that
// find it missing build it once. stagedBaselinePath is where the core leaves a build
// until its history is recorded (changetrack::staged_baseline_path).
func baselinePath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "index_baseline.json")
}

func stagedBaselinePath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "index_baseline.staged.json")
}

// buildBaseline runs the core's baseline build for reason, staged beside the stored
// baseline, records it in the history, and only then puts it in place. A build whose
// history cannot be recorded is discarded and the stored baseline kept; a recorded
// build that cannot be put in place is an error that says so.
func buildBaseline(ctx context.Context, dataDir, workspaceID, reason, principal string) (*IndexBaseline, error) {
	root, absData, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	staged := stagedBaselinePath(absData, workspaceID)
	out, err := rustcore.RunChangetrack(ctx, "index", absData, root, workspaceID, "--reason="+reason, "--stage")
	if err != nil {
		_ = os.Remove(staged) // a build cut short after writing it
		return nil, err
	}
	var b IndexBaseline
	if err := json.Unmarshal(out, &b); err != nil {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("index baseline %s discarded: its summary is undecodable: %w", reason, err)
	}
	b.WorkspaceID, b.Reason, b.Auto = workspaceID, reason, reason != BaselineAdmin
	if b.HistorySeq, err = recordBaselineHistory(ctx, dataDir, &b, principal); err != nil {
		_ = os.Remove(staged)
		return nil, fmt.Errorf("index baseline %s discarded: its history could not be recorded: %w", reason, err)
	}
	if err := os.Rename(staged, baselinePath(absData, workspaceID)); err != nil {
		return &b, fmt.Errorf("index baseline %s recorded in history (seq %d) but not put in place: %w", reason, b.HistorySeq, err)
	}
	if err := syncDir(filepath.Dir(staged)); err != nil {
		return &b, fmt.Errorf("index baseline %s put in place but not flushed: %w", reason, err)
	}
	return &b, nil
}

func recordBaselineHistory(ctx context.Context, dataDir string, b *IndexBaseline, principal string) (int64, error) {
	data := map[string]any{
		"auto": b.Auto, "reason": b.Reason, "head": b.Head, "indexed_at": b.IndexedAt, "dirty": b.Dirty, "from_head": b.FromHead,
		"replaced": b.Replaced, "previous_head": b.PreviousHead, "tracked_files": b.TrackedFiles, "signatures": b.Signatures,
	}
	if b.PreviousError != "" {
		data["previous_error"] = b.PreviousError
	}
	var seq int64
	err := memoryUpdate(ctx, dataDir, b.WorkspaceID, func(tx govstore.Tx) error {
		ev, err := tx.AppendEvent(ctx, govstore.EventInput{
			WorkspaceID: b.WorkspaceID, Type: govstore.EventIndexBaseline, Note: b.Reason, Data: data,
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
	HasBaseline *bool  `json:"has_baseline"`
	HeadChanged bool   `json:"head_changed"`
	Error       string `json:"error"`
	// BaselineError is why a stored baseline cannot be read (it then reads as missing).
	BaselineError string  `json:"baseline_error"`
	Head          *string `json:"baseline_head"`
	IndexedAt     *string `json:"baseline_indexed_at"`
	Reason        *string `json:"baseline_reason"`
	Dirty         *bool   `json:"baseline_dirty"`
}

// Why a due automatic rebuild did not run.
const (
	unreadableHold   = "the stored index baseline cannot be read, so it is kept for an indexer to rebaseline (POST /index): "
	buildBusyHold    = "a baseline build is in progress; a later ground reports it"
	notRunHoldPrefix = "automatic rebaseline not run: "
)

// autoRebuild applies the automatic policy: the reason to rebuild ("" to keep the
// baseline) and why a due rebuild is held. missing is the trigger for a missing
// baseline (registration or first ground). Nothing is rebuilt on a guess: a drift
// that could not fingerprint the worktree, or does not say whether a baseline exists,
// keeps what is there, and a stored baseline that cannot be read (possibly an admin
// one) is not replaced by an automatic build.
func (d baselineState) autoRebuild(missing string) (reason, held string) {
	switch {
	case d.Error != "" || d.HasBaseline == nil:
		return "", ""
	case d.BaselineError != "":
		return "", unreadableHold + d.BaselineError
	case !*d.HasBaseline:
		return missing, ""
	case d.HeadChanged:
		return BaselineHeadChanged, ""
	}
	return "", ""
}

// baselineTrigger is an automatic caller of the baseline policy: the reason a missing
// baseline is built under, and how the caller takes the baseline lock.
type baselineTrigger struct {
	missing string
	lock    func(key string) (func(), error)
	// superseded, when set, reports under the lock that a build replaced the stored
	// baseline after the caller read its drift. Only a caller that discards the drift
	// sets it (see observedAt).
	superseded func() bool
}

var (
	// registration waits its turn: nobody waits on it (it runs detached, or in the
	// one-shot ops CLI)
	registrationTrigger = baselineTrigger{missing: BaselineRegistration, lock: lockStore}
	// ground never waits on another build: it answers now, a later ground sees the result
	groundTrigger = baselineTrigger{missing: BaselineFirstGround, lock: tryLockStore}
)

// observedAt is t for a caller about to read drift that needs none back: a baseline
// put in place at path after this call (every build renames a new file there) answers
// the trigger, so the caller does not read drift again under the lock. Registration
// uses it: after an explicit rebaseline it waited for, the re-read would only confirm
// that build, and its core run would overlap the requests that follow (WS-FIX-07).
func (t baselineTrigger) observedAt(path string) baselineTrigger {
	before, _ := os.Stat(path)
	t.superseded = func() bool {
		after, err := os.Stat(path)
		return err == nil && (before == nil || !os.SameFile(before, after))
	}
	return t
}

// maintainBaseline applies the automatic policy to drift. When a rebuild is due it
// takes the baseline lock, re-reads drift (another caller may have rebuilt it), builds
// it and returns the drift after the build. held says why a due rebuild did not happen.
// A trigger whose baseline was superseded while it waited returns drift unchanged.
func maintainBaseline(ctx context.Context, dataDir, workspaceID string, drift json.RawMessage, t baselineTrigger) (json.RawMessage, string) {
	var st baselineState
	if json.Unmarshal(drift, &st) != nil {
		return drift, ""
	}
	if reason, held := st.autoRebuild(t.missing); reason == "" {
		return drift, held
	}
	unlock, err := t.lock(baselinePath(dataDir, workspaceID))
	switch {
	case errors.Is(err, errStoreBusy):
		return drift, buildBusyHold
	case err != nil:
		return drift, notRunHoldPrefix + err.Error()
	}
	defer unlock()
	if t.superseded != nil && t.superseded() {
		return drift, ""
	}
	fresh, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return drift, notRunHoldPrefix + "drift failed: " + err.Error()
	}
	st = baselineState{}
	if json.Unmarshal(fresh, &st) != nil {
		return fresh, ""
	}
	reason, held := st.autoRebuild(t.missing)
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

// EnsureRegistrationBaseline is the registration trigger: a registered workspace gets
// its baseline now (or a rebuild when HEAD moved). It returns why a due build did not
// happen, "" when none was due or it was built. The one-shot ops CLI runs it in line;
// the API runs it detached (StartRegistrationBaseline). The first ground retries it.
// A build put in place while it waits for the lock (an explicit rebaseline) answers it.
func EnsureRegistrationBaseline(ctx context.Context, dataDir, workspaceID string) string {
	t := registrationTrigger.observedAt(baselinePath(dataDir, workspaceID))
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return "drift failed: " + err.Error()
	}
	_, held := maintainBaseline(ctx, dataDir, workspaceID, drift, t)
	return held
}

// registrationBaselineTimeout bounds one detached registration build: drift, the wait
// for the heavy slot and the baseline lock, the build and its history.
const registrationBaselineTimeout = 5 * time.Minute

// registrationBaselines holds the detached registration builds by baseline path: at
// most one per workspace, and what WaitRegistrationBaselines waits on.
var registrationBaselines = struct {
	sync.Mutex
	running map[string]chan struct{}
}{running: map[string]chan struct{}{}}

// StartRegistrationBaseline runs EnsureRegistrationBaseline detached, so registration
// answers without waiting for the build, under its own bounded context (the
// registration request may end first). A workspace whose registration build is still
// running starts none: that build re-reads drift under the lock anyway.
func StartRegistrationBaseline(dataDir, workspaceID string) {
	key := baselinePath(dataDir, workspaceID)
	registrationBaselines.Lock()
	defer registrationBaselines.Unlock()
	if _, running := registrationBaselines.running[key]; running {
		return
	}
	done := make(chan struct{})
	registrationBaselines.running[key] = done
	go func() {
		defer func() {
			registrationBaselines.Lock()
			delete(registrationBaselines.running, key)
			registrationBaselines.Unlock()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), registrationBaselineTimeout)
		defer cancel()
		if held := EnsureRegistrationBaseline(ctx, dataDir, workspaceID); held != "" {
			log.Printf("workspace %s: registration baseline: %s", workspaceID, held)
		}
	}()
}

// WaitRegistrationBaselines waits until the detached registration builds running now
// have finished, or ctx ends.
func WaitRegistrationBaselines(ctx context.Context) error {
	registrationBaselines.Lock()
	pending := slices.Collect(maps.Values(registrationBaselines.running))
	registrationBaselines.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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
