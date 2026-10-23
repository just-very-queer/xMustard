// Package conformance replays pinned client hook fixtures against the shared hook adapter.
package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xmustard/api-go/internal/hooks"
)

// Manifest pins the client build and source used to derive the fixtures.
type Manifest struct {
	Derivation    string `json:"derivation"`
	CursorVersion string `json:"cursor_version"`
}

// Fixture describes one pinned input/output pair.
type Fixture struct {
	Input  string
	Event  string
	Abort  bool
	Output string
}

// CursorFixtures are deliberately ordered to match the fixture numbering.
var CursorFixtures = []Fixture{
	{Input: "01-sessionStart.in.json", Event: "sessionStart", Output: `{"additional_context":"fixture context"}`},
	{Input: "02-preToolUse-shell.in.json", Event: "preToolUse", Output: `{"additional_context":"fixture context"}`},
	{Input: "03-postToolUse-mcp.in.json", Event: "postToolUse"},
	{Input: "04-preCompact.in.json", Event: "preCompact", Abort: true},
}

// RunCursorFixtures checks that each pinned event reaches the adapter and that the
// resulting JSON matches the adapter's pinned response contract. The input fixtures
// are source-derived, not recorded from a live Cursor session. fixtureDir is the
// directory containing the input files and manifest.
func RunCursorFixtures(fixtureDir string) error {
	data, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode Cursor fixture manifest: %w", err)
	}
	if manifest.CursorVersion != "3.22.7" || manifest.Derivation != "source-derived" {
		return fmt.Errorf("unexpected Cursor fixture pin: version=%q derivation=%q", manifest.CursorVersion, manifest.Derivation)
	}
	for _, fixture := range CursorFixtures {
		input, err := os.ReadFile(filepath.Join(fixtureDir, fixture.Input))
		if err != nil {
			return fmt.Errorf("read %s: %w", fixture.Input, err)
		}
		var event struct {
			Name   string `json:"hook_event_name"`
			Output string `json:"tool_output"`
		}
		if err := json.Unmarshal(input, &event); err != nil {
			return fmt.Errorf("decode %s: %w", fixture.Input, err)
		}
		if event.Name != fixture.Event {
			return fmt.Errorf("%s event %q, want %q", fixture.Input, event.Name, fixture.Event)
		}
		ev, ok := hooks.Cursor.Event(event.Name)
		if !ok {
			return fmt.Errorf("%s has no routed Cursor event", fixture.Input)
		}
		var updated json.RawMessage
		if event.Name == "postToolUse" {
			updated = json.RawMessage(event.Output)
		}
		reply := hooks.Cursor.Reply(ev, hooks.Result{Context: "fixture context", Updated: updated})
		if fixture.Abort {
			if reply != nil {
				return fmt.Errorf("%s must abort without a reply, got %s", fixture.Input, reply.Body)
			}
			continue
		}
		if reply == nil {
			return fmt.Errorf("%s did not reach a model-facing reply", fixture.Input)
		}
		if fixture.Output != "" && strings.TrimSpace(string(reply.Body)) != fixture.Output {
			return fmt.Errorf("%s response %s, want %s", fixture.Input, reply.Body, fixture.Output)
		}
		var shape map[string]json.RawMessage
		if err := json.Unmarshal(reply.Body, &shape); err != nil {
			return fmt.Errorf("%s response is not an object: %w", fixture.Input, err)
		}
		wantKeys := 1
		if event.Name == "postToolUse" {
			wantKeys = 2
		}
		if len(shape) != wantKeys {
			return fmt.Errorf("%s response has %d members, want %d", fixture.Input, len(shape), wantKeys)
		}
		if _, ok := shape["additional_context"]; !ok {
			return fmt.Errorf("%s response has no model context", fixture.Input)
		}
		if event.Name == "postToolUse" {
			var replacement string
			if err := json.Unmarshal(shape["updated_mcp_tool_output"], &replacement); err != nil || replacement != string(updated) {
				return fmt.Errorf("%s replacement did not preserve the fixture result", fixture.Input)
			}
			var original, replaced struct {
				IsError json.RawMessage `json:"isError"`
			}
			if err := json.Unmarshal(updated, &original); err != nil {
				return fmt.Errorf("%s fixture result is invalid: %w", fixture.Input, err)
			}
			if err := json.Unmarshal([]byte(replacement), &replaced); err != nil || string(original.IsError) != string(replaced.IsError) {
				return fmt.Errorf("%s replacement changed fixture error state", fixture.Input)
			}
		}
	}
	return nil
}
