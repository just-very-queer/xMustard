# Recheck: repaired database-free local diagnostics — 2026-09-24

Reviewer: Claude Fable 5.1, independent of the implementer (Claude Opus 5.5) and of the
earlier Fable code review. Subject: the repaired, uncommitted slice in
`/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`; the main checkout
`/Users/for_work/Developer/xMustard` is at the same base). Inputs: the plan
`docs/plans/2026-09-24-local-diagnostics.md` (sha256 `95e0da6d…cd7de`, the post-repair
hash the implementation report records), the prior code review
`docs/reviews/2026-09-24-fable-local-diagnostics-code-review.md` (`ebe5a414…b7ace8`), and
the implementation report `docs/reviews/2026-09-24-local-diagnostics-implementation.md`
(`d17f08fa…89c53f`). Every claim below was checked against current source or by running
it. This file is the only write; no source, plan, test, script or main-checkout file was
changed; nothing was committed, pushed or merged.

## Verdict: import-ready on code and tests; the RSS target must be recorded as unproven

- **No hard blocker in the code.** All seven prior findings (P2-1, P2-2, P2-3, P3-4, P3-5,
  P3-6, P3-7) are fixed in the current source, each with a regression test that passes.
  The seam, the no-fallback rule, the nine tools, the GET-only `CORE_ONLY` allowlist,
  Go-owned capture with the Rust checksum cross-check, lexical path gating before any
  stat, bounded and charged identity hashing, ordered publish under a cross-process
  `flock`, fail-closed pruning, and the local label set match the plan.
- **Full Go suite, vet, race and the real no-database e2e pass here** (commands below).
  Rust sources and `xmustard-mcp/main.go` are byte-identical to the main checkout; the
  release core binary has the same hash in both trees.
