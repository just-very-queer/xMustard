package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"xmustard/api-go/internal/daemon"
	"xmustard/api-go/internal/govstore"
	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// The daemon lifecycle (WS-58): setup installs the API as a per-user service (a launchd
// agent on macOS; a systemd socket and service on Linux), starts it and waits until it
// answers /api/health; the store commands back up, check and restore the governance
// store.
//
//	xmustard-ops setup [--label L] [--data-dir DIR] [--port N] [--api-bin PATH] [--core-bin PATH]
//	    [--log-file PATH] [--unit-dir DIR] [--env KEY=VALUE]... [--wait 30s]
//	    [--root /abs/repo [--client NAME]] [--print [--platform darwin|linux]]
//	xmustard-ops uninstall [--label L] [--unit-dir DIR]
//	xmustard-ops daemon <status|restart|stop> [--label L] [--unit-dir DIR] [--port N] [--wait 30s]
//	xmustard-ops store backup [--data-dir DIR | --file PATH] [--out PATH]
//	xmustard-ops store check [--data-dir DIR | --file PATH]
//	xmustard-ops store restore <backup> [--data-dir DIR] [--label L] [--unit-dir DIR] [--port N] [--wait 30s]
//
// Rerunning setup after an upgrade restarts the daemon on the new binaries; the new
// daemon migrates the store as it starts, and connected MCP clients carry on (the relay
// waits for the restarted API and replays its initialize; under socket activation the
// socket keeps accepting). No unit carries a token: they live hashed in the data dir.

// lifecycleEnv is what the lifecycle commands use, so tests can drive them without a
// service manager, a daemon or the real home directory.
type lifecycleEnv struct {
	opsEnv
	goos     string
	uid      int
	run      daemon.Runner
	probe    func(context.Context, string) (daemon.Health, error)
	exe      string // this executable: its directory holds the sibling binaries
	lookPath func(string) (string, error)
	now      func() time.Time
}

func defaultLifecycleEnv() lifecycleEnv {
	exe, _ := os.Executable()
	return lifecycleEnv{opsEnv: defaultOpsEnv(), goos: runtime.GOOS, uid: os.Getuid(), run: daemon.ExecRunner,
		probe: daemon.Probe, exe: exe, lookPath: exec.LookPath, now: time.Now}
}

// lifecycleCommands are the subcommands this file adds to xmustard-ops.
var lifecycleCommands = map[string]func(lifecycleEnv, []string) int{
	"setup":     runSetup,
	"uninstall": runUninstall,
	"daemon":    runDaemon,
	"store":     runStore,
}

// placeFlags locate an installation; the unset ones take the platform's defaults.
type placeFlags struct {
	label, unitDir, dataDir, logFile, platform string
	port                                       int
	wait                                       time.Duration
}

