# xMustard hooks for Cursor

Cursor (checked against 3.22.7, macOS arm64, and the vendor page fetched 2026-10-03)
gets xMustard through a **command hook**: Cursor has no `http` hook type and no
`mcp_tool` hook type, so every event spawns `xmustard-cursor`, which posts the event to
the daemon's hook service (`/api/hooks/cursor/<event>`) and prints the answer.

## Install

Copy `hooks.json` into the project (`.cursor/hooks.json`, which runs from the project
root) or into `~/.cursor/hooks.json`, and put `xmustard-cursor` on `PATH`:

```
cargo build --release --manifest-path rust-core/Cargo.toml
install -m 0755 rust-core/target/release/xmustard-cursor ~/.local/bin/
```

Start the daemon and register the project once, so the hook has a workspace to bind:

```
xmustard-ops service install
xmustard-ops workspace register --root "$PWD"
```

## The four events this adapter routes

| Event | Answer | What xMustard does |
| --- | --- | --- |
| `sessionStart` | `additional_context` | Ground's summary (failed runs, stale and pending memory) and the core-tier memories, bounded to Cursor's own 10,000-character cap. |
| `preToolUse` (`Shell` only) | `additional_context` | For a search command: the index hits and the memories for its pattern. **The command is not rewritten:** xMustard never rewrites a tool's input, so the answer carries no `updated_input` and the command runs as Cursor's own matcher wrote it. |
| `postToolUse` (MCP tools only) | `updated_mcp_tool_output` and `additional_context` | Captures the tool result (redacted, retained behind a handle) and replaces what the model sees with the projection plus the recovery line. |
| `preCompact` | nothing | Observational. Cursor's output schema for this event takes `user_message` only, and it is not one of the five events whose responses carry `additional_context`, so xMustard sends nothing rather than an out-of-schema member. |

## The failure policy: it fails open

`failClosed` is `false` in `hooks.json`, and `xmustard-cursor` exits **non-zero and never
`2`** on every internal failure. Cursor's exit contract is the reason:

- exit `0` uses the JSON output;
- exit `2` blocks the action (the equivalent of `permission: "deny"`);
- any other non-zero exit fails open while `failClosed` is false.

So a stopped daemon, an unreadable event or a repository outside the token's workspaces
costs an unreduced tool result and a warning on stderr. **The user's agent is never
blocked**, which is the owner's decision (2026-10-03) and the reason the client's failure
exit is its own constant rather than a literal: `FAIL_OPEN_EXIT` is 3, and a test asserts
it is neither 0 nor 2.

A hook that fails *closed* here would mean a daemon outage stopping someone's editor,
which is worse than the unreduced output it was preventing.

## What is deliberately not routed

- **Cursor built-in tools are observe-only.** `updated_mcp_tool_output` is documented as
  "for MCP tools only", so a Shell, Read, Grep, Write or Task result is captured and
  never replaced. That is the replacement surface this adapter has, and it is narrower
  than it first looks.
- **`preToolUse` is registered for `Shell` alone.** `updated_input` is documented on the
  generic hook, so it should apply to every tool type, but it is reported to be
  discarded outside Write and Task. A vendor bug report is not a source, so the matcher
  is the narrow one and widening it needs a measurement against a real Cursor.
- **`afterShellExecution` and `afterMCPExecution` carry the same result as `postToolUse`**
  in two other shapes (`result_json` and raw `output`). Taking all three would give two
  answers for one call.
- **`beforeReadFile` and `beforeShellExecution` are permission hooks.** A hook client that
  stalls there stalls every read, and xMustard has nothing to refuse here.
- **`beforeSubmitPrompt`** is the one event whose `additional_context` spills to a file
  under a 1,000,000-character hard maximum instead of the 10,000-character cap. This
  adapter does not route it.

## Measured, and how

The `additional_context` cap is not in the vendor documentation. It was measured from
the installed 3.22.7 bundle, `out/vs/workbench/workbench.desktop.main.js`:

- `limits.ts` sets `Cbt=1e4`, the 10,000-character cap;
- the injector trims the value and then **throws** `HookAdditionalContextTooLargeError`
  when the trimmed value exceeds it, rather than truncating it;
- every caller of that injector sits inside a hook-execution `try` that only logs, so an
  over-cap answer is dropped whole: never delivered, never shortened.

So the cap is a ceiling the client respects on its own side
(`cursorhook::CURSOR_CONTEXT_CHARS`), and the same number is the policy table's
`clientPolicies["cursor"].ContextChars`.

Two more facts came out of the same bundle and shape the file:

- the tool-name map is `Bash → Shell`, `Edit → Write`, and `Glob` is unsupported, so the
  matchers read `Shell` and `MCP:<tool_name>`;
- `additional_context` is accepted by exactly five events (`sessionStart`,
  `beforeSubmitPrompt`, `preToolUse`, `postToolUse`, `postToolUseFailure`), which is why
  `preCompact` sends nothing.

## Cloud agents

Cursor cloud agents run **command-based project hooks only**, and never `sessionStart`,
`sessionEnd` or `beforeMCPExecution`. So `sessionStart` does not fire in the cloud, and
the other three do, provided the daemon address and token are reachable from the cloud
agent's environment. That has not been run against a cloud agent, so this adapter is
documented as local-only until one does.

## Configuration

| Key | This file | Cursor's default |
| --- | --- | --- |
| `timeout` | `5` | platform default |
| `failClosed` | `false` | `false` |
| `loop_limit` | `5` | `5` |

`failClosed` is set explicitly rather than left to the default so the file states the
failure policy it was written against. `loop_limit` bounds the stop/subagentStop
follow-up loop; none of the four routed events consumes it, so the value is the
documented default.

## Fixtures

`testdata/` holds one input body per routed event and the answer each produces. The
inputs are **source-derived** from Cursor's own schemas in the 3.22.7 bundle, not
recorded from a live session: recording one needs an interactive Cursor session and a
model call, which this workstream does not make. `manifest.json` names the bundle and
version they were derived from so a later session can re-derive or replace them.