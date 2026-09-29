package sessionimport

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// pi reads Pi's sessions: <root>/<project>/<time>_<id>.jsonl. The first line is
// {type: "session", id, cwd}; later lines are {type: "message", message: {role, content}}.
type pi struct{}

func (pi) Name() string { return "pi" }

type piLine struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Timestamp json.RawMessage `json:"timestamp"`
	Message   struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Timestamp json.RawMessage `json:"timestamp"`
	} `json:"message"`
}

func (p pi) Walk(ctx context.Context, opts Options, sink Sink) error {
	root, err := storeDir(opts, ".pi", "agent", "sessions")
	if err != nil {
		return err
	}
	files, err := jsonlFiles(root)
	if err != nil {
		return err
	}
	for _, path := range files {
		em := newEmitter(sink, opts, Session{Source: p.Name(), ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")})
		if err := walkJSONL(ctx, path, em, piEvent); err != nil {
			return err
		}
	}
	return nil
}

func piEvent(n int, raw []byte, em *emitter) error {
	var l piLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	switch l.Type {
	case "session":
		em.s.ID, em.s.Project, em.s.Start = nonEmpty(l.ID, em.s.ID), l.Cwd, parseTime(l.Timestamp)
	case "message":
		text, toolOnly := contentText(l.Message.Content, em.tool)
		if role := roleOf(l.Message.Role, toolOnly); role != "" {
			return em.add(n, parseTime(l.Timestamp), role, text)
		}
	}
	return nil
}
