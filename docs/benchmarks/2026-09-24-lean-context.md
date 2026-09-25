# Lean-context resource benchmark — 2026-09-24

Status: **the sampled-tree resource gate passes 3/3 on the reviewed source.** The limit
is 100,000,000 bytes. Every run passed 19 of 19 checks, and every sampler result was
valid.

| Run | Where | Sampled peak |
| --- | --- | --- |
| post-opt 1 | candidate worktree | **72,302,592 bytes (72.3 MB)** |
| post-opt 2 | candidate worktree | **84,852,736 bytes (84.9 MB)** |
| main | the main checkout after import | **80,592,896 bytes (80.6 MB)** |

All three runs share the same reviewed source digests and Rust core. On both the
candidate and main:
- `make check-backend` passed.
- The retrieval gate passed 12/12, with provenance.
- MCP evidence passed 23/23, with provenance.
- The Pi adapter passed 17 unit and 15 e2e tests, with all nine tools succeeding.

The Rust core (`d644743b`) is built from the source that Fable's Rust review approved.
That review identifies the source by recipe hash `d039d76d…7a358`.

History matters here. Before the optimization, the same frozen harness and Go/shim
binaries failed at 106.4 MB, and an earlier bench run failed at 110.5 MB. Both failures
are kept below.

What this result is:
- a **sampled** peak for this fixed workload, on one machine
- not a universal RSS ceiling
- not competitor parity or proof of task success

The three passing runs span 72.3–84.9 MB, a spread of about 12.5 MB, so the headroom
is 15–28 MB.

This report covers the fixed Stage 5 workload
(`docs/plans/2026-09-24-lean-context-implementation.md` §5). Pi/external numbers are
separate (see the end).

## Command

```sh
scripts/bench/rss.sh --report <out.json>               # exit 0 only if every check passes
scripts/bench/retrieval-gate.sh --report <out.json>
scripts/e2e/mcp-evidence.sh --report <out.json>
```

`XMUSTARD_CORE_BIN=<path>` uses a prebuilt core. Without it, the harness builds the
release core, which is how root runs. The Go API and shim are always built from the
checkout. No provider or model calls are made.

## Environment

MacBook Air (M1, 8 cores, 8 GiB). macOS 27.0 (26A428), arm64. Go 1.26.1, Python 3.9.6.
HEAD `cd13e2b` plus a dirty worktree. Each report records `results.provenance`: the
`source_diff_sha256` (the `git diff HEAD` of `api-go`, `rust-core/src` and the Cargo
files), an untracked-source digest, and the SHA-256 of every binary and script. Gold and
MCP use the shared `harness.provenance()`. `rss_bench.py` has an identical copy, kept
because that script is frozen. Resource runs were made with no other test load.

## Fixed workload (unchanged baseline fixture)

- 501 tracked files, 21,179,988 bytes. Tree digest `c16734b0…57ef8` (sha256 over sorted
  path + content-sha256). The digest is identical in every run below.
- 500 generated Go/Rust/TS files plus `core/src/many.rs` with 70 symbols.
- **Syntax limitation:** the generated Rust statements (`v += k`) lack `;`, so
  tree-sitter-rust parses them through GLR error recovery. Per the Rust owner, the Rust
  third takes 5.7 s of the 8.4 s cold parse. The fixture is kept byte-identical for
  comparability. No valid-source supplemental fixture exists; adding one needs root's
  approval, and it would never replace this baseline.
- Workload steps, in order:
  1. Load and scan.
  2. `POST /index`.
  3. Cold MCP `search`.
  4. Warm `search`.
  5. Same-size dirty edit.
  6. Rename plus delete.
  7. Two concurrent index clients (one per MCP shim).
  8. Five concurrent exactly-16 MiB captures against the 64 MiB pool.
  9. Full 64 KiB-paged expansion of every retained original: even-numbered captures via
     stdio MCP `resources/read` (alternating shims), odd-numbered via HTTP.
  10. One HTTP re-expansion.

## What the harness enforces (`scripts/bench/rss_bench.py`, frozen at `6411f391`)

