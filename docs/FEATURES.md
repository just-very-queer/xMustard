# Features

What xMustard `v0.1.0` ships, what backs each piece, and what is still planned.
Measured numbers link to their record. Where nothing was measured, this page says so.

xMustard gives the coding agents you already use a shared memory they can check, and
narrow, current context about your repository. It runs locally, with no Docker, and
reaches agents through nine MCP tools. Details: [release notes](releases/v0.1.0.md),
[architecture](ARCHITECTURE.md), [security](SECURITY.md).

## Nine tools, one small surface

Agents learn nine tools, not dozens. Modes and arguments do the rest.

| Tool | What the agent gets | Backed by |
|---|---|---|
| `ground` | What changed, went stale, broke or got blocked since the baseline, including changed signatures (contract breaks) | An output budget: `max_chars` defaults to 6,000 and accepts 2,000 to 65,536. Failure and stale signals are never dropped silently; they survive as counts or flags (`api-go/internal/groundbudget/`) |
| `recall` | Shared memory ranked for the task at hand | BM25, path overlap, trust, recency and feedback; `q`, `paths`, `limit` (default 8, max 50); a 4,000-character budget by default on MCP; entries flagged stale when their files change |
| `remember` | A way to propose a fact, decision or gotcha for every agent | Pending until enough distinct agents verify it; `paths` anchors it to files for drift checks |
| `verify` | A way to approve or reject a peer's proposal | Identity is the caller's token, never an argument; needs the `verifier` role |
| `search` | `path:line` hits with snippets and reasons | Up to six lanes fused with RRF (K=60); `mode=pattern` runs an ast-grep query (needs `ast-grep` or `sg` on `PATH`) |
| `explain` | A file or directory's purpose, role, key symbols, and how to run or verify it | One repo-relative `path` |
| `impact` | Blast radius of the current changes, a symbol, or the path between two symbols | A lexical reference graph, up to 4 hops; edges at distance 1 or more are leads, not proof |
| `diagnostics` | Normalized errors and warnings from the latest stored run | Reads its baseline from Postgres; needs `postgres_dsn` in `settings.json` |
| `why_failed` | Error lines and implicated files for a run, a pasted log or an evidence handle | Reads a bounded 1 MiB tail and redacts it; a pasted log or an evidence handle records an outcome that `ground` then lists |

The schema stays lean. Advanced arguments are accepted but listed only in the full
schema profile, and documented at the MCP resource `xmustard://docs/tools`. A test
caps the `tools/list` result: 8,769 bytes for all nine tools in the lean profile on
protocol 2025-06-18 (`api-go/internal/mcpserver/testdata/tools_list_budget.json`).
`mode=readonly` lists seven tools and hides `remember` and `verify`.

## Shared memory that agents check

- **Peer verification by default.** A memory is promoted only after approvals from
  distinct principals other than its author. The default threshold is 2, so a
  peer-verified demo needs three tokens: one author and two verifiers.
- **An honest trust label.** Every recalled entry carries `verification_mode`:
  `peer_verified`, `single_agent` or `self_asserted_open_mode`. With no tokens minted,
  every caller is one identity and writes are labelled `self_asserted_open_mode`.
