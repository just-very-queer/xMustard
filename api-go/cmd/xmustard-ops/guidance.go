package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"xmustard/api-go/internal/workspaceops"
)

// The guidance block writer (WS-31, PAR-HAR-03): `xmustard-ops guidance write` puts the
// workspace's human-approved core memory into AGENTS.md or CLAUDE.md as one block
// between two marker lines, stamped with the memory revision and a digest of its body.
// Everything outside the markers is the human's prose and is never changed. A marker
// counts only as a whole line starting at column 0 outside a fenced code block, so a
// marker quoted inline, indented or inside a code fence is text. A second write with
// the same memory changes nothing (idempotent); a block whose markers do not pair is an
// error left for the human to fix. The format is plain Markdown with HTML-comment
// markers, so it can be shared as a rule file (WS-62 exports path-scoped ones).

// guidanceFiles are the files the writer may edit: a closed set.
var guidanceFiles = []string{"AGENTS.md", "CLAUDE.md"}

const (
	guidanceBeginPrefix = "<!-- xmustard:core-memory:begin"
	guidanceEnd         = "<!-- xmustard:core-memory:end -->"
	guidanceFormat      = "v1"
	// maxGuidanceFileBytes bounds the file the writer reads and rewrites.
	maxGuidanceFileBytes = 1 << 20
)

// guidanceActions names what a write did.
const (
	guidanceCreated   = "created"   // the file did not exist
	guidanceAppended  = "appended"  // the file had no block
	guidanceReplaced  = "replaced"  // the block's body changed
	guidanceUnchanged = "unchanged" // the block already says this
)

var guidanceCommands = map[string]func(opsEnv, string, []string) int{
	"write": runGuidanceWrite,
}

func runGuidance(e opsEnv, args []string) int { return e.dispatch("guidance", guidanceCommands, args) }

// guidanceResult is what `guidance write` prints.
type guidanceResult struct {
	WorkspaceID string `json:"workspace_id"`
	File        string `json:"file"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
	Entries     int    `json:"entries"`
	Withheld    int    `json:"withheld"`
	Action      string `json:"action"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Block       string `json:"block,omitempty"`
}

// runGuidanceWrite is `guidance write <workspace_id> [--file AGENTS.md|CLAUDE.md]
// [--dry-run]`. It reads the store and writes only the repository file; it needs no
// token, because it exports only what the core surface already admits.
func runGuidanceWrite(e opsEnv, workspaceID string, args []string) int {
	usage := "usage: xmustard-ops guidance write <workspace_id> [--file " + strings.Join(guidanceFiles, "|") + "] [--dry-run] [--data-dir DIR]"
	fs, dataDir := e.dataDirFlagSet("guidance write")
	file := fs.String("file", guidanceFiles[0], "the guidance file under the workspace root")
	dryRun := fs.Bool("dry-run", false, "print the block and the action without writing")
	if fs.Parse(args) != nil || fs.NArg() > 0 || !slices.Contains(guidanceFiles, *file) {
		return e.usage(usage)
	}
	ws, err := workspaceops.LookupWorkspace(*dataDir, workspaceID)
	if err != nil {
		return e.fail(err)
	}
	p, err := workspaceops.CoreProjection(context.Background(), *dataDir, workspaceID, workspaceops.ProjectionMaxChars)
	if err != nil {
		return e.fail(err)
	}
	body := guidanceBody(workspaceID, p)
	block := guidanceBlock(p.Revision, body)
	action, err := writeGuidanceFile(ws.Root, *file, body, block, *dryRun)
	if err != nil {
		return e.fail(err)
	}
	res := guidanceResult{WorkspaceID: workspaceID, File: *file, Revision: p.Revision, Digest: bodyDigest(body),
		Entries: len(p.Core), Withheld: len(p.Withheld), Action: action, DryRun: *dryRun}
	if *dryRun {
		res.Block = block
	}
	return e.emit(res)
}

// guidanceBody is the block's Markdown between the markers: the core entries the
// projection pushes, each with its id and the revision its approval is on.
func guidanceBody(workspaceID string, p *workspaceops.MemoryProjection) string {
	var b strings.Builder
	b.WriteString("## Standing conventions (xMustard core memory)\n\n")
	fmt.Fprintf(&b, "Human-approved core memory of workspace %s, written by `xmustard-ops guidance write`. "+
		"Change the memory, not this block: the next write replaces it. ", workspaceID)
	fmt.Fprintf(&b, "The xmustard recall tool fetches the %d deferred entries.\n", p.Deferred.Entries)
	if len(p.Core) == 0 {
		b.WriteString("\nNo human-approved core memory yet: a human approver approves a served entry (`xmustard-ops approve`) " +
			"and moves it to the core tier (`xmustard-ops memory tier`).\n")
	}
	for _, m := range p.Core {
		fmt.Fprintf(&b, "\n### %s\n\n<!-- %s revision %d -->\n%s\n", strings.Join(strings.Fields(m.Title), " "), m.ID, m.Revision,
			guidanceText(m.Content))
	}
	if n := len(p.Withheld); n > 0 {
		fmt.Fprintf(&b, "\n%d core entries not exported (not approved, quarantined, flagged or over the budget); "+
			"`xmustard-ops memory tokens` lists them.\n", n)
	}
	return b.String()
}

// guidanceText keeps memory text from breaking the block: a line that could read as a
// marker is indented (markers count only at column 0), and a code fence the text opens
// is closed, so the end marker is never swallowed by it.
func guidanceText(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var fence fenceState
	for i, line := range lines {
		if strings.HasPrefix(line, "<!-- xmustard:") {
			lines[i] = " " + line
		}
		fence.observe(line)
	}
	if fence.open != "" {
		lines = append(lines, fence.open)
	}
	return strings.Join(lines, "\n")
}

