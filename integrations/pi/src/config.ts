// Adapter configuration, read once per extension load from the environment.
//
// Timeouts may only be LOWERED from the plan's defaults (60 s per tool execution,
// 5 s per result projection); larger or malformed values fall back to the default.

export type DeliveryMode = "source" | "hook";

export interface AdapterConfig {
	apiBase: string;
	token: string | undefined;
	delivery: DeliveryMode;
	toolTimeoutMs: number;
	projectionTimeoutMs: number;
}

export const DEFAULT_API_BASE = "http://127.0.0.1:8042";
export const DEFAULT_TOOL_TIMEOUT_MS = 60_000;
export const DEFAULT_PROJECTION_TIMEOUT_MS = 5_000;

function lowerOnly(raw: string | undefined, fallback: number): number {
	const v = Number.parseInt((raw ?? "").trim(), 10);
	return Number.isFinite(v) && v > 0 && v < fallback ? v : fallback;
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): AdapterConfig {
	const token = env.XMUSTARD_TOKEN?.trim();
	return {
		apiBase: (env.XMUSTARD_API_BASE?.trim() || DEFAULT_API_BASE).replace(/\/+$/, ""),
		token: token ? token : undefined,
		delivery: env.XMUSTARD_PI_DELIVERY?.trim() === "hook" ? "hook" : "source",
		toolTimeoutMs: lowerOnly(env.XMUSTARD_PI_TOOL_TIMEOUT_MS, DEFAULT_TOOL_TIMEOUT_MS),
		projectionTimeoutMs: lowerOnly(env.XMUSTARD_PI_PROJECTION_TIMEOUT_MS, DEFAULT_PROJECTION_TIMEOUT_MS),
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
