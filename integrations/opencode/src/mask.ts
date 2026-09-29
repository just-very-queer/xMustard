// Retroactive masking of older tool results (PAR-CTX-07) through OpenCode 2.x's
// session "context" hook, which hands a plugin the messages of each model request
// (@opencode/plugin 2.0.1 promise/session.d.ts SessionContext) before they are sent.
//
// A tool result older than `afterTurns` assistant turns is replaced, in that request
// only, by a stub
//
//   [xmustard masked: 812 lines, 14203 bytes of shell output; turn 3; handle xm1.…; workspace_id w; xmustard://evidence/…] first line
//
// that names a recovery handle. OpenCode rebuilds the messages for every request, so
// the plugin re-applies the stubs it decided on each time; the stored session keeps
// the raw result. The set of stubs only grows every `everyTurns` turns (a polling
// window), so between windows the prompt prefix does not change and the provider's
// prompt cache survives. The latest failure is never masked. A result without a live
// handle is first retained through the capture route at the smallest target; if that
// fails it stays unmasked. Messages are never mutated: a masked message is a copy
// with the same prototype.

import { textOf } from "./capture.ts";
import type { MaskConfig } from "./config.ts";
import { type EvidenceRecord, isLive, type Reducer, sanitize } from "./reduce.ts";

export const MASK_PREFIX = "[xmustard masked: ";

// Structural views of @opencode/ai 2.0.1 messages (schema/messages.d.ts): a
// tool-result part carries the call id, the tool name (and namespace) and a result
// value of type text, json, error or content.
export interface PartView {
	readonly type: string;
	readonly id?: string;
	readonly name?: string;
	readonly namespace?: string;
	readonly input?: unknown;
	readonly result?: { readonly type: string; readonly value?: unknown };
}

export interface MessageView {
	readonly role: string;
	readonly content: ReadonlyArray<PartView>;
}

export interface ContextView {
	readonly sessionID: string;
	messages: MessageView[];
}

export interface ResultRef {
	message: number; // index into messages
	part: number; // index into that message's content
	id: string;
	tool: string;
	turn: number; // assistant turns up to the call (1-based)
	text: string;
	isError: boolean;
	input: unknown;
}

const asText = (v: unknown): string => (typeof v === "string" ? v : (JSON.stringify(v) ?? ""));

// resultText reads a result value by its type (the closed ToolResultValue union);
// content with anything but text blocks is never masked.
const resultText: Record<string, (v: unknown) => string | undefined> = {
	text: asText,
	error: asText,
	json: asText,
	content: (v) => textOf(v),
};

// toolID is the name OpenCode gives a namespaced tool: "<namespace>_<name>".
const toolID = (p: PartView): string => (p.namespace ? `${p.namespace.replaceAll(".", "_")}_${sanitize(p.name ?? "")}` : (p.name ?? ""));

export function analyze(messages: ReadonlyArray<MessageView>): { turn: number; results: ResultRef[] } {
	let turn = 0;
	const inputs = new Map<string, unknown>();
	const results: ResultRef[] = [];
	messages.forEach((m, mi) => {
		if (m.role === "assistant") turn++;
		m.content.forEach((p, pi) => {
			if (p.type === "tool-call" && p.id) inputs.set(p.id, p.input);
			if (p.type !== "tool-result" || !p.id || !p.result) return;
			const text = resultText[p.result.type]?.(p.result.value);
			if (text === undefined) return;
			results.push({ message: mi, part: pi, id: p.id, tool: toolID(p), turn, text, isError: p.result.type === "error", input: inputs.get(p.id) });
		});
	});
	return { turn, results };
}

export function countLines(text: string): number {
	if (text === "") return 0;
	const n = text.split("\n").length;
	return text.endsWith("\n") ? n - 1 : n;
}

const FIRST_LINE_CHARS = 160;

export function stubFor(r: ResultRef, rec: EvidenceRecord): string {
	const first = r.text.split("\n", 1)[0] ?? "";
	const shown = first.length > FIRST_LINE_CHARS ? `${first.slice(0, FIRST_LINE_CHARS)}…` : first;
	const where = rec.resource_uri ? `; ${rec.resource_uri}` : "";
	return `${MASK_PREFIX}${countLines(r.text)} lines, ${Buffer.byteLength(r.text, "utf8")} bytes of ${r.tool} output${r.isError ? " (error)" : ""}; turn ${r.turn}; handle ${rec.handle}; workspace_id ${rec.workspace_id}${where}] ${shown}`;
}

