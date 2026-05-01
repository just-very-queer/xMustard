import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from app.models import (
    BrowserDumpUpsertRequest,
    DiscoverySignal,
    EvidenceRef,
    EvalScenarioUpsertRequest,
    EvalReplayBatchRecord,
    FileSymbolSummaryMaterializationRecord,
    FixRecordRequest,
    ImprovementSuggestion,
    GuidanceStarterRequest,
    IssueContextReplayRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    PatchCritique,
    PathSymbolsResult,
    PlanFileAttachment,
    PlanApproveRequest,
    PlanStep,
    PlanTrackingUpdateRequest,
    PostgresBootstrapResult,
    PostgresSemanticMaterializationResult,
    PostgresWorkspaceSemanticMaterializationResult,
    RepoMapSymbolRecord,
    RunPlan,
    SemanticIndexBaselineRecord,
    SemanticIndexStatus,
    SemanticPatternMatchRecord,
    SemanticPatternQueryResult,
    SemanticQueryMaterializationRecord,
    SemanticMatchMaterializationRecord,
    RunRecord,
    RunAcceptRequest,
    RunMetrics,
    RunReviewRequest,
    RunbookUpsertRequest,
    SavedIssueViewRequest,
    ThreatModelUpsertRequest,
    TicketContextUpsertRequest,
    VerificationProfileUpsertRequest,
    VerificationProfileExecutionResult,
    VerificationChecklistResult,
    VerificationProfileRunRequest,
    VerifyIssueRequest,
    VulnerabilityFindingUpsertRequest,
    WorktreeStatus,
    WorkspaceLoadRequest,
    SymbolMaterializationRecord,
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
            (root / ".xmustard.yaml").write_text(
                """
description: Export-heavy service with browser-based regressions.
code_guidelines:
  - Keep API and UI contracts aligned.
mcp_servers:
  - name: browser-dump
    description: Browser MCP snapshots for UI regressions.
reviews:
  path_instructions:
    - path: "api/src/**"
      title: "API focus"
      instructions: |
        Check validation, response-shape compatibility, and regression coverage.
  path_filters:
    - "!dist/**"
""".strip()
                + "\n",
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
            self.assertIsNotNone(packet.repo_config)
            self.assertEqual(packet.repo_config.source_path, ".xmustard.yaml")
            self.assertEqual(len(packet.matched_path_instructions), 1)
            self.assertEqual(packet.matched_path_instructions[0].matched_paths, ["api/src/example.py"])
            self.assertIn("Path-specific guidance:", packet.prompt)
            self.assertIn("API focus", packet.prompt)
            self.assertIn("Repo config:", packet.prompt)
            self.assertIn("browser-dump", packet.prompt)

            health = service.get_workspace_repo_config_health(snapshot.workspace.workspace_id)
            self.assertEqual(health.status, "configured")
            self.assertEqual(health.path_instruction_count, 1)

    def test_guidance_health_and_starter_generation_workflow(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "backend" / "app").mkdir(parents=True)
            (root / "frontend" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "backend" / "app" / "main.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            health_before = service.get_workspace_guidance_health(snapshot.workspace.workspace_id)
            self.assertEqual(health_before.status, "missing")
            self.assertIn("AGENTS.md", health_before.missing_files)

            created = service.generate_guidance_starter(
                snapshot.workspace.workspace_id,
                GuidanceStarterRequest(template_id="agents"),
            )
            self.assertEqual(created.path, "AGENTS.md")
            self.assertTrue((root / "AGENTS.md").exists())

            guidance = service.list_workspace_guidance(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.path == "AGENTS.md" for item in guidance))

            health_after = service.get_workspace_guidance_health(snapshot.workspace.workspace_id)
            self.assertEqual(health_after.status, "stale")
            self.assertIn("AGENTS.md", health_after.stale_files)

            (root / "AGENTS.md").write_text(
                "# Repository guide\n\n- Run pytest -q before finalizing.\n- Keep issue evidence current.\n",
                encoding="utf-8",
            )
            customized = service.get_workspace_guidance_health(snapshot.workspace.workspace_id)
            self.assertIn(customized.status, {"partial", "healthy"})
            self.assertNotIn("AGENTS.md", customized.stale_files)

    def test_guidance_starter_overwrite_requires_explicit_flag(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.generate_guidance_starter(snapshot.workspace.workspace_id, GuidanceStarterRequest(template_id="agents"))
            with self.assertRaises(FileExistsError):
                service.generate_guidance_starter(snapshot.workspace.workspace_id, GuidanceStarterRequest(template_id="agents"))

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
                    checklist_items=["Coverage artifact is produced", "Regression command passes"],
                ),
            )

            profiles = service.list_verification_profiles(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.profile_id == saved.profile_id for item in profiles))
            custom = next(item for item in profiles if item.profile_id == saved.profile_id)
            self.assertEqual(custom.coverage_report_path, "coverage.xml")
            self.assertEqual(custom.retry_count, 2)
            self.assertEqual(custom.checklist_items, ["Coverage artifact is produced", "Regression command passes"])

            service.delete_verification_profile(snapshot.workspace.workspace_id, saved.profile_id)
            remaining = service.list_verification_profiles(snapshot.workspace.workspace_id)
            self.assertFalse(any(item.profile_id == saved.profile_id for item in remaining))

    def test_running_verification_profile_persists_coverage_and_activity(self):
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

            profile = service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    name="Backend pytest",
                    description="Project verification commands",
                    test_command="printf 'tests ok\\n'",
                    coverage_command="printf 'SF:src/app.py\\nDA:1,1\\nDA:2,0\\nend_of_record\\n' > coverage.info",
                    coverage_report_path="coverage.info",
                    coverage_format="lcov",
                    max_runtime_seconds=10,
                    retry_count=1,
                    source_paths=["AGENTS.md"],
                    checklist_items=["Coverage artifact is produced", "Regression command passes"],
                ),
            )
            service.store.save_run(
                RunRecord(
                    run_id="manual-verification-run",
                    workspace_id=snapshot.workspace.workspace_id,
                    issue_id="P0_25M03_001",
                    runtime="codex",
                    model="gpt-5.4",
                    status="completed",
                    title="Verification lane",
                    prompt="Run verification profile",
                    command=["codex", "run"],
                    command_preview="codex run",
                    log_path="",
                    output_path="",
                    worktree=WorktreeStatus(
                        available=True,
                        is_git_repo=True,
                        branch="feature/verification-reports",
                        dirty_files=0,
                        staged_files=0,
                        untracked_files=0,
                        ahead=0,
                        behind=0,
                        dirty_paths=[],
                    ),
                )
            )

            result = service.run_issue_verification_profile(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                profile.profile_id,
                VerificationProfileRunRequest(run_id="manual-verification-run"),
            )

            self.assertTrue(result.success)
            self.assertEqual(result.attempt_count, 1)
            self.assertIsNotNone(result.coverage_result)
            assert result.coverage_result is not None
            self.assertEqual(result.coverage_result.format, "lcov")
            self.assertEqual(result.confidence, "high")
            self.assertTrue(all(item.passed for item in result.checklist_results))

            coverage = service.get_coverage(
                snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                run_id="manual-verification-run",
            )
            self.assertIsNotNone(coverage)
            assert coverage is not None
            self.assertEqual(coverage["line_coverage"], result.coverage_result.line_coverage)

            updated_issue = service._get_issue(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertIn("printf 'tests ok\\n'", updated_issue.tests_passed)
            self.assertTrue(any(item.path == "coverage.info" for item in updated_issue.verification_evidence))

            activity = service.list_activity(snapshot.workspace.workspace_id, issue_id="P0_25M03_001")
            self.assertTrue(any(item.action == "verification.profile_run" for item in activity))
            self.assertTrue(any(item.action == "coverage.parsed" for item in activity))
            history = service.list_verification_profile_history(
                snapshot.workspace.workspace_id,
                profile_id=profile.profile_id,
                issue_id="P0_25M03_001",
            )
            self.assertEqual(len(history), 1)
            self.assertEqual(history[0].run_id, "manual-verification-run")
            self.assertEqual(history[0].confidence, "high")
            reports = service.list_verification_profile_reports(
                snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
            )
            report = next(item for item in reports if item.profile_id == profile.profile_id)
            self.assertEqual(report.total_runs, 1)
            self.assertEqual(report.success_rate, 100.0)
            self.assertEqual(report.confidence_counts["high"], 1)
            self.assertEqual(report.runtime_breakdown[0].label, "codex")
            self.assertEqual(report.model_breakdown[0].label, "gpt-5.4")
            self.assertEqual(report.branch_breakdown[0].label, "feature/verification-reports")

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

    def test_browser_dump_crud_and_issue_packet(self):
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

            saved = service.save_browser_dump(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                BrowserDumpUpsertRequest(
                    source="mcp-chrome",
                    label="Checkout page dump",
                    page_url="http://localhost:3000/checkout",
                    page_title="Checkout",
                    summary="The browser shows an unhandled state after submit.",
                    dom_snapshot="button disabled=true\nerror banner visible\ncart total stale",
                    console_messages=["TypeError: Cannot read properties of undefined"],
                    network_requests=["POST /api/checkout -> 500"],
                    screenshot_path="artifacts/checkout.png",
                    notes="Captured from MCP chrome devtools snapshot",
                ),
            )

            dumps = service.list_browser_dumps(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertTrue(any(item.dump_id == saved.dump_id for item in dumps))

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(packet.browser_dumps[0].label, "Checkout page dump")
            self.assertIn("Browser context:", packet.prompt)
            self.assertIn("POST /api/checkout -> 500", packet.prompt)

            export = service.export_workspace(snapshot.workspace.workspace_id)
            self.assertEqual(export.browser_dumps[0].dump_id, saved.dump_id)

            service.delete_browser_dump(snapshot.workspace.workspace_id, "P0_25M03_001", saved.dump_id)
            self.assertEqual(service.list_browser_dumps(snapshot.workspace.workspace_id, "P0_25M03_001"), [])

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

    def test_vulnerability_finding_crud_and_issue_packet(self):
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

            saved = service.save_vulnerability_finding(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                VulnerabilityFindingUpsertRequest(
                    title="Tenant export authorization bypass",
                    scanner="nessus",
                    source="nessus-json",
                    severity="high",
                    summary="Export endpoint may allow cross-tenant record access.",
                    rule_id="plugin-12345",
                    status="open",
                    location_path="api/src/example.py",
                    location_line=12,
                    cwe_ids=["CWE-639"],
                    cve_ids=["CVE-2026-0001"],
                    references=["https://cwe.mitre.org/data/definitions/639.html"],
                    evidence=["Plugin output shows missing tenant scoping on export."],
                    raw_payload='{"plugin_id": 12345}',
                ),
            )

            findings = service.list_vulnerability_findings(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertTrue(any(item.finding_id == saved.finding_id for item in findings))

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(packet.vulnerability_findings[0].title, "Tenant export authorization bypass")
            self.assertIn("Vulnerability findings:", packet.prompt)
            self.assertIn("CWE-639", packet.prompt)

            exported = service.export_workspace(snapshot.workspace.workspace_id)
            self.assertEqual(exported.vulnerability_findings[0].finding_id, saved.finding_id)

            service.delete_vulnerability_finding(snapshot.workspace.workspace_id, "P0_25M03_001", saved.finding_id)
            self.assertEqual(service.list_vulnerability_findings(snapshot.workspace.workspace_id, "P0_25M03_001"), [])

    def test_import_sarif_vulnerability_findings_normalizes_and_dedupes(self):
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

            sarif_payload = json.dumps(
                {
                    "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
                    "runs": [
                        {
                            "tool": {
                                "driver": {
                                    "name": "CodeQL",
                                    "rules": [
                                        {
                                            "id": "py/path-injection",
                                            "shortDescription": {"text": "Path injection"},
                                            "helpUri": "https://example.com/rules/path-injection",
                                            "properties": {"tags": ["security", "external/cwe/cwe-022", "CVE-2026-1111"]},
                                        }
                                    ],
                                }
                            },
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
            )

            imported = service.import_sarif_vulnerability_findings(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                sarif_payload,
            )
            self.assertEqual(len(imported), 1)
            self.assertEqual(imported[0].scanner, "CodeQL")
            self.assertEqual(imported[0].source, "sarif")
            self.assertEqual(imported[0].severity, "high")
            self.assertEqual(imported[0].location_path, "api/src/example.py")
            self.assertIn("CWE-022", imported[0].cwe_ids)
            self.assertIn("CVE-2026-1111", imported[0].cve_ids)

            imported_again = service.import_sarif_vulnerability_findings(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                sarif_payload,
            )
            self.assertEqual(imported_again[0].finding_id, imported[0].finding_id)
            findings = service.list_vulnerability_findings(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertEqual(len(findings), 1)

    def test_import_nessus_vulnerability_findings_normalizes_and_persists(self):
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

            nessus_payload = json.dumps(
                {
                    "findings": [
                        {
                            "plugin_id": 12345,
                            "plugin_name": "Export endpoint tenant scoping",
                            "risk_factor": "Critical",
                            "synopsis": "Export endpoint may leak another tenant's data.",
                            "description": "Tenant scoping is not enforced before export.",
                            "plugin_output": "Observed export request completing without tenant validation.",
                            "cve": ["CVE-2026-2222"],
                            "cwe": ["CWE-639"],
                            "see_also": ["https://www.tenable.com/plugins/nessus/12345"],
                            "path": "api/src/example.py",
                            "line": 12,
                        }
                    ]
                }
            )

            imported = service.import_nessus_vulnerability_findings(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                nessus_payload,
            )
            self.assertEqual(len(imported), 1)
            finding = imported[0]
            self.assertEqual(finding.scanner, "nessus")
            self.assertEqual(finding.source, "nessus-json")
            self.assertEqual(finding.severity, "critical")
            self.assertEqual(finding.rule_id, "plugin-12345")
            self.assertEqual(finding.location_path, "api/src/example.py")
            self.assertIn("CWE-639", finding.cwe_ids)
            self.assertIn("CVE-2026-2222", finding.cve_ids)

            packet = service.build_issue_context(snapshot.workspace.workspace_id, "P0_25M03_001")
            self.assertIn("Export endpoint tenant scoping", packet.prompt)
            self.assertTrue(
                any(item.artifact_type == "vulnerability_finding" for item in (packet.dynamic_context.related_context if packet.dynamic_context else []))
            )
            self.assertTrue(any(item.source_id.startswith("vulnerability_finding:") for item in packet.retrieval_ledger))

    def test_vulnerability_import_batches_are_recorded_with_lifecycle_summaries(self):
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
            issue_id = "P0_25M03_001"

            sarif_payload = json.dumps(
                {
                    "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
                    "runs": [
                        {
                            "tool": {
                                "driver": {
                                    "name": "CodeQL",
                                    "rules": [
                                        {
                                            "id": "py/path-injection",
                                            "shortDescription": {"text": "Path injection"},
                                        }
                                    ],
                                }
                            },
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
            )

            imported = service.import_sarif_vulnerability_findings(workspace_id, issue_id, sarif_payload)
            self.assertEqual(len(imported), 1)

            first_batch = service.list_vulnerability_import_batches(workspace_id, issue_id)
            self.assertEqual(len(first_batch), 1)
            self.assertEqual(first_batch[0].source, "sarif")
            self.assertEqual(first_batch[0].scanner, "CodeQL")
            self.assertEqual(first_batch[0].finding_ids, [imported[0].finding_id])
            self.assertEqual(first_batch[0].summary_counts["new"], 1)
            self.assertEqual(first_batch[0].summary_counts["existing"], 0)

            imported_again = service.import_sarif_vulnerability_findings(workspace_id, issue_id, sarif_payload)
            self.assertEqual(imported_again[0].finding_id, imported[0].finding_id)

            batches = service.list_vulnerability_import_batches(workspace_id, issue_id)
            self.assertEqual(len(batches), 2)
            latest = batches[0]
            self.assertEqual(latest.summary_counts["new"], 0)
            self.assertEqual(latest.summary_counts["existing"], 1)
            self.assertEqual(latest.finding_ids, [imported[0].finding_id])

    def test_vulnerability_report_links_threat_models_and_renders_markdown(self):
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
            issue_id = "P0_25M03_001"

            threat_model = service.save_threat_model(
                workspace_id,
                issue_id,
                ThreatModelUpsertRequest(
                    title="Export boundary review",
                    summary="Cross-tenant export is the key trust-boundary risk.",
                    assets=["customer export data"],
                    trust_boundaries=["tenant scoped caller -> export service"],
                    abuse_cases=["Attacker exports another tenant's records"],
                    mitigations=["Enforce tenant ownership before export"],
                ),
            )

            service.save_vulnerability_finding(
                workspace_id,
                issue_id,
                VulnerabilityFindingUpsertRequest(
                    title="Tenant export authorization bypass",
                    scanner="semgrep",
                    source="semgrep-json",
                    severity="high",
                    status="triaged",
                    summary="The export path lacks tenant authorization enforcement.",
                    rule_id="python.export.authz",
                    location_path="api/src/example.py",
                    location_line=12,
                    cwe_ids=["CWE-639"],
                    evidence=["Semgrep flagged export flow without tenant check."],
                    references=["https://semgrep.dev/r/python.export.authz"],
                    threat_model_ids=[threat_model.threat_model_id],
                ),
            )

            report = service.get_vulnerability_finding_report(workspace_id, issue_id)
            self.assertEqual(report.issue_id, issue_id)
            self.assertEqual(report.total_findings, 1)
            self.assertEqual(report.by_severity["high"], 1)
            self.assertEqual(report.by_status["triaged"], 1)
            self.assertEqual(report.linked_threat_models[0].threat_model_id, threat_model.threat_model_id)
            self.assertEqual(report.findings[0].threat_model_ids, [threat_model.threat_model_id])
            self.assertEqual(report.findings[0].linked_threat_model_titles, ["Export boundary review"])

            packet = service.build_issue_context(workspace_id, issue_id)
            self.assertIn("Linked threat models: Export boundary review", packet.prompt)

            markdown = service.render_vulnerability_finding_report_markdown(workspace_id, issue_id)
            self.assertIn("# Vulnerability Report", markdown)
            self.assertIn("Tenant export authorization bypass", markdown)
            self.assertIn("Export boundary review", markdown)
            self.assertIn("CWE-639", markdown)

    def test_workspace_vulnerability_report_aggregates_across_issues(self):
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

            created = service.create_issue(
                workspace_id,
                IssueCreateRequest(
                    title="Secondary export regression",
                    severity="P1",
                    summary="Another workflow leaks export data.",
                    labels=["export"],
                ),
            )

            threat_model = service.save_threat_model(
                workspace_id,
                "P0_25M03_001",
                ThreatModelUpsertRequest(
                    title="Workspace export threat model",
                    summary="Export flows cross trust boundaries.",
                    assets=["customer export data"],
                    trust_boundaries=["tenant -> export service"],
                ),
            )

            service.save_vulnerability_finding(
                workspace_id,
                "P0_25M03_001",
                VulnerabilityFindingUpsertRequest(
                    title="Primary export bypass",
                    scanner="semgrep",
                    source="semgrep-json",
                    severity="high",
                    status="triaged",
                    summary="Primary export path lacks tenant authz.",
                    threat_model_ids=[threat_model.threat_model_id],
                ),
            )
            service.save_vulnerability_finding(
                workspace_id,
                created.bug_id,
                VulnerabilityFindingUpsertRequest(
                    title="Secondary export bypass",
                    scanner="nessus",
                    source="nessus-json",
                    severity="critical",
                    status="open",
                    summary="Second export path is exposed.",
                ),
            )

            report = service.get_workspace_vulnerability_report(workspace_id)
            self.assertEqual(report.workspace_id, workspace_id)
            self.assertEqual(report.total_findings, 2)
            self.assertEqual(report.by_severity["critical"], 1)
            self.assertEqual(report.by_severity["high"], 1)
            self.assertEqual(report.by_status["open"], 1)
            self.assertEqual(report.by_status["triaged"], 1)
            self.assertEqual(report.by_source["nessus-json"], 1)
            self.assertEqual(report.by_source["semgrep-json"], 1)
            self.assertEqual(report.linked_threat_models_total, 1)
            self.assertEqual(report.issue_rollups[0].issue_id, created.bug_id)
            self.assertEqual(report.issue_rollups[0].open_findings, 1)
            self.assertEqual(report.issue_rollups[1].issue_id, "P0_25M03_001")
            self.assertEqual(report.issue_rollups[1].linked_threat_models, 1)

            markdown = service.render_workspace_vulnerability_report_markdown(workspace_id)
            self.assertIn("# Workspace Vulnerability Report", markdown)
            self.assertIn(created.bug_id, markdown)
            self.assertIn("Workspace export threat model", markdown)

    def test_workspace_security_review_bundle_collects_top_findings_and_handoff_markdown(self):
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

            created = service.create_issue(
                workspace_id,
                IssueCreateRequest(
                    title="Background export regression",
                    severity="P1",
                    summary="A second export flow is leaking data.",
                    labels=["export", "security"],
                ),
            )
            threat_model = service.save_threat_model(
                workspace_id,
                "P0_25M03_001",
                ThreatModelUpsertRequest(
                    title="Security handoff threat model",
                    summary="Export flows cross a trust boundary.",
                    assets=["customer export data"],
                    trust_boundaries=["tenant -> export service"],
                ),
            )
            service.save_vulnerability_finding(
                workspace_id,
                "P0_25M03_001",
                VulnerabilityFindingUpsertRequest(
                    title="Primary export authz bypass",
                    scanner="semgrep",
                    source="semgrep-json",
                    severity="critical",
                    status="open",
                    summary="Primary export path lacks tenant authorization.",
                    threat_model_ids=[threat_model.threat_model_id],
                    evidence=["Missing tenant guard in export path."],
                ),
            )
            service.save_vulnerability_finding(
                workspace_id,
                created.bug_id,
                VulnerabilityFindingUpsertRequest(
                    title="Secondary export authz bypass",
                    scanner="nessus",
                    source="nessus-json",
                    severity="high",
                    status="triaged",
                    summary="Secondary export path is externally reachable.",
                    evidence=["Scanner reached export endpoint without authz."],
                ),
            )

            bundle = service.get_workspace_security_review_bundle(workspace_id)
            self.assertEqual(bundle.workspace_id, workspace_id)
            self.assertEqual(bundle.total_findings, 2)
            self.assertEqual(bundle.open_findings, 2)
            self.assertEqual(bundle.top_findings[0].title, "Primary export authz bypass")
            self.assertEqual(bundle.linked_threat_models[0].threat_model_id, threat_model.threat_model_id)
            self.assertEqual(bundle.issue_rollups[0].issue_id, "P0_25M03_001")
            self.assertTrue(bundle.recent_activity)

            markdown = service.render_workspace_security_review_bundle_markdown(workspace_id)
            self.assertIn("# Workspace Security Review Bundle", markdown)
            self.assertIn("Primary export authz bypass", markdown)
            self.assertIn("Security handoff threat model", markdown)
            self.assertIn("Recent Activity", markdown)

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

            browser_dump = service.save_browser_dump(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                BrowserDumpUpsertRequest(
                    label="Checkout dump",
                    summary="Captured while reproducing",
                    dom_snapshot="checkout screen",
                ),
            )
            replay = service.capture_issue_context_replay(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueContextReplayRequest(label="baseline"),
            )

            self.assertEqual(replay.label, "baseline")
            self.assertIn("You are fixing bug P0_25M03_001", replay.prompt)
            self.assertEqual(replay.browser_dump_ids, [browser_dump.dump_id])
            self.assertEqual(service.list_issue_context_replays(snapshot.workspace.workspace_id, "P0_25M03_001")[0].replay_id, replay.replay_id)

    def test_compare_issue_context_replay_reports_prompt_and_artifact_drift(self):
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

            saved_context = service.save_ticket_context(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                TicketContextUpsertRequest(
                    title="Customer replay delta",
                    summary="New customer evidence after the first replay.",
                ),
            )
            saved_dump = service.save_browser_dump(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                BrowserDumpUpsertRequest(
                    label="Updated checkout dump",
                    summary="State after the regression worsened.",
                    dom_snapshot="button disabled forever",
                ),
            )

            comparison = service.compare_issue_context_replay(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                replay.replay_id,
            )

            self.assertTrue(comparison.changed)
            self.assertTrue(comparison.prompt_changed)
            self.assertIn(saved_context.context_id, comparison.added_ticket_context_ids)
            self.assertIn(saved_dump.dump_id, comparison.added_browser_dump_ids)
            self.assertIn("prompt changed", comparison.summary)

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
            service.save_ticket_context(
                snapshot.workspace.workspace_id,
                created.bug_id,
                TicketContextUpsertRequest(
                    provider="manual",
                    title="Backend export acceptance",
                    acceptance_criteria=["CSV export works in backend flow"],
                ),
            )

            packet = service.build_issue_context(snapshot.workspace.workspace_id, created.bug_id)
            self.assertIsNotNone(packet.repo_map)
            self.assertIn("Structural context:", packet.prompt)
            self.assertIn("Ranked related paths:", packet.prompt)
            self.assertIn("Symbol context:", packet.prompt)
            self.assertIn("Related artifacts:", packet.prompt)
            self.assertIn("Retrieval ledger:", packet.prompt)
            self.assertTrue(any(path.startswith("backend") or path.startswith("tests") for path in packet.related_paths))
            self.assertIsNotNone(packet.dynamic_context)
            assert packet.dynamic_context is not None
            self.assertTrue(packet.dynamic_context.symbol_context)
            self.assertTrue(packet.dynamic_context.related_context)
            self.assertTrue(packet.retrieval_ledger)
            self.assertTrue(any(item.source_type in {"stored_symbol", "on_demand_symbol"} for item in packet.retrieval_ledger))
            self.assertTrue(any(item.source_type == "artifact" for item in packet.retrieval_ledger))

    def test_issue_context_includes_semantic_matches_when_ast_grep_hits(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text(
                "def render_payload():\n    return {'status': 'ok'}\n",
                encoding="utf-8",
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            created = service.create_issue(
                snapshot.workspace.workspace_id,
                IssueCreateRequest(
                    title="render_payload export regression",
                    severity="P1",
                    summary="render_payload fails during CSV export.",
                    labels=["export", "backend"],
                ),
            )

            fake_matches = [
                SemanticPatternMatchRecord(
                    path="api/src/example.py",
                    language="python",
                    line_start=1,
                    line_end=1,
                    column_start=1,
                    column_end=21,
                    matched_text="def render_payload():",
                    context_lines="def render_payload():",
                    meta_variables=[],
                )
            ]

            with patch("app.service.ast_grep_available", return_value=True):
                with patch(
                    "app.service.run_ast_grep_query",
                    return_value=(fake_matches, "/opt/homebrew/bin/sg", None, False),
                ):
                    packet = service.build_issue_context(snapshot.workspace.workspace_id, created.bug_id)

            assert packet.dynamic_context is not None
            self.assertTrue(packet.dynamic_context.semantic_matches)
            self.assertEqual(packet.dynamic_context.semantic_matches[0].path, "api/src/example.py")
            self.assertTrue(packet.dynamic_context.semantic_queries)
            self.assertTrue(packet.dynamic_context.semantic_match_rows)
            self.assertEqual(packet.dynamic_context.semantic_queries[0].source, "issue_context")
            self.assertEqual(
                packet.dynamic_context.semantic_match_rows[0].query_ref,
                packet.dynamic_context.semantic_queries[0].query_ref,
            )
            self.assertIn("Semantic matches:", packet.prompt)
            self.assertTrue(any(item.source_type == "semantic_match" for item in packet.retrieval_ledger))

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
                worktree=WorktreeStatus(
                    available=True,
                    is_git_repo=True,
                    branch="feature/run-insight",
                    dirty_files=1,
                    staged_files=0,
                    untracked_files=0,
                    ahead=0,
                    behind=0,
                    dirty_paths=["docs/notes.md"],
                ),
            )
            store.save_run(run)
            service.save_ticket_context(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                TicketContextUpsertRequest(
                    provider="manual",
                    title="Run insight acceptance",
                    acceptance_criteria=["Applied the fix", "ran pytest -q"],
                ),
            )
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
            self.assertEqual(insight["acceptance_review"]["status"], "met")
            self.assertTrue(any(item["kind"] == "unrelated_change" for item in insight["scope_warnings"]))
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

    def test_eval_scenario_report_aggregates_replays_verification_and_metrics(self):
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

            replay = service.capture_issue_context_replay(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                IssueContextReplayRequest(label="baseline replay"),
            )
            service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    profile_id="backend-pytest",
                    name="Backend pytest",
                    test_command="pytest -q",
                ),
            )
            service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    profile_id="frontend-smoke",
                    name="Frontend smoke",
                    test_command="npm run test:smoke",
                    checklist_items=["banner renders"],
                ),
            )
            service.save_ticket_context(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                TicketContextUpsertRequest(
                    context_id="ticket-2",
                    title="Alternate ticket context",
                    summary="A narrower export constraint",
                    acceptance_criteria=["Export summary is still present"],
                ),
            )
            run = RunRecord(
                run_id="eval_run_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="eval run",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "eval_run_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "eval_run_001.out.json"),
            )
            store.save_run(run)
            second_run = RunRecord(
                run_id="eval_run_002",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="opencode",
                model="gpt-5.4",
                status="failed",
                title="eval run 2",
                prompt="prompt",
                command=["opencode", "run"],
                command_preview="opencode run",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "eval_run_002.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "eval_run_002.out.json"),
            )
            store.save_run(second_run)
            metrics = service.runtime_service.calculate_run_metrics(run, len("completed eval run"))
            service.runtime_service.save_run_metrics(metrics)
            second_metrics = service.runtime_service.calculate_run_metrics(second_run, len("failed eval run"))
            service.runtime_service.save_run_metrics(second_metrics)
            store.save_verification_profile_history(
                snapshot.workspace.workspace_id,
                [
                    VerificationProfileExecutionResult(
                        profile_id="backend-pytest",
                        workspace_id=snapshot.workspace.workspace_id,
                        profile_name="Backend pytest",
                        issue_id="P0_25M03_001",
                        run_id="eval_run_001",
                        attempt_count=1,
                        success=True,
                        confidence="high",
                    ),
                    VerificationProfileExecutionResult(
                        profile_id="frontend-smoke",
                        workspace_id=snapshot.workspace.workspace_id,
                        profile_name="Frontend smoke",
                        issue_id="P0_25M03_001",
                        run_id="eval_run_002",
                        attempt_count=2,
                        success=False,
                        confidence="medium",
                        checklist_results=[
                            VerificationChecklistResult(
                                item_id="banner-renders",
                                title="banner renders",
                                passed=False,
                                details="Banner stayed hidden",
                            )
                        ],
                    ),
                ],
            )

            scenario = service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval baseline",
                    baseline_replay_id=replay.replay_id,
                    guidance_paths=["AGENTS.md", "missing.md"],
                    ticket_context_ids=["ticket-1", "ticket-missing"],
                    verification_profile_ids=["backend-pytest"],
                    run_ids=["eval_run_001"],
                ),
            )
            service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval alternate",
                    guidance_paths=["CONVENTIONS.md"],
                    ticket_context_ids=["ticket-2"],
                    verification_profile_ids=["frontend-smoke"],
                    run_ids=["eval_run_002"],
                ),
            )
            service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval duplicate baseline",
                    guidance_paths=["AGENTS.md", "missing.md"],
                    ticket_context_ids=["ticket-1", "ticket-missing"],
                    verification_profile_ids=["backend-pytest"],
                    run_ids=["eval_run_001"],
                ),
            )
            report = service.get_eval_report(snapshot.workspace.workspace_id, scenario.scenario_id)
            self.assertEqual(report.scenario_count, 1)
            self.assertEqual(report.scenario_reports[0].scenario.scenario_id, scenario.scenario_id)
            self.assertEqual(report.scenario_reports[0].success_runs, 1)
            self.assertEqual(report.scenario_reports[0].verification_success_rate, 100.0)
            self.assertIsNotNone(report.scenario_reports[0].latest_replay_comparison)
            self.assertIsNotNone(report.scenario_reports[0].variant_diff)
            self.assertIsNotNone(report.scenario_reports[0].latest_fresh_run)
            assert report.scenario_reports[0].variant_diff is not None
            self.assertTrue(report.scenario_reports[0].variant_diff.changed)
            self.assertIn("missing.md", report.scenario_reports[0].variant_diff.removed_guidance_paths)
            self.assertIn("ticket-missing", report.scenario_reports[0].variant_diff.removed_ticket_context_ids)
            full_report = service.get_eval_report(snapshot.workspace.workspace_id)
            self.assertEqual(full_report.scenario_count, 3)
            self.assertEqual(len(full_report.fresh_replay_rankings), 1)
            alternate_report = next(
                item for item in full_report.scenario_reports if item.scenario.name == "Eval alternate"
            )
            fresh_ranking = full_report.fresh_replay_rankings[0]
            self.assertEqual(fresh_ranking.issue_id, "P0_25M03_001")
            self.assertEqual(fresh_ranking.baseline_scenario_name, "Eval baseline")
            self.assertEqual(len(fresh_ranking.ranked_scenarios), 3)
            self.assertEqual(fresh_ranking.ranked_scenarios[0].scenario_name, "Eval baseline")
            self.assertEqual(fresh_ranking.ranked_scenarios[-1].scenario_name, "Eval alternate")
            self.assertEqual(fresh_ranking.ranked_scenarios[-1].pairwise_losses, 2)
            self.assertIsNotNone(alternate_report.comparison_to_baseline)
            assert alternate_report.comparison_to_baseline is not None
            self.assertEqual(alternate_report.comparison_to_baseline.compared_to_name, "Eval baseline")
            self.assertEqual(alternate_report.comparison_to_baseline.preferred, "baseline")
            self.assertIsNotNone(alternate_report.latest_fresh_run)
            self.assertIsNotNone(alternate_report.fresh_comparison_to_baseline)
            self.assertIn("CONVENTIONS.md", alternate_report.comparison_to_baseline.guidance_only_in_scenario)
            self.assertIn("AGENTS.md", alternate_report.comparison_to_baseline.guidance_only_in_baseline)
            self.assertEqual(len(alternate_report.comparison_to_baseline.verification_profile_deltas), 2)
            assert alternate_report.latest_fresh_run is not None
            assert alternate_report.fresh_comparison_to_baseline is not None
            self.assertEqual(alternate_report.latest_fresh_run.run_id, "eval_run_002")
            self.assertEqual(alternate_report.fresh_comparison_to_baseline.compared_to_name, "Eval baseline")
            frontend_delta = next(
                item
                for item in alternate_report.comparison_to_baseline.verification_profile_deltas
                if item.profile_id == "frontend-smoke"
            )
            self.assertEqual(frontend_delta.preferred, "baseline")
            self.assertEqual(frontend_delta.scenario_total_runs, 1)
            self.assertEqual(frontend_delta.baseline_total_runs, 0)
            self.assertGreaterEqual(len(full_report.guidance_variant_rollups), 2)
            self.assertGreaterEqual(len(full_report.ticket_context_variant_rollups), 2)
            guidance_rollup = next(item for item in full_report.guidance_variant_rollups if item.selected_values == ["AGENTS.md", "missing.md"])
            self.assertEqual(guidance_rollup.scenario_count, 2)
            self.assertEqual(guidance_rollup.run_count, 1)
            self.assertEqual(guidance_rollup.success_runs, 1)
            self.assertEqual(guidance_rollup.failed_runs, 0)
            self.assertEqual(guidance_rollup.verification_success_rate, 100.0)
            self.assertTrue(any(item.label == "codex" for item in guidance_rollup.runtime_breakdown))
            ticket_rollup = next(item for item in full_report.ticket_context_variant_rollups if item.selected_values == ["ticket-2"])
            self.assertEqual(ticket_rollup.failed_runs, 1)
            self.assertTrue(any(item.label == "gpt-5.4" for item in ticket_rollup.model_breakdown))

    def test_eval_report_tracks_fresh_replay_rank_movement(self):
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

            for run_id, runtime, model, status, created_at, cost, duration in [
                ("baseline_prev", "codex", "gpt-5.4-mini", "completed", "2026-04-14T10:00:00Z", 0.0100, 900),
                ("baseline_latest", "codex", "gpt-5.4-mini", "completed", "2026-04-14T10:10:00Z", 0.0500, 3000),
                ("alternate_prev", "opencode", "gpt-5.4", "failed", "2026-04-14T10:01:00Z", 0.0400, 2400),
                ("alternate_latest", "opencode", "gpt-5.4", "completed", "2026-04-14T10:11:00Z", 0.0100, 1200),
            ]:
                store.save_run(
                    RunRecord(
                        run_id=run_id,
                        workspace_id=snapshot.workspace.workspace_id,
                        issue_id="P0_25M03_001",
                        runtime=runtime,
                        model=model,
                        status=status,
                        eval_scenario_id="eval-eval-baseline" if run_id.startswith("baseline_") else "eval-eval-alternate",
                        title="eval trend",
                        prompt="prompt",
                        command=[runtime, "exec"],
                        command_preview=f"{runtime} exec",
                        log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / f"{run_id}.log"),
                        output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / f"{run_id}.out.json"),
                        created_at=created_at,
                    )
                )
                service.runtime_service.save_run_metrics(
                    RunMetrics(
                        run_id=run_id,
                        workspace_id=snapshot.workspace.workspace_id,
                        input_tokens=100,
                        output_tokens=50,
                        estimated_cost=cost,
                        duration_ms=duration,
                        model=model,
                        runtime=runtime,
                        calculated_at=created_at,
                    )
                )

            baseline_scenario = service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval baseline",
                    run_ids=["baseline_prev", "baseline_latest"],
                ),
            )
            alternate_scenario = service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval alternate",
                    run_ids=["alternate_prev", "alternate_latest"],
                ),
            )
            service.store.save_eval_replay_batches(
                snapshot.workspace.workspace_id,
                [
                    EvalReplayBatchRecord(
                        batch_id="evalbatch_prev",
                        workspace_id=snapshot.workspace.workspace_id,
                        issue_id="P0_25M03_001",
                        runtime="codex",
                        model="gpt-5.4-mini",
                        scenario_ids=[baseline_scenario.scenario_id, alternate_scenario.scenario_id],
                        queued_run_ids=["baseline_prev", "alternate_prev"],
                        created_at="2026-04-14T10:02:00Z",
                    ),
                    EvalReplayBatchRecord(
                        batch_id="evalbatch_latest",
                        workspace_id=snapshot.workspace.workspace_id,
                        issue_id="P0_25M03_001",
                        runtime="codex",
                        model="gpt-5.4-mini",
                        scenario_ids=[baseline_scenario.scenario_id, alternate_scenario.scenario_id],
                        queued_run_ids=["baseline_latest", "alternate_latest"],
                        created_at="2026-04-14T10:12:00Z",
                    ),
                ],
            )

            report = service.get_eval_report(snapshot.workspace.workspace_id)
            self.assertEqual(len(report.fresh_replay_rankings), 1)
            self.assertEqual(len(report.fresh_replay_trends), 1)
            self.assertEqual(len(report.replay_batches), 2)
            trend = report.fresh_replay_trends[0]
            self.assertEqual(trend.issue_id, "P0_25M03_001")
            self.assertEqual(trend.latest_batch_id, "evalbatch_latest")
            self.assertEqual(trend.previous_batch_id, "evalbatch_prev")
            baseline_entry = next(item for item in trend.entries if item.scenario_name == "Eval baseline")
            alternate_entry = next(item for item in trend.entries if item.scenario_name == "Eval alternate")
            self.assertEqual(baseline_entry.movement, "down")
            self.assertEqual(baseline_entry.previous_rank, 1)
            self.assertEqual(alternate_entry.movement, "up")
            self.assertEqual(alternate_entry.previous_rank, 2)

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

    def test_start_issue_run_with_eval_scenario_records_fresh_run(self):
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

            service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    profile_id="backend-pytest",
                    name="Backend pytest",
                    test_command="pytest -q",
                ),
            )
            scenario = service.save_eval_scenario(
                snapshot.workspace.workspace_id,
                EvalScenarioUpsertRequest(
                    issue_id="P0_25M03_001",
                    name="Eval fresh execution",
                    guidance_paths=["AGENTS.md"],
                    verification_profile_ids=["backend-pytest"],
                ),
            )
            queued_run = RunRecord(
                run_id="run_eval_exec_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5-codex",
                status="queued",
                title="codex:P0_25M03_001",
                prompt="placeholder",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_eval_exec_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_eval_exec_001.out.json"),
                eval_scenario_id=scenario.scenario_id,
            )

            with patch.object(service.runtime_service, "validate_runtime_model"):
                with patch.object(service.runtime_service, "start_issue_run", return_value=queued_run) as start_mock:
                    payload = service.start_issue_run(
                        snapshot.workspace.workspace_id,
                        "P0_25M03_001",
                        "codex",
                        "gpt-5-codex",
                        "focus on export regression",
                        eval_scenario_id=scenario.scenario_id,
                    )

            self.assertEqual(payload["eval_scenario_id"], scenario.scenario_id)
            prompt = start_mock.call_args.kwargs["prompt"]
            self.assertIn("Evaluation scenario: Eval fresh execution", prompt)
            self.assertIn("focus on export regression", prompt)
            updated = next(
                item
                for item in service.list_eval_scenarios(snapshot.workspace.workspace_id, issue_id="P0_25M03_001")
                if item.scenario_id == scenario.scenario_id
            )
            self.assertIn("run_eval_exec_001", updated.run_ids)

    def test_repo_tool_surfaces_cover_state_targets_changes_and_explainer(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "frontend").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            source_file = root / "api" / "src" / "example.py"
            source_file.write_text("import json\n\ndef render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "package.json").write_text(
                json.dumps({"name": "fixture", "scripts": {"dev": "vite", "build": "vite build", "test": "vitest run"}}),
                encoding="utf-8",
            )
            (root / "Makefile").write_text("backend:\n\tpython3 -m uvicorn app.main:app\nlint:\n\techo lint\n", encoding="utf-8")
            (root / "docker-compose.yml").write_text("services:\n  api:\n    image: example\n", encoding="utf-8")

            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)
            __import__("subprocess").run(
                [
                    "git",
                    "-C",
                    str(root),
                    "-c",
                    "user.name=xmustard",
                    "-c",
                    "user.email=xmustard@example.com",
                    "commit",
                    "-m",
                    "initial",
                ],
                check=True,
                capture_output=True,
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.save_verification_profile(
                snapshot.workspace.workspace_id,
                VerificationProfileUpsertRequest(
                    profile_id="backend-pytest",
                    name="Backend pytest",
                    test_command="pytest -q",
                ),
            )

            source_file.write_text("import json\n\ndef render_payload():\n    return {'status': 'changed'}\n", encoding="utf-8")

            repo_state = service.read_repo_tool_state(snapshot.workspace.workspace_id)
            self.assertEqual(repo_state.workspace.workspace_id, snapshot.workspace.workspace_id)
            self.assertIn("issues_total", repo_state.snapshot_summary)

            run_targets = service.list_run_targets(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.command == "npm run dev" for item in run_targets))
            self.assertTrue(any(item.command == "make backend" for item in run_targets))

            verify_targets = service.list_verify_targets(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.command == "pytest -q" for item in verify_targets))
            self.assertTrue(any(item.command == "npm run test" for item in verify_targets))

            changes = service.read_change_summary(snapshot.workspace.workspace_id)
            self.assertTrue(any(item.path == "api/src/example.py" for item in changes.changed_files))

            explained = service.explain_path(snapshot.workspace.workspace_id, "api/src/example.py")
            self.assertEqual(explained.role, "source")
            self.assertIn("render_payload", explained.detected_symbols)
            self.assertIn("api/src/example.py", explained.summary)

    def test_postgres_schema_plan_and_bootstrap_follow_settings(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "main.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            plan = service.read_postgres_schema_plan()
            self.assertTrue(plan.configured)
            self.assertEqual(plan.schema_name, "agent_context")
            self.assertIn("workspaces", plan.table_names)
            self.assertIn("symbols", plan.table_names)
            self.assertIn("semantic_matches", plan.semantic_table_names)
            self.assertIn("run_plans", plan.ops_memory_table_names)
            self.assertIn("run_plans", plan.search_document_tables)
            self.assertGreater(plan.statement_count, 10)

            rendered_sql = service.render_postgres_schema_sql()
            self.assertIn("create schema if not exists agent_context;", rendered_sql.lower())
            self.assertIn("agent_context.workspaces", rendered_sql)
            self.assertIn("agent_context.semantic_matches", rendered_sql)
            self.assertIn("agent_context.run_plans", rendered_sql)

            with patch("app.service.bootstrap_schema") as bootstrap_mock:
                bootstrap_mock.return_value = PostgresBootstrapResult(
                    applied=True,
                    dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                    schema_name="agent_context",
                    sql_path="/tmp/schema.sql",
                    statement_count=12,
                    message="Applied repo cockpit foundation schema to Postgres schema 'agent_context'.",
                )
                result = service.bootstrap_postgres_schema()
                self.assertTrue(result.applied)
                bootstrap_mock.assert_called_once()
                _, used_dsn, used_schema = bootstrap_mock.call_args.args
                self.assertEqual(used_dsn, "postgresql://xmustard:secret@localhost:5432/xmustard")
                self.assertEqual(used_schema, "agent_context")

    def test_postgres_materialization_surfaces_follow_settings(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            file_summary_row = FileSymbolSummaryMaterializationRecord(
                workspace_id=snapshot.workspace.workspace_id,
                path="api/src/example.py",
                language="python",
                parser_language="python",
                symbol_source="tree_sitter",
                symbol_count=1,
                summary_json={"top_symbols": ["render_payload"]},
            )
            symbol_rows = [
                SymbolMaterializationRecord(
                    workspace_id=snapshot.workspace.workspace_id,
                    path="api/src/example.py",
                    symbol="render_payload",
                    kind="function",
                    language="python",
                    line_start=1,
                    line_end=2,
                )
            ]
            with patch.object(
                service,
                "read_path_symbols",
                return_value=PathSymbolsResult(
                    workspace_id=snapshot.workspace.workspace_id,
                    path="api/src/example.py",
                    symbol_source="tree_sitter",
                    parser_language="python",
                    symbols=[],
                    file_summary_row=file_summary_row,
                    symbol_rows=symbol_rows,
                ),
            ):
                with patch("app.service.materialize_path_symbols") as path_materialize_mock:
                    path_materialize_mock.return_value = PostgresSemanticMaterializationResult(
                        applied=True,
                        dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                        schema_name="agent_context",
                        workspace_id=snapshot.workspace.workspace_id,
                        source="path_symbols",
                        target="api/src/example.py",
                        materialized_paths=["api/src/example.py"],
                        file_rows=1,
                        symbol_rows=1,
                        summary_rows=1,
                        message="ok",
                    )
                    result = service.materialize_path_symbols_to_postgres(
                        snapshot.workspace.workspace_id,
                        "api/src/example.py",
                    )
                    self.assertTrue(result.applied)
                    used_dsn, used_schema = path_materialize_mock.call_args.args[:2]
                    self.assertEqual(used_dsn, "postgresql://xmustard:secret@localhost:5432/xmustard")
                    self.assertEqual(used_schema, "agent_context")

            query_row = SemanticQueryMaterializationRecord(
                query_ref="semanticq_fixture",
                workspace_id=snapshot.workspace.workspace_id,
                source="adhoc_tool",
                pattern="def $A():",
                language="python",
                path_glob="api/src/**/*.py",
                engine="ast_grep",
                match_count=1,
            )
            match_rows = [
                SemanticMatchMaterializationRecord(
                    query_ref="semanticq_fixture",
                    workspace_id=snapshot.workspace.workspace_id,
                    path="api/src/example.py",
                    language="python",
                    line_start=1,
                    line_end=1,
                    column_start=1,
                    column_end=19,
                    matched_text="def render_payload():",
                )
            ]
            with patch.object(
                service,
                "search_semantic_pattern",
                return_value=SemanticPatternQueryResult(
                    workspace_id=snapshot.workspace.workspace_id,
                    pattern="def $A():",
                    language="python",
                    path_glob="api/src/**/*.py",
                    engine="ast_grep",
                    binary_path="/opt/homebrew/bin/sg",
                    match_count=1,
                    matches=[],
                    query_row=query_row,
                    match_rows=match_rows,
                ),
            ):
                with patch("app.service.materialize_semantic_search") as search_materialize_mock:
                    search_materialize_mock.return_value = PostgresSemanticMaterializationResult(
                        applied=True,
                        dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                        schema_name="agent_context",
                        workspace_id=snapshot.workspace.workspace_id,
                        source="semantic_search",
                        target="def $A():",
                        materialized_paths=["api/src/example.py"],
                        file_rows=1,
                        query_rows=1,
                        match_rows=1,
                        message="ok",
                    )
                    result = service.materialize_semantic_search_to_postgres(
                        snapshot.workspace.workspace_id,
                        "def $A():",
                        language="python",
                        path_glob="api/src/**/*.py",
                    )
                    self.assertTrue(result.applied)
                    used_dsn, used_schema = search_materialize_mock.call_args.args[:2]
                    self.assertEqual(used_dsn, "postgresql://xmustard:secret@localhost:5432/xmustard")
                    self.assertEqual(used_schema, "agent_context")

    def test_workspace_symbol_materialization_batches_key_files(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "api" / "src" / "worker.py").write_text("def run_worker():\n    return True\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            with patch.object(service, "materialize_path_symbols_to_postgres") as materialize_path_mock:
                materialize_path_mock.side_effect = [
                    PostgresSemanticMaterializationResult(
                        applied=True,
                        dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                        schema_name="agent_context",
                        workspace_id=snapshot.workspace.workspace_id,
                        source="path_symbols",
                        target="api/src/example.py",
                        materialized_paths=["api/src/example.py"],
                        file_rows=1,
                        symbol_rows=1,
                        summary_rows=1,
                        message="ok",
                    ),
                    PostgresSemanticMaterializationResult(
                        applied=True,
                        dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                        schema_name="agent_context",
                        workspace_id=snapshot.workspace.workspace_id,
                        source="path_symbols",
                        target="api/src/worker.py",
                        materialized_paths=["api/src/worker.py"],
                        file_rows=1,
                        symbol_rows=1,
                        summary_rows=1,
                        message="ok",
                    ),
                ]
                result = service.materialize_workspace_symbols_to_postgres(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py", "api/src/worker.py"],
                    limit=10,
                )

            self.assertTrue(result.applied)
            self.assertEqual(result.strategy, "paths")
            self.assertEqual(result.materialized_paths, ["api/src/example.py", "api/src/worker.py"])
            self.assertEqual(result.symbol_rows, 2)
            self.assertEqual(materialize_path_mock.call_count, 2)
            first_call = materialize_path_mock.call_args_list[0]
            self.assertEqual(first_call.args[:2], (snapshot.workspace.workspace_id, "api/src/example.py"))
            self.assertEqual(first_call.kwargs["dsn"], "postgresql://xmustard:secret@localhost:5432/xmustard")
            self.assertEqual(first_call.kwargs["schema"], "agent_context")
            self.assertFalse(first_call.kwargs["record_activity"])

    def test_semantic_index_plan_prefers_cli_surface_candidates(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "backend" / "app").mkdir(parents=True)
            (root / "frontend" / "src").mkdir(parents=True)
            (root / "backend" / "app" / "cli.py").write_text("def run_cli():\n    return True\n", encoding="utf-8")
            (root / "backend" / "app" / "service.py").write_text("def serve():\n    return True\n", encoding="utf-8")
            (root / "frontend" / "src" / "App.tsx").write_text("export function App() { return null }\n", encoding="utf-8")
            (root / "package.json").write_text('{"scripts":{"dev":"vite","cli":"python -m backend.app.cli"}}\n', encoding="utf-8")
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            plan = service.plan_semantic_index(snapshot.workspace.workspace_id, surface="cli", limit=3)

            self.assertEqual(plan.surface, "cli")
            self.assertIn("backend/app/cli.py", plan.selected_paths)
            cli_detail = next(item for item in plan.selected_path_details if item.path == "backend/app/cli.py")
            self.assertIn("CLI/runtime surface markers", cli_detail.reason)
            self.assertIsNotNone(cli_detail.sha256)
            self.assertIsNotNone(plan.index_fingerprint)
            self.assertTrue(plan.postgres_configured)
            self.assertGreaterEqual(plan.run_target_count, 1)
            self.assertTrue(any(item.source_type == "semantic_index_path" for item in plan.retrieval_ledger))

    def test_semantic_index_run_dry_run_returns_plan_without_materializing(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            with patch.object(service, "materialize_workspace_symbols_to_postgres") as materialize_mock:
                result = service.run_semantic_index(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py"],
                    dry_run=True,
                    dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                )

            self.assertTrue(result.dry_run)
            self.assertEqual(result.plan.selected_paths, ["api/src/example.py"])
            materialize_mock.assert_not_called()

    def test_semantic_index_run_persists_postgres_baseline_metadata(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            materialization = PostgresWorkspaceSemanticMaterializationResult(
                applied=True,
                dsn_redacted="postgresql://xmustard:***@localhost:5432/xmustard",
                schema_name="agent_context",
                workspace_id=snapshot.workspace.workspace_id,
                strategy="paths",
                requested_paths=["api/src/example.py"],
                materialized_paths=["api/src/example.py"],
                file_rows=1,
                symbol_rows=1,
                summary_rows=1,
                message="ok",
            )
            with patch.object(service, "materialize_workspace_symbols_to_postgres", return_value=materialization):
                with patch("app.service.persist_semantic_index_baseline") as persist_mock:
                    persist_mock.side_effect = lambda *args, **kwargs: kwargs["baseline"]
                    result = service.run_semantic_index(
                        snapshot.workspace.workspace_id,
                        strategy="paths",
                        paths=["api/src/example.py"],
                        dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                        schema="agent_context",
                    )

            self.assertFalse(result.dry_run)
            self.assertEqual(result.materialization.symbol_rows, 1)
            persist_mock.assert_called_once()
            baseline = persist_mock.call_args.kwargs["baseline"]
            self.assertEqual(baseline.workspace_id, snapshot.workspace.workspace_id)
            self.assertEqual(baseline.surface, "cli")
            self.assertEqual(baseline.strategy, "paths")
            self.assertEqual(baseline.selected_paths, ["api/src/example.py"])
            self.assertEqual(baseline.materialized_paths, ["api/src/example.py"])
            self.assertEqual(baseline.symbol_rows, 1)
            self.assertEqual(baseline.index_fingerprint, result.plan.index_fingerprint)

    def test_semantic_index_status_reports_no_baseline(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            with patch("app.service.read_latest_semantic_index_baseline", return_value=None):
                status = service.read_semantic_index_status(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py"],
                    dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                    schema="agent_context",
                )

            self.assertEqual(status.status, "no_baseline")
            self.assertTrue(status.postgres_configured)
            self.assertIn("No stored semantic index baseline", status.stale_reasons[0])

    def test_semantic_index_status_detects_fresh_and_stale_fingerprint(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            indexed_path = root / "api" / "src" / "example.py"
            indexed_path.write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            plan = service.plan_semantic_index(
                snapshot.workspace.workspace_id,
                strategy="paths",
                paths=["api/src/example.py"],
                dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
            )
            baseline = SemanticIndexBaselineRecord(
                index_run_id="semidx_fixture",
                workspace_id=snapshot.workspace.workspace_id,
                surface="cli",
                strategy="paths",
                index_fingerprint=plan.index_fingerprint,
                head_sha=plan.head_sha,
                selected_paths=plan.selected_paths,
                selected_path_details=plan.selected_path_details,
                materialized_paths=plan.selected_paths,
                postgres_schema="agent_context",
            )

            with patch("app.service.read_latest_semantic_index_baseline", return_value=baseline):
                fresh = service.read_semantic_index_status(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py"],
                    dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                    schema="agent_context",
                )
            self.assertEqual(fresh.status, "fresh")
            self.assertTrue(fresh.fingerprint_match)

            indexed_path.write_text("def render_payload():\n    return False\n", encoding="utf-8")
            with patch("app.service.read_latest_semantic_index_baseline", return_value=baseline):
                stale = service.read_semantic_index_status(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py"],
                    dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                    schema="agent_context",
                )
            self.assertEqual(stale.status, "stale")
            self.assertFalse(stale.fingerprint_match)
            self.assertIn("Selected path hashes", " ".join(stale.stale_reasons))

    def test_semantic_index_status_reports_dirty_provisional_when_baseline_matches(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)
            __import__("subprocess").run(
                [
                    "git",
                    "-C",
                    str(root),
                    "-c",
                    "user.name=xmustard",
                    "-c",
                    "user.email=xmustard@example.com",
                    "commit",
                    "-m",
                    "fixture",
                ],
                check=True,
                capture_output=True,
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            plan = service.plan_semantic_index(
                snapshot.workspace.workspace_id,
                strategy="paths",
                paths=["api/src/example.py"],
                dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
            )
            baseline = SemanticIndexBaselineRecord(
                index_run_id="semidx_fixture",
                workspace_id=snapshot.workspace.workspace_id,
                surface="cli",
                strategy="paths",
                index_fingerprint=plan.index_fingerprint,
                head_sha=plan.head_sha,
                selected_paths=plan.selected_paths,
                selected_path_details=plan.selected_path_details,
                materialized_paths=plan.selected_paths,
                postgres_schema="agent_context",
            )
            (root / "scratch.py").write_text("print('dirty')\n", encoding="utf-8")

            with patch("app.service.read_latest_semantic_index_baseline", return_value=baseline):
                status = service.read_semantic_index_status(
                    snapshot.workspace.workspace_id,
                    strategy="paths",
                    paths=["api/src/example.py"],
                    dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
                    schema="agent_context",
                )

            self.assertEqual(status.status, "dirty_provisional")
            self.assertTrue(status.fingerprint_match)
            self.assertGreater(status.current_dirty_files, 0)

    def test_path_symbols_falls_back_with_semantic_warning_when_status_is_blocked(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            with patch.object(
                service,
                "read_semantic_index_status",
                return_value=SemanticIndexStatus(
                    workspace_id=snapshot.workspace.workspace_id,
                    surface="cli",
                    status="blocked",
                    stale_reasons=["Postgres DSN is not configured; no semantic baseline can be read."],
                ),
            ):
                result = service.read_path_symbols(snapshot.workspace.workspace_id, "api/src/example.py")

            self.assertEqual(result.evidence_source, "on_demand_parser")
            self.assertIn("blocked", result.selection_reason.lower())
            self.assertTrue(any("Postgres DSN is not configured" in item for item in result.warnings))

    def test_path_symbols_uses_stored_rows_when_semantic_status_is_fresh(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            service.update_settings(
                service.get_settings().model_copy(
                    update={"postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard"}
                )
            )

            with patch.object(
                service,
                "read_semantic_index_status",
                return_value=SemanticIndexStatus(
                    workspace_id=snapshot.workspace.workspace_id,
                    surface="cli",
                    status="fresh",
                ),
            ):
                with patch(
                    "app.service.read_file_symbol_summary",
                    return_value=FileSymbolSummaryMaterializationRecord(
                        workspace_id=snapshot.workspace.workspace_id,
                        path="api/src/example.py",
                        language="python",
                        parser_language="python",
                        symbol_source="tree_sitter",
                        symbol_count=1,
                        summary_json={"top_symbols": ["render_payload"]},
                    ),
                ):
                    with patch(
                        "app.service.read_symbols_for_path",
                        return_value=[
                            SymbolMaterializationRecord(
                                workspace_id=snapshot.workspace.workspace_id,
                                path="api/src/example.py",
                                symbol="render_payload",
                                kind="function",
                                language="python",
                                line_start=1,
                                line_end=2,
                            )
                        ],
                    ):
                        result = service.read_path_symbols(snapshot.workspace.workspace_id, "api/src/example.py")

            self.assertEqual(result.evidence_source, "stored_semantic")
            self.assertEqual(result.semantic_status.status, "fresh")
            self.assertIn("stored semantic symbol rows", result.selection_reason.lower())
            self.assertEqual(result.symbols[0].evidence_source, "stored_semantic")

    def test_issue_context_reports_semantic_freshness_and_stored_symbol_provenance(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            created = service.create_issue(
                snapshot.workspace.workspace_id,
                IssueCreateRequest(
                    title="render_payload regression",
                    severity="P1",
                    summary="render_payload fails during export in api/src/example.py.",
                    labels=["export"],
                ),
            )
            service.update_settings(
                service.get_settings().model_copy(
                    update={"postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard"}
                )
            )

            with patch.object(
                service,
                "read_semantic_index_status",
                return_value=SemanticIndexStatus(
                    workspace_id=snapshot.workspace.workspace_id,
                    surface="cli",
                    status="fresh",
                ),
            ):
                with patch(
                    "app.service.read_file_symbol_summary",
                    return_value=FileSymbolSummaryMaterializationRecord(
                        workspace_id=snapshot.workspace.workspace_id,
                        path="api/src/example.py",
                        language="python",
                        parser_language="python",
                        symbol_source="tree_sitter",
                        symbol_count=1,
                        summary_json={"top_symbols": ["render_payload"]},
                    ),
                ):
                    with patch(
                        "app.service.read_symbols_for_path",
                        return_value=[
                            SymbolMaterializationRecord(
                                workspace_id=snapshot.workspace.workspace_id,
                                path="api/src/example.py",
                                symbol="render_payload",
                                kind="function",
                                language="python",
                                line_start=1,
                                line_end=2,
                            )
                        ],
                    ):
                        packet = service.build_issue_context(snapshot.workspace.workspace_id, created.bug_id)

            self.assertIsNotNone(packet.semantic_status)
            self.assertEqual(packet.semantic_status.status, "fresh")
            self.assertIn("Semantic freshness:", packet.prompt)

    def test_impact_and_repo_context_surface_changed_symbols_tests_and_plan_links(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "tests").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            source_path = root / "api" / "src" / "example.py"
            source_path.write_text("def render_payload():\n    return True\n", encoding="utf-8")
            (root / "tests" / "test_render_payload.py").write_text(
                "from api.src.example import render_payload\n\ndef test_render_payload():\n    assert render_payload()\n",
                encoding="utf-8",
            )
            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)
            __import__("subprocess").run(
                [
                    "git",
                    "-C",
                    str(root),
                    "-c",
                    "user.name=xmustard",
                    "-c",
                    "user.email=xmustard@example.com",
                    "commit",
                    "-m",
                    "fixture",
                ],
                check=True,
                capture_output=True,
            )
            source_path.write_text("def render_payload():\n    return False\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_ctx_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="planning",
                title="context fixture",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_ctx_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_ctx_001.out"),
                plan=RunPlan(
                    plan_id="plan_ctx_001",
                    run_id="run_ctx_001",
                    phase="awaiting_approval",
                    steps=[
                        PlanStep(
                            step_id="step_1",
                            description="Update render payload path",
                            files_affected=["api/src/example.py"],
                        )
                    ],
                    summary="Touch the changed API file",
                    ownership_mode="shared",
                    owner_label="operator",
                    attached_files=[PlanFileAttachment(path="api/src/example.py", source="manual", exists=True)],
                ),
            )
            store.save_run(run)
            service.record_fix(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                FixRecordRequest(
                    summary="Updated render payload path",
                    changed_files=["api/src/example.py"],
                    tests_run=["pytest -q tests/test_render_payload.py"],
                ),
            )

            impact = service.read_impact(snapshot.workspace.workspace_id)
            repo_context = service.read_repo_context(snapshot.workspace.workspace_id)

            self.assertTrue(any(item.symbol == "render_payload" for item in impact.changed_symbols))
            self.assertTrue(any(item.path == "tests/test_render_payload.py" for item in impact.likely_affected_tests))
            self.assertIn("changed file", impact.derivation_summary)
            self.assertIn(impact.confidence, {"medium", "high"})
            self.assertTrue(repo_context.plan_links)
            self.assertIsNotNone(repo_context.latest_accepted_fix)
            self.assertTrue(repo_context.retrieval_ledger)
            self.assertTrue(any(item.source_type in {"lexical_hit", "structural_hit"} for item in repo_context.retrieval_ledger))

            retrieval = service.search_retrieval(snapshot.workspace.workspace_id, "render payload", limit=5)
            self.assertTrue(retrieval.hits)
            self.assertTrue(retrieval.retrieval_ledger)

    def test_delta_commands_use_last_run_and_accepted_fix_heads(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            source_path = root / "api" / "src" / "example.py"
            source_path.write_text("def render_payload():\n    return True\n", encoding="utf-8")
            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)
            __import__("subprocess").run(
                [
                    "git",
                    "-C",
                    str(root),
                    "-c",
                    "user.name=xmustard",
                    "-c",
                    "user.email=xmustard@example.com",
                    "commit",
                    "-m",
                    "fixture",
                ],
                check=True,
                capture_output=True,
            )
            head_sha = __import__("subprocess").check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            run = RunRecord(
                run_id="run_delta_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="completed",
                title="delta fixture",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_delta_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_delta_001.out"),
                worktree=WorktreeStatus(available=True, is_git_repo=True, head_sha=head_sha),
            )
            store.save_run(run)
            service.record_fix(
                snapshot.workspace.workspace_id,
                "P0_25M03_001",
                FixRecordRequest(summary="Accepted delta fixture", run_id=run.run_id, changed_files=["api/src/example.py"]),
            )
            source_path.write_text("def render_payload():\n    return False\n", encoding="utf-8")

            since_run = service.read_changes_since_last_run(snapshot.workspace.workspace_id)
            since_fix = service.read_changes_since_last_accepted_fix(snapshot.workspace.workspace_id)

            self.assertEqual(since_run.base_ref, head_sha)
            self.assertEqual(since_fix.base_ref, head_sha)
            self.assertTrue(any(item.path == "api/src/example.py" for item in since_run.changed_files))

    def test_run_plan_tracking_captures_versions_files_and_ownership(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs").mkdir(exist_ok=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("print('ok')\n", encoding="utf-8")
            (root / "README.md").write_text("# fixture\n", encoding="utf-8")
            (root / "docs" / "plan-note.md").write_text("tracked note\n", encoding="utf-8")

            __import__("subprocess").run(["git", "-C", str(root), "init"], check=True, capture_output=True)
            __import__("subprocess").run(["git", "-C", str(root), "add", "."], check=True, capture_output=True)
            __import__("subprocess").run(
                [
                    "git",
                    "-C",
                    str(root),
                    "-c",
                    "user.name=xmustard",
                    "-c",
                    "user.email=xmustard@example.com",
                    "commit",
                    "-m",
                    "initial",
                ],
                check=True,
                capture_output=True,
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            run = RunRecord(
                run_id="run_plan_tracking_001",
                workspace_id=snapshot.workspace.workspace_id,
                issue_id="P0_25M03_001",
                runtime="codex",
                model="gpt-5.4-mini",
                status="queued",
                title="codex:P0_25M03_001",
                prompt="prompt",
                command=["codex", "exec"],
                command_preview="codex exec",
                log_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_plan_tracking_001.log"),
                output_path=str(store.runs_dir(snapshot.workspace.workspace_id) / "run_plan_tracking_001.out.json"),
            )
            store.save_run(run)

            with patch.object(
                service,
                "_call_agent_for_plan",
                return_value={
                    "summary": "Patch the API and document the behavior.",
                    "reasoning": "The bug is isolated to the API response path.",
                    "steps": [
                        {
                            "step_id": "step_1",
                            "description": "Update the API response helper.",
                            "estimated_impact": "medium",
                            "files_affected": ["api/src/example.py", "README.md"],
                            "risks": ["Potential response shape drift"],
                        }
                    ],
                },
            ):
                generated = service.generate_run_plan(snapshot.workspace.workspace_id, run.run_id)

            self.assertEqual(generated["version"], 1)
            self.assertEqual(generated["ownership_mode"], "agent")
            self.assertEqual(len(generated["revisions"]), 1)
            self.assertTrue(any(item["path"] == "api/src/example.py" for item in generated["attached_files"]))
            self.assertIsNotNone(generated["head_sha"])

            tracked = service.update_run_plan_tracking(
                snapshot.workspace.workspace_id,
                run.run_id,
                PlanTrackingUpdateRequest(
                    ownership_mode="shared",
                    owner_label="mustard team",
                    attached_files=["docs/plan-note.md"],
                    feedback="Operator claimed the plan and attached the note.",
                ),
            )
            self.assertEqual(tracked["version"], 2)
            self.assertEqual(tracked["ownership_mode"], "shared")
            self.assertEqual(tracked["owner_label"], "mustard team")
            self.assertEqual(len(tracked["revisions"]), 2)
            self.assertTrue(any(item["path"] == "docs/plan-note.md" for item in tracked["attached_files"]))

            with patch.object(service.runtime_service, "start_approved_run") as start_approved_run:
                approved = service.approve_run_plan(
                    snapshot.workspace.workspace_id,
                    run.run_id,
                    PlanApproveRequest(feedback="Looks good."),
                )
            self.assertEqual(approved["phase"], "approved")
            self.assertEqual(approved["version"], 3)
            self.assertEqual(len(approved["revisions"]), 3)
            start_approved_run.assert_called_once()

    def test_ingestion_plan_reports_completed_ready_and_blocked_phases(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "package.json").write_text(
                json.dumps({"name": "fixture", "scripts": {"dev": "vite", "test": "vitest run"}}),
                encoding="utf-8",
            )
            (root / "Makefile").write_text("backend:\n\tpython3 -m uvicorn app.main:app\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )

            with patch("app.service.tree_sitter_available", return_value=True):
                with patch("app.service.shutil.which", return_value="/opt/homebrew/bin/sg"):
                    plan = service.read_ingestion_plan(snapshot.workspace.workspace_id)

            self.assertEqual(plan.completed_phase_count, 3)
            self.assertEqual(plan.next_phase_id, "tree_sitter_index")
            self.assertIn("tree_sitter_index", plan.ready_phase_ids)
            self.assertIn("ast_grep_rules", plan.ready_phase_ids)
            self.assertIn("lsp_enrichment", plan.blocked_phase_ids)
            self.assertIn("search_materialization", plan.blocked_phase_ids)

            phases = {item.phase_id: item for item in plan.phases}
            self.assertEqual(phases["repo_scan"].delivery_state, "complete")
            self.assertEqual(phases["repo_map"].delivery_state, "complete")
            self.assertEqual(phases["runtime_discovery"].delivery_state, "complete")
            self.assertEqual(phases["tree_sitter_index"].implementation_state, "partial")
            self.assertEqual(phases["tree_sitter_index"].delivery_state, "ready")
            self.assertEqual(phases["ast_grep_rules"].implementation_state, "partial")
            self.assertEqual(phases["ast_grep_rules"].delivery_state, "ready")
            self.assertEqual(phases["lsp_enrichment"].implementation_state, "planned")
            self.assertTrue(any("On-demand parser-backed symbol extraction" in item for item in phases["tree_sitter_index"].evidence))
            self.assertTrue(any(item.startswith("total_files=") for item in phases["repo_map"].evidence))

    def test_path_symbols_and_explainer_use_tree_sitter_records(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text(
                "class ApiHandler:\n    def render_payload(self):\n        return {'status': 'ok'}\n",
                encoding="utf-8",
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            fake_symbols = [
                RepoMapSymbolRecord(path="api/src/example.py", symbol="ApiHandler", kind="class", line_start=1, line_end=3),
                RepoMapSymbolRecord(
                    path="api/src/example.py",
                    symbol="render_payload",
                    kind="method",
                    line_start=2,
                    line_end=3,
                    enclosing_scope="ApiHandler",
                ),
            ]

            with patch("app.service.extract_path_symbols", return_value=(fake_symbols, "tree_sitter", "python")):
                result = service.read_path_symbols(snapshot.workspace.workspace_id, "api/src/example.py")
                explained = service.explain_path(snapshot.workspace.workspace_id, "api/src/example.py")

            self.assertEqual(result.symbol_source, "tree_sitter")
            self.assertEqual(result.parser_language, "python")
            self.assertEqual([item.symbol for item in result.symbols], ["ApiHandler", "render_payload"])
            self.assertEqual(result.symbols[1].enclosing_scope, "ApiHandler")
            self.assertIsNotNone(result.file_summary_row)
            assert result.file_summary_row is not None
            self.assertEqual(result.file_summary_row.symbol_source, "tree_sitter")
            self.assertEqual(result.file_summary_row.summary_json["top_symbols"], ["ApiHandler", "render_payload"])
            self.assertEqual([item.symbol for item in result.symbol_rows], ["ApiHandler", "render_payload"])
            self.assertEqual(explained.symbol_source, "tree_sitter")
            self.assertEqual(explained.parser_language, "python")
            self.assertIn("render_payload", explained.detected_symbols)

    def test_path_symbols_prefers_fresh_stored_postgres_rows(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            indexed_path = root / "api" / "src" / "example.py"
            indexed_path.write_text("def render_payload():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )
            plan = service.plan_semantic_index(
                snapshot.workspace.workspace_id,
                strategy="paths",
                paths=["api/src/example.py"],
                dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
            )
            baseline = SemanticIndexBaselineRecord(
                index_run_id="semidx_fixture",
                workspace_id=snapshot.workspace.workspace_id,
                surface="cli",
                strategy="paths",
                index_fingerprint=plan.index_fingerprint,
                head_sha=plan.head_sha,
                selected_paths=plan.selected_paths,
                selected_path_details=plan.selected_path_details,
                materialized_paths=plan.selected_paths,
                postgres_schema="agent_context",
            )
            stored_summary = FileSymbolSummaryMaterializationRecord(
                workspace_id=snapshot.workspace.workspace_id,
                path="api/src/example.py",
                language="python",
                parser_language="python",
                symbol_source="tree_sitter",
                symbol_count=1,
                summary_json={"top_symbols": ["stored_symbol"]},
            )
            stored_symbols = [
                SymbolMaterializationRecord(
                    workspace_id=snapshot.workspace.workspace_id,
                    path="api/src/example.py",
                    symbol="stored_symbol",
                    kind="function",
                    language="python",
                    line_start=1,
                    line_end=2,
                )
            ]

            with patch("app.service.read_latest_semantic_index_baseline", return_value=baseline):
                with patch("app.service.read_file_symbol_summary", return_value=stored_summary):
                    with patch("app.service.read_symbols_for_path", return_value=stored_symbols):
                        with patch("app.service.extract_path_symbols") as extract_mock:
                            result = service.read_path_symbols(snapshot.workspace.workspace_id, "api/src/example.py")

            self.assertEqual([item.symbol for item in result.symbols], ["stored_symbol"])
            self.assertEqual(result.symbol_source, "tree_sitter")
            self.assertEqual(result.file_summary_row, stored_summary)
            extract_mock.assert_not_called()

    def test_code_explainer_prefers_fresh_stored_postgres_symbols(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "api" / "src" / "example.py").write_text("def local_symbol():\n    return {'status': 'ok'}\n", encoding="utf-8")
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None
            service.update_settings(
                service.get_settings().model_copy(
                    update={
                        "postgres_dsn": "postgresql://xmustard:secret@localhost:5432/xmustard",
                        "postgres_schema": "agent_context",
                    }
                )
            )
            plan = service.plan_semantic_index(
                snapshot.workspace.workspace_id,
                strategy="paths",
                paths=["api/src/example.py"],
                dsn="postgresql://xmustard:secret@localhost:5432/xmustard",
            )
            baseline = SemanticIndexBaselineRecord(
                index_run_id="semidx_fixture",
                workspace_id=snapshot.workspace.workspace_id,
                surface="cli",
                strategy="paths",
                index_fingerprint=plan.index_fingerprint,
                head_sha=plan.head_sha,
                selected_paths=plan.selected_paths,
                selected_path_details=plan.selected_path_details,
                materialized_paths=plan.selected_paths,
                postgres_schema="agent_context",
            )
            stored_summary = FileSymbolSummaryMaterializationRecord(
                workspace_id=snapshot.workspace.workspace_id,
                path="api/src/example.py",
                language="python",
                parser_language="python",
                symbol_source="tree_sitter",
                symbol_count=1,
                summary_json={"top_symbols": ["stored_symbol"]},
            )
            stored_symbols = [
                SymbolMaterializationRecord(
                    workspace_id=snapshot.workspace.workspace_id,
                    path="api/src/example.py",
                    symbol="stored_symbol",
                    kind="function",
                    language="python",
                    line_start=1,
                    line_end=2,
                )
            ]

            with patch("app.service.read_latest_semantic_index_baseline", return_value=baseline):
                with patch("app.service.read_file_symbol_summary", return_value=stored_summary):
                    with patch("app.service.read_symbols_for_path", return_value=stored_symbols):
                        with patch("app.service.extract_path_symbols") as extract_mock:
                            result = service.explain_path(snapshot.workspace.workspace_id, "api/src/example.py")

            self.assertEqual(result.symbol_source, "tree_sitter")
            self.assertEqual(result.parser_language, "python")
            self.assertEqual(result.detected_symbols, ["stored_symbol"])
            self.assertNotIn("local_symbol", result.detected_symbols)
            extract_mock.assert_not_called()

    def test_semantic_search_returns_ast_grep_matches(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api" / "src").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "src" / "example.py").write_text(
                "def render_payload():\n    return {'status': 'ok'}\n",
                encoding="utf-8",
            )

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            fake_matches = [
                SemanticPatternMatchRecord(
                    path="api/src/example.py",
                    language="python",
                    line_start=1,
                    line_end=1,
                    column_start=1,
                    column_end=19,
                    matched_text="def render_payload():",
                    context_lines="def render_payload():",
                    meta_variables=[],
                )
            ]

            with patch(
                "app.service.run_ast_grep_query",
                return_value=(fake_matches, "/opt/homebrew/bin/sg", None, False),
            ):
                result = service.search_semantic_pattern(
                    snapshot.workspace.workspace_id,
                    "def $A():",
                    language="python",
                    path_glob="api/src/**/*.py",
                )

            self.assertEqual(result.engine, "ast_grep")
            self.assertEqual(result.binary_path, "/opt/homebrew/bin/sg")
            self.assertEqual(result.match_count, 1)
            self.assertEqual(result.matches[0].path, "api/src/example.py")
            self.assertEqual(result.matches[0].line_start, 1)
            self.assertEqual(result.language, "python")
            self.assertIsNotNone(result.query_row)
            assert result.query_row is not None
            self.assertEqual(result.query_row.source, "adhoc_tool")
            self.assertEqual(result.query_row.match_count, 1)
            self.assertEqual(len(result.match_rows), 1)
            self.assertEqual(result.match_rows[0].query_ref, result.query_row.query_ref)
            self.assertEqual(result.match_rows[0].path, "api/src/example.py")

    def test_semantic_search_reports_missing_ast_grep(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            root = Path(tmp_dir) / "repo"
            (root / "docs" / "bugs").mkdir(parents=True)
            (root / "api").mkdir(parents=True)
            (root / "docs" / "bugs" / "Bugs_25260323.md").write_text(LEDGER_TEXT, encoding="utf-8")
            (root / "api" / "main.py").write_text("print('ok')\n", encoding="utf-8")

            store = FileStore(Path(tmp_dir) / "data")
            service = TrackerService(store)
            snapshot = service.load_workspace(WorkspaceLoadRequest(root_path=str(root), auto_scan=True))
            assert snapshot is not None

            with patch(
                "app.service.run_ast_grep_query",
                return_value=([], None, "ast-grep binary is not installed on this machine.", False),
            ):
                result = service.search_semantic_pattern(snapshot.workspace.workspace_id, "print($A)")

            self.assertEqual(result.engine, "none")
            self.assertEqual(result.match_count, 0)
            self.assertIn("not installed", result.error or "")
            self.assertIsNotNone(result.query_row)
            assert result.query_row is not None
            self.assertEqual(result.query_row.engine, "none")
            self.assertEqual(result.query_row.error, "ast-grep binary is not installed on this machine.")


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
