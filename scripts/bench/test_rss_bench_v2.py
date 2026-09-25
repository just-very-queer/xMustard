"""Unit tests for the budget gate v2 (scripts/bench/rss_bench_v2.py). Stdlib only.

    python3 -m unittest discover -s scripts/bench -p 'test_*.py'

The live check that v2 reproduces v1 on the 501-file workload runs the real API and core
for about three minutes; it is skipped unless XMUSTARD_BENCH_LIVE=1.
"""

import copy
import http.server
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import rss_bench as v1  # noqa: E402
import rss_bench_v2 as v2  # noqa: E402

REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
LEDGER = v2.load_json(v2.LEDGER_PATH)
FIXTURES = v2.load_json(v2.FIXTURES_PATH)
REGISTRY = v2.RoleRegistry(LEDGER["process_roles"])
HARNESS = 900


def ps(*rows):
    """Synthetic `ps -axo pid=,ppid=,rss=,comm=` text."""
    return "\n".join(f"{pid:>6} {ppid:>6} {rss:>8} {comm}" for pid, ppid, rss, comm in rows) + "\n"


class FakeProbe:
    name = "fake"
    basis = {"footprint": "fake footprint", "split": "fake split", "process_peak": "fake peak", "io": "fake io"}

    def __init__(self, stats=None, argv=None):
        self._stats, self._argv = stats or {}, argv or {}

    def stats(self, pid):
        return self._stats.get(pid)

    def argv1(self, pid):
        return self._argv.get(pid)


def roots(api=100, shims=(200,), agent=HARNESS):
    r = [v2.Root("xmustard-api", api, "go_daemon", "owned")]
    r += [v2.Root(f"xmustard-mcp#{i + 1}", p, "mcp_access", "owned") for i, p in enumerate(shims)]
    if agent:
        r.append(v2.Root("bench-harness", agent, "agent:bench-harness", "agent"))
    return r


class ParseAndAttribution(unittest.TestCase):
    def test_parse_ps_like_v1(self):
        procs, children = v2.parse_ps(ps((1, 0, 10, "/sbin/launchd"), (100, 1, 2048, "/tmp/bin/xmustard-api")))
        self.assertEqual(procs[100], v2.Proc(100, 1, 2048, "xmustard-api"))
        self.assertEqual(children[1], [100])
        with self.assertRaises(ValueError):
            v2.parse_ps("garbage line\n")

    def test_owned_children_are_classified_by_role(self):
        text = ps((HARNESS, 1, 30000, "python3"), (100, HARNESS, 20000, "/b/xmustard-api"), (200, HARNESS, 9000, "/b/xmustard-mcp"),
                  (101, 100, 15000, "/b/xmustard-core"), (102, 100, 4000, "/b/xmustard-core"), (103, 100, 3000, "/usr/bin/git"),
                  (104, 100, 1000, "/opt/x/strange-helper"), (105, 100, 22000, "(xmustard-core)"))
        procs, children = v2.parse_ps(text)
        missing, rows = v2.attribute(procs, children, roots(), REGISTRY, lambda pid: {102: "serve"}.get(pid))
        self.assertEqual(missing, [])
        self.assertEqual(rows[100][:2], ("go_daemon", "owned"))
        self.assertEqual(rows[200][:2], ("mcp_access", "owned"))
        self.assertEqual(rows[101][:2], ("rust_core_per_call", "owned"))
        self.assertEqual(rows[102][:2], ("rust_index_service", "owned"))
        self.assertEqual(rows[103][:2], ("git_child", "owned"))
        self.assertEqual(rows[104][:2], ("unclassified:strange-helper", "owned"))  # unknown counts, never silently dropped
        self.assertEqual(rows[105][:2], ("rust_core_per_call", "owned"))  # exited-but-unreaped macOS process
        self.assertEqual(rows[HARNESS][1], "agent")
        tot = v2.snapshot_totals(procs, rows)
        self.assertEqual(tot["gate_kib"], 20000 + 9000 + 15000 + 4000 + 3000 + 1000 + 22000)
        self.assertEqual(tot["agents_kib"], {"bench-harness": 30000})

    def test_externals_are_separate_and_inherit_downward(self):
        text = ps((100, 1, 20000, "xmustard-api"), (110, 100, 1000, "/bin/sh"), (111, 110, 50000, "/usr/local/go/bin/go"),
                  (112, 111, 70000, "gopls"), (113, 110, 3000, "strange-tool"), (114, 100, 80000, "typescript-lang"))
        procs, children = v2.parse_ps(text)
        _, rows = v2.attribute(procs, children, roots(shims=(), agent=None), REGISTRY)
        self.assertEqual(rows[110][:2], ("runner_command", "external"))
        self.assertEqual(rows[111][:2], ("runner_command", "external"))
        self.assertEqual(rows[112][:2], ("lsp_server", "external"))  # the more specific external name is kept
        self.assertEqual(rows[113][:2], ("runner_command", "external"))  # below an external stays external
        self.assertEqual(rows[114][:2], ("lsp_server", "external"))  # Linux 15-byte comm truncation
        tot = v2.snapshot_totals(procs, rows)
        self.assertEqual(tot["gate_kib"], 20000)
        self.assertEqual(tot["tree_all_kib"], 20000 + 1000 + 50000 + 70000 + 3000 + 80000)
        self.assertEqual(tot["externals_kib"], {"runner_command": 54000, "lsp_server": 150000})

    def test_agent_launched_xmustard_binaries_count_but_the_agent_does_not(self):
        text = ps((HARNESS, 1, 30000, "python3"), (100, HARNESS, 20000, "xmustard-api"), (300, HARNESS, 800, "/bin/ps"),
                  (301, HARNESS, 2000, "/x/xmustard-hook"), (302, 301, 900, "git"), (303, HARNESS, 90000, "go"),
                  (500, 1, 99999, "xmustard-api"))  # another session's API: not ours
        procs, children = v2.parse_ps(text)
        _, rows = v2.attribute(procs, children, roots(shims=()), REGISTRY)
        self.assertEqual(rows[301][:2], ("hook_client", "owned"))
        self.assertEqual(rows[302][:2], ("git_child", "owned"))
        for pid in (300, 303, 500):
            self.assertNotIn(pid, rows)
        self.assertEqual(v2.snapshot_totals(procs, rows)["gate_kib"], 20000 + 2000 + 900)

    def test_missing_root_is_reported(self):
        procs, children = v2.parse_ps(ps((100, 1, 20000, "xmustard-api")))
        missing, _ = v2.attribute(procs, children, roots(shims=(200,), agent=None), REGISTRY)
        self.assertEqual(missing, ["xmustard-mcp#1"])


