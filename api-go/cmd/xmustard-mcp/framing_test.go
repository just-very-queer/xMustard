package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestReadBoundedLineNormal(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("{\"jsonrpc\":\"2.0\"}\n"), 64<<10)
	line, truncated, err := readBoundedLine(r)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if truncated {
		t.Fatal("normal line must not be truncated")
	}
	if !strings.Contains(string(line), "jsonrpc") {
		t.Fatalf("unexpected line: %q", line)
	}
}

// A newline-less oversized message must be bounded (not OOM) and reported truncated.
func TestReadBoundedLineCapsHugeMessage(t *testing.T) {
	huge := bytes.Repeat([]byte("a"), maxMessageBytes+4096)
	huge = append(huge, '\n')
	r := bufio.NewReaderSize(bytes.NewReader(huge), 64<<10)
	line, truncated, _ := readBoundedLine(r)
	if !truncated {
		t.Fatal("oversized message must be reported truncated")
	}
	if len(line) > maxMessageBytes {
		t.Fatalf("retained bytes (%d) must not exceed the cap (%d)", len(line), maxMessageBytes)
	}
}
