# xMustard Pi extension

A [Pi](https://github.com/earendil-works/pi) extension that gives Pi xMustard's nine
tools (`ground`, `recall`, `remember`, `verify`, `search`, `explain`, `impact`,
`diagnostics`, `why_failed`) as direct HTTP calls to the xMustard Go API, with the same
names, descriptions and argument schemas as the stdio MCP server. Large results are
projected by Go's shared evidence module; the model gets a bounded projection plus a
recovery handle, and `xmustard_expand` (inactive until a handle exists) pages the exact
original in 64 KiB pages until it expires.

## Pin

| | |
| --- | --- |
| Researched source | `earendil-works/pi` @ `8676a0dcd8f9f6bca78835e63c8cd31493c4154d` (package.json version `0.87.1`) |
| Installed package | `@earendil-works/pi-coding-agent@0.87.1` (npm, gitHead `f07218c4`), locked in `package-lock.json` |
| Compatibility | `8676a0d` is 17 commits after the `0.87.1` publish. Diffing the two revisions shows the extension API this adapter uses (`registerTool`, `on("tool_result")`, `getActiveTools`/`setActiveTools`, `ExtensionContext.signal`, `registerProvider`) is unchanged; the only extension-surface addition is a `provider_stream_event` event. |
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
| `XMUSTARD_PI_DELIVERY` | `source` | `source` or `hook`; see below |
| `XMUSTARD_PI_TOOL_TIMEOUT_MS` | `60000` | Per-call deadline for tools and `xmustard_expand`; may only be lowered |
| `XMUSTARD_PI_PROJECTION_TIMEOUT_MS` | `5000` | Deadline for the `tool_result` projection POST; may only be lowered |

Loading the extension only registers tools and handlers: it starts no sidecar,
socket, timer or network call.

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

Only the nine xMustard tools are touched; every other tool result passes through
unchanged. A reduced result ends with one line
`[xmustard evidence] {"handle": …, "captured_identity": …, "expires_at": …}`;
`xmustard_expand` answers with a `[xmustard page] {…}` header (offset, next_offset,
eof, freshness, captured/current key) followed by the bytes, as text when the page is
valid UTF-8 on its own and standard base64 otherwise. Tool `details` always carry the
exact base64.

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

## Tests

```bash
npm run check                   # typecheck + unit tests (in-process HTTP server)
../../scripts/e2e/pi-adapter.sh # real Pi CLI + real Go API + Rust core
```

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

This covers the Pi extension seam only. It does not intercept Pi's built-in tools and
makes no claim about other clients.
