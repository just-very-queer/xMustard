from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Optional
from urllib.parse import urlsplit, urlunsplit

from .models import (
    FileSymbolSummaryMaterializationRecord,
    PostgresBootstrapResult,
    PostgresSchemaPlan,
    PostgresSemanticMaterializationResult,
    SemanticMatchMaterializationRecord,
    SemanticIndexBaselineRecord,
    SemanticIndexPathSelection,
    SemanticQueryMaterializationRecord,
    SymbolMaterializationRecord,
)


SCHEMA_NAME_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
TABLE_NAME_RE = re.compile(
    r"create table if not exists\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)",
    re.IGNORECASE,
)
SEARCH_DOCUMENT_TABLE_RE = re.compile(
    r"alter table\s+(?:[a-zA-Z_][a-zA-Z0-9_]*\.)?([a-zA-Z_][a-zA-Z0-9_]*)\s+add column if not exists\s+search_document\b",
    re.IGNORECASE,
)

SEMANTIC_TABLES = {
    "files",
    "symbols",
    "symbol_edges",
    "file_symbol_summaries",
    "semantic_queries",
    "semantic_matches",
    "semantic_index_runs",
    "diagnostics",
}
OPS_MEMORY_TABLES = {
    "activity_events",
    "run_records",
    "run_plans",
    "run_plan_revisions",
    "verification_profiles",
    "verification_runs",
    "issue_artifacts",
}


def sql_template_path(root: Path) -> Path:
    return root / "sql" / "001_repo_cockpit_postgres.sql"


def validate_schema_name(schema: str) -> str:
    normalized = schema.strip()
    if not normalized:
        raise ValueError("Postgres schema must not be empty")
    if not SCHEMA_NAME_RE.match(normalized):
        raise ValueError("Postgres schema must start with a letter or underscore and contain only letters, digits, or underscores")
    return normalized


def redact_dsn(dsn: Optional[str]) -> Optional[str]:
    if not dsn:
        return None
    parsed = urlsplit(dsn)
    if not parsed.scheme or not parsed.netloc:
        return "<configured>"
    host = parsed.hostname or ""
    port = f":{parsed.port}" if parsed.port else ""
    user = parsed.username or "user"
    netloc = f"{user}:***@{host}{port}"
    return urlunsplit((parsed.scheme, netloc, parsed.path, parsed.query, parsed.fragment))


def render_schema_sql(root: Path, schema: str) -> tuple[str, Path]:
    normalized_schema = validate_schema_name(schema)
    sql_path = sql_template_path(root)
    template = sql_path.read_text(encoding="utf-8")
    rendered = template.replace("{{schema}}", normalized_schema)
    return rendered, sql_path


def split_sql_statements(sql_text: str) -> list[str]:
    statements = []
    for chunk in sql_text.split(";\n"):
        statement = chunk.strip()
        if not statement:
            continue
        statements.append(f"{statement};")
    return statements


def extract_table_names(sql_text: str) -> list[str]:
    return sorted({match.group(1) for match in TABLE_NAME_RE.finditer(sql_text)})


def extract_search_document_tables(sql_text: str) -> list[str]:
    return sorted({match.group(1) for match in SEARCH_DOCUMENT_TABLE_RE.finditer(sql_text)})


def build_schema_plan(root: Path, dsn: Optional[str], schema: str) -> PostgresSchemaPlan:
    sql_text, sql_path = render_schema_sql(root, schema)
    statements = split_sql_statements(sql_text)
    table_names = extract_table_names(sql_text)
    return PostgresSchemaPlan(
        configured=bool(dsn),
        dsn_redacted=redact_dsn(dsn),
        schema_name=validate_schema_name(schema),
        sql_path=str(sql_path),
        statement_count=len(statements),
        table_names=table_names,
        semantic_table_names=[name for name in table_names if name in SEMANTIC_TABLES],
        ops_memory_table_names=[name for name in table_names if name in OPS_MEMORY_TABLES],
        search_document_tables=extract_search_document_tables(sql_text),
    )


