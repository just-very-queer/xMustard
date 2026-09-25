# Jev / System One: fit for xMustard

Checked **2026-09-24** against the user's exact [TypeSafe announcement](https://typesafe.ai/blog/introducing-system-one-models-and-jev), linked documentation, official repositories, and provider documentation. Research only: no models downloaded, packages installed, inference calls made, or repository/session content sent to a provider.

## Decision

**Useful design pattern; not a verified local tiny-model option.** Jev is a hosted typed-decision service. Consider it only as a separately approved, optional cloud scorer of a small retrieved shortlist. Keep the default model-free and local, with no Docker and the existing 50–100 MB harness target. Lower harness memory/bandwidth overhead, better indexing, and safe context shedding are the objective; lower provider charges are incidental.

Neither Jev nor Needle should become the authority for shared verified memory, repository truth, verification, or merge decisions. Existing coding agents remain the generators; humans retain merge authority.

## What is available now

| Item | Verified position |
|---|---|
| Model | Direct API lists `jev-1.13.0`; `jev-latest` and `jev-preview` currently resolve to it. Pin the version for evaluation. [Models](https://docs.typesafe.ai/models) |
| Interface | Text/JSON state plus typed questions; returns Choice, Score, or Noul (probability of yes). Choice allows at most 255 options; Score allows up to 10 levels. This is not a chat/completions or free-text generation interface. [API](https://docs.typesafe.ai/api), [coding-agent guidance](https://docs.typesafe.ai/introduction/coding-agents) |
| Context | 64K tokens across state and all questions, **and** 32K across state and the longest question. English is strongest; other languages require workload testing. [Models](https://docs.typesafe.ai/models) |
| Local artifacts | No public Jev weights, parameter count, quantization, local runtime, model-weight license, or RSS measurement found in the inspected official docs and ten-repository organization. This is a bounded availability finding, not a claim about private deployments. [Official organization](https://github.com/typesafe-ai) |
| Hosted availability | September 15 launch was early access. Hosted access is also documented by Vercel and Cloudflare, so “waitlist only” would be inaccurate. Both are remote options, not local model releases. [Vercel announcement](https://vercel.com/changelog/typesafe-ai-jev-now-available-on-ai-gateway), [Cloudflare model](https://developers.cloudflare.com/ai/models/typesafe/jev/) |
| Customization | No customer fine-tuning or LoRA; use request state, instructions, and criteria. [Models](https://docs.typesafe.ai/models) |
| Price / rate limits | Informational: direct price is $0.042/M input tokens, output free; currently 250K tokens/second and 1,200 requests/minute, explicitly changeable. [Models](https://docs.typesafe.ai/models) |
| Latency | Vendor claims 70–500 ms end-to-end; published tests generally originated on the US West Coast near the service. Not measured here, and not an xMustard latency guarantee. [Launch](https://typesafe.ai/blog/introducing-system-one-models-and-jev) |

The [Python SDK](https://github.com/typesafe-ai/typesafe-sdk-python) and [JS/TS SDK](https://github.com/typesafe-ai/typesafe-sdk-js) are MIT-licensed **API clients**, not inference engines. No official Go/Rust SDK is listed; existing Go can use authenticated `POST https://api.typesafe.ai/v1/systemone` without introducing Python/Node. The [System One adapter](https://github.com/typesafe-ai/system-one-adapter-python) substitutes other LLM APIs behind a compatible interface; it does not contain Jev. [SDK index](https://docs.typesafe.ai/sdk), [HTTP contract](https://docs.typesafe.ai/api)

TypeSafe states customer requests/responses are not used for training; its direct-service legal documentation describes enterprise zero-data-retention availability. That is not permission to export private repository/session content, nor proof of default zero retention. Provider-specific policies still need review before an approved cloud trial. [Data handling](https://docs.typesafe.ai/models), [legal documentation](https://docs.typesafe.ai/legal)

## Capabilities versus claims

- **Context selection, not a new index:** the RAG cookbook uses another service for embeddings, Jev to classify already-retrieved passages, and a generative model for the answer. Its example uses `jev-1.12`, not the current version. Jev could propose relevance/conflict labels; it does not replace filesystem parsing, dependency indexing, freshness checks, or retrieval. [RAG cookbook](https://docs.typesafe.ai/cookbooks/classifying_rag_passages)
- **Extraction is bounded selection:** the function-calling example chooses tools and closed-set arguments. Unconstrained text/numbers retain defaults; the documented alternative is to pre-extract candidates and ask Jev to choose. It is not arbitrary tool-argument generation or an abstractive summarizer. [Function cookbook](https://docs.typesafe.ai/cookbooks/function_calling), [known limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13)
- **Schema correctness is not factual correctness:** the launch's zero-hallucination figure represents guaranteed schema matching, not an empirical zero-error measurement. The official failure notes acknowledge unreliable counting/arithmetic, indirection, context distraction, and adversarial state that can steer answers. [Launch](https://typesafe.ai/blog/introducing-system-one-models-and-jev), [known limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13)
- **Confidence is not a verification receipt:** Choice/Score confidence is computed from their probability distribution; Noul has no separate confidence field. Thresholds require domain validation. Separate questions need not satisfy expected probability identities. [Confidence](https://docs.typesafe.ai/confidence), [known limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13)

Benchmark evidence is narrower than the headline. Workflow evaluations compare four fixed workflows against reference probabilities averaged from larger models, not independently verified ground truth. The vendor discloses possible author bias and a probability-producing LLM adapter that is slower than simple discrete decisions. None establishes repository-agent success or lower harness memory bandwidth. [Evaluation site](https://evals.typesafe.ai/), [launch caveats](https://typesafe.ai/blog/introducing-system-one-models-and-jev)

The more relevant reranking cookbook uses 40 CLERC legal queries and 3,565 passages: a 30-candidate BM25 shortlist already contains the answer for all queries; `jev-1.12` raises top-1 from 5% to 18% and top-10 from 38% to 62%, with 1,200 pairwise calls. This demonstrates a technique, not repository coverage, complete retrieval, or safe aggressive context deletion. [Reranking experiment](https://docs.typesafe.ai/cookbooks/rerank_typesafe)

## Comparison with current Needle3

| xMustard concern | Jev | Needle3 |
|---|---|---|
| Local deployment | No public local artifact/runtime verified. | Public Apache-2.0 model, native/WASM deployment metadata. |
| Actual size evidence | Parameters, quantization, and RSS undisclosed in inspected sources. | Config: 121,021,910 parameters; mostly 2-bit weights with 4-bit components. Published artifact is 35.3 MB. **Artifact size is not process RSS.** |
| Question/context boundary | Typed decisions; dual 64K/32K request limits. | Tool-calling architecture; 8,192 maximum positions, 1,024 sliding window, separate `kv_window: 256` setting. |
| Plausible helper role | Cloud shortlist scoring/routing, if separately authorized. | Optional local routing/typed extraction experiment, subject to actual memory and accuracy checks. |
| Whole-harness 50–100 MB proof | None; remote compute is not evidence of a local tiny model. | None yet; model mappings, runtime, buffers, cache, index, and concurrency must all be counted. |

Needle facts are from its current [configuration](https://huggingface.co/Cactus-Compute/needle3/blob/main/config.json) and [artifact metadata](https://huggingface.co/Cactus-Compute/needle3/blob/main/needle3.cact), not older 26M-model claims. Full Needle/runtime caveats and other candidates belong in the [combined tool/context research](TOOL_CONTEXT_RESEARCH_2026-09-24.md).

## Recommended boundary and acceptance test

This is a proposed experiment, not shipped capability:

1. Keep exact incremental indexing, scope/freshness checks, byte limits, deduplication, and context assembly in ordinary code. Retrieve bounded source IDs/spans first.
2. Let an optional helper return only scores or allowed candidate IDs. Preserve source hashes, paths, line ranges, contradictory evidence, and access to omitted candidates. Learned selection is lossy context shedding, not verified summarization.
3. Rust validates the result against the supplied candidates and current snapshot. Uncertain, unavailable, malformed, or stale output falls back to the deterministic shortlist. Model confidence cannot promote memory to verified, certify a test, or authorize a merge.
4. Compare the no-model baseline with an optional Needle trial; add Jev only after explicit cloud/data permission. Use labeled repository/session tasks and adversarial examples. Measure evidence recall, false omissions, answer/task success, context bytes/tokens, repeated disk reads, index/cache hit rate, CPU time, p50/p95 latency, and whole-process-tree peak RSS. Measure hardware memory bandwidth where supported; I/O counters are not its substitute.
5. Accept a helper only if context/overhead savings outweigh its own work, correctness does not regress, and the existing harness budget remains intact. Publish workload and concurrency with results. Keep API spend separate from the primary resource target.

**Unproven:** Jev's local-memory plausibility, xMustard quality gains, network latency here, calibration on code/session tasks, and net bandwidth savings. No inference benchmark was run in this research pass.