class V1Equivalence(unittest.TestCase):
    """v2's gate total is v1's total on the same snapshots (no externals present)."""

    SNAPSHOTS = [
        ps((1, 0, 5, "launchd"), (100, 1, 20000, "xmustard-api"), (200, 1, 9000, "xmustard-mcp"), (201, 1, 9100, "xmustard-mcp")),
        ps((1, 0, 5, "launchd"), (100, 1, 22000, "xmustard-api"), (200, 1, 9000, "xmustard-mcp"), (201, 1, 9100, "xmustard-mcp"),
           (101, 100, 21000, "xmustard-core"), (102, 100, 12000, "xmustard-core"), (103, 101, 700, "git")),
        ps((1, 0, 5, "launchd"), (100, 1, 30000, "xmustard-api"), (200, 1, 11000, "xmustard-mcp"), (201, 1, 9100, "xmustard-mcp"),
           (101, 100, 22448, "(xmustard-core)")),
        ps((1, 0, 5, "launchd"), (100, 1, 24000, "xmustard-api"), (201, 1, 9100, "xmustard-mcp")),  # a root lost
        "not a ps line\n",
    ]

    def test_same_peak_steps_and_validity_as_v1(self):
        steps = ["idle", "index", "capture", "lost", "broken"]
        v1roots = {"xmustard-api": 100, "xmustard-mcp#1": 200, "xmustard-mcp#2": 201}
        old = v1.Sampler(v1roots)
        new = v2.SamplerV2([v2.Root(k, p, "go_daemon" if "api" in k else "mcp_access", "owned") for k, p in v1roots.items()],
                           REGISTRY, FakeProbe())
        real_run = v1.subprocess.run
        try:
            for i, (text, step) in enumerate(zip(self.SNAPSHOTS, steps)):
                v1.CURRENT_STEP[0] = step
                v1.subprocess.run = lambda *a, _t=text, **k: subprocess.CompletedProcess(a, 0, _t, "")
                old.sample_once()
                new.ingest(1000.0 + i / 10, step, text)
        finally:
            v1.subprocess.run = real_run
            v1.CURRENT_STEP[0] = "setup"
        o, n = old.report(), new.report()
        self.assertEqual(o["peak_bytes"], n["gate"]["peak_bytes"])
        self.assertEqual(round(o["peak_bytes"] / 2**20, 1), n["tree_all_peak_mib"])
        self.assertEqual(o["peak_step"], n["gate"]["peak_step"])
        self.assertEqual(o["samples"], n["samples"])
        self.assertEqual(o["lost_root_count"], n["lost_root_count"])
        self.assertEqual(o["ps_error_count"], n["ps_error_count"])
        self.assertEqual(len(old.validity()), len(new.validity()))


