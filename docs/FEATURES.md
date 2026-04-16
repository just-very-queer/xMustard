# Feature Specifications

This file describes xMustard as it exists now and the next research-backed extensions we want to add.

## F1: Issue Context Packets

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

## F2: Planning-Gated Runs

Runs can enter a `planning` phase before execution. The system stores plans, supports approval and rejection, and lets the UI display plan state for live work.

What is already present:

- plan models and persistence
- approval and rejection endpoints
- planning-aware execution flow
- planning state shown in the UI

Next improvement:

- make plan review more opinionated with checklists for scope, verification, and rollback risk

## F3: Run Metrics And Cost Tracking

Each run can accumulate token and cost estimates, and the workspace aggregates those metrics.

What is already present:

- run metrics model
- workspace metrics endpoint
- workspace cost summary
- cost surfaced in the queue, detail views, and topbar

Next improvement:

- add budget policies, warnings, and governance rules by workspace or issue

## F4: Triage Analysis

xMustard evaluates issue quality and offers triage assistance.

What is already present:

- quality scoring
- duplicate detection
- triage suggestions
- queue and detail UI for analysis

Next improvement:

- create-time duplicate warnings and owner suggestions based on repo history

## F5: Verification Artifacts

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

## F6: Repository Guidance

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

## F7: Run Insights

Run insights are session-style summaries that explain what happened after a run.

What is already present:

- headline and summary
- strengths
- risks
- recommendations
- guidance-used list

Next improvement:

- add acceptance criteria compliance review, confidence explanations, and exportable run briefs

## F8: Repo Map And Dynamic Context

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

## F9: Eval And Replay

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

## F10: Ticket Context And Acceptance Criteria

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

## F11: Threat Modeling And Security Review

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

## F12: Confidence And Ticket Compliance

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

## F13: Semantic Retrieval And Issue Intelligence

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

## F14: Agent Operations And Governance

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

## F15: Backend Migration To Rust

The backend should move toward a compiled core over time, without breaking the current UI or evidence model. The current experiment in this repo is a Rust core paired with a possible Go API shell.

Planned behavior:

- preserve the current API and data contracts while isolating domain services
- migrate scanning, repo-map generation, search, and verification execution first
- evaluate replacing the Python orchestration layer with Go or Rust after the core services are stable

Expected impact:

- better performance and process control
- easier distribution as the product grows
- a cleaner long-term systems boundary for runtime-heavy workflows
