package workspaceops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"xmustard/api-go/internal/govstore"
)

// Recall disclosure and budgets (PAR-RCL-04/05/06): a page of the ranked candidates,
// minus what this session was already shown, rendered in full, compact or names-only
// form, and fitted to max_chars with what was cut reported.

// recallPage is the ranked, drift-checked candidate window of one recall and the page
// of it the request asked for.
type recallPage struct {
	candidates           []scoredEntry
	offset, limit, total int
}

// RecallLine is the compact render of an entry: one line of its text; fetch the full
// entry with recall(entry_id).
type RecallLine struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Trust        string        `json:"trust"`
	State        string        `json:"state"`
	Stale        bool          `json:"stale,omitempty"`
	Paths        []string      `json:"paths,omitempty"`
	VotesNeeded  *int          `json:"votes_needed,omitempty"`
	Text         string        `json:"text"`
	ScoreDetails *ScoreDetails `json:"score_details,omitempty"`
}

// RecallName is the names_only render: enough to choose what to fetch.
type RecallName struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Topic string   `json:"topic,omitempty"`
	State string   `json:"state"`
	Stale bool     `json:"stale,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

// compactTextChars bounds a compact line's text.
const compactTextChars = 160

// recallRender renders the returned entries. cut says the budget may shorten the last
// entry's content instead of dropping it; delivers says the render shows the content,
// so the entries count as shown to the session.
type recallRender struct {
	list     func([]ContextEntry) any
	cut      bool
	delivers bool
}

var recallRenders = map[string]recallRender{
	"full":    {list: func(es []ContextEntry) any { return es }, cut: true, delivers: true},
	"compact": {list: func(es []ContextEntry) any { return mapRows(es, compactRow) }, delivers: true},
	"names":   {list: func(es []ContextEntry) any { return mapRows(es, nameRow) }},
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
		VotesNeeded: e.VotesNeeded, Text: oneLine(e.Content, compactTextChars), ScoreDetails: e.ScoreDetails}
}

func nameRow(e ContextEntry) RecallName {
	return RecallName{ID: e.ID, Title: e.Title, Topic: e.Topic, State: e.State, Stale: e.Stale, Paths: e.Paths}
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

// label fills the recall-only fields of a returned entry.
func (c scoredEntry) label(explain bool) ContextEntry {
	e := c.entry
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
	seenKey := recallSeenKey(workspaceID, req.Caller, req.SessionID)
	shown := recallSeen.shown(seenKey)
	var picked []ContextEntry
	var at []int                  // candidate index of each picked entry
	prints := map[string]string{} // seen-print of each picked entry
	alreadyShown, next := 0, page.offset
	for i := page.offset; i < len(page.candidates) && len(picked) < page.limit; i++ {
		next = i + 1
		c := page.candidates[i]
		if fp, ok := shown[c.rank.ID]; ok && fp == c.seenPrint() {
			alreadyShown++
			continue
		}
		picked, at = append(picked, c.label(req.Explain)), append(at, i)
		prints[c.rank.ID] = c.seenPrint()
	}
	render := recallRenders[req.renderName()]
	res["render"] = req.renderName()
	res["already_shown"] = alreadyShown
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
			res["next_cursor"] = encodeRecallCursor(resume, req.fingerprint())
		}
		b, _ := json.Marshal(res)
		return len(b)
	}
	kept := picked
	build(kept, len(picked))
	if req.MaxChars > 0 {
		var truncated int
		kept, truncated = fitRecallBudget(picked, render.cut, req.MaxChars-budgetReportReserve, build)
		res["output_budget"] = map[string]any{
			"max_chars": req.MaxChars, "returned": len(kept), "dropped": len(picked) - len(kept), "truncated": truncated,
		}
	}
	if render.delivers && req.SessionID != "" {
		delivered := map[string]string{}
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
func fitRecallBudget(entries []ContextEntry, cut bool, limit int, build func([]ContextEntry, int) int) ([]ContextEntry, int) {
	for n := len(entries); n > 0; n-- {
		kept := entries[:n:n]
		if build(kept, n) > limit {
			continue
		}
		if n < len(entries) && cut {
			if withCut, ok := cutToFit(kept, entries[n], n, limit, build); ok {
				return withCut, 1
			}
			build(kept, n)
		}
		return kept, 0
	}
	if cut && len(entries) > 0 {
		if withCut, ok := cutToFit(nil, entries[0], 0, limit, build); ok {
			return withCut, 1
		}
	}
	build(nil, 0)
	return nil, 0
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

// fingerprint names the ranking a cursor pages: every argument that orders or filters
// it. The page size, render, budget and session may change between pages.
func (r RecallRequest) fingerprint() string {
	r.Cursor, r.Limit, r.NamesOnly, r.Render, r.MaxChars, r.SessionID, r.Explain = "", 0, false, "", 0, "", false
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

func encodeRecallCursor(offset int, fp string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("r1." + strconv.Itoa(offset) + "." + fp))
}

// cursorOffset reads a cursor's offset without checking which query it belongs to.
func cursorOffset(c string) (int, string) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	parts := strings.Split(string(raw), ".")
	if err != nil || len(parts) != 3 || parts[0] != "r1" {
		return -1, ""
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return -1, ""
	}
	return n, parts[2]
}

// decodeRecallCursor returns the offset a cursor continues from; "" starts at 0. A
// cursor from another query is rejected: its offset would page a different ranking.
func decodeRecallCursor(c, fp string) (int, error) {
	if c == "" {
		return 0, nil
	}
	n, got := cursorOffset(c)
	if n < 0 || got != fp {
		return 0, fmt.Errorf("cursor is not a next_cursor of this query: %w", ErrInvalidInput)
	}
	return n, nil
}
