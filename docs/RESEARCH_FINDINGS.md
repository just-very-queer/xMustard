# Research Findings

This document captures what the local research repos under `research/` suggest xMustard should keep, copy, or build next.

For a repo-by-repo breakdown, see [docs/RESEARCH_MATRIX.md](./RESEARCH_MATRIX.md).

## What Repeats Across Repos

### 1. Repository instructions are first-class

Strong examples:

- `research/OpenHands/skills/add_repo_inst.md`
- `research/OpenHands/openhands/resolver/README.md`
- `research/OpenHands/AGENTS.md`
- `research/cline/CHANGELOG.md`

Pattern:

- successful systems do not rely on generic prompting alone
- they ingest repo-specific instructions early
- they treat those instructions as always-on context for runs

What xMustard should do:

- detect and show repo instruction files
- generate starter `AGENTS.md` or repo microagent drafts
- store which guidance files shaped each run

### 2. Context should be curated, not huge

Strong examples:

- `research/pr-agent/docs/docs/core-abilities/dynamic_context.md`
- `research/aider/README.md`

Pattern:

- PR-Agent uses asymmetric, dynamic context around the changed code
- Aider uses a repo map instead of blindly stuffing the entire repository into context

What xMustard should do:

- build a repo-map artifact for each workspace
- attach focused structural context to issue packets
- rank guidance, files, and symbols by relevance instead of breadth

### 3. Verification loops are a product feature

Strong examples:

- `research/qodo-cover/README.md`
- `research/aider/README.md`

Pattern:

- generation alone is not trusted
- systems validate using coverage, tests, replay, and linting
- verification outputs are stored as durable artifacts

What xMustard should do:

- make test commands and verification profiles explicit per workspace
- support replayable verification runs
- keep verification evidence alongside fixes and run reviews

### 4. Review artifacts matter after execution

Strong examples:

- `research/pr-agent/docs/docs/tools/review.md`
- `research/pr-agent/docs/docs/tools/improve.md`

Pattern:

- useful systems produce summaries, critiques, suggestions, and checklists after a run
- this helps humans accept, reject, or refine output quickly

What xMustard should do:

- continue investing in patch critique and improvement suggestions
- add compact acceptance checklists for runs and fixes
- tie review artifacts to issue resolution history

### 5. Ticket context matters as much as code context

Strong examples:

- `research/pr-agent/docs/docs/core-abilities/fetching_ticket_context.md`
- `research/openhands-resolver/README.md` (canonical resolver home; OpenHands removed its in-tree `openhands/resolver/` during the 2026-06 refresh)

Pattern:

- leading systems pull in upstream ticket detail, acceptance criteria, and related discussion
- code changes are judged against the product expectation, not just local code symptoms

What xMustard should do:

- attach imported ticket or incident context to issue packets
- preserve acceptance criteria as a durable artifact
- show external context in run briefs and review decisions

### 6. Benchmarks and replays keep the product honest

Strong examples:

- `research/aider/benchmark/README.md`
- `research/qodo-cover/README.md`
- `research/SWE-agent`

Pattern:

- leading repos track reproducible tasks, outcomes, and evaluation harnesses
- replay and benchmark data are part of product development, not an afterthought

What xMustard should do:

- add eval scenarios for issue-context quality, plan quality, and verification success
- preserve replayable run outputs for regression testing
- report success rate and cost per workflow type

### 7. Security trust artifacts need their own lane

Strong examples:

- `research/vulnhuntr/README.md`
- `research/cline/docs/enterprise-solutions/configuration/infrastructure-configuration/control-other-cline-features/mcp-marketplace.mdx`
- `https://owasp.org/www-project-threat-dragon/`
- `https://github.com/OWASP/pytm`
- `https://semgrep.dev/docs/semgrep-code/overview`
- `https://docs.github.com/en/code-security/concepts/code-scanning/about-code-scanning`

Pattern:

- serious systems separate "it works" from "it is safe"
- threat modeling, exploitability review, policy controls, and transparent security findings all become durable artifacts
- governance and allowlists matter once teams use agent tooling operationally

What xMustard should do:

- keep issue-level threat models as first-class artifacts
- attach exploit paths, trust boundaries, and mitigations to issue packets and review flows
- pair threat modeling with security review signals from verification and code scanning
- add policy and audit surfaces as the product becomes more team-facing

## What xMustard Has Already Adopted

- planning checkpoints
- run cost metrics
- issue quality scoring and duplicate detection
- verification artifacts and coverage deltas
- workspace verification profiles
- issue-level ticket context with acceptance criteria
- issue-level threat models
- issue-context replay snapshots
- saved eval scenarios, fresh replay batches, baseline comparisons, and replay trend movement
- workspace repo-map summaries with ranked related paths
- repo guidance discovery
- starter guidance generation and guidance health for `AGENTS.md`, `.openhands/microagents/repo.md`, and `CONVENTIONS.md`
- repo-native `.xmustard.yaml` path instructions and MCP/browser-context hints
- run insights and review summaries
- Go/Rust migration boundaries for scanner, repo-map, verification, runtime, API, and integration surfaces

## Highest-Value Next Steps

1. Add a retrieval ledger for issue context packets, so selected files, symbols, guidance, and operational artifacts carry inspectable match reasons.
2. Deepen the repo map with enclosing-scope context and symbol-level explanations inspired by Aider and PR-Agent.
3. Extend eval reporting from latest-vs-previous replay batches into longer multi-batch timelines with movement explanations.
4. Deepen the security lane with vulnerability disposition, exploitability, risk acceptance, verification linkage, and exportable security review packets.
5. Add workspace policy records for runtime choice, required verification, budget warnings, and sensitive-workspace controls.
6. Add provider-grade review/export packets for GitHub, Linear, Jira, and human PR-style consumption.

See [docs/FRONTIER.md](./FRONTIER.md) for the current research-to-build frontier map.

## Refresh Log

Research repos were re-pulled on **2026-06-16** (`git fetch` + `pull --ff-only`,
non-destructive). All 22 cited evidence paths were re-verified against the new
checkouts; one stale citation was corrected (OpenHands removed its in-tree
`openhands/resolver/`, so the resolver finding now points at the standalone
`research/openhands-resolver/` repo).

Pinned commits after the refresh:

| Repo | Commit | Date | Refresh |
|------|--------|------|---------|
| `aider` | `5dc9490` | 2026-05-22 | fast-forwarded |
| `auto-code-rover` | `585d3e63` | 2025-04-24 | up-to-date (upstream quiet) |
| `cline` | `81384089` | 2026-06-15 | fast-forwarded |
| `gitnexus` | `df08ecc3` | 2026-06-15 | fast-forwarded |
| `openhands-resolver` | `baa2f45` | 2024-11-18 | up-to-date (archived) |
| `OpenHands` | `f941ba5` | 2026-06-15 | fast-forwarded (resolver extracted) |
| `pr-agent` | `31d7dd02` | 2026-06-06 | fast-forwarded |
| `qodo-cover` | `adcf55b` | 2025-06-23 | up-to-date (upstream quiet) |
| `SWE-agent` | `c53556f` | 2026-06-10 | fast-forwarded |
| `trIAge` | `5c12e53` | 2023-07-17 | up-to-date (archived) |
| `vulnhuntr` | `ead88c5` | 2025-02-06 | up-to-date (upstream quiet) |

The repeating patterns above (repo instructions, curated context, verification
loops, review artifacts, ticket context, eval/replay, security lanes) held
across the refreshed checkouts; no finding was invalidated by newer upstream
code.
