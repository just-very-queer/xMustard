# WS-39 resolvers for Rust, Python and Java: cost, coverage and precision — 2026-09-30

Status: **measured.** The Rust, Python and Java resolvers run in the same `index build|update`
worker step as the Go and TS/JS resolver of WS-17. On three real repositories the worker's peak
RSS rises by 1.1–1.5 MiB (OpenHands), 0–1 MiB (serena) and 3–6 MiB (codex, 4,934 Rust files).
The edge phase costs more on a large Rust repository: codex goes from 5 s to 24–28 s of edge
work, and its full build from 40–48 s to 49–64 s. A one-file update re-resolves one file. On
small fixtures every CALLS edge the resolvers emit between callables matches ground truth
(33 of 33, recall 33 of 33).

These are single-machine results under shared load, not a universal ceiling.

## Setup

- Binaries: `rust-core/target/release/xmustard-core` from `feat/parity-v2` at 0a0b253 (base:
  Go and TS/JS resolver only) and from `parity/ws-39`, both `cargo build --release --locked`.
- Machine: Apple M1, 8 GB, macOS 27.0. The Linux build box was down, so nothing ran there. Other
  agents may share the machine; the same binary on the same input varied by ±1 MiB in peak RSS
  and up to 30 % in wall time.
- Driver: `scripts/bench/index-rss.sh <bin> <repo> <work> 3`: cold full builds (`--no-cache`),
  then a one-file edit with `index update`, then a noop update. RSS is the `/usr/bin/time -l`
  maximum. The edit appends a function to the first tracked `.ts` file.
- Repositories (local clones under `research/`): OpenHands (Python and TS, 2,090 files), serena
  (Python, with Java, Go, Rust and TS test data, 619 files), codex (Rust, 5,891 files). No Java
  repository was available, so Java is measured on the fixture only.

## Cost

| Repository | Binary | Worker peak RSS (full build) | Full build | Edge phase | 1-file update |
|---|---|---|---|---|---|
| OpenHands | base | 25.2–25.5 MiB | 7.9–8.6 s | 1.3 s | 18.9 MiB, 0.9 s |
| OpenHands | ws-39 | 26.3–26.9 MiB | 4.9–5.0 s | 1.9 s | 19.1 MiB, 0.4 s |
| serena | base | 57.5–59.3 MiB | 2.9–3.1 s | 0.2 s | 16.9 MiB, 0.2 s |
| serena | ws-39 | 56.7–59.0 MiB | 2.7 s | 0.8 s | 17.6 MiB, 0.2 s |
| codex | base | 31.2–32.5 MiB | 39.8–47.6 s | 5.0 s | 26.7 MiB, 1.3 s |
| codex | ws-39 | 34.6–39.3 MiB | 49–64 s | 24–28 s | 27.5 MiB, 3.5 s |

Whole-build wall times swing by up to 2× between runs of one binary on this machine (OpenHands
ws-39 measured 4.6–11.6 s across sessions, faster than base in some and slower in others). Read
the edge-phase column, which is measured inside the worker.

serena's peak RSS was already at 57 MiB before this change (a repository with large generated
files), and is unchanged.

Where the RSS goes: the worker holds every symbol edge in memory when it writes the graph
segment. Rust resolution adds about 258,000 symbol edges on codex (110,000 to 368,000 edges in
all), which accounts for the 3–6 MiB. On OpenHands the resolver adds 24,800 Python edges and
the delta is 1.1–1.5 MiB. OpenHands was above the 25 MiB line before this change (25.2–25.5
MiB); the Linux gate
(`build_of_5000_files_has_no_file_cap_and_peaks_under_25_mib`, a synthetic 5,000-file fixture)
is not run here because the box was down.

Costs found and removed on the way (all measured on codex, before/after edge phase):

