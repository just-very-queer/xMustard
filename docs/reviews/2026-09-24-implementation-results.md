# Lean context implementation results — 2026-09-24

Implementer: Claude Opus 5.5. Plan: [`docs/plans/2026-09-24-lean-context-implementation.md`](../plans/2026-09-24-lean-context-implementation.md)
(frozen 1847-word version). Worktree: `/private/tmp/xmustard-opus-l9b1F4`, detached at `cd13e2b`,
seeded documentation preserved. **Nothing committed or pushed.** Human merge authority applies.

This report is updated after each completed stage. "Red" means the named test was run against
unchanged behavior and failed for the audited reason; "green" means the same test passed after the fix.
Test seams (no behavior change) were added before red runs so failures are behavioral, not compile errors.

## Baseline (before any change)

| Command | Result |
| --- | --- |
| `cd api-go && go build ./... && go test ./...` | all 6 packages ok |
| `cd rust-core && cargo test` | 114 passed |
| `cd rust-core && cargo clippy` | exit 0, 12 warnings |

Toolchain: go1.26.1 darwin/arm64, cargo/rustc 1.93.1, node v26.7.0, npm 11.19.0.

## Stage 1 — audited contract repairs

### 1a. Bounded plain recall and version-bound recall content

Structural prerequisite: `api-go/cmd/xmustard-api/main.go` route registration moved verbatim from
`main()` into `registerRoutes(mux)` (plus package-level `dataDir()`), so route tests use the real
route table through `httptest`.

Regressions (red before fix):

```
go test ./internal/workspaceops -run 'TestRecallNeverAttaches|TestPlainRecallWithDirty' -count=1
--- FAIL: TestRecallNeverAttachesRevisedContentToOldApproval
    revised unverified content returned with approval state status=verified promoted=true verifications=[{peer true  }]
--- FAIL: TestPlainRecallWithDirtyTreeStillReturnsRecencyTopN
    plain recall on a dirty tree returned 0 entries, want 8
go test ./cmd/xmustard-api -run 'TestRecallRoute' -count=1
--- FAIL: TestRecallRouteNoArgumentIsBounded
    plain recall returned 40 entries of 40; want bounded top-8
--- FAIL: TestRecallRouteFullHistoryIsAdministrative
    agent full-history read: want 403, got 200
```

The first failure is the deterministic reproduction of the Go audit's *inferred* metadata/content
race (now confirmed). The second is an additional defect found while routing plain recall through
`RecallContext`: implicit working-change paths gated relevance, so a dirty tree hid all memory.

Fix:
- `GET /context/active` always uses `RecallContext` (default 8, caller limit capped at 50). The full
  promoted history is `?scope=all` and requires the admin role (`requireRole`).
- `RecallContext` binds each returned body to the ranked metadata's content hash. On mismatch it
  re-runs the whole recall (3 attempts), then withholds mismatched entries (`consistency_withheld`).
  The source-fallback metadata path (`loadPromotedMeta`) now streams entries and records each
  content hash before discarding the body.
- Implicit changed-file focus only boosts; explicit `query`/`paths` gate relevance.
- Recall output adds `bounded`, `limit`, `active_count`, `require_multi_agent`, `verification_threshold`.

Green: the four tests above plus all existing `Recall|Context|Drift|Memory` workspaceops tests pass.
Stdio-MCP coverage of plain recall: **pending** — planned for `scripts/e2e/mcp-evidence.sh`, not yet
written (Stage 5).

Fable partial review (`docs/reviews/2026-09-24-fable-recall-review.md`, CHANGES_REQUIRED) follow-ups:

| Finding | Regression | Red (before follow-up) | Fix |
| --- | --- | --- | --- |
| 1 implicit focus discarded | `TestPlainRecallBoostsMemoryAboutChangedFiles` | exit 1: top-8 `[e0011 … e0004]`, changed-file memory absent | score = relevance + boost; only explicit query/paths gate |
| 2 `GET /context` full history to non-admin | `TestContextListRouteIsAdministrative` (7 filters incl. default × agent/readonly → 403; admin 200; agent POST remember 200) | exit 1: default filter 200 | route requires admin for every filter; POST unchanged |
| 3 excessive limit reset to 8 | `TestRecallRouteClampsExcessiveLimit` | exit 1: `limit=500` → 8 | clamp to 50 |
| 4 race test under-asserted | strengthened: attempts=2, returned=2, no withheld | passed on first run (strengthening only) | — |
| 5 withhold without backfill | documented in `docs/CONTEXT_LAYER.md` recall contract | — | fail closed: `returned` may be < limit, `consistency_withheld` reports it |
| 6 doc accuracy | this report marks MCP script evidence pending; stray `api-go/cmd/xmustard-api/xmustard-api` build binary (created by my own `go build ./`) deleted | — | builds now use `go build ./...` |

Contract note for the optional UI: `frontend/src/lib/api.ts:144` calls `GET /context?filter=…`; it now
needs an admin credential (the UI has no bearer plumbing today). UI work is out of scope.

Root hardening (after Root review): the trust comparison used `hashContent`, a 64-bit filename
prefix. It now compares a full SHA-256 `ContentDigest` recorded with the ranked metadata; the short
hash only locates candidate files. A legacy meta cache without full digests fails closed to the
source scan and is migrated under the store lock.

```
# red: hardening reverted in place (64-bit comparison, legacy check disabled), then restored
go test ./internal/workspaceops -run 'TestRecallLegacyShortHash|TestRecallTrustComparison' -count=1   # exit 1
--- FAIL: TestRecallLegacyShortHashCacheFailsClosedAndMigrates  (returned STALE_BODY_FROM_OLD_VERSION)
--- FAIL: TestRecallTrustComparisonUsesFullDigest  (short-hash-matching body returned)
# green with the hardening: exit 0; `-run 'Recall|Context|Drift|Memory'` exit 0
```

`ground` bounded (closed in Stage 1d): `BuildSessionGrounding` called `GetActiveContext`, decoding and
drift-hashing the full promoted history. `TestGroundingStaleMemoryWorkIsBounded` (300 baselined
memories, fake core) — red: `grounding drift-checked 300 memories; want at most 64`; green: it now
drift-checks at most 64 most-recent baselined memories from content-free metadata and reports
`stale_memory_checked`, `stale_memory_total`, `stale_memory_complete`.

