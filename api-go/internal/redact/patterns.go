package redact

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// byteSet is a set of bytes.
type byteSet [256]bool

// setOf builds a byteSet from characters and a-z style ranges.
func setOf(ranges ...string) *byteSet {
	var s byteSet
	for _, r := range ranges {
		for i := 0; i < len(r); i++ {
			if i+2 < len(r) && r[i+1] == '-' {
				for c := int(r[i]); c <= int(r[i+2]); c++ {
					s[c] = true
				}
				i += 2
				continue
			}
			s[r[i]] = true
		}
	}
	return &s
}

// allExcept is every byte but the given ones.
func allExcept(chars string) *byteSet {
	var s byteSet
	for c := range s {
		s[c] = true
	}
	for i := 0; i < len(chars); i++ {
		s[chars[i]] = false
	}
	return &s
}

var (
	alnum      = setOf("A-Z", "a-z", "0-9")
	alnumUS    = setOf("A-Z", "a-z", "0-9", "_")
	b64url     = setOf("A-Z", "a-z", "0-9", "_", "-")
	slackBody  = setOf("A-Z", "a-z", "0-9", "-")
	jwtBody    = setOf("A-Z", "a-z", "0-9", "_", ".", "-")
	hookPath   = setOf("A-Z", "a-z", "0-9", "+/_-")
	bearerSet  = setOf("A-Z", "a-z", "0-9", "._~+/=-")
	basicSet   = setOf("A-Z", "a-z", "0-9", "+/=")
	schemeByte = setOf("A-Z", "a-z", "0-9", "+.-")
	userByte   = allExcept(" \t\r\n\v\f:/@\"'<>")
	passByte   = allExcept(" \t\r\n\v\f@/\"'<>")
	emailLocal = setOf("A-Z", "a-z", "0-9", "._%+-")
	b64Std     = setOf("A-Z", "a-z", "0-9", "+/=")
)

func isWordByte(c byte) bool { return alnumUS[c] }

func isLetter(c byte) bool { return 'a' <= c|0x20 && c|0x20 <= 'z' }

func equalFoldASCII(b []byte, lower string) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

func scanSet(buf []byte, i, n int, set *byteSet) int {
	for i < n && set[buf[i]] {
		i++
	}
	return i
}

// regexRule is an RE2 pattern whose every match begins with one of its
// literals. It is evaluated anchored at each literal occurrence, so a
// candidate depends only on the bytes at and around its position.
type regexRule struct {
	id       string
	pattern  string   // RE2 syntax; compiled anchored (^) on first use
	group    int      // capture group holding the secret; 0 is the whole match
	lb       bool     // the match must not follow a word byte
	rb       bool     // the secret must not be followed by an alphanumeric byte
	literals []string // every match begins with one of these
	need     *need    // class check on the secret, if any
	tail     *byteSet // greedy final alphabet: extends a secret reaching the window end
	run      *byteSet // set when the literal is followed directly by a greedy run of this alphabet
	prio     int32

	re *regexp.Regexp
}

func (r *regexRule) triggers() ([]string, bool) { return r.literals, false }

// at evaluates the match anchored at pos and returns how far later hits of this
// rule are redundant, which keeps inputs like "sk-sk-sk-..." linear:
//
//   - an accepted match ending in a greedy tail covers every later match that
//     starts inside it (they end at the same place);
//   - when the literal is followed directly by the run, a later literal inside
//     the run has the same run end and a shorter body, so it fails the length
//     check whenever this one does; and it fails the class check while its
//     judged prefix holds no byte of a class this one lacked (see retry).
//
// A left-boundary failure says nothing about later hits. The run end is
// memoized per window, and a pattern that is the literal plus one greedy run
// is matched on a bounded prefix and extended to the run end, so each hit
// costs O(judgedLen) however long the run.
func (r *regexRule) at(buf []byte, pos, litLen, from, n int, eof bool, memo *runMemo, out []candidate) ([]candidate, int) {
	if r.lb && isWordByte(buf[pos-1]) {
		return out, pos + 1
	}
	fail, lim := pos+1, n
	if r.run != nil {
		fail = max(fail, memo.scan(buf, pos+litLen, n, r.run))
		if r.tail == r.run {
			lim = min(n, pos+litLen+judgedLen+16)
		}
	}
	loc := r.re.FindSubmatchIndex(buf[pos:lim])
	if loc == nil || loc[2*r.group] < 0 {
		return out, fail
	}
	gs, ge := pos+loc[2*r.group], pos+loc[2*r.group+1]
	if ge == lim && lim < n {
		ge = fail // cut by the bound: the greedy run goes on to its end
	}
	touches := ge == n && !eof
	if !touches && r.rb && ge < n && alnum[buf[ge]] {
		return out, fail
	}
	if r.need != nil && judgeable(gs, ge, touches) && !r.need.ok(judged(buf[gs:ge])) {
		return out, r.retry(buf, pos, gs, ge, fail)
	}
	c := candidate{anchor: pos, start: gs, end: ge, label: r.id, rule: r.id, prio: r.prio}
	if touches && r.tail != nil {
		c.cont = &tailCont{set: r.tail}
	}
	if r.tail == nil {
		return push(out, c), pos + 1 // fixed length: a later match may extend past this one
	}
	return push(out, c), max(fail, ge)
}

// retry returns the first later hit worth evaluating after the value [gs, ge)
// failed the class check. A later hit in the run is judged on its own first
// judgedLen bytes, all inside [gs, ge) and past this value's judged prefix
// only at their end: it fails as well unless that window reaches a byte of a
// class this value's judged prefix lacked. A value judged whole rules out the
// whole run.
func (r *regexRule) retry(buf []byte, pos, gs, ge, fail int) int {
	if ge-gs <= judgedLen {
		return fail
	}
	digit, letter := r.need.missing(buf[gs : gs+judgedLen])
	for q := gs + judgedLen; q < ge; q++ {
		c := buf[q]
		if (digit && '0' <= c && c <= '9') || (letter && isLetter(c)) {
			return max(pos+1, q-judgedLen+1)
		}
	}
	return fail
}

// need is a class check on a token: after its first skip bytes it must hold a
// digit and/or a letter. Class presence is monotone on suffixes, which is what
// lets one failed hit rule out later ones.
type need struct {
	skip          int
	digit, letter bool
}

func (v *need) ok(b []byte) bool {
	d, l := v.missing(b)
	return !d && !l
}

