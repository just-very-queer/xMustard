# Jev semantic lane for xMustard

**Status:** design proposal; no runtime behavior is implemented by this document.
**Scope:** Jevgrep-style intent retrieval, boolean composition, and bounded typed rechecks, implemented natively in Rust and Go. xMustard does not add a Jevgrep/npm/Node runtime dependency.
**Research snapshot:** Jevgrep `28cf384d444e1f4bf5571142ec32357cb84973ae`, jegrep `197e674285eb321e2be4b5f6797d755af2c32c77`, jgrep `9d56104abcf35b0af330122dbe14989c6111a1fd` (shallow clones under untracked `research/`).

## Decision in brief

Add an **optional, remote, typed-decision lane** to the existing indexed search pipeline. Jev answers narrowly phrased yes/no questions about bounded candidate records; xMustard owns candidate generation, filtering, scoring, provenance, freshness, caching, budgets, and output. The lane is off by default and is not an embedding index, a daemon, or an exhaustive tree walk. It augments the existing index; it does not replace BM25, identifier matching, graph lanes, or deterministic local search.

Use Jev only when the user explicitly opts into data egress. Never call it merely because the key is present. A fixed probability cutoff of **0.5** is the initial proposal, not a learned per-query threshold or a user-configurable threshold. A typed result is a relevance signal, not proof that a memory is true, code is safe, a test passed, or a finding is valid.

The core benefit over WS-37's proposed static embeddings is semantic classification without shipping or loading a model or storing vectors. The trade-off is explicit source egress, network dependency, provider variability, and a per-query cost. Keep the static embedding design as a fully local alternative/fallback until a measured owner decision retires or changes WS-37.

## Evidence and boundaries

### What the inspected tools actually establish

