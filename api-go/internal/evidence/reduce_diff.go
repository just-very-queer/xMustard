package evidence

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
)

// Diff family (xm-diff/1): git diff/show/log -p and unified diffs. Pass 1 counts
// files, hunks, additions and deletions exactly (a bounded per-file table). Pass 2
// keeps every file header (up to maxDiffFiles) and, per file, its hunks in order
// within an equal share of the budget; a hunk that does not fit is cut with an
// explicit "+a -d omitted" marker. Commit headers and the first lines of each
// commit message are kept.

const (
	maxDiffFiles      = 60
	maxCommitMsgLines = 3
	minFileShare      = 512
)

var (
	diffPlainRe = regexp.MustCompile(`^diff (-\S+ )*(\S+) (\S+)$`)
	hunkRe      = regexp.MustCompile(`^@@+ -\d+(,\d+)? \+\d+(,\d+)? @@+`)
	commitRe    = regexp.MustCompile(`^commit [0-9a-f]{7,64}\b`)
	fileMetaRe  = regexp.MustCompile(`^(index |new file mode|deleted file mode|similarity index|dissimilarity index|rename from|rename to|copy from|copy to|old mode|new mode|Binary files .* differ|GIT binary patch)`)
)

// diffCounts are additions, deletions and hunks: of one file, of the whole diff, or of
// what the projection omitted.
type diffCounts struct{ adds, dels, hunks int }

// add counts one hunk header, addition or deletion (context lines count nothing).
func (c *diffCounts) add(k diffKind) {
	switch k {
	case dkAdd:
		c.adds++
	case dkDel:
		c.dels++
	case dkHunk:
		c.hunks++
	}
}

type diffFile struct {
	path  string
	order int
	diffCounts
	listed bool // shown with hunks or as a header line
}

// DiffProjection is the structured per-kind projection of a diff.
type DiffProjection struct {
	Kind      string         `json:"kind"`
	Files     int            `json:"files"`
	Additions int            `json:"additions"`
	Deletions int            `json:"deletions"`
	Hunks     int            `json:"hunks"`
	Commits   int            `json:"commits,omitempty"`
	FileStats []DiffFileStat `json:"file_stats"`
}

type DiffFileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Hunks     int    `json:"hunks"`
}

type diffReducer struct{}

func (diffReducer) Family() Family { return FamilyDiff }
func (diffReducer) ID() string     { return "xm-diff" }
func (diffReducer) Version() int   { return 1 }

// diffScanner tracks where a line sits in a (possibly multi-commit) diff.
type diffScanner struct {
	commit int // commits seen: file stats are per (commit, path) occurrence
	keyBuf []byte
	inHunk bool
	// counted: the hunk header gave line counts, and oldLeft/newLeft are the lines
	// still owed. While any remain, a '-' or '+' line is a deletion or addition even
	// when it reads "--- x" or "+++ x" (a deleted SQL comment), as git reads it.
	counted          bool
	oldLeft, newLeft int
	gitFile          bool   // the current file began with "diff --git" (its ---/+++ lines are meta)
	pendingOld       []byte // "--- a/x" seen, waiting for "+++ b/x" (reused buffer)
	havePend         bool
	commitMsg        int // message lines seen of the current commit
	inCommit         bool
}

type diffKind uint8

const (
	dkOther diffKind = iota
	dkCommit
	dkCommitMeta
	dkCommitMsg
	dkFile
	dkFileMeta
	dkHunk
	dkAdd
	dkDel
	dkCtx
)

// headerPath returns the path of a "--- a/x" / "+++ b/x" line (tab suffix removed).
// fileKey identifies one file occurrence: the path within the current commit.
func (d *diffScanner) fileKey(path []byte) []byte {
	d.keyBuf = strconv.AppendInt(append(d.keyBuf[:0], path...), int64(d.commit), 10)
	return d.keyBuf
}

func headerPath(line []byte, strip string) []byte {
	p := line[4:]
	if i := bytes.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	return bytes.TrimPrefix(bytes.TrimSpace(p), []byte(strip))
}