- **Sampler.** One `ps -axo pid,ppid,rss,comm` snapshot every 100 ms. The tree total is
  summed within that single snapshot and covers the API, both MCP shims and all
  descendants (Rust core, git). The gate is invalid if there are zero samples, if any
  `ps` call errors or exits nonzero, if any line fails to parse, if any required root is
  missing from any snapshot, if the workload is incomplete, or if any mandatory workload
  check failed. A low RSS reading from a failed workload never counts as a pass.
- **Maxima.** Per-process maxima are unsynchronized diagnostics and are never summed.
  OS high-water (`getrusage`: bytes on darwin, KiB on Linux) is reported separately.
- **Admission.** Capture clients send `Expect: 100-continue`. The API reserves the
  declared length before reading the body. A refusal must be a parsed wire
  `503` + `Retry-After` + `"overloaded":true` with zero body bytes sent. A socket error is
  a `transport_error` failure, never a refusal. This fixes the earlier `-1` (reset)
  outcomes in the Go owner's `rss2`/`rss3`.
- **Expansion.** Every successful capture must report exactly 16,777,216 bytes and the
  payload SHA-256, and must retain a handle. Every original is then read to EOF in pages
  of ≤64 KiB while sampling continues. Each page's `offset` and `raw_sha256` are checked.
  An empty or non-contiguous non-EOF page fails immediately. So does exceeding
  `ceil(size/64 KiB)+1` pages. Reassembled size and SHA-256 must be exact.
- **Retrieval work.** Cold: cache `miss`, 501 files parsed. Warm: `hit`, 0 parsed.
  Same-size edit: 1 parsed, 500 reused. Rename/delete: ≤1 parsed, and the deleted
  file's symbols are dropped.
- **Pool and children.** Pool `in_use` ≤ max while reservations are held, and 0 at the
  end. Child admission peak ≤ cap, and the sampled concurrent `xmustard-core` count ≤ cap.
- **Sampled peak only.** The result is a sampled peak. Short spikes between samples can
  be missed, so it is not a universal RSS ceiling. Byte admission bounds what xMustard
  buffers, not process RSS.

## Results

| Run | Core | API bin | Script | Checks | Sampled tree peak | Peak step | Samples / ps errors / lost roots |
| --- | --- | --- | --- | --- | --- | --- | --- |
| **root main, post-import** (fresh build) | `d644743b` | `d2f0ec93` | `6411f391` | **19/19 pass** | **80.6 MB** (76.9 MiB) | two concurrent index clients | 853 / 0 / 0 |
| **root post-opt 1** (fresh build) | `d644743b` | `88e88bac` | `6411f391` | **19/19 pass** | **72.3 MB** (69.0 MiB) | two concurrent index clients | 838 / 0 / 0 |
| **root post-opt 2** (fresh build, same source) | `d644743b` | `88e88bac` | `6411f391` | **19/19 pass** | **84.9 MB** (80.9 MiB) | two concurrent index clients | 840 / 0 / 0 |
| root final, pre-opt (fresh build) | `a23473a6` | `88e88bac` | `6411f391` | 18/19 (**gate FAIL**) | 106.4 MB (101.5 MiB) | two concurrent index clients | 816 / 0 / 0 |
| bench 3 | `a23473a6` | `88e88bac` | `69d0e787` | 18/19 (**gate FAIL**) | 110.5 MB (105.4 MiB) | two concurrent index clients | 826 / 0 / 0 |
| bench 2 | `a23473a6` | `7cdb766e` | `69d0e787` | 19/19 pass | 95.9 MB (91.4 MiB) | cold query | 823 / 0 / 0 |
| bench 1 | `a23473a6` | `7cdb766e` | `065f7cbe` | 19/19 pass | 99.4 MB (94.8 MiB) | two concurrent index clients | 932 / 0 / 0 |

- **Post-opt 1 and 2 provenance:** every provenance field is identical in both reports.
  HEAD `cd13e2b`, source-diff `e0e282e6403c…`, untracked-source digest `61245cae425f…`
  (33 files), API `88e88bac`, shim `3d06d73f`, core `d644743b` (fresh build, not
  prebuilt), `rss_bench.py` `6411f391`, `harness.py` `97314722`, and fixture digest
  `c16734b0…`.
