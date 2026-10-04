#!/bin/bash
# WS-RT1 baseline: wall time, peak RSS and the core's pass counters per call on the
# rt_fixtures.sh repositories, plus the API's core_passes growth per API call. Box only.
#
# usage: rt_baseline.sh <fixture_base> <out_dir> <case>...
# env:   XMUSTARD_CORE_BIN and XM_API_BIN (release binaries built from this checkout)
# out:   <out_dir>/rows.jsonl, one object per call: case, call, rc, wall_s, max_rss_kb,
#        pass_counters (null when the result carried none), plus the raw outputs.
set -u
B=$1
OUT=$2
shift 2
CORE=${XMUSTARD_CORE_BIN:?}
API=${XM_API_BIN:?}
TO=300
mkdir -p "$OUT"
ROWS=$OUT/rows.jsonl

# row <case> <call> <rc> <json file> <time file>
row() {
  python3 - "$@" >> "$ROWS" <<'PY'
import json, sys
case, call, rc, out, tfile = sys.argv[1:6]
fields = {}
for line in open(tfile, errors="replace"):
    k, _, v = line.strip().rpartition(": ")
    fields[k] = v
wall = fields.get("Elapsed (wall clock) time (h:mm:ss or m:ss)", "")
secs = 0.0
for part in wall.split(":"):
    secs = secs * 60 + float(part or 0)
pc = None
try:
    doc = json.load(open(out))
    if isinstance(doc, dict):
        pc = doc.get("pass_counters")
except Exception:
    pass
print(json.dumps({"case": case, "call": call, "rc": int(rc), "wall_s": round(secs, 2),
                  "max_rss_kb": int(fields.get("Maximum resident set size (kbytes)", 0) or 0),
                  "pass_counters": pc}))
PY
}

# t <case> <call> <cmd...>: one core call under /usr/bin/time -v with a hard timeout
t() {
  local c=$1 call=$2
  shift 2
  local o=$OUT/$c.$call
  /usr/bin/time -v -o "$o.time" timeout "$TO" "$@" > "$o.json" 2> "$o.err"
  row "$c" "$call" "$?" "$o.json" "$o.time"
}

health_passes() {
  curl -s --max-time 30 "http://127.0.0.1:$PORT/api/health" |
    python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["budget"]["counters"]["core_passes"]))'
}

# h <case> <call> <method> <path> [body]: one API call; the row's pass_counters is the
# growth of /api/health core_passes across it (every core call the request made)
h() {
  local c=$1 call=$2 m=$3 p=$4 body=${5:-}
  local o=$OUT/$c.api-$call
  local before after
  before=$(health_passes)
  local args=(-s -o "$o.json" -w '%{http_code} %{time_total}' -X "$m" --max-time "$TO")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data "$body")
  local r
  r=$(curl "${args[@]}" "http://127.0.0.1:$PORT$p")
  after=$(health_passes)
  python3 - "$c" "api-$call" "$r" "$before" "$after" >> "$ROWS" <<'PY'
import json, sys
case, call, r, before, after = sys.argv[1:6]
code, wall = (r.split() + ["0", "0"])[:2]
try:
    b, a = json.loads(before), json.loads(after)
    pc = {k: a[k] - b[k] for k in a}
except Exception:
    pc = None
print(json.dumps({"case": case, "call": call, "rc": int(code), "wall_s": round(float(wall), 2),
                  "max_rss_kb": None, "pass_counters": pc}))
PY
}

for c in "$@"; do
  R=$B/$c
  [ -d "$R" ] || { echo "missing fixture $c" >&2; continue; }
  WS=ws-$c
  DD=$B/.data-$c
  rm -rf "$DD" "$R/.git/xmustard-cache"
  mkdir -p "$DD"
  t "$c" index-build "$CORE" index build "$R"
  t "$c" index-update "$CORE" index update "$R"
  t "$c" index-update-paths "$CORE" index update "$R" --paths a.go
  t "$c" index-stats "$CORE" index stats "$R"
  t "$c" repo-key1 "$CORE" repo-key "$R"
  t "$c" repo-key2 "$CORE" repo-key "$R"
  t "$c" ct-index "$CORE" changetrack index "$DD" "$R" "$WS" --reason=registration
  t "$c" ct-working "$CORE" changetrack working-changes "$DD" "$R" "$WS"
  t "$c" ct-drift "$CORE" changetrack drift "$DD" "$R" "$WS"
  t "$c" sg-build "$CORE" symbolgraph build "$R" "$WS"
  t "$c" search "$CORE" search "$R" "$WS" Alpha 10
  t "$c" impact "$CORE" index impact "$R" --symbol Alpha
  t "$c" repo-map "$CORE" build-repo-map "$WS" "$R"
  PORT=$((18100 + RANDOM % 800))
  AD=$B/.api-$c
  rm -rf "$AD"
  mkdir -p "$AD"
  XMUSTARD_DATA_DIR=$AD XMUSTARD_API_PORT=$PORT XMUSTARD_AUTH=off XMUSTARD_REGISTER_ROOTS=$B \
    "$API" > "$OUT/$c.api.log" 2>&1 &
  APID=$!
  for _ in $(seq 1 100); do curl -s -o /dev/null "http://127.0.0.1:$PORT/api/health" && break; sleep 0.2; done
  h "$c" load POST /api/workspaces/load "{\"root_path\":\"$R\",\"auto_scan\":true,\"prefer_cached_snapshot\":true}"
  WID=$(python3 - "$OUT/$c.api-load.json" <<'PY'
import json, sys
def find(o):
    if isinstance(o, dict):
        if isinstance(o.get("workspace_id"), str):
            return o["workspace_id"]
        o = list(o.values())
    if isinstance(o, list):
        for v in o:
            r = find(v)
            if r:
                return r
    return ""
try:
    print(find(json.load(open(sys.argv[1]))))
except Exception:
    print("")
PY
)
  if [ -n "$WID" ]; then
    h "$c" ground1 GET "/api/workspaces/$WID/session-grounding?max_chars=6000"
    h "$c" ground2 GET "/api/workspaces/$WID/session-grounding?max_chars=6000"
    h "$c" search GET "/api/workspaces/$WID/search?q=Alpha&limit=10"
  fi
  kill "$APID" 2>/dev/null
  wait "$APID" 2>/dev/null
  echo "case_done $c"
done

# the table the baseline record quotes, computed from rows.jsonl
python3 - "$ROWS" <<'PY'
import json, sys
names = ["files_listed", "files_statted", "files_hashed", "bytes_hashed", "git_spawns", "rows_loaded"]
print("| case | call | rc | wall s | max RSS KiB | " + " | ".join(names) + " |")
print("|" + "---|" * (5 + len(names)))
for line in open(sys.argv[1]):
    r = json.loads(line)
    pc = r["pass_counters"] or {}
    cells = [str(pc.get(n, "-")) for n in names]
    print(f"| {r['case']} | {r['call']} | {r['rc']} | {r['wall_s']} | {r['max_rss_kb'] or '-'} | " + " | ".join(cells) + " |")
PY
