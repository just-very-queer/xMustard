# Memory headroom — implementation and evidence report, 2026-09-24

Implementer: Claude Opus 5.5. Plan: `docs/plans/2026-09-24-memory-headroom.md`
(sha256 `745d1595…41acb`, verified). Final audit: `…-fable-memory-headroom-plan-final.md`.
Isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`). No commit, push, merge, or
main-checkout change. Evidence: `docs/benchmarks/evidence/2026-09-24/memory-headroom/`.

## Outcome

**Result: the 50–100 MB target is still unproven. Phase 2 stopped without a candidate.**
- Phase 1 baseline (series 1): 5/5 valid, **3 pass / 2 fail**. Peaks were 88.1, 91.9,
  105.8, 95.3 and 104.1 MB.
- The one profile-supported candidate cut allocation and CPU, but it *raised* isolated
  API RSS, so it was rejected and reverted.
- No A/B gate series was preregistered or run, because no candidate was selected.
- The only shipped-source additions are:
  - a `profile`-build-tag loopback pprof hook, absent from default binaries;
  - a handler benchmark.
- Default `xmustard-api` and `xmustard-mcp` builds are byte-identical to revision A:
  `3a1c6d67…f77` and `7045b1da…86b9`.

## Phase 1.1–1.2: attested baseline

- **Preregistration.** `series1-baseline/PREREGISTRATION.md` was written before attempt 1.
  It lists expected `head` `cd13e2b7…`, `source_diff_sha256` `a5aaf5d7…`, 39 untracked
  sources with `untracked_source_sha256` `8562a222…`, binary hashes from a sanitized
  prebuild (`prebuild/A-prebuild.txt`: `go version -m`, no `-tags`), `core_bin_prebuilt=false`,
  and all three pinned script hashes.
- **Launch and sidecars.** Each attempt ran through `tooling/gate_attempt.sh`. It uses the
  plan's `env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS -u GOMAXPROCS -u XMUSTARD_CORE_BIN`
  launch, refuses to overwrite a report, and records a sidecar with:
  - host identity and launch command;
  - variable NAMES only, for both the ambient and the sanitized environment (all empty);
  - `RUSTFLAGS|CARGO_` names (empty) and no `.cargo/config*` (Fable notes 1 and 3);
  - `go env GOFLAGS GODEBUG` (empty);
  - `rg SetGCPercent|SetMemoryLimit|//go:debug api-go` (no match);
  - `rss.sh` and the other pinned hashes;
  - exit code, report and log SHA-256.
- **Host snapshots.** `vm_stat` and `vm.swapusage` were captured before and after every
  attempt.
- **Provenance.** `tooling/summarize.py` checked every report against `expected-A.json`:
  all five match, with `core_bin_prebuilt=false` and the core hash `d644743b…` from a fresh
  in-tree `cargo build --release`.

| Attempt | Report sha256 | Peak MB | Valid | Gate | API / CLI / core / shims at peak (MiB) |
|---|---|---|---|---|---|
| S1-A1 | `daea4418…` | 88.1 | yes | pass | 40.6 / 28.0 (exiting) / – / 7.6, 7.6 |
| S1-A2 | `a600543f…` | 91.9 | yes | pass | 44.2 / 28.0 (exiting) / – / 7.7, 7.7 |
| S1-A3 | `681154b5…` | **105.8** | yes | **fail** | **52.6** / 19.1 / 13.9 / 7.7, 7.6 |
| S1-A4 | `b9b1f431…` | 95.3 | yes | pass | 42.4 / 19.6 / 13.5 / 7.7, 7.7 |
| S1-A5 | `0430fe09…` | **104.1** | yes | **fail** | **51.2** / 19.4 / 13.4 / 7.7, 7.6 |

- **Peak step.** Every peak fell in the CLI-import and concurrent-read step. The
  two-concurrent-index step peaked at 77–87 MB.
- **What separates failures from passes.** Attribution comes from the peak snapshot, using
  parent IDs. The shims were a steady 7.6–7.7 MiB across this series, and no git child was
  resident at any failing peak. What separates failures from passes is **API RSS**:
  51–53 MiB in the failures against 41–44 MiB in the passes.
- **Host state.** Free pages ran about 4.6k–15k (16 KiB each) and swap about 2.1 GB used
  of 3 GB. Other agent processes were resident.
