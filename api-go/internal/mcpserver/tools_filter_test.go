package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func listedNames(res any) []string {
	var names []string
	for _, t := range res.(map[string]any)["tools"].([]map[string]any) {
		names = append(names, t["name"].(string))
	}
	return names
}

// tools/list shows only the tools the API says this caller can use (its roles,
// read-only mode, disabled tools), and every tool when the API can't say. The
// session uses the stdio shim's backend: base URL and bearer token from the env.
func TestToolsListFollowsCallerPosture(t *testing.T) {
	var body string
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/whoami" {
			http.NotFound(w, r)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		if body == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)
	t.Setenv("XMUSTARD_API_TOKEN", "xmt_reader")
	s := New(Options{Backend: &HTTPBackend{}}).NewSession(nil)

	body = `{"id":"r","roles":["reader"],"read_only":false,"tools":["ground","recall","search"]}`
	res, rerr := s.Handle(context.Background(), "tools/list", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if got := listedNames(res); !slices.Equal(got, []string{"ground", "recall", "search"}) {
		t.Fatalf("filtered tools/list: %v", got)
	}
	if sawAuth != "Bearer xmt_reader" {
		t.Fatalf("the posture lookup must carry the agent's token, got %q", sawAuth)
	}
	// filtering drops entries; it never reshapes the ones it keeps
	full := map[string]map[string]any{}
	for _, e := range s.ToolsList()["tools"].([]map[string]any) {
		full[e["name"].(string)] = e
	}
	for _, e := range res.(map[string]any)["tools"].([]map[string]any) {
		if !reflect.DeepEqual(e, full[e["name"].(string)]) {
			t.Fatalf("filtered entry %v differs from the full list's", e["name"])
		}
	}

	// a caller the API grants no tool sees none
	body = `{"id":"x","roles":[],"tools":[]}`
	res, _ = s.Handle(context.Background(), "tools/list", nil)
	if got := listedNames(res); len(got) != 0 {
		t.Fatalf("no usable tools must list none, got %v", got)
	}

	// an API without the field, or an unreachable one, leaves the full list
	for _, b := range []string{"", `{"id":"old","role":"agent"}`} {
		body = b
		res, _ = s.Handle(context.Background(), "tools/list", nil)
		if got := listedNames(res); len(got) != 9 {
			t.Fatalf("fallback must advertise all nine tools, got %v", got)
		}
	}
	srv.Close()
	res, _ = s.Handle(context.Background(), "tools/list", nil)
	if got := listedNames(res); len(got) != 9 {
		t.Fatalf("unreachable API: want all nine tools, got %v", got)
	}
}
