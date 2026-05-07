package workspaceops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadProjectInfoBuildsDeterministicStaticAndRuntimeTruth(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("backend:\n\tpython3 -m uvicorn app.main:app\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docker-compose.yml"), []byte("services:\n  api:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
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
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{{
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
	}}); err != nil {
		t.Fatalf("save verification profiles: %v", err)
	}

	projectInfo, err := ReadProjectInfo(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read project info: %v", err)
	}

	if !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "package.json") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "backend/pyproject.toml") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "rust-core/Cargo.toml") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "docker-compose.yml") {
		t.Fatalf("expected manifest inventory, got %#v", projectInfo.StaticTruth.Manifests)
	}
	if !projectInfoHasCommand(projectInfo.StaticTruth.RunTargets, "npm run dev") || !projectInfoHasCommand(projectInfo.StaticTruth.RunTargets, "cd backend && python3 -m app.cli") || !projectInfoHasCommand(projectInfo.StaticTruth.VerifyTargets, "pytest -q") {
		t.Fatalf("expected target inventory, got run=%#v verify=%#v", projectInfo.StaticTruth.RunTargets, projectInfo.StaticTruth.VerifyTargets)
	}
	if !projectInfoHasEntrypoint(projectInfo.StaticTruth.Entrypoints, "backend/app/cli.py") || !projectInfoHasEntrypoint(projectInfo.StaticTruth.Entrypoints, "rust-core/src/bin/fixture-core.rs") {
		t.Fatalf("expected entrypoints, got %#v", projectInfo.StaticTruth.Entrypoints)
	}
	if !projectInfoHasService(projectInfo.StaticTruth.Services, "api") {
		t.Fatalf("expected compose service, got %#v", projectInfo.StaticTruth.Services)
	}
	if !projectInfoHasDeclaredRuntime(projectInfo.StaticTruth.Runtimes, "python3") || !projectInfoHasDeclaredRuntime(projectInfo.StaticTruth.Runtimes, "cargo") || !projectInfoHasObservedRuntime(projectInfo.RuntimeTruth.Runtimes, "npm") {
		t.Fatalf("expected runtime inventory, got static=%#v runtime=%#v", projectInfo.StaticTruth.Runtimes, projectInfo.RuntimeTruth.Runtimes)
	}
	for _, item := range projectInfo.RuntimeTruth.Runtimes {
		if item.Verdict != projectInfoVerdictRuntimeObserved && item.Verdict != projectInfoVerdictUnavailable {
			t.Fatalf("expected runtime observation verdict, got %#v", item)
		}
	}

	snapshot, err := ScanWorkspace(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	if snapshot.ProjectInfo == nil || !projectInfoHasService(snapshot.ProjectInfo.StaticTruth.Services, "api") {
		t.Fatalf("expected project info to persist in snapshot, got %#v", snapshot.ProjectInfo)
	}
}

func projectInfoHasManifest(items []ProjectManifestRecord, path string) bool {
	for _, item := range items {
		if item.Path == path {
			return true
		}
	}
	return false
}

func projectInfoHasCommand(items []ProjectCommandRecord, command string) bool {
	for _, item := range items {
		if item.Command == command {
			return true
		}
	}
	return false
}

func projectInfoHasEntrypoint(items []ProjectEntrypointRecord, entryPath string) bool {
	for _, item := range items {
		if item.Provenance.EntryPath != nil && *item.Provenance.EntryPath == entryPath {
			return true
		}
	}
	return false
}

func projectInfoHasService(items []ProjectServiceRecord, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func projectInfoHasDeclaredRuntime(items []ProjectRuntimeRecord, runtime string) bool {
	for _, item := range items {
		if item.Runtime == runtime {
			return true
		}
	}
	return false
}

func projectInfoHasObservedRuntime(items []ProjectObservedRuntimeRecord, runtime string) bool {
	for _, item := range items {
		if item.Runtime == runtime {
			return true
		}
	}
	return false
}
