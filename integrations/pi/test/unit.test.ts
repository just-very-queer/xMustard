// Unit tests for the adapter's pure modules against an in-process HTTP server. These
// check transport behavior (bounds, deadlines, abort forwarding, auth header) and the
// delivery state machine; the real Pi + Go behavior is covered by test/e2e.

import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync } from "node:fs";
import http from "node:http";
import type net from "node:net";
import os from "node:os";
import path from "node:path";
import { after, before, describe, test } from "node:test";
import { type AdapterConfig, loadConfig, PI_POLICY_TARGET } from "../src/config.ts";
import {
	argsDigest,
	CAPTURE_PAUSE_MS,
	Capturer,
	DELIVERY_HEADER,
	expand,
	DELIVERY_VERSION,
	type Delivery,
	INLINE_LIMIT,
	PendingCalls,
	projectBuiltin,
	projectResult,
	renderPage,
	runTool,
} from "../src/delivery.ts";
import { callerTools, WorkspaceResolver } from "../src/workspace.ts";
import { send, XmustardHttpError } from "../src/http.ts";
import { checkRequired, TOOL_SPECS, toJsonSchema } from "../src/tools.ts";


type Handler = (req: http.IncomingMessage, res: http.ServerResponse, body: Buffer) => void;
let handler: Handler = (_q, s) => s.end();
let server: http.Server;
let base = "";
const closed: string[] = [];

before(async () => {
	server = http.createServer((req, res) => {
		const chunks: Buffer[] = [];
		req.on("data", (c: Buffer) => chunks.push(c));
		req.on("end", () => handler(req, res, Buffer.concat(chunks)));
		res.on("close", () => closed.push(req.url ?? ""));
	});
	await new Promise<void>((r) => server.listen(0, "127.0.0.1", () => r()));
	base = `http://127.0.0.1:${(server.address() as net.AddressInfo).port}`;
});
after(() => {
	server.closeAllConnections();
	server.close();
});

const cfg = (over: Partial<AdapterConfig> = {}): AdapterConfig => ({
	...loadConfig({}),
	apiBase: base,
	token: undefined,
	delivery: "source",
	toolTimeoutMs: 2_000,
	projectionTimeoutMs: 300,
	...over,
});
const spec = (name: string) => TOOL_SPECS.find((t) => t.name === name)!;
const envelope = (over: Partial<Delivery> = {}): Delivery => ({
	delivery: DELIVERY_VERSION,
	tool: "search",
	status: 200,
	is_error: false,
	content_type: "application/json",
	reduced: true,
	projection: "{\"hits\":[…]}",
	handle: "xm1.AAAA",
	raw_bytes: 200_000,
	raw_sha256: "ab",
	projected_bytes: 12,
	reducer: "xm-reduce/1",
	omissions: [{ kind: "array_items" }],
	page_size: 65536,
	projection_mode: "json",
	captured_identity: "bound",
	captured_key: "k1",
	expires_at: "2026-09-25T00:00:00Z",
	...over,
});

