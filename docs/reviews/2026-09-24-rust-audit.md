# Rust core audit — 2026-09-24

Reviewed source revision: `cd13e2b78a9fadcbcd760b19442d5992136b1db0`, branch `feat/product-v1`.
The Rust default build and tests pass, but reproducible defects remain in cache freshness,
symbol coverage, and the retained goal platform. Historical completion claims do not establish
correctness of these paths today. No product behavior was changed in this audit.

## Scope and verification

Inventory: 20 Rust source files, 18,340 lines, including 3,689 lines of shared models
and the 1,649-line CLI dispatcher. All modules were mapped; substantive control paths,
limits, persistence, and relevant tests were inspected. This is a targeted source audit,
not a claim of line-by-line formal verification of every model or fixture.

Read `AGENTS.md`, `STATUS.md`, `RETHINK.md`, `INDEX_ENGINE.md`, `FRONTIER.md`,
`ROADMAP.md`, and `BENCHMARKS.md` to compare the implementation with its claims.

| Check | Same-run result |
| --- | --- |
| Toolchain | `rustc 1.93.1 (01f6ddf75 2026-02-11)`; `cargo 1.93.1 (083ac5135 2025-12-15)` |
| `cd rust-core && cargo test` | Exit 0; 114 passed, 0 failed, 0 ignored; binary tests 0; doc tests 0 |
| `cd rust-core && cargo build --bin xmustard-core` | Exit 0; fresh `target/debug/xmustard-core` supplied to Go and HTTP/MCP audit checks |
| `cd rust-core && cargo clippy` | Exit 0 with **12 warnings**, not warning-clean |
| Isolated correctness probes | Four defects reproduced below, outside user runtime data |

Clippy reported 7 collapsible conditionals, 4 excessive-argument warnings, and 1 complex
return type across diagnostics, repomap, semantic, symbolgraph, treesitter, and verification.
Tests used default features: `semantic-onnx` was not built or tested. This pass did not
prove neural retrieval quality, live language-server correctness, or sustained RSS limits.
The Rust LSP session tests primarily exercise server mapping and unavailable-server behavior;
the `matches!` expression at `lsp_session.rs:672` is not itself asserted.

## Module ownership

| Source modules | Responsibility |
| --- | --- |
| `lib.rs`, `bin/xmustard-core.rs` | Safe-Rust crate boundary and JSON/CLI bridge dispatch |
| `models.rs` | Shared serializable workspace, issue, run, planning, verification, integration, and operational contracts |
| `scanner.rs`, `repomap.rs`, `treesitter.rs` | Signal discovery, repo/file descriptions, path symbols, tree-sitter extraction with regex fallback |
| `symbolgraph.rs`, `indexcache.rs`, `search.rs`, `semantic.rs` | Lexical graph, file/cache invalidation, fused retrieval, docs/guidance search, external ast-grep pattern search |
| `changetrack.rs` | Content baselines, drift, dirty symbols/signatures, incorporation lineage |
| `lsp.rs`, `lsp_session.rs`, `diagnostics.rs` | LSP response normalization, server sessions, normalized/replayable diagnostics and conservative symbol linking |
| `ownership.rs`, `wiki.rs` | Git-history owner suggestions, graph subsystem summaries, cached wiki projections |
| `verification.rs` | Command execution, process-group timeout cleanup, bounded pipe capture, coverage parsing |
| `goalruntime.rs`, `swarm.rs`, `benchmark.rs` | Retained goal ledger, role-tagged iteration gates, local microbenchmarks |

## 1. P1 — repeated dirty-file edits reuse a stale graph

Core-product defect; reproduced. `indexcache.rs:54` trims the entire output of
`git status --porcelain`, removing the first row's leading status space. The fixed-column
slice at `indexcache.rs:73` then drops the first filename character. The metadata lookup
at line 79 misses the real file, so further edits to that already-dirty file do not change
the key. `symbolgraph.rs:926-933` returns the cached graph before reading current content.

Reproduction, using a new temporary Git repo with committed `a.rs`:

1. Change its function from `initial_symbol` to `first_dirty_symbol` without staging.
2. Run `xmustard-core search <repo> audit first_dirty_symbol 10`.
3. Change it again to `second_dirty_symbol_added_later`, also changing the file length.
4. Run `xmustard-core search <repo> audit second_dirty_symbol_added_later 10`.
5. Repeat step 4 with a new workspace identifier, `fresh-audit`, to bypass the old cache.

Observed: step 4 returned `first_dirty_symbol`; step 5 returned the exact new symbol.
The stale result still reported `eligible_files=1`, `indexed_files=1`, `truncated=false`
and had a fresh response timestamp. Existing `cheap_key_changes_when_a_file_changes`
only compares clean→dirty, which misses dirty→dirty invalidation.

Required repair: parse untrimmed, NUL-delimited Git status correctly, including renames
and unusual filenames; regress repeated edits before relying on warm cache freshness.

## 2. P1 — symbol truncation is invisible in coverage

Core-product defect; reproduced. `repomap.rs:440-442` takes only the first 32 symbols
per file; `treesitter.rs:118-119` has an earlier 64-symbol limit. Graph construction
uses this path at `symbolgraph.rs:967-971`. `IndexCoverage` at lines 47-54 reports
file counts only, and its truncation flag at lines 85-93 only reflects the 800-file cap.

