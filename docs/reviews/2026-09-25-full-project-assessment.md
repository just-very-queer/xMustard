# xMustard full project assessment — 2026-09-25

Scope: the whole repository on `feat/product-v1` as it sits in the working tree
today, including the uncommitted September candidate. Method: the vision,
status, architecture, roadmap, rethink, context-layer, parity, and history
documents were read in full; the Go shell, the Rust core, the end-to-end path of
three MCP tools, and repository hygiene were each read by a dedicated pass; the
four claims that carry the most weight were then re-verified directly in source.
Nothing was modified, built, or run beyond `go vet`. Line references are to the
working tree, not to HEAD, unless stated.

This is an opinion document. It is meant to be argued with.

---

## 1. The vision, taken in full

The vision as confirmed on 2026-09-24 has more parts than the README's two-line
summary suggests. Every part is listed here, because the assessment that follows
judges the code against all of them, not against a compressed version.

### 1.1 The one-sentence product

**xMustard provides shared, verified memory and repository intelligence for
existing coding agents.** It sits above the tool and context interfaces those
agents already support, and helps them establish what is true now, retrieve the
relevant evidence, and carry trustworthy knowledge between sessions.

This is a good sentence. It names a customer (existing agents, not a new agent),
a posture (above their interfaces, not replacing them), and three verbs
(establish, retrieve, carry). Every later scope decision can be tested against
it.

### 1.2 The accepted direction, item by item

1. **Work with Codex, Claude Code, and OpenCode through MCP and typed CLI/HTTP
   access. The agent-facing product is the priority.**
   Assessment: correct and delivered at the interface level. The MCP shim is
   nine tools with strict argument validation. The rest of this document is
   about what is behind that interface.

2. **Support concurrent agents in one repository, switching agents across
   sessions, and knowledge shared across explicitly authorized repositories,
   delivered in stages.**
   Assessment: the first workflow works for many shims against one API daemon.
   The second works by construction, because memory lives in files not in a
   session. The third has no implementation and, more importantly, the current
   storage layer is the wrong foundation for it. See section 6.

3. **Add tool-context reduction: select relevant tools and source evidence,
   reduce repetitive results, and provide access to retained originals.
   Client-specific integration determines which calls can actually be observed
   or transformed.**
   Assessment: the narrow version is built and is the best-engineered part of
   the codebase. It applies only to xMustard's own nine tool outputs and to one
   Pi extension. It does not and cannot see other MCP servers or the host's
   native tools. The docs say this clearly. The gap between "reduce what agents
   consume" and "reduce what xMustard emits" is the whole product question for
   this item, and it is unresolved.

4. **Investigate Cactus Needle and Typesafe Jev for tool selection and
   structured decisions, distinguishing local artifacts from hosted services.
   Model suggestions do not confer execution permission or verified-memory
   status. The default must remain useful without a helper model or hosted
   decision service.**
   Assessment: research exists, nothing is wired. The constraint that the
   default must be useful without a helper is the right constraint and the
   current code honors it by having no helper at all.

5. **Agents may prepare changes and verification evidence. The human is the
   final authority for merging code. Memory promotion, run approval, and Git
   merge approval are separate decisions.**
   Assessment: this is a policy statement, and the docs are careful to say it
   is not enforced anywhere. That honesty is good. Nothing in the repository
   currently binds an approval to a revision. The vision itself lists
   revision-bound approval as a proof requirement, not as a feature.

6. **Keep the default local, without Docker, targeting roughly 50–100 MB for
   the active xMustard-owned process tree. This is a design target, not a
   measured guarantee; report external agent/compiler processes separately.**
   Assessment: met at rest, on one fixed workload, on one machine. The sampled
   peaks are 72.3, 80.6 and 84.9 MB. The design is not lightweight in CPU or
   process count, which the target does not measure. See section 5.

7. **Compare against GitNexus, Serena, Augment, Sourcegraph, Mem0, Letta, and
   Zep/Graphiti together.**
   Assessment: the parity review is thorough and honest. It finds no peer with
   the exact combination of distinct-principal approval plus file-hash recheck
   on recall. It also finds that xMustard has not benchmarked itself against
   any of them.

8. **UI development is outside the current focus. Existing issue/run/evaluation
   and provider surfaces can support the product without defining its scope.**
   Assessment: the first half is respected. The second half is where the
   repository's weight comes from. "Can support without defining" has become
   "remains in the default build, in the default route table, and in the
   default data directory." See section 9.

