package workspaceops

import (
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"
)

// diagnosticsStreamScratchBytes is the one reusable output buffer a streamed
// diagnostics read writes through; nothing larger is accumulated for the body.
const diagnosticsStreamScratchBytes = 64 << 10

// jsonStream writes JSON through a fixed scratch buffer, reproducing the bytes
// encoding/json emits with HTML escaping on (json.Encoder's default). Strings are
// escaped incrementally from their decoded form, so a row's encoded size never has
// to fit in memory at once. The first write error sticks; later writes are no-ops.
type jsonStream struct {
	w       io.Writer
	buf     []byte
	written int64 // bytes handed to w (response bytes, a metric)
	err     error
}

func newJSONStream(w io.Writer) *jsonStream {
	return &jsonStream{w: w, buf: make([]byte, 0, diagnosticsStreamScratchBytes)}
}

func (s *jsonStream) flush() error {
	if s.err == nil && len(s.buf) > 0 {
		var n int
		n, s.err = s.w.Write(s.buf)
		s.written += int64(n)
		if s.err == nil && n != len(s.buf) {
			s.err = io.ErrShortWrite
		}
	}
	s.buf = s.buf[:0]
	return s.err
}

func (s *jsonStream) byte(c byte) {
	if len(s.buf) == cap(s.buf) {
		s.flush()
	}
	s.buf = append(s.buf, c)
}

func (s *jsonStream) raw(p []byte) {
	for len(p) > 0 {
		if len(s.buf) == cap(s.buf) {
			s.flush()
		}
		n := copy(s.buf[len(s.buf):cap(s.buf)], p)
		s.buf = s.buf[:len(s.buf)+n]
		p = p[n:]
	}
}

func (s *jsonStream) rawString(p string) {
	for len(p) > 0 {
		if len(s.buf) == cap(s.buf) {
			s.flush()
		}
		n := copy(s.buf[len(s.buf):cap(s.buf)], p)
		s.buf = s.buf[:len(s.buf)+n]
		p = p[n:]
	}
}

// marshal writes one bounded value exactly as json.Marshal encodes it.
func (s *jsonStream) marshal(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.raw(b)
	return nil
}

func (s *jsonStream) int(v int) {
	var tmp [24]byte
	s.raw(strconv.AppendInt(tmp[:0], int64(v), 10))
}

const jsonHex = "0123456789abcdef"

// str writes v as a JSON string, byte-for-byte what json.Marshal(v) produces:
// short escapes for \b \f \n \r \t, \u00XX for other control bytes and for < > &,
// \ufffd for each invalid UTF-8 byte, and U+2028/U+2029 escaped.
func (s *jsonStream) str(v string) {
	s.byte('"')
	start := 0
	for i := 0; i < len(v); {
		if c := v[i]; c < utf8.RuneSelf {
			if jsonSafeHTML(c) {
				i++
				continue
			}
			s.rawString(v[start:i])
			switch c {
			case '\\', '"':
				s.byte('\\')
				s.byte(c)
			case '\b':
				s.rawString(`\b`)
			case '\f':
				s.rawString(`\f`)
			case '\n':
				s.rawString(`\n`)
			case '\r':
				s.rawString(`\r`)
			case '\t':
				s.rawString(`\t`)
			default:
				s.rawString(`\u00`)
				s.byte(jsonHex[c>>4])
				s.byte(jsonHex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(v[i:])
		if r == utf8.RuneError && size == 1 {
			s.rawString(v[start:i])
			s.rawString(`\ufffd`)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			s.rawString(v[start:i])
			s.rawString(`\u202`)
			s.byte(jsonHex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	s.rawString(v[start:])
	s.byte('"')
}

// jsonSafeHTML reports whether an ASCII byte is written verbatim inside a JSON string
// when HTML escaping is on (encoding/json's htmlSafeSet).
func jsonSafeHTML(c byte) bool {
	return c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&'
}

// htmlEscapedRaw writes already-compact JSON with <, >, &, U+2028 and U+2029
// escaped, as json.HTMLEscape does and as json.Encoder writes a json.RawMessage.
func (s *jsonStream) htmlEscapedRaw(src []byte) {
	start := 0
	for i, c := range src {
		if c == '<' || c == '>' || c == '&' {
			s.raw(src[start:i])
			s.rawString(`\u00`)
			s.byte(jsonHex[c>>4])
			s.byte(jsonHex[c&0xF])
			start = i + 1
		}
		// U+2028 is E2 80 A8, U+2029 is E2 80 A9.
		if c == 0xE2 && i+2 < len(src) && src[i+1] == 0x80 && src[i+2]&^1 == 0xA8 {
			s.raw(src[start:i])
			s.rawString(`\u202`)
			s.byte(jsonHex[src[i+2]&0xF])
			start = i + len("\u2029")
		}
	}
	s.raw(src[start:])
}

func (s *jsonStream) optStr(key string, v *string) {
	if v != nil {
		s.rawString(key)
		s.str(*v)
	}
}

// diagnosticRecord writes one row in DiagnosticRecord's field order with its
// omitempty rules. Nested link objects (PostgreSQL-only; local rows never carry
// them) are bounded and use json.Marshal.
func (s *jsonStream) diagnosticRecord(r *DiagnosticRecord) error {
	s.rawString(`{"workspace_id":`)
	s.str(r.WorkspaceID)
	s.rawString(`,"diagnostic_run_id":`)
	s.str(r.DiagnosticRunID)
	s.rawString(`,"path":`)
	s.str(r.Path)
	s.rawString(`,"range_start_line":`)
	s.int(r.RangeStartLine)
	s.rawString(`,"range_start_column":`)
	s.int(r.RangeStartColumn)
	s.rawString(`,"range_end_line":`)
	s.int(r.RangeEndLine)
	s.rawString(`,"range_end_column":`)
	s.int(r.RangeEndColumn)
	s.rawString(`,"severity":`)
	s.str(r.Severity)
	s.rawString(`,"message":`)
	s.str(r.Message)
	s.rawString(`,"source_kind":`)
	s.str(r.SourceKind)
	s.rawString(`,"source_name":`)
	s.str(r.SourceName)
	s.optStr(`,"rule_code":`, r.RuleCode)
	s.rawString(`,"fingerprint":`)
	s.str(r.Fingerprint)
	s.optStr(`,"head_sha":`, r.HeadSHA)
	s.optStr(`,"content_hash":`, r.ContentHash)
	s.rawString(`,"link_status":`)
	s.str(r.LinkStatus)
	if r.LinkedSymbol != nil {
		s.rawString(`,"linked_symbol":`)
		if err := s.marshal(r.LinkedSymbol); err != nil {
			return err
		}
	}
	if r.LinkContext != nil {
		s.rawString(`,"link_context":`)
		if err := s.marshal(r.LinkContext); err != nil {
			return err
		}
	}
	if r.IdentityStatus != "" {
		s.rawString(`,"identity_status":`)
		s.str(r.IdentityStatus)
	}
	s.rawString(`,"generated_at":`)
	s.str(r.GeneratedAt)
	s.byte('}')
	return nil
}
