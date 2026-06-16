# Python → Rust Migration Plan

Goal: retire the Python backend (`backend/app/`, ~16.2k LOC) by moving logic into
the Rust core (`rust-core`) and the thin Go shell (`api-go`). Per
`docs/MIGRATION_RUST_GO.md`: **no flag-day rewrite — move ownership by subsystem,
smallest/most-isolated first, each step parity-verified.** The proven pattern is
the goal cutover (PR #6): Python/Go logic → Rust core → Go delegates via
`rustcore.Run*Command`, behind a parity test.

## Per-module status

| Module | LOC | Target owner | Status |
|---|---:|---|---|
| `scanners.py` | 725 | rust-core | **Shadowed** by `scanner.rs` — verify parity, then delete |
| `semantic.py` | 263 | rust-core | `extract_path_symbols` shadowed by `repomap.rs`; `run_ast_grep_query` → **porting now** (`semantic.rs`) |
| `terminal.py` | 113 | rust-core + Go shell | Runtime/terminal process plane — `contracts.rs` `next_removable_python_boundary` |
| `runtimes.py` | 490 | rust-core + Go shell | Process control; partly shadowed by `verification.rs run_managed_command` |
| `store.py` | 422 | Go (Rust-aware later) | File persistence; keep formats stable |
| `postgres.py` | 724 | Go (`api-go`) | DB layer already moving to Go |
| `main.py` | 1120 | Go (`api-go`) | HTTP surface, route-by-route (mostly migrated) |
| `cli.py` | 1917 | rust-core CLI + `xmustard-ops` | Subcommand-by-subcommand |
| `models.py` | 2275 | Rust structs / Go types | Wire contract; ported alongside each module |
| `service.py` | 8130 | split | Orchestration; much already shadowed (goals, scanner, repomap, verification, diagnostics, lsp). Split by subsystem, port **last** |

## Order (smallest/isolated → largest/entangled)

1. **`semantic.py` ast-grep search → `rust-core/src/semantic.rs`** ← this session (opencode)
2. `scanners.py`: verify shadow → delete
3. `terminal.py` + `runtimes.py` → rust-core process plane (the documented next cut)
4. `store.py` / `postgres.py` → Go
5. `main.py` / `cli.py` remaining routes/commands → Go / Rust CLI
6. `models.py` ported incrementally with each subsystem
7. `service.py` split by subsystem, ported last

## Execution loop (per module)

1. opencode/deepseek-v4-pro ports the module to Rust in an isolated worktree.
2. The Rust port ships with unit tests on the pure logic.
3. Controller (Claude) independently re-runs `cargo test` + reviews the diff.
4. Go shell delegates to the new Rust command (parity test where a contract exists).
5. Python module is deleted once parity holds; record here.

## Progress log

- _(this session)_ Plan written; `semantic.py` ast-grep search port in progress.
