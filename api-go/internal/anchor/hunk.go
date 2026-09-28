// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors
// Copyright 2026 xMustard contributors
//
// Translated from open-code-review@486022d internal/diff/hunk.go and splitDiffLines in
// internal/diff/parser.go. Changes by xMustard contributors: package and type names; a
// hunk ends when its header's line counts are used up, so a trailing newline no longer
// adds an empty context line and text after the hunk is not read as part of it.

package anchor

import (
	"regexp"
	"strconv"
	"strings"
)

// LineKind is the kind of one line inside a hunk.
type LineKind uint8

const (
	Context LineKind = iota // ' ' prefix: unchanged
	Added                   // '+' prefix
	Deleted                 // '-' prefix
)

// HunkLine is one line of a hunk, without its marker.
type HunkLine struct {
	Kind    LineKind
	Content string
}

// Hunk is one @@ block of a unified diff.
type Hunk struct {
	OldStart, OldCount int // first line and line count in the old file (1-based)
	NewStart, NewCount int // first line and line count in the new file (1-based)
	Lines              []HunkLine
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseHunks parses the unified diff text of one file. Lines before the first @@ header
// (diff --git, index, ---, +++) are skipped, and parsing stops at the next file's
// "diff --git" line. Each line's trailing CR is dropped, so CRLF text parses like LF.
func ParseHunks(diff string) []Hunk {
	var hunks []Hunk
	var cur *Hunk
	oldLeft, newLeft := 0, 0
	for _, line := range splitDiffLines(diff) {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			hunks = append(hunks, Hunk{OldStart: atoi(m[1]), OldCount: count(m[2]), NewStart: atoi(m[3]), NewCount: count(m[4])})
			cur = &hunks[len(hunks)-1]
			oldLeft, newLeft = cur.OldCount, cur.NewCount
			continue
		}
		if cur == nil {
			continue // the file's header lines
		}
		if strings.HasPrefix(line, "diff --git ") {
			break
		}
		if (oldLeft <= 0 && newLeft <= 0) || strings.HasPrefix(line, `\`) {
			continue // text after a complete hunk, or "\ No newline at end of file"
		}
		hl := hunkLine(line)
		cur.Lines = append(cur.Lines, hl)
		if hl.Kind != Added {
			oldLeft--
		}
		if hl.Kind != Deleted {
			newLeft--
		}
	}
	return hunks
}

// hunkLine classifies one line inside a hunk. A line without a marker (an empty
// context line whose space was stripped, or anything else) is context, as upstream.
func hunkLine(line string) HunkLine {
	switch {
	case strings.HasPrefix(line, "+"):
		return HunkLine{Added, line[1:]}
	case strings.HasPrefix(line, "-"):
		return HunkLine{Deleted, line[1:]}
	case strings.HasPrefix(line, " "):
		return HunkLine{Context, line[1:]}
	}
	return HunkLine{Context, line}
}

// splitDiffLines splits on LF and drops each line's final CR (an interior CR stays).
func splitDiffLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// count is a header's optional line count, 1 when omitted.
func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}
