package mcpserver

import "xmustard/api-go/internal/workspaceops"

// maxImpactDepth bounds max_depth for symbol= (PAR-IMP-04: 1 to 32, default 3); a
// trace allows 30 and defaults to 10 (PAR-IMP-07), which the API checks.
const maxImpactDepth = 32

var impactTool = &Tool{
	Name:        "impact",
	Description: "Blast radius. symbol= (name, Container.name, UID, path) → dependents by depth (d1 breaks), risk, completeness. Go/TS edges are resolved; other languages match names: leads, not proof. path= → a file's lexical referrers. from=&to= → shortest path, per-hop edges. No args → changes since the index baseline.",
	Args: []Arg{
		workspaceArg,
		{Name: "symbol", Type: typeString, Desc: "symbol to compute blast radius for"},
		{Name: "path", Type: typeString, MaxLen: maxPathArg, Desc: "a repo-relative file"},
		{Name: "from", Type: typeString, Desc: "trace path from this symbol"},
		{Name: "to", Type: typeString, Desc: "trace path to this symbol"},
		{Name: "max_depth", Type: typeInteger, Min: 1, Max: maxImpactDepth, Desc: "hops (default 3; path= 4)"},
	},
	// Impact v2 (WS-35): typed filters, per-depth paging and diff-to-symbol scopes, and
	// the output budget (WS-36), are accepted everywhere and listed only in the full
	// schema profile; the docs resource documents them.
	Advanced: append([]Arg{
		{Name: "op", Type: typeString, Enum: workspaceops.OccurrenceOps,
			Desc: "symbol-level occurrences instead of the depth tiers: refs (every use), definition (what declares it), implementations (types implementing it)"},
		{Name: "direction", Type: typeString, Enum: []string{"upstream", "downstream"}, Desc: "dependents (default) or dependencies"},
		{Name: "relation_types", Type: typeString, List: true, Enum: workspaceops.RelationTypes, MaxLen: 128,
			Desc: "edge kinds to walk (default CALLS,IMPORTS,EXTENDS,IMPLEMENTS)"},
		{Name: "min_confidence", Type: typeNumber, Min: 0, Max: 1, Desc: "skip edges below this confidence"},
		{Name: "include_tests", Type: typeBoolean, Desc: "walk test files (default false)"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: 500, Desc: "items per depth tier (default 50), or per page with op="},
		{Name: "offset", Type: typeInteger, Min: 0, Max: 50000, Desc: "items skipped per depth tier"},
		{Name: "cursor", Type: typeString, MaxLen: maxSearchCursorLen, Desc: "next_cursor of the previous op= page"},
		{Name: "summary_only", Type: typeBoolean, Desc: "counts, risk and envelope without items"},
		{Name: "timeout_ms", Type: typeInteger, Min: 1, Max: 60000, Desc: "walk budget (default 10000)"},
		{Name: "scope", Type: typeString, Enum: workspaceops.ChangeScopes, Desc: "symbols a diff touches, and their dependents"},
		{Name: "base", Type: typeString, MaxLen: 256, Desc: "scope=compare|range: base revision"},
		{Name: "head", Type: typeString, MaxLen: 256, Desc: "scope=compare|commit|range: head revision"},
		{Name: "worktree", Type: typeString, MaxLen: 4096, Desc: "another checkout of this repository to diff"},
	}, budgetArgs("impact")...),
	Doc: impactDoc + "\n" + budgetSpec("impact").Markdown(),
	// depth is the API's query name; a UID goes where a symbol does
	Aliases:     map[string]string{"depth": "max_depth", "uid": "symbol"},
	PathArg:     "path",
	Annotations: Annotations{Title: "Estimate change impact", ReadOnly: true},
	// symbol=: symbol, impacted_count, by_depth, risk; path=: found, impacted_count
	// (and impacted); from=&to=: found, length; scope= and no args: changed symbols and
	// since. Every graph read carries the graph's freshness and coverage. Other members
	// (counts, hops, affected) are returned but not listed, to keep tools/list within
	// its cap.
	Output: map[string]string{
		"symbol": "string", "impacted_count": "integer", "by_depth": "array", "risk": "string",
		"found": "boolean", "length": "integer", "since": "string",
		"freshness": "object", "coverage": "object",
		// op= (WS-59d): the occurrence page's members (occurrences, total,
		// next_cursor, epistemic) are returned but not listed, like counts, hops and
		// affected, so the lean schema keeps only the members every form shares.
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
		// the output budget follows the tool's own arguments (WS-36)
		return "GET", query(wsPath(a, "/changes/since-index"), append(kv, budgetQuery("impact", a)...)...), ""
	},
}

