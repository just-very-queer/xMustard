# Stage 5 integration and acceptance review — Claude Fable 5.1

Reviewer: Claude Fable 5.1 (`claude-fable-5-1`), independent of every implementer and of the
specialist reviewers. Scope: plan Stage 5 and cross-module acceptance in
`docs/plans/2026-09-24-lean-context-implementation.md` (3bbd708bb8d3): the executable
acceptance scripts, the gold fixture, the backend gate targets, the final reports, and
source/hash correspondence between reports and the tree. Not redone here: the Rust review, the
Pi review, and the Go evidence review; their currency is checked by hash below. This file is the
only repository write. No source, script, doc or evidence file was edited. Nothing was run that
competes with root's RSS measurement: every conclusion comes from reading source and the
recorded evidence, plus `python3 -m py_compile` on the four Python scripts (all compile).

Snapshot reviewed at 16:54:54 (sha256 prefix): `scripts/e2e/harness.py` f325c949b3f0,
`mcp_evidence.py` cb95839ac659, `mcp-evidence.sh` aab9dd2b950f, `pi-adapter.sh` afdfbfbddec5,
`scripts/bench/rss_bench.py` 6411f391878b, `rss.sh` 7d8d9e545e50, `retrieval_gate.py`
b800e6ec3ef3, `retrieval-gate.sh` cd3d5bbe5816, `gold/queries.json` 134cf7e713b0, gold repo
(concatenated sorted files) eb31911faa79; `docs/benchmarks/2026-09-24-lean-context.md`
96329e420a42; reviews: evidence 479dcbdc64eb, pi de4d6c519f09, rust 6f66b093ab82, Go results
9038cac1341e, Pi results aaba528f2d7a. `rss_bench.py` changed twice while I read it
(69d0e787122d → 002801aedb40 → 6411f391878b); the diffs are exactly the three root-requested
fixes and are assessed below.

## Verdict

**APPROVED for the assigned scope (source review of the executable acceptance and its
reporting), at the hashes above.** The harness, the three launchers, the RSS bench, the gold
gate and the benchmark report assert the plan's required behaviour, refuse to reclassify
failures, and label every estimate and every unmeasured quantity. No mandatory check accepts
"error or success".

**Program acceptance: see "Final (17:10:25)" at the end.** The verdict below was written when
the resource gate was open; the rechecks record how each blocker was closed and the final
section gives the current state. Human review of the exact final diff remains required;
nothing here authorizes a merge.

## What was verified (by reading, cross-checked against Go source and recorded runs)

### `scripts/e2e/harness.py`, `mcp_evidence.py`, `mcp-evidence.sh`

- Real API, real stdio shim, real Rust core (only wrapped to make `__slow__` block), temporary
  repo, temporary data root, random loopback port, `XMUSTARD_AUTH=required`, minted tokens.
  `clean_env` strips inherited `XMUSTARD_*`. No provider call anywhere.
- Every plan-required case is a named check whose failure sets exit 1 (`finish`): tools +
  resources capability, exactly nine tools, bounded plain recall (`returned <= 8`,
  `total_active == 10`) delivered with a handle, exact paging by SHA-256 with every page
  `<= 65536`, self-sufficient URI (`workspace_id=` in it), issuer 200 / other principal 403 /
  anonymous 401 / cross-workspace 404 as one tuple equality, six concurrent calls correlated by
  id, cancellation that must kill the observed child pid within 10 s, mutation → `stale` with
  `captured_key != current_key`, API + shim restart re-paging the same SHA-256 from the exact
  issued URI, expiry denied with `-32002` and `reason == "expired"` after a successful first
  read, unaffected longer-retention original, tool error stays `isError`, unknown resource
  `-32002`. Any harness exception becomes a failing check, never a silent exit.
- Contracts it relies on exist in Go: `XMUSTARD_EVIDENCE_RETENTION_SECONDS` (`store.go:90`),
  `XMUSTARD_CORE_ONLY` (`api/main.go:154`), `XMUSTARD_API_TOKEN` (`mcp/main.go:242`),
  `mint-token` (`api/main.go:28`), `shutdown: complete` (`api/main.go:118`), shim mapping
  404/410/403/401 → `-32002`, 416/400 → `-32602`, 503 → `-32000` (`mcp/evidence.go:225-232`).
