package workspaceops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSearchCore installs a core that answers `search` with body and records the
// arguments of each search call in the returned file.
func fakeSearchCore(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	core := filepath.Join(dir, "xmustard-core")
	script := "#!/bin/sh\nif [ \"$1\" = search ]; then printf '%s\\n' \"$@\" > '" + argsFile + "'; fi\ncat <<'EOF'\n" + body + "\nEOF\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	return argsFile
}

const searchPageBody = `{"workspace_id":"ws","query":"retry","total":30,"offset":0,"omitted":27,
"hits":[{"kind":"symbol","name":"RetryWithBackoff","path":"store/retry.go","line":6,"lines":[5,17],
"uid":"go:store/retry.go:RetryWithBackoff","score":0.05,"scores":{"bm25":1.5,"rrf":0.03},
"lanes_matched":["bm25","name"],"reasons":["bm25: every query term in the chunk"],"reason":"bm25: every query term in the chunk",
"snippet":[{"line":13,"text":"12: x\n13: time.Sleep(delay)\n14: y"}]},
{"kind":"doc","name":"README.md","path":"README.md","line":1,"score":0.01,"reason":"docs: guide section"},
{"kind":"file","name":"store/retry.go","path":"store/retry.go","score":0.009,"reason":"name: path match"}],
"degradations":["query terms past the first 12 are not searched"],"generated_at":"t"}`

// A page carries next_cursor bound to its query; the cursor sends the next offset to
// the core, and the core's hit fields survive the feedback re-encode.
func TestSearchPagesCarryABoundCursor(t *testing.T) {
	ws := "wsSearchPage"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	ctx := context.Background()

	raw, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "retry", PathGlob: "**/*.go", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"omitted", "degradations", "offset"} {
		if _, ok := page[k]; !ok {
			t.Fatalf("%s dropped by the re-encode: %s", k, raw)
		}
	}
	hit := page["hits"].([]any)[0].(map[string]any)
	for _, k := range []string{"lines", "uid", "scores", "lanes_matched", "reasons", "snippet"} {
		if _, ok := hit[k]; !ok {
			t.Fatalf("hit field %s dropped by the re-encode: %v", k, hit)
		}
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--path-glob=**/*.go") || strings.Contains(string(args), "--offset") {
		t.Fatalf("first page args: %q", args)
	}
	cursor, _ := page["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no next_cursor with 27 hits left: %s", raw)
	}

	if _, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "retry", PathGlob: "**/*.go", Limit: 3, Cursor: cursor}); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--offset=3") {
		t.Fatalf("the cursor must continue at offset 3: %q", args)
	}

	// another query or workspace, an edited offset, a cursor past the window, an unsigned
	// or recall-shaped one and an over-long one are rejected, never clamped.
	req := SearchRequest{Query: "retry", PathGlob: "**/*.go"}
	signed, _ := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(signed), ".")
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, c := range map[string]string{
		"edited offset": b64("s2.5." + parts[2]),
		"past window":   encodeSearchCursor(searchWindow+50, req.rankingKey(ws)),
		"unsigned":      b64("s1.3." + parts[2][:12]),
		"recall cursor": b64("r2.3.1." + parts[2]),
		// signed over this very search, but a recall cursor
		"recall-signed": recallCursors.encode(req.rankingKey(ws), 3, 16),
		"garbage":       "not-a-cursor",
		"over-long":     strings.Repeat("A", maxSearchCursorLen+1),
	} {
		req.Cursor = c
		if _, err := WorkspaceSearchPage(ctx, dir, ws, req); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	for name, req := range map[string]SearchRequest{
		"other query": {Query: "backoff", PathGlob: "**/*.go", Cursor: cursor},
		"other glob":  {Query: "retry", Cursor: cursor},
		"long glob":   {Query: "retry", PathGlob: strings.Repeat("*", maxSearchPathGlobLen+1)},
	} {
		if _, err := WorkspaceSearchPage(ctx, dir, ws, req); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	req.Cursor = cursor
	if _, err := decodeSearchCursor(cursor, req.rankingKey("wsOther")); !IsInvalidInput(err) {
		t.Errorf("another workspace: want invalid input, got %v", err)
	}
}

// Flags lead the core's positionals, so a seed shaped like a flag stays the seed.
func TestSearchSeedNeverReadsAsAFlag(t *testing.T) {
	ws := "wsSearchSeed"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	if _, err := WorkspaceSearchPage(context.Background(), dir, ws, SearchRequest{Query: "--path-glob=x", Seed: "--offset=2", PathGlob: "**/*.go", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	var root int
	for i, a := range lines {
		if !strings.HasPrefix(a, "--") && a != "search" {
			root = i
			break
		}
	}
	for _, a := range lines[1:root] {
		if !strings.HasPrefix(a, "--identity-key=") && !strings.HasPrefix(a, "--path-glob=") {
			t.Fatalf("unexpected leading flag %q in %q", a, lines)
		}
	}
	if tail := lines[root+2:]; len(tail) != 3 || tail[0] != "--path-glob=x" || tail[2] != "--offset=2" {
		t.Fatalf("query, limit and seed must follow the root as positionals: %q", lines)
	}
}

// The last page, and a page that reaches the window, carry no next_cursor.
func TestSearchCursorStopsAtTheEndAndTheWindow(t *testing.T) {
	ranking := SearchRequest{Query: "q"}.rankingKey("ws")
	body := func(total, hits int) json.RawMessage {
		hs := make([]map[string]any, hits)
		for i := range hs {
			hs[i] = map[string]any{"path": "a.go"}
		}
		b, _ := json.Marshal(map[string]any{"total": total, "hits": hs})
		return b
	}
	for _, c := range []struct {
		offset, total, hits int
		want                bool
	}{
		{0, 10, 3, true},
		{7, 10, 3, false},
		{searchWindow - 3, 1000, 3, false},
		{searchWindow - 4, 1000, 3, true},
		{0, 0, 0, false},
	} {
		var res map[string]any
		_ = json.Unmarshal(withSearchCursor(body(c.total, c.hits), c.offset, ranking), &res)
		_, got := res["next_cursor"]
		if got != c.want {
			t.Errorf("offset %d total %d hits %d: next_cursor %v, want %v", c.offset, c.total, c.hits, got, c.want)
		}
		if got {
			if n, err := decodeSearchCursor(res["next_cursor"].(string), ranking); err != nil || n != c.offset+c.hits {
				t.Errorf("cursor decodes to %d, %v", n, err)
			}
		}
	}
}
