package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Grep (xm-grep/1) and lint (xm-lint/1) share a grouped engine: pass 1 counts
// matches per file (a bounded table) and in total; pass 2 keeps at most perFile
// matches per file and maxGroupedItems overall — lint keeps errors before warnings —
// and renders them grouped by file with exact totals and "N more" markers. Every
// omitted line is covered by a byte range, and search-in-original recovers any of them.

const (
	maxGroupedItems  = 40 // search ≤40 matches (PAR-CTX-02)
	minPerFile       = 5  // per-file cap when many files match
	maxTrackedFiles  = 4096
	groupedTextBytes = 240 // bytes shown of one matching line
	maxTopFiles      = 8
)

type itemKind uint8

const (
	ikOther itemKind = iota
	ikMatch
	ikContext
	ikHeading // a file heading (rg --heading, eslint stylish)
	ikFile    // a path-only entry (files-with-matches)
	ikCount   // path:count (grep -c)
	ikSummary
)

// groupedItem views the current line: path, line and text are valid until the
// next line is read.
type groupedItem struct {
	kind  itemKind
	path  []byte
	line  []byte
	text  []byte
	sev   int // 2 error, 1 warning, 0 other
	count int
}

type groupedRules struct {
	family  Family
	id      string
	parse   func(st *groupState, line []byte) groupedItem
	sevName bool // render severity counts (lint)
}

// groupState is the parser state (the current heading, kept in a reused buffer).
type groupState struct{ heading []byte }

var grepRules = &groupedRules{family: FamilyGrep, id: "xm-grep", parse: parseGrepLine}

// numberAt returns the end of a run of digits starting at i (i when none).
func numberAt(b []byte, i int) int {
	j := i
	for j < len(b) && b[j] >= '0' && b[j] <= '9' {
		j++
	}
	return j
}

// splitNumbered finds "path<sep>digits<sep>rest" (grep -n, rg, Pi "path:12: text"),
// skipping an optional ":col". It allocates nothing.
func splitNumbered(t []byte, sep byte) (path, line, rest []byte, ok bool) {
	for i := 1; i < len(t); i++ {
		if t[i] != sep {
			continue
		}
		j := numberAt(t, i+1)
		if j == i+1 || j >= len(t) || t[j] != sep {
			continue
		}
		rest = t[j+1:]
		if sep == ':' {
			if k := numberAt(rest, 0); k > 0 && k < len(rest) && rest[k] == ':' {
				rest = rest[k+1:] // path:line:col:text
			}
		}
		return t[:i], t[i+1 : j], rest, true
	}
	return nil, nil, nil, false
}

func parseGrepLine(st *groupState, line []byte) groupedItem {
	t := bytes.TrimRight(line, "\r")
	switch {
	case len(bytes.TrimSpace(t)) == 0 || bytes.Equal(t, []byte("--")):
		return groupedItem{kind: ikOther}
	case len(st.heading) > 0 && numberAt(t, 0) > 0:
		k := numberAt(t, 0)
		switch {
		case k < len(t) && t[k] == ':':
			rest := t[k+1:]
			if c := numberAt(rest, 0); c > 0 && c < len(rest) && rest[c] == ':' {
				rest = rest[c+1:]
			}
			return groupedItem{kind: ikMatch, path: st.heading, line: t[:k], text: rest}
		case k < len(t) && t[k] == '-':
			return groupedItem{kind: ikContext, path: st.heading}
		}
	}
	if p, ln, rest, ok := splitNumbered(t, ':'); ok {
		st.heading = st.heading[:0]
		return groupedItem{kind: ikMatch, path: p, line: ln, text: rest}
	}
	if i := bytes.LastIndexByte(t, ':'); i > 0 && i < len(t)-1 && numberAt(t, i+1) == len(t) {
		return groupedItem{kind: ikCount, path: t[:i], count: atoi(t[i+1:])}
	}
	if p, _, _, ok := splitNumbered(t, '-'); ok && (len(st.heading) == 0 || bytes.Equal(p, st.heading)) {
		return groupedItem{kind: ikContext, path: p}
	}
	if bytes.HasPrefix(t, []byte("[")) || bytes.HasPrefix(t, []byte("Found ")) || bytes.HasPrefix(t, []byte("No files found")) {
		return groupedItem{kind: ikSummary} // notices (Pi "[100 matches limit reached]")
	}
	// a bare path: a heading when matches follow, else a files-with-matches entry
	st.heading = append(st.heading[:0], t...)
	return groupedItem{kind: ikFile, path: st.heading}
}