- Recorded result `docs/reviews/evidence/2026-09-24/mcp-evidence.json` 0d4699a5c2c5: 23 checks,
  0 failed, matching the script's 23 recorded checks (24 calls including the exception guard).

### `scripts/bench/rss_bench.py`, `rss.sh`

- Sampler: one `ps -axo pid=,ppid=,rss=,comm=` every 100 ms; the tree is the transitive
  `ppid` closure of the API and both shims inside one snapshot; the gate is invalid on zero
  samples, any `ps` error, any unparsable line, any snapshot missing a required root, an
  incomplete step list, or (after the third fix) any failed mandatory check. Per-process maxima
  are reported by command and explicitly "never summed". OS high-water comes from the API's own
  `getrusage` log (`rusage_unix.go`), labelled bytes on darwin / KiB on Linux, and described as
  the largest single waited-for child, not a concurrent peak. `unmeasured` lists bandwidth,
  Rust/shim allocations, per-stage copies and per-step CPU.
- Fixed workload: 501 tracked files, 19–21 MiB, 70-symbol `many.rs`, tree digest recorded.
  The generated Rust bodies lack `;` (GLR error recovery). This is disclosed in the script
  docstring, in every report's `fixture.syntax_limitation`, and in the benchmark report; the
  baseline is kept byte-identical, so runs stay comparable. Acceptable as required.
- Five concurrent captures are exactly `16 * 1024 * 1024` bytes each (sliced, not trimmed) with
  `Expect: 100-continue`. Admission is decided on headers because the API middleware reserves
  `r.ContentLength` before any handler read (`api/main.go:409-413`) and writes
  `503 + Retry-After: 1 + {"overloaded":true}` on refusal (`writeOverloaded`, line 432). The
  `bodyInFlight` slot limit defaults to 12 (`inFlightBodyLimit`), so five bodies cannot deadlock
  on it. A refusal counts only as a parsed wire 503 with `overloaded: true`, `Retry-After`
  present and `body_bytes_sent == 0`; a socket error is `transport_error` and fails the run.
  Pool `in_use <= max` is read while all admitted bodies are held.
- Expansion: every captured original (HTTP and stdio MCP alternately) plus one re-expansion is
  paged at 64 KiB under the running sampler with exact size and SHA-256 required. After the
  bound fix, an offset mismatch, oversized page, empty non-EOF page, non-contiguous
  `next_offset`, wrong `raw_sha256` or more than `ceil(size/64Ki)+1` pages fails immediately.
  The page JSON carries `offset`, `eof`, `next_offset`, `raw_sha256` (`store.go:189-198`) and
  the shim forwards the whole page map as `_meta["xmustard/page"]` (`mcp/evidence.go:249`), so
  the stricter loop is compatible with the served contract.
- Bytes labels after the fix: HTTP `delivered_measure = "http_response_body_bytes (exact)"`;
  MCP `"mcp_reply_reserialized_bytes (estimate, not wire bytes)"`; totals are grouped by
  measure and never mixed. `raw_bytes_recovered_total` is the exact recovered byte count.
- Retrieval-work checks are exact: cold `miss` with `files_parsed == 501`, warm `hit` with
  `files_parsed == 0`, same-size edit `files_parsed == 1` and `files_reused == 500`,
  rename/delete `files_parsed <= 1` with fewer indexed symbols than cold.
- Provenance per run: HEAD, tracked-source diff digest, untracked-source digest, SHA-256 of all
  three binaries and of both scripts, environment. This is what makes the staleness findings
  below decidable.

### `scripts/bench/retrieval_gate.py`, `retrieval-gate.sh`, `gold/`

- 12 queries: 10 `search` (4 Go, 3 Rust, 3 TS), one failure-only evidence case, one
  contradiction. Every gold span was checked against the fixture files: each starts at the
  doc comment or signature and ends at the closing brace (spans verified for all 12).
