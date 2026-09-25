// Adapter configuration, read once per extension load from the environment.
//
// Timeouts may only be LOWERED from the plan's defaults (60 s per tool execution,
// 5 s per result projection); larger or malformed values fall back to the default.
// The built-in projection target may likewise only be lowered from Go's Pi policy.

import { BUILTIN_TOOLS } from "./tools.ts";

export type DeliveryMode = "source" | "hook";

export interface MaskConfig {
	enabled: boolean; // XMUSTARD_PI_MASK=off disables turn_end masking
	afterTurns: number; // a result older than this many turns may be masked
	everyTurns: number; // the mask advances only every this many turns (polling window)
	minBytes: number; // smaller results are never masked
}

export interface AdapterConfig {
	apiBase: string;
	token: string | undefined;
	workspaceId?: string; // XMUSTARD_WORKSPACE_ID: the workspace for calls that omit one
	delivery: DeliveryMode;
	toolTimeoutMs: number;
	projectionTimeoutMs: number;
	// Pi built-ins whose results are projected through POST .../evidence/capture
	// (XMUSTARD_PI_BUILTINS: comma-separated subset, or "none").
	builtins: ReadonlySet<string>;
	// Built-in results at or below this many bytes pass through unchanged (Go would
	// return them unchanged too); larger ones are captured and projected to about it.
	projectionTarget: number;
	mask: MaskConfig;
	compaction: boolean; // XMUSTARD_PI_COMPACTION=off keeps Pi's own compaction
}

export const DEFAULT_API_BASE = "http://127.0.0.1:8042";
export const DEFAULT_TOOL_TIMEOUT_MS = 60_000;
export const DEFAULT_PROJECTION_TIMEOUT_MS = 5_000;
// Go's projection target for the pi client (api-go/internal/evidence/shapes.go
// clientPolicies["pi"].Target), and the smallest target a capture may ask for
// (evidence.MinCaptureTarget).
export const PI_POLICY_TARGET = 32 << 10;
export const MIN_CAPTURE_TARGET = 1 << 10;
export const DEFAULT_MASK: MaskConfig = { enabled: true, afterTurns: 10, everyTurns: 5, minBytes: 2 << 10 };

function lowerOnly(raw: string | undefined, fallback: number, min = 1): number {
	const v = Number.parseInt((raw ?? "").trim(), 10);
	return Number.isFinite(v) && v >= min && v < fallback ? v : fallback;
}

function bounded(raw: string | undefined, fallback: number, min: number, max: number): number {
	const s = (raw ?? "").trim();
	if (!/^\d+$/.test(s)) return fallback;
	const v = Number.parseInt(s, 10);
	return v >= min && v <= max ? v : fallback;
}

const off = (v: string | undefined): boolean => ["off", "0", "false", "no"].includes((v ?? "").trim().toLowerCase());

function builtinSet(raw: string | undefined): ReadonlySet<string> {
	const v = (raw ?? "").trim().toLowerCase();
	if (v === "") return new Set(BUILTIN_TOOLS);
	if (v === "none" || off(v)) return new Set();
	const known = new Set<string>(BUILTIN_TOOLS);
	return new Set(
		v
			.split(",")
			.map((x) => x.trim())
			.filter((x) => known.has(x)),
	);
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): AdapterConfig {
	const token = env.XMUSTARD_TOKEN?.trim();
	const workspaceId = env.XMUSTARD_WORKSPACE_ID?.trim();
	return {
		apiBase: (env.XMUSTARD_API_BASE?.trim() || DEFAULT_API_BASE).replace(/\/+$/, ""),
		token: token ? token : undefined,
		workspaceId: workspaceId ? workspaceId : undefined,
		delivery: env.XMUSTARD_PI_DELIVERY?.trim() === "hook" ? "hook" : "source",
		toolTimeoutMs: lowerOnly(env.XMUSTARD_PI_TOOL_TIMEOUT_MS, DEFAULT_TOOL_TIMEOUT_MS),
		projectionTimeoutMs: lowerOnly(env.XMUSTARD_PI_PROJECTION_TIMEOUT_MS, DEFAULT_PROJECTION_TIMEOUT_MS),
		builtins: builtinSet(env.XMUSTARD_PI_BUILTINS),
		projectionTarget: lowerOnly(env.XMUSTARD_PI_PROJECTION_TARGET_BYTES, PI_POLICY_TARGET, MIN_CAPTURE_TARGET),
		mask: {
			enabled: !off(env.XMUSTARD_PI_MASK),
			afterTurns: bounded(env.XMUSTARD_PI_MASK_AFTER_TURNS, DEFAULT_MASK.afterTurns, 1, 10_000),
			everyTurns: bounded(env.XMUSTARD_PI_MASK_EVERY_TURNS, DEFAULT_MASK.everyTurns, 1, 10_000),
			// a masked result must be retained behind a handle, which needs more than
			// the smallest capture target
			minBytes: bounded(env.XMUSTARD_PI_MASK_MIN_BYTES, DEFAULT_MASK.minBytes, MIN_CAPTURE_TARGET + 1, 1 << 20),
		},
		compaction: !off(env.XMUSTARD_PI_COMPACTION),
	};
}

// displayBase strips any userinfo so an error message never echoes a credential.
export function displayBase(apiBase: string): string {
	try {
		const u = new URL(apiBase);
		u.username = "";
		u.password = "";
		return u.toString().replace(/\/+$/, "");
	} catch {
		return "<invalid XMUSTARD_API_BASE>";
	}
}
