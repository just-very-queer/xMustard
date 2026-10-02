package mcpserver

import "xmustard/api-go/internal/workspaceops"

// maxSearchLimit bounds the hits one search returns.
const maxSearchLimit = 50

// searchQueryArgs are the search arguments that travel as query parameters under
// their own names.
var searchQueryArgs = []string{"q", "mode", "lang", "seed", "limit", "cursor", "path_glob",
	"kinds", "depth", "include_body", "include_info", "substring"}

var searchTool = &Tool{
	Name:        "search",
	Description: "Code search: path:line, snippets, reasons. RRF (K=60) of BM25 over function bodies, comments, names, paths and doc sections; identifier match; trigram typo tolerance (not meaning); reference degree; proximity to seed=<symbol>; pattern: ast-grep (`$A && $A()`); symbol: name path; text: body.",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Required: true, Desc: "the query"},
		{Name: "mode", Type: typeString, Enum: []string{"hybrid", "text", "pattern", "symbol"}, Desc: "hybrid (default), text (chunk text), pattern or symbol"},
		{Name: "lang", Type: typeString, Desc: "pattern: ast-grep hint; else filter"},
		{Name: "seed", Type: typeString, Desc: "symbol for the proximity lane"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxSearchLimit, Desc: "max hits (default 10)"},
	},
	// Paging and path filters (WS-18), the symbol lookup's options and the output
	// budget (WS-36): accepted everywhere, listed only in the full schema profile.
	Advanced: append([]Arg{
		{Name: "cursor", Type: typeString, MaxLen: maxSearchCursorLen, Desc: "next_cursor of the previous page"},
		{Name: "path_glob", Type: typeString, MaxLen: 256, Desc: "only paths matching this glob (** crosses /)"},
		{Name: "kinds", Type: typeString, MaxLen: 256, Desc: "mode=symbol: Function,Method or -Variable"},
		{Name: "depth", Type: typeInteger, Min: 0, Max: workspaceops.NavMaxDepth, Desc: "mode=symbol: child levels"},
		{Name: "include_body", Type: typeBoolean, Desc: "mode=symbol: add bodies"},
		{Name: "include_info", Type: typeBoolean, Desc: "mode=symbol: add signatures, docs"},
		{Name: "substring", Type: typeBoolean, Desc: "mode=symbol: substring"},
	}, budgetArgs("search")...),
	Doc: symbolSearchDoc + "\n" + budgetSpec("search").Markdown(),
	// Claude Code drops a tool argument named exactly "query" (see recall).
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Search code", ReadOnly: true},
	// hybrid: query, total, hits, omitted, next_cursor; pattern: match_count, truncated;
	// symbol: total, symbols (returned, not listed, to keep tools/list within its cap)
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "total": "integer", "hits": "array",
		"omitted": "integer", "next_cursor": "string",
		"match_count": "integer", "truncated": "boolean", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		kv := append(argQuery(a, searchQueryArgs), budgetQuery("search", a)...)
		return "GET", query(wsPath(a, "/search"), kv...), ""
	},
}

// symbolSearchDoc documents search mode=symbol.
const symbolSearchDoc = "### mode=symbol\n\n`q` is a name path over the code index: `name` matches every non-local symbol so " +
	"named (with `substring`, every name containing it, case-insensitive); `A/b` matches `b` whose containers end " +
	"with `A`; `/A/b` matches from the file's top level; `b[i]` keeps the i-th (0-based, source order) of one name " +
	"path in a file (overloads). `path_glob` scopes files; `kinds` includes (`Function,Method`) or excludes " +
	"(`-Variable`) kinds; `depth` (0 to 3) lists children; `include_info` adds the signature line and leading doc " +
	"comment, `include_body` the body, both sliced from the working tree (never stored), withheld with the reason " +
	"under `unavailable` when the file changed since it was indexed or is a secret path. Matches are ranked by " +
	"`relevance` and carry `uid`, `name_path`, `kind`, `path`, `lines`. `limit` (default 10) is the page and the " +
	"most matches listed: past it, `refine` maps each file to its matching name paths (`total_candidates`, " +
	"`candidates_truncated`), and `next_cursor` continues. `ambiguous` is true when the matches are more than one " +
	"symbol; `explain(uid=...)` then picks one.\n"
