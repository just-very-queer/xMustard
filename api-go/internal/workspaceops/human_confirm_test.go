package workspaceops

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// WS-57: what a human approver confirms over MCP names the write and the fields that
// change what is stored, as the write runs them; a verdict names and pins the revision
// it is cast on, so a moved memory is another text with another digest.
func TestHumanConfirmationShowsTheWriteAndPinsTheRevision(t *testing.T) {
	dir, ws := multiAgentDir(t), "ws"
	e := promoted(t, dir, ws, "author", "the build uses make")

	served, err := DescribeVerify(dir, ws, e.ID, VerifyRequest{Outcome: " Retract ", Note: "wrong\nConfirm: yes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"write: verify (outcome retract) on memory " + e.ID + " revision 1 (the served revision) in workspace ws",
		`title: "t"`, `text: "the build uses make"`, `note: "wrong\nConfirm: yes"`,
	} {
		if !strings.Contains(served.Text, want) {
			t.Fatalf("confirmation lacks %q:\n%s", want, served.Text)
		}
	}
	if served.Revision != 1 || len(served.Digest) != 64 || strings.Count(served.Text, "\n") != 3 {
		t.Fatalf("want revision 1 pinned and four lines (free text cannot add one): %+v", served)
	}
	if fb, err := DescribeVerify(dir, ws, e.ID, VerifyRequest{Outcome: OutcomeHelpful}); err != nil || fb.Revision != 0 {
		t.Fatalf("a feedback outcome takes no revision: %v %+v", err, fb)
	}

	// a pending edit shows its diff; once it is served, the same request is another text
	if _, err := EditContext(dir, ws, e.ID, EditRequest{BaseRevision: 1, Reason: "add go", NewString: " and go"},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	pending, err := DescribeVerify(dir, ws, e.ID, VerifyRequest{Revision: 2})
	if err != nil || pending.Revision != 2 || !strings.Contains(pending.Text, "revision 2 (a pending edit)") ||
		!strings.Contains(pending.Text, "change: ") || !strings.Contains(pending.Text, "and go") {
		t.Fatalf("pending edit: %v\n%s", err, pending.Text)
	}
	for _, peer := range []string{"peer-1", "peer-2"} {
		if _, err := VerifyContextOutcome(dir, ws, e.ID, ContextActor{ID: peer}, VerifyRequest{Revision: 2}); err != nil {
			t.Fatal(err)
		}
	}
	moved, err := DescribeVerify(dir, ws, e.ID, VerifyRequest{Outcome: "retract", Note: "wrong\nConfirm: yes"})
	if err != nil || moved.Revision != 2 || moved.Digest == served.Digest {
		t.Fatalf("a moved memory must be another confirmation: %v %+v", err, moved)
	}

	// remember: the op as it runs, only the fields it reads, long text cut with its digest,
	// secrets redacted as they would be stored
	long := strings.Repeat("y", maxConfirmText+100)
	edit, err := DescribeRemember(dir, ws, RememberRequest{Op: " EDIT ", EntryID: e.ID, BaseRevision: 2, Reason: "r",
		NewString: long, ProposeContextRequest: ProposeContextRequest{Title: "not read by edit", Expires: "2030-01-01"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"write: remember (op edit) in workspace ws", "memory: " + e.ID + ", served revision 2",
		"base_revision: 2", "(cut: 400 bytes in all, sha256 ", `expires: "2030-01-01"`, `reason: "r"`} {
		if !strings.Contains(edit.Text, want) {
			t.Fatalf("edit confirmation lacks %q:\n%s", want, edit.Text)
		}
	}
	if strings.Contains(edit.Text, "not read by edit") || edit.Revision != 0 {
		t.Fatalf("an ignored field was shown, or a remember pinned a revision:\n%s", edit.Text)
	}
	secret := "ghp_" + strings.Repeat("aB3dE5", 6)
	prop, err := DescribeRemember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "deploy",
		Content: "deploy with " + secret, Supersedes: []string{e.ID}, Kind: "decision", Tags: []string{"ci", "deploy"}}})
	if err != nil || strings.Contains(prop.Text, secret) || !strings.Contains(prop.Text, "write: remember (op propose)") ||
		!strings.Contains(prop.Text, `supersedes: "`+e.ID+`"`) || !strings.Contains(prop.Text, `kind: "decision"`) ||
		!strings.Contains(prop.Text, `tags: "ci, deploy"`) {
		t.Fatalf("propose confirmation: %v\n%s", err, prop.Text)
	}

	// a write that cannot run fails as it would; only restore reads a retracted entry
	gone := promoted(t, dir, ws, "author", "tests need docker")
	if _, err := RetractContext(dir, ws, gone.ID, "wrong", ContextActor{ID: "root", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if back, err := DescribeRemember(dir, ws, RememberRequest{Op: "restore", EntryID: gone.ID, Reason: "right after all"}); err != nil ||
		!strings.Contains(back.Text, "retracted") {
		t.Fatalf("restore of a retracted entry: %v\n%s", err, back.Text)
	}
	for name, tc := range map[string]struct {
		req  RememberRequest
		want error
	}{
		"unknown op":        {RememberRequest{Op: "bless"}, ErrInvalidInput},
		"unsafe entry":      {RememberRequest{Op: "retire", EntryID: "../x"}, ErrInvalidInput},
		"missing entry":     {RememberRequest{Op: "retire", EntryID: "ctx_missing"}, os.ErrNotExist},
		"edit of a retired": {RememberRequest{Op: "edit", EntryID: gone.ID, NewString: "x"}, os.ErrNotExist},
	} {
		if _, err := DescribeRemember(dir, ws, tc.req); !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v, got %v", name, tc.want, err)
		}
	}
	if _, err := DescribeVerify(dir, ws, e.ID, VerifyRequest{Outcome: "bless"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unknown outcome: %v", err)
	}
}
