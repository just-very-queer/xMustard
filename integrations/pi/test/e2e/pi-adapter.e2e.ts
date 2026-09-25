// End-to-end conformance for the Pi adapter: the pinned `pi` CLI (real extension
// loader, agent loop, tool_result hooks and active-tool control) drives the adapter
// against a real xMustard API + Rust core on a temporary repo, data dir and random
// port. The model is pi-ai's faux transport scripted by test/fixtures; no provider is
// contacted and no credential is read. Run through scripts/e2e/pi-adapter.sh.

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { appendFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, beforeEach, describe, test } from "node:test";
import {
	Api,
	footerOf,
	freePort,
	makeRepo,
	mcpToolsList,
	mintToken,
	type PiRun,
	pageHeaderOf,
	PgFixture,
	REPO_ROOT,
	runPi,
	sampler,
	StallProxy,
	type Step,
	seedFailedRun,
	textOf,
	toolEnds,
	toolResultMessages,
} from "./harness.ts";

const NINE = ["ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"];
const PAGE = 64 << 10;
const T = process.env.XM_E2E_DIR ?? mkdtempSync(join(tmpdir(), "xm-pi-e2e-"));
const repo = join(T, "repo");
const summary: Record<string, unknown> = { dir: T };
let api: Api;
let ws = "";
let runSeq = 0;
// Native Postgres is required for the diagnostics tool to succeed; the script sets
// XM_PG_BIN_DIR, or XM_E2E_ALLOW_NO_POSTGRES=1 to accept diagnostics as error-only.
const pgBin = process.env.XM_PG_BIN_DIR;
let pg: PgFixture | undefined;

const record = (k: string, v: unknown) => {
	summary[k] = v;
	writeFileSync(join(T, "summary.json"), JSON.stringify(summary, null, 2));
};

async function loadWorkspace(a: Api): Promise<string> {
	const t0 = Date.now();
	const r = await a.json("POST", "/api/workspaces/load", { root_path: repo, auto_scan: true });
	assert.equal(r.status, 200, `workspace load: ${r.text.slice(0, 300)}`);
	record("workspace_load_scan_ms", Date.now() - t0);
	return r.body.workspace.workspace_id as string;
}

// seedDataDir gives another API instance the already-scanned workspace record (a scan
// of the 1500-file fixture takes about two minutes); the repository is the same.
function seedDataDir(dir: string): void {
	mkdirSync(join(dir, "workspaces"), { recursive: true });
	cpSync(join(T, "data", "workspaces", ws), join(dir, "workspaces", ws), { recursive: true });
	cpSync(join(T, "data", "workspaces.json"), join(dir, "workspaces.json"));
}

// setUpDiagnostics points the API at the fixture database through the existing
// settings and bootstrap routes, then materializes one LSP diagnostic through the
// existing diagnostics/run route (Rust normalizes, Go persists).
async function setUpDiagnostics(fixture: PgFixture): Promise<void> {
	mkdirSync(join(T, "sql"), { recursive: true }); // the API reads <data>/../sql
	cpSync(join(REPO_ROOT, "backend", "sql", "001_repo_cockpit_postgres.sql"), join(T, "sql", "001_repo_cockpit_postgres.sql"));
	const current = await api.json("GET", "/api/settings");
	const set = await api.json("POST", "/api/settings", { ...current.body, postgres_dsn: fixture.dsn, postgres_schema: "xmustard" });
	assert.equal(set.status, 200, set.text);
	const boot = await api.json("POST", "/api/postgres/bootstrap", { dsn: fixture.dsn, schema_name: "xmustard" });
	assert.equal(boot.status, 200, boot.text);
	const input = join(T, "diagnostics-lsp.json");
	writeFileSync(
		input,
		JSON.stringify({
			uri: `file://${join(repo, "pkg", "handler_0007.go")}`,
			diagnostics: [
				{ range: { start: { line: 5, character: 8 }, end: { line: 5, character: 13 } }, severity: 1, code: "XME2E", source: "gopls", message: "xm_e2e_diagnostic: negative input panics" },
			],
		}),
	);
	const run = await api.json("POST", `/api/workspaces/${ws}/diagnostics/run`, { input_path: input, source_kind: "lsp", source_name: "gopls-e2e-fixture" });
	assert.equal(run.status, 200, run.text.slice(0, 500));
	record("postgres_fixture", { status: "native", port: fixture.port, bootstrap: boot.status, diagnostics_run: run.status });
}

type PiOpts = Pick<Parameters<typeof runPi>[0], "env" | "rpc" | "rpcManual" | "settings" | "timeoutMs"> & { apiBase?: string; ws?: string };
function pi(name: string, steps: Step[], o: PiOpts = {}): Promise<PiRun> {
	runSeq++;
	return runPi({ dir: join(T, "pi", `${String(runSeq).padStart(2, "0")}-${name}`), cwd: repo, apiBase: o.apiBase ?? api.base, ws: o.ws ?? ws, steps, ...o });
}

const call = (name: string, args: Record<string, unknown> = {}) => ({ name, args: { workspace_id: "$WS", ...args } });
const byName = (run: PiRun, name: string) => toolResultMessages(run, name);
const one = (run: PiRun, name: string) => {
	const m = byName(run, name);
	assert.equal(m.length, 1, `${name}: expected one result, got ${m.length}\n${run.stderr.slice(-2000)}`);
	return m[0];
};

// Every finalized tool result must reach the next model request byte-for-byte, with
// its error flag: this is what "the hook result is what the model sees" means.
function assertModelSawResults(run: PiRun): void {
	const seen = new Map<string, { text: string; isError: boolean }>();
	for (const line of run.trace) for (const r of line.tool_results) seen.set(r.toolCallId, r);
	for (const m of toolResultMessages(run)) {
		const s = seen.get(m.toolCallId);
		assert.ok(s, `model never received ${m.toolName} ${m.toolCallId}`);
		assert.equal(s.text, textOf(m), `${m.toolName}: model text differs from finalized result`);
		assert.equal(s.isError, m.isError, `${m.toolName}: error flag differs`);
	}
}

