package main

import (
	"errors"
	"fmt"
	"io"
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
type Worktree struct {
	Repo    string // repository the worktree belongs to
	Dir     string
	SHA     string
	ownRepo bool // Repo is a per-run repository, removed with the worktree
}

// newRunWorktree creates runRepo borrowing scratch's objects and a detached worktree
// of it at sha in dir.
func newRunWorktree(scratch, sha, runRepo, dir string) (*Worktree, error) {
	if err := os.MkdirAll(filepath.Dir(runRepo), 0o755); err != nil {
		return nil, err
	}
	if _, err := git(filepath.Dir(runRepo), "init", "-q", runRepo); err != nil {
		return nil, err
	}
	fail := func(err error) (*Worktree, error) {
		_ = os.RemoveAll(runRepo)
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
	w.ownRepo = true
	return w, nil
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
// call more than once and after a partial creation.
func (w *Worktree) Remove() error {
	_, rmErr := git(w.Repo, "worktree", "remove", "--force", "--force", w.Dir)
	if _, err := os.Stat(w.Dir); err == nil {
		if err := os.RemoveAll(w.Dir); err != nil {
			return fmt.Errorf("remove worktree %s: %w (git: %v)", w.Dir, err, rmErr)
		}
	}
	if w.ownRepo {
		return os.RemoveAll(w.Repo)
	}
	if _, err := git(w.Repo, "worktree", "prune"); err != nil {
		return err
	}
	return nil
}

// gone reports whether the worktree and (for a per-run repository) its repository no
// longer exist, and git no longer lists the worktree.
func (w *Worktree) gone() bool {
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		return false
	}
	if w.ownRepo {
		_, err := os.Stat(w.Repo)
		return os.IsNotExist(err)
	}
	registered, err := registeredWorktree(w.Repo, w.Dir)
	return err == nil && !registered
}

// registered reports whether git still lists dir as a worktree of repo.
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
// untracked files, honouring .gitignore) as a git tree object without touching the
// worktree's own index or creating a commit. It seeds a private index from the real
// one so unchanged files are not rehashed.
func (w *Worktree) SnapshotTree(scratch string) (string, error) {
	realIndex, err := git(w.Dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(scratch, "index-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if src, err := os.Open(realIndex); err == nil {
		_, cerr := io.Copy(tmp, src)
		src.Close()
		if cerr != nil {
			tmp.Close()
			return "", cerr
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + tmpPath}
	if _, err := gitEnv(w.Dir, env, "add", "-A"); err != nil {
		return "", err
	}
	return gitEnv(w.Dir, env, "write-tree")
}

// DiffChurn is the size of an agent's change between two snapshots.
type DiffChurn struct {
	FilesChanged int      `json:"files_changed"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
	BinaryFiles  int      `json:"binary_files"`
	Files        []string `json:"files"`
}

// diffChurn computes numstat churn from tree a to tree b and writes the full patch to
// patchPath (when non-empty).
func diffChurn(repoDir, a, b, patchPath string) (DiffChurn, error) {
	var ch DiffChurn
	out, err := git(repoDir, "diff", "--no-renames", "--numstat", "-z", a, b)
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
		patch, err := git(repoDir, "diff", "--no-renames", "--binary", a, b)
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
