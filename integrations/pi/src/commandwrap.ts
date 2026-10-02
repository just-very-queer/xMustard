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

// mutateBashInput rewrites a bash tool call's command in place. It does nothing
// unless the knob is on, the tool is `bash`, a workspace resolved and the command
// is a non-blank string that is not already wrapped.
export function mutateBashInput(event: unknown, workspaceId: string | undefined, enabled: boolean): void {
	if (!enabled) return;
	if (!isRecord(event) || event.toolName !== "bash") return;
	const input = event.input;
	if (!isRecord(input) || typeof input.command !== "string") return;
	const ws = workspaceId?.trim();
	if (!ws) return;
	const command = input.command;
	if (command.trim() === "" || ALREADY_WRAPPED.test(command.trim())) return;
	input.command = `${WRAPPER} --workspace ${shellQuote(ws)} -- ${SHELL} ${SHELL_FLAG} ${shellQuote(command)}`;
}

function isRecord(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

// shouldWrapBash is the tool_call hook's decision, as a pure function of what the
// hook can see. It is kept out of the hook body so the two refusals are testable
// without a running Pi extension host: a tool that is not bash, and a session whose
// token the wrapper cannot be handed.
//
// The second refusal is not a nicety. The wrapper authenticates with
// XMUSTARD_API_TOKEN from its own environment (rust-core/src/run_wrapper.rs) and this
// adapter reads XMUSTARD_TOKEN; a tool_call hook cannot change the child
// environment. Putting the token on the command line instead would write it into
// Pi's session transcript, the masking walk and every capture body. So the wrap
// happens only when the wrapper can authenticate with exactly the credential this
// process holds — the same value, not merely the presence of a variable. Two
// different tokens would make the wrapper's capture POST a 401 and fall back to
// unreduced output, which is the very thing the wrap exists to prevent. When the
// tokens cannot be reconciled the command runs natively and projectBuiltin captures
// its result through the adapter, which can authenticate.
export function shouldWrapBash(
	event: unknown,
	opts: { enabled: boolean; tokenConfigured: boolean; wrapperToken: string | undefined },
): boolean {
	if (!opts.enabled) return false;
	if (!isRecord(event) || event.toolName !== "bash") return false;
	if (!opts.tokenConfigured) return true;
	return !!opts.wrapperToken;
}