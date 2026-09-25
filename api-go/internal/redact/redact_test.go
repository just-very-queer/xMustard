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
	pgpBegin, pgpEnd := join("-----BEGIN ", "PGP PRIVATE", " KEY BLOCK-----"), join("-----END ", "PGP PRIVATE", " KEY BLOCK-----")
	pw := "hunter2-" + randToken(rng, alphaNum, 6)
	apiHex := randToken(rng, "0123456789abcdef", 32)

	out := []sample{
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
		// letter-only passwords on data lines (env, dotenv, YAML, flags)
		{"env letters-only password", "DB_PASSWORD=letmeinplease\n", "letmeinplease", RuleSecretField},
		{"export letters-only password", "export PGPASSWORD=correcthorsebatterystaple\n", "correcthorsebatterystaple", RuleSecretField},
		{"env root password at end", "MYSQL_ROOT_PASSWORD=changeme", "changeme", RuleSecretField},
		{"dotenv lowercase key", "\ndb_password=swordfish\nport=5432", "swordfish", RuleSecretField},
		{"yaml letters-only password", "db:\n  password: letmein\n", "letmein", RuleSecretField},
		{"yaml list item password", "users:\n  - password: opensesame # rotate\n", "opensesame", RuleSecretField},
		{"flag letters-only password", "mysql --password hunter -h db", "hunter", RuleSecretField},
		// YAML block scalars
		{"yaml block scalar password", "db:\n  password: |\n    Sup3rS3cret!Value\n  host: x\n", "Sup3rS3cret!Value", RuleSecretField},
		{"yaml folded block in json", `{"content":"db:\n  password: >-\n    first line\n    second line\n  host: x\n"}`, "second line", RuleSecretField},
		// values that start like a placeholder but are not one
		{"password starting with your", `{"password":"yourDog2024!"}`, "yourDog2024!", RuleSecretField},
		{"password starting with insert", `{"password":"Insert-Coin-99"}`, "Insert-Coin-99", RuleSecretField},
		{"password starting with replace", `{"password": "Replace-Me-Not-9x!"}`, "Replace-Me-Not-9x!", RuleSecretField},
		{"env-name-shaped password with digits", "DB_PASSWORD=HUNTER_2024\n", "HUNTER_2024", RuleSecretField},
		{"api key starting with your", `{"api_key": "your8f14e45fceea167a5a36dedd4bea2543"}`, "your8f14e45fceea167a5a36dedd4bea2543", RuleSecretField},
		// key bodies with armor headers
		{"encrypted pem with headers", begin + "\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,3F17F5316E2BAC89\n\n" + pemBody + "\n" + end,
			pemBody, RulePrivateKey},
		{"pgp private key block", pgpBegin + "\nVersion: GnuPG v2.2.41 (GNU/Linux)\nComment: https://gnupg.org\n\n" + pemBody + "\n=Ab12\n" + pgpEnd,
			pemBody, RulePrivateKey},
		{"single-line pem", `KEY="` + begin + " " + strings.ReplaceAll(pemBody, "\n", " ") + " " + end + `"`,
			strings.ReplaceAll(pemBody, "\n", " "), RulePrivateKey},
	}
	for _, sh := range pemShapes(begin, end, strings.Split(pemBody, "\n")) {
		out = append(out, sample{"pem " + sh.name, sh.text, strings.Split(pemBody, "\n")[0], RulePrivateKey})
	}
	return out
}

type pemShape struct{ name, text, want string }

