package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// `mode=text` is its own lane, not the hybrid under another name: the core is asked
// for the text mode, and the answer's hits are its own.
func TestSearchTextModeReachesTheCoreAsItsOwnLane(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	hits := `{"workspace_id":"ws","query":"retry","total":1,"hits":[` +
		`{"kind":"symbol","name":"retry","path":"a.go","line":1,"score":1,` +
		`"lanes_matched":["body"],"reason":"body: term in the chunk body"}]}`
	argsFile := dir + "/args"
	core := writeScript(t, "if [ \"$1\" = search ]; then printf '%s\\n' \"$@\" > "+argsFile+"; fi\ncat <<'EOF'\n"+hits+"\nEOF\n")
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core})
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })

	resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?mode=text&lang=go&q=retry")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("mode=text: status %d; log:\n%s", resp.StatusCode, p.stderr.String())
	}
	var res struct {
		Total int `json:"total"`
		Hits  []struct {
			Path         string   `json:"path"`
			LanesMatched []string `json:"lanes_matched"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Hits) != 1 {
		t.Fatalf("mode=text must return the core's own hits, got %+v", res)
	}
	if len(res.Hits[0].LanesMatched) == 0 || res.Hits[0].LanesMatched[0] != "body" {
		t.Fatalf("a text hit names the body lane: %+v", res.Hits[0])
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	for _, want := range []string{"--mode=text", "--lang=go"} {
		if !strings.Contains(args, want+"\n") {
			t.Fatalf("missing %q in core args %q", want, args)
		}
	}
}

// `lang` keeps the ast-grep hint in pattern mode: it is passed on, and an unknown
// language is ast-grep's own refusal, not a silent empty answer.
func TestSearchLangKeepsThePatternModeHint(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	argsFile := dir + "/args"
	// pattern mode answers from the semantic materialization, which is a different
	// store; an unknown language is refused there, so the shape is asserted instead.
	pattern := `{"match_count":0,"truncated":false,"generated_at":"t"}`
	core := writeScript(t, "if [ \"$1\" = search ]; then printf '%s\\n' \"$@\" > "+argsFile+"; fi\ncat <<'EOF'\n"+pattern+"\nEOF\n")
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core})
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })

	q := url.Values{"mode": {"pattern"}, "lang": {"go"}, "q": {"$A && $A()"}}
	resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Query struct {
			Language string `json:"language"`
		} `json:"query"`
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("mode=pattern: status %d; log:\n%s", resp.StatusCode, p.stderr.String())
	}
	// The hint must reach ast-grep, not merely avoid the lexical --lang flag: an absent
	// language in the recorded query is ast-grep searching every language, which the old
	// assertion could not see.
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("mode=pattern body: %v (%s)", err, body)
	}
	if out.Query.Language != "go" {
		t.Fatalf("pattern mode keeps lang as the ast-grep hint: got %q, want \"go\"", out.Query.Language)
	}
	// the pattern lane never asked the core's `search`, so no --lang flag was sent.
	// Reading the args file only when it exists passes vacuously when the file was never
	// written, so the positive assertion above is what makes this one mean something.
	if raw, err := os.ReadFile(argsFile); err == nil && strings.Contains(string(raw), "--lang=") {
		t.Fatalf("pattern mode must not send --lang to the core: %q", raw)
	}
}

// `rerank` must not be a way around the two filters this row added. The rerank branch
// sits before the lexical default and calls a search that takes no `mode` or `lang`, so
// without a guard `mode=text&rerank=...` answers from the unfiltered hybrid: a path-only
// match, or a file in another language, in a response that says it was asked for neither.
func TestSearchRerankDoesNotBypassTheLexicalFilters(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	hybrid := `{"workspace_id":"ws","query":"retry","total":1,"hits":[` +
		`{"kind":"file","name":"notes","path":"a.md","line":null,"score":1,` +
		`"lanes_matched":["bm25"],"reason":"bm25: every query term in the chunk"}]}`
	core := writeScript(t, "cat <<'EOF'\n"+hybrid+"\nEOF\n")
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core})
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })

	for _, tc := range []struct{ name string; q url.Values }{
		{"text mode", url.Values{"mode": {"text"}, "q": {"retry"}, "rerank": {"just-mini"}}},
		{"lang filter", url.Values{"lang": {"go"}, "q": {"retry"}, "rerank": {"just-mini"}}},
	} {
		resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?" + tc.q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// 400 is the fail-closed answer; 200 would hand back an unfiltered hybrid hit.
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s with rerank must not answer from the unfiltered hybrid: 200 %s",
				tc.name, body)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s with rerank: status %d; log:\n%s", tc.name, resp.StatusCode, p.stderr.String())
		}
	}
}

// An unknown mode is refused: the tool advertises four, and a fifth is a 400, not the
// hybrid by default.
func TestSearchRefusesAnUnknownMode(t *testing.T) {
	dir := t.TempDir()
	seedCoreWorkspace(t, dir, "ws")
	core := writeScript(t, "cat <<'EOF'\n{\"workspace_id\":\"ws\",\"query\":\"x\",\"total\":0,\"hits\":[]}\nEOF\n")
	p := startAPIProc(t, map[string]string{"XMUSTARD_DATA_DIR": dir, "XMUSTARD_CORE_BIN": core})
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })

	// mode=exact belongs to WS-59c; until it lands it must not silently answer hybrid.
	for _, m := range []string{"exact", "regex", "semantic"} {
		q := url.Values{"mode": {m}, "q": {"x"}}
		resp, err := testClient.Get(p.base + "/api/workspaces/ws/search?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("mode=%s must be refused with 400, got %d", m, resp.StatusCode)
		}
	}
}