class SamplerMetrics(unittest.TestCase):
    def test_footprint_split_components_and_externals(self):
        mb = 2**20
        probe = FakeProbe(stats={
            100: {"footprint_bytes": 12 * mb, "lifetime_max_footprint_bytes": 30 * mb, "hwm_bytes": 25 * mb, "io_read_bytes": 5000,
                  "anon_bytes": 14 * mb, "file_bytes": 6 * mb},
            101: {"footprint_bytes": 4 * mb, "lifetime_max_footprint_bytes": 9 * mb, "anon_bytes": 10 * mb, "file_bytes": 2 * mb},
            110: {"footprint_bytes": 60 * mb, "anon_bytes": 50 * mb, "file_bytes": 30 * mb}})
        s = v2.SamplerV2(roots(shims=(), agent=None), REGISTRY, probe)
        s.ingest(1.0, "idle", ps((100, 1, 20480, "xmustard-api")))
        s.ingest(1.1, "work", ps((100, 1, 20480, "xmustard-api"), (101, 100, 12288, "xmustard-core"), (110, 100, 81920, "gopls")))
        s.ingest(1.2, "idle", ps((100, 1, 18432, "xmustard-api")))
        r = s.report()
        self.assertEqual(r["gate"]["peak_mib"], 32.0)  # 20 + 12; the 80 MiB LSP server is external
        self.assertEqual(r["gate"]["peak_step"], "work")
        self.assertEqual(r["tree_all_peak_mib"], 112.0)
        self.assertEqual(r["footprint"]["tree_peak_mib"], 16.0)
        self.assertEqual(r["split_at_gate_peak"]["anon_mib"], 24.0)
        self.assertEqual(r["split_at_gate_peak"]["file_mib"], 8.0)
        self.assertTrue(r["split_at_gate_peak"]["complete"])
        self.assertEqual(r["externals"]["lsp_server"]["peak_mib"], 80.0)
        self.assertEqual(r["externals"]["lsp_server"]["footprint_peak_mib"], 60.0)
        self.assertNotIn("lsp_server", r["components"])
        d = r["components"]["go_daemon"]
        self.assertEqual((d["peak_mib"], d["p50_mib"], d["process_rss_peak_mib"], d["process_footprint_peak_mib"], d["io_read_bytes"]),
                         (20.0, 20.0, 25.0, 30.0, 5000))
        c = r["components"]["rust_core_per_call"]
        self.assertEqual((c["peak_mib"], c["p50_mib"], c["at_gate_peak_mib"], c["processes_seen"]), (12.0, 0.0, 12.0, 1))
        self.assertEqual({p["pid"] for p in r["processes_at_gate_peak"]}, {100, 101, 110})
        self.assertEqual(s.validity(), [])

    def test_missing_probe_data_marks_footprint_partial(self):
        s = v2.SamplerV2(roots(shims=(), agent=None), REGISTRY, FakeProbe(stats={}))
        s.ingest(1.0, "x", ps((100, 1, 2048, "xmustard-api"), (101, 100, 0, "<defunct>")))
        r = s.report()
        self.assertEqual(r["components"]["defunct"]["peak_mib"], 0.0)
        self.assertEqual(r["footprint"]["partial_samples"], 1)
        self.assertIsNone(r["footprint"]["tree_peak_mib"])
        self.assertFalse(r["split_at_gate_peak"]["complete"])

    def test_zero_samples_is_invalid(self):
        s = v2.SamplerV2(roots(), REGISTRY, FakeProbe())
        self.assertIn("zero samples", s.validity())


class GateVerdict(unittest.TestCase):
    def rep(self, peak):
        return {"gate": {"peak_bytes": peak}}

    def test_boundary_and_invalidity(self):
        self.assertEqual(v2.evaluate_gate(self.rep(100_000_000), [])["verdict"], "PASS")
        self.assertEqual(v2.evaluate_gate(self.rep(100_000_001), [])["verdict"], "FAIL")
        low = v2.evaluate_gate(self.rep(10), ["workload incomplete"])
        self.assertEqual(low["verdict"], "INVALID")
        self.assertFalse(low["passed"])
        self.assertEqual(v2.evaluate_gate(self.rep(1), [])["limit_mib"], 95.4)


