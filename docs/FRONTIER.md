# Research Frontier Map

This document turns the local research synthesis into the next product frontier for xMustard.
It is intentionally shorter and more operational than the broader planning docs: each lane
should point to a buildable artifact, not only a theme.

## Current Research Baseline

xMustard has already absorbed the strongest repeated patterns from the local research repos:

- repo-native guidance from OpenHands, Cline, and resolver-style workflows
- repo maps and ranked context inspired by Aider and PR-Agent
- durable verification artifacts inspired by Qodo Cover and Aider
- replay and eval records inspired by SWE-agent, Qodo Cover, and benchmark-driven agent repos
- ticket-aware review inspired by PR-Agent and OpenHands Resolver
- issue-level threat models and vulnerability workflow depth inspired by Vulnhuntr, OWASP, SARIF, and code-scanning systems
- Go/Rust migration boundaries that keep the product issue-first while moving heavy runtime, retrieval, and API work out of Python

The next frontier is not "scan more files." It is to make context, verification, security,
and governance more explainable and comparable over time.

## Frontier Lanes

### 1. Symbol-Aware Dynamic Context

Research pull:

- Aider treats repo maps as a first-class subsystem.
- PR-Agent narrows context around the code that matters.

Current xMustard state:

- workspace repo maps persist directory, extension, and notable-file summaries
- issue packets include ranked related paths and structural context
- dynamic symbol context exists, but enclosing scope and file-to-symbol explanations are still shallow

Next artifact:

- add per-issue context explanations that answer: "why did this file or symbol enter the packet?"
- persist enclosing function/class snippets for the top ranked paths
- expose symbol context in eval reports so context experiments can compare more than prompt text length

### 2. Cross-Artifact Retrieval

Research pull:

- PR-Agent and ticket-aware resolver flows combine code, issue text, prior discussion, and review evidence.
- trIAge shows the value of duplicate and quality signals before execution.

Current xMustard state:

- issue packets already rank related operational artifacts such as ticket context, threat models, browser dumps, fixes, and recent activity
- issue packets include a retrieval ledger that explains selected evidence, related paths, symbols, artifacts, guidance, and path-specific instructions
- retrieval is lexical and artifact-backed, which is inspectable but limited

Next artifact:

- add hybrid ranking hooks without requiring embeddings infrastructure on day one
- use retrieval results to improve duplicate warnings and owner suggestions

### 3. Eval Timelines And Replay Governance

Research pull:

- SWE-agent, Aider, Qodo Cover, and Cline all treat eval/replay as product infrastructure.

Current xMustard state:

- saved eval scenarios, fresh replay batches, baseline comparisons, replay trend movement, and verification-profile rollups exist
- reports compare latest and previous replay batches, but longer experiment timelines are still thin

Next artifact:

- add multi-batch scenario timelines that show rank, cost, duration, verification confidence, and context drift over time
- add a compact "why this scenario moved" explanation for each trend entry
- make stale eval baselines visible when guidance, ticket context, or verification profiles changed materially

### 4. Security Review Depth

Research pull:

- Vulnhuntr, OWASP Threat Dragon, Semgrep, SARIF, and code-scanning systems separate "works" from "safe."

Current xMustard state:

- issue-level threat models exist
- vulnerability findings, import batches, lifecycle summaries, and security reporting exist in the backend lane
- security context can enter prompts and review flows

Next artifact:

- add analyst disposition, exploitability, suppression, and risk-acceptance fields to vulnerability findings
- link verification profiles to specific security findings and mitigations
- produce an exportable security review packet per issue

### 5. Agent Operations And Policy

Research pull:

- Devin-style operations and Linear-style workflows need visible ownership, audit, and governance.

Current xMustard state:

- runs track runtime, model, costs, plans, metrics, logs, critique, acceptance, and verification artifacts
- Codex and OpenCode runtime probes work through the app runtime layer
- Go now owns a large share of the control-plane surface while Rust owns growing process and verification helpers

Next artifact:

- add workspace policy records for allowed runtimes, required verification profiles, and budget warning thresholds
- surface policy decisions in run insights and activity records
- make runtime choice auditable for team workflows

### 6. Review Packet Export

Research pull:

- PR-Agent and OpenHands Resolver make agent output consumable as review material, not just logs.

Current xMustard state:

- critique, improvements, run insights, fixes, verification, ticket context, and threat models exist as separate artifacts
- export bundles exist, but the operator-facing review packet is not yet a first-class handoff format

Next artifact:

- add an issue review packet that combines fix summary, acceptance-criteria compliance, verification evidence, residual risks, and security notes
- make the packet usable by GitHub PR creation, Linear/Jira sync, and human review without re-reading raw run logs

## Near-Term Build Order

1. Multi-batch eval timeline summaries.
2. Vulnerability disposition and risk-acceptance fields.
3. Workspace runtime and verification policy records.
4. Exportable issue review packet.
5. Hybrid ranking hooks for duplicate warnings and owner suggestions.

This order favors inspectability first: operators should be able to explain why a run had its
context, why an eval moved, why a security finding is accepted or suppressed, and why a runtime
was allowed before xMustard adds more automation.
