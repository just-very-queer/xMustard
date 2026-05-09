package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRunTargetsAndVerifyTargetsUseManifestDiscoveryAndSavedProfiles(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "Makefile"), []byte("backend:\n\tpython3 -m uvicorn app.main:app\n\ndev:\n\t@echo choose a target\n"), 0o644); err != nil {
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
	if !allTargetsHaveTruth(runTargets, repoTargetTruthSourceLiveDiscovery, false) {
		t.Fatalf("expected live truth on run targets: %#v", runTargets)
	}
	if !hasTargetCommand(runTargets, "make backend") {
		t.Fatalf("expected make backend in run targets: %#v", runTargets)
	}
	if !hasTargetCommand(runTargets, "docker compose -f docker-compose.yml up") {
		t.Fatalf("expected docker compose target in run targets: %#v", runTargets)
	}
	if hasTargetCommand(runTargets, "make dev") {
		t.Fatalf("did not expect echo-only make target in run targets: %#v", runTargets)
	}
	if !hasTargetCommand(verifyTargets, "npm run test") {
		t.Fatalf("expected npm run test in verify targets: %#v", verifyTargets)
	}
	if npmVerify := findTargetByCommand(verifyTargets, "npm run test"); npmVerify == nil || npmVerify.TruthSource != repoTargetTruthSourceLiveDiscovery || npmVerify.ScanBound {
		t.Fatalf("expected live truth on manifest verify target: %#v", npmVerify)
	}
	if !hasTargetCommand(verifyTargets, "pytest -q") {
		t.Fatalf("expected saved verification profile target in verify targets: %#v", verifyTargets)
	}
	if profileVerify := findTargetByCommand(verifyTargets, "pytest -q"); profileVerify == nil || profileVerify.TruthSource != repoTargetTruthSourceVerificationProfileOverlay || profileVerify.ScanBound {
		t.Fatalf("expected overlay truth on saved profile verify target: %#v", profileVerify)
	}
	if hasTargetCommand(verifyTargets, "docker compose -f docker-compose.yml up") {
		t.Fatalf("did not expect docker compose run target in verify targets: %#v", verifyTargets)
	}
	if !hasTargetSourcePath(verifyTargets, "verification_profiles.json") {
		t.Fatalf("expected verification_profiles.json provenance: %#v", verifyTargets)
	}
}