class PlatformDecoders(unittest.TestCase):
    def test_rusage_v4(self):
        vals = [0] * v2.RUSAGE_V4_FIELDS
        vals[6], vals[7], vals[16], vals[17], vals[28] = 111, 222, 333, 444, 555
        buf = b"\0" * 16 + struct.pack(f"<{v2.RUSAGE_V4_FIELDS}Q", *vals) + b"\0" * 64
        self.assertEqual(v2.unpack_rusage_v4(buf), {"resident_bytes": 111, "footprint_bytes": 222, "io_read_bytes": 333,
                                                    "io_written_bytes": 444, "lifetime_max_footprint_bytes": 555})

    def test_task_vm_info(self):
        vals = [0] * 23
        vals[0], vals[1], vals[2] = 1 << 40, 42, 16384  # virtual size, regions, page size
        vals[3], vals[4] = 20 << 20, 31 << 20  # resident, resident peak
        vals[7], vals[8], vals[9], vals[10] = 15 << 20, 22 << 20, 5 << 20, 9 << 20  # internal(+peak), external(+peak)
        vals[16], vals[19], vals[22] = 1 << 20, 18 << 20, 40 << 20  # compressed, phys_footprint, ledger footprint peak
        raw = struct.pack(v2.TASK_VM_INFO_STRUCT, *vals) + b"\0" * 128
        st = v2.unpack_task_vm_info(raw)
        self.assertEqual(st["anon_bytes"] + st["file_bytes"], st["resident_bytes"])
        self.assertEqual((st["hwm_bytes"], st["compressed_bytes"], st["footprint_bytes"]), (31 << 20, 1 << 20, 18 << 20))
        self.assertEqual(struct.calcsize(v2.TASK_VM_INFO_STRUCT), 176)

    def test_linux_proc_parsers(self):
        status = "Name:\txmustard-api\nVmHWM:\t   30000 kB\nVmRSS:\t   20000 kB\nRssAnon:\t   15000 kB\nRssFile:\t    4500 kB\nRssShmem:\t     500 kB\n"
        rollup = ("55d0-7ff [rollup]\nRss:              20000 kB\nPss:              17000 kB\nPss_Anon:         15000 kB\n"
                  "Pss_File:          2000 kB\nPrivate_Clean:      800 kB\nPrivate_Dirty:    15200 kB\nSwap: 0 kB\n")
        io = "rchar: 1000\nwchar: 2000\nsyscr: 5\nread_bytes: 4096\nwrite_bytes: 8192\n"
        st = v2.linux_stats(status, rollup, io)
        self.assertEqual(st["resident_bytes"], 20000 * 1024)
        self.assertEqual(st["footprint_bytes"], 17000 * 1024)
        self.assertEqual(st["uss_bytes"], 16000 * 1024)
        self.assertEqual((st["anon_bytes"], st["file_bytes"], st["shmem_bytes"]), (15000 * 1024, 4500 * 1024, 500 * 1024))
        self.assertEqual(st["hwm_bytes"], 30000 * 1024)
        self.assertEqual((st["io_read_bytes"], st["io_written_bytes"], st["io_rchar"]), (4096, 8192, 1000))
        self.assertIsNone(v2.linux_stats(status, None, None).get("footprint_bytes"))

    def test_split_reconciles_with_rss(self):
        s = v2.SamplerV2(roots(shims=(), agent=None), REGISTRY,
                         FakeProbe(stats={100: {"footprint_bytes": 1, "anon_bytes": 2 * 2**20, "file_bytes": 2**20, "shmem_bytes": 2**20}}))
        s.ingest(1.0, "x", ps((100, 1, 4096, "xmustard-api")))
        sp = s.report()["split_at_gate_peak"]
        self.assertEqual((sp["anon_mib"], sp["file_mib"], sp["shmem_mib"], sp["complete"]), (2.0, 1.0, 1.0, True))

    def test_procargs2(self):
        raw = struct.pack("<i", 3) + b"/b/xmustard-core\0\0\0\0xmustard-core\0serve\0--x\0HOME=/h\0"
        self.assertEqual(v2.parse_procargs2(raw), "serve")
        self.assertIsNone(v2.parse_procargs2(struct.pack("<i", 1) + b"/b/x\0\0x\0"))

    @unittest.skipUnless(sys.platform == "darwin", "macOS probe")
    def test_darwin_probe_reads_a_live_child(self):
        p = subprocess.Popen(["sleep", "5"])
        time.sleep(0.5)  # let exec settle so RSS is stable between the two reads
        try:
            probe = v2.DarwinProbe()
            st = probe.stats(p.pid)
            self.assertGreater(st["footprint_bytes"], 0)
            self.assertGreaterEqual(st["lifetime_max_footprint_bytes"], st["footprint_bytes"])
            self.assertEqual(st["anon_bytes"] + st["file_bytes"], st["resident_bytes"])
            self.assertGreaterEqual(st["hwm_bytes"], st["resident_bytes"])
            out = subprocess.run(["ps", "-o", "rss=", "-p", str(p.pid)], capture_output=True, text=True).stdout
            self.assertEqual(int(out) * 1024, st["resident_bytes"])  # the split reconciles with ps RSS
        finally:
            p.kill()
            p.wait()

    @unittest.skipUnless(sys.platform.startswith("linux"), "Linux probe")
    def test_linux_probe_reads_a_live_child(self):
        p = subprocess.Popen(["sleep", "5"])
        try:
            st = v2.LinuxProbe().stats(p.pid)
            self.assertGreater(st["resident_bytes"], 0)
            self.assertIsNotNone(st["anon_bytes"])
        finally:
            p.kill()
            p.wait()


