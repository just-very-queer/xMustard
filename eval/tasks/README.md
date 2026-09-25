# Evaluation task corpus and the `xmustard-eval` executor

`xmustard-eval` (`api-go/cmd/xmustard-eval`) runs coding tasks with a real headless
client under paired arms and reports whether xMustard changes the outcome
(PAR-EVAL-01), how shared memory behaves over its lifecycle (PAR-EVAL-02), and edit
localization against gold files (a PAR-EVAL-09 component). It is an operator tool for
macOS and Linux. It runs outside the measured xMustard process tree and never ships
with the product.

This directory holds the corpus schema (this file), a seed corpus (`seed.yaml`) and an
example run config (`run-config.example.yaml`). The seed tasks exercise the harness. They
are not evidence for a parity claim. WS-63 authors the evaluation corpus proper, and
WS-50 defines the numeric parity thresholds.

## Quick start

```bash
cd api-go
go build -o /tmp/xm/xmustard-eval ./cmd/xmustard-eval

# schema only
/tmp/xm/xmustard-eval validate --corpus ../eval/tasks/seed.yaml
# every oracle fails on the untouched task and passes on its reference patch
/tmp/xm/xmustard-eval validate --corpus ../eval/tasks/seed.yaml --oracles

# dry run: fake client, stub stack, no credentials, no model calls
/tmp/xm/xmustard-eval run --corpus ../eval/tasks/seed.yaml --out /tmp/xm/dry --driver fake:claude

# dry run against the real stack (needs the Rust core)
go build -o /tmp/xm/xmustard-api ./cmd/xmustard-api
go build -o /tmp/xm/xmustard-mcp ./cmd/xmustard-mcp
/tmp/xm/xmustard-eval run --corpus ../eval/tasks/seed.yaml --out /tmp/xm/dry-real --driver fake:codex \
  --api-bin /tmp/xm/xmustard-api --mcp-bin /tmp/xm/xmustard-mcp --core-bin ../rust-core/target/release/xmustard-core

# rebuild report.json / report.md from a finished run
/tmp/xm/xmustard-eval report --out /tmp/xm/dry
```

The output directory must be new. It receives:

