package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Worktree is one fresh, detached checkout of a task's starting commit. Every run gets
// its own worktree of its own repository: the per-run repository borrows the scratch
// repository's objects through git alternates, so refs, config, hooks and objects an
// agent creates (a commit, a branch, a hook) die with the run and never reach a later
// run of the same task.
//
// The agent can write the per-run repository (its config, hooks, alternates) and the
// worktree's .git file, and git runs configured commands (core.fsmonitor, clean
// filters, diff.external) as whoever invokes it. So once the agent has started, the
// harness never runs git through them: snapshots and diffs go through a harness-owned
// git directory and index (hdir, hindex) that borrow only the scratch objects, and
// removal deletes the directories without running git.
type Worktree struct {
	Repo    string // repository the worktree belongs to
	Dir     string
	SHA     string
	ownRepo bool   // Repo is a per-run repository, removed with the worktree
	runDir  string // the per-run directory holding Dir (per-run repositories only)
	hdir    string // harness-owned git directory (hidden from the agent)
	hindex  string // harness-owned index inside hdir
}

// newRunWorktree creates runRepo borrowing scratch's objects, a detached worktree of
// it at sha in runDir/name, and the harness's own git directory for it in harnessDir.
// runDir is the run's own directory. Only the harness may write its parent, and the
// agent may replace runDir itself (with a link, say), so it is removed as a whole and
// never through a path below it.
func newRunWorktree(scratch, sha, runRepo, runDir, name, harnessDir string) (*Worktree, error) {
	dir := filepath.Join(runDir, name)
	if err := os.MkdirAll(filepath.Dir(runRepo), 0o755); err != nil {
		return nil, err
	}
	if _, err := git(filepath.Dir(runRepo), "init", "-q", runRepo); err != nil {
		return nil, err
	}
	fail := func(err error) (*Worktree, error) {
		_ = os.RemoveAll(runRepo)
		_ = os.RemoveAll(harnessDir)
		_ = os.RemoveAll(runDir)
		return nil, err
	}
	objects, err := filepath.Abs(filepath.Join(scratch, ".git", "objects"))
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(runRepo, ".git", "objects", "info", "alternates"), []byte(objects+"\n"), 0o644); err != nil {
		return fail(err)
	}
	// a shallow scratch repository (fetched from a shallow fixture clone) needs its
	// shallow roots, or history walks stop at a missing parent
	if b, err := os.ReadFile(filepath.Join(scratch, ".git", "shallow")); err == nil {
		if err := os.WriteFile(filepath.Join(runRepo, ".git", "shallow"), b, 0o644); err != nil {
			return fail(err)
		}
	}
	w, err := addDetachedWorktree(runRepo, sha, dir)
	if err != nil {
		return fail(err)
	}
	w.ownRepo, w.runDir = true, runDir
	if err := w.initHarnessGit(objects, harnessDir); err != nil {
		_ = w.Remove()
		return fail(err)
	}
	return w, nil
}

// initHarnessGit creates the harness's git directory for w: a bare repository that
// borrows only the given (read-only) objects, and an index seeded from the fresh
// checkout's own index, which git wrote before any agent or task command ran.
func (w *Worktree) initHarnessGit(objects, harnessDir string) error {
	if err := os.MkdirAll(filepath.Dir(harnessDir), 0o700); err != nil {
		return err
	}
	if _, err := git(filepath.Dir(harnessDir), "init", "-q", "--bare", harnessDir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(harnessDir, "objects", "info", "alternates"), []byte(objects+"\n"), 0o644); err != nil {
		return err
	}
	realIndex, err := git(w.Dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return err
	}
	b, err := os.ReadFile(realIndex)
	if err != nil {
		return err
	}
	w.hdir, w.hindex = harnessDir, filepath.Join(harnessDir, "index")
	return os.WriteFile(w.hindex, b, 0o600)
}

