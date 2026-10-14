# A child that records EVERY signal it is handed, one line per delivery, instead of
# dying on it: two deliveries are two lines. A `sh` trap cannot witness that, because
# `dash` coalesces back-to-back SIGINTs, so the terminal's copy and the wrapper's relayed
# copy arrived microseconds apart and the trap ran once (measured on the box: three
# SIGINTs in a tight loop, one trap call). That is how the base code looked correct to a
# shell-based test of WS-FIX-20b.
#
# Each delivery also acknowledges itself in `ack`, carrying the count it has seen. The
# test waits for that acknowledgement rather than for a quiet log, so the observation
# boundary is the child's own report that it handled the signal, not a timer that a
# descheduled watcher can outlast (WS-FIX-20b r2).
import os
import signal
import sys
import time

d = sys.argv[1]
seen = [0]


def note(name):
    seen[0] += 1
    with open(d + "/log", "a") as f:
        f.write("%s %d\n" % (name, seen[0]))
    with open(d + "/ack", "a") as f:
        f.write("%s %d\n" % (name, seen[0]))
        f.flush()
        os.fsync(f.fileno())


signal.signal(signal.SIGINT, lambda s, f: note("INT"))
signal.signal(signal.SIGQUIT, lambda s, f: note("QUIT"))
signal.signal(signal.SIGTERM, lambda s, f: note("TERM"))
with open(d + "/pid", "w") as f:
    f.write(str(os.getpid()))
with open(d + "/ready", "w") as f:
    f.write("ready\n")
deadline = time.time() + 60
while time.time() < deadline:
    if os.path.exists(d + "/stop"):
        sys.exit(0)
    time.sleep(0.02)
sys.exit(9)