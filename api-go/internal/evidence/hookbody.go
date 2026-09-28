package evidence

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Stream decoding of hook and tool-result bodies (PAR-CTX-01). A client posts the
// body its harness gives a PostToolUse-style hook (Claude Code, Codex, Cursor) or a
// tool_result event (Pi, OpenCode). The decoder reads it once through a small
// buffer: output strings (stdout, stderr, file content, text blocks, filename lists)
// are unescaped straight into the capture spool as named sections, never held in
// memory whole; the tool input is hashed byte-exactly for the args digest; a few
// bounded metadata fields are kept; and a bounded skeleton of the response records
// where each section sat, so a shape adapter can rebuild the client's payload with
// projected text. Memory is O(buffer + skeleton bounds) whatever the body size: at
// most maxHookSections sections are opened (further output strings are appended to
// one overflow section), empty strings open none, section names are capped, and the
// response's status members are kept in one bounded status section at the end of the
// spool, so the retained original and every text projection carry them.

// HookFormat names a body layout.
type HookFormat string

const (
	FormatClaude   HookFormat = "claude"   // Claude Code PostToolUse / PostToolUseFailure
	FormatCodex    HookFormat = "codex"    // Codex PostToolUse (codex-rs hooks/src/schema.rs PostToolUseCommandInput)
	FormatCursor   HookFormat = "cursor"   // Cursor postToolUse / afterShellExecution / afterMCPExecution
	FormatPi       HookFormat = "pi"       // Pi tool_result event
	FormatOpenCode HookFormat = "opencode" // OpenCode tool.execute.after (tool, args, output)
)

// ErrBadBody reports a hook body that is not the declared JSON layout.
var ErrBadBody = errors.New("malformed hook body")

const (
	hookReadBuffer    = 32 << 10
	hookWriteBuffer   = 32 << 10
	maxHookKey        = 1 << 10
	maxHookSmall      = 1 << 10  // a non-output response string above this becomes a section
	maxHookInputValue = 4 << 10  // tool-input scalars kept (command, path, pattern, ...)
	maxHookMetaBytes  = 64 << 10 // all kept metadata and skeleton values together
	maxSkeletonNodes  = 4096
	maxSkeletonDepth  = 16
	// maxHookSections bounds the sections of one body. Every section costs the line
	// reducers a "[name]" label and at least one omission marker (about 100 bytes), so
	// 64 sections fit the smallest client target (16 KiB) with room for their text.
	maxHookSections = 64
	maxSectionName  = 256 // bytes of a section name (a JSON path)
	maxStatusFields = 32
	maxStatusBytes  = 4 << 10
)

// Names of the sections the decoder adds itself. A JSON path never starts with "(".
const (
	OverflowSection = "(overflow)" // output strings past maxHookSections, one per line
	StatusSection   = "(status)"   // the response's status members, one line
)

// Node is one value of the bounded response skeleton.
type Node struct {
	Kind    byte            // 'o' object, 'a' array, 's' string section, 'l' string-array section, 'v' raw scalar, 'x' dropped
	Key     string          // member key inside an object
	Raw     json.RawMessage // 'v': the value's JSON text
	Section int             // 's', 'l': index into HookBody.Sections
	Kids    []*Node
}

// HookBody is what DecodeHookBody extracted from one body.
type HookBody struct {
	Format     HookFormat
	Event      string
	ToolName   string
	SessionID  string
	CallID     string
	Cwd        string
	AgentID    string
	IsError    bool
	ExitCode   *int
	ArgsDigest string            // sha256 of the exact tool-input bytes ("" when absent)
	BodySHA256 string            // sha256 of the whole body
	BodyBytes  int64             // bytes read
	Input      map[string]string // small scalar tool-input fields
	Sections   []Section         // output streams in the spool, in body order
	Response   *Node             // skeleton of the tool response (nil when absent)
	Dropped    int               // binary values not captured (base64 image data)
	Incomplete bool              // the skeleton hit its bounds: the payload cannot be rebuilt
	// Status holds the response's status members outside arrays ("path=json"), in
	// body order; they are also spooled as the StatusSection.
	Status []string
	// StatusDropped reports a status member the status section could not carry (one
	// inside an array, or past its bounds): a payload built from the projection text
	// alone would lose it.
	StatusDropped bool
	// TurnID and TranscriptPath are the Codex turn and session transcript file.
	TurnID, TranscriptPath string
}

