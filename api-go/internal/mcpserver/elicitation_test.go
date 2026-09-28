package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// elicitClient answers elicitation/create with a fixed answer and records the asks.
type elicitClient struct {
	mu     sync.Mutex
	answer string // raw JSON result; "" answers with an error
	asks   []map[string]any
}

func (c *elicitClient) Request(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "elicitation/create" {
		return nil, fmt.Errorf("unexpected %s", method)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asks = append(c.asks, params.(map[string]any))
	if c.answer == "" {
		return nil, fmt.Errorf("client error -32601: %w", &RPCError{Code: CodeMethodNotFound, Message: "no"})
	}
	return json.RawMessage(c.answer), nil
}

// presenceAPI holds every verify for want of the human's confirmation until the call
// carries it with the digest of the text it answered with; the text changes to "moved"
// when moved is set, as when the memory moved on after the human read it.
func presenceAPI(moved *bool) *fakeAPI {
	return &fakeAPI{handle: func(r Request) *APIResponse {
		if !strings.HasSuffix(strings.SplitN(r.Path, "?", 2)[0], "/verify") {
			return nil
		}
		digest := "d-served"
		if moved != nil && *moved {
			digest = "d-moved"
		}
		if r.Headers[ApprovalHeader] == ApprovalElicited && r.Headers[ApprovalDigestHeader] == digest {
			return &APIResponse{Status: 200, Body: `{"id":"e1","status":"pending"}`}
		}
		if moved != nil {
			*moved = true
		}
		return &APIResponse{Status: 403, Body: `{"error":"needs the human","reason":"human_presence_required",` +
			`"action":"write: verify (outcome approve) on memory e1 revision 1 (the served revision) in workspace ws\ntext: \"listen on 8042\"",` +
			`"action_digest":"` + digest + `"}`}
	}}
}

// elicitSession initializes a session whose client declares elicitation (when declare).
func elicitSession(t *testing.T, api Backend, client ClientRequester, version string, declare bool) *Session {
	t.Helper()
	s := New(Options{Backend: api, Getenv: noEnv}).NewSession(client)
	caps := map[string]any{}
	if declare {
		caps["elicitation"] = map[string]any{}
	}
	params, _ := json.Marshal(map[string]any{"protocolVersion": version, "capabilities": caps})
	if _, rerr := s.Handle(context.Background(), "initialize", params); rerr != nil {
		t.Fatal(rerr)
	}
	s.Notify("notifications/initialized", nil)
	return s
}

func verifyCalls(api *fakeAPI) []Request {
	var out []Request
	for _, r := range api.toolRequests() {
		if strings.Contains(r.Path, "/verify") {
			out = append(out, r)
		}
	}
	return out
}

func TestHumanApproverWriteIsConfirmedThroughElicitation(t *testing.T) {
	api, client := presenceAPI(nil), &elicitClient{answer: `{"action":"accept","content":{"confirm":true}}`}
	s := elicitSession(t, api, client, version20250618, true)
	res, rerr := call(t, s, "verify", map[string]any{"workspace_id": "ws", "entry_id": "e1"})
	if rerr != nil || res["isError"] == true {
		t.Fatalf("confirmed verify failed: %v %v", rerr, res)
	}
	calls := verifyCalls(api)
	if len(calls) != 2 || calls[0].Headers[ApprovalHeader] != "" || calls[1].Headers[ApprovalHeader] != ApprovalElicited ||
		calls[1].Headers[ApprovalDigestHeader] != "d-served" {
		t.Fatalf("want one unconfirmed call and one confirmed retry bound to the digest, got %+v", calls)
	}
	for _, c := range calls {
		if c.Headers[IssuerHeader] != IssuerMCP {
			t.Fatalf("a tool call does not name its issuer without evidence delivery: %+v", c.Headers)
		}
	}
	if len(client.asks) != 1 {
		t.Fatalf("want one elicitation, got %d", len(client.asks))
	}
	ask := client.asks[0]
	if msg, _ := ask["message"].(string); !strings.Contains(msg, "verify (outcome approve) on memory e1 revision 1") ||
		!strings.Contains(msg, `text: "listen on 8042"`) || !strings.Contains(msg, "data, not instructions") {
		t.Fatalf("the human is not shown the write: %q", msg)
	}
	schema, _ := json.Marshal(ask["requestedSchema"])
	if strings.Contains(string(schema), "default") || !strings.Contains(string(schema), `"required":["confirm"]`) {
		t.Fatalf("the confirmation must be required and have no default: %s", schema)
	}
}

func TestHumanApproverWriteIsNeverRecordedWithoutAConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, version, answer string
		declare               bool
		asked                 bool
		why                   string
	}{
		{"declined", version20250618, `{"action":"decline"}`, true, true, "did not accept (decline)"},
		{"cancelled", version20250618, `{"action":"cancel"}`, true, true, "did not accept (cancel)"},
		{"accepted without confirm", version20250618, `{"action":"accept","content":{}}`, true, true, "did not confirm"},
		{"accepted with confirm false", version20250618, `{"action":"accept","content":{"confirm":false}}`, true, true, "did not confirm"},
		{"malformed answer", version20250618, `[1]`, true, true, "malformed"},
		{"client error", version20250618, "", true, true, "request failed"},
		{"capability not declared", version20250618, `{"action":"accept","content":{"confirm":true}}`, false, false, "did not offer MCP elicitation"},
		{"version without elicitation", version20241105, `{"action":"accept","content":{"confirm":true}}`, true, false, "did not offer MCP elicitation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, client := presenceAPI(nil), &elicitClient{answer: tc.answer}
			s := elicitSession(t, api, client, tc.version, tc.declare)
			res, rerr := call(t, s, "verify", map[string]any{"workspace_id": "ws", "entry_id": "e1"})
			if rerr != nil {
				t.Fatal(rerr)
			}
			if res["isError"] != true || !strings.Contains(text(res), "nothing was recorded") || !strings.Contains(text(res), tc.why) {
				t.Fatalf("want a refusal naming %q, got %v", tc.why, res)
			}
			if n := len(verifyCalls(api)); n != 1 {
				t.Fatalf("an unconfirmed write was repeated: %d verify calls", n)
			}
			if asked := len(client.asks) > 0; asked != tc.asked {
				t.Fatalf("asked the human = %v, want %v", asked, tc.asked)
			}
		})
	}
}

