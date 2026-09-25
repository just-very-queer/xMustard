# Plan recheck: database-free local diagnostics — 2026-09-24

Reviewer: Claude Fable 5.1. Subject: `docs/plans/2026-09-24-local-diagnostics.md`
(revised; sha256 `4ac05ecbf85057f23787159aa5bd5df37120bf45720cc88fbaf06b02fab6974f`,
171 lines, allowlisted at `.gitignore:86` but still untracked). Prior review:
`docs/reviews/2026-09-24-fable-local-diagnostics-plan.md` (plan sha256
`4cff9315…`). Checked at branch `feat/product-v1`, HEAD `cd13e2b`, dirty working
tree, against `AGENTS.md`, the Go diagnostics module and HTTP/CLI/MCP entry points,
the Rust normalizer and archive commands, the budget and safepath helpers, and the
Pi adapter plus its E2E fixture. No build, test, benchmark, or source edit was run;
this file is the only write. Independent review, not implementation authorization.

## Verdict: CHANGES_REQUIRED

Twelve of the fourteen prior findings are resolved as written, and the root's
dispositions (local evidence `available` but never `fresh` from an imported file;
PostgreSQL legacy labels preserved; Unix `flock` fail-closed elsewhere; no stale-age
`O_EXCL` stealing) are present in the text. Two items still block dispatch: the
PostgreSQL control fixture the plan keeps as a control posts an out-of-root absolute
path over HTTP and would fail under the plan's own confinement, and the local status
label set that carries the root's disposition is named but not defined. Both are
amendable in the plan without widening scope. Six P2 amendments follow; none changes
the seam, the nine tools, or the no-fallback rule.

## Prior findings, disposition

| # | Prior finding | Status | Where in revision |
|---|---|---|---|
| 1 | No-DSN read is a contract change | Resolved | lines 27–36 |
| 2 | HTTP producer absent under `CORE_ONLY` | Resolved | lines 41–45, gate 2 |
| 3 | HTTP confinement vs CLI authority seam | Design resolved, E2E consequence not carried | lines 50–57; see P1-A |
| 4 | Row paths as host-file oracle | Resolved | lines 73–77 |
| 5 | Clean-empty vs unrecognized input | Resolved | lines 60–68 |
| 6 | Byte limits vs shared pool | Resolved in principle, arithmetic still off | lines 96–105; see P2-C |
| 7 | Cancellation signatures | Resolved | lines 115–119 |
| 8 | Freshness label vs provenance | Partially resolved | lines 77–85; see P1-B |
| 9 | Explicit-DSN runs invisible to reads | Resolved | lines 119–122 |
| 10 | Cross-process lock | Resolved per root disposition, one inconsistency | lines 107–113; see P2-B |
| 11 | Two roots, one sentence | Resolved | lines 87–90 |
| 12 | Replay warning text for local rows | Resolved | lines 69–71 |
| 13 | Isolation statement | Resolved | lines 57–59 |
| 14 | Minimal four-method interface | Resolved | lines 16–25 |

Every source citation in the revision was re-verified: `diagnostics.go:447`
(missing-DSN read error), `:494` (run ID shape), `main.go:2212` (400 mapping),
`:288` (`coreWorkspaceSubpaths`), `:489` (scope check), `core_only_test.go:33`,
`xmustard-ops/main.go:47-108`, `xmustard-mcp/main.go:151`, `tools.ts:151`,
`rustcore/diagnostics.go:101`, `safepath.go:44`, `safepath_unix.go:23`,
`diagnostics.rs:472`, `context_packet.go:2065`, `pi-adapter.e2e.ts:247`. All match.

## P1 findings

