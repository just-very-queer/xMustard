# Plan: database-free local diagnostics baselines

Status: approved by Fable's final recheck
(`docs/reviews/2026-09-24-fable-local-diagnostics-plan-final.md`, plan sha256
`b3e9e772…`); implemented uncommitted in an isolated worktree, with Fable's P2-G
numbers and P3 notes folded in below. Evidence and residual gaps:
`docs/reviews/2026-09-24-local-diagnostics-implementation.md`. Nothing here is merged;
preserve human final merge authority.

## Goal and seams

Make explicitly imported diagnostic evidence durable without a configured
PostgreSQL DSN, so existing HTTP, MCP, and Pi readers can return it after
restart. Keep nine MCP tools, schemas, and routes except for the stated no-DSN
read behavior below. No daemon, UI, automatic compiler/LSP launch, agent loop,
or general SQL-correctness work. `auto_scan` emits scanner signals, not compiler
evidence (`api-go/internal/workspaceops/workspace_scan.go`).

Add one small `DiagnosticBaselineStore` interface in
`api-go/internal/workspaceops/diagnostics.go`:
`Publish(ctx, envelope)`, `Latest(ctx, ws)`, `ByID(ctx, ws, id)`, and
`Rows(ctx, ws, id)`. Keep a PostgreSQL adapter wrapping
`persistDiagnosticsBaseline`, `readDiagnosticRun`, and `readDiagnosticRows`
unchanged, except that `persistDiagnosticsBaseline` now derives its 60 s context
from the import's context, so cancellation and the 120 s deadline reach PostgreSQL
writes too (Fable code review P3-5); add `api-go/internal/workspaceops/diagnostics_local_store.go`.
`selectDiagnosticsStore(settings, requestDSN)` is the only DSN-selection seam:
explicit or configured DSN selects PostgreSQL; any connection/schema/write
failure is an error, never local fallback. With neither DSN, choose local and
do not dial PostgreSQL. No migration, mirroring, or reconciliation.

**Intentional API change:** today `ReadDiagnosticsCtx` rejects a missing DSN
and GET maps that to 400 (`diagnostics.go:447`, API `main.go:2212`). No-DSN
`GET /diagnostics` becomes 200 with `diagnostics: []`, the existing warning,
optional `baseline` omitted (the field is `omitempty`; null is also acceptable),
and `storage: {backend:"local", status:"no_baseline"}`. The local
`DiagnosticsStatus.status` is `no_baseline` while retaining
`postgres_configured=false`; the status route is platform-mode only because it
is not in the `CORE_ONLY` allowlist (`main.go:288-297`). For local storage,
enumerate both status fields: `no_baseline` when absent, `available` for a
valid baseline whose ingestion identity is matched or unknown, and `stale` when
the observed HEAD has moved since import. Add `stale_reasons` (for example,
`dirty_worktree` and `head_moved`). A dirty tree with unchanged HEAD adds
`dirty_worktree` but keeps status `available`; if HEAD moved, `stale` takes
precedence. Identity mapping (one vocabulary): `matched` → `available`;
`partial` → `available` plus `ingestion_identity_partial`; `unavailable` →
`available` plus `ingestion_identity_unavailable`; `changed` (a row's file
changed while it was hashed) → `stale` plus `ingestion_identity_changed`.
Partial normalization adds `normalization_partial`. Local storage emits these
coded tokens in the existing `stale_reasons` field; PostgreSQL keeps its prose
sentences there. Zero row paths count as `matched`.
Local imports never claim `fresh` or `dirty_provisional`. On GET,
`storage.status` mirrors that local set, including `stale_reasons`. PostgreSQL
keeps its existing five status values and label semantics unchanged. Set
`freshness_basis:"ingestion_identity"` for local and `"head_match"` for
PostgreSQL.
`DiagnosticRun.PostgresSchema` is `""` for local records; add
`storage_backend` to run response and `DiagnosticsPlan`. An empty but
successfully imported baseline is non-null and distinct from no baseline. Keep
`PostgresConfigured=false` when using local storage. Keep configured/explicit-DSN
PostgreSQL contracts unchanged; a platform HTTP request's existing body `dsn`
override continues to select PostgreSQL (`DiagnosticsRequest.DSN`,
`diagnostics.go:35`), and is not a new authority option.

