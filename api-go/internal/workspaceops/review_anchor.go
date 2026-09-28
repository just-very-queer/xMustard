//go:build review

package workspaceops

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"xmustard/api-go/internal/anchor"
	"xmustard/api-go/internal/review"
)

// Review finding anchoring (WS-65, PAR-REV-04/05), built only with the review build tag,
// which the lean default leaves off. A batch of findings (a findings file or a captured
// evidence original, decoded by the review package) is anchored against the sealed
// change a merge attestation binds: the hardened diff from the merge base of a base ref
// to a head (observeChange, the same bytes merge approval digests), plus the files at
// that head for snippets outside the hunks and for findings filed against a file the
// change does not touch. Nothing is stored; WS-66 owns the findings store and WS-67 the
// verify transport.

// ReviewEvidenceLabel is the label every anchoring carries.
const ReviewEvidenceLabel = "evidence only: anchors and checks are deterministic facts about the quoted code, " +
	"not verdicts; no review result approves a change"

// Anchoring bounds: the diff text held in memory, in bytes and lines, and the head
// content read for the whole-file tier, in bytes per file and bytes and lines per
// anchoring. Lines bound the parsed form (a line of either is 24 bytes, or 48 for a
// context line, which is on both sides of its hunk) and the search, which is linear in
// lines. A diff over its bounds is refused; a head over one is not read, is listed
// under head_reads, and leaves a finding that needs it head_unread. At every bound, 50
// findings found nowhere keep about 50 MiB live and take under a second of CPU
// (anchor.BenchmarkPlaceAtTheBounds). Variables so tests can lower them.
var (
	anchorDiffLimit     = 8 << 20
	anchorDiffLineLimit = 1 << 18
	headFileLimit       = int64(1 << 20)
	headTotalLimit      = int64(16 << 20)
	headLinesLimit      = int64(1 << 19)
	errAnchorTooLarge   = errors.New("the change is too large to anchor findings in")
	errNotAtHead        = errors.New("not at head")
)

// maxSkippedPaths bounds the skipped paths an anchoring lists.
const maxSkippedPaths = 20

// ReviewAnchoring is one batch of findings anchored against a sealed change. The
// change's diff_sha256 is the digest of exactly the diff the anchors count in.
type ReviewAnchoring struct {
	Change     ReviewedChange    `json:"change"`
	Source     review.Source     `json:"source"`
	Files      int               `json:"files"`
	Findings   []review.Anchored `json:"findings"`
	Counts     review.Counts     `json:"counts"`
	Normalized []string          `json:"normalized,omitempty"`
	HeadReads  HeadReads         `json:"head_reads"`
	Label      string            `json:"label"`
}

// HeadReads accounts for the files read at head: read in full, skipped (over a bound,
// binary, not a blob, or a name the batch reader cannot pass) and searched in their
// hunks only, or missing (a path a finding names that head does not hold). A finding a
// skipped head could decide is head_unread, with code_present unknown.
type HeadReads struct {
	Files        int      `json:"files"`
	Bytes        int64    `json:"bytes"`
	Lines        int64    `json:"lines"`
	Skipped      int      `json:"skipped"`
	SkippedPaths []string `json:"skipped_paths,omitempty"`
	Missing      int      `json:"missing"`
}

// AnchorReviewFindings anchors batch against the change from the merge base of baseRef
// to headRef in the workspace's repository.
func AnchorReviewFindings(ctx context.Context, dataDir, workspaceID, baseRef, headRef string, batch *review.Batch) (*ReviewAnchoring, error) {
	if batch == nil {
		return nil, fmt.Errorf("no findings: %w", ErrInvalidInput)
	}
	text := &cappedText{maxBytes: anchorDiffLimit, maxLines: anchorDiffLineLimit}
	c, err := observeChange(ctx, dataDir, workspaceID, baseRef, headRef, text)
	if text.over {
		return nil, fmt.Errorf("%w: its diff is over %d bytes or %d lines, and this command does not take the heavy slot",
			errAnchorTooLarge, anchorDiffLimit, anchorDiffLineLimit)
	}
	if err != nil {
		return nil, err
	}
	blobs := &headBlobs{ctx: ctx, root: c.Repository, head: c.Head, left: headTotalLimit, linesLeft: headLinesLimit}
	defer blobs.close()
	var files []*anchor.File
	for _, s := range review.SplitDiff(text.String()) {
		var head func() (string, bool)
		if s.NewPath != "" {
			p := s.NewPath
			head = func() (string, bool) { return blobs.read(p) }
		}
		files = append(files, anchor.NewFile(s.OldPath, s.NewPath, s.Diff, head))
	}
	set := anchor.NewSet(files)
	set.Outside = blobs.read
	found, counts := review.Anchor(batch.Findings, set)
	if blobs.err != nil { // a head read failed: anchors that needed it would be wrong
		return nil, blobs.err
	}
	return &ReviewAnchoring{Change: c, Source: batch.Source, Files: len(files), Findings: found, Counts: counts,
		Normalized: batch.Normalized, HeadReads: blobs.reads, Label: ReviewEvidenceLabel}, nil
}

