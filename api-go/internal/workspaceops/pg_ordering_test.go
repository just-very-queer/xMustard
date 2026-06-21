package workspaceops

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// An out-of-order async mirror must not regress a run's PG state: applying the
// newer snapshot (higher seq) then an older one (lower seq) must leave the newer
// state (XM-NEW-008). Skips when no PG is reachable.
func TestPgMirrorOrderingNoRegress(t *testing.T) {
	if !pgReachable(t) {
		t.Skip("no Postgres reachable; skipping live ordering test")
	}
	ws := "r8ord_" + time.Now().UTC().Format("150405.000000")
	completed := runRecord{RunID: "r", WorkspaceID: ws, Status: "completed", CreatedAt: nowUTC()}
	running := runRecord{RunID: "r", WorkspaceID: ws, Status: "running", CreatedAt: nowUTC()}

	// apply the NEWER snapshot (seq 2) first, then the OLDER (seq 1) — the older one
	// must be skipped by the seq guard.
	pgInlineUpsertRunSync(completed, 2)
	pgInlineUpsertRunSync(running, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgPool(ctx)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	var status string
	var seq int64
	if err := pool.QueryRow(ctx,
		"select status, mirror_seq from xm_runs where workspace_id=$1 and run_id=$2", ws, "r").Scan(&status, &seq); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "completed" || seq != 2 {
		t.Fatalf("older snapshot regressed PG: status=%q seq=%d (want completed/2)", status, seq)
	}
	// in-order apply of a still-newer snapshot wins.
	pgInlineUpsertRunSync(runRecord{RunID: "r", WorkspaceID: ws, Status: "failed", CreatedAt: nowUTC()}, 3)
	_ = pool.QueryRow(ctx, "select status from xm_runs where workspace_id=$1 and run_id=$2", ws, "r").Scan(&status)
	if status != "failed" {
		t.Fatalf("newer snapshot should win, got %q", status)
	}
	cleanupPgRun(t, pool, ws)
}

func cleanupPgRun(t *testing.T, pool *pgxpool.Pool, ws string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = pool.Exec(ctx, "delete from xm_runs where workspace_id=$1", ws)
	_, _ = pool.Exec(ctx, "delete from xm_run_plans where workspace_id=$1", ws)
}
