# Status

xMustard v0.1.0 is out. It gives the coding agents you already use one small, local
MCP server for three jobs: seeing what changed in the repository, finding the right
code, and sharing memory that another agent has checked. This page covers what ships,
what was measured, what is still missing, and what comes next.

Updated 2026-09-28 for tag `v0.1.0` (`8fca2d5`). The full change list is in the
[v0.1.0 release notes](releases/v0.1.0.md).

## At a glance

| | |
| --- | --- |
| Release | [v0.1.0 on GitHub](https://github.com/just-very-queer/xMustard/releases/tag/v0.1.0), published 2026-09-28 |
| Prebuilt archives | macOS arm64 and Linux x86_64, each with a `.sha256` file, plus `SHA256SUMS` |
| Binaries | `xmustard-api`, `xmustard-ops`, `xmustard-core`, `xmustard-relay`, and the older Go stdio shim `xmustard-mcp` |
| MCP surface | Nine tools on protocol 2025-06-18 (2024-11-05 is also negotiated) |
| Clients | Any MCP client, over Streamable HTTP or the stdio relay. Pi has its own extension. Codex and OpenCode connect through MCP configuration only |
| Default posture | Local, no Docker. The API binds `127.0.0.1:8042` and serves the core profile |
| License | MIT |
| Next | The production capture redactor, planned for v0.1.1. Development continues on `feat/parity-v2`; see [In progress](#in-progress) |

## What ships in v0.1.0

**Know what changed before you edit.** `ground` reports what changed, went stale,
broke or got blocked since the baseline, including changed signatures. Its answer fits
a budget of 6,000 characters by default. On a pi-mono clone where 60 files gained a new
leading parameter, it returned 4,550 B instead of 19,544 B and still counted all 200
contract breaks.

**Memory that someone else checked.** `remember` proposes a fact, decision or gotcha.
It is promoted only after approvals from 2 distinct principals other than its author
(the default threshold). `recall` ranks memory for the task at hand and marks an entry
stale when a file it names has changed (one path shape is hit by a bug; see
[Known limits](#known-limits-in-v010)). With no tokens minted, the API runs in open
mode: every write is labelled `self_asserted_open_mode` and is never peer-verified.

**Find code by the words inside it.** `search` fuses BM25 over function bodies, comments,
names, paths and doc sections with identifier match, trigram typo tolerance, reference
degree and proximity (RRF, K=60). Hits come back as `path:line` with snippets and
reasons. On the retrieval gate's gold fixture, 6 of 6 body-term queries now land in
the top 5, up from 0 of 6.

**Index the whole repository.** The old 800-file cap is gone. Every eligible file is
indexed up to a declared envelope (10,000 files and 100,000 symbols by default); past
it, the index is partial and says so. On a cline clone it indexed 2,660 of 2,660 files
(51,263 symbols) at a 21.8-23.8 MiB peak across the recorded runs, and a one-file
update took 140-265 ms under shared load. Fifteen tree-sitter packs cover Rust, Go,
TypeScript, TSX, JavaScript, Python, Java, C, C++, C#, Ruby, PHP, Kotlin, Swift and
Bash, and coverage is reported per language.

**See the blast radius.** `explain` describes a file; a directory path returns an error.
`impact` walks references from a symbol, between two symbols, or from your current
changes. For Go and TS/JS, a resolver adds typed symbol edges (CALLS, USES, ACCESSES,
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

**Safe defaults.** The API refuses a non-loopback bind unless `XMUSTARD_AUTH=required`
with minted tokens and TLS (or `XMUSTARD_ALLOW_INSECURE_BIND=1` behind a TLS proxy).
Tokens carry roles (`admin`, `human-approver`, `indexer`, `verifier`, `proposer`,
`reader`). Search refuses secret paths, memory is redacted on ingest, and recalled
memory carries flags from an instruction-pattern scan. Details: [Security](SECURITY.md).

**Connect your agent.** Any MCP client can connect over HTTP or through the relay.
`xmustard-ops mcp-config` prints the `mcpServers` entry and never writes the token in
literally. The [README](../README.md#connect-your-agent) has recipes for Claude Code and
Codex. Pi has its own extension in [`integrations/pi`](../integrations/pi/README.md).

## Measured

Each number names its platform or setup, and its source. Sampled peaks are
single-machine results, not universal ceilings.

| What | Result | Platform | Source |
| --- | --- | --- | --- |
| Budget gate v2, CI suite, release commit | 70.5 MiB (frozen v1 workload, stdio shim) and 68.8 MiB (2 agents through `xmustard-relay`); line 95.4 MiB | Linux x86_64 | [Release notes](releases/v0.1.0.md) (no stored report for this run) |
| Budget gate v2, latest stored report | 62.0 MiB and 66.0 MiB for the same two scenarios, both PASS | Linux x86_64 | [Gate v2 report](benchmarks/evidence/2026-09-28/ws18-gate-v2-ci-head.md) |
| Retrieval gate | 21 of 21 checks on the release commit. Stored run: 12/12 gold paths and 10/10 spans in the top 5, gold-path MRR@5 1.0, 6/6 body-term queries | Linux x86_64 | [Release notes](releases/v0.1.0.md), [WS-18](benchmarks/2026-09-28-ws18-hybrid-search.md) |
| Relay vs Go shim, RSS per stdio agent | 2,192-2,240 KiB vs 13,824-13,888 KiB | macOS arm64 | [WS-13](benchmarks/2026-09-26-ws13-relay-rss.md) |
| Code index, full build | cline 2,660/2,660 files at 21.8-23.8 MiB; pi-mono 1,636/1,636 at 20.9-22.5 MiB (both runs) | macOS M1 | [WS-07](benchmarks/2026-09-25-ws07-index-rss.md) |
| `ground` output budget | 19,544 B unbudgeted, 4,550 B at the default 6,000-character budget | pi-mono clone | [Build plan, WS-54 record](plans/2026-09-25-parity-build-plan.md) |
| `recall` over 1,000 memories | p50 28.5 ms, p95 31.0 ms; API RSS 31.5 MB after 600 recalls | Linux x86_64 | [Build plan, WS-20 record](plans/2026-09-25-parity-build-plan.md) |
| Resolver precision (CALLS edges) | 30 of 30 correct, recall 0.938 | Small purpose-written fixtures | [WS-17](benchmarks/2026-09-26-ws17-resolver.md) |
| `tools/list`, lean, all nine tools | Capped at 8,769 B on protocol 2025-06-18 | Enforced by a test | [`tools_list_budget.json`](../api-go/internal/mcpserver/testdata/tools_list_budget.json) |

Checks that passed on the release commit: every cargo test and every Go package,
`cargo build --locked`, `make bench-test` (74 tests) and the Pi check (44 tests).
Clippy reports 6 warnings.

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

## Known limits in v0.1.0

- **Native-output capture is off in release builds, on purpose.**
  `POST .../evidence/capture` answers `503 redaction_unavailable`. Capture keeps each
  original and makes it searchable, so it stays closed until a streaming secret
  redactor is wired into `captureRedactor`
  (`api-go/cmd/xmustard-api/evidence_capture_routes.go`). Only the test build
  (`-tags xmustard_e2e`) installs one. In v0.1.0 this means:
  - xMustard's own nine tools still return bounded results with recovery handles;
    native tool output is not captured or reduced.
  - The Pi extension's built-in tool projection and compaction fall back to Pi's own
    behavior, and masking covers only results that already carry a handle.
  - A client hook adapter that posts native output would get the same refusal. None
    ships in v0.1.0.

  The production redactor is the next fix, planned for v0.1.1. It is built on
  `parity/ws-23` and not yet merged.
- **Codex and OpenCode: MCP configuration only.** They connect as ordinary MCP clients.
  There is no Codex hook package or OpenCode plugin yet; those adapters are WS-40, in
  wave 3. Their `client=` label only attributes calls in the usage counters.
- **Drift on even-depth paths.** A memory anchored to a path with an even number of
  components, such as `pkg/auth.go`, is baselined as missing, so `recall` never flags it
  stale when that file changes. Top-level paths such as `main.go` work. The cause is a
  file-descriptor reuse check in `api-go/internal/workspaceops/safepath_unix.go`. A fix
  is on `parity/ws-23` and not yet merged.
- **`diagnostics` needs Postgres.** Without `postgres_dsn` in `<data dir>/settings.json`
  it returns "Postgres DSN is required to read diagnostics". The `XMUSTARD_PG_DSN`
  environment variable does not supply it.
- **Set an absolute data directory.** `XMUSTARD_DATA_DIR` defaults to `../backend/data`
  relative to the process working directory, release binaries included.
- **The resident worker is opt-in.** `XMUSTARD_CORE_WORKER=1` turns it on. Without it,
  each call starts a one-shot `xmustard-core`.
- **First ground while xMustard is busy.** The memory governor can refuse the first
  auto-registration baseline. That ground then reports no baseline, and a later one
  builds it.
- **Platforms.** Prebuilt archives cover macOS arm64 and Linux x86_64 only. macOS
  binaries carry only an ad-hoc signature, so a browser download needs
  `xattr -d com.apple.quarantine xmustard-*`. On Linux, `xmustard-core` needs glibc
  2.39 or newer (Ubuntu 24.04+, Debian 13+).
- **Homebrew.** The in-repo formula builds the `v0.1.0` tag from source with Go and
  Rust. There is no tap yet.
- **macOS memory test.** One Rust memory test (the 100k-symbol resident graph) exceeds
  its line on macOS. Linux is the reference platform, and it passes there.
- **Checks, not guarantees.** The injection scan matches known instruction patterns,
  so a paraphrase passes. Merge attestations are records; they block a merge only where
  a hook or CI check runs `xmustard-ops review gate`.
- **Tracked runtime data.** Files under `backend/data` are still tracked. Untracking
  them waits on the owner.

## In progress

As of 2026-09-28. Workstream numbers refer to the
[parity build plan](plans/2026-09-25-parity-build-plan.md).

**Merged into `feat/parity-v2` after the tag**

- **WS-15, file watcher and refresh loop.** Edits reach the resident index in about
  half a second on a 5,000-file tree: p50 424 ms on Linux (inotify), 488 ms on macOS
  (FSEvents), and 497 ms through the API on Linux. On an unchanged tree, ground's drift
  check hashes no files. The watcher lives in the opt-in resident worker. With that
  worker on, every parity-scale scenario measured is over the 95.4 MiB line, before and
  after WS-15 alike (agents-2 at 201-209 MiB; the worker alone peaks at 171-185 MiB).
  The plan ties this to an open WS-14 item. Measurements:
  `docs/benchmarks/2026-09-28-ws15-watcher.md` on that branch.

**On workstream branches, not yet merged**

- **WS-23** (`parity/ws-23`): the Claude Code hook service, a static hook client and
  the adapter plugin. It also wires the production capture redactor planned for
  v0.1.1, and fixes the even-depth path bug above.
- **WS-26** (`parity/ws-26`): a tag-triggered release workflow, and a Homebrew formula
  that installs the checksummed prebuilt archives.
- **WS-58** (`parity/ws-58`): daemon lifecycle. `xmustard-ops setup` installs a launchd
  agent or systemd units, `xmustard-ops store backup|check|restore` backs up, checks
  and restores the governance store, and the relay waits for a restarting API.
- **WS-65** (`parity/ws-65`): deterministic anchoring of review findings, ported from
  open-code-review (Apache-2.0), behind a `review` build tag.

**Planned**

- WS-21B: hardening for `why_failed` command mode.
- WS-66: a store for review findings.
- Wave 3, including WS-40 (Codex, OpenCode and Cursor adapters) and WS-50 (the parity
  evaluation suite and final parity-scale gate).

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

The 2026-09-24 pre-release review, its baseline audit and its evidence tables are kept
in Git: `git show v0.1.0:docs/STATUS.md`. The reports it cites are listed under History
in the [documentation map](README.md).
