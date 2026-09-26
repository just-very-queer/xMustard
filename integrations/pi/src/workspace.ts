// Who the caller is and where it works: callerTools scopes the nine tools to the
// caller (GET /api/auth/whoami). Workspace resolution for calls that omit
// workspace_id works as the MCP server's does
// (api-go/internal/mcpserver/resolve_workspace.go): XMUSTARD_WORKSPACE_ID first, then
// Pi's working directory, mapped to the registered workspace with the longest root
// containing it. Unlike the MCP server the adapter never registers a repository; an
// unregistered directory is an explicit error.

import { realpathSync } from "node:fs";
import path from "node:path";
import type { AdapterConfig } from "./config.ts";
import { errorFromResponse, send } from "./http.ts";
import type { ToolArgs } from "./tools.ts";

const LIST_MAX_BYTES = 1 << 20;
const LIST_TIMEOUT_MS = 10_000;

interface Registered {
	workspace_id: string;
	root_path: string;
}

function canonical(p: string): string {
	const clean = path.resolve(p);
	try {
		return realpathSync(clean);
	} catch {
		return clean;
	}
}

// within reports whether p is root or below it.
function within(root: string, p: string): boolean {
	const rel = path.relative(root, p);
	return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

export class WorkspaceResolver {
	private readonly byDir = new Map<string, string>();
	private readonly cfg: AdapterConfig;
	constructor(cfg: AdapterConfig) {
		this.cfg = cfg;
	}

	// resolve returns args with workspace_id filled in, or throws a bounded error that
	// names what was tried.
	async resolve(args: ToolArgs, cwd: string | undefined, signal?: AbortSignal): Promise<ToolArgs> {
		if (typeof args.workspace_id === "string" && args.workspace_id.trim() !== "") return args;
		if (this.cfg.workspaceId) return { ...args, workspace_id: this.cfg.workspaceId };
		const tried = "no workspace_id argument; XMUSTARD_WORKSPACE_ID is unset";
		if (!cwd) throw new Error(`no workspace resolved (${tried}; Pi reported no working directory). Pass workspace_id.`);
		const dir = canonical(cwd);
		const cached = this.byDir.get(dir);
		if (cached) return { ...args, workspace_id: cached };
		const list = await this.list(signal);
		let best: Registered | undefined;
		for (const w of list) {
			if (!w.root_path) continue;
			const root = canonical(w.root_path);
			if (within(root, dir) && (!best || root.length > canonical(best.root_path).length)) best = w;
		}
		if (!best) {
			const known = list
				.slice(0, 5)
				.map((w) => `${w.workspace_id} (${w.root_path})`)
				.join(", ");
			throw new Error(
				`no workspace resolved (${tried}; working directory ${dir} is not inside a registered workspace). Pass workspace_id${known ? `; registered: ${known}` : ""}.`,
			);
		}
		this.byDir.set(dir, best.workspace_id);
		return { ...args, workspace_id: best.workspace_id };
	}

	private async list(signal?: AbortSignal): Promise<Registered[]> {
		const res = await send(this.cfg, { method: "GET", path: "/api/workspaces", timeoutMs: LIST_TIMEOUT_MS, signal, maxBytes: LIST_MAX_BYTES });
		if (res.status !== 200) throw errorFromResponse("GET /api/workspaces", res);
		const parsed = JSON.parse(new TextDecoder().decode(res.body)) as unknown;
		return Array.isArray(parsed) ? (parsed as Registered[]) : [];
	}
}

// Bound on the whoami lookup at session start: a slow API never delays Pi by more.
export const CALLER_TOOLS_TIMEOUT_MS = 2_000;

// callerTools asks the API which of the nine tools this caller may use on this
// deployment (GET /api/auth/whoami "tools": roles, read-only mode, disabled tools and
// profile applied), as the MCP shim does for tools/list. It returns undefined when
// the API cannot say (unreachable, 401, an older API without the field); every tool
// then stays active and the API still enforces each call.
export async function callerTools(cfg: AdapterConfig, signal?: AbortSignal): Promise<ReadonlySet<string> | undefined> {
	try {
		const res = await send(cfg, { method: "GET", path: "/api/auth/whoami", timeoutMs: CALLER_TOOLS_TIMEOUT_MS, signal, maxBytes: 64 << 10 });
		if (res.status !== 200) throw errorFromResponse("GET /api/auth/whoami", res);
		const who = JSON.parse(new TextDecoder().decode(res.body)) as { tools?: unknown };
		if (!Array.isArray(who.tools)) return undefined;
		return new Set(who.tools.filter((t): t is string => typeof t === "string"));
	} catch {
		return undefined;
	}
}
