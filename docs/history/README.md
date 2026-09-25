# Project history and conversation index

Reviewed on 2026-09-24. This is an index of decisions and historical evidence,
not a second current roadmap. Start with [vision](../VISION.md),
[status](../STATUS.md), and [roadmap](../ROADMAP.md) for today's direction.

## Review coverage

The review covered the substantive conversation text in all **62 available
archived Codex transcripts**: 60 parent sessions and two subagent sessions,
created May 1–11, 2026. The files total **66,728,345 bytes** and contain
**26,292 valid JSONL records**. Their local filenames use the original machine's
time zone; associated handoff notes extend through May 12.

After excluding repeated repository instructions, inter-agent notification
wrappers, and interruption markers, the reviewed text contains **995 messages**:
69 user-role messages and 926 assistant messages, totaling 591,673 characters.
User-role messages include the initial tasks in the two subagent transcripts.
All JSONL records were parsed; this does **not** mean every tool response or
duplicated event payload received a line-by-line technical audit. The 148
inter-agent notifications were counted separately from the main conversations.

Additional local material was inventoried: 55 archived handoff notes, 60 saved
execution prompts, the June goal/audit artifacts, and the matching Claude project
library. Relevant handoffs, plans, and audit records were reviewed; the inventory
counts do not imply every historical prompt was independently re-audited.

Two older Codex entries remain in the session index but have no available
transcript or projected conversation items in the inspected local stores:

| Local date | Session ID | Available evidence |
| --- | --- | --- |
| 2026-04-29 | `019dd57c-d63e-7291-ab08-aedbfa1d23f6` | Metadata for a runtime JSON canary |
| 2026-06-18 | `019edb50-b7dd-75a1-84ac-9e3fa22a2852` | Metadata for a three-surface feature audit |

The matching Claude project contains eight historical project notes plus their
index, all reviewed. Its only available transcript is a September 24 exit stub;
the June conversation referenced by those notes is unavailable. These notes are
secondary historical evidence, not a substitute for the missing conversation.
No claim is made to have read unavailable sessions or unrelated project chats.
The available archive starts during implementation and closeout work; it does
not establish complete coverage of the project's original brainstorming.

## Decisions and progression

| Period | What the conversations establish | Evidence pointer |
| --- | --- | --- |
| May 1–2 | Structural intelligence, retrieval provenance, impact context, semantic baseline freshness, and durable plan/run/fix evidence were central. Early closeout loops repeatedly checked an externally dirty validation repository. | `019de3ab-a16f-7311-8231-5ea5a0124826`; `019de407-7e34-7da0-ba35-7af11ce4ee93`; `019de455-f07c-75f1-9708-12a532f9cdbb` |
| May 2 | The owner explicitly closed Phase 2 and removed external repository dirtiness as a gate. This supersedes earlier handoffs that keep that gate open. | `019de79c-e299-7d10-8496-8d5f4d17e188`, user message at JSONL line 6 |
| May 2–3 | Go/Rust ownership replaced Python authority across semantic, Postgres, CLI, and repository-intelligence paths. Migration completion was distinct from runtime/process-plane completion. | `019de99d-eeea-7452-896a-b0563b365cd7`; `019dedc0-261e-7a33-945f-1447aaf9eb6e`; `019deec3-c468-7c90-95b3-f8bc9fd2256b` |
| May 3–6 | The owner corrected an overloaded phase name: the original roadmap's Phase 3 meant LSP and diagnostics, while some migration work had also been called Phase 3. Live definitions, symbols, references, diagnostics, durable replay evidence, and validated run linkage followed. | `019deeed-97c0-7733-ab37-9fd3c7c589db`, user message at line 6; `019dfdd5-dedd-7c02-99eb-1717dc2c915d`; `019dfde8-f312-7050-a7dd-7964450ecd55` |
| May 7–8 | Deterministic project truth became an explicit product principle: CLI first, JSON first, one owner, declared/static truth separate from runtime observations. Verification outcomes must say when a command has never been observed. Python mirrors were to be retired once callers and parity were proved. | `019e0311-a937-7df0-ae08-36df46718f0a`; `019e0349-83bc-7442-bac8-d2552be693b7`; `019e0389-4a7e-70e0-9ad1-5d2b0b95f5f8`; `019e06ea-d641-7bd0-bd25-bc766133b0dc` |
| May 8–12 | Phase 4 developed manifest-backed run/verify discovery, service identities, workspace groups, explicit ambiguous ownership, and snapshot/live/overlay freshness. Cargo binaries finally received separate service ownership. | `019e08e9-5b44-71f3-9045-56734da0de91`; `019e0d21-0891-7a63-a042-e5e89fe034a3`; `019e17d4-cd11-7220-8358-fccc11a3b86d` |
| June | The project expanded through goal/swarm, provider, graph, memory, and cockpit work, then narrowed its agent-facing focus to governed memory and grounding. Later hardening records require concrete runtime evidence and a measured value comparison. | Local `docs/RETHINK.md`, `docs/plans/2026-06-21-remaining-work-loop.md`, and `docs/plans/xmustard_review_deliverables/` |
| September 24 | The owner confirmed shared verified memory and repository intelligence for existing coding agents, with UI outside the current focus. | [Current vision](../VISION.md) |

