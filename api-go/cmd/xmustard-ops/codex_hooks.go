package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"xmustard/api-go/internal/hooks"
	"xmustard/api-go/internal/mcpserver"
)

// `xmustard-ops codex-hooks` sets up the Codex hook client (WS-40a, PAR-ADP-07).
//
//	xmustard-ops codex-hooks config [--hook-bin PATH] [--root DIR | --workspace-id ID]
//	xmustard-ops codex-hooks status [--cwd DIR] [--codex PATH]
//	xmustard-ops codex-hooks trust  [--cwd DIR] [--codex PATH]
//
// config prints a hooks.json that runs xmustard-hook for every Codex event with a
// route. Without a pin it suits ~/.codex/hooks.json: the daemon binds each event to the
// registered root that holds its cwd. --root or --workspace-id pins every event (the
// client sends X-Xmustard-Workspace), for one project's .codex/hooks.json, which Codex
// loads only once the project itself is trusted.
//
// Codex runs a user or project hook only while the hash of its definition is trusted
// (hooks.state.<key>.trusted_hash in config.toml). status lists xMustard's hooks as
// Codex sees them from a directory, with their trust. trust shows xMustard's untrusted
// or modified hooks and records their current hashes only when the user types "trust"
// at the controlling terminal. Both ask `codex app-server` over stdio (hooks/list, then
// config/batchWrite on hooks.state), the path Codex's own /hooks view takes (codex-rs
// tui/src/hooks_rpc.rs). No flag skips the prompt: trusting a hook lets Codex run it,
// and that is the user's decision. A hook whose command is not exactly what config
// writes is never offered.

var codexHooksCommands = map[string]func(opsEnv, []string) int{
	"config": runCodexHooksConfig,
	"status": func(e opsEnv, args []string) int { return runCodexHooksReview(e, args, false) },
	"trust":  func(e opsEnv, args []string) int { return runCodexHooksReview(e, args, true) },
}

const codexHooksUsage = "usage: xmustard-ops codex-hooks <config|status|trust> [flags]"

// exitNeedsUser is status's and trust's exit when a routed event is not registered or
// a registered hook is not trusted.
const exitNeedsUser = 3

func runCodexHooks(e opsEnv, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.stderr, codexHooksUsage)
		return exitUsage
	}
	run, ok := codexHooksCommands[args[0]]
	if !ok {
		fmt.Fprintln(e.stderr, codexHooksUsage)
		return exitUsage
	}
	return run(e, args[1:])
}

