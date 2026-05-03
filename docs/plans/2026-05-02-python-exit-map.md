# Python Exit Map

Date: 2026-05-02

Phase 2 is complete for xMustard. External validation workspace dirtiness is not a Phase 3 gate.

This map is grounded in the current repository shape: Python still carries the compatibility CLI and much of the orchestration brain, Go already owns a large API delivery shell, Rust owns repo-map/scanner/verification primitives, and Postgres is the durable semantic storage direction.

Completion-pass audit truth on 2026-05-03: xMustard is still in mixed mode. The shipped `semantic-index`, repo-intelligence, changed-symbols, path-symbol/explainer, semantic-search, Postgres foundation, and semantic materialization CLI slices now delegate to Go `xmustard-ops`, and FastAPI no longer registers the single-path `path-symbols`/`explain-path` HTTP reads. The public `TrackerService` compatibility methods for Postgres foundation, Postgres semantic materialization, and semantic-index plan/run/status now delegate to Go too. Python remains in shipped authority through remaining FastAPI routes, broad Typer compatibility commands, `TrackerService` orchestration outside those migrated slices, and stored-semantic compatibility reads.

## Inventory Buckets

### Keep In Python Temporarily

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| `backend/app/cli.py` | Compatibility CLI for issue, run, semantic, retrieval, and tracker commands | Go CLI/API facade plus Rust semantic commands | Existing tests and operator workflows depend on this surface | Breaking local workflows before Go/Rust commands cover them | Go exposes stable delivery endpoints or a CLI wrapper for the same agent-facing reads | Python commands are wrappers only and no caller needs direct `TrackerService` orchestration |
| `backend/app/store.py` | File-backed compatibility persistence | Go delivery over Postgres/file compatibility during transition | Existing artifacts remain the product record while storage migrates | Artifact shape drift | Go/Postgres reads and writes are contract-tested against existing artifacts | Durable Postgres store is primary and file writes are archival/export only |
| `backend/app/postgres.py` | Compatibility semantic materialization helpers and baseline read/write bridge | Go migration/bootstrap plus Rust semantic emitters | Go now owns the shipped CLI/API materialization slices, but storage truth still needs one owner | Split schema ownership | Go owns schema lifecycle and Rust emits contract rows | Python no longer materializes semantic rows or reads semantic baselines |
| `backend/app/runtimes.py` | Legacy runtime adapter and process metadata | Go session/runtime delivery plus Rust process helpers where needed | Runtime surfaces are already partly Go-owned | Process cleanup and terminal behavior regressions | Go owns every live runtime/session request path | Python runtime adapter is unused outside tests |

### Move To Go Next

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| `backend/app/service.py` repo-context/impact/retrieval reads | Legacy compatibility implementation | Go delivery | These are request/response shaping surfaces and should not require Python as product brain | Contract drift with existing Pydantic JSON | Go endpoints and CLI wrappers return impact, repo-context, and retrieval-search payloads from workspace artifacts/git/repo-map | Python methods are test-only compatibility or removed |
| `backend/app/main.py` repo-intelligence routes | FastAPI route owner | Go API | Go already owns adjacent workspace, repo-map, context, run, and verification surfaces | Route parity gaps | Go route inventory and handlers cover `/impact`, `/repo-context`, `/retrieval-search` | FastAPI no longer registers these read handlers |
| `backend/app/service.py` runtime/session delivery | Mixed orchestration and artifact assembly | Go delivery | Go already owns run creation, terminal transport, runtime settings, probe flows | Long-running process cleanup | Go is the only live request path for runtime/session endpoints | Python runtime/session methods are not called by API or CLI |

### Move To Rust Next

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| `backend/app/scanners.py` | Python scan and repo-map compatibility | Rust core | Rust already owns `scan-signals` and `build-repo-map`; parity matters | Python/Rust split-brain scoring | Rust scanner and repo-map are default, not env-optional | Python scanner is test fixture or removed |
| `backend/app/semantic.py` | AST-grep and symbol helpers | Rust core | Structural extraction, symbols, and future LSP/diagnostics belong in the semantic core | Parser/language support regressions | Rust owns symbol extraction and materialized semantic row emission | Python symbol extraction is no longer used by path-symbols/code-explainer/impact |
| `backend/app/service.py` symbol and impact internals | Python derives changed symbols and ranked paths | Rust semantic core, delivered by Go | Symbol and graph-aware ranking are semantic work, not delivery work | Lower-quality impact if moved too early | Rust returns stable symbol/ranking contracts consumed by Go | Python ranking helpers are unused |

