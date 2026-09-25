package evidence

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
)

// Failure-evidence detection. The precise pattern (`salient`) is a case-folding
// regexp, which is slow; running it over the same bytes at every nesting level made
// reduction quadratic in practice. Instead a cheap keyword prefilter finds candidate
// positions and the regexp only confirms small windows around them, once per document.

var salientKeywords = [][]byte{
	[]byte("error"), []byte("fail"), []byte("panic"), []byte("exception"), []byte("traceback"),
	[]byte("fatal"), []byte("assert"), []byte("conflict"), []byte("contradict"), []byte("stale"),
	[]byte("false"), []byte("exit"),
}

// salientWindow is the context the regexp sees around a keyword hit, beyond any run
// of separator bytes (whitespace, ':', '=', '"') adjacent to the keyword. Extending
// over those runs keeps status patterns ("ok": false, "stale": true, exit code 3)
// recognized whatever legal whitespace separates their parts.
const salientWindow = 64

// maxSeparatorRun bounds how far a confirmation window extends over separators; a
// longer run next to a status keyword is refused as an unsupported input shape rather
// than silently not examined.
const maxSeparatorRun = 64 << 10

var errSeparatorRun = fmt.Errorf("%w: separator run over %d bytes next to a failure-status keyword", ErrUnsupported, maxSeparatorRun)

// byteSource gives absolute-offset access for window extension beyond a scanned chunk.
type byteSource interface {
	at(i int64) (byte, bool)
	read(start, end int64) []byte
}

type sliceSource []byte

func (b sliceSource) at(i int64) (byte, bool) {
	if i < 0 || i >= int64(len(b)) {
		return 0, false
	}
	return b[i], true
}
func (b sliceSource) read(start, end int64) []byte { return b[start:end] }

func isSeparator(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ':' || c == '=' || c == '"'
}

// maxSalientPositions bounds the per-document position index (8 bytes each); a
// variable only so tests can exercise exhaustion without a multi-megabyte fixture.
var maxSalientPositions = 1 << 20

var lowerPool = sync.Pool{New: func() any { b := make([]byte, 0, 64<<10); return &b }}

func asciiLower(dst, src []byte) []byte {
	dst = append(dst[:0], src...)
	for i, c := range dst {
		if c >= 'A' && c <= 'Z' {
			dst[i] = c + ('a' - 'A')
		}
	}
	return dst
}

// salientHits returns offsets in b (which is src[base:base+len(b)]) where confirmed
// failure evidence starts.
func salientHits(src byteSource, base int64, b []byte, limit int) ([]int, error) {
	lp := lowerPool.Get().(*[]byte)
	lower := asciiLower(*lp, b)
	defer func() { *lp = lower[:0]; lowerPool.Put(lp) }()
	var hits []int
	for _, kw := range salientKeywords {
		for off := 0; off < len(lower); {
			i := bytes.Index(lower[off:], kw)
			if i < 0 {
				break
			}
			h := off + i
			lo, hi, err := confirmWindow(src, base+int64(h), base+int64(h+len(kw)))
			if err != nil {
				return nil, err
			}
			if salient.Match(src.read(lo, hi)) {
				hits = append(hits, h)
				if len(hits) >= limit {
					return hits, nil
				}
			}
			off = h + len(kw)
		}
	}
	return hits, nil
}

// confirmWindow returns the regexp window around a keyword at [ks,ke): salientWindow
// bytes beyond the separator runs adjacent on each side.
func confirmWindow(src byteSource, ks, ke int64) (int64, int64, error) {
	lo := ks
	for n := 0; ; n++ {
		c, ok := src.at(lo - 1)
		if !ok || !isSeparator(c) {
			break
		}
		if n >= maxSeparatorRun {
			return 0, 0, errSeparatorRun
		}
		lo--
	}
	hi := ke
	// the rest of an identifier the keyword starts (exit_code, exit_status)
	for k := 0; k < 16; k++ {
		c, ok := src.at(hi)
		if !ok || !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			break
		}
		hi++
	}
	for n := 0; ; n++ {
		c, ok := src.at(hi)
		if !ok || !isSeparator(c) {
			break
		}
		if n >= maxSeparatorRun {
			return 0, 0, errSeparatorRun
		}
		hi++
	}
	lo = max(0, lo-salientWindow)
	for k := 0; k < salientWindow; k++ {
		if _, ok := src.at(hi); !ok {
			break
		}
		hi++
	}
	return lo, hi, nil
}

// salientMatch reports whether b contains failure evidence.
func salientMatch(b []byte) (bool, error) {
	hits, err := salientHits(sliceSource(b), 0, b, 1)
	return len(hits) > 0, err
}

// scanSalient records every failure-evidence position in the document once.
func (s *src) scanSalient(ctx context.Context) error {
	const chunk, overlap = 1 << 20, 2 * salientWindow
	s.salientPos = s.salientPos[:0]
	buf := make([]byte, chunk) // one reusable chunk buffer
	for off := int64(0); off < s.n; off += chunk - overlap {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(s.n, off+chunk)
		m, _ := s.r.ReadAt(buf[:end-off], off)
		hits, err := salientHits(s, off, buf[:m], maxSalientPositions)
		if err != nil {
			return err
		}
		for _, h := range hits {
			s.salientPos = append(s.salientPos, off+int64(h))
		}
		if len(s.salientPos) >= maxSalientPositions {
			// the detection budget is exhausted: later mandatory failures could be
			// missed, so the document is refused explicitly rather than reduced
			return fmt.Errorf("%w: more than %d failure-evidence positions", ErrUnsupported, maxSalientPositions)
		}
		if end == s.n {
			break
		}
	}
	sort.Slice(s.salientPos, func(a, b int) bool { return s.salientPos[a] < s.salientPos[b] })
	s.salientIndexed = true
	return nil
}

// salientIn reports whether [a,b) contains failure evidence.
func (s *src) salientIn(a, b int64) bool {
	if !s.salientIndexed || s.salientSaturated {
		ok, _ := salientMatch(s.read(a, min(b, a+64<<10)))
		return ok
	}
	i := sort.Search(len(s.salientPos), func(k int) bool { return s.salientPos[k] >= a })
	return i < len(s.salientPos) && s.salientPos[i] < b
}

// salientRange returns up to limit evidence positions in [a,b), at least `gap` apart.
func (s *src) salientRange(a, b int64, limit int, gap int64) []int64 {
	var out []int64
	if !s.salientIndexed || s.salientSaturated {
		buf := s.read(a, min(b, a+(1<<20)))
		hits, _ := salientHits(sliceSource(buf), 0, buf, limit)
		for _, h := range hits {
			out = append(out, a+int64(h))
		}
		sort.Slice(out, func(x, y int) bool { return out[x] < out[y] })
		return out
	}
	i := sort.Search(len(s.salientPos), func(k int) bool { return s.salientPos[k] >= a })
	for ; i < len(s.salientPos) && s.salientPos[i] < b && len(out) < limit; i++ {
		if len(out) == 0 || s.salientPos[i]-out[len(out)-1] > gap {
			out = append(out, s.salientPos[i])
		}
	}
	return out
}
