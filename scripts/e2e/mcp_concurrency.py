"""Eight independent read-only HTTP MCP clients and their API RSS (WS-75).

The clients share one authenticated xMustard API session table and issue only the
OCR recipe's allowlisted tools. No OCR binary, model provider, or external service is
started.
"""

import argparse
import concurrent.futures
import json
import os
import shutil
import sys
import tempfile
import traceback

sys.path.insert(0, os.path.dirname(__file__))
from harness import Api, Checks, HttpMcp, build_binaries, finish_with, git_commit_all, mint_token, provenance  # noqa: E402

SCRIPTS = ["scripts/e2e/mcp_concurrency.py", "scripts/e2e/harness.py"]
OCR_TOOLS = ("search", "explain", "impact", "recall")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", help="write the JSON check report here")
    ap.add_argument("--keep", action="store_true", help="keep the temporary directory")
    args = ap.parse_args()
    checks = Checks("mcp-concurrency")
    tmp = tempfile.mkdtemp(prefix="xm-mcp-concurrency-")
    api = None
    clients = []
    extra = {}
    try:
        api_bin, mcp_bin, core = build_binaries(os.path.join(tmp, "bin"))
        extra["provenance"] = provenance(api_bin, mcp_bin, core, SCRIPTS)

        repo = os.path.join(tmp, "repo")
        os.makedirs(repo)
        with open(os.path.join(repo, "main.go"), "w", encoding="utf-8") as f:
            f.write("package main\n\nfunc Add(a, b int) int { return a + b }\n")
        git_commit_all(repo)

        data = os.path.join(tmp, "data")
        os.makedirs(data)
        admin = mint_token(api_bin, data, "operator", "admin")
        reader = mint_token(api_bin, data, "ocr-reader", "reader")
        proposer = mint_token(api_bin, data, "proposer", "agent")
        setup_env = {"XMUSTARD_AUTH": "required", "XMUSTARD_CORE_BIN": core}
        api = Api(api_bin, data, setup_env).start()
        status, snapshot, _ = api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True}, token=admin)
        workspace = snapshot.get("workspace", {}).get("workspace_id") if isinstance(snapshot, dict) else None
        checks.check("authenticated workspace load", status == 200 and workspace, f"status={status}")
        if not workspace:
            raise RuntimeError(f"workspace load returned {status}: {snapshot}")
        status, _, _ = api.request("POST", f"/api/workspaces/{workspace}/index", token=admin)
        checks.check("workspace index baseline", status == 200, f"status={status}")
        api.stop()
        api = Api(api_bin, data, {**setup_env, "XMUSTARD_CORE_ONLY": "1"}).start()

        clients = [HttpMcp(api.base, workspace, reader) for _ in range(8)]
        initialized = [client.initialize() for client in clients]
        checks.check("eight reader-role Streamable HTTP sessions initialize", all("result" in r for r in initialized))
        calls = [
            ("search", {"workspace_id": workspace, "query": "Add"}),
            ("explain", {"workspace_id": workspace, "path": "main.go"}),
            ("impact", {"workspace_id": workspace, "path": "main.go"}),
            ("recall", {"workspace_id": workspace}),
            ("search", {"workspace_id": workspace, "query": "Add"}),
            ("explain", {"workspace_id": workspace, "path": "main.go"}),
            ("impact", {"workspace_id": workspace, "path": "main.go"}),
            ("recall", {"workspace_id": workspace}),
        ]
        checks.check("client requests stay within OCR's read allowlist", all(name in OCR_TOOLS for name, _ in calls))
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            replies = list(pool.map(lambda pair: pair[0].tool(*pair[1]), zip(clients, calls)))
        successful = all(reply.get("id") == 1 and not reply.get("error") and
                         not reply.get("result", {}).get("isError") for reply in replies)
        checks.check("eight concurrent read-only MCP calls answer and correlate", successful,
                     json.dumps([reply.get("result", {}).get("isError") for reply in replies]))

        denied = [clients[0].tool("remember", {"workspace_id": workspace, "title": "OCR contract", "content": "probe"}),
                  clients[1].tool("verify", {"workspace_id": workspace, "entry_id": "not-a-memory", "approve": True})]
        checks.check("readonly mode blocks writes on the OCR client",
                     all(reply.get("result", {}).get("isError") is True for reply in denied), json.dumps(denied)[:500])

        full_mode_reader = HttpMcp(api.base, workspace, reader, readonly=False)
        try:
            full_mode_reader.initialize()
            role_denied = [full_mode_reader.tool("remember", {"workspace_id": workspace, "title": "reader role", "content": "probe"}),
                           full_mode_reader.tool("verify", {"workspace_id": workspace, "entry_id": "not-a-memory", "approve": True})]
            checks.check("reader role cannot call remember or verify outside readonly mode",
                         all(reply.get("result", {}).get("isError") is True for reply in role_denied), json.dumps(role_denied)[:500])
        finally:
            full_mode_reader.close()

        readonly = HttpMcp(api.base, workspace, proposer)
        try:
            readonly.initialize()
            attempts = [readonly.tool("remember", {"workspace_id": workspace, "title": "readonly", "content": "probe"}),
                        readonly.tool("verify", {"workspace_id": workspace, "entry_id": "not-a-memory", "approve": True})]
            checks.check("readonly mode denies writes even to a proposer token",
                         all(reply.get("result", {}).get("isError") is True for reply in attempts), json.dumps(attempts)[:500])
        finally:
            readonly.close()

        with open(f"/proc/{api.proc.pid}/status", encoding="utf-8") as status_file:
            rss_line = next((line for line in status_file if line.startswith("VmHWM:")), "")
        rss_peak_kib = int(rss_line.split()[1]) if rss_line else 0
        extra["eight_client_api_vm_hwm_kib"] = rss_peak_kib
        checks.check("eight-client API high-water RSS is measured", rss_peak_kib > 0, rss_line.strip())
    except Exception as e:
        checks.check("harness completed without exception", False, f"{e!r}\n{traceback.format_exc()}")
    finally:
        for client in clients:
            client.close()
        if api:
            api.stop()
        if args.keep:
            print("kept", tmp)
        else:
            shutil.rmtree(tmp, ignore_errors=True)
    finish_with(checks, args.report, extra)


if __name__ == "__main__":
    main()
