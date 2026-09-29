// Unit tests for the OpenCode plugin (node --test test/unit.test.ts). A fake xMustard
// API on a loopback port stands in for Go; the 2.0.1 hook inputs come from
// test/fixtures/record.mjs, which derives them from the installed OpenCode's own code.

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import path from "node:path";
import { after, before, beforeEach, describe, test } from "node:test";
import { fileURLToPath } from "node:url";
import { Capturer, captureBody, scalarMetadata, textOf } from "../src/capture.ts";
import { DEFAULT_TOOLS, loadConfig, MIN_CAPTURE_TARGET, POLICY_TARGET, type PluginConfig } from "../src/config.ts";
import plugin from "../src/index.ts";
import { analyze, type MessageView, Masker, MASK_PREFIX } from "../src/mask.ts";
import { mcpEntry } from "../src/mcp.ts";
import { Reducer } from "../src/reduce.ts";
import { applyV1, outcomeV1, serverV1 } from "../src/v1.ts";
import { type AfterInput, afterV2, CodeModeCalls, outcomeV2, setupV2, withMessage } from "../src/v2.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const FIXTURES = path.join(here, "fixtures", "2.0.1");
const GO_BODIES = path.resolve(here, "../../../api-go/internal/evidence/testdata/opencode/2.0.1");

// ---- a fake xMustard API ------------------------------------------------------------

interface Captured {
	query: URLSearchParams;
	body: string;
	auth?: string;
}

class FakeAPI {
	server!: Server;
	base = "";
	captures: Captured[] = [];
	// answer builds the capture reply; the default reduces and shapes a replacement
	answer: (c: Captured) => { status: number; json: unknown } = (c) => ({ status: 200, json: observation(c) });
	workspaces = [{ workspace_id: "ws-1", root_path: "/Users/dev/repo" }];

	async start(): Promise<void> {
		this.server = createServer(async (req, res) => {
			const url = new URL(req.url ?? "/", "http://x");
			const body = await readBody(req);
			const send = (status: number, json: unknown) => {
				res.writeHead(status, { "Content-Type": "application/json" });
				res.end(JSON.stringify(json));
			};
			if (req.method === "GET" && url.pathname === "/api/workspaces") return send(200, this.workspaces);
			if (req.method === "POST" && /^\/api\/workspaces\/[^/]+\/evidence\/capture$/.test(url.pathname)) {
				const c = { query: url.searchParams, body, auth: req.headers.authorization };
				this.captures.push(c);
				const a = this.answer(c);
				return send(a.status, a.json);
			}
			send(404, { error: "not found" });
		});
		await new Promise<void>((r) => this.server.listen(0, "127.0.0.1", r));
		this.base = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
	}
	stop(): Promise<void> {
		return new Promise((r) => this.server.close(() => r()));
	}
}

function readBody(req: IncomingMessage): Promise<string> {
	return new Promise((resolve) => {
		let b = "";
		req.on("data", (c) => (b += c));
		req.on("end", () => resolve(b));
	});
}

// observation is a reduced capture with a replacement whose last line is the recovery line.
function observation(c: Captured, mode = "replace") {
	const handle = `xm1.${c.query.get("call_id")}`;
	return {
		delivery: "xmustard.evidence/v1",
		reduced: true,
		handle,
		resource_uri: `xmustard://evidence/${handle}?workspace_id=ws-1`,
		expires_at: "2099-01-01T00:00:00Z",
		raw_bytes: c.body.length,
		raw_sha256: "ab".repeat(32),
		projected_bytes: 40,
		reducer: "xm-shell/1",
		captured_identity: "unknown",
		shape: { client: "opencode", shape: "opencode.content", mode, payload: mode === "replace" ? { content: `[xmustard shell] projected\n[xmustard evidence] {"handle":"${handle}"}` } : undefined },
	};
}

const api = new FakeAPI();
before(() => {
	// the plugin reads these from the environment; the tests pass their own
	for (const v of ["XMUSTARD_API_TOKEN", "XMUSTARD_TOKEN", "XMUSTARD_WORKSPACE_ID"]) delete process.env[v];
	return api.start().then(() => {
		process.env.XMUSTARD_API_BASE = api.base;
	});
});
after(() => api.stop());
beforeEach(() => {
	api.captures = [];
	api.answer = (c) => ({ status: 200, json: observation(c) });
});

