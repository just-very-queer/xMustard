package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// --- validators ---

func TestShapeValidatorsAcceptValidPayloads(t *testing.T) {
	valid := []struct{ client, tool, payload string }{
		{"claude", "Bash", `{"stdout":"ok","stderr":"","interrupted":false,"isImage":false}`},
		{"claude", "Bash", `{"stdout":"ok","stderr":"x","interrupted":true,"returnCodeInterpretation":"exit 1"}`},
		{"claude", "Read", `{"type":"text","file":{"filePath":"/a.go","content":"package a\n","numLines":1,"startLine":1,"totalLines":1}}`},
		{"claude", "Grep", `{"mode":"content","numFiles":2,"filenames":[],"content":"a.go:1:x","numLines":1}`},
		{"claude", "Grep", `{"mode":"files_with_matches","numFiles":2,"filenames":["a.go","b.go"]}`},
		{"claude", "Glob", `{"filenames":["a.go"],"durationMs":3,"numFiles":1,"truncated":false}`},
		{"claude", "mcp__srv__tool", `{"content":[{"type":"text","text":"x"}]}`},
		{"codex", "shell", `{"decision":"block","reason":"projected output"}`},
		{"pi", "bash", `{"content":[{"type":"text","text":"x"}],"details":{"truncation":null},"isError":false}`},
		{"pi", "read", `{"content":[{"type":"text","text":"x"},{"type":"image","data":"AAAA","mimeType":"image/png"}]}`},
		{"opencode", "bash", `{"title":"bash","output":"x","metadata":{"exit":0}}`},
		{"cursor", "MCP:tool", `{"content":[{"type":"text","text":"x"}],"isError":false}`},
		{"mcp", "any", `{"content":[{"type":"text","text":"x"}]}`},
	}
	for _, v := range valid {
		if err := ValidateShape(v.client, v.tool, []byte(v.payload)); err != nil {
			t.Errorf("%s %s rejected a valid payload: %v", v.client, v.tool, err)
		}
	}
}

func TestShapeValidatorsRejectMismatches(t *testing.T) {
	invalid := []struct{ client, tool, payload, why string }{
		{"claude", "Bash", `{"stdout":"ok","stderr":""}`, "interrupted missing"},
		{"claude", "Bash", `{"stdout":1,"stderr":"","interrupted":false}`, "stdout not a string"},
		{"claude", "Bash", `"just text"`, "not an object"},
		{"claude", "Read", `{"type":"image","file":{"base64":"AAAA","type":"image/png"}}`, "image read"},
		{"claude", "Read", `{"type":"text","file":{"filePath":"/a","content":"x","startLine":1,"totalLines":1}}`, "numLines missing"},
		{"claude", "Grep", `{"numFiles":1,"filenames":[1]}`, "filenames not strings"},
		{"claude", "Grep", `{"mode":"lines","numFiles":1,"filenames":[]}`, "unknown mode"},
		{"claude", "Glob", `{"filenames":["a"],"numFiles":1}`, "truncated missing"},
		{"codex", "shell", `{"decision":"approve","reason":"x"}`, "not block"},
		{"pi", "bash", `{"content":[{"type":"text"}]}`, "text block without text"},
		{"pi", "bash", `{"content":[]}`, "empty content"},
		{"opencode", "bash", `{"title":"t","output":"x"}`, "metadata missing"},
		{"cursor", "MCP:x", `{"content":[{"type":"image","data":"A","mimeType":"image/png"}]}`, "image in an MCP text replacement"},
		{"mcp", "any", `{"text":"x"}`, "no content"},
		{"nobody", "any", `{}`, "unknown client"},
	}
	for _, v := range invalid {
		if err := ValidateShape(v.client, v.tool, []byte(v.payload)); err == nil {
			t.Errorf("%s %s accepted a mismatch (%s)", v.client, v.tool, v.why)
		}
	}
}

// --- hook body decoding ---