### 1b. Shutdown join and request cancellation into children

Red evidence method for process/route tests: `git archive cd13e2b api-go` into a scratch directory,
apply only the behavior-neutral `registerRoutes` extraction (same script), copy the new test file,
run. This is the "pristine" column below.

| Test | Pristine `cd13e2b` | This worktree |
| --- | --- | --- |
| `TestToolRouteCancellationKillsBlockedChild` (real blocking child as Rust core and `sg`) | exit 1: impact, impact-symbol, impact-trace, why_failed, ground, search, search-pattern, explain children "still running 3s after the request was cancelled"; plain recall started no child (full-history path) | exit 0, 9/9 routes |
| `TestSIGTERMPersistsInterruptedRunBeforeExit` (built binary, live managed run, SIGTERM) | exit 1: `after process exit the run status is "running", want durable "interrupted"` | exit 0: run `interrupted`, worker reaped, `shutdown: services closed` logged before exit |

Fix:
- `main` joins the shutdown goroutine (`shutdownDone`) after `ListenAndServe` returns, and logs
  drain/close/complete.
- `rustcore.RunSymbolgraph/RunSearch/RunWiki/RunOwnership/RunChangetrack` take `ctx`; the unused
  context-free `runCoreContext` was removed. Tool entry points gained `...Ctx` variants
  (`BuildSessionGroundingCtx`, `RecallContextCtx`, `WorkspaceSearchWithFeedbackCtx`,
  `SearchSemanticPatternCtx`, `ExplainPathCtx`, `WorkspaceChangesSinceIndexCtx`, `SymbolImpactCtx`,
  `TraceSymbolsCtx`, `ReadDiagnosticsCtx`, `ExplainRunFailureCtx`); the nine routes pass `r.Context()`.
  Non-tool callers keep `context.Background()`. `remember`/`verify` start no child.
- The Go ast-grep runner used `cmd.Run` with an unbounded `strings.Builder`; it now streams
  `--json=stream` lines (1 MiB line cap, 64 KiB stderr cap, 60 s timeout, kill on result limit or ctx).

### 1c. Authorization startup interlock

`validateStartup` (pure) + `buildHandler` extracted from `main`. Rules: `XMUSTARD_AUTH` must be
`auto|required|off`; `required` without credentials refuses on every bind; any non-loopback bind
requires `required` + credentials + (TLS or `XMUSTARD_ALLOW_INSECURE_BIND=1`); `off` is loopback-only.
Loopback detection uses `net.ParseIP(..).IsLoopback()` (was a three-string compare).

`TestStartupAuthMatrixAgainstRealListener`: 36 cells = host {127.0.0.1, 0.0.0.0} × auth {auto,
required, off} × credentials {none, configured} × transport {plain, insecure-override, TLS
self-signed}, each exec'ing the built binary. Started servers are probed unauthenticated on five
protected routes via loopback and, when present, the host's non-loopback IPv4 address; enforced
modes must answer 401, and a valid bearer must answer 200.

| | Pristine | This worktree |
| --- | --- | --- |
| exit | 1 — 9 cells started that must refuse: `127.0.0.1/required/creds=false/*` (3), `0.0.0.0/auto/creds=true/{insecure-override,tls}`, `0.0.0.0/required/creds=false/{insecure-override,tls}`, **`0.0.0.0/off/creds=true/{insecure-override,tls}`** (the audited finding) | 0 — 36/36 |

### 1d. Enforced transient-byte admission and helper-child limit

Design (`api-go/internal/budget`): `Charge` (unconditional) is removed. Every reservation goes through
`Acquire`/`Scope.Acquire` and is refused with `budget.ErrOverloaded` without reserving. A `Scope`
is a per-request ledger attached to the request context by the HTTP middleware and held until the
handler returns (deferred: success, error, cancellation and panic). `CaptureWriter` reserves in
256 KiB chunks **before** buffering and, on the first refusal or cap overflow, runs `OnStop`, which
kills the producer. `ChildLimit` (`XMUSTARD_MAX_CORE_CHILDREN`, default 4; wait
`XMUSTARD_CORE_CHILD_WAIT_MS`, default 10 s) bounds concurrent Rust core, bounded-capture and
ast-grep children separately from bytes. `Acquire` compares against remaining capacity (no
`used+n` overflow). HTTP maps overload to `503` + `Retry-After: 1` + `{"overloaded":true}`; MCP maps
it to JSON-RPC `-32000`.

Wired paths: request bodies (declared length reserved up front, including sub-1 MiB bodies;
chunked bodies read under admission in the middleware so refusal is still 503/413); Go→Rust
`runCoreCtx` and `runBoundedCmd` captures; ast-grep (child slot + line buffer reservation);
provider responses (`readProviderBody`, reservation held through decode, error past the cap
instead of silent truncation); MCP shim (at most `XMUSTARD_MCP_MAX_INFLIGHT`, default 8,
outstanding id-bearing requests; each worker's response read is reserved against the shim's own
pool and held until the reply is written).

Process-tree ownership (Root follow-up): Rust core and ast-grep children start as process-group
leaders (`IsolateProcessTree`, Unix); cancellation, refusal and normal completion kill the whole
group. On Windows only the immediate child is killed (`kill_windows.go`); `rustcore` builds for
`GOOS=windows`. The pre-existing Windows compile failure in `workspaceops/run_control.go`
(`Setpgid`, `syscall.Kill`) is unchanged from `cd13e2b`.

