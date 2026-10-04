#!/usr/bin/env python3
"""Apply one mutation, run the test that must die, restore the exact line.

Every mutation is a single-line replacement applied to a saved copy, and the file is
restored by reading the replaced line back from the copy. Never `git checkout --`: the
tree may hold another lane's work, and goal 6 A4's restore rule assumes a clean tree
this script does not get to assume.
"""
import shutil
import subprocess
import sys
import tempfile

BOX = "/Users/for_work/Developer/xMustard/.git/xm-parity/bin/xm-box.sh"
WT = "/private/tmp/claude-501/xm-wt/ws-fix-22b"


def run(subject, path, old, new, test):
    with tempfile.NamedTemporaryFile("r", delete=False) as save:
        backup = save.name
    shutil.copy(path, backup)
    text = open(path).read()
    if text.count(old) != 1:
        print(f"SKIP {subject}: the target line appears {text.count(old)} times, not 1")
        shutil.copy(backup, path)
        return 2
    open(path, "w").write(text.replace(old, new))
    cmd = [BOX, "run", WT, f"cd api-go && go test -p 1 -count=1 -run '{test}' ./internal/workspaceops"]
    rc = subprocess.run(cmd, capture_output=True, text=True)
    tail = [l for l in rc.stdout.splitlines() if "remote_exit=" in l or "FAIL" in l or "ok  " in l]
    # restore by reading the original bytes back, then prove the file is byte-identical
    shutil.copy(backup, path)
    same = open(path, "rb").read() == open(backup, "rb").read()
    print(f"{subject}: {' '.join(tail[-3:])} restored_identical={same}")
    return 0 if same else 1


PROV = f"{WT}/api-go/internal/workspaceops/memory_provenance.go"
VOTES = f"{WT}/api-go/internal/govstore/votes.go"
PARITY = "TestTheBatchedOwnerGuardAnswersExactlyWhatThePerRowGuardAnswers"

MUTATIONS = [
    # M1: the batched guard drops the revision-author arm. The parity test must catch
    # it, and the queue's own tests must stay green — that is the drift this row exists
    # to prevent, and the reason the parity test is the row's centre.
    ("M1 revision-author arm dropped", PROV,
     "\t\tif sameOwner(revOwner, mine) {\n\t\t\tout[i] = refuse(\"the revision's author\")",
     "\t\tif false && sameOwner(revOwner, mine) {\n\t\t\tout[i] = refuse(\"the revision's author\")", PARITY),
    # M2: the recordedOwner fallback goes, so a write that recorded no owner falls
    # back to nothing and a revoked-token owner stops tying to its write.
    ("M2 recordedOwner fallback dropped", PROV,
     "\t\trevOwner = recordedOwner(dataDir, revOwner, ro.Author)\n",
     "\t\trevOwner = strings.TrimSpace(revOwner)\n", PARITY),
    # M3: the epoch join goes from the batched votes query, so a verdict cast in a
    # superseded epoch counts as a voice.
    ("M3 epoch join dropped from the batched votes query", VOTES,
     "\t\t\tFROM votes v JOIN entries e ON e.id = v.entry_id AND e.vote_epoch = v.epoch\n\t\t\tWHERE v.verdict IN (`",
     "\t\t\tFROM votes v JOIN entries e ON e.id = v.entry_id\n\t\t\tWHERE v.verdict IN (`", PARITY),
]

if __name__ == "__main__":
    bad = 0
    for m in MUTATIONS:
        bad |= run(*m)
    sys.exit(bad)