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
architecture. Graph and per-file symbol caches live under `.git/xmustard-cache/`
for Git repositories. The default Rust build does not enable `semantic-onnx`.

## Module map

| Responsibility | Source owner | Important interface / constraint |
| --- | --- | --- |
| MCP protocol and tool schemas | `api-go/cmd/xmustard-mcp/` | Nine tools; strict arguments; HTTP proxy; bounded framing |
| HTTP, authentication, request limits | `api-go/cmd/xmustard-api/` | Go request routing and policy; core-only route allowlist |
| Scoped evidence delivery | `api-go/internal/evidence/`, API/MCP evidence routes | Admission, stable opaque scoped handles, byte-safe original pages, projection and expiry; enforced byte admission is not an RSS ceiling |
| Pi client adapter | `integrations/pi/` | Pinned extension uses the shared Go evidence path; nine existing tools plus `xmustard_expand` only when a handle is issued |
| Local operator commands | `api-go/cmd/xmustard-ops/` | Calls stores directly; local filesystem authority, not HTTP-token isolation |
| Memory proposals, votes, recall and drift | `api-go/internal/workspaceops/context_governance.go` | Content, trust state, path hashes, ranking and conflict reporting |
| Agent grounding and outcome feedback | `grounding.go`, `feedback.go`, `verifier_telemetry.go` in `workspaceops` | Compose current evidence and persist inspectable feedback |
| JSON persistence, auth, workspace scope | `workspaceops` store/auth/workspace files | Operational records under the configured data directory |
| Postgres materialization | `workspaceops/pg*.go`, `backend/sql/` | Optional; JSON remains operational write authority |
| Rust process invocation | `api-go/internal/rustcore/` | Binary resolution, subprocess output, deadlines and wire shaping |
| Repository files, roles, graph and coverage | `rust-core/src/symbolgraph.rs`, `indexcache.rs`, `treesitter.rs`, `repomap.rs` | Bounded file access, graph/cache generation, extracted symbols |
| Search, impact and change tracking | `search.rs`, `semantic.rs`, `changetrack.rs` | Retrieval lanes, graph traversal, baseline and signature differences |
| Diagnostics and live language servers | `diagnostics.rs`, `lsp.rs`, `lsp_session.rs`; Go LSP adapters | LSP is optional; transient results do not require Postgres |
| Verification and retained goal runtime | `verification.rs`, `goalruntime.rs`; Go run control | Process execution, evidence and persisted operational state |
| Resource accounting | `api-go/internal/budget/` | Shared transient-byte accounting; currently not a complete RSS bound |
| Shared wire models | Go request/record structs, Rust `models.rs`, `frontend/src/lib/types.ts` | Contract changes need matching consumers |
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

Use the root [README](../README.md) for arguments and setup. `XMUSTARD_CORE_ONLY=1`
restricts HTTP routing; it does not itself establish a 100 MB resource ceiling.
Authenticated agents need distinct principals for independent verification.

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