- Jevgrep presents the task as a natural-language query and evaluates relevance by folder/navigation item, file, and declaration/source range. Its architecture says it prunes hierarchically, keeps qualifying files without a fixed top-N, reports incomplete discovery when a navigation budget is exhausted, and treats unread descendants as unknown (`research/jevgrep/docs/architecture.md`, `packages/core/src/retrieve.ts`). It does not upload the whole tree first.
- The Jevgrep request builders use a state object plus named boolean questions (`packages/core/src/requests.ts`). Jev returns a probability per answer; current source applies `> 0.5` for selections and has a stricter `> 0.7` presentation boundary (`packages/core/src/selection.ts`). Those are Jevgrep policy choices, not xMustard requirements. This plan proposes one fixed xMustard cutoff of `>= 0.5` for lane membership and deliberately does not add a second cutoff for snippets.
- Requests are batched: navigation groups are limited to 128 items and 38,000 serialized bytes; declaration groups are limited to 128 and split at a 42,000-byte context boundary (`retrieve.ts`, `selection.ts`). Evaluator concurrency is bounded (default 32); the evaluator also has timeout, request-count, cancellation, 429 cooldown and response validation (`evaluator.ts`). These numbers are observed implementation details, not Jev vendor limits or recommended xMustard defaults.
- Jevgrep's local answer cache is keyed by a digest over the request plus provider/model/policy/parser/prompt namespace, stores probabilities rather than request text, and has TTL and byte limits (`cache.ts`). This is a useful design pattern, not code to port.
- Jevgrep's current TypeSafe adapter uses `https://api.typesafe.ai/v1` with model `jev-1.13.0`; its OpenRouter adapter uses `https://openrouter.ai/api/v1` and `typesafe/jev-1.13` (`providers.ts`). Its SDK request is a TypeSafe System One request with `{model, state, questions}` and boolean question entries; response validation expects an answer type and probability in `[0,1]` (`evaluator.ts`, `requests.ts`). The API key is passed to the provider SDK. Its direct TypeSafe throttle is a client-side 100,000 estimated input tokens/sec and 40 requests/sec (`evaluator.ts`), not evidence of vendor account limits.
- Primary public TypeSafe product material reviewed at [typesafe.ai](https://typesafe.ai) advertises $42 per billion input tokens ($0.042 per million) and describes typed decisions. It did not expose a complete API reference in the inspected page. The precise endpoint, authorization header, request/response schema, supported model IDs, maximum request size, published rate limits, usage accounting and retention terms must be confirmed from the current first-party API documentation before implementation ships. Do not copy guessed vendor limits into code or claim a provider's published rate cap.
- The inspected sibling `jegrep` README describes semantic path/range search without an index, probability thresholds, and both `TYPESAFE_API_KEY` and `OPENROUTER_API_KEY`; it reports roughly $0.005 for a typical search over a few thousand files. `jgrep` describes per-line yes/no classification, Boolean composition (`-e`, `--all`, `-v`), batching questions, configurable concurrency, and a budget. These are separate implementations and are not xMustard interfaces to copy.

### Unresolved vendor facts are ship blockers, not guessed constants

A worker implementing the transport must verify from current first-party TypeSafe docs (and OpenRouter docs if that provider is enabled): endpoint URL, key header, model identifier, request/response wire schema, max state/request size, usage fields, rate-limit/retry headers, and privacy/retention terms. Add dated evidence links or archived text to the implementation record. If a fact remains unpublished, mark it unknown and use a conservative local bound; do not advertise a provider guarantee. v1 should support **one** selected provider/endpoint, not automatic failover: fallback can bill a second account and makes cost/provenance less predictable.

## xMustard integration contract

### Candidate source and lane fusion

The intent lane runs against the **current code index**, not a filesystem walk. Its candidate funnel is:

1. Use existing deterministic retrieval (BM25, name/path/identifier, optional filters) to choose a bounded set of seed files and symbols. The search response already reports lanes, per-lane scores, reasons, freshness and degradations; Rust search fuses its existing lanes with RRF K=60 (`rust-core/src/search.rs`). Preserve that ranking contract.
2. When an index generation has WS-38 functional areas, use area labels/keywords and indexed member paths as a navigation prior. Otherwise use indexed directory/path groups. Ask about areas only when their membership is available and freshness is current; do not invent a new tree walk to imitate Jevgrep. Navigation positives expand only within the fixed candidate budget.
3. Score bounded file previews for selected indexed files, then bounded symbol/declaration excerpts from positive files. Derive all excerpts from the index/source snapshot and existing secret-path/snippet policy. A file or symbol is eligible only when the source hash matches the indexed identity. If the index is stale, the source changed, the query is cancelled, or discovery is budget-limited, return the deterministic lanes and report `semantic_unavailable`, `semantic_stale`, or `semantic_budget` as appropriate; never silently treat missing results as negative.
4. Convert probability answers meeting the fixed cutoff into the public `intent` ranking lane, distinct from the existing ONNX `semantic` lane. Sort equal probabilities by stable indexed identity (path, line, kind, UID). Fuse that lane with deterministic lanes using the current RRF K=60 implementation and the existing lane-weight discipline. Keep raw intent probability in `scores.intent` and the ONNX vector score in `scores.semantic`; add `intent` to `lanes_matched` and a bounded reason such as `intent_probability`. The existing deterministic evidence remains visible when Jev is unavailable. A semantic answer must not outrank a lexical hit solely because it was asked an easier or broader question; retrieval-gate evidence decides the lane weight.

The first version must cap candidate files, declarations, question count, input bytes/tokens, request count, concurrency and wall time with named constants; any overflow stops semantic expansion and returns honest partial coverage. Start with at most 32 file candidates and 64 declaration candidates per query, batch by a conservative serialized byte limit no higher than the verified provider limit, and at most 2 in-flight requests per workspace. The existing index already caps what can enter; no recursive enumeration or background polling is introduced. These proposed bounds can be reduced after fixture tests; increasing them requires measured cost and gate evidence, not a silent default change.

### Search interface and Boolean composition

Deepen the existing `search` MCP tool; do not add a tenth tool. Proposed request additions:

- `mode="intent"` runs deterministic candidate generation plus Jev; `mode="hybrid"` keeps current behavior and runs Jev only when an explicit `semantic=true`/configured opt-in is supplied. Existing requests retain their current behavior. A per-call `semantic=true` only uses a lane the operator has already enabled and whose egress notice is acknowledged; it can never enable egress by itself, and no MCP argument can name a provider, endpoint, model, key or threshold.
- A bounded `filters` expression is a JSON AST, not an ad-hoc string parser: `all`, `any`, and `not` compose leaves for `text` (existing indexed BM25), `name` (existing identifier lane), `path_glob`, `lang`, and one typed `intent_question`. Enforce maximum depth 8 and 64 total nodes; reject malformed/unknown operators. `all` is intersection, `any` is union and `not` excludes only the explicitly evaluated candidate universe. `not` never means “the whole repository was proven irrelevant.”
- Local lexical/path/language predicates execute against the index and are applied before remote classification whenever that is logically equivalent; this reduces both egress and cost. A semantic leaf is an explicit yes/no criterion, e.g. “Does this declaration create, validate, or revoke a workspace registration token?” It is not a free-form request to generate an answer.

Keep all filters in the MCP schema within the current byte caps (`CAPS` gate). If a typed AST makes the schema too large, keep the MCP arguments small and parse a bounded expression in the existing `query` field only after a versioned syntax decision; do not add unbounded prose to the tool description. `search` output must keep existing hit fields and cursor binding; include semantic settings/freshness in ranking-key material so a cursor cannot continue a page under a different semantic result set.

### Reconciliation predicates (not an agent substitute for authority)

Reuse the same small typed evaluator for internal checks only after search has stable budgets and cache behavior:

- **Recall staleness:** ask whether the memory's narrow, redacted claim remains supported by its current anchor/source. A positive is “still appears applicable”; a negative or failed request is `needs_review`/unknown, never automatic deletion, supersession, promotion or trust. Existing deterministic anchor relocation and freshness checks run first.
- **`why_failed`:** ask whether a cited code/test location directly corresponds to the recorded failure. Keep run output redaction, evidence provenance, and existing outcome lineage. No provider call receives an unredacted secret or an entire run log; send only the minimal bounded redacted evidence. Provider failure cannot change an outcome.
- **Review dedup:** use typed yes/no “same underlying issue and same affected behavior?” as one candidate-pair signal after deterministic anchor/symbol overlap has narrowed pairs. A probability does not merge findings or erase votes; retain separate records unless the existing human/governed action accepts a dedup proposal.

No model output writes memory or changes approval status. An unanswered/ambiguous/low-confidence predicate remains unknown and is represented as such. Do not introduce periodic reconciliation loops. Invoke on-demand with explicit caller/deadline and only on already selected candidates.

### WS-37 relationship and local-only boundary

Jev can replace the **semantic scoring mechanism** proposed by WS-37 for optional intent search, but it is not a drop-in replacement for vectors: it requires network access, has per-query cost, only classifies the candidates xMustard sends, and does not produce reusable embeddings for arbitrary local nearest-neighbor lookup. It also does not solve offline use or local recall by itself.

- Keep BM25, identifier search, structural/proximity retrieval and filter operations local and available with semantic mode off or unavailable.
- Preserve WS-37's embedding/vector option as a separate optional implementation until the owner decides whether to de-scope it. Do not load both models/providers by default or count Jev as local-only.
- Any Jev-assisted memory recheck is opt-in and sends only necessary redacted text. A local-only installation must work with no key, no network, and no semantic switch, with identical existing behavior.
- Evaluate a future low-resource local typed classifier separately through WS-51/helper-model work. A local model is not presumed equivalent to Jev probabilities without measured fixtures.

## Opt-in, privacy, configuration and budgets

### Enablement and egress

- Default is off, consistent with WS-CFG's reserved `semantic.enabled=false` and the existing WS-37 default-off boundary. Presence of an API key never opts in.
- Before the first remote call, expose an explicit, non-bypassable egress notice in the operator/API setup and the MCP tool documentation: “Enabling Jev sends the query and selected, bounded repository excerpts to the configured provider. xMustard does not control provider retention. Do not enable for repositories or content you are not authorized to send.” A notice acknowledgement records the notice version and time locally; a policy-version change requires acknowledgement again. Do not put source content or keys in logs.
- `TYPESAFE_API_KEY` is the provider credential source for a direct TypeSafe deployment (or the provider's documented environment variable for an explicitly supported alternate). Read it from the process environment only; never from `xmustard.toml`, `.xmustard.*`, `settings.json`, MCP arguments, a URL, or a repository-controlled file. Do not log it, echo it in errors, or include it in cache keys. The config loader must treat it as operator-only; repository files cannot set provider endpoints, models, thresholds, or credentials.
- WS-CFG already reserves `semantic.enabled` / `XMUSTARD_ENGINE_SEMANTIC_ENABLED` as off and says no threshold/model config keys. Keep that contract. If the owner chooses a configurable cost cap/provider later, first amend the WS-CFG boundary; this design does not smuggle a model ID, endpoint, probability threshold, or trust decision into repository TOML. Per-query and per-day caps below are fixed conservative code defaults, surfaced in `config check` as compiled policy, not repository-settable keys.

### Cost caps and cache

Proposed initial direct-TypeSafe accounting assumes the public price of $0.042 / 1,000,000 input tokens, output free, until first-party billing docs confirm otherwise:

- **Per-query cap:** $0.005, equivalent to at most 119,047 billable input tokens at that rate (round down; never exceed the dollar cap). Before each request, reserve from a conservative byte-based input-token estimate plus question overhead. Reconcile against provider-reported input usage when valid. Refuse a request before egress if its maximum plausible charge would cross the query or daily cap. No “one last request” overrun.
- **Per-day cap:** $0.10 per workspace, equivalent to 2,380,952 billable input tokens at the assumed rate. Persist the UTC-day ledger locally with atomic updates; concurrent requests reserve atomically. If a provider exposes no trustworthy usage count, keep the conservative reservation charged; never assume zero. After restart, the cap remains enforced. Reset is a day boundary, not a rolling loop.
- **Worked cases at the stated price:** 20,000 billable input tokens = 20,000 × $0.042 / 1,000,000 = **$0.00084**; ten such cold queries = **$0.0084**. One 64,000-token request = **$0.002688**. Two such requests in one query = **$0.005376**, so the second must be refused under the $0.005 per-query cap. The daily cap admits at most 119 full 20,000-token queries (119 × $0.00084 = $0.09996), before any other requests. These are estimates, not vendor bills; record actual reported usage/cost separately and expose estimates as estimates.
- Cache identity is SHA-256 over canonicalized **candidate content hash + exact question text + question-policy version + model/provider/protocol version**. Include the code snapshot hash and redaction policy version. Store answer probabilities and minimal metadata only—not source, prompt state, secret, API key, or raw request. Use a bounded local cache (proposal: 7-day TTL, 64 MiB allocated-byte ceiling, oldest-first eviction) with private directory permissions. A content/question change, provider/model version change, policy change, or redaction version change is a miss. Cache hits still require current source identity validation before use. Cache failure degrades to a network-free miss only if the call remains under budgets; it is not permission to exceed budget.
- A failed, malformed, out-of-range, partial, or question-key-mismatched response is not cached. Tests use fixed recorded `{question-id: probability}` fixtures injected behind a provider interface; CI, benchmarks and default search never call a network provider.

The first-party API does not currently establish rate limits, retention, or exact usage schema in the inspected material. The implementation must use response retry metadata where documented, one bounded retry for explicitly transient status only (429/5xx/timeout, with `Retry-After` and capped exponential backoff), and no retry for auth, invalid schema, or budget refusal. Retry attempts consume the same reserved query/day budget. Never sleep in a tight loop; cancellation and deadline terminate waits.

## Implementation sequence and parity rows

These rows are new additions to the parity plan, named `WS-N34+` to avoid colliding with existing WS-34 through WS-37. They are implementation-sized and test-first; each red test named below must fail before its behavior exists and pass after. The reviewed design is not evidence that any row is implemented.

### WS-N34 — indexed Jev intent lane

**Goal.** Add an opt-in typed intent lane using current indexed area/file/symbol candidates, validated bounded excerpts, recorded fixture responses, semantic score/provenance, and RRF K=60 fusion. No traversal of the live tree for candidate discovery and no product dependency on jevgrep.

**Files likely touched.** `rust-core/src/search.rs`, new Rust provider/decision module under `rust-core/src/` or `rust-core/src/index/`, `api-go/internal/mcpserver/tool_search.go`, config reader/validation per WS-48, search docs and focused Rust/Go tests. Final ownership may adjust after inspecting dispatch; keep one Rust owner for index/candidates/ranking and Go owner for configuration, secret handling, auth/environment and MCP contract.

**Red tests / acceptance.**
- A fixture where a query describes behavior but shares no identifier with code is found by the intent lane and appears in `lanes_matched` with a probability and stable reason; it fails with the lane disabled and passes with recorded probability fixtures.
- The same fixture with semantic disabled, missing key, stale index, provider timeout, invalid probability, or over-budget input returns unchanged deterministic hits plus an explicit degradation and performs zero network calls in every case except enabled valid provider fixture.
- A changed source hash invalidates the result/cache before a current snippet can be returned; a secret path is never read or sent.
- Positive Jev ranks fuse reproducibly with BM25/name at K=60; tie ordering and cursor binding are stable; existing retrieval gold queries do not regress. RRF's lane weight/cutoff is accepted only after retrieval-gate results.
- A repository of indexed paths does not cause tree enumeration; instrumented source/index reads stay within candidate caps.
- Fixture provider asserts full request body, bounded batch size, question IDs, validation, and output contract; network access is impossible from tests.

**Acceptance.** Off by default; no key or notice means no remote call; index-backed candidate funnel and honest partial/degradation fields; no unbounded loops/requests; deterministic fixture tests; search MCP schema stays within `CAPS`; `RET` passes with no baseline regressions; Linux RSS/BUD measured before claiming the budget. Do not turn it on by default in this row.

### WS-N35 — Boolean query composition

**Goal.** Compose local indexed lexical/name/path/language filters with typed intent predicates using a bounded AST and deterministic set semantics.

**Red tests / acceptance.**
- `all` intersects candidates, `any` unions, `not` excludes only its evaluated candidate universe; examples combining a natural-language intent with exact lexical/path constraints return only the expected paths.
- Unknown operators, wrong types, empty invalid groups, depth >8, >64 nodes, multiple unsupported semantic leaves, and malformed patterns are refused before provider invocation.
- Equivalent AST inputs canonicalize to the same cache identity; different questions or candidate content do not collide.
- Local predicates are applied before egress when logically equivalent; a test verifies excluded paths and secret paths never reach the provider fixture.
- With Jev off or missing, local-only parts remain usable, and output says semantic evaluation was skipped rather than treating it as false.

**Acceptance.** Existing `search` tool only; no fourth/new tool, no arbitrary expression execution, bounded JSON schema and tool-list bytes under `CAPS`; Boolean results and reasons are deterministic; docs distinguish local truth from semantic probability.

### WS-N36 — bounded typed freshness/relevance checks

**Goal.** Add opt-in rechecks to recall staleness, `why_failed` source matching and review dedup candidates, only after deterministic narrowing, redaction, and the shared provider/cache/budget contract exist.

**Red tests / acceptance.**
- A recorded positive/negative fixture distinguishes “still applicable” from stale/needs-review; provider error, uncertain answer and timeout remain `unknown`/`needs_review` and never delete, promote, supersede, approve, merge or rewrite a memory/finding/outcome.
- Reconciliation sends only the selected anchor and bounded redacted source; a secret-shaped run output and an unrelated file are absent from the fixture request. Existing `why_failed` redaction and evidence lineage remain intact.
- Review-pair candidates are created only after deterministic overlap; semantic positive suggests a duplicate without merging records or votes. Negative/unknown preserves both.
- Hash/anchor changes invalidate cache; same unchanged hash and question use a fixture cache hit without network; policy/model change invalidates it.
- Invocation is on-demand, bounded, cancellable and does not create timers, background polling or retries without backoff.

**Acceptance.** All three callers retain deterministic fallback; typed answers are advisory only; no governance transition is model-triggered; fixture-only tests; per-query/day budget shared across callers and persisted across restart.

### WS-N37 — local-only and WS-37 disposition gate

**Goal.** Decide from measured parity evidence whether Jev supplements, replaces the semantic-scoring part of, or coexists with WS-37 static embeddings. No source code from Jevgrep/jegrep/jgrep is copied.

**Red tests / acceptance.**
- With no network/key, local BM25/name/structural/proximity lanes return the same results as before, byte-for-byte where response fields do not change; the optional static lane remains independently selectable if built.
- Retrieval gate includes concept queries, exact identifier/body queries, ambiguous/negative controls, stale-source cases and offline runs; Jev cannot claim full repository recall when the bounded indexed candidates did not include an unseen file.
- Cost record reports query count, estimated/reported tokens, cache hit rate, provider/model, latency, fixture/retrieval outcome and degradation; reports do not label a paid network lane “local”.
- Linux memory and process-tree budgets show Jev adds no resident model/vector allocation; network and per-call process behavior are measured separately.

**Acceptance.** Owner decision is recorded in the active plan with evidence. Until then WS-37 remains an independent local option and Jev stays off by default.

## Licensing, attribution and implementation hygiene

This design uses public behavior as an idea reference only. Implement the transport, Boolean evaluation, caching, accounting and candidate/index logic in xMustard's existing Go/Rust idioms. Do not copy upstream code, prompt prose, test fixtures, source snippets, or prompt templates. Do not add npm/TypeScript dependencies or runtime processes.

The repository `NOTICE` and this design document credit:

- [jevgrep](https://github.com/dzhng/jevgrep), MIT, by David Zhang — hierarchical typed-decision repository retrieval pattern.
- [jegrep](https://github.com/can1357/jegrep), MIT, by Can Bölük — indexed-free semantic path/range search and probability presentation pattern.
- [jgrep](https://github.com/keltokhy/jgrep), MIT, by Khaled Eltokhy — Boolean composition and batched yes/no classification pattern.

No code from these repositories is included or translated in this design task. Their licenses are recorded for provenance; this attribution is not a claim that they are xMustard dependencies.

## Source map

**Local source snapshot:** `research/jevgrep` is the inspected algorithm and request-contract source; `research/jegrep` and `research/jgrep` hold sibling comparison. These shallow clones are research artifacts and are intentionally not required runtime dependencies. They are untracked per repository guidance; the upstream repository URLs, inspected commit hashes and relevant paths above make the evidence reproducible.

**xMustard contracts used:** `docs/plans/2026-09-25-parity-build-plan.md` (WS-18, WS-37, WS-38, WS-51); `docs/CONFIG.md` (reserved semantic switch and no model/threshold keys); `rust-core/src/search.rs` (RRF K=60, current lanes and deterministic order); `rust-core/src/index/areas.rs` (bounded area/member contract); `rust-core/src/index/fts.rs` (indexed chunks); `api-go/internal/mcpserver/tool_search.go` (existing tool contract); `docs/SECURITY.md` (secret-path and output protections).
