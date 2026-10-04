#!/bin/bash
# WS-RT3 measurements on rt_fixtures.sh repositories: per call the exit code, wall time,
# xmustard-core's own peak RSS and the tree's peak (Git children included, own_rss.py),
# the result fields RT3 changed, and API registration on the same tree. Box only.
#
# usage: rt3_measure.sh <fixture_base> <out_dir> <label> <case>...
# env:   XMUSTARD_CORE_BIN, XM_API_BIN (release binaries of the build being measured)
set -u
B=$1
OUT=$2
LABEL=$3
shift 3
CORE=${XMUSTARD_CORE_BIN:?}
API=${XM_API_BIN:?}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$OUT"
ROWS=$OUT/rows.jsonl

# c <case> <call> <cmd...>: one core call; the row adds the fields RT3 reads
c() {
  local cs=$1 call=$2
  shift 2
  local o=$OUT/$LABEL.$cs.$call.json
  local m
  m=$(timeout 300 python3 "$HERE/own_rss.py" "$o" "$@")
  python3 - "$LABEL" "$cs" "$call" "$m" "$o" >> "$ROWS" <<'PY'
import json, sys
label, case, call, m, path = sys.argv[1:6]
row = {"label": label, "case": case, "call": call, **json.loads(m or "{}")}
try:
    v = json.load(open(path))
except Exception:
    v = None
if isinstance(v, dict):
    cov = v.get("coverage") or {}
    row.update({
        "identity_complete": v.get("identity_complete"),
        "limitations": sorted({l.get("reason") for l in v.get("limitations", [])}),
        "untracked_entries": v.get("untracked_entries"),
        "files_hashed": v.get("files_hashed"),
        "repo_mode": cov.get("repo_mode"),
        "loss_causes": sorted(k for k in (cov.get("loss_counts") or {}) if k.startswith("git_")),
        "total": v.get("total"),
        "files_read": v.get("files_read"),
        "truncated": v.get("truncated"),
        "pass_counters": v.get("pass_counters"),
    })
elif isinstance(v, list):
    row["signals"] = len(v)
print(json.dumps(row))
PY
}

for cs in "$@"; do
  R=$B/$cs
  [ -d "$R" ] || { echo "missing fixture $cs" >&2; continue; }
  rm -rf "$R/.git/xmustard-cache"
  c "$cs" repo-key1 "$CORE" repo-key "$R"
  c "$cs" repo-key2 "$CORE" repo-key "$R"
  c "$cs" sg-build "$CORE" symbolgraph build "$R" ws
  c "$cs" scan-signals "$CORE" scan-signals "$R"
  PORT=$((18900 + RANDOM % 800))
  AD=$B/.api-$LABEL-$cs
  rm -rf "$AD"
  mkdir -p "$AD"
  XMUSTARD_DATA_DIR=$AD XMUSTARD_API_PORT=$PORT XMUSTARD_AUTH=off XMUSTARD_REGISTER_ROOTS=$B \
    XMUSTARD_CORE_BIN=$CORE "$API" > "$OUT/$LABEL.$cs.api.log" 2>&1 &
  APID=$!
  for _ in $(seq 1 100); do curl -s -o /dev/null "http://127.0.0.1:$PORT/api/health" && break; sleep 0.2; done
  r=$(curl -s -o "$OUT/$LABEL.$cs.api-load.json" -w '%{http_code} %{time_total}' -X POST --max-time 300 \
    -H 'Content-Type: application/json' \
    --data "{\"root_path\":\"$R\",\"auto_scan\":true,\"prefer_cached_snapshot\":true}" \
    "http://127.0.0.1:$PORT/api/workspaces/load")
  python3 - "$LABEL" "$cs" "$r" "$OUT/$LABEL.$cs.api-load.json" >> "$ROWS" <<'PY'
import json, sys
label, case, r, path = sys.argv[1:5]
code, wall = (r.split() + ["0", "0"])[:2]
summary = {}
try:
    doc = json.load(open(path))
    summary = {k: v for k, v in (doc.get("summary") or {}).items() if k.startswith(("signals", "repo_map"))}
except Exception:
    pass
print(json.dumps({"label": label, "case": case, "call": "api-load", "http": int(code),
                  "wall_s": round(float(wall), 2), "summary": summary}))
PY
  kill "$APID" 2>/dev/null
  wait "$APID" 2>/dev/null
  rm -rf "$AD"
  echo "case_done $LABEL $cs"
done
