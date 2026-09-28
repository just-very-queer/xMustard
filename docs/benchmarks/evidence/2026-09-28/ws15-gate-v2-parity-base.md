# Budget gate v2 report (2026-09-28T04:16:54Z)

**Verdict: FAIL** (gate: ps-RSS of the xMustard-owned tree <= 95.4 MiB, 100,000,000 bytes). Parity-scale claim: **not established**.

Missing for the parity-scale claim:
- v1-workload: not run in this invocation
- agents-1: not run in this invocation
- agents-2: gate FAIL
- agents-4: not run in this invocation
- reindex-during-queries: not run in this invocation
- snapshot-swap-under-load: gate INVALID
- captures-during-index: not run in this invocation
- two-hot-repos: not run in this invocation
- four-agents-four-worktrees: not run in this invocation
- watcher-on: skipped (feature absent at this HEAD: watcher (PAR-FRESH-03, watcher on (WS-15)))
- watcher: absent in 2 of 2 parity scenarios (PAR-FRESH-03, watcher on (WS-15))
- static_embeddings: absent in 2 of 2 parity scenarios (PAR-RET-03, static embeddings on (WS-37))
- uncapped_index: absent in 1 of 2 parity scenarios (PAR-RT-09, no file cap at parity scale (WS-14))
- resident_index: absent in 1 of 2 parity scenarios (PAR-RT-01, resident index service with snapshot swap (WS-14))
- grammars: 15 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `` · core `3db0ac2f92dc` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [6.02, 5.68, 3.94] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| agents-2 | FAIL | 204.6 (214.6) | 196.7 | 162.1 / 17.5 (+7.5 exited) | 0 | 2 agent(s) querying | http |
| snapshot-swap-under-load | INVALID | 217.4 (228.0) | 213.0 | 181.4 / 18.8 | 0 | 2 agent(s) querying + edits | http |
| watcher-on | skipped: feature absent at this HEAD: watcher (PAR-FRESH-03, watcher on (WS-15)) | – | – | – | – | – | – |

### agents-2

2 agents querying the composite fixture

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [200.9, 204.6, 209.1] MiB): a repeat was FAIL.

Samples 696 (median interval 100.2 ms, max 233.0 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 20.9 | 0.0 | 7.5 | 17.9 | 0.0 | 22.9 | – | 6 | 1 |
| go_daemon | 35.5 | 29.4 | 34.8 | 33.8 | 27.7 | 35.2 | – | 1 | 1 |
| rust_core_per_call | 26.7 | 22.2 | 0.0 | 22.6 | 18.5 | 26.7 | – | 2 | 1 |
| rust_index_service | 171.7 | 11.2 | 162.3 | 169.1 | 4.9 | 171.4 | – | 2 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 45.9 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 108537 | xmustard-core | rust_index_service (owned) | 162.3 | 159.7 | 146.5 | 15.8 |
| 108472 | xmustard-api | go_daemon (owned) | 34.8 | 33.1 | 15.6 | 1.8 |
| 121156 | git | git_child (owned) | 7.5 | – | – | – |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.8 | 0.8 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 7.4 | 7.4 | – | – |
| ground | 2 | 0 | 92 | 9502 | 4075 | 1020 | 16983.8 | 16983.8 | – | – |
| remember | 2 | 0 | 375 | 4492 | 1694 | 424 | 19.6 | 19.6 | – | – |
| search | 6 | 0 | 423 | 277422 | 131610 | 32904 | 947.9 | 1487.4 | 0 | 0 |
| explain | 6 | 0 | 606 | 300318 | 143781 | 35948 | 675.2 | 812.0 | – | – |
| impact | 6 | 0 | 429 | 427153 | 203257 | 50817 | 269.9 | 30247.1 | – | – |
| recall | 6 | 0 | 759 | 16874 | 6239 | 1561 | 31.0 | 40.8 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 4, 'git': 1, 'helper': 0}, spawns_total=5, bytes_hashed=4597558, captures=28, capture_bytes=4413383

Features observed: http_mcp=true, watcher="absent", uncapped_index={"truncated": false, "indexed_files": 5904, "eligible_files": 5904, "max_files": 10000}

