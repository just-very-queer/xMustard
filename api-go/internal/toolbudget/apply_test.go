package toolbudget

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// envelopeMembers are coverage and freshness shaped like the core's, with lists the
// signal_counts rung reduces.
func envelopeMembers(losses, dirty int) map[string]any {
	lossList := make([]map[string]any, losses)
	for i := range lossList {
		lossList[i] = map[string]any{"path": fmt.Sprintf("pkg/lost_%03d.go", i), "reason": "lexical_fallback", "content_indexed": true}
	}
	dirtyList := make([]string, dirty)
	for i := range dirtyList {
		dirtyList[i] = fmt.Sprintf("pkg/dirty_%03d.go", i)
	}
	return map[string]any{
		"coverage": map[string]any{"complete": false, "truncated": false, "indexed_files": 568,
			"loss_counts": map[string]any{"lexical_fallback": losses}, "losses": lossList,
			"languages": map[string]any{"go": map[string]int{"supported": 590, "failed": 6}, "rust": map[string]int{"supported": 77, "failed": 5},
				"python": map[string]int{"supported": 8, "failed": 2}},
			"source_identity": map[string]any{"head": "452da1e09e2c230b829c1d824bd82f83fa187dec", "identity_complete": true,
				"key": "a156582ef7dd69692cf8bae19d43cd2ba01007676dce6106b1c1f3a1db27afe4", "limitations": 0,
				"parser_version": "xm-index-2;ts-abi-15;rust-0.24", "stable": true},
			"work": map[string]any{"graph_cache": "hit", "files_parsed": 0, "files_reused": 568}},
		"freshness": map[string]any{"source": "index", "status": "current", "dirty_paths_touching_result": dirtyList,
			"refresh": map[string]any{"reparsed": 568, "reresolved": 568, "escalated": false}},
	}
}

// searchResult is a hybrid search page shaped like the core's: every hit with three
// snippet windows, reasons (and their joined reason), scores and lanes.
func searchResult(hits, nameLen int) map[string]any {
	list := make([]map[string]any, hits)
	for i := range list {
		name := fmt.Sprintf("Func%03d", i) + strings.Repeat("x", nameLen)
		reasons := []string{"normalized identifier match: " + name, "window coverage 2/3 query words at lines 10-12", "bm25: every query term in the chunk"}
		var snip []map[string]any
		for w := range 3 {
			snip = append(snip, map[string]any{"line": 10 + w, "text": fmt.Sprintf("%d: func %s() {\n%d:   body line of the function %03d\n%d: }", 9+w, name, 10+w, i, 11+w)})
		}
		list[i] = map[string]any{
			"kind": "symbol", "name": name, "path": "pkg/" + strings.Repeat("d", nameLen) + fmt.Sprintf("/file_%03d.go", i%4), "line": 10 + i, "lines": []int{9, 30},
			"uid": "Function:pkg/file.go:" + name, "score": 0.05, "scores": map[string]any{"bm25": 8.9, "name": 7.9, "rrf": 0.02},
			"lanes_matched": []string{"bm25", "name"}, "reasons": reasons, "reason": strings.Join(reasons, " · "), "snippet": snip,
		}
	}
	r := map[string]any{"workspace_id": "ws", "query": "retry backoff", "total": 40, "offset": 0, "omitted": 40 - hits,
		"hits": list, "degradations": []string{"query terms past the first 12 are not searched"}, "generated_at": "t",
		"next_cursor": "ORIGINAL"}
	for k, v := range envelopeMembers(6, 3) {
		r[k] = v
	}
	return r
}

type applied struct {
	out map[string]any
	rep report
	raw []byte
}