- Gate semantics match the plan: `>= 11/12` in top 5 on cold, warm and after-edit passes;
  explicit coverage loss with the expected reason for the 9 MiB oversized file and the
  invalid-UTF-8 file; `coverage.complete is False` with losses; warm `hit` with 0 parsed;
  after-edit `files_parsed == 1`; recall unchanged or improved; no stale hit and the served
  `source_identity.key` equal to a fresh `repo-key` after the probe file is removed. The
  failure-only case requires the FAIL line and gold path in the projection, a reduced size, and
  exact page-back of the original. The contradiction case requires both memories returned and
  `conflicts` non-empty.
- Honesty limit (already disclosed in the Go results doc): 14 fixture files and 24 symbols;
  10 of 12 queries contain the target identifier. It is a lexical-leaning internal regression
  gate. It must not be cited as parity or task-success evidence, and no report does so.
- Recorded result `retrieval-gate.json` 95aaf1c2b8b7: 12/12 checks, cold/warm/after-edit
  12/12 with spans 10/10, cold miss (15 files parsed), warm hit (0), after edit 1 parsed /
  14 reused.

### `scripts/e2e/pi-adapter.sh`

- Requires node/npm/go/cargo/git and Node >= 22.18; pins `@earendil-works/pi-coding-agent`
  0.87.1 and fails on any other installed version; builds Go from the checkout; either builds
  the core into `integrations/pi/.cache` or verifies a prebuilt `XMUSTARD_CORE_BIN` answers
  `repo-key`; native Postgres is required unless `XM_E2E_ALLOW_NO_POSTGRES=1` is set
  explicitly, in which case diagnostics success is loudly not covered. Temp dir kept on failure.
- Root's run at `/var/folders/.../xm-pi-e2e.gnVCwq/e2e/summary.json`: all nine tools
  `isError: false` with a native Postgres fixture, expansion 4 pages exact, stale keys differ,
  8 concurrent calls → 8 distinct handles, restart page SHA ok, expiry 410 then 404, abort
  acknowledged, timeouts explicit, unreachable API explicit, `xmustard_expand` absent before a
  handle, auth 401/401/403. The Pi review (de4d6c519f09) is APPROVED and its listed source is
  the current `integrations/pi` tree.

### Backend gate targets and specialist currency

- `make check-backend` = `go test ./...`, `go build ./...`, `cargo test`, `cargo clippy`
  (Makefile 62-66). Root reports all functional gates pass on final source; I did not rerun
  them during root's measurement. Pre-existing Clippy warnings are disclosed in STATUS, with no
  warning-clean claim.
- Rust review 6f66b093ab82: its final recipe hash (`git diff -- rust-core` + `tests/*.rs`)
  recomputes to `0ddf5562a0634e6f…` on this tree, identical to the reviewed hash. The prebuilt
  core used by every corrected RSS run (a23473a6be0e, built 16:25:28) postdates the last Rust
  source write (16:24:17). Rust review is current.
- Evidence review 479dcbdc64eb: its snapshot block now lists `reduce.go` 2a0bf1d3ba1d and
  `review_test.go` 4dc925ff9a0c, which match the tree, together with the unchanged
  `salience.go`, `store.go`, routes, both `main.go`, `budget.go`, `root.go`, `children.go`.
  The earlier APPROVED (b47b4f02c5fe) was hash-bound to `reduce.go` f323358b98cb and is
  superseded. The new regression `TestLateFailureOnLongLineIsEmitted` requires `Reduced == true`
  and the FATAL text past byte 4096 in the projection; it cannot pass on the old prefix
  behaviour. `TestWriteJSONRawMessageMatchesEncoder` (write_json_test.go e06e4b7b3f2c) and
  `TestDocumentSymbolsOmitExtractorCompletenessFields` exist; their correctness is the evidence
  reviewer's refreshed scope, not re-derived here.
- Go results doc 9038cac1341e: all cited source prefixes (evidence, budget, rustcore, mcp, api,
  five workspaceops files) match the tree.

### Test strictness sweep (api-go tests)

