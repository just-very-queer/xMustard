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
error, skipped a step or failed a workload check is **INVALID**, never PASS. So is a run
whose sampler raised, whose sampler thread ended before the workload, or whose last
sample is more than 5 s older than the end of the workload.

Reported beside the gate, per process and per component:

- footprint: `phys_footprint` on macOS, PSS and USS on Linux;
- anon versus file-backed resident memory at the tree peak (Linux `RssAnon/RssFile/RssShmem`,
  macOS `TASK_VM_INFO` internal/external); both sum to the RSS `ps` reports;
- the unsampled per-process peak (macOS lifetime max footprint, Linux `VmHWM`), storage
  bytes read and written, and the number of processes seen (a lower bound);
- per-tool MCP accounting: calls, argument and response bytes, a bytes/4 token estimate,
  latency, and the files and bytes the result says it parsed; plus the API's own
  `/api/health` data-movement counters when the API exposes them.

Attribution comes from `budget_ledger.json` `process_roles`. Registered roots carry their
role; descendants are classified by executable. External processes (LSP servers,
compilers, tests, shells, agent CLIs) and their descendants are reported on their own
lines and never counted, except xMustard binaries: a hook client, shim, relay or core
found below an agent or any external process counts toward the gate again, with its own
descendants. Unknown children of owned processes count as owned. A child still running its
parent's image (forked, not yet exec'd) counts as `fork_pre_exec`: ps-RSS counts its
copy-on-write pages a second time, which footprint does not.

Scenarios (`--scenarios a,b` or `--suite ci|parity`):

- `v1-workload` (CI) runs the frozen v1 workload through v1's own code, with v1's sampler
  and the v2 sampler side by side. A disagreement beyond `tolerance.v1_crosscheck_mib`
  invalidates the run, and so does any external line (the frozen workload launches none,
  and v1's number cannot see an owned process misfiled as external).
- `agents-2-small` (CI for the workstreams that designate it): 2 agents on the same
  generated 501-file fixture (`v1-generated`, pinned by hash, no network), over HTTP MCP
  when the API serves `/mcp` and through one stdio Go shim each otherwise. WS-13's saving
  is measured here. It is not in the suite every pull request runs: at this HEAD its two
  shims and concurrent per-call cores reach the gate (69.9-96.8 MiB over three runs of one
  build on a loaded M1).
- `agents-2-relay` (CI): 2 stdio agents through the native `xmustard-relay`, launched
  exactly like the Go shim; skipped until the revision has a relay and `/mcp`.
- Parity scale, over the pinned composite fixture (cline + pi-mono + OpenHands without
  `enterprise/`, 7,288 files): `agents-1/2/4`, `reindex-during-queries`,
  `snapshot-swap-under-load`, `captures-during-index`, `two-hot-repos`,
  `four-agents-four-worktrees`, `watcher-on`. A scenario whose product feature is absent
  is reported as skipped with the missing requirement: `watcher-on` needs the watcher,
  `snapshot-swap-under-load` needs the resident index's `snapshot_generation` marker and
  checks that it advanced while the agents were querying.

Feature markers are on only by `feature_on_rule` in `parity_fixtures.json`: a scalar that
is not null, false, 0 or an off word, or an object with an `enabled`, `state` or `status`
field that is on. An object without one of those fields, or a list, is off, and an off
observation is kept over later on ones. The parity-scale claim is established only when
every parity scenario ran and passed with agents over HTTP MCP, the watcher, static
embeddings, no file cap, the resident index, and all 17 grammars. The report lists exactly
what is missing. `--require-parity-claim` makes that fatal.

Outputs: `<out>/report.json` and `<out>/report.md` (`--out`, default `bench-out/gate-v2`).
A single run of a borderline scenario can land on either side of the gate (agents-2 measured
79.4-97.0 MiB across three runs of one build), so decisions use `--repeat 3`. The reported
run is the median-peak run among the valid repeats (an invalid repeat is never the
measurement), and any non-PASS repeat fails the scenario with the most severe verdict
(FAIL over INVALID). The ledger compares per-metric medians over the valid repeats.

### Fixtures (`parity_fixtures.json`)

Apache-2.0 and MIT sources only (GitNexus is PolyForm Noncommercial and is not used),
plus `generated.v1-generated`, the frozen v1 generator's output, built locally.
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
- `go_daemon_suballocation`: the §7.3 items inside the daemon line; each go_daemon
  workstream names its `item`.
- `workstreams`: each workstream's MiB line, its process and, where the line only shows
  at a larger scale or over another transport, the `scenarios` it is measured on.
- `tolerance`: noise allowances (tree peak, per-process p50 with overrides, v1 cross-check).
- `reconciliation`: the owner decision the ledger still needs (status `open`).

`rss_v2.sh ledger` reconciles the design lines, the §7.3 items and the workstream lines
against the measured p50 and exits nonzero while any process is projected over its design
line. With `--workstream WS-NN` (on `run` with `--baseline`, or on `ledger` with `--head`
and `--baseline`), each common scenario is checked:

- tree: owned gate-peak delta ≤ line + 10 MiB; the owned+external peak delta must meet the
  same allowance, and a negative delta counts only net of external growth (moving work
  into an external process is never a saving);
- process: the workstream's process p50 delta ≤ line + its tolerance (footprint p50 when
  both runs have it, because RSS p50 of a Go process on macOS moves with lazily reclaimed
  and compressed pages; the RSS delta is reported beside it);
- every other owned component: p50 delta ≤ 0 + its tolerance;
- design (cumulative): every process that was within its §7.2 design line at the ledger's
  reference must stay within design steady + tolerance (footprint p50 when present, else
  RSS p50), so small in-tolerance deltas still fail once they add up. Processes already
  over their line at the reference
  (today's stdio shims and per-call cores) are checked by delta only.

A line applies as declared on its designated scenarios and only as a ceiling
(max(line, 0)) elsewhere. Verdicts: **FAIL** (blocks CI), **NOT_CHECKABLE** (the
designated scenarios did not run on both sides), **BELOW_RESOLUTION** (every check held,
but |line| is smaller than its check's tolerance, so the line itself, or a saving such as
WS-25's 2 MiB, is not verified; only a larger overrun would have been seen), **PASS**. A
resolvable saving is enforced to within the tolerance: WS-49's -4 MiB must show at least
1 MiB.

CI measures the pull request's base revision and the head on the same runner (three
repeats each) over the CI suite plus the workstream's designated scenarios
(`rss_v2.sh ci-plan --branch REF`, which takes the workstream from a branch path segment
starting `ws-NN`). The check uses the base revision's ledger; a pull request that changes
any non-prose ledger value (`rss_v2.sh ledger --diff BASE_LEDGER`) fails unless a
maintainer adds the `budget-ledger-change` label. The measuring code and the workflow still
come from the pull request, so a change to `scripts/bench/` or `.github/workflows/` needs
review; branch protection with code owners for those paths is the owner's setting.
