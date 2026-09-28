// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/resolver.go (ResolveComment,
// resolveFromHunk, extractSideLines, matchConsecutive, resolveFromFileContent,
// splitAndNormalize, normalizeLine). Changes by xMustard contributors:
//   - a snippet is bounded (MaxSnippetLines, MaxSnippetBytes) and normalized once;
//   - a file's hunk sides are parsed once and, like the whole-file pass, skip blank
//     lines, so a snippet spanning a blank line anchors inside its hunk;
//   - each tier counts its matches: one match anchors, several are ambiguous and anchor
//     nothing (upstream takes the first), and a tier with no match falls through;
//   - matching is Knuth-Morris-Pratt over lines (upstream compares the snippet at every
//     line), so a search is linear in the lines searched whatever the snippet repeats;
//   - the result is an Anchor with a status, a side and a reason instead of line
//     numbers written into a comment, an old-side anchor names a renamed file's old
//     path, and head content is loaded only when needed;
//   - a head the caller did not read (over a bound, binary) was never searched, so a
//     snippet no hunk holds is head_unread there, not not_found.

package anchor

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Status says where a snippet was anchored.
type Status string

const (
	ExactNew   Status = "exact_new"  // a hunk's new side: context and added lines
	ExactOld   Status = "exact_old"  // a hunk's old side: context and deleted lines
	InFile     Status = "file"       // the whole file at head, when no hunk side held it
	Relocated  Status = "relocated"  // another file of the change, the only one holding it
	Unanchored Status = "unanchored" // nowhere, or in more than one place
)

// Side is the file version an anchor's line numbers count in.
type Side string

const (
	New Side = "new"
	Old Side = "old"
)

// Why a snippet is unanchored.
const (
	ReasonNoSnippet            = "no_snippet"
	ReasonTooLarge             = "snippet_too_large"
	ReasonNotFound             = "not_found"
	ReasonAmbiguous            = "ambiguous"              // more than once in the file itself
	ReasonAmbiguousAcrossFiles = "ambiguous_across_files" // not in the file, in several others
	ReasonHeadUnread           = "head_unread"            // in no hunk, and a head that could hold it was not read
)

// Anchor is where a snippet sits. StartLine and EndLine are 1-based and inclusive and
// count in Side's version of Path; both are 0 when Status is Unanchored. Candidates is
// how many places matched when Reason is an ambiguity (for ambiguous_across_files, at
// least that many: the search stops at the second). RefiledFrom is the path the snippet
// was filed against when it was relocated.
type Anchor struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Side        Side   `json:"side,omitempty"`
	Status      Status `json:"anchor_status"`
	Reason      string `json:"reason,omitempty"`
	Candidates  int    `json:"candidates,omitempty"`
	RefiledFrom string `json:"refiled_from,omitempty"`
}

// Present reports whether the snippet is in the anchor's file: anchored there, or found
// there more than once. It is false as well when that is not known (Unchecked).
func (a Anchor) Present() bool { return a.Status != Unanchored || a.Reason == ReasonAmbiguous }

// Unchecked reports whether the search could not finish: no hunk holds the snippet and a
// file's head that could hold it was not read, so it is neither found nor absent.
func (a Anchor) Unchecked() bool { return a.Reason == ReasonHeadUnread }

// Unplaced is an unanchored anchor on path.
func Unplaced(path, reason string) Anchor {
	return Anchor{Path: path, Status: Unanchored, Reason: reason}
}

// Snippet bounds: a quoted snippet is evidence to find, not a file to store.
const (
	MaxSnippetLines = 40
	MaxSnippetBytes = 4 << 10
)

var (
	ErrNoSnippet       = errors.New("anchor: the snippet has no non-blank line")
	ErrSnippetTooLarge = fmt.Errorf("anchor: a snippet is at most %d lines and %d bytes", MaxSnippetLines, MaxSnippetBytes)
)

