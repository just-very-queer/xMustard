# Budget gate v2 report (2026-09-26T08:42:36Z)

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
- grammars: 5 of 17 parity languages parse with a grammar (PAR-SYM-05, WS-16)

Source `9a6ed2d4c29b` · core `46a625e30c85` · macOS-27.0-arm64-arm-64bit arm64 · 8 CPUs · load [9.47, 7.61, 4.8] · probe `darwin`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| v1-workload | PASS | 74.1 (77.7) | 45.8 | 40.2 / 34.1 | 0 | two concurrent index clients | stdio-shim |
| agents-2-relay | skipped: feature absent at this HEAD: relay (PAR-RT-03, native stdio-to-HTTP relay xmustard-relay (WS-13)), http_mcp (PAR-RT-03, agents over HTTP MCP (WS-13)) | – | – | – | – | – | – |

### v1-workload

frozen v1 workload (501 generated files), v1's own code

Samples 201 (median interval 109.5 ms, max 111.4 ms). Footprint basis: phys_footprint (task_info TASK_VM_INFO through task_name_for_pid). Split basis: TASK_VM_INFO internal (anonymous) and external (file-backed) resident bytes, every sample; they sum to the RSS ps reports.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 4.6 | 0.0 | 0.0 | – | – | – | – | 8 | 2 |
| go_daemon | 31.8 | 30.2 | 30.7 | 20.3 | 19.9 | 31.8 | 20.3 | 1 | 1 |
| mcp_access | 26.5 | 20.4 | 21.6 | 15.5 | 8.9 | 15.9 | 10.1 | 2 | 2 |
| rust_core_per_call | 25.5 | 13.2 | 21.8 | 15.0 | 6.4 | 22.7 | 15.0 | 13 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 67.8 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 86972 | xmustard-api | go_daemon (owned) | 30.7 | 20.1 | 18.8 | 11.9 |
| 88063 | xmustard-core | rust_core_per_call (owned) | 21.8 | 14.6 | 14.3 | 7.6 |
| 86973 | xmustard-mcp | mcp_access (owned) | 11.5 | 6.2 | 4.3 | 7.2 |
| 86974 | xmustard-mcp | mcp_access (owned) | 10.0 | 4.9 | 2.8 | 7.3 |

v1 cross-check (same run): v1 sampler 75.2 MiB (78.9 MB), v2 owned+external 74.1 MiB, difference -1.15 MiB (tolerance 8.0): within noise. Median sample interval v1 110.0 ms, v2 109.5 ms.

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 196 | 3184 | 0 | 0 | 474.6 | 474.6 | – | – |
| search | 6 | 0 | 330 | 252426 | 117486 | 29375 | 383.7 | 9611.4 | 504 | 21559504 |
| resources/read | 256 | 0 | 34385 | 22558361 | 22370304 | 5592576 | 1.1 | 1.5 | – | – |

## Grammars

5 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript.

## Budget ledger

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| v1-workload | git_child | 0.0 | – | 0.0 | 4.6 | 5.0 |
| v1-workload | go_daemon | 30.2 | 19.9 | 27.0 | 31.8 | 33.0 |
| v1-workload | mcp_access | 20.4 | 8.9 | 0.0 **over** | 26.5 | 0.0 **over** |
| v1-workload | rust_core_per_call | 13.2 | 6.4 | 0.0 **over** | 25.5 | 25.0 **over** |

Reconciliation (open): design steady 64 MiB; peak 94 MiB by the §7.2 method, 112 MiB with query peaks overlapping the heavy slot; gate 95.4 MiB. Measured from this run (v1-workload, median of 1 valid run(s)). Already in the measurement (their lines are not projected again): WS-00, WS-01, WS-02, WS-03, WS-04, WS-05, WS-06, WS-07, WS-08, WS-09, WS-10, WS-11, WS-24, WS-25, WS-54. Projected over the design line: go_daemon, mcp_access, rust_core_per_call, rust_index_service.

| Process | Design steady | Design peak | Workstream lines | Measured p50 | Projected steady | Overcommit |
|---|---|---|---|---|---|---|
| git_child | 0.0 | 5.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| go_daemon | 27.0 | 33.0 | 27.7 | 30.2 | 52.2 | 25.2 |
| mcp_access | 0.0 | 0.0 | -10.0 | 20.4 | 10.4 | 10.4 |
| rust_core_per_call | 0.0 | 25.0 | -2.0 | 13.2 | 13.2 | 13.2 |
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
