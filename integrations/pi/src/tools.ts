// The nine xMustard tools, mirrored from api-go/internal/mcpserver/tool_<name>.go.
//
// Names, descriptions, argument schemas and the method/path/body each call maps to
// must stay identical to the MCP server: the unit test compares `toJsonSchema` with
// the generated tools/list snapshot (api-go/internal/mcpserver/testdata/tools_list.json)
// and the e2e script diffs it against the live `tools/list` answer. Hidden argument
// aliases (such as `query` for `q`) are MCP compatibility only: Pi validates calls
// against these closed schemas, so it never receives them. This module is plain data
// (no TypeBox) so the conformance check and unit tests run without Pi's module aliases.

export interface ArgSpec {
	name: string;
	type: "string" | "boolean" | "integer";
	required?: boolean;
	enum?: string[];
	minimum?: number; // integer bounds (inclusive)
	maximum?: number;
	maxLength?: number; // string length cap in characters
	desc: string;
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
	args: ArgSpec[];
	build(args: ToolArgs): HttpCall;
}

const str = (a: ToolArgs, k: string): string => {
	const v = a[k];
	if (typeof v === "string") return v;
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

export const TOOL_SPECS: readonly ToolSpec[] = [
	{
		name: "ground",
		description:
			"Orient before acting: what changed, is stale, broken or blocked since the baseline, with index drift and contract breaks (changed signatures).",
		args: [workspaceArg],
		build: (a) => ({ method: "GET", path: ws(a, "/session-grounding") }),
	},
	{
		name: "recall",
		description:
			"Shared memory RANKED by query/paths (lexical, path overlap, approvals); non-matches dropped (terms >=3 chars). No args: top-N by working-tree overlap, then recency. verification_mode: peer_verified | single_agent | self_asserted_open_mode (no auth/quorum). conflicts: path overlap, not contradiction.",
		args: [
			workspaceArg,
			{ name: "q", type: "string", desc: "task query to rank memories by" },
			{ name: "paths", type: "string", desc: "comma-separated repo-relative files to focus on" },
			{ name: "limit", type: "integer", minimum: 1, maximum: 50, desc: "max memories (default 8)" },
		],
		build: (a) => ({
			method: "GET",
			path: query(ws(a, "/context/active"), "query", str(a, "q"), "paths", str(a, "paths"), "limit", str(a, "limit")),
		}),
	},
	{
		name: "remember",
		description:
			"Propose a durable memory (fact/decision/gotcha); pending until enough distinct agents verify it (open mode: promoted at once as self_asserted_open_mode). Pass content; optional title, paths (comma-separated files it is about, so recall flags it stale when they change).",
		args: [
			workspaceArg,
			{ name: "content", type: "string", required: true, desc: "the memory text to propose" },
			{ name: "title", type: "string", desc: "short title" },
			{ name: "paths", type: "string", desc: "comma-separated repo-relative files the memory is about" },
		],
		build: (a) => {
			// content travels in the JSON body, never the URL (XM-NEW-018)
			const payload: Record<string, unknown> = { content: str(a, "content") };
			if (str(a, "title")) payload.title = str(a, "title");
			if (str(a, "paths")) payload.paths = splitCSV(str(a, "paths"));
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
		build: (a) => {
			const call: HttpCall = {
				method: "POST",
				path: `${ws(a, `/context/${p(str(a, "entry_id"))}/verify`)}?approve=${a.approve === false ? "false" : "true"}`,
			};
			// the note is stored text: body, like memory content (XM-NEW-018)
			if (str(a, "note")) call.body = JSON.stringify({ note: str(a, "note") });
			return call;
		},
	},
	{
		name: "search",
		description:
			"Code search, path:line slices. Hybrid ranks symbol NAMES, paths and doc chunks, not function bodies: RRF of lexical IDF, trigram fuzzy match (typo tolerance, not meaning, unless built with semantic-onnx and XMUSTARD_EMBED_MODEL set), reference degree, proximity to seed=<symbol>. mode=pattern: ast-grep structural query (e.g. `$A && $A()`; optional lang).",
		args: [
			workspaceArg,
			{ name: "q", type: "string", required: true, desc: "the search query" },
			{ name: "mode", type: "string", enum: ["hybrid", "pattern"], desc: "hybrid (default) or pattern (ast-grep)" },
			{ name: "lang", type: "string", desc: "language hint for pattern mode" },
			{ name: "seed", type: "string", desc: "symbol to anchor the graph-proximity lane" },
			{ name: "limit", type: "integer", minimum: 1, maximum: 50, desc: "max hits (default 25)" },
		],
		build: (a) => ({
			method: "GET",
			path: query(ws(a, "/search"), ...["q", "mode", "lang", "seed", "limit"].flatMap((k) => [k, str(a, k)])),
		}),
	},
	{
		name: "explain",
		description: "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.",
		args: [workspaceArg, { name: "path", type: "string", required: true, desc: "a repo-relative file or directory path" }],
		build: (a) => ({ method: "GET", path: query(ws(a, "/explain-path"), "path", str(a, "path")) }),
	},
	{
		name: "impact",
		description:
			"Blast radius over a LEXICAL reference graph (name matches + import lines, not resolved calls): distance≥1 edges are leads to confirm, not proof. No args → current changes (dirty symbols, contract_break). symbol= → files referencing its defining files, ≤4 hops. from=&to= → shortest undirected file path.",
		args: [
			workspaceArg,
			{ name: "symbol", type: "string", desc: "symbol to compute blast radius for" },
			{ name: "from", type: "string", desc: "trace path from this symbol" },
			{ name: "to", type: "string", desc: "trace path to this symbol" },
			{ name: "max_depth", type: "integer", minimum: 1, maximum: 4, desc: "max hops for symbol= (default 4)" },
		],
		build: (a) => {
			let path = ws(a, "/changes/since-index");
			if (str(a, "from") && str(a, "to")) path = query(path, "from", str(a, "from"), "to", str(a, "to"));
			else if (str(a, "symbol")) path = query(path, "symbol", str(a, "symbol"), "depth", str(a, "max_depth"));
			return { method: "GET", path };
		},
	},
	{
		name: "diagnostics",
		description: "Current normalized diagnostics (errors/warnings) for the workspace.",
		args: [workspaceArg],
		build: (a) => ({ method: "GET", path: ws(a, "/diagnostics") }),
	},
	{
		name: "why_failed",
		description: "Explain why a run failed: failure signals, salient error lines, and which changed files are implicated.",
		args: [workspaceArg, { name: "run_id", type: "string", required: true, desc: "the id of the run" }],
		build: (a) => ({ method: "GET", path: ws(a, `/runs/${p(str(a, "run_id"))}/why-failed`) }),
	},
];

export const TOOL_NAMES: ReadonlySet<string> = new Set(TOOL_SPECS.map((t) => t.name));

// toJsonSchema renders a spec's input schema exactly as MCP `tools/list` does
// (mcpserver Tool.InputSchema): closed, and `required` only when non-empty.
export function toJsonSchema(t: ToolSpec): Record<string, unknown> {
	const properties: Record<string, Record<string, unknown>> = {};
	for (const a of t.args) {
		properties[a.name] = {
			type: a.type,
			description: a.desc,
			...(a.enum ? { enum: a.enum } : {}),
			...(a.type === "integer" ? { minimum: a.minimum, maximum: a.maximum } : {}),
			...(a.maxLength ? { maxLength: a.maxLength } : {}),
		};
	}
	const required = t.args.filter((a) => a.required).map((a) => a.name);
	return { type: "object", properties, ...(required.length > 0 ? { required } : {}), additionalProperties: false };
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