## Ideas and boundaries to preserve

- **Evidence before conclusions.** Retrieval provenance, baseline identity,
  freshness, and coverage must travel with an answer. A stored symbol result is
  not automatically a live LSP result; an archived payload is not executable
  replay of the entire historical repository.
- **Link work to outcomes.** Plans, runs, fixes, verification, review, and
  diagnostics should retain their relationships so later agents can inspect why
  a decision or completion claim was accepted. The May 1 requests explicitly
  include changes since the last run and since the last accepted fix, plus a
  retrieval ledger explaining why evidence was selected (`019de455…`, line 6).
- **One owner for each answer.** Historical requests consistently assign Go
  delivery/orchestration and Rust semantic meaning. Postgres was justified by
  durable evidence needs, not required on every transient query path.
- **Repository evidence over guessing.** Use manifests, entrypoints, commands,
  configuration, and observed execution before adding markdown or filename
  heuristics. Preserve `unknown`, `unowned`, `shared_scope`, and `ambiguous` when
  evidence cannot establish an exact answer.
- **Reuse established boundaries.** Extend a proven session or contract instead
  of creating overlapping owners. Remove compatibility logic after classifying
  callers and proving the replacement.
- **Keep scope explicit.** The May passes repeatedly excluded UI redesign,
  speculative orchestration, and unrelated runtime work. Those were bounded
  implementation requests; the current product scope is set by the vision.
- **Finish against an acceptance bar.** The owner's May 11 instruction was:
  “do not keep Phase 4 open just because an edge case could be improved someday.”
  Conversely, historical “done” labels apply to their named slice, not overall
  product readiness or current benchmark/resource guarantees.

## Where the local library lives

These relative paths describe the existing local library; raw sessions and most
old prompts are intentionally excluded from Git and may be absent in a clone.

- `docs/history/SESSION_CATALOG.local.md`: local-only catalog with one row per
  transcript, dated topics, and clickable transcript/handoff paths. It is ignored
  by Git and absent from a fresh clone; the original files remain in place.
- `archive/codex-sessions/`: the 62 original JSONL files. Match a session ID to
  the filename suffix; do not export raw conversations into tracked docs.
- `archive/*session*.md`: 55 handoff notes. Useful indexes, but later user
  decisions and current source can supersede their conclusions.
- `docs/prompts/`: 60 dated execution prompts, primarily April–May phase work and
  the June 15 goal-runtime slice.
- `goal/`: the earlier post-hardening audit against commit `422bbaf`.
- `docs/plans/xmustard_review_deliverables/`: the June 23 audit, findings, program,
  and append-only evidence ledger. Read the inspected commit and later records.
- Preserved documentation snapshots: [architecture](architecture-before-2026-09-24.md),
  [June status](status-2026-06.md), and [June roadmap](roadmap-2026-06.md).

The June evidence ledger already outruns its own closeout document: the appended
`P1-B-WIRE` record reports budget charging in bridge and provider paths, whereas
`EVIDENCE_GATED_PASS_STATUS.md` still lists that wiring as remaining. Current
source and fresh checks decide which claim holds. The latest ledger still leaves
whole-process resource proof and the executed memory-value experiment open;
statistical helpers alone do not supply the task corpus or experimental results.