// Scalar returns the raw JSON text of a scalar in the response skeleton by dotted
// path (for example "file.startLine"), or "".
func (b *HookBody) Scalar(path string) string {
	n := b.Response
	for _, k := range strings.Split(path, ".") {
		if n == nil || n.Kind != 'o' {
			return ""
		}
		var next *Node
		for _, c := range n.Kids {
			if c.Key == k {
				next = c
				break
			}
		}
		n = next
	}
	if n == nil || n.Kind != 'v' {
		return ""
	}
	return string(n.Raw)
}

// StreamRedactor transforms captured bytes on their way into the spool (the WS-05
// streaming redactor plugs in here). Flush writes out anything it holds back at a
// section boundary.
type StreamRedactor interface {
	io.Writer
	Flush() error
}

// root key roles per format
type hookRole uint8

const (
	roleSkip hookRole = iota
	roleEvent
	roleTool
	roleSession
	roleCall
	roleCwd
	roleAgent
	roleTurn
	roleTranscript
	roleInput
	roleResponse       // the whole tool response
	roleResponseMember // a member of the response object (Pi content/details)
	roleError          // a failure message (Claude PostToolUseFailure "error"): a non-empty string
	roleIsError
	roleCommand // Cursor afterShellExecution command (a tool-input field at the root)
)

var hookRoles = map[HookFormat]map[string]hookRole{
	FormatClaude: {"hook_event_name": roleEvent, "tool_name": roleTool, "session_id": roleSession, "tool_use_id": roleCall,
		"cwd": roleCwd, "agent_id": roleAgent, "tool_input": roleInput, "tool_response": roleResponse, "error": roleError},
	// Codex sends no error member: PostToolUse runs only for a call that succeeded
	// (a failing MCP call never reaches it; a shell call that exits non-zero does)
	FormatCodex: {"hook_event_name": roleEvent, "tool_name": roleTool, "session_id": roleSession, "tool_use_id": roleCall,
		"turn_id": roleTurn, "agent_id": roleAgent, "transcript_path": roleTranscript, "cwd": roleCwd,
		"tool_input": roleInput, "tool_response": roleResponse},
	FormatCursor: {"hook_event_name": roleEvent, "tool_name": roleTool, "conversation_id": roleSession, "tool_use_id": roleCall,
		"tool_input": roleInput, "tool_output": roleResponse, "result_json": roleResponse, "output": roleResponse,
		"command": roleCommand, "error": roleError},
	FormatPi: {"type": roleEvent, "toolName": roleTool, "sessionId": roleSession, "toolCallId": roleCall, "input": roleInput,
		"content": roleResponseMember, "details": roleResponseMember, "isError": roleIsError},
	FormatOpenCode: {"tool": roleTool, "sessionID": roleSession, "callID": roleCall, "args": roleInput, "output": roleResponse},
}

// HookFormats lists the accepted body formats.
func HookFormats() []HookFormat {
	return []HookFormat{FormatClaude, FormatCodex, FormatCursor, FormatPi, FormatOpenCode}
}

// outputKeys are response strings that are tool output: always sections.
var outputKeys = map[string]bool{
	"stdout": true, "stderr": true, "content": true, "output": true, "text": true, "error": true,
	"result": true, "message": true, "body": true, "log": true, "diff": true, "patch": true,
	"tool_output": true, "tool_response": true, "result_json": true, "aggregated_output": true,
	"formatted_output": true, "error_message": true,
}

// listKeys are string arrays stored one element per line.
var listKeys = map[string]bool{"filenames": true, "files": true, "paths": true, "matches": true, "entries": true, "lines": true}

// dropKeys hold binary data (base64 images) that is never captured as text.
var dropKeys = map[string]bool{"data": true, "base64": true, "image": true, "blob": true, "image_url": true, "bytes": true}

// inputKeys are tool-input fields kept for selection and metadata.
var inputKeys = map[string]bool{
	"command": true, "cmd": true, "file_path": true, "filePath": true, "path": true, "target_file": true,
	"notebook_path": true, "directory": true, "dir_path": true, "pattern": true, "glob": true, "query": true,
	"offset": true, "limit": true, "include": true, "output_mode": true, "description": true, "url": true,
	"start_line": true, "end_line": true, "line": true, "workdir": true, "cwd": true,
}

// exitKeys carry a process exit code in a response (OpenCode bash: metadata.exit).
var exitKeys = map[string]bool{"exit_code": true, "exitCode": true, "returnCode": true, "return_code": true, "exit_status": true,
	"exitStatus": true, "exit": true}

