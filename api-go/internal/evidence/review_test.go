package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func nested(depth int, leaf string) []byte {
	return []byte(strings.Repeat(`{"a":`, depth) + `"` + leaf + `"` + strings.Repeat("}", depth))
}

func captureJSON(t *testing.T, s *Store, ctx context.Context, raw []byte) (*Delivery, error) {
	t.Helper()
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(raw)
	return s.Capture(ctx, sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
}

// Fable evidence F1: nesting made reduction O(depth x size) and uncancellable.
func TestDeepNestingIsLinearBoundedAndCancellable(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.ProjectionTarget = DefaultProjectionTarget })
	leaf := strings.Repeat("x", 1<<20)
	start := time.Now()
	d, err := captureJSON(t, s, context.Background(), nested(400, leaf))
	if err != nil || !d.Reduced {
		t.Fatalf("depth 400: %v", err)
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Fatalf("depth 400 x 1 MiB took %v", el)
	}
	start = time.Now()
	if _, err := captureJSON(t, s, context.Background(), nested(1000, leaf)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nesting beyond the cap must be an explicit unsupported error, got %v", err)
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Fatalf("refusing depth 1000 took %v", el)
	}
	deep := append([]byte(`[`), []byte(strings.Repeat(`{"k":[1,2,3],"v":"`+strings.Repeat("y", 200)+`"},`, 60000))...)
	deep = append(deep[:len(deep)-1], ']')
	sp, _ := s.NewSpool("ws") // prepared before the deadline starts: only Capture is timed
	_, _ = sp.Write(deep)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = s.Capture(ctx, sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
	if el := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || el > 500*time.Millisecond {
		t.Fatalf("a cancelled capture must stop reducing promptly with the context error: err=%v after %v", err, el)
	}
	// one single 16 MiB string: the scan inside the string must also observe cancellation
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write([]byte(`["` + strings.Repeat("z", 16<<20-8) + `"]`))
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	start = time.Now()
	_, err = s.Capture(ctx2, sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
	if el := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || el > 500*time.Millisecond {
		t.Fatalf("cancellation inside one huge string: err=%v after %v", err, el)
	}
}

// Fable evidence F3: JSON kept failure elements only up to the target; every failure
// element must survive up to the hard cap (like text), else an explicit size error.
func TestAllFailureElementsKeptUpToHardCap(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.ProjectionTarget = DefaultProjectionTarget })
	diags := make([]map[string]any, 3000)
	for i := range diags {
		diags[i] = map[string]any{"path": fmt.Sprintf("pkg/f%04d.go", i), "line": i + 1, "severity": "error", "message": "undefined: symbol" + fmt.Sprint(i)}
	}
	raw, _ := json.Marshal(map[string]any{"diagnostics": diags})
	d, err := captureJSON(t, s, context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Diagnostics []map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(d.Projection), &p); err != nil || len(p.Diagnostics) != 3000 {
		t.Fatalf("all 3000 failure elements must be kept within the 1 MiB cap, kept %d (%v)", len(p.Diagnostics), err)
	}
	// more failure evidence than the hard cap: explicit size error, never a silent subset
	s2, _ := testStore(t, func(l *Limits) { l.MaxProjection = 64 << 10 })
	if _, err := captureJSON(t, s2, context.Background(), raw); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("failure evidence over the hard cap: want ErrUnsupported, got %v", err)
	}
}

// Fable evidence F4: object keys were written unbudgeted (~16 MB allocated for a 2 MB
// original). Oversized keys end in an explicit size error without that allocation.
func TestHugeKeysFailFastWithoutUnboundedAllocation(t *testing.T) {
	s, _ := testStore(t, nil)
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, `"%s%04d":1,`, strings.Repeat("k", 4000), i)
	}
	b.WriteString(`"error":"boom"}`)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := captureJSON(t, s, context.Background(), []byte(b.String()))
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Fatalf("refusing a 2 MB original allocated %d bytes", alloc)
	}
}

// Fable evidence F5: unreduced deliveries reported captured_identity "".
func TestPassthroughReportsIdentityUnknown(t *testing.T) {
	s, _ := testStore(t, nil)
	d, err := captureJSON(t, s, context.Background(), []byte(`{"ok":true}`))
	if err != nil || d.CapturedIdentity != "unknown" {
		t.Fatalf("passthrough identity: %q %v", d.CapturedIdentity, err)
	}
}

// Fable evidence F6: a read racing a revoke answered corrupt instead of revoked or data.
func TestReadRacingRevokeIsNeverCorrupt(t *testing.T) {
	s, _ := testStore(t, nil)
	d, _ := capture(t, s, "ws", "", manyResults(400, 3), nil)
	done := make(chan struct{})
	readBeforeRawOpen = func() {
		go func() { _ = s.Revoke(ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); close(done) }()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer func() { readBeforeRawOpen = nil }()
	_, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle})
	<-done // the revoke completes after the read releases the lock
	if err != nil && !errors.Is(err, ErrRevoked) {
		t.Fatalf("read racing revoke must return data or revoked, got %v", err)
	}
	if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("after the revoke, reads must answer revoked, got %v", err)
	}
}

