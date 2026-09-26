# WS-14 resident index query side: memory and the graph-storage decision — 2026-09-26

Status: **measured on both platforms.** The resident service reads the code graph
from file-backed segments through a 2 MiB block cache. On a 100k-symbol graph with
five resolved edges per symbol, and including a snapshot swap under four concurrent
clients, its RSS stays within 15 MiB of its base:

- macOS: +10.5 to +14.1 MiB above base;
- Linux: +8.9 to +10.6 MiB above base, with the resident worker's single glibc arena.

Holding the same segment in memory costs 31–33 MiB steady and 56.6 MiB during the
swap.

Gate v2 results:

- CI suite: PASS at 74.1 MiB.
- Parity `agents-2`: PASS at 94.0 MiB.
- Parity `snapshot-swap-under-load`: INVALID. The heavy-slot admission refused the
  index refreshes because the tree was over the soft ceiling (see "Gate v2").

## What was decided (D-02, the open half)

The question was where the graph lives: file-backed CSR segments that the index
worker builds and the service reads with `pread`, or an in-memory CSR built on load.
The answer is **file-backed segments** (`GraphStorage::File`, the default).
`XMUSTARD_GRAPH_STORAGE=mem` keeps the in-memory layout for measurement only.

- One segment per index generation, `graph.<generation>.<nonce>.csr`, is written by
  the index worker (`xmustard-core index build|update`, heavy slot, one-shot) before
  the generation becomes visible. Meta key `graph_segment` names it. Readers never
  write. A store without a segment, built before WS-14, is served by the legacy graph
  until its next update writes one.
- Layout (`rust-core/src/index/csr.rs`):
  - fixed-width file rows (32 B) and symbol rows (16 B);
  - sorted interned heaps for paths and names, so a name or path id is its rank and
    lookup is a binary search;
  - postings for name→symbols and file→symbols;
  - forward and reverse CSR over every `edges` row: u32 offsets and 12-byte records
    (node, weight, kind, layer, provenance, quantized confidence). File nodes come
    first, then symbol nodes;
  - forward and reverse 8-byte file projections of the `structure` layer, which
    impact, trace, clusters and hotspots walk.
- Reads go through one process-wide block cache: 16 KiB blocks, 2 MiB by default
  (`XMUSTARD_GRAPH_CACHE_KIB`). Snapshots that overlap during a swap share the cache,
  so double-buffering costs one file descriptor, not a second graph. Reads of a block
  or more are section scans (search walks names, postings and symbol rows) and bypass
  the cache, so scans never evict the blocks that random walks reuse.
- Each snapshot keeps a small per-file table in memory, about 50 B per file: the path
  heap plus inbound weight and dependents. Every query touches it.

## Resident service (`tests/index_query.rs`, `resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line`)

Workload: a synthetic `index.db` with 10,000 files and 100,000 symbols. There are
60,000 distinct camelCase names from a 40×60×25 vocabulary, so a query word matches a
few percent of symbols, as in real code. The graph has 500,000 symbol-level
`resolved` edges, five per symbol: three stay inside a 100-file package and two go
anywhere, which gives 437,092 distinct file pairs. The worker writes the segment. The
service (`xmustard-core serve --max-inflight=4`) then answers rounds of `search`,
`symbolgraph impact`, `trace` and `cluster-of`. A second generation replaces the store
while four clients query, and the service swaps snapshots under that load. ps-RSS is
sampled every 50 ms during the swap.

| Storage | Base | Steady | Swap peak | After swap | Segment heap |
|---|---|---|---|---|---|
| file, 2 MiB cache (release) | 6.6 MiB | +10.5 to +11.0 | +11.5 to +12.5 | +13.2 to +14.1 | 0 |
| file, 2 MiB cache (debug) | 9.0 MiB | +11.5 | — | +12.1 | 0 |
| file, Linux, `MALLOC_ARENA_MAX=1` (release) | 5.7 MiB | +8.9 | +10.3 | +10.6 | 0 |
| file, Linux, `MALLOC_ARENA_MAX=2` (release) | 5.8 MiB | +10.4 | +14.2 | +14.2 | 0 |
| file, Linux, glibc default arenas (release) | 6.0 MiB | +15.8 | +18.7 | +19.0 | 0 |
| file, 4 MiB cache (release, earlier run) | 6.7 MiB | +13.7 | — | +16.0 | 0 |
| mem (release) | 6.6 MiB | +31.0 to +32.6 | +56.6 | +35.1 to +56.6 | 23.3 MiB |

With the 2 MiB cache, a first query that loads the snapshot costs about 6.6 MiB. That
covers the cache, the per-file table, SQLite for the meta read, and the code pages
that the new paths touch. The test asserts steady, swap peak and after-swap all at
≤15 MiB above base.

The Linux rows are from the borrowed build box (Ubuntu, 6 cores). On glibc, each pool
thread's arena keeps its own high-water mark of query transients. The Go supervisor
already caps core children at two arenas (WS-06). The resident worker now gets a
single arena (`rustcore.coreChildEnv("serve")`); an operator's own
`MALLOC_ARENA_MAX` still wins. The test starts `serve` with that same setting.

Three findings changed the code while it was measured:

- **Search candidate pool.** The lanes are unchanged: WS-18 owns the lanes and ranking.
  Before this change, a query token that every symbol shares, such as `symbolName…`,
  put all 100k symbols in the RRF pool with owned strings and a `SearchHit` each. That
  added 64 MiB for one query and 245 MiB after concurrent queries, because each pool
  thread's allocator keeps its own high-water mark. Candidates are now compact: shared
  path strings, a bitmask for the matched tokens, and hit strings built only for the
  returned `limit`. This cut the all-match case to about 17 MiB. The ranking is
  unchanged: the same order, ties broken by name, and the same `total`. A query that
  matches every symbol of a 100k-symbol repository still costs O(pool) transiently.
  That is WS-18's lane design question; the realistic-vocabulary workload above stays
  within the line.
