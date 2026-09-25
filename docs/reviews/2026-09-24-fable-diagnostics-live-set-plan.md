# Plan review: diagnostics live-set redesign — 2026-09-24

Reviewer: Claude Fable 5.1, independent. Subject: the provisional plan
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md` (sha256 `92c6ce88…5fb6`), read in
the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`). Checked against
`docs/VISION.md`, `docs/ARCHITECTURE.md`, the two sibling plans, the headroom
implementation report and my review of it, and the Go read path. This file is the only
write. No source, benchmark, plan, main-checkout, commit, push or merge; no gate run.

## Verdict: CHANGES_REQUIRED

The plan's scope discipline is right: no implementation, fixed gate unchanged, owner
chooses. It also states correctly what the read path does today (plan lines 10–14
match `diagnostics.go:691–750` and `diagnostics_local_store.go:454–514`). But both
candidates are described in a way the current contracts do not support, and one
admission claim is wrong. Five changes are required before this plan can guide a
prototype; the rest are non-blocking.

## Required changes

### R1. Candidate A cannot keep the store interface and stream rows

- `DiagnosticBaselineStore.Rows` returns `[]DiagnosticRecord` and `Latest`/`ByID` return
  `*DiagnosticRun` (`diagnostics.go:257–263`). A slice-returning `Rows` materializes
  every row by contract; nothing downstream can stream what it has already been handed.
