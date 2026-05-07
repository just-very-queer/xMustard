package workspaceops

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

type VerificationOutcomeRegistry struct {
	WorkspaceID string                      `json:"workspace_id"`
	RootPath    string                      `json:"root_path"`
	Items       []VerificationOutcomeRecord `json:"items"`
	Warnings    []string                    `json:"warnings"`
	GeneratedAt string                      `json:"generated_at"`
}

type VerificationOutcomeRecord struct {
	Target   RepoTargetRecord            `json:"target"`
	Observed ObservedVerificationOutcome `json:"observed"`
}

type ObservedVerificationOutcome struct {
	ObservationKind     string                           `json:"observation_kind"`
	MatchBasis          string                           `json:"match_basis"`
	State               string                           `json:"state"`
	LastStatus          string                           `json:"last_status"`
	LastRunAt           *string                          `json:"last_run_at,omitempty"`
	CommandExecuted     *string                          `json:"command_executed,omitempty"`
	Cwd                 *string                          `json:"cwd,omitempty"`
	ExitCode            *int                             `json:"exit_code,omitempty"`
	TimedOut            *bool                            `json:"timed_out,omitempty"`
	RunID               *string                          `json:"run_id,omitempty"`
	VerificationID      *string                          `json:"verification_id,omitempty"`
	IssueID             *string                          `json:"issue_id,omitempty"`
	ExecutionID         *string                          `json:"execution_id,omitempty"`
	CoverageResultID    *string                          `json:"coverage_result_id,omitempty"`
	CoverageReportPath  *string                          `json:"coverage_report_path,omitempty"`
	Branch              *string                          `json:"branch,omitempty"`
	HeadSHA             *string                          `json:"head_sha,omitempty"`
	ObservedTruthSource string                           `json:"observed_truth_source"`
	EvidenceRefs        []VerificationOutcomeEvidenceRef `json:"evidence_refs"`
	Reason              *string                          `json:"reason,omitempty"`
}

type VerificationOutcomeEvidenceRef struct {
	Kind       string  `json:"kind"`
	Path       *string `json:"path,omitempty"`
	ArtifactID *string `json:"artifact_id,omitempty"`
	CreatedAt  *string `json:"created_at,omitempty"`
	Summary    *string `json:"summary,omitempty"`
}

func ReadVerificationOutcomes(dataDir string, workspaceID string) (*VerificationOutcomeRegistry, error) {
	snapshot, err := loadSnapshot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	history, err := loadVerificationProfileHistory(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verifications, err := ListVerifications(dataDir, workspaceID, "")
	if err != nil {
		return nil, err
	}

	historyByProfile := map[string][]rustcore.VerificationProfileResult{}
	for _, item := range history {
		historyByProfile[item.ProfileID] = append(historyByProfile[item.ProfileID], item)
	}
	verificationByRunIssue := map[string]verificationRecord{}
	for _, item := range verifications {
		key := verificationJoinKey(item.RunID, item.IssueID)
		if key == "" {
			continue
		}
		if _, ok := verificationByRunIssue[key]; !ok {
			verificationByRunIssue[key] = item
		}
	}

	declaredProfiles := map[string]struct{}{}
	items := make([]VerificationOutcomeRecord, 0, len(verifyTargets))
	for _, target := range verifyTargets {
		if target.ProfileID != nil && strings.TrimSpace(*target.ProfileID) != "" {
			declaredProfiles[strings.TrimSpace(*target.ProfileID)] = struct{}{}
		}
		record := VerificationOutcomeRecord{
			Target:   target,
			Observed: newUnobservedVerificationOutcome(target),
		}
		if target.ProfileID != nil {
			profileID := strings.TrimSpace(*target.ProfileID)
			if entries := historyByProfile[profileID]; len(entries) > 0 {
				record.Observed = buildObservedVerificationOutcome(
					dataDir,
					snapshot.Workspace.RootPath,
					target,
					entries[0],
					verificationByRunIssue,
					workspaceID,
				)
			}
		}
		items = append(items, record)
	}

	warnings := []string{}
	for profileID := range historyByProfile {
		if _, ok := declaredProfiles[profileID]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("Observed verification history exists for undeclared profile_id %q.", profileID))
	}
	slices.Sort(warnings)

	return &VerificationOutcomeRegistry{
		WorkspaceID: workspaceID,
		RootPath:    snapshot.Workspace.RootPath,
		Items:       items,
		Warnings:    warnings,
		GeneratedAt: nowUTC(),
	}, nil
}

func newUnobservedVerificationOutcome(target RepoTargetRecord) ObservedVerificationOutcome {
	reason := "No durable observed verification execution is linked to this declared target yet."
	if target.ProfileID == nil {
		reason = "This declared target has no profile_id-backed observed execution link yet, so execution truth remains unknown."
	}
	return ObservedVerificationOutcome{
		ObservationKind:     "none",
		MatchBasis:          "none",
		State:               "unknown",
		LastStatus:          "never_observed",
		ObservedTruthSource: "none",
		EvidenceRefs:        []VerificationOutcomeEvidenceRef{},
		Reason:              &reason,
	}
}

