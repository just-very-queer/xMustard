package workspaceops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type RepoTargetRecord struct {
	TargetID   string  `json:"target_id"`
	Kind       string  `json:"kind"`
	Label      string  `json:"label"`
	Command    string  `json:"command"`
	Source     string  `json:"source"`
	SourcePath string  `json:"source_path"`
	Confidence int     `json:"confidence"`
	ProfileID  *string `json:"profile_id,omitempty"`
}

func ReadRunTargets(dataDir string, workspaceID string) ([]RepoTargetRecord, error) {
	if snapshot, err := loadSnapshot(dataDir, workspaceID); err == nil && snapshot != nil && snapshot.ScannerVersion >= scannerVersion {
		if _, ok := snapshot.Summary["run_targets_total"]; ok {
			return append([]RepoTargetRecord{}, snapshot.RunTargets...), nil
		}
	}
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return discoverRunTargetsForRoot(workspace.RootPath), nil
}

func ReadVerifyTargets(dataDir string, workspaceID string) ([]RepoTargetRecord, error) {
	if snapshot, err := loadSnapshot(dataDir, workspaceID); err == nil && snapshot != nil && snapshot.ScannerVersion >= scannerVersion {
		if _, ok := snapshot.Summary["verify_targets_total"]; ok {
			profiles, profileErr := loadSavedVerificationProfiles(dataDir, workspaceID)
			if profileErr != nil {
				return nil, profileErr
			}
			return mergeVerificationProfileTargets(snapshot.VerifyTargets, profiles), nil
		}
	}
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	profiles, err := loadSavedVerificationProfiles(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return discoverVerifyTargetsForRoot(workspace.RootPath, profiles), nil
}

func discoverRunTargetsForRoot(repoRoot string) []RepoTargetRecord {
	return discoverManifestTargets(repoRoot, false)
}

func discoverVerifyTargetsForRoot(repoRoot string, profiles []verificationProfileRecord) []RepoTargetRecord {
	return mergeVerificationProfileTargets(discoverManifestTargets(repoRoot, true), profiles)
}

func discoverManifestTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	targets := []RepoTargetRecord{}
	targets = append(targets, discoverMakeTargets(repoRoot, includeVerify)...)
	targets = append(targets, discoverPackageTargets(repoRoot, includeVerify)...)
	targets = append(targets, discoverDockerTargets(repoRoot)...)
	return dedupeRepoTargets(targets)
}

func discoverMakeTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	makefile := filepath.Join(repoRoot, "Makefile")
	content, err := os.ReadFile(makefile)
	if err != nil {
		return []RepoTargetRecord{}
	}
	targets := []RepoTargetRecord{}
	pattern := regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)
	for _, raw := range strings.Split(string(content), "\n") {
		match := pattern.FindStringSubmatch(raw)
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if strings.HasPrefix(name, ".") {
			continue
		}
		kind := categorizeSemanticTargetName(name)
		if !targetKindIncluded(kind, includeVerify) {
			continue
		}
		targets = append(targets, RepoTargetRecord{
			TargetID:   "make-" + name,
			Kind:       kind,
			Label:      "make " + name,
			Command:    "make " + name,
			Source:     "makefile",
			SourcePath: "Makefile",
			Confidence: 80,
		})
	}
	return targets
}

func discoverPackageTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	type packagePayload struct {
		Scripts map[string]any `json:"scripts"`
	}
	targets := []RepoTargetRecord{}
	for _, manifest := range candidatePackageJSONFiles(repoRoot) {
		content, err := os.ReadFile(manifest)
		if err != nil {
			continue
		}
		var payload packagePayload
		if err := json.Unmarshal(content, &payload); err != nil {
			continue
		}
		prefix := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(filepath.Dir(manifest), repoRoot), string(filepath.Separator)))
		runPrefix := ""
		if prefix != "" && prefix != "." {
			runPrefix = "cd " + prefix + " && "
		}
		for name := range payload.Scripts {
			kind := categorizeSemanticTargetName(name)
			if !targetKindIncluded(kind, includeVerify) {
				continue
			}
			labelPrefix := prefix
			if labelPrefix == "" || labelPrefix == "." {
				labelPrefix = filepath.Base(manifest)
			}
			manifestPath := filepath.ToSlash(strings.TrimPrefix(manifest, repoRoot+string(filepath.Separator)))
			targets = append(targets, RepoTargetRecord{
				TargetID:   "pkg-" + hashID(manifestPath, name),
				Kind:       kind,
				Label:      labelPrefix + ":" + name,
				Command:    runPrefix + "npm run " + name,
				Source:     "package_json",
				SourcePath: manifestPath,
				Confidence: 85,
			})
		}
	}
	return targets
}

func discoverDockerTargets(repoRoot string) []RepoTargetRecord {
	targets := []RepoTargetRecord{}
	for _, candidate := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(repoRoot, candidate)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		targets = append(targets, RepoTargetRecord{
			TargetID:   "docker-" + candidate,
			Kind:       "service",
			Label:      "docker compose up (" + candidate + ")",
			Command:    "docker compose -f " + candidate + " up",
			Source:     "docker_compose",
			SourcePath: candidate,
			Confidence: 75,
		})
	}
	return targets
}

func dedupeRepoTargets(targets []RepoTargetRecord) []RepoTargetRecord {
	deduped := map[string]RepoTargetRecord{}
	order := []string{}
	for _, target := range targets {
		key := target.Kind + "|" + target.Command
		existing, ok := deduped[key]
		if !ok {
			deduped[key] = target
			order = append(order, key)
			continue
		}
		if target.Confidence > existing.Confidence {
			deduped[key] = target
		}
	}
	out := make([]RepoTargetRecord, 0, len(order))
	for _, key := range order {
		out = append(out, deduped[key])
	}
	slices.SortFunc(out, func(a, b RepoTargetRecord) int {
		if a.Kind < b.Kind {
			return -1
		}
		if a.Kind > b.Kind {
			return 1
		}
		if a.Label < b.Label {
			return -1
		}
		if a.Label > b.Label {
			return 1
		}
		if a.Command < b.Command {
			return -1
		}
		if a.Command > b.Command {
			return 1
		}
		return 0
	})
	return out
}

func targetKindIncluded(kind string, includeVerify bool) bool {
	if kind == "verify" && !includeVerify {
		return false
	}
	if !includeVerify {
		return kind != "test" && kind != "lint" && kind != "verify"
	}
	return kind == "test" || kind == "lint" || kind == "verify" || kind == "build"
}

func buildRepoContextTargetLinks(targets []RepoTargetRecord, kindLabel string) []RepoContextTargetLink {
	links := make([]RepoContextTargetLink, 0, len(targets))
	for _, target := range targets {
		links = append(links, RepoContextTargetLink{
			Target: target,
			Reason: fmt.Sprintf("%s target discovered from %s.", kindLabel, target.SourcePath),
			Score:  target.Confidence,
		})
	}
	return links
}

func mergeVerificationProfileTargets(targets []RepoTargetRecord, profiles []verificationProfileRecord) []RepoTargetRecord {
	merged := append([]RepoTargetRecord{}, targets...)
	for _, profile := range profiles {
		merged = append(merged, RepoTargetRecord{
			TargetID:   "verify-profile-" + profile.ProfileID,
			Kind:       "verify",
			Label:      "verification profile: " + profile.Name,
			Command:    profile.TestCommand,
			Source:     "verification_profile",
			SourcePath: "verification_profiles.json",
			Confidence: 95,
			ProfileID:  &profile.ProfileID,
		})
	}
	return dedupeRepoTargets(merged)
}