- **Drift checks on recall.** At verification, a promoted memory snapshots the content
  hashes of its anchored files. Recall compares them and marks the entry stale when a
  file appears, disappears or changes. Known issue in v0.1.0: some nested anchors,
  such as `pkg/auth.go`, record a missing baseline and are never flagged stale
  ([details](ARCHITECTURE.md#known-seams-in-v010)). Top-level paths work.
- **A full lifecycle.** Supersede, retire, retract, purge with a digest tombstone,
  expiry, compare-and-swap edits (`base_revision`) and history.
- **Provenance on every write.** Principal, owner, kind, session and call ids, HEAD
  and branch, plus any cited evidence handles, checked when cited.
- **Known secret shapes stay out.** Memory text is redacted before it is stored.
- **Explainable recall.** Ranking is deterministic, and `explain=true` returns score
  details. Filters, `names_only` and compact renders, signed cursors, and suppression of
  entries a session has already seen keep results small.
- **Durable storage.** One SQLite database in WAL mode (`governance.db`) with an
  append-only event log.

## Code intelligence

- **A real code index.** `index.db` streams one file at a time and indexes every
  eligible file within a declared envelope (by default 10,000 files and 100,000
  symbols); the legacy 800-file cap is gone. On macOS M1, a full build of `cline`
  (2,660 files, 51,263 symbols) peaked at 21.8 to 23.8 MiB RSS across the WS-07 runs,
  and a one-file update took 140 to 265 ms under shared load
  ([WS-07 record](benchmarks/2026-09-25-ws07-index-rss.md)); with the resolver added
  later, the peak was 24.2 to 25.7 MiB
  ([WS-17 record](benchmarks/2026-09-26-ws17-resolver.md)).
  Coverage losses are reported per file.
- **Thirteen languages.** Fifteen tree-sitter packs behind one contract: Go, Rust,
  TypeScript, TSX, JavaScript, Python, Java, Kotlin, C, C++, C#, Swift, Ruby, PHP and
  Bash. Coverage is reported per language in search, explain, impact and ground.
- **Typed edges for Go and TS/JS.** A scope and import resolver emits CALLS, USES,
  ACCESSES, EXTENDS, IMPLEMENTS, IMPORTS, HAS_METHOD and HAS_PROPERTY edges with
  confidence tiers, and counts what it cannot bind. On small purpose-written fixtures,
  30 of 30 emitted CALLS edges matched the type checkers, with recall 0.938
  ([WS-17 record](benchmarks/2026-09-26-ws17-resolver.md)). That is not a precision
  estimate for real repositories.
- **Hybrid search.** BM25 over function bodies, comments, names and paths; BM25 over
  doc sections; identifier match; trigram typo tolerance (not meaning); reference
  degree; and proximity to a seed symbol. Results carry reasons and up to three
  line-numbered snippets. The retrieval gate passes 21 of 21 checks on the release
  commit ([release notes](releases/v0.1.0.md)); its gold fixture is a small,
  hand-made repository of 14 files, not a real-repository benchmark.
- **Secret-aware search.** Secret paths such as `.env` and SSH keys are refused before
  their text is read, and snippets mask credential-shaped words.

## Grounding and failures

- **Budgeted orientation.** On a `pi-mono` clone with 60 files given a new leading
  parameter, `ground` returned 19,544 bytes unbudgeted and 4,550 bytes under the default
  budget, with all 200 contract breaks still counted
  ([WS-54 record](plans/2026-09-25-parity-build-plan.md)). On MCP, a budgeted result
  that leaves anything out keeps the full original behind a recovery handle, or says
  why it could not.
- **Failures without a platform run.** `why_failed` works from a pasted log or an
  evidence handle. Outcomes land in `governance.db` and feed
  `ground.recent_failed_runs`.
- **No surprise execution.** Command mode is off on every bind. It needs
  `XMUSTARD_WHY_FAILED_COMMANDS=1` at startup and an authenticated admin, and even then
  it is trusted host-code execution, not a sandbox.

## Evidence you can recover

- **Large results stay recoverable.** A projection that omits bytes carries a
  recovery handle, and `resources/read` pages the exact original.
- **Built, not yet live: reducers for native tool output.** Shell, test, build, lint,
  log, git, diff, grep, read, list, glob and structured. A golden test keeps one
  failing assertion among 5,000 passing tests (exit code, counts, failure header,
  assertion, first and last stack frames) inside 16 KiB and 4 KiB targets
  (`api-go/internal/evidence/reducers_test.go`). They act only on captured output,
  and v0.1.0 release builds refuse capture (below).
- **Limit: capture is refused in release builds.** `POST .../evidence/capture`
  answers `503 redaction_unavailable`, because no streaming secret redactor is wired
  into `captureRedactor` yet; only the `-tags xmustard_e2e` test build installs one.
  It fails closed on purpose, since captured originals are kept and searchable. The
  nine tools' own results are unaffected. The production redactor is the next fix,
  planned for v0.1.1.

## Transports and clients

- **One process for every agent.** The API serves MCP over Streamable HTTP at
  `http://127.0.0.1:8042/mcp`. A client that takes a URL needs no extra process.
- **A tiny relay for command-only clients.** `xmustard-relay` measured 2,192 to
  2,240 KiB RSS per agent on macOS arm64, against 13,824 to 13,888 KiB for the
  deprecated Go shim `xmustard-mcp`
  ([relay record](benchmarks/2026-09-26-ws13-relay-rss.md)).
- **Config in one command.** `xmustard-ops mcp-config --root /abs/repo` prints an
  `mcpServers` entry for HTTP or, with `--transport relay`, for the relay. The token is
  referenced as `${XMUSTARD_API_TOKEN}`, never written in.
- **Documented clients.** Claude Code, Codex, Pi and any client that reads
  `mcpServers` JSON. The labels `claude-code`, `codex`, `cursor`, `opencode`, `pi` and
  `letta` for `?client=` and `--client` only attribute usage.
- **Pi adapter.** An in-repo extension, pinned to Pi 0.87.1, registers the nine tools
  as direct HTTP calls plus `xmustard_expand` ([Pi README](../integrations/pi/README.md)).
  Its built-in tool reduction, masking and compaction need evidence capture. In v0.1.0
  release builds, reduction and compaction fall back to Pi's own behavior, and masking
  covers only results that already carry a handle.
- **Codex and OpenCode: MCP configuration only.** There is no Codex hook package or
  OpenCode plugin yet; `integrations/pi` is the only packaged adapter. The server
  already decodes Codex, Cursor and OpenCode hook bodies and shapes results for each
  (`api-go/internal/evidence/`), but that capture route answers 503 in release builds.
  Adapters for these clients are planned (WS-40).

## Security and governance

- Loopback by default; a non-loopback bind needs `XMUSTARD_AUTH=required`, minted
  tokens and TLS (or `XMUSTARD_ALLOW_INSECURE_BIND=1` behind a TLS proxy).
- Six roles gate every route: `admin`, `human-approver`, `indexer`, `verifier`,
  `proposer` and `reader`.
- An instruction-pattern scan labels recalled memory and every evidence projection.
  Memory that cites an untrusted capture is quarantined; in v0.1.0 release builds no
  such capture can exist yet, because capture is refused.
- Human approvals through `xmustard-ops approve|reject|queue`, and merge attestations
  bound to a reviewed diff and its base through `xmustard-ops review`. xMustard never
  merges.

The full posture is in [SECURITY](SECURITY.md).

## Footprint

- **Release gate.** Budget gate v2 (`--suite ci`) passed on the release commit on a
  Linux x86_64 build box: process-tree peaks of 70.5 MiB for the frozen v1 workload
  over the stdio shim, and 68.8 MiB for two agents through `xmustard-relay`, against a
  95.4 MiB line. The [release notes](releases/v0.1.0.md) are the only source for that run. The newest stored
  report, from 2026-09-27 on Linux, shows 62.0 and 66.0 MiB
  ([report](benchmarks/evidence/2026-09-28/ws18-gate-v2-ci-head.md)).
- **Not yet at parity scale.** The parity-scale suite (4 agents, 2 hot repositories,
  4 worktrees) has not passed. v0.1.0 is not a parity claim.
- **Not measured yet.** No token savings or task-success lift on real agent tasks has
  been measured. `xmustard-eval` can run paired arms with real clients, but no
  real-model run is recorded in the repository ([eval tasks](../eval/tasks/README.md)).

## Install

- **Prebuilt archives** for macOS arm64 and Linux x86_64, each with five binaries and a
  `.sha256` file. The macOS binaries carry only an ad-hoc signature (no Developer ID).
  On Linux, `xmustard-core` needs glibc 2.39 or newer (Ubuntu 24.04+ or Debian 13+).
- **From source** with `make build` (Go 1.26, Rust stable).
- **Homebrew.** `packaging/homebrew/xmustard.rb` builds the v0.1.0 tag from source.
  Current Homebrew installs formulae only from a tap, and there is no public tap yet.

## Planned, not in v0.1.0

| Item | Workstream |
|---|---|
| File watcher and incremental refresh (merged into `feat/parity-v2` after the tag, not released) | WS-15 |
| Hook service and Claude Code plugin (on branch `parity/ws-23`, not released) | WS-23 |
| Tag-triggered release workflow | WS-26 |
| Daemon lifecycle | WS-58 |
| `why_failed` command-mode hardening | WS-21B |
| Streaming redactor wired into evidence capture, which unblocks the native-output reducers and the Pi adapter's built-in reduction, masking and compaction | WS-05 follow-up, planned for v0.1.1 |
| Tiered memory and memory pushed into context | WS-31 |
| Static-embedding semantic lane | WS-37 |
| Codex, OpenCode and Cursor adapters | WS-40 |
| Parity evaluation suite and parity-scale gate | WS-50 |
| Diagnostics without Postgres | [local diagnostics plan](plans/2026-09-24-local-diagnostics.md) |

The [parity build plan](plans/2026-09-25-parity-build-plan.md) holds the scope and exit
gate of each workstream.

## Historical: feature specifications, April 2026

Everything below is kept as history. It describes the issue-tracker platform of April
2026, not the v0.1.0 product. Those routes exist only under
`XMUSTARD_PROFILE=platform`, and the migration in F15 is done: the Python backend was
retired in June 2026, and Go plus Rust is the architecture.

### F1: Issue Context Packets

Issue context packets are deterministic bundles used to start analysis and execution. They currently include:

- the selected issue
- workspace metadata
- tree focus
- ranked related paths
- evidence bundle
- recent fixes
- recent activity
- available runbooks
- available verification profiles
- repo-map summary
- worktree status
- repository guidance

Why this matters:

- it keeps runs grounded in issue evidence instead of a loose chat history

Next improvement:

- add symbol-level and enclosing-scope context so packets explain not only what is broken, but where the bug lives structurally

### F2: Planning-Gated Runs

Runs can enter a `planning` phase before execution. The system stores plans, supports approval and rejection, and lets the UI display plan state for live work.

What is already present:

- plan models and persistence
- approval and rejection endpoints
- planning-aware execution flow
- planning state shown in the UI

Next improvement:

- make plan review more opinionated with checklists for scope, verification, and rollback risk

### F3: Run Metrics And Cost Tracking

Each run can accumulate token and cost estimates, and the workspace aggregates those metrics.

What is already present:

- run metrics model
- workspace metrics endpoint
- workspace cost summary
- cost surfaced in the queue, detail views, and topbar

Next improvement:

- add budget policies, warnings, and governance rules by workspace or issue

### F4: Triage Analysis

xMustard evaluates issue quality and offers triage assistance.

What is already present:

- quality scoring
- duplicate detection
- triage suggestions
- queue and detail UI for analysis

Next improvement:

- create-time duplicate warnings and owner suggestions based on repo history

### F5: Verification Artifacts

xMustard stores signals about whether a proposed fix has meaningful proof.

What is already present:

- coverage deltas
- generated and saved test suggestions
- patch critique
- improvement suggestions
- review detail in the run pane
- per-workspace verification profiles with saved test and coverage commands
- verification profile editing and instruction loading in the execution drawer

Current state:

- replayable verification runs now record pass or fail history per profile
- confidence scoring and verification checklist results are now attached to profile runs
- profile reports now break verification history down by runtime, model, and branch
- saved eval scenarios now correlate replay drift, guidance/ticket-context variant drift, verification reports, and run metrics into workspace eval reports
- workspace eval reports now group outcomes by saved guidance sets and ticket-context sets, including runtime/model rollups for comparing context variants without double-counting duplicated runs
- scenario reports now compare saved variants to the issue baseline scenario, surfacing input deltas and a weighted preference summary across outcome quality, cost, and speed
- baseline comparisons now also break verification history down per saved profile, including success/checklist/attempt deltas and confidence-count differences between variants
- saved eval scenarios can now trigger fresh queued runs using their pinned context selections, and those new runs are written back into scenario history automatically
- saved eval scenario batches can now be replayed in one action, and eval reports now surface the latest fresh run plus fresh-vs-baseline execution deltas per scenario
- workspace eval reports now add a fresh replay ranking block that orders all latest scenario replays for the issue by pairwise outcome strength, cost, and speed
- workspace eval reports now add fresh replay trend entries that compare each scenario's current rank to its previous fresh replay snapshot
- replay batches now persist as durable tracker artifacts, and trend entries now use latest-batch vs previous-batch movement when replay history exists
- replay trend entries now expose explicit latest-batch and previous-batch ids, making experiment history inspectable in the UI and API

Next improvement:

- add multi-batch summaries and longer-running experiment timelines so teams can compare more than the latest two replay sessions

### F6: Repository Guidance

The system discovers repo-specific instruction files and attaches them to issue context and runs.

Currently supported:

- `AGENTS.md`
- `agents.md`
- `CLAUDE.md`
- `GEMINI.md`
- `CONVENTIONS.md`
- `.clinerules`

Current state:

- starter guidance generation and health checks now cover the always-on repo instruction files
- `.xmustard.yaml` can now define path-specific instructions, path filters, code-guideline references, and MCP/browser-context hints
- matched path instructions now attach directly to issue context packets and agent prompts using the issue's ranked paths
- `.devin/wiki.json`
- `.openhands/microagents/repo.md`
- `.openhands/microagents/**/*.md`
- `.openhands/skills/*.md`
- `.openhands/skills/**/*.md`
- `.agents/skills/*.md`
- `.agents/skills/**/*.md`
- `.cursor/rules/*.mdc`
- `README.md`

What is already present:

- backend discovery and summarization
- UI for viewing guidance
- onboarding warning when no guidance is found
- run metadata showing which guidance shaped execution
- starter guidance generation for `AGENTS.md`, `.openhands/microagents/repo.md`, and `CONVENTIONS.md`
- workspace guidance health that reports missing and stale starter guidance

Next improvement:

- guided customization flows that turn starter content into repo-specific instructions without leaving the app

### F7: Run Insights

Run insights are session-style summaries that explain what happened after a run.

What is already present:

- headline and summary
- strengths
- risks
- recommendations
- guidance-used list

Next improvement:

- add acceptance criteria compliance review, confidence explanations, and exportable run briefs

### F8: Repo Map And Dynamic Context

This is now partially implemented and is still one of the most important capabilities to deepen.

Inspired by:

- `research/aider`
- `research/pr-agent`

Planned behavior:

- build a repo map per workspace
- attach top directories, files, and symbols to issue context
- extend context around enclosing functions or classes instead of dumping large files

Expected impact:

- lower token usage
- stronger file targeting
- better review quality for large repos

Current state:

- workspace repo maps now persist top directories, extension mix, and notable files
- issue packets now include ranked related paths and structural prompt context
- the issue detail pane shows the repo-map summary the prompt is using
- the next depth increase is symbol-level and enclosing-scope context, not broader file dumping

### F9: Eval And Replay

This is also still ahead of us, but it should become a first-class feature rather than test-only scaffolding.

Inspired by:

- `research/qodo-cover`
- `research/aider`
- `research/SWE-agent`

Planned behavior:

- save replayable run artifacts
- define evaluation scenarios for common bug workflows
- compare cost, success, and verification quality across changes

Current state:

- issue-context prompt snapshots can now be captured and stored per issue
- replay records include tree focus, guidance paths, linked ticket context, verification-profile references, and browser-dump references
- saved replays can now be compared against the current issue-context packet to show prompt drift and added or removed context artifacts

### F10: Ticket Context And Acceptance Criteria

This is now implemented and is one of the most important context layers in the product.

Inspired by:

- `research/pr-agent`
- `research/openhands-resolver`

Planned behavior:

- attach upstream ticket links, acceptance criteria, and incident notes to issue packets
- preserve imported ticket context as a durable artifact
- show product expectations in run briefs and review surfaces

Expected impact:

- better scoping for fixes
- stronger review quality
- fewer runs that solve the code symptom but miss the user-facing expectation

Current state:

- issue-level ticket context records now persist upstream links, summaries, labels, and acceptance criteria
- GitHub issue imports seed ticket context automatically when issues are created
- issue prompts and detail views now expose that context directly to operators and runs

Next improvement:

- deepen Jira and Linear ingestion so non-GitHub workflows retain equally strong acceptance criteria and ticket metadata

### F11: Threat Modeling And Security Review

This should become a first-class issue artifact instead of living outside the workflow.

Planned behavior:

- attach threat models to issues and runs
- record assets, trust boundaries, abuse paths, and mitigations
- add security acceptance criteria and review checkpoints
- surface security risks in run insights and export packets

Expected impact:

- higher trust for risky fixes
- better support for auth, data, and infra-sensitive work
- a clearer answer to whether a change alters the system threat profile safely

Current state:

- issue-level threat models now persist assets, entry points, trust boundaries, abuse cases, mitigations, and references
- threat models are included in issue-context packets and prompts
- the issue detail pane now supports creating, reviewing, and deleting threat-model artifacts

### F12: Confidence And Ticket Compliance

The system should explicitly state how sure it is and whether the change satisfies the upstream request.

Planned behavior:

- run-level confidence scoring
- verification confidence per saved profile
- compliance review against ticket acceptance criteria
- unrelated-change and scope-drift warnings

Expected impact:

- fewer ambiguous handoffs
- better operator trust in review artifacts
- stronger product fit for issue-driven engineering teams

Current state:

- run insights now include acceptance-criteria review and scope warnings derived from ticket context and worktree drift
- patch critique now carries the same compliance summary for stored run output

### F13: Semantic Retrieval And Issue Intelligence

Inspired by the hybrid search and context retrieval direction used by tools like Linear.

Planned behavior:

- hybrid semantic and keyword retrieval across issues, runs, ticket context, comments, and review artifacts
- better clustering of related issues and prior runs
- retrieval that uses ticket context and repo-map structure together

Current state:

- issue packets now attach lexical, artifact-backed related context from ticket contexts, threat models, browser dumps, fixes, and recent activity
- symbol-aware dynamic context now highlights likely functions, classes, and methods around ranked focus paths

Expected impact:

- faster triage
- better reuse of prior run evidence
- less operator time spent reconstructing context manually

### F14: Agent Operations And Governance

To compete with tools that are becoming team platforms, xMustard needs stronger operational visibility.

Planned behavior:

- agent identity and ownership history on runs
- dashboards for runtime success, cost, and verification quality
- audit logs for approvals, executions, and sync actions
- policy gates for sensitive workflows and runtime choice

Expected impact:

- safer team adoption
- better cost and quality management
- clearer accountability for delegated work

### F15: Backend Migration To Rust

The backend should move toward a compiled core over time, without breaking the current UI or evidence model. The current experiment in this repo is a Rust core paired with a possible Go API shell.

Planned behavior:

- preserve the current API and data contracts while isolating domain services
- migrate scanning, repo-map generation, search, and verification execution first
- evaluate replacing the Python orchestration layer with Go or Rust after the core services are stable

Expected impact:

- better performance and process control
- easier distribution as the product grows
- a cleaner long-term systems boundary for runtime-heavy workflows
