// Retroactive masking of older tool results (PAR-CTX-07) at Pi's turn_end boundary.
//
// Pi keeps the session as an append-only tree; a `context_edit` entry replaces one
// earlier entry's model-visible content and leaves the entry itself (and its
// toolCallId, toolName and isError) untouched. At turn_end the adapter appends such
// edits for tool results older than `afterTurns` turns, replacing each with a stub
//
//   [xmustard masked: 812 lines, 14203 bytes of bash output (error); turn 3; handle xm1.…; workspace_id w] …
//
// that names a recovery handle for xmustard_expand. The mask only advances every
// `everyTurns` turns (a polling window), so between windows the prompt prefix does not
// change and the provider's prompt cache survives. The latest failure and results
// about files edited within the last `afterTurns` turns are never masked. A result
// that has no handle yet is first retained through the capture route (target 1 KiB,
// so it gets one); if that fails it stays unmasked.
//
// This module reads Pi entries through minimal structural views, so it runs (and is
// unit-tested) without Pi's module aliases.

import { homedir } from "node:os";
import path from "node:path";
import { MIN_CAPTURE_TARGET, type MaskConfig } from "./config.ts";
import { argsDigestOf, type Capturer, EXPAND_TOOL, textBlocks } from "./delivery.ts";
import { BUILTIN_TOOLS, EDIT_TOOLS, FILE_TOOLS, TOOL_NAMES } from "./tools.ts";

export const MASK_PREFIX = "[xmustard masked: ";

// Tools whose results may be masked: Pi's built-ins, the nine xMustard tools and
// expansion pages. Other extensions' tools are left alone.
export const MASKABLE: ReadonlySet<string> = new Set<string>([...BUILTIN_TOOLS, ...TOOL_NAMES, EXPAND_TOOL]);

// ---- structural views of Pi session entries (core/session-manager.d.ts) -------------

export interface MessageView {
	role: string;
	content?: unknown; // string | (text | image | toolCall | thinking)[]
	toolCallId?: string;
	toolName?: string;
	isError?: boolean;
	details?: unknown;
}

export interface EntryView {
	type: string;
	id: string;
	message?: MessageView;
	targetId?: string; // context_edit
	replacement?: { content: unknown } | null; // context_edit
	firstKeptEntryId?: string; // compaction
	summary?: string; // compaction
	details?: unknown; // compaction
	fromHook?: boolean; // compaction
}

// HandleRef locates a retained original: the handle, its workspace and, for an
// expansion page, where the page started.
export interface HandleRef {
	handle: string;
	workspace_id: string;
	offset?: number;
	source: "delivery" | "capture" | "expand" | "mask" | "retained";
}

export interface ResultRecord {
	entryId: string;
	toolCallId: string;
	toolName: string;
	turn: number; // the assistant turn that called the tool (1-based)
	isError: boolean;
	args: Record<string, unknown>;
	path?: string; // absolute path a file tool addressed
	command?: string; // bash command
	text: string; // model-visible text now (after context edits)
	bytes: number;
	lines: number;
	image: boolean; // holds non-text content
	omitted: boolean; // a context edit removed it from context
	inContext: boolean;
	masked: boolean; // its model-visible text is an xMustard stub
	handle?: HandleRef;
}

export interface BranchAnalysis {
	turn: number; // assistant turns on the branch
	results: ResultRecord[];
	latestFailure?: string; // entry id of the most recent failing result
	lastEdit: Map<string, number>; // absolute path → last turn an edit/write changed it
	firstUser?: string;
	lastUser?: string;
	lastAssistantText?: string;
	compactions: EntryView[]; // compaction entries in branch order
}

const isObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);

// textOf returns the text of message content and whether it held anything else.
export function textOf(content: unknown): { text: string; image: boolean } {
	if (typeof content === "string") return { text: content, image: false };
	if (!Array.isArray(content)) return { text: "", image: false };
	const texts = textBlocks(content);
	if (texts) return { text: texts.join(""), image: false };
	let text = "";
	for (const b of content) if ((b as { type?: string })?.type === "text") text += String((b as { text?: unknown }).text ?? "");
	return { text, image: true };
}

export function countLines(text: string): number {
	if (text === "") return 0;
	let n = 1;
	for (let i = text.indexOf("\n"); i >= 0; i = text.indexOf("\n", i + 1)) n++;
	return text.endsWith("\n") ? n - 1 : n;
}

// resolvePath resolves a tool's path argument the way Pi does (~ expands to home).
export function resolvePath(p: string, cwd: string | undefined): string {
	let v = p.trim();
	if (v === "~") v = homedir();
	else if (v.startsWith("~/")) v = path.join(homedir(), v.slice(2));
	return path.resolve(cwd ?? "/", v);
}

