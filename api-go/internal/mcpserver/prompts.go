package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// MCP prompts (WS-31, PAR-HAR-11): two workflow prompts a client lists with
// prompts/list and fills with prompts/get. orient starts a task: the workflow and the
// standing memory of the session's workspace (so it doubles as the fallback for a
// client that drops initialize.instructions), then what to call first. pre_commit_check
// is the check before a commit. Neither calls a tool: each is text for the model.

// PromptArg is one prompt argument.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Prompt is one prompt and how its text is built from its arguments.
type Prompt struct {
	Name        string
	Description string
	Args        []PromptArg
	// Render builds the prompt's text; memory is the standing memory of the session's
	// workspace ("" when it has none).
	Render func(args map[string]string, memory string) string
	// Memory says the prompt carries the standing memory.
	Memory bool
}

// maxPromptArg bounds each prompt argument.
const maxPromptArg = 2000

var prompts = []Prompt{
	{
		Name: "orient",
		Description: "Orient in the workspace before a task: the workflow, the standing memory (core tier and deferred " +
			"index), and the first calls to make.",
		Args:   []PromptArg{{Name: "task", Description: "what you are about to do"}},
		Memory: true,
		Render: func(args map[string]string, memory string) string {
			task := args["task"]
			var b strings.Builder
			b.WriteString(Instructions + "\n\n")
			if memory != "" {
				b.WriteString(memory + "\n\n")
			}
			b.WriteString("Start now: call ground; ")
			if task != "" {
				fmt.Fprintf(&b, "then recall with q=%q and the paths you expect to touch; ", task)
			} else {
				b.WriteString("then recall with your task and the paths you expect to touch; ")
			}
			b.WriteString("then search or explain the code before you change it, and run impact on each symbol you will edit.")
			return b.String()
		},
	},
	{
		Name:        "pre_commit_check",
		Description: "Check a change before committing it: blast radius, diagnostics, stale memory, and what to remember.",
		Args:        []PromptArg{{Name: "paths", Description: "comma-separated repo-relative files the change touches"}},
		Render: func(args map[string]string, _ string) string {
			scope := "the change"
			paths := ""
			if p := strings.TrimSpace(args["paths"]); p != "" {
				scope, paths = "the change to "+p, p
			}
			recall := "recall with the paths the change touches"
			if paths != "" {
				recall = fmt.Sprintf("recall with paths=%q", paths)
			}
			return "Before committing " + scope + ", run these checks and report each result:\n" +
				"1. ground with sections=index,memory: changed files, contract breaks (changed signatures) and stale memory.\n" +
				"2. impact on each changed symbol, or path=<file> for a file: callers and dependents to update; distance>=1 edges are leads to confirm.\n" +
				"3. diagnostics for the changed files, and why_failed on any failed run.\n" +
				"4. " + recall + ": memory about these files, stale flags included; edit or retire what the change made wrong.\n" +
				"5. remember what the change established that others need, with its paths.\n" +
				"Do not commit while a check fails or a contract break is unexplained."
		},
	},
}

func promptByName(name string) (Prompt, bool) {
	for _, p := range prompts {
		if p.Name == name {
			return p, true
		}
	}
	return Prompt{}, false
}

// promptsList answers prompts/list.
func promptsList() map[string]any {
	list := make([]map[string]any, 0, len(prompts))
	for _, p := range prompts {
		list = append(list, map[string]any{"name": p.Name, "description": p.Description, "arguments": p.Args})
	}
	return map[string]any{"prompts": list}
}

// getPrompt answers prompts/get. An unknown prompt, a missing required argument, an
// argument the prompt does not take and an oversized one are invalid params.
func (s *Session) getPrompt(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	var req struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	p, ok := promptByName(req.Name)
	if !ok {
		names := make([]string, len(prompts))
		for i, q := range prompts {
			names[i] = q.Name
		}
		return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("unknown prompt %q; use: %s", req.Name, strings.Join(names, ", "))}
	}
	for name, v := range req.Arguments {
		switch {
		case !promptTakes(p, name):
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("prompt %s takes no argument %q", p.Name, name)}
		case len(v) > maxPromptArg:
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("argument %q is longer than %d bytes", name, maxPromptArg)}
		}
	}
	for _, a := range p.Args {
		if a.Required && strings.TrimSpace(req.Arguments[a.Name]) == "" {
			return nil, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("prompt %s needs argument %q", p.Name, a.Name)}
		}
	}
	memory := ""
	if p.Memory {
		memory = s.promptMemory(ctx)
	}
	return map[string]any{
		"description": p.Description,
		"messages":    []map[string]any{{"role": "user", "content": map[string]any{"type": "text", "text": p.Render(req.Arguments, memory)}}},
	}, nil
}

func promptTakes(p Prompt, name string) bool {
	return slices.ContainsFunc(p.Args, func(a PromptArg) bool { return a.Name == name })
}

// promptMemory is the standing memory a prompt carries: the projection of the workspace
// a tool call would use, "" when it cannot be read.
func (s *Session) promptMemory(ctx context.Context) string {
	ws, err := s.resolveWorkspace(ctx, &Tool{Name: "prompt"}, map[string]string{})
	if err != nil {
		return ""
	}
	p, _, err := s.readProjection(ctx, ws.ID, instructionsMemoryChars)
	if err != nil || p == nil {
		return ""
	}
	return strings.TrimSpace(p.Text)
}
