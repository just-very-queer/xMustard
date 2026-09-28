package evidence

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// List (xm-list/1: ls, tree, list_dir) and glob (xm-glob/1: find, fd, Glob) keep at
// most maxListEntries / maxGlobEntries entries in their original order, with exact
// totals and a summary of what was omitted: by extension for a listing, by top-level
// directory for a glob. Array sections (Claude Glob filenames) keep one entry per
// line so the shape adapter can rebuild the array.

const (
	maxListEntries   = 40
	maxGlobEntries   = 60
	maxGroupKeys     = 512
	maxListSummaries = 8 // tree summaries and tool notices kept
)

var treeSumRe = regexp.MustCompile(`^\d+ director(y|ies)(, \d+ files?)?$`)

// ListProjection is the structured list/glob projection.
type ListProjection struct {
	Kind    string      `json:"kind"` // list | glob
	Path    string      `json:"path,omitempty"`
	Total   int         `json:"total"`
	Dirs    int         `json:"dirs,omitempty"`
	Shown   int         `json:"shown"`
	Entries []string    `json:"entries"`
	Omitted []FileCount `json:"omitted_by_group,omitempty"` // extension or top directory → count
}

type listReducer struct{ glob bool }

func (r listReducer) Family() Family {
	if r.glob {
		return FamilyGlob
	}
	return FamilyList
}

func (r listReducer) ID() string {
	if r.glob {
		return "xm-glob"
	}
	return "xm-list"
}

func (listReducer) Version() int { return 1 }

// listEntry extracts the entry name of one listing line (nil for non-entries). The
// name is a view into line; nothing is allocated.
func listEntry(line []byte) (name []byte, dir, summary bool) {
	t := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
	switch {
	case len(t) == 0 || (bytes.HasPrefix(t, []byte("total ")) && numberAt(t, 6) == len(t)):
		return nil, false, false
	case treeSumRe.Match(t) || isNotice(t):
		return nil, false, true // tree summary, tool notices ("[500 entries limit reached]")
	}
	if n, ok := lsLongName(t); ok {
		return n, t[0] == 'd', false
	}
	// tree drawing: "├── name", "│   └── name"
	t = bytes.TrimLeft(t, "│├└─ \u00a0")
	return t, bytes.HasSuffix(t, []byte("/")), false
}

// lsLongName returns the name field of an `ls -l` line: after the mode and eight
// whitespace-separated fields (links, owner, group, size, month, day, time).
func lsLongName(t []byte) ([]byte, bool) {
	if len(t) < 11 || !bytes.ContainsRune([]byte("dlcbps-"), rune(t[0])) {
		return nil, false
	}
	for _, c := range t[1:10] {
		if !bytes.ContainsRune([]byte("rwxsStT-"), rune(c)) {
			return nil, false
		}
	}
	i := 10
	for f := 0; f < 7; f++ {
		for i < len(t) && t[i] != ' ' && t[i] != '\t' {
			i++ // the rest of the previous field (a trailing @ or + after the mode)
		}
		for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
			i++
		}
		if i >= len(t) {
			return nil, false
		}
	}
	for i < len(t) && t[i] != ' ' && t[i] != '\t' {
		i++
	}
	for i < len(t) && (t[i] == ' ' || t[i] == '\t') {
		i++
	}
	if i >= len(t) {
		return nil, false
	}
	return t[i:], true
}

