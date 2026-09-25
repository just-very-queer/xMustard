# Current status — 2026-09-24

**Baseline assessment:** substantial working foundation; not yet a demonstrated
reliable, resource-bounded competitor-parity product. The core memory loop works
in a live small fixture. The September audit findings below describe that
baseline; see candidate disposition before treating them as current source truth.

## Implementation candidate — imported, uncommitted, verification complete

The reviewed candidate is now imported into the main working tree, remains
uncommitted, and is not shipped. It repairs selected audited Go/Rust contracts,
adds bounded recoverable delivery for xMustard's own MCP surface, and adds one
pinned Pi adapter. Fable's final source review is approved. All current backend,
MCP, retrieval, Pi, and fixed-workload RSS gates pass on the imported candidate.
The remaining decision is human review of the exact diff and merge; no automatic
merge or repository-enforced human gate is claimed.

| Candidate evidence | Result | What it does not establish |
| --- | --- | --- |
| Backend tests/build and Rust checks | Main-worktree `make check-backend` passes: all 7 Go packages, 148 Rust tests, Clippy exits 0 with 11 existing warnings; uncached Go suite also passes all 7 packages | No warning-clean claim; does not establish broad language/repository coverage or task-quality parity |
| MCP evidence E2E | Main-worktree provenance-bound run: 23/23 checks pass, including bounded recall, exact recovery, authorization, expiry, cancellation and unchanged URI after restart; focused evidence and shim race checks also pass separately | Scoped to these MCP contracts; no other client/gateway parity implied |
| Retrieval fixture | Main-worktree provenance-bound gold gate: 12/12 across cold/warm/edit; spans 10/10 | Lexical-leaning regression only—not held-out localization quality, task success, or competitor parity |
| Rust index review | Fable Recheck 2 approved Rust source hash `d039d76d…7a358`; imported current core is `d644743b…` | Review/tests do not establish broader retrieval quality or task-quality parity |
| Pi adapter | Main-worktree run on current Rust core `d644743b…`: 17 unit + 15 actual pinned-runtime checks; all nine tools pass using disposable native Postgres fixture for diagnostics | Root run record binds main binaries; the Pi summary itself has no binary hashes. One adapter only; fixture is not a default DB dependency or SQL-correctness proof; no native host-tool or universal client claim |
| Sampled process-tree RSS | Main run passes 19/19 at 80,592,896 B (80.6 MB), 853 valid samples. Two same-source/script/fixture candidate runs pass 19/19 at 72.3 and 84.9 MB. Each fully recovered four 16 MiB originals plus a repeat (1,280 pages) and exercised a real 503 refusal | Fixed workload on one machine; sampled peaks are not a universal RSS ceiling or hardware-bandwidth/task-quality proof |

The main RSS/MCP/gold JSONs carry the matching Go/Rust source and untracked-source
digests (`e0e282e6…`, `61245cae…`). Their script and fixture provenance also
matches the two isolated runs. The main Go binaries have different hashes because
the build path is embedded. The Pi summary does not carry executable hashes; the
root invocation record binds its run to the main API/MCP binaries and current core
(see the final [Fable source review](reviews/2026-09-24-fable-code-review.md)).

Detailed scope: [Go report](reviews/2026-09-24-implementation-results.md),
[Rust report](reviews/2026-09-24-rust-implementation-results.md),
[Pi report](reviews/2026-09-24-pi-implementation-results.md), and
[resource benchmark](benchmarks/2026-09-24-lean-context.md). Current main-worktree
[RSS](benchmarks/evidence/2026-09-24/main-rss.json),
[MCP](benchmarks/evidence/2026-09-24/main-mcp.json),
[retrieval](benchmarks/evidence/2026-09-24/main-gold.json), and
[Pi E2E](benchmarks/evidence/2026-09-24/pi-e2e-summary-main.json) records are
linked here; only the first three JSONs carry source/build provenance. Their
Go/Rust source-diff and untracked-source digests match the reviewed candidate
(`e0e282e6…`, `61245cae…`). The Pi binary binding is documented in the root run
record cited above.

All required checks for this imported candidate tranche are green. A separate
database-free diagnostics plan is an unimplemented next-stage proposal; Fable
review returned `CHANGES_REQUIRED`. It is not an acceptance gate for this tranche.
Held-out task quality, broad
competitor parity, hardware memory bandwidth, and universal RSS claims remain
unproven.

