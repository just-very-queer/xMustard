# Budget gate v2 report (2026-09-28T04:25:07Z)

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
- watcher: absent in 2 of 3 parity scenarios (PAR-FRESH-03, watcher on (WS-15))
- static_embeddings: absent in 3 of 3 parity scenarios (PAR-RET-03, static embeddings on (WS-37))
- uncapped_index: absent in 3 of 3 parity scenarios (PAR-RT-09, no file cap at parity scale (WS-14))
- grammars: 15 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `` · core `1f2a29fc57f0` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [1.8, 5.03, 4.88] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| agents-2 | FAIL | 201.3 (211.1) | 197.0 | 167.4 / 16.9 | 0 | 2 agent(s) querying | http |
| snapshot-swap-under-load | FAIL | 225.6 (236.5) | 210.2 | 176.8 / 22.1 | 0 | 2 agent(s) querying + edits | http |
| watcher-on | FAIL | 225.0 (235.9) | 213.1 | 182.1 / 18.3 (+7.5 exited) | 0 | 2 agent(s) querying + edits | http |

### agents-2

2 agents querying the composite fixture

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [201.1, 201.3, 201.4] MiB): a repeat was FAIL.

Samples 565 (median interval 100.1 ms, max 118.9 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 22.6 | 0.0 | 0.0 | 19.8 | 0.0 | 25.2 | – | 1 | 1 |
| go_daemon | 33.3 | 29.6 | 29.5 | 31.6 | 27.8 | 32.8 | – | 1 | 1 |
| rust_core_per_call | 26.3 | 22.8 | 0.0 | 21.7 | 18.3 | 26.4 | – | 2 | 1 |
| rust_index_service | 171.8 | 9.2 | 171.8 | 169.2 | 4.8 | 171.4 | – | 3 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 47.8 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 157255 | xmustard-core | rust_index_service (owned) | 171.8 | 169.2 | 156.6 | 15.2 |
| 157241 | xmustard-api | go_daemon (owned) | 29.5 | 27.8 | 10.8 | 1.8 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.4 | 0.4 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 1.3 | 1.3 | – | – |
| ground | 2 | 0 | 92 | 10998 | 4781 | 1196 | 9284.6 | 9284.6 | – | – |
| remember | 2 | 0 | 375 | 4492 | 1694 | 424 | 5.9 | 5.9 | – | – |
| search | 6 | 0 | 423 | 558755 | 266695 | 66676 | 2807.4 | 4037.4 | 11606 | 75664862 |
| explain | 6 | 0 | 606 | 157412 | 74922 | 18732 | 94.3 | 424.8 | – | – |
| impact | 6 | 0 | 429 | 428877 | 204049 | 51015 | 86.7 | 30446.1 | – | – |
| recall | 6 | 0 | 759 | 16870 | 6237 | 1561 | 27.0 | 31.5 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 5, 'git': 1, 'helper': 0}, spawns_total=6, bytes_hashed=2911410, captures=28, capture_bytes=2727235

Features observed: http_mcp=true, watcher="overflow", uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}, resident_index={"state": "on", "min": 1, "max": 1, "observations": 12}

### snapshot-swap-under-load

2 agents query while edits force the resident index to swap snapshots (snapshot_generation must advance under load)

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [217.1, 225.6, 226.4] MiB): a repeat was FAIL.

Samples 724 (median interval 100.1 ms, max 109.2 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 16.2 | 0.0 | 0.0 | 15.8 | 0.0 | 18.2 | – | 5 | 1 |
| go_daemon | 34.9 | 27.7 | 32.7 | 33.2 | 25.9 | 34.5 | – | 1 | 1 |
| rust_core_per_call | 26.2 | 20.7 | 19.8 | 21.5 | 16.1 | 26.2 | – | 5 | 1 |
| rust_index_service | 173.0 | 17.7 | 173.0 | 170.3 | 12.8 | 172.7 | – | 8 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 48.2 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 168037 | xmustard-core | rust_index_service (owned) | 173.0 | 157.5 | 153.4 | 10.2 |
| 159852 | xmustard-api | go_daemon (owned) | 32.7 | 31.0 | 13.8 | 1.8 |
| 168051 | xmustard-core | rust_core_per_call (owned) | 19.8 | 13.7 | 9.6 | 10.1 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.6 | 0.6 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 2.2 | 2.2 | – | – |
| ground | 2 | 0 | 92 | 11738 | 5105 | 1277 | 218.2 | 218.2 | – | – |
| remember | 2 | 0 | 375 | 4556 | 1694 | 424 | 5.1 | 5.1 | – | – |
| search | 7 | 0 | 493 | 521197 | 248311 | 62082 | 1584.2 | 7158.5 | 20 | 324652 |
| explain | 6 | 0 | 606 | 157578 | 74907 | 18728 | 1114.8 | 5154.4 | – | – |
| impact | 6 | 0 | 429 | 429482 | 204247 | 51064 | 164.9 | 6229.5 | – | – |
| recall | 6 | 0 | 759 | 17066 | 6239 | 1561 | 27.6 | 37.5 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 15, 'git': 1, 'helper': 0}, spawns_total=16, bytes_hashed=2893320, captures=29, capture_bytes=2709145

Features observed: http_mcp=true, watcher="absent", resident_index={"state": "on", "min": 1, "max": 4, "observations": 12}, uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}

### watcher-on

2 agents with the watcher running while files change

Repeats: repeat verdicts ['FAIL', 'FAIL', 'FAIL'] (peaks [219.0, 225.0, 269.9] MiB): a repeat was FAIL.

