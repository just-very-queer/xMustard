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
// Every edge here fails open to the native command. A wrap that cannot be built
// correctly is worse than no wrap: the wrapper's own promise is that the command
// always runs and its result is never lost, so a malformed argv must never
// reach the shell in place of the model's command.

export const WRAPPER = "xmustard-core run";

// A command already addressed to the wrapper, with or without an absolute path
// to the binary, is not wrapped again: `xmustard-core run -- xmustard-core run --
// ls` would make the wrapper run the wrapper, and the inner one would reduce the
// outer's output. The wrapper's own parser accepts `--workspace`, `--api` then
// `--` then argv, so anything starting with that flag prefix is already wrapped.
const ALREADY_WRAPPED = /^(?:\S*\/)?xmustard-core\s+run(?:\s|$)/;

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
	input.command = `${WRAPPER} --workspace ${ws} -- ${command}`;
}

function isRecord(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}