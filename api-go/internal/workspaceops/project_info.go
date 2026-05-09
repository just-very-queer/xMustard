package workspaceops

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	Manifests            []ProjectManifestRecord            `json:"manifests"`
	Runtimes             []ProjectRuntimeRecord             `json:"runtimes"`
	Entrypoints          []ProjectEntrypointRecord          `json:"entrypoints"`
	RunTargets           []ProjectCommandRecord             `json:"run_targets"`
	VerifyTargets        []ProjectCommandRecord             `json:"verify_targets"`
	Services             []ProjectServiceRecord             `json:"services"`
	ServiceIdentities    []ProjectServiceIdentityRecord     `json:"service_identities"`
	ServiceRelationships []ProjectServiceRelationshipRecord `json:"service_relationships"`
	Warnings             []string                           `json:"warnings"`
}

type ProjectInfoRuntimeTruth struct {
	Runtimes []ProjectObservedRuntimeRecord `json:"runtimes"`
	Warnings []string                       `json:"warnings"`
}

type ProjectInfoProvenance struct {
	SourceKind      string        `json:"source_kind"`
	SourceFile      *string       `json:"source_file,omitempty"`
	Command         *string       `json:"command,omitempty"`
	Cwd             *string       `json:"cwd,omitempty"`
	EntryPath       *string       `json:"entry_path,omitempty"`
	DeclaredCommand *string       `json:"declared_command,omitempty"`
	ServiceName     *string       `json:"service_name,omitempty"`
	ConfigFiles     []string      `json:"config_files,omitempty"`
	ConfigHints     []string      `json:"config_hints,omitempty"`
	EvidenceType    string        `json:"evidence_type"`
	Evidence        []evidenceRef `json:"evidence"`
	ProfileID       *string       `json:"profile_id,omitempty"`
	Confidence      *int          `json:"confidence,omitempty"`
	Reason          *string       `json:"reason,omitempty"`
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
	TargetID         string                `json:"target_id"`
	Kind             string                `json:"kind"`
	Label            string                `json:"label"`
	Command          string                `json:"command"`
	Verdict          string                `json:"verdict"`
	OwnerServiceID   *string               `json:"owner_service_id,omitempty"`
	RelatedTargetIDs []string              `json:"related_target_ids,omitempty"`
	Provenance       ProjectInfoProvenance `json:"provenance"`
}

type ProjectServiceRecord struct {
	Name       string                `json:"name"`
	Command    string                `json:"command"`
	Verdict    string                `json:"verdict"`
	DependsOn  []string              `json:"depends_on,omitempty"`
	Profiles   []string              `json:"profiles,omitempty"`
	Provenance ProjectInfoProvenance `json:"provenance"`
}

type ProjectServiceIdentityRecord struct {
	ServiceID       string                `json:"service_id"`
	Name            string                `json:"name"`
	IdentityKind    string                `json:"identity_kind"`
	Verdict         string                `json:"verdict"`
	WorkingDir      *string               `json:"working_dir,omitempty"`
	ManifestPaths   []string              `json:"manifest_paths,omitempty"`
	EntryPaths      []string              `json:"entry_paths,omitempty"`
	RunTargetIDs    []string              `json:"run_target_ids,omitempty"`
	VerifyTargetIDs []string              `json:"verify_target_ids,omitempty"`
	RunCommands     []string              `json:"run_commands,omitempty"`
	VerifyCommands  []string              `json:"verify_commands,omitempty"`
	DependsOn       []string              `json:"depends_on,omitempty"`
	Profiles        []string              `json:"profiles,omitempty"`
	Provenance      ProjectInfoProvenance `json:"provenance"`
}

type ProjectServiceRelationshipRecord struct {
	RelationshipID   string                `json:"relationship_id"`
	RelationshipType string                `json:"relationship_type"`
	SourceServiceID  string                `json:"source_service_id"`
	TargetServiceID  string                `json:"target_service_id"`
	Verdict          string                `json:"verdict"`
	Provenance       ProjectInfoProvenance `json:"provenance"`
}

type projectCommandResolution struct {
	Cwd             *string
	EntryPath       *string
	DeclaredCommand *string
	ServiceName     *string
	ConfigFiles     []string
	ConfigHints     []string
	ManifestPaths   []string
	ListenPorts     []string
	ProxyTargets    []projectProxyTarget
}

type projectProxyTarget struct {
	Route     string
	TargetURL string
	Port      string
}

