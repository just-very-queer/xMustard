# Budget gate v2 report (2026-09-27T20:32:58Z)

**Verdict: PASS** (gate: ps-RSS of the xMustard-owned tree <= 95.4 MiB, 100,000,000 bytes). Parity-scale claim: **not established**.

Missing for the parity-scale claim:
- agents-1: not run in this invocation
- agents-2: not run in this invocation
- agents-4: not run in this invocation
- reindex-during-queries: not run in this invocation
- snapshot-swap-under-load: not run in this invocation
- captures-during-index: not run in this invocation
- two-hot-repos: not run in this invocation
- four-agents-four-worktrees: not run in this invocation
- watcher-on: not run in this invocation
- grammars: 15 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `` · core `71f942aeae59` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [4.73, 4.99, 4.35] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| v1-workload | PASS | 62.0 (65.0) | 49.9 | 14.3 / 21.9 | 0 | two concurrent index clients | stdio-shim |
| agents-2-relay | PASS | 66.0 (69.2) | 49.9 | 25.8 / 26.5 | 0 | 2 agent(s) querying | stdio-relay |

### v1-workload

frozen v1 workload (501 generated files), v1's own code

Samples 235 (median interval 100.1 ms, max 101.4 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 5.4 | 0.0 | 0.0 | 3.0 | 0.0 | 5.4 | – | 4 | 1 |
| go_daemon | 26.5 | 23.6 | 21.2 | 25.2 | 21.8 | 28.8 | – | 1 | 1 |
| mcp_access | 24.4 | 17.7 | 20.4 | 14.9 | 8.9 | 14.2 | – | 2 | 2 |
| rust_core_per_call | 20.3 | 12.4 | 20.3 | 17.0 | 9.8 | 21.0 | – | 13 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 78.0 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 436094 | xmustard-api | go_daemon (owned) | 21.2 | 19.4 | 5.5 | 1.8 |
| 436102 | xmustard-mcp | mcp_access (owned) | 11.1 | 6.3 | 3.3 | 1.8 |
| 436692 | xmustard-core | rust_core_per_call (owned) | 10.4 | 5.0 | 2.1 | 8.2 |
| 436693 | xmustard-core | rust_core_per_call (owned) | 10.0 | 4.7 | 1.8 | 8.3 |
| 436104 | xmustard-mcp | mcp_access (owned) | 9.3 | 4.6 | 1.6 | 1.8 |

v1 cross-check (same run): v1 sampler 61.9 MiB (64.9 MB), v2 owned+external 62.0 MiB, difference 0.07 MiB (tolerance 8.0): within noise. Median sample interval v1 100.1 ms, v2 100.1 ms.

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 196 | 3184 | 0 | 0 | 2.2 | 2.2 | – | – |
| search | 6 | 0 | 330 | 452362 | 213729 | 53434 | 290.0 | 11360.8 | 504 | 21559504 |
| resources/read | 256 | 0 | 34385 | 22559129 | 22370304 | 5592576 | 2.0 | 2.5 | – | – |

### agents-2-relay

2 stdio agents through the native xmustard-relay (launched like the Go shim) to /mcp

Samples 298 (median interval 100.1 ms, max 100.1 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 5.4 | 0.0 | 5.4 | 3.0 | 0.0 | 5.4 | – | 4 | 1 |
| go_daemon | 30.7 | 24.2 | 24.2 | 28.9 | 22.4 | 30.0 | – | 1 | 1 |
| mcp_access | 5.5 | 5.4 | 5.4 | 1.0 | 0.8 | 2.8 | – | 2 | 2 |
| rust_core_per_call | 31.1 | 12.5 | 31.1 | 23.7 | 9.9 | 27.9 | – | 17 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 52.7 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 438450 | xmustard-core | rust_core_per_call (owned) | 25.3 | 21.7 | 15.8 | 9.5 |
| 438272 | xmustard-api | go_daemon (owned) | 24.2 | 22.4 | 8.7 | 1.8 |
| 438459 | xmustard-core | rust_core_per_call (owned) | 5.7 | 2.0 | 0.6 | 5.2 |
| 438566 | git | git_child (owned) | 5.4 | 3.0 | 0.3 | 5.0 |
| 438280 | xmustard-relay | mcp_access (owned) | 2.7 | 0.4 | 0.2 | 2.5 |
| 438282 | xmustard-relay | mcp_access (owned) | 2.7 | 0.4 | 0.2 | 2.5 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3184 | 0 | 0 | 0.7 | 0.7 | – | – |
| tools/list | 2 | 0 | 4 | 17626 | 17506 | 4378 | 1.1 | 1.1 | – | – |
| ground | 2 | 0 | 84 | 6966 | 2903 | 726 | 8162.6 | 8162.6 | – | – |
| remember | 2 | 0 | 321 | 4139 | 1543 | 387 | 5.7 | 5.7 | – | – |
| search | 6 | 0 | 404 | 488112 | 230999 | 57753 | 183.9 | 11507.4 | 1002 | 42359976 |
| explain | 6 | 0 | 428 | 24332 | 10346 | 2588 | 61.4 | 71.7 | – | – |
| impact | 6 | 0 | 376 | 13744 | 5250 | 1316 | 48.0 | 53.4 | – | – |
| recall | 6 | 0 | 586 | 15492 | 5664 | 1418 | 44.4 | 46.2 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 48, 'git': 1, 'helper': 0}, spawns_total=49, bytes_hashed=265111, captures=28, capture_bytes=256705

Features observed: http_mcp=true, watcher="absent", resident_index={"state": "on", "min": 1, "max": 1, "observations": 18}, uncapped_index={"truncated": false, "indexed_files": 501, "eligible_files": 501, "max_files": 10000}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Blocking for this pull request

Workstream WS-18: **not blocking**. CI-suite scenarios, and those of a workstream that opts in with `gate_blocking`, block on their own verdict, and so does a scenario the base could not run. Other scenarios block only on a regression: the base median was within the gate, the head median is over it, and both the median and the max rose by more than 10.0 MiB. A scenario the base ran and the head skipped blocks.

| Scenario | Head verdict | Base verdict | Rule | Head median MiB | Base median MiB | Blocks | Why |
|---|---|---|---|---|---|---|---|
| v1-workload | PASS | PASS | absolute | 62.0 | 61.2 | no | a CI-suite scenario: its own verdict decides |
| agents-2-relay | PASS | PASS | absolute | 66.0 | 66.3 | no | a CI-suite scenario: its own verdict decides |

## Budget ledger

Workstream WS-18 (line 5 MiB on `rust_index_service`, measured on agents-2): **NOT_CHECKABLE** (the line is measured on ['agents-2'], which could not be measured on both sides (not run, or not runnable at the base); the generic checks held).

Workstreams merged into the base since the reference measurement: none (only the declared list: git could not resolve the range).

| Scenario | Line MiB | Tree delta (allowed) | Owned+external delta | External growth | Process delta, basis (allowed) | Other components over | Design bounds over |
|---|---|---|---|---|---|---|---|
| agents-2-relay (ceiling only) | 5 | -0.4 (15.0) | -0.4 | 0 | 0.0, RSS p50 (8.0) | none | rust_core_per_call 9.8/3.0 (base already over: reported) |
| v1-workload (ceiling only) | 5 | 0.6 (15.0) | 0.6 | 0 | 0.0, RSS p50 (8.0) | none | none |

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| v1-workload | git_child | 0.0 | 0.0 | 0.0 | 5.4 | 5.0 **over** |
| v1-workload | go_daemon | 24.0 | 22.2 | 27.0 | 26.5 | 33.0 |
| v1-workload | mcp_access | 17.7 | 8.9 | 0.0 **over** | 24.5 | 0.0 **over** |
| v1-workload | rust_core_per_call | 12.4 | 9.8 | 0.0 **over** | 20.3 | 25.0 |
| agents-2-relay | git_child | 0.0 | 0.0 | 0.0 | 5.4 | 5.0 **over** |
| agents-2-relay | go_daemon | 24.1 | 22.4 | 27.0 | 30.7 | 33.0 |
| agents-2-relay | mcp_access | 5.4 | 0.9 | 0.0 **over** | 5.6 | 0.0 **over** |
| agents-2-relay | rust_core_per_call | 12.5 | 9.8 | 0.0 **over** | 31.1 | 25.0 **over** |

Reconciliation (open): design steady 64 MiB; peak 94 MiB by the §7.2 method, 112 MiB with query peaks overlapping the heavy slot; gate 95.4 MiB. Measured from this run (v1-workload, median of 3 valid run(s)). Already in the measurement (their lines are not projected again): WS-00, WS-01, WS-02, WS-03, WS-04, WS-06, WS-08, WS-09, WS-18. Projected over the design line: go_daemon, mcp_access, rust_core_per_call.

| Process | Design steady | Design peak | Workstream lines | Measured p50 | Projected steady | Overcommit |
|---|---|---|---|---|---|---|
| git_child | 0.0 | 5.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| go_daemon | 27.0 | 33.0 | 27.7 | 24.0 | 46.5 | 19.5 |
| mcp_access | 0.0 | 0.0 | -10.0 | 17.7 | 7.7 | 7.7 |
| rust_core_per_call | 0.0 | 25.0 | -2.0 | 12.4 | 10.4 | 10.4 |
| rust_index_service | 32.0 | 44.0 | 37.0 | 0.0 | 29.0 | -3.0 |

| §7.3 go_daemon item | Allocation | Workstream lines | Workstreams |
|---|---|---|---|
| runtime_base (base) | 14 | -3.3 | WS-03, WS-06, WS-49 |
| sqlite_governance (feature) | 5 | 3 | WS-01 |
| mcp_http_sessions (feature) | 1 | 0 | – |
| governance_caches (feature) | 3 | 10.5 **over** | WS-19, WS-20, WS-27, WS-28, WS-29, WS-31, WS-32, WS-33 |
| evidence_caches (feature) | 2.5 | 6.5 **over** | WS-08, WS-21, WS-34, WS-41, WS-42, WS-47 |
| hooks_events (feature) | 1 | 1.5 **over** | WS-23 |
| run_lineage_runner (feature) | 1 | 2 **over** | WS-53 |
| redaction_toolcompat (feature) | 1 | 0.5 | WS-05 |
| unallocated (none) | – | 7.0 **over** | WS-43, WS-45, WS-51 |

## Not measured

- hardware memory bandwidth
- allocations inside the Rust core and the MCP shim (only process totals are measured)
- byte copies per stage
- short peaks between 100 ms samples (the gate is a SAMPLED peak; the per-process lifetime maximum is reported beside it)
- exact tokens (a bytes/4 estimate is reported; no tokenizer is loaded)
- work inside short-lived children that exit between samples (processes_seen is a lower bound)
