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

// groupState is the parser state: the current heading and the path of the last
// unnumbered match, kept in reused buffers.
type groupState struct{ heading, lastPath []byte }

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
	if isNotice(t) || bytes.HasPrefix(t, []byte("Found ")) || bytes.HasPrefix(t, []byte("No files found")) {
		return groupedItem{kind: ikSummary} // notices (Pi "[100 matches limit reached]")
	}
	// "path:text" without a line number (rg writing to a pipe, grep -r)
	if p, rest, ok := splitUnnumbered(t); ok {
		st.heading = st.heading[:0]
		st.lastPath = append(st.lastPath[:0], p...)
		return groupedItem{kind: ikMatch, path: p, text: rest}
	}
	// "path-text": unnumbered context of the file just matched
	if n := len(st.lastPath); n > 0 && len(t) > n && t[n] == '-' && bytes.HasPrefix(t, st.lastPath) {
		return groupedItem{kind: ikContext, path: t[:n]}
	}
	// a bare path: a heading when matches follow, else a files-with-matches entry
	st.heading = append(st.heading[:0], t...)
	return groupedItem{kind: ikFile, path: st.heading}
}

// noticeRe recognizes a tool's bracketed notice ("[100 matches limit reached. Use
// limit=200 for more]", "[50KB limit reached]"); other lines starting with '[' are
// output like any other.
var noticeRe = regexp.MustCompile(`(?i)^\[[^\[\]]*\b(limit reached|more (lines|entries|results|matches|files)|truncated|omitted|not shown|no (matches|results|files))\b[^\[\]]*\]$`)

func isNotice(t []byte) bool { return len(t) > 2 && len(t) <= 512 && t[0] == '[' && noticeRe.Match(t) }

// splitUnnumbered splits "path:text" when the part before the first ':' looks like
// a file path: no whitespace, a '/' or a '.', and not a URL scheme or a drive letter.
func splitUnnumbered(t []byte) (path, rest []byte, ok bool) {
	i := bytes.IndexByte(t, ':')
	if i < 2 || i > 4096 {
		return nil, nil, false
	}
	p := t[:i]
	if bytes.ContainsAny(p, " \t\"'<>|") || !bytes.ContainsAny(p, "/.") || bytes.HasPrefix(t[i+1:], []byte("//")) {
		return nil, nil, false
	}
	return p, t[i+1:], true
}

var (
	sevErrorRe   = regexp.MustCompile(`(?i)(^|[\s\[(:])(error|fatal|critical)\b|^\s*[EF]\d{2,4}\b|^\s*error\[`)
	sevWarnRe    = regexp.MustCompile(`(?i)(^|[\s\[(:])(warning|warn)\b|^\s*[WC]\d{2,4}\b`)
	lintSummary  = regexp.MustCompile(`(?i)(\d+ problems?|found \d+ (errors?|issues?)|\d+ errors?(,| and) \d+ warnings?|all checks passed|no issues found|success: no issues|\d+ issues?\.?$|\d+ files? checked)`)
	lintGoHeader = regexp.MustCompile(`^# \S+$`)
)

var lintRules = &groupedRules{family: FamilyLint, id: "xm-lint", parse: parseLintLine, sevName: true}

