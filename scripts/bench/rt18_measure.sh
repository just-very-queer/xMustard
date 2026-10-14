#!/bin/bash
# WS-RT18 box acceptance on the 1M-path fixture (rt_fixtures.sh t1m80) with one dirty
# tracked file and one untracked file: repo-key, changetrack working-changes and a full
# index build and update, once per source (XMUSTARD_GIT_INDEX_READER=git, then auto).
# Each call runs under gitspy (gitspy.c): xmustard-core's exact wait4 peak, and every
# Git child it starts with its argv and exact wait4 peak. A Git child is classified
# from its argv here, not by the code under test (critique C9). Box only.
#
# usage: rt18_measure.sh <out_dir> [fixture_base]
# env:   XMUSTARD_CORE_BIN (release binary of the build being measured)
# Exit 1 when, under auto, a call fails, a Git child reads the repository's index, a
# Git child is past GIT_LINE_KB, the core is past CORE_LINE_KB, the dirty or the
# untracked file is missing from repo-key's counts or from working-changes, or the
# identity key differs from Git's.
set -u
OUT=$1
B=${2:-$OUT/fx}
CORE=${XMUSTARD_CORE_BIN:?}
HERE=$(cd "$(dirname "$0")" && pwd)
# the lines: 8 MiB for a Git child (critique C3); the core's line as stated in the
# WS-RT18 record
GIT_LINE_KB=8192
CORE_LINE_KB=25600
mkdir -p "$OUT/bin"
cc -O2 -o "$OUT/gitspy" "$HERE/gitspy.c" || exit 2
ln -sf ../gitspy "$OUT/bin/git"
REAL_GIT=$(command -v git)
R=$B/t1m80
if [ ! -d "$R/.git" ]; then
  bash "$HERE/rt_fixtures.sh" "$B" t1m80 >/dev/null || exit 2
fi
DIRTY=src/services/team-platform/component-runtime/internal/pkg0500/handler_f0500.go
NEW=src/services/team-platform/component-runtime/internal/pkg0500/zz_rt18_new.go
cp "$R/$DIRTY" "$OUT/saved.go"
restore() {
  cp "$OUT/saved.go" "$R/$DIRTY"
  rm -f "$R/$NEW"
}
trap restore EXIT
printf '// rt18 edit\n' >> "$R/$DIRTY"
printf 'package p\n\nfunc RT18New() int { return 18 }\n' > "$R/$NEW"
DATA=$OUT/data
ROWS=$OUT/rows.jsonl
: > "$ROWS"
uptime

# c <source> <call> <args...>: one core call under gitspy, one row in rows.jsonl
c() {
  local src=$1 call=$2
  shift 2
  local o=$OUT/$src.$call.json log=$OUT/$src.$call.git.tsv
  : > "$log"
  local m
  m=$(PATH="$OUT/bin:$PATH" GITSPY_REAL="$REAL_GIT" GITSPY_LOG="$log" \
    XMUSTARD_GIT_INDEX_READER="$src" "$OUT/gitspy" run "$o" 300 "$CORE" "$@")
  python3 - "$src" "$call" "$m" "$o" "$log" "$DIRTY" "$NEW" >> "$ROWS" <<'PY'
import json, sys
src, call, m, out, log, dirty, new = sys.argv[1:8]
OBJECT_ONLY = {"rev-parse", "cat-file", "config", "remote", "symbolic-ref", "rev-list",
               "ls-tree", "log", "show", "var", "version"}
def sub(words):
    it = iter(words)
    for w in it:
        if w in ("-C", "-c"):
            next(it, None)
        elif not w.startswith("-"):
            return w
    return ""
children = []
for line in open(log):
    kb, code, private, args = line.rstrip("\n").split("\t", 3)
    s = sub(args.split(" "))
    loads = not (s in OBJECT_ONLY or (s == "update-index" and private == "1"))
    children.append({"kb": int(kb), "rc": int(code), "sub": s, "private": private == "1",
                     "loads_index": loads})
row = {"source": src, "call": call, **json.loads(m or "{}")}
row["git_children"] = len(children)
row["git_max_kb"] = max((c["kb"] for c in children), default=0)
row["index_loading"] = sorted({c["sub"] for c in children if c["loads_index"]})
row["index_loading_max_kb"] = max((c["kb"] for c in children if c["loads_index"]), default=0)
row["subcommands"] = sorted({c["sub"] + ("(private)" if c["private"] else "") for c in children})
try:
    v = json.load(open(out))
except Exception:
    v = {}
if isinstance(v, dict):
    for k in ("key", "identity_complete", "dirty_entries", "untracked_entries", "pass_counters"):
        if k in v:
            row[k] = v[k]
    row["limitations"] = sorted({l.get("reason") for l in v.get("limitations", [])})
    changed = v.get("changed_files")
    if isinstance(changed, list):
        paths = {f.get("path") for f in changed}
        row["changed_files"] = len(paths)
        row["reports_dirty_and_new"] = dirty in paths and new in paths
print(json.dumps(row))
PY
}

for src in git auto; do
  rm -rf "$R/.git/xmustard-cache" "$DATA"
  mkdir -p "$DATA"
  c "$src" repo-key repo-key "$R"
  c "$src" working-changes changetrack working-changes "$DATA" "$R" ws
  c "$src" index-build index build "$R"
  c "$src" index-update index update "$R"
done
uptime

python3 - "$ROWS" "$GIT_LINE_KB" "$CORE_LINE_KB" "$DIRTY" "$NEW" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
git_line, core_line, dirty, new = int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5]
fail = []
for r in rows:
    print("row", r["source"], r["call"], "rc", r.get("rc"), "wall_s", r.get("wall_s"),
          "core_maxrss_kb", r.get("maxrss_kb"), "git_children", r["git_children"],
          "git_max_kb", r["git_max_kb"], "index_loading", ",".join(r["index_loading"]) or "none",
          "subcommands", ",".join(r["subcommands"]) or "none",
          "limitations", ",".join(r.get("limitations") or []) or "none")
    if r["source"] != "auto":
        continue
    if r.get("rc") != 0:
        fail.append(f"{r['call']}: rc {r.get('rc')}")
    if r["index_loading"]:
        fail.append(f"{r['call']}: index-loading Git children {r['index_loading']}")
    if r["git_max_kb"] > git_line:
        fail.append(f"{r['call']}: a Git child at {r['git_max_kb']} kB, past {git_line}")
    if (r.get("maxrss_kb") or 0) > core_line:
        fail.append(f"{r['call']}: core at {r.get('maxrss_kb')} kB, past {core_line}")
key = {r["source"]: r.get("key") for r in rows if r["call"] == "repo-key"}
print("identity_key_git", key.get("git"))
print("identity_key_auto", key.get("auto"))
if not key.get("auto") or key.get("auto") != key.get("git"):
    fail.append("repo-key: the reader's key differs from Git's")
for r in rows:
    if r["source"] == "auto" and r["call"] == "repo-key":
        if (r.get("dirty_entries") or 0) < 1 or (r.get("untracked_entries") or 0) < 1:
            fail.append("repo-key: the dirty or the untracked file is not counted")
    if r["source"] == "auto" and r["call"] == "working-changes":
        if not r.get("reports_dirty_and_new"):
            fail.append(f"working-changes: {dirty} or {new} not reported")
for f in fail:
    print("acceptance_fail", f)
print("acceptance", "FAIL" if fail else "PASS")
sys.exit(1 if fail else 0)
PY
