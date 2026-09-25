#!/usr/bin/env bash
# Sampled xMustard process-tree RSS for the plan's fixed workload (see rss_bench.py).
# Gate: sampled tree peak <= 100 MB. Reports child high-water separately; hardware
# memory bandwidth is not measured. No provider or model calls.
#   scripts/bench/rss.sh [--report out.json] [--keep]
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/bench/rss_bench.py "$@"
