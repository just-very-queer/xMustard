package workspaceops

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Scoped workspace registration. Loading a workspace scans the directory into
// xMustard's store, and every token that can reach the workspace can then read it
// through search, explain and ground. An admin may register any directory. A
// non-admin principal (typically an agent whose MCP shim auto-registers the
// repository it runs in) may register only the top level of a git work tree that
// resolves, with symlinks evaluated, at or below a registration root the operator
// configured (XMUSTARD_REGISTER_ROOTS), and whose git directory lies below that
// root too. With no roots configured, only admins register.
//
// The checks hold at registration, but the agent can still write the directories
// involved afterwards. So the registry entry records the registration root, and
// every later use of the workspace's root re-runs the checks the filesystem can
// invalidate (verifyRegisteredRoot).

// Why a non-admin registration was refused (RegistrationRefusal.Code).
const (
	RefusalNoRegisterRoots = "no_register_roots" // the operator configured no roots
	RefusalInvalidPath     = "invalid_path"      // empty, relative or malformed
	// missing, or outside every root once symlinks are resolved: one answer for both
	RefusalOutsideRoots   = "outside_register_roots"
	RefusalGitInternals   = "git_internals"     // a .git directory or a path inside one
	RefusalBareRepository = "bare_repository"   // a git directory with no work tree
	RefusalNotTopLevel    = "not_work_tree_top" // a subdirectory of a work tree
	RefusalNotGitWorkTree = "not_git_work_tree" // no work tree here, or its .git leads outside the root
	RefusalRegisterLimit  = "register_limit"    // the principal holds XMUSTARD_REGISTER_LIMIT workspaces
)

// RegistrationRefusal is a refused non-admin registration: a stable code and a
// message the caller can act on.
type RegistrationRefusal struct {
	Code    string
	Message string
}

func (e *RegistrationRefusal) Error() string { return e.Message }

func refusal(code, format string, args ...any) *RegistrationRefusal {
	return &RegistrationRefusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

func refuse(code, format string, args ...any) (string, string, error) {
	return "", "", refusal(code, format, args...)
}

// ParseRegisterRoots parses XMUSTARD_REGISTER_ROOTS, an OS path list (":" between
// entries on Unix, ";" on Windows). Every entry must be an absolute directory path;
// a filesystem root is refused because it would admit every repository on the
// host. Entries are cleaned and deduplicated; symlinks are resolved when a
// registration is checked, so a root created after startup still works.
func ParseRegisterRoots(list string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, entry := range filepath.SplitList(list) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.ContainsRune(entry, 0) || !filepath.IsAbs(entry) {
			return nil, fmt.Errorf("XMUSTARD_REGISTER_ROOTS: %q must be an absolute directory path", entry)
		}
		clean := filepath.Clean(entry)
		if filepath.Dir(clean) == clean {
			return nil, fmt.Errorf("XMUSTARD_REGISTER_ROOTS: %q is a filesystem root and would let any agent register any repository; name the directories that hold checkouts", entry)
		}
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out, nil
}

// DefaultRegisterLimit is how many workspaces one non-admin principal may register
// when XMUSTARD_REGISTER_LIMIT is unset. The registry is read whole on most
// workspace requests, and each registration scans a repository, so an agent token
// must not add entries without bound.
const DefaultRegisterLimit = 50

// ParseRegisterLimit parses XMUSTARD_REGISTER_LIMIT: unset means
// DefaultRegisterLimit, anything else must be a positive integer.
func ParseRegisterLimit(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultRegisterLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("XMUSTARD_REGISTER_LIMIT: %q must be a positive integer (workspaces one non-admin principal may register)", v)
	}
	return n, nil
}

// CheckRegistrationRoot decides whether a non-admin principal may register
// rootPath. It returns the path to register (absolute, symlinks resolved, so the
// registry never stores a link that could later be repointed) and the registration
// root (resolved) that admits it. A refusal is a *RegistrationRefusal.
//
// Only a path that resolves inside a root is described. Every other path, whether
// it is missing, lies outside, or leads outside through a symlink planted inside a
// root, gets the same answer, which never names where the path leads: the check is
// not a probe of the host filesystem.
func CheckRegistrationRoot(rootPath string, roots []string) (path, root string, err error) {
	requested := strings.TrimSpace(rootPath)
	if len(roots) == 0 {
		return refuse(RefusalNoRegisterRoots, "non-admin registration is off: the operator has configured no registration roots (XMUSTARD_REGISTER_ROOTS), so an admin must register %s", requested)
	}
	if requested == "" || strings.ContainsRune(requested, 0) || !filepath.IsAbs(requested) {
		return refuse(RefusalInvalidPath, "root_path %q must be an absolute path", requested)
	}
	clean := filepath.Clean(requested)
	resolvedRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			resolvedRoots = append(resolvedRoots, rr)
		}
	}
	lexical := underAny(clean, roots) != "" || underAny(clean, resolvedRoots) != ""
	if lexical && hasGitComponent(clean) {
		return refuse(RefusalGitInternals, "%s is inside a .git directory; register the work tree's top level", requested)
	}
	resolved, rerr := filepath.EvalSymlinks(clean)
	admitted := ""
	if rerr == nil {
		admitted = underAny(resolved, resolvedRoots)
	}
	if admitted == "" {
		return refuse(RefusalOutsideRoots, "%s does not resolve to an existing directory inside a registration root (XMUSTARD_REGISTER_ROOTS); an admin must register anything else", requested)
	}
	if hasGitComponent(resolved) {
		return refuse(RefusalGitInternals, "%s resolves inside a .git directory; register the work tree's top level", requested)
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return refuse(RefusalNotGitWorkTree, "%s is not a directory", requested)
	}
	if r := checkWorkTreeTop(resolved, admitted, requested); r != nil {
		return "", "", r
	}
	return resolved, admitted, nil
}

