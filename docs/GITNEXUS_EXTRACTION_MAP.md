# GitNexus Extraction Map

This is a ruthless extraction map for `research/gitnexus`.

It is not a product review. It is a decision document for xMustard.

## What GitNexus Actually Is

GitNexus is a TypeScript monorepo built around four serious ideas:

1. a multi-phase ingestion pipeline over source code
2. a semantic model and knowledge graph
3. an MCP-first tool surface for agents
4. hybrid search and impact analysis over indexed repos

The implementation spine is real:

- ingestion pipeline and orchestrator:
  - `research/gitnexus/gitnexus/src/core/run-analyze.ts`
  - `research/gitnexus/gitnexus/src/core/ingestion/pipeline.ts`
  - `research/gitnexus/gitnexus/src/core/ingestion/pipeline-phases/*`
- semantic model:
  - `research/gitnexus/gitnexus/src/core/ingestion/model/semantic-model.ts`
- MCP backend and tool surface:
  - `research/gitnexus/gitnexus/src/mcp/local/local-backend.ts`
  - `research/gitnexus/gitnexus/src/mcp/tools.ts`
- search:
  - `research/gitnexus/gitnexus/src/core/search/bm25-index.ts`
  - `research/gitnexus/gitnexus/src/core/search/hybrid-search.ts`
- group and cross-repo analysis:
  - `research/gitnexus/gitnexus/src/core/group/service.ts`

The repo is not just a README trick. It has real machinery.

## Steal Now

These ideas fit xMustard directly and should influence implementation soon.

### 1. MCP-native repo intelligence surface

GitNexus treats agent tools as a product surface, not a side effect.

What to steal:

- typed repo tools as first-class backend contracts
- one local backend that serves CLI, MCP, and HTTP
- “query/context/impact/detect_changes” style task surfaces instead of raw graph exposure

Why it fits xMustard:

xMustard is becoming a repo intelligence tool consumed by other shells. That means the tool surface is the product.

What xMustard should do:

- keep expanding typed tool endpoints
- add `definitions`, `references`, `diagnostics`, `impact`, `recent_failures`, `run_targets`, `verify_targets`
- keep CLI, API, and future MCP responses aligned to the same contracts

### 2. Explicit ingestion pipeline phases

GitNexus has a real phase graph instead of “scan everything in one pass.”

What to steal:

- explicit phase boundaries
- progress and timing by phase
- typed outputs between phases
- ability to skip or rerun specific layers later

Why it fits xMustard:

xMustard is about to add `tree-sitter`, `ast-grep`, LSP, search, and DB indexing. Without phase boundaries it will turn into an opaque scanner blob.

What xMustard should do:

- split future indexing into phases like:
  - repo scan
  - manifest/runtime discovery
  - tree-sitter symbol extraction
  - ast-grep rule indexing
  - LSP enrichment
  - search materialization
  - change/impact materialization

### 3. Semantic model as a source of truth

GitNexus does not stop at file chunks. It builds symbol registries and treats them as the authoritative resolution layer.

What to steal:

- a semantic model that owns symbol-level truth
- one place for symbol/file/method/type lookup
- separation between write-time population and read-time query use

Why it fits xMustard:

Right now xMustard has context packets and repo summaries, but not a semantic brain. It needs a canonical symbol layer before impact analysis becomes trustworthy.

What xMustard should do:

- create symbol/file/edge tables in Postgres
- treat that as the source for explainers and impact
- do not let every feature invent its own mini-index

### 4. Staleness and sibling-clone detection

GitNexus is unusually good at admitting that an index may be stale or built from a different clone.

What to steal:

- explicit stale-index checks
- sibling-clone drift detection by remote URL and commit
- user-visible warnings instead of silent wrong answers

Why it fits xMustard:

xMustard already cares about worktree truth. This is exactly the sort of honesty it should keep.

What xMustard should do:

- store indexed head SHA and repo fingerprint
- detect when current worktree has drifted from index baseline
- surface “results may be stale” in repo-state and issue-context packets

### 5. Hybrid search with clear fallback behavior

GitNexus combines BM25 and semantic search with RRF, and it is explicit about exact-scan fallback when vector indexing is unavailable.

What to steal:

- lexical + semantic + structural retrieval
- simple rank fusion
- explicit fallback modes

Why it fits xMustard:

The user already wants Postgres and semantic search. This is the right retrieval posture.

What xMustard should do:

- use Postgres FTS/BM25-style lexical ranking as the default floor
- add embedding retrieval later
- fuse lexical, semantic, and structural evidence instead of pretending one index is enough

### 6. Shared contract package discipline

GitNexus uses `gitnexus-shared` for schema constants, graph types, and scope-resolution contracts across packages.

What to steal:

- narrow shared contract modules
- one authoritative home for schema and type constants
- package boundaries that reduce drift between backend surfaces

Why it fits xMustard:

xMustard already has Python, Go, and Rust moving toward the same product. Shared contracts matter.

What xMustard should do:

- keep a small contract-first shared layer for schema and tool shapes
- use it to align Python, Go, Rust, and future MCP surfaces
- avoid broad monorepo sprawl while still preventing contract drift

## Adapt Later

These are good, but xMustard should not try to absorb them immediately.

### 1. Cross-repo groups and contract bridge

GitNexus has real multi-repo group orchestration and cross-repo contract matching.

Why later:

xMustard does not yet have a strong single-repo semantic core. Cross-repo reasoning before that will create noisy magic.

What to keep in mind:

- workspace groups
- cross-repo contract extraction
- service boundary mapping
- cross-repo impact

### 2. Scope-resolution replacement pipeline

GitNexus is actively replacing legacy resolution paths with a registry-primary scope-resolution pipeline.

Why later:

This is powerful but expensive. xMustard should not start by building a language-theory lab.