// pagesOf reassembles xmustard_expand pages (tool details carry the exact base64).
function pagesOf(run: PiRun, handle: string) {
	return toolEnds(run, "xmustard_expand")
		.filter((e) => !e.isError && e.result.details?.handle === handle)
		.map((e) => e.result.details);
}
const sha = (b: Buffer) => createHash("sha256").update(b).digest("hex");

async function readAll(a: Api, w: string, handle: string, token?: string): Promise<{ status: number; bytes: Buffer; pages: any[] }> {
	const parts: Buffer[] = [];
	const pages: any[] = [];
	let off = 0;
	for (;;) {
		const r = await a.json("GET", `/api/workspaces/${w}/evidence/${handle}?offset=${off}`, undefined, token);
		if (r.status !== 200) return { status: r.status, bytes: Buffer.concat(parts), pages };
		pages.push(r.body);
		parts.push(Buffer.from(r.body.data, "base64"));
		if (r.body.eof) return { status: 200, bytes: Buffer.concat(parts), pages };
		off = r.body.next_offset;
	}
}

before(async () => {
	sampler.start();
	mkdirSync(join(T, "data"), { recursive: true });
	makeRepo(repo, 1500);
	api = new Api({ dataDir: join(T, "data"), logFile: join(T, "api.log") });
	await api.start();
	ws = await loadWorkspace(api);
	seedFailedRun(join(T, "data"), ws, "r-fail", T);
	record("workspace", ws);
	if (pgBin) {
		pg = new PgFixture(pgBin);
		await pg.start(T);
		await setUpDiagnostics(pg);
	} else {
		record("postgres_fixture", { status: "missing", consequence: "diagnostics success NOT covered; only its explicit error is" });
	}
});
after(async () => {
	await api?.stop();
	await pg?.stop();
	await sampler.stop();
	const resources = sampler.report();
	record("resources", resources);
	const r = resources as Record<string, any>;
	console.log(
		`sampled peak RSS (${r.samples} samples, ${r.sampling_failures.length} failures): xmustard-owned ${r.xmustard_owned.sampled_peak_mib} MiB, Pi external ${r.pi_external.sampled_peak_mib} MiB, full workflow ${r.workflow.sampled_peak_mib} MiB`,
	);
	appendFileSync(join(T, "summary.json"), "\n");
	console.log(`e2e artifacts: ${T}`);
});

beforeEach((t) => {
	sampler.phase = t.name;
});

let original: { handle: string; bytes: Buffer; footer: Record<string, any> } | undefined;