// missing reports which required classes b's body lacks.
func (v *need) missing(b []byte) (digit, letter bool) {
	body := b[min(v.skip, len(b)):]
	return v.digit && !hasDigit(body), v.letter && !hasUpper(body) && !hasLower(body)
}

// mixedAfter requires a digit and a letter after prefix.
func mixedAfter(prefix string) *need { return &need{skip: len(prefix), digit: true, letter: true} }

// judgedLen bounds how much of a value the validators look at. It is below the
// lookahead, so a value that reaches the end of a window is judged on exactly
// the bytes a one-shot pass would judge, and a long value costs O(judgedLen).
const judgedLen = 512

func judged(b []byte) []byte {
	if len(b) > judgedLen {
		return b[:judgedLen]
	}
	return b
}

// judgeable reports whether a value can be validated now: it ended inside the
// window, or at least judgedLen bytes of it are present. Otherwise the value is
// accepted (fail closed).
func judgeable(start, end int, touches bool) bool { return !touches || end-start >= judgedLen }

// tokenRules returns the default RE2 pattern set, in label priority order.
// It is compiled once, on first use, so binaries that link the package but
// never redact pay nothing.
var tokenRules = sync.OnceValue(func() []*regexRule {
	rules := []*regexRule{
		{id: RuleAnthropicKey, pattern: `sk-ant-[A-Za-z0-9_-]{20,}`, lb: true,
			literals: []string{"sk-ant-"}, need: mixedAfter("sk-ant-"), tail: b64url, run: b64url},
		{id: RuleOpenAIKey, pattern: `sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`, lb: true,
			literals: []string{"sk-"}, need: mixedAfter("sk-"), tail: b64url, run: b64url},
		{id: RuleGitHubToken, pattern: `(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}`, lb: true,
			literals: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, tail: alnum, run: alnum},
		{id: RuleGitHubToken, pattern: `github_pat_[A-Za-z0-9_]{22,}`, lb: true,
			literals: []string{"github_pat_"}, tail: alnumUS, run: alnumUS},
		{id: RuleGitLabToken, pattern: `glpat-[A-Za-z0-9_-]{20,}`, lb: true,
			literals: []string{"glpat-"}, tail: b64url, run: b64url},
		{id: RuleSlackToken, pattern: `xox[abposre]-[A-Za-z0-9-]{10,}`, lb: true,
			literals: []string{"xoxa-", "xoxb-", "xoxp-", "xoxo-", "xoxs-", "xoxr-", "xoxe-"}, need: &need{digit: true}, tail: slackBody, run: slackBody},
		{id: RuleSlackToken, pattern: `xapp-[0-9]-[A-Za-z0-9-]{10,}`, lb: true,
			literals: []string{"xapp-"}, tail: slackBody},
		{id: RuleSlackWebhook, pattern: `https://hooks\.slack\.com/(?:services|workflows|triggers)/([A-Za-z0-9+/_-]{16,})`,
			group: 1, literals: []string{"https://hooks.slack.com/"}, tail: hookPath},
		{id: RuleAWSAccessKey, pattern: `(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}`, lb: true, rb: true,
			literals: []string{"AKIA", "ASIA", "ABIA", "ACCA"}},
		{id: RuleGoogleAPIKey, pattern: `AIza[0-9A-Za-z_-]{35}`, lb: true, rb: true,
			literals: []string{"AIza"}},
		{id: RuleStripeKey, pattern: `[sr]k_(?:live|test)_[0-9A-Za-z]{16,}`, lb: true,
			literals: []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_"}, tail: alnum, run: alnum},
		{id: RuleNPMToken, pattern: `npm_[A-Za-z0-9]{36,}`, lb: true,
			literals: []string{"npm_"}, tail: alnum, run: alnum},
		{id: RuleHuggingFace, pattern: `hf_[A-Za-z0-9]{34,}`, lb: true,
			literals: []string{"hf_"}, need: mixedAfter("hf_"), tail: alnum, run: alnum},
		{id: RuleXmustardToken, pattern: `xmt_[A-Za-z0-9_-]{43,}`, lb: true,
			literals: []string{"xmt_"}, tail: b64url, run: b64url},
		{id: RuleJWT, pattern: `eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_.-]{8,}`, lb: true,
			literals: []string{"eyJ"}, tail: jwtBody, run: b64url},
	}
	for i, r := range rules {
		r.prio = int32(10 + i)
		r.re = regexp.MustCompile(`^(?:` + r.pattern + `)`)
	}
	return rules
})

func tokenValue(b []byte) bool { return !isPlaceholder(b) && looksRandom(b) }

func notPlaceholder(b []byte) bool { return !isPlaceholder(b) }

// basicCredentials accepts a base64 value that decodes to printable "user:pass".
func basicCredentials(b []byte) bool {
	dec, err := base64.StdEncoding.DecodeString(string(b))
	if err != nil {
		dec, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(string(b), "="))
		if err != nil {
			return false
		}
	}
	colon := bytes.IndexByte(dec, ':')
	if colon <= 0 || colon == len(dec)-1 {
		return false
	}
	for _, c := range dec {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// schemeRules match an authorization scheme word ("Bearer", "Basic", any
// case) followed by its credential.
var schemeRules = []*schemeRule{
	{id: RuleBearer, word: "bearer", set: bearerSet, min: 12, valid: tokenValue, prio: 40},
	{id: RuleBasicAuth, word: "basic", set: basicSet, min: 8, valid: basicCredentials, prio: 41},
}

// schemeRule matches "<word>[ \t]{1,8}<credential>" with word matched
// case-insensitively at a word boundary and the credential a run of set bytes.
type schemeRule struct {
	id    string
	word  string // lowercase
	set   *byteSet
	min   int
	valid func([]byte) bool
	prio  int32
}

func (r *schemeRule) triggers() ([]string, bool) { return []string{r.word}, true }

func (r *schemeRule) at(buf []byte, pos, litLen, from, n int, eof bool, _ *runMemo, out []candidate) ([]candidate, int) {
	if isWordByte(buf[pos-1]) {
		return out, pos + 1
	}
	k := pos + len(r.word)
	spaces := 0
	for k < n && spaces < 8 && (buf[k] == ' ' || buf[k] == '\t') {
		k++
		spaces++
	}
	if spaces == 0 || k == n {
		return out, pos + 1
	}
	end := scanSet(buf, k, n, r.set)
	touches := end == n && !eof
	if (!touches && end-k < r.min) || (judgeable(k, end, touches) && !r.valid(judged(buf[k:end]))) {
		return out, pos + 1
	}
	c := candidate{anchor: pos, start: k, end: end, label: r.id, rule: r.id, prio: r.prio}
	if touches {
		c.cont = &tailCont{set: r.set}
	}
	return push(out, c), end
}

// urlCredRule finds the password in URL userinfo: scheme://user:password@host.
// It triggers on "://" and reads the scheme backwards.
type urlCredRule struct{}

func (urlCredRule) triggers() ([]string, bool) { return []string{"://"}, false }

func (urlCredRule) at(buf []byte, sep, litLen, from, n int, eof bool, _ *runMemo, out []candidate) ([]candidate, int) {
	return urlCredAt(buf, sep, from, n, out), sep + 1
}

func urlCredAt(buf []byte, sep, from, n int, out []candidate) []candidate {
	// scheme: 2..21 scheme bytes, the first a letter at a word boundary
	lo := sep
	for lo > 0 && sep-lo < 21 && schemeByte[buf[lo-1]] {
		lo--
	}
	q := -1
	for k := max(lo, 1); k <= sep-2; k++ {
		if isLetter(buf[k]) && !isWordByte(buf[k-1]) {
			q = k
			break
		}
	}
	if q < from {
		return out
	}
	u := scanSet(buf, sep+3, min(n, sep+3+256), userByte)
	if u == sep+3 || u >= n || buf[u] != ':' {
		return out
	}
	p := scanSet(buf, u+1, min(n, u+1+256), passByte)
	if p == u+1 || p >= n || buf[p] != '@' || !notPlaceholder(buf[u+1:p]) {
		return out
	}
	return push(out, candidate{anchor: q, start: u + 1, end: p, label: RuleURLCredentials, rule: RuleURLCredentials, prio: 42})
}

// emailRule (PII) triggers on "@" and reads the local part backwards.
type emailRule struct{}

var emailDomain = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63})*\.[A-Za-z]{2,24}`)
})

func (emailRule) triggers() ([]string, bool) { return []string{"@"}, false }

func (emailRule) at(buf []byte, pos, litLen, from, n int, eof bool, _ *runMemo, out []candidate) ([]candidate, int) {
	return emailAt(buf, pos, from, n, eof, out), pos + 1
}

func emailAt(buf []byte, pos, from, n int, eof bool, out []candidate) []candidate {
	s := pos
	for s > 0 && pos-s < 64 && emailLocal[buf[s-1]] {
		s--
	}
	if s == pos || s < from || isWordByte(buf[s-1]) || emailLocal[buf[s-1]] {
		return out
	}
	loc := emailDomain().FindIndex(buf[pos:n])
	if loc == nil || (pos+loc[1] == n && !eof) {
		return out
	}
	return push(out, candidate{anchor: s, start: s, end: pos + loc[1], label: RuleEmail, rule: RuleEmail, prio: 90})
}

// cardRule (PII) finds 13-19 digit runs, optionally grouped by single spaces
// or dashes, that pass the Luhn check.
type cardRule struct{}

func (cardRule) find(buf []byte, from, to, n int, eof bool, out []candidate) []candidate {
	isDigit := func(c byte) bool { return '0' <= c && c <= '9' }
	for i := from; i < to; i++ {
		if !isDigit(buf[i]) || isWordByte(buf[i-1]) {
			continue
		}
		j, last, digits := i, i, 0
		for j < n && digits < 19 {
			if isDigit(buf[j]) {
				digits++
				j++
				last = j
			} else if (buf[j] == ' ' || buf[j] == '-') && j+1 < n && isDigit(buf[j+1]) {
				j++
			} else {
				break
			}
		}
		if last == n && !eof {
			return out // undecided; deferred to the next window
		}
		if digits >= 13 && (last == n || !alnum[buf[last]]) && luhn(buf[i:last]) {
			out = push(out, candidate{anchor: i, start: i, end: last, label: RulePaymentCard, rule: RulePaymentCard, prio: 91})
		}
		i = last
	}
	return out
}

func luhn(b []byte) bool {
	sum, digits, double := 0, 0, false
	for i := len(b) - 1; i >= 0; i-- {
		c := b[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if double {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		digits++
		double = !double
	}
	return digits >= 13 && sum%10 == 0
}

// tailCont extends a token while its bytes stay in the token alphabet.
type tailCont struct{ set *byteSet }

func (t *tailCont) advance(buf []byte, from, n int, eof bool) (int, bool) {
	i := scanSet(buf, from, n, t.set)
	if i == n && !eof {
		return n, true
	}
	return i, false
}

// PEM private-key blocks. The markers stay; the key material between them is
// replaced.
var (
	pemBegin = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^-----BEGIN[ A-Z0-9]{0,40}PRIVATE KEY(?: BLOCK)?-----`)
	})
	pemEnd = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^-----END[ A-Z0-9]{0,40}PRIVATE KEY(?: BLOCK)?-----`)
	})
)

const (
	// pemMarkerMax bounds a BEGIN or END marker.
	pemMarkerMax = 72
	// pemMaxBody bounds a body: an RSA-16384 key is about 13 KiB of PEM, so
	// this leaves room for escaping, string concatenation and armor headers.
	pemMaxBody = 32 << 10
	// pemSpan is the most a PEM decision reads past its trigger: a BEGIN
	// marker, a body and an END marker. The lookahead covers it.
	pemSpan = 2*pemMarkerMax + pemMaxBody
	// pemMinRun is the shortest base64 run taken for key material. A body with
	// no END marker must hold one, and between the separators of a quoted,
	// concatenated or prefixed key each such run is replaced. Key lines are 64
	// or 76 bytes; words in prose and log fields are shorter.
	pemMinRun = 16
)

type pemRule struct{}

func (pemRule) triggers() ([]string, bool) { return []string{"-----BEGIN"}, false }

// at redacts the key after a BEGIN marker:
//
//   - a body that runs to its END marker (raw, JSON-escaped, with armor
//     headers) is replaced whole;
//   - a body interrupted by separators (a closing quote and "+" or an adjacent
//     literal, "# " or "> " line prefixes, spaces between lines) whose END
//     marker follows within pemMaxBody keeps the separators: each base64 run of
//     pemMinRun or more bytes is replaced, and so is the key's short last line
//     (see keyRuns), so JSON strings and records, source code and comments keep
//     their shape;
//   - a body with no END marker is replaced up to the first byte a body cannot
//     hold, if it holds a base64 run of pemMinRun bytes, so a BEGIN marker named
//     in prose or a log line ("found -----BEGIN PRIVATE KEY----- in upload")
//     costs nothing.
//
// Later BEGIN markers inside what was redacted are skipped: they add nothing.
func (pemRule) at(buf []byte, pos, litLen, from, n int, eof bool, _ *runMemo, out []candidate) ([]candidate, int) {
	marker := buf[pos:min(n, pos+pemMarkerMax)]
	if !pemBegin().Match(marker) { // Match, unlike FindIndex, does not allocate
		return out, pos + 1
	}
	// '-' is outside the marker's name class: the first dashes after BEGIN close it.
	me := pos + litLen + bytes.IndexByte(marker[litLen:], '-') + len("-----")
	b := scanPEMBody(buf, me, n)
	c := candidate{anchor: pos, start: me, end: b.end, label: RulePrivateKey, rule: RulePrivateKey, prio: 1}
	if b.closed {
		if b.run == 0 {
			return out, pos + 1 // no key between the markers
		}
		return push(out, c), b.end
	}
	if lim := min(n, me+pemMaxBody); b.stop < lim {
		if e := pemEndAfter(buf, b.stop, lim, n); e >= 0 {
			return keyRuns(buf, pos, me, e, out), e
		}
	}
	if b.run < pemMinRun {
		return out, pos + 1
	}
	return push(out, c), b.end
}

// pemBodyScan describes the contiguous body after a BEGIN marker.
type pemBodyScan struct {
	stop   int  // first byte past the body; the END marker when closed
	end    int  // end of the redacted body: stop, less trailing whitespace unless closed
	run    int  // longest run of base64
	closed bool // the body ends at an END marker
}

// scanPEMBody reads a key body from me: base64 lines, whitespace, JSON-escaped
// line breaks (\n, \\n, \/) and armor header lines before the base64
// ("Proc-Type: 4,ENCRYPTED", "DEK-Info: ...", OpenPGP "Version: ..."). A line
// of base64 is one word, so a space followed by another word on the same line
// ends the body, as do a byte a body cannot hold (a quote, other punctuation,
// non-ASCII) and pemMaxBody bytes.
func scanPEMBody(buf []byte, me, n int) pemBodyScan {
	lim := min(n, me+pemMaxBody)
	var b pemBodyScan
	i, ws, run := me, 0, 0
	midLine, header, sawData := false, false, false
body:
	for i < lim {
		c, w, space, data := buf[i], 1, false, false
		switch {
		case c == '-':
			b.closed = pemEndAt(buf, i, n)
			break body
		case c == '\n' || c == '\r':
			midLine, header, space = false, false, true
		case c == ' ' || c == '\t':
			if midLine && !header && ws == 0 && wordFollows(buf, i, n) {
				break body
			}
			space = true
		case c == '\\':
			k := i
			for k < n && k-i < 8 && buf[k] == '\\' {
				k++
			}
			if k == n {
				break body
			}
			switch buf[k] {
			case 'n', 'r':
				midLine, header, space = false, false, true
			case 't':
				if midLine && !header && ws == 0 && wordFollows(buf, k+1, n) {
					break body
				}
				space = true
			case '/':
				data = !header
			default:
				break body // \" and other escapes end the body
			}
			w = k + 1 - i
		case header:
			if c < 0x20 || c > 0x7e || c == '"' {
				break body
			}
		default:
			if !midLine && !sawData && pemHeader(buf, i, n) {
				header, midLine = true, true
				break
			}
			if !b64Std[c] {
				break body
			}
			data = true
		}
		if data {
			midLine, sawData = true, true
			run++
			b.run = max(b.run, run)
		} else {
			run = 0
		}
		if space {
			ws += w
		} else {
			ws = 0
		}
		i += w
	}
	b.stop, b.end = i, i
	if !b.closed {
		b.end = i - ws // a stopped body gives back its trailing whitespace
	}
	return b
}

// wordFollows reports whether a word follows the spaces at i on the same line.
func wordFollows(buf []byte, i, n int) bool {
	for i < n {
		switch {
		case buf[i] == ' ' || buf[i] == '\t':
			i++
		case buf[i] == '\\' && i+1 < n && buf[i+1] == 't':
			i += 2
		default:
			return b64Std[buf[i]]
		}
	}
	return false
}

// pemHeader reports whether an armor header line ("Name: value") starts at i.
func pemHeader(buf []byte, i, n int) bool {
	if !isLetter(buf[i]) {
		return false
	}
	for k := i + 1; k < i+42 && k+1 < n; k++ {
		switch c := buf[k]; {
		case alnum[c] || c == '-':
		case c == ':':
			return buf[k+1] == ' ' || buf[k+1] == '\t'
		default:
			return false
		}
	}
	return false
}

var (
	pemDashes    = []byte("-----")
	pemEndPrefix = []byte("-----END")
	pemBeginWord = []byte("-----BEGIN")
)

// pemEndAt reports whether a private key END marker starts at i.
func pemEndAt(buf []byte, i, n int) bool {
	return bytes.HasPrefix(buf[i:n], pemEndPrefix) && pemEnd().Match(buf[i:min(n, i+pemMarkerMax)])
}

// pemEndAfter returns where a private key END marker starts in buf[from:lim),
// or -1 when there is none or another block's marker ("-----BEGIN", another
// kind of END) comes first. Stopping at the next BEGIN keeps the search for all
// the markers in an input linear.
func pemEndAfter(buf []byte, from, lim, n int) int {
	for i := from; i < lim; i++ {
		k := bytes.Index(buf[i:lim], pemDashes)
		if k < 0 {
			return -1
		}
		i += k
		switch {
		case bytes.HasPrefix(buf[i:n], pemEndPrefix):
			if pemEndAt(buf, i, n) {
				return i
			}
			return -1
		case bytes.HasPrefix(buf[i:n], pemBeginWord):
			return -1
		}
	}
	return -1
}

// keyRuns returns the key material of buf[me:e), a key between its markers
// that is interrupted by separators: every base64 run of pemMinRun or more
// bytes, and after the last of them the key's short last line, the last run of
// 4 or more bytes holding a digit, an uppercase letter or '+', '/', '='. A
// backslash escape (\n, \", \\) separates runs. Only the first run is counted;
// the others are parts of the same secret.
func keyRuns(buf []byte, pos, me, e int, out []candidate) []candidate {
	first, last := len(out), me
	tail := -1
	var tailEnd int
	for i := me; i < e; {
		switch c := buf[i]; {
		case c == '\\':
			i += 2
			continue
		case !b64Std[c]:
			i++
			continue
		}
		j := scanSet(buf, i, e, b64Std)
		switch {
		case j-i >= pemMinRun:
			out = push(out, candidate{anchor: pos, start: i, end: j, label: RulePrivateKey, rule: RulePrivateKey, prio: 1, part: len(out) > first})
			last = j
		case j-i >= 4 && keyLike(buf[i:j]):
			tail, tailEnd = i, j
		}
		i = j
	}
	if len(out) > first && tail >= last {
		out = push(out, candidate{anchor: pos, start: tail, end: tailEnd, label: RulePrivateKey, rule: RulePrivateKey, prio: 1, part: true})
	}
	return out
}

// keyLike reports whether a short base64 run looks like the end of a key
// rather than a word: it holds a digit, an uppercase letter or '+', '/', '='.
func keyLike(b []byte) bool {
	return hasDigit(b) || hasUpper(b) || bytes.ContainsAny(b, "+/=")
}

// literalRule redacts exact environment values.
type literalRule struct {
	values [][]byte
	labels []string
}

const (
	minLiteral = 8
	maxLiteral = 2048
)

func newLiteralRule(env []EnvSecret) *literalRule {
	seen := map[string]bool{}
	var lr literalRule
	// longer values first, so a value containing another is labeled by itself
	sorted := append([]EnvSecret(nil), env...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Value) > len(sorted[j].Value) })
	for _, s := range sorted {
		v := strings.TrimSpace(s.Value)
		if len(v) < minLiteral || len(v) > maxLiteral || seen[v] || s.Name == "" {
			continue
		}
		seen[v] = true
		lr.values = append(lr.values, []byte(v))
		lr.labels = append(lr.labels, RuleEnv+":"+s.Name)
	}
	if len(lr.values) == 0 {
		return nil
	}
	return &lr
}

func (l *literalRule) find(buf []byte, from, to, n int, eof bool, out []candidate) []candidate {
	for i, v := range l.values {
		for pos := from; pos < to; {
			j := bytes.Index(buf[pos:min(n, to+len(v)-1)], v)
			if j < 0 {
				break
			}
			s := pos + j
			out = push(out, candidate{anchor: s, start: s, end: s + len(v), label: l.labels[i], rule: RuleEnv})
			pos = s + len(v)
		}
	}
	return out
}

// keyClass is how strongly a key name marks its value as secret.
type keyClass int

const (
	keyNone keyClass = iota
	keyToken
	keyPassword
)

var (
	passwordSuffixes = []string{"password", "passwd", "passphrase", "secret"}
	tokenSuffixes    = []string{
		"token", "apikey", "accesskey", "secretkey", "privatekey", "signingkey", "encryptionkey",
		"masterkey", "clientkey", "authkey", "credential", "credentials", "authorization", "cookie",
		"connectionstring",
	}
	keyQualifiers = []string{
		"api", "access", "secret", "private", "signing", "encryption", "master", "client", "auth",
		"app", "service", "account", "license", "storage", "sas", "subscription", "webhook",
	}
)

// hinted reports whether a flattened key contains a secret hint ("pass",
// "pwd", "secret", "token", "key", "auth", "cookie", "credential",
// "connection") in one pass.
func hinted(k []byte) bool {
	has := func(i int, h string) bool { return len(k)-i >= len(h) && string(k[i:i+len(h)]) == h }
	for i := 0; i < len(k); i++ {
		switch k[i] {
		case 'p':
			if has(i, "pass") || has(i, "pwd") {
				return true
			}
		case 's':
			if has(i, "secret") {
				return true
			}
		case 't':
			if has(i, "token") {
				return true
			}
		case 'k':
			if has(i, "key") {
				return true
			}
		case 'a':
			if has(i, "auth") {
				return true
			}
		case 'c':
			if has(i, "cookie") || has(i, "credential") || has(i, "connection") {
				return true
			}
		}
	}
	return false
}

// classifyKey decides whether a key names a secret: "DB_PASSWORD",
// "clientSecret", "x-api-key", "access_token" do; "token_type", "max_tokens",
// "session_id", "primary_key" and "author" do not.
func classifyKey(key []byte) keyClass {
	if len(key) == 0 || len(key) > 64 {
		return keyNone
	}
	var flat [64]byte
	m := 0
	for _, c := range key {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if alnum[c] {
			flat[m] = c
			m++
		}
	}
	canon := flat[:m]
	if !hinted(canon) {
		return keyNone
	}
	for _, s := range passwordSuffixes {
		if hasSuffix(canon, s) {
			return keyPassword
		}
	}
	last, prev := lastWords(key)
	if prev != nil {
		if wordIs(last, "pass") || wordIs(last, "pwd") {
			return keyPassword
		}
		if wordIs(last, "auth") {
			return keyToken
		}
	}
	for _, s := range tokenSuffixes {
		if hasSuffix(canon, s) {
			return keyToken
		}
	}
	if wordIs(last, "key") {
		for _, q := range keyQualifiers {
			if wordIs(prev, q) {
				return keyToken
			}
		}
	}
	return keyNone
}

func hasSuffix(b []byte, s string) bool { return len(b) >= len(s) && string(b[len(b)-len(s):]) == s }

func wordIs(w []byte, lower string) bool { return len(w) == len(lower) && equalFoldASCII(w, lower) }

// lastWords returns the last two words of a key split on punctuation and
// camelCase boundaries: "clientSecret" → secret, client; "APIKey" → Key, API;
// "x-api-key" → key, api. prev is nil for a one-word key.
func lastWords(key []byte) (last, prev []byte) {
	start := -1
	for i, c := range key {
		if !alnum[c] {
			if start >= 0 {
				prev, last = last, key[start:i]
				start = -1
			}
			continue
		}
		if start >= 0 && 'A' <= c && c <= 'Z' {
			p := key[i-1]
			nextLower := i+1 < len(key) && 'a' <= key[i+1] && key[i+1] <= 'z'
			if ('a' <= p && p <= 'z') || ('0' <= p && p <= '9') || ('A' <= p && p <= 'Z' && nextLower) {
				prev, last = last, key[start:i]
				start = -1
			}
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		prev, last = last, key[start:]
	}
	return last, prev
}

// valueCtx is how a value was written, which decides whether a bare word can
// be a secret.
type valueCtx int

const (
	ctxCode    valueCtx = iota // unquoted and maybe an expression: password = getpass
	ctxLiteral                 // unquoted on an env, dotenv, flag or YAML line: data
	ctxQuoted                  // a string literal or YAML block scalar
)

// secretValue applies the per-class checks to a candidate value. A password
// is any value but a placeholder, except that a bare identifier in code is a
// reference (password = request_password). An unquoted token must also hold a
// digit; every token must look random.
func secretValue(class keyClass, v []byte, ctx valueCtx) bool {
	if isPlaceholder(v) {
		return false
	}
	switch class {
	case keyPassword:
		return ctx != ctxCode || !identifierLike(v)
	case keyToken:
		if ctx != ctxQuoted && (identifierLike(v) || !hasDigit(v)) {
			return false
		}
		return looksRandom(v)
	}
	return false
}

// keyedRule finds values assigned to secret-looking keys:
//
//	"password": "hunter2"        JSON (also double-encoded \"password\": \"...\")
//	api_key: 8f14e45f...         YAML, including block scalars (password: |)
//	export API_TOKEN=a8f5f16...  env and shell
//	X-Api-Key: 12ab...           header lines
//	--password=hunter2, --token 12ab...
//
// The anchor is the separator (or the flag); only the value is replaced.
type keyedRule struct{}

// runMemo remembers the last run scanned: every position inside it ends where
// it does, so a long run ("token:token:...", "sk-sk-...") is scanned once.
type runMemo struct{ start, end int }

func (m *runMemo) scan(buf []byte, v, n int, set *byteSet) int {
	if v < m.start || v >= m.end {
		m.start, m.end = v, scanSet(buf, v, n, set)
	}
	return m.end
}

func (keyedRule) find(buf []byte, from, to, n int, eof bool, out []candidate) []candidate {
	memo := &runMemo{}
	for i := from; i < to; {
		j := bytes.IndexAny(buf[i:to], ":=-")
		if j < 0 {
			break
		}
		at := i + j
		i = at + 1
		var c candidate
		var ok bool
		if buf[at] == '-' {
			c, ok = flagValue(buf, at, n, eof, memo)
		} else {
			c, ok = assignedValue(buf, at, n, eof, memo)
		}
		if ok {
			out = push(out, c)
			if c.covers {
				i = max(i, c.end)
			}
		}
	}
	return out
}

// assignedValue handles "key: value", "key=value", "key := value" and
// "key => value" with the separator at sep.
func assignedValue(buf []byte, sep, n int, eof bool, memo *runMemo) (candidate, bool) {
	v := sep + 1
	switch buf[sep] {
	case ':':
		if buf[sep-1] == ':' || (v < n && buf[v] == ':') {
			return candidate{}, false // "a::b" paths
		}
		if v < n && buf[v] == '=' {
			v++ // :=
		}
	case '=':
		if strings.IndexByte("=!<>+-*/%&|^~:", buf[sep-1]) >= 0 || (v < n && buf[v] == '=') {
			return candidate{}, false // ==, !=, +=, :=, ...
		}
		if v < n && buf[v] == '>' {
			v++ // =>
		}
	}
	start, end, level, ok := keyBefore(buf, sep)
	if !ok {
		return candidate{}, false
	}
	a := assign{class: classifyKey(buf[start:end]), level: level}
	if a.class == keyNone {
		return candidate{}, false
	}
	a.kind, a.col = assignment(buf, sep, start, end, level, v, n)
	for k := 0; k < 8 && v < n && (buf[v] == ' ' || buf[v] == '\t'); k++ {
		v++
	}
	return valueAt(buf, sep, v, n, eof, a, memo)
}

// assign describes the key and line a value belongs to.
type assign struct {
	class keyClass
	level int // key quoting: 0 bare, 1 "key", 2 \"key\"
	kind  assignKind
	col   int // key column, for YAML block scalars
}

type assignKind int

const (
	assignCode assignKind = iota // may be source code
	assignEnv                    // NAME=value, export name=value, dotenv, --flag value
	assignYAML                   // key: value at the start of a line
)

// assignment classifies the line around sep, the separator after the key
// buf[start:end]; v is just past the separator and n ends the window. An env
// assignment has no spaces around '=' and an UPPER_SNAKE key, an export, a
// flag, or a key at the start of its line; a YAML one is "key: " at the start
// of a line, after any indentation or "- " item marker.
func assignment(buf []byte, sep, start, end, level, v, n int) (assignKind, int) {
	if v >= n {
		return assignCode, 0
	}
	switch buf[sep] {
	case '=':
		if level != 0 || end != sep || v != sep+1 || buf[v] == ' ' || buf[v] == '\t' {
			return assignCode, 0
		}
		key := buf[start:end]
		if key[0] == '-' || upperSnake(key) || exported(buf, start) {
			return assignEnv, 0
		}
		if ok, col := lineStart(buf, start, false); ok && col == 0 {
			return assignEnv, 0
		}
	case ':':
		if level > 1 || (buf[v] != ' ' && buf[v] != '\t') {
			return assignCode, 0
		}
		if ok, col := lineStart(buf, start-level, true); ok {
			return assignYAML, col
		}
	}
	return assignCode, 0
}

func upperSnake(key []byte) bool {
	letter := false
	for _, c := range key {
		switch {
		case 'A' <= c && c <= 'Z':
			letter = true
		case '0' <= c && c <= '9', c == '_':
		default:
			return false
		}
	}
	return letter
}

// exported reports "export " (or "set ", "setenv ") just before start.
func exported(buf []byte, start int) bool {
	k := start - 1
	if k < 0 || buf[k] != ' ' {
		return false
	}
	for k >= 0 && buf[k] == ' ' {
		k--
	}
	for _, w := range []string{"export", "set", "setenv", "declare -x"} {
		if b := k - len(w) + 1; b >= 1 && string(buf[b:k+1]) == w && !isWordByte(buf[b-1]) {
			return true
		}
	}
	return false
}

// lineStart reports whether position i starts its line and returns i's
// column. With indent, spaces, tabs and a YAML "- " item marker may come
// first. A line starts after a raw line break, a JSON-escaped one (\n, \r),
// or the opening quote of a JSON string; the start of input is a line break
// (buf[0] is the synthetic newline). The lookback stays well inside the
// window's context.
func lineStart(buf []byte, i int, indent bool) (bool, int) {
	k := i - 1
	if indent {
		for s := 0; s < 64 && k >= 0 && (buf[k] == ' ' || buf[k] == '\t'); s++ {
			k--
		}
		if k >= 0 && buf[k] == '-' && k+1 < i && buf[k+1] == ' ' {
			k--
			for s := 0; s < 64 && k >= 0 && (buf[k] == ' ' || buf[k] == '\t'); s++ {
				k--
			}
		}
	}
	if k < 0 {
		return false, 0
	}
	switch c := buf[k]; {
	case c == '\n' || c == '\r':
	case c == '"' && k == i-1:
	case (c == 'n' || c == 'r') && k > 0 && buf[k-1] == '\\':
	default:
		return false, 0
	}
	return true, i - k - 1
}

// flagValue handles "--password value" and "--api-key value" at the first
// dash; "--password=value" is handled by the '=' separator.
func flagValue(buf []byte, at, n int, eof bool, memo *runMemo) (candidate, bool) {
	if at+1 >= n || buf[at+1] != '-' || isWordByte(buf[at-1]) || buf[at-1] == '-' {
		return candidate{}, false
	}
	k := at + 2
	for k < n && k-at-2 < 64 && (alnum[buf[k]] || buf[k] == '_' || buf[k] == '-') {
		k++
	}
	if k == at+2 || k >= n || (buf[k] != ' ' && buf[k] != '\t') {
		return candidate{}, false
	}
	a := assign{class: classifyKey(buf[at+2 : k]), kind: assignEnv}
	if a.class == keyNone {
		return candidate{}, false
	}
	v := k
	for s := 0; s < 8 && v < n && (buf[v] == ' ' || buf[v] == '\t'); s++ {
		v++
	}
	if v < n && buf[v] == '-' {
		return candidate{}, false // next flag, not a value
	}
	return valueAt(buf, at, v, n, eof, a, memo)
}

// keyBefore finds the key that ends just before sep, skipping spaces and a
// closing quote, as buf[start:end]. level is 1 for "key", 2 for \"key\" (JSON
// inside a JSON string), 0 for an unquoted key.
func keyBefore(buf []byte, sep int) (start, end, level int, ok bool) {
	k := sep - 1
	for s := 0; s < 8 && k >= 0 && (buf[k] == ' ' || buf[k] == '\t'); s++ {
		k--
	}
	if k < 0 {
		return 0, 0, 0, false
	}
	var quote byte
	if buf[k] == '"' || buf[k] == '\'' {
		quote = buf[k]
		level = 1
		k--
		if k >= 0 && buf[k] == '\\' {
			level = 2
			k--
		}
	}
	end = k + 1
	for k >= 0 && end-k <= 64 && (alnum[buf[k]] || buf[k] == '_' || buf[k] == '-' || buf[k] == '.') {
		k--
	}
	start = k + 1
	if start == end || end-start > 64 {
		return 0, 0, 0, false
	}
	if level > 0 {
		if k < 0 || buf[k] != quote || (level == 2 && (k == 0 || buf[k-1] != '\\')) {
			return 0, 0, 0, false
		}
	} else if k >= 0 && (alnum[buf[k]] || buf[k] == '_') {
		return 0, 0, 0, false
	}
	return start, end, level, true
}

// valueAt scans the value starting at v and builds its candidate.
func valueAt(buf []byte, anchor, v, n int, eof bool, a assign, memo *runMemo) (candidate, bool) {
	if v >= n {
		return candidate{}, false
	}
	c := candidate{anchor: anchor, label: RuleSecretField, rule: RuleSecretField, prio: 50}
	ctx := ctxQuoted
	block, bare := false, false
	if a.kind == assignYAML && (buf[v] == '|' || buf[v] == '>') {
		c.start, c.end, block = blockScalar(buf, v, n, a.col)
	}
	switch {
	case block:
	case buf[v] == '"' || buf[v] == '\'':
		c.start = v + 1
		c.end, c.cont = scanQuoted(quotedCont{quote: buf[v], level: 1}, buf, c.start, n, eof)
	case buf[v] == '\\' && v+1 < n && buf[v+1] == '"':
		c.start = v + 2
		c.end, c.cont = scanQuoted(quotedCont{quote: '"', level: 2}, buf, c.start, n, eof)
	default:
		if a.level == 2 && buf[v] == '\\' {
			return candidate{}, false
		}
		// A bare value is one run: a key inside it could only name a value
		// ending where it does, so find skips it, which keeps runs such as
		// "token:token:..." linear. A quoted value or block scalar ends at its
		// own delimiter, and a key inside it is still looked at: its value may
		// close later.
		c.covers = true
		c.start = v
		c.end = memo.scan(buf, v, n, valueByte)
		if w := buf[c.start:c.end]; c.end < n && (buf[c.end] == ' ' || buf[c.end] == '\t') && isScheme(w) {
			s := c.end
			for k := 0; k < 8 && s < n && (buf[s] == ' ' || buf[s] == '\t'); k++ {
				s++
			}
			c.start = s
			c.end = memo.scan(buf, s, n, valueByte)
		}
		bare = c.end == n && !eof
		ctx = unquotedCtx(buf, c.start, c.end, n, eof, a.kind)
	}
	touches := c.cont != nil || bare
	if (!touches && c.end <= c.start) || (judgeable(c.start, c.end, touches) && !secretValue(a.class, judged(buf[c.start:c.end]), ctx)) {
		return candidate{}, false
	}
	if bare {
		c.cont = &tailCont{set: valueByte} // allocated only for a value that is kept
	}
	return c, true
}

// unquotedCtx decides whether a bare value is data: on an env or flag line
// unless it is called ("getpass()"), and on a YAML line when nothing but a
// comment follows it. A value of judgedLen bytes or more is judged by its line
// alone: what follows it may lie past the window.
func unquotedCtx(buf []byte, start, end, n int, eof bool, kind assignKind) valueCtx {
	long := end-start >= judgedLen
	switch kind {
	case assignEnv:
		if long || end >= n || buf[end] != '(' {
			return ctxLiteral
		}
	case assignYAML:
		if long || lineEnds(buf, end, n, eof) {
			return ctxLiteral
		}
	}
	return ctxCode
}

// lineEnds reports whether only spaces and then a line end, a "#" comment or
// the end of a JSON string follow position i.
func lineEnds(buf []byte, i, n int, eof bool) bool {
	k := i
	for s := 0; s < 8 && k < n && (buf[k] == ' ' || buf[k] == '\t'); s++ {
		k++
	}
	if k >= n {
		return eof
	}
	switch buf[k] {
	case '\n', '\r', '"':
		return true
	case '#':
		return k > i
	}
	return lineBreak(buf, k, n) > 0
}

// lineBreak returns the length of the line break at i: a raw one, or one
// escaped inside a JSON string (\n, \r). It returns 0 for anything else.
func lineBreak(buf []byte, i, n int) int {
	if i >= n {
		return 0
	}
	switch buf[i] {
	case '\n':
		return 1
	case '\r':
		if i+1 < n && buf[i+1] == '\n' {
			return 2
		}
		return 1
	case '\\':
		if i+1 < n && (buf[i+1] == 'n' || buf[i+1] == 'r') {
			return 2
		}
	}
	return 0
}

// blockMax bounds a YAML block scalar value. It is below the lookahead, so a
// block is read the same whichever window its key falls in.
const blockMax = 3 << 10

// blockScalar reads a YAML block scalar ("password: |", ">-", "|2"): the
// lines below the key indented deeper than col, the key's column. It returns
// the value from its first content byte to the end of its last content line,
// read for at most blockMax bytes, and ok=false when the indicator is not
// alone on its line or no content line follows. A JSON-escaped block (YAML
// inside a JSON string) ends at the string's closing quote.
func blockScalar(buf []byte, v, n, col int) (start, end int, ok bool) {
	k := v + 1
	for s := 0; s < 2 && k < n && strings.IndexByte("+-123456789", buf[k]) >= 0; s++ {
		k++
	}
	for s := 0; s < 8 && k < n && (buf[k] == ' ' || buf[k] == '\t'); s++ {
		k++
	}
	lim := min(n, v+blockMax)
	br := lineBreak(buf, k, lim)
	if br == 0 {
		return 0, 0, false
	}
	start = -1
	for i := k + br; i < lim; {
		j := i
		for j < lim && (buf[j] == ' ' || buf[j] == '\t') {
			j++
		}
		if j == lim {
			break
		}
		if br := lineBreak(buf, j, lim); br > 0 {
			i = j + br // blank line
			continue
		}
		if j-i <= col {
			break // dedented: the block is over
		}
		if start < 0 {
			start = j
		}
		e := j
		for e < lim && buf[e] != '"' && lineBreak(buf, e, lim) == 0 {
			if buf[e] == '\\' {
				if e+1 >= lim {
					break // keep an escape pair whole
				}
				e++
			}
			e++
		}
		end = e
		br := lineBreak(buf, e, lim)
		if br == 0 {
			break
		}
		i = e + br
	}
	return start, end, start >= 0
}

var valueByte = func() *byteSet {
	var s byteSet
	for c := 0x21; c < 0x7f; c++ {
		s[c] = true
	}
	for c := 0x80; c < 0x100; c++ {
		s[c] = true
	}
	for _, c := range []byte("\"'`,;&\\<>(){}[]") {
		s[c] = false
	}
	return &s
}()

