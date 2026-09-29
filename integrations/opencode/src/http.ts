// Bounded HTTP to the xMustard Go API. Every call has its own deadline and reads the
// response incrementally, refusing it past a byte cap. The bearer token travels as a
// header only and never appears in an error message.

import { displayBase, type PluginConfig } from "./config.ts";

export type HttpFailure = "unreachable" | "timeout" | "too_large" | "http";

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
	body?: string;
	contentType?: string;
	timeoutMs: number;
	maxBytes: number;
}

export interface HttpResponse {
	status: number;
	body: Uint8Array;
}

type Connection = Pick<PluginConfig, "apiBase" | "token">;

export async function send(cfg: Connection, req: HttpRequest): Promise<HttpResponse> {
	const signal = AbortSignal.timeout(req.timeoutMs);
	const headers: Record<string, string> = {};
	if (req.body !== undefined) headers["Content-Type"] = req.contentType ?? "application/json";
	if (cfg.token) headers.Authorization = `Bearer ${cfg.token}`;
	const where = `${req.method} ${req.path.split("?")[0]}`;
	const failed = (err: unknown): XmustardHttpError =>
		err instanceof XmustardHttpError
			? err
			: signal.aborted
				? new XmustardHttpError("timeout", `xMustard ${where}: timed out after ${req.timeoutMs} ms`)
				: new XmustardHttpError("unreachable", `xMustard API unreachable at ${displayBase(cfg.apiBase)} (${where})`);
	try {
		const resp = await fetch(cfg.apiBase + req.path, { method: req.method, headers, body: req.body, signal });
		return { status: resp.status, body: await readBounded(resp, req.maxBytes, where) };
	} catch (err) {
		throw failed(err);
	}
}

async function readBounded(resp: Response, maxBytes: number, where: string): Promise<Uint8Array> {
	const tooLarge = () => new XmustardHttpError("too_large", `xMustard ${where}: response exceeds ${maxBytes} bytes`, resp.status, "too_large");
	if (Number(resp.headers.get("content-length") ?? "NaN") > maxBytes) {
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
	return Buffer.concat(chunks);
}

// errorFromResponse turns a >= 400 answer into an explicit error, quoting at most
// `limit` bytes of the server's message.
export function errorFromResponse(where: string, res: HttpResponse, limit = 2048): XmustardHttpError {
	const raw = new TextDecoder().decode(res.body.subarray(0, limit));
	let reason: string | undefined;
	let message = raw.trim();
	try {
		const parsed = JSON.parse(raw) as { error?: unknown; reason?: unknown };
		if (typeof parsed.reason === "string") reason = parsed.reason;
		if (typeof parsed.error === "string") message = parsed.error;
	} catch {
		// non-JSON body: quote the bounded text
	}
	return new XmustardHttpError("http", `xMustard ${where} -> ${res.status}${reason ? ` ${reason}` : ""}: ${message}`, res.status, reason);
}

// getJSON fetches and decodes a JSON answer, refusing a non-200 one.
export async function getJSON(cfg: Connection, path: string, timeoutMs: number, maxBytes: number): Promise<unknown> {
	const res = await send(cfg, { method: "GET", path, timeoutMs, maxBytes });
	if (res.status !== 200) throw errorFromResponse(`GET ${path.split("?")[0]}`, res);
	return JSON.parse(new TextDecoder().decode(res.body)) as unknown;
}