var (
	diagRe       = regexp.MustCompile(`^\s*(\S[^:()]*?)(?::(\d+)(?::(\d+))?|\((\d+),(\d+)\)):?\s*(?:-\s*)?(.*)$`)
	stylishRe    = regexp.MustCompile(`^\s+(\d+):(\d+)\s+(error|warning|warn|info)\s+(.*)$`)
	sevErrorRe   = regexp.MustCompile(`(?i)(^|[\s\[(:])(error|fatal|critical)\b|^\s*[EF]\d{2,4}\b|^\s*error\[`)
	sevWarnRe    = regexp.MustCompile(`(?i)(^|[\s\[(:])(warning|warn)\b|^\s*[WC]\d{2,4}\b`)
	lintSummary  = regexp.MustCompile(`(?i)(\d+ problems?|found \d+ (errors?|issues?)|\d+ errors?(,| and) \d+ warnings?|all checks passed|no issues found|success: no issues|\d+ issues?\.?$|\d+ files? checked)`)
	lintGoHeader = regexp.MustCompile(`^# \S+$`)
)

var lintRules = &groupedRules{family: FamilyLint, id: "xm-lint", parse: parseLintLine, sevName: true}

func parseLintLine(st *groupState, line []byte) groupedItem {
	t := bytes.TrimRight(line, "\r")
	switch {
	case len(bytes.TrimSpace(t)) == 0:
		return groupedItem{kind: ikOther}
	case lintSummary.Match(t):
		return groupedItem{kind: ikSummary}
	case len(st.heading) > 0 && stylishRe.Match(t):
		m := stylishRe.FindSubmatchIndex(t)
		sev := 1
		if string(t[m[6]:m[7]]) == "error" {
			sev = 2
		}
		return groupedItem{kind: ikMatch, path: st.heading, line: t[m[2]:m[5]], text: bytes.TrimSpace(t), sev: sev}
	case lintGoHeader.Match(t):
		return groupedItem{kind: ikOther}
	case !bytes.HasPrefix(bytes.TrimSpace(t), []byte("at ")):
		m := diagRe.FindSubmatchIndex(t)
		if m == nil {
			break
		}
		path := t[m[2]:m[3]]
		var ln []byte
		switch {
		case m[4] >= 0 && m[6] >= 0:
			ln = t[m[4]:m[7]] // line:col
		case m[4] >= 0:
			ln = t[m[4]:m[5]]
		case m[8] >= 0:
			ln = t[m[8]:m[11]] // (line,col)
		}
		if len(ln) == 0 || bytes.ContainsAny(path, " \t") {
			break
		}
		rest := t[m[12]:m[13]]
		sev := 0
		switch {
		case sevErrorRe.Match(rest):
			sev = 2
		case sevWarnRe.Match(rest):
			sev = 1
		}
		st.heading = st.heading[:0]
		return groupedItem{kind: ikMatch, path: path, line: ln, text: bytes.TrimSpace(rest), sev: sev}
	}
	if !bytes.HasPrefix(t, []byte(" ")) && !bytes.HasPrefix(t, []byte("\t")) && !bytes.ContainsAny(t, " \t") {
		st.heading = append(st.heading[:0], t...) // eslint stylish file header
		return groupedItem{kind: ikHeading, path: st.heading}
	}
	return groupedItem{kind: ikOther}
}

