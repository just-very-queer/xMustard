// The nine xMustard tools, mirrored from api-go/cmd/xmustard-mcp/main.go `tools()`.
//
// Names, descriptions, argument schemas and the method/path/body each call maps to
// must stay identical to the stdio MCP server: the e2e script diffs `toJsonSchema`
// against the live `tools/list` answer. This module is plain data (no TypeBox) so the
// conformance check and unit tests run without Pi's module aliases.

export interface ArgSpec {
	name: string;
	type: "string" | "boolean";
	enum?: string[];
	desc: string;
}

export interface HttpCall {
	method: "GET" | "POST";
	path: string;
	body?: string;
}

export type ToolArgs = Record<string, string | boolean | undefined>;

export interface ToolSpec {
	name: string;
	description: string;
	required: string[]; // always string-typed
	optional: ArgSpec[];
	build(args: ToolArgs): HttpCall;
}

const str = (a: ToolArgs, k: string): string => {
	const v = a[k];
	return typeof v === "string" ? v : "";
};
// Byte-identical to Go's url.QueryEscape / url.PathEscape, so the Pi and MCP clients
// issue the same requests (and the same Go argument digests).
const esc = (s: string): string =>
	encodeURIComponent(s).replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
export const goQueryEscape = (s: string): string => esc(s).replace(/%20/g, "+");
const q = goQueryEscape;
const p = (s: string): string => esc(s).replace(/%(24|26|2B|3A|3D|40)/g, (m) => decodeURIComponent(m));
const ws = (a: ToolArgs, suffix: string): string => `/api/workspaces/${p(str(a, "workspace_id"))}${suffix}`;
const splitCSV = (s: string): string[] =>
	s
		.split(",")
		.map((x) => x.trim())
		.filter((x) => x !== "");

