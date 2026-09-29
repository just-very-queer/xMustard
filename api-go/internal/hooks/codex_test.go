package hooks

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"xmustard/api-go/internal/evidence"
)

// Every Codex answer decodes into the output wire of its event with unknown fields
// denied: a block and context for PostToolUse, context alone elsewhere, nothing for the
// queued events, and never a Claude Code key (watchPaths, updatedToolOutput).
func TestCodexReplyWritesOnlyTheContractKeys(t *testing.T) {
	block := json.RawMessage(`{"decision":"block","reason":"reduced\n[xmustard evidence] {\"handle\":\"xm1.a\"}"}`)
	for _, ev := range CodexEvents {
		reply := Codex.Reply(ev, Result{Context: "ctx", Updated: block, Watch: []string{"/r/a.go"}})
		if ev.Mode == Enqueue || !ev.Context {
			if reply != nil {
				t.Errorf("%s answered %s", ev.Name, reply.Body)
			}
			continue
		}
		if reply == nil {
			t.Fatalf("%s said nothing", ev.Name)
		}
		if err := ValidateCodexAnswer(ev.Name, reply.Body); err != nil {
			t.Errorf("%s: %v: %s", ev.Name, err, reply.Body)
		}
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(reply.Body, &keys)
		want := []string{"hookSpecificOutput"}
		if ev.Replaces {
			want = []string{"decision", "hookSpecificOutput", "reason"}
		}
		if got := slices.Sorted(maps.Keys(keys)); !reflect.DeepEqual(got, want) || reply.Context != "ctx" {
			t.Errorf("%s keys %v (context %q), want %v", ev.Name, got, reply.Context, want)
		}
	}
	post, _ := Codex.Event("PostToolUse")
	if got := Codex.Reply(post, Result{Updated: block}); got == nil || string(got.Body) != `{"decision":"block","reason":"reduced\n[xmustard evidence] {\"handle\":\"xm1.a\"}"}` {
		t.Fatalf("block alone: %+v", got)
	}
	// a payload that is not exactly a block with a reason blocks nothing
	for _, bad := range []string{`{"decision":"approve","reason":"x"}`, `{"decision":"block","reason":"  "}`, `{"decision":"block"}`,
		`{"decision":"block","reason":"x","updatedMCPToolOutput":{}}`, `{"stdout":"x"}`, `not json`} {
		if got := Codex.Reply(post, Result{Updated: json.RawMessage(bad)}); got != nil {
			t.Errorf("payload %s answered %s", bad, got.Body)
		}
	}
}

