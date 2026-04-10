from __future__ import annotations

from collections import Counter
import hashlib
import json
import re
import subprocess
import time
import uuid
from pathlib import Path
from typing import Optional
from urllib.parse import quote

from .models import (
    ActivityActor,
    ActivityOverview,
    ActivityRecord,
    ActivityRollupItem,
    AppSettings,
    CostSummary,
    CoverageDelta,
    CoverageResult,
    DuplicateMatch,
    EvidenceRef,
    ExportBundle,
    FixRecord,
    FixDraftSuggestion,
    FixRecordRequest,
    FixUpdateRequest,
    IssueDriftDetail,
    IssueCreateRequest,
    IssueContextPacket,
    IssueQualityScore,
    IssueRecord,
    IssueUpdateRequest,
    PlanApproveRequest,
    PlanPhase,
    PlanRejectRequest,
    PlanStep,
    PromoteSignalRequest,
    RunMetrics,
    RunPlan,
    RunRecord,
    RunReviewRecord,
    RunReviewRequest,
    RunAcceptRequest,
    RunbookRecord,
    RunbookUpsertRequest,
    ReviewQueueItem,
    SavedIssueView,
    SavedIssueViewRequest,
    SourceRecord,
    TestSuggestion,
    TriageSuggestion,
    VerificationRecord,
    VerificationSummary,
    VerifyIssueRequest,
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

    def list_verifications(self, workspace_id: str, issue_id: Optional[str] = None) -> list[VerificationRecord]:
        self.get_workspace(workspace_id)
        records = self.store.list_verifications(workspace_id)
        if issue_id:
            records = [item for item in records if item.issue_id == issue_id]
        return records

    def verify_issue_three_pass(self, workspace_id: str, issue_id: str, request: VerifyIssueRequest) -> VerificationSummary:
        packet = self.build_issue_context(workspace_id, issue_id)
        models = request.models or self._default_verification_models(request.runtime)
        if not models:
            raise ValueError(f"No verification models available for runtime {request.runtime}")

        records: list[VerificationRecord] = []
        for model in models:
            self.runtime_service.validate_runtime_model(request.runtime, model)
            prompt = self._build_verification_prompt(packet, request.instruction)
            run = self.runtime_service.start_issue_run(
                workspace_id=workspace_id,
                workspace_path=Path(packet.workspace.root_path),
                issue_id=issue_id,
                runtime=request.runtime,
                model=model,
                prompt=prompt,
                worktree=packet.worktree,
                runbook_id=request.runbook_id or "verify",
            )
            self._record_activity(
                workspace_id=workspace_id,
                entity_type="run",
                entity_id=run.run_id,
                action="run.queued",
                summary=f"Queued {request.runtime} verification run for {issue_id}",
                actor=build_activity_actor("agent", request.runtime, runtime=request.runtime, model=model),
                issue_id=issue_id,
                run_id=run.run_id,
                details={"runtime": request.runtime, "model": model, "runbook_id": request.runbook_id or "verify"},
            )
            run_id = run.run_id
            deadline = time.monotonic() + max(request.timeout_seconds, 1.0)
            state = run.model_dump(mode="json")
            while time.monotonic() < deadline:
                time.sleep(max(request.poll_interval, 0.2))
                state = self.get_run(workspace_id, run_id)
                if state["status"] in {"completed", "failed", "cancelled"}:
                    break
            if state["status"] not in {"completed", "failed", "cancelled"}:
                state = self.cancel_run(workspace_id, run_id)

            run = self._load_run_for_verification(workspace_id, run_id)
            excerpt = self._run_excerpt(run) if run else ""
            parsed = self._parse_verification_excerpt(excerpt)
            verification = VerificationRecord(
                verification_id=hashlib.sha1(f"{workspace_id}:{issue_id}:{run_id}:{utc_now()}".encode("utf-8")).hexdigest()[:16],
                workspace_id=workspace_id,
                issue_id=issue_id,
                run_id=run_id,
                runtime=request.runtime,
                model=model,
                code_checked=self._normalize_verification_state(parsed.get("code_checked")),
                fixed=self._normalize_verification_state(parsed.get("fixed")),
                confidence=self._normalize_confidence(parsed.get("confidence")),
                summary=self._verification_summary_text(parsed, excerpt, run_id, model),
                evidence=self._normalize_string_list(parsed.get("evidence")),
                tests=self._normalize_string_list(parsed.get("tests")),
                actor=build_activity_actor("agent", request.runtime, runtime=request.runtime, model=model),
                raw_excerpt=excerpt or None,
            )
            records.append(verification)

        existing = self.store.list_verifications(workspace_id)
        self.store.save_verifications(workspace_id, [*existing, *records])
        for record in records:
            self._record_activity(
                workspace_id=workspace_id,
                entity_type="run",
                entity_id=record.run_id,
                action="verification.recorded",
                summary=f"Recorded verification pass for {issue_id} with {record.model}",
                actor=record.actor,
                issue_id=issue_id,
                run_id=record.run_id,
                details={
                    "code_checked": record.code_checked,
                    "fixed": record.fixed,
                    "confidence": record.confidence,
                    "summary": record.summary,
                },
            )
        return self._summarize_verifications(workspace_id, issue_id, records)

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
        planning: bool = False,
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
            wait_for_approval=planning,
        )
        action = "run.planning" if planning else "run.queued"
        summary = f"Started {runtime} run with planning for {issue_id}" if planning else f"Queued {runtime} run for {issue_id}"
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action=action,
            summary=summary,
            actor=build_activity_actor("agent", runtime, runtime=runtime, model=model),
            issue_id=issue_id,
            run_id=run.run_id,
            details={"runtime": runtime, "model": model, "runbook_id": runbook_id, "planning": planning},
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

    def generate_run_plan(self, workspace_id: str, run_id: str) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if run.status not in {"queued", "planning"}:
            raise ValueError(f"Cannot generate plan for run in status {run.status}")
        packet = self.issue_work(workspace_id, run.issue_id, runbook_id=run.runbook_id)
        plan_prompt = self._build_planning_prompt(packet)
        plan_result = self._call_agent_for_plan(run.runtime, run.model, plan_prompt, Path(packet.workspace.root_path))
        plan = RunPlan(
            plan_id=f"plan_{uuid.uuid4().hex[:12]}",
            run_id=run_id,
            phase="awaiting_approval",
            steps=plan_result.get("steps", []),
            summary=plan_result.get("summary", ""),
            reasoning=plan_result.get("reasoning"),
            created_at=utc_now(),
        )
        updated_run = run.model_copy(update={"status": "planning", "plan": plan})
        self.store.save_run(updated_run)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="run.plan_generated",
            summary=f"Generated plan for {run.issue_id}",
            actor=build_activity_actor("agent", run.runtime, runtime=run.runtime, model=run.model),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"plan_id": plan.plan_id, "step_count": len(plan.steps)},
        )
        return plan.model_dump(mode="json")

    def get_run_plan(self, workspace_id: str, run_id: str) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if not run.plan:
            raise FileNotFoundError(f"No plan found for run {run_id}")
        return run.plan.model_dump(mode="json")

    def approve_run_plan(self, workspace_id: str, run_id: str, request: PlanApproveRequest) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if not run.plan:
            raise FileNotFoundError(f"No plan found for run {run_id}")
        if run.plan.phase not in {"awaiting_approval", "modified"}:
            raise ValueError(f"Plan is not awaiting approval (phase: {run.plan.phase})")
        workspace = self.get_workspace(workspace_id)
        modified_summary = request.feedback if request.feedback and run.plan.phase == "modified" else None
        updated_plan = run.plan.model_copy(update={
            "phase": "approved",
            "approved_at": utc_now(),
            "approver": "operator",
            "feedback": request.feedback if request.feedback else None,
            "modified_summary": modified_summary,
        })
        updated_run = run.model_copy(update={"status": "queued", "plan": updated_plan})
        self.store.save_run(updated_run)
        self.runtime_service.start_approved_run(updated_run, Path(workspace.root_path))
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="run.plan_approved",
            summary=f"Approved plan for {run.issue_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"feedback": request.feedback},
        )
        return updated_plan.model_dump(mode="json")

    def reject_run_plan(self, workspace_id: str, run_id: str, request: PlanRejectRequest) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if not run.plan:
            raise FileNotFoundError(f"No plan found for run {run_id}")
        updated_plan = run.plan.model_copy(update={
            "phase": "rejected",
            "feedback": request.reason,
        })
        updated_run = run.model_copy(update={"status": "cancelled", "plan": updated_plan})
        self.store.save_run(updated_run)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="run.plan_rejected",
            summary=f"Rejected plan for {run.issue_id}: {request.reason}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"reason": request.reason},
        )
        return updated_plan.model_dump(mode="json")

    def _build_planning_prompt(self, packet: IssueContextPacket) -> str:
        return f"""You are a bug fixing assistant. Generate a structured plan to address the following issue.

ISSUE: {packet.issue.bug_id}
Title: {packet.issue.title}
Severity: {packet.issue.severity}
Summary: {packet.issue.summary or 'No summary provided'}
Impact: {packet.issue.impact or 'No impact provided'}

Evidence:
{chr(10).join(f"  - {e.path}:{e.line} - {e.excerpt}" if e.excerpt else f"  - {e.path}:{e.line}" for e in packet.evidence_bundle[:5])}

Recent fixes for context:
{chr(10).join(f"  - {f.summary} ({f.status})" for f in packet.recent_fixes[:3])}

Your task is to generate a concise fix plan. Consider:
1. What files need to be modified
2. What the actual fix should be
3. What tests should be added or updated
4. What risks this fix might introduce

Respond with a JSON object containing:
{{
  "summary": "Brief one-line summary of the fix approach",
  "reasoning": "Brief explanation of why this approach was chosen",
  "steps": [
    {{
      "step_id": "step_1",
      "description": "What to do in this step",
      "estimated_impact": "low|medium|high",
      "files_affected": ["file1.py", "file2.py"],
      "risks": ["risk1", "risk2"]
    }}
  ]
}}
"""

    def _call_agent_for_plan(self, runtime: str, model: str, prompt: str, workspace_path: Path) -> dict:
        settings = self.store.load_settings()
        if runtime == "codex":
            codex_bin = self.runtime_service._resolve_binary(settings.codex_bin, "codex") or "codex"
            command = [codex_bin, "exec", "--json", "--skip-git-repo-check", "-s", "workspace-write", "-C", str(workspace_path), "-m", model, prompt]
        else:
            opencode_bin = self.runtime_service._resolve_binary(settings.opencode_bin, "opencode") or "opencode"
            command = [opencode_bin, "run", "--format", "json", "--dir", str(workspace_path), "-m", model, prompt]
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=60, check=False)
            output = result.stdout + result.stderr
            for line in output.splitlines():
                try:
                    parsed = json.loads(line.strip())
                    if isinstance(parsed, dict) and "summary" in parsed:
                        return parsed
                except json.JSONDecodeError:
                    continue
            text = output.strip()
            if text.startswith("{") and text.endswith("}"):
                try:
                    return json.loads(text)
                except json.JSONDecodeError:
                    pass
            return {"summary": text[:500], "reasoning": None, "steps": []}
        except subprocess.TimeoutExpired:
            return {"summary": "Planning timed out", "reasoning": None, "steps": []}
        except Exception as exc:
            return {"summary": f"Planning failed: {exc}", "reasoning": None, "steps": []}

    def get_run_metrics(self, workspace_id: str, run_id: str) -> dict:
        metrics = self.runtime_service.load_run_metrics(run_id)
        if not metrics:
            raise FileNotFoundError(f"No metrics found for run {run_id}")
        return metrics.model_dump(mode="json")

    def get_workspace_cost_summary(self, workspace_id: str) -> dict:
        metrics_list = self.runtime_service.get_workspace_metrics(workspace_id)
        runs = self.store.list_runs(workspace_id)
        runs_by_status: dict[str, int] = {}
        cost_by_runtime: dict[str, float] = {}
        cost_by_model: dict[str, float] = {}
        total_input_tokens = 0
        total_output_tokens = 0
        total_estimated_cost = 0.0
        total_duration_ms = 0
        for m in metrics_list:
            total_input_tokens += m.input_tokens
            total_output_tokens += m.output_tokens
            total_estimated_cost += m.estimated_cost
            total_duration_ms += m.duration_ms
            cost_by_runtime[m.runtime] = cost_by_runtime.get(m.runtime, 0.0) + m.estimated_cost
            cost_by_model[m.model] = cost_by_model.get(m.model, 0.0) + m.estimated_cost
        for run in runs:
            status = run.status
            runs_by_status[status] = runs_by_status.get(status, 0) + 1
        return CostSummary(
            workspace_id=workspace_id,
            total_runs=len(runs),
            total_input_tokens=total_input_tokens,
            total_output_tokens=total_output_tokens,
            total_estimated_cost=round(total_estimated_cost, 6),
            total_duration_ms=total_duration_ms,
            runs_by_status=runs_by_status,
            cost_by_runtime=cost_by_runtime,
            cost_by_model=cost_by_model,
        ).model_dump(mode="json")

    def score_issue_quality(self, workspace_id: str, issue_id: str) -> dict:
        issue = self._get_issue(workspace_id, issue_id)
        score = self._calculate_quality_score(workspace_id, issue)
        self._save_quality_score(score)
        return score.model_dump(mode="json")

    def score_all_issues(self, workspace_id: str) -> list[dict]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        results = []
        for issue in snapshot.issues:
            score = self._calculate_quality_score(workspace_id, issue)
            self._save_quality_score(score)
            results.append(score.model_dump(mode="json"))
        return results

    def get_issue_quality(self, workspace_id: str, issue_id: str) -> dict:
        score = self._load_quality_score(workspace_id, issue_id)
        if not score:
            return self.score_issue_quality(workspace_id, issue_id)
        return score.model_dump(mode="json")

    def find_duplicates(self, workspace_id: str, issue_id: str) -> list[dict]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        source = self._get_issue(workspace_id, issue_id)
        matches: list[DuplicateMatch] = []
        for candidate in snapshot.issues:
            if candidate.bug_id == source.bug_id:
                continue
            sim = self._compute_similarity(source, candidate)
            if sim.similarity >= 0.3:
                matches.append(sim)
        matches.sort(key=lambda m: m.similarity, reverse=True)
        return [m.model_dump(mode="json") for m in matches[:10]]

    def triage_issue(self, workspace_id: str, issue_id: str) -> dict:
        issue = self._get_issue(workspace_id, issue_id)
        suggestion = self._generate_triage_suggestion(workspace_id, issue)
        return suggestion.model_dump(mode="json")

    def triage_all_issues(self, workspace_id: str) -> list[dict]:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        results = []
        for issue in snapshot.issues:
            suggestion = self._generate_triage_suggestion(workspace_id, issue)
            results.append(suggestion.model_dump(mode="json"))
        return results

    def _calculate_quality_score(self, workspace_id: str, issue: IssueRecord) -> IssueQualityScore:
        suggestions: list[str] = []
        has_repro = bool(issue.summary and len(issue.summary.strip()) > 20)
        has_severity = bool(issue.severity and issue.severity in {"critical", "high", "medium", "low"})
        has_evidence = len(issue.evidence) > 0
        has_impact = bool(issue.impact and len(issue.impact.strip()) > 10)
        has_summary = bool(issue.summary and len(issue.summary.strip()) > 0)
        completeness = 0.0
        if has_severity:
            completeness += 0.2
        else:
            suggestions.append("Add a severity rating")
        if has_repro:
            completeness += 0.25
        else:
            suggestions.append("Add reproduction steps to the summary (20+ chars)")
        if has_evidence:
            completeness += 0.25
        else:
            suggestions.append("Add code evidence (file path and line references)")
        if has_impact:
            completeness += 0.15
        else:
            suggestions.append("Describe the impact of this issue")
        if has_summary:
            completeness += 0.15
        clarity = 0.0
        title_len = len(issue.title.strip())
        if title_len >= 10:
            clarity += 0.3
        elif title_len >= 5:
            clarity += 0.15
        else:
            suggestions.append("Title is too short - be more descriptive")
        summary_len = len((issue.summary or "").strip())
        if summary_len >= 50:
            clarity += 0.4
        elif summary_len >= 20:
            clarity += 0.2
        else:
            suggestions.append("Summary needs more detail (50+ chars recommended)")
        if title_len > 0 and not issue.title.isupper() and not issue.title.islower():
            clarity += 0.15
        if len(issue.labels) > 0:
            clarity += 0.15
        else:
            suggestions.append("Add labels to categorize the issue")
        evidence_quality = 0.0
        evidence_count = len(issue.evidence)
        if evidence_count > 0:
            evidence_quality += min(0.4, evidence_count * 0.1)
            has_line_refs = any(e.line is not None for e in issue.evidence)
            if has_line_refs:
                evidence_quality += 0.3
            has_excerpts = any(e.excerpt for e in issue.evidence)
            if has_excerpts:
                evidence_quality += 0.3
        else:
            evidence_quality = 0.0
        overall = round((completeness * 0.4 + clarity * 0.35 + evidence_quality * 0.25) * 100, 1)
        return IssueQualityScore(
            issue_id=issue.bug_id,
            workspace_id=workspace_id,
            overall=overall,
            completeness=round(completeness * 100, 1),
            clarity=round(clarity * 100, 1),
            evidence_quality=round(evidence_quality * 100, 1),
            has_repro=has_repro,
            has_severity=has_severity,
            has_evidence=has_evidence,
            has_impact=has_impact,
            has_summary=has_summary,
            title_length=title_len,
            summary_length=summary_len,
            evidence_count=evidence_count,
            suggestions=suggestions,
        )

    def _save_quality_score(self, score: IssueQualityScore) -> None:
        scores_dir = self.store.data_dir / "quality_scores"
        scores_dir.mkdir(parents=True, exist_ok=True)
        path = scores_dir / f"{score.issue_id}.json"
        path.write_text(score.model_dump_json(), encoding="utf-8")

    def _load_quality_score(self, workspace_id: str, issue_id: str) -> Optional[IssueQualityScore]:
        scores_dir = self.store.data_dir / "quality_scores"
        path = scores_dir / f"{issue_id}.json"
        if not path.exists():
            return None
        try:
            return IssueQualityScore.model_validate_json(path.read_text(encoding="utf-8"))
        except Exception:
            return None

    def _get_issue(self, workspace_id: str, issue_id: str) -> IssueRecord:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        for issue in snapshot.issues:
            if issue.bug_id == issue_id:
                return issue
        raise FileNotFoundError(f"Issue {issue_id} not found in workspace {workspace_id}")

    def _compute_similarity(self, a: IssueRecord, b: IssueRecord) -> DuplicateMatch:
        shared: list[str] = []
        score = 0.0
        if a.fingerprint and b.fingerprint and a.fingerprint == b.fingerprint:
            return DuplicateMatch(
                source_id=a.bug_id, target_id=b.bug_id, similarity=1.0,
                match_type="fingerprint", shared_fields=["fingerprint"],
            )
        if a.title and b.title:
            title_sim = self._text_similarity(a.title.lower(), b.title.lower())
            if title_sim > 0.5:
                score += title_sim * 0.35
                shared.append("title")
        if a.summary and b.summary:
            sum_sim = self._text_similarity(a.summary.lower(), b.summary.lower())
            if sum_sim > 0.4:
                score += sum_sim * 0.25
                shared.append("summary")
        if a.severity == b.severity and a.severity:
            score += 0.1
            shared.append("severity")
        a_paths = {e.path for e in a.evidence}
        b_paths = {e.path for e in b.evidence}
        overlap = a_paths & b_paths
        if overlap:
            path_sim = len(overlap) / max(len(a_paths | b_paths), 1)
            score += path_sim * 0.2
            shared.append("evidence_paths")
        a_labels = set(a.labels)
        b_labels = set(b.labels)
        label_overlap = a_labels & b_labels
        if label_overlap:
            label_sim = len(label_overlap) / max(len(a_labels | b_labels), 1)
            score += label_sim * 0.1
            shared.append("labels")
        match_type = "exact" if score >= 0.95 else "fuzzy"
        return DuplicateMatch(
            source_id=a.bug_id, target_id=b.bug_id,
            similarity=round(min(score, 1.0), 3),
            match_type=match_type, shared_fields=shared,
        )

    def _text_similarity(self, a: str, b: str) -> float:
        if not a or not b:
            return 0.0
        a_words = set(a.split())
        b_words = set(b.split())
        intersection = a_words & b_words
        union = a_words | b_words
        if not union:
            return 0.0
        return len(intersection) / len(union)

    def _generate_triage_suggestion(self, workspace_id: str, issue: IssueRecord) -> TriageSuggestion:
        suggested_severity: Optional[str] = None
        suggested_labels: list[str] = list(issue.labels)
        reasoning_parts: list[str] = []
        confidence = 0.0
        if not issue.severity or issue.severity == "medium":
            if issue.impact and any(w in issue.impact.lower() for w in ("crash", "data loss", "security", "rce", "injection")):
                suggested_severity = "critical"
                reasoning_parts.append("Impact mentions critical keywords (crash/security/data loss)")
                confidence += 0.3
            elif issue.impact and any(w in issue.impact.lower() for w in ("break", "fail", "error", "wrong")):
                suggested_severity = "high"
                reasoning_parts.append("Impact mentions functional breakage")
                confidence += 0.25
            elif len(issue.evidence) >= 3:
                suggested_severity = "high"
                reasoning_parts.append("Multiple evidence references suggest higher severity")
                confidence += 0.2
        if "exception_swallow" in str(issue.drift_flags):
            if "silent-failure" not in suggested_labels:
                suggested_labels.append("silent-failure")
            reasoning_parts.append("Exception swallowing detected")
            confidence += 0.15
        if any(e.path and e.path.endswith((".test.", "_test.", ".spec.", ".e2e.")) for e in issue.evidence):
            if "test-related" not in suggested_labels:
                suggested_labels.append("test-related")
            reasoning_parts.append("Evidence in test files")
            confidence += 0.1
        if issue.source == "verdict":
            if "verdict-derived" not in suggested_labels:
                suggested_labels.append("verdict-derived")
            confidence += 0.1
        if not suggested_labels and not issue.labels:
            if "needs-triage" not in suggested_labels:
                suggested_labels.append("needs-triage")
            reasoning_parts.append("No labels assigned yet")
        return TriageSuggestion(
            issue_id=issue.bug_id,
            workspace_id=workspace_id,
            suggested_severity=suggested_severity,
            suggested_labels=suggested_labels,
            confidence=min(round(confidence, 2), 1.0),
            reasoning="; ".join(reasoning_parts) if reasoning_parts else "No specific triage suggestions",
        )

    def parse_coverage_report(self, workspace_id: str, report_path: str, run_id: Optional[str] = None, issue_id: Optional[str] = None) -> dict:
        workspace = self.get_workspace(workspace_id)
        full_path = Path(workspace.root_path) / report_path
        if not full_path.exists():
            raise FileNotFoundError(f"Coverage report not found: {report_path}")
        result = self._parse_coverage_file(full_path, workspace_id, run_id, issue_id)
        self._save_coverage_result(result)
        if issue_id:
            self._record_activity(
                workspace_id=workspace_id,
                entity_type="issue",
                entity_id=issue_id,
                action="coverage.parsed",
                summary=f"Parsed coverage report: {result.line_coverage:.1f}% lines",
                actor=build_activity_actor("system", "system"),
                issue_id=issue_id,
                run_id=run_id,
                details={"line_coverage": result.line_coverage, "files_covered": result.files_covered},
            )
        return result.model_dump(mode="json")

    def get_coverage(self, workspace_id: str, issue_id: Optional[str] = None, run_id: Optional[str] = None) -> Optional[dict]:
        result = self._load_latest_coverage(workspace_id, issue_id, run_id)
        if not result:
            return None
        return result.model_dump(mode="json")

    def get_coverage_delta(self, workspace_id: str, issue_id: str) -> dict:
        results = self._load_coverage_results(workspace_id, issue_id)
        if len(results) < 2:
            baseline = results[0] if results else None
            return CoverageDelta(
                workspace_id=workspace_id, issue_id=issue_id,
                baseline=baseline, current=None,
                line_delta=baseline.line_coverage if baseline else 0.0,
            ).model_dump(mode="json")
        results.sort(key=lambda r: r.created_at)
        baseline = results[0]
        current = results[-1]
        line_delta = round(current.line_coverage - baseline.line_coverage, 2)
        branch_delta = None
        if baseline.branch_coverage is not None and current.branch_coverage is not None:
            branch_delta = round(current.branch_coverage - baseline.branch_coverage, 2)
        baseline_uncovered = set(baseline.uncovered_files)
        current_uncovered = set(current.uncovered_files)
        new_covered = baseline_uncovered - current_uncovered
        regressed = current_uncovered - baseline_uncovered
        return CoverageDelta(
            workspace_id=workspace_id, issue_id=issue_id,
            baseline=baseline, current=current,
            line_delta=line_delta, branch_delta=branch_delta,
            lines_added=max(0, current.lines_covered - baseline.lines_covered),
            lines_lost=max(0, baseline.lines_covered - current.lines_covered),
            new_files_covered=sorted(new_covered),
            files_regressed=sorted(regressed),
        ).model_dump(mode="json")

    def generate_test_suggestions(self, workspace_id: str, issue_id: str) -> list[dict]:
        issue = self._get_issue(workspace_id, issue_id)
        workspace = self.get_workspace(workspace_id)
        suggestions = self._build_test_suggestions(workspace_id, issue, workspace)
        self._save_test_suggestions(suggestions)
        return [s.model_dump(mode="json") for s in suggestions]

    def get_test_suggestions(self, workspace_id: str, issue_id: str) -> list[dict]:
        suggestions = self._load_test_suggestions(workspace_id, issue_id)
        return [s.model_dump(mode="json") for s in suggestions]

    def _parse_coverage_file(self, path: Path, workspace_id: str, run_id: Optional[str], issue_id: Optional[str]) -> CoverageResult:
        content = path.read_text(encoding="utf-8")
        fmt = "unknown"
        if path.suffix == ".xml":
            fmt = "cobertura"
            return self._parse_cobertura(content, workspace_id, run_id, issue_id, fmt, str(path))
        if path.suffix == ".json":
            try:
                data = json.loads(content)
                if "coverage" in data or "source_files" in data:
                    fmt = "istanbul"
                    return self._parse_istanbul(data, workspace_id, run_id, issue_id, fmt, str(path))
            except json.JSONDecodeError:
                pass
        if path.suffix in (".csv", ".txt", ""):
            fmt = "lcov"
            return self._parse_lcov(content, workspace_id, run_id, issue_id, fmt, str(path))
        return CoverageResult(
            result_id=f"cov_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id, run_id=run_id, issue_id=issue_id,
            format=fmt, raw_report_path=str(path),
        )

    def _parse_cobertura(self, content: str, workspace_id: str, run_id: Optional[str], issue_id: Optional[str], fmt: str, path: str) -> CoverageResult:
        import xml.etree.ElementTree as ET
        try:
            root = ET.fromstring(content)
        except ET.ParseError:
            return CoverageResult(result_id=f"cov_{uuid.uuid4().hex[:12]}", workspace_id=workspace_id, run_id=run_id, issue_id=issue_id, format=fmt, raw_report_path=path)
        line_rate = float(root.attrib.get("line-rate", 0))
        branch_rate = root.attrib.get("branch-rate")
        branch_cov = float(branch_rate) if branch_rate else None
        lines_covered = 0
        lines_total = 0
        branches_covered = 0
        branches_total = 0
        files_covered = 0
        files_total = 0
        uncovered = []
        for pkg in root.iter("package"):
            for cls in pkg.iter("class"):
                files_total += 1
                fname = cls.attrib.get("filename", "")
                cls_lines = 0
                cls_total = 0
                for line_el in cls.iter("line"):
                    hits = int(line_el.attrib.get("hits", 0))
                    cls_total += 1
                    if hits > 0:
                        cls_lines += 1
                lines_covered += cls_lines
                lines_total += cls_total
                if cls_lines > 0:
                    files_covered += 1
                else:
                    if fname:
                        uncovered.append(fname)
        return CoverageResult(
            result_id=f"cov_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id, run_id=run_id, issue_id=issue_id,
            line_coverage=round(line_rate * 100, 2),
            branch_coverage=round(branch_cov * 100, 2) if branch_cov is not None else None,
            lines_covered=lines_covered, lines_total=lines_total,
            branches_covered=branches_covered or None, branches_total=branches_total or None,
            files_covered=files_covered, files_total=files_total,
            uncovered_files=uncovered[:50], format=fmt, raw_report_path=path,
        )

    def _parse_istanbul(self, data: dict, workspace_id: str, run_id: Optional[str], issue_id: Optional[str], fmt: str, path: str) -> CoverageResult:
        total_lines = 0
        covered_lines = 0
        files_covered = 0
        files_total = 0
        uncovered = []
        source_files = data.get("source_files", data.get("coverage", {}))
        if isinstance(source_files, dict):
            source_files = [{"path": k, **v} for k, v in source_files.items()]
        for sf in source_files:
            files_total += 1
            fpath = sf.get("path", sf.get("file", ""))
            s_data = sf.get("s", sf.get("statementMap", {}))
            if isinstance(s_data, dict):
                hit_count = sum(1 for v in s_data.values() if (isinstance(v, int) and v > 0) or (isinstance(v, dict) and v.get("executed", False)))
                total_count = len(s_data)
                if total_count > 0:
                    total_lines += total_count
                    covered_lines += hit_count
                    if hit_count > 0:
                        files_covered += 1
                    else:
                        uncovered.append(fpath)
        line_cov = round((covered_lines / total_lines) * 100, 2) if total_lines > 0 else 0.0
        return CoverageResult(
            result_id=f"cov_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id, run_id=run_id, issue_id=issue_id,
            line_coverage=line_cov,
            lines_covered=covered_lines, lines_total=total_lines,
            files_covered=files_covered, files_total=files_total,
            uncovered_files=uncovered[:50], format=fmt, raw_report_path=path,
        )

    def _parse_lcov(self, content: str, workspace_id: str, run_id: Optional[str], issue_id: Optional[str], fmt: str, path: str) -> CoverageResult:
        lines_covered = 0
        lines_total = 0
        files_covered = 0
        files_total = 0
        uncovered = []
        current_file = ""
        file_hit = 0
        file_total = 0
        for line in content.splitlines():
            line = line.strip()
            if line.startswith("SF:"):
                if current_file:
                    files_total += 1
                    lines_covered += file_hit
                    lines_total += file_total
                    if file_hit > 0:
                        files_covered += 1
                    else:
                        uncovered.append(current_file)
                current_file = line[3:]
                file_hit = 0
                file_total = 0
            elif line.startswith("DA:"):
                parts = line[3:].split(",")
                if len(parts) >= 2:
                    file_total += 1
                    try:
                        if int(parts[1]) > 0:
                            file_hit += 1
                    except ValueError:
                        pass
            elif line == "end_of_record":
                if current_file:
                    files_total += 1
                    lines_covered += file_hit
                    lines_total += file_total
                    if file_hit > 0:
                        files_covered += 1
                    else:
                        uncovered.append(current_file)
                current_file = ""
                file_hit = 0
                file_total = 0
        line_cov = round((lines_covered / lines_total) * 100, 2) if lines_total > 0 else 0.0
        return CoverageResult(
            result_id=f"cov_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id, run_id=run_id, issue_id=issue_id,
            line_coverage=line_cov,
            lines_covered=lines_covered, lines_total=lines_total,
            files_covered=files_covered, files_total=files_total,
            uncovered_files=uncovered[:50], format=fmt, raw_report_path=path,
        )

    def _save_coverage_result(self, result: CoverageResult) -> None:
        cov_dir = self.store.data_dir / "coverage"
        cov_dir.mkdir(parents=True, exist_ok=True)
        path = cov_dir / f"{result.result_id}.json"
        path.write_text(result.model_dump_json(), encoding="utf-8")

    def _load_coverage_results(self, workspace_id: str, issue_id: Optional[str] = None) -> list[CoverageResult]:
        cov_dir = self.store.data_dir / "coverage"
        if not cov_dir.exists():
            return []
        results = []
        for f in cov_dir.glob("*.json"):
            try:
                r = CoverageResult.model_validate_json(f.read_text(encoding="utf-8"))
                if r.workspace_id == workspace_id:
                    if issue_id is None or r.issue_id == issue_id:
                        results.append(r)
            except Exception:
                continue
        return results

    def _load_latest_coverage(self, workspace_id: str, issue_id: Optional[str] = None, run_id: Optional[str] = None) -> Optional[CoverageResult]:
        results = self._load_coverage_results(workspace_id, issue_id)
        if run_id:
            results = [r for r in results if r.run_id == run_id]
        if not results:
            return None
        results.sort(key=lambda r: r.created_at, reverse=True)
        return results[0]

    def _build_test_suggestions(self, workspace_id: str, issue: IssueRecord, workspace: WorkspaceRecord) -> list[TestSuggestion]:
        suggestions = []
        for i, ev in enumerate(issue.evidence[:5]):
            fpath = ev.path
            parts = fpath.rsplit(".", 1)
            test_file = f"{parts[0]}.test.{parts[1]}" if len(parts) == 2 else f"{fpath}_test.go"
            suggestions.append(TestSuggestion(
                suggestion_id=f"ts_{uuid.uuid4().hex[:8]}",
                issue_id=issue.bug_id,
                workspace_id=workspace_id,
                test_file=test_file,
                test_description=f"Test coverage for {fpath}:{ev.line}" if ev.line else f"Test coverage for {fpath}",
                priority="high" if issue.severity in ("critical", "high") else "medium",
                rationale=f"Evidence found at {fpath}" + (f":{ev.line}" if ev.line else ""),
            ))
        if not suggestions:
            suggestions.append(TestSuggestion(
                suggestion_id=f"ts_{uuid.uuid4().hex[:8]}",
                issue_id=issue.bug_id,
                workspace_id=workspace_id,
                test_file="test_regression.spec.ts",
                test_description=f"Regression test for {issue.title}",
                priority="high",
                rationale="No evidence-based test suggestions available; generic regression test recommended",
            ))
        return suggestions

    def _save_test_suggestions(self, suggestions: list[TestSuggestion]) -> None:
        if not suggestions:
            return
        ts_dir = self.store.data_dir / "test_suggestions"
        ts_dir.mkdir(parents=True, exist_ok=True)
        issue_id = suggestions[0].issue_id
        workspace_id = suggestions[0].workspace_id
        path = ts_dir / f"{workspace_id}_{issue_id}.json"
        data = [s.model_dump(mode="json") for s in suggestions]
        path.write_text(json.dumps(data), encoding="utf-8")

    def _load_test_suggestions(self, workspace_id: str, issue_id: str) -> list[TestSuggestion]:
        ts_dir = self.store.data_dir / "test_suggestions"
        path = ts_dir / f"{workspace_id}_{issue_id}.json"
        if not path.exists():
            return []
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
            return [TestSuggestion(**item) for item in data]
        except Exception:
            return []

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

    def _load_run_for_verification(self, workspace_id: str, run_id: str) -> Optional[RunRecord]:
        deadline = time.monotonic() + 2.5
        last_seen = self.store.load_run(workspace_id, run_id)
        while time.monotonic() < deadline:
            current = self.store.load_run(workspace_id, run_id)
            if current is not None:
                last_seen = current
                if current.summary:
                    return current
            time.sleep(0.1)
        return last_seen

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
            verifications=self.store.list_verifications(workspace_id),
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

    def _default_verification_models(self, runtime: str) -> list[str]:
        if runtime == "opencode":
            return ["opencode-go/minimax-m2.7", "opencode-go/glm-5", "opencode-go/kimi-k2.5"]
        return ["gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex"]

    def _verification_instruction(self, operator_instruction: Optional[str]) -> str:
        base = (
            "Verification pass only. Inspect the issue context, then respond with JSON only. "
            'Schema: {"code_checked":"yes|no|unknown","fixed":"yes|no|unknown","confidence":"high|medium|low","summary":"...","evidence":["..."],"tests":["..."]}. '
            "Use unknown when you cannot support a definitive claim."
        )
        if operator_instruction and operator_instruction.strip():
            return f"{base}\n\nAdditional operator instruction:\n{operator_instruction.strip()}"
        return base

    def _normalize_verification_state(self, value) -> str:
        if isinstance(value, bool):
            return "yes" if value else "no"
        normalized = str(value or "").strip().lower()
        if normalized in {"yes", "true", "checked", "fixed", "pass", "passed"}:
            return "yes"
        if normalized in {"no", "false", "not_checked", "not checked", "broken", "failed"}:
            return "no"
        return "unknown"

    def _normalize_confidence(self, value) -> str:
        normalized = str(value or "").strip().lower()
        if normalized in {"high", "medium", "low"}:
            return normalized
        return "low"

    def _normalize_string_list(self, value) -> list[str]:
        if not isinstance(value, list):
            return []
        return [str(item).strip() for item in value if str(item).strip()]

    def _parse_verification_excerpt(self, excerpt: str) -> dict:
        from_text = self._extract_json_object(excerpt)
        if self._is_verdict_payload(from_text):
            return from_text

        text_fragments: list[str] = []
        for raw_line in excerpt.splitlines():
            line = raw_line.strip()
            if not line:
                continue
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                continue
            if self._is_verdict_payload(payload):
                return payload
            for candidate in self._collect_text_candidates(payload):
                if candidate:
                    text_fragments.append(candidate)
                    parsed = self._extract_json_object(candidate)
                    if self._is_verdict_payload(parsed):
                        return parsed

        combined = "\n".join(fragment.strip() for fragment in text_fragments if fragment.strip()).strip()
        heuristics_source = combined or excerpt
        lower = heuristics_source.lower()
        if heuristics_source and any(token in lower for token in ("already fixed", "is fixed", '"fixed":"yes"', "looks fixed")):
            return {
                "code_checked": "yes",
                "fixed": "yes",
                "confidence": "medium",
                "summary": heuristics_source[:280],
            }
        if heuristics_source and any(token in lower for token in ("not fixed", "still broken", "still bypasses", '"fixed":"no"')):
            return {
                "code_checked": "yes",
                "fixed": "no",
                "confidence": "medium",
                "summary": heuristics_source[:280],
            }
        if any(token in lower for token in ('"tool":"read"', '"tool":"grep"', "callid\":\"read", "<content>", "</content>")):
            return {
                "code_checked": "yes",
                "fixed": "unknown",
                "confidence": "low",
                "summary": "Agent inspected repository files but did not emit a structured final verdict.",
            }

        return {}

    def _extract_json_object(self, text: str) -> dict:
        cleaned = text.strip()
        if not cleaned:
            return {}
        fence_match = re.search(r"```(?:json)?\s*(\{.*?\})\s*```", cleaned, re.DOTALL)
        candidate = fence_match.group(1) if fence_match else None
        if candidate is None:
            object_match = re.search(r"\{.*\}", cleaned, re.DOTALL)
            candidate = object_match.group(0) if object_match else None
        if not candidate:
            return {}
        try:
            payload = json.loads(candidate)
        except json.JSONDecodeError:
            return {}
        return payload if isinstance(payload, dict) else {}

    def _is_verdict_payload(self, payload: dict) -> bool:
        if not isinstance(payload, dict):
            return False
        verdict_keys = {"code_checked", "fixed", "confidence", "summary", "evidence", "tests"}
        return any(key in payload for key in verdict_keys)

    def _collect_text_candidates(self, payload) -> list[str]:
        candidates: list[str] = []
        if isinstance(payload, str):
            stripped = payload.strip()
            if stripped:
                candidates.append(stripped)
            return candidates
        if isinstance(payload, list):
            for item in payload:
                candidates.extend(self._collect_text_candidates(item))
            return candidates
        if not isinstance(payload, dict):
            return candidates
        direct = payload.get("text")
        if isinstance(direct, str) and direct.strip():
            candidates.append(direct.strip())
        part = payload.get("part")
        if isinstance(part, dict):
            text = part.get("text")
            if isinstance(text, str) and text.strip():
                candidates.append(text.strip())
        message = payload.get("message")
        if isinstance(message, dict):
            text = message.get("text")
            if isinstance(text, str) and text.strip():
                candidates.append(text.strip())
        for value in payload.values():
            if isinstance(value, (dict, list)):
                candidates.extend(self._collect_text_candidates(value))
        return candidates

    def _build_verification_prompt(self, packet: IssueContextPacket, operator_instruction: Optional[str]) -> str:
        refs = []
        for evidence in packet.evidence_bundle[:10]:
            ref = evidence.path + (f":{evidence.line}" if evidence.line else "")
            refs.append(f"- {ref}")
        evidence_lines = "\n".join(refs) or "- Inspect the most likely implementation and test files."
        issue = packet.issue
        base = (
            f"Verify bug {issue.bug_id} in {packet.workspace.root_path}.\n"
            "Do not modify files.\n"
            f"Title: {issue.title}\n"
            f"Summary: {issue.summary or 'No summary provided.'}\n"
            f"Impact: {issue.impact or 'No impact provided.'}\n"
            f"Current tracker statuses: doc={issue.doc_status}, code={issue.code_status}, issue={issue.issue_status}.\n"
            "Inspect the cited implementation and test files, then decide whether the code has actually been checked and whether the bug is fixed right now.\n"
            "Evidence to inspect:\n"
            f"{evidence_lines}\n\n"
            "Return exactly one JSON object and nothing else.\n"
            'Schema: {"code_checked":"yes|no|unknown","fixed":"yes|no|unknown","confidence":"high|medium|low","summary":"...","evidence":["path:line"],"tests":["command or test file"]}\n'
            "Use code_checked=yes only if you inspected code or tests. Use fixed=yes only if the current code path clearly closes the reported bug."
        )
        if operator_instruction and operator_instruction.strip():
            return f"{base}\nAdditional operator instruction: {operator_instruction.strip()}"
        return base

    def _verification_summary_text(self, parsed: dict, excerpt: str, run_id: str, model: str) -> str:
        summary = str(parsed.get("summary") or "").strip()
        if summary:
            return summary
        return f"Verification run {run_id} ({model}) did not return a structured verdict."

    def _summarize_verifications(
        self,
        workspace_id: str,
        issue_id: str,
        records: list[VerificationRecord],
    ) -> VerificationSummary:
        checked_yes = sum(1 for item in records if item.code_checked == "yes")
        checked_no = sum(1 for item in records if item.code_checked == "no")
        checked_unknown = sum(1 for item in records if item.code_checked == "unknown")
        fixed_yes = sum(1 for item in records if item.fixed == "yes")
        fixed_no = sum(1 for item in records if item.fixed == "no")
        fixed_unknown = sum(1 for item in records if item.fixed == "unknown")

        def consensus(yes: int, no: int) -> str:
            if yes >= 2:
                return "yes"
            if no >= 2:
                return "no"
            return "unknown"

        return VerificationSummary(
            workspace_id=workspace_id,
            issue_id=issue_id,
            records=records,
            checked_yes=checked_yes,
            checked_no=checked_no,
            checked_unknown=checked_unknown,
            fixed_yes=fixed_yes,
            fixed_no=fixed_no,
            fixed_unknown=fixed_unknown,
            consensus_code_checked=consensus(checked_yes, checked_no),
            consensus_fixed=consensus(fixed_yes, fixed_no),
        )

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
