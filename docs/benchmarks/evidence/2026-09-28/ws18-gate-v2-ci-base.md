# Budget gate v2 report (2026-09-27T20:29:40Z)

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

Source `` · core `2a4bac0a650f` · Linux-7.0.0-34-generic-x86_64-with-glibc2.43 x86_64 · 6 CPUs · load [3.96, 3.96, 3.91] · probe `linux`

## Scenarios

| Scenario | Status | Gate peak MiB (MB) | Footprint peak MiB | Anon / file at peak MiB | Externals peak MiB | Peak step | Transport |
|---|---|---|---|---|---|---|---|
| v1-workload | PASS | 61.2 (64.2) | 44.2 | 13.9 / 21.4 | 0 | two concurrent index clients | stdio-shim |
| agents-2-relay | PASS | 66.3 (69.5) | 46.7 | 25.5 / 21.8 (+5.3 exited) | 0 | 2 agent(s) querying | stdio-relay |

### v1-workload

frozen v1 workload (501 generated files), v1's own code

Samples 244 (median interval 100.1 ms, max 103.8 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 5.5 | 0.0 | 0.0 | 3.1 | 0.0 | 5.5 | – | 2 | 1 |
| go_daemon | 26.7 | 23.7 | 21.3 | 24.9 | 21.9 | 29.4 | – | 1 | 1 |
| mcp_access | 24.8 | 17.8 | 20.4 | 15.3 | 9.0 | 14.6 | – | 2 | 2 |
| rust_core_per_call | 19.6 | 12.3 | 19.6 | 15.1 | 9.7 | 17.4 | – | 12 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 76.6 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 413839 | xmustard-api | go_daemon (owned) | 21.3 | 19.5 | 5.6 | 1.8 |
| 413847 | xmustard-mcp | mcp_access (owned) | 10.9 | 6.1 | 3.1 | 1.8 |
| 414530 | xmustard-core | rust_core_per_call (owned) | 9.9 | 4.7 | 1.8 | 8.2 |
| 414531 | xmustard-core | rust_core_per_call (owned) | 9.7 | 4.5 | 1.8 | 7.9 |
| 413849 | xmustard-mcp | mcp_access (owned) | 9.5 | 4.7 | 1.7 | 1.8 |

v1 cross-check (same run): v1 sampler 60.5 MiB (63.4 MB), v2 owned+external 61.2 MiB, difference 0.72 MiB (tolerance 8.0): within noise. Median sample interval v1 100.1 ms, v2 100.1 ms.

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 196 | 3184 | 0 | 0 | 1.8 | 1.8 | – | – |
| search | 6 | 0 | 330 | 255522 | 119220 | 29807 | 314.4 | 12046.4 | 504 | 21559504 |
| resources/read | 256 | 0 | 34385 | 22559129 | 22370304 | 5592576 | 1.5 | 3.5 | – | – |

### agents-2-relay

2 stdio agents through the native xmustard-relay (launched like the Go shim) to /mcp

Samples 326 (median interval 100.1 ms, max 118.9 ms). Footprint basis: PSS (/proc/<pid>/smaps_rollup Pss); USS = Private_Clean + Private_Dirty. Split basis: RssAnon / RssFile / RssShmem (/proc/<pid>/status), every sample.

| Component | Peak MiB | p50 MiB | At gate peak MiB | Footprint peak MiB | Footprint p50 MiB | Largest process RSS peak MiB (unsampled) | Largest process footprint peak MiB (unsampled) | Processes seen | Max concurrent |
|---|---|---|---|---|---|---|---|---|---|
| git_child | 10.5 | 0.0 | 5.3 | 3.4 | 0.0 | 5.4 | – | 13 | 2 |
| go_daemon | 30.1 | 24.2 | 24.2 | 28.3 | 22.4 | 30.0 | – | 1 | 1 |
| mcp_access | 5.8 | 5.4 | 5.4 | 1.2 | 0.9 | 2.9 | – | 2 | 2 |
| rust_core_per_call | 31.4 | 12.6 | 31.4 | 23.4 | 9.8 | 27.8 | – | 22 | 2 |

External processes: none observed under the xMustard roots.
- agent line `bench-harness` (the bench harness, external): peak 52.4 MiB

Processes at the gate peak:

| PID | Command | Component | RSS MiB | Footprint MiB | Anon MiB | File MiB |
|---|---|---|---|---|---|---|
| 415509 | xmustard-core | rust_core_per_call (owned) | 25.2 | 21.2 | 15.9 | 9.3 |
| 415323 | xmustard-api | go_daemon (owned) | 24.2 | 22.4 | 8.7 | 1.8 |
| 415507 | xmustard-core | rust_core_per_call (owned) | 6.2 | 2.1 | 0.6 | 5.6 |
| 417658 | git | git_child (owned) | 5.3 | – | – | – |
| 415333 | xmustard-relay | mcp_access (owned) | 2.7 | 0.4 | 0.2 | 2.5 |
| 415331 | xmustard-relay | mcp_access (owned) | 2.7 | 0.4 | 0.2 | 2.5 |

Tool usage (PAR-EVAL-04, harness side; tokens: ceil(bytes/4) heuristic over result content bytes; no tokenizer is loaded):

| Call | Calls | Errors | Arg bytes | Response bytes | Content bytes | Est. tokens | p50 ms | p95 ms | Files parsed | Bytes parsed |
|---|---|---|---|---|---|---|---|---|---|---|
| initialize | 2 | 0 | 202 | 3184 | 0 | 0 | 3.1 | 3.1 | – | – |
| tools/list | 2 | 0 | 4 | 17622 | 17502 | 4376 | 3.7 | 3.7 | – | – |
| ground | 2 | 0 | 84 | 6852 | 2846 | 712 | 9222.7 | 9222.7 | – | – |
| remember | 2 | 0 | 321 | 4139 | 1543 | 387 | 10.2 | 10.2 | – | – |
| search | 6 | 0 | 404 | 255360 | 118844 | 29714 | 209.3 | 12359.9 | 1002 | 42359976 |
| explain | 6 | 0 | 428 | 24332 | 10346 | 2588 | 79.5 | 87.5 | – | – |
| impact | 6 | 0 | 376 | 13744 | 5250 | 1316 | 70.3 | 71.1 | – | – |
| recall | 6 | 0 | 586 | 15492 | 5664 | 1418 | 69.2 | 72.3 | – | – |

API data-movement counters (delta over the scenario): spawns={'core': 47, 'git': 1, 'helper': 0}, spawns_total=48, bytes_hashed=152899, captures=28, capture_bytes=144493

Features observed: http_mcp=true, watcher="absent", resident_index={"state": "on", "min": 1, "max": 1, "observations": 18}, uncapped_index={"truncated": false, "indexed_files": 501, "eligible_files": 501, "max_files": 10000}

## Grammars

15 of 17 parity languages parse with a grammar: go, rust, typescript, tsx, javascript, python, java, c, cpp, csharp, ruby, php, kotlin, swift, bash.

## Budget ledger

Measured against the §7.2 design lines (this run, medians of the valid repeats; the steady test uses footprint p50 when present, else RSS p50):

| Scenario | Process | RSS p50 | Footprint p50 | Design steady | RSS peak | Design peak |
|---|---|---|---|---|---|---|
| v1-workload | git_child | 0.0 | 0.0 | 0.0 | 5.4 | 5.0 **over** |
| v1-workload | go_daemon | 23.7 | 21.9 | 27.0 | 27.7 | 33.0 |
| v1-workload | mcp_access | 17.8 | 9.0 | 0.0 **over** | 24.6 | 0.0 **over** |
| v1-workload | rust_core_per_call | 12.3 | 9.7 | 0.0 **over** | 19.6 | 25.0 |
| agents-2-relay | git_child | 0.0 | 0.0 | 0.0 | 10.5 | 5.0 **over** |
| agents-2-relay | go_daemon | 24.3 | 22.5 | 27.0 | 30.1 | 33.0 |
| agents-2-relay | mcp_access | 5.4 | 0.9 | 0.0 **over** | 5.6 | 0.0 **over** |
| agents-2-relay | rust_core_per_call | 12.5 | 9.8 | 0.0 **over** | 31.5 | 25.0 **over** |

Reconciliation (open): design steady 64 MiB; peak 94 MiB by the §7.2 method, 112 MiB with query peaks overlapping the heavy slot; gate 95.4 MiB. Measured from this run (v1-workload, median of 3 valid run(s)). Already in the measurement (their lines are not projected again): WS-00, WS-01, WS-02, WS-03, WS-04, WS-06, WS-08, WS-09. Projected over the design line: go_daemon, mcp_access, rust_core_per_call, rust_index_service.

| Process | Design steady | Design peak | Workstream lines | Measured p50 | Projected steady | Overcommit |
|---|---|---|---|---|---|---|
| git_child | 0.0 | 5.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| go_daemon | 27.0 | 33.0 | 27.7 | 23.7 | 46.2 | 19.2 |
| mcp_access | 0.0 | 0.0 | -10.0 | 17.8 | 7.8 | 7.8 |
| rust_core_per_call | 0.0 | 25.0 | -2.0 | 12.3 | 10.3 | 10.3 |
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
