# xMustard

xMustard is a local repo-intelligence and operational-memory tool for coding agents.

The point is not to become another tracker. The point is to give an agent a trustworthy answer to:

- what changed
- what matters
- what is already known
- how to run and verify the repo
- what plan or prior run owns the current work

The project is still in active development. The current engineering focus is the CLI and backend surface, with the web UI kept as a secondary consumer rather than the main product trench.

## Public vs Private Docs

This `README.md` is the public GitHub-facing overview.

Deeper migration notes, tranche prompts, closeout logs, and working architecture handoff material live in internal-facing repo docs. The public README should explain what xMustard is, where it is headed, and how to run it without reading like an internal rollout diary.

## What xMustard Is

xMustard is **governed runtime memory for coding agents**: a small MCP server that
gives any agent (Claude Code, codex, opencode, …) two things and nothing else:

1. **Grounding** — what changed, what's stale, what's broken, and what's blocked
   since the last baseline, plus the verified facts the agent should trust.
2. **Memory with a trust lifecycle** — an agent proposes a durable fact; other
   agents verify it; only then is it promoted into shared context. Promoted memory
   is re-checked against the live tree on every recall, so it never goes silently
   stale (drift detection), and overlapping memories are surfaced for reconciliation.

The agent-facing surface is deliberately **nine tools**, not a platform. Current
coding-agent research is consistent that large tool sets bloat an agent's context
and degrade quality; the value is disciplined, governed context, not tool count.
The nine are enriched with modes/params (e.g. `search?mode=pattern`, `impact
symbol=/from=/to=`, `recall query=`) rather than split into more tools, and
`tools/call` strictly validates arguments — unknown/wrong-typed/non-scalar/
out-of-enum args are rejected with a JSON-RPC `-32602`, never silently coerced.
See [`docs/RETHINK.md`](docs/RETHINK.md).

Under the hood it sits on a Rust semantic core (tree-sitter symbol graph, change
tracking, hybrid search, live LSP) and a Go HTTP/persistence shell over Postgres.
That full API stays available for a future UI; the agent only ever sees the nine
tools.

## Using the MCP tools

`xmustard-mcp` is a stdio MCP server. It speaks JSON-RPC 2.0 over stdin/stdout and
proxies to the xMustard HTTP API (`XMUSTARD_API_BASE`, default
`http://127.0.0.1:8042`). Point any MCP client at it.

### Register it (Claude Code example)

```jsonc
// .mcp.json / client config
{
  "mcpServers": {
    "xmustard": {
      "command": "xmustard-mcp",
      "env": {
        "XMUSTARD_API_BASE": "http://127.0.0.1:8042",
        "XMUSTARD_API_TOKEN": "xmt_…"   // optional; required when the API enforces auth
      }
    }
  }
}
```

Each agent should use its **own** `XMUSTARD_API_TOKEN` (mint one with
`xmustard-api mint-token <agent-id> agent`). The token *is* the agent's identity:
the multi-agent verification gate counts distinct authenticated principals, so one
token cannot impersonate several verifiers.

### The nine tools

All tools take `workspace_id` (`?` marks an optional arg). The first four are the
governed-memory loop; the last five are narrow retrieval. `remember` sends its
content in the JSON request body (not the URL), so durable text never leaks into
access logs.

| Tool | Args | What it does |
|------|------|--------------|
| `ground` | — | Orientation before acting: changed / stale / broken / blocked since baseline, with index-trust (drift), contract breaks, and stale-memory count. |
| `recall` | `query?`, `paths?` | The verified shared context to trust, ranked to your task. Each entry is re-checked against the live tree; stale ones are flagged, and path-overlap conflicts are listed. |
| `remember` | `content`, `title?`, `paths?` | Propose a durable memory (fact / decision / gotcha). `paths` are the files it's about, so recall can flag it stale when they change. Pending until verified. |
| `verify` | `entry_id`, `approve?` | Approve (or reject) a peer's proposed memory; it promotes once enough distinct agents approve. Identity is your auth token. |
| `search` | `query`, `mode?` (`hybrid`\|`pattern`), `lang?`, `seed?` | Narrow code search — relevant slices, not a dump. Default `hybrid` fuses lexical + semantic + structural + graph-proximity (RRF); `mode=pattern` runs an ast-grep structural query; `seed=<symbol>` anchors the proximity lane. |
| `explain` | `path` | Explain a file or directory: purpose, key symbols, how to run/verify it. |
| `impact` | `symbol?`, `from?`, `to?` | Blast radius. No args → current changes (with `contract_break` flags); `symbol=` → transitive references (graph BFS); `from=`&`to=` → shortest dependency path between two symbols. |
| `diagnostics` | — | Current normalized errors/warnings for the workspace. |
| `why_failed` | `run_id` | Explain why a run failed: failure signals, salient error lines, and which changed files are implicated. |

### A typical session

