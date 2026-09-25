// xMustard Pi extension: the nine xMustard tools as direct HTTP-backed Pi tools, plus
// `xmustard_expand`, inactive until a result carries a recovery handle.
//
// Load-time work is registration only: no sidecar, socket, timer or network call.
// Configuration (see README.md): XMUSTARD_API_BASE, optional XMUSTARD_TOKEN (sent as
// a bearer token, never logged), optional XMUSTARD_WORKSPACE_ID (else the workspace
// is resolved from Pi's working directory), XMUSTARD_PI_DELIVERY=source|hook, and
// lower-only XMUSTARD_PI_TOOL_TIMEOUT_MS / XMUSTARD_PI_PROJECTION_TIMEOUT_MS.

import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { type TSchema, Type } from "typebox";
import { loadConfig } from "./config.ts";
import { EXPAND_TOOL, expand, PAGE_SIZE, PendingCalls, projectResult, runTool } from "./delivery.ts";
import { TOOL_NAMES, TOOL_SPECS, type ToolArgs, type ToolSpec } from "./tools.ts";
import { WorkspaceResolver } from "./workspace.ts";

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
		handle: Type.String({ description: "the xm1.… recovery handle from an [xmustard evidence] line" }),
		offset: Type.Optional(Type.Integer({ minimum: 0, description: "byte offset into the original (next_offset of the previous page)" })),
		length: Type.Optional(Type.Integer({ minimum: 1, maximum: PAGE_SIZE, description: `bytes to read (max ${PAGE_SIZE})` })),
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
			"Read the exact original bytes behind a reduced xMustard result, one page at a time (max 64 KiB). Pass the workspace_id and handle from its [xmustard evidence] line; start at offset 0 and continue from next_offset until eof. Pages report whether the capture is current, stale or of unknown freshness; serve captured bytes only.",
		parameters: ExpandParameters,
		async execute(_toolCallId, params, signal) {
			return expand(cfg, params, signal);
		},
	});

	const activateExpand = (): void => {
		const active = pi.getActiveTools();
		if (!active.includes(EXPAND_TOOL)) pi.setActiveTools([...active, EXPAND_TOOL]);
	};

	// Registered tools start active; expansion stays hidden until a handle is issued.
	pi.on("session_start", () => {
		const active = pi.getActiveTools();
		if (active.includes(EXPAND_TOOL)) pi.setActiveTools(active.filter((n) => n !== EXPAND_TOOL));
	});

	pi.on("tool_result", async (event, ctx) => {
		if (!TOOL_NAMES.has(event.toolName)) return undefined; // other tools pass through untouched
		return projectResult(
			cfg,
			event,
			pending,
			{ sessionId: sessionIdOf(ctx), signal: ctx.signal },
			activateExpand,
		);
	});
}
