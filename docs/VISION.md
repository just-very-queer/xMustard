# Product vision

Confirmed with the owner on 2026-09-24:

**xMustard provides shared, verified memory and repository intelligence for
existing coding agents.** It sits above their supported tool and context
interfaces, helping agents establish what is true now, retrieve the relevant
evidence, and carry trustworthy knowledge between work sessions.

## Accepted direction

- Work with agents such as Codex, Claude Code, and OpenCode through MCP and typed
  CLI/HTTP access. The agent-facing product is the priority.
- Support concurrent agents in one repository, switching agents across sessions,
  and knowledge shared across explicitly authorized repositories, delivered in stages.
- Add tool-context reduction: select relevant tools and source evidence, reduce
  repetitive results, and provide access to retained originals. Client-specific
  integration determines which calls can actually be observed or transformed.
- Investigate Cactus Needle and Typesafe Jev for tool selection and structured
  decisions, distinguishing local artifacts from hosted services. Model suggestions
  do not confer execution permission or verified-memory status. The default must
  remain useful without a helper model or hosted decision service.
- Agents may prepare changes and verification/review evidence. **The human is the
  final authority for merging code.** Memory promotion, run approval, and Git
  merge approval are separate decisions.
- Keep the default local, without Docker, targeting roughly **50–100 MB for the
  active xMustard-owned process tree**. This is a design target, not a measured
  guarantee; report external agent/compiler processes separately as well.
- Compare against the current code-intelligence and memory competitors together:
  GitNexus, Serena, Augment, Sourcegraph, Mem0, Letta, and Zep/Graphiti. The dated
  [parity review](research/COMPETITOR_PARITY_2026-09-24.md) distinguishes their
  advertised capabilities from tested xMustard behavior.
- UI development is outside the current focus. Existing issue/run/evaluation and
  provider surfaces can support the product without defining its scope.

The owner clarified that "cheaper than OpenHands" means a lighter harness,
lower memory overhead/data movement, context shedding, and better code indexing,
with the confirmed [Pi harness](https://github.com/badlogic/pi-mono) as a reference.
**Lower API prices or a billed-cost reduction
target are not the objective.** Measure working-set size, allocations, repeated
file/index reads, context delivered, latency, and task quality separately. Hardware
memory bandwidth is a distinct measurement, not a synonym for RSS or token count.
See the proposed [context layer](CONTEXT_LAYER.md) and dated
[proxy/model research](research/TOOL_CONTEXT_RESEARCH_2026-09-24.md).

## Ideas to preserve

The repository and conversation history contain a consistent set of useful ideas:

1. **Ground before acting.** Report changes, stale knowledge, diagnostics, and
   verification evidence with their source and freshness, including changes
   since the last run or accepted fix when that evidence exists.
2. **Give memory an explicit trust lifecycle.** A proposal, its verification,
   promotion, later edits, drift, and conflicts should remain inspectable.
3. **Return small, useful evidence.** Narrow search, file explanation, and impact
   should help an agent continue its own exploration. A retrieval ledger should
   explain why each piece of evidence was selected.
4. **Learn from outcomes.** Retrieval and verifier telemetry should relate to
   accepted work and failed work, with traceable evidence rather than hidden scores.
5. **Keep one owner for each kind of truth.** Go handles delivery and persistence;
   Rust handles repository semantics. JSON operational records and optional
   Postgres materialization have distinct roles.
6. **Reduce context while retaining evidence.** A shortened result must identify
   its source, omissions, and how to recover the original. Machine-generated
   summaries remain derived observations until the memory policy verifies them.

These ideas are implemented to different degrees. Their presence in source is
not proof that they improve task outcomes; see [status](STATUS.md).

## What parity means

Parity is evaluated by an agent's ability to solve the same representative task:
find the relevant code, follow dependencies, recognize stale facts, recover prior
decisions, and transfer supported knowledge. It is not a count of tools or a
requirement to copy every competing runtime, UI, or hosted service.

The current MCP interface has nine tools. Deepen those tools first; a new tool
needs a concrete workflow that the existing interface cannot represent clearly.
Competitor features that exceed the default resource target need a measured
design tradeoff before adoption.

## Proof required

- Correctness under repeated edits, concurrent agents, cancellation, and restart.
- Honest coverage: partial source, symbol, language, or memory coverage is visible.
- A held-out comparison of memory off, ungoverned memory, governed memory without
  drift, and governed memory with drift, with fixed worker/tool/budget conditions.
- Outcomes measured as accepted patches, localization, time/cost, stale-memory
  harm, promotion mistakes, handoff success, and process-tree resource use.
- Compression compared with unmodified output for missed evidence, retries,
  expansion requests, context retained/delivered, cache behavior, resource use,
  and task success. Provider usage remains diagnostic, not a savings promise.
- Human merge approval bound to the reviewed change revision; a later edit
  requires renewed approval. This policy is a target, not an existing enforcement claim.

All three target workflows are accepted; delivery proceeds in stages. The
proposed assurance progression is observation, evidence checking, independent
review, and policy-governed sharing, with human review for consequential decisions
and final code merge. Exact per-claim promotion rules, benchmark corpus,
worker/model, spend limit, and success thresholds still need concrete choices.
Existing votes are not automatically proof of correctness.
