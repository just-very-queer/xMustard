package groundbudget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// groundResult builds a ground result shaped like the API's: failed runs, broken
// contracts, drift reasons and unknown fields scale with the arguments.
func groundResult(runs, breaks, reasons, unknown int) map[string]any {
	failed := make([]string, runs)
	for i := range failed {
		failed[i] = fmt.Sprintf("run_%04d_newest_first", i)
	}
	broken := make([]string, breaks)
	for i := range broken {
		broken[i] = fmt.Sprintf("pkg.Func%04d in internal/pkg/file_%04d.go (fn(a int) -> fn(a, b int))", i, i)
	}
	why := make([]string, reasons)
	for i := range why {
		why[i] = fmt.Sprintf("tracked file %04d changed since the baseline", i)
	}
	g := map[string]any{
		"workspace_id": "ws1",
		"drift": map[string]any{
			"workspace_id": "ws1", "has_baseline": true, "stale": true, "head_changed": false, "content_changed": true,
			"baseline_head": "0123456789abcdef0123456789abcdef01234567", "current_head": "fedcba9876543210fedcba9876543210fedcba98",
			"reasons": why, "generated_at": "2026-09-25T00:00:00Z",
		},
		"changed_files": 12, "dirty_symbols": 30, "contract_breaks": breaks, "broken_contracts": broken,
		"recent_failed_runs": failed, "blocked_by_dirty_state": true, "blocked_by_failing_verification": runs > 0,
		"stale_memory": 3, "stale_memory_checked": 64, "stale_memory_total": 90, "stale_memory_complete": false,
		"memory_verification_modes": map[string]any{"peer_verified": 4, "self_asserted_open_mode": 0, "single_agent": 86},
		"summary":                   "12 changed file(s), 30 dirty symbol(s), N contract break(s), N failed run(s), 3 stale memory.",
		"generated_at":              "2026-09-25T00:00:00Z",
		"principal":                 map[string]any{"id": "agent-a", "role": "agent", "roles": []string{"proposer", "reader", "verifier"}, "open_mode": false},
	}
	if unknown > 0 {
		u := make([]map[string]string, unknown)
		for i := range u {
			u[i] = map[string]string{"field": fmt.Sprintf("field_%d", i), "reason": "run history unreadable: permission denied"}
		}
		g["unknown"] = u
	}
	return g
}

