//go:build !unix

package workspaceops

import "os"

// repoFingerprint is one fingerprint reading (see repo_stat_unix.go).
type repoFingerprint struct {
	digest string
	ok     bool
	git    bool
}

func fileInodeCtime(fi os.FileInfo) (uint64, int64) { return 0, 0 }

// repoStatFingerprint is unavailable off unix: the identity cache then samples
// `repo-key` on every call, as before.
func repoStatFingerprint(root string) repoFingerprint { return repoFingerprint{} }
