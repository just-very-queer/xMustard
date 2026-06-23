package workspaceops

import (
	"fmt"
	"io"
	"strings"
)

// GuidanceStarterResult is the response to POST /api/workspaces/{id}/guidance/starters.
// Matches the frontend GuidanceStarterResult contract.
type GuidanceStarterResult struct {
	WorkspaceID string `json:"workspace_id"`
	TemplateID  string `json:"template_id"`
	Path        string `json:"path"`
	Created     bool   `json:"created"`
	Overwritten bool   `json:"overwritten"`
	GeneratedAt string `json:"generated_at"`
}

// guidanceStarterTemplate returns the DETERMINISTIC starter content for a template id
// (no timestamps/randomness inside the body, so regenerating is byte-stable). Each body
// carries the guidancePlaceholderMarker so guidance-health flags it as "needs
// customization" until an operator fills it in.
func guidanceStarterTemplate(templateID string) (string, bool) {
	switch templateID {
	case "agents":
		return "# AGENTS.md\n\n" +
			"Always-on instructions for coding agents working in this repository.\n\n" +
			"## Build & test\n" +
			"- " + guidancePlaceholderMarker + ": how to build, and the exact command to run the tests.\n\n" +
			"## Conventions\n" +
			"- " + guidancePlaceholderMarker + ": the style/structure rules an agent must follow.\n\n" +
			"## Gotchas\n" +
			"- " + guidancePlaceholderMarker + ": the non-obvious traps in this codebase.\n", true
	case "openhands_repo":
		return "# Repo microagent\n\n" +
			"Short, repo-scoped guidance for planning and execution flows.\n\n" +
			"## What this repo is\n" +
			"- " + guidancePlaceholderMarker + ": one-paragraph description of the project.\n\n" +
			"## How to verify a change\n" +
			"- " + guidancePlaceholderMarker + ": the build + test commands that gate a change.\n", true
	case "conventions":
		return "# CONVENTIONS.md\n\n" +
			"Shared engineering conventions for style, structure, and review defaults.\n\n" +
			"## Code style\n" +
			"- " + guidancePlaceholderMarker + ": formatting/linting expectations.\n\n" +
			"## Review defaults\n" +
			"- " + guidancePlaceholderMarker + ": what a reviewer checks before approving.\n", true
	default:
		return "", false
	}
}

// GenerateGuidanceStarter writes a deterministic starter guidance file into the workspace
// repo, confined by the no-follow create opener (a symlink planted at the target path is
// refused, never followed — so this can't become an arbitrary host-file write). With
// overwrite=false an existing file is left untouched (created=false). Replaces the dead
// frontend path that previously 404'd.
func GenerateGuidanceStarter(dataDir, workspaceID, templateID string, overwrite bool) (*GuidanceStarterResult, error) {
	if err := validateSafeID("workspace", workspaceID); err != nil {
		return nil, err
	}
	spec, ok := starterSpecForTemplate(templateID)
	if !ok {
		return nil, Invalid(fmt.Sprintf("unknown guidance template %q (want agents|openhands_repo|conventions)", templateID))
	}
	content, ok := guidanceStarterTemplate(templateID)
	if !ok {
		return nil, Invalid(fmt.Sprintf("no template body for %q", templateID))
	}
	root := contextRoot(dataDir, workspaceID)
	if strings.TrimSpace(root) == "" {
		return nil, Unavailable("workspace repo root is not resolvable; load the workspace first")
	}

	f, existed, err := createWorkspaceFileBeneath(root, spec.Path, overwrite)
	if err != nil {
		return nil, err
	}
	result := &GuidanceStarterResult{
		WorkspaceID: workspaceID,
		TemplateID:  templateID,
		Path:        spec.Path,
		GeneratedAt: nowUTC(),
	}
	if f == nil {
		// existed and overwrite=false: idempotent no-op (already present).
		result.Created = false
		result.Overwritten = false
		return result, nil
	}
	defer f.Close()
	if _, err := io.WriteString(f, content); err != nil {
		return nil, err
	}
	result.Created = !existed
	result.Overwritten = existed
	return result, nil
}

func starterSpecForTemplate(templateID string) (guidanceStarterSpec, bool) {
	for _, s := range guidanceStarterSpecs() {
		if s.TemplateID == templateID {
			return s, true
		}
	}
	return guidanceStarterSpec{}, false
}
