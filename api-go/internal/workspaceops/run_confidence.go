package workspaceops

import (
	"fmt"
	"os"
	"strings"
)

// Run-level confidence scoring: a per-run confidence signal derived from the
// run's terminal status, exit code, recorded error, and whether it ran under an
// approved plan. Complements the profile-level verification confidence.

type RunConfidence struct {
	RunID        string   `json:"run_id"`
	WorkspaceID  string   `json:"workspace_id"`
	IssueID      string   `json:"issue_id"`
	Confidence   int      `json:"confidence"`
	Level        string   `json:"level"` // high | medium | low
	Status       string   `json:"status"`
	Signals      []string `json:"signals"`
	CalculatedAt string   `json:"calculated_at"`
}

func scoreRunConfidence(workspaceID string, run runRecord) RunConfidence {
	conf := 50
	signals := []string{}
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "succeeded", "completed", "success", "done":
		conf += 25
		signals = append(signals, "run reached a successful terminal status")
	case "failed", "error":
		conf -= 30
		signals = append(signals, "run failed")
	case "cancelled", "canceled":
		conf -= 20
		signals = append(signals, "run was cancelled")
	case "running", "planning", "queued":
		conf -= 5
		signals = append(signals, "run has not finished")
	}
	if run.ExitCode != nil {
		if *run.ExitCode == 0 {
			conf += 15
			signals = append(signals, "exit code 0")
		} else {
			conf -= 20
			signals = append(signals, fmt.Sprintf("non-zero exit code %d", *run.ExitCode))
		}
	}
	if run.Error != nil && strings.TrimSpace(*run.Error) != "" {
		conf -= 15
		signals = append(signals, "run recorded an error")
	}
	if run.Plan != nil {
		conf += 10
		signals = append(signals, "ran under a recorded plan")
	}
	if conf < 0 {
		conf = 0
	}
	if conf > 100 {
		conf = 100
	}
	level := "low"
	if conf >= 70 {
		level = "high"
	} else if conf >= 45 {
		level = "medium"
	}
	return RunConfidence{
		RunID:        run.RunID,
		WorkspaceID:  workspaceID,
		IssueID:      run.IssueID,
		Confidence:   conf,
		Level:        level,
		Status:       run.Status,
		Signals:      signals,
		CalculatedAt: nowUTC(),
	}
}

// ScoreRunConfidence scores a single run by id.
func ScoreRunConfidence(dataDir, workspaceID, runID string) (*RunConfidence, error) {
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.RunID == runID {
			score := scoreRunConfidence(workspaceID, run)
			return &score, nil
		}
	}
	return nil, os.ErrNotExist
}
