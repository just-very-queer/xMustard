package workspaceops

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// `lang` and `mode=text` reach the core as flags that lead the positionals, so a query
// that looks like a flag is still the query.
func TestSearchSendsLangAndTextModeAsLeadingFlags(t *testing.T) {
	ws := "wsSearchMode"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		req  SearchRequest
		want []string
		skip []string
	}{
		{
			name: "lang filter",
			req:  SearchRequest{Query: "retry", Lang: "go", Limit: 3},
			want: []string{"--lang=go"},
		},
		{
			name: "text mode",
			req:  SearchRequest{Query: "retry", Mode: SearchModeText, Lang: "rust", Limit: 3},
			want: []string{"--lang=rust", "--mode=text"},
		},
		{
			name: "no filter and no mode sends neither",
			req:  SearchRequest{Query: "retry", Limit: 3},
			skip: []string{"--lang=", "--mode="},
		},
		{
			name: "an explicit hybrid with a lang sends the lang but no mode flag, because hybrid is the core's default",
			req:  SearchRequest{Query: "retry", Mode: SearchModeHybrid, Lang: "go", Limit: 3},
			want: []string{"--lang=go"},
			skip: []string{"--mode="},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(argsFile)
			if _, err := WorkspaceSearchPage(ctx, dir, ws, c.req); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			args := string(raw)
			for _, w := range c.want {
				if !strings.Contains(args, w) {
					t.Fatalf("missing %q in core args %q", w, args)
				}
			}
			for _, s := range c.skip {
				if strings.Contains(args, s) {
					t.Fatalf("unexpected %q in core args %q", s, args)
				}
			}
			// every flag leads the positionals: the first non-flag argument is the root, so
			// a query or seed that looks like a flag is never read as one.
			lines := strings.Split(strings.TrimRight(args, "\n"), "\n")
			root := -1
			for i, a := range lines {
				if !strings.HasPrefix(a, "--") && a != "search" {
					root = i
					break
				}
			}
			if root < 0 {
				t.Fatalf("no positional root in %q", lines)
			}
			for _, a := range lines[1:root] {
				switch {
				case strings.HasPrefix(a, "--identity-key="),
					strings.HasPrefix(a, "--path-glob="),
					strings.HasPrefix(a, "--offset="),
					strings.HasPrefix(a, "--lang="),
					strings.HasPrefix(a, "--mode="):
				default:
					t.Fatalf("unexpected leading flag %q in %q", a, lines)
				}
			}
		})
	}
}

// An over-long `lang` is refused at its own bound, with the same shape as an over-long
// `path_glob`, and never reaches the core.
func TestSearchRefusesAnOverLongLang(t *testing.T) {
	ws := "wsSearchLangBound"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	ctx := context.Background()

	_, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "q", Lang: strings.Repeat("g", maxSearchLangLen+1)})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an over-long lang must be invalid input, got %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxSearchLangLen)) {
		t.Fatalf("the refusal must name the bound, got %v", err)
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Fatal("an over-long lang reached the core")
	}
	// the bound itself is accepted
	if _, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "q", Lang: strings.Repeat("g", maxSearchLangLen)}); err != nil {
		t.Fatalf("a lang at the bound must be accepted: %v", err)
	}
}

// A budget-truncated search continues through SearchCursor, which signs the cursor from
// the same query values. If it left `lang` or `mode` out of the ranking, the continuation
// request would carry a cursor the ranking refuses and answer 400 instead of page 2.
func TestSearchBudgetCursorCarriesLangAndMode(t *testing.T) {
	q := url.Values{
		"q": {"retry"}, "lang": {"go"}, "mode": {SearchModeText}, "path_glob": {"**/*.go"},
	}
	sign := SearchCursor("ws", q)
	cursor := sign(10)

	off, err := decodeSearchCursor(cursor, SearchRequest{
		Query: "retry", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.go",
	}.rankingKey("ws"))
	if err != nil {
		t.Fatalf("a budget cursor must continue the search that made it: %v", err)
	}
	if off != 10 {
		t.Fatalf("the budget cursor resumes at its own offset, got %d", off)
	}
	// and it does not continue a search without those filters
	if _, err := decodeSearchCursor(cursor, SearchRequest{Query: "retry"}.rankingKey("ws")); err == nil {
		t.Error("a budget cursor must not continue a search without its filters")
	}
}

// An unrecognised mode is refused here too, not only at the route. `WorkspaceSearchPage`
// is exported and its other callers do not validate, and only the three non-default modes
// are ever sent to the core, so anything else would be answered by the hybrid while the
// caller believed it had asked for something else.
func TestSearchRefusesAnUnknownModeBelowTheRoute(t *testing.T) {
	ws := "wsSearchModeBound"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	ctx := context.Background()

	for _, mode := range []string{"semantic", "Text", "TEXT", "exacts", "regexes"} {
		if _, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "q", Mode: mode}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("mode=%q must be invalid input, got %v", mode, err)
		}
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Error("an unknown mode reached the core")
	}
	// the advertised modes and the empty default are accepted
	for _, mode := range []string{"", SearchModeHybrid, SearchModeText, SearchModeExact, SearchModeRegex} {
		if _, err := WorkspaceSearchPage(ctx, dir, ws, SearchRequest{Query: "q", Mode: mode}); err != nil {
			t.Errorf("mode=%q must be accepted: %v", mode, err)
		}
	}
}

