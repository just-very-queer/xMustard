// Package toolbudget is the output-budget contract of the search, explain and impact
// tools (WS-36, PAR-CTX-05; ground has its own, groundbudget). A caller gives a
// max_chars budget. When the tool's JSON result does not fit, a degradation ladder
// declared per result shape (ladders.go) reduces it one named rung at a time: item
// detail first, then fewer items (the rest continue at next_cursor), then counts, then
// the total. A failure, stale or trust signal is never dropped silently: signal objects
// keep every scalar and report their lists' lengths, and every reduced list reports
// what it kept of how many. The reduced original is retained as evidence on an MCP
// call (recover_uri, recover_handle).
//
// The package depends only on the standard library and groundbudget (for the shared
// argument error, docs URI and recovery types), so the MCP shim can validate arguments
// and render the contract's documentation without linking workspaceops.
package toolbudget

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"xmustard/api-go/internal/groundbudget"
)

// Budget bounds, in characters of the JSON result (enforced on its UTF-8 bytes, which
// are never fewer than its characters).
const (
	// MinMaxChars is the smallest budget accepted: a result reduced to its signals
	// and its report fits in it (a search at the total rung measured 1,549 bytes).
	MinMaxChars = 2000
	// MaxMaxChars is the evidence projection target (64 KiB): a larger result would be
	// projected by evidence delivery anyway.
	MaxMaxChars = groundbudget.MaxMaxChars
	// MaxLimit bounds limit= where the budget layer pages a list itself (explain).
	MaxLimit = 500
	// ReportMember is the result member that reports what the budget did.
	ReportMember = groundbudget.ReportMember
	// DocsURI is the MCP resource that documents this contract.
	DocsURI = groundbudget.DocsURI
)

// ArgError rejects an argument; it names the argument so a caller can point at it.
type ArgError = groundbudget.ArgError

// Recovery names the retained unbudgeted result (see groundbudget.Recovery).
type Recovery = groundbudget.Recovery

// Spec is one tool's budget: its default, the query arguments the budget layer owns
// (the API strips them before the tool's handler runs) and its ladders.
type Spec struct {
	Tool string
	// DefaultMaxChars is what the MCP tool sends when max_chars is omitted.
	DefaultMaxChars int
	// Paged: the budget layer pages the tool's list itself with limit= and cursor=
	// (the tool computes the whole list). Otherwise the tool pages (search's own
	// cursor) or does not page (impact: WS-35 owns its paging).
	Paged bool
	// Unpaged names the query arguments that select a result with no paged list: limit
	// and cursor are rejected with them rather than ignored.
	Unpaged []string
	// Ladders by result shape, tried in order: the first whose Match member the result
	// has applies. Every tool ends with a signals-only ladder (Match "").
	Ladders []*Ladder
}

// Owns lists the query arguments the budget layer reads and strips.
func (s *Spec) Owns() []string {
	if s.Paged {
		return []string{"max_chars", "cursor", "limit"}
	}
	return []string{"max_chars"}
}

// specs is every budgeted tool.
var specs = map[string]*Spec{
	"search":  {Tool: "search", DefaultMaxChars: 8000, Ladders: searchLadders},
	"explain": {Tool: "explain", DefaultMaxChars: 6000, Paged: true, Unpaged: []string{"symbol", "uid"}, Ladders: explainLadders},
	"impact":  {Tool: "impact", DefaultMaxChars: 8000, Ladders: impactLadders},
}

// For returns the spec of a budgeted tool.
func For(tool string) (*Spec, bool) {
	s, ok := specs[tool]
	return s, ok
}

// Tools lists the budgeted tools, in the order the documentation shows them.
func Tools() []string { return []string{"search", "explain", "impact"} }

// Request is a validated budget request.
type Request struct {
	// MaxChars is the budget; 0 applies no ladder (paging only, or nothing).
	MaxChars int
	// Limit is the page size of a Paged tool (0: the rest of the list).
	Limit int
	// Cursor continues a Paged tool's list (a next_cursor); decoded by the caller.
	Cursor string
}

// Active reports whether the request asks the budget layer for anything.
func (r Request) Active() bool { return r.MaxChars > 0 || r.Limit > 0 || r.Cursor != "" }

// ParseRequest validates the arguments the budget layer owns. Out-of-range values are
// rejected, never clamped; an argument given twice is rejected.
func (s *Spec) ParseRequest(q url.Values) (Request, *ArgError) {
	var req Request
	for _, name := range s.Owns() {
		if len(q[name]) > 1 {
			return Request{}, &ArgError{Argument: name, Reason: "must be given once"}
		}
	}
	var aerr *ArgError
	if req.MaxChars, aerr = intArg(q, "max_chars", MinMaxChars, MaxMaxChars); aerr != nil {
		return Request{}, aerr
	}
	if !s.Paged {
		return req, nil
	}
	if req.Limit, aerr = intArg(q, "limit", 1, MaxLimit); aerr != nil {
		return Request{}, aerr
	}
	req.Cursor = strings.TrimSpace(q.Get("cursor"))
	for _, u := range s.Unpaged {
		if q.Get(u) == "" {
			continue
		}
		if req.Limit > 0 {
			return Request{}, &ArgError{Argument: "limit", Reason: fmt.Sprintf("pages a list; a %s= result has none", u)}
		}
		if req.Cursor != "" {
			return Request{}, &ArgError{Argument: "cursor", Reason: fmt.Sprintf("pages a list; a %s= result has none", u)}
		}
	}
	return req, nil
}

// intArg reads an optional integer argument within [lo, hi]; absent is 0.
func intArg(q url.Values, name string, lo, hi int) (int, *ArgError) {
	v := strings.TrimSpace(q.Get(name))
	if v == "" {
		return 0, nil
	}
	bounds := map[string]any{"minimum": lo, "maximum": hi}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, &ArgError{Argument: name, Reason: "must be an integer", Extra: bounds}
	}
	if n < lo || n > hi {
		return 0, &ArgError{Argument: name, Reason: fmt.Sprintf("must be between %d and %d (got %d); out-of-range values are rejected, not clamped", lo, hi, n),
			Extra: bounds}
	}
	return n, nil
}
