# Status

xMustard v0.1.1 is out. It gives the coding agents you already use one small, local
MCP server for three jobs: seeing what changed in the repository, finding the right
code, and sharing memory that another agent has checked. This page covers what ships,
what was measured, what is still missing, and what comes next.

Updated 2026-09-28 for `v0.1.1`, cut from `feat/parity-v2` at `1c64982`. The full
change list is in the [v0.1.1 release notes](releases/v0.1.1.md); the first release's
are in the [v0.1.0 notes](releases/v0.1.0.md).

## At a glance

| | |
| --- | --- |
| Release | [v0.1.1 on GitHub](https://github.com/just-very-queer/xMustard/releases/tag/v0.1.1), cut 2026-09-28. The tag-triggered release workflow builds the archives and drafts the release; the owner publishes it |
| Prebuilt archives | macOS arm64 (built on `macos-15`) and Linux x86_64 (built on `ubuntu-22.04`), each with a `.sha256` file, plus `SHA256SUMS` |
| Binaries | `xmustard-api`, `xmustard-ops`, `xmustard-core`, `xmustard-relay`, the older Go stdio shim `xmustard-mcp`, and the Claude Code plugin's `xmustard-hook` — all six ship in the release archive |
| MCP surface | Nine tools on protocol 2025-06-18 (2024-11-05 is also negotiated) |
| Clients | Any MCP client, over Streamable HTTP or the stdio relay. Claude Code has a plugin with hooks, and Pi its own extension. Codex, OpenCode and Cursor connect through MCP configuration only |
| Default posture | Local, no Docker. The API binds `127.0.0.1:8042` and serves the core profile |
| License | MIT, except the Apache-2.0 finding-anchoring files (linked only into `review`-tagged builds; see `NOTICE`) |
| Next | Codex, OpenCode and Cursor adapters, and the parity gate. Development continues on `feat/parity-v2`; see [In progress](#in-progress) |

## What ships in v0.1.1

**Know what changed before you edit.** `ground` reports what changed, went stale,
broke or got blocked since the baseline, including changed signatures. Its answer fits
a budget of 6,000 characters by default. On a pi-mono clone where 60 files gained a new
leading parameter, it returned 4,550 B instead of 19,544 B and still counted all 200
contract breaks.

**Memory that someone else checked.** `remember` proposes a fact, decision or gotcha.
It is promoted only after approvals from 2 distinct principals other than its author
(the default threshold). `recall` ranks memory for the task at hand and marks an entry
stale when a file it names has changed, at every path depth (v0.1.1 fixed the even-depth
bug of v0.1.0). With no tokens minted, the API runs in open
mode: every write is labelled `self_asserted_open_mode` and is never peer-verified.

**Find code by the words inside it.** `search` fuses BM25 over function bodies, comments,
names, paths and doc sections with identifier match, trigram typo tolerance, reference
degree and proximity (RRF, K=60). Hits come back as `path:line` with snippets and
reasons. On the retrieval gate's gold fixture, 6 of 6 body-term queries now land in
the top 5, up from 0 of 6.

**Index the whole repository.** The old 800-file cap is gone. Every eligible file is
indexed up to a declared envelope (10,000 files and 100,000 symbols by default); past
it, the index is partial and says so. On a cline clone it indexed 2,660 of 2,660 files
(51,263 symbols) at a 21.8-25.0 MiB peak across the recorded runs (25.0 MiB with the
graph-segment write that v0.1.1's index build performs), and a one-file update took
140-265 ms under shared load. Fifteen tree-sitter packs cover Rust, Go, TypeScript,
TSX, JavaScript, Python, Java, C, C++, C#, Ruby, PHP, Kotlin, Swift and Bash, and
coverage is reported per language. With the resident worker on, a watcher
turns edits into incremental updates: on a 5,000-file tree an edit reached the index in
p50 424 ms on Linux and 488 ms on macOS (in-process loop), 497 ms on Linux through the
API.

**See the blast radius.** `explain` describes a file; a directory path returns an error.
`impact` walks references from a symbol, from a file (`path=`), between two symbols, or
from your current changes. For Go and TS/JS, a resolver adds typed symbol edges (CALLS, USES, ACCESSES,
EXTENDS, IMPLEMENTS, IMPORTS, HAS_METHOD, HAS_PROPERTY) with confidence tiers. The
`impact` tool itself walks a lexical file graph, so edges at distance 1 or more are
leads, not proof.

**Explain a failure.** `why_failed` reads a run id, a pasted log or an evidence handle
and returns the error lines and implicated files. Its outcomes show up in
`ground.recent_failed_runs`. Command mode is off by default and needs
`XMUSTARD_WHY_FAILED_COMMANDS=1` plus an admin token.

**Stay light per agent.** A client that speaks Streamable HTTP uses `/mcp` on the API
and runs no extra process. A stdio client can use `xmustard-relay`: 2,192-2,240 KiB RSS
per agent, against 13,824-13,888 KiB for the Go shim.

**Keep humans in charge of merges.** `xmustard-ops approve|reject|queue` records human
votes on pending memory. `xmustard-ops review approve|revoke` records merge
attestations bound to a reviewed diff and its base, and `review gate` checks them.
xMustard never merges.

**Reduce what Claude Code reads.** The plugin in `integrations/claude-code` replaces a
large Bash, Read, Grep, Glob or WebFetch output with its redacted reduction behind a
recovery handle, and adds index hits and human-approved memory before searches and
file reads. No hook allows, denies or rewrites a tool call, and the API starts no
process for one. Captured output from any client is redacted before it is kept, and
the output of a secret path is refused.

**Run it as a service.** `xmustard-ops setup` installs a launchd agent or systemd
socket and service with no credential in the unit; `xmustard-ops store backup|check|restore`
backs up, checks and restores the memory database, and the API checks its store at
start-up.

**Safe defaults.** The API refuses a non-loopback bind unless `XMUSTARD_AUTH=required`
with minted tokens and TLS (or `XMUSTARD_ALLOW_INSECURE_BIND=1` behind a TLS proxy).
Tokens carry roles (`admin`, `human-approver`, `indexer`, `verifier`, `proposer`,
`reader`). Search refuses secret paths, memory is redacted on ingest, and recalled
memory carries flags from an instruction-pattern scan. Argument errors come back as
tool results the agent can read, with a hint. Details: [Security](SECURITY.md).

**Connect your agent.** Any MCP client can connect over HTTP or through the relay.
`xmustard-ops mcp-config` prints the `mcpServers` entry (Codex's `config.toml` table with
`--client codex`) and never writes the token in literally. The
[README](../README.md#connect-your-agent) has recipes for Claude Code, its plugin and
Codex. Pi has its own extension in [`integrations/pi`](../integrations/pi/README.md).

## Measured

Each number names its platform or setup, and its source. Sampled peaks are
single-machine results, not universal ceilings.

| What | Result | Platform | Source |
| --- | --- | --- | --- |
| Budget gate v2, CI suite, v0.1.1 release tree | 66.6 MiB (frozen v1 workload, stdio shim) and 60.8 MiB (2 agents through `xmustard-relay`), both PASS; line 95.4 MiB. v0.1.0's release commit: 70.5 and 68.8 MiB | Linux x86_64, release core and relay, worker off | [Gate v2 report](benchmarks/evidence/2026-09-28/release-v0.1.1-gate-v2-ci.md), [v0.1.1 notes](releases/v0.1.1.md), [v0.1.0 notes](releases/v0.1.0.md) |
| Retrieval gate | 21 of 21 checks on the v0.1.1 release tree: 12/12 gold paths and 10/10 spans in the top 5, gold-path MRR@5 1.0, 6/6 body-term queries, cold, warm and after a one-file edit | Linux x86_64 | [Release-tree log](benchmarks/evidence/2026-09-28/release-v0.1.1-checks.txt), [WS-18](benchmarks/2026-09-28-ws18-hybrid-search.md) |
| Watcher freshness, edit to index | p50 424 ms (max 447) inotify and 488 ms (max 504) FSEvents in the in-process loop; 497 ms (max 549) through the API | 5,000-file tree; Linux x86_64 and Apple M1 | [WS-15](benchmarks/2026-09-28-ws15-watcher.md) |
| Claude Code hooks under load | PreToolUse(Read) p50 4.2 ms, p95 7.5 ms; PostToolUse(Bash, 30,000 characters) p50 4.0 ms, p95 8.5 ms, 600 of 600 replaced; 0 spawns; API peak 41.7 MiB | Linux x86_64, 600 calls of each event from 4 concurrent clients | [Build plan, WS-23 record](plans/2026-09-25-parity-build-plan.md) |
| Capture redactor | Live heap at most 0.4 MiB on 16 MiB of pure secrets; raw 16 MiB captures at 31.4-33.0 MB/s against 33.3-39.3 pass-through | Linux x86_64, load 6 to 10 | [Release notes](releases/v0.1.1.md#capture-redaction) |
| Relay vs Go shim, RSS per stdio agent | 2,192-2,240 KiB vs 13,824-13,888 KiB | macOS arm64 | [WS-13](benchmarks/2026-09-26-ws13-relay-rss.md) |
| Code index, full build | cline 2,660/2,660 files at 21.8-25.0 MiB (25.0 with the graph-segment write); pi-mono 1,636/1,636 at 20.9-22.5 MiB | macOS M1 | [WS-07](benchmarks/2026-09-25-ws07-index-rss.md), [WS-14](benchmarks/2026-09-26-ws14-resident-index.md) |
| `ground` output budget | 19,544 B unbudgeted, 4,550 B at the default 6,000-character budget | pi-mono clone | [Build plan, WS-54 record](plans/2026-09-25-parity-build-plan.md) |
| `recall` over 1,000 memories | p50 28.5 ms, p95 31.0 ms; API RSS 31.5 MB after 600 recalls | Linux x86_64 | [Build plan, WS-20 record](plans/2026-09-25-parity-build-plan.md) |
| Resolver precision (CALLS edges) | 30 of 30 correct, recall 0.938 | Small purpose-written fixtures | [WS-17](benchmarks/2026-09-26-ws17-resolver.md) |
| `tools/list`, lean, all nine tools | Capped at 8,888 B on protocol 2025-06-18 (119 B more than v0.1.0, for `impact`'s `path`) | Enforced by a test | [`tools_list_budget.json`](../api-go/internal/mcpserver/testdata/tools_list_budget.json) |

Checks that passed on the v0.1.1 release tree: every cargo test target and all 20 Go
packages, the `review`-tagged vet and tests, `make bench-test` (76 tests, 3 skipped),
the retrieval gate, the Pi check (45 tests) and a local `make release` for
linux-x86_64, which builds with `cargo build --release --locked`. Clippy reports 6
warnings. The log:
[release-v0.1.1-checks.txt](benchmarks/evidence/2026-09-28/release-v0.1.1-checks.txt).

What these numbers do not show:

- **Token savings and task success.** The repository records no real-model run yet.
  `xmustard-eval` can run paired arms, and gate v2's token figures are a bytes/4
  estimate, not a tokenizer count.
- **Retrieval quality on real repositories.** The gold fixture is a handcrafted
  14-file repository.
- **Resolver precision on real repositories.** The fixtures are small and were written
  for the test.
- **Parity.** This is not a parity claim. The parity-scale suite has not passed its
  95.4 MiB line, and the final parity gate arrives with WS-50.
- **Real-model evaluation.** The `parity-v1` corpus (eight tasks with hidden oracles)
  ran only with fake drivers.

## Known limits in v0.1.1

- **Not a parity claim.** The parity-scale suite (4 agents, 2 hot repositories, 4
  worktrees) has not passed its 95.4 MiB line; its 4-agent, two-repository and
  four-worktree scenarios have not been run. With the resident worker on, the 2-agent
  scenario is over the line (201-209 MiB, the worker alone at 171-185 MiB), before and
  after the watcher alike; WS-15 reads that as the worker building the legacy graph,
  which was not measured. With the worker off, WS-14 measured it at 94.0 MiB on macOS.
  On the evaluation corpus's real repositories (pi-mono, cline), the process tree
  peaked at 91.7-120.9 MiB through the stdio shim, over the line in 5, 8 and 7 of 10
  runs. The final parity gate arrives with WS-50.
- **The Pi adapter e2e passes in part: 9 of its 18 tests.** Its unit tests and the
  WS-24 capture suite pass. Nine tests need a multi-page result from `impact` with no
  arguments, which a freshly baselined fixture no longer produces; a large-result
  fixture is an open WS-24 follow-up.
- **The Claude Code plugin needs a source checkout.** Its hook client is not in the
  release archive. Index hits and syntax reports in hooks, and the watcher, need
  `XMUSTARD_CORE_WORKER=1`. A static hook client that gives up in the last ~20 ms of
  its budget on a loaded host drops an answer the daemon counted as delivered, so those
  memories are not pushed again in that context until compaction. Codex, OpenCode and
  Cursor get MCP configuration only; their adapters are WS-40.
- **Redaction is pattern-based.** A secret no rule knows is kept as written, and a
  secret file read through a shell (`cat .env`) or matched by a directory search is
  checked by content only. A secret split across two output strings of one hook body
  is not joined.
- **Upgrading from v0.1.0.** The governance store moves to schema version 3, which
  v0.1.0 cannot open; back it up first (`xmustard-ops store backup`, with `--data-dir`
  for a store at the relative default). Memories promoted under v0.1.0 and anchored at
  an even path depth recorded that file as missing, so recall now flags them stale.
- **`diagnostics` needs Postgres.** Without `postgres_dsn` in `<data dir>/settings.json`
  it returns "Postgres DSN is required to read diagnostics". The `XMUSTARD_PG_DSN`
  environment variable does not supply it.
- **Set an absolute data directory.** `XMUSTARD_DATA_DIR` defaults to `../backend/data`
  relative to the process working directory, release binaries included. A service
  installed by `xmustard-ops setup` gets an absolute one. The service commands
  (`setup`, `daemon`, `store`, `uninstall`) take `XMUSTARD_DATA_DIR` only when it is
  absolute, and otherwise use the platform's data directory.
- **The resident worker is opt-in.** `XMUSTARD_CORE_WORKER=1` turns it on. Without it,
  each call starts a one-shot `xmustard-core` and nothing is watched. After the worker's
  2-minute idle exit, nothing is watched until the next call. Each refresh batch still
  scans the tracked files (16-23 ms at 5,000 files, 114-153 ms at 50,000).
- **`impact` is lexical.** Its edges come from symbol names and import lines, and a
  name shorter than 4 characters makes no edge.
- **`search` in pattern mode needs `ast-grep` on `PATH`.** Without it, `mode=pattern`
  answers engine `"none"` with no matches and no error, the same as a pattern that
  matches nothing.
- **First ground while xMustard is busy.** The memory governor can refuse the first
  auto-registration baseline. That ground then reports no baseline, and a later one
  builds it.
- **Platforms.** Prebuilt archives cover macOS arm64 and Linux x86_64 only. macOS
  binaries carry only an ad-hoc signature, so a browser download needs
  `xattr -d com.apple.quarantine xmustard-*`. The Linux archive is built on Ubuntu
  22.04, so its Rust binaries need glibc 2.35 at most; the exact floor was not measured.
  The service needs launchd or systemd.
- **Service.** launchd has no socket activation for the cgo-free API, so the macOS
  agent refuses connections while it restarts. A hung daemon is not detected (no
  watchdog). `store restore` is offline: it stops the daemon. Under systemd,
  `daemon status` starts the daemon. The real-launchd integration test was not run.
- **Homebrew.** The in-repo formula pins v0.1.1 (the prebuilt archive on macOS arm64,
  a source build elsewhere), moved by `packaging/homebrew/bump.sh`. There is no
  tap yet.
- **One Rust memory line is close.** Before WS-FIX-06, the debug-build index-build
  and resident-service RSS tests failed now and then under load on the shared build
  box; WS-FIX-06 gave debug builds their own RSS bounds, and every recorded full run
  since has passed. The release-profile resident line is the close one (+12.9 to
  +13.4 MiB against +15 MiB), and `make check-backend` runs only the debug profile.
  The resident-graph test is ignored off Linux, the reference platform.
- **Checks, not guarantees.** The injection scan matches known instruction patterns,
  so a paraphrase passes. Merge attestations are records; they block a merge only where
  a hook or CI check runs `xmustard-ops review gate`.
- **Tracked runtime data.** Files under `backend/data` are still tracked; `.gitignore`
  now ignores the directory, and untracking the 80 files waits on the owner.

## In progress

As of 2026-09-28. Workstream numbers refer to the
[parity build plan](plans/2026-09-25-parity-build-plan.md).

**Merged into `feat/parity-v2` after the v0.1.1 cut**

- **WS-35**: impact v2 (tiers, risk, epistemic envelope, typed filters).
- **WS-27**: dedupe, code anchors, tiered conflicts and structured claims for memory.

**On workstream branches, not yet merged**

- **WS-31** (`parity/ws-31`): tiered memory, write policy and guidance artifacts.
- **WS-33** (`parity/ws-33`): session ledger, compaction snapshot and cross-client
  handoff.
- **WS-36** (`parity/ws-36`): symbol navigation behind search, explain and impact.
- **WS-40a and WS-40b** (`parity/ws-40a`, `parity/ws-40b`): Codex and OpenCode adapters.
- **WS-42** (`parity/ws-42`): repeat guard and revision-keyed result cache.
- **WS-CQ-01** (`parity/ws-cq-01`): one route list for the API.

**Planned**

- WS-21B: hardening for `why_failed` command mode.
- A large-result fixture for the Pi adapter e2e (WS-24 follow-up).
- Wave 3, including WS-50 (the parity evaluation suite and final parity-scale gate).

## Check it yourself

```bash
make check-backend      # Go tests and build, cargo test, cargo clippy
make bench-test         # gate v2 unit tests, no binaries needed
make bench-retrieval    # retrieval gate
make bench-gate         # budget gate v2, CI suite
cd integrations/pi && npm ci && npm run check
```

After a Rust change, rebuild the release core (`make build`) or point
`XMUSTARD_CORE_BIN` at a fresh build. The Go API otherwise runs whatever
`xmustard-core` it finds first.

## Earlier status

The v0.1.0 status page, as refreshed after that release, is in Git at
`git show b994a9c:docs/STATUS.md`. The 2026-09-24 pre-release review, its baseline audit
and its evidence tables are at `git show v0.1.0:docs/STATUS.md`. The reports it cites are listed under History
in the [documentation map](README.md).
