package injection

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxJSONText bounds the text ScanText reads as JSON. Validating and walking a longer
// document costs more than its scan, of which at most MaxScanBytes is read anyway.
const maxJSONText = 2 * MaxScanBytes

// ScanText scans text xMustard delivers as a tool result. A JSON document, or a stream
// of them (one per line, say), is scanned as the text it encodes: every member name with
// its quotes and colon (so a forged hook key still reads as one), and every string value
// decoded, so the escapes \n, \" and < read as the characters they stand for; each
// name and value is its own paragraph. Other text, and JSON longer than maxJSONText, is
// scanned as it is. Report.Scanned then counts the decoded text.
func ScanText(text string) Report {
	if decoded, ok := jsonText(text); ok {
		return Scan(decoded)
	}
	return Scan(text)
}

// jsonText returns the member names and decoded string values of text, in order and
// separated by paragraph breaks, when text is a stream of JSON objects or arrays. It
// stops once it holds more than MaxScanBytes. ok is false for any other text.
func jsonText(text string) (decoded string, ok bool) {
	t := strings.TrimLeft(text, " \t\r\n")
	if len(t) > maxJSONText || t == "" || t[0] != '{' && t[0] != '[' || !validJSONStream(t) {
		return "", false
	}
	buf := make([]byte, 0, min(len(t), MaxScanBytes+1))
	// t is valid JSON: outside a string, a quote opens one
	for i := 0; len(buf) <= MaxScanBytes; {
		open := strings.IndexByte(t[i:], '"')
		if open < 0 {
			break
		}
		open += i
		end := jsonStringEnd(t, open)
		if len(buf) > 0 {
			buf = append(buf, '\n', '\n')
		}
		next := strings.TrimLeft(t[end+1:], " \t\r\n")
		if !strings.HasPrefix(next, ":") {
			buf = appendUnquoted(buf, t[open+1:end]) // a string value
			i = end + 1
			continue
		}
		// a member name, quoted, and its colon
		buf = append(buf, '"')
		buf = appendUnquoted(buf, t[open+1:end])
		buf = append(buf, '"', ':')
		i = len(t) - len(next) + 1
	}
	return string(buf), true
}

// validJSONStream reports whether t is one JSON value or a stream of them.
func validJSONStream(t string) bool {
	if json.Valid([]byte(t)) {
		return true
	}
	dec := json.NewDecoder(strings.NewReader(t))
	for {
		var v json.RawMessage
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// jsonStringEnd returns the index of the quote that closes the JSON string opening at
// t[open]: the next quote not escaped by an odd run of backslashes. t is valid JSON.
func jsonStringEnd(t string, open int) int {
	for j := open + 1; ; {
		k := j + strings.IndexByte(t[j:], '"')
		backslashes := 0
		for t[k-1-backslashes] == '\\' {
			backslashes++
		}
		if backslashes%2 == 0 {
			return k
		}
		j = k + 1
	}
}

// jsonEscapes maps the byte after a backslash in a JSON string to the byte it stands
// for (\u is decoded apart).
var jsonEscapes = [256]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

// appendUnquoted appends the decoded body of a valid JSON string literal (the text
// between its quotes). A lone surrogate decodes to U+FFFD, as encoding/json has it.
func appendUnquoted(dst []byte, body string) []byte {
	for {
		k := strings.IndexByte(body, '\\')
		if k < 0 {
			return append(dst, body...)
		}
		dst, body = append(dst, body[:k]...), body[k+1:]
		if body[0] != 'u' {
			dst, body = append(dst, jsonEscapes[body[0]]), body[1:]
			continue
		}
		r := hex4(body[1:5])
		body = body[5:]
		if utf16.IsSurrogate(r) && len(body) >= 6 && body[0] == '\\' && body[1] == 'u' {
			if pair := utf16.DecodeRune(r, hex4(body[2:6])); pair != utf8.RuneError {
				r, body = pair, body[6:]
			}
		}
		dst = utf8.AppendRune(dst, r) // a lone surrogate is written as U+FFFD
	}
}

// hex4 decodes four hex digits (valid JSON guarantees them).
func hex4(s string) rune {
	var r rune
	for i := range 4 {
		c := rune(s[i])
		switch {
		case c <= '9':
			c -= '0'
		case c >= 'a':
			c -= 'a' - 10
		default:
			c -= 'A' - 10
		}
		r = r<<4 | c
	}
	return r
}
