# Plan review: fixed-workload RSS headroom — 2026-09-24

Reviewer: Claude Fable 5.1, independent of the plan's author. Subject:
`docs/plans/2026-09-24-memory-headroom.md`, sha256
`cdda2126b561872be15fb25b260f33baab5c1dff37f8c3f5e15aa61f2b78c524`, read in the
isolated worktree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`, dirty local
diagnostics slice). Checked against `AGENTS.md`, `docs/VISION.md`,
`docs/plans/2026-09-24-local-diagnostics.md`,
`docs/reviews/2026-09-24-fable-local-diagnostics-recheck.md`, current Go source, the
unchanged benchmark (`scripts/bench/rss_bench.py` `7c7254e0…faa9f`,
`scripts/e2e/harness.py` `97314722…`, `scripts/bench/rss.sh` `7d8d9e54…`), and the
saved reports named below. This file is the only write. No plan, source, benchmark,
main-checkout file, commit, push or merge.

## Verdict: CHANGES_REQUIRED

The plan's direction is right: fixed gate unchanged, target reported as unproven,
runtime tuning kept out of product defaults, nine MCP tools and freshness preserved,
no Docker or daemon, human merge authority untouched. It is not implementable as
written because (1) its "no environment tuning" baseline and acceptance cannot be
proven from the reports it relies on, (2) its five-run acceptance has no attempt
discipline and no host-drift control, (3) its Phase 1 profiling is impossible on the
untouched binaries while the plan forbids source changes, and (4) it omits the two
read-path contributors that every saved peak snapshot actually shows. Six changes are
required before implementation; three simplifications are recommended.

## Evidence the plan cites, rechecked

- **Gate definition.** `GATE_BYTES = 100_000_000`, one `ps -axo pid=,ppid=,rss=,comm=`
  snapshot every 100 ms, sum over API + both shims + descendants + live transient CLI
  roots (`rss_bench.py:44,140,162-169`). Matches the plan.
- **Post-repair untuned runs.** Six reports exist: 93.8 pass, 110.6 FAIL, 78.1 pass,
  93.5 pass (implementer, `~/.claude/jobs/18159cba/tmp/rss{1..4}.json`), 104.3 FAIL
  (`~/.claude/jobs/bda04621/tmp/rss-fable-recheck.json`), plus the unsaved 111.5 FAIL.
  The plan's 3/6 and its per-process figures at 104.3 and 110.6 MB match those files.
  One further 86.5 MB pass (`~/.claude/jobs/82808e4e/tmp/rss.json`, 18:14, before the
  implementer's series) has unclear repair status and should not be counted either way.
- **Pre-diagnostics history on the same base** (12-step workload, no CLI step):
  72.3, 80.6, 84.9, 95.9, 99.4 pass; 106.4, 110.5 FAIL
  (`docs/benchmarks/evidence/2026-09-24/`). API sampled maximum then: 25.9–30.8 MiB.
  The plan's "already marginal" statement is correct.
- **The six tuning experiments** (`/private/tmp/xmustard-go*-rss*.json`, all on head
  `cd13e2b`, source diff `a5aaf5d7…`, 39 untracked source files `8562a222…`, identical
  binaries `3a1c6d67`/`7045b1da`/`d644743b`):

| Report | Peak | Step at peak | Gate | API `getrusage` maxrss | API self CPU |
|---|---|---|---|---|---|
| `gomem32-rss` (GOMEMLIMIT=32MiB) | 113.4 MB, 1 sample over | CLI imports | FAIL | 48.2 MB | 5.9 s |
| `gogc20-rss` | 97.4 MB | CLI imports | pass | 39.0 MB | 6.9 s |
| `gogc20-rss2` | 94.2 MB | CLI imports | pass | 38.4 MB | 7.0 s |
| `gogc20-rss3` | 96.4 MB | two concurrent index clients | pass | 35.8 MB | 6.8 s |
| `gogc10-rss` | 88.2 MB | cold query | pass | 35.1 MB | 6.8 s |
| `gogc10-rss2` | 97.4 MB | CLI imports | pass | 38.8 MB | 7.2 s |

  Untuned post-repair runs had API maxrss 41.6–49.1 MB and self CPU 5.6–6.2 s. So
  GOGC=20/10 lowered the API high-water by roughly 8–12 MB at about 15–25 % more API
  CPU, and the tree still sat 2.6–5.8 MB under the gate in four of five passes.
  GOMEMLIMIT=32MiB had no visible effect on the API high-water. **None of the six
  reports records GOGC or GOMEMLIMIT anywhere** (`grep` finds neither); their
  settings are known only from file names. The plan's refusal to treat them as proof
  or as product defaults is correct and should stay.

## Required changes

**R1. Make "no environment tuning" provable (non-gameability).**
`harness.py:55-59 clean_env` and `rss_bench.py:605 ops_env` strip only `XMUSTARD_*`
and pass everything else to the API, both shims and the CLI, so an ambient `GOGC`,
`GOMEMLIMIT`, `GODEBUG` or `GOFLAGS` silently tunes a "default" run.
`rss_bench.py:408-430 provenance()` records os, machine, python, go version and cpus
only. A tuned and an untuned report are therefore indistinguishable by their own
content. The plan pins the benchmark hash, so it cannot add provenance fields. It must
instead require an out-of-band attestation per run, stored beside each report with the
report's sha256: the exact launch command run through
`env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS …`, the output of
`env | grep -E '^GO(GC|MEMLIMIT|DEBUG|FLAGS)='` (must be empty), and a source grep
showing no `SetGCPercent`, `SetMemoryLimit` or `//go:debug` in `api-go` (none today).
Apply to the Phase 1 baseline and to the acceptance series alike.