| Regression | Red evidence | Green |
| --- | --- | --- |
| `TestCoreChildConcurrencyIsBounded` (binary, `MAX_CORE_CHILDREN=2`, 6 concurrent searches) | exit 1: `observed 6 concurrent core children` | exit 0 |
| `TestRustCaptureIsAdmittedAgainstTransientPool` (binary, 1 MiB pool, 4 MiB core output) | exit 1: `status 200, body 4194325 bytes` | 503 + Retry-After |
| `TestSmallRequestBodiesAreAdmitted` (256 KiB pool, 300 KiB body) | exit 1: `status 200` | 503; small body 200 |
| `TestRunCoreCaptureNeverExceedsPool` | exit 1: `succeeded with 4194304 bytes` | `ErrOverloaded`, peak ≤ max, 0 in use |
| `TestRunBoundedCmdNeverExceedsPool` | exit 1: succeeded (pool peak 64 MiB + 64 KiB vs 1 MiB max) | `ErrOverloaded` |
| `TestProviderResponseIsAdmittedAgainstPool` | exit 1: `pool peak 16777216 past max 1048576` | overload error, peak ≤ max |
| `TestShimRefusesCallsBeyondInflightLimit` (built shim, limit 2, 3 hanging calls) | exit 1: `third concurrent call was neither answered nor refused within 3s` | `-32000` for id 3 |
| `TestAcquireRejectsOverflowingReservation` (Root) | exit 1: `MaxInt64 admitted after 1 byte: used=-9223372036854775808` | refused |
| `TestRunCoreRefusalKillsFloodThenSleepChild`, `TestRunBoundedCmdRefusalKillsFloodThenSleepChild` (Root) | exit 1: both `call blocked 5.0s until the child was killed externally` | child killed at refusal; bounded variant asserts the child started and `ErrOverloaded` |
| `TestRunCoreCancelKillsChildAndGrandchild`, `TestRunCoreRefusalKillsChildAndGrandchild` (Root) | exit 1: grandchild `survived` in both | whole group gone |
| `TestGroundingStaleMemoryWorkIsBounded` | exit 1 (see 1a) | ≤ 64 checks |

Replaced tests (behavior deliberately changed, not weakened): `TestChargeAccountsAndPressuresAcquire`
(tested the removed unconditional charge) → `TestScopeEnforcesAndReleasesOnClose`,
`TestScopeReleasedOnPanic`, `TestChildLimitBoundsConcurrency`; `TestCapWriterBoundsAndFlags`
(tested "pretend full write" draining) → `TestCaptureWriterBoundsReservesAndStops` (stop + kill).
Caught during this stage: a first version of the bounded refusal test passed vacuously because
setting `cmd.Cancel` on a plain `exec.Command` makes `Start` fail; the test now asserts the child
started and the error is `ErrOverloaded`.

Honest limits: byte admission bounds what xMustard buffers, not process RSS. JSON response encoding
(`writeJSON`) still allocates the encoded body before writing and is not separately reserved; for
the nine tools this becomes a bounded projection in Stage 2. Full `go test ./...`: exit 0.

### Division of work (Root, mid-program)

Root reassigned all `rust-core/**` edits (plan Stage 1 `indexcache` repair and Stage 3 Rust
indexing) to a separate Opus 5.5 task reporting in `docs/reviews/2026-09-24-rust-implementation-results.md`.
Root later reassigned Stage 4 (Pi adapter, `integrations/pi/**`, `scripts/e2e/pi-adapter.sh`) to a
third Opus 5.5 session. This task owns Go (API, MCP, evidence), `scripts/e2e/mcp-evidence.sh`,
`scripts/bench/rss.sh`, the gold fixture, docs and end-to-end integration/gates.

Rust `repo-key` is integrated with Root's contract: `xmustard-core repo-key <root>` →
`{key, head, parser_version, identity_complete, limitations:[{path?, reason, detail?}], repo_mode,
root, …}` (`workspaceops.RepoIdentity`, typed `IdentityLimitation`). Any limitation forces
`identity_complete=false`. The temporary `changetrack fingerprint` fallback was **removed**: a failed
or undecodable `repo-key` yields an unavailable identity, so freshness is `unknown`, never current.

### Stage 1 runtime review follow-ups (`docs/reviews/2026-09-24-fable-runtime-review.md`)

| Finding | Regression | Red | Green / fix |
| --- | --- | --- | --- |
| #1 MCP ingress not admitted | `TestShimIngressFramesAreAdmitted` (built shim, 1 MiB pool, 6×7 MiB frames + 1 small) | exit 1: frame 1 (7 MiB) forwarded and answered | frames refused `-32000` before buffering, API saw only the small call, shim RSS < 64 MiB. Each frame reserves its chunks before append, its length again before decode, params before argument decode, 2× argument bytes before request build; the ledger is held by the worker until the reply is written. `strings.TrimSpace(string(raw))` copy removed; request body is a `strings.Reader` |
| #1 follow-up: id of a refused frame | `TestProbeIDUsesOnlyTopLevelID` (nested, escaped, id-after-params, id beyond prefix) | exit 1: regex returned nested `99` instead of top-level `4` | top-level member parse of a 1 KiB prefix; unknown → `null` id |
| #1 follow-up: cancel under saturation | `TestControlFramesReadUnderExhaustedPool` (in-process, pool provably full) | exit 1 with headroom temporarily set to 0: `cancellation frame must be read from headroom: false` | fixed 4 KiB control headroom (frames are read one at a time); only notifications and `ping` are serviced from it, other methods get `-32000` with their parsed id. `TestCancellationServiceableUnderPoolSaturation` is an end-to-end smoke only (pool not provably full; it passed before and after) |
| #3 permanent oversize answered 503 | `TestOversizedDeclaredBodyIs413NotRetryable` | exit 1: `got 503 Retry-After="1"` | `Content-Length` over the cap → 413 before pool admission. `TestBodyLimitMiddlewareCapsOversizedBody` updated: it previously required the handler's read to fail (400); oversize is now refused 413 before any handler runs, for declared and chunked lengths (stricter) |
| #5 helper children outlive shutdown | `TestShutdownEndsInFlightHelperChildren` (built binary, blocking child, SIGTERM, drain 1 s) | exit 1: `helper child … outlived the API` (a first version passed vacuously because the test client's 10 s timeout killed the child; fixed to use a client without timeout) | server `BaseContext` cancelled after the bounded drain (`XMUSTARD_SHUTDOWN_DRAIN_SECONDS`, default 15), then `rustcore.KillActiveChildren()` kills every tracked helper tree (Rust core, bounded captures, ast-grep) |
| #2 long-lived captures share child slots | not changed (Root: documented resource-policy tradeoff unless the combined gate exposes a need) | — | — |
| #4 post-Wait group kill | not changed (Root: unreproduced low-risk note) | — | — |
| #6 minor | `openai_providers.go` gofmt'd; `budget.AdmittedReader` still unused (kept only if Stage 5 needs it; to remove before final gates otherwise) | — | — |