func run(t *testing.T, tool string, result map[string]any, req Request, page Page, retain func() Recovery) applied {
	t.Helper()
	spec, ok := For(tool)
	if !ok {
		t.Fatalf("no spec for %s", tool)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	out, err := spec.Apply(raw, req, page, retain)
	if err != nil {
		t.Fatal(err)
	}
	out = append([]byte(nil), out...)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	var rep report
	if req.MaxChars > 0 {
		b, _ := json.Marshal(got[ReportMember])
		if err := json.Unmarshal(b, &rep); err != nil {
			t.Fatal(err)
		}
		if rep.UsedChars != len(out) {
			t.Fatalf("used_chars %d, output is %d bytes", rep.UsedChars, len(out))
		}
		if rep.OverBudget != (len(out) > req.MaxChars) {
			t.Fatalf("over_budget=%v for %d bytes against %d", rep.OverBudget, len(out), req.MaxChars)
		}
	}
	return applied{got, rep, out}
}

func cursorAt(offset int) string { return fmt.Sprintf("C%d", offset) }

// fullSize is the size of result budgeted at the largest max_chars: where a walk down
// the ladder starts.
func fullSize(t *testing.T, tool string, result map[string]any) int {
	t.Helper()
	return len(run(t, tool, result, Request{MaxChars: MaxMaxChars}, Page{Cursor: cursorAt}, nil).raw)
}

func TestSmallResultIsReturnedInFull(t *testing.T) {
	r := searchResult(2, 0)
	a := run(t, "search", r, Request{MaxChars: 65536}, Page{Cursor: cursorAt}, nil)
	if a.rep.DegradedStage != "full" || len(a.rep.Stages) != 0 || a.rep.Docs != "" {
		t.Fatalf("a fitting result is not reduced: %+v", a.rep)
	}
	if hits := a.out["hits"].([]any); len(hits) != 2 || hits[0].(map[string]any)["reason"] == nil {
		t.Fatalf("hits changed: %v", hits)
	}
	if a.out["has_more"] != true || a.out["next_cursor"] != "ORIGINAL" {
		t.Fatalf("the producer's cursor must stand when nothing was cut: %v %v", a.out["has_more"], a.out["next_cursor"])
	}
}

// Every rung of the search ladder is reached, and named, as max_chars shrinks; the
// stage never moves back up, and the signals survive every stage.
func TestEachSearchStageIsReachedAndNamed(t *testing.T) {
	r := searchResult(10, 600)
	rungs := searchLadders[0].Rungs
	seen := map[string]bool{}
	last := -1
	for mc := fullSize(t, "search", r); mc >= MinMaxChars; mc -= 50 {
		a := run(t, "search", r, Request{MaxChars: mc}, Page{Cursor: cursorAt}, nil)
		stage := a.rep.DegradedStage
		idx := slices.IndexFunc(rungs, func(x Rung) bool { return x.Name == stage })
		if stage != "full" && idx < 0 {
			t.Fatalf("unknown stage %q", stage)
		}
		if idx < last {
			t.Fatalf("stage moved back from %s to %s at max_chars=%d", rungs[last].Name, stage, mc)
		}
		last = max(last, idx)
		seen[stage] = true
		for _, k := range []string{"total", "omitted", "degradations", "coverage", "freshness", "query"} {
			if _, ok := a.out[k]; !ok {
				t.Fatalf("stage %s dropped %s", stage, k)
			}
		}
		cov := a.out["coverage"].(map[string]any)
		if cov["complete"] != false || cov["loss_counts"] == nil {
			t.Fatalf("stage %s dropped a coverage signal: %v", stage, cov)
		}
		if a.out["freshness"].(map[string]any)["status"] != "current" {
			t.Fatalf("stage %s dropped freshness.status", stage)
		}
		if !a.rep.OverBudget && len(a.raw) > mc {
			t.Fatalf("stage %s: %d bytes over %d without over_budget", stage, len(a.raw), mc)
		}
	}
	for _, r := range rungs {
		if !seen[r.Name] {
			t.Errorf("rung %s never reached (seen %v)", r.Name, seen)
		}
	}
}

func TestSearchRungsShapeHits(t *testing.T) {
	r := searchResult(10, 0)
	full := run(t, "search", r, Request{MaxChars: MaxMaxChars}, Page{}, nil)
	size := len(full.raw)
	// find the stage just past one_snippet: every hit keeps one window, no reason
	for max := size - 1; max > MinMaxChars; max -= 25 {
		a := run(t, "search", r, Request{MaxChars: max}, Page{Cursor: cursorAt}, nil)
		if a.rep.DegradedStage != "one_snippet" {
			continue
		}
		for _, h := range a.out["hits"].([]any) {
			hit := h.(map[string]any)
			if _, ok := hit["reason"]; ok {
				t.Fatal("one_snippet keeps reason")
			}
			if n := len(hit["snippet"].([]any)); n != 1 {
				t.Fatalf("one_snippet kept %d windows", n)
			}
			if hit["reasons"] == nil || hit["scores"] == nil {
				t.Fatal("one_snippet dropped reasons or scores")
			}
		}
		return
	}
	t.Fatal("one_snippet never reached")
}

func TestTrimmedPageContinuesAtTheFirstHitNotReturned(t *testing.T) {
	r := searchResult(10, 600)
	r["offset"] = 20
	for max := 12000; max >= MinMaxChars; max -= 100 {
		a := run(t, "search", r, Request{MaxChars: max}, Page{Offset: 20, Cursor: cursorAt}, nil)
		if a.rep.DegradedStage != "trimmed" {
			continue
		}
		kept := len(a.out["hits"].([]any))
		if kept == 0 || kept >= 10 {
			t.Fatalf("trimmed kept %d hits", kept)
		}
		if a.out["has_more"] != true || a.out["next_cursor"] != cursorAt(20+kept) {
			t.Fatalf("has_more=%v next_cursor=%v, want C%d", a.out["has_more"], a.out["next_cursor"], 20+kept)
		}
		lr := a.rep.Lists["hits"]
		if lr.Kept != kept || lr.Total != 10 || lr.State != "trimmed" || len(lr.By) == 0 {
			t.Fatalf("list report %+v", lr)
		}
		if a.out["offset"] != float64(20) {
			t.Fatalf("the producer's offset must stand: %v", a.out["offset"])
		}
		return
	}
	t.Fatal("trimmed never reached")
}

// A trimmed rung cuts hits, so the producer's page `omitted` (hits after the page)
// understates what the delivered answer left out. `omitted` counts the ranked hits
// the result does not carry: total - the hits actually delivered, at any offset and
// at every rung that returns hits (WS-FIX-18).
func TestOmittedCountsTheHitsTheResultDoesNotCarry(t *testing.T) {
	for _, offset := range []int{0, 20} {
		r := searchResult(10, 600)
		r["offset"] = offset
		full := run(t, "search", r, Request{MaxChars: MaxMaxChars}, Page{Offset: offset, Cursor: cursorAt}, nil)
		// with nothing cut, the producer's value stands
		if got := full.out["omitted"]; got != 30.0 {
			t.Fatalf("offset %d uncut omitted=%v, want the producer's 30", offset, got)
		}
		for max := fullSize(t, "search", r); max >= MinMaxChars; max -= 25 {
			a := run(t, "search", r, Request{MaxChars: max}, Page{Offset: offset, Cursor: cursorAt}, nil)
			delivered := 0
			if h, ok := a.out["hits"].([]any); ok {
				delivered = len(h)
			}
			if a.out["total"] == nil {
				continue
			}
			want := a.out["total"].(float64) - float64(delivered)
			if got := a.out["omitted"]; got != want {
				t.Fatalf("offset %d stage %s max_chars %d: omitted=%v, want total-len(hits)=%v (delivered %d)",
					offset, a.rep.DegradedStage, max, got, want, delivered)
			}
		}
	}
}

func TestCountsRungGroupsByPath(t *testing.T) {
	r := searchResult(10, 600)
	for max := 8000; max >= MinMaxChars; max -= 50 {
		a := run(t, "search", r, Request{MaxChars: max}, Page{Cursor: cursorAt}, nil)
		if a.rep.DegradedStage != "counts" {
			continue
		}
		if _, ok := a.out["hits"]; ok {
			t.Fatal("counts keeps hits")
		}
		by := a.out["hits_by_path"].(map[string]any)
		total := 0
		for _, v := range by {
			total += int(v.(float64))
		}
		if total != 10 || len(by) != 4 {
			t.Fatalf("hits_by_path %v", by)
		}
		if a.out["next_cursor"] != nil {
			// offset 0 with nothing kept: no item was ever returned, so there is no
			// valid next offset to sign (has_more/recover_uri still report the cut).
			t.Fatalf("counts at the first page issues no cursor: %v", a.out["next_cursor"])
		}
		return
	}
	t.Fatal("counts never reached")
}

func TestSignalCountsKeepFlagsAndReportLengths(t *testing.T) {
	r := searchResult(10, 600)
	for mc := fullSize(t, "search", r); mc >= MinMaxChars; mc -= 50 {
		a := run(t, "search", r, Request{MaxChars: mc}, Page{Cursor: cursorAt}, nil)
		if a.rep.DegradedStage != "signal_counts" {
			continue
		}
		cov := a.out["coverage"].(map[string]any)
		if _, ok := cov["losses"]; ok {
			t.Fatal("signal_counts keeps coverage.losses")
		}
		if cov["complete"] != false || cov["indexed_files"] != float64(568) {
			t.Fatalf("signal_counts dropped a coverage scalar: %v", cov)
		}
		if a.rep.Signals["coverage.losses"] != 6 || a.rep.Signals["freshness.dirty_paths_touching_result"] != 3 {
			t.Fatalf("signals %v", a.rep.Signals)
		}
		return
	}
	t.Fatal("signal_counts never reached")
}

func TestSignalFlagsKeepScalarsAndLossCounts(t *testing.T) {
	r := searchResult(10, 600)
	for mc := fullSize(t, "search", r); mc >= MinMaxChars; mc -= 50 {
		a := run(t, "search", r, Request{MaxChars: mc}, Page{Cursor: cursorAt}, nil)
		if a.rep.DegradedStage != "signal_flags" {
			continue
		}
		cov := a.out["coverage"].(map[string]any)
		if _, ok := cov["languages"]; ok {
			t.Fatal("signal_flags keeps coverage.languages")
		}
		if cov["loss_counts"] == nil || cov["complete"] != false || cov["indexed_files"] != float64(568) ||
			cov["work"].(map[string]any)["graph_cache"] != "hit" {
			t.Fatalf("signal_flags dropped a kept member: %v", cov)
		}
		// the third kept nested object: whether the source identity was stable
		si := cov["source_identity"].(map[string]any)
		if si["stable"] != true || si["identity_complete"] != true || si["head"] != "452da1e09e2c230b829c1d824bd82f83fa187dec" ||
			si["key"] != "a156582ef7dd69692cf8bae19d43cd2ba01007676dce6106b1c1f3a1db27afe4" || si["limitations"] != float64(0) {
			t.Fatalf("signal_flags dropped coverage.source_identity: %v", si)
		}
		fr := a.out["freshness"].(map[string]any)
		if _, ok := fr["refresh"]; ok || fr["status"] != "current" {
			t.Fatalf("freshness: %v", fr)
		}
		if a.rep.Signals["coverage.languages"] != 3 || a.rep.Signals["freshness.refresh"] != 3 || a.rep.Signals["coverage.losses"] != 6 {
			t.Fatalf("signals %v", a.rep.Signals)
		}
		if _, ok := a.rep.Signals["coverage.loss_counts"]; ok || a.rep.Signals["coverage.work"] != 0 {
			t.Fatal("a kept member is not reported as removed")
		}
		if _, ok := a.rep.Signals["coverage.source_identity"]; ok {
			t.Fatal("signal_flags reports the kept coverage.source_identity as removed")
		}
		return
	}
	t.Fatal("signal_flags never reached")
}

// explain pages its list itself: the page is cut first, then fitted.
func TestSlicedPageAndCursor(t *testing.T) {
	long := strings.Repeat("n", 1800)
	syms := make([]map[string]any, 12)
	for i := range syms {
		syms[i] = map[string]any{"name_path": fmt.Sprintf("Type%02d%s", i, long), "kind": "Struct", "lines": []int{i * 10, i*10 + 9},
			"children": []map[string]any{{"name_path": fmt.Sprintf("Type%02d/method%s", i, long), "kind": "Method", "lines": []int{i*10 + 1, i*10 + 3}}}}
	}
	var files []string
	for i := range 20 {
		files = append(files, fmt.Sprintf("pkg/%s/file_%02d.go", long[:60], i))
	}
	res := map[string]any{"path": "pkg/a.go", "symbols": syms, "symbols_total": 12, "symbols_by_kind": map[string]int{"Struct": 12},
		"cluster": map[string]any{"cluster_id": 1, "label": "pkg", "size": 20, "files": files}}
	a := run(t, "explain", res, Request{}, Page{Slice: true, Offset: 2, Limit: 3, Cursor: cursorAt}, nil)
	got := a.out["symbols"].([]any)
	if len(got) != 3 || !strings.HasPrefix(got[0].(map[string]any)["name_path"].(string), "Type02") {
		t.Fatalf("page %v", got)
	}
	if a.out["has_more"] != true || a.out["next_cursor"] != cursorAt(5) || a.out["offset"] != float64(2) {
		t.Fatalf("paging members: %v %v %v", a.out["has_more"], a.out["next_cursor"], a.out["offset"])
	}
	if _, ok := a.out[ReportMember]; ok {
		t.Fatal("paging alone adds no report")
	}
	last := run(t, "explain", res, Request{}, Page{Slice: true, Offset: 9, Limit: 5, Cursor: cursorAt}, nil)
	if last.out["has_more"] != false || last.out["next_cursor"] != nil || len(last.out["symbols"].([]any)) != 3 {
		t.Fatalf("last page: %v", last.out)
	}
	// budgeted: the cluster trims first, then children go, then fewer symbols
	var stages []string
	for mc := 40000; mc >= MinMaxChars; mc -= 20 {
		b := run(t, "explain", res, Request{MaxChars: mc}, Page{Slice: true, Offset: 2, Limit: 8, Cursor: cursorAt}, nil)
		if !slices.Contains(stages, b.rep.DegradedStage) {
			stages = append(stages, b.rep.DegradedStage)
		}
		if b.rep.DegradedStage == "trimmed" {
			kept := len(b.out["symbols"].([]any))
			if b.out["next_cursor"] != cursorAt(2+kept) {
				t.Fatalf("trimmed page continues at %v, want C%d", b.out["next_cursor"], 2+kept)
			}
		}
		if b.rep.DegradedStage == "counts" {
			if _, ok := b.out["symbols"]; ok || b.out["symbols_by_kind"] == nil {
				t.Fatalf("counts keeps only symbols_by_kind: %v", b.out)
			}
		}
		if cl, ok := b.out["cluster"].(map[string]any); !ok || cl["size"] != float64(20) {
			t.Fatalf("the cluster object and its size stay: %v", b.out["cluster"])
		}
	}
	want := []string{"full", "cluster_trimmed", "depth0", "trimmed", "counts"}
	if !slices.Equal(stages, want) {
		t.Fatalf("explain stages %v, want %v", stages, want)
	}
}

func TestRecoveryIsNamedOnlyWhenReduced(t *testing.T) {
	calls := 0
	retain := func() Recovery { calls++; return Recovery{Handle: "h1", URI: "xmustard://evidence/h1"} }
	a := run(t, "search", searchResult(2, 0), Request{MaxChars: MaxMaxChars}, Page{}, retain)
	if calls != 0 || a.rep.RecoverURI != "" {
		t.Fatalf("retained a full result: calls=%d %+v", calls, a.rep)
	}
	b := run(t, "search", searchResult(10, 600), Request{MaxChars: 4000}, Page{Cursor: cursorAt}, retain)
	if calls != 1 || b.rep.RecoverURI != "xmustard://evidence/h1" || b.rep.RecoverHandle != "h1" || b.rep.Docs != DocsURI || b.rep.More == "" {
		t.Fatalf("calls=%d report %+v", calls, b.rep)
	}
	u := run(t, "search", searchResult(10, 600), Request{MaxChars: 4000}, Page{},
		func() Recovery { return Recovery{Unavailable: "not a delivered call"} })
	if u.rep.RecoverUnavailable != "not a delivered call" {
		t.Fatalf("unavailable recovery not named: %+v", u.rep)
	}
}

func TestImpactBlastRadiusTrimsNearestFirst(t *testing.T) {
	impacted := make([]map[string]any, 300)
	for i := range impacted {
		impacted[i] = map[string]any{"path": fmt.Sprintf("internal/pkg%02d/file_%03d.go", i%17, i), "distance": 1 + i/100}
	}
	res := map[string]any{"symbol": "Capture", "defined_in": []string{"internal/evidence/store.go"}, "impacted": impacted,
		"impacted_count": 300, "max_depth": 4}
	for k, v := range envelopeMembers(2, 1) {
		res[k] = v
	}
	a := run(t, "impact", res, Request{MaxChars: 8000}, Page{}, nil)
	if a.rep.DegradedStage != "trimmed" {
		t.Fatalf("stage %s", a.rep.DegradedStage)
	}
	got := a.out["impacted"].([]any)
	if got[0].(map[string]any)["distance"] != float64(1) || len(got) >= 300 {
		t.Fatalf("impacted %d items", len(got))
	}
	lr := a.rep.Lists["impacted"]
	if lr.Total != 300 || lr.By["1"] != 100 || lr.By["3"] != 100 {
		t.Fatalf("list report %+v", lr)
	}
	if _, ok := a.out["has_more"]; ok {
		t.Fatal("impact has no paged list: no has_more member (WS-35 pages impact)")
	}
	small := run(t, "impact", res, Request{MaxChars: MinMaxChars}, Page{}, nil)
	if small.rep.OverBudget || small.out["impacted_count"] != float64(300) || small.out["coverage"] == nil {
		t.Fatalf("at the floor: %s", small.raw)
	}
}

// The WS-35 impact v2 shape: every depth tier keeps the same leading items.
func TestByDepthTiersKeepTheirFirstItems(t *testing.T) {
	var tiers []map[string]any
	for d := 1; d <= 3; d++ {
		items := make([]map[string]any, 40)
		for i := range items {
			items[i] = map[string]any{"name": fmt.Sprintf("caller_%d_%02d", d, i), "path": fmt.Sprintf("pkg/f%02d.go", i), "line": i, "edge": "CALLS", "confidence": 0.9}
		}
		tiers = append(tiers, map[string]any{"depth": d, "label": "WILL BREAK", "items": items, "offset": 0, "total": 40})
	}
	res := map[string]any{"symbol": "Add", "by_depth": tiers, "counts": map[string]any{"total": 120, "by_depth": []int{40, 40, 40}},
		"epistemic": map[string]any{"completeness": "exact"}, "risk": "HIGH"}
	a := run(t, "impact", res, Request{MaxChars: 4000}, Page{}, nil)
	if a.rep.DegradedStage != "tiers_trimmed" {
		t.Fatalf("stage %s", a.rep.DegradedStage)
	}
	var kept []int
	for _, tr := range a.out["by_depth"].([]any) {
		tier := tr.(map[string]any)
		kept = append(kept, len(tier["items"].([]any)))
		if tier["total"] != float64(40) {
			t.Fatal("a tier's total dropped")
		}
	}
	if kept[0] == 0 || kept[0] >= 40 || kept[0] != kept[1] || kept[1] != kept[2] {
		t.Fatalf("tiers kept %v", kept)
	}
	if lr := a.rep.Lists["by_depth"]; lr.EachKept != kept[0] || lr.Total != 3 {
		t.Fatalf("list report %+v, want each_kept %d", lr, kept[0])
	}
	if a.out["risk"] != "HIGH" {
		t.Fatal("risk dropped")
	}
}

// The WS-35 scope shape: its dependents live in affected.by_depth, a nested list of
// tiers, which keeps the same leading items per tier.
func TestImpactScopeTrimsNestedTiers(t *testing.T) {
	items := make([]map[string]any, 60)
	for i := range items {
		items[i] = map[string]any{"name": fmt.Sprintf("caller_%02d", i), "path": fmt.Sprintf("pkg/f%02d.go", i), "line": i, "edge": "CALLS"}
	}
	res := map[string]any{"scope": "all", "changed_files": []map[string]any{{"path": "pkg/a.go", "change": "modified"}}, "changed_files_total": 1,
		"changed_symbols": []map[string]any{{"name": "Add", "path": "pkg/a.go", "contract_break": true}}, "contract_breaks": 1,
		"affected": map[string]any{"risk": "HIGH", "counts": map[string]any{"total": 60},
			"by_depth": []map[string]any{{"depth": 1, "items": items, "total": 60}, {"depth": 2, "items": items, "total": 60}}}}
	a := run(t, "impact", res, Request{MaxChars: 4000}, Page{}, nil)
	if a.rep.DegradedStage != "tiers_trimmed" {
		t.Fatalf("stage %s (%v)", a.rep.DegradedStage, a.rep.Stages)
	}
	aff := a.out["affected"].(map[string]any)
	tiers := aff["by_depth"].([]any)
	n0, n1 := len(tiers[0].(map[string]any)["items"].([]any)), len(tiers[1].(map[string]any)["items"].([]any))
	if n0 == 0 || n0 >= 60 || n0 != n1 || aff["risk"] != "HIGH" {
		t.Fatalf("tiers kept %d and %d: %v", n0, n1, aff)
	}
	if a.out["contract_breaks"] != float64(1) || len(a.out["changed_symbols"].([]any)) != 1 {
		t.Fatal("changed symbols and breaks stay")
	}
	hops := make([]map[string]any, 30)
	for i := range hops {
		hops[i] = map[string]any{"edge": "CALLS", "from": map[string]any{"name": fmt.Sprintf("f%02d", i), "path": "pkg/x.go"},
			"to": map[string]any{"name": fmt.Sprintf("f%02d", i+1), "path": "pkg/y.go"}, "provenance": "scope", "confidence": 0.9}
	}
	trace := run(t, "impact", map[string]any{"found": true, "path": []string{"pkg/x.go", "pkg/y.go"}, "length": 30, "hops": hops},
		Request{MaxChars: MinMaxChars}, Page{}, nil)
	if trace.rep.DegradedStage != "hops_trimmed" || len(trace.out["path"].([]any)) != 2 {
		t.Fatalf("trace: %s %v", trace.rep.DegradedStage, trace.out)
	}
}

func TestImpactChangesListsBreaksLast(t *testing.T) {
	files := make([]map[string]any, 200)
	for i := range files {
		files[i] = map[string]any{"path": fmt.Sprintf("pkg/f%03d.go", i), "change": "modified"}
	}
	syms := make([]map[string]any, 200)
	for i := range syms {
		syms[i] = map[string]any{"path": fmt.Sprintf("pkg/f%03d.go", i), "symbol": fmt.Sprintf("F%03d", i), "kind": "function",
			"change": "modified", "contract_break": i < 5}
	}
	res := map[string]any{"since": "baseline", "has_baseline": true, "changed_files": files, "changed_files_total": 200,
		"dirty_symbols": syms, "dirty_symbols_total": 200, "contract_breaks": 5}
	a := run(t, "impact", res, Request{MaxChars: 6000}, Page{}, nil)
	if a.out["contract_breaks"] != float64(5) {
		t.Fatal("contract_breaks dropped")
	}
	if _, ok := a.out["changed_files"]; ok {
		t.Fatalf("changed files must go before dirty symbols are trimmed (stage %s)", a.rep.DegradedStage)
	}
	ds := a.out["dirty_symbols"].([]any)
	for i := range 5 {
		if ds[i].(map[string]any)["contract_break"] != true {
			t.Fatal("contract breaks are listed first and kept")
		}
	}
}

// TestExplainFirstPageCursorIsFollowable regression-tests the real ExplainCursor and
// DecodeExplainCursor (not the test's cursorAt stub) through Apply. When the first
// page (offset 0) is reduced past the "symbols" list to counts/total, no item was
// ever returned, so a next_cursor must not be signed: DecodeExplainCursor rejects any
// offset <= 0, and previously nextCursor still called Cursor(0) at that rung.
func TestExplainFirstPageCursorIsFollowable(t *testing.T) {
	q := url.Values{"path": {"pkg/a.go"}}
	cursor := workspaceops.ExplainCursor("ws", q)
	long := strings.Repeat("n", 1800)
	syms := make([]map[string]any, 5)
	for i := range syms {
		syms[i] = map[string]any{"name_path": fmt.Sprintf("Type%02d%s", i, long), "kind": "Struct", "lines": []int{i, i + 1}}
	}
	res := map[string]any{"path": "pkg/a.go", "explanation": strings.Repeat("x", 1200), "symbols": syms, "symbols_total": 5}
	var a applied
	for mc := 2000; mc >= MinMaxChars; mc -= 20 {
		a = run(t, "explain", res, Request{MaxChars: mc}, Page{Slice: true, Offset: 0, Cursor: cursor}, nil)
		if a.rep.DegradedStage == "counts" {
			break
		}
	}
	if a.rep.DegradedStage != "counts" {
		t.Fatalf("want the counts rung, got %q", a.rep.DegradedStage)
	}
	nc, _ := a.out["next_cursor"].(string)
	if nc != "" {
		if _, err := workspaceops.DecodeExplainCursor("ws", q, nc); err != nil {
			t.Fatalf("next_cursor %q is not a next_cursor of this explain call: %v", nc, err)
		}
	}
}

func TestOmitRungsNameWhatTheyRemove(t *testing.T) {
	res := map[string]any{
		"symbol":   map[string]any{"uid": "Function:a.go:F#0", "name_path": "F", "content": strings.Repeat("x", 3000)},
		"incoming": []map[string]any{{"relation": "CALLS", "path": "b.go", "line": 3, "snippet": "3: F()"}},
		"outgoing": []map[string]any{{"relation": "CALLS", "path": "c.go", "name": "G"}},
	}
	a := run(t, "explain", res, Request{MaxChars: 2000}, Page{}, nil)
	sym := a.out["symbol"].(map[string]any)
	if _, ok := sym["content"]; ok || sym["uid"] == nil {
		t.Fatalf("no_content: %v", sym)
	}
	if !slices.Contains(a.rep.Omitted, "symbol.content") || a.rep.DegradedStage != "no_content" {
		t.Fatalf("report %+v", a.rep)
	}
}

func TestStaleReportAndNonObjects(t *testing.T) {
	r := searchResult(1, 0)
	r[ReportMember] = map[string]any{"stale": true}
	a := run(t, "search", r, Request{MaxChars: MaxMaxChars}, Page{}, nil)
	if a.out[ReportMember].(map[string]any)["stale"] != nil {
		t.Fatal("a stale report must be replaced")
	}
	spec, _ := For("search")
	for _, in := range []string{`[1,2]`, `"text"`, `null`} {
		out, err := spec.Apply([]byte(in), Request{MaxChars: MinMaxChars}, Page{}, nil)
		if err != nil || string(out) != in {
			t.Fatalf("%s: %s %v", in, out, err)
		}
	}
	if _, err := spec.Apply([]byte(`{"a":`), Request{MaxChars: MinMaxChars}, Page{}, nil); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestParseRequestRejectsNeverClamps(t *testing.T) {
	search, _ := For("search")
	explain, _ := For("explain")
	cases := []struct {
		spec *Spec
		q    string
		arg  string
	}{
		{search, "max_chars=1999", "max_chars"},
		{search, "max_chars=65537", "max_chars"},
		{search, "max_chars=abc", "max_chars"},
		{search, "max_chars=2000&max_chars=3000", "max_chars"},
		{explain, "limit=0", "limit"},
		{explain, "limit=501", "limit"},
		{explain, "cursor=a&cursor=b", "cursor"},
		{explain, "symbol=Store/Capture&limit=5", "limit"},
		{explain, "uid=Function:a.go:F%230&cursor=x", "cursor"},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.q)
		_, aerr := c.spec.ParseRequest(q)
		if aerr == nil || aerr.Argument != c.arg {
			t.Fatalf("%s: %v", c.q, aerr)
		}
	}
	q, _ := url.ParseQuery("max_chars=2000&limit=500&cursor=x")
	req, aerr := explain.ParseRequest(q)
	if aerr != nil || req.MaxChars != 2000 || req.Limit != 500 || req.Cursor != "x" || !req.Active() {
		t.Fatalf("%+v %v", req, aerr)
	}
	req, _ = search.ParseRequest(q)
	if req.Limit != 0 || req.Cursor != "" {
		t.Fatal("search's own limit and cursor are not the budget layer's")
	}
	if slices.Contains(search.Owns(), "cursor") || !slices.Contains(explain.Owns(), "cursor") {
		t.Fatal("owned arguments")
	}
}

// Every rung names a list its ladder declares (or none, for signals and omissions),
// rung names are unique per ladder, and the documentation lists every rung.
func TestLadderTablesAreConsistent(t *testing.T) {
	for _, tool := range Tools() {
		spec, _ := For(tool)
		doc := spec.Markdown()
		if spec.Ladders[len(spec.Ladders)-1].Match != "" {
			t.Errorf("%s: the last ladder must match any result", tool)
		}
		for _, l := range spec.Ladders {
			names := map[string]bool{}
			for _, r := range l.Rungs {
				if names[r.Name] {
					t.Errorf("%s %s: rung %s twice", tool, l.Shape, r.Name)
				}
				names[r.Name] = true
				switch r.Op {
				case opSignals, opFlags, opOmit:
					if r.List != "" {
						t.Errorf("%s %s: rung %s names a list", tool, l.Shape, r.Name)
					}
				default:
					if !slices.ContainsFunc(l.Lists, func(x List) bool { return x.Member == r.List }) {
						t.Errorf("%s %s: rung %s reduces undeclared list %q", tool, l.Shape, r.Name, r.List)
					}
				}
				if r.Doc == "" || !strings.Contains(doc, "`"+r.Name+"`") {
					t.Errorf("%s %s: rung %s is undocumented", tool, l.Shape, r.Name)
				}
			}
			paged := 0
			for _, x := range l.Lists {
				if x.Paged {
					paged++
				}
			}
			if paged > 1 || (paged == 1 && !spec.Paged && tool != "search") {
				t.Errorf("%s %s: %d paged lists", tool, l.Shape, paged)
			}
		}
		if spec.DefaultMaxChars < MinMaxChars || spec.DefaultMaxChars > MaxMaxChars {
			t.Errorf("%s: default %d out of bounds", tool, spec.DefaultMaxChars)
		}
	}
}

func BenchmarkApplySearch(b *testing.B) {
	spec, _ := For("search")
	raw, _ := json.Marshal(searchResult(25, 0))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := spec.Apply(raw, Request{MaxChars: 8000}, Page{Cursor: cursorAt}, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyImpactLarge(b *testing.B) {
	spec, _ := For("impact")
	impacted := make([]map[string]any, 2000)
	for i := range impacted {
		impacted[i] = map[string]any{"path": fmt.Sprintf("internal/pkg%02d/file_%04d.go", i%17, i), "distance": 1 + i/500}
	}
	raw, _ := json.Marshal(map[string]any{"symbol": "X", "impacted": impacted, "impacted_count": 2000})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := spec.Apply(raw, Request{MaxChars: 8000}, Page{}, nil); err != nil {
			b.Fatal(err)
		}
	}
}
