//go:build unix

package workspaceops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// Spawn-free working-tree fingerprint (PAR-FRESH-02, PAR-RT-12). The identity cache
// reuses a sampled `repo-key` identity only while this fingerprint is unchanged. It
// walks what `git status --untracked-files=all` walks, without running git:
//   - HEAD, the ref it names (loose), and the stat keys of packed-refs / reftable;
//   - the stat keys of the index and of every file that changes what status shows
//     or ignores: the repository config and config.worktree, info/exclude,
//     info/attributes, info/sparse-checkout, the global and XDG config, ignore and
//     attributes files, the common system config paths, the config files any of
//     these include ([include] / [includeIf], whatever the condition), and the
//     core.excludesFile / core.attributesFile any of them names;
//   - every directory git traverses: from the worktree top, every directory except
//     the ones `repo-key` reported as ignored as a whole (git never descends into
//     them). Each directory enters by its own stat key and its listing in full
//     (names and types), and every file or symlink in it by its stat key (mode,
//     size, mtime, ctime, inode). That covers edits to tracked and untracked files
//     at any depth, files added to, removed from or renamed within any traversed
//     directory, and new directories. A removal leaves no stat key of its own: the
//     directory's (its mtime and ctime move) is what lets the racy rule below see
//     one when there is no earlier listing to compare with;
//   - nested worktrees (submodules, or repositories inside the tree): their HEAD,
//     refs, index and config, and all of their directories (their own ignore rules
//     are not known, so nothing inside them is skipped).
//
// An ignored directory enters by name and type only: git reads nothing inside it,
// and it holds no tracked file in the index state it was reported for (the index
// stat key is part of the fingerprint).
//
// Timestamps can be coarse (1 s, 2 s on FAT), so a stat key alone cannot see a
// same-size rewrite within one tick. Every fingerprint therefore records the newest
// timestamp among its stat keys, and the identity cache applies Git's racy rule: a
// fingerprint vouches for no change after the moment it (or the sample it is paired
// with) began only if every stat key it holds is older than that moment by
// fingerprintRacyWindow (repo_identity.go).
//
// Not covered: system config under other install prefixes, config named by
// GIT_CONFIG_COUNT / GIT_CONFIG_PARAMETERS in repo-key's environment, include paths
// using %(prefix), and a clock on another host (network filesystems) that disagrees
// with this one. The identity TTL bounds how long such a change can go
// unseen while a cached identity is reused. A walk that exceeds its bounds, a
// nested-worktree depth over fingerprintMaxNesting, or a layout it cannot read makes
// the fingerprint unavailable, and the caller samples instead.

// fingerprintEntryBound is the directory entries one walk may read (a var for tests).
var fingerprintEntryBound = 400_000

const (
	fingerprintMaxDirs      = 50_000 // directories traversed across one walk
	fingerprintBatch        = 1024   // directory entries read at a time
	fingerprintMaxSmallFile = 4 << 10
	fingerprintMaxConfig    = 1 << 20
	fingerprintMaxNesting   = 3 // nested worktrees inside nested worktrees
)

// fingerprintSupported: this platform can fingerprint without spawning.
const fingerprintSupported = true

// statMark reads a registry source file's stat identity. A missing file is an error
// wrapping os.ErrNotExist, as os.Stat reports it.
func statMark(path string) (fileMark, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return fileMark{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	return fileMark{exists: true, size: st.Size, mtimeNs: st.Mtim.Nano(), ctimeNs: st.Ctim.Nano(),
		ino: uint64(st.Ino), dev: uint64(st.Dev)}, nil
}

// gitLayoutFor finds the worktree top, its git dir and the common dir for root,
// the way git discovers them: the nearest .git directory or gitfile upward.
func gitLayoutFor(root string) (top, gitDir, commonDir string, found bool, err error) {
	dir := filepath.Clean(root)
	for {
		gitDir, commonDir, found, err = gitDirsAt(dir)
		if err != nil || found {
			return dir, gitDir, commonDir, found, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", "", false, nil
		}
		dir = parent
	}
}

// gitDirsAt resolves dir/.git (a directory, or a gitfile naming one) and the common
// dir a linked worktree shares. found is false when dir has no .git.
func gitDirsAt(dir string) (gitDir, commonDir string, found bool, err error) {
	p := filepath.Join(dir, ".git")
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", "", false, nil
	case err != nil:
		return "", "", false, err
	case fi.IsDir():
		gitDir = p
	case fi.Mode().IsRegular():
		b, err := readSmallFile(p)
		if err != nil {
			return "", "", false, err
		}
		gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !ok {
			return "", "", false, errors.New("unrecognized gitfile")
		}
		if gd = strings.TrimSpace(gd); !filepath.IsAbs(gd) {
			gd = filepath.Join(dir, gd)
		}
		gitDir = gd
	default:
		return "", "", false, errors.New(".git is neither a directory nor a gitfile")
	}
	commonDir = gitDir
	if b, err := readSmallFile(filepath.Join(gitDir, "commondir")); err == nil {
		cd := strings.TrimSpace(string(b))
		if !filepath.IsAbs(cd) {
			cd = filepath.Join(gitDir, cd)
		}
		commonDir = cd
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", false, err
	}
	return gitDir, commonDir, true, nil
}