// pemShapes writes a key the ways source code, comments and transcripts
// carry it, each with the output a redaction must produce: separators kept,
// every line replaced.
func pemShapes(begin, end string, lines []string) []pemShape {
	each := func(pre, post string) (text, want string) {
		for _, l := range lines {
			text += pre + l + post
			want += pre + "[REDACTED:private_key]" + post
		}
		return
	}
	var shapes []pemShape
	add := func(name, open, pre, post, close string) {
		text, want := each(pre, post)
		shapes = append(shapes, pemShape{name, open + text + close, open + want + close})
	}
	add("java concatenation", `String k = "`+begin+`\n"`, ` + "`, `\n"`, ` + "`+end+`";`)
	add("go concatenation", `k := "`+begin+`\n" +`, "\n\t\"", `\n" +`, "\n\t\""+end+`\n"`)
	add("python adjacent literals", "k = (\n    '"+begin+`\n'`, "\n    '", `\n'`, "\n    '"+end+`\n'`+"\n)\n")
	add("c adjacent literals", `const char *k = "`+begin+`\n"`, "\n\"", `\n"`, "\n\""+end+`\n";`)
	add("hash comment", "# "+begin, "\n# ", "", "\n# "+end+"\n")
	add("slash comment", "// "+begin, "\n// ", "", "\n// "+end+"\n")
	add("markdown quote", "> "+begin, "\n> ", "", "\n> "+end+"\n")
	goSrc := shapes[1]
	text, _ := json.Marshal(map[string]string{"text": goSrc.text})
	want, _ := json.Marshal(map[string]string{"text": goSrc.want})
	shapes = append(shapes, pemShape{"go concatenation in json", string(text), string(want)})
	return shapes
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

// JSONL transcripts stay one valid record per line, and later records
// survive, whatever a record's text contains.
func TestRedactedJSONLStaysValid(t *testing.T) {
	r := Default()
	begin := join("-----BEGIN ", "PRIVATE", " KEY-----")
	token := join("a8f5f167f44f", "4964e6c998dee827110c")
	lines := []string{
		`{"role":"user","text":"my key file starts with ` + begin + ` and then base64"}`,
		`{"text":"set password: 'hunter2 for now","n":1}`,
		`{"cmd":"export API_TOKEN='` + token + `","ok":true}`,
		`{"role":"assistant","text":"noted"}`,
		`["password: 'x9-long-pass", "next"]`,
		`{"n":2}`,
	}
	in := strings.Join(lines, "\n") + "\n"
	out := sameAsOneShot(t, r, "jsonl", in)
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(got) != len(lines) {
		t.Fatalf("records lost: %d of %d\n%s", len(got), len(lines), out)
	}
	for i, line := range got {
		if !json.Valid([]byte(line)) {
			t.Errorf("record %d is not JSON after redaction: %s", i, line)
		}
	}
	for _, i := range []int{3, 5} {
		if got[i] != lines[i] {
			t.Errorf("record %d changed: %s", i, got[i])
		}
	}
	for _, leak := range []string{"hunter2", token, "x9-long-pass"} {
		if strings.Contains(out, leak) {
			t.Errorf("secret %q survived: %s", leak, out)
		}
	}
	// A single-quoted shell value may still hold a double quote.
	if got, _ := r.String(`password='ab"cd' next`); got != `password='[REDACTED:secret_field]' next` {
		t.Errorf("single-quoted value: %q", got)
	}
}

// A key interrupted by separators (string concatenation, adjacent literals,
// comment and quote prefixes) is redacted line by line when its END marker
// follows: the separators stay, so source and JSON keep their shape, and the
// key counts once.
func TestPEMWithSeparators(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(11))
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	lines := []string{randToken(rng, alphaNum+"+/", 64), randToken(rng, alphaNum+"+/", 64), randToken(rng, alphaNum, 6) + "=="}
	for _, sh := range pemShapes(begin, end, lines) {
		got := sameAsOneShot(t, r, sh.name, sh.text)
		if got != sh.want {
			t.Errorf("%s:\n got %q\nwant %q", sh.name, got, sh.want)
		}
		if _, rep := r.String(sh.text); rep.Count != 1 || rep.Rules[RulePrivateKey] != 1 {
			t.Errorf("%s: one key must count once: %+v", sh.name, rep)
		}
		if f := r.Findings(sh.text); len(f) != 1 || f[0].Offset > strings.Index(sh.text, lines[0]) ||
			f[0].Offset+f[0].Length < strings.Index(sh.text, lines[2])+len(lines[2]) {
			t.Errorf("%s: one finding must cover the key: %+v", sh.name, f)
		}
	}

	// BEGIN and END in different JSONL records: the key lines between them go,
	// every record stays valid JSON and later records are untouched.
	records := []string{
		`{"role":"user","text":"my key: ` + begin + `"}`,
		`{"role":"user","text":"` + lines[0] + `"}`,
		`{"role":"user","text":"` + lines[1] + `\n` + lines[2] + `"}`,
		`{"role":"user","text":"` + end + `"}`,
		`{"role":"assistant","text":"noted, thanks"}`,
	}
	in := strings.Join(records, "\n") + "\n"
	out := sameAsOneShot(t, r, "jsonl key", in)
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(got) != len(records) || got[0] != records[0] || got[3] != records[3] || got[4] != records[4] {
		t.Fatalf("records merged or changed:\n%s", out)
	}
	for i, rec := range got {
		if !json.Valid([]byte(rec)) {
			t.Errorf("record %d is not JSON: %s", i, rec)
		}
	}
	for _, l := range lines {
		if strings.Contains(out, l) {
			t.Errorf("key line survived: %s", out)
		}
	}

	// Constants naming the markers, with code between them, are not a key.
	code := "const (\n\tpemBegin = \"" + begin + "\"\n\tpemEnd   = \"" + end + "\"\n)\n"
	if got, rep := r.String(code); got != code || rep.Redacted {
		t.Errorf("marker constants were redacted: %q", got)
	}
}

