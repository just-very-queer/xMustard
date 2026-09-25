# Stage 1 Go runtime review — PARTIAL (Stage 1 only)

Reviewer: Claude Fable 5.1 (`claude-fable-5-1`), independent of the implementer. Scope: the
Stage 1 Go runtime changes (1b shutdown/cancellation, 1c auth startup, 1d admission and child
limit) plus a recheck of the Stage 1a recall follow-ups, in worktree
`/private/tmp/xmustard-opus-l9b1F4` against baseline `cd13e2b`. Source is truth; the results
doc was used only to locate claims.

Excluded from this verdict (owned by other in-progress jobs): `api-go/internal/evidence`,
`evidence_routes.go`, Pi, and every `rust-core/**` change. Later-stage gaps are not flagged.
The moved route table (`registerRoutes`) was not re-reviewed as new logic.

Snapshot reviewed (sha256 prefix): `cmd/xmustard-api/main.go` 04f3e74ccd25,
`cmd/xmustard-mcp/main.go` 2defb1445ccd, `internal/budget/budget.go` 61a9043ff488,
`internal/rustcore/root.go` bdab4fd77cdc, `internal/rustcore/kill_unix.go` 125bc69f5c67,
`workspaceops/semantic_materialization.go` d8c939bf1997,
`workspaceops/context_governance.go` fe8864e21541, `workspaceops/openai_providers.go`
d5f69054a13b, `workspaceops/grounding.go` 0aeb7960bc5a.

## Verdict: CHANGES_REQUIRED (Stage 1)

One plan requirement is not met in shipped code: the stdio MCP shim admits nothing on
ingress. Everything else reviewed in Stage 1 is correct and evidenced; the remaining findings
are non-blocking.

## Gates run (direct exit codes, `set -o pipefail`)

| Step | Command | Exit |
| --- | --- | --- |
| build | `cd api-go && go build ./...` (final rerun 16:0x) | 0 |
| vet | `go vet ./...` | 0 |
| budget | `go test -race -count=1 ./internal/budget` | 0 |
| rustcore admission | `go test -race -count=1 -run 'Pool\|Refusal\|Cancel\|Kills\|Overflow' ./internal/rustcore` | 0 |
| workspaceops Stage 1 | `go test -count=1 -run 'Recall\|Context\|Drift\|Memory\|Grounding\|Provider\|Semantic\|AstGrep' ./internal/workspaceops` | 0 |
| api Stage 1 | `go test -count=1 -v -run 'Startup\|SIGTERM\|Cancellation\|CoreChild\|RustCapture\|SmallRequest' ./cmd/xmustard-api` | 0 (51 PASS subtests, 0 SKIP: 36 auth cells, 9 cancel routes, 6 top-level) |
| mcp | `go test -count=1 ./cmd/xmustard-mcp` | 0 |
| full | `go test -count=1 ./...` | **1** — `internal/rustcore` (`TestLinkDiagnosticSymbolUsesRustContract`, `TestNormalizeLSPDocumentSymbols`) and `cmd/xmustard-ops` (`TestWorkspaceLoadAndVerificationProfileOperatorLoop`) fail because no release `xmustard-core` exists and the `cargo run` fallback hits Rust compile errors in `rust-core/src/symbolgraph.rs` (in-progress Rust job). Not a Go Stage 1 defect, but `make check-backend` does not pass in this worktree right now. |

Note on masking: an earlier run of these gates piped through `tail` reported exit 0 while the
rustcore package had failed; the table above is from re-runs with direct exit codes. One
`go build ./...` at 15:47 failed on `evidence_routes.go:122` (`[]IdentityLimitation` vs
`[]string`); the same package compiled and passed one minute later, so the evidence owner was
mid-edit. The final build/vet rerun is exit 0.

## Findings

### 1. HIGH — MCP shim ingress (frame read, decode, retained params) is never admitted

`api-go/cmd/xmustard-mcp/main.go:570-601`. The read loop calls `readBoundedLine` (allocates up
to 8 MiB), `strings.TrimSpace(string(raw))` (second copy), and `json.Unmarshal` into
`rpcRequest` whose `Params json.RawMessage` is a third copy retained for the worker's whole
lifetime — all before the in-flight slot (line 594) and before the worker scope (line 601).
The scope then reserves only the API response read in `callAPICtx`. The pool is never
consulted for a single ingress byte, so `XMUSTARD_TRANSIENT_BYTE_BUDGET` has no effect on
request-side memory in the shim. (The env var named in the question,
`XMUSTARD_TRANSIENT_MAX_BYTES`, does not exist; the budget reads `XMUSTARD_TRANSIENT_BYTE_BUDGET`.)

