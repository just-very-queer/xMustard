# Parity evaluation corpus v1

`corpus.yaml` is the evaluation corpus for `xmustard-eval` (WS-63). It has eight coding
tasks on two permissively licensed TypeScript monorepos, pinned to full commits. Each
task has a hidden `node:test` oracle, and each oracle was validated to fail on the
starting state and pass with the task's reference patch. The corpus schema, arms,
isolation and metrics are described in `../README.md`.

This README is not hidden from the agent at run time, so it names the tasks but not
their solutions, their gold files or their seeded memories. Those are in `corpus.yaml`,
`oracles/` and `references/`, which a run hides.

## Tasks

| Task | Repository | Class | Memory fixture |
| --- | --- | --- | --- |
| `pi-truncate-reason` | pi-mono | bugfix | current, adversarial, pending |
| `pi-fuzzy-camel-humps` | pi-mono | feature | none (visible check: the package's own fuzzy tests) |
| `pi-retry-cap-decision` | pi-mono | memory_lifecycle | the answer is only in shared memory; every label but stale |
| `pi-protocol-limits-doc` | pi-mono | memory_lifecycle | drifted stale memories at an even and an odd path depth |
| `cline-hub-ipv6-url` | cline | bugfix | a drifted stale memory at an even path depth, current, pending |
| `cline-reasoning-effort-keys` | cline | bugfix | none |
| `cline-edit-preview-hunk-header` | cline | localization | none |
| `cline-schedule-preset-decision` | cline | memory_lifecycle | the answer is only in shared memory |

The bugfix tasks fix defects that exist at the pinned commits. The two decision tasks
cannot be solved without the team's shared memory: the no-memory arms are expected to
fail them, and they measure the lifecycle metrics (current-fact recall, supersession,
duplicates, contradictions, scope leakage, pending and adversarial memory). The
stale-memory tasks can be solved from the code, so a failure after a stale memory was
delivered, where the paired `xmustard_mcp` run passed, counts as stale-memory harm.

Stale memory anchored at an even path depth (such as `pkg/auth.go`) is the case v0.1.0
missed. Confined reads of such paths failed on descriptor reuse, so the memory was never
flagged stale. The fix is 7d380c4 on parity/ws-23. Both repositories carry such a
fixture, and pi-mono pairs it with an odd-depth control.

## Repositories and licences

| Repository | Licence | Pinned commit | Source |
| --- | --- | --- | --- |
| pi-mono | MIT, Copyright (c) 2025 Mario Zechner | `5fd446ca1843682e8da3fec4ceb71c42f56fbace` | https://github.com/badlogic/pi-mono |
| cline | Apache-2.0 | `ee59f81706981e0a64c8b32f8f0415c9d39561fa` | https://github.com/cline/cline |

No source from either repository is vendored. The reference patches carry short
excerpts of it as diff context and are used for evaluation only. GitNexus
(PolyForm Noncommercial) is not used.

## Requirements

- git, and the pinned commits under `research/`. `sh eval/tasks/parity/fetch-repos.sh`
  clones a missing repository shallowly at its commit. An existing clone is only
  fetched into and never checked out or reset.
- Node.js 22.18 or later. The oracles rely on TypeScript type stripping, which is on by
  default from that release. No `npm install` is needed: every oracle imports only
  modules whose import graph is dependency-free.
- A containment wrapper: `bwrap` on Linux or `sandbox-exec` on macOS (`--containment
  auto`).

## Checks and dry runs (no model, no credentials)

```bash
sh eval/tasks/parity/fetch-repos.sh
cd api-go
go build -o /tmp/xm/xmustard-eval ./cmd/xmustard-eval
/tmp/xm/xmustard-eval validate --corpus ../eval/tasks/parity/corpus.yaml --oracles
/tmp/xm/xmustard-eval run --corpus ../eval/tasks/parity/corpus.yaml --out /tmp/xm/parity-dry --driver fake:claude --stack stub
```

`go test ./cmd/xmustard-eval/` checks the corpus's schema and authoring rules on any
checkout (`TestParityCorpusAuthoring`). Where the clones and Node are present,
`TestParityCorpusOracles` runs the oracle validation under containment.

The fake driver applies each task's reference patch, so every arm resolves. Pass
`--fake-fail-arms xmustard_memory` to exercise stale-memory harm attribution. Against
the real stack, add `--stack real --api-bin ... --mcp-bin ... --core-bin ...`. The
fake agent's recall query is the first line of each prompt.

## Real-model run (operator step)

This step needs credentials and a budget. Nobody has run it yet.

```bash
sh eval/tasks/parity/fetch-repos.sh
cd api-go
go build -o /tmp/xm/xmustard-eval ./cmd/xmustard-eval
go build -o /tmp/xm/xmustard-api ./cmd/xmustard-api
go build -o /tmp/xm/xmustard-mcp ./cmd/xmustard-mcp
(cd ../rust-core && cargo build --release --bin xmustard-core)
cp ../eval/tasks/run-config.example.yaml /tmp/xm/parity-run.yaml
#   set: driver, model, repeats: 3, seed, clients.claude.max_budget_usd: 2.0 (or codex
#   pricing), stack.api_bin / mcp_bin / core_bin, and the pre-registered thresholds
/tmp/xm/xmustard-eval validate --corpus ../eval/tasks/parity/corpus.yaml --oracles
/tmp/xm/xmustard-eval run --corpus ../eval/tasks/parity/corpus.yaml --config /tmp/xm/parity-run.yaml \
  --out /tmp/xm/parity-$(date +%Y%m%d)
```

Keep the binaries and the output directory outside `research/` and outside this
checkout's `eval/` directory.

**Budget.** One repetition is 29 runs. Three arms run all eight tasks, and
`xmustard_memory` runs the five tasks with a memory fixture. `xmustard_mcp_hooks` is
skipped until WS-23 configures hook adapters. At `repeats: 3` that is 87 runs. With
claude's `max_budget_usd: 2.0` the spend is capped at USD 174. The worst-case wall time
is 87 x 900 s, about 22 hours; runs usually end much sooner.

**Power.** Eight tasks is a validated base, not a gate. The exact McNemar test reaches
p < 0.05 only with at least six discordant tasks, all in one direction. Add tasks
under the authoring rules in `../README.md` before WS-50 uses this corpus for a parity
decision.
