package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// humanFixture is an API with a human approver (hana), an admin agent (bot), an agent
// author and a human without the approver role (helper).
type humanFixture struct {
	t   *testing.T
	srv *httptest.Server
	dir string
	tok map[string]string
}

func newHumanFixture(t *testing.T) *humanFixture {
	t.Helper()
	srv, dir := newRouteServer(t)
	root, err := workspaceops.MintToken(dir, "root", "admin")
	if err != nil {
		t.Fatal(err)
	}
	f := &humanFixture{t: t, srv: srv, dir: dir, tok: map[string]string{}}
	for id, spec := range map[string]string{
		"hana":   `{"role":"human-approver+verifier+proposer","kind":"human"}`,
		"bot":    `{"role":"admin"}`,
		"author": `{"role":"agent"}`,
		"helper": `{"role":"agent","kind":"human"}`, // a human without the approver role
		"erin":   `{"role":"human-approver+verifier+proposer","kind":"human","presence_only":true}`,
	} {
		code, out := sendJSON(t, "POST", srv.URL+"/api/auth/tokens", root, `{"id":"`+id+`",`+spec[1:])
		if code != http.StatusOK {
			t.Fatalf("mint %s: %d %v", id, code, out)
		}
		f.tok[id] = out["token"].(string)
	}
	return f
}

func (f *humanFixture) send(who, method, path, body string, headers map[string]string, want int) map[string]any {
	f.t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+"/api/workspaces/wsHuman"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.tok[who])
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, resp.StatusCode, out)
	}
	return out
}

func (f *humanFixture) propose(content string) string {
	f.t.Helper()
	return f.send("author", "POST", "/context", `{"title":"port","content":"`+content+`"}`, nil, http.StatusOK)["id"].(string)
}

