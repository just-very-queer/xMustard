package workspaceops

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	projectInfoVerdictDeclared            = "declared"
	projectInfoVerdictRuntimeObserved     = "runtime_observed"
	projectInfoVerdictConfigBacked        = "config_backed"
	projectInfoVerdictInferredNeedsReview = "inferred_needs_review"
	projectInfoVerdictUnavailable         = "unavailable"
)

type ProjectInfoRecord struct {
	WorkspaceID  string                  `json:"workspace_id"`
	RootPath     string                  `json:"root_path"`
	StaticTruth  ProjectInfoStaticTruth  `json:"static_truth"`
	RuntimeTruth ProjectInfoRuntimeTruth `json:"runtime_truth"`
	GeneratedAt  string                  `json:"generated_at"`
}

type ProjectInfoStaticTruth struct {
	Manifests     []ProjectManifestRecord   `json:"manifests"`
	Runtimes      []ProjectRuntimeRecord    `json:"runtimes"`
	Entrypoints   []ProjectEntrypointRecord `json:"entrypoints"`
	RunTargets    []ProjectCommandRecord    `json:"run_targets"`
	VerifyTargets []ProjectCommandRecord    `json:"verify_targets"`
	Services      []ProjectServiceRecord    `json:"services"`
	Warnings      []string                  `json:"warnings"`
}

type ProjectInfoRuntimeTruth struct {
	Runtimes []ProjectObservedRuntimeRecord `json:"runtimes"`
	Warnings []string                       `json:"warnings"`
}

type ProjectInfoProvenance struct {
	SourceKind   string        `json:"source_kind"`
	SourceFile   *string       `json:"source_file,omitempty"`
	Command      *string       `json:"command,omitempty"`
	Cwd          *string       `json:"cwd,omitempty"`
	EntryPath    *string       `json:"entry_path,omitempty"`
	EvidenceType string        `json:"evidence_type"`
	Evidence     []evidenceRef `json:"evidence"`
	ProfileID    *string       `json:"profile_id,omitempty"`
	Confidence   *int          `json:"confidence,omitempty"`
	Reason       *string       `json:"reason,omitempty"`
}

type ProjectManifestRecord struct {
	ManifestKind string                `json:"manifest_kind"`
	Path         string                `json:"path"`
	Verdict      string                `json:"verdict"`
	Provenance   ProjectInfoProvenance `json:"provenance"`
}

type ProjectRuntimeRecord struct {
	Runtime     string                `json:"runtime"`
	SourceFiles []string              `json:"source_files"`
	Verdict     string                `json:"verdict"`
	Provenance  ProjectInfoProvenance `json:"provenance"`
}

type ProjectObservedRuntimeRecord struct {
	Runtime     string                `json:"runtime"`
	SourceFiles []string              `json:"source_files"`
	Available   bool                  `json:"available"`
	BinaryPath  *string               `json:"binary_path,omitempty"`
	Verdict     string                `json:"verdict"`
	Provenance  ProjectInfoProvenance `json:"provenance"`
}

type ProjectEntrypointRecord struct {
	Kind       string                `json:"kind"`
	Label      string                `json:"label"`
	Command    string                `json:"command"`
	Verdict    string                `json:"verdict"`
	Provenance ProjectInfoProvenance `json:"provenance"`
}

type ProjectCommandRecord struct {
	Kind       string                `json:"kind"`
	Label      string                `json:"label"`
	Command    string                `json:"command"`
	Verdict    string                `json:"verdict"`
	Provenance ProjectInfoProvenance `json:"provenance"`
}

type ProjectServiceRecord struct {
	Name       string                `json:"name"`
	Command    string                `json:"command"`
	Verdict    string                `json:"verdict"`
	Provenance ProjectInfoProvenance `json:"provenance"`
}

