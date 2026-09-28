# Budget gate v2 report (2026-09-28T04:41:41Z)

**Verdict: FAIL** (gate: ps-RSS of the xMustard-owned tree <= 95.4 MiB, 100,000,000 bytes). Parity-scale claim: **not established**.

Missing for the parity-scale claim:
- v1-workload: not run in this invocation
- agents-1: not run in this invocation
- agents-2: gate FAIL
- agents-4: not run in this invocation
- reindex-during-queries: not run in this invocation
- snapshot-swap-under-load: gate FAIL
- captures-during-index: not run in this invocation
- two-hot-repos: not run in this invocation
- four-agents-four-worktrees: not run in this invocation
- watcher-on: gate FAIL
- static_embeddings: absent in 3 of 3 parity scenarios (PAR-RET-03, static embeddings on (WS-37))
- uncapped_index: absent in 3 of 3 parity scenarios (PAR-RT-09, no file cap at parity scale (WS-14))
- grammars: 15 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `` · core `1f2a29fc57f0` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [4.6, 3.72, 3.91] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| agents-2 | FAIL | 201.3 (211.1) | 197.0 | 167.4 / 16.7 | 0 | 2 agent(s) querying | http |
| snapshot-swap-under-load | FAIL | 217.9 (228.5) | 213.5 | 182.3 / 18.3 | 0 | 2 agent(s) querying + edits | http |
| watcher-on | FAIL | 217.9 (228.5) | 213.7 | 182.6 / 18.1 | 0 | 2 agent(s) querying + edits | http |

### agents-2

2 agents querying the composite fixture

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [201.3, 201.3, 208.6] MiB): a repeat was FAIL.

Samples 594 (median interval 100.1 ms, max 109.5 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 20.6 | 0.0 | 0.0 | 18.1 | 0.0 | 22.7 | – | 4 | 1 |
| go_daemon | 33.2 | 30.1 | 29.6 | 31.5 | 28.3 | 32.8 | – | 1 | 1 |
| rust_core_per_call | 25.1 | 22.4 | 0.0 | 21.1 | 18.3 | 25.1 | – | 2 | 1 |
| rust_index_service | 171.7 | 9.3 | 171.7 | 169.1 | 4.8 | 24.8 | – | 4 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 45.8 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 213317 | xmustard-core | rust_index_service (owned) | 171.7 | 169.1 | 156.7 | 15.0 |
| 213302 | xmustard-api | go_daemon (owned) | 29.6 | 27.9 | 10.7 | 1.8 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.6 | 0.6 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 1.1 | 1.1 | – | – |
| ground | 2 | 0 | 92 | 10998 | 4781 | 1196 | 9321.2 | 9321.2 | – | – |
| remember | 2 | 0 | 375 | 4492 | 1694 | 424 | 5.1 | 5.1 | – | – |
| search | 6 | 0 | 423 | 482051 | 229941 | 57487 | 1522.2 | 30767.5 | 5803 | 37832431 |
| explain | 6 | 0 | 606 | 157420 | 74926 | 18733 | 397.2 | 7256.8 | – | – |
| impact | 6 | 0 | 429 | 428888 | 204054 | 51017 | 62.3 | 30468.1 | – | – |
| recall | 6 | 0 | 759 | 16874 | 6239 | 1561 | 27.3 | 60.3 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 6, 'git': 1, 'helper': 0}, spawns_total=7, bytes_hashed=2874666, captures=28, capture_bytes=2690491

Features observed: http_mcp=true, watcher="overflow", uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}, resident_index={"state": "on", "min": 1, "max": 1, "observations": 11}

### snapshot-swap-under-load

2 agents query while edits force the resident index to swap snapshots (snapshot_generation must advance under load)

Repeats: repeat verdicts ['FAIL', 'INVALID', 'INVALID'] (peaks [217.9, 218.1, 258.6] MiB): a repeat was FAIL.

Samples 916 (median interval 100.1 ms, max 102.5 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 25.7 | 0.0 | 0.0 | 23.3 | 0.0 | 27.7 | – | 2 | 1 |
| go_daemon | 34.2 | 28.9 | 32.6 | 32.5 | 27.2 | 33.6 | – | 1 | 1 |
| rust_core_per_call | 26.3 | 19.0 | 0.0 | 21.5 | 14.9 | 26.3 | – | 4 | 1 |
| rust_index_service | 185.3 | 18.5 | 185.3 | 182.6 | 13.7 | 185.3 | – | 4 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 47.9 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 238855 | xmustard-core | rust_index_service (owned) | 185.3 | 182.6 | 168.7 | 16.6 |
| 238051 | xmustard-api | go_daemon (owned) | 32.6 | 30.9 | 13.6 | 1.8 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.7 | 0.7 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 1.1 | 1.1 | – | – |
| ground | 2 | 0 | 92 | 11616 | 5044 | 1262 | 266.4 | 266.4 | – | – |
| remember | 2 | 0 | 375 | 4556 | 1694 | 424 | 4.6 | 4.6 | – | – |
| search | 7 | 0 | 493 | 586201 | 279620 | 69906 | 2611.5 | 23450.5 | 11819 | 76412825 |
| explain | 6 | 0 | 606 | 157594 | 74916 | 18731 | 124.5 | 17428.3 | – | – |
| impact | 6 | 0 | 429 | 429267 | 204145 | 51038 | 56.2 | 8502.4 | – | – |
| recall | 6 | 0 | 759 | 17064 | 6238 | 1560 | 22.4 | 27.3 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 10, 'git': 1, 'helper': 0}, spawns_total=11, bytes_hashed=2899518, captures=29, capture_bytes=2740409

