# Plan recheck: diagnostics live-set redesign — 2026-09-24

Reviewer: Claude Fable 5.1, independent recheck of the revised provisional plan
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md`. Verified sha256
`1a3929b4028d010b8049e9662d5b49d43359ab3e3197fa03254bdc1efbd76126` (matches the requested
value). Read in the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base `cd13e2b`),
against my first review (`…-fable-diagnostics-live-set-plan.md`), the Go read path, the
budget package, the fixed benchmark, the series-1 evidence and Go 1.26.1's `encoding/json`.
This file is the only write. No source, plan, benchmark, main-checkout, commit or gate run.

## Verdict: CHANGES_REQUIRED

R1–R5 from the first review are resolved as written. The revision is honest about what
candidate A can and cannot save, rejects candidate B as a fallback, and keeps the fixed
gate untouched. It is not yet executable as a prototype spec: four points would send an
implementer down a path the current code and contracts do not support. Each is small to
fix in the plan text; none reopens the candidate choice.

| Item | Status | Where |
|---|---|---|
| R1 seam + streaming writer + publish-time sort | resolved | plan 39–47, 67–71, 80–81 |
| R2 candidate B needs an owner contract decision | resolved | plan 113–124 |
| R3 same descriptor, no reopen/spool, byte-count cap, ctx per chunk | resolved, see N1/N5 | plan 52–62 |
| R4 real cap, 503, reservation not relaxed, `api_reads` reported | resolved, see N4/N6 | plan 92–103, 160 |
| R5 both baseline series, series 1 for attribution | resolved | plan 20–27; report `…-memory-headroom-implementation.md:49,51` |

## Required changes

### N1. Pass one must reproduce every current pre-response validation, not four of them

`readEnvelope` refuses an envelope, before any response byte, on all of these
(`diagnostics_local_store.go:466–509`): regular file within the cap (`:467`), pointer
checksum (`:485–490`), self-checksum (`:491`), JSON well-formedness (`:495`), Base64
validity, schema, run id, workspace id, non-nil archive, `len(rows) == DiagnosticCount`
(`:498–505`), and decoded raw SHA-256 plus byte length against the archive (`:506–509`).
Plan lines 56–57 promise only "schema, self-checksum, pointer checksum, and run identity"
before the first byte, and describe pass one as a hashing scan. Under that description the
other checks are either dropped or become after-first-byte truncations, which contradicts
line 105 ("corruption found in pass one returns the existing explicit error").

Two of them cannot be moved to pass two at all without buffering: row count is known only
after the last row, and the raw payload sits after the rows in the file (`:70–77`) while it
is emitted before them in the response (`DiagnosticsReadResult` puts `baseline` before
`diagnostics`, `diagnostics.go:206–213`). Required: state that pass one is a full
token-parse-and-validate pass with no retention that enforces the complete `:466–509`
set, and that it records the byte offsets of the `rows` and `raw_payload_base64` values so
pass two can seek to the raw payload first, then back to the rows. The CPU paragraph
(lines 83–88) must then count two token parses, not one parse plus one hash.

### N2. "Exact `raw_payload` bytes" is not what the wire carries today

- Local reads store the original as `json.RawMessage` inside an `any` field (`:522`,
  `diagnostics.go:144`). Go's encoder passes every `Marshaler` result through
  `appendCompact(out, b, opts.escapeHTML)` (`$GOROOT/src/encoding/json/encode.go:483–488`,
  Go 1.26.1, `GOEXPERIMENT` empty). The wire value is therefore the original with
  insignificant whitespace removed and `<`, `>`, `&`, U+2028, U+2029 escaped. A writer that
  copies the decoded bytes verbatim, as plan lines 15, 34 and 78 read, breaks parity for any
  pretty-printed or HTML-bearing input. The fix is cheap: the raw input is capped at 1 MiB
  (`diagnostics_input.go:33`), so buffer the decoded original inside the reservation and
  emit it through `json.Compact` + `json.HTMLEscape`; say so, and add pretty-printed and
  U+2028 inputs to the parity test list on lines 76–78.
- PostgreSQL unmarshals `raw_payload_json` into `any` (`diagnostics.go:1381–1385`), so its
  wire `raw_payload` is a canonical re-encoding (sorted keys, reformatted numbers), not the
  original bytes, and it is omitted when the column is empty (`omitempty` on a nil
  interface). The PostgreSQL adapter must keep that decode/encode for the field.
- PostgreSQL rows arrive as one `jsonb_agg` blob (`:1085–1096`); "its adapter must stream
  that order" (line 71–72) needs a per-row cursor query instead, plus inline reproduction
  of the `link_status` default (`:1100–1104`) and the `diagnosticReplayWarnings` counts
  (`:1210–1222`), which is feasible because `warnings` follows `diagnostics` in the struct.
  Name this query change; line 110–111 currently says only the adapter changes.

### N3. The reader cannot tell a publish-sorted envelope from a legacy one

New and old files share the schema string (`:27`, `:226–227`) and the plan adds no marker.
Line 70–71 ("may need a bounded row collection") leaves the branch undefined. Required:
either verify sortedness in pass one (O(1) state: compare each row's severity/path with
the previous; it already tokenizes per N1) and stream when sorted, else collect and
stable-sort within the reservation; or add an explicit envelope field. The first needs no
format change. Also require the publish-time sort to be `sort.SliceStable` on the same key
so rows with equal severity and path keep import order exactly as `:586–591` does today,
and put duplicate-key rows in the parity test.

### N4. The 95 % `api_reads` criterion has no defined reference

Series-1 A attempts recorded 3082, 1753, 1559, 1617 and 3152 reads for the same fixed
step (`docs/benchmarks/evidence/2026-09-24/memory-headroom/series1-baseline/SUMMARY.txt:4–20`);
the profile runs show the same two clusters (1740/1776/3189, 3205/1572/1646). "At least
95 % of A for the same fixed step" (line 152–153) is unanswerable when A itself spans 2×:
against 3152 a candidate at 1,600 fails though A produced 1,559 twice. Required: preregister
the statistic and pairing, for example candidate median over N ≥ 5 attempts against the
same-session A median, alternating order, with the bimodality either explained or paired
out first. Note that each GET spawns a Git probe (`diagnostics.go:721–723`, 5 s timeout,
`diagnostics_input.go:43`), so `api_reads` largely measures probe throughput, not the read
path; per-completed-read API CPU (already in line 151) is the better throughput guard.

## Non-blocking findings

- **N5. Truncation mechanics.** The store invariant (`writeAtomic` `:387–425`, prune under
  the lock `:364–366`) covers xMustard's own writers only; an outside process can still
  write the open inode between passes. Pass two's running digest can only be checked at
  EOF, so require the writer to consume the file to EOF and verify before emitting the
  closing `}` and newline; then any mutation yields syntactically incomplete JSON. Also
  say how the handler ends: Go's server writes the terminating chunk on normal return, so
  a body abandoned mid-stream still arrives as a complete chunked message; abort with
  `panic(http.ErrAbortHandler)` so clients see a transport error. Framing is otherwise
  unchanged: today's 1.85 MB body already goes out chunked.
- **N6. Reservation arithmetic and the gate.** Per admitted read the plan charges
  16 MiB plus up to 4 MiB read (`diagnosticsMaxEnvelopeBytes`) plus about 2 MiB output,
  about 22 MiB, against a 64 MiB pool (`budget.go:275`); today a GET reserves about three
  envelope sizes (`:475` plus `ReadAllAdmitted`), about 6 MB. Two readers leave roughly
  20 MiB for co-tenant work such as Rust captures, which may now see 503 where they did
  not. Say whether the 16 MiB is inclusive of read/output bytes (lines 97–100 read as
  double-charging). The fixed benchmark requires every reader status to be 200
  (`scripts/bench/rss_bench.py:623–625`) and its imports run out of process
  (`rss_bench.py:607–609`, `cmd/xmustard-ops/main.go:47`), so the cap must be ≥ 2 and two
  full reservations must always fit; state that as a gate constraint.
- **N7. Row count is 2,500 and the envelope 4 MiB by construction**
  (`diagnostics_input.go:33–35`), so "bounded row collection" for legacy files is already
  bounded; say the bound.
- **N8. `Latest` retry.** The pointer/prune race retry (`:534–546`) must wrap the prepared
  read's pass one, not only the pointer read, since pass one opens the envelope.
- **N9. Housekeeping.** Plan and both reviews are git-ignored (`.gitignore:34,88`);
  allowlist them if they should travel with the branch.

## Executability

If the owner chooses redesign: the seam, adapters, cap and stop/go structure are
implementable as written, and gates 1, 2 and 4 are sound. A prototype cannot start from
lines 52–62 and 67–78 until N1–N3 are written in, and gate 3 cannot be preregistered until
N4 defines its reference. No claim here selects or authorizes implementation; the
50–100 MB target remains unproven.
