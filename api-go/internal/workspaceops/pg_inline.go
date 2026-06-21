package workspaceops

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Inline Postgres write path (C1). The ops layer's durable source of truth is JSON
// (saveRunRecord, saveVerificationProfileHistory); historically PG was only filled
// by the one-shot Materialize* mirrors, so PG drifted until someone manually
// re-materialized. These hooks mirror each mutation into PG so the queryable index
// is current immediately — "no manual materialize."
//
// They are strictly BEST-EFFORT and OFF the request path:
//   - gated on XMUSTARD_PG_DSN (no PG configured → zero work, JSON only);
//   - dispatched to a bounded background worker so a slow/black-hole PG never adds
//     latency to the JSON mutation (which has already succeeded);
//   - every failure (and any panic) is logged and swallowed, so PG can never break
//     the JSON write that is the real source of truth.
//
// Drift note: if a mirror fails or is dropped under saturation, PG can lag JSON
// until the next mutation or a manual MaterializeOpsPostgres/MaterializeVerifications
// re-sync. That is an accepted property of an index that trails an authoritative log.

// pgInlineEnabled requires an explicit XMUSTARD_PG_DSN; without it inline mirroring
// is skipped entirely (the default dev case pays nothing).
func pgInlineEnabled() bool {
	return strings.TrimSpace(os.Getenv("XMUSTARD_PG_DSN")) != ""
}

// Background dispatch: a bounded pool of mirror goroutines so request handlers never
// wait on Postgres and a burst of mutations can't fan out unbounded goroutines.
var (
	pgInlineSem    = make(chan struct{}, 8)
	pgInlineWG     sync.WaitGroup
	pgSchemaReady  atomic.Bool
	pgInlineMaxDur = 5 * time.Second // connect+write budget for a single mirror
)

// pgInlineDispatch runs fn on a background worker (best-effort). If the worker pool
// is saturated it drops the mirror rather than block the caller — JSON is already
// durable and a re-materialize reconciles.
func pgInlineDispatch(fn func()) {
	if !pgInlineEnabled() {
		return
	}
	select {
	case pgInlineSem <- struct{}{}:
	default:
		log.Printf("pg inline: mirror pool saturated, dropping one mirror (JSON unaffected; re-materialize to reconcile)")
		return
	}
	pgInlineWG.Add(1)
	go func() {
		defer pgInlineWG.Done()
		defer func() { <-pgInlineSem }()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("pg inline: mirror panic recovered (JSON unaffected): %v", r)
			}
		}()
		fn()
	}()
}

// PgInlineFlush blocks until all dispatched mirrors finish. Used by tests for
// determinism; harmless in production (typically a no-op at shutdown).
func PgInlineFlush() { pgInlineWG.Wait() }

// pgRunPlansSchema is the queryable run-plans table the inline upsert needs on top
// of opsSchemaSQL (which already defines xm_runs). No unique index is required: the
// upserts use delete-by-key + insert, so historical duplicate rows can never break
// (or be left by) the inline path.
const pgRunPlansSchema = `
create table if not exists xm_run_plans (
    workspace_id text not null,
    plan_id      text not null,
    run_id       text not null,
    phase        text,
    summary      text,
    reasoning    text,
    step_count   int,
    created_at   text,
    approved_at  text,
    doc          tsvector,
    primary key (workspace_id, plan_id)
);
create index if not exists xm_run_plans_run_idx on xm_run_plans (workspace_id, run_id);
`

// ensureInlineSchema runs the DDL once per process (idempotent CREATE … IF NOT
// EXISTS). Skipped once it has succeeded, so it isn't paid on every mutation; a
// failure leaves the flag unset so a later mirror retries.
func ensureInlineSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pgSchemaReady.Load() {
		return nil
	}
	if _, err := pool.Exec(ctx, opsSchemaSQL+pgRunPlansSchema); err != nil {
		return err
	}
	pgSchemaReady.Store(true)
	return nil
}

// pgInlineUpsertRun mirrors a run (and its plan) into Postgres on every
// saveRunRecord. Best-effort; runs on a background worker.
func pgInlineUpsertRun(run runRecord) {
	pgInlineDispatch(func() { pgInlineUpsertRunSync(run) })
}