func TestHookBodyStreamDecode(t *testing.T) {
	input := `{"command":"go test ./...","description":"run \"tests\"","timeout":120000}`
	// tool_response comes before tool_name: the decoder does not depend on key order
	body := `{"session_id":"s-1","transcript_path":"/t.jsonl","cwd":"/repo","hook_event_name":"PostToolUse",` +
		`"tool_response":{"stdout":"line1\nüñí é 😀 tab\there \"q\" back\\slash \/ ` + "\xff" + `\ud800x","stderr":"warn\n","interrupted":false,"isImage":false},` +
		`"tool_name":"Bash","tool_input":` + input + `,"tool_use_id":"toolu_01"}`
	var spool bytes.Buffer
	hb, err := DecodeHookBody(FormatClaude, strings.NewReader(body), &spool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hb.ToolName != "Bash" || hb.SessionID != "s-1" || hb.CallID != "toolu_01" || hb.Event != "PostToolUse" || hb.Cwd != "/repo" {
		t.Fatalf("metadata: %+v", hb)
	}
	sum := sha256.Sum256([]byte(input))
	if hb.ArgsDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("args digest is not the sha256 of the exact tool_input bytes")
	}
	if hb.Input["command"] != "go test ./..." || hb.Input["description"] != `run "tests"` {
		t.Fatalf("input fields: %v", hb.Input)
	}
	whole := sha256.Sum256([]byte(body))
	if hb.BodySHA256 != hex.EncodeToString(whole[:]) || hb.BodyBytes != int64(len(body)) {
		t.Fatalf("body digest/size: %s %d", hb.BodySHA256, hb.BodyBytes)
	}
	// stdout, stderr, then the status line the decoder adds for "interrupted"
	if len(hb.Sections) != 3 || hb.Sections[0].Name != "stdout" || hb.Sections[1].Name != "stderr" ||
		hb.Sections[2].Name != StatusSection || !hb.Sections[2].Status {
		t.Fatalf("sections: %+v", hb.Sections)
	}
	stdout := spool.String()[hb.Sections[0].Start:hb.Sections[0].End]
	want := "line1\nüñí é 😀 tab\there \"q\" back\\slash / ��x"
	if stdout != want {
		t.Fatalf("decoded stdout %q, want %q", stdout, want)
	}
	if spool.String()[hb.Sections[1].Start:hb.Sections[1].End] != "warn\n" {
		t.Fatalf("stderr section wrong")
	}
	if got := spool.String()[hb.Sections[2].Start:hb.Sections[2].End]; got != "[xmustard status] interrupted=false\n" || hb.IsError {
		t.Fatalf("status section %q (is_error %v)", got, hb.IsError)
	}
	if hb.Response == nil || hb.Response.Kind != 'o' || len(hb.Response.Kids) != 4 || hb.Scalar("interrupted") != "false" {
		t.Fatalf("skeleton: %+v", hb.Response)
	}
	// surrogate escapes decode exactly as encoding/json does: a lone surrogate
	// becomes U+FFFD and never swallows the escape or character after it
	for _, esc := range []string{`\ud800A`, `\udc00A`, `\ud800😀`, `\ud800\u000atail`, `😀`, `\ud800𐀀`,
		`\udfff\ud800`, `\ud800`, `x\ud800é`} {
		var want string
		if err := json.Unmarshal([]byte(`"`+esc+`"`), &want); err != nil {
			t.Fatal(err)
		}
		var sp bytes.Buffer
		hb, err := DecodeHookBody(FormatClaude, strings.NewReader(`{"tool_name":"Bash","tool_response":{"stdout":"`+esc+`"}}`), &sp, nil)
		if err != nil {
			t.Fatalf("%s: %v", esc, err)
		}
		if got := sp.String()[hb.Sections[0].Start:hb.Sections[0].End]; got != want {
			t.Errorf("%s: decoded %q, encoding/json %q", esc, got, want)
		}
	}
}

// Output strings open at most maxHookSections sections: the rest are appended to one
// overflow section, empty strings open none, the whole body is still captured, and
// decoding memory stays bounded whatever the element count.
func TestHookBodySectionCountIsBounded(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("allocation bound is not meaningful under -race")
	}
	const n = 200000
	var b strings.Builder
	b.WriteString(`{"tool_name":"mcp__gh__list_issues","tool_response":[`)
	nonEmpty := 0
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		switch i % 3 {
		case 0:
			b.WriteString(`{"type":"text","text":"x"}`)
			nonEmpty++
		case 1:
			b.WriteString(`""`)
		default:
			b.WriteString(`"y"`)
			nonEmpty++
		}
	}
	b.WriteString(`],"hook_event_name":"PostToolUse"}`)
	body := b.String()
	var spool bytes.Buffer
	spool.Grow(n)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	hb, err := DecodeHookBody(FormatClaude, strings.NewReader(body), &spool, nil)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d elements: %d sections, %d KiB allocated, spool %d bytes", n, len(hb.Sections), alloc>>10, spool.Len())
	last := hb.Sections[len(hb.Sections)-1]
	if len(hb.Sections) != maxHookSections+1 || last.Name != OverflowSection || !hb.Incomplete || last.End != int64(spool.Len()) {
		t.Fatalf("sections not bounded: %d, last %+v", len(hb.Sections), last)
	}
	// every non-empty string was captured, one per line in the overflow section
	if got := strings.Count(spool.String(), "x") + strings.Count(spool.String(), "y"); got != nonEmpty || last.Merged != nonEmpty-maxHookSections {
		t.Fatalf("captured %d strings (%d merged), want %d", got, last.Merged, nonEmpty)
	}
	// allocation is per element (a JSON path each), never per section: a small
	// constant per element, and nothing retained
	if !raceEnabled && alloc > uint64(n)*256 {
		t.Fatalf("decoding %d tiny elements allocated %d bytes", n, alloc)
	}
	// a pathologically deep path is capped, so its frames hold bounded names
	deep := `{"tool_name":"x","tool_response":` + strings.Repeat(`{"`+strings.Repeat("k", 1000)+`":`, 300) + `"v"` + strings.Repeat("}", 300) + `}`
	hb, err = DecodeHookBody(FormatClaude, strings.NewReader(deep), io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range hb.Sections {
		if len(s.Name) > maxSectionName {
			t.Fatalf("section name of %d bytes", len(s.Name))
		}
	}
}

func TestHookBodyRejectsMalformed(t *testing.T) {
	for _, b := range []string{
		``, `[]`, `{"tool_name":"Bash"`, `{"tool_name":"Bash"} trailing`, `{"tool_response":{"stdout":"x}}`,
		`{"tool_response":{"stdout":"bad \q escape"}}`, "{\"tool_response\":\"ctl \x01\"}", `{"a":tru}`,
	} {
		if _, err := DecodeHookBody(FormatClaude, strings.NewReader(b), io.Discard, nil); !errors.Is(err, ErrBadBody) {
			t.Errorf("%q: want ErrBadBody, got %v", b, err)
		}
	}
	if _, err := DecodeHookBody("nope", strings.NewReader(`{}`), io.Discard, nil); !errors.Is(err, ErrBadBody) {
		t.Fatalf("unknown format accepted")
	}
}

// hookBodyReader generates a Claude PostToolUse body with an n-byte stdout of
// escaped test output on the fly, so the test holds no copy of it.
type hookBodyReader struct {
	head, tail []byte
	line       []byte
	n, sent    int64
	pos        int
	stage      int
}

