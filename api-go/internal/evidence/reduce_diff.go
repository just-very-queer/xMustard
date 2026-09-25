package evidence

import (
	"bytes"
	"context"
	"fmt"
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

type diffFile struct {
	path       string
	order      int
	adds, dels int
	hunks      int
	listed     bool // shown with hunks or as a header line
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
	files := map[string]*diffFile{}
	var order []*diffFile
	untracked, totalAdds, totalDels, totalHunks, commits := 0, 0, 0, 0, 0
	// pass 1: exact counts, with a git "diff --git" file keyed once even though
	// its "+++" line also names it
	for _, sec := range secs {
		ds := &diffScanner{}
		var cur *diffFile
		err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
			k, path := ds.kind(line)
			switch k {
			case dkCommit:
				commits++
			case dkFile:
				key := ds.fileKey(path)
				cur = files[string(key)]
				if cur == nil {
					if len(files) >= maxTrackedFiles {
						untracked++
						return nil
					}
					cur = &diffFile{path: string(validUTF8(path)), order: len(order)}
					files[string(key)] = cur
					order = append(order, cur)
				}
			case dkHunk:
				totalHunks++
				if cur != nil {
					cur.hunks++
				}
			case dkAdd:
				totalAdds++
				if cur != nil {
					cur.adds++
				}
			case dkDel:
				totalDels++
				if cur != nil {
					cur.dels++
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	nFiles := len(files) + untracked
	if nFiles == 0 && totalHunks == 0 {
		return reduceLineSections(ctx, in, gitRules)
	}
	// files shown with hunks share three quarters of the budget; further files (up
	// to maxDiffFiles) get their header line only, then a "largest omitted" summary
	// with commits (git log -p, git show), commit headers and messages share a fifth
	detailBudget, commitBudget := in.Target*3/4, 0
	if commits > 0 {
		detailBudget, commitBudget = in.Target*3/5, in.Target/5
	}
	detail := min(nFiles, maxDiffFiles, max(1, detailBudget/minFileShare))
	// what a shown file leaves of its share flows to the files after it
	detailLeft, filesLeft := detailBudget, detail
	share := minFileShare
	headerOnlyBudget := in.Target / 5
	commitUsed, commitsOmitted := 0, 0
	header := fmt.Sprintf("[xmustard diff] xm-diff/1 files=%d +%d -%d hunks=%d", nFiles, totalAdds, totalDels, totalHunks)
	if commits > 0 {
		header = fmt.Sprintf("[xmustard diff] xm-diff/1 commits=%d file_changes=%d +%d -%d hunks=%d", commits, nFiles, totalAdds, totalDels, totalHunks)
	}
	var out bytes.Buffer
	out.WriteString(header)
	out.WriteByte('\n')
	parts := map[string]string{}
	var oms []Omission
	fileIdx, listed := 0, 0
	for _, sec := range secs {
		var part bytes.Buffer
		ds := &diffScanner{}
		var cur *diffFile
		fileUsed, fileShown := 0, false
		hunkCut, omitHunks, omitAdds, omitDels := false, 0, 0, 0
		cutAdds, cutDels := 0, 0
		gapStart, gapLines := int64(-1), 0
		closeHunkCut := func() {
			if hunkCut && (cutAdds > 0 || cutDels > 0) {
				fmt.Fprintf(&part, "[xmustard: rest of hunk omitted (+%d -%d)]\n", cutAdds, cutDels)
			}
			hunkCut, cutAdds, cutDels = false, 0, 0
		}
		closeFile := func() {
			closeHunkCut()
			if cur != nil && fileShown && omitHunks > 0 {
				fmt.Fprintf(&part, "[xmustard: %d more hunks omitted in %s (+%d -%d)]\n", omitHunks, cur.path, omitAdds, omitDels)
			}
			if fileShown {
				detailLeft -= fileUsed
				filesLeft--
				fileShown = false
			}
			omitHunks, omitAdds, omitDels = 0, 0, 0
		}
		emit := func(line []byte, start int64) {
			if gapStart >= 0 {
				oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: start, Items: gapLines})
				gapStart, gapLines = -1, 0
			}
			part.Write(validUTF8(trimRune(line[:min(len(line), plainDisplay)])))
			if len(line) > plainDisplay {
				fmt.Fprintf(&part, "…[xmustard: %d bytes omitted]", len(line)-plainDisplay)
			}
			part.WriteByte('\n')
		}
		skip := func(start int64) {
			if gapStart < 0 {
				gapStart = start
			}
			gapLines++
		}
		inOmittedHunk := false
		// a global guard over the per-file shares: the projection never passes the
		// target (256 bytes stay for the closing summaries)
		fits := func(n int) bool { return out.Len()+part.Len()+n+96 <= in.Target-256 }
		err := scanLines(ctx, in.R, sec, func(idx int, line []byte, start, end int64) error {
			k, path := ds.kind(line)
			cost := min(len(line), plainDisplay) + 1
			switch k {
			case dkCommit, dkCommitMeta, dkCommitMsg:
				if k == dkCommit {
					closeFile()
				}
				switch {
				case k == dkCommitMsg && ds.commitMsg > maxCommitMsgLines:
					skip(start)
				case commitUsed+cost > commitBudget || !fits(cost):
					if k == dkCommit {
						commitsOmitted++
					}
					skip(start)
				default:
					commitUsed += cost
					emit(line, start)
				}
			case dkFile:
				closeFile()
				cur = files[string(ds.fileKey(path))]
				fileUsed, inOmittedHunk = 0, false
				fileShown = fileIdx < detail && cur != nil && detailLeft > 0 && fits(len(path)+48+minFileShare/2)
				if fileShown {
					share = min(max(minFileShare, detailLeft/max(filesLeft, 1)), max(detailLeft, 0)+minFileShare/2)
				}
				headerOnly := !fileShown && cur != nil && fileIdx < maxDiffFiles && headerOnlyBudget > 0 && fits(len(path)+48)
				fileIdx++
				if headerOnly || fileShown {
					cur.listed = true
				}
				if headerOnly {
					headerOnlyBudget -= len(cur.path) + 40
					fmt.Fprintf(&part, "=== %s (+%d -%d, %d hunks; not shown)\n", cur.path, cur.adds, cur.dels, cur.hunks)
					listed++
				}
				if fileShown {
					listed++
					if gapStart >= 0 {
						oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: start, Items: gapLines})
						gapStart, gapLines = -1, 0
					}
					fmt.Fprintf(&part, "=== %s (+%d -%d, %d hunks)\n", cur.path, cur.adds, cur.dels, cur.hunks)
				} else {
					skip(start)
				}
			case dkFileMeta:
				if fileShown && fits(cost) && (bytes.HasPrefix(line, []byte("rename ")) || bytes.HasPrefix(line, []byte("new file")) ||
					bytes.HasPrefix(line, []byte("deleted file")) || bytes.HasPrefix(line, []byte("Binary files"))) {
					emit(line, start)
				} else {
					skip(start)
				}
			case dkHunk:
				closeHunkCut()
				if fileShown && fileUsed+cost+256 <= share && fits(cost) {
					inOmittedHunk = false
					fileUsed += cost
					emit(line, start)
				} else {
					inOmittedHunk = true
					omitHunks++
					skip(start)
				}
			case dkAdd, dkDel, dkCtx:
				switch {
				case !fileShown || inOmittedHunk:
					if k == dkAdd {
						omitAdds++
					} else if k == dkDel {
						omitDels++
					}
					skip(start)
				case hunkCut || fileUsed+cost > share || !fits(cost):
					hunkCut = true
					if k == dkAdd {
						cutAdds++
					} else if k == dkDel {
						cutDels++
					}
					skip(start)
				default:
					fileUsed += cost
					emit(line, start)
				}
			default:
				skip(start)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		closeFile()
		if gapStart >= 0 {
			oms = append(oms, Omission{Kind: "lines", Start: gapStart, End: sec.End, Items: gapLines})
		}
		parts[sec.Name] = part.String()
		if len(parts) == 1 {
			parts[sec.Name] = header + "\n" + part.String()
		}
		if len(secs) > 1 {
			fmt.Fprintf(&out, "[%s]\n", sec.Name)
		}
		out.Write(part.Bytes())
	}
	if commitsOmitted > 0 {
		fmt.Fprintf(&out, "[xmustard: %d of %d commit headers not shown]\n", commitsOmitted, commits)
	}
	if nFiles > listed {
		var rest []*diffFile
		for _, f := range order {
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
		writeWithin(&out, in.Target, fmt.Sprintf("[xmustard: %d more files not listed; largest:", nFiles-listed), items, "]\n")
	}
	dp := &DiffProjection{Kind: "diff", Files: nFiles, Additions: totalAdds, Deletions: totalDels, Hunks: totalHunks, Commits: commits}
	for _, f := range order[:min(len(order), maxDiffFiles)] {
		dp.FileStats = append(dp.FileStats, DiffFileStat{Path: f.path, Additions: f.adds, Deletions: f.dels, Hunks: f.hunks})
	}
	for _, s := range in.Sections {
		if _, ok := parts[s.Name]; !ok {
			parts[s.Name] = ""
		}
	}
	facts := Facts{ExitCode: in.Sel.ExitCode, Files: nFiles, Additions: totalAdds, Deletions: totalDels}
	if facts.ExitCode != nil {
		facts.ExitFrom = "tool"
	}
	if len(secs) == 1 {
		parts[secs[0].Name] = out.String() // includes the "more files" summary
	}
	return &Projection{Text: out.String(), Parts: parts, Structured: dp, Facts: facts,
		Record: Record{Reducer: "xm-diff/1", Mode: "text", Reduced: true, Omissions: capOmissions(oms)}}, nil
}
