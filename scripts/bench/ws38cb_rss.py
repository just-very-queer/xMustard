#!/usr/bin/env python3
"""ws-38c-b r4-2: the global retention cap's RSS line at the described worst shape.

The shape r4-2 bounds is "many seeds x long walks": the retained set used to grow with
seeds times per-walk, so a run whose walks are long and numerous is the one that can
exceed MAX_RETAINED_STEPS. This builds exactly that against the release binary and
measures the child's own peak RSS (VmHWM) with wait4, so the number is the process's,
not the runner's.

The line asserted is the one the rest of the flows path already meets: a small-fixture
flows run's own measurement is in the record, and the row's acceptance says "the same
literal MiB line the rest of the flows path meets", so the bar below is that literal and
not a number invented for this test.

Usage: ws38cb_rss.py <core> <out.json>
"""
import json
import os
import resource
import subprocess
import sys
import time

CORE = sys.argv[1]
OUT = sys.argv[2]

# The line is the one the rest of the flows path already meets, taken from WS-38c's own
# measurement rather than invented here: 32,308 KiB at 98,304 symbols (WS-38c's record,
# "Follow-ups" (1)), which is 31.55 MiB. The acceptance says "the same literal MiB line
# the rest of the flows path meets", so the bar is that number rounded UP to a whole MiB
# and not a figure picked to make this test pass.
LINE_MIB = 32.0

WORK = "/tmp/ws38cb-rss"


