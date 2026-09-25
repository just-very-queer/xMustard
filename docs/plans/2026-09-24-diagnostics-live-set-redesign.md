# Provisional design: diagnostics live-set reduction — 2026-09-24

Status: provisional design only. The owner has not selected whether a redesign
should precede import. This document authorizes no implementation, migration,
benchmark edit, or import. The roughly 50–100 MB process-tree target remains
unproven; the fixed sampled gate is unchanged and remains the acceptance gate.

## Evidence and boundary

Local storage writes normalized rows in import order and embeds the exact input
as Base64 in one checksummed v2 envelope. A read verifies the pointer and
envelope, unmarshals it, decodes the original bytes into inline
`ReplayArchive.RawPayload`, then copies and sorts up to 2,500 rows by severity
and path. The API encoder buffers the full `DiagnosticsReadResult` before
writing. Thus ordinary GET returns all rows and inline `raw_payload`; the 1.97
MB envelope currently becomes about a 1.85 MB response for input without HTML
characters. Valid raw input with `<`, `>`, or `&` can expand up to sixfold in
the escaped raw JSON field. The stored input bytes are exact, while wire
`json.RawMessage` is compacted and HTML-escaped by Go's encoder. This behavior
is a contract to preserve, including PostgreSQL parity. `xmustard-ops
diagnostics read` calls `ReadDiagnosticsCtx` directly and marshals its existing
slice result; only API GET is in candidate A. No CLI RSS improvement is
claimed.

Evidence must keep the two baseline series distinct. The earlier post-repair
historical series failed 3/6 at 104.3, 110.6, and 111.5 MB; the 111.5 MB report
was not saved. The stronger, preregistered and sidecar-attested series 1 had
five valid attempts: 3 pass / 2 fail, with failures at 105.8 and 104.1 MB. API
RSS at those failing peaks was 52.6 and 51.2 MiB, versus 40.6–44.2 MiB in
passing peaks; no Git child was resident at failing peaks. This supports
investigating the concurrent API read path, but does not identify one dominant
operation. Profile fetches during the CLI step failed unexplained. A
capacity-hint candidate reduced allocation/CPU measurements but raised
isolated API RSS and was rejected; no A/B acceptance series ran.

Keep the nine MCP tools and schemas, HTTP/CLI import contracts, no-Docker and
no-Postgres default, human merge authority, replay integrity, and inline raw
payload. Do not edit the fixed benchmark fixture, sampler, interval, roots,
threshold, or workload.

## Candidate A: verified streaming response with a prepared-read seam

Add an internal `PreparedDiagnosticsRead` interface at the store/HTTP boundary,
separate from the slice-returning `DiagnosticBaselineStore.Rows`: prepare one
response under request context and admission, expose response metadata, and
write the JSON object to an `io.Writer`. It must not return a retained
`[]DiagnosticRecord` or full `DiagnosticsReadResult`. Use this seam in the API
GET handler. Implement matching local and PostgreSQL adapters so storage
policy, warnings, freshness, and wire behavior remain in their adapters; do
not duplicate that policy in the handler. MCP and Pi continue consuming the
same HTTP JSON contract. CLI keeps its existing direct `ReadDiagnosticsCtx`
slice path; its `json.Marshal` output belongs in parity fixtures, with no CLI
RSS win claimed. The status route remains unchanged and keeps its current
full-baseline live set; it is outside this GET-only candidate and its memory
claim. The prepared local reader must bypass the request-scoped full-envelope
cache in `readEnvelope`; calling that path to obtain run metadata would retain
all rows and the original payload, defeating the seam.

For newly written local v2 envelopes, preserve the schema/checksum prefix but
serialize top-level `raw_payload_base64` before `rows`. At publish time sort
rows with `sort.SliceStable` on the existing severity/path key, preserving
import order for equal keys. No new format marker is needed: pass one
recognizes field order and verifies row ordering. New envelopes can then stream
in response order. Legacy v2 files with rows before raw or unsorted rows use
the current bounded full-read path (at most 2,500 rows and the existing 4 MiB
envelope cap), including stable sort. This fallback remains in the adapter
indefinitely for a legacy envelope that remains latest; 24-hour retention only
bounds reads by ID after it is superseded by a newer import.

