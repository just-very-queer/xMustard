package workspaceops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Operational memory in Postgres. The semantic index (xm_files/xm_symbols/
// xm_edges, see pgindex.go) moved the *code* layer into Postgres; this moves the
// *ops* layer — runs, activity, issues — the same way. JSON remains the durable
// source of truth that the runtime writes; MaterializeOpsPostgres mirrors it into
// queryable Postgres tables so the cockpit/agents can filter, full-text search,
// and join ops data at scale instead of scanning JSON files.

const opsSchemaSQL = `
create table if not exists xm_runs (
    workspace_id text not null,
    run_id       text not null,
    issue_id     text,
    runtime      text,
    model        text,
    status       text,
    title        text,
    created_at   text,
    completed_at text,
    exit_code    int,
    error        text,
    doc          tsvector
);
create index if not exists xm_runs_ws_idx on xm_runs (workspace_id, created_at desc);
create index if not exists xm_runs_status_idx on xm_runs (workspace_id, status);
create index if not exists xm_runs_doc_idx on xm_runs using gin (doc);

create table if not exists xm_activity (
    workspace_id text not null,
    activity_id  text,
    entity_type  text,
    entity_id    text,
    action       text,
    summary      text,
    actor_kind   text,
    actor_name   text,
    issue_id     text,
    run_id       text,
    created_at   text
);
create index if not exists xm_activity_ws_idx on xm_activity (workspace_id, created_at desc);
create index if not exists xm_activity_issue_idx on xm_activity (workspace_id, issue_id);

create table if not exists xm_issues (
    workspace_id text not null,
    bug_id       text not null,
    title        text,
    severity     text,
    status       text,
    needs_followup boolean,
    doc          tsvector
);
create index if not exists xm_issues_ws_idx on xm_issues (workspace_id);
create index if not exists xm_issues_doc_idx on xm_issues using gin (doc);
`

// MaterializeOpsPostgres mirrors the workspace's runs, activity, and issues from
// the JSON stores into Postgres, returning row counts.
func MaterializeOpsPostgres(dataDir, workspaceID string) (map[string]any, error) {
	runs, err := listRuns(dataDir, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load runs: %w", err)
	}
	activity, err := loadAllWorkspaceActivity(dataDir, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load activity: %w", err)
	}
	issues, err := loadTrackerIssues(dataDir, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load issues: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgDSN())
	if err != nil {
		return nil, fmt.Errorf("postgres connect (%s): %w", pgDSN(), err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, opsSchemaSQL); err != nil {
		return nil, fmt.Errorf("ops schema: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback if commit not reached

	for _, table := range []string{"xm_runs", "xm_activity", "xm_issues"} {
		if _, err := tx.Exec(ctx, "delete from "+table+" where workspace_id = $1", workspaceID); err != nil {
			return nil, fmt.Errorf("clear %s: %w", table, err)
		}
	}

	for i := range runs {
		r := &runs[i]
		doc := strings.Join([]string{r.Title, r.IssueID, r.Runtime, r.Model, ptrStr(r.Error)}, " ")
		if _, err := tx.Exec(ctx, `
			insert into xm_runs(workspace_id,run_id,issue_id,runtime,model,status,title,created_at,completed_at,exit_code,error,doc)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,to_tsvector('simple',$12))`,
			workspaceID, r.RunID, r.IssueID, r.Runtime, r.Model, r.Status, r.Title,
			r.CreatedAt, r.CompletedAt, r.ExitCode, r.Error, doc); err != nil {
			return nil, fmt.Errorf("insert run %s: %w", r.RunID, err)
		}
	}
	for i := range activity {
		a := &activity[i]
		if _, err := tx.Exec(ctx, `
			insert into xm_activity(workspace_id,activity_id,entity_type,entity_id,action,summary,actor_kind,actor_name,issue_id,run_id,created_at)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			workspaceID, a.ActivityID, a.EntityType, a.EntityID, a.Action, a.Summary,
			a.Actor.Kind, a.Actor.Name, a.IssueID, a.RunID, a.CreatedAt); err != nil {
			return nil, fmt.Errorf("insert activity %s: %w", a.ActivityID, err)
		}
	}
	for i := range issues {
		is := &issues[i]
		doc := strings.Join([]string{is.Title, ptrStr(is.Summary), ptrStr(is.Impact), ptrStr(is.Notes)}, " ")
		if _, err := tx.Exec(ctx, `
			insert into xm_issues(workspace_id,bug_id,title,severity,status,needs_followup,doc)
			values($1,$2,$3,$4,$5,$6,to_tsvector('simple',$7))`,
			workspaceID, is.BugID, is.Title, is.Severity, is.IssueStatus, is.NeedsFollowup, doc); err != nil {
			return nil, fmt.Errorf("insert issue %s: %w", is.BugID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return map[string]any{
		"workspace_id": workspaceID,
		"store":        "postgres",
		"runs":         len(runs),
		"activity":     len(activity),
		"issues":       len(issues),
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// PostgresRun is one run row read back from Postgres.
type PostgresRun struct {
	RunID     string `json:"run_id"`
	IssueID   string `json:"issue_id"`
	Runtime   string `json:"runtime"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// ListRunsPostgres reads recent runs for a workspace from Postgres (optionally
// filtered by status), proving the ops layer is queryable from PG, not JSON.
func ListRunsPostgres(workspaceID, status string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgDSN())
	if err != nil {
		return nil, fmt.Errorf("postgres connect (%s): %w", pgDSN(), err)
	}
	defer conn.Close(ctx)

	query := `select run_id,issue_id,runtime,model,status,title,created_at from xm_runs where workspace_id = $1`
	args := []any{workspaceID}
	if strings.TrimSpace(status) != "" {
		query += ` and status = $2`
		args = append(args, status)
	}
	query += ` order by created_at desc limit ` + fmt.Sprint(limit)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg runs: %w", err)
	}
	defer rows.Close()
	out := []PostgresRun{}
	for rows.Next() {
		var r PostgresRun
		if err := rows.Scan(&r.RunID, &r.IssueID, &r.Runtime, &r.Model, &r.Status, &r.Title, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": workspaceID,
		"store":        "postgres",
		"status":       status,
		"total":        len(out),
		"runs":         out,
	}, nil
}

// PostgresIssueHit is one issue FTS result from Postgres.
type PostgresIssueHit struct {
	BugID    string  `json:"bug_id"`
	Title    string  `json:"title"`
	Severity string  `json:"severity"`
	Status   string  `json:"status"`
	Score    float64 `json:"score"`
}

// SearchIssuesPostgres runs FTS over the workspace's issues in Postgres.
func SearchIssuesPostgres(workspaceID, query string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 25
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgDSN())
	if err != nil {
		return nil, fmt.Errorf("postgres connect (%s): %w", pgDSN(), err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `
		select bug_id, title, severity, status,
		       ts_rank(doc, websearch_to_tsquery('simple', $2)) as score
		from xm_issues
		where workspace_id = $1 and doc @@ websearch_to_tsquery('simple', $2)
		order by score desc, bug_id
		limit $3`, workspaceID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("pg issue search: %w", err)
	}
	defer rows.Close()
	hits := []PostgresIssueHit{}
	for rows.Next() {
		var h PostgresIssueHit
		if err := rows.Scan(&h.BugID, &h.Title, &h.Severity, &h.Status, &h.Score); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": workspaceID,
		"query":        query,
		"store":        "postgres",
		"total":        len(hits),
		"hits":         hits,
	}, nil
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
