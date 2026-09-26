#!/usr/bin/env python3
"""worker-peak.py LABEL BIN ARGS...: run one xmustard-core invocation and print one JSON
line with the worker's own peak RSS (resident size sampled every 1 ms via
proc_pid_rusage), its lifetime max physical footprint (exact, excludes children), and the
wait4 max RSS, which on macOS also covers reaped children such as `git ls-files`.
macOS only (libproc)."""
import ctypes, json, os, subprocess, sys, time

lib = ctypes.CDLL("/usr/lib/libproc.dylib")
class RU(ctypes.Structure):  # rusage_info_v4
    _fields_ = [("uuid", ctypes.c_uint8 * 16)] + [(n, ctypes.c_uint64) for n in (
        "user_time", "system_time", "pkg_idle_wkups", "interrupt_wkups", "pageins",
        "wired_size", "resident_size", "phys_footprint", "proc_start_abstime",
        "proc_exit_abstime", "child_user_time", "child_system_time",
        "child_pkg_idle_wkups", "child_interrupt_wkups", "child_pageins",
        "child_elapsed_abstime", "diskio_bytesread", "diskio_byteswritten",
        "cpu_time_qos_default", "cpu_time_qos_maintenance", "cpu_time_qos_background",
        "cpu_time_qos_utility", "cpu_time_qos_legacy", "cpu_time_qos_user_initiated",
        "cpu_time_qos_user_interactive", "billed_system_time", "serviced_system_time",
        "logical_writes", "lifetime_max_phys_footprint", "instructions", "cycles",
        "billed_energy", "serviced_energy", "interval_max_phys_footprint", "runnable_time")]

label, binp, args = sys.argv[1], sys.argv[2], sys.argv[3:]
t0 = time.time()
p = subprocess.Popen([binp] + args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
own_rss = own_fp = 0
ru = RU()
out_chunks = []
import threading
def reader():
    out_chunks.append(p.stdout.read())
th = threading.Thread(target=reader); th.start()
while True:
    if lib.proc_pid_rusage(p.pid, 4, ctypes.byref(ru)) == 0:
        own_rss = max(own_rss, ru.resident_size)
        own_fp = max(own_fp, ru.lifetime_max_phys_footprint)
    pid, status, rus = os.wait4(p.pid, os.WNOHANG)
    if pid == p.pid:
        break
    time.sleep(0.001)
th.join()
elapsed = time.time() - t0
err = p.stderr.read().decode(errors="replace")
rec = {"label": label, "exit": os.waitstatus_to_exitcode(status),
       "worker_peak_rss_mib": round(own_rss / 2**20, 1),
       "worker_lifetime_max_footprint_mib": round(own_fp / 2**20, 1),
       "tree_maxrss_mib": round(rus.ru_maxrss / 2**20, 1),
       "wall_s": round(elapsed, 2)}
try:
    r = json.loads(out_chunks[0])
    c = r.get("coverage", {})
    rec.update({"mode": r.get("mode"), "reason": r.get("reason"),
                "indexed": c.get("indexed_files"), "eligible": c.get("eligible_files"),
                "symbols": c.get("symbols"), "loss_counts": c.get("loss_counts"),
                "written": r.get("counters", {}).get("written"),
                "rebudgeted": r.get("counters", {}).get("rebudgeted"),
                "bytes_read": r.get("counters", {}).get("bytes_read")})
except Exception:
    rec["stderr"] = err[-400:]
print(json.dumps(rec))
