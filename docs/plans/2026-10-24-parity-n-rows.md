# WS-N row specifications — 2026-10-24

Scope: specifications for every WS-N1–N34 proposal in the two 2026-10-03 research reports. Status below is based on this worktree’s current source, not on a branch name or historical claim. “Implemented” means the proposed contract is present; related machinery alone is not completion. The nine MCP tools remain the public tool set; deepen `search`, `impact`, `recall`, `remember`, `verify` and existing service/CLI surfaces rather than adding a tool (repository `AGENTS.md`, “What This Repo Is For”).

Shared gate vocabulary: **Go gate** = `cd api-go && go vet ./... && go test -count=1 ./...`; **Rust gate** = `cd rust-core && cargo test && cargo clippy --all-targets`; **retrieval gate** = `make bench-retrieval`; **budget gate** = `make bench-gate`. For docs-only rows, acceptance is content/review validation, not a code test. The existing process-tree target is approximately 50–100 MB; no row below claims new measurements. `bench-gate` is owed whenever persistent allocations, resident processes, or process count can change; otherwise document why not.

## Engine — report 2026-10-03 §3.1

### WS-N1 — Byte-exact output accounting (S, Rust+Go)

**Source:** `research-2026-10-03/REPORT.md` §3.1. **Status:** Partly. Request/response memory admission and result caps exist (`api-go/internal/mcpserver/backend.go:122-132`, `api-go/internal/mcpserver/server.go:50-63`); tool `max_chars` and output contracts exist (`api-go/internal/mcpserver/tool_impact.go`, `api-go/internal/mcpserver/tool_search.go`). No evidence found for budgeting the serialized full envelope including arguments, re-budget idempotence, or persisted per-session `bytes_removed`/`bytes_delivered` histograms.

**Contract:** Keep each existing tool’s arguments and serialized result envelope within its declared wire budget, including JSON escaping and protocol wrapper. Truncate only at deterministic boundaries; repeated budgeting is a no-op. Record per-session removed/delivered byte counts. Implement at the Go MCP result/admission boundary (`mcpserver` server/transport and session store), with Rust outputs measured as serialized rather than in-memory structs. Overflow/never-fit stays a bounded refusal, not a partial success.

**Compatibility:** **OWNER DECISION.** Adding output members or truncating results changes wire behavior. Preserve current limits/fields initially; make histograms internal, and gate any wire metadata behind explicit opt-in/config until versioned. Never raise schema caps to pay for it.

**Acceptance:** Escaping-heavy JSON stays within exact encoded budget; re-budgeting a truncated envelope is byte-identical; 10k-row impact serialization differs from charged count by zero; per-session counters survive readback and do not leak arguments/secrets. Go gate; Rust gate for Rust serialization; budget gate owed if retained histograms affect resident memory (measure first).

### WS-N2 — Freshness `regressed` state (S, Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.1. **Status:** Partly. Freshness currently emits `current|behind|diverged|unknown`; `ahead_behind` uses `git rev-list --left-right --count indexed...head` (`rust-core/src/index/envelope.rs:32-38,96-109`). The `Diverged` arm includes rollback, so it does not distinguish regressed. Commit relation is cached with TTL/watcher epoch (`envelope.rs:7-10,47-58`); branch-name cache identity is not used for index scope (`rust-core/src/indexcache.rs:25-28,1309-1319`).

**Contract:** Derive `regressed` only when indexed commit is not reachable from HEAD but HEAD is an ancestor of indexed commit; retain `diverged` for neither ancestor, `behind` for indexed ancestor, `current` for equality, `unknown` for Git failure/unborn ambiguity. Use one left/right count and preserve SHA-based cache binding. Implement enum/relation in `index/envelope.rs` and every consumer/schema in `index/reader.rs`, `index/mod.rs`, `search.rs`, Go bridge and MCP docs.

**Compatibility:** **OWNER DECISION.** New serialized enum value can break exhaustive clients. Recommend emit legacy `diverged` by default and expose `regressed` only through a negotiated/versioned freshness field until clients are audited; alternatively version the contract.

**Acceptance:** Forced reset makes indexed-ahead/HEAD-ancestor fixture `regressed`, not `current`; true divergence remains `diverged`; cache binding follows commit identity and invalidates on Git state. Rust gate; retrieval gate if freshness affects ranking; budget gate not owed (bounded Git work already has a 5 s child timeout, `envelope.rs:22-25`).

### WS-N3 — `unmapped_files` and unresolvable-target nulls (S, Go+Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.1. **Status:** Partly. Impact already has epistemic status and `graph: none` for no target, but `impacted_count` is a non-null `usize` (`rust-core/src/index/impact.rs:829-840`); Go `ImpactReport` has no `unmapped_files` member (`api-go/internal/workspaceops/workspace_reads.go:196-207`).

**Contract:** Existing `impact` accepts symbol or diff scope (`api-go/internal/mcpserver/tool_impact.go`); unresolved name-only target returns `impactedCount: null`, `risk: UNKNOWN`, with unresolved-target reason, not zero. Include changed source paths lacking mapped symbols in `unmapped_files`, ordered and deduplicated. Add nullable typed field in Rust impact output (`index/impact.rs`), translate through `workspaceops.ReadImpact`/`readRustSemanticImpact` (`workspace_reads.go:315,994`) and advertise via existing `impact` schema. Preserve evidence that a true empty resolved graph differs from unknown mapping.

**Compatibility:** **OWNER DECISION.** Null replaces numeric zero, and additive field changes response schema. Recommend additive `unmapped_files` immediately if schema cap permits; gate null/UNKNOWN semantics behind versioned/opt-in impact contract, defaulting legacy clients to current value until migration.

**Acceptance:** Name-only absent symbol serializes null/UNKNOWN; a changed unmapped fixture reports path; a resolved node with no edges remains a known zero. Go and Rust gates; retrieval gate; budget gate not owed unless response-size growth exceeds existing bounded result cap (measure).

