#!/bin/bash
# WS-59b round-1 mutation rows M17-M20. Each drops one behaviour this round's tests
# claim; a row is killed only by a FAIL line, never by a build error.
set -u
cd "${1:-.}" || exit 1

mutate() {
	python3 mut.py "$1" "$2" "$3"
}

run_rust() {
	(cd rust-core && cargo test --lib --no-fail-fast "$1" 2>&1)
}

echo "=== M17: text mode fuses every lane again ==="
mutate rust-core/src/search.rs 'for lane in if body_only {
        Lane::BODY_ONLY.as_slice()
    } else {
        Lane::ALL.as_slice()
    } {
        let mut ranked' 'for lane in Lane::ALL.iter() {
        let mut ranked'
echo "subject_hits=$(grep -c 'for lane in Lane::ALL.iter() {' rust-core/src/search.rs)"
run_rust text_mode_ranks | grep -E '^(test |error)' | head -3

echo "=== M18: the rerank guard is removed ==="
mutate api-go/cmd/xmustard-api/tool_routes.go 'if q.Get("mode") == workspaceops.SearchModeText || q.Get("lang") != "" {' 'if false {'
echo "subject_hits=$(grep -c 'if false {' api-go/cmd/xmustard-api/tool_routes.go)"
(cd api-go && go test -count=1 -run TestSearchRerankDoesNotBypassTheLexicalFilters ./cmd/xmustard-api 2>&1) | grep -E '^(---|FAIL|ok)' | head -3

echo "=== M19: the declaration feature returns in text mode ==="
mutate rust-core/src/search.rs 'if !text_mode {
        features.decl = rerank::decl_match(q, decl_name);
    } else {
        features.decl = rerank::DeclMatch::None;
    }' 'features.decl = rerank::decl_match(q, decl_name);'
echo "subject_hits=$(grep -c 'features.decl = rerank::decl_match(q, decl_name);' rust-core/src/search.rs)"
run_rust text_mode_ranks | grep -E '^(test |error)' | head -3

echo "=== M20: the cursor key stops naming lang and mode ==="
mutate api-go/internal/workspaceops/knowledge.go 'b, _ := json.Marshal([]string{workspaceID, r.Query, r.Seed, r.PathGlob, r.Lang, r.Mode})' 'b, _ := json.Marshal([]string{workspaceID, r.Query})'
echo "subject_hits=$(grep -c 'r.PathGlob, r.Lang, r.Mode' api-go/internal/workspaceops/knowledge.go)"
(cd api-go && go test -count=1 -run TestSearchCursorIsBoundToLangAndMode ./internal/workspaceops 2>&1) | grep -E '^(---|FAIL|ok)' | head -3


echo "=== M21: workspaceSearch stops refusing an unknown mode ==="
mutate api-go/internal/workspaceops/knowledge.go '	switch req.Mode {
	case "", SearchModeHybrid, SearchModeText:
	default:
		return nil, fmt.Errorf("mode %q is not a search mode: %w", req.Mode, ErrInvalidInput)
	}' ''
echo "subject_hits=$(grep -c 'is not a search mode' api-go/internal/workspaceops/knowledge.go)"
(cd api-go && go test -count=1 -run TestSearchRefusesAnUnknownModeBelowTheRoute ./internal/workspaceops 2>&1) | grep -E '^(---|FAIL|ok|.*unknown mode)' | head -4

echo "=== done ==="