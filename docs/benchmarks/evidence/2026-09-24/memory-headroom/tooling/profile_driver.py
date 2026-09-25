"""Instrumented (diagnostic-only, never a gate attempt) run of the unchanged rss_bench workload.

Imports <tree>/scripts/bench/rss_bench.py without editing it and patches, in memory only:
- build_binaries: rebuilds xmustard-api with `-tags profile` (the loopback pprof hook);
- Api: passes XMUSTARD_PPROF_ADDR=127.0.0.1:<port> and times every GET .../diagnostics.
A watcher thread follows rss_bench.CURRENT_STEP and, on the two peak steps
("two concurrent index clients", "CLI diagnostics imports with concurrent reads"),
records API memstats/rusage at step entry and exit and, with --profile, captures a
CPU profile and allocs profiles (entry/exit, diffable with `go tool pprof -base`).
Usage: python3 profile_driver.py <tree> <outdir> <label> [--profile]
"""
import json
import os
import socket
import subprocess
import sys
import threading
import time
import urllib.request

tree, outdir, label = os.path.abspath(sys.argv[1]), os.path.abspath(sys.argv[2]), sys.argv[3]
do_profile = "--profile" in sys.argv[4:]
os.makedirs(outdir, exist_ok=True)
sys.path.insert(0, os.path.join(tree, "scripts", "bench"))
import rss_bench  # noqa: E402

PEAK_STEPS = ("two concurrent index clients", "CLI diagnostics imports with concurrent reads")
s = socket.socket(); s.bind(("127.0.0.1", 0)); PPROF = f"127.0.0.1:{s.getsockname()[1]}"; s.close()
latency, events = {}, []
orig_build = rss_bench.build_binaries


def build(out_dir):
    api_bin, mcp_bin, core = orig_build(out_dir)
    subprocess.run(["go", "build", "-tags", "profile", "-o", api_bin, "./cmd/xmustard-api"],
                   cwd=os.path.join(tree, "api-go"), check=True)
    return api_bin, mcp_bin, core


class ProfApi(rss_bench.Api):
    def __init__(self, binary, data_dir, env=None, log_path=None):
        super().__init__(binary, data_dir, {**(env or {}), "XMUSTARD_PPROF_ADDR": PPROF}, log_path)

    def get(self, path, token=None):
        t0 = time.perf_counter()
        if do_profile and path.endswith("/diagnostics") and rss_bench.CURRENT_STEP[0] == PEAK_STEPS[1]:
            start_cpu(1, 2)  # readers run ~3 s after the CLI build
        r = super().get(path, token)
        if path.endswith("/diagnostics"):
            latency.setdefault(rss_bench.CURRENT_STEP[0], []).append((time.perf_counter() - t0) * 1000)
        return r


cpu_started = set()


def start_cpu(i, seconds):
    if i in cpu_started:
        return
    cpu_started.add(i)
    threading.Thread(target=lambda: events.append({"cpu_step": PEAK_STEPS[i], "seconds": seconds,
        "cpu": fetch(f"/debug/pprof/profile?seconds={seconds}", os.path.join(outdir, f"{label}.{i}.cpu.pb.gz"))}), daemon=True).start()


def fetch(path, dest=None, timeout=30):
    try:
        with urllib.request.urlopen(f"http://{PPROF}{path}", timeout=timeout) as r:
            data = r.read()
    except OSError as e:
        return {"error": repr(e)}
    if dest:
        with open(dest, "wb") as f:
            f.write(data)
        return {"saved": os.path.basename(dest), "bytes": len(data)}
    return json.loads(data)


def watch(stop):
    last = None
    while not stop.is_set():
        step = rss_bench.CURRENT_STEP[0]
        if step != last:
            if last in PEAK_STEPS:
                ev = {"t": time.time(), "exit": last, "stats": fetch("/debug/xm/stats")}
                if do_profile:
                    ev["allocs"] = fetch("/debug/pprof/allocs", os.path.join(outdir, f"{label}.{PEAK_STEPS.index(last)}.allocs.exit.pb.gz"))
                events.append(ev)
            if step in PEAK_STEPS:
                i = PEAK_STEPS.index(step)
                ev = {"t": time.time(), "enter": step, "stats": fetch("/debug/xm/stats")}
                if do_profile:
                    ev["allocs"] = fetch("/debug/pprof/allocs", os.path.join(outdir, f"{label}.{i}.allocs.enter.pb.gz"))
                    if i == 0:  # the index step lasts ~0.4 s
                        start_cpu(0, 1)
                events.append(ev)
            last = step
        stop.wait(0.02)


orig_sampler_stop = rss_bench.Sampler.stop


def sampler_stop(self):
    # the last peak step never relabels; the sampler stops before the API does
    step = rss_bench.CURRENT_STEP[0]
    if step in PEAK_STEPS:
        ev = {"t": time.time(), "exit": step, "stats": fetch("/debug/xm/stats")}
        if do_profile:
            ev["allocs"] = fetch("/debug/pprof/allocs", os.path.join(outdir, f"{label}.{PEAK_STEPS.index(step)}.allocs.exit.pb.gz"))
        events.append(ev)
    orig_sampler_stop(self)


rss_bench.build_binaries = build
rss_bench.Api = ProfApi
rss_bench.Sampler.stop = sampler_stop
stop = threading.Event()
w = threading.Thread(target=watch, args=(stop,), daemon=True)
w.start()
sys.argv = ["rss_bench.py", "--report", os.path.join(outdir, f"{label}.report.json")] + (["--keep"] if "--keep" in sys.argv[4:] else [])
code = 0
try:
    rss_bench.main()
except SystemExit as e:
    code = e.code or 0
finally:
    stop.set()
    w.join(5)
    def pct(v, p):
        v = sorted(v)
        return round(v[min(len(v) - 1, int(len(v) * p))], 2) if v else None
    summary = {"label": label, "instrumented": True, "gate_attempt": False, "profile_captured": do_profile,
               "pprof_addr": PPROF, "tree": tree,
               "diagnostics_get_latency_ms": {k: {"n": len(v), "p50": pct(v, 0.5), "p95": pct(v, 0.95), "max": pct(v, 1.0)}
                                              for k, v in latency.items()},
               "step_events": events}
    with open(os.path.join(outdir, f"{label}.summary.json"), "w") as f:
        json.dump(summary, f, indent=1)
sys.exit(code)
