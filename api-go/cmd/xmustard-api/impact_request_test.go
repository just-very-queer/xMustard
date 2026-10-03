package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"xmustard/api-go/internal/toolbudget"
	"xmustard/api-go/internal/workspaceops"
)

// A request mixing impact forms, or out of range, is a 400 before any core runs.
func TestImpactRouteRefusesMixedFormsBeforeRunningTheCore(t *testing.T) {
	srv, dir := newRouteServer(t)
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "core-ran")
	core := filepath.Join(bin, "xmustard-core")
	if err := os.WriteFile(core, []byte("#!/bin/sh\ntouch "+ran+"\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpact", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"?from=a",
		"?symbol=x&scope=all",
		"?symbol=x&depth=33",
		"?from=a&to=b&direction=upstream",
		"?scope=range&base=-x",
		"?depth=2",
		// what the MCP tool forwards when a caller mixes forms (every argument reaches the API)
		"?symbol=X&scope=staged",
		"?from=a&to=b&direction=downstream&limit=5",
		"?scope=all&direction=downstream",
		"?direction=downstream",
		// a scope's revision rules, a NaN confidence and a relative worktree
		"?scope=compare",
		"?scope=staged&head=HEAD",
		"?scope=commit&base=main",
		"?symbol=x&min_confidence=NaN",
		"?scope=compare&base=main&worktree=wt",
	} {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", q, resp.StatusCode)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a refused impact request ran the core")
	}
}

// A cursor drives the route end to end: the first page's next_cursor is accepted by
// the same query with cursor= in place of offset=, it becomes the core's --offset, and
// the cursor it returns continues the next page. The stub core pages a five-item tier
// the way the real one does (offset, then limit, next_offset when the page does not
// reach total), so the pages walked here are the core's own rule.
func TestImpactCursorPagesTheRouteEndToEnd(t *testing.T) {
	srv, dir := newRouteServer(t)
	argsLog := filepath.Join(t.TempDir(), "core-args")
	core := filepath.Join(t.TempDir(), "xmustard-core")
	// The stub records its argv so the test can assert the cursor became --offset, and
	// pages the fixture tier itself rather than echoing a canned answer.
	script := `#!/bin/sh
echo "$@" >> ` + argsLog + `
offset=0
limit=2
total=5
while [ $# -gt 0 ]; do
  case "$1" in
    --offset=*) offset="${1#--offset=}" ;;
    --limit=*) limit="${1#--limit=}" ;;
  esac
  shift
done
items=""
i=$offset
end=$((offset + limit))
[ $end -gt $total ] && end=$total
while [ $i -lt $end ]; do
  items="$items{\"name\":\"i$i\",\"depth\":1},"
  i=$((i + 1))
done
[ -n "$items" ] && items="${items%,}"
next=""
[ $end -lt $total ] && next=",\"next_offset\":$end"
printf '{"symbol":"Serve","impacted_count":%s,"by_depth":[{"depth":1,"total":%s,"offset":%s,"items":[%s]%s}]}' "$total" "$total" "$offset" "$items" "$next"
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsCursor", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	query := "?symbol=Serve&limit=2"
	for page := 0; page < 4; page++ {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index" + query)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("page %d: status %d, body %s", page, resp.StatusCode, body)
		}
		var res struct {
			ByDepth []struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
			} `json:"by_depth"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("page %d: %v (%s)", page, err, body)
		}
		if len(res.ByDepth) == 0 || len(res.ByDepth[0].Items) == 0 {
			t.Fatalf("page %d returned no items", page)
		}
		for _, it := range res.ByDepth[0].Items {
			if seen[it.Name] {
				t.Fatalf("page %d repeats %q: the cursor overlapped", page, it.Name)
			}
			seen[it.Name] = true
		}
		if res.NextCursor == "" {
			break
		}
		query = "?symbol=Serve&limit=2&cursor=" + url.QueryEscape(res.NextCursor)
	}
	if len(seen) != 5 {
		t.Fatalf("walked %d of 5 items: the cursor left a gap: %v", len(seen), seen)
	}
	// The cursor reached the core as --offset, not as a cursor the core would ignore.
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--offset=2") || !strings.Contains(string(args), "--offset=4") {
		t.Errorf("the cursor became the core's offset: %s", args)
	}
}

