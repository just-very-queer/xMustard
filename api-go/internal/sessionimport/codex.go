package sessionimport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// codex reads Codex CLI rollouts: <root>/YYYY/MM/DD/rollout-*.jsonl. A line is
// {timestamp, type, payload}; session_meta carries the id and cwd, response_item the
// messages and tool calls.
type codex struct{}

func (codex) Name() string { return "codex" }

type codexLine struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   struct {
		ID        string          `json:"id"`
		Cwd       string          `json:"cwd"`
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	} `json:"payload"`
}

func (c codex) Walk(ctx context.Context, opts Options, sink Sink) error {
	root, err := storeDir(opts, ".codex", "sessions")
	if err != nil {
		return err
	}
	files, err := jsonlFiles(root)
	if err != nil {
		return err
	}
	for _, path := range files {
		em := newEmitter(sink, opts, Session{Source: c.Name(), ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")})
		if err := walkJSONL(ctx, path, em, codexEvent); err != nil {
			return err
		}
	}
	return nil
}

// codexPayloads say how each payload type becomes a role and text.
var codexPayloads = map[string]func(p *codexLine) (role, text string){
	"message": func(p *codexLine) (string, string) {
		t, _ := contentText(p.Payload.Content)
		return roleOf(p.Payload.Role, false), t
	},
	"function_call": func(p *codexLine) (string, string) {
		return "tool", "tool " + p.Payload.Name + ": " + rawText(p.Payload.Arguments)
	},
	"function_call_output": func(p *codexLine) (string, string) { return "tool", rawText(p.Payload.Output) },
}

func codexEvent(n int, raw []byte, em *emitter) error {
	var l codexLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	if l.Type == "session_meta" {
		em.s.ID, em.s.Project, em.s.Start = nonEmpty(l.Payload.ID, em.s.ID), l.Payload.Cwd, parseTime(l.Timestamp)
		return nil
	}
	build, ok := codexPayloads[l.Payload.Type]
	if !ok || l.Type != "response_item" {
		return nil
	}
	role, text := build(&l)
	if role == "" {
		return nil
	}
	return em.add(n, parseTime(l.Timestamp), role, text)
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
