# xMustard — Build Log

Narrative, dated records of significant autonomous build sessions. Terse, canonical
entries live in `CHANGELOG.md`; this file keeps the fuller story and verification notes.

## 2026-06-18 — OpenAI-compatible providers + multi-agent context governance + research scout

1. **OpenAI-compatible provider integration** (`openai_providers.go`) — access any OpenAI `/v1`
   endpoint: Ollama, vLLM, LM Studio, OpenAI, plus vision/VLM via image content parts. Provider
   CRUD, `/models`, probe, `/chat/completions` (text + `image_url` parts, local-file→data-URL).
   Security-first: API keys are never stored — only the env-var name is kept; the key is read from
   the env at call time. `/api/providers*` + MCP `provider_chat`. Verified live against the Ollama
   actually running on this machine (`/v1/models` listed real models, probe `ok:true`); the chat
   client is unit-tested for text + vision against a mock. (A local 9b model errored
   `unexpected EOF` on its own — confirmed Ollama-side, not xMustard.)

2. **Context permission + multi-agent verification gate** (`context_governance.go`) — entries have a
   `readonly`/`readwrite` permission, are added to the shared context only when verified by multiple
   (distinct) agents, and a `require_multi_agent_verification` toggle (+ per-proposal override)
   chooses multi-agent or not. `/context*` + MCP `context_propose`/`context_verify`/`context_active`.
   Verified live (HTTP+MCP): propose→pending, one vote→pending, duplicate vote ignored, 2nd distinct
   agent (via MCP) → promoted, readonly edit rejected.

   MCP grew 27→31 tools. All unit + full workspaceops + MCP suites green.

3 & 4. **Audit ("check if all things are done") via codex `/goal` in tmux** — codex
   `gpt-5.3-codex-spark` audited the repo read-only: 3 surfaces + 4 depth upgrades all DONE; it
   flagged E/F (the two new features) which were then completed. Its feedback ("providers exist but
   not wired to use") drove adding the chat/models endpoints + MCP tool, not just config.

5. **Sonnet Claude in tmux for research + market + papers** — ran `claude --model claude-sonnet-4-6`
   in tmux; it produced a market scout (its session lacked live web, so it drew on training
   knowledge). Supplemented with live web search (June 2026: Ollama 0.24, vLLM 0.21/DeepSeek V4,
   GLM-4.7, MCP-native frameworks). Downloaded 12 arXiv papers (SWE-bench, SWE-agent, Agentless,
   OpenHands, RAPTOR, HippoRAG, CodeRAG-Bench, RepoGraph, multi-agent debate, LLM-as-judge, MemGPT,
   RAG) to `docs/research/papers/` (kept on disk, gitignored to avoid 32 MB of repo bloat), indexed
   in `docs/research/RESEARCH_INDEX.md`.

   Notably, the scout independently identified the exact gaps these two features fill —
   "multi-agent debate targets answers, not shared world-state" (→ context governance) and
   "provider routing is latency/cost-blind to task semantics" (→ provider layer) — so the build is
   corroborated by the state of the art.

   Commits pushed to `feat/product-v1`: `c3fcff4`, `ec9622e`.

   Sources: [codersera local-AI runtimes May 2026](https://codersera.com/blog/local-ai-runtimes-may-2026-update/),
   [LangChain AI agent frameworks 2026](https://www.langchain.com/resources/ai-agent-frameworks),
   [arXiv 2410.14684 RepoGraph](https://arxiv.org/abs/2410.14684).

### Follow-on (next development)
The provider layer delivers *access* (config, models, probe, chat, vision) but does not yet route
full coding runs through these HTTP providers, and context governance is not yet injected into the
run/issue work packet. Those two integrations — provider-backed runs + task-typed model routing, and
verified-context-into-prompts — are the next slice.