Features observed: http_mcp=true, watcher="ok", resident_index={"state": "on", "min": 1, "max": 3, "observations": 13}, uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}

### watcher-on

2 agents with the watcher running while files change

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [215.9, 217.9, 223.3] MiB): a repeat was FAIL.

Samples 902 (median interval 100.1 ms, max 102.7 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 22.5 | 0.0 | 0.0 | 20.0 | 0.0 | 25.0 | – | 5 | 2 |
| go_daemon | 33.0 | 28.4 | 33.0 | 31.5 | 26.6 | 33.6 | – | 1 | 1 |
| rust_core_per_call | 25.1 | 18.9 | 0.0 | 20.6 | 15.0 | 25.1 | – | 6 | 1 |
| rust_index_service | 185.0 | 18.0 | 184.9 | 182.3 | 13.2 | 185.0 | – | 4 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 48.0 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 248333 | xmustard-core | rust_index_service (owned) | 184.9 | 182.3 | 168.6 | 16.3 |
| 247531 | xmustard-api | go_daemon (owned) | 33.0 | 31.5 | 14.0 | 1.8 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 1.0 | 1.0 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 0.8 | 0.8 | – | – |
| ground | 2 | 0 | 92 | 11676 | 5102 | 1276 | 178.8 | 178.8 | – | – |
| remember | 2 | 0 | 375 | 4500 | 1694 | 424 | 6.3 | 6.3 | – | – |
| search | 7 | 0 | 493 | 444302 | 211567 | 52894 | 1515.3 | 23077.1 | 11819 | 76412825 |
| explain | 6 | 0 | 606 | 223100 | 106503 | 26627 | 455.4 | 17419.5 | – | – |
| impact | 6 | 0 | 429 | 428118 | 203656 | 50917 | 195.8 | 8513.8 | – | – |
| recall | 6 | 0 | 759 | 16898 | 6239 | 1561 | 11.5 | 26.9 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 10, 'git': 1, 'helper': 0}, spawns_total=11, bytes_hashed=3795752, captures=29, capture_bytes=3611577

Features observed: http_mcp=true, watcher="ok", resident_index={"state": "on", "min": 1, "max": 3, "observations": 7}, uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Blocking for this pull request

Workstream WS-15: **blocking**. CI-suite scenarios, and those of a workstream that opts in with `gate_blocking`, block on their own verdict, and so does a scenario the base could not run. Other scenarios block only on a regression: the base median was within the gate, the head median is over it, and both the median and the max rose by more than 10.0 MiB. A scenario the base ran and the head skipped blocks.

| Scenario | Head verdict | Base verdict | Rule | Head median MiB | Base median MiB | Blocks | Why |
|---|---|---|---|---|---|---|---|
| agents-2 | FAIL | FAIL | regression | 201.3 | 204.6 | no | the base median 204.6 MiB was already over the gate |
| snapshot-swap-under-load | FAIL | INVALID | regression | – | – | **yes** | the head has no measurement (1 of 3 repeats valid) |
| watcher-on | FAIL | skipped | new | 217.9 | – | **yes** | the base could not run it (feature absent at this HEAD: watcher (PAR-FRESH-03, watcher on (WS-15))): the pull request introduced it, so its own verdict decides |

## Budget ledger

Workstream WS-15 (line 2 MiB on `rust_index_service`, measured on agents-2): **BELOW_RESOLUTION** (every check held, but the line (2 MiB) is smaller than the 3.0 MiB tolerance, so it is not verified).

Workstreams merged into the base since the reference measurement: none (only the declared list: git could not resolve the range).

| Scenario | Line MiB | Tree delta (allowed) | Owned+external delta | External growth | Process delta, basis (allowed) | Other components over | Design bounds over |
|---|---|---|---|---|---|---|---|
| agents-2 | 2 | -3.3 (12.0) | -3.3 | 0 | -0.1, footprint p50 (5.0) | none | rust_core_per_call 18.3/3.0 (base already over: reported) |

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| agents-2 | git_child | 0.0 | 0.0 | 0.0 | 20.6 | 5.0 **over** |
| agents-2 | go_daemon | 30.0 | 28.2 | 27.0 **over** | 33.2 | 33.0 **over** |
| agents-2 | rust_core_per_call | 22.4 | 18.3 | 0.0 **over** | 25.4 | 25.0 **over** |
| agents-2 | rust_index_service | 9.3 | 4.8 | 32.0 | 171.7 | 44.0 **over** |
| watcher-on | git_child | 0.0 | 0.0 | 0.0 | 22.5 | 5.0 **over** |
| watcher-on | go_daemon | 28.5 | 26.7 | 27.0 | 33.0 | 33.0 |
| watcher-on | rust_core_per_call | 18.9 | 15.0 | 0.0 **over** | 25.4 | 25.0 **over** |
| watcher-on | rust_index_service | 18.0 | 13.2 | 32.0 | 185.1 | 44.0 **over** |

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
