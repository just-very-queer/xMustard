# Isolated HTTP and MCP verification — 2026-09-24

Source: `cd13e2b`. Go API/MCP binaries were built for this check; Rust was the
fresh default-feature debug build used by the Rust audit.

**Result: 18 checks passed.** The harness exited 0. Every harness/API/MCP child
started by this check was stopped; a subsequent process inventory found none
remaining. No user runtime data, provider calls, or UI was used.

## Setup

A temporary Git repository contained `README.md`, `go.mod`, and `main.go`.
The API bound a dynamically selected loopback port and used a temporary data
directory. Five synthetic principals represented an administrator, proposer,
two peers, and readonly reader. Token values stayed in harness memory and were
not printed. Runtime discovery pointed to nonexistent fixture-only executables.

Full HTTP mode supplied the initial fixture scan and baseline. The MCP tests
used `XMUSTARD_CORE_ONLY=1` with required bearer authentication.

## Observations

1. Public HTTP health returned 200 and service `api-go`.
2. Unauthenticated workspace enumeration returned 401.
3. The fixture snapshot scan succeeded with zero issues/signals.
4. Rust indexing baselined three tracked files and `Add`/`main` signatures.
5. Core-only mode rejected `/api/settings` with 404.
6. MCP initialization returned protocol `2024-11-05`.
7. Tool listing returned exactly the nine documented tools.
8. `ground` reported a clean baseline without stale memory.
9. `search Add` found the exact symbol at `main.go:6`.
10. `explain main.go` attributed `Add`/`main` to Rust/tree-sitter Go parsing.
11. `remember` attributed the proposal to the authenticated principal and kept
    it pending with two required verifications.
12. The author's self-approval did not promote the proposal.
13. One independent peer approval did not promote it.
14. Repeating that peer's approval still did not promote it.
15. A second distinct nonauthor peer promoted it to verified.
16. A readonly principal could recall memory but could not propose it (403).
17. Editing the verified source file caused recall to flag it stale and name
    `main.go` in `stale_paths`.
18. Grounding after the edit reported changed content, one dirty file, two dirty
    symbols, a dirty-state blocker, and stale memory.

## Limits and bootstrap behavior

Fresh `POST /api/workspaces/load` with `auto_scan:false` saves a workspace record
but returns 404 when no snapshot exists; indexing also requires that snapshot.
A first scan of the disposable fixture supplied it. Subsequent cached loading
worked. The root setup example uses the CLI's default initial scan.

These checks exercised six tools: ground, search, explain, remember, verify, and
recall. Listing all nine names does not verify live diagnostics, impact, or
failed-run explanation. It also does not prove real agent-client compatibility,
Postgres mirrors, remote TLS, concurrency safety, sustained memory limits, or
improved coding outcomes. The separate source audits expose defects outside this
successful small-fixture path.

Local reproduction artifacts remain under `/tmp/xmustard-runtime-smoke.IDX70v/`
(temporary, not part of the repository). The full local report also records an
initial harness restart caused by closed stdin; that restart left two synthetic
memory entries in the disposable store, both correctly reported stale.
