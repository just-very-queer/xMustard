# Benchmarks

> Historical working document, retained as evidence of earlier design and work.
> Use [VISION.md](VISION.md), [STATUS.md](STATUS.md), and [ROADMAP.md](ROADMAP.md)
> for current direction, verified behavior, and remaining work.

Reproducible micro-benchmarks for the Rust goal/swarm runtime (`rust-core`).

Run them yourself:

```bash
cd rust-core && cargo build --release --bin xmustard-core
/usr/bin/time -l target/release/xmustard-core bench 2000   # macOS; use -v on Linux
```

The harness (`rust-core/src/benchmark.rs`) is dependency-free, runs against a
self-cleaning scratch directory, and reports per-operation throughput and
latency percentiles as JSON.

## Results

Apple Silicon (arm64), APFS, release build, 2000 iterations per op:

| Operation | ops/sec | mean | p50 | p99 | Notes |
|-----------|--------:|-----:|----:|----:|-------|
| `goal_create`   |  56.5 | 17.7 ms | 17.1 ms | 27.1 ms | durable write × 3 (goals + iterations + ledger), O(store) |
| `goal_iterate`  |  41.0 | 24.4 ms | 24.0 ms | 37.0 ms | durable write × 3, store grows O(n) |
| `ledger_render` | 112.4 |  8.9 ms |  9.0 ms | 11.2 ms | re-render + 1 durable write |
| `swarm_gate`    | 325.0 |  3.1 ms |  3.0 ms |  3.6 ms | read + pure controller decision |
| `goal_list`     | 593.3 |  1.7 ms |  1.7 ms |  2.0 ms | read + sort whole workspace |

- **Binary size:** 3.1 MB. **Test suite:** 58 tests green.
- **Peak RSS under ~10k bulk ops:** 15.3 MB — well under the 50 MB runtime
  budget, and flat across thousands of operations (bounded buffers, no leak).

## Warm symbol-index (cold vs. cache reuse)

The symbol graph (`build_symbol_graph_cached`) persists a per-workspace graph +
per-file symbol cache under `.git/xmustard-cache/`. A cold build tree-sitter-parses
every source file; a warm build reuses the cached parse when file hashes are
unchanged. Measured on **this repo (369 tracked files)**, Apple Silicon, release
build, median of 5 runs each (full cache cleared between cold runs):

| Path | median | Notes |
|------|-------:|-------|
| **cold** (full rebuild) | 676 ms | tree-sitter parse + graph build of all files |
| **warm** (cache hit)    |  37 ms | reuse cached graph/symbols; incl. process start + cluster compute |
| **speedup**             | **18.0×** | the warm-index figure previously quoted as "~20×" |

Reproduce:

```bash
cd rust-core && cargo build --release --bin xmustard-core
BIN=target/release/xmustard-core; R=$(cd ../ && pwd)
rm -f $R/.git/xmustard-cache/symbolgraph-bench-*.json $R/.git/xmustard-cache/symbols-bench.json
time $BIN symbolgraph clusters "$R" bench   # cold
time $BIN symbolgraph clusters "$R" bench   # warm (cache hit)
```

The speedup grows with repo size (parse cost is the cold-path dominant term); 18×
on a 369-file tree is the measured floor for the "warm index" claim, not a ceiling.

## How to read these numbers

The write paths (`goal_create`, `goal_iterate`) are deliberately **crash-safe
and durable**: every mutation is written via temp-file + `fsync` + atomic
`rename`, so a crash or power loss can never leave a torn `goals.json`. That
`fsync` is the dominant cost (mid-teens of milliseconds), and because the store
is a single JSON document the cost scales with the number of records.

That trade-off is the right one here: a goal is an evidence ledger written at
human pace (a handful of records per workspace, a write per worker turn), where
**correctness beats throughput**. The hot path in practice is *reads* —
`list`, `gate`, `context` — which stay at **sub-3 ms / hundreds of ops per
second**.

If a future high-write surface needs it, the `fsync` can be made optional
(atomic `rename` alone still prevents torn files); it is durable-by-default on
purpose today.
