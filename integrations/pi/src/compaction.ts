// Compaction snapshot and restore for Pi (PAR-HAR-04).
//
// At Pi's compaction boundary (session_before_compact) the adapter supplies the
// compaction itself: a deterministic, priority-tiered snapshot of at most 2 KB
// (goal, open failures with handles, active files, pending memory proposals, memory
// decisions, last progress, then recoverable outputs) instead of Pi's model-written
// summary. Nothing it removes from model context is left without a recovery path:
//
//   - every summarized tool output above 1 KiB is retained behind its own handle (a
//     live existing one is reused), and the handles an earlier xMustard compaction
//     carried stay recoverable: live ones are kept, expired ones are retained again
//     from the session while the branch still holds their entries;
//   - everything else (the snapshot in full, every handle, every open failure, an
//     earlier summary in full, and the compacted messages with tool outputs of up to
//     1 KiB inline) goes into one index document, retained behind the handle the
//     snapshot names on its second line. When that document fits the 2 KB budget
//     itself, the summary is the document and no index is retained.
//
// The CompactionEntry's `details` carry the handles and the index. The snapshot is
// derived from this session and is never verified memory; it says so.
//
// When the snapshot cannot keep that promise (xMustard unreachable or capture
// unavailable while something still needs a handle, or any retention fails), or the
// user asked for a focused summary (/compact <instructions>), the handler returns
// nothing and Pi's own compaction runs.

import path from "node:path";
import { type Capturer, EXPAND_TOOL } from "./delivery.ts";
import {
	analyzeBranch,
	countLines,
	type EntryView,
	EXPIRY_MARGIN_MS,
	type HandleRef,
	isAdapterLine,
	isLive,
	knownHandle,
	MASK_PREFIX,
	type MessageView,
	mapLimit,
	preMaskText,
	type ResultRecord,
	type RetainDeps,
	resolvePath,
	retainText,
	type SessionContextView,
	sessionIdOf,
	textOf,
} from "./masking.ts";
import { EDIT_TOOLS } from "./tools.ts";

export const COMPACTION_VERSION = "xmustard.pi-compaction/v1";
export const SNAPSHOT_MAX_BYTES = 2048;
export const SNAPSHOT_HEADER = "[xmustard compaction snapshot]";
export const INDEX_HEADER = "[xmustard compaction index]";
// The capture tool name of the index document.
export const INDEX_TOOL = "pi_compaction";
// Summarized outputs above this get their own handle; smaller ones sit in the index
// (a capture of 1 KiB or less is not reduced, so Go would issue no handle for it).
export const RETAIN_MIN_BYTES = (1 << 10) + 1;
// Most outputs one compaction retains; beyond it Pi's own compaction runs.
export const MAX_RETAIN_PER_COMPACTION = 512;
// Most handles one entry's details carry (this compaction's plus carried ones); the
// index lists all of them.
export const MAX_CARRIED_HANDLES = 1024;
// Largest index document; beyond it Pi's own compaction runs.
export const MAX_INDEX_BYTES = 8 << 20;
const RETAIN_CONCURRENCY = 4;

export interface HandleEntry extends HandleRef {
	tool: string;
	tool_call_id: string;
	turn: number;
	is_error: boolean;
	bytes: number;
	lines: number;
	label: string;
}

export interface Snapshot {
	goal?: string;
	latest_request?: string;
	failures: { tool: string; turn: number; label: string; line: string; handle?: string }[];
	modified_files: string[];
	read_files: string[];
	proposals: { id: string; title?: string; status: string }[];
	decisions: string[];
	last_progress?: string;
}

export interface IndexRef extends HandleRef {
	bytes: number;
}

export interface XmustardCompactionDetails {
	version: string;
	derived: true;
	verified_memory: false;
	workspace_id?: string;
	reason?: string;
	snapshot: Snapshot;
	handles: HandleEntry[];
	carried: number; // handles taken over from an earlier xMustard compaction
	// carried handles that had expired and whose entries are no longer on the branch
	expired_dropped: number;
	// the index document; absent when the summary is the whole document
	index?: IndexRef;
	summary_bytes: number;
}

export interface CompactionDetails {
	readFiles: string[];
	modifiedFiles: string[];
	xmustard: XmustardCompactionDetails;
}

