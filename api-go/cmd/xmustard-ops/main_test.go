package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceLoadAndVerificationProfileOperatorLoop(t *testing.T) {
	tmpDir := t.TempDir()
	repoRoot := filepath.Join(tmpDir, "repo")
	dataDir := filepath.Join(tmpDir, "data")
	mustMkdirAll(t, filepath.Join(repoRoot, "docs", "bugs"))
	mustMkdirAll(t, filepath.Join(repoRoot, "backend", "tests"))
	mustWriteFile(t, filepath.Join(repoRoot, "docs", "bugs", "Bugs_25260323.md"), []byte(""+
		"# Bugs_25260323\n\n"+
		"## P0\n\n"+
		"### P0_25M03_001. Example bug\n\n"+
		"- Summary: Example summary\n"+
		"- Impact: Example impact\n"+
		"- Evidence:\n"+
		"  - `backend/tests/test_smoke.py:1`\n"+
		"- Status (2026-03-30): open in the current branch worktree.\n"))
	mustWriteFile(t, filepath.Join(repoRoot, "backend", "tests", "test_smoke.py"), []byte("def test_smoke():\n    assert True\n"))
	mustWriteFile(t, filepath.Join(repoRoot, "Makefile"), []byte("test:\n\tpython3 -m pytest -q backend/tests/test_smoke.py\n"))
	mustWriteFile(t, filepath.Join(repoRoot, "package.json"), []byte("{\"name\":\"fixture\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"}}\n"))
	mustWriteFile(t, filepath.Join(repoRoot, "pyproject.toml"), []byte("[project]\nname = \"fixture\"\nversion = \"0.1.0\"\n"))

	load := runOpsJSON(t, dataDir,
		"workspace", "load",
		"--root-path", repoRoot,
		"--auto-scan",
		"--prefer-cached-snapshot",
	)
	workspaceID := readJSONPathString(t, load, "workspace", "workspace_id")
	if workspaceID == "" {
		t.Fatalf("missing workspace id from load payload: %#v", load)
	}

	workspaces := runOpsJSON(t, dataDir, "workspace", "list")
	if len(asJSONArray(t, workspaces)) != 1 {
		t.Fatalf("expected one workspace after load, got %#v", workspaces)
	}

	repoState := runOpsJSON(t, dataDir, "workspace", "repo-state", workspaceID)
	if readJSONPathString(t, repoState, "workspace", "workspace_id") != workspaceID {
		t.Fatalf("repo-state did not return the loaded workspace: %#v", repoState)
	}

	projectInfo := runOpsJSON(t, dataDir, "workspace", "project-info", workspaceID)
	if readJSONPathString(t, projectInfo, "workspace_id") != workspaceID {
		t.Fatalf("project-info did not return the loaded workspace: %#v", projectInfo)
	}
	if len(asJSONArrayAtPath(t, projectInfo, "static_truth", "service_identities")) == 0 {
		t.Fatalf("expected project-info service identities, got %#v", projectInfo)
	}

	runTargets := runOpsJSON(t, dataDir, "workspace", "run-targets", workspaceID)
	if len(asJSONArray(t, runTargets)) == 0 {
		t.Fatalf("expected run targets, got %#v", runTargets)
	}
	if readTargetOwnershipStatus(t, runTargets, "npm run dev") != "exact" {
		t.Fatalf("expected raw run-target ownership in CLI payload, got %#v", runTargets)
	}
	if readTargetTruthSource(t, runTargets, "npm run dev") != "snapshot_scan" {
		t.Fatalf("expected snapshot scan truth on cached run target, got %#v", runTargets)
	}

	verifyTargets := runOpsJSON(t, dataDir, "workspace", "verify-targets", workspaceID)
	if len(asJSONArray(t, verifyTargets)) == 0 {
		t.Fatalf("expected verify targets, got %#v", verifyTargets)
	}
	if readTargetOwnershipStatus(t, verifyTargets, "npm run test") != "exact" {
		t.Fatalf("expected raw verify-target ownership in CLI payload, got %#v", verifyTargets)
	}
	if readTargetTruthSource(t, verifyTargets, "npm run test") != "snapshot_scan" {
		t.Fatalf("expected snapshot scan truth on cached verify target, got %#v", verifyTargets)
	}

	verificationOutcomes := runOpsJSON(t, dataDir, "workspace", "verification-outcomes", workspaceID)
	if readJSONPathString(t, verificationOutcomes, "workspace_id") != workspaceID {
		t.Fatalf("verification-outcomes did not return the loaded workspace: %#v", verificationOutcomes)
	}

	issueContext := runOpsJSON(t, dataDir, "workspace", "issue-context", workspaceID, "--issue-id", "P0_25M03_001")
	if readJSONPathString(t, issueContext, "issue", "bug_id") != "P0_25M03_001" {
		t.Fatalf("issue-context did not resolve the requested issue: %#v", issueContext)
	}

	savedProfile := runOpsJSON(t, dataDir,
		"workspace", "verification-profile-save", workspaceID,
		"--profile-id", "backend-pytest",
		"--name", "Backend pytest",
		"--description", "Fixture smoke",
		"--test-command", "python3 -m pytest -q backend/tests/test_smoke.py",
		"--source-path", "backend/tests/test_smoke.py",
		"--checklist-item", "smoke passes",
	)
	if readJSONPathString(t, savedProfile, "profile_id") != "backend-pytest" {
		t.Fatalf("verification-profile-save did not persist the requested profile: %#v", savedProfile)
	}

	profiles := runOpsJSON(t, dataDir, "workspace", "verification-profiles", workspaceID)
	if len(asJSONArray(t, profiles)) < 2 {
		t.Fatalf("expected built-in and saved verification profiles, got %#v", profiles)
	}
	verifyTargetsAfterProfileSave := runOpsJSON(t, dataDir, "workspace", "verify-targets", workspaceID)
	if readTargetTruthSource(t, verifyTargetsAfterProfileSave, "python3 -m pytest -q backend/tests/test_smoke.py") != "verification_profile_overlay" {
		t.Fatalf("expected live overlay truth after profile save, got %#v", verifyTargetsAfterProfileSave)
	}

	profileRun := runOpsJSON(t, dataDir,
		"workspace", "verification-profile-run", workspaceID,
		"--issue-id", "P0_25M03_001",
		"--profile-id", "backend-pytest",
		"--run-id", "manual-smoke-1",
	)
	if !readJSONPathBool(t, profileRun, "success") {
		t.Fatalf("verification-profile-run did not succeed: %#v", profileRun)
	}

	verificationOutcomesAfter := runOpsJSON(t, dataDir, "workspace", "verification-outcomes", workspaceID)
	items := asJSONArrayAtPath(t, verificationOutcomesAfter, "items")
	foundObservedProfile := false
	for _, raw := range items {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("verification outcome item is not an object: %#v", raw)
		}
		target, ok := record["target"].(map[string]any)
		if !ok {
			t.Fatalf("verification outcome target is not an object: %#v", record)
		}
		if target["profile_id"] != "backend-pytest" {
			continue
		}
		observed, ok := record["observed"].(map[string]any)
		if !ok {
			t.Fatalf("verification outcome observed block is not an object: %#v", record)
		}
		if observed["state"] != "success" {
			t.Fatalf("expected successful observed verification outcome, got %#v", observed)
		}
		foundObservedProfile = true
	}
	if !foundObservedProfile {
		t.Fatalf("missing observed verification outcome for saved profile: %#v", verificationOutcomesAfter)
	}
}

