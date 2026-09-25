package evidence

import "unicode/utf8"

// Token estimation (PAR-CTX-13). The default is a deterministic bytes/runes
// heuristic shaped after modern BPE vocabularies (o200k/cl100k): it allocates nothing
// and runs in one pass. Exact BPE counting needs a 10–20 MiB vocabulary, so it is
// never resident; an evaluation harness that needs exact counts loads a tokenizer
// in its own process and compares against this estimate.

// TokenEstimator names the heuristic and its version; it is recorded next to every
// estimate so estimates from different rules are never compared silently.
const TokenEstimator = "xm-tokens/1"

// EstimateTokens estimates the BPE tokens of s.
func EstimateTokens(s string) int {
	var st tokenScan
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			st.ascii(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		st.nonASCII(r, size)
		i += size
	}
	return st.finish()
}

// EstimateTokensBytes estimates the BPE tokens of b.
func EstimateTokensBytes(b []byte) int {
	var st tokenScan
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			st.ascii(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		st.nonASCII(r, size)
		i += size
	}
	return st.finish()
}

type runKind uint8

const (
	rkNone runKind = iota
	rkWord
	rkDigit
	rkSpace
	rkPunct
)

// tokenScan accumulates runs: a word costs one token up to 7 letters and one more
// per 4 letters after that (a leading space merges into it); digits one per 3;
// punctuation one per 2 characters; a newline run one; a space or tab run longer
// than one character one per 8; a CJK, kana, Hangul or emoji rune one each; other
// non-ASCII letters count double inside words; invalid bytes one each.
type tokenScan struct {
	tokens int
	kind   runKind
	n      int
	nl     bool
}

func (t *tokenScan) flush() {
	switch t.kind {
	case rkWord:
		t.tokens++
		if t.n > 7 {
			t.tokens += (t.n - 7 + 3) / 4
		}
	case rkDigit:
		t.tokens += (t.n + 2) / 3
	case rkPunct:
		t.tokens += (t.n + 1) / 2
	case rkSpace:
		if t.n > 1 {
			t.tokens += (t.n + 7) / 8
		}
	}
	t.kind, t.n = rkNone, 0
}

func (t *tokenScan) run(k runKind, w int) {
	if t.kind != k {
		t.flush()
		t.kind = k
	}
	t.n += w
}

func (t *tokenScan) ascii(c byte) {
	switch {
	case c == '\n' || c == '\r':
		t.flush()
		if !t.nl {
			t.tokens++
		}
		t.nl = true
		return
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
		t.run(rkWord, 1)
	case c >= '0' && c <= '9':
		t.run(rkDigit, 1)
	case c == ' ':
		t.run(rkSpace, 1)
	case c == '\t':
		t.flush()
		t.tokens++
	default:
		t.run(rkPunct, 1)
	}
	t.nl = false
}

func (t *tokenScan) nonASCII(r rune, size int) {
	t.nl = false
	switch {
	case r == utf8.RuneError && size <= 1:
		t.flush()
		t.tokens++
	case wideRune(r):
		t.flush()
		t.tokens++
	default:
		t.run(rkWord, 2)
	}
}

// wideRune reports scripts BPE vocabularies encode at about one token per character.
func wideRune(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF || // CJK radicals, kana, CJK ideographs
		r >= 0xAC00 && r <= 0xD7AF || // Hangul
		r >= 0xF900 && r <= 0xFAFF ||
		r >= 0x1F000 && r <= 0x1FAFF || // emoji and symbols
		r >= 0x20000 && r <= 0x2FFFF
}

func (t *tokenScan) finish() int {
	t.flush()
	return t.tokens
}
