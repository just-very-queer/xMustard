package mcpserver

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"time"
)

// Human confirmation through MCP elicitation (WS-57). A human approver's token lets
// whoever holds it act as the human, and an MCP client runs a model that calls tools on
// its own. So the API refuses a governed-memory write made through this bridge under a
// human approver's token (reason human_presence_required) unless the bridge says the
// human confirmed it. The bridge asks the human with elicitation/create, only when the
// client declared the elicitation capability in a protocol version that has it, and
// repeats the call once with the confirmation header. Nothing approves by default: a
// decline, a cancel, a missing or false confirm, a malformed answer, a timeout, or a
// client that cannot be asked leaves the write unrecorded and says so. The header is
// this bridge's word, not proof of presence, so the API labels such a write advisory.
// No tool, argument or tools/list byte is added: the confirmation rides on the call the
// agent already made.

// Request headers the bridge sends and the API reads.
const (
	// IssuerHeader marks a call made by this bridge (IssuerMCP).
	IssuerHeader = "X-Xmustard-Issuer"
	IssuerMCP    = "mcp"
	// ApprovalHeader carries ApprovalElicited when the human confirmed the call.
	ApprovalHeader   = "X-Xmustard-Approval"
	ApprovalElicited = "elicitation"
	// ReasonHumanPresence is the reason of the API's refusal of an unconfirmed human
	// approver's write that arrived through MCP.
	ReasonHumanPresence = "human_presence_required"
)

// elicitTimeout bounds how long a confirmation waits for the human.
const elicitTimeout = 10 * time.Minute

// maxElicitAction bounds the action text shown to the human.
const maxElicitAction = 300

// call makes the API request. When the API refuses it for want of the human
// approver's confirmation, it asks the human and repeats the request once, confirmed;
// refusal is then the tool result to return instead (nothing was recorded).
func (s *Session) call(ctx context.Context, req Request) (resp *APIResponse, refusal map[string]any, err error) {
	resp, err = s.srv.opts.Backend.Do(ctx, req)
	action, asked := presenceRefusal(resp)
	if err != nil || !asked {
		return resp, nil, err
	}
	if refusal = s.confirmHuman(ctx, action); refusal != nil {
		return nil, refusal, nil
	}
	req.Headers = maps.Clone(req.Headers)
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	req.Headers[ApprovalHeader] = ApprovalElicited
	resp, err = s.srv.opts.Backend.Do(ctx, req)
	return resp, nil, err
}

// presenceRefusal reads an API refusal that asks for the human's confirmation and
// returns the action it describes.
func presenceRefusal(resp *APIResponse) (string, bool) {
	if resp == nil || resp.Status != http.StatusForbidden {
		return "", false
	}
	var body struct {
		Reason string `json:"reason"`
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(resp.Body), &body) != nil || body.Reason != ReasonHumanPresence {
		return "", false
	}
	return body.Action, true
}

// canElicit reports whether the client can be asked: it declared elicitation, the
// negotiated version has it (2025-06-18), and it finished initialization.
func (s *Session) canElicit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client != nil && s.elicitCap && s.initialized && atLeast(s.version, version20250618)
}

// confirmHuman asks the human to confirm action. It returns nil only for an explicit
// accept with confirm=true; anything else is the tool result saying nothing was
// recorded and why.
func (s *Session) confirmHuman(ctx context.Context, action string) map[string]any {
	refuse := func(why string) map[string]any {
		return TextResult("xmustard: nothing was recorded: "+why+". This call used a human approver's token, and "+
			"over MCP such a write needs the human's confirmation in the client; a human can also act from a "+
			"terminal with xmustard-ops approve|reject.", true)
	}
	if !s.canElicit() {
		return refuse("this client did not offer MCP elicitation")
	}
	ctx, cancel := context.WithTimeout(ctx, elicitTimeout)
	defer cancel()
	raw, err := s.client.Request(ctx, "elicitation/create", elicitationParams(action))
	if err != nil {
		return refuse("the confirmation request failed (" + err.Error() + ")")
	}
	var answer struct {
		Action  string `json:"action"`
		Content struct {
			Confirm *bool `json:"confirm"`
		} `json:"content"`
	}
	switch {
	case json.Unmarshal(raw, &answer) != nil:
		return refuse("the confirmation answer is malformed")
	case answer.Action != "accept":
		return refuse("the human did not accept (" + clip(answer.Action, 20) + ")")
	case answer.Content.Confirm == nil || !*answer.Content.Confirm:
		return refuse("the human did not confirm")
	}
	return nil
}

// elicitationParams asks for one required boolean with no default, so a client can
// never submit a confirmation the human did not give.
func elicitationParams(action string) map[string]any {
	return map[string]any{
		"message": "xMustard: an agent asks to " + clip(action, maxElicitAction) + " with your human-approver token. " +
			"It is recorded as your decision only if you confirm.",
		"requestedSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{"type": "boolean", "title": "Confirm", "description": "true records it as your decision"},
			},
			"required": []string{"confirm"},
		},
	}
}