- **Status.** The series is characterization only. It does not replace the historical 3/6.

## Revision A

Revision A is `/private/tmp/xmustard-revA-l9b1F4`, a detached `git worktree` at `cd13e2b`.
- **Tracked diff.** Applied with `git diff HEAD --binary`. The full-diff hash `e0ecc995…`
  and `source_diff_sha256` `a5aaf5d7…` are equal in both trees.
- **Untracked files.** All 134 untracked non-ignored files were copied, including
  `scripts/bench` and `scripts/e2e`. The per-file hash list is equal (`d73cd8a2…`),
  `untracked_source_sha256` is `8562a222…`, and the three pinned scripts were re-verified.
- **Read-only.** The source was made read-only before any B edit
  (`revA/A-MANIFEST.txt`).
- **A's own binaries.** A's fresh builds are recorded there too. The core matches
  (`d644743b…`). Go binaries embed the build path, so A's differ: api `fd19aa90…`,
  mcp `771af904…`.

## Phase 1.3–1.4: profiling (diagnostic only, never gate attempts)

**Hook.** `api-go/cmd/xmustard-api/profile_hook.go` carries `//go:build profile`.
- Its listener is opt-in through `XMUSTARD_PPROF_ADDR`.
- It refuses any non-loopback address (test: `profile_hook_test.go`).
- It serves `/debug/pprof/*` and `/debug/xm/stats` (memstats and rusage).
- Default builds contain no `net/http/pprof` (`go list -deps`), and their hash is unchanged.
- Instrumented API binaries: A+hook `82ab978e…`; B (candidate)+hook `b74d4a83…`.

**Drivers.**
- `tooling/profile_driver.py` imports the unchanged `rss_bench.py` and swaps in the tagged
  API in memory only.
- `tooling/api_read_rss.py` runs the API alone under the same two concurrent GET readers on
  the same fixture and 2,500-row baseline.

**Findings on the CLI-import and concurrent-read step.** The A+hook allocs profile delta
covers 453 MB over about 3.4 s:

| Share of allocations | Source |
|---|---|
| 49% | `budget.CaptureWriter.Write` append growth while reading the 1.97 MB envelope |
| 24% | JSON response encoder buffer growth |
| 19% | Envelope `Unmarshal` |
| 3% | Row copy and sort |
| 1% | base64 |

**Per-GET cost.**
- Handler benchmark (`BenchmarkLocalDiagnosticsGet`): 23.7 MB allocated, 37.8k allocations,
  about 38 ms per GET, and about 4 GC cycles per GET. The response is 1.85 MB.
- CPU: in-process CPU is dominated by the envelope `Unmarshal`, including `checkValid`
  (27%), then the encode (11%). Most of the wall time is waiting on the per-request
  `git status` child.
- The index step lasted 0.37 s and added about 13–60 ms of API CPU.

**Latency across the whole CLI step.** Client p50 was about 0.4 ms. Most reads land before
the first import publishes, so they return `no_baseline` without a git probe. Reads with a
baseline take 50–70 ms under two readers.

**Git children.** They appear in some instrumented peaks at up to 2 × 5 MiB, but in none of
the failing baseline peaks.

**Profiler gaps.**
- Fetches of `/debug/pprof/profile` during the CLI step failed every time with
  `RemoteDisconnected`, and the API log showed no panic; index-step CPU profiles and allocs
  fetches worked. The cause was not found. The handler benchmark's `-cpuprofile` stands in.
- Profiler overhead could not be separated from host noise at n=1. A+hook runs peaked at
  93.4, 95.5 and 99.2 MB (hook-only). They are labelled instrumented, excluded from the gate,
  and never used as gate evidence.

## Phase 2: tested candidate, rejected

**Candidate.** The smallest profile-supported, general, no-cache change: when
`budget.ReadAllAdmitted` reads a regular file, reserve its stat size in the scope, then
allocate the buffer once. The size is only a hint, and admission refusal and limits are
unchanged. The patch and its three tests are kept in `rejected-candidate/presize-budget.patch`:
- exact capacity and reservation;
- a stale stat size in either direction;
- pool refusal and the `Limited`-scope cap.

**Handler benchmark** (interleaved, 12 runs × 200 GETs):