The plan (Stage 1 bullet 5) requires enforced aggregate admission for "request decode, MCP
calls" and to "reserve before streaming ingestion and through decode". The in-flight count
bounds the multiplier but is not admission: worst case is 8 workers × 8 MiB retained params
(64 MiB, the whole default pool) plus the reader's transient copies, none reserved.

Reproduction (standalone harness in the job tmp dir; built shim from this worktree; fake API
holds every request open so workers coexist):

```
pool=1 MiB, 1 frame  of 4 MiB : api received forwarded body 4194318 bytes; shim RSS 51 MB
pool=1 MiB, 4 frames of 4 MiB : all 4 forwarded, all 4 answered ok; shim RSS 88 MB
pool=1 MiB, 6 frames of 7 MiB : all 6 forwarded (7340046 bytes each); shim RSS 183 MB
```

Expected under the plan: the first frame that cannot be reserved is refused with JSON-RPC
`-32000` before it is buffered/decoded, and no unreserved copy of params outlives the read.
Distinct from Stage 2 response projection: this is the request side. The API side does admit
its ingress (declared length reserved at `main.go:382`, chunked bodies read under admission),
so the gap is the shim only.

Suggested fix shape: reserve the frame against the shim scope inside/around `readBoundedLine`
(chunked, before append), create the scope before decode, hand the scope to the worker, and
avoid the `string(raw)` copy. A regression should send N frames under a small pool and assert
a `-32000` reply plus `pool.Peak() <= pool.Max()`.

### 2. MEDIUM (non-blocking) — long-lived captures share the tool child slots and pool, on a background context

`api-go/internal/rustcore/root.go:96` takes a `budget.Children` slot with
`context.Background()` and the 10 s wait, for `RunVerificationProfile`,
`RunVerificationCommand`, `RunManagedCommand` and `RunGoalCommand`. A verification profile can
run for minutes. With the default of 4 slots, four concurrent verification profiles make every
nine-tool call wait 10 s and then answer 503; each such capture may also hold up to 64 MiB
(the whole default pool) for its duration. Not a regression versus `cd13e2b` (which charged
64 MiB unconditionally), but the serving path is now starved by background work, and the
caller's ctx is ignored for the slot wait. Consider a separate limit for long-lived captures
or a reserved share for the nine tools before the combined resource gate.

### 3. LOW — a declared body that can never be admitted answers 503 + Retry-After

`main.go:381-385` reserves `r.ContentLength` before checking it against the per-request cap.
Reproduced against the built binary: `Content-Length: 99999999999` → `503 Service Unavailable`,
`Retry-After: 1`. That request can never succeed, so a compliant client retries forever;
`413` is the correct answer when `ContentLength > limit`. (A declared 40 MB with a short body
returned 200 because the JSON decoder accepts a prefix — pre-existing, not Stage 1.)

### 4. LOW / speculative — unconditional group kill after `Wait`

`root.go:63` and `:117` call `KillProcessTree` after `cmd.Run` returns, including normal exit.
If every member of the group has already exited, the pgid is free and a new unrelated group
leader with that pid (for example the next xMustard child, which is a group leader) could be
signalled. The window is small and I could not reproduce it; noted because the comment in
`kill_unix.go:24-25` claims safety only while a member survives.

### 5. LOW — shutdown does not end in-flight helper children

`srv.Shutdown` waits for handlers but does not cancel their request contexts, and children now
run in their own process groups, so a terminal SIGINT no longer reaches them. After the 15 s
drain the API exits and a still-running Rust/ast-grep child is orphaned until its own timeout
(≤120 s / ≤60 s). Bounded, so informational; a `BaseContext` cancelled at shutdown would close
it.

### 6. Minor

- `budget.AdmittedReader` (`budget.go:237`) has no callers.
- `workspaceops/openai_providers.go` is not gofmt-clean (only changed file that is not).
- `ChildLimit.Acquire` fast path admits an already-cancelled ctx; harmless because
  `exec.Cmd.Start` returns the ctx error before spawning.
- `writeJSON` still encodes the whole response unreserved (disclosed in the results doc,
  deferred to Stage 2). Accepted for Stage 1 as a disclosed gap; not waived for the combined
  resource gate.

