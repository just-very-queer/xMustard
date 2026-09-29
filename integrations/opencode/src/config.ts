// Plugin configuration, read once per plugin load: the OpenCode 2.x plugin entry's
// options first (`plugins: [{ package, options }]` in opencode.json), then the
// environment. The token and the API base it is sent to are read from the environment
// only: a token never sits in a config file, and a repository's opencode.json cannot
// point it at another host. Budgets and timeouts may only be lowered from their
// defaults; a larger or malformed value keeps the default.

export const DEFAULT_API_BASE = "http://127.0.0.1:8042";
export const DEFAULT_SERVER_NAME = "xmustard";
// Go's projection target for the opencode client
// (api-go/internal/evidence/shapes.go clientPolicies["opencode"].Target), and the
// smallest target a capture may ask for (evidence.MinCaptureTarget).
export const POLICY_TARGET = 32 << 10;
export const MIN_CAPTURE_TARGET = 1 << 10;
export const DEFAULT_CAPTURE_TIMEOUT_MS = 5_000;
// Plugin setup waits at most this long for the workspace lookup that binds the MCP entry.
export const SETUP_LOOKUP_MS = 2_000;

// Tools whose results are reduced, by both plugin APIs' names: OpenCode 2.x shell,
// grep, glob, webfetch and the code-mode `execute`; 1.x bash and list. read is left
// alone: its lines are numbered already and it is paged (Go only observes it), and
// edits return short summaries.
export const DEFAULT_TOOLS: readonly string[] = ["shell", "bash", "grep", "glob", "list", "webfetch", "execute"];

export interface MaskConfig {
	enabled: boolean; // XMUSTARD_OPENCODE_MASK=off or options.mask=false turns masking off
	afterTurns: number; // a result older than this many assistant turns may be masked
	everyTurns: number; // the mask advances only every this many turns (a polling window)
	minBytes: number; // smaller results are never masked
}

export const DEFAULT_MASK: MaskConfig = { enabled: true, afterTurns: 10, everyTurns: 5, minBytes: 2 << 10 };

export interface PluginConfig {
	apiBase: string;
	token?: string;
	workspaceId?: string;
	serverName: string; // the MCP server entry's name, and so its tools' prefix (<name>_<tool>)
	registerMcp: boolean; // add the MCP server entry unless one of that name exists
	tools: ReadonlySet<string>;
	foreignMcp: boolean; // also reduce other MCP servers' tools called directly
	projectionTarget: number; // results at or below this many bytes pass through
	captureTimeoutMs: number;
	mask: MaskConfig;
}

type Source = Readonly<Record<string, unknown>>;

const OFF = new Set(["off", "0", "false", "no", "none"]);

const text = (v: unknown): string | undefined => {
	const s = typeof v === "number" || typeof v === "boolean" ? String(v) : typeof v === "string" ? v.trim() : undefined;
	return s ? s : undefined;
};

const isOff = (v: unknown): boolean => v === false || OFF.has((text(v) ?? "").toLowerCase());

// lowerOnly keeps an integer within [min, fallback), else the fallback.
function lowerOnly(v: unknown, fallback: number, min = 1): number {
	const s = text(v);
	if (!s || !/^\d+$/.test(s)) return fallback;
	const n = Number.parseInt(s, 10);
	return n >= min && n < fallback ? n : fallback;
}

// bounded keeps an integer within [min, max], else the fallback.
function bounded(v: unknown, fallback: number, min: number, max: number): number {
	const s = text(v);
	if (!s || !/^\d+$/.test(s)) return fallback;
	const n = Number.parseInt(s, 10);
	return n >= min && n <= max ? n : fallback;
}

// toolSet reads a tool list: an array or a comma-separated string, "none" for no
// tools, unset for the defaults.
function toolSet(v: unknown): ReadonlySet<string> {
	if (v === undefined || v === null || v === "") return new Set(DEFAULT_TOOLS);
	if (isOff(v)) return new Set();
	const items = Array.isArray(v) ? v : String(v).split(",");
	return new Set(items.map((x) => String(x).trim()).filter(Boolean));
}

export function loadConfig(options: Source = {}, env: NodeJS.ProcessEnv = process.env): PluginConfig {
	// an option wins over its environment variable
	const pick = (option: string, variable: string): unknown => options[option] ?? env[variable];
	const maskOption = options.mask;
	const mask: Source = maskOption && typeof maskOption === "object" ? (maskOption as Source) : {};
	return {
		apiBase: (text(env.XMUSTARD_API_BASE) ?? DEFAULT_API_BASE).replace(/\/+$/, ""),
		token: text(env.XMUSTARD_API_TOKEN) ?? text(env.XMUSTARD_TOKEN),
		workspaceId: text(pick("workspaceId", "XMUSTARD_WORKSPACE_ID")),
		serverName: text(options.server) ?? DEFAULT_SERVER_NAME,
		registerMcp: !isOff(pick("mcp", "XMUSTARD_OPENCODE_MCP")),
		tools: toolSet(pick("tools", "XMUSTARD_OPENCODE_TOOLS")),
		foreignMcp: !isOff(pick("foreignMcp", "XMUSTARD_OPENCODE_FOREIGN_MCP")),
		projectionTarget: lowerOnly(pick("projectionTarget", "XMUSTARD_OPENCODE_PROJECTION_TARGET_BYTES"), POLICY_TARGET, MIN_CAPTURE_TARGET),
		captureTimeoutMs: lowerOnly(pick("captureTimeoutMs", "XMUSTARD_OPENCODE_CAPTURE_TIMEOUT_MS"), DEFAULT_CAPTURE_TIMEOUT_MS),
		mask: {
			// any "off" turns masking off: the option (false, or enabled: false) or the variable
			enabled: !(maskOption === false || mask.enabled === false || isOff(env.XMUSTARD_OPENCODE_MASK)),
			afterTurns: bounded(mask.afterTurns ?? env.XMUSTARD_OPENCODE_MASK_AFTER_TURNS, DEFAULT_MASK.afterTurns, 1, 10_000),
			everyTurns: bounded(mask.everyTurns ?? env.XMUSTARD_OPENCODE_MASK_EVERY_TURNS, DEFAULT_MASK.everyTurns, 1, 10_000),
			// a masked result is retained behind a handle, which needs more than the
			// smallest capture target
			minBytes: bounded(mask.minBytes ?? env.XMUSTARD_OPENCODE_MASK_MIN_BYTES, DEFAULT_MASK.minBytes, MIN_CAPTURE_TARGET + 1, 1 << 20),
		},
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
