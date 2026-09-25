package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedSpec() *MemorySpec {
	return &MemorySpec{Seed: []SeedMemory{
		{Key: "cur", Label: LabelCurrent, Content: "port is 8081", Paths: []string{"a.go"}, Supersedes: "old"},
		{Key: "old", Label: LabelSuperseded, Content: "port is 8080", Paths: []string{"a.go"}},
		{Key: "dup", Label: LabelDuplicate, Content: "8081 again", Paths: []string{"a.go"}, DuplicateOf: "cur"},
		{Key: "con", Label: LabelContradiction, Content: "port is 9090", Paths: []string{"a.go"}, Contradicts: "cur"},
		{Key: "stl", Label: LabelStale, Content: "timeout 5s", Paths: []string{"b.go"}},
		{Key: "far", Label: LabelForeignScope, Content: "elsewhere"},
		{Key: "pnd", Label: LabelPending, Content: "maybe"},
	}}
}

// recallJSON renders a recall result the way the API does: entries with content,
// stale and verification_mode, plus path-overlap conflict groups.
func recallJSON(task string, spec *MemorySpec, keys []string, stale map[string]bool, conflicts [][]string, mode map[string]string) string {
	var entries []map[string]any
	for _, k := range keys {
		for _, s := range spec.Seed {
			if s.Key == k {
				m := firstNonEmpty(mode[k], "peer_verified")
				entries = append(entries, map[string]any{"id": "id-" + k, "content": seededContent(task, s), "stale": stale[k], "verification_mode": m})
			}
		}
	}
	var groups []map[string]any
	for _, g := range conflicts {
		var ids []string
		for _, k := range g {
			ids = append(ids, "id-"+k)
		}
		groups = append(groups, map[string]any{"path": "a.go", "entry_ids": ids})
	}
	b, _ := json.Marshal(map[string]any{"entries": entries, "conflicts": groups})
	return string(b) + "\n[xmustard evidence] {\"handle\":\"h\"}" // adapters append a trailer
}

func seedResults(spec *MemorySpec) []SeedResult {
	var out []SeedResult
	for _, s := range spec.Seed {
		out = append(out, SeedResult{Key: s.Key, Label: s.Label, EntryID: "id-" + s.Key})
	}
	return out
}

func TestMemoryMetricsScoreDeliveries(t *testing.T) {
	spec := seedSpec()
	zero := 0
	text := recallJSON("task", spec, []string{"cur", "old", "dup", "con", "stl"}, map[string]bool{"stl": true},
		[][]string{{"cur", "old", "dup", "con"}}, nil)
	mm := memoryMetrics("task", spec, []ToolResult{{Tool: "recall", Text: text, Bytes: len(text)}, {Tool: "ground", Text: "nothing", Bytes: 7}}, seedResults(spec), &zero)
	if *mm.CurrentFactRecall != 1 || *mm.SupersededServedRate != 1 || *mm.StaleServedRate != 0 || mm.StaleServedFlagged != 1 {
		t.Fatalf("recall/superseded/stale: %+v", mm)
	}
	if mm.DuplicatePairs != 1 || mm.DuplicateServedTogeth != 1 || *mm.DuplicateRate != 1 {
		t.Fatalf("duplicates: %+v", mm)
	}
	// 4 entries in one group = 6 flagged pairs, one of which is the labeled contradiction
	if mm.ContradictionPairs != 1 || mm.ContradictionServed != 1 || mm.ContradictionFlagged != 1 || mm.FlaggedPairs != 6 ||
		*mm.ContradictionRecall != 1 || *mm.ContradictionPrecision != 1.0/6 {
		t.Fatalf("contradictions: %+v", mm)
	}
	if mm.ScopeLeakage != 0 || mm.PendingServed != 0 || mm.RecallCalls != 1 || mm.RecallEstTokens != estTokens(len(text)) || !mm.HarmfulServed {
		t.Fatalf("governance/volume: %+v", mm)
	}

	// a leak of the foreign memory and an unverified memory shown as peer_verified
	leak := recallJSON("task", spec, []string{"far", "pnd"}, nil, nil, map[string]string{"pnd": "peer_verified"})
	mm = memoryMetrics("task", spec, []ToolResult{{Tool: "recall", Text: leak}}, seedResults(spec), nil)
	if mm.ScopeLeakage != 1 || mm.PendingServed != 1 || mm.PendingAsPeerVerified != 1 || mm.PromotionErrors != nil {
		t.Fatalf("leak: %+v", mm)
	}
	if *mm.CurrentFactRecall != 0 || mm.StaleServedRate == nil || *mm.StaleServedRate != 0 || mm.HarmfulServed {
		t.Fatalf("nothing current served: %+v", mm)
	}

	// markers in plain text still count as delivered (flag unknown = unflagged)
	plain := "memory: " + seededContent("task", spec.Seed[4])
	mm = memoryMetrics("task", spec, []ToolResult{{Tool: "recall", Text: plain}}, seedResults(spec), nil)
	if mm.Served[LabelStale] != 1 || *mm.StaleServedRate != 1 || !mm.HarmfulServed {
		t.Fatalf("plain text delivery: %+v", mm)
	}
	// markers are task-scoped: another task's marker is not this task's memory
	other := recallJSON("other-task", spec, []string{"cur"}, nil, nil, nil)
	mm = memoryMetrics("task", spec, []ToolResult{{Tool: "recall", Text: other}}, seedResults(spec), nil)
	if mm.Served[LabelCurrent] != 0 {
		t.Fatal("marker from another task counted")
	}
	if memoryMetrics("task", nil, nil, nil, nil) != nil || memoryMetrics("task", &MemorySpec{}, nil, nil, nil) != nil {
		t.Fatal("no fixture must yield no metrics")
	}
}

