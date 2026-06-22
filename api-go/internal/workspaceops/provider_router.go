package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
)

// Task-typed model routing. The provider layer (openai_providers.go) gives access
// to many OpenAI-compatible models; this picks WHICH one for a given request based
// on the coding sub-task. Routing is two steps:
//   1. classify the request into a coding sub-task type (+ a model class),
//   2. resolve that to a concrete provider+model via explicit rules, else by
//      provider capability (vision → a supports_vision provider, code → a
//      coder model, etc.).
// The taxonomy is grounded in agentic-coding research (SWE-agent ACI's locate-vs-edit
// split, Agentless locate→patch, RepoGraph navigation, CodeRAG-Bench retrieval).

type ModelRoute struct {
	TaskType   string `json:"task_type"`
	ModelClass string `json:"model_class"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Reason     string `json:"reason"`
}

type RouteRequest struct {
	Prompt   string `json:"prompt"`
	HasImage bool   `json:"has_image"`
	TaskHint string `json:"task_hint"` // optional explicit task_type override
}

// RoutingRule pins a task type to a specific provider+model (overrides heuristics).
type RoutingRule struct {
	TaskType string `json:"task_type"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// taskType -> model class. Order matters for classification priority.
var taskClasses = []struct {
	taskType   string
	modelClass string
	keywords   []string
}{
	{"vision_ui_diagnose", "vision", nil}, // selected by HasImage, not keywords
	{"test_gen_validate", "code-specialized", []string{"unit test", "write test", "add test", "test coverage", "testcase", "test case"}},
	{"multi_step_debug_reason", "large-reasoning", []string{"debug", "root cause", "why does", "why is", "investigate", "diagnose", "reproduce the bug", "stack trace"}},
	{"code_edit_patch", "code-specialized", []string{"implement", "refactor", "fix ", "patch", "rename", "add a ", "modify", "write a function", "edit "}},
	{"locate", "small-fast", []string{"where is", "which file", "find the", "who calls", "locate", "look up", "search for"}},
	{"repo_qa_explain", "small-fast", []string{"explain", "what does", "summarize", "how does", "describe", "what is"}},
}

// ClassifyTask maps a request to (taskType, modelClass). HasImage wins; then an
// explicit hint; then keyword intent; default repo_qa_explain (small-fast).
func ClassifyTask(prompt string, hasImage bool, hint string) (string, string) {
	if hasImage {
		return "vision_ui_diagnose", "vision"
	}
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint != "" {
		for _, tc := range taskClasses {
			if tc.taskType == hint {
				return tc.taskType, tc.modelClass
			}
		}
	}
	lower := strings.ToLower(prompt)
	for _, tc := range taskClasses {
		for _, kw := range tc.keywords {
			if strings.Contains(lower, kw) {
				return tc.taskType, tc.modelClass
			}
		}
	}
	return "repo_qa_explain", "small-fast"
}

func routesPath(dataDir string) string { return filepath.Join(dataDir, "model_routes.json") }

func loadRoutingRules(dataDir string) ([]RoutingRule, error) {
	var rules []RoutingRule
	if err := readJSON(routesPath(dataDir), &rules); err != nil {
		if os.IsNotExist(err) {
			return []RoutingRule{}, nil
		}
		return nil, err
	}
	return rules, nil
}

// ListModelRoutes returns the configured task→model rules.
func ListModelRoutes(dataDir string) ([]RoutingRule, error) { return loadRoutingRules(dataDir) }

// SetModelRoute upserts a task→provider/model rule.
func SetModelRoute(dataDir string, rule RoutingRule) ([]RoutingRule, error) {
	rule.TaskType = strings.TrimSpace(rule.TaskType)
	rules, err := loadRoutingRules(dataDir)
	if err != nil {
		return nil, err
	}
	next := make([]RoutingRule, 0, len(rules)+1)
	for _, r := range rules {
		if r.TaskType != rule.TaskType {
			next = append(next, r)
		}
	}
	next = append(next, rule)
	if err := writeJSON(routesPath(dataDir), next); err != nil {
		return nil, err
	}
	return next, nil
}

// modelLooksLike reports whether a model id hints at a capability class.
func modelLooksLike(model, class string) bool {
	m := strings.ToLower(model)
	switch class {
	case "code-specialized":
		return strings.Contains(m, "coder") || strings.Contains(m, "code")
	case "vision":
		return strings.Contains(m, "vision") || strings.Contains(m, "-vl") || strings.Contains(m, "llava")
	case "large-reasoning":
		return strings.Contains(m, "70b") || strings.Contains(m, "thinking") || strings.Contains(m, "reason") || strings.Contains(m, "opus")
	case "small-fast":
		return strings.Contains(m, "mini") || strings.Contains(m, "small") || strings.Contains(m, "1b") || strings.Contains(m, "3b") || strings.Contains(m, "haiku")
	}
	return false
}

// RouteModel classifies the request and resolves it to a provider+model.
func RouteModel(dataDir string, req RouteRequest) (*ModelRoute, error) {
	taskType, modelClass := ClassifyTask(req.Prompt, req.HasImage, req.TaskHint)
	route := &ModelRoute{TaskType: taskType, ModelClass: modelClass}

	// 1) explicit rule for this task type wins.
	rules, err := loadRoutingRules(dataDir)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if r.TaskType == taskType && strings.TrimSpace(r.Provider) != "" {
			route.Provider = r.Provider
			route.Model = r.Model
			route.Reason = "explicit routing rule for " + taskType
			return route, nil
		}
	}

	// 2) capability-based selection over configured providers.
	providers, err := loadProviders(dataDir)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		route.Reason = "no providers configured — classification only"
		return route, nil
	}

	// vision tasks need a vision-capable provider.
	if modelClass == "vision" {
		for _, p := range providers {
			if p.SupportsVision {
				route.Provider = p.Name
				route.Model = pickModelForClass(p, modelClass)
				route.Reason = "vision task → supports_vision provider " + p.Name
				return route, nil
			}
		}
		// fall through: no vision provider — degrade to best available
		route.Reason = "vision task but no supports_vision provider; "
	}

	// prefer a provider whose default_model name matches the class.
	for _, p := range providers {
		if p.DefaultModel != "" && modelLooksLike(p.DefaultModel, modelClass) {
			route.Provider = p.Name
			route.Model = p.DefaultModel
			route.Reason += "matched " + modelClass + " by model name on provider " + p.Name
			return route, nil
		}
	}
	// otherwise first provider with a default model.
	for _, p := range providers {
		if p.DefaultModel != "" {
			route.Provider = p.Name
			route.Model = p.DefaultModel
			route.Reason += "default model on provider " + p.Name
			return route, nil
		}
	}
	// last resort: first provider, model left to caller/provider default.
	route.Provider = providers[0].Name
	route.Reason += "first configured provider " + providers[0].Name
	return route, nil
}

func pickModelForClass(p OpenAIProvider, class string) string {
	if p.DefaultModel != "" {
		return p.DefaultModel
	}
	_ = class
	return ""
}

// RouteAndChat routes the request to a model, then executes the chat through that
// provider — the end-to-end "ask the right local/remote model" path.
func RouteAndChat(dataDir string, req RouteRequest, images []string) (map[string]any, error) {
	route, err := RouteModel(dataDir, req)
	if err != nil {
		return nil, err
	}
	if route.Provider == "" {
		return map[string]any{"route": route, "executed": false, "reason": "no provider available to execute"}, nil
	}
	chat, err := OpenAIChat(dataDir, route.Provider, ChatRequest{
		Model:     route.Model,
		Prompt:    req.Prompt,
		ImageURLs: images,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"route": route, "executed": true, "result": chat}, nil
}