describe("tool specs mirror the MCP server", () => {
	test("nine tools with Go-identical escaping", () => {
		assert.deepEqual(
			TOOL_SPECS.map((t) => t.name),
			["ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"],
		);
		// url.QueryEscape: space→"+", !'()* escaped; url.PathEscape keeps $&+:=@
		assert.equal(
			spec("search").build({ workspace_id: "w s/1", q: "a b&c!*'()", mode: "pattern" }).path,
			"/api/workspaces/w%20s%2F1/search?q=a+b%26c%21%2A%27%28%29&mode=pattern",
		);
		assert.equal(
			spec("verify").build({ workspace_id: "w", entry_id: "e:@$&+=;,?", approve: false }).path,
			"/api/workspaces/w/context/e:@$&+=%3B%2C%3F/verify?approve=false",
		);
		assert.equal(spec("verify").build({ workspace_id: "w", entry_id: "e" }).path, "/api/workspaces/w/context/e/verify?approve=true");
		const rem = spec("remember").build({ workspace_id: "w", content: "c", paths: " a.go, ,b.go " });
		assert.equal(rem.method, "POST");
		assert.deepEqual(JSON.parse(rem.body ?? ""), { content: "c", paths: ["a.go", "b.go"] });
		assert.equal(spec("impact").build({ workspace_id: "w", symbol: "S", from: "A", to: "B" }).path, "/api/workspaces/w/changes/since-index?from=A&to=B");
	});
	test("JSON schema matches tools/list shape", () => {
		assert.deepEqual(toJsonSchema(spec("search")), {
			type: "object",
			properties: {
				workspace_id: { type: "string", description: "workspace id; auto-resolved if omitted" },
				q: { type: "string", description: "the search query" },
				mode: { type: "string", description: "hybrid (default) or pattern (ast-grep)", enum: ["hybrid", "pattern"] },
				lang: { type: "string", description: "language hint for pattern mode" },
				seed: { type: "string", description: "symbol to anchor the graph-proximity lane" },
				limit: { type: "integer", description: "max hits (default 25)", minimum: 1, maximum: 50 },
			},
			required: ["q"],
			additionalProperties: false,
		});
		// no required arguments at all: the key is omitted, as Go omits it
		assert.deepEqual(Object.keys(toJsonSchema(spec("ground"))).sort(), ["additionalProperties", "properties", "type"]);
	});
	// The Go tool table generates api-go/internal/mcpserver/testdata/tools_list.json
	// (its snapshot test fails until it is regenerated); this mirror must match it, so a
	// description or schema edited on one side fails here, not only in the live e2e.
	test("mirror matches the generated Go tools/list", (t) => {
		const golden = new URL("../../../api-go/internal/mcpserver/testdata/tools_list.json", import.meta.url);
		if (!existsSync(golden)) return t.skip("api-go not present in this checkout");
		const list = JSON.parse(readFileSync(golden, "utf8")) as { tools: { name: string; description: string; inputSchema: unknown }[] };
		assert.deepEqual(
			TOOL_SPECS.map((s) => s.name),
			list.tools.map((g) => g.name),
		);
		for (const g of list.tools) {
			assert.equal(spec(g.name).description, g.description, `${g.name} description`);
			assert.deepEqual(toJsonSchema(spec(g.name)), g.inputSchema, `${g.name} inputSchema`);
		}
	});
	test("new bounds and the verify note build like Go", () => {
		assert.equal(spec("recall").build({ workspace_id: "w", q: "auth flow", limit: 5 }).path, "/api/workspaces/w/context/active?query=auth+flow&limit=5");
		assert.equal(spec("search").build({ workspace_id: "w", q: "x", limit: 50 }).path, "/api/workspaces/w/search?q=x&limit=50");
		assert.equal(spec("impact").build({ workspace_id: "w", symbol: "S", max_depth: 2 }).path, "/api/workspaces/w/changes/since-index?symbol=S&depth=2");
		const v = spec("verify").build({ workspace_id: "w", entry_id: "e", approve: false, note: "stale: a.go" });
		assert.equal(v.path, "/api/workspaces/w/context/e/verify?approve=false");
		assert.equal(v.body, '{"note":"stale: a.go"}');
		assert.equal(spec("verify").build({ workspace_id: "w", entry_id: "e" }).body, undefined);
		assert.equal(checkRequired(spec("search"), { workspace_id: "w" }), 'missing required argument "q" for search');
		assert.equal(checkRequired(spec("ground"), {}), "no workspace_id for ground");
		assert.equal(checkRequired(spec("ground"), { workspace_id: "w" }), undefined);
	});
	test("hook-path args_digest equals the Go middleware's digest", () => {
		// expected values computed with Go's net/http + the middleware's formula
		assert.equal(
			argsDigest("GET", "/api/workspaces/w%20s%2F1/search?q=a+b%26c%21%2A%27%28%29&mode=pattern&lang=go"),
			"9e29f393175a4fdd337ac52e8a3a905ac6da40ef63d54dc8e6ca8da78d4d447b",
		);
		assert.equal(argsDigest("POST", "/api/workspaces/w/context", '{"content":"c","paths":["a.go"]}'), "525109a162a0afe764e33afbd14fb428ea7d8e89d0b16e8f3bdce17efba37f42");
		assert.equal(
			argsDigest("POST", spec("verify").build({ workspace_id: "w", entry_id: "e:@$&+=;,?", approve: false }).path),
			"9cdf1459a4020d03492b20e9f510e47a771f61dbe9ca80ce0aab7430c6418801",
		);
	});
});

describe("config", () => {
	test("timeouts can only be lowered", () => {
		const c = loadConfig({ XMUSTARD_PI_TOOL_TIMEOUT_MS: "999999", XMUSTARD_PI_PROJECTION_TIMEOUT_MS: "250", XMUSTARD_TOKEN: "  " });
		assert.equal(c.toolTimeoutMs, 60_000);
		assert.equal(c.projectionTimeoutMs, 250);
		assert.equal(c.token, undefined);
		assert.equal(c.delivery, "source");
		assert.equal(c.apiBase, "http://127.0.0.1:8042");
		assert.equal(c.workspaceId, undefined);
		assert.equal(loadConfig({ XMUSTARD_WORKSPACE_ID: " ws-1 " }).workspaceId, "ws-1");
	});
});

