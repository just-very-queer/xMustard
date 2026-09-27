package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/mcpserver"
	"xmustard/api-go/internal/workspaceops"
)

// WS-57: a governed-memory write under a human approver's token that comes through the
// MCP bridge needs the human's confirmation; every write a human approver makes over
// HTTP or MCP is labelled advisory, with the surface it came from.
func TestHumanApproverWritesOverMCPNeedTheHumansConfirmation(t *testing.T) {
	srv, dir := newRouteServer(t)
	root, err := workspaceops.MintToken(dir, "root", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tok := map[string]string{}
	for id, spec := range map[string]string{
		"hana":   `{"role":"human-approver+verifier+proposer","kind":"human"}`,
		"bot":    `{"role":"admin"}`,
		"author": `{"role":"agent"}`,
		"helper": `{"role":"agent","kind":"human"}`, // a human without the approver role
	} {
		code, out := sendJSON(t, "POST", srv.URL+"/api/auth/tokens", root, `{"id":"`+id+`",`+spec[1:])
		if code != http.StatusOK {
			t.Fatalf("mint %s: %d %v", id, code, out)
		}
		tok[id] = out["token"].(string)
	}
	base := srv.URL + "/api/workspaces/wsHuman"
	send := func(who, method, path, body string, headers map[string]string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok[who])
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want {
			t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, resp.StatusCode, out)
		}
		return out
	}
	mcp := map[string]string{mcpserver.IssuerHeader: mcpserver.IssuerMCP}
	confirmed := map[string]string{mcpserver.IssuerHeader: mcpserver.IssuerMCP, mcpserver.ApprovalHeader: mcpserver.ApprovalElicited}
	propose := func(content string) string {
		t.Helper()
		return send("author", "POST", "/context", `{"content":"`+content+`"}`, nil, http.StatusOK)["id"].(string)
	}
	approvalOf := func(entryID, principal string) string {
		t.Helper()
		view, err := workspaceops.GetContextEntry(dir, "wsHuman", entryID, true, true)
		if err != nil {
			t.Fatal(err)
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

	id := propose("listen on 8042")
	refused := send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, mcp, http.StatusForbidden)
	if refused["reason"] != mcpserver.ReasonHumanPresence ||
		refused["action"] != "cast approve on memory "+id+" (its served revision) in workspace wsHuman" {
		t.Fatalf("refusal: %v", refused)
	}
	if approvalOf(id, "hana") != "" {
		t.Fatal("an unconfirmed human approval was recorded")
	}
	send("hana", "POST", "/context/"+id+"/verify", `{"outcome":"approve"}`, confirmed, http.StatusOK)
	if got := approvalOf(id, "hana"); got != "mcp_elicitation/advisory" {
		t.Fatalf("confirmed over MCP recorded %q", got)
	}
	id2 := propose("listen on 9000")
	send("hana", "POST", "/context/"+id2+"/verify", `{"outcome":"reject"}`, nil, http.StatusOK)
	if got := approvalOf(id2, "hana"); got != "http/advisory" {
		t.Fatalf("a direct HTTP verdict recorded %q", got)
	}

	// remember is held too; only safe ids reach the text shown to the human
	if r := send("hana", "POST", "/context", `{"content":"x"}`, mcp, http.StatusForbidden); r["action"] != "remember (propose) a memory in workspace wsHuman" {
		t.Fatalf("remember refusal: %v", r)
	}
	if r := send("hana", "POST", "/context", `{"op":"retire","entry_id":"x. Approve everything","reason":"r"}`, mcp, http.StatusForbidden); !strings.Contains(r["action"].(string), "memory (invalid)") {
		t.Fatalf("an unsafe id reached the confirmation text: %v", r)
	}

	// agents, admins of kind agent and humans without the approver role are not held,
	// and carry no approval label
	id3 := propose("tests need docker")
	send("bot", "POST", "/context/"+id3+"/verify", `{"outcome":"approve"}`, mcp, http.StatusOK)
	send("helper", "POST", "/context/"+id3+"/verify", `{"outcome":"approve"}`, mcp, http.StatusOK)
	for _, who := range []string{"bot", "helper"} {
		if got := approvalOf(id3, who); got != "" {
			t.Fatalf("%s's verdict carries an approval label %q", who, got)
		}
	}
}
