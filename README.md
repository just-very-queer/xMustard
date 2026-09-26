# xMustard

xMustard provides shared, verified memory and repository intelligence for existing
coding agents. It combines a small MCP interface with a Go API and Rust semantic
core, so agents can recover prior knowledge and check it against the current repo.

An implementation candidate now adds bounded, recoverable delivery for
xMustard's own MCP results and one version-pinned Pi extension. It is not a
provider gateway and does not intercept other MCP servers or host-native tools.
The candidate source is now imported into the main working tree and remains
uncommitted. Current implementation verification is complete; the exact diff
still awaits human review and merge. See
[status](docs/STATUS.md), [architecture](docs/ARCHITECTURE.md),
and the [implementation report](docs/reviews/2026-09-24-implementation-results.md).
Agents prepare and verify work; humans retain final merge authority.

The current focus is MCP, CLI, and backend behavior. The default design target is
local operation without Docker, at roughly 50–100 MB process-tree memory. That
candidate workload falls below 100 MB in two same-source sampled runs; this is
not a universal RSS ceiling or proof across workloads. Broader task-quality
benefit and hardware memory bandwidth remain unmeasured. UI development is
outside the current focus.

Start with [vision](docs/VISION.md), [current status](docs/STATUS.md),
[architecture](docs/ARCHITECTURE.md), and [roadmap](docs/ROADMAP.md).
The [documentation map](docs/README.md) separates current guidance from history.

## What xMustard Is

xMustard is **governed runtime memory for coding agents**: a small MCP server that
gives agents such as Claude Code, Codex, and OpenCode two related capabilities:

1. **Grounding** — what changed, what's stale, what's broken, and what's blocked
   since the last baseline, plus the verified facts the agent should trust.
2. **Memory with a trust lifecycle** — an agent proposes a durable fact; other
   agents verify it; only then is it promoted into shared context. Promoted memory
   is re-checked against referenced source paths on recall (drift detection), and
   overlapping memories are surfaced for reconciliation. Current correctness and
   resource-limit gaps are recorded in [status](docs/STATUS.md).

The agent-facing surface has **nine tools**. They use modes/params (e.g.
`search?mode=pattern`, `impact
symbol=/from=/to=`, `recall query=`) rather than split into more tools, and
`tools/call` strictly validates arguments — unknown/wrong-typed/non-scalar/
out-of-enum args are rejected with a JSON-RPC `-32602`, never silently coerced.
The historical narrowing decision is recorded in [`docs/RETHINK.md`](docs/RETHINK.md).

The candidate also advertises MCP `resources` for authorized original-evidence
reads; this does not add a tenth core tool. The Pi extension registers those nine
tools over the Go HTTP API and a separate `xmustard_expand` client tool when a
recovery handle is available. See [`integrations/pi`](integrations/pi/README.md).

Under the hood it sits on a Rust semantic core (tree-sitter symbol graph, change
tracking, hybrid search, live LSP) and a Go HTTP/persistence shell. JSON holds
operational state; Postgres is an optional queryable materialization. The broader
HTTP API supports retained operator workflows; MCP exposes the nine tools.

## Using the MCP tools

The API serves the nine tools over MCP Streamable HTTP at `http://127.0.0.1:8042/mcp`,
from its own process. Clients that take a URL connect to it directly; clients that
can only launch a command use `xmustard-relay`, a native stdio relay of about 2 MiB
RSS (measured: [2026-09-26 relay RSS](docs/benchmarks/2026-09-26-ws13-relay-rss.md)).
The older Go stdio shim `xmustard-mcp` still works but is deprecated: it costs about
13.5 MiB per agent and will be removed.

### Register it

`xmustard-ops mcp-config` prints the entry for one project. It binds the session to
the project's workspace, because HTTP has no working directory to resolve it from:

```bash
xmustard-ops mcp-config --root "$PWD" [--client claude-code] [--mode readonly] [--transport relay]
```

```jsonc
// .mcp.json / client config: URL-capable client
{
  "mcpServers": {
    "xmustard": {
      "type": "http",
      "url": "http://127.0.0.1:8042/mcp?workspace=<id>&client=claude-code",
      "headers": { "Authorization": "Bearer ${XMUSTARD_API_TOKEN}", "X-Xmustard-Workspace": "<id>" }
    }
  }
}
// stdio-only client: the relay (plain http:// only; loopback, or a local TLS proxy)
{
  "mcpServers": {
    "xmustard": {
      "command": "xmustard-relay",
      "args": ["--url", "http://127.0.0.1:8042/mcp", "--workspace", "<id>"],
      "env": { "XMUSTARD_API_TOKEN": "${XMUSTARD_API_TOKEN}" }
    }
  }
}
```

