package workspaceops

import (
	"bytes"
	"context"
	"errors"
	"os"
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

// A cursor is bound to the ranking it pages, so a request whose filters differ must not
// accept each other's cursor: `lang=go` and no `lang` are two rankings.
func TestSearchCursorIsBoundToLangAndMode(t *testing.T) {
	base := SearchRequest{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.go"}
	for _, other := range []SearchRequest{
		{Query: "q", Lang: "rust", Mode: SearchModeText, PathGlob: "**/*.go"},
		{Query: "q", Lang: "go", Mode: SearchModeHybrid, PathGlob: "**/*.go"},
		{Query: "q", Mode: SearchModeText, PathGlob: "**/*.go"},
		{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.rs"},
	} {
		if bytes.Equal(base.rankingKey("ws"), other.rankingKey("ws")) {
			t.Errorf("a filter change must change the ranking key: %+v vs %+v", base, other)
		}
	}
	same := SearchRequest{Query: "q", Lang: "go", Mode: SearchModeText, PathGlob: "**/*.go"}
	if !bytes.Equal(base.rankingKey("ws"), same.rankingKey("ws")) {
		t.Error("the same request must rank identically")
	}
}
