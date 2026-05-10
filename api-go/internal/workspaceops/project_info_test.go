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

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("backend:\n\tcd backend && python3 -m uvicorn app.main:app --reload --port 8042\nfrontend:\n\tcd frontend && npm run dev\ngo-api:\n\tcd api-go && go run ./cmd/fixture-api\nmigration-check:\n\tcd api-go && go build ./cmd/fixture-api\n\tcd rust-core && cargo check\ndev:\n\t@echo use frontend and go-api\n"), 0o644); err != nil {
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
	if projectInfo.SourceMode != projectInfoSourceModeLive {
		t.Fatalf("expected live project info source mode, got %#v", projectInfo.SourceMode)
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
	if !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "frontend") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "backend") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "fixture-api") {
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
	cargoTarget := findProjectCommand(projectInfo.StaticTruth.RunTargets, "cd rust-core && cargo run --bin fixture-core")
	if cargoTarget == nil || cargoTarget.OwnerServiceID == nil {
		t.Fatalf("expected Cargo run target owner service, got %#v", cargoTarget)
	}
	migrationCheck := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "make migration-check")
	if migrationCheck == nil || migrationCheck.OwnerServiceID != nil {
		t.Fatalf("expected multi-scope verify target to avoid a single owner, got %#v", migrationCheck)
	}
	if migrationCheck.Ownership.Status != "ambiguous" {
		t.Fatalf("expected multi-scope verify target ownership to stay ambiguous, got %#v", migrationCheck)
	}
	if !slices.Contains(migrationCheck.Ownership.ServiceIDs, *goTarget.OwnerServiceID) || !slices.Contains(migrationCheck.Ownership.ServiceIDs, *cargoTarget.OwnerServiceID) {
		t.Fatalf("expected multi-scope verify target to carry the proven service subset, got %#v", migrationCheck)
	}
	if !slices.Contains(migrationCheck.RelatedTargetIDs, goTarget.TargetID) || !slices.Contains(migrationCheck.RelatedTargetIDs, cargoTarget.TargetID) {
		t.Fatalf("expected multi-scope verify target to link both owned run targets, got %#v", migrationCheck)
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
	if snapshot.ProjectInfo.SourceMode != projectInfoSourceModeSnapshot {
		t.Fatalf("expected snapshot project info source mode, got %#v", snapshot.ProjectInfo.SourceMode)
	}
}