// sectionSink writes decoded output into the spool (through an optional redactor)
// and records section boundaries at the spool offset.
type sectionSink struct {
	bw    *bufio.Writer
	red   StreamRedactor
	cw    *countingWriter
	secs  []Section
	names map[string]bool
	// overflow is the index of the overflow section once maxHookSections is reached
	// (-1 before): every later section is appended to it. Strings merged into it are
	// not flushed one by one (a write per tiny string would cost a syscall each); its
	// end is set when the sink finishes.
	overflow int
	dup      bool // two sections share a name: the payload cannot be rebuilt by name
	// written and lastIn describe the bytes given to the sink (before redaction):
	// a merged string is separated from the previous one by a newline.
	written, overflowMark int64
	lastIn                byte
}

type countingWriter struct {
	w    io.Writer
	n    int64
	last byte // the last byte written
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if n > 0 {
		c.last = p[n-1]
	}
	return n, err
}

func newSectionSink(dst io.Writer, redact func(io.Writer) StreamRedactor) *sectionSink {
	cw := &countingWriter{w: dst}
	s := &sectionSink{cw: cw, names: map[string]bool{}, overflow: -1}
	var w io.Writer = cw
	if redact != nil {
		s.red = redact(cw)
		w = s.red
	}
	s.bw = bufio.NewWriterSize(w, hookWriteBuffer)
	return s
}

func (s *sectionSink) flush() error {
	if err := s.bw.Flush(); err != nil {
		return err
	}
	if s.red != nil {
		return s.red.Flush()
	}
	return nil
}

// begin opens the next section. Past maxHookSections it reopens the one overflow
// section instead: sections are contiguous in the spool and the overflow section is
// always the last one opened, so appending to it (after a newline separator) keeps it
// one byte range.
func (s *sectionSink) begin(name string) (int, error) {
	if s.overflow >= 0 {
		if s.written > s.overflowMark && s.lastIn != '\n' {
			if _, err := s.Write(newline); err != nil {
				return 0, err
			}
		}
		s.secs[s.overflow].Merged++
		return s.overflow, nil
	}
	if err := s.flush(); err != nil {
		return 0, err
	}
	if len(s.secs) < maxHookSections {
		if s.names[name] {
			s.dup = true
		}
		s.names[name] = true
		s.secs = append(s.secs, Section{Name: name, Start: s.cw.n})
		return len(s.secs) - 1, nil
	}
	s.secs = append(s.secs, Section{Name: OverflowSection, Start: s.cw.n, Merged: 1})
	s.overflow, s.overflowMark = len(s.secs)-1, s.written
	return s.overflow, nil
}

func (s *sectionSink) end(idx int) error {
	if idx == s.overflow {
		return nil // finish sets its end
	}
	if err := s.flush(); err != nil {
		return err
	}
	s.secs[idx].End = s.cw.n
	return nil
}

// finish flushes everything written and closes the overflow section.
func (s *sectionSink) finish() error {
	if err := s.flush(); err != nil {
		return err
	}
	if s.overflow >= 0 {
		s.secs[s.overflow].End = s.cw.n
	}
	return nil
}

// writeStatus spools the status members as the last section, one line.
func (s *sectionSink) writeStatus(status []string) error {
	if err := s.finish(); err != nil || len(status) == 0 {
		return err
	}
	s.secs = append(s.secs, Section{Name: StatusSection, Start: s.cw.n, Status: true})
	if s.cw.n > 0 && s.cw.last != '\n' {
		_ = s.bw.WriteByte('\n') // the status line never joins the output's last line
	}
	_, _ = s.bw.WriteString("[xmustard status] " + strings.Join(status, " ") + "\n")
	return s.end(len(s.secs) - 1)
}

var newline = []byte{'\n'}

func (s *sectionSink) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s.written += int64(len(p))
		s.lastIn = p[len(p)-1]
	}
	return s.bw.Write(p)
}

// hookDecoder is a streaming JSON reader over the body.
type hookDecoder struct {
	br          *bufio.Reader
	n           int64
	tee         hash.Hash // tool-input digest while active
	one         [1]byte
	sink        *sectionSink
	body        *HookBody
	nodes       int
	metaUsed    int
	statusBytes int
}

