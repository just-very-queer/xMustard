//go:build unix

package workspaceops

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Spawn-free repository stat fingerprint (PAR-FRESH-02, PAR-RT-12). The identity
// cache reuses a sampled `repo-key` identity only while this fingerprint is unchanged.
// It digests what `git status` would see without running git:
//   - HEAD, the ref it names (loose), and the stat keys of packed-refs / reftable;
//   - the stat key of the index (any stage, commit, checkout or reset rewrites it);
//   - every entry of every directory that holds a tracked file (from the index):
//     name, type, size, mtime, ctime and inode. That covers edits to tracked files
//     and to untracked files beside them, and files added to or removed from those
//     directories (including new untracked directories).
//
// Not covered: in-place edits of files inside directories that hold no tracked file
// (for example an existing untracked tree). The identity TTL bounds that window, and
// every cached observation carries its age. Anything unusual (split or sparse index,
// submodules, an unknown index version, a malformed file, more than the entry
// bounds) makes the fingerprint unavailable, and the caller then samples every time.

const (
	fingerprintMaxIndexEntries = 200_000
	fingerprintMaxDirs         = 50_000
	fingerprintMaxDirEntries   = 400_000
	fingerprintMaxSmallFile    = 4 << 10
	fingerprintMaxIndexBytes   = 256 << 20
	fingerprintRacyWindow      = 2 * time.Second
)

// repoFingerprint is one fingerprint reading. ok is false when it could not be
// computed; git is false when no Git worktree contains the root (digest then only
// proves that is still the case).
type repoFingerprint struct {
	digest string
	ok     bool
	git    bool
}

func fileInodeCtime(fi os.FileInfo) (uint64, int64) {
	if st, ok := fi.Sys().(*unix.Stat_t); ok {
		return uint64(st.Ino), st.Ctim.Nano()
	}
	return 0, 0
}

// gitLayoutFor finds the worktree top, its git dir and the common dir for root,
// the way git discovers them: the nearest .git directory or gitfile upward.
func gitLayoutFor(root string) (top, gitDir, commonDir string, found bool, err error) {
	dir := filepath.Clean(root)
	for {
		p := filepath.Join(dir, ".git")
		fi, lerr := os.Lstat(p)
		switch {
		case lerr == nil && fi.IsDir():
			top, gitDir = dir, p
		case lerr == nil && fi.Mode().IsRegular():
			b, rerr := readSmallFile(p)
			if rerr != nil {
				return "", "", "", false, rerr
			}
			line := strings.TrimSpace(string(b))
			if !strings.HasPrefix(line, "gitdir:") {
				return "", "", "", false, errors.New("unrecognized gitfile")
			}
			gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
			if !filepath.IsAbs(gd) {
				gd = filepath.Join(dir, gd)
			}
			top, gitDir = dir, gd
		case lerr == nil:
			return "", "", "", false, errors.New(".git is neither a directory nor a gitfile")
		case !errors.Is(lerr, os.ErrNotExist):
			return "", "", "", false, lerr
		}
		if gitDir != "" {
			commonDir = gitDir
			if b, cerr := readSmallFile(filepath.Join(gitDir, "commondir")); cerr == nil {
				cd := strings.TrimSpace(string(b))
				if !filepath.IsAbs(cd) {
					cd = filepath.Join(gitDir, cd)
				}
				commonDir = cd
			} else if !errors.Is(cerr, os.ErrNotExist) {
				return "", "", "", false, cerr
			}
			return top, gitDir, commonDir, true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", "", false, nil
		}
		dir = parent
	}
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, fingerprintMaxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > fingerprintMaxSmallFile {
		return nil, errors.New("file too large for a git metadata file")
	}
	return b, nil
}

// writeStatKey adds a stat key (or the reason there is none) to the digest.
func writeStatKey(h hash.Hash, label string, st *unix.Stat_t, err error) {
	if err != nil {
		fmt.Fprintf(h, "%s:err:%v\x00", label, errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT))
		return
	}
	fmt.Fprintf(h, "%s:%o:%d:%d:%d:%d\x00", label, st.Mode, st.Size, st.Mtim.Nano(), st.Ctim.Nano(), st.Ino)
}

func lstatKey(h hash.Hash, label, path string) {
	var st unix.Stat_t
	err := unix.Lstat(path, &st)
	writeStatKey(h, label, &st, err)
}

