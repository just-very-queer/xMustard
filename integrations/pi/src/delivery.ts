// Evidence delivery for the Pi adapter: run one xMustard tool over HTTP, pass its
// result through Go's shared projection, and page retained originals back.
//
// Two delivery paths share Go's evidence module (api-go/internal/evidence):
//
//   source (default) — execute() asks the tool route for an evidence envelope
//     (`X-Xmustard-Delivery`). Go spools, captures and projects the output at the
//     source, sampling repository identity before and after the handler, so the
//     result can carry `captured_identity: "bound"`.
//   hook — execute() fetches the raw result and the `tool_result` hook POSTs it to
//     /evidence. Go cannot know which repository state produced bytes it receives
//     after the fact, so these observations are always `captured_identity:
//     "unknown"` and every expansion reports `stale: true`. The adapter never
//     supplies a before-execution identity of its own.
//
// Either way only the bounded projection enters model context; originals stay on
// the Go side and are read in pages through xmustard_expand until they expire.

import { createHash } from "node:crypto";
import type { AdapterConfig } from "./config.ts";
import { errorFromResponse, type HttpResponse, send, XmustardHttpError } from "./http.ts";
import { checkRequired, goQueryEscape, type ToolArgs, type ToolSpec } from "./tools.ts";

export const DELIVERY_VERSION = "xmustard.evidence/v1";
export const DELIVERY_HEADER = "X-Xmustard-Delivery";
export const ADAPTER_VERSION = "pi-adapter/0.1.0";
export const EXPAND_TOOL = "xmustard_expand";

// Inline limit for results that reach the model without a Go projection (Go's own
// projection target). Anything larger is replaced by an explicit size error.
export const INLINE_LIMIT = 64 << 10;
// Largest raw original Go will capture; the adapter refuses to buffer more.
export const MAX_ORIGINAL = 16 << 20;
// An envelope carries at most a 1 MiB projection plus metadata; JSON escaping can
// grow it, so allow headroom but stay bounded.
const MAX_ENVELOPE = 8 << 20;
// One page is at most 64 KiB of original bytes, base64-encoded inside JSON.
const MAX_PAGE_RESPONSE = 1 << 20;
export const PAGE_SIZE = 64 << 10;
// Calls whose result the tool_result hook has not consumed yet (bounded FIFO).
const MAX_PENDING = 256;

export interface Omission {
	[k: string]: unknown;
}

// Delivery mirrors evidence.Delivery (Go JSON).
export interface Delivery {
	delivery: string;
	tool: string;
	call_id?: string;
	status: number;
	is_error: boolean;
	content_type: string;
	reduced: boolean;
	projection: string;
	handle?: string;
	resource_uri?: string;
	expires_at?: string;
	captured_key?: string;
	raw_bytes: number;
	raw_sha256: string;
	projected_bytes: number;
	reducer: string;
	omissions?: Omission[];
	page_size?: number;
	projection_mode: string;
	captured_identity?: string;
}

// Page mirrors evidence.Page (Go JSON).
export interface Page {
	handle: string;
	tool: string;
	call_id?: string;
	content_type: string;
	offset: number;
	length: number;
	total_bytes: number;
	next_offset: number;
	eof: boolean;
	encoding: string;
	data: string;
	raw_sha256: string;
	captured_key: string;
	current_key: string;
	freshness: string;
	stale: boolean;
	expires_at: string;
}

export interface EvidenceDetails {
	path: "source" | "hook" | "passthrough" | "size_error";
	workspace_id: string;
	delivery?: Omit<Delivery, "projection">;
	projection_error?: string;
}

// PendingCall is what execute() leaves for the tool_result hook. Pi drops `details`
// when execute() throws, so errors travel here too.
type PendingCall =
	| { kind: "delivered"; workspaceId: string; delivery: Delivery }
	| { kind: "raw"; workspaceId: string; raw: Uint8Array<ArrayBuffer>; status: number; contentType: string; isError: boolean; argsDigest: string };