// DecodeHookBody reads one body in the given format, writing its output strings to
// dst (the spool) as sections. redact, when set, wraps the spool writer.
func DecodeHookBody(format HookFormat, r io.Reader, dst io.Writer, redact func(io.Writer) StreamRedactor) (*HookBody, error) {
	roles, ok := hookRoles[format]
	if !ok {
		return nil, fmt.Errorf("%w: unknown format %q", ErrBadBody, format)
	}
	bodyHash := sha256.New()
	d := &hookDecoder{br: bufio.NewReaderSize(io.TeeReader(r, bodyHash), hookReadBuffer), sink: newSectionSink(dst, redact),
		body: &HookBody{Format: format, Input: map[string]string{}}}
	err := d.decodeRoot(roles)
	if err == nil {
		err = d.sink.writeStatus(d.body.Status)
	}
	if err == nil {
		err = d.sink.flush()
	}
	if err != nil {
		return nil, err
	}
	// the tee saw everything the buffered reader pulled; drain trailing bytes so the
	// body digest covers the whole body
	if _, err := io.Copy(io.Discard, d.br); err != nil {
		return nil, err
	}
	d.body.BodySHA256, d.body.BodyBytes = hex.EncodeToString(bodyHash.Sum(nil)), d.n
	d.body.Sections = d.sink.secs
	if d.sink.overflow >= 0 || d.sink.dup {
		// merged or same-named sections cannot be put back field by field
		d.body.Incomplete = true
	}
	return d.body, nil
}

// readErr maps a read error: the end of the body is a syntax error, anything else
// (the request cap, a disconnect, a timeout) is returned as is, so the caller can
// tell a malformed body from a transport failure.
func (d *hookDecoder) readErr(err error, what string) error {
	if err == io.EOF {
		return d.syntax(what)
	}
	return err
}

func (d *hookDecoder) syntax(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrBadBody, what, d.n)
}

func (d *hookDecoder) readByte() (byte, error) {
	b, err := d.br.ReadByte()
	if err != nil {
		return 0, d.readErr(err, "unexpected end")
	}
	d.n++
	if d.tee != nil {
		d.one[0] = b
		d.tee.Write(d.one[:])
	}
	return b, nil
}

func (d *hookDecoder) consume(k int) {
	if d.tee != nil {
		p, _ := d.br.Peek(k)
		d.tee.Write(p)
	}
	_, _ = d.br.Discard(k)
	d.n += int64(k)
}

// peek returns the next non-whitespace byte without consuming it.
func (d *hookDecoder) peek() (byte, error) {
	for {
		p, err := d.br.Peek(1)
		if err != nil {
			return 0, d.readErr(err, "unexpected end")
		}
		switch p[0] {
		case ' ', '\t', '\n', '\r':
			d.consume(1)
		default:
			return p[0], nil
		}
	}
}

func (d *hookDecoder) expect(c byte) error {
	b, err := d.peek()
	if err != nil {
		return err
	}
	if b != c {
		return d.syntax(fmt.Sprintf("expected %q, got %q", c, b))
	}
	_, err = d.readByte()
	return err
}

// strDest receives a decoded string: into a small buffer (up to limit), promoted to
// a spool section when it grows past it, or discarded.
type strDest struct {
	d        *hookDecoder
	small    []byte
	limit    int
	name     string // section name on promotion ("" = never promote: overflow is dropped)
	promoted bool
	section  int
	discard  bool
	overflow bool
	n        int64
}

func (s *strDest) write(p []byte) error {
	s.n += int64(len(p))
	switch {
	case s.discard:
		return nil
	case s.promoted:
		_, err := s.d.sink.Write(p)
		return err
	case len(s.small)+len(p) <= s.limit:
		s.small = append(s.small, p...)
		return nil
	case s.name == "":
		s.overflow = true
		return nil
	}
	// promoted on the first byte past the limit (limit 0: on the first byte, so an
	// empty string never opens a section)
	idx, err := s.d.sink.begin(s.name)
	if err != nil {
		return err
	}
	s.promoted, s.section = true, idx
	if _, err := s.d.sink.Write(s.small); err != nil {
		return err
	}
	s.small = nil
	_, err = s.d.sink.Write(p)
	return err
}

var replacementRune = []byte("�")

