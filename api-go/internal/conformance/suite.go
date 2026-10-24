// Package conformance validates pinned client hook fixtures before route-level replay.
package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest pins the client build and source used to derive the fixtures.
type Manifest struct {
	Derivation    string `json:"derivation"`
	CursorVersion string `json:"cursor_version"`
}

// Fixture describes one pinned input/output pair.
type Fixture struct {
	Input string
	Event string
}

// CursorFixtures are deliberately ordered to match the fixture numbering.
var CursorFixtures = []Fixture{
	{Input: "01-sessionStart.in.json", Event: "sessionStart"},
	{Input: "02-preToolUse-shell.in.json", Event: "preToolUse"},
	{Input: "03-postToolUse-mcp.in.json", Event: "postToolUse"},
	{Input: "04-preCompact.in.json", Event: "preCompact"},
}

// ReadCursorFixture validates the pinned metadata and reads one fixture input.
// The caller must send the bytes through the production route to assert client behavior.
func ReadCursorFixture(fixtureDir string, fixture Fixture) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode Cursor fixture manifest: %w", err)
	}
	switch manifest.Derivation {
	case "source-derived", "live-recorded":
	default:
		return nil, fmt.Errorf("unexpected Cursor fixture derivation %q", manifest.Derivation)
	}
	if manifest.CursorVersion != "3.22.7" {
		return nil, fmt.Errorf("unexpected Cursor fixture version %q", manifest.CursorVersion)
	}
	input, err := os.ReadFile(filepath.Join(fixtureDir, fixture.Input))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fixture.Input, err)
	}
	var event struct {
		Name          string `json:"hook_event_name"`
		CursorVersion string `json:"cursor_version"`
	}
	if err := json.Unmarshal(input, &event); err != nil {
		return nil, fmt.Errorf("decode %s: %w", fixture.Input, err)
	}
	if event.Name != fixture.Event {
		return nil, fmt.Errorf("%s event %q, want %q", fixture.Input, event.Name, fixture.Event)
	}
	if event.CursorVersion == "" || event.CursorVersion != manifest.CursorVersion {
		return nil, fmt.Errorf("%s Cursor version %q, want manifest version %q", fixture.Input, event.CursorVersion, manifest.CursorVersion)
	}
	return input, nil
}

// RunCursorFixtures validates every pinned input. Behavioral conformance is exercised
// by the API package through the production route, not by invoking the formatter here.
func RunCursorFixtures(fixtureDir string) error {
	for _, fixture := range CursorFixtures {
		if _, err := ReadCursorFixture(fixtureDir, fixture); err != nil {
			return err
		}
	}
	return nil
}
