package workspaceops

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// ExplainRunFailure produces a concise, grounded "why did this run fail" answer
// for a single run: it confirms the failure, extracts the failure signals from the
// run output, pulls file paths and error lines out of that output, and correlates
// them with the files that changed in the working tree — so an agent gets the
// likely cause and where to look instead of re-reading the whole log.

type FailureExplanation struct {
	RunID           string   `json:"run_id"`
	Failed          bool     `json:"failed"`
	Status          string   `json:"status"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	Signals         []string `json:"signals"`          // detected failure markers
	ErrorLines      []string `json:"error_lines"`      // salient lines pulled from output
	ImplicatedPaths []string `json:"implicated_paths"` // paths named in output that also changed
	ChangedFiles    []string `json:"changed_files"`    // current working-tree changes
	Summary         string   `json:"summary"`
	GeneratedAt     string   `json:"generated_at"`
}

// pathLikePattern matches repo-relative-ish paths (a/b/c.ext) that appear in logs.
var pathLikePattern = regexp.MustCompile(`[\w./-]+\.[A-Za-z]{1,5}`)

// errorLinePattern flags lines worth surfacing from a failure log.
var errorLinePattern = regexp.MustCompile(`(?i)\b(error|fail(ed|ure)?|panic|exception|undefined|cannot|expected|traceback|fatal)\b`)

func ExplainRunFailure(dataDir, workspaceID, runID string) (*FailureExplanation, error) {
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	output := ""
	if strings.TrimSpace(run.OutputPath) != "" {
		if content, readErr := os.ReadFile(run.OutputPath); readErr == nil {
			output = string(content)
		}
	}

	exp := &FailureExplanation{
		RunID:       runID,
		Status:      run.Status,
		ExitCode:    run.ExitCode,
		Signals:     detectPatchIssues(run, output),
		GeneratedAt: nowUTC(),
	}
	exp.Failed = runLooksFailed(run, exp.Signals)

	exp.ErrorLines = salientErrorLines(output, 8)
	exp.ChangedFiles = currentChangedFiles(dataDir, workspaceID)
	exp.ImplicatedPaths = intersectMentionedPaths(output, exp.ChangedFiles)
	exp.Summary = summarizeFailure(exp)
	// feed the outcome back into ranking: suppress the implicated paths of a failure.
	if exp.Failed && len(exp.ImplicatedPaths) > 0 {
		_ = RecordFeedback(dataDir, workspaceID, "run_fail", exp.ImplicatedPaths)
	}
	return exp, nil
}

func runLooksFailed(run *runRecord, signals []string) bool {
	if run.ExitCode != nil && *run.ExitCode != 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "failed", "error", "errored", "cancelled":
		return true
	}
	return len(signals) > 0
}

// salientErrorLines returns up to `limit` distinct error-ish lines from the output.
func salientErrorLines(output string, limit int) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !errorLinePattern.MatchString(line) {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// intersectMentionedPaths returns changed files that are also named in the output —
// the strongest signal for where the failure originates.
func intersectMentionedPaths(output string, changed []string) []string {
	if len(changed) == 0 || output == "" {
		return nil
	}
	mentioned := map[string]struct{}{}
	for _, m := range pathLikePattern.FindAllString(output, -1) {
		mentioned[strings.TrimPrefix(m, "./")] = struct{}{}
	}
	var hits []string
	for _, c := range changed {
		base := c
		if i := strings.LastIndex(c, "/"); i >= 0 {
			base = c[i+1:]
		}
		_, full := mentioned[c]
		_, byBase := mentioned[base]
		if full || byBase {
			hits = append(hits, c)
		}
	}
	sort.Strings(hits)
	return hits
}

// currentChangedFiles lists the working-tree changed paths (best-effort).
func currentChangedFiles(dataDir, workspaceID string) []string {
	raw, err := WorkspaceWorkingChanges(dataDir, workspaceID)
	if err != nil {
		return nil
	}
	var cs struct {
		ChangedFiles []struct {
			Path string `json:"path"`
		} `json:"changed_files"`
	}
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil
	}
	out := make([]string, 0, len(cs.ChangedFiles))
	for _, f := range cs.ChangedFiles {
		if f.Path != "" {
			out = append(out, f.Path)
		}
	}
	return out
}

func summarizeFailure(exp *FailureExplanation) string {
	if !exp.Failed {
		return fmt.Sprintf("Run %s does not look failed (status %q).", exp.RunID, exp.Status)
	}
	parts := []string{}
	if exp.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *exp.ExitCode))
	}
	if len(exp.Signals) > 0 {
		parts = append(parts, exp.Signals[0])
	}
	head := strings.Join(parts, "; ")
	if head == "" {
		head = "failed"
	}
	if len(exp.ImplicatedPaths) > 0 {
		return fmt.Sprintf("Failed (%s). Likely in changed file(s): %s.", head, strings.Join(exp.ImplicatedPaths, ", "))
	}
	if len(exp.ErrorLines) > 0 {
		return fmt.Sprintf("Failed (%s). First error: %s", head, exp.ErrorLines[0])
	}
	return fmt.Sprintf("Failed (%s).", head)
}
