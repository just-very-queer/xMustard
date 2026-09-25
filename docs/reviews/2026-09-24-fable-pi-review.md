# Pi adapter review (Claude Fable 5.1, independent)

Verdict for the assigned scope: **APPROVED** (pass 3, final reviewed source below). The
earlier CHANGES_REQUIRED conditions are met and were re-verified in an independent run.
Human review of the final diff remains required before any merge; this approval covers
only the Pi adapter scope listed here.

Conditions set at pass 2 and how they were met:
1. `diagnostics` succeeds: a disposable native Postgres fixture (`PgFixture` in
   `test/e2e/harness.ts`: `initdb` into the temp dir, fixture-only role with trust auth,
   loopback on a random port, private socket dir, no durability, deleted on stop) is
   configured through the existing settings/bootstrap routes on the temp data dir and one
   LSP diagnostic is materialized through the existing `diagnostics/run` route. The
   nine-tools test then asserts a non-error `diagnostics` result containing the seeded
   diagnostic at `pkg/handler_0007.go`. Verified: run 4 records
   `diagnostics_success {path: pkg/handler_0007.go, line: 6}`, fixture `pgdata` removed
   after the run, no leftover fixture process. If `initdb/postgres/psql` are absent the
   script stops with a message; `XM_E2E_ALLOW_NO_POSTGRES=1` is the explicit opt-out and
   then the summary records `diagnostics success NOT covered`.
2. `verify` assertion is strict (`isError === false` and the answered id equals the
   remembered entry). Verified non-error in run 4.
3. Wording: README documents the signal-absent caveat, the Go digest formula, "two
   concurrent Pi processes", the Postgres fixture as test-only (not the default no-DB
   deployment), and RSS sampling as a sampled per-tree figure. RSS numbers themselves are
   bench scope and not reviewed here.

Scope: `integrations/pi` (source, tests, README, package pin), `scripts/e2e/pi-adapter.sh`,
and the Pi-facing HTTP contract in `api-go/cmd/xmustard-api/evidence_routes.go` and
`api-go/cmd/xmustard-mcp/main.go` (`tools()`), against plan Stages 4–5 in
`docs/plans/2026-09-24-lean-context-implementation.md`. Excluded: Go/Rust defects under
separate review, `scripts/bench/rss.sh` and RSS acceptance, harness targets, resource or
competitor-parity claims. No source was edited; this file is the only artifact.

Worktree `/private/tmp/xmustard-opus-l9b1F4`, base `cd13e2b`; reviewed source is
uncommitted. Final reviewed hashes (`git hash-object`, first 8): `src/config.ts 757e342f`,
`src/delivery.ts 9311b1d0`, `src/http.ts ff6c1775`, `src/index.ts 46ebfe58`,
`src/tools.ts be14bd4d`, `test/unit.test.ts 2af76d96`, `test/e2e/harness.ts 8c63c7be`,
`test/e2e/pi-adapter.e2e.ts 81460220`, `test/fixtures/scripted-provider.ts 8bdc25ac`,
`package.json 481ce471`, `package-lock.json f00b38b0`, `scripts/e2e/pi-adapter.sh
4137ec56`, `README.md 5864df60`. Hashes were identical before and after run 4. Any later
edit to these files is outside this approval.

## Checks run by the reviewer (true exit codes)

| Check | Result |
| --- | --- |
| `npx tsc --noEmit -p integrations/pi/tsconfig.json` (Node v26.7.0) | exit 0 |
| `node --test integrations/pi/test/unit.test.ts` | 16 pass / 0 fail |
| `scripts/e2e/pi-adapter.sh` run 1 (16:19) | **exit 1** at `go build`: `internal/evidence/reduce.go` was mid-edit by the Go implementer (undefined `context`, arity mismatches). Unstable snapshot, not a Pi defect. |
| `scripts/e2e/pi-adapter.sh` run 2 (16:23) | exit 0; unit 16/16, e2e 15/15 |
| `scripts/e2e/pi-adapter.sh` run 3 (pass-2 source) | exit 0; unit 16/16, e2e 15/15; diagnostics error-only |
| `scripts/e2e/pi-adapter.sh` run 4 (final source, Postgres 16.14 fixture) | **exit 0; unit 17/17, e2e 15/15; all nine tools non-error**; artifacts `~/.claude/jobs/b760afe6/tmp/xm-pi-e2e.vlPGGf` |

Runs used temp repos, temp data dirs and random loopback ports; no provider was called,
no user credential was read, no real data was modified. Go/Rust binaries were built from
the worktree at run time (Rust into the gitignored `integrations/pi/.cache`).

Pin verified: `package.json` and `package-lock.json` resolve
`@earendil-works/pi-coding-agent@0.87.1` with integrity; the installed package reports
0.87.1; the script refuses any other version. The README's claim that the researched
revision `8676a0d` has an unchanged extension surface vs. 0.87.1 was not independently
diffed by this review (offline).

## Findings

F1 (resolved during review) — `README.md` was referenced by `src/index.ts:5` and the
script but absent at 16:15; it exists since 16:17:55 and documents setup, env, delivery
paths, failure table, tests and the no-signal limitation.

F2 (resolved at pass 3) — at pass 2 the nine-tools e2e test accepted an error result
for `verify` and for `diagnostics`. In the reviewer's runs `verify`
succeeded (non-error, returned the remembered entry) and `diagnostics` returned the
explicit error `invalid diagnostics request: Postgres DSN is required to read
diagnostics`. Final evidence (run 4): 9/9 tools return non-error results through the real Pi runtime
and reach the next model request; `diagnostics` delivers the seeded diagnostic; the
missing-run `why_failed` stays an explicit error.