def materialize_path_symbols(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    workspace_name: str,
    workspace_root: str,
    relative_path: str,
    file_summary_row: FileSymbolSummaryMaterializationRecord,
    symbol_rows: list[SymbolMaterializationRecord],
) -> PostgresSemanticMaterializationResult:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic materialization")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            _upsert_workspace(cursor, normalized_schema, workspace_id, workspace_name, workspace_root)
            file_id = _upsert_file(
                cursor,
                normalized_schema,
                workspace_id,
                workspace_root,
                relative_path,
                language=file_summary_row.language,
                role="source",
            )
            cursor.execute(
                f"delete from {normalized_schema}.symbols where workspace_id = %s and path = %s",
                (workspace_id, relative_path),
            )
            inserted_symbols = 0
            for row in symbol_rows:
                cursor.execute(
                    f"""
                    insert into {normalized_schema}.symbols (
                        workspace_id, file_id, path, symbol, kind, language, line_start, line_end, enclosing_scope, signature_text, symbol_text
                    ) values (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                    """,
                    (
                        row.workspace_id,
                        file_id,
                        row.path,
                        row.symbol,
                        row.kind,
                        row.language,
                        row.line_start,
                        row.line_end,
                        row.enclosing_scope,
                        row.signature_text,
                        row.symbol_text,
                    ),
                )
                inserted_symbols += 1
            cursor.execute(
                f"""
                insert into {normalized_schema}.file_symbol_summaries (
                    workspace_id, file_id, path, language, parser_language, symbol_source, symbol_count, summary_json
                ) values (%s, %s, %s, %s, %s, %s, %s, %s::jsonb)
                on conflict (workspace_id, path) do update set
                    file_id = excluded.file_id,
                    language = excluded.language,
                    parser_language = excluded.parser_language,
                    symbol_source = excluded.symbol_source,
                    symbol_count = excluded.symbol_count,
                    summary_json = excluded.summary_json,
                    indexed_at = now()
                """,
                (
                    workspace_id,
                    file_id,
                    relative_path,
                    file_summary_row.language,
                    file_summary_row.parser_language,
                    file_summary_row.symbol_source,
                    file_summary_row.symbol_count,
                    json.dumps(file_summary_row.summary_json),
                ),
            )

    return PostgresSemanticMaterializationResult(
        applied=True,
        dsn_redacted=redact_dsn(dsn),
        schema_name=normalized_schema,
        workspace_id=workspace_id,
        source="path_symbols",
        target=relative_path,
        materialized_paths=[relative_path],
        file_rows=1 if relative_path else 0,
        symbol_rows=inserted_symbols,
        summary_rows=1,
        message=f"Materialized parser-backed symbol rows for {relative_path} into Postgres schema '{normalized_schema}'.",
    )


def materialize_semantic_search(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    workspace_name: str,
    workspace_root: str,
    query_row: SemanticQueryMaterializationRecord,
    match_rows: list[SemanticMatchMaterializationRecord],
) -> PostgresSemanticMaterializationResult:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic materialization")
    psycopg = _load_psycopg()
    materialized_paths = sorted({row.path for row in match_rows if row.path})
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            _upsert_workspace(cursor, normalized_schema, workspace_id, workspace_name, workspace_root)
            cursor.execute(
                f"""
                insert into {normalized_schema}.semantic_queries (
                    workspace_id, issue_id, run_id, source, reason, pattern, language, path_glob, engine, match_count, truncated, error
                ) values (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
                returning query_id
                """,
                (
                    query_row.workspace_id,
                    query_row.issue_id,
                    query_row.run_id,
                    query_row.source,
                    query_row.reason,
                    query_row.pattern,
                    query_row.language,
                    query_row.path_glob,
                    query_row.engine,
                    query_row.match_count,
                    query_row.truncated,
                    query_row.error,
                ),
            )
            query_row_id = _fetch_returning_id(cursor)
            file_ids = {
                path: _upsert_file(
                    cursor,
                    normalized_schema,
                    workspace_id,
                    workspace_root,
                    path,
                    language=next((row.language for row in match_rows if row.path == path and row.language), None),
                    role="source",
                )
                for path in materialized_paths
            }
            inserted_matches = 0
            for row in match_rows:
                cursor.execute(
                    f"""
                    insert into {normalized_schema}.semantic_matches (
                        workspace_id, query_id, file_id, symbol_id, path, language, line_start, line_end, column_start, column_end,
                        matched_text, context_lines, meta_variables_json, reason, score
                    ) values (%s, %s, %s, null, %s, %s, %s, %s, %s, %s, %s, %s, %s::jsonb, %s, %s)
                    """,
                    (
                        row.workspace_id,
                        query_row_id,
                        file_ids.get(row.path),
                        row.path,
                        row.language,
                        row.line_start,
                        row.line_end,
                        row.column_start,
                        row.column_end,
                        row.matched_text,
                        row.context_lines,
                        json.dumps(row.meta_variables),
                        row.reason,
                        row.score,
                    ),
                )
                inserted_matches += 1

    return PostgresSemanticMaterializationResult(
        applied=True,
        dsn_redacted=redact_dsn(dsn),
        schema_name=normalized_schema,
        workspace_id=workspace_id,
        source="semantic_search",
        target=query_row.pattern,
        materialized_paths=materialized_paths,
        file_rows=len(materialized_paths),
        query_rows=1,
        match_rows=inserted_matches,
        message=f"Materialized semantic query '{query_row.pattern}' and {inserted_matches} match rows into Postgres schema '{normalized_schema}'.",
    )


