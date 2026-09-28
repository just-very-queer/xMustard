package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"xmustard/api-go/internal/mcpserver"
)

func TestMCPConfigWritesWorkspaceBinding(t *testing.T) {
	var out bytes.Buffer
	if err := writeMCPConfig(&out, []string{"--root", "/src/app", "--client", "claude-code", "--mode", "readonly", "--api", "http://127.0.0.1:8042/"}); err != nil {
		t.Fatal(err)
	}
	ws := mcpserver.WorkspaceIDForPath("/src/app")
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(out.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	e := cfg.MCPServers["xmustard"]
	if e.URL != "http://127.0.0.1:8042/mcp?client=claude-code&mode=readonly&workspace="+ws || e.Headers["X-Xmustard-Workspace"] != ws {
		t.Fatalf("entry: %+v", e)
	}
	if strings.Contains(out.String(), "xmt_") || !strings.Contains(out.String(), "${XMUSTARD_API_TOKEN}") {
		t.Fatalf("token handling: %s", out.String())
	}
	out.Reset()
	if err := writeMCPConfig(&out, []string{"--workspace-id", "ws1", "--transport", "relay"}); err != nil || !strings.Contains(out.String(), `"--workspace",`) {
		t.Fatalf("relay entry: %v %s", err, out.String())
	}
	for _, bad := range [][]string{{}, {"--root", "rel"}, {"--workspace-id", "w", "--transport", "sse"}, {"--workspace-id", "w", "--client", "x"}, {"--workspace-id", "w", "--mode", "admin"}} {
		if err := writeMCPConfig(&out, bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
}

// Codex reads MCP servers from config.toml, not JSON mcpServers (codex-rs
// config/src/mcp_types.rs), and sends no roots, so the entry carries the workspace
// header; the token is named, never written.
func TestMCPConfigCodexWritesConfigTOML(t *testing.T) {
	var out bytes.Buffer
	if err := writeMCPConfig(&out, []string{"--workspace-id", "ws-1", "--client", "Codex"}); err != nil {
		t.Fatal(err)
	}
	want := `[mcp_servers.xmustard]
url = "http://127.0.0.1:8042/mcp?client=codex&workspace=ws-1"
bearer_token_env_var = "XMUSTARD_API_TOKEN"
http_headers = { "X-Xmustard-Workspace" = "ws-1" }
`
	if out.String() != want {
		t.Fatalf("codex http entry:\n%s\nwant:\n%s", out.String(), want)
	}
	out.Reset()
	if err := writeMCPConfig(&out, []string{"--workspace-id", "ws \"1\"\x7f", "--client", "codex", "--transport", "relay", "--mode", "readonly"}); err != nil {
		t.Fatal(err)
	}
	want = `[mcp_servers.xmustard]
command = "xmustard-relay"
args = ["--url", "http://127.0.0.1:8042/mcp", "--workspace", "ws \"1\"\u007F", "--client", "codex", "--mode", "readonly"]
env_vars = ["XMUSTARD_API_TOKEN"]
`
	if out.String() != want {
		t.Fatalf("codex relay entry:\n%s\nwant:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), "${") {
		t.Fatal("codex config.toml expands no ${VAR}: the token is passed by name")
	}
}
