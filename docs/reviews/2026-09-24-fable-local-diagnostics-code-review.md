# Code review: database-free local diagnostics — 2026-09-24

Reviewer: Claude Fable 5.1, independent of the implementer (Claude Opus 5.5). Subject:
the uncommitted slice in `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`), compared
file by file against the untouched checkout `/Users/for_work/Developer/xMustard` at the
same base. Inputs: `docs/plans/2026-09-24-local-diagnostics.md` (sha256
`34737118a604e33e74ae9e792494d6087f22e0a6a7160e4d25522d8586859754`), the APPROVED plan
recheck `docs/reviews/2026-09-24-fable-local-diagnostics-plan-final.md`
(`c700112c…a2e96`), and the implementation report
`docs/reviews/2026-09-24-local-diagnostics-implementation.md` (`2567b727…349bff`).
Every acceptance claim below was checked against source or by running it; the report's
prose was not taken as evidence. No source, plan, test, or script was edited; nothing
was committed, pushed, or merged. This file is the only write.

## Verdict: APPROVED

The seam, the no-fallback DSN rule, the nine tools, the GET-only `CORE_ONLY` allowlist,
Go-owned capture with the Rust checksum cross-check, path confinement before any stat,
bounded identity work, ordered publish under a cross-process `flock`, lock-free readers
with one retry and explicit corrupt errors, and the local label set all match the plan
and the code. The full Go suite passes here, and a worst-case import (1 MiB raw, 2,500
rows, 2,000 real paths) fits the 16 MiB ledger through the real CLI. Three P2 findings
should be fixed in the slice before merge; none changes a gate, the seam, or a contract,
and none can evict an unexpired original. Human final merge authority is unchanged.

## Reviewed files (sha256, worktree)

| File | sha256 |
|---|---|
| `api-go/internal/workspaceops/diagnostics.go` | `fa0a6d06…a6114` |
| `api-go/internal/workspaceops/diagnostics_input.go` | `ae74f3cc…422bbf` |
| `api-go/internal/workspaceops/diagnostics_local_store.go` | `d7b59561…79f5bd` |
| `api-go/internal/workspaceops/diagnostics_local_store_unix.go` | `ebe6b45d…b84613` |
| `api-go/internal/workspaceops/diagnostics_local_store_other.go` | `80e9a3db…781b4` |
| `api-go/internal/workspaceops/diagnostics_local_store_test.go` | `eb40ad33…24f90` |
| `api-go/internal/workspaceops/diagnostics_test.go` | `708ef119…61312` |
| `api-go/internal/workspaceops/safepath_unix.go` | `9fc155d0…d53b5` |
| `api-go/internal/workspaceops/safepath_test.go` | `6dcb1e77…14057` |
| `api-go/internal/workspaceops/context_packet.go` | `54ea2963…9aba3` |
| `api-go/internal/budget/budget.go` | `6d3e065c…feab6d` |
| `api-go/internal/budget/budget_test.go` | `5acaf426…8aa96f` |
| `api-go/internal/rustcore/root.go` | `b6221ed0…66fe5e` |
| `api-go/cmd/xmustard-api/main.go` | `31cfc5b8…56a35` |
| `api-go/cmd/xmustard-api/diagnostics_local_test.go` | `1285e9d7…957b9` |
| `api-go/cmd/xmustard-ops/main.go` | `47b5cc89…316f05` |
| `scripts/e2e/local-diagnostics.sh` / `local_diagnostics.py` | `54884dce…04b9bc` / `929b5a3b…823946` |
| `scripts/e2e/pi-adapter.sh` | `2723b962…611a6` |
| `scripts/bench/rss_bench.py` | `7c7254e0…faa9f` |
| `integrations/pi/test/e2e/harness.ts` / `pi-adapter.e2e.ts` | `ae725c60…91363` / `4c795ec8…2c061` |
| `rust-core/target/release/xmustard-core` (used by my runs) | `d644743b…3576` |

`api-go/cmd/xmustard-mcp/main.go` and every file under `rust-core/src/` are byte-identical
to the baseline checkout, as the report states.

## Findings, prioritized