func apply(t *testing.T, g map[string]any, req Request) (map[string]any, report, []byte) {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Apply(raw, req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	var rep report
	b, _ := json.Marshal(got[ReportMember])
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.UsedChars != len(out) {
		t.Fatalf("used_chars %d, output is %d bytes", rep.UsedChars, len(out))
	}
	if rep.OverBudget != (len(out) > rep.MaxChars) {
		t.Fatalf("over_budget=%v for %d bytes against %d", rep.OverBudget, len(out), rep.MaxChars)
	}
	return got, rep, out
}

// A result inside every cap and the budget is returned unchanged, plus a short report.
func TestSmallResultIsReturnedInFull(t *testing.T) {
	g := groundResult(2, 1, 1, 0)
	got, rep, out := apply(t, g, Request{})
	if rep.MaxChars != DefaultMaxChars || rep.DegradedStage != "full" || rep.Sections != nil || rep.Signals != nil || rep.Docs != "" {
		t.Fatalf("unexpected report for a small result: %+v", rep)
	}
	delete(got, ReportMember)
	want := map[string]any{}
	b, _ := json.Marshal(g)
	_ = json.Unmarshal(b, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("small result changed:\n got %v\nwant %v", got, want)
	}
	// members keep their order; the report comes last
	if i := strings.Index(string(out), `"`+ReportMember+`"`); i < strings.Index(string(out), `"summary"`) {
		t.Fatal("output_budget must follow the result members")
	}
}

// One long section is held to its own cap: the newest items are kept, the rest are
// counted, and the other sections are untouched.
func TestSectionCapTrimsNewestFirst(t *testing.T) {
	g := groundResult(300, 0, 1, 0)
	got, rep, _ := apply(t, g, Request{})
	runs := got["recent_failed_runs"].([]any)
	if len(runs) == 0 || len(runs) >= 300 {
		t.Fatalf("runs not trimmed to the cap: %d kept", len(runs))
	}
	for i, r := range runs {
		if r != fmt.Sprintf("run_%04d_newest_first", i) {
			t.Fatalf("trim must keep the head (newest) of the list; item %d is %v", i, r)
		}
	}
	sr := rep.Sections["runs"]
	if sr.State != "trimmed" || sr.Reason != reasonSectionCap || sr.Cap != 1536 || sr.Total["recent_failed_runs"] != 300 ||
		sr.Kept["recent_failed_runs"] != len(runs) || !strings.Contains(sr.Recover, "sections=runs") {
		t.Fatalf("runs report: %+v", sr)
	}
	b, _ := json.Marshal(got["recent_failed_runs"])
	if len(b)+len(`"recent_failed_runs":,`) > 1536 {
		t.Fatalf("runs section is %d chars, over its 1536 cap", len(b))
	}
	if rep.DegradedStage != "trimmed" || rep.Docs != DocsURI {
		t.Fatalf("report: %+v", rep)
	}
	for _, name := range []string{"index", "memory", "drift", "principal", "summary"} {
		if _, ok := rep.Sections[name]; ok {
			t.Fatalf("%s was reduced although it fits its cap: %+v", name, rep.Sections[name])
		}
	}
}

// A section requested alone (besides summary) is bounded only by max_chars, so a
// caller can get it in full.
func TestSectionAloneLiftsItsCap(t *testing.T) {
	g := groundResult(300, 0, 1, 0)
	got, rep, out := apply(t, g, Request{Sections: map[string]bool{"runs": true}, MaxChars: MaxMaxChars})
	if n := len(got["recent_failed_runs"].([]any)); n != 300 {
		t.Fatalf("runs alone must be returned in full, got %d of 300 (%d bytes)", n, len(out))
	}
	if rep.Sections != nil {
		t.Fatalf("nothing requested was reduced: %+v", rep.Sections)
	}
	if want := []string{"index", "memory", "drift", "principal"}; !reflect.DeepEqual(rep.NotRequested, want) {
		t.Fatalf("not_requested = %v, want %v", rep.NotRequested, want)
	}
	for _, k := range []string{"changed_files", "broken_contracts", "drift", "stale_memory", "principal"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s returned although its section was not requested", k)
		}
	}
	// the pinned summary is always returned
	for _, k := range []string{"workspace_id", "summary", "blocked_by_dirty_state", "blocked_by_failing_verification", "generated_at"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("pinned %s missing", k)
		}
	}
	// the same section with a small budget is trimmed for max_chars, not for its cap
	_, rep, out = apply(t, g, Request{Sections: map[string]bool{"runs": true}, MaxChars: 3000})
	if len(out) > 3000 || rep.Sections["runs"].Reason != reasonMaxChars || rep.Sections["runs"].Cap != 0 {
		t.Fatalf("runs alone under max_chars=3000: %d bytes, %+v", len(out), rep.Sections["runs"])
	}
}

// Sections not requested still report their failure, stale and trust signals, so a
// narrow request never hides a failure.
func TestUnrequestedSectionsKeepTheirSignals(t *testing.T) {
	g := groundResult(5, 2, 3, 0)
	got, rep, _ := apply(t, g, Request{Sections: map[string]bool{"principal": true}})
	want := map[string]any{
		"recent_failed_runs": 5.0, "changed_files": 12.0, "dirty_symbols": 30.0, "contract_breaks": 2.0, "broken_contracts": 2.0,
		"stale_memory": 3.0, "stale_memory_complete": false,
		"memory_verification_modes.peer_verified": 4.0, "memory_verification_modes.self_asserted_open_mode": 0.0,
		"memory_verification_modes.single_agent": 86.0,
		"drift.stale":                            true, "drift.has_baseline": true, "drift.head_changed": false, "drift.content_changed": true,
		"drift.reasons": 3.0,
	}
	if !reflect.DeepEqual(rep.Signals, want) {
		t.Fatalf("signals of unrequested sections:\n got %v\nwant %v", rep.Signals, want)
	}
	if rep.Sections != nil || rep.Docs != "" || rep.DegradedStage != "full" {
		t.Fatalf("a narrow request is not a degradation: %+v", rep)
	}
	if got["principal"] == nil || got["blocked_by_failing_verification"] != true {
		t.Fatalf("requested and pinned members must be returned: %v", got)
	}
}

