#!/bin/bash
# WS-RT6 measurements: one-file `index update --paths` batches on an rt_fixtures.sh
# tracked repository (t1m). Per call: the exit code, wall time, xmustard-core's own peak
# RSS and the tree's peak (Git children included, own_rss.py), and the report fields
# RT6 reads (mode, listing, pass counters, timing). Box only. The edited files get their
# bytes back at the end; the index is rebuilt at the start of every run.
#
# usage: rt6_measure.sh <t1m_repo> <out_dir> <label>
# env:   XMUSTARD_CORE_BIN (release binary of the build being measured)
set -u
R=$1
OUT=$2
LABEL=$3
CORE=${XMUSTARD_CORE_BIN:?}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
ROWS=$OUT/rows.jsonl
INSIDE=a.go
PAST=d0999/f0996.go
DOC=d0000/f0002.md
NEW=zz_rt6_new.go

c() {
  local call=$1
  shift
  local o=$OUT/$LABEL.$call.json
  local m
  m=$(python3 "$HERE/own_rss.py" "$o" 300 "$@")
  python3 - "$LABEL" "$call" "$m" "$o" >> "$ROWS" <<'PY'
import json, sys
label, call, m, path = sys.argv[1:5]
row = {"label": label, "call": call, **json.loads(m or "{}")}
try:
    v = json.load(open(path))
except Exception:
    v = {}
row.update({k: v.get(k) for k in ("mode", "reason", "listing", "pass_counters", "timing")})
counters = v.get("counters") or {}
row["counters"] = {k: counters.get(k) for k in ("files_scanned", "unchanged", "reparsed", "written", "deleted")}
print(json.dumps(row))
PY
}

SAVE=$OUT/saved
mkdir -p "$SAVE"
for f in "$INSIDE" "$PAST" "$DOC"; do
  mkdir -p "$SAVE/$(dirname "$f")"
  cp "$R/$f" "$SAVE/$f"
done
rm -rf "$R/.git/xmustard-cache" "$R/$NEW"
c build "$CORE" index build "$R"
c update-tree "$CORE" index update "$R"
printf '// rt6 edit\n' >> "$R/$INSIDE"
c paths-inside "$CORE" index update "$R" --paths "$INSIDE"
c paths-inside-again "$CORE" index update "$R" --paths "$INSIDE"
printf '// rt6 edit\n' >> "$R/$PAST"
c paths-past "$CORE" index update "$R" --paths "$PAST"
printf 'rt6 edit\n' >> "$R/$DOC"
c paths-doc "$CORE" index update "$R" --paths "$DOC"
printf 'package p\n\nfunc RT6New() int { return 6 }\n' > "$R/$NEW"
c paths-untracked "$CORE" index update "$R" --paths "$NEW"
c update-tree-after "$CORE" index update "$R"
for f in "$INSIDE" "$PAST" "$DOC"; do
  cp "$SAVE/$f" "$R/$f"
done
rm -f "$R/$NEW"
echo "run_done $LABEL"
