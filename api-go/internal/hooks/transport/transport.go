// Package transport is what the hook daemon and the static hook client
// (cmd/xmustard-hook) agree on: where the Unix socket is, when it may be trusted, and
// which header pins a workspace. It imports nothing beyond the standard library's os,
// path and syscall handling, so the client stays small.
package transport

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SocketEnv names the Unix socket the daemon serves the hook routes on; "off"
// disables it.
const SocketEnv = "XMUSTARD_HOOK_SOCKET"

// WorkspaceHeader pins a hook call to a workspace id (from XMUSTARD_WORKSPACE_ID);
// without it the daemon resolves the registered root that holds the event's cwd.
const WorkspaceHeader = "X-Xmustard-Workspace"

// SocketPath is the hook socket: SocketEnv, else xmustard/hook.sock under
// $XDG_RUNTIME_DIR, else under a per-user directory of the OS temp dir; "" when
// disabled. getenv is os.Getenv outside tests.
func SocketPath(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv(SocketEnv)); v != "" {
		if strings.EqualFold(v, "off") {
			return ""
		}
		return v
	}
	if d := getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "xmustard", "hook.sock")
	}
	return filepath.Join(os.TempDir(), "xmustard-"+strconv.Itoa(os.Getuid()), "hook.sock")
}

// CheckDir fails unless dir is a private directory of this user: a directory (not a
// symlink to one), owned by this user, that no other user can enter. Another local
// user can create the default directory under a shared temp dir first; neither side
// then uses it (the daemon does not listen there, the client does not send its token
// there).
func CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New(dir + " is not a directory")
	}
	if !ownedByMe(fi) {
		return errors.New(dir + " is not owned by this user")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New(dir + " is open to other users (it must be mode 0700)")
	}
	return nil
}

// CheckSocket fails unless path is a Unix socket owned by this user in a directory
// CheckDir accepts. Once the directory is private, no other user can replace the
// socket between this check and a dial or a listen.
func CheckSocket(path string) error {
	if err := CheckDir(filepath.Dir(path)); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Type() != os.ModeSocket || !ownedByMe(fi) {
		return errors.New(path + " is not a socket owned by this user")
	}
	return nil
}
