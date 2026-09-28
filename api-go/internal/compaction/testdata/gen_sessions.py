#!/usr/bin/env python3
"""Generates the compaction fixtures: one coding session in two wire formats.

    python3 gen_sessions.py   (run from this directory; needs git)

The session fixes a failing test in api-go/internal/evidence. File reads are real
content of this repository at a pinned commit (git show), numbered like Claude Code's
Read tool; grep output is real grep output of the same tree; test runs, edits and todo
updates are written here. The same session is emitted as Anthropic Messages content
blocks (anthropic_session.json) and as Pi agent messages (pi_session.json).
"""
import json
import re
import subprocess

PIN = "866333a"  # feat/parity-v2 when the fixtures were generated

REMINDER = ("<system-reminder>\nThe TodoWrite tool hasn't been used recently. If you're working on tasks that would "
            "benefit from tracking progress, consider using the TodoWrite tool to track your progress.\n</system-reminder>")
HOOK_NOTE = ("[xmustard hook] 2 verified memories bound to api-go/internal/evidence/reduce.go: "
             "recall entry_id=ctx_1122b36dfdd8, ctx_b1e3995e496e.")


def show(path, lines):
    text = subprocess.run(["git", "show", f"{PIN}:{path}"], capture_output=True, text=True, check=True).stdout
    out = text.split("\n")[:lines]
    return "\n".join(f"{i + 1:6d}\t{l}" for i, l in enumerate(out))


def grep(pattern, path):
    files = subprocess.run(["git", "ls-tree", "-r", "--full-tree", "--name-only", PIN, path], capture_output=True, text=True,
                           check=True).stdout.split()
    hits = []
    for f in sorted(files):
        if not f.endswith(".go"):
            continue
        body = subprocess.run(["git", "show", f"{PIN}:{f}"], capture_output=True, text=True, check=True).stdout
        for n, line in enumerate(body.split("\n"), 1):
            if re.search(pattern, line):
                hits.append(f"{f}:{n}:{line}")
    return "\n".join(hits)


FAIL_RUN = "\n".join(
    ["=== RUN   TestReduceKeepsFailures", "--- FAIL: TestReduceKeepsFailures (0.00s)",
     "    reducers_test.go:212: projection lost the failing assertion:",
     "        want line 'expected 3, got 4' in the projection"] +
    [f"=== RUN   TestReducerCase{i:03d}\n--- PASS: TestReducerCase{i:03d} (0.00s)" for i in range(120)] +
    ["FAIL", "FAIL\txmustard/api-go/internal/evidence\t0.412s", "FAIL", "Exit code 1"])
PASS_RUN = "\n".join(["ok  \txmustard/api-go/internal/evidence\t0.398s"])

TODOS_1 = [{"content": "Reproduce the failing reducer test", "status": "completed", "activeForm": "Reproducing"},
           {"content": "Find where the failure line is dropped", "status": "in_progress", "activeForm": "Finding"},
           {"content": "Fix the reducer and rerun the package", "status": "pending", "activeForm": "Fixing"}]
TODOS_2 = [{"content": "Reproduce the failing reducer test", "status": "completed", "activeForm": "Reproducing"},
           {"content": "Find where the failure line is dropped", "status": "completed", "activeForm": "Finding"},
           {"content": "Fix the reducer and rerun the package", "status": "in_progress", "activeForm": "Fixing"},
           {"content": "Record the fix in the plan", "status": "pending", "activeForm": "Recording"}]
TODO_OK = ("Todos have been modified successfully. Ensure that you continue to use the todo list to track your "
           "progress. Please proceed with the current tasks if applicable")

with open("../../mcpserver/testdata/results/search_hybrid.json") as f:
    SEARCH = json.dumps(json.load(f)["result"], indent=2)