9. **"Cheaper than OpenHands" means a lighter harness, lower memory overhead
   and data movement, context shedding, and better code indexing, with Pi as
   the reference harness. Lower API prices are not the objective. Measure
   working-set size, allocations, repeated file/index reads, context delivered,
   latency, and task quality separately. Hardware memory bandwidth is a distinct
   measurement.**
   Assessment: this is the most demanding item and the least measured. Of the
   six named measurements, one (working-set size) has a number. Repeated
   file/index reads is the one where the current design does worst, and it has
   no number.

### 1.3 The six ideas to preserve

1. **Ground before acting.** Delivered by `ground`. The payload is small, the
   compute behind it is not.
2. **Give memory an explicit trust lifecycle.** Delivered. Proposal,
   verification, promotion, edits, drift, and conflicts are all inspectable
   records. This is the moat.
3. **Return small, useful evidence.** Delivered for `ground`, `recall`, and
   `search`. The retrieval ledger ("why each piece was selected") exists as a
   `reason` string per hit, which is thinner than the vision's wording.
4. **Learn from outcomes.** Partially built. Retrieval feedback and verifier
   telemetry are persisted. Nothing closes the loop from outcome back to
   ranking in a way that has been measured.
5. **Keep one owner for each kind of truth.** Go delivers, Rust means. The
   principle is right and mostly followed. The exceptions matter: two
   independent repository-identity schemes in Rust, and two wire-compatible
   goal runtimes, one in each language.
6. **Reduce context while retaining evidence.** Delivered in the narrow form
   described under item 3 above.

### 1.4 The proof the vision itself demands

The vision lists six proofs. None is complete. In order of how far each is:

| Proof required | State |
| --- | --- |
| Correctness under repeated edits, concurrent agents, cancellation, restart | Repeated edits: fixed in the working tree with regression tests. Concurrent agents: one process only. Cancellation: threaded through the bridge now. Restart: evidence handles survive; memory promotion state survives by construction. |
| Honest coverage | Symbol truncation is now reported. Language coverage is four grammars plus regex fallbacks and is not surfaced to the agent in tool output. |
| Held-out comparison of four memory arms | Statistical core exists. No corpus, no executor, no run. |
| Outcomes measured as accepted patches, localization, cost, stale-memory harm, promotion mistakes, handoff success, process-tree resource use | Only the last has a number. |
| Compression compared with unmodified output | A fixed MCP fixture passes. No paired task comparison. |
| Human merge approval bound to revision | Stated as a target. Not built. |

The pattern is consistent: everything that can be proven with a fixture is
proven, and everything that requires running an agent on a real task is not.

---

## 2. What exists today

The architecture as built, not as drawn:

```text
agent -> xmustard-mcp (stdio, 9 tools, 1.8k lines Go)
      -> xmustard-api (HTTP :8042, 4.3k-line main.go, 204 routes + ~11 evidence routes)
           -> workspaceops (170 files, 47.7k lines, 519 exported functions)
           -> evidence (3.1k lines, 83% coverage)
           -> budget (0.5k lines)
           -> rustcore bridge (1.9k lines) -> exec xmustard-core per call
                -> rust-core (20.6k lines, 20 modules, 4 tree-sitter grammars)
           -> JSON files under backend/data/workspaces/<ws>/
           -> optional Postgres mirror
frontend (12.8k lines React) -> the ~190 routes the core-only flag disables
integrations/pi (0.9k lines TS) -> the API directly, bypassing the shim
```

Sizes that matter:

| Surface | Lines | Serves the nine tools? |
| --- | --- | --- |
| MCP shim | 1,822 | yes, entirely |
| `context_governance.go` + grounding + the nine handlers | ~4,000 | yes |
| `evidence` + `budget` | ~3,600 | yes |
| Rest of `workspaceops` | ~40,000 | mostly no |
| `cmd/xmustard-api/main.go` route wiring | ~3,700 of 4,333 | no |
| `xmustard-ops` CLI | 942 | no |
| Rust: symbolgraph, indexcache, treesitter, search, changetrack, repomap, diagnostics, lsp | ~11,000 | yes |
| Rust: `models.rs` | 3,689 | no, zero consumers |
| Rust: goalruntime, swarm, benchmark, wiki | ~2,600 | no |
| Frontend | 12,823 | no |