**P1-A. The PostgreSQL control fixture breaks under the plan's HTTP confinement.**
`setUpDiagnostics` writes its input to `join(T, "diagnostics-lsp.json")` and posts
that absolute path to `POST /diagnostics/run` (`pi-adapter.e2e.ts:79-88`), while
the workspace root is `join(T, "repo")` (`:37`). The file is outside the root. Under
line 52 ("HTTP selects workspace-only authority… reject absolute, traversal, and
symlink escapes") that call returns 400, so gate 6's "existing native-Postgres
control" (line 158) and the deferred-section promise that the fixture "remains a
control" (line 170) cannot both hold. Prior finding 3 named this exact hazard; the
revision adopted the seam but not the fixture change. Amend: the slice must move
the fixture input beneath the workspace root (for example
`repo/.xmustard-e2e/diagnostics-lsp.json`, posted as a relative path, and excluded
from the seeded repo's scan) or seed the control through the CLI's `local_operator`
authority. List `pi-adapter.e2e.ts:79-88` as a file the slice changes, next to
`:247`.

**P1-B. The local status label set is asserted but never defined.** The plan says a
valid local record "may be `available`, but must not be labelled `fresh`" (line 82)
and gate 4 asserts "`available` rather than `fresh`" (line 148), but never says
which field carries `available`, what the local value set is, or what local emits
when HEAD has moved since ingestion or the tree is dirty. Today `DiagnosticsStatus.Status`
is `blocked | no_baseline | fresh | stale | dirty_provisional` (`diagnostics.go:384-425`)
and the GET result has no status at all; line 31 adds `storage.status` with only
`no_baseline` shown. Line 80 also fixes `freshness_basis:"head_match"` for every
record, which contradicts the rule that the imported file's HEAD sample is not a
freshness basis. Amend with one enumerated contract:

- `DiagnosticsStatus.status` for the local backend: `no_baseline`, `available`
  (ingestion HEAD matches or is unknown), `stale` (HEAD moved since ingestion);
  a dirty tree adds a stale reason and keeps `available`, never `fresh` or
  `dirty_provisional`. PostgreSQL keeps its existing five values untouched.
- `storage.status` on GET mirrors the same local set.
- `freshness_basis`: `head_match` for PostgreSQL records, `ingestion_identity`
  for local records. `ingestion_identity.status` stays as written.
- Gate 4 asserts these values by name, including the HEAD-moved case.

## P2 findings

**P2-A. No-DSN runs are still blocked before the store is ever selected.**
`PlanDiagnostics` adds the blocker "Postgres DSN is not configured" (`diagnostics.go:230`),
which sets `CanRun=false`; `RunDiagnostics` then returns "blocked" without
persisting (`:322-330`). The run path also records the activity kind
`postgres.materialize.diagnostics` (`:335`) and the message "into Postgres schema"
(`:352`), and the plan surfaces "Configure Postgres or pass --dsn" (`:273`). Gate 2
(CLI import with no DSN) cannot pass until these change, and the plan does not
name them. Amend: list the blocker removal, a `storage_backend` field on
`DiagnosticsPlan`, and backend-neutral activity kind and message text as in-scope
edits; keep `PostgresConfigured=false` on the plan.

**P2-B. Lock and admission waits disagree.** Line 98 says "5 s lock wait"; line 107
says `flock` "retried for at most 2 s". Independently, `budget.Children` waits up to
10 s for a child slot before `ErrOverloaded` (`budget.go:312`), and the normalize
child has its own 30 s timeout (`diagnostics.go:246`). Amend: one lock wait value,
and a stated worst-case HTTP latency for a contended import. Also say how lock
contention becomes the promised 503: `respondError` maps only
`budget.ErrOverloaded` to 503 with `Retry-After` (`main.go:556`, `:432`), so the
store's busy error must wrap `budget.ErrOverloaded` or the handler needs a new
mapping.

**P2-C. The caps do not fit each other or the request scope.** A normalized row
carries a 64-hex fingerprint, two timestamps, workspace ID, path, message, and
source fields, so it serializes near 250 bytes at minimum; 50,000 rows cannot fit
an 8 MiB envelope (about 170 bytes per row), so the row cap is unreachable and the
envelope cap binds first near 25–30k rows. More important, normalize stdout for
50,000 rows is roughly 15 MiB and is reserved against the request scope until the
handler returns (`rustcore/root.go:40-58`, `main.go:383`), so one maximal import
exceeds the 16 MiB working target by itself, before the archive child's stdout
(which embeds the payload again) and the envelope encoder. Amend: rows ≤ 20,000 or
derived from the envelope cap; state that normalize and archive stdout count toward
the working target; and note that a CLI import and an API import run against
separate 64 MiB pools, so gate 6 must sample RSS with a CLI import concurrent to
API reads, not API-only.

**P2-D. Retention promise plus reject-when-full is a self-imposed outage.** Thirty-day
retention of accepted originals, 256 MiB or 100 baselines per workspace, reject
when full, and never evict an unexpired original (lines 99–104) means 64 maximal
imports fill a workspace and every further import is refused for up to 30 days
with no operator relief. The evidence store precedent is 24 h (`evidence/store.go:42`).
Amend: a shorter default promise (24 h–7 d) that configuration may lower, or an
explicit CLI prune that removes only run IDs the operator names, with the removal
recorded. Say whether the promise attaches to the original bytes or to the baseline.

**P2-E. "Exact raw bytes" must come from Go, not from the Rust archive.** The archive
command reads the input with `read_to_string` and re-emits `raw_payload` as a
parsed JSON value (`diagnostics.rs:125,194`); key order, whitespace, and number
formatting are not preserved, so it is not the original bytes. Go already holds the
captured buffer. Amend: the envelope stores Go's captured bytes; Go computes its own
sha256 and rejects the import if Rust's `raw_payload_sha256` differs. Non-UTF-8
input fails in Rust and is an ordinary import error.

**P2-F. Lock-free readers can race the pruner.** Readers take no lock (line 94), so
a reader that loads the latest pointer and then opens an envelope the pruner just
removed sees ENOENT. Amend: readers retry the pointer-then-envelope read once, then
return an error, never `no_baseline`; and the pointer update itself is
temp-write plus rename, not an in-place write.

## P3 notes

- Identity hashing through `readWorkspaceRegularFile` buffers up to 8 MiB per file
  outside the budget (`safepath.go:100-117`); hash by streaming from the no-follow
  fd, or charge the read to the scope.
- `GET /diagnostics/status` is not in the `CORE_ONLY` allowlist (`main.go:288-297`),
  so gate 1's status assertion runs in platform mode only; say so.
- `selectDiagnosticsStore` will keep honoring a request-body `dsn` over HTTP in
  platform mode (`DiagnosticsRequest.DSN`, `diagnostics.go:35`). That is existing
  behavior, not new exposure; note it as retained rather than silent.
- `connectSemanticPostgres` is a package variable (`lsp_definition_test.go:165`), so
  gate 1's "prove no DB dial" is feasible as a test stub.
- This recheck file is not in the `.gitignore` allowlist; adding it is the owner's
  call.

## Sound as written

- The four-method `DiagnosticBaselineStore` seam, PostgreSQL-first selection with
  hard errors, and the no-fallback rule match the code and the prior review.
- Nine tools, GET-only `CORE_ONLY` allowlist, and the MCP and Pi read paths are
  untouched (`main_test.go:15` still pins nine).
- Row-path confinement before any stat, bounded identity work, and the
  `path_outside_workspace` marker close the oracle from prior finding 4.
- Normalization accounting in Go over the captured bytes, with `[]` complete and
  `{}` or all-invalid rejected, closes prior finding 5 without a Rust contract change.
- Unix `flock` with fail-closed unsupported platforms and no `O_EXCL` stealing
  matches the root disposition; the `!unix` build tag split already exists
  (`safepath_other.go`).
- `auto_scan` remains excluded as a producer.

## Acceptance gates, feasibility

Gates 1, 3, 4, and 5 are feasible once P1-B fixes the label set and P2-A removes the
blocker. Gate 2 depends on P2-A. Gate 6 depends on P1-A for the PostgreSQL control
and on P2-C for a meaningful RSS sample. The 50–100 MB process-tree target stays
unproven, as the plan itself states.