type grepReducer struct{}

func (grepReducer) Family() Family { return FamilyGrep }
func (grepReducer) ID() string     { return "xm-grep" }
func (grepReducer) Version() int   { return 1 }
func (grepReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	return reduceGrouped(ctx, in, grepRules)
}

type lintReducer struct{}

func (lintReducer) Family() Family { return FamilyLint }
func (lintReducer) ID() string     { return "xm-lint" }
func (lintReducer) Version() int   { return 1 }
func (lintReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	p, err := reduceGrouped(ctx, in, lintRules)
	if err == errNoItems {
		// not a diagnostics list (compiler-style or tool-specific format): the build
		// rules keep error lines failure-first instead
		p, err = reduceLineSections(ctx, in, buildRules)
		if p != nil {
			p.Text = strings.Replace(p.Text, "[xmustard build] xm-build/1", "[xmustard lint] xm-lint/1 (build rules)", 1)
		}
	}
	return p, err
}

var errNoItems = fmt.Errorf("no grouped items")

type fileStat struct {
	order    int
	matches  int
	errors   int
	shown    int
	lastSeen bool
}

// GroupedProjection is the structured per-kind projection of grep and lint.
type GroupedProjection struct {
	Kind       string         `json:"kind"` // search | lint
	Matches    int            `json:"matches"`
	Files      int            `json:"files"`
	Shown      int            `json:"shown"`
	PerFileCap int            `json:"per_file_cap"`
	Errors     int            `json:"errors,omitempty"`
	Warnings   int            `json:"warnings,omitempty"`
	Results    []GroupedMatch `json:"results"`
	TopOmitted []FileCount    `json:"top_omitted_files,omitempty"`
}

