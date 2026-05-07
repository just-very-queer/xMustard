package workspaceops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	WorkingDir string  `json:"working_dir,omitempty"`
	EntryPath  *string `json:"entry_path,omitempty"`
	Reason     *string `json:"reason,omitempty"`
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
	targets = append(targets, discoverPyprojectTargets(repoRoot, includeVerify)...)
	targets = append(targets, discoverCargoTargets(repoRoot, includeVerify)...)
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
			Reason:     optionalStringPtr(fmt.Sprintf("Make target '%s' is declared in Makefile.", name)),
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
				WorkingDir: normalizeWorkingDir(repoRoot, filepath.Dir(manifest)),
				Reason:     optionalStringPtr(fmt.Sprintf("package.json script '%s' is declared in %s.", name, manifestPath)),
			})
		}
	}
	return targets
}

func discoverPyprojectTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	if includeVerify {
		return []RepoTargetRecord{}
	}
	targets := []RepoTargetRecord{}
	for _, manifest := range candidatePyprojectFiles(repoRoot) {
		scripts, err := readPyprojectScripts(manifest)
		if err != nil {
			continue
		}
		manifestPath := normalizeRepoPath(repoRoot, manifest)
		workingDir := normalizeWorkingDir(repoRoot, filepath.Dir(manifest))
		labelPrefix := workingDir
		if labelPrefix == "" {
			labelPrefix = filepath.Base(manifest)
		}
		for name, target := range scripts {
			moduleName, ok := parsePythonScriptModuleTarget(target)
			if !ok {
				continue
			}
			entryFile := filepath.Join(filepath.Dir(manifest), filepath.FromSlash(strings.ReplaceAll(moduleName, ".", "/")+".py"))
			if !pythonModuleSupportsDashM(entryFile) {
				continue
			}
			entryPath := normalizeRepoPath(repoRoot, entryFile)
			command := "python3 -m " + moduleName
			reason := fmt.Sprintf("PEP 621 script '%s' in %s points to %s, and %s supports python -m.", name, manifestPath, target, entryPath)
			targets = append(targets, RepoTargetRecord{
				TargetID:   "pyproject-" + hashID(manifestPath, name, moduleName),
				Kind:       "run",
				Label:      labelPrefix + ":" + name,
				Command:    prefixCommandWithWorkingDir(workingDir, command),
				Source:     "pyproject_toml",
				SourcePath: manifestPath,
				Confidence: 92,
				WorkingDir: workingDir,
				EntryPath:  &entryPath,
				Reason:     &reason,
			})
		}
	}
	return targets
}

func discoverCargoTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	targets := []RepoTargetRecord{}
	for _, manifest := range candidateCargoTomlFiles(repoRoot) {
		info, err := readCargoManifest(manifest)
		if err != nil {
			continue
		}
		manifestPath := normalizeRepoPath(repoRoot, manifest)
		workingDir := normalizeWorkingDir(repoRoot, filepath.Dir(manifest))
		labelPrefix := workingDir
		if labelPrefix == "" {
			labelPrefix = filepath.Base(manifest)
		}
		if !includeVerify {
			for _, bin := range info.Bins {
				entryPath := normalizeRepoPath(repoRoot, bin.Path)
				command := "cargo run --bin " + bin.Name
				reason := fmt.Sprintf("Cargo package in %s exposes binary '%s' at %s.", manifestPath, bin.Name, entryPath)
				targets = append(targets, RepoTargetRecord{
					TargetID:   "cargo-bin-" + hashID(manifestPath, bin.Name, entryPath),
					Kind:       "run",
					Label:      labelPrefix + ":" + bin.Name,
					Command:    prefixCommandWithWorkingDir(workingDir, command),
					Source:     "cargo_toml",
					SourcePath: manifestPath,
					Confidence: 90,
					WorkingDir: workingDir,
					EntryPath:  &entryPath,
					Reason:     &reason,
				})
			}
			continue
		}
		reason := fmt.Sprintf("Cargo package in %s supports cargo test for workspace-local verification.", manifestPath)
		targets = append(targets, RepoTargetRecord{
			TargetID:   "cargo-test-" + hashID(manifestPath, workingDir),
			Kind:       "test",
			Label:      labelPrefix + ":cargo test",
			Command:    prefixCommandWithWorkingDir(workingDir, "cargo test"),
			Source:     "cargo_toml",
			SourcePath: manifestPath,
			Confidence: 88,
			WorkingDir: workingDir,
			Reason:     &reason,
		})
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
			Reason:     optionalStringPtr(fmt.Sprintf("Compose file %s is present at the workspace root.", candidate)),
		})
	}
	return targets
}

