package workspaceops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/rustcore"
)

// Goal logic lives in the Rust core (rust-core/src/goalruntime.rs, the
// `xmustard-core goal` CLI). This file is the Go delivery shim: it owns request
// shaping and workspace validation, then delegates create/list/get/iterate/
// status/ledger/context to the single Rust implementation over the shared
// goals.json / goal_iterations / ledger contract. The types below are the wire
// contract; they intentionally match the Rust serde structs.

const (
	GoalStatusDraft    = "draft"
	GoalStatusActive   = "active"
	GoalStatusBlocked  = "blocked"
	GoalStatusComplete = "complete"
	GoalStatusArchived = "archived"
)

type GoalEvidence struct {
	Kind      string `json:"kind"`
	Label     string `json:"label,omitempty"`
	Command   string `json:"command,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	Notes     string `json:"notes,omitempty"`
	CreatedAt string `json:"created_at"`
}

type GoalIterationRecord struct {
	IterationID  string         `json:"iteration_id"`
	GoalID       string         `json:"goal_id"`
	Role         string         `json:"role,omitempty"`
	Summary      string         `json:"summary"`
	Outcome      string         `json:"outcome,omitempty"`
	Runtime      string         `json:"runtime,omitempty"`
	Model        string         `json:"model,omitempty"`
	FilesTouched []string       `json:"files_touched,omitempty"`
	Evidence     []GoalEvidence `json:"evidence,omitempty"`
	CreatedAt    string         `json:"created_at"`
}

type GoalRecord struct {
	GoalID                 string         `json:"goal_id"`
	WorkspaceID            string         `json:"workspace_id"`
	Title                  string         `json:"title"`
	Objective              string         `json:"objective"`
	Status                 string         `json:"status"`
	AcceptanceCriteria     []string       `json:"acceptance_criteria,omitempty"`
	CurrentTranche         string         `json:"current_tranche,omitempty"`
	AllowedSurface         []string       `json:"allowed_surface,omitempty"`
	VerificationCommands   []string       `json:"verification_commands,omitempty"`
	VerificationProfileIDs []string       `json:"verification_profile_ids,omitempty"`
	RuntimePreference      string         `json:"runtime_preference,omitempty"`
	PreferredModel         string         `json:"preferred_model,omitempty"`
	ResumptionNotes        string         `json:"resumption_notes,omitempty"`
	Evidence               []GoalEvidence `json:"evidence,omitempty"`
	CreatedAt              string         `json:"created_at"`
	UpdatedAt              string         `json:"updated_at"`
	CompletedAt            *string        `json:"completed_at,omitempty"`
}

type GoalCreateRequest struct {
	Title                  string   `json:"title"`
	Objective              string   `json:"objective"`
	AcceptanceCriteria     []string `json:"acceptance_criteria,omitempty"`
	CurrentTranche         string   `json:"current_tranche,omitempty"`
	AllowedSurface         []string `json:"allowed_surface,omitempty"`
	VerificationCommands   []string `json:"verification_commands,omitempty"`
	VerificationProfileIDs []string `json:"verification_profile_ids,omitempty"`
	RuntimePreference      string   `json:"runtime_preference,omitempty"`
	PreferredModel         string   `json:"preferred_model,omitempty"`
	ResumptionNotes        string   `json:"resumption_notes,omitempty"`
}

type GoalStatusUpdateRequest struct {
	Status                    string `json:"status"`
	VerificationSkippedReason string `json:"verification_skipped_reason,omitempty"`
}

type GoalIterationAppendRequest struct {
	Role         string         `json:"role,omitempty"`
	Summary      string         `json:"summary"`
	Outcome      string         `json:"outcome,omitempty"`
	Runtime      string         `json:"runtime,omitempty"`
	Model        string         `json:"model,omitempty"`
	FilesTouched []string       `json:"files_touched,omitempty"`
	Evidence     []GoalEvidence `json:"evidence,omitempty"`
}

func ListGoals(dataDir string, workspaceID string) ([]GoalRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunGoalCommand("list", root, workspaceID)
	if err != nil {
		return nil, mapGoalError(err)
	}
	var goals []GoalRecord
	if err := json.Unmarshal(out, &goals); err != nil {
		return nil, fmt.Errorf("decode goals: %w", err)
	}
	return goals, nil
}

func CreateGoal(dataDir string, workspaceID string, request GoalCreateRequest) (*GoalRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return nil, err
	}
	reqPath, cleanup, err := writeGoalRequest(request)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out, err := rustcore.RunGoalCommand("create", root, workspaceID, reqPath)
	if err != nil {
		return nil, mapGoalError(err)
	}
	var wrapped struct {
		Goal GoalRecord `json:"goal"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, fmt.Errorf("decode created goal: %w", err)
	}
	return &wrapped.Goal, nil
}

