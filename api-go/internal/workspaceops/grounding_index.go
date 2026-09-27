package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"

	"xmustard/api-go/internal/budget"
	"xmustard/api-go/internal/rustcore"
)

// groundingIndex is the index section of `ground`: drift against the indexed
// baseline and the working-tree changes, with contract breaks on dirty symbols.
// A count is null (unknown) when the change-tracking result could not be decoded
// or lacks it; it is never reported as 0.
type groundingIndex struct {
	Drift json.RawMessage `json:"drift"`
	// Baseline is the index baseline drift and contract breaks compare against, after
	// the automatic policy ran (see maintainBaseline); null, and listed unknown, when
	// there is none.
	Baseline        *GroundBaseline `json:"baseline"`
	ChangedFiles    *int            `json:"changed_files"`
	DirtySymbols    *int            `json:"dirty_symbols"`
	ContractBreaks  *int            `json:"contract_breaks"`
	BrokenContracts []string        `json:"broken_contracts,omitempty"`
	// Coverage is the symbol graph's coverage block, the one search, explain and
	// impact report: whether it is complete and, per language pack, how many files
	// were extracted with a grammar (supported), had no grammar in this build
	// (unsupported) or fell back past the parse bounds or with parse errors (failed).
	// It comes from the same source they read: the code index's snapshot when the
	// root has one (as of its last generation; ground does not refresh the index, as
	// its subsystems section does not), else the legacy graph.
	Coverage json.RawMessage `json:"coverage"`
}

// GroundBaseline is ground's view of the index baseline (PAR-FRESH-06): the HEAD it
// was taken at, when, whether it was built automatically, and why. Held says why a
// due automatic rebuild did not run (a dirty worktree after a HEAD move, a failure).
type GroundBaseline struct {
	Head      *string `json:"head"`
	IndexedAt string  `json:"indexed_at"`
	Auto      bool    `json:"auto"`
	Reason    string  `json:"reason"`
	Held      string  `json:"held,omitempty"`
}

func (s *groundingIndex) build(ctx context.Context, dataDir, workspaceID string) ([]GroundingUnknown, error) {
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	// ground never waits for the heavy slot: a busy slot holds the rebuild to a later call
	drift, held := maintainBaseline(budget.WithoutHeavyWait(ctx), dataDir, workspaceID, drift, BaselineFirstGround)
	changesRaw, err := WorkspaceWorkingChangesCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	var unknown []GroundingUnknown
	if u := s.buildCoverage(ctx, dataDir, workspaceID); u != nil {
		unknown = append(unknown, *u)
	}
	if json.Valid(drift) {
		s.Drift = drift
	} else {
		unknown = append(unknown, GroundingUnknown{Field: "drift", Reason: "drift result is not valid JSON"})
	}
	if u := s.buildBaseline(drift, held); u != nil {
		unknown = append(unknown, *u)
	}
	return append(unknown, s.buildChanges(changesRaw)...), nil
}

// buildBaseline fills Baseline from drift; without a baseline it stays null and is
// reported unknown with the reason.
func (s *groundingIndex) buildBaseline(drift json.RawMessage, held string) *GroundingUnknown {
	var st baselineState
	if err := json.Unmarshal(drift, &st); err != nil {
		return &GroundingUnknown{Field: "baseline", Reason: "drift result undecodable: " + err.Error()}
	}
	switch {
	case st.HasBaseline == nil:
		return &GroundingUnknown{Field: "baseline", Reason: "drift result does not say whether a baseline exists"}
	case !*st.HasBaseline && st.Error != "":
		return &GroundingUnknown{Field: "baseline", Reason: "no index baseline, and drift could not fingerprint the worktree to build one: " + st.Error}
	case !*st.HasBaseline:
		return &GroundingUnknown{Field: "baseline", Reason: fallbackString(held, "no index baseline exists")}
	case st.IndexedAt == nil || st.Reason == nil:
		return &GroundingUnknown{Field: "baseline", Reason: "drift result lacks the baseline's indexed_at or reason"}
	}
	s.Baseline = &GroundBaseline{Head: st.Head, IndexedAt: *st.IndexedAt, Auto: *st.Reason != BaselineAdmin, Reason: *st.Reason, Held: held}
	return nil
}