// Snippet is quoted code, normalized: each line trimmed and stripped of one leading +
// and one leading - diff marker, blank lines dropped.
type Snippet struct {
	lines []string
	fail  []int // the lines' Knuth-Morris-Pratt table (failure)
}

// NewSnippet normalizes code, refusing more than MaxSnippetLines lines or
// MaxSnippetBytes bytes and code with no non-blank line.
func NewSnippet(code string) (Snippet, error) {
	if Oversized(code) {
		return Snippet{}, ErrSnippetTooLarge
	}
	var lines []string
	for l := range strings.SplitSeq(code, "\n") {
		if n := normalizeLine(l); n != "" {
			lines = append(lines, n)
		}
	}
	if len(lines) == 0 {
		return Snippet{}, ErrNoSnippet
	}
	return snippetOf(lines), nil
}

func snippetOf(lines []string) Snippet { return Snippet{lines, failure(lines)} }

// Oversized reports whether code is over MaxSnippetBytes bytes or MaxSnippetLines lines
// (trailing line breaks do not count).
func Oversized(code string) bool {
	return len(code) > MaxSnippetBytes || strings.Count(strings.TrimRight(code, "\r\n"), "\n") >= MaxSnippetLines
}

// SnippetReason is the unanchored reason for a NewSnippet error.
func SnippetReason(err error) string {
	if errors.Is(err, ErrSnippetTooLarge) {
		return ReasonTooLarge
	}
	return ReasonNoSnippet
}

// normalizeLine trims whitespace (a CR included) and strips one leading '+' then one
// leading '-' diff marker, as upstream.
func normalizeLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimPrefix(s, "-")
	return strings.TrimSpace(s)
}

// line is a normalized, non-blank line and its number in its file version.
type line struct {
	num  int
	text string
}

func appendLine(ls []line, num int, text string) []line {
	if text == "" {
		return ls
	}
	return append(ls, line{num, text})
}

// contentLines is a whole file's normalized, non-blank lines, allocated once.
func contentLines(content string) []line {
	out := make([]line, 0, strings.Count(content, "\n")+1)
	for num := 1; content != ""; num++ {
		var l string
		l, content, _ = strings.Cut(content, "\n")
		out = appendLine(out, num, normalizeLine(l))
	}
	return out
}

// File is one changed file, parsed once so every snippet anchored against it reuses its
// hunk sides. Its content at head is read on the first snippet that needs it.
type File struct {
	Path    string // at head; the old path of a deleted file
	OldPath string // "" for an added file
	deleted bool   // no version at head

	newSide, oldSide [][]line              // per hunk
	changed          map[Side][]int        // added (new) and deleted (old) line numbers, ascending
	head             func() (string, bool) // the file at head; nil when there is none
	headOnce         sync.Once
	headLines        []line
	headUnread       bool // head reported that it did not read the file
}

// NewFile parses one file's unified diff. oldPath is "" for an added file and newPath
// "" for a deleted one. head returns the file at head, and false when the caller did not
// read it (over a bound, binary): a snippet no hunk holds is then head_unread, not
// not_found. A nil head means there is no content to search.
func NewFile(oldPath, newPath, diff string, head func() (string, bool)) *File {
	f := &File{Path: newPath, OldPath: oldPath, head: head, changed: map[Side][]int{}}
	if f.Path == "" {
		f.Path, f.head, f.deleted = oldPath, nil, true
	}
	for _, h := range ParseHunks(diff) {
		// Sized by the header's counts, capped by the lines the hunk holds.
		nw, od := make([]line, 0, min(h.NewCount, len(h.Lines))), make([]line, 0, min(h.OldCount, len(h.Lines)))
		o, n := h.OldStart, h.NewStart
		for _, hl := range h.Lines {
			text := normalizeLine(hl.Content)
			switch hl.Kind {
			case Context:
				od, nw = appendLine(od, o, text), appendLine(nw, n, text)
				o, n = o+1, n+1
			case Added:
				nw, f.changed[New] = appendLine(nw, n, text), append(f.changed[New], n)
				n++
			case Deleted:
				od, f.changed[Old] = appendLine(od, o, text), append(f.changed[Old], o)
				o++
			}
		}
		f.newSide, f.oldSide = append(f.newSide, nw), append(f.oldSide, od)
	}
	return f
}

