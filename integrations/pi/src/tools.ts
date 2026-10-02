// The nine xMustard tools, mirrored from api-go/internal/mcpserver/tool_<name>.go.
//
// Names, descriptions, argument schemas and the method/path/body each call maps to
// must stay identical to the MCP server: the unit test compares `toJsonSchema` with
// the generated tools/list snapshots (api-go/internal/mcpserver/testdata/tools_list.json
// for the lean profile, tools_list_full.json for the full one) and the e2e script diffs it
// against the live `tools/list` answer. Each spec carries both halves of the server's
// argument surface: `args` is what the lean profile advertises, `advanced` what the full
// profile adds, and Pi registers the full profile because it validates a call against its
// own closed schema. Hidden argument aliases (such as `query` for `q`) are MCP
// compatibility only: Pi validates calls against these closed schemas, so it never
// receives them. This module is plain data (no TypeBox) so the conformance check and unit
// tests run without Pi's module aliases.

export interface ArgSpec {
	name: string;
	type: "string" | "boolean" | "integer" | "number";
	required?: boolean;
	enum?: string[];
	minimum?: number; // integer or number bounds (inclusive)
	maximum?: number;
	maxLength?: number; // string length cap in characters
	// desc is optional because many Advanced arguments carry none: Go's Arg.Desc is the
	// zero value for them and tools/list omits the description key entirely.
	desc?: string;
}

export interface HttpCall {
	method: "GET" | "POST";
	path: string;
	body?: string;
}

export type ToolArgs = Record<string, string | boolean | number | undefined>;

export interface ToolSpec {
	name: string;
	description: string;
	// args are the tool's lean arguments: the ones the MCP server advertises in its
	// default tools/list, which every session pays for in its prompt prefix.
	args: ArgSpec[];
	// advanced are the arguments the MCP server accepts on tools/call and advertises
	// only under XMUSTARD_MCP_SCHEMA=full (mcpserver Tool.Advanced). Pi validates every
	// call against this module's own closed schema, so an argument missing here cannot be
	// sent from Pi at all; they are carried and rendered under the "full" profile, which
	// is the one this adapter registers.
	advanced: ArgSpec[];
	build(args: ToolArgs): HttpCall;
}

// str is Go's coerce: a boolean argument becomes "true"/"false" and an integer its
// decimal form, so a boolean argument reaches the API as Go's Build sends it (the API
// reads these as strings, and `include_pending=` alone would mean false).
const str = (a: ToolArgs, k: string): string => {
	const v = a[k];
	if (typeof v === "string") return v;
	if (typeof v === "boolean") return v ? "true" : "false";
	return typeof v === "number" && Number.isInteger(v) ? String(v) : "";
};
// Byte-identical to Go's url.QueryEscape / url.PathEscape, so the Pi and MCP clients
// issue the same requests (and the same Go argument digests).
const esc = (s: string): string =>
	encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
export const goQueryEscape = (s: string): string => esc(s).replace(/%20/g, "+");
const p = (s: string): string => esc(s).replace(/%(24|26|2B|3A|3D|40)/g, (m) => decodeURIComponent(m));
const ws = (a: ToolArgs, suffix: string): string => `/api/workspaces/${p(str(a, "workspace_id"))}${suffix}`;
const splitCSV = (s: string): string[] =>
	s
		.split(",")
		.map((x) => x.trim())
		.filter((x) => x !== "");
// query appends k=v pairs whose value is non-empty, in order (mcpserver's query()).
const query = (path: string, ...kv: string[]): string => {
	let sep = path.includes("?") ? "&" : "?";
	for (let i = 0; i + 1 < kv.length; i += 2) {
		if (kv[i + 1] === "") continue;
		path += `${sep}${kv[i]}=${goQueryEscape(kv[i + 1])}`;
		sep = "&";
	}
	return path;
};

const workspaceArg: ArgSpec = { name: "workspace_id", type: "string", desc: "workspace id; auto-resolved if omitted" };

// RECALL_DEFAULT_MAX_CHARS is recallDefaultMaxChars in api-go/internal/mcpserver/tool_recall.go.
export const RECALL_DEFAULT_MAX_CHARS = 4000;

