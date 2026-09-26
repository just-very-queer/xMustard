# Budget gate v2 report (2026-09-26T08:43:04Z)

**Verdict: INVALID** (gate: ps-RSS of the xMustard-owned tree <= 95.4 MiB, 100,000,000 bytes). Parity-scale claim: **not established**.

Missing for the parity-scale claim:
- v1-workload: not run in this invocation
- agents-1: not run in this invocation
- agents-4: not run in this invocation
- reindex-during-queries: not run in this invocation
- snapshot-swap-under-load: gate INVALID
- captures-during-index: not run in this invocation
- two-hot-repos: not run in this invocation
- four-agents-four-worktrees: not run in this invocation
- watcher-on: not run in this invocation
- http_mcp: absent in 2 of 2 parity scenarios (PAR-RT-03, agents over HTTP MCP (WS-13))
- watcher: absent in 2 of 2 parity scenarios (PAR-FRESH-03, watcher on (WS-15))
- static_embeddings: absent in 2 of 2 parity scenarios (PAR-RET-03, static embeddings on (WS-37))
- uncapped_index: absent in 2 of 2 parity scenarios (PAR-RT-09, no file cap at parity scale (WS-14))
- grammars: 5 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `9a6ed2d4c29b` · core `46a625e30c85` · macOS-27.0-arm64-arm-64bit arm64 · 8 CPUs · load [7.33, 7.25, 4.76] · probe `darwin`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| agents-2 | PASS | 94.0 (98.5) | 59.3 | 57.5 / 36.4 | 0 | index baseline parity-composite | stdio-shim |
| snapshot-swap-under-load | INVALID | 104.2 (109.3) | 63.3 | 61.3 / 40.5 | 0 | 2 agent(s) querying + edits | stdio-shim |

### agents-2

2 agents querying the composite fixture

Samples 275 (median interval 109.5 ms, max 111.7 ms). Footprint basis: phys_footprint (task_info TASK_VM_INFO through task_name_for_pid). Split basis: TASK_VM_INFO internal (anonymous) and external (file-backed) resident bytes, every sample; they sum to the RSS ps reports.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 24.2 | 0.0 | 0.0 | 11.0 | 0.0 | 23.7 | 11.0 | 12 | 2 |
| go_daemon | 34.8 | 25.3 | 25.3 | 22.9 | 20.9 | 34.8 | 22.9 | 1 | 1 |
| mcp_access | 28.8 | 23.1 | 23.1 | 17.4 | 10.5 | 14.7 | 9.0 | 2 | 2 |
| rust_core_per_call | 45.5 | 22.9 | 45.5 | 35.7 | 13.8 | 45.5 | 35.7 | 26 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 58.4 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 89443 | xmustard-core | rust_core_per_call (owned) | 45.5 | 35.7 | 35.3 | 10.2 |
| 89390 | xmustard-api | go_daemon (owned) | 25.3 | 14.1 | 13.6 | 11.7 |
| 89392 | xmustard-mcp | mcp_access (owned) | 11.6 | 4.8 | 4.4 | 7.2 |
| 89391 | xmustard-mcp | mcp_access (owned) | 11.6 | 4.7 | 4.2 | 7.3 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3184 | 0 | 0 | 157.8 | 157.8 | – | – |
| tools/list | 2 | 0 | 4 | 17674 | 17554 | 4390 | 1.8 | 1.8 | – | – |
| ground | 2 | 0 | 92 | 6580 | 2648 | 662 | 1426.8 | 1426.8 | – | – |
| remember | 2 | 0 | 375 | 4532 | 1650 | 413 | 4.5 | 4.5 | – | – |
| search | 6 | 0 | 423 | 402052 | 190978 | 47746 | 813.0 | 12758.9 | 11536 | 75547974 |
| explain | 6 | 0 | 606 | 141575 | 67554 | 16892 | 134.5 | 248.4 | – | – |
| impact | 6 | 0 | 429 | 824116 | 395325 | 98833 | 100.1 | 188.2 | – | – |
| recall | 6 | 0 | 759 | 14708 | 5045 | 1265 | 30.6 | 36.1 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 36, 'git': 1, 'helper': 0}, spawns_total=37, bytes_hashed=1938873, captures=28, capture_bytes=1779764