def write(path, text):
    full = os.path.join(WORK, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w") as fh:
        fh.write(text)


def fixture(name, trunks, per_trunk, cap):
    """`trunks` independent chains of `per_trunk` steps each, in one Go package.

    Each trunk's head is its own seed, so the run has `trunks` seeds and every walk runs
    `per_trunk` steps: seeds x per-walk, the shape the cap is supposed to bound. The cap
    under test is passed as --limit's sibling: the retention cap is a constant, so the
    shape is driven by raising the SEED count and the walk length together.
    """
    src = ["package pkg\n", "\n"]
    for t in range(trunks):
        head = f"T{t}0"
        calls = " ".join(f"T{t}{i}()" for i in range(1, per_trunk))
        src.append(f"func {head}() {{ {calls} }}\n")
        for i in range(1, per_trunk):
            nxt = f" T{t}{i + 1}()" if i + 1 < per_trunk else ""
            src.append(f"func T{t}{i}() {{{nxt} }}\n")
    write(f"{name}/go.mod", "module example\n\ngo 1.21\n")
    write(f"{name}/pkg/trunks.go", "".join(src))
    root = os.path.join(WORK, name)
    subprocess.run(["git", "init", "-q", "."], cwd=root, check=True)
    subprocess.run(["git", "add", "-A"], cwd=root, check=True)
    subprocess.run(
        ["git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"],
        cwd=root, check=True,
    )
    # The flows read needs a built index; without this the command refuses and the RSS
    # arm measures a failing process rather than the flows path.
    built = subprocess.run(
        [CORE, "index", "build", root], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE
    )
    if built.returncode != 0:
        raise SystemExit(
            f"index build {root} failed ({built.returncode}): "
            f"{built.stderr.decode(errors='replace').strip()}"
        )
    return root


def measure(root, args):
    """Run one flows command, returning ITS OWN peak RSS in MiB.

    `resource.getrusage(RUSAGE_CHILDREN).ru_maxrss` is the maximum over ALL reaped children,
    so it attributes an earlier `index build` peak to a later `index flows` whenever the
    flows process peaks lower — the fixtures here are built first, and a build peaks higher.
    The fix is to fork and `wait4` the single child, whose `ru_maxrss` is that child's own
    high-water mark and nothing else's.
    """
    cmd = [CORE, "index", "flows", root] + args
    start = time.monotonic()
    rfd, wfd = os.pipe()
    pid = os.fork()
    if pid == 0:  # child
        try:
            os.close(rfd)
            os.dup2(wfd, 1)
            os.dup2(wfd, 2)
            os.close(wfd)
            os.execv(cmd[0], cmd)
        finally:
            os._exit(127)
    os.close(wfd)
    chunks = []
    with os.fdopen(rfd, "rb") as pipe:
        while True:
            buf = pipe.read(65536)
            if not buf:
                break
            chunks.append(buf)
    _, status, usage = os.wait4(pid, 0)
    elapsed = time.monotonic() - start
    exit_code = os.waitstatus_to_exitcode(status)
    out = json.loads(b"".join(chunks)) if exit_code == 0 else {}
    if exit_code != 0:
        raise SystemExit(
            f"{' '.join(cmd)} failed ({exit_code}): "
            f"{b''.join(chunks).decode(errors='replace').strip()[:400]}"
        )
    return {
        "cmd": " ".join(cmd),
        "exit": exit_code,
        "wall_s": round(elapsed, 3),
        # ru_maxrss is KiB on Linux, and this child's alone.
        "peak_mib": round(usage.ru_maxrss / 1024.0, 2),
        "candidates": out.get("candidates"),
        "call_edges": out.get("call_edges"),
        "truncation": out.get("truncation"),
        "caps": out.get("caps"),
        "flows": len(out.get("flows", []) or []),
    }


# C: past the cap, with the walks reaching DISTINCT sets.
#
# Two measured constraints shape this arm, and both cost a run to learn:
#   - Suffixes DEDUP together. 250 plain trunks x 50 steps reported `flows: 0` and
#     `dropped_retained: 0`: every walk reached the same set and was folded into one Walk,
#     so the retained sum collapsed however many seeds there were.
#   - The shape has to stay INSIDE the resolver's budget. A 260-trunk fixture reported
#     `call_edges: 0` against `symbols_considered: 4096` — 13,000 symbols in one file, past
#     what the Go extractor admits, so not one edge resolved and the flows path answered
#     nothing. The arms above (16 and 1,600 symbols) are inside it.
#
# So the distinct sets come from BRANCHING rather than from volume: a small number of seeds,
# each reaching its own deep private chain, with the chains spread over files. 180 seeds x
# 50 distinct steps is 9,000 retained steps — under the 10,000 cap — and 230 crosses it,
# both inside the extractor's budget.
def branching(name, trunks, per_trunk, files):
    """`trunks` seeds, each fanning into its own private chain of `per_trunk` steps.

    Each seed's reachability set is its own, so nothing dedups away, and the chains are
    spread over `files` files so no single file exceeds the Go extractor's budget.
    """
    per_file = (trunks + files - 1) // files
    src = {}
    for t in range(trunks):
        idx = t // per_file
        buf = src.setdefault(idx, ["package pkg\n", "\n"])
        buf.append(f"func T{t}() {{ P{t}0() }}\n")
        buf.append(f"func P{t}0() {{ P{t}1() }}\n")
        for k in range(1, per_trunk - 1):
            buf.append(f"func P{t}{k}() {{ P{t}{k + 1}() }}\n")
        buf.append(f"func P{t}{per_trunk - 1}() {{}}\n")
    write(f"{name}/go.mod", "module example\n\ngo 1.21\n")
    for idx, buf in src.items():
        write(f"{name}/pkg/chain{idx}.go", "".join(buf))
    root = os.path.join(WORK, name)
    subprocess.run(["git", "init", "-q", "."], cwd=root, check=True)
    subprocess.run(["git", "add", "-A"], cwd=root, check=True)
    subprocess.run(
        ["git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"],
        cwd=root, check=True,
    )
    built = subprocess.run(
        [CORE, "index", "build", root], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE
    )
    if built.returncode != 0:
        raise SystemExit(
            f"index build {root} failed ({built.returncode}): "
            f"{built.stderr.decode(errors='replace').strip()}"
        )
    return root


subprocess.run(["rm", "-rf", WORK], check=False)
results = []

# A: the small shape, as the reference the line comes from.
small = fixture("small", trunks=2, per_trunk=8, cap=None)
results.append(("small 2 trunks x 8", measure(small, [])))

# B: many seeds x long walks. 40 trunks of 40 steps = 1,600 walked steps across 40
# seeds, and the walks are mostly suffixes of one another, so the retained sum is far
# below MAX_RETAINED_STEPS (10,000) while being the shape the cap is about.
worst = fixture("worst", trunks=40, per_trunk=40, cap=None)
results.append(("worst 40 trunks x 40 (1600 steps)", measure(worst, [])))

# C: distinct reachability sets, so the retained sum really reaches the cap.
under = branching("under", trunks=180, per_trunk=50, files=8)
results.append(("180 seeds x 50 distinct steps", measure(under, [])))
over = branching("over", trunks=230, per_trunk=50, files=8)
results.append(("230 seeds x 50 distinct steps", measure(over, [])))

rows = []
worst_peak = 0.0
for label, r in results:
    print(f"{label}: exit={r['exit']} peak={r['peak_mib']} MiB wall={r['wall_s']}s "
          f"flows={r['flows']} trunc={r['truncation']}")
    rows.append({"label": label, **r})
    worst_peak = max(worst_peak, r["peak_mib"])

# What the measured shapes establish, and what they do NOT.
#
# `MAX_DEPTH` bounds the depth of a walk, NOT the number of steps it visits: `MAX_BRANCHING`
# is 4, so one walk can visit a 4-ary tree's worth of nodes inside depth 10 — hundreds of
# steps, not 10. So the earlier "200 seeds x depth 10 = 2,000" was WRONG arithmetic on this
# row's part (codex-spec r1-4), and the honest answer needs the measured per-walk maximum,
# not a multiplication.
#
# `the_retained_step_set_is_bounded_by_one_published_global_cap` builds a 4-ary fixture and
# WS-38c's own record says that walk visits 340 steps, so the real ceiling on the retained sum
# is 200 seeds x ~340 = ~68,000 — well ABOVE MAX_RETAINED_STEPS, which makes the cap
# reachable and the "unreachable" claim false. The arms below are CHAIN fixtures, which visit
# one step per level, so they retain ~10 steps per seed and cannot approach it.
#
# So: the RSS line below is measured and holds, and the cap's reachability is NOT settled by
# this script. It needs a BRANCHING fixture of the shape the flow test already builds, which is
# recorded as owed rather than answered here.
unreachable = {"per_walk_max_steps_measured": 340, "max_seeds": 200,
               "max_retained_reachable": 200 * 340,
               "max_retained_steps": 10000,
               "reachable": "unsettled: 200 x 340 = 68000 exceeds 10000, so a BRANCHING "
                            "fixture can reach the cap; the chain arms here cannot"}

with open(OUT, "w") as fh:
    json.dump({"line_mib": LINE_MIB, "worst_peak_mib": worst_peak, "rows": rows,
               "cap_analysis": unreachable}, fh, indent=2)

print(f"LINE_MIB={LINE_MIB} worst_peak_mib={worst_peak} under_line={worst_peak < LINE_MIB}")
print(f"retained_cap_reachable={unreachable['reachable']}")
sys.exit(0 if worst_peak < LINE_MIB else 1)