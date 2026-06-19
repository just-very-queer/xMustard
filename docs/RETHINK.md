# xMustard Rethink — governed memory, not a platform (June 2026)

The honest critique is correct: xMustard is **over-engineered and under-delivering**.
It has breadth (39 MCP tools, ~196 REST routes, a symbol graph, a Postgres static
index, RRF + a hashing embedding, a goal/swarm runtime, eval timelines, security
disposition, ops materialization) but no sharp, used value. This doc grounds a
focused rebuild in current research, then defines what to keep, cut, and build.

## What the 2026 research actually says

- **Tool proliferation is an antipattern.** mini-swe-agent is ~100 lines with 3
  tools; Anthropic's best SWE-bench scaffold uses **2 tools** (persistent bash +
  string-replace editor). A 22-point SWE-bench swing comes from *context
  management*, not tool count. → xMustard's 39 MCP tools actively hurt: they bloat
  the agent's context and cause "context rot."
- **Narrow retrieval beats broad dumps.** Agents should call targeted search and
  get *relevant context slices*, not symbol-graph/file-tree dumps. → xMustard's big
  static index + 5 overlapping search/graph tools are the wrong shape.
- **Active exploration beats static indexing.** Modern agents explore a repo
  iteratively like a developer; static indexes go stale (drift). → xMustard's
  materialized symbol graph + Postgres index is mostly dead weight.
- **Context governance IS the discipline.** Production agents need governed memory:
  trust lifecycle, conflict/drift detection, staleness, versioned standards,
  cross-agent distribution. → This is the ONE thing xMustard already got right (the
  multi-agent verification gate + change-tracking drift + incorporation lineage).

Sources: arXiv 2510.21413 (Context Engineering for AI Agents); arXiv 2604.03515
(Inside the Scaffold); arXiv 2601.07190 (Active Context Compression);
agents-remember-md (drift-aware repo memory, no vector search); SWE-bench Pro /
mini-swe-agent harness analyses.

## The thesis

**xMustard = governed runtime memory for coding agents.** A tiny MCP server that
gives any agent (codex, Claude Code, opencode, …) two things and nothing else:
1. **Grounding** — "what changed / what's stale / what's broken since you last
   touched this," plus the *verified* context slices it should trust.
2. **Memory with a trust lifecycle** — propose → multi-agent verify → promote, with
   drift detection so memory never goes stale silently.

It does NOT try to be the agent, the index, the search engine, the eval platform,
or the security tracker. It feeds the agent a small, high-signal, *governed* slice.

## Keep / Cut / Build

**Keep (the moat):** context governance (propose/verify/active), change-tracking +
drift + incorporation lineage, session grounding, one narrow code search, one
file/symbol explainer, impact (dirty symbols), diagnostics, auth.

**Cut from the agent surface (over-engineering):** collapse 39 MCP tools → ~8
narrow ones; retire the 5 overlapping search/graph tools to one; stop exposing the
ops/eval/security/provider/routing tools to agents (they belong in a UI, not the
agent's context); deprecate the goal/swarm runtime and the materialized static
index as the primary path (agents explore live instead).

**Build (delivery, not breadth):** make grounding + governed memory genuinely
excellent — drift that's actually checked against live sources on recall, memory
health/staleness scoring, and AGENTS.md-style versioned onboarding slices keyed by
source path. Measure on a real task, not a feature count.

## Implementation order

1. **Sharpen the MCP surface to ~8 narrow, high-signal tools** (this commit). The
   backing HTTP endpoints stay; the agent-facing surface gets disciplined.
2. Drift-on-recall: when `recall`/`ground` runs, re-check memory against the live
   tree and flag stale entries.
3. Memory health: staleness + conflict scoring on context entries.
4. Retire the materialized static index as the default; keep search live + narrow.
5. Deprecate goal/swarm + ops/eval/security platform surfaces (move to UI-only or remove).
