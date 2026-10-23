#!/usr/bin/env bash
# Budget gate v2 (PAR-EVAL-05): summed PSS of the xMustard-owned tree <= 95.4 MiB
# (summed ps-RSS where the platform has no PSS; owner decision G5.10, 2026-10-23), with
# summed ps-RSS, footprint, anon/file split, component attribution and external lines
# reported beside it. Writes <out>/report.json and <out>/report.md. No provider or model calls.
#   scripts/bench/rss_v2.sh run --suite ci|parity [--out DIR] [--repeat N]
#   scripts/bench/rss_v2.sh run --scenarios v1-workload --workstream WS-NN --baseline base/report.json
#   scripts/bench/rss_v2.sh fixtures            # clone + verify the pinned parity fixtures
#   scripts/bench/rss_v2.sh ledger              # reconcile the budget ledger (exit 1 while overcommitted)
#   scripts/bench/rss_v2.sh ledger --diff BASE_LEDGER   # governance-relevant ledger changes (exit 1 if any)
#   scripts/bench/rss_v2.sh ci-plan --branch REF        # workstream, scenarios and fixture need for CI
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/bench/rss_bench_v2.py "$@"