// A prefix repeated on every line of a key (a shell echo, a builder call) is a
// separator: each line goes, the short last line included, and the words of
// the prefix (a file name) stay.
func TestPEMWithRepeatedPrefix(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(21))
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	l1, l2, last := randToken(rng, alphaNum+"+/", 64), randToken(rng, alphaNum+"+/", 64), "Xk9pQ2w=="
	echo := func(l, op string) string { return `echo "` + l + `" ` + op + " key2.pem\n" }
	in := echo(begin, ">") + echo(l1, ">>") + echo(l2, ">>") + echo(last, ">>") + echo(end, ">>")
	want := echo(begin, ">") + echo("[REDACTED:private_key]", ">>") + echo("[REDACTED:private_key]", ">>") +
		echo("[REDACTED:private_key]", ">>") + echo(end, ">>")
	if got := sameAsOneShot(t, r, "echo", in); got != want {
		t.Errorf("echo lines:\n got %q\nwant %q", got, want)
	}
	app := func(l string) string { return `sb.append("` + l + `\n");` + "\n" }
	in = app(begin) + app(l1) + app(l2) + app(last) + app(end)
	want = app(begin) + app("[REDACTED:private_key]") + app("[REDACTED:private_key]") + app("[REDACTED:private_key]") + app(end)
	if got := sameAsOneShot(t, r, "append", in); got != want {
		t.Errorf("builder lines:\n got %q\nwant %q", got, want)
	}

	// A secret inside the repeated prefix is a finding of its own; the key's
	// finding spans its lines, and neither is stretched over the other.
	pw := "Tr0ub4!"
	pre := "\n# password=\"" + pw + "\" "
	in = "# " + begin + pre + l1 + pre + l2 + pre + last + pre + end + "\n"
	out := sameAsOneShot(t, r, "prefixed password", in)
	if strings.Contains(out, pw) || strings.Contains(out, l1) || strings.Contains(out, last) || !strings.Contains(out, "password=") {
		t.Fatalf("prefixed key: %q", out)
	}
	var keys, fields int
	for _, f := range r.Findings(in) {
		switch f.Rule {
		case RulePrivateKey:
			keys++
			if f.Offset != strings.Index(in, l1) || f.Offset+f.Length != strings.Index(in, last)+len(last) {
				t.Errorf("key finding %+v does not span its lines", f)
			}
		case RuleSecretField:
			fields++
			if f.Length != len(pw) {
				t.Errorf("password finding stretched: %+v", f)
			}
		}
	}
	if keys != 1 || fields != 4 {
		t.Errorf("findings: %d keys, %d passwords", keys, fields)
	}
}

