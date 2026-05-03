# Research Matrix

This matrix is based on the cloned repositories under `research/` in this workspace.

## Repo-By-Repo Takeaways

| Repo | Local evidence | What xMustard should borrow |
|------|----------------|-----------------------------|
| `OpenHands` | `AGENTS.md`, `.openhands/microagents/`, `.agents/skills/`, `skills/add_repo_inst.md` | repo instructions, microagent authoring, cross-repo testing patterns, operator-facing setup guidance |
| `aider` | `README.md`, `benchmark/README.md` | repo map, git-aware editing loop, always-run lint/test checks, benchmark mindset |
| `pr-agent` | `AGENTS.md`, `docs/docs/core-abilities/dynamic_context.md`, review/improve docs | dynamic context windows, ticket-aware review, post-run critique and acceptance framing |
| `qodo-cover` | `README.md`, `docs/repo_coverage.md`, `tests_integration/README.md`, `stored_responses/` | verification profiles, replayable integration runs, coverage-driven workflows |
| `SWE-agent` | `trajectories/README.md`, `config/README.md`, `.cursor/rules/` | trajectory persistence, eval harnesses, structured task config, repo rules |
| `cline` | `.clinerules`, `.agents/skills/`, `.claude/`, `.codex/`, `evals/README.md` | repo instruction surfaces, nested skill folders, eval culture, approvals and tooling ergonomics |
| `auto-code-rover` | `README.md`, `conf/`, `results/` | structure-aware planning, staged execution, result tracking |
| `trIAge` | `README.md` | issue quality control, duplicate hints, test-case generation from issue text |
| `openhands-resolver` | `README.md`, `prompts/repo_instructions/` | issue-resolution packaging, repository instruction prompts, provider-facing handoff |
| `vulnhuntr` | `README.md` | targeted deep analysis workflows for specialized issue classes, exploitability reasoning, confidence-scored security findings |

## Concrete Product Gaps Still Visible

### 1. Starter guidance authoring

Seen in:

- `research/OpenHands/skills/add_repo_inst.md`

xMustard status:

- guidance discovery exists
- onboarding warning exists
- starter generation exists for `AGENTS.md`, `.openhands/microagents/repo.md`, and `CONVENTIONS.md`
- guidance health can report missing and stale starter surfaces

Remaining gap:

- guided customization should help operators turn starter files into repo-specific instructions without leaving the app

### 2. Repo map and structural context

Seen in:

- `research/aider/README.md`
- `research/pr-agent/docs/docs/core-abilities/dynamic_context.md`

xMustard status:

- issue context packets exist
- structural repo maps and ranked related paths exist
- symbol ranking and retrieval-backed related artifacts exist
- enclosing-scope context and inspectable "why this symbol/file" explanations are still shallow

### 3. Replay and eval infrastructure

Seen in:

- `research/qodo-cover/tests_integration/README.md`
- `research/qodo-cover/stored_responses/`
- `research/SWE-agent/trajectories/README.md`
- `research/aider/benchmark/README.md`
- `research/cline/evals/README.md`

xMustard status:

- activity and run artifacts exist
- issue-context replays exist
- saved eval scenarios exist
- fresh replay batches, baseline comparisons, and rank movement reports exist

Remaining gap:

- multi-batch scenario timelines and movement explanations are still thin

### 4. Verification profiles

Seen in:

- `research/qodo-cover/docs/repo_coverage.md`
- `research/qodo-cover/cover_agent/settings/README.md`

xMustard status:

- coverage/test suggestion artifacts exist
- saved per-workspace verification commands exist
- replayable profile outcomes, checklist results, confidence scoring, and runtime/model/branch reports exist

Remaining gap:

- longer-running verification confidence trends should be tied more directly to eval timelines and review packets

### 5. Provider-style review packets

Seen in:

- `research/pr-agent/README.md`
- `research/openhands-resolver/README.md`

xMustard status:

- critique and improvements exist
- run insights, ticket context, threat models, verification records, fixes, and export bundles exist

Remaining gap:

- a first-class PR-style issue review packet should combine these artifacts into one handoff

### 6. Threat modeling and exploitability review

Seen in:

- `research/vulnhuntr/README.md`
- `https://owasp.org/www-project-threat-dragon/`
- `https://github.com/OWASP/pytm`
- `https://semgrep.dev/docs/semgrep-code/overview`
- `https://docs.github.com/en/code-security/concepts/code-scanning/about-code-scanning`

xMustard status:

- issue-level threat models now exist as durable artifacts
- prompts can now carry assets, trust boundaries, abuse cases, and mitigations
- vulnerability findings, import batches, lifecycle summaries, and issue-level security reporting exist in the backend lane

Remaining gap:

- deeper analyst disposition, exploitability, risk acceptance, suppression, verification linkage, and exportable security review packets are still missing

## Recommended Build Order

1. Retrieval ledger and symbol-aware dynamic context
2. Multi-batch eval and verification timelines
3. Threat-model, vulnerability, and security-review depth
4. Runtime, verification, and budget policy records
5. Review packet export
6. Guided guidance customization

For a build-oriented version of this list, see [docs/FRONTIER.md](./FRONTIER.md).
