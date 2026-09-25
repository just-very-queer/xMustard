"""Sampled process-tree RSS for the plan's fixed workload (scripts/bench/rss.sh).

Samples `ps -axo pid,ppid,rss` every 100 ms and sums RSS over every xMustard-owned
process in ONE ps snapshot: the API, both stdio MCP shims and all their descendants
(Rust core, git, ast-grep). Workload: generated repo of 500 tracked Go/Rust/TS files
(~20 MiB) plus one 70-symbol file; cold query, warm query, same-size dirty edit,
rename/delete cycle, two concurrent index clients, five concurrent exactly-16 MiB
capture attempts against the 64 MiB transient pool, then a full 64 KiB-paged expansion
of every retained original (plus one re-expansion) while the sampler is still running.

Gate: sampled tree peak <= 100 MB (100,000,000 bytes), valid only when the sampler took
samples, every ps call succeeded, every required root (API + both shims) was present in
every sample, and the whole workload completed. Sampling can miss short peaks, so the
result is a SAMPLED peak. OS child high-water (getrusage) is reported separately in
platform units; neither number, nor their maximum, is claimed as the true tree peak or a
universal RSS ceiling. Byte admission (pool peak) is admission evidence, not an RSS
bound. Hardware memory bandwidth, Rust/shim allocations and copy counts are unmeasured.
"""

import argparse
import base64
import hashlib
import http.client
import json
import os
import platform
import re
import select
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "e2e"))
from harness import REPO_ROOT, Api, Checks, Mcp, build_binaries, finish, git_commit_all, run  # noqa: E402

GATE_BYTES = 100_000_000
CAPTURE_BYTES = 16 * 1024 * 1024
PAGE_BYTES = 64 * 1024
CURRENT_STEP = ["setup"]
REQUIRED_STEPS = [
    "load workspace (initial scan)", "index baseline", "cold query symbol_070", "warm query symbol_070",
    "same-size dirty edit", "rename/delete cycle", "two concurrent index clients",
    "five concurrent 16 MiB captures", "expand every retained original", "re-expand one original",
]


def generate_repo(root, files=500, target_bytes=20 * 1024 * 1024):
    # Baseline fixture: kept byte-identical for comparable runs. Known syntax limitation:
    # the generated Rust statements (`v += k`) lack `;`, so tree-sitter-rust parses them
    # through GLR error recovery (slower cold parse than valid Rust would be).
    per = target_bytes // files
    langs = ["go", "rs", "ts"]
    for i in range(files):
        lang = langs[i % 3]
        d = os.path.join(root, {"go": "svc", "rs": "core/src", "ts": "web/src"}[lang], f"m{i // 50:02d}")
        os.makedirs(d, exist_ok=True)
        parts, n, size = [], 0, 0
        if lang == "go":
            parts.append(f"package m{i // 50:02d}\n\n")
        while size < per:
            name = f"f{i:03d}_{n:03d}"
            body = "".join(f"\t// step {k}: accumulate value for {name}\n\tv += {k}\n" for k in range(12))
            if lang == "go":
                s = f"// {name} computes a value.\nfunc {name}(v int) int {{\n{body}\treturn v\n}}\n\n"
            elif lang == "rs":
                s = f"/// {name} computes a value.\npub fn {name}(mut v: i64) -> i64 {{\n{body.replace(chr(9), '    ')}    v\n}}\n\n"
            else:
                s = f"// {name} computes a value.\nexport function {name}(v: number): number {{\n{body.replace(chr(9), '  ')}  return v;\n}}\n\n"
            parts.append(s)
            size += len(s)
            n += 1
        with open(os.path.join(d, f"file_{i:03d}.{lang}"), "w") as f:
            f.write("".join(parts))
    many = "".join(f"pub fn symbol_{k:03d}() -> u32 {{\n    {k}\n}}\n\n" for k in range(1, 71))
    with open(os.path.join(root, "core", "src", "many.rs"), "w") as f:
        f.write(many)


def tree_digest(root, paths):
    """sha256 over sorted (path NUL content-sha256 LF): identifies the exact fixture."""
    h = hashlib.sha256()
    for p in sorted(paths):
        with open(os.path.join(root, p), "rb") as f:
            h.update(p.encode() + b"\0" + hashlib.sha256(f.read()).hexdigest().encode() + b"\n")
    return h.hexdigest()


