from __future__ import annotations

from collections import Counter
import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Optional
from urllib.parse import quote

from .models import (
    ActivityActor,
    ActivityOverview,
    ActivityRecord,
    ActivityRollupItem,
    AppSettings,
    EvidenceRef,
    ExportBundle,
    FixRecord,
    FixDraftSuggestion,
    FixRecordRequest,
    FixUpdateRequest,
    IssueDriftDetail,
    IssueCreateRequest,
    IssueContextPacket,
    IssueRecord,
    IssueUpdateRequest,
    PromoteSignalRequest,
    RunReviewRecord,
    RunReviewRequest,
    RunAcceptRequest,
    RunbookRecord,
    RunbookUpsertRequest,
    ReviewQueueItem,
    SavedIssueView,
    SavedIssueViewRequest,
    SourceRecord,
    WorktreeStatus,
    WorkspaceLoadRequest,
    WorkspaceRecord,
    WorkspaceSnapshot,
    build_activity_actor,
    utc_now,
)
from .runtimes import RuntimeService
from .scanners import (
    apply_verdicts,
    build_source_records,
    latest_bug_ledger,
    list_tree_nodes,
    parse_ledger,
    scan_repo_signals,
    summarize_tree,
    verdict_bundles,
)
from .store import FileStore
from .terminal import TerminalService


