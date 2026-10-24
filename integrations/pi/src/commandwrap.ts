// WS-44e (PAR-ADP-09): rewrite a Pi bash tool call's `command` to WS-41's
// `xmustard-core run --` wrapper, so the raw output is reduced and retained
// server-side instead of arriving whole in the model's context.
//
// Pi's extension API fires `tool_call` before every tool executes and states
// "`event.input` is mutable. Mutate it in place to patch tool arguments before
// execution ... No re-validation is performed after mutation"
// (@earendil-works/pi-coding-agent 0.87.1, dist/core/extensions/types.d.ts,
// ToolCallEvent), which is why this mutates rather than returning a replacement:
// ToolCallEventResult has no input field, only block/reason/terminate.
//
// Pi runs `command` through a shell, but the wrapper execs the argv program after
// `--` directly (rust-core/src/run_wrapper.rs: `Command::new(&argv[0])`). A command
// like `cd sub && make` therefore has to be handed to a shell explicitly, or the
// wrapper would try to exec `cd`. So the wrapped form is
// `xmustard-core run --workspace <id> -- bash -c <quoted command>`: the shell sees
// the exact string Pi would have run, inside the wrapper's argv, and every byte it
// prints is still reduced and retained.
//
// Every edge here fails open to the native command. A wrap that cannot be built
// correctly is worse than no wrap: the wrapper's own promise is that the command
// always runs and its result is never lost, so a malformed argv must never reach
// the shell in place of the model's command.

import { accessSync, constants, statSync } from "node:fs";
import { randomBytes } from "node:crypto";
import { delimiter, join } from "node:path";
import { normalizeApiBase } from "./config.ts";
import { resultCarriesHandle } from "./delivery.ts";

export const WRAPPER = "xmustard-core run";
// The program the wrapped command runs as, and the flag that gives it the string.
// `bash`, not `sh`: the tool being wrapped is Pi's `bash`, whose semantics are
// bash's, and on a Debian-family host /bin/sh is dash, where `[[ ... ]]`, arrays
// and process substitution fail. Measured on the build box: /bin/sh -> dash,
// `[[ 1 == 1 ]]` exits 127 and `a=(x y)` exits 2 there.
const SHELL = "bash";
const SHELL_FLAG = "-c";

// A command already addressed to the wrapper, with or without an absolute path
// to the binary, is not wrapped again: `xmustard-core run -- xmustard-core run --
// ls` would make the wrapper run the wrapper, and the inner one would reduce the
// outer's output.
const ALREADY_WRAPPED = /^(?:\S*\/)?xmustard-core\s+run(?:\s|$)/;

// shellQuote wraps s in single quotes for POSIX sh, escaping an embedded quote by
// closing, inserting an escaped quote and reopening: 'a'"'"'b'. The workspace id is
// quoted the same way even though transport.ValidWorkspaceID would reject anything
// outside [_.-A-Za-z0-9]: the adapter's resolver never runs that check, so the id
// arrives here from XMUSTARD_WORKSPACE_ID as the user wrote it.
export function shellQuote(s: string): string {
	return `'${s.replaceAll("'", `'"'"'`)}'`;
}

// mutateBashInput rewrites a bash tool call's command in place, and reports whether
// it did. It does nothing unless the knob is on, the tool is `bash`, a workspace
// resolved and the command is a non-blank string that is not already wrapped.
//
// The boolean is what the tool_call hook records as provenance, so it has to be the
// rewrite's own answer rather than the hook's assumption: a command that was blank,
// malformed or already wrapped ran natively, and recording it as wrapped would let
// its result's text stand in for a capture that never happened.
export function mutateBashInput(event: unknown, workspaceId: string | undefined, enabled: boolean, token?: string): boolean {
	if (!enabled) return false;
	if (!isRecord(event) || event.toolName !== "bash") return false;
	const input = event.input;
	if (!isRecord(input) || typeof input.command !== "string") return false;
	const ws = workspaceId?.trim();
	if (!ws) return false;
	const command = input.command;
	const candidate = command.trim().replace(/^XMUSTARD_WRAP_TOKEN='[0-9a-f]{32}'\s+/, "");
	if (candidate === "" || ALREADY_WRAPPED.test(candidate)) return false;
	const wrapToken = token ?? randomBytes(16).toString("hex");
	input.command = `XMUSTARD_WRAP_TOKEN=${shellQuote(wrapToken)} ${WRAPPER} --workspace ${shellQuote(ws)} -- ${SHELL} ${SHELL_FLAG} ${shellQuote(command)}`;
	return true;
}