### Delete After Replacement

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| Python duplicate repo-intelligence assembly in `backend/app/service.py` | Primary implementation for CLI and FastAPI | Go + Rust | It duplicates the target Go delivery and Rust semantic split | Removing before CLI compatibility exists | Go route parity plus CLI compatibility delegation | No non-test caller imports the Python methods |
| Python-only semantic materialization adapters | Transitional Postgres write helpers | Go bootstrap + Rust emitters | Storage should not be owned by a compatibility shell | Schema drift | Go/Rust contract tests cover rows and freshness statuses | Python materialization path is dead |
| FastAPI route registrations for migrated groups in `backend/app/main.py` | Legacy HTTP path | Go API | Dual live routes hide ownership truth | Deployment confusion | Go route group is the deployed path | FastAPI route group is unreachable in supported runtime |

## Landed Tranche

This pass moved the first repo-intelligence read delivery slice into Go:

- `GET /api/workspaces/{workspace_id}/impact`
- `GET /api/workspaces/{workspace_id}/repo-context`
- `GET /api/workspaces/{workspace_id}/retrieval-search`

The Go implementation reads workspace snapshots, git state, repo-map artifacts, run plans, activity, and fix records directly. Python remains as compatibility CLI/service behavior, but it is no longer the only implementation of these agent-facing read surfaces.

This does shrink Python authority at the API delivery boundary. It does not yet retire Python semantic ranking. The next high-value Rust tranche is to move richer symbol extraction and impact ranking behind a Rust contract so Go can consume that instead of the current Go parser-lite fallback.

## Phase 3 Authority Reduction Landed

The next Phase 3 slice moved the first semantic-impact authority into Rust:

- `rust-core/src/repomap.rs` now emits a `RustSemanticImpactReport` with changed symbols, symbol provenance, likely affected files, and likely affected tests.
- `xmustard-core semantic-impact` exposes that contract as JSON.
- `api-go/internal/rustcore/repomap.go` invokes the Rust command and decodes the typed contract.
- `api-go/internal/workspaceops/workspace_reads.go` now consumes Rust semantic-core output for impact reports and structural retrieval hits.

This keeps the split honest: Go owns request shaping and response delivery, Rust owns semantic meaning, and Python is not part of the new authority path.

## Phase 3 Path-Symbol Authority Reduction Landed

This pass moved the first single-path semantic ownership cut toward Rust:

- `rust-core/src/repomap.rs` now emits a `RustPathSymbolsResult` contract for on-demand path-symbol extraction.
- `xmustard-core path-symbols` exposes that contract as JSON.
- `api-go/internal/rustcore/repomap.go` invokes the Rust command and decodes the typed path-symbol contract.
- `api-go/internal/workspaceops/workspace_reads.go` serves Go `path-symbols` and `explain-path` delivery from Rust semantic-core output.

This does not delete Python CLI compatibility yet. It does move the path-symbol/code-explainer meaning surface in the target Go API lane away from Python and keeps Go as delivery, Rust as meaning, and Postgres as the durable semantic storage direction.

## Phase 3 Code-Explainer Substrate Reduction Landed

This pass moved the next code-explainer ownership slice into Rust:

- `rust-core/src/repomap.rs` now emits a `RustCodeExplainerResult` contract with path role, line/import counts, detected symbols, summary, hints, evidence source, and selection reason.
- `xmustard-core explain-path` exposes that contract as JSON.
- `api-go/internal/rustcore/repomap.go` invokes the Rust command and decodes the typed code-explainer contract.
- `api-go/internal/workspaceops/workspace_reads.go` now serves `explain-path` by forwarding Rust semantic-core output instead of reading the file and assembling the semantic explanation in Go.

This is still a narrow slice: Python compatibility commands and Postgres materialization helpers remain. The real authority reduction is that the target Go API lane no longer owns code-explainer semantic substrate locally, and Python is not reopened as the meaning owner for this path.