// Pi's AgentMessage roles beyond user/assistant/toolResult, as far as the index reads them.
export interface SummarizedMessageView extends MessageView {
	command?: string; // bashExecution
	output?: string;
	exitCode?: number;
	excludeFromContext?: boolean;
	customType?: string; // custom
	summary?: string; // branchSummary / compactionSummary
}

export interface PreparationView {
	firstKeptEntryId: string;
	messagesToSummarize: readonly SummarizedMessageView[];
	turnPrefixMessages?: readonly SummarizedMessageView[];
	tokensBefore: number;
	previousSummary?: string;
	fileOps?: { read?: Iterable<string>; written?: Iterable<string>; edited?: Iterable<string> };
}

export interface CompactEventView {
	preparation: PreparationView;
	branchEntries: readonly unknown[];
	customInstructions?: string;
	reason?: string;
	signal?: AbortSignal;
}

const isObject = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const oneLine = (s: string): string => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n: number): string => {
	const one = oneLine(s);
	return one.length > n ? `${one.slice(0, n - 1)}…` : one;
};
const bytesOf = (s: string): number => Buffer.byteLength(s, "utf8");

function relative(p: string, cwd: string | undefined): string {
	if (!cwd) return p;
	const rel = path.relative(cwd, p);
	return rel && !rel.startsWith("..") && !path.isAbsolute(rel) ? rel : p;
}

// label names a result briefly: the command, path or query it ran on.
export function labelOf(r: ResultRecord, cwd?: string): string {
	if (r.command) return `${r.toolName} \`${clip(r.command, 60)}\``;
	if (r.path) return `${r.toolName} ${relative(r.path, cwd)}`;
	const a = r.args;
	for (const k of ["pattern", "q", "symbol", "path", "run_id", "entry_id", "handle"]) {
		if (typeof a[k] === "string" && a[k] !== "") return `${r.toolName} ${k}=${clip(String(a[k]), 50)}`;
	}
	return r.toolName;
}

// failureKey groups results that re-run the same thing: a later success closes an
// earlier failure with the same key.
const failureKey = (r: ResultRecord): string => `${r.toolName}\u0000${r.command ?? r.path ?? JSON.stringify(r.args)}`;

// lastLine is a failure's last line of tool output: never the adapter's recovery
// footer, page or search header, or stub header.
function lastLine(text: string, max: number): string {
	const lines = text.split("\n").filter((l) => l.trim() !== "" && !l.startsWith(MASK_PREFIX) && !isAdapterLine(l));
	return clip(lines.at(-1) ?? "", max);
}

// memoryOf reads an xMustard remember/verify result (JSON, maybe with a footer).
function memoryOf(r: ResultRecord): Record<string, unknown> | undefined {
	if (r.isError || r.masked) return undefined;
	try {
		const v = JSON.parse(r.text.split(/\n\[xmustard (?:evidence|injection-check)\] /)[0]) as unknown;
		return isObject(v) ? v : undefined;
	} catch {
		return undefined;
	}
}

// Clip limits of the snapshot in the summary and details; the index clips nothing
// but a failure's last line.
export interface SnapshotLimits {
	goal: number;
	request: number;
	progress: number;
	line: number;
	title: number;
}
const CLIPPED: SnapshotLimits = { goal: 240, request: 200, progress: 200, line: 140, title: 60 };
const FULL: SnapshotLimits = { goal: Infinity, request: Infinity, progress: Infinity, line: 2000, title: 400 };

