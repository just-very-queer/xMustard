#!/bin/bash
# WS-RT1 fixture generator: scale and nested-repository repositories for the streaming
# runtime baseline (scripts/bench/rt_baseline.sh). Run it on the Linux build box only;
# the 1M set takes about 5 GB of disk. Nothing it writes is committed.
#
# usage: rt_fixtures.sh <base_dir> <set>...
#   nested  c0-control n1-nested n2-deep n3-ignored n4-bare n5-gitfile g1-gitlink
#           s1-sub-dirty s2-sub-uninit s3-sub-ignall
#   t100k   t100k: 100,000 tracked paths
#   t1m     t1m: 1,000,000 tracked paths
#   u200k   u200k: 3 tracked files plus 200,000 untracked .go files
set -eu
B=$1
shift
mkdir -p "$B"
export GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@x GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@x
export GIT_CONFIG_NOSYSTEM=1
g() { git -c init.defaultBranch=main -c protocol.file.allow=always -c advice.addEmbeddedRepo=false "$@"; }

# base_ws <dir>: a workspace repo where b.go calls Alpha and c.go calls Beta
base_ws() {
  local d=$1
  rm -rf "$d"
  mkdir -p "$d"
  g init -q "$d"
  printf 'package p\n\n// Alpha is the seed.\nfunc Alpha() int { return 1 }\n' > "$d/a.go"
  printf 'package p\n\nfunc Beta() int { return Alpha() + 1 }\n' > "$d/b.go"
  printf 'package p\n\nfunc Gamma() int { return Beta() }\n' > "$d/c.go"
  g -C "$d" add -A
  g -C "$d" commit -qm init
}

# sub_repo <dir>: a standalone repo defining SubOnly
sub_repo() {
  local d=$1
  mkdir -p "$d"
  g init -q "$d"
  printf 'package sub\n\nfunc SubOnly() int { return 7 }\n' > "$d/x.go"
  printf 'package sub\n\nfunc SubCaller() int { return SubOnly() }\n' > "$d/y.go"
  g -C "$d" add -A
  g -C "$d" commit -qm sub
}

nested() {
  base_ws "$B/c0-control"
  base_ws "$B/n1-nested"
  sub_repo "$B/n1-nested/sub"
  base_ws "$B/n2-deep"
  mkdir -p "$B/n2-deep/a"
  printf 'package a\n\nfunc Keep() int { return Alpha() }\n' > "$B/n2-deep/a/keep.go"
  g -C "$B/n2-deep" add -A
  g -C "$B/n2-deep" commit -qm a
  sub_repo "$B/n2-deep/a/b/sub"
  sub_repo "$B/n2-deep/a/b/sub/inner"
  base_ws "$B/n3-ignored"
  printf 'vendor/\n' > "$B/n3-ignored/.gitignore"
  g -C "$B/n3-ignored" add -A
  g -C "$B/n3-ignored" commit -qm ign
  sub_repo "$B/n3-ignored/vendor/sub"
  base_ws "$B/n4-bare"
  g init -q --bare "$B/n4-bare/bare.git"
  base_ws "$B/n5-gitfile"
  rm -rf "$B/n5-sub2.gitdir"
  mkdir -p "$B/n5-gitfile/sub2"
  g init -q --separate-git-dir="$B/n5-sub2.gitdir" "$B/n5-gitfile/sub2"
  printf 'package s2\n\nfunc SubOnly() int { return 2 }\n' > "$B/n5-gitfile/sub2/z.go"
  g -C "$B/n5-gitfile/sub2" add -A
  g -C "$B/n5-gitfile/sub2" commit -qm s2
  mkdir -p "$B/n5-gitfile/broken"
  printf 'gitdir: /nonexistent/xm-gitdir\n' > "$B/n5-gitfile/broken/.git"
  printf 'package br\n\nfunc Broken() int { return 3 }\n' > "$B/n5-gitfile/broken/w.go"
  # an embedded repository added as a gitlink, with no .gitmodules entry
  base_ws "$B/g1-gitlink"
  sub_repo "$B/g1-gitlink/sub"
  g -C "$B/g1-gitlink" add sub
  g -C "$B/g1-gitlink" commit -qm gitlink
  rm -rf "$B/subsrc"
  sub_repo "$B/subsrc"
  base_ws "$B/s1-sub-dirty"
  g -C "$B/s1-sub-dirty" submodule -q add "$B/subsrc" mod
  g -C "$B/s1-sub-dirty" commit -qm addsub
  printf 'package sub\n\nfunc SubOnly() int { return 8 }\n' > "$B/s1-sub-dirty/mod/x.go"
  echo junk > "$B/s1-sub-dirty/mod/new.txt"
  rm -rf "$B/s2-sub-uninit"
  g clone -q "$B/s1-sub-dirty" "$B/s2-sub-uninit"
  base_ws "$B/s3-sub-ignall"
  g -C "$B/s3-sub-ignall" submodule -q add "$B/subsrc" mod
  g -C "$B/s3-sub-ignall" config -f .gitmodules submodule.mod.ignore all
  g -C "$B/s3-sub-ignall" add .gitmodules
  g -C "$B/s3-sub-ignall" commit -qm addsub
  printf 'package sub\n\nfunc SubOnly() int { return 9 }\n' > "$B/s3-sub-ignall/mod/x.go"
}