Roughly 45 to 50 percent of tracked source plausibly serves the product the
vision describes. The other half is the "repo cockpit" platform from the spring
and early summer: issues, runs, plans, terminals, goals, swarms, providers,
evals, security dispositions, Postgres materialization, and the kanban UI on top
of them. `docs/RETHINK.md` diagnosed this in June and chose to leave it in
place rather than cut it. That choice has a cost every day it stands, and
section 9 quantifies it.

---

## 3. The memory kernel

This is the part worth protecting. It deserves a precise description.

### 3.1 Data model

A `ContextEntry` (`api-go/internal/workspaceops/context_governance.go:31-66`)
carries an id, title, content, source principal, a permission of `readonly` or
`readwrite`, a status of `pending`, `verified`, or `rejected`, a `Promoted`
flag, a list of verifications each with agent, verdict, note, and time, the
required verification count, the referenced paths, a SHA-256 per referenced
path captured at promotion, and precomputed search tokens. Staleness is
computed at read time and never stored.

### 3.2 Promotion rule

`reconcileEntry` (`:521-541`) takes the latest verdict per agent, case
insensitive. When the threshold is greater than one, the author's own vote is
discarded. Rejections at or above threshold reject; approvals at or above
threshold promote; otherwise the entry stays pending. Defaults are
`require=true, threshold=2` (`:475-490`). A per-request override can only
tighten the requirement, never loosen it (`:572-580`). In single-agent mode
the proposer self-promotes at propose time (`:602-607`).

The verify handler discards any caller-supplied agent name and substitutes the
authenticated `Principal.ID` (`api-go/cmd/xmustard-api/main.go:3904-3908`);
propose does the same for `source` (`:3873-3877`). Token rotation preserves the
principal (`auth.go:291`). So the "distinct principals" gate is real when auth
is configured: an agent cannot vote twice by inventing names.

### 3.3 Drift on recall

At promotion, every referenced path is hashed, with a `"\x00missing"` sentinel
for absent files so that a file appearing later also counts as drift
(`:133-152`). On recall, only a bounded candidate window is re-hashed:
`max(4 × limit, 16)` entries (`:1057-1067`). Stale entries are penalized by
one point and flagged, never hidden.

### 3.4 Content binding

Ranking runs over a metadata cache. The content for only the returned window is
read from per-id, hash-named files and checked against the stored digest. A
mismatch retries up to three times and then withholds the entry rather than
serve unverified text as verified (`:820-833`, `:975-1000`). This was added
because the baseline audit reproduced a race where concurrent edits could
return unverified text under a verified header. It is the right fix.

### 3.5 Ranking

Bag-of-words. Query tokens are lowercase `[a-z0-9_]` of three or more
characters. Each matching token scores +1.0; each path overlap with the supplied
paths, or with the current working-tree changes when no paths are supplied,
scores +1.5; each distinct approval +0.25; the newest entry +0.5; a stale entry
−1.0 (`:920-955`). With a query, anything scoring zero or less is dropped.
Default top-N is 8, maximum 50. There is no IDF, no stemming, no embedding.

For stores of tens to low hundreds of entries this is fine and has the virtue
of being explainable. It will not scale to thousands of memories across
repositories, which the vision's third workflow implies. That is a future
problem, and a cheap one to solve when it arrives.

### 3.6 Conflict detection

Two active memories that cite the same file are reported as a conflict
(`:1109-1144`). The code comment admits this is path overlap, not a semantic
contradiction. The tool description should say the same.

### 3.7 Assessment

Sound. Small. Defended in depth. Commented with issue references. This is the
kind of code that survives a rewrite of everything around it, and it should.

Two defects sit right next to it and should be fixed before anyone else uses
the product:

- **Open mode cannot promote.** With no tokens configured, every caller
  resolves to `"anonymous"` (`main.go:464,699,3875`). The author is excluded
  from the vote when threshold is two, and every verifier is the author. So
  `remember` stays pending forever and `recall` returns nothing until an
  operator discovers `require_multi_agent_verification`. The default local
  install, which the README walks a new user through, is therefore a dead end.
  Neither README nor STATUS mentions this. Fail-closed is defensible; silent
  fail-closed is not.
- **`PUT /context/{entry_id}` is ungated.** The route (`main.go:3912-3922`)
  has no role check and no author binding. Any agent-role caller, or anyone in
  open mode, can rewrite any `readwrite` entry, which resets its verifications
  and demotes it. In a product whose thesis is that memory cannot be altered
  without a trust event, this is a hole in the thesis.

