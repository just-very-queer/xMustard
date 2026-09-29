// Live smoke against the installed OpenCode 2.x (run: node test/smoke/smoke.mjs).
//
// No paid model runs. A local fake server plays both sides OpenCode talks to: the
// xMustard API (GET /api/workspaces, POST /mcp, POST .../evidence/capture) and an
// OpenAI-compatible model (POST /v1/chat/completions) that follows a script of shell
// calls and records what OpenCode sends back. OpenCode runs in a throwaway HOME/XDG tree
// with `opencode run --standalone --auto`, so the owner's config, sessions and
// background service are untouched.
//
// Stage A loads the plugin from this package directory and checks that
//   1. setup resolved the workspace and registered the MCP entry (OpenCode opened an MCP
//      session with the workspace binding and the token),
//   2. the shell call's execute.after posted the capture body (format opencode, model
//      text only, scalar metadata), and
//   3. the model's next request carries the replacement text in place of the output.
// Stage B loads the `mcp` object `xmustard-ops mcp-config --client opencode` prints
// (OPS_BIN=/path/to/xmustard-ops, or the same form built here) and checks that OpenCode
// connects with {env:XMUSTARD_API_TOKEN} substituted.
// Stage C masks: with afterTurns 1, the first result (below the projection target, so
// not reduced) is retained at mask time and the model's third request carries its stub.
//
// OPENCODE_BIN overrides the binary (default: opencode2 on PATH); KEEP=1 keeps the tree
// and a seen.json of what the fake saw (whole model requests included);
// DEBUG=1 adds OpenCode's JSON events and debug log to each project's opencode.log.

import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const pluginDir = path.resolve(here, "../..");
const OPENCODE = process.env.OPENCODE_BIN ?? "opencode2";
const TOKEN = "smoke-token";
const root = mkdtempSync(path.join(process.env.SMOKE_DIR ?? tmpdir(), "xm-opencode-smoke-"));

// what the fake server saw in the current stage, and the shell calls its model makes
let seen;
let script = [];
const stage = (commands) => {
	seen = { workspaces: [], mcp: [], captures: [], model: [] };
	script = commands;
};
const replacement = (handle) => `[xmustard shell] projected\n[xmustard evidence] {"handle":"${handle}"}`;

// ---- the fake xMustard API and model ----------------------------------------------

const json = (res, status, value, headers = {}) => {
	res.writeHead(status, { "content-type": "application/json", ...headers });
	res.end(JSON.stringify(value));
};

function sse(res, chunks) {
	res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
	for (const c of chunks) res.write(`data: ${JSON.stringify(c)}\n\n`);
	res.end("data: [DONE]\n\n");
}

const chunk = (delta, finish = null) => ({ id: "chatcmpl-smoke", object: "chat.completion.chunk", created: 0, model: "m", choices: [{ index: 0, delta, finish_reason: finish }] });
const usage = { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 };
const toolText = (m) => (typeof m.content === "string" ? m.content : (m.content ?? []).map((p) => p.text ?? "").join(""));

// answerModel calls the script's next command after each tool result, then answers "done".
function answerModel(res, req) {
	const results = (req.messages ?? []).filter((m) => m.role === "tool");
	seen.model.push({ tools: (req.tools ?? []).map((t) => t.function?.name), results: results.map((m) => ({ id: m.tool_call_id, text: toolText(m) })), raw: process.env.KEEP ? req : undefined });
	const next = req.tools?.length ? script[results.length] : undefined;
	if (next === undefined) return sse(res, [chunk({ role: "assistant", content: req.tools?.length ? "done" : "Smoke" }), { ...chunk({}, "stop"), usage }]);
	const call = { index: 0, id: `call_smoke_${results.length}`, type: "function", function: { name: "shell", arguments: JSON.stringify({ command: next }) } };
	return sse(res, [chunk({ role: "assistant", tool_calls: [call] }), { ...chunk({}, "tool_calls"), usage }]);
}

function answerMCP(res, req, body) {
	const url = new URL(req.url, "http://x");
	if (req.method !== "POST") return json(res, 405, {});
	const m = JSON.parse(body || "{}");
	seen.mcp.push({ method: m.method, query: url.search, auth: req.headers.authorization, workspace: req.headers["x-xmustard-workspace"] });
	if (m.method === "initialize") {
		// what xMustard's NegotiateVersion answers a newer client (2.0.1 asks for 2025-11-25)
		seen.mcp.at(-1).requested = m.params?.protocolVersion;
		const result = { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "fake-xmustard", version: "0" } };
		return json(res, 200, { jsonrpc: "2.0", id: m.id, result }, { "mcp-session-id": "smoke" });
	}
	if (m.id === undefined) return res.writeHead(202).end();
	if (m.method === "tools/list") return json(res, 200, { jsonrpc: "2.0", id: m.id, result: { tools: [{ name: "ground", description: "fake", inputSchema: { type: "object", properties: {} } }] } });
	return json(res, 200, { jsonrpc: "2.0", id: m.id, result: {} });
}

