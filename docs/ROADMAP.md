# Current work and acceptance gates

Updated 2026-09-24 against `cd13e2b`. Scope follows the owner-confirmed
[vision](VISION.md). [Status](STATUS.md) separates fresh checks from historical
claims; [competitor research](research/COMPETITOR_PARITY_2026-09-24.md) supplies
comparison targets, not a union of every competing product's features.

## Implementation candidate snapshot (2026-09-24)

The reviewed candidate is imported into the main working tree, remains
uncommitted, and is not shipped. Its implementation acceptance gates pass; human
review of the exact diff and merge decision remain. It repairs audited Go
contract and Rust indexing seams, adds bounded/recoverable own-MCP evidence, and
adds one pinned Pi extension. The evidence is deliberately narrower than this
roadmap's goals:

| Evidence | Current result | Limit / open gate |
| --- | --- | --- |
| MCP evidence lifecycle | Main-worktree provenance-bound run: 23/23 | Scoped to tested MCP contracts; no gateway or other-client parity implied |
| Retrieval regression fixture | Main-worktree provenance-bound run: 12/12 gold cases across cold, warm and edit states | Internal lexical-leaning regression only; not held-out quality, task success or competitor parity |
| Rust indexing | Fable Recheck 2 approved source hash `d039d76d…7a358`; main-worktree `make check-backend` passes with 11 existing Clippy warnings | Broad language/repository coverage and task-quality parity remain open |
| Pi adapter | Main-worktree pinned Pi runtime E2E + unit checks pass (15 + 17) on current Rust core; all nine tools exercised with disposable PostgreSQL diagnostics fixture | One adapter only; fixture does not establish default DB dependency or SQL correctness; not native built-in tools or general client parity |
| Sampled process-tree memory | Main-worktree fixed workload passes 19/19 at 80.6 MB (853 valid samples); two same-source candidate runs pass at 72.3 MB and 84.9 MB | One machine/workload and sampled peaks only—not a universal ceiling, task-quality or hardware-bandwidth result |

See the [resource benchmark](benchmarks/2026-09-24-lean-context.md),
[main RSS](benchmarks/evidence/2026-09-24/main-rss.json),
[main MCP](benchmarks/evidence/2026-09-24/main-mcp.json),
[main gold](benchmarks/evidence/2026-09-24/main-gold.json), and
[main Pi E2E](benchmarks/evidence/2026-09-24/pi-e2e-summary-main.json). The
benchmark records the fixture, sampler, expansion workload, and provenance.

See the [Go](reviews/2026-09-24-implementation-results.md),
[Rust](reviews/2026-09-24-rust-implementation-results.md), and
[Pi](reviews/2026-09-24-pi-implementation-results.md) reports. A successful
MCP/Pi fixture or unit suite does not substitute for the remaining gates below.

## 1. Make the existing agent contract dependable

| Work | Completion evidence |
| --- | --- |
| Fix dirty→dirty cache invalidation and robust Git status parsing | Repeated edit, rename, deletion, unusual filename, branch-switch and untracked-path tests show fresh results or explicit unsupported coverage |
| Separate index completeness from display limits | A symbol past position 32 is retrievable; truncated/skipped/unsupported input is reported accurately |
| Use bounded recall for the no-query MCP path | Large-history HTTP+MCP test returns a limited ranked set with bounded work; administrative full reads stay explicit |
| Keep trust metadata and content from the same version | Forced concurrent revision/recall never returns unverified text as verified |
| Join shutdown and propagate request cancellation | Real subprocess fixture stops, releases resources, persists coherent state, and flushes required work before the API exits |
| Enforce auth on supported remote binds | Complete loopback/non-loopback, auto/required/off, configured/unconfigured-token and TLS matrix passes |

The original baseline evidence is in the [Go](reviews/2026-09-24-go-audit.md) and
[Rust](reviews/2026-09-24-rust-audit.md) reports. Candidate repairs and their
verification limits are recorded in the implementation reports above; do not
carry forward baseline findings as current failures without checking those
reports, or treat a repaired fixture as broad reliability proof.