- **Main post-import provenance:** HEAD `cd13e2b`, source-diff `e0e282e6403c…` and
  untracked-source digest `61245cae425f…` (33 files). These are identical to the reviewed
  candidate, as are core `d644743b`, the scripts and the fixture. The core was fresh-built
  in the main checkout. The Go binaries differ (API `d2f0ec93…`, shim `6ae2f34b…` vs
  `88e88bac`/`3d06d73f`). Root attributes this to the build path: the Go binaries embed
  the checkout path, and main builds from `/Users/for_work/Developer/xMustard`. The
  source digests being identical is what ties main to the reviewed source.
- **Review binding:** Fable's final code review (Recheck 2) recomputed the same
  source-diff and untracked-source digests on this worktree. The Fable Rust review (Recheck
  2, approved) identifies the Rust source by recipe hash
  `d039d76da66876af79a9df12e6dc23ef9941a0a846ba3e64a720dad7ae77a358`, whose release binary
  is `d644743ba005…`. That is the core in all three passing runs.
- **Why the result changed:** the candidate API and shim binaries are identical to the
  pre-opt root run. Only the Rust core changed, so the Rust optimization accounts for the
  106.4 MB → 72.3–84.9 MB change.
- **Pre-opt root run provenance:** source-diff `85fa9996…`, untracked `4feafb80…`, and a
  fresh-built core that hashed identically to the prebuilt `a23473a6`.
- **Bench runs 1–3** used earlier revisions of the bench script, from before the expansion
  bounds and failed-workload invalidation were added. The Go source changed between
  runs 2 and 3, so they are not same-source repeats.
- **Pre-correction baseline:** on the old harness (no expansion, no sampler validity
  checks) the peak was 99.3 MB, with capture codes `[200,200,200,200,503]`.

Processes in the sampled peak snapshot (KiB, one `ps` snapshot):

- **post-opt 1:** builder `xmustard-core` 22,336 + waiter `xmustard-core` 6,608 + API
  21,632 + shims 10,432 / 9,600
- **post-opt 2:** API 27,152 + builder `xmustard-core` 22,432 + waiter 13,072 + shims
  10,608 / 9,600
- **main:** API 23,168 + builder `xmustard-core` 22,512 + waiter 13,056 + shims
  10,384 / 9,584
- pre-opt root final: builder 40,752 + waiter 20,512 + API 22,672 + shims 10,480 / 9,520
- bench 3: builder 40,752 + waiter 16,304 + API 30,768 + shims 10,560 / 9,552
- bench 1: exiting builder `(xmustard-core)` 41,904 + waiter 20,576 + API 16,448 + shims
  9,456 / 8,656
- bench 2 (cold query): single `xmustard-core` 48,912 + API 28,400 + shims 9,616 / 6,688

Sampled per-step peaks (MB):

| Step | post-opt 1 | post-opt 2 | main | pre-opt root final |
| --- | --- | --- | --- | --- |
| load | 46.6 | 46.4 | 46.9 | 47.6 |
| index | 54.1 | 53.5 | 54.3 | 54.8 |
| cold query | 67.0 | 77.1 | 70.2 | 95.5 |
| warm query | 51.2 | 57.7 | 53.0 | 67.4 |
| same-size edit | 61.6 | 68.0 | 63.1 | 88.4 |
| rename/delete | 61.3 | 65.8 | 61.2 | 79.9 |
| two concurrent clients | **72.3** | **84.9** | **80.6** | **106.4** |
| captures | 49.9 | 48.1 | 49.5 | 48.0 |
| expansion | 66.3 | 69.5 | 63.4 | 64.7 |
| re-expand | 50.0 | 49.6 | 49.5 | 49.8 |

At the two-client peak, the Rust builder dropped from about 41 MB to 22 MB. The waiter
dropped from 16–21 MB to 6.6–13.1 MB. The shims (about 10 MB each) are unchanged.

The 12.5 MB spread across the passing runs comes from two processes. The API held
21.6–27.2 MB and the waiting core held 6.6–13.1 MB. Those are the two components whose
residency at the sampled instant varies from run to run. With the Rust processes smaller,
the API plus the shims (42–47 MB) now make up most of the peak.

### Admission, capture, storage, expansion (identical in every corrected run)

