package evidence

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// The line engine behind the shell, build, log, git and test families. It is
// failure-first and streaming: pass 1 reads the original once, classifying every
// line (failure evidence, summary, passing/progress noise, repeats, stack frames) and
// keeping only line indices and costs for what it may emit; pass 2 reads it again
// and writes exactly the selected lines. Memory is O(window) whatever the size:
// bounded head/tail/recent rings, a mandatory set capped by the budget, and one line
// buffer of at most maxSalienceLine bytes.
//
// Budget split (after Letta Code's head+tail middle truncation, 30% + 30%; an
// independent implementation): failure blocks first, up to 70% of the target, in
// document order; then the tail up to 30%, the head up to 30%, and any remainder to
// the tail and head again. Passing tests and progress lines are counted, never shown.

type lineClass uint8

const (
	lcPlain    lineClass = iota
	lcCollapse           // passing/progress noise: counted, never shown
	lcSalient            // failure evidence: kept with context
	lcSummary            // summary lines: always kept
	lcBoundary           // a new test starts: resets the per-test lookback (collapsed)
)

// lineRules adapts the engine to one family.
type lineRules struct {
	family  Family
	id      string
	version int
	// classify returns the class of a line (trailing newline and CR progress
	// prefix removed); it may update facts and state.
	classify func(st *lineState, line []byte) lineClass
	// normalize makes repeat detection ignore numbers and ids (logs).
	normalize bool
	// lookback keeps the lines of the current test (since the last boundary) so a
	// failure marker printed after them (go test) still keeps its assertion lines.
	lookback bool
	// header renders family facts for the projection's first line.
	header func(f *Facts) string
}

type lineState struct {
	facts *Facts
	src   lineSrc // reused per line: a pointer in an interface does not allocate
	// test family: lines still kept of the current failure report block
	block    int
	lastExit *int
}

type lineReducer struct{ rules *lineRules }

func (r lineReducer) Family() Family { return r.rules.family }
func (r lineReducer) ID() string     { return r.rules.id }
func (r lineReducer) Version() int   { return r.rules.version }

func (r lineReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	return reduceLineSections(ctx, in, r.rules)
}

const (
	lineContextBefore = 2
	lineContextAfter  = 2
	plainDisplay      = 1 << 10 // bytes shown of a head/tail/context line
	salientDisplay    = 2 << 10 // bytes shown around failure evidence in a long line
	repeatKeyBytes    = 4 << 10 // a line's first bytes identify repeats
	maxRingLines      = 4096
	maxSummaryLines   = 64
	maxLookback       = 8
	maxBlockLines     = 24
	maxFailingNames   = 20
)

// frame patterns: stack frames in the common runtimes' traces.
var (
	strongFrame = regexp.MustCompile(`^(\s+at \S|\s*File ".*", line \d+|\s+\S+\.(go|rs|ts|tsx|js|jsx|mjs|cjs|py|rb|java|kt|cs|cpp|cc|c|h|php|swift|scala|ex|exs|dart)(:\d+)+|\s+\d+: \S|#\d+\s+0x[0-9a-fA-F]+ |\s*from \S+:\d+:in |\S+\.rb:\d+:in |\s+\.\.\. \d+ more|goroutine \d+ \[)`)
	goFuncFrame = regexp.MustCompile(`^(created by )?[A-Za-z_][\w./*()\[\]{}-]*\(.*\)( in goroutine \d+)?$`)
	exitText    = regexp.MustCompile(`(?i)(?:\bexit(?:ed with)?(?: code| status)[: ]\s*(-?\d+)|process (?:finished|exited|completed) with (?:exit )?code (-?\d+)|returned non-zero exit status (\d+)|\*\*\* \[[^\]]*\] error (\d+)|command failed with exit code (\d+))`)
)

func isFrame(line []byte, inRun bool) bool {
	if len(line) == 0 {
		return false
	}
	// every strong frame form starts indented, with '#', "File", "from" or
	// "goroutine", or is a Ruby "x.rb:N:in" line: a cheap guard before the pattern
	c := line[0]
	if (c == ' ' || c == '\t' || c == '#' || c == 'g' || c == 'F' || c == 'f' || bytes.Contains(line, []byte(".rb:"))) && strongFrame.Match(line) {
		return true
	}
	return inRun && goFuncFrame.Match(line)
}

// frameContinuation: a Python source line (or caret marker) under a frame line.
func frameContinuation(line []byte) bool {
	return bytes.HasPrefix(line, []byte("    ")) && len(bytes.TrimSpace(line)) > 0
}

