package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// judgeSite is where one run is judged. The agent can write, and replace, its run
// directory wt/<run>, which holds its worktree and whatever it left beside it. So
// once the agent has started, the harness reads that tree only where no process of
// the run can reach it, and the oracle still sees it at the path setup ran at:
//
//   - hold moves the run directory into judge/<run>/held, which every run's profile
//     hides, and checks that it and the worktree in it are directories, not links. A
//     process the run left behind loses access to the tree as it moves (the sandbox
//     matches a file's current path), so nothing changes it while the harness reads.
//   - prepare copies the held run directory to judge/<run>/copy without following a
//     link, drops the copy's .git file and resets the copied worktree's non-ignored
//     files to the agent's final snapshot.
//   - link puts a link to the copy at the run directory's path. The oracle runs in
//     the worktree's own path through it: absolute paths setup recorded (a
//     virtualenv's interpreter, an editable install) resolve into the copy. The run's
//     own profile allows that path but not the copy it resolves to, and it pins the
//     run directory's entry, so no process of the run can create, remove or replace
//     it, whether it holds the worktree, is held away, or is the link.
//   - restore removes the link and moves the held run directory back. It is safe to
//     call at any point and more than once.
type judgeSite struct {
	run  string // wt/<run>, whose parent only the harness writes
	name string // the worktree's directory name inside run
	held string // judge/<run>/held
	copy string // judge/<run>/copy
}

// newJudgeSite returns the judging site for w, whose run directory is w.runDir, under
// judgeRun (judge/<run>).
func newJudgeSite(w *Worktree, judgeRun string) *judgeSite {
	return &judgeSite{run: w.runDir, name: filepath.Base(w.Dir),
		held: filepath.Join(judgeRun, "held"), copy: filepath.Join(judgeRun, "copy")}
}

// judgedWorktree is the worktree inside the copy, where the oracle is staged.
func (s *judgeSite) judgedWorktree() string { return filepath.Join(s.copy, s.name) }

// hold moves the run directory out of every run profile's reach and returns a view of
// the worktree there for the harness's git. It fails with worktree_replaced when the
// run directory or the worktree in it is no longer a directory (the agent replaced it
// with a link or a file); what was there stays held, and restore moves it back.
func (s *judgeSite) hold(w *Worktree) (*Worktree, error) {
	if err := os.MkdirAll(filepath.Dir(s.held), 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(s.run, s.held); err != nil {
		return nil, fmt.Errorf("worktree_replaced: move the run directory aside: %w", err)
	}
	for _, p := range []string{s.held, filepath.Join(s.held, s.name)} {
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, fmt.Errorf("worktree_replaced: %w", err)
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("worktree_replaced: the agent replaced %s with a %s", filepath.Base(p), typeName(fi.Mode()))
		}
	}
	return w.at(filepath.Join(s.held, s.name)), nil
}

// release moves the held run directory back to its path.
func (s *judgeSite) release() error {
	if err := os.Rename(s.held, s.run); err != nil {
		return fmt.Errorf("return the run directory: %w", err)
	}
	return nil
}

// prepare makes the copy the oracle judges from the held run directory: the worktree
// in it holds exactly tree (the agent's final snapshot) plus the ignored files, such
// as installed dependencies, as they were after verify. It is not a git repository.
func (s *judgeSite) prepare(held *Worktree, tree string) error {
	if err := copyNoFollow(s.held, s.copy); err != nil {
		return fmt.Errorf("copy the run directory: %w", err)
	}
	dst := s.judgedWorktree()
	// the copied .git file names the agent-writable run repository
	if err := os.RemoveAll(filepath.Join(dst, ".git")); err != nil {
		return err
	}
	j := &Worktree{Dir: dst, hdir: held.hdir, hindex: filepath.Join(held.hdir, "judge-index")}
	defer os.Remove(j.hindex)
	// start the copy's index from tree, so entries a fresh `add -A` would not see
	// (gitlinks of submodules that are not checked out) stay as snapshotted
	if _, err := j.hgit(nil, "read-tree", tree); err != nil {
		return err
	}
	return j.RestoreTree(tree)
}

// link puts a link to the copy at the run directory's path.
func (s *judgeSite) link() error {
	if err := os.Symlink(s.copy, s.run); err != nil {
		return fmt.Errorf("judge_path_taken: %w", err)
	}
	return nil
}

// restore undoes link and hold.
func (s *judgeSite) restore() error {
	var errs []error
	if target, err := os.Readlink(s.run); err == nil && target == s.copy {
		errs = append(errs, os.Remove(s.run))
	}
	if _, err := os.Lstat(s.held); err == nil {
		errs = append(errs, s.release())
	}
	return errors.Join(errs...)
}

func typeName(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m.IsRegular():
		return "file"
	}
	return "special file"
}

// ---- copying without following links ----

// maxCopyDepth bounds how deep a file-by-file copy descends; each level holds two
// open descriptors.
const maxCopyDepth = 256

// copyHook, when set (tests only), runs after an entry has been identified by name
// and before it is opened, so a test can replace it at the worst moment.
var copyHook func(name string)

// copyNoFollow copies the directory src to dst (which must not exist) with the
// operator's rights, never following a symbolic link: links are copied as links, and
// sockets, pipes and devices are left out. src and every directory on its path must
// be ones only the harness can change. On macOS the tree is cloned in one clonefile
// call (copy-on-write on APFS). Elsewhere, or when cloning fails (clonefile refuses
// some entries an agent can create), it is copied entry by entry through directory
// handles. Directories in the copy are owner-writable, so the reset can rewrite them.
func copyNoFollow(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is a %s, not a directory", src, typeName(fi.Mode()))
	}
	if err := cloneDir(src, dst); err == nil {
		return tidyClone(dst)
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return walkCopy(src, dst)
}

