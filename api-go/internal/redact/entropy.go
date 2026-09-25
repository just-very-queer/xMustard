package redact

import (
	"bytes"
	"math"
)

// Entropy is the Shannon entropy of s in bits per byte.
func Entropy(s string) float64 { return entropy(s) }

func entropy[T ~string | ~[]byte](s T) float64 {
	if len(s) == 0 {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

// LooksRandom reports whether s looks like a generated credential rather than
// a word or identifier: at least 8 bytes, entropy of at least 3.0 bits per byte
// (2.5 below 16 bytes, where the maximum is lower), and a digit or mixed case.
// It gates the generic key/value detections; prefixed token formats do not
// need it.
func LooksRandom(s string) bool { return looksRandom(s) }

func looksRandom[T ~string | ~[]byte](s T) bool {
	if len(s) < 8 {
		return false
	}
	threshold := 3.0
	if len(s) < 16 {
		threshold = 2.5
	}
	if entropy(s) < threshold {
		return false
	}
	return hasDigit(s) || (hasUpper(s) && hasLower(s))
}

func hasDigit[T ~string | ~[]byte](b T) bool {
	for i := 0; i < len(b); i++ {
		if '0' <= b[i] && b[i] <= '9' {
			return true
		}
	}
	return false
}

func hasUpper[T ~string | ~[]byte](b T) bool {
	for i := 0; i < len(b); i++ {
		if 'A' <= b[i] && b[i] <= 'Z' {
			return true
		}
	}
	return false
}

func hasLower[T ~string | ~[]byte](b T) bool {
	for i := 0; i < len(b); i++ {
		if 'a' <= b[i] && b[i] <= 'z' {
			return true
		}
	}
	return false
}

// identifierLike reports a code reference rather than a literal: letters,
// underscores, dots and dollar signs only (password = request.form, token:
// string, secret = self.secret).
func identifierLike(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', c == '_', c == '.', c == '$':
		default:
			return false
		}
	}
	return true
}

var placeholderWords = []string{
	"null", "none", "nil", "undefined", "true", "false", "redacted", "placeholder", "example",
	"dummy", "test", "todo", "tbd", "string", "str", "optional", "required", "secret", "password",
	"token",
}

// isPlaceholder reports a value that stands for a secret without being one:
// empty, a variable reference (${TOKEN}, $TOKEN, %TOKEN%, {{ token }}), an
// angle-bracketed hint, an already-redacted marker, a run of one character
// (****, xxxx), an env-var-style name (YOUR_API_KEY), a YAML block indicator,
// or a stock word.
func isPlaceholder(v []byte) bool {
	s := bytes.TrimSpace(v)
	if len(s) == 0 {
		return true
	}
	for _, w := range placeholderWords {
		if len(s) == len(w) && equalFoldASCII(s, w) {
			return true
		}
	}
	switch string(s) {
	case "|", "|-", "|+", ">-", ">+": // YAML block scalar: the value is on the next lines
		return true
	}
	for _, p := range []string{"${", "$(", "{{", "[REDACTED"} {
		if bytes.HasPrefix(s, []byte(p)) {
			return true
		}
	}
	for _, p := range []string{"<your", "your", "insert", "replace"} {
		if len(s) >= len(p) && equalFoldASCII(s[:len(p)], p) {
			return true
		}
	}
	switch last := s[len(s)-1]; {
	case len(s) > 1 && s[0] == '<' && last == '>':
		return true
	case len(s) > 2 && s[0] == '%' && last == '%':
		return true
	case s[0] == '$' && len(s) > 1 && identifierLike(s[1:]):
		return true
	}
	if len(s) >= 3 && bytes.Count(s, s[:1]) == len(s) {
		return true
	}
	return bytes.IndexByte(s, '_') >= 0 && envNameLike(s)
}

func envNameLike(s []byte) bool {
	for _, c := range s {
		if !('A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_') {
			return false
		}
	}
	return len(s) > 0 && s[0] >= 'A' && s[0] <= 'Z'
}