// readString decodes the JSON string at the cursor into dst.
func (d *hookDecoder) readString(dst *strDest) error {
	if err := d.expect('"'); err != nil {
		return err
	}
	var enc [utf8.UTFMax]byte
	for {
		if _, err := d.br.Peek(1); err != nil {
			return d.readErr(err, "unterminated string")
		}
		buf, _ := d.br.Peek(d.br.Buffered())
		k := 0
		for k < len(buf) {
			c := buf[k]
			if c == '"' || c == '\\' || c < 0x20 || c >= utf8.RuneSelf {
				break
			}
			k++
		}
		if k > 0 {
			if err := dst.write(buf[:k]); err != nil {
				return err
			}
			d.consume(k)
			continue
		}
		switch c := buf[0]; {
		case c == '"':
			d.consume(1)
			return nil
		case c == '\\':
			d.consume(1)
			e, err := d.readByte()
			if err != nil {
				return err
			}
			out := enc[:1]
			switch e {
			case '"', '\\', '/':
				enc[0] = e
			case 'b':
				enc[0] = '\b'
			case 'f':
				enc[0] = '\f'
			case 'n':
				enc[0] = '\n'
			case 'r':
				enc[0] = '\r'
			case 't':
				enc[0] = '\t'
			case 'u':
				r, err := d.readHex4()
				if err != nil {
					return err
				}
				switch {
				case r >= 0xD800 && r < 0xDC00:
					r = d.lowSurrogate(r)
				case utf16.IsSurrogate(r):
					r = utf8.RuneError // a lone low surrogate
				}
				out = enc[:utf8.EncodeRune(enc[:], r)]
			default:
				return d.syntax("invalid escape")
			}
			if err := dst.write(out); err != nil {
				return err
			}
		case c < 0x20:
			return d.syntax("control character in string")
		default:
			p, _ := d.br.Peek(utf8.UTFMax)
			r, size := utf8.DecodeRune(p)
			if r == utf8.RuneError && size <= 1 {
				// invalid UTF-8 is replaced, as encoding/json does
				if err := dst.write(replacementRune); err != nil {
					return err
				}
				d.consume(1)
				continue
			}
			if err := dst.write(p[:size]); err != nil {
				return err
			}
			d.consume(size)
		}
	}
}

func (d *hookDecoder) readHex4() (rune, error) {
	var v rune
	for i := 0; i < 4; i++ {
		b, err := d.readByte()
		if err != nil {
			return 0, err
		}
		switch {
		case b >= '0' && b <= '9':
			v = v<<4 | rune(b-'0')
		case b >= 'a' && b <= 'f':
			v = v<<4 | rune(b-'a'+10)
		case b >= 'A' && b <= 'F':
			v = v<<4 | rune(b-'A'+10)
		default:
			return 0, d.syntax("invalid \\u escape")
		}
	}
	return v, nil
}

// lowSurrogate completes a surrogate pair when the next escape is a low surrogate;
// otherwise the high surrogate alone becomes U+FFFD and the next escape (or
// character) is left to be decoded normally, as encoding/json does.
func (d *hookDecoder) lowSurrogate(hi rune) rune {
	p, _ := d.br.Peek(6)
	if len(p) < 6 || p[0] != '\\' || p[1] != 'u' {
		return utf8.RuneError
	}
	var lo rune
	for _, b := range p[2:6] {
		switch {
		case b >= '0' && b <= '9':
			lo = lo<<4 | rune(b-'0')
		case b >= 'a' && b <= 'f':
			lo = lo<<4 | rune(b-'a'+10)
		case b >= 'A' && b <= 'F':
			lo = lo<<4 | rune(b-'A'+10)
		default:
			return utf8.RuneError
		}
	}
	if lo < 0xDC00 || lo >= 0xE000 {
		return utf8.RuneError
	}
	d.consume(6)
	return utf16.DecodeRune(hi, lo)
}

// readKey reads an object key (bounded).
func (d *hookDecoder) readKey() (string, error) {
	dst := &strDest{d: d, limit: maxHookKey}
	if err := d.readString(dst); err != nil {
		return "", err
	}
	if dst.overflow {
		return "", d.syntax("object key too long")
	}
	return string(dst.small), nil
}

// readScalar reads a number or literal, returning its JSON text.
func (d *hookDecoder) readScalar() ([]byte, error) {
	var out []byte
	for {
		p, err := d.br.Peek(1)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err != nil || !(p[0] >= '0' && p[0] <= '9' || p[0] >= 'a' && p[0] <= 'z' || p[0] == '-' || p[0] == '+' || p[0] == '.' || p[0] == 'E') {
			break
		}
		if len(out) >= 64 {
			return nil, d.syntax("scalar too long")
		}
		out = append(out, p[0])
		d.consume(1)
	}
	if !json.Valid(out) {
		return nil, d.syntax("invalid literal")
	}
	return out, nil
}