// lineSrc is a byteSource over one line.
type lineSrc struct{ b []byte }

func (l *lineSrc) at(i int64) (byte, bool) {
	if i < 0 || i >= int64(len(l.b)) {
		return 0, false
	}
	return l.b[i], true
}

func (l *lineSrc) read(start, end int64) []byte { return l.b[start:end] }

// salient reports failure evidence in one line without allocating.
func (st *lineState) salient(line []byte) (bool, error) {
	st.src.b = line
	hits, err := salientHits(&st.src, 0, line, 1)
	return len(hits) > 0, err
}

// lineReader reads lines through one reusable buffer: the returned slice is valid
// until the next call, so reading allocates nothing per line. Readers are pooled:
// every pass over every section reuses one 64 KiB read buffer instead of allocating
// its own, so a many-section original costs no more than a one-section one.
type lineReader struct {
	sr  io.SectionReader
	br  *bufio.Reader
	buf []byte
}

var lineReaders = sync.Pool{New: func() any {
	lr := &lineReader{}
	lr.br = bufio.NewReaderSize(&lr.sr, 64<<10)
	return lr
}}

// newLineReader returns a pooled reader over one section; release returns it.
func newLineReader(r io.ReaderAt, sec Section) *lineReader {
	lr := lineReaders.Get().(*lineReader)
	lr.sr = *io.NewSectionReader(r, sec.Start, sec.End-sec.Start)
	lr.br.Reset(&lr.sr)
	return lr
}

func (lr *lineReader) release() {
	lr.sr = io.SectionReader{}
	if cap(lr.buf) > 64<<10 {
		lr.buf = nil // a long line's buffer is not kept past this pass
	}
	lr.buf = lr.buf[:0]
	lineReaders.Put(lr)
}

