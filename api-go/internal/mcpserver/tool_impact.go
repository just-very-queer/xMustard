package mcpserver

// maxImpactDepth is the API's default hop limit, which the description promises
// ("≤4 hops"); shallower walks are allowed, deeper ones rejected.
const maxImpactDepth = 4

var impactTool = &Tool{
	Name:        "impact",
	Description: "Blast radius over a LEXICAL reference graph (name matches + import lines, not resolved calls): distance≥1 edges are leads to confirm, not proof. No args → current changes (dirty symbols, contract_break). symbol= → files referencing its defining files, ≤4 hops; path= → the same from one file. from=&to= → shortest undirected file path.",
	Args: []Arg{
		workspaceArg,
		{Name: "symbol", Type: typeString, Desc: "symbol to compute blast radius for"},
		{Name: "path", Type: typeString, MaxLen: maxPathArg, Desc: "a repo-relative file"},
		{Name: "from", Type: typeString, Desc: "trace path from this symbol"},
		{Name: "to", Type: typeString, Desc: "trace path to this symbol"},
		{Name: "max_depth", Type: typeInteger, Min: 1, Max: maxImpactDepth, Desc: "max hops for symbol=/path= (default 4)"},
	},
	Aliases:     map[string]string{"depth": "max_depth"}, // the API's query name
	PathArg:     "path",
	Annotations: Annotations{Title: "Estimate change impact", ReadOnly: true},
	// symbol=: symbol, impacted; path=: found, impacted; from=&to=: found, length;
	// neither: since. The graph reads carry the graph's freshness and coverage. Echoed
	// inputs (path, max_depth) and generated_at are returned but not listed, to keep
	// tools/list within its cap.
	Output: map[string]string{
		"symbol": "string", "impacted_count": "integer", "impacted": "array",
		"found": "boolean", "length": "integer", "since": "string",
		"freshness": "object", "coverage": "object",
	},
	MaxResultChars: boundedResultChars,
	// path= is file-level impact (WS-FIX-05; WS-35's impact v2 absorbs it). It forwards
	// every mode argument, so the API refuses path mixed with symbol or from/to rather
	// than one silently winning.
	Build: func(a map[string]string) (string, string, string) {
		p := wsPath(a, "/changes/since-index")
		switch {
		case a["path"] != "":
			p = query(p, "path", a["path"], "symbol", a["symbol"], "from", a["from"], "to", a["to"], "depth", a["max_depth"])
		case a["from"] != "" && a["to"] != "":
			p = query(p, "from", a["from"], "to", a["to"])
		case a["symbol"] != "":
			p = query(p, "symbol", a["symbol"], "depth", a["max_depth"])
		}
		return "GET", p, ""
	},
}
