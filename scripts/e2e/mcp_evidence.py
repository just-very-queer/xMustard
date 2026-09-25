"""HTTP + stdio-MCP evidence end-to-end check (plan Stage 5, scripts/e2e/mcp-evidence.sh).

Real API, real stdio shim, real Rust core (wrapped only to make one query slow for
the cancellation check), temporary Git repository, temporary data root, random port,
required bearer authentication. Covers: tools and resources capability, bounded plain
recall delivered as a reduced projection with a recovery handle, exact paging of the
original through resources/read, authorization and cross-workspace denial, concurrent
calls, cancellation of an in-flight call and its child, repository mutation (stale
label), API + shim restart with the exact issued URI, and expiry.
"""

import argparse
import base64
import json
import os
import shutil
import signal
import sys
import tempfile
import threading
import time

sys.path.insert(0, os.path.dirname(__file__))
from harness import Api, Checks, Mcp, build_binaries, finish_with, git_commit_all, mint_token, provenance, sha256_hex  # noqa: E402

SCRIPTS = ["scripts/e2e/mcp_evidence.py", "scripts/e2e/mcp-evidence.sh", "scripts/e2e/harness.py"]


def text(result):
    return result["result"]["content"][0]["text"]


def evidence_meta(result):
    return result.get("result", {}).get("_meta", {}).get("xmustard/evidence")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", help="write the JSON check report here")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory")
    args = ap.parse_args()
    checks = Checks("mcp-evidence")
    tmp = tempfile.mkdtemp(prefix="xm-mcp-e2e-")
    api = mcp = None
    extra = {}
    try:
        api_bin, mcp_bin, core = build_binaries(os.path.join(tmp, "bin"))
        extra["provenance"] = provenance(api_bin, mcp_bin, core, SCRIPTS)
        # wrapper: a query containing __slow__ blocks (for the cancellation check)
        pid_file = os.path.join(tmp, "slow.pid")
        wrapper = os.path.join(tmp, "bin", "core-wrapper")
        with open(wrapper, "w") as f:
            f.write(f'#!/bin/sh\ncase "$*" in *__slow__*) echo $$ > "{pid_file}"; sleep 60 ;; esac\nexec "{core}" "$@"\n')
        os.chmod(wrapper, 0o755)

        repo = os.path.join(tmp, "repo")
        os.makedirs(repo)
        files = {
            "main.go": "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\nfunc main() { _ = Add(1, 2) }\n",
            "util.rs": "pub fn parse_config(s: &str) -> usize {\n    s.len()\n}\n",
            "app.ts": "export function renderPage(title: string): string {\n  return title;\n}\n",
        }
        for name, body in files.items():
            with open(os.path.join(repo, name), "w") as f:
                f.write(body)
        git_commit_all(repo)

        data = os.path.join(tmp, "data")
        os.makedirs(data)
        with open(os.path.join(data, "settings.json"), "w") as f:
            json.dump({"require_multi_agent_verification": False}, f)
        admin = mint_token(api_bin, data, "operator", "admin")
        alice = mint_token(api_bin, data, "alice", "agent")
        bob = mint_token(api_bin, data, "bob", "agent")
        # setup (workspace load + index baseline) uses the full surface; the checks run
        # against the lean CORE_ONLY surface the nine tools and evidence routes use
        setup_env = {"XMUSTARD_AUTH": "required", "XMUSTARD_CORE_BIN": wrapper}
        api_env = {**setup_env, "XMUSTARD_CORE_ONLY": "1"}
        api = Api(api_bin, data, setup_env).start()
        st, snap, _ = api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True}, token=admin)
        ws = snap["workspace"]["workspace_id"] if st == 200 else None
        checks.check("workspace loads over authenticated HTTP", st == 200 and ws, f"status {st}")
        st, _, _ = api.request("POST", f"/api/workspaces/{ws}/index", token=admin)
        checks.check("index baseline", st == 200, f"status {st}")
        api.stop()
        api = Api(api_bin, data, api_env).start()
        st, _, _ = api.get(f"/api/workspaces/{ws}/context/active")
        checks.check("unauthenticated tool route is refused", st == 401, f"status {st}")

        mcp = Mcp(mcp_bin, api.base, {"XMUSTARD_API_TOKEN": alice}).start()
        init = mcp.call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}})
        checks.check("initialize advertises tools and resources", {"tools", "resources"} <= set(init["result"]["capabilities"]))
        names = sorted(t["name"] for t in mcp.call("tools/list")["result"]["tools"])
        checks.check("exactly the nine tools", names == sorted(["ground", "recall", "remember", "verify", "search", "explain", "impact", "diagnostics", "why_failed"]), str(names))

        # ten large memories: plain recall (bounded top-8) exceeds the 64 KiB projection target
        ok = True
        for i in range(10):
            body = f"fact {i}: " + ("the deployment checklist requires step %d. " % i) * 400
            r = mcp.tool("remember", {"workspace_id": ws, "content": body, "title": f"fact {i}"})
            ok = ok and not r["result"]["isError"]
        checks.check("remember x10 over MCP", ok)

        r = mcp.tool("recall", {"workspace_id": ws})
        meta = evidence_meta(r)
        proj = json.loads(text(r)) if not r["result"]["isError"] else {}
        checks.check("plain recall is bounded", proj.get("bounded") is True and proj.get("returned", 99) <= 8 and proj.get("total_active") == 10,
                     f"returned={proj.get('returned')} total={proj.get('total_active')}")
        checks.check("recall delivered as reduced projection with a handle", bool(meta and meta.get("handle")),
                     f"projected={meta and meta.get('projected_bytes')} raw={meta and meta.get('raw_bytes')}")
        uri = meta["resource_uri"] if meta else ""
        checks.check("issued URI is self-sufficient (names its workspace)", f"workspace_id={ws}" in uri, uri)
        orig, pages = mcp.read_all(uri)
        checks.check("resources/read pages the exact original", orig is not None and sha256_hex(orig) == meta["raw_sha256"]
                     and all(p["length"] <= 65536 for p in pages),
                     f"{len(pages)} pages, client ms per page {[p.get('_client_ms') for p in pages]} (each page samples repo identity)")
        checks.check("original is the complete bounded recall", orig is not None and len(json.loads(orig)["entries"]) == proj.get("returned"))
        checks.check("fresh capture labeled current", pages and pages[0]["freshness"] == "current", pages and pages[0]["freshness"])

        handle = meta["handle"]
        path = f"/api/workspaces/{ws}/evidence/{handle}?offset=0&length=100"
        st_a = api.get(path, alice)[0]
        st_b = api.get(path, bob)[0]
        st_n = api.get(path)[0]
        st_x = api.get(f"/api/workspaces/otherws/evidence/{handle}", alice)[0]
        checks.check("issuer reads over HTTP; other principal, anonymous and cross-workspace are denied",
                     (st_a, st_b, st_n, st_x) == (200, 403, 401, 404), f"{st_a},{st_b},{st_n},{st_x}")

        ids = [mcp.send("tools/call", {"name": "search", "arguments": {"workspace_id": ws, "query": q}}) for q in ["Add", "parse_config", "renderPage", "main", "Add", "renderPage"]]
        replies = [mcp.wait(i) for i in ids]
        checks.check("six concurrent calls answered and correlated", all(rp.get("id") == i and not rp["result"]["isError"] for rp, i in zip(replies, ids)))
        hit = json.loads(text(replies[0]))
        checks.check("search finds the exact symbol with coverage", any(h["path"] == "main.go" and h.get("line") == 3 for h in hit["hits"]) and "coverage" in hit)

        slow = mcp.send("tools/call", {"name": "search", "arguments": {"workspace_id": ws, "query": "__slow__"}})
        deadline = time.time() + 20
        while not os.path.exists(pid_file) and time.time() < deadline:
            time.sleep(0.05)
        child = int(open(pid_file).read().strip()) if os.path.exists(pid_file) else 0
        started = time.time()
        mcp.notify("notifications/cancelled", {"requestId": slow, "reason": "e2e"})
        reply = mcp.wait(slow, timeout=15)
        time.sleep(0.5)
        alive = child > 0 and os.system(f"kill -0 {child} 2>/dev/null") == 0
        # the cancelled call must be answered as an error (JSON-RPC error or isError result)
        is_error = "error" in reply or reply.get("result", {}).get("isError") is True
        checks.check("cancellation answers the call with an error and kills its child",
                     child > 0 and not alive and is_error and time.time() - started < 10,
                     f"child={child} alive={alive} error_reply={is_error}")

        with open(os.path.join(repo, "main.go"), "a") as f:
            f.write("\nfunc Added() {}\n")
        _, pages2 = mcp.read_all(uri)
        checks.check("repository mutation labels the original stale", pages2 and pages2[0]["freshness"] == "stale" and pages2[0]["stale"] is True
                     and pages2[0]["captured_key"] != pages2[0]["current_key"], pages2 and pages2[0]["freshness"])

        # restart API and shim; the exact issued URI still recovers the original
        api.stop()
        checks.check("API shut down cleanly", "shutdown: complete" in api.log_text())
        mcp.stop()
        api = Api(api_bin, data, api_env).start()
        mcp = Mcp(mcp_bin, api.base, {"XMUSTARD_API_TOKEN": alice}).start()
        orig2, _ = mcp.read_all(uri)
        checks.check("after API + shim restart the exact URI pages the same original", orig2 is not None and sha256_hex(orig2) == meta["raw_sha256"])

        # expiry: a capture issued with a 3 s retention is denied after it expires
        api.stop()
        mcp.stop()
        api = Api(api_bin, data, {**api_env, "XMUSTARD_EVIDENCE_RETENTION_SECONDS": "3"}).start()
        mcp = Mcp(mcp_bin, api.base, {"XMUSTARD_API_TOKEN": alice}).start()
        r = mcp.tool("recall", {"workspace_id": ws})
        short = evidence_meta(r)
        first, _ = mcp.read_all(short["resource_uri"]) if short else (None, [])
        time.sleep(3.5)
        expired = mcp.call("resources/read", {"uri": short["resource_uri"]}) if short else {}
        err = expired.get("error", {})
        checks.check("original reads until expiry and is denied afterward", first is not None and err.get("code") == -32002
                     and err.get("data", {}).get("reason") == "expired", json.dumps(err)[:200])
        still, _ = mcp.read_all(uri)
        checks.check("longer-retention original unaffected by another's expiry", still is not None)

        bad = mcp.tool("explain", {"workspace_id": ws, "path": "../../etc/passwd"})
        checks.check("tool errors stay errors", bad["result"]["isError"] is True)
        missing = mcp.call("resources/read", {"uri": "xmustard://evidence/xm1.AAAA?workspace_id=" + ws})
        checks.check("unknown resource is a protocol resource-not-found error", missing.get("error", {}).get("code") == -32002)
    except Exception as e:  # report, never hide, harness failures
        checks.check("harness completed without exception", False, repr(e))
    finally:
        if mcp:
            mcp.stop()
        if api:
            api.stop()
        if not args.keep:
            shutil.rmtree(tmp, ignore_errors=True)
        else:
            print("kept", tmp)
    finish_with(checks, args.report, extra)


if __name__ == "__main__":
    main()
