package workspaceops

import (
	"bytes"
	"strings"
	"testing"
)

// boundedTail retains only the last `max` bytes while counting the total, so a
// chatty run can't grow the heap unbounded (XM-POST-005).
func TestBoundedTailCapsButCountsTotal(t *testing.T) {
	b := &boundedTail{max: 1024}
	// write 100 KiB in 1 KiB chunks
	chunk := bytes.Repeat([]byte("x"), 1024)
	for i := 0; i < 100; i++ {
		_, _ = b.Write(chunk)
	}
	if len(b.buf) > b.max {
		t.Fatalf("retained %d bytes, must be <= max %d", len(b.buf), b.max)
	}
	if b.total != int64(100*1024) {
		t.Fatalf("total must count all bytes, got %d", b.total)
	}
	if !b.truncated {
		t.Fatal("truncated flag must be set")
	}
	// a single huge write keeps only the tail
	b2 := &boundedTail{max: 16}
	_, _ = b2.Write([]byte(strings.Repeat("ab", 1000)))
	if len(b2.String()) != 16 || !b2.truncated {
		t.Fatalf("single huge write: want 16-byte tail truncated, got %d", len(b2.String()))
	}
}