// Under max_chars the least important section is reduced first, down to omitted,
// before a more important one is touched; summary is never omitted.
func TestLadderReducesLeastImportantFirst(t *testing.T) {
	g := groundResult(100, 40, 20, 0)
	_, full, _ := apply(t, g, Request{MaxChars: MaxMaxChars})
	prev := -1
	for _, max := range []int{MaxMaxChars, 8000, 6000, 5000, 4000, 3000, 2500, 2000} {
		got, rep, out := apply(t, g, Request{MaxChars: max})
		if len(out) > max {
			t.Fatalf("max_chars=%d: %d bytes (report %+v)", max, len(out), rep)
		}
		// rank order: once a section is reduced for max_chars, every less important
		// section is already omitted
		order := []string{"summary", "runs", "index", "memory", "drift", "principal"}
		for i, name := range order {
			sr, ok := rep.Sections[name]
			if !ok || sr.Reason != reasonMaxChars {
				continue
			}
			for _, less := range order[i+1:] {
				if lr, ok := rep.Sections[less]; !ok || lr.State != "omitted" {
					t.Fatalf("max_chars=%d: %s reduced (%s) while less important %s is %+v", max, name, sr.State, less, lr)
				}
			}
		}
		if got["summary"] == nil || got["workspace_id"] == nil {
			t.Fatalf("max_chars=%d: pinned summary dropped", max)
		}
		assertNoSilentSignalLoss(t, g, got, rep)
		reduced := len(rep.Sections)
		if reduced < prev {
			t.Fatalf("max_chars=%d reduced fewer sections (%d) than a larger budget (%d)", max, reduced, prev)
		}
		prev = reduced
	}
	if full.Sections["runs"].Reason != reasonSectionCap {
		t.Fatalf("even at the largest budget the runs cap applies: %+v", full.Sections)
	}
}

// assertNoSilentSignalLoss checks that every signal member of the input is either
// returned, counted in its section's report, or listed in output_budget.signals.
func assertNoSilentSignalLoss(t *testing.T, in, got map[string]any, rep report) {
	t.Helper()
	for _, sec := range sections {
		for _, m := range sec.Members {
			if !m.Signal {
				continue
			}
			if _, ok := in[m.Name]; !ok {
				continue
			}
			if _, ok := got[m.Name]; ok {
				continue
			}
			if _, ok := rep.Signals[m.Name]; ok {
				continue
			}
			found := false
			for k := range rep.Signals {
				if strings.HasPrefix(k, m.Name+".") {
					found = true
				}
			}
			for _, sr := range rep.Sections {
				if _, ok := sr.Total[m.Name]; ok {
					found = true
				}
			}
			if !found {
				t.Fatalf("signal %s (section %s) dropped silently; report %+v", m.Name, sec.Name, rep)
			}
		}
	}
}

