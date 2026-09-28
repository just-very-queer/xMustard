package mcpserver

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// packageVersion matches the version line of rust-core/Cargo.toml's [package] table,
// which is the file's first version line.
var packageVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"$`)

// One release carries one version: the serverInfo an MCP client sees, the core's
// crate version (which the resident worker reports) and the Claude Code plugin's
// manifest are bumped together.
func TestServerInfoVersionMatchesTheRelease(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	cargo, err := os.ReadFile(filepath.Join(root, "rust-core", "Cargo.toml"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("rust-core/Cargo.toml not present in this checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	m := packageVersion.FindSubmatch(cargo)
	if m == nil {
		t.Fatal("rust-core/Cargo.toml has no package version line")
	}
	want := string(m[1])
	if got := New(Options{}).opts.Version; got != want {
		t.Errorf("serverInfo version %q, rust-core/Cargo.toml %q", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(root, "integrations", "claude-code", ".claude-plugin", "plugin.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Version != want {
		t.Errorf("Claude Code plugin version %q, rust-core/Cargo.toml %q", plugin.Version, want)
	}
}
