package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/mcpserver"
)

// `xmustard-ops mcp-config` prints the mcpServers entry that binds one project to its
// workspace on the API's Streamable HTTP endpoint (PAR-RT-03, PAR-ADP-05). HTTP carries
// no working directory, so the binding is how a URL-configured client (or the relay)
// names the project; without it the server falls back to the client's roots.
//
//	xmustard-ops mcp-config --root /abs/repo | --workspace-id ID
//	    [--transport http|relay] [--api http://127.0.0.1:8042] [--client NAME] [--mode full|readonly]
//
// The token is never written into the entry: it names ${XMUSTARD_API_TOKEN}, which
// the client expands (http) or passes through the environment (relay).

// mcpEntry renders the mcpServers entry for a transport; false when it is unknown.
func mcpEntry(transport, api, ws, client, mode string) (map[string]any, bool) {
	api = strings.TrimRight(api, "/")
	switch transport {
	case "http":
		q := url.Values{"workspace": {ws}}
		if client != "" {
			q.Set("client", client)
		}
		if mode != "" {
			q.Set("mode", mode)
		}
		return map[string]any{
			"type":    "http",
			"url":     api + "/mcp?" + q.Encode(),
			"headers": map[string]string{"Authorization": "Bearer ${XMUSTARD_API_TOKEN}", "X-Xmustard-Workspace": ws},
		}, true
	case "relay":
		args := []string{"--url", api + "/mcp", "--workspace", ws}
		if client != "" {
			args = append(args, "--client", client)
		}
		if mode != "" {
			args = append(args, "--mode", mode)
		}
		return map[string]any{"command": "xmustard-relay", "args": args, "env": map[string]string{"XMUSTARD_API_TOKEN": "${XMUSTARD_API_TOKEN}"}}, true
	}
	return nil, false
}

func runMCPConfig(args []string) {
	if err := writeMCPConfig(os.Stdout, args); err != nil {
		fatalUsage("xmustard-ops mcp-config: " + err.Error())
	}
}

func writeMCPConfig(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("mcp-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "absolute repository root (the workspace id is derived from it)")
	wsID := fs.String("workspace-id", "", "workspace id (instead of --root)")
	transport := fs.String("transport", "http", "http or relay")
	api := fs.String("api", envDefault("XMUSTARD_API_BASE", "http://127.0.0.1:8042"), "API base URL")
	client := fs.String("client", "", "client profile: "+strings.Join(mcpserver.ClientProfiles, "|"))
	mode := fs.String("mode", "", "full or readonly")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ws := strings.TrimSpace(*wsID)
	if r := strings.TrimSpace(*root); r != "" {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("--root must be absolute")
		}
		ws = mcpserver.WorkspaceIDForPath(filepath.Clean(r))
	}
	if ws == "" {
		return fmt.Errorf("pass --root or --workspace-id")
	}
	if *client != "" {
		if _, err := mcpserver.ParseClientProfile(*client); err != nil {
			return err
		}
	}
	if _, err := mcpserver.ParseMode(*mode); err != nil {
		return err
	}
	entry, ok := mcpEntry(*transport, *api, ws, *client, *mode)
	if !ok {
		return fmt.Errorf("unknown --transport %q; use http or relay", *transport)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(map[string]any{"mcpServers": map[string]any{"xmustard": entry}})
}