## Stage 2 — bounded, recoverable evidence delivery

Module: `api-go/internal/evidence` (`store.go`, `reduce.go`). One narrow interface: `NewSpool` →
`Capture` (store raw, reduce, issue handle only when something was omitted) → `Read` (authorized
pages) / `Revoke` / `RevokeWorkspace`. Selection, admission, retention and expiry live there.

Limits (lower-only via `XMUSTARD_EVIDENCE_*`; larger values are ignored): 16 MiB per original,
projection target 64 KiB (results at or below pass unchanged), hard projection cap 1 MiB, 64 KiB
pages, 256 MiB retained per workspace, 24 h retention. Handle `xm1.` + base64url(32 random bytes);
stored under SHA-256(handle), so listings never reveal usable handles. Each artifact persists the
full observation (workspace, canonical repo scope, actor = stable `Principal.ID`, auth-enforced
flag, issuer, session/call IDs, tool + version, argument digest, capture time, captured key and
whether identity was bound, status, error flag, content type, raw SHA-256/size, expiry, quota) and
the projection record (reducer `xm-reduce/1`, mode, omissions with byte ranges).

Behavior contract:
- Reduction is deterministic. JSON stays valid JSON of the same shape: failure-bearing array
  elements are always represented (whole, or reduced into a share when too large), then leading
  elements; long strings keep head, windows around failure evidence and tail; omissions name
  pointer and byte range. Text keeps every failure/status line with context and stack
  continuation first, then tail and head. Required failure evidence over the 1 MiB cap is an
  explicit `ErrUnsupported`, never a success-looking projection.
- Declared-JSON that does not parse, binary and multimodal content are never reinterpreted as
  text: valid-UTF-8 passes unchanged within the cap (`projection_mode=unsupported_passthrough`);
  non-UTF-8 is refused at any size (a JSON transport would rewrite it). Reduced text replaces an
  invalid line with U+FFFD **and** lists it as an `invalid_utf8` omission; exact bytes page back.
- Pages are standard base64 (`encoding:"base64"`). Reads fail closed (`ErrCorrupt`) if the stored
  original's size changed or a page cannot be read in full.
- Freshness: the delivery middleware samples identity **before** the handler runs and again at
  capture; identity is `bound` only when both are complete and equal, else `unknown`. Pages report
  `captured_key`, `current_key`, `freshness` (`current|stale|unknown`) and `stale` (true unless
  current). Results POSTed after the fact are never given a sampled key (`unknown`).
- Quota counts retained originals and open spools; expired originals are reclaimed at admission;
  unexpired originals are never evicted; a full quota is an explicit error (HTTP 507).
- Authorization: an artifact issued to an authenticated principal is readable/revocable only by
  that `Principal.ID` (token rotation for the same ID keeps working); one issued without
  authentication is workspace-scoped and becomes unreadable once auth is enforced. Handles never
  resolve across workspaces. Loopback `auto` without credentials gives workspace isolation only.

HTTP contract (for the Pi owner and any direct client):

| Route | Purpose |
| --- | --- |
| any of the nine tool routes + header `X-Xmustard-Delivery: xmustard.evidence/v1` (optional `X-Xmustard-Call-Id`, `X-Xmustard-Session-Id`, `X-Xmustard-Issuer`) | response header echoes the delivery version; body is the envelope `{delivery, tool, call_id, status, is_error, content_type, reduced, projection, handle?, resource_uri?, expires_at?, captured_key?, captured_identity, raw_bytes, raw_sha256, projected_bytes, projection_mode, reducer, omissions[], page_size}`. The original HTTP status is `status`; `is_error` = status ≥ 400. Identity is bound around execution. |
| `POST /api/workspaces/{id}/evidence?tool=<one of nine>&call_id&session_id&issuer&status&is_error&content_type&tool_version&args_digest` body = raw result | same envelope; agent role; freshness always `unknown` (produced at an unknown state) |
| `GET /api/workspaces/{id}/evidence/{handle}?offset=N&length≤65536` | page `{handle, tool, call_id, content_type, offset, length, total_bytes, next_offset, eof, encoding:"base64", data, raw_sha256, captured_key, current_key, freshness, stale, expires_at}` |
| `DELETE /api/workspaces/{id}/evidence/{handle}` / `DELETE /api/workspaces/{id}/evidence` (admin) | revoke one handle / all of a workspace (there is no workspace-deletion API) |
| errors | 404 `missing`/`invalid_handle`, 410 `expired`/`revoked`, 403 `denied`, 413 `too_large`, 415 `unsupported`, 416 `invalid_range`, 507 `quota_full`, 500 `corrupt`, 503 overload |

MCP (`api-go/cmd/xmustard-mcp/evidence.go`): every `tools/call` requests the envelope; the tool
result text is the projection (errors stay `isError`), plus, when reduced, a note naming the
resource and `_meta["xmustard/evidence"]`. `initialize` advertises `resources`;
`resources/list` lists this session's handles, `resources/templates/list` gives
`xmustard://evidence/{handle}{?offset,length,workspace_id}`, `resources/read` returns a base64
`blob` page with `_meta["xmustard/page"]` (freshness labels). Unknown, expired, revoked or denied
→ `-32002`; bad offset/length → `-32602`; overload → `-32000`. The handle→workspace map is
per shim process; after a shim restart pass `?workspace_id=` or set `XMUSTARD_WORKSPACE_ID`. No
tenth tool; the nine tool names and schemas are unchanged.

Tests (all new; this module had no prior behavior to be red against except where noted):
- `internal/evidence`: passthrough, one failure among 2,000 passes kept + exact paged original,
  determinism, contradiction + stack trace (text), large string / capture-cap size error, byte-safe
  pages (4-byte UTF-8 split across a page, invalid UTF-8 beyond the sniff window, JSON wire
  round-trip), cross-workspace / other principal / anonymous / tampered / malformed handles,
  quota-full without eviction, expiry + restart + quota rebuild, stale and unknown labels, revoke +
  workspace revocation, concurrent similar calls, lower-only env limits, abandoned spool sweep.
