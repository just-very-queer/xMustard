package workspaceops

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckNamePathMatchesTheCoreGrammar(t *testing.T) {
	for _, ok := range []string{"Capture", "Store/Capture", "/Store/Capture", "handler[1]", "/A/b[0]", " spaced "} {
		if err := CheckNamePath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/", "a//b", "a/", "[1]", "m[x]", "m[-1]", "/[0]"} {
		if err := CheckNamePath(bad); !IsInvalidInput(err) {
			t.Errorf("%q: want invalid input, got %v", bad, err)
		}
	}
}

// fakeNavCore installs a core that records each symbolgraph call's arguments and
// answers find-symbol with body.
func fakeNavCore(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	core := filepath.Join(dir, "xmustard-core")
	script := "#!/bin/sh\nif [ \"$1\" = symbolgraph ]; then printf '%s\\n' \"$@\" > '" + argsFile + "'; fi\ncat <<'EOF'\n" + body + "\nEOF\n"
	if err := os.WriteFile(core, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMUSTARD_CORE_BIN", core)
	return argsFile
}

const symbolPageBody = `{"query":"Capture","total":5,"offset":0,"omitted":3,"symbols":[{"name_path":"Store/Capture"},{"name_path":"Capture"}]}`

// search mode=symbol sends its flags before the positionals, pages with a cursor of
// its own ranking and rejects a hybrid search's cursor.
func TestSymbolSearchFlagsAndCursor(t *testing.T) {
	ws := "wsSymbolSearch"
	dir := seedFeedbackWorkspace(t, ws)
	argsFile := fakeNavCore(t, symbolPageBody)
	ctx := context.Background()
	req := SymbolSearchRequest{Query: "Store/Capture", PathGlob: "svc/**", Kinds: "-Variable", Depth: 1, IncludeBody: true, Limit: 2}
	raw, err := SymbolSearchCtx(ctx, dir, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	n := len(lines)
	if lines[1] != "find-symbol" || lines[n-1] != "Store/Capture" || lines[n-2] != ws {
		t.Fatalf("argv %q", lines)
	}
	for _, want := range []string{"--max-matches=2", "--path-glob=svc/**", "--kinds=-Variable", "--depth=1", "--include-body=true"} {
		if !slices.Contains(lines, want) {
			t.Fatalf("argv %q lacks %s", lines, want)
		}
	}
	if slices.Contains(lines, "--offset=0") || slices.Contains(lines, "--include-info=true") {
		t.Fatalf("zero flags are not sent: %q", lines)
	}
	var page map[string]any
	_ = json.Unmarshal(raw, &page)
	cursor, _ := page["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no next_cursor with 3 matches left: %s", raw)
	}
	req.Cursor = cursor
	if _, err := SymbolSearchCtx(ctx, dir, ws, req); err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--offset=2") {
		t.Fatalf("the cursor continues at 2: %q", args)
	}
	// a hybrid cursor, another name path's, and a malformed name path are refused
	hybrid := encodeSearchCursor(2, SearchRequest{Query: "Store/Capture"}.rankingKey(ws))
	for name, r := range map[string]SymbolSearchRequest{
		"hybrid cursor": {Query: "Store/Capture", Cursor: hybrid},
		"other query":   {Query: "Capture", Cursor: cursor},
		"bad path":      {Query: "a//b"},
		"deep":          {Query: "a", Depth: NavMaxDepth + 1},
	} {
		if _, err := SymbolSearchCtx(ctx, dir, ws, r); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	// the budget layer signs the same cursors
	q := url.Values{"mode": {"symbol"}, "q": {"Store/Capture"}, "path_glob": {"svc/**"}, "kinds": {"-Variable"}}
	if SearchCursor(ws, q)(2) != cursor {
		t.Fatal("SearchCursor must sign the symbol ranking")
	}
	if SearchCursor(ws, url.Values{"q": {"Store/Capture"}})(2) != hybrid || SearchCursor(ws, q)(0) != "" {
		t.Fatal("SearchCursor must sign the hybrid ranking, and no cursor at the start")
	}
}

func TestParseAndCheckExplain(t *testing.T) {
	ctx := context.Background()
	for q, want := range map[string]ExplainRequest{
		"path=a.go":      {Path: "a.go", Depth: -1},
		"path=a&depth=2": {Path: "a", Depth: 2},
		"symbol=Store/Capture&chain_depth=1&include_content=true": {Symbol: "Store/Capture", Depth: -1, ChainDepth: 1, IncludeContent: true},
	} {
		v, _ := url.ParseQuery(q)
		got, err := ParseExplain(v, "me")
		want.Caller = "me"
		if err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", q, got, err, want)
		}
	}
	for _, q := range []string{"depth=x", "chain_depth=1.5", "include_content=maybe"} {
		v, _ := url.ParseQuery("path=a&" + q)
		if _, err := ParseExplain(v, ""); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", q, err)
		}
	}
	// the argument checks run before any workspace is read
	for name, req := range map[string]ExplainRequest{
		"none":       {Depth: -1},
		"two":        {Path: "a", Symbol: "b", Depth: -1},
		"deep":       {Path: "a", Depth: NavMaxDepth + 1},
		"negative":   {Path: "a", Depth: -2},
		"long chain": {Symbol: "a", Depth: -1, ChainDepth: NavMaxChainDepth + 1},
		"bad symbol": {Symbol: "a//b", Depth: -1},
	} {
		if _, err := ExplainCtx(ctx, t.TempDir(), "wsNone", req); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
}

func TestExplainCursorsAreBoundToTheirList(t *testing.T) {
	q := url.Values{"path": {"svc"}}
	c := ExplainCursor("ws", q)(3)
	if n, err := DecodeExplainCursor("ws", q, c); err != nil || n != 3 {
		t.Fatalf("round trip: %d %v", n, err)
	}
	for name, bad := range map[string]func() (int, error){
		"other path":      func() (int, error) { return DecodeExplainCursor("ws", url.Values{"path": {"web"}}, c) },
		"other depth":     func() (int, error) { return DecodeExplainCursor("ws", url.Values{"path": {"svc"}, "depth": {"2"}}, c) },
		"other workspace": func() (int, error) { return DecodeExplainCursor("ws2", q, c) },
		"search cursor": func() (int, error) {
			return DecodeExplainCursor("ws", q, SearchCursor("ws", url.Values{"q": {"svc"}})(3))
		},
		"zero":    func() (int, error) { return DecodeExplainCursor("ws", q, ExplainCursor("ws", q)(0)) },
		"garbage": func() (int, error) { return DecodeExplainCursor("ws", q, "nope") },
	} {
		if _, err := bad(); !IsInvalidInput(err) {
			t.Errorf("%s: want invalid input, got %v", name, err)
		}
	}
	if n, err := DecodeExplainCursor("ws", q, ""); err != nil || n != 0 {
		t.Fatalf("no cursor: %d %v", n, err)
	}
}