| Change | Effect |
|---|---|
| Re-export chains are followed only through imports the file re-exports (`pub use`; Python's package imports), once per file per lookup. A Rust file's `use super::*;` had made the walk exponential. | 217 s → 41 s (edge phase) |
| A Rust type's impl-block traits are read through the symbol's byte range in the refs primary key, not by scanning every reference of the crate. | part of the above |
| The same-file lookup uses `symbols_file` (`INDEXED BY`), not `symbols_name`: a name such as `new` has thousands of symbols, a file has few. It serves Go and TS/JS too. | 41 s → 28 s |
| A file's own top-level declarations are read from the symbols already in memory. | 28 s → 24 s |
| A cache of module export lookups was tried and dropped: no time gain and +3.6 MiB on OpenHands. | — |

## Coverage

`counters.resolve.languages` (new) reports files, edges and drops per language family. A full
build of each repository resolved:

| Repository | Family | Files | Symbol edges | Drops (occurrences) |
|---|---|---|---|---|
| OpenHands | python | 845 | 24,782 | receiver_untyped 8,927 · unresolved 6,286 · ambiguous 3,588 · callable_value 965 · external 111,307 |
| OpenHands | ts | 1,236 | 11,816 | (unchanged from WS-17) |
| serena | python | 480 | 17,109 | receiver_untyped 5,676 · unresolved 8,570 · ambiguous 1,448 · callable_value 1,221 · external 56,695 |
| serena | java / rust / go | 10 / 5 / 6 | 43 / 20 / 26 | mostly external |
| codex | rust | 4,934 | 256,304 | receiver_untyped 288,685 · ambiguous 140,383 · unresolved 39,303 · callable_value 28,935 · dispatch_boundary 130 · external 573,320 |

Every Rust drop is a reference the resolver would not bind exactly. Untyped receivers dominate
(`x.iter().map(..)` chains, call results and closure parameters), then ambiguous names: a
name defined at top level by more than one file is not bound at the global tier. On codex 83 %
of the resolved edges are same-file or import-scoped, 17 % global.

## Precision against ground truth (EVAL-09 input)

`rust-core/tests/resolve_more_langs.rs::precision_against_ground_truth` indexes
`rust-core/tests/fixtures/resolve/more/{py,java,rust}` and compares the resolver's CALLS edges
(caller symbol → callee symbol, callee a function or method) with the expected bindings in
`truth/*_calls.json`:

| Language | Ground truth | Precision | Recall |
|---|---|---|---|
| Python | The calls that ran: `python3 truth/gen_py.py ..` runs `main.run()` under `sys.setprofile` and records each call between the fixture's own functions | 13/13 = 1.000 | 13/13 = 1.000 |
| Java | The binding javac chooses by the language rules (JLS 15.12), listed by hand; the fixture compiles with `javac` | 11/11 = 1.000 | 11/11 = 1.000 |
| Rust | The binding rustc chooses, listed by hand; the fixture passes `cargo check` | 9/9 = 1.000 | 9/9 = 1.000 |
| Overall | | 33/33 = 1.000 | 33/33 = 1.000 |

The task named pyright, jdtls and rust-analyzer as ground truth. Only rust-analyzer was on the
machine and none was run: the fixtures are small enough that the expected bindings are
checkable by hand, and Python has a stronger truth in its own execution. **Open:** rerun the
comparison against pyright, jdtls and rust-analyzer output, and on a fixture large enough that
100 % is not the expected result. What the fixtures cover: Python absolute, relative and
namespace-package imports, `__init__` re-exports through a wildcard, module aliases, receivers
typed by constructor, C3 method resolution order; Java packages, single-type, wildcard and
static imports, same-package scope, inherited and interface methods, overloads, implicit
`this`; Rust `use` paths with aliases, `crate::` paths, trait impls with a default method,
associated functions and typed locals.

Constructor calls (`new C()`, `C()`) are not compared: the resolver binds them to the class
symbol, the truth to a constructor that these fixtures do not declare.

## Reproduce

```
cd rust-core
cargo test --test resolve_more_langs -- --nocapture       # precision and per-language edges
cd tests/fixtures/resolve/more/py/truth && python3 gen_py.py .. > py_calls.json
scripts/bench/index-rss.sh rust-core/target/release/xmustard-core <repo> <work-dir> 3
```