func (f *File) headSegments() [][]line {
	f.headOnce.Do(func() {
		if f.head == nil {
			return
		}
		content, ok := f.head()
		f.headLines, f.headUnread = contentLines(content), !ok
	})
	if len(f.headLines) == 0 {
		return nil
	}
	return [][]line{f.headLines}
}

// tier is one place Resolve looks, in order: the hunks' new side, their old side, then
// the whole file at head.
type tier struct {
	status   Status
	side     Side
	segments func(*File) [][]line
}

var tiers = [...]tier{
	{ExactNew, New, func(f *File) [][]line { return f.newSide }},
	{ExactOld, Old, func(f *File) [][]line { return f.oldSide }},
	wholeFile,
}

// wholeFile is the last tier, and the only one Locate and Reanchor use.
var wholeFile = tier{InFile, New, (*File).headSegments}

// Resolve anchors a snippet in this file. The first tier holding it decides: one match
// anchors, several are ambiguous; a snippet no tier holds is not found, or head_unread
// when the head was not read.
func (f *File) Resolve(s Snippet) Anchor {
	if len(s.lines) == 0 {
		return Unplaced(f.Path, ReasonNoSnippet)
	}
	for _, t := range tiers {
		if first, _, n := find(t.segments(f), s, 0); n > 0 {
			return decide(f.pathOn(t.side), t, first, n)
		}
	}
	if f.headUnread {
		return Unplaced(f.Path, ReasonHeadUnread)
	}
	return Unplaced(f.Path, ReasonNotFound)
}

// pathOn is the file's path in side's version: its old path on the old side of a rename.
func (f *File) pathOn(side Side) string {
	if side == Old && f.OldPath != "" {
		return f.OldPath
	}
	return f.Path
}

// decide turns a tier's n > 0 matches into an anchor.
func decide(path string, t tier, first span, n int) Anchor {
	if n > 1 {
		return Anchor{Path: path, Status: Unanchored, Reason: ReasonAmbiguous, Candidates: n}
	}
	return Anchor{Path: path, StartLine: first.start, EndLine: first.end, Side: t.side, Status: t.status}
}

type span struct{ start, end int }

// find slides the snippet over each segment (a match never spans two) and returns the
// first match, the match starting at line prefer (zero when none does), and the match
// count, overlapping matches included. It is Knuth-Morris-Pratt over lines: a segment
// costs at most two line comparisons per line, however much of the snippet it repeats.
func find(segs [][]line, s Snippet, prefer int) (first, preferred span, n int) {
	want := s.lines
	if len(want) == 0 {
		return first, preferred, 0
	}
	for _, seg := range segs {
		j := 0 // snippet lines matched so far
		for i, l := range seg {
			for j > 0 && l.text != want[j] {
				j = s.fail[j-1]
			}
			if l.text == want[j] {
				j++
			}
			if j < len(want) {
				continue
			}
			sp := span{seg[i+1-len(want)].num, l.num}
			if n == 0 {
				first = sp
			}
			if sp.start == prefer {
				preferred = sp
			}
			n++
			j = s.fail[j-1]
		}
	}
	return first, preferred, n
}

// failure is want's Knuth-Morris-Pratt table: fail[k] is the length of the longest
// proper prefix of want[:k+1] that is also its suffix.
func failure(want []string) []int {
	fail := make([]int, len(want))
	for k, j := 1, 0; k < len(want); k++ {
		for j > 0 && want[k] != want[j] {
			j = fail[j-1]
		}
		if want[k] == want[j] {
			j++
		}
		fail[k] = j
	}
	return fail
}