Every `err == nil ||` pattern found is "must fail with this exact code" (e.g. `-32000`,
`-32602`, `-32601`), which is the strict direction. Only two `t.Skip` calls exist, both for
non-POSIX shells in `managed_command_test.go`. One weaker assertion is listed as LOW below.

## Findings

### F1. HIGH (program) — the 100 MB sampled-tree gate is open (4 valid runs: 2 pass, 2 fail)

Corrected runs recorded in `docs/benchmarks/evidence/2026-09-24/` (hashes match the report):

| Run | API bin | Script | Peak | Gate |
| --- | --- | --- | --- | --- |
| run1 e0d3e83c | 7cdb766edd61 | 065f7cbeb134 | 99,401,728 B (99.4 MB) | valid, pass by 0.6 % |
| run2 b31e7a41 | 7cdb766edd61 | 69d0e787122d | 95,862,784 B | valid, pass |
| run3 6680df8b | 88e88bacc3f0 | 69d0e787122d | 110,526,464 B | valid, **fail** |
| root final1 c67bcc936eae (`/private/tmp/xmustard-root-checks.wEhHWd/rss-final1.json`, outside the tree) | 88e88bacc3f0 | **6411f391878b (final)** | 106,430,464 B | valid, **fail** |

All four passed every workload, admission (`captured ×4`, `wire_503_overloaded ×1` on the
wire, pool `in_use == max` while held, `in_use == 0` after, child peak 2 of 4) and expansion
check (5 expansions, 1,280 pages, 0 retries, 83,886,080 raw bytes recovered exactly). Root's
run is the only one on the final script and the final Go binary; it fails at 106.4 MB in "two
concurrent index clients" with, in one snapshot, `xmustard-core` 40,752 + 20,512 KiB, API
22,672 KiB, shims 10,480 + 9,520 KiB. The Go source changed between run2 and run3 (different
API hash and untracked digest), so runs 1–2 and 3/root are not same-source repeats. On the
final Go binary the gate has failed twice out of two. The benchmark report says "OPEN, not met
reliably" and STATUS says "Full gate not accepted"; both are correct. Root reports the Rust
owner is resuming residency work on the concurrent-builder case and Go is frozen; any pass
must come from repeated same-source runs on the final script after that change, with the
Rust review refreshed for the new hash.

### F2. MEDIUM (program) — every evidence JSON cited by hash is gitignored

`.gitignore` lines 34 and 50 ignore `docs/reviews/*` and `docs/benchmarks/*` except the
whitelisted `.md` files. `git check-ignore` confirms `docs/reviews/evidence/2026-09-24/*.json`
and `docs/benchmarks/evidence/2026-09-24/*.json` are ignored. The reports cite these files by
SHA-256 as the reproducible results. Either whitelist the two `evidence/` directories or state
in each report that the raw results are local-only. A committed report pointing at
uncommitted evidence is not a reproducible result.

### F3. MEDIUM — the MCP e2e and gold-gate JSONs record no provenance

`mcp-evidence.json` has only `suite/passed/failed/checks`; `retrieval-gate.json` adds
`results` but no HEAD, binary or script hash. Unlike the RSS reports they cannot be bound to a
source snapshot, so "MCP 23/23 and gold 12/12 on final source" rests on root's statement, not on
the files. Recommend the harness `finish()` gain the same `provenance()` block `rss_bench.py`
already has (shared-harness owner; not fixed here).

### F4. LOW — `TestFiveConcurrent16MiBCapturesAgainst64MiBPool` tolerates unexpected codes

`evidence_route_test.go:288-298` counts 503 and 200 and requires at least one of each; any
other status (500, 0) is silently ignored. It also does not assert that refusal happened
before the body was read; only the RSS bench does (via `Expect: 100-continue`). Tightening to
"every code is 200 or 503" would match the bench.

### F5. LOW — cancellation check does not assert the reply shape

`mcp_evidence.py:141` requires a reply within 10 s and a dead child, and only records
`isError` in the detail. The plan's "cancellation answers the call" is met; whether the answer
must be an error is unasserted.

