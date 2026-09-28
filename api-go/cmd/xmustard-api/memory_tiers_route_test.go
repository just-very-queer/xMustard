package main

import (
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// The write policy is enforced server-side: a violation answers 400 with its structured
// violations, a protected write by anyone but a human approver answers 403 (audited),
// and the projection route serves the standing memory to a reader.
func TestMemoryPolicyAndProjectionOverHTTP(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsTiers"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "rita": "reader"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	for id, role := range map[string]string{"hana": "proposer+human-approver", "root": "admin"} {
		raw, err := workspaceops.MintIdentityToken(dir, id, role, 0, nil, workspaceops.TokenIdentity{Kind: workspaceops.PrincipalHuman})
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	h, err := workspaceops.AuthorizeHumanApprover(dir, "wsTiers", tok["root"], workspaceops.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	p := workspaceops.DefaultMemoryPolicy()
	p.MaxEntryChars = 256
	if _, err := workspaceops.SetMemoryPolicy(dir, "wsTiers", p, h); err != nil {
		t.Fatal(err)
	}
	call := func(who, method, path, body string, want int) map[string]any {
		t.Helper()
		code, out := sendJSON(t, method, base+path, tok[who], body)
		if code != want {
			t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, code, out)
		}
		return out
	}
	long := strings.Repeat("x", 300)
	v := call("alice", "POST", "/context", `{"content":"`+long+`"}`, http.StatusBadRequest)
	if vs, _ := v["violations"].([]any); len(vs) != 1 || vs[0].(map[string]any)["rule"] != "max_chars" || vs[0].(map[string]any)["limit"] != float64(256) {
		t.Fatalf("size violation: %v", v)
	}
	v = call("alice", "POST", "/context", `{"content":"always true","tier":"core"}`, http.StatusForbidden)
	if vs, _ := v["violations"].([]any); len(vs) != 1 || vs[0].(map[string]any)["field"] != "tier" || vs[0].(map[string]any)["rule"] != "protected_field" {
		t.Fatalf("protected field: %v", v)
	}
	if e := call("hana", "POST", "/context", `{"content":"always true","title":"rule","tier":"core","description":"the one rule"}`, http.StatusOK); e["tier"] != "core" ||
		e["description"] != "the one rule" {
		t.Fatalf("human approver's core proposal: %v", e)
	}
	proj := call("rita", "GET", "/memory/core?max_chars=2000", "", http.StatusOK)
	if proj["revision"].(float64) < 1 || proj["budget"] != float64(2000) || !strings.Contains(proj["text"].(string), "memory revision") {
		t.Fatalf("projection: %v", proj)
	}
	call("rita", "GET", "/memory/core?max_chars=10", "", http.StatusBadRequest)
	call("rita", "GET", "/memory/core?max_chars=x", "", http.StatusBadRequest)
	call("", "GET", "/memory/core", "", http.StatusUnauthorized)
}
