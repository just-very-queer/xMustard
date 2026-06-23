package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// P1-E: a cancelled MCP tools/call must abort the in-flight HTTP request to the API
// promptly (propagating shim->API), not run to the client timeout. callAPICtx binds the
// request to the context; cancelling it returns at once with a context error.
func TestCallAPICtxCancellationAbortsInFlightRequest(t *testing.T) {
	// a server that hangs until the request's own context is cancelled (mimics a long
	// tool call); if cancellation didn't propagate, callAPICtx would block here.
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
	_, err := callAPICtx(ctx, "GET", "/api/workspaces/ws/search?q=x", "")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a cancellation error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation did not abort the request promptly, took %v", elapsed)
	}
}

// The in-flight registry cancels exactly the request addressed by id, and a cancel for
// an unknown/already-finished id is a harmless no-op.
func TestInflightRegistryCancelsById(t *testing.T) {
	reg := newInflight()
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	reg.add("1", cancelA)
	reg.add("2", cancelB)

	reg.cancel("999") // unknown id: no-op
	select {
	case <-ctxA.Done():
		t.Fatalf("unrelated request was cancelled")
	default:
	}

	reg.cancel("1")
	select {
	case <-ctxA.Done():
	case <-time.After(time.Second):
		t.Fatalf("request 1 was not cancelled")
	}
	select {
	case <-ctxB.Done():
		t.Fatalf("request 2 must remain in flight")
	default:
	}
	reg.cancel("1") // double-cancel is a no-op
}

// notifications/cancelled params parse the target request id for both numeric and string
// JSON-RPC ids (matched by exact raw encoding).
func TestCancelledRequestIDParsing(t *testing.T) {
	if id, ok := cancelledRequestID([]byte(`{"requestId":5,"reason":"user"}`)); !ok || id != "5" {
		t.Fatalf("numeric id parse: got %q ok=%v", id, ok)
	}
	if id, ok := cancelledRequestID([]byte(`{"requestId":"abc"}`)); !ok || id != `"abc"` {
		t.Fatalf("string id parse: got %q ok=%v", id, ok)
	}
	if _, ok := cancelledRequestID([]byte(`{"reason":"no id"}`)); ok {
		t.Fatalf("missing requestId should not parse")
	}
}