### F6. LOW — Pi/external numbers differ between documents

The benchmark report quotes root's Pi run as 71 / 248.7 / 365.5 MiB; the Pi results doc quotes
70.5 / 250.7 / 334.0 and an earlier root run of 65.8 / 250 / 310.4. These are different runs
of a different (conformance) workload and are correctly labelled separate from the gate, but the
benchmark report should name which summary file it quotes.

### F7. INFO — untracked deliverables

`scripts/**`, `integrations/pi/**`, `rust-core/tests/**`, the new Go files, `docs/plans`,
`docs/reviews`, `docs/benchmarks` and the research docs are all untracked (41 untracked paths;
58 modified files, +5,580/−2,075). "Committed 12-query fixture" is therefore still a human
commit decision, as the plan requires.

## Remaining program blockers (outside my approval)

1. F1: resource gate, failed on the final source (106.4 MB). Needs the Rust residency change,
   a refreshed Rust review for the new hash, then repeated same-source runs on `rss_bench.py`
   6411f391878b with all originals expanded, every one at or below 100,000,000 bytes; otherwise
   STATUS keeps "Full gate not accepted".
2. F2: whitelist or relabel the evidence directories before commit.
3. F3: provenance in the two e2e JSONs, then rerun once on frozen source.
4. Human exact-diff review and merge decision; agent approvals, run-plan approval and passing
   tests do not authorize a merge.

## What this review does not claim

No RSS number, latency or allocation figure here was measured by me. No API-cost, competitor
parity or task-quality statement is made or endorsed. Byte admission is not an RSS ceiling.
The sampled peak is a sampled peak; the larger of sampled-tree and `getrusage` child
high-water is not the true tree peak.

## Recheck 1 (17:05:31) — dispositions of F1–F7 against the tree

Re-hashed snapshot: `harness.py` 973147225ffc, `mcp_evidence.py` 5154958ca2fd,
`retrieval_gate.py` 3c183600f32b, `rss_bench.py` 6411f391878b (unchanged), launchers and
`gold/queries.json` unchanged, gold repo eb31911faa79 (unchanged), `.gitignore` 6b674407726c,
`evidence_route_test.go` 5e3eb5d82698, benchmark report 5a6d689dcb0b, STATUS e57a526f7674.
All four Python scripts still compile. The Go evidence sources are unchanged from the refreshed
evidence review snapshot (reduce 2a0bf1d3ba1d, salience b8573d5c83ba, store 11477dccb793,
routes 5d2a80532964, api main b15bb345ce16, mcp evidence ae531d19e24c, mcp main a4d0ee4601e9).