// snapshotOf derives the structured snapshot from the whole branch before the cut.
export function snapshotOf(
	results: readonly ResultRecord[],
	a: { firstUser?: string; lastUser?: string; lastAssistantText?: string },
	fileOps: PreparationView["fileOps"],
	handles: ReadonlyMap<string, HandleRef>,
	cwd?: string,
	limits: SnapshotLimits = CLIPPED,
): Snapshot {
	const snap: Snapshot = { failures: [], modified_files: [], read_files: [], proposals: [], decisions: [] };
	const cut = (s: string, n: number) => (n === Infinity ? s.trim() : clip(s, n));
	if (a.firstUser) snap.goal = cut(a.firstUser, limits.goal);
	if (a.lastUser && a.lastUser !== a.firstUser) snap.latest_request = cut(a.lastUser, limits.request);
	if (a.lastAssistantText) snap.last_progress = cut(a.lastAssistantText, limits.progress);
	// open failures: the latest failure of each key with no later success of that key;
	// a masked failure's line comes from its original, never from the stub
	const settled = new Set<string>();
	for (let i = results.length - 1; i >= 0; i--) {
		const r = results[i];
		const key = failureKey(r);
		if (settled.has(key)) continue;
		settled.add(key);
		if (!r.isError) continue;
		snap.failures.push({ tool: r.toolName, turn: r.turn, label: labelOf(r, cwd), line: lastLine(preMaskText(r), limits.line), handle: handles.get(r.toolCallId)?.handle });
	}
	const modified = new Set<string>();
	const read = new Set<string>();
	for (const p of [...(fileOps?.edited ?? []), ...(fileOps?.written ?? [])]) modified.add(relative(resolvePath(p, cwd), cwd));
	for (const p of fileOps?.read ?? []) read.add(relative(resolvePath(p, cwd), cwd));
	for (const r of results) {
		if (!r.path || r.isError) continue;
		if (EDIT_TOOLS.has(r.toolName)) modified.add(relative(r.path, cwd));
		else if (r.toolName === "read") read.add(relative(r.path, cwd));
	}
	snap.modified_files = [...modified];
	snap.read_files = [...read].filter((p) => !modified.has(p));
	for (const r of results) {
		const m = r.toolName === "remember" || r.toolName === "verify" ? memoryOf(r) : undefined;
		if (!m || typeof m.id !== "string") continue;
		const status = typeof m.status === "string" ? m.status : "unknown";
		const title = typeof m.title === "string" && m.title ? clip(m.title, limits.title) : undefined;
		if (r.toolName === "remember") {
			if (status === "pending" || status === "proposed") snap.proposals.push({ id: m.id, ...(title ? { title } : {}), status });
			else snap.decisions.push(`remembered ${m.id}${title ? ` "${title}"` : ""} (${status}${typeof m.verification_mode === "string" ? `, ${m.verification_mode}` : ""})`);
		} else {
			const approve = r.args.approve === false ? "reject" : "approve";
			snap.decisions.push(`verify ${String(r.args.entry_id ?? m.id)} ${approve} → ${status}`);
		}
	}
	return snap;
}

// Lines accumulates snapshot lines up to a UTF-8 byte budget.
class Lines {
	private readonly max: number;
	private readonly out: string[] = [];
	private used = 0;
	reserve = 0; // bytes kept free for a line that must follow
	constructor(max: number) {
		this.max = max;
	}
	fits(line: string): boolean {
		return this.used + bytesOf(line) + (this.out.length ? 1 : 0) + this.reserve <= this.max;
	}
	add(line: string): boolean {
		if (!this.fits(line)) return false;
		this.used += bytesOf(line) + (this.out.length ? 1 : 0);
		this.out.push(line);
		return true;
	}
	// section adds a heading and as many items as fit (the heading only with one).
	section(heading: string, items: readonly string[]): number {
		if (items.length === 0 || !this.fits(heading)) return 0;
		const mark = this.out.length;
		const used = this.used;
		this.add(heading);
		let n = 0;
		for (const it of items) {
			if (!this.add(it)) break;
			n++;
		}
		if (n === 0) {
			this.out.length = mark;
			this.used = used;
		}
		return n;
	}
	text(): string {
		return this.out.join("\n");
	}
}

const failureLine = (f: Snapshot["failures"][number]): string => `- ${f.label} (turn ${f.turn}): ${f.line || "failed"}${f.handle ? ` → ${f.handle}` : ""}`;
const handleLine = (h: HandleEntry, withExpiry = false): string =>
	`- ${h.label}${h.is_error ? " (error)" : ""}, ${h.bytes} B → ${h.handle}${h.offset !== undefined ? ` offset ${h.offset}` : ""}${withExpiry && h.expires_at ? ` (expires ${h.expires_at})` : ""}`;
const isSnapshot = (s: string | undefined): boolean => !!s && s.startsWith(SNAPSHOT_HEADER);

function headerLine(handles: readonly HandleEntry[], workspaceId: string | undefined): string {
	return `${SNAPSHOT_HEADER} Derived from this Pi session by the xMustard adapter; not verified memory.${handles.length ? ` ${handles.length} earlier tool outputs are recoverable with ${EXPAND_TOOL}(workspace_id${workspaceId ? `="${workspaceId}"` : ""}, handle).` : ""}`;
}

// indexLine names the index document; it is always the snapshot's second line.
export function indexLine(index: HandleRef): string {
	return `Snapshot index (every failure, handle and compacted message; page it, or search it with pattern=<RE2>): ${EXPAND_TOOL}(workspace_id="${index.workspace_id}", handle="${index.handle}", offset=0)`;
}

