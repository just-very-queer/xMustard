# Plan final recheck: database-free local diagnostics — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: `docs/plans/2026-09-24-local-diagnostics.md`,
sha256 `b3e9e772cce5f6701ef554de7f954ededa2ab943c615a5ed0e576ca766b8d222`, 224
lines, untracked but allowlisted (`.gitignore:88`). Prior recheck:
`docs/reviews/2026-09-24-fable-local-diagnostics-plan-recheck.md` (plan sha256
`4ac05ecb…`). Checked at HEAD `cd13e2b`, dirty working tree. Every P1/P2/P3
finding from the recheck was compared against the revised text, with targeted
source checks where a resolution depends on a code fact. No build, test, source
edit, or plan edit was made; this file is the only write. Independent review, not
implementation authorization.

## Verdict: APPROVED

Both P1 findings and all six P2 findings are resolved in the text, and the
resolutions match the code they rely on. One P2 amendment remains (the proposed
byte caps still do not fit each other under request-scope accounting, and HTTP
and CLI account differently). It changes numbers the plan itself labels
"proposed defaults", not the seam, the nine tools, the no-fallback rule, or any
acceptance gate, so it is carried into the slice rather than blocking dispatch.
Four P3 notes follow.

## Recheck findings, disposition

| # | Recheck finding | Status | Where in revision | Evidence |
|---|---|---|---|---|
| P1-A | PG control fixture posts out-of-root absolute path | Resolved | lines 207–211 | Fixture writes `join(T,"diagnostics-lsp.json")` with root `join(T,"repo")` (`pi-adapter.e2e.ts:37,79,88`); plan now moves it to `repo/.xmustard-e2e/`, posts a relative path, lists `:79-88` as changed. `resolveWorkspacePath`/`openWorkspaceFileBeneath` accept dot-directories (`safepath.go:44`, `safepath_unix.go:23`), so the relative path is admissible. |
| P1-B | Local status label set undefined | Resolved | lines 33–44, gate 4 (188–198) | Enumerated: `no_baseline` / `available` / `stale`; dirty tree keeps `available` with `dirty_worktree`; HEAD move is `stale` with `head_moved` and takes precedence; never `fresh`/`dirty_provisional`; `storage.status` mirrors; `freshness_basis` is `ingestion_identity` local, `head_match` PostgreSQL. PostgreSQL's five values (`diagnostics.go:384-426`) untouched. |
| P2-A | No-DSN blocker, PG-named activity/message | Resolved | lines 53–58 | Blocker at `diagnostics.go:231`, activity kind `:341`, message `:349` cited and listed as in-scope edits; `storage_backend` on plan and run; `PostgresConfigured=false` retained. |
| P2-B | Lock/admission waits disagree; 503 mapping | Resolved | lines 129, 134–138, 145–146 | One 2 s lock wait; 120 s total deadline composes 10 s child wait (`budget.go:312`), 30/60 s child limits, 5 s git probe, lock and I/O; busy errors wrap `budget.ErrOverloaded`, which `respondError` maps to 503 + `Retry-After` (`main.go:556`, `:432`). |
| P2-C | Caps do not fit; stdout not counted; separate pools | Resolved as asked; arithmetic still short | lines 126–133, 213–214 | Rows ≤20,000; normalize and archive stdout charged; gate 6 samples RSS with CLI import concurrent to API reads. See remaining P2 below. |
| P2-D | 30-day retention plus reject-when-full | Resolved | lines 139–142 | 24 h promise (matches `evidence/store.go:42`) on full envelope and original bytes; reject-when-full reports earliest expiry; prune on next operation; no eviction of unexpired originals. |
| P2-E | Exact bytes must come from Go | Resolved | lines 71–77 | Go keeps captured bytes and computes SHA-256; mismatch with Rust fails the import. Rust hashes `content.as_bytes()` of the input file, not the re-emitted `Value` (`diagnostics.rs:125,141,165,194`), so the comparison is well-defined and passes on identical input. |
| P2-F | Lock-free readers race the pruner | Resolved | lines 149–152 | Pointer written via temp + rename; reader retries pointer-then-envelope once, then returns an explicit error, never `no_baseline`. |
| P3 | Identity hashing outside budget | Resolved | lines 103–104 | Streaming no-follow fd or charge to admission. |
| P3 | Status route platform-only | Resolved | lines 32–33, gate 1 | `diagnostics/status` absent from `coreWorkspaceSubpaths` (`main.go:288-297`); `isCorePath` is an exact allowlist (`:316`), regression-guarded (`core_only_test.go:33`). |
| P3 | Body `dsn` retained | Resolved | lines 48–51 | `DiagnosticsRequest.DSN` (`diagnostics.go:35`) named as retained, not new authority. |
| P3 | No-dial proof via `connectSemanticPostgres` stub | Feasible | gate 1 | Plan says "prove no DB dial"; the stub is an implementation detail and needs no plan text. |
| P3 | Recheck file not allowlisted | Moot | — | `docs/reviews/` is untracked, not ignored; no allowlist entry is needed. |

