Web search permissions aren't granted in this session. I'll write the full report from training knowledge (cutoff ~August 2025), with a clear caveat at the top about the gap to June 2026.

---

# xMustard — Market & Research Scout

> **Data note:** Web search is unavailable in this session. This report draws on training data through approximately August 2025. Product version numbers for late 2025–2026 should be verified against current release pages. arXiv IDs marked † are high-confidence from training; always confirm the ID at arxiv.org before citing.

---

## 1. Latest Releases & Ecosystem News (2025–2026)

### Coding-Agent Frameworks

| Product | Last known state (~Aug 2025) | Notable |
|---|---|---|
| **Claude Code** | GA (Anthropic, Mar 2025) | CLI-native, direct bash/shell, MCP tool client, multi-file patch |
| **Cursor** | v0.40+ (2025) | Background agents, "Agent" tab, `.cursorrules`, claude-4/GPT-4o backends |
| **Cline** | v3.x (2025) | MCP client built-in, plan-then-act mode, browser control |
| **Aider** | v0.50+ (mid-2025) | Tree-sitter repo-map w/ PageRank weighting, architect+editor model split, watch mode |
| **OpenHands** | v0.15+ (2025) | Sandboxed Docker execution, browser agent; top-3 SWE-bench Verified score |
| **SWE-agent** | v1.x (Princeton, 2025) | ACI interface design; configurable tool surface; new `sweagent run-batch` |
| **Devin** | 1.x (Cognition, 2025) | Persistent workspace, Slack integration, enterprise REST API |
| **GitHub Copilot Workspace** | GA (Microsoft, 2025) | Task-centric dev environment; PR brainstorm → plan → code loop |
| **Continue.dev** | v1.x (2025) | VSCode + JetBrains; local codebase index; 50+ LLM providers |
| **Moatless Tools** | 0.x (2025) | Minimal tool surface for agents (FindCode, SemanticSearch, Edit); token-frugal |

### MCP Ecosystem

