# Plan: database-free diagnostics baselines

Status: proposed next-stage design for Fable review. This is a plan only; it
does not claim implementation or acceptance. It preserves the existing nine
MCP tools, human final merge authority, and configured PostgreSQL behavior.

## Goal and scope

Make explicitly imported diagnostic evidence durable when no PostgreSQL DSN is
configured, so the existing `diagnostics` MCP tool can return a useful
baseline after restart. Keep the behavior behind the existing diagnostics
interfaces and routes. Do not add a daemon, UI, MCP tool, automatic compiler
launch, automatic LSP collection, or general SQL-correctness project.

`auto_scan` is not a diagnostics producer: workspace scanning emits scanner
discovery signals and snapshots (`api-go/internal/workspaceops/workspace_scan.go`),
not compiler/LSP baseline evidence. Use an explicit import/collection action.
First support the existing `POST /api/workspaces/{workspace_id}/diagnostics/run`
and `xmustard-ops diagnostics run --input-path …` paths. Keep existing HTTP,
MCP, Pi, and CLI contracts. The diagnostics route allowed by `CORE_ONLY` remains
GET-only and is not widened; this does not change existing write operations on
other core tools. Preserve the existing `GET /diagnostics/live` behavior as a
separate explicit operation; a normal diagnostics read must not start a collector.

## Storage seam and selection

Add one small `DiagnosticBaselineStore` module behind the current operations in
`api-go/internal/workspaceops/diagnostics.go` (`RunDiagnostics`,
`ReadDiagnosticsCtx`, and `ReadDiagnosticsStatus`). Its interface should cover
atomic publish, latest/by-ID reads, and scoped status; keep selection and policy
in one composition point rather than scattering DSN checks across routes.
Retain the current PostgreSQL implementation as one adapter and add a bounded
local-file adapter as the other. Resolve storage once per operation:

- An explicit request DSN or configured DSN selects PostgreSQL. Connection,
  schema, and write failures are errors; never silently fall back to local files.
- With neither an explicit nor configured DSN, select the local adapter. Do not
  attempt a PostgreSQL connection.
- Do not migrate, mirror, or reconcile PostgreSQL and local histories in this
  stage. Existing PostgreSQL read/write semantics remain unchanged.

The HTTP wiring is in `api-go/cmd/xmustard-api/main.go:2197,2225,2250` near the
diagnostics GET, live, and POST `/diagnostics/run` handlers. The CLI producer/read
paths are in `api-go/cmd/xmustard-ops/main.go:47-108` (`runDiagnostics`). Keep the nine MCP tool names
and schemas unchanged (`api-go/cmd/xmustard-mcp/main.go`); Pi continues to call
the existing `diagnostics` route (`integrations/pi/src/tools.ts`). Public response
changes are additive optional fields only; preserve `DiagnosticsReadResult`,
normalized rows, provenance, replay warnings, run IDs, and status meanings.

## Evidence and freshness contract

Capture the bounded input once into immutable bytes, then normalize and archive
those exact bytes. Current planning invokes Rust normalization and archive
separately (`api-go/internal/workspaceops/diagnostics.go:230` →
`rust-core/src/diagnostics.rs:95,118,272`), so separate path reads must not become
separate evidence. Reject HTTP input paths outside the
workspace, symlink escapes, and no-follow violations; preserve existing auth and
workspace/principal checks. CLI file reads remain local operator authority.

Record `ingestion_repo_identity` (the complete relevant repository/source
identity observed while importing) separately from `source_revision`. Imported
reports have `source_revision=unknown` in this stage: sampling the current tree
at ingestion does not prove which revision produced an old report. Do not add a
producer-attestation feature here. Do not hash only a prefix or claim identity
for unread bytes. Capture complete relevant content identity before and after
import; if files change during ingestion, or a dirty same-size edit makes source
freshness uncertain, mark the result stale/unknown, never fresh. The ingestion
key binds captured evidence to its import context; it is not proof of diagnostic
production freshness. Imported evidence proves what bytes were imported, not
that a compiler or LSP actually ran.

Validation must distinguish: a successful run with zero diagnostics and known
coverage; missing baseline (`status=no_baseline`, explicitly unavailable); and
invalid/unsupported input, all-invalid rows, partial normalization, unavailable
collector, timeout, or failed collection. The latter cases are errors or explicit
degraded/stale results and must not overwrite a valid baseline as clean. Rust's
normalizer can produce an empty batch with warnings for unrecognized input, so
check supported payload shape and normalization completeness before publication.
Preserve raw provenance, row fingerprints and replay warnings. Local rows may
report `symbols_unavailable`; never fabricate PostgreSQL symbol IDs or semantic
baseline anchors. Diagnostic originals use this store's retention contract, not
expiring context-delivery handles as their sole copy.

