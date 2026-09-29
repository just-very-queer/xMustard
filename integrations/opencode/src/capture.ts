// xMustard's universal capture route (POST /api/workspaces/{ws}/evidence/capture,
// format=opencode) for one finished OpenCode tool call.
//
// Both plugin APIs normalize a call into a ToolOutcome and post it as one flat body
// (the contract api-go/internal/evidence/hookbody.go reads for format opencode):
//
//   {"tool", "sessionID", "id", "input", "content": <model-visible text>,
//    "metadata": {<number and boolean members>}}  or  {..., "error": <message>}
//
// Only the text the model reads is posted. OpenCode 2.x's structured result.output
// (a shell call's {output, exit, truncated}, a grep's match list) and 1.x's
// metadata.output repeat that text and would be captured twice, so they never are.

import { MIN_CAPTURE_TARGET, type PluginConfig } from "./config.ts";
import { errorFromResponse, send, XmustardHttpError } from "./http.ts";

export interface ToolOutcome {
	tool: string;
	sessionID: string;
	callID: string;
	input: unknown;
	text?: string; // a completed call's model-visible text
	error?: string; // a failed call's message
	metadata?: Record<string, number | boolean>;
}

export function captureBody(o: ToolOutcome): string {
	const body: Record<string, unknown> = { tool: o.tool, sessionID: o.sessionID, id: o.callID, input: o.input ?? {} };
	if (o.error !== undefined) body.error = o.error;
	else body.content = o.text ?? "";
	if (o.metadata && Object.keys(o.metadata).length > 0) body.metadata = o.metadata;
	return JSON.stringify(body);
}

// scalarMetadata keeps a result's number and boolean members (exit code, truncated,
// timeout): what xMustard reads from metadata. Strings, objects and arrays are left
// out; 1.x's metadata.output is a copy of the output.
export function scalarMetadata(meta: unknown): Record<string, number | boolean> | undefined {
	if (!meta || typeof meta !== "object" || Array.isArray(meta)) return undefined;
	const out: Record<string, number | boolean> = {};
	for (const [k, v] of Object.entries(meta)) {
		if ((typeof v === "number" && Number.isFinite(v)) || typeof v === "boolean") out[k] = v;
	}
	return Object.keys(out).length > 0 ? out : undefined;
}

// textOf returns the text of tool content: a string as is, text blocks joined by
// sep; undefined when the content holds anything but text (an image or a file is
// never captured).
export function textOf(content: unknown, sep = "\n"): string | undefined {
	if (typeof content === "string") return content;
	if (!Array.isArray(content) || content.length === 0) return undefined;
	const texts: string[] = [];
	for (const b of content) {
		const block = b as { type?: unknown; text?: unknown };
		if (block?.type !== "text" || typeof block.text !== "string") return undefined;
		texts.push(block.text);
	}
	return texts.join(sep);
}

// Observation mirrors the fields of evidence.ObservationResult (Go JSON) the plugin reads.
export interface Observation {
	reduced: boolean;
	handle?: string;
	resource_uri?: string;
	expires_at?: string;
	raw_bytes: number;
	raw_sha256: string;
	projected_bytes: number;
	reducer: string;
	captured_identity?: string;
	shape?: { client: string; shape: string; mode: string; reason?: string; payload?: { content?: unknown } };
}

const MAX_CAPTURE_RESPONSE = 4 << 20;
// After Go says capture is unavailable (no redactor wired, unreachable, hung, 401 or
// 403), the plugin stops asking for this long, so tool results are not delayed each time.
export const CAPTURE_PAUSE_MS = 30_000;

export interface CaptureOptions {
	toolVersion: string;
	target?: number; // lower the opencode projection target for this capture
}

export class Capturer {
	private readonly cfg: PluginConfig;
	private readonly now: () => number;
	private pausedUntil = 0;
	private pauseReason = "";
	constructor(cfg: PluginConfig, now: () => number = Date.now) {
		this.cfg = cfg;
		this.now = now;
	}

	// paused names why capture is currently skipped, or is undefined.
	get paused(): string | undefined {
		return this.now() < this.pausedUntil ? this.pauseReason : undefined;
	}

	async observe(workspaceId: string, o: ToolOutcome, opts: CaptureOptions): Promise<Observation> {
		const paused = this.paused;
		if (paused) throw new Error(`capture paused: ${paused}`);
		const params = new URLSearchParams({
			format: "opencode",
			client: "opencode",
			tool: o.tool,
			tool_version: opts.toolVersion,
			call_id: o.callID,
			session_id: o.sessionID,
			is_error: String(o.error !== undefined),
		});
		if (opts.target !== undefined) params.set("target", String(Math.max(MIN_CAPTURE_TARGET, Math.trunc(opts.target))));
		const where = "POST /evidence/capture";
		try {
			const res = await send(this.cfg, {
				method: "POST",
				path: `/api/workspaces/${encodeURIComponent(workspaceId)}/evidence/capture?${params}`,
				body: captureBody(o),
				timeoutMs: this.cfg.captureTimeoutMs,
				maxBytes: MAX_CAPTURE_RESPONSE,
			});
			if (res.status >= 400) throw errorFromResponse(where, res);
			const parsed = JSON.parse(new TextDecoder().decode(res.body)) as Observation;
			if (typeof parsed?.reduced !== "boolean") throw new XmustardHttpError("http", `xMustard ${where}: unexpected capture envelope`, res.status);
			return parsed;
		} catch (err) {
			this.noteFailure(err);
			throw err;
		}
	}

	// noteFailure pauses capture after an outage or a standing refusal: Go unreachable
	// or hung, capture refused because no redactor is wired, or this principal may not
	// capture (401/403).
	noteFailure(err: unknown): void {
		if (!(err instanceof XmustardHttpError)) return;
		const outage =
			err.failure === "unreachable" ||
			err.failure === "timeout" ||
			err.status === 401 ||
			err.status === 403 ||
			(err.status === 503 && err.reason === "redaction_unavailable");
		if (!outage) return;
		this.pausedUntil = this.now() + CAPTURE_PAUSE_MS;
		this.pauseReason = err.message;
	}
}