// Nested lists trim with their object; at counts an object keeps only its flags.
func TestObjectsTrimNestedListsThenKeepFlags(t *testing.T) {
	g := groundResult(0, 0, 200, 0)
	got, rep, _ := apply(t, g, Request{})
	drift := got["drift"].(map[string]any)
	reasons := drift["reasons"].([]any)
	if len(reasons) == 0 || len(reasons) >= 200 || rep.Sections["drift"].Total["drift.reasons"] != 200 {
		t.Fatalf("drift.reasons not trimmed to the drift cap: %d kept, report %+v", len(reasons), rep.Sections["drift"])
	}
	// a budget that forces drift to counts keeps its four flags
	g = groundResult(100, 40, 200, 0)
	for max := 6000; max >= MinMaxChars; max -= 10 {
		got, rep, _ = apply(t, g, Request{MaxChars: max})
		if rep.Sections["drift"].State == "counts" {
			d := got["drift"].(map[string]any)
			keys := []string{}
			for k := range d {
				keys = append(keys, k)
			}
			if len(d) != 4 || d["stale"] != true || d["has_baseline"] != true || d["head_changed"] != false || d["content_changed"] != true {
				t.Fatalf("drift at counts must keep exactly its flags, got %v", keys)
			}
			if rep.Sections["drift"].Total["drift.reasons"] != 200 {
				t.Fatalf("drift.reasons must be counted: %+v", rep.Sections["drift"])
			}
			return
		}
	}
	t.Fatal("no budget reduced drift to counts")
}

// The pinned summary section is reduced to counts but never omitted; when even that
// does not fit, the result says so instead of cutting it.
func TestPinnedSummaryIsNeverOmitted(t *testing.T) {
	g := groundResult(0, 0, 0, 80)
	got, rep, _ := apply(t, g, Request{MaxChars: MinMaxChars})
	if got["summary"] == nil || got["blocked_by_dirty_state"] != true {
		t.Fatalf("pinned members dropped: %v", got)
	}
	if u, ok := got["unknown"]; ok {
		if n := len(u.([]any)); n >= 80 {
			t.Fatalf("unknown not reduced under a 2000-char budget: %d", n)
		}
	} else if rep.Sections["summary"].Total["unknown"] != 80 {
		t.Fatalf("unknown removed without its count: %+v", rep.Sections["summary"])
	}
	g["summary"] = strings.Repeat("x", 3000)
	got, rep, out := apply(t, g, Request{MaxChars: MinMaxChars})
	if !rep.OverBudget || len(out) <= MinMaxChars || got["summary"] != g["summary"] {
		t.Fatalf("an unfittable summary must be returned whole and flagged over_budget: %d bytes, %+v", len(out), rep)
	}
}

// Members no section declares are returned with a full request and reduced first
// under pressure. They are treated as signals and reported by name, also when a
// narrow request leaves them out (they cannot be requested by name), so a failure
// member a later change adds is never dropped without a trace.
func TestUndeclaredMembersAreReportedByName(t *testing.T) {
	g := groundResult(1, 1, 1, 0)
	g["future_section"] = map[string]any{"items": []string{strings.Repeat("y", 200), strings.Repeat("z", 200)}}
	got, rep, _ := apply(t, g, Request{})
	if got["future_section"] == nil || rep.Sections != nil {
		t.Fatalf("an undeclared member within budget is returned as is: %+v", rep)
	}
	g = groundResult(100, 40, 20, 0)
	g["future_section"] = map[string]any{"items": []string{strings.Repeat("y", 200), strings.Repeat("z", 200)}}
	got, rep, _ = apply(t, g, Request{MaxChars: 2000})
	if _, ok := got["future_section"]; ok {
		t.Fatalf("undeclared members go first under pressure")
	}
	if sr := rep.Sections[otherSection]; sr.State != "omitted" || !reflect.DeepEqual(sr.Members, []string{"future_section"}) {
		t.Fatalf("undeclared member must be reported by name: %+v", sr)
	}
	if rep.Signals["future_section.items"] != 2.0 {
		t.Fatalf("an omitted undeclared member keeps its counts as signals: %v", rep.Signals)
	}
	// a narrow request: the undeclared members are named, and their lists counted
	g = groundResult(1, 1, 1, 0)
	g["verification_failures"] = []string{"v1", "v2"}
	g["future_flag"] = true
	got, rep, _ = apply(t, g, Request{Sections: map[string]bool{"runs": true}})
	if _, ok := got["verification_failures"]; ok {
		t.Fatal("undeclared members are not part of a narrow request")
	}
	sr, ok := rep.Sections[otherSection]
	if !ok || sr.State != "omitted" || sr.Reason != reasonNotRequested || !reflect.DeepEqual(sr.Members, []string{"future_flag", "verification_failures"}) ||
		sr.Recover != otherRecover {
		t.Fatalf("a narrow request must name the undeclared members it leaves out: %+v", rep)
	}
	if rep.Signals["verification_failures"] != 2.0 || rep.Signals["future_flag"] != true {
		t.Fatalf("undeclared members keep their signals: %v", rep.Signals)
	}
	if contains(rep.NotRequested, otherSection) || rep.DegradedStage != "full" || rep.Docs != DocsURI {
		t.Fatalf("other is reported as left out, not as not requested, and no requested section was degraded: %+v", rep)
	}
}

