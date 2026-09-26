package main

import (
	"net/http"
	"strings"
	"testing"

	"xmustard/api-go/internal/workspaceops"
)

// The lifecycle travels through the existing routes: remember(op=edit) proposes a
// pending revision, verify(revision) accepts it with the diff, recall(entry_id) fetches
// any state, and DELETE (retract or purge) is an approver's route only.
func TestMemoryLifecycleOverHTTP(t *testing.T) {
	srv, dir := newRouteServer(t)
	base := srv.URL + "/api/workspaces/wsLifecycle"
	tok := map[string]string{}
	for id, role := range map[string]string{"alice": "agent", "bob": "agent", "carol": "agent", "hana": "human-approver", "root": "admin"} {
		raw, err := workspaceops.MintToken(dir, id, role)
		if err != nil {
			t.Fatal(err)
		}
		tok[id] = raw
	}
	call := func(who, method, path, body string, want int) map[string]any {
		t.Helper()
		code, out := sendJSON(t, method, base+path, tok[who], body)
		if code != want {
			t.Fatalf("%s %s as %s: want %d, got %d %v", method, path, who, want, code, out)
		}
		return out
	}
	e := call("alice", "POST", "/context", `{"content":"listen on 8042","permission":"readwrite"}`, http.StatusOK)
	id := e["id"].(string)
	call("bob", "POST", "/context/"+id+"/verify?approve=true", "", http.StatusOK)
	if v := call("carol", "POST", "/context/"+id+"/verify?approve=true", "", http.StatusOK); v["promoted"] != true || v["revision"] != float64(1) {
		t.Fatalf("promotion: %v", v)
	}

	call("alice", "POST", "/context", `{"op":"edit","entry_id":"`+id+`","base_revision":1,"old_string":"8042","new_string":"9000"}`, http.StatusBadRequest)
	ed := call("alice", "POST", "/context", `{"op":"edit","entry_id":"`+id+`","base_revision":1,"reason":"moved","old_string":"8042","new_string":"9000"}`, http.StatusOK)
	if ed["pending_revision"] != float64(2) || ed["promoted"] != true {
		t.Fatalf("edit: %v", ed)
	}
	if c := call("alice", "POST", "/context", `{"op":"edit","entry_id":"`+id+`","base_revision":1,"reason":"x","new_string":"y"}`, http.StatusConflict); !strings.Contains(c["error"].(string), "current revision 1") {
		t.Fatalf("CAS conflict must name the current revision: %v", c)
	}
	call("bob", "POST", "/context/"+id+"/verify", `{"outcome":"approve","revision":2}`, http.StatusOK)
	v := call("carol", "POST", "/context/"+id+"/verify", `{"outcome":"approve","revision":2}`, http.StatusOK)
	if v["revision"] != float64(2) || !strings.Contains(v["diff"].(string), "+listen on 9000") {
		t.Fatalf("accept: %v", v)
	}
	got := call("bob", "GET", "/context/active?entry_id="+id+"&history=true", "", http.StatusOK)
	if got["entry"].(map[string]any)["content"] != "listen on 9000" || len(got["revisions"].([]any)) != 2 {
		t.Fatalf("recall(entry_id, history): %v", got)
	}

	for who, want := range map[string]int{"": http.StatusUnauthorized, "alice": http.StatusForbidden} {
		call(who, "DELETE", "/context/"+id+"?reason=x", "", want)
	}
	call("hana", "DELETE", "/context/"+id, "", http.StatusBadRequest) // a reason is required
	if r := call("hana", "DELETE", "/context/"+id+"?reason=wrong", "", http.StatusOK); r["lifecycle"] != "retracted" {
		t.Fatalf("approver retract: %v", r)
	}
	if r := call("root", "DELETE", "/context/"+id+"?purge=true&reason=secret", "", http.StatusOK); r["lifecycle"] != "purged" {
		t.Fatalf("admin purge: %v", r)
	}
	tomb := call("bob", "GET", "/context/active?entry_id="+id, "", http.StatusOK)
	if tomb["content_withheld"] == nil || len(tomb["content_digest"].(string)) != 64 {
		t.Fatalf("tombstone: %v", tomb)
	}
	call("alice", "POST", "/context", `{"op":"restore","entry_id":"`+id+`","reason":"undo"}`, http.StatusForbidden)
}