class Accounting(unittest.TestCase):
    def test_result_accounting(self):
        body = json.dumps({"hits": [], "coverage": {"work": {"files_parsed": 3, "bytes_parsed": 900, "graph_cache": "miss"}}})
        r = v2.result_accounting({"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": body}], "_meta": {"usage": {"x": 1}}}})
        self.assertEqual(r["content_bytes"], len(body))
        self.assertEqual(r["est_tokens"], -(-len(body) // 4))
        self.assertEqual(r["work"], {"files_parsed": 3, "bytes_parsed": 900})
        self.assertEqual(r["meta_usage"], {"x": 1})
        self.assertIsNone(r["error"])
        self.assertTrue(v2.result_accounting({"result": {"content": [{"text": "boom"}], "isError": True}})["error"].startswith("isError"))
        self.assertIn("rpc -32000", v2.result_accounting({"error": {"code": -32000, "message": "busy"}})["error"])

    def test_usage_summary(self):
        u = v2.UsageLog()
        u.record(name="search", transport="stdio", arg_bytes=10, request_bytes=80, response_bytes=500, content_bytes=400,
                 est_tokens=100, latency_ms=20.0, work={"files_parsed": 2}, error=None)
        u.record(name="search", transport="stdio", arg_bytes=12, request_bytes=82, response_bytes=300, content_bytes=200,
                 est_tokens=50, latency_ms=40.0, work={"files_parsed": 0}, error="isError: x")
        s = u.summary()["per_tool"]["search"]
        self.assertEqual((s["calls"], s["errors"], s["arg_bytes"], s["response_bytes"], s["est_tokens"]), (2, 1, 22, 800, 150))
        self.assertEqual(s["work"], {"files_parsed": 2})
        self.assertEqual(s["latency_ms"]["max"], 40.0)

    def test_sse_parsing(self):
        body = 'event: message\ndata: {"jsonrpc":"2.0","method":"notifications/progress"}\n\nid: 7\ndata: {"jsonrpc":"2.0",\ndata: "id":3,"result":{}}\n\n'
        msgs = v2.parse_sse_messages(body)
        self.assertEqual(len(msgs), 2)
        self.assertEqual(msgs[1]["id"], 3)


class StubMcp(http.server.BaseHTTPRequestHandler):
    """A tiny Streamable HTTP MCP server: JSON for initialize, SSE for tools/call."""

    def log_message(self, *a):
        pass

    def do_POST(self):
        if self.path != "/mcp":
            self.send_response(404)
            self.end_headers()
            return
        msg = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "id" not in msg:
            self.send_response(202)
            self.end_headers()
            return
        if msg["method"] == "initialize":
            out = json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": {"protocolVersion": "2025-06-18", "capabilities": {}}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Mcp-Session-Id", "s-1")
        else:
            assert self.headers.get("Mcp-Session-Id") == "s-1"
            text = json.dumps({"coverage": {"work": {"files_parsed": 1}}, "watcher_state": "on"})
            res = {"jsonrpc": "2.0", "id": msg["id"], "result": {"content": [{"type": "text", "text": text}]}}
            out = ('data: {"jsonrpc":"2.0","method":"notifications/progress","params":{}}\n\n' + "data: " + json.dumps(res) + "\n\n").encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def do_DELETE(self):
        self.send_response(204)
        self.end_headers()


class HttpTransport(unittest.TestCase):
    def setUp(self):
        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), StubMcp)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.srv.server_address[1]}"

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()

    def test_client_session_sse_and_accounting(self):
        self.assertEqual(v2.probe_http_mcp(self.base), (True, "initialize answered"))
        u = v2.UsageLog()
        c = v2.HttpMcp(self.base, usage=u, agent="a1")
        c.call("initialize", v2.INIT_PARAMS)
        self.assertEqual((c.session, c.protocol), ("s-1", "2025-06-18"))
        r = c.tool("search", {"workspace_id": "w", "query": "q"})
        self.assertEqual(v2.tool_body(r)["watcher_state"], "on")
        c.stop()
        s = u.summary()["per_tool"]["search"]
        self.assertEqual((s["calls"], s["errors"], s["transports"], s["work"]), (1, 0, ["http"], {"files_parsed": 1}))
        self.assertGreater(s["response_bytes"], s["content_bytes"])

    def test_absent_endpoint(self):
        class NoMcp(StubMcp):
            def do_POST(self):
                self.send_response(404)
                self.send_header("Content-Length", "0")
                self.end_headers()
        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), NoMcp)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            ok, detail = v2.probe_http_mcp(f"http://127.0.0.1:{srv.server_address[1]}")
            self.assertFalse(ok)
            self.assertIn("404", detail)
        finally:
            srv.shutdown()
            srv.server_close()