func isScheme(w []byte) bool {
	for _, s := range []string{"bearer", "basic", "token", "digest", "negotiate", "apikey"} {
		if wordIs(w, s) {
			return true
		}
	}
	return false
}

// quotedCont scans a quoted value to its closing quote or the end of the
// line. level 2 is a value inside a JSON string, whose quotes are written \".
// A single-quoted value also ends where a JSON string around it closes.
type quotedCont struct {
	quote byte
	level int
	esc   bool // inner escape pending
}

// scanQuoted returns the region end of a quoted value starting at i and, if
// the value ran past the window, the continuation to resume it (the only case
// that allocates).
func scanQuoted(q quotedCont, buf []byte, i, n int, eof bool) (int, continuation) {
	end, open := q.advance(buf, i, n, eof)
	if open {
		kept := q
		return end, &kept
	}
	return end, nil
}

func (q *quotedCont) advance(buf []byte, from, n int, eof bool) (int, bool) {
	i := from
	for i < n {
		c := buf[i]
		width := 1
		if q.level == 1 && q.quote == '\'' && c == '"' && !q.esc {
			// A single-quoted value inside a JSON string ('hunter2 for now",...)
			// must not swallow the string's closing quote.
			switch closesJSON(buf, i, n) {
			case jsonMore:
				if !eof {
					return i, true
				}
				return i, false
			case jsonYes:
				return i, false
			}
		}
		if q.level == 2 {
			switch {
			case c == '\\':
				if i+1 >= n {
					if eof {
						return n, false
					}
					// Stop before a lone outer backslash so the next window sees
					// the pair whole: it may be the \" that ends the value.
					return i, true
				}
				c = unescape(buf[i+1])
				width = 2
			case c == '"':
				return i, false // the enclosing string ended
			}
		}
		if c == '\n' {
			return i, false
		}
		if q.esc {
			q.esc = false
		} else if c == '\\' {
			q.esc = true
		} else if c == q.quote {
			return i, false
		}
		i += width
	}
	if eof {
		return n, false
	}
	return n, true
}

const (
	jsonNo = iota
	jsonYes
	jsonMore // the window ended before it was decided
)

// closesJSON reports whether the quote at i closes a JSON string: it is
// followed, after optional spaces, by ',', '}', ']', ':', a line break, or
// the end of input.
func closesJSON(buf []byte, i, n int) int {
	k := i + 1
	for s := 0; s < 8 && k < n && (buf[k] == ' ' || buf[k] == '\t'); s++ {
		k++
	}
	if k >= n {
		return jsonMore
	}
	if strings.IndexByte(",}]:\n\r", buf[k]) >= 0 {
		return jsonYes
	}
	return jsonNo
}

// unescape maps the byte after a JSON backslash to the character it encodes.
func unescape(c byte) byte {
	switch c {
	case 'n', 'r':
		return '\n'
	case 't':
		return '\t'
	}
	return c
}
