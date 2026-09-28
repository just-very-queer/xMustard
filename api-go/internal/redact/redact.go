// Package redact removes secrets (and, on request, PII) from text before it is
// stored, rendered, captured or imported.
//
// A Redactor holds an RE2 pattern set (bearer and basic credentials, AWS,
// GitHub, GitLab, Slack, OpenAI, Anthropic, Google, Stripe, npm, Hugging Face
// and xMustard tokens, JWTs, URL userinfo passwords, PEM private keys), a
// key-aware detector for secret fields in JSON, YAML, env files, headers and
// command flags ("password": ..., API_TOKEN=..., --token ...), and optional
// literal environment values. Generic key/value detections pass an entropy and
// placeholder check, so hashes, UUIDs, base64 images and "${TOKEN}" references
// are left alone.
//
// Each secret is replaced by a marker that names the rule, "[REDACTED:rule]",
// or the variable for an environment value, "[REDACTED:env:NAME]". Markers
// contain no quotes or backslashes, so redacted JSON stays valid. Callers get a
// Report (redacted:true with a count per rule) or, from Check, a structured
// *RejectError.
//
// The same engine serves strings (String, Bytes, Check, Findings) and streams
// (NewReader, NewWriter, Copy, WriteFile). An input of up to 128 KiB is one
// window. A longer one, and every stream, is buffered 128 KiB at a time and
// decided in windows of 8 KiB, each read with the 33 KiB lookahead after it,
// which covers everything a detector reads past where it matched. A window
// emits text in clear only before the positions it has decided; a secret it
// found that starts later (the password of a URL whose scheme it saw) is
// merged with the next window's findings. So a secret split across read or
// window boundaries is still found, the output is what one pass over the
// whole input produces whatever the chunking of reads or the window
// boundaries, and the engine's memory is bounded by its buffer and window
// whatever the input length (streaming 16 MiB, ordinary text or nothing but
// secrets, grows the live heap by about 0.4 MiB at most). String and Bytes
// also hold their result, one copy of the input's size, and String returns its
// input without copying when nothing is redacted; Check holds nothing;
// Findings holds one entry per secret.
//
// A private key is replaced between its BEGIN and END markers. When separators
// interrupt it (quoted lines joined by "+" or written as adjacent literals,
// "# " or "> " prefixes, a prefix such as `sb.append("` repeated on each line),
// they stay and each base64 line is replaced, so source code, comments and
// JSON strings keep their shape. Text between two markers that is not shaped
// like a key (code or prose that names both) is left alone.
//
// Key-aware detection reads how a value was written. A quoted value or YAML
// block scalar under a password-like key is always a secret unless it is a
// placeholder. So is a bare word on a data line: an env or dotenv assignment
// (DB_PASSWORD=..., export pw=...), a flag (--password ...), or a YAML
// "key: value" line. On a line that may be code (spaces around '=', a key in
// mid-line) a bare identifier is a reference (password = request_password).
//
// Known limits, which leave text in place rather than lose it: an unquoted
// token-like value needs a digit ("API_TOKEN=abcdefgh" is kept); an unquoted
// password is one word, so a multi-word YAML value ("password: correct horse")
// is not taken for one; validators judge the first 512 bytes of a
// value; a YAML block scalar is read for at most 3 KiB; and a private key is
// read for at most 32 KiB past its BEGIN marker. A key split by separators is
// taken only when its lines (all but the last) hold 40 bytes or more and its
// separators hold 32 bytes at most, with no words but a repeated prefix; a
// key interleaved with other text (records between its lines) is not. Without
// an END marker in that span, or with text between the markers that is not
// shaped like a key, a key is replaced only as far as its body runs
// uninterrupted, and only if that holds a mixed-case base64 run of 16 bytes,
// so a BEGIN marker named in prose or a log line costs nothing, and a
// truncated key split into quoted or commented lines keeps the lines after
// the first separator.
//
// Intended callers:
//
//   - Memory ingest (remember): Check to refuse content with a secret, or
//     String to store it redacted; surface the Report as `redacted`.
//   - Evidence capture: NewWriter over the spool writer, flushed at every
//     section boundary (the capture route wires it; see evidence.StreamRedactor).
//   - Transcript and session imports: NewReader per file, or Value per decoded
//     JSON record.
//   - Fixtures, overflow and handoff files: WriteFile (mode 0600, atomic).
//   - File reads and context injection: IsSecretPath (or MatchSecretPath, which
//     names the pattern for a structured refusal) before a path is opened.
//   - Request metadata in logs or fixtures: Headers.
//   - Rendering configuration: EnvRefs; redacting process output: WithEnv with
//     SecretEnv(os.Environ()).
package redact

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Rule identifiers. They appear in markers and reports.
const (
	RulePrivateKey     = "private_key"
	RuleAWSAccessKey   = "aws_access_key_id"
	RuleGitHubToken    = "github_token"
	RuleGitLabToken    = "gitlab_token"
	RuleSlackToken     = "slack_token"
	RuleSlackWebhook   = "slack_webhook"
	RuleAnthropicKey   = "anthropic_api_key"
	RuleOpenAIKey      = "openai_api_key"
	RuleGoogleAPIKey   = "google_api_key"
	RuleStripeKey      = "stripe_key"
	RuleNPMToken       = "npm_token"
	RuleHuggingFace    = "huggingface_token"
	RuleXmustardToken  = "xmustard_token"
	RuleJWT            = "jwt"
	RuleBearer         = "bearer_token"
	RuleBasicAuth      = "basic_auth"
	RuleURLCredentials = "url_credentials"
	RuleSecretField    = "secret_field"
	RuleEnv            = "env"
	RuleHeader         = "header"
	RuleEmail          = "email"
	RulePaymentCard    = "payment_card"
)

