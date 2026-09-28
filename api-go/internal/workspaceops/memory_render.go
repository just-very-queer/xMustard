package workspaceops

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/injection"
)

// Recall disclosure and budgets (PAR-RCL-04/05/06): a page of the ranked candidates,
// minus what this session was already shown, rendered in full, compact or names-only
// form, and fitted to max_chars with what was cut reported.

// recallPage is the ranked, drift-checked candidate window of one recall (positions
// span.lo to span.hi of the ranking) and the page of it the request asked for.
type recallPage struct {
	candidates   []scoredEntry
	span         recallSpan
	cursor       recallCursor
	limit, total int
}

// RecallLine is the compact render of an entry: one line of its text; fetch the full
// entry with recall(entry_id).
type RecallLine struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Trust          string        `json:"trust"`
	State          string        `json:"state"`
	Stale          bool          `json:"stale,omitempty"`
	Paths          []string      `json:"paths,omitempty"`
	VotesNeeded    *int          `json:"votes_needed,omitempty"`
	Quarantine     string        `json:"quarantine,omitempty"`
	InjectionFlags []string      `json:"injection_flags,omitempty"`
	Text           string        `json:"text"`
	ScoreDetails   *ScoreDetails `json:"score_details,omitempty"`
}

// RecallName is the names_only render: enough to choose what to fetch.
type RecallName struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Topic          string   `json:"topic,omitempty"`
	State          string   `json:"state"`
	Stale          bool     `json:"stale,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	Quarantine     string   `json:"quarantine,omitempty"`
	InjectionFlags []string `json:"injection_flags,omitempty"`
}

// compactTextChars bounds a compact line's text.
const compactTextChars = 160

// recallRender renders the returned entries. cut says the budget may shorten the last
// entry's content instead of dropping it; delivers says the render shows the whole
// content, so the entries count as shown to the session (a compact line or a name does
// not: a later full recall still returns the entry).
type recallRender struct {
	list     func([]ContextEntry) any
	cut      bool
	delivers bool
}

var recallRenders = map[string]recallRender{
	"full":    {list: func(es []ContextEntry) any { return es }, cut: true, delivers: true},
	"compact": {list: func(es []ContextEntry) any { return mapRows(es, compactRow) }},
	"names":   {list: func(es []ContextEntry) any { return mapRows(es, nameRow) }},
}

// size is what one entry adds to the rendered list, its separator aside.
func (r recallRender) size(e ContextEntry) int {
	b, _ := json.Marshal(r.list([]ContextEntry{e}))
	return len(b) - len("[]")
}

// recallRenderArgs are the render values a caller may pass; names comes from names_only.
var recallRenderArgs = []string{"", "full", "compact"}

func (r RecallRequest) renderName() string {
	if r.NamesOnly {
		return "names"
	}
	return fallbackString(r.Render, "full")
}

func mapRows[T any](es []ContextEntry, f func(ContextEntry) T) []T {
	out := make([]T, len(es))
	for i, e := range es {
		out[i] = f(e)
	}
	return out
}

func compactRow(e ContextEntry) RecallLine {
	return RecallLine{ID: e.ID, Title: e.Title, Trust: e.Trust, State: e.State, Stale: e.Stale, Paths: e.Paths,
		VotesNeeded: e.VotesNeeded, Quarantine: e.Quarantine, InjectionFlags: e.InjectionFlags,
		Text: oneLine(e.Content, compactTextChars), ScoreDetails: e.ScoreDetails}
}

func nameRow(e ContextEntry) RecallName {
	return RecallName{ID: e.ID, Title: e.Title, Topic: e.Topic, State: e.State, Stale: e.Stale, Paths: e.Paths,
		Quarantine: e.Quarantine, InjectionFlags: e.InjectionFlags}
}

// oneLine folds text onto one line of at most n bytes, cut on a rune boundary.
func oneLine(text string, n int) string {
	s := strings.Join(strings.Fields(text), " ")
	if len(s) <= n {
		return s
	}
	return cutRunes(s, n-len("…")) + "…"
}

func cutRunes(s string, n int) string {
	n = max(min(n, len(s)), 0)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// label fills the recall-only fields of a returned entry, and the instruction patterns
// its text matches (WS-56).
func (c scoredEntry) label(explain bool) ContextEntry {
	e := c.entry
	e.InjectionFlags = injection.Scan(e.Title, e.Content).Flags
	e.State, e.Trust = c.rank.State, trustLabel(c.rank)
	if c.rank.State == govstore.RankPending {
		need := max(c.rank.RequiredVerifications-c.rank.PeerApprovals, 0)
		e.VotesNeeded = &need
	}
	if explain {
		d := c.details
		e.ScoreDetails = &d
	}
	return e
}

// finishRecall pages the candidates, suppresses what the session was shown, renders
// and budgets the page, and completes the result.
func finishRecall(workspaceID string, req RecallRequest, res map[string]any, page recallPage) map[string]any {
	seenKey, session := recallSeenKey(workspaceID, req.Caller, req.SessionID)
	var shown map[string]seenPrint
	if session {
		shown = recallSeen.shown(seenKey, page.candidates[page.span.offset-page.span.lo:page.span.end-page.span.lo])
	}
	var picked []ContextEntry
	var at []int                     // ranking position of each picked entry
	prints := map[string]seenPrint{} // seen-print of each picked entry
	alreadyShown, next := 0, page.span.offset
	for pos := page.span.offset; pos < page.span.end && len(picked) < page.limit; pos++ {
		next = pos + 1
		c := page.candidates[pos-page.span.lo]
		if c.withheld {
			continue
		}
		if fp, ok := shown[c.rank.ID]; ok && fp == c.seenPrint() {
			alreadyShown++
			continue
		}
		picked, at = append(picked, c.label(req.Explain)), append(at, pos)
		prints[c.rank.ID] = c.seenPrint()
	}
	render := recallRenders[req.renderName()]
	res["render"] = req.renderName()
	res["already_shown"] = alreadyShown
	res["data_notice"] = injection.DataNotice // memory is data, not instructions (WS-56)
	// build renders the first n picked entries as kept, and returns the result's size.
	build := func(kept []ContextEntry, n int) int {
		resume := next
		if n < len(picked) {
			resume = at[n]
		}
		res["entries"] = render.list(kept)
		res["returned"] = len(kept)
		res["verification_modes"] = countVerificationModes(kept) // per-mode counts of the returned entries
		res["stale_count"] = countStale(kept)
		res["conflicts"] = overlappingMemory(kept)
		res["omitted"] = max(page.total-resume, 0)
		delete(res, "next_cursor")
		if resume < page.total {
			res["next_cursor"] = req.encodeCursor(workspaceID, recallCursor{offset: resume, block: page.cursor.block})
		}
		b, _ := json.Marshal(res)
		return len(b)
	}
	kept := picked
	if req.MaxChars > 0 {
		var truncated int
		kept, truncated = fitRecallBudget(picked, render.size, render.cut, req.MaxChars-budgetReportReserve, build)
		res["output_budget"] = map[string]any{
			"max_chars": req.MaxChars, "returned": len(kept), "dropped": len(picked) - len(kept), "truncated": truncated,
		}
	} else {
		build(kept, len(picked))
	}
	if render.delivers && session {
		delivered := map[string]seenPrint{}
		for _, e := range kept {
			if !e.ContentTruncated {
				delivered[e.ID] = prints[e.ID]
			}
		}
		recallSeen.mark(seenKey, delivered)
	}
	return res
}

// budgetReportReserve is kept free for the output_budget member, so the final result
// fits max_chars.
const budgetReportReserve = 128

// fitRecallBudget keeps the longest prefix of entries whose result fits limit, and
// appends the first entry that does not fit with its content cut when the render
// allows it. It returns the kept entries and how many were cut (0 or 1); the result is
// left built for them.
//
// Every kept entry adds at least its own size, so the prefix whose sizes alone pass
// limit bounds the search. Short of the whole page the result grows with each entry
// (only the whole page can drop next_cursor, so it is tried first), so the longest
// fitting prefix is a binary search: each entry is sized once, then O(log n) builds of
// at most limit bytes of entries.
func fitRecallBudget(entries []ContextEntry, size func(ContextEntry) int, cut bool, limit int, build func([]ContextEntry, int) int) ([]ContextEntry, int) {
	bound, sum := 0, 0
	for bound < len(entries) {
		if sum += size(entries[bound]); sum > limit {
			break
		}
		bound++
	}
	fits := func(n int) bool { return build(entries[:n:n], n) <= limit }
	if bound == len(entries) && fits(bound) {
		return entries, 0
	}
	bound = min(bound, len(entries)-1)
	n := max(sort.Search(bound+1, func(k int) bool { return !fits(k) })-1, 0)
	kept := entries[:n:n]
	if n < len(entries) && cut {
		if withCut, ok := cutToFit(kept, entries[n], n, limit, build); ok {
			return withCut, 1
		}
	}
	build(kept, n)
	return kept, 0
}

// minCutChars is the least content worth returning cut rather than dropping.
const minCutChars = 200

// cutToFit appends e with its content cut to the room left, if at least minCutChars of
// it fit; the entry is flagged content_truncated and the cursor resumes at it.
func cutToFit(kept []ContextEntry, e ContextEntry, n, limit int, build func([]ContextEntry, int) int) ([]ContextEntry, bool) {
	shell := e
	shell.Content, shell.ContentTruncated = "…", true
	room := limit - build(append(slices.Clone(kept), shell), n)
	for keep := room; keep >= minCutChars; keep -= 64 {
		c := shell
		c.Content = cutRunes(e.Content, keep) + "…"
		withCut := append(slices.Clone(kept), c)
		if build(withCut, n) <= limit {
			return withCut, true
		}
	}
	return nil, false
}

func countStale(es []ContextEntry) int {
	n := 0
	for _, e := range es {
		if e.Stale {
			n++
		}
	}
	return n
}

// A recall cursor (recallCursors) continues a ranking at an offset. It is signed over
// the offset, the block size, the workspace and every argument that orders or filters
// the ranking, the caller included.
type recallCursor struct {
	offset int
	// block is the stale-penalty block size the first page fixed (0 before it).
	block int
}

// rankingKey names the ranking a cursor pages: the workspace and every argument that
// orders or filters it, the caller included. The page size, render, budget and
// session may change between pages.
func (r RecallRequest) rankingKey(workspaceID string) []byte {
	r.Cursor, r.Limit, r.NamesOnly, r.Render, r.MaxChars, r.SessionID, r.Explain = "", 0, false, "", 0, "", false
	b, _ := json.Marshal(struct {
		Workspace string
		Request   RecallRequest
	}{workspaceID, r})
	return b
}

func (r RecallRequest) encodeCursor(workspaceID string, c recallCursor) string {
	return recallCursors.encode(r.rankingKey(workspaceID), c.offset, c.block)
}

// decodeCursor returns where the request's cursor continues; no cursor starts at 0. A
// cursor that is malformed, edited, or from another workspace, query or caller is
// rejected.
func (r RecallRequest) decodeCursor(workspaceID string) (recallCursor, error) {
	if r.Cursor == "" {
		return recallCursor{}, nil
	}
	nums, ok := recallCursors.decode(r.Cursor, r.rankingKey(workspaceID))
	if !ok || nums[0] < 0 || nums[1] < 1 {
		return recallCursor{}, fmt.Errorf("cursor is not a next_cursor of this query: %w", ErrInvalidInput)
	}
	return recallCursor{offset: nums[0], block: nums[1]}, nil
}
