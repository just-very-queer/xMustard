# Plan fourth review: diagnostics live-set redesign — 2026-09-24

Reviewer: Claude Fable 5.1, independent fourth review of the provisional plan
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md`. Verified sha256
`462587d8f72bcb79209de5e9e6fef12c2283b5ecf92d14021f36204226617c0b` (matches the requested
value; 230 lines). Read in the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base
`cd13e2b`), against the first review, the recheck and the final audit (F1–F4), the Go read
path, the budget package, the HTTP server, the MCP shim, the ops CLI, the Pi adapter, the
fixed benchmark and harness, the series-1 and profile evidence, and Go 1.26.1's
`encoding/json`. This file is the only write. No source, plan, benchmark, main-checkout,
commit, push, merge, import or gate run.

## Verdict: CHANGES_REQUIRED

Narrow: one item. F1–F4 from the final audit are resolved as written and verified below.
The candidate choice, the prepared-read seam, the raw-before-rows layout, the two-pass
validation, the monotonic 24 MiB reservation, the two-read semaphore and the four gates
are coherent with the code. One new acceptance gap remains (G1): the plan hand-writes the
row-string JSON escaper but its byte-parity fixture list never exercises it, so gate 2 as
written can pass while the streamed rows diverge from today's encoder. It is a one-paragraph
plan edit; with G1 added I would approve the text. Nothing here reopens candidate A versus B,
selects import-first, redesign-first or defer, or authorizes implementation.

## Status of F1–F4

| Item | Status | Verified against |
|---|---|---|
| F1 admission accounting not expressible; bound not derived | resolved (plan 133–152) | One up-front `Scope.Acquire(24 MiB)`, no per-buffer charges, no partial release, stream bytes counted not charged (plan 134–138). The GET request scope is a root ledger, `budget.NewScope(nil)` (`cmd/xmustard-api/main.go:383–384`), so a refused reservation is `ErrOverloaded` → `writeOverloaded` 503 (`budget.go:134–135`, `main.go:593`), matching plan 149; `ErrAdmissionLimit` → 413 (`main.go:585`) is reserved for the fixed caps. Sum 1+1+6+4+4+8 = 24 MiB is arithmetically right; the 6 MiB escaped term is the 1→6 byte expansion of `<`, `>`, `&` in `appendString` (`encode.go:1004–1032`) on a ≤1 MiB original (`diagnostics_input.go:33`). Pool is 64 MiB (`budget.go:275`); 2×24 = 48 ≤ 64 (plan 148). Today's read reserves 2×size plus the admitted read (`diagnostics_local_store.go:475–478`), about 6 MB for the 1.97 MB envelope; the plan is explicit that 24 MiB is accounting, not allocation. |
| F2 gate-3 rule the bimodality can flip | resolved (plan 200–212, 183–185, 215–218) | Preloaded-baseline two-reader per-reader completion medians are decisive; fixed-step `api_reads` is report-only; gate 1 and gate 4 record per-reader completions and thread liveness out-of-band. Harness mechanics confirmed: `read_codes.append(api.get(...)[0])` (`scripts/bench/rss_bench.py:600`) records a status only when `urlopen` returns (`scripts/e2e/harness.py:123–128`); a transport exception ends the thread silently; a non-JSON body still records its status (`harness.py:131–134`). See non-blocking 2 for new evidence on the clusters. |
| F3 legacy path permanent | resolved (plan 63–66) | Prune runs only from `Publish` under the lock (`diagnostics_local_store.go:162,317`) and skips the pointer target (`:364`); retention is 24 h (`:29`). A rows-before-raw latest envelope stays latest until the next import. Plan text now says exactly that. |
| F4 CLI reads in process | resolved (plan 20–23, 50–53, 116–118) | `xmustard-ops diagnostics read` → `ReadDiagnosticsCtx` (`cmd/xmustard-ops/main.go:116`), `json.Marshal` + newline (`:500–508`). MCP (`cmd/xmustard-mcp/main.go:150–151`) and Pi (`integrations/pi/src/tools.ts:147–151`) use HTTP. CLI output is in the parity fixture (plan 116–118). |

Final-audit non-blocking items absorbed: `writeOverloaded` reuse (plan 132–133;
`main.go:432–435`, shim mapping `cmd/xmustard-mcp/main.go:206–208`); status route named in
gate-4 attribution (plan 53–55, 198–199, 219–220); sixfold expansion stated where response
bytes are reported (plan 17–18, 211); `json.Valid` on the decoded original (plan 77–78);
offsets dropped (plan 78).

## Required change

### G1. The hand-written row escaper has no parity fixture

Plan 142–145 forbids a full encoded-row buffer and requires row strings to be escaped
"incrementally through a fixed 64 KiB scratch buffer". That replaces `encoding/json`'s
`appendString` for every string field of every row, and gate 2 accepts on "byte-identical
local and PostgreSQL bodies, including all parity cases above" (plan 195–196). The parity
list (plan 110–114) covers HTML and U+2028/U+2029 only for the pretty raw JSON payload; for
rows it lists duplicate sort keys and row order. Nothing exercises the escaper.

What it must reproduce (`$GOROOT/src/encoding/json/encode.go:999–1060`, Go 1.26.1): `"` and
`\` as `\"` `\\`; `\b` `\f` `\n` `\r` `\t` as their short forms; every other byte below 0x20
as `\u00XX`; `<` `>` `&` as `<` `>` `&` because the handler's `Encoder` has
`escapeHTML` on (`main.go:653`); invalid UTF-8 as `�`; U+2028/U+2029 as ` `/` `;
DEL and every other rune verbatim. Two of these are easy to get wrong in a chunked escaper:
a multi-byte rune split across the 64 KiB scratch boundary (emitted as `��` where Go
emits the rune, which corrupts non-ASCII paths and messages) and control bytes other than the
five short forms. Compiler messages routinely contain `<`, `>`, `&`, quotes and tabs, so
divergence would be common in real data, while the fixed fixture's messages are
`rss_diag_{i}: synthetic` on one ASCII path (`rss_bench.py:593`), so gate 4 cannot catch it.