// renderSnapshot renders the snapshot within maxBytes, highest tier first. With an
// index, its second line names it and everything cut or clipped here is in it.
export function renderSnapshot(
	snap: Snapshot,
	handles: readonly HandleEntry[],
	workspaceId: string | undefined,
	previousSummary?: string,
	maxBytes = SNAPSHOT_MAX_BYTES,
	index?: HandleRef,
): string {
	const L = new Lines(maxBytes);
	L.add(headerLine(handles, workspaceId));
	if (index) L.add(indexLine(index));
	const inIndex = index ? "; all of them in the index" : "";
	if (snap.goal) L.add(`Goal: ${snap.goal}`);
	if (snap.latest_request) L.add(`Latest request: ${snap.latest_request}`);
	const nf = snap.failures.length;
	L.section(nf > 3 ? `Open failures (${nf}, newest first${inIndex}):` : "Open failures:", snap.failures.slice(0, 3).map(failureLine));
	if (snap.modified_files.length) L.add(`Modified files: ${clip(snap.modified_files.slice(0, 8).join(", "), 300)}`);
	if (snap.read_files.length) L.add(`Read files: ${clip(snap.read_files.slice(0, 8).join(", "), 240)}`);
	L.section(
		"Memory proposals awaiting a distinct verifier:",
		snap.proposals.slice(0, 3).map((p) => `- ${p.id}${p.title ? ` "${p.title}"` : ""} (${p.status})`),
	);
	L.section(
		"Memory decisions this session:",
		snap.decisions.slice(-3).map((d) => `- ${d}`),
	);
	if (snap.last_progress) L.add(`Last progress: ${snap.last_progress}`);
	if (previousSummary && !isSnapshot(previousSummary)) {
		const c = clip(previousSummary, 300);
		L.add(`Earlier summary: ${c}${index && c !== oneLine(previousSummary) ? " (full text in the index)" : ""}`);
	}
	const shown = [...handles].reverse();
	const more = (k: number) => `(+${k} more${index ? " in the index" : ""})`;
	L.reserve = bytesOf(more(shown.length)) + 1;
	const n = L.section(`Recoverable outputs (newest first${inIndex}):`, shown.map((h) => handleLine(h)));
	L.reserve = 0;
	if (n < shown.length) L.add(more(shown.length - n));
	return L.text();
}

// transcript renders the compacted messages for the index: user and assistant text,
// tool calls with their arguments, and each tool result by its handle or, when it has
// none (1 KiB or less), inline.
export function transcript(messages: readonly SummarizedMessageView[], results: ReadonlyMap<string, ResultRecord>, refs: ReadonlyMap<string, HandleRef>): string[] {
	const out: string[] = [];
	for (const m of messages) {
		if (m.role === "user") {
			const { text, image } = textOf(m.content);
			out.push(`[user] ${text}${image ? " [image content not retained]" : ""}`);
		} else if (m.role === "assistant") {
			for (const b of Array.isArray(m.content) ? m.content : []) {
				const block = b as { type?: string; text?: unknown; thinking?: unknown; id?: unknown; name?: unknown; arguments?: unknown };
				if (block.type === "text" && typeof block.text === "string" && block.text.trim()) out.push(`[assistant] ${block.text}`);
				else if (block.type === "thinking" && typeof block.thinking === "string" && block.thinking.trim()) out.push(`[assistant thinking] ${block.thinking}`);
				else if (block.type === "toolCall") out.push(`[tool call ${String(block.id)}] ${String(block.name)} ${JSON.stringify(block.arguments ?? {})}`);
			}
		} else if (m.role === "toolResult" && m.toolCallId) {
			const r = results.get(m.toolCallId);
			const ref = refs.get(m.toolCallId);
			const head = `[tool result ${m.toolCallId}] ${m.toolName ?? r?.toolName ?? "tool"}${m.isError ? " (error)" : ""}`;
			if (r?.omitted) out.push(`${head}: removed from context by a context edit`);
			else if (ref) out.push(`${head}, ${bytesOf(r ? preMaskText(r) : textOf(m.content).text)} B → ${ref.handle}${ref.offset !== undefined ? ` offset ${ref.offset}` : ""}`);
			else {
				const { text, image } = r ? { text: preMaskText(r), image: r.image } : textOf(m.content);
				out.push(`${head}, ${bytesOf(text)} B${image ? " (image content not retained)" : ""}:\n${text}`);
			}
		} else if (m.role === "bashExecution") {
			if (!m.excludeFromContext) out.push(`[user bash] ${m.command ?? ""} (exit ${m.exitCode ?? "?"})\n${m.output ?? ""}`);
		} else if (m.role === "branchSummary" || m.role === "compactionSummary") {
			out.push(`[${m.role}] ${m.summary ?? ""}`);
		} else {
			out.push(`[${m.role}${m.customType ? ` ${m.customType}` : ""}] ${textOf(m.content).text}`);
		}
	}
	return out;
}

