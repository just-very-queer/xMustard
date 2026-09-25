# Documentation map

Start with the current product documents. Historical completion reports apply to
their named tranche and commit; they do not establish today's behavior.

| Question | Read |
| --- | --- |
| What are we building, and under which constraints? | [Vision](VISION.md) |
| What works, what was checked, and what remains open? | [Status](STATUS.md) |
| Which module owns a behavior? | [Architecture](ARCHITECTURE.md) |
| How does the bounded context candidate work, and what remains proposed? | [Context layer](CONTEXT_LAYER.md), [proxy/model research](research/TOOL_CONTEXT_RESEARCH_2026-09-24.md) |
| Is Jev a local model or a hosted decision helper? | [Jev / System One findings](research/JEV_RESEARCH_2026-09-24.md) |
| What should happen next, with what exit evidence? | [Roadmap](ROADMAP.md) |
| What is implemented in the imported, uncommitted candidate, and what remains open? | [Candidate status](STATUS.md), [implementation plan](plans/2026-09-24-lean-context-implementation.md), [Go report](reviews/2026-09-24-implementation-results.md), [Rust report](reviews/2026-09-24-rust-implementation-results.md), [Pi report](reviews/2026-09-24-pi-implementation-results.md) |
| What did the fixed resource/retrieval/MCP/Pi workloads measure? | [Benchmark report](benchmarks/2026-09-24-lean-context.md), with current [main RSS](benchmarks/evidence/2026-09-24/main-rss.json), [main MCP](benchmarks/evidence/2026-09-24/main-mcp.json), [main gold retrieval](benchmarks/evidence/2026-09-24/main-gold.json), and [main Pi E2E](benchmarks/evidence/2026-09-24/pi-e2e-summary-main.json) evidence |
| What is proposed after this verified tranche? | Database-free diagnostics is a separate, unimplemented next stage; its design review returned `CHANGES_REQUIRED`. |
| What do current competitors provide? | [September parity review](research/COMPETITOR_PARITY_2026-09-24.md) |
| What did the checks find? | [Go audit](reviews/2026-09-24-go-audit.md), [Rust audit](reviews/2026-09-24-rust-audit.md), [live MCP smoke](reviews/2026-09-24-runtime-smoke.md) |
| How did previous conversations and plans shape this? | [History and session library](history/README.md) |

For setup and the MCP contract, use the root [README](../README.md). For agent
working rules, use [AGENTS.md](../AGENTS.md).

## Document ownership

- `VISION.md` records accepted product decisions and explicit open choices.
- `ARCHITECTURE.md` describes source ownership and current execution paths.
- `CONTEXT_LAYER.md` describes the bounded own-MCP/Pi candidate, future adapters, result reduction, and assurance flow; implementation status is explicit.
- `STATUS.md` records dated checks, with failures and limits alongside passes.
- `ROADMAP.md` contains current work and its completion evidence.
- `reviews/` contains dated source audits and candidate implementation reports; findings remain tied to their reviewed source and scope.
- `research/` contains dated comparisons with first-party source links.
- `history/`, existing `plans/`, `prompts/`, and root `goal/` preserve earlier work.

New private working notes remain ignored unless explicitly allowlisted in
`.gitignore`. Raw conversations, runtime records, provider credentials, and
downloaded research are not material for the public documentation set.
