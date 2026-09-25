package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Root review (1): per-workspace counters shared one map across different workspace
// locks. Run with -race: concurrent capture/retained/revoke over many workspaces.
func TestConcurrentWorkspacesAreRaceFree(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := manyResults(300, 1)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		for k := 0; k < 4; k++ {
			wg.Add(1)
			go func(ws string) {
				defer wg.Done()
				d, err := capture(t, s, ws, "", raw, nil)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = s.Retained(ws)
				_ = s.Revoke(ReadRequest{WorkspaceID: ws, Handle: d.Handle})
			}(fmt.Sprintf("ws%d", w))
		}
	}
	wg.Wait()
}

// Root review (2): a truncated raw.bin must fail closed, never serve zero padding
// labeled with the original length and hash.
func TestTruncatedOriginalFailsClosed(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.PageSize = 1000 })
	raw := manyResults(300, 1)
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := handleKey(d.Handle)
	rawPath := filepath.Join(s.root, "ws", key, "raw.bin")
	if err := os.Truncate(rawPath, int64(len(raw))/2); err != nil {
		t.Fatal(err)
	}
	for _, off := range []int64{0, int64(len(raw)) - 500} {
		_, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Offset: off})
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("read at %d of a truncated original: want ErrCorrupt, got %v", off, err)
		}
	}
}

// Root review (3): expired but unread originals must not block admission, and the
// quota must include pending spool bytes, never evicting unexpired originals.
func TestQuotaReclaimsExpiredAndCountsPendingSpools(t *testing.T) {
	raw := manyResults(300, 1)
	s, now := testStore(t, func(l *Limits) { l.WorkspaceQuota = int64(len(raw)) + 100; l.Retention = time.Hour })
	if _, err := capture(t, s, "ws", "", raw, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(t, s, "ws", "", raw, nil); !errors.Is(err, ErrQuotaFull) {
		t.Fatalf("second capture within retention must be quota-full, got %v", err)
	}
	later := now.Add(2 * time.Hour)
	s.SetClock(func() time.Time { return later })
	if _, err := capture(t, s, "ws", "", raw, nil); err != nil {
		t.Fatalf("expired original must be reclaimed at admission without an explicit read: %v", err)
	}
	// pending spools count: a spool may not grow past the remaining quota
	s2, _ := testStore(t, func(l *Limits) { l.WorkspaceQuota = 10000 })
	a, _ := s2.NewSpool("ws")
	b, _ := s2.NewSpool("ws")
	_, _ = a.Write(bytes.Repeat([]byte("a"), 7000))
	_, _ = b.Write(bytes.Repeat([]byte("b"), 7000))
	if !b.QuotaFull() || a.QuotaFull() {
		t.Fatalf("concurrent spools must share the workspace quota: a=%v b=%v", a.QuotaFull(), b.QuotaFull())
	}
	if _, err := s2.Capture(context.Background(), b, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "text/plain"}); !errors.Is(err, ErrQuotaFull) {
		t.Fatalf("spool past quota must be rejected explicitly, got %v", err)
	}
	a.Discard()
	if n := s2.pendingBytes("ws"); n != 0 {
		t.Fatalf("discarded spools must release pending bytes, got %d", n)
	}
}

// Root review (4): a failure element larger than the projection target must still be
// represented (reduced), not dropped in favor of small passing rows; a failure near a
// large string's tail must survive; invalid structured output must not silently fall
// back to text.
func TestLargeFailureElementAndTailFailureSurvive(t *testing.T) {
	s, _ := testStore(t, nil) // 4 KiB target
	rows := []any{}
	for i := 0; i < 50; i++ {
		rows = append(rows, map[string]any{"name": fmt.Sprintf("t%02d", i), "status": "pass"})
	}
	rows[31] = map[string]any{"name": "t31", "status": "FAILED", "log": strings.Repeat("noise line\n", 2000) + "FATAL: disk quota exceeded at store.go:88"}
	raw, _ := json.Marshal(map[string]any{"results": rows})
	d, err := capture(t, s, "ws", "", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(d.Projection)) {
		t.Fatalf("projection must stay valid JSON")
	}
	for _, want := range []string{`"t31"`, "FAILED", "FATAL: disk quota exceeded"} {
		if !strings.Contains(d.Projection, want) {
			t.Fatalf("projection lost %q:\n%s", want, d.Projection)
		}
	}
	if d.ProjectedBytes > s.limits.ProjectionTarget+512 {
		t.Fatalf("projection %d far over target", d.ProjectedBytes)
	}
}