### WS-N4 — Deterministic identity scheme (M, Go+Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.1. **Status:** Not implemented as proposed. Review anchors exist (`api-go/internal/workspaceops/review_anchor.go`) and symbol UIDs exist (`rust-core/src/index/uid.rs`), but no one versioned FNV-style identity helper shared by review and memory was located. N1/N3 may be in progress on `parity/ap-parity-n-a`; verify before implementation.

**Contract:** Define one versioned byte encoding `[version, kind, parts…]`, `0xff` separators, hash before filtering, no line/column. Review finding identity uses semantic finding kind/path/anchor parts; memory identity uses explicit stable claim identity parts, never mutable content or line number unless explicitly chosen. Implement shared, documented encoding at suitable Go/Rust boundary and migrate consumers without conflating vocabulary.

**Compatibility:** **OWNER DECISION.** Re-keying persisted findings/memories risks duplicate/lost references. Recommend dual-read old IDs, write versioned IDs for new records, and migrate only through explicit transaction with alias preservation; owner must decide one shared function vs shared primitive/two domain wrappers.

**Acceptance:** Line shift preserves ID; rename changes it; `[ab,c] != [a,bc]`; scheme version namespace cannot collide; property/fixture tests in Go and Rust. Go and Rust gates. Budget gate not owed (small deterministic hash, no retained cache).

### WS-N5 — Version-pinned index cache manifests (S, Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.1. **Status:** Partly. Index format and analyzer versions are persisted and checked (`rust-core/src/indexcache.rs:47,64`; `rust-core/src/index/mod.rs:967`); stats report schema/analyzer version (`index/mod.rs:1865`). A separate manifest version contract and explicit semantic-change bump rationale were not found.

**Contract:** Every reusable index/cache artifact records manifest schema and semantic producer version. Refuse absent/unknown/incompatible versions and rebuild; never deserialize/replay unversioned cache as current. Add central version constant/rationale and route every cache read through validation (`indexcache.rs`, `index/mod.rs`, hash/graph cache owners). Error is typed as cache miss/rebuild reason, not silent partial index.

**Compatibility:** **OWNER DECISION.** Existing cache deletion/rebuild increases first-query latency and transient CPU. Preserve current cache where version can be verified; invalidate only unverifiable or incompatible entries, with explicit rebuild status, not a new opt-in required for correctness.

**Acceptance:** Old manifest and absent-version fixtures are rejected/rebuilt; unchanged version reuses cache; semantic version bump forces rebuild. Rust gate; retrieval gate. Budget gate owed only if manifest persistence changes cache memory/size materially; measure cache disk and build peak.

## Memory — report 2026-10-03 §3.2

### WS-N6 — Governance CAS with a fencing token (M, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.2. **Status:** Partly. Governance mutations are transactional and serialized in SQLite (`api-go/internal/govstore/store.go`, `entries.go`); concurrency tests cover concurrent propose/verify (`govstore/concurrency_test.go:300`). No lease/fencing-token protocol tied to process liveness was identified.

**Contract:** Every propose/verify/promote/supersede/retract mutation carries monotonically increasing fencing token; transaction checks token and expected revision before transition. Lease expiry alone never authorizes takeover: reclaim only after holder process is proven gone; late holder’s token is rejected and only its own lease can be cleared. Implement in govstore transaction APIs (`entries.go`, lifecycle transition owners), with audit record for winner/refusal.

**Compatibility:** **OWNER DECISION.** Internal storage changes; external behavior becomes more conflict-prone under concurrent writes. Recommend transparent serialization for single-process clients, typed 409 for stale token, no new required client field (server generates token).

**Acceptance:** Dead writer lease reclaimed; late old-token write refused; concurrent promote/supersede gives one audited winner; cancellation/lock tests ensure no false reclaim. Go gate. Budget gate owed if lock service/process is resident; prefer SQLite/process-local bounded state and measure, no new process.

### WS-N7 — Echo-proof recall corpus (S, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.2. **Status:** Not implemented as proposed. Recall ranking/corpus exists (`api-go/internal/workspaceops/memory_recall.go:578`, `memory_rank.go`); session/run filtering exists but no caller-echo exclusion from IDF corpus was identified.

**Contract:** On `recall`, derive session from authenticated context, reduce score for that session’s prior query echoes, and exclude those echoes from corpus document-frequency computation. Do not accept caller-supplied principal/session as authority. Implement at `rankRecall` and IDF source (`memory_recall.go`, govstore ranking/query methods), with bounded session history and existing deterministic tie-breaks.

**Compatibility:** **OWNER DECISION.** Changes ranking for repeated searches. Recommend opt-in per workspace or new ranking mode initially; default existing ranking remains until offline retrieval eval proves no regressions.

**Acceptance:** A session repeating same query 20 times ranks true matches identically to fresh session; another session’s corpus stays unchanged; no session key means current behavior. Go gate and retrieval gate. Budget gate not owed if query history is bounded/persisted in existing store; if new retained corpus/index, bench-gate is owed.

### WS-N8 — Required scope, deny-by-default (S, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.2. **Status:** Partly. Writes derive scope from authenticated principal/request; unknown scope or missing narrower key errors (`api-go/internal/workspaceops/memory_scopes.go:43-51,66-82,113-127`). However empty visibility currently defaults to workspace scope (`memory_scopes.go:66-83`), and workspace reads are intentionally broad. Scoped visibility has server-side rules (`memory_scopes.go:133-189`).

**Contract:** Existing `remember` write surface (`mcpserver/tool_remember.go`) derives required scope server-side from principal; no client payload may select an arbitrary scope key. Every scoped `recall` read must bind storage query to authenticated workspace/principal and requested scope; explicit applicability required for cross-workspace results. Missing scope on a scoped operation and unsupported scope are 4xx/refusal, never fallback to default store. Enforce in `DeriveScoped`, `RecallRequest.validate`, and govstore selection.

**Compatibility:** **OWNER DECISION.** Making currently omitted write visibility invalid breaks clients (presently defaults workspace). Recommend maintain omitted visibility as legacy workspace scope for existing `remember` clients; require explicit scope only for new scoped-read/cross-workspace modes, with migration/major-version switch before flipping writes.

**Acceptance:** Missing scope refusal on new scoped operations; cross-workspace query returns only matching applicability; accepted filter cannot bind default workspace store. Go gate. Budget gate not owed (no new process/cache); review security gate/SECURITY.md alignment.

