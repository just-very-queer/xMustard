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

1. ✅ **Sharpened the MCP surface to 8 narrow tools** (ground, recall, remember,
   verify, search, explain, impact, diagnostics). Backing HTTP API unchanged.
2. ✅ **Drift-on-recall**: a memory references the paths it's about; their content
   hashes are snapshotted at promotion; `recall`/`ground` re-check against the live
   tree and flag stale entries (`stale_count`, `stale_paths`, `stale_memory`).
3. ✅ **Memory conflict surfacing**: `recall` reports `conflicts` — files 2+ active
   memories claim something about — so agents reconcile before trusting.
4. ✅ Search is already live (builds the symbol graph from the current tree each
   call; the static Postgres index is a separate HTTP-only path, off the agent
   surface). Added the missing half of "slices not dumps": each symbol hit now
   carries its 1-based `line` so the agent jumps to the location.
5. ✅ Lean surface via `XMUSTARD_CORE_ONLY=1` (off by default): 404s any path
   outside the governed-memory + grounding + search core, so a production deployment
   serves only the agent-facing surface without deleting the platform routes the UI uses.

### Beyond the core (ROADMAP items — all delivered)
- ✅ **Failure explainers** (`why_failed`, A3): correlate a failed run's signals +
  salient error lines + the changed files named in its output. 9th MCP tool.
- ✅ **Routing as a run-execution runtime** (A1): `provider:<name>` / `route` runtimes
  execute via the model directly and record a runRecord (`StartProviderRun`).
- ✅ **Neural-embedding search lane** (A2): `/search?rerank=<provider>` fuses an
  embeddings-cosine rank with the lexical/structural rank via RRF (`OpenAIEmbeddings`).
- ✅ **Cockpit wiring** (A4): `MemoryPanel` surfaces verified context (with stale +
  conflict flags), pending proposals to approve/reject, and a propose form.
   (The agent surface is already free of them; this is internal cleanup, deferred —
   not gutting features without an explicit call.)

The governed-memory moat (propose → multi-agent verify → promote → drift → conflict)
is delivered and sharp. That is the product; the rest is a future UI's concern.