## Phase 3 Compatibility Semantic Row Reduction Landed

This pass moved the next compatibility-only semantic seam toward Rust too:

- `rust-core/src/repomap.rs` now emits storage-ready `file_summary_row` and `symbol_rows` inside the `path-symbols` contract.
- `backend/app/service.py` now prefers Rust `path-symbols` and Rust `explain-path` for on-demand compatibility reads whenever stored semantic rows are not fresh.
- Python keeps the stored-semantic read path and a last-resort local parser fallback, but it is no longer the default owner for on-demand code-explainer substrate or symbol-row shaping in the compatibility lane.
- Best-effort semantic consumers now degrade cleanly when Postgres semantic tables are absent, instead of treating missing optional semantic storage as a fatal read-path dependency.

This is still not full Python deletion. `backend/app/postgres.py` remains the compatibility writer into durable state, and Python still owns the last-resort fallback if the Rust command is unavailable. The authority cut is that Rust now defines both the on-demand semantic meaning and the on-demand storage-ready row shape for this slice.

## Phase 3 Diagnostics Contract Boundary Landed

This pass also defined the first diagnostics/LSP-normalized boundary without implementing Python-first LSP:

- `rust-core/src/contracts.rs` now exposes `diagnostics.normalized.v1`.
- Rust is the owner for diagnostic meaning and normalization.
- Go is the delivery owner.
- Postgres `diagnostics` is the durable row target.
- The required field set includes ranges, severity, source kind/name, rule code, fingerprint, and generated timestamp.

This is a contract boundary, not a completed diagnostics runtime. It exists so the later LSP/diagnostics implementation has a Rust-owned shape before any compatibility wrapper can drift into becoming the owner.

## Phase 3 Postgres Foundation Delivery Reduction Landed

This completion pass moved the first Postgres foundation delivery slice into Go:

- `api-go/internal/workspaceops/postgres_foundation.go` now owns settings-backed schema plan/render/bootstrap delivery.
- `api-go/cmd/xmustard-api/main.go` now serves:
  - `GET /api/postgres/plan`
  - `GET /api/postgres/render`
  - `POST /api/postgres/bootstrap`
- Go settings now round-trip `postgres_dsn` and `postgres_schema`, so the foundation endpoints no longer need Python to read or write their configuration contract.

This is not full Postgres ownership exit. Go now owns the shipped `semantic-index` baseline/status operator path through `xmustard-ops`, but Python still retains compatibility helpers around semantic state for non-delegated callers.

## Phase 3 FastAPI Semantic Route Reduction Landed

This completion pass moved the remaining live FastAPI semantic-search and semantic materialization route group into Go:

- `api-go/cmd/xmustard-api/main.go` now serves:
  - `GET /api/workspaces/{workspace_id}/semantic-search`
  - `POST /api/workspaces/{workspace_id}/path-symbols/materialize`
  - `POST /api/workspaces/{workspace_id}/semantic-index/materialize`
  - `POST /api/workspaces/{workspace_id}/semantic-search/materialize`
- `api-go/internal/workspaceops/semantic_materialization.go` now owns ast-grep query delivery, Rust-backed path-symbol row delivery, workspace symbol batch materialization, semantic-search row shaping, and Postgres writes for that HTTP slice.
- `backend/app/main.py` no longer registers those handlers, so Python is off the live request path for this route group.

This is still not a full Python exit. Python continues to own the broader compatibility CLI, `TrackerService` orchestration, and non-delegated semantic baseline/materialization helpers even though the shipped `semantic-index` operator path now delegates to Go.

## Phase 3 Semantic-Index CLI Reduction Landed

This follow-up pass removed one of the last blunt operator seams from Python:

- `api-go/internal/workspaceops/semantic_index.go` now owns semantic-index path selection, plan/run/status payloads, and baseline freshness/persistence for the shipped operator flow.
- `api-go/cmd/xmustard-ops/main.go` now exposes `semantic-index plan`, `semantic-index run`, and `semantic-index status` as a Go-owned ops surface.
- `backend/app/cli.py` keeps the same Typer commands, but they now delegate to `go run ./cmd/xmustard-ops ...` instead of calling `TrackerService.plan_semantic_index(...)`, `run_semantic_index(...)`, or `read_semantic_index_status(...)` directly.

