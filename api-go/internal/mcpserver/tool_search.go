package mcpserver

import "xmustard/api-go/internal/workspaceops"

// maxSearchLimit bounds the hits one search returns.
const maxSearchLimit = 50

// searchQueryArgs are the search arguments that travel as query parameters under
// their own names.
var searchQueryArgs = []string{"q", "mode", "lang", "seed", "limit", "cursor", "path_glob",
	"kinds", "depth", "include_body", "include_info", "substring", "max_lines", "max_bytes",
	"timeout_ms"}

var searchTool = &Tool{
	Name:        "search",
	// The lean schema has zero byte headroom — the caps equal the measured sizes — so the
	// two new modes and their two output members (89 bytes together) are paid for out of
	// this description and its argument descriptions, never by raising a cap (§5.1 rule 9;
	// WS-18, WS-21 and WS-35 trimmed the same way).
	//
	// What moved to `xmustard://docs/tools` is what the enum and the arg names already
	// say: the per-mode gloss, and the two caveats a caller needs but a session prompt does
	// not — that trigram tolerance is not meaning, and that `lang` does not filter
	// `mode=symbol`. `Doc` is not rendered in tools/list, so it costs nothing there.
	Description: "Code search: path:line, reasons. RRF (K=60) of BM25 over bodies, names, paths and docs; identifier match; trigram typo tolerance; reference degree; proximity to seed; pattern: ast-grep (`$A && $A()`); exact/regex: spans.",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Required: true, Desc: "the query"},
		{Name: "mode", Type: typeString, Enum: []string{"hybrid", "text", "exact", "regex", "pattern", "symbol"}, Desc: "hybrid (default); see xmustard://docs/tools"},
		{Name: "lang", Type: typeString, MaxLen: 256, Desc: "pattern: ast-grep hint; else a filter"},
		{Name: "seed", Type: typeString, Desc: "proximity seed"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxSearchLimit, Desc: "max hits"},
	},
	// Paging and path filters (WS-18), the symbol lookup's options and the output
	// budget (WS-36), and the scan lane's three caps (WS-59c): accepted everywhere,
	// listed only in the full schema profile.
	Advanced: append([]Arg{
		{Name: "cursor", Type: typeString, MaxLen: maxSearchCursorLen, Desc: "next_cursor of the previous page"},
		{Name: "path_glob", Type: typeString, MaxLen: 256, Desc: "only paths matching this glob (** crosses /)"},
		{Name: "kinds", Type: typeString, MaxLen: 256, Desc: "mode=symbol: Function,Method or -Variable"},
		{Name: "depth", Type: typeInteger, Min: 0, Max: workspaceops.NavMaxDepth, Desc: "mode=symbol: child levels"},
		{Name: "include_body", Type: typeBoolean, Desc: "mode=symbol: add bodies"},
		{Name: "include_info", Type: typeBoolean, Desc: "mode=symbol: add signatures, docs"},
		{Name: "substring", Type: typeBoolean, Desc: "mode=symbol: substring"},
		{Name: "max_lines", Type: typeInteger, Min: 1, Max: workspaceops.SearchScanMaxLines, Desc: "exact/regex: spans to report"},
		{Name: "max_bytes", Type: typeInteger, Min: 1, Max: workspaceops.SearchScanMaxBytes, Desc: "exact/regex: bytes to scan"},
		{Name: "timeout_ms", Type: typeInteger, Min: 1, Max: workspaceops.SearchScanTimeoutMS, Desc: "exact/regex: time budget"},
	}, budgetArgs("search")...),
	Doc: modesDoc + "\n" + symbolSearchDoc + "\n" + scanSearchDoc + "\n" + budgetSpec("search").Markdown(),
	// Claude Code drops a tool argument named exactly "query" (see recall).
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Search code", ReadOnly: true},
	// hybrid: query, total, hits, omitted, next_cursor; pattern: match_count, truncated;
	// symbol: total, symbols (returned, not listed, to keep tools/list within its cap);
	// exact/regex: spans and bytes_scanned beside truncated, which is already listed.
	// `cut` and `files_skipped` are NOT advertised: they are diagnostics the scan names in
	// `degradations` in prose, and listing them costs 63 bytes of the lean schema's
	// zero-headroom budget for a caller who reads them there anyway (§5.1 rule 9: the
	// lean schema advertises what a call needs, not everything the answer can carry).
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "total": "integer", "hits": "array",
		"omitted": "integer", "next_cursor": "string",
		"match_count": "integer", "truncated": "boolean", "generated_at": "string",
		"spans": "array", "bytes_scanned": "integer",
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

// scanSearchDoc documents search mode=exact|regex (WS-59c).
const scanSearchDoc = "### mode=exact|regex\n\n`q` is matched against the working tree's bytes, not ranked: `exact` takes it " +
	"literally and case-sensitively, `regex` as a pattern. Every occurrence is one span with `path`, `line`, " +
	"`column`, `byte` and `len`; two occurrences on one line are two spans. `path_glob` and `lang` mean here what " +
	"they mean in the hybrid, and `hits` is empty. Three caps bound the scan and each is reported rather than " +
	"implied: `max_lines` (default and ceiling 200) spans listed, `max_bytes` (4194304) bytes read and reported " +
	"as `bytes_scanned`, `timeout_ms` (2000) wall clock. A cap above its ceiling is refused, never clamped. A cap " +
	"that fires sets `truncated` and names itself in `cut` (`lines`, `bytes`, `time`) and in `degradations`, with " +
	"`files_skipped` counting the files never opened. A secret path is never read: it appears in `degradations` and " +
	"under `coverage.loss_counts`, never in `spans`. A pattern the engine refuses is reported, not answered as " +
	"`no match`."

// modesDoc carries what search's lean schema no longer has room for (WS-59c): the
// per-mode gloss and the two caveats that distinguish the lanes from each other. The lean
// tools/list has zero byte headroom, and `Doc` is printed at xmustard://docs/tools instead,
// which is where §5.1 rule 9 says an Advanced argument's documentation lives.
const modesDoc = "### modes\n\n`hybrid` (default) fuses six lanes. `text` is the body lane alone: a chunk's own text, " +
	"not its path or its declared names. `exact` and `regex` do not rank at all — they match " +
	"`q` against the working tree's bytes and report one `spans` entry per occurrence " +
	"(`path`, `line`, `column`, `byte`, `len`); two occurrences on one line are two spans. " +
	"`pattern` is an ast-grep structural query and `symbol` is a name path.\n\n" +
	"`lang` is ast-grep's own language under `mode=pattern` and a file-language filter under " +
	"`hybrid`, `text`, `exact` and `regex`; it is not a filter under `mode=symbol`. Trigram " +
	"tolerance in `hybrid` is typo tolerance, not meaning: it matches spellings, not intent."
