package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// P1-E: a cancelled MCP tools/call must abort the in-flight HTTP request to the API
// promptly (propagating shim->API), not run to the client timeout. HTTPBackend.Do binds
// the request to the context; cancelling it returns at once with a context error.
func TestBackendCancellationAbortsInFlightRequest(t *testing.T) {
	// a server that hangs until the request's own context is cancelled (mimics a long
	// tool call); if cancellation didn't propagate, Do would block here.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	t.Setenv("XMUSTARD_API_BASE", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := (&HTTPBackend{}).Do(ctx, Request{Method: "GET", Path: "/api/workspaces/ws/search?q=x"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a cancellation error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation did not abort the request promptly, took %v", elapsed)
	}
}
