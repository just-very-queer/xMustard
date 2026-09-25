#!/usr/bin/env bash
# HTTP + stdio-MCP evidence end-to-end check against the real API, shim and Rust core.
# Temporary repo/data/port; required auth; no provider or model calls.
#   scripts/e2e/mcp-evidence.sh [--report out.json] [--keep]
# XMUSTARD_CORE_BIN may point at a prebuilt core; otherwise it is built (release).
set -euo pipefail
cd "$(dirname "$0")/../.."
exec python3 scripts/e2e/mcp_evidence.py "$@"