// The validator is the contract, not the builder: it refuses what Codex refuses.
func TestValidateCodexAnswerRefusesWhatCodexRefuses(t *testing.T) {
	for _, c := range []struct{ event, body string }{
		{"SessionStart", `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"g","watchPaths":["/a"]}}`},
		{"PostToolUse", `{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":{"stdout":"x"}}}`},
		{"PostToolUse", `{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedMCPToolOutput":{"content":[]}}}`},
		{"PostToolUse", `{"decision":"block"}`},
		{"PostToolUse", `{"reason":"no decision"}`},
		{"PostToolUse", `{"decision":"approve","reason":"x"}`},
		{"PostToolUse", `{"suppressOutput":true}`},
		{"PreToolUse", `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"x"}}`},
		{"SessionStart", `{"decision":"block","reason":"x"}`},
		{"PostCompact", `{"hookSpecificOutput":{"hookEventName":"PostCompact"}}`},
		{"UserPromptSubmit", `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":7}}`},
		{"SessionEnd", `{"continue":true}`},
		{"PreToolUse", `[1]`},
	} {
		if ValidateCodexAnswer(c.event, []byte(c.body)) == nil {
			t.Errorf("%s accepted %s", c.event, c.body)
		}
	}
	for _, c := range []struct{ event, body string }{
		{"PostToolUse", `{"decision":"block","reason":"r","hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"c"}}`},
		{"PreToolUse", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"c"}}`},
		{"Stop", `{"systemMessage":null}`},
		{"SessionEnd", ``},
	} {
		if err := ValidateCodexAnswer(c.event, []byte(c.body)); err != nil {
			t.Errorf("%s refused %s: %v", c.event, c.body, err)
		}
	}
}

// Codex spills a context past 10,000 bytes: the cap counts bytes, cuts on a rune
// boundary, and prefers a paragraph break.
func TestCodexContextIsCappedInBytes(t *testing.T) {
	s := strings.Repeat("é", 6000) // 12,000 bytes, 6,000 characters: under Claude's cap
	limit := evidence.CodexHookTextBytes
	got := capContext(s, limit)
	if len(got) > limit || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("capped to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	para := strings.Repeat("a", 7000) + "\n\n" + strings.Repeat("b", 5000)
	if got := capContext(para, limit); got != strings.Repeat("a", 7000)+"…" {
		t.Fatalf("paragraph cut: %d bytes", len(got))
	}
	if early := strings.Repeat("a", 100) + "\n\n" + strings.Repeat("b", 12000); capContext(early, limit) != evidence.CapBytes(early, limit) {
		t.Fatal("a paragraph break in the first half cut the context")
	}
	pre, _ := Codex.Event("PreToolUse")
	reply := Codex.Reply(pre, Result{Context: s})
	if reply == nil || len(reply.Context) > limit || ValidateCodexAnswer("PreToolUse", reply.Body) != nil {
		t.Fatalf("reply context %d bytes", len(reply.Context))
	}
}

func TestPatchPathsListsEveryChangedFileOnce(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: src/new.go\n+package x\n*** Update File: src/billing.go\n*** Move to: src/ledger.go\n@@\n-a\n+b\n" +
		"*** Delete File: /abs/old.go\r\n*** Update File: src/billing.go\n*** Update File:   \n*** End Patch\n"
	want := []string{"/r/sub/src/new.go", "/r/sub/src/billing.go", "/r/sub/src/ledger.go", "/abs/old.go"}
	if got := PatchPaths(patch, "/r/sub"); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths %v", got)
	}
	// a Move to renames the Update File before it, and nothing else
	moves := PatchMoves(patch+"*** Add File: a.go\n*** Move to: b.go\n", "/r/sub")
	if want := map[string]string{"/r/sub/src/ledger.go": "/r/sub/src/billing.go"}; !reflect.DeepEqual(moves, want) {
		t.Fatalf("moves %v", moves)
	}
	if got := (Input{ToolName: "apply_patch", Cwd: "/r/sub", ToolInput: ToolInput{Command: patch}}).Moves(); !reflect.DeepEqual(got, moves) {
		t.Fatalf("apply_patch moves %v", got)
	}
	if got := (Input{ToolName: "Bash", ToolInput: ToolInput{Command: patch}}).Moves(); got != nil {
		t.Fatalf("a Bash command moved %v", got)
	}
	in := Input{ToolName: "apply_patch", Cwd: "/r", ToolInput: ToolInput{Command: "*** Update File: a.go\n"}}
	if got := in.Targets(); !reflect.DeepEqual(got, []string{"/r/a.go"}) || ClassOf("apply_patch") != ToolEdit {
		t.Fatalf("apply_patch targets %v", got)
	}
	var many strings.Builder
	for i := range 40 {
		many.WriteString("*** Add File: f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go\n")
	}
	if n := len(PatchPaths(many.String(), "/r")); n != maxPatchPaths {
		t.Fatalf("%d paths from a 40-file patch", n)
	}
	if got := (Input{ToolName: "Edit", ToolInput: ToolInput{FilePath: "/r/x.go"}}).Targets(); !reflect.DeepEqual(got, []string{"/r/x.go"}) {
		t.Fatalf("edit targets %v", got)
	}
}

// The commands hooks.json runs parse back to their event and pin, for the client on
// PATH or at an absolute path; nothing else is taken for xMustard's (trust is offered
// only for these). A relative client, an assignment prefix, a non-printing character
// or a pin the client would not send is refused.
func TestCodexCommandsRoundTrip(t *testing.T) {
	for _, c := range []struct{ bin, ws string }{
		{"xmustard-hook", ""}, {"/opt/xm/bin/xmustard-hook", "xmustard-3f2a"}, {"/Users/a b/it's/xmustard-hook", "ws.1_x"},
	} {
		for _, ev := range CodexEvents {
			cmd := CodexCommand(c.bin, ev.Name, c.ws)
			bin, event, ws, ok := ParseCodexCommand(cmd)
			if !ok || bin != c.bin || event != ev.Name || ws != c.ws {
				t.Fatalf("%q parsed as %q %q %q %v", cmd, bin, event, ws, ok)
			}
		}
	}
	for _, cmd := range []string{
		"xmustard-hook codex PostToolUse; rm -rf ~", "xmustard-hook codex PostToolUse && curl x", "evil codex PostToolUse",
		"xmustard-hook claude PostToolUse", "xmustard-hook codex Stop", "xmustard-hook codex PostToolUse --workspace",
		"xmustard-hook codex PostToolUse --other ws", "xmustard-hook codex PostToolUse --workspace ws extra",
		"'xmustard-hook codex PostToolUse", "$(which xmustard-hook) codex PostToolUse", "xmustard-hook codex $EVENT",
		"bin/xmustard-hook codex PostToolUse", "./xmustard-hook codex SessionStart", "PATH=bin/xmustard-hook codex SessionStart",
		"PATH=/opt/xm/xmustard-hook codex SessionStart", "'x\x1b[2K\r/opt/xm/xmustard-hook' codex PostToolUse",
		"'/opt/\x1b[2K\r/xm/xmustard-hook' codex PostToolUse", "'/opt/\u202egol/xmustard-hook' codex PostToolUse",
		"'/opt/\xff/xmustard-hook' codex PostToolUse", "/opt/xm/xmustard-hook.sh codex PostToolUse",
		"xmustard-hook codex PostToolUse --workspace 'my ws'", "xmustard-hook codex PostToolUse --workspace ../up",
		"xmustard-hook codex PostToolUse --workspace a=b",
	} {
		if _, _, _, ok := ParseCodexCommand(cmd); ok {
			t.Errorf("%q taken for xMustard's", cmd)
		}
	}
	for bin, want := range map[string]bool{"xmustard-hook": true, "/opt/xm/xmustard-hook": true, "/Users/a b/xmustard-hook": true,
		"bin/xmustard-hook": false, "~/bin/xmustard-hook": false, "/opt/xm/xmustard-hook\n": false, "/opt/xm/other": false, "": false} {
		if ValidCodexClient(bin) != want {
			t.Errorf("ValidCodexClient(%q) = %v", bin, !want)
		}
	}
}

// hooks.json is a Codex HooksFile (only description and hooks at the top, command
// handlers with whole-second timeouts) that registers every routed event once.
func TestCodexHooksJSONRegistersEveryRoutedEvent(t *testing.T) {
	raw := CodexHooksJSON("/opt/xm/xmustard-hook", "ws1")
	var file struct {
		Description string `json:"description"`
		Hooks       map[string][]struct {
			Matcher *string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatalf("%v:\n%s", err, raw)
	}
	if len(file.Hooks) != len(CodexEvents) {
		t.Fatalf("%d events registered, %d routed", len(file.Hooks), len(CodexEvents))
	}
	for _, ev := range CodexEvents {
		groups := file.Hooks[ev.Name]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", ev.Name, groups)
		}
		h := groups[0].Hooks[0]
		_, event, ws, ok := ParseCodexCommand(h.Command)
		limit := map[string]int{"SessionEnd": 3}[ev.Name]
		if limit == 0 {
			limit = 10
		}
		if h.Type != "command" || !ok || event != ev.Name || ws != "ws1" || h.Timeout < 1 || h.Timeout > limit {
			t.Errorf("%s: %+v", ev.Name, h)
		}
		if tool := ev.Name == "PreToolUse" || ev.Name == "PostToolUse"; tool != (groups[0].Matcher != nil) {
			t.Errorf("%s matcher %v", ev.Name, groups[0].Matcher)
		}
	}
	if m := *file.Hooks["PostToolUse"][0].Matcher; m != "^(Bash|apply_patch|mcp__.+)$" {
		t.Fatalf("PostToolUse matcher %q", m)
	}
}