// indexBody is the index document without its header line: nothing clipped, and the
// same line formats as the snapshot.
export function indexBody(o: {
	snapshot: Snapshot;
	handles: readonly HandleEntry[];
	previousSummary?: string;
	messages: readonly string[];
	expiredDropped: number;
	droppedFromDetails: number;
}): string {
	const s = o.snapshot;
	const out: string[] = [];
	if (s.goal) out.push(`Goal: ${s.goal}`);
	if (s.latest_request) out.push(`Latest request: ${s.latest_request}`);
	if (s.failures.length) out.push("Open failures:", ...s.failures.map(failureLine));
	if (s.modified_files.length) out.push(`Modified files: ${s.modified_files.join(", ")}`);
	if (s.read_files.length) out.push(`Read files: ${s.read_files.join(", ")}`);
	if (s.proposals.length) out.push("Memory proposals awaiting a distinct verifier:", ...s.proposals.map((p) => `- ${p.id}${p.title ? ` "${p.title}"` : ""} (${p.status})`));
	if (s.decisions.length) out.push("Memory decisions this session:", ...s.decisions.map((d) => `- ${d}`));
	if (s.last_progress) out.push(`Last progress: ${s.last_progress}`);
	if (o.previousSummary) out.push("Earlier summary:", o.previousSummary);
	if (o.handles.length) out.push("Recoverable outputs (newest first):", ...[...o.handles].reverse().map((h) => handleLine(h, true)));
	if (o.droppedFromDetails) out.push(`(${o.droppedFromDetails} of these exceed the compaction details' ${MAX_CARRIED_HANDLES}-handle limit; only this index names them.)`);
	if (o.expiredDropped) out.push(`${o.expiredDropped} older outputs are no longer recoverable: their handles expired and their entries are no longer on this branch.`);
	if (o.messages.length) out.push("Compacted messages:", ...o.messages);
	return out.join("\n");
}

const INDEX_PREAMBLE = `${INDEX_HEADER} ${COMPACTION_VERSION}. Derived from this Pi session by the xMustard adapter; not verified memory. It holds everything the compaction removed from model context: the snapshot in full, every recoverable output and the compacted messages (tool outputs above 1 KiB appear by handle).`;

// carriedHandles returns the handles an earlier xMustard compaction recorded.
export function carriedHandles(compactions: readonly EntryView[]): HandleEntry[] {
	for (let i = compactions.length - 1; i >= 0; i--) {
		const d = compactions[i].details;
		const xm = isObject(d) && isObject(d.xmustard) ? d.xmustard : undefined;
		if (xm?.version === COMPACTION_VERSION && Array.isArray(xm.handles)) {
			return (xm.handles as unknown[]).filter((h): h is HandleEntry => isObject(h) && typeof h.handle === "string" && typeof h.workspace_id === "string");
		}
	}
	return [];
}

// branchHoldsHandle reports whether the branch names a recovery handle the model may
// be told to expand: a mask stub, an xMustard compaction's handles or index, or a
// projected tool result. Session start keeps xmustard_expand active then, so a
// resumed, reloaded or forked session can still recover what it masked or compacted.
export function branchHoldsHandle(entries: readonly EntryView[]): boolean {
	return entries.some((e) => {
		switch (e.type) {
			case "context_edit":
				return !!e.replacement && textOf(e.replacement.content).text.startsWith(MASK_PREFIX);
			case "compaction": {
				const xm = isObject(e.details) && isObject(e.details.xmustard) ? e.details.xmustard : undefined;
				return xm?.version === COMPACTION_VERSION && ((Array.isArray(xm.handles) && xm.handles.length > 0) || isObject(xm.index));
			}
			case "message":
				return e.message?.role === "toolResult" && knownHandle(e.message.toolName ?? "", e.message.details, {}, "") !== undefined;
			default:
				return false;
		}
	});
}

