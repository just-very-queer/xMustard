# Benchmarks

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
