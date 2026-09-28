// Package transport is what the hook daemon and the static hook client
// (cmd/xmustard-hook) agree on: where the Unix socket is and which header pins a
// workspace. It imports nothing beyond the standard library's os and path handling,
// so the client stays small.
package transport

import (
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