For local reads, open once with `openWorkspaceFileBeneath` and retain that
descriptor. Pass one token-parses the entire envelope without retaining rows,
counts actual bytes read, and checks context between chunks. `Stat` may reject
an obviously oversized file early but does not enforce the byte cap. Pass one
must validate every existing `readEnvelope` invariant before output: regular
file and `diagnosticsMaxEnvelopeBytes`; pointer and envelope self-checksums;
well-formed JSON and Base64; schema, run ID, and workspace ID; non-nil replay
archive; row count equal to `DiagnosticCount`; and decoded raw payload SHA-256
and byte length. It records field order and checks that rows are sorted by the
stable severity/path key. Validate the decoded original with
`json.Valid` as well as its SHA-256 and byte length. Do not retain byte offsets:
pass two reads the raw-before-rows layout sequentially. Retain only bounded
run/archive metadata, decoded original bytes (maximum 1 MiB), and
warning/storage metadata.

Pass two seeks to the beginning of the same descriptor, parses sequentially
through the new raw-before-rows layout, and recomputes the whole-file digest.
On reaching the raw value, it emits the response and baseline prefix with the
wire payload; on rows, it opens `diagnostics` and emits rows one at a time. It
continues to EOF and checks the pass-two digest before emitting the response
suffix, closing brace, and newline. Thus both passes are full token parses;
there is no offset-seek/reopen or incoherent out-of-order whole-file hash. The
stored original must be buffered within admission, passed through
`json.Compact`, then `json.HTMLEscape` to match Go's RawMessage encoding for
pretty whitespace, `<`, `>`, `&`, U+2028, and U+2029. PostgreSQL currently
unmarshals `raw_payload_json` into `any`, producing canonical re-encoding;
retain it and continue omitting the field when nil.

This relies on xMustard's immutable-inode contract: writers atomically rename
complete files and pruning unlinks under the workspace lock. An external
in-place mutation remains possible. If pass two detects it after output starts,
the handler must abort with `panic(http.ErrAbortHandler)`, not return normally;
the transport then exposes an aborted/truncated response, not a complete valid
JSON body. MCP/Pi must surface parse/transport failure, never an empty list.
Wrap latest-pointer lookup and opening/pass one in the existing one-retry
pointer-plus-envelope protocol so an ENOENT caused by prune retries the pair.

Run the local Git freshness probe before writing the first response byte, as
the current baseline-present GET does; cancellation or probe failure remains
a clean pre-output response. A no-baseline GET does not run that probe.

The PostgreSQL prepared-reader adapter wraps the current query, aggregated
rows, defaults, `diagnosticReplayWarnings`, and encoder. It makes no streaming
or PostgreSQL RSS claim; no cursor or SQL change is part of this candidate.
Both adapters preserve field order and `omitempty`, HTML escaping, newline,
`diagnostics: []` rather than null, row order, raw payload semantics, and
freshness/warning/error behavior. Add same-baseline byte-for-byte response
parity against current `json.Encoder` for local and PostgreSQL, covering empty
rows, omitted fields, duplicate sort keys, pretty raw JSON with HTML and
U+2028/U+2029, omitted PostgreSQL raw payload, row order, default link status,
and warning counts. For row-string escaping, cover quotes, backslash, each
short control escape, other control bytes below 0x20, DEL, `<`, `>`, `&`,
U+2028/U+2029, invalid UTF-8, and a multibyte rune split across the 64 KiB
scratch boundary. On each identical string input, require byte-for-byte parity
with `json.Marshal(string)` as the encoding oracle; do not infer escapes from
handwritten expected literals. Freeze `GeneratedAt` and other volatile
metadata in the fixture, or compare both writers against the same prepared
metadata snapshot, so timestamps do not cause a false parity failure. Include the CLI's
`json.Marshal` plus newline output against the same snapshot; CLI retains its
existing slice path and receives no streaming/RSS claim.

