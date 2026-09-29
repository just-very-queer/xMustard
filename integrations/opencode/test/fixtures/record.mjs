// Records source-derived OpenCode 2.0.1 fixtures (run: node test/fixtures/record.mjs).
//
// No model runs and OpenCode is not started. The recorder reads the installed
// OpenCode binary, finds in it, byte for byte, the code that builds each tool's
// result (the grep and read formatters, the shell result builder, the MCP tool
// wrapper) and the code that builds the tool "execute.after" hook input, evaluates
// that code on deterministic sample data, and writes:
//
//   test/fixtures/2.0.1/<case>.hook.json   the hook input OpenCode passes the plugin
//   api-go/internal/evidence/testdata/opencode/2.0.1/<case>.body.json
//                                          the capture body the plugin posts for it
//                                          (the plugin's own outcomeV2 + captureBody)
//
// A different OpenCode build fails loudly: every snippet must be present verbatim.
// OPENCODE_BIN overrides the binary; OPENCODE_PACKAGE_JSON its package.json.

import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { captureBody } from "../../src/capture.ts";
import { outcomeV2 } from "../../src/v2.ts";

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, "../../../..");
const CLI = "/opt/homebrew/lib/node_modules/@opencode/cli";
const BIN = process.env.OPENCODE_BIN ?? `${CLI}/node_modules/@opencode/cli-${process.platform}-${process.arch}/bin/opencode`;
const PKG = process.env.OPENCODE_PACKAGE_JSON ?? `${CLI}/package.json`;
const VERSION = "2.0.1";

const pkg = JSON.parse(readFileSync(PKG, "utf8"));
if (pkg.version !== VERSION) throw new Error(`installed OpenCode is ${pkg.version}; these fixtures pin ${VERSION}`);
const bin = readFileSync(BIN);

// snippet returns the bytes of the binary from `start` through the first `end`
// after it, which must occur exactly once.
function snippet(start, end) {
	const from = bin.indexOf(start);
	if (from < 0 || bin.indexOf(start, from + 1) >= 0) throw new Error(`snippet start not found exactly once: ${start}`);
	const to = bin.indexOf(end, from);
	if (to < 0) throw new Error(`snippet end not found: ${end}`);
	return bin.subarray(from, to + end.length).toString("utf8");
}

// The 2.0.1 bundle's own code, evaluated as is (minified names included).
const sources = {
	// opencode.tool.grep: the model-visible text of a grep call
	grep: snippet("Xj=(e,i=!1)=>{let r=e.length===0?", "return r.join(`\n`)}"),
	// opencode.tool.read: the model-visible text of a read (numbered lines)
	read: snippet("NL=(e,i,r)=>{if(r.type===", "return c.join(`\n`)}"),
	// opencode.tool.shell: the result of a finished command
	shellNI: snippet("function NI(e){if(e.timeout)", ".`}"),
	shellCI: snippet("function CI(e){return{truncated:", ":{}}}"),
	shellDL: snippet('Dg=(e)=>{let i=e.status==="running"', "}:{}}})"),
	// the MCP tool wrapper (McpTool): a failed call's message, a completed call's result
	mcpError: snippet('M.content.flatMap((H)=>H.type==="text"?[H.text]:[]).join(`\n`).trim()||"MCP tool returned an error"', '"MCP tool returned an error"'),
	mcpResult: snippet('let K=M.content.map((H)=>H.type==="text"?{type:"text",text:H.text}', "{}:{content:K}}"),
	// Tool.execute: the execute.after input (completed and failed calls)
	hookCompleted: snippet('let zt={...yi,status:"completed",result:', "{metadata:zn.value.metadata}}}"),
	hookFailed: snippet('let Dr={...yi,status:"error",error:zn.failure}', "error:zn.failure}"),
};

const grepText = new Function(`let ${sources.grep};return Xj;`)();
const readText = new Function(`let ${sources.read};return NL;`)();
const shellResult = new Function(`const UL="(running)";${sources.shellNI}${sources.shellCI}let ${sources.shellDL};return DL;`)();
const mcpErrorMessage = new Function("M", `return ${sources.mcpError};`);
const mcpResult = new Function("M", "k", sources.mcpResult);
// yi is the call; zn.value the tool's result, or zn.failure its Tool.Error
const hookCompleted = new Function("yi", "zn", `${sources.hookCompleted};return zt;`);
const hookFailed = new Function("yi", "zn", `${sources.hookFailed};return Dr;`);

const call = (tool, id, input) => ({ tool, sessionID: "ses_fixture", agent: "build", messageID: "msg_fixture", id, input });
const ROOT = "/Users/dev/repo";

// ---- deterministic sample data ----------------------------------------------------

