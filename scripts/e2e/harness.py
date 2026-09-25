"""Shared harness for xMustard end-to-end and resource scripts (stdlib only).

Starts the real API and stdio MCP shim built from this checkout against temporary
repositories, data roots and random ports, drives them over HTTP and JSON-RPC, and
records named checks. Nothing here talks to a model provider.
"""

import base64
import hashlib
import json
import os
import platform
import queue
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))


class Checks:
    """Named pass/fail checks; exit status is nonzero when any check failed."""

    def __init__(self, name):
        self.name = name
        self.results = []

    def check(self, label, ok, detail=""):
        self.results.append({"check": label, "ok": bool(ok), "detail": detail})
        mark = "PASS" if ok else "FAIL"
        print(f"[{mark}] {label}" + (f" — {detail}" if detail else ""), flush=True)
        return bool(ok)

    def failed(self):
        return [r for r in self.results if not r["ok"]]

    def summary(self):
        return {"suite": self.name, "passed": len(self.results) - len(self.failed()),
                "failed": len(self.failed()), "checks": self.results}


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def clean_env(extra):
    """Inherited environment without any XMUSTARD_* setting, plus explicit values."""
    env = {k: v for k, v in os.environ.items() if not k.startswith("XMUSTARD_")}
    env.update({k: str(v) for k, v in extra.items()})
    return env


def run(cmd, cwd=None, env=None, check=True):
    return subprocess.run(cmd, cwd=cwd, env=env, check=check, capture_output=True, text=True)


def git_commit_all(repo, message="fixture"):
    run(["git", "init", "-q"], cwd=repo)
    run(["git", "add", "-A"], cwd=repo)
    run(["git", "-c", "user.email=e2e@xmustard.invalid", "-c", "user.name=xmustard-e2e",
         "commit", "-qm", message], cwd=repo)


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


class Api:
    """One xmustard-api process on a random loopback port."""

    def __init__(self, binary, data_dir, env=None, log_path=None):
        self.binary, self.data_dir = binary, data_dir
        self.extra = dict(env or {})
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        self.log_path = log_path or os.path.join(data_dir, f"api-{self.port}.log")
        self.proc = None

    def start(self, timeout=20):
        env = clean_env({"XMUSTARD_DATA_DIR": self.data_dir, "XMUSTARD_API_PORT": self.port, **self.extra})
        self.log = open(self.log_path, "ab")
        self.proc = subprocess.Popen([self.binary], env=env, stdout=self.log, stderr=self.log)
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(f"api exited at startup; see {self.log_path}")
            try:
                self.get("/api/health")
                return self
            except OSError:
                time.sleep(0.05)
        raise RuntimeError("api did not become healthy")

    def stop(self, sig=signal.SIGTERM, timeout=40):
        if self.proc and self.proc.poll() is None:
            self.proc.send_signal(sig)
            try:
                self.proc.wait(timeout)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
        if getattr(self, "log", None):
            self.log.close()

    def request(self, method, path, body=None, token=None, headers=None, timeout=120, raw=False):
        data = None
        hdrs = dict(headers or {})
        if body is not None:
            data = body if isinstance(body, (bytes, bytearray)) else json.dumps(body).encode()
            hdrs.setdefault("Content-Type", "application/json")
        if token:
            hdrs["Authorization"] = "Bearer " + token
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=hdrs)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                payload = resp.read()
                status, rh = resp.status, dict(resp.headers)
        except urllib.error.HTTPError as e:
            payload, status, rh = e.read(), e.code, dict(e.headers)
        if raw:
            return status, payload, rh
        try:
            return status, json.loads(payload or b"null"), rh
        except ValueError:
            return status, payload.decode(errors="replace"), rh

    def get(self, path, token=None):
        return self.request("GET", path, token=token)

    def log_text(self):
        with open(self.log_path, "rb") as f:
            return f.read().decode(errors="replace")


