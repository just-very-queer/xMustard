package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Recovery by search inside the original (PAR-CTX-04). Besides byte pages (Read), a
// retained original can be searched: an RE2 pattern or a case-insensitive literal,
// optionally within a line range, or just a line range. The scan streams the file in
// SearchChunk (1 MiB) reads through one pooled buffer, carrying at most one partial
// line between chunks, and stops at the match cap, returning where to resume. It is
// authorized exactly like Read: workspace scope, the issuing principal, expiry and
// revocation all fail closed, and freshness is labeled the same way.

const (
	SearchChunk          = 1 << 20
	DefaultSearchMatches = 40
	MaxSearchMatches     = 200
	MaxSearchContext     = 5
	MaxSearchLines       = 400
	maxPatternBytes      = 1 << 10
	searchLineBytes      = 512     // bytes shown of one matching or context line
	maxSearchLine        = 1 << 20 // longer lines are matched on their first 1 MiB
)

// ErrInvalidSearch reports a malformed search request.
var ErrInvalidSearch = errors.New("invalid evidence search")

// SearchRequest asks for matches (or a line range) inside one original.
type SearchRequest struct {
	ReadRequest // workspace, handle, principal, identity; Offset resumes a scan
	// StartLine is the line number at Offset. At offset 0 it defaults to the
	// original's first line number (1, or the first line of a read range).
	StartLine  int
	Pattern    string // RE2 regular expression
	Query      string // literal, case-insensitive (alternative to Pattern)
	FromLine   int    // inclusive line range; 0 = open
	ToLine     int
	MaxMatches int // default 40, at most 200
	Context    int // lines of context around a match, at most 5
}

