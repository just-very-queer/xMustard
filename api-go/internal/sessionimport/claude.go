package sessionimport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// claude reads Claude Code's per-project transcripts: <root>/<project>/<session>.jsonl,
// one JSON object per line with sessionId, cwd, gitBranch, timestamp and message.
type claude struct{}

func (claude) Name() string { return "claude-code" }

type claudeLine struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Branch    string          `json:"gitBranch"`
	Timestamp json.RawMessage `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (c claude) Walk(ctx context.Context, opts Options, sink Sink) error {
	root, err := storeDir(opts, ".claude", "projects")
	if err != nil {
		return err
	}
	files, err := jsonlFiles(root)
	if err != nil {
		return err
	}
	for _, path := range files {
		if strings.Contains(path, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			continue // a subagent's turns belong to its parent's transcript
		}
		em := newEmitter(sink, opts, Session{Source: c.Name(), ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")})
		if err := walkJSONL(ctx, path, em, claudeEvent); err != nil {
			return err
		}
	}
	return nil
}

func claudeEvent(n int, raw []byte, em *emitter) error {
	var l claudeLine
	if json.Unmarshal(raw, &l) != nil || l.Type != "user" && l.Type != "assistant" {
		return nil
	}
	if l.Cwd != "" {
		em.s.Project = l.Cwd
	}
	if l.Branch != "" {
		em.s.Branch = l.Branch
	}
	at := parseTime(l.Timestamp)
	if em.s.Start.IsZero() {
		em.s.Start = at
	}
	text, toolOnly := contentText(l.Message.Content, em.tool)
	role := roleOf(l.Message.Role, toolOnly)
	if role == "" {
		return nil
	}
	return em.add(n, at, role, text)
}