| Finding | Disposition | Verified |
| --- | --- | --- |
| F1 resource gate | still open | Benchmark report now opens with "FAILS on the final source" and lists root's run (`rss-root-final1.json` c67bcc936eae, script 6411f391 frozen, API 88e88bac, 106,430,464 B, 816 valid samples). STATUS keeps "Full gate not accepted". Correct. |
| F2 ignored evidence | fixed | Exact per-file allowlist for the 14 dated JSON artifacts in both `evidence/2026-09-24/` directories; `git check-ignore -v` resolves each to its negation line. Unknown files in those directories stay ignored. Every file on disk in both directories is either allowlisted or a previously listed artifact; every hash in the report's evidence tables matches the file on disk (run1–3, baseline, root-final1, the two root-final e2e JSONs, both Pi summary copies). |
| F3 provenance | fixed in scripts (exercised later: see Recheck 2 and post-import) | `harness.provenance()` uses the same keys and recipe as `rss_bench.py`; `mcp_evidence.py` writes it through `finish_with` with its three script hashes; `retrieval_gate.py` records it plus `results.fixture.git_tree` (the fixture's own `HEAD^{tree}`, which covers the generated oversized and invalid-UTF-8 files). The two root-final e2e JSONs predate this and carry no provenance; the benchmark report discloses that explicitly. A rerun of both e2e scripts on frozen source will be the first provenance-bound result. |
| F4 tolerant test | fixed | `evidence_route_test.go:289-297`: any status other than 200 or 503 is now `t.Fatalf`. Refusal-before-body remains bench-only, as disclosed. |
| F5 cancellation reply | fixed | `mcp_evidence.py:146-149`: the cancelled call must answer with a JSON-RPC error or `isError: true`, and the child must be dead within 10 s. |
| F6 Pi numbers | fixed | Report cites both root Pi summaries by path and full SHA-256 and keeps byte-identical copies; both copies match their cited hashes. |
| F7 untracked deliverables | unchanged | Still a human commit decision. |

**Rust review is now stale.** `rust-core/src/search.rs` (16:59:32) and `symbolgraph.rs`
(16:56:52) changed after the Rust review's reviewed state; the review recipe now hashes to
`d039d76da668…`, not the reviewed `0ddf5562a0634e6f…`, and the release core is `d644743ba005`
(17:00:10) rather than the `a23473a6be0e` used by every recorded RSS run. Root reports the Rust
re-review is underway. Until it lands, no RSS result on the new core is reviewer-backed, and the
recorded RSS runs describe the previous core.

**Approval status after this recheck. (superseded by the post-import section below).** The assigned-scope approval (executable acceptance
scripts, gold gate, backend gate targets, reports and their hash correspondence) is refreshed
at the hashes above. Program acceptance stays NOT approved: the resource gate has failed on
final Go source twice, the Rust residency change is unreviewed, and the first provenance-bound
e2e results and repeated same-source full-RSS runs on the frozen script are still pending.
No merge is authorized by this document.

## Recheck 2 (17:06:32) — provenance-bound functional gates on the frozen revision

Root's stable checks were verified against this worktree without running anything heavy
(root's post-optimization full RSS run was sampling at the time).

- **Source identity is identical.** This tree's `git diff HEAD` over `api-go`, `rust-core/src`
  and the Cargo files hashes to `e0e282e6403c…`, and its untracked-source digest is 33 files
  `61245cae425f…`. Both values equal the `provenance` block in root's two JSONs, so the results
  below describe exactly this source.
- **MCP e2e, provenance-bound.** `/private/tmp/xmustard-root-checks.wEhHWd/mcp-provenance-final.json`
  (69abd9801c7e): 23/23, 0 failed; scripts `mcp_evidence.py` 5154958ca2fd,
  `mcp-evidence.sh` aab9dd2b950f, `harness.py` 973147225ffc (all equal to this tree);
  binaries API 88e88bac, MCP 3d06d73f, core d644743b.
- **Gold gate, provenance-bound.** `gold-provenance-final.json` (f36d9af7e654): 12/12, 0
  failed; scripts `retrieval_gate.py` 3c183600f32b, `queries.json` 134cf7e713b0, `harness.py`
  973147225ffc; same binaries; fixture `git_tree` 44a25485…, 16 tracked files (14 committed
  plus the two generated coverage-loss files). F3 is now closed by evidence, not only by code.
- **Rust review is current again.** `docs/reviews/2026-09-24-fable-rust-review.md`
  (7d385fdc6679) carries "Recheck 2" with recipe hash
  `d039d76da66876af79a9df12e6dc23ef9941a0a846ba3e64a720dad7ae77a358`; the same recipe on this
  tree recomputes to that value. The release core here is d644743ba005, the binary root used.
  Last Rust source write 16:59:32; nothing changed since.
- **Go remains frozen** at the evidence review's snapshot hashes (unchanged since Recheck 1).
- **Backend gate** (`make check-backend`): root reports exit 0 with all seven Go packages, 148
  Rust tests, and Clippy clean apart from the 11 pre-existing warnings. Not rerun here.
- **Reports.** Benchmark report now 5cf01409b03e; its status line still reads "FAILS on the
  final source" and it states that the coming post-optimization MCP, gold and full RSS runs on
  core d644743b will supersede the provenance-less root-final JSONs. STATUS unchanged
  (e57a526f7674, "Full gate not accepted").

**Status after Recheck 2. (superseded by the post-import section below).** Assigned scope stays APPROVED, now with every functional gate
(MCP 23/23, gold 12/12) bound by provenance to this exact source and to reviewed Rust and Go
snapshots. Program acceptance stays NOT approved for one reason only: the resource gate.
Every recorded full-RSS result is on the previous core (a23473a6) and the last two failed;
root's post-optimization run on core d644743b has not reported. It must be at or below
100,000,000 bytes with a valid sampler and all originals expanded, and repeated on the same
source, before the gate can be called met. Human exact-diff review and merge decision remain
required regardless.

## Final (17:10:25) — resource gate on the frozen post-optimization revision

Two full RSS runs on the same frozen source now pass. Both were verified from the curated files
in `docs/benchmarks/evidence/2026-09-24/` (each allowlisted by exact name in `.gitignore`
lines 76–77; root's curated copies end with a trailing LF, as do all root-curated JSONs).

| Run | File (sha256 prefix) | Peak | Checks | Samples / ps errors / lost roots | Peak step |
| --- | --- | --- | --- | --- | --- |
| post-opt 1 | `rss-postopt1.json` 07dfd35d6751 | 72,302,592 B (72.3 MB) | 19/19, gate valid | 838 / 0 / 0 | two concurrent index clients |
| post-opt 2 | `rss-postopt2.json` 26206f9ecbaf | 84,852,736 B (84.9 MB) | 19/19, gate valid | 840 / 0 / 0 | two concurrent index clients |

Shared by both runs, and equal to this worktree at 17:10:25:

- Source: HEAD `cd13e2b`, tracked diff digest `e0e282e6403c…`, untracked-source digest 33 files
  `61245cae425f…`. Rust review recipe `d039d76da668…` (the APPROVED Recheck 2 hash in
  `2026-09-24-fable-rust-review.md` 7d385fdc6679). Go at the evidence review's snapshot.
- Binaries: API 88e88bac, MCP 3d06d73f, core d644743b (built by the harness itself,
  `core_bin_prebuilt: false`; identical to `rust-core/target/release/xmustard-core` here).
- Scripts: `rss_bench.py` 6411f391878b, `harness.py` 973147225ffc. Fixture: 501 tracked files,
  21,179,988 bytes, tree c16734b0a9e1 (the unchanged baseline with the disclosed missing-`;`
  Rust bodies).
- Workload: admission outcomes `captured ×4` + one `wire_503_overloaded` with `Retry-After` and
  zero body bytes; pool `in_use == max == 67,108,864` while the four reservations were held and
  0 afterward; child admission peak 2 of 4; four originals plus one re-expansion, 1,280 pages,
  0 overload retries, 83,886,080 raw bytes recovered with exact size and SHA-256 under the
  running sampler.
- At the run-2 peak, in one `ps` snapshot: API 27,152 KiB, cores 22,432 + 13,072 KiB, shims
  10,608 + 9,600 KiB. OS high-water (getrusage, bytes): API self 28.1–29.2 MB, largest single
  child 32.9–33.7 MB, reported separately and never summed with the sampled tree.

History that stays on the record: on the previous core a23473a6 the corrected harness gave
99.4 and 95.9 MB (pass, older API binary) and 110.5 and 106.4 MB (fail, final API binary).
The Rust residency optimization between core a23473a6 and d644743b is what moved the
two-client peak from 106.4 MB to 72.3–84.9 MB; that change is covered by the Rust review's
Recheck 2.

**What this proves and what it does not.** The plan's fixed Stage 5 workload passes the
100,000,000-byte sampled-tree gate on two same-source runs of the frozen script, with the
sampler valid and every mandatory workload check green. It is a sampled peak at 100 ms; short
spikes can be missed; it is not a universal RSS ceiling, not an OS high-water mark, and not
evidence of competitor parity, task-quality parity or API cost. The Pi workflow numbers remain
separate external measurements of a different workload.

**Final status (pre-import, superseded).** See the post-import section below for the current
state; the source, script and fixture hashes above are unchanged.

## Post-import (17:19:07) — main-tree proofs on the imported source

Root imported the reviewed source into the main checkout and reran every Stage 5 gate there.
The curated results are present in this worktree's `docs/benchmarks/evidence/2026-09-24/`,
each allowlisted by exact name (`.gitignore` lines 78–81), each ending in a trailing LF, and
each verified below from the file, not from the message.

| Proof | File (sha256 prefix) | Result |
| --- | --- | --- |
| Backend (`make check-backend`, uncached Go) | root report | exit 0: 7 Go packages, 148 Rust tests, Clippy with the 11 pre-existing warnings only |
| MCP e2e | `main-mcp.json` e7cc5499469a | 23/23, 0 failed |
| Gold gate | `main-gold.json` 7ce0092a93d4 | 12/12, 0 failed; fixture `git_tree` 44a25485…, 16 files |
| Full RSS | `main-rss.json` d8ba4d207229 | 19/19, gate valid and passed: 80,592,896 B (80.6 MB), 853 samples, 0 ps errors, 0 lost roots, median interval 106.2 ms, max 110.5 ms |
| Pi e2e | `pi-e2e-summary-main.json` be1d295e890f | native Postgres fixture; all nine tools `isError: false`; expansion, stale keys, restart, expiry, abort, timeouts, unreachable API, pass-through and auth cases recorded; 222 samples, 1 skipped tick, 0 sampling failures |

Source binding of the three provenance-carrying JSONs: HEAD `cd13e2b`, tracked diff digest
`e0e282e6403c…`, untracked-source digest 33 files `61245cae425f…`, identical to this worktree
at 17:19:07 and to the reviewed Rust recipe `d039d76da668…`. Core d644743b is byte-identical
to the candidate core. The Go binaries differ from the candidate's (API d2f0ec93 and MCP
6ae2f34b in main versus 88e88bac and 3d06d73f here) because Go embeds the build path; the Go
source digests are identical, so this is a build-location difference, not a source difference.
Scripts are frozen: `rss_bench.py` 6411f391878b, `harness.py` 973147225ffc,
`mcp_evidence.py` 5154958ca2fd, `retrieval_gate.py` 3c183600f32b, `queries.json`
134cf7e713b0; fixture tree c16734b0a9e1 (501 files, 21,179,988 bytes, disclosed missing-`;`
Rust bodies).

Main RSS workload detail: outcomes `captured ×4` + one `wire_503_overloaded`; pool
`in_use == max == 67,108,864` while held, 0 after; child admission peak 2 of 4; four originals
plus one re-expansion, 1,280 pages, 0 overload retries, 83,886,080 bytes recovered exactly.
At the peak, in one snapshot: API 23,168 KiB, cores 22,512 + 13,056 KiB, shims 10,384 + 9,584
KiB.

Pi qualification, resolved: the earlier Pi runs (gnVCwq, WI7Omh) were on core a23473a6. The
main run (e48Nfj) is on core d644743b with the main API and MCP binaries, per root, and its
summary shows all nine tools succeeding on that source. The Pi summary format does not record
binary hashes itself, so that binding rests on root's run record rather than on the file; the
adapter, launcher and Go API are unchanged since the Pi review. External numbers from that run
(sampled MiB, separate from the gate): xMustard-owned 68.8, Pi 263.6, Postgres fixture 41.3,
full workflow 366.9.

Resource-gate record on the reviewed source, all on core d644743b and script 6411f391878b:
72.3 MB (post-opt 1), 84.9 MB (post-opt 2), 80.6 MB (main). Three valid passes out of three on
this source. The earlier 110.5 and 106.4 MB failures were on core a23473a6 and remain on the
record as history. This is a sampled 100 ms tree peak on the fixed workload, not a universal
RSS ceiling, not an OS high-water mark, and not competitor, task-quality or cost evidence.

**Final status.** Assigned scope: APPROVED. Every plan Stage 5 gate holds on the imported
source with reviewed Rust (d039d76da668…) and reviewed Go (evidence review snapshot). Code is
frozen; the next diagnostics plan is separate work. Remaining: Luna's public-doc updates and
root's import of the docs, then human review of the exact final diff. No commit, push or merge
is authorized by this document.