// hunkCounts parses the old and new line counts of a two-way "@@ -a,b +c,d @@"
// header (a missing count is 1). Combined diffs (@@@) are not counted.
func hunkCounts(line []byte) (old, new int, ok bool) {
	if !bytes.HasPrefix(line, []byte("@@ -")) {
		return 0, 0, false
	}
	count := func(b []byte) (int, []byte, bool) {
		i := numberAt(b, 0)
		if i == 0 {
			return 0, nil, false
		}
		if i < len(b) && b[i] == ',' {
			j := numberAt(b, i+1)
			if j == i+1 {
				return 0, nil, false
			}
			n, err := strconv.Atoi(string(b[i+1 : j]))
			return n, b[j:], err == nil
		}
		return 1, b[i:], true
	}
	old, rest, ok := count(line[4:])
	if !ok || !bytes.HasPrefix(rest, []byte(" +")) {
		return 0, 0, false
	}
	new, rest, ok = count(rest[2:])
	if !ok || !bytes.HasPrefix(rest, []byte(" @@")) || bytes.HasPrefix(rest, []byte(" @@@")) {
		return 0, 0, false
	}
	return old, new, true
}

// kind classifies one line; the returned path is a view valid until the next call.
func (d *diffScanner) kind(line []byte) (diffKind, []byte) {
	if d.inHunk && d.counted {
		c := byte(0)
		if len(line) > 0 {
			c = line[0]
		}
		switch {
		case c == '-' && d.oldLeft > 0:
			d.oldLeft--
			return dkDel, nil
		case c == '+' && d.newLeft > 0:
			d.newLeft--
			return dkAdd, nil
		case (c == ' ' || c == 0) && d.oldLeft > 0 && d.newLeft > 0:
			d.oldLeft--
			d.newLeft--
			return dkCtx, nil
		case c == '\\':
			return dkCtx, nil // "\ No newline at end of file"
		case d.oldLeft > 0 || d.newLeft > 0:
			// counts not yet met: a line the header did not announce (a mangled or
			// hand-edited diff); read it by its prefix like an uncounted hunk
			d.counted = false
		default:
			d.inHunk, d.counted = false, false // the hunk is complete
		}
	}
	switch {
	case bytes.HasPrefix(line, []byte("commit ")) && commitRe.Match(line):
		d.inHunk, d.inCommit, d.commitMsg, d.gitFile = false, true, 0, false
		d.commit++
		return dkCommit, nil
	case bytes.HasPrefix(line, []byte("diff --git a/")) && bytes.Contains(line, []byte(" b/")):
		d.inHunk, d.inCommit, d.gitFile = false, false, true
		return dkFile, line[bytes.LastIndex(line, []byte(" b/"))+3:]
	case d.inHunk && len(line) > 0 && line[0] == '+':
		return dkAdd, nil
	case d.inHunk && len(line) > 0 && line[0] == '-' && !bytes.HasPrefix(line, []byte("--- ")):
		return dkDel, nil
	case d.inHunk && (len(line) == 0 || line[0] == ' ' || line[0] == '\\'):
		return dkCtx, nil
	case bytes.HasPrefix(line, []byte("@@")) && hunkRe.Match(line):
		d.inHunk = true
		d.oldLeft, d.newLeft, d.counted = hunkCounts(line)
		return dkHunk, nil
	case bytes.HasPrefix(line, []byte("--- ")) && len(bytes.TrimSpace(line)) > 4:
		d.inHunk = false
		d.pendingOld, d.havePend = append(d.pendingOld[:0], headerPath(line, "a/")...), true
		return dkFileMeta, nil
	case bytes.HasPrefix(line, []byte("+++ ")) && len(bytes.TrimSpace(line)) > 4:
		d.inHunk = false
		p := headerPath(line, "b/")
		pending := d.havePend
		d.havePend = false
		if pending && !d.gitFile {
			if bytes.Equal(p, []byte("/dev/null")) {
				p = d.pendingOld
			}
			return dkFile, p // plain unified diff without a "diff --git" line
		}
		return dkFileMeta, nil
	case fileMetaRe.Match(line), bytes.HasPrefix(line, []byte("diff ")) && diffPlainRe.Match(line):
		d.inHunk = false
		return dkFileMeta, nil
	case d.inCommit && (bytes.HasPrefix(line, []byte("Author:")) || bytes.HasPrefix(line, []byte("Date:")) || bytes.HasPrefix(line, []byte("Merge:"))):
		return dkCommitMeta, nil
	case d.inCommit && bytes.HasPrefix(line, []byte("    ")):
		d.commitMsg++
		return dkCommitMsg, nil
	}
	d.inHunk, d.counted = false, false
	return dkOther, nil
}