// cappedText keeps the diff text and fails the write that would pass maxBytes bytes or
// maxLines lines, which stops git.
type cappedText struct {
	strings.Builder
	maxBytes, maxLines int
	lines              int
	over               bool
}

func (t *cappedText) Write(p []byte) (int, error) {
	t.lines += bytes.Count(p, []byte{'\n'})
	if t.Len()+len(p) > t.maxBytes || t.lines > t.maxLines {
		t.over = true
		return 0, errAnchorTooLarge
	}
	return t.Builder.Write(p)
}

// headBlobs reads files at the sealed head through one `git cat-file --batch`, started
// on the first read and stopped by close.
type headBlobs struct {
	ctx        context.Context
	root, head string
	left       int64 // bytes the head bound has left
	linesLeft  int64 // lines it has left
	cmd        *exec.Cmd
	in         io.WriteCloser
	out        *bufio.Reader
	cancel     context.CancelFunc
	reads      HeadReads
	err        error
}

// read returns the file at head and true, "" and true when head does not hold it, or
// false when it was not read: skipped, or a read failed (err records a failure). It is
// also Set.Outside, so path may be any path a finding names; the batch reader resolves
// it in head's tree and never on disk.
func (h *headBlobs) read(path string) (string, bool) {
	if h.err != nil {
		return "", false
	}
	if strings.ContainsRune(path, '\n') || strings.HasSuffix(path, "\r") {
		return h.skip(path) // the batch protocol is one name per line
	}
	if h.cmd == nil {
		if h.err = h.start(); h.err != nil {
			return "", false
		}
	}
	content, ok, err := h.next(path)
	switch {
	case errors.Is(err, errNotAtHead):
		h.reads.Missing++
		return "", true
	case err != nil:
		h.err = fmt.Errorf("read %s at %s: %w", path, h.head, err)
		return "", false
	case !ok:
		return h.skip(path)
	}
	h.reads.Files++
	h.reads.Bytes += int64(len(content))
	return content, true
}

func (h *headBlobs) skip(path string) (string, bool) {
	h.reads.Skipped++
	if len(h.reads.SkippedPaths) < maxSkippedPaths {
		h.reads.SkippedPaths = append(h.reads.SkippedPaths, path)
	}
	return "", false
}

func (h *headBlobs) start() (err error) {
	ctx, cancel := context.WithTimeout(h.ctx, reviewDiffTimeout)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	cmd := reviewGitCommand(ctx, h.root, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	h.cmd, h.in, h.out, h.cancel = cmd, in, bufio.NewReader(out), cancel
	return nil
}

// next asks for head:path and reads the answer: "<oid> <type> <size>" and the object,
// or "<name> missing". A missing object is errNotAtHead; ok is false for a non-blob, a
// blob over a bound (bytes or lines) and a binary blob. The object's bytes are consumed
// either way.
func (h *headBlobs) next(path string) (content string, ok bool, err error) {
	if _, err := fmt.Fprintf(h.in, "%s:%s\n", h.head, path); err != nil {
		return "", false, err
	}
	header, err := h.out.ReadString('\n')
	if err != nil {
		return "", false, err
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
		return "", false, errNotAtHead
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", false, fmt.Errorf("unexpected cat-file answer %q", header)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return "", false, fmt.Errorf("unexpected cat-file size %q", header)
	}
	if fields[1] != "blob" || size > headFileLimit || size > h.left {
		_, err := io.CopyN(io.Discard, h.out, size+1)
		return "", false, err
	}
	buf := make([]byte, size+1)
	if _, err := io.ReadFull(h.out, buf); err != nil {
		return "", false, err
	}
	body := buf[:size]
	lines := lineCount(body)
	if bytes.IndexByte(body[:min(len(body), 8000)], 0) >= 0 || lines > h.linesLeft { // binary (as git sniffs it), or too many lines
		return "", false, nil
	}
	h.left, h.linesLeft = h.left-size, h.linesLeft-lines
	h.reads.Lines += lines
	return string(body), true, nil
}

// lineCount is the number of lines in b, a last line without a line break included.
func lineCount(b []byte) int64 {
	n := int64(bytes.Count(b, []byte{'\n'}))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// close ends the batch reader. Its exit status does not matter once every read it
// answered was checked, so it is stopped before the wait: after a malformed answer it
// may still be writing one nobody reads, and would otherwise hold the wait until the
// timeout.
func (h *headBlobs) close() {
	if h.cmd == nil {
		return
	}
	_ = h.in.Close()
	h.cancel()
	_ = h.cmd.Wait()
}