- The stored order is import order: `buildLocalDiagnosticsEnvelope` appends rows as
  normalized (`diagnostics_local_store.go:191–213`, no sort), and `Rows` copies and
  stable-sorts by severity then path (`:585–590`). Plan line 35–36 ("parses the rows
  incrementally and emits the existing response shape") and line 40 ("preserve …
  ordering") contradict each other for existing v2 files: a sorted stream from an
  unsorted file needs all rows resident first.
- The raw payload is the last large field: envelope layout is schema, checksum, run,
  rows, `raw_payload_base64`, identities (`:70–77`). `Latest` must return
  `Run.ReplayArchive.RawPayload` filled from those bytes (`:498–511`), so even the
  first call must scan the whole file, and the decoded 1 MiB original stays live from
  `Latest` to the end of the response under the retained interface.
- The response is fully buffered regardless: `writeJSON` calls
  `json.NewEncoder(w).Encode` (`cmd/xmustard-api/main.go:652–653`); `Encoder.Encode`
  marshals the whole value into an encode state before one `Write`
  (`$GOROOT/src/encoding/json/stream.go:209–233`). The report you cite already names the
  encode phase as the live-set peak (row copy + original + ~2 MB encoder buffer,
  `…-memory-headroom-implementation.md:163–166`; encoder growth is 24 % of allocations,
  `:93–101`). Plan line 44–46 says a full result "could" restore the live set; it does,
  by contract, unless the handler's writer changes.

Required: state plainly that A with the interface retained can only shrink the decode
phase (the 1.97 MB file buffer and the Base64 string), not the encode-phase peak, and
choose one of: (a) accept that ceiling and say so in the stop/go criteria, or (b) make
the interface change explicit now — a row visitor/iterator or a write-to-encoder seam,
implemented for both adapters — plus a streaming response writer in the handler. Also
decide how ordering is produced without a full set: sorting at publish time (an additive
change to what the writer emits, with sort-on-read retained for older files) is the
smallest option; name it or reject it.

### R2. Candidate B's premise is a wire-contract change the plan says it avoids

- Ordinary reads return the exact original bytes inline today. Local reads set
  `ReplayArchive.RawPayload = json.RawMessage(original)` (`diagnostics_local_store.go:516–524`,
  called at `:510` and `:183`), the field is `raw_payload,omitempty` (`diagnostics.go:144`),
  and the PostgreSQL adapter decodes `raw_payload_json` into the same field
  (`diagnostics.go:1051`, `:1377–1425`). Every consumer of `GET /diagnostics` receives it:
  HTTP (`main.go:2211–2222`), the MCP `diagnostics` tool (`cmd/xmustard-mcp/main.go:150–151`),
  Pi (`integrations/pi/src/tools.ts:147–151`) and `xmustard-ops diagnostics read`
  (`cmd/xmustard-ops/main.go:116`). The status route also returns the full baseline
  (`diagnostics.go:633`).
- There is no replay or export route to move it to: `grep replay` in the API finds
  only issue context-replays (`main.go:3368–3428`).
- Plan line 61–62 ("the raw payload is opened only for replay/export") therefore removes
  `raw_payload` from the GET body, contradicting line 67–68 and line 14–16 and breaking
  PostgreSQL wire parity unless the PostgreSQL read changes too, which line 64 forbids.
  No current test pins the inline bytes — the e2e checks compare only
  `raw_payload_sha256`/`raw_payload_bytes` (`scripts/e2e/local_diagnostics.py:188`,
  `integrations/pi/test/e2e/pi-adapter.e2e.ts:423`) — so the contract is real but
  untested; that is an argument for writing the test, not for silently dropping the field.
- If B instead keeps raw inline on GET, its memory advantage over A collapses to
  skipping one Base64 decode (1 % of allocations, report `:101`), which does not pay
  for a second on-disk format, orphan recovery states and dual-format reads.

Required: present "serve `raw_payload` inline on ordinary GET, or move it behind an
explicit replay/export route on both backends and all four consumers" as an owner
decision, in the same words the headroom report uses ("a smaller stored or served
representation, which is a contract decision", `:199–200`). Without that decision B
must be rejected as written.

### R3. Two-pass verification needs a same-descriptor rule, not a reopen

- Envelopes are never modified in place: `writeAtomic` always writes a fresh temp file
  and renames it (`diagnostics_local_store.go:387–425`), and the only other writer is
  prune, which unlinks (`:365`, under the import flock). So bytes behind one open
  descriptor are stable by the store's own contract; the path is not, since prune can
  unlink it (the existing `Latest` retry at `:534–546` exists for exactly that race).
- Plan line 37–38 lets the second pass "reopen the immutable run file". Reopening by
  path can hit ENOENT after a prune and, in principle, a different inode; it also
  re-runs the no-follow path walk (`safepath_unix.go:23–63`). The "bounded temporary
  spool" (line 38–39) doubles write I/O, must be charged to the 256 MiB quota and
  temp-file recovery, and buys nothing the same descriptor does not already give.

Required: replace both with one rule: open once under `openWorkspaceFileBeneath`,
`Stat` and bound by `diagnosticsMaxEnvelopeBytes` by counting bytes read (not by the
stat size alone, `:467`), hash the whole file and the self-checksum in pass one,
then `pread`/seek to 0 on the same descriptor for pass two, re-hashing while parsing
and aborting the body on any digest mismatch. Check `ctx` per chunk in both passes;
`ReadAllAdmitted` and `io.Copy` do not (`budget.go:255–268`). Drop the spool.

### R4. The admission paragraph names a limit that does not exist

- Plan line 50 says "retain a bounded concurrent-read limit". There is none for GET:
  the only bound is the per-request scope against the 64 MiB transient pool
  (`main.go:377–385`, `budget.go:275`), and a baseline GET reserves `2 × file size` plus
  the chunked file read (`diagnostics_local_store.go:475`, `budget.go:212–241`), about
  three envelope sizes. Import slots (`diagnostics.go:325`) do not apply to reads.
- This matters because streaming lowers the per-GET reservation. Fewer reserved bytes
  means the pool admits more concurrent GETs, which can raise the sampled peak — the
  same shape as the rejected candidate, where less accounting produced more resident
  memory (report `:143–158`). The benchmark's two readers run in tight loops
  (`scripts/bench/rss_bench.py:598–604`), so a slower GET also trivially lowers the peak
  by doing less work, which the headroom plan forbids counting (`…memory-headroom.md:134–135`).

Required: (a) correct the sentence; (b) add an explicit per-process cap on concurrent
baseline reads chosen so admitted concurrency does not increase; (c) charge the bytes
that will be live (rows, original, response or spool), not only bytes read; (d) require
each A/B report to state `api_reads` for the step (recorded at `rss_bench.py:620–622`)
and reject a candidate whose read count falls materially below A's.

### R5. Separate the historical 3/6 from the attested 3/5

Plan line 18–19 cites only the post-repair 3/6 (104.3, 110.6, 111.5 MB), of which the
111.5 MB report was never saved (`…-fable-local-diagnostics-recheck.md:105,110`;
`…-fable-memory-headroom-plan.md:165`). The attested series 1 — five preregistered,
sidecar-verified attempts, 3 pass / 2 fail at 105.8 and 104.1 MB, API 52.6 and 51.2 MiB
at the failing peaks versus 40.6–44.2 MiB when passing, and no `git` child resident at
any failing peak (`…-memory-headroom-implementation.md:45–58`; verified in my review
`:33`) — is the stronger evidence for the read path and is absent. Required: cite both
series with their provenance, and use series 1 for attribution.

## Non-blocking findings

1. **Cost model for two passes (plan asks, but does not state).** Per baseline GET today:
   one 1.97 MB read, one SHA-256, one `Unmarshal`, 23.7 MB allocated, ~38 ms per GET,
   ~29 ms CPU per read (report `:104–109,150`). Two passes add a second 1.97 MB read
   (page-cache memcpy on a warm cache, not disk), a second SHA-256, and a token-stream
   parse that is normally slower per byte than struct `Unmarshal`. Under the step's
   1,574–3,115 reads that is roughly double the read-syscall traffic and a CPU increase
   that must be measured in step 1, not assumed. State this and preregister a latency
   and CPU envelope, otherwise "unacceptable CPU/latency cost" has no threshold.
2. **Late errors are not new.** `writeJSON` already discards the encode error after
   `WriteHeader` (`main.go:653`), so a 200 with a truncated body is the existing failure
   signature. Say so, and require MCP and Pi to surface a truncated body as an error
   (the shim's JSON parse will fail, `cmd/xmustard-mcp/main.go:259–268`), never as an
   empty diagnostics list.
3. **The MCP shim buffers the whole body anyway** (`ReadAllAdmitted` up to 16 MiB, then
   `string(raw)`, `cmd/xmustard-mcp/main.go:254–268`). Streaming the API response does
   not change shim RSS; the benchmark's readers are direct HTTP, so this is fine for the
   gate but should be stated so no one expects a shim benefit.
4. **Single-read guarantee.** Today `Latest` then `Rows` read the file once through the
   per-request cache (`diagnostics_local_store.go:85–91,455`). A streaming adapter with
   the retained interface would scan twice per call and four times per GET; whichever
   candidate is chosen must preserve one verify pass and one parse pass per request.
5. **Wire parity details for any streaming encoder:** `diagnostics` is `[]` never null
   (`:584`), struct-tag field order and `omitempty`, HTML escaping on (`Encoder` default),
   trailing newline (`main.go:649,653`), and PostgreSQL ordering by SQL
   (`diagnostics.go:1087`). Add a byte-identical A/B body test on the same baseline.
6. **Old v2 during B.** Prefer "no rewrite ever": old envelopes stay readable until
   their 24 h expiry and are pruned by the existing rule; that removes the staged
   migration and one recovery state from lines 75–78.
7. **Cancellation.** Import cancellation and atomicity are unchanged by A; B's
   pre-manifest orphans are safe only because recovery runs under the flock the whole
   import holds (`diagnostics_local_store.go:107–128,317–385`); say that dependency.
8. **Nine tools, no-Docker, human merge authority, fixed gate:** preserved by both
   candidates as far as the plan goes; B's tool-output change is covered by R2.
9. **Housekeeping.** Both the plan and this review are ignored by git
   (`.gitignore:34,88`); allowlist them if they should travel.

## Non-claims

I ran no benchmark, profile or test, and made no code change. Line references are to
the isolated tree as of this review. The 50–100 MB target remains unproven; nothing
here selects or authorizes either candidate.