// SearchLine is one returned line.
type SearchLine struct {
	Line      int    `json:"line"`
	Offset    int64  `json:"offset"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
	Match     bool   `json:"match,omitempty"`
}

// SearchResult is one search answer.
type SearchResult struct {
	Handle          string       `json:"handle"`
	Tool            string       `json:"tool"`
	CallID          string       `json:"call_id,omitempty"`
	ContentType     string       `json:"content_type"`
	Pattern         string       `json:"pattern,omitempty"`
	Query           string       `json:"query,omitempty"`
	FromLine        int          `json:"from_line,omitempty"`
	ToLine          int          `json:"to_line,omitempty"`
	FirstLine       int          `json:"first_line"` // line number of the original's first line
	Lines           []SearchLine `json:"lines"`
	Matches         int          `json:"matches"`
	MatchCapReached bool         `json:"match_cap_reached"`
	ByteCapReached  bool         `json:"byte_cap_reached,omitempty"`
	LongLines       int          `json:"long_lines_truncated,omitempty"`
	StartOffset     int64        `json:"start_offset"`
	NextOffset      int64        `json:"next_offset"`
	NextLine        int          `json:"next_line"`
	EOF             bool         `json:"eof"`
	BytesScanned    int64        `json:"bytes_scanned"`
	LinesScanned    int          `json:"lines_scanned"`
	TotalBytes      int64        `json:"total_bytes"`
	ChunkSize       int          `json:"chunk_size"`
	RawSHA256       string       `json:"raw_sha256"`
	CapturedKey     string       `json:"captured_key"`
	CurrentKey      string       `json:"current_key"`
	// CurrentKeyCached / CurrentKeyAgeMs: as on Page, the current identity came from
	// the identity cache and was sampled that long ago.
	CurrentKeyCached bool   `json:"current_key_cached"`
	CurrentKeyAgeMs  int64  `json:"current_key_age_ms"`
	Freshness        string `json:"freshness"`
	Stale            bool   `json:"stale"`
	ExpiresAt        string `json:"expires_at"`
}

var chunkPool = sync.Pool{New: func() any { b := make([]byte, SearchChunk); return &b }}

// Search scans one authorized original.
func (s *Store) Search(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	q, err := compileSearch(req, s.limits.PageSize)
	if err != nil {
		return nil, err
	}
	key, err := handleKey(req.Handle)
	if err != nil {
		return nil, err
	}
	wsd, err := s.wsDir(req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	st := s.state(req.WorkspaceID)
	st.mu.Lock()
	f, obs, err := s.openOriginalLocked(req.ReadRequest, filepath.Join(wsd, key), st)
	st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// a read of a range is numbered from its first line, like its projection's
	// "lines A-B omitted" markers
	first := 1
	if fam := obs.Projection.Family; fam != nil && fam.FirstLine > 1 {
		first = fam.FirstLine
	}
	start := req.StartLine
	if start < 1 {
		start = first
	}
	res, err := searchStream(ctx, f, obs.RawBytes, req.Offset, start, q)
	if err != nil {
		return nil, err
	}
	res.FirstLine = first
	res.Handle, res.Tool, res.CallID, res.ContentType = req.Handle, obs.Tool, obs.CallID, obs.ContentType
	res.Pattern, res.Query, res.FromLine, res.ToLine = req.Pattern, req.Query, req.FromLine, req.ToLine
	res.RawSHA256, res.CapturedKey, res.ExpiresAt = obs.RawSHA256, obs.CapturedKey, obs.ExpiresAt
	// as in Read: a capture whose identity is not bound never needs the current
	// identity, so none is read (no repo-key run for an unbound capture)
	var cur Identity
	if obs.CapturedKeyOK && req.RepoKey != nil {
		cur = req.RepoKey(ctx)
	}
	res.CurrentKey, res.CurrentKeyCached, res.CurrentKeyAgeMs = cur.Key, cur.Cached, cur.AgeMs
	switch {
	case !obs.CapturedKeyOK || !cur.Complete || cur.Key == "":
		res.Freshness = "unknown"
	case cur.Key == obs.CapturedKey:
		res.Freshness = "current"
	default:
		res.Freshness = "stale"
	}
	res.Stale = res.Freshness != "current"
	return res, nil
}

// searchQuery is a validated request.
type searchQuery struct {
	re         *regexp.Regexp // nil: a line range read
	from, to   int
	maxMatches int
	context    int
	byteCap    int
}

func compileSearch(req SearchRequest, pageSize int) (*searchQuery, error) {
	q := &searchQuery{from: req.FromLine, to: req.ToLine, maxMatches: req.MaxMatches, context: req.Context, byteCap: max(pageSize, 4<<10)}
	switch {
	case req.Pattern != "" && req.Query != "":
		return nil, fmt.Errorf("%w: pass pattern or query, not both", ErrInvalidSearch)
	case len(req.Pattern) > maxPatternBytes || len(req.Query) > maxPatternBytes:
		return nil, fmt.Errorf("%w: pattern longer than %d bytes", ErrInvalidSearch, maxPatternBytes)
	case req.FromLine < 0 || req.ToLine < 0 || (req.ToLine > 0 && req.ToLine < req.FromLine):
		return nil, fmt.Errorf("%w: line range %d-%d", ErrInvalidSearch, req.FromLine, req.ToLine)
	case req.MaxMatches < 0 || req.MaxMatches > MaxSearchMatches:
		return nil, fmt.Errorf("%w: max_matches must be 1..%d", ErrInvalidSearch, MaxSearchMatches)
	case req.Context < 0 || req.Context > MaxSearchContext:
		return nil, fmt.Errorf("%w: context must be 0..%d", ErrInvalidSearch, MaxSearchContext)
	case req.Offset > 0 && req.StartLine < 1:
		return nil, fmt.Errorf("%w: resuming at an offset needs start_line (the next_line of the previous result)", ErrInvalidSearch)
	}
	switch {
	case req.Pattern != "":
		re, err := regexp.Compile(req.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: pattern: %v", ErrInvalidSearch, err)
		}
		q.re = re
	case req.Query != "":
		q.re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(req.Query))
	case req.FromLine == 0 && req.ToLine == 0:
		return nil, fmt.Errorf("%w: pass a pattern, a query or a line range", ErrInvalidSearch)
	}
	if q.maxMatches == 0 {
		q.maxMatches = DefaultSearchMatches
	}
	return q, nil
}

// searchStream scans r[offset:n) line by line in SearchChunk reads.
func searchStream(ctx context.Context, r io.ReaderAt, n, offset int64, startLine int, q *searchQuery) (*SearchResult, error) {
	if offset < 0 || offset > n {
		return nil, fmt.Errorf("%w: offset %d outside 0..%d", ErrInvalidRange, offset, n)
	}
	res := &SearchResult{Lines: []SearchLine{}, StartOffset: offset, TotalBytes: n, ChunkSize: SearchChunk}
	bp := chunkPool.Get().(*[]byte)
	defer chunkPool.Put(bp)
	buf := *bp
	var carry []byte // a partial line spanning chunks (at most maxSearchLine bytes kept)
	carryStart, carryLen := offset, int64(0)
	line := startLine
	// context: a ring of the previous lines and a count of lines still owed after a match
	var before []SearchLine
	after := 0
	outBytes := 0
	stopped := false
	lastEmitted := -1
	emit := func(l SearchLine) {
		if l.Line <= lastEmitted {
			return
		}
		lastEmitted = l.Line
		res.Lines = append(res.Lines, l)
		outBytes += len(l.Text) + 48
	}
	shown := func(b []byte) (string, bool) {
		b = bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
		cut := len(b) > searchLineBytes
		if cut {
			b = trimRune(b[:searchLineBytes])
		}
		if !utf8.Valid(b) {
			return strings.ToValidUTF8(string(b), "�"), cut
		}
		return string(b), cut
	}
	// handle one complete line (b holds at most maxSearchLine bytes of it)
	handle := func(b []byte, start int64, full int64) {
		res.LinesScanned++
		inRange := (q.from == 0 || line >= q.from) && (q.to == 0 || line <= q.to)
		if full > maxSearchLine {
			res.LongLines++
		}
		capped := res.MatchCapReached || res.ByteCapReached
		switch {
		case !inRange:
		case capped && after > 0 && q.re != nil && q.re.Match(bytes.TrimSuffix(b, []byte("\n"))):
			// a further match inside the owed context: stop before it, so resuming at
			// next_offset counts and flags it
			stopped = true
			return
		case capped && after > 0:
			// owed context after the last match (itself not a match)
			text, cut := shown(b)
			emit(SearchLine{Line: line, Offset: start, Text: text, Truncated: cut})
			after--
			stopped = after == 0
		case q.re == nil:
			text, cut := shown(b)
			emit(SearchLine{Line: line, Offset: start, Text: text, Truncated: cut})
			if len(res.Lines) >= MaxSearchLines || outBytes >= q.byteCap {
				res.ByteCapReached = outBytes >= q.byteCap
				stopped = true
			}
		case q.re.Match(bytes.TrimSuffix(b, []byte("\n"))):
			for _, bl := range before {
				emit(bl)
			}
			before = before[:0]
			text, cut := shown(b)
			emit(SearchLine{Line: line, Offset: start, Text: text, Truncated: cut, Match: true})
			res.Matches++
			after = q.context
			if res.Matches >= q.maxMatches || outBytes >= q.byteCap {
				res.MatchCapReached = res.Matches >= q.maxMatches
				res.ByteCapReached = outBytes >= q.byteCap
				stopped = after == 0
			}
		case after > 0:
			text, cut := shown(b)
			emit(SearchLine{Line: line, Offset: start, Text: text, Truncated: cut})
			after--
		case q.context > 0:
			text, cut := shown(b)
			before = append(before, SearchLine{Line: line, Offset: start, Text: text, Truncated: cut})
			if len(before) > q.context {
				before = before[1:]
			}
		}
		line++
		res.NextOffset = start + full
		res.NextLine = line
	}
	res.NextOffset, res.NextLine = offset, line
	for off := offset; off < n && !stopped; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := r.ReadAt(buf[:min(int64(len(buf)), n-off)], off)
		if m == 0 {
			if err != nil && err != io.EOF {
				return nil, err
			}
			return nil, ErrCorrupt
		}
		chunk := buf[:m]
		res.BytesScanned += int64(m)
		for len(chunk) > 0 && !stopped {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				// partial line: keep up to maxSearchLine bytes of it for matching
				if room := maxSearchLine - len(carry); room > 0 {
					carry = append(carry, chunk[:min(room, len(chunk))]...)
				}
				carryLen += int64(len(chunk))
				chunk = nil
				break
			}
			seg := chunk[:i+1]
			if carryLen > 0 {
				if room := maxSearchLine - len(carry); room > 0 {
					carry = append(carry, seg[:min(room, len(seg))]...)
				}
				handle(carry, carryStart, carryLen+int64(len(seg)))
				carry, carryLen = carry[:0], 0
			} else {
				handle(seg, off+int64(m-len(chunk)), int64(len(seg)))
			}
			chunk = chunk[i+1:]
			carryStart = off + int64(m-len(chunk))
			if q.to > 0 && line > q.to {
				stopped = true
			}
		}
		off += int64(m)
		if q.to > 0 && line > q.to {
			break
		}
	}
	if !stopped && carryLen > 0 {
		handle(carry, carryStart, carryLen) // the last line has no newline
	}
	res.EOF = res.NextOffset >= n
	return res, nil
}