func readSmallFile(path string) ([]byte, error) {
	return readBounded(path, fingerprintMaxSmallFile)
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file too large for a git metadata file")
	}
	return b, nil
}

// digestSink hashes one unit of the fingerprint (the metadata, or one directory)
// and keeps the newest timestamp among its stat keys. It formats into its own
// buffer: the walk writes one key per file.
type digestSink struct {
	h      hash.Hash
	newest int64
	buf    []byte
}

func newDigestSink() *digestSink {
	return &digestSink{h: sha256.New(), buf: make([]byte, 0, 256)}
}

func (s *digestSink) reset() {
	s.h.Reset()
	s.newest = 0
}

// str adds NUL-terminated strings.
func (s *digestSink) str(parts ...string) {
	b := s.buf[:0]
	for _, p := range parts {
		b = append(append(b, p...), 0)
	}
	s.h.Write(b)
	s.buf = b
}

// statKey adds a stat key (or the reason there is none).
func (s *digestSink) statKey(label string, st *unix.Stat_t, err error) {
	b := append(s.buf[:0], label...)
	if err != nil {
		b = append(b, ":err:"...)
		b = strconv.AppendBool(b, errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT))
	} else {
		b = append(b, ':')
		b = strconv.AppendUint(b, uint64(st.Mode), 8)
		for _, v := range [...]int64{st.Size, st.Mtim.Nano(), st.Ctim.Nano(), int64(st.Ino)} {
			b = append(b, ':')
			b = strconv.AppendInt(b, v, 10)
		}
		s.newest = newestNs(s.newest, newestNs(st.Mtim.Nano(), st.Ctim.Nano()))
	}
	s.h.Write(append(b, 0))
	s.buf = b
}

// fileKey adds the stat key of a metadata file, following symlinks as git does when
// it reads config and ignore files.
func (s *digestSink) fileKey(label, path string) {
	var st unix.Stat_t
	err := unix.Stat(path, &st)
	s.statKey(label, &st, err)
}

// repoStatFingerprint computes the fingerprint of the working state at root, walking
// every directory except those in ignored (repo-key's listing for this root). Inside
// a Git worktree it needs that listing: without it git's traversal is unknown and the
// fingerprint is unavailable.
func repoStatFingerprint(root string, ignored *ignoreSet) repoFingerprint {
	start := identityNow()
	if root == "" {
		return repoFingerprint{}
	}
	top, gitDir, commonDir, found, err := gitLayoutFor(root)
	if err != nil {
		return repoFingerprint{}
	}
	if !found {
		sum := sha256.Sum256([]byte("nogit\x00" + root))
		return repoFingerprint{digest: hex.EncodeToString(sum[:]), ok: true, startedAt: start}
	}
	if ignored == nil {
		return repoFingerprint{}
	}
	fingerprintWalks.Add(1)
	meta := newDigestSink()
	meta.str("ignored", ignored.digest)
	meta.userConfig(top)
	w := &fingerprintWalker{ignored: ignored}
	w.entries.Store(int64(fingerprintEntryBound))
	w.dirs.Store(fingerprintMaxDirs)
	if !meta.metadata(top, gitDir, commonDir) || !w.walk(top) {
		return repoFingerprint{}
	}
	// directories were digested in parallel: combine them in path order
	slices.SortFunc(w.units, func(a, b dirUnit) int { return strings.Compare(a.rel, b.rel) })
	h := sha256.New()
	h.Write(meta.h.Sum(nil))
	for _, u := range w.units {
		h.Write([]byte(u.rel))
		h.Write([]byte{0})
		h.Write(u.sum[:])
	}
	return repoFingerprint{digest: hex.EncodeToString(h.Sum(nil)), ok: true, git: true,
		newestNs: newestNs(meta.newest, w.newest), startedAt: start}
}