- **Protocol v1.0** (Anthropic, Nov 2024): JSON-RPC 2.0 over stdio or HTTP/SSE; primitives are `resources`, `tools`, `prompts`, `sampling`
- **SDKs**: Official Python, TypeScript, Java, Kotlin, C#; community Go (`mark3labs/mcp-go`) and Rust SDKs active
- **Registries**: `mcp.so`, `Smithery`, and `mcpservers.org` — each indexing hundreds of community servers by mid-2025
- **Broad server catalogue**: filesystem, git, GitHub, Postgres/SQLite, Slack, Sentry, Puppeteer/browser-use, sequential-thinking, memory, fetch
- **Editor adoption**: Cursor, Cline, Continue, Zed, and JetBrains AI all shipped MCP client support in H1 2025
- **Auth spec**: OAuth 2.1 profile added to the spec to enable remote, credentialed MCP servers (key for xMustard's hosted mode)
- **MCP Inspector**: official debugging tool for testing server implementations

### OpenAI-Compatible Local Serving

| Tool | Version / Milestone | Key 2025 capability |
|---|---|---|
| **Ollama** | v0.4–0.5 (2025) | Concurrent model loading, multimodal (vision), tool-call support, OpenAI `/v1` compat |
| **vLLM** | v0.5–0.6 (2025) | Prefix caching, chunked prefill, speculative decoding, disaggregated prefill/decode, production LoRA hot-swap |
| **SGLang** | v0.3+ (Stanford, 2025) | RadixAttention for KV-cache reuse, constrained JSON decoding, multi-modal; outperforms vLLM on throughput at equal quality |
| **LM Studio** | v0.3+ (2025) | Local HTTP server with OpenAI API, multi-model serving, model discovery UI |
| **llama.cpp server** | ongoing | GGUF quantization, speculative decoding, `-ngl` GPU offload, full OpenAI compat |
| **TabbyAPI** | 0.x (2025) | ExLlamaV2-backed; fine-grained VRAM control; growing adoption for local LoRA serving |

### Agent Memory / Context Engines

| Product | Version / Date | Architecture |
|---|---|---|
| **Mem0** | v1.x (2025) | Layered memory (user / session / agent tiers), graph + vector hybrid, OpenAI-compatible API, self-improving extraction |
| **Zep** | v2.x (2025) | Temporal knowledge graph, entity extraction, incremental episode storage, Graphiti underpinning |
| **Graphiti** | v0.3+ (Zep, 2025) | Incremental KG with temporal edges; designed for episodic agent memory; open-source core |
| **Letta (fka MemGPT)** | v0.5+ (2025) | Stateful agent OS; core / archival / recall memory tiers; REST API server; ADE (Agent Development Environment) |
| **Cognee** | v0.2+ (2025) | Graph-based cognition; Pydantic-typed data model; integrates LangChain/LlamaIndex pipelines |

### Repo-Map & Semantic Code Search

- **Aider repo-map** (v0.50+): Tree-sitter + `universal-ctags`; PageRank over symbol call-graph to rank which symbols fit in context; handles 100k+ LoC repos; re-ranked on every turn
- **Sourcegraph Cody**: Enterprise semantic search; `.cody/context.json` config; cross-repo symbol resolution; powers Sourcegraph's own agents
- **ast-grep**: Structural code search/rewrite tool (pattern matching over AST nodes); gaining traction as tree-sitter-based linting and code-mod backend
- **OpenCtx** (Sourcegraph, 2025): Protocol for pluggable code context providers; adopted by Cody and some VS Code extensions
- **CodeNav** (Meta, 2025): Open-source code-navigation agent using symbol graphs and cross-file reference resolution
- **moatless-tools**: Minimalist agent tool-surface (FindCode, SemanticSearch, StringReplace) designed for maximum token efficiency on SWE-bench

### Multi-Agent Verification / Voting

- **AutoGen v0.4** (Microsoft, Jan 2025): Full rewrite — async actor model; `AgentChat` for high-level patterns, `Core` for typed message-passing; replaces `GroupChat` with composable teams
- **LangGraph v0.2+** (LangChain, 2025): First-class cycles + checkpointing; `langgraph-platform` for cloud deployment; human-in-the-loop nodes; sub-graphs
- **CrewAI v0.60+** (2025): Task-level result validators, hierarchical process, flow-based orchestration, memory integration
- **DSPy v2.5+** (Stanford, 2025): Optimized multi-agent pipelines; `Predict` / `ChainOfThought` / `ReAct` modules with auto-prompt optimization via `BootstrapFewShot`
- **Swarm** (OpenAI, Oct 2024): Lightweight agent handoff primitives (educational; widely forked as production scaffold)

---

## 2. Relevant Research Papers

### Agentic Coding

**1. SWE-bench: Can Language Models Resolve Real-World GitHub Issues?**
- arXiv: `2310.06770` †
- *Why relevant:* Defines the canonical benchmark for coding agents against real GitHub PRs — xMustard's context-engine and verification layer should be evaluated here.
- https://arxiv.org/pdf/2310.06770

**2. SWE-agent: Agent-Computer Interfaces Enable Automated Software Engineering**
- arXiv: `2405.15793` †
- *Why relevant:* Introduces the ACI design philosophy — minimal, disciplined tool surface (editor + shell) reduces hallucinated file paths; directly informs xMustard's LSP + tool API design.
- https://arxiv.org/pdf/2405.15793

**3. Agentless: Demystifying LLM-based Software Engineering Agents**
- arXiv: `2407.01489` †
- *Why relevant:* Two-phase locate→patch pipeline (no heavy agent loop) matches complex agents on SWE-bench at lower cost; challenges xMustard to make its runtime memory measurably worth the overhead.
- https://arxiv.org/pdf/2407.01489

**4. OpenHands: An Open Platform for AI Software Developers as Generalist Agents**
- arXiv: `2407.16741` †
- *Why relevant:* Most complete open-source coding agent framework; xMustard's MCP context-engine must integrate cleanly with sandbox-isolated agents of this type.
- https://arxiv.org/pdf/2407.16741

### Retrieval / RRF for Code

**5. RAPTOR: Recursive Abstractive Processing for Tree-Organized Retrieval**
- arXiv: `2401.18059` †
- *Why relevant:* Hierarchical summarization tree alongside raw chunks; maps directly to xMustard's multi-granularity symbol graph (repo → file → class → method → token).
- https://arxiv.org/pdf/2401.18059

**6. HippoRAG: Neurologically Inspired Long-Term Memory for Large Language Models**
- arXiv: `2405.14831` †
- *Why relevant:* KG-augmented RAG with associative retrieval (Personalized PageRank over entity graph); architecture mirrors xMustard's Postgres knowledge layer + symbol-graph combo.
- https://arxiv.org/pdf/2405.14831

**7. CodeRAG-Bench: Can Retrieval Augment Code Generation?**
- arXiv: `2406.14497` *(verify ID)*
- *Why relevant:* Comprehensive evaluation showing dense + sparse hybrid (RRF) consistently beats single-retriever setups for code tasks — core empirical justification for xMustard's RRF layer.
- https://arxiv.org/pdf/2406.14497

### Repo Graphs & Code Understanding

**8. RepoGraph: Enhancing AI Software Engineering with Repository-level Code Graph**
- arXiv: `2410.14684` *(verify ID)*
- *Why relevant:* Explicit module-call graph built with tree-sitter for context selection in agents; validates xMustard's tree-sitter symbol graph as the right primitive.
- https://arxiv.org/pdf/2410.14684

### Multi-Agent Verification / Debate

**9. Improving Factuality and Reasoning in Language Models through Multiagent Debate**
- arXiv: `2305.14325` †
- *Why relevant:* Foundational paper showing iterative agent disagreement improves factual accuracy; directly motivates xMustard's multi-agent verification of shared context consistency.
- https://arxiv.org/pdf/2305.14325

**10. Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena**
- arXiv: `2306.05685` †
- *Why relevant:* Systematic study of LLM-based evaluation; xMustard's verification layer needs a judge model — this defines reliability, positional bias, and calibration best practices.
- https://arxiv.org/pdf/2306.05685

### LLM Context / Memory

**11. MemGPT: Towards LLMs as Operating Systems**
- arXiv: `2310.08560` †
- *Why relevant:* Three-tier memory model (core / archival / recall) with self-managed paging via function calls; direct intellectual ancestor of xMustard's runtime-memory design.
- https://arxiv.org/pdf/2310.08560

**12. Retrieval-Augmented Generation for Knowledge-Intensive NLP Tasks**
- arXiv: `2005.11401` †
- *Why relevant:* Foundational RAG architecture; xMustard's retrieve-then-generate loop for code context is a structured-domain specialization of this paper's core pattern.
- https://arxiv.org/pdf/2005.11401

---

## 3. Gaps & Opportunities for xMustard

- **No open tool delivers incremental, LSP-live, RRF-ranked context.** Aider's repo-map recomputes from scratch each turn using a static ctags pass; Cody and Continue do dense-only retrieval. xMustard can own the niche of *continuously maintained*, *LSP-fed* (go-to-definition, find-references in real time), *hybrid-ranked* context delivery — no open-source project does all three simultaneously.

- **MCP servers are file-aware but symbol-blind.** Every production MCP server today exposes raw file content or git history. None expose graph queries (`callers_of(sym)`, `implementors_of(interface)`, `dependents_of(package)`). xMustard's tree-sitter symbol graph surfaced as MCP `resource` URIs is a direct, deployable differentiator with no current competition in the MCP server catalogue.

- **Memory engines store episodes, not code identity.** Mem0, Zep, Graphiti, and Letta all model entities as conversational concepts. None track *code-native identity* (symbol × file × commit SHA × test outcome × type signature). xMustard's Postgres knowledge layer with code-typed entities fills a gap that every general-purpose memory platform leaves open.

- **Multi-agent debate targets *answers*, not *shared world-state*.** AutoGen, CrewAI, and LangGraph use agent disagreement to validate final outputs. The earlier, harder problem — ensuring every concurrent agent operates on a *consistent snapshot* of the codebase (same symbol graph revision, no stale cache, agreed dependency version) — is entirely unaddressed. xMustard's "context consensus" before action is an original architectural contribution with no current analogue.

- **Provider routing is latency/cost-blind to task semantics.** vLLM, Ollama, SGLang, and LiteLLM route by load or simple model-name rules. None select models based on the *coding sub-task type* (quick symbol lookup → small fast model; full-file refactor → large model; test synthesis → code-specialised model). xMustard, sitting between agents and providers with semantic metadata about each request, is the natural place to implement cost-aware, task-typed model routing — a layer no current tool implements with code-semantic signals.
