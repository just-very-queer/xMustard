"""Database-free diagnostics end-to-end check (scripts/e2e/local-diagnostics.sh).

Real CORE_ONLY API, real xmustard-ops CLI, real stdio MCP shim and Rust core against
temporary Git repositories, a temporary data root and a random port; no PostgreSQL
anywhere. Covers plan gates 2 and 6 (no-DB part): a fresh workspace reads
no_baseline; a CLI import while the API is running is visible on the next GET with
no restart; after an API restart the same run ID, severity, path/range/message, raw
provenance and fingerprints come back; a zero-error baseline is distinct from no
baseline; two concurrent CLI imports serialize through the workspace lock; CORE_ONLY
keeps diagnostics GET-only; MCP lists nine tools and its diagnostics tool returns the
row. It also samples the xMustard-owned process-tree RSS (API tree + CLI tree) with
`ps` while a CLI import runs concurrently with API reads.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(__file__))
from harness import Api, Checks, Mcp, REPO_ROOT, build_binaries, clean_env, finish_with, git_commit_all, run, sha256_hex  # noqa: E402

LOAD_IMPORTS = 10
NINE = {"ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"}
REPORT = {
    "uri": "file://{root}/pkg/handler.go",
    "diagnostics": [
        {"range": {"start": {"line": 3, "character": 1}, "end": {"line": 3, "character": 9}}, "severity": 1,
         "code": "XMLOCAL", "source": "e2e", "message": "xm_local_diagnostic: seeded by the CLI"},
    ],
}


class TreeSampler:
    """Samples summed RSS of process trees rooted at registered pids via `ps`."""

    def __init__(self):
        self.roots = {}
        self.peaks = {}
        self.samples = 0
        self.failures = 0
        self._stop = threading.Event()

    def register(self, label, pid):
        self.roots[label] = pid

    def _tree_rss(self, table, root):
        children = {}
        for pid, (ppid, _) in table.items():
            children.setdefault(ppid, []).append(pid)
        total, stack, seen = 0, [root], set()
        while stack:
            pid = stack.pop()
            if pid in seen or pid not in table:
                continue
            seen.add(pid)
            total += table[pid][1]
            stack.extend(children.get(pid, []))
        return total

    def _loop(self):
        while not self._stop.is_set():
            try:
                out = subprocess.run(["ps", "-A", "-o", "pid=,ppid=,rss="], capture_output=True, text=True, check=True).stdout
                table = {}
                for line in out.splitlines():
                    parts = line.split()
                    if len(parts) == 3:
                        table[int(parts[0])] = (int(parts[1]), int(parts[2]))
                total = 0
                for label, pid in list(self.roots.items()):
                    kib = self._tree_rss(table, pid)
                    total += kib
                    self.peaks[label] = max(self.peaks.get(label, 0), kib)
                self.peaks["xmustard_owned_total"] = max(self.peaks.get("xmustard_owned_total", 0), total)
                self.samples += 1
            except (OSError, subprocess.CalledProcessError, ValueError):
                self.failures += 1
            time.sleep(0.02)

    def start(self):
        threading.Thread(target=self._loop, daemon=True).start()

    def stop(self):
        self._stop.set()
        return {"method": "ps -A -o pid=,ppid=,rss= every ~20 ms, summed per process tree (KiB -> MiB)",
                "samples": self.samples, "failures": self.failures,
                "peak_mib": {k: round(v / 1024, 1) for k, v in self.peaks.items()}}


def make_repo(root):
    os.makedirs(os.path.join(root, "pkg"))
    with open(os.path.join(root, "pkg", "handler.go"), "w") as f:
        f.write("package pkg\n\nfunc Handle(n int) int {\n\treturn 10 / n\n}\n")
    git_commit_all(root)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", help="write the JSON check report here")
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()
    checks = Checks("local-diagnostics")
    tmp = tempfile.mkdtemp(prefix="xm-local-diag-")
    api = mcp = None
    extra = {}
    try:
        bin_dir = os.path.join(tmp, "bin")
        api_bin, mcp_bin, core = build_binaries(bin_dir)
        ops_bin = os.path.join(bin_dir, "xmustard-ops")
        run(["go", "build", "-o", ops_bin, "./cmd/xmustard-ops"], cwd=os.path.join(REPO_ROOT, "api-go"))
        data = os.path.join(tmp, "data")
        os.makedirs(data)
        env = {"XMUSTARD_CORE_BIN": core}
        cli_env = clean_env({**env, "XMUSTARD_DATA_DIR": data})

        def cli(*argv, check=True):
            return run([ops_bin, *argv, "--data-dir", data] if argv[0] != "workspace" else [ops_bin, *argv], env=cli_env, check=check)

        repo, empty_repo = os.path.join(tmp, "repo"), os.path.join(tmp, "repo-empty")
        make_repo(repo)
        make_repo(empty_repo)
        ws = json.loads(cli("workspace", "load", "--data-dir", data, "--root-path", repo).stdout)["workspace"]["workspace_id"]
        ws_empty = json.loads(cli("workspace", "load", "--data-dir", data, "--root-path", empty_repo).stdout)["workspace"]["workspace_id"]

        api = Api(api_bin, data, {**env, "XMUSTARD_CORE_ONLY": "1"}).start()
        st, body, _ = api.get(f"/api/workspaces/{ws}/diagnostics")
        checks.check("fresh workspace: no-DSN GET is 200 no_baseline", st == 200 and body["storage"] == {"backend": "local", "status": "no_baseline", "freshness_basis": "ingestion_identity"} and body["diagnostics"] == [] and "baseline" not in body, f"{st} {body}")
        for method, sub in (("GET", "diagnostics/status"), ("POST", "diagnostics/run"), ("GET", "diagnostics/live")):
            st, _, _ = api.request(method, f"/api/workspaces/{ws}/{sub}", {} if method == "POST" else None)
            checks.check(f"CORE_ONLY hides {method} {sub}", st == 404, f"status {st}")

        # CLI import (explicit external file) while the API serves concurrent reads.
        report_path = os.path.join(tmp, "external-report.json")
        with open(report_path, "w") as f:
            f.write(json.dumps(REPORT).replace("{root}", repo))
        raw = open(report_path, "rb").read()
        sampler = TreeSampler()
        sampler.register("api_tree", api.proc.pid)
        sampler.start()
        stop_reads = threading.Event()
        read_codes = []

        def reader():
            while not stop_reads.is_set():
                read_codes.append(api.get(f"/api/workspaces/{ws}/diagnostics")[0])

        readers = [threading.Thread(target=reader, daemon=True) for _ in range(4)]
        for t in readers:
            t.start()
        # Load phase: max-row (2,500) CLI imports, sequential, each concurrent with reads.
        big_path = os.path.join(tmp, "max-rows.json")
        with open(big_path, "w") as f:
            json.dump([{"path": "pkg/handler.go", "message": f"xm_load_{i}: synthetic", "severity": 2,
                        "range": {"start": {"line": 3, "character": 1}, "end": {"line": 3, "character": 9}}} for i in range(2500)], f)
        load_rc = []
        for _ in range(LOAD_IMPORTS):
            p = subprocess.Popen([ops_bin, "diagnostics", "run", ws, "--data-dir", data, "--input-path", big_path,
                                  "--source-kind", "compiler", "--source-name", "load"], env=cli_env,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
            sampler.register("cli_tree", p.pid)
            p.wait(180)
            load_rc.append(p.returncode)
        checks.check(f"{LOAD_IMPORTS} max-row (2,500) CLI imports succeed under concurrent reads", all(c == 0 for c in load_rc), str(load_rc))
        proc = subprocess.Popen([ops_bin, "diagnostics", "run", ws, "--data-dir", data, "--input-path", report_path,
                                 "--source-kind", "lsp", "--source-name", "gopls-e2e"], env=cli_env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        sampler.register("cli_tree", proc.pid)
        out, err = proc.communicate(timeout=180)
        stop_reads.set()
        for t in readers:
            t.join(10)
        resources = sampler.stop()
        extra["resources"] = {**resources, "workload": f"{LOAD_IMPORTS} sequential max-row (2,500-row) CLI imports plus one seeded 1-row import (real Rust normalize+archive), concurrent with 4 threads looping GET /diagnostics against the CORE_ONLY API", "api_reads": len(read_codes)}
        checks.check("CLI import succeeds with local storage", proc.returncode == 0 and '"storage_backend":"local"' in out, (err or out)[-300:])
        checks.check("concurrent API reads all succeeded", read_codes and all(c == 200 for c in read_codes), f"{len(read_codes)} reads, codes {sorted(set(read_codes))}")
        imported = json.loads(out)["baseline"]

        st, body, _ = api.get(f"/api/workspaces/{ws}/diagnostics")
        row = (body.get("diagnostics") or [{}])[0]
        checks.check("next GET sees the CLI row without restart", st == 200 and body["baseline"]["diagnostic_run_id"] == imported["diagnostic_run_id"] and "xm_local_diagnostic" in row.get("message", ""), f"{st}")
        checks.check("local row: symbols unavailable, no invented links", row.get("link_status") == "symbols_unavailable" and "linked_symbol" not in row and "semantic_baseline" not in body["baseline"])
        checks.check("raw provenance is the exact CLI input", body["baseline"]["replay_archive"]["raw_payload_sha256"] == sha256_hex(raw) and body["baseline"]["replay_archive"]["raw_payload_bytes"] == len(raw))
        checks.check("status labels: available with matched identity, source_revision unknown", body["storage"]["status"] == "available" and body["baseline"]["ingestion_identity"]["status"] == "matched" and row.get("path") == "pkg/handler.go" and body["baseline"]["source_revision"] == "unknown" and body["baseline"]["freshness_basis"] == "ingestion_identity", json.dumps(body["storage"]))
        before = {k: row.get(k) for k in ("diagnostic_run_id", "severity", "path", "range_start_line", "range_start_column", "range_end_line", "range_end_column", "message", "fingerprint")}
        before["raw_sha"] = body["baseline"]["replay_archive"]["raw_payload_sha256"]

        api.stop()
        api = Api(api_bin, data, {**env, "XMUSTARD_CORE_ONLY": "1"}).start()
        st, body, _ = api.get(f"/api/workspaces/{ws}/diagnostics")
        row = (body.get("diagnostics") or [{}])[0]
        after = {k: row.get(k) for k in before if k != "raw_sha"}
        after["raw_sha"] = (body.get("baseline") or {}).get("replay_archive", {}).get("raw_payload_sha256")
        checks.check("after restart: same run, severity, path/range/message, fingerprint, raw sha", st == 200 and after == before, json.dumps(after))

        empty_path = os.path.join(tmp, "empty.json")
        with open(empty_path, "w") as f:
            f.write("[]")
        r = cli("diagnostics", "run", ws_empty, "--input-path", empty_path, "--source-kind", "compiler", "--source-name", "e2e", check=False)
        st, body, _ = api.get(f"/api/workspaces/{ws_empty}/diagnostics")
        checks.check("zero-error baseline is present and distinct from no_baseline", r.returncode == 0 and st == 200 and body.get("baseline") and body["diagnostics"] == [] and body["storage"]["status"] == "available", f"{st} rc={r.returncode} {r.stdout[-300:]} {r.stderr[-300:]} {json.dumps(body)[:300]}")

        procs = [subprocess.Popen([ops_bin, "diagnostics", "run", ws, "--data-dir", data, "--input-path", report_path, "--source-name", f"race-{i}"],
                                  env=cli_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) for i in range(2)]
        results = [(p.wait(180), p.stdout.read(), p.stderr.read()) for p in procs]
        ok_ids = [json.loads(o)["baseline"]["diagnostic_run_id"] for code, o, _ in results if code == 0]
        busy = [e for code, _, e in results if code != 0]
        st, body, _ = api.get(f"/api/workspaces/{ws}/diagnostics")
        checks.check("two concurrent CLI imports: each publishes or reports busy; latest is a complete published run",
                     st == 200 and ok_ids and body["baseline"]["diagnostic_run_id"] in ok_ids and all("overloaded" in e for e in busy),
                     f"published {len(ok_ids)}, busy {len(busy)}")

        mcp = Mcp(mcp_bin, api.base).start()
        mcp.call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}})
        tools = {t["name"] for t in mcp.call("tools/list")["result"]["tools"]}
        checks.check("MCP exposes exactly the nine tools", tools == NINE, str(sorted(tools)))
        res = mcp.tool("diagnostics", {"workspace_id": ws})
        text = res.get("result", {}).get("content", [{}])[0].get("text", "")
        checks.check("MCP diagnostics tool returns the local row", not res.get("result", {}).get("isError") and "xm_local_diagnostic" in text, text[:200])
    except Exception as exc:  # report, then fail
        checks.check("harness completed", False, repr(exc))
    finally:
        if mcp:
            mcp.stop()
        if api:
            api.stop()
        if args.keep:
            print(f"artifacts kept in {tmp}")
        else:
            shutil.rmtree(tmp, ignore_errors=True)
    finish_with(checks, args.report, extra)


if __name__ == "__main__":
    main()