- Root review invariants (red first, all exit 1 before the fix): 10 data-race reports across
  workspaces (`-race`); truncated `raw.bin` served zero padding; expired originals blocked
  admission; pending spools uncounted; failure element larger than the target dropped; failure near a
  long string's tail dropped; invalid declared JSON silently became text; identity sampled only after
  execution; spool/workspace mismatch and enforced-auth-without-actor accepted. Later additions, also
  red first: small non-UTF-8 passthrough rewritten on the wire; a FATAL line after 40 × 4 KiB
  passing lines lost; required evidence over the cap not refused; pre-cancelled capture.
- `cmd/xmustard-api` (real handler stack, fake Rust core): MCP-style delivery + exact paged
  original, errors stay errors, stale/unknown freshness, principal binding with token rotation,
  expiry + restart + quota (507), **five concurrent 16 MiB captures against the 64 MiB pool: codes
  `[200 200 200 200 503]`, pool peak 67,104,708 ≤ 67,108,864, 0 in use after**, cancelled
  capture retains nothing, and the delivery seam releases its spool on handler panic/cancel.
- `cmd/xmustard-mcp`: resources capability, delivery headers, projection + note + `_meta`,
  resource paging, protocol error codes.

All `go test -race ./internal/evidence ./cmd/xmustard-mcp` and the evidence HTTP tests: exit 0.
Stdio MCP against the real API: `scripts/e2e/mcp-evidence.sh` (below).

Contract changes since the first Stage 2 write-up (for the Pi owner and clients):
- Issued resource URIs name their workspace: `xmustard://evidence/{handle}?workspace_id={ws}`.
  The exact issued URI works unchanged after an API and shim restart (the server still enforces
  workspace and principal scope). Append `&offset=N&length=N` to page.
- An admission refusal inside a delivered tool call passes through as `503` + `Retry-After`
  (never a 200 envelope); the shim answers `-32000`, and also maps an envelope with
  `status:503` + `"overloaded":true` to `-32000`.
- Unreduced (passthrough) deliveries report `captured_identity:"unknown"` (identity not sampled;
  nothing retained, no handle).
- JSON nesting deeper than 512 is unsupported structured output: passed unchanged within the
  1 MiB cap (valid UTF-8 only), otherwise `415 unsupported`. Failure evidence that cannot be
  represented within the 1 MiB hard cap (every failure element/member kept whole or reduced to
  at least 256 bytes with its failure windows) is `415 unsupported`: no projection, no handle, no
  retention, so no recovery is promised. Keys are never replaced by placeholders.
- Detection budget: more than 2^20 failure-evidence positions in one document → explicit
  `415 unsupported` (never a silent miss of later failures).

### Fable evidence review (`docs/reviews/2026-09-24-fable-evidence-review.md`, interim) closure

All regressions below were run red on the unchanged code first (exit 1), then green.

| Finding | Regression(s) | Red | Fix |
| --- | --- | --- | --- |
| F1 reduction O(depth × size), uncancellable | `TestDeepNestingIsLinearBoundedAndCancellable`: depth 400 × 1 MiB ≤ 1.5 s; depth 1000 → explicit unsupported ≤ 1.5 s; 13.8 MB array and a single 16 MiB string each cancelled at 30 ms must return `context.DeadlineExceeded` within 500 ms (payload spooled before the timer; both conditions required, per Root) | depth 400 took 3.4 s (Root: 9.2 s; 3 m under `-race`) | one cancellable structural pass (`src.index`) records the end of every container ≥ 4 KiB and enforces nesting ≤ 512; `skipValue` uses those ends; `skipString`/`skipValue`/index/hash/salience/text scans check ctx at least every 256 KiB of input (threshold crossing, not offset equality); `classify` only sniffs, validation happens in the cancellable pass; spool `fsync` moved to just before the retention commit. Profile before the fix: 84 % of CPU in the case-folding regexp re-run at every level → failure-evidence detection now uses a keyword prefilter and confirms ±64-byte windows once per document (`salience.go`, binary-searched per value). Now 0.12 s (3 consecutive `-race` runs pass) |
| F2 inner 503 wrapped in a 200 envelope | `TestDeliveredOverloadPassesThrough503` (built binary), `TestShimMapsEnvelopeOverloadToProtocolError` | 200 without Retry-After; shim returned a tool error | middleware passes 503 + Retry-After through; shim maps envelope overload to `-32000` |
| F3 JSON kept failures only up to the target | `TestAllFailureElementsKeptUpToHardCap` (3000 error diagnostics, 231 KB → all 3000 kept; with a 64 KiB cap → explicit unsupported) | 755 of 3000 kept (Root: 850) | salient elements/members are mandatory: `max(256, remaining/count)` each, whole when it fits, else reduced keeping failure windows; the shared slack up to the hard cap covers the minimums; otherwise `ErrUnsupported`. `TestLargeFailureElementAndTailFailureSurvive` still bounds the single-large-failure case to about the target |
| F4 unbudgeted keys / oversized result | `TestHugeKeysFailFastWithoutUnboundedAllocation` (500 × 4000-byte keys) | 18.0 MB allocated to refuse a 2 MB original (Root: 10.4 MB) | every write checked against the hard cap; if keys alone exceed it, fail before writing; shared JSON-pointer escaper, lazy pointers, reused salience chunk buffer. Disposition (Root): explicit size error, no key placeholders, no handle promised |
| F5 passthrough identity `""` | `TestPassthroughReportsIdentityUnknown` | `""` | `"unknown"` |
| F6 read racing revoke → corrupt | `TestReadRacingRevokeIsNeverCorrupt` (seam forces the interleaving; allowed outcomes data or revoked, then revoked afterwards) | `corrupt` | metadata check + open of `raw.bin` under the workspace lock; expiry removal inline under the same lock |
| F7 response construction unreserved | shim `TestShimResponseConstructionIsAdmitted` (pool 300 KiB, 200 KiB projection); API `TestResponseConstructionIsAdmitted` (built binary, 3 MiB pool, 1.2 MiB core output) | both answered from unreserved memory | shim reserves 2 × body for decode + reply encoding (1 × for plain text replies); API bridge reserves 2 × captured Rust output for decode + response construction under the request ledger; the delivery envelope reserves 2 × projection + 4 KiB before encoding |
| F8 a Rust child per page | not changed (Root: keep per-page freshness; no TTL cache) | — | measured: ~50 ms per 64 KiB page at the MCP client including the `repo-key` child (`[52.0, 51.5, 50.3]` ms in `mcp-evidence.sh`); Rust owner measured `repo-key` at 0.05 s / 6.5 MiB on the 501-file fixture |

