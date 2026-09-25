package evidence

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"xmustard/api-go/internal/budget"
)

// PAR-EVAL-04: every capture is counted with its original size, and the bytes hashed to
// fingerprint the original are counted too, whether or not anything is retained.
func TestCaptureIncrementsDataMovementCounters(t *testing.T) {
	s, _ := testStore(t, nil)
	before := budget.Counters()
	small := []byte(`{"hits":[]}`)
	if _, err := capture(t, s, "ws", "", small, nil); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte(`{"kind":"symbol","name":"ok","path":"src/f.go"},`), 20000)
	big = append(append([]byte(`{"hits":[`), big...), []byte(`{}]}`)...)
	d, err := capture(t, s, "ws", "", big, nil)
	if err != nil || !d.Reduced || d.Handle == "" {
		t.Fatalf("large capture must be reduced and retained: err=%v reduced=%v", err, d != nil && d.Reduced)
	}
	after := budget.Counters()
	raw := int64(len(small) + len(big))
	if got := after.Captures - before.Captures; got != 2 {
		t.Fatalf("captures counted %d, want 2", got)
	}
	if got := after.CaptureBytes - before.CaptureBytes; got != raw {
		t.Fatalf("capture bytes counted %d, want %d", got, raw)
	}
	if got := after.BytesHashed - before.BytesHashed; got != raw {
		t.Fatalf("hashed bytes counted %d, want %d", got, raw)
	}
}

// No capture path waits on the heavy slot: work a capture triggers (here, identity
// sampling) runs under a WithoutHeavyWait context, so heavy admission from it fails at
// once while heavy work holds the slot instead of waiting for the bound.
func TestCaptureNeverWaitsOnHeavySlot(t *testing.T) {
	prev := budget.Gov
	budget.Gov = budget.NewProcessGovernor(budget.GovernorConfig{SoftCeilingBytes: 1 << 40, HeavyWait: 10 * time.Second})
	defer func() { budget.Gov = prev }()
	release, err := budget.AcquireHeavy(context.Background(), "index_writer", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	s, _ := testStore(t, nil)
	sp, err := s.NewSpool("ws")
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte(`{"kind":"symbol","name":"ok","path":"src/f.go"},`), 20000)
	if _, err := sp.Write(append(append([]byte(`{"hits":[`), big...), []byte(`{}]}`)...)); err != nil {
		t.Fatal(err)
	}
	var heavyErr error
	var waited time.Duration
	start := time.Now()
	_, err = s.Capture(context.Background(), sp, CaptureRequest{WorkspaceID: "ws", Tool: "search", Status: 200, ContentType: "application/json",
		RepoKey: func(ctx context.Context) Identity {
			t0 := time.Now()
			if r, e := budget.AcquireHeavy(ctx, "capture_side_work", 0); e == nil {
				r()
			} else {
				heavyErr = e
			}
			waited = time.Since(t0)
			return Identity{}
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(heavyErr, budget.ErrOverloaded) || waited > time.Second || time.Since(start) > 3*time.Second {
		t.Fatalf("capture side work must be refused at once, not wait: err=%v waited=%s", heavyErr, waited)
	}
	if st := budget.Status().HeavySlot; st.QueueLen != 0 || st.RefusedHotPath != 1 {
		t.Fatalf("capture must never queue for the heavy slot: %+v", st)
	}
}

// The universal capture digests the whole request body (hook or raw) before the store
// hashes the decoded original: both count as hashed bytes, and the capture is counted.
func TestObserveCountsBodyDigestAndCapture(t *testing.T) {
	s, _ := testStore(t, nil)
	body := []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"a\nb\n","stderr":""}}`)
	before := budget.Counters()
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatClaude,
		Body: bytes.NewReader(body), Meta: CaptureMeta{Client: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	after := budget.Counters()
	if got := after.Captures - before.Captures; got != 1 {
		t.Fatalf("captures counted %d, want 1", got)
	}
	if got, want := after.BytesHashed-before.BytesHashed, int64(len(body))+res.RawBytes; got != want {
		t.Fatalf("hashed bytes counted %d, want body %d + original %d", got, len(body), res.RawBytes)
	}
}