### WS-N9 — Unsupported operator is a hard error (S, Rust+Go)

**Source:** `research-2026-10-03/REPORT.md` §3.2. **Status:** Partly. Existing typed search modes and validation are closed (`api-go/internal/mcpserver/tool_search.go:20-45`); there is no general filter-operator/distance-metric contract found in current recall/search APIs (`rust-core/src/search.rs`, `memory_recall.go`).

**Contract:** For any future typed filter, parse an exhaustive operator enum (`eq|ne|in|nin`) and metric enum before storage/query work. Unknown operator/metric returns structured invalid-input 4xx; never drop filter or fall back unfiltered. Score mapping must be monotonic and metric-specific. Surface through existing `search`/`recall` arguments, not a new tool.

**Compatibility:** **OWNER DECISION.** Rejecting a value previously ignored may break callers, but silently ignoring filters is unsafe. Recommend reject unknown values from first release of any new argument; retain old behavior only for legacy requests that do not send that argument.

**Acceptance:** All four supported operators filter correctly; unknown operator is 4xx; metric property tests establish monotonicity and invalid values error. Go and Rust gates. Budget gate not owed (pure query validation).

## Deterministic review / issue-resolution — report 2026-10-03 §3.3

### WS-N10 — Gate-certified review coverage (M, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.3. **Status:** Partly. Closed coverage JSON type validates fields/path (`api-go/internal/review/coverage.go:8-36`), and review records bind change/finding lineage (`workspaceops/review_record.go:113-180`). The research requirement that every changed file is in a denominator and `clean` impossible while any is not-reviewed is not established by this parser alone.

**Contract:** Review request binds to resolved diff against first parent for merge commits; denominator includes all changed files. Return `not_reviewed[]` with closed reason enum for unreadable/oversized/excluded; verdict `clean` only when empty, otherwise incomplete (or failed for execution failure). Implement in review orchestrator/submission reconciliation and `review.Batch`, not solely decode (`api-go/internal/review/coverage.go`, `workspaceops/review_record.go`, `merge_approval.go`).

**Compatibility:** **OWNER DECISION.** New `incomplete` terminal state and stricter `clean` meaning affect consumers. Recommend add `coverage_complete`/`not_reviewed` while preserving existing verdict until API version transition; never report false clean in new flow.

**Acceptance:** unreadable/oversized file appears with reason and cannot yield clean; merge diff uses first parent; every changed path accounted exactly once. Go gate; budget gate not owed (bounded diff/review data; any stored coverage retention measured as part existing store budget).

### WS-N11 — Deterministic chunk retry (M, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.3. **Status:** Not implemented as proposal. Review parser/records exist, but no chunk retry API or typed “cannot fit” outcome found (`api-go/internal/review`, `workspaceops/review_record.go`).

**Contract:** Existing review execution partitions work deterministically; retry only transient/fallback-eligible chunk failures under fixed attempt cap and backoff; non-fit is distinct terminal error, never retried. Persist/publish successful sibling chunks, list failed chunk IDs/reasons; overall run cannot be clean while any chunk failed.

**Compatibility:** **OWNER DECISION.** Partial findings and run status are additive. Recommend do not retry existing synchronous API by default; expose explicit retry policy on new review-run orchestration, preserve successful results in existing response.

**Acceptance:** One transient chunk succeeds on bounded retry; non-fit is attempted once; sibling success published beside failure list; no unbounded loop, cancellation honored. Go gate; budget gate owed if parallel workers/processes are added (prefer sequential bounded retry and bench-gate only if allocation/process count moves).

### WS-N12 — Structural pattern lane for rule findings (M, Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.3. **Status:** Partly. `search` already has structural `pattern` mode and ast-grep-backed matching (`api-go/internal/mcpserver/tool_search.go:20-45`), while persisted review finding rule packs, relational predicates, and stable fingerprints are not evidenced.

**Contract:** Keep matching on the existing Rust/indexed tree-sitter facts and existing `search` tool; add versioned declarative rule-pack input, bounded AST predicate evaluation, and findings with path/range/fingerprint. No executable rule code or model-generated rule evaluation. First implementation should be local/admin-supplied packs; report unsupported language/predicate explicitly.

**Compatibility:** **OWNER DECISION.** New rule-pack schema/output is additive; keep current `pattern` semantics unchanged and put rule-finding lane behind opt-in until pack format is versioned.

**Acceptance:** Data-only rule finds fixture with exact file/line/character range and stable fingerprint; malformed/unknown predicates fail closed; no model/provider invocation. Rust gate, Go gate for API/schema, retrieval gate if ranking changes. Budget gate owed if rule packs or ASTs are retained/resident; measure bounded pack size and peak.

## Multi-agent — report 2026-10-03 §3.4

### WS-N13 — Delegation does not inherit approval (M, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.4. **Status:** Partly. Authenticated actors and role checks exist across workspace operations; memory attribution is bound to actor and proposal verification is separate (`api-go/internal/workspaceops/memory_propose.go:842-864`, `memory_verify.go:15-31`). Explicit delegated-agent identity/approval chain was not identified.

**Contract:** Every delegated principal gets distinct authenticated identity, role, approval state, and audit attribution. Parent may delegate scoped capabilities, but approval is never inherited; proposed memory’s author is delegate, verifier is independently authorized principal. Validate at API auth middleware and governance transition (`mcpserver`, workspaceops actor binding, govstore vote transaction).

**Compatibility:** **OWNER DECISION.** Existing integrations may represent child calls under parent token. Recommend preserve current direct-call behavior; delegation claims must be explicit and cannot gain approval through parent token. No default identity inference.

**Acceptance:** delegated agent cannot self-verify through parent token; refusal audited; independent verifier succeeds; parent direct approval rules unchanged. Go gate; budget gate not owed unless delegation workers spawn resident processes (no new workers proposed).

### WS-N14 — Task ownership as a first-class edge (M, Go+Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.4. **Status:** Partly. Goal runtime has parent/child execution and ownership machinery (`rust-core/src/goalruntime.rs`, `rust-core/src/ownership.rs`); no durable task-ownership edge contract mapping abort/holds into existing graph was verified.

