package workspaceops

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// rustCoreBin locates the release xmustard-core binary that owns the Rust goal
// runtime. Tests skip (rather than fail) when it has not been built, so the Go
// suite stays green in environments without a Rust toolchain.
func rustCoreBin(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("XMUSTARD_CORE_BIN"); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override
		}
	}
	// Package dir is api-go/internal/workspaceops; repo root is three up.
	candidate := filepath.Join("..", "..", "..", "rust-core", "target", "release", "xmustard-core")
	if _, err := os.Stat(candidate); err != nil {
		t.Skipf("rust core binary not built (%s); run: cd rust-core && cargo build --release", candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("resolve rust binary: %v", err)
	}
	return abs
}

// TestGoalRustWireParity proves the Rust goal CLI and the Go goal shell read and
// write the same durable contract: a goal created by one runtime is faithfully
// readable by the other. This is the safety check behind moving the goal logic
// authority from Go to the Rust core.
func TestGoalRustWireParity(t *testing.T) {
	bin := rustCoreBin(t)

	t.Run("rust_writes_go_reads", func(t *testing.T) {
		dataDir, workspaceID, _, _ := writeIssueContextFixture(t, true)

		reqPath := filepath.Join(t.TempDir(), "create.json")
		req := GoalCreateRequest{
			Title:                "Parity goal from Rust",
			Objective:            "created by the Rust CLI and read back by the Go shell",
			AcceptanceCriteria:   []string{"Go can read the Rust-written record"},
			VerificationCommands: []string{"cargo test"},
			RuntimePreference:    "opencode",
		}
		writeJSONFile(t, reqPath, req)

		runRustGoal(t, bin, "create", dataDir, workspaceID, reqPath)

		goals, err := ListGoals(dataDir, workspaceID)
		if err != nil {
			t.Fatalf("go ListGoals over rust-written store: %v", err)
		}
		if len(goals) != 1 {
			t.Fatalf("expected 1 goal, got %d", len(goals))
		}
		got := goals[0]
		if got.Title != req.Title || got.Objective != req.Objective {
			t.Fatalf("field mismatch: %#v", got)
		}
		if got.Status != GoalStatusDraft {
			t.Fatalf("expected draft status, got %q", got.Status)
		}
		if len(got.AcceptanceCriteria) != 1 || got.AcceptanceCriteria[0] != req.AcceptanceCriteria[0] {
			t.Fatalf("acceptance criteria not preserved across runtimes: %#v", got.AcceptanceCriteria)
		}
	})

	t.Run("go_writes_rust_reads", func(t *testing.T) {
		dataDir, workspaceID, _, _ := writeIssueContextFixture(t, true)

		created, err := CreateGoal(dataDir, workspaceID, GoalCreateRequest{
			Title:                "Parity goal from Go",
			Objective:            "created by the Go shell and read back by the Rust CLI",
			AllowedSurface:       []string{"rust-core/src/goalruntime.rs"},
			VerificationCommands: []string{"cargo test"},
		})
		if err != nil {
			t.Fatalf("go CreateGoal: %v", err)
		}

		out := runRustGoal(t, bin, "get", dataDir, workspaceID, created.GoalID)

		// The strongest parity proof: the Rust output deserializes straight into
		// the Go GoalRecord struct.
		var roundTripped GoalRecord
		if err := json.Unmarshal(out, &roundTripped); err != nil {
			t.Fatalf("rust `goal get` output does not fit the Go GoalRecord struct: %v\n%s", err, out)
		}
		if roundTripped.GoalID != created.GoalID {
			t.Fatalf("goal_id mismatch: rust=%q go=%q", roundTripped.GoalID, created.GoalID)
		}
		if roundTripped.Title != created.Title || roundTripped.Objective != created.Objective {
			t.Fatalf("field mismatch across runtimes: %#v", roundTripped)
		}
		if roundTripped.Status != created.Status {
			t.Fatalf("status mismatch: rust=%q go=%q", roundTripped.Status, created.Status)
		}
		if len(roundTripped.AllowedSurface) != 1 || roundTripped.AllowedSurface[0] != "rust-core/src/goalruntime.rs" {
			t.Fatalf("allowed surface not preserved: %#v", roundTripped.AllowedSurface)
		}
	})
}

func runRustGoal(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	full := append([]string{"goal"}, args...)
	cmd := exec.Command(bin, full...)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("rust goal %v failed: %v\nstderr: %s", args, err, stderr)
	}
	return out
}

func writeJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write request file: %v", err)
	}
}
