#!/usr/bin/env python3
"""Hook service load and client cost (WS-23).

Builds xmustard-api and xmustard-hook, starts the API on a scratch data dir with one
registered workspace (written directly, so no scan runs), and measures:

  * the static client: binary size, wall time and max RSS of `xmustard-hook claude
    SessionStart` over the Unix socket (the command hook Claude Code spawns at session
    start), and its fail-open time with no daemon;
  * the http hooks: latency percentiles of PreToolUse(Read) with a path-bound memory in
    the store and PostToolUse(Bash) captures of a 30,000-character output (Claude Code's
    Bash clamp), sent from N concurrent clients;
  * the API's peak RSS (VmHWM) and the process-spawn counters before and after.

Usage: python3 scripts/bench/hooks-load.py [--requests 600] [--clients 4]
Run from the repository root on the build box.
"""

import argparse
import concurrent.futures
import http.client
import json
import os
import shutil
import statistics
import subprocess
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def build(out):
    for cmd in ("xmustard-api", "xmustard-hook"):
        subprocess.run(["go", "build", "-o", os.path.join(out, cmd), "./cmd/" + cmd], cwd=os.path.join(ROOT, "api-go"), check=True)


def post(port, event, body):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    t0 = time.perf_counter()
    conn.request("POST", "/api/hooks/claude/" + event, body=body, headers={"Content-Type": "application/json"})
    resp = conn.getresponse()
    data = resp.read()
    took = time.perf_counter() - t0
    conn.close()
    return resp.status, data, took


def pct(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p / 100 * (len(xs) - 1))))]


def status_kib(pid, key):
    with open("/proc/%d/status" % pid) as f:
        for line in f:
            if line.startswith(key + ":"):
                return int(line.split()[1])
    return -1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--requests", type=int, default=600)
    ap.add_argument("--clients", type=int, default=4)
    args = ap.parse_args()
    work = tempfile.mkdtemp(prefix="xmhook-load-")
    bins, data, repo = os.path.join(work, "bin"), os.path.join(work, "data"), os.path.join(work, "repo")
    os.makedirs(bins)
    os.makedirs(os.path.join(repo, "src"))
    build(bins)
    with open(os.path.join(repo, "src", "billing.go"), "w") as f:
        f.write("package billing\n")
    ws = "wsLoad"
    os.makedirs(os.path.join(data, "workspaces", ws))
    with open(os.path.join(data, "workspaces", ws, "snapshot.json"), "w") as f:
        json.dump({"workspace": {"workspace_id": ws, "root_path": repo}}, f)
    with open(os.path.join(data, "workspaces.json"), "w") as f:
        json.dump([{"workspace_id": ws, "name": "load", "root_path": repo}], f)
    sock = os.path.join(work, "run", "hook.sock")
    port = 18000 + os.getpid() % 1000
    env = dict(os.environ, XMUSTARD_DATA_DIR=data, XMUSTARD_API_PORT=str(port), XMUSTARD_HOOK_SOCKET=sock,
               XMUSTARD_CORE_BIN="/nonexistent/xmustard-core")
    api = subprocess.Popen([os.path.join(bins, "xmustard-api")], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            try:
                if post(port, "SessionEnd", b"{}")[0] == 200 and os.path.exists(sock):
                    break
            except OSError:
                pass
            time.sleep(0.05)
        # a promoted, path-bound memory (open mode: self-asserted, so the hook surface
        # withholds it and says so; the admission work runs either way)
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("POST", "/api/workspaces/%s/context" % ws, body=json.dumps({"title": "billing rounding",
                     "content": "invoices round half-even", "paths": ["src/billing.go"]}), headers={"Content-Type": "application/json"})
        conn.getresponse().read()
        conn.close()

        start = json.dumps({"session_id": "s0", "cwd": repo, "hook_event_name": "SessionStart", "source": "startup"}).encode()
        client = {"size_bytes": os.path.getsize(os.path.join(bins, "xmustard-hook")), "runs": []}
        for _ in range(20):
            p = subprocess.run(["/usr/bin/time", "-f", "%e %M", os.path.join(bins, "xmustard-hook"), "claude", "SessionStart"],
                               input=start, env=dict(env, XMUSTARD_HOOK_TIMEOUT_MS="200"), capture_output=True)
            wall, rss = p.stderr.decode().strip().splitlines()[-1].split()
            client["runs"].append((float(wall), int(rss), len(p.stdout)))
        down = subprocess.run(["/usr/bin/time", "-f", "%e %M", os.path.join(bins, "xmustard-hook"), "claude", "SessionStart"],
                              input=start, env=dict(env, XMUSTARD_HOOK_SOCKET=os.path.join(work, "none.sock"),
                                                    XMUSTARD_API_BASE="http://127.0.0.1:1"), capture_output=True)
        client["no_daemon"] = down.stderr.decode().strip().splitlines()[-1]

        stdout = "".join("=== RUN   TestCase%04d\n--- PASS: TestCase%04d (0.00s)\n" % (i, i) for i in range(900))[:30000]
        read = lambda i: json.dumps({"session_id": "r%d" % i, "cwd": repo, "hook_event_name": "PreToolUse", "tool_name": "Read",
                                     "tool_input": {"file_path": os.path.join(repo, "src", "billing.go")}}).encode()
        bash = lambda i: json.dumps({"session_id": "b%d" % i, "cwd": repo, "hook_event_name": "PostToolUse", "tool_name": "Bash",
                                     "tool_input": {"command": "go test ./..."}, "tool_use_id": "t%d" % i,
                                     "tool_response": {"stdout": stdout, "stderr": "", "interrupted": False, "isImage": False}}).encode()
        rss0 = status_kib(api.pid, "VmRSS")
        lat = {"PreToolUse-Read": [], "PostToolUse-Bash-30k": []}
        replaced = 0
        with concurrent.futures.ThreadPoolExecutor(args.clients) as pool:
            for name, event, body in (("PreToolUse-Read", "PreToolUse", read), ("PostToolUse-Bash-30k", "PostToolUse", bash)):
                for status, data_, took in pool.map(lambda i: post(port, event, body(i)), range(args.requests)):
                    assert status == 200, status
                    lat[name].append(took * 1000)
                    replaced += b"updatedToolOutput" in data_
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        conn.request("GET", "/api/health")
        health = json.loads(conn.getresponse().read())
        result = {
            "client": {"size_bytes": client["size_bytes"],
                       "wall_s_median": statistics.median(r[0] for r in client["runs"]),
                       "max_rss_kib_median": statistics.median(r[1] for r in client["runs"]),
                       "max_rss_kib_max": max(r[1] for r in client["runs"]),
                       "answered": sum(1 for r in client["runs"] if r[2] > 0),
                       "no_daemon_wall_s_rss_kib": client["no_daemon"]},
            "http": {k: {"n": len(v), "p50_ms": round(pct(v, 50), 2), "p95_ms": round(pct(v, 95), 2), "max_ms": round(max(v), 2)}
                     for k, v in lat.items()},
            "replaced_outputs": replaced,
            "api_rss_kib_before": rss0, "api_rss_kib_after": status_kib(api.pid, "VmRSS"), "api_hwm_kib": status_kib(api.pid, "VmHWM"),
            "spawns": health.get("budget", {}).get("counters", {}).get("spawns_total", health.get("budget", {}).get("counters")),
            "hook_usage": health.get("hook_usage"),
        }
        print(json.dumps(result, indent=2))
    finally:
        api.terminate()
        api.wait(timeout=10)
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    main()
