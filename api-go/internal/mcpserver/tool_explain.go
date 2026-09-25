package mcpserver

// maxPathArg bounds the path argument (PATH_MAX on Linux): it is model-supplied and
// matched against the registered roots on every call.
const maxPathArg = 4096

var explainTool = &Tool{
	Name:        "explain",
	Description: "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.",
	Args: []Arg{
		workspaceArg,
		{Name: "path", Type: typeString, Required: true, MaxLen: maxPathArg, Desc: "a repo-relative file or directory path"},
	},
	PathArg:     "path",
	Annotations: Annotations{Title: "Explain a path", ReadOnly: true},
	// {explanation, cluster} when the file has a cluster, else the explanation itself
	Output: map[string]string{
		"explanation": "object", "cluster": "object",
		"path": "string", "role": "string", "summary": "string", "line_count": "integer",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", query(wsPath(a, "/explain-path"), "path", a["path"]), ""
	},
}
