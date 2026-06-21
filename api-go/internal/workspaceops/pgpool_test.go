package workspaceops

import (
	"context"
	"testing"
)

// The shared pool is built lazily, reused for the same DSN, and rebuilt when the DSN
// changes (so a config change doesn't keep a stale pool). No live PG is required —
// pgxpool connects lazily.
func TestPgPoolReuseAndRebuild(t *testing.T) {
	t.Cleanup(ClosePgPool)
	ctx := context.Background()

	t.Setenv("XMUSTARD_PG_DSN", "postgres://u@127.0.0.1:5599/a")
	p1, err := pgPool(ctx)
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	p1b, _ := pgPool(ctx)
	if p1 != p1b {
		t.Fatal("same DSN must reuse the same pool")
	}
	if p1.Config().MaxConns != pgMaxConns {
		t.Fatalf("pool must be capped at %d, got %d", pgMaxConns, p1.Config().MaxConns)
	}

	t.Setenv("XMUSTARD_PG_DSN", "postgres://u@127.0.0.1:5599/b")
	p2, err := pgPool(ctx)
	if err != nil {
		t.Fatalf("rebuild pool: %v", err)
	}
	if p2 == p1 {
		t.Fatal("changed DSN must rebuild the pool")
	}
}
