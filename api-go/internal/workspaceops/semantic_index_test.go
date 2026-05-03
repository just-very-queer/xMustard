package workspaceops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"xmustard/api-go/internal/rustcore"
)

type fakeSemanticBaselineConn struct {
	row pgx.Row
}

func (f *fakeSemanticBaselineConn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *fakeSemanticBaselineConn) QueryRow(context.Context, string, ...any) pgx.Row {
	return f.row
}

func (f *fakeSemanticBaselineConn) Close(context.Context) error {
	return nil
}

type fakeSemanticBaselineRow struct {
	values []any
	err    error
}

func (f fakeSemanticBaselineRow) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	if len(dest) != len(f.values) {
		return errors.New("scan arity mismatch")
	}
	for idx, target := range dest {
		switch typed := target.(type) {
		case *string:
			*typed = f.values[idx].(string)
		case **string:
			value, _ := f.values[idx].(*string)
			*typed = value
		case *int:
			*typed = f.values[idx].(int)
		case *bool:
			*typed = f.values[idx].(bool)
		case *[]byte:
			*typed = f.values[idx].([]byte)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

func TestPlanSemanticIndexSelectsCliWeightedPaths(t *testing.T) {
	dataDir, workspaceID, repoRoot := writeSemanticIndexFixture(t)
	plan, err := PlanSemanticIndex(dataDir, workspaceID, SemanticIndexRequest{
		Surface: "cli",
		Limit:   4,
	})
	if err != nil {
		t.Fatalf("plan semantic index: %v", err)
	}
	if plan.Surface != "cli" {
		t.Fatalf("expected cli surface, got %#v", plan)
	}
	if len(plan.SelectedPaths) == 0 || plan.SelectedPaths[0] != "backend/app/cli.py" {
		t.Fatalf("expected backend/app/cli.py to be prioritized, got %#v", plan.SelectedPaths)
	}
	if plan.PostgresConfigured {
		t.Fatalf("expected Postgres to be unconfigured, got %#v", plan)
	}
	if plan.CanRun {
		t.Fatalf("expected plan to be blocked without Postgres DSN, got %#v", plan)
	}
	if plan.RootPath != repoRoot {
		t.Fatalf("expected root path %s, got %s", repoRoot, plan.RootPath)
	}
}

func TestReadSemanticIndexStatusUsesStoredBaselineFreshness(t *testing.T) {
	dataDir, workspaceID, _ := writeSemanticIndexFixture(t)
	dsn := "postgresql://xmustard:secret@localhost:5432/xmustard"
	settings := appSettings{LocalAgentType: "codex", PostgresDSN: &dsn, PostgresSchema: "agent_context"}
	if err := writeJSON(filepath.Join(dataDir, "settings.json"), settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	plan, err := PlanSemanticIndex(dataDir, workspaceID, SemanticIndexRequest{
		Surface: "cli",
		DSN:     &dsn,
		Schema:  optionalString("agent_context"),
		Limit:   4,
	})
	if err != nil {
		t.Fatalf("plan semantic index before baseline: %v", err)
	}
	selectedDetails := plan.SelectedPathDetails
	selectedDetailsJSON, _ := json.Marshal(selectedDetails)
	selectedPathsJSON, _ := json.Marshal(plan.SelectedPaths)
	baselinePathsJSON, _ := json.Marshal(plan.SelectedPaths)
	connectBackup := connectSemanticPostgres
	connectSemanticPostgres = func(context.Context, string) (semanticMaterializationConn, error) {
		return &fakeSemanticBaselineConn{
			row: fakeSemanticBaselineRow{
				values: []any{
					"semidx_fixture",
					workspaceID,
					"cli",
					"key_files",
					firstNonEmptyPtr(plan.IndexFingerprint),
					plan.HeadSHA,
					0,
					false,
					selectedPathsJSON,
					selectedDetailsJSON,
					baselinePathsJSON,
					1,
					1,
					1,
					"agent_context",
					false,
					false,
					"2026-05-03T00:00:00Z",
				},
			},
		}, nil
	}
	defer func() {
		connectSemanticPostgres = connectBackup
	}()

	status, err := ReadSemanticIndexStatus(dataDir, workspaceID, SemanticIndexRequest{
		Surface: "cli",
		DSN:     &dsn,
		Schema:  optionalString("agent_context"),
		Limit:   4,
	})
	if err != nil {
		t.Fatalf("read semantic index status: %v", err)
	}
	if status.Status != "fresh" {
		t.Fatalf("expected fresh status, got %#v", status)
	}
	if !status.FingerprintMatch {
		t.Fatalf("expected fingerprint match, got %#v", status)
	}
	if status.Baseline == nil || status.Baseline.IndexRunID != "semidx_fixture" {
		t.Fatalf("expected baseline payload, got %#v", status)
	}
}

func writeSemanticIndexFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(repoRoot, "backend", "app"), 0o755); err != nil {
		t.Fatalf("mkdir backend app: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "frontend", "src"), 0o755); err != nil {
		t.Fatalf("mkdir frontend src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "backend", "app", "cli.py"), []byte("def main():\n    return True\n"), 0o644); err != nil {
		t.Fatalf("write cli.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "frontend", "src", "App.tsx"), []byte("export const App = () => null;\n"), 0o644); err != nil {
		t.Fatalf("write App.tsx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "package.json"), []byte(`{"scripts":{"dev":"vite","test":"vitest run"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	workspaceID := "repo-fixture"
	now := nowUTC()
	workspaces := []workspaceRecord{{
		WorkspaceID: workspaceID,
		Name:        "repo",
		RootPath:    repoRoot,
		CreatedAt:   &now,
		UpdatedAt:   &now,
	}}
	if err := writeJSON(filepath.Join(dataDir, "workspaces.json"), workspaces); err != nil {
		t.Fatalf("write workspaces: %v", err)
	}
	snapshot := workspaceSnapshot{
		ScannerVersion: 2,
		Workspace: workspaceRecord{
			WorkspaceID: workspaceID,
			Name:        "repo",
			RootPath:    repoRoot,
			CreatedAt:   &now,
			UpdatedAt:   &now,
		},
		Summary:      map[string]int{},
		Issues:       []issueRecord{},
		Signals:      []discoverySignal{},
		Sources:      []sourceRecord{},
		DriftSummary: map[string]int{},
		Runtimes:     []runtimeCapabilities{},
		GeneratedAt:  now,
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "snapshot.json"), snapshot); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	repoMap := rustcore.RepoMapSummary{
		WorkspaceID: workspaceID,
		RootPath:    repoRoot,
		TotalFiles:  2,
		SourceFiles: 2,
		KeyFiles: []rustcore.RepoMapFileRecord{
			{Path: "backend/app/cli.py", Role: "entry"},
			{Path: "frontend/src/App.tsx", Role: "entry"},
		},
		GeneratedAt: now,
	}
	if err := writeJSON(filepath.Join(dataDir, "workspaces", workspaceID, "repo_map.json"), repoMap); err != nil {
		t.Fatalf("write repo map: %v", err)
	}
	return dataDir, workspaceID, repoRoot
}
