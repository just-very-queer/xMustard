# Changelog

Notable changes to xMustard. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.0.0/). Each tagged release also has
its own page under [releases/](releases/v0.1.1.md).

## [Unreleased]

Merged into `feat/parity-v2` after v0.1.1 and not yet released. Branches still under
review are listed in [Status](STATUS.md#in-progress).

## [0.1.1] - 2026-09-28

Fixes v0.1.0's two release-affecting bugs and adds the Claude Code plugin, the index
watcher, a background service with store backup, and file-level `impact`. Cut from
`feat/parity-v2` at `1c64982`, with the v0.1.0 docs refresh merged in. Full notes,
measurements, upgrade steps and known limits:
[releases/v0.1.1.md](releases/v0.1.1.md). This is not a parity claim.

### Fixed
- Evidence capture works in release builds, and every capture is redacted (WS-FIX-03).
  v0.1.0 wired no redactor, so `POST .../evidence/capture` answered
  `503 redaction_unavailable` and Pi's built-in reduction, masking and compaction fell
  back to Pi's own. A streaming redactor (`redact.Writer`, 0.4 MiB live heap at most on
  16 MiB of pure secrets) now runs before any byte is retained.
- Drift is checked at every path depth: a memory anchored to `pkg/auth.go` or any
  other even-depth path was baselined as missing and never flagged stale (`7d380c4`).
- Argument errors reach the agent as `isError` results with a hint and the tool's
  arguments, where clients used to show "Empty response" (WS-FIX-05).
- Client adapter corrections (WS-FIX-04): `client=claude-code` gets Claude's shapes, an
  `"error": null` member no longer marks a hook body failed, image blocks are refused
  instead of dropped, and the Codex hook fields match what Codex sends.
- A SIGTERM right after start-up no longer skips the API's drain (WS-58).

### Added
- Claude Code plugin (`integrations/claude-code`) and hook service (WS-23): 15 hook
  events; PostToolUse replaces large native outputs with redacted, shape-matched
  reductions behind recovery handles; pre-tool hooks add index hits and human-approved
  memory; no hook allows, denies or rewrites a tool call, and the daemon starts no
  process for one. Static command-hook client `xmustard-hook`.
- File watcher and incremental refresh loop for the code index (WS-15), inside the
  opt-in resident worker (FSEvents on macOS, inotify on Linux). On a 5,000-file tree
  an edit reaches the index in p50 424 ms on Linux and 488 ms on macOS in the
  in-process loop test, and 497 ms on Linux through the API. On an unchanged tree,
  ground's drift check hashes no files.
- `xmustard-ops setup|uninstall|daemon` installs the API as a launchd agent or systemd
  socket and service with no credential in the unit, and `store backup|check|restore`
  backs up, checks and restores the governance store; the API checks its store at
  start-up and the relay waits up to 10 s for a restarting API (WS-58).
- `impact(path=)`, a file's blast radius (WS-FIX-05).
- `xmustard-ops mcp-config --client codex` prints Codex's `config.toml` table; one
  shaper per client on the capture route (WS-FIX-04).
- Tag-triggered release workflow (`.github/workflows/release.yml`), `make release` and
  `make release-sums`, and the Homebrew bump helper `packaging/homebrew/bump.sh`
  (WS-26).
- Review finding anchoring and a findings store behind the `review` build tag, off in
  release builds (WS-65, WS-66); a merge attestation citing a review record written by
  its approver is refused.
- The `parity-v1` evaluation corpus: eight tasks with hidden oracles, stale-memory and
  adversarial-injection fixtures (WS-63).

### Changed
- An unknown tool is a JSON-RPC `-32602`, no longer an `isError` result.
- `tools/list` grew by 119 bytes for `impact`'s `path` argument (8,888 B for all nine
  tools, lean profile, protocol 2025-06-18).
- Capture refuses a secret path (`422 secret_path`), a failed redactor
  (`503 redaction_failed`) and an unknown client (`400 invalid_client`); the 16 MiB
  original limit applies to the redacted bytes.
- The governance store migrates to schema version 3, which v0.1.0 cannot open.
- `codex_args` for platform Codex runs is an allow-list.
- Memory guard tests bound dirty memory strictly and RSS by build profile (WS-FIX-06);
  if/else ladders became tables and small steps, with byte-identical reducer outputs
  and unchanged goldens (WS-CQ-02); WS-26 ran `gofmt` and `rustfmt` in
  formatting-only commits, and the Go sources are `gofmt`-clean.

