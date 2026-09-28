// The xMustard MCP server entry the plugin registers: a remote (Streamable HTTP) server
// at the API's /mcp endpoint. OpenCode 2.0.1 (@opencode/schema mcp.ts RemoteConfig)
// and 1.18.33 (core/src/v1/config/mcp.ts) take the same fields: url, headers, and
// oauth false so OpenCode does not try OAuth discovery against the API. The workspace
// binds the session (HTTP carries no working directory); without one the server falls
// back to the client's MCP roots.

import type { PluginConfig } from "./config.ts";

export interface RemoteServerEntry {
	type: "remote";
	url: string;
	headers: Record<string, string>;
	oauth: false;
}

export function mcpEntry(cfg: PluginConfig, workspaceId?: string): RemoteServerEntry {
	const query = new URLSearchParams({ client: "opencode" });
	const headers: Record<string, string> = {};
	if (cfg.token) headers.Authorization = `Bearer ${cfg.token}`;
	if (workspaceId) {
		query.set("workspace", workspaceId);
		headers["X-Xmustard-Workspace"] = workspaceId;
	}
	return { type: "remote", url: `${cfg.apiBase}/mcp?${query}`, headers, oauth: false };
}
