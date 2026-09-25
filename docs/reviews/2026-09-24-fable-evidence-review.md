# Stage 2 Go evidence review — FINAL for the completed Go scope

Reviewer: Claude Fable 5.1 (`claude-fable-5-1`), independent of the implementer. Scope: the new
`api-go/internal/evidence` module (`store.go`, `reduce.go`, `salience.go`), the delivery
middleware and routes (`cmd/xmustard-api/evidence_routes.go`), the shim's evidence, tools and
resources wiring (`cmd/xmustard-mcp/evidence.go`, `main.go`), and a recheck of the Stage 1
runtime findings (MCP ingress admission, 413 for permanent oversize, shutdown helper cleanup).
Excluded, owned elsewhere: `rust-core/**`, Pi, `scripts/**`, the RSS/resource gate, the gold
retrieval gate, and the workspaceops runtime-detection change. Source is truth; the results doc
only located claims. Probes ran as `go test -overlay` files from the job tmp directory, so no
probe file ever entered the worktree; this report is the only repository write.

Final snapshot reviewed (sha256 prefix; the Go owner declared this source final, the newest
file was last written 16:46:10, and every gate below started at 16:48:03 or later after a
45-second quiet window; the hashes were identical before and after each run):
`internal/evidence/reduce.go` 2a0bf1d3ba1d, `salience.go` b8573d5c83ba, `store.go` 11477dccb793,
`review_test.go` 4dc925ff9a0c, `cmd/xmustard-api/evidence_routes.go` 5d2a80532964,
`cmd/xmustard-api/main.go` b15bb345ce16, `cmd/xmustard-mcp/evidence.go` ae531d19e24c,
`cmd/xmustard-mcp/main.go` a4d0ee4601e9, `internal/budget/budget.go` 63c43aab67ef,
`internal/rustcore/root.go` 5d065a24ab9b, `children.go` 285fbd72d4c6.

## Verdict: APPROVED (completed Go Stage 2 scope, plus Stage 1 recheck)

Every HIGH and MEDIUM finding raised during this review (F1–F7, F9, F10) is fixed in the snapshot
above, each with an owner regression test that I read and ran under `-race`, and each
re-confirmed by my own independent probe against the final source. The remaining items are
LOW and non-blocking (listed under "Open, non-blocking"). This verdict covers correctness of
the Go module and wiring only: the stdio-MCP end-to-end script, the RSS gate, the retrieval
gate, the combined program review and human review of the final diff remain required and are
not claimed here.

Three kinds of closure, kept distinct as Root asked:

| Kind | What it means | Findings |
| --- | --- | --- |
| Mandatory-preservation fix | failure evidence that the reducer used to drop is now kept in the projection | F3 (every salient array element/member kept up to the hard cap), F9 (status fields separated by any legal whitespace run ≤ 64 KiB), F10 (a text failure line is emitted whole, not cut at 4 KiB) |
| Cancellation / cost fix | reduction is linear, cancellable and bounded | F1 |
| Explicit-size-error contract disposition | the input is refused with `ErrUnsupported` (HTTP 415, MCP tool error) and nothing is retained; plan-permitted, no handle promised | F4 (keys alone exceed the hard cap), F3 overflow (mandatory failures exceed 1 MiB), nesting > 512, > 1 Mi failure positions, separator run > 64 KiB, a text line over 1 MiB |