**Contract:** Add typed ownership edges among task/run IDs to existing goal runtime and evidence lineage; parent cancellation aborts owned children, child completion releases parent hold, dependency graph derives from recorded edges, not model prose. Enforce bounded child count/depth and propagate cancellation through existing process-group timeout.

**Compatibility:** **OWNER DECISION.** Auto-aborting children changes existing run cancellation behavior. Recommend opt-in structured ownership for new goal plans; legacy unowned children retain current semantics until explicit migration.

**Acceptance:** Abort parent terminates owned child group; completed child releases hold; edge query exactly reflects persisted state; cancellation works under deadline. Rust and Go gates. Budget gate owed if concurrency/process count changes; enforce existing process budget, measure peak/RSS.

## Runtime under 100 MB — report 2026-10-03 §3.5

### WS-N15 — Non-default compaction paths are tested (S, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.5. **Status:** Partly. Session snapshots/handoff/compaction markers exist (`api-go/internal/workspaceops/session_snapshot.go:494`; `docs/plans/2026-09-25-parity-build-plan.md:2517-2594`), but there is no evidence every alternate planner/profile/plugin compacts or that a raw spec without condenser fails.

**Contract:** Each context-carrying agent path must declare explicit condenser or explicit “no compaction supported”; planner construction rejects missing policy for paths expected to compact. Test all profile/plugin adapters, including non-default branches, using synthetic pressure crossing threshold and verify compacted bounded result.

**Compatibility:** **OWNER DECISION.** Rejecting previously accepted raw planner specs can break integrators. Recommend warning/telemetry for existing paths, require explicit policy for newly registered paths, and opt-in rollout before hard failure.

**Acceptance:** Table-driven adapter/profile matrix proves each configured compacting path compacts; missing condenser is rejected; no-compaction paths are explicit. Go gate and Pi adapter check if extension schema touched. Budget gate owed if compaction changes retained memory; measure compaction idle and peak against target.

### WS-N16 — Compile-once hook matchers (S, Go)

**Source:** `research-2026-10-03/REPORT.md` §3.5. **Status:** Partly. Hook adapters describe matcher semantics (`api-go/internal/hooks/cursor_hooks.go:43-59`), but Cursor matchers are fixed strings and no generic compile-at-registration matcher pipeline was found. Existing `path.Match` use should be assessed per adapter before claiming.

**Contract:** Parse/validate any configured matcher once when hooks register/discover; store compiled immutable matcher and use it in event hot path. Invalid matcher rejects registration/config (not match-all). No per-event regexp compilation or child process introduced. Keep Cursor client’s own native matcher contract intact.

**Compatibility:** **OWNER DECISION.** Invalid patterns that previously matched differently may now be refused. Recommend preserve fixed built-in matchers; compile only user-configurable patterns and report configuration error before events.

**Acceptance:** 1,000-hook burst has zero matcher compile allocations after registration; invalid pattern fails closed; concurrent event calls safe. Go gate; budget gate owed only if matcher cache is retained and large—measure memory/allocation; no new process.

### WS-N17 — De-retain after serialisation (S, Go+Rust)

**Source:** `research-2026-10-03/REPORT.md` §3.5. **Status:** Not implemented/evidence missing. Index/cache serialization exists (`rust-core/src/index/mod.rs`, `indexcache.rs`), but no retained-heap proof that parsed component trees are released after serialize.

**Contract:** After producing serialized index/cache bytes, drop parsed/tree-heavy intermediate ownership before publishing long-lived cache; avoid reference cycles or duplicate retained backing buffers. Keep output bytes and immutable minimal metadata only. Apply to specific measured hot serialization path, not indiscriminate arena redesign.

**Compatibility:** No client-visible default change if serialization is byte-identical. Any cache data change must preserve version check (N5).

**Acceptance:** Retained-heap test/measurement after forced serialization proves source tree no longer reachable; serialized bytes and reload semantics unchanged. Rust/Go gate as touched. Budget gate owed because this row is specifically memory/retention: report before/after Linux RSS/dirty memory; never infer from macOS.

## Engine — report 2026-10-03b §3.1

### WS-N18 — Edit-location prediction (M, Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.1. **Status:** Partly. Search/index has file/symbol/span candidates and deterministic lanes (`rust-core/src/search.rs`, `rust-core/src/index/areas.rs`, `index/fts.rs`); no issue-to-edit-location prediction output or calibrated `exact|lower_bound` marker was found.

**Contract:** Add optional retrieval stage consuming issue text plus indexed candidates, returning ordered `{path,symbol,line_span,score/reason}` with completeness `exact|lower_bound`; never scan live tree to discover candidates and never claim zero when index lacks region. Expose via existing `search`/`ground` result fields or internal retrieval API; candidate cap, deterministic no-model path required.

**Compatibility:** **OWNER DECISION.** New prediction result can imply stronger completeness than existing search. Recommend opt-in and additive field; default existing search unchanged.

**Acceptance:** ContextBench verified subset coverage/precision measured; no-symbol region reports lower_bound; same input byte-identical without model. Rust gate and retrieval gate. Budget gate owed if new index structures persist; benchmark transient candidate memory and cap.

### WS-N19 — SCIP import at the boundary (M, Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.1. **Status:** Partly. SCIP precise-lane ingestion is already specified as WS-46 (`docs/plans/2026-09-25-parity-build-plan.md:3889-3905`); no completed SCIP importer was confirmed in current `rust-core/src` search. This is a boundary conversion, not SCIP-shaped storage.

**Contract:** One-shot import of SCIP index to current symbol graph; record `provenance=scip`, SCIP/indexer version per imported node; no JVM start, no SCIP modification semantics. Validate input/version/checksum and reject stale import against repository identity/freshness. Implement at WS-46 Rust index ingest boundary and reuse graph nodes/edges, not new MCP tool.

**Compatibility:** **OWNER DECISION.** Imported graph can alter search/impact results. Recommend explicit import command/opt-in per workspace and keep existing index lane default until freshness/provenance shown.