func TestInvalidStructuredOutputIsExplicit(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.MaxProjection = 8 << 10 })
	bad := []byte(`{"hits":[` + strings.Repeat(`{"a":1},`, 4000)) // truncated JSON, ~32 KB
	if len(bad) <= s.limits.MaxProjection {
		t.Fatalf("fixture must exceed the %d-byte projection cap, is %d", s.limits.MaxProjection, len(bad))
	}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(bad)
	_, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("invalid JSON over the projection cap: want ErrUnsupported, got %v", err)
	}
	small := []byte(`{"hits":[{"a":1},`)
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write(bytes.Repeat(small, 300))
	s.limits.MaxProjection = 1 << 20
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
	if err != nil || d.Reduced || d.ProjectionMode != "unsupported_passthrough" {
		t.Fatalf("invalid JSON within the cap must pass unchanged and be labeled: %v %+v", err, d)
	}
	// the unchanged bytes must survive the JSON wire exactly
	wire, _ := json.Marshal(d)
	var back Delivery
	if err := json.Unmarshal(wire, &back); err != nil || back.Projection != string(bytes.Repeat(small, 300)) {
		t.Fatalf("passthrough bytes changed on the JSON wire")
	}
	// binary / non-UTF-8 output would be rewritten by JSON (U+FFFD): refuse explicitly,
	// at any size
	for _, bin := range [][]byte{
		append([]byte{0, 1, 2, 0xff}, bytes.Repeat([]byte{0}, 9000)...),
		append(bytes.Repeat([]byte("text "), 1000), 0xff, 0xfe),
	} {
		sp, _ = s.NewSpool("ws")
		_, _ = sp.Write(bin)
		if _, err = s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/octet-stream"}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("binary output must be refused explicitly, got %v", err)
		}
	}
}

// Root review (5): identity must be bound around execution; a change during execution
// (or a missing before-identity) makes freshness unknown, never current.
func TestIdentityChangedDuringExecutionIsUnknown(t *testing.T) {
	s, _ := testStore(t, nil)
	key := &fixedKey{key: "after", ok: true}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(manyResults(300, 1))
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json",
		BeforeKey: &Identity{Key: "before", Complete: true}, RepoKey: key.get})
	if err != nil {
		t.Fatal(err)
	}
	if d.CapturedIdentity != "unknown" {
		t.Fatalf("identity changed during execution must be unknown, got %q", d.CapturedIdentity)
	}
	p, _ := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, RepoKey: key.get})
	if p.Freshness != "unknown" || !p.Stale {
		t.Fatalf("page freshness must stay unknown: %+v", p)
	}
	// no before identity (e.g. a result posted after the fact): unknown too
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write(manyResults(300, 1))
	d, _ = s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json", RepoKey: key.get})
	if d.CapturedIdentity != "unknown" {
		t.Fatalf("missing before-identity must be unknown, got %q", d.CapturedIdentity)
	}
	// agreeing complete identities are bound
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write(manyResults(300, 1))
	d, _ = s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json",
		BeforeKey: &Identity{Key: "after", Complete: true}, RepoKey: key.get})
	if d.CapturedIdentity != "bound" {
		t.Fatalf("agreeing identities must be bound, got %q", d.CapturedIdentity)
	}
}

// Root review (6): module invariants, not caller conventions.
func TestCaptureInvariants(t *testing.T) {
	s, _ := testStore(t, nil)
	sp, _ := s.NewSpool("wsA")
	_, _ = sp.Write(manyResults(300, 1))
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "wsB", Tool: "search"}); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("spool/workspace mismatch must be rejected, got %v", err)
	}
	sp, _ = s.NewSpool("wsA")
	_, _ = sp.Write(manyResults(300, 1))
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "wsA", Tool: "search", AuthEnforced: true}); !errors.Is(err, ErrDenied) {
		t.Fatalf("enforced auth without a stable actor must be rejected, got %v", err)
	}
}

