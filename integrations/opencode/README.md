# xMustard plugin for OpenCode

This plugin connects OpenCode to a running xMustard daemon (`xmustard-api`). It is
written against the OpenCode 2.x plugin API and checked against the installed
OpenCode 2.0.1; one default export also carries a 1.x entrypoint. It does three things:

- **MCP.** It adds the xMustard server (the nine tools, over the daemon's Streamable
  HTTP endpoint `/mcp`) unless your config already has a server of that name. The entry
  binds the session to the project's workspace.
- **Reduction.** When a tool result is larger than the projection target (32 KiB), the
  plugin posts the text the model would read to xMustard's capture route. xMustard
  retains the original behind a handle and sends back a projection that ends with a
  recovery line. The model reads the projection instead. OpenCode's structured
  `output` and its metadata stay as they were.
- **Masking (2.x).** A result older than 10 assistant turns is replaced, in each model
  request, by a one-line stub naming its handle. The stored session keeps the original.

Every failure leaves OpenCode's own result in place. This covers the daemon being
unreachable, a capture being refused, and a result xMustard could not shape. After an
outage, or a refusal because no secret redactor is configured, the plugin stops asking
for 30 seconds.

## Install

1. Start the daemon and load the repository (`xmustard-ops workspace load --root-path
   /abs/repo`), as in the top-level README.
2. Set `XMUSTARD_API_TOKEN` (and `XMUSTARD_API_BASE` if the daemon is not on
   `http://127.0.0.1:8042`) in the environment OpenCode starts from.
3. Add the plugin's directory to `opencode.json` (the project's or
   `~/.config/opencode/opencode.json`). A configured plugin must be a directory; OpenCode
   2.0.1 loads its `server` or `index` module, here `index.ts`, with Bun, so nothing is
   built or installed:

```jsonc
{
  "plugins": [
    "/abs/path/to/xMustard/integrations/opencode"
    // or, with options:
    // { "package": "/abs/path/to/xMustard/integrations/opencode", "options": { "projectionTarget": 16384 } }
  ]
}
```

OpenCode 1.x takes `"plugin": ["/abs/path/.../integrations/opencode"]`, with options
as `[path, options]`. It loads the package's `main` and calls the default export's
`server`. This path was checked against the 1.18.33 source only; no 1.x is installed
here.

Without the plugin, `xmustard-ops mcp-config --root "$PWD" --client opencode` prints
the MCP entry alone, in 2.x's form (`mcp.servers.xmustard`). OpenCode substitutes
`{env:XMUSTARD_API_TOKEN}` when it reads the file. OpenCode 1.x takes the same entry
one level up, under `mcp.xmustard`, and 2.x still reads that form.

## Options

An option in the plugin entry wins over its environment variable. Budgets and timeouts
can only be lowered.

| Option | Variable | Default | Meaning |
| --- | --- | --- | --- |
| — | `XMUSTARD_API_BASE` | `http://127.0.0.1:8042` | The daemon. Read from the environment only, so a repository's config cannot send the token elsewhere. |
| — | `XMUSTARD_API_TOKEN` (or `XMUSTARD_TOKEN`) | none | Bearer token. Read from the environment only. |
| `workspaceId` | `XMUSTARD_WORKSPACE_ID` | the registered workspace containing OpenCode's directory | Which workspace captures and the MCP session use. |
| `server` | — | `xmustard` | The MCP entry's name, and so its tools' prefix (`xmustard_recall`). |
| `mcp` | `XMUSTARD_OPENCODE_MCP` | on | `false`/`off`: do not add the MCP entry. |
| `tools` | `XMUSTARD_OPENCODE_TOOLS` | `shell, bash, grep, glob, list, webfetch, execute` | Tools whose results are reduced (an array, a comma list, or `none`). |
| `foreignMcp` | `XMUSTARD_OPENCODE_FOREIGN_MCP` | on | Also reduce other MCP servers' tools called directly. |
| `projectionTarget` | `XMUSTARD_OPENCODE_PROJECTION_TARGET_BYTES` | 32768 | Results at or below this many bytes pass through. |
| `captureTimeoutMs` | `XMUSTARD_OPENCODE_CAPTURE_TIMEOUT_MS` | 5000 | Per capture. |
| `mask` | `XMUSTARD_OPENCODE_MASK` (`off`) | on | `false`, or `{ afterTurns: 10, everyTurns: 5, minBytes: 2048 }`. Also `XMUSTARD_OPENCODE_MASK_AFTER_TURNS`, `_EVERY_TURNS`, `_MIN_BYTES`. |