func ReadProjectInfo(dataDir string, workspaceID string) (*ProjectInfoRecord, error) {
	workspace, err := getWorkspaceRecord(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	runTargets, err := ReadRunTargets(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	verifyTargets, err := ReadVerifyTargets(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	return buildProjectInfo(workspaceID, workspace.RootPath, runTargets, verifyTargets), nil
}

func buildProjectInfo(workspaceID string, repoRoot string, runTargets []RepoTargetRecord, verifyTargets []RepoTargetRecord) *ProjectInfoRecord {
	manifests := discoverProjectManifests(repoRoot)
	declaredRuntimes := discoverDeclaredProjectRuntimes(manifests)
	services, warnings := discoverDeclaredServices(repoRoot)
	return &ProjectInfoRecord{
		WorkspaceID: workspaceID,
		RootPath:    repoRoot,
		StaticTruth: ProjectInfoStaticTruth{
			Manifests:     manifests,
			Runtimes:      declaredRuntimes,
			Entrypoints:   buildProjectEntrypoints(runTargets),
			RunTargets:    buildProjectCommandRecords(runTargets),
			VerifyTargets: buildProjectCommandRecords(verifyTargets),
			Services:      services,
			Warnings:      warnings,
		},
		RuntimeTruth: ProjectInfoRuntimeTruth{
			Runtimes: observeDeclaredProjectRuntimes(declaredRuntimes),
			Warnings: []string{},
		},
		GeneratedAt: nowUTC(),
	}
}

func discoverProjectManifests(repoRoot string) []ProjectManifestRecord {
	manifests := []ProjectManifestRecord{}
	for _, manifest := range candidatePackageJSONFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "package.json is present and contributes repo-declared scripts and package metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "package_json",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("package_json", &relative, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	makefilePath := filepath.Join(repoRoot, "Makefile")
	if _, err := os.Stat(makefilePath); err == nil {
		relative := "Makefile"
		reason := "Makefile is present at the workspace root."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "makefile",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("makefile", &relative, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, manifest := range candidatePyprojectFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "pyproject.toml is present and contributes repo-declared Python packaging metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "pyproject_toml",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("pyproject_toml", &relative, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, manifest := range candidateCargoTomlFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "Cargo.toml is present and contributes repo-declared Rust package metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "cargo_toml",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("cargo_toml", &relative, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, candidate := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(repoRoot, candidate)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		relative := normalizeRepoPath(repoRoot, path)
		reason := "Compose file is present at the workspace root."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "docker_compose",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("docker_compose", &relative, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	slices.SortFunc(manifests, func(a, b ProjectManifestRecord) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.ManifestKind, b.ManifestKind)
	})
	return manifests
}

func discoverDeclaredProjectRuntimes(manifests []ProjectManifestRecord) []ProjectRuntimeRecord {
	type runtimeSeed struct {
		sourcePaths []string
		sourceKind  string
	}
	seeds := map[string]*runtimeSeed{}
	add := func(runtime string, sourceKind string, sourcePath string) {
		if runtime == "" || sourcePath == "" {
			return
		}
		existing, ok := seeds[runtime]
		if !ok {
			seeds[runtime] = &runtimeSeed{
				sourcePaths: []string{sourcePath},
				sourceKind:  sourceKind,
			}
			return
		}
		if !slices.Contains(existing.sourcePaths, sourcePath) {
			existing.sourcePaths = append(existing.sourcePaths, sourcePath)
			slices.Sort(existing.sourcePaths)
		}
	}
	for _, manifest := range manifests {
		switch manifest.ManifestKind {
		case "package_json":
			add("node", manifest.ManifestKind, manifest.Path)
			add("npm", manifest.ManifestKind, manifest.Path)
		case "pyproject_toml":
			add("python3", manifest.ManifestKind, manifest.Path)
		case "cargo_toml":
			add("cargo", manifest.ManifestKind, manifest.Path)
		case "makefile":
			add("make", manifest.ManifestKind, manifest.Path)
		case "docker_compose":
			add("docker", manifest.ManifestKind, manifest.Path)
		}
	}
	runtimes := []ProjectRuntimeRecord{}
	for runtime, seed := range seeds {
		sourceFile := firstString(seed.sourcePaths)
		reason := "Repo manifests declare commands that depend on this local runtime."
		runtimes = append(runtimes, ProjectRuntimeRecord{
			Runtime:     runtime,
			SourceFiles: append([]string{}, seed.sourcePaths...),
			Verdict:     projectInfoVerdictDeclared,
			Provenance:  newProjectInfoProvenance(seed.sourceKind, optionalString(sourceFile), optionalString(runtime), nil, nil, "manifest_file", seed.sourcePaths, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	slices.SortFunc(runtimes, func(a, b ProjectRuntimeRecord) int {
		return strings.Compare(a.Runtime, b.Runtime)
	})
	return runtimes
}

func buildProjectEntrypoints(targets []RepoTargetRecord) []ProjectEntrypointRecord {
	items := []ProjectEntrypointRecord{}
	for _, target := range targets {
		if target.EntryPath == nil || strings.TrimSpace(*target.EntryPath) == "" {
			continue
		}
		verdict := projectInfoVerdictConfigBacked
		if target.Source == "pyproject_toml" {
			verdict = projectInfoVerdictInferredNeedsReview
		}
		items = append(items, ProjectEntrypointRecord{
			Kind:       target.Kind,
			Label:      target.Label,
			Command:    target.Command,
			Verdict:    verdict,
			Provenance: projectInfoProvenanceFromTarget(target, verdict, "entry_file"),
		})
	}
	slices.SortFunc(items, func(a, b ProjectEntrypointRecord) int {
		if a.Command != b.Command {
			return strings.Compare(a.Command, b.Command)
		}
		return strings.Compare(a.Label, b.Label)
	})
	return items
}

func buildProjectCommandRecords(targets []RepoTargetRecord) []ProjectCommandRecord {
	items := make([]ProjectCommandRecord, 0, len(targets))
	for _, target := range targets {
		verdict := projectInfoVerdictFromTarget(target)
		items = append(items, ProjectCommandRecord{
			Kind:       target.Kind,
			Label:      target.Label,
			Command:    target.Command,
			Verdict:    verdict,
			Provenance: projectInfoProvenanceFromTarget(target, verdict, projectInfoEvidenceTypeFromTarget(target, verdict)),
		})
	}
	return items
}

func projectInfoVerdictFromTarget(target RepoTargetRecord) string {
	switch target.Source {
	case "verification_profile":
		return projectInfoVerdictConfigBacked
	case "package_json", "makefile":
		return projectInfoVerdictInferredNeedsReview
	case "pyproject_toml", "cargo_toml":
		return projectInfoVerdictConfigBacked
	default:
		return projectInfoVerdictDeclared
	}
}

func projectInfoEvidenceTypeFromTarget(target RepoTargetRecord, verdict string) string {
	switch {
	case target.ProfileID != nil:
		return "saved_config"
	case target.EntryPath != nil && strings.TrimSpace(*target.EntryPath) != "":
		return "entry_file"
	case verdict == projectInfoVerdictInferredNeedsReview:
		return "derived_command"
	default:
		return "declared_command"
	}
}

func projectInfoProvenanceFromTarget(target RepoTargetRecord, verdict string, evidenceType string) ProjectInfoProvenance {
	sourceFile := optionalString(target.SourcePath)
	cwd := optionalString(target.WorkingDir)
	command := optionalString(target.Command)
	evidencePaths := []string{}
	if strings.TrimSpace(target.SourcePath) != "" {
		evidencePaths = append(evidencePaths, target.SourcePath)
	}
	if target.EntryPath != nil && strings.TrimSpace(*target.EntryPath) != "" {
		evidencePaths = append(evidencePaths, *target.EntryPath)
	}
	reason := target.Reason
	confidence := &target.Confidence
	if verdict == projectInfoVerdictInferredNeedsReview && reason == nil {
		reason = optionalStringPtr("Command grouping is derived from repo declarations and current target taxonomy, not from a repo-authored runbook.")
	}
	return newProjectInfoProvenance(target.Source, sourceFile, command, cwd, target.EntryPath, evidenceType, evidencePaths, target.ProfileID, confidence, reason)
}

func discoverDeclaredServices(repoRoot string) ([]ProjectServiceRecord, []string) {
	type composePayload struct {
		Services map[string]any `yaml:"services"`
	}
	items := []ProjectServiceRecord{}
	warnings := []string{}
	for _, candidate := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		path := filepath.Join(repoRoot, candidate)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var payload composePayload
		if err := yaml.Unmarshal(raw, &payload); err != nil {
			warnings = append(warnings, "Unable to parse declared services from "+candidate+".")
			continue
		}
		names := make([]string, 0, len(payload.Services))
		for name := range payload.Services {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			command := "docker compose -f " + candidate + " up " + name
			sourceFile := candidate
			reason := "Service is explicitly declared under the compose file's services map."
			items = append(items, ProjectServiceRecord{
				Name:    name,
				Command: command,
				Verdict: projectInfoVerdictDeclared,
				Provenance: newProjectInfoProvenance(
					"docker_compose",
					&sourceFile,
					&command,
					nil,
					nil,
					"compose_service",
					[]string{candidate},
					nil,
					projectInfoIntPtr(100),
					optionalStringPtr(reason),
				),
			})
		}
	}
	slices.SortFunc(items, func(a, b ProjectServiceRecord) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.Command, b.Command)
	})
	return items, dedupeStrings(warnings, 8)
}

func observeDeclaredProjectRuntimes(items []ProjectRuntimeRecord) []ProjectObservedRuntimeRecord {
	observed := make([]ProjectObservedRuntimeRecord, 0, len(items))
	for _, item := range items {
		runtimeCommand := item.Runtime
		binaryPath, err := exec.LookPath(runtimeCommand)
		available := err == nil && strings.TrimSpace(binaryPath) != ""
		verdict := projectInfoVerdictUnavailable
		if available {
			verdict = projectInfoVerdictRuntimeObserved
		}
		sourceFile := firstString(item.SourceFiles)
		command := runtimeCommand
		reason := "Required runtime was not found on PATH for the current host."
		if available {
			reason = "Required runtime was resolved on PATH for the current host."
		}
		observed = append(observed, ProjectObservedRuntimeRecord{
			Runtime:     item.Runtime,
			SourceFiles: append([]string{}, item.SourceFiles...),
			Available:   available,
			BinaryPath:  optionalString(binaryPath),
			Verdict:     verdict,
			Provenance: newProjectInfoProvenance(
				"runtime_probe",
				optionalString(sourceFile),
				&command,
				nil,
				nil,
				"runtime_binary_lookup",
				item.SourceFiles,
				nil,
				projectInfoIntPtr(100),
				optionalStringPtr(reason),
			),
		})
	}
	return observed
}

func newProjectInfoProvenance(sourceKind string, sourceFile *string, command *string, cwd *string, entryPath *string, evidenceType string, evidencePaths []string, profileID *string, confidence *int, reason *string) ProjectInfoProvenance {
	return ProjectInfoProvenance{
		SourceKind:   sourceKind,
		SourceFile:   sourceFile,
		Command:      command,
		Cwd:          cwd,
		EntryPath:    entryPath,
		EvidenceType: evidenceType,
		Evidence:     projectEvidenceRefs(evidencePaths),
		ProfileID:    profileID,
		Confidence:   confidence,
		Reason:       reason,
	}
}

func projectEvidenceRefs(paths []string) []evidenceRef {
	items := []evidenceRef{}
	for _, path := range paths {
		normalized := strings.TrimSpace(path)
		if normalized == "" {
			continue
		}
		items = append(items, evidenceRef{Path: normalized, NormalizedPath: optionalString(normalized)})
	}
	return items
}

func projectInfoIntPtr(value int) *int {
	return &value
}

func firstString(items []string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}
