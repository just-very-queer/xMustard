// The version-neutral core: decide whether a finished tool call is reduced, capture
// it, and hand back the text that replaces the model-visible result. Every failure
// leaves OpenCode's own result in place (fail open); nothing here throws.

import type { Capturer, Observation, ToolOutcome } from "./capture.ts";
import { MIN_CAPTURE_TARGET, POLICY_TARGET, type PluginConfig } from "./config.ts";

// EvidenceRecord is what a replaced result's metadata.xmustard (2.x) records.
export interface EvidenceRecord {
	handle: string;
	workspace_id: string;
	resource_uri?: string;
	raw_bytes: number;
	raw_sha256: string;
	projected_bytes: number;
	reducer: string;
	expires_at?: string;
	captured_identity: string;
}

export interface Replacement {
	text: string;
	record: EvidenceRecord;
}

// OpenCode 1.x built-ins whose names hold an underscore; any other underscored name is
// an MCP tool, named <server>_<tool> (1.18.33 mcp/catalog.ts, 2.0.1 tool registry).
const UNDERSCORED_BUILTINS = new Set(["apply_patch", "plan_enter", "plan_exit"]);

// sanitize is OpenCode's tool-name part (non [A-Za-z0-9_-] becomes "_").
export const sanitize = (s: string): string => s.replace(/[^a-zA-Z0-9_-]/g, "_");

// A handle expiring within this margin is treated as gone: Go retains originals for
// about a day, and a stub must not name a handle that dies before the model uses it.
export const EXPIRY_MARGIN_MS = 60 * 60_000;
const MAX_HANDLES = 1024;

export function isLive(expiresAt: string | undefined, now: number): boolean {
	if (expiresAt === undefined) return true;
	const t = Date.parse(expiresAt);
	return !Number.isNaN(t) && t - now > EXPIRY_MARGIN_MS;
}

export class Reducer {
	readonly cfg: PluginConfig;
	readonly capturer: Capturer;
	private readonly workspace: () => Promise<string>;
	private readonly toolVersion: string;
	private readonly ownPrefix: string;
	private readonly now: () => number;
	// the handle of each call captured so far, oldest first (bounded)
	private readonly handles = new Map<string, EvidenceRecord>();

	constructor(cfg: PluginConfig, capturer: Capturer, workspace: () => Promise<string>, toolVersion: string, now: () => number = Date.now) {
		this.cfg = cfg;
		this.capturer = capturer;
		this.workspace = workspace;
		this.toolVersion = toolVersion;
		this.ownPrefix = `${sanitize(cfg.serverName)}_`;
		this.now = now;
	}

	// ownTool reports one of xMustard's own nine tools: their results are bounded and
	// carry a handle at the source already.
	ownTool(tool: string): boolean {
		return tool.startsWith(this.ownPrefix);
	}

	foreignMcpTool(tool: string): boolean {
		return tool.includes("_") && !this.ownTool(tool) && !UNDERSCORED_BUILTINS.has(tool);
	}

	reducible(tool: string): boolean {
		return this.cfg.tools.has(tool) || (this.cfg.foreignMcp && this.foreignMcpTool(tool));
	}

	// maskable: what may be masked later (the reducible tools and xMustard's own).
	maskable(tool: string): boolean {
		return this.reducible(tool) || this.ownTool(tool);
	}

	// handle returns the live handle a call was captured under, if any.
	handle(callID: string): EvidenceRecord | undefined {
		const rec = this.handles.get(callID);
		return rec && isLive(rec.expires_at, this.now()) ? rec : undefined;
	}

	// reduce captures a large result and returns its replacement, or undefined when
	// OpenCode's result stays: a tool that is not reduced, non-text content, a result at
	// or below the projection target (Go would return it unchanged), capture paused or
	// failing, nothing omitted, or no payload Go could shape.
	async reduce(o: ToolOutcome): Promise<Replacement | undefined> {
		const body = o.error ?? o.text;
		if (body === undefined || !this.reducible(o.tool) || Buffer.byteLength(body, "utf8") <= this.cfg.projectionTarget) return undefined;
		const target = this.cfg.projectionTarget < POLICY_TARGET ? this.cfg.projectionTarget : undefined;
		const captured = await this.capture(o, target);
		const content = captured?.obs.shape?.mode === "replace" ? captured.obs.shape.payload?.content : undefined;
		if (!captured || typeof content !== "string") return undefined;
		return { text: content, record: captured.record };
	}

	// retain captures a result at the smallest target so it gets a handle (a result
	// about to be masked); the model keeps seeing OpenCode's text until then.
	async retain(o: ToolOutcome): Promise<EvidenceRecord | undefined> {
		return (await this.capture(o, MIN_CAPTURE_TARGET))?.record;
	}

	private async capture(o: ToolOutcome, target: number | undefined): Promise<{ obs: Observation; record: EvidenceRecord } | undefined> {
		if (this.capturer.paused) return undefined;
		try {
			const ws = await this.workspace();
			const obs = await this.capturer.observe(ws, o, { toolVersion: this.toolVersion, target });
			if (!obs.reduced || !obs.handle) return undefined;
			// no undefined member: OpenCode 2.0.1 drops a tool result whose metadata holds
			// one ("Tool result missing") and runs the step again
			const record: EvidenceRecord = {
				handle: obs.handle,
				workspace_id: ws,
				...(obs.resource_uri ? { resource_uri: obs.resource_uri } : {}),
				raw_bytes: obs.raw_bytes,
				raw_sha256: obs.raw_sha256,
				projected_bytes: obs.projected_bytes,
				reducer: obs.reducer,
				...(obs.expires_at ? { expires_at: obs.expires_at } : {}),
				captured_identity: obs.captured_identity ?? "unknown",
			};
			this.remember(o.callID, record);
			return { obs, record };
		} catch {
			return undefined; // the capturer paused itself on an outage
		}
	}

	private remember(callID: string, rec: EvidenceRecord): void {
		this.handles.delete(callID);
		this.handles.set(callID, rec);
		for (const oldest of this.handles.keys()) {
			if (this.handles.size <= MAX_HANDLES) break;
			this.handles.delete(oldest);
		}
	}
}