// checkWorkTreeTop decides whether dir (resolved, inside root) is the top level of
// a git work tree whose git directory also lies inside root. git follows a .git
// file or symlink wherever it points, so a .git leading to a repository elsewhere
// would hand that repository's file list, refs and history to every agent.
func checkWorkTreeTop(dir, root, requested string) *RegistrationRefusal {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		gitDir, ok := gitDirOf(dir)
		if !ok {
			return refusal(RefusalNotGitWorkTree, "%s/.git is neither a git directory nor a gitdir file", requested)
		}
		if !repositoryInside(gitDir, root) {
			return refusal(RefusalNotGitWorkTree, "%s/.git does not lead to a git repository inside its registration root", requested)
		}
		return nil
	}
	if isGitDir(dir) {
		return refusal(RefusalBareRepository, "%s is a bare repository or a git directory; register a work tree", requested)
	}
	if top := enclosingWorkTree(dir, root); top != "" {
		return refusal(RefusalNotTopLevel, "%s is inside the git work tree %s; register its top level", requested, top)
	}
	return refusal(RefusalNotGitWorkTree, "%s is not the top level of a git work tree", requested)
}

// verifyRegisteredRoot re-runs, for a root a non-admin registered, the checks the
// filesystem can invalidate after registration: the root must still resolve to
// itself (no element swapped for a symlink) and its .git must still lead to a git
// repository inside the registration root. Reading a root that fails would follow
// the swap to wherever it leads. A root an admin or open mode registered is used as
// registered. A swap timed between this check and the read that follows it is not
// caught (docs/SECURITY.md).
func verifyRegisteredRoot(w workspaceRecord) error {
	if w.RegisterRoot == "" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(w.RootPath)
	ok := err == nil && resolved == w.RootPath && !escapesRoot(w.RegisterRoot, resolved)
	if ok {
		gitDir, found := gitDirOf(resolved)
		ok = found && repositoryInside(gitDir, w.RegisterRoot)
	}
	if !ok {
		return Conflict(fmt.Sprintf("workspace %s was registered by a non-admin token at %s, which no longer resolves to that git work tree inside %s (a symlink or .git now leads elsewhere); xMustard will not read it until the directory is restored", w.WorkspaceID, w.RootPath, w.RegisterRoot))
	}
	return nil
}

// underAny returns the first root that p is at or below ("" for none).
func underAny(p string, roots []string) string {
	for _, r := range roots {
		if !escapesRoot(r, p) {
			return r
		}
	}
	return ""
}

// hasGitComponent reports whether any element of p is ".git" (case-insensitively,
// for case-insensitive filesystems).
func hasGitComponent(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

// gitDirOf returns the git directory workTree's .git entry names, unresolved: the
// directory itself, a symlink, or the path a "gitdir: " file names (relative to the
// work tree). ok is false when .git is none of these.
func gitDirOf(workTree string) (string, bool) {
	dotGit := filepath.Join(workTree, ".git")
	info, err := os.Lstat(dotGit)
	switch {
	case err != nil:
		return "", false
	case info.IsDir(), info.Mode()&os.ModeSymlink != 0:
		return dotGit, true
	case info.Mode().IsRegular():
		target, ok := readPointer(dotGit, "gitdir: ")
		if !ok {
			return "", false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(workTree, target)
		}
		return target, true
	}
	return "", false
}

// repositoryInside reports whether gitDir resolves, inside root, to a git
// directory: HEAD, plus objects either there or, for a linked worktree's git
// directory, in the common directory its commondir file names (also inside root).
func repositoryInside(gitDir, root string) bool {
	dir, ok := resolveInside(gitDir, root)
	if !ok {
		return false
	}
	if head, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || !head.Mode().IsRegular() {
		return false
	}
	common := dir
	if target, ok := readPointer(filepath.Join(dir, "commondir"), ""); ok {
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		if common, ok = resolveInside(target, root); !ok {
			return false
		}
	}
	return isGitDir(common)
}

// resolveInside resolves p and reports whether the result lies at or below root.
func resolveInside(p, root string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || escapesRoot(root, resolved) {
		return "", false
	}
	return resolved, true
}

// isGitDir reports whether dir has the layout of a git directory: a HEAD file and
// an objects directory.
func isGitDir(dir string) bool {
	head, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	objects, err := os.Stat(filepath.Join(dir, "objects"))
	return err == nil && objects.IsDir()
}

// readPointer reads the path a small git pointer file holds after prefix (".git"'s
// "gitdir: ", or commondir's bare path). It opens without blocking and reads only a
// regular file, so a FIFO planted in its place cannot stall the request.
func readPointer(path, prefix string) (string, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	head, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil || !bytes.HasPrefix(head, []byte(prefix)) {
		return "", false
	}
	line, _, _ := strings.Cut(string(head[len(prefix):]), "\n")
	line = strings.TrimSpace(line)
	return line, line != "" && !strings.ContainsRune(line, 0)
}

// enclosingWorkTree walks up from p, not above root, to the nearest directory that
// holds .git: the work tree p is a subdirectory of ("" for none).
func enclosingWorkTree(p, root string) string {
	for dir := filepath.Dir(p); !escapesRoot(root, dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if dir == root || filepath.Dir(dir) == dir {
			break
		}
	}
	return ""
}
