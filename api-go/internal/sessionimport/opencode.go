package sessionimport

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// plainID is what an OpenCode session or message id must look like before it is joined
// into a path: the ids are untrusted JSON, and one such as ../../x or with glob
// metacharacters would read files outside the store root.
var plainID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// opencode reads OpenCode's JSON storage: <root>/session/<project>/<id>.json (with the
// directory and title), <root>/message/<session>/<id>.json and <root>/part/<message>/
// <id>.json (the text and tool parts). One small file is read at a time.
type opencode struct{}

func (opencode) Name() string { return "opencode" }

type opencodeSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Time      struct {
		Created json.RawMessage `json:"created"`
	} `json:"time"`
}

type opencodeMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Time struct {
		Created json.RawMessage `json:"created"`
	} `json:"time"`
}

type opencodePart struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	State struct {
		Input  json.RawMessage `json:"input"`
		Output string          `json:"output"`
	} `json:"state"`
}

func (o opencode) Walk(ctx context.Context, opts Options, sink Sink) error {
	root, err := storeDir(opts, ".local", "share", "opencode", "storage")
	if err != nil {
		return err
	}
	sessions, _ := filepath.Glob(filepath.Join(root, "session", "*", "*.json"))
	sort.Strings(sessions)
	for _, path := range sessions {
		var s opencodeSession
		if !readJSON(path, &s) || !plainID.MatchString(s.ID) {
			continue
		}
		start := parseTime(s.Time.Created)
		em := newEmitter(sink, opts, Session{Source: o.Name(), ID: s.ID, Project: s.Directory, Title: s.Title, Start: start})
		if !inProject(opts.Project, s.Directory) {
			continue
		}
		if err := o.messages(ctx, root, s.ID, em); err != nil {
			return err
		}
		if err := em.finish(); err != nil {
			return err
		}
	}
	return nil
}

// messages emits a session's messages in creation order; the line is the message's
// position in that order, stable while messages are only appended.
func (o opencode) messages(ctx context.Context, root, session string, em *emitter) error {
	files, _ := filepath.Glob(filepath.Join(root, "message", session, "*.json"))
	sort.Strings(files) // ids sort by creation
	for i, path := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var m opencodeMessage
		if !readJSON(path, &m) || !plainID.MatchString(m.ID) {
			continue
		}
		role := roleOf(m.Role, false)
		if role == "" {
			continue
		}
		text := o.parts(root, m.ID, em)
		if err := em.add(i+1, parseTime(m.Time.Created), role, text); err != nil {
			return err
		}
	}
	return nil
}

// parts joins a message's text parts and tool calls, each clipped as it is read, and
// stops once the message's budget is spent, so a message with many large tool outputs
// never holds more than one part beyond MaxMessageBytes.
func (opencode) parts(root, message string, em *emitter) string {
	files, _ := filepath.Glob(filepath.Join(root, "part", message, "*.json"))
	sort.Strings(files)
	var out []string
	used := 0
	for _, path := range files {
		var p opencodePart
		if used >= MaxMessageBytes {
			break
		}
		if !readJSON(path, &p) {
			continue
		}
		var text string
		switch p.Type {
		case "text":
			text = em.clip(p.Text, MaxMessageBytes-used)
		case "tool":
			text = em.tool("tool " + p.Tool + ": " + rawText(p.State.Input, em.tool) + "\n" + em.tool(p.State.Output))
		}
		out, used = append(out, text), used+len(text)+1
	}
	return strings.Join(out, "\n")
}

// maxStoreFile bounds one OpenCode JSON file read.
const maxStoreFile = 8 << 20

func readJSON(path string, v any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, maxStoreFile)).Decode(v) == nil
}
