package govstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2_run_outcomes.sql
var schemaV2RunOutcomes string

// migration is one forward-only schema step. Migrations are append-only: an applied
// migration's SQL may never change, which Open verifies through its checksum. Later
// workstreams add a step here (for example 0002) and keep every earlier one.
type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{version: 1, name: "governance store v1", sql: schemaV1},
	{version: 2, name: "run-independent outcomes", sql: schemaV2RunOutcomes},
}

// applicationID marks the file as a govstore database ("xGOV" in ASCII).
const applicationID = 0x78474f56

// LatestSchemaVersion is the newest schema this build knows.
func LatestSchemaVersion() int { return migrations[len(migrations)-1].version }

// migrate brings the database to the latest schema inside one IMMEDIATE transaction,
// so two processes opening a new file at once cannot both apply a step. It records a
// fingerprint of the resulting schema and, when nothing needed applying, verifies that
// the live schema still matches the recorded one.
func migrate(ctx context.Context, db *sql.DB, now func() time.Time) (int, string, error) {
	tx, err := db.BeginTx(ctx, nil) // the writer DSN makes this BEGIN IMMEDIATE
	if err != nil {
		return 0, "", mapErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	var appID int64
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return 0, "", mapErr(err)
	}
	if appID != 0 && appID != applicationID {
		return 0, "", fmt.Errorf("%w: file is not a govstore database (application_id %#x)", ErrInvalid, appID)
	}
	if appID == 0 {
		var objects int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
			return 0, "", mapErr(err)
		}
		if objects > 0 {
			return 0, "", fmt.Errorf("%w: file already holds a non-govstore schema", ErrInvalid)
		}
	}

	// A file already at the latest version only needs its migrations verified; the
	// bootstrap DDL runs for new and older files alone.
	var userVersion int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return 0, "", mapErr(err)
	}
	if appID != applicationID || userVersion != LatestSchemaVersion() {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			checksum   TEXT NOT NULL,
			applied_at TEXT NOT NULL
		) STRICT`); err != nil {
			return 0, "", mapErr(err)
		}
	}

	applied := map[int]string{}
	rows, err := tx.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, "", mapErr(err)
	}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			rows.Close()
			return 0, "", err
		}
		applied[v] = sum
	}
	if err := rows.Close(); err != nil {
		return 0, "", err
	}
	latest := LatestSchemaVersion()
	for v := range applied {
		if v > latest {
			return 0, "", fmt.Errorf("%w: file is at version %d, this build knows %d", ErrSchemaTooNew, v, latest)
		}
	}

	ranAny := false
	for _, m := range migrations {
		sum := Digest(m.sql)
		if got, ok := applied[m.version]; ok {
			if got != sum {
				return 0, "", fmt.Errorf("%w: migration %d (%s) differs from the applied one", ErrSchemaDrift, m.version, m.name)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return 0, "", fmt.Errorf("govstore: migration %d (%s): %w", m.version, m.name, mapErr(err))
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			m.version, m.name, sum, canonTime(now())); err != nil {
			return 0, "", mapErr(err)
		}
		ranAny = true
	}

	if ranAny {
		// Header fields: a pragma cannot take bound parameters, so these are formatted
		// from integer constants only.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
			return 0, "", mapErr(err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
			return 0, "", mapErr(err)
		}
	}

	fp, err := schemaFingerprint(ctx, tx)
	if err != nil {
		return 0, "", err
	}
	var recorded string
	err = tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = 'schema_fingerprint'").Scan(&recorded)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		recorded = ""
	case err != nil:
		return 0, "", mapErr(err)
	}
	if !ranAny && recorded != "" && recorded != fp {
		return 0, "", fmt.Errorf("%w: live schema fingerprint %s, recorded %s", ErrSchemaDrift, fp, recorded)
	}
	if ranAny || recorded == "" {
		stamp := canonTime(now())
		for k, v := range map[string]string{
			"schema_fingerprint": fp,
			"schema_version":     fmt.Sprint(latest),
			"schema_updated_at":  stamp,
		} {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO store_meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
				k, v); err != nil {
				return 0, "", mapErr(err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO store_meta (key, value) VALUES ('created_at', ?) ON CONFLICT (key) DO NOTHING", stamp); err != nil {
			return 0, "", mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, "", mapErr(err)
	}
	return latest, fp, nil
}

// schemaFingerprint hashes every schema object (tables, indexes, triggers, views and
// the FTS shadow tables) with whitespace-normalized SQL. SQLite's own internal tables
// (sqlite_sequence, sqlite_stat*) are excluded because they change with data.
func schemaFingerprint(ctx context.Context, q queryer) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT type, name, tbl_name, coalesce(sql, '') FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY type, name`)
	if err != nil {
		return "", mapErr(err)
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var typ, name, tbl, def string
		if err := rows.Scan(&typ, &name, &tbl, &def); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1f%s\x1e", typ, name, tbl, strings.Join(strings.Fields(def), " "))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