func buildObservedVerificationOutcome(
	dataDir string,
	workspaceRoot string,
	target RepoTargetRecord,
	result rustcore.VerificationProfileResult,
	verificationByRunIssue map[string]verificationRecord,
	workspaceID string,
) ObservedVerificationOutcome {
	outcome := ObservedVerificationOutcome{
		ObservationKind:     "verification_profile_execution",
		MatchBasis:          "profile_id_exact",
		State:               map[bool]string{true: "success", false: "failure"}[result.Success],
		LastStatus:          map[bool]string{true: "passed", false: "failed"}[result.Success],
		LastRunAt:           optionalString(result.CreatedAt),
		RunID:               result.RunID,
		IssueID:             result.IssueID,
		ExecutionID:         optionalString(result.ExecutionID),
		ObservedTruthSource: filepath.ToSlash(filepath.Join("workspaces", workspaceID, "verification_profile_history.json")),
		EvidenceRefs:        []VerificationOutcomeEvidenceRef{},
	}

	if latest := lastVerificationAttempt(result); latest != nil {
		outcome.CommandExecuted = optionalString(latest.Command)
		outcome.Cwd = optionalString(latest.Cwd)
		outcome.ExitCode = latest.ExitCode
		timedOut := latest.TimedOut
		outcome.TimedOut = &timedOut
	}
	if result.CoverageResult != nil {
		outcome.CoverageResultID = optionalString(result.CoverageResult.ResultID)
	}
	if result.CoverageReportPath != nil && strings.TrimSpace(*result.CoverageReportPath) != "" {
		reportPath := normalizeWorkspaceArtifactPath(workspaceRoot, *result.CoverageReportPath)
		outcome.CoverageReportPath = &reportPath
	}

	outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
		Kind:       "verification_profile_history",
		Path:       optionalString(filepath.ToSlash(filepath.Join("workspaces", workspaceID, "verification_profile_history.json"))),
		ArtifactID: optionalString(result.ExecutionID),
		CreatedAt:  optionalString(result.CreatedAt),
		Summary:    optionalString(target.Label),
	})
	if result.CoverageResult != nil {
		coveragePath := filepath.ToSlash(filepath.Join("coverage", result.CoverageResult.ResultID+".json"))
		summary := fmt.Sprintf("Coverage result (%s).", result.CoverageResult.Format)
		outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
			Kind:       "coverage_result",
			Path:       &coveragePath,
			ArtifactID: optionalString(result.CoverageResult.ResultID),
			CreatedAt:  optionalString(result.CoverageResult.CreatedAt),
			Summary:    &summary,
		})
	}
	if outcome.CoverageReportPath != nil {
		outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
			Kind:    "coverage_report",
			Path:    outcome.CoverageReportPath,
			Summary: optionalString("Raw coverage report captured by the verification profile."),
		})
	}

	if result.RunID != nil && strings.TrimSpace(*result.RunID) != "" {
		runID := strings.TrimSpace(*result.RunID)
		if run, err := loadRun(dataDir, workspaceID, runID); err == nil {
			if run.Worktree != nil {
				outcome.Branch = run.Worktree.Branch
				outcome.HeadSHA = run.Worktree.HeadSHA
			}
			runPath := filepath.ToSlash(filepath.Join("workspaces", workspaceID, "runs", runID+".json"))
			outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
				Kind:       "run_record",
				Path:       &runPath,
				ArtifactID: optionalString(runID),
				CreatedAt:  optionalString(run.CreatedAt),
				Summary:    optionalString(run.CommandPreview),
			})
			if strings.TrimSpace(run.LogPath) != "" {
				logPath := normalizeWorkspaceArtifactPath(workspaceRoot, run.LogPath)
				outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
					Kind:    "run_log",
					Path:    &logPath,
					Summary: optionalString("Run log for the linked verification execution."),
				})
			}
		}
	}

	if result.RunID != nil && result.IssueID != nil {
		if verification, ok := verificationByRunIssue[verificationJoinKey(*result.RunID, *result.IssueID)]; ok {
			outcome.VerificationID = optionalString(verification.VerificationID)
			verificationPath := filepath.ToSlash(filepath.Join("workspaces", workspaceID, "verifications.json"))
			outcome.EvidenceRefs = append(outcome.EvidenceRefs, VerificationOutcomeEvidenceRef{
				Kind:       "verification_record",
				Path:       &verificationPath,
				ArtifactID: optionalString(verification.VerificationID),
				CreatedAt:  optionalString(verification.CreatedAt),
				Summary:    optionalString(verification.Summary),
			})
		}
	}

	return outcome
}

func lastVerificationAttempt(result rustcore.VerificationProfileResult) *rustcore.VerificationCommandResult {
	if len(result.Attempts) == 0 {
		return nil
	}
	return &result.Attempts[len(result.Attempts)-1]
}

func verificationJoinKey(runID string, issueID string) string {
	runID = strings.TrimSpace(runID)
	issueID = strings.TrimSpace(issueID)
	if runID == "" || issueID == "" {
		return ""
	}
	return runID + "\x00" + issueID
}

func normalizeWorkspaceArtifactPath(workspaceRoot string, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.ToSlash(relativeToWorkspace(workspaceRoot, trimmed))
	}
	return filepath.ToSlash(trimmed)
}
