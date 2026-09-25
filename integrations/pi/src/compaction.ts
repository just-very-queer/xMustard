// Compaction snapshot and restore for Pi (PAR-HAR-04).
//
// At Pi's compaction boundary (session_before_compact) the adapter supplies the
// compaction itself: a deterministic, priority-tiered snapshot of at most 2 KB
// (goal, open failures with handles, active files, pending memory proposals, memory
// decisions, last progress, then recoverable outputs) instead of Pi's model-written
// summary. Every summarized tool output above 1 KiB is retained behind a recovery
// handle first, and the CompactionEntry's `details` carry all of them (plus the ones
// an earlier xMustard compaction carried), so after compaction the model can still
// recover any earlier output with xmustard_expand. The snapshot is derived from this
// session and is never verified memory; it says so.
//
// When the snapshot cannot keep that promise (xMustard unreachable or capture
// unavailable while summarized outputs still need a handle), or the user asked for a
// focused summary (/compact <instructions>), the handler returns nothing and Pi's
// own compaction runs.

import path from "node:path";
import { EXPAND_TOOL } from "./delivery.ts";
import {
	analyzeBranch,
	type EntryView,
	type HandleRef,
	MASK_PREFIX,
	type MessageView,
	mapLimit,
	type ResultRecord,
	type RetainDeps,
	retain,
	type SessionContextView,
	sessionIdOf,
} from "./masking.ts";
import type { Capturer } from "./delivery.ts";
import { EDIT_TOOLS } from "./tools.ts";

export const COMPACTION_VERSION = "xmustard.pi-compaction/v1";
export const SNAPSHOT_MAX_BYTES = 2048;
export const SNAPSHOT_HEADER = "[xmustard compaction snapshot]";
// Summarized outputs above this are retained (a smaller one cannot get a handle).
export const RETAIN_MIN_BYTES = (1 << 10) + 1;
// Most outputs one compaction retains; beyond it Pi's own compaction runs.
export const MAX_RETAIN_PER_COMPACTION = 512;
// Most handles one entry carries (this compaction's plus carried ones).
export const MAX_CARRIED_HANDLES = 1024;
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

export interface XmustardCompactionDetails {
	version: string;
	derived: true;
	verified_memory: false;
	workspace_id?: string;
	reason?: string;
	snapshot: Snapshot;
	handles: HandleEntry[];
	carried: number; // handles taken over from an earlier xMustard compaction
	summary_bytes: number;
}

export interface CompactionDetails {
	readFiles: string[];
	modifiedFiles: string[];
	xmustard: XmustardCompactionDetails;
}

