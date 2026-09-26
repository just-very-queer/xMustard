package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"

	"xmustard/api-go/internal/rustcore"
)

// groundingIndex is the index section of `ground`: drift against the indexed
// baseline and the working-tree changes, with contract breaks on dirty symbols.
// A count is null (unknown) when the change-tracking result could not be decoded
// or lacks it; it is never reported as 0.
type groundingIndex struct {
	Drift           json.RawMessage `json:"drift"`
	ChangedFiles    *int            `json:"changed_files"`
	DirtySymbols    *int            `json:"dirty_symbols"`
	ContractBreaks  *int            `json:"contract_breaks"`
	BrokenContracts []string        `json:"broken_contracts,omitempty"`
	// Coverage is the symbol graph's coverage block, the one search, explain and
	// impact report: whether it is complete and, per language pack, how many files
	// were extracted with a grammar (supported), had no grammar in this build
	// (unsupported) or fell back to the regexes (failed).
	Coverage json.RawMessage `json:"coverage"`
}

func (s *groundingIndex) build(ctx context.Context, dataDir, workspaceID string) ([]GroundingUnknown, error) {
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
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
	var changes struct {
		ChangedFiles   *[]json.RawMessage `json:"changed_files"`
		ContractBreaks *int               `json:"contract_breaks"`
		DirtySymbols   *[]struct {
			Path            string `json:"path"`
			Symbol          string `json:"symbol"`
			ContractBreak   bool   `json:"contract_break"`
			SignatureChange string `json:"signature_change"`
		} `json:"dirty_symbols"`
	}
	if err := json.Unmarshal(changesRaw, &changes); err != nil {
		reason := "working-changes result undecodable: " + err.Error()
		return append(unknown,
			GroundingUnknown{Field: "changed_files", Reason: reason},
			GroundingUnknown{Field: "dirty_symbols", Reason: reason},
			GroundingUnknown{Field: "contract_breaks", Reason: reason}), nil
	}
	if changes.ChangedFiles != nil {
		n := len(*changes.ChangedFiles)
		s.ChangedFiles = &n
	} else {
		unknown = append(unknown, GroundingUnknown{Field: "changed_files", Reason: "working-changes result has no changed_files"})
	}
	if changes.DirtySymbols != nil {
		n := len(*changes.DirtySymbols)
		s.DirtySymbols = &n
		broken := []string{}
		for _, sym := range *changes.DirtySymbols {
			if sym.ContractBreak {
				broken = append(broken, fmt.Sprintf("%s in %s (%s)", sym.Symbol, sym.Path, sym.SignatureChange))
			}
		}
		s.BrokenContracts = broken
	} else {
		unknown = append(unknown, GroundingUnknown{Field: "dirty_symbols", Reason: "working-changes result has no dirty_symbols"})
	}
	if changes.ContractBreaks != nil {
		s.ContractBreaks = changes.ContractBreaks
	} else {
		unknown = append(unknown, GroundingUnknown{Field: "contract_breaks", Reason: "working-changes result has no contract_breaks"})
	}
	return unknown, nil
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