The explicit-error dispositions are consistent with the plan ("otherwise return a
protocol-compatible size error") and were accepted by Root; they are not preservation, and the
agent receives no recovery handle in those cases. That trade-off is now visible in code and tests
rather than silent.

## Findings and their closure

All "before" numbers are my probes against the pre-fix snapshot; "after" numbers are the same
probes against the final snapshot.

### F1. HIGH → fixed — reducer was O(depth × size) and uncancellable

Before: `reduce.go` rescanned the whole remaining value at every nesting level and `Capture`
ran `Reduce` without a context. Object depth 250/500/1000/2000 × 1 MiB took 2.39 / 3.77 /
7.49 / 18.04 s; a 110 KB array nested 20 000 deep took 5 m 41 s; a capture cancelled at 100 ms
returned after 7.1 s.

After: one cancellable structural pass (`src.index`) records container ends and enforces
`maxNesting = 512`; `skipValue` uses those ends; salience is detected once per document
(`salience.go`) and answered by binary search; ctx is checked in the index, hash, salience,
text and reduce paths; every write is checked against the hard cap. Probe: depth 250 → 12.4 ms,
depth 500 → 14.7 ms, depth 1000 → explicit unsupported in 0.3 ms. Owner test
`TestDeepNestingIsLinearBoundedAndCancellable` now prepares the 13.8 MB fixture before the
30 ms deadline starts and requires **both** `context.DeadlineExceeded` **and** return within
500 ms, for a 60 000-element array and for a single 16 MiB string; it passes under `-race`
(twice consecutively in my gate).

### F2. MEDIUM → fixed — inner 503 overload was wrapped into a 200 envelope

Before (real handler stack, 300 KiB pool): plain caller `503 Retry-After: 1`; delivery caller
`200`, no `Retry-After`, envelope `status: 503`; the shim answered a tool error, not `-32000`.

After: `evidence_routes.go` passes a `503` carrying `Retry-After` through unchanged
(`writeOverloaded`), and `evidenceResult` maps an envelope whose status is 503 with
`"overloaded":true` to JSON-RPC `-32000`. Probe: delivery caller now `503 Retry-After: 1`
with no envelope; shim returns `rpcError{-32000}`. Owner tests
`TestDeliveredOverloadPassesThrough503` (built binary) and
`TestShimMapsEnvelopeOverloadToProtocolError` pass.

### F3. MEDIUM → fixed — JSON arrays kept failure elements only up to the 64 KiB target

Before: 3000 `"severity":"error"` diagnostics (231,017 B) → 850 kept, 2150 omitted.
After: all 3000 kept (projection 231,017 B ≤ 1 MiB hard cap, no omission); salient
elements/members are mandatory (whole when they fit, else an equal share that keeps failure
windows), drawing on the slack up to the hard cap, and `errHardCap → ErrUnsupported` when
even that cannot fit. More than 8192 tracked salient elements is also an explicit error.
Owner test `TestAllFailureElementsKeptUpToHardCap` passes.

### F4. MEDIUM → fixed per disposition — object keys were emitted unbudgeted

Before: a valid 2,005,025 B object (500 × 4000-byte keys + `"error":"boom"`) allocated
15.9 MB before failing. After: every write is capped and the key total is checked before
writing, so the same input fails explicitly after allocating 2.4 MB. Disposition (Root): the
explicit size error is plan-permitted; no key placeholders; no handle. Owner test
`TestHugeKeysFailFastWithoutUnboundedAllocation` passes.

### F5. LOW → fixed — unreduced deliveries reported `captured_identity: ""`

Now `"unknown"` (`store.go` sets it before the early return). Probe and owner test
`TestPassthroughReportsIdentityUnknown` confirm.

### F6. LOW → fixed — `Read` racing `Revoke` answered `corrupt`

`Read` now resolves metadata, authorizes, and opens `raw.bin` under the workspace lock
(`openOriginalLocked`); expiry removal happens under the same lock, and the open file stays
readable after an unlink. Owner test `TestReadRacingRevokeIsNeverCorrupt` forces the
interleaving through a seam and accepts only data or `ErrRevoked`, then requires
`ErrRevoked` afterwards; passes under `-race`.

### F7. LOW → closed — response construction was not admitted

Shim: `evidenceResult` reserves 2 × body before decoding the envelope, and plain replies
reserve 1 × body (`reserveReply`, held in the worker's ledger until the reply is written).
API: `runCoreCtx` reserves 2 × captured output for the caller's decode and response
construction when a request ledger is present; the delivery envelope reserves
2 × projection + 4 KiB before encoding. Owner tests `TestShimResponseConstructionIsAdmitted`
(300 KiB pool, 200 KiB projection) and `TestResponseConstructionIsAdmitted` (built binary,
3 MiB pool, 1.2 MiB core output) pass. `writeJSON` for non-delivery routes is unchanged and
remains the disclosed Stage 1 gap.

### F8. LOW — a Rust child per 64 KiB page (quantified; semantics kept by disposition)

`GET …/evidence/{handle}` samples the current identity on every page, so paging a 16 MiB
original spawns 256 `repo-key` children, each taking one of the 4 helper-child slots.
Measured against the real handler stack with a trivial fake core:

| original | pages | children during paging | latency |
| --- | --- | --- | --- |
| 828,136 B | 13 | 13 (one per page) + 2 at capture | 80–107 ms total, 6.2–8.2 ms/page |

That is the spawn-only floor; the real `repo-key` adds a `git status` and dirty hashing per
page (the owner reports ~50 ms/page end to end in the e2e script, outside my scope). Per-page
current identity is the contract; any optimization is separate work.

### F9. MEDIUM → fixed — F1's first patch narrowed whitespace and dropped valid failures

The first salience patch bounded the status patterns to `\s{0,16}` so a match would fit the
64-byte confirmation window. Probe (3000-element array, 700 KB, element 2222 in the omitted
middle): `"ok":` + 16 spaces + `false` kept, 17 / 40 / 80 / 128 spaces dropped;
`"exit_code":` + 80 / 128 spaces + `1` dropped — while the original `\s*` pattern matched them.
Root ruled this not acceptable as a format limitation.

After: the pattern is `\s*` again and `confirmWindow` extends the confirmation window across
any adjacent separator run (whitespace, `:`, `=`, `"`, plus the rest of an identifier such as
`exit_code`) up to 64 KiB, refusing a longer run explicitly (`errSeparatorRun →
ErrUnsupported`). Probe: all nine cases (ok × {1,16,17,40,80,128}, exit_code × {1,80,128}) are
kept, each agreeing with the full-document regexp. Owner test
`TestFailureStatusWithLongLegalWhitespaceIsKept` (300 spaces / 400 newline-space pairs / 200
tabs, JSON and text) passes and asserts the failing **values** (`ok == false`, `exit_code == 3`,
`"success"\s*:\s*false` in text), plus an explicit refusal for a 70 KiB separator run.

### F10. MEDIUM → fixed — text mode cut a mandatory failure line at 4 KiB

Raised by Root from the settled source: pass one detected a late `FATAL` on a long line and
marked it mandatory, but pass two emitted only the 4 KiB display prefix, so the failure bytes
were not in the projection. Probe on the pre-fix snapshot: 12 000 passing lines with a middle
line of 8 KiB filler + `FATAL boom` (165,094 B) → projection without `FATAL boom` (line
selected, `line_tail` omission). After: pass one examines each line up to 1 MiB
(`maxSalienceLine`; a longer line is an explicit `ErrUnsupported`), a salient line is kept
whole at its full cost (`whole[idx]`, counted against the hard cap), and only context lines stay
truncated. Probe: both fixtures now contain the failure text (projections 6,162 B and 9,345 B,
`lines` omissions only). Owner test `TestLateFailureOnLongLineIsEmitted` (20 000 lines, 260 KB,
asserts `Reduced` and the exact `FATAL boom: disk quota exceeded at store.go:88`) passes under
`-race`.

### Salience saturation (raised by Root; verified)

Exhausting the 1 Mi failure-position index is an explicit `ErrUnsupported`, never a prefix
fallback. Probe: `[{"log":"error"×1.1M}, 50 passes, {"pad":70 KiB,"fatal":"disk on fire"}]`
(5.57 MB) → `unsupported: more than 1048576 failure-evidence positions` in 1.9 s; the same
document without the saturating element keeps `disk on fire`. Owner test
`TestSalienceBudgetExhaustionIsExplicit` passes. The old `salientSaturated` fallback branches
in `salientIn`/`salientRange` are now unreachable (dead code, see below).

### writeJSON (previously described as unchanged — corrected)

`cmd/xmustard-api/main.go` `writeJSON` now writes a valid `json.RawMessage` payload directly
(status, content type, trailing newline) instead of re-encoding it through a second growing
buffer, and falls back to the encoder for anything else, including invalid raw input.
`TestWriteJSONRawMessageMatchesEncoder` checks decoded equality, status, content type and the
newline on three payloads plus the invalid-input fallback; it passes. The fast path changes
byte formatting only (no compaction or HTML escaping of `<`, `>`, `&`), never the decoded value.
Non-`RawMessage` responses still allocate the encoder buffer unreserved (disclosed Stage 1 gap).

## Open, non-blocking

- **Doc claim, closed.** `docs/reviews/2026-09-24-implementation-results.md` ("Salience
  follow-ups") now records the rejected `\s{0,16}` step and the shipped unbounded `\s*` pattern
  with the explicit 64 KiB separator-run limit; it matches the source.
- **Dead code.** `salientSaturated` and its fallback branches in `salientIn`/`salientRange` are
  unreachable now that saturation returns an error (`budget.AdmittedReader` has been removed).
- **Text pass one now retains up to 1 MiB per line transiently** while scanning (was 4 KiB);
  bounded and released per line, noted for the resource gate.
- **`writeJSON` for non-`RawMessage` payloads** still encodes unreserved (Stage 1 disclosed gap).

## Stage 1 recheck (`2026-09-24-fable-runtime-review.md`)

| # | Status | Evidence |
| --- | --- | --- |
| 1 MCP ingress not admitted | Fixed | `readAdmittedLine` reserves each chunk before append, the frame again before decode, params before argument decode, 2 × argument bytes before the request; the worker owns the ledger until the reply is written; 4 KiB fixed control headroom serves only notifications and `ping`. `TestShimIngressFramesAreAdmitted` (built shim, 1 MiB pool, 6 × 7 MiB frames + 1 small: only the small call reaches the API, shim RSS < 64 MiB), `TestProbeIDUsesOnlyTopLevelID`, `TestControlFramesReadUnderExhaustedPool` pass under `-race`. |
| 3 permanent oversize answered 503 | Fixed | `Content-Length > limit` → 413 before admission; `TestOversizedDeclaredBodyIs413NotRetryable` passes. |
| 5 helper children outlive shutdown | Fixed | `srv.Shutdown` → `cancelBase()` → `rustcore.KillActiveChildren()` (every tracked helper group); `TestShutdownEndsInFlightHelperChildren` and `TestSIGTERMPersistsInterruptedRunBeforeExit` pass against the built binary. |
| 2 long-lived captures share child slots | Not changed | Root: documented policy trade-off; no contrary evidence found. |
| 4 post-Wait group kill | Not changed | Root: unreproduced, non-blocking; no contrary evidence found. |

## Verified correct in the evidence module and wiring (by reading and tests)

- **Middleware order** is body-limit → auth → core-only → delivery → mux, so the principal is
  in context when evidence is issued and read; `principalScope` binds to stable `Principal.ID`;
  enforced auth without an actor is refused at `Capture`; anonymous-issued artifacts become
  unreadable once auth is enforced; handles never resolve across workspaces (directory +
  `WorkspaceID` check); tampered/malformed handles are `missing`/`invalid_handle`.
- **Handles**: `xm1.` + base64url(32 random bytes), stored under SHA-256(handle).
- **Quota/expiry/restart**: pending spool bytes count toward the quota; expired originals are
  reclaimed under pressure or on read; unexpired originals are never evicted; used bytes are
  rebuilt lazily from disk after restart; a full quota is 507; `raw.bin` is renamed into place
  before `meta.json` makes it readable; reads fail closed on a size mismatch or short page.
- **Wire safety**: pages are standard base64; every projection is valid UTF-8 (passthrough and
  unsupported-passthrough refuse invalid UTF-8; text replaces per line with U+FFFD plus an
  `invalid_utf8` omission; JSON output is checked). Binary/multimodal content types are never
  reinterpreted as text.
- **Source identity**: the middleware samples identity before the handler and `Capture` samples
  it again; `bound` only when both complete and equal; `POST …/evidence` results are always
  `unknown`; pages report `captured_key`, `current_key`, `freshness`, `stale`.
- **Concurrency/cancel/panic**: the delivery seam discards the spool on handler panic and on
  client cancel (`TestDeliverySeamReleasesSpoolOnPanicAndCancel`); a cancelled capture issues
  no handle and retains nothing; five concurrent 16 MiB captures against a 64 MiB pool give
  `[200 200 200 200 503]` with pool peak ≤ max and 0 in use afterwards.
- **MCP protocol**: `initialize` advertises `resources`; `resources/list`,
  `resources/templates/list`, `resources/read` return a base64 `blob` with `_meta["xmustard/page"]`;
  missing/expired/revoked/denied → `-32002`, bad offset/length → `-32602`, overload → `-32000`;
  the issued URI now carries `?workspace_id=` so it survives a shim restart.

## Gates run (direct exit codes; `run_in_background` jobs wrote each command's `$?`)

Against the final snapshot (started 16:48:03 after a 45 s quiet window; hashes identical before
and after):

| Step | Command | Result |
| --- | --- | --- |
| build | `cd api-go && go build ./...` | exit 0 |
| vet | `go vet ./internal/evidence ./cmd/xmustard-api ./cmd/xmustard-mcp` | exit 0 |
| evidence | `go test -race -count=2 ./internal/evidence` | ok (14.3 s) |
| mcp | `go test -race -count=1 ./cmd/xmustard-mcp` | ok (3.4 s) |
| api evidence | `go test -race -count=1 -run 'Evidence\|Delivery\|Delivered\|Freshness\|Concurrent16MiB\|CancelledCapture\|ResponseConstruction\|Overload' ./cmd/xmustard-api` | ok (30.3 s) |
| writeJSON | `go test -count=1 -run TestWriteJSON ./cmd/xmustard-api` | ok |
| probes | overlay tests `TestProbe*` in evidence, api, mcp | ok (all values quoted above; delivery 503 pass-through, shim `-32000`, 6.8 ms/page, whitespace 9/9 kept, saturation explicit in 3.4 s) |

Against earlier snapshots of the same day (identical to the final one except
`reduce.go`/`salience.go`/`review_test.go`, which the Stage 1 process tests do not exercise):

| Step | Command | Result |
| --- | --- | --- |
| budget | `go test -race -count=1 ./internal/budget` | ok |
| rustcore | `go test -race -count=1 -run 'Pool\|Refusal\|Cancel\|Kills\|Overflow\|Admit' ./internal/rustcore` | ok |
| api Stage 1 + Stage 2, normal | `go test -count=1 -run 'Evidence\|Delivery\|Delivered\|Freshness\|Concurrent16MiB\|CancelledCapture\|BodyLimit\|Oversized\|Shutdown\|SIGTERM\|RustCapture\|SmallRequest\|CoreChild\|ResponseConstruction\|Overload' ./cmd/xmustard-api` | ok (12.5 s; earlier -v run: 15/15 PASS) |

Not run here: `make check-backend` as a whole (Rust owned elsewhere), `scripts/e2e/mcp-evidence.sh`,
`scripts/bench/rss.sh`, the gold retrieval gate. Byte admission is not an RSS ceiling; the
combined resource gate remains pending and is not implied by anything above.

## Remaining gates before merge

1. Stdio-MCP end-to-end against the real API (`scripts/e2e/mcp-evidence.sh`), including restart,
   cancellation, expiry, concurrency, repository mutation and auth.
2. The RSS gate on the fixed workload (sampled-tree peak, per-process child high-water reported
   separately, pass only at or below 100 MB).
3. Rust review (`2026-09-24-fable-rust-review.md`) and the combined program review.
4. Human review of the exact final diff; agent approval does not authorize a merge.