### P2-1. A committed publish is reported as a failure when the activity append fails

`RunDiagnosticsCtx` publishes (`diagnostics.go:571`) and only then appends the activity
record (`diagnostics.go:575-592`); an append error returns `nil, err`. For local storage
the envelope and pointer are already renamed and fsynced, so the baseline is live on the
next GET while the HTTP client sees 500 and the CLI exits non-zero with no run ID. A retry
creates a second baseline with a fresh nonce ID (`diagnostics_local_store.go:169`) and
consumes one of the 100 slots. The PostgreSQL path had the same shape before this slice,
but local storage now makes the duplicate cheap and silent. Fix in the slice: treat the
append failure as a warning on the result (or log it) once `Publish` has returned, so the
response always carries the run ID of a committed baseline. No test covers this path.

### P2-2. Directory fsync errors are ignored on both renames

`writeAtomic` propagates the file `Sync` error (`diagnostics_local_store.go:326`) but
discards the directory open and fsync (`:347-350`). The plan's crash contract ("readers see
old or new complete envelopes only") rests on the envelope rename being durable before the
pointer rename; a failed `runs/` fsync followed by a successful pointer publish leaves,
after a crash, a pointer that names a missing envelope, which readers surface as an
explicit corrupt error rather than the prior baseline. Two-line fix: return the directory
open/fsync error like the file fsync error.

### P2-3. The pruner treats a pointer read failure as "no latest"

`recoverAndCheckQuota` reads the pointer to protect the current baseline from pruning, but
any error leaves `latestID` empty (`diagnostics_local_store.go:275-277`). `readPointer`
opens a fresh pool scope (`:363-365`), so under transient-pool pressure it can be refused
without any on-disk corruption; the expired-but-current envelope is then removed
(`:292-296`) before the new envelope exists. If the import then fails (quota, cancellation,
write error) the valid pointer dangles and readers get an explicit corrupt error instead of
the prior baseline. Scope: only envelopes older than the 24 h promise, so no unexpired
original is evicted, but "preserve prior valid envelope and pointer on … quota rejection
or failed import" is not met in that window. Fix: on a pointer read error skip the prune
(or fail the publish with the corrupt error) rather than pruning with `latestID == ""`.
Relatedly, entries whose `Info()` fails are silently excluded from the byte and count
quota (`:286-289`), and `ReadDir` failures in the temp-file sweep are swallowed
(`:264-266`); both under-count against the plan's "disk quota includes orphan/temp files".

### P3-4. Bytes read for `changed_during_import` paths are not charged to the 64 MiB bound

`observeDiagnosticPaths` adds `id.Size` to `BytesHashed` only for `observed` paths
(`diagnostics_input.go:373-375`), and `remaining` is derived from it (`:370`). A path
whose file changes during the read has already streamed up to `before.Size()+1` bytes
through the hasher (`:431`) but leaves `remaining` unchanged, so the true bound on bytes
read is 64 MiB × (1 + changed paths), up to 2,000 × 64 MiB in theory. It needs concurrent
mutation of large listed files, and each file is still bounded by its own size, so this is
a documentation/accounting gap rather than an exploit. Fix: charge `n` regardless of
outcome. The unit test proves the path cap and the no-prefix-hash rule with `remaining=3`
(`diagnostics_local_store_test.go:310-327`), not this case.

### P3-5. PostgreSQL publish ignores the request context and the 120 s deadline

`postgresDiagnosticsStore.Publish` discards `ctx` (`diagnostics.go:279`) and
`persistDiagnosticsBaseline` opens its own 60 s background context (`:758`). Reads do use
the request context (`:991`, `:1068`). The plan permits wrapping persistence unchanged,
but the report's "one 120 s import deadline" and "cancellation … publishes nothing" hold
for local storage only; a cancelled HTTP request can still complete a PostgreSQL write up
to 60 s later. State this in the plan's PostgreSQL paragraph or thread `ctx` through.

### P3-6. Historical reads skip the envelope checksum