## What each hook does

| API | Hook | What the plugin does |
| --- | --- | --- |
| 2.x | `mcp.transform` | Adds the `xmustard` remote entry: `url` with `client=opencode` and `workspace`, `Authorization` and `X-Xmustard-Workspace` headers, `oauth: false`. OpenCode 2.0.1 appends `codemode=false` to a remote URL; the daemon ignores it. |
| 2.x | tool `execute.after` | Reduces a large text result of a listed tool, or of another server's MCP tool called directly: `result.content` becomes the projection and `metadata` gains `truncated: true` and an `xmustard` record (handle, sizes, reducer). A failed call's message is reduced the same way, through the error's own class. Calls made inside a code-mode `execute` program are left to the program, and the `execute` result is reduced like any other. |
| 2.x | tool `execute.before` | Tracks `execute` calls in flight, to tell code-mode calls apart. |
| 2.x | session `context` | Masks older results in the request's messages (copies; nothing stored changes). The set of masked results only grows every 5 turns, so the prompt prefix stays stable between windows. The latest failure is never masked. |
| 1.x | `config` | Adds the same MCP entry under `mcp`. |
| 1.x | `tool.execute.after` | Reduces a built-in's `output.output` or an MCP result's `content`. 1.x never passes a failed call to this hook, and masking has no 1.x seam here. |

Left alone: `read` (OpenCode numbers and pages it itself, so xMustard only observes
it), edits, results with images or files, xMustard's own nine tools (already bounded
at the source), and anything at or below the target.

OpenCode 2.x reaches MCP tools through code mode (`execute`) unless a server entry
sets `codemode: false`. To call the nine tools directly, define the `xmustard` entry
yourself with `"codemode": false`; the plugin never replaces an entry of that name.

Recovery: the last line of a projection names the handle and an `xmustard://evidence/…`
resource URI. Read the resource through the MCP server, or search the original with
`GET /api/workspaces/{ws}/evidence/search?handle=…&pattern=…`.

## The capture contract

The plugin posts one flat body to `POST /api/workspaces/{ws}/evidence/capture?format=opencode&client=opencode`:

```json
{ "tool": "shell", "sessionID": "ses_…", "id": "call_…", "input": { "command": "go test ./..." },
  "content": "<the text the model reads>", "metadata": { "exit": 1, "truncated": false } }
```

A failed call posts `"error": "<message>"` instead of `content`. Only number and
boolean metadata travel. The structured 2.x `result.output` and 1.x `metadata.output`
repeat the text and are never posted. The answer's `shape.payload` is
`{ "content": "<projection>" }` (`api-go/internal/evidence/shapes.go`, opencodeShaper).

## Checks

- `node --test test/unit.test.ts` runs the unit tests against a fake daemon.
- `npm run typecheck` (after `npm install`) checks the code against `@opencode/plugin` 2.0.1.
- `node test/fixtures/record.mjs` re-records `test/fixtures/2.0.1/*.hook.json` and the Go
  capture bodies in `api-go/internal/evidence/testdata/opencode/2.0.1/`. It runs the tool
  result code of the installed OpenCode 2.0.1 binary, found there byte for byte. No
  model runs, and OpenCode is not started.
- `node test/smoke/smoke.mjs` runs the installed OpenCode (`opencode2`) three times in a
  throwaway home. A local fake plays the daemon and an OpenAI-compatible model, so no
  paid model runs. The smoke checks the plugin load, the MCP binding, a reduced shell
  result reaching the model, the `mcp-config` form, and masking.
