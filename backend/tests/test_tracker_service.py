import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from app.models import (
    DiscoverySignal,
    EvidenceRef,
    FixRecordRequest,
    ImprovementSuggestion,
    IssueContextReplayRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    PatchCritique,
    RunRecord,
    RunAcceptRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    SavedIssueViewRequest,
    ThreatModelUpsertRequest,
    TicketContextUpsertRequest,
    VerificationProfileUpsertRequest,
    VerifyIssueRequest,
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

    def test_load_workspace_rescans_when_cached_snapshot_uses_old_scanner_version(self):
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
            assert snapshot is not None

            stale_snapshot = snapshot.model_copy(
                update={
                    "scanner_version": 0,
                    "signals": [
                        DiscoverySignal(
                            signal_id="sig_legacy",
                            kind="annotation",
                            severity="P2",
                            title="Legacy signal",
                            summary="Old cached signal",
                            file_path="legacy.py",
                            line=1,
                            evidence=[EvidenceRef(path="legacy.py", line=1, excerpt="# TODO legacy")],
                            fingerprint="legacy",
                        )
                    ],
                    "summary": {**snapshot.summary, "signals_total": 1},
                }
            )
            store.save_snapshot(stale_snapshot)

            refreshed = service.load_workspace(
                WorkspaceLoadRequest(
                    root_path=str(root),
                    auto_scan=True,
                    prefer_cached_snapshot=True,
                )
            )

            self.assertIsNotNone(refreshed)
            assert refreshed is not None
            self.assertEqual(refreshed.scanner_version, service.SCANNER_VERSION)
            self.assertEqual(refreshed.summary["signals_total"], 0)
            self.assertFalse(any(signal.file_path == "legacy.py" for signal in refreshed.signals))

    def test_issue_context_includes_repo_guidance_and_prompt_section(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / ".openhands" / "skills").mkdir(parents=True)
            (root / ".openhands" / "microagents").mkdir(parents=True)
            (root / ".agents" / "skills" / "review-helper").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")
            (root / "AGENTS.md").write_text(
                "# AGENTS\n\n- Run pytest before finalizing.\n- Prefer minimal safe fixes.\n",
                encoding="utf-8",
            )
            (root / ".openhands" / "microagents" / "repo.md").write_text(
                "---\nname: repo\ntype: repo\n---\n\n# Repository instructions\n\n- Keep the FastAPI backend and React UI in sync.\n- Favor evidence-backed issue workflows over generic chat.\n",
                encoding="utf-8",
            )
            (root / "CONVENTIONS.md").write_text(
                "# Conventions\n\n- Use type hints.\n",
                encoding="utf-8",
            )
            (root / ".openhands" / "skills" / "bugfix.md").write_text(
                "---\nname: bugfix\nkeywords: [\"bug\", \"regression\"]\n---\n\n# Bugfix skill\n\n- Reproduce before editing.\n",
                encoding="utf-8",
            )
            (root / ".agents" / "skills" / "review-helper" / "SKILL.md").write_text(
                "# Review helper\n\n- Summarize risk before approval.\n",
                encoding="utf-8",
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            self.assertIsNotNone(snapshot)
            assert snapshot is not None

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")

            self.assertTrue(any(item.path == "AGENTS.md" and item.always_on for item in packet.guidance))
            self.assertTrue(any(item.path == ".openhands/microagents/repo.md" and item.always_on for item in packet.guidance))
            self.assertTrue(any(item.path == ".openhands/skills/bugfix.md" for item in packet.guidance))
            self.assertTrue(any(item.path == ".agents/skills/review-helper/SKILL.md" for item in packet.guidance))
            self.assertEqual(sum(1 for item in packet.guidance if item.path == "AGENTS.md"), 1)
            self.assertIn("Repository guidance:", packet.prompt)
            self.assertIn("AGENTS.md", packet.prompt)
            self.assertIn(".openhands/microagents/repo.md", packet.prompt)
            self.assertIn(".agents/skills/review-helper/SKILL.md", packet.prompt)
            self.assertIn("CONVENTIONS.md", packet.prompt)

    def test_issue_context_includes_inferred_verification_profiles(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")
            (root / "AGENTS.md").write_text(
                "# AGENTS\n\n- Run pytest --cov=. --cov-report=xml before finalizing.\n",
                encoding="utf-8",
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")

            self.assertTrue(packet.available_verification_profiles)
            self.assertTrue(any("pytest" in item.test_command for item in packet.available_verification_profiles))
            self.assertIn("Known verification profiles:", packet.prompt)
            self.assertIn("pytest --cov=. --cov-report=xml", packet.prompt)

    def test_verification_profile_crud(self):
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

            saved = service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    name="Backend pytest",
                    description="Project verification commands",
                    test_command="pytest -q",
                    coverage_command="pytest --cov=. --cov-report=xml",
                    coverage_report_path="coverage.xml",
                    coverage_format="cobertura",
                    max_runtime_seconds=90,
                    retry_count=2,
                    source_paths=["AGENTS.md"],
                ),
            )

            profiles = service.list_verification_profiles(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.profile_id == saved.profile_id for item in profiles))
            custom = next(item for item in profiles if item.profile_id == saved.profile_id)
            self.assertEqual(custom.coverage_report_path, "coverage.xml")
            self.assertEqual(custom.retry_count, 2)

            service.delete_verification_profile(snapshot.workspace.workspace_id, saved.profile_id)
            remaining = service.list_verification_profiles(snapshot.workspace.workspace_id)
            self.assertFalse(any(item.profile_id == saved.profile_id for item in remaining))

    def test_ticket_context_crud_and_issue_packet(self):
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

            saved = service.save_ticket_context(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                TicketContextUpsertRequest(
                    provider="manual",
                    title="Customer escalation",
                    summary="The fix must preserve export compatibility for existing teams.",
                    acceptance_criteria=[
                        "Exports keep the previous CSV columns intact",
                        "The failure no longer appears in the current repro path",
                    ],
                    links=["https://tracker.example.com/incidents/123"],
                    labels=["customer", "export"],
                    status="open",
                ),
            )

            contexts = service.list_ticket_contexts(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertTrue(any(item.context_id == saved.context_id for item in contexts))

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(packet.ticket_contexts[0].title, "Customer escalation")
            self.assertIn("Ticket context:", packet.prompt)
            self.assertIn("Exports keep the previous CSV columns intact", packet.prompt)

            service.delete_ticket_context(snapshot.workspace.workspace_id, "P0_25M03_001", saved.context_id)
            self.assertEqual(service.list_ticket_contexts(snapshot.workspace.workspace_id, "P0_25M03_001"), [])

    def test_github_import_creates_ticket_context_with_acceptance_criteria(self):
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

            issue_payload = json.dumps(
                [
                    {
                        "number": 123,
                        "title": "Export bug",
                        "body": "## Acceptance Criteria\n- Export keeps old columns\n- Repro passes on current branch\n",
                        "labels": [{"name": "bug"}, {"name": "customer"}],
                        "state": "open",
                        "html_url": "https://github.com/acme/repo/issues/123",
                    }
                ]
            ).encode("utf-8")

            class FakeResponse:
                def __enter__(self):
                    return self

                def __exit__(self, exc_type, exc, tb):
                    return False

                def read(self):
                    return issue_payload

            with patch("app.service.urllib.request.urlopen", return_value=FakeResponse()):
                imports = service.import_github_issues(snapshot.workspace.workspace_id, "acme/repo")

            self.assertEqual(imports[0]["issue_id"], "GH-123")
            contexts = service.list_ticket_contexts(snapshot.workspace.workspace_id, "GH-123")
            self.assertEqual(contexts[0].provider, "github")
            self.assertEqual(contexts[0].external_id, "acme/repo#123")
            self.assertEqual(
                contexts[0].acceptance_criteria,
                ["Export keeps old columns", "Repro passes on current branch"],
            )

    def test_threat_model_crud_and_issue_packet(self):
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

            saved = service.save_threat_model(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                ThreatModelUpsertRequest(
                    title="Export boundary review",
                    methodology="stride",
                    summary="The export path crosses an auth boundary and touches customer data.",
                    assets=["customer export data", "authorization policy"],
                    entry_points=["POST /exports"],
                    trust_boundaries=["authenticated user -> export service"],
                    abuse_cases=["Export another tenant's records"],
                    mitigations=["Require tenant scoping before export"],
                    references=["https://owasp.org/www-project-threat-dragon/"],
                    status="reviewed",
                ),
            )

            threat_models = service.list_threat_models(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertTrue(any(item.threat_model_id == saved.threat_model_id for item in threat_models))

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(packet.threat_models[0].title, "Export boundary review")
            self.assertIn("Threat model:", packet.prompt)
            self.assertIn("Export another tenant's records", packet.prompt)

            exported = service.export_workspace(snapshot.workspace.workspace_id)
            self.assertEqual(exported.threat_models[0].threat_model_id, saved.threat_model_id)

            service.delete_threat_model(snapshot.workspace.workspace_id, "P0_25M03_001", saved.threat_model_id)
            self.assertEqual(service.list_threat_models(snapshot.workspace.workspace_id, "P0_25M03_001"), [])

    def test_capture_issue_context_replay_persists_prompt_snapshot(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")
            (root / "AGENTS.md").write_text("# AGENTS\n\n- Run pytest -q before finalizing.\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            replay = service.capture_issue_context_replay(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueContextReplayRequest(label="baseline"),
            )

            self.assertEqual(replay.label, "baseline")
            self.assertIn("You are fixing bug P0_25M03_001", replay.prompt)
            self.assertEqual(service.list_issue_context_replays(snapshot.workspace.workspace_id, "P0_25M03_001")[0].replay_id, replay.replay_id)

    def test_repo_map_and_related_paths_enrich_tracker_issue_context(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "backend" / "app").mkdir(parents=True)
            (root / "tests").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "backend" / "app" / "main.py").write_text("def export_csv():\n    return 'ok'\n", encoding="utf-8")
            (root / "tests" / "test_exporter.py").write_text("def test_export_csv():\n    assert True\n", encoding="utf-8")
            (root / "pyproject.toml").write_text("[project]\nname='repo'\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            created = service.create_issue(
                snapshot.workspace.workspace_id,
                IssueCreateRequest(
                    title="Export regression in backend app",
                    severity="P1",
                    summary="CSV export fails during the backend flow.",
                    labels=["export", "backend"],
                ),
            )

            repo_map = service.read_repo_map(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.path == "backend" for item in repo_map.top_directories))

            packet = service.build_issue_context(snapshot.workspace.workspace_id, created.bug_id)
            self.assertIsNotNone(packet.repo_map)
            self.assertIn("Structural context:", packet.prompt)
            self.assertIn("Ranked related paths:", packet.prompt)
            self.assertTrue(any(path.startswith("backend") or path.startswith("tests") for path in packet.related_paths))

    def test_run_session_insight_reports_guidance_and_review_findings(self):
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
            assert snapshot is not None

            run = RunRecord(
                run_id="run_insight_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_insight_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_insight_001.out.json"),
                guidance_paths=["AGENTS.md"],
                summary={"event_count": 10, "tool_event_count": 9, "text_excerpt": "Applied the fix and ran pytest -q"},
            )
            store.save_run(run)
            metrics = service.runtime_service.calculate_run_metrics(run, len("Applied the fix and ran pytest -q"))
            service.runtime_service.save_run_metrics(metrics)
            service._save_critique(
                PatchCritique(
                    critique_id="crit_001",
                    workspace_id=snapshot.workspace.workspace_id,
                    run_id="run_insight_001",
                    issue_id="P0_25M03_001",
                    overall_quality="needs_work",
                    issues_found=["Missing edge-case assertion"],
                    improvements=[
                        ImprovementSuggestion(
                            suggestion_id="imp_001",
                            file_path="api/src/example.py",
                            category="testing",
                            severity="high",
                            description="Add a regression assertion for the failing branch",
                        )
                    ],
                    summary="Patch mostly works but needs stronger regression coverage.",
                )
            )

            insight = service.get_run_session_insight(snapshot.workspace.workspace_id, "run_insight_001")

            self.assertEqual(insight["run_id"], "run_insight_001")
            self.assertEqual(insight["guidance_used"], ["AGENTS.md"])
            self.assertTrue(any("high share of events in tool usage" in item for item in insight["risks"]))
            self.assertTrue(any("high-severity improvement suggestion" in item for item in insight["risks"]))
            self.assertTrue(any("always-on repo instructions" in item for item in insight["recommendations"]))

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

    def test_parse_verification_excerpt_reads_json_payload(self):
        service = TrackerService(FileStore(Path(tempfile.gettempdir()) / "unused-bug-tracker-data"))
        payload = service._parse_verification_excerpt(
            """
            Verification complete.
            ```json
            {"code_checked":"yes","fixed":"no","confidence":"high","summary":"Checked the code path.","evidence":["api/src/example.py:12"],"tests":["pytest -q"]}
            ```
            """
        )
        self.assertEqual(payload["code_checked"], "yes")
        self.assertEqual(payload["fixed"], "no")
        self.assertEqual(payload["confidence"], "high")
        self.assertEqual(payload["evidence"], ["api/src/example.py:12"])

    def test_parse_verification_excerpt_reads_json_from_jsonl_text_event(self):
        service = TrackerService(FileStore(Path(tempfile.gettempdir()) / "unused-bug-tracker-data"))
        payload = service._parse_verification_excerpt(
            '\n'.join(
                [
                    '{"type":"step_start","part":{"type":"step-start"}}',
                    '{"type":"text","part":{"text":"{\\"code_checked\\":\\"yes\\",\\"fixed\\":\\"yes\\",\\"confidence\\":\\"high\\",\\"summary\\":\\"Verified in current code\\",\\"evidence\\":[\\"api/src/example.py:12\\"],\\"tests\\":[\\"pytest -q\\"]}"}}',
                ]
            )
        )
        self.assertEqual(payload["code_checked"], "yes")
        self.assertEqual(payload["fixed"], "yes")
        self.assertEqual(payload["tests"], ["pytest -q"])

    def test_parse_verification_excerpt_marks_code_checked_when_only_tool_reads_exist(self):
        service = TrackerService(FileStore(Path(tempfile.gettempdir()) / "unused-bug-tracker-data"))
        payload = service._parse_verification_excerpt(
            '{"type":"tool_use","part":{"tool":"read","state":{"output":"<content>api/src/example.py</content>"}}}'
        )
        self.assertEqual(payload["code_checked"], "yes")
        self.assertEqual(payload["fixed"], "unknown")

    def test_load_run_for_verification_waits_for_summary(self):
        service = TrackerService(FileStore(Path(tempfile.gettempdir()) / "unused-bug-tracker-data"))
        run = RunRecord(
            run_id="run_wait_summary",
            workspace_id="ws",
            issue_id="P0_25M03_001",
            runtime="opencode",
            model="opencode-go/minimax-m2.7",
            status="cancelled",
            title="opencode:P0_25M03_001",
            prompt="prompt",
            command=["opencode", "run"],
            command_preview="opencode run",
            log_path=str(Path(tempfile.gettempdir()) / "run_wait_summary.log"),
            output_path=str(Path(tempfile.gettempdir()) / "run_wait_summary.out.json"),
            summary=None,
        )
        settled = run.model_copy(update={"summary": {"text_excerpt": '{"code_checked":"yes","fixed":"yes","confidence":"high","summary":"settled"}'}})
        with patch.object(service.store, "load_run", side_effect=[run, run, settled]):
            with patch("app.service.time.sleep", return_value=None):
                loaded = service._load_run_for_verification("ws", "run_wait_summary")
        self.assertIsNotNone(loaded)
        assert loaded is not None
        self.assertIsNotNone(loaded.summary)

    def test_verify_issue_three_pass_records_consensus(self):
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
            workspace_id = snapshot.workspace.workspace_id

            run_ids = ["run_verify_1", "run_verify_2", "run_verify_3"]
            models = ["opencode-go/minimax-m2.7", "opencode-go/glm-5", "opencode-go/kimi-k2.5"]
            excerpts = [
                '{"code_checked":"yes","fixed":"yes","confidence":"high","summary":"Verified and fixed","evidence":["api/src/example.py:12"],"tests":["pytest -q"]}',
                '{"code_checked":"yes","fixed":"yes","confidence":"medium","summary":"Confirmed fix","evidence":["api/src/example.py:12"],"tests":["pytest -q"]}',
                '{"code_checked":"yes","fixed":"unknown","confidence":"low","summary":"Checked but inconclusive","evidence":["api/src/example.py:12"],"tests":[]}',
            ]

            for run_id, model, excerpt in zip(run_ids, models, excerpts):
                run = RunRecord(
                    run_id=run_id,
                    workspace_id=workspace_id,
                    issue_id="P0_25M03_001",
                    runtime="opencode",
                    model=model,
                    status="completed",
                    title=f"opencode:P0_25M03_001:{model}",
                    prompt="prompt",
                    command=["opencode", "run"],
                    command_preview="opencode run",
                    log_path=str(store.runs_dir(workspace_id) / f"{run_id}.log"),
                    output_path=str(store.runs_dir(workspace_id) / f"{run_id}.out.json"),
                    summary={"text_excerpt": excerpt},
                )
                store.save_run(run)

            starts = [
                RunRecord(
                    run_id=run_id,
                    workspace_id=workspace_id,
                    issue_id="P0_25M03_001",
                    runtime="opencode",
                    model=model,
                    status="queued",
                    title=f"opencode:P0_25M03_001:{model}",
                    prompt="prompt",
                    command=["opencode", "run"],
                    command_preview="opencode run",
                    log_path=str(store.runs_dir(workspace_id) / f"{run_id}.log"),
                    output_path=str(store.runs_dir(workspace_id) / f"{run_id}.out.json"),
                )
                for run_id, model in zip(run_ids, models)
            ]

            with patch.object(service.runtime_service, "validate_runtime_model", return_value=None):
                with patch.object(service.runtime_service, "start_issue_run", side_effect=starts):
                    with patch.object(service, "get_run", side_effect=[item.model_dump(mode="json") | {"status": "completed"} for item in starts]):
                        with patch("app.service.time.sleep", return_value=None):
                            summary = service.verify_issue_three_pass(
                                workspace_id,
                                "P0_25M03_001",
                                VerifyIssueRequest(
                                    runtime="opencode",
                                    models=models,
                                    timeout_seconds=0.01,
                                    poll_interval=0.0,
                                ),
                            )

            self.assertEqual(len(summary.records), 3)
            self.assertEqual(summary.checked_yes, 3)
            self.assertEqual(summary.consensus_code_checked, "yes")
            self.assertEqual(summary.fixed_yes, 2)
            self.assertEqual(summary.consensus_fixed, "yes")
            persisted = service.list_verifications(workspace_id, "P0_25M03_001")
            self.assertEqual(len(persisted), 3)
            self.assertEqual(persisted[0].issue_id, "P0_25M03_001")

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
