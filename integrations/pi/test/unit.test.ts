// Unit tests for the adapter's pure modules against an in-process HTTP server. These
// check transport behavior (bounds, deadlines, abort forwarding, auth header) and the
// delivery state machine; the real Pi + Go behavior is covered by test/e2e.

import assert from "node:assert/strict";
import http from "node:http";
import type net from "node:net";
import { after, before, describe, test } from "node:test";
import { type AdapterConfig, loadConfig } from "../src/config.ts";
import {
	argsDigest,
	DELIVERY_HEADER,
	DELIVERY_VERSION,
	type Delivery,
	INLINE_LIMIT,
	PendingCalls,
	projectResult,
	renderPage,
	runTool,
} from "../src/delivery.ts";
import { send, XmustardHttpError } from "../src/http.ts";
import { TOOL_SPECS, toJsonSchema } from "../src/tools.ts";

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
			spec("search").build({ workspace_id: "w s/1", query: "a b&c!*'()", mode: "pattern" }).path,
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
				workspace_id: { type: "string", description: "the workspace id" },
				query: { type: "string", description: "the search query" },
				mode: { type: "string", description: "hybrid (default) or pattern (ast-grep)", enum: ["hybrid", "pattern"] },
				lang: { type: "string", description: "language hint for pattern mode" },
				seed: { type: "string", description: "symbol to anchor the graph-proximity lane" },
			},
			required: ["workspace_id", "query"],
			additionalProperties: false,
		});
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
		const r = await runTool(cfg(), spec("search"), { workspace_id: "w", query: "q" }, { toolCallId: "c2", sessionId: "s" }, undefined, pending);
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