Other citations added by the revision were re-verified: run-ID shape `diag_` +
12 hex from `hashID` (sha1 hex, `verification.go:487`; `diagnostics.go:494`);
`baseline` is `omitempty` (`diagnostics.go:69,82,172`); git probe is plain
`exec.Command` (`context_packet.go:2065`); `createWorkspaceFileBeneath` exists on
both build tags and creates intermediate directories (`safepath_unix.go:89`,
`safepath_other.go:24`); MCP and Pi read the same GET (`xmustard-mcp/main.go:151`,
`tools.ts:151`); CLI already exposes `--data-dir`, `--input-path`, `--dsn`
(`xmustard-ops/main.go:57-65`).

## Remaining P2

**P2-G. The proposed caps still do not fit under request-scope accounting, and
HTTP and CLI account differently.** A normalized row carries thirteen JSON fields
including a 64-hex fingerprint, an RFC 3339 timestamp, and repeated key names
(`diagnostics.rs:11-24`), so it serializes near 400–450 bytes. Under a request
scope every capture is held until the handler returns (`root.go:36-38`,
`main.go:380-385`), so a maximal HTTP import charges raw capture (≤4 MiB) +
normalize stdout (~9 MiB at 20,000 rows) + archive stdout (re-embeds the payload,
~4.5 MiB) + envelope encoder (≤8 MiB) + response projection, about 25 MiB against
the 16 MiB admission target. Even an envelope-bound import (≈9,000 rows at 4 MiB
raw) charges about 20 MiB. Two consequences: the 20,000-row and 8 MiB envelope
caps are unreachable over HTTP, so gate 3's "max rows" case cannot be exercised
there; and deterministic over-budget inputs surface as 503 + `Retry-After`
(`root.go:66-67`), which promises a retry that cannot succeed. Separately, a CLI
call has no request scope, so `runCoreCtx` opens an owned scope and releases it on
return (`root.go:46-48`); the CLI therefore accepts inputs HTTP refuses under the
same caps, and the plan's "same working admission" (line 130) is only true of
HTTP. Amend in the slice, without re-review: either derive raw/row/envelope caps
from the 16 MiB target (roughly raw ≤1.5 MiB, envelope ≤3 MiB, rows derived), or
raise the per-import admission target and state it; have the CLI import open one
scope across capture, both children, and the envelope so both entry points
account identically; and map a deterministic cap breach to a non-retryable 4xx
distinct from `ErrOverloaded`, keeping 503 for contention. Record the chosen
numbers in the plan's caps paragraph when the slice lands.

## P3 notes

- `stale_reasons` already exists on `DiagnosticsStatus` and carries prose
  sentences for PostgreSQL (`diagnostics.go:391,420`). Line 37 says "add"; the
  slice should say whether local emits the coded tokens (`dirty_worktree`,
  `head_moved`) in that same field or in a sibling, because gate 4 asserts tokens
  and gate 4 also asserts PostgreSQL labels unchanged.
- Line 35 maps `available` to ingestion identity "matched or unknown", but the
  enumerated `ingestion_identity.status` set (line 108) is `matched`, `changed`,
  `partial`, `unavailable`. Say which of `changed` and `partial` map to
  `available` (with a reason) versus `stale`, and use `unavailable` rather than
  "unknown" so the two lists share one vocabulary.
- Line 156 preserves the current entry points "as background wrappers for CLI
  callers", while line 159 gives the CLI a 120 s overall timeout. The CLI should
  call the `Ctx` variants with a deadline context; a `context.Background()`
  wrapper cannot carry the deadline.
- The scanner skip list has no generic dot-directory rule (`scanner.rs:9-28`), so
  gate 6's "excluded from seeded scanning" for `repo/.xmustard-e2e/` must come from
  writing the fixture after the `auto_scan` load (`pi-adapter.e2e.ts:54`) or from
  an explicit skip, not from the directory name alone.

## Sound as written

- `DiagnosticBaselineStore` stays a four-method seam with two real adapters
  (PostgreSQL wrapping unchanged persistence, local store), selected once in
  `selectDiagnosticsStore`; PostgreSQL errors never fall back.
- Nine tools, GET-only `CORE_ONLY` allowlist, MCP and Pi read paths, and
  `auto_scan` as non-producer are unchanged.
- Go-owned immutable capture, Go SHA-256 cross-check, row-path confinement before
  any stat, bounded identity work, `[]` complete versus `{}` rejected, Unix
  `flock` fail-closed elsewhere, ordered publish under lock, and reader retry
  all match the code and the recheck's dispositions.
- Local evidence is `available`, never `fresh`; PostgreSQL label semantics are
  preserved by construction.

## Acceptance gates, feasibility

Gates 1, 2, 4, and 5 are feasible as written. Gate 3's "max rows/identity work"
case and gate 6's RSS sample depend on P2-G's reconciled numbers. The 50–100 MB
process-tree target stays unproven until gate 6 passes, as the plan states.