// The tool_call hook's own record step: rewrite the command, and record the call only
// if the rewrite happened. The hook calls this rather than pairing mutateBashInput with
// rememberWrapped at its call site, so the record follows the rewrite's answer in the
// one place both are visible — a hook that recorded unconditionally would let a command
// the rewrite declined skip capture on the strength of a footer-shaped line, and a test
// that re-paired the two at its own call site could not see that.
export function recordWrappedCall(calls: WrappedCalls, event: unknown, workspaceId: string | undefined, enabled: boolean): boolean {
	const token = randomBytes(16).toString("hex");
	if (!mutateBashInput(event, workspaceId, enabled, token)) return false;
	rememberWrapped(calls, event, token);
	return true;
}

// The tool_call hook records here that it rewrote a call, and the tool_result hook
// consumes that record once. It is how the result side knows a handle in the output
// is the wrapper's own rather than a line any untrusted result could print.
//
// The id is Pi's, not ours, and a result can arrive for a call whose id was never
// recorded (a resumed session, a hook registered mid-batch), which reads as unwrapped
// and so captures. The map is therefore capped and prunes the oldest entry, rather
// than growing with a long session.
export type WrappedCalls = Map<string, string>;

const WRAPPED_CALL_LIMIT = 256;

export function newWrappedCalls(): WrappedCalls {
	return new Map<string, string>();
}

// rememberWrapped records one rewritten call. It is a named function so the tool_call
// hook stays a sequence of decisions and a unit test can reach the bookkeeping without
// an ExtensionAPI.
export function rememberWrapped(calls: WrappedCalls, event: unknown, token = ""): void {
	if (!isRecord(event) || typeof event.toolCallId !== "string") return;
	if (calls.size >= WRAPPED_CALL_LIMIT) {
		const oldest = calls.keys().next();
		if (!oldest.done) calls.delete(oldest.value);
	}
	calls.set(event.toolCallId, token);
}

