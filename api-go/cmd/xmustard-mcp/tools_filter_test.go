package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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
// read-only mode, disabled tools), and every tool when the API can't say.
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

	body = `{"id":"r","roles":["reader"],"read_only":false,"tools":["ground","recall","search"]}`
	res, rerr := dispatchCtx(context.Background(), "tools/list", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if got := listedNames(res); !slices.Equal(got, []string{"ground", "recall", "search"}) {
		t.Fatalf("filtered tools/list: %v", got)
	}
	if sawAuth != "Bearer xmt_reader" {
		t.Fatalf("the posture lookup must carry the agent's token, got %q", sawAuth)
	}

	// an API without the field, or an unreachable one, leaves the full list
	for _, b := range []string{"", `{"id":"old","role":"agent"}`} {
		body = b
		res, _ = dispatchCtx(context.Background(), "tools/list", nil)
		if got := listedNames(res); len(got) != 9 {
			t.Fatalf("fallback must advertise all nine tools, got %v", got)
		}
	}
	srv.Close()
	res, _ = dispatchCtx(context.Background(), "tools/list", nil)
	if got := listedNames(res); len(got) != 9 {
		t.Fatalf("unreachable API: want all nine tools, got %v", got)
	}
}
