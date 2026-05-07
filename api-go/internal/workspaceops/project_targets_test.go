package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRunTargetsAndVerifyTargetsUseManifestDiscoveryAndSavedProfiles(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("backend:\n\tpython3 -m uvicorn app.main:app\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docker-compose.yml"), []byte("services:\n  api:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{
		{
			ProfileID:         "backend-pytest",
			WorkspaceID:       workspaceID,
			Name:              "Backend pytest",
			Description:       "Saved verification command",
			TestCommand:       "pytest -q",
			CoverageFormat:    "unknown",
			MaxRuntimeSeconds: 60,
			RetryCount:        1,
			BuiltIn:           false,
			CreatedAt:         nowUTC(),
			UpdatedAt:         nowUTC(),
		},
	}); err != nil {
		t.Fatalf("save verification profiles: %v", err)
	}

	runTargets, err := ReadRunTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read run targets: %v", err)
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read verify targets: %v", err)
	}

	if !hasTargetCommand(runTargets, "npm run dev") {
		t.Fatalf("expected npm run dev in run targets: %#v", runTargets)
	}
	if !hasTargetCommand(runTargets, "make backend") {
		t.Fatalf("expected make backend in run targets: %#v", runTargets)
	}
	if !hasTargetCommand(runTargets, "docker compose -f docker-compose.yml up") {
		t.Fatalf("expected docker compose target in run targets: %#v", runTargets)
	}
	if !hasTargetCommand(verifyTargets, "npm run test") {
		t.Fatalf("expected npm run test in verify targets: %#v", verifyTargets)
	}
	if !hasTargetCommand(verifyTargets, "pytest -q") {
		t.Fatalf("expected saved verification profile target in verify targets: %#v", verifyTargets)
	}
	if !hasTargetSourcePath(verifyTargets, "verification_profiles.json") {
		t.Fatalf("expected verification_profiles.json provenance: %#v", verifyTargets)
	}
}

func TestReadRunTargetsAndVerifyTargetsDiscoverPyprojectAndCargoEntrypoints(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "pyproject.toml"), []byte("[project]\nname = \"fixture-backend\"\n[project.scripts]\nxmustard = \"app.cli:app\"\n"), 0o644); err != nil {
		t.Fatalf("write backend pyproject.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "app", "cli.py"), []byte("def app():\n    return True\n\nif __name__ == \"__main__\":\n    app()\n"), 0o644); err != nil {
		t.Fatalf("rewrite backend cli.py: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "rust-core", "src", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir rust-core/src/bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "rust-core", "Cargo.toml"), []byte("[package]\nname = \"fixture-core\"\nversion = \"0.1.0\"\nedition = \"2024\"\n"), 0o644); err != nil {
		t.Fatalf("write rust-core Cargo.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "rust-core", "src", "bin", "fixture-core.rs"), []byte("fn main() {}\n"), 0o644); err != nil {
		t.Fatalf("write rust binary: %v", err)
	}

	runTargets, err := ReadRunTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read run targets: %v", err)
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read verify targets: %v", err)
	}

	pythonTarget := findTargetByCommand(runTargets, "cd backend && python3 -m app.cli")
	if pythonTarget == nil {
		t.Fatalf("expected pyproject python entrypoint in run targets: %#v", runTargets)
	}
	if pythonTarget.Source != "pyproject_toml" || pythonTarget.WorkingDir != "backend" || pythonTarget.EntryPath == nil || *pythonTarget.EntryPath != "backend/app/cli.py" {
		t.Fatalf("unexpected pyproject target provenance: %#v", pythonTarget)
	}
	if pythonTarget.Reason == nil || !strings.Contains(*pythonTarget.Reason, "PEP 621 script") {
		t.Fatalf("expected pyproject reason, got %#v", pythonTarget)
	}

	rustRunTarget := findTargetByCommand(runTargets, "cd rust-core && cargo run --bin fixture-core")
	if rustRunTarget == nil {
		t.Fatalf("expected cargo run target in run targets: %#v", runTargets)
	}
	if rustRunTarget.Source != "cargo_toml" || rustRunTarget.WorkingDir != "rust-core" || rustRunTarget.EntryPath == nil || *rustRunTarget.EntryPath != "rust-core/src/bin/fixture-core.rs" {
		t.Fatalf("unexpected cargo run provenance: %#v", rustRunTarget)
	}

	rustVerifyTarget := findTargetByCommand(verifyTargets, "cd rust-core && cargo test")
	if rustVerifyTarget == nil {
		t.Fatalf("expected cargo test verify target in verify targets: %#v", verifyTargets)
	}
	if rustVerifyTarget.Source != "cargo_toml" || rustVerifyTarget.Reason == nil || !strings.Contains(*rustVerifyTarget.Reason, "cargo test") {
		t.Fatalf("unexpected cargo verify provenance: %#v", rustVerifyTarget)
	}
}

func TestSemanticDiscoverTargetsUsesSharedManifestDiscovery(t *testing.T) {
	_, _, repoRoot := writeSemanticIndexFixture(t)

	expected := discoverManifestTargets(repoRoot, true)
	actual := semanticDiscoverTargets(repoRoot, true)

	if len(expected) != len(actual) {
		t.Fatalf("expected semantic targets to mirror shared discovery: expected=%#v actual=%#v", expected, actual)
	}
	for _, item := range expected {
		if !hasSemanticTarget(actual, item.Command, item.SourcePath) {
			t.Fatalf("missing semantic target for shared discovery item %#v in %#v", item, actual)
		}
	}
}

func hasTargetCommand(targets []RepoTargetRecord, command string) bool {
	for _, item := range targets {
		if item.Command == command {
			return true
		}
	}
	return false
}

func hasTargetSourcePath(targets []RepoTargetRecord, sourcePath string) bool {
	for _, item := range targets {
		if item.SourcePath == sourcePath {
			return true
		}
	}
	return false
}

func hasSemanticTarget(targets []semanticRepoTarget, command string, sourcePath string) bool {
	for _, item := range targets {
		if item.Command == command && item.SourcePath == sourcePath {
			return true
		}
	}
	return false
}

func findTargetByCommand(targets []RepoTargetRecord, command string) *RepoTargetRecord {
	for idx := range targets {
		if targets[idx].Command == command {
			return &targets[idx]
		}
	}
	return nil
}
