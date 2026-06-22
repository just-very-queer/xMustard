package workspaceops

import (
	"fmt"
	"os"
	"strings"
)

// Standalone per-run brief: a single export-ready summary of one run — its
// identity, outcome, confidence, touched files, and verification signal — for
// handoff or audit without re-reading raw run logs.

type RunBrief struct {
	RunID        string        `json:"run_id"`
	WorkspaceID  string        `json:"workspace_id"`
	IssueID      string        `json:"issue_id"`
	Title        string        `json:"title"`
	Runtime      string        `json:"runtime"`
	Model        string        `json:"model"`
	Status       string        `json:"status"`
	ExitCode     *int          `json:"exit_code,omitempty"`
	Error        string        `json:"error,omitempty"`
	Confidence   RunConfidence `json:"confidence"`
	FilesTouched []string      `json:"files_touched"`
	HadPlan      bool          `json:"had_plan"`
	CreatedAt    string        `json:"created_at"`
	CompletedAt  string        `json:"completed_at,omitempty"`
	Headline     string        `json:"headline"`
	GeneratedAt  string        `json:"generated_at"`
}

func filesTouchedFromSummary(summary map[string]any) []string {
	out := []string{}
	if summary == nil {
		return out
	}
	for _, key := range []string{"files_touched", "changed_files", "files"} {
		raw, ok := summary[key]
		if !ok {
			continue
		}
		if list, ok := raw.([]any); ok {
			for _, item := range list {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func buildRunBrief(workspaceID string, run runRecord) RunBrief {
	confidence := scoreRunConfidence(workspaceID, run)
	errText := ""
	if run.Error != nil {
		errText = *run.Error
	}
	completed := ""
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	files := filesTouchedFromSummary(run.Summary)
	headline := fmt.Sprintf("%s run %s (%s confidence)", fallbackStr(run.Runtime, "agent"), fallbackStr(run.Status, "unknown"), confidence.Level)
	return RunBrief{
		RunID:        run.RunID,
		WorkspaceID:  workspaceID,
		IssueID:      run.IssueID,
		Title:        run.Title,
		Runtime:      run.Runtime,
		Model:        run.Model,
		Status:       run.Status,
		ExitCode:     run.ExitCode,
		Error:        errText,
		Confidence:   confidence,
		FilesTouched: files,
		HadPlan:      run.Plan != nil,
		CreatedAt:    run.CreatedAt,
		CompletedAt:  completed,
		Headline:     headline,
		GeneratedAt:  nowUTC(),
	}
}

// BuildRunBrief assembles the export brief for one run.
func BuildRunBrief(dataDir, workspaceID, runID string) (*RunBrief, error) {
	runs, err := ListRuns(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.RunID == runID {
			brief := buildRunBrief(workspaceID, run)
			return &brief, nil
		}
	}
	return nil, os.ErrNotExist
}
