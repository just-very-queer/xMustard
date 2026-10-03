package workspaceops

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"xmustard/api-go/internal/rustcore"
)

// Build and query the workspace symbol graph via rust-core.

func WorkspaceSymbolGraph(dataDir, workspaceID string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "build", root, workspaceID)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func WorkspaceHotspots(dataDir, workspaceID string, limit int) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "hotspots", root, workspaceID, strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func SymbolBlastRadius(dataDir, workspaceID, symbol string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(context.Background(), "blast-radius", root, workspaceID, symbol)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// SymbolImpactCtx is impact v2 (WS-35) on a symbol, UID or path: its dependents or
// dependencies by depth tier, with risk and the completeness envelope, over the code
// index's resolved graph (the legacy graph when the root has no index). params are the
// ImpactBlast parameters (ImpactFlags). Cancelling ctx cancels the Rust work.
func SymbolImpactCtx(ctx context.Context, dataDir, workspaceID, symbol string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactBlast, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"impact"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID, symbol)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// TraceSymbolsCtx is the shortest path from one symbol to another with per-hop edges
// (WS-35); params are the ImpactTrace parameters. Cancelling ctx cancels the Rust work.
func TraceSymbolsCtx(ctx context.Context, dataDir, workspaceID, from, to string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactTrace, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"trace"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID, from, to)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// DetectChangesCtx maps a scoped diff onto symbols (WS-35, PAR-IMP-06): the changed
// symbols with contract breaks, and their dependents. params are the ImpactChanges
// parameters, scope among them. The core parses the changed files in a one-shot
// process. Cancelling ctx cancels the Rust work.
func DetectChangesCtx(ctx context.Context, dataDir, workspaceID string, params map[string]string) (json.RawMessage, error) {
	flags, err := ImpactFlags(ImpactChanges, params)
	if err != nil {
		return nil, err
	}
	return indexedRead(ctx, dataDir, workspaceID, func(root string, read codeIndexRead) ([]byte, error) {
		args := []string{"changes"}
		args = append(append(append(args, read.flags()...), flags...), root, workspaceID)
		return rustcore.RunSymbolgraph(ctx, args...)
	})
}

// FileImpactCtx is impact(path=) (WS-FIX-05): the files that reference a symbol defined
// in path, up to the depth hops of params (the ImpactFile parameters, default 4), with
// the graph's freshness and coverage. The walk is the legacy file-level one, started
// from the file; found is false when the graph does not hold the file. The path is
// confined to the workspace before it reaches the core, which only looks it up in the
// graph.
func FileImpactCtx(ctx context.Context, dataDir, workspaceID, path string, params map[string]string) (json.RawMessage, error) {
	if _, err := ImpactFlags(ImpactFile, params); err != nil {
		return nil, err
	}
	depth := cmp.Or(params["depth"], "4")
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	rel, err := ConfineWorkspacePath(root, path)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"impact-file"}
	args = append(append(args, read.flags()...), root, workspaceID, rel, depth)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	return read.annotate(out), nil
}

// impactPagingMember is the member an impact result carries when a tier page has a
// successor, beside the members the budget layer's own contract already writes for a
// producer-paged list (toolbudget.ReportMember's siblings in apply.go).
const (
	impactHasMoreMember    = "has_more"
	impactNextCursorMember = "next_cursor"
)

// WithImpactPaging adds the paging members to an impact result whose tiers were cut:
// has_more, and a next_cursor signed for the ranking that asked for the page. The core
// already emits each tier's next_offset, so this reads the page rather than
// recomputing it. A diff-scope result holds its walk under affected and only that walk
// pages, so the members go at the top level beside by_depth either way: one result,
// one page, one cursor. A result with no cut tier, or one that is not a JSON object, is
// returned unchanged: the members are never invented for a form that has no list to
// page.
func WithImpactPaging(workspaceID string, q url.Values, raw json.RawMessage) json.RawMessage {
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil || res == nil {
		return raw
	}
	next := 0
	for _, tiers := range []json.RawMessage{res["by_depth"], tiersOf(res["affected"])} {
		if n := nextTierOffset(tiers); n > next {
			next = n
		}
	}
	if next == 0 {
		return raw
	}
	cursor, err := json.Marshal(ImpactCursor(workspaceID, q)(next))
	if err != nil {
		return raw
	}
	res[impactHasMoreMember] = json.RawMessage("true")
	res[impactNextCursorMember] = cursor
	out, err := json.Marshal(res)
	if err != nil {
		return raw
	}
	return out
}

