#!/bin/sh
# Records Codex hook payloads with no model call: codex exec talks to a local mock
# Responses API (mock-responses.mjs), runs one tool, and hook.mjs logs the payload
# Codex hands the hook. Local paths are replaced by /Users/dev/... .
#   ./record.sh <out-dir> [port]      (needs codex and node on PATH)
# writes the PostToolUse bodies to <out-dir> and, from two more turns with a hook on
# every event they reach (one forced to compact), <out-dir>/events/<Event>.json
# (WS-40a). SubagentStart and SubagentStop need a subagent, which the mock cannot
# start (spawn_agent is an unsupported call for it): see integrations/codex/README.md.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?out dir}; port=${2:-18765}
mkdir -p "$out"
work=$(cd "$(mktemp -d)" && pwd -P); home="$work/home"; app="$work/app"; mkdir -p "$home" "$app"
cat > "$home/config.toml" <<TOML
model = "gpt-5.5"
model_provider = "mock"
approval_policy = "never"

[model_providers.mock]
name = "mock"
base_url = "http://127.0.0.1:$port/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false

[mcp_servers.cilogs]
command = "node"
args = ["$here/mcp-server.mjs"]
default_tools_approval_mode = "approve"
TOML
printf '{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"node %s %s"}]}]}}\n' "$here/hook.mjs" "$work/hook.jsonl" > "$home/hooks.json"
large='for i in $(seq 1 1500); do echo "=== RUN   TestCase$i"; echo "--- PASS: TestCase$i (0.00s)"; if [ $i = 700 ]; then echo "=== RUN   TestParseValue"; echo "    parse_test.go:17: parse(\"3\") = 3, want 4"; echo "--- FAIL: TestParseValue (0.00s)"; fi; done; echo FAIL; printf "FAIL\texample.com/pkg\t0.412s\n"; echo FAIL; exit 1'
record() { # name prompt shell
	XM_SHELL=$3 node "$here/mock-responses.mjs" "$port" & mock=$!
	sleep 1
	rm -f "$work/hook.jsonl"
	(cd "$app" && CODEX_HOME="$home" CODEX_SQLITE_HOME="$home" codex exec --json --skip-git-repo-check \
		--dangerously-bypass-hook-trust -s workspace-write "$2" </dev/null >/dev/null 2>&1)
	kill "$mock"; wait "$mock" 2>/dev/null || true
	tail -n 1 "$work/hook.jsonl" | sed -e "s#$home#/Users/dev/.codex#g" -e "s#$app#/Users/dev/src/app#g" > "$out/$1.json"
}
record post_tool_use_bash "run the SHELL step" "printf 'ok 1\\nFAIL 2\\n'; exit 3"
record post_tool_use_bash_large "run the SHELL step" "$large"
record post_tool_use_mcp "please CALL_MCP now" ""
# one hook on each event a turn reaches; <out-dir>/events keeps each event's first body
# (a SessionStart for another source than startup as SessionStart-<source>.json)
hook_all() {
	hooks=""
	for ev in "$@"; do
		hooks="$hooks\"$ev\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"node $here/hook.mjs $work/hook.jsonl\"}]}],"
	done
	printf '{"hooks":{%s}}\n' "${hooks%,}" > "$home/hooks.json"
}
split_events() {
	mkdir -p "$out/events"
	sed -e "s#$home#/Users/dev/.codex#g" -e "s#$app#/Users/dev/src/app#g" "$work/hook.jsonl" | node -e '
const fs = require("fs"), seen = new Set();
for (const line of fs.readFileSync(0, "utf8").split("\n").filter(Boolean)) {
	const body = JSON.parse(line), source = body.source && body.source !== "startup" ? "-" + body.source : "";
	const file = process.argv[1] + "/" + body.hook_event_name + source + ".json";
	if (!seen.has(file) && !fs.existsSync(file)) { seen.add(file); fs.writeFileSync(file, line + "\n"); }
}' "$out/events"
}
hook_all SessionStart UserPromptSubmit PreToolUse PostToolUse Stop SessionEnd
record events "deploy check: run the SHELL step" "printf 'ok 1\\n'"
split_events
rm -f "$out/events.json"
# compaction: with a tiny auto-compaction limit every turn compacts (the mock always
# reports 110 tokens), so the run is stopped once the first compaction has fired
hook_all PreCompact PostCompact SessionStart
XM_SHELL="printf 'ok 1\\n'" node "$here/mock-responses.mjs" "$port" & mock=$!
sleep 1
rm -f "$work/hook.jsonl"
(cd "$app" && CODEX_HOME="$home" CODEX_SQLITE_HOME="$home" exec codex exec --json --skip-git-repo-check \
	--dangerously-bypass-hook-trust -s workspace-write -c model_auto_compact_token_limit=100 "run the SHELL step" </dev/null >/dev/null 2>&1) & run=$!
sleep 15
kill "$run" "$mock" 2>/dev/null || true; wait "$run" "$mock" 2>/dev/null || true
split_events
codex --version > "$out/VERSION"
rm -rf "$work"
