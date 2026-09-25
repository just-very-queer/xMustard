# Benchmarks and gates

All scripts start the real API, MCP shim and Rust core built from this checkout against
scratch repositories and data roots. None of them talks to a model provider.

| Gate | Command | What passes |
|---|---|---|
| Retrieval | `make bench-retrieval` (`retrieval-gate.sh`) | ≥ 11/12 gold queries in the top 5, cold, warm and after a one-file edit |
| Budget v1 (frozen at `6411f391`) | `rss.sh` | sampled tree peak ≤ 100,000,000 bytes on the 501-file workload |
| Budget v2 | `make bench-gate` (CI suite), `make bench-parity` (parity-scale suite) | ps-RSS of the xMustard-owned tree ≤ 95.4 MiB on a valid, complete workload |
| Gate v2 unit tests | `make bench-test` | stdlib `unittest`, no binaries needed |

## Budget gate v2 (`rss_bench_v2.py`)

The gate metric is unchanged from v1: one `ps` snapshot every 100 ms, RSS summed over the
xMustard-owned tree, limit 1e8 bytes (95.4 MiB). A run that lost a root, had a `ps`
error, skipped a step or failed a workload check is **INVALID**, never PASS.

Reported beside the gate, per process and per component:

- footprint: `phys_footprint` on macOS, PSS and USS on Linux;
- anon versus file-backed resident memory at the tree peak (Linux `RssAnon/RssFile`,
  macOS VM region walk);
- the unsampled per-process peak (macOS lifetime max footprint, Linux `VmHWM`), storage
  bytes read and written, and the number of processes seen (a lower bound);
- per-tool MCP accounting: calls, argument and response bytes, a bytes/4 token estimate,
  latency, and the files and bytes the result says it parsed; plus the API's own
  `/api/health` data-movement counters when the API exposes them.

Attribution comes from `budget_ledger.json` `process_roles`. Registered roots carry their
role; descendants are classified by executable. External processes (LSP servers,
compilers, tests, shells, agent CLIs) are reported on their own lines and never counted.
Unknown children of owned processes count as owned. xMustard binaries an agent launches
(hook clients, shims, relays) count toward the gate. A child still running its parent's
image (forked, not yet exec'd) counts as `fork_pre_exec`: ps-RSS counts its copy-on-write
pages a second time, which footprint does not.

Scenarios (`--scenarios a,b` or `--suite ci|parity`):

- `v1-workload` runs the frozen v1 workload through v1's own code, with v1's sampler and
  the v2 sampler side by side. A disagreement beyond `tolerance.v1_crosscheck_mib`
  invalidates the run.
- Parity scale, over the pinned composite fixture (cline + pi-mono + OpenHands without
  `enterprise/`, 7,288 files): `agents-1/2/4`, `reindex-during-queries`,
  `snapshot-swap-under-load`, `captures-during-index`, `two-hot-repos`,
  `four-agents-four-worktrees`, `watcher-on`. A scenario whose product feature is absent
  (for example the watcher) is reported as skipped with the missing requirement.

The parity-scale claim is established only when every parity scenario ran and passed with
agents over HTTP MCP, the watcher, static embeddings, no file cap, and all 17 grammars.
The report lists exactly what is missing. `--require-parity-claim` makes that fatal.

Outputs: `<out>/report.json` and `<out>/report.md` (`--out`, default `bench-out/gate-v2`).

### Fixtures (`parity_fixtures.json`)

Apache-2.0 and MIT sources only (GitNexus is PolyForm Noncommercial and is not used).
Each is fetched at its pinned commit into a scratch cache (`--fixture-cache`,
`XMUSTARD_FIXTURE_CACHE`, default `$TMPDIR/xmustard-parity-fixtures`), local
`research/` mirrors first, then upstream. It is checked against the pinned commit and
tree id, rebuilt from git objects (no eol or LFS filter touches the bytes), and verified
against the pinned workspace tree id, file count and on-disk content SHA-256. Every
scenario verifies its private copy again. `rss_v2.sh fixtures` fetches and verifies
without measuring; `rss_v2.sh fixtures --compute-pins` prints the values to pin after a
deliberate commit bump.

### Budget ledger (`budget_ledger.json`)

- `components`: the §7.2 design lines, each placed on a process role.
- `workstreams`: each workstream's MiB line and the process it lands in.
- `tolerance`: noise allowances for the delta check and the v1 cross-check.

`rss_v2.sh ledger` reconciles the design lines with the workstream lines. With
`--workstream WS-NN --baseline base/report.json`, a run fails when the tree-peak delta
or the delta of the workstream's process p50 exceeds the line plus tolerance; a negative
line is a saving that must be delivered. The process check uses footprint p50 (private
memory) when both runs report it, because RSS p50 of a Go process on macOS moves with
lazily reclaimed and compressed pages; the RSS delta is reported beside it. CI measures the pull request's base revision and
the head on the same runner, each as the median of three runs (`--repeat 3`; any failed
repeat fails the scenario), and takes the workstream from the branch name (`parity/ws-NN`).