Features observed: http_mcp=false, watcher="absent", resident_index={"state": "on", "min": 1, "max": 1, "observations": 18}, uncapped_index={"truncated": true, "indexed_files": 5866, "eligible_files": 5866, "max_files": 10000}

### snapshot-swap-under-load

2 agents query while edits force the resident index to swap snapshots (snapshot_generation must advance under load)

Invalid because: mandatory workload checks failed: ['the resident index swapped snapshots while agents were querying (snapshot_generation advanced)', 'the last edit is searchable after the swap (freshness)']

Samples 334 (median interval 110.0 ms, max 113.7 ms). Footprint basis: phys_footprint (task_info TASK_VM_INFO through task_name_for_pid). Split basis: TASK_VM_INFO internal (anonymous) and external (file-backed) resident bytes, every sample; they sum to the RSS ps reports.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 18.8 | 0.0 | 0.0 | 10.5 | 0.0 | 22.2 | 10.5 | 35 | 2 |
| go_daemon | 40.0 | 39.2 | 40.0 | 27.0 | 26.6 | 40.0 | 27.5 | 1 | 1 |
| mcp_access | 32.4 | 23.1 | 31.5 | 18.4 | 9.5 | 16.8 | 9.8 | 2 | 2 |
| rust_core_per_call | 49.5 | 23.0 | 32.8 | 35.1 | 12.7 | 45.0 | 35.1 | 54 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 47.0 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 90828 | xmustard-api | go_daemon (owned) | 40.0 | 24.9 | 24.3 | 12.5 |
| 92777 | xmustard-core | rust_core_per_call (owned) | 19.4 | 13.2 | 12.9 | 6.6 |
| 90829 | xmustard-mcp | mcp_access (owned) | 15.9 | 8.9 | 8.5 | 7.4 |
| 90830 | xmustard-mcp | mcp_access (owned) | 15.5 | 8.5 | 8.1 | 7.4 |
| 92872 | xmustard-core | rust_core_per_call (owned) | 13.4 | 7.8 | 7.5 | 6.5 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3184 | 0 | 0 | 2.1 | 2.1 | – | – |
| tools/list | 2 | 0 | 4 | 17674 | 17554 | 4390 | 1.3 | 1.3 | – | – |
| ground | 2 | 0 | 92 | 7062 | 2853 | 714 | 1363.1 | 1363.1 | – | – |
| remember | 2 | 0 | 375 | 4596 | 1650 | 413 | 2.8 | 2.8 | – | – |
| search | 7 | 0 | 492 | 469424 | 222884 | 55724 | 855.9 | 1540.6 | 2 | 37820 |
| explain | 6 | 0 | 606 | 141773 | 67556 | 16892 | 199.2 | 415.8 | – | – |
| impact | 6 | 0 | 429 | 824320 | 395329 | 98834 | 271.1 | 311.1 | – | – |
| recall | 6 | 0 | 759 | 14900 | 5045 | 1264 | 100.8 | 105.2 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 68, 'git': 1, 'helper': 0}, spawns_total=69, bytes_hashed=1996050, captures=29, capture_bytes=1811875

Features observed: http_mcp=false, watcher="absent", resident_index={"state": "on", "min": 2, "max": 2, "observations": 18}, uncapped_index={"truncated": true, "indexed_files": 5866, "eligible_files": 5866, "max_files": 10000}

## Grammars

5 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript.

## Budget ledger

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| agents-2 | git_child | 0.0 | 0.0 | 0.0 | 24.2 | 5.0 **over** |
| agents-2 | go_daemon | 25.3 | 20.9 | 27.0 | 34.8 | 33.0 **over** |
| agents-2 | mcp_access | 23.1 | 10.5 | 0.0 **over** | 28.8 | 0.0 **over** |
| agents-2 | rust_core_per_call | 22.9 | 13.8 | 0.0 **over** | 45.5 | 25.0 **over** |

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
