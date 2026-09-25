# Implementation review: memory-headroom execution — 2026-09-24

Reviewer: Claude Fable 5.1, independent. Subject: `docs/reviews/2026-09-24-memory-headroom-implementation.md`
(Opus 5.5) against the approved plan `docs/plans/2026-09-24-memory-headroom.md` (sha256
`745d1595…41acb`, re-hashed, matches) and the prior audit `…-fable-memory-headroom-plan-final.md`.
Read and rebuilt in the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`) and the
read-only revision-A worktree `/private/tmp/xmustard-revA-l9b1F4`. This file is the only write. No gate
run, no source, benchmark, plan, main-checkout, commit, push or merge. Scratch builds went to the job
tmp directory only.

## Verdict: APPROVED

Every checkable claim in the implementation report reproduced. I found no false claim. The conclusion
holds: the one candidate was rejected on measured evidence, no A/B acceptance series was owed, default
binaries are byte-identical to revision A, and the 50–100 MB target remains unproven. The residual gaps
below are missing proof or precision, not contradictions; none changes the verdict.

## What I verified independently

### 1. Five untuned baseline attempts (plan Phase 1.1–1.2)

| Check | Result |
|---|---|
| Preregistration before attempt 1 | `series1-baseline/PREREGISTRATION.md` stamped 15:32:37Z; S1-A1 sidecar `start_utc` 15:32:42Z |
| Back-to-back, one session, no gaps | Sidecar start/end: A1 15:32:42–15:34:17, A2 15:34:17–15:35:50, A3 15:35:50–15:37:21, A4 15:37:22–15:38:54, A5 15:38:54–15:40:27Z. Each attempt starts within 1 s of the previous end, so no hidden attempt fits between them |
| No sixth or earlier unregistered attempt | Every `rss_bench` report on the host (job tmp dirs, `/private/tmp`, main checkout evidence) was listed by mtime and provenance. The last report with A provenance (`3a1c6d67`/`a5aaf5d7`/`8562a222`) before the series is `/private/tmp/xmustard-gogc10-rss2.json` at 20:31 local (the plan's own tuning experiments); none exists between 20:31 and the series start at 21:02 local, and none after it outside the evidence directory |
| Launcher unchanged | `tooling/gate_attempt.sh` sha256 `154ce975…87e5` equals the value in PREREGISTRATION.md; launch line is exactly the plan's `env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS -u GOMAXPROCS -u XMUSTARD_CORE_BIN bash <tree>/scripts/bench/rss.sh --report <path>` |
| Sidecar attestations | All five: ambient and sanitized env-name greps empty, `RUSTFLAGS|CARGO_` empty, no `.cargo/config*`, `go env GOFLAGS GODEBUG` two empty lines, `rg` exit 1 (no match), three pinned script hashes equal to the plan's, head `cd13e2b7…` |
| Sidecar report/log hashes | `report_sha256` and `log_sha256` in each sidecar equal the files on disk (5/5) |
| Report provenance | All five: head `cd13e2b7…`, `source_diff_sha256` `a5aaf5d7…`, 39 untracked / `8562a222…`, api `3a1c6d67…`, mcp `7045b1da…`, core `d644743b…`, `core_bin_prebuilt: false`, `script_sha256` for `rss_bench.py`/`harness.py` as pinned; `gate.valid: true`, `invalid_reasons: []` |
| Outcomes | Peaks 88.1 / 91.9 / 105.8 / 95.3 / 104.1 MB; passed true/true/false/true/false. Matches the report's 3 pass / 2 fail and `series.log` rc 0/0/1/0/1 |
| Host snapshots | `vm_stat` and `sysctl vm.swapusage` before and after each attempt present; free pages 4,586–15,324 (16 KiB pages), swap used 2,077–2,165 MB of 3,072 MB, matching the report's "4.6k–15k" and "2.1 GB of 3 GB" |
| Peak attribution | S1-A3 and S1-A5 `processes_at_peak`: API 52.6 / 51.2 MiB, CLI 19.1 / 19.4, core 13.9 / 13.4, shims 7.7+7.6, no `git` child. Passing peaks show API 40.6–44.2 MiB. The report's "API RSS separates failures from passes" is what the retained snapshots show |
| Core built in-tree | `harness.py:240` runs `cargo build --release` when `XMUSTARD_CORE_BIN` is unset; in-tree `target/release/xmustard-core` hashes `d644743b…` now |

### 2. Source, binary and script hashes

| Artifact | Claimed | Reproduced here |
|---|---|---|
| `scripts/bench/rss_bench.py`, `scripts/e2e/harness.py`, `scripts/bench/rss.sh` | `7c7254e0…`, `97314722…`, `7d8d9e54…` | identical, in both trees |
| Default `xmustard-api` / `xmustard-mcp` from the current tree (A + hook + benchmark), sanitized `go build` | `3a1c6d67…f77` / `7045b1da…86b9` | identical |
| Default `xmustard-api` / `xmustard-mcp` from the A worktree | `fd19aa90…` / `771af904…` | identical (path-dependent build, as the report states at line 74) |
| `go build -tags profile` from the current tree | A+hook `82ab978e…` | identical |
| `go version -m` prebuild record (`prebuild/A-prebuild.txt`) | no `-tags`, go1.26.1, `vcs.*` absent | as recorded; `go env GOFLAGS GODEBUG` empty on this host |
| `source_diff_sha256` (current tree and A, plan's exact path set) | `a5aaf5d7…` | identical in both |
| Full `git diff HEAD --binary` (current tree and A) | `e0ecc995…` | identical in both |
| A untracked manifest (134 files, per-file sha list) | `d73cd8a2…` | list equals `revA/A-untracked.list`; recomputed hash `d73cd8a2…` |
| A `untracked_source_sha256` (39 files) | `8562a222…` | identical |

### 3. Revision A reproduction and read-only state

`git worktree list` shows `/private/tmp/xmustard-revA-l9b1F4` detached at `cd13e2b`. `api-go`,
`rust-core/src`, `Cargo.*` and `scripts` are mode `r-x`/`r--` (checked on `api-go/internal/budget/budget.go`
and `scripts/bench/rss_bench.py`). `diff -rq` between A and the current tree (excluding `.git`, `target`,
evidence, reviews, logs) shows exactly: three added files (`profile_hook.go`, `profile_hook_test.go`,
`diagnostics_read_bench_test.go`), one changed untracked test file (`diagnostics_local_test.go`, two
signatures `*testing.T` → `testing.TB`), the ignored plan file, and `integrations/pi/.cache`. `budget.go`
is byte-identical to A: the rejected patch left no residue.

### 4. Profiler-only build-tag isolation and no product behaviour change

- `go list -deps ./cmd/xmustard-api ./cmd/xmustard-mcp` contains no `net/http/pprof`; with `-tags profile`
  it does. `strings` on the default binary finds no `net/http/pprof`.
- `profile_hook.go:1` is `//go:build profile`; wiring is an `init()` behind the tag, so `main.go` is
  untouched (no stub, no tracked-file change). Listener is opt-in via `XMUSTARD_PPROF_ADDR`, refuses any
  address whose host is not a parsed loopback IP (`loopbackAddr`, lines 46–53); `localhost`, `0.0.0.0`,
  `:6060` and a LAN IP are refused, `127.0.0.1` and `[::1]` accepted (`profile_hook_test.go`, run here with
  `-tags profile -v`: PASS). `go vet -tags profile` clean.