## 2. Prove the default operating profile

- Apply enforced admission to concurrent retrieval, decode, provider and child
  capture paths. Bound inner Rust/ast-grep work as well as the outer Go response.
- Define additional repo sizes, languages, client count, concurrency, and
  cold/warm workloads; the candidate fixed workload now passes the sampled gate
  across API, MCP shims and spawned processes. Measure other representative
  profiles against the roughly 50–100 MB target; include overload, cancellation
  and recovery.
- Measure allocation/copy churn, files/bytes read and reparsed, context retained
  and delivered, CPU and latency. Hardware memory bandwidth needs separate
  platform-specific profiling; RSS and prompt tokens are not substitutes.
- Make source installation and data locations reproducible; package optional SQL
  correctly. Verify registration in actual supported coding-agent clients.
- Run the skipped live Postgres mirror tests when that optional deployment is in
  scope. A no-Postgres smoke result is not proof of mirror correctness.
- Consolidate local/unpublished work with current GitHub branches through a
  reviewed integration path; introduce repeatable CI and a versioned release.
  This review does not authorize or perform a push or merge.

## 3. Deliver the context layer in stages

The owner has accepted all three workflows: concurrent agents in one repo,
switching clients/sessions, and sharing knowledge across authorized repositories.
The proposed [context layer](CONTEXT_LAYER.md) adds compression and an optional
small-model helper while retaining human final merge authority.

| Stage | Deliverable | Exit evidence |
| --- | --- | --- |
| Observe and retain | Scoped raw tool observations, exact source identity, actual usage/outcome records, bounded storage | Every reduced projection resolves to an authorized original within its declared retention period; denied/expired references fail explicitly; original errors remain visible |
| Deterministic reduction | Bounded projections of current xMustard results; repetition removal and relevant-span selection | Paired raw/reduced tasks retain failures and key evidence; expansion, cancellation, latency, tokens and RSS measured |
| One real client adapter | Version-pinned native result hook or supported MCP route (candidate: Pi extension, plus xMustard MCP resources) | Main-worktree pinned Pi runtime E2E proves the next request receives projection and reachable expansion using scripted local model transport. Other clients and native host tools remain future work |
| Shared operation | Concurrent agents and cross-session handoff through one persistence authority | Distinct identities, revision-bound verification, restart/concurrent-mutation tests; no promotion of altered content |
| Cross-repo reuse | Explicit sharing grants and per-repository applicability/freshness | Facts from repo A cannot silently acquire authority in repo B; grants, revocation and source changes tested |
| Optional helper | Needle-like local selection or an explicitly opted-in hosted decision adapter | Improves evidence selection/context load or relevant task outcomes while meeting quality and resource gates; hosted models are not a local-default dependency |

In parallel with deterministic delivery, deepen the existing Rust index for
incremental, source-exact retrieval: changed-file work, shared snapshot identity,
bounded disk-backed storage and honest coverage. Measure files/bytes reparsed per
edit and query; calling a repeated filesystem scan an index does not meet this
gate. The owner's Cursor bridge is a reference for normalization/compaction and
client ownership, not proof that the desired persistent index already exists.

