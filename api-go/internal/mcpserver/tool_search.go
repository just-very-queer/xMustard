package mcpserver

// maxSearchLimit bounds the hits one search returns.
const maxSearchLimit = 50

var searchTool = &Tool{
	Name:        "search",
	Description: "Code search: path:line hits, snippets, reasons. RRF (K=60) of BM25 over function bodies, comments, names, paths and doc sections; identifier match; trigram typo tolerance (not meaning); reference degree; proximity to seed=<symbol>. mode=pattern: ast-grep query (e.g. `$A && $A()`; optional lang).",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Required: true, Desc: "the search query"},
		{Name: "mode", Type: typeString, Enum: []string{"hybrid", "pattern"}, Desc: "hybrid (default) or pattern (ast-grep)"},
		{Name: "lang", Type: typeString, Desc: "language hint for pattern mode"},
		{Name: "seed", Type: typeString, Desc: "symbol to anchor the graph-proximity lane"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxSearchLimit, Desc: "max hits (default 25)"},
	},
	// Paging and path filters (WS-18): accepted everywhere, listed only in the full
	// schema profile.
	Advanced: []Arg{
		{Name: "cursor", Type: typeString, MaxLen: 64, Desc: "next_cursor of the previous page"},
		{Name: "path_glob", Type: typeString, MaxLen: 256, Desc: "only paths matching this glob (** crosses /)"},
	},
	// Claude Code drops a tool argument named exactly "query" (see recall).
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Search code", ReadOnly: true},
	// hybrid: query, total, hits, omitted, next_cursor; pattern: match_count, truncated
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "total": "integer", "hits": "array",
		"omitted": "integer", "next_cursor": "string",
		"match_count": "integer", "truncated": "boolean", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", query(wsPath(a, "/search"), "q", a["q"], "mode", a["mode"], "lang", a["lang"], "seed", a["seed"], "limit", a["limit"],
			"cursor", a["cursor"], "path_glob", a["path_glob"]), ""
	},
}