export class PendingCalls {
	private readonly calls = new Map<string, PendingCall>();
	set(id: string, call: PendingCall): void {
		this.calls.delete(id);
		this.calls.set(id, call);
		while (this.calls.size > MAX_PENDING) {
			const oldest = this.calls.keys().next().value as string;
			this.calls.delete(oldest);
		}
	}
	take(id: string): PendingCall | undefined {
		const c = this.calls.get(id);
		this.calls.delete(id);
		return c;
	}
	get size(): number {
		return this.calls.size;
	}
}

export interface CallMeta {
	toolCallId: string;
	sessionId?: string;
}

// renderDelivery is the model-facing text for an envelope: the projection, then —
// only when something was omitted — one `[xmustard evidence]` JSON line saying how
// to recover the rest and how fresh the capture is.
export function renderDelivery(d: Delivery, workspaceId: string): string {
	if (!d.handle && !d.reduced) return d.projection;
	const footer = {
		handle: d.handle,
		workspace_id: workspaceId,
		raw_bytes: d.raw_bytes,
		raw_sha256: d.raw_sha256,
		projected_bytes: d.projected_bytes,
		omissions: d.omissions?.length ?? 0,
		captured_identity: d.captured_identity ?? "unknown",
		captured_key: d.captured_key ?? "",
		expires_at: d.expires_at,
		page_size: d.page_size,
		expand: d.handle ? `${EXPAND_TOOL}(workspace_id, handle, offset=0)` : undefined,
	};
	return `${d.projection}\n[xmustard evidence] ${JSON.stringify(footer)}`;
}

const utf8 = new TextDecoder("utf-8");

function detailsOf(path: EvidenceDetails["path"], workspaceId: string, d?: Delivery, projectionError?: string): EvidenceDetails {
	const out: EvidenceDetails = { path, workspace_id: workspaceId };
	if (d) {
		const { projection: _omit, ...rest } = d;
		out.delivery = rest;
	}
	if (projectionError) out.projection_error = projectionError;
	return out;
}

// argsDigest reproduces the Go delivery middleware's argument digest exactly:
// SHA-256( "<METHOD> <unescaped path>?<url.Values.Encode()>\n" ‖ SHA-256(body) ), where
// Encode sorts keys and QueryEscapes keys and values. It is audit metadata supplied by
// the client on the hook path; Go does not verify it.
export function argsDigest(method: string, pathWithQuery: string, body = ""): string {
	const u = new URL(pathWithQuery, "http://xmustard.invalid");
	const groups = new Map<string, string[]>();
	for (const [k, v] of u.searchParams) groups.set(k, [...(groups.get(k) ?? []), v]);
	const query = [...groups.keys()]
		.sort()
		.flatMap((k) => (groups.get(k) ?? []).map((v) => `${goQueryEscape(k)}=${goQueryEscape(v)}`))
		.join("&");
	const bodyHash = createHash("sha256").update(body).digest();
	return createHash("sha256")
		.update(`${method} ${decodeURIComponent(u.pathname)}?${query}\n`)
		.update(bodyHash)
		.digest("hex");
}

function isDelivery(v: unknown): v is Delivery {
	const d = v as Delivery;
	return !!d && d.delivery === DELIVERY_VERSION && typeof d.projection === "string" && typeof d.is_error === "boolean";
}

function parseDelivery(res: HttpResponse, where: string): Delivery {
	let parsed: unknown;
	try {
		parsed = JSON.parse(utf8.decode(res.body));
	} catch {
		throw new XmustardHttpError("http", `xMustard ${where}: malformed evidence envelope`, res.status);
	}
	if (!isDelivery(parsed)) throw new XmustardHttpError("http", `xMustard ${where}: unexpected evidence envelope version`, res.status);
	return parsed;
}

export interface ToolRunResult {
	content: { type: "text"; text: string }[];
	details: EvidenceDetails;
}