func dedupeRepoTargets(targets []RepoTargetRecord) []RepoTargetRecord {
	deduped := map[string]RepoTargetRecord{}
	order := []string{}
	for _, target := range targets {
		key := strings.Join([]string{
			target.Kind,
			target.Command,
			target.SourcePath,
			target.WorkingDir,
			firstOptionalString(target.EntryPath),
			firstOptionalString(target.ProfileID),
		}, "|")
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
		reason := fmt.Sprintf("%s target discovered from %s.", kindLabel, target.SourcePath)
		if target.Reason != nil && strings.TrimSpace(*target.Reason) != "" {
			reason = *target.Reason
		}
		links = append(links, RepoContextTargetLink{
			Target: target,
			Reason: reason,
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
			Reason:     optionalStringPtr(fmt.Sprintf("Operator-saved verification profile '%s'.", profile.Name)),
		})
	}
	return dedupeRepoTargets(merged)
}

type cargoManifestInfo struct {
	PackageName string
	Bins        []cargoBinInfo
}

type cargoBinInfo struct {
	Name string
	Path string
}

func candidatePyprojectFiles(repoRoot string) []string {
	return candidateManifestFiles(repoRoot, "pyproject.toml", map[string]struct{}{
		".git": {}, "node_modules": {}, "dist": {}, "build": {}, "coverage": {}, "research": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	})
}

func candidateCargoTomlFiles(repoRoot string) []string {
	return candidateManifestFiles(repoRoot, "Cargo.toml", map[string]struct{}{
		".git": {}, "node_modules": {}, "dist": {}, "build": {}, "coverage": {}, "research": {}, "__pycache__": {}, ".venv": {}, "venv": {}, "target": {},
	})
}

func candidateManifestFiles(repoRoot string, manifestName string, excluded map[string]struct{}) []string {
	candidates := []string{}
	queue := []string{repoRoot}
	maxDepth := 2
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		relative, _ := filepath.Rel(repoRoot, current)
		depth := 0
		if relative != "." {
			depth = len(strings.Split(filepath.ToSlash(relative), "/"))
		}
		manifestPath := filepath.Join(current, manifestName)
		if _, err := os.Stat(manifestPath); err == nil {
			candidates = append(candidates, manifestPath)
		}
		if depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, skip := excluded[entry.Name()]; skip {
				continue
			}
			queue = append(queue, filepath.Join(current, entry.Name()))
		}
	}
	slices.Sort(candidates)
	return candidates
}

func readPyprojectScripts(manifestPath string) (map[string]string, error) {
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	scripts := map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(stripTOMLInlineComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		if section != "project.scripts" {
			continue
		}
		key, value, ok := parseTOMLKeyValue(line)
		if !ok {
			continue
		}
		scripts[key] = value
	}
	return scripts, nil
}

