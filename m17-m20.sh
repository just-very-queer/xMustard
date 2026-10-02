#!/bin/sh
# WS-59b round-1 mutation rows. Each drops one behaviour this round's tests claim and
# runs the tests that must fail. A row is killed only by a FAIL line, never by a build
# error: a mutation that does not compile proves nothing about the test.
set -u
cd "${1:-.}" || exit 1

run_rust() {
	(cd rust-core && cargo test --lib --no-fail-fast 2>&1)
}

mutate() {
	python3 - "$1" "$2" "$3" <<-'PY'
	import sys
	path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
	s = open(path).read()
	if s.count(old) != 1:
	    print("SUBJECT_MISS count=%d" % s.count(old))
	    sys.exit(3)
	open(path, "w").write(s.replace(old, new, 1))
	PY
}

echo "=== M17: text mode fuses every lane again ==="
mutate rust-core/src/search.rs \
	'for lane in if body_only {
        Lane::BODY_ONLY.as_slice()
    } else {
        Lane::ALL.as_slice()
    } {' \
	'for lane in Lane::ALL {'
echo "--- mutated ---"
grep -n 'for lane in Lane::ALL' rust-core/src/search.rs | head -2
run_rust > /tmp/M17.txt 2>&1
grep -E '^(test search::tests::text_mode_ranks_on_the_body_lane_alone|error\[|error:)' /tmp/M17.txt | head -3
echo "M17_rc=$?"

echo "=== M18: the rerank guard is removed ==="
mutate api-go/cmd/xmustard-api/tool_routes.go \
	'if q.Get("mode") == workspaceops.SearchModeText || q.Get("lang") != "" {' \
	'if false {'
echo "--- mutated ---"
grep -n 'if false {' api-go/cmd/xmustard-api/tool_routes.go | head -2
(cd api-go && go test -count=1 -run TestSearchRerankDoesNotBypassTheLexicalFilters ./cmd/xmustard-api 2>&1) > /tmp/M18.txt 2>&1
grep -E '^(---|FAIL|ok|\s+search_mode_routes_test)' /tmp/M18.txt | head -4

echo "=== M19: the declaration feature returns in text mode ==="
mutate rust-core/src/search.rs \
	'if !text_mode {
        features.decl = rerank::decl_match(q, decl_name);
    } else {
        features.decl = rerank::DeclMatch::None;
    }' \
	'features.decl = rerank::decl_match(q, decl_name);'
echo "--- mutated ---"
grep -n 'features.decl = rerank::decl_match' rust-core/src/search.rs | head -2
run_rust > /tmp/M19.txt 2>&1
grep -E '^(test search::tests::text_mode_ranks_on_the_body_lane_alone|error\[|error:)' /tmp/M19.txt | head -3

echo "=== M20: the cursor key drops lang and mode ==="
mutate api-go/internal/workspaceops/knowledge.go \
	'if bytes.Equal(base.rankingKey("ws"), other.rankingKey("ws")) {' \
	'if true {'
echo "--- mutated ---"
grep -n 'rankingKey' api-go/internal/workspaceops/knowledge.go | head -3
(cd api-go && go test -count=1 -run TestSearchCursorIsBoundToLangAndMode ./internal/workspaceops 2>&1) > /tmp/M20.txt 2>&1
grep -E '^(---|FAIL|ok|\s+search_mode_test)' /tmp/M20.txt | head -4