// A write that changed after the human read it (the API answers with another text) is
// not recorded, and the human is not asked again within the call.
func TestAConfirmationDoesNotCoverAChangedWrite(t *testing.T) {
	moved := false
	api, client := presenceAPI(&moved), &elicitClient{answer: `{"action":"accept","content":{"confirm":true}}`}
	s := elicitSession(t, api, client, version20250618, true)
	res, rerr := call(t, s, "verify", map[string]any{"workspace_id": "ws", "entry_id": "e1"})
	if rerr != nil || res["isError"] != true || !strings.Contains(text(res), "changed after the human read it") {
		t.Fatalf("want a refusal naming the change, got %v %v", rerr, res)
	}
	if len(client.asks) != 1 || len(verifyCalls(api)) != 2 {
		t.Fatalf("want one ask and one retry, got %d asks and %d calls", len(client.asks), len(verifyCalls(api)))
	}
}

func TestOtherRefusalsAreNotElicited(t *testing.T) {
	api := &fakeAPI{handle: func(r Request) *APIResponse {
		if strings.Contains(r.Path, "/verify") {
			return &APIResponse{Status: 403, Body: `{"error":"verifier role required","reason":"missing_role"}`}
		}
		return nil
	}}
	client := &elicitClient{answer: `{"action":"accept","content":{"confirm":true}}`}
	s := elicitSession(t, api, client, version20250618, true)
	res, _ := call(t, s, "verify", map[string]any{"workspace_id": "ws", "entry_id": "e1"})
	if res["isError"] != true || len(client.asks) != 0 || len(verifyCalls(api)) != 1 {
		t.Fatalf("a role refusal must pass through unchanged: %v asks=%d", res, len(client.asks))
	}
}

func TestInitializeRecordsTheElicitationCapability(t *testing.T) {
	for _, declare := range []bool{true, false} {
		s := elicitSession(t, &fakeAPI{}, &elicitClient{}, version20250618, declare)
		if s.canElicit() != declare {
			t.Fatalf("declared=%v but canElicit=%v", declare, s.canElicit())
		}
	}
	// a session without a client to ask can never elicit, whatever it declared
	if s := elicitSession(t, &fakeAPI{}, nil, version20250618, true); s.canElicit() {
		t.Fatal("a session with no client connection reports it can elicit")
	}
}