// tiersOf returns the by_depth member of a nested impact answer (a diff-scope result's
// affected walk), or nil when it holds none.
func tiersOf(body json.RawMessage) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return nil
	}
	return obj["by_depth"]
}

// nextTierOffset returns the offset the deepest cut tier continues from: the highest
// next_offset among the tiers, or 0 when none was cut. next_offset is absent on a tier
// that returned its last item (the core skips it when there is none), so a complete
// result yields 0 and gets no cursor. A page that carried no items at all yields 0 too:
// summary_only drops every tier's items while keeping next_offset, and a cursor over a
// ranking the caller cannot read would advance through pages that say nothing.
func nextTierOffset(tiers json.RawMessage) int {
	var page []struct {
		Items      []json.RawMessage `json:"items"`
		NextOffset *int              `json:"next_offset"`
	}
	if json.Unmarshal(tiers, &page) != nil {
		return 0
	}
	next := 0
	for _, tier := range page {
		if len(tier.Items) == 0 {
			continue
		}
		if tier.NextOffset != nil && *tier.NextOffset > next {
			next = *tier.NextOffset
		}
	}
	return next
}

// ImpactBudgetTrimmed reports whether the output budget cut a tier page. A cursor
// continues at the core's next_offset, which describes the core's page rather than the
// delivered one, so a trimmed page's cursor would skip the items the budget removed.
// The budget's report names the rung that cut it; the caller uses this to drop the
// cursor instead of letting a client follow it past items it never saw. The tier's own
// total still reports what the tier holds, so re-paging with offset= is lossless.
func ImpactBudgetTrimmed(out []byte) bool {
	var res struct {
		OutputBudget *struct {
			Stages []string `json:"stages"`
		} `json:"output_budget"`
	}
	if json.Unmarshal(out, &res) != nil || res.OutputBudget == nil {
		return false
	}
	for _, stage := range res.OutputBudget.Stages {
		if stage == "tiers_trimmed" || stage == "tiers_total" {
			return true
		}
	}
	return false
}

// ReconcileImpactPaging fixes up a budgeted impact result whose tier page the budget
// cut. The cursor the handler signed describes the core's page, not the delivered one,
// so following it would skip the items the budget removed. It is re-signed here to
// continue at the first item the caller did not receive, which is the core's offset plus
// the items actually delivered: the caller then pages without computing an offset, which
// is the hand-computed paging this row exists to remove. When nothing can be signed (a
// rung removed the tiers outright), the cursor is dropped and has_more stands so the
// caller knows to re-page. A result the budget did not cut is returned unchanged.
func ReconcileImpactPaging(workspaceID string, q url.Values, out []byte) []byte {
	var res map[string]json.RawMessage
	if json.Unmarshal(out, &res) != nil || res == nil {
		return out
	}
	if !ImpactBudgetTrimmed(out) {
		return out
	}
	next, ok := deliveredTierOffset(res)
	switch {
	case !ok:
		// A rung removed the tiers outright (tiers_total), so there is nothing left to
		// count and nothing to sign: has_more stands and the caller re-pages.
		delete(res, impactNextCursorMember)
		res[impactHasMoreMember] = json.RawMessage("true")
	case next == 0:
		res[impactHasMoreMember] = json.RawMessage("true")
	default:
		cursor, err := json.Marshal(ImpactCursor(workspaceID, q)(next))
		if err != nil {
			return out
		}
		res[impactNextCursorMember] = cursor
		res[impactHasMoreMember] = json.RawMessage("true")
	}
	reconciled, err := json.Marshal(res)
	if err != nil {
		return out
	}
	return reconciled
}