const STUB_RE = /^\[xmustard masked: [^\]\n]*?; handle (xm1\.[A-Za-z0-9_-]+)(?:; offset (\d+))?; workspace_id ([^\]\s;]+)\]/;

// parseStub returns the handle a mask stub names.
export function parseStub(text: string): HandleRef | undefined {
	const m = STUB_RE.exec(text);
	if (!m) return undefined;
	return { handle: m[1], workspace_id: m[3], ...(m[2] ? { offset: Number(m[2]) } : {}), source: "mask" };
}

// knownHandle finds a recovery handle a result already carries: a projection's
// (built-in capture or xMustard delivery), an expansion page's, or a mask stub's.
// A stub is only trusted where a context edit put it (edited): tool output that merely
// starts with the stub prefix is not a mask.
export function knownHandle(toolName: string, details: unknown, args: Record<string, unknown>, text: string, edited = false): HandleRef | undefined {
	if (edited && text.startsWith(MASK_PREFIX)) {
		const ref = parseStub(text);
		if (ref) return ref;
	}
	if (!isObject(details)) return undefined;
	const xm = details.xmustard;
	if (isObject(xm) && xm.path === "capture" && str(xm.handle) && str(xm.workspace_id)) {
		return { handle: String(xm.handle), workspace_id: String(xm.workspace_id), source: "capture" };
	}
	const delivery = details.delivery;
	if (TOOL_NAMES.has(toolName) && isObject(delivery) && str(delivery.handle) && str(details.workspace_id)) {
		return { handle: String(delivery.handle), workspace_id: String(details.workspace_id), source: "delivery" };
	}
	if (toolName === EXPAND_TOOL && str(details.handle) && str(args.workspace_id)) {
		// a page (offset) or a search result over the same original
		const offset = typeof details.offset === "number" && typeof details.data_base64 === "string" ? details.offset : undefined;
		return { handle: String(details.handle), workspace_id: String(args.workspace_id), ...(offset !== undefined ? { offset } : {}), source: "expand" };
	}
	return undefined;
}

export interface AnalyzeOptions {
	cwd?: string;
	// ids of the entries currently in model context (turn_end's context preview);
	// without it every result counts as in context
	inContext?: ReadonlySet<string>;
}

// analyzeBranch walks the active branch in order: assistant messages number the
// turns, tool calls give each result its arguments, and context edits give each
// result its current model-visible text.
export function analyzeBranch(entries: readonly EntryView[], opts: AnalyzeOptions = {}): BranchAnalysis {
	const edits = new Map<string, { content: unknown } | null>();
	for (const e of entries) if (e.type === "context_edit" && e.targetId) edits.set(e.targetId, e.replacement ?? null);
	const calls = new Map<string, Record<string, unknown>>();
	const out: BranchAnalysis = { turn: 0, results: [], lastEdit: new Map(), compactions: [] };
	for (const e of entries) {
		if (e.type === "compaction") {
			out.compactions.push(e);
			continue;
		}
		const m = e.type === "message" ? e.message : undefined;
		if (!m) continue;
		if (m.role === "user") {
			const t = textOf(m.content).text.trim();
			if (t) {
				out.firstUser ??= t;
				out.lastUser = t;
			}
		} else if (m.role === "assistant") {
			out.turn++;
			let said = "";
			for (const b of Array.isArray(m.content) ? m.content : []) {
				const block = b as { type?: string; id?: string; arguments?: unknown; text?: unknown };
				if (block.type === "toolCall" && block.id) calls.set(block.id, isObject(block.arguments) ? block.arguments : {});
				if (block.type === "text" && typeof block.text === "string") said += block.text;
			}
			if (said.trim()) out.lastAssistantText = said.trim();
		} else if (m.role === "toolResult" && m.toolCallId) {
			const edit = edits.get(e.id);
			const omitted = edits.has(e.id) && edit === null;
			const { text, image } = textOf(edit ? edit.content : m.content);
			const args = calls.get(m.toolCallId) ?? {};
			const toolName = m.toolName ?? "";
			const p = str(args.path) ?? str(args.file_path);
			const rec: ResultRecord = {
				entryId: e.id,
				toolCallId: m.toolCallId,
				toolName,
				turn: out.turn,
				isError: m.isError === true,
				args,
				...(p && (FILE_TOOLS.has(toolName) || toolName === "ls" || toolName === "grep" || toolName === "find") ? { path: resolvePath(p, opts.cwd) } : {}),
				...(str(args.command) ? { command: String(args.command) } : {}),
				text,
				bytes: Buffer.byteLength(text, "utf8"),
				lines: countLines(text),
				image,
				omitted,
				inContext: !omitted && (opts.inContext ? opts.inContext.has(e.id) : true),
				masked: !!edit && text.startsWith(MASK_PREFIX),
			};
			const handle = knownHandle(toolName, m.details, args, text, !!edit);
			if (handle) rec.handle = handle;
			out.results.push(rec);
			if (rec.isError) out.latestFailure = e.id;
			if (EDIT_TOOLS.has(toolName) && !rec.isError && rec.path) out.lastEdit.set(rec.path, rec.turn);
		}
	}
	return out;
}