class Mcp:
    """One stdio xmustard-mcp shim with a background reader matching replies by id."""

    def __init__(self, binary, api_base, env=None):
        self.binary, self.api_base, self.extra = binary, api_base, dict(env or {})
        self.proc = None
        self.replies = {}
        self.cv = threading.Condition()
        self.next_id = 1

    def start(self):
        env = clean_env({"XMUSTARD_API_BASE": self.api_base, **self.extra})
        self.proc = subprocess.Popen([self.binary], env=env, stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        threading.Thread(target=self._read, daemon=True).start()
        return self

    def _read(self):
        for line in self.proc.stdout:
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            with self.cv:
                self.replies[json.dumps(msg.get("id"))] = msg
                self.cv.notify_all()

    def send(self, method, params=None, id_=None):
        if id_ is None:
            id_ = self.next_id
            self.next_id += 1
        msg = {"jsonrpc": "2.0", "id": id_, "method": method}
        if params is not None:
            msg["params"] = params
        self.proc.stdin.write((json.dumps(msg) + "\n").encode())
        self.proc.stdin.flush()
        return id_

    def notify(self, method, params):
        self.proc.stdin.write((json.dumps({"jsonrpc": "2.0", "method": method, "params": params}) + "\n").encode())
        self.proc.stdin.flush()

    def wait(self, id_, timeout=120):
        key = json.dumps(id_)
        deadline = time.time() + timeout
        with self.cv:
            while key not in self.replies:
                left = deadline - time.time()
                if left <= 0:
                    raise TimeoutError(f"no reply for id {id_}")
                self.cv.wait(left)
            return self.replies.pop(key)

    def call(self, method, params=None, timeout=120):
        return self.wait(self.send(method, params), timeout)

    def tool(self, name, args, timeout=120):
        return self.call("tools/call", {"name": name, "arguments": args}, timeout)

    def read_all(self, uri, timeout=60):
        """Page an evidence resource to EOF using the exact issued URI; returns (bytes, pages)."""
        out, pages, offset = b"", [], 0
        sep = "&" if "?" in uri else "?"
        while True:
            t0 = time.time()
            r = self.call("resources/read", {"uri": f"{uri}{sep}offset={offset}&length=65536"}, timeout)
            elapsed_ms = round((time.time() - t0) * 1000, 1)
            if "error" in r:
                return None, pages + [r]
            blob = r["result"]["contents"][0]["blob"]
            meta = r["result"]["_meta"]["xmustard/page"]
            meta["_client_ms"] = elapsed_ms  # includes the per-page repo-key child
            out += base64.b64decode(blob)
            pages.append(meta)
            if meta["eof"]:
                return out, pages
            offset = meta["next_offset"]

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.stdin.close()
            try:
                self.proc.wait(10)
            except subprocess.TimeoutExpired:
                self.proc.kill()


def build_binaries(out_dir):
    """Build the Go API and shim from this checkout; resolve the Rust core binary."""
    os.makedirs(out_dir, exist_ok=True)
    api_go = os.path.join(REPO_ROOT, "api-go")
    for name in ("xmustard-api", "xmustard-mcp"):
        run(["go", "build", "-o", os.path.join(out_dir, name), f"./cmd/{name}"], cwd=api_go)
    core = os.environ.get("XMUSTARD_CORE_BIN")
    if not core:
        rust = os.path.join(REPO_ROOT, "rust-core")
        run(["cargo", "build", "--release", "--quiet", "--bin", "xmustard-core"], cwd=rust)
        core = os.path.join(rust, "target", "release", "xmustard-core")
    return os.path.join(out_dir, "xmustard-api"), os.path.join(out_dir, "xmustard-mcp"), core


def mint_token(api_binary, data_dir, principal, role):
    env = clean_env({"XMUSTARD_DATA_DIR": data_dir})
    return run([api_binary, "mint-token", principal, role], env=env).stdout.strip()


def finish(checks, report_path=None):
    finish_with(checks, report_path)


def finish_with(checks, report_path=None, extra=None):
    """Write the check summary (plus extra fields such as provenance) and exit nonzero
    when any check failed."""
    summary = checks.summary()
    summary.update(extra or {})
    if report_path:
        with open(report_path, "w") as f:
            json.dump(summary, f, indent=1)
    print(json.dumps({k: summary[k] for k in ("suite", "passed", "failed")}))
    sys.exit(1 if summary["failed"] else 0)


def sha_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def provenance(api_bin, mcp_bin, core, scripts):
    """Bind a report to the exact source and binaries it ran against.

    Contract (same keys and recipe as scripts/bench/rss_bench.py provenance()):
    head; source_diff_sha256 = sha256 of `git diff HEAD -- api-go rust-core/src
    rust-core/Cargo.toml rust-core/Cargo.lock`; untracked_source_files and
    untracked_source_sha256 over sorted "path\\0sha256\\n" of untracked files under
    api-go and rust-core/src; binaries_sha256 for xmustard-api, xmustard-mcp,
    xmustard-core; core_bin and core_bin_prebuilt; script_sha256 for the given
    repo-relative script paths; env (os, machine, python, go, cpus).
    """
    def git(*a):
        return run(["git", *a], cwd=REPO_ROOT, check=False).stdout.strip()
    diff = run(["git", "diff", "HEAD", "--", "api-go", "rust-core/src", "rust-core/Cargo.toml", "rust-core/Cargo.lock"],
               cwd=REPO_ROOT, check=False).stdout
    untracked = git("ls-files", "--others", "--exclude-standard", "--", "api-go", "rust-core/src").split()
    uh = hashlib.sha256()
    for p in sorted(untracked):
        uh.update(p.encode() + b"\0" + sha_file(os.path.join(REPO_ROOT, p)).encode() + b"\n")
    return {
        "head": git("rev-parse", "HEAD"), "source_diff_sha256": hashlib.sha256(diff.encode()).hexdigest(),
        "untracked_source_files": len(untracked), "untracked_source_sha256": uh.hexdigest(),
        "binaries_sha256": {"xmustard-api": sha_file(api_bin), "xmustard-mcp": sha_file(mcp_bin), "xmustard-core": sha_file(core)},
        "core_bin": core, "core_bin_prebuilt": bool(os.environ.get("XMUSTARD_CORE_BIN")),
        "script_sha256": {s: sha_file(os.path.join(REPO_ROOT, s)) for s in scripts},
        "env": {"os": platform.platform(), "machine": platform.machine(), "python": platform.python_version(),
                "go": run(["go", "version"], check=False).stdout.strip(), "cpus": os.cpu_count()},
    }