function goTestLog() {
	const lines = ["=== RUN   TestParseValue"];
	for (let i = 0; i < 120; i++) lines.push(`=== RUN   TestCase${String(i).padStart(4, "0")}`, `--- PASS: TestCase${String(i).padStart(4, "0")} (0.00s)`);
	lines.push('    parse_test.go:17: parse("3") = 3, want 4', "--- FAIL: TestParseValue (0.00s)", "FAIL", "FAIL\texample.com/pkg\t0.412s", "FAIL");
	return lines.join("\n");
}

function grepMatches(files, perFile) {
	const out = [];
	for (let f = 0; f < files; f++) {
		for (let m = 0; m < perFile; m++) {
			out.push({ entry: { path: `${ROOT}/src/pkg${String(f % 7).padStart(2, "0")}/file_${String(f).padStart(3, "0")}.go` }, line: 10 + m * 3, text: `\tfoo := callSomething(${m}) // matches needle` });
		}
	}
	return out;
}

function sourceFile(lines) {
	const out = ["package big", "", 'import "fmt"', ""];
	for (let i = 0; out.length < lines; i++) out.push(`// Func${i} does step ${i}.`, `func Func${i}(x int) int {`, `\ty := x * ${i}`, "\tfmt.Println(y)", `\treturn y + ${i}`, "}", "");
	return `${out.slice(0, lines).join("\n")}\n`;
}

function searchJSON(n) {
	const items = [];
	for (let i = 0; i < n; i++) items.push({ path: `src/mod${i % 9}/handler_${i}.ts`, repository: "acme/app", score: 1 - i / 1000, fragment: `export function handler${i}(req: Request) { return route(req, ${i}); }` });
	return JSON.stringify({ total_count: n, incomplete_results: false, items });
}

function ciFailureLog() {
	const lines = [];
	for (let i = 0; i < 90; i++) lines.push(`2026-09-28T10:${String(i % 60).padStart(2, "0")}:00Z step ${i}: npm run build --workspace pkg-${i % 5}`);
	lines.push("ERROR: build failed: src/index.ts(12,3): error TS2322: Type 'string' is not assignable to type 'number'.");
	return lines.join("\n");
}

// ---- the cases ----------------------------------------------------------------------

const cases = {
	// a failing test run: the shell tool's result for exit code 1
	"shell-go-test-fail": hookCompleted(call("shell", "call_shell", { command: "go test ./...", workdir: ROOT }), {
		value: shellResult({ output: goTestLog(), truncated: false, exit: 1, status: "completed" }),
	}),
	// 100 matches in 20 files: the O4 probe (a line-oriented parse counted 120 and 25)
	"grep-100-matches": hookCompleted(call("grep", "call_grep", { pattern: "needle", path: "src" }), {
		value: { output: grepMatches(20, 5), content: grepText(grepMatches(20, 5), false), metadata: { matches: 100, truncated: false } },
	}),
	// a 150-line read: numbered lines, observe-only for xMustard
	"read-numbered": hookCompleted(call("read", "call_read", { path: "big/big.go" }), {
		value: (() => {
			const page = { type: "file", uri: `file://${ROOT}/big/big.go`, name: "big.go", content: sourceFile(150), encoding: "utf8", mime: "text/x-go" };
			return { output: page, content: readText("big/big.go", undefined, page), metadata: { truncated: false } };
		})(),
	}),
	// another server's tool called directly (a server with codemode false): JSON text
	"mcp-github-search": hookCompleted(call("github_search_code", "call_mcp", { q: "handler" }), {
		value: mcpResult({ isError: false, structured: undefined, content: [{ type: "text", text: searchJSON(40) }] }, { outputSchema: undefined }),
	}),
	// the same wrapper when the server answers isError: a failed call
	"mcp-error": hookFailed(call("ci_fetch_log", "call_mcp_err", { job: "42" }), {
		failure: { _tag: "Tool.Error", message: mcpErrorMessage({ content: [{ type: "text", text: ciFailureLog() }] }) },
	}),
};

const hookDir = path.join(here, VERSION);
const bodyDir = path.join(repo, "api-go/internal/evidence/testdata/opencode", VERSION);
mkdirSync(hookDir, { recursive: true });
mkdirSync(bodyDir, { recursive: true });
for (const [name, hook] of Object.entries(cases)) {
	writeFileSync(path.join(hookDir, `${name}.hook.json`), `${JSON.stringify(hook, null, "\t")}\n`);
	writeFileSync(path.join(bodyDir, `${name}.body.json`), `${captureBody(outcomeV2(hook))}\n`);
}
const manifest = {
	opencode: VERSION,
	binary_sha256: createHash("sha256").update(bin).digest("hex"),
	snippets: Object.fromEntries(Object.entries(sources).map(([k, v]) => [k, createHash("sha256").update(v).digest("hex").slice(0, 16)])),
	cases: Object.keys(cases),
};
writeFileSync(path.join(hookDir, "manifest.json"), `${JSON.stringify(manifest, null, "\t")}\n`);
console.log(`wrote ${Object.keys(cases).length} cases to ${path.relative(repo, hookDir)} and ${path.relative(repo, bodyDir)}`);
