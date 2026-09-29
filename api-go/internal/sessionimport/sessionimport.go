// Package sessionimport streams the session stores of other coding agents (Claude Code,
// Codex, OpenCode, Pi) into normalized messages (WS-34; PAR-ADP-12). Every store is read
// one line or file at a time through a bounded buffer, so a large store costs a fixed
// amount of memory; every message is redacted (PAR-SEC-04) and scanned for instruction
// patterns (WS-56) before a sink sees it. The text is untrusted data: it is what an agent
// once said or saw, never a verified fact.
package sessionimport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xmustard/api-go/internal/injection"
	"xmustard/api-go/internal/redact"
)

// Bounds of an import.
const (
	MaxLineBytes    = 4 << 20 // a longer JSONL line is skipped, not buffered
	MaxMessageBytes = 8 << 10 // text kept per message
	MaxToolBytes    = 2 << 10 // text kept per tool call or result
	MaxMessages     = 20000   // messages kept per session
)

// Options select what to read.
type Options struct {
	// Dir is the store root; empty reads the source's default location under the home directory.
	Dir string
	// Project keeps the sessions whose working directory is this path or under it.
	Project string
	// Since drops messages older than this.
	Since time.Time
}

// Session is one imported conversation.
type Session struct {
	Source  string // claude-code | codex | opencode | pi
	ID      string
	Project string
	Title   string
	Branch  string
	Start   time.Time
}

// Message is one normalized, redacted message. Line orders it within its session.
type Message struct {
	Line       int
	At         time.Time
	Role       string // user | assistant | tool
	Text       string
	Redactions int
	// Flags are the instruction patterns the text matches (empty when clean).
	Flags []string
}

// Sink receives a store's sessions. Begin returns the last message line it already
// holds for the session, so a repeated import appends only what is new; End reports how
// many messages the session had beyond MaxMessages.
type Sink interface {
	Begin(s Session) (after int, err error)
	Message(m Message) error
	End(s Session, dropped int) error
}

// Source reads one agent's store.
type Source interface {
	Name() string
	Walk(ctx context.Context, opts Options, sink Sink) error
}

// Sources are the readers by the name --source takes.
var Sources = map[string]Source{
	"claude": claude{}, "codex": codex{}, "opencode": opencode{}, "pi": pi{},
}

