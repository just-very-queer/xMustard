package workspaceops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// WS-57 (folding WS-71, PAR-REV-14): a human merge-approval attestation binds the
// repository, the merge base, the head and the digest of the diff between them, goes
// stale when any of them moves, and can be revoked.

// reviewRepo registers a workspace over a git repository whose feature branch (checked
// out) is one commit ahead of main, and returns a git runner for it.
func reviewRepo(t testing.TB) (dataDir, ws, root string, git func(args ...string) string) {
	t.Helper()
	dataDir, ws, root = t.TempDir(), "wsReview", t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(scrubbedGitEnv(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.go", "package a\n\nfunc A() int { return 1 }\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	write("a.go", "package a\n\nfunc A() int { return 2 }\n")
	write("b.go", "package a\n\nfunc B() {}\n")
	git("add", ".")
	git("commit", "-q", "-m", "change")
	rec := workspaceRecord{WorkspaceID: ws, Name: "review", RootPath: root}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", ws, "snapshot.json"), workspaceSnapshot{ScannerVersion: scannerVersion, Workspace: rec}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(workspacesPath(dataDir), []workspaceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	return dataDir, ws, root, git
}

func mergeState(t *testing.T, dir, ws string) *MergeApprovalStatus {
	t.Helper()
	st, err := MergeApprovalState(context.Background(), dir, ws, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestMergeApprovalBindsTheReviewedDiff(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, git := reviewRepo(t)
	h := humanApprover("alice", "alice-team")
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalNone || st.Enforcement != MergeApprovalEnforcement {
		t.Fatalf("no attestation yet: %+v", st)
	}
	first, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", []string{"review-1"}, "read every hunk")
	if err != nil {
		t.Fatal(err)
	}
	if first.Head != git("rev-parse", "HEAD") || first.MergeBase != git("rev-parse", "main") || len(first.DiffSHA256) != 64 ||
		first.DiffBytes == 0 || first.Repository != WorkspaceRepoScope(dir, ws) || first.Approver != "alice" ||
		first.ApproverOwner != "alice-team" || first.ApproverKind != PrincipalHuman || first.Assurance != AssuranceUserPresence ||
		len(first.ReviewRecords) != 1 || first.Note != "read every hunk" || first.Enforcement != MergeApprovalEnforcement {
		t.Fatalf("attestation: %+v", first)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalCurrent || st.Approval.Seq != first.Seq || !st.Current.same(first.ReviewedChange) {
		t.Fatalf("want current: %+v", st)
	}

	// a new head makes it stale without diffing again
	if err := os.WriteFile(filepath.Join(WorkspaceRepoScope(dir, ws), "c.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "more")
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalStale || st.Approval.Seq != first.Seq || st.Current != nil ||
		st.Head != git("rev-parse", "HEAD") {
		t.Fatalf("want stale after a new head: %+v", st)
	}
	second, err := ApproveMerge(ctx, dir, ws, h, "main", "HEAD", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalCurrent || st.Approval.Seq != second.Seq {
		t.Fatalf("want the new attestation current: %+v", st)
	}

	// the same head with another merge base is another diff: stale
	git("branch", "-f", "main", "HEAD~1")
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalStale || st.Current == nil || st.Current.DiffSHA256 == second.DiffSHA256 {
		t.Fatalf("want stale after the base moved: %+v", st)
	}
	git("branch", "-f", "main", second.MergeBase)
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalCurrent {
		t.Fatalf("the reviewed change is back: %+v", st)
	}

	// revocation
	if _, err := RevokeMergeApproval(ctx, dir, ws, h, second.Seq, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("revoke without a reason: %v", err)
	}
	revoked, err := RevokeMergeApproval(ctx, dir, ws, humanApprover("bob", "bob"), second.Seq, "found a bug")
	if err != nil || revoked.Revoked == nil || revoked.Revoked.By != "bob" || revoked.Revoked.Reason != "found a bug" {
		t.Fatalf("revoke: %v %+v", err, revoked)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalStale || st.Approval.Seq != first.Seq || st.Revoked != 1 {
		t.Fatalf("after revoking the current attestation only the stale one is left: %+v", st)
	}
	if _, err := RevokeMergeApproval(ctx, dir, ws, h, second.Seq, "again"); err == nil || !strings.Contains(err.Error(), "already revoked") {
		t.Fatalf("revoked twice: %v", err)
	}
	if _, err := RevokeMergeApproval(ctx, dir, ws, h, first.Seq, "old"); err != nil {
		t.Fatal(err)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalNone || st.Revoked != 2 || st.Approval != nil {
		t.Fatalf("everything revoked: %+v", st)
	}
	if _, err := RevokeMergeApproval(ctx, dir, ws, h, 999999, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoke an unknown seq: %v", err)
	}
}

func TestReviewDiffIgnoresRepositoryAndEnvironmentConfig(t *testing.T) {
	ctx := context.Background()
	dir, ws, root, git := reviewRepo(t)
	before, err := DiffReviewedChange(ctx, dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}, {"diff.algorithm", "patience"},
		{"diff.context", "10"}, {"diff.renames", "copies"}, {"diff.interHunkContext", "20"},
		{"diff.external", "false"}, {"diff.boom.textconv", "false"}, {"color.ui", "always"},
		{"diff.orderFile", filepath.Join(root, "order")}, {"core.quotePath", "false"},
	} {
		git("config", kv[0], kv[1])
	}
	if err := os.WriteFile(filepath.Join(root, "order"), []byte("b.go\na.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("* diff=boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_EXTERNAL_DIFF", "false")
	after, err := DiffReviewedChange(ctx, dir, ws, "main", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if after.DiffSHA256 != before.DiffSHA256 || after.DiffBytes != before.DiffBytes {
		t.Fatalf("repository or environment config changed the digested diff: %+v vs %+v", before, after)
	}
}

func TestMergeApprovalRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	dir, ws, _, _ := reviewRepo(t)
	h := humanApprover("alice", "alice")
	for name, tc := range map[string]struct {
		base, head string
		reviews    []string
	}{
		"option-shaped ref":    {"--output=/tmp/x", "HEAD", nil},
		"unknown ref":          {"no-such-branch", "HEAD", nil},
		"head already in base": {"feature", "HEAD", nil},
		"unsafe review id":     {"main", "HEAD", []string{"../x"}},
		"ref with a space":     {"main", "HEAD extra", nil},
		"too many review ids":  {"main", "HEAD", strings.Split(strings.Repeat("r,", maxReviewRecords+1), ",")},
	} {
		if _, err := ApproveMerge(ctx, dir, ws, h, tc.base, tc.head, tc.reviews, ""); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: want ErrInvalidInput, got %v", name, err)
		}
	}
	if _, err := ApproveMerge(ctx, dir, "unregistered", h, "main", "HEAD", nil, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unregistered workspace: %v", err)
	}
	if st := mergeState(t, dir, ws); st.Status != MergeApprovalNone {
		t.Fatalf("a refused approval was recorded: %+v", st)
	}
}

// BenchmarkMergeApprovalStateLargeDiff checks a current attestation over a change of
// 500 new files of 4 KiB each (a diff of about 2 MiB): the gate re-diffs it and streams the
// diff into the digest, so memory stays flat whatever the diff's size.
func BenchmarkMergeApprovalStateLargeDiff(b *testing.B) {
	dir, ws, root, git := reviewRepo(b)
	line := strings.Repeat("x", 63) + "\n"
	for i := range 500 {
		body := strings.Repeat(fmt.Sprintf("%03d", i)+line[3:], 64)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "large")
	a, err := ApproveMerge(context.Background(), dir, ws, humanApprover("alice", "alice"), "main", "HEAD", nil, "")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		st, err := MergeApprovalState(context.Background(), dir, ws, "HEAD")
		if err != nil || st.Status != MergeApprovalCurrent {
			b.Fatalf("%v %+v", err, st)
		}
	}
	b.ReportMetric(float64(a.DiffBytes), "diff_bytes")
}