// next returns the next line including its newline (at most keep bytes of it), the
// line's full length, and any read error (io.EOF after the last line).
func (lr *lineReader) next(keep int) ([]byte, int64, error) {
	lr.buf = lr.buf[:0]
	var full int64
	for {
		chunk, err := lr.br.ReadSlice('\n')
		full += int64(len(chunk))
		if room := keep - len(lr.buf); room > 0 {
			lr.buf = append(lr.buf, chunk[:min(room, len(chunk))]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return lr.buf, full, err
	}
}

// candidate is one line the engine may emit.
type candidate struct{ idx, cost int }

// gapMarkerCost bounds one "[xmustard: N lines omitted, bytes a-b]" marker.
const gapMarkerCost = 64

// sectionPlan is pass 1's decision for one section.
type sectionPlan struct {
	all     bool         // the section fits its share: emitted whole
	keep    map[int]bool // selected line indices
	salient map[int]bool // emitted around their failure evidence
	lines   int
	used    int
	budget  int
}

// reduceLineSections reduces every section with its share of the budget and joins
// them: the family header first, then each non-empty section (labeled when there are
// several), each with explicit omission markers carrying absolute byte ranges. Each
// section is planned and emitted before the next is read, so one plan is alive at a
// time; the labels are charged against the budget before it is shared.
func reduceLineSections(ctx context.Context, in *Input, rules *lineRules) (*Projection, error) {
	var facts Facts
	facts.setToolExit(in.Sel.ExitCode)
	secs := nonEmpty(in.Sections)
	shares := sectionShares(secs, in.Target-512-labelCost(secs))
	var body bytes.Buffer
	parts := map[string]string{}
	var oms []Omission
	firstLen := 0
	for i, s := range secs {
		p, err := planLines(ctx, in.R, s, shares[i], rules, &facts)
		if err != nil {
			return nil, err
		}
		if len(secs) > 1 {
			fmt.Fprintf(&body, "[%s]\n", s.Name)
		}
		start := body.Len()
		o, err := emitLines(ctx, in.R, s, p, rules, &body, in.Max)
		if err != nil {
			return nil, err
		}
		oms = append(oms, o...)
		if i == 0 {
			firstLen = body.Len() - start
		} else {
			parts[s.Name] = body.String()[start:]
		}
		if body.Len() > in.Max {
			return nil, fmt.Errorf("%w: %s projection exceeds %d bytes", ErrUnsupported, rules.id, in.Max)
		}
	}
	finishFacts(&facts)
	header := familyHeader(rules, &facts)
	if len(secs) > 0 {
		// the first section's part carries the header (the totals)
		first := body.Bytes()
		if len(secs) > 1 {
			first = first[len(secs[0].Name)+3:]
		}
		parts[secs[0].Name] = header + string(first[:firstLen])
	}
	fillParts(parts, in.Sections)
	return &Projection{Text: header + body.String(), Parts: parts, Facts: facts,
		Record: Record{Reducer: rules.id + "/" + strconv.Itoa(rules.version), Mode: "text", Reduced: true, Omissions: capOmissions(oms)}}, nil
}

// labelCost is what the "[name]" section labels of a multi-section projection cost.
func labelCost(secs []Section) int {
	if len(secs) < 2 {
		return 0
	}
	n := 0
	for _, s := range secs {
		n += len(s.Name) + 3
	}
	return n
}

func nonEmpty(secs []Section) []Section {
	var out []Section
	for _, s := range secs {
		if s.End > s.Start {
			out = append(out, s)
		}
	}
	return out
}

// sectionShares splits a budget over sections max-min fairly (water-filling): in
// ascending size, each section gets all it needs or an equal share of what is left,
// so a small stderr is kept whole, large sections split the rest, and the shares
// never add up to more than the budget.
func sectionShares(secs []Section, budget int) []int {
	budget = max(budget, 1024)
	order := make([]int, len(secs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		return secs[order[a]].End-secs[order[a]].Start < secs[order[b]].End-secs[order[b]].Start
	})
	shares := make([]int, len(secs))
	left := budget
	for k, i := range order {
		fair := left / (len(order) - k)
		shares[i] = int(min(secs[i].End-secs[i].Start, int64(fair)))
		left -= shares[i]
	}
	return shares
}

// capOmissions bounds the omission list (256 entries); the rest are merged into the
// last entry so every omitted byte stays covered by a listed range.
func capOmissions(oms []Omission) []Omission {
	const limit = 256
	if len(oms) <= limit {
		return oms
	}
	last := oms[limit-1]
	for _, o := range oms[limit:] {
		last.End = max(last.End, o.End)
		last.Items += o.Items
	}
	last.Kind = "lines"
	out := append([]Omission(nil), oms[:limit-1]...)
	return append(out, last)
}

// lineGap collects a run of consecutive omitted lines into one "lines" omission.
type lineGap struct {
	start int64 // -1: no run is open
	lines int
}

func newLineGap() lineGap { return lineGap{start: -1} }

// skip adds the line starting at start to the open run, opening one if needed.
func (g *lineGap) skip(start int64) {
	if g.start < 0 {
		g.start = start
	}
	g.lines++
}

// take closes the open run at end and returns its omission; ok is false when no run
// was open.
func (g *lineGap) take(end int64) (o Omission, ok bool) {
	if g.start < 0 {
		return Omission{}, false
	}
	o = Omission{Kind: "lines", Start: g.start, End: end, Items: g.lines}
	g.start, g.lines = -1, 0
	return o, true
}

// flush closes the open run at end, appending its omission to oms.
func (g *lineGap) flush(oms []Omission, end int64) []Omission {
	if o, ok := g.take(end); ok {
		return append(oms, o)
	}
	return oms
}

// fillParts gives every section without a projection part an empty one.
func fillParts(parts map[string]string, secs []Section) {
	for _, s := range secs {
		if _, ok := parts[s.Name]; !ok {
			parts[s.Name] = ""
		}
	}
}

// setToolExit records the exit code the client reported for the tool, if any.
func (f *Facts) setToolExit(code *int) {
	f.ExitCode = code
	if code != nil {
		f.ExitFrom = "tool"
	}
}

// planLines is pass 1 over one section.
func planLines(ctx context.Context, r io.ReaderAt, sec Section, budget int, rules *lineRules, facts *Facts) (*sectionPlan, error) {
	lp := &linePlanner{
		p:     &sectionPlan{keep: map[int]bool{}, salient: map[int]bool{}, budget: budget},
		rules: rules, facts: facts, st: &lineState{facts: facts},
		failCap: budget * 7 / 10, tail: make([]candidate, 0, 64), lastSalient: -100,
	}
	lr := newLineReader(r, sec)
	defer lr.release()
	for idx := 0; ; idx++ {
		if idx&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		raw, full, err := lr.next(maxSalienceLine)
		if len(raw) == 0 && err != nil {
			break
		}
		if full > maxSalienceLine {
			return nil, fmt.Errorf("%w: text line of %d bytes exceeds the %d-byte failure-detection limit", ErrUnsupported, full, maxSalienceLine)
		}
		if perr := lp.line(idx, displayLine(raw), full); perr != nil {
			return nil, perr
		}
		if err != nil {
			break
		}
	}
	if lp.run.in && lp.run.attached {
		lp.keepRun()
	}
	if facts.ExitCode == nil && lp.st.lastExit != nil {
		facts.ExitCode, facts.ExitFrom = lp.st.lastExit, "text"
	}
	if sec.End-sec.Start <= int64(budget) {
		lp.p.all = true // the whole section fits its share: nothing is omitted
		return lp.p, nil
	}
	lp.fillHeadAndTail()
	return lp.p, nil
}

// linePlanner is the state of pass 1 over one section: the selection so far, the
// head, tail and context rings, and the stack-trace run being read.
type linePlanner struct {
	p       *sectionPlan
	rules   *lineRules
	facts   *Facts
	st      *lineState
	failCap int // failure evidence may use up to 70% of the budget

	head      []candidate // the first plain lines, up to 30% of the budget
	headUsed  int
	tail      []candidate // ring of the newest plain lines
	tailPos   int
	recent    []candidate // the last lineContextBefore plain lines
	look      []candidate // lookback: the newest lines of the current test
	blockHead []candidate // lookback: the first lines of the current test
	after     int         // context lines still owed after failure evidence
	summaries int         // summary lines kept

	run         frameRun
	lastSalient int
	prevKey     uint64
	havePrev    bool
}

// frameRun is a stack-trace run: its first and last frame groups are kept when the
// run is attached to failure evidence; the frames between them are omitted.
type frameRun struct {
	in, attached, firstStarted bool
	first, last                []candidate
}

func plainCost(n int64) int   { return int(min(n, plainDisplay+48)) + 1 }
func salientCost(n int64) int { return int(min(n, salientDisplay+plainDisplay/2+96)) + 1 }

// add selects a line. Each run of kept lines costs about one gap marker: a line that
// joins no run opens one, a line that joins two runs closes one.
func (lp *linePlanner) add(idx, c int) bool {
	p := lp.p
	if p.keep[idx] {
		return false
	}
	switch prev, next := p.keep[idx-1], p.keep[idx+1]; {
	case !prev && !next:
		c += gapMarkerCost
	case prev && next:
		c -= gapMarkerCost
	}
	p.keep[idx] = true
	p.used += c
	return true
}

func (lp *linePlanner) addAll(cands []candidate) {
	for _, c := range cands {
		lp.add(c.idx, c.cost)
	}
}

// line classifies one line (failure evidence, summary, passing/progress noise,
// repeats, stack frames) and keeps what it may emit.
func (lp *linePlanner) line(idx int, line []byte, full int64) error {
	lp.p.lines++
	lp.facts.Lines++
	key := lineKey(line, lp.rules.normalize)
	repeat := lp.havePrev && key == lp.prevKey && len(bytes.TrimSpace(line)) > 0
	lp.prevKey, lp.havePrev = key, true
	if lp.facts.ExitCode == nil && mayCarryExit(line) {
		if m := exitText.FindSubmatch(line); m != nil {
			lp.st.lastExit = exitFrom(m)
		}
	}
	class := lp.rules.classify(lp.st, line)
	// stack frames are judged before generic failure keywords, so a frame such as
	// "panic(...)" or "at Assert.assertEquals" is a frame, not a failure line
	frame := class != lcSummary && class != lcSalient && isFrame(line, lp.run.in)
	cont := !frame && lp.run.in && class == lcPlain && frameContinuation(line)
	if !frame && !cont && class == lcPlain {
		ok, err := lp.st.salient(line)
		if err != nil {
			return err
		}
		if ok {
			class = lcSalient
		}
	}
	if repeat && class != lcSummary {
		lp.facts.Repeats++
		class = lcCollapse
	}
	c := plainCost(full)
	lp.trackFrames(candidate{idx, c}, line, frame, cont, class)
	lp.keepByClass(idx, full, class)
	if class == lcPlain {
		lp.plainLine(candidate{idx, c}, frame || cont)
	}
	return nil
}

// trackFrames follows the stack-trace run: a frame opens it (attached when failure
// evidence was just seen), a frame-group start begins the last group, frames and
// continuation lines extend the current group, and any other line ends it (a trace
// followed by its exception line, as Python prints it, is attached).
func (lp *linePlanner) trackFrames(cand candidate, line []byte, frame, cont bool, class lineClass) {
	r := &lp.run
	switch {
	case frame && !r.in:
		r.in, r.attached = true, cand.idx-lp.lastSalient <= lineContextBefore+3
		r.firstStarted = !goroutineHeader(line)
		r.first, r.last = []candidate{cand}, nil
	case frame && isFrameStart(line) && !r.firstStarted:
		r.first = append(r.first, cand)
		r.firstStarted = true
	case frame && isFrameStart(line):
		r.last = []candidate{cand}
	case (frame || cont) && r.last == nil:
		if len(r.first) < 5 {
			r.first = append(r.first, cand)
		}
	case frame || cont:
		if len(r.last) < 3 {
			r.last = append(r.last, cand)
		}
	case r.in:
		r.in = false
		if r.attached || class == lcSalient {
			lp.keepRun()
		}
	}
}

func (lp *linePlanner) keepRun() {
	if lp.p.used >= lp.failCap {
		return
	}
	lp.addAll(lp.run.first)
	lp.addAll(lp.run.last)
}

// keepByClass counts a line by its class and keeps summaries and failure evidence.
func (lp *linePlanner) keepByClass(idx int, full int64, class lineClass) {
	switch class {
	case lcBoundary:
		lp.look, lp.blockHead = lp.look[:0], lp.blockHead[:0]
		lp.st.block = 0
		lp.facts.Collapsed++
	case lcCollapse:
		lp.facts.Collapsed++
	case lcSummary:
		if lp.summaries < maxSummaryLines {
			lp.add(idx, plainCost(full))
			lp.summaries++
		}
	case lcSalient:
		lp.keepFailure(idx, full)
	}
}

// keepFailure keeps failure evidence with the context before it (and, for test
// runners, the lines of the current test) up to the failure share; beyond it the line
// is counted and searchable in the original.
func (lp *linePlanner) keepFailure(idx int, full int64) {
	lp.facts.FailureLines++
	if lp.p.used+salientCost(full) > lp.failCap && lp.p.used > 0 {
		return
	}
	lp.add(idx, salientCost(full))
	lp.p.salient[idx] = true
	lp.facts.FailuresShown++
	lp.lastSalient = idx
	lp.addAll(lp.recent)
	if lp.rules.lookback {
		lp.addAll(lp.blockHead)
		lp.addAll(lp.look)
		lp.look, lp.blockHead = lp.look[:0], lp.blockHead[:0]
	}
	lp.after = lineContextAfter
}

// plainLine keeps a plain line owed as failure context (outside a trace) and
// remembers it as a head, tail, context or lookback candidate.
func (lp *linePlanner) plainLine(cand candidate, inTrace bool) {
	if !inTrace {
		switch {
		case lp.after > 0:
			lp.add(cand.idx, cand.cost)
			lp.after--
		case lp.st.block > 0 && lp.p.used < lp.failCap:
			lp.add(cand.idx, cand.cost) // inside a failure report block (test family)
			lp.st.block--
		}
	}
	if len(lp.head) < maxRingLines && lp.headUsed+cand.cost <= lp.p.budget*3/10 {
		lp.head = append(lp.head, cand)
		lp.headUsed += cand.cost
	}
	if len(lp.tail) < maxRingLines {
		lp.tail = append(lp.tail, cand)
	} else {
		lp.tail[lp.tailPos] = cand
		lp.tailPos = (lp.tailPos + 1) % maxRingLines
	}
	if inTrace {
		return
	}
	lp.recent = pushRing(lp.recent, cand, lineContextBefore)
	if lp.rules.lookback {
		if len(lp.blockHead) < maxLookback/2 {
			lp.blockHead = append(lp.blockHead, cand)
		} else {
			lp.look = pushRing(lp.look, cand, maxLookback)
		}
	}
}

// fillHeadAndTail spends what is left of the budget: the tail (newest lines first) up
// to 30%, the head (in order) up to 30%, then any remainder to the tail and the head.
func (lp *linePlanner) fillHeadAndTail() {
	newest := make([]candidate, 0, len(lp.tail))
	for k := len(lp.tail) - 1; k >= 0; k-- {
		newest = append(newest, lp.tail[(lp.tailPos+k)%len(lp.tail)])
	}
	share := lp.p.budget * 3 / 10
	lp.fill(newest, lp.p.used+share)
	lp.fill(lp.head, lp.p.used+share)
	lp.fill(newest, lp.p.budget)
	lp.fill(lp.head, lp.p.budget)
}

// fill keeps candidates in order until the next one would pass limit.
func (lp *linePlanner) fill(cands []candidate, limit int) {
	for _, c := range cands {
		if lp.p.keep[c.idx] {
			continue
		}
		if lp.p.used+c.cost+gapMarkerCost > limit {
			return
		}
		lp.add(c.idx, c.cost)
	}
}

// pushRing appends to a small ring kept in place (no reallocation per line).
func pushRing(ring []candidate, c candidate, size int) []candidate {
	if len(ring) < size {
		return append(ring, c)
	}
	copy(ring, ring[1:])
	ring[len(ring)-1] = c
	return ring
}

func goroutineHeader(line []byte) bool { return bytes.HasPrefix(line, []byte("goroutine ")) }

// mayCarryExit is a cheap prefilter for exitText.
func mayCarryExit(line []byte) bool {
	return bytes.Contains(line, []byte("xit")) || bytes.Contains(line, []byte("rror")) || bytes.Contains(line, []byte("ode "))
}

func isFrameStart(line []byte) bool {
	// a Go file:line line follows its function line; both form one frame group
	return !(bytes.HasPrefix(line, []byte("\t")) && bytes.Contains(line, []byte(".go:")))
}

// displayLine strips the line terminator and any carriage-return progress prefix.
func displayLine(raw []byte) []byte {
	line := bytes.TrimRight(raw, "\n")
	line = bytes.TrimSuffix(line, []byte("\r"))
	if i := bytes.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	return line
}

// lineKey hashes a line's first repeatKeyBytes (FNV-1a, no allocation). With
// normalize, runs of digits and long hex strings (timestamps, ids, counters) hash
// as one placeholder, so log lines differing only in them count as repeats.
func lineKey(line []byte, normalize bool) uint64 {
	const offset64, prime64 = 14695981039346656037, 1099511628211
	h := uint64(offset64)
	line = line[:min(len(line), repeatKeyBytes)]
	for i := 0; i < len(line); i++ {
		c := line[i]
		if normalize && isHexDigit(c) {
			j := i
			digits := true
			for j < len(line) && isHexDigit(line[j]) {
				digits = digits && line[j] >= '0' && line[j] <= '9'
				j++
			}
			if digits || j-i >= 8 {
				h ^= '#'
				h *= prime64
				i = j - 1
				continue
			}
		}
		h ^= uint64(c)
		h *= prime64
	}
	return h
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func exitFrom(m [][]byte) *int {
	for _, g := range m[1:] {
		if len(g) > 0 {
			if v, err := strconv.Atoi(string(g)); err == nil {
				return &v
			}
		}
	}
	return nil
}

// emitLines is pass 2: the selected lines in input order with explicit gap markers.
func emitLines(ctx context.Context, r io.ReaderAt, sec Section, p *sectionPlan, rules *lineRules, out *bytes.Buffer, hardMax int) ([]Omission, error) {
	var oms []Omission
	lr := newLineReader(r, sec)
	defer lr.release()
	off := sec.Start
	gapStart, gapLines, gapRepeats := int64(-1), 0, 0
	var lastKey uint64
	flush := func(end int64) {
		if gapStart < 0 {
			return
		}
		if gapRepeats == gapLines {
			fmt.Fprintf(out, "[xmustard: previous line repeated %d more times, bytes %d-%d]\n", gapLines, gapStart, end)
		} else {
			fmt.Fprintf(out, "[xmustard: %d lines omitted, bytes %d-%d]\n", gapLines, gapStart, end)
		}
		oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: end, Items: gapLines})
		gapStart, gapLines, gapRepeats = -1, 0, 0
	}
	if p.all {
		// the section fits its share: copied whole (invalid UTF-8 is replaced; the
		// exact bytes stay recoverable through the handle)
		buf := make([]byte, 32<<10)
		for o := sec.Start; o < sec.End; {
			m, err := r.ReadAt(buf[:min(int64(len(buf)), sec.End-o)], o)
			if m == 0 {
				return nil, ErrCorrupt
			}
			if err != nil && err != io.EOF {
				return nil, err
			}
			chunk := trimTo(buf[:m], o+int64(m) < sec.End)
			if len(chunk) == 0 {
				chunk = buf[:m] // a lone invalid byte run: replaced below
			}
			out.Write(validUTF8(chunk))
			o += int64(len(chunk))
		}
		if b := out.Bytes(); len(b) > 0 && b[len(b)-1] != '\n' {
			out.WriteByte('\n')
		}
		return nil, nil
	}
	for idx := 0; idx < p.lines; idx++ {
		if idx&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		keep := p.keep[idx]
		limit := repeatKeyBytes // enough to recognize repeats and show a plain line
		if keep && p.salient[idx] {
			limit = maxSalienceLine
		}
		raw, full, _ := lr.next(limit)
		start := off
		off += full
		line := displayLine(raw)
		key := lineKey(line, rules.normalize)
		if !keep {
			if gapStart < 0 {
				gapStart = start
			}
			gapLines++
			if key == lastKey {
				gapRepeats++
			}
			continue
		}
		flush(start)
		lastKey = key
		emitted, cut := excerptLine(line, full, p.salient[idx])
		out.Write(emitted)
		out.WriteByte('\n')
		if cut > 0 {
			oms = append(oms, Omission{Kind: "line_tail", Start: start, End: start + full})
		}
		if out.Len() > hardMax {
			return nil, fmt.Errorf("%w: %s projection exceeds %d bytes", ErrUnsupported, rules.id, hardMax)
		}
	}
	flush(sec.End)
	return oms, nil
}

// excerptLine shows a line whole when short, else its head (and for failure lines a
// window around the first failure evidence) with explicit byte-count markers. The
// result is always valid UTF-8.
func excerptLine(line []byte, full int64, salient bool) ([]byte, int) {
	if len(line) <= plainDisplay && int64(len(line)) >= full-2 {
		return validUTF8(line), 0
	}
	if !salient || len(line) <= salientDisplay {
		n := min(len(line), plainDisplay)
		head := trimRune(line[:n])
		omitted := len(line) - len(head)
		if omitted <= 0 {
			return validUTF8(line), 0
		}
		return append(validUTF8(head), []byte(fmt.Sprintf("…[xmustard: %d bytes omitted]", omitted))...), omitted
	}
	hits, _ := salientHits(sliceSource(line), 0, line, 1)
	at := 0
	if len(hits) > 0 {
		at = hits[0]
	}
	headN := plainDisplay / 2
	if at < headN+64 {
		n := min(len(line), salientDisplay)
		head := trimRune(line[:n])
		omitted := len(line) - len(head)
		return append(validUTF8(head), []byte(fmt.Sprintf("…[xmustard: %d bytes omitted]", omitted))...), omitted
	}
	winStart := max(headN, at-256)
	winEnd := min(len(line), winStart+salientDisplay-headN)
	var b bytes.Buffer
	b.Write(validUTF8(trimRune(line[:headN])))
	fmt.Fprintf(&b, "…[xmustard: %d bytes omitted]", winStart-headN)
	b.Write(validUTF8(trimRuneStart(trimRune(line[winStart:winEnd]))))
	if winEnd < len(line) {
		fmt.Fprintf(&b, "…[xmustard: %d bytes omitted]", len(line)-winEnd)
	}
	return b.Bytes(), len(line) - (winEnd - winStart) - headN
}

// trimTo drops a trailing partial UTF-8 sequence when more bytes follow.
func trimTo(b []byte, more bool) []byte {
	if !more {
		return b
	}
	for k := 1; k <= utf8.UTFMax && k <= len(b); k++ {
		if utf8.RuneStart(b[len(b)-k]) {
			if !utf8.FullRune(b[len(b)-k:]) {
				return b[:len(b)-k]
			}
			break
		}
	}
	return b
}

func trimRune(b []byte) []byte {
	for k := 0; k < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); k++ {
		b = b[:len(b)-1]
	}
	return b
}