// Root review of salience.go: failure statuses are recognized whatever legal
// whitespace separates key and value (a late array element with long whitespace runs
// must not be silently omitted); only an over-64 KiB separator run or an over-1 MiB
// text line is refused, explicitly.
func TestFailureStatusWithLongLegalWhitespaceIsKept(t *testing.T) {
	s, _ := testStore(t, nil)
	rows := make([]string, 400)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"name":"case%03d","ok":true}`, i)
	}
	rows[321] = `{"name":"case321","ok"` + strings.Repeat(" ", 300) + `:` + strings.Repeat("\n ", 400) + `false}`
	rows[390] = `{"name":"case390","exit_code"` + strings.Repeat("\t", 200) + `:` + strings.Repeat(" ", 200) + `3}`
	d, err := captureJSON(t, s, context.Background(), []byte("["+strings.Join(rows, ",")+"]"))
	if err != nil || !strings.Contains(d.Projection, "case321") || !strings.Contains(d.Projection, "case390") {
		t.Fatalf("JSON failure statuses with long whitespace runs must be kept: %v", err)
	}
	// the failing VALUES survive, not only the keys
	var kept []map[string]any
	if err := json.Unmarshal([]byte(d.Projection), &kept); err != nil {
		t.Fatalf("projection is not valid JSON: %v", err)
	}
	statuses := map[string]any{}
	for _, row := range kept {
		if row["name"] == "case321" {
			statuses["case321"] = row["ok"]
		}
		if row["name"] == "case390" {
			statuses["case390"] = row["exit_code"]
		}
	}
	if statuses["case321"] != false || statuses["case390"] != float64(3) {
		t.Fatalf("failing status values lost: %v", statuses)
	}
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "line %04d \"passed\": true\n", i)
		if i == 1500 {
			b.WriteString(`result "success"` + strings.Repeat("\t", 500) + ":" + strings.Repeat(" ", 500) + "false\n")
		}
	}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write([]byte(b.String()))
	d, err = s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"})
	if err != nil || !regexp.MustCompile(`"success"\s*:\s*false`).MatchString(d.Projection) {
		t.Fatalf("text failure status with long whitespace runs must be kept: %v", err)
	}
	// shapes that cannot be examined within bounds are refused explicitly
	huge := `[{"ok"` + strings.Repeat(" ", 70<<10) + `:false},` + strings.Repeat(`{"a":1},`, 3000) + `{"a":1}]`
	if _, err := captureJSON(t, s, context.Background(), []byte(huge)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("separator run over 64 KiB next to a status key: want ErrUnsupported, got %v", err)
	}
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write([]byte("ok\n" + strings.Repeat("x", 1<<20+10) + "\nok\n"))
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("text line over 1 MiB: want ErrUnsupported, got %v", err)
	}
}

// Root review of reduceText: a failure after 4 KiB of filler on a middle line was
// detected (pass 1 reads 1 MiB) but emitted truncated at 4 KiB, keeping the filler and
// dropping the failure. A line carrying failure evidence is mandatory in whole.
func TestLateFailureOnLongLineIsEmitted(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.ProjectionTarget = DefaultProjectionTarget })
	var b strings.Builder
	for i := 0; i < 20000; i++ { // ~260 KB: well over the 64 KiB target, so it is reduced
		fmt.Fprintf(&b, "ok step %04d\n", i)
		if i == 10000 {
			b.WriteString(strings.Repeat("filler ", 8<<10/7) + "FATAL boom: disk quota exceeded at store.go:88\n")
		}
	}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write([]byte(b.String()))
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Reduced {
		t.Fatalf("fixture must be reduced to test emission")
	}
	if !strings.Contains(d.Projection, "FATAL boom: disk quota exceeded at store.go:88") {
		t.Fatalf("failure bytes after 4 KiB on a line were dropped")
	}
}

// Root review of salience.go: exhausting the failure-evidence position budget must be
// an explicit unsupported error, never a reduction that could miss later failures.
func TestSalienceBudgetExhaustionIsExplicit(t *testing.T) {
	prev := maxSalientPositions
	maxSalientPositions = 50
	defer func() { maxSalientPositions = prev }()
	s, _ := testStore(t, nil)
	rows := make([]string, 400)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"case":%d,"status":"error"}`, i)
	}
	if _, err := captureJSON(t, s, context.Background(), []byte("["+strings.Join(rows, ",")+"]")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("salience budget exhaustion: want ErrUnsupported, got %v", err)
	}
}