This is a real authority cut, but not a full Python exit. `TrackerService` still contains compatibility implementations for this slice, and the rest of the operator shell is still mostly Python.

## Phase 3 Compatibility CLI Delegation Expansion Landed

This completion pass widened `xmustard-ops` so Python is no longer the CLI authority for surfaces that already had Go delivery:

- `api-go/cmd/xmustard-ops/main.go` now exposes `postgres plan/render/bootstrap`.
- `api-go/cmd/xmustard-ops/main.go` now exposes workspace actions for `impact`, `repo-context`, `retrieval-search`, `path-symbols`, `explain-path`, `semantic-search`, `postgres-materialize-path`, `postgres-materialize-workspace-symbols`, and `postgres-materialize-semantic-search`.
- `backend/app/cli.py` keeps the old Typer command names, but those commands now shell to `go run ./cmd/xmustard-ops ... --data-dir <backend data dir>` instead of calling `TrackerService` or `backend/app/postgres.py` directly.
- CLI tests now assert Go delegation for these migrated slices rather than mocking Python service methods as the implementation owner.

This shrinks the compatibility shell, but it does not make Python disposable. Many Typer commands still call `TrackerService`, and `TrackerService` still carries compatibility implementations for semantic status, stored semantic reads, fallback parser paths, context packet assembly, runs, runtime/session behavior, issue workflows, and persistence glue.

## Phase 3 Single-Path Semantic HTTP And Changed-Symbols CLI Reduction Landed

This completion pass removed the stale Python request and CLI ownership left around the already-migrated single-path semantic reads:

- `backend/app/main.py` no longer registers `GET /api/workspaces/{workspace_id}/path-symbols`.
- `backend/app/main.py` no longer registers `GET /api/workspaces/{workspace_id}/explain-path`.
- `api-go/cmd/xmustard-ops/main.go` now exposes `workspace changed-symbols`.
- `backend/app/cli.py` now delegates `changed-symbols` to Go instead of calling `TrackerService`.
- `backend/app/service.py` no longer exposes `read_changed_symbols(...)` as a public compatibility authority seam.

The ownership moved here is narrow but real: Go is the only shipped HTTP owner for `path-symbols` and `explain-path`, Rust remains the semantic meaning owner behind those reads, and CLI `changed-symbols` now comes from Go/Rust impact instead of Python service assembly.

## Phase 3 TrackerService Compatibility Delegation Landed

The final completion cut narrowed `TrackerService` for the already-migrated Postgres and semantic-index slices:

- `TrackerService.read_postgres_schema_plan(...)`, `render_postgres_schema_sql(...)`, and `bootstrap_postgres_schema(...)` now call Go `xmustard-ops postgres plan/render/bootstrap`.
- `TrackerService.materialize_path_symbols_to_postgres(...)`, `materialize_workspace_symbols_to_postgres(...)`, and `materialize_semantic_search_to_postgres(...)` now call Go `xmustard-ops workspace ...` materialization actions.
- `TrackerService.plan_semantic_index(...)`, `run_semantic_index(...)`, and `read_semantic_index_status(...)` now call Go `xmustard-ops semantic-index ...`.
- The older Python helpers remain in the file as compatibility residue for stored semantic reads and tests, but they are no longer the public `TrackerService` authority for those migrated operations.

This is the Phase 3 landing point: Go owns shipped delivery and operator control for repo-intelligence, semantic-index, semantic-search, and Postgres semantic materialization; Rust owns the migrated semantic meaning contracts; Python is no longer the live authority for those intended Phase 3 paths.

## Phase 3 Boundary

Phase 3 LSP/diagnostics should follow this ownership split:

- Go owns LSP/diagnostic delivery endpoints and request shaping.
- Rust owns language-aware symbol extraction, diagnostics normalization, and later LSP coordination helpers.
- Postgres owns durable diagnostic, baseline, freshness, and replay rows.
- Python remains compatibility only while CLI commands are being delegated.

Do not implement LSP first in Python. If a Python fallback is required, it must be explicitly temporary and contract-compatible with the Rust shape.
