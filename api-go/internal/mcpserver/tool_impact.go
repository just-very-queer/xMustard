package mcpserver

// maxImpactDepth is the API's default hop limit, which the description promises
// ("≤4 hops"); shallower walks are allowed, deeper ones rejected.
const maxImpactDepth = 4

var impactTool = &Tool{
	Name:        "impact",
	Description: "Blast radius over a LEXICAL reference graph (name matches + import lines, not resolved calls): distance≥1 edges are leads to confirm, not proof. No args → current changes (dirty symbols, contract_break). symbol= → files referencing its defining files, ≤4 hops. from=&to= → shortest undirected file path.",
	Args: []Arg{
		workspaceArg,
		{Name: "symbol", Type: typeString, Desc: "symbol to compute blast radius for"},
		{Name: "from", Type: typeString, Desc: "trace path from this symbol"},
		{Name: "to", Type: typeString, Desc: "trace path to this symbol"},
		{Name: "max_depth", Type: typeInteger, Min: 1, Max: maxImpactDepth, Desc: "max hops for symbol= (default 4)"},
	},
	Aliases:     map[string]string{"depth": "max_depth"}, // the API's query name
	Annotations: Annotations{Title: "Estimate change impact", ReadOnly: true},
	// symbol=: symbol, max_depth, impacted; from=&to=: found, length; neither: since
	Output: map[string]string{
		"symbol": "string", "max_depth": "integer", "impacted_count": "integer", "impacted": "array",
		"found": "boolean", "length": "integer", "since": "string", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		p := wsPath(a, "/changes/since-index")
		switch {
		case a["from"] != "" && a["to"] != "":
			p = query(p, "from", a["from"], "to", a["to"])
		case a["symbol"] != "":
			p = query(p, "symbol", a["symbol"], "depth", a["max_depth"])
		}
		return "GET", p, ""
	},
}