// at is a view of w's worktree at dir (moved there by the harness), for the
// harness's git only.
func (w *Worktree) at(dir string) *Worktree {
	return &Worktree{Repo: w.Repo, Dir: dir, SHA: w.SHA, hdir: w.hdir, hindex: w.hindex}
}

// hgit runs git on the worktree through the harness-owned git directory and index.
// Only the harness's own repository config applies; attributes in the worktree can
// name a filter or diff driver, but no configuration defines one.
func (w *Worktree) hgit(env []string, args ...string) (string, error) {
	if w.hdir == "" {
		return "", errors.New("worktree has no harness git directory")
	}
	env = append([]string{"GIT_DIR=" + w.hdir, "GIT_WORK_TREE=" + w.Dir, "GIT_INDEX_FILE=" + w.hindex}, env...)
	return gitEnv(w.Dir, env, append([]string{"-c", "core.bare=false"}, args...)...)
}

// worktreeRegistry tracks every live worktree so an interrupt can remove the ones a
// run's own deferred cleanup has not reached yet.
type worktreeRegistry struct {
	mu   sync.Mutex
	live map[string]*Worktree
}

func newWorktreeRegistry() *worktreeRegistry {
	return &worktreeRegistry{live: map[string]*Worktree{}}
}

func (r *worktreeRegistry) add(w *Worktree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[w.Dir] = w
}

func (r *worktreeRegistry) remove(w *Worktree) error {
	r.mu.Lock()
	delete(r.live, w.Dir)
	r.mu.Unlock()
	return w.Remove()
}

// forget stops tracking w without removing it (a kept worktree).
func (r *worktreeRegistry) forget(w *Worktree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, w.Dir)
}

// removeAll removes every worktree still registered.
func (r *worktreeRegistry) removeAll() error {
	r.mu.Lock()
	dirs := make([]string, 0, len(r.live))
	for d := range r.live {
		dirs = append(dirs, d)
	}
	r.mu.Unlock()
	slices.Sort(dirs)
	var errs []error
	for _, d := range dirs {
		r.mu.Lock()
		w := r.live[d]
		r.mu.Unlock()
		if w != nil {
			errs = append(errs, r.remove(w))
		}
	}
	return errors.Join(errs...)
}

// addDetachedWorktree creates dir as a detached worktree of repo at sha and confirms
// HEAD is detached at exactly that commit.
func addDetachedWorktree(repo, sha, dir string) (*Worktree, error) {
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("worktree path %s already exists", dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if _, err := git(repo, "worktree", "add", "--detach", "-q", dir, sha); err != nil {
		return nil, err
	}
	w := &Worktree{Repo: repo, Dir: dir, SHA: sha}
	detached, err := w.Detached()
	if err == nil && !detached {
		err = errors.New("worktree HEAD is attached to a branch")
	}
	if err == nil {
		var head string
		if head, err = git(dir, "rev-parse", "HEAD"); err == nil && head != sha {
			err = fmt.Errorf("worktree HEAD %s is not %s", head, sha)
		}
	}
	if err != nil {
		_ = w.Remove()
		return nil, err
	}
	return w, nil
}

// Detached reports whether HEAD is detached (not a symbolic ref to a branch).
func (w *Worktree) Detached() (bool, error) {
	_, err := git(w.Dir, "symbolic-ref", "-q", "HEAD")
	if err == nil {
		return false, nil
	}
	// symbolic-ref -q exits 1 for a detached HEAD; anything else is a real failure.
	if strings.Contains(err.Error(), "exit status 1") {
		return true, nil
	}
	return false, err
}

// Remove deletes the worktree directory and its administrative entry. It is safe to
// call more than once and after a partial creation. A per-run repository is deleted
// outright: the agent could have configured it, so git is not run through it. Its
// worktree goes with the whole run directory: removing a path below it would follow
// a link the agent put in its place (os.RemoveAll follows every component but the
// last), and delete whatever the link names.
func (w *Worktree) Remove() error {
	if w.ownRepo {
		var errs []error
		for _, p := range []string{w.runDir, w.Repo, w.hdir} {
			if p != "" {
				errs = append(errs, os.RemoveAll(p))
			}
		}
		return errors.Join(errs...)
	}
	_, rmErr := git(w.Repo, "worktree", "remove", "--force", "--force", w.Dir)
	if _, err := os.Stat(w.Dir); err == nil {
		if err := os.RemoveAll(w.Dir); err != nil {
			return fmt.Errorf("remove worktree %s: %w (git: %v)", w.Dir, err, rmErr)
		}
	}
	if _, err := git(w.Repo, "worktree", "prune"); err != nil {
		return err
	}
	return nil
}

// gone reports whether the worktree and (for a per-run repository) its repository no
// longer exist, and git no longer lists the worktree.
func (w *Worktree) gone() bool {
	if w.ownRepo {
		for _, p := range []string{w.runDir, w.Repo, w.hdir} {
			if _, err := os.Lstat(p); p != "" && !os.IsNotExist(err) {
				return false
			}
		}
		return true
	}
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		return false
	}
	registered, err := registeredWorktree(w.Repo, w.Dir)
	return err == nil && !registered
}

