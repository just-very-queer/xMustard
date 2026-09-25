# Plan review: database-free local diagnostics — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: `docs/plans/2026-09-24-local-diagnostics.md`
(sha256 `4cff9315be533e0d71e5e1ee026f2105db8e46a4f5f8a9a2fc58803bbbb71f46`,
150 lines, gitignored by `.gitignore:79`). Checked against `AGENTS.md`, the
diagnostics HTTP handlers, CLI, store helpers, Rust normalizer, MCP shim, and the
Pi adapter plus its E2E fixture, at working tree `cd13e2b` with uncommitted
changes. No builds, benchmarks, or source edits were run; this file is the only
write. The plan is a draft; this review does not authorize implementation.

## Verdict: CHANGES_REQUIRED

The storage-seam shape, the PostgreSQL-first selection rule, the refusal to fall
back silently, and the atomic-publish and admission requirements are all correct
and match what the source lacks today. Seven findings block execution as
written: the no-DSN read is a contract change the plan mislabels as additive, the
HTTP producer is unreachable under `CORE_ONLY`, HTTP confinement has no seam to
attach to, row paths are an unconfined host-file oracle, the clean-empty check
has no mechanism, the byte limits do not fit the shared pool, and cancellation
requires signatures the plan does not name. All are amendable without widening
scope. Findings are numbered for the amendment; acceptance criteria follow.

## P1 findings

**1. The no-DSN read is a contract change, not an additive field.** Today
`ReadDiagnosticsCtx` returns `ErrInvalidDiagnosticsRequest` when no DSN is
configured (`api-go/internal/workspaceops/diagnostics.go:447`), which the GET
handler maps to 400 (`api-go/cmd/xmustard-api/main.go:2212`). `ReadDiagnosticsStatus`
returns `status=blocked` (`diagnostics.go:386`, pinned by
`TestDiagnosticsStatusBlocksWithoutPostgres`). The Pi E2E asserts the error text
`Postgres DSN is required` in its no-PostgreSQL branch
(`integrations/pi/test/e2e/pi-adapter.e2e.ts:247`). Gate 1 requires a 200
`no_baseline` result instead. Amend: state this as the one intentional
behavioral change. GET `/diagnostics` with no DSN returns 200 with
`baseline: null`, `diagnostics: []`, the existing warning string, and a new
optional `storage: {backend: "local", status: "no_baseline"}`. Status returns
`no_baseline` (an existing value) with `postgres_configured=false` retained. List
the unit test and the E2E branch as files the slice must update. Every other
field stays; `DiagnosticRun.PostgresSchema` is a required string, so the local
adapter sets it to `""` and adds `storage_backend`.

**2. The HTTP producer does not exist under `CORE_ONLY`.** `coreWorkspaceSubpaths`
admits only the exact `diagnostics` subpath (`main.go:288`), and
`core_only_test.go:33` pins `/diagnostics/run` as denied. Gate 1 says "seed
through the existing import producer" and lists HTTP first. In the lean
deployment the only producer is `xmustard-ops diagnostics run`
(`api-go/cmd/xmustard-ops/main.go:102`), a second process writing the same data
directory the API reads. That is the cross-process case the plan's lock exists
for, and it also means the API must read the latest pointer from disk on every
request; a process-local cache would pass the restart test and fail the live
one. Amend gate 1: seed via CLI while the API is running `CORE_ONLY`; a GET
without restart returns the row; restart; GET again. Keep the HTTP `run` route
for platform mode only and say so.

