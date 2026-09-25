package mcpserver

import "net/url"

var whyFailedTool = &Tool{
	Name:        "why_failed",
	Description: "Explain why a run failed: failure signals, salient error lines, and which changed files are implicated.",
	Args: []Arg{
		workspaceArg,
		{Name: "run_id", Type: typeString, Required: true, Desc: "the id of the run"},
	},
	Annotations: Annotations{Title: "Explain a failed run", ReadOnly: true},
	Output: map[string]string{
		"run_id": "string", "failed": "boolean", "status": "string", "summary": "string", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", wsPath(a, "/runs/"+url.PathEscape(a["run_id"])+"/why-failed"), ""
	},
}
