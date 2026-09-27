# WS-18 hybrid search: BM25 lane, docs from the index, rerank and snippets — 2026-09-28

Status: **measured on Linux (the borrowed build box, 6 cores, glibc, release builds)**.
The retrieval gate improves with no regression on the existing gold and gates at the
new values; the resident BM25 cache stays under its 5 MiB steady / 8 MiB peak line; the
index writer stays inside the 25 MiB heavy slot; and a one-shot search is about twice as
fast, because docs are no longer reread per query.

Fixtures: `research/cline` at `ee59f817` (Apache-2.0) and `research/pi-mono` at
`5fd446c` (MIT), the same commits as D-02; the gold repository in `scripts/bench/gold`.
Raw output: `docs/benchmarks/evidence/2026-09-28/`.

## What changed in the measured path

- `search` reads two FTS5 lanes from `index.db` through one read-only connection per
  snapshot (`reader::Snapshot::with_text`): `bm25` over `chunk_fts` (function-aligned
  chunks: body words, comments, declared names, path) and `docs` over the new `doc_fts`
  (heading sections of tracked docs and guidance, schema version 4). The connection has
  no mmap and a 2 MiB page cache (`fts::TEXT_CACHE_KIB`).
- Docs are indexed by the index worker (`index::docs`), compared by stat key on every
  build and update; search no longer runs `git ls-files` or reads docs per query.
- The top 40 fused candidates are reranked from their declaration name and the text of
  the one chunk each hit names (a bounded `pread`, used only when its hash still equals
  the indexed chunk's). Snippets come from the same read.

## Retrieval gate (`scripts/bench/retrieval_gate.py`, three runs × cold/warm/after-edit)

The gate gained ranking metrics over the existing ten search queries (gold spans and a
gold-path MRR@5) and six body-term queries whose terms only a function body holds
(`body_queries` in `gold/queries.json`). Both sides ran the same extended gate script.

| Metric | Before (feat/parity-v2 2bf6b2c) | After (parity/ws-18) | Threshold now |
|---|---|---|---|
| gold paths in top 5 (of 12) | 12 every pass | 12 every pass | 12 (was 11) |
| gold spans in top 5 (of 10) | 10 every pass | 10 every pass | 10 |
| gold-path MRR@5 (10 search queries) | 0.95 cold, 0.85 warm and after-edit | 1.0 every pass | 1.0 |
| body-term queries with the gold span in top 5 (of 6) | 0 every pass | 6 every pass | 6 |
| mean search latency per pass, gold repo | 36-54 ms | 33-41 ms | — |

Noise: the nine after-samples (three runs, three passes each) agree exactly, so every
threshold is the measured value minus zero. The base's spread is between passes, not
runs: its cold pass ranked `auth/session.go` and `src/config.rs` first for go-session and
rs-config, its warm and after-edit passes put `ratelimit/bucket.go` and `src/lib.rs`
above them (the base broke ties by name only). The head orders every lane and the final
list by path, line, kind and name.

## Resident BM25 cache (Linux)

`tests/search_hybrid.rs::resident_bm25_cache_stays_within_its_line` (run with
`XMUSTARD_PARITY_FIXTURES`): the index is built into a temporary directory, then
`xmustard-core serve --max-inflight=1` (one glibc arena, as the Go supervisor starts it)
answers 40 queries three times over (30 corpus identifiers, whole or split into words,
and 10 prose phrases). The same run is repeated with `XMUSTARD_SEARCH_TEXT_LANES=off`;
the BM25 cache is the difference.

| Fixture | Lanes off: growth | Lanes on: growth | BM25 cache | bm25 hits returned |
|---|---|---|---|---|
| cline (3,454 files) | +8.2 MiB | +10.8 MiB | **+2.6 MiB** (steady = peak) | 1,368 |
| pi-mono (1,929 files) | +6.2 MiB | +9.3 MiB | **+3.1 MiB** (steady = peak) | 1,119 |

Both are under the ledger's `code_bm25` component (5 MiB steady, 8 MiB peak) and the
WS-18 line (5 MiB on `rust_index_service`). The test asserts both: steady ≤ 5 MiB and peak ≤ 8 MiB.

## One-shot search and the index writer (Linux, `/usr/bin/time -v`, release)

Five queries per fixture; peak RSS of the whole `xmustard-core search` process.

| Fixture | Base search | Head, text lanes off | Head, text lanes on |
|---|---|---|---|
| cline | 10.2-11.1 MiB, 0.38-0.39 s | 10.2-10.7 MiB, 0.20-0.21 s | 13.0-13.4 MiB, 0.21-0.23 s |
| pi-mono | 11.4-12.0 MiB, 0.44-0.45 s | 10.1-10.4 MiB, 0.16-0.17 s | 12.6-12.9 MiB, 0.18 s |

The base reread every tracked doc per query (278 docs on cline, 166 on pi-mono); the
head reads them from the index, so a query takes half the time. The text lanes add
2.5-2.9 MiB to a one-shot reader.

| Fixture | Build peak, base | Build peak, head | `index.db`, base → head | Docs indexed |
|---|---|---|---|---|
| cline | 22.1 MiB, 11.7 s | 22.4 MiB, 11.7 s | 90.7 → 92.1 MiB | 278 (1.6 MiB read) |
| pi-mono | 18.9 MiB, 11.8 s | 18.7 MiB, 11.8 s | 82.5 → 85.2 MiB | 166 (3.6 MiB read) |

The writer stays inside the 25 MiB heavy slot.

## Gate v2

`scripts/bench/rss_v2.sh run --suite ci --repeat 3` on the build box, base tree then
head (`--workstream WS-18 --baseline <base report>`). Reports:
`evidence/2026-09-28/ws18-gate-v2-ci-{base,head}.md`.

| Scenario | Base gate peak (median of 3) | Head gate peak (median of 3) | Verdict |
|---|---|---|---|
| v1-workload | 61.2 MiB | 62.0 MiB | PASS |
| agents-2-relay | 66.3 MiB | 66.0 MiB | PASS |

The ledger check reports WS-18's own line as NOT_CHECKABLE: that line is measured on the
parity `agents-2` scenario, and gate v2 starts the core per call (it strips `XMUSTARD_*`
from the API's environment, so the resident worker never runs). The generic checks held:
tree delta +0.6 MiB (v1-workload) and -0.4 MiB (agents-2-relay) against 15 MiB allowed,
no other component grew, and `rust_core_per_call` was already over its design line at
the base. The resident BM25 cache is therefore measured by the dedicated test above.

## Reproduce

```sh
# retrieval gate (both trees run the extended gate script)
scripts/bench/retrieval-gate.sh --report out.json
# BM25 cache, one-shot readers and the writer (Linux)
XMUSTARD_PARITY_FIXTURES=<dir with cline and pi-mono> \
  cargo test --release --manifest-path rust-core/Cargo.toml --test search_hybrid resident_bm25 -- --nocapture
/usr/bin/time -v env XMUSTARD_INDEX_DIR=<dir> [XMUSTARD_SEARCH_TEXT_LANES=off] \
  rust-core/target/release/xmustard-core search <fixture> ws "<query>" 25
```