**Acceptance:** rust-analyzer SCIP fixture imports without JVM; nodes carry provenance/version; stale input reports freshness regression once N2 contract decided. Rust gate, retrieval gate. Budget gate owed if imported graph increases persisted index/resident graph; measure index size/RSS.

### WS-N20 — Engine index-status truth (S, Rust+Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.1. **Status:** Partly. Core `index stats` reads stored index identity, versions and coverage (`rust-core/src/index/mod.rs:1832-1886`); Go exposes worker health and some semantic-index plan/status, but no unified provenance marker tying file/chunk/symbol counts to live store snapshot was verified.

**Contract:** One source computes files/chunks/symbol counts from the index generation answering the request, with `{provenance: live|snapshot|advisory, generation, observed_at}`. Do not combine snapshot counters with a live store as if consistent. Missing/unreadable index is unknown, not zero. Keep existing diagnostics/status routes and Go core bridge; no new tool.

**Compatibility:** **OWNER DECISION.** Provenance fields additive; correcting stale zeros changes values. Recommend add provenance and keep existing counts, but serialize unknown as null in versioned response rather than zero.

**Acceptance:** stale snapshot cannot return 0 counts for populated index; response provenance/generation match serving snapshot; absent index is distinguished from empty populated index. Rust and Go gates. Budget gate not owed unless status caching is added; avoid resident polling/timer.

## Memory — report 2026-10-03b §3.2

### WS-N21 — Promotion-time drift check (M, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.2. **Status:** Partly. Memory drift checks/reverification on recall exist (`api-go/internal/workspaceops/recall_bounded_test.go:21`, `memory_recall.go`); no fingerprint captured at verification approval and revalidated at promotion was established. Distinct from N6 writer fencing.

**Contract:** At approval, persist fingerprint over the exact evidence set/revisions/anchors; at promote, re-read under transaction and compare. Mismatch refuses transition with `re_propose` and current fingerprint, one audit event; no evidence content returned unless authorized. Place check in `memory_verify.go` and promote transition in `memory_propose.go`/govstore transaction.

**Compatibility:** **OWNER DECISION.** Promotion can now refuse previously successful approvals. Recommend enforce for new approvals only first; legacy approvals without fingerprint receive explicit re-verification path, never silently treated as fresh.

**Acceptance:** Evidence mutation between approve/promote refuses with one audit and drift details; unchanged evidence promotes as before; concurrent writer race serializes. Go gate. Budget gate not owed (small fingerprint metadata); measure DB growth/transaction latency.

### WS-N22 — Content-level proposal dedup (S, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.2. **Status:** Partly. Proposal dedupe/conflict handling already exists (`api-go/internal/workspaceops/memory_dedupe.go`, `memory_propose.go:319-321,497-528`), including corroboration/possible duplicate signaling. Exact same normalized content+principal collapse with `repeats` count and distinct-principal quorum was not verified; current semantics may intentionally retain rows.

**Contract:** Normalize content deterministically and key proposal by `(workspace, principal, normalized content)`; identical same-principal proposal returns same proposal and increments repeat count atomically; other principals remain separate and count once each toward quorum. Do not auto-promote based on repeats. Integrate with existing dedupe review rather than replacing conflict detection.

**Compatibility:** **OWNER DECISION.** Returning existing proposal instead of new ID changes writes/idempotency. Recommend opt-in idempotency mode for new clients first; preserve current behavior for existing `remember` requests until migration.

**Acceptance:** duplicate same-principal fact is one proposal with repeats=2; self-repeat does not reach quorum; two principals count two but each evidence/approval requirement remains. Go gate. Budget gate not owed if counter stored on proposal; measure row/index increase and keep updates transactional.

### WS-N23 — Typed write outcomes and refusal vocabulary (M, Go+Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.2. **Status:** Partly. `remember` has outcome strings such as `created` and `corroborated` (`api-go/internal/workspaceops/memory_propose.go:178`, `memory_dedupe.go:57`); invalid input/conflict errors exist. Full closed union (`updated|already_exists|conflict|stale_revision|locked|refused`) and stable mapping (409/423) are not established.

**Contract:** Define closed mutation outcome enum and stable JSON keys; every mutation returns exactly one outcome or typed refusal, never inferred success. Stale revision includes current checksum/revision for rebase; lock returns 423; conflict 409. Map Go domain error to existing HTTP/MCP transport; if Rust caller exists, mirror generated contract and exhaustive match. Keep audit semantics and secret redaction.

**Compatibility:** **OWNER DECISION.** Existing clients may branch on current strings/status. Recommend additive `outcome_v2`/stable error code while preserving legacy fields, then deprecate after adapter coverage; never change success to error silently.

**Acceptance:** exhaustive table test covers each union arm; stale-revision refusal includes checksum; locked is 423 not 500; unknown future arm cannot be treated success. Go gate and Rust gate if bridge type added. Budget gate not owed (small response fields).

### WS-N24 — Recall observability (S, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.2. **Status:** Partly. Recall returns selected memories and scope/freshness metadata (`api-go/internal/workspaceops/memory_recall.go:499-578`, `memory_render.go:148`); durable event naming delivered and suppressed IDs was not found.

**Contract:** Each recall emits bounded run-transcript event with delivered entry IDs and suppressed IDs/reasons (echo, scope, policy, budget), never suppressed content. Record only after final output selection/budgeting; bind to authenticated run/session and existing transcript retention. Existing `recall` response remains unchanged; internal observability event goes through session ledger/store.

**Compatibility:** No client-visible default change. **OWNER DECISION** on transcript retention/visibility: recommend event metadata-only, same retention class as session event, disabled when session capture is disabled.

**Acceptance:** Run answers which IDs were delivered/suppressed without prompt replay; exact reasons enumerated; event has no content/secrets; failed recall emits no success event. Go gate. Budget gate owed only if event retention materially grows store; measure per-event bytes and use existing bounded retention.

## Pipelines — report 2026-10-03b §3.3

### WS-N25 — Verified selection for repair candidates (M, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.3. **Status:** Not implemented as a repair-candidate pipeline found. Verification and evidence recording exist (`api-go/internal/workspaceops/verification.go`, `verification_outcomes.go`), but no N-candidate generation/reranking flow tied to stored verification evidence was identified.