func (e lifecycleEnv) flagSet(name string) (*flag.FlagSet, *placeFlags) {
	fs := flag.NewFlagSet("xmustard-ops "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	f := &placeFlags{}
	fs.StringVar(&f.label, "label", "", "launchd label or systemd unit name (default com.xmustard.api on macOS, xmustard-api on Linux)")
	fs.StringVar(&f.unitDir, "unit-dir", "", "directory of the unit files (default ~/Library/LaunchAgents, or ~/.config/systemd/user)")
	fs.StringVar(&f.dataDir, "data-dir", "", "the daemon's data dir (default $XMUSTARD_DATA_DIR, else the platform's application data dir)")
	fs.IntVar(&f.port, "port", 0, "loopback port the daemon serves (default 8042)")
	fs.DurationVar(&f.wait, "wait", 30*time.Second, "how long to wait for the daemon to answer its health probe")
	return fs, f
}

// resolve picks the platform and fills the unset flags with its defaults.
func (e lifecycleEnv) resolve(f *placeFlags) (daemon.Platform, daemon.Spec, error) {
	goos := cmp.Or(f.platform, e.goos)
	p, ok := daemon.For(goos, e.uid)
	if !ok {
		return nil, daemon.Spec{}, fmt.Errorf("no supported service manager on %s (launchd on darwin, systemd on linux); run xmustard-api under your own supervisor", goos)
	}
	home := e.getenv("HOME")
	if !filepath.IsAbs(home) {
		return nil, daemon.Spec{}, errors.New("HOME must be an absolute path")
	}
	d := p.Defaults(e.getenv, home)
	if env := e.getenv("XMUSTARD_DATA_DIR"); filepath.IsAbs(env) {
		d.DataDir = env
	}
	s := d
	s.Label, s.Port = cmp.Or(f.label, d.Label), cmp.Or(f.port, d.Port)
	for _, v := range []struct {
		dst        *string
		flag, dflt string
	}{{&s.UnitDir, f.unitDir, d.UnitDir}, {&s.DataDir, f.dataDir, d.DataDir}, {&s.LogFile, f.logFile, d.LogFile}} {
		abs, err := filepath.Abs(cmp.Or(v.flag, v.dflt))
		if err != nil {
			return nil, daemon.Spec{}, err
		}
		*v.dst = abs
	}
	return p, s, nil
}

func (e lifecycleEnv) installer(p daemon.Platform, wait time.Duration) daemon.Installer {
	return daemon.Installer{Platform: p, Run: e.run, Probe: e.probe, Wait: wait}
}

// binary finds name next to this executable, else on PATH; "" when neither has it.
// When PATH reaches the sibling through a link (a package manager's bin dir, while
// this executable resolved to its versioned directory), the unit names the link,
// which an upgrade that removes the old version leaves in place.
func (e lifecycleEnv) binary(name string) string {
	onPath := ""
	if p, err := e.lookPath(name); err == nil {
		onPath, _ = filepath.Abs(p)
	}
	if e.exe != "" {
		if p := filepath.Join(filepath.Dir(e.exe), name); isExecutableFile(p) {
			if onPath != "" && sameFile(onPath, p) {
				return onPath
			}
			return p
		}
	}
	return onPath
}

func sameFile(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// setupReport is what setup prints: the install, plus the MCP binding for --root.
type setupReport struct {
	daemon.InstallReport
	MCP map[string]any `json:"mcp,omitempty"`
}

// runSetup installs (or upgrades) the daemon and waits until it answers.
func runSetup(e lifecycleEnv, args []string) int {
	usage := "usage: xmustard-ops setup [--label L] [--data-dir DIR] [--port N] [--api-bin PATH] [--core-bin PATH] [--log-file PATH] [--unit-dir DIR] [--env KEY=VALUE]... [--wait 30s] [--root /abs/repo [--client NAME]] [--print [--platform darwin|linux]]"
	fs, f := e.flagSet("setup")
	fs.StringVar(&f.logFile, "log-file", "", "the daemon's rotated log (default ~/Library/Logs/xmustard/api.log, or ~/.local/state/xmustard/api.log)")
	fs.StringVar(&f.platform, "platform", "", "with --print: render the units of darwin or linux")
	apiBin := fs.String("api-bin", "", "xmustard-api to run (default: next to xmustard-ops, else on PATH)")
	coreBin := fs.String("core-bin", "", "xmustard-core (default: $XMUSTARD_CORE_BIN, next to xmustard-ops, else on PATH)")
	root := fs.String("root", "", "also print the MCP client binding for this absolute repository root")
	client := fs.String("client", "", "client profile for the binding: "+strings.Join(mcpserver.ClientProfiles, "|"))
	printOnly := fs.Bool("print", false, "print the unit files and change nothing")
	var env stringSliceFlag
	fs.Var(&env, "env", "extra KEY=VALUE for the daemon's environment; may be repeated (credentials are refused)")
	if fs.Parse(args) != nil || fs.NArg() > 0 || (f.platform != "" && !*printOnly) {
		return e.usage(usage)
	}
	p, spec, err := e.resolve(f)
	if err != nil {
		return e.fail(err)
	}
	spec.APIBin = cmp.Or(*apiBin, e.binary("xmustard-api"))
	if spec.APIBin == "" {
		return e.fail(errors.New("xmustard-api is neither next to xmustard-ops nor on PATH; pass --api-bin"))
	}
	spec.CoreBin = cmp.Or(*coreBin, absOrEmpty(e.getenv("XMUSTARD_CORE_BIN")), e.binary("xmustard-core"))
	spec.Path = daemon.SearchPath(e.getenv("PATH"), dirOf(spec.APIBin), dirOf(spec.CoreBin))
	if spec.Env, err = parseEnvFlags(env); err != nil {
		return e.fail(err)
	}
	if err := spec.Validate(); err != nil {
		return e.fail(err)
	}
	if *printOnly {
		return e.emit(map[string]any{"platform": p.Name(), "units": p.Files(spec)})
	}
	if !isExecutableFile(spec.APIBin) {
		return e.fail(fmt.Errorf("%s is not an executable file", spec.APIBin))
	}
	binding, err := clientBinding(*root, *client, spec.BaseURL())
	if err != nil {
		return e.fail(err)
	}
	report, err := e.installer(p, f.wait).Install(context.Background(), spec)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(setupReport{InstallReport: report, MCP: binding})
}

// clientBinding is the mcpServers entry binding root's workspace to the daemon's
// Streamable HTTP endpoint (the mcp-config shape); nil without --root.
func clientBinding(root, client, api string) (map[string]any, error) {
	if root == "" {
		return nil, nil
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("--root must be absolute")
	}
	if client != "" {
		if _, err := mcpserver.ParseClientProfile(client); err != nil {
			return nil, err
		}
	}
	entry, _ := mcpEntry("http", api, mcpserver.WorkspaceIDForPath(filepath.Clean(root)), client, "")
	return map[string]any{"mcpServers": map[string]any{"xmustard": entry}}, nil
}

func parseEnvFlags(kvs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--env %q: want KEY=VALUE", kv)
		}
		out[k] = v
	}
	return out, nil
}

