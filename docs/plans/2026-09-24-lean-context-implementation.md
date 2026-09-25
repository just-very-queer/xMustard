# Lean context implementation plan

## Goal and order

Deliver one correctness-first program for existing agents: make current retrieval
dependable, add bounded and recoverable delivery for xMustard MCP results, improve
source-correct incremental indexing, then connect one real client through Pi's
extension hooks. Go remains the owner of delivery, authorization, retention, and
storage; Rust remains the owner of repository identity and indexing. The Pi adapter
calls the shared Go implementation directly over HTTP and registers the nine existing
xMustard tools as Pi extension tools. Pi does not ship an MCP client; the current
stdio MCP server remains the second supported client and gains resource reads for
expansion. No new daemon is needed.

Order: contract repairs, shared evidence delivery, incremental retrieval/coverage,
Pi adapter, then combined verification. Add a focused regression for every audited
defect before changing behavior. Keep the nine MCP tool names and schemas stable.

## 1. Repair audited contracts

Work in `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-mcp/main.go`,
`api-go/internal/workspaceops/context_governance.go`, `api-go/internal/rustcore/`,
`api-go/internal/budget/`, and `rust-core/src/indexcache.rs`.

- Route no-argument `GET /context/active` through bounded, ranked `RecallContext`;
  retain full history only behind an explicitly administrative read. Test empty,
  queried, path-filtered, and large-history requests through HTTP and stdio MCP.
- Make recall bind content to the exact promoted metadata version. Add a deterministic
  update/recall interleaving that proves revised, unverified content cannot inherit
  the old approval state; retry on mismatch or fail closed.
- Join the signal-shutdown goroutine before `main` exits. Thread request context from
  all nine MCP tool paths through handlers and `internal/rustcore` into child
  processes. Tests must cancel an actual blocked child, observe its exit, and prove
  shutdown persists interruption state and closes services before process exit.
- Correct authorization startup: non-loopback requires
  `XMUSTARD_AUTH=required` and configured credentials; `off` is always fatal on a
  non-loopback bind, even with tokens or the insecure-bind override. `required`
  without credentials is a startup failure on every bind. Exercise loopback/remote,
  auto/required/off, configured/unconfigured credentials, TLS and explicit
  insecure-bind combinations against a real non-loopback listener; unauthenticated
  requests must never reach protected routes.
- Replace advisory transient-byte charging with enforced aggregate admission for
  request decode, MCP calls, Go→Rust captures and provider/child output. Reserve
  before streaming ingestion and through decode, projection, storage and response
  construction; release on success, error, cancellation and panic. Bound concurrent
  child count separately. Rust must bound its own reads and child output. Byte
  admission is not an RSS ceiling; return protocol-correct overload errors and never
  use an unbounded fallback.
- In `indexcache.rs`, parse `git status --porcelain=v1 -z --untracked-files=all`
  without trimming, represent rename pairs/deletions/untracked and unusual filenames,
  and hash dirty-file bytes into the key. Regress dirty→dirty edits including
  same-size edits within one mtime tick, rename, deletion, branch switch, untracked
  changes, and whitespace/newline filenames against warm cache results.

Completion criterion: each defect has a test that failed before the fix, HTTP/MCP
contract tests pass, and cancellation/shutdown/auth/budget tests exercise real
process or listener behavior where the finding concerns that boundary.

## 2. Add one bounded, recoverable evidence-delivery module

Add a small Go module under `api-go/internal/` with one narrow interface to capture
scoped observations, return bounded deterministic projections, and read authorized
original ranges. Keep selection, admission, raw retention and expiry there. Expose
`GET /api/workspaces/{workspace_id}/evidence/{handle}?offset=N&length=N` and MCP
`resources/read` for `xmustard://evidence/{handle}?offset=N&length=N`; do not add a
tenth core MCP tool. Pi adds `xmustard_expand`, inactive until a recovery handle is
emitted, then enabled through its documented active-tool control. It calls the same
Go endpoint. Both clients page through this shared implementation.

Use an initial maximum of 16 MiB per captured original, 1 MiB per delivered
projection, 64 KiB per expansion page, 256 MiB of retained originals per workspace,
and a 24-hour default retention period. Only projections travel in tool results;
originals are fetched in 64 KiB pages, below the 8 MiB MCP frame limit. Use a stable
opaque handle: `xm1.` plus 256 random bits encoded base64url. Persist quotas, issuance
scope and expiry with the artifact. Revoke handles on workspace deletion/revocation
and deny expired or tampered handles. When quota is full, reject new retention with
an explicit error; never evict an unexpired original whose recovery was promised.
Configuration may lower these limits; any increase needs a new resource measurement
and retention statement.