// A key that a JSON encoder wrote with \u escapes ('+' as \u002B, '"' as
// \u0022, as System.Text.Json does by default) is replaced along whole
// escapes: the output stays valid JSON and no fragment of a line survives.
func TestPEMWithUnicodeEscapes(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(22))
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	var lines []string
	for range 3 {
		l := []byte(randToken(rng, alphaNum+"/", 64))
		for k := 5; k < len(l); k += 11 {
			l[k] = '+'
		}
		lines = append(lines, string(l))
	}
	lines = append(lines, randToken(rng, alphaNum, 18)+"==")
	encode := func(v string) string {
		var b strings.Builder
		b.WriteByte('"')
		for _, c := range []byte(v) {
			switch c {
			case '"':
				b.WriteString(`\u0022`)
			case '+':
				b.WriteString(`\u002B`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('"')
		return b.String()
	}
	java := `String k = "` + begin + `\n"`
	for _, l := range lines {
		java += ` + "` + l + `\n"`
	}
	java += ` + "` + end + `";`
	for name, v := range map[string]string{
		"pem":  begin + "\n" + strings.Join(lines, "\n") + "\n" + end + "\n",
		"java": java,
	} {
		in := `{"text":` + encode(v) + `,"n":1}`
		if !json.Valid([]byte(in)) {
			t.Fatalf("%s: fixture is not JSON", name)
		}
		out := sameAsOneShot(t, r, name, in)
		if !json.Valid([]byte(out)) {
			t.Errorf("%s: redaction broke JSON: %s", name, out)
		}
		for _, l := range lines {
			for k := 0; k+5 <= len(l); k++ {
				if frag := l[k : k+5]; !strings.ContainsAny(frag, "+") && strings.Contains(out, frag) {
					t.Fatalf("%s: fragment %q survived: %s", name, frag, out)
				}
			}
		}
	}
}

// A BEGIN marker without an END redacts only what could be a key body: base64
// up to the first byte a body cannot hold, never a following word, quote, line
// or record, and nothing when there is no base64 run of pemMinRun bytes.
func TestPEMWithoutEndIsBounded(t *testing.T) {
	r := Default()
	begin := join("-----BEGIN ", "PRIVATE", " KEY-----")
	kept := []string{
		"Keys look like " + begin + " followed by base64. The rest of this paragraph stays.",
		`if strings.HasPrefix(s, "` + begin + `") { return true }`,
		"warn: found " + begin + " in upload\n\n{\"n\":1}\n{\"n\":2}\n",
		"WARN found " + begin + " in upload\nINFO server started on port 8080\nINFO ready\nERROR disk full: /var\n",
		begin + "\n2026-01-01 INFO " + strings.Repeat("QUJD", 10) + "\n",
		`{"text":"the header is ` + begin + `\n","n":1}`,
	}
	for _, in := range kept {
		if got, rep := r.String(in); got != in || rep.Redacted {
			t.Errorf("a marker mention lost text:\n in %q\nout %q", in, got)
		}
	}

	// A truncated key is redacted up to where it stops; what follows the stop,
	// including trailing whitespace and later records, stays.
	rng := rand.New(rand.NewSource(9))
	l1, l2 := randToken(rng, alphaNum+"+/", 64), randToken(rng, alphaNum+"+/", 40)
	cases := map[string]string{
		`{"text":"` + begin + `\n` + l1 + `\n` + l2 + `"}` + "\n{\"n\":1}\n": `{"text":"` + begin + `[REDACTED:private_key]"}` + "\n{\"n\":1}\n",
		begin + "\n" + l1 + "\n" + l2 + "   \n\n# next line\n":               begin + "[REDACTED:private_key]   \n\n# next line\n",
		begin + "\n" + l1 + " (truncated)\n":                                 begin + "[REDACTED:private_key] (truncated)\n",
	}
	for in, want := range cases {
		if got := sameAsOneShot(t, r, "truncated key", in); got != want {
			t.Errorf("truncated key:\n got %q\nwant %q", got, want)
		}
	}

	// The same around the first window boundary, from either side of the
	// lookahead, whatever the chunking of reads.
	first := contextLen + windowSize - 1 // input bytes in the first window
	for _, at := range []int{first - lookahead - 3000, first - lookahead - 40, first - lookahead + 40, first - 2000} {
		in := filler(rng, at) + "\n" + begin + "\n" + l1 + "\n" + l2 + strings.Repeat(" ", 300) + "\n" + `{"n":1}` + "\n" + filler(rng, 3000)
		want := sameAsOneShot(t, r, "truncated key at boundary", in)
		if !strings.Contains(want, begin+"[REDACTED:private_key]"+strings.Repeat(" ", 300)+"\n"+`{"n":1}`+"\n") {
			t.Fatalf("at %d: trailing whitespace or next record lost", at)
		}
		for _, src := range []io.Reader{strings.NewReader(in), iotest.HalfReader(strings.NewReader(in))} {
			if got, _ := streamAll(t, r, src); got != want {
				t.Fatalf("at %d: stream differs", at)
			}
		}
	}

	var body strings.Builder
	for body.Len() < pemMaxBody+8<<10 {
		body.WriteString(randToken(rng, alphaNum+"+/", 64) + "\n")
	}
	in := begin + "\n" + body.String() + "tail"
	want := begin + "[REDACTED:private_key]" + in[len(begin)+pemMaxBody:]
	if got := sameAsOneShot(t, r, "capped body", in); got != want {
		t.Fatalf("body not capped at pemMaxBody: kept %d bytes", len(got))
	}
	if got, _ := streamAll(t, r, &chunkReader{data: []byte(in), rng: rng, max: 3000}); got != want {
		t.Fatal("capped body: stream differs")
	}
}

// A long run that fails its class check must not hide a real key glued after
// it: each hit is judged on its own first judgedLen bytes.
func TestLongRunDoesNotHideGluedKey(t *testing.T) {
	r := Default()
	key := join("sk-", "proj-", "Ab3dEf5gHi7jKl9mNo1pQr3sTu5vWx7yZa9Bc")
	slack := join("xox", "b-", "123456789012-", "Ab3dEf5gHi7jKl9mNo1pQr3s")
	cases := map[string]struct{ in, secret string }{
		"openai after 600":   {"x sk-" + strings.Repeat("a", 600) + "-" + key + " y", key[8:]},
		"openai at the edge": {"x sk-" + strings.Repeat("a", judgedLen-4) + "-" + key + " y", key[8:]},
		"slack after 600":    {"x xoxb-" + strings.Repeat("a", 600) + "-" + slack + " y", slack[5:]},
	}
	rng := rand.New(rand.NewSource(4))
	for name, tc := range cases {
		out := sameAsOneShot(t, r, name, tc.in)
		if strings.Contains(out, tc.secret) {
			t.Errorf("%s: key survived: %q", name, out)
		}
		padded := filler(rng, windowSize-300) + " " + tc.in
		want, _, _ := oneShot(r, padded)
		if got, _ := streamAll(t, r, &chunkReader{data: []byte(padded), rng: rng, max: 999}); got != want || strings.Contains(got, tc.secret) {
			t.Errorf("%s: stream differs or leaks", name)
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
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	hash := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	return []string{
		// both key markers named in code, prose and chat, with no key between
		"const pemBegin = \"" + begin + "\"\n\nfunc load(b []byte) (*rsa.PrivateKey, error) {\n\tblock, _ := pem.Decode(b)\n" +
			"\treturn x509.ParsePKCS1PrivateKey(block.Bytes)\n}\n\nconst pemEnd = \"" + end + "\"\n",
		"if bytes.HasPrefix(b, []byte(\"" + begin + "\")) {\n\treturn x509.ParsePKCS1PrivateKey(b)\n}\nconst pemEnd = \"" + end + "\"\n",
		"Put the " + begin + " block in /etc/ssl/private/server.key and close it with " + end + ".",
		"Write " + begin + " to /etc/ssl/private/server.key, then " + end + " last.",
		begin + "\n- sha256: " + hash + "\n" + end + "\n",
		begin + "\n\"sha256: " + hash + "\"\n" + end + "\n",
		"The key " + begin + " sha256: " + hash + " " + end + " was rotated.",
		`{"role":"user","text":"why does openssl write ` + begin + ` to /home/developer/.ssh/key.pem?"}` + "\n" +
			`{"role":"assistant","text":"x509.ParsePKCS1PrivateKey expects the ` + end + ` footer"}` + "\n",
		`{"role":"user","text":"` + begin + ` /home/developer/"}` + "\n" + `{"role":"assistant","text":"ParsePKCS1PrivateKey ` + end + `"}` + "\n",
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
		// code, prose and references that data-line detection must leave alone
		"    password: SecretStr = Field(default=None)",
		"    conn = connect(\n        password=db_password,\n    )",
		"connect(host, password=secretvalue)",
		"PASSWORD=getpass()",
		"Password: must be at least 8 characters.",
		`{"msg":"password: invalid"}`,
		"DB_PASSWORD=${DB_PASSWORD}",
		"password: ${DB_PASSWORD}",
		"export PGPASSWORD=$(pass show db)",
		`{"password":"your-password-here","token":"YOUR_TOKEN_HERE"}`,
		`{"password":"yourApiKey"}`,
		"db:\n  password: |\n  host: x\n",
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

// oneShot redacts in as one window: the reference that every windowed path
// (String, Bytes, Check and Findings above 64 KiB, Reader, Copy) must match.
func oneShot(r *Redactor, in string) (string, Report, []Finding) {
	e := &engine{r: r, collect: true, base: -1}
	buf := make([]byte, 1+len(in))
	buf[0] = '\n'
	copy(buf[1:], in)
	out, _ := e.window(nil, buf, 1, len(buf), true)
	return string(out), e.rep, e.found
}

// sameAsOneShot checks the windowed one-shot calls against oneShot.
func sameAsOneShot(t *testing.T, r *Redactor, name, in string) string {
	t.Helper()
	want, wantRep, wantFound := oneShot(r, in)
	if got, rep := r.String(in); got != want || rep.Count != wantRep.Count {
		t.Fatalf("%s: String differs from one window (%d vs %d redactions)", name, rep.Count, wantRep.Count)
	}
	if got, _ := r.Bytes([]byte(in)); string(got) != want {
		t.Fatalf("%s: Bytes differs from one window", name)
	}
	if found := r.Findings(in); fmt.Sprint(found) != fmt.Sprint(wantFound) {
		t.Fatalf("%s: Findings differ from one window:\n got %v\nwant %v", name, found, wantFound)
	}
	return want
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
		want := sameAsOneShot(t, r, s.name, in)
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
		want := sameAsOneShot(t, r, fmt.Sprint("seed ", seed), in)
		_, wantRep, _ := oneShot(r, in)
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
	pemBody := randToken(rng, alphaNum+"+/\n", 30<<10) // a large key, under pemMaxBody
	cases := []struct {
		name, in, secret, tail string
	}{
		{"long token", filler(rng, 60<<10) + " " + longToken + " tail", longToken, " tail"},
		{"long unquoted value", filler(rng, 61<<10) + "\nAPI_TOKEN=" + longValue + "1\nnext", longValue, "\nnext"},
		{"long json value", filler(rng, 62<<10) + `{"password":"` + longJSON + `","n":1}`, longJSON, `","n":1}`},
		{"long pem", filler(rng, 63<<10) + "\n" + begin + "\n" + pemBody + "\n" + end + "\nafter", pemBody, end + "\nafter"},
	}
	for _, tc := range cases {
		want := sameAsOneShot(t, r, tc.name, tc.in)
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

// A secret that starts inside a region carried over from the previous window
// and reaches past its end extends that region, as one window over the whole
// input would merge the two: here a key, and a quoted value that closes after
// the region does, inside a long unterminated single-quoted value that crosses
// the first window boundary.
func TestCarriedRegionExtendsIntoNextWindow(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(12))
	first := contextLen + windowSize - 1 // input bytes in the first window
	begin, end := join("-----BEGIN ", "PRIVATE", " KEY-----"), join("-----END ", "PRIVATE", " KEY-----")
	l1, l2 := randToken(rng, alphaNum+"+/", 64), randToken(rng, alphaNum+"+/", 64)
	tok := "Zq8" + randToken(rng, alphaNum, 20) + "'" + randToken(rng, alphaNum, 20) + "7x"
	tails := map[string]string{
		"key": begin + "\n" + l1 + "\n" + l2 + "\n" + end + "\nafter\n",
		// the single-quoted region closes at the quote inside the token value
		"quoted value": `api_key: "` + tok + `"` + "\nafter\n",
	}
	for name, tail := range tails {
		for _, lead := range []int{lookahead + 500, lookahead + 3000} {
			pre := strings.Repeat("lorem ipsum\n", (first-lead)/12)
			words := strings.Repeat("lorem ipsum ", (lead+2000)/12) // crosses the window end
			in := pre + "note: password: 'draft " + words + tail
			want := sameAsOneShot(t, r, name, in)
			for _, leak := range []string{l1, l2, tok[len(tok)-22:]} {
				if strings.Contains(tail, leak) && strings.Contains(want, leak) {
					t.Fatalf("%s: one-shot reference leaks", name)
				}
			}
			for _, src := range []io.Reader{strings.NewReader(in), &chunkReader{data: []byte(in), rng: rng, max: 4000}, iotest.OneByteReader(strings.NewReader(in))} {
				if got, rep := streamAll(t, r, src); got != want || rep.Count != strings.Count(want, "[REDACTED:") {
					t.Fatalf("%s lead %d: stream differs from one window", name, lead)
				}
			}
		}
	}
}

// firstLimit is the input offset of the first window's limit: anchors before
// it are decided in the first window, the rest in the second.
const firstLimit = contextLen + windowSize - 1 - lookahead

// padTo returns prose of exactly n bytes, ending in a line break.
func padTo(n int) string {
	pre := strings.Repeat("lorem ipsum\n", (n-1)/12)
	return pre + strings.Repeat(" ", n-1-len(pre)) + "\n"
}

// A secret decided in one window that starts past the output it emits (a URL
// password after a token user name, the later lines of a split key after a
// prefix holding a password) waits for the next window: what that window
// finds before or inside it is merged exactly as one window would. Before,
// the bytes between were emitted in clear and a secret anchored there leaked.
func TestSecretsPastTheDecisionLimit(t *testing.T) {
	r := Default()
	rng := rand.New(rand.NewSource(31))
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	step := 1
	if testing.Short() {
		step = 7
	}
	check := func(name, in string, secrets ...string) {
		t.Helper()
		want := sameAsOneShot(t, r, name, in)
		for _, s := range secrets {
			if strings.Contains(want, s) {
				t.Fatalf("%s: one window leaks %q", name, s)
			}
		}
		_, wantRep, _ := oneShot(r, in)
		if got, rep := streamAll(t, r, &chunkReader{data: []byte(in), rng: rng, max: 1 + rng.Intn(9000)}); got != want || rep.Count != wantRep.Count {
			t.Fatalf("%s: stream differs from one window", name)
		}
	}
	tail := filler(rng, 40000)
	pw := "Tr0ub4dor&3x!"
	for d := 1; d <= 400; d += step {
		l1 := randToken(rng, alphaNum+"+/", 64)
		// BEGIN just before the limit, the password and the key line after it
		in := padTo(firstLimit-d) + `{"text":"my key: ` + begin + `"}` + "\n" + `{"cmd":"export DB_PASSWORD='` + pw + `'"}` + "\n" +
			`{"text":"` + l1 + `"}` + "\n" + `{"text":"` + end + `"}` + "\n" + tail
		check(fmt.Sprint("jsonl records ", d), in, pw)
		// a key whose repeated line prefix holds a password
		pre := "\n# password=\"" + randToken(rng, alphaNum, 6) + "!\" "
		in = padTo(firstLimit-d) + "# " + begin + pre + l1 + pre + randToken(rng, alphaNum+"+/", 64) + pre + "Xk9pQ2w==" + pre + end + "\n" + tail
		check(fmt.Sprint("prefixed key ", d), in, pre[len("\n# password=\""):len(pre)-2], l1, "Xk9pQ2w==")
	}
	gh := join("gh", "p_", randToken(rng, alphaNum, 36))
	for d := 1; d <= 12; d++ {
		in := padTo(firstLimit-d) + "https://" + gh + ":x-oauth-basic@github.com/org/repo.git\n" + tail
		check(fmt.Sprint("url user ", d), in, gh)
		in = padTo(firstLimit-d) + "Authorization: Bearer " + randToken(rng, alphaNum, 40) + "\n" + tail
		check(fmt.Sprint("bearer header ", d), in)
	}
}

// denseInput packs fragments whose secrets start well past their anchors
// (URL passwords, the later lines of split keys, YAML block scalars, flag and
// header values, webhook paths) next to secrets that may lie between, so every
// window boundary falls inside one.
func denseInput(rng *rand.Rand, size int) string {
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	line := func() string { return randToken(rng, alphaNum+"+/", 64) }
	var b strings.Builder
	for b.Len() < size {
		switch rng.Intn(10) {
		case 0:
			b.WriteString("git clone https://" + join("gh", "p_", randToken(rng, alphaNum, 36)) + ":x-oauth-basic@github.com/o/r.git\n")
		case 1:
			pre := "\n# password=\"" + randToken(rng, alphaNum, 6) + "!\" "
			b.WriteString("# " + begin)
			for range 1 + rng.Intn(6) {
				b.WriteString(pre + line())
			}
			b.WriteString(pre + randToken(rng, alphaNum, 1+rng.Intn(12)) + "==" + pre + end + "\n")
		case 2:
			b.WriteString("Authorization: Bearer " + randToken(rng, alphaNum, 40) + "\n")
		case 3:
			b.WriteString(`{"text":"my key: ` + begin + `"}` + "\n" + `{"cmd":"export DB_PASSWORD='Tr0ub4dor&3x!'"}` + "\n" +
				`{"text":"` + line() + `"}` + "\n" + `{"text":"` + end + `"}` + "\n")
		case 4:
			shapes := pemShapes(begin, end, []string{line(), line(), randToken(rng, alphaNum, 10) + "=="})
			b.WriteString(shapes[rng.Intn(len(shapes))].text + "\n")
		case 5:
			b.WriteString("db:\n  password: |\n" + strings.Repeat(" ", rng.Intn(200)) + "\n    " + line() + "\n  host: x\n")
		case 6:
			b.WriteString("post to https://hooks.slack.com/services/T0" + randToken(rng, upperNum, 8) + "/B0" +
				randToken(rng, upperNum, 8) + "/" + randToken(rng, alphaNum, 24) + " ok\n")
		case 7:
			b.WriteString("deploy --api-key        " + randToken(rng, "0123456789abcdef", 32) + " --force\n")
		case 8:
			b.WriteString("echo \"" + begin + "\" > k.pem\necho \"" + line() + "\" >> k.pem\necho \"" +
				join("AK", "IA", randToken(rng, upperNum, 16)) + "\" >> k.pem\necho \"" + end + "\" >> k.pem\n")
		default:
			b.WriteString(filler(rng, rng.Intn(400)) + "\n")
		}
	}
	return b.String()
}

// Randomized differential: on dense inputs, every windowed path produces the
// one-window output, report and findings, whatever the chunking of reads.
// Before pending candidates, the windowed paths differed on 115 of 200 such
// inputs, each time redacting less.
func TestStreamMatchesOneShotDense(t *testing.T) {
	r := Default()
	seeds := int64(40)
	if testing.Short() {
		seeds = 6
	}
	for seed := int64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		in := denseInput(rng, 300<<10+rng.Intn(300<<10))
		want := sameAsOneShot(t, r, fmt.Sprint("seed ", seed), in)
		_, wantRep, _ := oneShot(r, in)
		got, rep := streamAll(t, r, &chunkReader{data: []byte(in), rng: rng, max: 1 + rng.Intn(20000)})
		if got != want || rep.Count != wantRep.Count {
			t.Fatalf("seed %d: stream differs from one window (%d vs %d redactions)", seed, rep.Count, wantRep.Count)
		}
	}
}

// stickyInput mixes fragments that open long regions (unterminated quotes,
// long lines and bare values, YAML blocks) with keys in every shape, secrets
// between the lines of split keys, tokens and marker mentions, so that
// regions and secrets straddle window ends.
func stickyInput(rng *rand.Rand, size int) string {
	begin, end := join("-----BEGIN ", "RSA PRIVATE", " KEY-----"), join("-----END ", "RSA PRIVATE", " KEY-----")
	words := func(n int) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString([]string{"lorem ", "ipsum ", "dolor ", "sit ", "amet "}[rng.Intn(5)])
		}
		return b.String()
	}
	line := func() string { return randToken(rng, alphaNum+"+/", 64) }
	var b strings.Builder
	for b.Len() < size {
		switch rng.Intn(16) {
		case 0:
			b.WriteString("note: password: 'draft " + words(rng.Intn(60000)) + "\n")
		case 1:
			b.WriteString("note: password: 'draft " + words(rng.Intn(60000)) + begin + "\n" + line() + "\n" + line() + "\n" + end + "\n")
		case 2:
			b.WriteString(`{"password":"` + randToken(rng, alphaNum, 1+rng.Intn(3000)) + `"}` + "\n")
		case 3:
			shapes := pemShapes(begin, end, []string{line(), line(), randToken(rng, alphaNum, 10) + "=="})
			b.WriteString(shapes[rng.Intn(len(shapes))].text + "\n")
		case 4:
			b.WriteString("token " + join("gh", "p_", randToken(rng, alphaNum, 36+rng.Intn(3))) + " x\n")
		case 5:
			b.WriteString("API_TOKEN=" + randToken(rng, alphaNum, 1+rng.Intn(50000)) + "1\n")
		case 6:
			b.WriteString("warn " + begin + " in upload " + words(rng.Intn(200)) + "\n")
		case 7:
			b.WriteString(begin + "\n" + line() + "\n" + line() + "\n" + end + "\n")
		case 8:
			b.WriteString(`{"text":"say password: '` + words(rng.Intn(20000)) + `","n":1}` + "\n")
		case 9:
			b.WriteString("db:\n  password: |\n    " + line() + "\n  host: x\n")
		case 10:
			b.WriteString("key " + begin + "\n" + line() + "\n" + words(rng.Intn(40)) + "\n")
		case 11:
			// secrets between the lines of a split key: in its repeated prefix,
			// or in records between (text that is then not taken for a key)
			pre := "\n# password=\"" + randToken(rng, alphaNum, 6) + "!\" "
			b.WriteString("# " + begin + pre + line() + pre + line() + pre + end + "\n")
			b.WriteString(`{"t":"` + begin + `"}` + "\n" + `{"t":"token ` + join("gh", "p_", randToken(rng, alphaNum, 36)) + `"}` + "\n" +
				`{"t":"` + line() + `"}` + "\n" + `{"t":"` + end + `"}` + "\n")
		case 12:
			b.WriteString("https://" + join("gh", "p_", randToken(rng, alphaNum, 36)) + ":x-oauth-basic@github.com/o/r " + words(rng.Intn(200)) + "\n")
		default:
			b.WriteString(words(rng.Intn(9000)) + "\n")
		}
	}
	return b.String()
}

// Randomized: long regions and secrets at random positions, random read
// sizes. Every windowed path produces exactly the one-window output, report
// and findings.
func TestStreamMatchesOneShotSticky(t *testing.T) {
	r := Default()
	seeds := int64(60)
	if testing.Short() {
		seeds = 10
	}
	for seed := int64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		in := stickyInput(rng, windowSize/2+rng.Intn(3*windowSize))
		want := sameAsOneShot(t, r, fmt.Sprint("seed ", seed), in)
		_, wantRep, _ := oneShot(r, in)
		got, rep := streamAll(t, r, &chunkReader{data: []byte(in), rng: rng, max: 1 + rng.Intn(20000)})
		if got != want || rep.Count != wantRep.Count {
			t.Fatalf("seed %d: stream differs from one window (%d vs %d redactions)", seed, rep.Count, wantRep.Count)
		}
	}
}

// A PEM END marker split across a window boundary still closes the block, for
// a BEGIN marker on either side of the lookahead.
func TestStreamPEMEndMarkerAcrossBoundary(t *testing.T) {
	r := Default()
	begin, end := join("-----BEGIN ", "EC PRIVATE", " KEY-----"), join("-----END ", "EC PRIVATE", " KEY-----")
	first := contextLen + windowSize - 1
	for split := 1; split < len(end); split += 3 {
		bodyLen := pemMaxBody - 100
		if split%2 == 0 {
			bodyLen = 3000
		}
		head := strings.Repeat("a", first-split-len(begin)-bodyLen) // END starts split bytes before the window end
		body := strings.Repeat("Q", bodyLen)
		in := head + begin + body + end + "\nvisible"
		want := sameAsOneShot(t, r, "pem end split", in)
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
		want := sameAsOneShot(t, r, "escaped quote split", in)
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
		"openai literal run":     rep("sk-"),
		"anthropic literal run":  rep("sk-ant-"),
		"jwt literal run":        rep("eyJ"),
		"github pat run":         rep("github_pat_"),
		"xmustard token run":     rep("xmt_"),
		"slack run":              rep("xoxb-"),
		"keyed separators":       rep("token:"),
		"keyed equals":           rep("api_key=api_key="),
		"flags":                  rep("--token --token "),
		"pem begin markers":      rep(join("-----BEGIN ", "RSA PRIVATE", " KEY-----")),
		"bearer words":           rep("bearer bearer "),
		"url separators":         rep("a://b:c"),
		"quotes":                 rep(`"password":"`),
		"glued failing runs":     rep("sk-" + strings.Repeat("a", 510) + "1"),
		"glued slack runs":       rep("xoxb-" + strings.Repeat("a", 520) + "1"),
		"pem prose mentions":     rep(join("-----BEGIN ", "PRIVATE", " KEY----- and then ")),
		"yaml blocks":            rep("api_key: |\n  api_key: |\n    aaaa\n"),
		"single quotes in json":  rep(`{"t":"password: 'a","u":"`),
		"pem quoted mentions":    rep(join("-----BEGIN ", "PRIVATE", ` KEY-----" + x + "`)),
		"pem lines without end":  rep(join("-----BEGIN ", "PRIVATE", " KEY-----\n# ") + strings.Repeat("Ab3/", 16) + "\n# "),
		"pem ends without key":   rep(join("-----BEGIN ", "PRIVATE", ` KEY-----", "`, "-----END ", "PRIVATE", ` KEY-----", "`)),
		"split keys":             rep(join("# -----BEGIN ", "PRIVATE", " KEY-----\n# ") + strings.Repeat("Ab3/", 16) + "\n# " + strings.Repeat("Qz9+", 16) + join("\n# -----END ", "PRIVATE", " KEY-----\n")),
		"escaped key lines":      rep(join("-----BEGIN ", "PRIVATE", ` KEY-----\u0022 \u002B \u0022`) + strings.Repeat(`Ab3\u002B`, 12) + `\n\u0022 `),
		"short runs to an end":   rep(join("-----BEGIN ", "PRIVATE", " KEY-----\n") + strings.Repeat("Ab3/", 16) + strings.Repeat(" ab", 10000) + join("-----END ", "PRIVATE", " KEY-----\n")),
		"url token users":        rep("https://ghp_" + strings.Repeat("a1", 18) + ":x@h "),
		"keys in a quoted value": `password: "` + rep("token:"),
		"nested quoted values":   `password: "` + rep("a_token='Zx9 "),
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

// The one-shot calls run through the same fixed windows as a Reader, so their
// working memory does not grow with the input or its density of trigger
// literals. Before, String on 16 MiB of "hf_" allocated about 836 MiB.
func TestOneShotMemoryIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates 16 MiB inputs")
	}
	const size = 16 << 20
	allocated := func(f func()) uint64 {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		return b.TotalAlloc - a.TotalAlloc
	}
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)+1)[:size] }
	const bound = 4 << 20
	pii := New(WithPII())
	cases := []struct {
		name string
		red  *Redactor
		unit string
	}{
		{"hugging face literal", Default(), "hf_"},
		{"openai literal", Default(), "sk-"},
		{"url separators", Default(), "://"},
		{"keyed separators", Default(), "token:"},
		{"quoted keys", Default(), `"password":"`},
		{"pem markers", Default(), join("-----BEGIN ", "PRIVATE", " KEY-----")},
		{"email at signs", pii, "@"},
	}
	for _, tc := range cases {
		in := fill(tc.unit)
		if n := allocated(func() { tc.red.String(in) }); n > bound {
			t.Errorf("String(%s): allocated %d MiB", tc.name, n>>20)
		}
	}
	// Check shares the window loop; it also keeps counts rather than findings,
	// so a secret on every line costs nothing either.
	in := fill("hf_")
	if n := allocated(func() { _ = Default().Check(in) }); n > bound {
		t.Errorf("Check(hugging face literal): allocated %d MiB", n>>20)
	}
	in = fill("password=Zx9!Zx9!\n")
	if n := allocated(func() { _ = Default().Check(in) }); n > bound {
		t.Errorf("Check on dense secrets: allocated %d MiB", n>>20)
	}
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