---

## 4. Grounding and retrieval, tool by tool

### `ground`

Composes four things (`grounding.go:93-155`): drift since baseline, working
tree changes with contract-break flags, failed runs, and stale-memory counts.
The payload with no changes is about 1 KB, 300 to 400 tokens, using counts not
lists. Small and high signal, as claimed.

The cost is behind it. One call spawns four `xmustard-core` processes: two
repository-identity samples from the evidence middleware, one `drift`, one
`working-changes`. Each Rust process shells out to git two to five times. Total
is twelve to eighteen processes. The `drift` subcommand runs `git ls-files`
and then SHA-256s every tracked file, every call, with no cache
(`rust-core/src/changetrack.rs:88-98`). On a small repository this is
milliseconds. On a 50k-file monorepo it is seconds per call, and `ground` is
the tool the README tells agents to call first.

### `recall`

Described in section 3. Two Rust spawns with a query (both from the identity
middleware), three without (one `working-changes` to find the paths to boost).

### `remember` and `verify`

Thin handlers over the kernel. Each is a full load, marshal, and rewrite of the
workspace's entry array, so cost is linear in history per write. Fine at
current scale. `RecordFeedback` errors are silently dropped (`:613,667`).

### `search`

Rust `hybrid_search` (`rust-core/src/search.rs:219-461`). Loads the cached
symbol graph or builds it under an advisory lock. Four lanes over symbol names:

- lexical: IDF over camel/snake subtokens of the symbol name, path tokens at
  0.3 weight
- "semantic": cosine over a 256-dimension FNV-hashed vector of tokens plus
  character trigrams, gated at 0.30
- structural: inbound edge weight
- proximity: 1 / (BFS depth + 1) from a seed or an exact-name auto-seed

Fused by reciprocal rank fusion with k = 60, plus 1.0 for an exact name match.
A separate docs lane scans every tracked documentation file in 30-line chunks
at a flat 0.05 × matched-fraction score, re-read from disk on every call. One
hit is `{kind, name, path, line, score, reason}` and the `reason` string names
the lanes that fired.

Honest capability: this is a good symbol-name search with typo tolerance and a
useful graph-proximity lane. It does not search function bodies. The
"semantic" lane cannot relate `authenticate` to `login`. A real embedding
exists only behind the `semantic-onnx` feature, which nothing in the build or
docs enables. The tool description an agent reads should say "symbol and
identifier search" and should not say "semantic."

Bounds are real and careful: 800 files, 100k symbols, 8 MiB per file, no
symlink following. The 32-symbol-per-file cap the audit found is fixed in the
working tree: the extractor cap is now 4096, truncation is reported in
coverage, and 32 survives only as a display limit with a warning
(`treesitter.rs:88-150`, `symbolgraph.rs:1598-1608`, `repomap.rs:420-470`).
STATUS still lists it as open and needs updating once this lands.

Two costs on this path that should not be there: `embed(&sym.name)` is
recomputed for every symbol on every query (`search.rs:307`), and Go performs
a read-modify-write of `feedback.json` on every search under the process-local
lock (`feedback.go:57-65`). A write on the hottest read path is a design smell
regardless of scale.

### `impact`

Graph BFS over the symbol graph. The graph is name-based: `analyze_source`
builds a per-file set of identifiers of four or more characters, and
`build_graph` emits a `calls` edge whenever any word in file B equals a
function name uniquely defined in file A (`symbolgraph.rs:549-555,
1687-1695`). Ambiguous names are dropped; a stopword list suppresses `main`,
`new`, `run`. Imports and inheritance are line-prefix and string scans.

This is a lead generator, not a call graph. It will report an edge from a
docstring that mentions a function. Your own memory note from an earlier
session records exactly this. The tool description should say "lexical
reference graph" and rank distance-one edges as leads. An LSP-upgraded graph
exists (`upgrade_graph_with_lsp`, `:1034`) but is reachable only through a
subcommand Go never calls.

### `explain`, `diagnostics`, `why_failed`

`explain` composes repo-map and symbol data per path; adequate.
`diagnostics` normalizes LSP and tool output with 200-item caps; adequate.
`why_failed` requires a run record that only the platform's
`POST /issues/{id}/runs` route creates. So one of the nine agent tools depends
on the half of the codebase the product says is not the product. Either the
run record needs a core-surface creator, or `why_failed` belongs to the
platform.

