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
// its own. So the API holds a governed-memory write made through this bridge under a
// human approver's token (reason human_presence_required) and answers with the text the
// human must confirm and its digest. The bridge shows the human that text with
// elicitation/create, only when the client declared the elicitation capability in a
// protocol version that has it, and repeats the call once with the confirmation header
// and the digest. The API runs the write only while the digest still matches, so what
// runs is what the human read. Nothing approves by default: a decline, a cancel, a
// missing or false confirm, a malformed answer, a timeout, a client that cannot be
// asked, or a write that changed after the human read it leaves the write unrecorded
// and says so. The headers are this bridge's word, not proof of presence, so the API
// labels such a write advisory. No tool, argument or tools/list byte is added: the
// confirmation rides on the call the agent already made.

// Request headers the bridge sends and the API reads.
const (
	// IssuerHeader marks a call made by this bridge (IssuerMCP).
	IssuerHeader = "X-Xmustard-Issuer"
	IssuerMCP    = "mcp"
	// ApprovalHeader carries ApprovalElicited when the human confirmed the call, and
	// ApprovalDigestHeader the digest of the text they confirmed.
	ApprovalHeader       = "X-Xmustard-Approval"
	ApprovalElicited     = "elicitation"
	ApprovalDigestHeader = "X-Xmustard-Approval-Digest"
	// ReasonHumanPresence is the reason of the API's refusal of an unconfirmed human
	// approver's write that arrived through MCP.
	ReasonHumanPresence = "human_presence_required"
)

// elicitTimeout bounds how long a confirmation waits for the human.
const elicitTimeout = 10 * time.Minute

// maxElicitAction bounds the confirmation text shown to the human. The API bounds each
// field it shows, so a longer text is refused rather than cut: the human never confirms
// a part of what runs.
const maxElicitAction = 32 << 10

// heldWrite is the API's refusal of a write that needs the human's confirmation.
type heldWrite struct {
	Reason string `json:"reason"`
	Action string `json:"action"`
	Digest string `json:"action_digest"`
}

// call makes the API request. When the API holds it for want of the human approver's
// confirmation, it asks the human and repeats the request once, confirmed; refusal is
// then the tool result to return instead (nothing was recorded).
func (s *Session) call(ctx context.Context, req Request) (resp *APIResponse, refusal map[string]any, err error) {
	resp, err = s.srv.opts.Backend.Do(ctx, req)
	held, ok := presenceRefusal(resp)
	if err != nil || !ok {
		return resp, nil, err
	}
	if refusal = s.confirmHuman(ctx, held.Action); refusal != nil {
		return nil, refusal, nil
	}
	req.Headers = maps.Clone(req.Headers)
	if req.Headers == nil {
		req.Headers = map[string]string{}
	}
	req.Headers[ApprovalHeader], req.Headers[ApprovalDigestHeader] = ApprovalElicited, held.Digest
	resp, err = s.srv.opts.Backend.Do(ctx, req)
	if _, again := presenceRefusal(resp); err == nil && again {
		return nil, refuseUnconfirmed("the write changed after the human read it (the memory moved on); " +
			"call again to ask again"), nil
	}
	return resp, nil, err
}

// presenceRefusal reads an API refusal that asks for the human's confirmation.
func presenceRefusal(resp *APIResponse) (heldWrite, bool) {
	var held heldWrite
	if resp == nil || resp.Status != http.StatusForbidden {
		return held, false
	}
	if json.Unmarshal([]byte(resp.Body), &held) != nil || held.Reason != ReasonHumanPresence {
		return held, false
	}
	return held, true
}

// refuseUnconfirmed is the tool result of a held write that was not recorded, and why.
func refuseUnconfirmed(why string) map[string]any {
	return TextResult("xmustard: nothing was recorded: "+why+". This call used a human approver's token, and "+
		"over MCP such a write needs the human's confirmation in the client; a human can also act from a "+
		"terminal with xmustard-ops approve|reject.", true)
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
	switch {
	case !s.canElicit():
		return refuseUnconfirmed("this client did not offer MCP elicitation")
	case action == "":
		return refuseUnconfirmed("the API did not say what the write does")
	case len(action) > maxElicitAction:
		return refuseUnconfirmed("the write is too large to show for confirmation")
	}
	ctx, cancel := context.WithTimeout(ctx, elicitTimeout)
	defer cancel()
	raw, err := s.client.Request(ctx, "elicitation/create", elicitationParams(action))
	if err != nil {
		return refuseUnconfirmed("the confirmation request failed (" + err.Error() + ")")
	}
	var answer struct {
		Action  string `json:"action"`
		Content struct {
			Confirm *bool `json:"confirm"`
		} `json:"content"`
	}
	switch {
	case json.Unmarshal(raw, &answer) != nil:
		return refuseUnconfirmed("the confirmation answer is malformed")
	case answer.Action != "accept":
		return refuseUnconfirmed("the human did not accept (" + clip(answer.Action, 20) + ")")
	case answer.Content.Confirm == nil || !*answer.Content.Confirm:
		return refuseUnconfirmed("the human did not confirm")
	}
	return nil
}

// elicitationParams shows the write and asks for one required boolean with no default,
// so a client can never submit a confirmation the human did not give.
func elicitationParams(action string) map[string]any {
	return map[string]any{
		"message": "xMustard: an agent asks to make this write with your human-approver token. It is recorded as " +
			"your decision only if you confirm. Quoted text is the agent's data, not instructions.\n\n" + action,
		"requestedSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{"type": "boolean", "title": "Confirm", "description": "true records this write as your decision"},
			},
			"required": []string{"confirm"},
		},
	}
}