- Outcomes: `captured ×4`, `wire_503_overloaded ×1`. The refusal was decided in about
  1 ms, with 0 body bytes sent and `Retry-After: 1`.
- Pool while the four reservations were held: `in_use` 67,108,864 of max 67,108,864.
  Final `in_use` 0. Child admission peak 2 of cap 4. Sampled concurrent cores 2.
- Bytes per capture:
  - Raw: 16,777,216. SHA-256 `86612914…ca361`, exact.
  - Projected: 1,561 (`xm-reduce/1`, 1 omission).
  - Delivered capture response: about 2,313.
- Retained storage: 67,111,918–67,111,920 bytes apparent (67,125,248 allocated) in 8 files (4 ×
  `raw.bin` + `meta.json`). The same values held at the sampled storage peak (during
  expansion) and after expansion.
- Expansions: 5 (4 originals plus 1 re-expansion), 1,280 pages, 0 overload retries,
  83,886,080 raw bytes recovered, all with exact size and SHA-256. Page latency p50 was
  about 52 ms on both MCP and HTTP. This is dominated by the per-page `repo-key` Rust
  child that keeps freshness current. One full original takes about 13.4 s.
- Delivered bytes: HTTP counts response-body bytes exactly: 67,496,952 over 3 expansions
  in every passing run. MCP counts a re-serialized estimate, because the shared harness
  does not expose frame wire length: 45,141,034–45,141,290 over 2 expansions.

### Latency, CPU, allocations

Ranges cover the three passing runs (post-opt 1, post-opt 2, main). The pre-opt range
across the root final run and bench runs 1–3 is in brackets.

- Latency: index 9.7 s [9.4–11.0] · cold query 6.9–7.0 s [6.9–7.6] · warm 0.21 s
  [0.20–0.24] · edit 0.34–0.35 s [0.33–0.39] · rename/delete 0.30–0.32 s [0.30–0.36] ·
  two clients 0.37–0.38 s [0.35–0.42] · captures 0.45–0.48 s [0.43–0.79] · expand all
  54.4–56.7 s [54–64].
- Rust work: cold, 501 files parsed in 6,764–6,843 ms · warm hit, 0 parsed, 51–53 ms ·
  edit, 1 parsed, 171–183 ms · rename/delete, 0 parsed, 138–148 ms · concurrent
  clients: one miss with 1 parsed (177–183 ms) and one hit with 0 parsed (207–214 ms).
- CPU (API `getrusage`): self 4.5–4.6 s [4.5–5.3]. Waited children (Rust/git),
  cumulative: 72.2–74.5 s [71.5–79.5].
- Go allocations (API): 1.08–1.09 GB total allocated, 12.95 M mallocs, `HeapSys`
  15.8–20.0 MB [1.09–1.10 GB, about 12.95 M, 16–20 MB].
- OS high-water (bytes): API self 28.1–29.2 MB [27.2–32.3]. Largest single child
  **32.9–33.7 MB** [49.4–50.3].
- **Unmeasured:** Rust core allocations, MCP shim allocations and high-water, per-stage
  byte copies, per-step CPU, and hardware memory bandwidth.

## Retrieval gate and MCP evidence (reviewed source: candidate and main)

`scripts/bench/retrieval-gate.sh` keeps the ≥11/12 top-5 requirement, cold-miss and warm
`files_parsed=0` assertions, explicit coverage losses, and the one-file-edit reparse and
stale-hit checks.

- `gold-provenance-final.json`: **12/12 pass**. Provenance matches post-opt 1: API
  `88e88bac`, shim `3d06d73f`, core `d644743b`, source-diff `e0e282e6…`, untracked
  `61245cae…`, `retrieval_gate.py` `3c183600…`, `gold/queries.json` `134cf7e7…`,
  `harness.py` `97314722…`. Fixture git tree `44a25485…` across 16 files.
- `mcp-provenance-final.json`: **23/23 pass**. Same binaries and source digests.
  `mcp_evidence.py` `5154958c…`, `mcp-evidence.sh` `aab9dd2b…`, `harness.py` `97314722…`.
