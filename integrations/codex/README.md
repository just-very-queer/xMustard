# xMustard hooks for Codex

Codex (codex-cli, checked against 0.156.1 and codex-rs at 44fe510) gets xMustard in two
parts:

- **MCP.** The nine tools, from the `[mcp_servers.xmustard]` table that
  `xmustard-ops mcp-config --root "$PWD" --client codex` prints for
  `~/.codex/config.toml`. Codex sends an MCP server no roots, so that table carries the
  `X-Xmustard-Workspace` header that binds the project.
- **Hooks.** `hooks.json` runs the static client `xmustard-hook` for each Codex event
  xMustard answers. The client posts the event to the daemon's hook service
  (`/api/hooks/codex/<Event>`) and prints the answer. Codex has no `http` hook
  handler, only `command` and `mcp_tool`, so every event starts the client.

## What each event does

| Event | Answer | What xMustard does |
| --- | --- | --- |
| `SessionStart` | `additionalContext` | Ground's spawn-free summary (failed runs, stale and pending memory) and the core-tier memories. Codex fires it again with `source: "compact"` after a compaction, and the memories are pushed again. |
| `SubagentStart` | `additionalContext` | The same for the subagent. It has steering state of its own, keyed by its `agent_id`. |
| `UserPromptSubmit` | `additionalContext` | The memories that a keyword in the prompt triggers (tag `trigger-<keyword>`). |
| `PreToolUse` (`Bash`, `apply_patch`) | `additionalContext` | For an `rg` or `grep` command: the index hits and the memories for its pattern. For `apply_patch`: the memories bound to the files the patch touches, and their syntax errors kept as the baseline. The tool call itself is never changed. |
| `PostToolUse` (`Bash`, MCP tools of other servers) | `decision: "block"` and `reason` | Captures the output (redacted, retained behind a handle for about a day). An output larger than the codex target (8,464 bytes) comes back as a block whose `reason` replaces what the model sees: the reduction, with the recovery line (`[xmustard evidence] {...handle...}`) last. A smaller output is left alone. A test, build or lint run is recorded as an outcome. A git command that moves HEAD adds a freshness notice as `additionalContext`. |
| `PostToolUse` (`apply_patch`) | `additionalContext` | Adds the patched files to the index watcher's pending batch and reports the syntax errors the patch introduced (tree-sitter). |
| `PostCompact`, `SubagentStop`, `SessionEnd` | nothing | Queued and answered at once. After compaction, memories may be pushed again. SubagentStop drops the subagent's steering state, SessionEnd the session's. |

`Stop` and `PreCompact` are not hooked. They would only mark the queue until WS-33
gives them work, and every hook costs Codex a login shell and a client start.

### How a replaced output reads

Codex hands the model a PostToolUse block's `reason` in place of the tool output, as
the call's output (codex-rs `core/src/tools/registry.rs`). The reason is kept within
10,000 bytes. Past that size (2,500 approximate tokens) Codex writes hook text to a
file, and the model gets only a head-and-tail preview. The recovery line comes last so
that it survives such a cut.

The block also replaces Codex's header for the call, and that header's `Process
exited with code N` is the only place Codex gives a shell command's exit status. The
hook payload carries only the output text (`core/src/tools/context.rs`). So a replaced
`Bash` output starts with a line saying the exit status is unknown. The exact status
exists only in Codex's app-server stream (`item/completed`), which WS-34 plans to read.

Codex hands a hook its own preview of a long command output, with the middle already
cut (about 40 KB). A failure in that middle is gone before xMustard sees it. A failed
MCP call, and a command still running when the call returns, reach no hook at all.

## Install

1. Build the static client (a 3.5 MB binary that starts in a few milliseconds) and put
   it where Codex's shell finds it, or note its absolute path:

   ```sh
   cd api-go && go build -o ~/.local/bin/xmustard-hook ./cmd/xmustard-hook
   ```

