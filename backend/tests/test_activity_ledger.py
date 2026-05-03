import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from fastapi.testclient import TestClient

from app import main as app_main
from app.models import IssueUpdateRequest, RunRecord, SavedIssueViewRequest, WorkspaceLoadRequest
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


class ActivityLedgerTests(unittest.TestCase):
    def _create_service(self, tmp_dir: str) -> tuple[TrackerService, str]:
        root = Path(tmp_dir) / "repo"
        (root / "docs" / "bugs").mkdir(parents=True)
        (root / "api" / "src").mkdir(parents=True)
        (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
        (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

        store = FileStore(Path(tmp_dir) / "data")
        service = TrackerService(store)
        snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
        assert snapshot is not None
        return service, snapshot.workspace.workspace_id

    def _queued_run(self, workspace_id: str, issue_id: str, runtime: str = "codex", model: str = "gpt-5-codex") -> RunRecord:
        return RunRecord(
            run_id="run_test_001",
            workspace_id=workspace_id,
            issue_id=issue_id,
            runtime=runtime,
            model=model,
            status="queued",
            title=f"{runtime}:{issue_id}",
            prompt="prompt",
            command=[runtime, "exec"],
            command_preview=f"{runtime} exec",
            log_path="/tmp/run.log",
            output_path="/tmp/run.out.json",
        )

    def test_service_activity_history_lists_run_issue_and_view_entries(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            service.update_issue(
                workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(issue_status="verification", labels=["tracking"], needs_followup=True),
            )
            service.create_saved_view(
                workspace_id,
                SavedIssueViewRequest(name="Verification queue", statuses=["verification"]),
            )

            run_record = self._queued_run(workspace_id, "P0_25M03_001")
            with patch.object(service.runtime_service, "validate_runtime_model"):
                with patch.object(service.runtime_service, "start_issue_run", return_value=run_record):
                    service.start_issue_run(workspace_id, "P0_25M03_001", "codex", "gpt-5-codex", None)

            activity = service.list_activity(workspace_id, limit=10)
            self.assertEqual([item.action for item in activity[:3]], ["run.queued", "view.created", "issue.updated"])
            self.assertEqual([item.entity_type for item in activity[:3]], ["run", "view", "issue"])

            issue_history = service.list_activity(workspace_id, issue_id="P0_25M03_001", limit=10)
            self.assertEqual([item.action for item in issue_history], ["run.queued", "issue.updated"])
            self.assertTrue(all(item.issue_id == "P0_25M03_001" for item in issue_history))

            run_history = service.list_activity(workspace_id, run_id="run_test_001", limit=10)
            self.assertEqual(len(run_history), 1)
            self.assertEqual(run_history[0].action, "run.queued")
            self.assertEqual(run_history[0].entity_type, "run")
            self.assertEqual(run_history[0].issue_id, "P0_25M03_001")

            activity_path = service.store.activity_path(workspace_id)
            self.assertTrue(activity_path.exists())
            self.assertEqual(len(activity_path.read_text(encoding="utf-8").splitlines()), 3)

    def test_activity_api_and_export_include_filtered_history(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            service.update_issue(
                workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(issue_status="verification", notes="ready for review"),
            )
            service.create_saved_view(
                workspace_id,
                SavedIssueViewRequest(name="Verification queue", statuses=["verification"]),
            )

            run_record = self._queued_run(workspace_id, "P0_25M03_001", runtime="opencode", model="opencode-go/minimax-m2.7")
            with patch.object(service.runtime_service, "validate_runtime_model"):
                with patch.object(service.runtime_service, "start_issue_run", return_value=run_record):
                    service.start_issue_run(
                        workspace_id,
                        "P0_25M03_001",
                        "opencode",
                        "opencode-go/minimax-m2.7",
                        "investigate only",
                    )

            with patch.object(app_main, "SERVICE", service):
                with TestClient(app_main.app) as client:
                    response = client.get(
                        f"/api/workspaces/{workspace_id}/activity",
                        params={"issue_id": "P0_25M03_001", "limit": 10},
                    )
                    self.assertEqual(response.status_code, 200)
                    issue_history = response.json()
                    self.assertEqual([item["action"] for item in issue_history], ["run.queued", "issue.updated"])

                    run_response = client.get(
                        f"/api/workspaces/{workspace_id}/activity",
                        params={"run_id": "run_test_001", "limit": 10},
                    )
                    self.assertEqual(run_response.status_code, 200)
                    run_history = run_response.json()
                    self.assertEqual(len(run_history), 1)
                    self.assertEqual(run_history[0]["entity_type"], "run")

                    export_response = client.get(f"/api/workspaces/{workspace_id}/export")
                    self.assertEqual(export_response.status_code, 200)
                    export_payload = export_response.json()

            self.assertIn("activity", export_payload)
            self.assertEqual([item["action"] for item in export_payload["activity"][:3]], ["run.queued", "view.created", "issue.updated"])
            self.assertEqual(export_payload["activity"][0]["run_id"], "run_test_001")
            self.assertEqual(export_payload["activity"][0]["issue_id"], "P0_25M03_001")

    def test_ingestion_plan_api_exposes_phase_readiness(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)
            root = Path(service.get_workspace(workspace_id).root_path)
            (root / "package.json").write_text('{"name":"fixture","scripts":{"dev":"vite","test":"vitest run"}}', encoding="utf-8")
            (root / "Makefile").write_text("backend:\n\tpython3 -m uvicorn app.main:app\n", encoding="utf-8")
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            with patch.object(app_main, "SERVICE", service):
                with patch("app.service.tree_sitter_available", return_value=True):
                    with patch("app.service.shutil.which", return_value="/opt/homebrew/bin/sg"):
                        with TestClient(app_main.app) as client:
                            response = client.get(f"/api/workspaces/{workspace_id}/ingestion-plan")

            self.assertEqual(response.status_code, 200)
            payload = response.json()
            self.assertEqual(payload["next_phase_id"], "tree_sitter_index")
            self.assertIn("tree_sitter_index", payload["ready_phase_ids"])
            self.assertIn("search_materialization", payload["blocked_phase_ids"])

    def test_fastapi_path_symbols_route_is_not_registered_after_go_cutover(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(app_main, "SERVICE", service):
                with TestClient(app_main.app) as client:
                    response = client.get(
                        f"/api/workspaces/{workspace_id}/path-symbols",
                        params={"path": "api/src/example.py"},
                    )

            self.assertEqual(response.status_code, 404)

    def test_fastapi_semantic_search_route_is_not_registered_after_go_cutover(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            with patch.object(app_main, "SERVICE", service):
                with TestClient(app_main.app) as client:
                    response = client.get(
                        f"/api/workspaces/{workspace_id}/semantic-search",
                        params={"pattern": "def $A():", "language": "python"},
                    )

            self.assertEqual(response.status_code, 404)


if __name__ == "__main__":
    unittest.main()