// The paging members reconciliation adds are counted by the budget report. A result
// the budget trimmed carries has_more (and often a cursor), and those bytes are written
// after the report was fitted, so a report settled before reconciliation names a length
// the response does not have. Here the stub core emits a tier large enough that the
// budget trims it, and the answer's own bytes must match what output_budget reports.
func TestImpactBudgetReportCountsTheReconciledPagingMembers(t *testing.T) {
	srv, dir := newRouteServer(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	// A tier of 400 items with a successor: the budget cuts it, so reconciliation
	// re-signs the cursor and sets has_more after the report is fitted.
	script := `#!/bin/sh
items=""
i=0
while [ $i -lt 400 ]; do
  items="$items{\"name\":\"SymbolWithAFairlyLongName$i\",\"depth\":1,\"kind\":\"function\",\"path\":\"pkg/deep/nested/path/to/file$i.go\"},"
  i=$((i + 1))
done
printf '{"symbol":"Serve","impacted_count":400,"by_depth":[{"depth":1,"total":400,"offset":0,"items":[%s],"next_offset":400}]}' "${items%,}"
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpactBudget", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	// max_chars above the ladder's floor leaves room for part of the tier, so the
	// tiers rung cuts it and reconciliation adds the paging members.
	resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index?symbol=Serve&limit=50&max_chars=2000")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var res struct {
		HasMore      *bool `json:"has_more"`
		OutputBudget *struct {
			MaxChars   int  `json:"max_chars"`
			UsedChars  int  `json:"used_chars"`
			OverBudget bool `json:"over_budget"`
		} `json:"output_budget"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	if res.OutputBudget == nil {
		t.Fatalf("no output_budget report: %s", body)
	}
	// used_chars is the budget layer's own settled total for the encoded result
	// (toolbudget's apply_test asserts rep.UsedChars == len(out)), so the response
	// body minus the newline writeJSON appends must equal it.
	if got, want := len(body)-1, res.OutputBudget.UsedChars; got != want {
		t.Errorf("the encoded result is %d bytes, used_chars says %d: the report was settled before the paging members were added",
			got, res.OutputBudget.UsedChars)
	}
	if res.HasMore == nil {
		t.Fatalf("a trimmed tier reported neither has_more nor a cursor: %s", body)
	}
	if res.OutputBudget.OverBudget || len(body) > res.OutputBudget.MaxChars {
		t.Errorf("a reconciled answer of %d bytes over max_chars %d (over_budget=%v)",
			len(body), res.OutputBudget.MaxChars, res.OutputBudget.OverBudget)
	}
}

// The core's refusal of a scope= request (a revision that names no commit) is a 400
// that names it; any other core failure stays a sanitized 500.
func TestImpactScopeRefusedByTheCoreIsABadRequest(t *testing.T) {
	srv, dir := newRouteServer(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	script := `#!/bin/sh
if [ "$1 $2" = "symbolgraph changes" ]; then
  case "$*" in
  *--base=nope*) echo 'symbolgraph changes: revision "nope" does not name a commit' >&2; exit 2 ;;
  esac
  echo 'boom /secret/host/path' >&2; exit 1
fi
echo '{}'
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpactRefused", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q, has, hasNot string
		status         int
	}{
		{"?scope=range&base=nope", `does not name a commit`, "", http.StatusBadRequest},
		{"?scope=range&base=main", "", "/secret/host/path", http.StatusInternalServerError},
	} {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index" + c.q)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || !strings.Contains(string(body), c.has) ||
			(c.hasNot != "" && strings.Contains(string(body), c.hasNot)) {
			t.Fatalf("%s: %d %s", c.q, resp.StatusCode, body)
		}
	}
}

// tierAnswer is the part of a budgeted impact answer this file asserts on: the tier items
// the caller received, the cursor that continues them, and the report that has to describe
// the bytes written.
type tierAnswer struct {
	ByDepth []struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	} `json:"by_depth"`
	NextCursor   string `json:"next_cursor"`
	OutputBudget *struct {
		UsedChars int `json:"used_chars"`
	} `json:"output_budget"`
}

func (a tierAnswer) delivered() int {
	if len(a.ByDepth) == 0 {
		return 0
	}
	return len(a.ByDepth[0].Items)
}

// Settling a reconciled result can push it over max_chars again, and when it does the
// second fit removes items the cursor reconciliation just signed no longer describes:
// following it would skip them. The route runs two passes for that reason — reconcile,
// settle, reconcile again against what survived, settle the report. The fixture below is
// sized so the reconciled page fits while the settled one does not, which is the window in
// which the second fit has to trim; the sweep walks max_chars across it.
//
// This asserts the contract rather than forcing the second pass: it is green with one
// pass and with two, because fitEach binary-searches the item count against max_chars, so
// after Apply's fit the page already sits at the boundary and the second pass trims back
// to the count the first reconcile signed. That is recorded in FU rather than papered
// over with a weaker assertion.
func TestImpactCursorIsReDerivedWhenTheSettledPageIsTrimmedAgain(t *testing.T) {
	srv, dir := newRouteServer(t)
	core := filepath.Join(t.TempDir(), "xmustard-core")
	// 60 items of a realistic size against total 120, so the core page is several times
	// the budget floor and the tiers rung has to cut it.
	script := `#!/bin/sh
items=""
i=0
while [ $i -lt 60 ]; do
  items="$items{\"name\":\"ImpactedSymbolWithALongNameNumber$i\",\"depth\":1,\"kind\":\"function\",\"path\":\"pkg/deeply/nested/package/path/file$i.go\"},"
  i=$((i + 1))
done
printf '{"symbol":"Serve","impacted_count":120,"by_depth":[{"depth":1,"total":120,"offset":0,"items":[%s],"next_offset":60}]}' "${items%,}"
`
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	ws, root := "wsImpactSecondFit", t.TempDir()
	snap, _ := json.Marshal(map[string]any{"workspace": map[string]any{"workspace_id": ws, "root_path": root}})
	if err := os.MkdirAll(filepath.Join(dir, "workspaces", ws), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspaces", ws, "snapshot.json"), snap, 0o644); err != nil {
		t.Fatal(err)
	}
	// The ranking the cursor is signed over: the query the route builds for this call.
	q := url.Values{"symbol": {"Serve"}, "limit": {"60"}}
	// The floor is the smallest budget accepted and the paging members add about 97 bytes,
	// so the window where the reconciled page fits but the settled one does not sits just
	// above it.
	cursors, checked := 0, 0
	for maxChars := toolbudget.MinMaxChars; maxChars <= toolbudget.MinMaxChars+600; maxChars += 5 {
		resp, err := http.Get(srv.URL + "/api/workspaces/" + ws + "/changes/since-index?symbol=Serve&limit=60&max_chars=" + strconv.Itoa(maxChars))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("max_chars=%d: status %d: %s", maxChars, resp.StatusCode, body)
		}
		var res tierAnswer
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatal(err)
		}
		// On every budget the report describes the bytes actually written, which is the
		// invariant the second pass exists to keep.
		if res.OutputBudget == nil || len(body)-1 != res.OutputBudget.UsedChars {
			t.Errorf("max_chars=%d: used_chars %+v does not describe the %d bytes written",
				maxChars, res.OutputBudget, len(body)-1)
		}
		if res.NextCursor == "" {
			continue
		}
		cursors++
		offset, err := workspaceops.DecodeImpactCursor(ws, q, res.NextCursor)
		if err != nil {
			t.Errorf("max_chars=%d: the answer's own cursor does not decode: %v", maxChars, err)
			continue
		}
		// The cursor must name the first item this page did not deliver. Past it, the
		// caller skips an item it never received; short of it, the caller repeats one.
		if got := res.delivered(); offset != got {
			t.Errorf("max_chars=%d: %d items delivered, the cursor continues at %d: it %s",
				maxChars, got, offset,
				map[bool]string{true: "skips the items this fit removed", false: "repeats an item"}[offset > got])
			continue
		}
		checked++
	}
	if cursors == 0 {
		t.Fatalf("no budget in [%d,%d] produced a cursor: the fixture never reached the window this test is about",
			toolbudget.MinMaxChars, toolbudget.MinMaxChars+600)
	}
	if checked == 0 {
		t.Error("every cursor in the sweep named the wrong position")
	}
	// What this test does NOT prove, and why (WS-59e-r5-5): the sweep is green with a
	// single pass, because toolbudget's fitEach binary-searches the item count against
	// max_chars, so after the first fit the page already sits at the boundary and the
	// second pass trims back to the count the first reconcile signed. M22 (drop the
	// second pass) survived five attempts through this route. The invariant asserted
	// above -- used_chars describes the bytes written, and a cursor names the first
	// undelivered item -- holds either way, which is exactly why the mutation survives:
	// the second pass is correct by construction and unreachable through the route at
	// these budgets. The M22 row in reviews/ws-59e/r5/slop-audit.md is the honest
	// record of that, not this sweep.
}