// guidanceBlock is the body between the begin marker, stamped with the format, the
// memory revision and the body's digest, and the end marker.
func guidanceBlock(revision int64, body string) string {
	return fmt.Sprintf("%s %s revision=%d sha256=%s -->\n%s%s", guidanceBeginPrefix, guidanceFormat, revision,
		bodyDigest(body), body, guidanceEnd)
}

func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])[:16]
}

// fenceState tracks Markdown code fences: open is the fence that opened the current
// block ("" outside one). A fence is ``` or ~~~ (three or more) after at most three
// spaces; it closes on the same character, at least as long.
type fenceState struct{ open string }

func (f *fenceState) observe(line string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return
	}
	for _, c := range []string{"`", "~"} {
		n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
		if n < 3 {
			continue
		}
		run := strings.Repeat(c, n)
		switch {
		case f.open == "":
			f.open = run
		case strings.HasPrefix(run, f.open) && strings.TrimSpace(trimmed[n:]) == "":
			f.open = ""
		}
		return
	}
}

// guidanceSpan finds the block's marker lines: begin and end are line indexes, -1 when
// the document has no block. A marker is a whole line at column 0 (trailing spaces
// allowed) outside a fence. Markers that do not pair as one begin before one end are an
// error.
func guidanceSpan(lines []string) (begin, end int, err error) {
	begin, end = -1, -1
	var fence fenceState
	begins, ends := 0, 0
	for i, line := range lines {
		wasOpen := fence.open != ""
		fence.observe(line)
		if wasOpen || fence.open != "" {
			continue // inside a fence, or the fence line itself
		}
		l := strings.TrimRight(line, " \t\r")
		switch {
		case strings.HasPrefix(l, guidanceBeginPrefix+" ") && strings.HasSuffix(l, "-->"):
			begin, begins = i, begins+1
		case l == guidanceEnd:
			end, ends = i, ends+1
		}
	}
	switch {
	case begins == 0 && ends == 0:
		return -1, -1, nil
	case begins == 1 && ends == 1 && begin < end:
		return begin, end, nil
	}
	return -1, -1, fmt.Errorf("the xMustard block's markers do not pair (%d begin, %d end lines); fix the file by hand", begins, ends)
}

// spliceGuidance puts block into doc: in place of an existing block, or appended after
// the human's prose. It returns the new document and the action; unchanged when the
// existing block's body is already body.
func spliceGuidance(doc, body, block string) (string, string, error) {
	// A CRLF document's lines still carry a trailing "\r" after splitting on "\n"; fold
	// to LF for the span search and the body comparison, then restore CRLF on write so
	// the result stays idempotent and single-line-ending instead of mixing the two.
	crlf := strings.Contains(doc, "\r\n")
	norm := doc
	if crlf {
		norm = strings.ReplaceAll(doc, "\r\n", "\n")
	}
	lines := strings.Split(norm, "\n")
	begin, end, err := guidanceSpan(lines)
	switch {
	case err != nil:
		return "", "", err
	case begin < 0 && strings.TrimSpace(norm) == "":
		return restoreEOL(block+"\n", crlf), guidanceAppended, nil
	case begin < 0:
		return restoreEOL(strings.TrimRight(norm, "\n")+"\n\n"+block+"\n", crlf), guidanceAppended, nil
	}
	if old := strings.Join(lines[begin+1:end], "\n") + "\n"; old == body {
		return doc, guidanceUnchanged, nil
	}
	out := append(slices.Clone(lines[:begin]), strings.Split(block, "\n")...)
	return restoreEOL(strings.Join(append(out, lines[end+1:]...), "\n"), crlf), guidanceReplaced, nil
}

// restoreEOL puts s back into CRLF when the source document used it; s is otherwise
// always LF-only (guidanceSpan and the block text never emit "\r").
func restoreEOL(s string, crlf bool) string {
	if crlf {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

// errGuidanceSymlink: the writer never writes through a symlink (CLAUDE.md is often one
// to AGENTS.md); name the target instead.
var errGuidanceSymlink = errors.New("is a symlink; write the file it points to instead")

// writeGuidanceFile splices the block into name under root and replaces the file
// atomically (a temporary file in the same directory, then a rename), keeping its mode.
// Every access goes through os.Root, so nothing outside root is read or written. A
// symlink, a non-regular file and a file over maxGuidanceFileBytes are refused.
func writeGuidanceFile(root, name, body, block string, dryRun bool) (string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	doc, mode, existed := "", fs.FileMode(0o644), true
	info, err := r.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		existed = false
	case err != nil:
		return "", err
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s %w", name, errGuidanceSymlink)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a regular file", name)
	case info.Size() > maxGuidanceFileBytes:
		return "", fmt.Errorf("%s is larger than %d bytes", name, maxGuidanceFileBytes)
	default:
		raw, err := r.ReadFile(name)
		if err != nil {
			return "", err
		}
		doc, mode = string(raw), info.Mode().Perm()
	}
	out, action, err := spliceGuidance(doc, body, block)
	if err != nil {
		return "", err
	}
	if !existed {
		action = guidanceCreated
	}
	if dryRun || action == guidanceUnchanged {
		return action, nil
	}
	return action, replaceInRoot(r, name, []byte(out), mode)
}

// replaceInRoot writes data to name through a temporary file and a rename.
func replaceInRoot(r *os.Root, name string, data []byte, mode fs.FileMode) error {
	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	tmp := "." + name + ".xmustard-" + hex.EncodeToString(nonce[:]) + "-" + strconv.Itoa(os.Getpid()) + ".tmp"
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = r.Rename(tmp, name)
	}
	if werr != nil {
		_ = r.Remove(tmp)
	}
	return werr
}
