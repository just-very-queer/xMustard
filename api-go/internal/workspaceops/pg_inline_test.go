package workspaceops

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPgInlineDisabledByDefault(t *testing.T) {
	t.Setenv("XMUSTARD_PG_DSN", "")
	if pgInlineEnabled() {
		t.Fatal("inline PG must be disabled when XMUSTARD_PG_DSN is unset")
	}
}

// saveRunRecord must succeed (JSON is the source of truth) even when inline PG is
// pointed at a dead address — the mirror is strictly best-effort.
func TestSaveRunRecordSucceedsWhenPgDead(t *testing.T) {
	dir := t.TempDir()
	// a refused port → connect fails fast; the mutation must still succeed.
	t.Setenv("XMUSTARD_PG_DSN", "postgres://nobody@127.0.0.1:1/none")
	run := runRecord{RunID: "run-x", WorkspaceID: "ws", Status: "queued", Title: "t"}
	if err := saveRunRecord(dir, run); err != nil {
		t.Fatalf("saveRunRecord must not fail when PG is down: %v", err)
	}
	if _, err := os.Stat(dir + "/workspaces/ws/runs/run-x.json"); err != nil {
		t.Fatalf("run JSON (source of truth) must be written: %v", err)
	}
	PgInlineFlush() // let the best-effort background mirror finish (it fails harmlessly)
}

// saveRunRecord must return promptly even when PG would hang on connect — the
// mirror is dispatched to a background worker, so request latency is decoupled from
// Postgres reachability (a refused host fails fast; a black-hole host would hang the
// connect for the full budget, but only on the background goroutine).
func TestSaveRunRecordFastWhenPgBlackHole(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XMUSTARD_PG_DSN", "postgres://x@10.255.255.1:5432/none") // non-routable
	start := time.Now()
	if err := saveRunRecord(dir, runRecord{RunID: "r", WorkspaceID: "ws", Status: "queued"}); err != nil {
		t.Fatalf("saveRunRecord: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("saveRunRecord must not block on PG connect (async mirror); took %v", d)
	}
	PgInlineFlush() // drain the background goroutine before the test ends
}

// pgReachable probes the default PG so the live round-trip skips cleanly in CI.
func pgReachable(t *testing.T) bool {
	t.Helper()
	if os.Getenv("XMUSTARD_PG_DSN") == "" {
		// only probe the well-known dev default; never spin up a real connection
		// against an operator's configured DSN from a unit test.
		t.Setenv("XMUSTARD_PG_DSN", "postgres://postgres@127.0.0.1:5433/xmustard")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgDSN())
	if err != nil {
		return false
	}
	_ = conn.Close(ctx)
	return true
}

// Live round-trip: saving a run with a plan makes it queryable in PG with no
// manual materialize. Skips when no PG is reachable.
func TestInlineRunPlanQueryableInPg(t *testing.T) {
	if !pgReachable(t) {
		t.Skip("no Postgres reachable; skipping live inline-write round-trip")
	}
	ws := "r5test_" + time.Now().UTC().Format("150405.000000")
	dir := t.TempDir()
	reasoning := "because"
	run := runRecord{
		RunID:       "run-1",
		WorkspaceID: ws,
		IssueID:     "iss-1",
		Runtime:     "claude",
		Model:       "opus",
		Status:      "planning",
		Title:       "fix the widget",
		CreatedAt:   nowUTC(),
		Plan: &RunPlan{
			PlanID:    "plan-1",
			RunID:     "run-1",
			Phase:     "awaiting_approval",
			Summary:   "do the thing",
			Reasoning: &reasoning,
			Steps:     []PlanStep{{}, {}, {}},
			CreatedAt: nowUTC(),
		},
	}
	if err := saveRunRecord(dir, run); err != nil {
		t.Fatalf("saveRunRecord: %v", err)
	}
	PgInlineFlush() // mirror is async best-effort; wait for it before asserting
	// queryable immediately, no manual materialize.
	res, err := ListRunPlansPostgres(ws, 10)
	if err != nil {
		t.Fatalf("ListRunPlansPostgres: %v", err)
	}
	plans, _ := res["run_plans"].([]PostgresRunPlan)
	if len(plans) != 1 || plans[0].PlanID != "plan-1" || plans[0].StepCount != 3 || plans[0].Phase != "awaiting_approval" {
		t.Fatalf("inline-written plan not queryable as expected: %+v", plans)
	}

	// a status/phase mutation upserts (not duplicates) the same plan.
	run.Plan.Phase = "approved"
	approved := nowUTC()
	run.Plan.ApprovedAt = &approved
	run.Status = "queued"
	if err := saveRunRecord(dir, run); err != nil {
		t.Fatalf("saveRunRecord (update): %v", err)
	}
	PgInlineFlush()
	res2, _ := ListRunPlansPostgres(ws, 10)
	plans2, _ := res2["run_plans"].([]PostgresRunPlan)
	if len(plans2) != 1 || plans2[0].Phase != "approved" {
		t.Fatalf("mutation should upsert in place to phase=approved, got: %+v", plans2)
	}

	// cleanup this test's rows.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if conn, err := pgx.Connect(ctx, pgDSN()); err == nil {
		_, _ = conn.Exec(ctx, "delete from xm_run_plans where workspace_id=$1", ws)
		_, _ = conn.Exec(ctx, "delete from xm_runs where workspace_id=$1", ws)
		_ = conn.Close(ctx)
	}
}