**Contract:** Existing goal/run system records N candidate patches, runs deterministic verification independently, then selects by verification result using fixed ordering. Model cannot rank its own output. Candidate with verification unavailable is `unverified`, below every verified candidate; retain per-run cost/resolution evidence. Use existing goal runtime and run/evidence APIs; do not add a new MCP tool.

**Compatibility:** **OWNER DECISION.** Automatically choosing and applying a patch would alter user control. Recommend selection-only advisory output; never apply/merge; explicit user chooses candidate.

**Acceptance:** Selection reproducible from persisted evidence; unverified sorts below verified; unavailable verification never counts as pass; cost/resolution persisted per run. Go gate and Rust gate if goal runtime changes. Budget gate owed if multiple candidate processes run; serialize or cap concurrency, then measure process-tree RSS.

### WS-N26 — Pipeline run verdict vocabulary (M, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.3. **Status:** Partly. Existing run/outcome records distinguish execution/result concepts, but unified `clean|incomplete|failed` plus `infra_failure{reason,tier}` contract not found (`workspaceops/outcomes.go`, `verification_outcomes.go`).

**Contract:** Every pipeline run has one verdict: clean (complete and successful), incomplete (some inputs not assessed), failed (execution produced failure/no valid output); optional post-hoc `infra_failure` with reason and tier environment/ambiguous never alters denominator. Separate process exit code/log status from classification. Apply to current run/eval result schema, no new MCP tool.

**Compatibility:** **OWNER DECISION.** Renaming states affects clients and saved results. Recommend add canonical `verdict_v2` while retaining legacy status; preserve raw exit code and denominator.

**Acceptance:** no-output runner is failed; unreadable input incomplete; only clean contributes success headline; ambiguous never aggregates into environment; classifier does not mutate outcome denominator. Go gate; eval corpus gate (xmustard-eval package tests). Budget gate not owed unless logs/schema persistence materially expands; bounded event only.

## Multi-agent — report 2026-10-03b §3.4

### WS-N27 — Skip-is-not-approve (S, Go+Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.4. **Status:** Partly. Memory verification quorum and non-self-approval are enforced (`api-go/internal/workspaceops/memory_verify.go`, `govstore` lifecycle); explicit “skipped/unreachable -> unverified” state across every verification gate is not verified.

**Contract:** Every required verification step returns pass/fail/unverified; skip, timeout, missing evidence or unreachable quorum cannot transition to promoted/approved. Enforce at final governance/merge gate, record audit event and preserve pending state. Keep human final merge authority; no model vote substitutes.

**Compatibility:** **OWNER DECISION.** Existing clients may treat skipped as success/empty. Recommend preserve old outputs but change gate to fail closed immediately for new approval transitions; migration reports existing ambiguous states for review.

**Acceptance:** Quorum step timeout yields unverified plus audit; no code path from unverified to promoted; explicit pass still promotes under existing policy. Go and Rust gates as applicable. Budget gate owed if verifier fanout/process count changes; prefer bounded sequential work, measure.

## Runtime — report 2026-10-03b §3.5

### WS-N28 — Workspace lint and dependency policy (S, Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.5. **Status:** Not implemented as specified. Rust workspace has `Cargo.toml` and clippy checks, but exhaustive enum/struct lints at warn and `DEPENDENCY_POLICY.md` were not located.

**Contract:** Set workspace clippy `exhaustive_enums` and `exhaustive_structs` to warn where supported; add `DEPENDENCY_POLICY.md` stating necessity, footprint, optionality/feature-gating review criteria. No runtime API change; new non-optional dependency requires written review rationale, not an automatic compile-time rejection.

**Compatibility:** No default runtime behavior change; lint warnings affect contributors/CI. Recommend gradual lint cleanup, warnings first as proposal says, not deny.

**Acceptance:** Add test/fixture enum variant demonstrates warning at exhaustive match; docs policy covers every criterion; cargo clippy reports expected warnings without raising gate thresholds. Rust gate. Budget gate not owed (build-only policy); dependency additions still require measured process/memory impact.

### WS-N29 — MCP schema generated from Rust types (M, Rust)

**Source:** `research-2026-10-03b/REPORT.md` §3.5. **Status:** Not implemented; MCP schemas are currently explicitly authored in Go (`api-go/internal/mcpserver/tool_search.go:20-45` and sibling tool files), while Rust core is a separate semantic backend.

**Contract:** Single typed source per tool generates JSON schema and Go/stdio/HTTP published contract plus canonical round-trip fixture. Must preserve existing nine-tool list and exact `CAPS`; generation is build-time, not runtime reflection/server dependency. Prove schemas cannot drift through regeneration/diff check. Integration boundary: MCP schema registry/Go tool definitions and Rust protocol types; do not force engine ownership into Rust solely to generate schemas.

**Compatibility:** **OWNER DECISION.** Replacing schemas can change argument requiredness, descriptions, outputs, tool-list bytes. Recommend generated output initially byte-identical to current Go schemas; no client-visible changes in migration PR.

**Acceptance:** Regeneration produces no diff; deliberate type change requires generated schema update; round-trip validates published schema against tool type; nine-tool/caps checks pass. Go gate, Rust gate, Pi adapter check if schemas consumed there. Budget gate not owed if build-time only; tool-list byte cap must remain unchanged.

## Evaluation — report 2026-10-03b §3.6

### WS-N30 — Context retrieval evaluation lane (M, Rust+Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.6. **Status:** Partly. Retrieval and memory evaluation tooling exist (`api-go/cmd/xmustard-eval`, `scripts/bench`); a four-granularity gold metric lane with AUC-Coverage/Redundancy and `ground_truth_mode` was not verified.

**Contract:** Evaluation rows record ground-truth mode and granularity `{file,symbol,span,EditLoc}`; score byte-measured spans, AUC-Coverage, Redundancy for engine and memory; normalize trajectories agent-independently. Separate headline per lane; no self-referential single score. Implement in eval task schema/runner and bench reports, not production MCP surface.