// RECALL_QUERY_ARGS is recallQueryArgs in tool_recall.go: the arguments recall sends as
// query parameters under their own names, in Go's order, so a Pi call and an MCP call of
// the same arguments produce the same query string and the same Go argument digest.
export const RECALL_QUERY_ARGS = [
	"paths", "limit", "entry_id", "history", "explain", "kind", "tier", "tags", "topic",
	"path_prefix", "since", "until", "by", "status", "include_pending", "include_superseded", "show_expired",
	"include_merged", "subject", "predicate", "as_of", "valid_only", "seed", "cursor", "names_only", "render", "session_id", "source", "agent", "conversation",
] as const;
const recallQueryArgs: readonly string[] = RECALL_QUERY_ARGS;

// searchQueryArgs is searchQueryArgs in tool_search.go: the arguments search sends as
// query parameters under their own names, in Go's order.
const searchQueryArgs: readonly string[] = [
	"q", "mode", "lang", "seed", "limit", "cursor", "path_glob", "kinds", "depth", "include_body", "include_info", "substring",
];
// explainQueryArgs is explainQueryArgs in tool_explain.go.
const explainQueryArgs: readonly string[] = ["path", "symbol", "uid", "depth", "chain_depth", "include_content"];
// impactQuery is impactQuery in tool_impact.go: every impact argument in the order the
// API receives it.
const impactQuery: readonly string[] = [
	"path", "symbol", "from", "to", "direction", "scope", "base", "head", "worktree",
	"relation_types", "min_confidence", "include_tests", "max_depth", "limit", "offset", "summary_only", "timeout_ms",
];

// GROUND_DEFAULT_MAX_CHARS is groundbudget.DefaultMaxChars in api-go/internal/groundbudget.
export const GROUND_DEFAULT_MAX_CHARS = 6000;

// The per-tool output-budget defaults, from toolbudget.For's table: search 8000,
// explain 6000, impact 8000 (and ground's own 6000 above).
export const SEARCH_DEFAULT_MAX_CHARS = 8000;
export const EXPLAIN_DEFAULT_MAX_CHARS = 6000;
export const IMPACT_DEFAULT_MAX_CHARS = 8000;

// The output budget's shared bounds and per-tool defaults, from
// api-go/internal/toolbudget (MinMaxChars, MaxMaxChars, MaxLimit and For's table) and
// api-go/internal/groundbudget for ground. Go's budgetArgs builds max_chars from these,
// so the mirror advertises the same argument and sends the same default per tool.
export const MIN_MAX_CHARS = 2000;
export const MAX_MAX_CHARS = 65536;
export const BUDGET_MAX_LIMIT = 500;
// maxSearchCursorLen is the cursor bound the budget layer advertises (degrade.go).
export const MAX_CURSOR_LEN = 64;
// maxPathArg bounds a path argument (PATH_MAX on Linux): maxPathArg in tool_explain.go.
export const MAX_PATH_ARG = 4096;
// maxCharsArg is budgetArgs' output-budget argument for a toolbudget Paged or not.
const maxCharsArg = (defaultMaxChars: number): ArgSpec => ({
	name: "max_chars",
	type: "integer",
	minimum: MIN_MAX_CHARS,
	maximum: MAX_MAX_CHARS,
	desc: `result budget (default ${defaultMaxChars})`,
});
// explain is the one Paged spec: the budget layer pages its list with limit= and
// cursor= (budgetArgs in degrade.go).
const pagingArgs: ArgSpec[] = [
	{ name: "limit", type: "integer", minimum: 1, maximum: BUDGET_MAX_LIMIT, desc: "items per page" },
	{ name: "cursor", type: "string", maxLength: MAX_CURSOR_LEN, desc: "next_cursor of the previous page" },
];
// budgetMaxChars is Go's budgetQuery: the caller's max_chars, or the tool's own default.
// Every budgeted tool sends one, so a Pi call and an MCP call of the same tool ask for
// the same budget.
const budgetMaxChars = (a: ToolArgs, defaultMaxChars: number): string => {
	const given = str(a, "max_chars");
	return given === "" ? String(defaultMaxChars) : given;
};