2. Start the daemon with the resident worker (index hits and syntax errors come from
   it), register the repository, and give Codex an agent token in its environment. The
   hook client and Codex's MCP client both read `XMUSTARD_API_TOKEN`:

   ```sh
   XMUSTARD_CORE_WORKER=1 xmustard-api &
   xmustard-ops workspace load --root-path "$PWD"
   xmustard-api mint-token codex-1 agent   # prints the token
   export XMUSTARD_API_TOKEN=<token>
   ```

   Codex sends no MCP roots, so the tools cannot register the repository on first use
   as they do for Claude Code; register it once. On a loopback daemon with no tokens
   minted, no token is needed.

3. Write the hooks. For every project, in `~/.codex/hooks.json`, the daemon binds each
   event to the registered root that holds its `cwd`:

   ```sh
   xmustard-ops codex-hooks config --hook-bin ~/.local/bin/xmustard-hook > ~/.codex/hooks.json
   ```

   For one project, in `<repo>/.codex/hooks.json`, pin its workspace. Codex loads a
   project's `.codex/` only once the project is trusted in Codex:

   ```sh
   xmustard-ops codex-hooks config --root "$PWD" --hook-bin ~/.local/bin/xmustard-hook > .codex/hooks.json
   ```

   `--hook-bin` must be `xmustard-hook` (found on Codex's `PATH`) or an absolute path
   to it; a relative path would run whatever the repository holds at that path. A
   `--workspace-id` must be a safe id (letters, digits, `_`, `.`, `-`). Merge by hand
   if the file already has hooks. `hooks.json` in this directory is the default output
   (client on `PATH`, no pin).

4. Trust them. Codex runs a user or project hook only while the hash of its definition
   is trusted, and it records that in `config.toml` under `hooks.state`. Nothing in
   xMustard sets trust on its own. Either review the hooks in Codex's `/hooks` view, or
   run:

   ```sh
   xmustard-ops codex-hooks trust --cwd "$PWD" --hook-bin ~/.local/bin/xmustard-hook
   ```

   Pass the same `--hook-bin` as to `config` (leave it out for a client on `PATH`).
   This asks `codex app-server` which hooks Codex would run from that directory
   (`hooks/list`). It shows xMustard's untrusted or modified hooks: only commands
   exactly of the form `<hook-bin> codex <Event> [--workspace <id>]`, printed quoted.
   A hook that runs another `xmustard-hook`, such as one a repository's
   `.codex/hooks.json` points at, is not offered; the report counts these and names
   their clients. It records the hooks' current hashes (`config/batchWrite` on
   `hooks.state`) only after you type `trust` at the terminal. No flag skips that
   prompt. It refuses without a terminal, and inside an agent's shell (when
   `CODEX_THREAD_ID`, `CODEX_SANDBOX`, `CODEX_SANDBOX_NETWORK_DISABLED` or
   `CLAUDECODE` is set), where the agent could answer the prompt itself. If you edit a
   hook later (a new path or pin), its hash changes, and Codex stops running it until
   you trust it again. `xmustard-ops codex-hooks status --hook-bin <path>` lists the
   hooks and their trust, and exits 3 when an event is unregistered or untrusted.

## Configuration

| Variable | Meaning |
| --- | --- |
| `XMUSTARD_API_TOKEN` | Bearer token (the proposer role; an `agent` token has it). Codex passes its environment to hooks. |
| `XMUSTARD_WORKSPACE_ID` | Pins every event to one workspace when the command has no `--workspace`. |
| `XMUSTARD_API_BASE` | Daemon address when no daemon listens on the socket, default `http://127.0.0.1:8042`. |
| `XMUSTARD_HOOK_SOCKET` | The daemon's Unix socket (see the Claude Code README). `off` disables it. |
| `XMUSTARD_HOOK_TIMEOUT_MS` | The client's budget, default 200. |