// runTool executes one xMustard tool. It resolves with the model-facing result or
// throws (Pi then records an error result); a thrown message is always bounded.
export async function runTool(
	cfg: AdapterConfig,
	spec: ToolSpec,
	args: ToolArgs,
	meta: CallMeta,
	signal: AbortSignal | undefined,
	pending: PendingCalls,
): Promise<ToolRunResult> {
	const missing = checkRequired(spec, args);
	if (missing) throw new Error(missing);
	const workspaceId = String(args.workspace_id);
	const call = spec.build(args);
	const where = `${call.method} ${call.path.split("?")[0]}`;
	const headers: Record<string, string> = { "X-Xmustard-Issuer": "pi", "X-Xmustard-Call-Id": meta.toolCallId };
	if (meta.sessionId) headers["X-Xmustard-Session-Id"] = meta.sessionId;
	if (cfg.delivery === "source") headers[DELIVERY_HEADER] = DELIVERY_VERSION;
	const res = await send(cfg, {
		method: call.method,
		path: call.path,
		body: call.body,
		headers,
		timeoutMs: cfg.toolTimeoutMs,
		signal,
		maxBytes: cfg.delivery === "source" ? MAX_ENVELOPE : MAX_ORIGINAL,
	});
	if (res.headers.get(DELIVERY_HEADER) === DELIVERY_VERSION && res.status < 400) {
		const d = parseDelivery(res, where);
		pending.set(meta.toolCallId, { kind: "delivered", workspaceId, delivery: d });
		const text = renderDelivery(d, workspaceId);
		if (d.is_error) throw new Error(text);
		return { content: [{ type: "text", text }], details: detailsOf("source", workspaceId, d) };
	}
	if (cfg.delivery === "source" || res.status === 401 || res.status === 403) {
		// No envelope: the request failed before or during capture (auth, admission,
		// evidence size/quota) or the server predates evidence delivery.
		if (res.status >= 400) throw errorFromResponse(where, res);
	}
	// Raw result: the tool_result hook projects it through POST /evidence.
	const isError = res.status >= 400;
	pending.set(meta.toolCallId, {
		kind: "raw",
		workspaceId,
		raw: res.body,
		status: res.status,
		contentType: res.headers.get("content-type") ?? "application/json",
		isError,
		argsDigest: argsDigest(call.method, call.path, call.body),
	});
	const inline =
		res.body.byteLength <= INLINE_LIMIT
			? utf8.decode(res.body)
			: `[xmustard] ${res.body.byteLength}-byte ${spec.name} result awaiting projection`;
	if (isError) throw new Error(inline);
	return { content: [{ type: "text", text: inline }], details: detailsOf("passthrough", workspaceId) };
}

export interface ToolResultInput {
	toolCallId: string;
	toolName: string;
	isError: boolean;
	content: unknown[];
}

export interface ToolResultOutput {
	content?: { type: "text"; text: string }[];
	details?: EvidenceDetails;
	isError?: boolean;
}

// projectResult is the tool_result handler for the nine xMustard tools. Source-path
// results are already projected; raw results are posted to Go. `onHandle` fires for
// every recovery handle so the caller can activate xmustard_expand.
export async function projectResult(
	cfg: AdapterConfig,
	ev: ToolResultInput,
	pending: PendingCalls,
	meta: { sessionId?: string; signal?: AbortSignal },
	onHandle: () => void,
): Promise<ToolResultOutput | undefined> {
	const call = pending.take(ev.toolCallId);
	if (!call) return undefined; // never reached Go (validation failure, abort)
	if (call.kind === "delivered") {
		if (call.delivery.handle) onHandle();
		// Errors lost `details` when execute() threw; restore them without touching content.
		return call.delivery.is_error
			? { details: detailsOf("source", call.workspaceId, call.delivery), isError: true }
			: undefined;
	}
	const params = new URLSearchParams({
		tool: ev.toolName,
		status: String(call.status),
		is_error: String(call.isError || ev.isError),
		call_id: ev.toolCallId,
		issuer: "pi",
		tool_version: ADAPTER_VERSION,
		args_digest: call.argsDigest,
		content_type: call.contentType,
	});
	if (meta.sessionId) params.set("session_id", meta.sessionId);
	const path = `/api/workspaces/${encodeURIComponent(call.workspaceId)}/evidence?${params.toString()}`;
	try {
		const res = await send(cfg, {
			method: "POST",
			path,
			body: call.raw,
			contentType: "application/octet-stream",
			timeoutMs: cfg.projectionTimeoutMs,
			signal: meta.signal,
			maxBytes: MAX_ENVELOPE,
		});
		if (res.status >= 400) throw errorFromResponse("POST /evidence", res);
		const d = parseDelivery(res, "POST /evidence");
		if (d.handle) onHandle();
		return {
			content: [{ type: "text", text: renderDelivery(d, call.workspaceId) }],
			details: detailsOf("hook", call.workspaceId, d),
			isError: call.isError || ev.isError || d.is_error,
		};
	} catch (err) {
		const reason = err instanceof Error ? err.message : String(err);
		const isError = call.isError || ev.isError;
		if (call.raw.byteLength <= INLINE_LIMIT) {
			// Preserve the bounded original exactly; say why in details only.
			return {
				content: [{ type: "text", text: utf8.decode(call.raw) }],
				details: detailsOf("passthrough", call.workspaceId, undefined, reason),
				isError,
			};
		}
		return {
			content: [
				{
					type: "text",
					text: `[xmustard] ${ev.toolName} result is ${call.raw.byteLength} bytes, above the ${INLINE_LIMIT}-byte inline limit, and projection failed (${reason}). The original was not delivered or retained; narrow the request and retry.${isError ? " The tool itself reported an error." : ""}`,
				},
			],
			details: detailsOf("size_error", call.workspaceId, undefined, reason),
			isError: true,
		};
	}
}

