package daemon

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Platform installs and drives the daemon under one service manager.
type Platform interface {
	// Name is the service manager: launchd or systemd.
	Name() string
	// Defaults are this platform's label and locations for the user whose home is home.
	Defaults(getenv func(string) string, home string) Spec
	// Files renders spec's unit files.
	Files(spec Spec) []File
	// Start (re)loads the written units and (re)starts the daemon. changed lists the
	// unit files whose content this install changed.
	Start(ctx context.Context, run Runner, spec Spec, changed []string) error
	// Stop stops the daemon so that nothing starts it again before the next Start.
	Stop(ctx context.Context, run Runner, spec Spec) error
	// Remove stops the daemon and unregisters its units around deleteFiles.
	Remove(ctx context.Context, run Runner, spec Spec, deleteFiles func() error) error
	// Status is what the service manager says about the daemon.
	Status(ctx context.Context, run Runner, spec Spec) Status
}

// File is one rendered unit file.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Status is the service manager's view of the daemon.
type Status struct {
	Manager string `json:"manager"`
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Socket  string `json:"socket,omitempty"`
}

// Runner runs a service manager command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// For returns the platform of goos (launchd on darwin, systemd on linux) for the user
// uid; false where neither runs.
func For(goos string, uid int) (Platform, bool) {
	switch goos {
	case "darwin":
		return Launchd{UID: uid}, true
	case "linux":
		return Systemd{}, true
	}
	return nil, false
}

// ExecRunner runs commands for real, with the credentials of SecretEnv removed from
// their environment: launchctl and systemctl need none, and systemctl could hand its
// environment to the user manager (import-environment).
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return IsSecretKey(k)
	})
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fields parses "key = value" (launchctl print) or "key=value" (systemctl show) lines,
// keeping the first occurrence of each key.
func fields(out, sep string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), sep)
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, seen := m[k]; !seen {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}