Remove the no-DSN blocker in `PlanDiagnostics` (`diagnostics.go:230`) so a local
run can proceed. Make activity kind and message backend-neutral (currently
`postgres.materialize.diagnostics` and “into Postgres schema”,
`diagnostics.go:335,352`); include the selected `storage_backend` in plan and
run results. Do not retain user-facing “Configure Postgres or pass --dsn” for
local mode.

Relevant wiring: API GET/live/run handlers (`api-go/cmd/xmustard-api/main.go:
2197-2259`); CLI run (`api-go/cmd/xmustard-ops/main.go:47-108`); MCP calls the
existing GET (`api-go/cmd/xmustard-mcp/main.go:151`); Pi uses the same route
(`integrations/pi/src/tools.ts:151`). `CORE_ONLY` diagnostics allowlist remains
GET-only (`main.go:288`, `api-go/cmd/xmustard-api/core_only_test.go:33`); do
not widen it. The HTTP `POST /diagnostics/run` remains platform-mode only.
`GET /diagnostics/live` stays separate and explicit; ordinary reads never
launch collection.

## Import, identity, and recoverability

Capture input once into immutable bytes and pass the same Go-owned temp file to
Rust normalization and archive commands (`api-go/internal/rustcore/diagnostics.go:101`).
Go retains the exact captured input bytes in the envelope and computes their
SHA-256. Compare it with Rust's `raw_payload_sha256` before publish; mismatch
fails the import. Rust's archive path parses and re-emits JSON, so it is not the
byte source of truth (`rust-core/src/diagnostics.rs:125,194`). Non-UTF-8 input
is a normal import error.
Add non-serialized server options to `RunDiagnosticsCtx(ctx, dataDir, ws, req,
opts)`: HTTP selects workspace-only authority; CLI selects
`local_operator` for an explicitly supplied external regular file. Request
JSON cannot select authority. HTTP resolves via `resolveWorkspacePath`
(`safepath.go:44`) and `openWorkspaceFileBeneath` (`safepath_unix.go:23`);
reject absolute, traversal, and symlink escapes. CLI explicit paths still use
no-follow regular-file opens. Keep auth/workspace checks; loopback open mode
collapses callers to one principal, so local storage promises workspace
directory isolation, not per-issuer isolation (`main.go:489`).

Normalize and validate the supported input shape before publishing. Top-level
array and `{diagnostics:[...]}` are supported (`rust-core/src/diagnostics.rs:472`).
Record additive `normalization:{items,normalized,skipped}` and explicit
coverage (`known`/`unknown` plus scope). A valid `[]` is normalization-complete
with zero diagnostics, not proof of whole-repo cleanliness or producer
execution. Unsupported shape and all-invalid nonempty input error; partial
normalization is explicitly partial and cannot read as clean. Preserve existing
baseline on any failed import. Keep captured raw provenance and row fingerprints.
Local rows may say `symbols_unavailable` and add “symbol linking requires
PostgreSQL”; never invent `linked_symbol` or `semantic_baseline`. Keep existing
PostgreSQL replay-warning text unchanged.

For each normalized row, validate its path before any `stat` or read. Confine
identity reads to the repository root; outside paths get
`identity_status:path_outside_workspace` without reading bytes. Bound identity
work to 2,000 unique paths and 64 MiB read; hash via streaming no-follow file
descriptors, or charge every buffered read to request admission. Excess is
`partial`, never a prefix hash represented as complete identity. Separate
identity of bytes observed at
ingestion from production revision: set `source_revision:"unknown"` (no
producer attestation), and add `ingestion_identity.status` (`matched`,
`changed`, `partial`, `unavailable`) and backend-specific
`freshness_basis` (`ingestion_identity` locally, `head_match` for PostgreSQL).
Sampling current tree state at import does not prove when a report was
produced. A valid local record may be `available`, but must not be labelled
`fresh` on that basis. Preserve PostgreSQL's legacy HEAD-match/clean-tree label
semantics unchanged; the additive fields distinguish import identity from
production freshness.