export const TOOL_SPECS: readonly ToolSpec[] = [
	{
		name: "ground",
		description:
			"Orient before acting: what changed, is stale, broken or blocked since the baseline, with index drift and contract breaks (changed signatures).",
		args: [workspaceArg],
		// groundTool.Advanced in tool_ground.go.
		advanced: [
			{ name: "sections", type: "string", maxLength: 256, desc: "sections to return (summary always; default all)" },
			{ name: "max_chars", type: "integer", minimum: MIN_MAX_CHARS, maximum: MAX_MAX_CHARS, desc: "result budget in characters (default 6000)" },
			{ name: "since_revision", type: "integer", minimum: 0, maximum: 9007199254740991, desc: "memory_delta after this memory_revision" },
			{ name: "session_id", type: "string", maxLength: 200 },
			{ name: "query", type: "string", maxLength: 512, desc: "the task: adds relevant_sessions" },
		],
		build: (a) => ({
			method: "GET",
			path: query(
				ws(a, "/session-grounding"),
				"sections", str(a, "sections"),
				"max_chars", budgetMaxChars(a, GROUND_DEFAULT_MAX_CHARS),
				"since_revision", str(a, "since_revision"),
				"session_id", str(a, "session_id"),
				"query", str(a, "query"),
			),
		}),
	},
	{
		name: "recall",
		description:
			"Shared memory RANKED by query/paths (BM25+path+trust+recency+feedback); non-matches dropped (terms >=3 chars). No args: top-N by working-tree overlap, then recency. verification_mode: peer_verified|single_agent|self_asserted_open_mode (no auth/quorum). conflicts: claim/contradiction/path overlap.",
		args: [
			workspaceArg,
			{ name: "q", type: "string", desc: "task query to rank memories by" },
			{ name: "paths", type: "string", desc: "comma-separated repo-relative files to focus on" },
			{ name: "limit", type: "integer", minimum: 1, maximum: 50, desc: "max memories (default 8)" },
		],
		// recallTool.Advanced in tool_recall.go, in the same order. Every one is accepted
		// on tools/call and travels as a query parameter under its own name
		// (recallQueryArgs), which the unit test holds to.
		advanced: [
			{ name: "entry_id", type: "string", desc: "fetch one entry, any state" },
			{ name: "history", type: "boolean", desc: "with entry_id: revisions, events (verifier)" },
			{ name: "status", type: "string", enum: ["promoted", "pending", "awaiting_me"] },
			{ name: "include_pending", type: "boolean" },
			{ name: "include_superseded", type: "boolean" },
			{ name: "show_expired", type: "boolean" },
			{ name: "include_merged", type: "boolean" },
			{ name: "subject", type: "string", maxLength: 256 },
			{ name: "predicate", type: "string" },
			{ name: "as_of", type: "string", maxLength: 256 },
			{ name: "valid_only", type: "boolean" },
			{ name: "seed", type: "string", maxLength: MAX_PATH_ARG },
			{ name: "kind", type: "string", maxLength: 512 },
			{ name: "tier", type: "string", enum: ["core", "deferred"] },
			{ name: "tags", type: "string", maxLength: 1024 },
			{ name: "topic", type: "string" },
			{ name: "path_prefix", type: "string" },
			{ name: "since", type: "string" },
			{ name: "until", type: "string" },
			{ name: "by", type: "string" },
			{ name: "explain", type: "boolean" },
			{ name: "names_only", type: "boolean" },
			{ name: "render", type: "string", enum: ["full", "compact"] },
			{ name: "max_chars", type: "integer", minimum: 1000, maximum: 10000 },
			{ name: "cursor", type: "string" },
			{ name: "session_id", type: "string", maxLength: 128 },
			// WS-34: search past session transcripts instead of memory.
			{ name: "source", type: "string", enum: ["sessions"], desc: "past session transcripts" },
			{ name: "agent", type: "string", maxLength: 64 },
			{ name: "conversation", type: "string", maxLength: 256 },
		],
		// the MCP tool always budgets recall (max_chars, default 4000; tool_recall.go);
		// Pi sends the same default so both clients issue the same request. A fetch by
		// entry_id is not budgeted, as Go's Build also leaves it out.
		build: (a) => ({
			method: "GET",
			path: query(
				ws(a, "/context/active"),
				"query", str(a, "q"),
				...recallQueryArgs.flatMap((k) => [k, str(a, k)]),
				"max_chars",
				str(a, "entry_id") === "" ? budgetMaxChars(a, RECALL_DEFAULT_MAX_CHARS) : str(a, "max_chars"),
			),
		}),
	},
	{
		name: "remember",
		description:
			"Propose a durable memory (fact/decision/gotcha); pending until enough distinct agents verify it (open mode: promoted at once as self_asserted_open_mode). Pass content; optional title, paths (comma-separated files it is about, so recall flags it stale when they change).",
		args: [
			workspaceArg,
			{ name: "content", type: "string", desc: "the memory text to propose" },
			{ name: "title", type: "string", desc: "short title" },
			{ name: "paths", type: "string", desc: "comma-separated repo-relative files the memory is about" },
		],
		// rememberTool.Advanced in tool_remember.go: the write ops and the structured
		// claim surface. Every member travels in the JSON body under its own name, as
		// Go's Build sends it.
		advanced: [
			{ name: "op", type: "string", enum: ["propose", "supersede", "edit", "retire", "restore", "tier"], desc: "default propose" },
			{ name: "entry_id", type: "string", desc: "target of edit/retire/restore/tier" },
			{ name: "base_revision", type: "integer", minimum: 1, maximum: 1073741824, desc: "edit: revision read (CAS)" },
			{ name: "reason", type: "string", maxLength: 1000, desc: "required unless proposing" },
			{ name: "old_string", type: "string", desc: "edit: text to replace once" },
			{ name: "new_string", type: "string", desc: "edit: replacement, or appended" },
			{ name: "description", type: "string", desc: "one-line description (deferred index)" },
			{ name: "supersedes", type: "string", maxLength: 1024, desc: "entries this replaces" },
			{ name: "expires", type: "string", desc: "last UTC date served (YYYY-MM-DD)" },
			{ name: "evidence", type: "string", maxLength: 1024, desc: "evidence handles it rests on" },
			{ name: "run_id", type: "string", desc: "run it came from" },
			{
				name: "kind",
				type: "string",
				enum: ["project_knowledge", "decision", "constraint", "workflow", "procedure", "gotcha", "measurement", "convention", "handoff", "candidate", "maintenance"],
			},
			{ name: "topic", type: "string", desc: "a/b/c" },
			{ name: "tags", type: "string", maxLength: 1024 },
			{ name: "session_id", type: "string", maxLength: 200 },
			{ name: "claims", type: "string", maxLength: 4096 },
			{ name: "relations", type: "string", maxLength: 1024 },
			{ name: "tier", type: "string", enum: ["core", "deferred"], desc: "core needs a human approver" },
		],
		build: (a) => {
			// content travels in the JSON body, never the URL (XM-NEW-018)
			// Go marshals a map[string]any, so its keys are sorted; the members are set in
			// that order so both clients send byte-identical bodies (and digests).
			const payload: Record<string, unknown> = {};
			for (const k of ["base_revision", "content", "description", "entry_id", "expires", "kind", "new_string", "old_string", "op", "reason", "run_id", "session_id", "tier", "title", "topic"]) {
				const v = str(a, k);
				if (!v) continue;
				payload[k] = k === "base_revision" ? Number(v) : v;
			}
			for (const k of ["evidence", "paths", "supersedes", "tags"]) {
				const v = str(a, k);
				if (v) payload[k] = splitCSV(v);
			}
			const claimsRaw = str(a, "claims");
			if (claimsRaw) {
				const claims = claimsRaw
					.split(";")
					.map((c) => c.trim())
					.filter((c) => c !== "")
					.map((c) => {
						const parts = c.split("|");
						return { subject: (parts[0] ?? "").trim(), predicate: (parts[1] ?? "").trim(), object: (parts[2] ?? "").trim() };
					});
				if (claims.length > 0) payload.claims = claims;
			}
			const relationsRaw = str(a, "relations");
			if (relationsRaw) {
				const relations = splitCSV(relationsRaw).map((r) => {
					const at = r.indexOf(":");
					return at < 0 ? { kind: r.trim(), target: "" } : { kind: r.slice(0, at).trim(), target: r.slice(at + 1).trim() };
				});
				if (relations.length > 0) payload.relations = relations;
			}
			return { method: "POST", path: ws(a, "/context"), body: JSON.stringify(payload) };
		},
	},
	{
		name: "verify",
		description:
			"Verify (approve/reject) a peer's proposed memory; it promotes once enough DISTINCT agents approve. Your identity is your auth token; approve defaults true.",
		args: [
			workspaceArg,
			{ name: "entry_id", type: "string", required: true, desc: "the id of the memory entry" },
			{ name: "approve", type: "boolean", desc: "approve (default true) or reject" },
			{ name: "note", type: "string", maxLength: 1000, desc: "reason for the verdict, stored with the vote" },
		],
		// verifyTool.Advanced in tool_verify.go: the richer outcome vocabulary and the
		// revision a pending edit is voted on. All travel in the body.
		advanced: [
			{
				name: "outcome",
				type: "string",
				enum: ["approve", "reject", "retract", "duplicate_of", "helpful", "misleading", "stale_harm"],
				desc: "overrides approve",
			},
			{ name: "target", type: "string", desc: "duplicate_of: the kept entry" },
			{ name: "evidence_handle", type: "string", desc: "evidence the verdict rests on" },
			{ name: "revision", type: "integer", minimum: 1, maximum: 1073741824, desc: "pending edit to vote on" },
		],
		build: (a) => {
			const call: HttpCall = {
				method: "POST",
				path: `${ws(a, `/context/${p(str(a, "entry_id"))}/verify`)}?approve=${a.approve === false ? "false" : "true"}`,
			};
			// the note is stored text: body, like memory content (XM-NEW-018). Go marshals a
			// map, so its keys are sorted; the members are set in that order so both clients
			// send byte-identical bodies (and digests).
			const payload: Record<string, unknown> = {};
			for (const k of ["evidence_handle", "note", "outcome", "target"]) {
				const v = str(a, k);
				if (v) payload[k] = v;
			}
			const revision = str(a, "revision");
			if (revision) payload.revision = Number(revision);
			if (Object.keys(payload).length > 0) call.body = JSON.stringify(payload);
			return call;
		},
	},
	{
		name: "search",
		description:
			"Code search: path:line hits, snippets, reasons. RRF (K=60) of BM25 over function bodies, comments, names, paths and doc sections; identifier match; trigram typo tolerance (not meaning); reference degree; proximity to seed=<symbol>. mode=pattern: ast-grep (`$A && $A()`); mode=symbol: name path.",
		args: [
			workspaceArg,
			{ name: "q", type: "string", required: true, desc: "the search query" },
			{ name: "mode", type: "string", enum: ["hybrid", "pattern", "symbol"], desc: "hybrid (default), pattern (ast-grep) or symbol" },
			{ name: "lang", type: "string", desc: "language hint for pattern mode" },
			{ name: "seed", type: "string", desc: "symbol to anchor the graph-proximity lane" },
			{ name: "limit", type: "integer", minimum: 1, maximum: 50, desc: "max hits (default 10)" },
		],
		// searchTool.Advanced in tool_search.go: the symbol lookup's options, the path
		// filter, paging and the output budget.
		advanced: [
			maxCharsArg(SEARCH_DEFAULT_MAX_CHARS),
			{ name: "cursor", type: "string", maxLength: MAX_CURSOR_LEN, desc: "next_cursor of the previous page" },
			{ name: "path_glob", type: "string", maxLength: 256, desc: "only paths matching this glob (** crosses /)" },
			{ name: "kinds", type: "string", maxLength: 256, desc: "mode=symbol: Function,Method or -Variable" },
			{ name: "depth", type: "integer", minimum: 0, maximum: 3, desc: "mode=symbol: child levels" },
			{ name: "include_body", type: "boolean", desc: "mode=symbol: add bodies" },
			{ name: "include_info", type: "boolean", desc: "mode=symbol: add signatures, docs" },
			{ name: "substring", type: "boolean", desc: "mode=symbol: substring match" },
		],
		build: (a) => ({
			method: "GET",
			// searchQueryArgs in tool_search.go, then the output budget as Go appends it.
			path: query(
				ws(a, "/search"),
				...searchQueryArgs.flatMap((k) => [k, str(a, k)]),
				"max_chars", budgetMaxChars(a, SEARCH_DEFAULT_MAX_CHARS),
			),
		}),
	},
	{
		name: "explain",
		description: "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.",
		args: [workspaceArg, { name: "path", type: "string", maxLength: MAX_PATH_ARG, desc: "a repo-relative file or directory path" }],
		// explainTool.Advanced in tool_explain.go: a symbol's context, the outline's depth
		// and the output budget, which for explain also pages its list.
		advanced: [
			{ name: "symbol", type: "string", maxLength: MAX_PATH_ARG, desc: "a name path: its context" },
			{ name: "uid", type: "string", maxLength: MAX_PATH_ARG, desc: "a symbol UID: its context" },
			{ name: "depth", type: "integer", minimum: 0, maximum: 3, desc: "outline child levels" },
			{ name: "chain_depth", type: "integer", minimum: 0, maximum: 3, desc: "symbol=: extra edge layers" },
			{ name: "include_content", type: "boolean", desc: "symbol=: add its body" },
			maxCharsArg(EXPLAIN_DEFAULT_MAX_CHARS),
			...pagingArgs,
		],
		build: (a) => ({
			method: "GET",
			// explainQueryArgs in tool_explain.go, then the budget and its paging as Go
			// appends them (explain is the one Paged spec).
			path: query(
				ws(a, "/explain-path"),
				...explainQueryArgs.flatMap((k) => [k, str(a, k)]),
				"max_chars", budgetMaxChars(a, EXPLAIN_DEFAULT_MAX_CHARS),
				"limit", str(a, "limit"),
				"cursor", str(a, "cursor"),
			),
		}),
	},
	{
		name: "impact",
		description:
			"Blast radius. symbol= (name, Container.name, UID, path) → dependents by depth (d1 breaks), risk, completeness. Go/TS edges are resolved; other languages match names: leads, not proof. path= → a file's lexical referrers. from=&to= → shortest path, per-hop edges. No args → changes since the index baseline.",
		args: [
			workspaceArg,
			{ name: "symbol", type: "string", desc: "symbol to compute blast radius for" },
			{ name: "path", type: "string", maxLength: 4096, desc: "a repo-relative file" },
			{ name: "from", type: "string", desc: "trace path from this symbol" },
			{ name: "to", type: "string", desc: "trace path to this symbol" },
			{ name: "max_depth", type: "integer", minimum: 1, maximum: 32, desc: "hops (default 3; path= 4)" },
		],
		// impactTool.Advanced in tool_impact.go: the diff scopes, the walk's filters and
		// paging, and the output budget. Every argument goes to the API as Go's Build
		// sends it, and the API refuses one of another form (two of symbol, path and
		// from/to; max_depth alone) instead of it being dropped.
		advanced: [
			maxCharsArg(IMPACT_DEFAULT_MAX_CHARS),
			{ name: "direction", type: "string", enum: ["upstream", "downstream"], desc: "dependents (default) or dependencies" },
			{
				name: "scope",
				type: "string",
				enum: ["unstaged", "staged", "all", "workspace", "compare", "commit", "range"],
				desc: "symbols a diff touches, and their dependents",
			},
			{ name: "base", type: "string", maxLength: 256, desc: "scope=compare|range: base revision" },
			{ name: "head", type: "string", maxLength: 256, desc: "scope=compare|commit|range: head revision" },
			{ name: "worktree", type: "string", maxLength: MAX_PATH_ARG, desc: "another checkout of this repository to diff" },
			{ name: "relation_types", type: "string", maxLength: 128, desc: "edge kinds to walk (default CALLS,IMPORTS,EXTENDS,IMPLEMENTS)" },
			{ name: "min_confidence", type: "number", minimum: 0, maximum: 1, desc: "skip edges below this confidence" },
			{ name: "include_tests", type: "boolean", desc: "walk test files (default false)" },
			{ name: "limit", type: "integer", minimum: 1, maximum: BUDGET_MAX_LIMIT, desc: "items per depth tier (default 50)" },
			{ name: "offset", type: "integer", minimum: 0, maximum: 50000, desc: "items skipped per depth tier" },
			{ name: "summary_only", type: "boolean", desc: "counts, risk and envelope without items" },
			{ name: "timeout_ms", type: "integer", minimum: 1, maximum: 60000, desc: "walk budget (default 10000)" },
		],
		build: (a) => ({
			method: "GET",
			// impactQuery in tool_impact.go, with max_depth sent as the API's "depth", then
			// the output budget as Go appends it.
			path: query(
				ws(a, "/changes/since-index"),
				...impactQuery.flatMap((k) => [k === "max_depth" ? "depth" : k, str(a, k)]),
				"max_chars", budgetMaxChars(a, IMPACT_DEFAULT_MAX_CHARS),
			),
		}),
	},
	{
		name: "diagnostics",
		description: "Current normalized diagnostics (errors/warnings) for the workspace.",
		args: [workspaceArg],
		// diagnosticsTool takes no Advanced arguments.
		advanced: [],
		build: (a) => ({ method: "GET", path: ws(a, "/diagnostics") }),
	},
	{
		name: "why_failed",
		description: "Why a run_id, log or evidence_handle failed: error lines, implicated files.",
		args: [workspaceArg, { name: "run_id", type: "string", desc: "run/outcome id" }],
		// whyFailedTool.Advanced in tool_why_failed.go. command= is off unless the
		// operator enabled it and then needs an admin; the other sources only record.
		advanced: [
			{ name: "command", type: "string", maxLength: 4096, desc: "admins, if the operator enabled commands: test/build/lint argv to run" },
			{ name: "cwd", type: "string", maxLength: 1024, desc: "repo-relative directory to run command in" },
			{ name: "timeout_seconds", type: "integer", minimum: 1, maximum: 50, desc: "seconds before command's process group is killed (default 45)" },
			{ name: "evidence_handle", type: "string", maxLength: 256, desc: "retained tool output to explain from its last MiB; records the outcome" },
			{ name: "log", type: "string", maxLength: 1048576, desc: "pasted log to explain from its last MiB; records the outcome" },
		],
		build: (a) => {
			const write = ["command", "cwd", "evidence_handle", "log"].some((k) => str(a, k) !== "");
			if (!write && str(a, "run_id"))
				return { method: "GET", path: ws(a, `/runs/${p(str(a, "run_id"))}/why-failed`) };
			// Anything else is a recording call; the API rejects a missing or mixed source
			// (run_id travels along so a run_id with a source is refused, not ignored).
			// Go marshals a map, so its keys are sorted.
			const payload: Record<string, unknown> = {};
			for (const k of ["command", "cwd", "evidence_handle", "log", "run_id"]) {
				const v = str(a, k);
				if (v) payload[k] = v;
			}
			const timeout = str(a, "timeout_seconds");
			if (timeout) payload.timeout_seconds = Number(timeout);
			return { method: "POST", path: ws(a, "/why-failed"), body: JSON.stringify(payload) };
		},
	},
];