export type ExemptReason = "latest_failure" | "active_file";

export interface MaskPlan {
	due: boolean; // this turn is a polling-window boundary
	turn: number;
	cutoff: number; // results from turns <= cutoff are old
	candidates: ResultRecord[];
	exempt: { entryId: string; toolCallId: string; reason: ExemptReason }[];
}

// isWindow reports whether the mask advances at this turn: only every everyTurns
// turns, once some turn is older than afterTurns.
export function isWindow(turn: number, cfg: MaskConfig): boolean {
	return cfg.enabled && turn - cfg.afterTurns > 0 && turn % cfg.everyTurns === 0;
}

// countTurns counts the assistant messages on a branch (cheap: no text is read).
export function countTurns(entries: readonly EntryView[]): number {
	let n = 0;
	for (const e of entries) if (e.type === "message" && e.message?.role === "assistant") n++;
	return n;
}

// planMask decides, deterministically, which results to mask at this turn.
export function planMask(a: BranchAnalysis, cfg: MaskConfig): MaskPlan {
	const cutoff = a.turn - cfg.afterTurns;
	const plan: MaskPlan = { due: isWindow(a.turn, cfg), turn: a.turn, cutoff, candidates: [], exempt: [] };
	if (!plan.due) return plan;
	const active = new Set<string>();
	for (const [p, t] of a.lastEdit) if (t > cutoff) active.add(p);
	for (const r of a.results) {
		if (r.turn > cutoff || !r.inContext || r.masked || r.image || !MASKABLE.has(r.toolName) || r.bytes < cfg.minBytes) continue;
		if (r.entryId === a.latestFailure) {
			plan.exempt.push({ entryId: r.entryId, toolCallId: r.toolCallId, reason: "latest_failure" });
			continue;
		}
		if (r.path && FILE_TOOLS.has(r.toolName) && active.has(r.path)) {
			plan.exempt.push({ entryId: r.entryId, toolCallId: r.toolCallId, reason: "active_file" });
			continue;
		}
		plan.candidates.push(r);
	}
	return plan;
}

const clip = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

// maskStub is the model-visible replacement of a masked result. Errors keep their
// first and last non-empty lines, so a failure stays visible as a failure.
export function maskStub(r: ResultRecord, ref: HandleRef): string {
	const what = `${r.lines} lines, ${r.bytes} bytes of ${r.toolName} output${r.isError ? " (error)" : ""}`;
	const where = [`turn ${r.turn}`, `handle ${ref.handle}`, ...(ref.offset !== undefined ? [`offset ${ref.offset}`] : []), `workspace_id ${ref.workspace_id}`];
	let stub = `${MASK_PREFIX}${what}; ${where.join("; ")}] Recover it with ${EXPAND_TOOL}(workspace_id="${ref.workspace_id}", handle="${ref.handle}", offset=${ref.offset ?? 0}), or search it with pattern=<RE2> or lines=A-B.`;
	if (r.isError) {
		const lines = r.text.split("\n").filter((l) => l.trim() !== "");
		const first = clip(lines[0]?.trim() ?? "", 200);
		const last = clip(lines.at(-1)?.trim() ?? "", 200);
		if (first) stub += `\nfirst line: ${first}`;
		if (last && lines.length > 1) stub += `\nlast line: ${last}`;
	}
	return stub;
}

export interface RetainDeps {
	capturer: Capturer;
	workspaceId?: string; // where handle-less results are retained
	workspaceError?: string;
	sessionId?: string;
	signal?: AbortSignal;
}

// retain returns a result's recovery handle, retaining its model-visible text
// through the capture route (raw, 1 KiB target) when it has none yet.
export async function retain(r: ResultRecord, deps: RetainDeps): Promise<HandleRef> {
	if (r.handle) return r.handle;
	if (!deps.workspaceId) throw new Error(deps.workspaceError ?? "no workspace to retain the output in");
	const obs = await deps.capturer.observe(
		deps.workspaceId,
		{
			format: "raw",
			body: r.text,
			tool: r.toolName,
			callId: r.toolCallId,
			sessionId: deps.sessionId,
			isError: r.isError,
			target: MIN_CAPTURE_TARGET,
			command: r.command,
			path: r.path,
			argsDigest: argsDigestOf(r.args),
		},
		deps.signal,
	);
	if (!obs.handle) throw new Error(`capture retained no original (${obs.raw_bytes} bytes were not reduced)`);
	return { handle: obs.handle, workspace_id: deps.workspaceId, source: "retained" };
}