func (r listReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	limit, kind := maxListEntries, "list"
	if r.glob {
		limit, kind = maxGlobEntries, "glob"
	}
	secs := nonEmpty(in.Sections)
	lp := &ListProjection{Kind: kind, Path: in.Sel.Path}
	groups := map[string]*int{}
	otherGroups := 0
	shown := 0
	var oms []Omission
	parts := map[string]string{}
	var body bytes.Buffer
	summaries := 0
	for _, sec := range secs {
		var part bytes.Buffer
		gap := newLineGap()
		err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
			name, dir, summary := listEntry(line)
			keep := false
			// a kept line is shown whole up to plainDisplay: charge that, not the name
			cost := min(len(displayLine(line)), plainDisplay) + 1
			switch {
			case summary:
				if summaries < maxListSummaries && body.Len()+part.Len()+cost < in.Target-512 {
					keep = true
					summaries++
				}
			case len(name) > 0:
				lp.Total++
				if dir {
					lp.Dirs++
				}
				if shown < limit && body.Len()+part.Len()+cost < in.Target-512 {
					keep = true
					shown++
					lp.Entries = append(lp.Entries, string(validUTF8(name)))
				} else {
					g := listGroup(name, dir, r.glob)
					if c := groups[string(g)]; c != nil {
						*c++ // lookup by a byte view: no allocation
					} else if len(groups) < maxGroupKeys {
						one := 1
						groups[string(g)] = &one
					} else {
						otherGroups++
					}
				}
			}
			if !keep {
				gap.skip(start)
				return nil
			}
			oms = gap.flush(oms, start)
			shownLine := displayLine(line)
			part.Write(validUTF8(trimRune(shownLine[:min(len(shownLine), plainDisplay)])))
			part.WriteByte('\n')
			return nil
		})
		if err != nil {
			return nil, err
		}
		oms = gap.flush(oms, sec.End)
		parts[sec.Name] = part.String()
		if len(secs) > 1 {
			fmt.Fprintf(&body, "[%s]\n", sec.Name)
		}
		body.Write(part.Bytes())
	}
	lp.Shown = shown
	for g, n := range groups {
		lp.Omitted = append(lp.Omitted, FileCount{Path: g, Matches: *n})
	}
	sort.Slice(lp.Omitted, func(i, j int) bool {
		if lp.Omitted[i].Matches != lp.Omitted[j].Matches {
			return lp.Omitted[i].Matches > lp.Omitted[j].Matches
		}
		return lp.Omitted[i].Path < lp.Omitted[j].Path
	})
	if len(lp.Omitted) > maxTopFiles {
		lp.Omitted = lp.Omitted[:maxTopFiles]
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "[xmustard %s] %s/1 %s entries=%d", kind, r.ID(), pathOr(in.Sel.Path), lp.Total)
	if !r.glob {
		fmt.Fprintf(&out, " dirs=%d", lp.Dirs)
	}
	fmt.Fprintf(&out, " shown=%d\n", shown)
	out.Write(body.Bytes())
	note := ""
	if shown < lp.Total {
		prefix := fmt.Sprintf("[xmustard: %d more entries not shown", lp.Total-shown)
		var items []string
		if len(lp.Omitted) > 0 {
			by := "extension"
			if r.glob {
				by = "directory"
			}
			prefix += "; by " + by + ":"
			for _, g := range lp.Omitted {
				items = append(items, fmt.Sprintf(" %s %d", g.Path, g.Matches))
			}
			if otherGroups > 0 {
				items = append(items, fmt.Sprintf(" other %d", otherGroups))
			}
		}
		start := out.Len()
		writeWithin(&out, in.Target, prefix, items, "]\n")
		note = out.String()[start:]
	}
	// the totals line leads the first (text) section and the closing note follows
	// it, once: a client payload rebuilt from many sections carries each only once
	header := out.String()[:strings.IndexByte(out.String(), '\n')+1]
	if len(secs) > 0 {
		if !secs[0].Array {
			parts[secs[0].Name] = header + parts[secs[0].Name]
		}
		parts[secs[0].Name] += note
	}
	fillParts(parts, in.Sections)
	facts := Facts{Entries: lp.Total, Shown: shown}
	facts.setToolExit(in.Sel.ExitCode)
	return &Projection{Text: out.String(), Parts: parts, Structured: lp, Facts: facts,
		Record: Record{Reducer: r.ID() + "/1", Mode: "text", Reduced: true, Omissions: capOmissions(oms)}}, nil
}

var (
	groupDir  = []byte("dir/")
	groupRoot = []byte("./")
	groupNone = []byte("(none)")
)

// listGroup names the group an omitted entry is summarized under (a view into
// name or a constant; nothing is allocated).
func listGroup(name []byte, dir, glob bool) []byte {
	if glob {
		clean := bytes.TrimPrefix(bytes.TrimPrefix(name, []byte("./")), []byte("/"))
		if i := bytes.IndexByte(clean, '/'); i >= 0 {
			return clean[:i+1]
		}
		return groupRoot
	}
	if dir {
		return groupDir
	}
	if i := bytes.LastIndexByte(name, '.'); i > 0 && len(name)-i <= 12 && bytes.IndexByte(name[i:], '/') < 0 {
		return name[i:]
	}
	return groupNone
}
