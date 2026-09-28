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
// that has no live handle yet (none, or one that expires within EXPIRY_MARGIN_MS) is
// first retained through the capture route (target 1 KiB, so it gets one); if that
// fails it stays unmasked. A stub whose handle is about to expire is rewritten at the
// next window, and one that has expired at the next turn_end, from the raw entry the
// session still holds.
//
// This module reads Pi entries through minimal structural views, so it runs (and is
// unit-tested) without Pi's module aliases.

import { homedir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { MIN_CAPTURE_TARGET, type MaskConfig } from "./config.ts";
import { argsDigestOf, type Capturer, EXPAND_TOOL, textBlocks } from "./delivery.ts";
import { BUILTIN_TOOLS, EDIT_TOOLS, FILE_TOOLS, TOOL_NAMES } from "./tools.ts";

export const MASK_PREFIX = "[xmustard masked: ";
// A handle that expires within this margin is treated as gone: Go retains originals
// for 24 h by default, and a stub or snapshot must not name a handle that dies before
// the model can use it. Such results are retained again under a fresh handle.
export const EXPIRY_MARGIN_MS = 60 * 60_000;

// Lines the adapter itself adds to a result (recovery footer, injection-check note,
// page and search headers): never the tool's own first or last line.
const ADAPTER_LINE = /^\[xmustard (evidence|injection-check|page|search)\] /;
export const isAdapterLine = (line: string): boolean => ADAPTER_LINE.test(line);

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

// HandleRef locates a retained original: the handle, its workspace, when Go stops
// serving it and, for an expansion page, where the page started.
export interface HandleRef {
	handle: string;
	workspace_id: string;
	offset?: number;
	expires_at?: string; // RFC 3339, as Go issued it; absent when unknown
	source: "delivery" | "capture" | "expand" | "mask" | "retained";
}

// isLive reports whether a handle will still be served marginMs from now. An unknown
// expiry counts as live (Go did not say); an unparsable one as expired.
export function isLive(ref: HandleRef | undefined, now: number, marginMs = EXPIRY_MARGIN_MS): boolean {
	if (!ref) return false;
	if (ref.expires_at === undefined) return true;
	const t = Date.parse(ref.expires_at);
	return !Number.isNaN(t) && t - now > marginMs;
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
	original?: string; // the raw entry's text, when a context edit replaced it
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

const UNICODE_SPACES = /[\u00A0\u2000-\u200A\u202F\u205F\u3000]/g;

// resolvePath resolves a tool's path argument the way Pi's tools do (resolveToCwd in
// core/tools/path-utils.js): unicode spaces become spaces, a leading @ is dropped, ~
// expands to home and file:// URLs become paths. Models often send "@src/a.go", so
// "@src/a.go" and "src/a.go" must name the same file.
export function resolvePath(p: string, cwd: string | undefined): string {
	let v = p.replace(UNICODE_SPACES, " ");
	if (v.startsWith("@")) v = v.slice(1);
	if (v === "~") v = homedir();
	else if (v.startsWith("~/")) v = path.join(homedir(), v.slice(2));
	else if (v.startsWith("file://")) {
		try {
			v = fileURLToPath(v);
		} catch {
			// not a valid file URL: resolved as written, as Pi would fail on it
		}
	}
	return path.resolve(cwd ?? "/", v);
}

const STUB_RE = /^\[xmustard masked: [^\]\n]*?; handle (xm1\.[A-Za-z0-9_-]+)(?:; offset (\d+))?; workspace_id ([^\]\s;]+)(?:; expires ([^\]\s;]+))?\]/;

// parseStub returns the handle a mask stub names.
export function parseStub(text: string): HandleRef | undefined {
	const m = STUB_RE.exec(text);
	if (!m) return undefined;
	return { handle: m[1], workspace_id: m[3], ...(m[2] ? { offset: Number(m[2]) } : {}), ...(m[4] ? { expires_at: m[4] } : {}), source: "mask" };
}

const expiresOf = (v: Record<string, unknown>): { expires_at?: string } => (str(v.expires_at) ? { expires_at: String(v.expires_at) } : {});

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
		return { handle: String(xm.handle), workspace_id: String(xm.workspace_id), ...expiresOf(xm), source: "capture" };
	}
	const delivery = details.delivery;
	if (TOOL_NAMES.has(toolName) && isObject(delivery) && str(delivery.handle) && str(details.workspace_id)) {
		return { handle: String(delivery.handle), workspace_id: String(details.workspace_id), ...expiresOf(delivery), source: "delivery" };
	}
	if (toolName === EXPAND_TOOL && str(details.handle) && str(args.workspace_id)) {
		// a page (offset) or a search result over the same original
		const offset = typeof details.offset === "number" && typeof details.data_base64 === "string" ? details.offset : undefined;
		return { handle: String(details.handle), workspace_id: String(args.workspace_id), ...(offset !== undefined ? { offset } : {}), ...expiresOf(details), source: "expand" };
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
			const original = edit ? textOf(m.content).text : undefined;
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
				...(original !== undefined ? { original } : {}),
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
	// in-context stubs whose handle expires within the margin: rewritten from the raw entry
	refresh: ResultRecord[];
	exempt: { entryId: string; toolCallId: string; reason: ExemptReason }[];
}

