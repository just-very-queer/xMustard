#!/usr/bin/env python3
"""ws35_impact_rss.py BIN REPO OUT.jsonl [--rounds N] [--load sequential|overlapping]

WS-35 measurements on a real repository (PAR-IMP-02 and the WS-14 swap/overlap check
with impact v2 on the resident path):

1. Clones REPO (a local Git repository, at HEAD) into a scratch directory and builds its
   code index with BIN (`xmustard-core index build --no-cache`), recording the build's
   peak RSS (wait4).
2. Reads the generation's graph segment: bytes per symbol, and each section's bytes
   (the footer names them).
3. Starts `BIN serve --max-inflight=4 --trim-idle-ms=0` (MALLOC_ARENA_MAX=1, as the Go
   supervisor starts the resident worker) and sends rounds of impact v2 (depth 3),
   trace and search for names taken from the index. ps-RSS is sampled every 50 ms.
   `base` is the service after `initialize`; `steady` after the first rounds.
4. Edits one source file, runs `index update` (one-shot) while the rounds continue, and
   records the peak while the new generation is swapped in and the RSS after it.

One JSON line per run goes to OUT.jsonl. Linux and macOS (ps). Nothing is written to
REPO.
"""
import argparse
import json
import os
import shutil
import sqlite3
import statistics
import subprocess
import sys
import tempfile
import threading
import time

SECTIONS = [
    "FileRows", "PathOff", "PathHeap", "SymRows", "NameOff", "NameHeap", "NamePostOff",
    "NamePost", "FileSymOff", "FileSyms", "OutOff", "Out", "InOff", "In", "FileOutOff",
    "FileOut", "FileInOff", "FileIn", "NameDropOff", "NameDrops", "FileDropOff", "FileDrops",
]
MIB = 1 << 20


def impact_count_median(answers):
    counts = [
        answer["impacted_count"]
        for answer in answers
        if isinstance(answer.get("impacted_count"), (int, float))
    ]
    return statistics.median(counts) if counts else None


def rss(pid):
    out = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True).stdout
    return int(out.strip() or 0) * 1024


def run_json(args, env=None):
    out = subprocess.run(args, capture_output=True, text=True, env=env)
    if out.returncode != 0:
        sys.exit(f"{args}: exit {out.returncode}: {out.stderr[-2000:]}")
    return json.loads(out.stdout)


def segment_report(index_path):
    d = os.path.dirname(index_path)
    conn = sqlite3.connect(f"file:{index_path}?mode=ro", uri=True)
    name = conn.execute("SELECT value FROM meta WHERE key = 'graph_segment'").fetchone()[0]
    path = os.path.join(d, name)
    size = os.path.getsize(path)
    with open(path, "rb") as f:
        f.seek(size - 12)
        tail = f.read(12)
        flen = int.from_bytes(tail[:4], "little")
        f.seek(size - 12 - flen)
        footer = json.loads(f.read(flen))
    counts = footer["counts"]
    sections = {SECTIONS[i]: rng[1] for i, rng in enumerate(footer["sections"])}
    return conn, {
        "segment_bytes": size,
        "format_version": footer["version"],
        "counts": counts,
        "bytes_per_symbol": round(size / max(counts["symbols"], 1), 1),
        "bytes_per_global_symbol": round(size / max(counts["global_symbols"], 1), 1),
        "sections": sections,
    }