export interface PreparationView {
	firstKeptEntryId: string;
	messagesToSummarize: readonly MessageView[];
	turnPrefixMessages?: readonly MessageView[];
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
const clip = (s: string, n: number): string => {
	const one = s.replace(/\s+/g, " ").trim();
	return one.length > n ? `${one.slice(0, n - 1)}…` : one;
};

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

function lastLine(text: string): string {
	const lines = text.split("\n").filter((l) => l.trim() !== "" && !l.startsWith(MASK_PREFIX) && !l.startsWith("[xmustard evidence] "));
	const tagged = lines.filter((l) => /^(first|last) line: /.test(l));
	const pick = (tagged.at(-1) ?? lines.at(-1) ?? "").replace(/^(first|last) line: /, "");
	return clip(pick, 140);
}

// memoryOf reads an xMustard remember/verify result (JSON, maybe with a footer).
function memoryOf(r: ResultRecord): Record<string, unknown> | undefined {
	if (r.isError || r.masked) return undefined;
	try {
		const v = JSON.parse(r.text.split("\n[xmustard evidence] ")[0]) as unknown;
		return isObject(v) ? v : undefined;
	} catch {
		return undefined;
	}
}

// snapshotOf derives the structured snapshot from the whole branch before the cut.
export function snapshotOf(results: readonly ResultRecord[], a: { firstUser?: string; lastUser?: string; lastAssistantText?: string }, fileOps: PreparationView["fileOps"], handles: ReadonlyMap<string, HandleRef>, cwd?: string): Snapshot {
	const snap: Snapshot = { failures: [], modified_files: [], read_files: [], proposals: [], decisions: [] };
	if (a.firstUser) snap.goal = clip(a.firstUser, 240);
	if (a.lastUser && a.lastUser !== a.firstUser) snap.latest_request = clip(a.lastUser, 200);
	if (a.lastAssistantText) snap.last_progress = clip(a.lastAssistantText, 200);
	// open failures: the latest failure of each key with no later success of that key
	const settled = new Set<string>();
	for (let i = results.length - 1; i >= 0; i--) {
		const r = results[i];
		const key = failureKey(r);
		if (settled.has(key)) continue;
		settled.add(key);
		if (!r.isError) continue;
		snap.failures.push({ tool: r.toolName, turn: r.turn, label: labelOf(r, cwd), line: lastLine(r.text), handle: handles.get(r.toolCallId)?.handle });
	}
	const modified = new Set<string>();
	const read = new Set<string>();
	for (const p of [...(fileOps?.edited ?? []), ...(fileOps?.written ?? [])]) modified.add(relative(path.resolve(cwd ?? "/", p), cwd));
	for (const p of fileOps?.read ?? []) read.add(relative(path.resolve(cwd ?? "/", p), cwd));
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
		const title = typeof m.title === "string" && m.title ? clip(m.title, 60) : undefined;
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
		return this.used + Buffer.byteLength(line, "utf8") + (this.out.length ? 1 : 0) + this.reserve <= this.max;
	}
	add(line: string): boolean {
		if (!this.fits(line)) return false;
		this.used += Buffer.byteLength(line, "utf8") + (this.out.length ? 1 : 0);
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

// renderSnapshot renders the snapshot within SNAPSHOT_MAX_BYTES, highest tier first.
export function renderSnapshot(snap: Snapshot, handles: readonly HandleEntry[], workspaceId: string | undefined, previousSummary?: string, maxBytes = SNAPSHOT_MAX_BYTES): string {
	const L = new Lines(maxBytes);
	L.add(
		`${SNAPSHOT_HEADER} Derived from this Pi session by the xMustard adapter; not verified memory.${handles.length ? ` ${handles.length} earlier tool outputs are recoverable with ${EXPAND_TOOL}(workspace_id${workspaceId ? `="${workspaceId}"` : ""}, handle).` : ""}`,
	);
	if (snap.goal) L.add(`Goal: ${snap.goal}`);
	if (snap.latest_request) L.add(`Latest request: ${snap.latest_request}`);
	L.section(
		"Open failures:",
		snap.failures.slice(0, 3).map((f) => `- ${f.label} (turn ${f.turn}): ${f.line || "failed"}${f.handle ? ` → ${f.handle}` : ""}`),
	);
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
	if (previousSummary && !previousSummary.startsWith(SNAPSHOT_HEADER)) L.add(`Earlier summary: ${clip(previousSummary, 300)}`);
	const shown = [...handles].reverse();
	L.reserve = Buffer.byteLength(`(+${shown.length} more)`, "utf8") + 1;
	const n = L.section(
		"Recoverable outputs (newest first; all of them are in the compaction details):",
		shown.map((h) => `- ${h.label}${h.is_error ? " (error)" : ""}, ${h.bytes} B → ${h.handle}${h.offset !== undefined ? ` offset ${h.offset}` : ""}`),
	);
	L.reserve = 0;
	if (n < shown.length) L.add(`(+${shown.length - n} more)`);
	return L.text();
}

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

export interface CompactorDeps {
	enabled: boolean;
	capturer: Capturer;
	resolveWorkspace(cwd: string | undefined, signal?: AbortSignal): Promise<string>;
	onHandle(): void;
}

export interface CompactionResultView {
	compaction: { summary: string; firstKeptEntryId: string; tokensBefore: number; details: CompactionDetails };
}

// buildCompaction retains the summarized outputs and assembles the entry, or returns
// a reason to leave compaction to Pi.
export async function buildCompaction(event: CompactEventView, ctx: SessionContextView, deps: CompactorDeps): Promise<CompactionResultView | { fallback: string }> {
	const prep = event.preparation;
	const a = analyzeBranch(event.branchEntries as EntryView[], { cwd: ctx.cwd });
	const summarizedIds = new Set<string>();
	for (const m of [...prep.messagesToSummarize, ...(prep.turnPrefixMessages ?? [])]) {
		if (m.role === "toolResult" && m.toolCallId) summarizedIds.add(m.toolCallId);
	}
	// everything before the kept range: the snapshot covers the whole history, since
	// it replaces the previous summary as well
	const keptAt = (event.branchEntries as EntryView[]).findIndex((e) => e.id === prep.firstKeptEntryId);
	const keptIds = new Set(keptAt >= 0 ? (event.branchEntries as EntryView[]).slice(keptAt).map((e) => e.id) : []);
	const before = a.results.filter((r) => !keptIds.has(r.entryId));
	const summarized = a.results.filter((r) => summarizedIds.has(r.toolCallId) && !r.omitted && !r.image);
	const retainDeps: RetainDeps = { capturer: deps.capturer, sessionId: sessionIdOf(ctx), signal: event.signal ?? ctx.signal };
	const needs = summarized.filter((r) => !r.handle && r.bytes >= RETAIN_MIN_BYTES);
	if (needs.length > MAX_RETAIN_PER_COMPACTION) return { fallback: `${needs.length} outputs would need retaining (limit ${MAX_RETAIN_PER_COMPACTION})` };
	if (needs.length > 0) {
		const paused = deps.capturer.paused;
		if (paused) return { fallback: `capture paused: ${paused}` };
		try {
			retainDeps.workspaceId = await deps.resolveWorkspace(ctx.cwd, retainDeps.signal);
		} catch (err) {
			return { fallback: err instanceof Error ? err.message : String(err) };
		}
	}
	const refs = new Map<string, HandleRef>(); // toolCallId → handle
	for (const r of summarized) if (r.handle) refs.set(r.toolCallId, r.handle);
	let failure: string | undefined;
	await mapLimit(needs, RETAIN_CONCURRENCY, async (r) => {
		if (failure) return;
		try {
			refs.set(r.toolCallId, await retain(r, retainDeps));
		} catch (err) {
			failure ??= `retaining ${r.toolName} ${r.toolCallId}: ${err instanceof Error ? err.message : String(err)}`;
		}
	});
	if (failure) return { fallback: failure };
	if (event.signal?.aborted) return { fallback: "compaction aborted" };
	const workspaceId = retainDeps.workspaceId ?? [...refs.values()][0]?.workspace_id;
	const own: HandleEntry[] = summarized.flatMap((r) => {
		const h = refs.get(r.toolCallId);
		return h
			? [{ ...h, tool: r.toolName, tool_call_id: r.toolCallId, turn: r.turn, is_error: r.isError, bytes: r.bytes, lines: r.lines, label: labelOf(r, ctx.cwd) }]
			: [];
	});
	const seen = new Set(own.map((h) => `${h.handle}@${h.offset ?? ""}`));
	const carried = carriedHandles(a.compactions).filter((h) => !seen.has(`${h.handle}@${h.offset ?? ""}`));
	const handles = [...carried, ...own].slice(-MAX_CARRIED_HANDLES);
	if (handles.length) deps.onHandle();
	// failures anywhere before the cut name their handle when one is known
	const known = new Map(refs);
	for (const h of carried) if (!known.has(h.tool_call_id)) known.set(h.tool_call_id, h);
	for (const r of before) if (r.handle && !known.has(r.toolCallId)) known.set(r.toolCallId, r.handle);
	const snapshot = snapshotOf(before, a, prep.fileOps, known, ctx.cwd);
	const summary = renderSnapshot(snapshot, handles, workspaceId, prep.previousSummary);
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
					summary_bytes: Buffer.byteLength(summary, "utf8"),
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
