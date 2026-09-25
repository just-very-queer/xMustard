package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	hdir    string // harness-owned git directory (hidden from the agent)
	hindex  string // harness-owned index inside hdir
}

// newRunWorktree creates runRepo borrowing scratch's objects, a detached worktree of
// it at sha in dir, and the harness's own git directory for it in harnessDir.
func newRunWorktree(scratch, sha, runRepo, dir, harnessDir string) (*Worktree, error) {
	if err := os.MkdirAll(filepath.Dir(runRepo), 0o755); err != nil {
		return nil, err
	}
	if _, err := git(filepath.Dir(runRepo), "init", "-q", runRepo); err != nil {
		return nil, err
	}
	fail := func(err error) (*Worktree, error) {
		_ = os.RemoveAll(runRepo)
		_ = os.RemoveAll(harnessDir)
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
// outright: the agent could have configured it, so git is not run through it.
func (w *Worktree) Remove() error {
	if w.ownRepo {
		var errs []error
		for _, p := range []string{w.Dir, w.Repo, w.hdir} {
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
	if _, err := os.Stat(w.Dir); !os.IsNotExist(err) {
		return false
	}
	if w.ownRepo {
		for _, p := range []string{w.Repo, w.hdir} {
			if _, err := os.Stat(p); p != "" && !os.IsNotExist(err) {
				return false
			}
		}
		return true
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

// JudgeCopy makes dst, the tree the oracle judges. It copies the worktree, so ignored
// files such as installed dependencies come along, then resets every non-ignored file
// to tree through the harness's git directory, so dst holds exactly the agent's final
// snapshot whatever happened to the worktree since. dst must lie where no process of
// the run can reach it (the run profiles hide it), so nothing the run left behind can
// change the judged tree or read the oracle staged into it. dst is not a git
// repository: the copied .git file, which names the agent-writable run repository,
// is dropped.
func (w *Worktree) JudgeCopy(tree, dst string) error {
	if w.hdir == "" {
		return errors.New("worktree has no harness git directory")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := copyWorktree(w.Dir, dst); err != nil {
		return fmt.Errorf("copy the worktree: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(dst, ".git")); err != nil {
		return err
	}
	j := &Worktree{Dir: dst, hdir: w.hdir, hindex: filepath.Join(w.hdir, "judge-index")}
	defer os.Remove(j.hindex)
	// start the copy's index from tree, so entries a fresh `add -A` would not see
	// (gitlinks of submodules that are not checked out) stay as snapshotted
	if _, err := j.hgit(nil, "read-tree", tree); err != nil {
		return err
	}
	return j.RestoreTree(tree)
}

// copyWorktree copies src to dst (which must not exist) without following symbolic
// links: links are copied as links, and sockets, pipes and devices are left out. On
// macOS the directory is cloned in one clonefile call (copy-on-write on APFS);
// elsewhere, or when cloning fails, files are copied one by one (on Linux io.Copy
// between files uses copy_file_range, which shares extents where the filesystem can).
// Directories in the copy are owner-writable, so the reset to the snapshot can
// rewrite them.
func copyWorktree(src, dst string) error {
	// the agent can replace its worktree with a link; the harness must not copy (and
	// then write) through one
	if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is no longer a directory: %v", src, err)
	}
	err := cloneDir(src, dst)
	if err == nil {
		err = tidyCopy(dst)
	} else {
		_ = os.RemoveAll(dst)
		err = walkCopy(src, dst)
	}
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(dst); err != nil || !fi.IsDir() {
		return fmt.Errorf("the copy of %s is not a directory: %v", src, err)
	}
	return nil
}

func walkCopy(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch typ := d.Type(); {
		case typ.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.Mkdir(target, info.Mode().Perm()|0o700)
		case typ&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case typ.IsRegular():
			return copyRegular(p, target)
		}
		return nil
	})
}

// copyRegular copies one regular file with its permission bits. The source is opened
// without following a link and without blocking, and anything that turns out not to
// be a regular file is skipped.
func copyRegular(src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// tidyCopy removes sockets, pipes and devices from a cloned tree and makes its
// directories owner-writable.
func tidyCopy(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch typ := d.Type(); {
		case typ.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o700 != 0o700 {
				return os.Chmod(p, info.Mode().Perm()|0o700)
			}
		case typ.IsRegular(), typ&fs.ModeSymlink != 0:
		default:
			return os.Remove(p)
		}
		return nil
	})
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
