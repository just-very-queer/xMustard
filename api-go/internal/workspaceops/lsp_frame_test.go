package workspaceops

import (
	"bufio"
	"strconv"
	"strings"
	"testing"
)

// readLSPFrame must reject an over-large Content-Length BEFORE allocating, so a
// buggy/hostile language server can't drive a multi-GB make([]byte, ...) (XM-PRO-006).
func TestReadLSPFrameBounds(t *testing.T) {
	oversized := bufio.NewReader(strings.NewReader("Content-Length: 999999999999\r\n\r\n"))
	if _, err := readLSPFrame(oversized); err == nil {
		t.Fatal("expected oversized Content-Length to be rejected before allocation")
	}

	body := `{"jsonrpc":"2.0","id":1,"result":null}`
	frame := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	got, err := readLSPFrame(bufio.NewReader(strings.NewReader(frame)))
	if err != nil {
		t.Fatalf("a normal frame must parse: %v", err)
	}
	if string(got) != body {
		t.Fatalf("payload mismatch: got %q", got)
	}
}