---

## 5. Runtime cost, against the "lighter harness" goal

The vision defines cheaper as: lighter harness, lower memory overhead and data
movement, context shedding, better indexing. Against each:

**Memory overhead.** Met at rest. One Go API, one shim per agent, Rust only
transient. The transient-byte budget refuses rather than allocates, and the
docs are honest that it is not an RSS ceiling. 80 MB sampled peak on a fixed
workload is a credible number.

**Data movement.** Not met, and unmeasured. Per `ground`: full-tree SHA-256.
Per any tool call: two identity samples, each of which runs `git status` and
hashes every dirty and untracked file up to 64 MiB. Per `search`: full graph
JSON deserialize, every doc file re-read, every symbol re-embedded. There is no
resident state at all; the only caches are the on-disk graph snapshot and the
memory metadata file. The roadmap itself says "calling a repeated filesystem
scan an index does not meet this gate." By its own standard, the current index
does not meet the gate.

**Context shedding.** Met for xMustard's own outputs above 64 KiB. Below 64 KiB
the projection is passthrough. Since `ground` and `recall` are almost always
under 64 KiB, in practice the evidence layer engages on `search`, `explain`,
and `diagnostics` on large repositories. That is the right place for it.

**Better indexing.** Better than nothing; not better than the peers named in
the parity review, which use incremental indexes or live language servers.
Four grammars. No incremental edge resolution: a cache miss recomputes edges for
every file. Live LSP is one-shot: a fresh language server is spawned and
initialized per hover or definition call (`lsp_session.rs:333-370`), and the
comment at `:414-417` says it cannot scale. The persistent session type exists
and is unused.

**Process count.** The child-slot budget defaults to four with a ten-second
wait. Two agents each calling `ground` saturate it. The third caller gets an
overload error. This will be the first thing a two-agent user notices.

The fixes are mostly cheap and do not change the architecture: cache the
fingerprint by HEAD plus the porcelain status output, sample identity once per
request and pass it down, drop the feedback write from the search path, reuse
the indexcache identity in changetrack instead of maintaining a second scheme,
and precompute symbol embeddings into the cache. A resident daemon is a bigger
decision and should follow a measurement, as the roadmap says.

---

## 6. Storage and the three workflows

Persistence is one JSON array per store under
`backend/data/workspaces/<ws>/`, rewritten in full on each write via
temp-and-rename. Writes are serialized by an in-process `sync.Mutex` keyed on
path (`storelock.go:26-52`). There is no file lock. Only the evidence store
fsyncs. Of 47 `writeJSON` call sites in `workspaceops`, nine are under the
lock; the governance path is locked, most of the platform is not.

Against the three accepted workflows:

- **Concurrent agents in one repo.** Works when every agent goes through one
  API process. The Pi adapter talks to the API directly, which is fine as long
  as it is the same API. The `xmustard-ops` CLI writes to the same files with
  no coordination with a running server. Two API processes on one data
  directory will lose updates.
- **Switching agents across sessions.** Works by construction.
- **Sharing across authorized repositories.** Not built, and the current
  store has no notion of a grant, a source workspace, or per-repository
  applicability. The context-layer doc's own warning applies: "existing
  in-process store locks do not serialize unrelated CLI/API processes writing
  the same files; adding more client adapters must not multiply independent
  writers." The right time to change the store is before the third workflow,
  not after.

SQLite in WAL mode would solve locking, fsync, atomicity, and the O(history)
rewrite in one move, and it is a single file, which keeps the no-Docker
default. At minimum, flock plus fsync on the governance files.

The optional Postgres mirror is a separate concern. It exists, it is tested
only with skips, and nothing in the nine tools needs it. It belongs to the
platform half.

---

## 7. Authentication

Tokens are SHA-256 at rest in a JSON file or an environment variable, compared
in constant time, with roles admin > agent > readonly, optional workspace scope,
and TTL (`auth.go:57-164`). Minting is admin-only. The non-loopback interlock
the audit flagged is fixed (`main.go:181-199`). The token file is re-read and
re-parsed on every request (`auth.go:176-180`), which is wasteful but not
wrong.

The two gaps are the ones in section 3.7: open mode cannot promote and does not
say so, and the content-edit route is ungated. Neither is hard to fix.