func runCodexHooksConfig(e opsEnv, args []string) int {
	fs := flag.NewFlagSet("xmustard-ops codex-hooks config", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	bin := fs.String("hook-bin", "xmustard-hook", "the static hook client: an absolute path, or a name on Codex's PATH")
	root := fs.String("root", "", "pin every event to the workspace of this absolute repository root")
	wsID := fs.String("workspace-id", "", "pin every event to this workspace id (instead of --root)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	ws := strings.TrimSpace(*wsID)
	if r := strings.TrimSpace(*root); r != "" {
		if !filepath.IsAbs(r) {
			fmt.Fprintln(e.stderr, "xmustard-ops codex-hooks config: --root must be absolute")
			return exitUsage
		}
		ws = mcpserver.WorkspaceIDForPath(filepath.Clean(r))
	}
	if strings.TrimSpace(*bin) == "" {
		fmt.Fprintln(e.stderr, "xmustard-ops codex-hooks config: --hook-bin is empty")
		return exitUsage
	}
	_, _ = e.stdout.Write(hooks.CodexHooksJSON(*bin, ws))
	return 0
}

// codexHook is one hooks/list entry (app-server-protocol v2 HookMetadata).
type codexHook struct {
	Key         string `json:"key"`
	EventName   string `json:"eventName"`
	HandlerType string `json:"handlerType"`
	Command     string `json:"command"`
	Async       bool   `json:"async"`
	SourcePath  string `json:"sourcePath"`
	Enabled     bool   `json:"enabled"`
	IsManaged   bool   `json:"isManaged"`
	CurrentHash string `json:"currentHash"`
	TrustStatus string `json:"trustStatus"`
	event       string // the routed event of an xMustard hook
}

// needsTrust reports a hook Codex will not run until the user trusts its current hash.
func (h codexHook) needsTrust() bool {
	return h.Enabled && !h.IsManaged && (h.TrustStatus == "untrusted" || h.TrustStatus == "modified")
}

// xmustardHooks keeps the synchronous command hooks whose command is one config
// writes, for the event it names.
func xmustardHooks(all []codexHook) []codexHook {
	var out []codexHook
	for _, h := range all {
		_, event, _, ok := hooks.ParseCodexCommand(h.Command)
		if ok && h.HandlerType == "command" && !h.Async && strings.EqualFold(h.EventName, event) {
			h.event = event
			out = append(out, h)
		}
	}
	return out
}

func runCodexHooksReview(e opsEnv, args []string, trust bool) int {
	fs := flag.NewFlagSet("xmustard-ops codex-hooks", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	cwd := fs.String("cwd", ".", "the directory whose hooks Codex would run (project hooks load only in a trusted project)")
	codexBin := fs.String("codex", envOr(e.getenv, "XMUSTARD_CODEX_BIN", "codex"), "the codex binary")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	dir, err := filepath.Abs(*cwd)
	if err != nil {
		fmt.Fprintln(e.stderr, err)
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server, stop, err := e.codex(ctx, *codexBin)
	if err != nil {
		fmt.Fprintf(e.stderr, "xmustard-ops codex-hooks: %v\n", err)
		return exitError
	}
	defer stop()
	ours, err := listCodexHooks(e, server, dir)
	if err != nil {
		fmt.Fprintf(e.stderr, "xmustard-ops codex-hooks: %v\n", err)
		return exitError
	}
	review := slices.DeleteFunc(slices.Clone(ours), func(h codexHook) bool { return !h.needsTrust() })
	if trust && len(review) > 0 {
		if code := trustCodexHooks(e, server, review); code != 0 {
			return code
		}
		if ours, err = listCodexHooks(e, server, dir); err != nil {
			fmt.Fprintf(e.stderr, "xmustard-ops codex-hooks: %v\n", err)
			return exitError
		}
	}
	return reportCodexHooks(e, dir, ours)
}

// listCodexHooks asks Codex which hooks it would run from dir and keeps xMustard's,
// passing on Codex's warnings and errors about them.
func listCodexHooks(e opsEnv, server *appServer, dir string) ([]codexHook, error) {
	var list struct {
		Data []struct {
			Cwd      string      `json:"cwd"`
			Hooks    []codexHook `json:"hooks"`
			Warnings []string    `json:"warnings"`
			Errors   []struct {
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := server.call("hooks/list", map[string]any{"cwds": []string{dir}}, &list); err != nil {
		return nil, err
	}
	var all []codexHook
	for _, entry := range list.Data {
		all = append(all, entry.Hooks...)
		for _, w := range entry.Warnings {
			fmt.Fprintf(e.stderr, "codex: %s\n", w)
		}
		for _, x := range entry.Errors {
			fmt.Fprintf(e.stderr, "codex: %s: %s\n", x.Path, x.Message)
		}
	}
	return xmustardHooks(all), nil
}

// trustCodexHooks shows the hooks and, once the user types "trust" at the terminal,
// records their current hashes as trusted.
func trustCodexHooks(e opsEnv, server *appServer, review []codexHook) int {
	fmt.Fprintf(e.stdout, "Codex will not run these %d xMustard hooks until you trust them:\n", len(review))
	for _, h := range review {
		fmt.Fprintf(e.stdout, "  %-16s %-9s %s\n  %16s from %s\n", h.event, h.TrustStatus, h.Command, "", h.SourcePath)
	}
	answer, err := e.confirm(fmt.Sprintf(`Type "trust" to let Codex run these %d hooks: `, len(review)))
	if err != nil {
		fmt.Fprintf(e.stderr, "xmustard-ops codex-hooks trust: %v; trust them in Codex's /hooks view instead\n", err)
		return exitUsage
	}
	if strings.TrimSpace(answer) != "trust" {
		fmt.Fprintln(e.stderr, "not trusted: nothing was written")
		return exitError
	}
	state := map[string]any{}
	for _, h := range review {
		state[h.Key] = map[string]string{"trusted_hash": h.CurrentHash}
	}
	var written struct {
		Status   string `json:"status"`
		FilePath string `json:"filePath"`
	}
	edit := map[string]any{"keyPath": "hooks.state", "value": state, "mergeStrategy": "upsert"}
	if err := server.call("config/batchWrite", map[string]any{"edits": []any{edit}, "reloadUserConfig": true}, &written); err != nil {
		fmt.Fprintf(e.stderr, "xmustard-ops codex-hooks trust: %v\n", err)
		return exitError
	}
	fmt.Fprintf(e.stdout, "trusted %d hooks in %s\n", len(review), written.FilePath)
	return 0
}

// reportCodexHooks prints xMustard's hooks with their trust and the routed events no
// hook registers; exitNeedsUser unless every event is registered and runs.
func reportCodexHooks(e opsEnv, dir string, ours []codexHook) int {
	code := 0
	registered := map[string]bool{}
	fmt.Fprintf(e.stdout, "xMustard's Codex hooks for %s:\n", dir)
	for _, h := range ours {
		registered[h.event] = true
		runs := h.Enabled && (h.IsManaged || h.TrustStatus == "trusted")
		if !runs {
			code = exitNeedsUser
		}
		fmt.Fprintf(e.stdout, "  %-16s %-9s enabled=%-5v %s\n", h.event, h.TrustStatus, h.Enabled, h.SourcePath)
	}
	var missing []string
	for _, ev := range hooks.CodexEvents {
		if !registered[ev.Name] {
			missing = append(missing, ev.Name)
		}
	}
	if len(missing) > 0 {
		code = exitNeedsUser
		fmt.Fprintf(e.stdout, "  not registered: %s (xmustard-ops codex-hooks config writes them)\n", strings.Join(missing, ", "))
	}
	return code
}

// appServer is a JSON-RPC connection to `codex app-server` over stdio, one JSON
// message per line (codex-rs app-server-transport stdio.rs).
type appServer struct {
	w    io.Writer
	r    *bufio.Reader
	next int
}

// maxAppServerOutput bounds what is read from the app server.
const maxAppServerOutput = 64 << 20

func newAppServer(w io.Writer, r io.Reader) *appServer {
	return &appServer{w: w, r: bufio.NewReader(io.LimitReader(r, maxAppServerOutput))}
}

// call sends a request and decodes its result, skipping the notifications and server
// requests that arrive before the response.
func (a *appServer) call(method string, params, result any) error {
	a.next++
	id := a.next
	if err := a.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		line, err := a.r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("codex app-server %s: %w", method, err)
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.ID == nil || *msg.ID != id || msg.Method != "" {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("codex app-server %s: %s", method, msg.Error.Message)
		}
		return json.Unmarshal(msg.Result, result)
	}
}

func (a *appServer) send(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = a.w.Write(append(b, '\n'))
	return err
}

// startAppServer runs `codex app-server` on stdio and initializes it; stop closes its
// input (it then exits) and waits for it.
func startAppServer(ctx context.Context, bin string) (*appServer, func(), error) {
	cmd := exec.CommandContext(ctx, bin, "app-server")
	cmd.Stderr = io.Discard // its log; warnings about hooks come back in hooks/list
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s app-server: %w", bin, err)
	}
	stop := func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	server := newAppServer(stdin, stdout)
	info := map[string]any{"clientInfo": map[string]string{"name": "xmustard-ops", "version": "1"}}
	if err := server.call("initialize", info, &json.RawMessage{}); err != nil {
		stop()
		return nil, nil, err
	}
	if err := server.send(map[string]any{"method": "initialized"}); err != nil {
		stop()
		return nil, nil, err
	}
	return server, stop, nil
}

// readTerminalLine prompts on the controlling terminal and reads one line typed there
// (echo on). Without a controlling terminal it fails, so nothing is trusted from a
// pipe, a script or an agent's shell.
func readTerminalLine(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no controlling terminal to ask you (%v)", err)
	}
	defer tty.Close()
	if _, err := tty.WriteString(label); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(tty, 256)).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("nothing was typed")
	}
	return line, nil
}
