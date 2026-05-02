# Python Exit Map

Date: 2026-05-02

Phase 2 is complete for xMustard. External validation workspace dirtiness is not a Phase 3 gate.

This map is grounded in the current repository shape: Python still carries the compatibility CLI and much of the orchestration brain, Go already owns a large API delivery shell, Rust owns repo-map/scanner/verification primitives, and Postgres is the durable semantic storage direction.

## Inventory Buckets

### Keep In Python Temporarily

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| `backend/app/cli.py` | Compatibility CLI for issue, run, semantic, retrieval, and tracker commands | Go CLI/API facade plus Rust semantic commands | Existing tests and operator workflows depend on this surface | Breaking local workflows before Go/Rust commands cover them | Go exposes stable delivery endpoints or a CLI wrapper for the same agent-facing reads | Python commands are wrappers only and no caller needs direct `TrackerService` orchestration |
| `backend/app/store.py` | File-backed compatibility persistence | Go delivery over Postgres/file compatibility during transition | Existing artifacts remain the product record while storage migrates | Artifact shape drift | Go/Postgres reads and writes are contract-tested against existing artifacts | Durable Postgres store is primary and file writes are archival/export only |
| `backend/app/postgres.py` | Semantic schema bootstrap and materialization helpers | Go migration/bootstrap plus Rust semantic emitters | It is already a bridge around the durable store | Split schema ownership | Go owns schema lifecycle and Rust emits contract rows | Python no longer materializes semantic rows |
| `backend/app/runtimes.py` | Legacy runtime adapter and process metadata | Go session/runtime delivery plus Rust process helpers where needed | Runtime surfaces are already partly Go-owned | Process cleanup and terminal behavior regressions | Go owns every live runtime/session request path | Python runtime adapter is unused outside tests |

### Move To Go Next

| File/module | Current authority | Target owner | Why | Migration risk | Cutover condition | Deletion condition |
|---|---|---|---|---|---|---|
| `backend/app/service.py` repo-context/impact/retrieval reads | Agent-facing repo intelligence assembly | Go delivery | These are request/response shaping surfaces and should not require Python as product brain | Contract drift with existing Pydantic JSON | Go endpoints return impact, repo-context, and retrieval-search payloads from workspace artifacts/git/repo-map | Python methods are compatibility CLI wrappers or removed |
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

## Phase 3 Boundary

Phase 3 LSP/diagnostics should follow this ownership split:

- Go owns LSP/diagnostic delivery endpoints and request shaping.
- Rust owns language-aware symbol extraction, diagnostics normalization, and later LSP coordination helpers.
- Postgres owns durable diagnostic, baseline, freshness, and replay rows.
- Python remains compatibility only while CLI commands are being delegated.

Do not implement LSP first in Python. If a Python fallback is required, it must be explicitly temporary and contract-compatible with the Rust shape.