Store under `<dataDir>/workspaces/{ws}/diagnostics/`, never in the repository.
Validate workspace/run IDs before path construction; run IDs must match
`^diag_[0-9a-f]{12}$` (`diagnostics.go:494`). Use data-dir-relative
`createWorkspaceFileBeneath` for envelope and pointer writes. Publish a
versioned envelope with exact raw bytes/lossless encoding, normalized rows,
provenance, identities, coverage, warnings, and retention. The envelope
(`xmustard.diagnostics.local.v2`) carries its own SHA-256, so reads by run ID
verify it without the pointer. Temp-write, validate
checksums/bounds, fsync file, atomic rename, fsync directory, then update latest
pointer. A failed directory open or fsync fails the publish. Readers see old or
new complete envelopes only.

Implemented caps (Fable P2-G, derived from one 16 MiB per-import admission
ledger so every cap is reachable over HTTP and the CLI): raw input ≤1 MiB;
≤2,500 input diagnostics (so ≤2,500 rows); serialized envelope ≤4 MiB; 2,000
unique identity paths and 64 MiB streamed through the hasher (64 KiB buffer,
charged). Worst case under the ledger: capture 1 + normalize stdout ×3 (capture
plus rustcore's decode/response reservation, ≈5.7) + archive stdout ×3 (≈3.2)
+ envelope estimate (≤4) + identity buffer and 256 KiB chunk rounding (≤1) ≈
15 MiB. The ledger is `budget.Scope.Limited(16 MiB)` opened inside the caller's
scope: the HTTP request scope, or one scope the CLI opens for the whole command
and holds until output is written, so both entry points account identically.
Exceeding it is `budget.ErrAdmissionLimit`; any fixed-cap breach maps to a
non-retryable 413 (no `Retry-After`); a full store is 409; only lock, process
slot or shared-pool contention is 503 + `Retry-After`. One import per
workspace (cross-process flock held for the whole import), two per process;
one 2 s wait for each; 256 MiB/workspace and 100 baselines, whichever first.
Reads charge the envelope file plus two copies for decode and response to the
request scope. This limits accounted inputs/outputs only, not untracked
runtime allocations. The Go process's child
admission may itself wait up to 10 s (`budget.go:312`); a single 120 s total
import deadline bounds HTTP and CLI latency, including child-slot waits, 30 s
normalize/60 s archive limits, git probe, lock, and I/O. Lock contention and
admission overload wrap `budget.ErrOverloaded`, which maps to 503 with
`Retry-After` (`main.go:556,432`). Disk quota includes orphan/temp files and
recovery. Promise retention of the full baseline envelope and exact original
bytes for 24 h; expired entries may then be pruned. When full before expiry,
reject new retention and report earliest expiry/quota; relief is expiry and
pruning on the next operation. Never evict an unexpired original. No unbounded
JSON fallback.

Use a cross-process Unix `flock(LOCK_EX|LOCK_NB)` retried for at most 2 s in
the store directory (the same 2 s bound above). Unsupported platforms fail closed with a clear unsupported
error; do not guess stale-lock age or steal an `O_EXCL` lock. Lock order:
acquire → recover/prune expired → quota check → publish envelope → update latest
pointer → unlock. Recover/remove orphan/temp files under lock. Write latest
pointer through a temp file and atomic rename. Recovery and pruning fail closed:
if the latest pointer, a directory listing, an entry's stat, or a temp-file
cleanup fails, nothing is pruned and the import is refused. A corrupt pointer
therefore blocks imports until an operator removes it. Once the envelope and
pointer are published, the import has succeeded. A later activity-log failure
is a `warnings` entry on the result, which still carries the run ID. Readers
load pointer then
envelope; if a concurrent prune creates ENOENT, retry that pair once, then
return an explicit error, never `no_baseline`. Preserve prior valid envelope
and pointer on corruption, oversize, cancellation, timeout, quota rejection,
or failed import.

Add `PlanDiagnosticsCtx`, `RunDiagnosticsCtx` and `ReadDiagnosticsStatusCtx`;
keep the old entry points as `context.Background()` wrappers for other Go
callers. The CLI calls the `Ctx` variants with a 120 s deadline context (a
background wrapper cannot carry it). POST passes `r.Context()`. Use a
request-bound Rust child and a 5 s cancellable git status probe (currently plain
`exec.Command`, `context_packet.go:2065`); CLI has a 120 s overall timeout.
Cancellation kills children and publishes no partial data. Explicit `--dsn`
without configured DSN writes PostgreSQL; no-DSN reads still select local and
show `no_baseline`. Add `storage_backend` to run response so this is visible;
no automatic mirror.

## Required acceptance evidence

Each defect gets a focused regression test plus these integration gates:

1. Update `TestDiagnosticsStatusBlocksWithoutPostgres` and Pi’s no-Postgres
   branch (`integrations/pi/test/e2e/pi-adapter.e2e.ts:247`) for intentional
   200/no-baseline result. Prove no DB dial; `baseline` omission is valid.
   Test `DiagnosticsStatus.status` in platform mode only; CORE_ONLY continues
   to expose the GET diagnostics route, not `/diagnostics/status`.
2. While `CORE_ONLY` API is running, execute actual `xmustard-ops diagnostics
   run --input-path`; next GET sees complete row without restart, and after
   restart the same run ID, severity, path/range/message, raw provenance and
   fingerprints. CLI ingestion may use explicit external regular-file input.
   Platform-mode HTTP producer remains workspace-confined. Do not widen
   `CORE_ONLY` diagnostics beyond GET.
3. Test valid `[]` as complete zero rows versus absent baseline; `{}` and
   all-invalid input error; good+bad rows report partial/skipped; unsupported,
   unavailable, failed, timeout, and cancellation never overwrite good data.
   Test a canary outside-root row is marked outside without `stat`/read. Include
   raw over-limit-before-Rust, max rows/identity work, 503 concurrency, corrupt
   envelope, unknown run ID, quota-full rejection/no eviction, expiry, crash
   before envelope rename and between rename/pointer update, multi-process
   CLI/API lock and latest-pointer visibility.
4. Test unchanged, dirty same-size change, mutation during import, delete,
   rename, and unknown source revision using additive identity fields; verify
   `source_revision` stays unknown, incomplete identity is partial/unknown,
   `DiagnosticsStatus.status` and GET `storage.status` are `no_baseline`,
   `available`, or `stale` for local storage. A HEAD move yields `stale`; a
   dirty tree with unchanged HEAD adds `stale_reasons:["dirty_worktree"]` but
   remains `available`; a moved HEAD adds `head_moved` and is `stale` even if
   the tree is also dirty. No local import is
   `fresh` or `dirty_provisional`. Assert local
   `freshness_basis=ingestion_identity`, PostgreSQL
   `freshness_basis=head_match`, and unchanged PostgreSQL status labels. Verify
   HTTP rejects symlink, traversal, and absolute input paths; CLI is restricted
   only by its explicit local-operator regular-file authority.
5. Prove explicit/configured DSNs retain PostgreSQL behavior and forced PG
   failure does not fallback. Explicit `--dsn` with no configured DSN reports
   PostgreSQL storage, while following no-DSN GET reports local no-baseline.
   Add response/storage and local warning tests; local emits no symbol or
   semantic-baseline IDs. Assert loopback is workspace-scoped, not issuer-private.
6. Run full backend suite and real no-Postgres HTTP/MCP/Pi flow with all nine
   tools plus the existing native-Postgres control. Change the Pi fixture at
   `integrations/pi/test/e2e/pi-adapter.e2e.ts:79-88`: place
   `diagnostics-lsp.json` under `repo/.xmustard-e2e/` and POST its workspace-
   relative path so HTTP confinement permits the PostgreSQL control. The
   scanner has no dot-directory skip, so the file is kept out of seeded
   scanning by writing it after the `auto_scan` load. Include CLI-produced seeded diagnostic, fresh
   no-baseline workspace, zero-error baseline, and post-restart retrieval.
   Later rerun the full xMustard-owned process-tree resource gate including a
   CLI import concurrent with API reads (they have separate process pools);
   report workload and sampled RSS method. Keep the established 50–100 MB
   target unproven until it passes. Byte limits alone are not an RSS ceiling.

## Explicitly deferred

No UI, daemon, compiler/LSP producer, producer attestation, MCP tool expansion,
PostgreSQL/local migration or mirroring, broad SQL correctness, per-issuer
identity in open loopback mode, or automatic merge authority. Existing Pi
PostgreSQL fixture remains a control.
