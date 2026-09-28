#!/bin/bash
# ground's change-tracking cost on an unchanged and a slightly dirty tree, base core vs
# head core: `changetrack drift` (ground's drift section) and `working-changes` (its
# changed files), one-shot, on a private clone of the workspace.
# usage: ground-cost.sh <base-core> <head-core> <workspace>
set -u
B=$1; H=$2; W=$3
T=$(mktemp -d); git clone -q --no-hardlinks "$W" "$T/repo"; R="$T/repo"
git -C "$R" config user.email t@t; git -C "$R" config user.name t
G=$(git -C "$R" rev-parse --absolute-git-dir)
# a fresh clone's index is racily clean, and --no-optional-locks (every xMustard Git
# call) cannot refresh it: settle it once, as the user's own Git commands would
sleep 2; git -C "$R" update-index -q --refresh
t0=$(date +%s%N); git -C "$R" --no-optional-locks status --porcelain=v1 -z --untracked-files=all >/dev/null; t1=$(date +%s%N)
echo "git status alone: $(( (t1 - t0) / 1000000 )) ms"
echo "tracked files $(git -C "$R" ls-files | wc -l), bytes $(git -C "$R" ls-files -z | (cd "$R" && xargs -0 stat -c %s) | awk '{s+=$1} END {print s}')"
summ() { python3 -c '
import json,sys
d=json.load(sys.stdin)
dc=d.get("drift_checked")
print(json.dumps(dc, separators=(",",":")) if dc else "-", "stale=%s" % d.get("stale"), "changed_files_total=%s" % d.get("changed_files_total"))'; }
run() {
  local label=$1 core=$2; shift 2
  local t0 t1 out
  t0=$(date +%s%N); out=$("$core" "$@"); t1=$(date +%s%N)
  printf "%-28s %7.1f ms  %s\n" "$label" "$(( (t1 - t0) / 100000 ))e-1" "$(printf %s "$out" | summ)"
}
side() {
  local name=$1 core=$2 D
  D=$(mktemp -d)
  "$core" changetrack index "$D" "$R" ws >/dev/null
  rm -rf "$G/xmustard-cache/filehash-v1"
  run "$name drift, no stat cache" "$core" changetrack drift "$D" "$R" ws
  for i in 1 2 3 4 5; do run "$name drift" "$core" changetrack drift "$D" "$R" ws; done
  for i in 1 2 3; do run "$name working-changes" "$core" changetrack working-changes "$D" "$R" ws; done
  echo "$D"
}
DB=$(side base "$B" | tee /dev/stderr | tail -1)
DH=$(side head "$H" | tee /dev/stderr | tail -1)
# a slightly dirty tree: three edited tracked files and one untracked file
for f in $(git -C "$R" ls-files '*.ts' | head -3); do echo "// edited" >> "$R/$f"; done
echo "untracked" > "$R/notes-untracked.txt"
sleep 3  # out of the racy window, so the second pass may trust its stat entries
for i in 1 2 3; do run "base drift (dirty)" "$B" changetrack drift "$DB" "$R" ws; done
for i in 1 2 3; do run "head drift (dirty)" "$H" changetrack drift "$DH" "$R" ws; done
rm -rf "$T" "$DB" "$DH"
