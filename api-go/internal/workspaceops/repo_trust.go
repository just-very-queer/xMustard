package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const repoTrustFile = "trusted-repositories.json"

type repoTrustList struct {
	Roots []string `json:"roots"`
}

// CanonicalRepoRoot returns the resolved Git worktree root, not the caller's subdirectory.
func CanonicalRepoRoot(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root must be an existing directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", resolved, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("path is not inside a Git worktree")
	}
	worktree, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return "", err
	}
	return filepath.Clean(worktree), nil
}

func RepoTrustPath(dataDir string) string {
	return filepath.Join(dataDir, repoTrustFile)
}

func ListRepoTrust(dataDir string) ([]string, error) {
	entries, err := readRepoTrust(dataDir)
	if err != nil {
		return nil, err
	}
	return entries.Roots, nil
}

func RepoTrusted(dataDir, root string) (bool, error) {
	canonical, err := CanonicalRepoRoot(root)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dataDir); err == nil {
		if err := ensureTrustStoreOutsideRepo(dataDir, canonical); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := readRepoTrust(dataDir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries.Roots {
		if entry == canonical {
			return true, nil
		}
	}
	return false, nil
}

func UpdateRepoTrust(dataDir, root string, grant bool) ([]string, error) {
	canonical, err := CanonicalRepoRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := ensureTrustStoreOutsideRepo(dataDir, canonical); err != nil {
		return nil, err
	}
	entries, err := readRepoTrust(dataDir)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(entries.Roots)+1)
	for _, entry := range entries.Roots {
		if filepath.IsAbs(entry) && filepath.Clean(entry) == entry {
			set[entry] = true
		}
	}
	if grant {
		set[canonical] = true
	} else {
		delete(set, canonical)
	}
	roots := make([]string, 0, len(set))
	for entry := range set {
		roots = append(roots, entry)
	}
	sort.Strings(roots)
	encoded, err := json.Marshal(repoTrustList{Roots: roots})
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dataDir, ".trust-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(append(encoded, '\n')); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, RepoTrustPath(dataDir)); err != nil {
		return nil, err
	}
	return roots, nil
}

func ensureTrustStoreOutsideRepo(dataDir, root string) error {
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	canonicalDataDir, err := filepath.EvalSymlinks(absoluteDataDir)
	if err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalDataDir)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("repository trust file must be outside the repository")
	}
	return nil
}

func readRepoTrust(dataDir string) (repoTrustList, error) {
	path := RepoTrustPath(dataDir)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return repoTrustList{}, nil
	}
	if err != nil {
		return repoTrustList{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return repoTrustList{}, errors.New("repository trust file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return repoTrustList{}, err
	}
	var trust repoTrustList
	if err := json.Unmarshal(data, &trust); err != nil {
		return repoTrustList{}, err
	}
	for _, root := range trust.Roots {
		if strings.TrimSpace(root) != root || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return repoTrustList{}, errors.New("repository trust file contains an invalid root")
		}
	}
	return trust, nil
}
