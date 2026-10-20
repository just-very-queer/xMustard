package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/hooks/transport"
	"xmustard/api-go/internal/mcpserver"
)

// `xmustard-ops cursor-hooks` sets up the Cursor hook client (WS-40c-b, WS-44a;
// PAR-ADP-10, PAR-HAR-12).
//
//	xmustard-ops cursor-hooks config   [--client-bin PATH] [--root DIR | --workspace-id ID]
//	xmustard-ops cursor-hooks install  [--client-bin PATH] [--root DIR | --workspace-id ID]
//	xmustard-ops cursor-hooks status   [--root DIR]
//	xmustard-ops cursor-hooks uninstall [--root DIR]
//
// config prints a hooks.json that runs xmustard-cursor for every Cursor event
// xMustard routes (sessionStart, preToolUse, postToolUse, preCompact). install
// writes that file where Cursor loads it: the project's .cursor/hooks.json with
// --root, else the user's ~/.cursor/hooks.json. Without a pin the daemon binds each
// event to the registered root that holds the event's project directory, and the client
// refuses to post at all when that directory and the roots disagree. --root or
// --workspace-id pins every event (the client sends X-Xmustard-Workspace), for one
// repository.
//
// install keeps a backup of the file it replaces (the same `.bak` name the store's
// backups use) and swaps the new file in atomically, so Cursor never reads half a
// file. It **refuses** a file that holds hooks xMustard did not write: those are the
// user's, and taking them away is not an install. uninstall removes the file when it
// is xMustard's own, and refuses a file that holds another's hooks.
//
// The pin is a **workspace id**, not a path (WS-40c-b r1-2): the daemon's header is
// a workspace-id channel, and the client refuses an id it cannot validate. The
// command is written for one client only: a hook that runs another command, or
// another xmustard-cursor, is never written.
var cursorHooksCommands = map[string]func(opsEnv, []string) int{
	"config":    runCursorHooksConfig,
	"install":   runCursorHooksInstall,
	"status":    runCursorHooksStatus,
	"uninstall": runCursorHooksUninstall,
}

const cursorHooksUsage = "usage: xmustard-ops cursor-hooks <config|install|status|uninstall> [flags]"

// runCursorHooks is the entry point for the cursor-hooks subcommand.
func runCursorHooks(e opsEnv, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.stderr, cursorHooksUsage)
		return exitUsage
	}
	run, ok := cursorHooksCommands[args[0]]
	if !ok {
		fmt.Fprintln(e.stderr, cursorHooksUsage)
		return exitUsage
	}
	return run(e, args[1:])
}

// runCursorHooksConfig prints the Cursor hooks.json.
func runCursorHooksConfig(e opsEnv, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops cursor-hooks config", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	bin := fs.String("client-bin", hooks.CursorClient, "the command hook client: "+hooks.CursorClient+" on Cursor's PATH, or an absolute path to it")
	root := fs.String("root", "", "pin every event to the workspace of this absolute repository root")
	wsID := fs.String("workspace-id", "", "pin every event to this workspace id (instead of --root)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	ws, code := cursorHooksPin(e, *wsID, *root)
	if code != 0 {
		return code
	}
	if code := checkCursorClientBin(e, *bin); code != 0 {
		return code
	}
	_, _ = e.stdout.Write(hooks.CursorHooksJSON(*bin, ws))
	return 0
}

// checkCursorClientBin refuses a client a Cursor hooks.json could not run safely
// (hooks.ValidCursorClient).
func checkCursorClientBin(e opsEnv, bin string) int {
	if hooks.ValidCursorClient(bin) {
		return 0
	}
	fmt.Fprintf(e.stderr, "xmustard-ops cursor-hooks: --client-bin %q must be %s or an absolute path to it\n", bin, hooks.CursorClient)
	return exitUsage
}

// cursorHooksPath is the file Cursor loads the hooks from: the project's
// .cursor/hooks.json with --root, else the user's ~/.cursor/hooks.json.
func cursorHooksPath(e opsEnv, root string) (string, error) {
	if root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("--root must be absolute")
		}
		return filepath.Join(root, ".cursor", "hooks.json"), nil
	}
	home := strings.TrimSpace(e.getenv("HOME"))
	if home == "" || !filepath.IsAbs(home) {
		return "", errors.New("HOME must be an absolute path")
	}
	return filepath.Join(home, ".cursor", "hooks.json"), nil
}

