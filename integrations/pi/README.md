# xMustard Pi extension

A [Pi](https://github.com/earendil-works/pi) extension that gives Pi xMustard's nine
tools (`ground`, `recall`, `remember`, `verify`, `search`, `explain`, `impact`,
`diagnostics`, `why_failed`) as direct HTTP calls to the xMustard Go API, with the same
names, descriptions and argument schemas as the stdio MCP server. Large results are
projected by Go's shared evidence module; the model gets a bounded projection plus a
recovery handle, and `xmustard_expand` (inactive until a handle exists) pages the exact
original in 64 KiB pages until it expires.

It also reduces what Pi's own tools put into context:

- **Built-in tools** (`bash`, `read`, `grep`, `find`, `ls`, `edit`, `write`): a result
  larger than the projection target goes through Go's universal capture route and is
  replaced by its tool-family projection plus a recovery line. `isError` and `details`
  are kept.
- **Masking** at `turn_end`: tool results older than a number of turns become
  `[xmustard masked: …; handle …]` stubs through Pi `context_edit` entries. The mask
  advances only in polling windows. The latest failure and files being edited are
  never masked.
- **Compaction** at `session_before_compact`: a deterministic snapshot of at most 2 KB
  replaces Pi's model-written summary. Every output it removes stays recoverable:
  larger outputs by their own handle, everything else through one retained index
  document that the snapshot names.

## Pin

| | |
| --- | --- |
| Researched source | `earendil-works/pi` @ `8676a0dcd8f9f6bca78835e63c8cd31493c4154d` (package.json version `0.87.1`) |
| Installed package | `@earendil-works/pi-coding-agent@0.87.1` (npm, gitHead `f07218c4`), locked in `package-lock.json` |
| Compatibility | `8676a0d` is 17 commits after the `0.87.1` publish. Diffing the two revisions shows the extension API this adapter uses (`registerTool`, `on("tool_result")`, `getActiveTools`/`setActiveTools`, `ExtensionContext.signal`, `registerProvider`) is unchanged; the only extension-surface addition is a `provider_stream_event` event. The masking and compaction hooks (`turn_end` with `context_edit` boundary entries, `session_before_compact` with a custom `CompactionResult`) are read from the installed 0.87.1 `dist` (`core/extensions/types.d.ts`, `core/agent-session.js`) and exercised by the e2e. |
| Node | >= 22.18 (tests run `.ts` directly with default type stripping) |

## Use

```bash
cd integrations/pi && npm ci --ignore-scripts      # project-local; nothing global
XMUSTARD_API_BASE=http://127.0.0.1:8042 XMUSTARD_TOKEN=... \
  ./node_modules/.bin/pi -e ./src/index.ts
```

Or list this directory as a Pi package (`package.json` declares `pi.extensions`).

| Variable | Default | Meaning |
| --- | --- | --- |
| `XMUSTARD_API_BASE` | `http://127.0.0.1:8042` | Go API base URL |
| `XMUSTARD_TOKEN` | unset | Bearer token (needs `agent` role for capture). Sent as a header, never logged or echoed. Required when the API enforces auth. |
| `XMUSTARD_WORKSPACE_ID` | unset | Workspace for calls that omit `workspace_id`. Unset: the registered workspace whose root contains Pi's working directory (longest root wins). The adapter never registers a repository; an unregistered directory is an explicit error. |
| `XMUSTARD_PI_DELIVERY` | `source` | `source` or `hook`; see below |
| `XMUSTARD_PI_TOOL_TIMEOUT_MS` | `60000` | Per-call deadline for tools and `xmustard_expand`; may only be lowered |
| `XMUSTARD_PI_PROJECTION_TIMEOUT_MS` | `5000` | Deadline for each projection or capture POST (and the workspace lookup those make); may only be lowered |
| `XMUSTARD_PI_BUILTINS` | all seven | Comma-separated built-ins projected through capture (`bash,read,grep,find,ls,edit,write`), or `none` |
| `XMUSTARD_PI_PROJECTION_TARGET_BYTES` | `32768` | Built-in results at or below this pass through; larger ones are projected to about it. Go's Pi policy target; may only be lowered (min 1024) |
| `XMUSTARD_PI_MASK` | on | `off` disables `turn_end` masking |
| `XMUSTARD_PI_MASK_AFTER_TURNS` | `10` | A result older than this many turns may be masked |
| `XMUSTARD_PI_MASK_EVERY_TURNS` | `5` | The mask advances only on turns divisible by this (the polling window) |
| `XMUSTARD_PI_MASK_MIN_BYTES` | `2048` | Smaller results are never masked (min 1025) |
| `XMUSTARD_PI_COMPACTION` | on | `off` keeps Pi's own compaction |

Loading the extension only registers tools and handlers: it starts no sidecar,
socket, timer or network call. At `session_start` it asks `GET /api/auth/whoami` (2 s
bound) which of the nine tools this caller may use, as the MCP shim does for
`tools/list`, and deactivates the rest: a reader token is not offered `remember` or
`verify`. When the API cannot say (unreachable, 401, an older API) all nine stay active
and the API still enforces every call.

## Delivery paths and provenance

- **source** (default): `execute` calls the tool route with
  `X-Xmustard-Delivery: xmustard.evidence/v1`. Go captures the handler's output and
  samples repository identity before and after it, so results are
  `captured_identity: "bound"` and pages report `current` or `stale`.
- **hook**: `execute` fetches the raw result and the `tool_result` hook POSTs it to
  `/api/workspaces/{ws}/evidence`. Go receives bytes produced earlier by an untrusted
  client, so these observations are always `captured_identity: "unknown"` and every
  page is `stale: true`. The adapter never supplies a repository identity of its own.
  The `args_digest` it sends uses the Go middleware's formula (tested against Go's
  values); it is client-supplied audit metadata that Go does not verify.