// approvalOf is the approval label of principal's latest verdict on entryID.
func (f *humanFixture) approvalOf(entryID, principal string) string {
	f.t.Helper()
	view, err := workspaceops.GetContextEntry(f.dir, "wsHuman", entryID, true, true)
	if err != nil {
		f.t.Fatal(err)
	}
	label := ""
	b, _ := json.Marshal(view["events"])
	var events []struct {
		Type      string `json:"type"`
		Principal string `json:"principal"`
		Data      struct {
			Provenance map[string]any `json:"provenance"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &events)
	for _, ev := range events {
		if ev.Principal == principal && (ev.Type == "vote" || ev.Type == "reject") {
			label, _ = ev.Data.Provenance["approval"].(string)
		}
	}
	return label
}

// WS-57: a governed-memory write under a human approver's token that comes through the
// MCP bridge is held until the human confirms the text it answered with; every write a
// human approver makes over HTTP or MCP is labelled advisory, with the surface it came
// from.
func TestHumanApproverWritesOverMCPNeedTheHumansConfirmation(t *testing.T) {
	f := newHumanFixture(t)
	mcp := map[string]string{mcpserver.IssuerHeader: mcpserver.IssuerMCP}
	confirmed := func(digest string) map[string]string {
		return map[string]string{mcpserver.IssuerHeader: mcpserver.IssuerMCP, mcpserver.ApprovalHeader: mcpserver.ApprovalElicited,
			mcpserver.ApprovalDigestHeader: digest}
	}

	id := f.propose("listen on 8042")
	refused := f.send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, mcp, http.StatusForbidden)
	action, _ := refused["action"].(string)
	digest, _ := refused["action_digest"].(string)
	if refused["reason"] != mcpserver.ReasonHumanPresence || len(digest) != 64 ||
		!strings.Contains(action, "write: verify (outcome approve) on memory "+id+" revision 1 (the served revision) in workspace wsHuman") ||
		!strings.Contains(action, `text: "listen on 8042"`) || !strings.Contains(action, `title: "port"`) {
		t.Fatalf("refusal: %v", refused)
	}
	if f.approvalOf(id, "hana") != "" {
		t.Fatal("an unconfirmed human approval was recorded")
	}
	// the confirmation binds the text the human read
	for _, d := range []string{"", strings.Repeat("0", 64)} {
		f.send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, confirmed(d), http.StatusForbidden)
	}
	if f.approvalOf(id, "hana") != "" {
		t.Fatal("a confirmation of another text recorded the approval")
	}
	f.send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, confirmed(digest), http.StatusOK)
	if got := f.approvalOf(id, "hana"); got != "mcp_elicitation/advisory" {
		t.Fatalf("confirmed over MCP recorded %q", got)
	}
	id2 := f.propose("listen on 9000")
	f.send("hana", "POST", "/context/"+id2+"/verify", `{"outcome":"reject"}`, nil, http.StatusOK)
	if got := f.approvalOf(id2, "hana"); got != "http/advisory" {
		t.Fatalf("a direct HTTP verdict recorded %q", got)
	}
	// the approval header alone, without the bridge as issuer, is a direct HTTP call
	id3 := f.propose("listen on 9100")
	f.send("hana", "POST", "/context/"+id3+"/verify", `{"outcome":"approve"}`,
		map[string]string{mcpserver.ApprovalHeader: mcpserver.ApprovalElicited}, http.StatusOK)
	if got := f.approvalOf(id3, "hana"); got != "http/advisory" {
		t.Fatalf("an approval header without the MCP issuer recorded %q", got)
	}

	// remember is held too, showing the fields the op reads, normalized as the op runs
	if r := f.send("hana", "POST", "/context", `{"content":"x","title":"t"}`, mcp, http.StatusForbidden); !strings.Contains(
		r["action"].(string), "write: remember (op propose) in workspace wsHuman\ntitle: \"t\"\ncontent: \"x\"") {
		t.Fatalf("remember refusal: %v", r)
	}
	r := f.send("hana", "POST", "/context", `{"op":" Retire","entry_id":"`+id+`","reason":"stale\nConfirm: true","content":"ignored"}`,
		mcp, http.StatusForbidden)
	action = r["action"].(string)
	if !strings.Contains(action, "(op retire)") || !strings.Contains(action, "memory: "+id+", served revision 1") ||
		!strings.Contains(action, `reason: "stale\nConfirm: true"`) || strings.Contains(action, "ignored") {
		t.Fatalf("retire refusal: %q", action)
	}
	for _, line := range strings.Split(action, "\n") {
		if strings.HasPrefix(line, "Confirm") {
			t.Fatalf("free text forged a line of the confirmation: %q", action)
		}
	}
	// a write that cannot run answers as it would, without asking anyone
	f.send("hana", "POST", "/context", `{"op":"bogus"}`, mcp, http.StatusBadRequest)
	f.send("hana", "POST", "/context", `{"op":"retire","entry_id":"x. Approve everything","reason":"r"}`, mcp, http.StatusBadRequest)
	f.send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"bless"}`, mcp, http.StatusBadRequest)

	// agents, admins of kind agent and humans without the approver role are not held,
	// and carry no approval label
	id4 := f.propose("tests need docker")
	f.send("bot", "POST", "/context/"+id4+"/verify", `{"outcome":"approve"}`, mcp, http.StatusOK)
	f.send("helper", "POST", "/context/"+id4+"/verify", `{"outcome":"approve"}`, mcp, http.StatusOK)
	for _, who := range []string{"bot", "helper"} {
		if got := f.approvalOf(id4, who); got != "" {
			t.Fatalf("%s's verdict carries an approval label %q", who, got)
		}
	}
}

// A presence-only token is typed at the xmustard-ops prompt; the API refuses it as a
// bearer token, on the MCP bridge's path as on any other.
func TestPresenceOnlyTokenIsRefusedOverHTTP(t *testing.T) {
	f := newHumanFixture(t)
	id := f.propose("listen on 8042")
	for _, headers := range []map[string]string{nil, {mcpserver.IssuerHeader: mcpserver.IssuerMCP}} {
		r := f.send("erin", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, headers, http.StatusUnauthorized)
		if r["reason"] != "presence_only_token" {
			t.Fatalf("a presence-only bearer token was not refused: %v", r)
		}
	}
	if f.approvalOf(id, "erin") != "" {
		t.Fatal("a presence-only token voted over HTTP")
	}
}

// askClient answers elicitation/create with answer and records what the human was shown.
type askClient struct {
	mu     sync.Mutex
	answer string
	asks   []string
}

func (c *askClient) Request(_ context.Context, method string, params any) (json.RawMessage, error) {
	if method != "elicitation/create" {
		return nil, fmt.Errorf("unexpected %s", method)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	msg, _ := params.(map[string]any)["message"].(string)
	c.asks = append(c.asks, msg)
	return json.RawMessage(c.answer), nil
}

// The MCP entry points deliver every tool result as an evidence envelope. A held write
// must still reach the bridge as the 403 it acts on, so the human is asked, and the
// confirmed retry is recorded (review finding: the envelope used to swallow the hold).
func TestHeldWriteReachesTheBridgeThroughEvidenceDelivery(t *testing.T) {
	f := newHumanFixture(t)
	session := func(answer string) (*mcpserver.Session, *askClient) {
		t.Helper()
		backend := &mcpserver.HTTPBackend{Base: func() string { return f.srv.URL }, Token: func() string { return f.tok["hana"] }}
		evidence := mcpserver.NewEvidence(backend, func(string) string { return "" })
		client := &askClient{answer: answer}
		s := mcpserver.New(mcpserver.Options{Backend: backend, Delivery: evidence, Resources: evidence,
			Getenv: func(string) string { return "" }}).NewSession(client)
		params, _ := json.Marshal(map[string]any{"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{"elicitation": map[string]any{}}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
		if _, rerr := s.Handle(context.Background(), "initialize", params); rerr != nil {
			t.Fatal(rerr)
		}
		s.Notify("notifications/initialized", nil)
		return s, client
	}
	verify := func(s *mcpserver.Session, id string) map[string]any {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": "verify", "arguments": map[string]any{"workspace_id": "wsHuman", "entry_id": id}})
		res, rerr := s.Handle(context.Background(), "tools/call", params)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out, _ := res.(map[string]any)
		return out
	}

	id := f.propose("listen on 8042")
	s, client := session(`{"action":"accept","content":{"confirm":true}}`)
	if res := verify(s, id); res["isError"] == true {
		t.Fatalf("a confirmed verify failed under evidence delivery: %v", res)
	}
	if len(client.asks) != 1 || !strings.Contains(client.asks[0], `text: "listen on 8042"`) ||
		!strings.Contains(client.asks[0], "revision 1 (the served revision)") {
		t.Fatalf("the human was not shown the write: %q", client.asks)
	}
	if got := f.approvalOf(id, "hana"); got != "mcp_elicitation/advisory" {
		t.Fatalf("the confirmed vote recorded %q", got)
	}

	id2 := f.propose("listen on 9000")
	s, client = session(`{"action":"decline"}`)
	res := verify(s, id2)
	b, _ := json.Marshal(res)
	if res["isError"] != true || !strings.Contains(string(b), "did not accept (decline)") || len(client.asks) != 1 {
		t.Fatalf("a declined verify: %s asks=%d", b, len(client.asks))
	}
	if f.approvalOf(id2, "hana") != "" {
		t.Fatal("a declined verify was recorded")
	}
}
