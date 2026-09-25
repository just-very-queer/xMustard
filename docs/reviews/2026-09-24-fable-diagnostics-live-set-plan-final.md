# Plan final audit: diagnostics live-set redesign — 2026-09-24

Reviewer: Claude Fable 5.1, independent final audit of the provisional plan
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md`. Verified sha256
`869a227e25090d945350ec4834d2cfa4c3e1becbb91136932244ada59ae668da` (matches the requested
value; 197 lines). Read in the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base
`cd13e2b`), against my first review and recheck, the Go read path, the budget package,
the HTTP server and MCP shim, the ops CLI, the Pi adapter, the fixed benchmark and its
harness, the series-1 and profile evidence, and Go 1.26.1's `encoding/json` and `net/http`.
This file is the only write. No source, benchmark, plan, main-checkout, commit, push,
merge or gate run.

## Verdict: CHANGES_REQUIRED

Narrow. N1–N4 from the recheck are resolved as written, and the non-blocking N5–N8 are
absorbed. The design is coherent: the raw-before-rows layout, publish-time stable sort,
full first-pass validation, same-descriptor sequential second pass with EOF digest check,
Compact + HTMLEscape wire parity, the permanent bounded path for legacy files, the
non-streaming PostgreSQL wrapper, the two-read semaphore and the staged gates fit
together and fit the code. Four things still stop it from being a safe prototype
specification: one admission-accounting decision the budget package cannot express as
written (F1), one gate-3 decision rule that the known bimodality can flip (F2), and two
factual sentences an implementer would act on wrongly (F3, F4). Each is a small plan edit;
none reopens the candidate choice.

This is a readiness judgment on the plan text. It is not implementation approval: the
owner has not chosen import-first, redesign-first or defer; no gate has run; the
50–100 MB process-tree target remains unproven.

## Status of prior findings

| Item | Status | Verified against |
|---|---|---|
| N1 full pre-response validation in pass one | resolved (plan 60–69) | `readEnvelope` `diagnostics_local_store.go:454–512`: regular file + cap `:467`, pointer SHA `:487`, self-checksum `:491`, well-formed JSON `:495`, Base64/schema/run/workspace/archive/count `:498–503`, raw SHA + length `:507`. Plan lists all of them. |
| N2 wire raw payload is compacted + HTML-escaped; PostgreSQL re-encodes | resolved (plan 78–83) | Go 1.26.1 `encoding/json/encode.go:473–488` routes every `Marshaler` result through `appendCompact(out, b, opts.escapeHTML)`; `indent.go:44–50,64–71` shows `Compact` + `HTMLEscape` produce the same bytes for valid JSON, including U+2028/2029. PostgreSQL decodes `raw_payload_json` into `any` (`diagnostics.go:1381–1385`); field is `omitempty` (`:144`). |
| N2 PostgreSQL row streaming | resolved by not streaming (plan 94–96) | `readDiagnosticRows` is one `jsonb_agg` scan (`diagnostics.go:1087`); the wrapper keeps it, the `link_status` default (`:1102`) and `diagnosticReplayWarnings` (`:1210`). No SQL change claimed. |
| N3 reader cannot tell new from legacy | resolved (plan 51–58) | Field order is the marker: today `rows` precedes `raw_payload_base64` (`:74–75`); the checksum prefix is fixed by `localDiagnosticsEnvelopeSHAPrefix` (`:68`) and `sealLocalDiagnosticsEnvelope` (`:261–269`), which a struct reorder preserves. `json.Unmarshal` into the struct is order-independent, so the legacy path still reads new files. Publish sort is `sort.SliceStable` on the key `Rows` uses today (`:586–590`). No test pins field order beyond the prefix. |
| N4 undefined 95 % reference | resolved as a statistic (plan 172–183), decision rule still open | series 1 `api_reads` 3082/1753/1559/1617/3152 (`series1-baseline/SUMMARY.txt:4–20`); profile runs 1740/1776/3189 and 3205/1572/1646 (`profile-A/*.report.json`, `profile-B/*.report.json`). See F2. |
| N5 abort after first byte | resolved (plan 86–90) | `net/http/server.go:1904–1919`: an `ErrAbortHandler` panic skips the log, cancels the response and closes the connection, so the terminating chunk `chunkWriter.close` (`:408–422`) is never written. No `TimeoutHandler`, gzip or recovering middleware sits in front (`cmd/xmustard-api/main.go:56,62–63`; no `recover()` in the file). `bodyLimitMiddleware` defers `scope.Close()` (`main.go:381–382`), so the ledger is released on panic. |
| N6 cap ≥ 2, two reservations must fit | resolved (plan 116–128) | Benchmark runs two reader threads (`scripts/bench/rss_bench.py:598–604`) and requires every status 200 (`:623–625`). |
| N7 bounds for legacy path | resolved (plan 56–58) | 2,500 rows / 4 MiB (`diagnostics_input.go:34–35`). |
| N8 retry wraps pass one | resolved (plan 91–92) | `Latest` retries the pointer-plus-envelope pair once (`:534–546`). |
| N9 git-ignored | still true | `.gitignore:34,88` ignore both `docs/reviews/*` and `docs/plans/*`; `git check-ignore` confirms the plan and reviews are not tracked. |

## Required changes

### F1. The admission accounting is not expressible with `Scope.Limited`, and its bound is not derived

Plan 120–130 asks for a per-read 16 MiB `budget.Scope.Limited` child, charges "as they
become live, releasing charges when those buffers are released", and in the same
paragraph says "do not relax admission because accounting decreases".

- `Scope` has no partial release. `Limited` documents "Closing the child releases
  nothing" and the child's bytes stay reserved in the parent until the parent closes
  (`budget.go:105–113`); `Acquire` only adds (`:115–140`); the only `Release` is on the
  pool (`:66`). "Releasing charges" therefore requires a new budget primitive the plan
  does not name, and whatever it releases to (child cap, parent, pool) is exactly the
  relaxation the next sentence forbids.
- The choice decides whether a legal baseline can be read at all. With monotonic
  charging, a maximal envelope charges: two token passes over up to 4 MiB of input if
  streamed bytes are charged (8 MiB), the decoded original (1 MiB), its compacted copy
  (≤ 1 MiB), its HTML-escaped copy (up to 6 MiB, since `<`, `>`, `&` expand 1→6 bytes and
  a 1 MiB JSON string of them is valid input, `diagnostics_input.go:33,242–300` validates
  shape only), and up to 4 MiB of row buffers: about 20 MiB > 16 MiB. That would turn a
  baseline today's path serves with 200 into `ErrAdmissionLimit`, which
  `respondDiagnosticsError` maps to **413**, not 503 (`main.go:583–586`). With live-set
  charging the peak is about 8 MiB and fits. The benchmark envelope (1.97 MB, no HTML)
  fits either way, so the fixed gate cannot catch this.

Required: choose one. (a) Monotonic: the child charges each buffer's high-water size
once and never releases; then say the counted stream bytes are *not* charged, list the
charged buffers, and show the worst-case sum (including the 6× escape expansion) is
under 16 MiB, or raise the per-read cap and re-derive "two reads ≤ 32 MiB of 64 MiB".
(b) Live-set: name the new `Scope` method, say released bytes return only to the child's
own cap while the parent keeps the high-water reservation until response completion,
and keep the semaphore as the only concurrency control. Either way state that a per-read
cap breach is a 413 (deterministic for the file) and that no envelope within the fixed
input caps may reach it.

### F2. Gate 3 needs a decision rule that the bimodality cannot flip

Plan 174–176 preregisters "median B `api_reads` over five interleaved pairs ≥ 95 % of the
A median" and 176–178 admits the ~1.6k/~3.1k clusters are unexplained. With five draws
from a distribution that spans 2×, the A and B medians each land in a cluster
independently: A at {3082,1753,1559,1617,3152} gives 1753; a B series with three
high-cluster draws passes at ~180 %, and an A with three high draws against a B with two
fails at ~52 % with identical code. The recheck asked for the bimodality to be explained
or paired out *before* the criterion is used; the plan reports it instead.

Required: state which measurement decides gate 3 when the two disagree. The
preloaded-baseline two-reader check (plan 178–180) has no interleaved CLI import and is
the sound throughput comparison; make it decisive, and demote the fixed-step
`api_reads` median to a reported, non-deciding figure until the clusters are explained.
Add to gate 1: record per-reader completion counts and reader-thread liveness. The fixed
harness records a status only when `urlopen` returns (`scripts/e2e/harness.py:123–134`);
any transport exception in `diag_reader` (`rss_bench.py:598–600`) ends that thread and
leaves `read_codes` untouched, so one dead reader halves `api_reads` while every
recorded status stays 200. That is both a candidate explanation for the low cluster and
the only way an aborted candidate body would show up in gate 4, so it must be observed
outside the fixed harness rather than assumed.

### F3. The legacy bounded path is permanent, not a 24-hour shim

Plan 56–58 keeps the full-read path for legacy envelopes "until their 24-hour retention
expires". Pruning runs only inside an import under the lock and never removes the
pointer's target: `recoverAndCheckQuota` prunes `id != latestID && now.After(expires)`
(`diagnostics_local_store.go:364`). A workspace whose last import predates the change
keeps a rows-before-raw latest envelope for as long as no new import happens. Required:
say the legacy path has no removal date and stays part of the local adapter; the 24-hour
figure bounds only how long a superseded legacy file can still be read by ID.

### F4. The ops CLI reads in process, not over HTTP

Plan 46–47 says "MCP, Pi, and CLI continue consuming the same HTTP JSON contract". MCP
(`cmd/xmustard-mcp/main.go:150–151`) and Pi (`integrations/pi/src/tools.ts:147–151`) do.
`xmustard-ops diagnostics read` calls `ReadDiagnosticsCtx` directly
(`cmd/xmustard-ops/main.go:116`) and prints `json.Marshal(payload)` plus a newline
(`:504–508`); it never touches the handler or the new seam. The design survives because
the plan keeps the slice-returning `Rows` (plan 39–41), so the CLI keeps today's path.
Required: correct the sentence, and add the CLI's `json.Marshal` output to the parity
fixture, since it is the third writer of the same contract and today matches the
handler's `Encoder` byte for byte (same `escapeHTML`, same trailing newline).

## Coherence of the named elements

- **Raw-before-rows v2 layout.** Safe: only the struct field order changes; prefix,
  self-checksum, pointer checksum and retention are untouched; readers of either layout
  work through `json.Unmarshal`. `path_identities` stays last and is consumed to EOF in
  pass two. The benchmark fixture has one path and one severity, so its wire order equals
  import order under both old and new paths; the parity cases at plan 99–103 are what
  prove the general case.
- **Stable sort at publish.** Matches `Rows` exactly (`:586–590`); pass-one sortedness is a
  non-strict check, so equal keys in import order pass and stream in stored order.
- **First pass.** The list at plan 64–69 is the complete `:466–509` set. It records
  offsets (line 68) that the sequential pass two never uses (plan 72–77); harmless, but
  drop the words or the implementer will build a seek that the plan then forbids.
- **Second pass, same descriptor.** `openWorkspaceFileBeneath` returns a plain `*os.File`
  (`safepath_unix.go:23`), so `Seek(0, io.SeekStart)` on it is sound; writers only ever
  rename (`:387–427`), so bytes behind the descriptor are stable against xMustard's own
  writers and the EOF digest covers an outside writer. Local `warnings` derive from the
  baseline only (`diagnostics.go:739,753–763`), and `warnings`, `storage` and
  `generated_at` follow `diagnostics` in the struct (`:206–213`), so nothing emitted after
  the rows needs the rows.
- **Wire parity.** Compact + HTMLEscape reproduces `appendCompact(escape=true)` for valid
  JSON; the stored original is always one complete JSON value because import token-parses
  it to EOF (`diagnostics_input.go:246–300`), so `Compact` cannot fail after the first
  byte. Still add `json.Valid` on the decoded original to pass one so that guarantee is
  checked, not inherited.
- **PostgreSQL wrapper.** Correctly claims nothing about streaming or RSS; the field must
  stay omitted when the column is empty, and the canonical re-encoding must go through
  the same encoder path as today.
- **Inclusive admission cap.** Intent is right (no pre-acquisition, no relaxed
  concurrency); the accounting model is F1.
- **Preregistered A/B gate.** Order, provenance, five pairs, both clusters reported,
  per-read CPU and p50/p95 within 1.5×, dropped work rejected: sound, subject to F2.

## Non-blocking findings

1. **503 shape.** `writeOverloaded` sets `Retry-After: 1` and `"overloaded":true`
   (`main.go:432–435`); the MCP shim maps that to `ErrOverloaded` and any ≥ 400 to an
   error (`cmd/xmustard-mcp/main.go:206–211`); Pi raises on non-2xx (`http.ts:108–118`).
   The semaphore's 503 should reuse `writeOverloaded` so all three consumers see the same
   shape. A truncated body reaches the shim as a read error (`:259–268`), never as a list.
2. **Status route.** `ReadDiagnosticsStatusCtx` still returns the full baseline with the
   inline original (`diagnostics.go:631–646`) through `Latest`; the plan scopes it out
   (47–49), which is right, but its per-request live set is then unchanged and should be
   named as such in the gate-4 attribution.
3. **Per-request cache.** `readEnvelope` caches the whole envelope on the store instance
   (`:512`); the prepared reader must not call it, or rows come back resident. The plan
   implies this; say it.
4. **Git probe.** Each baseline-present GET runs `git status` (`diagnostics.go:722`,
   `context_packet.go:2071`), so wall time per read is mostly the child; the plan says so
   (111–113) and correctly keeps CPU-per-read as the throughput guard.
5. **Escape expansion in the response.** Independent of F1, the response-size figure
   "about a 1.85 MB response" holds only for inputs without HTML characters; state the
   6× worst case where response bytes are reported.
6. **Housekeeping.** Plan and all three reviews are git-ignored (`.gitignore:34,88`);
   allowlist them if they are to travel with the branch.

## Non-claims

I ran no benchmark, profile, test or build, and changed no source, plan or benchmark.
Line references are to the isolated tree at base `cd13e2b` as of this review. Nothing
here selects or authorizes implementation, import or a representation change; approving
the plan text after F1–F4 would still leave the owner's import-first/redesign-first/defer
choice open and the 50–100 MB target unproven until the fixed attested series passes.