describe("Pi adapter against the real xMustard API", () => {
	test("conformance: Pi sends the model exactly the MCP tool schemas; expand starts inactive", async () => {
		const mcp = await mcpToolsList(api.base);
		const run = await pi("conformance", [{ text: "ok" }]);
		assert.equal(run.code, 0, run.stderr);
		const first = run.trace[0];
		assert.ok(first?.tools, "trace recorded tool definitions");
		for (const t of mcp) {
			const got: { description: string; parameters: unknown } | undefined = first.tools?.find((x) => x.name === t.name);
			assert.ok(got, `Pi does not expose ${t.name}`);
			assert.equal(got.description, t.description, `${t.name} description`);
			assert.deepEqual(JSON.parse(JSON.stringify(got.parameters)), t.inputSchema, `${t.name} schema`);
		}
		assert.deepEqual(mcp.map((t) => t.name).sort(), [...NINE].sort());
		assert.ok(!first.active_tools.includes("xmustard_expand"), "xmustard_expand must start inactive");
		for (const n of NINE) assert.ok(first.active_tools.includes(n), `${n} active`);
		record("conformance", { mcp_tools: mcp.length, pi_active: first.active_tools });
	});

	test("all nine tools run through Pi; results reach the next model request; errors stay errors", async () => {
		const run = await pi("nine-tools", [
			{
				calls: [
					call("ground"),
					call("recall"),
					call("remember", { content: "Pi e2e: app doubles its input", title: "pi-e2e", paths: "web/app.ts" }),
					call("search", { q: "xm_marker_app" }),
					call("explain", { path: "web/app.ts" }),
					call("impact", { symbol: "Handler7" }),
					call("diagnostics"),
					call("why_failed", { run_id: "r-fail" }),
				],
			},
			{ calls: [call("verify", { entry_id: "$JSON:remember:id" }), call("why_failed", { run_id: "missing-run" })] },
			{ text: "done" },
		]);
		assert.equal(run.code, 0, run.stderr);
		assertModelSawResults(run);
		const outcome: Record<string, { isError: boolean; head: string }> = {};
		for (const m of toolResultMessages(run)) outcome[`${m.toolName}${m.isError ? "!" : ""}`] = { isError: m.isError, head: textOf(m).slice(0, 160) };
		record("nine_tools", outcome);
		for (const n of NINE) assert.ok(byName(run, n).length >= 1, `${n} ran`);
		assert.equal(one(run, "ground").isError, false);
		assert.ok(JSON.parse(textOf(one(run, "ground"))).workspace_id === ws);
		assert.equal(one(run, "recall").isError, false);
		const rem = JSON.parse(textOf(one(run, "remember")));
		// this API runs without auth (open mode): a proposal is promoted at once but
		// labelled self-asserted, never peer-verified
		assert.equal(rem.status, "verified");
		assert.equal(rem.verification_mode, "self_asserted_open_mode");
		assert.match(textOf(one(run, "search")), /xm_marker_app/);
		assert.equal(one(run, "explain").isError, false);
		assert.equal(one(run, "impact").isError, false);
		const wf = byName(run, "why_failed");
		const ok = wf.find((m) => !m.isError);
		const missing = wf.find((m) => m.isError);
		assert.ok(ok && /TestHandler7|xm_marker/.test(textOf(ok)), "why_failed explains the seeded failure");
		assert.ok(missing && /404|Missing/.test(textOf(missing)), "missing run stays an error");
		const verify = one(run, "verify");
		assert.equal(verify.isError, false, textOf(verify).slice(0, 300));
		assert.equal(JSON.parse(textOf(verify)).id, rem.id, "verify answered for the remembered entry");
		const diag = one(run, "diagnostics");
		if (pgBin) {
			assert.equal(diag.isError, false, textOf(diag).slice(0, 300));
			const d = JSON.parse(textOf(diag));
			const hit = d.diagnostics.find((x: any) => /xm_e2e_diagnostic/.test(x.message));
			assert.ok(hit, `seeded diagnostic delivered: ${textOf(diag).slice(0, 400)}`);
			assert.match(JSON.stringify(hit), /pkg\/handler_0007\.go/);
			record("diagnostics_success", { path: hit.path ?? hit.file_path, line: hit.range_start_line, message: hit.message });
		} else {
			assert.equal(diag.isError, true);
			assert.match(textOf(diag), /Postgres DSN is required/, "diagnostics error is explicit");
		}
		assert.ok(!run.trace.at(-1)?.active_tools.includes("xmustard_expand"), "no handle issued, so expand stays inactive");
	});

	test("a large result is projected at the source (bound identity); xmustard_expand activates and pages 64 KiB exactly", async () => {
		const run = await pi("expand-source", [{ calls: [call("impact")] }, { expand_all: { workspace_id: "$WS" } }, { text: "done" }]);
		assert.equal(run.code, 0, run.stderr);
		assertModelSawResults(run);
		const impact = one(run, "impact");
		const footer = footerOf(textOf(impact)) ?? assert.fail(`no evidence footer: ${textOf(impact).slice(-400)}`);
		assert.ok(footer.handle?.startsWith("xm1."), `impact footer: ${textOf(impact).slice(-400)}`);
		assert.equal(footer.captured_identity, "bound");
		assert.ok(footer.raw_bytes > 3 * PAGE, `original is multi-page (${footer.raw_bytes})`);
		assert.ok(Buffer.byteLength(textOf(impact)) <= PAGE + 2048, "projection is bounded");
		assert.equal(impact.details?.path, "source");
		// activation happens between the impact result and the next model request
		const idx = run.trace.findIndex((l) => l.tool_results.some((r) => r.toolName === "impact"));
		assert.ok(!run.trace[idx - 1]?.active_tools.includes("xmustard_expand"));
		assert.ok(run.trace[idx].active_tools.includes("xmustard_expand"));
		const pages = pagesOf(run, footer.handle);
		assert.equal(pages.length, Math.ceil(footer.raw_bytes / PAGE));
		let off = 0;
		for (const p of pages) {
			assert.equal(p.offset, off);
			assert.ok(p.length <= PAGE);
			assert.equal(p.freshness, "current");
			assert.equal(p.stale, false);
			off = p.next_offset;
		}
		const bytes = Buffer.concat(pages.map((p) => Buffer.from(p.data_base64, "base64")));
		assert.equal(bytes.length, footer.raw_bytes);
		assert.equal(sha(bytes), footer.raw_sha256);
		for (const m of byName(run, "xmustard_expand")) assert.ok(pageHeaderOf(textOf(m)), "model sees a page header");
		original = { handle: footer.handle, bytes, footer };
		record("expand_source", { raw_bytes: footer.raw_bytes, projected_text_bytes: Buffer.byteLength(textOf(impact)), pages: pages.length, captured_identity: footer.captured_identity, encodings: pages.map((p) => p.text_encoding) });
	});

	test("repository mutation: the old capture is served unchanged and labelled stale", async () => {
		assert.ok(original);
		appendFileSync(join(repo, "pkg", "handler_0001.go"), "\n// mutated by e2e\n");
		const run = await pi("stale", [{ calls: [call("impact")] }, { calls: [call("xmustard_expand", { handle: "$ENV:XM_OLD_HANDLE", offset: 0 })] }, { text: "done" }], {
			env: { XM_OLD_HANDLE: original.handle },
		});
		assert.equal(run.code, 0, run.stderr);
		const fresh = footerOf(textOf(one(run, "impact")));
		assert.notEqual(fresh?.captured_key, original.footer.captured_key, "new capture has the mutated identity");
		const [page] = pagesOf(run, original.handle);
		assert.ok(page, "old handle expanded");
		assert.equal(page.freshness, "stale");
		assert.equal(page.stale, true);
		assert.equal(page.captured_key, original.footer.captured_key);
		assert.notEqual(page.current_key, page.captured_key);
		assert.deepEqual(Buffer.from(page.data_base64, "base64"), original.bytes.subarray(0, page.length), "captured bytes, not current content");
		assert.match(pageHeaderOf(textOf(one(run, "xmustard_expand")))?.freshness ?? "", /stale/);
		record("stale", { captured_key: page.captured_key, current_key: page.current_key });
	});

	test("hook delivery: POSTed results are projected by Go with unknown provenance, never fresh", async () => {
		const proxy = new StallProxy(api.base, () => false);
		await proxy.start();
		try {
			const run = await pi("hook", [{ calls: [call("impact"), call("why_failed", { run_id: "missing-run" })] }, { expand_all: { workspace_id: "$WS" } }, { text: "done" }], {
				apiBase: proxy.base,
				env: { XMUSTARD_PI_DELIVERY: "hook" },
			});
			assert.equal(run.code, 0, run.stderr);
			assertModelSawResults(run);
			const impact = one(run, "impact");
			assert.equal(impact.details?.path, "hook");
			const footer = footerOf(textOf(impact));
			assert.equal(footer?.captured_identity, "unknown");
			const pages = pagesOf(run, footer?.handle);
			assert.ok(pages.length > 1);
			for (const p of pages) {
				assert.equal(p.freshness, "unknown");
				assert.equal(p.stale, true);
			}
			assert.equal(sha(Buffer.concat(pages.map((p) => Buffer.from(p.data_base64, "base64")))), footer?.raw_sha256);
			const missing = one(run, "why_failed");
			assert.equal(missing.isError, true);
			assert.equal(missing.details?.path, "hook");
			assert.equal(proxy.seen.filter((s) => s.startsWith("POST") && s.endsWith("/evidence")).length, 2, "both results posted for projection");
			record("hook", { captured_identity: footer?.captured_identity, freshness: pages[0].freshness, pages: pages.length });
		} finally {
			await proxy.stop();
		}
	});

	test("concurrency: two concurrent Pi processes, four calls per turn, get distinct, exact originals", async () => {
		const steps: Step[] = [{ calls: [call("impact"), call("impact"), call("impact"), call("impact")] }, { text: "done" }];
		const [a, b] = await Promise.all([pi("concurrent-a", steps), pi("concurrent-b", steps)]);
		const footers = [a, b].flatMap((r) => {
			assert.equal(r.code, 0, r.stderr);
			assertModelSawResults(r);
			return byName(r, "impact").map((m) => {
				assert.equal(m.isError, false, textOf(m).slice(0, 300));
				return footerOf(textOf(m));
			});
		});
		assert.equal(footers.length, 8);
		assert.equal(new Set(footers.map((f) => f?.handle)).size, 8, "distinct handles");
		for (const f of footers) {
			const got = await readAll(api, ws, f?.handle);
			assert.equal(got.status, 200);
			assert.equal(sha(got.bytes), f?.raw_sha256);
		}
		record("concurrency", { calls: footers.length, distinct_handles: 8 });
	});

	test("API restart: a handle issued before restart still expands to the same bytes", async () => {
		assert.ok(original);
		await api.restart();
		const run = await pi("restart", [{ calls: [call("impact")] }, { calls: [call("xmustard_expand", { handle: "$ENV:XM_OLD_HANDLE", offset: PAGE })] }, { text: "done" }], {
			env: { XM_OLD_HANDLE: original.handle },
		});
		assert.equal(run.code, 0, run.stderr);
		const [page] = pagesOf(run, original.handle);
		assert.ok(page, textOf(one(run, "xmustard_expand")).slice(0, 300));
		assert.deepEqual(Buffer.from(page.data_base64, "base64"), original.bytes.subarray(PAGE, PAGE + page.length));
		const all = await readAll(api, ws, original.handle);
		assert.equal(sha(all.bytes), original.footer.raw_sha256);
		record("restart", { page_offset: page.offset, sha_ok: true });
	});

	test("expiry: an expired handle is refused explicitly and never re-served", async () => {
		seedDataDir(join(T, "data-expiry"));
		const api2 = new Api({ dataDir: join(T, "data-expiry"), logFile: join(T, "api-expiry.log"), extraEnv: { XMUSTARD_EVIDENCE_RETENTION_SECONDS: "2" } });
		await api2.start();
		try {
			const ws2 = ws;
			const run = await pi("expiry", [{ calls: [call("impact")] }, { delay_ms: 3000, calls: [call("xmustard_expand", { handle: "$HANDLE", offset: 0 })] }, { text: "done" }], {
				apiBase: api2.base,
				ws: ws2,
			});
			assert.equal(run.code, 0, run.stderr);
			const handle = footerOf(textOf(one(run, "impact")))?.handle;
			assert.ok(handle);
			const exp = one(run, "xmustard_expand");
			assert.equal(exp.isError, true);
			assert.match(textOf(exp), /410 expired|expired/);
			const again = await api2.json("GET", `/api/workspaces/${ws2}/evidence/${handle}?offset=0`);
			assert.ok(again.status === 410 || again.status === 404, `after expiry: ${again.status}`);
			assert.equal(again.body?.data, undefined);
			record("expiry", { first: textOf(exp).slice(0, 160), again: again.status });
		} finally {
			await api2.stop();
		}
	});

	test("cancellation: an RPC abort during a tool call closes the request to Go", async () => {
		const proxy = new StallProxy(api.base, (req) => (req.url ?? "").includes("/session-grounding"));
		await proxy.start();
		try {
			let aborted = 0;
			const t0 = Date.now();
			const run = await pi("abort", [{ calls: [call("ground")] }, { text: "done" }], {
				apiBase: proxy.base,
				rpc: (ev, write) => {
					if (ev.type === "tool_execution_start" && ev.toolName === "ground") setTimeout(() => write({ id: "a1", type: "abort" }), 300);
					if (ev.type === "response" && ev.command === "abort") aborted = Date.now() - t0;
				},
				timeoutMs: 30_000,
			});
			assert.ok(aborted > 0, "abort acknowledged");
			assert.equal(proxy.stalled, 1);
			assert.equal(proxy.stalledClosed, 1, "the in-flight request was closed, not left running");
			const end = toolEnds(run, "ground")[0];
			assert.equal(end?.isError, true);
			assert.ok(Date.now() - t0 < 20_000, "well before the 60 s tool deadline");
			assert.equal((await api.json("GET", "/api/health")).status, 200);
			record("abort", { acknowledged_ms: aborted, result: textOf(end.result).slice(0, 120), exit: run.code });
		} finally {
			await proxy.stop();
		}
	});

	test("tool deadline with a Pi signal present: a hung route fails at the configured timeout", async () => {
		const proxy = new StallProxy(api.base, (req) => (req.url ?? "").includes("/session-grounding"));
		await proxy.start();
		try {
			const run = await pi("tool-timeout", [{ calls: [call("ground")] }, { text: "done" }], { apiBase: proxy.base, env: { XMUSTARD_PI_TOOL_TIMEOUT_MS: "1500" } });
			assert.equal(run.code, 0, run.stderr);
			const g = one(run, "ground");
			assert.equal(g.isError, true);
			assert.match(textOf(g), /timed out after 1500 ms/);
			assert.equal(proxy.stalledClosed, 1);
			record("tool_timeout", textOf(g));
		} finally {
			await proxy.stop();
		}
	});

	test("projection deadline: small originals pass through exactly, large ones become an explicit size error", async () => {
		const proxy = new StallProxy(api.base, (req) => req.method === "POST" && (req.url ?? "").split("?")[0].endsWith("/evidence"));
		await proxy.start();
		try {
			const run = await pi("projection-timeout", [{ calls: [call("impact"), call("recall")] }, { text: "done" }], {
				apiBase: proxy.base,
				env: { XMUSTARD_PI_DELIVERY: "hook", XMUSTARD_PI_PROJECTION_TIMEOUT_MS: "1000" },
			});
			assert.equal(run.code, 0, run.stderr);
			assertModelSawResults(run);
			const big = one(run, "impact");
			assert.equal(big.isError, true);
			assert.match(textOf(big), /inline limit.*timed out after 1000 ms/);
			const small = one(run, "recall");
			assert.equal(small.isError, false);
			assert.ok(Array.isArray(JSON.parse(textOf(small)).entries), "original recall JSON preserved");
			assert.match(small.details?.projection_error ?? "", /timed out after 1000 ms/);
			assert.equal(proxy.stalledClosed, 2, "both projection requests were closed at the deadline");
			record("projection_timeout", { big: textOf(big).slice(0, 200), small_preserved: true });
		} finally {
			await proxy.stop();
		}
	});

	test("unreachable Go: xMustard tools fail explicitly, expand is not callable while inactive, other tools pass through", async () => {
		const dead = `http://127.0.0.1:${await freePort()}`;
		const run = await pi("unreachable", [{ calls: [call("ground"), call("search", { q: "x" }), call("xmustard_expand", { handle: "xm1.x" }), { name: "read", args: { path: "README.md" } }] }, { text: "done" }], {
			apiBase: dead,
		});
		assert.equal(run.code, 0, run.stderr);
		for (const n of ["ground", "search"]) {
			const m = one(run, n);
			assert.equal(m.isError, true);
			assert.match(textOf(m), /unreachable/);
		}
		assert.equal(one(run, "xmustard_expand").isError, true, "inactive tool is rejected by Pi");
		assert.equal(textOf(one(run, "read")), readFileSync(join(repo, "README.md"), "utf8"));
		record("unreachable", { ground: textOf(one(run, "ground")), expand: textOf(one(run, "xmustard_expand")) });
	});

	test("activated xmustard_expand whose Go endpoint fails: explicit errors, never substituted content", async () => {
		const isPage = (req: { method?: string; url?: string }) => req.method === "GET" && /\/evidence\/xm1\./.test(req.url ?? "");
		// first page read: connection reset; second: hangs past the (lowered) tool deadline
		let pageReads = 0;
		const proxy = new StallProxy(
			api.base,
			(req) => isPage(req) && pageReads === 2,
			(req) => isPage(req) && ++pageReads === 1,
		);
		await proxy.start();
		try {
			const run = await pi(
				"expand-endpoint-failure",
				[
					{ calls: [call("impact")] },
					{ calls: [call("xmustard_expand", { handle: "$HANDLE", offset: 0 })] },
					{ calls: [call("xmustard_expand", { handle: "$HANDLE", offset: 0 })] },
					{ calls: [call("xmustard_expand", { handle: "$HANDLE", offset: 0 })] },
					{ text: "done" },
				],
				{ apiBase: proxy.base, env: { XMUSTARD_PI_TOOL_TIMEOUT_MS: "1500" } },
			);
			assert.equal(run.code, 0, run.stderr);
			assertModelSawResults(run);
			const handle = footerOf(textOf(one(run, "impact")))?.handle;
			assert.ok(handle, "impact issued a handle");
			const expands = byName(run, "xmustard_expand");
			assert.equal(expands.length, 3);
			// every expand call was made while the tool was active (activated by the handle)
			for (const line of run.trace.slice(1)) assert.ok(line.active_tools.includes("xmustard_expand"));
			const [reset, hung, recovered] = expands;
			assert.equal(reset.isError, true);
			assert.match(textOf(reset), /unreachable/);
			assert.equal(hung.isError, true);
			assert.match(textOf(hung), /timed out after 1500 ms/);
			assert.equal(proxy.dropped, 1);
			assert.equal(proxy.stalledClosed, 1, "hung page request closed at the deadline");
			for (const m of [reset, hung]) assert.ok(!pageHeaderOf(textOf(m)), "no page content on failure");
			assert.equal(recovered.isError, false, "endpoint back: the same handle reads again");
			assert.equal(pageHeaderOf(textOf(recovered))?.offset, 0);
			record("expand_endpoint_failure", { reset: textOf(reset), hung: textOf(hung) });
		} finally {
			await proxy.stop();
		}
	});

	test("small built-in results pass through untouched and never reach Go", async () => {
		const proxy = new StallProxy(api.base, () => false);
		await proxy.start();
		try {
			const run = await pi(
				"passthrough",
				[{ calls: [{ name: "read", args: { path: "web/app.ts" } }, { name: "read", args: { path: "no-such-file.txt" } }, { name: "bash", args: { command: "printf pass-through" } }] }, { text: "done" }],
				{ apiBase: proxy.base },
			);
			assert.equal(run.code, 0, run.stderr);
			assertModelSawResults(run);
			const reads = byName(run, "read");
			const okRead = reads.find((m) => !m.isError);
			const badRead = reads.find((m) => m.isError);
			assert.ok(okRead, "a successful host-native read result exists");
			assert.equal(textOf(okRead), readFileSync(join(repo, "web/app.ts"), "utf8"), "successful read is byte-identical");
			assert.ok(badRead, "a failing host-native read result exists");
			assert.ok(!textOf(badRead).includes("[xmustard"), "failed read untouched");
			const bash = one(run, "bash");
			assert.equal(bash.isError, false);
			assert.equal(textOf(bash).trim(), "pass-through");
			for (const m of toolResultMessages(run)) assert.ok(!m.details?.path && !m.details?.xmustard, `${m.toolName} carries no xMustard details`);
			// the only request is the session-start tool scoping (whoami); small results
			// are not posted for capture (Go would return them unchanged)
			assert.deepEqual(proxy.seen, ["GET /api/auth/whoami"], "no xMustard request for small built-in results");
			record("passthrough", { read_error: textOf(badRead).slice(0, 120) });
		} finally {
			await proxy.stop();
		}
	});

	test("auth: bearer token required, handles bound to the principal, rotation keeps access, other principals denied", async () => {
		const dir = join(T, "data-auth");
		seedDataDir(dir);
		const admin = mintToken(dir, "e2e-admin", "admin");
		const a1 = mintToken(dir, "agent-a");
		const b = mintToken(dir, "agent-b");
		const api3 = new Api({ dataDir: dir, logFile: join(T, "api-auth.log"), extraEnv: { XMUSTARD_AUTH: "required" } });
		await api3.start();
		const secrets = [admin, a1, b];
		const runs: PiRun[] = [];
		try {
			const ws3 = ws;
			const listed = await api3.json("GET", "/api/workspaces", undefined, admin);
			assert.ok(listed.text.includes(ws), "seeded workspace visible under auth");
			const go = async (name: string, steps: Step[], token?: string, extra: Record<string, string> = {}) => {
				const r = await pi(name, steps, { apiBase: api3.base, ws: ws3, env: { ...(token ? { XMUSTARD_TOKEN: token } : {}), ...extra } });
				assert.equal(r.code, 0, r.stderr);
				runs.push(r);
				return r;
			};
			const anon = await go("auth-none", [{ calls: [call("ground")] }, { text: "done" }]);
			assert.equal(one(anon, "ground").isError, true);
			assert.match(textOf(one(anon, "ground")), /401/);

			const ra = await go("auth-a", [{ calls: [call("impact")] }, { expand_all: { workspace_id: "$WS" }, max_pages: 1 }, { text: "done" }], a1);
			const fa = footerOf(textOf(one(ra, "impact")));
			assert.ok(fa?.handle);
			assert.equal(pagesOf(ra, fa.handle).length, 1);

			const a2 = mintToken(dir, "agent-a"); // rotation: replaces agent-a's token
			secrets.push(a2);
			const stale = await go("auth-a-old", [{ calls: [call("ground")] }, { text: "done" }], a1);
			assert.equal(one(stale, "ground").isError, true, "rotated-out token is refused");
			const rotated = await go("auth-a-rotated", [{ calls: [call("impact")] }, { calls: [call("xmustard_expand", { handle: "$ENV:XM_OLD_HANDLE", offset: 0 })] }, { text: "done" }], a2, { XM_OLD_HANDLE: fa.handle });
			assert.equal(one(rotated, "xmustard_expand").isError, false, textOf(one(rotated, "xmustard_expand")).slice(0, 200));

			const other = await go("auth-b", [{ calls: [call("impact")] }, { calls: [call("xmustard_expand", { handle: "$ENV:XM_OLD_HANDLE", offset: 0 })] }, { text: "done" }], b, { XM_OLD_HANDLE: fa.handle });
			const denied = one(other, "xmustard_expand");
			assert.equal(denied.isError, true);
			assert.match(textOf(denied), /403|denied/);

			// caller-scoped tools: a reader token is not offered remember/verify (whoami tools)
			const reader = mintToken(dir, "reader-r", "readonly");
			secrets.push(reader);
			const scoped = await go("auth-reader", [{ text: "ok" }], reader);
			const active = scoped.trace[0]?.active_tools ?? [];
			for (const n of ["ground", "recall", "search", "explain", "impact", "diagnostics", "why_failed"]) assert.ok(active.includes(n), `${n} active for a reader`);
			for (const n of ["remember", "verify"]) assert.ok(!active.includes(n), `${n} must not be offered to a reader`);
			record("caller_scoped_tools", { reader_active: active.filter((n) => NINE.includes(n)) });

			for (const r of runs) for (const s of secrets) assert.ok(!r.stdout.includes(s) && !r.stderr.includes(s) && !JSON.stringify(r.trace).includes(s), "token leaked into Pi output");
			record("auth", { anonymous: textOf(one(anon, "ground")).slice(0, 120), rotated_old: textOf(one(stale, "ground")).slice(0, 120), other_principal: textOf(denied).slice(0, 160) });
		} finally {
			await api3.stop();
		}
	});
});

