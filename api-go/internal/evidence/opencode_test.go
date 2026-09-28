package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The capture bodies in testdata/opencode/2.0.1 are what the OpenCode plugin
// (integrations/opencode) posts for execute.after hook inputs that
// integrations/opencode/test/fixtures/record.mjs derives from the installed OpenCode
// 2.0.1 by running the bundle's own tool-result code (grep and read formatters, the
// shell result builder, the MCP tool wrapper). The plugin's unit test checks that the
// plugin still turns those hook inputs into exactly these bodies.

func observeOpenCodeFixture(t *testing.T, s *Store, name string) *ObservationResult {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "opencode", "2.0.1", name+".body.json"))
	if err != nil {
		t.Fatal(err)
	}
	// the plugin's projectionTarget option lowered to 2 KiB: the fixtures are a few KiB
	res, err := s.Observe(context.Background(), nil, ObservationInput{WorkspaceID: "ws", RepoScope: "/repo", Format: FormatOpenCode,
		Body: bytes.NewReader(body), Meta: CaptureMeta{Client: "opencode", ToolVersion: "opencode/2.0.1"}, Target: 2 << 10})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// payloadContent returns a replacement's text and checks that it ends with the
// recovery line: OpenCode keeps a tool text's head when it truncates it.
func payloadContent(t *testing.T, res *ObservationResult) string {
	t.Helper()
	if res.Shape.Mode != ShapeReplace || res.Shape.Shape != "opencode.content" {
		t.Fatalf("%s: shape %+v", res.Capture.Tool, res.Shape)
	}
	var p struct{ Content string }
	if err := json.Unmarshal(res.Shape.Payload, &p); err != nil || ValidateShape("opencode", res.Capture.Tool, res.Shape.Payload) != nil {
		t.Fatalf("%s: payload %s (%v)", res.Capture.Tool, res.Shape.Payload, err)
	}
	lines := strings.Split(strings.TrimRight(p.Content, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "[xmustard evidence] ") || !strings.Contains(last, res.Handle) {
		t.Fatalf("%s: the recovery line is not last: %q", res.Capture.Tool, last)
	}
	return p.Content
}

func TestOpenCode201FixturesAreShapedForTheModelText(t *testing.T) {
	s := productionStore(t)
	for name, check := range map[string]func(t *testing.T, res *ObservationResult){
		"shell-go-test-fail": func(t *testing.T, res *ObservationResult) {
			text := payloadContent(t, res)
			if res.Family != FamilyTest || !res.Capture.IsError || res.Capture.ExitCode == nil || *res.Capture.ExitCode != 1 || res.Capture.CallID != "call_shell" {
				t.Fatalf("shell: family %s capture %+v", res.Family, res.Capture)
			}
			mustContain(t, text, "--- FAIL: TestParseValue", "exit=1")
		},
		"grep-100-matches": func(t *testing.T, res *ObservationResult) {
			text := payloadContent(t, res)
			if res.Family != FamilyGrep || res.Facts.Matches != 100 || res.Facts.Files != 20 {
				t.Fatalf("grep: family %s facts %+v", res.Family, res.Facts)
			}
			mustContain(t, text, "matches=100 files=20", "/Users/dev/repo/src/pkg00/file_000.go (5)", "  10: foo := callSomething(0)")
		},
		"read-numbered": func(t *testing.T, res *ObservationResult) {
			// OpenCode numbers and pages read itself: observed, never replaced
			if res.Shape.Mode != ShapeObserve || res.Shape.Shape != "opencode.observe" || len(res.Shape.Payload) != 0 {
				t.Fatalf("read: %+v", res.Shape)
			}
		},
		"mcp-github-search": func(t *testing.T, res *ObservationResult) {
			payloadContent(t, res)
			if res.Family != FamilyStructured || !PolicyFor("opencode").ForeignTool(res.Capture.Tool) {
				t.Fatalf("mcp: family %s", res.Family)
			}
		},
		"mcp-error": func(t *testing.T, res *ObservationResult) {
			text := payloadContent(t, res)
			if !res.Capture.IsError || res.Capture.Tool != "ci_fetch_log" {
				t.Fatalf("mcp error: %+v", res.Capture)
			}
			mustContain(t, text, "error TS2322")
		},
	} {
		t.Run(name, func(t *testing.T) { check(t, observeOpenCodeFixture(t, s, name)) })
	}
}

// Metadata never becomes model text: a 1.x bash metadata.output (a copy of the output)
// and any other metadata string are skipped; its numbers still report the exit code.
func TestOpenCodeMetadataIsNeverCaptured(t *testing.T) {
	s := productionStore(t)
	out := string(goTestRun(3000))
	body := `{"tool":"bash","sessionID":"ses_1","id":"call_1","input":{"command":"go test ./..."},"content":` + jsonString(out) +
		`,"metadata":{"output":` + jsonString("METADATA COPY "+out[:4000]) + `,"description":"tests","exit":2,"truncated":true,"nested":{"output":"x"}}}`
	hb, err := DecodeHookBody(FormatOpenCode, strings.NewReader(body), io.Discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range hb.Sections {
		if strings.HasPrefix(sec.Name, "metadata") {
			t.Fatalf("metadata became a section: %+v", hb.Sections)
		}
	}
	if hb.ExitCode == nil || *hb.ExitCode != 2 || !slices.Equal(hb.Status, []string{"metadata.truncated=true"}) {
		t.Fatalf("exit %v status %v", hb.ExitCode, hb.Status)
	}
	res := observe(t, s, FormatOpenCode, body, CaptureMeta{Client: "opencode"})
	// the original holds the output and the status line, nothing of the metadata text
	if !res.Capture.IsError || res.RawBytes >= int64(len(out))+100 {
		t.Fatalf("is_error %v, raw %d bytes for a %d-byte output", res.Capture.IsError, res.RawBytes, len(out))
	}
	mustNotContain(t, payloadContent(t, res), "METADATA COPY")
}

// OpenCode's grep text (1.18.33 tool/grep.ts, 2.0.1 grep formatter): "Found N
// matches", then per file an absolute "path:" heading and "  Line N: text" rows, and a
// parenthesized truncation note. Every row is a match of its heading's file.
func TestOpenCodeGrepFormatIsGroupedByHeading(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("Found 100 matches (more matches available)\n")
	for f := 0; f < 20; f++ {
		if f > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "/repo/src/pkg%02d/file_%03d.go:\n", f%7, f)
		for m := 0; m < 5; m++ {
			fmt.Fprintf(&b, "  Line %d: \tfoo := call(%d) // needle: a:1:b\n", 10+m*3, m)
		}
	}
	b.WriteString("\n(Results truncated. Consider using a more specific path or pattern.)\n")
	p, rec := reduceWith(t, b.Bytes(), Selector{Client: "opencode", Tool: "grep"}, 2<<10)
	gp := p.Structured.(*GroupedProjection)
	if rec.Reducer != "xm-grep/1" || gp.Matches != 100 || gp.Files != 20 {
		t.Fatalf("opencode grep: %s matches %d files %d", rec.Reducer, gp.Matches, gp.Files)
	}
	mustContain(t, p.Text, "matches=100 files=20", "/repo/src/pkg00/file_000.go (5)", "  10: foo := call(0) // needle: a:1:b")
	// a Windows heading, and lines that only look like one stay what they were
	for line, want := range map[string]itemKind{
		`C:\repo\src\a.go:`:           ikHeading,
		"/repo/a.go:":                 ikHeading,
		"src/a.go:":                   ikMatch, // relative: grep -r "path:text" with empty text
		"Error in /repo/a.go:":        ikFile,
		"https://example.com/x:":      ikFile,
		"/repo/a.go:12:text":          ikMatch,
		"(showing 3 of 9: truncated)": ikSummary,
		"(Results truncated. Consider using a more specific path or pattern.)":                                ikSummary,
		"(Results are truncated: showing first 100 results. Consider using a more specific path or pattern.)": ikSummary,
		"[100 matches limit reached]":  ikSummary,
		"(not a notice)":               ikFile,
		"No matches found":             ikSummary,
		"  Line 7: orphan, no heading": ikFile,
	} {
		if got := parseGrepLine(&groupState{}, []byte(line)).kind; got != want {
			t.Errorf("%q: kind %d, want %d", line, got, want)
		}
	}
}