- `main-gold.json`: **12/12 pass** on the main checkout after import. It has the same
  source digests, core, scripts and fixture tree `44a25485…` as the candidate. The main
  Go binaries are API `d2f0ec93`, shim `6ae2f34b` (the difference is the build path; see
  above).
- `main-mcp.json`: **23/23 pass** on main, with the same source digests, core, scripts
  and main Go binaries.

These reports address review F3. They supersede the pre-opt
`retrieval-gate-root-final.json` (12/12) and `mcp-evidence-root-final.json` (23/23),
which carry no provenance and are kept only as history.

## Open gates and requests (via root; no source edited here)

1. **RSS gate: passed 3/3** on the reviewed source: 72.3 and 84.9 MB on the candidate,
   80.6 MB on main after import. The claim is limited to this fixed workload, as a
   sampled peak on this machine. The spread across runs is about 12.5 MB. Any source or
   script change needs new runs.
2. Rust: the optimization cut the two-client peak from 106.4 MB to 72.3–84.9 MB and the
   cold step from 95.5 MB to 67.0–77.1 MB. There is no open request.
3. Go (frozen): the API held 21.6–27.2 MB at the passing peaks and is now the largest
   single contributor at the peak. It varied from 16 to 31 MB across all runs. This is
   not blocking. It would matter if headroom ever needed to grow.
4. Pi: the main post-import Pi run uses the current core `d644743b`. The two earlier Pi
   runs used the pre-opt core `a23473a6`.
5. Fixture: the Rust third still lacks `;`, as described above. A valid-source
   supplemental fixture is not part of this gate.
6. Harness (optional): expose MCP frame byte length so MCP delivered bytes are exact
   rather than an estimate.
7. Human review of the exact final diff is required before any merge. No commit or merge
   has been made from this workstream. The code freeze continues. The next diagnostics
   plan is separate and outside this gate.

## Evidence

The raw machine-readable reports, including the per-sample series, are in
`docs/benchmarks/evidence/2026-09-24/`. Root allowlisted them explicitly in `.gitignore`.

| File | Run | sha256 of the file as stored |
| --- | --- | --- |
| `main-rss.json` | root main post-import, 19/19, 80.6 MB | `d8ba4d20722999d6205859ab5ba295fca3462b5fcb44e280d3141660d23573e4` ¹ |
| `main-gold.json` | gold 12/12, main | `7ce0092a93d41b0cf35f640066fa44f49270e931e45414ce2d87aaa63022e9d8` ¹ |
| `main-mcp.json` | MCP 23/23, main | `e7cc5499469a879e8fbf6d86dd90131c7c441b95e87effea0f5c24dabfacf941` ¹ |
| `pi-e2e-summary-main.json` | Pi, main post-import | `be1d295e890fb9578db3a20fb53e00c247ab7f3ad2b113dde007effcc61daab3` |
| `rss-postopt1.json` | root post-opt 1, 19/19, 72.3 MB | `07dfd35d67514addce29784dab7222a1d206999e52bb4ecdd94a2bcde7dcd48a` ¹ |
| `rss-postopt2.json` | root post-opt 2, 19/19, 84.9 MB | `26206f9ecbaf28423474ef88233de7164249e162f04a48ed854e5590b4448710` ¹ |
| `gold-provenance-final.json` | gold 12/12, post-opt revision | `d25fd5e469807fc3cba1f60b4bee6c62757802abd9762642612affe109be4cd6` ¹ |
| `mcp-provenance-final.json` | MCP 23/23, post-opt revision | `98f557fd3d7ca9225c0e2dac129c360cd618ac833c8f508fa14109e5d624ba70` ¹ |
| `rss-root-final1.json` | root pre-opt, gate FAIL 106.4 MB | `c67bcc936eae52b16a168073a12ea5aa13a6c468b488c2e9c1f7b19d53b69f74` |
| `retrieval-gate-root-final.json` | gold 12/12, pre-opt, no provenance | `1756d52b19d6b6dcc2f28b2ea50eb471572201b61331af6b0b18c0d54dbb3ead` |
| `mcp-evidence-root-final.json` | MCP 23/23, pre-opt, no provenance | `e977f5e8ad7ebc2f9418a47f45aab9b716fdbf1644854b6d6284a50337d3e4cf` |
| `rss-corrected-run1.json` | bench 1 | `e0d3e83c0d0702b12096bb3b2cb6285633067de0c926d842300630391e26a290` |
| `rss-corrected-run2.json` | bench 2 | `b31e7a414cb568c02275a84b1ff45ee32e1cc9c686f034db7b6b72c79329738a` |
| `rss-corrected-run3.json` | bench 3 | `6680df8be2f98e685fe2f856ee4c5740cb2228ee806e5461227ced243f61de85` |
| `rss-pre-correction-baseline.json` | old harness | `2716f283d97566c5c5b39c4b9e5746e9fa8bc1a31018501670a49c186dcea044` |
| `pi-e2e-summary-gnVCwq.json` | Pi, root independent | `e32698b68ce057216cd03c0f0f53a1ee7962549685f0b7041f3a8159f6a29870` |
| `pi-e2e-summary-WI7Omh.json` | Pi, root final source | `c26dbd8e207b33e6bf85fcf6b86fb020e866b72f9eab8032ee5a20e36a5c8d58` |