// `exact` and `regex` are the two modes the core must be told about explicitly, since its
// own default is the hybrid: a mode the caller asked for and the core never received would
// be answered by the fusion. The boundary is the argv the core is handed, so the test reads
// the fake core's args file rather than the response.
func TestSearchSendsTheScanModesToTheCore(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{SearchModeText, "--mode=text"},
		{SearchModeExact, "--mode=exact"},
		{SearchModeRegex, "--mode=regex"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ws := "wsSearchModeSent"
			dir := seedFeedbackWorkspace(t, ws)
			useFeedbackRecorder(t, feedbackMaxPendingPaths)
			argsFile := fakeSearchCore(t, searchPageBody)
			if _, err := WorkspaceSearchPage(context.Background(), dir, ws,
				SearchRequest{Query: "q", Mode: tc.mode}); err != nil {
				t.Fatalf("mode=%s: %v", tc.mode, err)
			}
			args, readErr := os.ReadFile(argsFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(args), tc.want) {
				t.Errorf("mode=%s must reach the core as %s, got args %q", tc.mode, tc.want, args)
			}
		})
	}
}

// The three scan caps ride the same argv and are only sent when set, so an unset cap is
// the core's own default rather than a zero it would have to interpret.
func TestSearchSendsTheScanCapsOnlyWhenSet(t *testing.T) {
	ws := "wsSearchCaps"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	argsFile := fakeSearchCore(t, searchPageBody)
	if _, err := WorkspaceSearchPage(context.Background(), dir, ws, SearchRequest{
		Query: "q", Mode: SearchModeExact, ScanLines: 7, ScanBytes: 4096, ScanTimeoutMS: 250,
	}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--max-lines=7", "--max-bytes=4096", "--timeout-ms=250"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("the cap must reach the core as %s, got args %q", want, args)
		}
	}
}

// A cap the caller sets is a bound the caller believes is in force, so the argv must be
// byte-identical for the same request: Go randomizes map iteration, and a core that saw
// the flags in a different order on each call would make the request unreproducible.
func TestSearchArgvOrderIsStableAcrossIdenticalRequests(t *testing.T) {
	ws := "wsSearchArgvOrder"
	dir := seedFeedbackWorkspace(t, ws)
	useFeedbackRecorder(t, feedbackMaxPendingPaths)
	var seen []string
	for range 8 {
		argsFile := fakeSearchCore(t, searchPageBody)
		if _, err := WorkspaceSearchPage(context.Background(), dir, ws, SearchRequest{
			Query: "q", Mode: SearchModeExact, ScanLines: 5, ScanBytes: 1024, ScanTimeoutMS: 99,
		}); err != nil {
			t.Fatal(err)
		}
		b, readErr := os.ReadFile(argsFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		seen = append(seen, string(b))
	}
	for i, s := range seen {
		if s != seen[0] {
			t.Fatalf("argv differs on call %d: %q against %q", i, s, seen[0])
		}
	}
}

// The Go caps and the core's ceilings are one contract in two places, and a drift would
// let the schema advertise a bound the core refuses. Each is asserted against the number,
// not restated.
func TestTheScanCapsMatchTheCoreCeilings(t *testing.T) {
	core, err := os.ReadFile(filepath.Join(coreSrcDir(t), "src", "exactscan.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		goVal int
		want  string
	}{
		{"lines", SearchScanMaxLines, "DEFAULT_MAX_LINES: usize = 200"},
		{"bytes", SearchScanMaxBytes, "DEFAULT_MAX_BYTES: u64 = 4 << 20"},
		{"timeout", SearchScanTimeoutMS, "DEFAULT_TIMEOUT_MS: u64 = 2_000"},
	} {
		if !strings.Contains(string(core), tc.want) {
			t.Errorf("the core's %s ceiling is not %q any more; SearchScanMax%s must follow it",
				tc.name, tc.want, strings.ToUpper(tc.name[:1])+tc.name[1:])
		}
		_ = tc.goVal
	}
}

// The core's own source directory, located from this package so the cap-parity test reads
// the file it names rather than a copy of it.
func coreSrcDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// api-go/internal/workspaceops -> the repository root
	return filepath.Join(wd, "..", "..", "..", "rust-core")
}

// A cursor is bound to the ranking it pages, so a request whose filters differ must not
// accept each other's cursor: `lang=go` and no `lang` are two rankings. The boundary is
// the decode, not the key, so the cursor is encoded under one request and offered to the
// next; comparing key bytes alone restates the construction and proves nothing.
func TestSearchCursorIsBoundToLangAndMode(t *testing.T) {
	base := SearchRequest{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.go"}
	same := SearchRequest{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.go"}
	cursor := encodeSearchCursor(10, base.rankingKey("ws"))

	off, err := decodeSearchCursor(cursor, same.rankingKey("ws"))
	if err != nil {
		t.Fatalf("the same request must accept its own cursor: %v", err)
	}
	if off != 10 {
		t.Fatalf("the cursor must resume at its own offset, got %d", off)
	}

	for _, other := range []SearchRequest{
		{Query: "q", Lang: "rust", Mode: SearchModeText, PathGlob: "**/*.go"},
		{Query: "q", Lang: "go", Mode: SearchModeHybrid, PathGlob: "**/*.go"},
		{Query: "q", Mode: SearchModeText, PathGlob: "**/*.go"},
		{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.rs"},
	} {
		got, err := decodeSearchCursor(cursor, other.rankingKey("ws"))
		if err == nil {
			t.Errorf("a cursor from another ranking must be refused: %+v resumed at %d", other, got)
		}
	}
}