func trimRuneStart(b []byte) []byte {
	for k := 0; k < utf8.UTFMax && len(b) > 0 && !utf8.RuneStart(b[0]); k++ {
		b = b[1:]
	}
	return b
}

func validUTF8(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	return []byte(strings.ToValidUTF8(string(b), "�"))
}

// finishFacts prefers counts a runner printed itself over per-line tallies.
func finishFacts(f *Facts) {
	switch {
	case f.sum != nil && f.sum.seen:
		f.Passed, f.Failed, f.Skipped, f.CountsFrom = f.sum.passed, f.sum.failed, f.sum.skipped, "summary"
	case f.Passed > 0 || f.Failed > 0 || f.Skipped > 0:
		f.CountsFrom = "lines"
	}
}

// familyHeader is the projection's first line: the family, the exit code when known
// and the counts pass 1 extracted from the whole original.
func familyHeader(rules *lineRules, f *Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[xmustard %s] %s/%d", rules.family, rules.id, rules.version)
	if f.ExitCode != nil {
		fmt.Fprintf(&b, " exit=%d", *f.ExitCode)
	}
	fmt.Fprintf(&b, " lines=%d", f.Lines)
	if rules.header != nil {
		b.WriteString(rules.header(f))
	}
	if f.FailureLines > 0 {
		fmt.Fprintf(&b, " failure_lines=%d", f.FailureLines)
		if f.FailuresShown < f.FailureLines {
			fmt.Fprintf(&b, " (shown %d; search the original for the rest)", f.FailuresShown)
		}
	}
	if f.Collapsed > 0 {
		fmt.Fprintf(&b, " collapsed=%d", f.Collapsed)
	}
	b.WriteByte('\n')
	return b.String()
}

