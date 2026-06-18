package workspaceops

import "strings"

// Guided guidance customization: turn a starter guidance file (AGENTS.md /
// CONVENTIONS.md / microagent repo.md) into a repo-specific one by walking the
// operator through the sections worth filling in, with prompts and examples.

type GuidanceCustomizationSuggestion struct {
	Section string `json:"section"`
	Prompt  string `json:"prompt"`
	Example string `json:"example"`
}

type GuidanceCustomization struct {
	WorkspaceID string                            `json:"workspace_id"`
	Kind        string                            `json:"kind"`
	Suggestions []GuidanceCustomizationSuggestion `json:"suggestions"`
	GeneratedAt string                            `json:"generated_at"`
}

func guidanceSections() []GuidanceCustomizationSuggestion {
	return []GuidanceCustomizationSuggestion{
		{Section: "Overview", Prompt: "What is this repository for, in one or two sentences?", Example: "xMustard is a local bug-operations system: scan repos, track issues, run agents, verify fixes."},
		{Section: "Build & run", Prompt: "How does an agent build and start the project?", Example: "make backend (Go API on :8042); cd frontend && npm run dev."},
		{Section: "Test", Prompt: "What are the canonical test/lint commands?", Example: "cd api-go && go test ./...; cd rust-core && cargo test; cd frontend && npm run lint."},
		{Section: "Architecture", Prompt: "What are the main components and their boundaries?", Example: "Go api-go owns HTTP; rust-core owns scanning/verification/goals; React frontend."},
		{Section: "Conventions", Prompt: "What coding/PR conventions must changes follow?", Example: "Keep backend and frontend contracts in sync; prefer durable artifacts over chat-only behavior."},
		{Section: "Gotchas", Prompt: "What traps or non-obvious rules should an agent know?", Example: "docs/ is gitignored but many files are tracked; pass an absolute data dir to the Rust CLI."},
	}
}

// BuildGuidanceCustomization returns the section-by-section customization guide
// for a starter file kind.
func BuildGuidanceCustomization(dataDir, workspaceID, kind string) (*GuidanceCustomization, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	normalized := strings.TrimSpace(kind)
	if normalized == "" {
		normalized = "AGENTS.md"
	}
	return &GuidanceCustomization{
		WorkspaceID: workspaceID,
		Kind:        normalized,
		Suggestions: guidanceSections(),
		GeneratedAt: nowUTC(),
	}, nil
}
