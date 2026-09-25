# Diagnostics live-set candidate: stop/go record (2026-09-25)

Scope: isolated worktree `/private/tmp/xmustard-opus-l9b1F4` only. The main
checkout was not changed, merged, committed, or pushed. No Claude review was
used for this continuation. The proposal remains experimental and is **not
accepted for import**.

The prepared local API GET retains the inline raw payload and v2 envelope,
validates before output, rereads the same descriptor, streams rows, and limits
active baseline reads to two. The PostgreSQL adapter keeps the existing
materialized response. The CLI and status route remain on their prior paths.
New local envelopes put Base64 before stably sorted rows; legacy envelopes
fall back to the bounded reader.

Evidence:

- The full Go suite passed after reverting an experimental raw-row forwarding
  shortcut. Earlier in this isolated worktree, `make check-backend` passed,
  including Rust tests and Clippy; that full gate predates the last revert.
- The local no-Postgres E2E passed 16/16 with 488 API GETs and no sampled
  resource failure: [local-diagnostics-own-scope.json](local-diagnostics-own-scope.json).
- One unchanged fixed RSS workload passed 20/20, sampled process-tree peak
  95,682,560 bytes against the 100,000,000-byte limit. This is only a
  preliminary single run, not the required five-run acceptance series:
  [diagnostics-stream-preliminary-rss.json](diagnostics-stream-preliminary-rss.json).
- The preloaded two-reader diagnostic A/B on the safe row-encoding candidate
  completed A `[142,134,140,131,129]` versus B `[106,102,100,98,97]`
  successful reads in five paired trials. Median B/A was 100/134 = 74.6%,
  below the required 95%. All reported responses were HTTP 200. B's API RSS
  was lower, but throughput materially regressed:
  [preloaded-ab/api_read_rss.json](preloaded-ab/api_read_rss.json).
- A temporary raw-row forwarding shortcut reduced the microbenchmark from
  roughly 55 ms to 51 ms per GET, but its paired diagnostic A/B still missed
  the throughput bar in all five trials (A `[136,90,95,84,82]`, B
  `[87,76,78,57,68]`). This shortcut was reverted because forwarding stored
  bytes does not preserve byte parity for every valid noncanonical envelope:
  [preloaded-ab-raw/api_read_rss.json](preloaded-ab-raw/api_read_rss.json).

The A/B driver is diagnostic, not an acceptance gate: it records successful
GET counts and status codes but does not independently attest per-reader
liveness or baseline versus no-baseline completion. System load also varied
across its second series. Neither limitation explains away the large and
repeated throughput shortfall. The 95% throughput condition therefore remains
unmet, so the planned five-run fixed RSS acceptance series and promotion were
not attempted.

Other focused acceptance items remain open: exhaustive corruption and
post-header abort cases, PostgreSQL byte parity, maximum legal escaped-input
live set, concurrent two-read-plus-import admission, and Pi propagation. A
configured transient pool below 24 MiB currently returns a non-retryable GET
error instead of rejecting API startup; startup rejection conflicted with
existing low-pool tests for unrelated endpoints. This deviation needs a
decision before any import.