interface Stub {
	text: string;
	expiresAt?: string;
}

interface SessionMask {
	windowTurn: number;
	stubs: Map<string, Stub>; // call id -> stub
}

const MAX_SESSIONS = 64;

export class Masker {
	private readonly cfg: MaskConfig;
	private readonly reducer: Reducer;
	private readonly now: () => number;
	private readonly sessions = new Map<string, SessionMask>();

	constructor(cfg: MaskConfig, reducer: Reducer, now: () => number = Date.now) {
		this.cfg = cfg;
		this.reducer = reducer;
		this.now = now;
	}

	// apply masks the context's older results in place of OpenCode's. It never throws:
	// on any failure the context stays as OpenCode built it.
	async apply(ctx: ContextView): Promise<void> {
		try {
			const view = analyze(ctx.messages);
			const st = this.session(ctx.sessionID);
			if (view.turn - st.windowTurn >= this.cfg.everyTurns) {
				st.windowTurn = view.turn;
				await this.advance(ctx.sessionID, view, st);
			}
			applyStubs(ctx.messages, view.results, st.stubs);
		} catch {
			// fail open
		}
	}

	private session(id: string): SessionMask {
		let st = this.sessions.get(id);
		if (!st) {
			st = { windowTurn: 0, stubs: new Map() };
			this.sessions.set(id, st);
			for (const oldest of this.sessions.keys()) {
				if (this.sessions.size <= MAX_SESSIONS) break;
				this.sessions.delete(oldest);
			}
		}
		return st;
	}

	private async advance(sessionID: string, view: { turn: number; results: ResultRef[] }, st: SessionMask): Promise<void> {
		const now = this.now();
		// a stub whose handle is about to expire is dropped and retained afresh below
		for (const [id, stub] of st.stubs) if (!isLive(stub.expiresAt, now)) st.stubs.delete(id);
		const latestFailure = view.results.findLast((r) => r.isError)?.id;
		// a result stays visible while any of these holds
		const keepVisible: ((r: ResultRef) => boolean)[] = [
			(r) => st.stubs.has(r.id),
			(r) => r.id === latestFailure,
			(r) => view.turn - r.turn < this.cfg.afterTurns,
			(r) => !this.reducer.maskable(r.tool),
			(r) => Buffer.byteLength(r.text, "utf8") < this.cfg.minBytes,
			(r) => r.text.startsWith(MASK_PREFIX),
		];
		for (const r of view.results) {
			if (keepVisible.some((keep) => keep(r))) continue;
			const outcome = { tool: r.tool, sessionID, callID: r.id, input: r.input ?? {}, ...(r.isError ? { error: r.text } : { text: r.text }) };
			const rec = this.reducer.handle(r.id) ?? (await this.reducer.retain(outcome));
			if (rec) st.stubs.set(r.id, { text: stubFor(r, rec), expiresAt: rec.expires_at });
		}
	}
}

// applyStubs replaces each masked result's part by a copy carrying its stub; the
// message is copied with its prototype kept, and never mutated.
export function applyStubs(messages: MessageView[], results: ReadonlyArray<ResultRef>, stubs: ReadonlyMap<string, Stub>): void {
	const byMessage = new Map<number, Map<number, string>>();
	for (const r of results) {
		const stub = stubs.get(r.id);
		if (!stub || stub.text === r.text) continue;
		const parts = byMessage.get(r.message) ?? new Map<number, string>();
		parts.set(r.part, stub.text);
		byMessage.set(r.message, parts);
	}
	for (const [mi, parts] of byMessage) {
		const m = messages[mi];
		if (!m) continue;
		const content = m.content.map((p, pi) => {
			const stub = parts.get(pi);
			return stub === undefined ? p : { ...p, result: { type: p.result?.type === "error" ? "error" : "text", value: stub } };
		});
		messages[mi] = Object.assign(Object.create(Object.getPrototypeOf(m)) as MessageView, m, { content });
	}
}
