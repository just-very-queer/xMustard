# Plan: improve fixed-workload process-tree RSS headroom — 2026-09-24

Status: proposed; no implementation or benchmark changes authorized by this plan. The
50–100 MB process-tree target remains unproven. The fixed gate is a sampled peak
of at most 100,000,000 bytes on the existing workload.

## Evidence and scope

The unchanged `scripts/bench/rss_bench.py` fixture, sampler, and threshold are
the acceptance baseline. Its 100 ms `ps` samples sum all xMustard-owned roots in
one snapshot (API, two MCP shims and descendants, plus transient CLI and its
children while alive). Sampling is a lower bound, not a universal ceiling.

The post-repair no-tuning gate passed 3/6 and failed 3/6, at 104.3, 110.6, and
111.5 MB. Each saved failure peaked during CLI diagnostics imports concurrent
with API reads. At 104.3 MB, the peak snapshot showed API 44.2 MiB, CLI 19.3
MiB, core 13.9 MiB, and MCP shims 14.5 and 7.6 MiB; at 110.6 MB it showed API
42.3 MiB, CLI 16.4 MiB, core 15.6 MiB, and shims 15.7 and 15.5 MiB. Each such
snapshot also showed two API-owned `git` children (about 5.0–5.3 and 2.3–5.1
MiB). The concurrent readers made 1,574–3,115 GETs during the 3.4–4.2 s step.
Each GET with a baseline starts `git status --branch --porcelain=v2` and reads
the whole local envelope, verifies its checksums, unmarshals it, base64-decodes
the raw input, then copies and sorts 2,500 rows. The API sampled maximum rose
from 26–31 MiB on the earlier 12-step workload to 42–49 MiB with this read-heavy
step. These observations identify read-path work to profile, but do not prove
which operation dominates. Use `processes_at_peak` and parent IDs for
attribution; per-process maxima are unsynchronized and must not be summed. A
process shown as `(xmustard-core)` is the same binary exiting, not an additional
workload process.

Environment experiments are diagnostic observations, not robust proof or
product defaults: `GOMEMLIMIT=32MiB` produced one failure at 113.4 MB;
`GOGC=20` passed three runs at 94.2–97.4 MB; `GOGC=10` passed two at 88.2 and
97.4 MB. They do not justify shipping runtime tuning. The input plan's 16 MiB
admission ledger and release-to-zero pool prove bounded admitted bytes, not an
RSS bound. The tuning reports do not record the relevant Go environment
values; their settings are known from experiment names and reported facts only.

Keep the nine MCP tools and their schemas, freshness behavior, CLI imports,
concurrent reads, and all existing correctness checks. Do not change benchmark
fixture, sampler, sampling interval, threshold, or required roots; do not add
Docker or a daemon. The target must not be reported as met until the fixed gate
passes repeatedly.

## Phase 1 — baseline and profiling

1. Preserve these fixed inputs for every gate run: `scripts/bench/rss_bench.py`
   SHA-256 `7c7254e0…faa9f`, `scripts/e2e/harness.py` `97314722…`, and
   `scripts/bench/rss.sh` `7d8d9e54…`. Do not edit benchmark source, fixture,
   sampler, roots, interval, threshold, or workload. The gate report does not
   attest to inherited Go settings, so create a sidecar for every report with
   the report SHA-256, exact launch command, output of
   `env | cut -d= -f1 | grep -E '^(GOGC|GOMEMLIMIT|GODEBUG|GOFLAGS|GOMAXPROCS|XMUSTARD_)'`
   (must be empty; names only, never print token values), plus `go env GOFLAGS GODEBUG`
   (both must be empty), and source
   grep output showing no `SetGCPercent`, `SetMemoryLimit`, or `//go:debug` in
   `api-go` (`rg -n 'SetGCPercent|SetMemoryLimit|//go:debug' api-go`, no
   matches expected). Launch each gate run with
   `env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS -u GOMAXPROCS -u XMUSTARD_CORE_BIN bash scripts/bench/rss.sh --report <report.json>`
   (substitute and record the actual output path) and store the command and
   empty-environment attestation. Verify each report has
   `core_bin_prebuilt=false` and its core hash matches a fresh in-worktree
   `cargo build --release`. Before the series, build the API and MCP shim under
   the same sanitized environment, record their hashes and `go version -m`
   output, require no `-tags` build setting, and verify each gate report's
   binary hashes match those recorded builds. Apply this to baseline and acceptance
   runs. This out-of-band sidecar leaves the frozen report format unchanged.
2. Before starting, register exactly five numbered baseline attempts `A1`–`A5`
   and their output paths. Run them back-to-back in one host session, with the
   same source/binaries and sanitized environment. Retain every report and
   sidecar, including failures, invalid samples, and harness exceptions. This
   characterization series has no pass/fail outcome; record its full counts,
   and never rerun merely because a baseline attempt failed. No sixth attempt
   or run-until-pass; a later series requires a new series number and written
   reason. Immediately before and after each attempt, store `vm_stat` and
   `sysctl vm.swapusage` output beside its report. Include host identity and
   source/binary hashes. This baseline characterizes current variance; it does
   not replace the historical 3/6 result.
3. Add only a minimal `profile` build-tag-gated Go profiling hook for the API
   read path: a dedicated, opt-in, loopback-only `net/http/pprof` listener,
   compiled only with `-tags profile`. Default binaries must contain no pprof
   import or handler. Profile API CPU, heap/allocation, and GC behavior during
   the two-concurrent-index and CLI-import/concurrent-read steps; capture idle
   and active RSS. Do not profile MCP initially; add a similarly build-tag-only
   hook there only if API profiles leave a specific shim hypothesis unresolved.
   Instrumented runs are diagnostic only, never gate attempts. Build tags do
   not change the source diff hash, so list instrumented binary hashes
   separately and label them. Keep profiling overhead runs separate from gate
   runs.
