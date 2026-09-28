package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resultFixtures are the nine tools' recorded results (WS-04 goldens): the defect
// PAR-CTX-15 names is generated_at in their text.
func resultFixtures(t *testing.T) map[string]struct {
	tool string
	body []byte
} {
	t.Helper()
	paths, err := filepath.Glob("../mcpserver/testdata/results/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("result fixtures: %v (%d)", err, len(paths))
	}
	out := map[string]struct {
		tool string
		body []byte
	}{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Tool   string          `json:"tool"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[filepath.Base(p)] = struct {
			tool string
			body []byte
		}{f.Tool, f.Result}
	}
	return out
}

func TestRenderRemovesVolatileMembersFromEveryToolFixture(t *testing.T) {
	for name, f := range resultFixtures(t) {
		r := Render(f.tool, f.body)
		if !r.JSON {
			t.Fatalf("%s: not rendered as JSON", name)
		}
		for _, v := range []string{`"generated_at"`, `"elapsed_ms"`, `"lock_wait_ms"`, `"identity_age_ms"`, `"lock_contended"`} {
			if strings.Contains(string(r.Text), v) {
				t.Errorf("%s: stable text still carries %s", name, v)
			}
		}
		// rendering the rendering changes nothing: the text is already stable
		if again := Render(f.tool, r.Text); string(again.Text) != string(r.Text) || again.Meta != nil {
			t.Errorf("%s: rendering is not idempotent (meta %v)", name, again.Meta)
		}
	}
}

// mutate decodes body, applies set (dotted path -> value) and re-encodes it with its
// members in another order than canonical (json.Marshal of the map sorts, so shuffle by
// wrapping in an indenting encoder).
func mutate(t *testing.T, body string, set map[string]any) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	for path, val := range set {
		parts := strings.Split(path, ".")
		m := v
		for _, p := range parts[:len(parts)-1] {
			m = m[p].(map[string]any)
		}
		m[parts[len(parts)-1]] = val
	}
	b, err := json.MarshalIndent(v, "", "   ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const searchBody = `{"workspace_id":"ws","query":"Double","total":1,"generated_at":"2026-09-25T08:23:32Z",
 "hits":[{"path":"pkg/calc/calc.go","line":7,"name":"Double","score":1.1417331496161323}],
 "coverage":{"complete":true,"indexed_files":2,"source_identity":{"key":"abc","identity_complete":true},
  "work":{"elapsed_ms":435,"lock_wait_ms":0,"graph_cache":"miss","files_parsed":2,"lock":"not_used"}}}`

const impactBody = `{"path":"a.go","impacted_count":1,"impacted":[{"distance":1,"path":"b.go"}],"generated_at":"x",
 "freshness":{"status":"current","identity_age_ms":0,"head":"7eb1","watcher_state":"absent",
  "refresh":{"escalated":false,"reparsed":2,"reresolved":2},"snapshot_generation":1}}`

func TestRenderIgnoresSearchVolatileFieldsAndBucketsFreshness(t *testing.T) {
	base := Render("search", []byte(searchBody))
	if _, ok := base.Meta["coverage.work"]; !ok || base.Meta["generated_at"] != "2026-09-25T08:23:32Z" {
		t.Fatalf("volatile members not moved to meta: %v", base.Meta)
	}
	// a warm call: another time, another duration, a graph-cache hit, nothing parsed
	warm := Render("search", mutate(t, searchBody, map[string]any{
		"generated_at": "2026-09-25T09:00:00Z", "coverage.work": map[string]any{"elapsed_ms": 3, "lock_wait_ms": 17, "graph_cache": "hit", "files_parsed": 0},
	}))
	if base.StableDigest() != warm.StableDigest() || base.ContentDigest() != warm.ContentDigest() {
		t.Fatalf("identical answers render differently:\n%s\n%s", base.Text, warm.Text)
	}
	// a different answer is a different digest
	moved := Render("search", mutate(t, searchBody, map[string]any{"total": 2}))
	if moved.ContentDigest() == base.ContentDigest() {
		t.Fatal("a changed result kept its digest")
	}

	imp := Render("impact", []byte(impactBody))
	if !strings.Contains(string(imp.Text), `"identity_age":"under_5s"`) || imp.Meta["freshness.identity_age_ms"] == nil {
		t.Fatalf("freshness age not bucketed: %s meta %v", imp.Text, imp.Meta)
	}
	// a warm call: another age in the same bucket, nothing reparsed, a later snapshot
	warm = Render("impact", mutate(t, impactBody, map[string]any{"freshness.identity_age_ms": 4200,
		"freshness.refresh": map[string]any{"escalated": false, "reparsed": 0, "reresolved": 0}, "freshness.snapshot_generation": 2}))
	if warm.ContentDigest() != imp.ContentDigest() {
		t.Fatalf("a warm impact call rendered differently:\n%s\n%s", imp.Text, warm.Text)
	}
	older := Render("impact", mutate(t, impactBody, map[string]any{"freshness.identity_age_ms": 90_000}))
	if older.ContentDigest() == imp.ContentDigest() || !strings.Contains(string(older.Text), `"identity_age":"under_10m"`) {
		t.Fatalf("a coarser freshness bucket must show and count: %s", older.Text)
	}
	stale := Render("impact", mutate(t, impactBody, map[string]any{"freshness.status": "stale"}))
	if stale.ContentDigest() == imp.ContentDigest() {
		t.Fatal("a freshness status change kept its digest")
	}
}

func TestRenderKeepsLiveSessionSectionsInASeparatelyKeyedBlock(t *testing.T) {
	ground := `{"workspace_id":"ws","changed_files":1,"summary":"1 changed file(s). 1 pending memory await your verification.",
	 "pending_for_you":1,"principal":{"id":"agent-a","role":"proposer"},"generated_at":"t"}`
	r := Render("ground", []byte(ground))
	if strings.Contains(string(r.Text), "pending_for_you") || strings.Contains(string(r.Text), "agent-a") {
		t.Fatalf("live sections left in the stable text: %s", r.Text)
	}
	if !strings.Contains(string(r.Live), `"pending_for_you":1`) || !strings.Contains(string(r.Live), "agent-a") {
		t.Fatalf("live block: %s", r.Live)
	}
	other := Render("ground", mutate(t, ground, map[string]any{"pending_for_you": 0, "principal": map[string]any{"id": "agent-b"}}))
	if other.StableDigest() != r.StableDigest() {
		t.Fatal("a live-section change moved the stable digest")
	}
	if other.ContentDigest() == r.ContentDigest() {
		t.Fatal("a live-section change must change the content digest the repeat guard compares")
	}
	recall := Render("recall", []byte(`{"entries":[],"already_shown":2,"consistency_attempts":3,"generated_at":"t"}`))
	if string(recall.Live) != `{"already_shown":2}` || strings.Contains(string(recall.Text), "consistency_attempts") {
		t.Fatalf("recall: text %s live %s", recall.Text, recall.Live)
	}
}

func TestRenderPassesTextResultsThroughWhole(t *testing.T) {
	body := []byte("generated_at: 12:00\nnot json")
	r := Render("Bash", body)
	if r.JSON || string(r.Text) != string(body) || r.Meta != nil {
		t.Fatalf("%+v", r)
	}
}

func TestCanonicalArgs(t *testing.T) {
	a, err := CanonicalArgs([]byte(`{ "q": "Double", "limit": 5, "nested": {"z": 1, "a": 2.50} }`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := CanonicalArgs([]byte(`{"nested":{"a":2.50,"z":1},"limit":5,"q":"Double"}`))
	if a != b || a != `{"limit":5,"nested":{"a":2.50,"z":1},"q":"Double"}` {
		t.Fatalf("canonical args differ: %s / %s", a, b)
	}
	for _, empty := range []string{"", " ", "null", "{}"} {
		if got, err := CanonicalArgs([]byte(empty)); err != nil || got != "{}" {
			t.Fatalf("%q -> %q %v", empty, got, err)
		}
	}
	for _, bad := range []string{`[1]`, `"x"`, `{"a":1} {"b":2}`, `{`} {
		if _, err := CanonicalArgs([]byte(bad)); !errors.Is(err, ErrArgsInvalid) {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}

func completeRev() Revisions {
	return Revisions{Repo: Identity{Key: "repo-1", Complete: true}, Memory: "mem-1", Parser: "xm-index-2", Source: "diag-1"}
}

func TestKeyDerivationNeedsTheRevisionsItsToolDependsOn(t *testing.T) {
	rev := completeRev()
	incomplete := rev
	incomplete.Repo.Complete = false
	if _, err := NewKey("search", []byte(`{"q":"x"}`), incomplete); !errors.Is(err, ErrIdentityIncomplete) {
		t.Fatalf("incomplete identity keyed: %v", err)
	}
	for _, c := range []struct {
		tool string
		drop func(*Revisions)
	}{
		{"search", func(r *Revisions) { r.Parser = "" }},
		{"search", func(r *Revisions) { r.Memory = "" }},
		{"explain", func(r *Revisions) { r.Parser = "" }},
		{"diagnostics", func(r *Revisions) { r.Source = "" }},
		{"ground", func(r *Revisions) { r.Memory = "" }},
	} {
		r := rev
		c.drop(&r)
		if _, err := NewKey(c.tool, nil, r); !errors.Is(err, ErrRevisionMissing) {
			t.Errorf("%s keyed without a revision it needs: %v", c.tool, err)
		}
	}
	base, err := NewKey("search", []byte(`{"q":"x","limit":5}`), rev)
	if err != nil {
		t.Fatal(err)
	}
	reordered, _ := NewKey("mcp__xmustard__search", []byte(`{"limit":5,"q":"x"}`), rev)
	if reordered.Digest() != base.Digest() {
		t.Fatal("argument order or the client's namespace changed the key")
	}
	for name, change := range map[string]func(*Revisions){
		"repo":   func(r *Revisions) { r.Repo.Key = "repo-2" },
		"memory": func(r *Revisions) { r.Memory = "mem-2" },
		"parser": func(r *Revisions) { r.Parser = "xm-index-3" },
		"source": func(r *Revisions) { r.Source = "diag-2" },
	} {
		r := rev
		change(&r)
		k, err := NewKey("search", []byte(`{"q":"x","limit":5}`), r)
		if err != nil || k.Digest() == base.Digest() || k.RevisionKey() == base.RevisionKey() {
			t.Errorf("a %s revision change kept the key (%v)", name, err)
		}
	}
	other, _ := NewKey("search", []byte(`{"q":"y"}`), rev)
	if other.Digest() == base.Digest() || other.RevisionKey() != base.RevisionKey() {
		t.Fatal("arguments must change the key but not the revision key")
	}
	if base.Renderer != RendererVersion {
		t.Fatalf("renderer %q", base.Renderer)
	}
}

func TestToolPolicyTable(t *testing.T) {
	for _, c := range []struct {
		tool                            string
		sideEffecting, exploratory, can bool
	}{
		{"search", false, true, true}, {"explain", false, true, true}, {"impact", false, true, true},
		{"diagnostics", false, false, true}, {"ground", false, false, false}, {"recall", false, true, false},
		{"why_failed", false, false, false}, {"remember", true, false, false}, {"verify", true, false, false},
		{"mcp__xmustard__remember", true, false, false}, {"mcp__xmustard__impact", false, true, true},
		{"Read", false, true, false}, {"Grep", false, true, false}, {"Glob", false, true, false}, {"LS", false, true, false},
		{"Bash", true, false, false}, {"exec_command", true, false, false}, {"Edit", true, false, false},
		{"Write", true, false, false}, {"TodoWrite", true, false, false}, {"mcp__github__create_issue", true, false, false},
		{"mcp__github__search", true, false, false}, {"docs.read", true, false, false},
		{"WebFetch", true, false, false},
	} {
		p := ToolPolicyFor(c.tool)
		if p.SideEffecting != c.sideEffecting || p.Exploratory != c.exploratory || p.Cacheable != c.can {
			t.Errorf("%s: %+v", c.tool, p)
		}
	}
}