// buildChanges fills the change counts from the working-changes result. A count is
// null and listed unknown when the core names it unknown (a failed listing, a partial
// symbol pass, no baseline to compare signatures against) or the result lacks it.
func (s *groundingIndex) buildChanges(raw json.RawMessage) []GroundingUnknown {
	var changes struct {
		ChangedFiles      *[]json.RawMessage `json:"changed_files"`
		ChangedFilesTotal *int               `json:"changed_files_total"`
		DirtySymbolsTotal *int               `json:"dirty_symbols_total"`
		ContractBreaks    *int               `json:"contract_breaks"`
		DirtySymbols      *[]struct {
			Path            string `json:"path"`
			Symbol          string `json:"symbol"`
			ContractBreak   bool   `json:"contract_break"`
			SignatureChange string `json:"signature_change"`
		} `json:"dirty_symbols"`
		Unknown []GroundingUnknown `json:"unknown"`
	}
	fields := []string{"changed_files", "dirty_symbols", "contract_breaks"}
	if err := json.Unmarshal(raw, &changes); err != nil {
		unknown := make([]GroundingUnknown, 0, len(fields))
		for _, f := range fields {
			unknown = append(unknown, GroundingUnknown{Field: f, Reason: "working-changes result undecodable: " + err.Error()})
		}
		return unknown
	}
	if changes.DirtySymbols != nil {
		broken := []string{}
		for _, sym := range *changes.DirtySymbols {
			if sym.ContractBreak {
				broken = append(broken, fmt.Sprintf("%s in %s (%s)", sym.Symbol, sym.Path, sym.SignatureChange))
			}
		}
		s.BrokenContracts = broken
	}
	reported := map[string]string{}
	for _, u := range changes.Unknown {
		reported[u.Field] = u.Reason
	}
	counts := []struct {
		field string
		dst   **int
		value *int
	}{
		// the totals count past the listing caps; a core without them lists everything
		{"changed_files", &s.ChangedFiles, firstPresent(changes.ChangedFilesTotal, lengthOf(changes.ChangedFiles))},
		{"dirty_symbols", &s.DirtySymbols, firstPresent(changes.DirtySymbolsTotal, lengthOf(changes.DirtySymbols))},
		{"contract_breaks", &s.ContractBreaks, changes.ContractBreaks},
	}
	var unknown []GroundingUnknown
	for _, c := range counts {
		reason, isReported := reported[c.field]
		switch {
		case isReported:
			unknown = append(unknown, GroundingUnknown{Field: c.field, Reason: reason})
		case c.value != nil:
			*c.dst = c.value
		default:
			unknown = append(unknown, GroundingUnknown{Field: c.field, Reason: "working-changes result has no " + c.field})
		}
	}
	return unknown
}

// lengthOf is the length of a decoded list, nil when the list was absent or null.
func lengthOf[T any](list *[]T) *int {
	if list == nil {
		return nil
	}
	n := len(*list)
	return &n
}

// firstPresent is the first non-nil value.
func firstPresent(values ...*int) *int {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// buildCoverage fills Coverage; a failure leaves it null and is reported unknown,
// never as a complete or empty coverage.
func (s *groundingIndex) buildCoverage(ctx context.Context, dataDir, workspaceID string) *GroundingUnknown {
	root, _, err := resolveChangeRootCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return &GroundingUnknown{Field: "coverage", Reason: "workspace root unavailable: " + err.Error()}
	}
	out, err := rustcore.RunSymbolgraph(ctx, "coverage", root, workspaceID)
	if err != nil {
		return &GroundingUnknown{Field: "coverage", Reason: "symbol graph coverage failed: " + err.Error()}
	}
	var probe struct {
		Languages map[string]json.RawMessage `json:"languages"`
	}
	if json.Unmarshal(out, &probe) != nil || probe.Languages == nil {
		return &GroundingUnknown{Field: "coverage", Reason: "symbol graph coverage has no languages"}
	}
	s.Coverage = out
	return nil
}