- **The one open item is the resource gate.** The sampled process-tree gate now fails in
  3 of 6 post-repair runs (the implementer's 111.5 and 110.6 MB, plus my 104.3 MB). The
  plan's own gate text says to keep the 50–100 MB target unproven until it passes, so this
  is a reporting obligation, not a code defect: the slice may be imported with the target
  marked unproven, and that call belongs to the human merger. Details in the resource
  section.
- **Residuals** (below) are real but bounded; none evicts an unexpired original, none
  changes a contract, and none needs a fix before import.

## Prior findings against current code

| Finding | Current code | Test | Status |
|---|---|---|---|
| P2-1 committed publish reported as failure | `RunDiagnosticsCtx` keeps the result after `Publish`; an activity-append error becomes a `warnings` entry naming the run ID (`diagnostics.go:578-598`); `DiagnosticsRunResult.Warnings` is `omitempty` (`:78`) | `TestLocalDiagnosticsActivityFailureKeepsCommittedRunID` makes `activity.jsonl` a directory; import succeeds, one warning, run is latest | fixed |
| P2-2 directory fsync ignored | `writeAtomic` returns the directory open/fsync error through the `diagnosticsSyncDir` seam after either rename (`diagnostics_local_store.go:419-423`) | `TestLocalDiagnosticsDirectoryFsyncFailureIsReturned`: failed `runs/` fsync leaves the pointer on the prior run | fixed |
| P2-3 pruner treated failures as "no latest" | `recoverAndCheckQuota` refuses on pointer read error, either `ReadDir` error, non-ENOENT `Entry.Info` error, non-ENOENT temp removal error (`:320-358`); pool refusal in `readPointer` is `ErrOverloaded`, not corrupt (`:439-441`); a failed `os.Remove` of an expired entry keeps it counted (`:364-368`) | `TestLocalDiagnosticsPruneFailsClosed`: four corrupt pointers on read and import paths, store/runs listing, entry stat, temp cleanup (`chmod 0555`); envelope and pointer untouched in every case | fixed |
| P3-4 changed-file bytes uncharged | `observeDiagnosticPathsWithin` adds `n` for every outcome (`diagnostics_input.go:379-380`); `hashDiagnosticPath` reads `min(size+1, remaining)` (`:445-449`) and returns `n` on the changed path (`:456`) | `TestDiagnosticsIdentityChargesChangedFileBytes` | fixed |
| P3-5 PostgreSQL publish ignored ctx | `postgresDiagnosticsStore.Publish` passes `ctx`; `persistDiagnosticsBaseline(ctx, …)` derives its 60 s timeout from it (`diagnostics.go:282-284, 766-768`); the plan's PostgreSQL paragraph states it | `TestPostgresDiagnosticsPublishUsesTheImportContext` (value, ≤60 s deadline, cancellation propagate) | fixed |
| P3-6 historical reads unchecked | Envelope schema `xmustard.diagnostics.local.v2` carries `envelope_sha256` as its second field, sealed in place over 64 `'0'`s (`:151-158, 261-269`); every `readEnvelope` recomputes it before decode (`:491-493`), in addition to the pointer SHA on `Latest` | `TestLocalDiagnosticsHistoricalReadVerifiesEnvelopeChecksum` flips one row byte in a historical envelope → `ErrDiagnosticsStoreCorrupt` | fixed |
| P3-7 reports gitignored | `git check-ignore -v`: implementation report and code review are allowlisted at `.gitignore:47-48`; plan at `:90` | — | fixed; **this recheck file is still ignored by `.gitignore:34`** (I may not edit `.gitignore`); add `!docs/reviews/2026-09-24-fable-local-diagnostics-recheck.md` if it should travel with the slice |
| P3-8 test gaps | Worst-case ledger (`TestLocalDiagnosticsWorstCaseFitsTheAdmissionLedger`: 2,500 rows, raw within 8 KiB of 1 MiB, 2,000 real 8 KiB files hashed, `partial`), observed child death (`TestLocalDiagnosticsTimeoutTerminatesTheChild` polls `kill(pid,0)`), corrupt pointers, pruner failures, P2-1 | all present and passing | fixed except HTTP 409/503 (below) |

## Assessment of the areas asked about

**Full HTTP mapping and test gaps.** Through a real request, `diagnostics_local_test.go`
covers: no-DSN GET 200 with `baseline` omitted; `CORE_ONLY` 404 for status, run and live;
400 for absolute, traversal and symlink input and for a body `input_authority` field;
413 without `Retry-After` for a 1 MiB+ input; 200 run with `storage_backend:"local"`; GET
`available`; status `freshness_basis`; 400 for an unknown run ID. **409 (quota) and 503
(busy) are still reached only by calling `respondDiagnosticsError` directly**
(`diagnostics_local_test.go:121-145`), as the report admits. The mapping itself is
correct by inspection (`main.go:583-600`; `writeOverloaded` sets `Retry-After: 1`,
`:432-435`), and the CLI e2e exercises busy at the process level ("each publishes or
reports busy"). Residual, not blocking. Store-corrupt errors fall to `respondError` and
map to 500 (tested), which is the intended "explicit error, never `no_baseline`".

**Post-pointer fsync semantics.** The pointer is written with `context.Background()`
after the envelope is durable (`diagnostics_local_store.go:177-181`), so cancellation
cannot strand a committed envelope without a pointer. If the pointer-directory fsync
fails after the rename, the import returns an error while the new pointer is already
visible; a retry publishes a second baseline with a fresh nonce ID. If the pointer rename
or temp write fails, the fsynced envelope stays in `runs/` as a `diag_*.json` orphan:
readable by ID, counted against the 256 MiB/100 quota, pruned only after 24 h. Both are
plan-consistent ("a failed directory open or fsync fails the publish"; "never evict an
unexpired original") and the report lists the first. No change needed; operators should
know that a durability error can leave an extra baseline.

**Concurrency and cache safety.** `newLocalDiagnosticsStore` is called only from
`selectDiagnosticsStore` (`diagnostics.go:319`), once per entry-point call, so `held` and
`cache` are per-call and never shared across goroutines. The cache serves one verified
envelope to `Latest`/`ByID` followed by `Rows` in the same call, so a concurrent prune
cannot make `Rows` observe a different version. Readers are lock-free and take a fresh
pool scope for the 4 KiB pointer that they close immediately (`:436-437`). The workspace
`flock` is taken before `planDiagnostics` (`diagnostics.go:538-544`), so it is held
across both Rust children; a second same-workspace import waits 2 s then gets 503 even
while the first is still normalizing. That is what the plan specifies ("held for the
whole import") but it means same-workspace imports serialize for up to the 120 s
deadline. `go test -race` is clean on the diagnostics selection in `workspaceops` and on
the whole `budget` and `xmustard-api` packages. The package-level test seams
(`diagnosticsReadDir`, `diagnosticsSyncDir`, `diagnosticsPublishHook`,
`diagnosticIdentityHook`, `connectSemanticPostgres`) are mutated by non-parallel tests only.

**Docs allowlisting.** Verified as above. Only the leftover "Postgres DSN is required"
strings in the repo are in semantic-index and symbol-read code, not diagnostics.

**Minor notes, no action required.**
- Outside-root row paths consume slots of the 2,000-path cap (`diagnostics_input.go:373-376`
  keys on the loop index). Conservative.
- Retention expiry in the envelope is computed from `createdAt`; pruning uses file mtime,
  which is a few milliseconds later, so the promise is met with margin.
- `TestDiagnosticsStatusLocalNoBaselineWithoutPostgres` proves "no dial" through the
  fixture's `connectSemanticPostgres` stub that fails the test on any call
  (`diagnostics_local_store_test.go:29-34`), not in the test body itself.

## Resource evidence, honestly

The gate is the sampled sum of `ps` RSS over the xMustard-owned tree every 100 ms
(`rss_bench.py`), limit 100,000,000 bytes. All post-repair runs on this host (Apple M1,
8 GB; the implementer's four saved reports are in their job directory
`~/.claude/jobs/18159cba/tmp/rss{1..4}.json`, not in the repo; no report of the 111.5 MB
run was found anywhere):

| Run | Peak | Step at peak | Processes at peak (MiB) | Gate |
|---|---|---|---|---|
| implementer 1 | 111.5 MB | CLI imports (per report) | not saved | FAIL |
| implementer 2 (`rss1.json`) | 93.8 MB | two concurrent index clients | api 31.6, core 21.9, core 12.8, mcp 11.9, mcp 11.2 | pass |
| implementer 3 (`rss2.json`) | 110.6 MB | CLI imports with concurrent reads | api 42.3, ops 16.4, mcp 15.7, core 15.6, mcp 15.5 | FAIL |
| implementer 4 (`rss3.json`) | 78.1 MB | CLI imports with concurrent reads | api 39.6, ops 25.8, mcp 4.8, mcp 4.2 | pass |
| implementer 5 (`rss4.json`) | 93.5 MB | CLI imports with concurrent reads | api 45.7, ops 28.2, mcp 7.7, mcp 7.7 | pass |
| **this recheck** (`rss-fable-recheck.json`, sha256 `1f15fd6e…483b66`) | **104.3 MB** (99.5 MiB), 843 samples, median interval 105 ms | CLI imports with concurrent reads | api 44.2, ops 19.3, mcp 14.5, core 13.9, mcp 7.6 | **FAIL** (19/20 checks) |

Findings:
- The gate is not met robustly: 3 passes, 3 failures. The new CLI-import step is the
  heaviest point of the workload by construction (API, `xmustard-ops`, its core child and
  both idle MCP shims resident together), and it peaks between 78 and 112 MB depending on
  how much of the two MCP shims the OS keeps resident (4–16 MiB each). The per-process
  maxima of the API, CLI and core are consistent across passing and failing runs.
- The host was under memory pressure during my run: `vm_stat` showed ≈4,000 free 16 KiB
  pages (≈63 MB) of 8 GB. That supports the report's paging explanation but does not
  prove it, and it is not a reason to discard the failures: the gate is defined as a
  sampled tree peak on this workload, and it fails half the time.
- The same gate already failed on this host before this slice existed: the lean-context
  workstream's saved `rss-corrected-run3.json` (110.5 MB) and `rss-root-final1.json`
  (106.4 MB) fail at "two concurrent index clients". So the target was marginal before
  the diagnostics step was added; this slice adds a second step that sits near the line.
- My e2e run (`local-diag-fable-recheck.json`, sha256 `cca81a71…2ea5f`, 16/16) sampled a
  94.4 MiB (99.0 MB) xMustard-owned peak over only 30 samples, consistent with the
  report's 94.6–103.2 MiB spread.
- Conclusion: report the 50–100 MB target as **unproven for this slice**, as the plan
  requires until the gate passes. The byte-admission evidence (pool returns to 0, ledger
  worst case ≈15 MiB) is admission evidence, not an RSS ceiling. Whether an unproven
  target blocks import is the human merger's decision; nothing in the code needs to
  change to make the numbers honest.

## Checks performed here

| Check | Command (from `/private/tmp/xmustard-opus-l9b1F4`) | Result |
|---|---|---|
| Build and vet | `cd api-go && go build ./... && go vet ./...` | ok |
| Focused diagnostics tests | `cd api-go && go test ./internal/workspaceops -run 'Diagnostic\|OpenWorkspaceFileBeneath' -count=1 -v` | 39 pass, 1 skip (`TestDiagnosticsLockHelper`, the child-process helper) |
| HTTP, budget, CLI packages | `go test ./cmd/xmustard-api -run 'Diagnostics\|CoreOnly' -count=1`; `go test ./internal/budget ./cmd/xmustard-ops -count=1` | ok |
| Race | `go test -race ./internal/workspaceops -run 'LocalDiagnostics\|DiagnosticsIdentity\|PostgresDiagnostics\|DiagnosticsStatusLocal\|DiagnosticsInputAuthority\|DiagnosticsPostgresSelection\|DiagnosticsLock' -count=1`; `go test -race ./internal/budget ./cmd/xmustard-api -count=1` | ok |
| Full Go suite | `cd api-go && go test ./... -count=1` | all 7 packages ok (`workspaceops` 33.3 s) |
| Rust/MCP untouched | `diff -rq rust-core/src /Users/for_work/Developer/xMustard/rust-core/src`; `diff -q api-go/cmd/xmustard-mcp/main.go …`; `shasum -a 256` of both `xmustard-core` binaries | identical (only `.DS_Store` differs); binary `d644743b…3576` in both |
| Gitignore | `git check-ignore -v` on plan, both reviews, implementation report, this file | as stated in P3-7 |
| Real no-DB e2e | `XMUSTARD_CORE_BIN=$PWD/rust-core/target/release/xmustard-core bash scripts/e2e/local-diagnostics.sh --report …` | 16/16 |
| Resource gate | `XMUSTARD_CORE_BIN=$PWD/rust-core/target/release/xmustard-core bash scripts/bench/rss.sh --report …` | 19/20; sampled peak 104.3 MB, FAIL |

Reviewed file hashes (sha256, worktree): `diagnostics.go` `da9d7ebe…1c902`;
`diagnostics_input.go` `131c02a8…a00f3`; `diagnostics_local_store.go` `389c3116…ff2289`;
`diagnostics_local_store_unix.go` `ebe6b45d…b84613`; `diagnostics_local_store_other.go`
`80e9a3db…3f781b4`; `diagnostics_local_store_test.go` `43d5610c…13e02c`;
`diagnostics_test.go` `708ef119…61312`; `safepath_unix.go` `9fc155d0…d53b5`;
`context_packet.go` `54ea2963…9aba3`; `budget.go` `6d3e065c…feab6d`;
`cmd/xmustard-api/main.go` `31cfc5b8…56a35`; `diagnostics_local_test.go`
`1285e9d7…957b9`; `cmd/xmustard-ops/main.go` `47b5cc89…316f05`;
`scripts/bench/rss_bench.py` `7c7254e0…faa9f`; `scripts/e2e/local_diagnostics.py`
`929b5a3b…823946`; `pi-adapter.e2e.ts` `4c795ec8…2c061`; `.gitignore` `9a2613df…62e66`.
Files unchanged since the prior review keep the hashes it lists.

## Non-claims

- I did not rerun `scripts/e2e/pi-adapter.sh`, `scripts/e2e/mcp-evidence.sh`, `cargo test`
  or `cargo clippy`; those figures remain the report's. No Rust source changed.
- No PostgreSQL instance was available; the PostgreSQL control and no-fallback behavior
  were verified through the fake-connection tests only.
- Windows/`!unix` builds were not compile-checked.
- One RSS run and one e2e run are single samples on a loaded 8 GB host; they widen the
  evidence, they do not settle it.
