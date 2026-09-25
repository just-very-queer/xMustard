// xMustard Pi extension: the nine xMustard tools as direct HTTP-backed Pi tools, plus
// `xmustard_expand`, inactive until a result carries a recovery handle. Pi's built-in
// tools (bash, read, grep, find, ls, edit, write) are projected through xMustard's
// capture route, and older tool results are masked at turn_end in polling windows.
//
// Load-time work is registration only: no sidecar, socket, timer or network call.
// Configuration (see README.md): XMUSTARD_API_BASE, optional XMUSTARD_TOKEN (sent as
// a bearer token, never logged), optional XMUSTARD_WORKSPACE_ID (else the workspace
// is resolved from Pi's working directory), XMUSTARD_PI_DELIVERY=source|hook,
// lower-only XMUSTARD_PI_TOOL_TIMEOUT_MS / XMUSTARD_PI_PROJECTION_TIMEOUT_MS /
// XMUSTARD_PI_PROJECTION_TARGET_BYTES, XMUSTARD_PI_BUILTINS, XMUSTARD_PI_MASK*.

import type { ExtensionAPI, ExtensionContext, SessionBoundaryDraft } from "@earendil-works/pi-coding-agent";
import { type TSchema, Type } from "typebox";
import { loadConfig } from "./config.ts";
import { Capturer, EXPAND_TOOL, expand, PAGE_SIZE, PendingCalls, projectBuiltin, projectResult, runTool } from "./delivery.ts";
import { createMasker } from "./masking.ts";
import { TOOL_NAMES, TOOL_SPECS, type ToolArgs, type ToolSpec } from "./tools.ts";
import { callerTools, WorkspaceResolver } from "./workspace.ts";

// toolParameters renders a spec as TypeBox, serializing to exactly toJsonSchema (the
// MCP tools/list inputSchema).
export function toolParameters(spec: ToolSpec): TSchema {
	const props: Record<string, TSchema> = {};
	for (const a of spec.args) {
		let schema: TSchema;
		if (a.type === "boolean") schema = Type.Boolean({ description: a.desc });
		else if (a.type === "integer") schema = Type.Integer({ description: a.desc, minimum: a.minimum, maximum: a.maximum });
		// {type:"string", enum} exactly as MCP tools/list (same shape as pi-ai's StringEnum)
		else if (a.enum) schema = Type.Unsafe<string>({ type: "string", description: a.desc, enum: a.enum });
		else schema = Type.String(a.maxLength ? { description: a.desc, maxLength: a.maxLength } : { description: a.desc });
		props[a.name] = a.required ? schema : Type.Optional(schema);
	}
	return Type.Object(props, { additionalProperties: false });
}

const ExpandParameters = Type.Object(
	{
		workspace_id: Type.String({ description: "the workspace id the handle was issued in" }),
		handle: Type.String({ description: "the xm1.… recovery handle from an [xmustard evidence] line or [xmustard masked: …] stub" }),
		offset: Type.Optional(Type.Integer({ minimum: 0, description: "byte offset into the original (next_offset of the previous page)" })),
		length: Type.Optional(Type.Integer({ minimum: 1, maximum: PAGE_SIZE, description: `bytes to read (max ${PAGE_SIZE})` })),
		pattern: Type.Optional(Type.String({ description: "search the original for this RE2 pattern instead of paging" })),
		query: Type.Optional(Type.String({ description: "search for this literal text (case-insensitive)" })),
		lines: Type.Optional(Type.String({ description: "line range A-B to return (or to search within)" })),
		max_matches: Type.Optional(Type.Integer({ minimum: 1, maximum: 200, description: "match cap (default 40)" })),
		start_line: Type.Optional(Type.Integer({ minimum: 1, description: "next_line of the previous search; required with offset=next_offset to resume it" })),
	},
	{ additionalProperties: false },
);

function sessionIdOf(ctx: ExtensionContext | undefined): string | undefined {
	try {
		return ctx?.sessionManager.getSessionId();
	} catch {
		return undefined;
	}
}

export default function xmustard(pi: ExtensionAPI): void {
	const cfg = loadConfig();
	const pending = new PendingCalls();
	const workspaces = new WorkspaceResolver(cfg);
	const capturer = new Capturer(cfg);
	// built-in projection and masking resolve the session's workspace
	// within the projection deadline (the resolver caches it per directory)
	const resolveWorkspace = async (cwd: string | undefined, signal?: AbortSignal): Promise<string> => {
		const deadline = AbortSignal.timeout(cfg.projectionTimeoutMs);
		return String((await workspaces.resolve({}, cwd, signal ? AbortSignal.any([signal, deadline]) : deadline)).workspace_id);
	};

	for (const spec of TOOL_SPECS) {
		pi.registerTool({
			name: spec.name,
			label: `xMustard ${spec.name}`,
			description: spec.description,
			parameters: toolParameters(spec),
			async execute(toolCallId, params, signal, _onUpdate, ctx) {
				const args = await workspaces.resolve(params as ToolArgs, ctx?.cwd, signal);
				return runTool(cfg, spec, args, { toolCallId, sessionId: sessionIdOf(ctx) }, signal, pending);
			},
		});
	}

	pi.registerTool({
		name: EXPAND_TOOL,
		label: "xMustard expand",
		description:
			"Read the exact original bytes behind a reduced or masked xMustard result, one page at a time (max 64 KiB), or search it. Pass the workspace_id and handle from its [xmustard evidence] line or [xmustard masked: …] stub; start at offset 0 and continue from next_offset until eof, or pass pattern (RE2), query or lines=A-B to get matching lines with numbers. Results report whether the capture is current, stale or of unknown freshness; serve captured bytes only.",
		parameters: ExpandParameters,
		async execute(_toolCallId, params, signal) {
			return expand(cfg, params, signal);
		},
	});

	const activateExpand = (): void => {
		const active = pi.getActiveTools();
		if (!active.includes(EXPAND_TOOL)) pi.setActiveTools([...active, EXPAND_TOOL]);
	};

	// Registered tools start active; expansion stays hidden until a handle is issued,
	// and xMustard tools this caller cannot use are deactivated, so they never reach
	// the model (registration itself stays load-time only, without network calls).
	pi.on("session_start", async () => {
		let active = pi.getActiveTools().filter((n) => n !== EXPAND_TOOL);
		const allowed = await callerTools(cfg);
		if (allowed) active = active.filter((n) => !TOOL_NAMES.has(n) || allowed.has(n));
		pi.setActiveTools(active);
	});

	pi.on("tool_result", async (event, ctx) => {
		const meta = { sessionId: sessionIdOf(ctx), signal: ctx.signal };
		if (TOOL_NAMES.has(event.toolName)) return projectResult(cfg, event, pending, meta, activateExpand);
		if (cfg.builtins.has(event.toolName)) {
			return projectBuiltin(cfg, capturer, (signal) => resolveWorkspace(ctx.cwd, signal), event, meta, activateExpand);
		}
		return undefined; // other tools pass through untouched
	});

	// masking returns the drafts earlier handlers proposed plus its context edits
	const mask = createMasker({ cfg: cfg.mask, capturer, resolveWorkspace, onHandle: activateExpand });
	pi.on("turn_end", async (event, ctx) => {
		const out = await mask(event, ctx);
		return out ? { entries: out.entries as SessionBoundaryDraft[] } : undefined;
	});
}
