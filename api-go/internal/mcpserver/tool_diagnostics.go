package mcpserver

var diagnosticsTool = &Tool{
	Name:           "diagnostics",
	Description:    "Current normalized diagnostics (errors/warnings) for the workspace.",
	Args:           []Arg{workspaceArg},
	Annotations:    Annotations{Title: "Read diagnostics", ReadOnly: true},
	Output:         map[string]string{"workspace_id": "string", "generated_at": "string"},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", wsPath(a, "/diagnostics"), ""
	},
}
