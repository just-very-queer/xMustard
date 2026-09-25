# Reconciled plan review — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: revised `docs/plans/2026-09-24-lean-context-implementation.md`.

## Verdict: APPROVED

All eleven prior findings are resolved: Pi tools register directly over HTTP, the
signal question is settled by the pinned extensions doc plus an adapter timeout,
expansion has one Go endpoint with MCP `resources/read` and `xmustard_expand`,
auth scope and byte-admission limits are stated honestly, dirty-file bytes are
hashed, staleness is labeled, cache keys carry repo/trust identity, and Step 5
names executable scripts, a fixture, and numeric gates. The remaining items below
are implementation-level amendments Opus 5.5 should fold in; none changes the
contract or blocks starting.

## Amendments to apply during implementation

1. **Expansion pages must be byte-safe.** A 64 KiB page can split a multibyte
   UTF-8 sequence, and originals may be invalid UTF-8. Serve `resources/read`
   pages as `blob` (base64) or align page ends to UTF-8 boundaries and report the
   actual byte range returned. Same rule for the HTTP endpoint via an explicit
   content-type. Test: page across a 4-byte character; page an invalid-UTF-8 original.

2. **Advertise the resources capability.** The shim's `initialize` reply
   (`api-go/cmd/xmustard-mcp/main.go:393`) declares only `tools`. Add
   `resources: {}` and answer `resources/list` with an empty list or a template so
   conforming clients will call `resources/read`. Test: `initialize` shows the
   capability; `resources/read` on an unknown URI returns the MCP resource-not-found
   error, not a tool error.

3. **Builder serialization must be cross-process.** Rust runs as a fresh child per
   call and `rust-core` has no file-lock dependency today. "Serialize competing
   builders for one key" needs an advisory file lock on the cache key with a bounded
   wait and a stale-lock policy, since in-process locks do not cover two API-spawned
   children. Test: two `xmustard-core` processes build the same key concurrently and
   exactly one valid cache results.

4. **Pi tool execution needs its own timeout.** The five-second budget is for the
   `tool_result` delivery pass. Tool `execute` calls (search, impact, ground on a
   cold 500-file repo) can legitimately exceed that. State a separate execute
   timeout matching the shim's 60 s HTTP client and honor the execute `AbortSignal`.
   Test: cold search under a fake transport completes; abort during execute reaches Go.

5. **Bound dirty-file hashing.** Hashing every dirty file's bytes on each query is
   O(dirty bytes); a large generated dirty file makes warm queries slow. Hash through
   the existing bounded reader cap and mark over-cap files `oversized` in the key so
   freshness degrades visibly rather than silently. Test: dirty file above the cap
   still changes the key on edit and is reported in coverage.

6. **Sampled RSS undercounts short-lived Rust children.** `ps` at 100 ms can miss
   a child's peak. Have the API also record `getrusage(RUSAGE_CHILDREN).ru_maxrss`
   at shutdown and report both numbers; the higher one is the tree peak. Test:
   bench output shows both fields and the gate uses the max.

7. **Name the principal for enforced auth.** Bind evidence handles to the token's
   minted identity ID, not the token string, so rotating a token for the same
   identity does not orphan handles. Test: re-minted token for the same ID reads
   its handle; a different ID is denied.

## Confirmed adequate

The 5×16 MiB admission test against the 64 MiB pool, `--porcelain=v1 -z`
parsing, projection/frame size relationships, the 12-query fixture gate, and the
stated limits on what byte admission proves are all consistent with source and
with `VISION.md`. No scope expansion is required or proposed.

## Root disposition

The independent approval applies to the revised program, not implemented behavior.
Amendments 1–4 and 7 are accepted as implementation clarifications. Amendment 5
must not turn a bounded prefix hash into a claim about unread bytes: oversized
inputs must remain visibly excluded/degraded, never silently current.

Amendment 6 correctly identifies sampling gaps, but its proposed interpretation
is not accepted. Child high-water RSS is a separate diagnostic, not an aggregate
concurrent tree peak; taking the maximum of it and sampled tree RSS cannot recover
a missed concurrent peak. Report platform-correct units, both observations and
the sampling limitation. Retain the specified workload and sampled <=100 MB gate,
without claiming a hard universal memory ceiling. See the
[Linux getrusage manual](https://man7.org/linux/man-pages/man2/getrusage.2.html).

Review session: `12ba2daa-4282-4c1e-bf82-dc08cbfa2cdc`; actual assistant model
`claude-fable-5-1`. The CLI forked the prior review when resume flags changed.
