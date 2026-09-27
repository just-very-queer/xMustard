package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// failingLog is a JSON-free log of n bytes whose last line names the failure.
func failingLog(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n-64; i++ {
		fmt.Fprintf(&b, "=== RUN TestCase%06d\n--- PASS: TestCase%06d (0.00s)\n", i, i)
	}
	b.WriteString("--- FAIL: TestLast (0.01s)\nFAIL\n")
	return b.Bytes()
}

func captureText(t *testing.T, s *Store, ws, actor string, raw []byte) *Delivery {
	t.Helper()
	sp, err := s.NewSpool(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Write(raw); err != nil {
		t.Fatal(err)
	}
	d, err := s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: ws, Actor: actor, AuthEnforced: actor != "",
		Tool: "Bash", ContentType: "text/plain", Status: 1, IsError: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Handle == "" {
		t.Fatalf("a %d-byte original is retained", len(raw))
	}
	return d
}

// Tail returns exactly the last bytes of the original, with its metadata.
func TestTailReadsTheEndOfTheOriginal(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := failingLog(3 << 20)
	d := captureText(t, s, "ws", "alice", raw)
	tail, err := s.Tail(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "alice", AuthEnforced: true}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tail.Data, raw[len(raw)-1<<20:]) || tail.Offset != int64(len(raw)-1<<20) || tail.TotalBytes != int64(len(raw)) {
		t.Fatalf("tail offset %d total %d len %d", tail.Offset, tail.TotalBytes, len(tail.Data))
	}
	if !tail.IsError || tail.Tool != "Bash" || tail.RawSHA256 != d.RawSHA256 || !bytes.HasSuffix(tail.Data, []byte("FAIL\n")) {
		t.Fatalf("tail metadata = %+v", tail)
	}
	// a window larger than the original returns the whole original
	whole, err := s.Tail(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "alice", AuthEnforced: true}, 64<<20)
	if err != nil || whole.Offset != 0 || len(whole.Data) != len(raw) {
		t.Fatalf("whole tail: offset %d len %d err %v", whole.Offset, len(whole.Data), err)
	}
}

// Tail applies Read's scope checks: another principal, another workspace, an expired
// or revoked original, and a malformed handle are all refused.
func TestTailEnforcesReadScope(t *testing.T) {
	s, now := testStore(t, nil)
	d := captureText(t, s, "ws", "alice", failingLog(256<<10))
	req := ReadRequest{WorkspaceID: "ws", Handle: d.Handle, Actor: "alice", AuthEnforced: true}
	cases := map[string]struct {
		mutate func(*ReadRequest)
		want   error
	}{
		"other principal": {func(r *ReadRequest) { r.Actor = "bob" }, ErrDenied},
		"other workspace": {func(r *ReadRequest) { r.WorkspaceID = "ws2" }, ErrMissing},
		"bad handle":      {func(r *ReadRequest) { r.Handle = "xm1.nope" }, ErrInvalidHandle},
	}
	for name, c := range cases {
		r := req
		c.mutate(&r)
		if _, err := s.Tail(context.Background(), r, 1<<20); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
	*now = now.Add(DefaultRetention + time.Minute)
	if _, err := s.Tail(context.Background(), req, 1<<20); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	d2 := captureText(t, s, "ws", "alice", failingLog(256<<10))
	req.Handle = d2.Handle
	if err := s.Revoke(req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tail(context.Background(), req, 1<<20); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
}

// Reading the tail of a 16 MiB original allocates about the window, never the
// original (PAR-RT-11: no whole-file read on the why_failed path).
func TestTailAllocatesTheWindowNotTheOriginal(t *testing.T) {
	s, _ := testStore(t, nil)
	raw := failingLog(16 << 20)
	d := captureText(t, s, "ws", "", raw)
	raw = nil
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	tail, err := s.Tail(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: d.Handle}, 1<<20)
	runtime.ReadMemStats(&after)
	if err != nil || len(tail.Data) != 1<<20 {
		t.Fatalf("tail: %d bytes, %v", len(tail.Data), err)
	}
	grew := after.TotalAlloc - before.TotalAlloc
	if grew > 2<<20 {
		t.Fatalf("a 1 MiB tail of a 16 MiB original allocated %d bytes", grew)
	}
	t.Logf("a 1 MiB tail of a 16 MiB original allocated %d KiB", grew>>10)
}