export const TOOL_NAMES: ReadonlySet<string> = new Set(TOOL_SPECS.map((t) => t.name));

// Pi's built-in tools whose results the adapter projects through the capture API and
// masks (not part of the MCP mirror above).
export const BUILTIN_TOOLS = ["bash", "read", "grep", "find", "ls", "edit", "write"] as const;
export type BuiltinTool = (typeof BUILTIN_TOOLS)[number];
// Built-ins that address one file through their `path` argument (edit/write change it).
export const FILE_TOOLS: ReadonlySet<string> = new Set(["read", "edit", "write"]);
export const EDIT_TOOLS: ReadonlySet<string> = new Set(["edit", "write"]);

// toJsonSchema renders a spec's input schema exactly as MCP `tools/list` does
// (mcpserver Tool.InputSchema): closed, and `required` only when non-empty. profile
// "lean" (the default) renders Args alone, as the server's default tools/list does;
// "full" renders Args then Advanced, as XMUSTARD_MCP_SCHEMA=full does. The Pi adapter
// registers the full profile, because Pi validates a call against its own closed schema
// and so can send only an argument that schema names.
export function toJsonSchema(t: ToolSpec, profile: "lean" | "full" = "lean"): Record<string, unknown> {
	const properties: Record<string, Record<string, unknown>> = {};
	for (const a of profile === "full" ? [...t.args, ...t.advanced] : t.args) {
		properties[a.name] = {
			type: a.type,
			// an empty description is noise in every session's prompt prefix, so Go omits
			// the key entirely (schema.go) and so does the mirror
			...(a.desc ? { description: a.desc } : {}),
			...(a.enum ? { enum: a.enum } : {}),
			...(a.type === "integer" || a.type === "number" ? { minimum: a.minimum, maximum: a.maximum } : {}),
			...(a.maxLength ? { maxLength: a.maxLength } : {}),
		};
	}
	const required = t.args.filter((a) => a.required).map((a) => a.name);
	return { type: "object", properties, ...(required.length > 0 ? { required } : {}), additionalProperties: false };
}

// allArgs is a spec's full argument surface, the order the full profile renders.
export function allArgs(t: ToolSpec): ArgSpec[] {
	return [...t.args, ...t.advanced];
}

// checkRequired returns an error for a missing/blank required argument, matching the
// MCP server's check (Pi has already validated types against the schema). The
// workspace is resolved before this runs (workspace.ts).
export function checkRequired(t: ToolSpec, args: ToolArgs): string | undefined {
	for (const a of t.args) {
		if (a.required && str(args, a.name).trim() === "") return `missing required argument "${a.name}" for ${t.name}`;
	}
	if (str(args, "workspace_id").trim() === "") return `no workspace_id for ${t.name}`;
	return undefined;
}