Reviewed implementation: `cd13e2b78a9fadcbcd760b19442d5992136b1db0`, local branch
`feat/product-v1`. The worktree was clean at review start. At that baseline review,
the source defects listed below remained open; candidate disposition follows.

## Product direction

The owner confirmed shared, verified memory and repository intelligence for
existing coding agents, current competitor comparisons across both categories,
and a local/no-Docker default targeting roughly 50–100 MB. UI is out of scope.
The subsequent clarification includes concurrent/cross-session/cross-repo work,
tool-context compression, investigation of a Needle-like local helper, and human
final code-merge authority. See [vision](VISION.md), the proposed
[context layer](CONTEXT_LAYER.md), and the
[current parity review](research/COMPETITOR_PARITY_2026-09-24.md).

The candidate implements only recoverable projection on xMustard's own MCP path
and one Pi extension. An upstream gateway, other native-client result hooks,
helper-model integration, and human-specific merge enforcement are **not
implemented**. Existing memory promotion counts peer votes; run-plan approval
starts work and does not establish human approval of a Git merge. The owner
clarified that "cheaper than OpenHands" refers to a lighter harness, context
shedding and better indexing, not API-price reductions. Comparative harness RSS,
allocation/copy overhead and hardware memory bandwidth remain unmeasured.

## Live GitHub state

Checked through GitHub and remote refs on 2026-09-24; local tracking refs were not
treated as current remote truth.