Use mitmproxy for scoped inspection when needed; evaluate LiteLLM and other
gateways/compressors by their actual integration seam. The owner confirmed
[LiteLLM AI tools](https://docs.litellm.ai/docs/ai_tools),
[mitmproxy](https://www.mitmproxy.org), and
[Typesafe Jev](https://typesafe.ai/blog/introducing-system-one-models-and-jev)
as intended references. [Jev research](research/JEV_RESEARCH_2026-09-24.md)
distinguishes its hosted interface from a local model. Pi is confirmed as the
lightweight-harness reference. No proxy, certificate, provider configuration,
or model installation is changed by this research.

The human approves the exact change before merge. Supporting agent reviews,
tests and memory votes do not substitute for that decision. Design enforcement
with repository permissions and revision-bound approval records as part of the
first workflow that prepares mergeable changes.

## 4. Establish competitor parity on representative tasks

| Capability | Comparison families | What to measure |
| --- | --- | --- |
| Repository search and current code context | GitNexus, Serena, Augment, Sourcegraph | Localization recall/precision, useful snippets, language coverage, edit/branch freshness, cold/warm latency and resource use |
| References and impact | GitNexus, Serena, Sourcegraph | Correct dependency paths and callers, explicit heuristic vs LSP provenance, omitted-symbol honesty |
| Shared memory and history | Mem0, Letta, Zep/Graphiti | Cross-session recovery, scope isolation, version/provenance inspection, conflict and supersession behavior |
| Verified repo-bound memory | All relevant retrieval/memory peers | Incorrect promotion, stale-memory harm, source traceability, independent verification and useful handoffs |
| Tool/context delivery | LiteLLM, MCP compression gateways, Headroom, RTK, context-mode | Correct schema/call identity, recoverable originals, missed evidence, context load, expansion/retry rates, memory/copy overhead and cache behavior |
| Agent workflow and human review | OpenHands SDK/ACP, Pi-like harnesses and bare existing agents | Accepted patches, harness footprint/CPU/latency, human intervention and merge-authority enforcement; provider usage is secondary |

Use the same repositories, tasks, retrieval budget and worker where comparable.
Keep hosted products, OSS variants, optional language servers and heavy embeddings
distinct when interpreting results. First-party feature claims are comparison
inputs, not proof that xMustard has or lacks parity.

## 5. Measure the project's own idea

Finish the execution side of `memory_harness.go`: a fixed task corpus, recorded
worker/model and tool versions, budget, timeout, seeds, sandbox and scoring.
Compare memory off / ungoverned / governed without drift / governed with drift.
Use accepted patches as the primary outcome, with time/cost, context volume,
stale-memory harm, promotion errors and handoff success alongside it.

Choose go/no-go thresholds before running the experiment. The statistical
functions already exist; synthetic tests are not experimental evidence. Add
competitor baselines one at a time to attribute effects fairly.

For context delivery, start with paired unmodified/deterministic-reduction runs,
then add the optional helper and governed-memory treatment separately. Include
failed attempts, retries, expansion and verification in resource/context accounting.
The owner corrected the earlier billed-cost interpretation: the priority is a
lighter harness, context shedding and better indexing, not reduced API prices.
Current run-cost estimates use text-length heuristics and hardcoded rates; retain
them only as labelled estimates, not a product savings claim.

Index-time embeddings, a resident daemon, HNSW, and deeper graph machinery follow
measured retrieval or latency gaps and an explicit resource profile. They are
not prerequisites inferred merely from a competitor's feature list.

## Open product choices

- Per-claim staged promotion rules, evidence requirements, and which claims need
  human review in addition to the settled human-only final code-merge decision.
- Initial language/repository-size coverage and the first supported client set.
- Benchmark worker/model, task corpus, spend limit, acceptable quality change,
  and numeric context/latency thresholds within the settled memory target.

The owner has already settled external-agent augmentation, all three sharing
workflows delivered in stages, context compression, investigation of tiny local
helpers, human final merge authority, no UI focus, broad competitor comparison,
and the local/no-Docker/default resource target. Those decisions do not need to
be reopened to begin the correctness work.

## Retained secondary surfaces

Record and fix the reproduced goal-store concurrency/completion defects before
advertising that runtime as safe. Existing frontend lint/auth issues are recorded
in status; UI development remains outside this program. Preserve issue/run/fix
evidence that supports memory quality and outcome measurement.

Earlier closed work remains in [the historical roadmap](history/roadmap-2026-06.md).
Use named workstreams and explicit gates rather than overloaded phase numbers.