# tracked <dir> <paths>: a repo of <paths> tracked files (a.go, b.go, then
# dNNNN/fNNNN.{go,txt,md,py} 1,000 per directory) written by fast-import and checked out
tracked() {
  local d=$1 n=$2
  rm -rf "$d"
  g init -q "$d"
  python3 - "$n" <<'PY' | g -C "$d" fast-import --quiet
import sys
total = int(sys.argv[1])
w = sys.stdout.buffer.write
bodies = {
    "go": [b"package p%d\n\nfunc F%d() int { return %d }\n" % (i, i, i) for i in range(16)],
    "py": [b"def f%d():\n    return %d\n" % (i, i) for i in range(16)],
    "txt": [b"note %d\n" % i for i in range(16)],
    "md": [b"# Page %d\n" % i for i in range(16)],
}
marks = {}
mark = 0
for ext, items in bodies.items():
    for i, data in enumerate(items):
        mark += 1
        marks[(ext, i)] = mark
        w(b"blob\nmark :%d\ndata %d\n" % (mark, len(data)) + data + b"\n")
seed = b"package p\n\n// Alpha is the seed.\nfunc Alpha() int { return 1 }\n"
call = b"package p\n\nfunc Beta() int { return Alpha() + 1 }\n"
for m, data in ((1000, seed), (1001, call)):
    w(b"blob\nmark :%d\ndata %d\n" % (m, len(data)) + data + b"\n")
w(b"commit refs/heads/main\ncommitter x <x@x> 0 +0000\ndata 3\nbig\n")
w(b"M 100644 :1000 a.go\nM 100644 :1001 b.go\n")
exts = ("go", "txt", "md", "py")
for n in range(total - 2):
    ext = exts[n % 4]
    w(b"M 100644 :%d d%04d/f%04d.%s\n" % (marks[(ext, n % 16)], n // 1000, n % 1000, ext.encode()))
w(b"\n")
PY
  g -C "$d" symbolic-ref HEAD refs/heads/main
  g -C "$d" -c core.fsmonitor=false reset -q --hard
}

untracked() {
  local d=$B/u200k
  base_ws "$d"
  python3 - "$d" <<'PY'
import os, sys
root = sys.argv[1]
for d in range(200):
    p = os.path.join(root, "junk", "u%03d" % d)
    os.makedirs(p, exist_ok=True)
    for f in range(1000):
        with open(os.path.join(p, "n%04d.go" % f), "w") as fh:
            fh.write("package u\n")
PY
}

for set in "$@"; do
  case "$set" in
    nested) nested ;;
    t100k) tracked "$B/t100k" 100000 ;;
    t1m) tracked "$B/t1m" 1000000 ;;
    u200k) untracked ;;
    *) echo "unknown set: $set" >&2; exit 2 ;;
  esac
  echo "fixtures_done set=$set"
done