// repoStatFingerprint computes the fingerprint of the working state at root.
func repoStatFingerprint(root string) repoFingerprint {
	if root == "" {
		return repoFingerprint{}
	}
	top, gitDir, commonDir, found, err := gitLayoutFor(root)
	if err != nil {
		return repoFingerprint{}
	}
	h := sha256.New()
	if !found {
		fmt.Fprintf(h, "nogit\x00%s\x00", root)
		return repoFingerprint{digest: hex.EncodeToString(h.Sum(nil)), ok: true}
	}
	fmt.Fprintf(h, "git\x00%s\x00%s\x00%s\x00", top, gitDir, commonDir)
	head, err := readSmallFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return repoFingerprint{}
	}
	h.Write(head)
	h.Write([]byte{0})
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: "); ok {
		if strings.Contains(ref, "..") || filepath.IsAbs(ref) {
			return repoFingerprint{}
		}
		loose, lerr := readSmallFile(filepath.Join(gitDir, ref))
		if errors.Is(lerr, os.ErrNotExist) && gitDir != commonDir {
			loose, lerr = readSmallFile(filepath.Join(commonDir, ref))
		}
		if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
			return repoFingerprint{}
		}
		h.Write(loose)
		h.Write([]byte{0})
	}
	lstatKey(h, "packed-refs", filepath.Join(commonDir, "packed-refs"))
	lstatKey(h, "reftable", filepath.Join(commonDir, "reftable", "tables.list"))

	dirs, err := trackedDirs(filepath.Join(gitDir, "index"), objectHashLen(commonDir), h)
	if err != nil {
		return repoFingerprint{}
	}
	if !digestDirs(h, top, dirs) {
		return repoFingerprint{}
	}
	return repoFingerprint{digest: hex.EncodeToString(h.Sum(nil)), ok: true, git: true}
}

// objectHashLen is 32 for SHA-256 repositories, else 20.
func objectHashLen(commonDir string) int {
	b, err := os.ReadFile(filepath.Join(commonDir, "config"))
	if err != nil || len(b) > 1<<20 {
		return 20
	}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.ToLower(strings.Join(strings.Fields(line), ""))
		if l == "objectformat=sha256" {
			return 32
		}
	}
	return 20
}

// indexDirs caches the tracked-directory list per index file stat key.
type indexDirsEntry struct {
	key  string
	dirs []string
}

var indexDirsCache = struct {
	sync.Mutex
	m map[string]indexDirsEntry
}{m: map[string]indexDirsEntry{}}

