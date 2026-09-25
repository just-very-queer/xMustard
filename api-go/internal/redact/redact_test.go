package redact

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// Test secrets are assembled at run time so the source never contains a
// literal credential that a secret scanner would flag.
func join(parts ...string) string { return strings.Join(parts, "") }

func randToken(rng *rand.Rand, alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

const (
	alphaNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	urlSafe  = alphaNum + "_-"
	upperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0234567"
)

type sample struct {
	name   string
	text   string // input containing the secret
	secret string // the part that must disappear
	rule   string // rule that must be reported
}

func secretCorpus() []sample {
	rng := rand.New(rand.NewSource(7))
	gh := join("gh", "p_", randToken(rng, alphaNum, 36))
	ghPat := join("github", "_pat_", randToken(rng, alphaNum+"_", 82))
	slack := join("xox", "b-", "1234567890", "-", "9876543210", "-", randToken(rng, alphaNum, 24))
	slackHook := join("https://hooks.", "slack.com/services/", "T0", randToken(rng, upperNum, 8), "/B0", randToken(rng, upperNum, 8), "/", randToken(rng, alphaNum, 24))
	openai := join("sk-", "proj-", randToken(rng, urlSafe, 120))
	openaiLegacy := join("sk-", randToken(rng, alphaNum, 20), "T3Blbk", "FJ", randToken(rng, alphaNum, 20))
	anthropic := join("sk-", "ant-", "api03-", randToken(rng, urlSafe, 93), "AA")
	aws := join("AK", "IA", randToken(rng, upperNum, 16))
	awsSecret := randToken(rng, alphaNum+"/+", 40)
	google := join("AI", "za", randToken(rng, urlSafe, 35))
	stripe := join("sk", "_live_", randToken(rng, alphaNum, 24))
	npm := join("np", "m_", randToken(rng, alphaNum, 36))
	hf := join("h", "f_", randToken(rng, alphaNum, 34))
	xmt := join("xm", "t_", randToken(rng, urlSafe, 43))
	jwt := join("ey", "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", ".", "ey", "JzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkphbmUifQ", ".", randToken(rng, urlSafe, 43))
	bearer := randToken(rng, alphaNum, 40)
	basic := base64.StdEncoding.EncodeToString([]byte("deploy:" + randToken(rng, alphaNum, 16)))
	pemBody := randToken(rng, alphaNum+"+/", 64) + "\n" + randToken(rng, alphaNum+"+/", 64) + "\n" + randToken(rng, alphaNum+"+/", 20) + "=="
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	pw := "hunter2-" + randToken(rng, alphaNum, 6)
	apiHex := randToken(rng, "0123456789abcdef", 32)

	return []sample{
		{"bearer header", "Authorization: Bearer " + bearer + "\n", bearer, RuleBearer},
		{"bearer in curl", `curl -H "authorization: bearer ` + bearer + `" https://api`, bearer, RuleBearer},
		{"basic auth", "Authorization: Basic " + basic, basic, RuleBasicAuth},
		{"aws key id", "aws_access_key_id = " + aws, aws, RuleAWSAccessKey},
		{"aws secret", "aws_secret_access_key = " + awsSecret + "\n", awsSecret, RuleSecretField},
		{"github classic", "token " + gh + " expires", gh, RuleGitHubToken},
		{"github fine-grained", "GH=" + ghPat, ghPat, RuleGitHubToken},
		{"slack bot token", `{"slack":"` + slack + `"}`, slack, RuleSlackToken},
		{"slack webhook", "post to " + slackHook + " now", slackHook[len("https://hooks.slack.com/services/"):], RuleSlackWebhook},
		{"openai project key", "OPENAI=" + openai, openai, RuleOpenAIKey},
		{"openai legacy key", "key " + openaiLegacy + ".", openaiLegacy, RuleOpenAIKey},
		{"anthropic key", "x-api-key: " + anthropic, anthropic, RuleAnthropicKey},
		{"google api key", "?key=" + google + "&q=1", google, RuleGoogleAPIKey},
		{"stripe key", "stripe " + stripe, stripe, RuleStripeKey},
		{"npm token", "//registry.npmjs.org/:_authToken=" + npm, npm, RuleNPMToken},
		{"huggingface token", "HF " + hf, hf, RuleHuggingFace},
		{"xmustard token", "XMUSTARD_API_TOKEN " + xmt, xmt, RuleXmustardToken},
		{"jwt", "id_token " + jwt, jwt, RuleJWT},
		{"pem block", "key:\n" + begin + "\n" + pemBody + "\n" + end + "\n", pemBody, RulePrivateKey},
		{"pem in json field", `{"private_key":"` + begin + `\n` + strings.ReplaceAll(pemBody, "\n", `\n`) + `\n` + end + `\n"}`,
			strings.ReplaceAll(pemBody, "\n", `\n`), RuleSecretField},
		{"pem in json text", `{"note":"key follows ` + begin + `\n` + strings.ReplaceAll(pemBody, "\n", `\n`) + `\n` + end + `\n"}`,
			strings.ReplaceAll(pemBody, "\n", `\n`), RulePrivateKey},
		{"json password", `{"user":"deploy","password":"` + pw + `"}`, pw, RuleSecretField},
		{"json client secret", `{"clientSecret": "` + apiHex + `"}`, apiHex, RuleSecretField},
		{"double-encoded json", `{"arguments":"{\"password\":\"` + pw + `\",\"x\":1}"}`, pw, RuleSecretField},
		{"yaml api key", "service:\n  api_key: " + apiHex + "\n", apiHex, RuleSecretField},
		{"env file", "export DB_PASSWORD=" + pw + "\n", pw, RuleSecretField},
		{"quoted assignment in code", `const dbPassword = "` + pw + `";`, pw, RuleSecretField},
		{"flag with equals", "psql --password=" + pw + " -h db", pw, RuleSecretField},
		{"flag with space", "deploy --api-key " + apiHex + " --force", apiHex, RuleSecretField},
		{"url userinfo", "DATABASE_URL=postgres://admin:" + pw + "@db:5432/app", pw, RuleURLCredentials},
		{"header line", "X-Api-Key: " + apiHex, apiHex, RuleSecretField},
	}
}

func TestSecretCorpus(t *testing.T) {
	r := Default()
	for _, s := range secretCorpus() {
		t.Run(s.name, func(t *testing.T) {
			out, rep := r.String(s.text)
			if strings.Contains(out, s.secret) {
				t.Fatalf("secret survived:\n in: %s\nout: %s", s.text, out)
			}
			if !rep.Redacted || rep.Count < 1 || rep.Rules[s.rule] < 1 {
				t.Fatalf("report %+v does not name %s (out %q)", rep, s.rule, out)
			}
			if !strings.Contains(out, "[REDACTED:") {
				t.Fatalf("no marker in %q", out)
			}
			if err := r.Check(s.text); !errors.Is(err, ErrSecret) {
				t.Fatalf("Check should reject: %v", err)
			}
		})
	}
}

func TestRedactedJSONStaysValid(t *testing.T) {
	r := Default()
	for _, s := range secretCorpus() {
		if !json.Valid([]byte(s.text)) {
			continue
		}
		out, _ := r.String(s.text)
		if !json.Valid([]byte(out)) {
			t.Errorf("%s: redaction broke JSON: %s", s.name, out)
		}
	}
}

func TestKeepsContextAroundSecrets(t *testing.T) {
	r := Default()
	cases := map[string]string{
		`{"user":"deploy","password":"p4ss-w0rd!"}`:   `{"user":"deploy","password":"[REDACTED:secret_field]"}`,
		"Authorization: Bearer abcDEF123456ghiJKL789": "Authorization: Bearer [REDACTED:bearer_token]",
		"export DB_PASSWORD=s3cr3t!pw\nexport X=1":    "export DB_PASSWORD=[REDACTED:secret_field]\nexport X=1",
		"postgres://admin:S3cretPass@db:5432/app":     "postgres://admin:[REDACTED:url_credentials]@db:5432/app",
	}
	for in, want := range cases {
		if got, _ := r.String(in); got != want {
			t.Errorf("String(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	begin, end := join("-----BEGIN ", "OPENSSH PRIVATE", " KEY-----"), join("-----END ", "OPENSSH PRIVATE", " KEY-----")
	got, _ := r.String(begin + "\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\n" + end)
	if got != begin+"[REDACTED:private_key]"+end {
		t.Errorf("PEM markers should frame the redaction, got %q", got)
	}
}

func falsePositiveCorpus() []string {
	rng := rand.New(rand.NewSource(11))
	png := make([]byte, 48<<10)
	rng.Read(png)
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	return []string{
		"sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"commit 3f5ba7e1c2d4e6f8a0b1c3d5e7f9a1b3c5d7e9f1",
		`{"content_digest":"sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"}`,
		"id 123e4567-e89b-12d3-a456-426614174000 and 550E8400-E29B-41D4-A716-446655440000",
		`{"session_id":"6f1c2a9e-8b7d-4c3e-9f0a-1b2c3d4e5f60","hook_event_name":"PostToolUse"}`,
		`<img src="` + image + `">`,
		image,
		`password = request.form["password"]`,
		"def login(password: str, token: Optional[str]) -> bool:",
		"secret = self.secret",
		`const token = process.env.GITHUB_TOKEN;`,
		`{"token_type":"bearer","expires_in":3600,"max_tokens":4096}`,
		`{"primary_key":"id","sort_key":"created_at","cache_key":"a1b2c3"}`,
		`api_key: ${OPENAI_API_KEY}`,
		`password: "<your-password>"`,
		`GITHUB_TOKEN=$GITHUB_TOKEN`,
		`token: YOUR_API_TOKEN`,
		`{"password":"","secret":null}`,
		"Bearer authentication sends a token in the Authorization header.",
		"We need a basic understanding of the problem first.",
		"the task-management-service-integration-layer handles retries",
		"evidence handle xm1.q2Zb3Jw7Kp0LmN4sT6vX8yA1cE3gI5kM7oQ9sU1wY3a",
		"if password == other.password { return true }",
		"author: Jane Doe <jane@example.com>",
		"https://github.com/org/repo/blob/main/README.md#usage",
		"git@github.com:org/repo.git",
		"std::collections::HashMap<String, Vec<u8>>",
		"skipping sk-learn-compat because it is deprecated",
		"AKIAIOSFODNN7EXAMPLEEXTRA is not a key id",
		"--password-file /run/secrets/db --token-path ~/.config/token",
		"db:\n  password: |\n",
		`Password: os.Getenv("DB_PASSWORD"),`,
		"level=info msg=ok token_count=5 auth_mode=oidc",
	}
}

func TestFalsePositiveCorpus(t *testing.T) {
	r := Default()
	for i, in := range falsePositiveCorpus() {
		out, rep := r.String(in)
		if out != in || rep.Redacted || rep.Count != 0 {
			shown := in
			if len(shown) > 120 {
				shown = shown[:120] + "..."
			}
			t.Errorf("case %d redacted a non-secret (%+v):\n in: %s\nout: %.200s", i, rep, shown, out)
		}
		if err := r.Check(in); err != nil {
			t.Errorf("case %d: Check rejected a non-secret: %v", i, err)
		}
	}
}

func TestEntropy(t *testing.T) {
	if Entropy("") != 0 || Entropy("aaaa") != 0 {
		t.Fatal("constant strings have zero entropy")
	}
	if e := Entropy("ab"); e != 1 {
		t.Fatalf("Entropy(ab) = %v", e)
	}
	for _, s := range []string{"8f14e45fceea167a5a36dedd4bea2543", "wJalrXUtnFEMIK7MDENGbPxRfiCY", "a8F2kQ9z"} {
		if !LooksRandom(s) {
			t.Errorf("LooksRandom(%q) = false", s)
		}
	}
	for _, s := range []string{"password", "aaaaaaaaaaaaaaaa", "short", "tokenization"} {
		if LooksRandom(s) {
			t.Errorf("LooksRandom(%q) = true", s)
		}
	}
}

func TestClassifyKey(t *testing.T) {
	cases := map[string]keyClass{
		"password": keyPassword, "DB_PASSWORD": keyPassword, "clientSecret": keyPassword, "db_pass": keyPassword,
		"passphrase": keyPassword, "access_token": keyToken, "githubToken": keyToken, "x-api-key": keyToken,
		"APIKey": keyToken, "aws_secret_access_key": keyToken, "Authorization": keyToken, "Set-Cookie": keyToken,
		"credentials": keyToken, "http_auth": keyToken, "private_key": keyToken,
		"token_type": keyNone, "max_tokens": keyNone, "session_id": keyNone, "primary_key": keyNone,
		"author": keyNone, "bypass": keyNone, "secret_name": keyNone, "PWD": keyNone, "passport": keyNone,
		"tokenizer": keyNone, "keyboard": keyNone,
	}
	for key, want := range cases {
		if got := classifyKey([]byte(key)); got != want {
			t.Errorf("classifyKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestFindingsAndReject(t *testing.T) {
	r := Default()
	gh := join("gh", "o_", strings.Repeat("aB3", 12))
	text := "a " + gh + " b password=Zx9!Zx9!"
	found := r.Findings(text)
	if len(found) != 2 {
		t.Fatalf("findings = %+v", found)
	}
	if f := found[0]; f.Rule != RuleGitHubToken || text[f.Offset:f.Offset+f.Length] != gh {
		t.Fatalf("finding does not locate the token: %+v", f)
	}
	err := r.Check(text)
	var rej *RejectError
	if !errors.As(err, &rej) || rej.Count != 2 || strings.Join(rej.Rules, ",") != "github_token,secret_field" {
		t.Fatalf("reject = %#v", err)
	}
	b, _ := json.Marshal(rej)
	if strings.Contains(string(b), gh) || strings.Contains(err.Error(), gh) || !strings.Contains(string(b), `"code":"secret_detected"`) {
		t.Fatalf("reject leaks or lacks structure: %s / %v", b, err)
	}
	if r.Check("nothing here") != nil {
		t.Fatal("clean text must pass")
	}
}

func TestValueRedactsStructuredData(t *testing.T) {
	gh := join("gh", "s_", strings.Repeat("Qw9", 12))
	in := map[string]any{
		"tool_input": map[string]any{"command": "curl -H 'Authorization: token " + gh + "'", "timeout": float64(30)},
		"headers":    []any{map[string]any{"X-Api-Key": "k1Lm2Np3Qr4St5"}, "plain"},
		"password":   "correct horse battery staple",
		"session_id": "6f1c2a9e-8b7d-4c3e-9f0a-1b2c3d4e5f60",
	}
	before, _ := json.Marshal(in)
	out, rep := Default().Value(in)
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("Value mutated its input")
	}
	enc, _ := json.Marshal(out)
	for _, leak := range []string{gh, "k1Lm2Np3Qr4St5", "correct horse"} {
		if strings.Contains(string(enc), leak) {
			t.Fatalf("leaked %q: %s", leak, enc)
		}
	}
	if !strings.Contains(string(enc), "6f1c2a9e-8b7d") || !strings.Contains(string(enc), `"timeout":30`) {
		t.Fatalf("non-secrets must survive: %s", enc)
	}
	if rep.Count != 3 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestHeadersAllowList(t *testing.T) {
	for in, want := range map[string]string{"x-api-key": "X-Api-Key", "CONTENT-type": "Content-Type", "mcp-session-id": "Mcp-Session-Id"} {
		if got := canonicalHeader(in); got != want || got != http.CanonicalHeaderKey(in) {
			t.Errorf("canonicalHeader(%q) = %q, want %q", in, got, want)
		}
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer abc")
	h.Set("Cookie", "sid=1")
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "claude-code/2.1 key="+join("sk-", "ant-", strings.Repeat("x1Y", 10)))
	h.Set("X-Custom-Trace", "abc")
	raw, rep := Default().Headers(h, "Authorization", "x-custom-trace")
	out := http.Header(raw)
	if out.Get("Authorization") != "[REDACTED:header]" || out.Get("Cookie") != "[REDACTED:header]" {
		t.Fatalf("credential headers must always be redacted: %v", out)
	}
	if out.Get("Content-Type") != "application/json" || out.Get("X-Custom-Trace") != "abc" {
		t.Fatalf("allowed headers must be kept: %v", out)
	}
	if !strings.HasPrefix(out.Get("User-Agent"), "claude-code/2.1 key=[REDACTED:") {
		t.Fatalf("allowed values are still redacted as text: %q", out.Get("User-Agent"))
	}
	if h.Get("Authorization") != "Bearer abc" {
		t.Fatal("Headers mutated its input")
	}
	if rep.Rules[RuleHeader] != 2 || rep.Rules[RuleAnthropicKey] != 1 {
		t.Fatalf("report = %+v", rep)
	}
	raw, _ = Default().Headers(http.Header{"X-Unlisted": {"v"}})
	if http.Header(raw).Get("X-Unlisted") != "[REDACTED:header]" {
		t.Fatalf("headers outside the allow-list are redacted: %v", out)
	}
}

func TestEnvValuesReplacedByName(t *testing.T) {
	stripeLike := join("rk", "_live_", strings.Repeat("Zz9", 8))
	environ := []string{
		"PATH=/usr/bin:/bin", "HOME=/Users/dev", "PWD=/repo", "SHORT_TOKEN=abc",
		"DEPLOY_PASSWORD=Pl41n-Text-Pw", "OPENAI_API_KEY=local-proxy-key-123", "UNRELATED=" + stripeLike,
	}
	secrets := SecretEnv(environ)
	names := make([]string, len(secrets))
	for i, s := range secrets {
		names[i] = s.Name
	}
	if strings.Join(names, ",") != "DEPLOY_PASSWORD,OPENAI_API_KEY,UNRELATED" {
		t.Fatalf("SecretEnv selected %v", names)
	}
	r := New(WithEnv(secrets...))
	out, rep := r.String("using local-proxy-key-123 as fallback; pw Pl41n-Text-Pw; home /Users/dev")
	want := "using [REDACTED:env:OPENAI_API_KEY] as fallback; pw [REDACTED:env:DEPLOY_PASSWORD]; home /Users/dev"
	if out != want || rep.Rules[RuleEnv] != 2 {
		t.Fatalf("got %q %+v", out, rep)
	}
	refs := EnvRefs(map[string]string{"GITHUB_TOKEN": "x", "A": "y"})
	if refs["GITHUB_TOKEN"] != "${GITHUB_TOKEN}" || refs["A"] != "${A}" || len(refs) != 2 {
		t.Fatalf("EnvRefs = %v", refs)
	}
}

func TestPIIIsOptIn(t *testing.T) {
	in := "mail jane.doe@example.com card 4111 1111 1111 1111 not 4111 1111 1111 1112"
	if out, _ := Default().String(in); out != in {
		t.Fatalf("PII redacted by default: %q", out)
	}
	out, rep := New(WithPII()).String(in)
	if out != "mail [REDACTED:email] card [REDACTED:payment_card] not 4111 1111 1111 1112" || rep.Count != 2 {
		t.Fatalf("got %q %+v", out, rep)
	}
}

func TestWriteFilePrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := join("gh", "u_", strings.Repeat("R7t", 12))
	rep, err := Default().WriteFile(path, strings.NewReader(`{"token":"`+secret+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), secret) || rep.Count != 1 {
		t.Fatalf("file not redacted: %s %+v", data, rep)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
	if _, err := Default().WriteFile(filepath.Join(dir, "missing", "x"), strings.NewReader("x")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

// --- streaming ---

// chunkReader returns the input in pseudo-random chunk sizes.
type chunkReader struct {
	data []byte
	rng  *rand.Rand
	max  int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := 1 + c.rng.Intn(c.max)
	n = min(n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func streamAll(t *testing.T, r *Redactor, src io.Reader) (string, Report) {
	t.Helper()
	var out bytes.Buffer
	_, rep, err := r.Copy(&out, src)
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), rep
}

// filler is prose-like text with no secrets.
func filler(rng *rand.Rand, n int) string {
	words := []string{"the", "index", "memory", "verify", "path", "func", "return", "err", "nil", "{", "}", "\n", "x := 1", "// note", "a-b", "key", "value", "::", "==", "https://example.com/a"}
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(words[rng.Intn(len(words))])
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

// A secret placed across every window boundary position near the first
// window's end is redacted exactly as in one-shot mode, under one-byte and
// random-size reads.
func TestStreamSecretsAcrossWindowBoundaries(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(3))
	first := contextLen + windowSize - 1 // input bytes in the first window
	offsets := []int{}
	for d := -lookahead - 300; d <= 300; d += 37 {
		offsets = append(offsets, first+d)
	}
	for _, d := range []int{-lookahead - 1, -lookahead, -lookahead + 1, -1, 0, 1} {
		offsets = append(offsets, first+d)
	}
	corpus := secretCorpus()
	for i, off := range offsets {
		s := corpus[i%len(corpus)]
		in := filler(rng, off) + " " + s.text + " " + filler(rng, 2000)
		want, _ := r.String(in)
		if strings.Contains(want, s.secret) {
			t.Fatalf("one-shot leaked %s at %d", s.name, off)
		}
		var src io.Reader = &chunkReader{data: []byte(in), rng: rng, max: 5000}
		if i%4 == 0 {
			src = iotest.OneByteReader(strings.NewReader(in))
		}
		got, _ := streamAll(t, r, src)
		if got != want {
			t.Fatalf("%s at offset %d: stream differs from one-shot", s.name, off)
		}
	}
}

// Randomized: long inputs with many secrets at random positions, random read
// sizes. Output equals one-shot output and no secret survives.
func TestStreamMatchesOneShotRandomized(t *testing.T) {
	r := Default()
	corpus := secretCorpus()
	for seed := int64(1); seed <= 6; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var b strings.Builder
		var secrets []string
		for b.Len() < 400<<10 {
			b.WriteString(filler(rng, rng.Intn(9000)))
			s := corpus[rng.Intn(len(corpus))]
			b.WriteString(" " + s.text + " ")
			secrets = append(secrets, s.secret)
		}
		in := b.String()
		want, wantRep := r.String(in)
		got, gotRep := streamAll(t, r, &chunkReader{data: []byte(in), rng: rng, max: 1 + rng.Intn(20000)})
		if got != want {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			t.Fatalf("seed %d: stream differs from one-shot at %d:\n got %.80q\nwant %.80q", seed, i, got[i:], want[i:])
		}
		if gotRep.Count != wantRep.Count {
			t.Fatalf("seed %d: report %d vs %d", seed, gotRep.Count, wantRep.Count)
		}
		for _, s := range secrets {
			if strings.Contains(got, s) {
				t.Fatalf("seed %d: secret survived streaming", seed)
			}
		}
	}
}

// Secrets longer than the lookahead or than a whole window are redacted in
// full by the continuation states.
func TestStreamLongSecretsSpanWindows(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(5))
	longToken := join("gh", "p_", randToken(rng, alphaNum, 150<<10))
	longValue := randToken(rng, alphaNum+"+/", 200<<10)
	longJSON := randToken(rng, alphaNum, 90<<10) + `\"` + randToken(rng, alphaNum, 90<<10)
	begin, end := join("-----BEGIN ", "PRIVATE", " KEY-----"), join("-----END ", "PRIVATE", " KEY-----")
	pemBody := randToken(rng, alphaNum+"+/\n", 180<<10)
	cases := []struct {
		name, in, secret, tail string
	}{
		{"long token", filler(rng, 60<<10) + " " + longToken + " tail", longToken, " tail"},
		{"long unquoted value", filler(rng, 61<<10) + "\nAPI_TOKEN=" + longValue + "1\nnext", longValue, "\nnext"},
		{"long json value", filler(rng, 62<<10) + `{"password":"` + longJSON + `","n":1}`, longJSON, `","n":1}`},
		{"long pem", filler(rng, 63<<10) + "\n" + begin + "\n" + pemBody + "\n" + end + "\nafter", pemBody, end + "\nafter"},
	}
	for _, tc := range cases {
		want, _ := r.String(tc.in)
		for _, src := range []io.Reader{strings.NewReader(tc.in), &chunkReader{data: []byte(tc.in), rng: rng, max: 777}} {
			got, rep := streamAll(t, r, src)
			if got != want {
				t.Fatalf("%s: stream differs from one-shot", tc.name)
			}
			if strings.Contains(got, tc.secret[:64]) || strings.Contains(got, tc.secret[len(tc.secret)-64:]) {
				t.Fatalf("%s: part of the secret survived", tc.name)
			}
			if !strings.HasSuffix(got, tc.tail) || rep.Count != 1 {
				t.Fatalf("%s: wrong tail or count %+v: %.60q", tc.name, rep, got[max(0, len(got)-60):])
			}
		}
	}
}

// A PEM END marker split across a window boundary still closes the block.
func TestStreamPEMEndMarkerAcrossBoundary(t *testing.T) {
	r := Default()
	begin, end := join("-----BEGIN ", "EC PRIVATE", " KEY-----"), join("-----END ", "EC PRIVATE", " KEY-----")
	first := contextLen + windowSize - 1
	for split := 1; split < len(end); split += 3 {
		head := strings.Repeat("a", first-lookahead-200)
		body := strings.Repeat("Q", lookahead+200-len(begin)-split)
		in := head + begin + body + end + "\nvisible"
		want, _ := r.String(in)
		got, _ := streamAll(t, r, iotest.HalfReader(strings.NewReader(in)))
		if got != want || !strings.HasSuffix(got, begin+"[REDACTED:private_key]"+end+"\nvisible") {
			t.Fatalf("split %d: got suffix %q", split, got[max(0, len(got)-80):])
		}
	}
}

// A double-encoded value whose closing \" is split across a window boundary
// keeps its backslash: the output is the one-shot output and valid JSON.
func TestStreamEscapedQuoteAcrossBoundary(t *testing.T) {
	r := Default()
	first := contextLen + windowSize - 1 // input bytes in the first window
	for shift := -2; shift <= 2; shift++ {
		prefix := `{"filler":"` + strings.Repeat("f", 1000) + `","arguments":"{\"password\":\"`
		value := strings.Repeat("s3cR", (first-len(prefix)-1+shift)/4)
		value += strings.Repeat("x", first-len(prefix)-1+shift-len(value))
		in := prefix + value + `\",\"n\":1}"}`
		if !json.Valid([]byte(in)) {
			t.Fatal("fixture is not JSON")
		}
		want, _ := r.String(in)
		got, _ := streamAll(t, r, strings.NewReader(in))
		if got != want || !json.Valid([]byte(got)) || strings.Contains(got, "s3cR") {
			t.Fatalf("shift %d: got suffix %q, want %q", shift, got[max(0, len(got)-40):], want[max(0, len(want)-40):])
		}
	}
}

// A trigger literal inside the run of a failed or accepted match must still
// be evaluated when its own match could differ.
func TestNestedTriggerLiterals(t *testing.T) {
	r := Default()
	slack := join("xox", "b-", "1234567890", "abcdef1")
	xapp := join("xa", "pp-1-", "ABCDEFGHIJ1")
	google := join("AI", "za", strings.Repeat("Q", 30), "-", "AI", "za", strings.Repeat("R7", 17), "Z")
	cases := map[string]string{
		"xoxz-" + slack + " end":  slack,
		"xapp-x-" + xapp + " end": xapp,
		google + " end":           google[len(google)-39:],
	}
	for in, secret := range cases {
		if out, _ := r.String(in); strings.Contains(out, secret) {
			t.Errorf("nested token survived: %q", out)
		}
	}
}

// Adversarial inputs (a trigger literal repeated inside its own token
// alphabet, separators inside one long value, nested PEM markers) must be
// redacted in linear time; before the fix, 2 MiB of "sk-sk-sk-..." took
// minutes. The bound is generous so a slow CI machine does not flake.
func TestAdversarialInputsAreLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("adversarial inputs")
	}
	const size = 2 << 20
	rep := func(unit string) string { return strings.Repeat(unit, size/len(unit)+1)[:size] }
	inputs := map[string]string{
		"openai literal run":    rep("sk-"),
		"anthropic literal run": rep("sk-ant-"),
		"jwt literal run":       rep("eyJ"),
		"github pat run":        rep("github_pat_"),
		"xmustard token run":    rep("xmt_"),
		"slack run":             rep("xoxb-"),
		"keyed separators":      rep("token:"),
		"keyed equals":          rep("api_key=api_key="),
		"flags":                 rep("--token --token "),
		"pem begin markers":     rep(join("-----BEGIN ", "RSA PRIVATE", " KEY-----")),
		"bearer words":          rep("bearer bearer "),
		"url separators":        rep("a://b:c"),
		"quotes":                rep(`"password":"`),
	}
	start := time.Now()
	for name, in := range inputs {
		t0 := time.Now()
		if _, _, err := Default().Copy(io.Discard, strings.NewReader(in)); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(t0); d > 5*time.Second {
			t.Errorf("%s: %v for %d MiB (superlinear?)", name, d, size>>20)
		}
	}
	t.Logf("all adversarial inputs in %v", time.Since(start))
}

// errAfterReader yields its data and then fails.
type errAfterReader struct{ data []byte }

func (e *errAfterReader) Read(p []byte) (int, error) {
	if len(e.data) == 0 {
		return 0, errors.New("disk on fire")
	}
	n := copy(p, e.data)
	e.data = e.data[n:]
	return n, nil
}

func TestStreamReadErrorWithholdsUndecidedTail(t *testing.T) {
	partial := join("gh", "p_", strings.Repeat("k", 20)) // truncated: not yet a full token
	in := strings.Repeat("safe ", 30000) + partial
	var out bytes.Buffer
	_, _, err := Default().Copy(&out, &errAfterReader{data: []byte(in)})
	if err == nil || err.Error() != "disk on fire" {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), partial[:10]) {
		t.Fatal("undecided tail (a possible secret prefix) was emitted")
	}
	if out.Len() == 0 || out.Len() > len(in)-lookahead+1 {
		t.Fatalf("emitted %d of %d bytes", out.Len(), len(in))
	}
}

// genReader produces n bytes of filler with a secret every ~4 KiB, without
// holding the stream in memory.
type genReader struct {
	left    int
	rng     *rand.Rand
	pending []byte
	secrets int
	onChunk func()
	chunks  int
}

func (g *genReader) Read(p []byte) (int, error) {
	if g.left <= 0 {
		return 0, io.EOF
	}
	if len(g.pending) == 0 {
		gh := join("gh", "p_", randToken(g.rng, alphaNum, 36))
		g.pending = []byte(filler(g.rng, 4000) + " token=" + gh + " password=\"p@ss-" + gh[4:12] + "\" ")
		g.secrets++
		if len(g.pending) > g.left { // the last chunk carries no secret, so counts are exact
			g.pending = []byte(filler(g.rng, g.left))
			g.secrets--
		}
		if g.chunks++; g.onChunk != nil && g.chunks%256 == 0 {
			g.onChunk()
		}
	}
	n := min(len(p), len(g.pending), g.left)
	copy(p, g.pending[:n])
	g.pending = g.pending[n:]
	g.left -= n
	return n, nil
}

// Redacting a 16 MiB stream uses constant memory: the live heap after GC,
// sampled throughout the stream, never grows by more than the fixed buffers.
func TestStreamMemoryIsConstant(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 16 MiB")
	}
	const size = 16 << 20
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak uint64
	sample := func() {
		runtime.GC()
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapAlloc)
	}
	g := &genReader{left: size, rng: rand.New(rand.NewSource(1)), onChunk: sample}
	n, rep, err := Default().Copy(io.Discard, g)
	if err != nil {
		t.Fatal(err)
	}
	sample()
	growth := int64(peak) - int64(base)
	t.Logf("streamed %d MiB in, %d bytes out, %d redactions; live heap growth peak %d KiB", size>>20, n, rep.Count, growth>>10)
	if rep.Count != 2*g.secrets {
		t.Fatalf("redactions = %d, want %d", rep.Count, 2*g.secrets)
	}
	if growth > 1<<20 {
		t.Fatalf("live heap grew by %d KiB while streaming 16 MiB; want O(1) (< 1 MiB)", growth>>10)
	}
}

// BenchmarkReader16MiB redacts a 16 MiB stream (a secret pair every ~4 KiB).
// Allocations per op stay small and fixed-size: the stream is never buffered.
func BenchmarkReader16MiB(b *testing.B) {
	const size = 16 << 20
	in, err := io.ReadAll(&genReader{left: size, rng: rand.New(rand.NewSource(1))})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(size)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Default().Copy(io.Discard, bytes.NewReader(in)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReaderSource16MiB redacts real repository text: the package's own
// source repeated to 16 MiB.
func BenchmarkReaderSource16MiB(b *testing.B) {
	var src []byte
	for _, name := range []string{"redact.go", "patterns.go", "stream.go", "entropy.go", "redact_test.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			b.Fatal(err)
		}
		src = append(src, data...)
	}
	in := bytes.Repeat(src, (16<<20)/len(src)+1)[:16<<20]
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Default().Copy(io.Discard, bytes.NewReader(in)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStringSmall(b *testing.B) {
	in := fmt.Sprintf(`{"content":"remember: the deploy uses %s and the DB_PASSWORD env var","paths":["a.go"]}`, "a ${TOKEN} reference")
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Default().String(in)
	}
}
