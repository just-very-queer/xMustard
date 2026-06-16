from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from app.models import ActivityActor, ActivityRecord, WorkspaceLoadRequest
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


class ActivityOverviewTests(unittest.TestCase):
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

    def _append_activity(
        self,
        service: TrackerService,
        *,
        workspace_id: str,
        activity_id: str,
        entity_type: str,
        entity_id: str,
        action: str,
        actor: ActivityActor,
        created_at: str,
        issue_id: str | None = None,
        run_id: str | None = None,
    ) -> None:
        service.store.append_activity(
            ActivityRecord(
                activity_id=activity_id,
                workspace_id=workspace_id,
                entity_type=entity_type,
                entity_id=entity_id,
                action=action,
                summary=f"{action} summary",
                actor=actor,
                issue_id=issue_id,
                run_id=run_id,
                created_at=created_at,
            )
        )

    def _plain(self, value):
        if hasattr(value, "model_dump"):
            value = value.model_dump(mode="json")
        if isinstance(value, list):
            return [self._plain(item) for item in value]
        if isinstance(value, dict):
            return {key: self._plain(item) for key, item in value.items()}
        return value

    def test_activity_overview_groups_operator_and_agents_by_stable_actor_key(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_operator",
                entity_type="issue",
                entity_id="P0_25M03_001",
                action="issue.updated",
                actor=ActivityActor(kind="operator", name="operator"),
                issue_id="P0_25M03_001",
                created_at="2026-03-30T10:00:00+00:00",
            )
            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_codex_queued",
                entity_type="run",
                entity_id="run_codex_1",
                action="run.queued",
                actor=ActivityActor(kind="agent", name="codex", runtime="codex", model="gpt-5.4"),
                issue_id="P0_25M03_001",
                run_id="run_codex_1",
                created_at="2026-03-30T10:01:00+00:00",
            )
            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_codex_completed",
                entity_type="run",
                entity_id="run_codex_2",
                action="run.completed",
                actor=ActivityActor(kind="agent", name="codex", runtime="codex", model="gpt-5.4-mini"),
                issue_id="P0_25M03_001",
                run_id="run_codex_2",
                created_at="2026-03-30T10:02:00+00:00",
            )
            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_opencode",
                entity_type="run",
                entity_id="run_opencode_1",
                action="run.queued",
                actor=ActivityActor(
                    kind="agent",
                    name="opencode",
                    runtime="opencode",
                    model="opencode-go/minimax-m2.7",
                ),
                issue_id="P0_25M03_001",
                run_id="run_opencode_1",
                created_at="2026-03-30T10:03:00+00:00",
            )

            overview = self._plain(service.get_activity_overview(workspace_id))
            top_actors = {
                item["actor_key"]: item["count"]
                for item in overview["top_actors"]
            }

            self.assertEqual(
                top_actors,
                {
                    "agent:codex": 2,
                    "agent:opencode": 1,
                    "operator:operator": 1,
                },
            )

    def test_activity_overview_aggregates_counts_top_actors_and_top_actions_from_store(self):
        with tempfile.TemporaryDirectory() as tmp_dir:
            service, workspace_id = self._create_service(tmp_dir)

            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_issue_1",
                entity_type="issue",
                entity_id="P0_25M03_001",
                action="issue.updated",
                actor=ActivityActor(kind="operator", name="operator"),
                issue_id="P0_25M03_001",
                created_at="2026-03-30T10:00:00+00:00",
            )
            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_issue_2",
                entity_type="issue",
                entity_id="P0_25M03_001",
                action="issue.updated",
                actor=ActivityActor(kind="operator", name="operator"),
                issue_id="P0_25M03_001",
                created_at="2026-03-30T10:01:00+00:00",
            )
            for index in range(3):
                self._append_activity(
                    service,
                    workspace_id=workspace_id,
                    activity_id=f"act_run_queued_{index}",
                    entity_type="run",
                    entity_id=f"run_codex_{index}",
                    action="run.queued",
                    actor=ActivityActor(kind="agent", name="codex", runtime="codex", model="gpt-5.4"),
                    issue_id="P0_25M03_001",
                    run_id=f"run_codex_{index}",
                    created_at=f"2026-03-30T10:1{index}:00+00:00",
                )
            self._append_activity(
                service,
                workspace_id=workspace_id,
                activity_id="act_run_completed",
                entity_type="run",
                entity_id="run_opencode_1",
                action="run.completed",
                actor=ActivityActor(
                    kind="agent",
                    name="opencode",
                    runtime="opencode",
                    model="opencode-go/minimax-m2.7",
                ),
                issue_id="P0_25M03_001",
                run_id="run_opencode_1",
                created_at="2026-03-30T10:20:00+00:00",
            )

            overview = self._plain(service.get_activity_overview(workspace_id))

            self.assertEqual(overview["total_events"], 6)
            self.assertEqual(overview["counts_by_entity_type"], {"run": 4, "issue": 2})
            self.assertEqual(
                [(item["actor_key"], item["count"]) for item in overview["top_actors"]],
                [
                    ("agent:codex", 3),
                    ("operator:operator", 2),
                    ("agent:opencode", 1),
                ],
            )
            self.assertEqual(
                [(item["action"], item["count"]) for item in overview["top_actions"]],
                [
                    ("run.queued", 3),
                    ("issue.updated", 2),
                    ("run.completed", 1),
                ],
            )


if __name__ == "__main__":
    unittest.main()
