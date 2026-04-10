from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Optional, Type, TypeVar
import uuid

from pydantic import BaseModel, ValidationError

from .models import (
    ActivityRecord,
    AppSettings,
    FixRecord,
    IssueRecord,
    RunRecord,
    RunReviewRecord,
    RunbookRecord,
    SavedIssueView,
    VerificationRecord,
    WorkspaceRecord,
    WorkspaceSnapshot,
)

T = TypeVar("T", bound=BaseModel)


class FileStore:
    def __init__(self, root: Path) -> None:
        self.root = root
        self.root.mkdir(parents=True, exist_ok=True)
        self.workspaces_file = self.root / "workspaces.json"
        self.settings_file = self.root / "settings.json"

    def _read_json(self, path: Path, default):
        if not path.exists():
            return default
        return json.loads(path.read_text(encoding="utf-8"))

    def _write_json(self, path: Path, payload) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        temp_path = path.with_name(f"{path.name}.{uuid.uuid4().hex}.tmp")
        temp_path.write_text(json.dumps(payload, indent=2, sort_keys=False), encoding="utf-8")
        temp_path.replace(path)

    def workspace_id_for_path(self, root_path: str) -> str:
        normalized = str(Path(root_path).resolve())
        digest = hashlib.sha1(normalized.encode("utf-8")).hexdigest()[:10]
        stem = Path(normalized).name.lower().replace(" ", "-").replace("_", "-")
        stem = "".join(ch for ch in stem if ch.isalnum() or ch == "-").strip("-") or "workspace"
        return f"{stem}-{digest}"

    def list_workspaces(self) -> list[WorkspaceRecord]:
        data = self._read_json(self.workspaces_file, [])
        return [WorkspaceRecord.model_validate(item) for item in data]

    def save_workspace(self, workspace: WorkspaceRecord) -> None:
        existing = {item.workspace_id: item for item in self.list_workspaces()}
        existing[workspace.workspace_id] = workspace
        self._write_json(
            self.workspaces_file,
            [item.model_dump(mode="json") for item in sorted(existing.values(), key=lambda value: value.name.lower())],
        )

    def workspace_dir(self, workspace_id: str) -> Path:
        path = self.root / "workspaces" / workspace_id
        path.mkdir(parents=True, exist_ok=True)
        return path

    def snapshot_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "snapshot.json"

    def runs_dir(self, workspace_id: str) -> Path:
        path = self.workspace_dir(workspace_id) / "runs"
        path.mkdir(parents=True, exist_ok=True)
        return path

    def run_record_path(self, workspace_id: str, run_id: str) -> Path:
        return self.runs_dir(workspace_id) / f"{run_id}.json"

    def issue_overrides_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "issue_overrides.json"

    def saved_views_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "saved_views.json"

    def tracker_issues_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "tracker_issues.json"

    def fix_records_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "fix_records.json"

    def run_reviews_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "run_reviews.json"

    def runbooks_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "runbooks.json"

    def verifications_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "verifications.json"

    def activity_path(self, workspace_id: str) -> Path:
        return self.workspace_dir(workspace_id) / "activity.jsonl"

    def load_snapshot(self, workspace_id: str) -> Optional[WorkspaceSnapshot]:
        path = self.snapshot_path(workspace_id)
        if not path.exists():
            return None
        return WorkspaceSnapshot.model_validate_json(path.read_text(encoding="utf-8"))

    def save_snapshot(self, snapshot: WorkspaceSnapshot) -> None:
        self._write_json(self.snapshot_path(snapshot.workspace.workspace_id), snapshot.model_dump(mode="json"))

    def load_issue_overrides(self, workspace_id: str) -> dict:
        return self._read_json(self.issue_overrides_path(workspace_id), {})

    def save_issue_overrides(self, workspace_id: str, overrides: dict) -> None:
        self._write_json(self.issue_overrides_path(workspace_id), overrides)

    def list_saved_views(self, workspace_id: str) -> list[SavedIssueView]:
        data = self._read_json(self.saved_views_path(workspace_id), [])
        return [SavedIssueView.model_validate(item) for item in data]

    def save_saved_views(self, workspace_id: str, views: list[SavedIssueView]) -> None:
        ordered = sorted(views, key=lambda item: (item.name.lower(), item.created_at))
        self._write_json(
            self.saved_views_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def list_tracker_issues(self, workspace_id: str) -> list[IssueRecord]:
        data = self._read_json(self.tracker_issues_path(workspace_id), [])
        return [IssueRecord.model_validate(item) for item in data]

    def save_tracker_issues(self, workspace_id: str, issues: list[IssueRecord]) -> None:
        ordered = sorted(issues, key=lambda item: (item.bug_id, item.updated_at), reverse=False)
        self._write_json(
            self.tracker_issues_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def list_fix_records(self, workspace_id: str) -> list[FixRecord]:
        data = self._read_json(self.fix_records_path(workspace_id), [])
        items = [FixRecord.model_validate(item) for item in data]
        return sorted(items, key=lambda item: item.recorded_at, reverse=True)

    def save_fix_records(self, workspace_id: str, fixes: list[FixRecord]) -> None:
        ordered = sorted(fixes, key=lambda item: item.recorded_at, reverse=True)
        self._write_json(
            self.fix_records_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def list_run_reviews(self, workspace_id: str) -> list[RunReviewRecord]:
        data = self._read_json(self.run_reviews_path(workspace_id), [])
        items = [RunReviewRecord.model_validate(item) for item in data]
        return sorted(items, key=lambda item: item.created_at, reverse=True)

    def save_run_reviews(self, workspace_id: str, reviews: list[RunReviewRecord]) -> None:
        ordered = sorted(reviews, key=lambda item: item.created_at, reverse=True)
        self._write_json(
            self.run_reviews_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def list_runbooks(self, workspace_id: str) -> list[RunbookRecord]:
        data = self._read_json(self.runbooks_path(workspace_id), [])
        items = [RunbookRecord.model_validate(item) for item in data]
        return sorted(items, key=lambda item: (item.built_in, item.name.lower(), item.created_at))

    def save_runbooks(self, workspace_id: str, runbooks: list[RunbookRecord]) -> None:
        ordered = sorted(runbooks, key=lambda item: (item.built_in, item.name.lower(), item.created_at))
        self._write_json(
            self.runbooks_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def list_verifications(self, workspace_id: str) -> list[VerificationRecord]:
        data = self._read_json(self.verifications_path(workspace_id), [])
        items = [VerificationRecord.model_validate(item) for item in data]
        return sorted(items, key=lambda item: item.created_at, reverse=True)

    def save_verifications(self, workspace_id: str, verifications: list[VerificationRecord]) -> None:
        ordered = sorted(verifications, key=lambda item: item.created_at, reverse=True)
        self._write_json(
            self.verifications_path(workspace_id),
            [item.model_dump(mode="json") for item in ordered],
        )

    def append_activity(self, activity: ActivityRecord) -> None:
        path = self.activity_path(activity.workspace_id)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(activity.model_dump(mode="json"), sort_keys=False))
            handle.write("\n")

    def list_activity(self, workspace_id: str) -> list[ActivityRecord]:
        path = self.activity_path(workspace_id)
        if not path.exists():
            return []
        items: list[ActivityRecord] = []
        with path.open("r", encoding="utf-8") as handle:
            for line in handle:
                payload = line.strip()
                if not payload:
                    continue
                items.append(ActivityRecord.model_validate_json(payload))
        return sorted(items, key=lambda item: item.created_at, reverse=True)

    def save_run(self, run: RunRecord) -> None:
        self._write_json(self.run_record_path(run.workspace_id, run.run_id), run.model_dump(mode="json"))

    def load_run(self, workspace_id: str, run_id: str) -> Optional[RunRecord]:
        path = self.run_record_path(workspace_id, run_id)
        if not path.exists():
            return None
        payload = path.read_text(encoding="utf-8").strip()
        if not payload:
            return None
        try:
            return RunRecord.model_validate_json(payload)
        except ValidationError:
            return None

    def list_runs(self, workspace_id: str) -> list[RunRecord]:
        runs = []
        for path in sorted(self.runs_dir(workspace_id).glob("*.json")):
            if path.name.endswith(".out.json"):
                continue
            try:
                runs.append(RunRecord.model_validate_json(path.read_text(encoding="utf-8")))
            except ValidationError:
                continue
        return sorted(runs, key=lambda item: item.created_at, reverse=True)

    def load_model(self, path: Path, model_type: Type[T]) -> T:
        return model_type.model_validate_json(path.read_text(encoding="utf-8"))

    def load_settings(self) -> AppSettings:
        if not self.settings_file.exists():
            settings = AppSettings()
            self.save_settings(settings)
            return settings
        return AppSettings.model_validate_json(self.settings_file.read_text(encoding="utf-8"))

    def save_settings(self, settings: AppSettings) -> AppSettings:
        self._write_json(self.settings_file, settings.model_dump(mode="json"))
        return settings