// registeredWorktree reports whether git still lists dir as a worktree of repo.
func registeredWorktree(repo, dir string) (bool, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want, _ := filepath.EvalSymlinks(dir)
	for _, line := range strings.Split(out, "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if p == dir || (want != "" && p == want) {
			return true, nil
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && real == want {
			return true, nil
		}
	}
	return false, nil
}

// SnapshotTree records the complete working-tree state (tracked, modified and new
// untracked files, honouring .gitignore) as a git tree object in the harness's git
// directory, without touching the worktree's own index or creating a commit. The
// harness index keeps stat data from the previous snapshot, so unchanged files are
// not rehashed.
func (w *Worktree) SnapshotTree() (string, error) {
	if _, err := w.hgit(nil, "add", "-A"); err != nil {
		return "", err
	}
	return w.hgit(nil, "write-tree")
}

// RestoreTree resets the worktree's non-ignored files to tree: files changed or
// deleted since are written back, and new untracked files and directories are
// removed. Ignored files are left alone (they are outside every snapshot).
func (w *Worktree) RestoreTree(tree string) error {
	idx := filepath.Join(w.hdir, "restore-index")
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	for _, args := range [][]string{{"read-tree", tree}, {"checkout-index", "-a", "-f"}, {"clean", "-ffdq"}} {
		if _, err := w.hgit(env, args...); err != nil {
			return err
		}
	}
	got, err := w.SnapshotTree()
	if err != nil {
		return err
	}
	if got != tree {
		return fmt.Errorf("worktree is %s after restoring %s", got, tree)
	}
	return nil
}

// DiffChurn is the size of an agent's change between two snapshots.
type DiffChurn struct {
	FilesChanged int      `json:"files_changed"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	BinaryFiles  int      `json:"binary_files"`
	Files        []string `json:"files"`
}

// diffChurn computes numstat churn from tree a to tree b in the harness's git
// directory and writes the full patch to patchPath (when non-empty). External diff
// drivers and textconv are off.
func diffChurn(w *Worktree, a, b, patchPath string) (DiffChurn, error) {
	var ch DiffChurn
	out, err := w.hgit(nil, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", "-z", a, b)
	if err != nil {
		return ch, err
	}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		ch.FilesChanged++
		ch.Files = append(ch.Files, parts[2])
		if parts[0] == "-" || parts[1] == "-" {
			ch.BinaryFiles++
			continue
		}
		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		ch.Insertions += add
		ch.Deletions += del
	}
	slices.Sort(ch.Files)
	if patchPath != "" {
		patch, err := w.hgit(nil, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", a, b)
		if err != nil {
			return ch, err
		}
		if patch != "" {
			patch += "\n"
		}
		if err := os.WriteFile(patchPath, []byte(patch), 0o644); err != nil {
			return ch, err
		}
	}
	return ch, nil
}
