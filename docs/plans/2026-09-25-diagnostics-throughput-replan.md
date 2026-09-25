# Diagnostics GET throughput replan — 2026-09-25

## Decision

Prototype a single-pass local GET that writes the exact response into a
mode-0600 temporary spool while parsing and hashing the existing v2 envelope.
Validate the pointer checksum, envelope checksum, metadata, row count, replay
payload size/hash/JSON, ordering, and trailing input before copying any bytes to
HTTP. This keeps corruption errors pre-response, removes the candidate's second
full envelope parse/hash, and bounds heap use independently of response size.
The spool is transient response staging, not a new persistence format.

This is a hypothesis, not an expected result. The prepared-stream candidate
lowered API peak RSS (about 28–30 MiB versus 49–51 MiB) but completed only
100/134 median reads (74.6%); its p50, p95, and CPU per read all rose. The raw-row
forwarding attempt also missed throughput and failed byte parity on valid
noncanonical envelopes, so it was reverted. One unchanged fixed RSS run passed
at 95,682,560 bytes, but that is preliminary and does not offset the throughput
failure. See the [stop/go record](../benchmarks/evidence/2026-09-25/diagnostics-live-set-stop.md),
[safe-candidate A/B](../benchmarks/evidence/2026-09-25/preloaded-ab/api_read_rss.json),
[raw-row A/B](../benchmarks/evidence/2026-09-25/preloaded-ab-raw/api_read_rss.json),
and [fixed RSS run](../benchmarks/evidence/2026-09-25/diagnostics-stream-preliminary-rss.json).

## Ordered work and completion criteria

1. **Profile the safe candidate.** Use its existing opt-in profile build and
   paired-run artifacts to attribute time and allocations across both envelope
   scans, row decode/encode, Git freshness, and response writes. Keep diagnostic
   profiling separate from acceptance. Record whether spool creation/copy is
   likely to erase the saved parse cost. **Done when** the profile report names
   the dominant per-read costs and supports or rejects the single-pass spool
   hypothesis. If it does not, stop and report the evidence before coding.

2. **Implement one local prototype.** Limit source changes to
   `api-go/internal/workspaceops/diagnostics_prepared_local.go`,
   `diagnostics_prepared.go`, and the narrow API GET integration in
   `api-go/cmd/xmustard-api/main.go`; add focused tests beside those files.
   Parse the envelope once, incrementally hash the exact bytes, and encode rows
   with the standard JSON semantics used by the existing GET contract into a
   private temporary spool. Validate all invariants before `io.Copy` to the
   client; close and remove the spool on success, error, or cancellation. Keep
   pointer/prune retry, freshness behavior, the two-read admission, and the
   existing 24 MiB per-read reservation. Preserve raw payload inline, response
   field order, escaping, newline, warning/storage metadata, and stable row
   order. Keep PostgreSQL, CLI, status, MCP tool schemas, and Pi behavior on
   their existing adapters. **Done when** byte-for-byte fixtures match the
   current encoder for local and PostgreSQL responses, and corruption,
   cancellation, cleanup, and concurrent replacement tests pass.

3. **Run the decisive throughput check.** Use the fixed preloaded two-reader
   diagnostic workload and five paired, interleaved trials under the existing
   provenance protocol. Capture per-reader counts/liveness and statuses
   out-of-band; all completed GETs must be HTTP 200. Require median candidate
   completions at least 95% of median baseline, with median and p95 latency and
   API CPU per completed read each no more than 1.5× baseline. The existing
   driver is diagnostic and does not prove independent reader liveness; keep
   that limitation explicit. **Stop** if any performance threshold fails or
   completion is lost, even if RSS falls. Do not run or cite a fixed-gate series
   as a substitute for this check.

4. **Prove memory and integration only after step 3 passes.** Run the unchanged,
   attested five-run fixed RSS A/B protocol with its original fixture, sampler,
   interval, roots, workload, and 100,000,000-byte threshold. Every valid
   candidate run must pass; report `api_reads`, HTTP statuses, reader counts,
   and thread liveness. Then run the full backend gate, no-Postgres HTTP/MCP/Pi
   flow, and native PostgreSQL control. Exercise two GETs plus a full-size HTTP
   import under the configured 64 MiB pool; verify no-Postgres remains the
   default. **Done when** all gates pass with provenance and exact wire parity
   evidence. Report the roughly 50–100 MB process-tree target as measured only
   for these workloads; finite samples do not establish a universal RSS bound.

## Alternatives and boundaries

Reverting to materialized `ReadDiagnosticsCtx` is the known throughput reference,
but gives up the candidate's large GET live set and therefore does not satisfy
the combined goal by itself. Raw stored-row forwarding is rejected because it
failed both the paired throughput bar and exact parity. A storage representation
change (for example, moving replay payload out of GET) could reduce work, but
changes public behavior and requires a separate owner-approved compatibility,
migration, retention, and replay-integrity design. Do not fold it into this
prototype.

Keep local-first operation, no Docker/PostgreSQL requirement, all nine MCP tools,
existing UI scope, and human final merge authority. Tests and agent reviews do
not authorize import or merge.

## Acceptance matrix

| Gap | Evidence required | Stop condition |
|---|---|---|
| Exact GET wire parity | Same-snapshot byte equality against current `json.Encoder`; local and PostgreSQL, empty/legacy rows, omitted fields, escaping, raw payload and newline | Any byte mismatch |
| Integrity before response | Corrupt checksum/metadata/row count/raw payload/trailing data returns an error before body; concurrent replacement and cancellation clean spool | Partial valid-looking JSON or leaked temp file |
| Throughput | Five paired preloaded two-reader trials; median completions ≥95%; latency and CPU ratios ≤1.5×; all statuses 200 | Any threshold miss or dropped reader work |
| Memory | Five valid unchanged fixed-gate candidate runs below 100,000,000 sampled bytes; inspect per-reader completion evidence | Any valid run over limit or invalid series |
| Runtime compatibility | Backend checks, no-Postgres HTTP/MCP/Pi, native PostgreSQL control, and two-read plus import admission | Any contract/gate regression |

