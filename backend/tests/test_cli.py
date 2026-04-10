import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from typer.testing import CliRunner

from app import cli as cli_module
from app.models import RunRecord, RuntimeProbeResult, VerificationSummary, WorkspaceLoadRequest
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


if __name__ == "__main__":
    unittest.main()