// Every flag of a signal object survives the counts and omitted stages, including
// flags its Keep list does not name, and its nested lists stay counted. The drift
// object is shaped like Rust's DriftReport (rust-core/src/changetrack.rs), plus a
// flag a later change might add.
func TestSignalObjectsKeepEveryFlag(t *testing.T) {
	g := groundResult(100, 40, 20, 0)
	g["drift"] = map[string]any{
		"workspace_id": "ws1", "has_baseline": true, "stale": true, "head_changed": false, "content_changed": false,
		"sibling_clone": true, "baseline_head": "0123456789abcdef0123456789abcdef01234567", "current_head": "0123456789abcdef0123456789abcdef01234567",
		"baseline_remote": "git@example.com:a/b.git", "current_remote": "git@example.com:c/d.git",
		"reasons": []string{"index baseline was built from a sibling clone"}, "generated_at": "2026-09-25T00:00:00Z",
		"future_flag": true,
	}
	flags := []string{"has_baseline", "stale", "head_changed", "content_changed", "sibling_clone", "future_flag"}
	drift := g["drift"].(map[string]any)
	sawCounts, sawOmitted := false, false
	for max := MaxMaxChars; max >= MinMaxChars; max -= 50 {
		got, rep, _ := apply(t, g, Request{MaxChars: max})
		switch rep.Sections["drift"].State {
		case "counts":
			sawCounts = true
			d := got["drift"].(map[string]any)
			for _, f := range flags {
				if d[f] != drift[f] {
					t.Fatalf("max_chars=%d: drift at counts lost flag %s: %v", max, f, d)
				}
			}
			if rep.Sections["drift"].Total["drift.reasons"] != 1 {
				t.Fatalf("drift.reasons must be counted: %+v", rep.Sections["drift"])
			}
		case "omitted":
			sawOmitted = true
			for _, f := range flags {
				if rep.Signals["drift."+f] != drift[f] {
					t.Fatalf("max_chars=%d: omitted drift lost flag %s from signals: %v", max, f, rep.Signals)
				}
			}
			if rep.Signals["drift.reasons"] != 1.0 {
				t.Fatalf("omitted drift must keep its reasons count: %v", rep.Signals)
			}
		}
	}
	if !sawCounts || !sawOmitted {
		t.Fatalf("the budgets tried did not reach both stages (counts %v, omitted %v)", sawCounts, sawOmitted)
	}
	// not requested: the same signals
	_, rep, _ := apply(t, g, Request{Sections: map[string]bool{"runs": true}})
	for _, f := range flags {
		if rep.Signals["drift."+f] != drift[f] {
			t.Fatalf("unrequested drift lost flag %s: %v", f, rep.Signals)
		}
	}
}

