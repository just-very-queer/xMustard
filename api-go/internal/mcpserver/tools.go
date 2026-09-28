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

// toolNames lists the tools in their advertised order.
func toolNames() []string {
	names := make([]string, 0, len(Tools()))
	for _, t := range Tools() {
		names = append(names, t.Name)
	}
	return names
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

// boundedResultChars is the result size up to which a read tool asks to be delivered
// inline (_meta["anthropic/maxResultSizeChars"]); a larger result is written to a
// file the agent can read, and the original is always paged through resources/read.
// Projections aim at the 64 KiB evidence target and grow past it only to keep failure
// evidence: the largest measured is impact with no arguments on a 1,929-file
// repository, a 2 MiB original projected to 100,671 bytes. Twice the target plus the
// recovery note covers that (characters never exceed bytes) and stays well inside
// Claude Code's 500,000-character ceiling for this annotation; declaring the 1 MiB
// projection hard cap would claim more than the client honors and let ~125k-token
// results into context.
const (
	projectionTarget   = 64 << 10 // evidence.DefaultProjectionTarget
	recoveryNoteBytes  = 1 << 10
	boundedResultChars = 2*projectionTarget + recoveryNoteBytes
	// claudeCodeResultCeiling is the largest maxResultSizeChars Claude Code honors.
	claudeCodeResultCeiling = 500_000
	// measuredLargestProjection is the largest projection measured so far (impact).
	measuredLargestProjection = 100_671
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
