package rustcore

import (
	"context"
	"encoding/json"
	"fmt"
)

type DiscoverySignal struct {
	SignalID    string        `json:"signal_id"`
	Kind        string        `json:"kind"`
	Severity    string        `json:"severity"`
	Title       string        `json:"title"`
	Summary     string        `json:"summary"`
	FilePath    string        `json:"file_path"`
	Line        int           `json:"line"`
	Evidence    []EvidenceRef `json:"evidence"`
	Tags        []string      `json:"tags"`
	Fingerprint string        `json:"fingerprint"`
}

type EvidenceRef struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

func ScanSignals(ctx context.Context, repoRoot string) ([]DiscoverySignal, error) {
	stdout, err := runCoreCtx(ctx, "scan-signals", repoRoot)
	if err != nil {
		return nil, err
	}
	var signals []DiscoverySignal
	if err := json.Unmarshal(stdout, &signals); err != nil {
		return nil, fmt.Errorf("decode rust-core signals: %w", err)
	}
	return signals, nil
}