func GetGoal(dataDir string, workspaceID string, goalID string) (*GoalRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return nil, err
	}
	out, err := rustcore.RunGoalCommand("get", root, workspaceID, goalID)
	if err != nil {
		return nil, mapGoalError(err)
	}
	var goal GoalRecord
	if err := json.Unmarshal(out, &goal); err != nil {
		return nil, fmt.Errorf("decode goal: %w", err)
	}
	return &goal, nil
}

func UpdateGoalStatus(dataDir string, workspaceID string, goalID string, request GoalStatusUpdateRequest) (*GoalRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return nil, err
	}
	args := []string{"status", root, workspaceID, goalID, strings.TrimSpace(request.Status)}
	if reason := strings.TrimSpace(request.VerificationSkippedReason); reason != "" {
		args = append(args, reason)
	}
	out, err := rustcore.RunGoalCommand(args...)
	if err != nil {
		return nil, mapGoalError(err)
	}
	var goal GoalRecord
	if err := json.Unmarshal(out, &goal); err != nil {
		return nil, fmt.Errorf("decode updated goal: %w", err)
	}
	return &goal, nil
}

func AppendGoalIteration(dataDir string, workspaceID string, goalID string, request GoalIterationAppendRequest) (*GoalIterationRecord, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return nil, err
	}
	reqPath, cleanup, err := writeGoalRequest(request)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out, err := rustcore.RunGoalCommand("iterate", root, workspaceID, goalID, reqPath)
	if err != nil {
		return nil, mapGoalError(err)
	}
	var wrapped struct {
		Iteration GoalIterationRecord `json:"iteration"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		return nil, fmt.Errorf("decode iteration: %w", err)
	}
	return &wrapped.Iteration, nil
}

func ReadGoalLedger(dataDir string, workspaceID string, goalID string) (string, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return "", err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return "", err
	}
	out, err := rustcore.RunGoalCommand("ledger", root, workspaceID, goalID)
	if err != nil {
		return "", mapGoalError(err)
	}
	return string(out), nil
}

func BuildGoalContextPacket(dataDir string, workspaceID string, goalID string) (string, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return "", err
	}
	root, err := goalDataRoot(dataDir)
	if err != nil {
		return "", err
	}
	out, err := rustcore.RunGoalCommand("context", root, workspaceID, goalID)
	if err != nil {
		return "", mapGoalError(err)
	}
	return string(out), nil
}

// goalDataRoot returns an absolute data dir. The Rust binary runs with its own
// working directory, so a relative dataDir must be resolved before handing off.
func goalDataRoot(dataDir string) (string, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir %q: %w", dataDir, err)
	}
	return abs, nil
}

// writeGoalRequest serializes a create/iterate request to a temp file for the
// Rust CLI to read, and returns a cleanup func.
func writeGoalRequest(payload any) (string, func(), error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", func() {}, fmt.Errorf("encode goal request: %w", err)
	}
	file, err := os.CreateTemp("", "xmustard-goal-req-*.json")
	if err != nil {
		return "", func() {}, fmt.Errorf("create goal request file: %w", err)
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write goal request file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close goal request file: %w", err)
	}
	return file.Name(), cleanup, nil
}

// mapGoalError translates the Rust "not found" signal into os.ErrNotExist so the
// HTTP handlers keep returning 404 for missing goals.
func mapGoalError(err error) error {
	if errors.Is(err, rustcore.ErrGoalNotFound) {
		return os.ErrNotExist
	}
	return err
}