### snapshot-swap-under-load

2 agents query while edits force the resident index to swap snapshots (snapshot_generation must advance under load)

Invalid because: mandatory workload checks failed: ['the resident index swapped snapshots while agents were querying (snapshot_generation advanced)']; repeat verdicts ['INVALID', 'INVALID', 'INVALID'] (peaks [215.7, 217.4, 305.5] MiB): a repeat was INVALID

Samples 890 (median interval 100.1 ms, max 101.7 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 25.3 | 0.0 | 0.0 | 22.8 | 0.0 | 27.7 | – | 2 | 1 |
| go_daemon | 33.0 | 28.7 | 32.7 | 31.4 | 26.9 | 33.8 | – | 1 | 1 |
| rust_core_per_call | 26.4 | 19.7 | 0.0 | 21.2 | 15.5 | 26.4 | – | 4 | 1 |
| rust_index_service | 184.7 | 21.5 | 184.7 | 182.1 | 16.3 | 184.8 | – | 4 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 47.7 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 150743 | xmustard-core | rust_index_service (owned) | 184.7 | 182.1 | 167.8 | 17.0 |
| 149862 | xmustard-api | go_daemon (owned) | 32.7 | 30.9 | 13.7 | 1.8 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.4 | 0.4 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 1.7 | 1.7 | – | – |
| ground | 2 | 0 | 92 | 9862 | 4219 | 1056 | 307.8 | 307.8 | – | – |
| remember | 2 | 0 | 375 | 4556 | 1694 | 424 | 3.2 | 3.2 | – | – |
| search | 7 | 0 | 493 | 456950 | 217406 | 54354 | 990.2 | 23051.2 | 11819 | 76412825 |
| explain | 6 | 0 | 606 | 222982 | 106368 | 26594 | 394.8 | 17027.8 | – | – |
| impact | 6 | 0 | 429 | 428462 | 203747 | 50938 | 216.6 | 8472.5 | – | – |
| recall | 6 | 0 | 759 | 17066 | 6239 | 1561 | 23.6 | 28.2 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 8, 'git': 1, 'helper': 0}, spawns_total=9, bytes_hashed=3817183, captures=29, capture_bytes=3616263

Features observed: http_mcp=true, watcher="absent", uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}, resident_index={"state": "on", "min": 3, "max": 3, "observations": 5}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Budget ledger

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| agents-2 | git_child | 0.0 | 0.0 | 0.0 | 25.3 | 5.0 **over** |
| agents-2 | go_daemon | 30.2 | 28.4 | 27.0 **over** | 35.5 | 33.0 **over** |
| agents-2 | rust_core_per_call | 23.4 | 18.5 | 0.0 **over** | 26.6 | 25.0 **over** |
| agents-2 | rust_index_service | 10.7 | 4.9 | 32.0 | 171.8 | 44.0 **over** |

Reconciliation (open): design steady 64 MiB; peak 94 MiB by the §7.2 method, 112 MiB with query peaks overlapping the heavy slot; gate 95.4 MiB. Measured from reference_measurement (v1-workload, linux-x86_64 (Ubuntu 26.04, kernel 7.0, procps-ng 4.0.4, 6 cores, 22 GB; shared with a Windows VM and other builds, load average 8-11 during the runs), 2026-09-25). Already in the measurement (their lines are not projected again): WS-00, WS-01, WS-02, WS-03, WS-04, WS-06, WS-08, WS-09. Projected over the design line: go_daemon, mcp_access, rust_core_per_call, rust_index_service.

| Process | Design steady | Design peak | Workstream lines | Measured p50 | Projected steady | Overcommit |
|---|---|---|---|---|---|---|
| git_child | 0.0 | 5.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| go_daemon | 27.0 | 33.0 | 27.7 | 21.4 | 43.9 | 16.9 |
| mcp_access | 0.0 | 0.0 | -10.0 | 16.6 | 6.6 | 6.6 |
| rust_core_per_call | 0.0 | 25.0 | -2.0 | 9.7 | 7.7 | 7.7 |
| rust_index_service | 32.0 | 44.0 | 37.0 | 0.0 | 34.0 | 2.0 |

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