Two passes read and token-parse the envelope twice. The second warm read is
page-cache traffic rather than necessarily disk I/O, but adds about 1.97 MB of
scan traffic and another SHA-256; token parsing twice may use more CPU than
today's one `Unmarshal`. Historical single-read measurements were around 38 ms
wall / 29 ms CPU. Each baseline-present GET also launches a Git probe, so
`api_reads` largely measures probe throughput rather than read-path throughput;
no-baseline GETs do not run that probe. The MCP shim still buffers its response
and gains no claimed RSS reduction; fixed benchmark readers use direct HTTP.

There is currently no GET baseline-read concurrency cap. Add a per-process
semaphore of at most two active baseline reads, held through write completion
or cancellation. This matches the fixed workload's two direct readers and
cannot increase their concurrency. Contention returns HTTP 503 with
`Retry-After`; reuse `writeOverloaded` so HTTP, MCP, and Pi retain the existing
503 shape. Give each read one up-front `Scope.Acquire(24 MiB)` reservation,
held through response completion; this is a monotonic accounting reservation,
not a 24 MiB allocation. Do not add per-buffer charges to the same pool or
depend on partial release, which `Scope` does not provide. Count actual stream
bytes separately as metrics and cap checks, not admission charges. Use one
reusable bounded I/O/escaping scratch buffer; reserve for decoded original
(≤1 MiB), compacted original (≤1 MiB), escaped raw JSON (≤6 MiB), bounded run
metadata (≤4 MiB), one parsed row (≤4 MiB), and decoder/parser/object/scratch
overhead (≤8 MiB): a conservative combined bound of 24 MiB. Do not retain a
full encoded-row buffer: escape row strings incrementally through a fixed
64 KiB scratch buffer, with wire expansion up to sixfold, including HTML
characters. The response body itself must not accumulate in memory.
Focused worst-case legal fixtures must demonstrate that the actual live set
fits. If not, stop and review the cap before shipping; valid data must not be
rejected with 413. Two active reads reserve at most 48 MiB of the 64 MiB pool,
leaving 16 MiB for co-tenants. The effective transient pool must be at least
24 MiB for one baseline read, 48 MiB to guarantee two concurrent reads, and
64 MiB to also admit one full 16 MiB HTTP import at the same time. Reject an
operator override below 24 MiB as invalid configuration; below 48 or 64 MiB,
respectively, do not claim the higher concurrency guarantee. Respect
`XMUSTARD_TRANSIENT_BYTE_BUDGET` and surface the effective configured limit.
The semaphore handles contention with 503; a
deterministic violation of the fixed file/input cap is 413. No envelope within
the fixed caps may hit the 24 MiB reservation. Two fixed-workload readers must
both receive HTTP 200.

Pass-one corruption returns the existing explicit error before body output.
Cancellation before output releases the descriptor and reservation without a
partial response. Cancellation or digest mismatch after output begins aborts
the transport as above. Storage needs no migration: retain v2 compatibility,
lock/recovery, checksums, 24-hour retention, and pointer behavior. PostgreSQL
data and semantics stay unchanged behind its prepared-reader wrapper.

## Candidate B: representation change only by owner decision

Separating manifest, row, and raw-payload files does not lower ordinary-GET
live bytes while exact replay payload remains inline: the raw bytes still must
be read and emitted. Savings would mainly be one Base64 decode, while adding
format, recovery, and migration states. Do not treat B as a fallback under
current contracts. Consider it only after an explicit owner decision to
change stored or served representation, such as moving raw payload behind a
replay/export route on both backends and all consumers. That needs separate
compatibility, migration, recovery, retention, and replay-integrity design and
approval. Nothing here implies that change.

## Provisional recommendation and stop/go gates

Investigate candidate A first because it retains the v2 persistence format and
wire response while putting the work at a defined read seam. The owner still
chooses whether import precedes redesign. If these gates fail, stop and import
first or defer; do not substitute B silently.

