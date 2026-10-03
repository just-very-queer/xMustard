package mcpserver

import "xmustard/api-go/internal/workspaceops"

// maxImpactDepth bounds max_depth for symbol= (PAR-IMP-04: 1 to 32, default 3); a
// trace allows 30 and defaults to 10 (PAR-IMP-07), which the API checks.
const maxImpactDepth = 32

var impactTool = &Tool{
	Name:        "impact",
	Description: "Blast radius. symbol= → dependents by depth (d1 breaks), risk, completeness. Go/TS edges are resolved; other languages match names: leads, not proof. path= → a file's lexical referrers. from=&to= → shortest path, per-hop edges. No args → changes since baseline.",
	Args: []Arg{
		workspaceArg,
		{Name: "symbol", Type: typeString, Desc: "name, Container.name, UID or path"},
		{Name: "path", Type: typeString, MaxLen: maxPathArg, Desc: "a repo-relative file"},
		{Name: "from", Type: typeString, Desc: "trace from here"},
		{Name: "to", Type: typeString, Desc: "trace to here"},
		{Name: "max_depth", Type: typeInteger, Min: 1, Max: maxImpactDepth, Desc: "hops (default 3; path= 4)"},
	},
	// Impact v2 (WS-35): typed filters, per-depth paging and diff-to-symbol scopes, and
	// the output budget (WS-36), are accepted everywhere and listed only in the full
	// schema profile; the docs resource documents them. cursor= continues a cut tier
	// (WS-59e), and the two paging members are listed for the same reason.
	Advanced: append([]Arg{
		{Name: "direction", Type: typeString, Enum: []string{"upstream", "downstream"}, Desc: "dependents (default) or dependencies"},
		{Name: "relation_types", Type: typeString, List: true, Enum: workspaceops.RelationTypes, MaxLen: 128,
			Desc: "edge kinds (default CALLS,IMPORTS,EXTENDS,IMPLEMENTS)"},
		{Name: "min_confidence", Type: typeNumber, Min: 0, Max: 1, Desc: "skip edges below this confidence"},
		{Name: "include_tests", Type: typeBoolean, Desc: "walk test files (default false)"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: 500, Desc: "items per depth tier (default 50)"},
		{Name: "offset", Type: typeInteger, Min: 0, Max: 50000, Desc: "items skipped per tier"},
		{Name: "summary_only", Type: typeBoolean, Desc: "counts, risk, no items"},
		{Name: "timeout_ms", Type: typeInteger, Min: 1, Max: 60000, Desc: "walk budget (default 10000)"},
		{Name: "scope", Type: typeString, Enum: workspaceops.ChangeScopes, Desc: "symbols a diff touches, and dependents"},
		{Name: "base", Type: typeString, MaxLen: 256, Desc: "scope=compare|range: base"},
		{Name: "head", Type: typeString, MaxLen: 256, Desc: "scope=compare|commit|range: head"},
		{Name: "worktree", Type: typeString, MaxLen: 4096, Desc: "another checkout to diff"},
		// WS-59e: the tier cursor, declared here because budgetArgs omits it for a
		// non-Paged budget tool. Advanced, documented at xmustard://docs/tools.
		{Name: "cursor", Type: typeString, MaxLen: maxSearchCursorLen, Desc: "next_cursor of the previous page"},
	}, budgetArgs("impact")...),
	Doc: impactDoc + "\n" + budgetSpec("impact").Markdown(),
	// depth is the API's query name; a UID goes where a symbol does
	Aliases:     map[string]string{"depth": "max_depth", "uid": "symbol"},
	PathArg:     "path",
	Annotations: Annotations{Title: "Estimate change impact", ReadOnly: true},
	// symbol=: symbol, impacted_count, by_depth, risk; path=: found, impacted_count
	// (and impacted); from=&to=: found, length; scope= and no args: changed symbols and
	// since. Every graph read carries the graph's freshness and coverage. Other members
	// (epistemic, counts, hops, affected) are returned but not listed, to keep
	// tools/list within its cap. A paged form reports has_more and next_cursor.
	Output: map[string]string{
		"symbol": "string", "impacted_count": "integer", "by_depth": "array", "risk": "string",
		"found": "boolean", "length": "integer", "since": "string",
		"has_more": "boolean", "next_cursor": "string",
		"freshness": "object", "coverage": "object",
	},
	MaxResultChars: boundedResultChars,
	// Every argument goes to the API, which picks the form and refuses an argument of
	// another form (workspaceops.CheckImpactRequest), so no filter is dropped unsaid.
	Build: func(a map[string]string) (string, string, string) {
		kv := make([]string, 0, 2*len(impactQuery))
		for _, name := range impactQuery {
			param := name
			if name == "max_depth" {
				param = "depth" // the API's name
			}
			kv = append(kv, param, a[name])
		}
		// The output budget follows the tool's own arguments (WS-36). impact is not a
		// Paged budget tool (WS-35 pages its tiers itself), so budgetQuery sends
		// max_chars alone and the tier cursor travels with it (WS-59e).
		kv = append(kv, "cursor", a["cursor"])
		return "GET", query(wsPath(a, "/changes/since-index"), append(kv, budgetQuery("impact", a)...)...), ""
	},
}