Reproduction: add and stage `many.rs` containing 70 standalone functions named
`symbol_001` through `symbol_070`, then run
`xmustard-core symbolgraph build <repo> coverage-audit`.

Observed: `many.rs.symbol_count=32`; only `symbol_001` through `symbol_032` existed
in the graph. Coverage reported both fixture files indexed, `truncated=false`, with no
degradation reason. Search, impact, and signature baselines cannot represent omitted symbols.

Coverage also counts selected files before parsing; unreadable, oversized, or invalid-UTF-8
files become empty content/symbols through `unwrap_or_default` at
`symbolgraph.rs:964-983`, without a per-file failure report.

Required repair: separate display limits from index completeness and report skipped files
and truncated symbols. Test missing symbols beyond the limit, not only small sample files.

## 3. P1 — parallel goal creation loses successful writes

Secondary retained-platform defect; reproduced through the Rust CLI. Goal mutations use
read→modify→atomic replacement without an interprocess transaction lock:
`goalruntime.rs:754-777` for create, and lines 792-831 for iteration append.
Atomic rename prevents torn files but does not serialize competing mutations.
`api-go/internal/workspaceops/goals.go:120-143` delegates creation without a store lock.

Reproduction: write one request with title `Concurrent audit fixture goal` and objective
`Verify concurrency records using a temporary audit fixture.` Run 24 concurrent
`xmustard-core goal create <scratch-data> audit <request.json>` processes, then list goals.

Observed result:

```json
{"create_calls":24,"successful_responses":24,"failed_responses":0,"durable_goal_records":2,"distinct_returned_goal_ids":2}
```

The number retained is timing-dependent; acknowledged mutations must not disappear.
Required repair: serialize the complete transaction across processes and define recovery
for the goal/iteration/ledger write sequence. Exercise concurrent HTTP and CLI writers.

## 4. P1 — a failed test can satisfy goal completion

Secondary retained-platform defect; reproduced. `GoalEvidence::is_verification` at
`goalruntime.rs:120-129` returns true for `test`, `build`, `lint`, or `verification`
without checking outcome. The completion gate at lines 476-497 only tests this predicate.

Reproduction: create a scratch goal; append this iteration; request status `complete`
without supplying a verification-skip reason:

```json
{"role":"verifier","summary":"The audit fixture test command failed with a nonzero exit.","outcome":"failed","evidence":[{"kind":"test","command":"false","outcome":"failed"}]}
```

Observed: the status command exited 0 and persisted `status=complete`, with the failed
test as its only evidence. This probe recorded a failed result; it did not execute `false`.
Required repair: distinguish evidence presence from passing verification, and reject
completion on failed/unknown verification unless an explicit operator override is recorded.

## 5. P2 — resource and read confinement claims exceed the shared implementation

Source-confirmed gaps; no hostile-load or file-exfiltration probe was run.

- The safe Unix source opener at `symbolgraph.rs:189-244` rejects symlink traversal,
  and regular-file reads/hashes are capped at 8 MiB. However `scanner.rs:109` and
  `lsp_session.rs:225,434` read source using raw `read_to_string`, bypassing that primitive.
- ast-grep runs through `Command::output` at `semantic.rs:148`, buffering its entire
  output before the result limit is enforced at lines 185-199. That limit is not a
  process-output or memory bound; the Rust path also has no local child timeout.
- The LSP payload cap at `lsp_session.rs:140` does not bound header `read_line` at
  line 124 or the unbounded message channel at lines 249 and 376.
- Graph construction retains file bodies in `content_cache` at `symbolgraph.rs:1003`.
  An 800-file cap and an 8 MiB per-file cap do not establish a 50–100 MB total RSS bound.

Required repair: use the common capped source reader in every source-reading lane,
bound child output while streaming, and measure total RSS on a specified workload.
The Go bridge's outer limits do not prevent an inner Rust process allocating excessively.

## Product and documentation implications

The implementation supports the governed-memory thesis, but its supporting grounding
cannot yet be treated as always current or complete. The primary search/impact graph is
lexical (`symbolgraph.rs:1098`); the LSP upgrade is the separate `build-lsp` command
(`bin/xmustard-core.rs:1563`). This is already acknowledged in historical `STATUS.md:158-160`.
Default embeddings are deterministic lexical hashes, not learned semantic embeddings.
Tree-sitter covers Rust, Go, TypeScript/TSX and JavaScript/JSX; other supported paths use
regex fallback. Non-Git and over-800-file degradation is surfaced, but symbol-level
degradation is not. Docs/guidance search rereads tracked prose and is not a resident index.

Keep the explicitly deferred daemon and blake3/Merkle migration deferred until measurements
justify them. Prioritize cache correctness and honest coverage before expanding graph/search
features. Goal fixes concern the retained HTTP/CLI platform, not the nine-tool MCP surface.
Historical benchmark numbers in `BENCHMARKS.md` were not remeasured in this pass.

All behavioral probes ran under `/tmp/xmustard-rust-audit.qz03P6` with disposable repo/data
fixtures. The existing user workspace data was not modified. No runtime/source changes were
made; this report is the only tracked artifact created by the Rust audit.