// --- shell, build, log and git rules ---

var progressLine = regexp.MustCompile(`(?i)^\s*(\[?\s*\d{1,3}(\.\d+)?%\]?|[#=>\-.\s]{0,4}\[[#=>\-\s.]+\]|downloading|downloaded|fetching|fetched|resolving|resolved|compiling|compiled|checking|building|installing|installed|unpacking|extracting|collecting|uploading|progress|receiving objects|resolving deltas|counting objects|compressing objects|writing objects|remote: (counting|compressing|enumerating|total))\b`)

// progress reports a progress/noise line that carries no failure evidence: only
// those collapse (a "Checking ... error" line is never hidden as progress).
func (st *lineState) progress(line []byte) bool {
	if !progressLine.Match(line) {
		return false
	}
	ok, err := st.salient(line)
	return err == nil && !ok
}

var shellRules = &lineRules{
	family: FamilyShell, id: "xm-shell", version: 1,
	classify: func(st *lineState, line []byte) lineClass {
		if st.progress(line) {
			return lcCollapse
		}
		return lcPlain
	},
}

var (
	buildErrorLine = regexp.MustCompile(`(?i)(\berror(\[[a-z]?\d+\])?:|: error |\bfatal\b|undefined (reference|symbol)|cannot find|no such file|failed to compile|build failed|compilation failed|\*\*\* \[)`)
	buildWarnLine  = regexp.MustCompile(`(?i)\bwarning(\[[a-z]?\d+\])?:`)
	buildSummary   = regexp.MustCompile(`(?i)^(\s*(finished|built|compiled successfully|build succeeded|build failed)|.*\b\d+ (errors?|warnings?) generated|error: could not compile|found \d+ errors?|\d+ error\(s\), \d+ warning\(s\)|BUILD (SUCCESSFUL|FAILED))`)
)