¹ **Normalization disclosure.** Root curated these seven files, and each stored copy has
one trailing LF appended. The original reports ended without one. The JSON content is
identical, but the sha256 given is for the stored, curated file, so it differs from the
original report's file hash. Root also placed the four `main-*`/`pi-e2e-summary-main`
files in the main checkout's matching evidence folder.

The other files are byte-identical to their originals:

- the `rss-root-final1`, `retrieval-gate-root-final` and `mcp-evidence-root-final`
  files are `cp` copies of root's `/private/tmp/xmustard-root-checks.wEhHWd/{rss-final1,gold-final,mcp-final}.json`
- the `pi-e2e-summary-*` files come from the paths in the Pi table below. Their
  originals already end in LF. `pi-e2e-summary-main.json` was checked byte-for-byte
  against its temp original: they are identical, so no extra LF was needed.

Root checked the Pi copies and found no credential keys or literal secrets.

## Separate: Pi / external workflow

These runs are not the fixed gate. They come from `scripts/e2e/pi-adapter.sh`, a smaller
conformance workload. The first two ran on the pre-opt core `a23473a6`; the main run
used `d644743b`. Each value is a sampled maximum over 100 ms `ps` ticks, in MiB, taken
from the `resources` field of that run's summary. Every run below passed 17 unit and 15
e2e tests, with all nine tools succeeding and the native Postgres fixture in use. The
one `isError: true` entry in each summary, `why_failed!`, is the deliberate negative
case: a missing run must stay an error.

| Run | Summary (exact path) | sha256 | Samples / skipped / failures | xMustard-owned | Pi external | Postgres fixture | Full workflow |
| --- | --- | --- | --- | --- | --- | --- | --- |
| root independent (16:37) | `/var/folders/rv/59993tb901gcfy8q0qdmynvm0000gn/T/xm-pi-e2e.gnVCwq/e2e/summary.json` | `e32698b68ce057216cd03c0f0f53a1ee7962549685f0b7041f3a8159f6a29870` | 214 / 0 / 0 | 71.0 | 248.7 | 55.0 | 365.5 |
| root, pre-optimization core (16:51) | `/var/folders/rv/59993tb901gcfy8q0qdmynvm0000gn/T/xm-pi-e2e.WI7Omh/e2e/summary.json` | `c26dbd8e207b33e6bf85fcf6b86fb020e866b72f9eab8032ee5a20e36a5c8d58` | 222 / 0 / 0 | 66.6 | 210.3 | 45.5 | 310.4 |
| main post-import (17:15) | `docs/benchmarks/evidence/2026-09-24/pi-e2e-summary-main.json` (curated copy; one trailing LF added) | `be1d295e890fb9578db3a20fb53e00c247ab7f3ad2b113dde007effcc61daab3` | 222 / 1 / 0 | 68.8 | 263.6 | 41.3 | 366.9 |

In every run, every peak occurred in the two-concurrent-Pi-process phase except the
Postgres fixture's. That one peaked during conformance in the first two runs and during
setup in the main run. The main run's one skipped tick was a `ps` call still running at
the next 100 ms boundary; it is not a sampling failure. See
`docs/reviews/2026-09-24-pi-implementation-results.md`.
