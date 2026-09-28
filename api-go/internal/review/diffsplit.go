package review

import (
	"strconv"
	"strings"
)

// Section is one file of a multi-file git diff.
type Section struct {
	OldPath string // "" for an added file
	NewPath string // "" for a deleted file
	Diff    string // the file's text, from its "diff --git" line
}

const gitHeader = "diff --git "

// SplitDiff splits git diff output (a/ and b/ prefixes, no renames) into files. A
// file's paths come from its --- and +++ lines, where /dev/null is the missing side, a
// C-quoted name is decoded and the tab git adds after a name with a space is dropped. A
// file without those lines (binary, mode or submodule only) takes its path from the
// diff --git line; one whose path cannot be read there is left out, since it has no
// hunk to anchor in either.
func SplitDiff(text string) []Section {
	var starts []int
	for off := 0; off < len(text); {
		if strings.HasPrefix(text[off:], gitHeader) {
			starts = append(starts, off)
		}
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			break
		}
		off += nl + 1
	}
	var out []Section
	for i, s := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if sec, ok := parseSection(text[s:end]); ok {
			out = append(out, sec)
		}
	}
	return out
}

// parseSection reads one file's header lines, up to its first hunk.
func parseSection(s string) (Section, bool) {
	first, rest, _ := strings.Cut(s, "\n")
	sec := Section{Diff: s}
	var minus, plus *string
	var added, deleted bool
	for l := range strings.SplitSeq(rest, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if strings.HasPrefix(l, "@@") {
			break
		}
		switch {
		case strings.HasPrefix(l, "--- "):
			minus = ptr(sideName(l[4:], "a/"))
		case strings.HasPrefix(l, "+++ "):
			plus = ptr(sideName(l[4:], "b/"))
		case strings.HasPrefix(l, "new file mode"):
			added = true
		case strings.HasPrefix(l, "deleted file mode"):
			deleted = true
		}
	}
	if minus != nil && plus != nil {
		sec.OldPath, sec.NewPath = *minus, *plus
		return sec, sec.OldPath != "" || sec.NewPath != ""
	}
	p := headerPath(strings.TrimSuffix(first, "\r"))
	if p == "" {
		return sec, false
	}
	sec.OldPath, sec.NewPath = p, p
	if added {
		sec.OldPath = ""
	}
	if deleted {
		sec.NewPath = ""
	}
	return sec, true
}

func ptr(s string) *string { return &s }

// sideName is the path on a --- or +++ line, "" for /dev/null or a name that does not
// decode.
func sideName(name, prefix string) string {
	name = strings.TrimSuffix(name, "\t")
	if name == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(name, `"`) {
		u, err := strconv.Unquote(name)
		if err != nil {
			return ""
		}
		name = u
	}
	return strings.TrimPrefix(name, prefix)
}

// headerPath reads the path of an unrenamed file from "diff --git a/P b/P", quoted or
// not; "" when it cannot.
func headerPath(line string) string {
	rest, ok := strings.CutPrefix(line, gitHeader)
	if !ok {
		return ""
	}
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return ""
		}
		u, _ := strconv.Unquote(q)
		return strings.TrimPrefix(u, "a/")
	}
	// Unquoted, both names are the same path, so the line is "a/" P " b/" P.
	n := (len(rest) - len("a/ b/")) / 2
	if n <= 0 || len(rest) != 2*n+len("a/ b/") || rest[:2] != "a/" || rest[2+n:2+n+3] != " b/" || rest[2:2+n] != rest[2+n+3:] {
		return ""
	}
	return rest[2 : 2+n]
}