// Names lists the source names in order.
func Names() []string {
	out := make([]string, 0, len(Sources))
	for n := range Sources {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// errSkipSession ends a file's read: the session is outside the project.
var errSkipSession = errors.New("session outside the project")

// emitter turns one session's raw messages into sink calls: the project filter, the
// resume point, the bounds, redaction and the injection scan live here once.
type emitter struct {
	sink    Sink
	opts    Options
	s       Session
	began   bool
	after   int
	kept    int
	dropped int
}

func newEmitter(sink Sink, opts Options, s Session) *emitter {
	return &emitter{sink: sink, opts: opts, s: s}
}

// inProject reports whether dir is project or below it. An empty project matches all.
func inProject(project, dir string) bool {
	if project == "" {
		return true
	}
	project, dir = filepath.Clean(project), filepath.Clean(dir)
	return dir == project || strings.HasPrefix(dir, strings.TrimRight(project, string(filepath.Separator))+string(filepath.Separator))
}

// roleLimits bounds a message's kept text by role.
var roleLimits = map[string]int{"tool": MaxToolBytes}

func (e *emitter) add(line int, at time.Time, role, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || !e.opts.Since.IsZero() && !at.IsZero() && at.Before(e.opts.Since) {
		return nil
	}
	if !e.began {
		if !inProject(e.opts.Project, e.s.Project) {
			return errSkipSession
		}
		var err error
		if e.after, err = e.sink.Begin(e.s); err != nil {
			return err
		}
		e.began = true
	}
	if line <= e.after {
		return nil
	}
	if e.kept == MaxMessages {
		e.dropped++
		return nil
	}
	e.kept++
	limit := MaxMessageBytes
	if l, ok := roleLimits[role]; ok {
		limit = l
	}
	text, rep := redact.Default().String(cut(text, limit))
	return e.sink.Message(Message{Line: line, At: at, Role: role, Text: text, Redactions: rep.Count,
		Flags: injection.Scan(text).Flags})
}

func (e *emitter) finish() error {
	if !e.began {
		return nil
	}
	return e.sink.End(e.s, e.dropped)
}

// cut cuts s to at most n bytes on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// eachLine calls fn for every line of r with its 1-based number. A line longer than
// MaxLineBytes is skipped (fn is not called), so memory stays bounded whatever the file.
func eachLine(r io.Reader, fn func(n int, line []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	over := false
	for n := 1; ; {
		chunk, prefix, err := br.ReadLine()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if !over && len(line)+len(chunk) > MaxLineBytes {
			over = true
		}
		if !over {
			line = append(line, chunk...)
		}
		if prefix {
			continue
		}
		if !over && len(line) > 0 {
			if err := fn(n, line); err != nil {
				return err
			}
		}
		if cap(line) > 1<<20 { // do not keep a big buffer for the rest of the file
			line = nil
		}
		line, over, n = line[:0], false, n+1
	}
}

// walkJSONL runs each: it reads one .jsonl file's lines into an emitter.
func walkJSONL(ctx context.Context, path string, em *emitter, line func(n int, raw []byte, em *emitter) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	err = eachLine(f, func(n int, raw []byte) error {
		if n%256 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		return line(n, raw, em)
	})
	if errors.Is(err, errSkipSession) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return em.finish()
}

// jsonlFiles lists the .jsonl files under root (to the given depth), oldest name first.
func jsonlFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		switch {
		case err != nil && p == root:
			return err
		case err != nil:
			return nil
		case !d.IsDir() && d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".jsonl"):
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// storeDir is the store root: Options.Dir, else home/rel.
func storeDir(opts Options, rel ...string) (string, error) {
	if opts.Dir != "" {
		return opts.Dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, rel...)...), nil
}

// parseTime reads an RFC 3339 string or a Unix millisecond number.
func parseTime(raw json.RawMessage) time.Time {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	var ms float64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 {
		return time.UnixMilli(int64(ms))
	}
	return time.Time{}
}

// block is one part of a message's content in the shapes the four agents write.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Arguments json.RawMessage `json:"arguments"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
}

// blockKinds maps a block type to what it contributes: "text" keeps its text, "call"
// the tool name and arguments, "result" its content. Others (thinking, images) add nothing.
var blockKinds = map[string]string{
	"text": "text", "input_text": "text", "output_text": "text",
	"tool_use": "call", "toolCall": "call", "function_call": "call",
	"tool_result": "result", "function_call_output": "result",
}

// contentText flattens message content (a string, or an array of blocks) into text and
// reports whether it held only tool calls and results.
func contentText(raw json.RawMessage) (text string, toolOnly bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var parts []string
	toolOnly = len(blocks) > 0
	for _, b := range blocks {
		switch blockKinds[b.Type] {
		case "text":
			parts, toolOnly = append(parts, b.Text), false
		case "call":
			parts = append(parts, "tool "+b.Name+": "+rawText(firstRaw(b.Input, b.Arguments)))
		case "result":
			parts = append(parts, rawText(firstRaw(b.Content, b.Output)))
		}
	}
	return strings.Join(parts, "\n"), toolOnly
}

func firstRaw(a, b json.RawMessage) json.RawMessage {
	if len(a) > 0 {
		return a
	}
	return b
}

// rawText is a JSON value as text: a string as is, an array of blocks flattened, any
// other value compacted.
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if t, _ := contentText(raw); t != "" {
		return t
	}
	return string(raw)
}

// roleOf normalizes an agent's role name: tool results are "tool".
func roleOf(role string, toolOnly bool) string {
	switch {
	case toolOnly || role == "toolResult" || role == "tool":
		return "tool"
	case role == "user" || role == "assistant":
		return role
	}
	return ""
}
