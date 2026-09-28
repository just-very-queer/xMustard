package injection

import (
	"sync"
	"unicode"
	"unicode/utf8"
)

// MaxScanBytes bounds the text one scan reads, across all its parts. It sits above the
// largest projection an MCP client receives (64 KiB) and every push budget (a hook's
// additionalContext is 10,000 characters), so a scan of injected text is complete; a
// longer text is scanned up to the cap and reported with FlagTruncated.
const MaxScanBytes = 128 << 10

// FlagTruncated is reported when the text was longer than MaxScanBytes and only its
// head was scanned. A pushed surface treats it like a match: text it could not check
// is never pushed.
const FlagTruncated = "scan_truncated"

// Report is what a scan found.
type Report struct {
	// Flags are the ids of the rules that matched, in table order, then FlagTruncated
	// when the text was cut. Empty means the scanned text is clean.
	Flags []string `json:"flags,omitempty"`
	// Scanned is how many bytes were read.
	Scanned int `json:"scanned_bytes"`
}

// Clean reports whether nothing was flagged.
func (r Report) Clean() bool { return len(r.Flags) == 0 }

// foldBuffers reuses fold buffers across scans.
var foldBuffers = sync.Pool{New: func() any { return new([]byte) }}

// Scan checks text, given as parts (a title and a body, say), for instruction patterns.
// It reads at most MaxScanBytes in total and folds each part once; the cost is one pass
// over the folded bytes plus a match bounded by the rule's window at each trigger
// occurrence.
func Scan(parts ...string) Report {
	var rep Report
	var hit uint64 // bit i: compiled[i] matched
	room, truncated := MaxScanBytes, false
	bp := foldBuffers.Get().(*[]byte)
	defer foldBuffers.Put(bp)
	for _, p := range parts {
		if len(p) > room {
			p, truncated = cutUTF8(p, room), true
		}
		room -= len(p)
		rep.Scanned += len(p)
		if p != "" {
			*bp = fold((*bp)[:0], p)
			hit = scanFolded(*bp, hit)
		}
	}
	for i, c := range compiled {
		if hit&(1<<i) != 0 {
			rep.Flags = append(rep.Flags, c.id)
		}
	}
	if truncated {
		rep.Flags = append(rep.Flags, FlagTruncated)
	}
	return rep
}

// allRules has a bit set for every compiled rule.
var allRules = uint64(1)<<len(compiled) - 1

// scanFolded adds to hit the rules that match folded text. A byte inside a word starts
// no trigger (word triggers count only at the start of a word), so it is skipped; at
// any other byte, the triggers that start with it are compared, and a rule not yet
// matched is tried from each of its triggers that occurs there.
func scanFolded(f []byte, hit uint64) uint64 {
	prevWord := false
	for i := 0; i < len(f) && hit != allRules; i++ {
		b := f[i]
		word := isWordByte(b)
		if word && prevWord {
			continue
		}
		prevWord = word
		for _, t := range byFirst[b] {
			if hit&(1<<t.rule) != 0 || !hasPrefix(f[i:], t.lit) {
				continue
			}
			c := compiled[t.rule]
			if c.re.Match(f[i:min(len(f), i+c.window)]) {
				hit |= 1 << t.rule
			}
		}
	}
	return hit
}

func hasPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix
}

// fold appends s to dst lowercased, with every run of whitespace written as one space,
// or as "\n\n" when the run holds a paragraph break (two newlines). The text is taken
// to start after a paragraph break, so the output starts with "\n\n". Invalid UTF-8
// bytes are kept as they are.
func fold(dst []byte, s string) []byte {
	space, newlines := true, 2
	flush := func() {
		switch {
		case newlines >= 2:
			dst = append(dst, '\n', '\n')
		case space:
			dst = append(dst, ' ')
		}
		space, newlines = false, 0
	}
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			i++
			switch asciiClass[b] {
			case asciiNewline:
				space, newlines = true, newlines+1
			case asciiSpace:
				space = true
			default:
				flush()
				dst = append(dst, asciiLower[b])
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			flush()
			dst = append(dst, b)
		case unicode.IsSpace(r):
			space = true
		default:
			flush()
			dst = utf8.AppendRune(dst, unicode.ToLower(r))
		}
		i += size
	}
	flush()
	return dst
}

// ASCII classes for fold.
const (
	asciiOther = iota
	asciiSpace
	asciiNewline
)

// asciiClass and asciiLower are fold's ASCII tables: the whitespace class of a byte,
// and its lowercase.
var asciiClass, asciiLower = asciiTables()

func asciiTables() (class, lower [utf8.RuneSelf]byte) {
	for b := range byte(utf8.RuneSelf) {
		lower[b] = b
		if 'A' <= b && b <= 'Z' {
			lower[b] = b + 'a' - 'A'
		}
	}
	for _, b := range []byte(" \t\r\v\f") {
		class[b] = asciiSpace
	}
	class['\n'] = asciiNewline
	return class, lower
}

// cutUTF8 returns the longest prefix of s of at most n bytes that ends on a rune
// boundary.
func cutUTF8(s string, n int) string {
	n = max(min(n, len(s)), 0)
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