These two paths serve the nine xMustard tools. Pi's built-ins go through the capture
route (below); results of any other tool pass through unchanged. A reduced result ends
with one line
`[xmustard evidence] {"handle": …, "captured_identity": …, "expires_at": …}`;
`xmustard_expand` answers with a `[xmustard page] {…}` header (offset, next_offset,
eof, freshness, captured/current key) followed by the bytes, as text when the page is
valid UTF-8 on its own and standard base64 otherwise. Tool `details` always carry the
exact base64. With `pattern` (RE2), `query` (literal) or `lines` (`A-B`),
`xmustard_expand` searches the original instead (Go scans it in 1 MiB chunks up to
`max_matches`, default 40) and answers with a `[xmustard search] {…}` header
(matches, next_offset, next_line, freshness) followed by numbered lines (`12:` a
match, `11-` context); pass `offset=next_offset` with `start_line=next_line` to
continue.

Failure behavior:

| Case | Result |
| --- | --- |
| Go unreachable, HTTP >= 400, timeout, abort | Error result with a bounded explicit message |
| Tool reported an error | Stays an error, in both delivery paths |
| Projection fails (hook path), original <= 64 KiB | Original passed through byte-exact; reason in `details.projection_error` |
| Projection fails, original > 64 KiB | Explicit size error (error result); original not delivered |
| Response larger than bound (16 MiB raw, 8 MiB envelope, 1 MiB page) | Explicit size error |
| Expired / revoked / other principal / unknown handle | Explicit `410` / `403` / `404` error; never re-served |

`execute` forwards Pi's `AbortSignal`, so an aborted turn closes the HTTP request and Go
cancels the handler. The projection uses `ctx.signal` when Pi supplies one. In real Pi
runs `ctx.signal` is always present in `tool_result`, so the signal-absent path is proven
by unit tests only: there, the deadline alone bounds the request.

## Built-in tools

The `tool_result` handler looks at `bash`, `read`, `grep`, `find`, `ls`, `edit` and
`write` (or the `XMUSTARD_PI_BUILTINS` subset). A result of text blocks larger than the
projection target (32 KiB) is posted as a Pi `tool_result` body (`toolName`,
`toolCallId`, `sessionId`, `input`, `content`, `isError`; not `details`, which the model
never sees) to `POST /api/workspaces/{ws}/evidence/capture?format=pi&client=pi`. Go
redacts it into the spool, selects the tool-family reducer (shell, test, read, grep,
list, …) and returns the projection plus a handle. The adapter replaces the content with
the projection and the `[xmustard evidence]` line, leaves `isError` as Pi set it, and
keeps Pi's `details` with an `xmustard` member added (path, handle, raw and projected
bytes, reducer, family). Captured bytes arrive after the fact, so they are always
`captured_identity: "unknown"`.

