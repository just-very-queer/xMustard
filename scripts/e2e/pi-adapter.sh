#!/usr/bin/env bash
# End-to-end check of the xMustard Pi adapter (integrations/pi) against a real
# xMustard API + Rust core and the pinned Pi CLI, with an offline scripted model.
#
#   scripts/e2e/pi-adapter.sh            # typecheck + unit + e2e
#   XM_E2E_KEEP=1 scripts/e2e/pi-adapter.sh   # keep the temp repo/data/logs
#   XMUSTARD_CORE_BIN=/path/xmustard-core scripts/e2e/pi-adapter.sh  # reuse a core build
#
# Everything is local and temporary: Pi deps install into integrations/pi/node_modules
# from its lockfile, Go binaries build into a temp dir, the Rust core into
# integrations/pi/.cache (gitignored), and every run uses a temp repo, data dir and
# random loopback port. No provider is contacted and no credential is read.
#
# Native test dependency: PostgreSQL server binaries (initdb, postgres, psql) on PATH.
# They run the PostgreSQL control: a disposable cluster in the temp dir (loopback,
# random port, private socket; deleted afterwards) holds the diagnostics baseline.
# Without them the script fails, unless XM_E2E_ALLOW_NO_POSTGRES=1, which runs the
# database-free path instead: the baseline is imported by the real xmustard-ops CLI
# into local storage and all nine tools must still succeed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PI_DIR="$ROOT/integrations/pi"
PINNED_PI="0.87.1" # published build of earendil-works/pi; see integrations/pi/README.md

for tool in node npm go cargo git; do
	command -v "$tool" >/dev/null || { echo "pi-adapter e2e: missing $tool" >&2; exit 2; }
done
# The tests run .ts files directly: type stripping is on by default from Node 22.18.
node -e 'const [a, b] = process.versions.node.split(".").map(Number); process.exit(a > 22 || (a === 22 && b >= 18) ? 0 : 1)' ||
	{ echo "pi-adapter e2e: Node >= 22.18 required (have $(node --version))" >&2; exit 2; }
PG_BIN=""
if command -v initdb >/dev/null && command -v postgres >/dev/null && command -v psql >/dev/null; then
	PG_BIN="$(dirname "$(command -v postgres)")"
	echo "native Postgres fixture: $("$PG_BIN/postgres" --version)"
elif [[ "${XM_E2E_ALLOW_NO_POSTGRES:-}" == "1" ]]; then
	echo "WARNING: native Postgres not found; the PostgreSQL control will NOT be covered (diagnostics use local storage)" >&2
else
	echo "pi-adapter e2e: native PostgreSQL (initdb, postgres, psql) required for the PostgreSQL control; set XM_E2E_ALLOW_NO_POSTGRES=1 to run the database-free path only" >&2
	exit 2
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/xm-pi-e2e.XXXXXX")"
cleanup() {
	local rc=$?
	if [[ $rc -eq 0 && "${XM_E2E_KEEP:-}" != "1" ]]; then
		rm -rf "$WORK"
	else
		echo "pi-adapter e2e: artifacts kept in $WORK" >&2
	fi
	exit $rc
}
trap cleanup EXIT

echo "== Pi dependencies (project-local, lockfile)"
if [[ ! -x "$PI_DIR/node_modules/.bin/pi" || "$PI_DIR/package-lock.json" -nt "$PI_DIR/node_modules/.package-lock.json" ]]; then
	(cd "$PI_DIR" && npm ci --ignore-scripts --no-audit --no-fund)
fi
have="$(node -p 'require(process.argv[1]).version' "$PI_DIR/node_modules/@earendil-works/pi-coding-agent/package.json")"
[[ "$have" == "$PINNED_PI" ]] || { echo "pi-adapter e2e: pi-coding-agent $have installed, $PINNED_PI pinned" >&2; exit 1; }
echo "pi-coding-agent $have"

echo "== build xmustard-api / xmustard-mcp / xmustard-ops"
(cd "$ROOT/api-go" && go build -o "$WORK/bin/xmustard-api" ./cmd/xmustard-api && go build -o "$WORK/bin/xmustard-mcp" ./cmd/xmustard-mcp && go build -o "$WORK/bin/xmustard-ops" ./cmd/xmustard-ops)

if [[ -z "${XMUSTARD_CORE_BIN:-}" ]]; then
	echo "== build xmustard-core (release)"
	CARGO_TARGET_DIR="$PI_DIR/.cache/cargo-target" cargo build --release --quiet \
		--manifest-path "$ROOT/rust-core/Cargo.toml" --bin xmustard-core
	XMUSTARD_CORE_BIN="$PI_DIR/.cache/cargo-target/release/xmustard-core"
fi
"$XMUSTARD_CORE_BIN" repo-key "$ROOT" >/dev/null ||
	{ echo "pi-adapter e2e: $XMUSTARD_CORE_BIN lacks a working repo-key command" >&2; exit 1; }

cd "$PI_DIR"
echo "== typecheck"
npx --no-install tsc --noEmit -p tsconfig.json
echo "== unit"
node --test test/unit.test.ts
echo "== e2e (real Pi CLI + real API)"
mkdir -p "$WORK/e2e"
XM_API_BIN="$WORK/bin/xmustard-api" XM_MCP_BIN="$WORK/bin/xmustard-mcp" XM_OPS_BIN="$WORK/bin/xmustard-ops" XM_CORE_BIN="$XMUSTARD_CORE_BIN" XM_PG_BIN_DIR="$PG_BIN" \
	XM_E2E_DIR="$WORK/e2e" node --test --test-concurrency=1 --test-timeout=300000 test/e2e/pi-adapter.e2e.ts
echo "== summary"
cat "$WORK/e2e/summary.json"
