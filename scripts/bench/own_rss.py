#!/usr/bin/env python3
"""own_rss.py <stdout_file> <cmd...>: run one command and print one JSON line with its
exit code, wall time, its own peak RSS (VmHWM of the process itself, sampled from /proc
every 10 ms) and the largest peak among it and its waited children (a git child
included). Linux only; the WS-RT3 box measurements use it to put xMustard's own
memory and Git's on separate lines."""
import json
import resource
import subprocess
import sys
import time

start = time.monotonic()
with open(sys.argv[1], "wb") as out:
    proc = subprocess.Popen(sys.argv[2:], stdout=out, stderr=subprocess.DEVNULL)
    own = 0
    while proc.poll() is None:
        try:
            with open(f"/proc/{proc.pid}/status") as status:
                for line in status:
                    if line.startswith("VmHWM:"):
                        own = max(own, int(line.split()[1]))
        except OSError:
            pass
        time.sleep(0.01)
tree = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
print(json.dumps({"rc": proc.returncode, "wall_s": round(time.monotonic() - start, 2),
                  "core_hwm_kb": own, "tree_max_kb": tree}))