`Latest` verifies the envelope against the pointer's SHA-256
(`diagnostics_local_store.go:409-413`); `ByID` and `Rows` pass `wantSHA == ""` (`:482`,
`:498`), so a bit-flipped historical envelope that still parses and whose base64 payload
still matches its own checksum is served as valid. Row content is not covered by any
checksum on that path. Storing the envelope SHA inside the run (or a sidecar) would close
it; low priority given the server-owned data dir assumption.

### P3-7. The implementation report and this review are gitignored

`.gitignore:34` ignores `docs/reviews/*`; the slice allowlists the plan recheck and final
review (`.gitignore:45-46`) but not
`docs/reviews/2026-09-24-local-diagnostics-implementation.md`
(`git check-ignore -v` confirms). The evidence file that the plan's status line points at
would not be committed with the slice. Add the allowlist line (and one for this file).

### P3-8. Test gaps

- Worst-case ledger input is not under test: `TestLocalDiagnosticsMaxRowsFitTheAdmissionLedger`
  uses 2,500 rows of ~45 bytes (`diagnostics_local_store_test.go:303`), and both e2e
  scripts use similar small rows (`local_diagnostics.py:159`, `rss_bench.py:591`). My CLI
  run below covers 1 MiB / 2,500 rows / 2,000 real paths; add it as a test so the P2-G
  arithmetic stays pinned.
- No corrupt-pointer case (malformed `latest.json`, wrong schema, bad SHA length) on the
  read or publish path; only a tampered envelope and a pointer to a missing envelope are
  tested (`:359-382`).
- No test for P2-1 or for `Entry.Info` / `ReadDir` failures in the pruner.
- The timeout case asserts only a prompt error (`:482-491`); the report says "the fake core
  child is killed" but nothing observes the `sleep` child's death.
- HTTP 409 and 503 mappings are tested only through `respondDiagnosticsError` directly
  (`diagnostics_local_test.go:121-145`), not through a request.

### Notes (no action required)

- **Safepath fix.** I ran the baseline `openWorkspaceFileBeneath` in a scratch module:
  `a/f.txt` and `a/b/c/f.txt` returned `errEmptyPath`, `f.txt` and `a/b/f.txt` opened.
  The bug is real and predates the slice, and the fix (`safepath_unix.go:50,61,77`) is
  correct; the report's "every path with two or more components" overstates it (even
  component counts fail, because the final `openat` reuses the closed root fd number).
- **Row-item over-cap.** Items are counted by streaming before Rust runs
  (`diagnostics_input.go:163-169`); the count stops at cap+1 (`:314-316`), so the 413
  message says "2501" for any larger input, and an over-cap object with trailing garbage is
  413 rather than 400. Both deterministic; cosmetic.
- **`countDiagnosticItems` comment.** "costs no second copy" is not literally true: each
  `json.RawMessage` decode copies the item, and a non-`diagnostics` key in the object form
  copies its whole value (≤ 1 MiB). Transient, GC'd, unadmitted, bounded by the raw cap.
- **`PlanDiagnosticsCtx`** runs both Rust children without the per-process import slot
  (`diagnostics.go:531` is inside `RunDiagnosticsCtx` only); `budget.Children` still bounds
  it.
- **Activity kind.** `postgres.materialize.diagnostics` → `diagnostics.materialize` for
  both backends is plan-mandated; the only consumer in the repo is the updated test
  (`diagnostics_test.go:164`). No frontend or Rust reader of the old kind exists.
- **Error mapping.** `os.ErrNotExist` → 404 "Workspace not found" in
  `respondDiagnosticsError` (`main.go:595`) could also catch a store-side ENOENT wrapped by
  `writeAtomic`; `Latest`/`ByID`/`Rows` already translate their ENOENTs, so only the
  temp-write path can leak it. Cosmetic.

## Checks performed

