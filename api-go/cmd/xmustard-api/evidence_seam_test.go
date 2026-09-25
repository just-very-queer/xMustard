package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
)

// Root checklist: the delivery seam must release its spool when the tool handler
// panics or the request is cancelled before capture — no stranded spool, no pending
// quota, no retained handle.
func TestDeliverySeamReleasesSpoolOnPanicAndCancel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_DATA_DIR", dir)
	store := evidence.NewStore(dir, evidence.DefaultLimits())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 200<<10)) // spooled bytes before failing
		if r.URL.Query().Get("q") == "panic" {
			panic("tool handler panic")
		}
		<-r.Context().Done() // hold until the client cancels
	})
	srv := httptest.NewServer(evidenceDeliveryMiddleware(store, handler))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/workspaces/ws/search?q=panic", nil)
	req.Header.Set(deliveryHeader, evidence.DeliveryVersion)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, _ = http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/ws/search?q=hold", nil)
	req.Header.Set(deliveryHeader, evidence.DeliveryVersion)
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	deadline := time.Now().Add(3 * time.Second)
	for store.PendingBytes("ws") != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := store.PendingBytes("ws"); n != 0 {
		t.Fatalf("stranded pending spool bytes: %d", n)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "evidence", "ws"))
	for _, e := range ents {
		t.Fatalf("seam left %s behind (spool or retained artifact)", e.Name())
	}
}