func TestReadProjectInfoBuildsNonComposeGoServicesAndServiceScopedProfiles(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("frontend:\n\tcd frontend && npm run dev\ngo-api:\n\tcd api-go && XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api\ngo-ops:\n\tcd api-go && go run ./cmd/xmustard-ops\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "package.json"), []byte("{\"name\":\"fixture-ui\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write frontend package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "vite.config.ts"), []byte("import { defineConfig } from 'vite'\nexport default defineConfig({ server: { port: 5177, proxy: { '/api': 'http://127.0.0.1:8042' } } })\n"), 0o644); err != nil {
		t.Fatalf("write frontend vite config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-api"), 0o755); err != nil {
		t.Fatalf("mkdir api-go/cmd/xmustard-api: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-ops"), 0o755); err != nil {
		t.Fatalf("mkdir api-go/cmd/xmustard-ops: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "go.mod"), []byte("module fixture/api-go\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatalf("write api-go go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-api", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write xmustard-api main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-ops", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write xmustard-ops main.go: %v", err)
	}
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{{
		ProfileID:         "xmustard-api-smoke",
		WorkspaceID:       workspaceID,
		Name:              "xmustard-api smoke",
		Description:       "Service-scoped verification command",
		TestCommand:       "go test ./cmd/xmustard-api",
		CoverageFormat:    "unknown",
		MaxRuntimeSeconds: 60,
		RetryCount:        1,
		SourcePaths:       []string{"api-go/cmd/xmustard-api/main.go"},
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
	if projectInfo.SourceMode != projectInfoSourceModeLive {
		t.Fatalf("expected live project info source mode, got %#v", projectInfo.SourceMode)
	}

	if len(projectInfo.StaticTruth.Services) != 0 {
		t.Fatalf("expected no compose services, got %#v", projectInfo.StaticTruth.Services)
	}
	if !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "frontend") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "xmustard-api") || !projectInfoHasServiceIdentity(projectInfo.StaticTruth.ServiceIdentities, "xmustard-ops") {
		t.Fatalf("expected non-compose service identities, got %#v", projectInfo.StaticTruth.ServiceIdentities)
	}
	frontendRun := findProjectCommand(projectInfo.StaticTruth.RunTargets, "make frontend")
	apiRun := findProjectCommand(projectInfo.StaticTruth.RunTargets, "make go-api")
	opsRun := findProjectCommand(projectInfo.StaticTruth.RunTargets, "make go-ops")
	if frontendRun == nil || apiRun == nil || opsRun == nil || frontendRun.OwnerServiceID == nil || apiRun.OwnerServiceID == nil || opsRun.OwnerServiceID == nil {
		t.Fatalf("expected owned run targets, got frontend=%#v api=%#v ops=%#v", frontendRun, apiRun, opsRun)
	}
	if *apiRun.OwnerServiceID == *opsRun.OwnerServiceID {
		t.Fatalf("expected Go cmd targets to split into distinct services, got api=%#v ops=%#v", apiRun, opsRun)
	}
	if !projectInfoHasRelationship(projectInfo.StaticTruth.ServiceRelationships, "vite_proxy_depends_on", *frontendRun.OwnerServiceID, *apiRun.OwnerServiceID) {
		t.Fatalf("expected frontend proxy to point at xmustard-api, got %#v", projectInfo.StaticTruth.ServiceRelationships)
	}
	goVerify := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "cd api-go && go test ./...")
	if goVerify == nil || goVerify.OwnerServiceID != nil {
		t.Fatalf("expected module-wide go test to remain unowned across multiple Go services, got %#v", goVerify)
	}
	if goVerify.Ownership.Status != "shared_scope" || !slices.Contains(goVerify.Ownership.ServiceIDs, *apiRun.OwnerServiceID) || !slices.Contains(goVerify.Ownership.ServiceIDs, *opsRun.OwnerServiceID) {
		t.Fatalf("expected shared-scope ownership for module-wide go test, got %#v", goVerify)
	}
	profileVerify := findProjectCommand(projectInfo.StaticTruth.VerifyTargets, "go test ./cmd/xmustard-api")
	if profileVerify == nil || profileVerify.OwnerServiceID == nil || *profileVerify.OwnerServiceID != *apiRun.OwnerServiceID {
		t.Fatalf("expected service-scoped verification profile ownership, got %#v", profileVerify)
	}
	if !slices.Contains(profileVerify.RelatedTargetIDs, apiRun.TargetID) {
		t.Fatalf("expected profile verify target to link xmustard-api run target, got %#v", profileVerify)
	}
	if profileVerify.Provenance.ConfigFiles == nil || !containsProjectInfoString(profileVerify.Provenance.ConfigFiles, "api-go/cmd/xmustard-api/main.go") {
		t.Fatalf("expected profile source path provenance, got %#v", profileVerify)
	}
	if !containsProjectInfoString(profileVerify.Provenance.ConfigHints, "Saved verification profile source path is api-go/cmd/xmustard-api/main.go.") {
		t.Fatalf("expected profile source hint, got %#v", profileVerify)
	}
}

func TestReadProjectInfoBuildsPackageWorkspaceGroupsAndDependencies(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.MkdirAll(filepath.Join(repoRoot, "apps", "web", "client"), 0o755); err != nil {
		t.Fatalf("mkdir apps/web/client: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "apps", "api", "server"), 0o755); err != nil {
		t.Fatalf("mkdir apps/api/server: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "package.json"), []byte("{\"name\":\"fixture-root\",\"workspaces\":[\"apps/*/*\"]}\n"), 0o644); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "turbo.json"), []byte("{\"tasks\":{\"dev\":{\"dependsOn\":[\"^dev\"]}}}\n"), 0o644); err != nil {
		t.Fatalf("write turbo.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "apps", "web", "client", "package.json"), []byte("{\"name\":\"@acme/web\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"},\"dependencies\":{\"@acme/api\":\"workspace:*\"}}\n"), 0o644); err != nil {
		t.Fatalf("write web package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "apps", "api", "server", "package.json"), []byte("{\"name\":\"@acme/api\",\"scripts\":{\"dev\":\"node server.js\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write api package.json: %v", err)
	}

	projectInfo, err := ReadProjectInfo(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read project info: %v", err)
	}
	if projectInfo.SourceMode != projectInfoSourceModeLive {
		t.Fatalf("expected live project info source mode, got %#v", projectInfo.SourceMode)
	}

	webRun := findProjectCommand(projectInfo.StaticTruth.RunTargets, "cd apps/web/client && npm run dev")
	apiRun := findProjectCommand(projectInfo.StaticTruth.RunTargets, "cd apps/api/server && npm run dev")
	if webRun == nil || apiRun == nil || webRun.OwnerServiceID == nil || apiRun.OwnerServiceID == nil {
		t.Fatalf("expected deep workspace package run targets, got run=%#v", projectInfo.StaticTruth.RunTargets)
	}
	if len(projectInfo.StaticTruth.ServiceGroups) != 1 {
		t.Fatalf("expected one package workspace group, got %#v", projectInfo.StaticTruth.ServiceGroups)
	}
	group := projectInfo.StaticTruth.ServiceGroups[0]
	if group.GroupType != "package_workspace" || !slices.Contains(group.MemberServiceIDs, *webRun.OwnerServiceID) || !slices.Contains(group.MemberServiceIDs, *apiRun.OwnerServiceID) {
		t.Fatalf("expected package workspace group membership, got %#v", group)
	}
	if !containsProjectInfoString(group.Provenance.ConfigFiles, "turbo.json") {
		t.Fatalf("expected workspace config evidence to include turbo.json, got %#v", group)
	}
	if !projectInfoHasRelationship(projectInfo.StaticTruth.ServiceRelationships, "package_workspace_depends_on", *webRun.OwnerServiceID, *apiRun.OwnerServiceID) {
		t.Fatalf("expected package workspace dependency relationship, got %#v", projectInfo.StaticTruth.ServiceRelationships)
	}
	webService := findProjectServiceIdentityByID(projectInfo.StaticTruth.ServiceIdentities, *webRun.OwnerServiceID)
	if webService == nil || !slices.Contains(webService.GroupIDs, group.GroupID) {
		t.Fatalf("expected workspace group linkage on service identity, got %#v", webService)
	}
}

func TestReadProjectInfoUsesLiveDiscoveryWhileSnapshotProjectInfoStaysPersisted(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("frontend:\n\tcd frontend && npm run dev\n"), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "package.json"), []byte("{\"name\":\"fixture-ui\",\"scripts\":{\"dev\":\"vite\"}}\n"), 0o644); err != nil {
		t.Fatalf("write frontend package.json: %v", err)
	}

	snapshot, err := ScanWorkspace(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	if snapshot.ProjectInfo == nil || snapshot.ProjectInfo.SourceMode != projectInfoSourceModeSnapshot {
		t.Fatalf("expected snapshot project info, got %#v", snapshot.ProjectInfo)
	}
	if !projectInfoHasCommand(snapshot.ProjectInfo.StaticTruth.RunTargets, "make frontend") {
		t.Fatalf("expected persisted snapshot run target, got %#v", snapshot.ProjectInfo.StaticTruth.RunTargets)
	}

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("frontend:\n\tcd frontend && npm run dev\nops:\n\tcd api-go && go run ./cmd/xmustard-ops\n"), 0o644); err != nil {
		t.Fatalf("rewrite Makefile: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go"), 0o755); err != nil {
		t.Fatalf("mkdir api-go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "go.mod"), []byte("module fixture/api-go\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatalf("write api-go go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-ops"), 0o755); err != nil {
		t.Fatalf("mkdir api-go cmd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "cmd", "xmustard-ops", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write api-go cmd main.go: %v", err)
	}
	if _, err := SaveVerificationProfile(dataDir, workspaceID, VerificationProfileUpsertRequest{
		Name:        "Ops smoke",
		Description: "Live-only verification profile",
		TestCommand: "go test ./cmd/xmustard-ops",
		SourcePaths: []string{"api-go/cmd/xmustard-ops/main.go"},
	}); err != nil {
		t.Fatalf("save verification profile: %v", err)
	}

	liveProjectInfo, err := ReadProjectInfo(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read project info: %v", err)
	}
	if liveProjectInfo.SourceMode != projectInfoSourceModeLive {
		t.Fatalf("expected live project info source mode, got %#v", liveProjectInfo.SourceMode)
	}
	if !projectInfoHasCommand(liveProjectInfo.StaticTruth.RunTargets, "make ops") {
		t.Fatalf("expected live project info to discover new Make target, got %#v", liveProjectInfo.StaticTruth.RunTargets)
	}
	if !projectInfoHasCommand(liveProjectInfo.StaticTruth.VerifyTargets, "go test ./cmd/xmustard-ops") {
		t.Fatalf("expected live project info to discover saved verification profile, got %#v", liveProjectInfo.StaticTruth.VerifyTargets)
	}

	persistedSnapshot, err := ReadWorkspaceSnapshot(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read workspace snapshot: %v", err)
	}
	if persistedSnapshot.ProjectInfo == nil || persistedSnapshot.ProjectInfo.SourceMode != projectInfoSourceModeSnapshot {
		t.Fatalf("expected persisted snapshot project info, got %#v", persistedSnapshot.ProjectInfo)
	}
	if projectInfoHasCommand(persistedSnapshot.ProjectInfo.StaticTruth.RunTargets, "make ops") {
		t.Fatalf("expected snapshot project info to stay persisted until rescan, got %#v", persistedSnapshot.ProjectInfo.StaticTruth.RunTargets)
	}
	if projectInfoHasCommand(persistedSnapshot.ProjectInfo.StaticTruth.VerifyTargets, "go test ./cmd/xmustard-ops") {
		t.Fatalf("expected snapshot project info to exclude post-scan verification profile, got %#v", persistedSnapshot.ProjectInfo.StaticTruth.VerifyTargets)
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

func findProjectServiceIdentityByID(items []ProjectServiceIdentityRecord, serviceID string) *ProjectServiceIdentityRecord {
	for idx := range items {
		if items[idx].ServiceID == serviceID {
			return &items[idx]
		}
	}
	return nil
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