function config(over: Partial<PluginConfig> = {}): PluginConfig {
	return { ...loadConfig({}, { XMUSTARD_API_BASE: api.base, XMUSTARD_API_TOKEN: "tok" }), ...over };
}

function reducer(cfg = config()): Reducer {
	return new Reducer(cfg, new Capturer(cfg), async () => "ws-1", "opencode/2.0.1");
}

const big = (n = POLICY_TARGET + 100) => "x".repeat(n);

function after2(tool: string, content: unknown, over: Partial<AfterInput> = {}): AfterInput {
	return { tool, sessionID: "ses_1", id: `call_${tool}`, input: { command: "go test ./..." }, status: "completed", result: { output: { structured: true }, content, metadata: { exit: 1, truncated: false } }, ...over };
}

// ---- configuration ------------------------------------------------------------------

describe("config", () => {
	test("defaults, lower-only budgets and tool lists", () => {
		const c = loadConfig({}, {});
		assert.equal(c.apiBase, "http://127.0.0.1:8042");
		assert.equal(c.serverName, "xmustard");
		assert.equal(c.projectionTarget, POLICY_TARGET);
		assert.deepEqual([...c.tools].sort(), [...DEFAULT_TOOLS].sort());
		assert.ok(c.registerMcp && c.foreignMcp && c.mask.enabled);
		const env = { XMUSTARD_OPENCODE_PROJECTION_TARGET_BYTES: "4096", XMUSTARD_OPENCODE_TOOLS: "shell, grep", XMUSTARD_OPENCODE_MASK: "off" };
		const l = loadConfig({}, env);
		assert.equal(l.projectionTarget, 4096);
		assert.deepEqual([...l.tools], ["shell", "grep"]);
		assert.equal(l.mask.enabled, false);
		// raising or garbage keeps the default; the option wins over the variable
		assert.equal(loadConfig({}, { XMUSTARD_OPENCODE_PROJECTION_TARGET_BYTES: String(1 << 20) }).projectionTarget, POLICY_TARGET);
		assert.equal(loadConfig({ projectionTarget: 10 }, {}).projectionTarget, POLICY_TARGET);
		assert.equal(loadConfig({ projectionTarget: 8192 }, env).projectionTarget, 8192);
		assert.deepEqual([...loadConfig({ tools: ["read"] }, env).tools], ["read"]);
		assert.equal(loadConfig({ tools: "none" }, {}).tools.size, 0);
		assert.equal(loadConfig({ mask: { enabled: false } }, {}).mask.enabled, false);
		assert.equal(loadConfig({ mcp: false }, {}).registerMcp, false);
	});
	test("the token and the API base come from the environment only", () => {
		assert.equal(loadConfig({ token: "from-options" }, {}).token, undefined);
		assert.equal(loadConfig({ apiBase: "http://attacker.example" }, {}).apiBase, "http://127.0.0.1:8042");
		assert.equal(loadConfig({}, { XMUSTARD_API_TOKEN: "a", XMUSTARD_TOKEN: "b" }).token, "a");
		assert.equal(loadConfig({}, { XMUSTARD_TOKEN: "b" }).token, "b");
	});
});

// ---- the body contract, checked against the Go fixtures -----------------------------

describe("2.0.1 hook inputs", () => {
	const cases = readdirSync(FIXTURES).filter((f) => f.endsWith(".hook.json"));
	test("every recorded case has a Go capture body", () => {
		assert.equal(cases.length, 5);
	});
	for (const file of cases) {
		test(`${file} posts exactly the Go fixture's body`, () => {
			const hook = JSON.parse(readFileSync(path.join(FIXTURES, file), "utf8")) as AfterInput;
			const want = readFileSync(path.join(GO_BODIES, file.replace(".hook.json", ".body.json")), "utf8").trimEnd();
			const outcome = outcomeV2(hook);
			assert.ok(outcome, "a text result or an error");
			assert.equal(captureBody(outcome), want);
			// the structured result.output never travels: it repeats the text
			assert.ok(!("output" in JSON.parse(want)));
		});
	}
	test("shell: content blocks are joined, metadata keeps numbers and booleans", () => {
		const hook = JSON.parse(readFileSync(path.join(FIXTURES, "shell-go-test-fail.hook.json"), "utf8")) as AfterInput;
		const o = outcomeV2(hook);
		assert.ok(o?.text?.endsWith("\nCommand exited with code 1."));
		assert.deepEqual(o?.metadata, { truncated: false, exit: 1 });
	});
});

