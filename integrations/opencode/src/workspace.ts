// Which xMustard workspace a directory belongs to, resolved as the MCP server does
// (api-go/internal/mcpserver/resolve_workspace.go): XMUSTARD_WORKSPACE_ID (or the
// workspaceId option) first, then the registered workspace with the longest root
// containing the directory. The plugin never registers a repository; an unregistered
// directory is an explicit error, and a capture is then skipped.

import { realpathSync } from "node:fs";
import path from "node:path";
import type { PluginConfig } from "./config.ts";
import { getJSON } from "./http.ts";

const LIST_MAX_BYTES = 1 << 20;
export const LIST_TIMEOUT_MS = 5_000;

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
	private readonly cfg: PluginConfig;
	private readonly byDir = new Map<string, string>();
	constructor(cfg: PluginConfig) {
		this.cfg = cfg;
	}

	async resolve(dir: string, timeoutMs = LIST_TIMEOUT_MS): Promise<string> {
		if (this.cfg.workspaceId) return this.cfg.workspaceId;
		const abs = canonical(dir);
		const cached = this.byDir.get(abs);
		if (cached) return cached;
		const listed = await getJSON(this.cfg, "/api/workspaces", timeoutMs, LIST_MAX_BYTES);
		let best: { id: string; root: string } | undefined;
		for (const w of Array.isArray(listed) ? (listed as Registered[]) : []) {
			if (!w?.root_path || !w.workspace_id) continue;
			const root = canonical(w.root_path);
			if (within(root, abs) && (!best || root.length > best.root.length)) best = { id: w.workspace_id, root };
		}
		if (!best) throw new Error(`no xMustard workspace contains ${abs}; set XMUSTARD_WORKSPACE_ID or load the repository`);
		this.byDir.set(abs, best.id);
		return best.id;
	}
}