F3 (documented limitation) — signal-absent timeout behavior is covered by the unit test
only; Pi 0.87.1 always supplies a signal while streaming. The README states this. Not a
runtime-proven case; do not claim it as one.

F4 (resolved during review) — hook-mode `args_digest` originally differed from Go's
source-mode digest. `src/delivery.ts` (hash `9311b1d0`) now implements
`SHA-256("METHOD path?url.Values.Encode()\n" ‖ SHA-256(body))`. The reviewer ran a Go
`net/http` probe (job scratch, not committed) over the three unit-test inputs; all three
hex values match the unit assertions exactly, so the "same digest" claim now holds.

F5 (retracted) — interim doubt about in-process concurrency: the concurrent-a trace shows
four `tool_execution_start` events before any `tool_execution_end`, so Pi does run the
four `impact` calls in parallel within one process, and two processes run concurrently.
The test's claim stands.

F6 (verified) — the earlier "unreachable" test only showed `Tool xmustard_expand not
found` (tool inactive). The added test "activated xmustard_expand whose Go endpoint fails"
issues a handle, then a connection reset (`unreachable`), then a hang past a 1500 ms
deadline (`timed out after 1500 ms`, proxy saw the request closed), then recovery on the
same handle, with the trace asserting `xmustard_expand` was active for every request.
Passed in runs 2 and 3.

## What the evidence proves (scope items)

- **Nine direct HTTP tools, schema conformance.** Conformance test diffs Pi's first model
  request (tool definitions recorded by the scripted provider) against the live stdio
  `tools/list` from `xmustard-mcp`: names, descriptions and `inputSchema` equal for all
  nine; `xmustard_expand` absent from active tools at start; Pi's `read/bash/edit/write`
  remain active. Source check: `toolParameters` matches `toolsListResult()` shape;
  path/query escaping reproduces `url.PathEscape`/`url.QueryEscape` (unit-tested).
- **Same Go evidence owner.** Source mode sends `X-Xmustard-Delivery: xmustard.evidence/v1`
  and Go's middleware spools/captures/projects; hook mode posts raw bytes to
  `POST /evidence`; both page via `GET /evidence/{handle}`. The adapter has no reducer.
  Auth middleware wraps outside the delivery middleware, so 401/403 arrive as plain
  errors, never envelopes (observed: `-> 401: authentication required`).
- **tool_result handling / next-model delivery.** `assertModelSawResults` proves every
  finalized tool result reaches the next model request byte-for-byte with its error flag.
  Pi's `emitToolResult` starts from the original event and overrides only returned
  fields (`runner.js:803-846`), so delivered errors keep content and non-xMustard tools
  are untouched.
- **Expansion activation and exact pages.** A 239,220-byte `impact` result was projected
  to 65,941 text bytes with `captured_identity: "bound"`; the trace shows
  `xmustard_expand` inactive in the request before the handle and active in the next;
  four pages of ≤ 64 KiB reassemble to the raw SHA-256. Stale (repo mutation), unknown
  (hook path), restart and expiry (`410 expired`, then 404, never re-served) all passed.
- **Errors, limits, abort, 5 s projection vs 60 s execute.** RPC abort closed the
  in-flight request at the proxy in ~650 ms; tool deadline observed at a lowered 1500 ms;
  projection deadline at a lowered 1000 ms preserved a ≤ 64 KiB `recall` original exactly
  and turned the oversized `impact` into an explicit size error. Defaults are 60 s / 5 s,
  lower-only via env. Response reads are capped (16 MiB raw, 8 MiB envelope, 1 MiB page).
- **No sidecar on load.** `xmustard()` only reads env and registers tools/handlers; the
  unreachable-Go run loads and runs with no API listening.
- **Token auth and credential isolation.** Under `XMUSTARD_AUTH=required`: no token → 401;
  handles bound to the principal; token rotation keeps access; another principal → 403;
  no minted token appears in Pi stdout/stderr/trace. Unit tests assert the secret is
  absent from 401 and unreachable errors and that userinfo is stripped from the base URL.
- **Host-native pass-through.** `read` (success: byte-equal file content; error: ENOENT
  without any `[xmustard` text) and `bash` (`pass-through`) run untouched, with no
  request reaching the xMustard proxy.
- **Real pinned Pi 0.87.1 with an offline scripted provider.** The e2e drives the real
  `pi` CLI (JSON and RPC modes) with `-e src/index.ts -e test/fixtures/scripted-provider.ts`;
  the provider is pi-ai's faux core with a placeholder key and `127.0.0.1:9`, empty
  HOME/agent dir, scrubbed env.

## Exact limitations

- Coverage is the Pi extension seam only: no interception of Pi's built-in tools, no
  claim about other clients (README says so).
- `diagnostics` success depends on the test-only Postgres fixture; xMustard's default
  no-DB deployment still answers `diagnostics` with the explicit Postgres error.
- Signal-absent timeout: unit level only (F3).
- Hook mode: provenance is always `unknown`/`stale: true` by design; a 16 MiB original
  under the 5 s projection deadline becomes a size error rather than a projection.
- Source mode: if Go rejects capture (quota full, over 16 MiB), the model receives an
  explicit HTTP error and the original is not delivered; results at or below the 64 KiB
  projection allowance are admitted even when the quota is full (Go `Spool.Write`).
- Not reviewed here: Go/Rust internals, RSS/bench, competitor parity, 50–100 MB target.
