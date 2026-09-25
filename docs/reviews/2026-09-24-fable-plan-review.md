# Plan review: lean context implementation — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: `docs/plans/2026-09-24-lean-context-implementation.md`.
Checked against `VISION.md`, `CONTEXT_LAYER.md`, the three dated audits, and source.

## Verdict: CHANGES_REQUIRED

Steps 1–3 are correctly anchored to reproduced or source-confirmed defects and are
implementable as written. Step 4 rests on an unsupported Pi interface, Step 2's
expansion path is unspecified per client, and Step 5's gates are not executable.
Amend before Opus 5.5 implements; no easier goal is substituted below.

## P1 issues

**1. Step 4 assumes Pi consumes MCP results. Pi core ships no MCP client.**
Pi's README lists eight built-in tools and explicitly declines MCP; xMustard's
stdio shim (`api-go/cmd/xmustard-mcp/main.go:393`) advertises only `tools`.
There is no local clone under `research/` and no repo evidence that Pi loads
`xmustard-mcp`. The "HTTP→stdio MCP→Pi" workflow in Step 5 therefore has no
supported path. Amend: the Pi adapter registers the nine tools directly through
Pi's extension tool registration, calling the Go HTTP API, and `tool_result`
transforms those registered tools. Keep the stdio shim as the second client.
If the owner wants Pi to reach the shim, pin a named third-party MCP extension
as a separate dependency and test it; do not assume it.

**2. `ctx.signal` on the `tool_result` hook is unverified.** Pi passes an
`AbortSignal` to custom-tool `execute`; the research doc does not confirm one on
extension event context. Amend: adapter owns an `AbortController` with a bounded
timeout (default 5 s) and forwards abort to the Go request. State that Pi-side
cancellation reaching Go is conditional on the pinned API; test both branches.

**3. Expansion path is undefined for each client.** Step 2 says "API/MCP
resource read". The shim has no `resources/*` methods, and Pi has no MCP resource
concept. Amend: define one Go HTTP endpoint, `GET /api/workspaces/{id}/evidence/
{observation_id}?offset&length`, as the canonical read; add `resources/read` to the
shim (capability change only; tool names stay stable); the Pi adapter registers one
inactive `xmustard_expand` tool. Record the client-visible handle format once.

**4. "Same-authenticated-principal" is vacuous in the default local mode.**
Under loopback `auto` with no tokens, callers collapse to one identity
(`main.go:3758`). Amend: scope evidence reads by workspace + issuing session/call
ID always, principal additionally when auth is enforced; state that unauthenticated
local mode gives workspace-level isolation only. Specify how the Pi adapter presents
a bearer token (`XMUSTARD_TOKEN`).

**5. Admission cannot govern Rust allocations across the process boundary.**
Step 1 lists "concurrent indexing" under Go aggregate admission. The Go pool
(`budget.go:85`, 64 MiB default) bounds captures, not the child's RSS. Amend:
Go admits capture bytes and concurrent child count; Rust bounds its own reads
(`symbolgraph.rs:286` reader) and reports them. Keep the audit's wording:
enforced admission is not a process-memory ceiling; RSS is proven only in Step 5.

## P2 issues

**6. Dirty-file key still relies on size+mtime.** Fixing NUL parsing
(`indexcache.rs:54,73`) repairs the reproduced defect but two edits within one
mtime tick and equal length still collide. Amend: hash dirty-file content in
`cheap_key`; add a same-size same-mtime regression.

**7. Expansion after repository mutation must label staleness.** Step 2 forbids
falling back to new content but does not require the read to compare the stored
revision key against the current one. Amend: every expansion response carries
`captured_key`, `current_key`, and `stale: bool`.

**8. "One shared snapshot" needs a cross-workspace key.** Graph caches are per
`workspace_id` (`indexcache.rs:171`), so concurrent agents duplicate builds.
Amend: key caches by content key plus parser version; workspace ID becomes a
pointer. `atomic_write` already exists; add a stale-temp sweep test.

**9. Auth amendment is stricter than needed and under-specified.** Requiring
`required` and credentials is fine; also state that `required` with no credentials
is a startup failure, and that `off` on non-loopback is always fatal (`main.go:3738`).

**10. Step 5 gates are not executable.** Only `make check-backend` is a command.
Amend with exact artifacts: `scripts/e2e/pi-adapter.sh` (temp repo, temp data dir,
random port, pinned Pi version), `scripts/bench/rss.sh` sampling `ps -o rss` at
100 ms over the xMustard tree, and results written to
`docs/benchmarks/2026-XX-XX-lean-context.md`. Recall quality and task completion
have no corpus in this program; label them "not measured" rather than "record".

**11. Per-result cap vs delivery caps.** 16 MiB per capture equals the shim's
response cap (`main.go:235`) but exceeds the 8 MiB inbound frame. State that
projections, not originals, cross the MCP frame, and that expansion reads are
paged under the frame limit.

## Required tests (beyond those listed)

- Pi: registered-tool `tool_result` replacement, unchanged non-xMustard tools,
  Go unreachable → bounded original, abort during Go call, pinned version check.
- Evidence: expansion returns `stale=true` after a commit; paged read across the
  8 MiB frame; handle from workspace A rejected in B under both auth modes.
- Index: 70-symbol file fully searchable; same-size same-mtime dirty edit; two
  processes building the same key concurrently leave one valid cache.
- Budget: N concurrent captures where N × cap exceeds pool returns overload,
  never grows RSS beyond pool plus base; child count limit observed.
- Auth: full matrix from Step 1 run against a real non-loopback listener.

## Confirmed correct

Anchors for §1 items 1–3, 5, 6 and §3 caps (`treesitter.rs:118`,
`repomap.rs:442`, `root.go:53`, `main.go:3223,3783`) match source. Scope limits,
resource-target language, and human-merge statement align with `VISION.md`.