class Features(unittest.TestCase):
    def test_detection_and_presence(self):
        f = v2.FeatureState(FIXTURES["feature_probes"])
        f.observe({"coverage": {"truncated": True, "indexed_files": 800, "eligible_files": 2632, "max_files": 800},
                   "freshness": {"watcher_state": "off"}})
        self.assertFalse(v2.feature_present("uncapped_index", f.values["uncapped_index"]))
        self.assertFalse(v2.feature_present("watcher", f.values["watcher"]))
        self.assertFalse(v2.feature_present("static_embeddings", f.values.get("static_embeddings")))
        g = v2.FeatureState(FIXTURES["feature_probes"])
        g.observe({"coverage": {"truncated": False}, "watcher_state": "watching", "semantic_lane": {"kind": "static"}})
        for feat in ("uncapped_index", "watcher", "static_embeddings"):
            self.assertTrue(v2.feature_present(feat, g.values[feat]), feat)

    def test_parity_claim_needs_every_scenario_and_feature(self):
        good = {"http_mcp": True, "watcher": "on", "static_embeddings": {"kind": "static"}, "uncapped_index": {"truncated": False}}
        gate = {"passed": True, "verdict": "PASS"}
        scen = {n: {"status": "ran", "gate": dict(gate), "features": dict(good)} for n in v2.SCENARIOS}
        rep = {"scenarios": scen, "grammars": {"count": 17, "required": 17}}
        self.assertEqual(v2.parity_claim(rep, FIXTURES), {"established": True, "missing": []})
        rep2 = copy.deepcopy(rep)
        rep2["scenarios"]["watcher-on"] = {"status": "skipped", "reason": "feature absent"}
        rep2["scenarios"]["agents-2"]["features"]["http_mcp"] = False
        rep2["grammars"]["count"] = 4
        claim = v2.parity_claim(rep2, FIXTURES)
        self.assertFalse(claim["established"])
        text = " ".join(claim["missing"])
        for needle in ("watcher-on: skipped", "http_mcp: absent in 1", "grammars: 4 of 17"):
            self.assertIn(needle, text)


class Ledger(unittest.TestCase):
    def test_real_ledger_is_consistent(self):
        procs = {c["process"] for c in LEDGER["components"].values() if c["process"]}
        roles = {r["component"] for r in LEDGER["process_roles"]["rules"]}
        self.assertEqual(LEDGER["gate"]["limit_bytes"], v2.GATE_BYTES)
        self.assertEqual(sorted(LEDGER["workstreams"]), [f"WS-{i:02d}" for i in range(64)])
        for wid, w in LEDGER["workstreams"].items():
            self.assertTrue(w["process"] is None or w["process"] in procs | roles, wid)
        for p in procs:
            self.assertIn(p, roles | {"go_daemon"}, p)
        for k in ("tree_peak_mib", "component_p50_mib", "v1_crosscheck_mib"):
            self.assertIsInstance(LEDGER["tolerance"][k], (int, float), k)

    def test_reconcile_reports_the_critic_overcommit(self):
        rec = v2.ledger_reconcile(LEDGER)
        by = {r["process"]: r for r in rec["per_process"]}
        self.assertGreater(by["rust_index_service"]["workstream_lines_mib"], by["rust_index_service"]["design_steady_mib"])
        self.assertTrue(rec["fits_7_2_method"])
        self.assertFalse(rec["fits_with_query_overlap"])
        self.assertIn("WS-55", rec["workstreams_without_a_line"])

    def view(self, peak, daemon):
        return {"v1-workload": {"gate_peak_mib": peak, "components_p50_mib": {"go_daemon": daemon, "mcp_access": 20.0}}}

    def test_check_enforces_lines_and_savings(self):
        tol = LEDGER["tolerance"]
        base = self.view(80.0, 20.0)
        ok = v2.ledger_check(LEDGER, "WS-01", self.view(80.0 + 3 + tol["tree_peak_mib"], 20.0 + 3), base)
        self.assertTrue(ok["passed"], ok)
        over = v2.ledger_check(LEDGER, "WS-01", self.view(80.0 + 3 + tol["tree_peak_mib"] + 0.5, 23.0), base)
        self.assertFalse(over["passed"])
        daemon_over = v2.ledger_check(LEDGER, "WS-01", self.view(80.0, 20.0 + 3 + tol["component_p50_mib"] + 0.1), base)
        self.assertFalse(daemon_over["passed"])
        # WS-13 must actually save its 10 MiB
        saved = {"v1-workload": {"gate_peak_mib": 60.0, "components_p50_mib": {"mcp_access": 0.0}}}
        self.assertTrue(v2.ledger_check(LEDGER, "WS-13", saved, base)["passed"])
        self.assertFalse(v2.ledger_check(LEDGER, "WS-13", self.view(80.0, 20.0), base)["passed"])
        # unlisted work and a null line default to 0 MiB
        self.assertFalse(v2.ledger_check(LEDGER, "not-a-workstream", self.view(90.0, 20.0), base)["passed"])
        self.assertEqual(v2.ledger_check(LEDGER, "WS-55", base, base)["line_mib"], 0.0)
        # no common scenario is a failure, not a pass
        self.assertFalse(v2.ledger_check(LEDGER, "WS-01", {}, base)["passed"])

    def test_ledger_view_skips_invalid_runs(self):
        rep = {"scenarios": {
            "a": {"status": "ran", "gate": {"valid": True, "peak_mib": 50.0}, "sampler": {"components": {"go_daemon": {"p50_mib": 20.0}}}},
            "b": {"status": "ran", "gate": {"valid": False, "peak_mib": 10.0}, "sampler": {"components": {}}},
            "c": {"status": "skipped"}}}
        self.assertEqual(v2.ledger_view(rep), {"a": {"gate_peak_mib": 50.0, "components_p50_mib": {"go_daemon": 20.0}}})