// metadata adds one worktree's HEAD, refs, index and repository-level config,
// ignore, attributes and sparse-checkout files.
func (s *digestSink) metadata(top, gitDir, commonDir string) bool {
	s.str("git", top, gitDir, commonDir)
	head, err := readSmallFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false
	}
	s.str(string(head))
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		if strings.Contains(ref, "..") || filepath.IsAbs(ref) {
			return false
		}
		loose, lerr := readSmallFile(filepath.Join(gitDir, ref))
		if errors.Is(lerr, os.ErrNotExist) && gitDir != commonDir {
			loose, lerr = readSmallFile(filepath.Join(commonDir, ref))
		}
		if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
			return false
		}
		s.str(string(loose))
	}
	s.fileKey("packed-refs", filepath.Join(commonDir, "packed-refs"))
	s.fileKey("reftable", filepath.Join(commonDir, "reftable", "tables.list"))
	s.fileKey("index", filepath.Join(gitDir, "index"))
	s.fileKey("config", filepath.Join(commonDir, "config"))
	s.fileKey("config.worktree", filepath.Join(gitDir, "config.worktree"))
	s.fileKey("exclude", filepath.Join(commonDir, "info", "exclude"))
	s.fileKey("attributes", filepath.Join(commonDir, "info", "attributes"))
	s.fileKey("sparse-checkout", filepath.Join(commonDir, "info", "sparse-checkout"))
	for i, p := range configuredFiles(filepath.Join(commonDir, "config"), top) {
		s.fileKey("repo-configured-"+strconv.Itoa(i), p)
	}
	// git reads config.worktree only with extensions.worktreeConfig; following it
	// regardless costs at most a few stat keys
	for i, p := range configuredFiles(filepath.Join(gitDir, "config.worktree"), top) {
		s.fileKey("worktree-configured-"+strconv.Itoa(i), p)
	}
	return true
}

// userConfig adds the global, XDG and system config files and the ignore and
// attributes files they name (core.excludesFile, core.attributesFile) or default to.
func (s *digestSink) userConfig(top string) {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	configs := []string{"/etc/gitconfig", "/opt/homebrew/etc/gitconfig", "/usr/local/etc/gitconfig",
		os.Getenv("GIT_CONFIG_GLOBAL"), os.Getenv("GIT_CONFIG_SYSTEM")}
	if home != "" {
		configs = append(configs, filepath.Join(home, ".gitconfig"))
	}
	if xdg != "" {
		configs = append(configs, filepath.Join(xdg, "git", "config"))
		s.fileKey("xdg-ignore", filepath.Join(xdg, "git", "ignore"))
		s.fileKey("xdg-attributes", filepath.Join(xdg, "git", "attributes"))
	}
	for i, c := range configs {
		if c == "" {
			continue
		}
		s.fileKey("config-"+strconv.Itoa(i), c)
		for j, p := range configuredFiles(c, top) {
			s.fileKey("configured-"+strconv.Itoa(i)+"-"+strconv.Itoa(j), p)
		}
	}
}

// configuredFiles returns the files a config file adds to what status reads: the
// config files it includes through [include] / [includeIf] (whatever the condition:
// following one git skips only adds a stat key), scanned in turn to git's include
// depth, and the core.excludesFile / core.attributesFile any of them sets (a key
// scan: both keys exist only in [core]). An include path is relative to the
// including file's directory; an excludes or attributes path is relative to top,
// where repo-key runs git. "~/" is the home directory in both.
func configuredFiles(configPath, top string) []string {
	var out []string
	seen := map[string]bool{}
	var scan func(path string, depth int)
	scan = func(path string, depth int) {
		if seen[path] {
			return
		}
		seen[path] = true
		b, err := readBounded(path, fingerprintMaxConfig)
		if err != nil {
			return
		}
		section := ""
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(line, "["); ok {
				header, after, _ := strings.Cut(rest, "]")
				name, _, _ := strings.Cut(strings.TrimSpace(header), " ")
				name, _, _ = strings.Cut(name, ".") // the older [section.subsection] form
				section, line = strings.ToLower(name), after
			}
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			switch {
			case key == "excludesfile" || key == "attributesfile":
				if p := configValuePath(val, top); p != "" {
					out = append(out, p)
				}
			case key == "path" && (section == "include" || section == "includeif") && depth < configMaxIncludeDepth:
				if p := configValuePath(val, filepath.Dir(path)); p != "" {
					out = append(out, p)
					scan(p, depth+1)
				}
			}
		}
	}
	scan(configPath, 0)
	return out
}

// configMaxIncludeDepth is git's include depth limit.
const configMaxIncludeDepth = 10

