package workspaceops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"xmustard/api-go/internal/govstore"
)

// The daemon's startup check: no store yet is "none"; a healthy store is migrated and
// "ok" at this build's schema; a damaged one is "corrupt" and every later memory call
// in the process is refused (fail closed) instead of served from a damaged file.
func TestStoreCheckMigratesAndFailsClosedOnDamage(t *testing.T) {
	ctx := context.Background()
	dir, ws := t.TempDir(), "ws"
	if h := MemoryStoreHealth(dir); h.Status != "" {
		t.Fatalf("unchecked store health = %+v", h)
	}
	if h := CheckMemoryStore(ctx, dir); h.Status != StoreNone || h.CheckedAt == "" {
		t.Fatalf("no store yet: %+v", h)
	}
	if _, err := os.Stat(memoryStorePath(dir)); !os.IsNotExist(err) {
		t.Fatal("the check created a store")
	}
	if _, err := Remember(dir, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "the build uses make"}},
		ContextActor{ID: "author"}); err != nil {
		t.Fatal(err)
	}
	h := CheckMemoryStore(ctx, dir) // the store is open in this process: checked in place
	if h.Status != StoreOK || h.SchemaVersion != govstore.LatestSchemaVersion() || MemoryStoreHealth(dir) != h {
		t.Fatalf("healthy store: %+v (recorded %+v)", h, MemoryStoreHealth(dir))
	}
	if p := h.Public(); p.Detail != "" || p.CheckedAt != "" || p.Status != StoreOK {
		t.Fatalf("public view = %+v", p)
	}

	damaged := t.TempDir()
	s, err := govstore.Open(ctx, memoryStorePath(damaged), govstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx govstore.Tx) error {
		for i := range 200 {
			if _, err := tx.InsertEntry(ctx, govstore.NewEntry{ID: fmt.Sprintf("d%d", i), WorkspaceID: ws,
				Content: strings.Repeat("payload ", 200)}, govstore.Actor{Principal: "alice"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Checkpoint(ctx)
	_ = s.Close()
	f, _ := os.OpenFile(memoryStorePath(damaged), os.O_RDWR, 0)
	_, _ = f.WriteAt([]byte(strings.Repeat("\xa5", 4096*4)), 4096*6)
	_ = f.Close()

	if h := CheckMemoryStore(ctx, damaged); h.Status != StoreCorrupt || h.Detail == "" {
		t.Fatalf("damaged store: %+v", h)
	}
	_, err = Remember(damaged, ws, RememberRequest{ProposeContextRequest: ProposeContextRequest{Title: "t", Content: "after the damage"}},
		ContextActor{ID: "author"})
	if err == nil || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("a memory write to a store that failed its check: %v", err)
	}
	if _, err := ListContextEntries(damaged, ws, ""); err == nil {
		t.Fatal("a memory read from a store that failed its check was served")
	}
}