func runOpsJSON(t *testing.T, dataDir string, args ...string) any {
	t.Helper()
	apiGoDir := filepath.Join("..", "..")
	commandArgs := append([]string{"run", "./cmd/xmustard-ops"}, args...)
	commandArgs = append(commandArgs, "--data-dir", dataDir)
	cmd := exec.Command("go", commandArgs...)
	cmd.Dir = apiGoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run ./cmd/xmustard-ops %v failed: %v\n%s", args, err, string(output))
	}
	var payload any
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatalf("decode xmustard-ops JSON for %v: %v\n%s", args, err, string(output))
	}
	return payload
}

func asJSONArray(t *testing.T, payload any) []any {
	t.Helper()
	items, ok := payload.([]any)
	if !ok {
		t.Fatalf("expected JSON array, got %#v", payload)
	}
	return items
}

func asJSONArrayAtPath(t *testing.T, payload any, path ...string) []any {
	t.Helper()
	value := readJSONPathValue(t, payload, path...)
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("expected JSON array at %v, got %#v", path, value)
	}
	return items
}

func readTargetOwnershipStatus(t *testing.T, payload any, command string) string {
	t.Helper()
	for _, raw := range asJSONArray(t, payload) {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected target record object, got %#v", raw)
		}
		value, _ := record["command"].(string)
		if value != command {
			continue
		}
		ownership, ok := record["ownership"].(map[string]any)
		if !ok {
			t.Fatalf("expected ownership object on raw target %#v", record)
		}
		status, ok := ownership["status"].(string)
		if !ok {
			t.Fatalf("expected ownership status on raw target %#v", record)
		}
		return status
	}
	t.Fatalf("missing raw target %q in %#v", command, payload)
	return ""
}

func readTargetTruthSource(t *testing.T, payload any, command string) string {
	t.Helper()
	for _, raw := range asJSONArray(t, payload) {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("expected target record object, got %#v", raw)
		}
		value, _ := record["command"].(string)
		if value != command {
			continue
		}
		truthSource, ok := record["truth_source"].(string)
		if !ok {
			t.Fatalf("expected truth_source on raw target %#v", record)
		}
		return truthSource
	}
	t.Fatalf("missing raw target %q in %#v", command, payload)
	return ""
}

func readJSONPathString(t *testing.T, payload any, path ...string) string {
	t.Helper()
	value := readJSONPathValue(t, payload, path...)
	text, ok := value.(string)
	if !ok {
		t.Fatalf("expected string at %v, got %#v", path, value)
	}
	return text
}

func readJSONPathBool(t *testing.T, payload any, path ...string) bool {
	t.Helper()
	value := readJSONPathValue(t, payload, path...)
	enabled, ok := value.(bool)
	if !ok {
		t.Fatalf("expected bool at %v, got %#v", path, value)
	}
	return enabled
}

func readJSONPathValue(t *testing.T, payload any, path ...string) any {
	t.Helper()
	current := payload
	for _, key := range path {
		record, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("expected object before key %q in path %v, got %#v", key, path, current)
		}
		value, ok := record[key]
		if !ok {
			t.Fatalf("missing key %q in path %v from payload %#v", key, path, record)
		}
		current = value
	}
	return current
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
