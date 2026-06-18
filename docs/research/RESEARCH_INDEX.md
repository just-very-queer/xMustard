# xMustard — Research Index (June 2026)

Two inputs feed this index:
1. **Market scout** (`MARKET_SCOUT_2026-06.md`) — produced by a Sonnet agent run in tmux; it
   drew on training knowledge (≈Aug 2025) because that session lacked live web.
2. **Live web supplement** (below) — gathered with live web search in this session (June 2026)
   to close the gap.

The 12 most-relevant papers are downloaded under `papers/` (arXiv PDFs).

## Live web supplement (June 2026)

Local serving / OpenAI-compatible endpoints (directly relevant to the new provider layer):
- **Ollama 0.24** (May 2026) — added Codex App support, Gemma 4 MTP speculative decoding via the
  MLX runner; positioned as the single-command bridge for local **or** cloud models into Codex
  App / Claude Code / OpenCode. Confirms the value of xMustard's OpenAI-compatible provider layer.
- **vLLM 0.21** — stabilized DeepSeek V4 on Blackwell (TOKENSPEED_MLA backend), speculative
  decoding respects reasoning budgets.
- **GLM-4.7 Thinking** — top open-weight coding model in 2026, available on Ollama/HF; alongside
  DeepSeek-Coder-V2, Qwen2.5-Coder-32B, Llama-3.3-70B as the common local coding models.
- **MCP is now table-stakes**: LangGraph, Hermes Agent, Ontheia and others are MCP-native across
  stdio / SSE / streamable-HTTP. xMustard's 31-tool MCP server is on the right side of this.

Sources: codersera local-AI-runtimes May 2026; langchain AI agent frameworks 2026; tech-insider
vLLM vs Ollama 2026; whatllm best LLM for coding 2026.

## Papers (downloaded under `papers/`)

| # | Paper | arXiv | Maps to xMustard |
|---|-------|-------|------------------|
| 1 | SWE-bench: Resolve Real-World GitHub Issues | 2310.06770 | the benchmark our context engine + verification should be measured on |
| 2 | SWE-agent: Agent-Computer Interfaces | 2405.15793 | minimal disciplined tool surface → informs our LSP + MCP tool API |
| 3 | Agentless: Demystifying LLM SWE agents | 2407.01489 | locate→patch baseline; pressure-test that runtime memory earns its overhead |
| 4 | OpenHands: Open Platform for AI Developers | 2407.16741 | sandboxed agents our MCP engine must serve cleanly |
| 5 | RAPTOR: Tree-Organized Retrieval | 2401.18059 | multi-granularity (repo→file→symbol) retrieval, like our symbol graph |
| 6 | HippoRAG: Long-Term Memory for LLMs | 2405.14831 | KG + PageRank associative retrieval ≈ Postgres knowledge layer + symbol graph |
| 7 | CodeRAG-Bench: Retrieval-Augmented Code Gen | 2406.14497 | empirical case that dense+sparse hybrid (RRF) beats single retrievers for code |
| 8 | RepoGraph: Repository-level Code Graph | 2410.14684 | tree-sitter module/call graph for context selection — validates our typed graph |
| 9 | Multiagent Debate improves Factuality | 2305.14325 | foundation for multi-agent verification of shared context |
| 10 | Judging LLM-as-a-Judge (MT-Bench/Arena) | 2306.05685 | calibration/bias of the verifier/judge in our governance gate |
| 11 | MemGPT: LLMs as Operating Systems | 2310.08560 | tiered self-managed memory — ancestor of our runtime-memory design |
| 12 | RAG for Knowledge-Intensive NLP | 2005.11401 | the retrieve-then-generate foundation our code-context loop specializes |

## How the research validated the two new features

The Sonnet scout independently surfaced two gaps that the features built this session fill:

- **"Multi-agent debate targets *answers*, not *shared world-state*."** No framework ensures
  concurrent agents act on a consistent snapshot. → xMustard's **context governance** (propose →
  multi-agent verify → promote, with readonly permission and a require-multi-agent toggle) is the
  "context consensus before action" layer. Grounded in papers #9 and #10.

- **"Provider routing is latency/cost-blind to task semantics."** → xMustard's **OpenAI-compatible
  provider layer** (Ollama/vLLM/LM Studio/OpenAI + VLM) is the seam where task-typed model routing
  becomes possible. The next step (not yet built) is semantic routing by coding sub-task.