def git(args, cwd):
    return subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@t.invalid", "-c", "init.defaultBranch=main", *args],
                          cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


class FixturePins(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="xm-fx-test-")
        self.up = os.path.join(self.tmp, "upstream")
        os.makedirs(os.path.join(self.up, "src"))
        os.makedirs(os.path.join(self.up, "enterprise"))
        with open(os.path.join(self.up, "src", "a.ts"), "wb") as f:
            f.write(b"export function a() { return 1; }\r\n")  # a CRLF blob must stay byte-exact
        with open(os.path.join(self.up, "enterprise", "secret.py"), "w") as f:
            f.write("x = 1\n")
        os.symlink("src/a.ts", os.path.join(self.up, "link.ts"))
        git(["init", "-q"], self.up)
        git(["add", "-A"], self.up)
        git(["commit", "-qm", "c1"], self.up)
        with open(os.path.join(self.up, ".gitattributes"), "w") as f:  # added later: the CRLF blob stays unnormalized
            f.write("* text=auto eol=lf\n")
        git(["add", ".gitattributes"], self.up)
        git(["commit", "-qm", "c2"], self.up)
        self.commit = git(["rev-parse", "HEAD"], self.up)
        self.tree = git(["rev-parse", "HEAD^{tree}"], self.up)
        self.cache = os.path.join(self.tmp, "cache")
        self.spec = {"fixtures": {"up": {"commit": self.commit, "tree": self.tree, "mirrors": [self.up], "exclude": ["enterprise/"],
                                         "workspace": {"tree": None, "files": None, "content_sha256": None},
                                         "probes": [{"search": "a", "explain": "src/a.ts", "impact": "a"}]}},
                     "composites": {"combo": {"members": ["up"], "workspace": {"tree": None, "files": None, "content_sha256": None}}}}

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_clone_verify_pin_and_detect_tampering(self):
        info = v2.ensure_workspace("up", self.spec, self.cache)
        got = info["verified"]
        self.assertEqual(got["files"], 3)  # a.ts, link.ts, .gitattributes; enterprise/ excluded
        self.assertFalse(os.path.exists(os.path.join(info["path"], "enterprise")))
        with open(os.path.join(info["path"], "src", "a.ts"), "rb") as f:
            self.assertTrue(f.read().endswith(b"\r\n"))  # no eol filter touched the bytes
        self.assertEqual(info["upstream"]["fetched_from"], self.up)
        pinned = copy.deepcopy(self.spec)
        pinned["fixtures"]["up"]["workspace"] = dict(got)
        again = v2.ensure_workspace("up", pinned, self.cache)
        self.assertEqual(again["verified"], got)
        # a tampered cached workspace is rebuilt from the pinned source, never trusted
        with open(os.path.join(again["path"], "src", "a.ts"), "a") as f:
            f.write("tampered\n")
        healed = v2.ensure_workspace("up", pinned, self.cache)
        self.assertEqual((healed["source"], healed["verified"]), ("built", got))
        # a scenario copy verifies against the same pins
        dest = v2.clone_workspace(healed["path"], os.path.join(self.tmp, "copy"))
        self.assertEqual(v2.verify_workspace(dest, got, "copy"), got)
        # wrong pins fail loudly
        bad = copy.deepcopy(pinned)
        bad["fixtures"]["up"]["workspace"]["content_sha256"] = "0" * 64
        with self.assertRaises(v2.FixtureError):
            v2.ensure_workspace("up", bad, os.path.join(self.tmp, "cache2"))
        wrong_tree = copy.deepcopy(self.spec)
        wrong_tree["fixtures"]["up"]["tree"] = "1" * 40
        with self.assertRaises(v2.FixtureError):
            v2.ensure_workspace("up", wrong_tree, os.path.join(self.tmp, "cache3"))
        missing = copy.deepcopy(self.spec)
        missing["fixtures"]["up"]["commit"] = "2" * 40
        with self.assertRaises(v2.FixtureError):
            v2.ensure_workspace("up", missing, os.path.join(self.tmp, "cache4"))
        no_probe = copy.deepcopy(self.spec)
        no_probe["fixtures"]["up"]["probes"][0]["explain"] = "src/missing.ts"
        with self.assertRaises(v2.FixtureError):
            v2.ensure_workspace("up", no_probe, os.path.join(self.tmp, "cache5"))

    def test_composite_is_deterministic_and_prefixed(self):
        a = v2.ensure_workspace("combo", self.spec, os.path.join(self.tmp, "c1"))
        b = v2.ensure_workspace("combo", self.spec, os.path.join(self.tmp, "c2"))
        self.assertEqual(a["verified"], b["verified"])
        self.assertTrue(os.path.isfile(os.path.join(a["path"], "up", "src", "a.ts")))
        self.assertTrue(os.path.islink(os.path.join(a["path"], "up", "link.ts")))

    def test_real_pins_are_complete_and_licensed(self):
        fx = FIXTURES["fixtures"]
        self.assertEqual(set(fx), {"cline", "pi-mono", "openhands"})
        self.assertNotIn("gitnexus", fx)
        for name, spec in fx.items():
            self.assertRegex(spec["commit"], r"^[0-9a-f]{40}$")
            self.assertRegex(spec["tree"], r"^[0-9a-f]{40}$")
            self.assertTrue(spec["license"].startswith(("Apache-2.0", "MIT")), name)
            self.assertRegex(spec["workspace"]["content_sha256"], r"^[0-9a-f]{64}$")
            self.assertTrue(spec["probes"])
            if not spec["exclude"]:
                self.assertEqual(spec["workspace"]["tree"], spec["tree"], name)
        self.assertEqual(fx["openhands"]["exclude"], ["enterprise/"])
        comp = FIXTURES["composites"]["parity-composite"]
        self.assertEqual(comp["workspace"]["files"], sum(fx[m]["workspace"]["files"] for m in comp["members"]))
        self.assertTrue(5000 <= comp["workspace"]["files"] <= 10000)

    @unittest.skipUnless(os.environ.get("XMUSTARD_FIXTURE_TEST"), "clones the real pinned fixtures (set XMUSTARD_FIXTURE_TEST=1)")
    def test_real_fixtures_clone_and_verify(self):
        cache = os.environ.get("XMUSTARD_FIXTURE_CACHE") or os.path.join(self.tmp, "real")
        for name in ("cline", "pi-mono", "openhands", "parity-composite"):
            info = v2.ensure_workspace(name, FIXTURES, cache)
            spec = FIXTURES["fixtures"].get(name) or FIXTURES["composites"][name]
            self.assertEqual(info["verified"], spec["workspace"])