describe("workspace resolution", () => {
	const repo = realpathSync(mkdtempSync(path.join(os.tmpdir(), "xm-pi-ws-")));
	const sub = path.join(repo, "src", "pkg");
	mkdirSync(sub, { recursive: true });
	test("argument, then XMUSTARD_WORKSPACE_ID, then the working directory", async () => {
		let lists = 0;
		handler = (req, res) => {
			if (req.url === "/api/workspaces") {
				lists++;
				return res.end(JSON.stringify([{ workspace_id: "outer", root_path: path.dirname(repo) }, { workspace_id: "inner", root_path: repo }]));
			}
			res.writeHead(404).end();
		};
		const r = new WorkspaceResolver(cfg());
		assert.equal((await r.resolve({ workspace_id: "given" }, sub)).workspace_id, "given");
		assert.equal((await new WorkspaceResolver(cfg({ workspaceId: "env" })).resolve({}, sub)).workspace_id, "env");
		assert.equal(lists, 0, "no listing when the workspace is known");
		assert.equal((await r.resolve({ q: "x" }, sub)).workspace_id, "inner", "longest containing root wins");
		assert.equal((await r.resolve({}, sub)).workspace_id, "inner");
		assert.equal(lists, 1, "the directory's workspace is cached");
	});
	test("an unregistered directory fails clearly and never registers", async () => {
		const posts: string[] = [];
		handler = (req, res) => {
			if (req.method === "POST") posts.push(req.url ?? "");
			res.end(JSON.stringify([{ workspace_id: "alpha", root_path: "/nonexistent/alpha" }]));
		};
		await assert.rejects(new WorkspaceResolver(cfg()).resolve({}, sub), (e: Error) => {
			assert.match(e.message, /no workspace resolved .*XMUSTARD_WORKSPACE_ID is unset.*not inside a registered workspace\). Pass workspace_id; registered: alpha \(\/nonexistent\/alpha\)/);
			return true;
		});
		await assert.rejects(new WorkspaceResolver(cfg()).resolve({}, undefined), /Pi reported no working directory/);
		assert.deepEqual(posts, []);
	});
});

describe("transport", () => {
	test("sends bearer token but never echoes it in errors", async () => {
		let auth = "";
		handler = (req, res) => {
			auth = req.headers.authorization ?? "";
			res.writeHead(401).end('{"error":"authentication required"}');
		};
		const secret = "xmt_secret_value";
		const c = cfg({ token: secret });
		const res = await send(c, { method: "GET", path: "/x", timeoutMs: 1000, maxBytes: 100 });
		assert.equal(auth, `Bearer ${secret}`);
		assert.equal(res.status, 401);
		await assert.rejects(runTool(c, spec("ground"), { workspace_id: "w" }, { toolCallId: "c1" }, undefined, new PendingCalls()), (e: Error) => {
			assert.match(e.message, /401/);
			assert.ok(!e.message.includes(secret));
			return true;
		});
		const unreachable = { ...c, apiBase: "http://user:pw@127.0.0.1:9" };
		await assert.rejects(send(unreachable, { method: "GET", path: "/x", timeoutMs: 1000, maxBytes: 10 }), (e: XmustardHttpError) => {
			assert.equal(e.failure, "unreachable");
			assert.ok(!e.message.includes(secret) && !e.message.includes("pw"));
			return true;
		});
	});
	test("refuses bodies past the cap, with and without Content-Length", async () => {
		const big = Buffer.alloc(2 << 20, 0x61);
		handler = (_q, res) => res.end(big);
		await assert.rejects(send(cfg(), { method: "GET", path: "/cl", timeoutMs: 2000, maxBytes: 1 << 20 }), { failure: "too_large" });
		handler = (_q, res) => {
			res.writeHead(200, { "Transfer-Encoding": "chunked" });
			for (let i = 0; i < 32; i++) res.write(big.subarray(0, 64 << 10));
			res.end();
		};
		await assert.rejects(send(cfg(), { method: "GET", path: "/chunked", timeoutMs: 2000, maxBytes: 1 << 20 }), { failure: "too_large" });
	});
	test("deadline bounds a hung server when no signal is supplied", async () => {
		handler = () => {}; // never answers
		const t0 = Date.now();
		await assert.rejects(send(cfg(), { method: "GET", path: "/hang-nosignal", timeoutMs: 300, maxBytes: 10 }), { failure: "timeout" });
		assert.ok(Date.now() - t0 < 2000);
	});
	test("caller abort is forwarded: the server sees the connection close", async () => {
		handler = () => {};
		const ac = new AbortController();
		const p = send(cfg(), { method: "GET", path: "/hang-abort", timeoutMs: 10_000, signal: ac.signal, maxBytes: 10 });
		setTimeout(() => ac.abort(), 100);
		await assert.rejects(p, { failure: "aborted" });
		for (let i = 0; i < 50 && !closed.includes("/hang-abort"); i++) await new Promise((r) => setTimeout(r, 20));
		assert.ok(closed.includes("/hang-abort"), "server observed the aborted request closing");
	});
});

