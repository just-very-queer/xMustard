"""Diagnostic (never a gate attempt): API-only RSS under the gate's concurrent-read load.

Uses the unchanged rss_bench fixture generator and one database-free CLI import of the
same 2,500-row report, then, for each binary in an interleaved schedule, starts a fresh
API on a copy of that data dir, runs two GET /diagnostics reader threads for --seconds,
and samples the API process RSS (ps, KiB) every 50 ms. Reports max/median RSS, reads,
client latency and API CPU (ps cputime) per trial.
Usage: api_read_rss.py <tree-for-fixture> <outdir> --bin A=/path/api --bin B=/path/api
       --ops /path/xmustard-ops --core /path/xmustard-core [--trials 5] [--seconds 4]
"""
import argparse
import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import threading
import time

ap = argparse.ArgumentParser()
ap.add_argument("tree"); ap.add_argument("outdir")
ap.add_argument("--bin", action="append", required=True)
ap.add_argument("--ops", required=True); ap.add_argument("--core", required=True)
ap.add_argument("--trials", type=int, default=5); ap.add_argument("--seconds", type=float, default=4.0)
args = ap.parse_args()
sys.path.insert(0, os.path.join(args.tree, "scripts", "bench"))
import rss_bench  # noqa: E402
from harness import Api, git_commit_all, run  # noqa: E402

bins = dict(b.split("=", 1) for b in args.bin)
tmp = tempfile.mkdtemp(prefix="xm-apirss-")
repo, seed = os.path.join(tmp, "repo"), os.path.join(tmp, "seed")
os.makedirs(repo); os.makedirs(seed)
rss_bench.generate_repo(repo)
git_commit_all(repo)
with open(os.path.join(seed, "settings.json"), "w") as f:
    json.dump({"require_multi_agent_verification": False}, f)
report = os.path.join(tmp, "diagnostics-2500.json")
with open(report, "w") as f:
    json.dump([{"path": "core/src/many.rs", "message": f"rss_diag_{i}: synthetic", "severity": 2,
                "range": {"start": {"line": i % 60, "character": 0}, "end": {"line": i % 60, "character": 8}}}
               for i in range(rss_bench.DIAG_ROWS)], f)
api = Api(bins[sorted(bins)[0]], seed, {"XMUSTARD_CORE_BIN": args.core}).start()
_, snap, _ = api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True})
ws = snap["workspace"]["workspace_id"]
api.stop()
env = {k: v for k, v in os.environ.items() if not k.startswith("XMUSTARD_")}
env["XMUSTARD_CORE_BIN"] = args.core
run([args.ops, "diagnostics", "run", ws, "--data-dir", seed, "--input-path", report,
     "--source-kind", "compiler", "--source-name", "rss"], env=env)


def ps(pid, fields):
    return subprocess.run(["ps", "-o", fields, "-p", str(pid)], capture_output=True, text=True).stdout.strip()


def cpu_s(pid):
    t = ps(pid, "cputime=")  # [[dd-]hh:]mm:ss.cc
    parts = [float(x) for x in t.replace("-", ":").split(":")]
    s = 0.0
    for p in parts:
        s = s * 60 + p
    return s


trials = []
for i in range(args.trials):
    for label in sorted(bins):
        data = os.path.join(tmp, f"data-{label}-{i}")
        shutil.copytree(seed, data)
        a = Api(bins[label], data, {"XMUSTARD_CORE_BIN": args.core}).start()
        pid = a.proc.pid
        idle = int(ps(pid, "rss=") or 0)
        st, body, _ = a.get(f"/api/workspaces/{ws}/diagnostics")
        assert st == 200 and len(body["diagnostics"]) == rss_bench.DIAG_ROWS and body["storage"]["status"] in ("available", "stale"), body.get("storage")
        stop, lat, samples = threading.Event(), [], []

        def reader():
            while not stop.is_set():
                t0 = time.perf_counter()
                code = a.get(f"/api/workspaces/{ws}/diagnostics")[0]
                lat.append(((time.perf_counter() - t0) * 1000, code))

        c0 = cpu_s(pid)
        ths = [threading.Thread(target=reader, daemon=True) for _ in range(2)]
        [t.start() for t in ths]
        end = time.time() + args.seconds
        while time.time() < end:
            samples.append(int(ps(pid, "rss=") or 0))
            time.sleep(0.05)
        stop.set()
        [t.join(30) for t in ths]
        c1 = cpu_s(pid)
        a.stop()
        ms = sorted(x[0] for x in lat)
        trials.append({"trial": i + 1, "bin": label, "idle_rss_kib": idle, "max_rss_kib": max(samples),
                       "median_rss_kib": int(statistics.median(samples)), "samples": len(samples), "reads": len(lat),
                       "codes": sorted({x[1] for x in lat}), "lat_ms_p50": round(ms[len(ms) // 2], 1),
                       "lat_ms_p95": round(ms[int(len(ms) * 0.95)], 1), "api_cpu_s": round(c1 - c0, 2),
                       "cpu_ms_per_read": round((c1 - c0) * 1000 / max(1, len(lat)), 2)})
        print(json.dumps(trials[-1]), flush=True)
        shutil.rmtree(data, ignore_errors=True)
summary = {}
for label in sorted(bins):
    t = [x for x in trials if x["bin"] == label]
    summary[label] = {k: [x[k] for x in t] for k in ("max_rss_kib", "median_rss_kib", "reads", "lat_ms_p50", "lat_ms_p95", "cpu_ms_per_read")}
out = {"instrumented": False, "gate_attempt": False, "bins": {k: {"path": v, "sha256": subprocess.run(["shasum", "-a", "256", v], capture_output=True, text=True).stdout.split()[0]} for k, v in bins.items()},
       "seconds": args.seconds, "trials": trials, "summary": summary}
os.makedirs(args.outdir, exist_ok=True)
with open(os.path.join(args.outdir, "api_read_rss.json"), "w") as f:
    json.dump(out, f, indent=1)
shutil.rmtree(tmp, ignore_errors=True)
print(json.dumps(summary, indent=1))