Query parameters on `/mcp` (the relay's flags of the same names): `workspace=<id>`
(or the `X-Xmustard-Workspace` header) binds the session; `mode=readonly` lists and
serves only the seven read tools; `client=claude-code|codex|cursor|opencode|pi|letta`
attributes usage; `schema=lean|full` picks the tools/list schema profile. The token
always travels in `Authorization`, never in the URL. A session belongs to the
principal that opened it; each tool call re-enters the API as that principal, so
caller-scoped tools/list, route gates, evidence resources and registration scope
apply as they do for any other caller. Per-tool usage counters are in
`/api/health` under `mcp_usage` (shown to authenticated callers).

Each agent should authenticate with an `XMUSTARD_API_TOKEN` (mint one with
`xmustard-api mint-token <principal-id> agent`). The authenticated stable
`Principal.ID`, not the bearer secret, is the identity counted by the
multi-agent verification gate. Rotating a token while retaining its principal ID
does not create a new verifier. Never share one principal between verifiers.

**Open vs authenticated trust.** With no tokens minted, the loopback-only API runs
in open mode: every caller is one identity (`anonymous`), so `remember` promotes a
memory at once as `verification_mode: self_asserted_open_mode` instead of leaving it
pending forever. Once tokens exist, every call must authenticate, and a memory becomes
`peer_verified` only after enough distinct principals other than its author approve it
(or is promoted at once as `single_agent` if the operator turned multi-agent
verification off). Open mode is a property of each write: once tokens exist, an
open-mode memory stays self-asserted until a full quorum of distinct principals
approves it, and any authenticated dissent or edit puts it back under that quorum, so
one principal cannot rewrite and re-promote it alone. The id `anonymous` is reserved
and cannot be minted. `recall` shows each entry's mode and counts them in
`verification_modes`; `ground` reports `memory_verification_modes`.

### The nine tools

All tools take `workspace_id` (`?` marks an optional arg). The first four are the
governed-memory loop; the last five are narrow retrieval. `remember` sends its
content in the JSON request body (not the URL), so durable text never leaks into
access logs.

| Tool | Args | What it does |
|------|------|--------------|
| `ground` | — | Orientation before acting: changed / stale / broken / blocked since baseline, with index-trust (drift), contract breaks, and stale-memory count. |
| `recall` | `query?`, `paths?` | The promoted shared context, ranked to your task (lexical + path overlap + approval count). With `query` and/or `paths`, entries matching neither are dropped (query terms need 3+ characters); with no args, nothing is dropped, entries touching working-tree changes are boosted, and recency breaks ties. Each entry carries its `verification_mode` (`peer_verified`, `single_agent`, or `self_asserted_open_mode`) and is re-checked against the live tree; stale ones are flagged. `conflicts` lists memories that cite the same file: path overlap, not semantic contradiction. |
| `remember` | `content`, `title?`, `paths?` | Propose a durable memory (fact / decision / gotcha). `paths` are the files it's about, so recall can flag it stale when they change. Pending until verified; in open mode it is promoted at once as self-asserted. |
| `verify` | `entry_id`, `approve?` | Approve (or reject) a peer's proposed memory; it promotes once enough distinct principals approve. |
| `search` | `query`, `mode?` (`hybrid`\|`pattern`), `lang?`, `seed?` | Code search returning `path:line` slices, not a dump. Default `hybrid` ranks symbol names, file paths and doc chunks, not function bodies: RRF over lexical IDF, char-trigram fuzzy matching (typo tolerance; conceptual matching only when built with the optional `semantic-onnx` feature and `XMUSTARD_EMBED_MODEL` names a local ONNX model directory), inbound-reference degree, and graph proximity to `seed=<symbol>`. `mode=pattern` runs an ast-grep structural query over code. |
| `explain` | `path` | Explain a file or directory: purpose, key symbols, how to run/verify it. |
| `impact` | `symbol?`, `from?`, `to?` | Blast radius over a lexical reference graph (symbol-name matches across files plus import-line heuristics, not resolved calls), so distance ≥ 1 edges are leads to confirm, not proof. No args → current changes (with `contract_break` flags); `symbol=` → files that reference the file(s) defining that name, up to 4 hops (file-level, so a hit may use a different symbol from the same file); `from=`&`to=` → shortest undirected path between the files defining the two names. |
| `diagnostics` | — | Current normalized errors/warnings for the workspace. |
| `why_failed` | `run_id` | Explain why a run failed: failure signals, salient error lines, and which changed files are implicated. |

### A typical session

```text
ground                       → orient: 3 changed files, 1 stale memory
recall                       → read the verified facts (skip the stale one)
search "where is auth"       → find the relevant slice
explain api-go/.../auth.go   → understand it
…do the work…
remember content="auth identity = stable Principal.ID; bearer token is a rotating credential" paths="api-go/.../auth.go"
                             → propose what you learned; peers verify it next
```

### Quick check from a shell

```bash
printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ground","arguments":{"workspace_id":"my-ws"}}}' \
  | xmustard-relay --url http://127.0.0.1:8042/mcp
```

## Supporting capabilities

The source also contains workspace snapshots, issue/run/plan records, verification
profiles, diagnostic history, guidance discovery, provider routing, evaluation
statistics, and review handoffs. These support the memory and intelligence
product; their presence is not evidence that every workflow is complete.

Go owns delivery and persistence. Rust owns semantic meaning and systems-heavy
helpers. The Python backend was retired in June 2026. Current ownership is mapped
in [architecture](docs/ARCHITECTURE.md).

## Repo Layout

- `api-go/`: Go HTTP backend, stdio MCP server, operator CLI, persistence, and Rust bridge
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal runtime, semantic search
- `backend/`: runtime data (`data/`) and SQL schema (`sql/`) only; the Python FastAPI/Typer stack was retired to `archive/2026-06-16-python-backend/`
- `frontend/`: React and TypeScript UI surface (proxies `/api` → `:8042`)
- `integrations/pi/`: version-pinned Pi extension (implementation candidate)
- `archive/`: retired implementations, including the legacy Python backend
- `research/`: local reference repos used for product and architecture study; ignored from git
- `docs/`: current product documents, audits, research, and indexed history

## Build and run

Use a source checkout with Go 1.26+ and a Rust toolchain supporting edition 2024.
Node/npm is required for the Pi extension and optional frontend.

```bash
make build
export XMUSTARD_CORE_BIN="$PWD/rust-core/target/release/xmustard-core"
export XMUSTARD_DATA_DIR="$PWD/backend/data"
./api-go/bin/xmustard-api                     # 127.0.0.1:8042
```

In another shell with the same data-directory configuration, load a repository:

```bash
./api-go/bin/xmustard-ops workspace load --root-path /absolute/path/to/repository
```

Use the returned `workspace_id` in MCP calls (or bind it with `xmustard-ops
mcp-config`). Point a URL-capable MCP client at `http://127.0.0.1:8042/mcp`, or a
stdio-only one at `rust-core/target/release/xmustard-relay`; or put the binaries on
`PATH`. Use an explicit
absolute `XMUSTARD_DATA_DIR` when running outside the source checkout; the current
fallback is relative to the process working directory.

`make backend` starts the development API. `make check-backend` runs the Go tests
and build plus Rust tests and Clippy. `make check-frontend` runs the separate
frontend lint/build checks when UI changes are in scope.

`make install PREFIX=/your/prefix` copies the five binaries. Optional Postgres
bootstrap still needs the SQL schema from the checkout. The Homebrew formula is
a development packaging starting point: there is no published tagged release or
tap verified by the [September audit](docs/STATUS.md).

## Current Status

The September 24 candidate is imported and uncommitted. Current main-worktree
checks pass: backend, provenance-bound MCP and retrieval gates, the pinned Pi
runtime on the current Rust core, and the sampled RSS gate. The current RSS run
measured 80.6 MB; two same-source candidate runs measured 72.3 MB and 84.9 MB.
These are fixed-workload sampled peaks, not a universal RSS ceiling. Human review
of the exact diff and final merge remain yours; held-out task benefit, broad
competitor parity, and hardware memory bandwidth are unproven. See [current
checks and findings](docs/STATUS.md), the [resource benchmark](docs/benchmarks/2026-09-24-lean-context.md),
and the [evidence-gated backlog](docs/ROADMAP.md).

## Architecture

- **Rust core** (`rust-core`) — semantic meaning: tree-sitter symbol graph, change
  tracking/drift, hybrid search, diagnostics, live LSP, the goal runtime.
- **Go shell** (`api-go`) — HTTP API, persistence, auth, and the `xmustard-mcp`
  stdio server; calls the Rust core for the heavy work.
- **Postgres** — durable semantic and operational index (JSON files remain the
  source of truth; Postgres is the queryable materialization).