**Compatibility:** **OWNER DECISION.** Metrics change published benchmark comparability. Recommend version the eval schema/report and preserve historic results with original method labels; do not silently recompute or compare unlike datasets.

**Acceptance:** schema validation requires both fields; fixtures cover all granularities, redactions and memory lane; metrics match hand-calculated gold and are not circular. Go eval tests and `make bench-test`; retrieval gate owed for changed retrieval scoring. Budget gate not owed unless eval process count/memory moves; benchmark runner itself measured separately.

### WS-N31 — Benchmark-version pinning and decontamination (S, Go)

**Source:** `research-2026-10-03b/REPORT.md` §3.6. **Status:** Not implemented as proposal found. Eval tasks/results exist but mandatory dataset version/commit/window fields and stale refusal were not confirmed (`api-go/cmd/xmustard-eval`, `eval/tasks`).

**Contract:** Every stored/published benchmark result includes dataset name/version/commit, benchmark code version, decontamination window and contamination check status. Missing version is refusal for new result; stale dataset labeled stale, outside-window result flagged not silently compared. Preserve original result, no retroactive rewriting.

**Compatibility:** **OWNER DECISION.** Existing unversioned records cannot satisfy schema. Recommend legacy records retained as `unversioned/unknown`, excluded from direct comparisons; require metadata for all new records.

**Acceptance:** unversioned new LongMemEval result refused; old result remains readable with unknown status; stale LoCoMo labeled by date; outside-window flagged. Go eval gate and `make bench-test`. Budget gate not owed (metadata only).

## Documentation and registry — report 2026-10-03b §3.7

### WS-N32 — Peer registry liveness fields (S, docs)

**Source:** `research-2026-10-03b/REPORT.md` §3.7. **Status:** Not implemented as proposal; current `docs/research/RESEARCH_INDEX.md` is a narrative snapshot headed June 2026, not a structured peer registry with liveness fields.

**Contract:** Add registry rows with canonical source URL, verification method/date, `source_present`, `superseded_by`, `last_commit_date`; refresh on a bounded human/CI schedule, not a resident watcher. Record redirects as canonical URL changes with evidence. Keep registry distinct from opinion/benchmark claims.

**Compatibility:** None (docs/internal process only). New row fields should be optional for historical references until checked; do not invent status.

**Acceptance:** letta retired repo points to letta-code with `source_present:false`; fixture/check records 301 canonical URL; every entry has verification source/date. Docs review/check script if added; no code gate. Budget gate not owed; scheduled check is external/on-demand only, no polling loop.

### WS-N33 — Refresh `docs/research/RESEARCH_INDEX.md` (S, docs)

**Source:** `research-2026-10-03b/REPORT.md` §3.7. **Status:** Not implemented/currently stale. The file says “Research Index (June 2026)” and acknowledges training-knowledge input (`docs/research/RESEARCH_INDEX.md:1-10`).

**Contract:** Date the index and replace unsupported stale summary with verified entries for every project in both 2026-10-03 reports; each entry names verification method (clone/API/docs) and last activity date. Include ContextBench, SWE-ContextBench, LongMemEval-V2, SWE-rebench as cited. Mark unknown where unverified; do not turn leaderboard data into product proof.

**Compatibility:** None to runtime defaults. Docs-only, citations must retain attribution/licensing distinctions.

**Acceptance:** scripted/manual cross-check ensures all cited projects appear once and each has verification method/date; report citations link to appropriate source. Docs review, no builds/tests. `bench-gate` not owed.

## WS-N34 — indexed Jev intent lane

**Source:** `docs/plans/2026-10-23-jev-semantic-lane.md` §3, “WS-N34 — indexed Jev intent lane” (source row explicitly defines N34+; implementation is on `jev-n34` branch). **Status:** Not implemented in this branch’s current source; design document only. N34 is opt-in typed intent lane with recorded fixture provider, indexed candidate funnel and RRF K=60, strictly off by default.

**Contract:** Existing `search` only; typed query/config, bounded index-backed area/file/symbol candidates, validated bounded excerpts, semantic probability/provenance and degradation; never discover candidates by walking live tree or send secrets. Go owns config/auth/redaction/notice; Rust owns index/candidates/rank. Provider timeout/invalid output/missing key/stale index fall back to deterministic hits with explicit degradation and zero network where disabled. Follow N34 doc §3 for full request/fixture/cursor/cache policy.

**Compatibility:** **OWNER DECISION** for any default enablement or lane weighting. Proposed default is off; keep current search results/schema unless opt-in, no key/notice => no remote call.

**Acceptance:** recorded fixture finds paraphrase absent identifiers only when enabled; offline/missing-key/stale/timeout/invalid probability preserve deterministic hits; hash invalidation, secret-path exclusion, stable RRF/cursor/ties, candidate caps, no tree enumeration, no test network; retrieval gold has no regressions. Rust and Go gates; retrieval gate required; budget gate owed for network/process/memory changes, with Linux RSS/process tree and per-call cost measured before claims.

## Respect the reports’ constraints

Do not adopt the rejected ideas in `research-2026-10-03/REPORT.md` §4 or `research-2026-10-03b/REPORT.md` §4: no hosted-only semantic architecture, unsafe prompt/parameter logging, automatic confirmation/merge, 41-tool MCP surface, full-repo single-process memory footprint, unverified governance status enums, default-scope fallback, unbounded distance-score inversion, vendored SDK patches, machine-sized opt-out budgets, dormant Agentless-as-system, static-leaderboard claims, AutoGen write-without-verification, goose footprint claims, or training-knowledge research summaries as evidence. Use attribution lists in each report §5 to update `NOTICE`/README when an implementation actually adopts a design; cite references only, copy no code/prompts/fixtures. Licensing/provenance remains per report, including design-only and noncommercial/AGPL/CC-BY cases.

## Owner decisions

