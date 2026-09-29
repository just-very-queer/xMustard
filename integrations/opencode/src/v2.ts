// OpenCode 2.x entrypoint (the plugin module's `setup`), written against
// @opencode/plugin 2.0.1 and checked against the installed OpenCode 2.0.1:
//
// - ctx.mcp.transform adds the xMustard MCP server entry unless the user's config
//   already names a server so (the editor's transform runs on every MCP reload).
// - ctx.tool.hook("execute.after") sees every tool call once it finished, completed or
//   failed. OpenCode builds the model's text of every tool, built-in or MCP, from
//   result.content (Xre in the 2.0.1 tool service); result.output is structured data
//   for the UI and code mode, and metadata never reaches the model. A large text
//   result is captured and its content replaced by Go's projection, which ends with
//   the recovery line; output is kept. A failed call's message is replaced the same
//   way, through the error's own constructor.
// - ctx.session.hook("context") masks older results (mask.ts).
//
// Calls a code-mode `execute` program makes run inside that call (same call id): the
// program reads their output, not the model, so they are left alone; the `execute`
// result itself, what the model reads, is reduced like any other.

import type { Plugin as PluginV2 } from "@opencode/plugin";
import { Capturer, scalarMetadata, textOf, type ToolOutcome } from "./capture.ts";
import { loadConfig, SETUP_LOOKUP_MS } from "./config.ts";
import { Masker } from "./mask.ts";
import { mcpEntry } from "./mcp.ts";
import { Reducer, type Replacement } from "./reduce.ts";
import { WorkspaceResolver } from "./workspace.ts";

export const CODE_MODE_TOOL = "execute";
const MAX_CODE_MODE_CALLS = 256;

// AfterInput is the tool "execute.after" hook input (@opencode/plugin 2.0.1
// promise/tool.d.ts ToolHooks): the call, then a completed result or an error, both
// assignable. OpenCode reads them back after every hook ran.
export interface AfterInput {
	readonly tool: string;
	readonly sessionID: string;
	readonly id: string;
	readonly input: unknown;
	readonly status: "completed" | "error";
	result?: { readonly output?: unknown; readonly content?: unknown; readonly metadata?: Readonly<Record<string, unknown>> };
	error?: { readonly message: string; readonly error?: unknown; readonly metadata?: unknown };
}

export function outcomeV2(i: AfterInput): ToolOutcome | undefined {
	const call = { tool: i.tool, sessionID: i.sessionID, callID: i.id, input: i.input };
	if (i.status === "error") return typeof i.error?.message === "string" ? { ...call, error: i.error.message } : undefined;
	const text = textOf(i.result?.content);
	return text === undefined ? undefined : { ...call, text, metadata: scalarMetadata(i.result?.metadata) };
}

// withMessage rebuilds a Tool.Error with another message through its own constructor
// (an Effect tagged error class), keeping its cause and metadata; undefined when the
// error cannot be rebuilt.
export function withMessage(err: AfterInput["error"], message: string): AfterInput["error"] | undefined {
	const Ctor = (err as { constructor?: unknown } | undefined)?.constructor;
	if (typeof Ctor !== "function" || Ctor === Object) return undefined;
	const props = { message, ...(err?.error !== undefined ? { error: err.error } : {}), ...(err?.metadata !== undefined ? { metadata: err.metadata } : {}) };
	try {
		return new (Ctor as new (p: typeof props) => AfterInput["error"])(props);
	} catch {
		return undefined;
	}
}

export function applyV2(i: AfterInput, rep: Replacement): void {
	if (i.status === "error") {
		const rebuilt = withMessage(i.error, rep.text);
		if (rebuilt) i.error = rebuilt;
		return;
	}
	// truncated: true marks the text as reduced and makes OpenCode skip its own head
	// truncation (ToolOutput.truncate returns a result that sets it unchanged), which
	// could cut the recovery line
	i.result = { ...i.result, content: rep.text, metadata: { ...i.result?.metadata, truncated: true, xmustard: rep.record } };
}

// CodeModeCalls tracks `execute` calls in flight: another tool finishing under one of
// their ids ran inside the program.
export class CodeModeCalls {
	private readonly ids = new Set<string>();
	start(id: string): void {
		this.ids.add(id);
		for (const oldest of this.ids) {
			if (this.ids.size <= MAX_CODE_MODE_CALLS) break;
			this.ids.delete(oldest);
		}
	}
	// inner reports a call made inside a running program; an `execute` call ends here.
	inner(i: Pick<AfterInput, "tool" | "id">): boolean {
		if (i.tool !== CODE_MODE_TOOL) return this.ids.has(i.id);
		this.ids.delete(i.id);
		return false;
	}
}

// afterV2 is the execute.after handler. It never throws: a throwing hook would fail
// the tool call, and every failure leaves OpenCode's result as it is.
export async function afterV2(reducer: Reducer, codeMode: CodeModeCalls, i: AfterInput): Promise<void> {
	try {
		if (codeMode.inner(i)) return;
		const outcome = outcomeV2(i);
		const rep = outcome && (await reducer.reduce(outcome));
		if (rep) applyV2(i, rep);
	} catch {
		// fail open
	}
}

export async function setupV2(ctx: PluginV2.Context): Promise<PluginV2.Cleanup> {
	const cfg = loadConfig(ctx.options);
	const dir = ctx.location.directory;
	const workspaces = new WorkspaceResolver(cfg);
	const reducer = new Reducer(cfg, new Capturer(cfg), () => workspaces.resolve(dir), `opencode/${ctx.app.version}`);
	const registrations: { dispose(): Promise<void> }[] = [];
	if (cfg.registerMcp) {
		const ws = await workspaces.resolve(dir, SETUP_LOOKUP_MS).catch(() => undefined);
		const entry = mcpEntry(cfg, ws);
		registrations.push(
			await ctx.mcp.transform((editor) => {
				if (!editor.get(cfg.serverName)) editor.set(cfg.serverName, entry);
			}),
		);
	}
	const codeMode = new CodeModeCalls();
	registrations.push(
		await ctx.tool.hook("execute.before", (i) => {
			if (i.tool === CODE_MODE_TOOL) codeMode.start(i.id);
		}),
	);
	registrations.push(await ctx.tool.hook("execute.after", (i) => afterV2(reducer, codeMode, i)));
	if (cfg.mask.enabled) {
		const masker = new Masker(cfg.mask, reducer);
		registrations.push(await ctx.session.hook("context", (c) => masker.apply(c)));
	}
	return async () => {
		await Promise.allSettled(registrations.map((r) => r.dispose()));
	};
}
