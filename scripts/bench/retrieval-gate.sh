#!/usr/bin/env bash
# 12-query internal retrieval regression gate over scripts/bench/gold (cold, warm,
# after a one-file edit). No provider or model calls.
#   scripts/bench/retrieval-gate.sh [--report out.json] [--keep]
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/bench/retrieval_gate.py "$@"
