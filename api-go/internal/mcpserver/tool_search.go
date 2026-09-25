package mcpserver

// maxSearchLimit bounds the hits one search returns.
const maxSearchLimit = 50

var searchTool = &Tool{
	Name:        "search",
	Description: "Code search, path:line slices. Hybrid ranks symbol NAMES, paths and doc chunks, not function bodies: RRF of lexical IDF, trigram fuzzy match (typo tolerance, not meaning, unless built with semantic-onnx and XMUSTARD_EMBED_MODEL set), reference degree, proximity to seed=<symbol>. mode=pattern: ast-grep structural query (e.g. `$A && $A()`; optional lang).",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Required: true, Desc: "the search query"},
		{Name: "mode", Type: typeString, Enum: []string{"hybrid", "pattern"}, Desc: "hybrid (default) or pattern (ast-grep)"},
		{Name: "lang", Type: typeString, Desc: "language hint for pattern mode"},
		{Name: "seed", Type: typeString, Desc: "symbol to anchor the graph-proximity lane"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxSearchLimit, Desc: "max hits (default 25)"},
	},
	// Claude Code drops a tool argument named exactly "query" (see recall).
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Search code", ReadOnly: true},
	// hybrid: query, total, hits; pattern: match_count, truncated
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "total": "integer", "hits": "array",
		"match_count": "integer", "truncated": "boolean", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		return "GET", query(wsPath(a, "/search"), "q", a["q"], "mode", a["mode"], "lang", a["lang"], "seed", a["seed"], "limit", a["limit"]), ""
	},
}