// open_mode is a trust signal: memory an open-mode caller writes is never
// peer-verified. It survives when principal is not requested or omitted.
func TestPrincipalOpenModeIsASignal(t *testing.T) {
	g := groundResult(100, 40, 20, 0)
	g["principal"] = map[string]any{"id": "local", "role": "open-mode", "roles": []string{"admin", "agent"}, "open_mode": true}
	for _, req := range []Request{{Sections: map[string]bool{"runs": true}, MaxChars: MinMaxChars}, {MaxChars: MinMaxChars}} {
		got, rep, _ := apply(t, g, req)
		if _, ok := got["principal"]; ok {
			continue
		}
		if rep.Signals["principal.open_mode"] != true || rep.Signals["principal.role"] != "open-mode" || rep.Signals["principal.roles"] != 2.0 {
			t.Fatalf("%+v: principal left out without its open_mode signal: %v", req, rep.Signals)
		}
	}
}

// The pinned summary is held to its cap in a full request, and sections=summary alone
// lifts it, so its recover hint works.
func TestSummaryAloneLiftsItsCap(t *testing.T) {
	g := groundResult(0, 0, 0, 0)
	u := make([]map[string]string, 8)
	for i := range u {
		u[i] = map[string]string{"field": fmt.Sprintf("field_%d", i), "reason": strings.Repeat("r", 200)}
	}
	g["unknown"] = u
	for _, max := range []int{DefaultMaxChars, MaxMaxChars} {
		got, rep, _ := apply(t, g, Request{MaxChars: max})
		if n := len(got["unknown"].([]any)); n == 8 || rep.Sections["summary"].Reason != reasonSectionCap {
			t.Fatalf("max_chars=%d: the summary cap applies in a full request: %d of 8 kept, %+v", max, n, rep.Sections["summary"])
		}
		if !strings.Contains(rep.Sections["summary"].Recover, "sections=summary") {
			t.Fatalf("recover hint: %q", rep.Sections["summary"].Recover)
		}
	}
	got, rep, _ := apply(t, g, Request{Sections: map[string]bool{"summary": true}, MaxChars: MaxMaxChars})
	if n := len(got["unknown"].([]any)); n != 8 || rep.Sections != nil {
		t.Fatalf("sections=summary alone must return the summary in full: %d of 8, %+v", n, rep.Sections)
	}
	// with another section requested the summary keeps its cap
	got, _, _ = apply(t, g, Request{Sections: map[string]bool{"summary": true, "runs": true}, MaxChars: MaxMaxChars})
	if n := len(got["unknown"].([]any)); n == 8 {
		t.Fatal("the summary cap is lifted only when the summary is the one section requested")
	}
}

// A caller that can retain the unbudgeted result is asked to exactly when anything it
// requested was reduced, and the report names what it returned, within max_chars.
func TestRecoveryIsNamedOnlyWhenReduced(t *testing.T) {
	calls := 0
	retain := func() Recovery {
		calls++
		return Recovery{Handle: "xm1." + strings.Repeat("A", 43), URI: "xmustard://evidence/xm1." + strings.Repeat("A", 43) + "?workspace_id=repo-0123456789"}
	}
	small := mustJSON(groundResult(2, 1, 1, 0))
	out, err := ApplyRecoverable(small, Request{}, retain)
	if err != nil || calls != 0 || strings.Contains(string(out), `"recover_`) {
		t.Fatalf("a result returned in full needs no recovery: calls=%d %s", calls, out)
	}
	// a narrow request that only leaves out unrequested sections is not a reduction
	if out, _ = ApplyRecoverable(small, Request{Sections: map[string]bool{"runs": true}}, retain); calls != 0 {
		t.Fatalf("not requesting a section is not a reduction: %s", out)
	}
	rng := rand.New(rand.NewSource(5454))
	for i := 0; i < 200; i++ {
		calls = 0
		raw := mustJSON(groundResult(rng.Intn(400), rng.Intn(120), rng.Intn(80), rng.Intn(20)))
		req := Request{MaxChars: MinMaxChars + rng.Intn(MaxMaxChars-MinMaxChars+1)}
		out, err := ApplyRecoverable(raw, req, retain)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("case %d: not JSON: %v", i, err)
		}
		rep := got[ReportMember].(map[string]any)
		_, reduced := rep["sections"]
		if reduced != (calls == 1) || calls > 1 {
			t.Fatalf("case %d: retain called %d times for reduced=%v", i, calls, reduced)
		}
		if reduced && (rep["recover_uri"] == nil || rep["recover_handle"] == nil) {
			t.Fatalf("case %d: a reduced result must name its recovery: %v", i, rep)
		}
		if len(out) > req.MaxChars || rep["used_chars"] != float64(len(out)) {
			t.Fatalf("case %d: %d bytes against max_chars %d (report %v)", i, len(out), req.MaxChars, rep)
		}
	}
	// no handle: the reason is reported instead
	out, _ = ApplyRecoverable(mustJSON(groundResult(300, 0, 1, 0)), Request{}, func() Recovery { return Recovery{Unavailable: "evidence quota_full"} })
	if !strings.Contains(string(out), `"recover_unavailable":"evidence quota_full"`) || strings.Contains(string(out), `"recover_uri":`) {
		t.Fatalf("unavailable recovery: %s", out)
	}
}

