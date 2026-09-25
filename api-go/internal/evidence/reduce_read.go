package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
)

// Read family (xm-read/1): a file read is projected as a line-numbered range. The
// projection keeps a contiguous numbered excerpt from the start of the requested
// range (70% of the budget), then the declarations after it — an outline of what was
// omitted (20%) — and the last lines of the range (10%), with explicit "lines A-B
// omitted" markers. Line numbers are absolute: the capture's first line is
// Selector.StartLine (recorded as FamilyRecord.FirstLine, which search-in-original
// applies), and a later section continues the original's line count. Shape adapters
// that number lines themselves (Claude Read) get the contiguous excerpt alone,
// unnumbered, in Parts, with long lines cut exactly as in the numbered projection.

const (
	readExcerptShare = 70
	readOutlineShare = 20
	maxOutlineLines  = 200
	readNumberWidth  = 8 // "%6d\t" plus newline
	readGapMarker    = 40
)

var declLine = regexp.MustCompile(`^((export\s+)?(default\s+)?(pub(\([^)]*\))?\s+)?(async\s+)?(func|fn|def|class|struct|enum|interface|trait|impl|type|module|package|namespace|function|abstract|public|private|protected|static|const|var|let|macro_rules!|#{1,4} )\b|\s{2,4}(async def|def|fn|func|pub fn|pub\(crate\) fn|public|private|protected|static|override)\b)`)

// ReadProjection is the structured read projection {path, line_count, excerpt}.
type ReadProjection struct {
	Kind      string     `json:"kind"` // read
	Path      string     `json:"path,omitempty"`
	LineCount int        `json:"line_count"`
	FirstLine int        `json:"first_line"`
	LastLine  int        `json:"last_line"`
	Requested *LineRange `json:"requested,omitempty"`
	Excerpt   LineRange  `json:"excerpt"`
	Outline   int        `json:"outline_lines,omitempty"`
	TailFrom  int        `json:"tail_from,omitempty"`
	Omitted   int        `json:"omitted_lines"`
}

// LineRange is an inclusive range of absolute line numbers.
type LineRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

type readReducer struct{}

func (readReducer) Family() Family { return FamilyRead }
func (readReducer) ID() string     { return "xm-read" }
func (readReducer) Version() int   { return 1 }

func (readReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	secs := nonEmpty(in.Sections)
	shares := sectionShares(secs, in.Target-512)
	origin := max(in.Sel.StartLine, 1)
	var out bytes.Buffer
	parts := map[string]string{}
	var oms []Omission
	var rp *ReadProjection
	facts := Facts{ExitCode: in.Sel.ExitCode}
	var body bytes.Buffer
	// newlines before the current section: its first line's number in the original
	newlines, at := 0, int64(0)
	for i, sec := range secs {
		gap, err := countNewlines(ctx, in.R, at, sec.Start)
		if err != nil {
			return nil, err
		}
		newlines += gap
		p, raw, o, nl, err := readSection(ctx, in, sec, shares[i], origin+newlines, &body)
		if err != nil {
			return nil, err
		}
		newlines, at = newlines+nl, sec.End
		oms = append(oms, o...)
		parts[sec.Name] = raw
		facts.Lines += p.LineCount
		facts.Shown += p.Excerpt.To - p.Excerpt.From + 1 + p.Outline
		if rp == nil {
			rp = p
		}
	}
	if rp == nil {
		rp = &ReadProjection{Kind: "read", Path: in.Sel.Path}
	}
	fmt.Fprintf(&out, "[xmustard read] xm-read/1 %s lines %d-%d (%d lines): excerpt %d-%d", pathOr(in.Sel.Path),
		rp.FirstLine, rp.LastLine, rp.LineCount, rp.Excerpt.From, rp.Excerpt.To)
	if rp.Outline > 0 {
		fmt.Fprintf(&out, ", outline %d lines", rp.Outline)
	}
	if rp.TailFrom > 0 {
		fmt.Fprintf(&out, ", tail %d-%d", rp.TailFrom, rp.LastLine)
	}
	out.WriteByte('\n')
	out.Write(body.Bytes())
	for _, s := range in.Sections {
		if _, ok := parts[s.Name]; !ok {
			parts[s.Name] = ""
		}
	}
	return &Projection{Text: out.String(), Parts: parts, Structured: rp, Facts: facts,
		Record: Record{Reducer: "xm-read/1", Mode: "text", Reduced: true, Omissions: capOmissions(oms)}}, nil
}

func pathOr(p string) string {
	if p == "" {
		return "(unnamed)"
	}
	return p
}

// countNewlines counts the line feeds in r[from:to) (bytes between sections, or a
// section's last byte).
func countNewlines(ctx context.Context, r io.ReaderAt, from, to int64) (int, error) {
	n := 0
	var buf [4096]byte
	for off := from; off < to; {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		m, err := r.ReadAt(buf[:min(int64(len(buf)), to-off)], off)
		if m == 0 {
			if err != nil && err != io.EOF {
				return 0, err
			}
			return 0, ErrCorrupt
		}
		n += bytes.Count(buf[:m], []byte{'\n'})
		off += int64(m)
	}
	return n, nil
}

