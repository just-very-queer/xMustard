# xMustard as a bidirectional search engine for repos

**Thesis:** agents query a precomputed inverted index + authority graph; agent
actions (tool calls, verified memories, run outcomes) write back into a feedback
layer and incrementally update the crawl — Google's loop, for code intelligence.

## The problem (measured)

Every `search` rebuilds the whole symbol graph: re-crawl + re-parse + re-hash,
~2.9s/query at the 800-file cap, O(repo) per call. That's grep-with-extra-steps,
not an index. Postgres `xm_*` tables exist but MCP search bypasses them; the rich
schema (content_hash, enclosing_scope) is underused; edges are file-level heuristics.

## Target: one Rust-owned IndexEngine, four segments

1. **Forward store** — `DocId → {kind, path, line, symbol?, enclosing_scope,
   content_hash, snippet}`. Segments: code, memory, guidance (AGENTS.md),
   pattern_match (ast-grep hits).
2. **Inverted index** — `token → sorted posting list [(doc_id, tf, field_boost)]`.
   Built once on scan; subtoken expansion (snake/camel/path) at index time, not
   query time. Backing: mmap'd sorted posting lists in the workspace data dir;
   Postgres GIN as an optional scale replica.
3. **Graph index** — `symbol_id → {inbound_rank, outbound_edges[], cluster_id}`.
   PageRank/inbound-degree authority precomputed at index time. Edges upgraded to
   LSP references where available, heuristic fallback otherwise. Lightweight
   modularity clustering on the file graph (generalize wiki's top-dir grouping).
4. **Feedback index** — `(path|symbol|query_hash) → {retrieval_count, verify_count,
   run_success_delta, last_used}`. Appended from MCP tool calls; decays over time;
   fused into the final score: `0.4·BM25 + 0.25·authority + 0.15·semantic +
   0.10·feedback + 0.10·memory_trust`.

## Query planner (stop recomputing everything)

| Query shape | Lanes |
|-------------|-------|
| CamelCase identifier | inverted exact + symbol-field boost |
| natural language | BM25 + hash-embed + optional neural rerank |
| `$A && $A()` AST pattern | ast-grep → upsert pattern_match docs |
| `dir/path/` | forward-store directory traversal |
| recall, no query | feedback-ranked top-K verified memories |

Precompute at index time: IDF, PageRank, symbol-name embeddings (batch).
At query time: merge posting lists + RRF — target <50ms warm.

## Bidirectional loop

Forward (index→agent): ground / search(query) / recall(query) / explain(path) /
impact(symbol). Backward (agent→index): search logs query+path; remember adds a
memory doc with path postings; verify boosts authority on those paths; a passing
run boosts touched symbols; a failing run suppresses bad recall paths. This is the
retrieval-ledger seed writing to an `agent_feedback` segment.

## Implementation order

- **Phase 0 — stop the bleeding (this doc's first deliverable):** persist the
  symbol graph to a warm cache keyed by a CHEAP fingerprint (HEAD SHA + dirty-file
  mtime/size — O(dirty), not O(repo)); search loads the warm graph, rebuilds only on
  drift. *(done — `rust-core/src/indexcache.rs`)*
- **Phase 1 — incremental:** *(done)* per-file symbol cache keyed by content hash
  re-parses only dirty files; authority precomputed onto each file node.
- **Phase 2 — bidirectional:** *(done)* `feedback.go` segment written from search /
  verify / run outcomes; `WorkspaceSearchWithFeedback` fuses a recency-decayed boost
  into the default ranking.
- **Phase 3 — graph parity:** *(done)* LSP-backed CALLS edges (`build-lsp`),
  `impact?symbol=` BFS blast radius + `impact?from=&to=` trace over precomputed
  adjacency, ast-grep `search?mode=pattern`, communities/clusters.

See `docs/plans/2026-06-20-deep-graph-loop.md` (S1–S6, all complete).

## What NOT to do

Don't hash every symbol on every query (hash once at index time). Don't keep two
divergent search paths (one engine; Postgres optional replica). Don't add 16 MCP
tools (enrich the 8 with modes). Don't rebuild GitNexus from scratch (borrow the
model: structure → parse → graph → query, incrementally in Rust).

## Related plans

- **Full LSP** (`docs/plans` / RETHINK): `lsp_session.rs` has documentSymbol+hover;
  add definition / references / implementation / typeDefinition / rename so the
  graph index can use real references instead of name-matching.
- **enclosing_scope** is currently a stub (always `None`); the graph index needs it
  populated from the tree-sitter parent chain.
