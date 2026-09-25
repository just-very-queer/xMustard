//go:build !unix

package workspaceops

import "os"

// fingerprintSupported: off unix the identity cache samples `repo-key` on every
// observation, as without it.
const fingerprintSupported = false

// statMark reads a registry source file's stat identity (size and mtime only here).
func statMark(path string) (fileMark, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileMark{}, err
	}
	return fileMark{exists: true, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}, nil
}

func repoStatFingerprint(root string, ignored *ignoreSet) repoFingerprint { return repoFingerprint{} }