**3. HTTP confinement versus CLI authority has no seam.** Both the HTTP handler
(`main.go:2259`) and the CLI call the same `RunDiagnostics`, which resolves the
path through `resolveDiagnosticsInputPath` (`diagnostics.go:822`). That helper
accepts any absolute host path and uses `os.Stat`, which follows symlinks.
`DiagnosticsRequest` is decoded straight from the JSON body, so any policy
carried on the request struct would be caller-controlled. Amend: add a
non-serialized options parameter (`RunDiagnosticsCtx(ctx, dataDir, ws, req,
DiagnosticsOptions{InputAuthority: "workspace"|"local_operator"})`), defaulted
to `workspace` and set to `local_operator` only by the CLI. Under `workspace`,
resolve through `resolveWorkspacePath` (`safepath.go:44`) and open with
`openWorkspaceFileBeneath` (`safepath_unix.go:23`) so `..`, absolute paths, and
symlink escapes are rejected the way file explain already does. Check the E2E:
it posts `join(T, "diagnostics-lsp.json")` (`pi-adapter.e2e.ts:79`) by absolute
path; if `T` is not inside the workspace root, HTTP seeding breaks under
confinement and must move inside the root or switch to the CLI.

**4. Row paths are an unconfined host-file oracle once ingestion identity hashes them.**
The Rust normalizer keeps absolute paths that do not strip against the root and
passes `..` segments through untouched (`rust-core/src/diagnostics.rs:482-511`).
The PostgreSQL path already reads `row.Path` joined to the root without
confinement (`semanticFileMetadata`, `semantic_materialization.go:674`). The plan's
"complete relevant content identity" would do the same over HTTP for every row
path in an imported file. Amend: each row path passes `resolveWorkspacePath`
before any stat or read; rows that fail are recorded with
`identity_status=path_outside_workspace` and are never read. Use
`readWorkspaceRegularFile` (8 MiB cap, `safepath.go`) or the no-follow opener.
Bound identity work independently of the row cap: at most 2,000 distinct paths
and 64 MiB read per import; beyond that mark `coverage=partial`. Never hash a
prefix and call it identity.

**5. Clean-empty versus unrecognized input has no mechanism.** `diagnostic_items`
accepts only a top-level array or an object with a `diagnostics` array
(`diagnostics.rs:472`); anything else yields zero rows plus a free-text warning
(`diagnostics.rs:344`). Skipped items are also only warnings
(`diagnostics.rs:292,302`). `PlanDiagnostics` treats warnings as non-blocking, so
today `{}` produces `can_run=true` and would publish as a zero-row baseline. The
plan names the problem but not the check. Amend, without a Rust contract change:
Go already holds the captured bytes, so it decodes the top level once and
computes `items`; `normalized` is `batch.DiagnosticCount`; `skipped = items -
normalized`. Rules: unsupported shape is a 400 error; `items>0 && normalized==0`
is `all_invalid` and an error; `skipped>0` publishes with
`completeness=partial` and the counts recorded; `items==0` on a supported shape
publishes as `completeness=complete, diagnostic_count=0`. Record
`normalization: {items, normalized, skipped}` in the envelope and as an additive
response field.

**6. The proposed byte limits do not fit the shared transient pool.**
`budget.TransientBytes` defaults to 64 MiB process-wide (`budget.go:239`). Every
Rust capture is reserved against the request scope and held until the handler
returns (`rustcore/root.go:40-58`, `main.go:383`). An import runs the Rust child
twice, and the archive child's stdout embeds the raw payload, so one 16 MiB
import already reserves roughly 16 MiB raw plus more than 16 MiB archive stdout
plus normalize stdout plus the encoder buffer. Two concurrent imports at the
plan's caps exceed the pool and fail with `ErrOverloaded`, not the plan's
retryable busy error, and a third tool call in flight starves. Amend: state the
initial caps as 4 MiB raw, 8 MiB envelope, 16 MiB working, one concurrent
import per workspace and two per process, with 16 MiB as a measured tuning
target only. Pass the captured bytes to Rust through one Go-owned temp file used
by both the normalize and archive commands (the pattern exists at
`rustcore/diagnostics.go:101`), which also closes the double-read the plan
already flags. State that quota (256 MiB) and count (100) limits apply
"whichever first".

