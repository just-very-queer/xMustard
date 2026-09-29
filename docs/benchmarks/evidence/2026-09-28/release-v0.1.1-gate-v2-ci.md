# Budget gate v2 report (2026-09-28T15:36:10Z)

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

Source `` · core `29386cd07f94` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [11.13, 9.98, 9.43] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| v1-workload | PASS | 66.6 (69.9) | 49.3 | 18.5 / 21.8 | 0 | two concurrent index clients | stdio-shim |
| agents-2-relay | PASS | 60.8 (63.8) | 46.5 | 20.2 / 23.2 | 0 | 2 agent(s) querying | stdio-relay |

### v1-workload

frozen v1 workload (501 generated files), v1's own code

Samples 228 (median interval 100.7 ms, max 141.1 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| go_daemon | 30.4 | 24.2 | 22.6 | 28.1 | 22.4 | 31.0 | – | 1 | 1 |
| mcp_access | 25.9 | 19.7 | 23.2 | 16.5 | 11.0 | 15.2 | – | 2 | 2 |
| rust_core_per_call | 20.8 | 12.6 | 20.8 | 13.6 | 10.2 | 17.5 | – | 12 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 58.9 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 3952224 | xmustard-api | go_daemon (owned) | 22.6 | 20.8 | 6.6 | 1.8 |
| 3952247 | xmustard-mcp | mcp_access (owned) | 12.9 | 8.2 | 5.1 | 1.8 |
| 3958718 | xmustard-core | rust_core_per_call (owned) | 10.7 | 5.5 | 2.1 | 8.5 |
| 3952249 | xmustard-mcp | mcp_access (owned) | 10.3 | 5.6 | 2.5 | 1.8 |
| 3958717 | xmustard-core | rust_core_per_call (owned) | 10.2 | 5.0 | 2.1 | 8.1 |

v1 cross-check (same run): v1 sampler 66.7 MiB (69.9 MB), v2 owned+external 66.6 MiB, difference -0.08 MiB (tolerance 8.0): within noise. Median sample interval v1 100.1 ms, v2 100.7 ms.

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 196 | 3202 | 0 | 0 | 16.5 | 16.5 | – | – |
| search | 6 | 0 | 330 | 453454 | 214251 | 53564 | 426.9 | 18709.3 | 504 | 42697324 |
| resources/read | 256 | 0 | 34385 | 22559129 | 22370304 | 5592576 | 2.8 | 9.2 | – | – |

### agents-2-relay

2 stdio agents through the native xmustard-relay (launched like the Go shim) to /mcp

Samples 304 (median interval 100.1 ms, max 105.1 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| go_daemon | 32.2 | 27.0 | 30.7 | 30.4 | 25.2 | 32.2 | – | 1 | 1 |
| mcp_access | 5.5 | 5.3 | 5.4 | 1.1 | 0.8 | 3.0 | – | 2 | 2 |
| rust_core_per_call | 28.3 | 12.4 | 24.8 | 20.4 | 9.8 | 22.1 | – | 11 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 43.2 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 3958818 | xmustard-api | go_daemon (owned) | 30.7 | 28.9 | 11.5 | 1.8 |
| 3960729 | xmustard-core | rust_core_per_call (owned) | 12.5 | 7.3 | 4.1 | 8.4 |
| 3960683 | xmustard-core | rust_core_per_call (owned) | 12.2 | 7.0 | 4.1 | 8.1 |
| 3958829 | xmustard-relay | mcp_access (owned) | 2.7 | 0.5 | 0.3 | 2.5 |
| 3958827 | xmustard-relay | mcp_access (owned) | 2.7 | 0.5 | 0.2 | 2.5 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3202 | 0 | 0 | 0.7 | 0.7 | – | – |
| tools/list | 2 | 0 | 4 | 17846 | 17732 | 4434 | 1.5 | 1.5 | – | – |
| ground | 2 | 0 | 84 | 9818 | 4249 | 1063 | 8592.4 | 8592.4 | – | – |
| remember | 2 | 0 | 321 | 4240 | 1594 | 399 | 9.5 | 9.5 | – | – |
| search | 6 | 0 | 404 | 489192 | 231515 | 57881 | 165.2 | 12413.3 | 1002 | 42359976 |
| explain | 6 | 0 | 428 | 25124 | 10718 | 2684 | 33.7 | 37.4 | – | – |
| impact | 6 | 0 | 376 | 14536 | 5622 | 1406 | 18.8 | 26.0 | – | – |
| recall | 6 | 0 | 586 | 20184 | 7911 | 1981 | 13.6 | 20.9 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 57, 'git': 1, 'helper': 0}, spawns_total=58, bytes_hashed=439699, captures=28, capture_bytes=261609

Features observed: http_mcp=true, watcher="absent", resident_index={"state": "on", "min": 1, "max": 1, "observations": 18}, uncapped_index={"truncated": false, "indexed_files": 501, "eligible_files": 501, "max_files": 10000}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Budget ledger

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| v1-workload | go_daemon | 24.2 | 22.4 | 27.0 | 30.4 | 33.0 |
| v1-workload | mcp_access | 19.7 | 11.0 | 0.0 **over** | 25.9 | 0.0 **over** |
| v1-workload | rust_core_per_call | 12.6 | 10.2 | 0.0 **over** | 20.8 | 25.0 |
| agents-2-relay | go_daemon | 27.0 | 25.2 | 27.0 | 32.2 | 33.0 |
| agents-2-relay | mcp_access | 5.3 | 0.8 | 0.0 **over** | 5.5 | 0.0 **over** |
| agents-2-relay | rust_core_per_call | 12.4 | 9.8 | 0.0 **over** | 28.3 | 25.0 **over** |

Reconciliation (open): design steady 64 MiB; peak 94 MiB by the §7.2 method, 112 MiB with query peaks overlapping the heavy slot; gate 95.4 MiB. Measured from this run (v1-workload, median of 1 valid run(s)). Already in the measurement (their lines are not projected again): WS-00, WS-01, WS-02, WS-03, WS-04, WS-06, WS-08, WS-09. Projected over the design line: go_daemon, mcp_access, rust_core_per_call, rust_index_service.

| Process | Design steady | Design peak | Workstream lines | Measured p50 | Projected steady | Overcommit |
|---|---|---|---|---|---|---|
| git_child | 0.0 | 5.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| go_daemon | 27.0 | 33.0 | 27.7 | 24.2 | 46.7 | 19.7 |
| mcp_access | 0.0 | 0.0 | -10.0 | 19.7 | 9.7 | 9.7 |
| rust_core_per_call | 0.0 | 25.0 | -2.0 | 12.6 | 10.6 | 10.6 |
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