// skip consumes one value of any kind without keeping it.
func (d *hookDecoder) skip(depth int) error {
	if depth > maxNesting {
		return d.syntax("nesting too deep")
	}
	c, err := d.peek()
	if err != nil {
		return err
	}
	switch c {
	case '"':
		return d.readString(&strDest{d: d, discard: true})
	case '{':
		return d.eachMember(func(string) error { return d.skip(depth + 1) })
	case '[':
		return d.eachElement(func(int) error { return d.skip(depth + 1) })
	}
	_, err = d.readScalar()
	return err
}

func (d *hookDecoder) eachMember(fn func(key string) error) error {
	if err := d.expect('{'); err != nil {
		return err
	}
	if c, err := d.peek(); err != nil {
		return err
	} else if c == '}' {
		_, err = d.readByte()
		return err
	}
	for {
		key, err := d.readKey()
		if err != nil {
			return err
		}
		if err := d.expect(':'); err != nil {
			return err
		}
		if err := fn(key); err != nil {
			return err
		}
		c, err := d.peek()
		if err != nil {
			return err
		}
		if _, err := d.readByte(); err != nil {
			return err
		}
		switch c {
		case '}':
			return nil
		case ',':
		default:
			return d.syntax("expected , or }")
		}
		if _, err := d.peek(); err != nil {
			return err
		}
	}
}

func (d *hookDecoder) eachElement(fn func(i int) error) error {
	if err := d.expect('['); err != nil {
		return err
	}
	if c, err := d.peek(); err != nil {
		return err
	} else if c == ']' {
		_, err = d.readByte()
		return err
	}
	for i := 0; ; i++ {
		if err := fn(i); err != nil {
			return err
		}
		c, err := d.peek()
		if err != nil {
			return err
		}
		if _, err := d.readByte(); err != nil {
			return err
		}
		switch c {
		case ']':
			return nil
		case ',':
		default:
			return d.syntax("expected , or ]")
		}
	}
}

// meta keeps a small root string (bounded in total).
func (d *hookDecoder) metaString(limit int) (string, error) {
	dst := &strDest{d: d, limit: min(limit, max(0, maxHookMetaBytes-d.metaUsed))}
	if err := d.readString(dst); err != nil {
		return "", err
	}
	d.metaUsed += len(dst.small)
	return string(dst.small), nil
}

func (d *hookDecoder) decodeRoot(roles map[string]hookRole) error {
	c, err := d.peek()
	if err != nil {
		return err
	}
	if c != '{' {
		return d.syntax("body is not a JSON object")
	}
	// the metadata roles, one string field each
	b := d.body
	fields := map[hookRole]*string{roleEvent: &b.Event, roleTool: &b.ToolName, roleSession: &b.SessionID, roleCall: &b.CallID,
		roleCwd: &b.Cwd, roleAgent: &b.AgentID, roleTurn: &b.TurnID, roleTranscript: &b.TranscriptPath}
	err = d.eachMember(func(key string) error {
		role := roles[key]
		c, err := d.peek()
		if err != nil {
			return err
		}
		str := func(dst *string) error {
			if c != '"' {
				return d.skip(1)
			}
			s, err := d.metaString(maxHookInputValue)
			*dst = s
			return err
		}
		if dst := fields[role]; dst != nil {
			return str(dst)
		}
		switch role {
		case roleCommand:
			var s string
			err := str(&s)
			d.body.Input["command"] = s
			return err
		case roleIsError:
			raw, err := d.valueScalar(c)
			d.body.IsError = string(raw) == "true"
			return err
		case roleInput:
			return d.decodeInput(c)
		case roleResponse:
			n, err := d.walk(key, "", 0)
			d.body.Response = n
			return err
		case roleResponseMember:
			if d.body.Response == nil {
				d.body.Response = &Node{Kind: 'o'}
			}
			n, err := d.walk(key, key, 1)
			if n != nil {
				d.body.Response.Kids = append(d.body.Response.Kids, n)
			}
			return err
		case roleError:
			// a failure message (PostToolUseFailure): the output of a failed call. Only
			// a non-empty string is one; "error": null, "" or a non-string says nothing
			if c != '"' {
				return d.skip(1)
			}
			before := d.sink.written
			n, err := d.walk(key, "error", 0)
			d.body.IsError = d.body.IsError || d.sink.written > before // a non-empty string opened a section
			if d.body.Response == nil {
				d.body.Response = n
			}
			return err
		}
		return d.skip(1)
	})
	if err != nil {
		return err
	}
	if _, err := d.br.Peek(1); err == nil {
		if c, _ := d.peek(); c != 0 {
			return d.syntax("trailing content after the body")
		}
	} else if err != io.EOF {
		return err
	}
	return nil
}