func dirOf(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
}

func absOrEmpty(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return ""
}

// runUninstall stops the daemon and removes its units; data and logs are kept.
func runUninstall(e lifecycleEnv, args []string) int {
	fs, f := e.flagSet("uninstall")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage("usage: xmustard-ops uninstall [--label L] [--unit-dir DIR]")
	}
	p, spec, err := e.resolve(f)
	if err != nil {
		return e.fail(err)
	}
	report, err := e.installer(p, f.wait).Uninstall(context.Background(), spec)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(report)
}

// daemonActions are the `daemon` subcommands. restart and stop act only on a unit
// setup installed.
var daemonActions = map[string]func(context.Context, daemon.Installer, daemon.Spec) (any, error){
	"status": func(ctx context.Context, in daemon.Installer, s daemon.Spec) (any, error) { return in.Status(ctx, s) },
	"restart": func(ctx context.Context, in daemon.Installer, s daemon.Spec) (any, error) {
		if err := requireInstalled(in, s); err != nil {
			return nil, err
		}
		h, err := in.Restart(ctx, s)
		return map[string]any{"health": h, "service": in.Platform.Status(ctx, in.Run, s)}, err
	},
	"stop": func(ctx context.Context, in daemon.Installer, s daemon.Spec) (any, error) {
		if err := requireInstalled(in, s); err != nil {
			return nil, err
		}
		err := in.Stop(ctx, s)
		return map[string]any{"service": in.Platform.Status(ctx, in.Run, s)}, err
	},
}

func requireInstalled(in daemon.Installer, s daemon.Spec) error {
	present, err := in.Installed(s)
	if err == nil && len(present) == 0 {
		err = fmt.Errorf("no daemon is installed under %q; run xmustard-ops setup", s.Label)
	}
	return err
}

// runDaemon is `daemon <status|restart|stop>`.
func runDaemon(e lifecycleEnv, args []string) int {
	usage := "usage: xmustard-ops daemon <status|restart|stop> [--label L] [--unit-dir DIR] [--port N] [--wait 30s]"
	if len(args) < 1 {
		return e.usage(usage)
	}
	act, ok := daemonActions[args[0]]
	fs, f := e.flagSet("daemon " + args[0])
	if !ok || fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		return e.usage(usage)
	}
	p, spec, err := e.resolve(f)
	if err != nil {
		return e.fail(err)
	}
	out, err := act(context.Background(), e.installer(p, f.wait), spec)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(out)
}

// storeActions are the `store` subcommands.
var storeActions = map[string]func(lifecycleEnv, []string) int{
	"backup":  runStoreBackup,
	"check":   runStoreCheck,
	"restore": runStoreRestore,
}

