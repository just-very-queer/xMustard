package workspaceops

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const opsVerifySchemaSQL = `
create table if not exists xm_verifications (
    workspace_id text not null,
    target_id    text not null,
    target_name  text not null,
    status       text,
    outcome      text,
    run_id       text,
    observed_at  text,
    doc          tsvector
);
create index if not exists xm_verifications_ws_idx on xm_verifications (workspace_id, observed_at desc);
create index if not exists xm_verifications_doc_idx on xm_verifications using gin (doc);
`

type pgVerificationRow struct {
	TargetID   string
	TargetName string
	Status     string
	Outcome    string
	RunID      string
	ObservedAt string
	Doc        string
}

func verificationOutcomeToPostgresRow(target RepoTargetRecord, observed ObservedVerificationOutcome) pgVerificationRow {
	row := pgVerificationRow{
		TargetID:   target.TargetID,
		TargetName: target.Label,
		Status:     observed.LastStatus,
		Outcome:    observed.State,
		RunID:      ptrStr(observed.RunID),
		ObservedAt: ptrStr(observed.LastRunAt),
	}
	row.Doc = fmt.Sprintf("%s %s %s", row.TargetName, row.Status, row.Outcome)
	return row
}

// MaterializeVerificationsPostgres mirrors verification outcomes from JSON into
// Postgres, keeping JSON as source of truth and Postgres as queryable index.
func MaterializeVerificationsPostgres(dataDir, workspaceID string) (map[string]any, error) {
	registry, err := ReadVerificationOutcomes(dataDir, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load verification outcomes: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}

	if _, err := conn.Exec(ctx, opsVerifySchemaSQL); err != nil {
		return nil, fmt.Errorf("verification schema: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort rollback if commit not reached

	if _, err := tx.Exec(ctx, "delete from xm_verifications where workspace_id = $1", workspaceID); err != nil {
		return nil, fmt.Errorf("clear xm_verifications: %w", err)
	}

	for _, item := range registry.Items {
		outcome := verificationOutcomeToPostgresRow(item.Target, item.Observed)
		if _, err := tx.Exec(ctx, `
			insert into xm_verifications(workspace_id,target_id,target_name,status,outcome,run_id,observed_at,doc)
			values($1,$2,$3,$4,$5,$6,$7,to_tsvector('simple',$8))`,
			workspaceID, outcome.TargetID, outcome.TargetName, outcome.Status, outcome.Outcome,
			outcome.RunID, outcome.ObservedAt, outcome.Doc); err != nil {
			return nil, fmt.Errorf("insert verification outcome %s: %w", outcome.TargetID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return map[string]any{
		"workspace_id":  workspaceID,
		"store":         "postgres",
		"verifications": len(registry.Items),
		"generated_at":  nowUTC(),
	}, nil
}

type PostgresVerificationHit struct {
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	Status     string `json:"status"`
	Outcome    string `json:"outcome"`
	RunID      string `json:"run_id"`
	ObservedAt string `json:"observed_at"`
}

// ListVerificationsPostgres lists recent materialized verification outcomes with
// optional status filtering.
func ListVerificationsPostgres(workspaceID, status string, limit int) (map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}

	query := "select target_id,target_name,status,outcome,run_id,observed_at from xm_verifications where workspace_id = $1"
	args := []any{workspaceID}
	if strings.TrimSpace(status) != "" {
		query += " and status = $2"
		args = append(args, status)
	}
	query += " order by observed_at desc limit " + fmt.Sprint(limit)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg verifications: %w", err)
	}
	defer rows.Close()

	out := []PostgresVerificationHit{}
	for rows.Next() {
		var hit PostgresVerificationHit
		if err := rows.Scan(&hit.TargetID, &hit.TargetName, &hit.Status, &hit.Outcome, &hit.RunID, &hit.ObservedAt); err != nil {
			return nil, err
		}
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return map[string]any{
		"workspace_id":  workspaceID,
		"store":         "postgres",
		"status":        status,
		"total":         len(out),
		"verifications": out,
	}, nil
}
