#!/usr/bin/env bash
# Peak RSS of `xmustard-core index build` (full, cold: --no-cache) and of a one-file
# incremental update, on a Git fixture. Each run is its own process under `time`.
#   usage: index-rss.sh <xmustard-core> <repo> <work-dir> [runs]
# The fixture is cloned into <work-dir> so the edit never touches the source repo, and
# the index lives in <work-dir>/idx (never inside the fixture's .git).
set -euo pipefail
BIN=$1; SRC=$2; WORK=$3; RUNS=${4:-3}
mkdir -p "$WORK"
LABEL=$(basename "$SRC")
CLONE="$WORK/$LABEL-clone"
rm -rf "$CLONE" "$WORK/idx-$LABEL"
git clone -q --local --no-hardlinks "$SRC" "$CLONE"
peak() { # <label> <args...>: prints one JSON line
  local name=$1; shift
  local tf="$WORK/$LABEL.$name.time" out="$WORK/$LABEL.$name.json"
  if [[ "$(uname)" == "Darwin" ]]; then
    /usr/bin/time -l "$BIN" "$@" >"$out" 2>"$tf"
    rss=$(awk '/maximum resident set size/ {print $1}' "$tf")
    fp=$(awk '/peak memory footprint/ {print $1}' "$tf")
  else
    /usr/bin/time -v "$BIN" "$@" >"$out" 2>"$tf"
    rss=$(( $(awk -F: '/Maximum resident set size/ {gsub(/ /,"",$2); print $2}' "$tf") * 1024 ))
    fp=null
  fi
  python3 - "$LABEL" "$name" "$rss" "${fp:-null}" "$out" <<'PY'
import json, sys
label, name, rss, fp, out = sys.argv[1:]
r = json.load(open(out))
c = r["coverage"]
print(json.dumps({
    "fixture": label, "run": name, "maxrss_bytes": int(rss),
    "peak_footprint_bytes": None if fp == "null" else int(fp),
    "mode": r["mode"], "reason": r["reason"], "elapsed_ms": r["timing"]["elapsed_ms"],
    "eligible": c["eligible_files"], "indexed": c["indexed_files"],
    "symbols": c["symbols"], "references": c["references"], "chunks": c["chunks"],
    "edges": c["edges"], "loss_counts": c["loss_counts"], "counters": r["counters"],
}))
PY
}
for i in $(seq 1 "$RUNS"); do
  peak "build$i" index build "$CLONE" --index-dir "$WORK/idx-$LABEL" --no-cache
done
# one-file edit: append a function to the first tracked TypeScript file
f=$(git -C "$CLONE" ls-files -- '*.ts' | awk 'NR == 1')
printf '\nexport function benchInserted%s() { return 1; }\n' "$RANDOM" >>"$CLONE/$f"
peak update index update "$CLONE" --index-dir "$WORK/idx-$LABEL"
peak noop index update "$CLONE" --index-dir "$WORK/idx-$LABEL"