1. **Profile gate.** Use the opt-in profile build only, not as fixed-gate
   evidence. Reproduce isolated/concurrent reads and capture heap/allocation,
   CPU, latency, bytes-read, and `api_reads`; use an out-of-band driver to
   record each reader's completed count, thread liveness, and whether each
   completion had a baseline or returned no baseline. Inspect fixed
   benchmark logs for reader-thread exceptions without changing its fixture or
   sampler. Resolve or document an alternative for the failed CLI-step profile
   fetch. If no tractable live-set cost appears, stop.
2. **Focused implementation gate.** After owner selection, prototype A only.
   Test every pass-one invariant, same-descriptor seek, actual-byte caps,
   cancellation checks in both passes, corruption before first byte,
   `http.ErrAbortHandler` after first byte, latest-pointer/prune retry, and
   per-read cap classification (valid maximum fixtures fit; deterministic
   fixed file/input cap breaches are 413; semaphore contention uses the
   existing `writeOverloaded` 503 shape).
   Test stable publish sorting for new files and bounded sort-on-read for old
   v2. Verify byte-identical local and PostgreSQL bodies, including all parity
   cases above. Exercise two concurrent reads, pool exhaustion, 503 behavior,
   and HTTP/MCP/Pi propagation. Test two concurrent reads plus a full-budget
   HTTP import; under a 64 MiB effective pool all three must be admitted
   together. Test operator overrides below 48 and 64 MiB against the stated
   concurrency guarantees. The fixed workload's two readers must receive 200.
   Failed/cancelled imports must still preserve the prior baseline. Test
   the status route as unchanged and make no memory claim for it.
3. **Preregistered performance gate.** Pin provenance, workload, and comparison
   order. Run five same-session interleaved A/B pairs under the existing
   attestation protocol. Make the focused, preloaded-baseline two-reader
   throughput check decisive: compare per-reader completed GET counts across
   five paired A/B trials and require median B completion count at least 95%
   of median A. Require median/p95 latency and API CPU per completed read
   within 1.5× of A; retain per-reader counts and thread liveness. The fixed
   step's `api_reads` median is reported only, not a decision criterion, until
   bimodality is explained. Report both observed ~1.6k and ~3.1k clusters:
   baseline-present GETs start a Git probe, and a single A attempt is not a
   valid denominator. Report bytes scanned, cache context, response bytes
   (including up to sixfold escaped raw-field expansion), p50/p95, CPU, and
   completions. Reject materially dropped work even if RSS falls.
4. **Fixed resource acceptance.** Only after the preceding gates pass, run the
   unchanged attested repeated RSS A/B protocol: no fixture, sampler, interval,
   roots, threshold, or workload edits. Report `api_reads`, per-reader
   completions, reader-thread liveness, and HTTP statuses per attempt through
   out-of-band observation; inspect logs for thread exceptions without editing
   the harness. All fixed-workload reader statuses must remain 200. In the
   no-Postgres HTTP/MCP/Pi rerun, include two concurrent baseline reads plus
   one HTTP diagnostics import under a 64 MiB effective pool. Attribute any
   memory change only to API GET: status route and direct CLI reads retain
   their old live sets. Require
   every valid candidate attempt to pass the fixed 100,000,000-byte sampled
   threshold. Then rerun full backend checks and no-Postgres HTTP/MCP/Pi flow;
   run the native PostgreSQL control because its adapter implements the new
   interface. Failed/invalid series stops acceptance. Finite gate/profile
   passes do not prove a universal memory ceiling; the 50–100 MB target remains
   unproven pending that evidence across representative profiles.

The owner retains the choice to import first, redesign first, or defer both.
Human review of the exact diff remains merge authority; this plan makes no
claim that implementation is selected or authorized. This plan and its reviews
are git-ignored; do not allowlist them now. If the owner approves importing the
candidate, allowlist the relevant plan/review artifacts only as part of that
approved import.
