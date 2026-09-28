package review

import (
	"fmt"
	"slices"

	"xmustard/api-go/internal/anchor"
)

// Checks are the deterministic checks of one finding (PAR-REV-05). CodePresent is a
// partial Ground A: open-code-review's filter drops a comment whose code is absent from
// its subject file's diff, judged by a model; this checks only that the quoted code is
// in the file the finding is filed (or re-filed) against, at head or, for deleted code,
// on the old side. Semantic judgment stays with a reviewer.
type Checks struct {
	CodePresent   bool `json:"code_present"`
	InChangedHunk bool `json:"in_changed_hunk"` // the range holds an added or deleted line
	InScope       bool `json:"in_scope"`        // the anchored file is part of the change
	// SymbolResolved stays "unknown": resolving the enclosing symbol needs the resident
	// index, and this package runs no query.
	SymbolResolved string `json:"symbol_resolved"`
}

// Support labels. An unsupported finding (its code is not where it says) is kept and
// shown, never deleted.
const (
	Supported   = "supported"
	Unsupported = "unsupported"
)

// Anchored is a finding with its anchor, checks and normalization record.
type Anchored struct {
	Finding
	Anchor     anchor.Anchor `json:"anchor"`
	Checks     Checks        `json:"checks"`
	Support    string        `json:"support"`
	Normalized []string      `json:"normalized,omitempty"`
}

// Counts summarize one anchoring.
type Counts struct {
	Findings      int                   `json:"findings"`
	ByStatus      map[anchor.Status]int `json:"by_status"`
	Supported     int                   `json:"supported"`
	Unsupported   int                   `json:"unsupported"`
	InChangedHunk int                   `json:"in_changed_hunk"`
}

// Anchor places each finding in the change (anchor.Set.Place) and checks it.
func Anchor(findings []Finding, set *anchor.Set) ([]Anchored, Counts) {
	out := make([]Anchored, 0, len(findings))
	counts := Counts{Findings: len(findings), ByStatus: map[anchor.Status]int{}}
	for _, f := range findings {
		a := anchor.Unplaced(f.Path, anchor.SnippetReason(f.snippetErr))
		if f.snippetErr == nil {
			a = set.Place(f.Path, f.snippet)
		}
		r := Anchored{Finding: f, Anchor: a, Support: Unsupported, Normalized: slices.Clone(f.notes),
			Checks: Checks{CodePresent: a.Present(), InChangedHunk: set.Touches(a), InScope: set.File(a.Path) != nil, SymbolResolved: "unknown"}}
		if r.Checks.CodePresent {
			r.Support = Supported
		}
		if c := f.claimed; c != [2]int{} && c != [2]int{a.StartLine, a.EndLine} {
			r.Normalized = append(r.Normalized, fmt.Sprintf("the producer's lines %d-%d were replaced by the anchor", c[0], c[1]))
		}
		counts.add(r)
		out = append(out, r)
	}
	return out, counts
}

func (c *Counts) add(r Anchored) {
	c.ByStatus[r.Anchor.Status]++
	if r.Support == Supported {
		c.Supported++
	} else {
		c.Unsupported++
	}
	if r.Checks.InChangedHunk {
		c.InChangedHunk++
	}
}