## Bounded, recoverable publication

Proposed initial limits for review: 16 MiB raw input per import; at most 50,000
normalized rows; at most 16 MiB per complete serialized envelope (including
base64 expansion and metadata); at most 32 MiB decoded/working memory per import;
at most 100 baselines and 256 MiB persisted storage per workspace; retain
accepted baselines for at least 30 days. A raw input that fits its own cap may
still be rejected if its complete envelope exceeds the cap. Count/stream before
decode or allocation and charge shared admission for captured bytes, normalized
rows, and encoder buffers. Reject new work when an admission limit is full; do
not evict an unexpired baseline to make room. Expire/prune only beyond the
declared retention. Limit concurrent imports to two per workspace, bound lock
wait to two seconds, and return an explicit retryable busy/quota/size error
rather than queueing or buffering without limit. Tune only with measured fixtures.

Reuse the existing byte/child admission pattern through capture, decode,
normalization, persistence, and response; account for raw input plus normalized
rows, not just the response projection. Thread HTTP cancellation into a
request-scoped `RunDiagnosticsCtx` (the current POST handler calls
`RunDiagnostics` without `r.Context()`); bound CLI work as well.

Publish a versioned envelope containing workspace/repository scope, run metadata,
normalized rows, exact captured raw bytes or lossless encoding, hashes, source and
ingestion identities, coverage/completeness, retention, and warnings. Write to a
temporary file, validate bounds/checksums, fsync, then atomically publish; update
the latest pointer only after the envelope is durable. Add a bounded cross-process
lock around quota/prune/publish/latest-pointer updates: `lockStore` in
`api-go/internal/workspaceops/storelock.go:46` is process-local, while the
existing atomic writer (`api-go/internal/workspaceops/verification.go:392`)
alone does not serialize competing writers. Use no-follow and
workspace-root/path-safety checks for all reads, writes, pruning and lock paths.
On corruption, oversized records, lock timeout, interruption, or failed import,
return an explicit error and preserve the prior valid envelope and latest pointer.

## Acceptance gates

1. With no DSN and no PostgreSQL process, explicitly seed one diagnostic through
   the existing import producer. After restart, HTTP, MCP and the pinned Pi
   runtime return the same nonempty severity/path/range/message/provenance/run ID;
   all nine tools still execute. Also prove a successful zero-error imported
   baseline differs from a fresh workspace's `no_baseline` result.
2. With configured and explicit DSNs, existing PostgreSQL tests and error behavior
   remain unchanged; force a failed PostgreSQL connection and prove there is no
   local fallback. Confirm no-DSN operations do not attempt a DB connection.
3. Test malformed, unsupported, all-invalid, partial, oversized, corrupt,
   unknown-run, unavailable, timeout, and cancelled imports/reads. None may be
   represented as a clean empty baseline or erase the previous valid envelope.
4. Test unchanged and dirty same-size source edits, source mutation during import,
   rename/delete, unknown production revision, and post-restart freshness labels.
   Unknown/unvalidated provenance must never be labelled fresh.
5. Test workspace/principal isolation, arbitrary-host-path and symlink rejection,
   concurrent processes, lock timeout, quota saturation, expiry/pruning, and
   crash/interruption before publication. Readers see either the old complete
   envelope or the new complete envelope, never partial state.
6. Run the full backend suite, real no-Postgres HTTP/MCP/Pi E2E, and later the
   complete xMustard-owned process-tree resource gate with raw imports and reads
   included. State measured workload and RSS method; no byte-admission claim is
   itself an RSS ceiling. Keep the established 50–100 MB target unproven until
   that full gate passes.

The current Pi E2E uses a disposable PostgreSQL fixture specifically to seed a
diagnostic before testing all nine tools (`integrations/pi/test/e2e/pi-adapter.e2e.ts`);
retain that configured-Postgres control and add a database-free seeded fixture.
No implementation has been performed yet. After Fable reviews this plan and the
root dispatches execution, the plan authorizes only this local diagnostics slice;
it does not authorize Postgres/local migration, broader SQL-correctness claims,
automatic LSP/compiler execution, or merge authorization.