What to carry forward:

- language-specific resolution hooks
- explicit migrated-language gating
- parity testing between old and new resolution paths

### 3. Graph-assisted rename and code transformation tools

GitNexus has rename as a first-class tool.

Why later:

xMustard needs trustworthy definitions/references/impact first. Rename before that is performance art.

### 4. Embedding-heavy enrichment pipeline

GitNexus has a fairly serious embedding pipeline with chunking, hashes, and exact-scan fallback.

Why later:

xMustard should first get:

- Postgres storage
- tree-sitter indexing
- ast-grep search
- LSP enrichment

Then add embeddings on top.

### 5. Graph explorer web UI

GitNexus has a graph-focused browser UI.

Why later:

xMustard’s immediate UI problem is not “missing graph canvas.” It is “wrong center of gravity.” The product should become a repo cockpit first.

### 6. Wiki generation

GitNexus has a real graph-backed, incremental wiki pipeline, not a fake README feature.

Why later:

xMustard should not build docs generation before its semantic core is trustworthy, but the feature is worth keeping in the roadmap.

What to carry forward:

- graph-backed overview generation
- subsystem and module pages
- incremental regeneration keyed to repo changes
- generated docs that can be anchored to runbooks, verification, and issue history
- review-first artifact flow where a generated module map can be inspected and edited before full generation

Do not copy blindly:

- GitNexus mixes current source files with previously indexed graph state, which can make docs stale in subtle ways
- its nested incremental invalidation is fragile
- its module grouping fallback is inspectable but low-confidence

### 7. Monorepo/package boundary structure

GitNexus’s package split is doing real work:

- `gitnexus/` for CLI, MCP, and ingestion
- `gitnexus-web/` for the client
- `gitnexus-shared/` for shared types and schema constants

Why later:

xMustard does not need to restructure around monorepo packaging immediately, but the boundary discipline is useful as the Go, Rust, and Python layers deepen.

The specific lesson:

- keep shared contract definitions
- avoid manual package choreography and publish hacks
- if xMustard grows more packages, use real workspace discipline or keep the boundary simpler

## Ignore

These are not bad, but they are not xMustard priorities.

### 1. Editor-specific skill and hook sprawl

GitNexus spends real energy on:

- Claude hooks
- generated skills
- editor-specific setup paths
- AGENTS/CLAUDE file management

This is fine for their product. It is not the core thing xMustard needs right now.

### 2. Hosted web/demo posture

The web UI and browser-first exploration story are useful, but not central to xMustard’s immediate build.

## Conflicts With xMustard Direction

These are not “bad ideas.” They are architectural or product conflicts.

### 1. LadybugDB as the storage center

GitNexus is graph/DB centered around LadybugDB.

xMustard has already chosen Postgres as the primary store. That should hold.

Take the idea:

- symbol tables
- edge materialization
- search indexes

Do not take the storage engine decision.

### 2. Repo-centric product center without strong operational memory

GitNexus is much stronger on code intelligence than on durable run/issue/verification memory.

xMustard should not collapse into “GitNexus clone with a different name.”

Keep xMustard’s advantages:

- issue/work context packets
- verification profiles
- run history
- plan ownership and revision tracking
- replay/eval artifacts

This is xMustard’s moat:

- semantic repo intelligence tells you what code means
- operational memory tells you what happened, what was tried, what passed, what failed, and what was accepted

GitNexus is stronger on the first half today. xMustard should win by combining both.

### 3. Auto-mutating AGENTS.md / CLAUDE.md as primary context strategy

GitNexus writes inline context blocks into repo docs.

That is useful, but it should not be xMustard’s primary substrate.

xMustard should prefer:

- repo config
- structured context packets
- typed tool responses
- explicit runbook and guidance objects

Markdown should be export and handoff, not the control plane.

### 4. License shape

GitNexus is under a PolyForm Noncommercial license in the repo metadata.

That means:

- do not vendor code from it into xMustard
- do not lift implementation verbatim
- treat it as design research, not a copy source

This is not optional.

## Concrete xMustard Decisions From This Read

### Build next

1. tighten indexing boundaries for large repos
2. add `tree-sitter` symbol extraction
3. add `ast-grep` semantic pattern search
4. materialize symbol/file/edge/search tables in Postgres
5. expose tool surfaces for definitions/references/impact/diagnostics

### Quality bar to borrow

GitNexus has a real quality posture around:

- typechecking
- unit and integration test separation
- web E2E for UI flows
- explicit architecture docs for package ownership

xMustard should borrow that seriousness even if it does not copy the stack.

In practice:

1. semantic indexing work should ship with targeted regression tests
2. API/tool contracts should be checked for drift
3. wiki generation should not ship without freshness and invalidation tests
4. multi-language indexing should have fixture-based tests, not only happy-path demos

### Keep from xMustard

1. issue/work context packet
2. verification profiles and reports
3. run, plan, fix, and review records
4. durable activity and replay artifacts
5. operator-facing repo/runtime state
6. wiki generation as a future consumer of semantic truth plus operational memory

### Do not do

1. do not replatform xMustard onto LadybugDB
2. do not copy GitNexus plugin/hook sprawl first
3. do not build graph UI before semantic truth is stable
4. do not chase cross-repo groups before single-repo indexing is good

## Bottom Line

GitNexus is worth studying because it has a stronger semantic repo brain than xMustard.

But xMustard should not become GitNexus.

The right move is:

- steal the semantic indexing and tool-surface ideas
- keep xMustard’s operational memory and verification spine
- keep wiki generation on the roadmap once the semantic core is good enough
- rebuild the semantic layer in xMustard’s own architecture with Postgres, `tree-sitter`, `ast-grep`, and later LSP

That is the real merge of the ideas without inheriting the wrong center.
