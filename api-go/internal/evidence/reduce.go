package evidence

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ReducerVersion identifies the deterministic reduction rules below.
const ReducerVersion = "xm-reduce/1"

// Record is the persisted description of one projection: what was kept and omitted.
type Record struct {
	Reducer        string     `json:"reducer"`
	Mode           string     `json:"mode"` // passthrough | json | text
	Reduced        bool       `json:"reduced"`
	RawBytes       int64      `json:"raw_bytes"`
	ProjectedBytes int        `json:"projected_bytes"`
	Omissions      []Omission `json:"omissions,omitempty"`
}

// Omission names one omitted region of the original by byte range, so a client can
// expand exactly that region through the recovery handle.
type Omission struct {
	Kind    string `json:"kind"` // array_items | string_tail | members | lines | line_tail
	Pointer string `json:"pointer,omitempty"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Items   int    `json:"items,omitempty"`
	Kept    int    `json:"kept,omitempty"`
	Total   int    `json:"total,omitempty"`
}

// Reduce projects the n-byte original in r to at most target bytes (hard cap max),
// deterministically. JSON stays valid JSON with the same shape: arrays keep failure-
// bearing elements first and then leading elements, long strings keep a prefix and
// every omission is listed with its byte range. Other text keeps the head, the tail
// and every error/failure/stack-trace line with context.
//
// Reduction is linear in the original's size: one structural pre-pass records where
// every large container ends, nesting deeper than maxNesting is refused, ctx is
// checked throughout, and every write is checked against the hard cap max.
func Reduce(ctx context.Context, r io.ReaderAt, n int64, contentType string, target, max int) (string, Record, error) {
	rec := Record{Reducer: ReducerVersion, RawBytes: n}
	if target > max {
		target = max
	}
	if n <= int64(target) {
		buf := make([]byte, n)
		if m, err := r.ReadAt(buf, 0); int64(m) != n || (err != nil && !errors.Is(err, io.EOF)) {
			return "", rec, ErrCorrupt
		}
		// unchanged delivery must survive a JSON transport byte-for-byte
		if !utf8.Valid(buf) {
			return "", rec, fmt.Errorf("%w: non-UTF-8 output (%s) cannot be delivered as text", ErrUnsupported, contentType)
		}
		rec.Mode, rec.ProjectedBytes = "passthrough", len(buf)
		return string(buf), rec, nil
	}
	mode := classify(r, n, contentType)
	declaredJSON := strings.Contains(strings.ToLower(contentType), "json")
	if mode == "json" {
		jr := &jsonReducer{ctx: ctx, src: newSrc(r, n), hardMax: max, extra: max - target}
		var out bytes.Buffer
		// one linear, cancellable structural pass validates and indexes the document
		err := jr.src.index(ctx)
		if err == nil {
			start := jr.src.skipWS(0)
			var end int64
			if end, err = jr.src.skipValue(start); err == nil && jr.src.skipWS(end) != n {
				err = errSyntax // trailing content after the top-level value
			}
			if err == nil {
				err = jr.reduce(&out, start, end, target, "", 0)
			}
		}
		switch {
		case err == nil && out.Len() <= max && utf8.Valid(out.Bytes()):
			rec.Reduced, rec.Mode, rec.Omissions, rec.ProjectedBytes = true, "json", jr.omissions, out.Len()
			return out.String(), rec, nil
		case errors.Is(err, errHardCap):
			return "", rec, fmt.Errorf("%w: required JSON evidence does not fit the %d-byte projection limit", ErrUnsupported, max)
		case err != nil && !errors.Is(err, errSyntax) && !errors.Is(err, errTooDeep):
			return "", rec, err
		}
		// not a valid JSON document: never reinterpreted as text when declared JSON
		mode = "unsupported"
		if !declaredJSON && !errors.Is(err, errTooDeep) {
			mode = "text"
		}
	}
	switch mode {
	case "text":
		proj, oms, err := reduceText(ctx, r, n, target, max)
		if err != nil {
			return "", rec, err
		}
		rec.Reduced, rec.Mode, rec.Omissions, rec.ProjectedBytes = true, "text", oms, len(proj)
		return proj, rec, nil
	default:
		// Invalid structured output or binary/multimodal content has no reduction that
		// preserves its schema: pass valid UTF-8 text unchanged within the projection cap
		// (labeled), otherwise refuse explicitly — never reinterpret it as text, and never
		// emit bytes a JSON transport would silently rewrite (invalid UTF-8 becomes U+FFFD).
		if n > int64(max) {
			return "", rec, fmt.Errorf("%w (%d bytes, %s)", ErrUnsupported, n, contentType)
		}
		buf := make([]byte, n)
		if m, err := r.ReadAt(buf, 0); int64(m) != n || (err != nil && !errors.Is(err, io.EOF)) {
			return "", rec, ErrCorrupt
		}
		if !utf8.Valid(buf) || bytes.IndexByte(buf, 0) >= 0 {
			return "", rec, fmt.Errorf("%w: binary or non-UTF-8 output (%s) cannot be delivered as text", ErrUnsupported, contentType)
		}
		rec.Mode, rec.ProjectedBytes = "unsupported_passthrough", len(buf)
		return string(buf), rec, nil
	}
}

// salient marks failure evidence that must survive reduction.
// Whitespace between a status key and its value is unbounded, as in JSON; the
// confirmation window extends over adjacent separator runs (salience.go).
var salient = regexp.MustCompile(`(?i)(error|fail|panic|exception|traceback|fatal|assert|conflict|contradict|stale"\s*:\s*true|"is_?error"\s*:\s*true|"(ok|passed|success)"\s*:\s*false|exit[_ ]?(code|status)"?\s*[:=]\s*[1-9])`)

// classify decides how an original may be reduced: "json" (valid JSON document),
// "text" (valid UTF-8 text that is not declared JSON), or "unsupported" (declared JSON
// that does not parse, binary, or multimodal content types).
func classify(r io.ReaderAt, n int64, contentType string) string {
	ct := strings.ToLower(contentType)
	for _, p := range []string{"image/", "audio/", "video/", "application/octet-stream", "application/pdf"} {
		if strings.HasPrefix(ct, p) {
			return "unsupported"
		}
	}
	head := make([]byte, min(n, 8<<10))
	m, _ := r.ReadAt(head, 0)
	head = head[:m]
	if bytes.IndexByte(head, 0) >= 0 {
		return "unsupported"
	}
	// allow a rune cut at the sniff window's end
	valid := head
	for k := 0; k < utf8.UTFMax && len(valid) > 0 && !utf8.Valid(valid); k++ {
		valid = valid[:len(valid)-1]
	}
	if !utf8.Valid(valid) {
		return "unsupported"
	}
	// structural sniff only; Reduce validates the document in its cancellable index pass
	declaredJSON := strings.Contains(ct, "json")
	s := newSrc(r, n)
	if c, ok := s.at(s.skipWS(0)); ok && (c == '{' || c == '[') {
		return "json"
	}
	if declaredJSON {
		return "unsupported"
	}
	return "text"
}

// --- byte source over io.ReaderAt with a small read-ahead window ---

type src struct {
	r      io.ReaderAt
	n      int64
	win    []byte
	winOff int64
	// ends records where each container larger than indexThreshold ends, so a value
	// is scanned once however deeply the reducer descends (linear, not depth x size).
	ends map[int64]int64
	// salientPos lists confirmed failure-evidence offsets (see salience.go).
	salientPos                       []int64
	salientIndexed, salientSaturated bool
	// ctx cancels long scans (a single huge string included); nil means never.
	ctx context.Context
	err error // first cancellation seen by a scan
}

// cancelled checks ctx at most once per 256 KiB of scanned input.
func (s *src) cancelled(i int64, next *int64) bool {
	if s.ctx == nil || i < *next {
		return false
	}
	*next = i + 256<<10
	if err := s.ctx.Err(); err != nil {
		s.err = err
		return true
	}
	return false
}

func newSrc(r io.ReaderAt, n int64) *src { return &src{r: r, n: n, winOff: -1} }

func (s *src) at(i int64) (byte, bool) {
	if i < 0 || i >= s.n {
		return 0, false
	}
	if s.winOff < 0 || i < s.winOff || i >= s.winOff+int64(len(s.win)) {
		size := int64(64 << 10)
		if rem := s.n - i; rem < size {
			size = rem
		}
		if cap(s.win) < int(size) {
			s.win = make([]byte, size)
		}
		s.win = s.win[:size]
		m, err := s.r.ReadAt(s.win, i)
		if m == 0 && err != nil {
			return 0, false
		}
		s.win, s.winOff = s.win[:m], i
	}
	return s.win[i-s.winOff], true
}

func (s *src) read(start, end int64) []byte {
	buf := make([]byte, end-start)
	m, _ := s.r.ReadAt(buf, start)
	return buf[:m]
}

func (s *src) skipWS(i int64) int64 {
	for {
		c, ok := s.at(i)
		if !ok || (c != ' ' && c != '\n' && c != '\r' && c != '\t') {
			return i
		}
		i++
	}
}

var errSyntax = errors.New("json syntax")

// maxNesting bounds JSON nesting depth; deeper documents are unsupported.
const maxNesting = 512

// indexThreshold: containers at least this large get their end recorded; smaller ones
// are cheap to rescan.
const indexThreshold = 4 << 10

var (
	errHardCap = errors.New("projection hard cap reached")
	// errTooDeep marks nesting beyond maxNesting: unsupported structured output.
	errTooDeep = fmt.Errorf("%w: JSON nesting deeper than %d", ErrUnsupported, maxNesting)
)

// index scans the document once, recording large-container ends and enforcing the
// nesting limit. It checks ctx every 1 MiB.
func (s *src) index(ctx context.Context) error {
	s.ctx = ctx
	s.ends = map[int64]int64{}
	var stack []int64
	nextCheck := int64(0)
	for i := int64(0); i < s.n; i++ {
		if i >= nextCheck { // at least every 256 KiB of input (string skips jump ahead)
			if err := ctx.Err(); err != nil {
				return err
			}
			nextCheck = i + 256<<10
		}
		c, _ := s.at(i)
		switch c {
		case '"':
			end, err := s.skipString(i)
			if err != nil {
				if s.err != nil {
					return s.err
				}
				return err
			}
			i = end - 1
		case '{', '[':
			if len(stack) >= maxNesting {
				return errTooDeep
			}
			stack = append(stack, i)
		case '}', ']':
			if len(stack) == 0 {
				return errSyntax
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if i+1-open >= indexThreshold {
				s.ends[open] = i + 1
			}
		}
	}
	if len(stack) != 0 {
		return errSyntax
	}
	return s.scanSalient(ctx)
}

// skipValue returns the end offset of the JSON value starting at i.
func (s *src) skipValue(i int64) (int64, error) {
	c, ok := s.at(i)
	if !ok {
		return 0, errSyntax
	}
	if end, ok := s.ends[i]; ok {
		return end, nil
	}
	switch c {
	case '"':
		return s.skipString(i)
	case '{', '[':
		depth := 0
		next := i
		for {
			if s.cancelled(i, &next) {
				return 0, s.err
			}
			c, ok := s.at(i)
			if !ok {
				return 0, errSyntax
			}
			switch c {
			case '"':
				end, err := s.skipString(i)
				if err != nil {
					return 0, err
				}
				i = end
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
			i++
		}
	default:
		for {
			c, ok := s.at(i)
			if !ok || c == ',' || c == '}' || c == ']' || c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				return i, nil
			}
			i++
		}
	}
}

func (s *src) skipString(i int64) (int64, error) {
	i++ // opening quote
	next := i
	for {
		if s.cancelled(i, &next) {
			return 0, s.err
		}
		c, ok := s.at(i)
		if !ok {
			return 0, errSyntax
		}
		switch c {
		case '\\':
			i += 2
		case '"':
			return i + 1, nil
		default:
			i++
		}
	}
}

type span struct{ start, end int64 }

type member struct {
	key span // including quotes
	val span
}

// maxSpans bounds how many child spans one container records; further children are
// counted and reported as one omitted range.
const maxSpans = 4096

func (s *src) members(start int64) ([]member, int, span, error) {
	var out []member
	extra, extraSpan := 0, span{-1, -1}
	i := s.skipWS(start + 1)
	if c, _ := s.at(i); c == '}' {
		return out, 0, extraSpan, nil
	}
	for {
		ks := i
		ke, err := s.skipString(ks)
		if err != nil {
			return nil, 0, extraSpan, err
		}
		i = s.skipWS(ke)
		if c, _ := s.at(i); c != ':' {
			return nil, 0, extraSpan, errSyntax
		}
		vs := s.skipWS(i + 1)
		ve, err := s.skipValue(vs)
		if err != nil {
			return nil, 0, extraSpan, err
		}
		if len(out) < maxSpans {
			out = append(out, member{span{ks, ke}, span{vs, ve}})
		} else {
			extra++
			if extraSpan.start < 0 {
				extraSpan.start = ks
			}
			extraSpan.end = ve
		}
		i = s.skipWS(ve)
		c, _ := s.at(i)
		if c == '}' {
			return out, extra, extraSpan, nil
		}
		if c != ',' {
			return nil, 0, extraSpan, errSyntax
		}
		i = s.skipWS(i + 1)
	}
}

type element struct {
	span
	index   int
	salient bool
}

// elements lists array children: the first maxSpans, plus up to maxSpans salient ones
// beyond that; total counts all of them.
func (s *src) elements(ctx context.Context, start int64) ([]element, int, int, error) {
	var out []element
	total, salientBeyond, salientAll, salientKept := 0, 0, 0, 0
	i := s.skipWS(start + 1)
	if c, _ := s.at(i); c == ']' {
		return out, 0, 0, nil
	}
	for {
		if total&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, 0, err
			}
		}
		ve, err := s.skipValue(i)
		if err != nil {
			return nil, 0, 0, err
		}
		e := element{span: span{i, ve}, index: total}
		e.salient = s.salientIn(i, ve)
		if e.salient {
			salientAll++
		}
		if total < maxSpans {
			out = append(out, e)
			if e.salient {
				salientKept++
			}
		} else if e.salient && salientBeyond < maxSpans {
			out = append(out, e)
			salientBeyond++
			salientKept++
		}
		total++
		i = s.skipWS(ve)
		c, _ := s.at(i)
		if c == ']' {
			return out, total, salientAll - salientKept, nil
		}
		if c != ',' {
			return nil, 0, 0, errSyntax
		}
		i = s.skipWS(i + 1)
	}
}

type jsonReducer struct {
	ctx       context.Context
	src       *src
	omissions []Omission
	hardMax   int // no projection may exceed this
	extra     int // shared slack beyond the target for mandatory failure elements
}

func (j *jsonReducer) omit(o Omission) {
	if len(j.omissions) < 256 {
		j.omissions = append(j.omissions, o)
	}
}

// write appends b unless that would pass the hard cap.
func (j *jsonReducer) write(out *bytes.Buffer, b []byte) error {
	if out.Len()+len(b) > j.hardMax {
		return errHardCap
	}
	out.Write(b)
	return nil
}

// reduce writes the value at [start,end) into out using about budget bytes.
func (j *jsonReducer) reduce(out *bytes.Buffer, start, end int64, budget int, ptr string, depth int) error {
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if depth > maxNesting {
		return errTooDeep
	}
	if out.Len() > j.hardMax {
		return errHardCap
	}
	if end-start <= int64(budget) {
		if end-start > int64(j.hardMax) {
			return errHardCap
		}
		return j.write(out, j.src.read(start, end))
	}
	c, _ := j.src.at(start)
	switch c {
	case '{':
		return j.reduceObject(out, start, end, budget, ptr, depth)
	case '[':
		return j.reduceArray(out, start, end, budget, ptr, depth)
	case '"':
		j.reduceString(out, start, end, budget, ptr)
		return nil
	default:
		if end-start > int64(j.hardMax) {
			return errHardCap
		}
		return j.write(out, j.src.read(start, end)) // numbers/literals are never meaningfully large
	}
}

const truncMarker = "…[xmustard: %d bytes omitted]"

// reduceString keeps the head, windows around failure evidence found later in the
// string, and the tail, joined by omission markers. Cut points never split a JSON
// escape or a UTF-8 sequence, so the projection stays a valid JSON string.
func (j *jsonReducer) reduceString(out *bytes.Buffer, start, end int64, budget int, ptr string) {
	lo, hi := start+1, end-1 // string body, excluding quotes
	room := int64(max(budget-3*len(truncMarker)-2, 48))
	headLen, tailLen, winLen := room/3, room/6, room/4
	segs := []span{{lo, lo + headLen}}
	for _, m := range j.findSalient(lo+headLen, hi-tailLen, 2) {
		segs = append(segs, span{m - winLen/4, m + winLen*3/4})
	}
	segs = append(segs, span{hi - tailLen, hi})
	// clip, make cut points safe, merge overlaps
	var merged []span
	for _, sg := range segs {
		sg.start, sg.end = j.safeCut(max(sg.start, lo), lo, true), j.safeCut(min(sg.end, hi), lo, false)
		if sg.end <= sg.start {
			continue
		}
		if n := len(merged); n > 0 && sg.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, sg.end)
			continue
		}
		merged = append(merged, sg)
	}
	out.WriteByte('"')
	prev := lo
	for _, sg := range merged {
		if sg.start > prev {
			fmt.Fprintf(out, truncMarker, sg.start-prev)
			j.omit(Omission{Kind: "string_span", Pointer: ptr, Start: prev, End: sg.start})
		}
		out.Write(j.src.read(sg.start, sg.end))
		prev = sg.end
	}
	if prev < hi {
		fmt.Fprintf(out, truncMarker, hi-prev)
		j.omit(Omission{Kind: "string_span", Pointer: ptr, Start: prev, End: hi})
	}
	out.WriteByte('"')
}

// findSalient returns up to limit offsets in [from,to) where failure evidence starts,
// scanning in bounded chunks.
func (j *jsonReducer) findSalient(from, to int64, limit int) []int64 {
	return j.src.salientRange(from, to, limit, 128)
}

// safeCut moves a cut point inside a JSON string body (starting at lo) off any escape
// sequence or UTF-8 continuation byte: forward for a segment start, backward for an end.
func (j *jsonReducer) safeCut(pos, lo int64, forward bool) int64 {
	for k := 0; k < 8; k++ {
		if j.cutOK(pos, lo) {
			return pos
		}
		if forward {
			pos++
		} else {
			pos--
		}
	}
	return pos
}

func (j *jsonReducer) cutOK(pos, lo int64) bool {
	if pos <= lo {
		return true
	}
	if c, ok := j.src.at(pos); ok && c >= 0x80 && c < 0xC0 {
		return false // UTF-8 continuation byte
	}
	// inside "\X" or "\uXXXX": a backslash with an odd run of backslashes before it
	// that starts within the previous 5 bytes
	for d := int64(1); d <= 5 && pos-d >= lo; d++ {
		if c, _ := j.src.at(pos - d); c != '\\' {
			continue
		}
		run := int64(0)
		for p := pos - d; p >= lo; p-- {
			if c, _ := j.src.at(p); c != '\\' {
				break
			}
			run++
		}
		if run%2 == 0 {
			continue // an escaped backslash, not an escape introducer
		}
		next, _ := j.src.at(pos - d + 1)
		if d == 1 || (next == 'u' && d <= 5) {
			return false
		}
	}
	return true
}

func placeholder(c byte) string {
	switch c {
	case '{':
		return "{}"
	case '[':
		return "[]"
	case '"':
		return `""`
	default:
		return "null"
	}
}

func (j *jsonReducer) reduceObject(out *bytes.Buffer, start, end int64, budget int, ptr string, depth int) error {
	ms, extra, extraSpan, err := j.src.members(start)
	if err != nil {
		return err
	}
	remaining := budget - 2
	include := make([]int, len(ms)) // 0 placeholder, 1 verbatim, 2 reduce with share
	cost := func(m member) int { return int(m.key.end-m.key.start) + 2 + int(m.val.end-m.val.start) }
	// phase 1: small members verbatim, in document order
	for k, m := range ms {
		if c := cost(m); c <= 512 && c <= remaining {
			include[k], remaining = 1, remaining-c
		}
	}
	// phase 2: large members share what is left. A member whose value carries failure
	// evidence is mandatory: it may draw on the shared slack up to the hard cap and is
	// never replaced by a placeholder.
	var large, mandatory []int
	for k, m := range ms {
		if include[k] != 0 {
			continue
		}
		if j.src.salientIn(m.val.start, m.val.end) {
			mandatory = append(mandatory, k)
		} else {
			large = append(large, k)
		}
	}
	// every large member shares the remaining budget; a mandatory member is guaranteed
	// at least 256 bytes, drawing on the shared slack (up to the hard cap) if needed.
	shares := make(map[int]int, len(large)+len(mandatory))
	if n := len(large) + len(mandatory); n > 0 {
		per := remaining / n
		for _, k := range large {
			shares[k] = per
		}
		for _, k := range mandatory {
			want := max(per, min(256, cost(ms[k])))
			if want > per {
				if j.extra < want-per {
					return errHardCap
				}
				j.extra -= want - per
			}
			shares[k] = want
		}
	}
	// every key is kept verbatim (no key placeholders): if the keys alone cannot fit
	// the hard cap, fail before writing anything
	keyBytes := int64(0)
	for _, m := range ms {
		keyBytes += m.key.end - m.key.start + 2
	}
	if int64(out.Len())+keyBytes > int64(j.hardMax) {
		return errHardCap
	}
	if err := j.write(out, []byte{'{'}); err != nil {
		return err
	}
	for k, m := range ms {
		if k > 0 {
			if err := j.write(out, []byte{','}); err != nil {
				return err
			}
		}
		if err := j.write(out, j.src.read(m.key.start, m.key.end)); err != nil {
			return err
		}
		if err := j.write(out, []byte{':'}); err != nil {
			return err
		}
		child := func() string { return ptr + "/" + jsonPointerToken(j.src.read(m.key.start+1, m.key.end-1)) }
		share := shares[k] - int(m.key.end-m.key.start) - 2
		switch {
		case include[k] == 1:
			if err := j.write(out, j.src.read(m.val.start, m.val.end)); err != nil {
				return err
			}
		case share >= 64:
			if err := j.reduce(out, m.val.start, m.val.end, share, child(), depth+1); err != nil {
				return err
			}
		default:
			c, _ := j.src.at(m.val.start)
			if err := j.write(out, []byte(placeholder(c))); err != nil {
				return err
			}
			j.omit(Omission{Kind: "members", Pointer: child(), Start: m.val.start, End: m.val.end})
		}
	}
	if err := j.write(out, []byte{'}'}); err != nil {
		return err
	}
	if extra > 0 {
		j.omit(Omission{Kind: "members", Pointer: ptr, Start: extraSpan.start, End: extraSpan.end, Items: extra})
	}
	return nil
}

func (j *jsonReducer) reduceArray(out *bytes.Buffer, start, end int64, budget int, ptr string, depth int) error {
	els, total, unrecordedSalient, err := j.src.elements(j.ctx, start)
	if err != nil {
		return err
	}
	if unrecordedSalient > 0 {
		// failure elements beyond what can be tracked cannot all be kept
		return errHardCap
	}
	remaining := budget - 2
	chosen := make(map[int]int, len(els)) // element slot -> budget (-1 verbatim)
	size := func(k int) int { return int(els[k].end-els[k].start) + 1 }
	// Failure evidence is mandatory: every salient element is kept — whole when the
	// target plus the shared slack (up to the hard cap) allows, otherwise reduced into an
	// equal share that keeps its failure windows. If even that cannot fit, the
	// projection fails with an explicit size error rather than dropping failures.
	var salientIdx []int
	for k := range els {
		if els[k].salient {
			salientIdx = append(salientIdx, k)
		}
	}
	if len(salientIdx) > 0 {
		per := max(256, remaining/len(salientIdx))
		need := 0
		for _, k := range salientIdx {
			if size(k) <= per {
				chosen[k] = -1
				need += size(k)
			} else {
				chosen[k] = per
				need += per
			}
		}
		if need <= remaining {
			remaining -= need
		} else if need-remaining <= j.extra {
			j.extra -= need - remaining
			remaining = 0
		} else {
			return errHardCap
		}
	}
	for k := range els {
		if _, ok := chosen[k]; ok {
			continue
		}
		if size(k) > remaining {
			break
		}
		chosen[k], remaining = -1, remaining-size(k)
	}
	if len(chosen) == 0 && len(els) > 0 {
		// even the first element is too large: reduce it into the whole budget
		chosen[0] = remaining
	}
	if err := j.write(out, []byte{'['}); err != nil {
		return err
	}
	kept, first := 0, true
	var gap *Omission
	flush := func() {
		if gap != nil {
			j.omit(*gap)
			gap = nil
		}
	}
	lastIndex := -1
	for k, e := range els {
		b, ok := chosen[k]
		if !ok {
			if gap == nil {
				gap = &Omission{Kind: "array_items", Pointer: ptr, Start: e.start, Total: total}
			}
			gap.End, gap.Items = e.end, gap.Items+1+(e.index-lastIndex-1)
			lastIndex = e.index
			continue
		}
		if e.index-lastIndex > 1 && gap == nil {
			// unrecorded elements (beyond maxSpans) between recorded ones
			gap = &Omission{Kind: "array_items", Pointer: ptr, Start: -1, End: e.start, Total: total, Items: e.index - lastIndex - 1}
		}
		flush()
		lastIndex = e.index
		if !first {
			if err := j.write(out, []byte{','}); err != nil {
				return err
			}
		}
		first = false
		kept++
		if b == -1 {
			if err := j.write(out, j.src.read(e.start, e.end)); err != nil {
				return err
			}
		} else if err := j.reduce(out, e.start, e.end, max(b, 64), ptr+"/"+strconv.Itoa(e.index), depth+1); err != nil {
			return err
		}
	}
	if lastIndex < total-1 {
		if gap == nil {
			gap = &Omission{Kind: "array_items", Pointer: ptr, Start: -1, Total: total}
		}
		gap.Items += total - 1 - lastIndex
		gap.End = end - 1
	}
	flush()
	if err := j.write(out, []byte{']'}); err != nil {
		return err
	}
	for i := range j.omissions {
		if j.omissions[i].Pointer == ptr && j.omissions[i].Kind == "array_items" {
			j.omissions[i].Kept = kept
		}
	}
	return nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func jsonPointerToken(raw []byte) string {
	return pointerEscaper.Replace(string(raw))
}

// --- text reduction ---

// stackLine marks continuation lines of a stack trace (kept with a salient line).
var stackLine = regexp.MustCompile(`^(\s+at |\s+File "|\s+\S+\.(go|rs|ts|js|py):\d+|goroutine \d+|\s+\d+: |Caused by:|\s+\.\.\. \d+ more)`)

const (
	textHead         = 40
	textTail         = 40
	textContext      = 2
	textMaxLine      = 4 << 10
	maxRequiredLines = 16384
	// maxSalienceLine: text lines up to this size are examined whole for failure
	// evidence; a longer line is refused explicitly rather than examined in part.
	maxSalienceLine = 1 << 20
	// textLineOverhead covers the omission/truncation markers an emitted line may add.
	textLineOverhead = 96
)

type lineInfo struct {
	start, end int64
}

type lineCost struct{ idx, cost int }

// reduceText selects the MANDATORY lines first — every failure/status line with its
// context and stack-trace continuation — and only then spends what is left of the
// target on the head and the tail. Required evidence may use up to hardMax; if it
// cannot fit even there, the reduction fails explicitly rather than returning a
// success-looking projection without it.
func reduceText(ctx context.Context, r io.ReaderAt, n int64, target, hardMax int) (string, []Omission, error) {
	cost := func(full int64) int { return int(min(full, textMaxLine)) + textLineOverhead }
	// pass 1: mandatory lines and the costs of head/tail candidates
	mandatory := map[int]int{}
	whole := map[int]bool{} // lines emitted whole because they carry failure evidence
	var recent, head, tail []lineCost
	after, total := 0, 0
	inTrace := false
	br := bufio.NewReaderSize(io.NewSectionReader(r, 0, n), 64<<10)
	for {
		if total&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return "", nil, err
			}
		}
		line, full, err := readLine(br, maxSalienceLine)
		if len(line) > 0 || err == nil {
			c := cost(full)
			if full > maxSalienceLine {
				// the whole line could not be examined for failure evidence
				return "", nil, fmt.Errorf("%w: text line of %d bytes exceeds the %d-byte failure-detection limit", ErrUnsupported, full, maxSalienceLine)
			}
			isSalient, serr := salientMatch(line)
			if serr != nil {
				return "", nil, serr
			}
			if isSalient || (inTrace && stackLine.Match(line)) {
				for _, p := range recent {
					if !whole[p.idx] { // never shrink a whole failure line to context cost
						mandatory[p.idx] = p.cost
					}
				}
				// a line carrying failure evidence is kept whole (its failure bytes may
				// sit past the 4 KiB display cut); context lines stay truncated
				mandatory[total] = int(full) + textLineOverhead
				whole[total] = true
				after, inTrace = textContext, true
			} else {
				inTrace = false
				if after > 0 {
					mandatory[total] = c
					after--
				}
			}
			if len(mandatory) > maxRequiredLines {
				return "", nil, fmt.Errorf("%w: more than %d required failure lines", ErrUnsupported, maxRequiredLines)
			}
			recent = append(recent, lineCost{total, c})
			if len(recent) > textContext {
				recent = recent[1:]
			}
			if total < textHead {
				head = append(head, lineCost{total, c})
			}
			tail = append(tail, lineCost{total, c})
			if len(tail) > textTail {
				tail = tail[1:]
			}
			total++
		}
		if err != nil {
			break
		}
	}
	used := 0
	keep := make(map[int]bool, len(mandatory)+textHead+textTail)
	for idx, c := range mandatory {
		keep[idx] = true
		used += c
	}
	if used > hardMax {
		return "", nil, fmt.Errorf("%w: required failure evidence (%d bytes) exceeds the %d-byte projection limit", ErrUnsupported, used, hardMax)
	}
	// then what is left of the target: the tail (newest lines first) may take up to half,
	// the head (in order) takes the rest, and the tail any remainder.
	fillTail := func(limit int) {
		for k := len(tail) - 1; k >= 0; k-- {
			t := tail[k]
			if keep[t.idx] {
				continue
			}
			if used+t.cost > limit {
				return
			}
			keep[t.idx], used = true, used+t.cost
		}
	}
	fillTail(used + max(0, target-used)/2)
	for _, h := range head {
		if keep[h.idx] {
			continue
		}
		if used+h.cost > target {
			break
		}
		keep[h.idx], used = true, used+h.cost
	}
	fillTail(target)
	// pass 2: emit exactly the selected lines, in input order
	var out bytes.Buffer
	var oms []Omission
	br = bufio.NewReaderSize(io.NewSectionReader(r, 0, n), 64<<10)
	var off int64
	gapStart, gapLines := int64(-1), 0
	for idx := 0; idx < total; idx++ {
		keepBytes := textMaxLine + 1
		if whole[idx] {
			keepBytes = maxSalienceLine // pass 1 refused anything longer
		}
		line, full, _ := readLine(br, keepBytes)
		info := lineInfo{off, off + full}
		off = info.end
		if !keep[idx] {
			if gapStart < 0 {
				gapStart = info.start
			}
			gapLines++
			continue
		}
		if gapStart >= 0 {
			fmt.Fprintf(&out, "[xmustard: %d lines omitted, bytes %d-%d]\n", gapLines, gapStart, info.start)
			oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: info.start, Items: gapLines})
			gapStart, gapLines = -1, 0
		}
		emitted := line
		if full > textMaxLine && !whole[idx] {
			emitted = line[:textMaxLine]
			for k := 0; k < utf8.UTFMax && len(emitted) > 0 && !utf8.Valid(emitted); k++ {
				emitted = emitted[:len(emitted)-1] // do not split a trailing rune
			}
		}
		if utf8.Valid(emitted) {
			out.Write(emitted)
		} else {
			// the projection must be valid UTF-8 (JSON transports rewrite anything
			// else); the exact bytes stay recoverable through the handle
			out.WriteString(strings.ToValidUTF8(string(emitted), "�"))
			oms = append(oms, Omission{Kind: "invalid_utf8", Start: info.start, End: info.end})
		}
		if full > int64(len(emitted)) {
			fmt.Fprintf(&out, "…[xmustard: %d bytes omitted]\n", full-int64(len(emitted)))
			oms = append(oms, Omission{Kind: "line_tail", Start: info.start + int64(len(emitted)), End: info.end})
		}
	}
	if gapStart >= 0 {
		fmt.Fprintf(&out, "[xmustard: %d lines omitted, bytes %d-%d]\n", gapLines, gapStart, n)
		oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: n, Items: gapLines})
	}
	if out.Len() > hardMax {
		return "", nil, fmt.Errorf("%w: text projection %d bytes exceeds %d", ErrUnsupported, out.Len(), hardMax)
	}
	return out.String(), oms, nil
}

// readLine returns the next line including its newline (the final line may lack one)
// and its full byte length. At most keep bytes are retained; the rest is consumed
// without growing memory.
func readLine(br *bufio.Reader, keep int) ([]byte, int64, error) {
	var line []byte
	var full int64
	for {
		chunk, err := br.ReadSlice('\n')
		full += int64(len(chunk))
		if room := keep - len(line); room > 0 {
			line = append(line, chunk[:min(room, len(chunk))]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, full, err
	}
}
