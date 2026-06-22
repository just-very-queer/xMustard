# xMustard post-hardening prioritized fix queue (HEAD 422bbaf)

Scope note: the uploaded zip is at HEAD `422bbaf`, but `FIXES_APPLIED.jsonl` and `GIT_HISTORY.md` were not present in the package. This queue is based on code inspection, `goal/xmustard_new_findings.jsonl`, `RESEARCH_NOTE_FOR_GPT.md`, and `RETRIEVAL_MEMORY_NOTE.md`.

## P0 — fix before more scale/daemon work

1. **Close terminal workspace-scope bypass** (`XM-POST-001`) — 0.5–1 day  
   Move terminal routes under `/api/workspaces/{workspace_id}/terminal/...`, or add a safe workspace extractor for body/query routes before auth decisions. Add scoped-token 200/403 tests for open/write/read/resize/close.

2. **Make terminal IDs collision-safe** (`XM-POST-002`) — 0.5–1 day  
   Key sessions by `{workspace_id, terminal_id}` and reject duplicate live IDs with `LoadOrStore`. Never overwrite a live process handle.

3. **Prevent duplicate run starts** (`XM-POST-004`) — 1–2 days  
   Add a per-run lock/supervisor around approve/queue/start. Use `LoadOrStore` for active run processes and make approval idempotent after first start.

## P1 — next reliability/scalability layer

4. **Bound managed runs** (`XM-POST-005`) — 2–3 days  
   Add run concurrency limits, context timeout/kill, ring-buffer output capture, capped summaries, and explicit timeout status.

5. **Fix PG mirror sequence durability** (`XM-POST-006`) — 1 day  
   Initialize sequence from `max(mirror_seq)`, or replace process-local sequence with DB sequence/durable per-run revision. Add restart test.

6. **Bound and sanitize all Go→Rust bridge calls** (`XM-POST-007`) — 1–2 days  
   Shared helper: context timeout, stdout/stderr caps, sanitized external errors, server-side full logs.

7. **Finish binary-first bridge migration** (`XM-POST-008`) — 1 day  
   Remove hard-coded `cargo run` from runtime helpers except explicit dev mode. Add no-Cargo packaging test.

8. **Cap Go LSP frame parsing** (`XM-POST-010`) — 0.5–1 day  
   Add max header bytes/lines, max content length, context deadlines, and close-on-protocol-violation.

9. **Make recall drift-honest at O(top-K)** (`XM-POST-011`) — 2 days  
   Metadata-first ranking, bounded candidate window, digest cache, background invalidation. This is the highest moat-per-effort memory change.

10. **Tighten MCP schemas and remember transport** (`XM-POST-012`) — 1–2 days  
    Per-tool JSON Schema with optional fields/types/enums; reject wrong types/unknown fields; send `remember` content in POST body; reject malformed body.

## P2 — architecture and budget work

11. **Terminal idle reaper + natural-exit deletion** (`XM-POST-003`) — 1 day.
12. **Move LSP close off the global lock** (`XM-POST-009`) — 0.5 day.
13. **Cap MCP bridge HTTP response bodies/errors** (`XM-POST-013`) — 0.5 day.
14. **Token snapshot cache / one read per request** (`XM-POST-014`) — 0.5–1 day.
15. **IndexEngine daemon + mmap segments** (`XM-POST-016`) — 3–5 days for first slice.
16. **BLAKE3 + Merkle cache key** — 2–3 days.
17. **Shared streamable-HTTP MCP service + stdio shim** — 3–5 days.

## P3 — correctness polish and roadmap

18. **Expose workspace scopes in principal listings** (`XM-POST-015`) — 0.25 day.
19. **Docs truth pass** — 0.5–1 day: clarify PG source-of-truth vs mirror, LSP lexical/default path, bridge packaging state, runtime budget caveats.
20. **Rust typed error cleanup / non-test unwrap audit** — 1–2 days.
21. **Opt-in real embeddings/HNSW, real dataflow/taint** — after daemon and memory hot-path fixes.

## Three moat-per-effort changes

1. **Metadata-first + top-K drift-check recall**: preserves the “memory never silently rots” moat while making it cheap enough to keep enabled.
2. **Typed memory/MCP schema + body transport**: turns free-text memory into a safer durable object and improves agent self-correction.
3. **Persistent IndexEngine daemon with content-addressed cache**: removes the fork/deserialize floor and makes warm graph retrieval feel native rather than bolted on.
