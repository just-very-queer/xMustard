package workspaceops

import (
	"os"
	"path/filepath"
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
