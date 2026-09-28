#!/usr/bin/env python3
"""WS-15 end-to-end freshness through the API: a 5,000-file repository (the Rust test's
synthetic Go/Rust/TS/JS/TSX tree), the API with the resident worker, the watcher
registered by a first search. Each edit's freshness is the time from the write until the
index generation moved (read with one-shot `index stats`, which never refreshes), with no
agent read in between: the watcher, the Go orchestrator, the one-shot `index update
--paths` in the heavy slot and the snapshot reload. Then a search must find the edit.
usage: e2e_fresh.py <xmustard-api> <xmustard-core>"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

api_bin, core = sys.argv[1], os.path.abspath(sys.argv[2])
work = tempfile.mkdtemp(prefix="ws15-e2e-")
root = os.path.join(work, "repo")


def synthetic(i):
    d = f"mod{i % 50:02d}"
    kind = i % 5
    if kind == 0:
        return f"{d}/svc{i}.go", f"package mod{i % 50:02d}\n\ntype Service{i} struct {{\n\tName string\n}}\n\nfunc (s *Service{i}) Handle{i}(in string) string {{\n\treturn s.Name + in\n}}\n"
    if kind == 1:
        return f"{d}/lib{i}.rs", f"pub struct Store{i} {{ total: u64 }}\n\nimpl Store{i} {{\n    pub fn add(&mut self, v: u64) -> u64 {{\n        self.total += v;\n        self.total\n    }}\n}}\n"
    if kind == 2:
        return f"{d}/widget{i}.ts", f"import {{ helper{i + 1} }} from './util{i + 1}';\n\nexport class Widget{i} {{\n  render(): number {{\n    return helper{i + 1}({i});\n  }}\n}}\n"
    if kind == 3:
        return f"{d}/util{i}.js", f"export function helper{i}(value) {{\n  return value * 2 + {i};\n}}\n"
    return f"{d}/view{i}.tsx", f"export function View{i}(props: {{ title: string }}) {{\n  return <b>{{props.title}}</b>;\n}}\n"


for i in range(5000):
    rel, body = synthetic(i)
    p = os.path.join(root, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, "w") as f:
        f.write(body)
for args in (["init", "-q"], ["config", "user.email", "t@t"], ["config", "user.name", "t"], ["add", "-A"], ["commit", "-qm", "c"]):
    subprocess.run(["git", "-C", root, *args], check=True)
root = os.path.realpath(root)

log = os.path.join(work, "execs.log")
wrapper = os.path.join(work, "xmustard-core")
with open(wrapper, "w") as f:
    f.write(f"#!/bin/sh\necho \"$(date +%s%N) $*\" >> '{log}'\nexec '{core}' \"$@\"\n")
os.chmod(wrapper, 0o755)
data = os.path.join(work, "data")
os.makedirs(data)
s = socket.socket()
s.bind(("127.0.0.1", 0))
port = s.getsockname()[1]
s.close()
env = {k: v for k, v in os.environ.items() if not k.startswith("XMUSTARD_")}
env.update(XMUSTARD_DATA_DIR=data, XMUSTARD_API_PORT=str(port), XMUSTARD_CORE_BIN=wrapper, XMUSTARD_CORE_WORKER="1",
           XMUSTARD_RSS_SOFT_CEILING_BYTES=str(1 << 40))
api = subprocess.Popen([api_bin], env=env, stdout=open(os.path.join(work, "api.log"), "wb"), stderr=subprocess.STDOUT)
base = f"http://127.0.0.1:{port}"


def req(method, path, body=None, timeout=600):
    r = urllib.request.Request(base + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                               headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        sys.exit(f"{method} {path}: {e.code} {e.read()[:300]!r}")


def generation():
    out = subprocess.run([core, "index", "stats", root], capture_output=True, text=True, check=True).stdout
    return int(json.loads(out).get("generation") or 0)


try:
    for _ in range(400):
        try:
            req("GET", "/api/health")
            break
        except OSError:
            time.sleep(0.05)
    ws = req("POST", "/api/workspaces/load", {"root_path": root, "auto_scan": True})["workspace"]["workspace_id"]
    t = time.time()
    while True:
        body = json.dumps(req("GET", f"/api/workspaces/{ws}/search?q=Widget2"))
        if '"watcher_state": "ok"' in body or '"watcher_state":"ok"' in body:
            break
        if time.time() - t > 300:
            sys.exit("the watcher never reached ok: " + body[:500])
        time.sleep(0.2)
    print(f"first search to watcher ok: {time.time() - t:.1f} s")
    samples = []
    for n in range(10):
        i = 3 + 5 * n
        rel = f"mod{i % 50:02d}/util{i}.js"
        g0 = generation()
        with open(log) as f:
            before = len(f.readlines())
        t0 = time.time_ns()
        with open(os.path.join(root, rel), "w") as f:
            f.write(f"export function helper{i}(value) {{\n  return value + {n};\n}}\nexport function watcherEdit{n}() {{ return {n}; }}\n")
        while generation() == g0:
            if time.time_ns() - t0 > 10e9:
                sys.exit(f"edit {n}: the index did not move within 10 s")
            time.sleep(0.02)
        fresh = (time.time_ns() - t0) / 1e6
        with open(log) as f:
            lines = f.readlines()[before:]
        upd = [l for l in lines if " index update " in l]
        started = (int(upd[0].split()[0]) - t0) / 1e6 if upd else None
        paths = upd[0].split("--paths")[1].split() if upd and "--paths" in upd[0] else None
        samples.append(fresh)
        print(f"edit {n}: fresh after {fresh:.0f} ms; index update started at {started:.0f} ms with --paths {paths}")
        time.sleep(0.5)
    samples.sort()
    print(f"p50 {samples[5]:.0f} ms, max {samples[-1]:.0f} ms over {len(samples)} edits")
    with open(log) as f:
        before = len(f.readlines())
    body = req("GET", f"/api/workspaces/{ws}/search?q=watcherEdit9")
    text = json.dumps(body)
    with open(log) as f:
        extra = [l.split(" ", 1)[1].strip() for l in f.readlines()[before:]]
    print("search for the last edit:", "hit" if "watcherEdit9" in text else "MISS",
          "| source resident_index" if "resident_index" in text else "| not the resident index",
          "| core execs during the read:", extra or "none")
finally:
    api.terminate()
    try:
        api.wait(20)
    except subprocess.TimeoutExpired:
        api.kill()
    shutil.rmtree(work, ignore_errors=True)