---

## 8. The Rust core

Twenty modules, 20.6k lines, `#![forbid(unsafe_code)]` crate-wide, 149 tests,
no panics on user input, bounded reads everywhere. The resource hygiene is
genuinely careful and better than most Go or Rust code of this size.

What is dead or unreachable:

| Module | Lines | Status |
| --- | --- | --- |
| `models.rs` | 3,689 | zero consumers anywhere; a Python-migration leftover |
| `goalruntime.rs` | 1,480 | reachable only via platform HTTP routes; Go has a wire-compatible duplicate |
| `swarm.rs` | 533 | no Go caller |
| `wiki.rs` | 407 | platform only |
| `benchmark.rs` | 177 | no Go caller |
| `symbolgraph build-lsp/clusters/flow/hotspots/blast-radius`, six `lsp-*` commands | — | no Go caller |

Of about 55 leaf subcommands, Go calls 27.

Is Rust justified? It is here for the tree-sitter crates and, secondarily, for
the bounded-I/O plumbing. Nothing in the crate is compute-bound in a way that
requires it: the graph is set intersection, search is IDF plus RRF over at most
100k names, changetrack is SHA-256 over files, goals are JSON CRUD. Go has
tree-sitter bindings through cgo and could do all of this in-process, which
would remove the per-call spawn, the graph deserialize, the string-only error
transport, and the duplicated goal and identity logic.

The counter-argument is real: a subprocess is a hard kill boundary with a
120-second timeout and a memory isolation line, and the vision's resource
target is easier to defend when the heavy work cannot leak into the daemon's
RSS. That is a defensible reason to keep the split. It is not a reason to keep
6k lines nobody calls, or to run the split four times per `ground`.

The split is defensible but not justified by what is built. Keep it if a
resident Rust index is the next step; fold it into Go if the index stays
on-demand.

---

## 9. The Go shell

`workspaceops` is 170 files and 47,700 lines with 519 exported functions and
43 package-level variables, spanning memory governance, issues, runs, goals,
LSP sessions, Postgres mirroring, OpenAI providers, security dispositions,
threat models, evaluations, and integrations. It is a god package. The largest
files are `project_info.go` at 2,581 lines, `context_packet.go` at 2,145,
`evals.go` at 1,927, and `integrations.go` at 1,454. None of the four is on the
nine-tool path.

`cmd/xmustard-api/main.go` is 4,333 lines with handler logic inline, 204 route
registrations, 24 percent test coverage, and the expression
`envDefault("XMUSTARD_DATA_DIR", "../backend/data")` repeated 186 times. Seven
handlers decode the body with an ignored error, so malformed JSON silently falls
through to query parameters (`:3853,3889`).

Coverage by package: `workspaceops` 71%, `evidence` 83%, `budget` 73%, MCP
shim 66%, `rustcore` 45%, API 24%, ops CLI 0%. The pattern is clear: the
product code is well tested, the platform code is not.

`XMUSTARD_CORE_ONLY=1` restricts the route table to about 29 of roughly 215
routes. It is opt-in. The default surface, and the default attack surface, is
everything.

Two API design wrinkles worth naming: `why_failed` depends on a platform-only
route to create its input, and the core-only allowlist is an exact-match
string list that will silently drop any new core route someone forgets to add.

---

## 10. The frontend

12,823 lines across 28 files. Kanban, queue, execution pane, terminal, goal
panel, admin panel, and a memory panel. It calls sixteen API prefixes, none of
which the core-only allowlist permits except the memory routes. It has an
admin UI for minting tokens but never sends a bearer header itself. Lint fails
on four hook rules.

The vision says UI is out of focus. The code says the UI is a client of the
retired platform, not of the product. Both can be true at once; the question is
whether it should remain in the default build and the default checks. It should
not.

---

## 11. Repository hygiene and process

This section is uncomfortable and it is the most important one.

**Commits.** 270 commits, one author under four name spellings, from
2026-03-31 to 2026-06-23, on 29 distinct days. 162 of the commits are in June
and 32 are on June 23 alone. Nothing has been committed since June 23. The
working tree holds 101 status entries, 42 of them untracked, and a diff of
+5,731 / −2,118 across 59 tracked files. The September candidate, including the
Rust indexing fixes with their regression tests, the entire evidence package,
the Pi adapter, the benchmark scripts, and every dated review, is uncommitted.

