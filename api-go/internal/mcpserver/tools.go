package mcpserver

import (
	"net/url"
	"strings"
)

// Tools returns the nine agent-facing tools in their advertised order. The surface is
// deliberately small (docs/RETHINK.md): governed memory, grounding and narrow
// retrieval. Each tool lives in its own tool_<name>.go file.
func Tools() []*Tool {
	return []*Tool{groundTool, recallTool, rememberTool, verifyTool, searchTool, explainTool, impactTool, diagnosticsTool, whyFailedTool}
}

// ToolByName returns the named tool. Hidden aliases name arguments, never tools.
func ToolByName(name string) (*Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}

// workspaceArg is optional on every tool: an omitted id resolves from the
// environment, the client's roots or the working directory (resolve_workspace.go).
var workspaceArg = Arg{Name: "workspace_id", Type: typeString, Desc: "workspace id; auto-resolved if omitted"}

// boundedResultChars is the largest result a read tool delivers inline: the evidence
// projection's hard cap (1 MiB; characters never exceed bytes) plus the recovery
// note. Projections aim at 64 KiB but may grow toward the cap to keep failure evidence
// (measured: impact with no arguments on a 1,929-file repository projected a 2 MiB
// original to 100,671 bytes). Originals are paged through resources/read.
const (
	projectionHardCap  = 1 << 20 // evidence.DefaultMaxProjection
	recoveryNoteBytes  = 1 << 10
	boundedResultChars = projectionHardCap + recoveryNoteBytes
)

func wsPath(args map[string]string, suffix string) string {
	return "/api/workspaces/" + url.PathEscape(args["workspace_id"]) + suffix
}

// splitCSV turns a comma-separated arg ("a.go, b.go") into a trimmed, non-empty
// slice for JSON-body fields like `paths`.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// query appends k=v pairs whose value is non-empty, in order, to path.
func query(path string, kv ...string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		path += sep + kv[i] + "=" + url.QueryEscape(kv[i+1])
		sep = "&"
	}
	return path
}
