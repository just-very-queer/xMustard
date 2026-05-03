package workspaceops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsRoundTripPreservesPostgresFields(t *testing.T) {
	dataDir, _, _, _ := writeIssueContextFixture(t, false)
	settings, err := GetSettings(dataDir)
	if err != nil {
		t.Fatalf("get default settings: %v", err)
	}
	if settings.PostgresSchema != "xmustard" {
		t.Fatalf("expected default postgres schema, got %#v", settings)
	}

	dsn := "postgresql://xmustard:secret@localhost:5432/xmustard"
	updated, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "codex",
		PostgresDSN:    &dsn,
		PostgresSchema: "xmustard_live",
	})
	if err != nil {
		t.Fatalf("update settings with postgres fields: %v", err)
	}
	if updated.PostgresDSN == nil || *updated.PostgresDSN != dsn || updated.PostgresSchema != "xmustard_live" {
		t.Fatalf("unexpected postgres settings round-trip: %#v", updated)
	}
}

func TestGetPostgresSchemaPlanUsesConfiguredSchemaAndTemplate(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "backend", "data")
	sqlDir := filepath.Join(rootDir, "backend", "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir sql dir: %v", err)
	}
	template := "create table if not exists {{schema}}.files (file_id serial primary key);\n" +
		"create table if not exists {{schema}}.run_records (run_id text primary key);\n" +
		"alter table {{schema}}.files add column if not exists search_document tsvector;\n"
	if err := os.WriteFile(filepath.Join(sqlDir, "001_repo_cockpit_postgres.sql"), []byte(template), 0o644); err != nil {
		t.Fatalf("write sql template: %v", err)
	}
	dsn := "postgresql://xmustard:secret@localhost:5432/xmustard"
	if _, err := UpdateSettings(dataDir, AppSettings{
		LocalAgentType: "codex",
		PostgresDSN:    &dsn,
		PostgresSchema: "xmustard_plan",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	plan, err := GetPostgresSchemaPlan(dataDir)
	if err != nil {
		t.Fatalf("get postgres plan: %v", err)
	}
	if !plan.Configured || plan.SchemaName != "xmustard_plan" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if len(plan.TableNames) != 2 || plan.TableNames[0] != "files" || plan.TableNames[1] != "run_records" {
		t.Fatalf("unexpected table names: %#v", plan.TableNames)
	}
	if len(plan.SearchDocumentTables) != 1 || plan.SearchDocumentTables[0] != "files" {
		t.Fatalf("unexpected search document tables: %#v", plan.SearchDocumentTables)
	}
	if plan.DSNRedacted == nil || (!strings.Contains(*plan.DSNRedacted, "***") && !strings.Contains(*plan.DSNRedacted, "%2A%2A%2A")) {
		t.Fatalf("expected redacted dsn, got %#v", plan.DSNRedacted)
	}
}

func TestRenderPostgresSchemaSQLUsesRequestedSchema(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "backend", "data")
	sqlDir := filepath.Join(rootDir, "backend", "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir sql dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sqlDir, "001_repo_cockpit_postgres.sql"), []byte("create schema if not exists {{schema}};\n"), 0o644); err != nil {
		t.Fatalf("write sql template: %v", err)
	}
	if _, err := UpdateSettings(dataDir, AppSettings{LocalAgentType: "codex"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	rendered, err := RenderPostgresSchemaSQL(dataDir, "xmustard_render")
	if err != nil {
		t.Fatalf("render postgres schema: %v", err)
	}
	if !strings.Contains(rendered, "xmustard_render") {
		t.Fatalf("expected requested schema in rendered SQL: %s", rendered)
	}
}

func TestBootstrapPostgresSchemaRejectsEmptyDSN(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "backend", "data")
	sqlDir := filepath.Join(rootDir, "backend", "sql")
	if err := os.MkdirAll(sqlDir, 0o755); err != nil {
		t.Fatalf("mkdir sql dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sqlDir, "001_repo_cockpit_postgres.sql"), []byte("create schema if not exists {{schema}};\n"), 0o644); err != nil {
		t.Fatalf("write sql template: %v", err)
	}
	if _, err := UpdateSettings(dataDir, AppSettings{LocalAgentType: "codex"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if _, err := BootstrapPostgresSchema(dataDir, PostgresBootstrapRequest{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected empty dsn validation error, got %v", err)
	}
}