// wireRoundTrip asserts a delivery's projection survives JSON marshal/unmarshal.
func wireRoundTrip(t *testing.T, d *Delivery) {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var back Delivery
	if err := json.Unmarshal(b, &back); err != nil || back.Projection != d.Projection {
		t.Fatalf("projection changed on the JSON wire")
	}
}

// Root review: invalid UTF-8 below the target used to pass unchanged and be rewritten
// by JSON; above the target it must be reduced to wire-safe text with the exact bytes
// recoverable.
func TestInvalidBytesBelowAndAboveTargetOverTheWire(t *testing.T) {
	s, _ := testStore(t, nil) // 4 KiB target
	small := []byte("ok line\n\xff\xfe broken\n")
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(small)
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("small non-UTF-8 output must be refused explicitly, got %v", err)
	}
	big := append(bytes.Repeat([]byte("plain line ok\n"), 1000), []byte("FAILED \xff\xfe here\n")...)
	sp, _ = s.NewSpool("ws")
	_, _ = sp.Write(big)
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"})
	if err != nil || d.Handle == "" {
		t.Fatalf("large text with a later invalid line: %v", err)
	}
	wireRoundTrip(t, d)
	if !strings.Contains(d.Projection, "FAILED") {
		t.Fatalf("failure line lost")
	}
	if got, _ := readAll(t, s, "ws", "", d.Handle, nil); !bytes.Equal(got, big) {
		t.Fatalf("exact bytes not recoverable")
	}
	// every other successful delivery shape also survives the wire
	for _, raw := range [][]byte{manyResults(400, 3), []byte(`{"small":true}`)} {
		d, err := capture(t, s, "ws", "", raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		wireRoundTrip(t, d)
	}
}

// Root review: long passing head lines exhausted the budget before a later FATAL line.
// Mandatory failure regions are selected before head/tail.
func TestFailureAfterLongPassingHeadLinesSurvives(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.ProjectionTarget = DefaultProjectionTarget })
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "PASS test_%02d %s\n", i, strings.Repeat("x", 4<<10))
	}
	b.WriteString("FATAL: checksum mismatch in segment 9\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "PASS short_%04d\n", i)
	}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write([]byte(b.String()))
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain", IsError: true, Status: 500})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Projection, "FATAL: checksum mismatch in segment 9") {
		t.Fatalf("failure line lost behind long passing head lines")
	}
	if d.ProjectedBytes > DefaultProjectionTarget {
		t.Fatalf("projection %d over target", d.ProjectedBytes)
	}
	wireRoundTrip(t, d)
}

// Required failure evidence that cannot fit the hard projection cap is an explicit
// error, never a success-looking projection without it.
func TestRequiredEvidenceBeyondHardCapIsExplicit(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.MaxProjection = 8 << 10 })
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "ERROR case %04d failed with a long diagnostic message about the failure\n", i)
	}
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write([]byte(b.String()))
	if _, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "why_failed", ContentType: "text/plain"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want explicit ErrUnsupported, got %v", err)
	}
}

// Root checklist: a cancelled capture never issues or retains a handle, and releases
// its pending quota.
func TestCancelledCaptureIssuesNoHandle(t *testing.T) {
	s, _ := testStore(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sp, _ := s.NewSpool("ws")
	_, _ = sp.Write(manyResults(300, 1))
	d, err := s.Capture(ctx, sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", ContentType: "application/json"})
	if !errors.Is(err, context.Canceled) || d != nil {
		t.Fatalf("pre-cancelled capture: want context.Canceled and no delivery, got %v %+v", err, d)
	}
	if used, _ := s.Retained("ws"); used != 0 || s.PendingBytes("ws") != 0 {
		t.Fatalf("cancelled capture retained %d / pending %d bytes", used, s.PendingBytes("ws"))
	}
	ents, _ := os.ReadDir(filepath.Join(s.root, "ws"))
	if len(ents) != 0 {
		t.Fatalf("cancelled capture left %d entries", len(ents))
	}
}
