// xMustard Pi extension: the nine xMustard tools as direct HTTP-backed Pi tools, plus
// `xmustard_expand`, inactive until a result carries a recovery handle. Pi's built-in
// tools (bash, read, grep, find, ls, edit, write) are projected through xMustard's
// capture route; older tool results are masked at turn_end in polling windows; and
// session_before_compact supplies a snapshot compaction whose details carry handles.
//
// Load-time work is registration only: no sidecar, socket, timer or network call.
// Configuration (see README.md): XMUSTARD_API_BASE, optional XMUSTARD_TOKEN (sent as
// a bearer token, never logged), optional XMUSTARD_WORKSPACE_ID (else the workspace
// is resolved from Pi's working directory), XMUSTARD_PI_DELIVERY=source|hook,
// lower-only XMUSTARD_PI_TOOL_TIMEOUT_MS / XMUSTARD_PI_PROJECTION_TIMEOUT_MS /
// XMUSTARD_PI_PROJECTION_TARGET_BYTES, XMUSTARD_PI_BUILTINS, XMUSTARD_PI_MASK*,
// XMUSTARD_PI_COMPACTION.

import { type ExtensionAPI, type ExtensionContext, type SessionBoundaryDraft, VERSION } from "@earendil-works/pi-coding-agent";
import { type TSchema, Type } from "typebox";
import { branchHoldsHandle, createCompactor } from "./compaction.ts";
import { loadConfig } from "./config.ts";
import { Capturer, EXPAND_TOOL, expand, PAGE_SIZE, PendingCalls, projectBuiltin, projectResult, runTool } from "./delivery.ts";
import { mutateBashInput, newWrappedCalls, recordWrappedCall, shouldActivateExpand, shouldWrapBash, wrapperOnPath } from "./commandwrap.ts";
import { createMasker, type EntryView } from "./masking.ts";
import { allArgs, TOOL_NAMES, TOOL_SPECS, type ToolArgs, type ToolSpec } from "./tools.ts";
import { callerTools, WorkspaceResolver } from "./workspace.ts";

// toolParameters renders a spec as TypeBox, serializing to exactly toJsonSchema (the
// MCP tools/list inputSchema). The adapter registers the "full" profile: Pi validates
// every call against this closed schema, so an argument the server accepts on tools/call
// but the schema omits could never be sent from Pi (WS-FIX-26).
export function toolParameters(spec: ToolSpec): TSchema {
	const props: Record<string, TSchema> = {};
	// an argument with no description carries no description key, as tools/list does
	const desc = (a: (typeof spec.args)[number]): { description?: string } => (a.desc ? { description: a.desc } : {});
	for (const a of allArgs(spec)) {
		let schema: TSchema;
		if (a.type === "boolean") schema = Type.Boolean(desc(a));
		else if (a.type === "integer") schema = Type.Integer({ ...desc(a), minimum: a.minimum, maximum: a.maximum });
		else if (a.type === "number") schema = Type.Number({ ...desc(a), minimum: a.minimum, maximum: a.maximum });
		// {type:"string", enum} exactly as MCP tools/list (same shape as pi-ai's StringEnum)
		else if (a.enum) schema = Type.Unsafe<string>({ type: "string", ...desc(a), enum: a.enum });
		else schema = Type.String(a.maxLength ? { ...desc(a), maxLength: a.maxLength } : desc(a));
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
	const capturer = new Capturer(cfg, Date.now, `pi-coding-agent/${VERSION}`);
	// built-in projection, masking and compaction resolve the session's workspace
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

	// Which calls the tool_call hook rewrote, consumed once by tool_result. See
	// commandwrap.ts for why the record and not the output text is the provenance.
	const wrappedCalls = newWrappedCalls();

	// Registered tools start active; expansion stays hidden until a handle is issued
	// (or the resumed, reloaded or forked branch already names one), and xMustard tools
	// this caller cannot use are deactivated, so they never reach the model
	// (registration itself stays load-time only, without network calls).
	pi.on("session_start", async (_event, ctx) => {
		const holdsHandle = branchHoldsHandle(ctx.sessionManager.getBranch() as EntryView[]);
		let active = pi.getActiveTools().filter((n) => n !== EXPAND_TOOL);
		const allowed = await callerTools(cfg);
		if (allowed) active = active.filter((n) => !TOOL_NAMES.has(n) || allowed.has(n));
		pi.setActiveTools(holdsHandle ? [...active, EXPAND_TOOL] : active);
	});

	pi.on("tool_result", async (event, ctx) => {
		const meta = { sessionId: sessionIdOf(ctx), signal: ctx.signal };
		if (TOOL_NAMES.has(event.toolName)) return projectResult(cfg, event, pending, meta, activateExpand);
		// The tool_call hook's own record of which call it rewrote, consumed once.
		// Output text cannot stand in for it: an untrusted result can print a
		// footer-shaped line, and a handle inferred from text let such a result skip
		// capture, redaction and the output budget.
		const wrapped = wrappedCalls.delete(event.toolCallId) === true;
		// A wrapper projection already carries its handle in the text and never reaches
		// the capture path that would otherwise notice, so the rule is
		// shouldActivateExpand's, applied before the built-ins guard: with
		// XMUSTARD_PI_BUILTINS=none the wrapped call is still captured and still
		// carries a handle, and the model needs xmustard_expand to use it. Whether the
		// result is then projected remains that set's own decision.
		if (shouldActivateExpand(event.content)) activateExpand();
		if (cfg.builtins.has(event.toolName)) {
			return projectBuiltin(cfg, capturer, (signal) => resolveWorkspace(ctx.cwd, signal), event, meta, activateExpand, wrapped);
		}
		return undefined; // other tools pass through untouched
	});

	// WS-44e (PAR-ADP-09): a bash call's command is rewritten to the run wrapper
	// before Pi executes it, so its output is reduced server-side. The decision is
	// shouldWrapBash's, so no other call pays for a workspace lookup and its deadline,
	// and a session whose wrapper cannot reach this adapter's API with its credential
	// keeps running natively, captured by the adapter instead.
	pi.on("tool_call", async (event, ctx) => {
		if (!shouldWrapBash(event, {
			enabled: cfg.wrapBash,
			wrapperAvailable: wrapperOnPath(process.env.PATH),
			token: cfg.token,
			wrapperToken: process.env.XMUSTARD_API_TOKEN,
			wrapperApiBase: process.env.XMUSTARD_API_BASE,
			apiBase: cfg.apiBase,
		})) return undefined;
		await resolveWorkspace(ctx.cwd, ctx.signal).then(
			(ws) => {
				// The record follows the rewrite's own answer: a command left alone
				// (blank, malformed, already wrapped) ran natively, and its result must
				// not be treated as the wrapper's projection (r7).
				recordWrappedCall(wrappedCalls, event, ws, true);
			},
			() => mutateBashInput(event, undefined, true),
		);
		return undefined;
	});

	// masking returns the drafts earlier handlers proposed plus its context edits
	const mask = createMasker({ cfg: cfg.mask, capturer, resolveWorkspace, onHandle: activateExpand });
	pi.on("turn_end", async (event, ctx) => {
		const out = await mask(event, ctx);
		return out ? { entries: out.entries as SessionBoundaryDraft[] } : undefined;
	});

	pi.on("session_before_compact", createCompactor({ enabled: cfg.compaction, capturer, resolveWorkspace, onHandle: activateExpand }));
}