# the session: (assistant text, [calls]) turns; a call is (id, tool, input, result, is_error)
TURNS = [
    ("I'll run the evidence tests first.", [
        ("t01", "Bash", {"command": "cd api-go && go test ./internal/evidence/", "description": "Run evidence tests"},
         FAIL_RUN, True)]),
    ("Let me find the reducers and read the text reducer.", [
        ("t02", "Grep", {"pattern": "func .*[rR]educe", "path": "api-go/internal/evidence", "output_mode": "content",
                         "-n": True}, grep(r"func (\(.*\) )?[rR]educe", "api-go/internal/evidence"), False),
        ("t03", "Read", {"file_path": "api-go/internal/evidence/reduce.go"}, show("api-go/internal/evidence/reduce.go", 420),
         False)]),
    ("Tracking the work.", [("t04", "TodoWrite", {"todos": TODOS_1}, TODO_OK, False)]),
    ("Checking xMustard's index for the salience rules.", [
        ("t05", "mcp__xmustard__search", {"q": "salience failure lines", "limit": 5}, SEARCH, False)]),
    ("Reading the salience rules.", [
        ("t06", "Read", {"file_path": "api-go/internal/evidence/salience.go"},
         show("api-go/internal/evidence/salience.go", 211), False)]),
    ("The failure pattern misses 'got'. Fixing it.", [
        ("t07", "Edit", {"file_path": "api-go/internal/evidence/salience.go", "old_string": "failed|panic",
                         "new_string": "failed|panic|got"},
         "The file api-go/internal/evidence/salience.go has been updated.", False)]),
    ("Updating the todos.", [("t08", "TodoWrite", {"todos": TODOS_2}, TODO_OK, False)]),
    ("Rerunning the package and reading the registry to check the family path.", [
        ("t09", "Bash", {"command": "cd api-go && go test ./internal/evidence/"}, PASS_RUN, False),
        ("t10", "Read", {"file_path": "api-go/internal/evidence/registry.go"},
         show("api-go/internal/evidence/registry.go", 260), False)]),
    ("The tests pass; the fix is in salience.go.", []),
]

PI_NAMES = {"Bash": "bash", "Grep": "grep", "Read": "read", "Edit": "edit", "TodoWrite": "todo_write",
            "mcp__xmustard__search": "xmustard_search"}
PI_ARGS = {"file_path": "path", "old_string": "oldText", "new_string": "newText"}


def anthropic():
    msgs = [{"role": "user", "content": [
        {"type": "text", "text": "Fix the failing reducer test in api-go/internal/evidence."},
        {"type": "text", "text": REMINDER}]}]
    for n, (text, calls) in enumerate(TURNS):
        blocks = [{"type": "text", "text": text}] + [
            {"type": "tool_use", "id": f"toolu_{cid}", "name": tool, "input": inp} for cid, tool, inp, _, _ in calls]
        msgs.append({"role": "assistant", "content": blocks})
        if not calls:
            continue
        results = []
        for cid, _, _, res, err in calls:
            r = {"type": "tool_result", "tool_use_id": f"toolu_{cid}", "content": res}
            if err:
                r["is_error"] = True
            results.append(r)
        if n in (2, 5):  # the harness repeats its guidance and hook notes
            results.append({"type": "text", "text": REMINDER})
        if n in (1, 4, 7):
            results.append({"type": "text", "text": HOOK_NOTE})
        msgs.append({"role": "user", "content": results})
    return msgs


def pi():
    ts = 1790000000000
    msgs = [{"role": "user", "content": [{"type": "text", "text": "Fix the failing reducer test in api-go/internal/evidence."}],
             "timestamp": ts}]
    for n, (text, calls) in enumerate(TURNS):
        ts += 1000
        msgs.append({"role": "assistant", "content": [{"type": "text", "text": text}] + [
            {"type": "toolCall", "id": f"call_{cid}", "name": PI_NAMES[tool],
             "arguments": {PI_ARGS.get(k, k): v for k, v in inp.items()}} for cid, tool, inp, _, _ in calls],
            "api": "anthropic-messages", "provider": "anthropic", "model": "claude-opus-5-5",
            "usage": {"input": 0, "output": 0}, "stopReason": "toolUse" if calls else "stop", "timestamp": ts})
        for cid, tool, _, res, err in calls:
            msgs.append({"role": "toolResult", "toolCallId": f"call_{cid}", "toolName": PI_NAMES[tool],
                         "content": [{"type": "text", "text": res}], "isError": err, "timestamp": ts})
        if n in (1, 4, 7):
            msgs.append({"role": "user", "content": [{"type": "text", "text": HOOK_NOTE}], "timestamp": ts})
    return msgs


for name, build in (("anthropic_session.json", anthropic), ("pi_session.json", pi)):
    with open(name, "w") as f:
        json.dump(build(), f, indent=1)
        f.write("\n")