export interface PlanOptions {
	now?: number;
	marginMs?: number; // a handle expiring within this is not live (EXPIRY_MARGIN_MS)
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

// needsRefresh: an in-context stub this adapter wrote whose handle is not live.
const needsRefresh = (r: ResultRecord, now: number, marginMs: number): boolean =>
	r.masked && r.inContext && r.original !== undefined && r.handle?.source === "mask" && !isLive(r.handle, now, marginMs);

// planMask decides, deterministically, which results to mask at this turn: new
// candidates only at a window; stubs to rewrite whenever their handle is not live
// (the caller passes a zero margin between windows, so only expired ones qualify).
export function planMask(a: BranchAnalysis, cfg: MaskConfig, opts: PlanOptions = {}): MaskPlan {
	const now = opts.now ?? Date.now();
	const marginMs = opts.marginMs ?? EXPIRY_MARGIN_MS;
	const cutoff = a.turn - cfg.afterTurns;
	const plan: MaskPlan = { due: isWindow(a.turn, cfg), turn: a.turn, cutoff, candidates: [], refresh: [], exempt: [] };
	if (!cfg.enabled) return plan;
	plan.refresh = a.results.filter((r) => needsRefresh(r, now, marginMs));
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

// hasExpiredStub reports, cheaply (no tool output is read), whether a stub in model
// context names a handle that has expired: then the masker runs between windows too.
export function hasExpiredStub(entries: readonly EntryView[], inContext: ReadonlySet<string> | undefined, now: number): boolean {
	const latest = new Map<string, EntryView>();
	for (const e of entries) if (e.type === "context_edit" && e.targetId) latest.set(e.targetId, e);
	for (const [target, e] of latest) {
		if (!e.replacement || (inContext && !inContext.has(target))) continue;
		const { text } = textOf(e.replacement.content);
		if (!text.startsWith(MASK_PREFIX)) continue;
		const ref = parseStub(text);
		if (ref && !isLive(ref, now, 0)) return true;
	}
	return false;
}

const clip = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

// preMaskText is what the model saw before this adapter masked a result: the raw
// entry for a stub, else the current text.
export const preMaskText = (r: ResultRecord): string => (r.masked && r.original !== undefined ? r.original : r.text);

// unmasked returns a stub's result as it was before the mask (sizes of the original).
function unmasked(r: ResultRecord): ResultRecord {
	if (!r.masked || r.original === undefined) return r;
	return { ...r, text: r.original, bytes: Buffer.byteLength(r.original, "utf8"), lines: countLines(r.original), masked: false };
}

// maskStub is the model-visible replacement of a masked result. Errors keep their
// first and last non-empty lines of tool output, so a failure stays visible as a
// failure: the adapter's own recovery footer or page header is never one of them.
export function maskStub(r: ResultRecord, ref: HandleRef): string {
	const what = `${r.lines} lines, ${r.bytes} bytes of ${r.toolName} output${r.isError ? " (error)" : ""}`;
	const where = [
		`turn ${r.turn}`,
		`handle ${ref.handle}`,
		...(ref.offset !== undefined ? [`offset ${ref.offset}`] : []),
		`workspace_id ${ref.workspace_id}`,
		...(ref.expires_at ? [`expires ${ref.expires_at}`] : []),
	];
	let stub = `${MASK_PREFIX}${what}; ${where.join("; ")}] Recover it with ${EXPAND_TOOL}(workspace_id="${ref.workspace_id}", handle="${ref.handle}", offset=${ref.offset ?? 0}), or search it with pattern=<RE2> or lines=A-B.`;
	if (r.isError) {
		const lines = r.text.split("\n").filter((l) => l.trim() !== "" && !isAdapterLine(l));
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

// retainText retains one text through the capture route (raw, 1 KiB target, so any
// text above 1 KiB gets a handle) and returns the fresh handle.
export async function retainText(r: Pick<ResultRecord, "toolName" | "toolCallId" | "isError" | "args" | "command" | "path">, body: string, deps: RetainDeps): Promise<HandleRef> {
	if (!deps.workspaceId) throw new Error(deps.workspaceError ?? "no workspace to retain the output in");
	const obs = await deps.capturer.observe(
		deps.workspaceId,
		{
			format: "raw",
			body,
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
	return { handle: obs.handle, workspace_id: deps.workspaceId, ...(obs.expires_at ? { expires_at: obs.expires_at } : {}), source: "retained" };
}

// retain returns a result's recovery handle when it stays live past the margin, else
// retains the text the model saw before any mask under a fresh one.
export async function retain(r: ResultRecord, deps: RetainDeps, now = Date.now(), marginMs = EXPIRY_MARGIN_MS): Promise<HandleRef> {
	if (r.handle && isLive(r.handle, now, marginMs)) return r.handle;
	return retainText(r, preMaskText(r), deps);
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
	refreshed: string[]; // toolCallIds whose stub was rewritten under a fresh handle
	failed: { toolCallId: string; reason: string }[];
}

// maskDrafts turns a plan into context edits, retaining first every result without a
// live handle (stubs to refresh first). A result whose handle cannot be obtained is
// not masked, and a stub that cannot be refreshed stays as it is.
export async function maskDrafts(plan: MaskPlan, deps: RetainDeps, skip: ReadonlySet<string> = new Set(), opts: PlanOptions = {}): Promise<MaskOutcome> {
	const now = opts.now ?? Date.now();
	const marginMs = opts.marginMs ?? EXPIRY_MARGIN_MS;
	const refresh = plan.refresh.filter((r) => !skip.has(r.entryId));
	const todo = plan.candidates.filter((r) => !skip.has(r.entryId));
	const refs = new Map<string, HandleRef>();
	for (const r of todo) if (r.handle && isLive(r.handle, now, marginMs)) refs.set(r.entryId, r.handle);
	const needs = [...refresh, ...todo.filter((r) => !refs.has(r.entryId))].slice(0, MAX_RETAIN_PER_WINDOW);
	const failed: MaskOutcome["failed"] = [];
	await mapLimit(needs, RETAIN_CONCURRENCY, async (r) => {
		try {
			refs.set(r.entryId, await retainText(r, preMaskText(r), deps));
		} catch (err) {
			failed.push({ toolCallId: r.toolCallId, reason: err instanceof Error ? err.message : String(err) });
		}
	});
	const drafts: ContextEditDraft[] = [];
	const refreshed: string[] = [];
	for (const r of [...refresh, ...todo]) {
		const ref = refs.get(r.entryId);
		if (!ref) continue;
		drafts.push({ type: "context_edit", targetId: r.entryId, replacement: { content: [{ type: "text", text: maskStub(unmasked(r), ref) }] } });
		if (r.masked) refreshed.push(r.toolCallId);
	}
	return { plan, drafts, refreshed, failed };
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
	now?: () => number;
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
// replace the proposed list), or undefined when there is nothing to do: between
// windows unless a stub in context names an expired handle.
export function createMasker(deps: MaskerDeps) {
	return async (event: TurnEndView, ctx: SessionContextView): Promise<{ entries: unknown[] } | undefined> => {
		if (!deps.cfg.enabled) return undefined;
		const now = (deps.now ?? Date.now)();
		const branch = ctx.sessionManager.getBranch() as EntryView[];
		const inContext = event.context?.contextEntries ? new Set(event.context.contextEntries.map((e) => e.sourceEntry.id)) : undefined;
		const window = isWindow(countTurns(branch), deps.cfg);
		if (!window && !hasExpiredStub(branch, inContext, now)) return undefined; // between windows: no work
		const proposed = event.entries ?? [];
		const opts: PlanOptions = { now, marginMs: window ? EXPIRY_MARGIN_MS : 0 };
		const analysis = analyzeBranch(branch, { cwd: ctx.cwd, inContext });
		const plan = planMask(analysis, deps.cfg, opts);
		if (plan.candidates.length === 0 && plan.refresh.length === 0) return undefined;
		const retainDeps: RetainDeps = { capturer: deps.capturer, sessionId: sessionIdOf(ctx), signal: ctx.signal };
		if (plan.refresh.length > 0 || plan.candidates.some((r) => !isLive(r.handle, now, opts.marginMs))) {
			try {
				retainDeps.workspaceId = await deps.resolveWorkspace(ctx.cwd, ctx.signal);
			} catch (err) {
				retainDeps.workspaceError = err instanceof Error ? err.message : String(err);
			}
		}
		// another extension already edits these entries in this boundary: leave them
		const skip = new Set(proposed.filter((d) => d.type === "context_edit" && d.targetId).map((d) => d.targetId as string));
		const outcome = await maskDrafts(plan, retainDeps, skip, opts);
		deps.onOutcome?.(outcome);
		if (outcome.drafts.length === 0) return undefined;
		deps.onHandle();
		return { entries: [...proposed, ...outcome.drafts] };
	};
}