// impactQuery is every impact argument in the order the API receives it: the forms'
// own arguments, then the walk's, then paging.
var impactQuery = []string{"path", "symbol", "from", "to", "direction", "scope", "base", "head", "worktree",
	"relation_types", "min_confidence", "include_tests", "max_depth", "limit", "offset", "summary_only", "timeout_ms"}

// impactDoc is the docs resource's paragraph on impact v2.
const impactDoc = `symbol= accepts a name, Container.name, a UID or a file path; a class or interface is also seeded with its constructors and its file (upstream) or its members (downstream). Results group by depth: 1 WILL BREAK, 2 LIKELY AFFECTED, 3+ MAY NEED TESTING, each paged by limit and offset. risk (upstream only) is CRITICAL when direct dependents >= 30, modules >= 5 or impacted >= 200; HIGH at 15, 3, 100; MEDIUM at 5 direct or 30 impacted; LOW otherwise; UNKNOWN when no dependent is found. epistemic.completeness is exact only when no resolver drop, lexical edge, coverage loss or cap could hide a dependent; otherwise lower_bound with causes and boundaries. Arguments of another form (for example direction with from=&to=, or two of symbol=, path= and from=&to=) are refused.

path= (WS-FIX-05) lists the files that reference a symbol the file defines, with their distance, over the file-level lexical graph: max_depth 1 to 4 (default 4), no filters or paging; found is false when the graph does not hold the file. For resolved tiers and risk from a file, pass it as symbol=.

scope= reads a diff (system and global Git configuration ignored, every diff option pinned): unstaged (index to working tree), staged (HEAD to index), all or workspace (HEAD to working tree), compare (base to the working tree, or to head), commit (head's first parent to head, default HEAD), range (merge-base of base and head to head); a revision the scope does not take is refused. worktree, an absolute path, diffs another checkout of the repository. Working-tree scopes include untracked files. Each changed file is parsed on both sides, so spans are current; changed_symbols are added, modified or removed with contract_break on a signature change or a removed export, and affected is the upstream walk from all of them.

from=&to= searches downstream (from depends on to), then upstream; max_depth defaults to 10 (at most 30), 200 edges per node, 5000 nodes per level, 50000 visited. Without a path it returns the furthest node reached and the caps hit.

A tier that limit cut reports has_more and a next_cursor; pass it back as cursor= to continue at the next page. It is signed for the ranking that asked for the page, so two calls that differ in symbol, scope, direction, max_depth, relation_types, min_confidence, include_tests or limit do not accept each other's cursor. cursor= and offset= both name the page position, so giving both is refused; the trace (from=&to=) and file (path=) forms have no tier page and refuse a cursor.
`
