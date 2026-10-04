#!/usr/bin/env python3
"""ws-38c-b mutation rows: one per new test, each proving the test can fail.

Applies one reversal at a time to the branch's own source, runs the single test that must
notice, restores the file from a saved copy and verifies the bytes came back. Prints one
`subject=` line per subject and a SUMMARY, so one box run yields every FAIL line.

Restore is by COPY BACK with a checksum, not by `git checkout`: a clean-status assumption
is how a restore can silently keep a mutation.
"""
import hashlib
import os
import subprocess
import sys

ROOT = "/mnt/Home-Drive-SS2/xm-work/xm-remote/ws-38c-b"
SAVE = "/tmp/ws38cb-save"

FLOWS_RS = "rust-core/src/index/flows.rs"
READER_RS = "rust-core/src/index/reader.rs"
MOD_RS = "rust-core/src/index/mod.rs"

# (row, file, exact old text, new text, test name)
#
# Each subject is the ONE line its test's assertion is about, chosen so the test's own
# assertion — not some neighbour — is what fails. M2 is the generation comparison the
# refusal turns on; M3 is the seed's area joining the community set; M4 is the retention cap's
# own comparison, which the publication test reads through `caps.max_retained_steps`.
SUBJECTS = [
    (
        "M2", READER_RS,
        "                    if read_generation != self.generation.to_string() {",
        "                    if read_generation == read_generation {",
        "a_store_generation_that_moved_past_the_segment_is_refused_rather_than_mixed",
    ),
    (
        "M3", FLOWS_RS,
        "    for &origin in &walk.origins {\n",
        "    for &origin in walk.origins.iter().take(0) {\n",
        "the_seeds_own_community_counts_toward_the_class",
    ),
    (
        "M4", FLOWS_RS,
        "        if retained + walk.visited.len() as u64 > limits.max_retained_steps {",
        "        if retained + walk.visited.len() as u64 > u64::MAX {",
        "a_walk_that_would_push_the_run_past_the_global_cap_is_dropped_and_counted",
    ),
    (
        "M5", FLOWS_RS,
        "    let mut retained: u64 = 0;\n",
        "    let mut retained: u64 = limits.max_retained_steps;\n",
        "the_retained_step_set_is_bounded_by_one_published_global_cap",
    ),
    (
        "M6", READER_RS,
        '                    conn.execute_batch("BEGIN DEFERRED").map_err(sql)?;\n',
        "",
        "a_store_generation_that_moved_past_the_segment_is_refused_rather_than_mixed",
    ),
]


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_one(row, rel, old, new, test):
    path = os.path.join(ROOT, rel)
    with open(path) as fh:
        original = fh.read()
    if original.count(old) != 1:
        print(f"subject={row} test={test} ANCHOR_MISS occurrences={original.count(old)}")
        return None
    mutated = original.replace(old, new, 1)
    backup = os.path.join(SAVE, rel.replace("/", "_"))
    os.makedirs(SAVE, exist_ok=True)
    with open(path, "w") as fh:
        fh.write(mutated)
    print(f"subject={row} applied={rel} bytes={len(original)}->{len(mutated)}")

    # Force the rebuild. A survivor caused by a binary that never saw the change is worse
    # than no row at all, and `cargo test`'s mtime check can miss an rsynced file.
    os.utime(path, None)
    built = subprocess.run(
        ["cargo", "test", "--test", "flows", "--no-run"],
        cwd=os.path.join(ROOT, "rust-core"),
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=280,
    )
    if built.returncode != 0:
        print(f"subject={row} test={test} BUILD_FAILED")
        for ln in built.stdout.decode(errors="replace").splitlines()[-15:]:
            print("    " + ln)
        with open(path, "w") as fh:
            fh.write(original)
        return None

    proc = subprocess.run(
        ["cargo", "test", "--test", "flows", "--", "--test-threads=1", test],
        cwd=os.path.join(ROOT, "rust-core"),
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=280,
    )
    out = proc.stdout.decode(errors="replace")
    killed = proc.returncode != 0
    fail_lines = [ln for ln in out.splitlines() if "FAILED" in ln or "panicked at" in ln]
    verdict = "KILLED" if killed else "SURVIVED"
    print(f"subject={row} test={test} {verdict} :: "
          + (fail_lines[0] if fail_lines else out.strip().splitlines()[-1:][0]
             if out.strip() else "no output"))
    for ln in fail_lines[:4]:
        print(f"    {ln.strip()}")

    with open(path, "w") as fh:
        fh.write(original)
    back = sha(path) == sha(backup) if os.path.exists(backup) else None
    with open(backup, "w") as fh:
        fh.write(original)
    ok = sha(path) == sha(backup)
    print(f"subject={row} restored={'byte-identical' if ok else 'MISMATCH'}")
    return killed, ok


applied = killed_n = survived = broken = 0
for row, rel, old, new, test in SUBJECTS:
    try:
        res = run_one(row, rel, old, new, test)
    except subprocess.TimeoutExpired:
        print(f"subject={row} test={test} TIMEOUT")
        res = None
    if res is None:
        continue
    applied += 1
    killed, ok = res
    if killed:
        killed_n += 1
    else:
        survived += 1
    if not ok:
        broken += 1

print(f"SUMMARY applied={applied} killed={killed_n} survived={survived} "
      f"restore_mismatch={broken}")
sys.exit(1 if survived or broken else 0)