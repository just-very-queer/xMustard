package hooks

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"xmustard/api-go/internal/injection"
)

// Rendering of what a hook injects. Repository-derived text (index hits) is framed as
// data and scanned for instruction patterns (WS-56, internal/injection); memory arrives
// already framed by the admission policy; the notes are xMustard's own sentences.

// searchAnswer is the part of the core's search result a hook renders.
type searchAnswer struct {
	Hits []struct {
		Kind         string   `json:"kind"`
		Name         string   `json:"name"`
		Path         string   `json:"path"`
		Line         *int     `json:"line"`
		Lines        *[2]int  `json:"lines"`
		LanesMatched []string `json:"lanes_matched"`
	} `json:"hits"`
	Freshness *struct {
		Source        string   `json:"source"`
		IndexedCommit string   `json:"indexed_commit"`
		Status        string   `json:"status"`
		Dirty         []string `json:"dirty_paths_touching_result"`
	} `json:"freshness"`
}

// RenderHits renders up to max hits of a core search answer as one framed block, or ""
// when there are none or the answer cannot be read.
func RenderHits(query string, raw []byte, max int) string {
	var a searchAnswer
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil || len(a.Hits) == 0 {
		return ""
	}
	var b strings.Builder
	for i, h := range a.Hits {
		if i == max {
			break
		}
		loc := h.Path
		switch {
		case h.Lines != nil:
			loc += fmt.Sprintf(":%d-%d", h.Lines[0], h.Lines[1])
		case h.Line != nil:
			loc += fmt.Sprintf(":%d", *h.Line)
		}
		fmt.Fprintf(&b, "%s %s %s [%s]\n", loc, h.Kind, h.Name, strings.Join(h.LanesMatched, ","))
	}
	if f := a.Freshness; f != nil {
		fmt.Fprintf(&b, "index %s at %s: %s", f.Source, shortCommit(f.IndexedCommit), f.Status)
		if len(f.Dirty) > 0 {
			fmt.Fprintf(&b, "; changed since indexing: %s", strings.Join(f.Dirty, ", "))
		}
	}
	text := strings.TrimRight(b.String(), "\n")
	block := injection.Block{Kind: "search", Trust: "index", Flags: injection.Scan(text).Flags, Text: text}
	return fmt.Sprintf("[xmustard] index hits for %q (the xmustard search tool returns more, with snippets):\n%s", query, injection.Frame(block))
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	if c == "" {
		return "(no commit)"
	}
	return c
}

// StaleNote names the pushed memories anchored to files that changed since they were
// verified; "" when none is stale.
func StaleNote(stale map[string][]string) string {
	if len(stale) == 0 {
		return ""
	}
	var lines []string
	for _, id := range slices.Sorted(maps.Keys(stale)) {
		lines = append(lines, fmt.Sprintf("memory %s is stale: %s changed since it was verified.", id, strings.Join(stale[id], ", ")))
	}
	return "[xmustard] " + strings.Join(lines, " ")
}

// WithheldNote summarizes the related memories a push left out, by reason, and names
// the recall call that shows them labeled; "" when none was withheld.
func WithheldNote(reasons []string, recall string) string {
	if len(reasons) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, r := range reasons {
		counts[r]++
	}
	var parts []string
	for _, r := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s: %d", r, counts[r]))
	}
	return fmt.Sprintf("[xmustard] %d related memor%s not pushed (%s); %s shows them with their trust labels.",
		len(reasons), plural(len(reasons), "y was", "ies were"), strings.Join(parts, ", "), recall)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// SyntaxFile is one file of `xmustard-core syntax-check`.
type SyntaxFile struct {
	Path      string        `json:"path"`
	Status    string        `json:"status"`
	Errors    []SyntaxError `json:"errors"`
	Truncated bool          `json:"truncated"`
}

// SyntaxError is one tree-sitter error or missing node.
type SyntaxError struct {
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Kind     string `json:"kind"`
	Node     string `json:"node"`
	LineHash string `json:"line_hash"`
}

// Key identifies an error across line shifts: its kind, node and source line.
func (e SyntaxError) Key() string { return e.Kind + "|" + e.Node + "|" + e.LineHash }

// ParseSyntax reads a syntax-check answer.
func ParseSyntax(raw []byte) ([]SyntaxFile, error) {
	var r struct {
		Files []SyntaxFile `json:"files"`
	}
	err := json.Unmarshal(raw, &r)
	return r.Files, err
}

// Keys lists a file's error keys (the baseline an edit is compared with).
func (f SyntaxFile) Keys() []string {
	out := make([]string, len(f.Errors))
	for i, e := range f.Errors {
		out[i] = e.Key()
	}
	return out
}

// NewErrors returns the errors not in before, counting duplicates: an edit that adds a
// second identical error reports one.
func NewErrors(before []string, after []SyntaxError) []SyntaxError {
	left := map[string]int{}
	for _, k := range before {
		left[k]++
	}
	var out []SyntaxError
	for _, e := range after {
		if left[e.Key()] > 0 {
			left[e.Key()]--
			continue
		}
		out = append(out, e)
	}
	return out
}

// maxSyntaxListed bounds the errors a note lists.
const maxSyntaxListed = 5

// SyntaxNote renders the syntax errors an edit introduced; "" when there are none.
// Without a pre-edit baseline every current error is listed, and the note says so.
func SyntaxNote(path string, errs []SyntaxError, baseline, truncated bool) string {
	if len(errs) == 0 {
		return ""
	}
	var items []string
	for i, e := range errs {
		if i == maxSyntaxListed {
			items = append(items, fmt.Sprintf("and %d more", len(errs)-i))
			break
		}
		what := map[string]string{"missing": "missing %q", "error": "cannot parse near %q"}[e.Kind]
		if what == "" {
			what = e.Kind + " %q"
		}
		items = append(items, fmt.Sprintf("line %d col %d: "+what, e.Line, e.Column, e.Node))
	}
	scope := "new syntax error(s) after this edit"
	if !baseline {
		scope = "syntax error(s) (no pre-edit baseline: all current errors)"
	}
	more := ""
	if truncated {
		more = " The file has more errors than were checked."
	}
	return fmt.Sprintf("[xmustard] %s: %d %s (tree-sitter): %s.%s", path, len(errs), scope, strings.Join(items, "; "), more)
}