type resolvedProjectTarget struct {
	Target           RepoTargetRecord
	Resolution       projectCommandResolution
	Verdict          string
	EvidenceType     string
	OwnerServiceID   *string
	RelatedTargetIDs []string
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
	resolvedRunTargets := resolveProjectTargets(repoRoot, runTargets)
	resolvedVerifyTargets := resolveProjectTargets(repoRoot, verifyTargets)
	serviceIdentities, serviceRelationships, graphWarnings := buildProjectServiceGraph(repoRoot, services, resolvedRunTargets, resolvedVerifyTargets)
	warnings = dedupeStrings(append(warnings, graphWarnings...), 12)
	return &ProjectInfoRecord{
		WorkspaceID: workspaceID,
		RootPath:    repoRoot,
		StaticTruth: ProjectInfoStaticTruth{
			Manifests:            manifests,
			Runtimes:             declaredRuntimes,
			Entrypoints:          buildProjectEntrypoints(repoRoot, runTargets),
			RunTargets:           buildProjectCommandRecords(resolvedRunTargets),
			VerifyTargets:        buildProjectCommandRecords(resolvedVerifyTargets),
			Services:             services,
			ServiceIdentities:    serviceIdentities,
			ServiceRelationships: serviceRelationships,
			Warnings:             warnings,
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
			Provenance:   newProjectInfoProvenance("package_json", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
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
			Provenance:   newProjectInfoProvenance("makefile", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, manifest := range candidatePyprojectFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "pyproject.toml is present and contributes repo-declared Python packaging metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "pyproject_toml",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("pyproject_toml", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, manifest := range candidateCargoTomlFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "Cargo.toml is present and contributes repo-declared Rust package metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "cargo_toml",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("cargo_toml", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	for _, manifest := range candidateGoModFiles(repoRoot) {
		relative := normalizeRepoPath(repoRoot, manifest)
		reason := "go.mod is present and contributes repo-declared Go module metadata."
		manifests = append(manifests, ProjectManifestRecord{
			ManifestKind: "go_mod",
			Path:         relative,
			Verdict:      projectInfoVerdictDeclared,
			Provenance:   newProjectInfoProvenance("go_mod", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
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
			Provenance:   newProjectInfoProvenance("docker_compose", &relative, nil, nil, nil, nil, nil, nil, nil, "manifest_file", []string{relative}, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
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
		case "go_mod":
			add("go", manifest.ManifestKind, manifest.Path)
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
			Provenance:  newProjectInfoProvenance(seed.sourceKind, optionalString(sourceFile), optionalString(runtime), nil, nil, nil, nil, nil, nil, "manifest_file", seed.sourcePaths, nil, projectInfoIntPtr(100), optionalStringPtr(reason)),
		})
	}
	slices.SortFunc(runtimes, func(a, b ProjectRuntimeRecord) int {
		return strings.Compare(a.Runtime, b.Runtime)
	})
	return runtimes
}

func buildProjectEntrypoints(repoRoot string, targets []RepoTargetRecord) []ProjectEntrypointRecord {
	items := []ProjectEntrypointRecord{}
	for _, target := range targets {
		resolution := resolveProjectCommand(repoRoot, target)
		entryPath := target.EntryPath
		if resolution.EntryPath != nil {
			entryPath = resolution.EntryPath
		}
		if entryPath == nil || strings.TrimSpace(*entryPath) == "" {
			continue
		}
		verdict := projectInfoVerdictFromTarget(target, resolution)
		items = append(items, ProjectEntrypointRecord{
			Kind:       target.Kind,
			Label:      target.Label,
			Command:    target.Command,
			Verdict:    verdict,
			Provenance: projectInfoProvenanceFromTarget(target, resolution, verdict, "entry_file"),
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

func resolveProjectTargets(repoRoot string, targets []RepoTargetRecord) []resolvedProjectTarget {
	items := make([]resolvedProjectTarget, 0, len(targets))
	for _, target := range targets {
		resolution := resolveProjectCommand(repoRoot, target)
		verdict := projectInfoVerdictFromTarget(target, resolution)
		items = append(items, resolvedProjectTarget{
			Target:       target,
			Resolution:   resolution,
			Verdict:      verdict,
			EvidenceType: projectInfoEvidenceTypeFromTarget(target, resolution, verdict),
		})
	}
	return items
}

func buildProjectCommandRecords(targets []resolvedProjectTarget) []ProjectCommandRecord {
	items := make([]ProjectCommandRecord, 0, len(targets))
	for _, target := range targets {
		items = append(items, ProjectCommandRecord{
			TargetID:         target.Target.TargetID,
			Kind:             target.Target.Kind,
			Label:            target.Target.Label,
			Command:          target.Target.Command,
			Verdict:          target.Verdict,
			OwnerServiceID:   target.OwnerServiceID,
			RelatedTargetIDs: append([]string{}, target.RelatedTargetIDs...),
			Provenance:       projectInfoProvenanceFromTarget(target.Target, target.Resolution, target.Verdict, target.EvidenceType),
		})
	}
	return items
}

func projectInfoVerdictFromTarget(target RepoTargetRecord, resolution projectCommandResolution) string {
	switch target.Source {
	case "verification_profile":
		return projectInfoVerdictConfigBacked
	case "go_mod", "pyproject_toml", "cargo_toml":
		return projectInfoVerdictConfigBacked
	case "package_json", "makefile":
		if resolution.DeclaredCommand != nil || resolution.EntryPath != nil || len(resolution.ConfigFiles) > 0 || len(resolution.ConfigHints) > 0 {
			return projectInfoVerdictConfigBacked
		}
		return projectInfoVerdictInferredNeedsReview
	default:
		return projectInfoVerdictDeclared
	}
}

func projectInfoEvidenceTypeFromTarget(target RepoTargetRecord, resolution projectCommandResolution, verdict string) string {
	switch {
	case target.ProfileID != nil:
		return "saved_config"
	case resolution.EntryPath != nil && strings.TrimSpace(*resolution.EntryPath) != "":
		return "entry_file"
	case resolution.DeclaredCommand != nil || len(resolution.ConfigFiles) > 0 || len(resolution.ConfigHints) > 0:
		return "declared_command"
	case verdict == projectInfoVerdictInferredNeedsReview:
		return "derived_command"
	default:
		return "declared_command"
	}
}

func projectInfoProvenanceFromTarget(target RepoTargetRecord, resolution projectCommandResolution, verdict string, evidenceType string) ProjectInfoProvenance {
	sourceFile := optionalString(target.SourcePath)
	cwd := optionalString(target.WorkingDir)
	if resolution.Cwd != nil {
		cwd = resolution.Cwd
	}
	command := optionalString(target.Command)
	entryPath := target.EntryPath
	if resolution.EntryPath != nil {
		entryPath = resolution.EntryPath
	}
	evidencePaths := []string{}
	if strings.TrimSpace(target.SourcePath) != "" {
		evidencePaths = append(evidencePaths, target.SourcePath)
	}
	if entryPath != nil && strings.TrimSpace(*entryPath) != "" {
		evidencePaths = append(evidencePaths, *entryPath)
	}
	evidencePaths = append(evidencePaths, resolution.ManifestPaths...)
	evidencePaths = append(evidencePaths, resolution.ConfigFiles...)
	evidencePaths = dedupeStrings(evidencePaths, 12)
	reason := target.Reason
	if resolution.DeclaredCommand != nil && verdict == projectInfoVerdictConfigBacked {
		declaredReason := "Repo files declare the underlying command behind this surfaced target."
		if target.Reason != nil && strings.TrimSpace(*target.Reason) != "" {
			declaredReason = strings.TrimSpace(*target.Reason) + " The underlying command is declared directly in repo files."
		}
		reason = optionalStringPtr(declaredReason)
	}
	confidence := &target.Confidence
	if verdict == projectInfoVerdictInferredNeedsReview && reason == nil {
		reason = optionalStringPtr("Command grouping is derived from repo declarations and current target taxonomy, not from a repo-authored runbook.")
	}
	return newProjectInfoProvenance(target.Source, sourceFile, command, cwd, entryPath, resolution.DeclaredCommand, resolution.ServiceName, resolution.ConfigFiles, resolution.ConfigHints, evidenceType, evidencePaths, target.ProfileID, confidence, reason)
}

func discoverDeclaredServices(repoRoot string) ([]ProjectServiceRecord, []string) {
	items := []ProjectServiceRecord{}
	warnings := []string{}
	manifests, manifestWarnings := readComposeManifestServices(repoRoot)
	warnings = append(warnings, manifestWarnings...)
	for _, manifest := range manifests {
		for _, service := range manifest.Services {
			command := "docker compose -f " + manifest.Path + " up " + service.Name
			sourceFile := manifest.Path
			reason := "Service is explicitly declared under the compose file's services map."
			configFiles := []string{manifest.Path}
			configHints := []string{}
			if service.Image != "" {
				configHints = append(configHints, "Compose service image: "+service.Image+".")
			}
			if service.BuildContext != "" {
				configHints = append(configHints, "Compose service build context: "+service.BuildContext+".")
				configFiles = append(configFiles, service.BuildContext)
			}
			for _, port := range service.Ports {
				configHints = append(configHints, "Compose service publishes port mapping "+port+".")
			}
			for _, envFile := range service.EnvFiles {
				configHints = append(configHints, "Compose service loads env file "+envFile+".")
				configFiles = append(configFiles, envFile)
			}
			for _, key := range service.EnvironmentKeys {
				configHints = append(configHints, "Compose service declares environment key "+key+".")
			}
			if service.WorkingDir != "" {
				configHints = append(configHints, "Compose service working_dir is "+service.WorkingDir+".")
			}
			if service.Entrypoint != "" {
				configHints = append(configHints, "Compose service entrypoint is "+service.Entrypoint+".")
			}
			if service.Command != "" {
				configHints = append(configHints, "Compose service command is "+service.Command+".")
			}
			items = append(items, ProjectServiceRecord{
				Name:      service.Name,
				Command:   command,
				Verdict:   projectInfoVerdictDeclared,
				DependsOn: append([]string{}, service.DependsOn...),
				Profiles:  append([]string{}, service.Profiles...),
				Provenance: newProjectInfoProvenance(
					"docker_compose",
					&sourceFile,
					&command,
					optionalString(service.WorkingDir),
					nil,
					nil,
					optionalString(service.Name),
					dedupeStrings(configFiles, 12),
					dedupeStrings(configHints, 12),
					"compose_service",
					dedupeStrings(configFiles, 12),
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

type projectServiceIdentitySeed struct {
	ServiceID       string
	Name            string
	IdentityKind    string
	Verdict         string
	SourceKind      string
	SourceFile      string
	WorkingDir      *string
	ManifestPaths   []string
	EntryPaths      []string
	RunTargetIDs    []string
	VerifyTargetIDs []string
	RunCommands     []string
	VerifyCommands  []string
	DependsOn       []string
	Profiles        []string
	ConfigFiles     []string
	ConfigHints     []string
	ListenPorts     []string
	ProxyTargets    []projectProxyTarget
	Reason          string
}

func buildProjectServiceGraph(repoRoot string, composeServices []ProjectServiceRecord, runTargets []resolvedProjectTarget, verifyTargets []resolvedProjectTarget) ([]ProjectServiceIdentityRecord, []ProjectServiceRelationshipRecord, []string) {
	seeds := map[string]*projectServiceIdentitySeed{}
	order := []string{}
	manifestKeyToServiceID := map[string]string{}
	composeNameToServiceID := map[string]string{}
	warnings := []string{}
	addSeed := func(seed *projectServiceIdentitySeed) *projectServiceIdentitySeed {
		if seed == nil || strings.TrimSpace(seed.ServiceID) == "" {
			return nil
		}
		existing, ok := seeds[seed.ServiceID]
		if !ok {
			seeds[seed.ServiceID] = seed
			order = append(order, seed.ServiceID)
			return seed
		}
		existing.ManifestPaths = dedupeStrings(append(existing.ManifestPaths, seed.ManifestPaths...), 12)
		existing.EntryPaths = dedupeStrings(append(existing.EntryPaths, seed.EntryPaths...), 12)
		existing.RunTargetIDs = dedupeStrings(append(existing.RunTargetIDs, seed.RunTargetIDs...), 24)
		existing.VerifyTargetIDs = dedupeStrings(append(existing.VerifyTargetIDs, seed.VerifyTargetIDs...), 24)
		existing.RunCommands = dedupeStrings(append(existing.RunCommands, seed.RunCommands...), 24)
		existing.VerifyCommands = dedupeStrings(append(existing.VerifyCommands, seed.VerifyCommands...), 24)
		existing.DependsOn = dedupeStrings(append(existing.DependsOn, seed.DependsOn...), 24)
		existing.Profiles = dedupeStrings(append(existing.Profiles, seed.Profiles...), 24)
		existing.ConfigFiles = dedupeStrings(append(existing.ConfigFiles, seed.ConfigFiles...), 24)
		existing.ConfigHints = dedupeStrings(append(existing.ConfigHints, seed.ConfigHints...), 24)
		existing.ListenPorts = dedupeStrings(append(existing.ListenPorts, seed.ListenPorts...), 24)
		existing.ProxyTargets = append(existing.ProxyTargets, seed.ProxyTargets...)
		if existing.WorkingDir == nil && seed.WorkingDir != nil {
			existing.WorkingDir = seed.WorkingDir
		}
		if existing.Reason == "" {
			existing.Reason = seed.Reason
		}
		return existing
	}

	for _, service := range composeServices {
		sourceFile := firstNonEmptyPtr(service.Provenance.SourceFile)
		serviceID := "compose:" + hashID(sourceFile, service.Name)
		dependsOn := []string{}
		for _, name := range service.DependsOn {
			dependsOn = append(dependsOn, "compose:"+hashID(sourceFile, name))
		}
		seed := addSeed(&projectServiceIdentitySeed{
			ServiceID:     serviceID,
			Name:          service.Name,
			IdentityKind:  "compose_service",
			Verdict:       service.Verdict,
			SourceKind:    "docker_compose",
			SourceFile:    sourceFile,
			WorkingDir:    service.Provenance.Cwd,
			ManifestPaths: conditionalStrings(sourceFile != "", sourceFile),
			RunCommands:   conditionalStrings(strings.TrimSpace(service.Command) != "", service.Command),
			DependsOn:     dependsOn,
			Profiles:      append([]string{}, service.Profiles...),
			ConfigFiles:   append([]string{}, service.Provenance.ConfigFiles...),
			ConfigHints:   append([]string{}, service.Provenance.ConfigHints...),
			Reason:        firstNonEmptyPtr(service.Provenance.Reason),
		})
		if seed != nil {
			composeNameToServiceID[service.Name] = seed.ServiceID
		}
	}

	for idx := range runTargets {
		manifestKey, _, seed := ensureManifestServiceIdentity(repoRoot, &runTargets[idx], nil)
		if seed == nil {
			continue
		}
		seed = addSeed(seed)
		if seed == nil {
			continue
		}
		runTargets[idx].OwnerServiceID = &seed.ServiceID
		if manifestKey != "" {
			manifestKeyToServiceID[manifestKey] = seed.ServiceID
		}
	}

	for idx := range verifyTargets {
		if verifyTargets[idx].Target.ProfileID != nil {
			continue
		}
		manifestKey, _, seed := ensureManifestServiceIdentity(repoRoot, nil, &verifyTargets[idx])
		if seed == nil {
			continue
		}
		if existingID, ok := manifestKeyToServiceID[manifestKey]; ok && existingID != "" {
			if existing := seeds[existingID]; existing != nil {
				existing = addSeed(seed)
				if existing != nil {
					verifyTargets[idx].OwnerServiceID = &existing.ServiceID
					verifyTargets[idx].RelatedTargetIDs = append([]string{}, existing.RunTargetIDs...)
				}
			}
			continue
		}
		seed = addSeed(seed)
		if seed == nil {
			continue
		}
		if manifestKey != "" {
			manifestKeyToServiceID[manifestKey] = seed.ServiceID
		}
		verifyTargets[idx].OwnerServiceID = &seed.ServiceID
		verifyTargets[idx].RelatedTargetIDs = append([]string{}, seed.RunTargetIDs...)
	}
	for idx := range verifyTargets {
		if verifyTargets[idx].Target.ProfileID == nil {
			continue
		}
		matchedServiceID, matchedTargetIDs := findExactVerificationAlias(runTargets, verifyTargets, verifyTargets[idx].Target.Command)
		if matchedServiceID == "" {
			continue
		}
		verifyTargets[idx].OwnerServiceID = &matchedServiceID
		verifyTargets[idx].RelatedTargetIDs = append([]string{}, matchedTargetIDs...)
	}

	relationships := []ProjectServiceRelationshipRecord{}
	relationshipSeen := map[string]struct{}{}
	appendRelationship := func(item ProjectServiceRelationshipRecord) {
		if _, ok := relationshipSeen[item.RelationshipID]; ok {
			return
		}
		relationshipSeen[item.RelationshipID] = struct{}{}
		relationships = append(relationships, item)
	}

	for _, service := range composeServices {
		sourceFile := firstNonEmptyPtr(service.Provenance.SourceFile)
		sourceServiceID := composeNameToServiceID[service.Name]
		for _, dependencyName := range service.DependsOn {
			targetServiceID := composeNameToServiceID[dependencyName]
			if sourceServiceID == "" || targetServiceID == "" {
				continue
			}
			reason := "Compose explicitly declares depends_on: " + service.Name + " -> " + dependencyName + "."
			appendRelationship(ProjectServiceRelationshipRecord{
				RelationshipID:   "compose-depends-on-" + hashID(sourceServiceID, targetServiceID),
				RelationshipType: "compose_depends_on",
				SourceServiceID:  sourceServiceID,
				TargetServiceID:  targetServiceID,
				Verdict:          projectInfoVerdictDeclared,
				Provenance: newProjectInfoProvenance(
					"docker_compose",
					optionalString(sourceFile),
					optionalString(service.Command),
					service.Provenance.Cwd,
					nil,
					nil,
					optionalString(service.Name),
					append([]string{}, service.Provenance.ConfigFiles...),
					append([]string{}, service.Provenance.ConfigHints...),
					"service_relationship",
					append([]string{}, service.Provenance.ConfigFiles...),
					nil,
					projectInfoIntPtr(100),
					optionalStringPtr(reason),
				),
			})
		}
	}

	portOwners := map[string][]string{}
	for serviceID, seed := range seeds {
		if seed == nil {
			continue
		}
		for _, port := range seed.ListenPorts {
			portOwners[port] = append(portOwners[port], serviceID)
		}
	}
	for port, owners := range portOwners {
		portOwners[port] = dedupeStrings(owners, 12)
	}
	for _, serviceID := range order {
		seed := seeds[serviceID]
		if seed == nil {
			continue
		}
		for _, proxyTarget := range seed.ProxyTargets {
			if proxyTarget.Port == "" {
				continue
			}
			owners := []string{}
			for _, owner := range portOwners[proxyTarget.Port] {
				if owner != serviceID {
					owners = append(owners, owner)
				}
			}
			owners = dedupeStrings(owners, 12)
			if len(owners) == 0 {
				continue
			}
			if len(owners) > 1 {
				warnings = append(warnings, "Skipped Vite proxy dependency for service "+seed.Name+" because port "+proxyTarget.Port+" matches multiple services.")
				continue
			}
			targetSeed := seeds[owners[0]]
			if targetSeed == nil {
				continue
			}
			evidencePaths := append([]string{}, seed.ManifestPaths...)
			evidencePaths = append(evidencePaths, seed.ConfigFiles...)
			evidencePaths = append(evidencePaths, targetSeed.ManifestPaths...)
			evidencePaths = append(evidencePaths, targetSeed.ConfigFiles...)
			evidencePaths = dedupeStrings(evidencePaths, 24)
			reason := "Vite proxy route " + proxyTarget.Route + " targets " + proxyTarget.TargetURL + ", and " + targetSeed.Name + " declares port " + proxyTarget.Port + "."
			appendRelationship(ProjectServiceRelationshipRecord{
				RelationshipID:   "vite-proxy-depends-on-" + hashID(serviceID, owners[0], proxyTarget.Route, proxyTarget.Port),
				RelationshipType: "vite_proxy_depends_on",
				SourceServiceID:  serviceID,
				TargetServiceID:  owners[0],
				Verdict:          projectInfoVerdictConfigBacked,
				Provenance: newProjectInfoProvenance(
					seed.SourceKind,
					optionalString(seed.SourceFile),
					optionalString(firstString(seed.RunCommands)),
					seed.WorkingDir,
					optionalString(firstString(seed.EntryPaths)),
					nil,
					optionalString(seed.Name),
					dedupeStrings(append([]string{}, seed.ConfigFiles...), 24),
					dedupeStrings(append([]string{}, seed.ConfigHints...), 24),
					"service_relationship",
					evidencePaths,
					nil,
					projectInfoIntPtr(95),
					optionalStringPtr(reason),
				),
			})
			seed.DependsOn = dedupeStrings(append(seed.DependsOn, owners[0]), 12)
		}
	}

	serviceIdentities := make([]ProjectServiceIdentityRecord, 0, len(order))
	for _, serviceID := range order {
		seed := seeds[serviceID]
		if seed == nil {
			continue
		}
		evidencePaths := append([]string{}, seed.ManifestPaths...)
		evidencePaths = append(evidencePaths, seed.EntryPaths...)
		evidencePaths = append(evidencePaths, seed.ConfigFiles...)
		evidencePaths = dedupeStrings(evidencePaths, 24)
		serviceIdentities = append(serviceIdentities, ProjectServiceIdentityRecord{
			ServiceID:       seed.ServiceID,
			Name:            seed.Name,
			IdentityKind:    seed.IdentityKind,
			Verdict:         seed.Verdict,
			WorkingDir:      seed.WorkingDir,
			ManifestPaths:   append([]string{}, seed.ManifestPaths...),
			EntryPaths:      append([]string{}, seed.EntryPaths...),
			RunTargetIDs:    append([]string{}, seed.RunTargetIDs...),
			VerifyTargetIDs: append([]string{}, seed.VerifyTargetIDs...),
			RunCommands:     append([]string{}, seed.RunCommands...),
			VerifyCommands:  append([]string{}, seed.VerifyCommands...),
			DependsOn:       append([]string{}, seed.DependsOn...),
			Profiles:        append([]string{}, seed.Profiles...),
			Provenance: newProjectInfoProvenance(
				seed.SourceKind,
				optionalString(seed.SourceFile),
				optionalString(firstString(seed.RunCommands)),
				seed.WorkingDir,
				optionalString(firstString(seed.EntryPaths)),
				nil,
				optionalString(seed.Name),
				append([]string{}, seed.ConfigFiles...),
				append([]string{}, seed.ConfigHints...),
				"service_identity",
				evidencePaths,
				nil,
				projectInfoIntPtr(100),
				optionalStringPtr(seed.Reason),
			),
		})
	}
	slices.SortFunc(serviceIdentities, func(a, b ProjectServiceIdentityRecord) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.ServiceID, b.ServiceID)
	})
	slices.SortFunc(relationships, func(a, b ProjectServiceRelationshipRecord) int {
		if a.SourceServiceID != b.SourceServiceID {
			return strings.Compare(a.SourceServiceID, b.SourceServiceID)
		}
		if a.TargetServiceID != b.TargetServiceID {
			return strings.Compare(a.TargetServiceID, b.TargetServiceID)
		}
		return strings.Compare(a.RelationshipType, b.RelationshipType)
	})
	return serviceIdentities, relationships, dedupeStrings(warnings, 12)
}

func ensureManifestServiceIdentity(repoRoot string, runTarget *resolvedProjectTarget, verifyTarget *resolvedProjectTarget) (string, string, *projectServiceIdentitySeed) {
	var current *resolvedProjectTarget
	if runTarget != nil {
		current = runTarget
	} else {
		current = verifyTarget
	}
	if current == nil {
		return "", "", nil
	}
	manifestPath, workingDir, ok := projectTargetManifestScope(repoRoot, *current)
	if !ok {
		return "", "", nil
	}
	name := projectManifestServiceName(repoRoot, manifestPath, workingDir, current.Resolution.ServiceName)
	serviceID := "manifest:" + hashID(manifestPath, workingDir)
	reason := "Manifest-backed service identity groups commands declared from " + manifestPath + "."
	entryPaths := []string{}
	if current.Resolution.EntryPath != nil && strings.TrimSpace(*current.Resolution.EntryPath) != "" {
		entryPaths = append(entryPaths, strings.TrimSpace(*current.Resolution.EntryPath))
	}
	seed := &projectServiceIdentitySeed{
		ServiceID:     serviceID,
		Name:          name,
		IdentityKind:  "manifest_scope",
		Verdict:       projectInfoVerdictConfigBacked,
		SourceKind:    current.Target.Source,
		SourceFile:    manifestPath,
		WorkingDir:    optionalString(workingDir),
		ManifestPaths: []string{manifestPath},
		EntryPaths:    entryPaths,
		ConfigFiles:   append([]string{}, current.Resolution.ConfigFiles...),
		ConfigHints:   append([]string{}, current.Resolution.ConfigHints...),
		ListenPorts:   append([]string{}, current.Resolution.ListenPorts...),
		ProxyTargets:  append([]projectProxyTarget{}, current.Resolution.ProxyTargets...),
		Reason:        reason,
	}
	if runTarget != nil {
		seed.RunTargetIDs = []string{current.Target.TargetID}
		seed.RunCommands = []string{current.Target.Command}
	} else {
		seed.VerifyTargetIDs = []string{current.Target.TargetID}
		seed.VerifyCommands = []string{current.Target.Command}
	}
	return manifestPath + "|" + workingDir, serviceID, seed
}

func findExactVerificationAlias(runTargets []resolvedProjectTarget, verifyTargets []resolvedProjectTarget, command string) (string, []string) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", nil
	}
	for _, item := range verifyTargets {
		if item.Target.ProfileID != nil || strings.TrimSpace(item.Target.Command) != trimmed || item.OwnerServiceID == nil {
			continue
		}
		return *item.OwnerServiceID, append([]string{}, item.RelatedTargetIDs...)
	}
	for _, item := range runTargets {
		if strings.TrimSpace(item.Target.Command) != trimmed || item.OwnerServiceID == nil {
			continue
		}
		return *item.OwnerServiceID, nil
	}
	return "", nil
}

func projectTargetManifestScope(repoRoot string, target resolvedProjectTarget) (string, string, bool) {
	workingDir := firstNonEmptyProjectString(firstNonEmptyPtr(target.Resolution.Cwd), target.Target.WorkingDir)
	manifestPaths := []string{}
	for _, path := range target.Resolution.ManifestPaths {
		if strings.TrimSpace(path) != "" {
			manifestPaths = append(manifestPaths, strings.TrimSpace(path))
		}
	}
	switch target.Target.Source {
	case "package_json", "pyproject_toml", "cargo_toml", "go_mod":
		manifestPaths = append(manifestPaths, target.Target.SourcePath)
	}
	if len(manifestPaths) == 0 && workingDir != "" {
		manifestPaths = append(manifestPaths, discoverManifestPathsForWorkingDir(repoRoot, workingDir)...)
	}
	manifestPaths = dedupeStrings(manifestPaths, 12)
	if len(manifestPaths) != 1 {
		return "", "", false
	}
	return manifestPaths[0], workingDir, true
}

func discoverManifestPathsForWorkingDir(repoRoot string, workingDir string) []string {
	items := []string{}
	for _, candidate := range []string{"package.json", "pyproject.toml", "Cargo.toml", "go.mod"} {
		path := filepath.Join(repoRoot, filepath.FromSlash(workingDir), candidate)
		if _, err := os.Stat(path); err == nil {
			items = append(items, normalizeRepoPath(repoRoot, path))
		}
	}
	return dedupeStrings(items, 8)
}

func projectManifestServiceName(repoRoot string, manifestPath string, workingDir string, serviceName *string) string {
	if serviceName != nil && strings.TrimSpace(*serviceName) != "" {
		return strings.TrimSpace(*serviceName)
	}
	if strings.TrimSpace(workingDir) != "" {
		return filepath.Base(filepath.FromSlash(workingDir))
	}
	manifestName := filepath.Base(manifestPath)
	if manifestName != "" && manifestName != "." {
		return filepath.Base(repoRoot)
	}
	return filepath.Base(repoRoot)
}

func appendWorkingDirManifestHints(repoRoot string, resolution *projectCommandResolution) {
	if resolution == nil || resolution.Cwd == nil || strings.TrimSpace(*resolution.Cwd) == "" {
		return
	}
	resolution.ManifestPaths = append(resolution.ManifestPaths, discoverManifestPathsForWorkingDir(repoRoot, *resolution.Cwd)...)
}

func dedupeProjectProxyTargets(items []projectProxyTarget) []projectProxyTarget {
	seen := map[string]projectProxyTarget{}
	order := []string{}
	for _, item := range items {
		key := item.Route + "|" + item.TargetURL + "|" + item.Port
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = item
		order = append(order, key)
	}
	out := make([]projectProxyTarget, 0, len(order))
	for _, key := range order {
		out = append(out, seen[key])
	}
	return out
}

func parseProjectProxyTarget(route string, rawURL string) (projectProxyTarget, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return projectProxyTarget{}, false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" {
		return projectProxyTarget{}, false
	}
	port := strings.TrimSpace(parsed.Port())
	if port == "" {
		return projectProxyTarget{}, false
	}
	return projectProxyTarget{
		Route:     strings.TrimSpace(route),
		TargetURL: strings.TrimSpace(rawURL),
		Port:      port,
	}, true
}

func isManifestSourceKind(sourceKind string) bool {
	switch sourceKind {
	case "package_json", "pyproject_toml", "cargo_toml", "go_mod":
		return true
	default:
		return false
	}
}

type composeManifestRecord struct {
	Path     string
	Services []composeServiceRecord
}

type composeServiceRecord struct {
	Name            string
	Image           string
	BuildContext    string
	Ports           []string
	EnvFiles        []string
	EnvironmentKeys []string
	DependsOn       []string
	Profiles        []string
	WorkingDir      string
	Entrypoint      string
	Command         string
}

func resolveProjectCommand(repoRoot string, target RepoTargetRecord) projectCommandResolution {
	resolution := projectCommandResolution{
		Cwd:       optionalString(target.WorkingDir),
		EntryPath: target.EntryPath,
	}
	switch target.Source {
	case "package_json":
		resolvePackageTarget(repoRoot, target, &resolution)
	case "makefile":
		resolveMakeTarget(repoRoot, target, &resolution)
	case "pyproject_toml":
		resolvePyprojectTarget(repoRoot, target, &resolution)
	case "cargo_toml":
		resolveCargoTarget(repoRoot, target, &resolution)
	case "go_mod":
		resolveGoTarget(repoRoot, target, &resolution)
	}
	appendWorkingDirManifestHints(repoRoot, &resolution)
	if isManifestSourceKind(target.Source) && strings.TrimSpace(target.SourcePath) != "" {
		resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
	}
	resolution.ManifestPaths = dedupeStrings(resolution.ManifestPaths, 12)
	resolution.ConfigFiles = dedupeStrings(resolution.ConfigFiles, 12)
	resolution.ConfigHints = dedupeStrings(resolution.ConfigHints, 12)
	resolution.ListenPorts = dedupeStrings(resolution.ListenPorts, 12)
	resolution.ProxyTargets = dedupeProjectProxyTargets(resolution.ProxyTargets)
	return resolution
}

func resolvePackageTarget(repoRoot string, target RepoTargetRecord, resolution *projectCommandResolution) {
	if resolution == nil {
		return
	}
	manifestPath := filepath.Join(repoRoot, filepath.FromSlash(target.SourcePath))
	scripts, err := readPackageScripts(manifestPath)
	if err != nil {
		return
	}
	scriptName, ok := packageScriptNameFromCommand(target.Command)
	if !ok {
		return
	}
	declared, ok := scripts[scriptName]
	if !ok || strings.TrimSpace(declared) == "" {
		return
	}
	trimmed := strings.TrimSpace(declared)
	resolution.DeclaredCommand = &trimmed
	resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
	resolution.ConfigFiles = append(resolution.ConfigFiles, target.SourcePath)
	workingDir := firstNonEmptyProjectString(target.WorkingDir, normalizeWorkingDir(repoRoot, filepath.Dir(manifestPath)))
	if workingDir != "" {
		resolution.Cwd = &workingDir
		if resolution.ServiceName == nil {
			resolution.ServiceName = optionalString(filepath.Base(workingDir))
		}
	}
	resolveViteHints(repoRoot, workingDir, trimmed, resolution)
}

func resolveMakeTarget(repoRoot string, target RepoTargetRecord, resolution *projectCommandResolution) {
	if resolution == nil {
		return
	}
	makeTarget, ok := makeTargetNameFromCommand(target.Command)
	if !ok {
		return
	}
	recipes, err := readMakeTargetRecipes(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		return
	}
	for _, recipe := range recipes {
		if recipe.Name != makeTarget {
			continue
		}
		executableLines := []string{}
		for _, line := range recipe.RecipeLines {
			normalized := strings.TrimSpace(strings.TrimPrefix(line, "@"))
			if normalized == "" || strings.HasPrefix(normalized, "#") || strings.HasPrefix(normalized, "echo ") || normalized == "echo" {
				continue
			}
			executableLines = append(executableLines, normalized)
		}
		if len(executableLines) == 0 {
			return
		}
		joined := strings.Join(executableLines, " && ")
		resolution.DeclaredCommand = &joined
		resolution.ConfigFiles = append(resolution.ConfigFiles, "Makefile")
		serviceName := recipe.Name
		resolution.ServiceName = &serviceName
		resolveShellCommand(repoRoot, joined, resolution)
		return
	}
}

func resolvePyprojectTarget(repoRoot string, target RepoTargetRecord, resolution *projectCommandResolution) {
	if resolution == nil {
		return
	}
	manifestPath := filepath.Join(repoRoot, filepath.FromSlash(target.SourcePath))
	scripts, err := readPyprojectScripts(manifestPath)
	if err != nil {
		return
	}
	labelBits := strings.Split(target.Label, ":")
	if len(labelBits) == 0 {
		return
	}
	scriptName := labelBits[len(labelBits)-1]
	if declared, ok := scripts[scriptName]; ok && strings.TrimSpace(declared) != "" {
		trimmed := strings.TrimSpace(declared)
		resolution.DeclaredCommand = &trimmed
		resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
		resolution.ConfigFiles = append(resolution.ConfigFiles, target.SourcePath)
	}
}

func resolveCargoTarget(repoRoot string, target RepoTargetRecord, resolution *projectCommandResolution) {
	if resolution == nil {
		return
	}
	resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
	manifestPath := filepath.Join(repoRoot, filepath.FromSlash(target.SourcePath))
	workingDir := firstNonEmptyProjectString(target.WorkingDir, normalizeWorkingDir(repoRoot, filepath.Dir(manifestPath)))
	if workingDir != "" {
		resolution.Cwd = &workingDir
		if resolution.ServiceName == nil {
			resolution.ServiceName = optionalString(filepath.Base(workingDir))
		}
	}
}

func resolveGoTarget(repoRoot string, target RepoTargetRecord, resolution *projectCommandResolution) {
	if resolution == nil || resolution.EntryPath == nil || strings.TrimSpace(*resolution.EntryPath) == "" {
		if resolution != nil {
			resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
		}
		return
	}
	resolution.ManifestPaths = append(resolution.ManifestPaths, target.SourcePath)
	entryAbs := filepath.Join(repoRoot, filepath.FromSlash(*resolution.EntryPath))
	content, err := os.ReadFile(entryAbs)
	if err != nil {
		return
	}
	pattern := regexp.MustCompile(`envDefault\("([A-Z0-9_]+)",\s*"([^"]+)"\)`)
	matches := pattern.FindAllStringSubmatch(string(content), -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		resolution.ConfigHints = append(resolution.ConfigHints, "Entrypoint reads env var "+match[1]+" with default "+match[2]+".")
	}
}

func resolveShellCommand(repoRoot string, command string, resolution *projectCommandResolution) {
	if resolution == nil {
		return
	}
	workingDir, inner := splitShellWorkingDir(command)
	if workingDir != "" {
		resolution.Cwd = &workingDir
		if resolution.ServiceName == nil {
			resolution.ServiceName = optionalString(filepath.Base(workingDir))
		}
	}
	if inner == "" {
		inner = command
	}
	if port, ok := extractFlagValue(inner, "--port"); ok {
		resolution.ListenPorts = append(resolution.ListenPorts, port)
		resolution.ConfigHints = append(resolution.ConfigHints, "Declared command sets --port "+port+".")
	}
	for _, match := range regexp.MustCompile(`([A-Z0-9_]+)=([^\s]+)`).FindAllStringSubmatch(inner, -1) {
		if len(match) < 3 {
			continue
		}
		resolution.ConfigHints = append(resolution.ConfigHints, "Declared command sets "+match[1]+"="+match[2]+".")
	}
	if entryPath, ok := resolvePythonModuleReference(repoRoot, workingDir, inner); ok {
		resolution.EntryPath = &entryPath
	}
	if entryPath, ok := resolveGoRunReference(repoRoot, workingDir, inner); ok {
		resolution.EntryPath = &entryPath
	}
	if scriptName, ok := packageScriptNameFromCommand(inner); ok {
		target := RepoTargetRecord{
			Command:    inner,
			Source:     "package_json",
			SourcePath: normalizeRepoPath(repoRoot, filepath.Join(repoRoot, workingDir, "package.json")),
			WorkingDir: workingDir,
			Label:      workingDir + ":" + scriptName,
		}
		resolvePackageTarget(repoRoot, target, resolution)
	}
}

func resolvePythonModuleReference(repoRoot string, workingDir string, command string) (string, bool) {
	modulePattern := regexp.MustCompile(`(?:^|\s)(?:python3?|uvicorn)\s+-m\s+([A-Za-z0-9_\.]+)`)
	if match := modulePattern.FindStringSubmatch(command); len(match) >= 2 {
		entry := filepath.Join(repoRoot, workingDir, filepath.FromSlash(strings.ReplaceAll(match[1], ".", "/")+".py"))
		if _, err := os.Stat(entry); err == nil {
			return normalizeRepoPath(repoRoot, entry), true
		}
	}
	uvicornPattern := regexp.MustCompile(`uvicorn\s+([A-Za-z0-9_\.]+):[A-Za-z0-9_]+`)
	if match := uvicornPattern.FindStringSubmatch(command); len(match) >= 2 {
		entry := filepath.Join(repoRoot, workingDir, filepath.FromSlash(strings.ReplaceAll(match[1], ".", "/")+".py"))
		if _, err := os.Stat(entry); err == nil {
			return normalizeRepoPath(repoRoot, entry), true
		}
	}
	return "", false
}

func resolveGoRunReference(repoRoot string, workingDir string, command string) (string, bool) {
	pattern := regexp.MustCompile(`go\s+(?:run|build)\s+\./([^\s]+)`)
	match := pattern.FindStringSubmatch(command)
	if len(match) < 2 {
		return "", false
	}
	entry := filepath.Join(repoRoot, workingDir, filepath.FromSlash(match[1]), "main.go")
	if _, err := os.Stat(entry); err != nil {
		return "", false
	}
	return normalizeRepoPath(repoRoot, entry), true
}

func resolveViteHints(repoRoot string, workingDir string, command string, resolution *projectCommandResolution) {
	if resolution == nil || !strings.Contains(command, "vite") {
		return
	}
	if workingDir == "" {
		return
	}
	var configPath string
	for _, candidate := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.cjs"} {
		abs := filepath.Join(repoRoot, filepath.FromSlash(workingDir), candidate)
		if _, err := os.Stat(abs); err == nil {
			configPath = normalizeRepoPath(repoRoot, abs)
			break
		}
	}
	if configPath == "" {
		return
	}
	resolution.ConfigFiles = append(resolution.ConfigFiles, configPath)
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(configPath)))
	if err != nil {
		return
	}
	if match := regexp.MustCompile(`port:\s*(\d+)`).FindStringSubmatch(string(content)); len(match) >= 2 {
		resolution.ListenPorts = append(resolution.ListenPorts, match[1])
		resolution.ConfigHints = append(resolution.ConfigHints, "Vite dev server port is "+match[1]+".")
	}
	proxyPattern := regexp.MustCompile(`['"]([^'"]+)['"]:\s*['"]([^'"]+)['"]`)
	for _, match := range proxyPattern.FindAllStringSubmatch(string(content), -1) {
		if len(match) < 3 || !strings.HasPrefix(match[1], "/") {
			continue
		}
		if proxyTarget, ok := parseProjectProxyTarget(match[1], match[2]); ok {
			resolution.ProxyTargets = append(resolution.ProxyTargets, proxyTarget)
		}
		resolution.ConfigHints = append(resolution.ConfigHints, "Vite proxy maps "+match[1]+" to "+match[2]+".")
	}
}

func readComposeManifestServices(repoRoot string) ([]composeManifestRecord, []string) {
	type composePayload struct {
		Services map[string]composeServicePayload `yaml:"services"`
	}
	type composeBuildPayload struct {
		Context    string `yaml:"context"`
		Dockerfile string `yaml:"dockerfile"`
	}
	manifests := []composeManifestRecord{}
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
		manifest := composeManifestRecord{Path: candidate}
		names := make([]string, 0, len(payload.Services))
		for name := range payload.Services {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			service := payload.Services[name]
			buildContext := ""
			switch value := service.Build.(type) {
			case string:
				buildContext = strings.TrimSpace(value)
			case map[string]any:
				if rawContext, ok := value["context"].(string); ok {
					buildContext = strings.TrimSpace(rawContext)
				}
			case composeBuildPayload:
				buildContext = strings.TrimSpace(value.Context)
			}
			manifest.Services = append(manifest.Services, composeServiceRecord{
				Name:            name,
				Image:           strings.TrimSpace(service.Image),
				BuildContext:    normalizeComposePath(candidate, buildContext),
				Ports:           append([]string{}, service.Ports...),
				EnvFiles:        normalizeComposePaths(candidate, stringSliceFromYAML(service.EnvFile)),
				EnvironmentKeys: environmentKeysFromComposeValue(service.Environment),
				DependsOn:       dependsOnFromComposeValue(service.DependsOn),
				Profiles:        append([]string{}, service.Profiles...),
				WorkingDir:      strings.TrimSpace(service.WorkingDir),
				Entrypoint:      joinComposeCommand(service.Entrypoint),
				Command:         joinComposeCommand(service.Command),
			})
		}
		manifests = append(manifests, manifest)
	}
	return manifests, dedupeStrings(warnings, 8)
}

type composeServicePayload struct {
	Image       string   `yaml:"image"`
	Build       any      `yaml:"build"`
	Ports       []string `yaml:"ports"`
	EnvFile     any      `yaml:"env_file"`
	Environment any      `yaml:"environment"`
	DependsOn   any      `yaml:"depends_on"`
	Profiles    []string `yaml:"profiles"`
	WorkingDir  string   `yaml:"working_dir"`
	Entrypoint  any      `yaml:"entrypoint"`
	Command     any      `yaml:"command"`
}

func environmentKeysFromComposeValue(value any) []string {
	keys := []string{}
	switch typed := value.(type) {
	case map[string]any:
		for key := range typed {
			keys = append(keys, key)
		}
	case []any:
		for _, raw := range typed {
			item, ok := raw.(string)
			if !ok {
				continue
			}
			key := item
			if index := strings.Index(item, "="); index > 0 {
				key = item[:index]
			}
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return dedupeStrings(keys, 24)
}

func dependsOnFromComposeValue(value any) []string {
	items := []string{}
	switch typed := value.(type) {
	case map[string]any:
		for key := range typed {
			items = append(items, key)
		}
	case []any:
		for _, raw := range typed {
			if item, ok := raw.(string); ok {
				items = append(items, item)
			}
		}
	}
	slices.Sort(items)
	return dedupeStrings(items, 24)
}

func joinComposeCommand(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := []string{}
		for _, raw := range typed {
			if item, ok := raw.(string); ok {
				parts = append(parts, strings.TrimSpace(item))
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	default:
		return ""
	}
}

func stringSliceFromYAML(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{strings.TrimSpace(typed)}
	case []any:
		items := []string{}
		for _, raw := range typed {
			if item, ok := raw.(string); ok {
				items = append(items, strings.TrimSpace(item))
			}
		}
		return items
	default:
		return []string{}
	}
}

func normalizeComposePath(manifestPath string, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(trimmed))))
}

func normalizeComposePaths(manifestPath string, values []string) []string {
	items := []string{}
	manifestDir := filepath.Dir(manifestPath)
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		items = append(items, filepath.ToSlash(filepath.Clean(filepath.Join(manifestDir, trimmed))))
	}
	return dedupeStrings(items, 12)
}

func packageScriptNameFromCommand(command string) (string, bool) {
	pattern := regexp.MustCompile(`npm\s+run\s+([A-Za-z0-9:_-]+)`)
	match := pattern.FindStringSubmatch(command)
	if len(match) < 2 {
		return "", false
	}
	return match[1], true
}

func makeTargetNameFromCommand(command string) (string, bool) {
	pattern := regexp.MustCompile(`^make\s+([A-Za-z0-9_.-]+)$`)
	match := pattern.FindStringSubmatch(strings.TrimSpace(command))
	if len(match) < 2 {
		return "", false
	}
	return match[1], true
}

func splitShellWorkingDir(command string) (string, string) {
	pattern := regexp.MustCompile(`^cd\s+([^&]+?)\s+&&\s+(.+)$`)
	match := pattern.FindStringSubmatch(strings.TrimSpace(command))
	if len(match) < 3 {
		return "", command
	}
	return filepath.ToSlash(strings.TrimSpace(match[1])), strings.TrimSpace(match[2])
}

func extractFlagValue(command string, flag string) (string, bool) {
	pattern := regexp.MustCompile(regexp.QuoteMeta(flag) + `(?:=|\s+)(\d+)`)
	match := pattern.FindStringSubmatch(command)
	if len(match) < 2 {
		return "", false
	}
	if _, err := strconv.Atoi(match[1]); err != nil {
		return "", false
	}
	return match[1], true
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
				nil,
				nil,
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

func newProjectInfoProvenance(sourceKind string, sourceFile *string, command *string, cwd *string, entryPath *string, declaredCommand *string, serviceName *string, configFiles []string, configHints []string, evidenceType string, evidencePaths []string, profileID *string, confidence *int, reason *string) ProjectInfoProvenance {
	return ProjectInfoProvenance{
		SourceKind:      sourceKind,
		SourceFile:      sourceFile,
		Command:         command,
		Cwd:             cwd,
		EntryPath:       entryPath,
		DeclaredCommand: declaredCommand,
		ServiceName:     serviceName,
		ConfigFiles:     dedupeStrings(configFiles, 12),
		ConfigHints:     dedupeStrings(configHints, 12),
		EvidenceType:    evidenceType,
		Evidence:        projectEvidenceRefs(evidencePaths),
		ProfileID:       profileID,
		Confidence:      confidence,
		Reason:          reason,
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

func firstNonEmptyProjectString(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return strings.TrimSpace(item)
		}
	}
	return ""
}