Samples 933 (median interval 100.1 ms, max 118.5 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 22.5 | 0.0 | 7.5 | 20.0 | 0.0 | 25.1 | – | 7 | 2 |
| go_daemon | 33.1 | 29.2 | 33.0 | 31.4 | 27.5 | 34.0 | – | 1 | 1 |
| rust_core_per_call | 25.5 | 19.3 | 0.0 | 21.0 | 14.1 | 25.5 | – | 6 | 1 |
| rust_index_service | 184.5 | 18.0 | 184.5 | 181.9 | 13.2 | 184.5 | – | 4 | 1 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 47.9 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 182456 | xmustard-core | rust_index_service (owned) | 184.5 | 181.9 | 168.0 | 16.5 |
| 181650 | xmustard-api | go_daemon (owned) | 33.0 | 31.2 | 14.1 | 1.8 |
| 186525 | git | git_child (owned) | 7.5 | – | – | – |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3172 | 0 | 0 | 0.5 | 0.5 | – | – |
| tools/list | 2 | 0 | 4 | 17608 | 17488 | 4372 | 1.5 | 1.5 | – | – |
| ground | 2 | 0 | 92 | 11560 | 5044 | 1262 | 152.4 | 152.4 | – | – |
| remember | 2 | 0 | 375 | 4500 | 1694 | 424 | 7.7 | 7.7 | – | – |
| search | 7 | 0 | 493 | 456960 | 217501 | 54378 | 1049.8 | 24057.5 | 11819 | 76412825 |
| explain | 6 | 0 | 606 | 223086 | 106496 | 26626 | 435.5 | 21079.8 | – | – |
| impact | 6 | 0 | 429 | 428314 | 203751 | 50940 | 173.9 | 8828.1 | – | – |
| recall | 6 | 0 | 759 | 16894 | 6237 | 1561 | 29.7 | 41.5 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 10, 'git': 1, 'helper': 0}, spawns_total=11, bytes_hashed=3776545, captures=29, capture_bytes=3617436

Features observed: http_mcp=true, watcher="absent", resident_index={"state": "on", "min": 1, "max": 3, "observations": 7}, uncapped_index={"truncated": true, "indexed_files": 5901, "eligible_files": 5901, "max_files": 10000}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Blocking for this pull request

Workstream WS-15: **blocking**. CI-suite scenarios, and those of a workstream that opts in with `gate_blocking`, block on their own verdict, and so does a scenario the base could not run. Other scenarios block only on a regression: the base median was within the gate, the head median is over it, and both the median and the max rose by more than 10.0 MiB. A scenario the base ran and the head skipped blocks.

| Scenario | Head verdict | Base verdict | Rule | Head median MiB | Base median MiB | Blocks | Why |
|---|---|---|---|---|---|---|---|
| agents-2 | FAIL | FAIL | regression | 201.3 | 204.6 | no | the base median 204.6 MiB was already over the gate |
| snapshot-swap-under-load | FAIL | INVALID | regression | 225.6 | – | **yes** | the base has no measurement (most repeats invalid): re-run the check |
| watcher-on | FAIL | skipped | new | 225.0 | – | **yes** | the base could not run it (feature absent at this HEAD: watcher (PAR-FRESH-03, watcher on (WS-15))): the pull request introduced it, so its own verdict decides |

## Budget ledger

Workstream WS-15 (line 2 MiB on `rust_index_service`, measured on agents-2): **BELOW_RESOLUTION** (every check held, but the line (2 MiB) is smaller than the 3.0 MiB tolerance, so it is not verified).

Workstreams merged into the base since the reference measurement: none (only the declared list: git could not resolve the range).

| Scenario | Line MiB | Tree delta (allowed) | Owned+external delta | External growth | Process delta, basis (allowed) | Other components over | Design bounds over |
|---|---|---|---|---|---|---|---|
| agents-2 | 2 | -7.7 (12.0) | -7.7 | 0 | -0.1, footprint p50 (5.0) | none | rust_core_per_call 18.3/3.0 (base already over: reported) |

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| agents-2 | git_child | 0.0 | 0.0 | 0.0 | 22.6 | 5.0 **over** |
| agents-2 | go_daemon | 29.7 | 28.0 | 27.0 **over** | 33.7 | 33.0 **over** |
| agents-2 | rust_core_per_call | 22.7 | 18.3 | 0.0 **over** | 25.6 | 25.0 **over** |
| agents-2 | rust_index_service | 9.2 | 4.8 | 32.0 | 171.8 | 44.0 **over** |
| snapshot-swap-under-load | git_child | 0.0 | 0.0 | 0.0 | 18.3 | 5.0 **over** |
| snapshot-swap-under-load | go_daemon | 29.1 | 27.3 | 27.0 **over** | 34.9 | 33.0 **over** |
| snapshot-swap-under-load | rust_core_per_call | 19.3 | 14.1 | 0.0 **over** | 25.4 | 25.0 **over** |
| snapshot-swap-under-load | rust_index_service | 17.7 | 12.8 | 32.0 | 185.7 | 44.0 **over** |
| watcher-on | git_child | 0.0 | 0.0 | 0.0 | 25.3 | 5.0 **over** |
| watcher-on | go_daemon | 29.1 | 27.4 | 27.0 **over** | 35.2 | 33.0 **over** |
| watcher-on | rust_core_per_call | 19.3 | 14.1 | 0.0 **over** | 25.6 | 25.0 **over** |
| watcher-on | rust_index_service | 17.9 | 13.0 | 32.0 | 185.2 | 44.0 **over** |

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
