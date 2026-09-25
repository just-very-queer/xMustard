# Implementation: database-free local diagnostics — 2026-09-24

Implementer: Claude Opus 5.5, in the isolated worktree `/private/tmp/xmustard-opus-l9b1F4`
(base `cd13e2b`, already-dirty tree). Nothing committed, pushed, or merged; human final
merge authority is unchanged. Inputs: plan
`docs/plans/2026-09-24-local-diagnostics.md` (dispatched at sha256 `b3e9e772…d222`) and
Fable's APPROVED final recheck. The plan's caps, status mapping, and CLI-deadline text
were updated during implementation. The new plan sha256 is recorded at the end of this file.
Fable's independent code review (`docs/reviews/2026-09-24-fable-local-diagnostics-code-review.md`,
APPROVED with three P2 findings) was then repaired in the same worktree; see
[Post-review repair](#post-review-repair). Where the two differ, the repair section is current.

## What shipped

| Area | Where | Summary |
|---|---|---|
| Store seam | `api-go/internal/workspaceops/diagnostics.go` | `DiagnosticBaselineStore` (`Publish`, `Latest`, `ByID`, `Rows`, plus `Backend()` for reporting). `selectDiagnosticsStore` is the only DSN seam: an explicit or configured DSN selects PostgreSQL (the adapter wraps `persistDiagnosticsBaseline`/`readDiagnosticRun`/`readDiagnosticRows` unchanged); with no DSN it selects local storage and never dials. There is no fallback. |
| Entry points | same | `PlanDiagnosticsCtx`, `RunDiagnosticsCtx(ctx, dataDir, ws, req, opts)`, `ReadDiagnosticsStatusCtx`, `ReadDiagnosticsCtx`. Old names are `context.Background()` wrappers. There is one 120 s import deadline, 30 s normalize, 60 s archive, and a 5 s cancellable git probe (`readWorktreeStatusCtx`). The no-DSN blocker and the "Configure Postgres" action are removed. The activity kind is `diagnostics.materialize` and carries `storage_backend`. |
| Capture | `diagnostics_input.go` | Go reads the original bytes once, under admission, and computes their SHA-256. The bytes go to a private `0400` temp file used by both Rust commands. Rust's `raw_payload_sha256`/bytes must match, or the import fails. Shape is validated by streaming: an array or one `diagnostics` array. `{}`, a non-array, duplicate keys, scalars, trailing data, and non-UTF-8 input are 400. All-invalid nonempty input is 400. Partial input is labelled. `[]` is a complete zero-row baseline. |
| Authority | same, `diagnostics_local_store_unix.go` | `DiagnosticsRunOptions.InputAuthority` is a server option; request JSON cannot set it. HTTP accepts only a workspace-relative, symlink-free path (`openWorkspaceFileBeneath`). The CLI's `local_operator` authority also accepts an explicit absolute regular file, opened with `O_NOFOLLOW`. |
| Row identity | `diagnostics_input.go` | Paths are gated lexically before any filesystem call: absolute, `..`, and `file:` paths become `path_outside_workspace`. In-root paths are hashed through a no-follow fd, streaming, with fstat before and after. Hashing is capped at 2,000 unique paths and 64 MiB. Excess work is `budget_exceeded`, never a prefix hash. `source_revision:"unknown"`. |
| Local store | `diagnostics_local_store.go` | `<data>/workspaces/{ws}/diagnostics/{.lock,latest.json,runs/diag_*.json}`. A cross-process `flock` is held for the whole import. Under the lock: recover temps → prune expired non-latest → quota → temp-write/fsync/rename/dir-fsync the envelope → the same for the pointer. The envelope keeps the exact original bytes (base64) with checksums. The envelope carries its own SHA-256 (schema `v2`), so reads by run ID are verified too. Readers are lock-free: pointer then envelope, SHA-checked, retried once on ENOENT. After that, an explicit corrupt error is returned, never `no_baseline`. Non-Unix fails closed (`diagnostics_local_store_other.go`). |
| Admission | `internal/budget/budget.go`, `internal/rustcore/root.go` | `Scope.Limited(n)` is a per-operation child ledger. It reserves in the parent, so bytes are held until the HTTP response or CLI output is written. Overflow is `ErrAdmissionLimit`, which is deterministic, not `ErrOverloaded`. `CaptureWriter.RefusalErr` and `runCoreCtx` keep the two apart. |
| HTTP / CLI | `cmd/xmustard-api/main.go`, `cmd/xmustard-ops/main.go` | `respondDiagnosticsError` maps: size cap or admission limit → 413 (no `Retry-After`); quota → 409; invalid → 400; busy → 503 + `Retry-After`; unsupported platform → 501. The CLI opens one scope and a 120 s deadline for the whole command and calls the `Ctx` variants. `CORE_ONLY` is unchanged: diagnostics stays GET-only and `/diagnostics/status` stays platform-only. MCP code and the nine schemas are untouched. |
| Safepath fix | `safepath_unix.go` | This bug predates the feature and is fixed here. `openWorkspaceFileBeneath` detected "nothing opened" by comparing fd numbers. After the root fd closed, a later `openat` could reuse its number, so every path with two or more components returned `errEmptyPath`. The fix tracks a bool. This affects every caller of the function, for the better. Regression test: `TestOpenWorkspaceFileBeneathNestedPathAfterFdReuse`. |

### Contract changes

- A no-DSN `GET /diagnostics` now returns 200 with `diagnostics: []`, `baseline` omitted, and `storage:{backend:"local",status:"no_baseline",freshness_basis:"ingestion_identity"}`. It used to be 400.
- Local `DiagnosticsStatus` has three statuses: `no_baseline`, `available`, and `stale`. `stale_reasons` carries coded tokens: `head_moved`, `dirty_worktree`, `ingestion_identity_{changed,partial,unavailable}`, and `normalization_partial`.
- Identity status maps to local status as follows:
  - `matched` (including zero paths) → `available`
  - `partial` or `unavailable` → `available` plus a reason token
  - `changed` → `stale`
- Local storage never reports `fresh` or `dirty_provisional`.
- PostgreSQL keeps its five labels and its prose reasons. Its responses gain `storage_backend:"postgres"` and `freshness_basis:"head_match"`.
- New fields on runs, plans, and results: `storage_backend`, `normalization`, `coverage`, `ingestion_identity`, `retention`, `source_revision`, and row `identity_status`. All are additive and omitted when empty.
- Local rows are always `symbols_unavailable` and carry a "Symbol linking requires PostgreSQL" warning. `linked_symbol` and `semantic_baseline` are never set.
- Input validation (shape, all-invalid, caps, HTTP confinement) applies to both backends. A PostgreSQL import of `{}`, or of an absolute path over HTTP, now fails where it used to be accepted.

### Caps (Fable P2-G)

Raw input ≤ 1 MiB; ≤ 2,500 input diagnostics; envelope ≤ 4 MiB; 16 MiB per-import ledger (the worst case is ≈ 15 MiB; the arithmetic is in the plan and in `diagnostics_input.go`).

Concurrency: one import per workspace and two per process, with a 2 s wait for each. Storage: 256 MiB and 100 baselines per workspace. Retention: 24 h, measured from the envelope file's mtime.

The 2,500-row maximum is reachable over HTTP and the CLI: `TestLocalDiagnosticsMaxRowsFitTheAdmissionLedger`, plus the RSS gate step below.

## Evidence (all run in this worktree)

Pre-repair runs, before Fable's code review. The post-repair section reruns every one of these gates.

| Gate | Command | Result |
|---|---|---|
| Go | `cd api-go && go build ./... && go vet ./... && go test ./... -count=1` | all packages ok |
| Rust | `cd rust-core && cargo test && cargo clippy` | 148 tests pass; clippy exits 0 with warnings that were already there. No Rust source was changed. |
| Pi unit + typecheck | inside `pi-adapter.sh` | 17/17, tsc clean |
| Gates 1, 3–5 (unit/HTTP) | `diagnostics_local_store_test.go`, `cmd/xmustard-api/diagnostics_local_test.go`, `budget_test.go`, `safepath_test.go` | pass |
| Gates 2 and 6: real binaries, no DB | `scripts/e2e/local-diagnostics.sh` (new) | 16/16: fresh `no_baseline`; `CORE_ONLY` hides status/run/live; ten 2,500-row CLI imports plus one seeded CLI import while the API runs, with 464 concurrent GETs all 200; next GET sees the row without a restart; after a restart the run ID, severity, path/range/message, fingerprint and raw SHA are unchanged; a zero-error baseline is distinct from none; two concurrent CLI imports both publish; MCP lists exactly nine tools; the MCP `diagnostics` tool returns the row |
| Gate 6: Pi + native-PostgreSQL control | `scripts/e2e/pi-adapter.sh` | 16/16 e2e. All nine tools succeed; diagnostics come from PostgreSQL (fixture posted as `.xmustard-e2e/diagnostics-lsp.json`); the new database-free test on a second workspace passes; xMustard-owned sampled peak 67.4 MiB |
| Gate 6: Pi without PostgreSQL | same e2e with `XM_PG_BIN_DIR=` and the same built binaries | 16/16. All nine tools succeed; diagnostics come from local storage seeded by the real CLI; fresh workspace reads `no_baseline`; zero-error and after-restart checks pass; xMustard-owned sampled peak 69.2 MiB |
| MCP evidence regression | `scripts/e2e/mcp-evidence.sh` | 23/23. This run came before the pointer-read leak fix, a 3-line change on the local read path that this suite does not exercise. |
| Resource gate | `scripts/bench/rss.sh` (extended) | 20/20. Sampled xMustard-owned tree peak 86.5 MB (82.5 MiB) over 978 samples, reached during the new CLI-import step. The pool finished at 0 bytes in use. |

What the unit and HTTP tests cover, by gate:

1. No-DSN status and GET return `no_baseline` with `baseline` omitted, and a stub fails the test on any PostgreSQL dial. `CORE_ONLY` returns 404 for `/diagnostics/status`, `/run`, and `/live`.
3. Input handling: `[]` versus absent; `{}` and six other bad shapes; all-invalid; partial. An outside-root canary becomes `path_outside_workspace` rather than `missing`, which proves it was never stat'ed.
   - Caps: raw and row caps are refused before Rust (a broken `XMUSTARD_CORE_BIN` proves Rust never ran); the 2,500-row maximum succeeds; identity path and byte bounds hold.
   - Busy: a held lock and a full process are each 503 after the ~2 s wait. A cross-process `flock` is taken from a helper process and released by the kernel when that process is killed.
   - Stored data: a corrupt envelope and a pointer to a missing envelope are explicit errors. Unknown and malformed run IDs are handled.
   - Quota and crashes: a full quota is refused without evicting anything, expired baselines are pruned except the latest, and crashes before either rename are recovered.
   - Failure paths: a failed producer, a timeout (the fake core child is killed), and cancellation never overwrite the good baseline.
4. Status labels: unchanged, dirty same-size edit, HEAD move, HEAD move plus delete/rename, and a mutation during hashing (→ `changed` → `stale`). GET `storage` mirrors status. HTTP rejects absolute, traversal, and symlink inputs, and a body field cannot grant CLI authority. The CLI accepts an external file but refuses a final symlink.
5. Explicit and configured DSNs select PostgreSQL. A forced PostgreSQL failure surfaces as an error on import, read, and status, and no local files are created. The existing PostgreSQL test asserts `storage_backend`/`head_match` and the new activity kind.
   - After an explicit-DSN import, the following no-DSN GET reports local `no_baseline`, because the selection seam reads the configured DSN. This is exercised in `TestDiagnosticsPostgresSelectionHasNoLocalFallback`.
   - Workspace-scoped storage is shown rather than asserted by a dedicated test: a principal-less CLI import is served to every API caller of that workspace.

Resource runs, by workload:

- `rss.sh`: the original fixed workload, then five 2,500-row CLI imports, each concurrent with two threads looping GET. There were 2,927 reads, all 200.
  - The sampler polls `ps -axo pid,ppid,rss` every 100 ms and sums one snapshot over the required roots (the API and both MCP shims) plus the CLI processes, which count while alive.
  - Adding the step makes the report non-comparable with earlier runs at the tail. Earlier steps are unchanged.
- `local-diagnostics.sh`: ten 2,500-row imports and one seeded import against four reader threads. Sampled peaks, `ps` every ~20 ms: 88.8 MiB after the fix (API tree 61.9 MiB, CLI tree 33.6 MiB, 30 samples) and 95.4 MiB before it. Few samples were taken, so short spikes could be missed.
- Pi e2e runs: the xMustard-owned sampled peak was 67.4 MiB with PostgreSQL and 69.2 MiB without it.
- All of these are sampled lower bounds on a fixed workload, not RSS ceilings. The 50–100 MB target is met by this measured workload only.

## Bugs found by the gates

- **Transient-byte leak on every local GET.** `readPointer` reserved into a scope it never closed, about 4 KiB per read. The extended RSS gate caught it: the pool did not return to 0. It is fixed and covered by `TestLocalDiagnosticsReadsReleaseTransientBytes`, which fails without the fix and passes with it.
- **Empty baseline serialized `diagnostics: null`.** Caught by the real-binary e2e; fixed.
- **Safepath fd reuse.** Described above.

## Post-review repair

This repair addresses the findings in Fable's code review, whose reviewed hashes are listed in that file. The Fable review itself was not edited. Nothing was committed, pushed, or merged.

| Finding | Fix | Regression test (all in `diagnostics_local_store_test.go`) |
|---|---|---|
| P2-1: a committed publish was reported as a failure | `RunDiagnosticsCtx` keeps the result once `Publish` returns. An activity-append error becomes a `warnings` entry naming the run ID. `DiagnosticsRunResult.warnings` is a new additive, `omitempty` field. | `TestLocalDiagnosticsActivityFailureKeepsCommittedRunID` makes `activity.jsonl` a directory. The import succeeds, carries one warning naming the run ID, and that run is the latest. |
| P2-2: directory fsync errors were ignored | `writeAtomic` returns the directory open or fsync error after either rename, through the `diagnosticsSyncDir` seam. | `TestLocalDiagnosticsDirectoryFsyncFailureIsReturned`. When the `runs/` fsync fails, the pointer does not move. A failed pointer-directory fsync is also returned. |
| P2-3: the pruner treated failures as "no latest" | `recoverAndCheckQuota` fails closed on a pointer read error, a `ReadDir` error (temp sweep or `runs/`), a non-ENOENT `Entry.Info` error, and a non-ENOENT temp-file removal error. `readPointer` returns a pool refusal as `ErrOverloaded` (503), not as corruption. | `TestLocalDiagnosticsPruneFailsClosed` covers four corrupt pointers (malformed JSON, wrong schema, bad run ID, short SHA) on both the read and import paths, plus a store-listing failure, a `runs/` listing failure, an entry stat failure, and a temp cleanup failure (`chmod 0555`). In each case the expired-but-current envelope and the pointer survive. |
| P3-4: changed-file bytes were not charged | `observeDiagnosticPathsWithin` charges every streamed byte, changed files included. The read limit is `min(size+1, remaining)`, so the bound is on bytes read. | `TestDiagnosticsIdentityChargesChangedFileBytes`: a file that changes mid-read consumes the budget, the next file is `budget_exceeded`, and an exact fit is still `observed`. |
| P3-5: PostgreSQL publish ignored the request context | `persistDiagnosticsBaseline(ctx, …)` derives its 60 s timeout from the import context. The plan's PostgreSQL paragraph says so. | `TestPostgresDiagnosticsPublishUsesTheImportContext`: the dial receives the import's context value, a deadline of at most 60 s, and its cancellation. |
| P3-6: historical reads skipped the envelope checksum | The envelope self-checksum (`envelope_sha256`, the second field) is computed with those 64 bytes set to `'0'`, then written in place. Every envelope read recomputes it without copying. The schema is `xmustard.diagnostics.local.v2`, so unmerged v1 envelopes read as corrupt. | `TestLocalDiagnosticsHistoricalReadVerifiesEnvelopeChecksum` flips one byte of a row message in a historical, still-parseable envelope. The result is `ErrDiagnosticsStoreCorrupt`. |
| P3-7: reports were gitignored | `.gitignore` allowlists this report and the Fable code review. `git check-ignore -v` shows the `!` rules at lines 47–48. | none |
| P3-8: test gaps | Worst-case ledger: `TestLocalDiagnosticsWorstCaseFitsTheAdmissionLedger` (2,500 rows, raw input just under 1 MiB, 2,000 real 8 KiB files hashed, `partial`, 16,384,000 bytes hashed). Observed child termination: `TestLocalDiagnosticsTimeoutTerminatesTheChild` records the fake core's PID and polls `kill(pid, 0)` until the process is gone. Corrupt pointer, the pruner failures, and P2-1 are covered above. | The HTTP 409 and 503 mappings are still tested only through `respondDiagnosticsError` (see residuals). |

**Mutation check.** Each fix was reverted in turn and its test re-run. The activity, fsync, corrupt-pointer, listing, entry-stat, temp-cleanup, changed-bytes, self-checksum, and PostgreSQL-context tests each fail with the fix reverted. The temp-cleanup assertion was first too loose, because a later temp-create failure also satisfied it. It now requires `recover diagnostics temp file`.

### Post-repair evidence

The machine is an Apple M1 with 8 GB of RAM, running Go 1.26.1. The Rust binary is `rust-core/target/release/xmustard-core` with sha256 `d644743b…`. No Rust source changed.

| Gate | Command | Result |
|---|---|---|
| Go build/vet/test | `cd api-go && go build ./... && go vet ./... && go test ./... -count=1` | all 7 packages ok (`workspaceops` 33.0 s) |
| Race, diagnostics | `go test -race -count=1 -run 'LocalDiagnostics\|DiagnosticsIdentity\|PostgresDiagnostics\|DiagnosticsStatus\|…'` on `./internal/workspaceops`; full `-race` on `./internal/budget ./cmd/xmustard-api ./cmd/xmustard-ops` | ok. A `-race -run Diagnostic` run also matches `TestLspLiveDiagnosticsUseWorkspaceScopedSession`, which reports a race. That race predates this work; see residuals. |
| Linux cross-build | `GOOS=linux GOARCH=amd64 go build ./... && go vet ./internal/workspaceops` | ok |
| Backend | `make check-backend` | exit 0. Go ok; Rust 148 tests (131+5+12); clippy has warnings that were already there. |
| Real no-DB e2e | `scripts/e2e/local-diagnostics.sh` (×6) | 16/16 every run. MCP lists exactly nine tools. Sampled peaks are below. |
| Pi + native PostgreSQL control | `scripts/e2e/pi-adapter.sh` | 17/17 unit, tsc clean, 16/16 e2e. The diagnostics backend is `postgres`. xMustard-owned sampled peak 73.5 MiB. |
| Pi without PostgreSQL | the same e2e with `XM_PG_BIN_DIR=` and the same binaries | 16/16. The backend is `local`. xMustard-owned sampled peak 63.2 MiB. |
| MCP evidence | `scripts/e2e/mcp-evidence.sh` | 23/23 |
| Resource gate | `scripts/bench/rss.sh` (×5) | **passed 3 of 5**. Details below. |

**Resource gate, sampled xMustard-owned tree peak (gate ≤ 100 MB):**

| Build | Run 1 | Run 2 | Run 3 | Run 4 | Run 5 |
|---|---|---|---|---|---|
| Post-repair | 111.5 MB, FAIL | 93.8 | 110.6 MB, FAIL | 78.1 | 93.5 |
| Pre-repair A/B | 81.9 | 72.7 | 82.5 | — | — |

The pre-repair A/B used the same source with the repair reverted, so the runs were not interleaved; run order was post 1–2, pre 1–3, post 3–4.

- Both failures peak in the step `CLI diagnostics imports with concurrent reads`.
- Per-process sampled maxima match across the builds: API 36–46 MiB, `xmustard-ops` 16–28, core 26–32, MCP 13–17.
- The difference at the peak is the two idle MCP shims. They were resident at 11–16 MiB each in the failing runs and 2.7–7.7 MiB in the passing ones. Their code is not touched by this repair, which points to OS paging on an 8 GB host rather than a repair regression. That is not proven.
- `local-diagnostics.sh` peaks are 94.6–103.2 MiB over the five post-repair runs that wrote a report. A sixth run passed 16/16 without one. With the envelope self-check disabled (A/B), three runs peaked at 92.2–99.3 MiB.
- The earlier single-run figures (86.5 MB and 88.8 MiB) sit at the favourable end of this spread.

## Residual gaps and limits

- **Resource target not robustly met.** The 50–100 MB target holds in only some sampled runs of the fixed workload. The gate can exceed it when the API, a CLI import, its core child, and two resident MCP shims are sampled together. Treat the target as unproven for this slice.
- **Corrupt pointer blocks imports.** Failing closed means a corrupt `latest.json` refuses every import until an operator removes it. Reads return an explicit corrupt error meanwhile.
- **Durability failure after the pointer rename.** If only the pointer-directory fsync fails, the import returns an error even though the new pointer is already visible. A retry then publishes a second baseline. This is deliberate: durability is not confirmed.
- **Pre-existing LSP test race.** `TestLspLiveDiagnosticsUseWorkspaceScopedSession` races on `lspSessionIdleTTL`: the stub cleanup at `lsp_definition_test.go:400` writes it while the session reaper reads it. It appears under `-race` together with other tests. Neither file is modified here, and it is not fixed.
- **HTTP 409 and 503** are still mapped and tested only through `respondDiagnosticsError`, not through a full request.

- **Symlinked roots.** Row-path relativization is Rust's lexical `strip_prefix` of the recorded root. If a report uses the realpath of a symlinked root, for example `/private/var` versus `/var` on macOS, its rows stay absolute and read as `path_outside_workspace`. Identity is then `unavailable`. This is conservative, not wrong. Canonicalizing would need a Rust change.
- **Undetected mutation.** `changed` compares size and mtime before and after hashing. An in-place rewrite that keeps both the size and the mtime during the read goes undetected.
- **Retention clock.** Retention and pruning use the envelope file's mtime. The data dir is assumed to be server-owned.
- **CLI symlinks.** CLI `local_operator` refuses only a symlink in the final path component. Intermediate symlinks are followed, which is acceptable for operator-named files.
- **Non-Unix builds.** They already fail in `run_control.go` (`Setpgid`, `syscall.Kill`), so the fail-closed `!unix` store file could not be compile-checked for Windows.
- **Temp cleanup test** is skipped when running as root, because root ignores directory permissions.
- **Stale Rust binary risk.** `rust-core` has uncommitted changes that predate this work, and the e2e runs used the existing `target/release/xmustard-core`. A no-op `cargo build --release` left the binary byte-identical, so it is current with that source.
- **No producer attestation.** There is no daemon, no automatic compiler or LSP producer, no mirroring or migration, and no per-issuer isolation in open loopback mode. All of these were deferred by the plan.

Plan sha256 after the implementation updates: `34737118a604e33e74ae9e792494d6087f22e0a6a7160e4d25522d8586859754`.
Plan sha256 after the post-review repair (P3-5 context, envelope self-checksum, fail-closed pruning, committed-run warning): `95e0da6debffea9321b09748b3d28a211cbdc6dca915da0307ebcdf7a10cd7de`.