func pgInlineUpsertRunSync(run runRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), pgInlineMaxDur)
	defer cancel()
	pool, err := pgPool(ctx)
	if err != nil {
		log.Printf("pg inline: pool unavailable (run mirror skipped, JSON unaffected): %v", err)
		return
	}
	if err := ensureInlineSchema(ctx, pool); err != nil {
		log.Printf("pg inline: run schema failed (mirror skipped): %v", err)
		return
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Printf("pg inline: begin failed (mirror skipped): %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback if commit not reached

	// delete-by-key + insert is an upsert that needs no unique constraint, so a
	// pre-existing duplicate (workspace_id, run_id) can never disable mirroring.
	if _, err := tx.Exec(ctx, "delete from xm_runs where workspace_id=$1 and run_id=$2", run.WorkspaceID, run.RunID); err != nil {
		log.Printf("pg inline: run delete %s failed: %v", run.RunID, err)
		return
	}
	doc := strings.Join([]string{run.Title, run.IssueID, run.Runtime, run.Model, ptrStr(run.Error)}, " ")
	// raw nullable pointers (CompletedAt/ExitCode/Error) so absent values are SQL
	// NULL, not "", matching MaterializeOpsPostgres and keeping IS NULL queries sound.
	if _, err := tx.Exec(ctx, `
		insert into xm_runs(workspace_id,run_id,issue_id,runtime,model,status,title,created_at,completed_at,exit_code,error,doc)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,to_tsvector('simple',$12))`,
		run.WorkspaceID, run.RunID, run.IssueID, run.Runtime, run.Model, run.Status, run.Title,
		run.CreatedAt, run.CompletedAt, run.ExitCode, run.Error, doc); err != nil {
		log.Printf("pg inline: run insert %s failed: %v", run.RunID, err)
		return
	}

	// keep exactly one plan row per run (a re-generated plan supersedes the old one,
	// so delete-by-run avoids orphaned plan rows).
	if _, err := tx.Exec(ctx, "delete from xm_run_plans where workspace_id=$1 and run_id=$2", run.WorkspaceID, run.RunID); err != nil {
		log.Printf("pg inline: plan delete for run %s failed: %v", run.RunID, err)
		return
	}
	if p := run.Plan; p != nil {
		pdoc := strings.Join([]string{p.Summary, ptrStr(p.Reasoning)}, " ")
		if _, err := tx.Exec(ctx, `
			insert into xm_run_plans(workspace_id,plan_id,run_id,phase,summary,reasoning,step_count,created_at,approved_at,doc)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,to_tsvector('simple',$10))`,
			run.WorkspaceID, p.PlanID, p.RunID, p.Phase, p.Summary, p.Reasoning,
			len(p.Steps), p.CreatedAt, p.ApprovedAt, pdoc); err != nil {
			log.Printf("pg inline: plan insert %s failed: %v", p.PlanID, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("pg inline: run commit %s failed: %v", run.RunID, err)
	}
}

// PostgresRunPlan is one run-plan row read back from Postgres.
type PostgresRunPlan struct {
	PlanID     string `json:"plan_id"`
	RunID      string `json:"run_id"`
	Phase      string `json:"phase"`
	Summary    string `json:"summary"`
	StepCount  int    `json:"step_count"`
	CreatedAt  string `json:"created_at"`
	ApprovedAt string `json:"approved_at"`
}

// ListRunPlansPostgres reads the workspace's run plans from the inline PG index.
func ListRunPlansPostgres(workspaceID string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}

	rows, err := pool.Query(ctx, `
		select plan_id, run_id, coalesce(phase,''), coalesce(summary,''), coalesce(step_count,0),
		       coalesce(created_at,''), coalesce(approved_at,'')
		from xm_run_plans where workspace_id = $1
		order by created_at desc limit $2`, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("pg run plans: %w", err)
	}
	defer rows.Close()
	out := []PostgresRunPlan{}
	for rows.Next() {
		var p PostgresRunPlan
		if err := rows.Scan(&p.PlanID, &p.RunID, &p.Phase, &p.Summary, &p.StepCount, &p.CreatedAt, &p.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": workspaceID,
		"store":        "postgres",
		"total":        len(out),
		"run_plans":    out,
	}, nil
}

// pgInlineMirrorVerifications re-mirrors the workspace's verification outcomes into
// Postgres after a verification-history mutation. Best-effort, on a background
// worker (verification outcomes are derived, not a single record, so it reuses the
// materialize — off the request path so its 60s budget never stalls the save).
func pgInlineMirrorVerifications(dataDir, workspaceID string) {
	pgInlineDispatch(func() {
		if _, err := MaterializeVerificationsPostgres(dataDir, workspaceID); err != nil {
			log.Printf("pg inline: verification mirror (ws=%s) failed (JSON unaffected): %v", workspaceID, err)
		}
	})
}