describe("result text", () => {
	test("text of strings and text blocks; nothing for files or images", () => {
		assert.equal(textOf("a"), "a");
		assert.equal(textOf([{ type: "text", text: "a" }, { type: "text", text: "b" }]), "a\nb");
		assert.equal(textOf([{ type: "text", text: "a" }, { type: "text", text: "b" }], "\n\n"), "a\n\nb");
		assert.equal(textOf([{ type: "text", text: "a" }, { type: "file", uri: "data:image/png;base64,AAAA", mime: "image/png" }]), undefined);
		assert.equal(textOf([]), undefined);
		assert.equal(textOf({ output: "x" }), undefined);
	});
	test("scalar metadata drops strings (1.x metadata.output), objects and arrays", () => {
		assert.deepEqual(scalarMetadata({ output: "copy", exit: 2, truncated: true, description: "d", nested: { a: 1 }, list: [1], n: Number.NaN }), { exit: 2, truncated: true });
		assert.equal(scalarMetadata({ output: "copy" }), undefined);
		assert.equal(scalarMetadata(undefined), undefined);
	});
});

// ---- 2.x execute.after --------------------------------------------------------------

describe("2.x execute.after", () => {
	test("a large shell result is captured and its content replaced", async () => {
		const i = after2("shell", [{ type: "text", text: big() }, { type: "text", text: "Command exited with code 1." }]);
		await afterV2(reducer(), new CodeModeCalls(), i);
		assert.equal(api.captures.length, 1);
		const c = api.captures[0];
		assert.deepEqual(Object.fromEntries(c.query), {
			format: "opencode", client: "opencode", tool: "shell", tool_version: "opencode/2.0.1", call_id: "call_shell", session_id: "ses_1", is_error: "false",
		});
		assert.equal(c.auth, "Bearer tok");
		assert.deepEqual(JSON.parse(c.body).metadata, { exit: 1, truncated: false });
		assert.match(String(i.result?.content), /^\[xmustard shell\] projected\n\[xmustard evidence\] /);
		assert.deepEqual(i.result?.output, { structured: true }, "the structured output is kept");
		const meta = i.result?.metadata as Record<string, unknown>;
		assert.equal(meta.truncated, true, "OpenCode must not truncate the recovery line away");
		assert.equal(meta.exit, 1);
		assert.equal((meta.xmustard as { handle: string }).handle, "xm1.call_shell");
	});
	test("the replacement's metadata holds no undefined member", async () => {
		// OpenCode 2.0.1 drops a tool result whose metadata holds one (seen live:
		// "Tool result missing", and the step runs again)
		api.answer = (c) => {
			const { resource_uri: _r, expires_at: _e, ...rest } = observation(c);
			return { status: 200, json: rest };
		};
		const i = after2("shell", big());
		await afterV2(reducer(), new CodeModeCalls(), i);
		const meta = i.result?.metadata;
		assert.deepEqual(JSON.parse(JSON.stringify(meta)), meta);
	});
	test("left alone: small, read, xMustard's own tools, non-text, not shaped", async () => {
		const r = reducer();
		const untouched: AfterInput[] = [
			after2("shell", "small output"),
			after2("read", big()),
			after2("xmustard_search", big()),
			after2("shell", [{ type: "text", text: big() }, { type: "file", uri: "data:image/png;base64,AAAA", mime: "image/png" }]),
		];
		for (const i of untouched) {
			const before = structuredClone(i.result);
			await afterV2(r, new CodeModeCalls(), i);
			assert.deepEqual(i.result, before, i.tool);
		}
		assert.equal(api.captures.length, 0);
		api.answer = (c) => ({ status: 200, json: observation(c, "observe") });
		const observed = after2("grep", big());
		await afterV2(r, new CodeModeCalls(), observed);
		assert.equal(api.captures.length, 1);
		assert.equal(observed.result?.content, big());
	});
	test("another server's MCP tool is reduced; a code-mode program's inner calls are not", async () => {
		const r = reducer();
		const codeMode = new CodeModeCalls();
		codeMode.start("call_exec");
		const inner = after2("github_search_code", big(), { id: "call_exec" });
		await afterV2(r, codeMode, inner);
		assert.equal(api.captures.length, 0, "the program reads the inner call's output, not the model");
		const program = after2("execute", [{ type: "text", text: big() }], { id: "call_exec" });
		await afterV2(r, codeMode, program);
		assert.equal(api.captures.length, 1);
		const direct = after2("github_search_code", big(), { id: "call_direct" });
		await afterV2(r, codeMode, direct);
		assert.equal(api.captures.length, 2);
		assert.equal(api.captures[1].query.get("tool"), "github_search_code");
		assert.match(String(direct.result?.content), /xmustard evidence/);
	});
	test("a failed call's message is replaced through the error's own constructor", async () => {
		class ToolError extends Error {
			readonly _tag = "Tool.Error";
			readonly error: unknown;
			readonly metadata: unknown;
			constructor(p: { message: string; error?: unknown; metadata?: unknown }) {
				super(p.message);
				this.error = p.error;
				this.metadata = p.metadata;
			}
		}
		const cause = new Error("isError");
		const i: AfterInput = { tool: "ci_fetch_log", sessionID: "ses_1", id: "call_err", input: {}, status: "error", error: new ToolError({ message: big(), error: cause, metadata: { k: 1 } }) };
		await afterV2(reducer(), new CodeModeCalls(), i);
		assert.equal(api.captures[0].query.get("is_error"), "true");
		assert.ok("error" in JSON.parse(api.captures[0].body));
		assert.ok(i.error instanceof ToolError);
		assert.match(i.error.message, /xmustard evidence/);
		assert.equal((i.error as ToolError).error, cause);
		assert.deepEqual((i.error as ToolError).metadata, { k: 1 });
		assert.equal(withMessage({ message: "plain object" }, "x"), undefined, "a plain object is not rebuilt");
	});
	test("capture refused for want of a redactor pauses capture; results stay", async () => {
		api.answer = () => ({ status: 503, json: { reason: "redaction_unavailable", error: "no redactor" } });
		const r = reducer();
		const first = after2("shell", big());
		await afterV2(r, new CodeModeCalls(), first);
		await afterV2(r, new CodeModeCalls(), after2("shell", big(), { id: "call_2" }));
		assert.equal(api.captures.length, 1, "paused after the refusal");
		assert.equal(first.result?.content, big());
		assert.match(r.capturer.paused ?? "", /redaction_unavailable/);
	});
	test("an unreachable API leaves results alone and never throws", async () => {
		const cfg = config({ apiBase: "http://127.0.0.1:9", captureTimeoutMs: 500 });
		const i = after2("shell", big());
		await afterV2(reducer(cfg), new CodeModeCalls(), i);
		assert.equal(i.result?.content, big());
	});
});

