package mcpserver

// maxRecallLimit mirrors the API's cap on GET context/active (maxRecallLimit in
// cmd/xmustard-api); larger values are rejected here instead of clamped there.
const maxRecallLimit = 50

var recallTool = &Tool{
	Name:        "recall",
	Description: "Shared memory RANKED by query/paths (lexical, path overlap, approvals); non-matches dropped (terms >=3 chars). No args: top-N by working-tree overlap, then recency. verification_mode: peer_verified | single_agent | self_asserted_open_mode (no auth/quorum). conflicts: path overlap, not contradiction.",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Desc: "task query to rank memories by"},
		{Name: "paths", Type: typeString, Desc: "comma-separated repo-relative files to focus on"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxRecallLimit, Desc: "max memories (default 8)"},
	},
	// Fetch by id (WS-19A): any lifecycle state, listed only in the full schema profile.
	Advanced: []Arg{
		{Name: "entry_id", Type: typeString, Desc: "fetch one entry by id, even expired or retired"},
		{Name: "history", Type: typeBoolean, Desc: "with entry_id: add revisions and events"},
	},
	// Claude Code drops a tool argument named exactly "query", so the advertised name
	// is q; "query" stays accepted for clients that already send it.
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Recall shared memory", ReadOnly: true},
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "ranked": "boolean", "limit": "integer", "returned": "integer",
		"stale_count": "integer", "drift_checked": "integer", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", query(wsPath(a, "/context/active"), "query", a["q"], "paths", a["paths"], "limit", a["limit"],
			"entry_id", a["entry_id"], "history", a["history"]), ""
	},
}
