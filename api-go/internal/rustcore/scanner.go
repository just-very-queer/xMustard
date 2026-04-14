package rustcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	cmd := exec.CommandContext(
		ctx,
		"cargo",
		"run",
		"--quiet",
		"--bin",
		"xmustard-core",
		"--",
		"scan-signals",
		repoRoot,
	)
	cmd.Dir = rustCoreDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rust-core scan-signals failed: %w: %s", err, stderr.String())
	}

	var signals []DiscoverySignal
	if err := json.Unmarshal(stdout.Bytes(), &signals); err != nil {
		return nil, fmt.Errorf("decode rust-core signals: %w", err)
	}
	return signals, nil
}