| Item | Observed state |
| --- | --- |
| Public repository | [just-very-queer/xMustard](https://github.com/just-very-queer/xMustard), MIT, default branch `main` |
| Public main | [`2c312338`](https://github.com/just-very-queer/xMustard/commit/2c312338aeddf829c811c606481b6edff652dd10), PR #7 merged June 22 |
| Remote feature | [`3f28eef7`](https://github.com/just-very-queer/xMustard/commit/3f28eef72f373d62e69001fd0170c571ae1fc577) |
| Local development | `cd13e2b`, **21 commits ahead of remote feature, zero behind** |
| Feature vs main | Remote feature 18 ahead / 3 behind; the three main-only commits are merges #4, #5, #7. Local has 39 feature-line commits after their common ancestor. [Comparison](https://github.com/just-very-queer/xMustard/compare/main...feat/product-v1) |
| CI / releases | No Actions workflows or runs; no tags or releases |
| Work tracking | No issues; [PR #3](https://github.com/just-very-queer/xMustard/pull/3) is open, conflicting, with no checks |

At review start the local and published README were byte-identical, despite
the local code being ahead. The published overview therefore still contains
unqualified completion claims and contradictory migration language. This review
did not push, merge, change repository settings, or modify that PR.

## Fresh verification (baseline review)

| Check | Result and limit |
| --- | --- |
| Go build | All six packages pass |
| Go tests | All six packages pass: 277 top-level tests passed, 3 initially skipped. Fresh Rust parity rerun passed; **278/280 exercised**, two live Postgres tests remain skipped |
| Rust tests | **114 passed**, no failures or ignored tests, default features |
| Rust binary build | Passed; fresh debug core used for integration checks |
| Clippy | Exit 0, **12 warnings**; not warning-clean |
| Isolated HTTP + stdio MCP | **18 checks passed**, including authenticated propose/verify/recall and drift after edit |
| Optional frontend | Build passes; lint fails with four hook errors. Recorded baseline only; UI changes excluded |
| Reorganization checks | Curated Markdown links resolve; historical document bodies preserved; diff whitespace check and formula Ruby syntax pass; Make recipe dry runs and missing-root rejection verified |

Details: [Go audit](reviews/2026-09-24-go-audit.md),
[Rust audit](reviews/2026-09-24-rust-audit.md), and
[live smoke](reviews/2026-09-24-runtime-smoke.md).

The frontend lint errors are in `AdminPanel.tsx:65,147,209` and
`MemoryPanel.tsx:38`. No browser workflow was tested. The UI also lacks bearer
token plumbing and assumes the full API rather than core-only mode; it is not a
release-ready authenticated frontend.

## Highest-priority baseline findings

| Finding | Evidence level | Effect |
| --- | --- | --- |
| Repeated unstaged edits reuse a stale symbol graph | Reproduced in an isolated repository | Search can return a removed symbol as current; Git porcelain whitespace is trimmed before fixed-column parsing |
| Graph indexes only 32 symbols per file without reporting truncation | Reproduced with a 70-function file | Search/impact omit real code while file coverage says complete |
| Plain `recall` bypasses the bounded ranking path | Source-confirmed route/control flow | Reads and drift-checks all promoted entries instead of top-N |
| Main returns before the shutdown goroutine completes | Source-confirmed lifecycle | Run interruption, child cleanup, and Postgres flush can be cut short |
| Non-loopback auth interlock admits `AUTH=off` when tokens exist | Source-confirmed condition mismatch | Supported remote-bind configurations can disable authentication |
| MCP cancellation stops the HTTP client but is not threaded through core handlers | Source-confirmed call chain | Rust work can continue after a tool is cancelled |
| Shared transient-byte accounting is not enforced on all work | Source-confirmed paths | At the audit revision the 50–100 MB target was unproven; unconditional charges and uncharged calls allowed excess work |

These rows are findings against the reviewed baseline commit, not a fresh list of
known defects in the candidate. The candidate addresses several of them; exact
scope and regression evidence are in the implementation reports. The candidate
does not establish that every route is covered or that enforced byte admission
forms a hard RSS ceiling. The three passing sampled runs include original recovery
expansions and the measured xMustard-owned process tree, but establish only that
fixed workload on that machine. They do not establish other workload performance
or hardware bandwidth.

The baseline metadata/content race in ranked recall under concurrent edits was
reproduced; the candidate fixes it with a regression test. The retained goal
runtime still loses acknowledged creates under concurrency and accepts
failed-test evidence for completion; both are reproduced, deferred
secondary-platform defects.

## Strategic and delivery gaps

- The memory harness implements paired statistical analysis, **not** a task
  executor, held-out corpus, or measured solve-rate benefit.
- Default retrieval uses lexical/hash-based signals and a mostly lexical graph.
  Optional ONNX inference does not establish a persisted neural vector index;
  its heavy feature was not built or benchmarked in this review.
- Language coverage, cross-repo behavior, real client compatibility, and
  comparable retrieval quality need explicit parity tests.
- The baseline audit did not complete a sustained process-tree RSS test or live
  Postgres verification. The imported candidate's fixed-workload RSS gate now
  passes; the Pi diagnostics E2E uses a disposable native Postgres fixture.
  Optional Postgres mirror correctness remains a separate deferred gate; this
  does not establish general SQL correctness.
- Homebrew instructions did not constitute a working release path: the formula
  is HEAD-only and the named public tap was not found. Installed binaries do not
  include the SQL asset required by the optional Postgres bootstrap path.
- Existing tracked runtime data needs a separate hygiene review. This pass
  preserved the records and did not remove data or rewrite Git history.

## Reorganization completed in this review

- Established [one documentation entrypoint](README.md), current vision,
  implementation map, status, and [ordered backlog](ROADMAP.md).
- Preserved previous architecture/status/roadmap under `docs/history/` and
  indexed available conversations, decisions, and unavailable transcript gaps.
- Aligned README, AGENTS, and OpenHands guidance with Go/Rust and the confirmed
  agent-facing focus; removed obsolete Python checks and global completion claims.
- Allowlisted the curated documentation set while leaving raw sessions and
  private working material ignored.
- Added phony Make targets so backend/frontend startup recipes execute; repaired
  the `scan` CLI invocation and added backend/frontend check entrypoints.
- Corrected the packaging caveat from eight to nine tools; packaging remains
  a development starting point, not a verified release.

Code-module directories and persisted data paths remain stable. Their real
coupling needs behavior-level repairs and deliberate extraction, not a bulk move.

## Review coverage

The review inventoried the active Go, Rust, frontend, build and SQL surfaces and
inspected their principal contracts and implementations. Detailed correctness
review concentrated on the agent-facing paths and shared runtime/storage seams;
this is not a claim of formal line-by-line verification of all source and tests.

All **62 available archived project conversations** were read at the conversational
message level (995 substantive messages). Two indexed older Codex transcripts
and the June Claude conversation are unavailable locally. All tool payloads were
parsed where present, but not exhaustively re-audited. See the
[history index](history/README.md) for exact scope and provenance.
