#!/usr/bin/env python3
"""ws-38c-b r4-4: the mutation that must KILL the freshness test.

M1 survived against the `--limit=1` fixture, because `total_paths` is built from `ranked` —
the scored pool AFTER the MAX_CANDIDATES cut — so a candidate dropped by `--limit` (which
lowers the same cap) is still in the pool and its file is already in `total_paths`.

What `candidate_paths` actually adds is the CALLERS OF SCORED CANDIDATES, including
candidates that fell OUTSIDE the 200-cap and so never became seeds at all. Shape B measures
that: 260 equal-scoring mid symbols fill the pool, `Low1` (score 2.0) stays a seed, and the
low-scoring `Low2..Low4` in the SAME file are outside the pool — yet they are callers of a
reported seed, so their file belongs in the envelope.

This applies the mutation, runs the one test that must notice, and restores from a saved copy
with a checksum.
"""
import hashlib
import os
import subprocess

ROOT = "/mnt/Home-Drive-SS2/xm-work/xm-remote/ws-38c-b"
MOD_RS = "rust-core/src/index/mod.rs"
TEST = "an_unselected_callers_file_marks_the_answer_stale"
OLD = "        .chain(result.candidate_paths.iter().cloned())\n"

path = os.path.join(ROOT, MOD_RS)
with open(path) as fh:
    original = fh.read()
assert original.count(OLD) == 1, f"anchor occurrences={original.count(OLD)}"
with open(path, "w") as fh:
    fh.write(original.replace(OLD, "", 1))
print(f"applied: removed candidate_paths from the dispatch path set "
      f"({len(original)} -> {len(original) - len(OLD)} bytes)")

# A stale test binary reads the OLD source and would report the mutation survived when the
# mutation was never compiled. `cargo test` rebuilds by mtime, and the box copy's rsync can
# leave the timestamp older than the artifact, so touch the file and build explicitly.
os.utime(path, None)
built = subprocess.run(
    ["cargo", "test", "--test", "flows", "--no-run"],
    cwd=os.path.join(ROOT, "rust-core"),
    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=280,
)
print("rebuilt=%s" % ("ok" if built.returncode == 0 else "FAILED"))
if built.returncode != 0:
    for ln in built.stdout.decode(errors="replace").splitlines()[-20:]:
        print("  " + ln)

proc = subprocess.run(
    ["cargo", "test", "--test", "flows", "--", "--test-threads=1", TEST],
    cwd=os.path.join(ROOT, "rust-core"),
    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=280,
)
out = proc.stdout.decode(errors="replace")
print("MUTATION_EXIT=%d" % proc.returncode)
for ln in out.splitlines():
    if "FAILED" in ln or "panicked at" in ln or "test result:" in ln or "not named" in ln:
        print("  " + ln.strip())

with open(path, "w") as fh:
    fh.write(original)
restored = hashlib.sha256(open(path, "rb").read()).hexdigest() == \
    hashlib.sha256(original.encode()).hexdigest()
print("restored=%s" % ("byte-identical" if restored else "MISMATCH"))
print("KILLED=%s" % (proc.returncode != 0))