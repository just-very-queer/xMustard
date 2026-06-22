package workspaceops

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Shared Postgres connection pool. Every PG op used to pgx.Connect/Close per call,
// so a burst of mirror writes + /pg/* reads churned FDs and could exhaust the
// server's max_connections under concurrent agents. One lazily-built, capped pool
// (keyed by DSN, rebuilt if the DSN changes) bounds concurrent connections and
// amortizes connection setup. pgxpool.Pool's Exec/Query/QueryRow/Begin acquire and
// release a connection per call, so callers use the pool directly instead of a conn.

// pgMaxConns caps concurrent connections well below Postgres's default
// max_connections (100), leaving headroom for psql/other clients.
const pgMaxConns = 10

var (
	pgPoolMu      sync.Mutex
	pgPoolCurrent *pgxpool.Pool
	pgPoolDSN     string
)

// pgPool returns the shared pool for the current DSN, building it lazily. The pool
// is created with a background lifetime (not the request ctx) and connects lazily
// on first use (MinConns 0), so building it never blocks or couples to a request.
func pgPool(_ context.Context) (*pgxpool.Pool, error) {
	dsn := pgDSN()
	pgPoolMu.Lock()
	defer pgPoolMu.Unlock()
	if pgPoolCurrent != nil && pgPoolDSN == dsn {
		return pgPoolCurrent, nil
	}
	if pgPoolCurrent != nil { // DSN changed: replace the pool
		pgPoolCurrent.Close()
		pgPoolCurrent = nil
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = pgMaxConns
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 60 * time.Second
	cfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	pgPoolCurrent = pool
	pgPoolDSN = dsn
	return pool, nil
}

// ClosePgPool releases the shared pool (used on graceful shutdown).
func ClosePgPool() {
	pgPoolMu.Lock()
	defer pgPoolMu.Unlock()
	if pgPoolCurrent != nil {
		pgPoolCurrent.Close()
		pgPoolCurrent = nil
		pgPoolDSN = ""
	}
}
