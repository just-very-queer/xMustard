package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"xmustard/api-go/internal/mcpserver"
)

// `xmustard-ops mcp-config` prints the MCP server entry that binds one project to its
// workspace on the API's Streamable HTTP endpoint (PAR-RT-03, PAR-ADP-05), in the
// form the client's config file takes. HTTP carries no working directory, so the
// binding is how a URL-configured client (or the relay) names the project; without it
// the server falls back to the client's roots, which Codex never sends.
//
//	xmustard-ops mcp-config --root /abs/repo | --workspace-id ID
//	    [--transport http|relay] [--api http://127.0.0.1:8042] [--client NAME] [--mode full|readonly]
//
// The token is never written into the entry. The JSON mcpServers form names
// ${XMUSTARD_API_TOKEN}, which the client expands (http) or passes through the
// environment (relay); Codex's config.toml names the variable itself
// (bearer_token_env_var, env_vars) and OpenCode's config substitutes
// {env:XMUSTARD_API_TOKEN}. --client codex prints the TOML table for
// ~/.codex/config.toml, --client opencode the `mcp` object of opencode.json; every
// other client gets the JSON mcpServers form.

const tokenEnv = "XMUSTARD_API_TOKEN"

// mcpEntry is one transport's server entry, before a client's config syntax.
type mcpEntry struct {
	url     string            // http: the /mcp URL with its query
	headers map[string]string // http: headers besides the token's
	command string            // relay: the stdio command and its arguments
	args    []string
}

// newMCPEntry builds the entry for a transport; false when the transport is unknown.
func newMCPEntry(transport, api, ws, client, mode string) (mcpEntry, bool) {
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
		return mcpEntry{url: api + "/mcp?" + q.Encode(), headers: map[string]string{"X-Xmustard-Workspace": ws}}, true
	case "relay":
		args := []string{"--url", api + "/mcp", "--workspace", ws}
		if client != "" {
			args = append(args, "--client", client)
		}
		if mode != "" {
			args = append(args, "--mode", mode)
		}
		return mcpEntry{command: "xmustard-relay", args: args}, true
	}
	return mcpEntry{}, false
}

// mcpConfigWriters render an entry in a client's own config syntax; a client not
// listed takes the JSON mcpServers form.
var mcpConfigWriters = map[string]func(io.Writer, mcpEntry) error{
	"codex":    writeCodexTOML,
	"opencode": writeOpenCodeJSON,
}

// writeMCPServersJSON prints {"mcpServers": {"xmustard": entry}}.
func writeMCPServersJSON(w io.Writer, e mcpEntry) error {
	var entry map[string]any
	if e.url != "" {
		entry = map[string]any{"type": "http", "url": e.url, "headers": e.headersWithToken("Bearer ${" + tokenEnv + "}")}
	} else {
		entry = map[string]any{"command": e.command, "args": e.args, "env": map[string]string{tokenEnv: "${" + tokenEnv + "}"}}
	}
	return writeIndentedJSON(w, map[string]any{"mcpServers": map[string]any{"xmustard": entry}})
}

// writeOpenCodeJSON prints the `mcp` object of opencode.json in OpenCode 2.x's form
// (@opencode/schema 2.0.1 config/mcp.ts: mcp.servers.<name>, what `opencode mcp add`
// writes): a remote server with url, headers and oauth false (no OAuth discovery
// against the API), or a local one whose command is an argv array and whose
// environment names the token. OpenCode substitutes {env:NAME} in its config text.
// OpenCode 1.x takes the same entry one level up (mcp.<name>), a form 2.x still reads.
func writeOpenCodeJSON(w io.Writer, e mcpEntry) error {
	ref := "{env:" + tokenEnv + "}"
	var entry map[string]any
	if e.url != "" {
		entry = map[string]any{"type": "remote", "url": e.url, "headers": e.headersWithToken("Bearer " + ref), "oauth": false}
	} else {
		entry = map[string]any{"type": "local", "command": append([]string{e.command}, e.args...), "environment": map[string]string{tokenEnv: ref}}
	}
	return writeIndentedJSON(w, map[string]any{"mcp": map[string]any{"servers": map[string]any{"xmustard": entry}}})
}

// headersWithToken is the entry's headers plus an Authorization header.
func (e mcpEntry) headersWithToken(auth string) map[string]string {
	headers := map[string]string{"Authorization": auth}
	maps.Copy(headers, e.headers)
	return headers
}

func writeIndentedJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// writeCodexTOML prints the [mcp_servers.xmustard] table of Codex's config.toml
// (codex-rs config/src/mcp_types.rs RawMcpServerConfig): a streamable HTTP server
// takes url, bearer_token_env_var and http_headers; a stdio server takes command,
// args and env_vars, the names of variables passed through from Codex's environment.
func writeCodexTOML(w io.Writer, e mcpEntry) error {
	var b strings.Builder
	b.WriteString("[mcp_servers.xmustard]\n")
	if e.url != "" {
		fmt.Fprintf(&b, "url = %s\nbearer_token_env_var = %s\nhttp_headers = %s\n", tomlString(e.url), tomlString(tokenEnv), tomlInlineTable(e.headers))
	} else {
		fmt.Fprintf(&b, "command = %s\nargs = %s\nenv_vars = %s\n", tomlString(e.command), tomlArray(e.args), tomlArray([]string{tokenEnv}))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// tomlString is a TOML basic string. JSON's string escapes are all valid TOML; TOML
// also forbids a raw DEL, which JSON leaves as is.
func tomlString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), "\x7f", `\u007F`)
}

func tomlArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlInlineTable renders string pairs sorted by key, keys quoted.
func tomlInlineTable(m map[string]string) string {
	var pairs []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		pairs = append(pairs, tomlString(k)+" = "+tomlString(m[k]))
	}
	return "{ " + strings.Join(pairs, ", ") + " }"
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
	profile := ""
	if *client != "" {
		p, err := mcpserver.ParseClientProfile(*client)
		if err != nil {
			return err
		}
		profile = p
	}
	if _, err := mcpserver.ParseMode(*mode); err != nil {
		return err
	}
	entry, ok := newMCPEntry(*transport, *api, ws, profile, *mode)
	if !ok {
		return fmt.Errorf("unknown --transport %q; use http or relay", *transport)
	}
	write, ok := mcpConfigWriters[profile]
	if !ok {
		write = writeMCPServersJSON
	}
	return write(w, entry)
}