Each hook in `hooks.json` has a 5 s timeout (SessionEnd 2 s, Codex's cap is 3 s).
Codex runs a command hook through `$SHELL -lc`, so the timeout also covers the login
shell's start. The client fails open: with the daemon down, slow, or refusing the
token, it prints nothing and exits 0, so Codex keeps its own output.

## What is not built

- `mcp_tool` handlers. Codex reads an `mcp_tool` handler's text as command output
  (`core/src/hook_mcp_executor.rs`), so one could return a block without a process.
  Its arguments are templated field by field, though, and a missing field fails the
  hook. The root agent's calls carry no `agent_id`, so a template could not name the
  subagent, and the route would need a tool beyond the nine.
- PreToolUse `updatedInput` (wrapping a command so its exit status is printed). Codex
  takes it only with `permissionDecision: "allow"`, and xMustard does not decide
  permissions. It needs WS-41's `xmustard-core run`.
- A Codex plugin (`.codex-plugin` with `.mcp.json` and these hooks) and a prebuilt
  client: WS-44.

## Fixtures

- `testdata/*.in.json` are hook bodies derived from codex-rs `hooks/src/schema.rs` at
  44fe510 (the `*CommandInput` structs, serialized as Codex does), with `${ROOT}` for
  the repository. They are source-derived, not recordings.
- `testdata/golden/*.out.json` are the daemon's answers in one scripted session
  (`TestCodexHookGoldenSession` in `api-go/cmd/xmustard-api/hooks_codex_test.go`). An
  empty file is an empty answer. Every answer passes Codex's output contract
  (`hooks.ValidateCodexAnswer`: exactly the keys its `deny_unknown_fields` wires take).
  `XMUSTARD_UPDATE_GOLDEN=1` rewrites them.
- `testdata/recorded/0.156.1/` are bodies codex-cli 0.156.1 (linux-x64) sent for
  SessionStart (startup, and compact after a compaction), UserPromptSubmit,
  PreToolUse, PostToolUse, PreCompact, PostCompact, Stop and SessionEnd. They were
  recorded with no model call by
  `api-go/internal/evidence/testdata/codex/record/record.sh <out-dir>`, which runs
  `codex exec` against a local mock of the Responses API (a compaction is forced with
  `-c model_auto_compact_token_limit=100`), and then copied from `<out-dir>/events/`.
  `TestCodexFixturesMatchTheRecordedPayloads` checks that the source-derived bodies
  carry exactly their fields, and that each recorded body gets an answer Codex accepts.
- SubagentStart and SubagentStop need a real subagent. The mock cannot start one:
  0.156.1 refuses its `spawn_agent` call as unsupported, with or without
  `--enable multi_agent`. Recording them takes model calls, so it is an operator step.
  With codex-cli 0.156.1 signed in (credentials in `~/.codex/auth.json`, the default
  store), run this from the repository root. It uses a scratch `CODEX_HOME`, so your
  own hooks and config are not touched:

  ```sh
  repo=$PWD scratch=$(mktemp -d)
  export CODEX_HOME="$scratch/home"
  mkdir -p "$CODEX_HOME" "$scratch/app" && cp ~/.codex/auth.json "$CODEX_HOME/"
  hook="node $repo/api-go/internal/evidence/testdata/codex/record/hook.mjs $scratch/subagent.jsonl"
  cat > "$CODEX_HOME/hooks.json" <<EOF
  {"hooks": {
    "SubagentStart": [{"hooks": [{"type": "command", "command": "$hook"}]}],
    "SubagentStop": [{"hooks": [{"type": "command", "command": "$hook"}]}]
  }}
  EOF
  (cd "$scratch/app" && codex exec --json --skip-git-repo-check --dangerously-bypass-hook-trust -s read-only \
    "Spawn one explorer subagent to list the files here, wait for it, then answer done.")
  unset CODEX_HOME
  ```

  Then copy each event's first line of `$scratch/subagent.jsonl` into
  `testdata/recorded/0.156.1/<Event>.json`, with `$scratch/home` replaced by
  `/Users/dev/.codex` and `$scratch/app` by `/Users/dev/src/app`, as the recorder
  does.
- `TestCodexHooksEndToEnd` runs the installed codex-cli with `hooks.json` from
  `CodexHooksJSON`, a freshly built client and a test daemon against the mock model,
  and checks what the model is sent. It needs `XMUSTARD_CODEX_BIN` set to the codex
  binary and `node` on `PATH`; it is skipped otherwise.
