# Pi adapter implementation results — 2026-09-24

Scope: plan Stage 4 and the Pi parts of Stage 5
([plan](../plans/2026-09-24-lean-context-implementation.md)). Implementer: Claude Opus 5.5.
Paths written: `integrations/pi/**` and `scripts/e2e/pi-adapter.sh` only. No Go, Rust, MCP,
UI, global config or credential changes, and no paid or provider model calls. Nothing
is committed: human review of the exact diff is still required before any merge.

## Pinned Pi

- Researched revision: `earendil-works/pi@8676a0dcd8f9f6bca78835e63c8cd31493c4154d`
  (formerly `badlogic/pi-mono`). `packages/coding-agent/package.json` at that revision
  reports version `0.87.1`.
- Installed: `@earendil-works/pi-coding-agent@0.87.1`, exact version in `package.json`,
  locked with an integrity hash in `integrations/pi/package-lock.json`. The package
  ships its own `npm-shrinkwrap.json`. Install is project-local only (`npm ci
  --ignore-scripts`).
- Compatibility: the published `0.87.1` has gitHead `f07218c4`. GitHub's compare shows
  `8676a0d` is 17 commits ahead and 0 behind. Diffing `core/extensions/{types,loader,index}.ts`,
  `core/sdk.ts` and `src/index.ts` between the two found only additions: a
  `provider_stream_event` event and image/classifier provider-config types.
  `registerTool`, `on("tool_result")` and `ToolResultEventResult`, `getActiveTools`/`setActiveTools`,
  `ExtensionContext.signal`, `registerProvider(streamSimple)` and the faux provider are
  unchanged. The tests run the published build, not a source build of `8676a0d`.
- Node >= 22.18 (unflagged type stripping). This is enforced in `engines`, the script
  preflight and the README. Tests ran on Node 26.7.0.

## What was built