**Tracked data.** `backend/data` is tracked: 78 files, 4.6 MB, including 47
run records and six workspace snapshots up to 3.8 MB, all from the retired
Python era and all keyed to one machine's workspace ids. This is half of all
tracked bytes. The ignore rules for it post-date the commits and so do nothing.

**`.gitignore`.** The committed version ignores `docs/` entirely while 35 doc
files are tracked. The uncommitted rewrite replaces it with a 90-line
per-file allowlist. Under the current rules, 108 tracked files would be
ignored. The index and the ignore file disagree.

**Docs.** 123 markdown files and 24,256 lines under `docs/`, 35 tracked. Sixty
of the files (11,952 lines) are execution prompts titled things like
"hundred-tranche pass" and "true final python exit." Fifteen are same-day
model reviews. Thirteen are plans. 110 of 123 files mention "review"; 42 name
an AI model. About 79 percent of the documentation text is process artifact
from AI sessions. The 22 top-level files include at least eight overlapping
"what we are" documents: VISION, RETHINK, PLANNING, PLANNED_FEATURES, FEATURES,
FRONTIER, ROADMAP, and two migration histories.

**Untracked infrastructure.** `scripts/` (retrieval gate, RSS bench, MCP
evidence E2E, Pi harness) and `integrations/pi/` are untracked and not
referenced by the Makefile. The evidence every status table cites was produced
by scripts that are not in the repository and cannot be re-run by anyone else.
Three of those scripts are Python, in a repository whose README says Python
was retired.

**CI and release.** No workflows, no tags, no releases. The Homebrew formula is
HEAD-only and its tap does not exist. PR #3 is open, conflicting, with no
checks.

**Reading of the history.** The May transcripts (60 Codex sessions) show
breadth-first construction of a repo cockpit. June shows the Rethink and the
narrowing to nine tools, then a burst of hardening and 32 commits in one day.
September shows a review cycle: audit, candidate, re-audit, re-check, source
review, implementation report, benchmark record. Each of those documents is
careful and hedged. Together they add up to three months of work that has
produced no commit, no release, no CI, and no task-level measurement.

The vision's own phrase is "measure on a real task, not a feature count." The
project has not yet done that once. The harness has McNemar's test and
bootstrap intervals and four arms, and its header explicitly leaves task
execution to "additional infrastructure." The statistics were built before the
experiment.

---

## 12. Where the docs and the code disagree

The docs are unusually honest, so the list is short.

| Claim | Reality |
| --- | --- |
| STATUS: "Plain recall bypasses the bounded ranking path" | Fixed. `/context/active` goes through bounded recall; full dump is admin `scope=all`. |
| STATUS: "Non-loopback auth interlock admits AUTH=off" | Fixed at `main.go:181-199`. |
| STATUS: 32-symbol cap without truncation report | Fixed in the working tree. |
| STATUS: "six packages" | Seven. |
| README: "~196 REST routes" | 204 in main plus ~11 evidence routes. |
| README: per-principal identity is the normal setup | No token bootstrap in Makefile or packaging; open mode cannot promote. |
| PLANNED_FEATURES and CHANGELOG: per-proposal override toggles verification both ways; tools named `context_propose/verify/active` | Override only tightens; tools are `remember/verify/recall`. |
| Tool descriptions: "hybrid lexical + semantic" | Semantic is trigram hashing unless built with an optional feature. |
| Tool descriptions: "transitive references (graph BFS)" | Over a lexical reference graph, not a resolved call graph. |

---

## 13. Competitive position

The parity review's conclusion is the right one and worth restating without
softening: the combination of distinct-principal approval and file-hash recheck
on recall was not found in any of the seven peers. That is a real, narrow,
defensible distinction. It is also a hypothesis about outcomes, not a proven
benefit.

