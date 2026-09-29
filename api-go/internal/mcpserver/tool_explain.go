package mcpserver

import "xmustard/api-go/internal/workspaceops"

// maxPathArg bounds the path argument (PATH_MAX on Linux): it is model-supplied and
// matched against the registered roots on every call.
const maxPathArg = 4096

// explainQueryArgs are the explain arguments that travel as query parameters under
// their own names.
var explainQueryArgs = []string{"path", "symbol", "uid", "depth", "chain_depth", "include_content"}

var explainTool = &Tool{
	Name:        "explain",
	Description: "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.",
	Args: []Arg{
		workspaceArg,
		{Name: "path", Type: typeString, MaxLen: maxPathArg, Desc: "file or directory path"},
	},
	// A symbol's context, the outline's depth and the output budget (WS-36): accepted
	// everywhere, listed only in the full schema profile, documented at DocsURI.
	Advanced: append([]Arg{
		{Name: "symbol", Type: typeString, MaxLen: maxPathArg, Desc: "a name path: its context"},
		{Name: "uid", Type: typeString, MaxLen: maxPathArg, Desc: "a symbol UID: its context"},
		{Name: "depth", Type: typeInteger, Min: 0, Max: workspaceops.NavMaxDepth, Desc: "outline child levels"},
		{Name: "chain_depth", Type: typeInteger, Min: 0, Max: workspaceops.NavMaxChainDepth, Desc: "symbol=: extra edge layers"},
		{Name: "include_content", Type: typeBoolean, Desc: "symbol=: add its body"},
	}, budgetArgs("explain")...),
	Doc:         explainDoc + "\n" + budgetSpec("explain").Markdown(),
	PathArg:     "path",
	Annotations: Annotations{Title: "Explain a path", ReadOnly: true},
	// {explanation, cluster} when the file has a cluster, else the explanation itself;
	// either way with the graph's freshness and coverage and the file's outline
	// (symbols). A directory answers its outline (kind, files), a symbol its context
	// (symbol, incoming, outgoing). The echoed path, line_count and the outline and
	// context members are returned but not listed, to keep tools/list within its cap.
	Output: map[string]string{
		"explanation": "object", "cluster": "object", "role": "string", "summary": "string",
		"freshness": "object", "coverage": "object",
	},
	MaxResultChars: boundedResultChars,
	Build: func(a map[string]string) (string, string, string) {
		kv := append(argQuery(a, explainQueryArgs), budgetQuery("explain", a)...)
		return "GET", query(wsPath(a, "/explain-path"), kv...), ""
	},
}

// explainDoc documents explain's outline and symbol context.
const explainDoc = "### Outline and symbol context\n\nexplain takes one of `path`, `symbol` or `uid`. A file answers its " +
	"explanation, its community cluster and its outline: top-level symbols (locals excluded) with line ranges and " +
	"children to `depth` (default 1, at most 3), `symbols_total` and `symbols_by_kind`. A directory answers its " +
	"outline: `summary`, `files_total`, `languages`, `symbols_by_kind`, its immediate `dirs`, and `files`, each " +
	"with its own outline (default depth 0; at most 2,000 files, `files_truncated` past that, and 50 top-level " +
	"symbols per file, `symbols_total` counting them all). `symbol` (a name path, as search mode=symbol takes it) or `uid` " +
	"answers the symbol's context: `symbol` with its signature and doc comment (`include_content` adds the body), " +
	"`incoming` and `outgoing` edges grouped by `relation` (CALLS, USES, ACCESSES with access, EXTENDS, " +
	"IMPLEMENTS, HAS_METHOD, HAS_PROPERTY) with `layer`, `confidence` and `provenance`, `chain_depth` (0 to 3) " +
	"further layers over resolved edges, counts by relation, `epistemic` (`completeness` exact or lower_bound, " +
	"and why), and `memories`: served memory on the symbol's file that its name ranks. Incoming references carry " +
	"the first line naming the symbol, and the first ten a ±1-line snippet. Go and TS/JS edges are resolved; " +
	"other languages answer incoming references by name (`provenance: lexical`), which are leads, not proof. " +
	"An ambiguous name path answers `ambiguous` with ranked `candidates` and their UIDs.\n"
