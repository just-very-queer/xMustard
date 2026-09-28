# xMustard plugin for Claude Code

This plugin connects Claude Code to a running xMustard daemon (`xmustard-api`). It does
two things:

- **MCP.** `.mcp.json` adds the nine xMustard tools (`ground`, `recall`, `remember`,
  `verify`, `search`, `explain`, `impact`, `diagnostics`, `why_failed`) through the
  daemon's Streamable HTTP endpoint (`/mcp`). No process is started for them.
- **Hooks.** `hooks/hooks.json` sends Claude Code's hook events to the daemon's hook
  service (`/api/hooks/claude/<Event>`), which reduces native tool output and injects
  verified memory and index hits.

Hooks fail open on the daemon's side. If the daemon is slow (past about 200 ms),
cannot read an event, or the event's directory is outside the token's workspaces, it
answers with an empty 200 and Claude Code keeps its own output and behavior.
Authentication is not relaxed: a missing, expired, rotated or reader-only token gets
401 or 403, which Claude Code shows as a non-blocking hook error on every matched call
(the static client stays silent). No hook allows, denies or rewrites a tool call.

## What each event does

| Event | Hook type | What xMustard does |
| --- | --- | --- |
| `SessionStart` | command (`hooks/bin/xmustard-hook`) | Adds ground's spawn-free summary (failed runs, stale and pending memory) and the core-tier memories as context. Returns `watchPaths`: the files memory is anchored to. |
| `SubagentStart` | http | The same context for the subagent. A subagent has steering state of its own (keyed by its `agent_id`), so a memory pushed into the main thread is pushed into the subagent too, and the reverse. |
| `UserPromptSubmit` | http | Pushes the memories that a keyword in the prompt triggers. A memory tagged `trigger-deploy` is pushed when the prompt says "deploy". |
| `PreToolUse` (Grep, Glob, Bash, Read, Edit, Write, NotebookEdit) | http | For Grep, Glob and a Bash `rg`/`grep`: the index hits (BM25, names, graph proximity) and memories for the pattern. For Read and Edit: the memories bound to the file, stale ones labeled. Before an edit, it records the file's syntax errors. A burst of searches earns one nudge toward `search`/`impact` (2-minute cooldown). |
| `PostToolUse` (Bash, Read, Grep, Glob, WebFetch, other servers' MCP tools) | http | Captures the output (redacted, retained behind a handle for about a day) and, when it was reduced, returns a shape-matched `updatedToolOutput` whose last line names the handle. A test, build or lint output is recorded as a run outcome. A git commit, merge, rebase, cherry-pick or pull adds a freshness notice. |
| `PostToolUse` (Edit, Write, NotebookEdit) | http | Adds the file to the index watcher's pending batch and reports the syntax errors the edit introduced (tree-sitter). |
| `PostToolUseFailure` (Bash) | http | Records the failure as a run outcome and names the failing tests it parsed. |
| `PostToolBatch` | http | Nudges once when one parallel batch ran several searches. |
| `CwdChanged` | http | Returns the `watchPaths` of the new directory's workspace; `[]`, which clears the list, when the directory is in none. |
| `FileChanged` | http | Adds the file to the index watcher's pending batch (when a watcher runs). The next read re-samples the repository identity. |
| `WorktreeRemove` | command | Forgets the worktree's cached identity. It is a command hook because the client always exits 0, and a failing WorktreeRemove hook blocks the removal. |
| `PreCompact`, `PostCompact`, `Stop`, `SubagentStop`, `SessionEnd` | http | Queued and answered at once (SessionEnd hooks share a 1.5 s budget). After compaction, memories may be pushed again. SubagentStop drops the subagent's steering state, SessionEnd the session's. |

`WorktreeCreate` is not hooked: a WorktreeCreate hook replaces Claude Code's own git
worktree creation.

Pushed memory is only memory that a human approver approved, that is not quarantined
and that holds no instruction-like text. It arrives inside `<xmustard-data>` blocks. A
related memory that was held back, by that policy or because it did not fit in the
context left (`too_large`), is counted in one line, with the `recall` call that shows
it.

## Install (manual)

1. Build the static client into the plugin's `hooks/bin/` (not a top-level `bin/`:
   Claude Code puts that on the Bash tool's `PATH`, and claude.ai and Cowork refuse to
   install a plugin that has one):

   ```sh
   cd api-go && go build -o ../integrations/claude-code/hooks/bin/xmustard-hook ./cmd/xmustard-hook
   ```