// ---- 2.x setup ----------------------------------------------------------------------

describe("2.x setup", () => {
	function fakeContext(existing?: string) {
		const servers = new Map<string, unknown>(existing ? [[existing, { type: "remote", url: "user" }]] : []);
		const hooks = new Map<string, (i: unknown) => unknown>();
		let disposed = 0;
		const reg = () => ({ dispose: async () => void disposed++ });
		const ctx = {
			app: { name: "opencode", version: "2.0.1", channel: "latest" },
			location: { directory: "/Users/dev/repo" },
			options: {},
			mcp: {
				transform: async (cb: (e: unknown) => void) => {
					cb({ get: (n: string) => servers.get(n), set: (n: string, v: unknown) => servers.set(n, v) });
					return reg();
				},
			},
			tool: { hook: async (name: string, cb: (i: unknown) => unknown) => (hooks.set(`tool.${name}`, cb), reg()) },
			session: { hook: async (name: string, cb: (i: unknown) => unknown) => (hooks.set(`session.${name}`, cb), reg()) },
		};
		return { ctx, servers, hooks, disposed: () => disposed };
	}
	test("registers the MCP entry, the tool hooks and masking; cleanup disposes them", async () => {
		const f = fakeContext();
		const cleanup = await setupV2(f.ctx as never);
		assert.deepEqual(f.servers.get("xmustard"), {
			type: "remote",
			url: `${api.base}/mcp?client=opencode&workspace=ws-1`,
			headers: { "X-Xmustard-Workspace": "ws-1" },
			oauth: false,
		});
		assert.deepEqual([...f.hooks.keys()].sort(), ["session.context", "tool.execute.after", "tool.execute.before"]);
		const i = after2("shell", big());
		await f.hooks.get("tool.execute.after")?.(i);
		assert.equal(api.captures[0].query.get("tool_version"), "opencode/2.0.1");
		await cleanup?.();
		assert.equal(f.disposed(), 4);
	});
	test("never overrides a server entry of the same name", async () => {
		const f = fakeContext("xmustard");
		await setupV2(f.ctx as never);
		assert.deepEqual(f.servers.get("xmustard"), { type: "remote", url: "user" });
	});
	test("the default export serves both plugin APIs", () => {
		assert.equal(plugin.id, "xmustard");
		assert.equal(plugin.setup, setupV2);
		assert.equal(plugin.server, serverV1);
	});
	test("the MCP entry without a workspace falls back to roots", () => {
		assert.deepEqual(mcpEntry(config({ token: undefined })), { type: "remote", url: `${api.base}/mcp?client=opencode`, headers: {}, oauth: false });
	});
});

