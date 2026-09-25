#!/usr/bin/env bash
# Budget gate v2 (PAR-EVAL-05): ps-RSS of the xMustard-owned tree <= 95.4 MiB, with
# footprint/PSS, anon/file split, component attribution and external lines reported
# beside it. Writes <out>/report.json and <out>/report.md. No provider or model calls.
#   scripts/bench/rss_v2.sh run --suite ci|parity [--out DIR] [--repeat N]
#   scripts/bench/rss_v2.sh run --scenarios v1-workload --workstream WS-NN --baseline base/report.json
#   scripts/bench/rss_v2.sh fixtures            # clone + verify the pinned parity fixtures
#   scripts/bench/rss_v2.sh ledger              # reconcile the budget ledger
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/bench/rss_bench_v2.py "$@"
