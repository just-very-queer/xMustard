package mcpserver

import "strconv"

// maxRecallLimit mirrors the API's cap on GET context/active (maxRecallLimit in
// cmd/xmustard-api); larger values are rejected here instead of clamped there.
const maxRecallLimit = 50

// The recall output budget (WS-20), duplicated from workspaceops so the shim does not
// link the memory store; a test keeps them equal. The MCP tool always budgets.
const (
	recallDefaultMaxChars = 4000
	recallMinMaxChars     = 1000
	recallMaxMaxChars     = 10000
)

// recallQueryArgs are the recall arguments that travel as query parameters under
// their own names.
var recallQueryArgs = []string{"paths", "limit", "entry_id", "history", "explain", "kind", "tags", "topic",
	"path_prefix", "since", "until", "by", "status", "include_pending", "include_superseded", "show_expired",
	"cursor", "names_only", "render", "session_id"}

var recallTool = &Tool{
	Name:        "recall",
	Description: "Shared memory RANKED by query/paths (BM25+path+trust+recency+feedback); non-matches dropped (terms >=3 chars). No args: top-N by working-tree overlap, then recency. verification_mode: peer_verified | single_agent | self_asserted_open_mode (no auth/quorum). conflicts: path overlap, not contradiction.",
	Args: []Arg{
		workspaceArg,
		{Name: "q", Type: typeString, Desc: "task query to rank memories by"},
		{Name: "paths", Type: typeString, Desc: "comma-separated repo-relative files to focus on"},
		{Name: "limit", Type: typeInteger, Min: 1, Max: maxRecallLimit, Desc: "max memories (default 8)"},
	},
	// Fetch by id (WS-19A), and filters, the verification queue, disclosure and the
	// output budget (WS-20): listed only in the full schema profile, documented at
	// DocsURI.
	Advanced: []Arg{
		{Name: "entry_id", Type: typeString, Desc: "fetch one entry, any state"},
		{Name: "history", Type: typeBoolean, Desc: "with entry_id: revisions, events (verifier)"},
		{Name: "status", Type: typeString, Enum: []string{"promoted", "pending", "awaiting_me"}},
		{Name: "include_pending", Type: typeBoolean},
		{Name: "include_superseded", Type: typeBoolean},
		{Name: "show_expired", Type: typeBoolean},
		{Name: "kind", Type: typeString, List: true, MaxLen: 512},
		{Name: "tags", Type: typeString, List: true, MaxLen: 1024},
		{Name: "topic", Type: typeString},
		{Name: "path_prefix", Type: typeString},
		{Name: "since", Type: typeString},
		{Name: "until", Type: typeString},
		{Name: "by", Type: typeString},
		{Name: "explain", Type: typeBoolean},
		{Name: "names_only", Type: typeBoolean},
		{Name: "render", Type: typeString, Enum: []string{"full", "compact"}},
		{Name: "max_chars", Type: typeInteger, Min: recallMinMaxChars, Max: recallMaxMaxChars},
		{Name: "cursor", Type: typeString},
		{Name: "session_id", Type: typeString},
	},
	Doc: "Filters combine with AND across arguments and OR within a list. `status`: promoted (default), " +
		"pending (the verification queue) or awaiting_me (pending entries you neither authored nor voted on); " +
		"pending entries are labelled trust=unverified with votes_needed, and reading them needs the verifier or " +
		"human-approver role. `include_pending`, `include_superseded` and `show_expired` add those states to " +
		"promoted memory. `kind` is one of remember's kinds. `topic` matches the topic and every topic under it (a/b " +
		"matches a/b/c). `path_prefix` keeps entries with a path under it. `since` and `until` bound updated_at " +
		"(a UTC date or RFC 3339 time; until is exclusive). `by` keeps one author's entries. `explain` adds " +
		"score_details (bm25, path, anchor, trust, recency, feedback, stale_penalty, total, reasons). " +
		"`names_only` returns id, title, topic, state, stale and paths; `render=compact` one line per entry; " +
		"fetch the full entry with `entry_id`. `max_chars` budgets the whole result (default 4000): entries " +
		"that do not fit are left out, the last one may come back with content_truncated, and output_budget " +
		"reports what was cut. `cursor` takes next_cursor to continue; omitted counts the ranked entries after " +
		"this page. `session_id` leaves out entries this session was already shown until their content, stale " +
		"flag or state changes (already_shown counts them; the set expires after 30 minutes unused).",
	// Claude Code drops a tool argument named exactly "query", so the advertised name
	// is q; "query" stays accepted for clients that already send it.
	Aliases:     map[string]string{"query": "q"},
	Annotations: Annotations{Title: "Recall shared memory", ReadOnly: true},
	Output: map[string]string{
		"workspace_id": "string", "query": "string", "ranked": "boolean", "limit": "integer", "returned": "integer",
		"stale_count": "integer", "drift_checked": "integer", "generated_at": "string",
	},
	MaxResultChars: boundedResultChars,
	// Over plain HTTP the budget is opt-in; the MCP tool always sends max_chars
	// (recallDefaultMaxChars when omitted). A fetch by id is not budgeted.
	Build: func(a map[string]string) (string, string, string) {
		kv := []string{"query", a["q"]}
		for _, k := range recallQueryArgs {
			kv = append(kv, k, a[k])
		}
		maxChars := a["max_chars"]
		if maxChars == "" && a["entry_id"] == "" {
			maxChars = strconv.Itoa(recallDefaultMaxChars)
		}
		return "GET", query(wsPath(a, "/context/active"), append(kv, "max_chars", maxChars)...), ""
	},
}
