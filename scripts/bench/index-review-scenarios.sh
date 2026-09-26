#!/bin/zsh
# WS-07 review round 1 scenarios: one JSON line per run (worker-peak.py) on stdout.
#   usage: index-review-scenarios.sh <xmustard-core> <work-dir> <cline repo> <pi-mono repo>
# Fixtures are cloned or generated under <work-dir>; indexes live there too.
set -euo pipefail
B=$1; W=$2; CLINE=$3; PIMONO=$4
M=${0:A:h}/worker-peak.py
mkdir -p $W
commit() { git -C $1 add -A >/dev/null && git -C $1 -c user.email=t@t -c user.name=t commit -qm "$2" >/dev/null; }
clone() { rm -rf $2; git clone -q --local --no-hardlinks $1 $2; }
# past the symbol envelope: noops, then a one-file edit
P=$W/pi-mono; clone $PIMONO $P; rm -rf $W/idx-pi
$M pi-sym-build $B index build $P --index-dir $W/idx-pi --max-symbols 40000
for i in 1 2 3; do $M pi-sym-noop$i $B index update $P --index-dir $W/idx-pi --max-symbols 40000; done
f=$(git -C $P ls-files -- '*.ts' | awk 'NR == 7')
printf '\nexport function scenarioAdded() { return 2; }\n' >> $P/$f
$M pi-sym-edit $B index update $P --index-dir $W/idx-pi --max-symbols 40000
# one committed oversized bundle must not block the noop exit
C=$W/cline; clone $CLINE $C; rm -rf $W/idx-cl
python3 -c "open('$C/generated_bundle.js','w').write(''.join('var v%d = function(){ return %d; };\n' % (i, i) for i in range(50000)))"
commit $C bundle
$M cline-bundle-build $B index build $C --index-dir $W/idx-cl
for i in 1 2 3; do $M cline-bundle-noop$i $B index update $C --index-dir $W/idx-cl; done
# 60,000 tracked one-line files (default envelope: 10,000 indexed)
E=$W/t60k
if [[ ! -d $E/.git ]]; then
  rm -rf $E; mkdir -p $E
  python3 -c "
import os
for i in range(60000):
    d='$E/d%03d' % (i % 600); os.makedirs(d, exist_ok=True)
    open(d + '/f%05d.ts' % i, 'w').write('export const v%d = %d;\n' % (i, i))"
  git -C $E init -q; commit $E c
fi
rm -rf $W/idx-60k
$M 60k-build $B index build $E --index-dir $W/idx-60k --no-cache
$M 60k-noop $B index update $E --index-dir $W/idx-60k
# 20,000 present plus 100,000 missing tracked entries
T=$W/t120k
if [[ ! -d $T/.git ]]; then
  rm -rf $T; mkdir -p $T; git -C $T init -q
  blob=$(git -C $T hash-object -w --stdin </dev/null)
  python3 - "$T" "$blob" <<'PY'
import os, sys
root, blob = sys.argv[1], sys.argv[2]
lines = []
for i in range(20000):
    rel = f"p{i % 100:02d}/f{i:05d}.ts"
    os.makedirs(os.path.join(root, os.path.dirname(rel)), exist_ok=True)
    open(os.path.join(root, rel), "w").write(f"export const v{i} = {i};\n")
    lines.append(f"100644 {blob}\t{rel}\n")
lines += [f"100644 {blob}\tgone/g{i:06d}.ts\n" for i in range(100000)]
open(os.path.join(root, ".info"), "w").write("".join(lines))
PY
  git -C $T update-index --add --index-info < $T/.info; rm $T/.info
fi
rm -rf $W/idx-120k
$M 120k-build $B index build $T --index-dir $W/idx-120k --no-cache
$M 120k-noop $B index update $T --index-dir $W/idx-120k
# one deeply nested 64 KB file
D=$W/deep; rm -rf $D $W/idx-deep; mkdir -p $D
python3 -c "open('$D/deep.ts','w').write('function a(){'*4600 + '}'*4600 + '\n'); open('$D/ok.ts','w').write('export function ok() {}\n')"
git -C $D init -q; commit $D c
$M deep-build $B index build $D --index-dir $W/idx-deep --no-cache
# a pathological ignore pattern
H=$W/glob; rm -rf $H $W/idx-glob; mkdir -p $H
python3 -c "open('$H/' + 'a'*40 + '.ts','w').write('export const a = 1;\n'); open('$H/.xmustardignore','w').write('*a'*12 + '*b\n')"
git -C $H init -q; commit $H c
$M glob12-build $B index build $H --index-dir $W/idx-glob
