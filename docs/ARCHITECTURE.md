# Current architecture

Source baseline: `cd13e2b` on `feat/product-v1`, inspected 2026-09-24. The
implementation described below is the reviewed candidate now imported into the
main working tree. It remains uncommitted; post-import implementation gates pass,
with the human exact-diff review and merge decision remaining.
Product direction: [shared governed memory and repository intelligence](VISION.md).

The [context layer](CONTEXT_LAYER.md) describes both the current bounded candidate
and later design targets. The candidate adds only xMustard's own MCP delivery and
one Pi extension; it is not a general upstream MCP or provider gateway.

## Execution and storage

```text
Existing coding agent
  -> xmustard-mcp (stdio JSON-RPC; nine tools)
  -> xmustard-api (Go HTTP, default 127.0.0.1:8042)
       -> workspaceops (memory, auth, operational records, coordination)
       -> evidence (scoped originals, bounded projection, recovery/resources)
       -> rustcore bridge -> xmustard-core subprocess (repository semantics)
       -> JSON files (operational write authority)
       -> optional Postgres (queryable materialization / semantic state)

xmustard-ops -> workspaceops directly (local administrative CLI)
Pinned Pi extension -> xmustard-api (nine tools + conditional expand adapter)
Optional React UI -> full HTTP surface
```

The Rust core is invoked on demand. A resident index daemon is not the current
architecture. Graph and per-file symbol caches, plus the tracked-file hash stat
cache that change tracking uses, live under `.git/xmustard-cache/` for Git
repositories. The default Rust build does not enable `semantic-onnx`.

## Module map

| Responsibility | Source owner | Important interface / constraint |
| --- | --- | --- |
| MCP protocol and tool schemas | `api-go/internal/mcpserver/` (tool table, one `tool_<name>.go` per tool); `api-go/cmd/xmustard-mcp/` (stdio transport, evidence resources: pages and search) | Nine tools; closed schemas with bounds; version negotiation (2025-06-18, 2024-11-05); `tools/list` limited to the caller's usable tools (`GET /api/auth/whoami`); optional `workspace_id` resolution; HTTP proxy; bounded framing |
| HTTP, authentication, request limits | `api-go/cmd/xmustard-api/` | Go request routing and policy; route gate table (profile, role, read-only) and exposure middleware ([SECURITY](SECURITY.md)) |
| Scoped evidence delivery | `api-go/internal/evidence/`, API/MCP evidence routes | Admission, stable opaque scoped handles, byte-safe original pages and search, projection and expiry. The nine tools use `xm-reduce/1`; any tool's output captured through `POST .../evidence/capture` (raw or a Claude, Codex, Cursor, Pi or OpenCode hook body, stream-decoded) is reduced by a versioned tool-family reducer (`registry.go`) and shaped and validated per client (`shapes.go`). A capture may pass a lower-only `target` (1 KiB to 1 MiB, `ObservationInput.Target`; 400 `invalid_target` otherwise) so a client can retain a small output behind a handle before masking or compacting it; each retained original counts at least `MinRetainedCharge` (16 KiB) against the workspace quota, which bounds the number of originals and their metadata. Capture is refused (503 `redaction_unavailable`) until a streaming secret redactor is wired, because originals are retained and searchable; enforced byte admission is not an RSS ceiling |
| Pi client adapter | `integrations/pi/` | Pinned extension uses the shared Go evidence path; nine existing tools plus `xmustard_expand` (pages or search) only when a handle is issued |
| Local operator commands | `api-go/cmd/xmustard-ops/` | Calls stores directly; local filesystem authority, not HTTP-token isolation |
| Memory proposals, votes, recall and drift | `api-go/internal/workspaceops/context_governance.go` | Content, trust state, path hashes, ranking and conflict reporting |
| Agent grounding and outcome feedback | `grounding.go`, `feedback.go`, `verifier_telemetry.go` in `workspaceops` | Compose current evidence and persist inspectable feedback |
| JSON persistence, auth, workspace scope | `workspaceops` store/auth/workspace files | Operational records under the configured data directory |
| Postgres materialization | `workspaceops/pg*.go`, `backend/sql/` | Optional; JSON remains operational write authority |
| Rust process invocation | `api-go/internal/rustcore/` | Binary resolution, subprocess output, deadlines and wire shaping |
| Repository files, roles, graph and coverage | `rust-core/src/symbolgraph.rs`, `indexcache.rs`, `treesitter.rs`, `repomap.rs` | Bounded file access, graph/cache generation, extracted symbols |
| Search, impact and change tracking | `search.rs`, `semantic.rs`, `changetrack.rs`, `hashcache.rs` | Retrieval lanes, graph traversal, baseline and signature differences; stat-keyed file-hash cache with a racy-timestamp guard |
| Diagnostics and live language servers | `diagnostics.rs`, `lsp.rs`, `lsp_session.rs`; Go LSP adapters | LSP is optional; transient results do not require Postgres |
| Verification and retained goal runtime | `verification.rs`, `goalruntime.rs`; Go run control | Process execution, evidence and persisted operational state |
| Resource accounting | `api-go/internal/budget/` | 24 MiB transient-byte pool and helper-child limit: work that does not fit now gets 503/-32000, work that could never fit the pool gets a permanent answer (413, -32600 or a tool error); static component reservations; one heavy slot (bounded wait, owner label) behind an RSS watchdog over the owned tree plus the stdio shims, with no production callers yet, so it governs nothing today; Go memory limit at the daemon's 28 MiB line with a GOGC floor; data-movement counters; the `/api/health` budget block, which needs a bearer token while auth is enforced. Byte admission is still not a complete RSS bound |
| Shared wire models | Go request/record structs, each Rust module's own `Serialize` output types, `frontend/src/lib/types.ts` | Contract changes need matching consumers |
| Optional operator UI | `frontend/src/` | Full API consumer; outside the current development focus |