| Check | Result |
|---|---|
| `diff -rq` worktree vs baseline checkout (excluding build/vendor/archive) | 26 changed or new source, test, script, and doc files; `xmustard-mcp/main.go` and `rust-core/src` identical |
| `cd api-go && go build ./... && go vet ./... && go test ./... -count=1` | all 7 packages ok (`workspaceops` 32.6 s, `xmustard-api` 18.2 s) |
| Baseline `openWorkspaceFileBeneath` in a scratch module, nested paths | 2- and 4-component paths fail with `empty path`; 1- and 3-component open (bug confirmed) |
| Real `xmustard-ops diagnostics run`, no DSN, temp data dir, git repo: 2,500 items / 1,032,500 B, all paths missing | exit 0, 2,500 rows, identity `unavailable` (0/2,500 hashed), envelope 3,518,928 B |
| Same, 2,500 items / 1,031,000 B, 2,000 real 8 KiB files + 500 missing | exit 0, identity `partial` (2,000 hashed, 16,384,000 B), envelope 3,740,428 B, ≈1 s |
| Same, 2,500 small items on one path (control) | exit 0, identity `matched`, envelope 1,918,539 B |
| `git check-ignore -v` on the plan, both review files, and the implementation report | plan and plan-final allowlisted; implementation report and this file ignored by `docs/reviews/*` |
| `selectDiagnosticsStore` is the only DSN seam; PostgreSQL adapter wraps `persistDiagnosticsBaseline` / `readDiagnosticRun` / `readDiagnosticRows` unchanged; no fallback branch | confirmed by reading `diagnostics.go:254-320` |
| 16 MiB ledger composition: `Scope.Limited` reserves in the parent, child stdout charged in 256 KiB chunks then ×2 on decode (`root.go:55,95`), capture ≤ 1 MiB, identity buffer 64 KiB, envelope estimate acquired before `Marshal` | matches the plan's worst-case sum; empirically fits above |
| HTTP: `respondDiagnosticsError` 413 without `Retry-After` for `ErrDiagnosticsLimit`/`ErrAdmissionLimit`, 409 quota, 400 invalid, 501 unsupported, 503 + `Retry-After` for `ErrOverloaded` | `main.go:583-600`; unit test covers each |
| `CORE_ONLY` allowlist untouched; status/run/live 404 there | `diagnostics_local_test.go:68-77` |
| Local label mapping vs plan: `changed`→`stale`, `partial`/`unavailable`→`available`+token, `head_moved` precedence, no `fresh`/`dirty_provisional` | `diagnostics_local_store.go:525-556`; `TestLocalDiagnosticsStatusLabels` |
| Reader retry then explicit error, never `no_baseline` | `diagnostics_local_store.go:455-470`; tested |
| Crash windows before either rename keep the prior baseline; temp files recovered | hook-driven test `:424-458` |
| Cross-process lock, kernel release on holder death | helper-process test `:636-683` |
| RSS sampling method in `rss_bench.py` and `local_diagnostics.py` (`ps` tree sums, CLI trees counted while alive) | read; consistent with the report's description |
| Pi fixture moved under `repo/.xmustard-e2e/`, posted relatively, written after the `auto_scan` load; no-Postgres branch now imports through the CLI | `pi-adapter.e2e.ts:92-115,175-179` |

## Non-claims

- I did not re-run `scripts/e2e/local-diagnostics.sh`, `scripts/e2e/pi-adapter.sh`,
  `scripts/e2e/mcp-evidence.sh`, `scripts/bench/rss.sh`, or `cargo test`/`clippy`; the
  16/16, 16/16, 23/23, 20/20, and 148-test figures are the report's, not mine. The RSS
  peaks (86.5 MB gate, 67.4 / 69.2 MiB Pi, 88.8 MiB e2e) are sampled lower bounds on
  fixed workloads, as the report says; they are not an RSS ceiling and I did not
  reproduce them.
- I did not rebuild `rust-core/target/release/xmustard-core`; my CLI runs used the binary
  present in the worktree (sha256 above, built 17:00 today). The report's "byte-identical
  after a no-op release build" was not re-verified.
- No PostgreSQL instance was available to me; the PostgreSQL control and the
  no-fallback behavior were checked through the existing fake-connection tests only.
- `go test -race` was not run.
- Windows/`!unix` builds were not compile-checked (the report notes `run_control.go`
  already prevents it).
- I did not assess the frontend or any docs outside the plan and the two review files;
  a grep found no doc still describing the old 400 / "Postgres DSN is required" contract.