// Marker returns the replacement text for a redacted value.
func Marker(label string) string { return "[REDACTED:" + label + "]" }

// Report summarizes what a redaction replaced. It never contains secret values.
type Report struct {
	Redacted bool           `json:"redacted"`
	Count    int            `json:"count"`
	Rules    map[string]int `json:"rules,omitempty"`
}

func (r *Report) add(rule string, n int) {
	if n <= 0 {
		return
	}
	r.Redacted = true
	r.Count += n
	if r.Rules == nil {
		r.Rules = map[string]int{}
	}
	r.Rules[rule] += n
}

// Merge adds another report's counts to r.
func (r *Report) Merge(o Report) {
	for rule, n := range o.Rules {
		r.add(rule, n)
	}
}

func (r Report) clone() Report {
	out := Report{Redacted: r.Redacted, Count: r.Count}
	if r.Rules != nil {
		out.Rules = make(map[string]int, len(r.Rules))
		for k, v := range r.Rules {
			out.Rules[k] = v
		}
	}
	return out
}

// Finding locates one secret without revealing it.
type Finding struct {
	Rule   string `json:"rule"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// ErrSecret matches every *RejectError with errors.Is.
var ErrSecret = errors.New("secret detected")

// RejectError is the structured refusal returned by Check. It names the rules
// that matched and how many values, never the values themselves.
type RejectError struct {
	Code  string   `json:"code"`
	Rules []string `json:"rules"`
	Count int      `json:"count"`
}

func (e *RejectError) Error() string {
	return fmt.Sprintf("content contains %d secret value(s) (%s); remove them or refer to them by name", e.Count, strings.Join(e.Rules, ", "))
}

// Is reports whether target is ErrSecret.
func (e *RejectError) Is(target error) bool { return target == ErrSecret }

// Redactor applies one rule set. It is safe for concurrent use.
type Redactor struct {
	triggered []triggered         // evaluated only where one of their literals occurs
	scanners  []scanner           // evaluated over the whole window
	lits      [256][]literalEntry // trigger literals by first byte (both cases when folded)
	pairs     [256][4]uint64      // first two bytes of some literal (a 1-byte literal sets its whole row)
}

type literalEntry struct {
	lit      string
	fold     bool
	detector int
}

// anchorReach bounds how far before its literal a triggered detector's anchor
// can lie (an e-mail's local part before "@", a URL scheme before "://").
const anchorReach = 64

// triggered detectors can only match where one of their literals occurs.
type triggered interface {
	triggers() (literals []string, fold bool)
	// at appends the candidate for the literal occurrence (of length litLen)
	// at pos, if any. It returns skip: later occurrences of this detector's
	// literals before skip are redundant and are not evaluated. memo is the
	// detector's scratch for this window.
	at(buf []byte, pos, litLen, from, n int, eof bool, memo *runMemo, out []candidate) ([]candidate, int)
}

// scanners look at the whole window.
type scanner interface {
	// find appends candidates whose anchor lies in [from, to); it may read up
	// to n. buf[:from] is already-emitted context (at least one byte),
	// available for lookbehind. It returns where its scan stopped: a scan
	// that jumps over what it has read (cardRule over digit groups) may stop
	// past to, and the next window resumes it there (see engine.resume).
	find(buf []byte, from, to, n int, eof bool, out []candidate) ([]candidate, int)
}

func (r *Redactor) add(d triggered) {
	i := len(r.triggered)
	r.triggered = append(r.triggered, d)
	lits, fold := d.triggers()
	for _, lit := range lits {
		e := literalEntry{lit: lit, fold: fold, detector: i}
		for _, c0 := range cases(lit[0], fold) {
			r.lits[c0] = append(r.lits[c0], e)
			if len(lit) == 1 {
				r.pairs[c0] = [4]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
				continue
			}
			for _, c1 := range cases(lit[1], fold) {
				r.pairs[c0][c1>>6] |= 1 << (c1 & 63)
			}
		}
	}
}

func cases(c byte, fold bool) []byte {
	if fold && 'a' <= c && c <= 'z' {
		return []byte{c, c - 'a' + 'A'}
	}
	return []byte{c}
}

// trigger evaluates, in one pass over buf[from:to], every trigger literal
// occurrence that its detector has not ruled out, in position order; the
// detectors may read up to n. Hits are evaluated as they are found, so nothing
// grows with their number.
func (e *engine) trigger(buf []byte, from, to, n int, eof bool) {
	r := e.r
	for i := from; i < to; i++ {
		var c1 byte
		if i+1 < n {
			c1 = buf[i+1]
		}
		if r.pairs[buf[i]][c1>>6]&(1<<(c1&63)) == 0 {
			continue
		}
		for _, le := range r.lits[buf[i]] {
			if n-i < len(le.lit) || i < e.skip[le.detector] {
				continue
			}
			var ok bool
			if le.fold {
				ok = equalFoldASCII(buf[i:i+len(le.lit)], le.lit)
			} else {
				ok = string(buf[i:i+len(le.lit)]) == le.lit
			}
			if ok {
				e.cands, e.skip[le.detector] = r.triggered[le.detector].at(buf, i, len(le.lit), from, n, eof, &e.memo[le.detector], e.cands)
			}
		}
	}
}

// Option configures New.
type Option func(*config)

type config struct {
	env []EnvSecret
	pii bool
}

// WithEnv redacts each literal environment value, replacing it with
// "[REDACTED:env:NAME]". Values shorter than 8 or longer than 2048 bytes are
// ignored. Build the list with SecretEnv.
func WithEnv(secrets ...EnvSecret) Option {
	return func(c *config) { c.env = append(c.env, secrets...) }
}

// WithPII adds personal-data rules: email addresses and Luhn-valid payment
// card numbers. They are off by default because code and docs are full of
// maintainer emails.
func WithPII() Option { return func(c *config) { c.pii = true } }

// New builds a Redactor with the default secret rules plus any options.
func New(opts ...Option) *Redactor {
	var c config
	for _, o := range opts {
		o(&c)
	}
	r := &Redactor{}
	if lit := newLiteralRule(c.env); lit != nil {
		r.scanners = append(r.scanners, lit)
	}
	r.add(pemRule{})
	for _, t := range tokenRules() {
		r.add(t)
	}
	for _, t := range schemeRules {
		r.add(t)
	}
	r.add(urlCredRule{})
	r.scanners = append(r.scanners, keyedRule{})
	if c.pii {
		r.add(emailRule{})
		r.scanners = append(r.scanners, cardRule{})
	}
	return r
}

var (
	defaultOnce sync.Once
	defaultRed  *Redactor
)

// Default returns the shared Redactor with the default secret rules.
func Default() *Redactor {
	defaultOnce.Do(func() { defaultRed = New() })
	return defaultRed
}

// String redacts s. When nothing is redacted it returns s itself.
func (r *Redactor) String(s string) (string, Report) {
	e := &engine{r: r}
	var sb strings.Builder
	same := 0 // until the first redaction, the output is s[:same]
	runAll(e, s, func(out []byte) {
		if !e.rep.Redacted {
			same += len(out)
			return
		}
		if sb.Cap() == 0 {
			sb.Grow(len(s) + 64)
			sb.WriteString(s[:same])
		}
		sb.Write(out)
	})
	if !e.rep.Redacted {
		return s, e.rep
	}
	return sb.String(), e.rep
}

// Bytes redacts b and returns a new slice; b is not modified.
func (r *Redactor) Bytes(b []byte) ([]byte, Report) {
	e := &engine{r: r}
	out := make([]byte, 0, len(b)+64)
	runAll(e, b, func(chunk []byte) { out = append(out, chunk...) })
	return out, e.rep
}

// runAll runs e over all of in. An input that fits one window is processed in
// place of a Reader, exactly as a Reader's first and final window would be;
// a longer one goes through a Reader's fixed windows, so the engine's memory
// does not grow with the input. sink, if set, receives each output chunk and
// must copy what it keeps.
func runAll[T string | []byte](e *engine, in T, sink func([]byte)) {
	if len(in) <= windowSize {
		buf := make([]byte, 1+len(in))
		buf[0] = '\n' // synthetic left context: the start of input is a boundary
		copy(buf[1:], in)
		var out []byte
		if !e.discard {
			out = make([]byte, 0, len(in)+64)
		}
		e.base = -1
		out, _ = e.window(out, buf, 1, len(buf), true)
		if sink != nil {
			sink(out)
		}
		return
	}
	var src io.Reader
	switch v := any(in).(type) {
	case string:
		src = strings.NewReader(v)
	case []byte:
		src = bytes.NewReader(v)
	}
	rd := newReader(src, e)
	for !rd.done {
		rd.step()
		if sink != nil && len(rd.out) > 0 {
			sink(rd.out)
		}
	}
}

// Findings reports where secrets occur in s, without changing it.
func (r *Redactor) Findings(s string) []Finding {
	e := &engine{r: r, discard: true, collect: true}
	runAll(e, s, nil)
	return e.found
}

// Check returns a *RejectError when s contains a secret, and nil otherwise.
// Use it where a secret must be refused rather than rewritten.
func (r *Redactor) Check(s string) error {
	e := &engine{r: r, discard: true}
	runAll(e, s, nil)
	if !e.rep.Redacted {
		return nil
	}
	return &RejectError{Code: "secret_detected", Rules: e.order, Count: e.rep.Count}
}

// Value redacts a decoded JSON value (maps, slices, strings) and returns a deep
// copy. A string under a secret-looking key ("password", "api_key",
// "clientSecret", ...) is replaced whole when it passes the same checks as a
// quoted secret field; every other string is redacted as text.
func (r *Redactor) Value(v any) (any, Report) {
	var rep Report
	out := r.value(v, &rep)
	return out, rep
}

func (r *Redactor) value(v any, rep *Report) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if s, ok := val.(string); ok {
				if class := classifyKey([]byte(k)); class != keyNone && secretValue(class, []byte(s), ctxQuoted) {
					out[k] = Marker(RuleSecretField)
					rep.add(RuleSecretField, 1)
					continue
				}
			}
			out[k] = r.value(val, rep)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = r.value(item, rep)
		}
		return out
	case string:
		s, sub := r.String(x)
		rep.Merge(sub)
		return s
	}
	return v
}

// DefaultAllowedHeaders lists the headers whose values Headers keeps.
var DefaultAllowedHeaders = []string{
	"Accept", "Accept-Encoding", "Accept-Language", "Cache-Control", "Connection",
	"Content-Encoding", "Content-Length", "Content-Type", "Date", "Etag", "Expect",
	"Host", "Last-Event-Id", "Mcp-Protocol-Version", "Retry-After", "Transfer-Encoding",
	"User-Agent", "Vary", "X-Request-Id",
}

// deniedHeaders are always redacted, even when a caller allows them.
var deniedHeaders = map[string]bool{
	"Authorization": true, "Proxy-Authorization": true, "Cookie": true, "Set-Cookie": true,
	"Mcp-Session-Id": true, "X-Api-Key": true, "X-Auth-Token": true,
}

// Headers returns a copy of h safe to log or store: values of headers outside
// the allow-list (DefaultAllowedHeaders plus allow) become "[REDACTED:header]",
// credential headers are redacted even when allowed, and allowed values are
// still redacted as text. Header names are kept so the shape stays visible.
// An http.Header can be passed directly; convert the result with
// http.Header(out).
func (r *Redactor) Headers(h map[string][]string, allow ...string) (map[string][]string, Report) {
	allowed := make(map[string]bool, len(DefaultAllowedHeaders)+len(allow))
	for _, name := range DefaultAllowedHeaders {
		allowed[canonicalHeader(name)] = true
	}
	for _, name := range allow {
		allowed[canonicalHeader(name)] = true
	}
	var rep Report
	out := make(map[string][]string, len(h))
	for name, values := range h {
		canon := canonicalHeader(name)
		keep := allowed[canon] && !deniedHeaders[canon] && classifyKey([]byte(canon)) == keyNone
		redacted := make([]string, len(values))
		for i, v := range values {
			if !keep {
				redacted[i] = Marker(RuleHeader)
				rep.add(RuleHeader, 1)
				continue
			}
			s, sub := r.String(v)
			rep.Merge(sub)
			redacted[i] = s
		}
		out[name] = redacted
	}
	return out, rep
}

// canonicalHeader is the MIME canonical form of an ASCII header name
// ("x-api-key" → "X-Api-Key"), as net/http uses for map keys.
func canonicalHeader(name string) string {
	b := []byte(name)
	upper := true
	for i, c := range b {
		switch {
		case upper && 'a' <= c && c <= 'z':
			b[i] = c - 'a' + 'A'
		case !upper && 'A' <= c && c <= 'Z':
			b[i] = c - 'A' + 'a'
		}
		upper = c == '-'
	}
	return string(b)
}

// candidate is one detected secret inside a window.
type candidate struct {
	anchor     int    // where the detector matched; decides commit versus defer
	start, end int    // bytes replaced by the marker
	label      string // marker label
	rule       string // report rule
	cont       continuation
	prio       int32 // lower wins the label when regions merge
	scanner    int8  // 1 + index of the scanner that found it; 0 for a triggered detector
	covers     bool  // its scanner looks for nothing inside it (see engine.resume)
	head       bool  // the first part of a secret redacted in several parts
	part       bool  // a later part of the secret whose head shares its anchor: a marker, but no new count
	ext        bool  // continues the region that ended where this window's output resumes
}

// continuation extends a region that reached the end of a window before it
// could end (a long token, an unterminated quoted value).
type continuation interface {
	// advance consumes buf[from:n]. It returns where the region ends and whether
	// it is still open; an open region may stop short of n to keep a carry.
	advance(buf []byte, from, n int, eof bool) (end int, open bool)
}

// push appends c to out, doubling the capacity when it runs out: a window can
// hold thousands of candidates, and append's gentler growth for large slices
// would allocate several times the final size on the way there.
func push(out []candidate, c candidate) []candidate {
	if len(out) == cap(out) {
		grown := make([]candidate, len(out), 2*cap(out)+16)
		copy(grown, out)
		out = grown
	}
	return append(out, c)
}

// openRegion is a region carried into the next window.
type openRegion struct {
	cont    continuation
	scanner int8 // 1 + index of the scanner whose covering value this is, or 0
}

// engine runs the detectors over successive windows of one input.
type engine struct {
	r       *Redactor
	open    []openRegion
	cands   []candidate
	skip    []int
	memo    []runMemo
	rep     Report
	order   []string // rules in order of first redaction
	discard bool     // produce no output (Check, Findings)
	collect bool     // record Findings
	found   []Finding
	base    int // input offset of buf[0]; -1 while buf[0] is the synthetic newline
	// span, when set, is the most input one window decides, a final window
	// included (a stream's windows; see decideSpan). Zero decides all of a
	// final window.
	span int

	// evalFrom is the input offset from which detector positions still need
	// their final evaluation: positions in a window's lookahead are evaluated
	// again, with a full lookahead, in the next window.
	evalFrom int
	// lastStart and lastEnd are the input offsets of the last redacted region,
	// including what its continuation swallowed; lastFinding is its entry in
	// found.
	lastStart, lastEnd, lastFinding int
	// keyAnchor is the input offset of the anchor of the last secret redacted
	// in parts, and keyFinding its entry in found: its later parts extend it.
	keyAnchor, keyFinding int
	// resume holds, per scanner, the input offset its scan resumes from. A
	// scanner may skip what a value it found covers (keyedRule does not look
	// for keys inside a bare value) or what it has already read (cardRule
	// jumps over a run of digit groups); a later window skips it as well, so
	// where a window ends does not change what is found.
	resume []int
	// pending holds candidates, in input offsets, that a window decided but
	// could not emit: they start past what it consumed, where secrets anchored
	// in the next window may still precede or overlap them. The next window
	// merges them with its own.
	pending []candidate
}

// lookahead is how many bytes at the end of a non-final window are held back.
// Only positions before n-lookahead are decided in a window, so every decision
// sees at least lookahead bytes, which covers the longest any detector reads
// past its anchor (a private key body and its END marker), and does not depend
// on where the window ends.
const lookahead = pemSpan + 512

// window redacts buf[from:n], appending output to out, and returns how far the
// input was consumed. Unconsumed bytes must be presented again, after the
// consumed prefix (kept as context), in the next call. eof reports that the
// input ends at n; a window decides up to n then, or, with span set, up to
// span bytes past from, and leaves the rest to the next call as a non-final
// window leaves its lookahead.
//
// A region carried from the previous window (an open continuation) is swallowed
// first. Detectors then run from the first position that has not had its final
// evaluation, which may lie inside that region or in the previous window's
// lookahead (the Reader keeps enough context for it). A secret found there
// that reaches past what is already consumed extends the carried region when
// it starts inside it, exactly as one window over the whole input would merge
// the two.
//
// Output goes as far as the decided positions (limit) and, past them, only
// through the one redacted region that crosses limit, so every clear byte
// emitted precedes limit and every anchor inside an emitted byte range was
// evaluated. A candidate decided here that starts past that point (the
// password of a URL whose scheme precedes limit, a later line of a split key)
// waits in pending for the next window, where it is merged with what that
// window finds before it, as one window over the whole input would.
func (e *engine) window(out, buf []byte, from, n int, eof bool) ([]byte, int) {
	done := from // everything before done is emitted or swallowed
	if len(e.open) > 0 {
		still := e.open[:0]
		for _, o := range e.open {
			end, open := o.cont.advance(buf, from, n, eof)
			done = max(done, end)
			if o.scanner > 0 {
				e.resume[o.scanner-1] = max(e.resume[o.scanner-1], e.base+end)
			}
			if open {
				still = append(still, o)
			}
		}
		e.open = still
		e.extendRegion(done)
	}

	start := from
	if s := e.evalFrom - e.base; s < start {
		start = max(1, s)
	}
	e.cands = e.cands[:0]
	limit := n
	if !eof {
		limit = max(from, n-lookahead)
	}
	if e.span > 0 {
		limit = min(limit, from+e.span)
	}
	if limit < n {
		e.evalFrom = e.base + limit
	}
	// Only anchors before limit are decided here. With span set, the detectors
	// run over slices of at most span bytes, each bounded as a window is, and
	// each slice's candidates are settled before the next, so a window's
	// scratch holds a slice's worth, not all it evaluates again inside a
	// carried region.
	for lo := start; ; {
		hi := limit
		if e.span > 0 && lo+e.span < limit {
			hi = lo + e.span
		}
		k := len(e.cands)
		e.detect(buf, lo, hi, n, eof)
		e.cands = e.settle(e.cands, k, hi, done)
		if hi == limit {
			break
		}
		lo = hi
	}

	// The pending candidates join the rest.
	known := e.cands
	for _, c := range e.pending {
		c.anchor, c.start, c.end = c.anchor-e.base, c.start-e.base, c.end-e.base
		if c.cont != nil { // a value that ran to the previous window's end
			end, open := c.cont.advance(buf, c.end, n, eof)
			c.end = end
			if !open {
				c.cont = nil
			}
		}
		known = push(known, c)
	}
	e.pending = e.pending[:0]
	e.cands = known

	kept := known[:0]
	for _, c := range known {
		if c.start < done {
			if c.end <= done && c.cont == nil {
				continue // inside what was already redacted
			}
			c.ext = e.lastEnd == e.base+done && e.base+c.start >= e.lastStart
			c.start, c.end = done, max(c.end, done)
		}
		kept = append(kept, c)
	}
	slices.SortStableFunc(kept, commitOrder)

	// Commit, in start order, what starts before limit, extends the region
	// carried to done, or starts inside the region that crosses limit; the
	// rest waits for the next window.
	consumed := max(limit, done)
	m := 0
	for ; m < len(kept) && (kept[m].start < consumed || kept[m].ext); m++ {
		consumed = max(consumed, kept[m].end)
		if c := kept[m]; c.cont != nil {
			e.open = append(e.open, openRegion{c.cont, c.scanner})
		}
	}
	committed := kept[:m]
	for _, c := range kept[m:] {
		c.anchor, c.start, c.end = c.anchor+e.base, c.start+e.base, c.end+e.base
		e.pending = append(e.pending, c)
	}

	pos := done
	for i := 0; i < len(committed); {
		c := committed[i]
		end := c.end
		j := i + 1
		for j < len(committed) && committed[j].start < end {
			end = max(end, committed[j].end)
			j++
		}
		if !c.ext {
			if !e.discard {
				out = append(out, buf[pos:c.start]...)
				out = append(out, "[REDACTED:"...)
				out = append(out, c.label...)
				out = append(out, ']')
			}
			e.lastStart = e.base + c.start
		}
		switch {
		case c.ext:
			e.extendRegion(end)
		case c.part:
			if e.keyAnchor == e.base+c.anchor {
				e.lastFinding = e.keyFinding
			}
			e.extendRegion(end)
		default:
			if e.collect {
				e.found = append(e.found, Finding{Rule: c.rule, Offset: e.base + c.start, Length: end - c.start})
				e.lastFinding = len(e.found) - 1
			}
			if e.rep.Rules[c.rule] == 0 {
				e.order = append(e.order, c.rule)
			}
			e.rep.add(c.rule, 1)
		}
		for _, h := range committed[i:j] {
			if h.head {
				e.keyAnchor, e.keyFinding = e.base+h.anchor, e.lastFinding
			}
		}
		e.lastEnd = e.base + end
		pos = end
		i = j
	}
	if !e.discard && consumed > pos {
		out = append(out, buf[pos:consumed]...)
	}
	return out, consumed
}

// detect appends to e.cands what the detectors find anchored in [from, to):
// the triggered detectors evaluate their literals up to anchorReach past to,
// since an anchor may precede its literal, and each scanner starts where it
// resumes. Anchors before from were found by an earlier call.
func (e *engine) detect(buf []byte, from, to, n int, eof bool) {
	if len(e.skip) < len(e.r.triggered) {
		e.skip = make([]int, len(e.r.triggered))
		e.memo = make([]runMemo, len(e.r.triggered))
	}
	clear(e.skip)
	clear(e.memo)
	e.trigger(buf, from, min(n, to+anchorReach), n, eof)
	if len(e.resume) < len(e.r.scanners) {
		e.resume = make([]int, len(e.r.scanners))
	}
	for i, d := range e.r.scanners {
		k := len(e.cands)
		var stop int
		e.cands, stop = d.find(buf, min(to, max(from, e.resume[i]-e.base)), to, n, eof, e.cands)
		e.resume[i] = max(e.resume[i], e.base+stop)
		for ; k < len(e.cands); k++ {
			e.cands[k].scanner = int8(i + 1)
		}
	}
}

// settle drops, from the candidates found from index k on, those that the
// window does not keep: those anchored at or past to, which a later call
// evaluates again (the next window with its full lookahead), and those inside
// the region already redacted up to done. A candidate whose scanner looks for
// nothing inside it first moves that scanner's resume past it, so later calls
// skip it as the scanner itself does.
func (e *engine) settle(cands []candidate, k, to, done int) []candidate {
	kept := cands[:k]
	for _, c := range cands[k:] {
		if c.anchor >= to {
			continue
		}
		if c.covers {
			e.resume[c.scanner-1] = max(e.resume[c.scanner-1], e.base+c.end)
		} else {
			c.scanner = 0
		}
		if c.start < done && c.end <= done && c.cont == nil {
			continue // inside what was already redacted
		}
		kept = append(kept, c)
	}
	return kept
}

// commitOrder orders a window's candidates for commit: by start, the extension
// of the carried region first, then by priority (lower wins the label) and
// anchor. It allocates nothing, since a stream sorts once per window.
func commitOrder(a, b candidate) int {
	if c := cmp.Compare(a.start, b.start); c != 0 {
		return c
	}
	if a.ext != b.ext {
		if a.ext {
			return -1
		}
		return 1
	}
	return cmp.Or(cmp.Compare(a.prio, b.prio), cmp.Compare(a.anchor, b.anchor))
}

// extendRegion records that the last redacted region now reaches end (a
// window position); its finding grows to match.
func (e *engine) extendRegion(end int) {
	e.lastEnd = max(e.lastEnd, e.base+end)
	if e.collect && e.lastFinding < len(e.found) {
		f := &e.found[e.lastFinding]
		f.Length = max(f.Length, e.lastEnd-f.Offset)
	}
}

// EnvSecret is one environment variable whose value must never appear in
// stored or rendered text.
type EnvSecret struct {
	Name  string
	Value string
}

// SecretEnv selects the entries of an environment ("NAME=value" pairs, as from
// os.Environ) whose values should be redacted by name: variables whose name
// marks a secret (GITHUB_TOKEN, DB_PASSWORD, OPENAI_API_KEY, ...) and any
// variable whose value is itself a recognized credential. Values shorter than
// 8 bytes are skipped: they would redact ordinary words.
func SecretEnv(environ []string) []EnvSecret {
	var out []EnvSecret
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		value = strings.TrimSpace(value)
		if !ok || name == "" || len(value) < minLiteral || len(value) > maxLiteral {
			continue
		}
		if classifyKey([]byte(name)) != keyNone || credentialValue(value) {
			out = append(out, EnvSecret{Name: name, Value: value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// credentialValue reports whether a whole value matches a token rule.
func credentialValue(v string) bool {
	for _, f := range Default().Findings(v) {
		if f.Offset == 0 && f.Length == len(v) && f.Rule != RuleSecretField {
			return true
		}
	}
	return false
}

// EnvRefs returns env with every value replaced by a "${NAME}" reference, for
// showing a configuration (an MCP server entry, a hook command) without the
// values it was built from.
func EnvRefs(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for name := range env {
		out[name] = "${" + name + "}"
	}
	return out
}