Each observation records workspace/repo trust scope, actor when authenticated,
issuer/session/call IDs, source tool/version, argument digest, capture time,
revision/dirty identity, status/error, content type, raw hash/size and expiry. The
revocable unguessable handle is bound to workspace and observation; session IDs are
audit metadata, never identity. Enforced auth also requires the issuing principal.
Loopback `auto` without credentials provides workspace-level isolation only. Pi reads
optional `XMUSTARD_TOKEN`; required auth without a valid token fails closed.

Record each projection's observation, selected ranges, reducer version, byte counts
and omissions. Store raw before projecting; stream/spool under ingress and disk
quotas. Return explicit expired, denied or missing errors; never substitute new
repository content after expiry. Captured evidence is not verified memory.

Start with deterministic text reduction preserving tool/call identity, result schema,
errors, exit status, exact paths/spans and failure evidence. Include omissions and an
expansion path. Pass unsupported structured/multimodal results unchanged only within
limits; otherwise return a protocol-compatible size error. Do not alter command
arguments or permissions. Expansion reports `captured_key`, `current_key`, `stale`
and serves captured bytes only.

Tests cover one failure among many passes, contradictions, stack traces, large
structured/unsupported output, concurrent similar calls, streaming cancellation,
cross-workspace/principal reads, tampered handles, full-quota rejection, expiry and
restart. A projection passes only if its authorized original reads until expiry and
is denied afterward.

## 3. Make indexing incremental and honest

In `rust-core/src/{repomap,treesitter,symbolgraph,indexcache,scanner,semantic,lsp_session}.rs`,
repair the 32/64-symbol caps so extraction completeness is separate from display
limits. Extend coverage to report skipped, unreadable, invalid-UTF-8, oversized,
unsupported, and symbol-truncated files with reasons; a file count must not imply its
symbols were indexed. Replace `unwrap_or_default` where it conceals failed source
reads. Apply the existing bounded, no-follow file reader to scanner and LSP source
reads, bound ast-grep output while streaming with a child timeout, and cap LSP headers
and queued messages. Release or avoid retaining full file bodies after graph building
when downstream edges no longer need them.

Preserve a durable per-file content identity and parser-version identity. Reparse
only changed files, remove deleted/renamed entries, invalidate dependent references,
and make one shared snapshot serve agents using the same canonical repository and
trust scope. Cache keys include canonical repo identity, content key and parser
version; workspace ID points to an authorized snapshot and never substitutes for
repo/trust scope. Never reuse or disclose a graph across repositories by matching
content hashes alone. Serialize competing builders for one key and sweep stale
atomic-write temp files. Add counters for files/bytes read and reparsed, symbols
extracted, coverage losses and cache hits; expose them in existing diagnostic and
coverage results. Do not introduce embeddings, a resident daemon, or broad graph
expansion in this program.

Regression fixtures must retrieve a symbol beyond prior limits; report intentionally
truncated and unreadable inputs; verify rename/delete and repeated dirty edits; prove
one-file edits do not parse unchanged files; and ensure source output reflects the
same revision as its reported identity. Include cold and warm runs.

## 4. Add one Pi adapter

At the Pi revision pinned in
[`TOOL_CONTEXT_RESEARCH_2026-09-24.md`](../research/TOOL_CONTEXT_RESEARCH_2026-09-24.md),
implement a TypeScript extension that registers nine HTTP-backed xMustard tools with
their current schemas and routes, plus inactive `xmustard_expand`. Use Pi's
documented `pi.on("tool_result")` hook to pass these results through Go; leave other
tools untouched. Respect event types and use `ctx.signal` when present. Also enforce
a five-second adapter timeout and forward aborts to Go. If Pi supplies no signal,
document the limitation; timeout still bounds the request. On endpoint or projection
failure, preserve a bounded original or return an explicit size error. Start no
sidecar or timer from extension load.

Send `XMUSTARD_TOKEN` as bearer auth when set; never log it. Pin Pi's version. In a
real Pi runtime with scripted local fake-model transport, prove all nine tools work,
`tool_result` appears in the next model request, errors stay errors, and
`xmustard_expand` activates and retrieves 64 KiB pages. Check stale labels,
signal-present/absent timeout behavior, unreachable Go, and non-xMustard pass-through.
The fake transport makes no provider request and needs no private credential copy.
Publish coverage; do not claim access to Pi host-native tools or other clients.

