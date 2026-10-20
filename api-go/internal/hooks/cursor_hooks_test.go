package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

// The generated file is the shape Cursor loads: its own event names, its own
// matchers, and the failure policy the owner decided.
func TestCursorHooksJSONRegistersTheRoutedEvents(t *testing.T) {
	var file struct {
		Description string `json:"description"`
		Hooks       map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type       string `json:"type"`
				Command    string `json:"command"`
				FailClosed bool   `json:"failClosed"`
				LoopLimit  int    `json:"loop_limit"`
				Timeout    int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(CursorHooksJSON(CursorClient, ""), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Hooks) != len(CursorEvents) {
		t.Fatalf("the file registers %d events, want %d", len(file.Hooks), len(CursorEvents))
	}
	for _, ev := range CursorEvents {
		groups, ok := file.Hooks[ev.Name]
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s has no single hook", ev.Name)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("%s runs a %q hook", ev.Name, h.Type)
		}
		if h.FailClosed {
			t.Errorf("%s fails closed; the owner's answer is fail open", ev.Name)
		}
		if h.Command != CursorClient+" "+ev.Name {
			t.Errorf("%s runs %q", ev.Name, h.Command)
		}
		if h.LoopLimit != CursorLoopLimit || h.Timeout != CursorTimeout {
			t.Errorf("%s carries loop_limit %d and timeout %d", ev.Name, h.LoopLimit, h.Timeout)
		}
	}
	if !strings.Contains(file.Description, "failClosed is false") {
		t.Error("the file does not state the failure policy it was written against")
	}
}

// A pin is the workspace id the daemon resolves, and it is quoted: the command
// runs in a shell Cursor spawns.
func TestCursorHooksJSONPinsWithAQuotedWorkspaceID(t *testing.T) {
	body := string(CursorHooksJSON(CursorClient, "ws-cursor"))
	if !strings.Contains(body, CursorClient+" postToolUse --workspace ws-cursor") {
		t.Errorf("the pin is missing: %s", body)
	}
	// A word with a space is one quoted word, so the shell does not split it.
	body = string(CursorHooksJSON(CursorClient, "ws cursor"))
	if !strings.Contains(body, `--workspace 'ws cursor'`) {
		t.Errorf("a pin with a space is not quoted: %s", body)
	}
}

// The client a file runs must be the one xMustard ships, or an absolute path to it:
// a relative path or a NAME=value word would run what the repository holds, and a
// control character could repaint the line that shows the command.
func TestValidCursorClient(t *testing.T) {
	if !ValidCursorClient(CursorClient) {
		t.Errorf("%s is not a valid client", CursorClient)
	}
	if !ValidCursorClient("/opt/xmustard/bin/xmustard-cursor") {
		t.Error("an absolute path to the client is not valid")
	}
	for _, bad := range []string{
		"bin/xmustard-cursor",
		"xmustard-cursor --evil",
		"/x/xmustard-cursor\nrm -rf /",
		"/x/other",
		"",
	} {
		if ValidCursorClient(bad) {
			t.Errorf("%q is a valid client", bad)
		}
	}
}
