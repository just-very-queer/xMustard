package workspaceops

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReadProjectInfoBuildsDeterministicStaticAndRuntimeTruth(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("backend:\n\tcd backend && python3 -m uvicorn app.main:app --reload --port 8042\nfrontend:\n\tcd frontend && npm run dev\ngo-api:\n\tcd api-go && go run ./cmd/fixture-api\ndev:\n\t@echo use frontend and go-api\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "docker-compose.yml"), []byte("services:\n  api:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "package.json"), []byte("{\"name\":\"fixture-ui\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write frontend package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "vite.config.ts"), []byte("import { defineConfig } from 'vite'\nexport default defineConfig({ server: { port: 5177, proxy: { '/api': 'http://127.0.0.1:8042' } } })\n"), 0o644); err != nil {
		t.Fatalf("write frontend vite config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "pyproject.toml"), []byte("[project]\nname = \"fixture-backend\"\n[project.scripts]\nxmustard = \"app.cli:app\"\n"), 0o644); err != nil {
		t.Fatalf("write backend pyproject.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "app", "cli.py"), []byte("def app():\n    return True\n\nif __name__ == \"__main__\":\n    app()\n"), 0o644); err != nil {
		t.Fatalf("rewrite backend cli.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "app", "main.py"), []byte("def app():\n    return True\n"), 0o644); err != nil {
		t.Fatalf("write backend main.py: %v", err)
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go", "cmd", "fixture-api"), 0o755); err != nil {
		t.Fatalf("mkdir api-go/cmd/fixture-api: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "go.mod"), []byte("module fixture/api-go\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatalf("write api-go go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "cmd", "fixture-api", "main.go"), []byte("package main\n\nimport \"os\"\n\nfunc main() {\n\t_ = envDefault(\"XMUSTARD_API_PORT\", \"8080\")\n}\n\nfunc envDefault(name string, fallback string) string {\n\tvalue := os.Getenv(name)\n\tif value == \"\" {\n\t\treturn fallback\n\t}\n\treturn value\n}\n"), 0o644); err != nil {
		t.Fatalf("write api-go main.go: %v", err)
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

	if !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "package.json") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "backend/pyproject.toml") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "rust-core/Cargo.toml") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "api-go/go.mod") || !projectInfoHasManifest(projectInfo.StaticTruth.Manifests, "docker-compose.yml") {
		t.Fatalf("expected manifest inventory, got %#v", projectInfo.StaticTruth.Manifests)
	}
	if !projectInfoHasCommand(projectInfo.StaticTruth.RunTargets, "npm run dev") || !projectInfoHasCommand(projectInfo.StaticTruth.RunTargets, "cd backend && python3 -m app.cli") || !projectInfoHasCommand(projectInfo.StaticTruth.RunTargets, "cd api-go && go run ./cmd/fixture-api") || !projectInfoHasCommand(projectInfo.StaticTruth.VerifyTargets, "pytest -q") || !projectInfoHasCommand(projectInfo.StaticTruth.VerifyTargets, "cd api-go && go test ./...") {
		t.Fatalf("expected target inventory, got run=%#v verify=%#v", projectInfo.StaticTruth.RunTargets, projectInfo.StaticTruth.VerifyTargets)
	}
	if !projectInfoHasEntrypoint(projectInfo.StaticTruth.Entrypoints, "backend/app/cli.py") || !projectInfoHasEntrypoint(projectInfo.StaticTruth.Entrypoints, "rust-core/src/bin/fixture-core.rs") || !projectInfoHasEntrypoint(projectInfo.StaticTruth.Entrypoints, "api-go/cmd/fixture-api/main.go") {
		t.Fatalf("expected entrypoints, got %#v", projectInfo.StaticTruth.Entrypoints)
	}
	if !projectInfoHasService(projectInfo.StaticTruth.Services, "api") {
		t.Fatalf("expected compose service, got %#v", projectInfo.StaticTruth.Services)
	}
	if !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "frontend") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "backend") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "api-go") {
		t.Fatalf("expected manifest-backed service identities, got %#v", projectInfo.StaticTruth.ServiceIdentities)
	}
	frontendTarget := findProjectCommand(projectInfo.StaticTruth.RunTargets, "make frontend")
	if frontendTarget == nil || frontendTarget.Provenance.DeclaredCommand == nil || *frontendTarget.Provenance.DeclaredCommand != "vite" {
		t.Fatalf("expected frontend make target declared command, got %#v", frontendTarget)
	}
	if frontendTarget.OwnerServiceID == nil {
		t.Fatalf("expected frontend run target owner service, got %#v", frontendTarget)
	}
	if !slices.Contains(frontendTarget.Provenance.ConfigFiles, "frontend/vite.config.ts") {
		t.Fatalf("expected frontend vite config evidence, got %#v", frontendTarget)
	}
	if !containsProjectInfoString(frontendTarget.Provenance.ConfigHints, "Vite dev server port is 5177.") || !containsProjectInfoString(frontendTarget.Provenance.ConfigHints, "Vite proxy maps /api to http://127.0.0.1:8042.") {
		t.Fatalf("expected frontend config hints, got %#v", frontendTarget)
	}
	backendTarget := findProjectCommand(projectInfo.StaticTruth.RunTargets, "make backend")
	if backendTarget == nil || backendTarget.Provenance.EntryPath == nil || *backendTarget.Provenance.EntryPath != "backend/app/main.py" {
		t.Fatalf("expected backend make target entrypoint, got %#v", backendTarget)
	}
	if backendTarget.Provenance.DeclaredCommand == nil || !strings.Contains(*backendTarget.Provenance.DeclaredCommand, "uvicorn app.main:app --reload --port 8042") {
		t.Fatalf("expected backend declared command, got %#v", backendTarget)
	}
	if !containsProjectInfoString(backendTarget.Provenance.ConfigHints, "Declared command sets --port 8042.") {
		t.Fatalf("expected backend port hint, got %#v", backendTarget)
	}
	if backendTarget.OwnerServiceID == nil {
		t.Fatalf("expected backend run target owner service, got %#v", backendTarget)
	}
	goTarget := findProjectCommand(projectInfo.StaticTruth.RunTargets, "cd api-go && go run ./cmd/fixture-api")
	if goTarget == nil || !containsProjectInfoString(goTarget.Provenance.ConfigHints, "Entrypoint reads env var XMUSTARD_API_PORT with default 8080.") {
		t.Fatalf("expected Go env config hint, got %#v", goTarget)
	}
	if goTarget.OwnerServiceID == nil {
		t.Fatalf("expected Go run target owner service, got %#v", goTarget)
	}
	frontendVerify := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "cd frontend && npm run test")
	if frontendVerify == nil || frontendVerify.OwnerServiceID == nil || frontendTarget.OwnerServiceID == nil || *frontendVerify.OwnerServiceID != *frontendTarget.OwnerServiceID {
		t.Fatalf("expected frontend verify ownership, got %#v", frontendVerify)
	}
	if !slices.Contains(frontendVerify.RelatedTargetIDs, frontendTarget.TargetID) {
		t.Fatalf("expected frontend verify target to link frontend run target, got %#v", frontendVerify)
	}
	goVerify := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "cd api-go && go test ./...")
	if goVerify == nil || goVerify.OwnerServiceID == nil || goTarget.OwnerServiceID == nil || *goVerify.OwnerServiceID != *goTarget.OwnerServiceID {
		t.Fatalf("expected Go verify ownership, got %#v", goVerify)
	}
	if !slices.Contains(goVerify.RelatedTargetIDs, goTarget.TargetID) {
		t.Fatalf("expected Go verify target to link Go run target, got %#v", goVerify)
	}
	profileVerify := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "pytest -q")
	if profileVerify == nil || profileVerify.OwnerServiceID != nil {
		t.Fatalf("expected saved verification profile to remain unowned, got %#v", profileVerify)
	}
	service := findProjectService(projectInfo.StaticTruth.Services, "api")
	if service == nil || service.Provenance.ServiceName == nil || *service.Provenance.ServiceName != "api" {
		t.Fatalf("expected compose service provenance, got %#v", service)
	}
	if !containsProjectInfoString(service.Provenance.ConfigHints, "Compose service image: busybox.") {
		t.Fatalf("expected compose service image hint, got %#v", service)
	}
	if !projectInfoHasDeclaredRuntime(projectInfo.StaticTruth.Runtimes, "python3") || !projectInfoHasDeclaredRuntime(projectInfo.StaticTruth.Runtimes, "cargo") || !projectInfoHasDeclaredRuntime(projectInfo.StaticTruth.Runtimes, "go") || !projectInfoHasObservedRuntime(projectInfo.RuntimeTruth.Runtimes, "npm") || !projectInfoHasObservedRuntime(projectInfo.RuntimeTruth.Runtimes, "go") {
		t.Fatalf("expected runtime inventory, got static=%#v runtime=%#v", projectInfo.StaticTruth.Runtimes, projectInfo.RuntimeTruth.Runtimes)
	}
	if !projectInfoHasRelationship(projectInfo.StaticTruth.ServiceRelationships, "vite_proxy_depends_on", *frontendTarget.OwnerServiceID, *backendTarget.OwnerServiceID) {
		t.Fatalf("expected Vite proxy dependency relationship, got %#v", projectInfo.StaticTruth.ServiceRelationships)
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

func projectInfoHasServiceIdentity(items []ProjectServiceIdentityRecord, name string) bool {
	for _, item := range items {
		if item.Name == name {
			return true
		}
	}
	return false
}

func projectInfoHasRelationship(items []ProjectServiceRelationshipRecord, relationshipType string, sourceServiceID string, targetServiceID string) bool {
	for _, item := range items {
		if item.RelationshipType == relationshipType && item.SourceServiceID == sourceServiceID && item.TargetServiceID == targetServiceID {
			return true
		}
	}
	return false
}

func findProjectCommand(items []ProjectCommandRecord, command string) *ProjectCommandRecord {
	for idx := range items {
		if items[idx].Command == command {
			return &items[idx]
		}
	}
	return nil
}

func findProjectService(items []ProjectServiceRecord, name string) *ProjectServiceRecord {
	for idx := range items {
		if items[idx].Name == name {
			return &items[idx]
		}
	}
	return nil
}

func containsProjectInfoString(items []string, expected string) bool {
	for _, item := range items {
		if item == expected {
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