```text
ground                       → orient: 3 changed files, 1 stale memory
recall                       → read the verified facts (skip the stale one)
search "where is auth"       → find the relevant slice
explain api-go/.../auth.go   → understand it
…do the work…
remember content="auth identity = bearer token" paths="api-go/.../auth.go"
                             → propose what you learned; peers verify it next
```

### Quick check from a shell

```bash
printf '%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ground","arguments":{"workspace_id":"my-ws"}}}' \
  | XMUSTARD_API_BASE=http://127.0.0.1:8042 xmustard-mcp
```

## What Exists Today

The current codebase already has working surfaces for:

- workspace loading and repo scanning
- worktree state: branch, head, dirty paths, staged and untracked state
- issue records, signals, critiques, improvements, and review artifacts
- plan tracking with ownership, revision history, and attached files
- verification profiles and verification history
- ticket context, threat models, vulnerability records, and browser dumps
- terminal transport and runtime launching
- issue-context packets and replay artifacts
- repo guidance discovery from files like `AGENTS.md` and repo-native config
- CLI surfaces for repo state, changes, run targets, verify targets, changed symbols, impact, repo context, code explainer, path symbols, and semantic index planning

The semantic/runtime layer also includes:

- semantic index planning, execution, and status
- durable semantic baseline storage in Postgres
- stored symbol and semantic row read paths
- freshness and provenance metadata for semantic context
- changed-symbol and impact reporting
- structural retrieval and semantic search
- live `ast-grep`-backed semantic matching

Under the hood, the project is in the middle of an ownership shift:

- Go is taking over delivery and request shaping
- Rust is taking over semantic meaning and systems-heavy execution boundaries
- Postgres remains the durable semantic state layer
- Python is being reduced toward compatibility-only status

## What This Repo Is Becoming

The near-term target is a CLI-first agent cockpit for a single repo.

The core product test is simple: xMustard should be able to answer, through typed CLI and backend surfaces:

- what changed since the last useful baseline
- which symbols and files matter
- whether semantic state is trustworthy
- what commands run the project
- what verification targets matter
- what plans, fixes, and runs are connected to the current change

We are not treating “feature count” as progress. If the system cannot ground an agent in repo reality, the work is not finished.

## Repo Layout

- `api-go/`: Go HTTP shell — the backend (120 routes), request shaping, persistence, calling the Rust core
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal/swarm runtime, data models, semantic search
- `backend/`: runtime data (`data/`) and SQL schema (`sql/`) only; the Python FastAPI/Typer stack was retired to `archive/2026-06-16-python-backend/`
- `frontend/`: React and TypeScript UI surface (proxies `/api` → `:8042`)
- `archive/`: retired implementations, including the legacy Python backend
- `research/`: local reference repos used for product and architecture study; ignored from git
- `docs/`: planning, architecture, handoff notes, prompts, and closeout logs

## Install

Via Homebrew (builds the Rust core and the three Go binaries —
`xmustard-api`, `xmustard-mcp`, `xmustard-ops`):

```bash
brew install --build-from-source ./packaging/homebrew/xmustard.rb
# or, once tapped:  brew install just-very-queer/tap/xmustard
```

## Development

The backend is Go (`api-go`) calling the Rust core (`rust-core`). There is **no
Python** in the project — it was fully migrated to Go + Rust.

```bash
make build                                   # rust core (release) + go binaries
cd api-go && XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api   # or: make backend
```

Checks:

```bash
cd rust-core && cargo test
cd api-go && go test ./...
cd frontend && npm install && npm run lint && npm run build
```

The frontend (optional, for a future UI) expects the backend at
`http://127.0.0.1:8042`.

## Current Status

The governed-memory product is built and verified end to end: the nine-tool MCP
surface, the propose → multi-agent-verify → promote loop, drift-on-recall, conflict
surfacing, bearer-token auth, and the Rust semantic core (tree-sitter symbol graph,
change tracking, hybrid search, live LSP). Two hardening passes have since closed
every feasible P0/P1 (concurrency/lifecycle/confinement, then a deepening pass:
bounded recall, shared PG pool + ordered mirror, index-coverage honesty,
workspace-scoped tokens, strict MCP arg validation). See
[`docs/STATUS.md`](docs/STATUS.md) §6–§7 for the audited evidence, and
[`docs/INDEX_ENGINE.md`](docs/INDEX_ENGINE.md) §Deferred for the two P2 items
intentionally left for later.

## Architecture

- **Rust core** (`rust-core`) — semantic meaning: tree-sitter symbol graph, change
  tracking/drift, hybrid search, diagnostics, live LSP, the goal/swarm runtime.
- **Go shell** (`api-go`) — HTTP API, persistence, auth, and the `xmustard-mcp`
  stdio server; calls the Rust core for the heavy work.
- **Postgres** — durable semantic and operational index (JSON files remain the
  source of truth; Postgres is the queryable materialization).