describe("source delivery", () => {
	test("renders projection + evidence footer and signals the handle", async () => {
		let hdr = "";
		handler = (req, res) => {
			hdr = String(req.headers[DELIVERY_HEADER.toLowerCase()]);
			res.writeHead(200, { [DELIVERY_HEADER]: DELIVERY_VERSION, "Content-Type": "application/json" }).end(JSON.stringify(envelope()));
		};
		const pending = new PendingCalls();
		const r = await runTool(cfg(), spec("search"), { workspace_id: "w", q: "q" }, { toolCallId: "c2", sessionId: "s" }, undefined, pending);
		assert.equal(hdr, DELIVERY_VERSION);
		const [proj, footer] = r.content[0].text.split("\n[xmustard evidence] ");
		assert.equal(proj, envelope().projection);
		const f = JSON.parse(footer);
		assert.equal(f.handle, "xm1.AAAA");
		assert.equal(f.captured_identity, "bound");
		assert.equal(f.workspace_id, "w");
		let handles = 0;
		const out = await projectResult(cfg(), { toolCallId: "c2", toolName: "search", isError: false, content: [] }, pending, {}, () => handles++);
		assert.equal(out, undefined, "already-projected content is left as is");
		assert.equal(handles, 1);
	});
	test("delivered errors stay errors and keep their details", async () => {
		handler = (_q, res) =>
			res.writeHead(200, { [DELIVERY_HEADER]: DELIVERY_VERSION }).end(JSON.stringify(envelope({ is_error: true, status: 404, reduced: false, handle: undefined, projection: '{"error":"Missing resource"}' })));
		const pending = new PendingCalls();
		await assert.rejects(runTool(cfg(), spec("why_failed"), { workspace_id: "w", run_id: "r" }, { toolCallId: "c3" }, undefined, pending), /Missing resource/);
		const out = await projectResult(cfg(), { toolCallId: "c3", toolName: "why_failed", isError: true, content: [] }, pending, {}, () => assert.fail("no handle"));
		assert.equal(out?.isError, true);
		assert.equal(out?.details?.delivery?.status, 404);
	});
});