// readSection plans and emits one section; it returns the structured projection,
// the unnumbered contiguous excerpt (with a trailing marker), the omissions and the
// section's line feed count.
func readSection(ctx context.Context, in *Input, sec Section, budget, origin int, out *bytes.Buffer) (*ReadProjection, string, []Omission, int, error) {
	from, to := in.Sel.FromLine, in.Sel.ToLine
	inRange := func(ln int) bool { return (from <= 0 || ln >= from) && (to <= 0 || ln <= to) }
	cost := func(n int) int { return min(n, plainDisplay) + readNumberWidth + 1 }
	excerptCap, outlineCap := budget*readExcerptShare/100, budget*readOutlineShare/100
	excerptFrom, excerptTo, excerptUsed, excerptOpen := 0, 0, 0, true
	topDecls, anyDecls := 0, 0
	var tail []candidate
	tailPos := 0
	lines, first, last := 0, 0, 0
	// pass 1: the excerpt, declaration counts after it, and a ring of the last lines
	err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
		lines++
		ln := origin + idx
		if !inRange(ln) {
			return nil
		}
		if first == 0 {
			first = ln
		}
		last = ln
		c := cost(len(line))
		if excerptOpen && excerptUsed+c <= excerptCap {
			if excerptFrom == 0 {
				excerptFrom = ln
			}
			excerptTo, excerptUsed = ln, excerptUsed+c
			return nil
		}
		excerptOpen = false
		if declLine.Match(line) {
			anyDecls++
			if line[0] != ' ' && line[0] != '\t' {
				topDecls++
			}
		}
		if len(tail) < maxRingLines {
			tail = append(tail, candidate{idx, c})
		} else {
			tail[tailPos] = candidate{idx, c}
			tailPos = (tailPos + 1) % maxRingLines
		}
		return nil
	})
	if err != nil {
		return nil, "", nil, 0, err
	}
	// the outline samples declarations evenly over the omitted middle: top-level ones
	// when there are any, else all; each costs about one line plus a gap marker
	slots := min(maxOutlineLines, outlineCap/(64+readGapMarker))
	topOnly := topDecls > 0
	decls := anyDecls
	if topOnly {
		decls = topDecls
	}
	stride := 1
	if slots > 0 && decls > slots {
		stride = (decls + slots - 1) / slots
	}
	// the tail: the newest lines within what the excerpt and outline leave
	ordered := append(append([]candidate(nil), tail[tailPos:]...), tail[:tailPos]...)
	used := excerptUsed + min(outlineCap, min(decls, slots)*(64+readGapMarker)) + readGapMarker
	tailFrom := 0
	for k := len(ordered) - 1; k >= 0; k-- {
		if used+ordered[k].cost > budget {
			break
		}
		used += ordered[k].cost
		tailFrom = origin + ordered[k].idx
	}
	rp := &ReadProjection{Kind: "read", Path: in.Sel.Path, LineCount: lines, FirstLine: first, LastLine: last,
		Excerpt: LineRange{excerptFrom, excerptTo}, TailFrom: tailFrom}
	if from > 0 || to > 0 {
		rp.Requested = &LineRange{from, to}
	}
	// pass 2
	var raw bytes.Buffer
	var oms []Omission
	gapFrom, gapStart, gapLines := 0, int64(-1), 0
	flush := func(end int64, nextLine int) {
		if gapStart < 0 {
			return
		}
		fmt.Fprintf(out, "[xmustard: lines %d-%d omitted]\n", gapFrom, nextLine-1)
		oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: end, Items: gapLines})
		rp.Omitted += gapLines
		gapStart, gapLines = -1, 0
	}
	seenDecls := 0
	err = scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
		ln := origin + idx
		keep := false
		switch {
		case !inRange(ln):
		case ln >= excerptFrom && ln <= excerptTo:
			keep = true
		case tailFrom > 0 && ln >= tailFrom:
			keep = true
		case rp.Outline < slots && declLine.Match(line) && (!topOnly || (line[0] != ' ' && line[0] != '\t')):
			if seenDecls%stride == 0 {
				keep = true
				rp.Outline++
			}
			seenDecls++
		}
		if !keep {
			if gapStart < 0 {
				gapStart, gapFrom = start, ln
			}
			gapLines++
			return nil
		}
		flush(start, ln)
		text := displayLine(line)
		shown := validUTF8(trimRune(text[:min(len(text), plainDisplay)]))
		fmt.Fprintf(out, "%6d\t%s", ln, shown)
		if len(text) > plainDisplay {
			fmt.Fprintf(out, "…[xmustard: %d bytes omitted]", len(text)-len(shown))
			oms = append(oms, Omission{Kind: "line_tail", Start: start, End: end})
		}
		out.WriteByte('\n')
		if ln >= excerptFrom && ln <= excerptTo {
			// the unnumbered excerpt is budgeted like the numbered one: a long line is
			// cut at plainDisplay with an explicit marker (numLines is unchanged)
			exact := validUTF8(trimRune(line[:min(len(line), plainDisplay)]))
			raw.Write(exact)
			if len(line) > plainDisplay {
				fmt.Fprintf(&raw, "…[xmustard: %d bytes omitted]", len(line)-len(exact))
			}
			raw.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		return nil, "", nil, 0, err
	}
	flush(sec.End, origin+lines)
	if excerptTo < last {
		fmt.Fprintf(&raw, "[xmustard: this read continues to line %d; lines %d-%d are not in this result; read on from line %d]\n",
			last, excerptTo+1, last, excerptTo+1)
	}
	// the section's line feeds: every line but an unterminated last one
	newlines := lines
	if lines > 0 {
		if lf, err := countNewlines(ctx, in.R, sec.End-1, sec.End); err != nil {
			return nil, "", nil, 0, err
		} else if lf == 0 {
			newlines--
		}
	}
	return rp, raw.String(), oms, newlines, nil
}
