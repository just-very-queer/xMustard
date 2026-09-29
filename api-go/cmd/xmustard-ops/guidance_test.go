package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

func testProjection(revision int64, entries ...workspaceops.ProjectedMemory) *workspaceops.MemoryProjection {
	return &workspaceops.MemoryProjection{WorkspaceID: "ws", Revision: revision, Core: entries,
		Deferred: workspaceops.DeferredIndex{Entries: 3}}
}

// The block writer keeps the human's prose, recognizes a marker only as a whole line at
// column 0 outside a code fence (never an inline, indented or fenced mention), rewrites
// its own block in place, and changes nothing when the memory did not change.
func TestGuidanceBlockIsIdempotentWithLineAnchoredMarkers(t *testing.T) {
	prose := "# Project\n\nMention `" + guidanceBeginPrefix + " v1 -->` inline, and quote " + guidanceEnd + " in a sentence.\n" +
		"    " + guidanceEnd + "\n" +
		"```markdown\n" + guidanceBeginPrefix + " v1 revision=1 sha256=00 -->\n" + guidanceEnd + "\n```\n" +
		"Our own conventions stay ours.\n"
	p := testProjection(5, workspaceops.ProjectedMemory{ID: "ctx_a", Revision: 2, Title: "Release  rule", Content: "Tag from main only."})
	body := guidanceBody("ws", p)
	block := guidanceBlock(p.Revision, body)
	doc, action, err := spliceGuidance(prose, body, block)
	if err != nil || action != guidanceAppended {
		t.Fatalf("first write: %s %v", action, err)
	}
	if !strings.HasPrefix(doc, prose) || !strings.Contains(doc, "### Release rule\n\n<!-- ctx_a revision 2 -->\nTag from main only.") ||
		!strings.Contains(doc, guidanceBeginPrefix+" v1 revision=5 sha256=") {
		t.Fatalf("the prose was changed or the block is wrong:\n%s", doc)
	}
	// the same memory at a later revision changes nothing: the file keeps its stamp
	again, action, err := spliceGuidance(doc, body, guidanceBlock(9, body))
	if err != nil || action != guidanceUnchanged || again != doc {
		t.Fatalf("second write: %s %v\n%s", action, err, again)
	}
	// new memory replaces the block in place, prose before and after untouched
	after := doc + "\nTrailing prose.\n"
	p2 := testProjection(12, workspaceops.ProjectedMemory{ID: "ctx_b", Revision: 1, Title: "Port", Content: "The API listens on 8042."})
	body2 := guidanceBody("ws", p2)
	doc2, action, err := spliceGuidance(after, body2, guidanceBlock(12, body2))
	if err != nil || action != guidanceReplaced || !strings.HasPrefix(doc2, prose) || !strings.HasSuffix(doc2, "\nTrailing prose.\n") ||
		strings.Contains(doc2, "Tag from main only.") || !strings.Contains(doc2, "revision=12") || strings.Count(doc2, "revision=12") != 1 {
		t.Fatalf("replace: %s %v\n%s", action, err, doc2)
	}
	// markers that do not pair are the human's to fix
	for name, broken := range map[string]string{
		"two begins": doc + "\n" + guidanceBeginPrefix + " v1 revision=1 sha256=00 -->\n",
		"no end":     doc[:strings.LastIndex(doc, guidanceEnd)],
	} {
		if _, _, err := spliceGuidance(broken, body, block); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// A CRLF document keeps its line ending and the writer is idempotent on it: splicing the
// same body twice reports unchanged, and the block itself never mixes bare "\n" into a
// CRLF file.
func TestGuidanceBlockIsIdempotentOnCRLF(t *testing.T) {
	prose := "# Project\r\n\r\nOur own conventions stay ours.\r\n"
	p := testProjection(5, workspaceops.ProjectedMemory{ID: "ctx_a", Revision: 2, Title: "Release rule", Content: "Tag from main only."})
	body := guidanceBody("ws", p)
	block := guidanceBlock(p.Revision, body)

	doc, action, err := spliceGuidance(prose, body, block)
	if err != nil || action != guidanceAppended {
		t.Fatalf("first write: %s %v", action, err)
	}
	if strings.Contains(doc, "\n") && strings.Count(doc, "\r\n") != strings.Count(doc, "\n") {
		t.Fatalf("mixed line endings after first write:\n%q", doc)
	}
	again, action, err := spliceGuidance(doc, body, block)
	if err != nil || action != guidanceUnchanged || again != doc {
		t.Fatalf("second write should be a no-op: %s %v\n%q", action, err, again)
	}
}

// Memory text cannot break the block: a marker-like line is indented and a fence it
// opens is closed, so the block still parses back as one.
func TestGuidanceTextCannotBreakTheBlock(t *testing.T) {
	p := testProjection(3, workspaceops.ProjectedMemory{ID: "ctx_x", Revision: 1, Title: "tricky",
		Content: "before\n" + guidanceEnd + "\n```go\nfunc open() {"})
	body := guidanceBody("ws", p)
	doc, _, err := spliceGuidance("", body, guidanceBlock(3, body))
	if err != nil {
		t.Fatal(err)
	}
	begin, end, err := guidanceSpan(strings.Split(doc, "\n"))
	if err != nil || begin != 0 || !strings.HasPrefix(strings.Split(doc, "\n")[end], guidanceEnd) {
		t.Fatalf("the block does not parse back: %d %d %v\n%s", begin, end, err, doc)
	}
	if _, action, err := spliceGuidance(doc, body, guidanceBlock(4, body)); err != nil || action != guidanceUnchanged {
		t.Fatalf("rewrite: %s %v", action, err)
	}
	if empty := guidanceBody("ws", testProjection(1)); !strings.Contains(empty, "No human-approved core memory yet") {
		t.Fatalf("empty body: %s", empty)
	}
}

// The file writer creates, appends and leaves an unchanged file alone, keeps the file's
// mode, and never writes through a symlink.
func TestWriteGuidanceFile(t *testing.T) {
	root := t.TempDir()
	p := testProjection(5, workspaceops.ProjectedMemory{ID: "ctx_a", Revision: 2, Title: "rule", Content: "Tag from main only."})
	body := guidanceBody("ws", p)
	block := guidanceBlock(5, body)
	if action, err := writeGuidanceFile(root, "CLAUDE.md", body, block, true); err != nil || action != guidanceCreated {
		t.Fatalf("dry run: %s %v", action, err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the file: %v", err)
	}
	if action, err := writeGuidanceFile(root, "CLAUDE.md", body, block, false); err != nil || action != guidanceCreated {
		t.Fatalf("create: %s %v", action, err)
	}
	agents := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("# Agents\n\nKeep this.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if action, err := writeGuidanceFile(root, "AGENTS.md", body, block, false); err != nil || action != guidanceAppended {
		t.Fatalf("append: %s %v", action, err)
	}
	first, _ := os.ReadFile(agents)
	if !strings.HasPrefix(string(first), "# Agents\n\nKeep this.\n\n"+guidanceBeginPrefix) {
		t.Fatalf("appended file:\n%s", first)
	}
	if info, _ := os.Stat(agents); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	if action, err := writeGuidanceFile(root, "AGENTS.md", body, guidanceBlock(8, body), false); err != nil || action != guidanceUnchanged {
		t.Fatalf("second write: %s %v", action, err)
	}
	if again, _ := os.ReadFile(agents); string(again) != string(first) {
		t.Fatal("an unchanged write rewrote the file")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("a temporary file was left: %s", e.Name())
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Remove(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGuidanceFile(root, "CLAUDE.md", body, block, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("wrote through a symlink: %v", err)
	}
}