// ---- WS-24: Pi built-ins through capture, turn_end masking, snapshot compaction ------

const MASKED = "[xmustard masked: ";
const stubHandle = (text: string) => /; handle (xm1\.[A-Za-z0-9_-]+)(?:; offset \d+)?; workspace_id ([^\]\s;]+)\]/.exec(text);

describe("Pi built-ins, masking and compaction through xMustard (WS-24)", () => {
	test("built-in results above the target are projected through capture: handle, isError and details kept, redacted, recoverable", async () => {
		const big = `${Array.from({ length: 1500 }, (_, i) => `big line ${String(i).padStart(5, "0")} ${"x".repeat(30)}`).join("\n")}\n`;
		writeFileSync(join(repo, "big.txt"), big);
		const cmd = "for i in $(seq 1 3000); do printf 'ok line %05d padding-padding-padding-padding\\n' $i; done; echo '--- FAIL: TestBuiltin (0.00s)'; echo 'token XM_E2E_SECRET_abc123'; exit 3";
		const run = await pi("builtins", [
			{ calls: [{ name: "read", args: { path: "big.txt" } }, { name: "ls", args: { path: "web" } }] },
			{ calls: [{ name: "bash", args: { command: cmd } }] },
			{ expand_all: { workspace_id: "$WS" } },
			{ text: "done" },
		]);
		assert.equal(run.code, 0, run.stderr);
		assertModelSawResults(run);
		// read: projected with a handle; Pi's own truncation details are kept
		const read = one(run, "read");
		assert.equal(read.isError, false);
		const rf = footerOf(textOf(read)) ?? assert.fail(`read not projected: ${textOf(read).slice(-300)}`);
		assert.ok(rf.handle?.startsWith("xm1."));
		assert.equal(read.details?.xmustard?.path, "capture");
		assert.equal(read.details?.xmustard?.handle, rf.handle);
		assert.ok(read.details?.truncation?.truncated, "Pi's truncation details survive");
		const readOrig = await readAll(api, ws, rf.handle);
		assert.equal(sha(readOrig.bytes), rf.raw_sha256);
		const readText = readOrig.bytes.toString("utf8");
		assert.ok(big.startsWith(readText.split("\n\n[Showing lines")[0]), "the retained original is Pi's read output");
		// ls is small: untouched
		assert.ok(!one(run, "ls").details?.xmustard, "small ls untouched");
		// bash: an error stays an error; the projection keeps the failure, not the secret
		const bash = one(run, "bash");
		assert.equal(bash.isError, true);
		const bt = textOf(bash);
		const bf = footerOf(bt) ?? assert.fail(`bash not projected: ${bt.slice(-300)}`);
		assert.match(bt, /--- FAIL: TestBuiltin/);
		assert.match(bt, /Command exited with code 3/);
		assert.ok(!bt.includes("XM_E2E_SECRET_abc123"), "the secret never reaches the model");
		assert.ok(Buffer.byteLength(bt) < 34 << 10, `projection is bounded (${Buffer.byteLength(bt)} bytes)`);
		assert.equal(bash.details?.xmustard?.path, "capture");
		assert.equal(bf.captured_identity, "unknown");
		// xmustard_expand pages the (redacted) original back exactly
		const pages = pagesOf(run, bf.handle);
		assert.ok(pages.length >= 1, "expand paged the bash original");
		const orig = Buffer.concat(pages.map((p) => Buffer.from(p.data_base64, "base64")));
		assert.equal(orig.length, bf.raw_bytes);
		assert.equal(sha(orig), bf.raw_sha256);
		const ot = orig.toString("utf8");
		assert.match(ot, /--- FAIL: TestBuiltin \(0\.00s\)/);
		assert.match(ot, /token \[REDACTED:e2e\]/, "redacted before retention");
		assert.ok(!ot.includes("XM_E2E_SECRET_abc123"));
		assert.match(ot, /Command exited with code 3$/);
		// expansion activates between the read result and the next model request
		const idx = run.trace.findIndex((l) => l.tool_results.some((r) => r.toolName === "read"));
		assert.ok(!run.trace[idx - 1]?.active_tools.includes("xmustard_expand"));
		assert.ok(run.trace[idx].active_tools.includes("xmustard_expand"));
		record("builtins", {
			read: { raw_bytes: rf.raw_bytes, projected_text_bytes: Buffer.byteLength(textOf(read)), reducer: read.details?.xmustard?.reducer },
			bash: { raw_bytes: bf.raw_bytes, projected_text_bytes: Buffer.byteLength(bt), reducer: bash.details?.xmustard?.reducer, pages: pages.length },
		});
	});

	test("masking: older results become stubs only at window turns; the latest failure and edited files stay; stubs recover originals; raw entries are kept", async () => {
		writeFileSync(join(repo, "mid.txt"), `${Array.from({ length: 120 }, (_, i) => `mid line ${i} ${"y".repeat(30)}`).join("\n")}\n`);
		let entries: any[] = [];
		const run = await pi(
			"masking",
			[
				{ calls: [{ name: "bash", args: { command: "seq 1 800" } }] }, // turn 1
				{ calls: [{ name: "read", args: { path: "mid.txt" } }] }, // turn 2
				{ calls: [{ name: "bash", args: { command: "seq 1 700; exit 2" } }] }, // turn 3: the latest failure
				{ calls: [{ name: "edit", args: { path: "mid.txt", edits: [{ oldText: "mid line 0 ", newText: "mid line zero " }] } }] }, // turn 4
				{ calls: [{ name: "bash", args: { command: "seq 1 600" } }] }, // turn 5
				{ calls: [{ name: "bash", args: { command: "true" } }] }, // turn 6
				{ text: "done" }, // turn 7
			],
			{
				env: { XMUSTARD_PI_MASK_AFTER_TURNS: "2", XMUSTARD_PI_MASK_EVERY_TURNS: "2" },
				rpcManual: true,
				rpc: (ev, write, end) => {
					if (ev.type === "agent_settled") write({ id: "g1", type: "get_entries" });
					if (ev.type === "response" && ev.command === "get_entries") {
						entries = ev.data?.entries ?? ev.data ?? [];
						end();
					}
				},
			},
		);
		assert.equal(run.code, 0, run.stderr);
		const results = toolResultMessages(run);
		const idOf = (name: string, n = 0) => results.filter((m) => m.toolName === name)[n]?.toolCallId as string;
		const [b1, rd, b3] = [idOf("bash", 0), idOf("read"), idOf("bash", 1)];
		// which results each model request saw masked
		const maskedPerRequest = run.trace.map((l) =>
			l.context_results
				.filter((r) => r.head.startsWith(MASKED))
				.map((r) => r.toolCallId)
				.sort(),
		);
		assert.deepEqual(
			maskedPerRequest,
			[[], [], [], [], [b1], [b1], [b1, rd].sort()],
			`masks advance only after turns 4 and 6: ${JSON.stringify(maskedPerRequest)}`,
		);
		// the failure is never masked; the read of mid.txt survives the first window (edited at turn 4)
		for (const l of run.trace) {
			const f = l.context_results.find((r) => r.toolCallId === b3);
			if (f) {
				assert.ok(!f.head.startsWith(MASKED), "the latest failure stays visible");
				assert.equal(f.isError, true);
			}
		}
		// the stub reached the model with the original's error flag, and recovers the original exactly
		const stub = run.trace[4].context_results.find((r) => r.toolCallId === b1) ?? assert.fail("no stub");
		const m = stubHandle(stub.head) ?? assert.fail(`stub names no handle: ${stub.head}`);
		assert.equal(m[2], ws);
		const orig = await readAll(api, ws, m[1]);
		const original = textOf(results.find((r) => r.toolCallId === b1));
		assert.equal(orig.bytes.toString("utf8"), original, "the masked original is recoverable byte for byte");
		assert.equal(sha(orig.bytes), sha(Buffer.from(original)));
		// raw entries are kept: the session still holds the original result, plus context edits
		const rawResult = entries.find((e) => e.type === "message" && e.message?.toolCallId === b1);
		assert.ok(rawResult, `get_entries returned ${entries.length} entries`);
		assert.equal(textOf(rawResult.message), original);
		const edits = entries.filter((e) => e.type === "context_edit");
		assert.equal(edits.length, 2);
		assert.ok(edits.some((e) => e.targetId === rawResult.id));
		assert.ok(run.trace.at(-1)?.active_tools.includes("xmustard_expand"), "stubs activate xmustard_expand");
		record("masking", { masked_per_request: maskedPerRequest.map((x) => x.length), stub: stub.head.slice(0, 240), context_edits: edits.length });
	});

	test("compaction: a snapshot entry whose details carry handles reaches the next model request; every handle recovers its original", async () => {
		let phase = "p1";
		let compacted: any;
		let entries: any[] = [];
		const run = await pi(
			"compaction",
			[
				{ calls: [{ name: "bash", args: { command: "seq 1 800" } }] },
				{ calls: [{ name: "read", args: { path: "mid.txt" } }] },
				{ calls: [{ name: "bash", args: { command: "seq 1 700; echo 'boom: compaction fixture'; exit 4" } }] },
				{ calls: [call("remember", { content: "compaction e2e memory", title: "compaction-e2e" })] },
				{ text: "phase one done" },
				{ text: "after compaction" },
			],
			{
				env: { XMUSTARD_PI_MASK: "off" },
				settings: { compaction: { keepRecentTokens: 1 } },
				rpcManual: true,
				rpc: (ev, write, end) => {
					if (ev.type === "agent_settled" && phase === "p1") {
						phase = "compacting";
						write({ id: "c1", type: "compact" });
					} else if (ev.type === "response" && ev.command === "compact") {
						compacted = ev;
						phase = "p2";
						write({ id: "p2", type: "prompt", message: "continue after compaction" });
					} else if (ev.type === "agent_settled" && phase === "p2") {
						phase = "entries";
						write({ id: "g1", type: "get_entries" });
					} else if (ev.type === "response" && ev.command === "get_entries") {
						entries = ev.data?.entries ?? ev.data ?? [];
						end();
					}
				},
				timeoutMs: 60_000,
			},
		);
		assert.equal(run.code, 0, run.stderr);
		assert.equal(compacted?.success, true, JSON.stringify(compacted).slice(0, 500));
		const { summary, details } = compacted.data;
		assert.ok(summary.startsWith("[xmustard compaction snapshot]"), summary.slice(0, 200));
		assert.match(summary, /not verified memory/);
		assert.ok(Buffer.byteLength(summary) <= 2048, `${Buffer.byteLength(summary)} bytes`);
		assert.match(summary, /Open failures:\n- bash `seq 1 700; echo 'boom: compaction fixture'; exit 4` \(turn 3\): Command exited with code 4 → xm1\./);
		assert.equal(details?.xmustard?.version, "xmustard.pi-compaction/v1");
		assert.equal(details.xmustard.verified_memory, false);
		// every summarized output above 1 KiB has a handle that recovers it exactly
		const results = toolResultMessages(run);
		const big = results.filter((m) => Buffer.byteLength(textOf(m)) > 1024);
		assert.equal(big.length, 3);
		const byCall = new Map<string, any>(details.xmustard.handles.map((h: any) => [h.tool_call_id, h]));
		for (const m of big) {
			const h = byCall.get(m.toolCallId) ?? assert.fail(`no handle for ${m.toolName} ${m.toolCallId}`);
			const got = await readAll(api, h.workspace_id, h.handle);
			assert.equal(got.status, 200);
			assert.equal(got.bytes.toString("utf8"), textOf(m), `${m.toolName} original recovered`);
		}
		// the entry Pi appended is ours, with the handles in details
		const entry = entries.find((e) => e.type === "compaction");
		assert.ok(entry, "compaction entry appended");
		assert.equal(entry.fromHook, true);
		assert.equal(entry.summary, summary);
		assert.deepEqual(entry.details.xmustard.handles, details.xmustard.handles);
		// the next model request carries the snapshot, not the compacted tool outputs
		const after = run.trace.find((l) => l.user_texts.some((t) => t.includes(summary))) ?? assert.fail("snapshot never reached the model");
		for (const m of big) assert.ok(!after.context_results.some((r) => r.toolCallId === m.toolCallId), "compacted results are out of context");
		assert.ok(after.active_tools.includes("xmustard_expand"));
		record("compaction", { summary_bytes: Buffer.byteLength(summary), handles: details.xmustard.handles.length, tokens_before: compacted.data.tokensBefore });
	});
});
