package workspaceops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"xmustard/api-go/internal/anchor"
	"xmustard/api-go/internal/govstore"
)

// Quoted-code re-anchoring (WS-28B, the remainder of PAR-PROV-03). WS-28 gave path and
// symbol anchors a drift baseline and left quoted code out: a code anchor's lines shift
// with every edit above them, so "path#L4-L5" silently came to mean whatever ended up on
// those lines. This pass re-finds each code anchor's snippet in its file's current
// content through anchor.Reanchor and either moves the anchor to the lines the code now
// holds, or marks the memory at risk when the code is gone. What cannot be decided (no
// served content to search for, an ambiguous match, a file that cannot be read) is
// unknown, never stale, as everywhere else in this file.

// snippetID names a code anchor by the code it placed rather than by the lines it sits
// on. It is a digest, so the stored anchor carries no quoted text, and it survives the
// move a re-anchor performs: the value's line range is what such a move rewrites. The
// digest is over the block with each line trimmed and blank lines dropped, so that the
// whitespace a fenced block picks up in prose does not change the anchor's identity.
func snippetID(code string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(code, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "snippet:" + hex.EncodeToString(sum[:8])
}

// codeAnchorValue is the "path#Lstart-Lend" value quoteAnchors writes for a placed
// block, and ParseCodeAnchorValue reads back.
func codeAnchorValue(path string, start, end int) string {
	return fmt.Sprintf("%s#L%d-L%d", path, start, end)
}

// quotedBlocks are the fenced code blocks of a memory's served content, in the order
// quoteAnchors saw them: at most maxQuotedBlocks, the bound the placement enforces.
func quotedBlocks(content string) []string {
	var out []string
	fencedCode.ReplaceAllStringFunc(anchorText(content), func(block string) string {
		if m := fencedCode.FindStringSubmatch(block); len(out) < maxQuotedBlocks && strings.TrimSpace(m[1]) != "" {
			out = append(out, m[1])
		}
		return block
	})
	return out
}

// codeSites pairs each code anchor of an entry with the snippet it placed, in the order
// quoteAnchors placed them (path anchors and other kinds take the lower ordinals, and
// the placed code anchors keep their blocks' order among themselves). An entry whose
// anchors and blocks do not correspond one for one is unknown rather than mis-paired:
// pointing a memory's quote at another function's body is the one outcome this must
// not produce, so the surplus on either side is dropped.
func codeSites(content string, anchors []govstore.Anchor) []codeAnchorSite {
	blocks := quotedBlocks(content)
	var out []codeAnchorSite
	for _, a := range anchors {
		if a.Kind != govstore.AnchorCode || a.StaleSince != "" || a.SymbolUID == "" {
			continue
		}
		if a.SymbolUID != snippetID(blocks[len(out)]) {
			return nil
		}
		snippet, err := anchor.NewSnippet(blocks[len(out)])
		if err != nil {
			return nil
		}
		out = append(out, codeAnchorSite{anchor: a, snippet: snippet})
	}
	return out
}

// codeAnchorSite is one code anchor and the snippet it placed.
type codeAnchorSite struct {
	anchor  govstore.Anchor
	snippet anchor.Snippet
}

// reanchorOutcome is what one code anchor's re-anchor found.
type reanchorOutcome int

const (
	// reanchorKept: the code is where the anchor says, or the answer is unknown.
	reanchorKept reanchorOutcome = iota
	// reanchorMoved: the code moved and the anchor follows it.
	reanchorMoved
	// reanchorGone: the quoted code is no longer in the file; the memory is at risk.
	reanchorGone
)

// codeMoveEntry is one entry's moved anchors and lost ones, held until the write.
type codeMoveEntry struct {
	entry string
	moves []govstore.CodeMove
	gone  []govstore.AnchorRef
}

// reanchorCode moves the quoted-code anchors of every entry whose quote lies in a changed
// path, marks the entries whose quoted code is no longer in its file, and returns how
// many anchors it moved plus the entries it marked.
func reanchorCode(ctx context.Context, session *rehashSession, anchors []govstore.Anchor) (int, map[string]bool) {
	marked := map[string]bool{}
	byEntry := map[string][]govstore.Anchor{}
	for _, a := range anchors {
		if a.Kind == govstore.AnchorCode && a.StaleSince == "" {
			byEntry[a.EntryID] = append(byEntry[a.EntryID], a)
		}
	}
	if len(byEntry) == 0 {
		return 0, marked
	}
	ids := slices.Sorted(maps.Keys(byEntry))
	contents, err := entryContents(ctx, session.dataDir, session.workspaceID, ids)
	if err != nil {
		return 0, marked
	}
	var done []codeMoveEntry
	moved := 0
	for _, id := range ids {
		content, ok := contents[id]
		if !ok {
			continue
		}
		var entry codeMoveEntry
		entry.entry = id
		for _, site := range codeSites(content, byEntry[id]) {
			outcome, to := session.reanchorCode(site)
			switch outcome {
			case reanchorMoved:
				entry.moves = append(entry.moves, govstore.CodeMove{SnippetID: site.anchor.SymbolUID,
					From: site.anchor.Value, To: to})
				moved++
			case reanchorGone:
				entry.gone = append(entry.gone, govstore.AnchorRef{Kind: govstore.AnchorCode, Value: site.anchor.Value})
				marked[id] = true
			}
		}
		if len(entry.moves) > 0 || len(entry.gone) > 0 {
			done = append(done, entry)
		}
	}
	applied := writeCodeMoves(ctx, session, done)
	return applied, marked
}

// reanchorCode re-finds one code anchor's snippet in its file's current content. An
// anchor with no snippet digest or an unparsable value, a file that cannot be read, an
// ambiguous match, and a block still sitting where it did all answer reanchorKept:
// unknown is not stale, and a match at the anchor's own lines is nothing to do.
func (s *rehashSession) reanchorCode(site codeAnchorSite) (reanchorOutcome, string) {
	from, err := govstore.ParseCodeAnchorValue(site.anchor.Value)
	if err != nil {
		return reanchorKept, ""
	}
	content := s.text(from.Path)
	if !content.ok {
		return reanchorKept, ""
	}
	a := anchor.Reanchor(anchor.Anchor{Path: from.Path, StartLine: from.Start, EndLine: from.End}, content.text, site.snippet)
	switch {
	case a.Status == anchor.Unanchored && a.Reason == anchor.ReasonNotFound:
		return reanchorGone, ""
	case a.Status == anchor.Unanchored, a.StartLine == from.Start && a.EndLine == from.End:
		return reanchorKept, ""
	}
	return reanchorMoved, codeAnchorValue(from.Path, a.StartLine, a.EndLine)
}

// writeCodeMoves records each entry's moves and marks its losses, in one transaction.
// A move is applied only if the anchor still holds the value the move names, so a move
// computed from a stale read moves nothing rather than overwriting another block's
// lines; ReanchorCode applies the store's own verdict and returns what it moved, so the
// report never counts a move the store refused. A failure to write is not fatal to the
// invalidation that hosts it: the entry it concerns keeps the anchor state it had, and
// the next pass reads the file again.
func writeCodeMoves(ctx context.Context, session *rehashSession, done []codeMoveEntry) int {
	if len(done) == 0 {
		return 0
	}
	actor := invalidationActor(session.root)
	applied := 0
	_ = memoryUpdate(ctx, session.dataDir, session.workspaceID, func(tx govstore.Tx) error {
		applied = 0
		for _, e := range done {
			if len(e.moves) > 0 {
				n, err := tx.ReanchorCode(ctx, e.entry, e.moves, actor.HeadSHA, actor)
				if err != nil {
					return err
				}
				applied += n
			}
			if len(e.gone) > 0 {
				if _, err := tx.MarkStale(ctx, e.entry, e.gone, actor.HeadSHA, actor); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return applied
}

// entryContents reads the served content of ids, without the ids whose content was
// withheld: a memory with no content to search in has no snippet, and a snippet that
// cannot be re-normalized is as unknown as a missing one.
func entryContents(ctx context.Context, dataDir, workspaceID string, ids []string) (map[string]string, error) {
	var read map[string]govstore.EntryContent
	if err := memoryView(ctx, dataDir, workspaceID, func(r govstore.Reader) (err error) {
		read, err = r.EntryContents(ctx, ids)
		return err
	}); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(read))
	for id, c := range read {
		if c.Withheld == "" {
			out[id] = c.Content
		}
	}
	return out, nil
}