def probe_names(conn, n):
    rows = conn.execute(
        "SELECT n.name FROM symbols s JOIN names n ON n.id = s.name_id "
        "JOIN files f ON f.id = s.file_id WHERE s.local = 0 AND length(n.name) >= 4 "
        "AND f.lang IN ('typescript', 'tsx', 'javascript', 'go') ORDER BY s.id"
    ).fetchall()
    step = max(len(rows) // n, 1)
    return [r[0] for r in rows[::step][:n]]


class Serve:
    def __init__(self, binp):
        env = dict(os.environ, MALLOC_ARENA_MAX="1")
        self.p = subprocess.Popen([binp, "serve", "--max-inflight=4", "--trim-idle-ms=0"],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env)
        self.next = 0
        self.lock = threading.Lock()

    def send(self, method, args):
        self.next += 1
        body = json.dumps({"jsonrpc": "2.0", "id": self.next, "method": method,
                           "params": {"args": args}}).encode()
        self.p.stdin.write(b"Content-Length: %d\r\nXmustard-Id: %d\r\n\r\n" % (len(body), self.next) + body)
        self.p.stdin.flush()
        return self.next

    def read(self):
        length = None
        while True:
            line = self.p.stdout.readline()
            if not line:
                raise EOFError("serve closed its output")
            line = line.strip()
            if not line:
                if length is not None:
                    break
                continue
            k, _, v = line.decode().partition(":")
            if k.lower() == "content-length":
                length = int(v)
        msg = json.loads(self.p.stdout.read(length))
        if "id" not in msg:  # a watcher notification
            return self.read()
        return msg

    def calls(self, calls, overlapping):
        """Answers of `calls` [(method, args)], each timed; overlapping sends a batch of
        four before reading."""
        out, times = [], []
        batch = 4 if overlapping else 1
        for i in range(0, len(calls), batch):
            t0 = time.perf_counter()
            ids = [self.send(m, a) for m, a in calls[i:i + batch]]
            for _ in ids:
                msg = self.read()
                if "error" in msg:
                    sys.exit(f"serve error: {msg['error']}")
                out.append(msg["result"])
            times.append((time.perf_counter() - t0) * 1000 / len(ids))
        return out, times

    def close(self):
        self.p.kill()
        self.p.wait()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("bin")
    ap.add_argument("repo")
    ap.add_argument("out")
    ap.add_argument("--rounds", type=int, default=3)
    ap.add_argument("--load", choices=["sequential", "overlapping"], default="sequential")
    ap.add_argument("--probes", type=int, default=40)
    args = ap.parse_args()
    binp = os.path.abspath(args.bin)
    scratch = tempfile.mkdtemp(prefix="ws35-bench-")
    try:
        work = os.path.join(scratch, "repo")
        subprocess.run(["git", "clone", "-q", "--no-local", os.path.abspath(args.repo), work], check=True)
        t0 = time.time()
        p = subprocess.Popen([binp, "index", "build", work, "--no-cache"], stdout=subprocess.PIPE)
        out = p.stdout.read()
        _, status, ru = os.wait4(p.pid, 0)
        if status != 0:
            sys.exit(f"index build failed: {status}")
        build = json.loads(out)
        build_s = round(time.time() - t0, 1)
        # ru_maxrss is KiB on Linux and bytes on macOS
        build_peak = ru.ru_maxrss * (1 if sys.platform == "darwin" else 1024)
        conn, seg = segment_report(build["index_path"])
        names = probe_names(conn, args.probes)
        edit_path = conn.execute(
            "SELECT path FROM files WHERE lang IN ('typescript', 'go') ORDER BY path LIMIT 1").fetchone()[0]
        conn.close()

        def round_calls():
            calls = []
            for i, n in enumerate(names):
                other = names[(i * 7 + 1) % len(names)]
                calls += [("symbolgraph", ["impact", work, "ws", n]),
                          ("symbolgraph", ["trace", work, "ws", n, other]),
                          ("search", [work, "ws", n, "10"])]
            return calls

        s = Serve(binp)
        s.send("initialize", [])
        s.read()
        time.sleep(0.3)
        base = rss(s.p.pid)
        peak = [0]
        stop = threading.Event()

        def sample():
            while not stop.is_set():
                peak[0] = max(peak[0], rss(s.p.pid))
                time.sleep(0.05)

        overlapping = args.load == "overlapping"
        answers, times = [], []
        for _ in range(args.rounds):
            a, t = s.calls(round_calls(), overlapping)
            answers += a
            times += t
        steady = rss(s.p.pid)
        # answers arrive in completion order when overlapping: pick impact's by shape
        impact = [a for a in answers if "impacted_count" in a]
        impact_total_p50 = impact_count_median(impact)
        # the swap: one file edited and brought in while the rounds go on
        with open(os.path.join(work, edit_path), "a") as f:
            f.write("\nexport function ws35BenchAdded(): number { return 1; }\n" if edit_path.endswith("ts")
                    else "\nfunc ws35BenchAdded() int { return 1 }\n")
        peak[0] = 0
        th = threading.Thread(target=sample)
        th.start()
        upd = subprocess.Popen([binp, "index", "update", work], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        swap_times = []
        while upd.poll() is None:
            _, t = s.calls(round_calls()[:12], overlapping)
            swap_times += t
        for _ in range(2):
            _, t = s.calls(round_calls(), overlapping)
            swap_times += t
        stop.set()
        th.join()
        after = rss(s.p.pid)
        stats = s.send("$/stats", [])
        st = s.read()["result"]
        s.close()
        rec = {
            "repo": os.path.basename(os.path.abspath(args.repo)),
            "head": subprocess.run(["git", "-C", work, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip(),
            "load": args.load,
            "build_s": build_s,
            "build_peak_mib": round(build_peak / MIB, 1),
            **seg,
            "base_mib": round(base / MIB, 1),
            "steady_above_mib": round((steady - base) / MIB, 1),
            "swap_peak_above_mib": round((max(peak[0], steady) - base) / MIB, 1),
            "after_swap_above_mib": round((after - base) / MIB, 1),
            # the code index's resident snapshots (`index`), not the legacy graph cache
            "swaps": (st.get("index") or {}).get("swaps"),
            "generations": [r.get("generation") for r in (st.get("index") or {}).get("resident", [])],
            "queries": len(answers),
            "query_ms_p50": round(statistics.median(times), 2),
            "query_ms_p95": round(sorted(times)[int(len(times) * 0.95) - 1], 2),
            "impact_total_p50": impact_total_p50,
            # absent before WS-35 (a base binary answers the legacy file-level shape)
            "impact_lower_bound_share": round(sum(a.get("epistemic", {}).get("completeness") == "lower_bound"
                                                  for a in impact) / len(impact), 2),
            "graph_kinds": sorted({a.get("graph", "files") for a in impact}),
        }
        with open(args.out, "a") as f:
            f.write(json.dumps(rec) + "\n")
        print(json.dumps(rec))
    finally:
        shutil.rmtree(scratch, ignore_errors=True)


if __name__ == "__main__":
    main()