| Metric | A | B |
|---|---|---|
| Allocated per GET | 23.7 MB | 13.8–14.1 MB (−42%) |
| Latency per GET | 37.4–39.0 ms | 36.6–37.4 ms (≈ −3%) |

**Isolated API RSS** (`api-read-rss-1/api_read_rss.json`; 5 interleaved trials, two readers,
4 s each, default sanitized builds, A `fd19aa90…` against B `a1a588ef…`):

| Metric | A | B |
|---|---|---|
| Max RSS | 46.6–49.1 MiB | **52.8–55.0 MiB** (higher in 5/5 trials) |
| CPU per read | ≈ 29.0 ms | ≈ 25.9 ms (−11%) |
| Idle RSS | 14 MiB | 14 MiB |

- **Instrumented full workload** (candidate): 100.3, 117.0 and 117.0 MB, all above the line
  on a loaded host. The shims were 10–15 MiB here and two git children were resident, so
  these runs are noisy and not decisive either way.
- **Why RSS rose.** Unverified. A plausible explanation is fewer GC cycles and less
  scavenging, so more freed pages stay resident.
- **Decision.** Rejected under the plan's rule against unacceptable tradeoffs: lower CPU
  bought higher RSS. Reverted exactly; `diff -r` against A shows only the three
  added test and hook files and the `testing.TB` helper change.

## Why no other small fix is credible from this evidence

**Live heap sets the RSS here, not allocation volume.** Per request, the live set peaks
during decode: the 1.97 MB raw envelope plus the decoded rows and their strings, with the
rows slice regrowing. The encode phase holds the row copy, the original bytes and an encoder
buffer of about 2 MB. Two readers in flight give the heap goal (about 2 × live) seen as a
42–53 MiB API.

**Options considered and not tried:**

| Option | Why not |
|---|---|
| Streaming response encode | Trims only the encode phase |
| Streaming or partial decode | Not a small change |
| Git-probe coalescing | Must wait for a probe started after arrival, which adds latency, and git children were absent from the failing baseline peaks |
| Decoded-envelope cache | Retains memory, and the plan prefers no cache first |
| `GOGC`/`GOMEMLIMIT` | Excluded as product defaults |

**Plan rule applied.** The profiles do not support a small general-purpose change with a
credible RSS gain, so the work stops here with the target unproven.

## Checks (current tree = A + hook + benchmark)

| Check | Result |
|---|---|
| `make check-backend` | rc 0 (`check-backend.log`). Rust has 131 + 5 + 12 tests passing; the clippy warnings already existed |
| `cd api-go && go test -count=1 ./...` | All 7 packages ok (`go-test-count1.log`) |
| `go vet -tags profile` and tagged tests | ok |
| `scripts/e2e/local-diagnostics.sh` (no DB, sanitized env) | 16/16 (`local-diagnostics-e2e.json` `5cf5cf55…`) |
| Native PostgreSQL | Not available (`pg_isready`: no response). No live control ran; only the fake-connection tests in the Go suite cover PostgreSQL |
| MCP evidence, Pi e2e, A/B gate series | **Not run.** No candidate was selected, and default binaries are byte-identical to A, so there is no behaviour change to accept |

## Remaining gaps

1. The gate still fails intermittently. Series 1 was 3/5 and the historical result 3/6. The
   API read path under two concurrent 2,500-row GETs is the attributed lever, at 41–53 MiB.
   Any fix must lower the per-request live set:
   - streaming decode or encode of the envelope; or
   - a smaller stored or served representation, which is a contract decision;

   Otherwise the workload or target needs revisiting by the human owner.
2. The CPU-profile fetch failure during the CLI step is unexplained.
3. There is no live PostgreSQL control, and no MCP-evidence or Pi rerun, because no
   behaviour changed.
4. Evidence files sit in the isolated tree only, and git ignores both this report
   (`.gitignore:34`) and the evidence directory (`.gitignore:69`). Allowlist them if they
   should travel.
5. The revision-A worktree `/private/tmp/xmustard-revA-l9b1F4` is registered in the shared
   `.git`. Remove it when done:
   `chmod -R u+w /private/tmp/xmustard-revA-l9b1F4 && git worktree remove --force /private/tmp/xmustard-revA-l9b1F4`
