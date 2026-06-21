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
divergent search paths (one engine; Postgres optional replica). Don't add more MCP
tools (the surface is a fixed **9**, enriched with modes/params — `search?mode=`,
`impact symbol=/from=/to=`, `recall query=`; `tools/call` strictly validates them
and rejects malformed/unknown/wrong-typed args with `-32602`). Don't rebuild
GitNexus from scratch (borrow the model: structure → parse → graph → query,
incrementally in Rust).

## Deferred — designed, intentionally NOT built yet (P2)

Two items from the post-hardening pass (G, H) were analysed and **deferred on
purpose**. Recording the design + the trigger that would justify building them,
so the decision is auditable rather than silently dropped.

### G — long-lived IndexEngine daemon

**What it would be:** a resident `xmustard-indexd` per workspace holding the parsed
`SymbolGraph` + inverted index hot in RAM, fed file-change events (fs-watch or the
existing `cheap_key` drift), serving `search`/`impact` over a local socket so warm
queries skip even the cache-deserialize step.

**Why deferred (not a skeleton):** the current model is **on-demand binary
invocation** with a cheap-fingerprint warm cache (`indexcache.rs`) — a cold call
rebuilds, a warm call loads a cached graph. A daemon trades that statelessness for
a long-lived process + socket/IPC surface + crash-recovery + resident memory that
fights the **50–100 MB RSS, no-Docker** budget (holding multiple workspace graphs
in RAM is exactly what that budget forbids). The win (skip cache deserialize) is
small next to the cost (tree-sitter **parsing**, not cache I/O, dominates a cold
rebuild). For a *tiny MCP server*, statelessness is a feature.

**Trigger to revisit:** a measured benchmark showing cache-deserialize (not parse)
as the warm-query bottleneck on a real repo, **and** a workspace count low enough
that resident graphs stay inside the RSS budget. Build the daemon as an *optional*
accelerator behind an env flag, never the default path.

### H — blake3 / merkle content-key migration

**What it would be:** replace the `sha2::Sha256` file/cache hashing with `blake3`,
and add a directory **merkle tree** so "did anything under `dir/` change" is a
single root-hash compare.

**Why deferred (not a dependency add):** hashing is **not** the reindex
bottleneck — tree-sitter parsing is — so blake3's speed edge buys little, while it
adds a dependency and invalidates every existing on-disk cache key (format churn).
The merkle tree is largely **redundant with git**: `cheap_key` already gets
O(dirty-file) invalidation from `git HEAD + dirty {path,size,mtime}`, which beats
recomputing a merkle root. `file_hash` already content-addresses per file (sha256)
for incremental reparse, so the correctness property H targets already holds.

**Trigger to revisit:** profiling that puts the hash step on the hot path (very
large files / non-git trees where the git fast-path is unavailable), at which
point switch `file_hash` → blake3 **keeping the existing atomic-write path** and
add the merkle root only for the non-git fallback.

## Related plans

- **Full LSP** (`docs/plans` / RETHINK): `lsp_session.rs` has documentSymbol+hover;
  add definition / references / implementation / typeDefinition / rename so the
  graph index can use real references instead of name-matching.
- **enclosing_scope** is currently a stub (always `None`); the graph index needs it
  populated from the tree-sitter parent chain.