func (diffReducer) Reduce(ctx context.Context, in *Input) (*Projection, error) {
	secs := nonEmpty(in.Sections)
	t, err := countDiff(ctx, in.R, secs)
	if err != nil {
		return nil, err
	}
	if t.nFiles() == 0 && t.sum.hunks == 0 {
		return reduceLineSections(ctx, in, gitRules)
	}
	r := newDiffRender(in, t)
	for _, sec := range secs {
		if err := r.section(ctx, sec, len(secs) > 1); err != nil {
			return nil, err
		}
	}
	r.summarize()
	return r.projection(secs), nil
}

// diffTotals are pass 1's exact counts, per file occurrence and in total.
type diffTotals struct {
	files     map[string]*diffFile // by fileKey
	order     []*diffFile
	untracked int // files past maxTrackedFiles
	sum       diffCounts
	commits   int
}

func (t *diffTotals) nFiles() int { return len(t.files) + t.untracked }

// countDiff is pass 1: exact counts, with a git "diff --git" file keyed once even
// though its "+++" line also names it.
func countDiff(ctx context.Context, r io.ReaderAt, secs []Section) (*diffTotals, error) {
	t := &diffTotals{files: map[string]*diffFile{}}
	for _, sec := range secs {
		ds := &diffScanner{}
		var cur *diffFile
		err := scanLines(ctx, r, sec, func(idx int, line []byte, start, end int64) error {
			k, path := ds.kind(line)
			switch k {
			case dkCommit:
				t.commits++
			case dkFile:
				cur = t.file(ds.fileKey(path), path)
			case dkHunk, dkAdd, dkDel:
				t.sum.add(k)
				if cur != nil {
					cur.add(k)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}

// file returns the file of one occurrence key, registering it on first sight; nil
// once maxTrackedFiles are tracked (the file is then only counted).
func (t *diffTotals) file(key, path []byte) *diffFile {
	if f := t.files[string(key)]; f != nil {
		return f
	}
	if len(t.files) >= maxTrackedFiles {
		t.untracked++
		return nil
	}
	f := &diffFile{path: string(validUTF8(path)), order: len(t.order)}
	t.files[string(key)] = f
	t.order = append(t.order, f)
	return f
}

// diffRender is pass 2. Files shown with hunks share three quarters of the budget;
// further files (up to maxDiffFiles) get their header line only, then a "largest
// omitted" summary. With commits (git log -p, git show), commit headers and messages
// share a fifth. What a shown file leaves of its share flows to the files after it.
type diffRender struct {
	in     *Input
	t      *diffTotals
	header string
	out    bytes.Buffer
	parts  map[string]string
	oms    []Omission

	commitBudget, commitUsed, commitsOmitted int
	detail, detailLeft, filesLeft, share     int
	headerOnlyBudget                         int
	fileIdx, listed                          int

	// per section
	part          bytes.Buffer
	ds            *diffScanner
	gap           lineGap
	cur           *diffFile
	fileUsed      int
	fileShown     bool
	inOmittedHunk bool
	hunkCut       bool
	omit, cut     diffCounts // of the current file's omitted hunks, and of its cut hunk
}

func newDiffRender(in *Input, t *diffTotals) *diffRender {
	detailBudget, commitBudget := in.Target*3/4, 0
	if t.commits > 0 {
		detailBudget, commitBudget = in.Target*3/5, in.Target/5
	}
	detail := min(t.nFiles(), maxDiffFiles, max(1, detailBudget/minFileShare))
	r := &diffRender{in: in, t: t, parts: map[string]string{}, commitBudget: commitBudget,
		detail: detail, detailLeft: detailBudget, filesLeft: detail, share: minFileShare, headerOnlyBudget: in.Target / 5}
	r.header = fmt.Sprintf("[xmustard diff] xm-diff/1 files=%d +%d -%d hunks=%d", t.nFiles(), t.sum.adds, t.sum.dels, t.sum.hunks)
	if t.commits > 0 {
		r.header = fmt.Sprintf("[xmustard diff] xm-diff/1 commits=%d file_changes=%d +%d -%d hunks=%d", t.commits, t.nFiles(), t.sum.adds, t.sum.dels, t.sum.hunks)
	}
	r.out.WriteString(r.header)
	r.out.WriteByte('\n')
	return r
}

// section renders one section.
func (r *diffRender) section(ctx context.Context, sec Section, labeled bool) error {
	r.part = bytes.Buffer{}
	r.ds, r.gap, r.cur = &diffScanner{}, newLineGap(), nil
	r.fileUsed, r.fileShown, r.inOmittedHunk, r.hunkCut = 0, false, false, false
	r.omit, r.cut = diffCounts{}, diffCounts{}
	err := scanLines(ctx, r.in.R, sec, func(idx int, line []byte, start, end int64) error {
		r.line(line, start)
		return nil
	})
	if err != nil {
		return err
	}
	r.closeFile()
	r.oms = r.gap.flush(r.oms, sec.End)
	r.parts[sec.Name] = r.part.String()
	if len(r.parts) == 1 {
		r.parts[sec.Name] = r.header + "\n" + r.part.String()
	}
	if labeled {
		fmt.Fprintf(&r.out, "[%s]\n", sec.Name)
	}
	r.out.Write(r.part.Bytes())
	return nil
}

// line dispatches one line by its place in the diff.
func (r *diffRender) line(line []byte, start int64) {
	k, path := r.ds.kind(line)
	cost := min(len(line), plainDisplay) + 1
	switch k {
	case dkCommit, dkCommitMeta, dkCommitMsg:
		r.commitLine(k, line, start, cost)
	case dkFile:
		r.fileHeader(path, start)
	case dkFileMeta:
		r.fileMeta(line, start, cost)
	case dkHunk:
		r.hunkHeader(line, start, cost)
	case dkAdd, dkDel, dkCtx:
		r.hunkLine(k, line, start, cost)
	default:
		r.gap.skip(start)
	}
}

// commitLine keeps a commit header and the first lines of its message within the
// commit budget.
func (r *diffRender) commitLine(k diffKind, line []byte, start int64, cost int) {
	if k == dkCommit {
		r.closeFile()
	}
	switch {
	case k == dkCommitMsg && r.ds.commitMsg > maxCommitMsgLines:
		r.gap.skip(start)
	case r.commitUsed+cost > r.commitBudget || !r.fits(cost):
		if k == dkCommit {
			r.commitsOmitted++
		}
		r.gap.skip(start)
	default:
		r.commitUsed += cost
		r.emit(line, start)
	}
}

// fileHeader starts a file: shown with its hunks while the detail budget lasts, else
// listed by a header line while the header-only budget lasts, else omitted.
func (r *diffRender) fileHeader(path []byte, start int64) {
	r.closeFile()
	r.cur = r.t.files[string(r.ds.fileKey(path))]
	r.fileUsed, r.inOmittedHunk = 0, false
	r.fileShown = r.fileIdx < r.detail && r.cur != nil && r.detailLeft > 0 && r.fits(len(path)+48+minFileShare/2)
	if r.fileShown {
		r.share = min(max(minFileShare, r.detailLeft/max(r.filesLeft, 1)), max(r.detailLeft, 0)+minFileShare/2)
	}
	headerOnly := !r.fileShown && r.cur != nil && r.fileIdx < maxDiffFiles && r.headerOnlyBudget > 0 && r.fits(len(path)+48)
	r.fileIdx++
	switch {
	case r.fileShown:
		r.cur.listed = true
		r.listed++
		r.oms = r.gap.flush(r.oms, start)
		fmt.Fprintf(&r.part, "=== %s (+%d -%d, %d hunks)\n", r.cur.path, r.cur.adds, r.cur.dels, r.cur.hunks)
	case headerOnly:
		r.cur.listed = true
		r.listed++
		r.headerOnlyBudget -= len(r.cur.path) + 40
		fmt.Fprintf(&r.part, "=== %s (+%d -%d, %d hunks; not shown)\n", r.cur.path, r.cur.adds, r.cur.dels, r.cur.hunks)
		r.gap.skip(start)
	default:
		r.gap.skip(start)
	}
}

// shownFileMeta are the metadata lines a shown file keeps.
var shownFileMeta = [][]byte{[]byte("rename "), []byte("new file"), []byte("deleted file"), []byte("Binary files")}

func hasAnyPrefix(line []byte, prefixes [][]byte) bool {
	for _, p := range prefixes {
		if bytes.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

func (r *diffRender) fileMeta(line []byte, start int64, cost int) {
	if r.fileShown && r.fits(cost) && hasAnyPrefix(line, shownFileMeta) {
		r.emit(line, start)
		return
	}
	r.gap.skip(start)
}

// hunkHeader starts a hunk: shown while the file's share lasts, else omitted whole.
func (r *diffRender) hunkHeader(line []byte, start int64, cost int) {
	r.closeHunkCut()
	if r.fileShown && r.fileUsed+cost+256 <= r.share && r.fits(cost) {
		r.inOmittedHunk = false
		r.fileUsed += cost
		r.emit(line, start)
		return
	}
	r.inOmittedHunk = true
	r.omit.hunks++
	r.gap.skip(start)
}

// hunkLine keeps a line of a shown hunk until the file's share runs out; the rest of
// that hunk is cut with a "+a -d omitted" marker.
func (r *diffRender) hunkLine(k diffKind, line []byte, start int64, cost int) {
	switch {
	case !r.fileShown || r.inOmittedHunk:
		r.omit.add(k)
		r.gap.skip(start)
	case r.hunkCut || r.fileUsed+cost > r.share || !r.fits(cost):
		r.hunkCut = true
		r.cut.add(k)
		r.gap.skip(start)
	default:
		r.fileUsed += cost
		r.emit(line, start)
	}
}

// fits is a global guard over the per-file shares: the projection never passes the
// target (256 bytes stay for the closing summaries).
func (r *diffRender) fits(n int) bool { return r.out.Len()+r.part.Len()+n+96 <= r.in.Target-256 }

func (r *diffRender) emit(line []byte, start int64) {
	r.oms = r.gap.flush(r.oms, start)
	r.part.Write(validUTF8(trimRune(line[:min(len(line), plainDisplay)])))
	if len(line) > plainDisplay {
		fmt.Fprintf(&r.part, "…[xmustard: %d bytes omitted]", len(line)-plainDisplay)
	}
	r.part.WriteByte('\n')
}

func (r *diffRender) closeHunkCut() {
	if r.hunkCut && (r.cut.adds > 0 || r.cut.dels > 0) {
		fmt.Fprintf(&r.part, "[xmustard: rest of hunk omitted (+%d -%d)]\n", r.cut.adds, r.cut.dels)
	}
	r.hunkCut, r.cut = false, diffCounts{}
}

// closeFile ends the current file: its omitted-hunks marker, and what it used of the
// detail budget.
func (r *diffRender) closeFile() {
	r.closeHunkCut()
	if r.cur != nil && r.fileShown && r.omit.hunks > 0 {
		fmt.Fprintf(&r.part, "[xmustard: %d more hunks omitted in %s (+%d -%d)]\n", r.omit.hunks, r.cur.path, r.omit.adds, r.omit.dels)
	}
	if r.fileShown {
		r.detailLeft -= r.fileUsed
		r.filesLeft--
		r.fileShown = false
	}
	r.omit = diffCounts{}
}

// summarize appends the closing summaries: commit headers not shown, and the largest
// files not listed.
func (r *diffRender) summarize() {
	if r.commitsOmitted > 0 {
		fmt.Fprintf(&r.out, "[xmustard: %d of %d commit headers not shown]\n", r.commitsOmitted, r.t.commits)
	}
	nFiles := r.t.nFiles()
	if nFiles <= r.listed {
		return
	}
	var rest []*diffFile
	for _, f := range r.t.order {
		if !f.listed {
			rest = append(rest, f)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		ci, cj := rest[i].adds+rest[i].dels, rest[j].adds+rest[j].dels
		if ci != cj {
			return ci > cj
		}
		return rest[i].order < rest[j].order
	})
	var items []string
	for _, f := range rest[:min(len(rest), maxTopFiles)] {
		items = append(items, fmt.Sprintf(" %s (+%d -%d)", f.path, f.adds, f.dels))
	}
	writeWithin(&r.out, r.in.Target, fmt.Sprintf("[xmustard: %d more files not listed; largest:", nFiles-r.listed), items, "]\n")
}

func (r *diffRender) projection(secs []Section) *Projection {
	t := r.t
	dp := &DiffProjection{Kind: "diff", Files: t.nFiles(), Additions: t.sum.adds, Deletions: t.sum.dels, Hunks: t.sum.hunks, Commits: t.commits}
	for _, f := range t.order[:min(len(t.order), maxDiffFiles)] {
		dp.FileStats = append(dp.FileStats, DiffFileStat{Path: f.path, Additions: f.adds, Deletions: f.dels, Hunks: f.hunks})
	}
	fillParts(r.parts, r.in.Sections)
	facts := Facts{Files: t.nFiles(), Additions: t.sum.adds, Deletions: t.sum.dels}
	facts.setToolExit(r.in.Sel.ExitCode)
	if len(secs) == 1 {
		r.parts[secs[0].Name] = r.out.String() // includes the "more files" summary
	}
	return &Projection{Text: r.out.String(), Parts: r.parts, Structured: dp, Facts: facts,
		Record: Record{Reducer: "xm-diff/1", Mode: "text", Reduced: true, Omissions: capOmissions(r.oms)}}
}