Fable's minor notes: `budget.AdmittedReader` (unused) and `Store.expire` (unused after F6)
removed.

Salience follow-ups (Root review of `salience.go`):
- An intermediate change bounded the status patterns to `\s{0,16}`; Root rejected it as a
  narrowing of recognized failure statuses. Reverted to unbounded `\s*`. Instead the
  confirmation window extends over the separator runs (whitespace, `:`, `=`, `"`, plus the rest
  of an identifier such as `exit_code`) adjacent to the keyword, read from the source, then
  64 bytes beyond. A separator run over 64 KiB next to a status keyword is refused explicitly
  (`415 unsupported`, input shape). Text lines are examined whole for failure evidence up to
  1 MiB (previously only the first 4 KiB of a long line was checked); a longer line is refused
  explicitly. `TestFailureStatusWithLongLegalWhitespaceIsKept`: late array elements with 300
  spaces / 400 newlines between `"ok"` and `false`, and 200 tabs around `exit_code:3`, plus a
  text line with 500 tabs/spaces, all kept; 70 KiB separator run and 1 MiB+ line refused. Red
  with window extension disabled (fixed ±64 windows): `JSON failure statuses with long
  whitespace runs must be kept`.
- Detection-budget exhaustion (> 2^20 positions) is an explicit `ErrUnsupported`
  (`TestSalienceBudgetExhaustionIsExplicit`; red with the old silent saturation fallback
  restored: `want ErrUnsupported, got <nil>`).
- Whole failure lines (Root review of `reduceText`): pass 1 examined up to 1 MiB of a line but
  pass 2 emitted only its first 4 KiB, so a failure after 8 KiB of filler was dropped.
  `TestLateFailureOnLongLineIsEmitted` (260 KB log, middle line = 8 KiB filler + `FATAL boom`):
  red `failure bytes after 4 KiB on a line were dropped`; fix: a line that itself carries failure
  evidence (or is a stack continuation) is mandatory in whole — costed and emitted at full length
  within the 1 MiB hard cap, else explicit unsupported; context lines stay truncated at 4 KiB; a
  whole line's cost is never shrunk to context cost by a later neighbor. The long-whitespace test
  now asserts the failing values (`ok:false`, `exit_code:3`, `"success": false`) survive, not
  only the keys.

## Stage 3 — Go integration of the Rust indexing results

- `repo-key` wired as described above (typed limitations, no fallback).
- Search, impact, blast-radius and graph results pass Rust's `coverage` (losses, loss counts,
  `source_identity`, `work` counters) through unchanged as raw JSON; verified end to end by the
  retrieval gate (cache miss/hit, files parsed/reused, explicit losses, served key).
- `path-symbols`: Go dropped the new `total_symbols`/`symbols_truncated`
  (`TestPathSymbolsCarriesRustTruncationFields`, red: fields dropped). Both Go structs now carry
  them; the workspaceops result uses optional fields so the live-LSP document-symbols path omits
  them instead of reporting a false `total_symbols: 0` (`TestDocumentSymbolsOmitExtractorCompletenessFields`,
  red: `"total_symbols":0` beside two symbols).

## API retained-heap profile and the RawMessage fast path

Scratch step profiler (job tmp, generated 501-file fixture, `GODEBUG=gctrace=1`, API RSS by
`ps`): before, the index step raised API RSS 19.0 → 38.4 MiB and kept it, while live heap after
GC was 1–5 MB; `self_maxrss` 46.9 MB. Cause: `writeJSON` re-encoded the 2,464,007-byte
`changetrack index` JSON through `json.Encoder` (compaction into a second growing buffer). Fix:
`writeJSON` writes a valid `json.RawMessage` as-is (invalid raw input keeps the encoder path).
After: index step peak 28.6 MiB then 15.8 MiB after the next request; four concurrent 16 MiB
captures peak 24.6 MiB; `self_maxrss` 30.0 MB. `TestWriteJSONRawMessageMatchesEncoder`
checks semantic equality with the encoder (status, content type, decoded value incl.
HTML-sensitive characters and whitespace, trailing newline) and the invalid-input fallback. Byte
difference: the fast path does not HTML-escape `<`, `>`, `&` (the decoded value is identical).
No GC/memory-limit tuning was applied.

## Changed files (this task)

Modified: `api-go/cmd/xmustard-api/{main.go, body_limit_test.go}`,
`api-go/cmd/xmustard-mcp/main.go`, `api-go/internal/budget/{budget.go, budget_test.go}`,
`api-go/internal/rustcore/{root.go, changetrack.go, knowledge.go, lsp.go, repomap.go,
symbolgraph.go, bridge_test.go}`, `api-go/internal/workspaceops/{changes.go,
context_governance.go, diagnostics.go, failure_explainer.go, grounding.go,
issue_symbol_edges.go, knowledge.go, openai_providers.go, pgindex.go, run_control.go,
runtime_settings.go, semantic_materialization.go, symbol_reads.go, symbolgraph.go,
workspace_reads.go, workspace_scan.go}`; appended section in `docs/CONTEXT_LAYER.md` (recall
contract; that file is now owned by the docs owner).

Added: `api-go/internal/evidence/{store.go, reduce.go, salience.go}` + tests
(`store_test.go, invariants_test.go, review_test.go`);
`api-go/cmd/xmustard-api/{evidence_routes.go, rusage_unix.go, rusage_windows.go}` + tests
(`admission_process_test.go, auth_startup_test.go, cancel_route_test.go,
evidence_route_test.go, evidence_seam_test.go, proc_helpers_test.go, recall_route_test.go,
shutdown_process_test.go, write_json_test.go`); `api-go/cmd/xmustard-mcp/evidence.go` + tests
(`admission_process_test.go, evidence_test.go, ingress_test.go`);
`api-go/internal/rustcore/{children.go, kill_unix.go, kill_windows.go}` + `admission_test.go`;
`api-go/internal/workspaceops/repo_identity.go` + tests (`document_symbols_test.go,
grounding_bounded_test.go, path_symbols_coverage_test.go, provider_admission_test.go,
recall_contract_test.go, runtime_detect_test.go`); `scripts/e2e/{harness.py, mcp_evidence.py,
mcp-evidence.sh}`; this report; `docs/reviews/evidence/2026-09-24/*.json`.

