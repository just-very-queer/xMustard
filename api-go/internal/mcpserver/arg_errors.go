package mcpserver

import (
	"fmt"
	"strings"

	"xmustard/api-go/internal/toolcompat"
)

// Argument errors are tool results (MCP 2025-11-25, tools, Error Handling: input
// validation errors are tool execution errors a model can correct), not JSON-RPC
// errors: clients show a JSON-RPC error to the model as an empty or generic failure,
// and an agent then guesses at the cause. The result says what was wrong, what the
// tool takes and, for a known confusion, which call does what the agent was after.

// argErrorResult answers a rejected argument: the error, a hint when argHints has one,
// and the tool's arguments; _meta["xmustard/argument_error"] carries the same facts
// for clients that read them.
func argErrorResult(t *Tool, e *ArgError) map[string]any {
	lines := []string{e.Message + "."}
	data := e.data()
	if hint, ok := argHints[hintKey{t.Name, toolcompat.Token(e.Argument)}]; ok {
		lines = append(lines, "Hint: "+hint+".")
		data["hint"] = hint
	}
	lines = append(lines, t.usage())
	res := TextResult(strings.Join(lines, "\n"), true)
	res["_meta"] = map[string]any{"xmustard/argument_error": data}
	return res
}

// usage lists what t accepts: the listed arguments with their bounds, then the
// Advanced ones by name (documented at DocsURI). Hidden aliases stay unlisted.
func (t *Tool) usage() string {
	listed := make([]string, 0, len(t.Args))
	for _, a := range t.Args {
		listed = append(listed, a.Name+argBounds(a))
	}
	s := fmt.Sprintf("Arguments of %s: %s.", t.Name, strings.Join(listed, ", "))
	if len(t.Advanced) == 0 {
		return s
	}
	advanced := make([]string, 0, len(t.Advanced))
	for _, a := range t.Advanced {
		advanced = append(advanced, a.Name)
	}
	return s + fmt.Sprintf(" Also accepted (see %s): %s.", DocsURI, strings.Join(advanced, ", "))
}

// argBounds is the short shape an argument error lists: required, enum values, the
// integer range.
func argBounds(a Arg) string {
	var b []string
	if a.Required {
		b = append(b, "required")
	}
	if len(a.Enum) > 0 {
		b = append(b, strings.Join(a.Enum, "|"))
	}
	if a.Type == typeInteger {
		b = append(b, fmt.Sprintf("%d-%d", a.Min, a.Max))
	}
	if len(b) == 0 {
		return ""
	}
	return " (" + strings.Join(b, ", ") + ")"
}

type hintKey struct{ tool, token string }

// argHints point an argument a tool does not take at the call that does what the
// agent was after. A misspelling of an accepted argument needs no row: toolcompat
// repairs it on a read tool and names the canonical spelling on a write tool.
// Arguments are matched by toolcompat.Token, so "filePath" matches "file_path".
var argHints = func() map[hintKey]string {
	rows := []struct {
		tool string
		args []string
		hint string
	}{
		{"explain", []string{"symbol", "name", "function", "q", "query"},
			"explain takes a file or directory path; for a symbol, search(q=...) finds it and impact(symbol=...) gives its blast radius"},
		{"impact", []string{"q", "query", "text"},
			"impact takes symbol=, path= (a file), from= and to=, or no arguments for the current changes; search(q=...) finds a name"},
		{"impact", []string{"paths", "files"},
			"impact takes one file as path=; call it once per file"},
		{"search", []string{"path", "paths", "file", "files", "dir", "directory"},
			"search narrows files with path_glob= (a glob, ** crosses /); explain(path=...) describes one file and impact(path=...) lists what references it"},
		{"search", []string{"symbol"},
			"search takes q=; seed= ranks results near a symbol, and impact(symbol=...) gives its blast radius"},
		{"ground", []string{"q", "query", "path", "paths"},
			"ground takes no query or path; recall(q=..., paths=...) ranks memory and search(q=...) finds code"},
		{"diagnostics", []string{"path", "paths", "file"},
			"diagnostics covers the whole workspace and takes no path"},
		{"why_failed", []string{"output", "stdout", "stderr", "error", "text"},
			"pass failing output as log= (see " + DocsURI + ")"},
	}
	m := map[hintKey]string{}
	for _, r := range rows {
		for _, a := range r.args {
			m[hintKey{r.tool, toolcompat.Token(a)}] = r.hint
		}
	}
	return m
}()
