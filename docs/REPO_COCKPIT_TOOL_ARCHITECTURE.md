# Repo Cockpit Tool Architecture

This document reframes xMustard from a tracker-first local bug operations app into a
standalone repo intelligence and agent grounding tool.

It is intentionally blunt about product center:

- xMustard should not be merged into MustardCoPilot
- xMustard should be consumable by MustardCoPilot as a tool or service
- the tracker-shaped surfaces in xMustard should stop being the primary product story
- the new product center is repo reality, execution reality, and verification reality

## Product Boundary

xMustard v2 is a standalone tool that answers:

- what repo is this
- what changed
- what is dirty right now
- what is open or broken
- how do I run it
- how do I verify it
- what code matters for this task
- what is the likely impact of a change
- what happened recently in this workspace

MustardCoPilot should call into xMustard to retrieve those answers. It should not absorb
xMustard's internal storage, routes, or UI code directly.

The product advantage should stay explicit:

- semantic repo intelligence explains code structure, change impact, and execution shape
- operational memory preserves what happened across runs, plans, fixes, verification, and review

xMustard should keep both. Repo intelligence without operational memory is not enough.

That operational memory should include first-class plan tracking:

- plan ownership
- plan version history
- attached repo-relative files
- branch and head snapshots
- dirty-path capture at revision time

The first durable Postgres shape should explicitly make room for both sides of that moat:

- semantic tables such as `files`, `symbols`, `file_symbol_summaries`, `semantic_queries`, `semantic_matches`, and `diagnostics`
- operational-memory tables such as `activity_events`, `run_records`, `run_plans`, `run_plan_revisions`, `verification_profiles`, `verification_runs`, and `issue_artifacts`

## Non-Goals

- Do not build a second thread manager or agent shell inside xMustard.
- Do not make markdown the primary operating substrate.
- Do not keep queue-first tracker workflows as the main product identity.
- Do not chase route parity if it does not improve agent understanding.
- Do not merge the xMustard app into MustardCoPilot.

## Keep As-Is

These parts already fit the repo cockpit direction and are worth preserving mostly intact.

- `IssueContextPacket`
- `WorktreeStatus`
- verification profiles
- activity ledger
- `.xmustard.yaml` repo config
- terminal transport
- replay and eval artifacts
- ticket context
- threat models
- browser dumps
- vulnerability records

These form the current memory and evidence shell.

## Upgrade

These parts are useful, but need stronger implementation and a different center of gravity.

### Repo map

Today the repo map is mostly structural summary plus key-file inventory.

Upgrade it into:

- symbol-aware code maps
- enclosing scope context
- path-to-symbol explanations
- impacted test and entrypoint hints

### Dynamic context bundle

Today it is a shell around shallow ranking.

Upgrade it so it is fed by:

- LSP
- `tree-sitter`
- `ast-grep`
- structural graph data
- semantic search

### Retrieval ledger

Keep the ledger, but make it explain:

- why this symbol entered context
- why this definition or reference matters
- why this subsystem is relevant
- why this test or verification target was selected

### Prompt assembly

Prompt assembly should become a projection step, not the source of truth.

The source of truth should be structured repo state, structured semantic context, and
structured execution state.

### Runtime layer

Upgrade the runtime layer from prompt launching into:

- managed execution
- structured run state
- structured verification state
- failure provenance
- bounded output capture
- durable summaries

Plan tracking belongs here too. Plans should be durable runtime-adjacent artifacts with:

- ownership and assignment state
- versioned revisions
- attached files and symbols
- git context at each revision
- links to runs, fixes, and verification

### Frontend

The current components can survive, but the information architecture should move from:

- issues
- runs
- signals
- review

to:

- repo state
- change state
- execution state
- verification state
- risk state
- intelligence inspector

### Migration plan

Keep the Go and Rust direction, but shift the priority toward:

- code intelligence
- managed execution
- semantic indexing
- retrieval quality
- shared contract definitions across surfaces

instead of route-for-route parity as a success metric.

## Missing Entirely

These are the core missing systems that would make xMustard excellent for agents.

### LSP layer

- go to definition
- find references
- hover
- document symbols
- workspace symbols
- diagnostics
- implementation lookup
- type definition lookup
- rename readiness

### `tree-sitter` layer

- symbol extraction
- enclosing scope recovery
- structural chunks
- language-aware syntax graph

### `ast-grep` layer

- semantic pattern search
- rule-driven issue matching
- fix-shape detection
- contract and impact rules

### Persistent database

Use PostgreSQL as the primary persistent store.

Why:

- durable multi-table joins
- stronger indexing than flat JSON
- better event and artifact history storage
- hybrid retrieval support
- BM25-capable text search support through the Postgres stack we choose

The current foundation schema should be treated as the first durable contract, not a placeholder:

- semantic storage for parser-backed summaries and ast-grep-shaped matches
- ops-memory storage for versioned plans and revisions
- search-document columns for files, symbols, semantic matches, plans, and issue artifacts

The current JSON files can remain as migration input and compatibility artifacts, but not
the long-term primary store.

### Semantic search

Add hybrid retrieval across:

- code
- symbols
- issues
- tickets
- runs
- fixes
- diagnostics
- verification records
- docs

Ranking should combine:

- lexical relevance
- BM25 text ranking
- structural relevance
- graph proximity
- optional embedding similarity

### Wiki generation

Keep wiki generation on the roadmap.

It should be built after the semantic core is trustworthy and should consume:

- semantic repo structure
- subsystem boundaries
- execution and verification knowledge
- runbooks
- operational history where it adds real context

### Semantic repo graph

Add first-class graph models for:

- symbols
- files
- callers and callees
- imports
- inheritance and interface edges
- tests to code links
- issue to symbol links

### Impact analysis

The system should answer:

- what symbols changed
- what callers are affected
- what files depend on this
- what tests likely need to run
- what public contracts may break

### Runtime and project discovery

Detect and persist:

- entrypoints
- run targets
- build targets
- test targets
- lint targets
- config files
- env variables
- Docker and compose services
- process and service graph

### Change intelligence

Add durable answers for:

- changed since last run
- changed since accepted fix
- changed since last agent touch
- dirty symbols, not only dirty files

### Code explainer

Add first-class explainers for:

- file/module purpose
- symbol purpose
- subsystem purpose
- change impact
- how to run a subsystem
- how to verify a subsystem
- why a failure happened

### Session grounding layer

Add a layer that can answer:

- what happened since this thread last touched the repo
- what is currently broken
- what is blocked by dirty state
- what is blocked by failing verification

### Ownership and subsystem model

Add durable models for:

- subsystem map
- code clusters
- hotspots
- likely owners
- likely blast radius

The important distinction:

- repo graph answers structural questions
- operational memory answers workflow and ownership questions

Both need to be first-class.

## Reframe Or De-Emphasize

These surfaces are not wrong, but they should stop being the product center.

- queue-first tracker framing as the home screen
- saved views as a headline feature
- source inventory as a first-class pane
- tracker-shaped CRUD as the dominant interaction model
- markdown-heavy operation flow
- migration work that only demonstrates parity

Keep them where they are still useful, but move them behind the repo cockpit story.

## Target Architecture

xMustard should converge on four layers.

### 1. Ingestion Layer

Responsibilities:

- git and worktree scanning
- manifest scanning
- config and env discovery
- issue and artifact ingestion
- event and activity capture

### 2. Semantic Intelligence Layer

Responsibilities:

- LSP session management
- `tree-sitter` parsing
- `ast-grep` query execution
- symbol extraction
- graph building
- diagnostics capture

### 3. Knowledge Layer

Responsibilities:

- PostgreSQL persistence
- BM25-capable text search
- semantic retrieval indexes
- graph joins
- replay and eval history
- artifact lineage

### 4. Delivery Layer

Responsibilities:

- structured context packet generation
- tool and API surfaces
- explainers
- wiki and generated documentation surfaces
- review and export projections
- MustardCoPilot tool integration boundary

## Package Boundary Discipline

GitNexus is a useful reminder that package boundaries matter when a system has multiple delivery surfaces.

xMustard does not need GitNexus-style JS monorepo complexity, but it does need:

- a narrow shared contract layer for tool schemas and graph/storage constants
- clear boundaries between semantic indexing, storage, tool delivery, and UI
- contract alignment between Python, Go, Rust, and future MCP surfaces

This should be implemented as focused shared contracts, not as broad packaging theater.

## MustardCoPilot Boundary

MustardCoPilot should treat xMustard as a tool or service.

That means MustardCoPilot can ask xMustard for:

- repo state
- changed files and changed symbols
- definitions and references
- diagnostics
- impact analysis
- run targets
- verification targets
- context packets
- recent failures
- code and subsystem explanations

But MustardCoPilot should not take on:

- xMustard's internal persistence model
- xMustard's route inventory
- xMustard's tracker UI
- xMustard's migration work

## Core Tool Surface

The first stable external tool surface should include:

- `repo_state`
- `repo_summary`
- `changed_since`
- `definitions`
- `references`
- `diagnostics`
- `impact`
- `run_targets`
- `verify_targets`
- `issue_context_packet`
- `recent_failures`
- `code_explainer`
- `subsystem_explainer`

## Success Criteria

xMustard is succeeding as a repo cockpit tool when an agent can ask:

- what changed
- what matters here
- how do I run this
- how do I verify this
- what symbols are involved
- what diagnostics already exist
- what is risky to touch

and get structured, current, evidence-backed answers without depending on markdown notes or
manual human reconstruction.
