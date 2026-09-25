# Plan recheck: fixed-workload RSS headroom — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: revised `docs/plans/2026-09-24-memory-headroom.md`,
sha256 `7998bbcd29a4eb64f37cea8d4e7221d5b70bb72ca6eaff97bcfdad038d2af5da`, read in the
isolated worktree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`, dirty local
diagnostics slice, 39 untracked source files). Prior review:
`docs/reviews/2026-09-24-fable-memory-headroom-plan.md` (R1–R6). Benchmark hashes
re-verified unchanged: `rss_bench.py` `7c7254e0…faa9f`, `harness.py` `97314722…`,
`rss.sh` `7d8d9e54…`. This file is the only write. No gate run, no source, plan,
benchmark, main-checkout, commit, push or merge.

## Verdict: CHANGES_REQUIRED (two small, precise blockers)

R1–R6 are each concretely addressed. The valid-A-failure rule is correct. The
interleaved A/B design is feasible and its provenance is checkable from the frozen
report. Two gaps remain that defeat the plan's own "provable, non-gameable" standard;
both are one-paragraph edits.

## R1–R6 status

| Item | Where addressed | Status |
|---|---|---|
| R1 sanitized-env attestation | Phase 1.1 (lines 50–60): sidecar per report with report sha256, exact `env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS bash scripts/bench/rss.sh --report …` command, empty `env` grep, empty `rg` for `SetGCPercent|SetMemoryLimit|//go:debug` | Addressed; see B1 for two uncovered inputs |
| R2 pre-registered attempt count | Phase 1.2 (61–66), Acceptance (130–143): exactly five numbered attempts, back-to-back, all reports retained incl. invalid/exception, no sixth, new series needs number + reason | Addressed |
| R3 host drift | Interleaved `A1,B1,…,A5,B5` (131–132); `vm_stat` + `sysctl vm.swapusage` before/after every attempt, stored beside report (67–68, 137–139) | Addressed |
| R4 profiling contradiction | Phase 1.3 (71–81): `-tags profile` build-tag-gated loopback `net/http/pprof`; default binaries carry no pprof import (verified: none today); instrumented runs never gate attempts; instrumented binary hashes listed separately | Addressed |
| R5 read-path contributors + freshness | Evidence (19–24), Phase 1.4 (83–85), Phase 2 (104–108): git probe named; coalescing only reuses a probe started at/after request arrival; HEAD-move-between-GETs → `stale` regression test; cache keyed `(run ID, envelope sha256)` with latest pointer read per request | Addressed |
| R6 bounded cache vs admission | Phase 2 (108–113): one decoded envelope per workspace, ≤ 4 MiB serialized (matches `diagnosticsMaxEnvelopeBytes = 4 << 20`), retained bytes in `/api/health` (health already exposes `transient_pool` counters, `main.go:659-667`), no-cache candidate preferred | Addressed |

## A/B baseline snapshot: feasibility and gameability

- **Feasible.** `rss.sh:7` does `cd "$(dirname "$0")/../.."` and `harness.py:23` derives
  `REPO_ROOT` from its own file, so launching revision A's copy of the script builds and
  benchmarks A's tree; `build_binaries` runs `go build` and `cargo build --release` from
  that root on every run (`harness.py:231-242`). A fresh detached worktree needs a full
  Rust release build once.
- **Checkable from the frozen report.** `provenance()` already records `head`,
  `source_diff_sha256` (diff of `api-go`, `rust-core/src`, `Cargo.toml`, `Cargo.lock`),
  `untracked_source_sha256` (untracked under `api-go`, `rust-core/src`),
  `binaries_sha256` for all three binaries, `core_bin_prebuilt`, and `script_sha256`
  (`rss_bench.py:408-431`). A run on the wrong tree is therefore detectable after the
  fact, provided the expected values are written down before the first run (see B2).
- **Valid A failure handling is correct.** Lines 139–142: a failed-but-valid A is
  retained as baseline evidence and is not a rerun trigger; only an invalid A makes the
  paired comparison inconclusive; a failed or invalid B fails the candidate series;
  the gate claim (144–146) rests on the five B reports alone. This is the right split:
  A informs attribution, B carries the gate.

## Blockers

**B1. Close the two attestation holes R1 left open.**
1. `harness.py:237` reads `XMUSTARD_CORE_BIN` from the raw `os.environ`, not through
   `clean_env`, so an ambient value substitutes a prebuilt core binary into a "default"
   run. The plan's `env -u` list and `env | grep` do not cover it. Require, per run:
   `env | grep '^XMUSTARD_'` empty in the sidecar, and `core_bin_prebuilt == false` with
   `binaries_sha256.xmustard-core` equal to that revision's fresh in-worktree
   `cargo build --release` output.
2. `env -u GOFLAGS` does not clear a persisted `go env -w` value (`GOENV` file at
   `~/Library/Application Support/go/env`, currently empty), which still reaches the
   `go build` in `build_binaries` and could inject `-tags profile` or ldflags. Require
   the sidecar to include `go env GOFLAGS GODEBUG` (must be empty) and
   `go version -m` of the built `xmustard-api` and `xmustard-mcp` showing no `-tags`
   build setting. Recommended, not required: add `-u GOMAXPROCS` to the launch command.

**B2. Complete the revision-A reproduction recipe and bind reports to it.**
`scripts/bench/` and `scripts/e2e/` are untracked (`git status`: `?? scripts/bench/`,
`?? scripts/e2e/`) and outside the untracked-source manifest, so a detached worktree of
`cd13e2b` contains no benchmark. The plan (93–99) reproduces only the tracked diff and
the `api-go`/`rust-core/src` manifest. Add: copy the three pinned scripts (and the rest
of `scripts/e2e/` that `harness.py` needs) into A and verify their pinned hashes; launch
every A attempt via A's own `scripts/bench/rss.sh` and every B attempt via B's; and put
the expected `head`, `source_diff_sha256`, `untracked_source_sha256`, `binaries_sha256`
and `script_sha256` for A and for B into the preregistration, with the rule that a
report whose provenance does not match its slot is invalid.

## Non-blocking clarifications

- Phase 1.2 says "any failure or invalid report fails this series" for a series that
  only characterizes variance. State that the baseline series has no pass/fail outcome,
  only recorded counts, so a "failed" baseline is never grounds for a new series.
- State explicitly that an invalid A attempt voids only the attribution claim ("the
  candidate caused the change"), not the B gate evidence, or that it voids the series;
  lines 141–146 admit both readings.
- The plan now implies fifteen full gate runs (five baseline, ten interleaved) plus
  instrumented runs and two Rust release builds. Feasible, but say it, and store the
  Phase 1 baseline reports under the same series/sidecar scheme as the A/B.
- The existing `head_moved` test (`diagnostics_local_store_test.go:541`) exercises the
  status function only; the required R5 regression test must go through the GET
  handler path where coalescing would live.

## Non-claims

I did not run the gate, `make check-backend`, or any e2e suite. Ambient `GO*`,
`CARGO*` and `XMUSTARD_*` variables were empty on this host at recheck time; that is an
observation, not an attestation for future runs.