describe("hook delivery", () => {
	test("posts the exact raw bytes to /evidence and keeps error status", async () => {
		const raw = Buffer.from(JSON.stringify({ error: "boom", detail: "é" }));
		let posted: Buffer = Buffer.alloc(0);
		let query = new URLSearchParams();
		handler = (req, res, body) => {
			if (req.method === "GET") return res.writeHead(500, { "Content-Type": "application/json" }).end(raw);
			posted = body;
			query = new URL(req.url ?? "", base).searchParams;
			res.writeHead(200).end(JSON.stringify(envelope({ is_error: true, status: 500, captured_identity: "unknown", captured_key: "" })));
		};
		const pending = new PendingCalls();
		const c = cfg({ delivery: "hook" });
		await assert.rejects(runTool(c, spec("ground"), { workspace_id: "w" }, { toolCallId: "c4" }, undefined, pending), /boom/);
		let handles = 0;
		const out = await projectResult(c, { toolCallId: "c4", toolName: "ground", isError: true, content: [] }, pending, { sessionId: "s1" }, () => handles++);
		assert.deepEqual(posted, raw);
		assert.equal(query.get("tool"), "ground");
		assert.equal(query.get("is_error"), "true");
		assert.equal(query.get("status"), "500");
		assert.equal(query.get("issuer"), "pi");
		assert.equal(query.get("call_id"), "c4");
		assert.equal(query.get("session_id"), "s1");
		assert.equal(out?.isError, true);
		assert.equal(JSON.parse(out?.content?.[0].text.split("[xmustard evidence] ")[1] ?? "{}").captured_identity, "unknown");
		assert.equal(handles, 1);
	});
	test("projection timeout without a signal preserves a small original exactly", async () => {
		const raw = '{"ok":true,"text":"résumé"}';
		handler = (req, res) => {
			if (req.method === "GET") return res.end(raw);
			// POST /evidence hangs
		};
		const pending = new PendingCalls();
		const c = cfg({ delivery: "hook" });
		await runTool(c, spec("ground"), { workspace_id: "w" }, { toolCallId: "c5" }, undefined, pending);
		const t0 = Date.now();
		const out = await projectResult(c, { toolCallId: "c5", toolName: "ground", isError: false, content: [] }, pending, {}, () => assert.fail());
		assert.ok(Date.now() - t0 < 2000);
		assert.equal(out?.content?.[0].text, raw);
		assert.equal(out?.isError, false);
		assert.match(out?.details?.projection_error ?? "", /timed out after 300 ms/);
	});
	test("projection failure on an oversized original is an explicit size error", async () => {
		const raw = Buffer.alloc(INLINE_LIMIT + 1, 0x62);
		handler = (req, res) => {
			if (req.method === "GET") return res.end(raw);
			res.writeHead(507).end('{"error":"evidence retention quota full","reason":"quota_full"}');
		};
		const pending = new PendingCalls();
		const c = cfg({ delivery: "hook" });
		const r = await runTool(c, spec("ground"), { workspace_id: "w" }, { toolCallId: "c6" }, undefined, pending);
		assert.match(r.content[0].text, /awaiting projection/);
		const out = await projectResult(c, { toolCallId: "c6", toolName: "ground", isError: false, content: [] }, pending, {}, () => assert.fail());
		assert.equal(out?.isError, true);
		assert.match(out?.content?.[0].text ?? "", /above the 65536-byte inline limit.*quota_full/);
		assert.equal(out?.details?.path, "size_error");
	});
	test("an aborted turn cancels the projection promptly", async () => {
		handler = (req, res) => (req.method === "GET" ? res.end("{}") : undefined);
		const pending = new PendingCalls();
		const c = cfg({ delivery: "hook", projectionTimeoutMs: 5000 });
		await runTool(c, spec("ground"), { workspace_id: "w" }, { toolCallId: "c7" }, undefined, pending);
		const ac = new AbortController();
		setTimeout(() => ac.abort(), 50);
		const t0 = Date.now();
		const out = await projectResult(c, { toolCallId: "c7", toolName: "ground", isError: false, content: [] }, pending, { signal: ac.signal }, () => {});
		assert.ok(Date.now() - t0 < 1000);
		assert.equal(out?.content?.[0].text, "{}");
		assert.match(out?.details?.projection_error ?? "", /aborted/);
	});
	test("results that never reached Go are left untouched", async () => {
		assert.equal(await projectResult(cfg(), { toolCallId: "none", toolName: "ground", isError: true, content: [] }, new PendingCalls(), {}, () => {}), undefined);
	});
});

describe("expansion pages", () => {
	const page = (bytes: Buffer) => ({
		handle: "xm1.A",
		tool: "impact",
		content_type: "application/json",
		offset: 0,
		length: bytes.length,
		total_bytes: 10,
		next_offset: bytes.length,
		eof: false,
		encoding: "base64",
		data: bytes.toString("base64"),
		raw_sha256: "x",
		captured_key: "k1",
		current_key: "k2",
		freshness: "stale",
		stale: true,
		expires_at: "t",
	});
	test("valid UTF-8 pages render as text, split or invalid ones as base64", () => {
		const ok = renderPage(page(Buffer.from("héllo")));
		assert.equal(ok.details.text_encoding, "utf-8");
		assert.match(ok.content[0].text, /\nhéllo$/);
		assert.match(ok.content[0].text, /"stale":true/);
		const split = Buffer.from("hé").subarray(0, 2); // cuts é in half
		const s = renderPage(page(split));
		assert.equal(s.details.text_encoding, "base64");
		assert.deepEqual(Buffer.from(s.details.data_base64, "base64"), split);
		const bad = renderPage(page(Buffer.from([0xff, 0xfe, 0x00])));
		assert.equal(bad.details.text_encoding, "base64");
		assert.ok(bad.content[0].text.endsWith(Buffer.from([0xff, 0xfe, 0x00]).toString("base64")));
	});
	test("pending-call state is bounded", () => {
		const p = new PendingCalls();
		for (let i = 0; i < 1000; i++) p.set(`c${i}`, { kind: "delivered", workspaceId: "w", delivery: envelope() });
		assert.equal(p.size, 256);
		assert.equal(p.take("c0"), undefined);
		assert.ok(p.take("c999"));
	});
});