4. Measure API read latency and CPU only on those two peak steps. Profile and
   inspect the per-request Git status subprocess and local-envelope reread,
   checksum verification, decode/base64 work, row copy, and sort. Use the peak
   snapshot with parent IDs, not unsynchronized per-process maxima, for tree
   attribution. Do not infer shim cost from RSS alone; its idle resident
   variation may reflect paging. Pi latency profiling is out of scope; retain
   Pi correctness coverage in acceptance.
5. End Phase 1 with an evidence note naming the supported code path, expected
   RSS mechanism, and measured CPU/latency/client impact. If evidence does not
   support a small general-purpose change, stop with the target unproven.

Before Phase 2 edits, preserve this repaired diagnostics source as comparison
revision A in a separate, detached temporary worktree. Reproduce the exact
tracked diff and untracked source manifest there. The benchmark files are also
untracked at base `cd13e2b`: copy `scripts/bench/` and all of `scripts/e2e/`
needed by `harness.py` into A, and verify the three pinned script hashes above.
Do not use the main checkout as A: it does not contain database-free diagnostics.
Keep A source read-only, verify its tracked-diff and untracked-source hashes
match the repaired isolated tree before editing B, and rebuild and record A and
B binary hashes. Reject the comparison if A cannot be reproduced exactly. This
creates no commit or merge.

## Phase 2 — implement only a profile-supported fix

Test the smallest profile-supported general-purpose API read-path candidate.
Prefer removing demonstrated allocations/copies without caching for the first
candidate. Explicitly evaluate Git subprocess and envelope decode/sort costs.
Any Git-probe coalescing may only reuse a probe started at or after the request
arrived; a HEAD move between two consecutive GETs must produce `stale` on the
second GET, covered through the real GET handler path (not just the status
helper). If a later candidate uses a decoded
envelope cache, key it by `(run ID, envelope SHA-256)` and read the latest
pointer on every request. Bound it to one decoded envelope per workspace and
at most 4 MiB serialized per entry, and include retained bytes in `/api/health`
accounting. The initial no-cache candidate is preferred and must not introduce
retained memory outside request admission.

Any change must benefit supported clients generally, preserve all nine MCP
tools and schemas, freshness, complete reads, CLI imports, and concurrent
reads. No client-specific weakening or hidden benchmark roots.

Add focused correctness, freshness, concurrency, and cancellation regression
coverage for the changed path. Compare instrumented CPU, allocation, and API
latency on the two peak steps; state measured tradeoffs. Check behavior across
MCP, HTTP, CLI, local no-Postgres diagnostics, and Pi. Reject any RSS gain that
depends on dropped work, stale/partial reads, lower concurrency, changed
benchmark conditions, unaccounted retained cache bytes, or unacceptable
CPU/latency cost. Keep runtime tuning out of product defaults unless a separate
representative evaluation supports an explicit decision.

## Acceptance evidence

After selecting the candidate, preregister one same-session interleaved
comparison sequence before its first gate run: `A1, B1, A2, B2, A3, B3, A4,
B4, A5, B5`, with A the unchanged baseline and B the candidate. This is
exactly five attempts per revision, following the five Phase 1 baseline runs
(fifteen full gate runs in total, plus separate instrumented runs); do not add
retries or select runs. Store the Phase 1 reports under the same numbered
series/sidecar convention. Before the first A/B attempt, preregister expected
`head`, `source_diff_sha256`, `untracked_source_sha256`, `binaries_sha256`, and
`script_sha256` for both A and B. Run each A attempt through A's own
`scripts/bench/rss.sh` and each B attempt through B's; a report whose provenance
does not match its registered revision is invalid. Keep the
same host session, sanitized gate environment, fixture, workload, and unchanged
benchmark files. Retain every report, including failures, invalid reports, and
harness exceptions, with report SHA-256 and sidecar attestations. Record which
verified A or B source tree and binaries each run used. Capture
`vm_stat` and `sysctl vm.swapusage` immediately before and after every attempt,
and store output alongside its report. Any failed or invalid B attempt fails
the candidate series. A failing but valid A attempt is retained as baseline
evidence, not discarded or treated as a reason to rerun; an invalid A attempt
voids only the A/B attribution claim, not independently valid B gate evidence.
A new comparison requires a new series number and written reason.
The candidate supports a fixed-workload result only if all five B reports are
valid and pass at ≤100,000,000 bytes; report all A results and host conditions.
A five-run pass is not a universal RSS ceiling.

Also provide current-build evidence for:

- `make check-backend`;
- real no-Postgres HTTP/MCP flow, with exactly nine MCP tools and concurrent
  reads during CLI imports, including persistence/restart checks from the
  diagnostics plan;
- Pi flow with all nine tools, both local no-Postgres diagnostics and the
  existing PostgreSQL control. Run the native PostgreSQL control if an instance
  is available; otherwise use existing fake-connection PostgreSQL tests and
  state that no live native control ran;
- unchanged diagnostics freshness, admission, cancellation, and error behavior
  relevant to the touched code;
- before/after CPU, latency, allocation, and per-process/tree sampled RSS, with
  profiler overhead identified separately.

Until that evidence exists, record the target as unproven.

No UI, daemon, Docker, or MCP tool expansion is in scope. The plan and its
acceptance evidence do not grant merge authority; the human remains final merge
authority.