2. Start the daemon with the resident worker, which serves index hits and syntax
   errors to hooks:

   ```sh
   XMUSTARD_CORE_WORKER=1 xmustard-api
   ```

   Hooks never start a process. Until a resident worker runs and this daemon has built
   the repository's code index (any `search`, `explain` or `impact` call does it), hooks
   add no index hits and no syntax report. Memory, capture and ground work without it.

3. Register the repository with the daemon (the MCP tools register the client's root
   on first use; `POST /api/workspaces/load` does it directly), and give Claude Code
   an agent token:

   ```sh
   xmustard-api mint-token claude-1 agent   # prints the token
   export XMUSTARD_API_TOKEN=<token>
   ```

   On a loopback daemon with no tokens minted, no token is needed.

4. Load the plugin:

   ```sh
   claude --plugin-dir integrations/claude-code
   ```

   Or add the directory to a plugin marketplace and install it from there.

## Configuration

| Variable | Read by | Meaning |
| --- | --- | --- |
| `XMUSTARD_API_TOKEN` | http hooks, `xmustard-hook`, MCP | Bearer token. Hook routes need the proposer role (an `agent` token has it). |
| `XMUSTARD_WORKSPACE_ID` | http hooks, `xmustard-hook` | Optional. Pins every event to one workspace. Without it, the daemon uses the registered root that holds the event's `cwd`. |
| `XMUSTARD_API_BASE` | MCP, `xmustard-hook` | Daemon address, default `http://127.0.0.1:8042`. The client uses it when no daemon listens on the socket. |
| `XMUSTARD_HOOK_SOCKET` | daemon, `xmustard-hook` | The Unix socket; default `$XDG_RUNTIME_DIR/xmustard/hook.sock`, else `xmustard-<uid>/hook.sock` in the temp dir. Both sides use it only when its directory is owned by the user and closed to others (mode 0700) and the socket is the user's; otherwise the client goes to TCP. `off` disables it. |
| `XMUSTARD_HOOK_TIMEOUT_MS` | `xmustard-hook` | The client's budget, default 200. |
| `XMUSTARD_HOOK_BUDGET_MS` | daemon | How long a hook answer may take, default 180. |

The http hook URLs in `hooks/hooks.json` name `127.0.0.1:8042`, because Claude Code
does not expand variables in a hook URL. Edit them if the daemon listens elsewhere.
Each hook has an explicit `timeout` (2 s, SessionEnd 1 s); the daemon answers well
inside it. With the daemon down, an http hook shows a non-blocking hook error in the
transcript; the command hooks stay silent.

## What is not built

- PreToolUse `updatedInput` command wrapping (it needs WS-41's `xmustard-core run`).
- Compiler and language-server diagnostics in the post-edit delta: it reports
  tree-sitter syntax errors only.
- `mcp_tool` hooks: SessionStart takes command and `mcp_tool` hooks only, and Claude
  Code skips `mcp_tool` hooks at launch, so SessionStart uses the static client.

## Fixtures

`testdata/*.in.json` are hook bodies in the shapes of the Claude Code hooks reference
(read 2026-09-28), with `${ROOT}` for the repository. `testdata/golden/*.out.json` are
the daemon's answers in one scripted session (`TestHookGoldenSession` in
`api-go/cmd/xmustard-api/hooks_routes_test.go`); an empty file is an empty answer.
`XMUSTARD_UPDATE_GOLDEN=1` rewrites them.