describe("expansion search", () => {
	test("pattern, query and lines search the original through /evidence/search", async () => {
		let seen = "";
		handler = (req, res) => {
			seen = req.url ?? "";
			res.setHeader("content-type", "application/json");
			res.end(
				JSON.stringify({
					handle: "xm1.S",
					tool: "bash",
					lines: [
						{ line: 11, offset: 300, text: "context before" },
						{ line: 12, offset: 320, text: "--- FAIL: TestParse (0.00s)", match: true },
					],
					matches: 1,
					match_cap_reached: false,
					next_offset: 400,
					next_line: 13,
					eof: true,
					bytes_scanned: 400,
					total_bytes: 400,
					freshness: "unknown",
					stale: true,
					expires_at: "t",
				}),
			);
		};
		const out = await expand(cfg(), { workspace_id: "w 1", handle: "xm1.S", pattern: "--- FAIL", max_matches: 5 }, undefined);
		const u = new URL(seen, "http://x");
		assert.equal(u.pathname, "/api/workspaces/w%201/evidence/search");
		assert.equal(u.searchParams.get("pattern"), "--- FAIL");
		assert.equal(u.searchParams.get("max_matches"), "5");
		assert.equal(u.searchParams.get("handle"), "xm1.S");
		assert.match(out.content[0].text, /^\[xmustard search\] .*"matches":1/);
		assert.match(out.content[0].text, /\n {4}12: --- FAIL: TestParse/);
		assert.match(out.content[0].text, /\n {4}11- context before/);
		// resuming a search forwards offset with start_line
		await expand(cfg(), { workspace_id: "w", handle: "xm1.S", query: "fail", offset: 400, start_line: 13 }, undefined);
		const r = new URL(seen, "http://x");
		assert.equal(r.searchParams.get("query"), "fail");
		assert.equal(r.searchParams.get("offset"), "400");
		assert.equal(r.searchParams.get("start_line"), "13");
		// an offset without start_line is forwarded as is: the server refuses it
		// rather than the adapter renumbering lines from 1
		await expand(cfg(), { workspace_id: "w", handle: "xm1.S", pattern: "x", offset: 400 }, undefined);
		const o = new URL(seen, "http://x");
		assert.equal(o.searchParams.get("offset"), "400");
		assert.equal(o.searchParams.get("start_line"), null);
	});
	test("a resumed search without start_line surfaces the server's refusal", async () => {
		handler = (req, res) => {
			const u = new URL(req.url ?? "", "http://x");
			res.statusCode = u.searchParams.get("offset") && !u.searchParams.get("start_line") ? 400 : 200;
			res.end(JSON.stringify({ error: "invalid evidence search: resuming at an offset needs start_line", reason: "invalid_search" }));
		};
		await assert.rejects(expand(cfg(), { workspace_id: "w", handle: "xm1.S", query: "x", offset: 10 }, undefined), /evidence\/search/);
	});
	test("search refusals are explicit errors", async () => {
		handler = (_req, res) => {
			res.statusCode = 403;
			res.end(JSON.stringify({ error: "evidence access denied", reason: "denied" }));
		};
		await assert.rejects(expand(cfg(), { workspace_id: "w", handle: "xm1.S", lines: "1-5" }, undefined), /evidence\/search/);
	});
});

// ---- WS-24: built-in projection through capture, caller-scoped tools ----------------

const observation = (over: Record<string, unknown> = {}) => ({
	delivery: DELIVERY_VERSION,
	tool: "bash",
	status: 0,
	is_error: false,
	content_type: "text/plain; charset=utf-8",
	reduced: true,
	projection: "[xmustard test] xm-test/1 exit=1 failed=1\\n--- FAIL: TestParse",
	handle: "xm1.CAPTURED",
	raw_bytes: 60_000,
	raw_sha256: "cd",
	projected_bytes: 60,
	reducer: "xm-test/1",
	omissions: [{ kind: "lines" }],
	page_size: 65536,
	projection_mode: "text",
	captured_identity: "unknown",
	expires_at: "2026-09-26T00:00:00Z",
	family: "test",
	target_bytes: 32768,
	shape: { client: "pi", shape: "pi.tool_result", mode: "replace" },
	...over,
});

interface Seen {
	method: string;
	path: string;
	query: URLSearchParams;
	body: string;
}