- Tracked source diff hash unchanged (`a5aaf5d7…`), default binaries byte-identical (above). There is no
  product behaviour change to accept, so the plan's acceptance A/B series and the MCP/Pi reruns are
  correctly reported as not applicable (report lines 15, 191).

### 5. Profiling evidence

- The allocation-share table (report lines 93–101) reproduces from the retained profiles:
  `go tool pprof -sample_index=alloc_space -base debug/D1.1.allocs.enter.pb.gz <82ab978e binary> debug/D1.1.allocs.exit.pb.gz`
  gives 453.16 MB total, `budget.(*CaptureWriter).Write` 49.2%, `bytes.growSlice` under `json.(*Encoder).Encode`
  23.7%, `json.Unmarshal` 18.7%, `localDiagnosticsStore.Rows` 3.4%, `base64…DecodeString` 1.1%.
- Instrumented reports are self-labelling: their `binaries_sha256.xmustard-api` is `82ab978e…` (A+hook) or
  `b74d4a83…` (B+hook), untracked counts 41/42 and, for B, a different `source_diff_sha256` (`fd331325…`),
  so `summarize.py` against `expected-A.json` would flag every one as a provenance mismatch. They cannot be
  mistaken for gate attempts. Summaries carry `instrumented: true, gate_attempt: false`.
- A+hook peaks 93.4 / 95.5 / 99.2 MB (PA2, PA1, PA3) and B+hook 100.3 / 117.0 / 117.0 MB (PB2, PB1, PB3)
  match the report. PB1 and PB3 peaks show two `git` children and shims at 10–15 MiB, supporting the
  report's "noisy, not decisive either way".

### 6. Rejected candidate

- `rejected-candidate/presize-budget.patch` is a stat-size capacity hint inside `ReadAllAdmitted` with the
  reservation taken through the same scope; refusal and limits unchanged; three tests cover exact
  reservation, stale stat size in both directions, and pool/limited-scope refusal.
- Handler benchmark: I ran `BenchmarkLocalDiagnosticsGet` on the current tree (30×): 38.3 ms/op,
  23,688,636 B/op, 37,832 allocs/op, identical to the retained A rows in `handler-bench-A-vs-B.txt`.
  The retained B rows (13.80–14.11 MB/op, 36.6–37.4 ms) support "−42% bytes, ≈ −3% latency".