// containsFold reports whether b contains the lowercase ASCII word w, ignoring case,
// without allocating.
func containsFold(b []byte, w string) bool {
	for i := 0; i+len(w) <= len(b); i++ {
		j := 0
		for j < len(w) {
			c := b[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != w[j] {
				break
			}
			j++
		}
		if j == len(w) {
			return true
		}
	}
	return false
}

// maySummarize is a cheap guard for lintSummary.
func maySummarize(t []byte) bool {
	for _, w := range []string{"problem", "found", "error", "issue", "passed", "checked"} {
		if containsFold(t, w) {
			return true
		}
	}
	return false
}

// severity classifies a diagnostic's message.
func severity(rest []byte) int {
	switch {
	case (containsFold(rest, "error") || containsFold(rest, "fatal") || containsFold(rest, "critical") ||
		len(rest) > 1 && (rest[0] == 'E' || rest[0] == 'F')) && sevErrorRe.Match(rest):
		return 2
	case (containsFold(rest, "warn") || len(rest) > 1 && (rest[0] == 'W' || rest[0] == 'C')) && sevWarnRe.Match(rest):
		return 1
	}
	return 0
}

func parseLintLine(st *groupState, line []byte) groupedItem {
	t := bytes.TrimRight(line, "\r")
	trimmed := bytes.TrimSpace(t)
	if len(trimmed) == 0 {
		return groupedItem{kind: ikOther}
	}
	if len(st.heading) > 0 {
		if ln, word, ok := parseStylish(t); ok {
			sev := 1
			if string(word) == "error" {
				sev = 2
			}
			return groupedItem{kind: ikMatch, path: st.heading, line: ln, text: trimmed, sev: sev}
		}
	}
	// diagnostics first: they are most lines, and cheaper to recognize
	if bytes.ContainsAny(t, ":(") && !bytes.HasPrefix(trimmed, []byte("at ")) {
		if path, ln, rest, ok := parseDiag(t); ok && !bytes.ContainsAny(path, " \t") {
			st.heading = st.heading[:0]
			return groupedItem{kind: ikMatch, path: path, line: ln, text: bytes.TrimSpace(rest), sev: severity(rest)}
		}
	}
	switch {
	case maySummarize(t) && lintSummary.Match(t):
		return groupedItem{kind: ikSummary}
	case t[0] == '#' && lintGoHeader.Match(t):
		return groupedItem{kind: ikOther}
	}
	if !bytes.HasPrefix(t, []byte(" ")) && !bytes.HasPrefix(t, []byte("\t")) && !bytes.ContainsAny(t, " \t") {
		st.heading = append(st.heading[:0], t...) // eslint stylish file header
		return groupedItem{kind: ikHeading, path: st.heading}
	}
	return groupedItem{kind: ikOther}
}

// isRESpace is the \s class of Go's regexp syntax.
func isRESpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

// parseDiag matches `^\s*(\S[^:()]*?)(?::(\d+)(?::(\d+))?|\((\d+),(\d+)\)):?\s*(?:-\s*)?(.*)$`
// without allocating (regexp submatch indexes allocate per line, which made lint
// reduction allocate in proportion to its input): "path:line[:col][:] [- ]rest" or
// "path(line,col)[:] [- ]rest". ln is "line", "line:col" or "line,col".
// TestLintParsersMatchTheirPatterns checks the equivalence.
func parseDiag(t []byte) (path, ln, rest []byte, ok bool) {
	i := 0
	for i < len(t) && isRESpace(t[i]) {
		i++
	}
	if i >= len(t) {
		return nil, nil, nil, false
	}
	j := i + 1
	for j < len(t) && t[j] != ':' && t[j] != '(' && t[j] != ')' {
		j++
	}
	if j >= len(t) {
		return nil, nil, nil, false
	}
	path = t[i:j]
	pos := 0
	switch t[j] {
	case ':':
		k := numberAt(t, j+1)
		if k == j+1 {
			return nil, nil, nil, false
		}
		pos = k
		if k < len(t) && t[k] == ':' {
			if c := numberAt(t, k+1); c > k+1 {
				pos = c // line:col
			}
		}
		ln = t[j+1 : pos]
	case '(':
		a := numberAt(t, j+1)
		if a == j+1 || a >= len(t) || t[a] != ',' {
			return nil, nil, nil, false
		}
		b := numberAt(t, a+1)
		if b == a+1 || b >= len(t) || t[b] != ')' {
			return nil, nil, nil, false
		}
		ln, pos = t[j+1:b], b+1
	default:
		return nil, nil, nil, false
	}
	if pos < len(t) && t[pos] == ':' {
		pos++
	}
	for pos < len(t) && isRESpace(t[pos]) {
		pos++
	}
	if pos < len(t) && t[pos] == '-' {
		pos++
		for pos < len(t) && isRESpace(t[pos]) {
			pos++
		}
	}
	return path, ln, t[pos:], true
}

// parseStylish matches `^\s+(\d+):(\d+)\s+(error|warning|warn|info)\s+(.*)$`
// ("  12:5  error  message") without allocating; ln is "line:col" and word the
// severity word.
func parseStylish(t []byte) (ln, word []byte, ok bool) {
	i := 0
	for i < len(t) && isRESpace(t[i]) {
		i++
	}
	if i == 0 {
		return nil, nil, false
	}
	a := numberAt(t, i)
	if a == i || a >= len(t) || t[a] != ':' {
		return nil, nil, false
	}
	b := numberAt(t, a+1)
	if b == a+1 {
		return nil, nil, false
	}
	ln = t[i:b]
	s := b
	for s < len(t) && isRESpace(t[s]) {
		s++
	}
	if s == b {
		return nil, nil, false
	}
	for _, w := range []string{"error", "warning", "warn", "info"} {
		e := s + len(w)
		if e < len(t) && string(t[s:e]) == w && isRESpace(t[e]) {
			return ln, t[s:e], true
		}
	}
	return nil, nil, false
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
		// rules keep error lines failure-first instead, and the header and record
		// name them (xm-build/1)
		p, err = reduceLineSections(ctx, in, buildRules)
		if p != nil {
			p.Text = strings.Replace(p.Text, "[xmustard build] ", "[xmustard lint] ", 1)
			for k, v := range p.Parts {
				if strings.HasPrefix(v, "[xmustard build] ") {
					p.Parts[k] = strings.Replace(v, "[xmustard build] ", "[xmustard lint] ", 1)
				}
			}
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

// maxGroupedSummaries bounds the summary and notice lines a grouped projection keeps.
const maxGroupedSummaries = 8

func reduceGrouped(ctx context.Context, in *Input, rules *groupedRules) (*Projection, error) {
	secs := nonEmpty(in.Sections)
	c, err := countGrouped(ctx, in.R, secs, rules)
	if err != nil {
		return nil, err
	}
	if c.total == 0 {
		if rules.family == FamilyLint {
			return nil, errNoItems
		}
		return reduceLineSections(ctx, in, shellRules)
	}
	r := newGroupedRender(in, rules, c)
	for _, sec := range secs {
		if err := r.section(ctx, sec, len(secs) > 1); err != nil {
			return nil, err
		}
	}
	return r.finish(secs), nil
}

// groupedCounts are pass 1's per-file and total counts.
type groupedCounts struct {
	files       map[string]*fileStat
	untracked   int // files past maxTrackedFiles
	total       int
	errs, warns int
	headingMode bool // rg --heading: bare paths are headings, not entries
	// pending is a bare path not yet known to be a heading (reused buffer)
	pending     []byte
	havePending bool
}

func (c *groupedCounts) nFiles() int { return len(c.files) + c.untracked }

// countGrouped is pass 1: per-file and total counts.
func countGrouped(ctx context.Context, r io.ReaderAt, secs []Section, rules *groupedRules) (*groupedCounts, error) {
	c := &groupedCounts{files: map[string]*fileStat{}}
	for _, sec := range secs {
		st := &groupState{}
		c.havePending = false
		err := scanLines(ctx, r, sec, func(idx int, line []byte, start, end int64) error {
			c.observe(st, rules.parse(st, line))
			return nil
		})
		if err != nil {
			return nil, err
		}
		c.flushPending()
	}
	return c, nil
}

// observe counts one parsed item. A bare path stays pending until the next item shows
// what it was: the heading of the matches below it, or (followed by anything else) a
// files-with-matches entry of its own.
func (c *groupedCounts) observe(st *groupState, it groupedItem) {
	switch it.kind {
	case ikFile:
		c.flushPending()
		c.pending, c.havePending = append(c.pending[:0], it.path...), true
	case ikMatch:
		switch it.sev {
		case 2:
			c.errs++
		case 1:
			c.warns++
		}
		if len(st.heading) > 0 && bytes.Equal(it.path, st.heading) {
			c.headingMode = true
		}
		c.havePending = false
		c.register(it.path, 1, it.sev)
	case ikHeading:
		c.havePending = false
	case ikCount:
		c.register(it.path, it.count, it.sev)
	case ikContext:
	default:
		c.flushPending()
	}
}

// flushPending registers the pending bare path as a files-only entry.
func (c *groupedCounts) flushPending() {
	if c.havePending {
		registerFile(c.files, c.pending, 1, 0, &c.untracked)
		c.total++
		c.havePending = false
	}
}

func (c *groupedCounts) register(path []byte, n, sev int) {
	if n <= 0 {
		return
	}
	errs := 0
	if sev == 2 {
		errs = 1
	}
	registerFile(c.files, path, n, errs, &c.untracked)
	c.total += n
}

// omittedFiles are the files with nothing shown, largest first (then in order of
// appearance), for orientation.
func (c *groupedCounts) omittedFiles() []FileCount {
	var omitted []FileCount
	for p, fs := range c.files {
		if fs.shown == 0 {
			omitted = append(omitted, FileCount{Path: p, Matches: fs.matches})
		}
	}
	sort.Slice(omitted, func(i, j int) bool {
		if omitted[i].Matches != omitted[j].Matches {
			return omitted[i].Matches > omitted[j].Matches
		}
		return c.files[omitted[i].Path].order < c.files[omitted[j].Path].order
	})
	return omitted
}

// groupedRender is pass 2: it keeps at most perFile items per file and
// maxGroupedItems overall (lint keeps errors before warnings) and renders them grouped
// by file.
type groupedRender struct {
	in         *Input
	rules      *groupedRules
	c          *groupedCounts
	gp         *GroupedProjection
	header     string
	perFile    int
	warnBudget int
	kept       int
	keptWarn   int
	keptSums   int
	body       bytes.Buffer // the sections rendered so far (the header is written last)
	parts      map[string]string
	oms        []Omission

	// per section
	part    bytes.Buffer
	curPath string // copied only when a kept item starts a new file group
	gap     lineGap
}

func newGroupedRender(in *Input, rules *groupedRules, c *groupedCounts) *groupedRender {
	nFiles := c.nFiles()
	r := &groupedRender{in: in, rules: rules, c: c, parts: map[string]string{},
		perFile: max(minPerFile, maxGroupedItems/max(nFiles, 1)), warnBudget: maxGroupedItems - min(c.errs, maxGroupedItems)}
	r.gp = &GroupedProjection{Kind: "search", Matches: c.total, Files: nFiles, PerFileCap: r.perFile}
	if rules.family == FamilyLint {
		r.gp.Kind, r.gp.Errors, r.gp.Warnings = "lint", c.errs, c.warns
	}
	r.header = fmt.Sprintf("[xmustard %s] %s/1 matches=%d files=%d", rules.family, rules.id, c.total, nFiles)
	if rules.sevName {
		r.header = fmt.Sprintf("[xmustard %s] %s/1 diagnostics=%d errors=%d warnings=%d files=%d", rules.family, rules.id, c.total, c.errs, c.warns, nFiles)
	}
	return r
}

// used is what the projection holds so far; every kept item is checked with its own
// rendered size (and a file heading when it starts a new group).
func (r *groupedRender) used() int { return r.body.Len() + r.part.Len() }

// section renders one section.
func (r *groupedRender) section(ctx context.Context, sec Section, labeled bool) error {
	r.part = bytes.Buffer{}
	r.curPath, r.gap = "", newLineGap()
	st := &groupState{}
	var lastEnd int64
	err := scanLines(ctx, r.in.R, sec, func(idx int, line []byte, start, end int64) error {
		it := r.rules.parse(st, line)
		lastEnd = end
		if !r.admit(it, line, sec.Array) {
			r.gap.skip(start) // headings are re-rendered above their first kept match
			return nil
		}
		r.oms = r.gap.flush(r.oms, start)
		r.render(it, line, sec.Array)
		return nil
	})
	if err != nil {
		return err
	}
	r.oms = r.gap.flush(r.oms, min(lastEnd, sec.End))
	if !sec.Array {
		r.closeFile()
	}
	r.parts[sec.Name] = r.part.String()
	if labeled {
		fmt.Fprintf(&r.body, "[%s]\n", sec.Name)
	}
	r.body.Write(r.part.Bytes())
	return nil
}

// admit decides whether an item is kept: matches, counts and files-only entries
// within their file's cap, the item cap and the target; summary and notice lines up
// to maxGroupedSummaries. Under rg --heading a bare path is a heading, re-rendered
// above its first kept match.
func (r *groupedRender) admit(it groupedItem, line []byte, array bool) bool {
	switch it.kind {
	case ikFile:
		return !r.c.headingMode && r.admitItem(it, line, array)
	case ikMatch, ikCount:
		return r.admitItem(it, line, array)
	case ikSummary:
		if r.keptSums < maxGroupedSummaries && r.used()+min(len(line), plainDisplay)+1 < r.in.Target-512 {
			r.keptSums++
			return true
		}
	}
	return false
}

func (r *groupedRender) admitItem(it groupedItem, line []byte, array bool) bool {
	need := itemCost(it, line, array)
	if string(it.path) != r.curPath {
		need += min(len(it.path), groupedTextBytes) + 48 // heading and the previous group's "more" line
	}
	room := r.kept < maxGroupedItems && r.used()+need < r.in.Target-512
	if it.kind == ikMatch && it.sev < 2 && r.rules.family == FamilyLint {
		room = room && r.keptWarn < r.warnBudget
	}
	fs := r.c.files[string(it.path)]
	if fs == nil || fs.shown >= r.perFile || !room {
		return false
	}
	fs.shown++
	r.kept++
	if it.sev < 2 {
		r.keptWarn++
	}
	return true
}

// render writes a kept item, under a heading when it starts a new file group.
func (r *groupedRender) render(it groupedItem, line []byte, array bool) {
	if it.kind == ikSummary {
		r.closeFile()
		r.curPath = ""
		r.part.Write(validUTF8(trimRune(line[:min(len(line), plainDisplay)])))
		r.part.WriteByte('\n')
		return
	}
	if string(it.path) != r.curPath {
		r.closeFile()
		r.curPath = string(it.path)
		// array sections (Claude Grep filenames) render one entry per line
		if !array && it.kind == ikMatch {
			fmt.Fprintf(&r.part, "%s (%d)\n", shortPath(it.path), r.c.files[r.curPath].matches)
		}
	}
	gm := GroupedMatch{Path: string(shortPath([]byte(r.curPath))), Line: string(it.line)}
	switch {
	case array:
		r.part.Write(validUTF8(trimRune(line[:min(len(line), groupedTextBytes*2)])))
		r.part.WriteByte('\n')
	case it.kind == ikMatch:
		text := bytes.TrimSpace(it.text)
		short := trimRune(text[:min(len(text), groupedTextBytes)])
		gm.Text = string(validUTF8(short))
		if len(it.line) > 0 {
			fmt.Fprintf(&r.part, "  %s: %s", it.line, gm.Text)
		} else {
			fmt.Fprintf(&r.part, "  %s", gm.Text)
		}
		if len(short) < len(text) {
			fmt.Fprintf(&r.part, "…[+%d bytes]", len(text)-len(short))
		}
		r.part.WriteByte('\n')
	case it.kind == ikCount:
		fmt.Fprintf(&r.part, "%s: %d matches\n", shortPath(it.path), it.count)
	default:
		r.part.Write(shortPath(it.path))
		r.part.WriteByte('\n')
	}
	r.gp.Results = append(r.gp.Results, gm)
}

// closeFile ends the current file group with its "N more" line, once per file.
func (r *groupedRender) closeFile() {
	if r.curPath == "" {
		return
	}
	if fs := r.c.files[r.curPath]; fs != nil && fs.matches > fs.shown && !fs.lastSeen {
		fmt.Fprintf(&r.part, "  … %d more in %s\n", fs.matches-fs.shown, shortPath([]byte(r.curPath)))
		fs.lastSeen = true
	}
}

// finish writes the totals header and the closing "more not shown" summary.
func (r *groupedRender) finish(secs []Section) *Projection {
	c := r.c
	r.gp.Shown = r.kept
	headerLine := fmt.Sprintf("%s shown=%d per_file=%d\n", r.header, r.kept, r.perFile)
	for _, sec := range secs {
		if !sec.Array {
			r.parts[sec.Name] = headerLine + r.parts[sec.Name] // the first text section carries the totals
			break
		}
	}
	omitted := c.omittedFiles()
	r.gp.TopOmitted = omitted[:min(len(omitted), maxTopFiles)]
	var out bytes.Buffer
	out.WriteString(headerLine)
	out.Write(r.body.Bytes())
	if r.kept < c.total {
		prefix := fmt.Sprintf("[xmustard: %d more of %d not shown", c.total-r.kept, c.total)
		var items []string
		if len(omitted) > 0 {
			prefix += fmt.Sprintf("; %d files with none shown, largest:", len(omitted)+c.untracked)
			for _, fc := range r.gp.TopOmitted {
				items = append(items, fmt.Sprintf(" %s (%d)", fc.Path, fc.Matches))
			}
		}
		writeWithin(&out, r.in.Target, prefix, items, "; search the original with pattern=…]\n")
		// the note follows the array sections and the first text section
		note := fmt.Sprintf("[xmustard: %d more of %d not shown]\n", c.total-r.kept, c.total)
		for _, sec := range secs {
			r.parts[sec.Name] += note
			if !sec.Array {
				break
			}
		}
	}
	fillParts(r.parts, r.in.Sections)
	facts := Facts{Matches: c.total, Files: c.nFiles(), Shown: r.kept, Errors: c.errs, Warnings: c.warns}
	facts.setToolExit(r.in.Sel.ExitCode)
	return &Projection{Text: out.String(), Parts: r.parts, Structured: r.gp, Facts: facts,
		Record: Record{Reducer: r.rules.id + "/1", Mode: "text", Reduced: true, Omissions: capOmissions(r.oms)}}
}

// shortPath renders a path (or a bare output line taken for one) within
// groupedTextBytes, as valid UTF-8, with an explicit marker when cut.
func shortPath(p []byte) []byte {
	if len(p) <= groupedTextBytes {
		return validUTF8(p)
	}
	head := trimRune(p[:groupedTextBytes])
	out := make([]byte, 0, len(head)+24) // p is a view into the line buffer: never append to it
	out = append(out, validUTF8(head)...)
	return fmt.Appendf(out, "…[+%d bytes]", len(p)-len(head))
}

// itemCost bounds the rendered size of one kept item.
func itemCost(it groupedItem, line []byte, array bool) int {
	switch {
	case array:
		return min(len(line), groupedTextBytes*2) + 1
	case it.kind == ikMatch:
		return len(it.line) + min(len(bytes.TrimSpace(it.text)), groupedTextBytes) + 24
	}
	return min(len(it.path), groupedTextBytes) + 32
}

// writeWithin writes prefix, then as many items as keep out within limit bytes
// (leaving room for suffix), then suffix: closing summaries never push a projection
// past its target.
func writeWithin(out *bytes.Buffer, limit int, prefix string, items []string, suffix string) {
	out.WriteString(prefix)
	for i, it := range items {
		if out.Len()+len(it)+len(suffix)+4 > limit {
			if i < len(items) {
				out.WriteString(" …")
			}
			break
		}
		out.WriteString(it)
	}
	out.WriteString(suffix)
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
	defer lr.release()
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