// captureServer answers capture and whoami requests and records them; retained
// originals are kept by handle, as Go's store would keep them.
function captureServer(over: (seen: Seen, n: number) => { status?: number; body?: unknown } | undefined = () => undefined) {
	const seen: Seen[] = [];
	const retained = new Map<string, string>();
	handler = (req, res, body) => {
		const u = new URL(req.url ?? "", "http://x");
		const s: Seen = { method: req.method ?? "", path: u.pathname, query: u.searchParams, body: body.toString("utf8") };
		seen.push(s);
		const custom = over(s, seen.length);
		if (custom) {
			res.writeHead(custom.status ?? 200, { "Content-Type": "application/json" }).end(JSON.stringify(custom.body ?? {}));
			return;
		}
		if (u.pathname === "/api/workspaces") return res.end(JSON.stringify([{ workspace_id: "w1", root_path: "/" }]));
		if (u.pathname.endsWith("/evidence/capture")) {
			const handle = `xm1.H${seen.length}`;
			retained.set(handle, s.body);
			return res.end(JSON.stringify(observation({ handle, tool: u.searchParams.get("tool"), raw_bytes: body.length, is_error: u.searchParams.get("is_error") === "true" })));
		}
		res.writeHead(404).end("{}");
	};
	return { seen, retained, captures: () => seen.filter((x) => x.path.endsWith("/evidence/capture")) };
}

const bigText = (n: number, word = "ok") => Array.from({ length: n }, (_, i) => `${word} line ${i}`).join("\n");