Authored before the bench handoff (now owned by the benchmark owner): `scripts/bench/{rss.sh,
rss_bench.py, retrieval-gate.sh, retrieval_gate.py, gold/queries.json, gold/repo/**}`.
No `rust-core/**`, `integrations/pi/**`, `scripts/e2e/pi-adapter.sh`, frontend, Docker or
provider configuration was edited. No build binaries are left in the worktree.

## Source snapshot for final review (sha256 prefixes, `api-go/`)

`internal/evidence/{reduce.go 2a0bf1d3ba1d, salience.go b8573d5c83ba, store.go 11477dccb793}`,
`internal/budget/budget.go 63c43aab67ef`, `internal/rustcore/{root.go 5d065a24ab9b,
children.go 285fbd72d4c6}`, `cmd/xmustard-mcp/{main.go a4d0ee4601e9, evidence.go ae531d19e24c}`,
`cmd/xmustard-api/{main.go b15bb345ce16, evidence_routes.go 5d2a80532964}`,
`internal/workspaceops/{repo_identity.go 4617c764d952, runtime_settings.go 150cea8bfb97,
symbol_reads.go 28950dce962c, workspace_reads.go 7b9de59be969, context_governance.go
fe8864e21541}`. Gates at this snapshot: build/vet 0; `go test ./...` 0; `-race` evidence,
shim, budget, rustcore 0; five consecutive `-race` runs of the cancellation proof pass
(0.35–0.40 s); `scripts/e2e/mcp-evidence.sh` 23/23; retrieval gate 12/12.

## Remaining (honest)

## Read-only residency analysis after Root's final RSS run (Go source frozen)

## Final code review follow-ups (`docs/reviews/2026-09-24-fable-code-review.md` F3–F5)

Narrow freeze exception from Root: tests, harness and report only; production Go unchanged
(API binary still `88e88bac…`).
- F3 provenance: `scripts/e2e/harness.py` gained `provenance(api_bin, mcp_bin, core, scripts)`
  (same keys and recipe as `rss_bench.py`: `head`, `source_diff_sha256` over `git diff HEAD --
  api-go rust-core/src rust-core/Cargo.toml rust-core/Cargo.lock`, `untracked_source_files` /
  `untracked_source_sha256` over sorted `path\0sha256\n`, `binaries_sha256` for api/mcp/core,
  `core_bin`, `core_bin_prebuilt`, `script_sha256` for the listed scripts, `env`) and
  `finish_with(checks, report, extra)`. `mcp_evidence.py` writes `provenance` into its report.
  Contract offered to the benchmark owner via Root for `retrieval_gate.py`; `scripts/bench` not
  edited here.
- F4: `TestFiveConcurrent16MiBCapturesAgainst64MiBPool` now fails on any code other than 200/503
  (3 runs pass).
- F5: the MCP cancellation check requires an error reply (JSON-RPC error or `isError: true`).
- Smoke run: `scripts/e2e/mcp-evidence.sh` 23/23 with provenance, cancellation `error_reply=True`.
  Its core binary (`d644743b…`) is the Rust owner's in-progress rebuild, not Root's `a23473a6…`,
  so the final bound rerun waits for Root's stable-Rust signal.

Root's corrected run failed at 106.4 MB ("two concurrent index clients": Rust 40,752 + 20,512,
API 22,672, shims 10,480 + 9,520 KiB). Scratch profiler (job tmp `prof/residency.py`: frozen
source, same generated fixture, 50 ms sampling, `vmmap`, `gctrace`, env-only variants; no
source edits):

| Variant | idle API / shim RSS KiB | API peak KiB: index / captures / expansion | API `self_maxrss` | GC cycles | API CPU |
| --- | --- | --- | --- | --- | --- |
| default | 14,160 / 9,696 | 28,048 / 31,360 / 31,600 | 32.4 MB | 295 | 2.67 s |
| `GOGC=50` | 14,080 / 9,632 | 21,184 / 22,064 / 22,112 | 23.8 MB | 678 | 2.73 s |
| `GOMEMLIMIT=12MiB` | 13,968 / 9,728 | 28,304 / 22,688 / 22,752 | 29.6 MB | 563 | 2.92 s |

- Idle Go processes are mostly clean, file-backed pages: `vmmap` physical footprint is 4.8 MB
  for the idle API (14.2 MB `ps` RSS) and 4.1 MB for an idle shim (9.7 MB RSS); resident
  `__TEXT`/`__DATA_CONST`/`__LINKEDIT` dominate. The gate's `ps` RSS sum counts these per
  process, including pages the two identical shims share. Reported as metric semantics, not a
  reason to change the gate.
- API growth above idle is transient garbage retained between GCs (live heap after GC is 1–5
  MB): the index step (2.46 MB result), four 16 MiB captures (text reducer allocates a fresh
  slice per line in both passes, ~64 MiB churn per 4 captures) and page expansion (~300 KB
  allocated per 64 KiB page). Total allocation for the run: 641 MB.

Options if the Rust residency work is not enough (none applied):

| Option | Measured / estimated effect | Cost |
| --- | --- | --- |
| A. `GOGC=50` default for the API (env or `debug.SetGCPercent` when unset) | measured: API peaks −7 to −9 MB at index/captures/expansion | measured: GC cycles ×2.3, API CPU +2 % on this workload |
| B. `GOMEMLIMIT=12MiB` | measured: no gain at index, −9 MB at captures | measured: API CPU +9 %; soft limit can thrash if live heap grows; not recommended |
| C. Reuse one line buffer in the text reducer (both passes) | estimate: removes ~16 MiB allocation churn per 16 MiB capture; lowers the capture-step peak | small code change; no CPU cost expected (fewer allocations) |
| D. Block-list `CaptureWriter` (no append doubling) | estimate: saves up to ~1× the captured size transiently on large Rust outputs (index 2.46 MB) | small; more complex buffer assembly |
| E. Stream pure-passthrough Rust stdout (non-tool routes such as index) to the response | estimate: removes the ~2.5 MB capture + write for index | larger change; a failure after partial output can no longer become a clean error |
| F. Leaner shim binary (smaller dependency set) | estimate: 1–3 MB RSS per shim, mostly clean text | moderate change; two shims are part of the workload |

