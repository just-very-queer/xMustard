#!/usr/bin/env bash
# Database-free diagnostics end-to-end check against the real CORE_ONLY API, the
# xmustard-ops CLI, the stdio MCP shim and the Rust core. Temporary repos/data/port;
# no PostgreSQL, provider or model.
#   scripts/e2e/local-diagnostics.sh [--report out.json] [--keep]
# XMUSTARD_CORE_BIN may point at a prebuilt core; otherwise it is built (release).
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/e2e/local_diagnostics.py "$@"