### Known limits
- The Pi adapter e2e passes 9 of its 18 tests: nine need a multi-page `impact` result
  that a freshly baselined fixture no longer produces (a WS-24 follow-up).
- The Claude Code plugin's hook client is not in the release archive; build it from a
  source checkout. Codex, OpenCode and Cursor get MCP configuration only.
- The parity-scale suite has not passed its 95.4 MiB line. The full list is in the
  [release notes](releases/v0.1.1.md#known-limits).

## [0.1.0] - 2026-09-28

First tagged release: one small, local MCP server that gives coding agents repository
grounding, code search and peer-checked shared memory through nine tools. Cut from
`feat/parity-v2` at `9086dab` and tagged at `8fca2d5`. Prebuilt archives for macOS
arm64 and Linux x86_64 are on the
[GitHub release](https://github.com/just-very-queer/xMustard/releases/tag/v0.1.0).
Full notes, install steps and known limits: [releases/v0.1.0.md](releases/v0.1.0.md).
This is not a parity claim.

### Added
- **MCP and transport.** A dedicated MCP server package on protocol 2025-06-18, still
  nine tools. Streamable HTTP at `/mcp` on the API, and `xmustard-relay`, a std-only
  stdio relay (about 2 MiB RSS, against about 14 MiB for the Go shim). Lean `tools/list`
  under per-profile byte caps, with advanced arguments documented at
  `xmustard://docs/tools`. An output budget for `ground` (`sections`, `max_chars`).
  `xmustard-ops mcp-config` prints a client entry without writing the token in it.
- **Storage and runtime.** govstore, a SQLite (WAL) governance store, with legacy JSON
  memory imported. A resident Rust worker with a Go supervisor (opt-in). A budget
  governor with a heavy slot and health counters.
- **Code intelligence.** The `index.db` code index with per-language coverage and a
  declared envelope in place of the old 800-file cap (10,000 files and 100,000 symbols
  by default; the index reports what falls outside), fifteen language packs, a Go and
  TS/JS scope and import resolver with typed edges, a resident index service, and a
  BM25 code lane with hybrid RRF fusion, snippets, reasons and signed cursors.
- **Governed memory.** Lifecycle operations (supersede, retire, retract, purge, expiry,
  compare-and-swap edits, history), provenance and evidence-bound votes, recall v2
  (ranking with reasons, filters, budgets, verification-queue views), a human-approval
  surface (`xmustard-ops approve|reject|queue`, `xmustard-ops review approve|revoke|gate`)
  and an injection-safety policy.
- **Evidence and failures.** Bounded tool results with recoverable handles for the
  nine tools. Evidence reducers per tool family for captured native output, which
  release builds refuse for now (see Known limits). `why_failed` from an evidence
  handle or a pasted log, with outcomes in `ground.recent_failed_runs`; command mode is
  off by default. A governed automatic index baseline.
- **Security.** Roles and route gates, a core-only exposure default, scoped workspace
  auto-registration and a shared redaction library.
- **Integrations, evaluation and CI.** The Pi extension, which registers the nine tools
  as direct HTTP calls plus `xmustard_expand`. Its built-in tool projection, masking and
  compaction rely on capture, so in release builds projection and compaction fall back to
  Pi's own behavior and masking covers only results that already carry a handle.
  The `xmustard-eval` outcome executor. Budget gate v2, the retrieval gate and the
  `check` CI workflow.

### Removed
- Unreachable Rust code (WS-25, PAR-RT-08): the `swarm` and `bench` subcommands with `rust-core/src/swarm.rs` and `rust-core/src/benchmark.rs`; the `lsp-references`, `lsp-definition`, `lsp-implementation`, `lsp-type-definition` and `lsp-rename` subcommands with their `lsp_session` functions; the `symbolgraph build-lsp` and `symbolgraph flow` subcommands; and port-era scaffolding with no caller (`initial_*_plan`, `run_migration_verification`, `indexcache::file_hash`). Nothing in api-go, the Makefile or scripts/ called them. `upgrade_graph_with_lsp`, `LspWorkspaceSession`, `wiki.rs` and `goalruntime.rs` stay. `rust-core/go-calls.txt` lists every core call reachable from an api-go main. It also lists the four commands kept without one: `lsp-hover`, `parse-coverage-lcov` and `run-verification-command`, whose Go wrappers nothing calls, and `semantic-search`, which has no Go wrapper. `TestCoreCallManifestMatchesGoSources` holds the file to the Go sources. The core's `go_called_commands_exist` test fails when a listed command leaves the table. `TestEveryCoreSubcommandGoCallsExists` runs each call against a core that cargo has just checked, or the one `XMUSTARD_CORE_BIN` names. The release binary is 114 KB smaller (macOS arm64). Resident memory did not change (measured on pi-mono).

### Measured
- Budget gate v2 (CI suite, Linux x86_64): 70.5 MiB for the frozen v1 workload and
  68.8 MiB for 2 agents through `xmustard-relay`, against a 95.4 MiB line.
- Retrieval gate: 21 of 21 checks.

### Known limits
- Native-output capture is off in release builds, on purpose. `POST .../evidence/capture`
  answers `503 redaction_unavailable` because no streaming secret redactor is wired into
  `captureRedactor`; only the `-tags xmustard_e2e` test build installs one. The Pi
  extension's built-in projection and compaction therefore fall back to Pi's own
  behavior, its masking covers only results that already carry a handle, and a client
  hook adapter that posts native output would get the same refusal. The production
  redactor is the next fix, planned for v0.1.1.
- Codex and OpenCode connect through MCP configuration only. There is no Codex hook
  package or OpenCode plugin yet.
- A memory anchored to an even-depth path such as `pkg/auth.go` is never flagged stale
  when that file changes.
- The full list is in the [release notes](releases/v0.1.0.md#known-limits).

---

## Pre-release history (untagged)

The entries below predate the first tag. Their version labels were never Git tags.
They describe the product as it was then, including the issue-tracker platform, the
retired Python backend and a larger MCP surface, and are kept as history.

## Untagged work, April to June 2026 (formerly "Unreleased")

### Changed
- Goal logic is now owned solely by the Rust core: `api-go/internal/workspaceops/goals.go` deletes its duplicated implementation (~326 lines) and becomes a thin delivery shim that validates the workspace and delegates create/list/get/iterate/status/ledger/context to `xmustard-core goal` via `rustcore.RunGoalCommand`. One source of truth instead of two parallel implementations; lifecycle + parity tests run the real Rust binary.
- `docs/RESEARCH_FINDINGS.md` and `docs/RESEARCH_MATRIX.md` now reflect shipped guidance, eval, verification, vulnerability, and Go/Rust migration work instead of treating those lanes as still missing.
- `docs/PLANNING.md` now tracks the next strategic lanes more explicitly:
  - threat modeling and security review
  - confidence and ticket compliance
  - semantic retrieval and issue intelligence
  - agent operations, insights, and governance
  - incremental backend migration away from Python toward a Rust-based core
- `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, and `README.md` now reflect that xMustard is moving toward stronger trust, retrieval, governance, and backend-platform planning instead of simply expanding UI surface area
- backend migration planning now explicitly tracks a Rust core plus a possible Go API shell instead of assuming the HTTP layer must stay in Python until the very end
- the no-Python migration target now has a durable architecture contract, explicit three-surface agent model, and a completed `external_integrations_gateway` cutover on the FastAPI request path
- FastAPI no longer registers the integration config/test/sync endpoints, leaving those existing route paths under Go ownership while Python retains only non-request-path compatibility helpers

### Added
- **OpenAI-compatible provider integration** (`api-go/internal/workspaceops/openai_providers.go`): access any OpenAI `/v1` endpoint — Ollama, vLLM, LM Studio, OpenAI — plus vision/VLM via `image_url` content parts. Provider CRUD, `/models`, probe, `/chat/completions` (text + image parts, local-file→data-URL). Security-first: API keys are never stored — a provider records the env-var *name* and the key is read from the process env at call time. `/api/providers*` endpoints + MCP tool `provider_chat`. Verified live against a running Ollama (`/v1/models`, probe ok).
- **Context governance / multi-agent verification gate** (`api-go/internal/workspaceops/context_governance.go`): the trust layer for the MCP context engine. Agents PROPOSE entries with a `readonly`/`readwrite` permission; an entry is promoted into the active shared context only once ≥N **distinct** agents verify it. A `require_multi_agent_verification` toggle (+ per-proposal override) runs context through multiple agents or not; readonly+verified entries reject edits. `/context*` endpoints + MCP `context_propose`/`context_verify`/`context_active`. New settings: `require_multi_agent_verification`, `context_verification_threshold`.
- **Task-typed model routing** (`provider_router.go`): classifies a request into a coding sub-task (locate / code_edit_patch / multi_step_debug_reason / repo_qa_explain / test_gen_validate / vision_ui_diagnose — taxonomy grounded in the downloaded research) and resolves it to a provider+model by explicit rule or capability (vision→supports_vision provider, code→coder model). `/api/route`, `/api/route/chat`, `/api/routes` + MCP `route_model`.
- **Verified context injected into agent runs**: `applyActiveContextToPrompt` prepends the multi-agent-approved shared context to the prompt on both run paths (`StartIssueRun`, `StartAgentQuery`), so every run grounds on the same trusted facts.
- MCP server grew from 27 to **32 typed tools** (`provider_chat`, `context_propose`, `context_verify`, `context_active`, `route_model`).

### Security
- **Bearer-token authentication** (`auth.go` + `authMiddleware`): per-principal tokens (role `admin`/`agent`/`readonly`) hashed (sha256) at rest, minted via `xmustard-api mint-token <id> <role>` or `XMUSTARD_AUTH_TOKENS` env, resolved with constant-time compare. The MCP server sends its agent's `XMUSTARD_API_TOKEN`, so the **multi-agent verification gate now uses the authenticated principal as the agent identity** — one token cannot satisfy the gate as N fake agents. Token admin endpoints (`/api/auth/*`) are admin-gated; `readonly` principals can only GET; server-config mutations (settings/providers/routes) require `admin`. Modes via `XMUSTARD_AUTH` (`auto`/`required`/`off`); a non-loopback bind fails closed without auth **and** TLS (`XMUSTARD_API_TLS_CERT`/`KEY`, or explicit `XMUSTARD_ALLOW_INSECURE_BIND=1`). An adversarial review of the auth code found 7 issues, all fixed: `AUTH=off` no longer bypasses the non-loopback interlock; in open mode all unauthenticated callers collapse to one `anonymous` identity (gate stays bypass-proof); a proposer's own vote no longer counts toward a multi-agent threshold; env tokens must clear a 24-char entropy floor.
- Hardened the new provider + context-governance surface after an adversarial review found real issues: (a) the per-request `require_verification` override may now only TIGHTEN the multi-agent gate, never loosen it (closes a single-agent context-poisoning bypass); (b) provider `base_url` SSRF guard blocks link-local / cloud-metadata IPs (169.254.169.254, fd00:ec2::254, 100.100.100.200) at dial time and does not follow redirects, while still allowing loopback/RFC1918 for local model servers; (c) `workspace_id`/`entry_id` are validated against path traversal; (d) the verify endpoint only treats a literal `"true"` as approval; (e) the API binds `127.0.0.1` by default (opt out via `XMUSTARD_API_HOST`). Known limitation: the API still has no auth layer, so agent identity in the verification gate is caller-asserted — multi-agent governance assumes a trusted local network.
- `docs/research/` market scout (Sonnet agent in tmux) + live-web supplement (June 2026) + `RESEARCH_INDEX.md` mapping 12 downloaded arXiv papers (SWE-bench, SWE-agent, Agentless, OpenHands, RAPTOR, HippoRAG, CodeRAG-Bench, RepoGraph, multi-agent debate, LLM-as-judge, MemGPT, RAG) to xMustard subsystems; PDFs under `docs/research/papers/` (gitignored, kept on disk). Session narrative in `docs/BUILD_LOG.md`.
- Rust goal-runtime CLI (`rust-core/src/goalruntime.rs`, `xmustard-core goal create|list|get|iterate|status|ledger|context|lint`): wire-compatible with the Go shell over the same `goals.json`/iterations/ledger files, with a closed `GoalStatus` enum, atomic temp+fsync+rename writes, `#![forbid(unsafe_code)]`, and an anti-AI-slop linter that refuses empty/refusal content and gates completion on verifiable evidence.
- `/swarm` scaffold in Rust (`rust-core/src/swarm.rs`, `xmustard-core swarm plan|status|gate|record`): role-tagged reader/builder/critic/verifier lanes plus a deterministic controller gate (`accept | block | narrow | complete`) over goal iterations.
- Benchmark harness (`xmustard-core bench`) and `docs/BENCHMARKS.md`: reproducible goal/swarm throughput + p50/p99 latency, with peak RSS ~15 MB under bulk load (under the 50 MB runtime budget).
- `TestGoalRustWireParity` (`api-go`): round-trips a goal through both the Go shell and the Rust CLI in both directions, the safety check for moving goal-logic authority to the Rust core.
- Research frontier map in `docs/FRONTIER.md`, turning the local research synthesis into current build lanes for retrieval, symbol-aware context, eval timelines, security review depth, policy records, and review packet export.
- Issue context packets now include a retrieval ledger that explains selected evidence, related paths, symbols, related artifacts, guidance, and path-specific instructions in both Python and Go context builders, with the ledger surfaced in the issue detail UI and prompt.
- Go-owned project truth now covers the Phase 4 runtime/project-discovery closeout: `xmustard-ops` exposes `project-info`, `run-targets`, `verify-targets`, `verification-outcomes`, `verification-profile-save`, and `verification-profile-run`, with raw target ownership, truth-source, coherence, overlay, freshness, and related-target fields.
- `project-info` now carries richer runtime/config/service graph truth, including package workspaces, `go.work` groups, Go `cmd/*` entrypoints, Cargo bin entrypoints, compose relationships, Vite proxy edges, and package workspace dependency edges only when backed by manifest/config/entrypoint evidence.
- Cargo bin run targets now split into distinct service identities, while package-wide Cargo verification stays shared-scope and links back to the proven bin run targets.
- ADR `docs/plans/2026-04-18-no-python-control-plane-adr.md` locking in the no-Python target architecture: Go control-plane shell, Rust runtime/retrieval/store core, sub-500MB steady-state target, and the three agent surfaces (`works with agents`, `works within agents`, `commands agents`)
- Rust-owned architecture contract in `rust-core/src/contracts.rs` plus `xmustard-core describe-architecture`
- Go-served architecture and agent-surface inventory endpoints at `/api/migration/plan`, `/api/migration/agent-surfaces`, and `/api/agent/surfaces`
- Go-owned plugin-manifest registry plus provider integration config/test/sync routes for GitHub, Slack, Linear, and Jira, including durable activity and ticket-context artifacts behind the existing integration endpoints
- issue-level vulnerability import batch persistence via `vulnerability_import_batches.json`, including source/scanner provenance, imported finding ids, payload hash, and lifecycle summary counts for `new` / `existing` / `resolved` / `regressed`
- issue-level vulnerability import batch inspection through Python service, FastAPI route `/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-import-batches`, and matching CLI command `vulnerability-import-batches`
- vulnerability import activity now records the durable import batch id alongside SARIF and Nessus imports
- repo-native `.xmustard.yaml` support for path-specific instructions, code-guideline references, MCP/browser-context hints, and issue-context prompt attachment
- Go parity for `.xmustard.yaml` repo-config reads and issue-context prompt attachment, including matched path instructions inside Go-built issue packets
- Issue-level threat model artifacts with backend CRUD, prompt integration, export support, and issue detail editing
- Research notes now include external security/trust references such as OWASP Threat Dragon, OWASP pytm, Semgrep Code, GitHub code scanning, and Vulnhuntr patterns
- Workspace metadata under `backend/data/` now points the self-workspace at `/Users/for_home/Developer/xMustard`
- `.gitignore` coverage for local mirrors, generated run logs, and OS junk
- initial migration working note in `docs/MIGRATION_RUST_GO.md`
- initial `rust-core/` scaffolding for scanner, repo-map, verification, and contract boundaries
- initial `api-go/` scaffolding for the future HTTP/API shell
- route-group migration inventory parity test in `backend/tests/test_api_route_inventory.py`
- Rust scanner CLI plus Python parity and adapter-path tests
- Rust repo-map CLI plus Python parity and adapter-path tests
- Rust coverage parsers for LCOV, Cobertura, and Istanbul plus Python parity and adapter-path tests
- Rust verification command runner plus Python parity and adapter-path tests
- Rust verification profile runner with retries and optional coverage artifact parsing
- Go migration endpoints for scanner, repo-map, and generic coverage parsing
- Go migration endpoint for verification command execution
- Go migration endpoint for verification profile execution
- issue-level verification profile execution endpoint and tracker persistence for coverage/activity artifacts
- Go API shell support for the issue-level verification profile run workflow with snapshot/activity/coverage writes
- Go API shell support for coverage parse, latest coverage lookup, and coverage delta from persisted tracker artifacts
- Go API shell support for verification profile list/save/delete with persisted settings activity
- Go API shell support for ticket-context list/save/delete with persisted issue activity
- Go API shell support for threat-model list/save/delete with persisted issue activity
- Go API shell support for runbook list/save/delete with persisted settings activity
- Go API shell support for browser-dump list/save/delete with persisted issue activity plus issue-context/export inclusion
- Go API shell support for issue-context packet reads, issue-work packet reads with runbook selection, and issue-context replay list/capture against persisted tracker artifacts
- Go API shell support for workspace snapshot reads, activity feeds, activity overview, sources, tree browsing, guidance discovery, and repo-map reads against persisted workspace artifacts
- Go API shell support for issue queue reads, issue drift reads, signal queue reads, and workspace drift summary reads against persisted workspace snapshots
- Go API shell support for issue create/update and saved-view CRUD against persisted tracker artifacts with matching activity records
- Go API shell support for live run cancel/retry, run plan generate/read/approve/reject, run listing/detail/log reads, run review submission, run acceptance, run insights, metrics/cost reads, critique generation/read, improvement dismissal, fix listing/recording, fix-draft generation, verification listing, and review-queue reads against persisted run and tracker artifacts
- Go API shell support for runtime listing, settings reads/writes, local agent capability reads, workspace runtime probe flows, issue-run creation, and workspace query runs against the existing settings and run artifact contracts
- Go API shell support for workspace listing, cached workspace load, worktree reads, export bundle reads, and terminal open/write/resize/read/close transport against the existing workspace registry and terminal log contracts
- Go API shell support for fresh workspace scan/load and explicit `/scan` snapshot rebuilds using Rust-backed signal and repo-map generation plus Go-side tracker artifact assembly
- Python CLI parity expansion for verification profiles, ticket context, threat models, context replays, plan/metrics/coverage/critique flows, integrations, repo inspection, and terminal transport
- Durable browser-dump artifacts for MCP/manual browser debugging context, now attached to issue context packets and export bundles
- Guidance authoring workflow with starter generation for `AGENTS.md`, `.openhands/microagents/repo.md`, and `CONVENTIONS.md`, plus workspace guidance health reporting
- Issue-context replay comparison across prompt content, tree focus, guidance, verification profiles, ticket contexts, and browser dumps, now surfaced in the API, CLI, and issue detail UI
- Verification-profile checklist items, confidence scoring, and durable per-profile execution history, now surfaced in Python, Go, CLI, and the issue detail UI
- Verification-profile reports now break execution history down by runtime, model, and branch, with matching Go API ownership and issue detail UI summaries
- Dynamic issue context now includes symbol ranking and retrieval-backed related artifacts from ticket context, threat models, browser dumps, fixes, and activity
- Eval scenarios and workspace eval reports now correlate replay drift, verification success, and run metrics through the Python API and CLI
- Eval scenarios now persist saved guidance-path and ticket-context variant selections, and eval reports now expose current-vs-saved variant drift summaries across both Python and Go
- Eval workspace reports now group outcomes by saved guidance sets and ticket-context sets with unique-run rollups, deterministic Go parity, and selected-value/cost detail surfaced in the UI
- Eval scenario reports now include baseline-aware comparisons for the same issue, surfacing guidance/ticket/browser/profile deltas and weighted preference reasons without adding new eval routes
- Eval baseline comparisons now include per-profile verification history deltas across saved variants, including runs, success rate, checklist pass rate, attempt count, and confidence-count changes in both Python and Go
- Saved eval scenarios can now launch fresh queued runs through the normal issue-run flow, with scenario overlays applied to prompt context and new run IDs appended back into the scenario record
- Eval scenarios can now be batch replayed per issue, and eval reports now include each scenario's latest fresh run plus fresh execution comparisons back to the issue baseline scenario
- Eval workspace reports now rank fresh replay outcomes across all saved scenarios for an issue, using pairwise fresh-run wins/losses/ties plus deterministic cost and duration tie-breaks in both Python and Go
- Eval workspace reports now show fresh replay rank movement versus the previous fresh replay snapshot for each scenario across Python, Go, and the UI
- Eval replay batches now persist as durable artifacts, and batch-backed trend comparisons now prefer latest-batch vs previous-batch movement before falling back to per-scenario fresh history
- Replay trend views now expose explicit latest-batch and previous-batch ids, making batch history inspectable across Python, Go, and the UI
- The Go API shell now owns eval scenario CRUD and workspace eval-report reads, with route registration moved into a dedicated registrar instead of extending the main HTTP mux tree inline
- Run insights and patch critique now include acceptance-criteria review plus scope/unrelated-change warnings derived from ticket context and worktree state

## 2026-04-14 (untagged; labelled 0.2.0 at the time)

### Added
- Repository guidance discovery for:
  - `AGENTS.md`
  - `CONVENTIONS.md`
  - `.devin/wiki.json`
  - `.openhands/skills/*.md`
  - `.agents/skills/*.md`
  - `.cursor/rules/*.mdc`
- OpenHands-style repo microagent support for `.openhands/microagents/repo.md` and related markdown files
- Run insight summaries that capture guidance used, strengths, risks, and recommendations
- Workspace guidance API and run insight API
- Workspace verification profile API for saved test and coverage commands
- Ticket-context API for issue-level upstream references and acceptance criteria
- Issue-context replay API for saved prompt snapshots
- Repo-map API for workspace structural summaries
- UI sections for repository guidance and run insights
- UI sections for verification profiles in the execution drawer and issue detail
- UI section for ticket context editing and inspection in issue detail
- UI section for issue-context replay capture and inspection
- UI section for repo-map summaries and ranked related paths
- Guidance onboarding in the sidebar, topbar, and workspace empty state
- Research synthesis document: `docs/RESEARCH_FINDINGS.md`
- Repo-by-repo research matrix: `docs/RESEARCH_MATRIX.md`
- First-party repo guidance files:
  - `AGENTS.md`
  - `.openhands/microagents/repo.md`

### Changed
- Scanner behavior now avoids noisy self-referential matches from research and generated directories
- Planning, cost, triage, verification, and review features are now reflected in the UI much more completely
- Verification commands are now treated as saved workspace artifacts instead of ad hoc operator text
- Imported issue references are now normalized into durable ticket-context records instead of being lost in sync metadata
- Issue prompts now include structural repo-map context and ranked related paths
- `docs/PLANNING.md`, `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, and `README.md` now reflect current implementation instead of the original sketch roadmap
- Frontend effect wiring in `App.tsx` was tightened to keep lint clean while guidance and insight state updates stay stable

## 2026-04-10 (untagged; labelled 0.1.0 at the time)

### Added
- Project renamed from Co_Titan_Bug_Tracker to **xMustard**
- Branch: `alpha.test.001.ideation.architecture.o1`
- Research folder with 11 cloned reference repositories:
  - OpenHands, SWE-agent, AutoCodeRover, Aider, cline
  - pr-agent, qodo-cover, trIAge, vulnhuntr, openhands-resolver, auto-code-rover
- New documentation:
  - `docs/ARCHITECTURE.md` - Updated architecture with new features
  - `docs/PLANNING.md` - Implementation roadmap
  - `docs/FEATURES.md` - Detailed feature specifications
- **Phase 2: Planning Checkpoint System** (backend complete, UI pending):
  - `PlanPhase`, `PlanStep`, `RunPlan` models
  - `planning` status for runs
  - Plan generation, approval, rejection logic
  - API endpoints: `/runs/{id}/plan`, `/plan/approve`, `/plan/reject`
  - Approval gate in runtimes with `wait_for_approval` support
  - Frontend types and API functions for plan handling

### Changed
- README.md - Updated with new project name, features, and research references
- package.json - Renamed to `xmustard-ui`
- pyproject.toml - Renamed to `xmustard-backend`
- WorkspaceSidebar.tsx - Updated branding to "xMustard Bug operations"
- App title changed to "xMustard"

### Planned Features (Phase 2+)
- [ ] F1: Planning checkpoint system - UI integration (Phase 2.2)
- [ ] F2: Cost tracking
- [ ] F3: Issue quality scoring
- [ ] F4: Coverage verification
- [ ] F5: Post-run review artifacts
- [ ] F6: Multi-agent orchestration
- [ ] F7: GitHub integration
- [ ] F8: Slack integration

---

## Previous (Co_Titan_Bug_Tracker)

See git history for previous changelog entries.