## 5. Acceptance and handoff

Use temporary repositories, data roots, listeners and subprocess fixtures. Run
`make check-backend`. Add executable `scripts/e2e/mcp-evidence.sh` for HTTP plus
stdio-MCP tools/resources and `scripts/e2e/pi-adapter.sh` for a pinned Pi version,
temporary repo/data, random API port and direct extension-tool workflow. Both scripts
must cover restart, cancellation/timeout, expiry, concurrent calls, repository
mutation and auth; the Pi script also covers an unreachable Go endpoint and
non-xMustard pass-through. Add `scripts/bench/rss.sh`, sampling `ps -axo pid,ppid,rss`
every 100 ms and tracking all xMustard-owned descendants (API, MCP shim, Rust child)
to report peak RSS. Its fixed workload is a generated repo of 500 tracked Go/Rust/TS
files (about 20 MiB), one 70-symbol file, a warm query, same-size dirty edit,
rename/delete cycle, two index clients and five concurrent 16 MiB capture attempts
against the 64 MiB transient pool. At least one capture must fail admission, in-use
bytes must never exceed the pool, and child-count limits must hold; separately record
peak RSS without treating pool use as an RSS guarantee. Report the xMustard-owned tree
and the full Pi workflow
separately; the latter includes Pi as an external process.

Include a committed 12-query retrieval fixture with gold paths/spans across Go, Rust
and TypeScript, including a failure-only evidence case and one contradiction. On cold
and warm runs, require at least 11/12 gold paths in top five, no stale hit presented as
current, explicit coverage loss for every omitted/unreadable fixture, and unchanged
or improved recall after one-file edits without reparsing unchanged files. This is an
internal retrieval regression gate, not broad competitor parity or proof of accepted
patch/task success. Do not add a paid-model run; broader task-quality and competitor
comparison remain future work. Record invocation results at
`docs/benchmarks/2026-09-24-lean-context.md`: latency, CPU, allocations/copies,
files/bytes read and reparsed, context retained/delivered, expansion/retry counts,
admission outcomes and peak RSS, including capture through storage and every
re-expansion. The active xMustard-owned tree targets 50–100 MB and passes only at or
below 100 MB for this workload. External Pi/compiler processes are reported
separately and as a complete workflow. API usage/cost is diagnostic only; hardware
memory bandwidth is separately measured or labeled unmeasured. Byte admission does
not prove an RSS ceiling.

Required deliverables: focused regression tests for every repaired audit finding;
the shared delivery module, handle format, paging and retention/auth notes; source-
linked coverage and incremental-work diagnostics; nine direct Pi tool definitions
plus `xmustard_expand` and `resources/read` for stdio MCP; the pinned Pi extension
and setup/conformance instructions; the three scripts above; the 12-query fixture;
reproducible end-to-end/resource results; and a concise status update marking only
evidenced behavior complete. Claude Fable 5.1 reviews this plan before
implementation; Claude Opus 5.5 implements against the reviewed plan. Human review
of the exact final diff remains required before any merge.

## Final implementation clarifications

Expansion returns standard base64-encoded bytes so arbitrary originals round-trip exactly; test
UTF-8 page boundaries and invalid UTF-8. Advertise MCP resources capability and
return protocol-correct resource errors. Serialize cache builds across processes
with a bounded lock wait and explicit timeout. Pi tool `execute` uses its
`AbortSignal` and a separate 60-second execution timeout; result projection has its
own five-second timeout. Bind retained evidence to stable `Principal.ID`, never the
token secret; token rotation for that principal must still read, while another
principal is denied. Oversized dirty files are visibly excluded/degraded, not served
from stale cache; a capped read/hash cannot claim full content identity. For RSS,
keep the 100 ms sampled-tree gate and report per-process child high-water separately
in platform-correct units. Sampling may miss short peaks: label the result sampled
peak and do not claim that the larger of sampled-tree or child-high-water is the
true process-tree peak or a universal RSS ceiling.

## Scope limits

No UI work, Docker, new agent loop, provider/model gateway, hosted helper model,
local model download, API-price target, generalized upstream MCP proxy, universal
native-tool interception, or full competitor parity claim. Keep cross-repository
sharing, optional SQL correctness, human merge enforcement, and the retained goal
runtime's concurrent-create/failed-verification defects as separate follow-up work.
Do not claim the resource target, task-quality parity, or full source coverage from
unit tests alone.
