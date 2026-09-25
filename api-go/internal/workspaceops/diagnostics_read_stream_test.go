package workspaceops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestJSONStreamStringParity(t *testing.T) {
	cases := map[string]string{
		"plain":              "diagnostic message",
		"quotes and slash":   "\"\\",
		"short controls":     "\b\f\n\r\t",
		"other controls":     string([]byte{0, 1, 0x1f, 0x7f}),
		"html":               "<>&",
		"line separators":    "\u2028\u2029",
		"invalid utf8":       string([]byte{0xff, 0xc3, '('}),
		"split rune":         strings.Repeat("a", diagnosticsStreamScratchBytes-1) + "🧡",
		"split separator":    strings.Repeat("a", diagnosticsStreamScratchBytes-1) + "\u2028",
		"split invalid utf8": strings.Repeat("a", diagnosticsStreamScratchBytes-1) + string([]byte{0xe2, 0x80}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			want, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			stream := newJSONStream(&out)
			stream.str(input)
			if err := stream.flush(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("JSON string mismatch at byte %d: got %q, want %q", firstDifferentByte(out.Bytes(), want), out.Bytes(), want)
			}
		})
	}
}

func TestJSONStreamRawMessageParity(t *testing.T) {
	raw := []byte("{\n  \"value\": \"<>&\u2028\u2029\"\n}")
	want, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stream := newJSONStream(&out)
	stream.htmlEscapedRaw(compact.Bytes())
	if err := stream.flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("raw payload mismatch: got %q, want %q", out.Bytes(), want)
	}
}

type diagnosticsShortWriter struct{}

func (diagnosticsShortWriter) Write(p []byte) (int, error) { return min(len(p), 1), nil }

func TestJSONStreamShortWrite(t *testing.T) {
	stream := newJSONStream(diagnosticsShortWriter{})
	stream.str("message")
	if err := stream.flush(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
}

func firstDifferentByte(a, b []byte) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return len(a)
}
