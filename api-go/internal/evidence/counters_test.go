package evidence

import (
	"bytes"
	"testing"

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
