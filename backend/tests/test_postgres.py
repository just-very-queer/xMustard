import sys
import tempfile
import types
import unittest
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

from app.models import (
    FileSymbolSummaryMaterializationRecord,
    SemanticIndexBaselineRecord,
    SemanticIndexPathSelection,
    SemanticMatchMaterializationRecord,
    SemanticQueryMaterializationRecord,
    SymbolMaterializationRecord,
)
from app.postgres import (
    materialize_path_symbols,
    materialize_semantic_search,
    persist_semantic_index_baseline,
    read_file_symbol_summary,
    read_latest_semantic_index_baseline,
    read_semantic_matches,
    read_semantic_queries,
    read_symbols_for_path,
)


class _FakeCursor:
    def __init__(self, returning_values: list[int]) -> None:
        self.returning_values = list(returning_values)
        self.executed: list[tuple[str, object]] = []

    def execute(self, sql: str, params=None) -> None:
        self.executed.append((sql, params))

    def fetchone(self):
        if not self.returning_values:
            return None
        value = self.returning_values.pop(0)
        if isinstance(value, tuple):
            return value
        return (value,)

    def fetchall(self):
        if not self.returning_values:
            return []
        value = self.returning_values.pop(0)
        return value if isinstance(value, list) else []

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


class _FakeConnection:
    def __init__(self, cursor: _FakeCursor) -> None:
        self.cursor_obj = cursor

    def cursor(self) -> _FakeCursor:
        return self.cursor_obj

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False


