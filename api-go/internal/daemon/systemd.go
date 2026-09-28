package daemon

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Systemd is a pair of systemd user units: a socket on the loopback port and the
// service it activates. The first connection (a hook, an MCP client, setup's health
// probe) starts the daemon, and connections made while it restarts, after a crash or
// for an upgrade, wait in the socket's backlog instead of being refused.
type Systemd struct{}

// Name is the service manager's name.
func (Systemd) Name() string { return "systemd" }

// Defaults follow the XDG base directories.
func (Systemd) Defaults(getenv func(string) string, home string) Spec {
	xdg := func(key, fallback string) string {
		if v := getenv(key); filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	return Spec{
		Label:   "xmustard-api",
		UnitDir: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user"),
		DataDir: filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "xmustard"),
		LogFile: filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(".local", "state")), "xmustard", "api.log"),
		Port:    8042,
	}
}

func (Systemd) service(s Spec) string { return s.Label + ".service" }
func (Systemd) socket(s Spec) string  { return s.Label + ".socket" }

var (
	// specifiers (%) are expanded in every setting. Paths hold no quote or backslash
	// (Spec.Validate), and an executable path is not $-expanded.
	pathQuote = strings.NewReplacer("%", "%%")
	// a quoted Environment= assignment takes C escapes
	envQuote = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")
)

// Files renders the service and socket units.
func (d Systemd) Files(s Spec) []File {
	var env strings.Builder
	for _, e := range s.Environ() {
		fmt.Fprintf(&env, "Environment=\"%s=%s\"\n", e.Key, envQuote.Replace(e.Value))
	}
	service := fmt.Sprintf(`# %[1]s: rerun setup instead of editing this file
[Unit]
Description=xMustard API daemon (MCP over HTTP, governed memory)
Requires=%[2]s
After=%[2]s
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=simple
ExecStart="%[3]s"
WorkingDirectory=%[4]s
%[5]sUnsetEnvironment=%[6]s
# restart after a crash or a failed exit, not after a clean stop
Restart=on-failure
RestartSec=2
# the daemon drains for up to 25 s (in-flight requests, then runs) before it exits
TimeoutStopSec=30
KillMode=mixed
UMask=0077
NoNewPrivileges=yes
`, Marker, d.socket(s), pathQuote.Replace(s.APIBin), pathQuote.Replace(s.DataDir), env.String(), strings.Join(SecretEnv, " "))
	socket := fmt.Sprintf(`# %[1]s: rerun setup instead of editing this file
[Unit]
Description=xMustard API socket (starts the daemon on the first connection)

[Socket]
ListenStream=%[2]s
NoDelay=true

[Install]
WantedBy=sockets.target
`, Marker, net.JoinHostPort(LoopbackHost, strconv.Itoa(s.Port)))
	return []File{
		{Path: filepath.Join(s.UnitDir, d.service(s)), Content: service},
		{Path: filepath.Join(s.UnitDir, d.socket(s)), Content: socket},
	}
}

// Start reloads the units, enables the socket and (re)starts the daemon behind it.
func (d Systemd) Start(ctx context.Context, run Runner, s Spec, changed []string) error {
	steps := [][]string{{"daemon-reload"}, {"enable", d.socket(s)}}
	if slices.Contains(changed, filepath.Join(s.UnitDir, d.socket(s))) {
		// a new or changed socket is rebound; systemd refuses to restart a socket while
		// its service runs, so the service stops first and starts on the next connection
		steps = append(steps, []string{"stop", d.service(s)}, []string{"restart", d.socket(s)})
	} else {
		// the socket keeps listening while the service restarts on the new binary
		steps = append(steps, []string{"start", d.socket(s)}, []string{"try-restart", d.service(s)})
	}
	return systemctl(ctx, run, steps...)
}

// Stop stops the socket and the service, so no connection starts the daemon again.
func (d Systemd) Stop(ctx context.Context, run Runner, s Spec) error {
	return systemctl(ctx, run, []string{"stop", d.socket(s), d.service(s)})
}

// Remove disables and stops both units, deletes them and reloads the manager.
func (d Systemd) Remove(ctx context.Context, run Runner, s Spec, deleteFiles func() error) error {
	if err := systemctl(ctx, run, []string{"disable", "--now", d.socket(s)}, []string{"stop", d.service(s)}); err != nil {
		return err
	}
	if err := deleteFiles(); err != nil {
		return err
	}
	return systemctl(ctx, run, []string{"daemon-reload"})
}

// Status reads `systemctl --user show` for the service and the socket.
func (d Systemd) Status(ctx context.Context, run Runner, s Spec) Status {
	show := func(unit string) map[string]string {
		out, err := run(ctx, "systemctl", "--user", "show", "--property=LoadState,ActiveState,SubState,MainPID", unit)
		if err != nil {
			return map[string]string{}
		}
		return fields(out, "=")
	}
	svc, sock := show(d.service(s)), show(d.socket(s))
	st := Status{Manager: d.Name(), State: svc["ActiveState"] + "/" + svc["SubState"], Socket: sock["ActiveState"]}
	if svc["LoadState"] != "loaded" {
		st.State = "not-loaded"
	}
	st.PID, _ = strconv.Atoi(svc["MainPID"])
	return st
}

func systemctl(ctx context.Context, run Runner, steps ...[]string) error {
	for _, step := range steps {
		if out, err := run(ctx, "systemctl", append([]string{"--user"}, step...)...); err != nil {
			return fmt.Errorf("systemctl --user %s: %v: %s", strings.Join(step, " "), err, strings.TrimSpace(out))
		}
	}
	return nil
}