func readCargoManifest(manifestPath string) (cargoManifestInfo, error) {
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return cargoManifestInfo{}, err
	}
	info := cargoManifestInfo{}
	section := ""
	currentBin := cargoBinInfo{}
	appendCurrentBin := func() {
		if currentBin.Name == "" || currentBin.Path == "" {
			currentBin = cargoBinInfo{}
			return
		}
		currentBin.Path = filepath.Clean(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(currentBin.Path)))
		info.Bins = append(info.Bins, currentBin)
		currentBin = cargoBinInfo{}
	}
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(stripTOMLInlineComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			appendCurrentBin()
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			appendCurrentBin()
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := parseTOMLKeyValue(line)
		if !ok {
			continue
		}
		switch section {
		case "package":
			if key == "name" {
				info.PackageName = value
			}
		case "bin":
			switch key {
			case "name":
				currentBin.Name = value
			case "path":
				currentBin.Path = value
			}
		}
	}
	appendCurrentBin()
	info.Bins = append(info.Bins, discoverCargoConventionBins(manifestPath, info.PackageName)...)
	info.Bins = dedupeCargoBins(info.Bins)
	return info, nil
}

func discoverCargoConventionBins(manifestPath string, packageName string) []cargoBinInfo {
	manifestDir := filepath.Dir(manifestPath)
	bins := []cargoBinInfo{}
	if packageName != "" {
		mainPath := filepath.Join(manifestDir, "src", "main.rs")
		if _, err := os.Stat(mainPath); err == nil {
			bins = append(bins, cargoBinInfo{Name: packageName, Path: mainPath})
		}
	}
	pattern := filepath.Join(manifestDir, "src", "bin", "*.rs")
	matches, _ := filepath.Glob(pattern)
	for _, match := range matches {
		name := strings.TrimSuffix(filepath.Base(match), filepath.Ext(match))
		if name == "" {
			continue
		}
		bins = append(bins, cargoBinInfo{Name: name, Path: match})
	}
	return bins
}

func dedupeCargoBins(items []cargoBinInfo) []cargoBinInfo {
	seen := map[string]cargoBinInfo{}
	order := []string{}
	for _, item := range items {
		key := item.Name + "|" + filepath.Clean(item.Path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = item
		order = append(order, key)
	}
	out := make([]cargoBinInfo, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	return out
}

func parsePythonScriptModuleTarget(raw string) (string, bool) {
	module, _, found := strings.Cut(raw, ":")
	module = strings.TrimSpace(module)
	if !found || module == "" {
		return "", false
	}
	return module, true
}

func pythonModuleSupportsDashM(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(content)
	return strings.Contains(text, `if __name__ == "__main__"`) || strings.Contains(text, "if __name__ == '__main__'")
}

func stripTOMLInlineComment(line string) string {
	var builder strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	for _, r := range line {
		switch r {
		case '\\':
			if inDouble {
				escaped = !escaped
			}
			builder.WriteRune(r)
			continue
		case '"':
			if !inSingle && !escaped {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '#':
			if !inSingle && !inDouble {
				return builder.String()
			}
		}
		builder.WriteRune(r)
		escaped = false
	}
	return builder.String()
}

func parseTOMLKeyValue(line string) (string, string, bool) {
	index := strings.Index(line, "=")
	if index < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:index])
	value := strings.TrimSpace(line[index+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	decoded, ok := decodeTOMLString(value)
	if !ok {
		return "", "", false
	}
	return key, decoded, true
}

func decodeTOMLString(value string) (string, bool) {
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", false
		}
		return decoded, true
	}
	if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return value[1 : len(value)-1], true
	}
	return "", false
}

func normalizeWorkingDir(repoRoot string, absDir string) string {
	normalized := normalizeRepoPath(repoRoot, absDir)
	if normalized == "." {
		return ""
	}
	return normalized
}

func normalizeRepoPath(repoRoot string, absPath string) string {
	relative, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(absPath))
	}
	return filepath.ToSlash(relative)
}

func prefixCommandWithWorkingDir(workingDir string, command string) string {
	if strings.TrimSpace(workingDir) == "" || workingDir == "." {
		return command
	}
	return "cd " + workingDir + " && " + command
}

func optionalStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func firstOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
