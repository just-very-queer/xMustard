# Plan fifth review: diagnostics live-set redesign — 2026-09-24

Reviewer: Claude Fable 5.1, focused recheck of the provisional plan
`docs/plans/2026-09-24-diagnostics-live-set-redesign.md`. Verified sha256
`62f8fee5be15792fc70c65206635c4287a8e60672ef64dd1ed85f2a0799f8501` (matches the requested
value; 256 lines). Read in the isolated tree `/private/tmp/xmustard-opus-l9b1F4` (base
`cd13e2b`) against the fourth review
(`docs/reviews/2026-09-24-fable-diagnostics-live-set-plan-fourth-review.md`), the budget
package, the read path, the ops CLI, the fixed benchmark, and Go 1.26.1's `encoding/json` run
as an oracle. This file is the only write. No source, plan, benchmark, main-checkout, commit,
push, merge, import or gate run. Scope: G1 sufficiency, coherence of the absorbed non-blocking
clarifications, and the fourth review's escape-literal rendering. Prior resolved items
(F1–F4, coherence checks) are not reopened; no new evidence against them was found.

## Verdict: APPROVED (plan text)

G1 is closed as required and the five non-blocking clarifications are absorbed coherently.
Approval covers the plan text only. It does not select import-first, redesign-first or defer,
does not authorize implementation, migration, benchmark edit or import, and leaves the
50–100 MB process-tree target unproven until the fixed attested series passes (plan 3–6,
247–256).

## G1: row-string parity gate is now sufficient

Plan 119–125 adds the row-string cases the fourth review required, and plan 123–125 makes
`json.Marshal(string)` on identical inputs the byte-for-byte oracle and forbids handwritten
expected literals. Checked against the required list:

| Required by G1 | Plan | Oracle behavior (Go 1.26.1) |
|---|---|---|
| quotes, backslash | 120 | `\"`, `\\` |
| each short control escape | 120 | `\b \f \n \r \t` |
| other bytes below 0x20 | 120–121 | `\u00XX` (checked 0x00, 0x01, 0x1f) |
| DEL | 121 | verbatim byte 0x7f |
| `<`, `>`, `&` | 121 | `<`, `>`, `&` |
| U+2028 / U+2029 | 122 | ` `, ` ` |
| invalid UTF-8 | 122 | `�` per invalid byte; a truncated 2-byte prefix of a 3-byte rune yields two |
| multibyte rune across the 64 KiB scratch boundary | 122–123 | oracle is the whole-string marshal, so the chunked path must reassemble |
| tested against `json.Marshal(string)` | 123–125 | handler encoder (`cmd/xmustard-api/main.go:653`) and `json.Marshal` both escape HTML, so the oracle matches the wire encoder |

The oracle is the right one: `DiagnosticRecord` and its nested link types hold only strings,
`*string`, ints and pointer structs, no maps (`workspaceops/diagnostics.go:183–203`), so
`appendString` is the only string encoder in play and there is no key-sorting path to
reproduce. Whole-body parity against the current `json.Encoder` (plan 116–119) plus the
frozen metadata snapshot (plan 125–129) covers `omitempty` pointers and nested objects, and
the CLI's `json.Marshal` plus newline (`cmd/xmustard-ops/main.go:500–508`) is in the same
fixture (plan 127–129). Gate 2 (plan 213–214) requires all parity cases. Sufficient.

## Erratum to the fourth review (non-blocking, records only)

Fourth review lines 52–53 render the escape sequences as literal characters (`<` `>` `&`
"as `<` `>` `&`", invalid UTF-8 "as `�`", U+2028/U+2029 "as ` `/` `"). The backslash
escapes were lost in rendering. Go's encoder actually emits `<`, `>`, `&`,
`�` (one per invalid byte) and ` `, ` `, as confirmed by running
`encoding/json` (hex of `json.Marshal("<")` is `22 5c 75 30 30 33 63 22`). The rest of that
paragraph (short forms, `\u00XX` for other controls, DEL verbatim) is correct. The plan is
unaffected: it repeats no literals and defers to the oracle (plan 123–125), which is exactly
why the oracle rule matters.

Also confirmed by oracle: `json.Compact` then `json.HTMLEscape` on a pretty payload with
`<`, `>`, `&`, U+2028 and U+2029 is byte-identical to a `json.RawMessage` field written by
both `json.NewEncoder(...).Encode` and `json.Marshal` (plan 91–94), a nil `RawMessage` with
`omitempty` is omitted (plan 95–96), and an invalid `RawMessage` makes the encoder error,
which is why pass one's `json.Valid` check (plan 79–80) must precede output.

## Non-blocking clarifications: coherence

| Fourth-review item | Plan | Coherent with code |
|---|---|---|
| 1 co-tenant arithmetic, operator override | 159–165, 215–218, 240–242 | Pool 64 MiB (`budget.go:275`); import ledger 16 MiB (`diagnostics_input.go:35`); `Acquire` admits when `used ≤ max−n` (`budget.go:55`), so 48 used + 16 requested is admitted at exactly 64. Minimums 24/48/64 MiB follow. Gate 2 and gate 4 both exercise two reads plus one HTTP import at 64 MiB. |
| 2 `api_reads` clusters, per-reader tally | 199–201, 226–231 | Gate 1 records per-reader completions, thread liveness, and baseline vs no-baseline per completion; gate 3 reports both clusters and treats fixed-step `api_reads` as report-only. Matches `rss_bench.py:596–604` (readers start before the first import) and the no-baseline early return (`diagnostics.go:729–731`). |
| 3 per-request envelope cache | 55–57 | Prepared local reader must bypass `readEnvelope`; the reason (retaining all rows and the original) is stated. |
| 4 Git probe placement | 107–109 | Probe runs before the first response byte for a baseline-present GET, as today (`diagnostics.go:720–724` precedes row read at `:733`); no-baseline GET skips it. Cancellation during the probe stays a clean pre-output response (plan 108–109, 171–173). |
| 5 git-ignore housekeeping | 253–256 | Plan and reviews are ignored (`.gitignore:34,88`; `git check-ignore` confirms this file too). Plan says do not allowlist now, and allowlist only as part of an approved import. Consistent with the no-authorization stance. |

## Non-blocking notes for the implementer (no plan edit required)

1. **"Reject an operator override below 24 MiB as invalid configuration" (plan 163).**
   Today `transientBudgetFromEnv` (`budget.go:281–288`) silently falls back to 64 MiB on an
   unparsable or non-positive value. The plan's word "reject" should mean fail fast at
   startup with a logged reason, not silent fallback, since an operator who lowered the pool
   deliberately would otherwise get a larger pool than configured. Either reading satisfies
   the plan; the implementation should pick fail-fast and say so in the surfaced effective
   limit (plan 165).
2. **Boundary-case enrichment (plan 122–123).** The oracle rule makes extra boundary shapes
   cheap. Beyond a valid rune split across the 64 KiB boundary, place an escapable rune
   (U+2028) and a truncated invalid sequence at the boundary too, at offsets 65,536 minus
   one to three. These are the shapes where a chunked escaper must both reassemble and
   decide to escape, or must emit `�` per byte without waiting for a continuation
   byte that never comes.

## Non-claims

I ran no benchmark, profile, backend test or build; the only execution was a throwaway Go
program under the job's temporary directory calling `encoding/json` as the oracle. Line
references are to the isolated tree at base `cd13e2b`. Nothing here selects or authorizes
implementation, import or a representation change. The fixed sampled gate remains the
acceptance gate, and the roughly 50–100 MB process-tree target remains unproven.
