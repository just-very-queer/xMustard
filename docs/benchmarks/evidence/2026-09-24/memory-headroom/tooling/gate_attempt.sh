#!/usr/bin/env bash
# One preregistered RSS gate attempt with an out-of-band sidecar (plan 2026-09-24-memory-headroom, Phase 1.1).
# Usage: gate_attempt.sh <label> <tree> <outdir>
# Never edits benchmark files; runs <tree>/scripts/bench/rss.sh under a sanitized Go/XMUSTARD env.
set -uo pipefail
label=$1; tree=$(cd "$2" && pwd -P); out=$(mkdir -p "$3" && cd "$3" && pwd -P)
rep="$out/$label.json"; side="$out/$label.sidecar.txt"; log="$out/$label.log"
[ -e "$rep" ] && { echo "refusing: $rep exists (no reruns)"; exit 2; }
vm_stat > "$out/$label.vm_stat.before.txt" 2>&1; sysctl vm.swapusage > "$out/$label.swap.before.txt" 2>&1
cmd=(env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS -u GOMAXPROCS -u XMUSTARD_CORE_BIN bash "$tree/scripts/bench/rss.sh" --report "$rep")
{
  echo "label: $label"; echo "tree: $tree"; echo "start_utc: $(date -u +%FT%TZ)"
  echo "host: $(hostname -s) $(sysctl -n machdep.cpu.brand_string) memsize=$(sysctl -n hw.memsize) $(sw_vers -productVersion) $(uname -r)"
  echo "uptime: $(uptime)"
  echo "launch_command: ${cmd[*]}"
  echo "--- ambient env names matching ^(GOGC|GOMEMLIMIT|GODEBUG|GOFLAGS|GOMAXPROCS|XMUSTARD_) (must be empty):"
  env | cut -d= -f1 | grep -E '^(GOGC|GOMEMLIMIT|GODEBUG|GOFLAGS|GOMAXPROCS|XMUSTARD_)' || true
  echo "--- sanitized-launch env names, same pattern (must be empty):"
  env -u GOGC -u GOMEMLIMIT -u GODEBUG -u GOFLAGS -u GOMAXPROCS -u XMUSTARD_CORE_BIN env | cut -d= -f1 | grep -E '^(GOGC|GOMEMLIMIT|GODEBUG|GOFLAGS|GOMAXPROCS|XMUSTARD_)' || true
  echo "--- env names ^(RUSTFLAGS|CARGO_) (must be empty):"
  env | cut -d= -f1 | grep -E '^(RUSTFLAGS|CARGO_)' || true
  echo "--- .cargo/config* in tree: $(ls "$tree"/.cargo "$tree"/rust-core/.cargo 2>/dev/null | tr '\n' ' ')(must be empty)"
  echo "--- go env GOFLAGS GODEBUG (must be two empty lines):"; (cd "$tree/api-go" && go env GOFLAGS GODEBUG)
  echo "--- rg -n 'SetGCPercent|SetMemoryLimit|//go:debug' api-go (no matches expected):"
  (cd "$tree" && rg -n 'SetGCPercent|SetMemoryLimit|//go:debug' api-go; echo "rg_exit=$?")
  echo "--- pinned script sha256:"; (cd "$tree" && shasum -a 256 scripts/bench/rss_bench.py scripts/e2e/harness.py scripts/bench/rss.sh)
  echo "--- tree head: $(git -C "$tree" rev-parse HEAD)"
} > "$side" 2>&1
t0=$(date +%s)
"${cmd[@]}" > "$log" 2>&1; rc=$?
{
  echo "--- exit_code: $rc  wall_s: $(( $(date +%s) - t0 ))  end_utc: $(date -u +%FT%TZ)"
  if [ -f "$rep" ]; then echo "report_sha256: $(shasum -a 256 "$rep" | cut -d' ' -f1)"; else echo "report_sha256: MISSING"; fi
  echo "log_sha256: $(shasum -a 256 "$log" | cut -d' ' -f1)"
} >> "$side"
vm_stat > "$out/$label.vm_stat.after.txt" 2>&1; sysctl vm.swapusage > "$out/$label.swap.after.txt" 2>&1
echo "$label rc=$rc"; exit 0