func TestReadRunTargetsAndVerifyTargetsExposeSnapshotTruthAndLiveProfileOverlay(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)

	if err := os.WriteFile(filepath.Join(repoRoot, "package.json"), []byte("{\"name\":\"fixture\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{
		{
			ProfileID:         "scan-profile",
			WorkspaceID:       workspaceID,
			Name:              "scan profile",
			Description:       "Persisted at scan time",
			TestCommand:       "pytest -q tests/test_scan.py",
			CoverageFormat:    "unknown",
			MaxRuntimeSeconds: 60,
			RetryCount:        1,
			BuiltIn:           false,
			CreatedAt:         "2026-05-09T10:00:00Z",
			UpdatedAt:         "2026-05-09T10:00:00Z",
		},
	}); err != nil {
		t.Fatalf("save scan-time verification profiles: %v", err)
	}

	snapshot, err := ScanWorkspace(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("scan workspace: %v", err)
	}

	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{
		{
			ProfileID:         "scan-profile",
			WorkspaceID:       workspaceID,
			Name:              "scan profile",
			Description:       "Persisted at scan time",
			TestCommand:       "pytest -q tests/test_scan.py",
			CoverageFormat:    "unknown",
			MaxRuntimeSeconds: 60,
			RetryCount:        1,
			BuiltIn:           false,
			CreatedAt:         "2026-05-09T10:00:00Z",
			UpdatedAt:         "2026-05-09T10:00:00Z",
		},
		{
			ProfileID:         "live-overlay",
			WorkspaceID:       workspaceID,
			Name:              "live overlay",
			Description:       "Added after scan",
			TestCommand:       "pytest -q tests/test_overlay.py",
			CoverageFormat:    "unknown",
			MaxRuntimeSeconds: 60,
			RetryCount:        1,
			BuiltIn:           false,
			CreatedAt:         "2026-05-09T11:00:00Z",
			UpdatedAt:         "2026-05-09T12:00:00Z",
		},
	}); err != nil {
		t.Fatalf("save live overlay verification profiles: %v", err)
	}

	runTargets, err := ReadRunTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read run targets: %v", err)
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read verify targets: %v", err)
	}

	runTarget := findTargetByCommand(runTargets, "npm run dev")
	if runTarget == nil || runTarget.TruthSource != repoTargetTruthSourceSnapshotScan || !runTarget.ScanBound || runTarget.TruthGeneratedAt == nil || *runTarget.TruthGeneratedAt != snapshot.GeneratedAt {
		t.Fatalf("expected snapshot truth on cached run target, got %#v", runTarget)
	}

	snapshotVerify := findTargetByCommand(verifyTargets, "npm run test")
	if snapshotVerify == nil || snapshotVerify.TruthSource != repoTargetTruthSourceSnapshotScan || !snapshotVerify.ScanBound || snapshotVerify.TruthGeneratedAt == nil || *snapshotVerify.TruthGeneratedAt != snapshot.GeneratedAt {
		t.Fatalf("expected snapshot truth on cached verify target, got %#v", snapshotVerify)
	}

	overlayVerify := findTargetByCommand(verifyTargets, "pytest -q tests/test_overlay.py")
	if overlayVerify == nil || overlayVerify.TruthSource != repoTargetTruthSourceVerificationProfileOverlay || overlayVerify.ScanBound || overlayVerify.TruthGeneratedAt == nil || *overlayVerify.TruthGeneratedAt != "2026-05-09T12:00:00Z" {
		t.Fatalf("expected live overlay truth on post-scan profile target, got %#v", overlayVerify)
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
	if err := os.MkdirAll(filepath.Join(repoRoot, "api-go", "cmd", "fixture-api"), 0o755); err != nil {
		t.Fatalf("mkdir api-go/cmd/fixture-api: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "go.mod"), []byte("module fixture/api-go\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatalf("write api-go go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "api-go", "cmd", "fixture-api", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write api-go main.go: %v", err)
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

	goRunTarget := findTargetByCommand(runTargets, "cd api-go && go run ./cmd/fixture-api")
	if goRunTarget == nil {
		t.Fatalf("expected go run target in run targets: %#v", runTargets)
	}
	if goRunTarget.Source != "go_mod" || goRunTarget.WorkingDir != "api-go" || goRunTarget.EntryPath == nil || *goRunTarget.EntryPath != "api-go/cmd/fixture-api/main.go" {
		t.Fatalf("unexpected go run provenance: %#v", goRunTarget)
	}

	goBuildTarget := findTargetByCommand(runTargets, "cd api-go && go build ./cmd/fixture-api")
	if goBuildTarget == nil || goBuildTarget.Source != "go_mod" {
		t.Fatalf("expected go build target in run targets: %#v", runTargets)
	}

	goVerifyTarget := findTargetByCommand(verifyTargets, "cd api-go && go test ./...")
	if goVerifyTarget == nil {
		t.Fatalf("expected go test verify target in verify targets: %#v", verifyTargets)
	}
	if goVerifyTarget.Source != "go_mod" || goVerifyTarget.Reason == nil || !strings.Contains(*goVerifyTarget.Reason, "go test ./...") {
		t.Fatalf("unexpected go verify provenance: %#v", goVerifyTarget)
	}
}

func TestReadRunTargetsAndVerifyTargetsDiscoverDeepWorkspacePackages(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(repoRoot, "apps", "web", "client", "package.json"), []byte("{\"name\":\"@acme/web\",\"scripts\":{\"dev\":\"vite\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write web package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "apps", "api", "server", "package.json"), []byte("{\"name\":\"@acme/api\",\"scripts\":{\"dev\":\"node server.js\",\"test\":\"vitest run\"}}\n"), 0o644); err != nil {
		t.Fatalf("write api package.json: %v", err)
	}

	runTargets, err := ReadRunTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read run targets: %v", err)
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		t.Fatalf("read verify targets: %v", err)
	}

	if !hasTargetCommand(runTargets, "cd apps/web/client && npm run dev") || !hasTargetCommand(runTargets, "cd apps/api/server && npm run dev") {
		t.Fatalf("expected deep workspace package run targets: %#v", runTargets)
	}
	if !hasTargetCommand(verifyTargets, "cd apps/web/client && npm run test") || !hasTargetCommand(verifyTargets, "cd apps/api/server && npm run test") {
		t.Fatalf("expected deep workspace package verify targets: %#v", verifyTargets)
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

func TestReadRunTargetsAndVerifyTargetsExposeOwnershipAndCandidateScope(t *testing.T) {
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
	if err := saveVerificationProfiles(dataDir, workspaceID, []verificationProfileRecord{
		{
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
		},
		{
			ProfileID:         "generic-smoke",
			WorkspaceID:       workspaceID,
			Name:              "generic smoke",
			Description:       "Unscoped verification command",
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

	frontendRun := findTargetByCommand(runTargets, "make frontend")
	if frontendRun == nil || frontendRun.Ownership.Status != "exact" || len(frontendRun.Ownership.ServiceIDs) != 1 {
		t.Fatalf("expected exact raw ownership for make frontend, got %#v", frontendRun)
	}
	if frontendRun.Ownership.ScopeKind == nil || *frontendRun.Ownership.ScopeKind != "manifest" || frontendRun.Ownership.ScopeKey == nil || *frontendRun.Ownership.ScopeKey == "" {
		t.Fatalf("expected candidate scope on frontend raw target, got %#v", frontendRun)
	}

	goVerify := findTargetByCommand(verifyTargets, "cd api-go && go test ./...")
	if goVerify == nil || goVerify.Ownership.Status != "shared_scope" || len(goVerify.Ownership.ServiceIDs) != 2 {
		t.Fatalf("expected shared-scope raw ownership for module-wide go test, got %#v", goVerify)
	}
	if goVerify.Ownership.ScopeKind == nil || *goVerify.Ownership.ScopeKind != "manifest" || goVerify.Ownership.ScopeKey == nil || *goVerify.Ownership.ScopeKey == "" {
		t.Fatalf("expected manifest candidate scope for module-wide go test, got %#v", goVerify)
	}

	profileVerify := findTargetByCommand(verifyTargets, "go test ./cmd/xmustard-api")
	if profileVerify == nil || profileVerify.Ownership.Status != "exact" || len(profileVerify.Ownership.ServiceIDs) != 1 {
		t.Fatalf("expected exact raw ownership for service-scoped verification profile, got %#v", profileVerify)
	}

	genericVerify := findTargetByCommand(verifyTargets, "pytest -q")
	if genericVerify == nil || genericVerify.Ownership.Status != "unowned" || len(genericVerify.Ownership.ServiceIDs) != 0 {
		t.Fatalf("expected unowned raw ownership for generic verification profile, got %#v", genericVerify)
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

func allTargetsHaveTruth(targets []RepoTargetRecord, truthSource string, scanBound bool) bool {
	for _, item := range targets {
		if item.TruthSource != truthSource || item.ScanBound != scanBound || item.TruthGeneratedAt == nil || strings.TrimSpace(*item.TruthGeneratedAt) == "" {
			return false
		}
	}
	return true
}
