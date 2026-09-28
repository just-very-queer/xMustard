# Documentation map

xMustard's docs fall into three groups. **Start here** explains what the product is
and what v0.1.1 ships. **Reference** holds the details you look up while working.
**History** keeps earlier reviews, plans and research; each one describes the project
when it was written, not today's behavior.

## Start here

Read these in order.

| Read | For |
| --- | --- |
| [README](../README.md) | What xMustard is, how to install it, and how to connect an MCP client |
| [v0.1.1 release notes](releases/v0.1.1.md) | What the current release fixes and adds, how to install and upgrade, and its known limits |
| [v0.1.0 release notes](releases/v0.1.0.md) | What the first tagged release contained, and its limits at the time |
| [Status](STATUS.md) | What ships, measured numbers, known limits, and work in progress |
| [Vision](VISION.md) | Product scope, constraints, and open choices |
| [Architecture](ARCHITECTURE.md) | Which module owns a behavior |
| [AGENTS.md](../AGENTS.md) | Working rules and checks for agents and contributors |

## Reference

| Topic | Read |
| --- | --- |
| What v0.1.1 ships per feature, what backs it, and what is still planned | [Features](FEATURES.md) |
| Roles, route gates, exposure posture, registration, `why_failed` command mode, injection safety, capture redaction, client hooks, the service unit | [Security](SECURITY.md) |
| Advanced tool arguments and hidden aliases | The MCP resource `xmustard://docs/tools`, generated from `api-go/internal/mcpserver` |
| Claude Code plugin and hook events | [integrations/claude-code](../integrations/claude-code/README.md) |
| Pi extension | [integrations/pi](../integrations/pi/README.md) |
| Retrieval gate and budget gate v2 | [scripts/bench](../scripts/bench/README.md) |
| Evaluation corpus and the `xmustard-eval` executor | [eval/tasks](../eval/tasks/README.md) |
| Active execution plan, with implementation records per workstream | [Parity build plan](plans/2026-09-25-parity-build-plan.md) |
| Requirements behind that plan | [Parity requirements](research/PARITY_REQUIREMENTS_2026-09-25.md) |
| Measurements by workstream | [WS-07 index RSS](benchmarks/2026-09-25-ws07-index-rss.md), [D-02 FTS5 vs tantivy](benchmarks/2026-09-25-d02-fts5-vs-tantivy.md), [WS-13 relay RSS](benchmarks/2026-09-26-ws13-relay-rss.md), [WS-14 resident index](benchmarks/2026-09-26-ws14-resident-index.md), [WS-16 language packs](benchmarks/2026-09-26-ws16-language-packs.md), [WS-17 resolver](benchmarks/2026-09-26-ws17-resolver.md), [WS-18 hybrid search](benchmarks/2026-09-28-ws18-hybrid-search.md), [WS-15 watcher](benchmarks/2026-09-28-ws15-watcher.md) |
| Release history | [Changelog](CHANGELOG.md) |
| Tool-output reduction and assurance design, dated 2026-09-24; its status lines predate the release | [Context layer](CONTEXT_LAYER.md) |
| Competitor comparison, dated 2026-09-24 | [Competitor parity](research/COMPETITOR_PARITY_2026-09-24.md); refresh first-party evidence before relying on it |

## History

Earlier material. Use it for context and decisions, not as a description of current
behavior.

- **Project history and session library:** [history/README.md](history/README.md), with
  [status 2026-06](history/status-2026-06.md), [roadmap 2026-06](history/roadmap-2026-06.md)
  and [architecture before 2026-09-24](history/architecture-before-2026-09-24.md).
- **Status before the release:** the 2026-09-24 pre-release status is in Git, at
  `git show v0.1.0:docs/STATUS.md`.
- **Roadmap before the release:** [Roadmap](ROADMAP.md), dated 2026-09-24. The parity
  build plan above replaced it as the active plan.
- **2026-09-24 baseline audits and pre-release reviews:** [Go audit](reviews/2026-09-24-go-audit.md),
  [Rust audit](reviews/2026-09-24-rust-audit.md),
  [runtime smoke](reviews/2026-09-24-runtime-smoke.md),
  [Go results](reviews/2026-09-24-implementation-results.md),
  [Rust results](reviews/2026-09-24-rust-implementation-results.md),
  [Pi results](reviews/2026-09-24-pi-implementation-results.md),
  [final source review](reviews/2026-09-24-fable-code-review.md), and the other
  `reviews/2026-09-24-*` files.
- **Project assessment:** [2026-09-25 full assessment](reviews/2026-09-25-full-project-assessment.md).
- **Pre-release benchmark:** [lean context, 2026-09-24](benchmarks/2026-09-24-lean-context.md),
  with its evidence under `benchmarks/evidence/2026-09-24/`.
- **Earlier plans:** [lean context implementation](plans/2026-09-24-lean-context-implementation.md),
  [local diagnostics](plans/2026-09-24-local-diagnostics.md), and the April to June
  plans in [plans/](plans/).
- **Earlier research:** from 2026-09-24, [tool context](research/TOOL_CONTEXT_RESEARCH_2026-09-24.md)
  and [Jev](research/JEV_RESEARCH_2026-09-24.md); from June 2026, the
  [research index](research/RESEARCH_INDEX.md), [market scout](research/MARKET_SCOUT_2026-06.md)
  and [Twitter scan](research/TWITTER_SCAN_2026-06.md).
- **Earlier top-level docs:** [Rethink](RETHINK.md), [Planned features](PLANNED_FEATURES.md),
  [Planning](PLANNING.md), [Benchmarks](BENCHMARKS.md),
  [Goal runtime](GOAL_RUNTIME.md), [Index engine](INDEX_ENGINE.md),
  [Frontier](FRONTIER.md), [Build log](BUILD_LOG.md),
  [GitNexus extraction map](GITNEXUS_EXTRACTION_MAP.md),
  [Rust/Go migration](MIGRATION_RUST_GO.md),
  [Python to Rust migration](PYTHON_TO_RUST_MIGRATION.md),
  [Repo cockpit architecture](REPO_COCKPIT_TOOL_ARCHITECTURE.md),
  [Research findings](RESEARCH_FINDINGS.md) and [Research matrix](RESEARCH_MATRIX.md).
- **Prompts and goal files:** `prompts/` and the root `goal/` directory.

## Document ownership

- `STATUS.md` records what ships and what was measured, with limits beside the passes.
- `releases/` holds one page per tagged release.
- `CHANGELOG.md` summarizes each release and links its page.
- `VISION.md` records accepted product decisions and open choices.
- `ARCHITECTURE.md` describes module ownership and execution paths.
- `SECURITY.md` owns roles, gates and exposure posture.
- `plans/2026-09-25-parity-build-plan.md` owns the workstreams and their records.
- `benchmarks/` holds dated measurements with their evidence.
- `reviews/`, `research/` and `history/` hold dated material tied to its source and
  scope.

New files under `docs/` are ignored unless `.gitignore` allowlists them. Raw
conversations, runtime records, provider credentials and downloaded research stay out
of the public documentation set.