Where xMustard is behind: incremental indexing (GitNexus, Serena via LSP,
Augment), language coverage (all four repo-intelligence peers), real embeddings
(Augment, Mem0, Graphiti), and any published outcome evidence at all (Serena's
side-by-side, GitNexus's SWE-bench harness, Mem0's and Zep's LoCoMo runs).

Where xMustard could be ahead: nobody else makes an agent's memory fail loudly
when the code it describes changes. If that turns out to reduce stale-memory
harm on real tasks, it is a product. If it does not, it is a feature.

---

## 14. Risks, ranked

1. **The core hypothesis is unmeasured.** Everything else is secondary to
   whether governed memory changes task outcomes for an agent.
2. **Three months of work is uncommitted.** One disk failure or one careless
   `git checkout` loses the indexing fixes, the evidence layer, and the Pi
   adapter.
3. **First-run failure.** A new user following the README will propose a
   memory, never see it promoted, and conclude the product does not work.
4. **Per-call cost scales with repository size.** The first user with a large
   monorepo will see multi-second `ground` calls and overload errors with two
   agents.
5. **The platform half is the default.** Attack surface, build time, test
   time, and reader comprehension all pay for code the product does not use.
6. **Storage is single-process.** The moment a second writer appears, memory
   integrity claims stop being true.
7. **Naming oversells retrieval.** Agents will trust "semantic" and "call
   graph" more than the implementation warrants.

---

## 15. A recommended plan

Ordered so that each step makes the next one cheaper. Each has an exit
criterion, in the project's own style.

**Step 0. Commit.** Commit the candidate as it stands, including `scripts/`,
`integrations/pi/`, and the reviews. Exit: `git status` is clean and the
benchmark scripts can be run from a fresh clone.

**Step 1. Run the experiment.** Twenty tasks on one repository, one worker,
one model, fixed tool budget. Arms: memory off, governed memory. Score accepted
patches and stale-memory incidents. The harness's statistics already exist.
Exit: a number with a confidence interval, whatever the number is.
Consequence: if the lift is absent, stop building retrieval features and
reconsider the product; if present, everything below is worth doing.

**Step 2. Fix the two kernel defects.** Document open mode or default to
threshold one in open mode with a loud log line; add a role check and author
binding to the content-edit route. Exit: a fresh-install walkthrough promotes a
memory; an agent-role token cannot edit another principal's entry.

**Step 3. Pay down the per-call taxes.** Cache the fingerprint by HEAD plus
porcelain output; sample identity once per request; remove the feedback write
from search; reuse the indexcache identity in changetrack; precompute
embeddings. Exit: `ground` spawns one Rust process; a repeated `ground` on an
unchanged tree does no file hashing.

**Step 4. Make core-only the default and extract the kernel.** Move governance,
grounding, evidence, budget, the nine handlers, and the shim into their own
package or binary with an explicit store interface. Move issues, runs, goals,
terminal, providers, evals, security, Postgres, the ops CLI, and the frontend
to `archive/` or a separate module. Exit: the default binary registers only
the core routes; `make check-backend` runs in a fraction of the current time;
`why_failed` either has a core-surface run creator or is dropped to eight tools.

**Step 5. Delete the dead Rust.** `models.rs`, `swarm.rs`, `benchmark.rs`,
`wiki.rs`, the unreachable subcommands, and `goalruntime.rs` once Go's copy is
the only one. Exit: every subcommand has a Go caller or a test that is the
caller.

**Step 6. Replace the store.** SQLite in WAL mode behind the store interface
from step 4, or flock plus fsync as an interim. Exit: two API processes on one
data directory pass a concurrent propose/verify test without lost updates.

**Step 7. Rename what retrieval does.** "Identifier search with typo
tolerance." "Lexical reference graph; edges are leads." Exit: the tool
descriptions in the shim match the parity review's own description of the
implementation.

**Step 8. Repository hygiene.** Remove `backend/data` from the index, reconcile
`.gitignore` with the index, collapse the eight top-level "what we are" docs to
three (vision, status, roadmap), move prompts to `archive/`. Add one CI
workflow that runs `make check-backend` and the retrieval gate. Tag a version.
Exit: a fresh clone builds, tests, and passes the gates in CI.

Steps 0 through 3 are days of work. Step 4 is the big one and is worth a week.
The rest follows.

---

## 16. Verdict

The one-sentence product is right. The kernel that implements it is small,
careful, and better defended than most memory systems in the peer set. The
evidence layer around it is real engineering. The documentation is honest to a
fault.

Around that kernel sits a platform the project decided in June it did not
want, a runtime that recomputes the world on every call, a storage layer that
is safe for exactly one process, a first-run experience that dead-ends
silently, and a documentation folder that is four-fifths process record. And
after three months of auditing itself, the project has never once run the
experiment its own vision names as the proof.

A sharp, defensible idea with a well-engineered core, wrapped in a platform it
already decided to abandon, and still unmeasured on the only question that
matters. Commit it, measure it, then cut around it.