- **Readers never write segments.** In an earlier version, the resident reader wrote a
  missing segment itself. During a swap that put the writer's peak into the service
  (+29 MiB). The writer now produces the segment before it commits the generation. A
  reader that finds the new state unreadable keeps serving its previous snapshot and
  looks again on the next read.
- **The segment writer's memory runs inside SQLite.** The first writer held dense-id
  maps, symbol rows and postings in Rust. That added up to +8 MiB to a composite
  incremental update (28–30 MiB peak). Now temporary tables in SQLite (with a
  256 KiB page cache that spills to temporary files) hold the remapping and every
  ordering, and every section streams to the file. The writer itself keeps
  per-file, per-name and per-node counters.

## Index worker (heavy slot) with the segment write

`xmustard-core index build <fixture> --index-dir <scratch> --no-cache`, run twice
each. The research clones are `research/cline` at ee59f817 and `research/pi-mono` at
5fd446c. "Without" is the same binary with the segment write disabled.

| Fixture | Symbols / edges | Peak RSS with segment | Without | Footprint with / without | Segment write |
|---|---|---|---|---|---|
| cline | 51,263 / 26,982 | 25.0 MiB | 24.3–24.5 MiB | 13.5–13.6 / 13.0–13.1 MiB | 0.18–0.20 s |
| pi-mono | 49,895 / 24,090 | 22.1 MiB | 21.2–21.3 MiB | 13.0 / 12.1–12.2 MiB | 0.18 s |

With the streaming writer, the segment write adds 0.5–0.9 MiB to the worker's peak.
The cline segment is 3.3 MB on disk; the store is 80 MB.

On the 7,288-file parity composite (100,000 symbols at the symbol budget):

- a no-op `index update` peaks at 16.1 MiB;
- a no-op that has to rewrite a missing segment peaks at 17.8 MiB (0.47–0.79 s);
- a one-file incremental update, segment included, peaks at 24.4–25.4 MiB and takes
  0.7–1.2 s.

The cline build sits at the 25 MiB heavy line: 24.3–24.5 MiB without the write on
this machine today, against WS-07's 22.4–23.5 MiB in a quieter window. The ledger
should carry the delta (WS-10).

## Equality with the legacy graph

`bfs_trace_and_blast_radius_equal_the_legacy_graph_on_the_same_edges` builds the
legacy `SymbolGraph` from the store's own rows: indexed files, non-local symbols and
`structure` edges. It checks that the snapshot answers exactly like the legacy
functions for:

- `symbol_impact` at depths 1, 2 and 4 for every symbol name;
- `trace_symbols` over consecutive name pairs;
- `compute_hotspots` and `compute_clusters`, plus `cluster_of` for each cluster;
- search's inputs: symbols, lines and per-file inbound weight.

It compares serialized output, not a summary. With `XMUSTARD_PARITY_FIXTURES` pointing
at the research clones, `legacy_equality_holds_on_parity_fixtures` runs the same check
on cline and pi-mono (400 sampled names each).

The index's lexical edges differ from the legacy builder's by design (see
`rust-core/src/index/edges.rs`: no edges from words in comments or strings). So the
equality is with the legacy algorithms over the same edges, not with the legacy
builder's edges.

## Gate v2 (evidence in `docs/benchmarks/evidence/2026-09-26/`)

These runs were on the same M1 with agents over stdio shims. The gate runs the core
per call; it does not start the resident worker, so it measures the index refresh and
the one-shot readers.

| Run | Verdict | Gate peak |
|---|---|---|
| `make bench-gate` (CI suite, v1-workload) | PASS | 74.1 MiB (74.0 before the writer change) |
| parity `agents-2` (composite, 7,288 files) | PASS | 94.0 MiB. An earlier run before the streaming writer failed at 102.4 MiB |
| parity `snapshot-swap-under-load` | INVALID | 104.2 MiB |

Checks that failed in the swap scenario:

- "snapshot_generation advanced" failed in 2 of 3 runs (one run advanced 2→3);
- "the last edit is searchable" failed in all runs.

The API log shows why:

```
code index …/parity-composite: heavy slot refused: … memory near the soft ceiling:
measured tree and stdio shims 71–87 MB + declared 26214400 > 94371840 bytes
```

With two stdio shims (about 29 MiB) and a Go daemon at 39–41 MiB, the tree is already
over 65 MiB. The governor therefore refuses every 25 MiB index update, as designed.
The refresh fails open: the reads keep answering from the last generation, and the
freshness envelope lists the edited result paths as dirty.

A realistic declaration would not change this. A one-file composite update measures
24–25 MiB, which is the heavy line itself. The remedy is the open reconciliation in
`budget_ledger.json`: the daemon over its line and the shims that WS-13 removes. The
scenario's own acceptance also needs the WS-15 watcher.

The composite is also at the envelope's symbol bound: 528 files hold `symbol_budget`
losses. So `coverage.truncated` is true there and the `uncapped_index` probe reads it
as off. The files are all read and reported; the declared 100k-symbol envelope,
not a file cap, bounds them.

## Caveats

- Other agents shared both machines; the numbers moved by about ±1 MiB between runs.
  Gate v2 ran on macOS only. The Linux figures come from the resident-RSS test on the
  build box.
- Freshness costs one or two `git` children every 2 s per root (`envelope::TTL`), or
  sooner when the store changes. The index refresh before a read is a one-shot
  `index update` under the heavy slot, once per new identity key. WS-15's watcher
  replaces both.