func newHookBodyReader(stdoutBytes int64) *hookBodyReader {
	return &hookBodyReader{
		head: []byte(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."},"tool_use_id":"t1","tool_response":{"stdout":"`),
		line: []byte(`--- PASS: TestCase (0.00s) \"ok\"\n`),
		tail: []byte(`--- FAIL: TestBroken (0.01s)\n    x_test.go:9: got 1 want 2\nFAIL\n","stderr":"","interrupted":false,"isImage":false}}`),
		n:    stdoutBytes,
	}
}

func (r *hookBodyReader) Read(p []byte) (int, error) {
	w := 0
	for w < len(p) {
		switch r.stage {
		case 0:
			k := copy(p[w:], r.head[r.pos:])
			w, r.pos = w+k, r.pos+k
			if r.pos == len(r.head) {
				r.stage, r.pos = 1, 0
			}
		case 1:
			if r.sent >= r.n {
				r.stage, r.pos = 2, 0
				continue
			}
			k := copy(p[w:], r.line[r.pos:])
			w, r.pos, r.sent = w+k, r.pos+k, r.sent+int64(k)
			if r.pos == len(r.line) {
				r.pos = 0
			}
		case 2:
			k := copy(p[w:], r.tail[r.pos:])
			w, r.pos = w+k, r.pos+k
			if r.pos == len(r.tail) {
				r.stage = 3
			}
		default:
			if w == 0 {
				return 0, io.EOF
			}
			return w, nil
		}
	}
	return w, nil
}

// A 16 MiB Claude Code hook body is stream-decoded into the spool with allocation
// below a fixed bound (the body never exists in memory whole).
func TestSixteenMiBHookBodyStreamsIntoSpool(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { l.MaxOriginal = 24 << 20 })
	sp, err := s.NewSpool("ws")
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Discard()
	body := newHookBodyReader(16 << 20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	hb, err := DecodeHookBody(FormatClaude, body, sp, nil)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("decoded a %d-byte body: %d KiB allocated, spool %d bytes", hb.BodyBytes, alloc>>10, sp.n)
	if hb.BodyBytes < 16<<20 {
		t.Fatalf("body only %d bytes", hb.BodyBytes)
	}
	if !raceEnabled && alloc > 1<<20 {
		t.Fatalf("decoding allocated %d bytes; the bound is 1 MiB", alloc)
	}
	// the empty stderr opens no section; the status line ends the spool
	if sp.Over() || len(hb.Sections) != 2 || hb.Sections[0].Name != "stdout" || hb.Sections[1].Name != StatusSection || hb.Sections[1].End != sp.n {
		t.Fatalf("spool/sections: over=%v %+v n=%d", sp.Over(), hb.Sections, sp.n)
	}
	// the decoded bytes are what the tool printed: escapes resolved
	tail := make([]byte, 64)
	if _, err := sp.f.ReadAt(tail, hb.Sections[0].End-64); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(tail, []byte("got 1 want 2\nFAIL\n")) {
		t.Fatalf("decoded tail %q", tail)
	}
}

// --- the capture pipeline ---

func observe(t *testing.T, s *Store, format HookFormat, body string, meta CaptureMeta) *ObservationResult {
	t.Helper()
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", RepoScope: "/repo",
		Format: format, Body: strings.NewReader(body), Meta: meta})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return res
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestClaudeBashCaptureIsShapeMatchedAndRecoverable(t *testing.T) {
	s, _ := testStore(t, nil)
	stdout := string(unittestRun(3000, 60))
	body := `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"python -m unittest -v"},` +
		`"tool_response":{"stdout":` + jsonString(stdout) + `,"stderr":"DeprecationWarning: x\n","interrupted":false,"isImage":false},"tool_use_id":"toolu_9"}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Family != FamilyTest || res.Reducer != "xm-test/1" || !res.Reduced || res.Handle == "" {
		t.Fatalf("capture: family %s reducer %s reduced %v", res.Family, res.Reducer, res.Reduced)
	}
	if res.CapturedIdentity != "unknown" || res.Capture.CapturedIdentity != "unknown" {
		t.Fatalf("hook-delivered bytes must be captured_identity=unknown")
	}
	if res.Capture.Tool != "Bash" || res.Capture.CallID != "toolu_9" || res.Capture.SessionID != "s1" || res.Capture.OutputShape != "claude.Bash" ||
		res.Capture.ArgsDigest == "" || res.Capture.HookEvent != "PostToolUse" || res.Capture.BodySHA256 == "" {
		t.Fatalf("capture metadata: %+v", res.Capture)
	}
	if res.Shape.Mode != ShapeReplace {
		t.Fatalf("shape: %+v", res.Shape)
	}
	if err := ValidateShape("claude", "Bash", res.Shape.Payload); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Stdout, Stderr string
		Interrupted    bool
		IsImage        bool
	}
	if err := json.Unmarshal(res.Shape.Payload, &out); err != nil {
		t.Fatal(err)
	}
	mustContain(t, out.Stdout, "[xmustard test] xm-test/1", "passed=3000 failed=1", "AssertionError: 3 != 4", "[xmustard evidence] ", res.Handle)
	if out.Stderr != "DeprecationWarning: x\n" || out.Interrupted || out.IsImage {
		t.Fatalf("other Bash fields changed: %+v", out)
	}
	if res.Shape.Chars > PolicyFor("claude").MaxChars || res.DeliveredTokensEst == 0 || res.TokenEstimator != TokenEstimator {
		t.Fatalf("budget: chars %d tokens %d", res.Shape.Chars, res.DeliveredTokensEst)
	}
	// the exact decoded original (output strings, then the status line) is
	// recoverable through the handle
	got, _ := readAll(t, s, "ws", "", res.Handle, nil)
	if string(got) != stdout+"DeprecationWarning: x\n[xmustard status] interrupted=false\n" {
		t.Fatalf("recovered original differs (%d bytes vs %d)", len(got), len(stdout)+22)
	}
	// and the persisted record names the family reducer, client and shape
	fam := readObservation(t, s, "ws", res.Handle).Projection.Family
	if fam == nil || fam.Reducer != "xm-test/1" || fam.Client != "claude" || fam.Shape != "claude.Bash" || fam.Capture == nil || fam.Capture.CallID != "toolu_9" {
		t.Fatalf("persisted family record: %+v", fam)
	}
}

func readObservation(t *testing.T, s *Store, ws, handle string) Observation {
	t.Helper()
	key, _ := handleKey(handle)
	var obs Observation
	if err := readJSON(s.root+"/"+ws+"/"+key+"/meta.json", &obs); err != nil {
		t.Fatal(err)
	}
	return obs
}

func TestClaudeReadGrepGlobShapes(t *testing.T) {
	s, _ := testStore(t, nil)
	src := string(sourceFile(4000))
	read := `{"tool_name":"Read","tool_input":{"file_path":"/repo/big.go"},"tool_response":{"type":"text","file":{"filePath":"/repo/big.go","content":` +
		jsonString(src) + `,"numLines":` + fmt.Sprint(countLines([]byte(src))) + `,"startLine":1,"totalLines":` + fmt.Sprint(countLines([]byte(src))) + `}}}`
	res := observe(t, s, FormatClaude, read, CaptureMeta{Client: "claude"})
	if res.Family != FamilyRead || res.Shape.Mode != ShapeReplace {
		t.Fatalf("read: %s %+v", res.Family, res.Shape)
	}
	var rd struct {
		Type string
		File struct {
			FilePath                        string
			Content                         string
			NumLines, StartLine, TotalLines int
		}
	}
	_ = json.Unmarshal(res.Shape.Payload, &rd)
	if rd.File.FilePath != "/repo/big.go" || !strings.HasPrefix(rd.File.Content, "package big\n") || rd.File.StartLine != 1 ||
		rd.File.NumLines != countLines([]byte(rd.File.Content)) || rd.File.TotalLines != countLines([]byte(src)) {
		t.Fatalf("read payload: path %s start %d num %d total %d", rd.File.FilePath, rd.File.StartLine, rd.File.NumLines, rd.File.TotalLines)
	}
	mustContain(t, rd.File.Content, "read on from line", "[xmustard evidence]")
	if st := res.Structured.(*ReadProjection); st.Path != "/repo/big.go" || st.LineCount != countLines([]byte(src)) {
		t.Fatalf("read projection: %+v", st)
	}

	var files []string
	for i := 0; i < 700; i++ {
		files = append(files, fmt.Sprintf("src/pkg%d/f%04d.go", i%9, i))
	}
	fl, _ := json.Marshal(files)
	glob := `{"tool_name":"Glob","tool_input":{"pattern":"**/*.go"},"tool_response":{"filenames":` + string(fl) + `,"durationMs":12,"numFiles":700,"truncated":false}}`
	res = observe(t, s, FormatClaude, glob, CaptureMeta{Client: "claude"})
	var gl struct {
		Filenames []string
		NumFiles  int
		Truncated bool
	}
	_ = json.Unmarshal(res.Shape.Payload, &gl)
	if res.Family != FamilyGlob || res.Shape.Mode != ShapeReplace || gl.NumFiles != 700 || !gl.Truncated || len(gl.Filenames) > 62 || gl.Filenames[0] != files[0] {
		t.Fatalf("glob: %s %+v %d names", res.Family, res.Shape.Mode, len(gl.Filenames))
	}
	// content mode: an empty filenames list precedes the content; the recovery line
	// and numLines follow the content, and the empty list stays []
	var content strings.Builder
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&content, "src/pkg%d/f%04d.go:%d:\tneedle := %d\n", i%9, i%70, i+1, i)
	}
	grepContent := `{"tool_name":"Grep","tool_input":{"pattern":"needle","output_mode":"content"},"tool_response":{"mode":"content","numFiles":70,"filenames":[],"content":` +
		jsonString(content.String()) + `,"numLines":900}}`
	res = observe(t, s, FormatClaude, grepContent, CaptureMeta{Client: "claude"})
	var gc struct {
		Mode      string
		NumFiles  int
		Filenames []string
		Content   string
		NumLines  int
	}
	_ = json.Unmarshal(res.Shape.Payload, &gc)
	if res.Shape.Mode != ShapeReplace || gc.Filenames == nil || len(gc.Filenames) != 0 || gc.NumFiles != 70 ||
		!strings.Contains(gc.Content, "[xmustard evidence]") || gc.NumLines != countLines([]byte(gc.Content)) || !strings.Contains(string(res.Shape.Payload), `"filenames":[]`) {
		t.Fatalf("grep content: %s %+v", res.Shape.Mode, gc)
	}
	grep := `{"tool_name":"Grep","tool_input":{"pattern":"needle","output_mode":"files_with_matches"},"tool_response":{"mode":"files_with_matches","filenames":` + string(fl) + `,"numFiles":700}}`
	res = observe(t, s, FormatClaude, grep, CaptureMeta{Client: "claude"})
	if res.Family != FamilyGrep || res.Shape.Mode != ShapeReplace {
		t.Fatalf("grep: %s %+v", res.Family, res.Shape)
	}
	if err := ValidateShape("claude", "Grep", res.Shape.Payload); err != nil {
		t.Fatal(err)
	}
}

func TestShapeMismatchFallsBack(t *testing.T) {
	s, _ := testStore(t, nil)
	// an image read cannot carry a text projection: the client keeps its original
	img := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 30000))
	body := `{"tool_name":"Read","tool_input":{"file_path":"/a.png"},"tool_response":{"type":"image","file":{"base64":"` + img +
		`","type":"image/png","originalSize":120000},"note":` + jsonString(strings.Repeat("caption line\n", 2000)) + `}}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Shape.Mode != ShapeFallback || res.Shape.Payload != nil || !strings.Contains(res.Shape.Reason, "binary") || res.Shape.Notice == "" {
		t.Fatalf("image read must fall back: %+v", res.Shape)
	}
	// a Bash payload missing "interrupted" (a client that changed its schema) is
	// rejected by the validator, and an oversized original gets an explicit size error
	big := strings.Repeat("output line that is long enough to matter\n", 2000)
	body = `{"tool_name":"Bash","tool_input":{"command":"./x"},"tool_response":{"stdout":` + jsonString(big) + `,"stderr":""}}`
	res = observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Shape.Mode != ShapeSizeError || !strings.Contains(res.Shape.Reason, "interrupted") || len([]rune(res.Shape.Notice)) > 10000 {
		t.Fatalf("schema mismatch must be a size error: %+v", res.Shape)
	}
	// small outputs are not replaced at all
	body = `{"tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"a\nb\n","stderr":"","interrupted":false,"isImage":false}}`
	res = observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if res.Shape.Mode != ShapeUnchanged || res.Handle != "" || res.Reduced {
		t.Fatalf("small output: %+v", res.Shape)
	}
}

func TestOtherClientShapes(t *testing.T) {
	s, _ := testStore(t, nil)
	log := string(goTestRun(3000))
	// Pi tool_result: content blocks plus details, isError preserved
	pi := `{"type":"tool_result","toolName":"bash","toolCallId":"c7","input":{"command":"go test ./..."},"content":[{"type":"text","text":` +
		jsonString(log) + `}],"details":{"truncation":null,"fullOutputPath":"/tmp/pi-out.txt"},"isError":true}`
	res := observe(t, s, FormatPi, pi, CaptureMeta{Client: "pi"})
	var pp struct {
		Content []struct{ Type, Text string }
		Details map[string]any
		IsError bool
	}
	_ = json.Unmarshal(res.Shape.Payload, &pp)
	if res.Family != FamilyTest || res.Shape.Mode != ShapeReplace || !pp.IsError || pp.Details["fullOutputPath"] != "/tmp/pi-out.txt" ||
		!strings.Contains(pp.Content[0].Text, "--- FAIL: TestParseValue") || res.Capture.CallID != "c7" || !res.Capture.IsError {
		t.Fatalf("pi: %s %+v %+v", res.Family, res.Shape.Mode, pp)
	}
	// OpenCode tool.execute.after
	oc := `{"tool":"bash","sessionID":"ses_1","callID":"call_1","args":{"command":"go test ./..."},"output":{"title":"go test","output":` +
		jsonString(log) + `,"metadata":{"exit":1,"description":"tests"}}}`
	res = observe(t, s, FormatOpenCode, oc, CaptureMeta{Client: "opencode"})
	var op struct {
		Title    string
		Output   string
		Metadata map[string]any
	}
	_ = json.Unmarshal(res.Shape.Payload, &op)
	if res.Shape.Mode != ShapeReplace || op.Title != "go test" || op.Metadata["exit"] != float64(1) || !strings.Contains(op.Output, "xm-test/1") {
		t.Fatalf("opencode: %+v %+v", res.Shape.Mode, op)
	}
	// Codex: decision block with the projection as the reason
	cx := `{"session_id":"s","turn_id":"t","hook_event_name":"PostToolUse","tool_name":"shell","tool_input":{"command":["bash","-lc","go test ./..."],"workdir":"/repo"},"tool_response":{"output":` +
		jsonString(log) + `,"exit_code":1}}`
	res = observe(t, s, FormatCodex, cx, CaptureMeta{Client: "codex"})
	var cd struct{ Decision, Reason string }
	_ = json.Unmarshal(res.Shape.Payload, &cd)
	if res.Shape.Mode != ShapeReplace || cd.Decision != "block" || !strings.Contains(cd.Reason, "exit=1") || res.Capture.ExitCode == nil || *res.Capture.ExitCode != 1 ||
		res.Family != FamilyTest {
		t.Fatalf("codex: %+v %+v", res.Shape.Mode, cd)
	}
	// Cursor: built-in shell output is observe-only; MCP results are replaced
	cu := `{"hook_event_name":"afterShellExecution","conversation_id":"conv","command":"go test ./...","output":` + jsonString(log) + `}`
	res = observe(t, s, FormatCursor, cu, CaptureMeta{Client: "cursor", Tool: "Shell"})
	if res.Shape.Mode != ShapeObserve || res.Capture.Tool != "Shell" || res.Family != FamilyTest {
		t.Fatalf("cursor shell: %+v %s", res.Shape, res.Family)
	}
	cm := `{"hook_event_name":"afterMCPExecution","tool_name":"MCP:ci_logs","tool_input":{"job":"42"},"result_json":` + jsonString(log) + `}`
	res = observe(t, s, FormatCursor, cm, CaptureMeta{Client: "cursor"})
	if res.Shape.Mode != ShapeReplace {
		t.Fatalf("cursor mcp: %+v", res.Shape)
	}
}

func TestRawCaptureForAnyTool(t *testing.T) {
	s, _ := testStore(t, nil)
	two := 2
	raw := string(noisyShell())
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatRaw, Body: strings.NewReader(raw),
		Meta: CaptureMeta{Client: "pi", Tool: "bash", ExitCode: &two, CallID: "c1"}, Sel: Selector{Command: "./deploy.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Family != FamilyShell || res.Capture.OutputShape != "raw" || !res.Capture.IsError || res.Facts.ExitCode == nil || *res.Facts.ExitCode != 2 {
		t.Fatalf("raw: %s %+v %+v", res.Family, res.Capture, res.Facts)
	}
	if res.Shape.Mode != ShapeReplace || ValidateShape("pi", "bash", res.Shape.Payload) != nil {
		t.Fatalf("raw pi shape: %+v", res.Shape)
	}
	// the replacement never turns a failed call into a success: a raw capture has no
	// body, and its error status comes from the capture metadata
	var pp struct{ IsError bool }
	if err := json.Unmarshal(res.Shape.Payload, &pp); err != nil || !pp.IsError {
		t.Fatalf("raw pi payload must say isError (exit 2): %s", res.Shape.Payload)
	}
	for _, meta := range []CaptureMeta{
		{Client: "mcp", Tool: "mcp__srv__deploy", IsError: true},
		{Client: "cursor", Tool: "MCP:deploy", IsError: true},
		{Client: "letta", Tool: "mcp__srv__deploy", ExitCode: &two},
		{Client: "http", Tool: "deploy", IsError: true},
	} {
		res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatRaw, Body: strings.NewReader(raw), Meta: meta})
		if err != nil {
			t.Fatal(err)
		}
		var mp struct{ IsError bool }
		if res.Shape.Mode != ShapeReplace || json.Unmarshal(res.Shape.Payload, &mp) != nil || !mp.IsError || !res.Capture.IsError {
			t.Fatalf("%s %s: shape %s payload isError=%v", meta.Client, meta.Tool, res.Shape.Mode, mp.IsError)
		}
	}
	if _, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", Format: FormatRaw, Body: strings.NewReader("x")}); !errors.Is(err, ErrBadBody) {
		t.Fatalf("a capture without a tool name must be refused: %v", err)
	}
}

func TestPolicyTableLowersTargets(t *testing.T) {
	for _, p := range ClientPolicies() {
		if p.Target <= 0 || p.Target > DefaultProjectionTarget || p.MaxChars <= 0 || p.Replacement == "" {
			t.Fatalf("policy %+v", p)
		}
	}
	if PolicyFor("claude").ContextChars != 10000 || PolicyFor("codex").ContextTokens != 2500 || PolicyFor("letta").MaxChars != 30000 ||
		PolicyFor("unknown").Client != "http" {
		t.Fatalf("policy table drifted")
	}
}

func TestManySectionsShareTheTarget(t *testing.T) {
	s, _ := testStore(t, nil)
	var blocks []string
	for i := 0; i < 10; i++ {
		blocks = append(blocks, `{"type":"text","text":`+jsonString(strings.Repeat(fmt.Sprintf("block %d line of output\n", i), 3000))+`}`)
	}
	body := `{"tool_name":"mcp__ci__logs","tool_input":{"job":"7"},"tool_response":[` + strings.Join(blocks, ",") + `]}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	if len(res.Projection) > PolicyFor("claude").Target || res.Shape.Mode != ShapeReplace {
		t.Fatalf("10 sections: projection %d bytes, shape %+v", len(res.Projection), res.Shape.Mode)
	}
	var out []map[string]any
	if err := json.Unmarshal(res.Shape.Payload, &out); err != nil || len(out) != 10 {
		t.Fatalf("MCP content array not preserved: %v %d", err, len(out))
	}
	if strings.Count(string(res.Shape.Payload), "[xmustard evidence]") != 1 {
		t.Fatalf("recovery line must appear once")
	}
}

// A status member of the tool response (outside the output strings) is never lost:
// it is spooled as the status line, so every text payload carries it and search
// finds it in the retained original, and a failing status marks the call an error.
func TestStatusMembersSurviveInPayloadAndOriginal(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { *l = DefaultLimits() })
	out := jsonString(strings.Repeat("ci log line with nothing wrong in it\n", 9000))
	for _, c := range []struct {
		format HookFormat
		client string
		body   string
	}{
		{FormatCursor, "cursor", `{"hook_event_name":"postToolUse","tool_name":"MCP:ci_status","tool_output":{"status":"failed","conclusion":"failure","output":` + out + `}}`},
		{FormatCodex, "codex", `{"hook_event_name":"PostToolUse","tool_name":"mcp__ci__status","tool_input":{},"tool_response":{"status":"failed","conclusion":"failure","content":` + out + `}}`},
		{FormatClaude, "claude", `{"hook_event_name":"PostToolUse","tool_name":"mcp__ci__status","tool_input":{},"tool_response":{"status":"failed","conclusion":"failure","content":` + out + `}}`},
		{FormatClaude, "mcp", `{"hook_event_name":"PostToolUse","tool_name":"mcp__ci__status","tool_input":{},"tool_response":{"status":"failed","conclusion":"failure","content":` + out + `}}`},
	} {
		res := observe(t, s, c.format, c.body, CaptureMeta{Client: c.client})
		if res.Shape.Mode != ShapeReplace || !res.Capture.IsError {
			t.Fatalf("%s: shape %s (%s), is_error %v", c.client, res.Shape.Mode, res.Shape.Reason, res.Capture.IsError)
		}
		payload := string(res.Shape.Payload)
		if !strings.Contains(payload, "failed") || !strings.Contains(payload, "failure") {
			t.Fatalf("%s: payload lost the status:\n%.600s", c.client, payload)
		}
		var pe struct{ IsError *bool }
		if json.Unmarshal(res.Shape.Payload, &pe) == nil && pe.IsError != nil && !*pe.IsError {
			t.Fatalf("%s: payload says isError=false for a failed call", c.client)
		}
		sr, err := s.Search(context.Background(), SearchRequest{ReadRequest: ReadRequest{WorkspaceID: "ws", Handle: res.Handle}, Query: "failed"})
		if err != nil || sr.Matches != 1 || !strings.Contains(sr.Lines[0].Text, `status="failed"`) {
			t.Fatalf("%s: search for the status in the original: %v %+v", c.client, err, sr)
		}
	}
	// a status member inside an array cannot be carried by a text payload: the client
	// keeps its original output instead of a replacement that drops it
	body := `{"tool_name":"mcp__ci__jobs","tool_response":{"jobs":[{"status":"failed"},{"status":"ok"}],"content":` + out + `}}`
	res := observe(t, s, FormatCodex, body, CaptureMeta{Client: "codex"})
	if res.Shape.Mode == ShapeReplace || !strings.Contains(res.Shape.Reason, "status") {
		t.Fatalf("array status: %s %s", res.Shape.Mode, res.Shape.Reason)
	}
}

// A result of 100,000 content blocks is captured in bounded memory: at most
// maxHookSections sections plus one overflow section, a projection within the
// client target, a small persisted record, and page reads that stay cheap.
func TestManyContentBlocksCaptureIsBounded(t *testing.T) {
	if raceEnabled && testing.Short() {
		t.Skip("allocation bound is not meaningful under -race")
	}
	s, _ := testStore(t, func(l *Limits) { *l = DefaultLimits() })
	const n = 100000
	var b strings.Builder
	b.WriteString(`{"tool_name":"mcp__gh__list_issues","tool_input":{"repo":"x"},"tool_response":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":"text","text":"issue #%d: something broke in module %d"}`, i, i%50)
	}
	b.WriteString(`]}`)
	body := b.String()
	for _, client := range []string{"claude", "codex"} {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		res := observe(t, s, FormatClaude, body, CaptureMeta{Client: client})
		runtime.ReadMemStats(&after)
		alloc := after.TotalAlloc - before.TotalAlloc
		obs := readObservation(t, s, "ws", res.Handle)
		key, _ := handleKey(res.Handle)
		info, err := os.Stat(filepath.Join(s.root, "ws", key, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d-byte body, %d KiB allocated, projection %d bytes, meta.json %d bytes, shape %s", client, len(body), alloc>>10,
			len(res.Projection), info.Size(), res.Shape.Mode)
		if len(res.Projection) > PolicyFor(client).Target || !res.Reduced || res.Handle == "" {
			t.Fatalf("%s: projection %d bytes for a %d target", client, len(res.Projection), PolicyFor(client).Target)
		}
		if secs := obs.Projection.Family.Sections; len(secs) != maxHookSections+1 || secs[maxHookSections].Merged != n-maxHookSections {
			t.Fatalf("%s: %d sections persisted", client, len(secs))
		}
		if info.Size() > 32<<10 {
			t.Fatalf("%s: meta.json %d bytes", client, info.Size())
		}
		// decoding allocates a small constant per element; reduction allocates the
		// same whatever the section count (pooled line readers)
		if !raceEnabled && alloc > uint64(n)*512 {
			t.Fatalf("%s: capturing %d blocks allocated %d bytes", client, n, alloc)
		}
		switch client {
		case "claude":
			// merged blocks cannot be put back into 100,000 content blocks: the client
			// keeps its original output (it persists large MCP results itself)
			if res.Shape.Mode != ShapeSizeError || res.Shape.Notice == "" {
				t.Fatalf("claude shape: %+v", res.Shape)
			}
		case "codex":
			if res.Shape.Mode != ShapeReplace || res.Shape.Chars > PolicyFor("codex").MaxChars {
				t.Fatalf("codex shape: %s %d chars", res.Shape.Mode, res.Shape.Chars)
			}
		}
		runtime.GC()
		runtime.ReadMemStats(&before)
		if _, err := s.Read(context.Background(), ReadRequest{WorkspaceID: "ws", Handle: res.Handle, Length: 100}); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		if read := after.TotalAlloc - before.TotalAlloc; !raceEnabled && read > 1<<20 {
			t.Fatalf("a 100-byte page read allocated %d bytes", read)
		}
	}
}

// A read with long lines is budgeted by what it delivers: the unnumbered excerpt a
// Claude Read payload carries is cut like the numbered projection, so the payload
// stays within the client's target and character cap.
func TestReadLongLinesPayloadWithinTarget(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { *l = DefaultLimits() })
	for _, width := range []int{2000, 4000, 50000} {
		var src strings.Builder
		for i := 1; i <= 100; i++ {
			fmt.Fprintf(&src, "%05d %s\n", i, strings.Repeat("x", width))
		}
		body := `{"tool_name":"Read","tool_input":{"file_path":"/repo/min.js"},"tool_response":{"type":"text","file":{"filePath":"/repo/min.js","content":` +
			jsonString(src.String()) + `,"numLines":100,"startLine":1,"totalLines":100}}}`
		res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
		var rd struct{ File struct{ Content string } }
		_ = json.Unmarshal(res.Shape.Payload, &rd)
		if res.Shape.Mode != ShapeReplace || res.Shape.Chars > PolicyFor("claude").MaxChars || len(rd.File.Content) > PolicyFor("claude").Target {
			t.Fatalf("%d-byte lines: shape %s (%s), %d chars, content %d bytes", width, res.Shape.Mode, res.Shape.Reason, res.Shape.Chars, len(rd.File.Content))
		}
		mustContain(t, rd.File.Content, "00001 xxx", "bytes omitted]")
	}
}

// A read of a range numbers its lines from the range's first line, and search in
// the original applies the same numbering: an omitted range is recovered exactly.
func TestReadRangeRecoveredBySearch(t *testing.T) {
	s, _ := testStore(t, func(l *Limits) { *l = DefaultLimits() })
	var src strings.Builder
	for i := 500; i < 3500; i++ {
		fmt.Fprintf(&src, "file line %d content here\n", i)
	}
	body := `{"tool_name":"Read","tool_input":{"file_path":"/repo/big.txt","offset":500},"tool_response":{"type":"text","file":{"filePath":"/repo/big.txt","content":` +
		jsonString(src.String()) + `,"numLines":3000,"startLine":500,"totalLines":9000}}}`
	res := observe(t, s, FormatClaude, body, CaptureMeta{Client: "claude"})
	m := regexp.MustCompile(`\[xmustard: lines (\d+)-(\d+) omitted\]`).FindStringSubmatch(res.Projection)
	if m == nil {
		t.Fatalf("no omitted range in:\n%.400s", res.Projection)
	}
	from, _ := strconv.Atoi(m[1])
	sr, err := s.Search(context.Background(), SearchRequest{ReadRequest: ReadRequest{WorkspaceID: "ws", Handle: res.Handle}, FromLine: from, ToLine: from + 1})
	if err != nil {
		t.Fatal(err)
	}
	if sr.FirstLine != 500 || len(sr.Lines) != 2 || sr.Lines[0].Line != from || sr.Lines[0].Text != fmt.Sprintf("file line %d content here", from) {
		t.Fatalf("lines %d-%d recovered as %+v (first line %d)", from, from+1, sr.Lines, sr.FirstLine)
	}
	// a pattern search reports the file's line numbers too
	sr, err = s.Search(context.Background(), SearchRequest{ReadRequest: ReadRequest{WorkspaceID: "ws", Handle: res.Handle}, Query: "file line 3000 "})
	if err != nil || sr.Matches != 1 || sr.Lines[0].Line != 3000 {
		t.Fatalf("pattern search numbering: %v %+v", err, sr.Lines)
	}
}

// A read error of the request body (the request cap, a disconnect) is returned as
// itself, never as a malformed body.
func TestHookBodyReadErrorsAreNotMalformed(t *testing.T) {
	body := `{"tool_name":"Bash","tool_response":{"stdout":"` + strings.Repeat("output line\\n", 5000) + `"}}`
	for _, capAt := range []int64{3, 30, 2000, int64(len(body) - 3)} {
		r := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(strings.NewReader(body)), capAt)
		_, err := DecodeHookBody(FormatClaude, r, io.Discard, nil)
		var mbe *http.MaxBytesError
		if !errors.As(err, &mbe) || errors.Is(err, ErrBadBody) {
			t.Fatalf("cap at %d: %v (want the MaxBytesError itself)", capAt, err)
		}
	}
	boom := errors.New("connection reset")
	_, err := DecodeHookBody(FormatClaude, io.MultiReader(strings.NewReader(body[:100]), errReader{boom}), io.Discard, nil)
	if !errors.Is(err, boom) || errors.Is(err, ErrBadBody) {
		t.Fatalf("transport error: %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// replacingRedactor is a test StreamRedactor: it replaces a secret, holding back a
// partial match across writes until Flush.
type replacingRedactor struct {
	dst     io.Writer
	secret  string
	pending []byte
	flushes int
}

func (r *replacingRedactor) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	keep := len(r.secret) - 1
	if len(r.pending) <= keep {
		return len(p), nil
	}
	out := bytes.ReplaceAll(r.pending[:len(r.pending)-keep], []byte(r.secret), []byte("[REDACTED]"))
	// a secret straddling the cut is completed by the held-back tail
	cut := len(r.pending) - keep
	if i := bytes.LastIndex(r.pending[:len(r.pending)], []byte(r.secret)); i >= 0 && i < cut && i+len(r.secret) > cut {
		cut = i
		out = bytes.ReplaceAll(r.pending[:cut], []byte(r.secret), []byte("[REDACTED]"))
	}
	if _, err := r.dst.Write(out); err != nil {
		return 0, err
	}
	r.pending = append(r.pending[:0], r.pending[cut:]...)
	return len(p), nil
}

func (r *replacingRedactor) Flush() error {
	r.flushes++
	out := bytes.ReplaceAll(r.pending, []byte(r.secret), []byte("[REDACTED]"))
	r.pending = r.pending[:0]
	_, err := r.dst.Write(out)
	return err
}

// Every captured byte passes through the capture's redactor before it reaches the
// spool — stdout, stderr, the status line and raw bodies alike — so a secret is
// neither retained nor searchable.
func TestCaptureRedactorSeesEverySection(t *testing.T) {
	s, _ := testStore(t, nil)
	const secret = "sk-live-0123456789abcdef"
	var reds []*replacingRedactor
	redact := func(w io.Writer) StreamRedactor {
		r := &replacingRedactor{dst: w, secret: secret}
		reds = append(reds, r)
		return r
	}
	stdout := strings.Repeat("deploying with key "+secret+" now\n", 400)
	body := `{"tool_name":"Bash","tool_input":{"command":"./deploy.sh"},"tool_response":{"stdout":` + jsonString(stdout) +
		`,"stderr":` + jsonString("warning: "+secret+"\n") + `,"interrupted":false,"isImage":false}}`
	for _, in := range []ObservationInput{
		{WorkspaceID: "ws", Format: FormatClaude, Body: strings.NewReader(body), Meta: CaptureMeta{Client: "claude"}, Redact: redact},
		{WorkspaceID: "ws", Format: FormatRaw, Body: strings.NewReader(stdout), Meta: CaptureMeta{Client: "pi", Tool: "bash"}, Redact: redact},
	} {
		res, err := s.Observe(context.Background(), nil, in)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := readAll(t, s, "ws", "", res.Handle, nil)
		if strings.Contains(string(got), secret) || strings.Contains(res.Projection, secret) || !strings.Contains(string(got), "[REDACTED]") {
			t.Fatalf("%s: the secret reached the spool or the projection", in.Format)
		}
		sr, err := s.Search(context.Background(), SearchRequest{ReadRequest: ReadRequest{WorkspaceID: "ws", Handle: res.Handle}, Query: "sk-live"})
		if err != nil || sr.Matches != 0 {
			t.Fatalf("%s: the secret is searchable: %v %+v", in.Format, err, sr)
		}
	}
	if len(reds) != 2 || reds[0].flushes < 3 {
		t.Fatalf("redactor not flushed at section boundaries: %d redactors, %d flushes", len(reds), reds[0].flushes)
	}
}
