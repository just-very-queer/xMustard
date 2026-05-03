import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from app import cli as cli_module
from app.models import (
    DiscoverySignal,
    EvidenceRef,
    FileSymbolSummaryMaterializationRecord,
    PathSymbolsResult,
    PostgresSemanticMaterializationResult,
    PostgresWorkspaceSemanticMaterializationResult,
    PostgresBootstrapResult,
    RepoMapSymbolRecord,
    SemanticIndexPlan,
    SemanticIndexStatus,
    SemanticPatternMatchRecord,
    RunRecord,
    RuntimeProbeResult,
    SymbolMaterializationRecord,
    VerificationSummary,
    WorkspaceLoadRequest,
)
from app.service import TrackerService
from app.store import FileStore


LEDGER_TEXT = """# Bugs_25260323

## P0

### P0_25M03_001. Example bug

- Summary: Example summary
- Impact: Example impact
- Evidence:
  - `api/src/example.py:12`
- Status (2026-03-30): fixed in the current branch worktree.
"""


class CliSurfaceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.runner = CliRunner()

    def _create_service(self, tmp_dir: str) -> tuple[TrackerService, str]:
        root = Path(tmp_dir) / "repo"
        (root / "docs" / "bugs").mkdir(parents=True)
        (root / "api" / "src").mkdir(parents=True)
        (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
        (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")
        (root / ".xmustard.yaml").write_text(
            """
description: CLI fixture repo
reviews:
  path_instructions:
    - path: "api/src/**"
      instructions: Check API behavior carefully.
""".strip()
            + "\n",
            encoding="utf-8",
        )

        store = FileStore(Path(tmp_dir) / "data")
        service = TrackerService(store)
        snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
        assert snapshot is not None
        return service, snapshot.workspace.workspace_id

    def test_cli_read_commands_cover_workspace_surfaces(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(cli_module, "service", service):
                checks = [
                    (["health"], lambda payload: self.assertEqual(payload["status"], "ok")),
                    (["capabilities"], lambda payload: self.assertIn("runtimes", payload)),
                    (["snapshot", workspace_id], lambda payload: self.assertEqual(payload["workspace"]["workspace_id"], workspace_id)),
                    (["issues", workspace_id], lambda payload: self.assertEqual(payload[0]["bug_id"], "P0_25M03_001")),
                    (["signals", workspace_id], lambda payload: self.assertIsInstance(payload, list)),
                    (["sources", workspace_id], lambda payload: self.assertGreaterEqual(len(payload), 1)),
                    (["drift", workspace_id], lambda payload: self.assertIsInstance(payload, dict)),
                    (["worktree", workspace_id], lambda payload: self.assertIn("available", payload)),
                    (["issue-context", workspace_id, "P0_25M03_001"], lambda payload: self.assertIn("prompt", payload)),
                    (["issue-drift", workspace_id, "P0_25M03_001"], lambda payload: self.assertEqual(payload["bug_id"], "P0_25M03_001")),
                    (["verifications", workspace_id], lambda payload: self.assertIsInstance(payload, list)),
                    (["activity-overview", workspace_id], lambda payload: self.assertIn("total_events", payload)),
                ]

                for argv, validator in checks:
                    result = self.runner.invoke(cli_module.app, argv)
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    validator(payload)

    def test_cli_repo_tool_commands_cover_first_tranche_surfaces(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            root = Path(service.get_workspace(workspace_id).root_path)
            source_file = root / "api" / "src" / "example.py"
            source_file.write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "package.json").write_text(
                json.dumps({"name": "fixture", "scripts": {"dev": "vite", "test": "vitest run"}}),
                encoding="utf-8",
            )
            (root / "Makefile").write_text("backend:\n\tpython3 -m uvicorn app.main:app\n", encoding="utf-8")

            service.save_verification_profile(
                workspace_id,
                cli_module.VerificationProfileUpsertRequest(
                    profile_id="backend-pytest",
                    name="Backend pytest",
                    test_command="pytest -q",
                ),
            )
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            with patch.object(cli_module, "service", service):
                with patch("app.service.tree_sitter_available", return_value=True):
                    with patch("app.service.shutil.which", return_value="/opt/homebrew/bin/sg"):
                        def fake_go_workspace(action: str, workspace_id_arg: str, flags=None):
                            self.assertEqual(workspace_id_arg, workspace_id)
                            if action == "path-symbols":
                                return {
                                    "workspace_id": workspace_id,
                                    "path": "api/src/example.py",
                                    "symbol_source": "tree_sitter",
                                    "parser_language": "python",
                                    "evidence_source": "rust_semantic_core",
                                    "selection_reason": "Rust semantic core produced on-demand path symbols for the requested file.",
                                    "symbols": [{"path": "api/src/example.py", "symbol": "render_payload", "kind": "function"}],
                                    "warnings": [],
                                }
                            if action == "impact":
                                return {"workspace_id": workspace_id, "derivation_summary": "Rust-backed impact report", "warnings": []}
                            if action == "repo-context":
                                return {"workspace_id": workspace_id, "retrieval_ledger": []}
                            if action == "retrieval-search":
                                return {"workspace_id": workspace_id, "query": "render payload", "hits": [], "retrieval_ledger": []}
                            if action == "semantic-search":
                                return {"workspace_id": workspace_id, "pattern": "def $A():", "match_count": 1, "matches": []}
                            if action == "explain-path":
                                return {
                                    "workspace_id": workspace_id,
                                    "path": "api/src/example.py",
                                    "detected_symbols": ["render_payload"],
                                    "warnings": [],
                                }
                            raise AssertionError(f"unexpected Go workspace action: {action}")

                        checks = [
                            (["repo-state", workspace_id], lambda payload: self.assertEqual(payload["workspace"]["workspace_id"], workspace_id)),
                            (
                                ["ingestion-plan", workspace_id],
                                lambda payload: (
                                    self.assertEqual(payload["next_phase_id"], "tree_sitter_index"),
                                    self.assertIn("ast_grep_rules", payload["ready_phase_ids"]),
                                ),
                            ),
                            (["run-targets", workspace_id], lambda payload: self.assertTrue(any(item["command"] == "npm run dev" for item in payload))),
                            (["verify-targets", workspace_id], lambda payload: self.assertTrue(any(item["command"] == "pytest -q" for item in payload))),
                            (
                                ["path-symbols", workspace_id, "--path", "api/src/example.py"],
                                lambda payload: self.assertIn("symbols", payload),
                            ),
                            (
                                ["changed-symbols", workspace_id],
                                lambda payload: self.assertIsInstance(payload, list),
                            ),
                            (
                                ["changed-since-last-run", workspace_id],
                                lambda payload: self.assertIn("changed_files", payload),
                            ),
                            (
                                ["changed-since-last-accepted-fix", workspace_id],
                                lambda payload: self.assertIn("changed_files", payload),
                            ),
                            (
                                ["impact", workspace_id],
                                lambda payload: self.assertIn("derivation_summary", payload),
                            ),
                            (
                                ["repo-context", workspace_id],
                                lambda payload: self.assertIn("retrieval_ledger", payload),
                            ),
                            (
                                ["retrieval-search", workspace_id, "--query", "render payload"],
                                lambda payload: self.assertIn("retrieval_ledger", payload),
                            ),
                            (
                                ["semantic-search", workspace_id, "--pattern", "def $A():", "--language", "python"],
                                lambda payload: self.assertIn("match_count", payload),
                            ),
                            (
                                ["code-explainer", workspace_id, "--path", "api/src/example.py"],
                                lambda payload: self.assertIn("render_payload", payload["detected_symbols"]),
                            ),
                        ]

                        with patch.object(cli_module, "_run_go_workspace_json", side_effect=fake_go_workspace):
                            for argv, validator in checks:
                                result = self.runner.invoke(cli_module.app, argv)
                                self.assertEqual(result.exit_code, 0, msg=result.output)
                                payload = json.loads(result.stdout)
                                validator(payload)

    def test_cli_path_symbols_reports_tree_sitter_metadata(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            rust_result = PathSymbolsResult(
                workspace_id=workspace_id,
                path="api/src/example.py",
                symbol_source="tree_sitter",
                parser_language="python",
                evidence_source="rust_semantic_core",
                selection_reason="Rust semantic core produced on-demand path symbols for the requested file.",
                symbols=[
                    RepoMapSymbolRecord(path="api/src/example.py", symbol="ApiHandler", kind="class", line_start=1, line_end=3, evidence_source="rust_semantic_core"),
                    RepoMapSymbolRecord(
                        path="api/src/example.py",
                        symbol="render_payload",
                        kind="method",
                        line_start=2,
                        line_end=3,
                        enclosing_scope="ApiHandler",
                        evidence_source="rust_semantic_core",
                    ),
                ],
                file_summary_row=FileSymbolSummaryMaterializationRecord(
                    workspace_id=workspace_id,
                    path="api/src/example.py",
                    language="python",
                    parser_language="python",
                    symbol_source="tree_sitter",
                    symbol_count=2,
                    summary_json={"top_symbols": ["ApiHandler", "render_payload"]},
                ),
                symbol_rows=[
                    SymbolMaterializationRecord(
                        workspace_id=workspace_id,
                        path="api/src/example.py",
                        symbol="ApiHandler",
                        kind="class",
                        language="python",
                        line_start=1,
                        line_end=3,
                    ),
                    SymbolMaterializationRecord(
                        workspace_id=workspace_id,
                        path="api/src/example.py",
                        symbol="render_payload",
                        kind="method",
                        language="python",
                        line_start=2,
                        line_end=3,
                        enclosing_scope="ApiHandler",
                    ),
                ],
            )

            with patch.object(cli_module, "service", service):
                with patch.object(cli_module, "_run_go_workspace_json", return_value=rust_result.model_dump(mode="json")) as go_mock:
                    result = self.runner.invoke(cli_module.app, ["path-symbols", workspace_id, "--path", "api/src/example.py"])

            self.assertEqual(result.exit_code, 0, msg=result.output)
            payload = json.loads(result.stdout)
            go_mock.assert_called_once_with("path-symbols", workspace_id, ["--path", "api/src/example.py"])
            self.assertEqual(payload["symbol_source"], "tree_sitter")
            self.assertEqual(payload["parser_language"], "python")
            self.assertEqual(payload["symbols"][1]["enclosing_scope"], "ApiHandler")
            self.assertEqual(payload["file_summary_row"]["symbol_source"], "tree_sitter")
            self.assertEqual(payload["symbol_rows"][1]["symbol"], "render_payload")

    def test_cli_semantic_search_reports_matches(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            fake_payload = {
                "workspace_id": workspace_id,
                "pattern": "def $A():",
                "language": "python",
                "engine": "ast_grep",
                "binary_path": "/opt/homebrew/bin/sg",
                "match_count": 1,
                "truncated": False,
                "matches": [
                    {
                        "path": "api/src/example.py",
                        "language": "python",
                        "line_start": 1,
                        "line_end": 1,
                        "column_start": 1,
                        "column_end": 19,
                        "matched_text": "def render_payload():",
                        "context_lines": "def render_payload():",
                        "meta_variables": ["A"],
                    }
                ],
                "query_row": {"query_ref": "semanticq_test", "source": "adhoc_tool"},
                "match_rows": [{"query_ref": "semanticq_test"}],
            }

            with patch.object(cli_module, "service", service):
                with patch.object(cli_module, "_run_go_workspace_json", return_value=fake_payload) as go_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        ["semantic-search", workspace_id, "--pattern", "def $A():", "--language", "python"],
                    )

            self.assertEqual(result.exit_code, 0, msg=result.output)
            payload = json.loads(result.stdout)
            go_mock.assert_called_once_with(
                "semantic-search",
                workspace_id,
                ["--pattern", "def $A():", "--limit", "50", "--language", "python"],
            )
            self.assertEqual(payload["engine"], "ast_grep")
            self.assertEqual(payload["match_count"], 1)
            self.assertEqual(payload["matches"][0]["meta_variables"], ["A"])
            self.assertEqual(payload["query_row"]["source"], "adhoc_tool")
            self.assertEqual(payload["match_rows"][0]["query_ref"], payload["query_row"]["query_ref"])

    def test_cli_postgres_materialization_commands(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(cli_module, "service", service):
                with patch.object(cli_module, "_run_go_workspace_json") as path_materialize_mock:
                    path_materialize_mock.return_value = {
                        "applied": True,
                        "dsn_redacted": "postgresql://xmustard:***@localhost:5432/xmustard",
                        "schema_name": "agent_context",
                        "workspace_id": workspace_id,
                        "source": "path_symbols",
                        "target": "api/src/example.py",
                        "materialized_paths": ["api/src/example.py"],
                        "file_rows": 1,
                        "symbol_rows": 1,
                        "summary_rows": 1,
                        "message": "ok",
                    }
                    result = self.runner.invoke(
                        cli_module.app,
                        ["postgres-materialize-path", workspace_id, "--path", "api/src/example.py"],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertTrue(payload["applied"])
                    path_materialize_mock.assert_called_once_with(
                        "postgres-materialize-path",
                        workspace_id,
                        ["--path", "api/src/example.py"],
                    )

                with patch.object(cli_module, "_run_go_workspace_json") as workspace_materialize_mock:
                    workspace_materialize_mock.return_value = {
                        "applied": True,
                        "dsn_redacted": "postgresql://xmustard:***@localhost:5432/xmustard",
                        "schema_name": "agent_context",
                        "workspace_id": workspace_id,
                        "strategy": "paths",
                        "requested_paths": ["api/src/example.py"],
                        "materialized_paths": ["api/src/example.py"],
                        "file_rows": 1,
                        "symbol_rows": 1,
                        "summary_rows": 1,
                        "message": "ok",
                    }
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "postgres-materialize-workspace-symbols",
                            workspace_id,
                            "--strategy",
                            "paths",
                            "--path",
                            "api/src/example.py",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertTrue(payload["applied"])
                    workspace_materialize_mock.assert_called_once_with(
                        "postgres-materialize-workspace-symbols",
                        workspace_id,
                        ["--strategy", "paths", "--limit", "12", "--select-path", "api/src/example.py"],
                    )

                with patch.object(cli_module, "_run_go_semantic_index") as semantic_plan_mock:
                    semantic_plan_mock.return_value = {
                        "workspace_id": workspace_id,
                        "root_path": str(Path(tmp_dir) / "repo"),
                        "surface": "cli",
                        "strategy": "paths",
                        "requested_paths": ["api/src/example.py"],
                        "selected_paths": ["api/src/example.py"],
                        "postgres_configured": True,
                        "postgres_schema": "agent_context",
                        "tree_sitter_available": False,
                        "ast_grep_available": False,
                        "run_target_count": 1,
                        "verify_target_count": 1,
                        "warnings": ["ast-grep binary is unavailable"],
                        "can_run": True,
                    }
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "semantic-index",
                            "plan",
                            workspace_id,
                            "--surface",
                            "cli",
                            "--strategy",
                            "paths",
                            "--path",
                            "api/src/example.py",
                            "--dsn",
                            "postgresql://xmustard:secret@localhost:5432/xmustard",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertEqual(payload["surface"], "cli")
                    semantic_plan_mock.assert_called_once_with(
                        "plan",
                        workspace_id,
                        surface="cli",
                        strategy="paths",
                        paths=["api/src/example.py"],
                        limit=12,
                        dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                    )

                with patch.object(cli_module, "_run_go_semantic_index") as semantic_status_mock:
                    semantic_status_mock.return_value = {
                        "workspace_id": workspace_id,
                        "surface": "cli",
                        "status": "fresh",
                        "postgres_configured": True,
                        "postgres_schema": "agent_context",
                        "current_fingerprint": "fp_cli",
                        "fingerprint_match": True,
                    }
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "semantic-index",
                            "status",
                            workspace_id,
                            "--surface",
                            "cli",
                            "--strategy",
                            "paths",
                            "--path",
                            "api/src/example.py",
                            "--dsn",
                            "postgresql://xmustard:secret@localhost:5432/xmustard",
                            "--schema",
                            "agent_context",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertEqual(payload["status"], "fresh")
                    self.assertTrue(payload["fingerprint_match"])
                    semantic_status_mock.assert_called_once_with(
                        "status",
                        workspace_id,
                        surface="cli",
                        strategy="paths",
                        paths=["api/src/example.py"],
                        limit=12,
                        dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                        schema="agent_context",
                    )

                with patch.object(cli_module, "_run_go_workspace_json") as search_materialize_mock:
                    search_materialize_mock.return_value = {
                        "applied": True,
                        "dsn_redacted": "postgresql://xmustard:***@localhost:5432/xmustard",
                        "schema_name": "agent_context",
                        "workspace_id": workspace_id,
                        "source": "semantic_search",
                        "target": "def $A():",
                        "materialized_paths": ["api/src/example.py"],
                        "file_rows": 1,
                        "query_rows": 1,
                        "match_rows": 1,
                        "message": "ok",
                    }
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "postgres-materialize-semantic-search",
                            workspace_id,
                            "--pattern",
                            "def $A():",
                            "--language",
                            "python",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertTrue(payload["applied"])
                    search_materialize_mock.assert_called_once_with(
                        "postgres-materialize-semantic-search",
                        workspace_id,
                        ["--pattern", "def $A():", "--limit", "50", "--language", "python"],
                    )

    def test_cli_postgres_commands_cover_plan_render_and_bootstrap(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, _workspace_id = self._create_service(tmp_dir)
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            with patch.object(cli_module, "service", service):
                with patch.object(cli_module, "_run_go_postgres_json") as plan_mock:
                    plan_mock.return_value = {
                        "configured": True,
                        "schema_name": "agent_context",
                        "table_names": ["workspaces"],
                        "semantic_table_names": ["semantic_matches"],
                        "ops_memory_table_names": ["run_plans"],
                    }
                    result = self.runner.invoke(cli_module.app, ["postgres-plan"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    plan = json.loads(result.stdout)
                    self.assertTrue(plan["configured"])
                    self.assertEqual(plan["schema_name"], "agent_context")
                    self.assertIn("workspaces", plan["table_names"])
                    self.assertIn("semantic_matches", plan["semantic_table_names"])
                    self.assertIn("run_plans", plan["ops_memory_table_names"])
                    plan_mock.assert_called_once_with("plan")

                with patch.object(cli_module, "_run_go_postgres_text", return_value="create schema if not exists agent_context;\n") as render_mock:
                    result = self.runner.invoke(cli_module.app, ["postgres-render"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertIn("create schema if not exists agent_context;", result.stdout.lower())
                    render_mock.assert_called_once_with("render", [])

                with patch.object(cli_module, "_run_go_postgres_json") as bootstrap_mock:
                    bootstrap_mock.return_value = {
                        "applied": True,
                        "dsn_redacted": "postgresql://xmustard:***@localhost:5432/xmustard",
                        "schema_name": "agent_context",
                        "sql_path": "/tmp/schema.sql",
                        "statement_count": 12,
                        "message": "Applied repo cockpit foundation schema to Postgres schema 'agent_context'.",
                    }
                    result = self.runner.invoke(cli_module.app, ["postgres-bootstrap"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertTrue(payload["applied"])
                    bootstrap_mock.assert_called_once_with("bootstrap", [])

    def test_cli_agent_and_run_commands_cover_main_integration_flow(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            run = RunRecord(
                run_id="run_test_cli",
                workspace_id=workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(service.store.runs_dir(workspace_id) / "run_test_cli.log"),
                output_path=str(service.store.runs_dir(workspace_id) / "run_test_cli.out.json"),
            )
            service.store.save_run(run)
            Path(run.log_path).write_text("line one\nline two\n", encoding="utf-8")

            probe = RuntimeProbeResult(
                runtime="codex",
                model="gpt-5.4-mini",
                ok=True,
                available=True,
                binary_path="/opt/homebrew/bin/codex",
                command_preview="codex exec",
                duration_ms=123,
                exit_code=0,
                output_excerpt='{"status":"ok"}',
            )

            with patch.object(cli_module, "service", service):
                with patch.object(service, "probe_runtime", return_value=probe.model_dump(mode="json")):
                    result = self.runner.invoke(
                        cli_module.app,
                        ["agent-probe", workspace_id, "--runtime", "codex", "--model", "gpt-5.4-mini"],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertTrue(json.loads(result.stdout)["ok"])

                with patch.object(service, "start_agent_query", return_value=run.model_dump(mode="json")):
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "agent-query",
                            workspace_id,
                            "--runtime",
                            "codex",
                            "--model",
                            "gpt-5.4-mini",
                            "--prompt",
                            "Summarize the repo",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["run_id"], "run_test_cli")

                with patch.object(service, "start_issue_run", return_value=run.model_dump(mode="json")):
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "run-start",
                            workspace_id,
                            "P0_25M03_001",
                            "--runtime",
                            "codex",
                            "--model",
                            "gpt-5.4-mini",
                            "--instruction",
                            "validate first",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(cli_module.app, ["run-get", workspace_id, "run_test_cli"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["run_id"], "run_test_cli")

                result = self.runner.invoke(cli_module.app, ["run-log", workspace_id, "run_test_cli"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertIn("line one", json.loads(result.stdout)["content"])

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "run-review",
                        workspace_id,
                        "run_test_cli",
                        "--disposition",
                        "investigation_only",
                        "--notes",
                        "Used for analysis only",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                reviewed = json.loads(result.stdout)
                self.assertEqual(reviewed["run_id"], "run_test_cli")
                self.assertEqual(reviewed["disposition"], "investigation_only")

    def test_cli_run_wait_and_smoke_cover_terminal_workflow(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            query_run = RunRecord(
                run_id="run_query_cli",
                workspace_id=workspace_id,
                issue_id="workspace-query",
                runtime="codex",
                model="gpt-5.4-mini",
                status="queued",
                title="codex:workspace-query",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(service.store.runs_dir(workspace_id) / "run_query_cli.log"),
                output_path=str(service.store.runs_dir(workspace_id) / "run_query_cli.out.json"),
            )
            issue_run = query_run.model_copy(update={"run_id": "run_issue_cli", "issue_id": "P0_25M03_001"})
            Path(query_run.log_path).write_text("", encoding="utf-8")
            Path(issue_run.log_path).write_text("", encoding="utf-8")

            probe = RuntimeProbeResult(
                runtime="codex",
                model="gpt-5.4-mini",
                ok=True,
                available=True,
                binary_path="/opt/homebrew/bin/codex",
                command_preview="codex exec",
                duration_ms=100,
                exit_code=0,
            )

            run_wait_states = [
                {"run_id": "run_query_cli", "status": "queued", "runtime": "codex", "model": "gpt-5.4-mini"},
                {"run_id": "run_query_cli", "status": "completed", "runtime": "codex", "model": "gpt-5.4-mini"},
            ]

            with patch.object(cli_module, "service", service):
                with patch.object(service, "get_run", side_effect=run_wait_states):
                    with patch("app.cli.time.sleep", return_value=None):
                        result = self.runner.invoke(
                            cli_module.app,
                            ["run-wait", workspace_id, "run_query_cli", "--timeout-seconds", "2", "--poll-interval", "0.01"],
                        )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["status"], "completed")

                with patch.object(service, "probe_runtime", return_value=probe.model_dump(mode="json")):
                    with patch.object(service, "start_agent_query", return_value=query_run.model_dump(mode="json")):
                        with patch.object(service, "start_issue_run", return_value=issue_run.model_dump(mode="json")):
                            with patch.object(
                                service,
                                "get_run",
                                side_effect=[
                                    query_run.model_dump(mode="json"),
                                    issue_run.model_dump(mode="json"),
                                ],
                            ):
                                with patch.object(
                                    service,
                                    "read_run_log",
                                    side_effect=[
                                        {"offset": 0, "content": "", "eof": False},
                                        {"offset": 0, "content": "", "eof": False},
                                    ],
                                ):
                                    with patch.object(
                                        service,
                                        "cancel_run",
                                        side_effect=[
                                            query_run.model_dump(mode="json") | {"status": "cancelled"},
                                            issue_run.model_dump(mode="json") | {"status": "cancelled"},
                                        ],
                                    ):
                                        with patch("app.cli.time.sleep", return_value=None):
                                            result = self.runner.invoke(
                                                cli_module.app,
                                                [
                                                    "smoke",
                                                    workspace_id,
                                                    "P0_25M03_001",
                                                    "--runtime",
                                                    "codex",
                                                    "--model",
                                                    "gpt-5.4-mini",
                                                    "--settle-seconds",
                                                    "0",
                                                ],
                                            )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                payload = json.loads(result.stdout)
                self.assertTrue(payload["probe"]["ok"])
                self.assertEqual(payload["query_run"]["status"], "cancelled")
                self.assertEqual(payload["issue_run"]["status"], "cancelled")

    def test_cli_view_lifecycle_and_issue_update(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(cli_module, "service", service):
                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "view-save",
                        workspace_id,
                        "Verification queue",
                        "--issue-status",
                        "verification",
                        "--label",
                        "tracking",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                created = json.loads(result.stdout)

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "view-update",
                        workspace_id,
                        created["view_id"],
                        "--name",
                        "Verification queue updated",
                        "--issue-status",
                        "resolved",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                updated = json.loads(result.stdout)
                self.assertEqual(updated["name"], "Verification queue updated")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "issue-update",
                        workspace_id,
                        "P0_25M03_001",
                        "--issue-status",
                        "verification",
                        "--labels",
                        "tracking,ops",
                        "--notes",
                        "checked from cli",
                        "--needs-followup",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                issue = json.loads(result.stdout)
                self.assertEqual(issue["issue_status"], "verification")
                self.assertTrue(issue["needs_followup"])

                result = self.runner.invoke(cli_module.app, ["view-delete", workspace_id, created["view_id"]])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(json.loads(result.stdout)["ok"])

    def test_cli_tracker_issue_and_fix_commands(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            run = RunRecord(
                run_id="run_fix_cli",
                workspace_id=workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(service.store.runs_dir(workspace_id) / "run_fix_cli.log"),
                output_path=str(service.store.runs_dir(workspace_id) / "run_fix_cli.out.json"),
                summary={"session_id": "ses_fix_cli", "text_excerpt": "Applied scope fix"},
            )
            service.store.save_run(run)
            review_run = run.model_copy(
                update={
                    "run_id": "run_review_cli",
                    "log_path": str(service.store.runs_dir(workspace_id) / "run_review_cli.log"),
                    "output_path": str(service.store.runs_dir(workspace_id) / "run_review_cli.out.json"),
                    "summary": {"session_id": "ses_review_cli", "text_excerpt": "Verified scope in api/src/example.py\npytest test_kb_router.py -q"},
                }
            )
            service.store.save_run(review_run)

            with patch.object(cli_module, "service", service):
                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "runbook-save",
                        workspace_id,
                        "--name",
                        "Focused verify",
                        "--template",
                        "1. Reproduce the bug.\\n2. Report scope only.",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                saved_runbook = json.loads(result.stdout)
                self.assertEqual(saved_runbook["runbook_id"], "focused-verify")

                result = self.runner.invoke(cli_module.app, ["runbooks", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                runbooks = json.loads(result.stdout)
                self.assertTrue(any(item["runbook_id"] == "fix" for item in runbooks))
                self.assertTrue(any(item["runbook_id"] == "focused-verify" for item in runbooks))

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "issue-create",
                        workspace_id,
                        "--title",
                        "Tracker issue",
                        "--severity",
                        "P2",
                        "--summary",
                        "Created from cli",
                        "--labels",
                        "tracker,cli",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                created_issue = json.loads(result.stdout)
                self.assertEqual(created_issue["source"], "tracker")

                result = self.runner.invoke(cli_module.app, ["issues", workspace_id, "--review-ready-only"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                review_queue = json.loads(result.stdout)
                self.assertEqual(review_queue[0]["review_ready_count"], 2)

                result = self.runner.invoke(cli_module.app, ["review-queue", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                review_candidates = json.loads(result.stdout)
                self.assertEqual({item["run"]["run_id"] for item in review_candidates}, {"run_fix_cli", "run_review_cli"})

                result = self.runner.invoke(
                    cli_module.app,
                    ["issue-work", workspace_id, "P0_25M03_001", "--runbook-id", "focused-verify"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                issue_work = json.loads(result.stdout)
                self.assertEqual(issue_work["runbook"], ["Reproduce the bug.", "Report scope only."])

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "issue-update",
                        workspace_id,
                        "P0_25M03_001",
                        "--severity",
                        "P2",
                        "--issue-status",
                        "triaged",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "issue-update",
                        workspace_id,
                        "P0_25M03_001",
                        "--severity",
                        "P1",
                        "--issue-status",
                        "investigating",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "fix-record",
                        workspace_id,
                        "P0_25M03_001",
                        "--summary",
                        "Scoped KB term mutations",
                        "--run-id",
                        "run_fix_cli",
                        "--changed-files",
                        "api/src/example.py",
                        "--tests-run",
                        "pytest test_kb_router.py -q",
                        "--issue-status",
                        "verification",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                created_fix = json.loads(result.stdout)
                self.assertEqual(created_fix["run_id"], "run_fix_cli")
                self.assertEqual(created_fix["actor"]["kind"], "agent")

                result = self.runner.invoke(cli_module.app, ["fixes", workspace_id, "--issue-id", "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                fixes = json.loads(result.stdout)
                self.assertEqual(fixes[0]["fix_id"], created_fix["fix_id"])

                result = self.runner.invoke(
                    cli_module.app,
                    ["fix-draft", workspace_id, "P0_25M03_001", "--run-id", "run_fix_cli"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                suggested = json.loads(result.stdout)
                self.assertEqual(suggested["run_id"], "run_fix_cli")
                self.assertEqual(suggested["suggested_issue_status"], "verification")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "review-accept",
                        workspace_id,
                        "run_review_cli",
                        "--issue-status",
                        "verification",
                        "--notes",
                        "accepted from cli",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                accepted_fix = json.loads(result.stdout)
                self.assertEqual(accepted_fix["run_id"], "run_review_cli")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "fix-update",
                        workspace_id,
                        created_fix["fix_id"],
                        "--status",
                        "verified",
                        "--notes",
                        "reviewed",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                updated_fix = json.loads(result.stdout)
                self.assertEqual(updated_fix["status"], "verified")

                result = self.runner.invoke(cli_module.app, ["issue-context", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                context_payload = json.loads(result.stdout)
                self.assertEqual(context_payload["issue"]["severity"], "P1")
                self.assertTrue(context_payload["recent_activity"])
                severity_activity = next(
                    item
                    for item in context_payload["recent_activity"]
                    if item["details"].get("before_after", {}).get("severity")
                )
                severity_change = severity_activity["details"]["before_after"]["severity"]
                self.assertEqual(severity_change["from"], "P2")
                self.assertEqual(severity_change["to"], "P1")

    def test_cli_verify_bug_uses_three_pass_summary(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            summary = VerificationSummary(
                workspace_id=workspace_id,
                issue_id="P0_25M03_001",
                checked_yes=2,
                checked_no=1,
                fixed_yes=2,
                fixed_no=1,
                consensus_code_checked="yes",
                consensus_fixed="yes",
            )

            with patch.object(cli_module, "service", service):
                with patch.object(service, "verify_issue_three_pass", return_value=summary) as verify_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "verify-bug",
                            workspace_id,
                            "P0_25M03_001",
                            "--runtime",
                            "opencode",
                            "--models",
                            "opencode-go/minimax-m2.7,opencode-go/glm-5,opencode-go/kimi-k2.5",
                            "--timeout-seconds",
                            "5",
                            "--poll-interval",
                            "0.5",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    payload = json.loads(result.stdout)
                    self.assertEqual(payload["consensus_code_checked"], "yes")
                    self.assertEqual(payload["consensus_fixed"], "yes")
                    request = verify_mock.call_args.args[2]
                    self.assertEqual(request.runtime, "opencode")
                    self.assertEqual(
                        request.models,
                        ["opencode-go/minimax-m2.7", "opencode-go/glm-5", "opencode-go/kimi-k2.5"],
                    )

    def test_cli_analysis_and_signal_commands(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            service.create_issue(
                workspace_id,
                cli_module.IssueCreateRequest(
                    bug_id="P0_25M03_002",
                    title="Example bug follow-up",
                    severity="P0",
                    summary="Example summary with follow-up wording for duplicate detection coverage.",
                    impact="Example impact still breaks behavior.",
                    labels=["cli", "duplicate"],
                ),
            )
            snapshot = service.read_snapshot(workspace_id)
            assert snapshot is not None
            signal = DiscoverySignal(
                signal_id="sig_cli_001",
                kind="not_implemented",
                severity="P2",
                title="Signal promoted issue",
                summary="Scanner discovered a follow-up investigation site.",
                file_path="api/src/example.py",
                line=12,
                evidence=[EvidenceRef(path="api/src/example.py", line=12, excerpt="print('ok')")],
                tags=["scanner", "cli"],
                fingerprint="fp_sig_cli_001",
            )
            service.store.save_snapshot(snapshot.model_copy(update={"signals": [*snapshot.signals, signal]}))

            with patch.object(cli_module, "service", service):
                result = self.runner.invoke(cli_module.app, ["quality-score", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                quality = json.loads(result.stdout)
                self.assertEqual(quality["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(cli_module.app, ["quality-get", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(cli_module.app, ["quality-score-all", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertGreaterEqual(len(json.loads(result.stdout)), 2)

                result = self.runner.invoke(cli_module.app, ["duplicates", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                duplicates = json.loads(result.stdout)
                self.assertTrue(any(item["target_id"] == "P0_25M03_002" for item in duplicates))

                result = self.runner.invoke(cli_module.app, ["triage-issue", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(cli_module.app, ["triage-all", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                triage_all = json.loads(result.stdout)
                self.assertTrue(any(item["issue_id"] == "P0_25M03_001" for item in triage_all))

                result = self.runner.invoke(cli_module.app, ["test-suggestions-generate", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                generated = json.loads(result.stdout)
                self.assertTrue(generated)
                self.assertEqual(generated[0]["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(cli_module.app, ["test-suggestions", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(json.loads(result.stdout))

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "signal-promote",
                        workspace_id,
                        "sig_cli_001",
                        "--severity",
                        "P1",
                        "--labels",
                        "cli,promoted",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                promoted_snapshot = json.loads(result.stdout)
                promoted_issue = next(item for item in promoted_snapshot["issues"] if item["title"] == "Signal promoted issue")
                self.assertEqual(promoted_issue["issue_status"], "triaged")
                promoted_signal = next(item for item in promoted_snapshot["signals"] if item["signal_id"] == "sig_cli_001")
                self.assertEqual(promoted_signal["promoted_bug_id"], promoted_issue["bug_id"])

    def test_cli_integration_commands(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(cli_module, "service", service):
                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "integration-configure",
                        workspace_id,
                        "github",
                        "--setting",
                        "repo=owner/example",
                        "--setting",
                        "token=abc123",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                config = json.loads(result.stdout)
                self.assertEqual(config["provider"], "github")
                self.assertEqual(config["settings"]["repo"], "owner/example")

                result = self.runner.invoke(cli_module.app, ["integrations", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                integrations = json.loads(result.stdout)
                self.assertTrue(any(item["provider"] == "github" for item in integrations))

                with patch.object(service, "test_integration", return_value={"provider": "github", "ok": True, "message": "ok"}) as test_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "integration-test",
                            "github",
                            "--settings-json",
                            '{"token":"abc123","repo":"owner/example"}',
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertTrue(json.loads(result.stdout)["ok"])
                    request = test_mock.call_args.args[0]
                    self.assertEqual(request.provider, "github")
                    self.assertEqual(request.settings["repo"], "owner/example")

                with patch.object(service, "import_github_issues", return_value=[{"issue_id": "GH-1"}]):
                    result = self.runner.invoke(cli_module.app, ["github-import", workspace_id, "owner/example"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)[0]["issue_id"], "GH-1")

                with patch.object(service, "create_github_pr", return_value={"pr_number": 17, "html_url": "https://example.test/pr/17"}) as pr_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "github-pr",
                            workspace_id,
                            "run_cli_001",
                            "P0_25M03_001",
                            "feature/cli-flow",
                            "--base-branch",
                            "main",
                            "--title",
                            "CLI PR",
                            "--body",
                            "Ready for review",
                            "--draft",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["pr_number"], 17)
                    request = pr_mock.call_args.args[1]
                    self.assertEqual(request.head_branch, "feature/cli-flow")
                    self.assertTrue(request.draft)

                with patch.object(service, "send_slack_notification", return_value={"status": "sent", "event": "run.completed"}):
                    result = self.runner.invoke(
                        cli_module.app,
                        ["slack-notify", workspace_id, "run.completed", "--message", "CLI finished"],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["status"], "sent")

                with patch.object(service, "sync_issue_to_linear", return_value={"issue_id": "P0_25M03_001", "linear_id": "LIN-1"}):
                    result = self.runner.invoke(cli_module.app, ["linear-sync", workspace_id, "P0_25M03_001"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["linear_id"], "LIN-1")

                with patch.object(service, "sync_issue_to_jira", return_value={"issue_id": "P0_25M03_001", "jira_key": "JIRA-1"}):
                    result = self.runner.invoke(cli_module.app, ["jira-sync", workspace_id, "P0_25M03_001"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["jira_key"], "JIRA-1")

    def test_cli_extended_parity_commands(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(cli_module, "service", service):
                result = self.runner.invoke(cli_module.app, ["workspaces"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(any(item["workspace_id"] == workspace_id for item in json.loads(result.stdout)))

                result = self.runner.invoke(cli_module.app, ["tree", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(any(item["path"] == "api" for item in json.loads(result.stdout)))

                result = self.runner.invoke(cli_module.app, ["repo-map", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["workspace_id"], workspace_id)

                result = self.runner.invoke(cli_module.app, ["guidance-health", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["workspace_id"], workspace_id)

                result = self.runner.invoke(cli_module.app, ["repo-config", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["source_path"], ".xmustard.yaml")

                result = self.runner.invoke(cli_module.app, ["repo-config-health", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["status"], "configured")

                result = self.runner.invoke(
                    cli_module.app,
                    ["guidance-generate", workspace_id, "--template-id", "agents"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout)["path"], "AGENTS.md")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "verification-profile-save",
                        workspace_id,
                        "--name",
                        "CLI Verify",
                        "--test-command",
                        "pytest -q",
                        "--coverage-command",
                        "pytest --cov=. --cov-report=xml",
                        "--coverage-report-path",
                        "coverage.xml",
                        "--coverage-format",
                        "cobertura",
                        "--checklist-items",
                        "Coverage artifact is produced,Regression command passes",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                profile = json.loads(result.stdout)
                self.assertEqual(profile["name"], "CLI Verify")
                self.assertEqual(profile["checklist_items"], ["Coverage artifact is produced", "Regression command passes"])

                result = self.runner.invoke(cli_module.app, ["verification-profiles", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(any(item["profile_id"] == profile["profile_id"] for item in json.loads(result.stdout)))

                result = self.runner.invoke(
                    cli_module.app,
                    ["verification-profile-history", workspace_id, "--profile-id", profile["profile_id"]],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertEqual(json.loads(result.stdout), [])

                result = self.runner.invoke(cli_module.app, ["verification-profile-reports", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                reports_payload = json.loads(result.stdout)
                self.assertTrue(any(item["profile_id"] == profile["profile_id"] for item in reports_payload))

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "ticket-context-save",
                        workspace_id,
                        "P0_25M03_001",
                        "--title",
                        "Browser bug ticket",
                        "--acceptance-criteria",
                        "render works,error banner gone",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                context_payload = json.loads(result.stdout)
                self.assertEqual(context_payload["title"], "Browser bug ticket")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "threat-model-save",
                        workspace_id,
                        "P0_25M03_001",
                        "--title",
                        "Checkout threat review",
                        "--assets",
                        "payment data,cart state",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                threat_payload = json.loads(result.stdout)
                self.assertEqual(threat_payload["title"], "Checkout threat review")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "vulnerability-finding-save",
                        workspace_id,
                        "P0_25M03_001",
                        "--title",
                        "Checkout authz bypass",
                        "--scanner",
                        "semgrep",
                        "--source",
                        "semgrep-json",
                        "--severity",
                        "high",
                        "--status",
                        "triaged",
                        "--summary",
                        "Checkout export flow lacks tenant authorization.",
                        "--rule-id",
                        "python.checkout.authz",
                        "--location-path",
                        "api/src/example.py",
                        "--location-line",
                        "12",
                        "--cwe-id",
                        "CWE-639",
                        "--evidence",
                        "Semgrep matched a missing tenant guard.",
                        "--threat-model-id",
                        threat_payload["threat_model_id"],
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                vulnerability_payload = json.loads(result.stdout)
                self.assertEqual(vulnerability_payload["title"], "Checkout authz bypass")
                self.assertEqual(vulnerability_payload["threat_model_ids"], [threat_payload["threat_model_id"]])

                result = self.runner.invoke(cli_module.app, ["vulnerability-findings", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                findings_payload = json.loads(result.stdout)
                self.assertTrue(any(item["finding_id"] == vulnerability_payload["finding_id"] for item in findings_payload))

                result = self.runner.invoke(cli_module.app, ["vulnerability-report", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                report_payload = json.loads(result.stdout)
                self.assertEqual(report_payload["total_findings"], 1)
                self.assertEqual(report_payload["linked_threat_models"][0]["threat_model_id"], threat_payload["threat_model_id"])

                result = self.runner.invoke(
                    cli_module.app,
                    ["vulnerability-report", workspace_id, "P0_25M03_001", "--format", "markdown"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertIn("# Vulnerability Report", result.stdout)
                self.assertIn("Checkout threat review", result.stdout)

                result = self.runner.invoke(cli_module.app, ["workspace-vulnerability-report", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                workspace_report_payload = json.loads(result.stdout)
                self.assertEqual(workspace_report_payload["total_findings"], 1)
                self.assertEqual(workspace_report_payload["issue_rollups"][0]["issue_id"], "P0_25M03_001")

                result = self.runner.invoke(
                    cli_module.app,
                    ["workspace-vulnerability-report", workspace_id, "--format", "markdown"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertIn("# Workspace Vulnerability Report", result.stdout)

                result = self.runner.invoke(cli_module.app, ["workspace-security-review-bundle", workspace_id])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                security_bundle_payload = json.loads(result.stdout)
                self.assertEqual(security_bundle_payload["total_findings"], 1)
                self.assertEqual(security_bundle_payload["top_findings"][0]["title"], "Checkout authz bypass")

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "vulnerability-findings-import",
                        workspace_id,
                        "P0_25M03_001",
                        "--source",
                        "sarif",
                        "--payload",
                        json.dumps(
                            {
                                "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
                                "runs": [
                                    {
                                        "tool": {"driver": {"name": "CodeQL", "rules": [{"id": "py/path-injection"}]}},
                                        "results": [
                                            {
                                                "ruleId": "py/path-injection",
                                                "level": "error",
                                                "message": {"text": "User-controlled path reaches file open."},
                                                "locations": [
                                                    {
                                                        "physicalLocation": {
                                                            "artifactLocation": {"uri": "api/src/example.py"},
                                                            "region": {"startLine": 12},
                                                        }
                                                    }
                                                ],
                                            }
                                        ],
                                    }
                                ],
                            }
                        ),
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                imported_payload = json.loads(result.stdout)
                self.assertEqual(len(imported_payload), 1)

                result = self.runner.invoke(cli_module.app, ["vulnerability-import-batches", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                batch_payload = json.loads(result.stdout)
                self.assertEqual(len(batch_payload), 1)
                self.assertEqual(batch_payload[0]["source"], "sarif")
                self.assertEqual(batch_payload[0]["scanner"], "CodeQL")
                self.assertEqual(batch_payload[0]["summary_counts"]["new"], 1)
                self.assertEqual(batch_payload[0]["finding_ids"], [imported_payload[0]["finding_id"]])

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "vulnerability-findings-import",
                        workspace_id,
                        "P0_25M03_001",
                        "--source",
                        "sarif",
                        "--payload",
                        json.dumps(
                            {
                                "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
                                "runs": [
                                    {
                                        "tool": {"driver": {"name": "CodeQL", "rules": [{"id": "py/path-injection"}]}},
                                        "results": [
                                            {
                                                "ruleId": "py/path-injection",
                                                "level": "error",
                                                "message": {"text": "User-controlled path reaches file open."},
                                                "locations": [
                                                    {
                                                        "physicalLocation": {
                                                            "artifactLocation": {"uri": "api/src/example.py"},
                                                            "region": {"startLine": 12},
                                                        }
                                                    }
                                                ],
                                            }
                                        ],
                                    }
                                ],
                            }
                        ),
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)

                result = self.runner.invoke(cli_module.app, ["vulnerability-import-batches", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                repeated_batch_payload = json.loads(result.stdout)
                self.assertEqual(len(repeated_batch_payload), 2)
                self.assertEqual(repeated_batch_payload[0]["summary_counts"]["new"], 0)
                self.assertEqual(repeated_batch_payload[0]["summary_counts"]["existing"], 1)

                result = self.runner.invoke(
                    cli_module.app,
                    ["workspace-security-review-bundle", workspace_id, "--format", "markdown"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertIn("# Workspace Security Review Bundle", result.stdout)

                result = self.runner.invoke(
                    cli_module.app,
                    ["context-replay-capture", workspace_id, "P0_25M03_001", "--label", "cli replay"],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                replay_payload = json.loads(result.stdout)
                self.assertEqual(replay_payload["label"], "cli replay")

                result = self.runner.invoke(
                    cli_module.app,
                    ["context-replay-compare", workspace_id, "P0_25M03_001", replay_payload["replay_id"]],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                comparison_payload = json.loads(result.stdout)
                self.assertEqual(comparison_payload["replay"]["replay_id"], replay_payload["replay_id"])

                result = self.runner.invoke(
                    cli_module.app,
                    [
                        "browser-dump-save",
                        workspace_id,
                        "P0_25M03_001",
                        "--label",
                        "Checkout dump",
                        "--page-url",
                        "http://localhost:3000/checkout",
                        "--summary",
                        "UI is stuck after submit",
                        "--dom-snapshot",
                        "checkout error visible",
                        "--console-message",
                        "TypeError: boom",
                        "--network-request",
                        "POST /api/checkout -> 500",
                    ],
                )
                self.assertEqual(result.exit_code, 0, msg=result.output)
                browser_dump = json.loads(result.stdout)
                self.assertEqual(browser_dump["label"], "Checkout dump")

                result = self.runner.invoke(cli_module.app, ["browser-dumps", workspace_id, "P0_25M03_001"])
                self.assertEqual(result.exit_code, 0, msg=result.output)
                self.assertTrue(any(item["dump_id"] == browser_dump["dump_id"] for item in json.loads(result.stdout)))

                with patch.object(service, "generate_run_plan", return_value={"plan_id": "plan_1", "phase": "awaiting_approval"}):
                    result = self.runner.invoke(cli_module.app, ["plan-generate", workspace_id, "run_123"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["phase"], "awaiting_approval")

                with patch.object(service, "approve_run_plan", return_value={"plan_id": "plan_1", "phase": "approved"}) as approve_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        ["plan-approve", workspace_id, "run_123", "--feedback", "ship it"],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["phase"], "approved")
                    self.assertEqual(approve_mock.call_args.args[2].feedback, "ship it")

                with patch.object(service, "update_run_plan_tracking", return_value={"plan_id": "plan_1", "version": 2}) as track_mock:
                    result = self.runner.invoke(
                        cli_module.app,
                        [
                            "plan-track",
                            workspace_id,
                            "run_123",
                            "--ownership-mode",
                            "shared",
                            "--owner-label",
                            "mustard team",
                            "--attached-file",
                            "docs/plan-note.md",
                            "--feedback",
                            "claimed",
                        ],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["version"], 2)
                    tracking_request = track_mock.call_args.args[2]
                    self.assertEqual(tracking_request.ownership_mode, "shared")
                    self.assertEqual(tracking_request.owner_label, "mustard team")
                    self.assertEqual(tracking_request.attached_files, ["docs/plan-note.md"])

                with patch.object(service, "parse_coverage_report", return_value={"line_coverage": 91.2, "workspace_id": workspace_id}):
                    result = self.runner.invoke(
                        cli_module.app,
                        ["coverage-parse", workspace_id, "--report-path", "coverage.xml", "--issue-id", "P0_25M03_001"],
                    )
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["line_coverage"], 91.2)

                with patch.object(service, "get_patch_critique", return_value={"overall_quality": "good"}):
                    result = self.runner.invoke(cli_module.app, ["critique-get", workspace_id, "run_123"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["overall_quality"], "good")

                with patch.object(service, "open_terminal", return_value={"terminal_id": "term_1", "pid": 1234}):
                    result = self.runner.invoke(cli_module.app, ["terminal-open", workspace_id])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["terminal_id"], "term_1")

                with patch.object(service, "terminal_read", return_value={"terminal_id": "term_1", "content": "ok", "offset": 2, "eof": False}):
                    result = self.runner.invoke(cli_module.app, ["terminal-read", workspace_id, "term_1"])
                    self.assertEqual(result.exit_code, 0, msg=result.output)
                    self.assertEqual(json.loads(result.stdout)["content"], "ok")


if __name__ == "__main__":
    unittest.main()