class PostgresMaterializationTests(unittest.TestCase):
    def test_materialize_path_symbols_executes_summary_and_symbol_writes(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "src").mkdir(parents=True)
            (root / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")

            cursor = _FakeCursor([101])
            fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

            with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
                result = materialize_path_symbols(
                    "postgresql://xmustard:secret@localhost:5432/xmustard",
                    "xmustard",
                    workspace_id="ws_123",
                    workspace_name="fixture",
                    workspace_root=str(root),
                    relative_path="src/example.py",
                    file_summary_row=FileSymbolSummaryMaterializationRecord(
                        workspace_id="ws_123",
                        path="src/example.py",
                        language="python",
                        parser_language="python",
                        symbol_source="tree_sitter",
                        symbol_count=1,
                        summary_json={"top_symbols": ["render_payload"]},
                    ),
                    symbol_rows=[
                        SymbolMaterializationRecord(
                            workspace_id="ws_123",
                            path="src/example.py",
                            symbol="render_payload",
                            kind="function",
                            language="python",
                            line_start=1,
                            line_end=2,
                        )
                    ],
                )

            self.assertTrue(result.applied)
            self.assertEqual(result.file_rows, 1)
            self.assertEqual(result.summary_rows, 1)
            self.assertEqual(result.symbol_rows, 1)
            sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
            self.assertIn("insert into xmustard.workspaces", sql_blob)
            self.assertIn("insert into xmustard.files", sql_blob)
            self.assertIn("delete from xmustard.symbols", sql_blob)
            self.assertIn("insert into xmustard.symbols", sql_blob)
            self.assertIn("insert into xmustard.file_symbol_summaries", sql_blob)

    def test_materialize_semantic_search_executes_query_and_match_writes(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "src").mkdir(parents=True)
            (root / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")

            cursor = _FakeCursor([301, 201])
            fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

            with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
                result = materialize_semantic_search(
                    "postgresql://xmustard:secret@localhost:5432/xmustard",
                    "xmustard",
                    workspace_id="ws_123",
                    workspace_name="fixture",
                    workspace_root=str(root),
                    query_row=SemanticQueryMaterializationRecord(
                        query_ref="semanticq_fixture",
                        workspace_id="ws_123",
                        source="adhoc_tool",
                        pattern="def $A():",
                        language="python",
                        path_glob="src/**/*.py",
                        engine="ast_grep",
                        match_count=1,
                    ),
                    match_rows=[
                        SemanticMatchMaterializationRecord(
                            query_ref="semanticq_fixture",
                            workspace_id="ws_123",
                            path="src/example.py",
                            language="python",
                            line_start=1,
                            line_end=1,
                            column_start=1,
                            column_end=19,
                            matched_text="def render_payload():",
                            context_lines="def render_payload():",
                        )
                    ],
                )

            self.assertTrue(result.applied)
            self.assertEqual(result.query_rows, 1)
            self.assertEqual(result.match_rows, 1)
            self.assertEqual(result.file_rows, 1)
            sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
            self.assertIn("insert into xmustard.semantic_queries", sql_blob)
            self.assertIn("insert into xmustard.semantic_matches", sql_blob)
            self.assertIn("insert into xmustard.files", sql_blob)

    def test_persist_semantic_index_baseline_executes_insert(self):
        baseline = SemanticIndexBaselineRecord(
            index_run_id="semidx_fixture",
            workspace_id="ws_123",
            surface="cli",
            strategy="paths",
            index_fingerprint="fp_fixture",
            head_sha="abc123",
            dirty_files=0,
            selected_paths=["src/example.py"],
            selected_path_details=[
                SemanticIndexPathSelection(path="src/example.py", role="source", score=95, reason="fixture", sha256="sha")
            ],
            materialized_paths=["src/example.py"],
            file_rows=1,
            symbol_rows=2,
            summary_rows=1,
            postgres_schema="xmustard",
            tree_sitter_available=True,
            ast_grep_available=False,
        )
        cursor = _FakeCursor([])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = persist_semantic_index_baseline(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                baseline=baseline,
                workspace_name="fixture",
                workspace_root="/tmp/repo",
            )

        self.assertEqual(result.index_run_id, "semidx_fixture")
        sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
        self.assertIn("insert into xmustard.workspaces", sql_blob)
        self.assertIn("insert into xmustard.semantic_index_runs", sql_blob)

    def test_read_latest_semantic_index_baseline_maps_row(self):
        created_at = datetime(2026, 5, 1, tzinfo=timezone.utc)
        row = (
            "semidx_fixture",
            "ws_123",
            "cli",
            "paths",
            "fp_fixture",
            "abc123",
            0,
            False,
            ["src/example.py"],
            [{"path": "src/example.py", "role": "source", "score": 95, "reason": "fixture", "sha256": "sha"}],
            ["src/example.py"],
            1,
            2,
            1,
            "xmustard",
            True,
            False,
            created_at,
        )
        cursor = _FakeCursor([row])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = read_latest_semantic_index_baseline(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                workspace_id="ws_123",
                surface="cli",
                strategy="paths",
            )

        assert result is not None
        self.assertEqual(result.index_run_id, "semidx_fixture")
        self.assertEqual(result.strategy, "paths")
        self.assertEqual(result.selected_paths, ["src/example.py"])
        self.assertEqual(result.selected_path_details[0].sha256, "sha")
        self.assertEqual(result.symbol_rows, 2)
        self.assertIn("and strategy = %s", cursor.executed[0][0])
        self.assertEqual(cursor.executed[0][1], ("ws_123", "cli", "paths"))

    def test_read_file_symbol_summary_maps_row(self):
        row = (
            "ws_123",
            "src/example.py",
            "python",
            "python",
            "tree_sitter",
            2,
            {"top_symbols": ["ApiHandler", "render_payload"]},
        )
        cursor = _FakeCursor([row])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = read_file_symbol_summary(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                workspace_id="ws_123",
                relative_path="src/example.py",
            )

        assert result is not None
        self.assertEqual(result.workspace_id, "ws_123")
        self.assertEqual(result.path, "src/example.py")
        self.assertEqual(result.symbol_source, "tree_sitter")
        self.assertEqual(result.symbol_count, 2)
        self.assertEqual(result.summary_json["top_symbols"], ["ApiHandler", "render_payload"])
        sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
        self.assertIn("from xmustard.file_symbol_summaries", sql_blob)

    def test_read_symbols_for_path_maps_rows(self):
        rows = [
            (
                "ws_123",
                "src/example.py",
                "ApiHandler",
                "class",
                "python",
                1,
                4,
                None,
                None,
                None,
            ),
            (
                "ws_123",
                "src/example.py",
                "render_payload",
                "method",
                "python",
                2,
                3,
                "ApiHandler",
                None,
                None,
            ),
        ]
        cursor = _FakeCursor([rows])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = read_symbols_for_path(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                workspace_id="ws_123",
                relative_path="src/example.py",
            )

        self.assertEqual([item.symbol for item in result], ["ApiHandler", "render_payload"])
        self.assertEqual(result[1].kind, "method")
        self.assertEqual(result[1].enclosing_scope, "ApiHandler")
        sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
        self.assertIn("from xmustard.symbols", sql_blob)

    def test_read_semantic_queries_maps_rows(self):
        rows = [
            (
                42,
                "ws_123",
                "issue_1",
                None,
                "issue_context",
                "derived from issue",
                "def $A():",
                "python",
                "src/**/*.py",
                "ast_grep",
                1,
                False,
                None,
            )
        ]
        cursor = _FakeCursor([rows])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = read_semantic_queries(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                workspace_id="ws_123",
            )

        self.assertEqual(result[0].query_ref, "semanticq_db_42")
        self.assertEqual(result[0].source, "issue_context")
        self.assertEqual(result[0].pattern, "def $A():")
        self.assertEqual(result[0].match_count, 1)
        sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
        self.assertIn("from xmustard.semantic_queries", sql_blob)

    def test_read_semantic_matches_maps_rows(self):
        rows = [
            (
                42,
                "ws_123",
                "src/example.py",
                "python",
                1,
                1,
                1,
                19,
                "def render_payload():",
                "def render_payload():",
                ["A"],
                "fixture match",
                80,
            )
        ]
        cursor = _FakeCursor([rows])
        fake_psycopg = types.SimpleNamespace(connect=lambda *args, **kwargs: _FakeConnection(cursor))

        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            result = read_semantic_matches(
                "postgresql://xmustard:secret@localhost:5432/xmustard",
                "xmustard",
                workspace_id="ws_123",
                path="src/example.py",
            )

        self.assertEqual(result[0].query_ref, "semanticq_db_42")
        self.assertEqual(result[0].path, "src/example.py")
        self.assertEqual(result[0].meta_variables, ["A"])
        self.assertEqual(result[0].score, 80)
        sql_blob = "\n".join(item[0] for item in cursor.executed).lower()
        self.assertIn("from xmustard.semantic_matches", sql_blob)