def persist_semantic_index_baseline(
    dsn: str,
    schema: str,
    *,
    baseline: SemanticIndexBaselineRecord,
    workspace_name: str,
    workspace_root: str,
) -> SemanticIndexBaselineRecord:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic index baseline persistence")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            _upsert_workspace(cursor, normalized_schema, baseline.workspace_id, workspace_name, workspace_root)
            cursor.execute(
                f"""
                insert into {normalized_schema}.semantic_index_runs (
                    index_run_id, workspace_id, surface, strategy, index_fingerprint, head_sha, dirty_files,
                    worktree_dirty, selected_paths_json, selected_path_details_json, materialized_paths_json,
                    file_rows, symbol_rows, summary_rows, postgres_schema, tree_sitter_available, ast_grep_available
                ) values (%s, %s, %s, %s, %s, %s, %s, %s, %s::jsonb, %s::jsonb, %s::jsonb, %s, %s, %s, %s, %s, %s)
                """,
                (
                    baseline.index_run_id,
                    baseline.workspace_id,
                    baseline.surface,
                    baseline.strategy,
                    baseline.index_fingerprint,
                    baseline.head_sha,
                    baseline.dirty_files,
                    baseline.worktree_dirty,
                    json.dumps(baseline.selected_paths),
                    json.dumps([item.model_dump(mode="json") for item in baseline.selected_path_details]),
                    json.dumps(baseline.materialized_paths),
                    baseline.file_rows,
                    baseline.symbol_rows,
                    baseline.summary_rows,
                    baseline.postgres_schema,
                    baseline.tree_sitter_available,
                    baseline.ast_grep_available,
                ),
            )
    return baseline


def read_latest_semantic_index_baseline(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    surface: str,
    strategy: Optional[str] = None,
) -> Optional[SemanticIndexBaselineRecord]:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic index baseline reads")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            strategy_sql = "and strategy = %s" if strategy else ""
            params = (workspace_id, surface, strategy) if strategy else (workspace_id, surface)
            cursor.execute(
                f"""
                select index_run_id, workspace_id, surface, strategy, index_fingerprint, head_sha, dirty_files,
                    worktree_dirty, selected_paths_json, selected_path_details_json, materialized_paths_json,
                    file_rows, symbol_rows, summary_rows, postgres_schema, tree_sitter_available, ast_grep_available,
                    created_at
                from {normalized_schema}.semantic_index_runs
                where workspace_id = %s and surface = %s
                    {strategy_sql}
                order by created_at desc
                limit 1
                """,
                params,
            )
            row = cursor.fetchone()
    if not row:
        return None
    return _semantic_index_baseline_from_row(row)