func (d *hookDecoder) valueScalar(c byte) ([]byte, error) {
	if c == '"' || c == '{' || c == '[' {
		return nil, d.skip(1)
	}
	return d.readScalar()
}

// decodeInput hashes the tool input byte-exactly and keeps its small scalar fields.
func (d *hookDecoder) decodeInput(c byte) error {
	h := sha256.New()
	d.tee = h
	defer func() {
		d.tee = nil
		d.body.ArgsDigest = hex.EncodeToString(h.Sum(nil))
	}()
	if c != '{' {
		return d.skip(1)
	}
	return d.eachMember(func(key string) error {
		c, err := d.peek()
		if err != nil {
			return err
		}
		if !inputKeys[key] {
			return d.skip(1)
		}
		switch c {
		case '"':
			dst := &strDest{d: d, limit: min(maxHookInputValue, max(0, maxHookMetaBytes-d.metaUsed))}
			if err := d.readString(dst); err != nil {
				return err
			}
			if !dst.overflow {
				d.metaUsed += len(dst.small)
				d.body.Input[key] = string(dst.small)
			}
			return nil
		case '[':
			// an argv array (Codex shell: ["bash","-lc","go test ./..."]) is kept joined
			// by spaces; other element kinds are skipped
			var parts []string
			used := 0
			err := d.eachElement(func(int) error {
				c, err := d.peek()
				if err != nil {
					return err
				}
				if c != '"' {
					return d.skip(2)
				}
				dst := &strDest{d: d, limit: min(maxHookInputValue-used, max(0, maxHookMetaBytes-d.metaUsed))}
				if err := d.readString(dst); err != nil {
					return err
				}
				if !dst.overflow {
					parts = append(parts, string(dst.small))
					used += len(dst.small) + 1
				}
				return nil
			})
			if err == nil && len(parts) > 0 {
				d.body.Input[key] = strings.Join(parts, " ")
				d.metaUsed += used
			}
			return err
		case '{':
			return d.skip(1)
		}
		raw, err := d.readScalar()
		if err == nil {
			d.body.Input[key] = string(raw)
		}
		return err
	})
}

// node admits one skeleton node; its key counts against the metadata bound.
func (d *hookDecoder) node(n *Node) *Node {
	if d.nodes >= maxSkeletonNodes || d.metaUsed+len(n.Key) > maxHookMetaBytes {
		d.body.Incomplete = true
		return nil
	}
	d.nodes++
	d.metaUsed += len(n.Key)
	return n
}

// child is the JSON path of a member (sep ".") or element (sep "["), capped at
// maxSectionName bytes: a capped path may name two sections alike, so the payload
// is marked unrebuildable.
func (d *hookDecoder) child(parent, sep, key string) string {
	if parent == "" && sep == "." {
		sep = ""
	}
	if len(parent)+len(sep)+len(key) <= maxSectionName {
		return parent + sep + key
	}
	d.body.Incomplete = true
	room := max(0, maxSectionName-len(parent)-len(sep))
	if room == 0 {
		return parent
	}
	return parent + sep + string(trimRune([]byte(key[:min(len(key), room)])))
}

// status records a status member of the response (statusKeys, at any depth outside
// arrays) for the status section; a failing status among the response's own
// members marks the call as an error.
func (d *hookDecoder) status(key, path string, raw []byte, depth int) {
	if len(key) > 24 || !isStatusKey([]byte(key)) {
		return
	}
	if strings.Contains(path, "[") || len(d.body.Status) >= maxStatusFields || d.statusBytes+len(path)+len(raw)+2 > maxStatusBytes {
		d.body.StatusDropped = true
		return
	}
	if depth == 1 && failingStatus(key, raw) {
		d.body.IsError = true
	}
	d.body.Status = append(d.body.Status, path+"="+string(raw))
	d.statusBytes += len(path) + len(raw) + 2
}

// failingStatus reports a status member that says the call failed.
func failingStatus(key string, raw []byte) bool {
	switch strings.ToLower(key) {
	case "ok", "success", "succeeded", "passed":
		return string(raw) == "false"
	case "is_error", "iserror", "failed", "timed_out", "timedout":
		return string(raw) == "true"
	case "status", "state", "conclusion", "outcome", "result":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		switch strings.ToLower(s) {
		case "failed", "failure", "fail", "failing", "error", "errored", "timed_out", "timedout", "timeout", "crashed":
			return true
		}
	}
	return false
}

