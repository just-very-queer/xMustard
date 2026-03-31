import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from app.models import (
    FixRecordRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    RunRecord,
    RunAcceptRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    SavedIssueViewRequest,
    WorktreeStatus,
    WorkspaceLoadRequest,
)
from app.runtimes import RuntimeService
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


class TrackerServiceTests(unittest.TestCase):
    def test_tracker_issue_persists_after_rescan(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            created = service.create_issue(
                snapshot.workspace.workspace_id,
                IssueCreateRequest(
                    title="Tracker-native regression",
                    severity="P1",
                    summary="Created from tracker only",
                    labels=["tracker"],
                    needs_followup=True,
                ),
            )

            rescanned = service.scan_workspace(snapshot.workspace.workspace_id)
            created_issue = next(item for item in rescanned.issues if item.bug_id == created.bug_id)
            self.assertEqual(created_issue.source, "tracker")
            self.assertEqual(created_issue.summary, "Created from tracker only")
            self.assertIn("tracker", created_issue.labels)

    def test_issue_override_persists_after_rescan(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            self.assertIsNotNone(snapshot)

            updated = service.update_issue(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(
                    issue_status="verification",
                    labels=["ops", "tracking"],
                    notes="manual note",
                    needs_followup=True,
                ),
            )
            self.assertEqual(updated.issue_status, "verification")

            rescanned = service.scan_workspace(snapshot.workspace.workspace_id)
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.issue_status, "verification")
            self.assertEqual(issue.labels, ["ops", "tracking"])
            self.assertEqual(issue.notes, "manual note")
            self.assertTrue(issue.needs_followup)

    def test_fix_record_persists_and_enriches_issue_context(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_test_fix",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_test_fix.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_test_fix.out.json"),
                summary={"session_id": "ses_123", "text_excerpt": "Fixed by tightening scope checks."},
            )
            store.save_run(run)

            fix = service.record_fix(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                FixRecordRequest(
                    summary="Tightened KB scope ownership checks",
                    run_id="run_test_fix",
                    changed_files=["api/src/example.py"],
                    tests_run=["pytest test_kb_router.py -q"],
                    issue_status="verification",
                ),
            )

            self.assertEqual(fix.run_id, "run_test_fix")
            self.assertEqual(fix.actor.kind, "agent")
            self.assertEqual(fix.session_id, "ses_123")
            self.assertEqual(service.list_fixes(snapshot.workspace.workspace_id, "P0_25M03_001")[0].fix_id, fix.fix_id)

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(packet.recent_fixes[0].fix_id, fix.fix_id)

    def test_suggest_fix_draft_uses_run_excerpt_tests_and_dirty_paths(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            sample_file = root / "api" / "src" / "example.py"
            sample_file.write_text("print('ok')\n", encoding="utf-8")

            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            sample_file.write_text("print('changed')\n", encoding="utf-8")
            run = RunRecord(
                run_id="run_test_draft",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_test_draft.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_test_draft.out.json"),
                summary={"text_excerpt": "Tightened scope checks in api/src/example.py\npytest test_kb_router.py -q"},
            )
            store.save_run(run)

            draft = service.suggest_fix_draft(snapshot.workspace.workspace_id, "P0_25M03_001", "run_test_draft")

            self.assertIn("api/src/example.py", draft.changed_files)
            self.assertIn("pytest test_kb_router.py -q", draft.tests_run)
            self.assertEqual(draft.suggested_issue_status, "verification")

    def test_review_ready_runs_are_derived_from_completed_runs_without_fix_records(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_review_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_review_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_review_001.out.json"),
            )
            store.save_run(run)

            rescanned = service.scan_workspace(snapshot.workspace.workspace_id)
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.review_ready_count, 1)
            self.assertEqual(issue.review_ready_runs, ["run_review_001"])
            self.assertEqual(rescanned.summary["review_ready_total"], 1)

            service.record_fix(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                FixRecordRequest(summary="Reviewed and recorded", run_id="run_review_001"),
            )
            rescanned = service.scan_workspace(snapshot.workspace.workspace_id)
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.review_ready_count, 0)

    def test_review_run_dismisses_completed_run_from_review_queue(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_review_dismissed",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_review_dismissed.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_review_dismissed.out.json"),
            )
            store.save_run(run)

            rescanned = service.scan_workspace(snapshot.workspace.workspace_id)
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.review_ready_runs, ["run_review_dismissed"])

            review = service.review_run(
                snapshot.workspace.workspace_id,
                "run_review_dismissed",
                RunReviewRequest(disposition="dismissed", notes="No fix to keep"),
            )
            self.assertEqual(review.disposition, "dismissed")

            rescanned = service.read_snapshot(snapshot.workspace.workspace_id)
            assert rescanned is not None
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.review_ready_count, 0)
            self.assertEqual(issue.review_ready_runs, [])
            activity = service.list_activity(snapshot.workspace.workspace_id, run_id="run_review_dismissed", limit=5)
            self.assertEqual(activity[0].action, "run.reviewed")

    def test_accept_review_run_records_fix_and_clears_queue(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_accept_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_accept_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_accept_001.out.json"),
                summary={"text_excerpt": "Tightened scope checks in api/src/example.py\npytest test_kb_router.py -q"},
                worktree=WorktreeStatus(available=True, is_git_repo=True, branch="main", dirty_files=1, dirty_paths=["api/src/example.py"]),
            )
            store.save_run(run)
            service.scan_workspace(snapshot.workspace.workspace_id)

            queue = service.list_review_queue(snapshot.workspace.workspace_id)
            self.assertEqual(len(queue), 1)
            self.assertEqual(queue[0].run.run_id, "run_accept_001")

            fix = service.accept_review_run(
                snapshot.workspace.workspace_id,
                "run_accept_001",
                RunAcceptRequest(issue_status="verification"),
            )
            self.assertEqual(fix.run_id, "run_accept_001")
            self.assertIsNotNone(fix.worktree)
            self.assertEqual(fix.worktree.dirty_paths, ["api/src/example.py"])

            rescanned = service.read_snapshot(snapshot.workspace.workspace_id)
            assert rescanned is not None
            issue = next(item for item in rescanned.issues if item.bug_id == "P0_25M03_001")
            self.assertEqual(issue.review_ready_count, 0)
            self.assertEqual(service.list_review_queue(snapshot.workspace.workspace_id), [])

    def test_runbook_lifecycle_and_issue_work_packet(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            runbook = service.save_runbook(
                snapshot.workspace.workspace_id,
                RunbookUpsertRequest(
                    name="Focused verify",
                    description="Verification-only workflow",
                    template="1. Reproduce the bug.\n2. Report scope only.",
                ),
            )
            listed = service.list_runbooks(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.runbook_id == "focused-verify" for item in listed))

            packet = service.issue_work(snapshot.workspace.workspace_id, "P0_25M03_001", runbook_id=runbook.runbook_id)
            self.assertEqual(packet.runbook, ["Reproduce the bug.", "Report scope only."])
            self.assertTrue(any(item.runbook_id == "fix" for item in packet.available_runbooks))
            self.assertIn("Selected runbook: Focused verify", packet.prompt)

    def test_issue_context_includes_severity_history_from_activity(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_issue(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(severity="P2", issue_status="triaged"),
            )
            service.update_issue(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(severity="P1", issue_status="investigating"),
            )

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")

            self.assertEqual(packet.issue.severity, "P1")
            self.assertTrue(packet.recent_activity)
            first_change = packet.recent_activity[0].details["before_after"]["severity"]
            self.assertEqual(first_change["from"], "P2")
            self.assertEqual(first_change["to"], "P1")
            self.assertIn("severity P2 -> P1", packet.prompt)

    def test_saved_view_persists_and_filters_issue_queue(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_issue(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(labels=["tracking"], needs_followup=True),
            )

            saved = service.create_saved_view(
                snapshot.workspace.workspace_id,
                SavedIssueViewRequest(
                    name="Followup queue",
                    labels=["tracking"],
                    needs_followup=True,
                ),
            )

            stored = service.list_saved_views(snapshot.workspace.workspace_id)
            self.assertEqual(len(stored), 1)
            self.assertEqual(stored[0].view_id, saved.view_id)

            filtered = service.list_issues(
                snapshot.workspace.workspace_id,
                labels=saved.labels,
                needs_followup=saved.needs_followup,
            )
            self.assertEqual([item.bug_id for item in filtered], ["P0_25M03_001"])

    def test_activity_log_records_issue_and_view_changes(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_issue(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueUpdateRequest(issue_status="verification", needs_followup=True),
            )
            service.create_saved_view(
                snapshot.workspace.workspace_id,
                SavedIssueViewRequest(name="Verification queue", statuses=["verification"]),
            )

            activity = service.list_activity(snapshot.workspace.workspace_id, limit=10)
            self.assertEqual(len(activity), 2)
            self.assertEqual(activity[0].entity_type, "view")
            self.assertEqual(activity[1].entity_type, "issue")
            self.assertEqual(activity[1].issue_id, "P0_25M03_001")


class RuntimeSummaryTests(unittest.TestCase):
    def test_summarize_run_output_extracts_session_and_text(self):
        runtime_service = RuntimeService(FileStore(Path(tempfile.mkdtemp()) / "data"))
        payload = "\n".join(
            [
                json.dumps({"type": "step_start", "sessionID": "ses_123"}),
                json.dumps({"type": "tool_use"}),
                json.dumps({"type": "text", "text": "First line"}),
                json.dumps({"type": "text", "part": {"text": "Second line"}}),
            ]
        )

        summary = runtime_service._summarize_run_output("opencode", payload)

        self.assertEqual(summary["session_id"], "ses_123")
        self.assertEqual(summary["event_count"], 4)
        self.assertEqual(summary["tool_event_count"], 1)
        self.assertIn("First line", summary["text_excerpt"])
        self.assertIn("Second line", summary["text_excerpt"])

    def test_sanitize_codex_args_strips_conflicting_flags(self):
        runtime_service = RuntimeService(FileStore(Path(tempfile.mkdtemp()) / "data"))

        sanitized = runtime_service._sanitize_codex_args(
            "--approval-mode full-auto -m gpt-5.2 --sandbox-mode read-only --profile bugfix --json"
        )

        self.assertEqual(sanitized, ["--profile", "bugfix"])

    def test_opencode_model_parsing_and_cache(self):
        temp_dir = tempfile.mkdtemp()
        store = FileStore(Path(temp_dir) / "data")
        binary = Path(temp_dir) / "opencode"
        binary.write_text("", encoding="utf-8")
        store.save_settings(store.load_settings().model_copy(update={"opencode_bin": str(binary)}))
        runtime_service = RuntimeService(store)

        completed = __import__("subprocess").CompletedProcess(
            args=[str(binary), "models"],
            returncode=0,
            stdout="opencode-go/minimax-m2.7\ninvalid token here\n* opencode-go/kimi-k2.5\nopencode-go/glm-5 extra\n",
            stderr="",
        )

        with patch("app.runtimes.subprocess.run", return_value=completed) as mocked_run:
            first = runtime_service._opencode_models()
            second = runtime_service._opencode_models()

        self.assertEqual(first, ["opencode-go/minimax-m2.7", "opencode-go/kimi-k2.5", "opencode-go/glm-5"])
        self.assertEqual(second, first)
        self.assertEqual(mocked_run.call_count, 1)

    def test_detect_runtimes_uses_runtime_capability_cache(self):
        runtime_service = RuntimeService(FileStore(Path(tempfile.mkdtemp()) / "data"))

        with patch.object(runtime_service, "_resolve_binary", side_effect=["/tmp/codex", "/tmp/opencode"]) as mocked_resolve:
            with patch.object(runtime_service, "_opencode_models", return_value=["opencode-go/minimax-m2.7"]) as mocked_models:
                first = runtime_service.detect_runtimes()
                second = runtime_service.detect_runtimes()

        self.assertEqual([item.runtime for item in first], ["codex", "opencode"])
        self.assertEqual([item.runtime for item in second], ["codex", "opencode"])
        self.assertEqual(mocked_models.call_count, 1)
        self.assertEqual(mocked_resolve.call_count, 2)


class FileStoreRunTests(unittest.TestCase):
    def test_list_runs_ignores_stream_output_json_files(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            store = FileStore(Path(tmp_dir) / "data")
            workspace_id = "workspace-test"
            run = RunRecord(
                run_id="run_test_001",
                workspace_id=workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(workspace_id) / "run_test_001.log"),
                output_path=str(store.runs_dir(workspace_id) / "run_test_001.out.json"),
            )
            store.save_run(run)
            Path(run.output_path).write_text('{"type":"step_start"}\n{"type":"step_finish"}\n', encoding="utf-8")

            runs = store.list_runs(workspace_id)

            self.assertEqual(len(runs), 1)
            self.assertEqual(runs[0].run_id, "run_test_001")

    def test_load_run_returns_none_for_empty_run_record_file(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            store = FileStore(Path(tmp_dir) / "data")
            workspace_id = "workspace-test"
            run_id = "run_empty_001"
            path = store.run_record_path(workspace_id, run_id)
            path.write_text("", encoding="utf-8")

            run = store.load_run(workspace_id, run_id)

            self.assertIsNone(run)

    def test_read_worktree_status_reports_dirty_files_for_git_workspace(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            root.mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            sample_file = root / "api" / "src" / "example.py"
            sample_file.write_text("print('ok')\n", encoding="utf-8")

            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            sample_file.write_text("print('changed')\n", encoding="utf-8")
            status = service.read_worktree_status(snapshot.workspace.workspace_id)

            self.assertTrue(status.available)
            self.assertTrue(status.is_git_repo)
            self.assertGreaterEqual(status.dirty_files, 1)
            self.assertIn("api/src/example.py", status.dirty_paths)


if __name__ == "__main__":
    unittest.main()
