package workspaceops

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Scoped workspace registration. Loading a workspace scans the directory into
// xMustard's store, and every token that can reach the workspace can then read it
// through search, explain and ground. An admin may register any directory. A
// non-admin principal (typically an agent whose MCP shim auto-registers the
// repository it runs in) may register only the top level of a git work tree that
// resolves, with symlinks evaluated, at or below a registration root the operator
// configured (XMUSTARD_REGISTER_ROOTS). With no roots configured, only admins
// register.

// Why a non-admin registration was refused (RegistrationRefusal.Code).
const (
	RefusalNoRegisterRoots = "no_register_roots" // the operator configured no roots
	RefusalInvalidPath     = "invalid_path"      // empty, relative or malformed
	RefusalOutsideRoots    = "outside_register_roots"
	RefusalUnresolvable    = "unresolvable"      // inside a root, but missing or unreadable
	RefusalGitInternals    = "git_internals"     // a .git directory or a path inside one
	RefusalBareRepository  = "bare_repository"   // a git directory with no work tree
	RefusalNotTopLevel     = "not_work_tree_top" // a subdirectory of a work tree
	RefusalNotGitWorkTree  = "not_git_work_tree" // no git work tree here at all
)

// RegistrationRefusal is a refused non-admin registration: a stable code and a
// message the caller can act on.
type RegistrationRefusal struct {
	Code    string
	Message string
}

func (e *RegistrationRefusal) Error() string { return e.Message }

func refuse(code, format string, args ...any) (string, string, error) {
	return "", "", &RegistrationRefusal{Code: code, Message: fmt.Sprintf(format, args...)}
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

// CheckRegistrationRoot decides whether a non-admin principal may register
// rootPath. It returns the path to register (absolute, symlinks resolved, so the
// registry never stores a link that could later be repointed) and the registration
// root (resolved) that admits it. A refusal is a *RegistrationRefusal.
//
// Containment is checked before anything else is read, and a path outside every
// root is refused with the same answer whether or not it exists, so the check does
// not reveal the layout of directories an agent could not register.
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
	// lexically under a root, as configured or resolved: only then may the answer
	// describe the path (whether it exists, where it resolves)
	lexical := underAny(clean, roots) != "" || underAny(clean, resolvedRoots) != ""
	outside := func() (string, string, error) {
		return refuse(RefusalOutsideRoots, "outside every registration root (XMUSTARD_REGISTER_ROOTS), so an admin must register %s", requested)
	}
	if hasGitComponent(clean) {
		if !lexical {
			return outside()
		}
		return refuse(RefusalGitInternals, "%s is inside a .git directory; register the work tree's top level", requested)
	}
	resolved, rerr := filepath.EvalSymlinks(clean)
	if rerr != nil {
		if !lexical {
			return outside()
		}
		return refuse(RefusalUnresolvable, "%s does not exist or cannot be resolved", requested)
	}
	admitted := underAny(resolved, resolvedRoots)
	if admitted == "" {
		if lexical && resolved != clean {
			return refuse(RefusalOutsideRoots, "outside every registration root (XMUSTARD_REGISTER_ROOTS): %s resolves to %s", requested, resolved)
		}
		return outside()
	}
	if hasGitComponent(resolved) {
		return refuse(RefusalGitInternals, "%s resolves to %s, inside a .git directory; register the work tree's top level", requested, resolved)
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return refuse(RefusalNotGitWorkTree, "%s is not a directory", requested)
	}
	switch dotGit, err := os.Stat(filepath.Join(resolved, ".git")); {
	case err == nil && dotGit.IsDir():
		if !isGitDir(filepath.Join(resolved, ".git")) {
			return refuse(RefusalNotGitWorkTree, "%s has a .git directory that is not a git repository (no HEAD or objects)", requested)
		}
	case err == nil && dotGit.Mode().IsRegular():
		if !isGitdirFile(filepath.Join(resolved, ".git")) {
			return refuse(RefusalNotGitWorkTree, "%s has a .git file that does not name a git directory (gitdir: ...)", requested)
		}
	case err == nil:
		return refuse(RefusalNotGitWorkTree, "%s/.git is neither a directory nor a gitdir file", requested)
	case isGitDir(resolved):
		return refuse(RefusalBareRepository, "%s is a bare repository or a git directory; register a work tree", requested)
	default:
		if top := enclosingWorkTree(resolved, admitted); top != "" {
			return refuse(RefusalNotTopLevel, "%s is inside the git work tree %s; register its top level", requested, top)
		}
		return refuse(RefusalNotGitWorkTree, "%s is not the top level of a git work tree", requested)
	}
	return resolved, admitted, nil
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

// isGitdirFile reports whether a .git file points at a git directory, as linked
// worktrees and submodules do ("gitdir: <path>").
func isGitdirFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, 512))
	return err == nil && bytes.HasPrefix(head, []byte("gitdir: "))
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