def read_file_symbol_summary(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    relative_path: str,
) -> Optional[FileSymbolSummaryMaterializationRecord]:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for file symbol summary reads")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                f"""
                select workspace_id, path, language, parser_language, symbol_source, symbol_count, summary_json
                from {normalized_schema}.file_symbol_summaries
                where workspace_id = %s and path = %s
                order by indexed_at desc
                limit 1
                """,
                (workspace_id, relative_path),
            )
            row = cursor.fetchone()
    if not row:
        return None
    return FileSymbolSummaryMaterializationRecord(
        workspace_id=str(row[0]),
        path=str(row[1]),
        language=row[2],
        parser_language=row[3],
        symbol_source=row[4] if row[4] in {"tree_sitter", "regex", "none"} else "none",
        symbol_count=int(row[5] or 0),
        summary_json=_json_dict(row[6]),
    )


def read_symbols_for_path(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    relative_path: str,
    limit: int = 100,
) -> list[SymbolMaterializationRecord]:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for symbol reads")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                f"""
                select workspace_id, path, symbol, kind, language, line_start, line_end,
                    enclosing_scope, signature_text, symbol_text
                from {normalized_schema}.symbols
                where workspace_id = %s and path = %s
                order by coalesce(line_start, 2147483647), symbol
                limit %s
                """,
                (workspace_id, relative_path, max(1, min(limit, 500))),
            )
            rows = cursor.fetchall()
    return [
        SymbolMaterializationRecord(
            workspace_id=str(row[0]),
            path=str(row[1]),
            symbol=str(row[2]),
            kind=row[3] if row[3] in {"function", "class", "method", "type", "module"} else "function",
            language=row[4],
            line_start=row[5],
            line_end=row[6],
            enclosing_scope=row[7],
            signature_text=row[8],
            symbol_text=row[9],
        )
        for row in rows
    ]


def read_semantic_queries(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    limit: int = 50,
) -> list[SemanticQueryMaterializationRecord]:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic query reads")
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                f"""
                select query_id, workspace_id, issue_id, run_id, source, reason, pattern, language,
                    path_glob, engine, match_count, truncated, error
                from {normalized_schema}.semantic_queries
                where workspace_id = %s
                order by executed_at desc
                limit %s
                """,
                (workspace_id, max(1, min(limit, 500))),
            )
            rows = cursor.fetchall()
    return [
        SemanticQueryMaterializationRecord(
            query_ref=f"semanticq_db_{row[0]}",
            workspace_id=str(row[1]),
            issue_id=row[2],
            run_id=row[3],
            source=row[4] if row[4] in {"adhoc_tool", "issue_context"} else "adhoc_tool",
            reason=row[5],
            pattern=str(row[6]),
            language=row[7],
            path_glob=row[8],
            engine=row[9] if row[9] in {"ast_grep", "none"} else "none",
            match_count=int(row[10] or 0),
            truncated=bool(row[11]),
            error=row[12],
        )
        for row in rows
    ]


def read_semantic_matches(
    dsn: str,
    schema: str,
    *,
    workspace_id: str,
    path: Optional[str] = None,
    limit: int = 50,
) -> list[SemanticMatchMaterializationRecord]:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for semantic match reads")
    params: tuple
    where_sql = "where workspace_id = %s"
    params = (workspace_id, max(1, min(limit, 500)))
    if path:
        where_sql += " and path = %s"
        params = (workspace_id, path, max(1, min(limit, 500)))
    psycopg = _load_psycopg()
    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                f"""
                select query_id, workspace_id, path, language, line_start, line_end, column_start, column_end,
                    matched_text, context_lines, meta_variables_json, reason, score
                from {normalized_schema}.semantic_matches
                {where_sql}
                order by matched_at desc
                limit %s
                """,
                params,
            )
            rows = cursor.fetchall()
    return [
        SemanticMatchMaterializationRecord(
            query_ref=f"semanticq_db_{row[0]}" if row[0] is not None else "semanticq_db_unknown",
            workspace_id=str(row[1]),
            path=str(row[2]),
            language=row[3],
            line_start=row[4],
            line_end=row[5],
            column_start=row[6],
            column_end=row[7],
            matched_text=str(row[8]),
            context_lines=row[9],
            meta_variables=[str(item) for item in _json_list(row[10])],
            reason=row[11],
            score=int(row[12] or 0),
        )
        for row in rows
    ]


def bootstrap_schema(root: Path, dsn: str, schema: str) -> PostgresBootstrapResult:
    normalized_schema = validate_schema_name(schema)
    if not dsn or not dsn.strip():
        raise ValueError("Postgres DSN is required for bootstrap")
    sql_text, sql_path = render_schema_sql(root, normalized_schema)
    statements = split_sql_statements(sql_text)
    table_names = extract_table_names(sql_text)
    try:
        import psycopg
    except ModuleNotFoundError as exc:
        raise RuntimeError("psycopg is not installed. Install backend dependencies with psycopg support before bootstrapping Postgres.") from exc

    with psycopg.connect(dsn, autocommit=True) as connection:
        with connection.cursor() as cursor:
            for statement in statements:
                cursor.execute(statement)

    return PostgresBootstrapResult(
        applied=True,
        dsn_redacted=redact_dsn(dsn),
        schema_name=normalized_schema,
        sql_path=str(sql_path),
        statement_count=len(statements),
        table_names=table_names,
        semantic_table_names=[name for name in table_names if name in SEMANTIC_TABLES],
        search_document_tables=extract_search_document_tables(sql_text),
        message=f"Applied repo cockpit foundation schema to Postgres schema '{normalized_schema}'.",
    )


def _load_psycopg():
    try:
        import psycopg
    except ModuleNotFoundError as exc:
        raise RuntimeError("psycopg is not installed. Install backend dependencies with psycopg support before using Postgres-backed features.") from exc
    return psycopg


def _upsert_workspace(cursor, schema: str, workspace_id: str, workspace_name: str, workspace_root: str) -> None:
    cursor.execute(
        f"""
        insert into {schema}.workspaces (workspace_id, name, root_path, latest_scan_at)
        values (%s, %s, %s, now())
        on conflict (workspace_id) do update set
            name = excluded.name,
            root_path = excluded.root_path,
            latest_scan_at = now(),
            updated_at = now()
        """,
        (workspace_id, workspace_name, workspace_root),
    )


def _upsert_file(
    cursor,
    schema: str,
    workspace_id: str,
    workspace_root: str,
    relative_path: str,
    *,
    language: Optional[str],
    role: str,
) -> Optional[int]:
    size_bytes, content_hash = _file_metadata(workspace_root, relative_path)
    cursor.execute(
        f"""
        insert into {schema}.files (workspace_id, path, role, language, size_bytes, content_hash)
        values (%s, %s, %s, %s, %s, %s)
        on conflict (workspace_id, path) do update set
            role = excluded.role,
            language = coalesce(excluded.language, files.language),
            size_bytes = excluded.size_bytes,
            content_hash = excluded.content_hash,
            last_indexed_at = now()
        returning file_id
        """,
        (workspace_id, relative_path, role, language, size_bytes, content_hash),
    )
    return _fetch_returning_id(cursor)


def _file_metadata(workspace_root: str, relative_path: str) -> tuple[Optional[int], Optional[str]]:
    path = Path(workspace_root) / relative_path
    if not path.exists() or not path.is_file():
        return None, None
    raw = path.read_bytes()
    return len(raw), hashlib.sha1(raw).hexdigest()


def _fetch_returning_id(cursor) -> Optional[int]:
    row = cursor.fetchone()
    if not row:
        return None
    value = row[0]
    return int(value) if isinstance(value, int) else None


def _semantic_index_baseline_from_row(row) -> SemanticIndexBaselineRecord:
    selected_path_details = [
        item if isinstance(item, SemanticIndexPathSelection) else SemanticIndexPathSelection(**item)
        for item in _json_list(row[9])
        if isinstance(item, (dict, SemanticIndexPathSelection))
    ]
    return SemanticIndexBaselineRecord(
        index_run_id=str(row[0]),
        workspace_id=str(row[1]),
        surface=row[2] if row[2] in {"cli", "web", "all"} else "cli",
        strategy=row[3] if row[3] in {"key_files", "paths"} else "key_files",
        index_fingerprint=str(row[4]),
        head_sha=row[5],
        dirty_files=int(row[6] or 0),
        worktree_dirty=bool(row[7]),
        selected_paths=[str(item) for item in _json_list(row[8])],
        selected_path_details=selected_path_details,
        materialized_paths=[str(item) for item in _json_list(row[10])],
        file_rows=int(row[11] or 0),
        symbol_rows=int(row[12] or 0),
        summary_rows=int(row[13] or 0),
        postgres_schema=str(row[14] or "xmustard"),
        tree_sitter_available=bool(row[15]),
        ast_grep_available=bool(row[16]),
        created_at=row[17].isoformat() if hasattr(row[17], "isoformat") else str(row[17]),
    )


def _json_list(value) -> list:
    if value is None:
        return []
    if isinstance(value, list):
        return value
    if isinstance(value, str):
        try:
            parsed = json.loads(value)
        except json.JSONDecodeError:
            return []
        return parsed if isinstance(parsed, list) else []
    return []


def _json_dict(value) -> dict:
    if value is None:
        return {}
    if isinstance(value, dict):
        return value
    if isinstance(value, str):
        try:
            parsed = json.loads(value)
        except json.JSONDecodeError:
            return {}
        return parsed if isinstance(parsed, dict) else {}
    return {}