// runStore is `store <backup|check|restore>`.
func runStore(e lifecycleEnv, args []string) int {
	if len(args) < 1 {
		return e.usage("usage: xmustard-ops store <backup|check|restore> [flags]")
	}
	act, ok := storeActions[args[0]]
	if !ok {
		return e.usage("usage: xmustard-ops store <backup|check|restore> [flags]")
	}
	return act(e, args[1:])
}

// storeFile is the store a backup or check reads: --file, else --data-dir's, else the
// installed daemon's default data dir.
func (e lifecycleEnv) storeFile(f *placeFlags, file string) (string, error) {
	switch {
	case file != "":
		return filepath.Abs(file)
	case f.dataDir != "":
		dir, err := filepath.Abs(f.dataDir)
		return workspaceops.MemoryStorePath(dir), err
	}
	_, spec, err := e.resolve(f)
	return workspaceops.MemoryStorePath(spec.DataDir), err
}

// runStoreBackup copies the store (VACUUM INTO) while the daemon runs, and verifies the copy.
func runStoreBackup(e lifecycleEnv, args []string) int {
	fs, f := e.flagSet("store backup")
	file := fs.String("file", "", "the store file to back up (default: the data dir's governance.db)")
	out := fs.String("out", "", "the backup to write, which must not exist (default: <data dir>/backups/governance-<UTC time>.db)")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage("usage: xmustard-ops store backup [--data-dir DIR | --file PATH] [--out PATH]")
	}
	src, err := e.storeFile(f, *file)
	if err != nil {
		return e.fail(err)
	}
	dest := *out
	if dest == "" {
		dir := filepath.Join(filepath.Dir(src), "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return e.fail(err)
		}
		dest = filepath.Join(dir, "governance-"+e.now().UTC().Format("20060102T150405Z")+".db")
	}
	info, err := govstore.BackupFile(context.Background(), src, dest)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"source": src, "backup": info})
}

// runStoreCheck verifies a store file read-only: identity, schema, quick_check.
func runStoreCheck(e lifecycleEnv, args []string) int {
	fs, f := e.flagSet("store check")
	file := fs.String("file", "", "the store file to check (default: the data dir's governance.db)")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return e.usage("usage: xmustard-ops store check [--data-dir DIR | --file PATH]")
	}
	path, err := e.storeFile(f, *file)
	if err != nil {
		return e.fail(err)
	}
	info, err := govstore.VerifyFile(context.Background(), path)
	if err != nil {
		return e.fail(err)
	}
	return e.emit(map[string]any{"ok": true, "store": info})
}

// runStoreRestore stops the installed daemon, restores the backup over the data dir's
// store (the replaced files are kept beside it), and starts the daemon again, which
// checks the restored store as it starts.
func runStoreRestore(e lifecycleEnv, args []string) int {
	usage := "usage: xmustard-ops store restore <backup> [--data-dir DIR] [--label L] [--unit-dir DIR] [--port N] [--wait 30s]"
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return e.usage(usage)
	}
	fs, f := e.flagSet("store restore")
	if fs.Parse(args[1:]) != nil || fs.NArg() > 0 {
		return e.usage(usage)
	}
	p, spec, err := e.resolve(f)
	if err != nil {
		return e.fail(err)
	}
	ctx, in := context.Background(), e.installer(p, f.wait)
	installed, err := in.Installed(spec)
	if err != nil {
		return e.fail(err)
	}
	if len(installed) > 0 {
		if err := in.Stop(ctx, spec); err != nil {
			return e.fail(err)
		}
	}
	result := map[string]any{}
	var restoreErr error
	if _, err := e.probe(ctx, spec.BaseURL()); err == nil {
		restoreErr = fmt.Errorf("an xMustard API still answers on %s after the installed daemon stopped; stop it before restoring", spec.BaseURL())
	} else {
		report, err := govstore.RestoreFile(ctx, args[0], workspaceops.MemoryStorePath(spec.DataDir), e.now())
		result["restore"], restoreErr = report, err
	}
	if len(installed) > 0 { // back up on the restored store, or on the previous one when the restore failed
		h, err := in.Restart(ctx, spec)
		result["health"] = h
		restoreErr = errors.Join(restoreErr, err)
	}
	if code := e.emit(result); code != 0 {
		return code
	}
	if restoreErr != nil {
		return e.fail(restoreErr)
	}
	return 0
}
