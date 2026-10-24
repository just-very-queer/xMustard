package conformance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"xmustard/api-go/internal/evidence"
)

func TestPinnedCursorFixtures(t *testing.T) {
	fixtureDir := filepath.Join("..", "..", "..", "integrations", "cursor", "testdata")
	if err := RunCursorFixtures(fixtureDir); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceHandlesRejectForeignAndExpiredReads(t *testing.T) {
	limits := evidence.DefaultLimits()
	store := evidence.NewStore(t.TempDir(), limits)
	now := time.Now()
	store.SetClock(func() time.Time { return now })
	spool, err := store.NewSpool("workspace-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Write([]byte(`{"content":[{"type":"text","text":"captured"}]}`)); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.Capture(context.Background(), spool, evidence.CaptureRequest{
		WorkspaceID: "workspace-a", Tool: "MCP:recall", ContentType: "application/json", Retain: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), evidence.ReadRequest{WorkspaceID: "workspace-b", Handle: delivery.Handle}); !errors.Is(err, evidence.ErrMissing) {
		t.Fatalf("foreign workspace read error = %v, want missing", err)
	}
	now = now.Add(limits.Retention + time.Minute)
	if _, err := store.Read(context.Background(), evidence.ReadRequest{WorkspaceID: "workspace-a", Handle: delivery.Handle}); !errors.Is(err, evidence.ErrExpired) {
		t.Fatalf("expired handle read error = %v, want expired", err)
	}
}

func TestCancelledCaptureIssuesNoHandle(t *testing.T) {
	store := evidence.NewStore(t.TempDir(), evidence.DefaultLimits())
	spool, err := store.NewSpool("workspace-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Write([]byte(`captured`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if delivery, err := store.Capture(ctx, spool, evidence.CaptureRequest{WorkspaceID: "workspace-a", Tool: "MCP:recall", Retain: true}); !errors.Is(err, context.Canceled) || delivery != nil {
		t.Fatalf("cancelled capture = (%v, %v), want no delivery and cancellation", delivery, err)
	}
}