// trackedDirs returns every directory (relative, "" for the top) that holds a
// tracked path, parsed from the index, and adds the index stat key to h. A missing
// index (no commit and nothing staged) yields only the top directory.
func trackedDirs(indexPath string, hashLen int, h hash.Hash) ([]string, error) {
	f, err := os.Open(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprint(h, "index:none\x00")
		return []string{""}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	writeStatKey(h, "index", &st, nil)
	key := fmt.Sprintf("%d:%d:%d:%d:%d", st.Size, st.Mtim.Nano(), st.Ctim.Nano(), st.Ino, st.Dev)
	indexDirsCache.Lock()
	if e, ok := indexDirsCache.m[indexPath]; ok && e.key == key {
		indexDirsCache.Unlock()
		return e.dirs, nil
	}
	indexDirsCache.Unlock()
	if st.Size > fingerprintMaxIndexBytes {
		return nil, errors.New("index too large")
	}
	dirs, err := parseIndexDirs(bufio.NewReaderSize(f, 64<<10), st.Size, hashLen)
	if err != nil {
		return nil, err
	}
	// racy: an index written within the window may be rewritten in the same tick
	if newestNs(st.Mtim.Nano(), st.Ctim.Nano()) < time.Now().Add(-fingerprintRacyWindow).UnixNano() {
		indexDirsCache.Lock()
		if len(indexDirsCache.m) >= 16 {
			clear(indexDirsCache.m)
		}
		indexDirsCache.m[indexPath] = indexDirsEntry{key: key, dirs: dirs}
		indexDirsCache.Unlock()
	}
	return dirs, nil
}

// parseIndexDirs reads a Git index (versions 2-4) and returns the sorted set of
// directories holding its entries. It refuses split and sparse indexes and gitlinks
// (their working state is not visible from this directory walk).
func parseIndexDirs(r *bufio.Reader, size int64, hashLen int) ([]string, error) {
	var consumed int64
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		consumed += int64(n)
		return b, nil
	}
	hdr, err := read(12)
	if err != nil {
		return nil, err
	}
	if string(hdr[:4]) != "DIRC" {
		return nil, errors.New("not a git index")
	}
	version := binary.BigEndian.Uint32(hdr[4:8])
	count := binary.BigEndian.Uint32(hdr[8:12])
	if version < 2 || version > 4 {
		return nil, fmt.Errorf("unsupported index version %d", version)
	}
	if count > fingerprintMaxIndexEntries {
		return nil, errors.New("index has too many entries")
	}
	dirSet := map[string]struct{}{"": {}}
	var prev []byte
	fixed := 40 + hashLen + 2
	for i := uint32(0); i < count; i++ {
		meta, err := read(fixed)
		if err != nil {
			return nil, err
		}
		mode := binary.BigEndian.Uint32(meta[24:28])
		flags := binary.BigEndian.Uint16(meta[fixed-2:])
		entryLen := fixed
		if flags&0x4000 != 0 {
			if version < 3 {
				return nil, errors.New("extended flag in a version 2 index")
			}
			if _, err := read(2); err != nil {
				return nil, err
			}
			entryLen += 2
		}
		var name []byte
		if version == 4 {
			strip, n, err := readIndexVarint(r)
			if err != nil {
				return nil, err
			}
			consumed += int64(n)
			suffix, err := r.ReadBytes(0)
			if err != nil {
				return nil, err
			}
			consumed += int64(len(suffix))
			if strip > uint64(len(prev)) {
				return nil, errors.New("malformed index path prefix")
			}
			name = append(append([]byte{}, prev[:len(prev)-int(strip)]...), suffix[:len(suffix)-1]...)
		} else {
			nameLen := int(flags & 0x0fff)
			read0 := 0 // NUL bytes already consumed after the name
			if nameLen < 0x0fff {
				if name, err = read(nameLen); err != nil {
					return nil, err
				}
			} else {
				// a long name is NUL-terminated inside the padding
				b, err := r.ReadBytes(0)
				if err != nil {
					return nil, err
				}
				consumed += int64(len(b))
				name, read0 = b[:len(b)-1], 1
			}
			// entries are NUL-padded to a multiple of 8, with at least one NUL
			total := (entryLen + len(name) + 8) &^ 7
			if pad := total - entryLen - len(name) - read0; pad > 0 {
				if _, err := read(pad); err != nil {
					return nil, err
				}
			}
		}
		switch mode & 0o170000 {
		case 0o160000:
			return nil, errors.New("index has a submodule (gitlink)")
		case 0o040000:
			return nil, errors.New("sparse index directory entry")
		}
		p := string(name)
		if p == "" || strings.HasPrefix(p, "/") || slices.Contains(strings.Split(p, "/"), "..") {
			return nil, errors.New("unsafe index path")
		}
		for d := pathDir(p); ; d = pathDir(d) {
			if _, seen := dirSet[d]; seen {
				break
			}
			dirSet[d] = struct{}{}
			if len(dirSet) > fingerprintMaxDirs {
				return nil, errors.New("too many tracked directories")
			}
		}
		prev = name
	}
	// extensions, then the trailing checksum
	for consumed+int64(hashLen) < size {
		ext, err := read(8)
		if err != nil {
			return nil, err
		}
		switch string(ext[:4]) {
		case "link":
			return nil, errors.New("split index")
		case "sdir":
			return nil, errors.New("sparse index")
		}
		n := int64(binary.BigEndian.Uint32(ext[4:8]))
		if consumed+n+int64(hashLen) > size {
			return nil, errors.New("malformed index extension")
		}
		if _, err := r.Discard(int(n)); err != nil {
			return nil, err
		}
		consumed += n
	}
	if consumed+int64(hashLen) != size {
		return nil, errors.New("malformed index")
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	return dirs, nil
}

// pathDir is path.Dir for index paths, with "" for the top directory.
func pathDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// readIndexVarint decodes Git's offset varint (index v4 path prefix length).
func readIndexVarint(r *bufio.Reader) (uint64, int, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	n := 1
	val := uint64(c & 0x7f)
	for c&0x80 != 0 {
		if c, err = r.ReadByte(); err != nil {
			return 0, 0, err
		}
		n++
		if n > 10 {
			return 0, 0, errors.New("index varint overflow")
		}
		val = ((val + 1) << 7) | uint64(c&0x7f)
	}
	return val, n, nil
}

// digestDirs adds every entry of every tracked directory to h. False when a bound
// is exceeded or a directory cannot be read for a reason other than being gone.
func digestDirs(h hash.Hash, top string, dirs []string) bool {
	total := 0
	for _, rel := range dirs {
		dir := top
		if rel != "" {
			dir = filepath.Join(top, filepath.FromSlash(rel))
		}
		fmt.Fprintf(h, "dir\x00%s\x00", rel)
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			// a tracked directory that is gone or replaced is a state, not a failure
			fmt.Fprintf(h, "open:%v\x00", err)
			continue
		}
		f := os.NewFile(uintptr(fd), dir)
		names, err := f.Readdirnames(-1)
		if err != nil {
			f.Close()
			return false
		}
		total += len(names)
		if total > fingerprintMaxDirEntries {
			f.Close()
			return false
		}
		slices.Sort(names)
		for _, name := range names {
			if name == ".git" {
				continue
			}
			var st unix.Stat_t
			serr := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
			writeStatKey(h, name, &st, serr)
		}
		f.Close()
	}
	return true
}