type GroupedMatch struct {
	Path string `json:"path"`
	Line string `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

type FileCount struct {
	Path    string `json:"path"`
	Matches int    `json:"matches"`
}

func reduceGrouped(ctx context.Context, in *Input, rules *groupedRules) (*Projection, error) {
	secs := nonEmpty(in.Sections)
	files := map[string]*fileStat{}
	untracked := 0
	var total, errs, warns, summaries int
	headingMode := false
	// pass 1: per-file and total counts
	for _, sec := range secs {
		st := &groupState{}
		var prevFile []byte // a bare path not yet known to be a heading (reused buffer)
		havePrev := false
		err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
			it := rules.parse(st, line)
			count := 0
			switch it.kind {
			case ikMatch:
				count = 1
				if it.sev == 2 {
					errs++
				} else if it.sev == 1 {
					warns++
				}
			case ikCount:
				count = it.count
			case ikFile:
				if havePrev {
					// the previous bare path had no matches below it: a files-only entry
					registerFile(files, prevFile, 1, 0, &untracked)
					total++
				}
				prevFile, havePrev = append(prevFile[:0], it.path...), true
				return nil
			case ikSummary:
				summaries++
			}
			if it.kind != ikMatch && it.kind != ikCount && it.kind != ikHeading && havePrev && it.kind != ikContext {
				// the previous bare path had no matches below it: a files-only entry
				registerFile(files, prevFile, 1, 0, &untracked)
				total++
				havePrev = false
			}
			if it.kind == ikMatch || it.kind == ikHeading {
				if it.kind == ikMatch && len(st.heading) > 0 && bytes.Equal(it.path, st.heading) {
					headingMode = true // rg --heading: bare paths are headings, not entries
				}
				havePrev = false
			}
			if count > 0 {
				e := 0
				if it.sev == 2 {
					e = 1
				}
				registerFile(files, it.path, count, e, &untracked)
				total += count
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if havePrev {
			registerFile(files, prevFile, 1, 0, &untracked)
			total++
		}
	}
	if total == 0 {
		if rules.family == FamilyLint {
			return nil, errNoItems
		}
		return reduceLineSections(ctx, in, shellRules)
	}
	nFiles := len(files) + untracked
	perFile := max(minPerFile, maxGroupedItems/max(nFiles, 1))
	warnBudget := maxGroupedItems - min(errs, maxGroupedItems)
	gp := &GroupedProjection{Kind: "search", Matches: total, Files: nFiles, PerFileCap: perFile}
	if rules.family == FamilyLint {
		gp.Kind, gp.Errors, gp.Warnings = "lint", errs, warns
	}
	// pass 2: select and render
	var out bytes.Buffer
	header := fmt.Sprintf("[xmustard %s] %s/1 matches=%d files=%d", rules.family, rules.id, total, nFiles)
	if rules.sevName {
		header = fmt.Sprintf("[xmustard %s] %s/1 diagnostics=%d errors=%d warnings=%d files=%d", rules.family, rules.id, total, errs, warns, nFiles)
	}
	parts := map[string]string{}
	var oms []Omission
	kept, keptWarn := 0, 0
	var body bytes.Buffer
	for _, sec := range secs {
		var part bytes.Buffer
		st := &groupState{}
		curPath := "" // copied only when a kept item starts a new file group
		gapStart, gapLines := int64(-1), 0
		closeFile := func() {
			if curPath == "" {
				return
			}
			if fs := files[curPath]; fs != nil && fs.matches > fs.shown && !fs.lastSeen {
				fmt.Fprintf(&part, "  … %d more in %s\n", fs.matches-fs.shown, curPath)
				fs.lastSeen = true
			}
		}
		var lastEnd int64
		err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
			it := rules.parse(st, line)
			lastEnd = end
			keep := false
			switch it.kind {
			case ikFile:
				if headingMode {
					break // a heading: re-rendered above its first kept match
				}
				fallthrough
			case ikMatch, ikCount:
				fs := files[string(it.path)]
				room := kept < maxGroupedItems && out.Len()+body.Len()+part.Len() < in.Target-512
				if it.kind == ikMatch && it.sev < 2 && rules.family == FamilyLint {
					room = room && keptWarn < warnBudget
				}
				if fs != nil && fs.shown < perFile && room {
					keep = true
					fs.shown++
					kept++
					if it.sev < 2 {
						keptWarn++
					}
				}
			case ikSummary:
				keep = true
			}
			if !keep {
				// headings are re-rendered above their first kept match
				if gapStart < 0 {
					gapStart = start
				}
				gapLines++
				return nil
			}
			if gapStart >= 0 {
				oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: start, Items: gapLines})
				gapStart, gapLines = -1, 0
			}
			switch it.kind {
			case ikSummary:
				closeFile()
				curPath = ""
				part.Write(validUTF8(trimRune(line[:min(len(line), plainDisplay)])))
				part.WriteByte('\n')
				return nil
			}
			if string(it.path) != curPath {
				closeFile()
				curPath = string(it.path)
				// array sections (Claude Grep filenames) render one entry per line
				if !sec.Array && it.kind == ikMatch {
					fmt.Fprintf(&part, "%s (%d)\n", validUTF8(it.path), files[curPath].matches)
				}
			}
			gm := GroupedMatch{Path: curPath, Line: string(it.line)}
			switch {
			case sec.Array:
				part.Write(validUTF8(line[:min(len(line), groupedTextBytes*2)]))
				part.WriteByte('\n')
			case it.kind == ikMatch:
				text := bytes.TrimSpace(it.text)
				short := trimRune(text[:min(len(text), groupedTextBytes)])
				gm.Text = string(validUTF8(short))
				fmt.Fprintf(&part, "  %s: %s", it.line, gm.Text)
				if len(short) < len(text) {
					fmt.Fprintf(&part, "…[+%d bytes]", len(text)-len(short))
				}
				part.WriteByte('\n')
			case it.kind == ikCount:
				fmt.Fprintf(&part, "%s: %d matches\n", it.path, it.count)
			default:
				part.Write(validUTF8(it.path))
				part.WriteByte('\n')
			}
			gp.Results = append(gp.Results, gm)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if gapStart >= 0 {
			oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: min(lastEnd, sec.End), Items: gapLines})
		}
		if !sec.Array {
			closeFile()
		}
		parts[sec.Name] = part.String()
		if len(secs) > 1 {
			fmt.Fprintf(&body, "[%s]\n", sec.Name)
		}
		body.Write(part.Bytes())
	}
	gp.Shown = kept
	headerLine := fmt.Sprintf("%s shown=%d per_file=%d\n", header, kept, perFile)
	for _, sec := range secs {
		if !sec.Array {
			parts[sec.Name] = headerLine + parts[sec.Name] // the first text section carries the totals
			break
		}
	}
	// the biggest files with nothing shown, for orientation
	var omitted []FileCount
	for p, fs := range files {
		if fs.shown == 0 {
			omitted = append(omitted, FileCount{Path: p, Matches: fs.matches})
		}
	}
	sort.Slice(omitted, func(i, j int) bool {
		if omitted[i].Matches != omitted[j].Matches {
			return omitted[i].Matches > omitted[j].Matches
		}
		return files[omitted[i].Path].order < files[omitted[j].Path].order
	})
	if len(omitted) > maxTopFiles {
		gp.TopOmitted = omitted[:maxTopFiles]
	} else {
		gp.TopOmitted = omitted
	}
	out.WriteString(headerLine)
	out.Write(body.Bytes())
	if kept < total {
		fmt.Fprintf(&out, "[xmustard: %d more of %d not shown", total-kept, total)
		if len(omitted) > 0 {
			fmt.Fprintf(&out, "; %d files with none shown, largest:", len(omitted)+untracked)
			for _, fc := range gp.TopOmitted {
				fmt.Fprintf(&out, " %s (%d)", fc.Path, fc.Matches)
			}
		}
		out.WriteString("; search the original with pattern=…]\n")
		for _, sec := range secs {
			if sec.Array {
				parts[sec.Name] += fmt.Sprintf("[xmustard: %d more of %d not shown]\n", total-kept, total)
			} else {
				parts[sec.Name] += fmt.Sprintf("[xmustard: %d more of %d not shown]\n", total-kept, total)
				break
			}
		}
	}
	for _, s := range in.Sections {
		if _, ok := parts[s.Name]; !ok {
			parts[s.Name] = ""
		}
	}
	facts := Facts{ExitCode: in.Sel.ExitCode, Matches: total, Files: nFiles, Shown: kept, Errors: errs, Warnings: warns}
	if facts.ExitCode != nil {
		facts.ExitFrom = "tool"
	}
	return &Projection{Text: out.String(), Parts: parts, Structured: gp, Facts: facts,
		Record: Record{Mode: "text", Reduced: true, Omissions: capOmissions(oms)}}, nil
}

func registerFile(files map[string]*fileStat, path []byte, n, errs int, untracked *int) {
	fs := files[string(path)] // no allocation for the lookup
	if fs == nil {
		if len(files) >= maxTrackedFiles {
			*untracked++
			return
		}
		fs = &fileStat{order: len(files)}
		files[string(path)] = fs
	}
	fs.matches += n
	fs.errors += errs
}

// scanLines calls fn for every line of a section (without its newline; at most
// maxSalienceLine bytes are passed) with the line's absolute start offset.
func scanLines(ctx context.Context, r io.ReaderAt, sec Section, fn func(idx int, line []byte, start, end int64) error) error {
	lr := newLineReader(r, sec)
	off := sec.Start
	for idx := 0; ; idx++ {
		if idx&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		raw, full, err := lr.next(maxSalienceLine)
		if len(raw) == 0 && err != nil {
			return nil
		}
		line := bytes.TrimSuffix(raw, []byte("\n"))
		if ferr := fn(idx, line, off, off+full); ferr != nil {
			return ferr
		}
		off += full
		if err != nil {
			return nil
		}
	}
}