At the failing step the API holds retained garbage from earlier steps rather than work for that
step (the two searches return a few KB). A and C together target that residue with measured or
low CPU cost; neither changes the Rust share, which is the largest contributor.

- Combined sampled-tree RSS gate: not passed by this task's runs (115.8–134.1 MB before the
  scan and RawMessage fixes and before the Rust optimizations). The benchmark owner's
  preliminary current-source run reports 99.3 MB without full expansions; the final full run
  (all captures expanded, hashes, per-page costs) belongs to the benchmark owner.
- `docs/benchmarks/2026-09-24-lean-context.md` and the public status update: benchmark owner /
  docs owners.
- Fable #2 (long-lived captures share helper-child slots) and #4 (post-Wait group kill): Root
  dispositions — policy note and low-risk note, not changed.
- Windows: `workspaceops/run_control.go` did not compile for `GOOS=windows` at baseline and
  still does not; process-tree cleanup on Windows kills only the immediate child.
- Hardware memory bandwidth: not measured.

## Resource hotspot found by the RSS bench: agent CLI launched by every workspace scan

`rss2` step attribution (sampled tree 129.3 MB during "load workspace (initial scan)") showed an
`opencode` process at 89,712 KiB as an API descendant. Source: `ScanWorkspace` →
`DetectRuntimes` → `detectOpencodeModels` ran `opencode models` (external agent CLI, no timeout,
unbounded output) just to list model names in the snapshot.

- Regression `TestWorkspaceScanDoesNotLaunchAgentCLIs`: red (`workspace scan launched the opencode CLI`), green after the fix.
- Fix (narrow): scans use `DetectRuntimesCached` (binary availability from settings/PATH, model
  list from the last live detection in `runtime_models_cache.json`); `GET /api/runtimes` and run
  validation still detect live. Live detection is bounded: 15 s timeout, 1 MiB output cap, own
  process group (killed with it), tracked for shutdown.
- Effect: load-step sampled peak 129.3 MB (rss2) → 45.8 MB (rss3).

## Stage 5 — end-to-end, retrieval gate and resource runs (this task's scope)

Root reassigned `scripts/bench/**` (RSS bench and gold gate scripts, and closing their known
gaps) to a dedicated benchmark owner after rss3; this task made no further edits there. This
task keeps `scripts/e2e/harness.py`, `scripts/e2e/mcp_evidence.py`, `scripts/e2e/mcp-evidence.sh`.

| Script | Result (latest) | Evidence copy |
| --- | --- | --- |
| `scripts/e2e/mcp-evidence.sh` (real API + shim + Rust core, required auth, CORE_ONLY) | **23/23 pass**, exit 0 — tools+resources capability, 9 tools, bounded plain recall (8 of 10) delivered reduced 138,514 → 52,143 bytes with a handle, exact 3-page paging by SHA-256, current → stale after mutation, issuer 200 / other principal 403 / anonymous 401 / cross-workspace 404, 6 concurrent calls correlated, cancellation kills the child, API + shim restart with the exact issued URI, expiry denied with `-32002 expired`, errors stay errors | `docs/reviews/evidence/2026-09-24/mcp-evidence.json` |
| `scripts/bench/retrieval-gate.sh` (12-query gold fixture, Go/Rust/TS) | **12/12 checks**, exit 0: cold 12/12 gold paths in top 5 (spans 10/10) with a graph cache miss; warm 12/12, cache hit, 0 files parsed; after one-file edit 12/12, 1 file parsed / 14 reused; oversized and invalid-UTF-8 fixture files reported as explicit coverage losses; no stale hit (served key equals current `repo-key`); failure-only evidence and contradiction cases pass | `docs/reviews/evidence/2026-09-24/retrieval-gate.json` |
| `scripts/bench/rss.sh` | **FAILS the 100 MB gate** (runs before handoff; retained as the before record): run1 115.8 MB (110.4 MiB), run2 129.3 MB (opencode hotspot), run3 134.1 MB at "two concurrent index clients" = Rust 74,928 KiB + API 36,208 KiB + 2 shims 19,776 KiB. Admission held in every run (pool peak ≤ 64 MiB, child peak ≤ cap); run3 recorded a client-side reset (`-1`) instead of a wire 503 for the fifth capture — a harness read issue to prove, not reclassify (now bench owner's) | `docs/reviews/evidence/2026-09-24/rss-run{1,2,3}-*.json` |

Honesty notes on the gold gate: 10 of 12 queries contain the target identifier, so it is a
lexical-leaning internal regression gate, not a paraphrase/semantic benchmark and not
competitor parity. The RSS numbers above predate the Rust optimizations Root reported
(single-process cold 58,048 → 48,032 KiB) and the F-fixes; the combined sampled-tree gate has
not been rerun here since and is **not passed**. API retained heap (~36 MB in run3) is a
measured contributor; no GC tuning was applied without a profile.

Evidence JSONs: Root allowlisted the dated files under `docs/reviews/evidence/2026-09-24/` by
exact name in `.gitignore` (including this task's `mcp-evidence.json`, `retrieval-gate.json`
and `rss-run{1,2,3}-*.json`); other files in that directory stay ignored. This task did not
edit `.gitignore`. The copied `mcp-evidence.json` and `retrieval-gate.json` predate the F3
provenance block; Root's final runs are the provenance-bound results.

## Current gates (this snapshot)

| Command | Exit |
| --- | --- |
| `cd api-go && go build ./... && go vet ./...` | 0 |
| `cd api-go && go test ./... -count=1` | 0 (all 7 packages) |
| `go test -race -count=1 ./internal/evidence` (3 consecutive runs) | 0 |
| `go test -race -count=1 ./cmd/xmustard-mcp` | 0 |
| `scripts/e2e/mcp-evidence.sh` | 0 (23/23) |
| `scripts/bench/retrieval-gate.sh` | 0 (12/12) |
| `scripts/bench/rss.sh` | 1 (gate not met; see above; now owned by the bench owner) |
| Rust (`cargo test`, `cargo clippy`, release build) | owned by the Rust task; Root reports pass |

`make check-backend` as a single command: pending the final joint gate (Root's earlier run
caught a mid-edit snapshot).