Required: add row-string parity cases to plan 110–114 covering quotes and backslash, each
control-byte class (`\b \f \n \r \t`, another byte below 0x20, DEL), `<>&`, U+2028/U+2029,
invalid UTF-8, and multi-byte runes positioned across the scratch boundary; and require the
escaper to be tested against `json.Marshal(string)` on the same inputs. How the implementer
bounds the per-row encode (chunked escaper, or `json.Marshal` per field below a size
threshold with the chunked path above it) stays an implementation choice within the 24 MiB
reservation.

## Non-blocking findings

1. **Co-tenant arithmetic and the operator override.** Plan 148–149: two reads reserve
   48 MiB, "leaving 16 MiB for co-tenants". One HTTP import's `Limited` ledger is exactly
   16 MiB (`diagnostics.go:343–344`, `diagnostics_input.go:35`), so two GETs plus one HTTP
   import fill the pool exactly (`ByteBudget.Acquire` admits when `used ≤ max−n`,
   `budget.go:55`) and any other admitted work in the API process sees 503 until a read
   completes. Today two reads reserve about 12 MB in total. The fixed workload's imports
   run out of process (`rss_bench.py:607–609`), so gate 4 is unaffected, but the no-Postgres
   HTTP/MCP/Pi rerun in gate 4 should include a concurrent HTTP import. Separately,
   `XMUSTARD_TRANSIENT_BYTE_BUDGET` may be lowered by operators (`budget.go:273–274,281–288`);
   below 48 MiB the second fixed reader is refused, and below 24 MiB every baseline GET is a
   permanent 503 mislabelled as retryable. State the minimum pool the candidate needs.