// deliveredTierOffset returns the offset the next page continues from when a budget cut
// this one: the page's own offset plus the fewest items any tier delivered, so no tier
// is skipped (the shortest tier is the binding constraint). ok is false when no tier
// list survived to read, which is the tiers_total case.
func deliveredTierOffset(res map[string]json.RawMessage) (int, bool) {
	next := 0
	sawTiers := false
	for _, body := range []json.RawMessage{res["by_depth"], tiersOf(res["affected"])} {
		if len(body) == 0 {
			continue
		}
		var page []struct {
			Offset int               `json:"offset"`
			Total  int               `json:"total"`
			Items  []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(body, &page) != nil {
			return 0, false
		}
		for _, tier := range page {
			sawTiers = true
			// A tier that delivered its total is finished and does not bind the cursor.
			if len(tier.Items) >= tier.Total {
				continue
			}
			if at := tier.Offset + len(tier.Items); next == 0 || at < next {
				next = at
			}
		}
	}
	return next, sawTiers
}

// indexedRead brings the workspace's code index to its observed identity, runs one
// graph read and reports the refresh it waited for in the result's coverage.
func indexedRead(ctx context.Context, dataDir, workspaceID string, run func(root string, read codeIndexRead) ([]byte, error)) (json.RawMessage, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	out, err := run(root, read)
	var refusal *rustcore.Refusal
	if errors.As(err, &refusal) {
		// the core refused the caller's input (a revision that names no commit, a
		// worktree that is not a checkout); its refusal names only that input
		return nil, fmt.Errorf("%s: %w", refusal.Reason, ErrInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	return read.annotate(out), nil
}

// FileCluster mirrors rust-core's community-cluster output.
type FileCluster struct {
	ClusterID int      `json:"cluster_id"`
	Label     string   `json:"label"`
	Files     []string `json:"files"`
	Size      int      `json:"size"`
}

// WorkspaceClustersCtx returns the file communities (label-propagation clusters) for
// the workspace's symbol graph. Cancelling ctx cancels the clustering (see
// rustcore.runCoreCtx).
func WorkspaceClustersCtx(ctx context.Context, dataDir, workspaceID string) ([]FileCluster, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunSymbolgraph(ctx, "clusters", root, workspaceID)
	if err != nil {
		return nil, err
	}
	var clusters []FileCluster
	if err := json.Unmarshal(out, &clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}

// PathGraph is where a file sits in the code graph, for `explain`: its community
// cluster (its functional neighbourhood; nil when the file is in none) and the
// freshness and coverage of the graph that answered.
type PathGraph struct {
	Cluster   *FileCluster    `json:"cluster"`
	Freshness json.RawMessage `json:"freshness,omitempty"`
	Coverage  json.RawMessage `json:"coverage,omitempty"`
}

// PathGraphCtx reads path's cluster from the code index (refreshed first, see
// ensureCodeIndex; the core takes the observed identity after the subcommand) or the
// legacy graph. Cancelling ctx cancels the Rust work.
func PathGraphCtx(ctx context.Context, dataDir, workspaceID, path string) (*PathGraph, error) {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	read := ensureCodeIndex(ctx, root)
	args := []string{"cluster-of"}
	args = append(append(args, read.flags()...), root, workspaceID, path)
	out, err := rustcore.RunSymbolgraph(ctx, args...)
	if err != nil {
		return nil, err
	}
	var g PathGraph
	if err := json.Unmarshal(read.annotate(out), &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// LiveDocumentSymbols runs a live LSP session (rust-core spawns the language
// server, runs the handshake, and returns normalized document symbols). Servers
// that aren't installed degrade gracefully to {"available": false, ...}.
func LiveDocumentSymbols(dataDir, workspaceID, path string) (json.RawMessage, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunLspDocumentSymbols(workspaceID, root, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