Smaller results, results with images, and results of other tools are not projected
and do not reach Go here (Go would return a small result unchanged); only compaction's
index (below) carries them, so that nothing compacted away is lost. The session's
workspace is resolved as for the nine tools, within the projection deadline. Built-in
captures send `tool_version=pi-coding-agent/<Pi VERSION>`.

`xmustard_expand` is inactive at `session_start` unless the branch already names a
handle (a mask stub, an xMustard compaction or a projected result), so a resumed,
reloaded or forked session can still recover what it masked or compacted.

## Masking (turn_end)

Each `turn_end`, the adapter counts the assistant turns on the branch. On turns
divisible by `XMUSTARD_PI_MASK_EVERY_TURNS` (the polling window), it appends a
`context_edit` for every tool result that is older than `XMUSTARD_PI_MASK_AFTER_TURNS`
turns and at least `XMUSTARD_PI_MASK_MIN_BYTES`. The edit replaces the result's
model-visible content with a stub:

```text
[xmustard masked: 800 lines, 3092 bytes of bash output; turn 1; handle xm1.…; workspace_id w] Recover it with xmustard_expand(workspace_id="w", handle="xm1.…", offset=0), or search it with pattern=<RE2> or lines=A-B.
```

A masked error keeps `isError` and adds its first and last non-empty lines to the stub.
Between windows, no earlier message changes, so the provider's prompt cache survives.
These results are never masked:

- the latest failing result on the branch;
- a `read`, `edit` or `write` result for a file that an `edit` or `write` changed
  within the last `AFTER_TURNS` turns;
- tools other than the built-ins, the nine xMustard tools and `xmustard_expand`;
- results with images.

A result that already has a handle reuses it: a built-in or xMustard projection, or an
`xmustard_expand` page, whose stub then names the page offset. Any other result is
first retained through the capture route with `format=raw&target=1024`, so the handle
recovers exactly the text the model saw. If that fails, the result stays unmasked.
Handles expire (24 h by default). A handle that expires within an hour counts as
absent, so the text is retained again under a fresh one, and the stub names the
expiry (`; expires <RFC 3339>`). When a stub's handle has expired, the next `turn_end`
(window or not) rewrites it from the raw entry, which the session still holds.
Tool paths resolve as Pi's tools resolve them (`@` prefix, `~`, `file://`, unicode
spaces), so `@src/a.go` and `src/a.go` are the same active file.
Pi's session is append-only: the original entry is kept and only the `context_edit`
entry is added. Pi composes boundary handlers, so the adapter returns the entries that
earlier handlers proposed plus its own, and skips targets another handler already
edits.

## Compaction (session_before_compact)

The adapter returns its own `CompactionResult`, so Pi does not call a model to
summarize:

- `summary`: at most 2 KB of UTF-8, in priority tiers:
  1. a header that says the snapshot is derived from this session and is not
     verified memory;
  2. the goal (the first user message) and the latest request;
  3. up to 3 open failures (a failure with no later success of the same command or
     path), each with its handle and its last line of tool output;
  4. modified and read files;
  5. memory proposals still pending, and this session's memory decisions (remember
     results with their verification mode, verify verdicts);
  6. the last progress (the last assistant text) and an earlier non-xMustard
     summary;
  7. as many recoverable outputs as fit, newest first, then `(+N more in the index)`.
- the index: when everything does not fit in 2 KB, one document is retained
  (`tool=pi_compaction`) and named on the summary's second line. It holds the
  snapshot unclipped, every open failure and handle (with expiry), an earlier
  model-written summary in full, and the compacted messages, with tool outputs above
  1 KiB by handle and smaller ones inline. When the whole document fits in 2 KB, it is
  the summary and no index is retained.
