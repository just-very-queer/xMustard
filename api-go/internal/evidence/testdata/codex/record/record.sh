#!/bin/sh
# Records Codex PostToolUse hook payloads with no model call: codex exec talks to a
# local mock Responses API (mock-responses.mjs), runs one tool, and hook.mjs logs the
# payload Codex hands the hook. Local paths are replaced by /Users/dev/... .
#   ./record.sh <out-dir> [port]      (needs codex and node on PATH)
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
codex --version > "$out/VERSION"
rm -rf "$work"