// runCursorHooksInstall writes the file where Cursor loads it.
func runCursorHooksInstall(e opsEnv, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops cursor-hooks install", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	bin := fs.String("client-bin", hooks.CursorClient, "the command hook client: "+hooks.CursorClient+" on Cursor's PATH, or an absolute path to it")
	root := fs.String("root", "", "install into this absolute repository root's .cursor/hooks.json (else the user's ~/.cursor/hooks.json)")
	wsID := fs.String("workspace-id", "", "pin every event to this workspace id (instead of --root)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	ws, code := cursorHooksPin(e, *wsID, *root)
	if code != 0 {
		return code
	}
	if code := checkCursorClientBin(e, *bin); code != 0 {
		return code
	}
	path, err := cursorHooksPath(e, strings.TrimSpace(*root))
	if err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks install:", err)
		return exitUsage
	}
	body := hooks.CursorHooksJSON(*bin, ws)
	if existing, err := os.ReadFile(path); err == nil {
		_, foreign := hooks.ClassifyCursorHooks(existing)
		if foreign > 0 {
			fmt.Fprintf(e.stderr, "xmustard-ops cursor-hooks install: %s holds %d hooks xMustard did not write; move them out of the way and run this again\n", path, foreign)
			return exitError
		}
		// The file is replaced, so it is kept: a user who had their own hooks there
		// (or none) can put them back.
		if err := os.WriteFile(path+".bak", existing, 0o644); err != nil {
			fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks install:", err)
			return exitError
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks install:", err)
		return exitError
	}
	if err := writeFileAtomic(path, string(body)); err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks install:", err)
		return exitError
	}
	fmt.Fprintf(e.stdout, "installed %s\n", path)
	return 0
}

// runCursorHooksStatus reports the file Cursor would load.
func runCursorHooksStatus(e opsEnv, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops cursor-hooks status", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	root := fs.String("root", "", "report this absolute repository root's .cursor/hooks.json (else the user's)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	path, err := cursorHooksPath(e, strings.TrimSpace(*root))
	if err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks status:", err)
		return exitUsage
	}
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(e.stdout, "%s: not installed\n", path)
		return exitNeedsUser
	}
	ours, foreign := hooks.ClassifyCursorHooks(body)
	fmt.Fprintf(e.stdout, "%s: %d xMustard hooks, %d other hooks\n", path, ours, foreign)
	for _, ev := range hooks.CursorEvents {
		if !hooks.CursorHooksHasEvent(body, ev.Name) {
			fmt.Fprintf(e.stdout, "  %s: not registered\n", ev.Name)
			return exitNeedsUser
		}
	}
	return 0
}

// runCursorHooksUninstall removes the file when it is xMustard's own.
func runCursorHooksUninstall(e opsEnv, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops cursor-hooks uninstall", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	root := fs.String("root", "", "remove this absolute repository root's .cursor/hooks.json (else the user's)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	path, err := cursorHooksPath(e, strings.TrimSpace(*root))
	if err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks uninstall:", err)
		return exitUsage
	}
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(e.stdout, "%s: nothing to remove\n", path)
		return 0
	}
	ours, foreign := hooks.ClassifyCursorHooks(body)
	if foreign > 0 {
		fmt.Fprintf(e.stderr, "xmustard-ops cursor-hooks uninstall: %s holds %d hooks xMustard did not write; remove them by hand, or restore the backup\n", path, foreign)
		return exitError
	}
	if ours == 0 {
		fmt.Fprintf(e.stdout, "%s: no xMustard hooks in it; nothing removed\n", path)
		return 0
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks uninstall:", err)
		return exitError
	}
	fmt.Fprintf(e.stdout, "removed %s\n", path)
	return 0
}

// cursorHooksPin resolves the pin a file carries: the workspace id, or the one the
// root belongs to.
func cursorHooksPin(e opsEnv, wsID, root string) (string, int) {
	ws := strings.TrimSpace(wsID)
	if r := strings.TrimSpace(root); r != "" {
		if !filepath.IsAbs(r) {
			fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks: --root must be absolute")
			return "", exitUsage
		}
		ws = mcpserver.WorkspaceIDForPath(filepath.Clean(r))
	}
	if ws != "" && !transport.ValidWorkspaceID(ws) {
		fmt.Fprintf(e.stderr, "xmustard-ops cursor-hooks: workspace id %q is not a safe id (letters, digits, '_', '.', '-'); the client would send no pin\n", ws)
		return "", exitUsage
	}
	return ws, 0
}

// writeFileAtomic replaces path through a temporary file in its directory, so Cursor
// never reads half a file.
func writeFileAtomic(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, werr := tmp.WriteString(content)
	if err := errors.Join(werr, tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
