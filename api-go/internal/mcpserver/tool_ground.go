package mcpserver

var groundTool = &Tool{
	Name:        "ground",
	Description: "Orient before acting: what changed, is stale, broken or blocked since the baseline, with index drift and contract breaks (changed signatures).",
	Args:        []Arg{workspaceArg},
	Annotations: Annotations{Title: "Ground the session", ReadOnly: true},
	Output: map[string]string{
		"workspace_id": "string", "changed_files": "integer", "dirty_symbols": "integer", "contract_breaks": "integer",
		"stale_memory": "integer", "blocked_by_dirty_state": "boolean", "blocked_by_failing_verification": "boolean",
		"summary": "string", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", wsPath(a, "/session-grounding"), ""
	},
}
