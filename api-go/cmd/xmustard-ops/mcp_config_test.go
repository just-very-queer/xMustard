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
	if err := writeMCPConfig(&out, []string{"--root", "/src/app", "--client", "codex", "--mode", "readonly", "--api", "http://127.0.0.1:8042/"}); err != nil {
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
	if e.URL != "http://127.0.0.1:8042/mcp?client=codex&mode=readonly&workspace="+ws || e.Headers["X-Xmustard-Workspace"] != ws {
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