func TestApplyDrift(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "a\n")
	mustWrite(t, filepath.Join(dir, "b.txt"), "b\n")
	mustWrite(t, filepath.Join(dir, "c.txt"), "c\n")
	app, rep := "more\n", "new\n"
	err := applyDrift(&MemorySpec{Drift: []DriftEdit{{Path: "a.txt", Append: &app}, {Path: "b.txt", Replace: &rep}, {Path: "c.txt", Delete: true}}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if string(a) != "a\nmore\n" || string(b) != "new\n" {
		t.Fatalf("drift %q %q", a, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("delete drift")
	}
	if err := applyDrift(&MemorySpec{Drift: []DriftEdit{{Path: "missing.txt", Append: &app}}}, dir); err == nil {
		t.Fatal("appending to a missing file must fail")
	}
}

func TestCorpusValidationErrors(t *testing.T) {
	base := func(dir string) {
		mustWrite(t, filepath.Join(dir, "fx/a.txt"), "a\n")
		mustWrite(t, filepath.Join(dir, "fx/hidden.sh"), "true\n")
		mustWrite(t, filepath.Join(dir, "o.sh"), "true\n")
	}
	cases := []struct {
		name, yaml, want string
	}{
		{"schema", "schema: v0\nname: x\ntasks: []\n", "schema must be"},
		{"unknown field", "schema: xmustard.eval/v1\nname: x\nbogus: 1\ntasks: []\n", "field bogus not found"},
		{"no oracle", task("id: a", "oracle: {}"), "oracle.cmd is required"},
		{"duplicate id", task("id: a", "oracle: {cmd: [true]}") + taskItem("id: a", "oracle: {cmd: [true]}"), "duplicate id"},
		{"oracle inside fixture", task("id: a", "oracle: {cmd: [true], files: [{src: fx/hidden.sh, dest: h.sh}]}"), "inside the fixture"},
		{"dest escapes", task("id: a", "oracle: {cmd: [true], files: [{src: o.sh, dest: ../h.sh}]}"), "clean relative path"},
		{"path repo needs ref", "schema: xmustard.eval/v1\nname: x\ntasks:\n  - {id: a, class: bugfix, repo: {path: fx}, prompt: p, oracle: {cmd: [true]}}\n", "repo.ref is required"},
		{"bad class", "schema: xmustard.eval/v1\nname: x\ntasks:\n  - {id: a, class: vibes, repo: {fixture: fx}, prompt: p, oracle: {cmd: [true]}}\n", "class must be one of"},
		{"stale without drift", task("id: a", "oracle: {cmd: [true]}", "memory: {seed: [{key: s, label: stale, content: c, paths: [a.txt]}]}"), "needs paths changed by a drift edit"},
		{"superseded unreferenced", task("id: a", "oracle: {cmd: [true]}", "memory: {seed: [{key: s, label: superseded, content: c}]}"), "must be named by another memory's supersedes"},
		{"supersedes wrong label", task("id: a", "oracle: {cmd: [true]}", "memory: {seed: [{key: s, label: current, content: c, supersedes: t}, {key: t, label: current, content: c}]}"), "must be labeled superseded"},
		{"drift needs one op", task("id: a", "oracle: {cmd: [true]}", "memory: {seed: [{key: s, label: current, content: c}], drift: [{path: a.txt}]}"), "exactly one of append"},
		{"bad arm", task("id: a", "oracle: {cmd: [true]}", "arms: [magic]"), "unknown arm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			base(dir)
			mustWrite(t, filepath.Join(dir, "c.yaml"), tc.yaml)
			_, err := LoadCorpus(filepath.Join(dir, "c.yaml"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func task(fields ...string) string {
	return "schema: xmustard.eval/v1\nname: x\ntasks:\n" + taskItem(fields...)
}

func taskItem(fields ...string) string {
	s := "  - class: bugfix\n    repo: {fixture: fx}\n    prompt: p\n"
	for _, f := range fields {
		s += "    " + f + "\n"
	}
	return s
}