class RepoWiring(unittest.TestCase):
    def test_makefile_targets(self):
        with open(os.path.join(REPO, "Makefile")) as f:
            text = f.read()
        for target in ("bench-parity:", "bench-gate:", "bench-retrieval:", "bench-test:"):
            self.assertIn(target, text)
        self.assertIn("rss_v2.sh run --suite parity", text)

    def test_ci_workflow_runs_backend_checks_and_gates_on_prs(self):
        path = os.path.join(REPO, ".github", "workflows", "check.yml")
        with open(path) as f:
            text = f.read()
        try:
            import yaml
        except ImportError:
            yaml = None
        if yaml is not None:
            wf = yaml.safe_load(text)
            on = wf.get("on", wf.get(True))
            self.assertIn("pull_request", on)
            runs = "\n".join(s.get("run", "") for j in wf["jobs"].values() for s in j["steps"])
        else:
            self.assertIn("pull_request:", text)
            runs = text
        for needle in ("make check-backend", "retrieval-gate.sh", "rss_v2.sh run --suite ci", "make bench-test", "--workstream"):
            self.assertIn(needle, runs)


class Rendering(unittest.TestCase):
    def test_markdown_carries_the_verdict_and_external_lines(self):
        s = v2.SamplerV2(roots(shims=(), agent=None), REGISTRY, FakeProbe(stats={100: {"footprint_bytes": 2**20}}))
        s.ingest(1.0, "work", ps((100, 1, 20480, "xmustard-api"), (110, 100, 81920, "gopls")))
        srep = s.report()
        rep = {"generated_at": "t", "verdict": "PASS", "probe": "fake", "provenance": {"head": "abc", "env": {}},
               "parity_claim": {"established": False, "missing": ["watcher-on: skipped (feature absent)"]},
               "scenarios": {"agents-1": {"status": "ran", "about": "x", "gate": v2.evaluate_gate(srep, []), "sampler": srep,
                                          "transport": "stdio-shim"},
                             "watcher-on": {"status": "skipped", "reason": "feature absent at this HEAD: watcher"}},
               "ledger_reconcile": v2.ledger_reconcile(LEDGER), "unmeasured": v2.UNMEASURED}
        md = v2.render_markdown(rep)
        for needle in ("**Verdict: PASS**", "95.4 MiB", "not established", "watcher-on | skipped", "lsp_server: peak 80.0 MiB",
                       "| go_daemon |", "Design lines"):
            self.assertIn(needle, md)


@unittest.skipUnless(os.environ.get("XMUSTARD_BENCH_LIVE"), "live 3-minute run of the v1 workload (set XMUSTARD_BENCH_LIVE=1)")
class LiveV1Reproduction(unittest.TestCase):
    def test_v2_reproduces_v1_on_the_501_file_workload(self):
        out = tempfile.mkdtemp(prefix="xm-live-")
        try:
            code = v2.main(["run", "--scenarios", "v1-workload", "--out", out])
            rep = v2.load_json(os.path.join(out, "report.json"))
            r = rep["scenarios"]["v1-workload"]
            self.assertTrue(r["gate"]["valid"], r["gate"]["invalid_reasons"])
            self.assertTrue(r["v1_crosscheck"]["within_noise"], r["v1_crosscheck"])
            self.assertEqual(code, 0 if r["gate"]["passed"] else 1)
        finally:
            shutil.rmtree(out, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