// ---- 1.x ----------------------------------------------------------------------------

describe("1.x entrypoint", () => {
	test("a built-in's output and an MCP result's content are replaced", async () => {
		const hooks = await serverV1({ directory: "/Users/dev/repo" }, {});
		const builtin = { title: "go test", output: big(), metadata: { exit: 1, output: "tail copy", description: "d" } };
		await hooks["tool.execute.after"]({ tool: "bash", sessionID: "s", callID: "c1", args: { command: "go test" } }, builtin);
		assert.match(String(builtin.output), /xmustard evidence/);
		assert.deepEqual(JSON.parse(api.captures[0].body).metadata, { exit: 1 }, "metadata.output is never posted");
		const mcp = { content: [{ type: "text", text: big() }], isError: false };
		await hooks["tool.execute.after"]({ tool: "github_search_code", sessionID: "s", callID: "c2", args: {} }, mcp);
		assert.deepEqual(mcp.content.length, 1);
		assert.match(mcp.content[0].text, /xmustard evidence/);
		const config: { mcp?: Record<string, unknown> } = { mcp: { xmustard: "user's" } };
		await hooks.config(config);
		assert.equal(config.mcp?.xmustard, "user's");
		const empty: { mcp?: Record<string, unknown> } = {};
		await hooks.config(empty);
		assert.equal((empty.mcp?.xmustard as { type: string }).type, "remote");
	});
	test("1.x MCP text blocks are joined by blank lines", () => {
		const o = outcomeV1({ tool: "gh_x", sessionID: "s", callID: "c", args: {} }, { content: [{ type: "text", text: "a" }, { type: "text", text: "b" }] });
		assert.equal(o?.text, "a\n\nb");
		const out = { output: "x" };
		applyV1(out, "y");
		assert.equal(out.output, "y");
	});
});

// ---- masking ------------------------------------------------------------------------

class Message {
	role: string;
	content: MessageView["content"];
	constructor(role: string, content: MessageView["content"]) {
		this.role = role;
		this.content = content;
	}
}

// history builds `turns` assistant turns, each calling a shell that returns `text(turn)`.
function history(turns: number, text: (turn: number) => string, failAt?: number): Message[] {
	const out: Message[] = [new Message("user", [{ type: "text" }])];
	for (let t = 1; t <= turns; t++) {
		out.push(new Message("assistant", [{ type: "tool-call", id: `c${t}`, name: "shell", input: { command: `step ${t}` } }]));
		out.push(new Message("tool", [{ type: "tool-result", id: `c${t}`, name: "shell", result: { type: t === failAt ? "error" : "text", value: text(t) } }]));
	}
	return out;
}

