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
	TargetID   string                 `json:"target_id"`
	Kind       string                 `json:"kind"`
	Label      string                 `json:"label"`
	Command    string                 `json:"command"`
	Source     string                 `json:"source"`
	SourcePath string                 `json:"source_path"`
	Confidence int                    `json:"confidence"`
	ProfileID  *string                `json:"profile_id,omitempty"`
	WorkingDir string                 `json:"working_dir,omitempty"`
	EntryPath  *string                `json:"entry_path,omitempty"`
	Reason     *string                `json:"reason,omitempty"`
	Ownership  ProjectTargetOwnership `json:"ownership"`
}

type makeTargetRecipe struct {
	Name        string
	RecipeLines []string
}

type packageManifestInfo struct {
	Name         string
	Scripts      map[string]string
	Dependencies map[string]string
}

func ReadRunTargets(dataDir string, workspaceID string) ([]RepoTargetRecord, error) {
	if snapshot, err := loadSnapshot(dataDir, workspaceID); err == nil && snapshot != nil && snapshot.ScannerVersion >= scannerVersion {
		if _, ok := snapshot.Summary["run_targets_total"]; ok {
			return ensureRepoTargetsOwnership(append([]RepoTargetRecord{}, snapshot.RunTargets...)), nil
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
	runTargets, _ := discoverProjectTargetsWithOwnership(workspace.RootPath, profiles)
	return runTargets, nil
}

func ReadVerifyTargets(dataDir string, workspaceID string) ([]RepoTargetRecord, error) {
	if snapshot, err := loadSnapshot(dataDir, workspaceID); err == nil && snapshot != nil && snapshot.ScannerVersion >= scannerVersion {
		if _, ok := snapshot.Summary["verify_targets_total"]; ok {
			profiles, profileErr := loadSavedVerificationProfiles(dataDir, workspaceID)
			if profileErr != nil {
				return nil, profileErr
			}
			return ensureRepoTargetsOwnership(mergeVerificationProfileTargets(snapshot.VerifyTargets, profiles)), nil
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
	_, verifyTargets := discoverProjectTargetsWithOwnership(workspace.RootPath, profiles)
	return verifyTargets, nil
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
	targets = append(targets, discoverGoTargets(repoRoot, includeVerify)...)
	if !includeVerify {
		targets = append(targets, discoverDockerTargets(repoRoot)...)
	}
	return dedupeRepoTargets(targets)
}

func discoverMakeTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	makefile := filepath.Join(repoRoot, "Makefile")
	recipes, err := readMakeTargetRecipes(makefile)
	if err != nil {
		return []RepoTargetRecord{}
	}
	targets := []RepoTargetRecord{}
	for _, recipe := range recipes {
		name := recipe.Name
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !makeRecipeHasExecutableCommand(recipe.RecipeLines) {
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
	targets := []RepoTargetRecord{}
	for _, manifest := range candidatePackageJSONFiles(repoRoot) {
		info, err := readPackageManifestInfo(manifest)
		if err != nil {
			continue
		}
		prefix := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(filepath.Dir(manifest), repoRoot), string(filepath.Separator)))
		runPrefix := ""
		if prefix != "" && prefix != "." {
			runPrefix = "cd " + prefix + " && "
		}
		for name := range info.Scripts {
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

func discoverGoTargets(repoRoot string, includeVerify bool) []RepoTargetRecord {
	targets := []RepoTargetRecord{}
	for _, manifest := range candidateGoModFiles(repoRoot) {
		manifestPath := normalizeRepoPath(repoRoot, manifest)
		workingDir := normalizeWorkingDir(repoRoot, filepath.Dir(manifest))
		labelPrefix := workingDir
		if labelPrefix == "" {
			labelPrefix = filepath.Base(manifest)
		}
		if includeVerify {
			reason := fmt.Sprintf("Go module in %s supports go test ./... for workspace-local verification.", manifestPath)
			targets = append(targets, RepoTargetRecord{
				TargetID:   "go-test-" + hashID(manifestPath, workingDir),
				Kind:       "test",
				Label:      labelPrefix + ":go test",
				Command:    prefixCommandWithWorkingDir(workingDir, "go test ./..."),
				Source:     "go_mod",
				SourcePath: manifestPath,
				Confidence: 90,
				WorkingDir: workingDir,
				Reason:     &reason,
			})
			continue
		}
		for _, commandPkg := range discoverGoCommandPackages(repoRoot, manifest) {
			runReason := fmt.Sprintf("Go command package %s is declared by %s and has package main at %s.", commandPkg.PackagePath, manifestPath, commandPkg.EntryPath)
			targets = append(targets, RepoTargetRecord{
				TargetID:   "go-run-" + hashID(manifestPath, commandPkg.PackagePath, commandPkg.EntryPath),
				Kind:       "run",
				Label:      labelPrefix + ":" + commandPkg.Name,
				Command:    prefixCommandWithWorkingDir(workingDir, "go run ./"+commandPkg.PackagePath),
				Source:     "go_mod",
				SourcePath: manifestPath,
				Confidence: 91,
				WorkingDir: workingDir,
				EntryPath:  &commandPkg.EntryPath,
				Reason:     &runReason,
			})
			buildReason := fmt.Sprintf("Go command package %s is declared by %s and can be built from %s.", commandPkg.PackagePath, manifestPath, commandPkg.EntryPath)
			targets = append(targets, RepoTargetRecord{
				TargetID:   "go-build-" + hashID(manifestPath, commandPkg.PackagePath, commandPkg.EntryPath),
				Kind:       "build",
				Label:      labelPrefix + ":" + commandPkg.Name + " build",
				Command:    prefixCommandWithWorkingDir(workingDir, "go build ./"+commandPkg.PackagePath),
				Source:     "go_mod",
				SourcePath: manifestPath,
				Confidence: 89,
				WorkingDir: workingDir,
				EntryPath:  &commandPkg.EntryPath,
				Reason:     &buildReason,
			})
		}
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

func discoverProjectTargetsWithOwnership(repoRoot string, profiles []verificationProfileRecord) ([]RepoTargetRecord, []RepoTargetRecord) {
	runTargets := discoverRunTargetsForRoot(repoRoot)
	verifyTargets := discoverVerifyTargetsForRoot(repoRoot, profiles)
	projectInfo := buildProjectInfo("", repoRoot, runTargets, verifyTargets, profiles)
	return applyProjectCommandOwnership(runTargets, projectInfo.StaticTruth.RunTargets), applyProjectCommandOwnership(verifyTargets, projectInfo.StaticTruth.VerifyTargets)
}

func applyProjectCommandOwnership(targets []RepoTargetRecord, commands []ProjectCommandRecord) []RepoTargetRecord {
	commandsByID := map[string]ProjectCommandRecord{}
	for _, command := range commands {
		if strings.TrimSpace(command.TargetID) == "" {
			continue
		}
		commandsByID[command.TargetID] = command
	}
	items := make([]RepoTargetRecord, 0, len(targets))
	for _, target := range targets {
		enriched := target
		if command, ok := commandsByID[target.TargetID]; ok {
			enriched.Ownership = command.Ownership
		}
		items = append(items, ensureRepoTargetOwnership(enriched))
	}
	return items
}

func ensureRepoTargetsOwnership(targets []RepoTargetRecord) []RepoTargetRecord {
	items := make([]RepoTargetRecord, 0, len(targets))
	for _, target := range targets {
		items = append(items, ensureRepoTargetOwnership(target))
	}
	return items
}

func ensureRepoTargetOwnership(target RepoTargetRecord) RepoTargetRecord {
	if strings.TrimSpace(target.Ownership.Status) == "" {
		target.Ownership = newUnownedProjectTargetOwnership("No repo-backed service ownership evidence was found for this target.")
	}
	return target
}

type cargoManifestInfo struct {
	PackageName string
	Bins        []cargoBinInfo
}

type cargoBinInfo struct {
	Name string
	Path string
}

type goCommandPackageInfo struct {
	Name        string
	PackagePath string
	EntryPath   string
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

func candidateGoModFiles(repoRoot string) []string {
	candidates := candidateManifestFiles(repoRoot, "go.mod", map[string]struct{}{
		".git": {}, "node_modules": {}, "dist": {}, "build": {}, "coverage": {}, "research": {}, "__pycache__": {}, ".venv": {}, "venv": {}, "target": {}, "vendor": {},
	})
	candidates = append(candidates, discoverGoWorkspaceModuleManifestPaths(repoRoot)...)
	return dedupeStrings(candidates, 24)
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

func readPackageManifestInfo(manifestPath string) (packageManifestInfo, error) {
	type packagePayload struct {
		Name                 string            `json:"name"`
		Scripts              map[string]string `json:"scripts"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		Workspaces           any               `json:"workspaces"`
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return packageManifestInfo{}, err
	}
	var payload packagePayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return packageManifestInfo{}, err
	}
	info := packageManifestInfo{
		Name:         strings.TrimSpace(payload.Name),
		Scripts:      map[string]string{},
		Dependencies: map[string]string{},
	}
	for key, value := range payload.Scripts {
		info.Scripts[key] = value
	}
	for _, dependencyMap := range []map[string]string{
		payload.Dependencies,
		payload.DevDependencies,
		payload.OptionalDependencies,
		payload.PeerDependencies,
	} {
		for key, value := range dependencyMap {
			if _, ok := info.Dependencies[key]; !ok {
				info.Dependencies[key] = value
			}
		}
	}
	return info, nil
}

func readPackageScripts(manifestPath string) (map[string]string, error) {
	info, err := readPackageManifestInfo(manifestPath)
	if err != nil {
		return nil, err
	}
	return info.Scripts, nil
}

func discoverPackageWorkspaceManifestPaths(repoRoot string) []string {
	rootManifest := filepath.Join(repoRoot, "package.json")
	info, err := readPackageManifestInfo(rootManifest)
	if err != nil {
		return []string{}
	}
	patterns := packageWorkspacePatternsFromManifest(rootManifest, info)
	if len(patterns) == 0 {
		return []string{}
	}
	return resolveWorkspaceManifestPatterns(repoRoot, patterns, "package.json")
}

func packageWorkspacePatternsFromManifest(manifestPath string, info packageManifestInfo) []string {
	type packageWorkspacePayload struct {
		Workspaces any `json:"workspaces"`
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return []string{}
	}
	var payload packageWorkspacePayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return []string{}
	}
	return normalizePackageWorkspacePatterns(payload.Workspaces)
}

func normalizePackageWorkspacePatterns(raw any) []string {
	switch value := raw.(type) {
	case []any:
		items := []string{}
		for _, entry := range value {
			text, ok := entry.(string)
			if !ok {
				continue
			}
			items = append(items, strings.TrimSpace(text))
		}
		return dedupeStrings(items, 24)
	case map[string]any:
		packages, ok := value["packages"]
		if !ok {
			return []string{}
		}
		return normalizePackageWorkspacePatterns(packages)
	default:
		return []string{}
	}
}

func resolveWorkspaceManifestPatterns(repoRoot string, patterns []string, manifestName string) []string {
	items := []string{}
	for _, pattern := range patterns {
		items = append(items, expandWorkspaceManifestPattern(repoRoot, pattern, manifestName)...)
	}
	return dedupeStrings(items, 48)
}

func expandWorkspaceManifestPattern(repoRoot string, pattern string, manifestName string) []string {
	trimmed := filepath.ToSlash(strings.TrimSpace(pattern))
	if trimmed == "" || strings.HasPrefix(trimmed, "!") {
		return []string{}
	}
	results := []string{}
	if strings.Contains(trimmed, "**") {
		prefix := strings.TrimSuffix(trimmed, "/**")
		prefix = strings.TrimSuffix(prefix, "/*")
		base := filepath.Join(repoRoot, filepath.FromSlash(prefix))
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil {
				return nil
			}
			if entry.IsDir() {
				switch entry.Name() {
				case ".git", "node_modules", "dist", "build", "coverage", "research", "__pycache__", ".venv", "venv", "target", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() != manifestName {
				return nil
			}
			results = append(results, path)
			return nil
		})
		return dedupeStrings(results, 48)
	}
	globPattern := filepath.Join(repoRoot, filepath.FromSlash(trimmed))
	matches, _ := filepath.Glob(globPattern)
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if info.IsDir() {
			manifestPath := filepath.Join(match, manifestName)
			if _, err := os.Stat(manifestPath); err == nil {
				results = append(results, manifestPath)
			}
			continue
		}
		if filepath.Base(match) == manifestName {
			results = append(results, match)
		}
	}
	return dedupeStrings(results, 48)
}

func discoverGoWorkspaceModuleManifestPaths(repoRoot string) []string {
	goWorkPath := filepath.Join(repoRoot, "go.work")
	content, err := os.ReadFile(goWorkPath)
	if err != nil {
		return []string{}
	}
	items := []string{}
	inUseBlock := false
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "use ("):
			inUseBlock = true
			continue
		case inUseBlock && line == ")":
			inUseBlock = false
			continue
		case strings.HasPrefix(line, "use "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "use "))
		case !inUseBlock:
			continue
		}
		for _, field := range strings.Fields(line) {
			trimmed := strings.Trim(field, "\"")
			if trimmed == "" {
				continue
			}
			moduleRoot := filepath.Clean(filepath.Join(repoRoot, trimmed))
			manifestPath := filepath.Join(moduleRoot, "go.mod")
			if _, err := os.Stat(manifestPath); err == nil {
				items = append(items, manifestPath)
			}
		}
	}
	return dedupeStrings(items, 24)
}

func readMakeTargetRecipes(makefilePath string) ([]makeTargetRecipe, error) {
	content, err := os.ReadFile(makefilePath)
	if err != nil {
		return nil, err
	}
	pattern := regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)
	recipes := []makeTargetRecipe{}
	var current *makeTargetRecipe
	flush := func() {
		if current == nil {
			return
		}
		trimmed := []string{}
		for _, line := range current.RecipeLines {
			value := strings.TrimSpace(line)
			if value == "" {
				continue
			}
			trimmed = append(trimmed, value)
		}
		current.RecipeLines = trimmed
		recipes = append(recipes, *current)
		current = nil
	}
	for _, raw := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(raw) == "" {
			if current != nil && len(current.RecipeLines) > 0 {
				flush()
			}
			continue
		}
		match := pattern.FindStringSubmatch(raw)
		if len(match) >= 2 && !strings.HasPrefix(raw, "\t") && !strings.HasPrefix(raw, " ") {
			flush()
			current = &makeTargetRecipe{Name: match[1]}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, " ") {
			current.RecipeLines = append(current.RecipeLines, strings.TrimSpace(raw))
		}
	}
	flush()
	return recipes, nil
}

func makeRecipeHasExecutableCommand(lines []string) bool {
	for _, line := range lines {
		normalized := strings.TrimSpace(strings.TrimPrefix(line, "@"))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, "#") {
			continue
		}
		if strings.HasPrefix(normalized, "echo ") || normalized == "echo" {
			continue
		}
		return true
	}
	return false
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

func discoverGoCommandPackages(repoRoot string, goModPath string) []goCommandPackageInfo {
	moduleRoot := filepath.Dir(goModPath)
	cmdRoot := filepath.Join(moduleRoot, "cmd")
	entries, err := os.ReadDir(cmdRoot)
	if err != nil {
		return []goCommandPackageInfo{}
	}
	items := []goCommandPackageInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		mainPath := filepath.Join(cmdRoot, entry.Name(), "main.go")
		content, err := os.ReadFile(mainPath)
		if err != nil || !strings.Contains(string(content), "package main") {
			continue
		}
		items = append(items, goCommandPackageInfo{
			Name:        entry.Name(),
			PackagePath: filepath.ToSlash(filepath.Join("cmd", entry.Name())),
			EntryPath:   normalizeRepoPath(repoRoot, mainPath),
		})
	}
	slices.SortFunc(items, func(a, b goCommandPackageInfo) int {
		if a.PackagePath != b.PackagePath {
			return strings.Compare(a.PackagePath, b.PackagePath)
		}
		return strings.Compare(a.EntryPath, b.EntryPath)
	})
	return items
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