**7. Cancellation needs named signatures.** `RunDiagnostics` takes no context;
`PlanDiagnostics` uses `context.Background()` with a 30 s timeout
(`diagnostics.go:246`) and `persistDiagnosticsBaseline` uses a 60 s background
context (`diagnostics.go:481`). Because the request scope lives on `r.Context()`,
these Rust captures are charged to a throwaway scope, not the request, so
finding 6's accounting is currently invisible. `readWorktreeStatus` runs `git
status` with plain `exec.Command` (`context_packet.go:2065`), unbounded on a
large tree. Amend: add `PlanDiagnosticsCtx` and `RunDiagnosticsCtx`, keep the
existing names as `Background` wrappers for the CLI, switch the POST handler to
pass `r.Context()`, and run the git status probe under the same context with a
5 s bound. CLI runs get a 120 s overall context.

## P2 findings

**8. Freshness label versus provenance are conflated in gate 4.** The status
label today means HEAD match plus clean tree (`diagnostics.go:399-412`) for
PostgreSQL baselines, which the plan says must not change. Gate 4 says imported
evidence "must never be labelled fresh". Both cannot hold for identical evidence
in two backends. Amend: keep the label semantics for both backends and assert
gate 4 on additive fields instead: `source_revision: "unknown"`,
`ingestion_identity: {status: matched|changed|partial|unavailable, ...}`, and
`freshness_basis: "head_match"`. If the owner wants the label itself to change,
that is a PostgreSQL contract change too and must be listed as one.

**9. Explicit-DSN runs are invisible to no-DSN reads.** Reads select by
configured DSN only (`ReadDiagnosticsCtx` passes `nil` for the request DSN). A run
with `--dsn` and no configured DSN writes PostgreSQL; the next GET selects local
and reports `no_baseline`. Not a defect under "no mirror", but the plan should
say it, and the run response should carry `storage_backend` so the operator
sees where it went.

**10. Cross-process lock has no precedent in `api-go`.** The only `Sync()` calls
are in the evidence store (`internal/evidence/store.go:485,759`) and no `Flock`
exists. Specify `unix.Flock` with `LOCK_EX|LOCK_NB` retried up to 2 s on a lock
file inside the store directory, fsync the temp file and the directory after
rename, and provide a `safepath_other.go`-style fallback using an `O_EXCL` lock
file with a stale-age rule. Name the lock order: lock, prune, quota check,
publish envelope, update latest pointer, unlock.

**11. Two roots, one sentence.** The plan applies "workspace-root/path-safety
checks" to reads, writes, pruning, and lock paths. Input reads are confined to
the repository root; store I/O belongs under
`dataDir/workspaces/{ws}/diagnostics/` and must never touch the repository.
Validate `diagnostic_run_id` against `^diag_[0-9a-f]{12}$` (format from
`diagnostics.go:494`) before building any path, so a query parameter cannot
traverse into another workspace's directory. Use `createWorkspaceFileBeneath`
against the data directory for envelope and pointer writes.

**12. Local rows and the replay warning text.** Local rows will carry
`symbols_unavailable`, and `diagnosticReplayWarnings` (`diagnostics.go:943`) then
emits "stored before materialized symbols were available", which is wrong for a
backend that never has symbols. Add one additive warning for local baselines
("symbol linking requires PostgreSQL") and leave the existing text for
PostgreSQL rows. Never emit a `linked_symbol` or `semantic_baseline` from the
local adapter.

**13. Isolation statement.** In the default loopback open mode, all callers
collapse to one identity; workspace scoping applies only when tokens exist
(`main.go:489`). The local store gives workspace-directory isolation only. Say so
in the plan, as the lean-context review already required for evidence.

**14. Minimal interface.** Keep `DiagnosticBaselineStore` to four methods:
`Publish(ctx, envelope)`, `Latest(ctx, ws)`, `ByID(ctx, ws, id)`, `Rows(ctx, ws,
id)`. The PostgreSQL adapter wraps `persistDiagnosticsBaseline`,
`readDiagnosticRun`, and `readDiagnosticRows` unchanged. One
`selectDiagnosticsStore(settings, requestDSN)` is the only place a DSN is
inspected. Put the local adapter in `workspaceops/diagnostics_local_store.go`
so it reuses the unexported safepath helpers rather than a new package.

## Sound as written

- `auto_scan` is correctly excluded as a producer; `workspace_scan.go` emits
  scanner signals, not compiler evidence.
- PostgreSQL-first selection with hard errors and no silent fallback matches the
  existing `connectSemanticPostgres` seam and keeps `TestRunDiagnosticsNormalizesWithRustAndPersistsRows` valid.
- The nine tools and the `CORE_ONLY` GET allowlist are untouched; the MCP shim and
  Pi adapter both issue GET `/diagnostics` (`xmustard-mcp/main.go:151`,
  `integrations/pi/src/tools.ts:151`), and `main_test.go:15` pins the count.
- No collector launch from reads; `/diagnostics/live` stays a separate route.
- The atomic writer at `verification.go:392` does not fsync and `lockStore` is
  process-local, exactly as the plan states.
- Retention and quota numbers mirror the evidence store defaults
  (`store.go:38-43`), which is the right precedent.

## Acceptance criteria for the amended plan

Each criterion is a test the slice must ship; names are suggestions.

1. `TestLocalDiagnosticsNoDSNReadsNoBaseline`: fresh workspace, no DSN, GET
   returns 200 with `baseline=null`, `storage.status=no_baseline`; status returns
   `no_baseline` with `postgres_configured=false`; no PostgreSQL dial is attempted
   (assert via a `connectSemanticPostgres` stub that fails the test if called).
2. `TestLocalDiagnosticsCLISeedVisibleToRunningAPI`: CLI `run` writes while an
   API handler goroutine reads the same data dir; the next GET returns the row
   with matching severity, path, range, message, provenance, and run ID; a new
   process reading the same dir returns the identical envelope.
3. `TestLocalDiagnosticsZeroRowsDiffersFromNoBaseline`: import `[]` publishes
   `diagnostic_count=0, completeness=complete`; GET returns a non-null baseline;
   import `{}` returns 400 and leaves the prior baseline in place.
4. `TestLocalDiagnosticsRejectsAllInvalidAndMarksPartial`: two items with no
   path and no message yield `all_invalid` error; one good plus one bad yields
   `completeness=partial, normalization.skipped=1`.
5. `TestLocalDiagnosticsHTTPInputConfinement`: absolute host path, `../` path,
   and an in-repo symlink to a host file are each rejected over HTTP with 400;
   the same paths succeed through the CLI option.
6. `TestLocalDiagnosticsRowPathConfinement`: a row with `path: ../../etc/hosts`
   is stored with `identity_status=path_outside_workspace` and no bytes of that
   file are read (assert with a canary path).
7. `TestLocalDiagnosticsBoundsAndBusy`: raw input one byte over the cap returns
   413-class error before Rust is invoked; a second concurrent import returns the
   retryable busy error; quota saturation refuses new work and does not evict an
   unexpired baseline.
8. `TestLocalDiagnosticsCrashBeforePublishKeepsOldPointer`: inject a failure
   after the temp write and before rename, then after rename and before the
   pointer update; readers see the old complete envelope in both cases; the lock
   file is released.
9. `TestLocalDiagnosticsIdentityFields`: unchanged tree gives
   `ingestion_identity.status=matched`; a same-size edit gives `changed`; a
   deleted path gives `partial`; `source_revision` is always `unknown`; the
   status label follows the existing HEAD rule.
10. `TestRunDiagnosticsCtxCancels`: a cancelled request context aborts the Rust
    child and the git probe within the bound and leaves no partial files.
11. PostgreSQL control: existing tests pass unchanged; a forced connection
    failure with a configured DSN returns an error and writes nothing local.
12. Pi E2E: add the no-DSN seeded run alongside the native-PostgreSQL run; the
    no-DSN branch asserts a delivered row, not the old error text; all nine tools
    execute in both.
13. Gate 6 stays as written: the process-tree RSS claim remains unproven until
    the full gate runs with imports and reads included.

## Out of scope, confirmed

No new MCP tool, no daemon, no UI, no automatic LSP or compiler launch, no
producer attestation, no PostgreSQL-to-local migration, no general SQL
correctness work, and no merge authorization. `docs/reviews/*` is gitignored
except for allowlisted files; adding this file to the allowlist is the owner's
call and was not done here.
