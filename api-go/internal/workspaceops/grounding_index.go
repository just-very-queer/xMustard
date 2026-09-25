package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
)

// groundingIndex is the index section of `ground`: drift against the indexed
// baseline and the working-tree changes, with contract breaks on dirty symbols.
type groundingIndex struct {
	Drift           json.RawMessage `json:"drift"`
	ChangedFiles    int             `json:"changed_files"`
	DirtySymbols    int             `json:"dirty_symbols"`
	ContractBreaks  int             `json:"contract_breaks"`
	BrokenContracts []string        `json:"broken_contracts,omitempty"`
}

func (s *groundingIndex) build(ctx context.Context, dataDir, workspaceID string) error {
	drift, err := WorkspaceDriftCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return err
	}
	changesRaw, err := WorkspaceWorkingChangesCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return err
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
	for _, sym := range changes.DirtySymbols {
		if sym.ContractBreak {
			broken = append(broken, fmt.Sprintf("%s in %s (%s)", sym.Symbol, sym.Path, sym.SignatureChange))
		}
	}
	s.Drift = drift
	s.ChangedFiles = len(changes.ChangedFiles)
	s.DirtySymbols = len(changes.DirtySymbols)
	s.ContractBreaks = changes.ContractBreaks
	s.BrokenContracts = broken
	return nil
}
