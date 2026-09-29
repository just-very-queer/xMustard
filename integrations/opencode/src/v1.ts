// OpenCode 1.x entrypoint (the plugin module's `server`), checked against the 1.18.33
// source (packages/plugin/src/index.ts Hooks; opencode/src/session/tools.ts;
// tool/tool.ts). It registers the MCP server through the `config` hook and reduces
// results through "tool.execute.after", which 1.x passes (input, output) and reads
// back after the hook:
//
// - a built-in's output is {title, output, metadata}; the model reads output (already
//   truncated to 2,000 lines / 50 KiB before the hook), and metadata.output (bash) is
//   a copy of it;
// - an MCP tool's output is the MCP CallToolResult; the model reads its text blocks,
//   joined by blank lines, truncated after the hook.
//
// Failed calls never reach the 1.x hook, and masking has no 1.x seam here (its
// experimental.chat.messages.transform is not used).

import { Capturer, scalarMetadata, textOf, type ToolOutcome } from "./capture.ts";
import { loadConfig, SETUP_LOOKUP_MS } from "./config.ts";
import { mcpEntry } from "./mcp.ts";
import { Reducer } from "./reduce.ts";
import { WorkspaceResolver } from "./workspace.ts";

export interface V1PluginInput {
	directory: string;
}

export interface V1AfterInput {
	tool: string;
	sessionID: string;
	callID: string;
	args: unknown;
}

// V1Output is a built-in's {title, output, metadata} or an MCP CallToolResult.
export interface V1Output {
	title?: string;
	output?: unknown;
	metadata?: unknown;
	content?: unknown;
}

export interface V1Hooks {
	config(config: { mcp?: Record<string, unknown> }): Promise<void>;
	"tool.execute.after"(input: V1AfterInput, output: V1Output): Promise<void>;
}

// MCP_JOIN is how 1.x joins an MCP result's text blocks for the model.
const MCP_JOIN = "\n\n";

export function outcomeV1(i: V1AfterInput, out: V1Output): ToolOutcome | undefined {
	const call = { tool: i.tool, sessionID: i.sessionID, callID: i.callID, input: i.args };
	if (typeof out.output === "string") return { ...call, text: out.output, metadata: scalarMetadata(out.metadata) };
	const text = textOf(out.content, MCP_JOIN);
	return text === undefined ? undefined : { ...call, text };
}

export function applyV1(out: V1Output, text: string): void {
	if (typeof out.output === "string") out.output = text;
	else out.content = [{ type: "text", text }];
}

export async function serverV1(input: V1PluginInput, options?: Record<string, unknown>): Promise<V1Hooks> {
	const cfg = loadConfig(options ?? {});
	const workspaces = new WorkspaceResolver(cfg);
	const reducer = new Reducer(cfg, new Capturer(cfg), () => workspaces.resolve(input.directory), "opencode/1");
	const ws = cfg.registerMcp ? await workspaces.resolve(input.directory, SETUP_LOOKUP_MS).catch(() => undefined) : undefined;
	return {
		async config(config) {
			if (!cfg.registerMcp) return;
			config.mcp ??= {};
			config.mcp[cfg.serverName] ??= mcpEntry(cfg, ws);
		},
		async "tool.execute.after"(i, out) {
			try {
				const outcome = outcomeV1(i, out);
				const rep = outcome && (await reducer.reduce(outcome));
				if (rep) applyV1(out, rep.text);
			} catch {
				// fail open: a throwing hook fails the tool call
			}
		},
	};
}
