package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The output contract Codex parses a command hook's stdout with: the
// *CommandOutputWire structs of codex-rs hooks/src/schema.rs (all deny_unknown_fields)
// and the rules engine/output_parser.rs adds. ValidateCodexAnswer checks an answer
// against it without trusting codexReply, as evidence.ValidateShape does for payloads;
// the golden session and the live conformance test run every Codex answer through it.

// codexUniversalKeys are the keys every output wire flattens in (HookUniversalOutputWire).
var codexUniversalKeys = []string{"continue", "stopReason", "suppressOutput", "systemMessage"}

// codexOutputKeys are, per event, the keys its wire takes besides the universal ones,
// and those its hookSpecificOutput takes (nil: it has none). SessionEnd has no output
// wire: Codex does not read its stdout.
var codexOutputKeys = map[string]struct{ top, specific []string }{
	"SessionStart":     {nil, []string{"hookEventName", "additionalContext"}},
	"SubagentStart":    {nil, []string{"hookEventName", "additionalContext"}},
	"UserPromptSubmit": {[]string{"decision", "reason"}, []string{"hookEventName", "additionalContext"}},
	"PreToolUse": {[]string{"decision", "reason"},
		[]string{"hookEventName", "permissionDecision", "permissionDecisionReason", "updatedInput", "additionalContext"}},
	"PostToolUse":  {[]string{"decision", "reason"}, []string{"hookEventName", "additionalContext", "updatedMCPToolOutput"}},
	"PreCompact":   {},
	"PostCompact":  {},
	"Stop":         {[]string{"decision", "reason"}, nil},
	"SubagentStop": {[]string{"decision", "reason"}, nil},
}

// codexKeyKinds are the JSON kinds of the wire's fields: 'b' bool, 's' string, 'a'
// any value. Every field but hookEventName is an Option, so null is accepted too.
var codexKeyKinds = map[string]byte{
	"continue": 'b', "suppressOutput": 'b', "stopReason": 's', "systemMessage": 's', "decision": 's', "reason": 's',
	"hookEventName": 's', "additionalContext": 's', "permissionDecision": 's', "permissionDecisionReason": 's',
	"updatedInput": 'a', "updatedMCPToolOutput": 'a', "hookSpecificOutput": 'a',
}

// codexUnsupported are fields the wire takes that Codex then refuses for an event,
// failing the hook run (output_parser.rs unsupported_*).
var codexUnsupported = map[string][]string{"PostToolUse": {"updatedMCPToolOutput", "suppressOutput"}}

// ValidateCodexAnswer reports whether Codex would accept body as event's hook output.
// An empty body is always accepted (no output).
func ValidateCodexAnswer(event string, body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	keys, ok := codexOutputKeys[event]
	if !ok {
		return fmt.Errorf("codex: %s takes no output", event)
	}
	top, err := codexObject(body, append(slices.Clone(codexUniversalKeys), keys.top...), keys.specific != nil)
	if err != nil {
		return err
	}
	if err := codexDecision(top); err != nil {
		return err
	}
	for _, k := range codexUnsupported[event] {
		if _, ok := top[k]; ok {
			return fmt.Errorf("codex: %s returned unsupported %s", event, k)
		}
	}
	raw, ok := top["hookSpecificOutput"]
	if !ok || string(raw) == "null" {
		return nil
	}
	specific, err := codexObject(raw, keys.specific, false)
	if err != nil {
		return fmt.Errorf("hookSpecificOutput: %w", err)
	}
	var name string
	if json.Unmarshal(specific["hookEventName"], &name) != nil || name != event {
		return fmt.Errorf("codex: hookSpecificOutput.hookEventName %s, want %q", specific["hookEventName"], event)
	}
	for _, k := range codexUnsupported[event] {
		if _, ok := specific[k]; ok {
			return fmt.Errorf("codex: %s returned unsupported %s", event, k)
		}
	}
	return nil
}

// codexObject decodes a JSON object whose keys must be among allowed (and
// hookSpecificOutput when specific), each of its field's kind.
func codexObject(raw []byte, allowed []string, specific bool) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("codex: not a JSON object: %s", raw)
	}
	for k, v := range obj {
		if !slices.Contains(allowed, k) && !(specific && k == "hookSpecificOutput") {
			return nil, fmt.Errorf("codex: unknown field %q (the wire denies unknown fields)", k)
		}
		if !codexKind(codexKeyKinds[k], v, k != "hookEventName") {
			return nil, fmt.Errorf("codex: field %q is %s", k, v)
		}
	}
	return obj, nil
}

// codexKind reports whether a value has the kind (null when nullable).
func codexKind(kind byte, v json.RawMessage, nullable bool) bool {
	s := strings.TrimSpace(string(v))
	if s == "null" {
		return nullable
	}
	switch kind {
	case 'b':
		return s == "true" || s == "false"
	case 's':
		return strings.HasPrefix(s, `"`)
	}
	return true
}

// codexDecision applies the decision rules: the only decision xMustard's events take
// is "block" (PreToolUse's "approve" is unsupported), a block needs a non-empty
// reason, and a reason needs a decision.
func codexDecision(top map[string]json.RawMessage) error {
	var decision, reason *string
	_ = json.Unmarshal(top["decision"], &decision)
	_ = json.Unmarshal(top["reason"], &reason)
	switch {
	case decision == nil && reason != nil:
		return fmt.Errorf("codex: reason without decision")
	case decision == nil:
		return nil
	case *decision != "block":
		return fmt.Errorf("codex: decision %q", *decision)
	case reason == nil || strings.TrimSpace(*reason) == "":
		return fmt.Errorf("codex: decision block without a non-empty reason")
	}
	return nil
}