- `details`: `readFiles` and `modifiedFiles` (Pi's shape), and
  `xmustard: {version: "xmustard.pi-compaction/v1", derived: true,
  verified_memory: false, workspace_id, snapshot, handles[], carried, expired_dropped,
  index?, summary_bytes}`. Every summarized tool output above 1 KiB has a live handle
  (one that outlives the next hour): a live existing one is reused, the rest are
  retained first (`target=1024`). Handles recorded by an earlier xMustard compaction
  are carried forward (up to 1,024 in details; the index names all of them); expired
  ones are retained again from the branch or counted in `expired_dropped`.

Pi's own compaction runs instead in three cases:

- the user gave `/compact` instructions, which only Pi's summarizer can honor;
- capture is unavailable (paused after an outage, unreachable, or no redactor) while
  some summarized output still needs a handle;
- retaining any output or the index fails, more than 512 outputs would need
  retaining, or the index would exceed 8 MiB.

Failure behavior of the capture paths:

| Case | Result |
| --- | --- |
| Capture refused (`503 redaction_unavailable`, or 401/403 for this principal), Go unreachable or hung | Pi's own result stays; the reason goes into `details.xmustard.reason`; capture pauses for 30 s |
| Capture refused for one output (507 quota, 413 size) | Pi's own result stays, with the reason; capture stays on |
| Go could not shape a Pi payload (`shape.mode` not `replace`) | Pi's own result stays; the reason goes into `details.xmustard.reason` |
| Workspace not resolved | Pi's own result stays, with the reason; masking only masks results that already have a handle |
| Retaining one masked result fails | That result stays unmasked; the others are masked |

## Tests

```bash
npm run check                   # typecheck + unit tests (in-process HTTP server)
../../scripts/e2e/pi-adapter.sh # real Pi CLI + real Go API + Rust core
```

`src/tools.ts` mirrors `api-go/internal/mcpserver/tool_<name>.go`. The unit tests
compare it with the tools/list snapshot that the Go tests generate
(`api-go/internal/mcpserver/testdata/tools_list.json`; regenerate with
`XMUSTARD_UPDATE_GOLDEN=1 go test ./internal/mcpserver`), so a description or schema
changed on one side fails without the live e2e.

The e2e needs native PostgreSQL server binaries (`initdb`, `postgres`, `psql`) on PATH.
`diagnostics` reads its baseline from Postgres (optional and off in xMustard's default),
so the script starts a disposable cluster: temp dir, loopback on a random port, private
socket, deleted afterwards. It configures the cluster through the existing settings and
bootstrap routes and seeds one diagnostic through `diagnostics/run`. Without the binaries
the script stops, unless `XM_E2E_ALLOW_NO_POSTGRES=1`, which accepts diagnostics as
error-only.

The e2e drives the pinned `pi` CLI (JSON and RPC modes). The model is pi-ai's faux
transport scripted by `test/fixtures/scripted-provider.ts`, which records every model
request. No provider is contacted, and each Pi run gets an empty HOME, an empty agent
dir and a scrubbed environment. Coverage: schema conformance with MCP `tools/list`;
all nine tools succeeding (diagnostics through the Postgres fixture); results reaching
the next model request; errors staying errors;
`xmustard_expand` activation and exact 64 KiB paging; bound, stale and unknown labels;
API restart; expiry; two concurrent Pi processes; RPC abort; tool and projection
deadlines; unreachable Go, including an activated `xmustard_expand` whose endpoint
fails; pass-through of successful and failing `read`/`bash`; auth (401 without a token,
principal binding, token rotation, cross-principal denial, no token in output). The
e2e also samples RSS every 100 ms per process tree (xMustard, Pi, Postgres fixture);
see `summary.json` → `resources`.

The e2e builds the API with `-tags xmustard_e2e`, which wires a test-only capture
redactor (`api-go/cmd/xmustard-api/capture_redactor_e2e.go`, one fixed marker). A
production build has no capture redactor until the WS-05 streaming redactor is wired,
so there `POST .../evidence/capture` answers `503 redaction_unavailable` and built-in
results, masking and compaction fall back as described above. WS-24 coverage: a
failing `bash` and a large `read` projected with handles (isError and Pi's `details`
kept, the secret marker redacted before retention, the original paged back exactly),
small built-ins untouched, masking stubs appearing only after the window turns while
the latest failure and a file edited in the window stay, stubs recovering their
originals and the raw session entries kept (`get_entries`), a snapshot compaction via
RPC `compact` reaching the next model request with every summarized output
recoverable from `details`, and a reader token not offered `remember`/`verify`.

Not built here: `tool_call` input rewriting to wrap commands (PAR-ADP-09) needs the
`xmustard-core run --` wrapper (WS-41) and is redundant in Pi, where `tool_result`
replaces results directly.
