// SPDX-License-Identifier: MIT
// Copyright 2026 xMustard contributors

package anchor

import "slices"

// Locate anchors a snippet in a whole file (blank lines ignored): the quoted-code
// anchor of a memory (WS-27). One match anchors with status file on the new side;
// several are ambiguous and none is not found.
func Locate(path, content string, s Snippet) Anchor { return locate(path, content, s, 0) }

// Reanchor finds a snippet anchored at prev in its file's current content (WS-28
// refs_stale). One match anchors, wherever it moved. When several match, the one still
// starting at prev.StartLine keeps the anchor, since the quoted code has not moved;
// otherwise it is ambiguous. No match means the quoted code is gone.
func Reanchor(prev Anchor, content string, s Snippet) Anchor {
	return locate(prev.Path, content, s, prev.StartLine)
}

func locate(path, content string, s Snippet, prefer int) Anchor {
	if len(s.lines) == 0 {
		return Unplaced(path, ReasonNoSnippet)
	}
	first, preferred, n := find([][]line{contentLines(content)}, s.lines, prefer)
	if n > 1 && preferred.start > 0 {
		first, n = preferred, 1
	}
	if n == 0 {
		return Unplaced(path, ReasonNotFound)
	}
	return decide(path, wholeFile, first, n)
}

// Touches reports whether a's range holds a line the change touched on a's side: an
// added line on the new side, a deleted line on the old side (blank ones included).
// An anchor outside the change, or unanchored, touches nothing.
func (s *Set) Touches(a Anchor) bool {
	f := s.byPath[a.Path]
	if f == nil || a.StartLine <= 0 {
		return false
	}
	changed := f.changed[a.Side]
	i, _ := slices.BinarySearch(changed, a.StartLine)
	return i < len(changed) && changed[i] <= a.EndLine
}
