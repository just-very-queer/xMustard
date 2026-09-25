package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingReaderAt records every read so a test can check the chunking.
type countingReaderAt struct {
	r interface {
		ReadAt([]byte, int64) (int, error)
	}
	mu    sync.Mutex
	sizes []int
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	c.sizes = append(c.sizes, len(p))
	c.mu.Unlock()
	return c.r.ReadAt(p, off)
}

func numberedLines(n int, mark func(i int) bool) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		if mark(i) {
			fmt.Fprintf(&b, "line %06d ERROR needle here\n", i)
		} else {
			fmt.Fprintf(&b, "line %06d quiet\n", i)
		}
	}
	return b.Bytes()
}

// Search-in-original streams SearchChunk (1 MiB) reads with a match cap, and a
// resumed search continues exactly where the capped one stopped.
func TestSearchStreamsChunksWithMatchCap(t *testing.T) {
	raw := numberedLines(300000, func(i int) bool { return i%1000 == 0 }) // ~7 MiB, 300 matches
	cr := &countingReaderAt{r: bytes.NewReader(raw)}
	q, err := compileSearch(SearchRequest{Pattern: `ERROR needle`, MaxMatches: 40}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	res, err := searchStream(context.Background(), cr, int64(len(raw)), 0, 1, q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Matches != 40 || !res.MatchCapReached || res.EOF || len(res.Lines) != 40 {
		t.Fatalf("cap: %d matches, capped %v eof %v", res.Matches, res.MatchCapReached, res.EOF)
	}
	if res.Lines[0].Line != 1000 || res.Lines[39].Line != 40000 || !strings.Contains(res.Lines[0].Text, "line 001000 ERROR") {
		t.Fatalf("matches: first %+v last %+v", res.Lines[0], res.Lines[39])
	}
	if res.NextLine != 40001 || string(raw[res.Lines[39].Offset:res.Lines[39].Offset+11]) != "line 040000" {
		t.Fatalf("resume point/offsets wrong: next line %d", res.NextLine)
	}
	for _, sz := range cr.sizes {
		if sz > SearchChunk {
			t.Fatalf("read of %d bytes exceeds the %d-byte chunk", sz, SearchChunk)
		}
	}
	if int64(len(cr.sizes)) > res.BytesScanned/SearchChunk+1 {
		t.Fatalf("%d reads for %d scanned bytes", len(cr.sizes), res.BytesScanned)
	}
	// resume from next_offset/next_line: the next page starts at the 41st match
	res2, err := searchStream(context.Background(), cr, int64(len(raw)), res.NextOffset, res.NextLine, q)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Lines[0].Line != 41000 || res2.Matches != 40 {
		t.Fatalf("resume: %+v", res2.Lines[0])
	}
	// a full scan without matches holds O(chunk) memory: the chunk buffer is pooled
	// and nothing grows with the original
	none, _ := compileSearch(SearchRequest{Query: "never-present"}, 64<<10)
	big := repeatReaderAt{pat: []byte("a quiet line of evidence output\n"), n: 64 << 20}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res3, err := searchStream(context.Background(), big, big.n, 0, 1, none)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if !res3.EOF || res3.BytesScanned != big.n || res3.Matches != 0 {
		t.Fatalf("full scan: %+v", res3)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; !raceEnabled && alloc > 3<<20 {
		t.Fatalf("scanning 64 MiB allocated %d bytes", alloc)
	}
}

func TestSearchLineRangeContextAndChunkBoundaries(t *testing.T) {
	// a matching line that straddles the 1 MiB chunk boundary, and one long line
	var b bytes.Buffer
	for b.Len() < SearchChunk-10 {
		b.WriteString("filler filler filler\n")
	}
	b.WriteString("straddling the chunk boundary with a NEEDLE inside\n")
	b.WriteString(strings.Repeat("y", 3<<20) + " NEEDLE at the end of a long line\n")
	b.WriteString("after one\nafter two\n")
	raw := b.Bytes()
	lineOf := func(off int) int { return bytes.Count(raw[:off], []byte("\n")) + 1 }
	q, _ := compileSearch(SearchRequest{Query: "needle", Context: 1}, 64<<10)
	res, err := searchStream(context.Background(), bytes.NewReader(raw), int64(len(raw)), 0, 1, q)
	if err != nil {
		t.Fatal(err)
	}
	strad := bytes.Index(raw, []byte("straddling"))
	if res.Matches != 1 || res.LongLines != 1 {
		// the long line's NEEDLE sits past its first 1 MiB: matched lines are
		// examined on their first MiB and the truncation is reported
		t.Fatalf("matches %d long %d: %+v", res.Matches, res.LongLines, res.Lines)
	}
	var m SearchLine
	for _, l := range res.Lines {
		if l.Match {
			m = l
		}
	}
	if m.Line != lineOf(strad) || m.Offset != int64(strad) || !strings.HasPrefix(m.Text, "straddling") {
		t.Fatalf("straddling match: %+v (want line %d offset %d)", m, lineOf(strad), strad)
	}
	if len(res.Lines) != 3 || res.Lines[0].Text != "filler filler filler" || !res.Lines[2].Truncated {
		t.Fatalf("context lines: %+v", res.Lines)
	}
	// a pure line range, line-numbered
	q, _ = compileSearch(SearchRequest{FromLine: 3, ToLine: 5}, 64<<10)
	res, err = searchStream(context.Background(), bytes.NewReader(raw), int64(len(raw)), 0, 1, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 3 || res.Lines[0].Line != 3 || res.Lines[2].Line != 5 || res.NextLine != 6 || res.EOF {
		t.Fatalf("range: %+v next %d", res.Lines, res.NextLine)
	}
	for _, bad := range []SearchRequest{
		{}, {Pattern: "(", MaxMatches: 1}, {Pattern: "a", Query: "b"}, {FromLine: 9, ToLine: 3},
		{Pattern: "a", MaxMatches: 1000}, {Pattern: "a", Context: 9}, {Pattern: strings.Repeat("a", 2000)},
		{Pattern: "a", ReadRequest: ReadRequest{Offset: 10}},
	} {
		if _, err := compileSearch(bad, 64<<10); !errors.Is(err, ErrInvalidSearch) {
			t.Errorf("%+v: want ErrInvalidSearch, got %v", bad, err)
		}
	}
}

// Search is authorized like Read: scope, principal, expiry and revocation fail closed.
func TestSearchFailsClosed(t *testing.T) {
	s, now := testStore(t, func(l *Limits) { l.Retention = time.Hour })
	raw := numberedLines(20000, func(i int) bool { return i == 777 })
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Actor: "alice", AuthEnforced: true,
		Format: FormatRaw, Body: bytes.NewReader(raw), Meta: CaptureMeta{Client: "http", Tool: "Bash"}})
	if err != nil || res.Handle == "" {
		t.Fatalf("capture: %v", err)
	}
	key := &fixedKey{}
	key.set("k1", true)
	req := func(ws, actor string) SearchRequest {
		return SearchRequest{ReadRequest: ReadRequest{WorkspaceID: ws, Handle: res.Handle, Actor: actor, AuthEnforced: true, RepoKey: key.get}, Pattern: "ERROR"}
	}
	got, err := s.Search(context.Background(), req("ws", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Matches != 1 || got.Lines[0].Line != 777 || got.Freshness != "unknown" || !got.Stale || got.RawSHA256 == "" || got.Tool != "Bash" {
		t.Fatalf("search: %+v", got)
	}
	if _, err := s.Search(context.Background(), req("ws", "mallory")); !errors.Is(err, ErrDenied) {
		t.Fatalf("other principal: %v", err)
	}
	if _, err := s.Search(context.Background(), req("ws", "")); !errors.Is(err, ErrDenied) {
		t.Fatalf("anonymous under enforcement: %v", err)
	}
	if _, err := s.Search(context.Background(), req("other", "alice")); !errors.Is(err, ErrMissing) {
		t.Fatalf("other workspace: %v", err)
	}
	bad := req("ws", "alice")
	bad.Handle = "xm1.not-a-handle"
	if _, err := s.Search(context.Background(), bad); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("invalid handle: %v", err)
	}
	later := now.Add(2 * time.Hour)
	s.SetClock(func() time.Time { return later })
	if _, err := s.Search(context.Background(), req("ws", "alice")); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	s.SetClock(func() time.Time { return *now })
	res2, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Actor: "alice", AuthEnforced: true,
		Format: FormatRaw, Body: bytes.NewReader(raw), Meta: CaptureMeta{Client: "http", Tool: "Bash"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ReadRequest{WorkspaceID: "ws", Handle: res2.Handle, Actor: "alice", AuthEnforced: true}); err != nil {
		t.Fatal(err)
	}
	r := req("ws", "alice")
	r.Handle = res2.Handle
	if _, err := s.Search(context.Background(), r); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}