| File | Role |
| --- | --- |
| `integrations/pi/src/index.ts` | Extension factory. Registers the nine tools, `xmustard_expand`, and the `session_start` and `tool_result` handlers. No load-time I/O or timers. |
| `src/tools.ts` | The nine tool specs (names, descriptions, schemas, method/path/body) mirrored from `xmustard-mcp`, with byte-identical Go `QueryEscape`/`PathEscape` escaping |
| `src/delivery.ts` | Source and hook delivery, evidence footer, fallbacks, bounded pending-call map, expansion paging |
| `src/http.ts`, `src/config.ts` | Bounded streaming reads, per-call deadline plus caller `AbortSignal`, bearer token that never reaches error text, lower-only timeouts |
| `test/unit.test.ts` | 17 unit tests against an in-process HTTP server (including `args_digest` matching Go's values) |
| `test/fixtures/scripted-provider.ts` | Offline scripted model: pi-ai faux core behind `pi.registerProvider`. Records each model request's active tools and new tool results. |
| `test/e2e/{harness,pi-adapter.e2e}.ts` | Real API + Rust core + the `pi` CLI, fault-injection proxy, disposable Postgres fixture, RSS sampler, 15 e2e tests |
| `scripts/e2e/pi-adapter.sh` | Preflight (Node >= 22.18, native Postgres), `npm ci`, version-pin check, fresh Go build, Rust build into the gitignored `.cache`, then typecheck, unit and e2e |
| `integrations/pi/README.md` | Setup, configuration, provenance, failure behavior, coverage and non-claims |

**Delivery design (deliberate deviation from the literal plan).** The plan projects
through `tool_result` → `POST /evidence`. Root's provenance ruling says POSTed bytes
must stay `captured_identity: unknown`, so the **default** path (`source`) has
`execute` call the tool route with `X-Xmustard-Delivery`. Go then captures and
projects at the source with before/after identity, which gives `bound` and later
`stale`. The plan's path is kept as `XMUSTARD_PI_DELIVERY=hook`, and the `tool_result`
hook also handles any raw result (for example from a server without delivery support).
In the source path the hook passes the Go envelope through, restores error details
(Pi drops `details` when `execute` throws) and activates `xmustard_expand` on a handle.
Non-xMustard tools return `undefined` from the hook and are untouched.

## Commands and results

```text
cd integrations/pi && npm run check     # tsc + 17 unit tests: pass
scripts/e2e/pi-adapter.sh               # exit 0; native Postgres 16.14 fixture
  unit 17/17 pass; e2e 15/15 pass (real pi CLI 0.87.1, JSON + RPC modes)
```

Root independently confirmed `npm run check` and a green script run (16 unit + 15 e2e,
before the `args_digest` test and the Postgres fixture were added). One independent
attempt failed only because the Go tree was mid-edit (`evidence.Reduce` signature
change).

E2E coverage (each is a real Pi run against the real API unless noted):

| Case | Evidence |
| --- | --- |
| Schema conformance | Tool definitions in Pi's first model request deep-equal live `xmustard-mcp tools/list` for all nine tools; `xmustard_expand` absent from the initial active set |
| All nine tools succeed | Each runs through Pi. Every finalized result appears byte-identical, with its error flag, in the next model request. `verify` succeeds (`isError: false`, answer id = the `remember` id). `why_failed` explains a seeded failed run; a missing run stays an error (404). `diagnostics` succeeds against the Postgres fixture, delivering the seeded `pkg/handler_0007.go` line 6 `xm_e2e_diagnostic` record. Without Postgres it is an explicit 400 error, and the script refuses to run unless opted out. |
| Projection + paging | `impact` on a 1,500-file repo: 239,220-byte original, 65,460-byte Go projection, `captured_identity: bound`. `xmustard_expand` becomes active only in the request after the handle. 4 pages, each ≤ 64 KiB and contiguous, reassembled SHA-256 equals `raw_sha256`, all `current`. |
| Repo mutation | After editing a tracked file, the old handle serves the captured bytes, labelled `stale: true` with `captured_key != current_key` |
| Hook path | POSTed results show `captured_identity: unknown`; every page `freshness: unknown, stale: true` even with the repo unchanged; errors stay errors |
| Concurrency | Two concurrent Pi processes, each issuing 4 tool calls in one assistant turn, give 8 distinct handles, each re-read exactly. Whether Pi ran the calls within a turn in parallel is not asserted. |
| API restart | A pre-restart handle expands through Pi to the same bytes; full re-read SHA matches |
| Expiry | With 2 s retention the expand fails with an explicit `410 expired`; a later read gives `404`, never content |
| Abort (RPC `abort`) | The in-flight Go request is closed (proxy observed); error result; acknowledged in about 0.5 s |
| Tool deadline, signal present | Hung route becomes an error at the lowered 1,500 ms deadline, and the request is closed |
| Projection deadline | Hook POST hung: a small `recall` original passes through exactly (reason in details); a 239 KB `impact` becomes an explicit size error |
| Unreachable Go | xMustard tools give explicit `unreachable` errors; Pi rejects an inactive `xmustard_expand`; `read` succeeds unchanged |
| Activated expander, endpoint fails | After a real handle activates it: reset connection gives `unreachable`, hang gives a 1,500 ms timeout, neither returns page content, and the same handle reads again after recovery |
| Pass-through | A successful `read` is byte-identical, a failed `read` and a successful `bash` are untouched, and zero xMustard requests are made |
| Auth (`XMUSTARD_AUTH=required`) | No token gives 401. Handle bound to `agent-a`. After re-minting `agent-a`, the old token gets 401 and the new token reads the handle. `agent-b` gets `403 denied`. No token string appears in any Pi stdout, stderr or trace. |

Signal-absent projection timeouts are proven by unit tests only. In real Pi runs,
`ctx.signal` is always present in `tool_result`, so that path cannot occur there.

Hook-path `args_digest` now uses Go's formula exactly: SHA-256 of `"<METHOD> <unescaped
path>?<sorted, QueryEscaped query>\n"` followed by SHA-256(body). Three unit vectors
were computed with Go's `net/http` and the middleware's own code. The digest is
client-supplied audit metadata that Go does not verify.

## Resource sampling (Stage 5, Pi workflow)

The sampler in `test/e2e/harness.ts` runs `ps -axo pid,ppid,rss,comm` every 100 ms and
attributes each process tree to a registered root: the API (with its Rust/git children),
the MCP shim, Pi, or the Postgres fixture. The test driver, proxies and builds are
excluded. Every value is the maximum across samples, not an OS high-water mark; the
per-process field is named `per_process_sampled_max_mib` accordingly. Last run: 358
samples, 17 skipped ticks (a `ps` call still running), 0 sampling failures.

| Scope | Sampled peak | Composition at peak |
| --- | --- | --- |
| xMustard-owned | 70.5 MiB | API plus `xmustard-core` and `git` children (concurrency test) |
| Pi external | 250.7 MiB | two Pi processes (about 110–130 each) |
| Postgres fixture (test-only, external) | 45.8 MiB | postmaster plus backends |
| Full workflow | 334.0 MiB | all of the above at one sample (concurrency test) |
| Single-Pi phases, earlier run without the fixture | 120–170 MiB | one Pi process at about 105–133 MiB plus the API tree |

Root's independent run (before the fixture) sampled xMustard 65.8, Pi 250 and full
310.4 MiB over 470 samples with no failures.

This is the e2e conformance workload, not the fixed `scripts/bench/rss.sh` workload
(owned by the Go owner). During the expiry and auth tests the "xMustard-owned" figure
includes two API instances. The MCP shim was alive too briefly to be sampled
meaningfully. Sampled peaks can miss spikes. These are not a ceiling and not the
100 MB gate. Pi alone exceeds 100 MB and is reported as an external process.

## Requests for the Go owner (not patched here)

1. **Pi provenance.** No new route is needed: Pi uses the existing
   `X-Xmustard-Delivery` on tool routes (with `X-Xmustard-Issuer: pi`,
   `X-Xmustard-Call-Id`, `X-Xmustard-Session-Id`) and gets `bound`. Please keep that
   header path stable. `POST /evidence` correctly stays `unknown`; the README
   documents this limitation.
2. **`diagnostics` needs Postgres.** In the no-DB default the ninth tool returns 400
   "Postgres DSN is required". The e2e proves success only with a disposable native
   Postgres fixture set up through existing routes (`/api/settings`,
   `/api/postgres/bootstrap`, `diagnostics/run`); no Go or SQL changes. A no-DB
   diagnostics read path remains a product decision.
3. **Expired then missing.** Once an expired read deletes the original, later reads
   answer `404 missing` instead of `410 expired`. That is explicit and not
   substituted, but a tombstone would keep the answer stable.
4. **Non-reduced deliveries** report `captured_identity: unknown` even when identity
   was sampled. This is harmless (no handle), but please confirm it is intended.
5. **Token variable names differ.** The MCP shim reads `XMUSTARD_API_TOKEN`; Pi reads
   `XMUSTARD_TOKEN` as the plan specifies. Consider accepting both in docs.
6. **Observation.** A first `auto_scan` load of the 1,500-file fixture took 123 s in one
   run and 0.17–0.19 s in later runs on fresh temp repos. Rust caches are keyed per root,
   so the cause is not cross-repo reuse. It was not investigated, and concurrent builds
   were running at the time. The e2e now scans once and seeds that workspace record into
   the expiry and auth data dirs.
7. `POST /api/workspaces/load` with `auto_scan: false` on a fresh data dir returns 404.

## Non-claims and notes

- No claim about Pi's built-in tools, other clients, task quality or the 100 MB target.
- The fake transport is pi-ai's own faux core loaded through Pi's extension provider API.
  It is test infrastructure, not a substitute for Pi: the tested Pi CLI, extension loader,
  agent loop, hooks and active-tool control are the real ones.
- Process note: three early edits used `sed -i`. After the editing rule arrived, every
  repository edit used `apply_patch`. One throwaway `go.mod` for the Go digest
  cross-check was written with `printf` in the job's scratch dir, outside the repository.
