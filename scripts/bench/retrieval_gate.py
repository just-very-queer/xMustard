"""12-query internal retrieval regression gate (plan Stage 5).

Runs the committed gold fixture (scripts/bench/gold) through the real API and Rust
core, cold then warm, then after a one-file edit. Requires >= pass_min/12 gold paths in
the top five on every pass, explicit coverage loss for every omitted/unreadable fixture
file, unchanged or improved recall after the edit without reparsing unchanged files,
and no stale hit presented as current. Internal regression gate only: not competitor
parity and not proof of task success. No model or provider calls.
"""

import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.parse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "e2e"))
from harness import Api, Checks, build_binaries, finish, git_commit_all, provenance, run, sha256_hex  # noqa: E402

GOLD = os.path.join(os.path.dirname(os.path.abspath(__file__)), "gold")


def search(api, ws, query):
    t0 = time.time()
    st, body, _ = api.get(f"/api/workspaces/{ws}/search?q={urllib.parse.quote(query)}")
    return st, body, time.time() - t0


def judge_search(q, body, top_k):
    hits = (body or {}).get("hits") or []
    top = hits[:top_k]
    path_hit = any(h.get("path") == q["gold"]["path"] for h in top)
    lo, hi = q["gold"]["span"]
    span_hit = any(h.get("path") == q["gold"]["path"] and h.get("line") and lo <= h["line"] <= hi for h in top)
    return path_hit, span_hit, [(h.get("path"), h.get("line")) for h in top]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report")
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()
    spec = json.load(open(os.path.join(GOLD, "queries.json")))
    top_k, pass_min = spec["top_k"], spec["pass_min"]
    checks = Checks("retrieval-gate")
    tmp = tempfile.mkdtemp(prefix="xm-gold-")
    api = None
    results = {"passes": {}, "work": {}}
    try:
        api_bin, mcp_bin, core = build_binaries(os.path.join(tmp, "bin"))
        results["provenance"] = provenance(api_bin, mcp_bin, core, [
            "scripts/bench/retrieval_gate.py", "scripts/bench/gold/queries.json", "scripts/e2e/harness.py"])
        repo = os.path.join(tmp, "repo")
        shutil.copytree(os.path.join(GOLD, "repo"), repo)
        os.makedirs(os.path.join(repo, "gen"), exist_ok=True)
        with open(os.path.join(repo, "gen", "huge.go"), "w") as f:
            line = "// filler line for an oversized generated file that exceeds the read cap\n"
            f.write("package gen\n" + line * (9 * 1024 * 1024 // len(line) + 1))
        with open(os.path.join(repo, "web", "src", "broken.ts"), "wb") as f:
            f.write(b"export const bad = '\xff\xfe\x80';\n")
        git_commit_all(repo)
        # the committed fixture (gold repo + generated oversized/invalid-UTF-8 files),
        # identified by its content-addressed git tree id
        results["fixture"] = {"git_tree": run(["git", "rev-parse", "HEAD^{tree}"], cwd=repo).stdout.strip(),
                              "tracked_files": len(run(["git", "ls-files"], cwd=repo).stdout.split())}
        data = os.path.join(tmp, "data")
        os.makedirs(data)
        with open(os.path.join(data, "settings.json"), "w") as f:
            json.dump({"require_multi_agent_verification": False}, f)
        api = Api(api_bin, data, {"XMUSTARD_CORE_BIN": core}).start()
        st, snap, _ = api.request("POST", "/api/workspaces/load", {"root_path": repo, "auto_scan": True})
        ws = snap["workspace"]["workspace_id"]
        checks.check("fixture workspace loads", st == 200, f"{st}")

        contra = next(q for q in spec["queries"] if q["kind"] == "contradiction")
        for m in contra["memories"]:
            api.request("POST", f"/api/workspaces/{ws}/context", {"content": m, "paths": [contra["gold"]["path"]]})

        def run_pass(label):
            score, spans, rows, coverage, latencies = 0, 0, [], None, []
            for q in spec["queries"]:
                ok, detail = False, ""
                if q["kind"] == "search":
                    st, body, dt = search(api, ws, q["query"])
                    latencies.append(dt)
                    coverage = coverage or (body or {}).get("coverage")
                    ok, span_ok, top = judge_search(q, body, top_k)
                    spans += span_ok
                    detail = f"top{top_k}={top}"
                elif q["kind"] == "failure_evidence":
                    log = "".join(f"PASS web/src/suite_{i:04d}.test.ts > case {i} ok\n" for i in range(q["log"]["passing_lines"]))
                    half = q["log"]["passing_lines"] // 2
                    lines = log.splitlines(keepends=True)
                    log = "".join(lines[:half]) + q["log"]["fail_line"] + "\n" + "".join(lines[half:])
                    raw = log.encode()
                    st, d, _ = api.request("POST", f"/api/workspaces/{ws}/evidence?tool=why_failed&content_type=text/plain", raw)
                    proj = d.get("projection", "") if isinstance(d, dict) else ""
                    got, off = b"", 0
                    while isinstance(d, dict) and d.get("handle"):
                        _, page, _ = api.get(f"/api/workspaces/{ws}/evidence/{d['handle']}?offset={off}")
                        got += base64.b64decode(page["data"])
                        if page["eof"]:
                            break
                        off = page["next_offset"]
                    ok = (q["log"]["fail_line"] in proj and q["gold"]["path"] in proj and got == raw
                          and d.get("projected_bytes", 1 << 30) < len(raw))
                    detail = f"projected={d.get('projected_bytes')} raw={len(raw)} exact={got == raw}"
                elif q["kind"] == "contradiction":
                    st, body, _ = api.get(f"/api/workspaces/{ws}/context/active?paths={urllib.parse.quote(q['gold']['path'])}")
                    texts = [e.get("content") for e in body.get("entries", [])]
                    ok = all(m in texts for m in q["memories"]) and bool(body.get("conflicts"))
                    detail = f"returned={len(texts)} conflicts={len(body.get('conflicts') or [])}"
                score += ok
                rows.append({"id": q["id"], "ok": ok, "detail": detail})
            results["passes"][label] = {"score": score, "span_hits": spans, "rows": rows,
                                         "search_latency_s": {"mean": sum(latencies) / len(latencies), "max": max(latencies)}}
            checks.check(f"{label}: >= {pass_min}/12 gold in top {top_k}", score >= pass_min, f"{score}/12, spans {spans}/10; misses {[r['id'] for r in rows if not r['ok']]}")
            return score, coverage

        cold, coverage = run_pass("cold")
        work = (coverage or {}).get("work", {})
        results["work"]["cold_first_query"] = work
        checks.check("cold pass built the graph (first query is a cache miss)", work.get("graph_cache") == "miss",
                     f"graph_cache={work.get('graph_cache')} files_parsed={work.get('files_parsed')}")
        losses = {l["path"]: l["reason"] for l in (coverage or {}).get("losses", [])}
        for fx in spec["coverage_fixtures"]:
            checks.check(f"explicit coverage loss: {fx['path']} ({fx['reason']})", losses.get(fx["path"]) == fx["reason"], f"reported {losses.get(fx['path'])}")
        checks.check("coverage not claimed complete with losses present", coverage and coverage.get("complete") is False)
        warm, coverage = run_pass("warm")
        results["work"]["warm_first_query"] = coverage.get("work", {})
        ww = coverage.get("work", {})
        checks.check("warm pass served from the cache without parsing", ww.get("graph_cache") == "hit" and ww.get("files_parsed") == 0,
                     f"graph_cache={ww.get('graph_cache')} files_parsed={ww.get('files_parsed')}")

        edit = spec["one_file_edit"]
        with open(os.path.join(repo, edit["path"]), "a") as f:
            f.write(edit["append"])
        after, coverage = run_pass("after-one-file-edit")
        w = coverage.get("work", {})
        results["work"]["after_edit_first_query"] = w
        checks.check("recall unchanged or improved after one-file edit", after >= warm, f"{after} vs warm {warm}")
        checks.check("one-file edit reparses only that file", w.get("files_parsed") == 1 and w.get("files_reused", 0) >= 1,
                     f"files_parsed={w.get('files_parsed')} files_reused={w.get('files_reused')} bytes_parsed={w.get('bytes_parsed')}")

        probe = spec["stale_probe"]
        probe_path = os.path.join(repo, probe["path"])
        with open(probe_path, "w") as f:
            f.write(f"package store\n\n// {probe['symbol']} drains the legacy queue.\nfunc {probe['symbol']}() {{}}\n")
        run(["git", "add", probe["path"]], cwd=repo)
        _, body, _ = search(api, ws, probe["symbol"])
        found = any(h.get("path") == probe["path"] for h in (body.get("hits") or [])[:top_k])
        os.remove(probe_path)
        run(["git", "rm", "-q", "--cached", probe["path"]], cwd=repo)
        _, body, _ = search(api, ws, probe["symbol"])
        stale = any(h.get("path") == probe["path"] for h in (body.get("hits") or []))
        key_now = json.loads(run([core, "repo-key", repo]).stdout)["key"]
        served = (body.get("coverage") or {}).get("source_identity", {}).get("key")
        checks.check("no stale hit presented as current", found and not stale and served == key_now,
                     f"found_before={found} stale_after={stale} served_key==current={served == key_now}")
    except Exception as e:
        checks.check("harness completed without exception", False, repr(e))
    finally:
        if api:
            api.stop()
        if not args.keep:
            shutil.rmtree(tmp, ignore_errors=True)
    s = checks.summary()
    s["results"] = results
    if args.report:
        with open(args.report, "w") as f:
            json.dump(s, f, indent=1)
    finish(checks)


if __name__ == "__main__":
    main()
