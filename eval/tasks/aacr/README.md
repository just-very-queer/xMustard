# AACR-Bench pilot (optional corpus)

WS-76 is folded into WS-63 as an optional corpus. It is **not** part of the WS-50
parity gate, and no run has been made. The question it can answer is narrow: does
xMustard context change a host reviewer's F1 and token use on real pull requests? It is
not a review-quality claim, and the vendor's AACR numbers are not reproduced here.

## Gate 0: licence and manifest (done 2026-09-28)

`pilot.json` records:

- the dataset (`Alibaba-Aone/aacr-bench` on Hugging Face) at revision `47be1d6d`, with
  the SHA-256 and size of `dataset.json` and `README.md` at that revision. The card
  metadata gives the licence as Apache-2.0. The dataset holds 2,145 review comments
  (1,505 expert-verified correct, 640 incorrect) on 200 pull requests from 50
  repositories;
- the pilot selection: 7 pull requests in 7 languages (TypeScript, Go, Python, Rust,
  Java, C++, C) from repositories licensed MIT, Apache-2.0 or BSD-3-Clause (GitHub API
  `spdx_id`, checked on 2026-09-28). In each repository the pick is the pull request with
  the most correct comments among those changing at most 400 lines. Together they carry
  73 correct and 30 incorrect comments over 1,249 changed lines.

The dataset is not vendored. Download it at the pinned revision and check the digests
before use:

```bash
base=https://huggingface.co/datasets/Alibaba-Aone/aacr-bench/resolve/47be1d6df1e7faf222cf531587772d92f79fe6b2
curl -sLO "$base/dataset.json" && shasum -a 256 dataset.json   # must match pilot.json
```

Each repository's own licence governs its checkout. Repositories without an MIT, Apache
or BSD licence (for example AGPL-3.0 or NOASSERTION in the API) stay out unless the owner
decides otherwise.

## Pilot protocol

- **Arms.** A bare host reviewer (`claude -p` or `codex exec` with one fixed prompt), the
  host plus xMustard MCP, and the host plus MCP plus hooks. OCR delegate mode, installed
  separately and never vendored, is the peer comparator. Model and seed are fixed.
- **Checkout.** A fresh detached worktree at `pr_target_commit` (the reviewed revision),
  reviewed against `pr_source_commit`. Before any model run, check that every correct
  comment's `path` and line range fall inside that diff. A pull request that fails the
  check leaves the pilot.
- **Matching.** A deterministic pre-matcher first (same path, overlapping line ranges),
  then a pinned LLM judge in an eval-only lane. The judge runs twice over the same
  findings, and Cohen's kappa between the two passes is reported. Below 0.6, no judged F1
  is reported.
- **Statistics.** Precision, recall and F1 per pull request. Confidence intervals come
  from a bootstrap that resamples pull requests, not comments: the comments of one pull
  request and one model run are not independent, so per-issue McNemar is not used.
- **Tokens.** In, out, cache read and cache write, from each client's final event
  (`claude -p` stream-json, `codex exec --json`), with cost reported separately.
- **Spend cap.** The pilot is capped at USD 50: `max_budget_usd: 2.0` per claude run
  (7 pull requests x 3 arms, at most USD 42), plus at most USD 8 for the two judge
  passes. The cap for any larger run is set only after the pilot, as 1.25 x the pilot's
  measured mean cost per run x the planned number of runs, and recorded before the run
  starts.

## What is missing to run it

`xmustard-eval` has no review task type. A run needs:

- a findings contract: the agent writes findings to a known path in the worktree;
- a scoring step that returns P/R/F1 per pull request instead of an oracle's pass or
  fail;
- the judge lane;
- the pull-request cluster bootstrap in `report.go`.

That work belongs to a later WS-76 build, which the critic kept optional ("could").
Until it exists, this directory is the pinned, licence-checked input for the pilot.