export interface ExpandArgs {
	workspace_id: string;
	handle: string;
	offset?: number;
	length?: number;
}

export interface ExpandResult {
	content: { type: "text"; text: string }[];
	details: Omit<Page, "data"> & { data_base64: string; text_encoding: "utf-8" | "base64" };
}

const strictUtf8 = new TextDecoder("utf-8", { fatal: true });

// renderPage shows one page: a `[xmustard page]` JSON header (range, freshness,
// identity) then the bytes — as text when the page is valid UTF-8 on its own, else as
// standard base64 (a page boundary may split a code point; originals may be binary).
export function renderPage(page: Page): ExpandResult {
	const bytes = Buffer.from(page.data, "base64");
	let body: string;
	let encoding: "utf-8" | "base64" = "utf-8";
	try {
		body = strictUtf8.decode(bytes);
	} catch {
		body = page.data;
		encoding = "base64";
	}
	const { data, ...meta } = page;
	const header = {
		handle: page.handle,
		tool: page.tool,
		offset: page.offset,
		length: page.length,
		next_offset: page.next_offset,
		total_bytes: page.total_bytes,
		eof: page.eof,
		freshness: page.freshness,
		stale: page.stale,
		captured_key: page.captured_key,
		current_key: page.current_key,
		expires_at: page.expires_at,
		raw_sha256: page.raw_sha256,
		encoding,
	};
	return {
		content: [{ type: "text", text: `[xmustard page] ${JSON.stringify(header)}\n${body}` }],
		details: { ...meta, data_base64: data, text_encoding: encoding },
	};
}

export async function expand(cfg: AdapterConfig, args: ExpandArgs, signal: AbortSignal | undefined): Promise<ExpandResult> {
	if (!args.workspace_id?.trim() || !args.handle?.trim()) throw new Error("xmustard_expand needs workspace_id and handle");
	const params = new URLSearchParams({ offset: String(Math.max(0, Math.trunc(args.offset ?? 0))) });
	if (args.length !== undefined) params.set("length", String(Math.min(PAGE_SIZE, Math.max(1, Math.trunc(args.length)))));
	const path = `/api/workspaces/${encodeURIComponent(args.workspace_id)}/evidence/${encodeURIComponent(args.handle)}?${params}`;
	const res = await send(cfg, { method: "GET", path, timeoutMs: cfg.toolTimeoutMs, signal, maxBytes: MAX_PAGE_RESPONSE });
	if (res.status >= 400) throw errorFromResponse("GET /evidence/{handle}", res);
	let page: Page;
	try {
		page = JSON.parse(utf8.decode(res.body)) as Page;
	} catch {
		throw new Error("xMustard GET /evidence/{handle}: malformed page");
	}
	if (page.encoding !== "base64" || typeof page.data !== "string") throw new Error("xMustard GET /evidence/{handle}: unexpected page encoding");
	return renderPage(page);
}
