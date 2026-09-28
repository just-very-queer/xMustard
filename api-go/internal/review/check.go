package review

import (
	"fmt"
	"slices"

	"xmustard/api-go/internal/anchor"
)

// Fact is one check's answer. Unknown means the check could not run: symbol_resolved
// needs the resident index, and code_present and in_changed_hunk need a file's head
// that was not read (anchor reason head_unread).
type Fact string

const (
	Yes     Fact = "yes"
	No      Fact = "no"
	Unknown Fact = "unknown"
)

func factOf(b bool) Fact {
	if b {
		return Yes
	}
	return No
}

// Checks are the deterministic checks of one finding (PAR-REV-05). CodePresent is a
// partial Ground A: open-code-review's filter drops a comment whose code is absent from
// its subject file's diff, judged by a model; this checks only that the quoted code is
// in the file the finding is filed (or re-filed) against, at head or, for deleted code,
// on the old side. Semantic judgment stays with a reviewer.
type Checks struct {
	CodePresent   Fact `json:"code_present"`
	InChangedHunk Fact `json:"in_changed_hunk"` // the range holds an added or deleted line
	InScope       Fact `json:"in_scope"`        // the anchored file is part of the change
	// SymbolResolved stays unknown: resolving the enclosing symbol needs the resident
	// index, and this package runs no query.
	SymbolResolved Fact `json:"symbol_resolved"`
}

// Support labels, by code_present. An unsupported finding (its code is not where it
// says) is kept and shown, never deleted; an unchecked one could not be checked.
const (
	Supported   = "supported"
	Unsupported = "unsupported"
	Unchecked   = "unchecked"
)

var supportOf = map[Fact]string{Yes: Supported, No: Unsupported, Unknown: Unchecked}

// Anchored is a finding with its anchor, checks and normalization record.
type Anchored struct {
	Finding
	Anchor     anchor.Anchor `json:"anchor"`
	Checks     Checks        `json:"checks"`
	Support    string        `json:"support"`
	Normalized []string      `json:"normalized,omitempty"`
}

// Counts summarize one anchoring: findings per anchor status and per support label,
// and those whose range holds a changed line.
type Counts struct {
	Findings      int                   `json:"findings"`
	ByStatus      map[anchor.Status]int `json:"by_status"`
	BySupport     map[string]int        `json:"by_support"`
	InChangedHunk int                   `json:"in_changed_hunk"`
}

// Anchor places every finding in the change (anchor.Set.PlaceAll, so each finding's own
// file is read before any is re-filed) and checks it.
func Anchor(findings []Finding, set *anchor.Set) ([]Anchored, Counts) {
	qs := make([]anchor.Query, len(findings))
	for i, f := range findings {
		qs[i] = anchor.Query{Path: f.Path, Snippet: f.snippet} // a refused snippet is empty: no_snippet, nothing read
	}
	anchors := set.PlaceAll(qs)
	out := make([]Anchored, 0, len(findings))
	counts := Counts{Findings: len(findings), ByStatus: map[anchor.Status]int{}, BySupport: map[string]int{}}
	for i, f := range findings {
		a := anchors[i]
		if f.snippetErr != nil {
			a = anchor.Unplaced(f.Path, anchor.SnippetReason(f.snippetErr))
		}
		c := checks(a, set)
		r := Anchored{Finding: f, Anchor: a, Checks: c, Support: supportOf[c.CodePresent], Normalized: slices.Clone(f.notes)}
		if claimed := f.claimed; claimed != [2]int{} && claimed != [2]int{a.StartLine, a.EndLine} {
			r.Normalized = append(r.Normalized, fmt.Sprintf("the producer's lines %d-%d were replaced by the anchor", claimed[0], claimed[1]))
		}
		counts.ByStatus[a.Status]++
		counts.BySupport[r.Support]++
		if c.InChangedHunk == Yes {
			counts.InChangedHunk++
		}
		out = append(out, r)
	}
	return out, counts
}

// checks are one anchor's checks. When a head the search needed was not read, whether
// the code is there, and whether its range holds a changed line, are unknown.
func checks(a anchor.Anchor, set *anchor.Set) Checks {
	c := Checks{CodePresent: factOf(a.Present()), InChangedHunk: factOf(set.Touches(a)),
		InScope: factOf(set.File(a.Path) != nil), SymbolResolved: Unknown}
	if a.Unchecked() {
		c.CodePresent, c.InChangedHunk = Unknown, Unknown
	}
	return c
}
