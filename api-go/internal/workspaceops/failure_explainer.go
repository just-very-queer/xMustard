package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
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

	// Run-independent outcomes and the bounded read (WS-21; PAR-HAR-06, PAR-RT-11).
	// Source is run (a platform run), command, evidence, log or capture.
	Source         string             `json:"source,omitempty"`
	Command        string             `json:"command,omitempty"`
	Cwd            string             `json:"cwd,omitempty"`
	TimedOut       bool               `json:"timed_out,omitempty"`
	FailingTests   []string           `json:"failing_tests,omitempty"`
	EvidenceHandle string             `json:"evidence_handle,omitempty"`
	Output         *OutcomeOutput     `json:"output,omitempty"`
	Memories       []ImplicatedMemory `json:"memories,omitempty"`
	RecordedAt     string             `json:"recorded_at,omitempty"`
	HeadSHA        string             `json:"head_sha,omitempty"`
	// ResolvedBy names the later outcome of the same command that superseded this one.
	ResolvedBy string `json:"resolved_by,omitempty"`
	// Created is set by a recording call: false when this output was recorded before
	// and the first outcome is returned.
	Created *bool              `json:"created,omitempty"`
	Unknown []GroundingUnknown `json:"unknown,omitempty"`
}

// pathLikePattern matches repo-relative-ish paths (a/b/c.ext) that appear in logs.
var pathLikePattern = regexp.MustCompile(`[\w./-]+\.[A-Za-z]{1,5}`)

// errorLinePattern flags lines worth surfacing from a failure log.
var errorLinePattern = regexp.MustCompile(`(?i)\b(error|fail(ed|ure)?|panic|exception|undefined|cannot|expected|traceback|fatal)\b`)

// errorLineWords are the words errorLinePattern needs: a line holding none of them
// (in lower case) cannot match, so most lines of a MiB-long log skip the regexp, which
// costs about 35 times more per line.
var errorLineWords = []string{"error", "fail", "panic", "exception", "undefined", "cannot", "expected", "traceback", "fatal"}

func mayBeErrorLine(line string) bool {
	lower := strings.ToLower(line)
	return slices.ContainsFunc(errorLineWords, func(w string) bool { return strings.Contains(lower, w) })
}

func ExplainRunFailure(dataDir, workspaceID, runID string) (*FailureExplanation, error) {
	return ExplainRunFailureCtx(context.Background(), dataDir, workspaceID, runID)
}

// ExplainRunFailureCtx is the request-scoped variant: cancelling ctx cancels its Rust/tool work (see rustcore.runCoreCtx).
// runID names a platform run or a run-independent outcome (outcomes.go). It only reads:
// run_fail feedback is recorded once, when an outcome is recorded, never on a GET.
func ExplainRunFailureCtx(ctx context.Context, dataDir, workspaceID, runID string) (*FailureExplanation, error) {
	if IsRunOutcomeID(runID) {
		return ExplainRunOutcome(ctx, dataDir, workspaceID, runID)
	}
	run, err := ReadRun(dataDir, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	output, window := "", &OutcomeOutput{}
	if strings.TrimSpace(run.OutputPath) != "" {
		if content, total, readErr := readFileTail(run.OutputPath, outcomeTailBytes); readErr == nil {
			output = string(content)
			window = &OutcomeOutput{TotalBytes: total, AnalyzedBytes: int64(len(content)), Truncated: int64(len(content)) < total}
		}
	}

	exp := &FailureExplanation{
		RunID:       runID,
		Status:      run.Status,
		ExitCode:    run.ExitCode,
		Signals:     detectPatchIssues(run, output),
		GeneratedAt: nowUTC(),
		Source:      "run",
		Output:      window,
	}
	exp.Failed = runLooksFailed(run, exp.Signals)

	exp.ErrorLines = salientErrorLines(output, 8)
	changed, changedErr := workingChangedFiles(ctx, dataDir, workspaceID)
	if changedErr != nil {
		exp.Unknown = append(exp.Unknown, GroundingUnknown{Field: "changed_files", Reason: changedErr.Error()})
	}
	mentioned := mentionedPaths(output)
	exp.ChangedFiles = nonNil(changed)
	exp.ImplicatedPaths = implicatedBy(mentioned, contextRoot(dataDir, workspaceID), changed)
	exp.Memories, exp.Unknown = linkMemories(ctx, dataDir, workspaceID, exp.ImplicatedPaths, mentioned, exp.Unknown)
	exp.Summary = summarizeFailure(exp)
	return exp, nil
}

// readFileTail reads at most maxBytes from the end of a file, and its size, without
// loading the rest (PAR-RT-11: a run's output can be arbitrarily large).
func readFileTail(path string, maxBytes int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := info.Size()
	n := min(size, maxBytes)
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, size-n)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	return buf[:m], size, nil
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
		if line == "" || !mayBeErrorLine(line) || !errorLinePattern.MatchString(line) {
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

// currentChangedFiles lists the working-tree changed paths (best-effort).
func currentChangedFiles(ctx context.Context, dataDir, workspaceID string) []string {
	out, _ := workingChangedFiles(ctx, dataDir, workspaceID)
	return out
}

// workingChangedFiles lists the working-tree changed paths, or why they are unknown.
func workingChangedFiles(ctx context.Context, dataDir, workspaceID string) ([]string, error) {
	raw, err := WorkspaceWorkingChangesCtx(ctx, dataDir, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("working changes unavailable: %w", err)
	}
	var cs struct {
		ChangedFiles []struct {
			Path string `json:"path"`
		} `json:"changed_files"`
	}
	if err := json.Unmarshal(raw, &cs); err != nil {
		return nil, fmt.Errorf("working changes undecodable: %w", err)
	}
	out := make([]string, 0, len(cs.ChangedFiles))
	for _, f := range cs.ChangedFiles {
		if f.Path != "" {
			out = append(out, f.Path)
		}
	}
	return out, nil
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