// walkCopy copies src to dst entry by entry. Every entry is opened relative to its
// parent's open handle, by name, with O_NOFOLLOW, and must be the same file (device
// and inode) the listing identified: an entry replaced by a link, or by another file,
// while the walk runs fails the copy instead of being followed or read.
func walkCopy(src, dst string) error {
	sfd, err := openDirAt(unix.AT_FDCWD, src)
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	var st unix.Stat_t
	if err := unix.Fstat(sfd, &st); err != nil {
		return err
	}
	if err := os.Mkdir(dst, fs.FileMode(dirMode(&st))); err != nil {
		return err
	}
	dfd, err := openDirAt(unix.AT_FDCWD, dst)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	return copyDirAt(sfd, dfd, "", 0)
}

// copyDirAt copies the entries of the directory open as sfd into the one open as dfd.
// rel names sfd for errors.
func copyDirAt(sfd, dfd int, rel string, depth int) error {
	if depth >= maxCopyDepth {
		return fmt.Errorf("%s: deeper than %d directories", rel, maxCopyDepth)
	}
	names, err := dirNames(sfd)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	for _, name := range names {
		p := filepath.Join(rel, name)
		var st unix.Stat_t
		if err := unix.Fstatat(sfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if copyHook != nil {
			copyHook(p)
		}
		var err error
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			err = copySubdirAt(sfd, dfd, name, p, &st, depth)
		case unix.S_IFLNK:
			var target string
			if target, err = readlinkAt(sfd, name); err == nil {
				err = unix.Symlinkat(target, dfd, name)
			}
		case unix.S_IFREG:
			err = copyFileAt(sfd, dfd, name, &st)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func copySubdirAt(sfd, dfd int, name, rel string, st *unix.Stat_t, depth int) error {
	cfd, err := openDirAt(sfd, name)
	if err != nil {
		return err
	}
	defer unix.Close(cfd)
	if err := sameFile(cfd, st); err != nil {
		return err
	}
	if err := unix.Mkdirat(dfd, name, dirMode(st)); err != nil {
		return err
	}
	nfd, err := openDirAt(dfd, name)
	if err != nil {
		return err
	}
	defer unix.Close(nfd)
	return copyDirAt(cfd, nfd, rel, depth+1)
}

// copyFileAt copies one regular file with its permission bits. O_NONBLOCK keeps a
// pipe swapped in after the listing from blocking the open; sameFile then rejects it.
func copyFileAt(sfd, dfd int, name string, st *unix.Stat_t) error {
	in, err := unix.Openat(sfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	if err := sameFile(in, st); err != nil {
		unix.Close(in)
		return err
	}
	if err := unix.SetNonblock(in, false); err != nil {
		unix.Close(in)
		return err
	}
	inf := os.NewFile(uintptr(in), name)
	defer inf.Close()
	out, err := unix.Openat(dfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(st.Mode)&0o777)
	if err != nil {
		return err
	}
	outf := os.NewFile(uintptr(out), name)
	if _, err := io.Copy(outf, inf); err != nil {
		outf.Close()
		return err
	}
	return outf.Close()
}

// tidyClone removes sockets, pipes and devices from a cloned tree and makes its
// directories owner-writable, through directory handles like walkCopy.
// The clone is in a directory only the harness writes, so nothing replaces its
// entries meanwhile; the handles only keep the walk inside it.
func tidyClone(root string) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o700 != 0o700 {
		if err := os.Chmod(root, fi.Mode().Perm()|0o700); err != nil {
			return err
		}
	}
	fd, err := openDirAt(unix.AT_FDCWD, root)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return tidyDirAt(fd, "", 0)
}

func tidyDirAt(fd int, rel string, depth int) error {
	if depth >= maxCopyDepth {
		return fmt.Errorf("%s: deeper than %d directories", rel, maxCopyDepth)
	}
	names, err := dirNames(fd)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	for _, name := range names {
		p := filepath.Join(rel, name)
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		var err error
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if uint32(st.Mode)&0o700 != 0o700 {
				// before opening it: a directory its owner cannot read cannot be listed
				err = unix.Fchmodat(fd, name, dirMode(&st), 0)
			}
			if err == nil {
				err = tidySubdirAt(fd, name, p, &st, depth)
			}
		case unix.S_IFREG, unix.S_IFLNK:
		default:
			err = unix.Unlinkat(fd, name, 0)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func tidySubdirAt(fd int, name, rel string, st *unix.Stat_t, depth int) error {
	cfd, err := openDirAt(fd, name)
	if err != nil {
		return err
	}
	defer unix.Close(cfd)
	if err := sameFile(cfd, st); err != nil {
		return err
	}
	return tidyDirAt(cfd, rel, depth+1)
}

// openDirAt opens the directory name relative to dirfd without following a link.
func openDirAt(dirfd int, name string) (int, error) {
	for {
		fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// sameFile fails unless the open fd is the file st describes.
func sameFile(fd int, st *unix.Stat_t) error {
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		return err
	}
	if got.Dev != st.Dev || got.Ino != st.Ino || got.Mode&unix.S_IFMT != st.Mode&unix.S_IFMT {
		return errors.New("replaced while it was being copied")
	}
	return nil
}

// dirNames lists the directory open as fd.
func dirNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "")
	defer f.Close()
	return f.Readdirnames(-1)
}

func readlinkAt(dirfd int, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dirfd, name, buf)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// dirMode is a copied directory's permission bits, owner rwx added.
func dirMode(st *unix.Stat_t) uint32 { return uint32(st.Mode)&0o777 | 0o700 }