export interface CompactorDeps {
	enabled: boolean;
	capturer: Capturer;
	resolveWorkspace(cwd: string | undefined, signal?: AbortSignal): Promise<string>;
	onHandle(): void;
	now?: () => number;
}

export interface CompactionResultView {
	compaction: { summary: string; firstKeptEntryId: string; tokensBefore: number; details: CompactionDetails };
}

function entryOf(r: ResultRecord, h: HandleRef, cwd: string | undefined): HandleEntry {
	const body = preMaskText(r);
	return { ...h, tool: r.toolName, tool_call_id: r.toolCallId, turn: r.turn, is_error: r.isError, bytes: bytesOf(body), lines: countLines(body), label: labelOf(r, cwd) };
}

// buildCompaction retains what the snapshot removes and assembles the entry, or
// returns a reason to leave compaction to Pi.
export async function buildCompaction(event: CompactEventView, ctx: SessionContextView, deps: CompactorDeps): Promise<CompactionResultView | { fallback: string }> {
	const now = (deps.now ?? Date.now)();
	const live = (h: HandleRef | undefined): h is HandleRef => isLive(h, now, EXPIRY_MARGIN_MS);
	const prep = event.preparation;
	const a = analyzeBranch(event.branchEntries as EntryView[], { cwd: ctx.cwd });
	const byCall = new Map(a.results.map((r) => [r.toolCallId, r]));
	const messages = [...prep.messagesToSummarize, ...(prep.turnPrefixMessages ?? [])];
	const summarizedIds = new Set<string>();
	for (const m of messages) if (m.role === "toolResult" && m.toolCallId) summarizedIds.add(m.toolCallId);
	// everything before the kept range: the snapshot covers the whole history, since
	// it replaces the previous summary as well
	const keptAt = (event.branchEntries as EntryView[]).findIndex((e) => e.id === prep.firstKeptEntryId);
	const keptIds = new Set(keptAt >= 0 ? (event.branchEntries as EntryView[]).slice(keptAt).map((e) => e.id) : []);
	const before = a.results.filter((r) => !keptIds.has(r.entryId));
	const summarized = a.results.filter((r) => summarizedIds.has(r.toolCallId) && !r.omitted && !r.image);
	const retainable = (r: ResultRecord | undefined): r is ResultRecord => !!r && !r.omitted && !r.image && bytesOf(preMaskText(r)) >= RETAIN_MIN_BYTES;
	// handles an earlier compaction carried: live ones stay; expired ones are retained
	// again from the branch, or dropped (and counted) when it no longer holds them
	const ownIds = new Set(summarized.map((r) => r.toolCallId));
	const carriedAll = carriedHandles(a.compactions).filter((h) => !ownIds.has(h.tool_call_id));
	const carriedLive = carriedAll.filter((h) => live(h));
	const reRetain = carriedAll.filter((h) => !isLive(h, now, EXPIRY_MARGIN_MS)).flatMap((h) => {
		const r = byCall.get(h.tool_call_id);
		return retainable(r) ? [r] : [];
	});
	const expiredDropped = carriedAll.length - carriedLive.length - reRetain.length;
	const needs = [...summarized.filter((r) => !live(r.handle) && retainable(r)), ...reRetain];
	if (needs.length > MAX_RETAIN_PER_COMPACTION) return { fallback: `${needs.length} outputs would need retaining (limit ${MAX_RETAIN_PER_COMPACTION})` };
	const retainDeps: RetainDeps = { capturer: deps.capturer, sessionId: sessionIdOf(ctx), signal: event.signal ?? ctx.signal };
	const ensureWorkspace = async (): Promise<string | undefined> => {
		if (retainDeps.workspaceId) return undefined;
		const paused = deps.capturer.paused;
		if (paused) return `capture paused: ${paused}`;
		try {
			retainDeps.workspaceId = await deps.resolveWorkspace(ctx.cwd, retainDeps.signal);
			return undefined;
		} catch (err) {
			return err instanceof Error ? err.message : String(err);
		}
	};
	if (needs.length > 0) {
		const why = await ensureWorkspace();
		if (why) return { fallback: why };
	}
	const refs = new Map<string, HandleRef>(); // toolCallId → handle
	for (const r of summarized) if (live(r.handle)) refs.set(r.toolCallId, r.handle);
	let failure: string | undefined;
	await mapLimit(needs, RETAIN_CONCURRENCY, async (r) => {
		if (failure) return;
		try {
			refs.set(r.toolCallId, await retainText(r, preMaskText(r), retainDeps));
		} catch (err) {
			failure ??= `retaining ${r.toolName} ${r.toolCallId}: ${err instanceof Error ? err.message : String(err)}`;
		}
	});
	if (failure) return { fallback: failure };
	if (event.signal?.aborted) return { fallback: "compaction aborted" };
	const entry = (r: ResultRecord): HandleEntry[] => {
		const h = refs.get(r.toolCallId);
		return h ? [entryOf(r, h, ctx.cwd)] : [];
	};
	const carried = [...carriedLive, ...reRetain.flatMap(entry)];
	const all = [...carried, ...summarized.flatMap(entry)];
	const handles = all.slice(-MAX_CARRIED_HANDLES);
	// failures anywhere before the cut name their handle when a live one is known
	const known = new Map(refs);
	for (const h of carried) if (!known.has(h.tool_call_id)) known.set(h.tool_call_id, h);
	for (const r of before) if (live(r.handle) && !known.has(r.toolCallId)) known.set(r.toolCallId, r.handle);
	const snapshot = snapshotOf(before, a, prep.fileOps, known, ctx.cwd);
	const body = indexBody({
		snapshot: snapshotOf(before, a, prep.fileOps, known, ctx.cwd, FULL),
		handles: all,
		previousSummary: prep.previousSummary,
		messages: transcript(messages, byCall, refs),
		expiredDropped,
		droppedFromDetails: all.length - handles.length,
	});
	let workspaceId = retainDeps.workspaceId ?? all[0]?.workspace_id;
	const whole = body ? `${headerLine(all, workspaceId)}\n${body}` : headerLine(all, workspaceId);
	let summary = whole;
	let index: IndexRef | undefined;
	if (bytesOf(whole) > SNAPSHOT_MAX_BYTES) {
		// the summary cannot hold everything: retain the whole document behind one handle
		const doc = `${INDEX_PREAMBLE}\n${body}`;
		if (bytesOf(doc) > MAX_INDEX_BYTES) return { fallback: `the compaction index would be ${bytesOf(doc)} bytes (limit ${MAX_INDEX_BYTES})` };
		const why = await ensureWorkspace();
		if (why) return { fallback: why };
		try {
			const ref = await retainText({ toolName: INDEX_TOOL, toolCallId: `compaction:${prep.firstKeptEntryId}`, isError: false, args: {} }, doc, retainDeps);
			index = { ...ref, bytes: bytesOf(doc) };
		} catch (err) {
			return { fallback: `retaining the compaction index: ${err instanceof Error ? err.message : String(err)}` };
		}
		workspaceId = index.workspace_id;
		summary = renderSnapshot(snapshot, all, workspaceId, prep.previousSummary, SNAPSHOT_MAX_BYTES, index);
	}
	if (event.signal?.aborted) return { fallback: "compaction aborted" };
	if (all.length || index) deps.onHandle();
	return {
		compaction: {
			summary,
			firstKeptEntryId: prep.firstKeptEntryId,
			tokensBefore: prep.tokensBefore,
			details: {
				readFiles: snapshot.read_files,
				modifiedFiles: snapshot.modified_files,
				xmustard: {
					version: COMPACTION_VERSION,
					derived: true,
					verified_memory: false,
					...(workspaceId ? { workspace_id: workspaceId } : {}),
					...(event.reason ? { reason: event.reason } : {}),
					snapshot,
					handles,
					carried: carried.length,
					expired_dropped: expiredDropped,
					...(index ? { index } : {}),
					summary_bytes: bytesOf(summary),
				},
			},
		},
	};
}

// createCompactor returns Pi's session_before_compact handler.
export function createCompactor(deps: CompactorDeps, onFallback?: (reason: string) => void) {
	return async (event: CompactEventView, ctx: SessionContextView): Promise<CompactionResultView | undefined> => {
		if (!deps.enabled) return undefined;
		if (event.customInstructions?.trim()) {
			onFallback?.("custom instructions: Pi's summarizer honors them");
			return undefined;
		}
		const out = await buildCompaction(event, ctx, deps);
		if ("fallback" in out) {
			onFallback?.(out.fallback);
			return undefined;
		}
		return out;
	};
}
