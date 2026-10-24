package workspaceops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryTrustIsExplicitPrivateAndRootSpecific(t *testing.T) {
	base := t.TempDir()
	root := initTrustRepo(t, filepath.Join(base, "repo"))
	data := filepath.Join(base, "data")
	if trusted, err := RepoTrusted(data, root); err != nil || trusted {
		t.Fatalf("initial trust=%v err=%v", trusted, err)
	}
	roots, err := UpdateRepoTrust(data, root, true)
	if err != nil || len(roots) != 1 {
		t.Fatalf("grant roots=%v err=%v", roots, err)
	}
	if trusted, err := RepoTrusted(data, root); err != nil || !trusted {
		t.Fatalf("after grant trust=%v err=%v", trusted, err)
	}
	info, err := os.Stat(RepoTrustPath(data))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("trust file mode=%v err=%v", info, err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if trusted, err := RepoTrusted(data, linked); err != nil || !trusted {
		t.Fatalf("symlink to same root trust=%v err=%v", trusted, err)
	}
	other := initTrustRepo(t, filepath.Join(base, "other"))
	if trusted, err := RepoTrusted(data, other); err != nil || trusted {
		t.Fatalf("other root trust=%v err=%v", trusted, err)
	}
	linkedWorktree := filepath.Join(base, "linked-worktree")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", linkedWorktree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create linked worktree: %v: %s", err, out)
	}
	if trusted, err := RepoTrusted(data, linkedWorktree); err != nil || trusted {
		t.Fatalf("linked worktree trust=%v err=%v", trusted, err)
	}
	if _, err := UpdateRepoTrust(data, root, false); err != nil {
		t.Fatal(err)
	}
	if trusted, err := RepoTrusted(data, root); err != nil || trusted {
		t.Fatalf("after revoke trust=%v err=%v", trusted, err)
	}
}

func TestRepositoryTrustRejectsRepositoryControlledTrustFile(t *testing.T) {
	root := initTrustRepo(t, filepath.Join(t.TempDir(), "repo"))
	if err := os.WriteFile(filepath.Join(root, ".xmustard.yaml"), []byte("trusted: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if trusted, err := RepoTrusted(filepath.Join(root, "operator-data"), root); err != nil || trusted {
		t.Fatalf("repo file granted trust=%v err=%v", trusted, err)
	}
}

func TestRepositoryCannotHoldOrGrantItsOwnTrustFile(t *testing.T) {
	root := initTrustRepo(t, filepath.Join(t.TempDir(), "repo"))
	payload := `{"roots":["` + root + `"]}`
	if err := os.WriteFile(RepoTrustPath(root), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if trusted, err := RepoTrusted(root, root); err == nil || trusted {
		t.Fatalf("repository trust file was accepted: trusted=%v err=%v", trusted, err)
	}
	if _, err := UpdateRepoTrust(root, root, true); err == nil {
		t.Fatal("grant stored trust inside the repository")
	}
}

func initTrustRepo(t *testing.T, root string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init", "-q"},
		{"config", "user.email", "trust-test@example.invalid"},
		{"config", "user.name", "trust test"},
		{"-c", "user.email=trust-test@example.invalid", "-c", "user.name=trust test", "commit", "--allow-empty", "-q", "-m", "init"},
	}
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}
