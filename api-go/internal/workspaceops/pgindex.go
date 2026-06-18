package workspaceops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"xmustard/api-go/internal/rustcore"
)

// Postgres primary store + hybrid search (knowledge layer). Materializes the
// Rust symbol graph into Postgres tables with tsvector search documents, then
// serves hybrid lexical (FTS/BM25-style ts_rank) + structural (hotspot boost)
// search from the database. JSON remains the migration input; Postgres becomes
// the queryable index. Connection via XMUSTARD_PG_DSN.

func pgDSN() string {
	if v := os.Getenv("XMUSTARD_PG_DSN"); v != "" {
		return v
	}
	return "postgres://postgres@127.0.0.1:5433/xmustard"
}

const pgIndexSchema = `
create table if not exists xm_files (
  workspace_id text not null, path text not null, symbol_count int not null default 0,
  search_text text not null default '', doc tsvector,
  primary key (workspace_id, path));
create table if not exists xm_symbols (
  id bigserial primary key, workspace_id text not null, name text not null, kind text not null,
  path text not null, line_start int, search_text text not null default '', doc tsvector);
create table if not exists xm_edges (
  workspace_id text not null, from_path text not null, to_path text not null, weight int not null default 1);
create index if not exists xm_symbols_doc_idx on xm_symbols using gin (doc);
create index if not exists xm_files_doc_idx on xm_files using gin (doc);
create index if not exists xm_symbols_ws_idx on xm_symbols (workspace_id);
create index if not exists xm_edges_to_idx on xm_edges (workspace_id, to_path);
`

type sgFile struct {
	Path        string `json:"path"`
	SymbolCount int    `json:"symbol_count"`
}
type sgSymbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	LineStart *int   `json:"line_start"`
}
type sgEdge struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
	Weight   int    `json:"weight"`
}
type sgGraph struct {
	Files   []sgFile   `json:"files"`
	Symbols []sgSymbol `json:"symbols"`
	Edges   []sgEdge   `json:"edges"`
}

// searchText expands an identifier/path into space-separated subtokens (snake +
// camelCase) so the tsvector matches "dashboard" against "BuildWorkspaceDashboard".
func searchText(parts ...string) string {
	var out []string
	for _, p := range parts {
		for _, word := range strings.FieldsFunc(p, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			out = append(out, strings.ToLower(word))
			// camelCase split
			start := 0
			rs := []rune(word)
			for i := 1; i < len(rs); i++ {
				if unicode.IsUpper(rs[i]) && !unicode.IsUpper(rs[i-1]) {
					if i-start >= 2 {
						out = append(out, strings.ToLower(string(rs[start:i])))
					}
					start = i
				}
			}
			if len(rs)-start >= 2 {
				out = append(out, strings.ToLower(string(rs[start:])))
			}
		}
	}
	return strings.Join(out, " ")
}

// MaterializePostgresIndex builds the symbol graph and loads it into Postgres.
func MaterializePostgresIndex(dataDir, workspaceID string) (map[string]any, error) {
	root, _, err := resolveChangeRoot(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	raw, err := rustcore.RunSymbolgraph("build", root, workspaceID)
	if err != nil {
		return nil, err
	}
	var g sgGraph
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("decode symbol graph: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, pgDSN())
	if err != nil {
		return nil, fmt.Errorf("postgres connect (%s): %w", pgDSN(), err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, pgIndexSchema); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	for _, table := range []string{"xm_files", "xm_symbols", "xm_edges"} {
		if _, err := tx.Exec(ctx, "delete from "+table+" where workspace_id=$1", workspaceID); err != nil {
			return nil, err
		}
	}
	for _, f := range g.Files {
		st := searchText(f.Path)
		if _, err := tx.Exec(ctx,
			"insert into xm_files(workspace_id,path,symbol_count,search_text,doc) values($1,$2,$3,$4,to_tsvector('simple',$4))",
			workspaceID, f.Path, f.SymbolCount, st); err != nil {
			return nil, err
		}
	}
	for _, s := range g.Symbols {
		st := searchText(s.Name, s.Path)
		if _, err := tx.Exec(ctx,
			"insert into xm_symbols(workspace_id,name,kind,path,line_start,search_text,doc) values($1,$2,$3,$4,$5,$6,to_tsvector('simple',$6))",
			workspaceID, s.Name, s.Kind, s.Path, s.LineStart, st); err != nil {
			return nil, err
		}
	}
	for _, e := range g.Edges {
		if _, err := tx.Exec(ctx,
			"insert into xm_edges(workspace_id,from_path,to_path,weight) values($1,$2,$3,$4)",
			workspaceID, e.FromPath, e.ToPath, e.Weight); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": workspaceID,
		"files":        len(g.Files),
		"symbols":      len(g.Symbols),
		"edges":        len(g.Edges),
		"store":        "postgres",
		"indexed_at":   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// PostgresSearchHit is one hybrid search result from Postgres.
type PostgresSearchHit struct {
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Score float64 `json:"score"`
}

// SearchPostgres runs hybrid FTS (ts_rank) + structural (inbound-edge boost) search.
func SearchPostgres(dataDir, workspaceID, query string, limit int) (map[string]any, error) {
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

	// hybrid: FTS rank on the symbol doc + a small boost for symbols in
	// high-inbound-edge (hotspot) files.
	rows, err := conn.Query(ctx, `
		select s.kind, s.name, s.path,
		       ts_rank(s.doc, websearch_to_tsquery('simple', $2))
		         + coalesce((select least(count(*)::float / 50.0, 1.0) from xm_edges e
		                     where e.workspace_id = s.workspace_id and e.to_path = s.path), 0) as score
		from xm_symbols s
		where s.workspace_id = $1 and s.doc @@ websearch_to_tsquery('simple', $2)
		order by score desc, s.name
		limit $3`, workspaceID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("pg search: %w", err)
	}
	defer rows.Close()
	hits := []PostgresSearchHit{}
	for rows.Next() {
		var h PostgresSearchHit
		h.Kind = "symbol"
		if err := rows.Scan(&h.Kind, &h.Name, &h.Path, &h.Score); err != nil {
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
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}, nil
}