// Budgeting does not copy the result: Apply allocates its element tables and the
// output, and WorkingSet (what the API admits) covers that plus the result itself.
func TestApplyDoesNotCopyTheResult(t *testing.T) {
	raw := mustJSON(groundResult(2000, 20000, 50, 5)) // about 2 MB, most of it list elements
	req := Request{MaxChars: DefaultMaxChars}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := Apply(raw, req); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	alloc := int64(after.TotalAlloc - before.TotalAlloc)
	if alloc > int64(len(raw))/4 {
		t.Fatalf("Apply allocated %d bytes for a %d-byte result; it must not copy the result", alloc, len(raw))
	}
	if ws := WorkingSet(raw, req); ws < int64(len(raw))+alloc {
		t.Fatalf("WorkingSet %d does not cover the result (%d) plus Apply's allocations (%d)", ws, len(raw), alloc)
	}
	t.Logf("result %d bytes: Apply allocated %d bytes; WorkingSet %d", len(raw), alloc, WorkingSet(raw, req))
}

// Result bytes are copied through exactly, whitespace and escapes included.
func TestApplyKeepsTheResultBytes(t *testing.T) {
	in := []byte(` { "workspace_id" : "w\u0073" , "summary":"a \"quoted\" line",` +
		` "recent_failed_runs" : [ "r1" , "r\\2", "r]3" ] , "drift": {"stale": true, "reasons": [ "x" ] } } `)
	out, err := Apply(in, Request{Sections: map[string]bool{"runs": true}, MaxChars: MaxMaxChars})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got["workspace_id"] != "ws" || !reflect.DeepEqual(got["recent_failed_runs"], []any{"r1", "r\\2", "r]3"}) || got["drift"] != nil {
		t.Fatalf("members changed: %s", out)
	}
	if !bytes.Contains(out, []byte(`[ "r1" , "r\\2", "r]3" ]`)) {
		t.Fatalf("a list returned in full is its original bytes: %s", out)
	}
	for _, bad := range []string{`[1,2]`, `{"a":`, `nope`} {
		if _, err := Apply([]byte(bad), Request{}); err == nil {
			t.Fatalf("%s must be refused", bad)
		}
	}
}

// An earlier report member in the input is replaced, never budgeted as content.
func TestStaleReportIsReplaced(t *testing.T) {
	g := groundResult(1, 1, 1, 0)
	g[ReportMember] = map[string]any{"max_chars": 1}
	_, rep, out := apply(t, g, Request{})
	if rep.MaxChars != DefaultMaxChars || strings.Count(string(out), `"`+ReportMember+`"`) != 1 {
		t.Fatalf("stale report kept: %s", out)
	}
}