// mapLimit runs fn over items with at most `limit` in flight, keeping order.
export async function mapLimit<T, R>(items: readonly T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
	const out = new Array<R>(items.length);
	let next = 0;
	const worker = async () => {
		while (next < items.length) {
			const i = next++;
			out[i] = await fn(items[i]);
		}
	};
	await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
	return out;
}

// Most results retained in one window; the rest wait for the next one.
export const MAX_RETAIN_PER_WINDOW = 64;
const RETAIN_CONCURRENCY = 4;

export interface ContextEditDraft {
	type: "context_edit";
	targetId: string;
	replacement: { content: { type: "text"; text: string }[] };
}

export interface MaskOutcome {
	plan: MaskPlan;
	drafts: ContextEditDraft[];
	failed: { toolCallId: string; reason: string }[];
}

// maskDrafts turns a plan into context edits, retaining handle-less results first.
// A result whose handle cannot be obtained is not masked.
export async function maskDrafts(plan: MaskPlan, deps: RetainDeps, skip: ReadonlySet<string> = new Set()): Promise<MaskOutcome> {
	const todo = plan.candidates.filter((r) => !skip.has(r.entryId));
	const withHandle = todo.filter((r) => r.handle);
	const needs = todo.filter((r) => !r.handle).slice(0, MAX_RETAIN_PER_WINDOW);
	const failed: MaskOutcome["failed"] = [];
	const refs = new Map<string, HandleRef>();
	for (const r of withHandle) refs.set(r.entryId, r.handle as HandleRef);
	await mapLimit(needs, RETAIN_CONCURRENCY, async (r) => {
		try {
			refs.set(r.entryId, await retain(r, deps));
		} catch (err) {
			failed.push({ toolCallId: r.toolCallId, reason: err instanceof Error ? err.message : String(err) });
		}
	});
	const drafts: ContextEditDraft[] = [];
	for (const r of todo) {
		const ref = refs.get(r.entryId);
		if (ref) drafts.push({ type: "context_edit", targetId: r.entryId, replacement: { content: [{ type: "text", text: maskStub(r, ref) }] } });
	}
	return { plan, drafts, failed };
}

// ---- the turn_end handler -------------------------------------------------------------

export interface TurnEndView {
	entries?: readonly { type: string; targetId?: string }[];
	context?: { contextEntries?: readonly { sourceEntry: { id: string } }[] };
}

export interface SessionContextView {
	cwd?: string;
	signal?: AbortSignal;
	sessionManager: { getBranch(): unknown[]; getSessionId(): string };
}

export interface MaskerDeps {
	cfg: MaskConfig;
	capturer: Capturer;
	resolveWorkspace(cwd: string | undefined, signal?: AbortSignal): Promise<string>;
	onHandle(): void;
	onOutcome?(o: MaskOutcome): void;
}

export function sessionIdOf(ctx: SessionContextView): string | undefined {
	try {
		return ctx.sessionManager.getSessionId();
	} catch {
		return undefined;
	}
}

// createMasker returns Pi's turn_end handler. It returns the drafts proposed by
// earlier handlers plus its own context edits (a boundary handler's `entries`
// replace the proposed list), or undefined when this turn is not a window boundary.
export function createMasker(deps: MaskerDeps) {
	return async (event: TurnEndView, ctx: SessionContextView): Promise<{ entries: unknown[] } | undefined> => {
		if (!deps.cfg.enabled) return undefined;
		const branch = ctx.sessionManager.getBranch() as EntryView[];
		if (!isWindow(countTurns(branch), deps.cfg)) return undefined; // between windows: no work
		const proposed = event.entries ?? [];
		const inContext = event.context?.contextEntries ? new Set(event.context.contextEntries.map((e) => e.sourceEntry.id)) : undefined;
		const analysis = analyzeBranch(branch, { cwd: ctx.cwd, inContext });
		const plan = planMask(analysis, deps.cfg);
		if (!plan.due || plan.candidates.length === 0) return undefined;
		const retainDeps: RetainDeps = { capturer: deps.capturer, sessionId: sessionIdOf(ctx), signal: ctx.signal };
		if (plan.candidates.some((r) => !r.handle)) {
			try {
				retainDeps.workspaceId = await deps.resolveWorkspace(ctx.cwd, ctx.signal);
			} catch (err) {
				retainDeps.workspaceError = err instanceof Error ? err.message : String(err);
			}
		}
		// another extension already edits these entries in this boundary: leave them
		const skip = new Set(proposed.filter((d) => d.type === "context_edit" && d.targetId).map((d) => d.targetId as string));
		const outcome = await maskDrafts(plan, retainDeps, skip);
		deps.onOutcome?.(outcome);
		if (outcome.drafts.length === 0) return undefined;
		deps.onHandle();
		return { entries: [...proposed, ...outcome.drafts] };
	};
}