describe("built-in tool projection through capture", () => {
	const resolve = async () => "w1";
	test("config: built-ins and the lower-only target", () => {
		const d = loadConfig({});
		assert.deepEqual([...d.builtins].sort(), ["bash", "edit", "find", "grep", "ls", "read", "write"]);
		assert.equal(d.projectionTarget, PI_POLICY_TARGET);
		const c = loadConfig({ XMUSTARD_PI_BUILTINS: "bash, read,nope", XMUSTARD_PI_PROJECTION_TARGET_BYTES: "8192" });
		assert.deepEqual([...c.builtins].sort(), ["bash", "read"]);
		assert.equal(c.projectionTarget, 8192);
		assert.equal(loadConfig({ XMUSTARD_PI_PROJECTION_TARGET_BYTES: "999999" }).projectionTarget, PI_POLICY_TARGET, "never raised");
		assert.equal(loadConfig({ XMUSTARD_PI_PROJECTION_TARGET_BYTES: "100" }).projectionTarget, PI_POLICY_TARGET, "below 1 KiB refused");
		assert.equal(loadConfig({ XMUSTARD_PI_BUILTINS: "none" }).builtins.size, 0);
	});
	test("a large result is replaced by its projection with a handle; isError and details are kept", async () => {
		const srv = captureServer();
		const text = bigText(4000, "--- PASS");
		const details = { truncation: { truncated: true, totalLines: 9000 }, fullOutputPath: "/tmp/pi-bash-1.log" };
		let handles = 0;
		const out = await projectBuiltin(
			cfg(),
			new Capturer(cfg()),
			resolve,
			{ toolCallId: "b1", toolName: "bash", input: { command: "go test ./..." }, content: [{ type: "text", text }], details, isError: true },
			{ sessionId: "s1" },
			() => handles++,
		);
		const [cap] = srv.captures();
		assert.equal(cap.path, "/api/workspaces/w1/evidence/capture");
		assert.equal(cap.query.get("format"), "pi");
		assert.equal(cap.query.get("client"), "pi");
		assert.equal(cap.query.get("is_error"), "true");
		assert.equal(cap.query.get("session_id"), "s1");
		assert.equal(cap.query.get("target"), null, "the Pi policy target is Go's default");
		const posted = JSON.parse(cap.body);
		assert.deepEqual(posted, { type: "tool_result", toolName: "bash", toolCallId: "b1", sessionId: "s1", input: { command: "go test ./..." }, content: [{ type: "text", text }], isError: true });
		assert.ok(!("details" in posted), "details are not model-visible and are not captured");
		assert.ok(out?.content);
		const [proj, footer] = out.content[0].text.split("\n[xmustard evidence] ");
		assert.equal(proj, observation().projection);
		assert.equal(JSON.parse(footer).handle, "xm1.H1");
		assert.equal(JSON.parse(footer).workspace_id, "w1");
		assert.equal((out as { isError?: boolean }).isError, undefined, "isError is never touched");
		const d = out.details as Record<string, any>;
		assert.deepEqual(d.truncation, details.truncation);
		assert.equal(d.fullOutputPath, details.fullOutputPath);
		assert.equal(d.xmustard.path, "capture");
		assert.equal(d.xmustard.handle, "xm1.H1");
		assert.equal(d.xmustard.family, "test");
		assert.equal(handles, 1);
		assert.ok(!("xmustard" in details), "the tool's own details object is not mutated");
	});
	test("small, image and non-built-in results never reach Go", async () => {
		const srv = captureServer();
		const c = new Capturer(cfg());
		const small = await projectBuiltin(cfg(), c, resolve, { toolCallId: "s", toolName: "read", input: {}, content: [{ type: "text", text: "x".repeat(PI_POLICY_TARGET) }], details: undefined, isError: false }, {}, () => {});
		const img = await projectBuiltin(
			cfg(),
			c,
			resolve,
			{ toolCallId: "i", toolName: "read", input: {}, content: [{ type: "text", text: bigText(9000) }, { type: "image", data: "AAAA", mimeType: "image/png" }], details: undefined, isError: false },
			{},
			() => {},
		);
		const other = await projectBuiltin(cfg({ builtins: new Set(["bash"]) }), c, resolve, { toolCallId: "o", toolName: "read", input: {}, content: [{ type: "text", text: bigText(9000) }], details: undefined, isError: false }, {}, () => {});
		assert.deepEqual([small, img, other], [undefined, undefined, undefined]);
		assert.equal(srv.seen.length, 0);
	});
	test("capture unavailable: Pi's result stands, the reason is recorded, and capture pauses", async () => {
		let now = 1_000;
		const srv = captureServer(() => ({ status: 503, body: { reason: "redaction_unavailable", error: "capture is disabled until a streaming secret redactor is configured" } }));
		const c = new Capturer(cfg(), () => now);
		const ev = { toolCallId: "u", toolName: "bash", input: { command: "yes" }, content: [{ type: "text", text: bigText(9000) }], details: undefined, isError: false };
		const out = await projectBuiltin(cfg(), c, resolve, ev, {}, () => assert.fail("no handle"));
		assert.equal(out?.content, undefined, "content untouched");
		assert.match((out?.details as any).xmustard.reason, /503 redaction_unavailable/);
		assert.equal(srv.captures().length, 1);
		const again = await projectBuiltin(cfg(), c, resolve, ev, {}, () => {});
		assert.match((again?.details as any).xmustard.reason, /capture paused/);
		assert.equal(srv.captures().length, 1, "paused: no second request");
		now += CAPTURE_PAUSE_MS + 1;
		await projectBuiltin(cfg(), c, resolve, ev, {}, () => {});
		assert.equal(srv.captures().length, 2, "retried after the pause");
	});
	test("an unshapable capture or an unresolved workspace keeps Pi's result", async () => {
		captureServer((s) => (s.path.endsWith("/capture") ? { body: observation({ shape: { client: "pi", shape: "pi.tool_result", mode: "fallback_original", reason: "status members" } }) } : undefined));
		const ev = { toolCallId: "f", toolName: "grep", input: { pattern: "x" }, content: [{ type: "text", text: bigText(9000) }], details: { matchLimitReached: 100 }, isError: false };
		const out = await projectBuiltin(cfg(), new Capturer(cfg()), resolve, ev, {}, () => assert.fail("no handle"));
		assert.equal(out?.content, undefined);
		assert.equal((out?.details as any).matchLimitReached, 100);
		assert.match((out?.details as any).xmustard.reason, /fallback_original/);
		const noWs = await projectBuiltin(cfg(), new Capturer(cfg()), async () => Promise.reject(new Error("no workspace resolved")), ev, {}, () => {});
		assert.equal(noWs?.content, undefined);
		assert.match((noWs?.details as any).xmustard.reason, /no workspace resolved/);
	});
	test("a lowered projection target is sent to Go", async () => {
		const srv = captureServer();
		await projectBuiltin(cfg({ projectionTarget: 4096 }), new Capturer(cfg()), resolve, { toolCallId: "t", toolName: "ls", input: {}, content: [{ type: "text", text: bigText(600) }], details: undefined, isError: false }, {}, () => {});
		assert.equal(srv.captures()[0].query.get("target"), "4096");
	});
	test("whoami scopes the nine tools; any failure keeps them all", async () => {
		handler = (req, res) => res.end(JSON.stringify({ id: "rita", tools: ["ground", "recall", "search"] }));
		assert.deepEqual([...((await callerTools(cfg())) ?? [])], ["ground", "recall", "search"]);
		handler = (_q, res) => res.writeHead(401).end('{"error":"authentication required"}');
		assert.equal(await callerTools(cfg()), undefined);
		handler = (_q, res) => res.end('{"id":"old-api"}');
		assert.equal(await callerTools(cfg()), undefined);
	});
});