// Randomized: the output is JSON, never over budget unless flagged, reports its own
// size, and never loses a signal silently.
func TestBudgetInvariantsRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(54))
	names := SectionNames()
	for i := 0; i < 300; i++ {
		g := groundResult(rng.Intn(400), rng.Intn(120), rng.Intn(80), rng.Intn(20))
		req := Request{MaxChars: MinMaxChars + rng.Intn(MaxMaxChars-MinMaxChars+1)}
		if rng.Intn(2) == 0 {
			req.Sections = map[string]bool{}
			for _, n := range names {
				if rng.Intn(2) == 0 {
					req.Sections[n] = true
				}
			}
		}
		got, rep, out := apply(t, g, req)
		if len(out) > req.MaxChars && !rep.OverBudget {
			t.Fatalf("case %d: %d bytes over %d without over_budget", i, len(out), req.MaxChars)
		}
		if rep.OverBudget {
			t.Fatalf("case %d: over budget at %d for an ordinary result: %+v", i, req.MaxChars, rep)
		}
		assertNoSilentSignalLoss(t, g, got, rep)
		if again, _ := Apply(mustJSON(g), req); string(again) != string(out) {
			t.Fatalf("case %d: budgeting is not deterministic", i)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestParseRequestRejectsNeverClamps(t *testing.T) {
	if r, err := ParseRequest("", ""); err != nil || r.Sections != nil || r.MaxChars != DefaultMaxChars {
		t.Fatalf("defaults: %+v %v", r, err)
	}
	r, err := ParseRequest(" runs, index ,runs,", "2000")
	if err != nil || !reflect.DeepEqual(r.Sections, map[string]bool{"runs": true, "index": true}) || r.MaxChars != 2000 {
		t.Fatalf("parse: %+v %v", r, err)
	}
	for _, bad := range []struct{ sections, max, arg string }{
		{"runs,bogus", "", "sections"},
		{"", "1999", "max_chars"},
		{"", "65537", "max_chars"},
		{"", "6k", "max_chars"},
		{"", "-1", "max_chars"},
	} {
		_, err := ParseRequest(bad.sections, bad.max)
		if err == nil || err.Argument != bad.arg {
			t.Fatalf("ParseRequest(%q, %q) = %v, want an error on %s", bad.sections, bad.max, err, bad.arg)
		}
	}
	if _, err := ParseRequest("", "1999"); !strings.Contains(err.Error(), "not clamped") || err.Extra["minimum"] != MinMaxChars {
		t.Fatalf("out-of-range error must name the bounds: %v %v", err, err.Extra)
	}
}

// Every section name is unique, every member belongs to one section, and every
// section can say how to get it back.
func TestSectionTableIsConsistent(t *testing.T) {
	seen := map[string]string{}
	for i, s := range sections {
		if s.Pinned != (i == 0) {
			t.Fatalf("only the first (most important) section is pinned: %s", s.Name)
		}
		if s.Cap <= 0 || s.Recover == "" || s.Doc == "" {
			t.Fatalf("section %s needs a cap, a recover hint and a doc", s.Name)
		}
		for _, m := range s.Members {
			if prev, dup := seen[m.Name]; dup {
				t.Fatalf("member %s declared by %s and %s", m.Name, prev, s.Name)
			}
			seen[m.Name] = s.Name
		}
	}
	if SectionOf("recent_failed_runs") != "runs" || SectionOf("nope") != "" {
		t.Fatal("SectionOf")
	}
}

// BenchmarkApply budgets a ground result the size of a live one on a busy
// repository (about 20 KB: 200 contract breaks, 120 failed runs) to the default.
func BenchmarkApply(b *testing.B) {
	raw := mustJSON(groundResult(120, 200, 10, 2))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Apply(raw, Request{MaxChars: DefaultMaxChars}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkApplyLarge budgets a 1.6 MB result (20,000 contract breaks, 2,000 failed
// runs) to the default.
func BenchmarkApplyLarge(b *testing.B) {
	raw := mustJSON(groundResult(2000, 20000, 50, 5))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Apply(raw, Request{MaxChars: DefaultMaxChars}); err != nil {
			b.Fatal(err)
		}
	}
}