// walk reads one response value at path (key is its member key), streaming output
// strings into sections and building the skeleton.
func (d *hookDecoder) walk(key, path string, depth int) (*Node, error) {
	if depth > maxNesting {
		return nil, d.syntax("nesting too deep")
	}
	c, err := d.peek()
	if err != nil {
		return nil, err
	}
	skeleton := depth <= maxSkeletonDepth
	if !skeleton {
		d.body.Incomplete = true
	}
	switch c {
	case '"':
		dst := &strDest{d: d, limit: maxHookSmall, name: path}
		switch {
		case dropKeys[key]:
			dst.discard = true
		case outputKeys[key] || key == "" || depth == 0:
			if path == "" {
				dst.name = "output" // the whole response is one string
			}
			dst.limit = 0 // a section from the first byte; an empty string opens none
		}
		if err := d.readString(dst); err != nil {
			return nil, err
		}
		switch {
		case dst.discard:
			d.body.Dropped++
			return d.node(&Node{Kind: 'x', Key: key}), nil
		case dst.promoted:
			if err := d.sink.end(dst.section); err != nil {
				return nil, err
			}
			return d.node(&Node{Kind: 's', Key: key, Section: dst.section}), nil
		}
		raw, _ := json.Marshal(string(dst.small))
		d.status(key, path, raw, depth)
		if d.metaUsed+len(raw) > maxHookMetaBytes {
			d.body.Incomplete = true
			return nil, nil
		}
		d.metaUsed += len(raw)
		return d.node(&Node{Kind: 'v', Key: key, Raw: raw}), nil
	case '{':
		n := d.node(&Node{Kind: 'o', Key: key})
		err := d.eachMember(func(k string) error {
			kid, err := d.walk(k, d.child(path, ".", k), depth+1)
			if n != nil && kid != nil && skeleton {
				n.Kids = append(n.Kids, kid)
			} else if kid == nil {
				d.body.Incomplete = true
			}
			return err
		})
		return n, err
	case '[':
		if listKeys[key] {
			return d.walkList(key, path)
		}
		n := d.node(&Node{Kind: 'a', Key: key})
		err := d.eachElement(func(i int) error {
			kid, err := d.walk("", d.child(path, "[", strconv.Itoa(i)+"]"), depth+1)
			if n != nil && kid != nil && skeleton {
				n.Kids = append(n.Kids, kid)
			} else if kid == nil {
				d.body.Incomplete = true
			}
			return err
		})
		return n, err
	}
	raw, err := d.readScalar()
	if err != nil {
		return nil, err
	}
	if exitKeys[key] {
		if v, err := strconv.Atoi(string(raw)); err == nil {
			d.body.ExitCode = &v
		}
	}
	if (key == "is_error" || key == "isError") && string(raw) == "true" {
		d.body.IsError = true
	}
	d.status(key, path, raw, depth)
	d.metaUsed += len(raw)
	return d.node(&Node{Kind: 'v', Key: key, Raw: raw}), nil
}

// walkList stores a string array one element per line in one section, opened at the
// first element (an empty list opens none).
func (d *hookDecoder) walkList(key, path string) (*Node, error) {
	idx := -1
	var dst *strDest
	err := d.eachElement(func(int) error {
		c, err := d.peek()
		if err != nil {
			return err
		}
		if c != '"' {
			// not a string list after all: the payload cannot be rebuilt
			d.body.Incomplete = true
			return d.skip(1)
		}
		if idx < 0 {
			if idx, err = d.sink.begin(path); err != nil {
				return err
			}
			if idx != d.sink.overflow {
				d.sink.secs[idx].Array = true
			}
			dst = &strDest{d: d, promoted: true, section: idx}
		}
		if err := d.readString(dst); err != nil {
			return err
		}
		_, err = d.sink.Write(newline)
		return err
	})
	if err != nil {
		return nil, err
	}
	if idx < 0 {
		d.metaUsed += 2
		return d.node(&Node{Kind: 'v', Key: key, Raw: json.RawMessage("[]")}), nil
	}
	if err := d.sink.end(idx); err != nil {
		return nil, err
	}
	return d.node(&Node{Kind: 'l', Key: key, Section: idx}), nil
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