- Isolated API RSS (`api-read-rss-1/api_read_rss.json`, `instrumented: false, gate_attempt: false`):
  A max RSS 46.6–49.1 MiB, B 52.8–55.0 MiB, non-overlapping, B higher in 5/5 trials; CPU per read 29.0 vs
  25.9 ms; idle 14 MiB both. The A binary is the A-worktree build `fd19aa90…` (reproduced above).
- **Adequacy.** This microbench is adequate to reject: the plan forbids an RSS gain bought with an
  unacceptable tradeoff, and here there is no RSS gain at all on the attributed process under the same
  two-reader load, with an effect larger than the trial spread. It is not, and is not claimed as,
  evidence about the fixed gate or the target. Rejecting a candidate does not require the ten-run A/B
  series; that series is owed only for a selected candidate.

### 7. Checks

`make check-backend` log: 7 Go packages ok (6 cached), Rust 131 + 5 + 12 tests, clippy warnings
pre-existing. `go-test-count1.log`: all 7 packages ok uncached. I re-ran `go build ./...`,
`go test -count=1 ./cmd/xmustard-api/ ./internal/budget/` (ok), the tagged hook test (PASS), and
`go vet -tags profile` (clean). `local-diagnostics-e2e.json` hashes `5cf5cf55…` as reported, 16/16,
including "MCP exposes exactly the nine tools" and two concurrent CLI imports. No native PostgreSQL
control ran; the report says so.

## Residual gaps (non-blocking)

1. **Sidecar attestation is record-only.** `gate_attempt.sh:17,19,21` end in `|| true` and nothing asserts
   emptiness or aborts; the launcher also records neither the report's provenance nor the prebuild hashes
   (those checks live in `summarize.py`, whose output `SUMMARY.txt` is retained). For this series I read all
   five sidecars and they are empty where required, so the attestation stands, but a future series should
   fail the attempt on any non-empty line. `hostname -s` printed `192`, so host identity rests on the
   hardware string and memsize only.
2. **Untracked-hash comparability after the helper change.** Because `diagnostics_local_test.go` changed,
   the current tree's 39-file `untracked_source_sha256` is now `b8fca47f…` (42-file value `357312e8…`), not
   A's `8562a222…`, even though default binaries are identical. The report discloses the change (line 159)
   but not this consequence. Any future B preregistration must register the new values, and A-slot runs
   must use the A worktree, as the plan already requires.
3. **CPU-share claim not reproducible from retained evidence.** The "Unmarshal incl. checkValid 27%, encode
   11%" statement (line 106) rests on the handler benchmark's `-cpuprofile`, which exists only in the Opus
   job tmp (`benchA.cpu`, `benchA.mem`), not under `docs/benchmarks/evidence/…`. Missing proof, not a
   false claim; copying those two files in closes it. The allocation table's source pair is also unnamed in
   the report; it is `debug/D1.1.allocs.{enter,exit}.pb.gz` (the only A+hook pair whose exit fetch
   succeeded), and pprof's "453 MB" is MiB.
4. **B binaries are not rebuildable to the recorded hashes.** `a1a588ef…` (B default) and `b74d4a83…`
   (B+hook) depended on the exact B tree path and state, which was reverted. The patch text is retained,
   so the candidate is reproducible in substance, not by hash. Note that B also changed the MCP shim
   (`aafbd089…` in PB reports) because `budget` is shared; the isolated microbench measured the API only.
   Moot for a rejected candidate.
5. **Isolated RSS trial order.** `api_read_rss.py:70` runs A then B within every trial (sorted labels), not
   alternating starts. A carry-over effect cannot be excluded by design, only by the non-overlapping ranges.
6. **Unexplained CPU-profile fetch failures.** In PA1 and PB2 the *exit* allocs fetch also failed with
   `Connection refused`, which means the API had already exited when `Sampler.stop` ran; the
   `RemoteDisconnected` on the 2-second `/debug/pprof/profile` during the CLI step is consistent with the
   same teardown race rather than a hook fault. A lead for the next profiling pass, not a finding.
7. **Hook test coverage** is limited to `loopbackAddr`. Nothing in CI asserts that default builds exclude
   `net/http/pprof`; the check is the out-of-band `go list -deps` done here and in the report.
8. **Evidence is gitignored** (`.gitignore:34`, `:69`, `:88` for the plan). The revision-A worktree remains
   registered in the shared `.git`; the report's removal command is correct. Neither is a review defect.

## Non-claims

I did not run the gate, the MCP-evidence or Pi suites, or `cargo test`. The rebuilds above are hash
reproductions, not gate attempts. The 50–100 MB target remains unproven; series 1 (3/5) and the historical
3/6 stand as characterization only.