// impactQuery is every impact argument in the order the API receives it: the forms'
// own arguments, then the walk's, then paging.
var impactQuery = []string{"path", "symbol", "from", "to", "direction", "scope", "base", "head", "worktree",
	"op", "relation_types", "min_confidence", "include_tests", "max_depth", "limit", "offset", "summary_only",
	"timeout_ms", "cursor"}

// impactDoc is the docs resource's paragraph on impact v2.
const impactDoc = `symbol= accepts a name, Container.name, a UID or a file path; a class or interface is also seeded with its constructors and its file (upstream) or its members (downstream). Results group by depth: 1 WILL BREAK, 2 LIKELY AFFECTED, 3+ MAY NEED TESTING, each paged by limit and offset. risk (upstream only) is CRITICAL when direct dependents >= 30, modules >= 5 or impacted >= 200; HIGH at 15, 3, 100; MEDIUM at 5 direct or 30 impacted; LOW otherwise; UNKNOWN when no dependent is found. epistemic.completeness is exact only when no resolver drop, lexical edge, coverage loss or cap could hide a dependent; otherwise lower_bound with causes and boundaries. Arguments of another form (for example direction with from=&to=, or two of symbol=, path= and from=&to=) are refused.

path= (WS-FIX-05) lists the files that reference a symbol the file defines, with their distance, over the file-level lexical graph: max_depth 1 to 4 (default 4), no filters or paging; found is false when the graph does not hold the file. For resolved tiers and risk from a file, pass it as symbol=.

scope= reads a diff (system and global Git configuration ignored, every diff option pinned): unstaged (index to working tree), staged (HEAD to index), all or workspace (HEAD to working tree), compare (base to the working tree, or to head), commit (head's first parent to head, default HEAD), range (merge-base of base and head to head); a revision the scope does not take is refused. worktree, an absolute path, diffs another checkout of the repository. Working-tree scopes include untracked files. Each changed file is parsed on both sides, so spans are current; changed_symbols are added, modified or removed with contract_break on a signature change or a removed export, and affected is the upstream walk from all of them.

from=&to= searches downstream (from depends on to), then upstream; max_depth defaults to 10 (at most 30), 200 edges per node, 5000 nodes per level, 50000 visited. Without a path it returns the furthest node reached and the caps hit.

op= (WS-59d) replaces the depth tiers with the symbol-level occurrences of the symbol: op=refs lists every use of the name, op=definition the symbols that declare it, op=implementations the types that implement or extend it. Each occurrence carries the containing symbol's name path, kind and line, the reference's own kind, a ±1-line snippet of the reference (numbered, secret words masked), and its provenance: resolved when the scope resolver bound the reference, lexical when it is a name match and therefore a lead, not proof. A reference outside any symbol is reported with kind File and the file's path as the name. The occurrence page is paged by limit and continued by next_cursor, which is bound to this symbol and this operation; a cursor from another occurrence call, an edited one, or one passed with offset is refused. completeness is exact only when every occurrence is resolved and no reference to the name went unbound; otherwise lower_bound with the cause counts. offset, direction, relation_types, min_confidence, include_tests, max_depth, summary_only and timeout_ms describe the tier walk, so they are not read with op=.
`
