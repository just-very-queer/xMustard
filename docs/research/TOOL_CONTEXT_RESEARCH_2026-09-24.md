# Tool/context interception and compression research

Checked live on 2026-09-24 using official documentation, source, papers, and GitHub APIs. Research only: no installation, certificate changes, proxy configuration, traffic interception, or paid evaluation. Uses the research skill's primary-source method. This complements [competitor parity](COMPETITOR_PARITY_2026-09-24.md), [vision](../VISION.md), and [architecture](../ARCHITECTURE.md).

Scope: augment existing coding agents across concurrent sessions, agent switching, and repositories; shed unnecessary context and improve repository indexing without weakening evidence. UI and replacement-agent development are excluded. The owner confirmed [mitmproxy](https://www.mitmproxy.org/) and [LiteLLM's AI Tools integration documentation](https://docs.litellm.ai/docs/ai_tools). The primary target is low harness resource overhead and useful context delivery, **not reduced API prices or a promised provider-bill reduction**. Claims below concern inspected revisions, not an assertion that every feature is in the latest release. No inspected peer establishes task-quality parity, complete native-tool coverage, and a 50–100 MB active process tree together. Hardware memory bandwidth is a separate measurement from RSS, context size, and tokens.

## Four different integration seams

| Seam | Can observe/change | Important boundary | Examples |
| --- | --- | --- | --- |
| Model HTTP gateway | Conversation messages and tool results submitted to a routed provider request; sometimes tool schemas | Does not control local tool execution itself; requires compatible routing/auth/protocols | mitmproxy, LiteLLM, Headroom |
| MCP gateway/catalog | Tools advertised by attached MCP servers; discovery, descriptions, schemas, downstream calls | Built-in agent file/shell tools are outside this seam unless separately redirected | LiteLLM MCP gateway, mcp-compression-proxy |
| Tool execution/result adapter | Outputs from covered shell commands or client hooks | Hook capabilities and coverage are client/version-specific | RTK, context-mode, client-native adapters |
| Explicit compression library | Text selected by its caller | No interception, durable memory, or integration automatically follows | LLMLingua, LiteLLM compress() |

These distinctions are architectural inferences from the implementations below. Native tools can still be visible at the later model-request seam. Conversely, MCP installation alone does not make xMustard authoritative over every tool result. Current client hook capabilities must be version-tested, not inferred from older MCP-only restrictions.

## Version snapshot

“Release” means GitHub's latest non-prerelease release, not a package-registry or deployment guarantee.

| Project | Inspected revision/date | Latest release/date |
| --- | --- | --- |
| mitmproxy | [b506c681](https://github.com/mitmproxy/mitmproxy/commit/b506c68108e287104045333ade476d92c39c275e), Sep 10 | [v12.2.3](https://github.com/mitmproxy/mitmproxy/releases/tag/v12.2.3), May 12 |
| LiteLLM | [1c8a0ff6](https://github.com/BerriAI/litellm/commit/1c8a0ff6023b3eba6b01e955c49edcfa97bf0d22), Sep 24 | [v1.102.1](https://github.com/BerriAI/litellm/releases/tag/v1.102.1), Sep 23; separate v1.104.0-dev.1 prerelease |
| mcp-compression-proxy | [886fb2a1](https://github.com/kdpa-llc/mcp-compression-proxy/commit/886fb2a1ebb7f4dee6e601160ef29f713150fa19), Sep 24 | [v1.3.0](https://github.com/kdpa-llc/mcp-compression-proxy/releases/tag/v1.3.0), Sep 24 |
| Headroom | [7f2766ca](https://github.com/headroomlabs-ai/headroom/commit/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a), Sep 24 | [v0.38.0](https://github.com/headroomlabs-ai/headroom/releases/tag/v0.38.0), Sep 21 |
| LLMLingua | [5a4c78ae](https://github.com/microsoft/LLMLingua/commit/5a4c78ae18ab17a98cf997e8259354e546081d64), Sep 10 | [v0.2.2](https://github.com/microsoft/LLMLingua/releases/tag/v0.2.2), Apr 9 **2024** |
| RTK | **develop** [f5e104e1](https://github.com/rtk-ai/rtk/commit/f5e104e117ab5b05c69d448103c28f1155e04417), Sep 24 | [v0.49.0](https://github.com/rtk-ai/rtk/releases/tag/v0.49.0), Sep 11 |
| context-mode | [5a92b7ca](https://github.com/mksglu/context-mode/commit/5a92b7caaf0086d04b87cc03a82505c891aa8254), Sep 23 | [v1.0.169](https://github.com/mksglu/context-mode/releases/tag/v1.0.169), Jun 29 |

## mitmproxy: interception substrate, not a compressor

mitmproxy supplies mutable HTTP flow events and multiple proxy modes; HTTPS interception requires client trust/routing configuration. It does not supply an LLM-specific contract for tool IDs, reasoning blocks, schema integrity, or useful compression. Default body handling buffers the complete body. When streaming is enabled, regular body replacement does not apply; SSE/WebSocket handling needs explicit implementation and tests. Its generic replay matching is not an agent-state-aware semantic cache. [Events](https://docs.mitmproxy.org/stable/api/events.html), [modes](https://docs.mitmproxy.org/stable/concepts/modes/), [certificates](https://docs.mitmproxy.org/stable/concepts/certificates/), [streaming and replay](https://docs.mitmproxy.org/stable/overview/features/).

Fit inference: useful for controlled protocol observation or an experimental adapter, not evidence that xMustard should require TLS interception. A routed model payload may contain built-in shell/file outputs, but interception is later than execution. No comparable 50–100 MB whole-tree measurement was located in the reviewed material.

The confirmed [mitmproxy homepage](https://www.mitmproxy.org/) reinforces the headless option: `mitmdump` runs Python addons for inspecting/modifying/replaying traffic, including HTTP/1–3 and WebSockets. This adds protocol reach, not LLM-aware compression or indexing. Previously checked LiteProxy/LightProxy names are outside the now-confirmed scope.

## LiteLLM: gateway, MCP routing, and now beta compression

LiteLLM offers provider routing and request/response hooks, plus a distinct MCP gateway with tool discovery, namespacing and access policies. That MCP gateway is not automatic interception of native agent tools. Claude Code integration changes the API endpoint/auth path; subscriptions and existing billing routes must not be assumed equivalent. Its own current guide warns custom base URLs can disable Claude Code's on-demand tool search unless explicitly re-enabled. These are version-sensitive integration checks. [Hooks](https://docs.litellm.ai/docs/proxy/call_hooks), [MCP gateway](https://docs.litellm.ai/docs/mcp), [Claude Code guide](https://docs.litellm.ai/docs/tutorials/claude_responses_api).

Current beta `compress()` scores history with BM25/optional embeddings, replaces lower-priority content with retrieval stubs, and supplies a retrieval tool/cache. The `/v1/messages` `compression_interception` callback also handles retrieval internally with additional provider calls. Reported evaluation covers only **five** SWE-bench-Lite problems: 77.7% prompt-token reduction and 72% cost reduction, but measures patch-file/hunk overlap and changed-line similarity, **not resolved-issue test success**. Hunk overlap falls from .582 to .361. This is a small vendor experiment, not demonstrated coding-agent parity. [Compression documentation](https://docs.litellm.ai/docs/completion/prompt_compression).

Important source/documentation discrepancy: despite the documentation's broad recoverability claim, inspected `compress.py` skips dropped Anthropic tool-exchange spans before adding originals to the retrieval cache; truncated retained messages are also not added there. Anthropic exchanges are treated atomically; system/current messages and the explicit provider-cache prefix are protected. The callback's original-content cache is process-local, per call, with a **15-minute TTL**. Thus “raw content always recoverable” is not established. [Compressor source](https://github.com/BerriAI/litellm/blob/1c8a0ff6023b3eba6b01e955c49edcfa97bf0d22/litellm/compression/compress.py), [callback source](https://github.com/BerriAI/litellm/blob/1c8a0ff6023b3eba6b01e955c49edcfa97bf0d22/litellm/integrations/compression_interception/handler.py).

Keep three caches distinct: provider prefix caching, exact response caching, and semantic response caching. LiteLLM explicitly warns semantic caching can replay stale agent responses because successive conversation embeddings remain similar and tool-call metadata is not embedded. Its documented response-cache surfaces also exclude Anthropic Messages/pass-through. Exact response caching is not repo-artifact freshness verification. [Semantic-cache limitations](https://docs.litellm.ai/docs/proxy/caching_semantic), [provider prompt caching](https://docs.litellm.ai/docs/completion/prompt_caching).

The new **beta Rust gateway** does not make normal LiteLLM deployments Python-free: default opt-in mode retains Python auth/routing/callbacks, and `/chat/completions` tool calls/results fall back to Python. Standalone Axum has narrower coverage and no published prebuilt image in the inspected docs. No evidence establishes full proxy/compression parity within xMustard's RAM target. [Rust gateway](https://docs.litellm.ai/docs/proxy/rust_gateway).

### Confirmed AI Tools page: material additions

The owner's [AI Tools link](https://docs.litellm.ai/docs/ai_tools) is a directory of client integrations, not evidence of universal interception. Its linked pages add these distinctions to the earlier audit:

- **Actual routing must be selected.** OpenCode uses a configured OpenAI-compatible provider; other providers can bypass LiteLLM. Its custom-provider image modalities must be declared or images are removed before reaching the gateway. Codex inference routing selects a provider using the Responses API; MCP registration is separate. Neither setup means control of local tool execution. [OpenCode](https://docs.litellm.ai/docs/tutorials/opencode_integration), [Codex](https://docs.litellm.ai/docs/proxy/client_setup/codex_cli).
- **Deferred MCP discovery is already a first-class capability.** Key-scoped tool search ranks names/descriptions by token overlap without embeddings; a separate semantic filter uses embeddings on model-request surfaces. Current documentation drifts on the virtual catalog size: the cost guide says two tools, while the dedicated guide lists four but retains three-tool prose. Pin and test the catalog instead of copying a count. This is schema-context reduction, not result compression. [Tool search](https://docs.litellm.ai/docs/mcp_tool_search), [semantic filter](https://docs.litellm.ai/docs/mcp_semantic_filter), [cost guide](https://docs.litellm.ai/docs/tutorials/claude_code_cut_costs).
- **Context editing is another mechanism beyond compress().** The documented `context_management` polyfill clears old tool-result content or invokes a configured summary model. It preserves message/role structure for clearing but currently accepts and ignores `exclude_tools`, `clear_tool_inputs` and `clear_at_least`; thinking clearing is not implemented there. Summary compaction is lossy and adds a model call. Do not treat compatible parameter acceptance as preservation-policy enforcement or verified memory. [Context management](https://docs.litellm.ai/docs/claude_code_context_management).
- **Composition has prerequisites.** LiteLLM documents Headroom as an opt-in `pre_call` sidecar for Chat Completions/Messages and cache-aware deployment affinity for repeated prefixes. Budget fallbacks and auto-routing change the selected model, not just context size; router metadata does not set the client's compaction window. These remain optional adapters/reference patterns, not required xMustard runtime dependencies. [Headroom integration](https://docs.litellm.ai/docs/proxy/headroom), [cache routing](https://docs.litellm.ai/docs/tutorials/claude_code_prompt_cache_routing), [auto-router boundaries](https://docs.litellm.ai/docs/tutorials/claude_code_autorouter).
- **Coverage is tested per client/provider/version.** The matrix inspected was generated Sep 23 for LiteLLM v1.102.0 and Claude Code 2.1.228; a dash means untested, not unsupported. LiteLLM calls its Cursor integration best-effort, requires an internet-reachable gateway for the documented IDE path, and says the Cursor CLI cannot target it. This rules out assuming every listed client supports a local-only gateway. [Compatibility matrix](https://docs.litellm.ai/docs/claude_code_compatibility), [Cursor limits](https://docs.litellm.ai/docs/tutorials/cursor_integration).
- **Usage visibility is secondary, not compression proof.** The tracking guide accounts for requests routed through LiteLLM and labels clients using their User-Agent; it is not a telemetry-only sidecar for unmodified direct traffic. A separate Max guide supports subscription OAuth routing, so API-key-only generalizations are wrong; tracked cost is not proof of a matching subscription invoice. Neither guide establishes lower RSS, hardware memory bandwidth, context-quality parity or original recovery. [Usage tracking](https://docs.litellm.ai/docs/tutorials/cost_tracking_coding), [subscription routing](https://docs.litellm.ai/docs/tutorials/claude_code_max_subscription).

## mcp-compression-proxy: closest MCP catalog/result reference

The TypeScript project offers full or lazy MCP catalogs, namespaced tools, description rewriting, and result offloading. Model-free JSON projection is available; text extraction may use a model. Optional Needle ranking is distinct from description compression and payload shaping. Its 30-query/43-tool fixture reports BM25 top-1/top-5 **17/23**, versus BM25+Needle **15/26**: better top-five recall but worse first choice. This is retrieval evidence, not autonomous action correctness or invoice savings. [Project documentation](https://github.com/kdpa-llc/mcp-compression-proxy/tree/886fb2a1ebb7f4dee6e601160ef29f713150fa19).

Schema rewriting changes descriptions of existing top-level properties, not names/types/required fields. Cache lookup uses server/tool plus original-description matching; that is not complete schema/repository/policy identity. Stale detection depends on both original descriptions being present. The executor opens a downstream SDK call and explicitly rejects task-based MCP results. Request-ID correspondence, cancellation, progress, structured results and arbitrary extension forwarding therefore require conformance tests; this is not proven byte-transparent forwarding. [Description/schema cache](https://github.com/kdpa-llc/mcp-compression-proxy/blob/886fb2a1ebb7f4dee6e601160ef29f713150fa19/src/services/compression-cache.ts), [executor](https://github.com/kdpa-llc/mcp-compression-proxy/blob/886fb2a1ebb7f4dee6e601160ef29f713150fa19/src/mcp/tool-call-executor.ts).

Payload offloading retains originals in owner-private files with content-derived IDs, bounded by **100 entries by default**, not an overall byte budget. It accepts the complete output string, and read/find load the entire file before slicing/searching. Consequently, paged context is **not** bounded ingestion/read RSS, and eviction limits recovery. Shared backend processes and per-client config are useful patterns, not a tenant-isolation guarantee. [Payload store](https://github.com/kdpa-llc/mcp-compression-proxy/blob/886fb2a1ebb7f4dee6e601160ef29f713150fa19/src/cli/payload-interceptor.ts), [client view](https://github.com/kdpa-llc/mcp-compression-proxy/blob/886fb2a1ebb7f4dee6e601160ef29f713150fa19/src/proxy/client-view.ts).

## Headroom: direct provider-payload comparator

Official upstream is **headroomlabs-ai/headroom**; older chopratejas URLs redirect there, and headroom-mcp is a separate fork. It has model-API proxy handlers plus SDK/MCP surfaces. Routed native-tool results are visible at the model request, unlike an MCP-only wrapper. Provider handlers correlate call IDs and protect file-read content needed for exact edits. Schema compaction still removes some annotations and normalizes descriptions: it is not untouched-schema forwarding. [OpenAI handler](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/headroom/proxy/handlers/openai.py), [schema compaction](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/headroom/proxy/tool_schema_compaction.py).

CCR recovery injects a retrieval tool but has capacity eviction and **30-minute default retention**; direct SDK use does not automatically provide the whole retrieval loop. Cache mode freezes previously processed content; CacheAligner itself is detector-only and disabled inside the proxy. These are stronger mechanisms than unconstrained per-turn rewriting, not guarantees of indefinite evidence retention. [CCR](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/docs/content/docs/ccr.mdx), [session/cache engine](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/headroom/proxy/session_engine.py).

Current text compression is Kompress/ModernBERT, not the removed LLMLingua implementation. Python/Rust plus ONNX Runtime/Transformers dependencies and optional heavier ML do not establish a 50–100 MB complete process tree. Seeded workflow fixtures show 21–57% reduction; the benchmark documentation distinguishes timings/anomaly retention from model accuracy and says the paid QA comparison lacks committed results. [Dependencies](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/pyproject.toml), [README fixtures](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/README.md), [benchmark limitations](https://github.com/headroomlabs-ai/headroom/blob/7f2766ca4beb9270ce35aa4f18aa01ef7ae3698a/docs/content/docs/benchmarks.mdx).

## RTK: low-overhead shell subset

RTK is a Rust command-output filter activated explicitly or through command-rewriting hooks. Its README expressly excludes native Read/Grep/Glob from Bash-hook coverage; it is not a provider-history or tool-schema proxy. Current development source includes SQLite recovery for failures/truncation through `rtk recall`, so “no raw recovery” is stale. Default limits include 200 entries, 30 days and 10 MiB per entry; successful runs are not generally archived by that mechanism. Historical recall is not freshness validation. [README](https://github.com/rtk-ai/rtk/blob/f5e104e117ab5b05c69d448103c28f1155e04417/README.md), [retriever](https://github.com/rtk-ai/rtk/blob/f5e104e117ab5b05c69d448103c28f1155e04417/src/core/retriever.rs).

A single Rust binary with bundled SQLite and no compressor model is an attractive architectural reference, but invoked compilers/tests still count toward the process tree. “Up to 90%” concerns Bash-output **bytes**, not bills; token estimates use bytes/4. An ON/OFF session runner exists, but its required sibling modules/setup artifacts are missing from the inspected tree: it is not a reproducible completed task-quality evaluation. [Dependencies](https://github.com/rtk-ai/rtk/blob/f5e104e117ab5b05c69d448103c28f1155e04417/Cargo.toml), [economics explanation](https://github.com/rtk-ai/rtk/blob/f5e104e117ab5b05c69d448103c28f1155e04417/docs/guide/resources/savings-explained.md), [runner](https://github.com/rtk-ai/rtk/blob/f5e104e117ab5b05c69d448103c28f1155e04417/scripts/benchmark-sessions/lib/runner.py).

## context-mode: execute/index/search rather than transparent forwarding

context-mode offers MCP/native-plugin tools, subprocess execution, indexing/search and host-dependent redirection hooks. Its README's older Codex restriction disagrees with current hook source, which detects Codex ≥0.141.0 and uses updated input/context; older builds deny redirects. This illustrates why client integration needs pinned conformance tests. [Codex hook](https://github.com/mksglu/context-mode/blob/5a92b7caaf0086d04b87cc03a82505c891aa8254/hooks/codex/pretooluse.mjs).

Indexed chunks are retrievable, but HTML-to-Markdown, parsed JSON and user summaries are not universal original-byte recovery. Fetch cache defaults to **24 hours**, keyed by source/URL, with explicit refresh controls. File refresh requires advancing mtime followed by changed SHA-256; deleted files deliberately keep cached results. These policies are not verified-current repository memory. [Server/cache](https://github.com/mksglu/context-mode/blob/5a92b7caaf0086d04b87cc03a82505c891aa8254/src/server.ts), [store](https://github.com/mksglu/context-mode/blob/5a92b7caaf0086d04b87cc03a82505c891aa8254/src/store.ts).

Its fixture benchmark reports 376 KB to 16.5 KB (96%), but distinguishes tiny summaries from usable exact chunks: chunk retrieval saves 50–93%, 82% aggregate. It explicitly warns tiny documentation summaries can be useless for coding. No matched-agent success/cost proof follows. Node, native SQLite and spawned runtimes have an unmeasured complete-tree footprint. [Benchmark](https://github.com/mksglu/context-mode/blob/5a92b7caaf0086d04b87cc03a82505c891aa8254/BENCHMARK.md), [dependencies](https://github.com/mksglu/context-mode/blob/5a92b7caaf0086d04b87cc03a82505c891aa8254/package.json).

## LLMLingua: optional lossy algorithm, not integration

LLMLingua is a Python/model compression library, including structured/JSON helpers. Protected segments and tokens are caller-configured; JSON reparsing does not establish automatic tool-ID/schema integrity, original-byte recovery, or provider-prefix preservation. It supplies no native-agent interception layer. Standard Torch/Transformers configurations and default 7B/LLMLingua-2 encoder models are not plausible 50–100 MB total-RSS defaults; this is an architectural inference, not a local measurement. [Implementation](https://github.com/microsoft/LLMLingua/blob/5a4c78ae18ab17a98cf997e8259354e546081d64/llmlingua/prompt_compressor.py), [dependencies](https://github.com/microsoft/LLMLingua/blob/5a4c78ae18ab17a98cf997e8259354e546081d64/setup.py).

Its ACL research provides controlled task evidence across MeetingBank, LongBench, ZeroScrolls, GSM8K and BBH, reporting 2–5× compression and 1.6–2.9× acceleration. Those workloads are stronger evidence than byte-only demos, but not contemporary coding-agent repairs or measured current provider invoices. [LLMLingua-2 paper](https://aclanthology.org/2024.findings-acl.57/).

## Implications and unresolved choices for xMustard

These are proposed evaluation criteria, not implemented capabilities or approved scope expansion:

1. Prefer exact projection, duplicate elimination, query-scoped chunks and lazy catalog loading before lossy rewriting. Compression is a derived view; immutable raw artifacts remain the evidence authority.
2. Choose adapters explicitly: own MCP results first, client-native hooks where supported, optional provider gateway for broader routed visibility. Do not promise universal interception or silently change billing/auth/certificate trust.
3. Preserve tool names, argument types, required fields, result/error structure, call correlations and reasoning/cache-sensitive blocks. Test streaming, parallel calls, cancellation, task-based MCP, multimodal results and retrieval misses. Pass through original content when shaping is unsafe only within admitted budgets; otherwise return an explicit protocol-compatible size/quota failure.
4. Scope derived caches by content hash, repository revision/dirty state, workspace, principal/policy, tool/schema and compressor version. Cross-agent sharing must not become cross-repository or cross-user leakage; semantic answer reuse is not verification.
5. Make raw retrieval retention/eviction explicit. Measure ingest/read memory, not just returned chunk size or model-file size; include child processes and optional models in steady/peak RSS, CPU time and startup/first-use latency. Profile actual hardware memory bandwidth separately; it cannot be inferred from RSS or token counts.
6. Run paired representative coding tasks: resolution/tests, indexing freshness and evidence recall, missed evidence, bad edits, context bytes/tokens delivered, recovery calls and total latency. Provider input/output/cache usage is a secondary diagnostic: a smaller prompt may invalidate a reusable prefix or add retrieval/model calls. API cost reduction is not this product's primary acceptance target.

The strongest initial design references are mcp-compression-proxy for progressive MCP discovery/result access, RTK for model-free typed output reduction, and Headroom/LiteLLM for provider-payload edge cases. Whether xMustard embeds, interoperates with, or merely borrows these patterns remains a product/engineering choice requiring client conformance and resource measurements.

## Needle and optional local models

The owner identified "Jev" as Typesafe's System One service. Its hosted typed
decisions, artifact availability and indexing limits are covered separately in
[Jev research](JEV_RESEARCH_2026-09-24.md); it is not another verified local
tiny-model release.

Needle 3 is a plausible **optional tool selector or structured extractor**, not an established code summarizer or default search engine. The current released configuration describes 121,021,910 parameters and 8,192 maximum positions, with distinct local-attention and KV-window settings. These are configuration limits, not tested long-document accuracy. The actual `needle3.cact` artifact is **35,335,380 bytes (35.3 MB)**, despite the website's 8–29 MB range. File size is not inference RSS. Model revision inspected: `b274efcb211a9eef48c9a88da4b43bd569696a39`, September 19; Apache-2.0. [Configuration](https://huggingface.co/Cactus-Compute/needle3/blob/b274efcb211a9eef48c9a88da4b43bd569696a39/config.json), [artifact](https://huggingface.co/Cactus-Compute/needle3/blob/b274efcb211a9eef48c9a88da4b43bd569696a39/needle3.cact), [model card](https://huggingface.co/Cactus-Compute/needle3).

The general Python guide describes automatic tool retrieval, but the more specific porting guide says the shipped archive **has no contrastive embedding head**, so automatic shortlisting is disabled. Its `needle_embed` output is a normalized 3,072-dimensional confidence-probe vector, not a contrastively trained retrieval representation. The guide warns of declining accuracy above roughly two dozen tools and recommends external shortlisting. The released configuration lists only the confidence head. Treat the specific artifact/porting evidence as authoritative for a prototype. [Python guide](https://cactuscompute.com/blog/needle-python-docs), [porting guide](https://cactuscompute.com/blog/porting-needle).

Needle's documented interaction returns tool calls/results rather than a generated free-text answer. Schema-constrained extraction can produce candidate IDs, fields or routing decisions; valid structure does not prove those values correct. In xMustard, resolve selected IDs to original evidence and validate paths/spans before returning context. Do not ask the helper to invent a trustworthy summary or approve a claim. [Interaction contract](https://cactuscompute.com/blog/needle-python-docs), [extraction guide](https://cactuscompute.com/blog/structured-extraction-with-needle).

Confidence needs task-specific calibration. The default threshold is 0.1, tool triggers can bypass some gates, and the vendor reports correct Spanish calls scoring 0.0. Wrapper `extract()` can recover suppressed calls without exposing the full confidence envelope; use the lower-level completion result when fallback depends on confidence. Some tuned archives lack that head. Published mobile-action, single-turn tool-call and extraction benchmarks do not establish coding-agent repair quality. [Confidence](https://cactuscompute.com/blog/needle-confidence), [wrapper](https://github.com/cactus-compute/needle/blob/main/needle/__init__.py), [benchmark scope](https://cactuscompute.com/needle).

Native CLI/C interfaces avoid restoring Python runtime ownership. The published C header describes a process-global, non-thread-safe model: serialize inference in an isolated worker and reset between unrelated requests. Refuse overflowing input instead of silently cutting evidence; disable default telemetry (`NEEDLE_TELEMETRY=0`, `DO_NOT_TRACK=1`). Keep the helper off by default until combined Go/Rust/adapter/helper RSS and quality are measured. No weights were downloaded or benchmarked here. [Deployment](https://cactuscompute.com/blog/needle-supported-devices), [C header](https://huggingface.co/Cactus-Compute/needle3/resolve/b274efcb211a9eef48c9a88da4b43bd569696a39/macos-arm64/needle.h), [runtime notes](https://github.com/cactus-compute/needle).

| Alternative | Useful comparison and budget limit |
| --- | --- |
| FunctionGemma 270M | Specialized tool-calling fine-tuning; Google's tested int8 Mobile Actions artifact is 288 MB and peak RSS is 551 MB. Not a default-budget fit. [Model card](https://ai.google.dev/gemma/docs/functiongemma/model_card) |
| Liquid LFM2.5-230M | Tool-capable small model; official Q4 files are 149 MB, vendor memory measurements 293 MB on Pi 5 and 375 MB on S25 Ultra. Larger-budget opt-in only; LFM Open License applies. [Model](https://huggingface.co/LiquidAI/LFM2.5-230M), [files](https://huggingface.co/LiquidAI/LFM2.5-230M-GGUF/tree/main), [measurements](https://www.liquid.ai/blog/lfm2-5-230m) |
| Model2Vec potion-base-8M | MIT static embeddings, 30.2 MB F32 weights and native Rust support. Worth a ranking/deduplication experiment, not generation; complete-process RSS and repository retrieval remain unproven. [Model](https://huggingface.co/minishlab/potion-base-8M), [Rust runtime](https://minish.ai/packages/model2vec-rs/usage/) |

Recommendation: deterministic reduction first; compare optional evidence selection against that baseline using task success, evidence recall, false omissions, expansions, context delivered, latency, CPU and total RSS. Measure hardware memory bandwidth separately where suitable profiling is available. Provider usage is secondary; no silent paid-model fallback or automatic resource-budget increase.

## Current client integration boundaries

The following are documentation/source capabilities, **not tested xMustard integrations**. Pin client versions and exercise the actual configured client before advertising coverage.

| Client/surface | Current capability | Limit |
| --- | --- | --- |
| Claude Code `PostToolUse` | `updatedToolOutput` can replace results for all tools, including Bash; older `updatedMCPToolOutput` is MCP-only | Replacement must match the tool schema; invalid built-in replacements are ignored. Effects have already occurred and telemetry may retain originals. [Hooks](https://code.claude.com/docs/en/hooks#posttooluse-decision-control) |
| OpenCode V2 plugins | `execute.before` can alter input; `execute.after` can replace successful output; context hooks can change model-bound messages | V1/V2 APIs differ. Keep reduction separate from command rewriting and test failures/cancellation. [Plugins](https://opencode.ai/v2/docs/build/plugins), [migration](https://opencode.ai/v2/docs/build/plugins/migrate-v1) |
| Codex hooks | Built-in/local/MCP tool lifecycle hooks; post-tool feedback through blocking or `continue: false` can replace model-visible output | `updatedMCPToolOutput` is unsupported. Code-mode blocking can reject a nested promise; this is not a uniform transparent result replacement API. [Hooks](https://learn.chatgpt.com/docs/hooks) |
| Codex app-server | Structured sessions/events, usage, dynamic tools and command/file approval exchanges | Dynamic tools cannot take over reserved built-in namespaces. It is an explicit integration, not automatic interception. [Protocol](https://learn.chatgpt.com/docs/app-server) |

Local read-only help inspection found Codex 0.156.1 with JSON events, output-schema, worktree/sandbox and app-server support. No live client hook, provider credential, or paid run was exercised. MCP itself only covers routed MCP calls, not every host-native tool. [MCP tool protocol](https://modelcontextprotocol.io/specification/2025-06-18/server/tools).

At a model gateway, preserve IDs, role ordering and tool/result pairing. Never rewrite signed/encrypted reasoning or required continuation objects. Repeatedly rewriting old context can invalidate reusable prompt prefixes and defeat savings. Prefer supported fresh-result delivery, then preserve those delivered bytes in history. [Claude tool protocol](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls), [Claude thinking](https://platform.claude.com/docs/en/about-claude/models/extended-thinking-models), [OpenAI reasoning](https://developers.openai.com/api/docs/guides/reasoning), [cache behavior](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching).

## Pi: confirmed lightweight-harness reference

The owner confirmed Mario Zechner's Pi. `badlogic/pi-mono` currently redirects
to `earendil-works/pi`; inspected revision
[`8676a0d`](https://github.com/earendil-works/pi/commit/8676a0dcd8f9f6bca78835e63c8cd31493c4154d),
September 24. This is a design reference, not a proposal to replace xMustard's
Go/Rust foundation with Pi's runtime.

Pi defaults to four coding tools (`read`, `bash`, `edit`, `write`), with eight
built-ins available overall. SDK/RPC integration does not require the TUI.
Extensions can transform model context, postprocess tool results, customize
compaction and expose initially inactive tools. These are concrete seams for
xMustard's evidence delivery without taking over the agent loop.
[Tool factories](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/src/core/tools/index.ts),
[SDK](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/docs/sdk.md),
[extensions](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/docs/extensions.md).

Compaction preserves original session entries while presenting a summary plus
recent messages; call/result pairing is retained. Default budgets reserve 16,384
tokens and keep roughly 20,000 recent tokens. This separates durable history
from model-visible context, but does not itself prove resident transcript memory
was reclaimed.
[Compaction](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/docs/compaction.md).

Tool output limits are not whole-process memory limits. Bash retains a rolling
tail while spilling full output to a temporary file; text reads still load and
split the full file before applying offset/limit/truncation. Borrow the streaming
spill pattern and improve source-exact bounded reads through xMustard's index.
Do not equate a 50 KiB result limit with low ingest allocations.
[Bash accumulator](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/src/core/tools/output-accumulator.ts),
[read implementation](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/coding-agent/src/core/tools/read.ts).

No verified 50–100 MB whole-Pi figure or matched OpenHands comparison was found.
Pi's benchmark notes distinguish retained footprint from allocation churn and
disclaim production representativeness. Compare bare Pi versus Pi+xMustard on
the same tasks, measuring incremental overhead and complete workflow separately.
Keep footprint, memory traffic and model-context volume as three measurements.
Pi extensions are not an OS permission/sandbox guarantee; human merge authority
still requires enforcement outside a prompt.
[Benchmark method](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/packages/agent/benchmark/session/README.md),
[security scope](https://github.com/earendil-works/pi/blob/8676a0dcd8f9f6bca78835e63c8cd31493c4154d/README.md).

## OpenHands comparison and human final merge authority

OpenHands is not only a UI. Its SDK supplies an execution loop, tools, persisted state, workspace/security controls and metrics. Its ACP integration already delegates to existing agents: **wrapping another coding agent is not a unique xMustard capability**. Under ACP, the delegated agent owns its model/context/tools; OpenHands cannot inject its usual condenser, tools or critic. Current documentation says ACP permission requests are automatically approved, distinct from native-agent confirmation policy. [ACP integration](https://docs.openhands.dev/sdk/guides/agent-acp).

Native confirmation supports always/never/risk-based policies; a risk score is not itself an enforcement boundary. Goal completion can involve an additional judging model. OpenHands also supports issue-resolution and PR-review workflows. xMustard should compare these headless workflows, not compete on UI breadth. [Security](https://docs.openhands.dev/sdk/guides/security), [goal checks](https://docs.openhands.dev/sdk/guides/convo-goal), [PR review](https://docs.openhands.dev/sdk/guides/github-workflows/pr-review), [GitHub action](https://docs.openhands.dev/openhands/usage/run-openhands/github-action).

The owner's settled rule is **agents work; a human approves final merge**. Keep memory assurance, execution permission and merge authorization distinct. Recommendation: bind approval to repository/PR/base/head SHA and required checks, invalidate it on new commits, and enforce through repository permissions plus worker credentials that cannot bypass review. Current xMustard run-plan approval and memory votes do not implement this gate; this research changes no GitHub protections.

The owner's clarified target is verified repository-bound memory, better indexing and context shedding with **lower harness resource overhead**, closer to a small pi-like harness rather than a larger orchestration runtime. This is an intended comparison, not a measured advantage or a completed pi audit. Compare bare agent versus xMustard with the same worker, then matched OpenHands versus xMustard tasks/revisions/checks. Include failed attempts, helper processes, retrieval/index latency, steady/peak RSS, CPU, human intervention and time to verified completion; measure actual hardware memory bandwidth separately. Provider/auxiliary-model usage remains a secondary diagnostic, not a promised API-price reduction. [OpenHands metrics](https://docs.openhands.dev/sdk/guides/metrics).

Neither this research nor current xMustard estimates proves a resource advantage. Set numeric harness-overhead, context-delivery and indexing-quality gates with an acceptable task-quality margin before benchmarking; do not substitute token reduction for memory-bandwidth measurements. Implementation sequencing and acceptance gates are in the [roadmap](../ROADMAP.md); the proposed ownership/retention/approval contract is in [the context-layer design](../CONTEXT_LAYER.md).
