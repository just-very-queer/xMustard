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
// (NewReader, Copy, WriteFile). Input is processed in fixed 64 KiB windows with
// a held-back lookahead, so a secret split across read or window boundaries is
// still found, the output does not depend on how the source chunks its reads,
// and the engine's memory is bounded by the window whatever the input length
// (the 16 MiB stream test grows the live heap by about 0.2 MiB). String and
// Bytes also hold their result, one copy of the input's size, and String
// returns its input without copying when nothing is redacted; Check holds
// nothing; Findings holds one entry per secret.
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
// value; a YAML block scalar is read for at most 3 KiB; and a private key body
// is at most 32 KiB, ending without an END marker at the first byte a key body
// cannot hold (so a BEGIN marker in prose costs the rest of its phrase).
//
// Intended callers:
//
//   - Memory ingest (remember): Check to refuse content with a secret, or
//     String to store it redacted; surface the Report as `redacted`.
//   - Evidence capture: hash the original while storing redacted bytes with
//     r.NewReader(io.TeeReader(src, sha256.New())).
//   - Transcript and session imports: NewReader per file, or Value per decoded
//     JSON record.
//   - Fixtures, overflow and handoff files: WriteFile (mode 0600, atomic).
//   - Request metadata in logs or fixtures: Headers.
//   - Rendering configuration: EnvRefs; redacting process output: WithEnv with
//     SecretEnv(os.Environ()).
package redact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	// find appends candidates whose anchor lies in [from, n). buf[:from] is
	// already-emitted context (at least one byte), available for lookbehind.
	find(buf []byte, from, n int, eof bool, out []candidate) []candidate
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

// trigger evaluates, in one pass over buf[from:n], every trigger literal
// occurrence that its detector has not ruled out, in position order. Hits are
// evaluated as they are found, so nothing grows with their number.
func (e *engine) trigger(buf []byte, from, n int, eof bool) {
	r := e.r
	for i := from; i < n; i++ {
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
	prio       int    // lower wins the label when regions merge
	cont       continuation
}

// continuation extends a region that reached the end of a window before it
// could end (a long token, an unterminated quoted value, a PEM body).
type continuation interface {
	// advance consumes buf[from:n]. It returns where the region ends and whether
	// it is still open; an open region may stop short of n to keep a carry.
	advance(buf []byte, from, n int, eof bool) (end int, open bool)
}

// engine runs the detectors over successive windows of one input.
type engine struct {
	r       *Redactor
	open    []continuation
	cands   []candidate
	skip    []int
	memo    []runMemo
	rep     Report
	order   []string // rules in order of first redaction
	discard bool     // produce no output (Check, Findings)
	collect bool     // record Findings
	found   []Finding
	base    int // input offset of buf[0]; -1 while buf[0] is the synthetic newline
}

// lookahead is how many bytes at the end of a non-final window are held back:
// a detector's decision about a secret starting before n-lookahead is final.
const lookahead = 4 << 10

// window redacts buf[from:n], appending output to out, and returns how far the
// input was consumed. Unconsumed bytes must be presented again, after the
// consumed prefix (kept as context), in the next call.
func (e *engine) window(out, buf []byte, from, n int, eof bool) ([]byte, int) {
	if len(e.open) > 0 {
		frontier, still := from, e.open[:0]
		for _, c := range e.open {
			end, open := c.advance(buf, from, n, eof)
			if end > frontier {
				frontier = end
			}
			if open {
				still = append(still, c)
			}
		}
		e.open = still
		if e.collect && len(e.found) > 0 { // the open region is the last finding
			f := &e.found[len(e.found)-1]
			f.Length = max(f.Length, e.base+frontier-f.Offset)
		}
		if len(e.open) > 0 {
			return out, frontier
		}
		from = frontier
	}

	e.cands = e.cands[:0]
	if len(e.skip) < len(e.r.triggered) {
		e.skip = make([]int, len(e.r.triggered))
		e.memo = make([]runMemo, len(e.r.triggered))
	}
	clear(e.skip)
	clear(e.memo)
	e.trigger(buf, from, n, eof)
	for _, d := range e.r.scanners {
		e.cands = d.find(buf, from, n, eof, e.cands)
	}
	limit := n
	if !eof {
		limit = max(from, n-lookahead)
	}
	sort.SliceStable(e.cands, func(i, j int) bool { return e.cands[i].anchor < e.cands[j].anchor })

	frontier := limit
	committed := e.cands[:0]
	for _, c := range e.cands {
		if c.anchor >= frontier {
			break
		}
		committed = append(committed, c)
		if c.end > frontier {
			frontier = c.end
		}
		if c.cont != nil {
			e.open = append(e.open, c.cont)
		}
	}
	sort.SliceStable(committed, func(i, j int) bool {
		if committed[i].start != committed[j].start {
			return committed[i].start < committed[j].start
		}
		return committed[i].prio < committed[j].prio
	})

	pos := from
	for i := 0; i < len(committed); {
		c := committed[i]
		end := c.end
		j := i + 1
		for j < len(committed) && committed[j].start < end {
			end = max(end, committed[j].end)
			j++
		}
		if !e.discard {
			out = append(out, buf[pos:c.start]...)
			out = append(out, "[REDACTED:"...)
			out = append(out, c.label...)
			out = append(out, ']')
		}
		if e.collect {
			e.found = append(e.found, Finding{Rule: c.rule, Offset: e.base + c.start, Length: end - c.start})
		}
		if e.rep.Rules[c.rule] == 0 {
			e.order = append(e.order, c.rule)
		}
		e.rep.add(c.rule, 1)
		pos = end
		i = j
	}
	if !e.discard && frontier > pos {
		out = append(out, buf[pos:frontier]...)
	}
	return out, frontier
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
