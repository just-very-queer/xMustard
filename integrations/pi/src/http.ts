// Bounded, cancellable HTTP to the xMustard Go API.
//
// Every call has its own deadline and forwards the caller's AbortSignal, so an
// aborted Pi turn closes the connection and the Go handler's request context (and
// any Rust child under it) is cancelled. Response bodies are read incrementally and
// refused past a byte cap instead of being buffered without limit. The bearer token
// is sent as a header only; it never appears in an error message.

import { type AdapterConfig, displayBase } from "./config.ts";

export type HttpFailure = "unreachable" | "timeout" | "aborted" | "too_large" | "http";

export class XmustardHttpError extends Error {
	readonly failure: HttpFailure;
	readonly status: number | undefined;
	readonly reason: string | undefined;
	constructor(failure: HttpFailure, message: string, status?: number, reason?: string) {
		super(message);
		this.name = "XmustardHttpError";
		this.failure = failure;
		this.status = status;
		this.reason = reason;
	}
}

export interface HttpRequest {
	method: "GET" | "POST";
	path: string;
	body?: Uint8Array<ArrayBuffer> | string;
	contentType?: string;
	headers?: Record<string, string>;
	timeoutMs: number;
	signal?: AbortSignal;
	maxBytes: number;
}

export interface HttpResponse {
	status: number;
	headers: Headers;
	body: Uint8Array<ArrayBuffer>;
}

export async function send(cfg: AdapterConfig, req: HttpRequest): Promise<HttpResponse> {
	const timeout = AbortSignal.timeout(req.timeoutMs);
	const signal = req.signal ? AbortSignal.any([req.signal, timeout]) : timeout;
	const headers: Record<string, string> = { ...req.headers };
	if (req.body !== undefined) headers["Content-Type"] = req.contentType ?? "application/json";
	if (cfg.token) headers.Authorization = `Bearer ${cfg.token}`;
	const where = `${req.method} ${req.path.split("?")[0]}`;
	const failed = (err: unknown): XmustardHttpError => {
		if (err instanceof XmustardHttpError) return err;
		if (req.signal?.aborted) return new XmustardHttpError("aborted", `xMustard ${where}: aborted by caller`);
		if (timeout.aborted) return new XmustardHttpError("timeout", `xMustard ${where}: timed out after ${req.timeoutMs} ms`);
		return new XmustardHttpError("unreachable", `xMustard API unreachable at ${displayBase(cfg.apiBase)} (${where})`);
	};
	let resp: Response;
	try {
		resp = await fetch(cfg.apiBase + req.path, { method: req.method, headers, body: req.body, signal });
	} catch (err) {
		throw failed(err);
	}
	try {
		const body = await readBounded(resp, req.maxBytes, where);
		return { status: resp.status, headers: resp.headers, body };
	} catch (err) {
		throw failed(err);
	}
}

async function readBounded(resp: Response, maxBytes: number, where: string): Promise<Uint8Array<ArrayBuffer>> {
	const tooLarge = () =>
		new XmustardHttpError("too_large", `xMustard ${where}: response exceeds ${maxBytes} bytes; narrow the request`, resp.status, "too_large");
	const declared = Number(resp.headers.get("content-length") ?? "NaN");
	if (declared > maxBytes) {
		await resp.body?.cancel().catch(() => {});
		throw tooLarge();
	}
	if (!resp.body) return new Uint8Array();
	const reader = resp.body.getReader();
	const chunks: Uint8Array[] = [];
	let total = 0;
	for (;;) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > maxBytes) {
			await reader.cancel().catch(() => {});
			throw tooLarge();
		}
		chunks.push(value);
	}
	const out = new Uint8Array(total);
	let at = 0;
	for (const c of chunks) {
		out.set(c, at);
		at += c.byteLength;
	}
	return out;
}

// errorFromResponse turns a >= 400 answer into an explicit error, quoting at most
// `limit` bytes of the server's message.
export function errorFromResponse(where: string, res: HttpResponse, limit = 4096): XmustardHttpError {
	const text = new TextDecoder().decode(res.body.subarray(0, limit));
	let reason: string | undefined;
	let message = text.trim();
	try {
		const parsed = JSON.parse(text) as { error?: unknown; reason?: unknown };
		if (typeof parsed.reason === "string") reason = parsed.reason;
		if (typeof parsed.error === "string") message = parsed.error;
	} catch {
		// non-JSON body: quote the bounded text
	}
	const suffix = res.body.byteLength > limit ? " …(truncated)" : "";
	return new XmustardHttpError(
		"http",
		`xMustard ${where} -> ${res.status}${reason ? ` ${reason}` : ""}: ${message}${suffix}`,
		res.status,
		reason,
	);
}