## Verified correct

- **Shutdown join**: `main.go:65-104` joins `shutdownDone` after `ListenAndServe` returns;
  drain is bounded (15 s + 10 s). `TestSIGTERMPersistsInterruptedRunBeforeExit` passes against
  the built binary: run file is `interrupted` after exit, worker reaped, `shutdown: services
  closed` logged before exit. `ShutdownInFlight` itself is unchanged from baseline.
- **Auth startup matrix**: `validateStartup` (`main.go:158-177`) refuses `required` without
  credentials on every bind, refuses any non-loopback bind unless `required` + credentials +
  (TLS or explicit insecure override), and `off` is loopback-only. Loopback detection is
  `net.ParseIP(...).IsLoopback()` (`::`, `0.0.0.0`, unresolvable names count as exposed).
  `authMiddleware` is byte-identical to baseline. All 36 cells pass on a real listener with
  unauthenticated probes of five protected routes and an authenticated 200.
- **Cancellation to children and grandchildren**: all nine routes pass `r.Context()`
  (`main.go:2159, 2573, 3738-3745, 3805, 3921, 3933, 4044, 4053`); `runCoreCtx` uses
  `exec.CommandContext` + `IsolateProcessTree` (Setpgid + group SIGKILL on cancel, 2 s
  `WaitDelay`); ast-grep uses the same. `TestToolRouteCancellationKillsBlockedChild` (9/9 with a
  real blocking child) and `TestRunCoreCancelKillsChildAndGrandchild` /
  `TestRunCoreRefusalKillsChildAndGrandchild` pass under `-race`. `remember`/`verify` spawn
  nothing.
- **Byte admission (API side)**: `ByteBudget.Acquire` compares against remaining capacity (no
  `used+n` overflow; `TestAcquireRejectsOverflowingReservation`). `Scope` is deferred in the
  middleware so success, error, cancellation and panic release (`TestScopeReleasedOnPanic`).
  `CaptureWriter` reserves 256 KiB chunks before buffering and runs `OnStop` on the first
  refusal, which kills the producer's whole group; refusal tests assert `InUse()==0` and
  `Peak()<=Max()`. Declared bodies are reserved up front (sub-1 MiB included), chunked bodies
  are read under admission (verified: 4 MiB chunked → 200 under the default pool). Provider
  reads hold the reservation through decode and error past the cap instead of truncating.
  Overload is `503` + `Retry-After: 1` + `{"overloaded":true}` on HTTP and `-32000` on MCP,
  including the shim mapping a 503 from the API.
- **Child count**: `budget.Children` (default 4, 10 s wait) bounds Rust core, bounded
  captures and ast-grep; `TestCoreChildConcurrencyIsBounded` observes peak 2 with the limit
  at 2 and only 200/503 statuses.
- **Grounding bounded**: `boundedStaleMemory` hashes at most 64 baselined memories from
  content-free metadata and reports checked/total/complete; test passes.

## Recheck of prior recall findings (`2026-09-24-fable-recall-review.md`)

| # | Status | Evidence |
| --- | --- | --- |
| 1 implicit focus discarded | Fixed | `score := relevance + boost`; gate only when `explicitSignal` (`context_governance.go` recallOnce). `TestPlainRecallBoostsMemoryAboutChangedFiles` passes. |
| 2 `GET /context` full history to non-admin | Fixed | route requires admin for every filter (`main.go:3772-3781`); `TestContextListRouteIsAdministrative` covers 7 filters × agent/readonly → 403, admin 200, agent POST 200. |
| 3 limit reset instead of clamp | Fixed | `min(n, maxRecallLimit)` (`main.go:3802`); `TestRecallRouteClampsExcessiveLimit`. |
| 4 race test under-asserted | Fixed | asserts attempts=2, returned=2, no withheld. |
| 5 withhold without backfill | Documented | `docs/CONTEXT_LAYER.md` recall contract (lines ~229-233). |
| 6 doc accuracy / stray binary | Fixed | MCP script marked pending; no build binary in the untracked set. |

## Required before Stage 1 approval

Finding 1 only: admit shim ingress before buffering/decoding, keep params under the worker's
scope, add the regression described above, and update the results doc's "MCP shim" line to
describe what is reserved. Findings 2–3 should be scheduled before the combined resource gate;
4–6 are at the owner's discretion.

Human review of the final diff remains required before any merge.
