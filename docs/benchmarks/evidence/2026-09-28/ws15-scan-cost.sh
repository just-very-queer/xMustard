#!/usr/bin/env bash
# WS-15: per-batch cost of the Git listing inside `index update --paths` (one edited
# file) as the tracked-file count grows. Usage: scan-cost.sh <core-bin> <n>...
set -eu
CORE=$1; shift
for N in "$@"; do
  D=$(mktemp -d)
  python3 - "$D" "$N" <<'PY'
import os, sys
d, n = sys.argv[1], int(sys.argv[2])
for i in range(n):
    sub = os.path.join(d, f"mod{i % 100:03d}")
    os.makedirs(sub, exist_ok=True)
    with open(os.path.join(sub, f"f{i}.ts"), "w") as f:
        f.write(f"export function fn{i}(v: number): number {{\n  return v + {i};\n}}\n")
PY
  git -C "$D" init -q && git -C "$D" add -A && git -C "$D" -c user.email=t@t -c user.name=t commit -qm c
  "$CORE" index build "$D" >/dev/null
  scans=(); totals=()
  for k in 1 2 3 4 5; do
    printf 'export function fn3(v: number): number {\n  return v * %d;\n}\n' "$k" > "$D/mod003/f3.ts"
    out=$("$CORE" index update "$D" --paths mod003/f3.ts)
    scans+=($(printf %s "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["timing"]["scan_ms"])'))
    totals+=($(printf %s "$out" | python3 -c 'import json,sys; print(json.load(sys.stdin)["timing"]["elapsed_ms"])'))
  done
  t0=$(date +%s%N); for k in 1 2 3 4 5; do git -C "$D" --no-optional-locks ls-files -s -z >/dev/null; done; t1=$(date +%s%N)
  echo "files=$N scan_ms=${scans[*]} update_elapsed_ms=${totals[*]} git_ls_files_s_ms=$(( (t1 - t0) / 5000000 ))"
  rm -rf "$D"
done