// answerCapture reduces every capture; the handle names the call.
function answerCapture(res, req, body) {
	const url = new URL(req.url, "http://x");
	seen.captures.push({ query: Object.fromEntries(url.searchParams), body: JSON.parse(body) });
	const handle = `xm1.${url.searchParams.get("call_id")}`;
	json(res, 200, {
		delivery: "xmustard.evidence/v1", reduced: true, handle, raw_bytes: body.length, raw_sha256: "00", projected_bytes: 64,
		reducer: "xm-shell/1", captured_identity: "unknown", resource_uri: `xmustard://evidence/${handle}?workspace_id=ws-smoke`,
		shape: { client: "opencode", shape: "opencode.content", mode: "replace", payload: { content: replacement(handle) } },
	});
}

const server = createServer(async (req, res) => {
	let body = "";
	for await (const c of req) body += c;
	const p = new URL(req.url, "http://x").pathname;
	if (p === "/api/workspaces") {
		seen.workspaces.push(req.headers.authorization);
		return json(res, 200, ["a", "c"].map((d) => ({ workspace_id: "ws-smoke", root_path: path.join(root, d) })));
	}
	if (p === "/mcp") return answerMCP(res, req, body);
	if (/^\/api\/workspaces\/[^/]+\/evidence\/capture$/.test(p)) return answerCapture(res, req, body);
	if (p === "/v1/chat/completions") return answerModel(res, JSON.parse(body));
	json(res, 404, { error: p });
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const api = `http://127.0.0.1:${server.address().port}`;

// ---- OpenCode in a throwaway home ---------------------------------------------------

const home = path.join(root, "home");
const env = {
	PATH: process.env.PATH,
	HOME: home,
	XDG_CONFIG_HOME: path.join(home, ".config"),
	XDG_DATA_HOME: path.join(home, ".local/share"),
	XDG_STATE_HOME: path.join(home, ".local/state"),
	XDG_CACHE_HOME: path.join(home, ".cache"),
	XMUSTARD_API_BASE: api,
	XMUSTARD_API_TOKEN: TOKEN,
	OPENCODE_DISABLE_AUTOUPDATE: "1",
};
const provider = { mock: { npm: "@ai-sdk/openai-compatible", name: "Mock", options: { baseURL: `${api}/v1`, apiKey: "mock" }, models: { m: { name: "Mock", tool_call: true } } } };
const withPlugin = (options) => ({ plugins: [{ package: pluginDir, options }] });

function project(name, config) {
	const dir = path.join(root, name);
	mkdirSync(dir, { recursive: true });
	writeFileSync(path.join(dir, "opencode.json"), JSON.stringify({ provider, model: "mock/m", ...config }, null, 2));
	return dir;
}

function run(dir, timeoutMs = 90_000) {
	return new Promise((resolve) => {
		const debug = process.env.DEBUG ? ["--format", "json", "--print-logs", "--log-level", "debug"] : [];
		const child = spawn(OPENCODE, ["run", "--standalone", "--auto", "--model", "mock/m", ...debug, "run the commands"], { cwd: dir, env, detached: true, stdio: ["ignore", "pipe", "pipe"] });
		let out = "";
		child.stdout.on("data", (c) => (out += c));
		child.stderr.on("data", (c) => (out += c));
		const kill = () => {
			try {
				process.kill(-child.pid, "SIGKILL");
			} catch {}
		};
		const timer = setTimeout(kill, timeoutMs);
		child.on("close", (code) => {
			clearTimeout(timer);
			writeFileSync(path.join(dir, "opencode.log"), out);
			kill(); // the standalone server child, if it outlived the CLI
			resolve({ code, out });
		});
	});
}

function check(name, ok, detail) {
	console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok ? "" : `: ${JSON.stringify(detail)}`}`);
	if (!ok) process.exitCode = 1;
}

function opsConfig(ws) {
	if (process.env.OPS_BIN) return JSON.parse(execFileSync(process.env.OPS_BIN, ["mcp-config", "--workspace-id", ws, "--client", "opencode", "--api", api], { encoding: "utf8" }));
	// the form api-go/cmd/xmustard-ops/mcp_config.go writeOpenCodeJSON prints
	const url = `${api}/mcp?client=opencode&workspace=${ws}`;
	return { mcp: { servers: { xmustard: { type: "remote", url, headers: { Authorization: "Bearer {env:XMUSTARD_API_TOKEN}", "X-Xmustard-Workspace": ws }, oauth: false } } } };
}

const initialize = () => seen.mcp.find((m) => m.method === "initialize");
const bound = (init, ws) => !!init && init.query.startsWith(`?client=opencode&workspace=${ws}`) && init.auth === `Bearer ${TOKEN}` && init.workspace === ws;

try {
	const version = execFileSync(OPENCODE, ["--version"], { env, encoding: "utf8", timeout: 30_000 }).match(/(\d+\.\d+\.\d+)/)?.[1];
	console.log(`OpenCode ${version}; tree ${root}`);

	// Stage A: the plugin reduces one shell result
	stage(["seq 1 3000"]); // about 14 KB: above the 4 KiB target the plugin is given
	const a = await run(project("a", withPlugin({ projectionTarget: 4096 })));
	check("A: setup resolved the workspace with the token", seen.workspaces[0] === `Bearer ${TOKEN}`, seen.workspaces);
	check("A: the MCP entry opened a session bound to the workspace", bound(initialize(), "ws-smoke"), initialize());
	check("A: OpenCode took the 2025-06-18 answer and listed tools", seen.mcp.some((m) => m.method === "tools/list"), seen.mcp);
	check("A: execute.after posted one capture", seen.captures.length === 1, { captures: seen.captures.length, out: a.out.slice(-2000) });
	const cap = seen.captures[0];
	if (cap) {
		const q = cap.query;
		check("A: capture query", q.format === "opencode" && q.client === "opencode" && q.tool === "shell" && q.tool_version === `opencode/${version}` && q.is_error === "false" && q.target === "4096", q);
		// the shell hands the hook its own preview: the last 2,000 lines, a note naming
		// the file with the full output, the exit line; metadata truncated: true
		const text = cap.body.content;
		const preview = text.startsWith("1001\n") && text.includes("\n3000\n\n[output truncated; full output saved to: ") && text.endsWith(".out]\nCommand exited with code 0.");
		check("A: capture body: the model text and scalar metadata only", preview && cap.body.id === "call_smoke_0" && !("output" in cap.body) && cap.body.metadata?.exit === 0 && cap.body.metadata?.truncated === true, { ...cap.body, content: `${text.slice(0, 40)}…${text.slice(-60)}` });
	}
	const sent = seen.model.flatMap((m) => m.results).find((r) => r.id === "call_smoke_0")?.text;
	check("A: the model read the replacement instead of the output", sent === replacement("xm1.call_smoke_0"), { sent: sent?.slice(0, 300) });
	check("A: the shell tool was offered to the model", seen.model.some((m) => m.tools.includes("shell")), seen.model.map((m) => m.tools));

	// Stage B: the mcp-config form, no plugin
	stage([]);
	await run(project("b", opsConfig("ws-config")));
	check("B: the mcp-config entry loads and {env:} is substituted", bound(initialize(), "ws-config"), initialize());

	// Stage C: masking through the session context hook
	stage(["seq 1 700", "echo two"]); // about 2.6 KB: above minBytes, below the target
	await run(project("c", withPlugin({ projectionTarget: 4096, mcp: false, mask: { afterTurns: 1, everyTurns: 1, minBytes: 2048 } })));
	const retained = seen.captures.find((c) => c.query.call_id === "call_smoke_0");
	check("C: the older result was retained at the smallest target", retained?.query.target === "1024" && retained.body.content.startsWith("1\n2\n3\n"), retained?.query);
	const last = seen.model.at(-1)?.results ?? [];
	const stub = last.find((r) => r.id === "call_smoke_0")?.text ?? "";
	check("C: the model's last request carries the stub", /^\[xmustard masked: \d+ lines, \d+ bytes of shell output; turn 1; handle xm1\.call_smoke_0; workspace_id ws-smoke; xmustard:\/\/evidence\/xm1\.call_smoke_0\?workspace_id=ws-smoke\] 1$/.test(stub), last);
	check("C: the latest result stays whole", last.find((r) => r.id === "call_smoke_1")?.text.startsWith("two"), last);

} finally {
	if (process.env.KEEP) writeFileSync(path.join(root, "seen.json"), JSON.stringify(seen, null, 2));
	server.close();
	if (!process.env.KEEP) rmSync(root, { recursive: true, force: true });
}