Paths without a prefix in Rust rows are under `rust-core/src/`. Go module names
above identify actual ownership, not a proposed additional abstraction layer.

## Public agent interface

`ground`, `recall`, `remember`, `verify`, `search`, `explain`, `impact`,
`diagnostics`, and `why_failed` remain the nine MCP tools. Candidate MCP also
advertises resources/list and resources/read for authorized recovery; this is not
a tenth tool. The Pi client separately exposes `xmustard_expand` when a recovery
handle is emitted. The HTTP/CLI platform is much larger; its existence does not
expand the supported agent interface.

Use the root [README](../README.md) for arguments and setup. The API serves the
core profile by default; `XMUSTARD_PROFILE=platform` adds the platform routes the UI
uses ([SECURITY](SECURITY.md)). The profile restricts HTTP routing; it does not itself
establish a 100 MB resource ceiling. Authenticated agents need distinct principals
for independent verification, and `verify` needs the `verifier` role.

## Current seams that need work

The earlier [Go audit](reviews/2026-09-24-go-audit.md) and
[Rust audit](reviews/2026-09-24-rust-audit.md) record baseline defects. Candidate
repairs and their evidence are tracked in the [implementation reports](reviews/2026-09-24-implementation-results.md)
and [Rust report](reviews/2026-09-24-rust-implementation-results.md). MCP and Pi
checks have passed on their stated fixtures. The fixed sampled process-tree RSS
gate passes on the imported tree at 80.6 MB, following two same-source candidate
runs at 72.3 MB and 84.9 MB; see the [benchmark and raw evidence](benchmarks/2026-09-24-lean-context.md).
This is a sampled result for one workload, not a universal RSS ceiling. The
implementation gates pass; human exact-diff review and merge remain. Do not infer
bounded, fresh behavior for untested paths or parity beyond these reports.

The operational package and HTTP entrypoint are large. Extracting modules should
follow concrete shared invariants, such as one recall transaction or one process
lifecycle, with tests through their interface. Moving source directories alone
would not repair these defects and would disturb existing build/runtime paths.

## Historical and local material

- `backend/` holds data and SQL, not an active Python application.
- `archive/2026-06-16-python-backend/` is a local retired implementation.
- `archive/codex-sessions/`, historical handoffs, `docs/prompts/`, and `goal/`
  preserve provenance. They are not runtime inputs or current specifications.
- `research/` contains local reference clones and is ignored by Git.
- Some old `backend/data/` records are already tracked despite ignore rules;
  they need a separate, deliberate repository-hygiene review.

The previous architecture document is preserved in
[history](history/architecture-before-2026-09-24.md).