**R2. Pre-register the attempt count and forbid selection.**
"Rerun five times; all five must pass" says nothing about a sixth attempt. With an
observed untuned pass rate near one half, repeating until five consecutive passes
appear would eventually succeed by chance. Require: exactly five numbered attempts,
scheduled before the first starts, run back to back with no source, binary or fixture
change between them, every report retained (including invalid ones and harness
exceptions, which the harness still writes), any failure ends the series as a fail,
and a new series needs a stated reason and a new series number. State the same for
the Phase 1 baseline five.

**R3. Control host drift by interleaving.**
The host is an 8 GB MacBook Air under memory pressure (`vm_stat` free ≈ 4,300 pages,
≈ 70 MB, at review time; ≈ 4,000 during the recheck). Peaks on identical source spread
by 33 MB (78.1–111.5). Five baseline runs followed later by five candidate runs cannot
separate the fix from host state. Require the before/after comparison to be run as an
interleaved A/B (baseline, candidate, baseline, …) on the same host session, and
require the plan's "host memory-pressure observations" to be concrete: `vm_stat` and
`sysctl vm.swapusage` captured immediately before and after every run, stored beside
the report. The harness does not record these, so the plan must say where they go.

**R4. Resolve the profiling contradiction.**
Phase 1 asks for Go heap, allocation, GC and CPU profiles of API, CLI and MCP, but
`api-go` contains no `pprof` import, no profiling flag and no env hook (grep), and the
plan's status line authorizes no source change. Either authorize a minimal
build-tag-gated (`-tags profile`) or env-gated `runtime/pprof` hook that is absent from
default builds, or restrict Phase 1 to what needs no code: `GODEBUG=gctrace=1` on
stderr, `getrusage` figures already in `api_rusage`, and per-step sampled maxima.
Whichever is chosen, state that an instrumented build shares the report's source diff
hash (build tags do not change `git diff`) and differs only in `binaries_sha256`, so
the evidence note must list instrumented binary hashes explicitly and no instrumented
report may count toward baseline or acceptance.

**R5. Name the read-path contributors the snapshots show, and set their freshness rule.**
Every saved peak in the CLI-import step includes two `git` children of the API
(5.0–5.3 MiB plus 2.3–5.1 MiB), because each local `GET /diagnostics` with a baseline
spawns `git status --branch --porcelain=v2` (`diagnostics.go:718-722` →
`context_packet.go:2071`), and the harness's two readers issued 1,574–3,115 reads in
the 3.4–4.2 s step. Each read also re-reads the whole envelope, verifies two SHA-256s,
unmarshals it, base64-decodes the original, and copies and sorts 2,500 rows
(`diagnostics_local_store.go:454-512, 572-590`), charging twice the envelope size per
request (`:475`). The API high-water rose from 26–31 MiB on the 12-step workload to
42–49 MB with this step, so the read loop, not the import, is the API's growth. The
plan's candidate list ("idle shim footprint" or "allocation/copy hotspot on GET reads")
must add the git probe explicitly and must fix the freshness rule for any coalescing
or caching before Phase 2: a response may only report a probe that started no earlier
than the request arrived, and a decoded-envelope cache may be keyed only on
(run ID, envelope sha256) with the latest pointer's sha still read per request. Add a
required regression test: a HEAD move between two consecutive GETs yields `stale` on
the second. The benchmark checks row count and status 200 only (`rss_bench.py:623-625`),
so it would not catch a stale-but-fast answer.

**R6. State what a bounded cache does to admission.**
Phase 2 requires "bounded admission" but a per-workspace decoded envelope held across
requests is memory the ledger no longer charges. The plan must say whether such
retention is allowed and, if so, that it is bounded (one envelope per workspace, ≤ 4 MiB
serialized) and reported in `/api/health`, so "bounded admission" is not quietly weakened.

## Recommended simplifications (not blocking)

- Phase 1 item 3: drop Pi-path latency and CPU measurement; the change is a Go read
  path, and `scripts/e2e/pi-adapter.sh` with nine tools already covers Pi behavior.
  Measure latency and CPU on the two peak steps only.
- Phase 1 item 2: profile the shims only if the chosen candidate is shim-related; they
  are idle during the peak step and their 4–16 MiB variance is OS residency of pages
  touched during the 16 MiB expansion step.
- Phase 1 attribution: use `processes_at_peak` (with `ppid`) rather than
  `per_process_sampled_max_kib_by_command`, which the report itself marks
  "unsynchronized … never summed", and note that `ps` prints `(name)` for exiting
  processes, so `(xmustard-core)` and `xmustard-core` are one binary under two owners.
- Acceptance: the PostgreSQL control could not run in the recheck (no instance). Say
  explicitly that fake-connection tests satisfy it when no instance exists, or the
  acceptance list is unfulfillable on this host.

## What the plan preserves correctly

- Full workload, fixture, sampler, interval, threshold and required roots unchanged.
- Nine MCP tools and schemas (`xmustard-mcp/main.go:77` `tools()`), freshness labels,
  CLI imports, concurrent reads, cancellation, local no-Postgres path.
- No Docker, no daemon, no UI; runtime tuning excluded from product defaults; the
  50–100 MB figure kept as a design target and reported unproven, consistent with
  `docs/VISION.md:26-28` and `AGENTS.md:24`.
- Human final merge authority; a five-run pass is scoped to one host and workload.

## Non-claims

I did not rerun the gate, the e2e suites or `make check-backend`. I did not verify the
111.5 MB run, which has no saved report. GOGC/GOMEMLIMIT values for the six experiments
are taken from their file names and the requester's statement, not from the reports.