export const TOOL_SPECS: readonly ToolSpec[] = [
	{
		name: "ground",
		description:
			"Orient before acting: what changed / what's stale / what's broken / what's blocked since the indexed baseline, with index-trust (drift) and any contract breaks (changed function signatures vs the baseline) included.",
		required: ["workspace_id"],
		optional: [],
		build: (a) => ({ method: "GET", path: ws(a, "/session-grounding") }),
	},
	{
		name: "recall",
		description:
			"The VERIFIED shared context to trust, RANKED to your task: pass a query and/or paths to get the few relevant facts (multi-signal: lexical + path overlap + verification strength), not a dump. No query → recency-ranked top-N.",
		required: ["workspace_id"],
		optional: [
			{ name: "query", type: "string", desc: "task query to rank memories by" },
			{ name: "paths", type: "string", desc: "comma-separated repo-relative files to focus on" },
		],
		build: (a) => {
			let path = ws(a, "/context/active");
			let sep = "?";
			if (str(a, "query")) {
				path += `${sep}query=${q(str(a, "query"))}`;
				sep = "&";
			}
			if (str(a, "paths")) path += `${sep}paths=${q(str(a, "paths"))}`;
			return { method: "GET", path };
		},
	},
	{
		name: "remember",
		description:
			"Propose a durable memory (fact/decision/gotcha) for the shared context; pending until verified by enough agents. Pass content; optional title and paths (comma-separated files the memory is about, so recall can flag it stale when they change).",
		required: ["workspace_id", "content"],
		optional: [
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
		required: ["workspace_id", "entry_id"],
		optional: [{ name: "approve", type: "boolean", desc: "approve (default true) or reject" }],
		build: (a) => ({
			method: "POST",
			path: `${ws(a, `/context/${p(str(a, "entry_id"))}/verify`)}?approve=${a.approve === false ? "false" : "true"}`,
		}),
	},
	{
		name: "search",
		description:
			"Narrow code search over the repo, returning relevant slices (path:line), not a dump. Default mode is hybrid (lexical+semantic+structural+proximity). Pass seed=<symbol> to anchor a graph-PROXIMITY lane that pulls symbols structurally near that symbol up the ranking (auto-seeds from an exact query→symbol match otherwise). Pass mode=pattern to run an ast-grep STRUCTURAL query (query is the pattern, e.g. `$A && $A()`; optional lang).",
		required: ["workspace_id", "query"],
		optional: [
			{ name: "mode", type: "string", enum: ["hybrid", "pattern"], desc: "hybrid (default) or pattern (ast-grep)" },
			{ name: "lang", type: "string", desc: "language hint for pattern mode" },
			{ name: "seed", type: "string", desc: "symbol to anchor the graph-proximity lane" },
		],
		build: (a) => {
			let path = `${ws(a, "/search")}?q=${q(str(a, "query"))}`;
			for (const k of ["mode", "lang", "seed"]) if (str(a, k)) path += `&${k}=${q(str(a, k))}`;
			return { method: "GET", path };
		},
	},
	{
		name: "explain",
		description: "Explain a file or directory: purpose, role, key symbols, and how to run/verify it.",
		required: ["workspace_id", "path"],
		optional: [],
		build: (a) => ({ method: "GET", path: `${ws(a, "/explain-path")}?path=${q(str(a, "path"))}` }),
	},
	{
		name: "impact",
		description:
			"Blast radius. No args → impact of the current changes (dirty symbols, with contract_break flags where a signature changed vs the baseline). symbol= → every file that transitively references that symbol (graph BFS). from= & to= → the shortest dependency path between two symbols.",
		required: ["workspace_id"],
		optional: [
			{ name: "symbol", type: "string", desc: "symbol to compute blast radius for" },
			{ name: "from", type: "string", desc: "trace path from this symbol" },
			{ name: "to", type: "string", desc: "trace path to this symbol" },
		],
		build: (a) => {
			let query = "";
			if (str(a, "from") && str(a, "to")) query = `?from=${q(str(a, "from"))}&to=${q(str(a, "to"))}`;
			else if (str(a, "symbol")) query = `?symbol=${q(str(a, "symbol"))}`;
			return { method: "GET", path: ws(a, "/changes/since-index") + query };
		},
	},
	{
		name: "diagnostics",
		description: "Current normalized diagnostics (errors/warnings) for the workspace.",
		required: ["workspace_id"],
		optional: [],
		build: (a) => ({ method: "GET", path: ws(a, "/diagnostics") }),
	},
	{
		name: "why_failed",
		description: "Explain why a run failed: failure signals, salient error lines, and which changed files are implicated.",
		required: ["workspace_id", "run_id"],
		optional: [],
		build: (a) => ({ method: "GET", path: ws(a, `/runs/${p(str(a, "run_id"))}/why-failed`) }),
	},
];

export const TOOL_NAMES: ReadonlySet<string> = new Set(TOOL_SPECS.map((t) => t.name));

// requiredDesc mirrors the MCP server's descriptions for required (string) args.
export function requiredDesc(name: string): string {
	switch (name) {
		case "issue_id":
			return "the issue/bug id";
		case "entry_id":
			return "the id of the memory entry";
		case "run_id":
			return "the id of the run";
		case "path":
			return "a repo-relative file or directory path";
		case "query":
			return "the search query";
		case "content":
			return "the memory text to propose";
		case "symbol":
			return "a symbol name";
		default:
			return "the workspace id";
	}
}

// toJsonSchema renders a spec's input schema exactly as MCP `tools/list` does.
export function toJsonSchema(t: ToolSpec): Record<string, unknown> {
	const properties: Record<string, Record<string, unknown>> = {};
	for (const r of t.required) properties[r] = { type: "string", description: requiredDesc(r) };
	for (const o of t.optional) {
		properties[o.name] = { type: o.type, description: o.desc, ...(o.enum ? { enum: o.enum } : {}) };
	}
	return { type: "object", properties, required: t.required, additionalProperties: false };
}

// checkRequired returns an error for a missing/blank required argument, matching the
// MCP server's defensive check (Pi has already validated types against the schema).
export function checkRequired(t: ToolSpec, args: ToolArgs): string | undefined {
	for (const r of t.required) {
		if (str(args, r).trim() === "") return `missing required argument "${r}" for ${t.name}`;
	}
	return undefined;
}