class TrackerService:
    def __init__(self, store: FileStore) -> None:
        self.store = store
        self.runtime_service = RuntimeService(store)
        self.terminal_service = TerminalService(store)

    def load_workspace(self, request: WorkspaceLoadRequest) -> Optional[WorkspaceSnapshot]:
        root_path = str(Path(request.root_path).resolve())
        workspace_id = self.store.workspace_id_for_path(root_path)
        workspace = WorkspaceRecord(
            workspace_id=workspace_id,
            name=request.name or Path(root_path).name,
            root_path=root_path,
            latest_scan_at=None,
            updated_at=utc_now(),
        )
        existing = {item.workspace_id: item for item in self.store.list_workspaces()}
        if workspace_id in existing:
            workspace = existing[workspace_id].model_copy(update={"updated_at": utc_now(), "name": workspace.name, "root_path": root_path})
        self.store.save_workspace(workspace)
        cached_snapshot = self.store.load_snapshot(workspace_id)
        if cached_snapshot and request.prefer_cached_snapshot:
            return cached_snapshot.model_copy(update={"workspace": workspace.model_copy(update={"latest_scan_at": cached_snapshot.workspace.latest_scan_at})})
        if request.auto_scan:
            return self.scan_workspace(workspace_id)
        return cached_snapshot

    def list_workspaces(self) -> list[WorkspaceRecord]:
        return self.store.list_workspaces()

    def get_workspace(self, workspace_id: str) -> WorkspaceRecord:
        workspaces = {workspace.workspace_id: workspace for workspace in self.store.list_workspaces()}
        workspace = workspaces.get(workspace_id)
        if not workspace:
            raise FileNotFoundError(workspace_id)
        return workspace

    def scan_workspace(self, workspace_id: str) -> WorkspaceSnapshot:
        workspace = self.get_workspace(workspace_id)
        root = Path(workspace.root_path)
        ledger_path = latest_bug_ledger(root)
        verdict_paths = verdict_bundles(root)
        tracker_issues = self.store.list_tracker_issues(workspace_id)
        fixes = self.store.list_fix_records(workspace_id)
        run_reviews = self.store.list_run_reviews(workspace_id)
        runbooks = self.list_runbooks(workspace_id)

        issues: list[IssueRecord] = parse_ledger(ledger_path) if ledger_path else []
        if verdict_paths and issues:
            issues = apply_verdicts(issues, verdict_paths, root)
        issues = self._merge_tracker_issues(issues, tracker_issues)
        issues = self._apply_issue_overrides(workspace_id, issues)
        issues = self._annotate_review_ready(issues, self.store.list_runs(workspace_id), fixes, run_reviews)
        issues = [self._normalize_issue_evidence(root, issue) for issue in issues]
        issues = self._apply_issue_drift(root, issues)
        signals = scan_repo_signals(root)
        sources = build_source_records(ledger_path, issues, verdict_paths, signals)
        sources.extend(self._build_tracker_sources(workspace_id, tracker_issues, fixes, runbooks, run_reviews))
        drift_summary = self._build_drift_summary(issues)
        runtimes = self.runtime_service.detect_runtimes()
        tree_summary = summarize_tree(root)
        summary = {
            "issues_total": len(issues),
            "issues_fixed": sum(1 for issue in issues if issue.code_status == "fixed" or issue.doc_status == "fixed"),
            "issues_open": sum(1 for issue in issues if issue.issue_status in {"open", "partial"}),
            "review_ready_total": sum(1 for issue in issues if issue.review_ready_count > 0),
            "review_queue_total": sum(issue.review_ready_count for issue in issues),
            "signals_total": len(signals),
            "signals_promoted": sum(1 for signal in signals if signal.promoted_bug_id),
            "drift_total": sum(len(issue.drift_flags) for issue in issues),
            "sources_total": len(sources),
            "tracker_issues_total": len(tracker_issues),
            "fixes_total": len(fixes),
            "runbooks_total": len(runbooks),
            "tree_files": tree_summary["files"],
            "tree_directories": tree_summary["directories"],
        }
        snapshot = WorkspaceSnapshot(
            workspace=workspace.model_copy(update={"latest_scan_at": utc_now(), "updated_at": utc_now()}),
            summary=summary,
            issues=issues,
            signals=signals,
            sources=sources,
            drift_summary=drift_summary,
            runtimes=runtimes,
            latest_ledger=str(ledger_path) if ledger_path else None,
            latest_verdicts=str(verdict_paths[-1]) if verdict_paths else None,
        )
        self.store.save_workspace(snapshot.workspace)
        self.store.save_snapshot(snapshot)
        return snapshot

    def read_snapshot(self, workspace_id: str) -> Optional[WorkspaceSnapshot]:
        return self.store.load_snapshot(workspace_id)

    def list_activity(
        self,
        workspace_id: str,
        issue_id: Optional[str] = None,
        run_id: Optional[str] = None,
        limit: int = 100,
    ) -> list[ActivityRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_activity(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        if run_id:
            items = [item for item in items if item.run_id == run_id]
        return items[:limit]

    def read_activity_overview(self, workspace_id: str, limit: int = 200) -> ActivityOverview:
        activity = self.list_activity(workspace_id, limit=limit)
        actor_counts: Counter[str] = Counter()
        action_counts: Counter[str] = Counter()
        entity_counts: Counter[str] = Counter()
        actor_labels: dict[str, str] = {}

        for item in activity:
            actor_counts[item.actor.key] += 1
            action_counts[item.action] += 1
            entity_counts[item.entity_type] += 1
            actor_labels[item.actor.key] = item.actor.label

        top_actors = [
            ActivityRollupItem(key=key, actor_key=key, label=actor_labels.get(key, key), count=count)
            for key, count in sorted(actor_counts.items(), key=lambda pair: (-pair[1], pair[0]))[:5]
        ]
        top_actions = [
            ActivityRollupItem(key=key, action=key, label=key, count=count)
            for key, count in sorted(action_counts.items(), key=lambda pair: (-pair[1], pair[0]))[:5]
        ]
        entity_labels = {
            "issue": "Issues",
            "fix": "Fixes",
            "run": "Runs",
            "view": "Views",
            "signal": "Signals",
            "workspace": "Workspace",
            "settings": "Settings",
        }
        top_entities = [
            ActivityRollupItem(key=key, entity_type=key, label=entity_labels.get(key, key), count=count)
            for key, count in sorted(entity_counts.items(), key=lambda pair: (-pair[1], pair[0]))[:5]
        ]

        return ActivityOverview(
            total_events=len(activity),
            unique_actors=len(actor_counts),
            unique_actions=len(action_counts),
            operator_events=sum(1 for item in activity if item.actor.kind == "operator"),
            agent_events=sum(1 for item in activity if item.actor.kind == "agent"),
            system_events=sum(1 for item in activity if item.actor.kind == "system"),
            issues_touched=len({item.issue_id or item.entity_id for item in activity if item.issue_id or item.entity_type == "issue"}),
            fixes_touched=len({item.entity_id for item in activity if item.entity_type == "fix"}),
            runs_touched=len({item.run_id or item.entity_id for item in activity if item.run_id or item.entity_type == "run"}),
            views_touched=len({item.entity_id for item in activity if item.entity_type == "view"}),
            counts_by_entity_type=dict(sorted(entity_counts.items(), key=lambda pair: (-pair[1], pair[0]))),
            top_actors=top_actors,
            top_actions=top_actions,
            top_entities=top_entities,
            most_recent_at=activity[0].created_at if activity else None,
        )

    def get_activity_overview(self, workspace_id: str, limit: int = 200) -> ActivityOverview:
        return self.read_activity_overview(workspace_id, limit=limit)

    def list_issues(
        self,
        workspace_id: str,
        query: str = "",
        severities: Optional[list[str]] = None,
        issue_statuses: Optional[list[str]] = None,
        sources: Optional[list[str]] = None,
        labels: Optional[list[str]] = None,
        drift_only: bool = False,
        needs_followup: Optional[bool] = None,
        review_ready_only: bool = False,
    ) -> list[IssueRecord]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        normalized_query = query.strip().lower()
        items = snapshot.issues
        if severities:
            severity_set = {item for item in severities if item}
            items = [item for item in items if item.severity in severity_set]
        if issue_statuses:
            status_set = {item for item in issue_statuses if item}
            items = [item for item in items if item.issue_status in status_set]
        if sources:
            source_set = {item for item in sources if item}
            items = [item for item in items if item.source in source_set]
        if labels:
            label_set = {item.strip().lower() for item in labels if item.strip()}
            items = [item for item in items if label_set.intersection(label.lower() for label in item.labels)]
        if drift_only:
            items = [item for item in items if item.drift_flags]
        if needs_followup is not None:
            items = [item for item in items if item.needs_followup is needs_followup]
        if review_ready_only:
            items = [item for item in items if item.review_ready_count > 0]
        if normalized_query:
            items = [
                item
                for item in items
                if normalized_query in item.bug_id.lower()
                or normalized_query in item.title.lower()
                or normalized_query in (item.summary or "").lower()
            ]
        return items

    def create_issue(self, workspace_id: str, request: IssueCreateRequest) -> IssueRecord:
        self.get_workspace(workspace_id)
        tracked = self.store.list_tracker_issues(workspace_id)
        bug_id = (request.bug_id or "").strip() or self._next_tracker_issue_id(tracked)
        snapshot = self.store.load_snapshot(workspace_id)
        if any(item.bug_id == bug_id for item in tracked) or (
            snapshot and any(item.bug_id == bug_id for item in snapshot.issues)
        ):
            raise ValueError(f"Issue already exists: {bug_id}")
        issue = IssueRecord(
            bug_id=bug_id,
            title=request.title.strip(),
            severity=request.severity.strip().upper(),
            issue_status=request.issue_status,
            source="tracker",
            source_doc=request.source_doc,
            doc_status="open",
            code_status="unknown",
            summary=request.summary.strip() if request.summary else None,
            impact=request.impact.strip() if request.impact else None,
            labels=sorted(set(label.strip() for label in request.labels if label.strip())),
            notes=request.notes.strip() if request.notes else None,
            needs_followup=request.needs_followup,
            fingerprint=hashlib.sha1(f"{workspace_id}:tracker:{bug_id}".encode("utf-8")).hexdigest(),
        )
        tracked.append(issue)
        self.store.save_tracker_issues(workspace_id, tracked)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=bug_id,
            action="issue.created",
            summary=f"Created tracker issue {bug_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=bug_id,
            details={"source": "tracker", "severity": issue.severity, "title": issue.title},
        )
        snapshot = self.scan_workspace(workspace_id)
        return next(item for item in snapshot.issues if item.bug_id == bug_id)

    def list_fixes(self, workspace_id: str, issue_id: Optional[str] = None) -> list[FixRecord]:
        self.get_workspace(workspace_id)
        fixes = self.store.list_fix_records(workspace_id)
        if issue_id:
            fixes = [item for item in fixes if item.issue_id == issue_id]
        return fixes

    def record_fix(self, workspace_id: str, issue_id: str, request: FixRecordRequest) -> FixRecord:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        run = self.store.load_run(workspace_id, request.run_id) if request.run_id else None
        worktree = run.worktree if run and run.worktree and run.worktree.available else self.read_worktree_status(workspace_id)
        if run:
            actor = build_activity_actor("agent", run.runtime, runtime=run.runtime, model=run.model)
            session_id = run.summary.get("session_id") if isinstance(run.summary, dict) else None
        elif request.runtime and request.model:
            actor = build_activity_actor("agent", request.runtime, runtime=request.runtime, model=request.model)
            session_id = None
        else:
            actor = build_activity_actor("operator", "operator")
            session_id = None
        fixes = self.store.list_fix_records(workspace_id)
        fix_id = self._next_fix_id(workspace_id, issue_id, fixes)
        summary = request.summary.strip()
        evidence = list(request.evidence)
        if run and run.summary and run.summary.get("text_excerpt"):
            evidence.append(EvidenceRef(path=run.output_path, excerpt=str(run.summary.get("text_excerpt"))[:280]))
        fix = FixRecord(
            fix_id=fix_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            status=request.status,
            summary=summary,
            how=request.how.strip() if request.how else None,
            actor=actor,
            run_id=request.run_id,
            session_id=session_id,
            changed_files=sorted(set(item.strip() for item in request.changed_files if item.strip())),
            tests_run=[item.strip() for item in request.tests_run if item.strip()],
            evidence=evidence,
            worktree=worktree if worktree.available else None,
            notes=request.notes.strip() if request.notes else None,
        )
        fixes.append(fix)
        self.store.save_fix_records(workspace_id, fixes)
        if request.issue_status is not None:
            self.update_issue(workspace_id, issue_id, IssueUpdateRequest(issue_status=request.issue_status))
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="fix",
            entity_id=fix.fix_id,
            action="fix.recorded",
            summary=f"Recorded fix {fix.fix_id} for {issue_id}",
            actor=actor,
            issue_id=issue_id,
            run_id=request.run_id,
            details={
                "status": fix.status,
                "run_id": request.run_id,
                "changed_files": fix.changed_files,
                "tests_run": fix.tests_run,
            },
        )
        self.scan_workspace(workspace_id)
        return fix

    def suggest_fix_draft(self, workspace_id: str, issue_id: str, run_id: str) -> FixDraftSuggestion:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if run.issue_id != issue_id:
            raise ValueError(f"Run {run_id} does not belong to issue {issue_id}")

        worktree = self.read_worktree_status(workspace_id)
        excerpt = self._run_excerpt(run)
        summary = self._draft_summary_from_excerpt(excerpt, issue_id, run_id)
        tests_run = self._extract_test_commands(excerpt)
        changed_files = self._extract_changed_files(excerpt)
        if not changed_files and worktree.available:
            changed_files = list(worktree.dirty_paths[:8])
        suggested_issue_status = "verification" if run.status == "completed" else "in_progress"

        return FixDraftSuggestion(
            workspace_id=workspace_id,
            issue_id=issue_id,
            run_id=run_id,
            summary=summary,
            how=excerpt or None,
            changed_files=changed_files,
            tests_run=tests_run,
            suggested_issue_status=suggested_issue_status,
            source_excerpt=excerpt or None,
        )

    def review_run(self, workspace_id: str, run_id: str, request: RunReviewRequest) -> RunReviewRecord:
        self.get_workspace(workspace_id)
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if run.issue_id == "workspace-query":
            raise ValueError("Workspace query runs cannot be reviewed as issue work")
        if run.status != "completed":
            raise ValueError("Only completed issue runs can be reviewed")

        reviews = [item for item in self.store.list_run_reviews(workspace_id) if item.run_id != run_id]
        review = RunReviewRecord(
            review_id=hashlib.sha1(f"{workspace_id}:{run_id}:{request.disposition}:{utc_now()}".encode("utf-8")).hexdigest()[:16],
            workspace_id=workspace_id,
            run_id=run_id,
            issue_id=run.issue_id,
            disposition=request.disposition,
            actor=build_activity_actor("operator", "operator"),
            notes=request.notes.strip() if request.notes else None,
        )
        reviews.append(review)
        self.store.save_run_reviews(workspace_id, reviews)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="run.reviewed",
            summary=f"Marked run {run_id} as {request.disposition}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"disposition": request.disposition, "notes": review.notes},
        )
        self.scan_workspace(workspace_id)
        return review

    def update_fix(self, workspace_id: str, fix_id: str, request: FixUpdateRequest) -> FixRecord:
        self.get_workspace(workspace_id)
        fixes = self.store.list_fix_records(workspace_id)
        target = next((item for item in fixes if item.fix_id == fix_id), None)
        if not target:
            raise FileNotFoundError(fix_id)
        updates = {"updated_at": utc_now()}
        if request.status is not None:
            updates["status"] = request.status
        if request.notes is not None:
            updates["notes"] = request.notes.strip() or None
        updated = target.model_copy(update=updates)
        self.store.save_fix_records(workspace_id, [updated if item.fix_id == fix_id else item for item in fixes])
        if request.issue_status is not None:
            self.update_issue(workspace_id, target.issue_id, IssueUpdateRequest(issue_status=request.issue_status))
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="fix",
            entity_id=fix_id,
            action="fix.updated",
            summary=f"Updated fix {fix_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=target.issue_id,
            run_id=target.run_id,
            details={"status": updated.status},
        )
        self.scan_workspace(workspace_id)
        return updated

    def list_saved_views(self, workspace_id: str) -> list[SavedIssueView]:
        self.get_workspace(workspace_id)
        return self.store.list_saved_views(workspace_id)

    def create_saved_view(self, workspace_id: str, request: SavedIssueViewRequest) -> SavedIssueView:
        self.get_workspace(workspace_id)
        views = self.store.list_saved_views(workspace_id)
        base = request.name.strip() or "Untitled view"
        seed = f"{workspace_id}:{base}:{utc_now()}"
        slug = quote(base.lower().replace(" ", "-"), safe="-")
        view_id = f"{slug}-{hashlib.sha1(seed.encode('utf-8')).hexdigest()[:8]}"
        view = SavedIssueView(
            view_id=view_id,
            workspace_id=workspace_id,
            name=base,
            query=request.query.strip(),
            severities=sorted(set(item for item in request.severities if item)),
            statuses=sorted(set(item for item in request.statuses if item)),
            sources=sorted(set(item for item in request.sources if item)),
            labels=sorted(set(item.strip() for item in request.labels if item.strip())),
            drift_only=request.drift_only,
            needs_followup=request.needs_followup,
            review_ready_only=request.review_ready_only,
        )
        self.store.save_saved_views(workspace_id, [*views, view])
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="view",
            entity_id=view.view_id,
            action="view.created",
            summary=f"Created saved view {view.name}",
            actor=build_activity_actor("operator", "operator"),
            details={"name": view.name, "filters": view.model_dump(mode="json")},
        )
        return view

    def update_saved_view(self, workspace_id: str, view_id: str, request: SavedIssueViewRequest) -> SavedIssueView:
        self.get_workspace(workspace_id)
        views = self.store.list_saved_views(workspace_id)
        target = next((item for item in views if item.view_id == view_id), None)
        if not target:
            raise FileNotFoundError(view_id)
        updated = target.model_copy(
            update={
                "name": request.name.strip() or target.name,
                "query": request.query.strip(),
                "severities": sorted(set(item for item in request.severities if item)),
                "statuses": sorted(set(item for item in request.statuses if item)),
                "sources": sorted(set(item for item in request.sources if item)),
                "labels": sorted(set(item.strip() for item in request.labels if item.strip())),
                "drift_only": request.drift_only,
                "needs_followup": request.needs_followup,
                "review_ready_only": request.review_ready_only,
                "updated_at": utc_now(),
            }
        )
        self.store.save_saved_views(
            workspace_id,
            [updated if item.view_id == view_id else item for item in views],
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="view",
            entity_id=view_id,
            action="view.updated",
            summary=f"Updated saved view {updated.name}",
            actor=build_activity_actor("operator", "operator"),
            details={"name": updated.name, "filters": updated.model_dump(mode="json")},
        )
        return updated

    def delete_saved_view(self, workspace_id: str, view_id: str) -> None:
        self.get_workspace(workspace_id)
        views = self.store.list_saved_views(workspace_id)
        target = next((item for item in views if item.view_id == view_id), None)
        if not target:
            raise FileNotFoundError(view_id)
        self.store.save_saved_views(workspace_id, [item for item in views if item.view_id != view_id])
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="view",
            entity_id=view_id,
            action="view.deleted",
            summary=f"Deleted saved view {target.name}",
            actor=build_activity_actor("operator", "operator"),
            details={"name": target.name},
        )

    def list_signals(
        self,
        workspace_id: str,
        query: str = "",
        severity: Optional[str] = None,
        promoted: Optional[bool] = None,
    ) -> list:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        normalized_query = query.strip().lower()
        items = snapshot.signals
        if severity:
            items = [item for item in items if item.severity == severity]
        if promoted is not None:
            items = [item for item in items if (item.promoted_bug_id is not None) == promoted]
        if normalized_query:
            items = [
                item
                for item in items
                if normalized_query in item.title.lower()
                or normalized_query in item.summary.lower()
                or normalized_query in item.file_path.lower()
            ]
        return items

    def read_sources(self, workspace_id: str) -> list[SourceRecord]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        return snapshot.sources

    def read_drift(self, workspace_id: str) -> dict[str, int]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        return snapshot.drift_summary

    def read_issue_drift(self, workspace_id: str, issue_id: str) -> IssueDriftDetail:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        issue = next((item for item in snapshot.issues if item.bug_id == issue_id), None)
        if not issue:
            raise FileNotFoundError(issue_id)
        missing = [item for item in [*issue.evidence, *issue.verification_evidence] if item.path_exists is False]
        return IssueDriftDetail(
            bug_id=issue.bug_id,
            drift_flags=issue.drift_flags,
            missing_evidence=missing,
            verification_gap=not bool(issue.tests_passed),
        )

    def get_settings(self) -> AppSettings:
        return self.store.load_settings()

    def update_settings(self, settings: AppSettings) -> AppSettings:
        return self.store.save_settings(settings)

    def list_runbooks(self, workspace_id: str) -> list[RunbookRecord]:
        self.get_workspace(workspace_id)
        saved = {item.runbook_id: item for item in self.store.list_runbooks(workspace_id)}
        merged = {item.runbook_id: item for item in self._default_runbooks(workspace_id)}
        merged.update(saved)
        return sorted(merged.values(), key=lambda item: (item.built_in, item.name.lower(), item.created_at))

    def save_runbook(self, workspace_id: str, request: RunbookUpsertRequest) -> RunbookRecord:
        self.get_workspace(workspace_id)
        existing = [item for item in self.store.list_runbooks(workspace_id) if item.runbook_id != request.runbook_id]
        runbook_id = request.runbook_id or self._slug_runbook_name(request.name)
        runbook = RunbookRecord(
            runbook_id=runbook_id,
            workspace_id=workspace_id,
            name=request.name.strip(),
            description=request.description.strip(),
            scope=request.scope,
            template=request.template.strip(),
            built_in=False,
        )
        existing.append(runbook)
        self.store.save_runbooks(workspace_id, existing)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"runbook:{runbook_id}",
            action="runbook.saved",
            summary=f"Saved runbook {runbook.name}",
            actor=build_activity_actor("operator", "operator"),
            details={"runbook_id": runbook_id, "scope": runbook.scope},
        )
        return runbook

    def delete_runbook(self, workspace_id: str, runbook_id: str) -> None:
        self.get_workspace(workspace_id)
        existing = self.store.list_runbooks(workspace_id)
        remaining = [item for item in existing if item.runbook_id != runbook_id]
        if len(remaining) == len(existing):
            raise FileNotFoundError(runbook_id)
        self.store.save_runbooks(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"runbook:{runbook_id}",
            action="runbook.deleted",
            summary=f"Deleted runbook {runbook_id}",
            actor=build_activity_actor("operator", "operator"),
            details={"runbook_id": runbook_id},
        )

    def list_review_queue(self, workspace_id: str) -> list[ReviewQueueItem]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        issues = {item.bug_id: item for item in snapshot.issues}
        review_ids = {run_id for issue in snapshot.issues for run_id in issue.review_ready_runs}
        candidates: list[ReviewQueueItem] = []
        for run in self.store.list_runs(workspace_id):
            if run.run_id not in review_ids:
                continue
            issue = issues.get(run.issue_id)
            if not issue:
                continue
            draft = None
            try:
                draft = self.suggest_fix_draft(workspace_id, run.issue_id, run.run_id)
            except Exception:
                draft = None
            candidates.append(ReviewQueueItem(run=run, issue=issue, draft=draft))
        return sorted(candidates, key=lambda item: item.run.created_at, reverse=True)

    def accept_review_run(self, workspace_id: str, run_id: str, request: RunAcceptRequest) -> FixRecord:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if run.issue_id == "workspace-query":
            raise ValueError("Workspace query runs cannot be accepted as fixes")
        if any(item.run_id == run_id for item in self.store.list_fix_records(workspace_id)):
            raise ValueError(f"Run {run_id} already has a recorded fix")
        draft = self.suggest_fix_draft(workspace_id, run.issue_id, run.run_id)
        fix = self.record_fix(
            workspace_id,
            run.issue_id,
            FixRecordRequest(
                summary=draft.summary,
                how=draft.how,
                run_id=run.run_id,
                changed_files=draft.changed_files,
                tests_run=draft.tests_run,
                notes=request.notes,
                issue_status=request.issue_status or draft.suggested_issue_status or "verification",
            ),
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="run.accepted",
            summary=f"Accepted run {run_id} into fix {fix.fix_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"fix_id": fix.fix_id, "issue_status": request.issue_status or draft.suggested_issue_status},
        )
        return fix

    def issue_work(self, workspace_id: str, issue_id: str, runbook_id: Optional[str] = None) -> IssueContextPacket:
        packet = self.build_issue_context(workspace_id, issue_id)
        if not runbook_id:
            return packet
        runbook = self._resolve_runbook(workspace_id, runbook_id)
        selected_steps = self._render_runbook_steps(runbook.template)
        return packet.model_copy(
            update={
                "runbook": selected_steps,
                "prompt": f"{packet.prompt}\n\nSelected runbook: {runbook.name}\n{runbook.template.strip()}",
            }
        )

    def list_tree(self, workspace_id: str, relative_path: str = "") -> list[dict]:
        workspace = self.get_workspace(workspace_id)
        return list_tree_nodes(Path(workspace.root_path), relative_path)

    def build_issue_context(self, workspace_id: str, issue_id: str) -> IssueContextPacket:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        issue = next((item for item in snapshot.issues if item.bug_id == issue_id), None)
        if not issue:
            raise FileNotFoundError(issue_id)
        tree_focus = []
        evidence_bundle = list(issue.evidence) + list(issue.verification_evidence)
        for item in evidence_bundle:
            focus_path = item.normalized_path or item.path
            if focus_path not in tree_focus:
                tree_focus.append(focus_path)
        default_runbook = self._resolve_runbook(workspace_id, "fix")
        runbooks = self.list_runbooks(workspace_id)
        runbook = self._render_runbook_steps(default_runbook.template)
        recent_fixes = self.list_fixes(workspace_id, issue_id=issue_id)[:5]
        recent_activity = [item for item in self.list_activity(workspace_id, issue_id=issue_id, limit=16) if item.entity_type == "issue"][:8]
        prompt = self._build_prompt(snapshot.workspace, issue, tree_focus, recent_fixes, recent_activity)
        return IssueContextPacket(
            issue=issue,
            workspace=snapshot.workspace,
            tree_focus=tree_focus[:12],
            evidence_bundle=evidence_bundle[:20],
            recent_fixes=recent_fixes,
            recent_activity=recent_activity,
            runbook=runbook,
            available_runbooks=runbooks,
            worktree=self.read_worktree_status(workspace_id),
            prompt=prompt,
        )

    def read_worktree_status(self, workspace_id: str) -> WorktreeStatus:
        workspace = self.get_workspace(workspace_id)
        root = Path(workspace.root_path)
        try:
            branch_output = subprocess.run(
                ["git", "-C", str(root), "status", "--branch", "--porcelain=v2"],
                capture_output=True,
                text=True,
                check=False,
            )
        except FileNotFoundError:
            return WorktreeStatus()
        if branch_output.returncode != 0:
            return WorktreeStatus(available=True, is_git_repo=False)

        branch = None
        head_sha = None
        ahead = 0
        behind = 0
        dirty_files = 0
        staged_files = 0
        untracked_files = 0
        dirty_paths: list[str] = []

        for line in branch_output.stdout.splitlines():
            if line.startswith("# branch.head "):
                branch = line.replace("# branch.head ", "", 1).strip()
            elif line.startswith("# branch.oid "):
                value = line.replace("# branch.oid ", "", 1).strip()
                head_sha = None if value == "(initial)" else value
            elif line.startswith("# branch.ab "):
                parts = line.split()
                for part in parts:
                    if part.startswith("+"):
                        ahead = int(part[1:])
                    elif part.startswith("-"):
                        behind = int(part[1:])
            elif line.startswith("? "):
                untracked_files += 1
                dirty_files += 1
                dirty_paths.append(line[2:].strip())
            elif line[:2] in {"1 ", "2 ", "u "}:
                parts = line.split()
                xy = parts[1]
                path = parts[-1]
                dirty_files += 1
                dirty_paths.append(path)
                if xy[0] != ".":
                    staged_files += 1

        return WorktreeStatus(
            available=True,
            is_git_repo=True,
            branch=branch,
            head_sha=head_sha,
            dirty_files=dirty_files,
            staged_files=staged_files,
            untracked_files=untracked_files,
            ahead=ahead,
            behind=behind,
            dirty_paths=dirty_paths[:20],
        )

    def start_issue_run(
        self,
        workspace_id: str,
        issue_id: str,
        runtime: str,
        model: str,
        instruction: Optional[str],
        runbook_id: Optional[str] = None,
    ) -> dict:
        packet = self.issue_work(workspace_id, issue_id, runbook_id=runbook_id)
        self.runtime_service.validate_runtime_model(runtime, model)
        prompt = packet.prompt if not instruction else f"{packet.prompt}\n\nAdditional operator instruction:\n{instruction.strip()}"
        run = self.runtime_service.start_issue_run(
            workspace_id=workspace_id,
            workspace_path=Path(packet.workspace.root_path),
            issue_id=issue_id,
            runtime=runtime,
            model=model,
            prompt=prompt,
            worktree=packet.worktree,
            runbook_id=runbook_id,
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action="run.queued",
            summary=f"Queued {runtime} run for {issue_id}",
            actor=build_activity_actor("agent", runtime, runtime=runtime, model=model),
            issue_id=issue_id,
            run_id=run.run_id,
            details={"runtime": runtime, "model": model, "runbook_id": runbook_id},
        )
        return run.model_dump(mode="json")

    def probe_runtime(self, workspace_id: str, runtime: str, model: str) -> dict:
        workspace = self.get_workspace(workspace_id)
        result = self.runtime_service.probe_runtime(Path(workspace.root_path), runtime, model)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"runtime:{runtime}",
            action="runtime.probed",
            summary=f"Probed {runtime} runtime with {model}",
            actor=build_activity_actor("operator", "operator"),
            details={"runtime": runtime, "model": model, "ok": result.ok, "exit_code": result.exit_code},
        )
        return result.model_dump(mode="json")

    def start_agent_query(self, workspace_id: str, runtime: str, model: str, prompt: str) -> dict:
        workspace = self.get_workspace(workspace_id)
        trimmed_prompt = prompt.strip()
        if not trimmed_prompt:
            raise ValueError("Prompt is required")
        self.runtime_service.validate_runtime_model(runtime, model)
        run = self.runtime_service.start_issue_run(
            workspace_id=workspace_id,
            workspace_path=Path(workspace.root_path),
            issue_id="workspace-query",
            runtime=runtime,
            model=model,
            prompt=trimmed_prompt,
            worktree=self.read_worktree_status(workspace_id),
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action="run.query",
            summary=f"Queued {runtime} workspace query",
            actor=build_activity_actor("agent", runtime, runtime=runtime, model=model),
            issue_id=run.issue_id,
            run_id=run.run_id,
            details={"runtime": runtime, "model": model, "prompt_preview": trimmed_prompt[:160]},
        )
        return run.model_dump(mode="json")

    def list_runs(self, workspace_id: str) -> list[dict]:
        return [item.model_dump(mode="json") for item in self.store.list_runs(workspace_id)]

    def get_run(self, workspace_id: str, run_id: str) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        return run.model_dump(mode="json")

    def read_run_log(self, workspace_id: str, run_id: str, offset: int) -> dict:
        return self.runtime_service.read_run_log(workspace_id, run_id, offset)

    def cancel_run(self, workspace_id: str, run_id: str) -> dict:
        run = self.runtime_service.cancel_run(workspace_id, run_id)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action="run.cancelled",
            summary=f"Cancelled run {run.run_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=run.issue_id,
            run_id=run.run_id,
            details={"runtime": run.runtime, "model": run.model},
        )
        return run.model_dump(mode="json")

    def retry_run(self, workspace_id: str, run_id: str) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        self.runtime_service.validate_runtime_model(run.runtime, run.model)
        worktree = run.worktree or self.read_worktree_status(workspace_id)
        retried = self.runtime_service.start_issue_run(
            workspace_id=workspace_id,
            workspace_path=Path(self.get_workspace(workspace_id).root_path),
            issue_id=run.issue_id,
            runtime=run.runtime,
            model=run.model,
            prompt=run.prompt,
            worktree=worktree,
            runbook_id=run.runbook_id,
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=retried.run_id,
            action="run.retried",
            summary=f"Retried {run.runtime} run for {run.issue_id}",
            actor=build_activity_actor("agent", run.runtime, runtime=run.runtime, model=run.model),
            issue_id=run.issue_id,
            run_id=retried.run_id,
            details={"previous_run_id": run.run_id, "runtime": run.runtime, "model": run.model},
        )
        return retried.model_dump(mode="json")

    def _run_excerpt(self, run: RunRecord) -> str:
        parts: list[str] = []
        if isinstance(run.summary, dict):
            excerpt = run.summary.get("text_excerpt")
            if isinstance(excerpt, str) and excerpt.strip():
                parts.append(excerpt.strip())
        log_path = Path(run.log_path)
        if log_path.exists():
            try:
                tail = log_path.read_text(encoding="utf-8")[-4000:].strip()
            except OSError:
                tail = ""
            if tail:
                parts.append(tail)
        combined = "\n".join(part for part in parts if part).strip()
        return combined[:4000]

    def open_terminal(self, workspace_id: str, cols: int, rows: int, terminal_id: Optional[str]) -> dict:
        workspace = self.get_workspace(workspace_id)
        return self.terminal_service.open_terminal(workspace_id, Path(workspace.root_path), cols, rows, terminal_id)

    def terminal_write(self, terminal_id: str, data: str) -> None:
        self.terminal_service.write(terminal_id, data)

    def terminal_resize(self, terminal_id: str, cols: int, rows: int) -> None:
        self.terminal_service.resize(terminal_id, cols, rows)

    def terminal_close(self, terminal_id: str) -> None:
        self.terminal_service.close(terminal_id)

    def terminal_read(self, workspace_id: str, terminal_id: str, offset: int) -> dict:
        return self.terminal_service.read(workspace_id, terminal_id, offset)

    def export_workspace(self, workspace_id: str) -> ExportBundle:
        workspace = self.get_workspace(workspace_id)
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            snapshot = self.scan_workspace(workspace_id)
        return ExportBundle(
            workspace=workspace,
            snapshot=snapshot,
            runs=self.store.list_runs(workspace_id),
            fixes=self.store.list_fix_records(workspace_id),
            run_reviews=self.store.list_run_reviews(workspace_id),
            runbooks=self.list_runbooks(workspace_id),
            activity=self.store.list_activity(workspace_id),
        )

    def promote_signal(self, workspace_id: str, signal_id: str, request: PromoteSignalRequest) -> WorkspaceSnapshot:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        signal = next((item for item in snapshot.signals if item.signal_id == signal_id), None)
        if not signal:
            raise FileNotFoundError(signal_id)
        next_index = len(snapshot.issues) + 1
        bug_id = f"{request.severity or signal.severity}_AUTO_{next_index:03d}"
        issue = IssueRecord(
            bug_id=bug_id,
            title=request.title or signal.title,
            severity=request.severity or signal.severity,
            issue_status="triaged",
            source="signal",
            source_doc=signal.file_path,
            doc_status="open",
            code_status="open",
            summary=signal.summary,
            evidence=signal.evidence,
            labels=sorted(set(["auto-discovery", *request.labels, *signal.tags])),
            notes=f"Promoted from signal {signal.signal_id}",
            needs_followup=True,
            fingerprint=hashlib.sha1(f"{workspace_id}:{signal.signal_id}:{bug_id}".encode("utf-8")).hexdigest(),
        )
        signals = []
        for item in snapshot.signals:
            if item.signal_id == signal_id:
                signals.append(item.model_copy(update={"promoted_bug_id": bug_id}))
            else:
                signals.append(item)
        tracked = [item for item in self.store.list_tracker_issues(workspace_id) if item.bug_id != bug_id]
        tracked.append(issue)
        self.store.save_tracker_issues(workspace_id, tracked)
        updated = self.scan_workspace(workspace_id).model_copy(update={"signals": signals})
        self.store.save_snapshot(updated)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=bug_id,
            action="signal.promoted",
            summary=f"Promoted signal {signal.signal_id} into issue {bug_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=bug_id,
            details={"signal_id": signal.signal_id, "severity": issue.severity},
        )
        return updated

    def update_issue(self, workspace_id: str, issue_id: str, request: IssueUpdateRequest) -> IssueRecord:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        target = next((item for item in snapshot.issues if item.bug_id == issue_id), None)
        if not target:
            raise FileNotFoundError(issue_id)
        updates = {"updated_at": utc_now()}
        if request.severity is not None:
            updates["severity"] = request.severity.strip().upper()
        if request.issue_status is not None:
            updates["issue_status"] = request.issue_status
        if request.doc_status is not None:
            updates["doc_status"] = request.doc_status.strip()
        if request.code_status is not None:
            updates["code_status"] = request.code_status.strip()
        if request.labels is not None:
            updates["labels"] = sorted(set(label.strip() for label in request.labels if label.strip()))
        if request.notes is not None:
            updates["notes"] = request.notes.strip() or None
        if request.needs_followup is not None:
            updates["needs_followup"] = request.needs_followup
        updated_issue = target.model_copy(update=updates)
        updated_snapshot = snapshot.model_copy(
            update={"issues": [updated_issue if item.bug_id == issue_id else item for item in snapshot.issues]}
        )
        self.store.save_snapshot(updated_snapshot)
        if updated_issue.source == "tracker" or self._tracked_issue_exists(workspace_id, updated_issue.bug_id):
            self._persist_tracked_issue(workspace_id, updated_issue)
        else:
            self._persist_issue_override(workspace_id, updated_issue)
        changed_fields = {
            key: value
            for key, value in {
                "severity": request.severity.strip().upper() if request.severity is not None else None,
                "issue_status": request.issue_status,
                "doc_status": request.doc_status.strip() if request.doc_status is not None else None,
                "code_status": request.code_status.strip() if request.code_status is not None else None,
                "labels": sorted(set(label.strip() for label in request.labels if label.strip())) if request.labels is not None else None,
                "notes": request.notes.strip() or None if request.notes is not None else None,
                "needs_followup": request.needs_followup,
            }.items()
            if value is not None
        }
        before_after = {}
        for field, next_value in changed_fields.items():
            previous_value = getattr(target, field)
            if previous_value != next_value:
                before_after[field] = {"from": previous_value, "to": next_value}
        summary = f"Updated issue {issue_id}"
        if "severity" in before_after:
            summary = f"Updated issue {issue_id} severity {before_after['severity']['from']} -> {before_after['severity']['to']}"
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="issue.updated",
            summary=summary,
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"changes": changed_fields, "before_after": before_after},
        )
        return updated_issue

    def _tracked_issue_exists(self, workspace_id: str, issue_id: str) -> bool:
        return any(item.bug_id == issue_id for item in self.store.list_tracker_issues(workspace_id))

    def _persist_tracked_issue(self, workspace_id: str, issue: IssueRecord) -> None:
        tracked = self.store.list_tracker_issues(workspace_id)
        updated = [item for item in tracked if item.bug_id != issue.bug_id]
        updated.append(issue.model_copy(update={"source": issue.source if issue.source != "ledger" else "tracker"}))
        self.store.save_tracker_issues(workspace_id, updated)

    def _get_issue_from_snapshot(self, workspace_id: str, issue_id: str) -> IssueRecord:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            snapshot = self.scan_workspace(workspace_id)
        issue = next((item for item in snapshot.issues if item.bug_id == issue_id), None)
        if not issue:
            raise FileNotFoundError(issue_id)
        return issue

    def _next_tracker_issue_id(self, issues: list[IssueRecord]) -> str:
        highest = 0
        for issue in issues:
            if not issue.bug_id.startswith("TRK_"):
                continue
            try:
                highest = max(highest, int(issue.bug_id.split("_", 1)[1]))
            except (IndexError, ValueError):
                continue
        return f"TRK_{highest + 1:04d}"

    def _next_fix_id(self, workspace_id: str, issue_id: str, fixes: list[FixRecord]) -> str:
        digest = hashlib.sha1(f"{workspace_id}:{issue_id}:{len(fixes) + 1}:{utc_now()}".encode("utf-8")).hexdigest()[:8]
        return f"fix_{issue_id.lower()}_{digest}"

    def _merge_tracker_issues(self, imported: list[IssueRecord], tracked: list[IssueRecord]) -> list[IssueRecord]:
        merged = {item.bug_id: item for item in imported}
        for tracked_issue in tracked:
            existing = merged.get(tracked_issue.bug_id)
            if not existing:
                merged[tracked_issue.bug_id] = tracked_issue
                continue
            merged[tracked_issue.bug_id] = existing.model_copy(
                update={
                    "title": tracked_issue.title or existing.title,
                    "severity": tracked_issue.severity or existing.severity,
                    "issue_status": tracked_issue.issue_status,
                    "source": tracked_issue.source,
                    "source_doc": tracked_issue.source_doc or existing.source_doc,
                    "doc_status": tracked_issue.doc_status or existing.doc_status,
                    "code_status": tracked_issue.code_status or existing.code_status,
                    "summary": tracked_issue.summary or existing.summary,
                    "impact": tracked_issue.impact or existing.impact,
                    "evidence": tracked_issue.evidence or existing.evidence,
                    "verification_evidence": tracked_issue.verification_evidence or existing.verification_evidence,
                    "tests_added": tracked_issue.tests_added or existing.tests_added,
                    "tests_passed": tracked_issue.tests_passed or existing.tests_passed,
                    "labels": sorted(set([*existing.labels, *tracked_issue.labels])),
                    "notes": tracked_issue.notes or existing.notes,
                    "verified_at": tracked_issue.verified_at or existing.verified_at,
                    "verified_by": tracked_issue.verified_by or existing.verified_by,
                    "needs_followup": tracked_issue.needs_followup,
                    "fingerprint": tracked_issue.fingerprint or existing.fingerprint,
                    "updated_at": tracked_issue.updated_at,
                }
            )
        return sorted(merged.values(), key=lambda item: (item.severity, item.bug_id))

    def _build_tracker_sources(
        self,
        workspace_id: str,
        tracker_issues: list[IssueRecord],
        fixes: list[FixRecord],
        runbooks: list[RunbookRecord],
        run_reviews: list[RunReviewRecord],
    ) -> list[SourceRecord]:
        sources: list[SourceRecord] = []
        tracker_path = self.store.tracker_issues_path(workspace_id)
        if tracker_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(tracker_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="tracker_issue",
                    label=tracker_path.name,
                    path=str(tracker_path),
                    record_count=len(tracker_issues),
                    modified_at=utc_now(),
                    notes="Tracker-native issue records.",
                )
            )
        fix_path = self.store.fix_records_path(workspace_id)
        if fix_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(fix_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="fix_record",
                    label=fix_path.name,
                    path=str(fix_path),
                    record_count=len(fixes),
                    modified_at=utc_now(),
                    notes="Agent and operator fix history.",
                )
            )
        runbook_path = self.store.runbooks_path(workspace_id)
        if runbook_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(runbook_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="fix_record",
                    label=runbook_path.name,
                    path=str(runbook_path),
                    record_count=len(runbooks),
                    modified_at=utc_now(),
                    notes="Tracker-native reusable runbooks.",
                )
            )
        review_path = self.store.run_reviews_path(workspace_id)
        if review_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(review_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="fix_record",
                    label=review_path.name,
                    path=str(review_path),
                    record_count=len(run_reviews),
                    modified_at=utc_now(),
                    notes="Run review dispositions and review audit history.",
                )
            )
        return sources

    def _default_runbooks(self, workspace_id: str) -> list[RunbookRecord]:
        defaults = [
            (
                "verify",
                "Verify",
                "Validate reproduction and current failure shape before changing code.",
                "1. Reproduce or validate the reported behavior in the current tree.\n2. Confirm the cited evidence and likely failing surface.\n3. Stop after verification and return findings, impacted files, and candidate tests.",
            ),
            (
                "fix",
                "Fix",
                "Default bug-fix workflow with tests and tracker provenance.",
                "1. Verify the bug still reproduces in the current workspace tree.\n2. Inspect the cited evidence paths before changing code.\n3. Make the minimal safe fix.\n4. Add or update tests that fail before the fix and pass after the fix.\n5. Record a fix entry with files changed, tests run, and agent provenance.",
            ),
            (
                "reproduce",
                "Reproduce",
                "Focus only on reproduction, scope, and proof.",
                "1. Reproduce the reported bug.\n2. Narrow the failing boundary to concrete files and functions.\n3. Suggest the smallest next-step fix plan.\n4. Do not modify code.",
            ),
            (
                "drift-audit",
                "Drift Audit",
                "Compare tracker evidence, code state, and tests for drift.",
                "1. Check whether the tracker claim still matches code.\n2. Identify missing evidence, missing tests, or outdated status.\n3. Return only drift findings and recommended tracker updates.",
            ),
        ]
        return [
            RunbookRecord(
                runbook_id=runbook_id,
                workspace_id=workspace_id,
                name=name,
                description=description,
                scope="issue",
                template=template,
                built_in=True,
            )
            for runbook_id, name, description, template in defaults
        ]

    def _resolve_runbook(self, workspace_id: str, runbook_id: str) -> RunbookRecord:
        for runbook in self.list_runbooks(workspace_id):
            if runbook.runbook_id == runbook_id:
                return runbook
        raise FileNotFoundError(runbook_id)

    def _render_runbook_steps(self, template: str) -> list[str]:
        steps: list[str] = []
        normalized_template = template.replace("\\n", "\n")
        for raw_line in normalized_template.splitlines():
            line = raw_line.strip()
            if not line:
                continue
            line = re.sub(r"^[-*]\s*", "", line)
            line = re.sub(r"^\d+\.\s*", "", line)
            if line:
                steps.append(line)
        return steps

    def _slug_runbook_name(self, name: str) -> str:
        slug = re.sub(r"[^a-z0-9]+", "-", name.strip().lower()).strip("-")
        return slug or f"runbook-{hashlib.sha1(name.encode('utf-8')).hexdigest()[:8]}"

    def _build_prompt(
        self,
        workspace: WorkspaceRecord,
        issue: IssueRecord,
        tree_focus: list[str],
        recent_fixes: list[FixRecord],
        recent_activity: list[ActivityRecord],
    ) -> str:
        evidence_lines = []
        for evidence in issue.evidence[:8] + issue.verification_evidence[:8]:
            ref = evidence.path + (f":{evidence.line}" if evidence.line else "")
            evidence_lines.append(f"- {ref}")
        focus_lines = "\n".join(f"- {path}" for path in tree_focus[:12]) or "- Inspect the workspace tree around the bug."
        fix_lines = []
        for fix in recent_fixes[:4]:
            actor_label = fix.actor.label or fix.actor.name
            changed = ", ".join(fix.changed_files[:4]) or "no files recorded"
            fix_lines.append(f"- {fix.fix_id} [{fix.status}] by {actor_label}: {fix.summary} ({changed})")
        history_lines = []
        for entry in recent_activity[:6]:
            before_after = entry.details.get("before_after", {}) if isinstance(entry.details, dict) else {}
            if before_after:
                fragments = []
                for field, diff in before_after.items():
                    fragments.append(f"{field} {diff.get('from')} -> {diff.get('to')}")
                history_lines.append(f"- {entry.created_at}: {', '.join(fragments)}")
            else:
                history_lines.append(f"- {entry.created_at}: {entry.summary}")
        return (
            f"You are fixing bug {issue.bug_id} in workspace {workspace.root_path}.\n"
            f"Title: {issue.title}\n"
            f"Severity: {issue.severity}\n"
            f"Doc status: {issue.doc_status}\n"
            f"Code status: {issue.code_status}\n"
            f"Tracker source: {issue.source}\n"
            f"Summary: {issue.summary or 'No summary supplied.'}\n"
            f"Impact: {issue.impact or 'No impact supplied.'}\n\n"
            f"Evidence references:\n{chr(10).join(evidence_lines) if evidence_lines else '- None listed.'}\n\n"
            f"Recent issue history:\n{chr(10).join(history_lines) if history_lines else '- No recent issue history.'}\n\n"
            f"Prior fix history:\n{chr(10).join(fix_lines) if fix_lines else '- No prior fixes recorded.'}\n\n"
            f"Priority files:\n{focus_lines}\n\n"
            "Required workflow:\n"
            "1. Reproduce or validate the bug against the current code.\n"
            "2. Make the minimal safe fix.\n"
            "3. Add or update tests.\n"
            "4. Record exact files changed, tests run, and how the fix works back into the tracker.\n"
            "Return a concise engineering result, not a conversation."
        )

    def _draft_summary_from_excerpt(self, excerpt: str, issue_id: str, run_id: str) -> str:
        for raw_line in excerpt.splitlines():
            line = raw_line.strip().lstrip("-*0123456789. ").strip()
            if not line:
                continue
            if line.lower().startswith(("pytest", "npm test", "pnpm test", "yarn test", "cargo test", "go test")):
                continue
            return line[:160]
        return f"Review run {run_id} result for {issue_id}"

    def _extract_test_commands(self, text: str) -> list[str]:
        pattern = re.compile(
            r"((?:python3?\s+-m\s+pytest|pytest|uv\s+run\s+pytest|npm\s+test|pnpm\s+test|yarn\s+test|cargo\s+test|go\s+test)[^\n\r;]*)",
            re.IGNORECASE,
        )
        commands: list[str] = []
        seen: set[str] = set()
        for match in pattern.finditer(text):
            command = " ".join(match.group(1).strip().split())
            if command and command not in seen:
                seen.add(command)
                commands.append(command[:220])
        return commands[:6]

    def _extract_changed_files(self, text: str) -> list[str]:
        pattern = re.compile(r"\b(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\.(?:py|ts|tsx|js|jsx|go|rs|md|json|yaml|yml|css)\b")
        files: list[str] = []
        seen: set[str] = set()
        for match in pattern.finditer(text):
            path = match.group(0)
            if path not in seen:
                seen.add(path)
                files.append(path)
        return files[:8]

    def _apply_issue_drift(self, root: Path, issues: list[IssueRecord]) -> list[IssueRecord]:
        updated: list[IssueRecord] = []
        for issue in issues:
            drift_flags = list(issue.drift_flags)
            if issue.doc_status == "partial":
                drift_flags.append("partial_fix")
            if not issue.tests_passed:
                drift_flags.append("missing_verification_tests")
            for evidence in [*issue.evidence, *issue.verification_evidence]:
                if not evidence.path:
                    continue
                evidence_path = root / (evidence.normalized_path or evidence.path)
                if evidence.path_exists is False or not evidence_path.exists():
                    drift_flags.append(f"missing_evidence:{evidence.path}")
            updated.append(issue.model_copy(update={"drift_flags": sorted(set(drift_flags))}))
        return updated

    def _normalize_issue_evidence(self, root: Path, issue: IssueRecord) -> IssueRecord:
        def normalize(items):
            normalized_items = []
            for item in items:
                normalized_path = item.normalized_path or item.path.lstrip("./")
                candidate = root / normalized_path
                normalized_items.append(
                    item.model_copy(
                        update={
                            "normalized_path": normalized_path,
                            "path_exists": candidate.exists(),
                            "path_scope": "repo-relative",
                        }
                    )
                )
            return normalized_items

        return issue.model_copy(
            update={
                "evidence": normalize(issue.evidence),
                "verification_evidence": normalize(issue.verification_evidence),
            }
        )

    def _build_drift_summary(self, issues: list[IssueRecord]) -> dict[str, int]:
        summary: dict[str, int] = {}
        for issue in issues:
            for flag in issue.drift_flags:
                bucket = flag.split(":", 1)[0]
                summary[bucket] = summary.get(bucket, 0) + 1
        return dict(sorted(summary.items(), key=lambda item: (-item[1], item[0])))

    def _annotate_review_ready(
        self,
        issues: list[IssueRecord],
        runs: list[RunRecord],
        fixes: list[FixRecord],
        run_reviews: list[RunReviewRecord],
    ) -> list[IssueRecord]:
        fixed_run_ids = {item.run_id for item in fixes if item.run_id}
        reviewed_run_ids = {item.run_id for item in run_reviews}
        pending_by_issue: dict[str, list[str]] = {}
        for run in runs:
            if run.issue_id == "workspace-query":
                continue
            if run.status != "completed":
                continue
            if run.run_id in fixed_run_ids:
                continue
            if run.run_id in reviewed_run_ids:
                continue
            pending_by_issue.setdefault(run.issue_id, []).append(run.run_id)
        updated: list[IssueRecord] = []
        for issue in issues:
            pending = pending_by_issue.get(issue.bug_id, [])
            updated.append(
                issue.model_copy(
                    update={
                        "review_ready_count": len(pending),
                        "review_ready_runs": pending[:8],
                    }
                )
            )
        return updated

    def _apply_issue_overrides(self, workspace_id: str, issues: list[IssueRecord]) -> list[IssueRecord]:
        overrides = self.store.load_issue_overrides(workspace_id)
        updated: list[IssueRecord] = []
        for issue in issues:
            override = overrides.get(issue.bug_id)
            if not override:
                updated.append(issue)
                continue
            mutable = {
                key: override[key]
                for key in ("severity", "issue_status", "doc_status", "code_status", "labels", "notes", "needs_followup", "updated_at")
                if key in override
            }
            updated.append(issue.model_copy(update=mutable))
        return updated

    def _persist_issue_override(self, workspace_id: str, issue: IssueRecord) -> None:
        overrides = self.store.load_issue_overrides(workspace_id)
        overrides[issue.bug_id] = {
            "severity": issue.severity,
            "issue_status": issue.issue_status,
            "doc_status": issue.doc_status,
            "code_status": issue.code_status,
            "labels": issue.labels,
            "notes": issue.notes,
            "needs_followup": issue.needs_followup,
            "updated_at": issue.updated_at,
        }
        self.store.save_issue_overrides(workspace_id, overrides)

    def _record_activity(
        self,
        workspace_id: str,
        entity_type: str,
        entity_id: str,
        action: str,
        summary: str,
        actor: ActivityActor,
        issue_id: Optional[str] = None,
        run_id: Optional[str] = None,
        details: Optional[dict] = None,
    ) -> None:
        activity = ActivityRecord(
            activity_id=hashlib.sha1(f"{workspace_id}:{entity_type}:{entity_id}:{action}:{utc_now()}".encode("utf-8")).hexdigest()[:16],
            workspace_id=workspace_id,
            entity_type=entity_type,
            entity_id=entity_id,
            action=action,
            summary=summary,
            actor=actor,
            issue_id=issue_id,
            run_id=run_id,
            details=details or {},
        )
        self.store.append_activity(activity)
