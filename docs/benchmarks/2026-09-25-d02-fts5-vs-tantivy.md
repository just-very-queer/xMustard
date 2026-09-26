# D-02: code-chunk BM25 storage, FTS5 versus tantivy — 2026-09-25

Status: **measured; the chunk BM25 part of D-02 closes as option (a), rusqlite FTS5 in
`index.db`.** The graph placement in D-02 (file-backed CSR segments versus an in-memory CSR
built on load) is not measured here and stays open for WS-14 (§12). This is the
first deliverable of WS-07 (critic correction, PARITY_REQUIREMENTS §12.5), taken before
the index schema was fixed.

## What was measured

`scripts/bench/d02-fts5-vs-tantivy/` is a standalone probe crate (rusqlite 0.32.1
bundled = SQLite 3.46.0; tantivy 0.24.2). `run.sh` runs each phase in its own process
under `/usr/bin/time -l`, so every peak belongs to one phase.

- **Corpus.** Every tracked source file (rs, go, ts, tsx, js, jsx, mjs, cjs, py, java,
  rb, c, h, cpp, hpp, cc) of two Apache/MIT fixtures: `research/cline` at `ee59f81`
  (2,653 files, 41,113 chunks) and `research/pi-mono` at `5fd446c` (1,639 files,
  34,207 chunks). GitNexus is not used (licence rule, §2.4).
- **Input parity.** Both engines get the same chunks (declaration-aligned, ≤80 lines)
  and the same pre-tokenized body: lowercase identifiers plus camelCase/snake_case
  subtokens. Ranking differences therefore come from the engines, not tokenizers.
- **FTS5.** Contentless table (`content=''`, `contentless_delete=1`), `unicode61`
  tokenizer, `cache_size=-2048` (2 MiB), `mmap_size=0`, one writer thread, 2,000 rows
  per transaction, `optimize` at the end. Also `detail=column` for comparison.
- **tantivy.** One writer thread with the minimum 15 MB arena, `commit` plus
  `wait_merging_threads`; the reader uses the default mmap directory.
- **Ranking.** 400 known-item queries per fixture: a function or type name defined
  exactly once in the corpus (≥8 characters, ≥2 subtokens), queried by its subtokens
  ("resolve hub owner context"). The relevant chunk holds the definition line. Top 10.
- **Machine.** Apple M1, 8 CPUs, 8 GB, macOS 27.0; other agents shared the machine.

Raw per-phase records: `docs/benchmarks/evidence/2026-09-25/d02-fts5-vs-tantivy.jsonl`.

## Results

Peak RSS is `/usr/bin/time -l` maximum resident set size of the phase process; footprint
is macOS peak physical footprint.

| Fixture | Phase | Peak RSS | Footprint | Other |
|---|---|---|---|---|
| cline | FTS5 build (detail=full) | **12.7 MiB** | 10.5 MiB | 1.2 s, 13.3 MiB on disk |
| cline | tantivy build | **43.8 MiB** | 34.4 MiB | 1.7 s, 6.6 MiB on disk |
| cline | FTS5 query, OR | 9.6 MiB | 7.3 MiB | MRR@10 0.486, R@10 0.840, p50 10.5 ms, p95 27.8 ms |
| cline | tantivy query, OR | 12.3 MiB | 5.8 MiB | MRR@10 0.486, R@10 0.835, p50 0.14 ms, p95 0.28 ms |
| cline | FTS5 query, AND | 8.1 MiB | 5.9 MiB | MRR@10 0.509, R@10 0.873, p50 0.97 ms, p95 2.6 ms |
| cline | tantivy query, AND | 12.0 MiB | 5.5 MiB | MRR@10 0.505, R@10 0.870, p50 0.05 ms, p95 0.14 ms |
| cline | FTS5 build (detail=column) | 12.8 MiB | 10.6 MiB | 11.9 MiB on disk; OR MRR@10 **0.001** |
| pi-mono | FTS5 build (detail=full) | **11.3 MiB** | 9.1 MiB | 1.6 s, 11.4 MiB on disk |
| pi-mono | tantivy build | **40.1 MiB** | 31.4 MiB | 1.9 s, 5.3 MiB on disk |
| pi-mono | FTS5 query, OR | 9.0 MiB | 7.1 MiB | MRR@10 0.497, R@10 0.835, p50 10.1 ms, p95 40.2 ms |
| pi-mono | tantivy query, OR | 11.0 MiB | 5.4 MiB | MRR@10 0.497, R@10 0.840, p50 0.15 ms, p95 0.30 ms |
| pi-mono | FTS5 query, AND | 8.1 MiB | 5.9 MiB | MRR@10 0.521, R@10 0.885, p50 1.0 ms, p95 5.1 ms |
| pi-mono | tantivy query, AND | 10.8 MiB | 5.1 MiB | MRR@10 0.518, R@10 0.880, p50 0.06 ms, p95 0.14 ms |

## Decision

For code-chunk BM25 and the fact store, D-02 closes as **(a): rusqlite FTS5 inside the
per-workspace `index.db`.** This decides nothing about the CSR graph.

- **Writer RSS decides it.** The index writer runs in the single heavy slot with a
  ≤25 MiB transient line (§7.2). FTS5 peaks at 11–13 MiB for the whole build; tantivy's
  single-thread writer peaks at 40–44 MiB, because its arena cannot go below 15 MB and
  merging adds more. tantivy would not fit the heavy-slot line.
- **Ranking is equal.** On identical input, MRR@10 and recall@10 match to the third
  decimal with OR queries, and within 0.004 with AND queries.
- **Reader RSS is comparable.** FTS5 with `mmap_size=0` and a 2 MiB page cache peaks at
  8–10 MiB for the whole process. tantivy's mmap reader shows 11–12 MiB RSS (5–6 MiB
  footprint), because mapped segment pages count toward ps-RSS (§7.6).
- **Incremental updates are native.** `contentless_delete=1` deletes one chunk's
  postings by rowid, so a changed file replaces only its own rows inside the same
  transaction as its facts.

Costs the consumers must handle:

- **OR latency.** FTS5 scores every matching row: OR queries over common subtokens take
  p50 ≈10 ms and p95 28–40 ms, against sub-millisecond tantivy. AND queries take p50
  ≈1 ms and p95 2.6–5 ms. WS-18 should query AND first and fall back to OR, or drop
  low-IDF subtokens. If the retrieval gate needs OR latency under 5 ms, revisit a
  tantivy reader over segments built by the worker.
- **Disk.** FTS5 postings are about 2× tantivy's (13.3 versus 6.6 MiB on cline). Disk
  is not the gate.
- **`detail=column` is unusable** for ranking in this setup (MRR@10 0.001–0.007), so
  the index keeps `detail=full`.

## Reproduce

```sh
export CARGO_TARGET_DIR=/tmp/d02-target
cargo build --release --manifest-path scripts/bench/d02-fts5-vs-tantivy/Cargo.toml
for r in cline pi-mono; do
  scripts/bench/d02-fts5-vs-tantivy/run.sh "$CARGO_TARGET_DIR/release/d02-probe" \
    research/$r /tmp/d02-work $r
done
```
