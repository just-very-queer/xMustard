// Process harness for the Pi adapter e2e: a real xMustard API (Go + Rust core) on a
// random loopback port over a temporary data dir, the real `pi` CLI (pinned,
// project-local) with the scripted offline provider, and two fault injectors — a
// stalling reverse proxy and a blackhole listener. No global config or credentials:
// every Pi run gets an empty HOME/agent dir and a scrubbed environment.

import { type ChildProcess, execFile, execFileSync, spawn } from "node:child_process";
import { appendFileSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

export const PI_DIR = resolve(import.meta.dirname, "../..");
export const REPO_ROOT = resolve(PI_DIR, "../..");
const PI_BIN = join(PI_DIR, "node_modules/.bin/pi");
const EXTENSION = join(PI_DIR, "src/index.ts");
const PROVIDER = join(PI_DIR, "test/fixtures/scripted-provider.ts");

export function env(name: string): string {
	const v = process.env[name];
	if (!v) throw new Error(`${name} is required (run through scripts/e2e/pi-adapter.sh)`);
	return v;
}

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// ---- resource sampling -------------------------------------------------------------
//
// RssSampler polls `ps -axo pid,ppid,rss,comm` every 100 ms and attributes every process
// to the registered root it descends from: the xMustard API (with its Rust core and git
// children), the xMustard MCP shim, Pi (external; includes its tool children), or the
// disposable Postgres test fixture (external; reported separately). The test driver,
// fault-injection proxies, builds and compilers are never registered, so they are
// excluded. RSS is in KiB as `ps` reports it on macOS and Linux. Every figure is the
// largest value seen across samples, not an OS high-water mark: short spikes between
// samples are missed, so these are sampled lower bounds, not ceilings.

export type Owner = "xmustard-api" | "xmustard-mcp" | "pi" | "postgres-fixture";
const XMUSTARD_OWNED: readonly Owner[] = ["xmustard-api", "xmustard-mcp"];

interface Proc {
	pid: number;
	owner: Owner;
	comm: string;
	rss_kib: number;
}
interface Peak {
	kib: number;
	at_ms: number;
	phase: string;
	processes: Proc[];
}
const emptyPeak = (): Peak => ({ kib: 0, at_ms: 0, phase: "", processes: [] });

export class RssSampler {
	private readonly roots = new Map<number, Owner>();
	private timer: NodeJS.Timeout | undefined;
	private busy = false;
	private t0 = 0;
	phase = "setup";
	samples = 0;
	skipped = 0;
	failures: string[] = [];
	readonly peaks = { xmustard_owned: emptyPeak(), pi_external: emptyPeak(), postgres_fixture: emptyPeak(), workflow: emptyPeak() };
	readonly phases = new Map<string, { samples: number; xmustard_owned_kib: number; pi_external_kib: number; postgres_fixture_kib: number; workflow_kib: number }>();
	readonly sampledMax = new Map<string, number>(); // `${owner}:${comm}` → largest sampled single-process RSS

	register(pid: number | undefined, owner: Owner): void {
		if (pid) this.roots.set(pid, owner);
	}
	start(): void {
		this.t0 = Date.now();
		this.timer = setInterval(() => this.tick(), 100);
	}
	async stop(): Promise<void> {
		clearInterval(this.timer);
		while (this.busy) await sleep(20);
	}
	private tick(): void {
		if (this.busy) {
			this.skipped++;
			return;
		}
		this.busy = true;
		execFile("ps", ["-axo", "pid=,ppid=,rss=,comm="], { maxBuffer: 8 << 20 }, (err, out) => {
			this.busy = false;
			if (err) {
				if (this.failures.length < 20) this.failures.push(err.message);
				return;
			}
			this.observe(out);
		});
	}
	private observe(out: string): void {
		const rows = new Map<number, { ppid: number; rss: number; comm: string }>();
		const kids = new Map<number, number[]>();
		for (const line of out.split("\n")) {
			const m = /^\s*(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/.exec(line);
			if (!m) continue;
			const pid = Number(m[1]);
			const ppid = Number(m[2]);
			rows.set(pid, { ppid, rss: Number(m[3]), comm: m[4].trim().split("/").pop() ?? "" });
			kids.set(ppid, [...(kids.get(ppid) ?? []), pid]);
		}
		const procs: Proc[] = [];
		for (const [root, owner] of this.roots) {
			const queue = rows.has(root) ? [root] : [];
			while (queue.length) {
				const pid = queue.shift() as number;
				const r = rows.get(pid);
				if (!r) continue;
				procs.push({ pid, owner, comm: r.comm, rss_kib: r.rss });
				queue.push(...(kids.get(pid) ?? []));
			}
		}
		this.samples++;
		const at = Date.now() - this.t0;
		const sum = (f: (p: Proc) => boolean) => procs.filter(f).reduce((a, p) => a + p.rss_kib, 0);
		const owned = sum((p) => XMUSTARD_OWNED.includes(p.owner));
		const pi = sum((p) => p.owner === "pi");
		const pg = sum((p) => p.owner === "postgres-fixture");
		const all = owned + pi + pg;
		const bump = (peak: Peak, kib: number, f: (p: Proc) => boolean) => {
			if (kib > peak.kib) Object.assign(peak, { kib, at_ms: at, phase: this.phase, processes: procs.filter(f) });
		};
		bump(this.peaks.xmustard_owned, owned, (p) => XMUSTARD_OWNED.includes(p.owner));
		bump(this.peaks.pi_external, pi, (p) => p.owner === "pi");
		bump(this.peaks.postgres_fixture, pg, (p) => p.owner === "postgres-fixture");
		bump(this.peaks.workflow, all, () => true);
		const ph = this.phases.get(this.phase) ?? { samples: 0, xmustard_owned_kib: 0, pi_external_kib: 0, postgres_fixture_kib: 0, workflow_kib: 0 };
		ph.samples++;
		ph.xmustard_owned_kib = Math.max(ph.xmustard_owned_kib, owned);
		ph.pi_external_kib = Math.max(ph.pi_external_kib, pi);
		ph.postgres_fixture_kib = Math.max(ph.postgres_fixture_kib, pg);
		ph.workflow_kib = Math.max(ph.workflow_kib, all);
		this.phases.set(this.phase, ph);
		for (const p of procs) {
			const k = `${p.owner}:${p.comm}`;
			this.sampledMax.set(k, Math.max(this.sampledMax.get(k) ?? 0, p.rss_kib));
		}
	}
	report(): Record<string, unknown> {
		const mib = (k: number) => Math.round((k / 1024) * 10) / 10;
		const peak = (p: Peak) => ({ sampled_peak_mib: mib(p.kib), at_ms: p.at_ms, phase: p.phase, composition: p.processes.map((x) => ({ ...x, rss_mib: mib(x.rss_kib) })) });
		return {
			method: "ps -axo pid,ppid,rss,comm every 100 ms; RSS KiB→MiB; trees attributed to registered roots",
			scope:
				"e2e conformance workload, not the fixed scripts/bench/rss.sh workload; xmustard_owned = every registered API instance alive at the sample (the main API, plus the expiry/auth instance during those tests) with Rust/git children, and the MCP shim while it runs for the schema check; pi_external = all Pi CLI process trees alive (two during the concurrent-process test); postgres_fixture = the disposable native Postgres used only so the diagnostics tool can succeed (not part of the default no-DB deployment); workflow = all three. Test driver, proxies, builds and compilers excluded.",
			caveat: "every value is the maximum over 100 ms samples, not an OS high-water mark; short spikes are missed; not a ceiling and not the 100 MB gate",
			samples: this.samples,
			skipped_ticks: this.skipped,
			sampling_failures: this.failures,
			xmustard_owned: peak(this.peaks.xmustard_owned),
			pi_external: peak(this.peaks.pi_external),
			postgres_fixture: peak(this.peaks.postgres_fixture),
			workflow: peak(this.peaks.workflow),
			per_process_sampled_max_mib: Object.fromEntries([...this.sampledMax].map(([k, v]) => [k, mib(v)])),
			per_phase_peak_mib: Object.fromEntries(
				[...this.phases].map(([k, v]) => [
					k,
					{ samples: v.samples, xmustard_owned: mib(v.xmustard_owned_kib), pi_external: mib(v.pi_external_kib), postgres_fixture: mib(v.postgres_fixture_kib), workflow: mib(v.workflow_kib) },
				]),
			),
		};
	}
}

export const sampler = new RssSampler();

// ---- disposable Postgres fixture ------------------------------------------------------
//
// The diagnostics tool reads its baseline from Postgres (optional, off by default in
// xMustard). To prove that tool succeeds through Pi, the e2e starts a throwaway native
// cluster: initdb into the temp dir, trust auth for a fixture-only role, loopback on a
// random port plus a private socket dir, no durability. It never touches an existing
// server or database, and stop() deletes it.
export class PgFixture {
	readonly bin: string;
	port = 0;
	private dir = "";
	private sock = "";
	private child: ChildProcess | undefined;
	constructor(binDir: string) {
		this.bin = binDir;
	}
	get dsn(): string {
		return `postgres://xm_fixture@127.0.0.1:${this.port}/xmustard_e2e?sslmode=disable`;
	}
	private get env(): NodeJS.ProcessEnv {
		return { PATH: process.env.PATH ?? "", HOME: this.dir, LC_ALL: "C", TZ: "UTC" };
	}
	private psql(sql: string, db = "postgres"): string {
		return execFileSync(join(this.bin, "psql"), ["-X", "-h", "127.0.0.1", "-p", String(this.port), "-U", "xm_fixture", "-d", db, "-Atc", sql], {
			env: this.env,
			encoding: "utf8",
			stdio: ["ignore", "pipe", "pipe"],
		});
	}
	async start(root: string): Promise<void> {
		this.dir = join(root, "pgdata");
		this.sock = mkdtempSync(join(tmpdir(), "xmpg-"));
		execFileSync(join(this.bin, "initdb"), ["-D", this.dir, "-U", "xm_fixture", "-A", "trust", "-E", "UTF8", "--no-locale"], { env: this.env, stdio: "pipe" });
		this.port = await freePort();
		const log = openSync(join(root, "postgres.log"), "a");
		this.child = spawn(
			join(this.bin, "postgres"),
			["-D", this.dir, "-p", String(this.port), "-k", this.sock, "-c", "listen_addresses=127.0.0.1", "-c", "fsync=off", "-c", "full_page_writes=off", "-c", "synchronous_commit=off"],
			{ env: this.env, stdio: ["ignore", log, log] },
		);
		sampler.register(this.child.pid, "postgres-fixture");
		for (let i = 0; ; i++) {
			try {
				this.psql("select 1");
				break;
			} catch (err) {
				if (i > 100 || this.child.exitCode !== null) throw new Error(`fixture postgres did not start: ${String(err)}`);
				await sleep(100);
			}
		}
		this.psql("create database xmustard_e2e");
	}
	async stop(): Promise<void> {
		const c = this.child;
		if (c && c.exitCode === null) {
			const done = new Promise((r) => c.once("exit", r));
			c.kill("SIGINT"); // fast shutdown
			const t = setTimeout(() => c.kill("SIGKILL"), 10_000);
			await done;
			clearTimeout(t);
		}
		if (this.dir) rmSync(this.dir, { recursive: true, force: true });
		if (this.sock) rmSync(this.sock, { recursive: true, force: true });
	}
}

export async function freePort(): Promise<number> {
	return new Promise((res, rej) => {
		const s = net.createServer();
		s.once("error", rej);
		s.listen(0, "127.0.0.1", () => {
			const port = (s.address() as net.AddressInfo).port;
			s.close(() => res(port));
		});
	});
}

export interface ApiOptions {
	dataDir: string;
	port?: number;
	extraEnv?: Record<string, string>;
	logFile: string;
}

export class Api {
	readonly opts: ApiOptions;
	port = 0;
	private child: ChildProcess | undefined;
	constructor(opts: ApiOptions) {
		this.opts = opts;
	}
	get base(): string {
		return `http://127.0.0.1:${this.port}`;
	}
	async start(): Promise<void> {
		this.port = this.opts.port ?? (await freePort());
		const child = spawn(env("XM_API_BIN"), [], {
			env: {
				PATH: process.env.PATH ?? "",
				HOME: this.opts.dataDir,
				XMUSTARD_DATA_DIR: this.opts.dataDir,
				XMUSTARD_API_HOST: "127.0.0.1",
				XMUSTARD_API_PORT: String(this.port),
				XMUSTARD_CORE_BIN: env("XM_CORE_BIN"),
				...this.opts.extraEnv,
			},
			stdio: ["ignore", "pipe", "pipe"],
		});
		sampler.register(child.pid, "xmustard-api");
		const log = (b: Buffer) => appendFileSync(this.opts.logFile, b);
		child.stdout?.on("data", log);
		child.stderr?.on("data", log);
		this.child = child;
		for (let i = 0; i < 100; i++) {
			if (child.exitCode !== null) throw new Error(`xmustard-api exited ${child.exitCode}; see ${this.opts.logFile}`);
			try {
				const r = await fetch(`${this.base}/api/health`);
				if (r.status < 500) return;
			} catch {
				// not listening yet
			}
			await sleep(100);
		}
		throw new Error(`xmustard-api did not become healthy; see ${this.opts.logFile}`);
	}
	async stop(): Promise<void> {
		const c = this.child;
		if (!c || c.exitCode !== null) return;
		const done = new Promise((r) => c.once("exit", r));
		c.kill("SIGTERM");
		const t = setTimeout(() => c.kill("SIGKILL"), 20_000);
		await done;
		clearTimeout(t);
	}
	async restart(): Promise<void> {
		const port = this.port;
		await this.stop();
		this.opts.port = port;
		await this.start();
	}
	async json(method: string, path: string, body?: unknown, token?: string): Promise<{ status: number; body: any; text: string }> {
		const headers: Record<string, string> = {};
		if (body !== undefined) headers["Content-Type"] = "application/json";
		if (token) headers.Authorization = `Bearer ${token}`;
		const r = await fetch(this.base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
		const text = await r.text();
		let parsed: any;
		try {
			parsed = JSON.parse(text);
		} catch {
			parsed = undefined;
		}
		return { status: r.status, body: parsed, text };
	}
}

export function mintToken(dataDir: string, id: string, role = "agent"): string {
	return execFileSync(env("XM_API_BIN"), ["mint-token", id, role], {
		env: { PATH: process.env.PATH ?? "", XMUSTARD_DATA_DIR: dataDir },
		encoding: "utf8",
	}).trim();
}

export function git(repo: string, ...args: string[]): string {
	return execFileSync("git", ["-C", repo, ...args], {
		encoding: "utf8",
		env: { PATH: process.env.PATH ?? "", HOME: repo, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1" },
	});
}

// makeRepo writes a small Go/TS repository: many handlers with a shared failure
// marker, so search and explain return sizeable, deterministic results.
export function makeRepo(dir: string, files = 400): void {
	mkdirSync(join(dir, "pkg"), { recursive: true });
	mkdirSync(join(dir, "web"), { recursive: true });
	for (let i = 0; i < files; i++) {
		writeFileSync(
			join(dir, "pkg", `handler_${String(i).padStart(4, "0")}.go`),
			`package pkg\n\n// Handler${i} validates request ${i}; résumé naïve café.\nfunc Handler${i}(x int) int {\n\tif x < 0 {\n\t\tpanic("xm_marker negative input in Handler${i}")\n\t}\n\treturn x + ${i}\n}\n`,
		);
	}
	writeFileSync(join(dir, "web", "app.ts"), `export function xm_marker_app(n: number): number {\n  return n * 2;\n}\n`);
	writeFileSync(join(dir, "README.md"), "# fixture\n");
	git(dir, "init", "-q");
	git(dir, "-c", "user.email=e2e@example.invalid", "-c", "user.name=e2e", "add", "-A");
	git(dir, "-c", "user.email=e2e@example.invalid", "-c", "user.name=e2e", "commit", "-qm", "fixture");
}

// seedFailedRun writes a run record: the API has no route that creates a run
// without an agent runtime, so why_failed needs a persisted record to explain.
export function seedFailedRun(dataDir: string, ws: string, runId: string, logDir: string): void {
	const out = join(logDir, `${runId}.log`);
	writeFileSync(out, "go test ./...\n--- FAIL: TestHandler7 (0.00s)\npkg/handler_0007.go:6: panic: xm_marker negative input in Handler7\nFAIL\n");
	const runsDir = join(dataDir, "workspaces", ws, "runs");
	mkdirSync(runsDir, { recursive: true });
	writeFileSync(
		join(runsDir, `${runId}.json`),
		JSON.stringify({
			run_id: runId,
			workspace_id: ws,
			issue_id: "",
			runtime: "e2e",
			model: "none",
			status: "failed",
			title: "fixture failure",
			prompt: "",
			command: ["go", "test", "./..."],
			command_preview: "go test ./...",
			log_path: out,
			output_path: out,
			created_at: new Date().toISOString(),
			exit_code: 1,
			guidance_paths: [],
		}),
	);
}

export interface Step {
	[k: string]: unknown;
}

export interface TraceLine {
	request: number;
	active_tools: string[];
	tools?: { name: string; description: string; parameters: unknown }[];
	tool_results: { toolCallId: string; toolName: string; isError: boolean; text: string }[];
}

export interface PiRun {
	code: number | null;
	events: any[];
	trace: TraceLine[];
	stderr: string;
	stdout: string;
}

export interface PiOptions {
	dir: string; // scratch dir for this run
	cwd: string;
	apiBase: string;
	ws: string;
	steps: Step[];
	env?: Record<string, string>;
	timeoutMs?: number;
	// rpc: drive `pi --mode rpc`; onEvent may write further commands to stdin.
	rpc?: (ev: any, write: (cmd: unknown) => void) => void;
}

// runPi runs the real `pi` CLI once with the adapter and the scripted provider.
export async function runPi(o: PiOptions): Promise<PiRun> {
	mkdirSync(o.dir, { recursive: true });
	const home = join(o.dir, "home");
	mkdirSync(home, { recursive: true });
	const scenario = join(o.dir, "scenario.json");
	const trace = join(o.dir, "trace.jsonl");
	writeFileSync(scenario, JSON.stringify({ steps: o.steps }));
	writeFileSync(trace, "");
	const args = [
		"--mode",
		o.rpc ? "rpc" : "json",
		...(o.rpc ? [] : ["-p"]),
		"--no-session",
		"--no-extensions",
		"--no-skills",
		"--no-prompt-templates",
		"--no-themes",
		"--no-context-files",
		"-e",
		EXTENSION,
		"-e",
		PROVIDER,
		"--model",
		"xmscripted/scripted",
		...(o.rpc ? [] : ["run the scenario"]),
	];
	const child = spawn(PI_BIN, args, {
		cwd: o.cwd,
		env: {
			PATH: process.env.PATH ?? "",
			HOME: home,
			PI_CODING_AGENT_DIR: join(home, "agent"),
			PI_OFFLINE: "1",
			PI_SKIP_VERSION_CHECK: "1",
			PI_TELEMETRY: "0",
			XMUSTARD_API_BASE: o.apiBase,
			XM_PI_SCENARIO: scenario,
			XM_PI_TRACE: trace,
			XM_PI_WS: o.ws,
			...o.env,
		},
		stdio: ["pipe", "pipe", "pipe"],
	});
	sampler.register(child.pid, "pi");
	let stdout = "";
	let stderr = "";
	let pending = "";
	const events: any[] = [];
	const write = (cmd: unknown) => child.stdin.write(`${JSON.stringify(cmd)}\n`);
	child.stdout.on("data", (b: Buffer) => {
		const s = b.toString("utf8");
		stdout += s;
		pending += s;
		let nl = pending.indexOf("\n");
		while (nl >= 0) {
			const line = pending.slice(0, nl).replace(/\r$/, "");
			pending = pending.slice(nl + 1);
			nl = pending.indexOf("\n");
			if (!line.trim()) continue;
			let ev: any;
			try {
				ev = JSON.parse(line);
			} catch {
				continue;
			}
			events.push(ev);
			if (o.rpc) {
				o.rpc(ev, write);
				if (ev.type === "agent_settled") child.stdin.end();
			}
		}
	});
	child.stderr.on("data", (b: Buffer) => {
		stderr += b.toString("utf8");
	});
	if (o.rpc) write({ id: "p1", type: "prompt", message: "run the scenario" });
	else child.stdin.end();
	const timeoutMs = o.timeoutMs ?? 120_000;
	const code = await new Promise<number | null>((res) => {
		const t = setTimeout(() => {
			child.kill("SIGKILL");
		}, timeoutMs);
		child.once("exit", (c) => {
			clearTimeout(t);
			res(c);
		});
	});
	writeFileSync(join(o.dir, "stdout.jsonl"), stdout);
	writeFileSync(join(o.dir, "stderr.txt"), stderr);
	const lines = existsSync(trace) ? readFileSync(trace, "utf8").split("\n").filter(Boolean) : [];
	return { code, events, trace: lines.map((l) => JSON.parse(l) as TraceLine), stderr, stdout };
}

// toolEnds returns tool_execution_end events in completion order.
export function toolEnds(run: PiRun, name?: string): any[] {
	return run.events.filter((e) => e.type === "tool_execution_end" && (!name || e.toolName === name));
}

// toolResultMessages returns the finalized toolResult messages (after tool_result hooks).
export function toolResultMessages(run: PiRun, name?: string): any[] {
	return run.events.filter((e) => e.type === "message_end" && e.message?.role === "toolResult" && (!name || e.message.toolName === name)).map((e) => e.message);
}

export const textOf = (m: { content?: { type: string; text?: string }[] }): string =>
	(m.content ?? []).map((c) => (c.type === "text" ? (c.text ?? "") : "")).join("");

export function footerOf(text: string): Record<string, any> | undefined {
	const line = text.split("\n").reverse().find((l) => l.startsWith("[xmustard evidence] "));
	return line ? JSON.parse(line.slice("[xmustard evidence] ".length)) : undefined;
}

export function pageHeaderOf(text: string): Record<string, any> | undefined {
	const first = text.split("\n", 1)[0];
	return first.startsWith("[xmustard page] ") ? JSON.parse(first.slice("[xmustard page] ".length)) : undefined;
}

// Fault-injecting reverse proxy: forwards to `target` except requests matching `stall`,
// which are held open (never answered), and requests matching `drop`, whose connection
// is reset (Go unreachable mid-session). Records when a stalled client disconnects,
// which is how an aborted Pi fetch becomes observable.
export class StallProxy {
	readonly target: string;
	readonly stall: (req: http.IncomingMessage) => boolean;
	readonly drop: (req: http.IncomingMessage) => boolean;
	port = 0;
	stalled = 0;
	stalledClosed = 0;
	dropped = 0;
	readonly seen: string[] = [];
	private server: http.Server | undefined;
	constructor(target: string, stall: (req: http.IncomingMessage) => boolean, drop: (req: http.IncomingMessage) => boolean = () => false) {
		this.target = target;
		this.stall = stall;
		this.drop = drop;
	}
	get base(): string {
		return `http://127.0.0.1:${this.port}`;
	}
	async start(): Promise<void> {
		this.server = http.createServer((req, res) => {
			this.seen.push(`${req.method} ${(req.url ?? "").split("?")[0]}`);
			if (this.drop(req)) {
				this.dropped++;
				req.socket.destroy();
				return;
			}
			if (this.stall(req)) {
				this.stalled++;
				req.resume();
				res.on("close", () => {
					this.stalledClosed++;
				});
				return;
			}
			const up = http.request(this.target + (req.url ?? ""), { method: req.method, headers: req.headers }, (ur) => {
				res.writeHead(ur.statusCode ?? 502, ur.headers);
				ur.pipe(res);
			});
			up.on("error", () => res.destroy());
			res.on("close", () => up.destroy());
			req.pipe(up);
		});
		await new Promise<void>((r) => this.server?.listen(0, "127.0.0.1", () => r()));
		this.port = (this.server?.address() as net.AddressInfo).port;
	}
	async stop(): Promise<void> {
		this.server?.closeAllConnections();
		await new Promise((r) => this.server?.close(r));
	}
}

// mcpToolsList asks the real stdio MCP server for its tool list (no API needed).
export async function mcpToolsList(apiBase: string): Promise<{ name: string; description: string; inputSchema: unknown }[]> {
	const child = spawn(env("XM_MCP_BIN"), [], { env: { PATH: process.env.PATH ?? "", XMUSTARD_API_BASE: apiBase }, stdio: ["pipe", "pipe", "inherit"] });
	sampler.register(child.pid, "xmustard-mcp");
	let buf = "";
	const result = new Promise<any>((res, rej) => {
		child.stdout.on("data", (b: Buffer) => {
			buf += b.toString("utf8");
			for (const line of buf.split("\n")) {
				try {
					const msg = JSON.parse(line);
					if (msg.id === 2) res(msg.result);
				} catch {
					// partial line
				}
			}
		});
		child.once("exit", () => rej(new Error("xmustard-mcp exited before tools/list")));
	});
	child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "pi-e2e", version: "1" } } })}\n`);
	child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" })}\n`);
	child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id: 2, method: "tools/list" })}\n`);
	const r = await result;
	child.kill();
	return r.tools;
}
