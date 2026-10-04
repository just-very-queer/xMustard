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

// SignalScan is one bounded signal scan: the signals found, the files read, and why
// the walk stopped early when a bound cut it (the core reads at most 10,000 files and
// 64 MiB, in name order).
type SignalScan struct {
	Signals   []DiscoverySignal `json:"signals"`
	FilesRead int               `json:"files_read"`
	Truncated bool              `json:"truncated"`
	Reason    string            `json:"reason,omitempty"`
}

func ScanSignals(ctx context.Context, repoRoot string) (SignalScan, error) {
	stdout, err := runCoreCtx(ctx, "scan-signals", repoRoot)
	if err != nil {
		return SignalScan{}, err
	}
	var scan SignalScan
	if err := json.Unmarshal(stdout, &scan); err != nil {
		return SignalScan{}, fmt.Errorf("decode rust-core signals: %w", err)
	}
	return scan, nil
}