describe("masking", () => {
	const long = (t: number) => `first line of ${t}\n${"y".repeat(3000)}`;
	test("results older than afterTurns are masked at a window; the latest failure is not", async () => {
		const r = reducer();
		const masker = new Masker({ enabled: true, afterTurns: 10, everyTurns: 5, minBytes: 2048 }, r);
		const messages = history(15, long, 2);
		const originals = [...messages];
		await masker.apply({ sessionID: "ses_1", messages });
		const { results } = analyze(messages);
		const masked = results.filter((x) => x.text.startsWith(MASK_PREFIX)).map((x) => x.id);
		// turns 1..5 are 10+ turns old; turn 2 failed last
		assert.deepEqual(masked, ["c1", "c3", "c4", "c5"]);
		assert.equal(api.captures.length, 4);
		assert.ok(api.captures.every((c) => c.query.get("target") === String(MIN_CAPTURE_TARGET)), "retained at the smallest target");
		assert.equal(JSON.parse(api.captures[0].body).input.command, "step 1", "the call's input selects the family");
		const stub = results[0].text;
		assert.match(stub, /^\[xmustard masked: 2 lines, 3016 bytes of shell output; turn 1; handle xm1\.c1; workspace_id ws-1; xmustard:\/\/evidence\/xm1\.c1\?workspace_id=ws-1\] first line of 1$/);
		// copies with the prototype kept; the originals are untouched
		assert.ok(messages[2] instanceof Message && messages[2] !== originals[2]);
		assert.equal(originals[2].content[0].result?.value, long(1));
		// the next request rebuilds the messages: the same stubs, no new capture
		const again = history(15, long, 2);
		await masker.apply({ sessionID: "ses_1", messages: again });
		assert.deepEqual(analyze(again).results.map((x) => x.text), results.map((x) => x.text));
		assert.equal(api.captures.length, 4);
	});
	test("the mask advances only every everyTurns turns", async () => {
		const masker = new Masker({ enabled: true, afterTurns: 2, everyTurns: 5, minBytes: 2048 }, reducer());
		const at = async (turns: number) => {
			const m = history(turns, long);
			await masker.apply({ sessionID: "ses_w", messages: m });
			return analyze(m).results.filter((x) => x.text.startsWith(MASK_PREFIX)).length;
		};
		assert.equal(await at(4), 0, "no window yet");
		assert.equal(await at(5), 3, "window at turn 5: turns 1-3");
		assert.equal(await at(9), 3, "between windows the prefix does not change");
		assert.equal(await at(10), 8, "window at turn 10");
	});
	test("small results, other tools and a capture outage stay visible", async () => {
		api.answer = () => ({ status: 503, json: { reason: "redaction_unavailable" } });
		const masker = new Masker({ enabled: true, afterTurns: 1, everyTurns: 1, minBytes: 2048 }, reducer());
		const m = history(4, (t) => (t === 1 ? "short" : long(t)));
		m.push(new Message("tool", [{ type: "tool-result", id: "r1", name: "read", result: { type: "text", value: long(9) } }]));
		await masker.apply({ sessionID: "ses_o", messages: m });
		assert.ok(analyze(m).results.every((x) => !x.text.startsWith(MASK_PREFIX)));
	});
	test("a handle from the reduction is reused, not captured again", async () => {
		const r = reducer();
		await afterV2(r, new CodeModeCalls(), after2("shell", big(), { id: "c1" }));
		assert.equal(api.captures.length, 1);
		const masker = new Masker({ enabled: true, afterTurns: 1, everyTurns: 1, minBytes: 2048 }, r);
		const m = history(3, long);
		await masker.apply({ sessionID: "ses_h", messages: m });
		assert.equal(api.captures.length, 2, "only c2 needed a capture");
		assert.match(analyze(m).results[0].text, /; handle xm1\.c1;/);
	});
});