def dir_bytes(path):
    """(apparent bytes, allocated bytes, files) under path; missing path is zero."""
    size = alloc = files = 0
    for d, _, names in os.walk(path):
        for n in names:
            try:
                st = os.lstat(os.path.join(d, n))
            except OSError:
                continue
            size += st.st_size
            alloc += st.st_blocks * 512
            files += 1
    return size, alloc, files


class Sampler(threading.Thread):
    """Every 100 ms: one `ps` snapshot, summed RSS over the descendants of the roots.

    `roots` is {label: pid}; every root must appear in every snapshot. A failed ps
    call, an unparsable line or a missing root is recorded and invalidates the gate.
    """

    def __init__(self, roots, storage_dir=None):
        super().__init__(daemon=True)
        self.roots = dict(roots)
        self.storage_dir = storage_dir
        self.stop_ev = threading.Event()
        self.peak_kib, self.samples, self.max_core = 0, 0, 0
        self.pid_peak = {}  # pid -> (KiB, command): per-process sampled maxima (diagnostic)
        self.peak_snapshot, self.peak_step, self.peak_t = [], None, None
        self.step_peak_kib = {}  # workload step -> sampled tree peak (KiB)
        self.errors, self.lost_roots = [], []
        self.gaps, self.last_t, self.t0 = [], None, time.time()
        self.series = []  # [t_s, step, tree KiB]
        self.storage_peak = (0, 0, 0)
        self.storage_step = None

    def sample_once(self):
        t = time.time()
        step = CURRENT_STEP[0]
        try:
            p = subprocess.run(["ps", "-axo", "pid=,ppid=,rss=,comm="], capture_output=True, text=True, timeout=5)
        except (OSError, subprocess.TimeoutExpired) as e:
            self.errors.append({"t": round(t - self.t0, 2), "step": step, "error": repr(e)})
            return
        if p.returncode != 0 or not p.stdout.strip():
            self.errors.append({"t": round(t - self.t0, 2), "step": step,
                                "error": f"ps exit {p.returncode}: {p.stderr.strip()[:200]}"})
            return
        procs, children = {}, {}
        for line in p.stdout.splitlines():
            f = line.split(None, 3)
            try:
                pid, ppid, rss = int(f[0]), int(f[1]), int(f[2])
            except (ValueError, IndexError):
                self.errors.append({"t": round(t - self.t0, 2), "step": step, "error": f"unparsable ps line {line[:120]!r}"})
                return
            procs[pid] = (ppid, rss, os.path.basename(f[3]) if len(f) > 3 else "?")
            children.setdefault(ppid, []).append(pid)
        missing = [lbl for lbl, pid in self.roots.items() if pid not in procs]
        if missing:
            self.lost_roots.append({"t": round(t - self.t0, 2), "step": step, "missing": missing})
            return
        owned, stack = set(), list(self.roots.values())
        while stack:
            q = stack.pop()
            if q in owned:
                continue
            owned.add(q)
            stack.extend(children.get(q, []))
        total = sum(procs[q][1] for q in owned)  # ps rss is KiB on macOS and Linux
        self.samples += 1
        if self.last_t is not None:
            self.gaps.append(t - self.last_t)
        self.last_t = t
        self.max_core = max(self.max_core, sum(1 for q in owned if "xmustard-core" in procs[q][2]))
        for q in owned:
            comm, rss = procs[q][2], procs[q][1]
            if rss > self.pid_peak.get(q, (0, comm))[0]:
                self.pid_peak[q] = (rss, comm)
        if total > self.peak_kib:
            self.peak_kib, self.peak_step, self.peak_t = total, step, round(t - self.t0, 2)
            self.peak_snapshot = sorted(({"pid": q, "ppid": procs[q][0], "comm": procs[q][2], "rss_kib": procs[q][1]}
                                         for q in owned), key=lambda r: -r["rss_kib"])
        self.step_peak_kib[step] = max(self.step_peak_kib.get(step, 0), total)
        self.series.append([round(t - self.t0, 2), step, total])
        if self.storage_dir and self.samples % 10 == 0:  # ~1 s storage probe
            s = dir_bytes(self.storage_dir)
            if s[0] > self.storage_peak[0]:
                self.storage_peak, self.storage_step = s, step

    def run(self):
        while not self.stop_ev.is_set():
            t0 = time.time()
            self.sample_once()
            self.stop_ev.wait(max(0.0, 0.1 - (time.time() - t0)))

    def stop(self):
        self.stop_ev.set()
        self.join(10)

    def validity(self):
        problems = []
        if self.samples == 0:
            problems.append("zero samples")
        if self.errors:
            problems.append(f"{len(self.errors)} ps errors")
        if self.lost_roots:
            problems.append(f"required roots missing in {len(self.lost_roots)} snapshots")
        return problems

    def report(self):
        peak_bytes = self.peak_kib * 1024
        gaps = sorted(self.gaps)
        return {
            "peak_bytes": peak_bytes, "peak_mb": round(peak_bytes / 1e6, 1), "peak_mib": round(peak_bytes / 2**20, 1),
            "samples": self.samples, "interval_ms_target": 100,
            "interval_ms_observed": {"median": round(gaps[len(gaps) // 2] * 1000, 1) if gaps else None,
                                     "max": round(gaps[-1] * 1000, 1) if gaps else None,
                                     "over_150ms": sum(1 for g in gaps if g > 0.15)},
            "ps_errors": self.errors[:20], "ps_error_count": len(self.errors),
            "lost_root_snapshots": self.lost_roots[:20], "lost_root_count": len(self.lost_roots),
            "required_roots": self.roots,
            "peak_step": self.peak_step, "peak_t_s": self.peak_t,
            "processes_at_peak": self.peak_snapshot,
            "step_sampled_peak_mb": {k: round(v * 1024 / 1e6, 1) for k, v in self.step_peak_kib.items()},
            "per_process_sampled_max_kib_by_command": {
                comm: max(k for k, c in self.pid_peak.values() if c == comm)
                for comm in {c for _, c in self.pid_peak.values()}},
            "note": "per-process maxima are unsynchronized diagnostics; they are never summed",
            "max_concurrent_xmustard_core": self.max_core,
            "units": "ps rss KiB (1024 bytes) on darwin/linux; *_mb = 10^6 bytes, *_mib = 2^20 bytes",
            "series_t_step_kib": self.series,
        }


def capture_attempt(port, ws, payload, decided, gate, out, idx):
    """POST one capture with `Expect: 100-continue` and a declared 16 MiB body.

    The API reserves the declared length before reading the body, so admission is
    decided on the headers: an interim `100 Continue` means admitted (the body is then
    held until `gate` opens, so admitted reservations overlap), a final status means
    refused before any body byte was sent. Only a parsed wire response counts; a socket
    error is recorded as `transport_error`, never as a refusal.
    """
    rec = {"idx": idx, "outcome": None, "body_bytes_sent": 0}
    out[idx] = rec
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=120)
    t0 = time.time()
    try:
        conn.putrequest("POST", f"/api/workspaces/{ws}/evidence?tool=why_failed&content_type=text/plain&call_id=cap{idx}")
        conn.putheader("Content-Type", "text/plain")
        conn.putheader("Content-Length", str(len(payload)))
        conn.putheader("Expect", "100-continue")
        conn.endheaders()
        sock, head, deadline = conn.sock, b"", time.time() + 30
        while b"\r\n\r\n" not in head:
            if time.time() > deadline:
                raise TimeoutError("no interim or final response to headers")
            r, _, _ = select.select([sock], [], [], 0.05)
            if r:
                head = sock.recv(65536, socket.MSG_PEEK)
                if not head:
                    raise ConnectionError("closed before any response")
        rec["decision_ms"] = round((time.time() - t0) * 1000, 1)
        if head.startswith(b"HTTP/1.1 100"):
            sock.recv(head.index(b"\r\n\r\n") + 4)  # consume the interim response only
            rec["admitted_on_headers"] = True
            decided.set()
            gate.wait(30)
            conn.send(payload)
            rec["body_bytes_sent"] = len(payload)
        else:
            rec["admitted_on_headers"] = False
            decided.set()
        resp = conn.getresponse()
        body = resp.read()
        rec.update(status=resp.status, retry_after=resp.getheader("Retry-After"), response_bytes=len(body))
        try:
            rec["json"] = json.loads(body)
        except ValueError:
            rec["json"] = None
        overloaded = isinstance(rec["json"], dict) and rec["json"].get("overloaded") is True
        if resp.status == 503 and overloaded:
            rec["outcome"] = "wire_503_overloaded"
        elif resp.status == 200:
            rec["outcome"] = "captured"
        else:
            rec["outcome"] = f"http_{resp.status}"
    except Exception as e:  # noqa: BLE001 - every failure is recorded, never reclassified
        rec.update(outcome="transport_error", error=repr(e))
        decided.set()
    finally:
        rec["elapsed_ms"] = round((time.time() - t0) * 1000, 1)
        conn.close()


def expand(fetch, handle_label, raw_bytes, raw_sha, max_retries=50):
    """Page one original to EOF in 64 KiB pages via `fetch(offset)`.

    fetch returns ("ok", data_bytes, meta, delivered_bytes) or ("overload", None, None, 0)
    or ("error", detail, None, 0). Overloads are retried with backoff and counted. Any
    offset mismatch, empty or oversized non-EOF page, non-contiguous next_offset or more
    pages than the original's size allows fails immediately (no unbounded loop).
    """
    h, got, offset, pages, retries, wire, lat = hashlib.sha256(), 0, 0, 0, 0, 0, []
    max_pages = -(-raw_bytes // PAGE_BYTES) + 1

    def fail(msg):
        return {"handle": handle_label, "ok": False, "error": msg, "pages": pages, "bytes": got}
    while True:
        if pages >= max_pages:
            return fail(f"page count exceeded {max_pages} for {raw_bytes} bytes")
        tries = 0
        while True:
            t0 = time.time()
            kind, data, meta, wb = fetch(offset)
            if kind != "overload":
                break
            retries += 1
            tries += 1
            if tries > max_retries:
                return fail(f"overloaded {tries} times at offset {offset}")
            time.sleep(min(1.0, 0.05 * tries))
        lat.append((time.time() - t0) * 1000)
        if kind != "ok":
            return fail(f"offset {offset}: {data}")
        pages += 1
        wire += wb
        if len(data) > PAGE_BYTES:
            return fail(f"page at {offset} is {len(data)} bytes > 64 KiB")
        if meta.get("offset") != offset:
            return fail(f"page offset {meta.get('offset')} != requested {offset}")
        if meta.get("raw_sha256") != raw_sha:
            return fail(f"page at {offset} reports raw_sha256 {meta.get('raw_sha256')}")
        h.update(data)
        got += len(data)
        if meta.get("eof") is True:
            break
        if not data:
            return fail(f"empty non-EOF page at {offset}")
        if meta.get("next_offset") != offset + len(data):
            return fail(f"non-contiguous next_offset {meta.get('next_offset')} after {offset}+{len(data)}")
        offset = meta["next_offset"]
    lat.sort()
    exact = got == raw_bytes and h.hexdigest() == raw_sha
    return {"handle": handle_label, "ok": exact, "bytes": got, "sha256": h.hexdigest(),
            "exact_size_and_sha256": exact, "pages": pages, "overload_retries": retries,
            "delivered_bytes": wire, "delivered_measure": getattr(fetch, "delivered_measure", "unknown"),
            "page_ms": {"p50": round(lat[len(lat) // 2], 1), "p95": round(lat[int(len(lat) * 0.95)], 1),
                        "max": round(lat[-1], 1), "total_s": round(sum(lat) / 1000, 2)}}


def http_fetcher(api, ws, handle):
    def fetch(offset):
        st, raw, _ = api.request("GET", f"/api/workspaces/{ws}/evidence/{handle}?offset={offset}&length={PAGE_BYTES}", raw=True)
        if st == 503:
            return "overload", None, None, 0
        if st != 200:
            return "error", f"HTTP {st} {raw[:200]!r}", None, 0
        page = json.loads(raw)
        return "ok", base64.b64decode(page["data"]), page, len(raw)
    fetch.delivered_measure = "http_response_body_bytes (exact)"
    return fetch


def mcp_fetcher(mcp, uri):
    sep = "&" if "?" in uri else "?"

    def fetch(offset):
        r = mcp.call("resources/read", {"uri": f"{uri}{sep}offset={offset}&length={PAGE_BYTES}"}, timeout=60)
        if "error" in r:
            if r["error"].get("code") == -32000:
                return "overload", None, None, 0
            return "error", json.dumps(r["error"])[:200], None, 0
        blob = r["result"]["contents"][0]["blob"]
        # the shared harness does not expose the frame's wire length; this re-serializes
        # the parsed reply, so it is an estimate (Go spacing/escaping differ)
        return "ok", base64.b64decode(blob), r["result"]["_meta"]["xmustard/page"], len(json.dumps(r))
    fetch.delivered_measure = "mcp_reply_reserialized_bytes (estimate, not wire bytes)"
    return fetch


def timed(label, fn, steps, set_label=True):
    if set_label:
        CURRENT_STEP[0] = label
    t0 = time.time()
    r = fn()
    steps.append({"step": label, "seconds": round(time.time() - t0, 3)})
    if set_label:
        CURRENT_STEP[0] = "between steps"
    return r


def tool_json(res):
    try:
        return json.loads(res["result"]["content"][0]["text"])
    except (KeyError, ValueError, IndexError, TypeError):
        return {}


def sha_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def provenance(api_bin, mcp_bin, core):
    def git(*a):
        return run(["git", *a], cwd=REPO_ROOT, check=False).stdout.strip()
    src = {}
    for name in ("scripts/bench/rss_bench.py", "scripts/e2e/harness.py"):
        src[name] = sha_file(os.path.join(REPO_ROOT, name))
    # dirty-tree identity of the sources that built the binaries (tracked diff + untracked files)
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
        "script_sha256": src,
        "env": {"os": platform.platform(), "machine": platform.machine(), "python": platform.python_version(),
                "go": run(["go", "version"], check=False).stdout.strip(), "cpus": os.cpu_count()},
    }


def work_check(checks, works, label, cond, desc):
    w = works.get(label, {})
    keys = ("graph_cache", "files_read", "files_parsed", "files_reused", "bytes_parsed", "symbols_indexed")
    checks.check(desc, bool(w) and cond(w), json.dumps({k: w.get(k) for k in keys}))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report")
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()
    checks = Checks("rss-bench")
    tmp = tempfile.mkdtemp(prefix="xm-rss-")
    api = sampler = None
    shims = []
    steps, works, captures = [], {}, [None] * 5
    result = {"tmp": tmp if args.keep else None}
    completed = False
    try:
        api_bin, mcp_bin, core = build_binaries(os.path.join(tmp, "bin"))
        result["provenance"] = provenance(api_bin, mcp_bin, core)
        repo = os.path.join(tmp, "repo")
        os.makedirs(repo)
        generate_repo(repo)
        git_commit_all(repo)
        tracked = run(["git", "ls-files"], cwd=repo).stdout.split()
        repo_bytes = sum(os.path.getsize(os.path.join(repo, p)) for p in tracked)
        many = open(os.path.join(repo, "core", "src", "many.rs")).read()
        result["fixture"] = {"tracked_files": len(tracked), "bytes": repo_bytes, "tree_sha256": tree_digest(repo, tracked),
                             "many_rs_symbols": len(re.findall(r"^pub fn symbol_\d+", many, re.M)),
                             "syntax_limitation": "generated Rust statements lack ';' (tree-sitter-rust GLR error recovery); baseline kept unchanged for comparability"}
        checks.check("fixture: 501 tracked files, ~20 MiB, 70-symbol file",
                     len(tracked) == 501 and 19 * 2**20 <= repo_bytes <= 21 * 2**20 and result["fixture"]["many_rs_symbols"] == 70,
                     f"{len(tracked)} files, {repo_bytes} bytes")
        data = os.path.join(tmp, "data")
        os.makedirs(data)
        with open(os.path.join(data, "settings.json"), "w") as f:
            json.dump({"require_multi_agent_verification": False}, f)
        api = Api(api_bin, data, {"XMUSTARD_CORE_BIN": core}).start()
        shims = [Mcp(mcp_bin, api.base).start(), Mcp(mcp_bin, api.base).start()]
        for s in shims:
            s.call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "rss-bench", "version": "1"}})
        roots = {"xmustard-api": api.proc.pid, "xmustard-mcp#1": shims[0].proc.pid, "xmustard-mcp#2": shims[1].proc.pid}
        evidence_dir = os.path.join(data, "evidence")
        sampler = Sampler(roots, storage_dir=evidence_dir)
        sampler.start()
        mcp = shims[0]
        st, snap, _ = timed("load workspace (initial scan)", lambda: api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True}), steps)
        ws = snap["workspace"]["workspace_id"]
        timed("index baseline", lambda: api.request("POST", f"/api/workspaces/{ws}/index"), steps)

        def search(label, q, client=mcp, set_label=True):
            r = timed(label, lambda: client.tool("search", {"workspace_id": ws, "query": q}, timeout=300), steps, set_label)
            body = tool_json(r)
            works[label] = (body.get("coverage") or {}).get("work", {})
            return body

        cold = search("cold query symbol_070", "symbol_070")
        checks.check("cold query finds symbol_070 (beyond the old 32/64 caps)", any(h.get("path") == "core/src/many.rs" for h in cold.get("hits", [])[:5]))
        work_check(checks, works, "cold query symbol_070", lambda w: w.get("graph_cache") == "miss" and w.get("files_parsed") == len(tracked),
                   "cold query is a cache miss that parses every tracked file")
        search("warm query symbol_070", "symbol_070")
        work_check(checks, works, "warm query symbol_070", lambda w: w.get("graph_cache") == "hit" and w.get("files_parsed") == 0,
                   "warm query is a cache hit with 0 files parsed")
        target = os.path.join(repo, tracked[0])
        with open(target) as f:
            text = f.read()
        with open(target, "w") as f:
            f.write(text.replace("accumulate", "accumulatf", 1))  # same size
        search("same-size dirty edit", "symbol_070")
        work_check(checks, works, "same-size dirty edit", lambda w: w.get("files_parsed") == 1 and w.get("files_reused") == len(tracked) - 1,
                   "same-size dirty edit reparses exactly one file")
        run(["git", "mv", tracked[1], tracked[1] + ".moved"], cwd=repo)
        os.remove(os.path.join(repo, tracked[2]))
        search("rename/delete cycle", "symbol_070")
        work_check(checks, works, "rename/delete cycle",
                   lambda w: w.get("files_parsed", 99) <= 1 and w.get("symbols_indexed", 1 << 30) < works["cold query symbol_070"].get("symbols_indexed", 0),
                   "rename/delete reparses at most the renamed file and drops the deleted file's symbols")
        with open(os.path.join(repo, tracked[3]), "a") as f:
            f.write("\n")
        both = [None, None]
        CURRENT_STEP[0] = "two concurrent index clients"
        t0 = time.time()
        # one stable label for the whole concurrent group (threads never relabel)
        ths = [threading.Thread(target=lambda i=i: both.__setitem__(i, search(f"index client {i + 1} (concurrent)", "symbol_070", shims[i], False))) for i in range(2)]
        [t.start() for t in ths]
        [t.join() for t in ths]
        steps.append({"step": "two concurrent index clients", "seconds": round(time.time() - t0, 3)})
        CURRENT_STEP[0] = "between steps"
        checks.check("two concurrent index clients both answer", all(b and b.get("hits") for b in both))

        # exactly 16 MiB each: repeat past the size, then slice exactly (no trimming)
        unit = b"PASS suite case ok\n"
        payload = (unit * (CAPTURE_BYTES // len(unit) + 1))[:CAPTURE_BYTES]
        payload_sha = hashlib.sha256(payload).hexdigest()
        checks.check("capture payloads are exactly 16 MiB", len(payload) == CAPTURE_BYTES, f"{len(payload)} bytes x5, sha256 {payload_sha[:16]}")
        gate = threading.Event()
        decided = [threading.Event() for _ in range(5)]
        CURRENT_STEP[0] = "five concurrent 16 MiB captures"
        th = [threading.Thread(target=capture_attempt, args=(api.port, ws, payload, decided[i], gate, captures, i)) for i in range(5)]
        t0 = time.time()
        [t.start() for t in th]
        all_decided = all(e.wait(30) for e in decided)
        _, held, _ = api.get("/api/health")  # pool while admitted reservations are held
        gate.set()
        [t.join() for t in th]
        CURRENT_STEP[0] = "between steps"
        steps.append({"step": "five concurrent 16 MiB captures", "seconds": round(time.time() - t0, 3)})
        outcomes = [c["outcome"] for c in captures]
        checks.check("every capture attempt was decided on its headers", all_decided, str(outcomes))
        checks.check("no capture attempt ended in a transport error", "transport_error" not in outcomes,
                     json.dumps([c.get("error") for c in captures if c.get("error")]))
        refused = [c for c in captures if c["outcome"] == "wire_503_overloaded"]
        checks.check("at least one capture refused on the wire: 503 + Retry-After + overloaded:true",
                     bool(refused) and all(c["retry_after"] and c["body_bytes_sent"] == 0 for c in refused), str(outcomes))
        ok_caps = [c for c in captures if c["outcome"] == "captured"]
        checks.check("admitted captures succeed", len(ok_caps) >= 1, str(outcomes))
        checks.check("pool held while admitted captures overlapped never exceeded its max",
                     held["transient_pool"]["in_use"] <= held["transient_pool"]["max"], json.dumps(held["transient_pool"]))
        storage_after_capture = dir_bytes(evidence_dir)
        deliveries = []
        for c in ok_caps:
            d = c["json"] or {}
            deliveries.append({"idx": c["idx"], "handle": d.get("handle"), "resource_uri": d.get("resource_uri"),
                               "raw_bytes": d.get("raw_bytes"), "raw_sha256": d.get("raw_sha256"),
                               "projected_bytes": d.get("projected_bytes"), "projection_len": len((d.get("projection") or "").encode()),
                               "response_bytes": c["response_bytes"], "reduced": d.get("reduced"), "reducer": d.get("reducer"),
                               "omissions": len(d.get("omissions") or [])})
        checks.check("every captured original reports exact size and SHA-256 and retains a handle",
                     all(d["handle"] and d["raw_bytes"] == CAPTURE_BYTES and d["raw_sha256"] == payload_sha for d in deliveries),
                     json.dumps([(d["handle"] is not None, d["raw_bytes"], (d["raw_sha256"] or "")[:12]) for d in deliveries]))

        # Expand every retained original fully while the sampler runs: even captures via
        # stdio MCP resources/read (alternating shims), odd via the HTTP evidence route.
        expansions = []
        CURRENT_STEP[0] = "expand every retained original"
        t0 = time.time()
        for k, d in enumerate(deliveries):
            if not d["handle"]:
                continue
            if k % 2 == 0:
                via, fetch = f"mcp#{k // 2 % 2 + 1}", mcp_fetcher(shims[k // 2 % 2], d["resource_uri"])
            else:
                via, fetch = "http", http_fetcher(api, ws, d["handle"])
            e = expand(fetch, d["handle"][:12], d["raw_bytes"], payload_sha)
            e["via"] = via
            expansions.append(e)
        steps.append({"step": "expand every retained original", "seconds": round(time.time() - t0, 3)})
        CURRENT_STEP[0] = "re-expand one original"
        t0 = time.time()
        if deliveries and deliveries[0]["handle"]:
            e = expand(http_fetcher(api, ws, deliveries[0]["handle"]), deliveries[0]["handle"][:12], CAPTURE_BYTES, payload_sha)
            e["via"], e["re_expansion"] = "http", True
            expansions.append(e)
        steps.append({"step": "re-expand one original", "seconds": round(time.time() - t0, 3)})
        CURRENT_STEP[0] = "between steps"
        first = [e for e in expansions if not e.get("re_expansion")]
        checks.check("every captured original fully expanded in <=64 KiB pages with exact size and SHA-256",
                     len(first) == len(deliveries) and all(e["ok"] for e in expansions),
                     json.dumps([(e["via"], e.get("pages"), e.get("exact_size_and_sha256"), e.get("error")) for e in expansions]))
        storage_after_expand = dir_bytes(evidence_dir)

        _, health, _ = api.get("/api/health")
        pool, kids = health["transient_pool"], health["children"]
        checks.check("transient pool in-use never exceeded its max and is released", pool["peak"] <= pool["max"] and pool["in_use"] == 0, json.dumps(pool))
        checks.check("helper-child limit held", kids["peak"] <= kids["cap"] and sampler.max_core <= kids["cap"],
                     f"admission peak {kids['peak']}/{kids['cap']}, sampled concurrent xmustard-core {sampler.max_core}")
        result["admission"] = {
            "capture_attempts": [{k: v for k, v in c.items() if k != "json"} for c in captures],
            "outcomes": outcomes, "pool_while_held": held["transient_pool"], "transient_pool_final": pool, "children": kids,
            "client": "Expect: 100-continue; refusal decided on headers before any body byte",
        }
        result["bytes"] = {
            "raw_per_capture": CAPTURE_BYTES, "captured_originals": len(deliveries), "deliveries": deliveries,
            "retained_storage_after_capture": {"apparent": storage_after_capture[0], "allocated": storage_after_capture[1], "files": storage_after_capture[2]},
            "retained_storage_after_expansion": {"apparent": storage_after_expand[0], "allocated": storage_after_expand[1], "files": storage_after_expand[2]},
            "expansions": expansions,
            "expansion_count": len(expansions), "pages_total": sum(e.get("pages", 0) for e in expansions),
            "overload_retries_total": sum(e.get("overload_retries", 0) for e in expansions),
            "expansion_delivered_bytes_by_measure": {
                m: sum(e.get("delivered_bytes", 0) for e in expansions if e.get("delivered_measure") == m)
                for m in {e.get("delivered_measure") for e in expansions if e.get("delivered_measure")}},
            "raw_bytes_recovered_total": sum(e.get("bytes", 0) for e in expansions),
        }
        completed = True
    except Exception as e:
        checks.check("harness completed without exception", False, repr(e))
    finally:
        if sampler:
            sampler.stop()
        for s in shims:
            s.stop()
        if api:
            api.stop()
            m = re.search(r"rusage (.*)", api.log_text())
            result["api_rusage"] = m.group(1) if m else None
        if not args.keep:
            shutil.rmtree(tmp, ignore_errors=True)
    result["os_child_high_water"] = {
        "api_self_and_children": result.get("api_rusage"),
        "unit": "bytes" if sys.platform == "darwin" else "KiB",
        "note": ("API getrusage: self high-water and the largest single waited-for child (Rust core/git); "
                 "not a concurrent tree peak. MCP shim high-water is unmeasured (the harness's own "
                 "RUSAGE_CHILDREN also counts `go build`, so it is not attributable and not reported)."),
    }
    if sampler:
        rep = sampler.report()
        rep["storage_sampled_peak"] = {"apparent": sampler.storage_peak[0], "allocated": sampler.storage_peak[1],
                                       "files": sampler.storage_peak[2], "step": sampler.storage_step}
        result["sampled_tree"] = rep
        invalid = sampler.validity()
        missing_steps = [s for s in REQUIRED_STEPS if s not in {x["step"] for x in steps}]
        if not completed or missing_steps:
            invalid.append(f"workload incomplete (missing steps {missing_steps})")
        # a low RSS on a failed workload is not a pass: every mandatory workload check
        # (fixture, retrieval work, admission, capture, expansion, pool, child cap) must hold
        failed_workload = [c["check"] for c in checks.failed()]
        if failed_workload:
            invalid.append(f"mandatory workload checks failed: {failed_workload}")
        result["gate"] = {"limit_bytes": GATE_BYTES, "valid": not invalid, "invalid_reasons": invalid,
                          "passed": not invalid and rep["peak_bytes"] <= GATE_BYTES}
        checks.check("sampler valid: samples taken, no ps errors, no lost required roots, workload complete", not invalid, "; ".join(invalid))
        checks.check("sampled xMustard-owned tree peak <= 100 MB (100,000,000 bytes)", result["gate"]["passed"],
                     f"{rep['peak_bytes'] / 1e6:.1f} MB ({rep['peak_bytes'] / 2**20:.1f} MiB) over {rep['samples']} samples at '{rep['peak_step']}'"
                     + ("" if not invalid else " [INVALID]"))
    else:
        result["gate"] = {"limit_bytes": GATE_BYTES, "valid": False, "invalid_reasons": ["sampler never started"], "passed": False}
        checks.check("sampler valid", False, "sampler never started")
    result["steps"], result["work"] = steps, works
    result["unmeasured"] = ["hardware memory bandwidth", "Rust core allocations", "MCP shim allocations",
                            "byte copies per stage", "per-step CPU (only whole-run CPU totals are measured)"]
    s = checks.summary()
    s["results"] = result
    summary = {k: result.get(k) for k in ("gate", "admission", "fixture")}
    print(json.dumps(summary, indent=1)[:4000])
    if args.report:
        with open(args.report, "w") as f:
            json.dump(s, f, indent=1)
    finish(checks)


if __name__ == "__main__":
    main()
