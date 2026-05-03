package workspaceops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSearchSemanticPatternParsesAstGrepStream(t *testing.T) {
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	installFakeAstGrep(t, repoRoot)

	result, err := SearchSemanticPattern(dataDir, workspaceID, "ExportService", "", "", 10)
	if err != nil {
		t.Fatalf("search semantic pattern: %v", err)
	}
	if result.Engine != "ast_grep" || result.BinaryPath == nil {
		t.Fatalf("expected ast-grep engine and binary path, got %#v", result)
	}
	if result.MatchCount != 1 || len(result.Matches) != 1 || len(result.MatchRows) != 1 {
		t.Fatalf("expected one semantic match, got %#v", result)
	}
	if result.Matches[0].Path != "src/app.py" || result.QueryRow == nil {
		t.Fatalf("unexpected semantic payload: %#v", result)
	}
}

func TestMaterializePathSymbolsToPostgresUsesRustRows(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	fakeConn := &fakeSemanticConn{queryIDs: []int64{11}}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := MaterializePathSymbolsToPostgres(dataDir, workspaceID, PostgresPathMaterializationRequest{
		Path:       "src/app.py",
		DSN:        optionalString("postgres://user:secret@example.com/xmustard"),
		SchemaName: optionalString("xmustard"),
	})
	if err != nil {
		t.Fatalf("materialize path symbols: %v", err)
	}
	if !result.Applied || result.SymbolRows == 0 || result.SummaryRows != 1 {
		t.Fatalf("unexpected path materialization result: %#v", result)
	}
	if !containsSubstring(fakeConn.execSQL, "insert into xmustard.symbols") || !containsSubstring(fakeConn.execSQL, "insert into xmustard.file_symbol_summaries") {
		t.Fatalf("expected symbol and summary writes, got %#v", fakeConn.execSQL)
	}
	activityPath := filepath.Join(dataDir, "workspaces", workspaceID, "activity.jsonl")
	activityContent, err := os.ReadFile(activityPath)
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !strings.Contains(string(activityContent), "postgres.materialize.path_symbols") {
		t.Fatalf("expected path materialization activity, got %s", activityContent)
	}
}

func TestMaterializeWorkspaceSymbolsToPostgresBatchesSelectedPaths(t *testing.T) {
	dataDir, workspaceID, _, _ := writeIssueContextFixture(t, false)

	fakeConn := &fakeSemanticConn{queryIDs: []int64{11, 12}}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := MaterializeWorkspaceSymbolsToPostgres(dataDir, workspaceID, PostgresWorkspaceSemanticMaterializationRequest{
		Strategy: "key_files",
		Limit:    2,
		DSN:      optionalString("postgres://user:secret@example.com/xmustard"),
	})
	if err != nil {
		t.Fatalf("materialize workspace symbols: %v", err)
	}
	if !result.Applied || len(result.RequestedPaths) == 0 || len(result.MaterializedPaths) == 0 {
		t.Fatalf("expected workspace materialization to select and write paths, got %#v", result)
	}
	if result.MaterializedPaths[0] != "src/app.py" {
		t.Fatalf("unexpected materialized paths: %#v", result.MaterializedPaths)
	}
}

func TestMaterializeSemanticSearchToPostgresWritesQueryAndMatches(t *testing.T) {
	dataDir, workspaceID, _, repoRoot := writeIssueContextFixture(t, false)
	installFakeAstGrep(t, repoRoot)

	fakeConn := &fakeSemanticConn{queryIDs: []int64{41, 42}}
	restore := stubSemanticPostgresConnection(fakeConn)
	defer restore()

	result, err := MaterializeSemanticSearchToPostgres(dataDir, workspaceID, PostgresSemanticSearchMaterializationRequest{
		Pattern:    "ExportService",
		Limit:      10,
		DSN:        optionalString("postgres://user:secret@example.com/xmustard"),
		SchemaName: optionalString("xmustard"),
	})
	if err != nil {
		t.Fatalf("materialize semantic search: %v", err)
	}
	if !result.Applied || result.QueryRows != 1 || result.MatchRows != 1 {
		t.Fatalf("unexpected semantic-search materialization result: %#v", result)
	}
	if !containsSubstring(fakeConn.querySQL, "insert into xmustard.semantic_queries") || !containsSubstring(fakeConn.execSQL, "insert into xmustard.semantic_matches") {
		t.Fatalf("expected semantic query + match writes, query=%#v exec=%#v", fakeConn.querySQL, fakeConn.execSQL)
	}
}

type fakeSemanticConn struct {
	queryIDs []int64
	querySQL []string
	execSQL  []string
}

func (f *fakeSemanticConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execSQL = append(f.execSQL, sql)
	return pgconn.CommandTag{}, nil
}

func (f *fakeSemanticConn) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.querySQL = append(f.querySQL, sql)
	index := len(f.querySQL) - 1
	id := int64(index + 1)
	if index < len(f.queryIDs) {
		id = f.queryIDs[index]
	}
	return fakeSemanticRow{id: id}
}

func (f *fakeSemanticConn) Close(context.Context) error {
	return nil
}

type fakeSemanticRow struct {
	id int64
}

func (f fakeSemanticRow) Scan(dest ...any) error {
	if len(dest) == 0 {
		return nil
	}
	if target, ok := dest[0].(*int64); ok {
		*target = f.id
	}
	return nil
}

func stubSemanticPostgresConnection(connection semanticMaterializationConn) func() {
	previous := connectSemanticPostgres
	connectSemanticPostgres = func(context.Context, string) (semanticMaterializationConn, error) {
		return connection, nil
	}
	return func() {
		connectSemanticPostgres = previous
	}
}

func installFakeAstGrep(t *testing.T, repoRoot string) {
	t.Helper()
	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "sg")
	content := "#!/bin/sh\ncat <<'EOF'\n{\"file\":\"" + filepath.Join(repoRoot, "src", "app.py") + "\",\"text\":\"ExportService\",\"language\":\"Python\",\"range\":{\"start\":{\"line\":0,\"column\":6},\"end\":{\"line\":0,\"column\":19}},\"lines\":\"class ExportService:\",\"metaVariables\":{\"single\":{\"$NAME\":{}}}}\nEOF\n"
	if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake ast-grep: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)
}

func containsSubstring(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}