var buildRules = &lineRules{
	family: FamilyBuild, id: "xm-build", version: 1,
	classify: func(st *lineState, line []byte) lineClass {
		switch {
		case buildErrorLine.Match(line):
			st.facts.Errors++
			return lcSalient
		case buildWarnLine.Match(line):
			st.facts.Warnings++
			return lcPlain
		case buildSummary.Match(line):
			return lcSummary
		case st.progress(line):
			return lcCollapse
		}
		return lcPlain
	},
	header: func(f *Facts) string {
		return fmt.Sprintf(" errors=%d warnings=%d", f.Errors, f.Warnings)
	},
}

var (
	logLevelError = regexp.MustCompile(`(?i)\b(error|err|fatal|crit(ical)?|panic|emerg|alert|severe)\b`)
	logLevelWarn  = regexp.MustCompile(`(?i)\b(warn|warning)\b`)
)

var logRules = &lineRules{
	family: FamilyLog, id: "xm-log", version: 1,
	classify: func(st *lineState, line []byte) lineClass {
		switch {
		case logLevelError.Match(line):
			st.facts.Errors++
			return lcSalient
		case logLevelWarn.Match(line):
			st.facts.Warnings++
		}
		return lcPlain
	},
	// timestamps, ids and numbers vary between otherwise identical log lines
	normalize: true,
	header: func(f *Facts) string {
		return fmt.Sprintf(" errors=%d warnings=%d repeats=%d", f.Errors, f.Warnings, f.Repeats)
	},
}

var gitRules = &lineRules{
	family: FamilyGit, id: "xm-git", version: 1,
	classify: func(st *lineState, line []byte) lineClass {
		switch {
		case bytes.HasPrefix(line, []byte("fatal:")) || bytes.HasPrefix(line, []byte("error:")) || bytes.Contains(line, []byte("CONFLICT")):
			return lcSalient
		case bytes.HasPrefix(line, []byte("On branch ")) || bytes.HasPrefix(line, []byte("Your branch ")) ||
			bytes.HasPrefix(line, []byte("HEAD detached")) || bytes.HasPrefix(line, []byte("nothing to commit")):
			return lcSummary
		case st.progress(line):
			return lcCollapse
		}
		return lcPlain
	},
}
