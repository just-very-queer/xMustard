package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/hooks/transport"
	"xmustard/api-go/internal/mcpserver"
)

// `xmustard-ops cursor-hooks` sets up the Cursor hook client (WS-40c-b, WS-44;
// PAR-ADP-10, PAR-HAR-12).
//
//	xmustard-ops cursor-hooks config [--client-bin PATH] [--root DIR | --workspace-id ID]
//
// config prints a hooks.json that runs xmustard-cursor for every Cursor event
// xMustard routes (sessionStart, preToolUse, postToolUse, preCompact). Without a pin
// it suits a user hooks file in ~/.cursor: the daemon binds each event to the
// registered root that holds the event's project directory, and the client refuses to
// post at all when that directory and the roots disagree. --root or --workspace-id
// pins every event (the client sends X-Xmustard-Workspace), for one project's
// .cursor/hooks.json.
//
// The pin is a **workspace id**, not a path (WS-40c-b r1-2): the daemon's header is
// a workspace-id channel, and the client refuses an id it cannot validate. The
// command is written for one client only: a hook that runs another command, or
// another xmustard-cursor, is never written.
var cursorHooksCommands = map[string]func(opsEnv, []string) int{
	"config": runCursorHooksConfig,
}

const cursorHooksUsage = "usage: xmustard-ops cursor-hooks <config> [flags]"

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
	ws := strings.TrimSpace(*wsID)
	if r := strings.TrimSpace(*root); r != "" {
		if !filepath.IsAbs(r) {
			fmt.Fprintln(e.stderr, "xmustard-ops cursor-hooks config: --root must be absolute")
			return exitUsage
		}
		ws = mcpserver.WorkspaceIDForPath(filepath.Clean(r))
	}
	if ws != "" && !transport.ValidWorkspaceID(ws) {
		fmt.Fprintf(e.stderr, "xmustard-ops cursor-hooks config: workspace id %q is not a safe id (letters, digits, '_', '.', '-'); the client would send no pin\n", ws)
		return exitUsage
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