2. **The `api_reads` clusters have a second candidate explanation; the decisive check
   removes it by construction.** Across the 11 recorded A-code runs (series 1 attempts 1–5,
   `series1-baseline/SUMMARY.txt:4,8,12,16,20`; profile A/B reports
   `results/diagnostics_imports/api_reads`) the CLI step's wall time is flat at 3.28–3.98 s
   while `api_reads` splits into ~1.56–1.78k and ~3.08–3.21k. Constant duration with a
   halved count fits one dead reader thread (the final audit's candidate). It also fits a
   variable no-baseline window: the readers start (`rss_bench.py:602–604`) before the first
   import publishes, nothing imports diagnostics earlier in the run, and a no-baseline GET
   returns before any envelope read or Git probe (`diagnostics.go:729–731`) while a
   baseline-present GET runs `git status` (`:722`, `context_packet.go:2071`). So the fixed
   step's count mixes fast no-baseline GETs with slow probe-bearing ones, and its split
   depends on the first import's latency. The preloaded-baseline two-reader check (plan
   202–205) has no such window, which is a further reason it is the right decisive
   measurement. Suggest gate 1 also tally baseline-present versus no-baseline completions
   per reader (the response carries `baseline` only when present, `diagnostics.go:208`).
3. **Per-request envelope cache.** `readEnvelope` retains the whole decoded envelope, rows
   and original, on the store instance for the request's life (`diagnostics_local_store.go:85–91,
   455–457, 512`). The plan implies the prepared reader bypasses it (plan 69); say so, or a
   reader that calls `Latest` for run metadata resurrects today's live set.
4. **Git probe placement.** `storage` follows `diagnostics` in the struct (`diagnostics.go:211`),
   so the probe's result is not needed before the first byte, but the plan does not say when
   the probe runs in the streaming sequence. Running it before the first byte, as today
   (`:722` precedes `:733`), keeps the 5 s probe window out of the open body and keeps
   cancellation during the probe a clean pre-output error rather than an abort.
5. **Housekeeping.** Plan and all four reviews are git-ignored (`.gitignore:34,88`;
   `git check-ignore` confirms); allowlist them if they are to travel with the branch.

## Coherence checks passed

- Raw-before-rows preserves the fixed checksum prefix (`localDiagnosticsEnvelopeSHAPrefix`,
  `diagnostics_local_store.go:68`) and `Run` stays third; `json.Unmarshal` is order-independent,
  so the legacy path and any older binary still read new files. No test pins field order
  (`grep raw_payload_base64 *_test.go` is empty), so the reorder breaks nothing.
- Publish sort key equals `Rows`' key: `Severity` is a string (`diagnostics.go:191`) and the
  sort is `sort.SliceStable` on severity then path (`diagnostics_local_store.go:586–591`); the
  pass-one sortedness check must be non-strict so equal keys stream in import order.
- `Latest` retries the pointer-plus-envelope pair once (`:529–546`); plan 102–103 wraps pass
  one in it.
- No `recover()` sits in `cmd/xmustard-api`, so `panic(http.ErrAbortHandler)` reaches the
  server and closes the connection; the shim reports a read error, never a list
  (`cmd/xmustard-mcp/main.go:255–268`); Pi raises on non-2xx (`integrations/pi/src/http.ts:106–118`).
- Compact + HTMLEscape reproduces `marshalerEncoder`'s `appendCompact(escapeHTML)` for the
  `RawMessage` field (`encode.go:473–488`); PostgreSQL's `any` re-encoding and `omitempty`
  are retained (plan 92–94; `diagnostics.go:144, 1381–1385`).
- The 2,500-row / 4 MiB bounds for the legacy path are the fixed caps
  (`diagnostics_input.go:34–35`).

## Non-claims

I ran no benchmark, profile, test or build, and changed no source, plan or benchmark. Line
references are to the isolated tree at base `cd13e2b` as of this review. Nothing here selects
or authorizes implementation, import or a representation change; approving the plan text after
G1 would still leave the owner's import-first/redesign-first/defer choice open and the
50–100 MB process-tree target unproven until the fixed attested series passes.