| File | Content |
| --- | --- |
| `eval.json` | Manifest: corpus sha256, the sha256 of every oracle source and reference patch as loaded, the repository paths hidden alongside them, run config (peer env removed), client version, codex MCP servers, stack binary sha256s, host, the executor's own peak RSS, and `aborted` when the run stopped early |
| `runs.jsonl` | One `xmustard.eval.run/v1` record per (task, arm, repetition), appended and fsynced as each run ends |
| `runs/<task>/<arm>/r<n>/` | `prompt.txt`, `transcript.jsonl` (client stdout), `client.stderr.log`, `diff.patch`, `setup-*.log`, `verify.log`, `oracle.log`, `rss.jsonl`, and for xMustard arms `stack/` (API logs and the run's data directory) |
| `report.json`, `report.md` | The paired report. It is a pure function of `eval.json` and `runs.jsonl` |

## Corpus schema (`xmustard.eval/v1`)

YAML, decoded strictly: an unknown key is an error. Relative paths resolve against the
corpus file's directory.

```yaml
schema: xmustard.eval/v1        # required, exact
name: seed                      # required
description: free text
defaults:
  timeout_sec: 900              # agent wall-clock limit per run (default 900)
  repeats: 1                    # default repetitions when the run config sets none
tasks:
  - id: calc-mean-fraction      # [a-z0-9][a-z0-9._-]{0,63}, unique
    class: bugfix               # bugfix | feature | refactor | localization |
                                # memory_lifecycle | reduction | component
    repo:                       # exactly one of fixture / path
      fixture: fixtures/go-calc #   a directory made into a one-commit repo (pinned author and date, so a fixed sha)
      # path: /abs/clone        #   a local git repository (a shallow clone works)
      # ref: <full sha>         #   required with path; history is fetched only up to it
    setup:                      # harness-run commands before the agent (for example npm ci)
      - {cmd: [npm, ci], timeout_sec: 600, env: {CI: "1"}}
    prompt: |                   # what the agent is told
      ...
    verify:                     # model-visible check, named in the prompt, run again after the agent
      cmd: [go, test, ./...]
    oracle:                     # hidden, harness-owned; a run is resolved only if it passes
      files:                    # copied in only after the agent has exited
        - {src: oracles/calc_mean_hidden_test.go, dest: calc/mean_hidden_test.go}
      cmd: [go, test, -count=1, -run, Hidden, ./calc/]
      timeout_sec: 600
    reference: {patch: references/calc-mean.patch}   # known-good fix (validate, fake driver)
    gold_files: [calc/calc.go]  # edit-localization ground truth
    arms: [baseline, xmustard_mcp]                   # optional restriction
    timeout_sec: 900            # overrides defaults.timeout_sec
    memory: ...                 # coding-memory fixture, below
```

Oracle sources and reference patches are read once, when the corpus is loaded. The
harness stages and applies those bytes and never re-reads them. Their digests go into
`eval.json`. Before each oracle and at the end of the run, the files on disk are
checked against those digests. A change means something wrote to a hidden path: the
run fails as `corpus_changed`, no further run starts, and the report is marked
`ABORTED`.

Commands are argv lists and run without a shell. Use `[sh, -c, "..."]` when you need
one. They run in the worktree with the harness environment minus every `XMUSTARD_*`
variable.

### Memory fixtures (PAR-EVAL-02)

The `xmustard_memory` arm seeds `memory.seed` before the agent starts. Seeding uses the
real propose and verify routes. An author principal proposes, and two other principals
approve, so every seeded memory is `peer_verified` exactly as in use. A seed that does
not end in the expected state fails the run rather than mislabel the fixture. `drift`
edits run afterwards in every arm, so all arms start from the same files.

```yaml
memory:
  recall_k: 5                   # k for current-fact recall@k (default 5)
  seed:
    - key: port-decision        # unique within the task
      label: current            # ground truth only the harness knows (see below)
      title: Service port decision
      content: "Team decision (2026-08): the service listens on port 8081 ..."
      paths: [server/config.go] # recall flags the memory stale when these change
      supersedes: port-original # names a memory labelled superseded
    - {key: port-original, label: superseded, content: "...", paths: [server/config.go]}
    - {key: port-reminder, label: duplicate, content: "...", duplicate_of: port-decision}
    - {key: port-infra-ticket, label: contradiction, content: "...", contradicts: port-decision}
    - {key: read-timeout, label: stale, content: "...", paths: [server/timeouts.go]}
    - {key: other-repo-port, label: foreign_scope, content: "..."}   # seeded into another workspace
    - {key: unreviewed-port, label: pending, content: "..."}         # proposed, never verified
  drift:
    - {path: server/timeouts.go, replace: "..."}   # exactly one of append | replace | delete
```

| Label | Meaning | Served (delivered to the model) counts as |
| --- | --- | --- |
| `current` | a true, current fact | current-fact recall |
| `stale` | true when written; a drift edit changes one of its paths | stale-served rate when delivered without `stale: true` |
| `superseded` | replaced by a later memory's `supersedes` | superseded-served rate |
| `duplicate` | restates `duplicate_of` | duplicate rate when both reach the model in one result |
| `contradiction` | conflicts with `contradicts` | scored against the server's conflict groups (precision, recall) |
| `foreign_scope` | lives in a different workspace | scope leakage; must be 0 |
| `pending` | never verified | governance violation if served; worse if shown as `peer_verified` |

The harness puts a marker (`[xmem-<hash>]`) at the start of each seeded memory's content.
It then scans the tool results in the client's transcript, so "served" means the model
actually received the memory. Current-fact recall counts a current fact delivered
anywhere. Recall@k counts it only when a ranked `recall` result lists it among its first
`recall_k` entries. When no recall result has a ranked `entries` list, recall@k is n/a.
Promotion errors count entries that became verified during a single-principal run.
Stale-memory harm counts memory-arm runs that failed the oracle after a harmful memory
was delivered, where the paired `xmustard_mcp` run (same task, same repetition, no
memory) passed. A failing run with a harmful delivery but no completed `xmustard_mcp`
partner is counted as unpaired and reported with a warning. When every such run is
unpaired, harm is n/a, not 0. Tokens per recall use the bytes/4 heuristic and are
labelled as estimates.

Not covered by this schema: **expiry**. xMustard memories have no expiry or
valid-until field, so an expired-memory fixture cannot be seeded through the real API.
A PAR-EVAL-02 expiry fixture needs that product field first, then a label here
(`corpus.go` `memoryLabels`, `validateMemory`) and a metric in `memory.go`. WS-50,
which authors the lifecycle fixtures, has to edit `corpus.go` for it.

## Arms

| Arm | Client configuration |
| --- | --- |
| `baseline` | the client exactly as the operator configured it |
| `baseline_nomcp` | every MCP server disabled: claude `--strict-mcp-config` with an empty config; codex `-c mcp_servers.<name>.enabled=false` for each server that `codex mcp list --json` reports (`-c mcp_servers={}` merges rather than replaces); pi `--no-extensions` |
| `xmustard_mcp` | `baseline_nomcp` plus the xMustard stack: MCP server `xmustard` for claude and codex, the Pi adapter (`-e`) for pi; empty memory. Server credentials never reach a command line: claude reads its MCP config from a 0600 file, and codex forwards server env by name (`env_vars`) from its own environment |
| `xmustard_mcp_hooks` | `xmustard_mcp` plus `hooks.<driver>` arguments from the run config. Without them it is skipped and named as a placeholder until WS-23 builds the adapters |
| `xmustard_memory` | `xmustard_mcp` plus the task's seeded memory. Tasks without `memory.seed` are skipped and named |
| `peer:<name>` | `baseline_nomcp` plus one peer MCP server from `peers`. Peers are installed separately and never vendored. `owner_decision` is required when the license names noncommercial terms, is any PolyForm license, or carries an NC term (such as CC-BY-NC), and whenever the peer's name, command or arguments mention a known noncommercial peer (GitNexus), whatever its license field says. The rule that applied is recorded as `owner_decision_rule` in `eval.json` |

xMustard arms get one extra prompt line naming the workspace id. That line is the only
prompt difference between arms, and `prompt_sha256` records it. Every xMustard run gets
a fresh data directory and its own principals. The agent phase restarts the API as the
`CORE_ONLY` nine-tool surface (`stack.core_only`, default true).

## Isolation

The agent must not see the hidden oracle, and nothing it wrote may run outside its
containment. Code the agent wrote runs again after the agent exits: the visible verify
step runs its tests, the oracle compiles and runs them, xMustard's own git reads the
repository config it may have changed, and background processes it detached keep
running. So:

- **Fresh detached worktree per run.** Each run gets its own repository, which borrows
  the task's scratch repository's objects through git alternates, and a detached
  worktree of it. Commits, branches, config and hooks an agent creates are removed with
  the run and never reach a later run. Worktrees are removed on success, failure and
  interrupt (SIGINT or SIGTERM: the run in flight is killed and recorded as
  `interrupted`, the report covers the completed runs, and the exit code is 130).
- **History stops at the task ref.** A path repository is fetched into the scratch
  repository only up to `ref`. `FETCH_HEAD` is deleted because it names the source.
- **The harness never runs git through agent-writable config.** The agent can write
  its run repository's config and hooks, the worktree's `.git` file and
  `.gitattributes`, and git runs configured commands (`core.fsmonitor`, clean filters,
  `diff.external`, textconv) as whoever invokes it. Snapshots, diffs and restores
  therefore go through a harness-owned git directory and index that borrow only the
  read-only scratch objects, with system and global config ignored, the fsmonitor and
  hooks off, and `--no-ext-diff --no-textconv`. Removal deletes the directories without
  running git.
- **One containment for everything that runs agent code.** The client, the setup,
  verify and oracle commands, and the xMustard API (it runs git in the worktree, so a
  planted `core.fsmonitor` or hook would otherwise run outside the sandbox) all run
  under the same wrapper: `sandbox-exec` on macOS, or `bwrap` on Linux when installed
  (with a private pid namespace). It denies reads and writes of:
  - the corpus file, oracle sources, reference patches and the originals of path
    repositories;
  - the git directory of any repository that contains one of those, since its objects
    hold the committed bytes (`git show HEAD:<oracle>`), and the same paths in each of
    that repository's other worktrees;
  - the output directory (other runs' diffs and oracle logs). The API is allowed only
    its own stack directory inside it;
  - the harness's git directories and watchdog state, and every other run's worktree,
    repository, client config and fake-driver files, including kept worktrees.

  It denies writes to the scratch repositories and to the operator's global git
  config (`~/.gitconfig`, `~/.config/git`). The mode is recorded per run. `none` puts a
  warning in the report. PWD and OLDPWD are removed from every child's environment, and
  PWD is set to the command's own directory, so the operator's directory is not
  revealed.
- **Hidden oracle.** Oracle files enter the worktree only after all of these steps:
  - the agent's process group is dead;
  - every other process of the run is dead (see the next item);
  - the visible verify step has run;
  - the worktree has been checked against the agent's final snapshot.

  Verify runs after that snapshot. If verify changed non-ignored files, the worktree is
  restored to the snapshot, the run records `verify_changed_tree`, and the report warns.
  The oracle therefore judges exactly the tree in `diff.patch`. Before the agent starts,
  the worktree is scanned for any file whose bytes equal an oracle file, and for
  existing oracle destinations. A hit fails the run as `oracle_visible`.
- **Escaped processes.** Processes that leave the client's process group (`setsid`, as
  Pi's bash tool does for every command) are found and killed at three points: after
  the agent, after verify, and after the oracle. A run fails if any survive. They are
  found three ways:
  - as descendants the 100 ms process sampler saw while the client ran, identified by
    pid and start time;
  - by a per-run environment marker (Linux reads `/proc/<pid>/environ`; recent macOS no
    longer exposes other processes' environments);
  - by a working directory inside the worktree.

  On macOS, a process that detaches and leaves the worktree within 100 ms of starting,
  with a parent that exits just as fast, can be missed. It stays inside the run's
  containment. On Linux, bwrap's pid namespace kills everything when the sandbox exits.
- **Executor death.** A watchdog child process holds a pipe to the executor. If the
  executor dies without cleaning up (SIGKILL, an OOM kill, a test timeout), the
  watchdog kills the live process groups and the run's escaped processes, and removes
  the work root.
- **Kept worktrees.** `--keep-worktrees` keeps every run's worktree under the work
  root, which is printed at the end, with the oracle files removed. Each is hidden from
  later runs' agents. `isolation.kept_at` names the directory, and the report warns
  that worktrees were kept. Remove the work root when done.
- **Nested seatbelts.** A macOS seatbelt cannot be applied inside another one. Under
  `sandbox-exec`, codex runs with `--sandbox danger-full-access` because the sandbox is
  external. If Claude Code's own sandbox is enabled in the operator's settings, use
  `--containment none` or disable it for the eval.
- Paths the agent can see (its working directory and the workspace id derived from it)
  are an opaque run id plus the repository's own name, identical across arms.

What containment does not cover:
- The sandbox allows everything it does not deny. The agent can read the rest of the
  machine and write its home directory (caches, client settings), and those writes
  persist into later runs.
- Copies of the corpus outside its repository's worktrees are not found. Keep oracle
  sources and reference patches out of any other place an agent could read, such as
  another clone or a synced folder.
- Ignored files (per `.gitignore`) are outside every snapshot. Verify-time changes to
  them are not undone.

## Drivers and accounting

| Driver | Invocation | Final event used for tokens and cost |
| --- | --- | --- |
| `claude` | `claude -p --output-format stream-json --verbose --no-session-persistence --permission-mode bypassPermissions --model M`, prompt on stdin | `{"type":"result"}`: `modelUsage` summed over models (else `usage`), `total_cost_usd` |
| `codex` | `codex exec --json --color never -C <worktree> --sandbox workspace-write -m M -` (`danger-full-access` under `sandbox-exec`, see Isolation), prompt on stdin | every `turn.completed` `usage` (cached tokens split out of `input_tokens`); cost from `pricing` or `unpriced` |
| `pi` | `pi --mode rpc --no-session --model M`; the harness sends `prompt`, waits for `agent_settled`, then asks `get_session_stats` | the `get_session_stats` response (tokens, cost). Without it, per-message usage is reported with `usage_source: message_end` and `final_event: false` |

A run without a final event is flagged in the report. Token numbers count only when the
client reported session usage in its final event (`usage_reported`). A run killed at
its timeout before that event, or a codex run that ends in `turn.failed`, is left out
of token medians and deltas and counted as `runs_without_reported_usage`. It is never
counted as 0 tokens. The price table prices only reported usage. Pi's per-message sums
are kept in the record but left out of cost medians as partial. Nothing is estimated
from text length except the labelled memory-volume estimate.

A client failure that is not a timeout (a non-zero exit, `is_error`, `turn.failed`, a
`stopReason` of error) is recorded as `client_error` and named in a report warning. By
default it is scored as the agent's outcome. Setting `exclude_client_errors: true` in the
run config pre-registers that such runs leave the pairs with status `client_error`. That
rule also covers budget and turn limits (`error_max_budget_usd`, `error_max_turns`). Each run also records wall time,
exit status, tool-call counts, the delivered xMustard (and peer) tool results by size,
diff churn from snapshot trees (tracked, modified and new untracked files, honouring
`.gitignore`), and edit localization (gold files touched by the diff: recall and
precision).

`fake:claude`, `fake:codex` and `fake:pi` run the same invocation with this executable as
a fake agent that speaks the client's protocol. The fake agent calls `ground` and
`recall` through the arm's wiring (MCP for claude and codex, HTTP like the Pi adapter
for pi), applies the task's reference patch unless `--fake-fail-arms` names the arm,
and emits deterministic usage. `stack: stub` serves ground and recall from memory for
dry runs only. Records and the report carry `dry_run`.

## RSS

For every run the executor samples `ps -A -o pid,ppid,rss,comm` every 100 ms, which is
the frozen v1 method. It sums RSS over the xMustard-owned tree: the registered API
process and its descendants (core, git), plus any xMustard process under the client,
with its descendants. xMustard processes are matched by name. The names are
`xmustard-api`, `xmustard-mcp` and `xmustard-core`, plus the basenames of the
configured `api_bin`, `mcp_bin` and `core_bin`, so a renamed shim or relay still counts.
Each run records the names it used (`xmustard_names`). The containment wrapper (bwrap)
and the watchdog are in neither tree. Peaks are attributed
to roles and to phases (`setup`, `agent`, `post`), and checked against the 95.4 MiB gate
(`1e8` bytes). The client's other processes are an external line, and so is the
executor's own peak RSS (getrusage). These are sampled ps-RSS peaks. phys_footprint and
PSS/USS are not measured here. WS-10's gate v2 and WS-50's parity-scale gate are
authoritative.

## Report

The report includes, per arm, the resolve rate by hidden oracle, visible-verify passes,
medians of tokens, cost, wall time, churn and edit-localization recall, and xMustard RSS
and agent RSS maxima. It also reports per-task and per-arm medians over repetitions,
resolve rate by task class, and memory-lifecycle aggregates. It flags governance
violations (scope leakage, delivered pending memories, promotion errors) as warnings and
names every run that did not complete, with its reason. It also warns about:
- client errors;
- runs without reported usage;
- verify steps that changed the tree;
- processes killed after escaping their group;
- unpaired stale-memory harm;
- kept worktrees;
- an aborted run.

Paired comparisons of each arm against `reference_arm` use `memory_harness.go` as is.
Repetitions of one task are not independent, so runs pair on the **task**. A task
counts as solved for an arm when a strict majority of its completed repetitions
resolved, so use an odd `repeats`. `CompareArms` supplies the exact McNemar test and
the paired-bootstrap solve-delta CI on those task-level outcomes, and gives GO or NO-GO
against the pre-registered `thresholds`. `solve_rate_delta_ci` bootstraps the per-task
mean solve-rate delta over tasks, which keeps the information that majority voting
drops. The token, cost, wall-time and edit-localization CIs bootstrap per-task median
deltas over tasks. A token delta pairs a task only when both arms have reported usage
for it. An arm with no task paired is `NO-DATA`. The same inputs always give
byte-identical `report.json` and `report.md`.

## Real-model run (operator step)

No real-model run was made while this executor was built. A real run needs credentials
and a budget, so an operator runs it:

1. Install and authenticate the client (`claude`, `codex`, or Pi from
   `integrations/pi` with `npm ci`). Record the version. The manifest stores it too.
2. Build the stack from the commit under test (`make build`, or the `go build` lines
   above plus `cargo build --release --bin xmustard-core`). Keep the binaries outside the
   output directory and outside any path repository used by a task. The agent cannot
   read those paths.
3. Prepare fixtures: Apache/MIT repositories (for example cline, pi-mono) cloned to
   scratch at pinned shas. GitNexus stays a design reference only. Do not use its code
   or fixtures, and do not run it as a peer without an explicit owner decision.
4. Run `validate --oracles` on the corpus. Every task must be valid.
5. Copy `run-config.example.yaml`, then set `model`, `repeats` (at least 3), `seed`,
   the pre-registered `thresholds`, the client `max_budget_usd` (claude) and `pricing`
   (codex).
6. `xmustard-eval run --corpus <corpus> --config <run.yaml> --out <new dir>`. The run is
   sequential. Budget about `tasks × arms × repeats × timeout`.
7. Read `report.md`. Each of these warnings qualifies the result:
   - a dry-run banner;
   - `containment none`;
   - runs without a final event or reported usage;
   - client errors;
   - a verify step that changed the tree;
   - a baseline with an operator `xmustard` MCP server;
   - governance warnings;
   - `ABORTED`. Transcripts can contain repository content, so
   review them before sharing the output directory.

## Authoring rules (for WS-63)

- An oracle must fail on the untouched starting state (after `setup` and `drift`) and
  pass once `reference.patch` is applied. `validate --oracles` checks both. Keep oracle
  sources outside fixture directories. The loader rejects them there, and the leak scan
  catches copies inside a repository.
- An oracle must not be reachable through anything the agent can read. The harness
  hides the corpus's files, the git directory of the repository that holds them, and
  their copies in that repository's other worktrees. It cannot find other clones or
  copies. A corpus committed to a repository that an agent's task also uses as
  `repo.path` is hidden with that whole repository.
- Prompts describe the goal and never the oracle. `verify` is what the model is told to
  run. It should pass on the reference, and it must not need the oracle's files.
- Use Apache/MIT repositories, pin full shas, and prefer tasks that fail on `baseline`
  for a reason xMustard is designed to address. Record the licence of every fixture.