- **OQ-N1:** N1 wire contract: should accounting remain internal first, or add negotiated serialized envelope metrics? Default internal-only; do not add fields or change truncation defaults.
- **OQ-N2:** N2 freshness enum compatibility: version/negotiation for `regressed` versus legacy `diverged`. Default preserve old field and add versioned status.
- **OQ-N3:** N3 impact null and unmapped-file shape/schema cap. Default additive list; version nullable count change.
- **OQ-N4:** N4 one identity function vs shared encoding primitive with separate review/memory wrappers; migration/alias policy. Default primitive plus domain wrappers and dual-read.
- **OQ-N5:** N5 cache invalidation policy for unverifiable legacy artifacts. Default rebuild only unverifiable/incompatible caches and report it.
- **OQ-N6:** N6 lease holder liveness proof mechanism and process identity portability. Default DB transaction/fencing token; no wall-clock lease expiry alone.
- **OQ-N7:** N7 echo discount values, history window and enablement. Default opt-in until retrieval gate.
- **OQ-N8:** N8 when legacy omitted `visibility` ceases to mean workspace scope; how cross-workspace applicability is represented. Default preserve existing write behavior.
- **OQ-N9:** N9 which new filter operators/metrics ship first. Default closed enums and 4xx for unknown.
- **OQ-N10:** N10 terminal `incomplete` versus `clean` plus `not_reviewed`. Default add `incomplete` in versioned verdict; never call incomplete clean.
- **OQ-N11:** N11 retry attempt cap/backoff and which failure classes are transient. Default one bounded retry only for explicit transient classes, with cancellation and backoff.
- **OQ-N12:** N12 rule-pack ownership, versioning/distribution and supported languages. Default opt-in local/admin packs, no executable rules.
- **OQ-N13:** N13 delegation identity/token issuance and capability scope. Default separate principal; no approval inheritance.
- **OQ-N14:** N14 ownership edges and parent abort semantics for already-running children. Default opt-in for explicitly owned tasks.
- **OQ-N15:** N15 adapter paths that promise compaction and explicit no-compaction semantics. Default require policy only for newly registered compacting paths.
- **OQ-N16:** N16 user matcher syntax/invalid-config handling. Default compile user-defined matchers at registration; preserve built-ins.
- **OQ-N17:** N17 measured target serialization path and Linux heap proof method. Default no refactor until retained-memory regression is reproduced.
- **OQ-N18:** N18 gold subset provenance and `exact|lower_bound` calibration threshold. Default opt-in, lower_bound unless completeness proven.
- **OQ-N19:** N19 SCIP import timing relative to WS-46 and N2 freshness. Default boundary import after WS-46 input/version validation, opt-in.
- **OQ-N20:** N20 provenance vocabulary and nullable count schema. Default additive provenance, unknown not zero in versioned API.
- **OQ-N21:** N21 interaction with N6 fencing and legacy approvals. Default both mechanisms address separate races; fingerprint new approvals, explicit reverify legacy.
- **OQ-N22:** N22 normalized content definition and same-principal idempotency rollout. Default opt-in first; no quorum contribution from repeats.
- **OQ-N23:** N23 exact union, status mapping and transition from current `corroborated` outcome. Default additive typed code while preserving old fields.
- **OQ-N24:** N24 transcript event retention/access policy. Default IDs/reasons only under existing session retention; no content.
- **OQ-N25:** N25 candidate count/concurrency and whether selection can ever apply patches. Default selection-only, bounded, human chooses.
- **OQ-N26:** N26 naming alignment with SWE-bench versus local `clean|incomplete|failed`. Default local vocabulary plus separate infra classification, versioned.
- **OQ-N27:** N27 audit/event semantics for skipped or timed-out quorum. Default unverified and never promoted.
- **OQ-N28:** N28 supported compiler/clippy version for requested lints and dependency-policy enforcement. Default warn, policy review rather than automated deny.
- **OQ-N29:** N29 schema source ownership across Go MCP surface and Rust core. Default generate Go-compatible output at build time; no runtime SDK/tool expansion.
- **OQ-N30:** N30 adopt-and-cite ContextBench gold vs maintain vendor-neutral gold. Default methodology-inspired own/versioned gold and cite ContextBench as reference, not proof.
- **OQ-N31:** N31 decontamination window and dataset freshness definition. Default unknown/flag unless documented, never silently compare.
- **OQ-N32:** N32 verification cadence and authority to mark peer absent/superseded. Default bounded manual/CI refresh, cite evidence.
- **OQ-N33:** N33 research index scope and evidence bar for all report projects. Default enumerate all citations, mark unverified rather than infer.
- **OQ-N34:** N34 semantic provider, notice, budgets and RRF lane weighting. Default off, fixture-only tests, no network without explicit opt-in and notice.

## Suggested implementation order

Order assumes required owner decisions are resolved before breaking contracts. Group into briefs of 3–5 rows; keep independent doc-only rows separate where appropriate.

1. **Brief A (N5, N16, N28, N33):** version/cache guardrails, compile-once matchers, lint/dependency policy, refresh research index; non-breaking first.
2. **Brief B (N1, N20, N24, N31):** byte accounting, truthful index status, recall metadata events and benchmark provenance; additive/internal contracts first.
3. **Brief C (N3, N10, N11, N26):** impact epistemics and deterministic review/run coverage; stabilize additive fields before verdict migration.
4. **Brief D (N4, N6, N21, N23):** identity plus governance concurrency/drift/typed outcomes; coordinate transaction/schema migration.
5. **Brief E (N7, N8, N9, N22):** recall ranking, scope hardening and proposal semantics; requires compatibility decisions and eval.
6. **Brief F (N12, N18, N19, N29):** structural rules, edit-location retrieval, SCIP boundary and generated schemas; keep opt-in and measure index footprint.
7. **Brief G (N13, N14, N15, N27):** delegation, task ownership, compaction matrix and skip-is-not-approve; security/cancellation review first.
8. **Brief H (N2, N25, N30, N32):** freshness enum, repair selection, evaluation lane, peer liveness; coordinate N2 schema and benchmark evidence.
9. **Brief I (N17, N34):** retained-heap proof and optional semantic provider; N34 first requires N17 only if profiling identifies shared retained structures; both need measurements, and N34 needs retrieval/budget gates.

Suggested ordering deliberately postpones behavior flips (N2/N8/N10/N22/N26) and any provider-enabled semantic path until owner decisions, compatibility rollout and evidence are complete.