// configValuePath resolves a path-valued config value: unquoted, without a trailing
// comment, "~/" expanded, a relative path taken from base.
func configValuePath(val, base string) string {
	val = strings.TrimSpace(val)
	if quoted, ok := strings.CutPrefix(val, `"`); ok {
		val, _, _ = strings.Cut(quoted, `"`)
	} else if i := strings.IndexAny(val, "#;"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	if rest, ok := strings.CutPrefix(val, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, rest)
	}
	if val != "" && !filepath.IsAbs(val) {
		return filepath.Join(base, val)
	}
	return val
}

// fingerprintWalker walks the directories of one fingerprint with a few workers
// (as git's preloaded index stats in parallel); each directory is digested on its
// own and the digests are combined in path order.
type fingerprintWalker struct {
	ignored *ignoreSet
	entries atomic.Int64 // directory entries still allowed
	dirs    atomic.Int64 // directories still allowed
	mu      sync.Mutex
	newest  int64
	units   []dirUnit
}

// dirUnit is one directory's digest.
type dirUnit struct {
	rel string
	sum [sha256.Size]byte
}

// walkDir is one directory still to be read: its path, its path relative to the
// top-level worktree (the form repo-key's ignored listing uses) and how deep in
// nested worktrees it lies.
type walkDir struct {
	abs, rel string
	nesting  int
}

// fingerprintWorkers is how many directories are read at once.
const fingerprintWorkers = 4

// walk digests every directory git traverses from top. False when a bound is
// exceeded or a directory cannot be read for a reason other than being gone.
func (w *fingerprintWalker) walk(top string) bool {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stack := []walkDir{{abs: top}}
	busy, failed := 0, false
	var wg sync.WaitGroup
	for i := 0; i < fingerprintWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newDigestSink()
			for {
				mu.Lock()
				for len(stack) == 0 && busy > 0 && !failed {
					cond.Wait()
				}
				if failed || len(stack) == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				d := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				busy++
				mu.Unlock()
				subdirs, ok := w.dir(d, s)
				u := dirUnit{rel: d.rel}
				s.h.Sum(u.sum[:0])
				mu.Lock()
				busy--
				if ok {
					stack = append(stack, subdirs...)
				} else {
					failed = true
				}
				mu.Unlock()
				cond.Broadcast()
				if ok {
					w.mu.Lock()
					w.units = append(w.units, u)
					w.newest = newestNs(w.newest, s.newest)
					w.mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return !failed
}

// dir digests one directory listing into s, reading it in batches (so a huge
// directory is never held in memory at once and the entry bound stops the walk
// early), and returns the subdirectories to traverse.
func (w *fingerprintWalker) dir(d walkDir, s *digestSink) ([]walkDir, bool) {
	s.reset()
	fd, err := unix.Open(d.abs, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		// a directory that is gone or replaced is a state, not a failure
		s.str("open", err.Error())
		return nil, true
	}
	f := os.NewFile(uintptr(fd), d.abs)
	defer f.Close()
	// the directory's own stat key: its mtime and ctime move when an entry is added,
	// removed or renamed, so a removal is visible to the racy rule too
	var dst unix.Stat_t
	s.statKey(".", &dst, unix.Fstat(fd, &dst))
	var subdirs []walkDir
	nested := false
	for {
		ents, err := f.ReadDir(fingerprintBatch)
		if w.entries.Add(-int64(len(ents))) < 0 {
			return nil, false
		}
		for _, e := range ents {
			name := e.Name()
			if name == ".git" {
				// the top's own .git is digested as metadata; anywhere else it marks
				// a nested worktree (a submodule or a repository inside the tree)
				nested = d.rel != ""
				continue
			}
			if e.Type().IsDir() {
				rel := name
				if d.rel != "" {
					rel = d.rel + "/" + name
				}
				if d.nesting == 0 && w.ignored.has(rel) {
					s.str(name, "ignored-dir")
					continue
				}
				s.str(name, "dir")
				if w.dirs.Add(-1) < 0 {
					return nil, false
				}
				subdirs = append(subdirs, walkDir{abs: d.abs + "/" + name, rel: rel, nesting: d.nesting})
				continue
			}
			var st unix.Stat_t
			serr := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
			s.statKey(name, &st, serr)
		}
		if err == io.EOF || (err == nil && len(ents) == 0) {
			break
		}
		if err != nil {
			return nil, false
		}
	}
	if nested {
		if d.nesting+1 > fingerprintMaxNesting {
			return nil, false
		}
		gitDir, commonDir, found, err := gitDirsAt(d.abs)
		if err != nil {
			return nil, false
		}
		if found && !s.metadata(d.abs, gitDir, commonDir) {
			return nil, false
		}
		for i := range subdirs {
			subdirs[i].nesting = d.nesting + 1
		}
	}
	return subdirs, true
}