function isRecord(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

// shouldWrapBash is the tool_call hook's decision, as a pure function of what the
// hook can see. It is kept out of the hook body so the refusals are testable
// without a running Pi extension host: a tool that is not bash, and a session whose
// API the wrapper cannot be pointed at with a credential it holds.
//
// Both refusals are about the wrapper's ability to reach the same server this
// adapter uses. The wrapper authenticates with XMUSTARD_API_TOKEN from its own
// environment (rust-core/src/run_wrapper.rs) and this adapter reads XMUSTARD_TOKEN;
// a tool_call hook cannot change the child environment, and a token on the command
// line would be written into Pi's session transcript, the masking walk and every
// capture body. So the argv may only name the wrapper when the wrapper would reach
// the same endpoint with a credential this process holds: the base it is pointed at
// must be this adapter's, and when a token is configured the wrapper's own must be
// the same one. Otherwise the command runs natively and projectBuiltin captures its
// result through the adapter, which can authenticate.
export function shouldWrapBash(
	event: unknown,
	opts: {
		enabled: boolean;
		wrapperAvailable: boolean;
		token: string | undefined;
		wrapperToken: string | undefined;
		wrapperApiBase: string | undefined;
		apiBase: string;
	},
): boolean {
	if (!opts.enabled) return false;
	if (!isRecord(event) || event.toolName !== "bash") return false;
	// No executable wrapper means the rewritten command would exit 127 and the
	// model's own command would never run, so the call stays native.
	if (!opts.wrapperAvailable) return false;
	// The wrapper's effective endpoint, not just an explicit one: with
	// XMUSTARD_API_BASE unset it falls back to its own DEFAULT_API, so a session
	// pointed at a different server would capture there and issue a handle this
	// adapter cannot spend.
	if (wrapperEndpoint(opts.wrapperApiBase) !== opts.apiBase) return false;
	// The wrapper speaks plain http on loopback only (run_wrapper.rs: an upload to
	// any other host is refused before it starts), so an https or remote adapter
	// base can never be reached by it and the capture would fail open to native
	// output instead of the promised projection.
	if (!isLoopbackHttp(opts.apiBase)) return false;
	// The handle the wrapper issues is spent through this adapter, so both sides must
	// hold the same effective credential, including both holding none: a wrapper
	// token with no adapter token captures fine and then fails xmustard_expand.
	// Trimmed, because the adapter trims XMUSTARD_TOKEN and the wrapper's nonblank()
	// ignores whitespace only.
	return effectiveToken(opts.token) === effectiveToken(opts.wrapperToken);
}

// The wrapper's own default when XMUSTARD_API_BASE is unset or blank
// (run_wrapper.rs DEFAULT_API). It is a second constant in a different language,
// and the two can disagree without either test noticing, so the wrapper's value is
// named here and the Rust one is asserted equal to it in the Rust test suite.
export const WRAPPER_DEFAULT_API = "http://127.0.0.1:8042";

// Where the wrapper would actually upload: its default when nothing is set, the
// normalized value when something is.
export function wrapperEndpoint(raw: string | undefined): string {
	const set = raw?.trim();
	return set ? normalizeApiBase(set) : normalizeApiBase(WRAPPER_DEFAULT_API);
}

// Whether the wrapper can reach this base at all: plain http, on loopback, and no
// path, query or fragment. Mirrors run_wrapper.rs `split_api` and `is_loopback` and
// its refusal of every other scheme. The 127/8 test is on the parsed address, not on
// the hostname's text: `127.0.0.1.example` parses to a name, not an address.
export function isLoopbackHttp(base: string): boolean {
	let url: URL;
	try {
		url = new URL(base);
	} catch {
		return false;
	}
	if (url.protocol !== "http:") return false;
	// The wrapper's split_api keeps the authority and discards the path
	// (run_wrapper.rs: `rest.split('/').next()`), so a base with a path prefix would
	// have the adapter addressing /xm/api/... and the wrapper posting to /api/... on
	// the same host. Equality on the two strings is not enough: the endpoints differ
	// even though the strings match.
	if (url.pathname !== "/" && url.pathname !== "") return false;
	if (url.search !== "" || url.hash !== "") return false;
	const host = url.hostname.replace(/^\[|\]$/g, "");
	if (host === "localhost" || host === "::1") return true;
	const octets = host.split(".");
	return octets.length === 4 && octets.every((o) => /^\d{1,3}$/.test(o) && Number(o) <= 255) && octets[0] === "127";
}

function effectiveToken(t: string | undefined): string | undefined {
	return t?.trim() || undefined;
}

// wrapperOnPath reports whether `xmustard-core` resolves to an executable file in
// one of the PATH entries (a regular file, not a directory), the same lookup the shell will do for the wrapped argv.
export function wrapperOnPath(path: string | undefined): boolean {
	return (path ?? "").split(delimiter).filter(Boolean).some((dir) => {
		try {
			const bin = join(dir, "xmustard-core");
			// A directory is X_OK too, and the shell cannot exec it.
			accessSync(bin, constants.X_OK);
			return statSync(bin).isFile();
		} catch {
			return false;
		}
	});
}

// shouldActivateExpand reports whether a finished tool result must offer
// xmustard_expand. A wrapper projection already carries its handle in the text and
// never reaches the capture path that would otherwise notice, so the rule is its own
// function rather than a line in the tool_result hook: the hook needs a live
// ExtensionAPI and the rule is what the hook calls, so a unit test can reach the
// rule but not the call. It is deliberately independent of the tool and of the
// built-ins set — whether a result is then projected is that set's decision, and a
// handle the model cannot spend is worse than a large result.
export function shouldActivateExpand(content: unknown[]): boolean {
	return resultCarriesHandle(content);
}