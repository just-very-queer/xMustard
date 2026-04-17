from __future__ import annotations

from collections import Counter
from fnmatch import fnmatch
import hashlib
import json
import os
import re
import subprocess
import tempfile
import time
import urllib.request
import urllib.error
import uuid
from pathlib import Path
from typing import Optional
from urllib.parse import quote
import yaml

from .models import (
    AcceptanceCriteriaReview,
    ActivityActor,
    ActivityOverview,
    ActivityRecord,
    ActivityRollupItem,
    AppSettings,
    BrowserDumpRecord,
    BrowserDumpUpsertRequest,
    CostSummary,
    CoverageDelta,
    CoverageResult,
    DynamicContextBundle,
    DismissImprovementRequest,
    DuplicateMatch,
    EvidenceRef,
    EvalScenarioRecord,
    EvalScenarioBaselineComparison,
    EvalFreshExecutionComparison,
    EvalFreshReplayRanking,
    EvalFreshReplayRankingEntry,
    EvalFreshReplayTrend,
    EvalFreshReplayTrendEntry,
    EvalFreshRunSummary,
    EvalReplayBatchRecord,
    EvalScenarioReplayRequest,
    EvalScenarioReplayResult,
    EvalScenarioReport,
    EvalScenarioUpsertRequest,
    EvalScenarioVerificationProfileDelta,
    EvalVariantRollup,
    EvalScenarioVariantDiff,
    EvalWorkspaceReport,
    ExportBundle,
    FixRecord,
    FixDraftSuggestion,
    FixRecordRequest,
    FixUpdateRequest,
    GitHubIssueImport,
    GitHubPRCreate,
    GitHubPRResult,
    GuidanceStarterRecord,
    GuidanceStarterRequest,
    GuidanceStarterResult,
    ImprovementSuggestion,
    IssueContextReplayComparison,
    IssueContextReplayRecord,
    IssueContextReplayRequest,
    IntegrationConfig,
    IntegrationTestRequest,
    IntegrationTestResult,
    IssueDriftDetail,
    IssueCreateRequest,
    IssueContextPacket,
    IssueQualityScore,
    IssueRecord,
    IssueUpdateRequest,
    JiraIssueSync,
    LinearIssueSync,
    NotificationEvent,
    PatchCritique,
    PlanApproveRequest,
    PlanPhase,
    PlanRejectRequest,
    PlanStep,
    PromoteSignalRequest,
    RepoGuidanceHealth,
    RepoGuidanceRecord,
    RepoConfigHealth,
    RepoConfigRecord,
    RepoMCPServerRecord,
    RepoPathInstructionMatch,
    RepoPathInstructionRecord,
    RunMetrics,
    RunPlan,
    RunRecord,
    RunReviewRecord,
    RunReviewRequest,
    RunAcceptRequest,
    RunbookRecord,
    RunSessionInsight,
    RunbookUpsertRequest,
    ScopeWarning,
    ReviewQueueItem,
    RelatedContextRecord,
    RepoMapSymbolRecord,
    RepoMapSummary,
    SavedIssueView,
    SavedIssueViewRequest,
    SlackNotification,
    SourceRecord,
    ThreatModelRecord,
    ThreatModelUpsertRequest,
    TicketContextRecord,
    TicketContextUpsertRequest,
    TestSuggestion,
    TriageSuggestion,
    VerificationChecklistResult,
    VerificationProfileDimensionSummary,
    VerificationProfileRecord,
    VerificationProfileReport,
    VerificationProfileUpsertRequest,
    VerificationCommandResult,
    VerificationRecord,
    VerificationProfileExecutionResult,
    VerificationSummary,
    VerificationProfileRunRequest,
    VerifyIssueRequest,
    VulnerabilityFindingRecord,
    VulnerabilityFindingUpsertRequest,
    VulnerabilityImportRequest,
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
    build_repo_map,
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
    MAX_CACHED_SNAPSHOT_BYTES = 25 * 1024 * 1024
    SCANNER_VERSION = 2
    GUIDANCE_LIMIT = 6
    GUIDANCE_STARTER_MARKER = "xmustard:starter-template"
    GUIDANCE_PLACEHOLDER_MARKER = "TODO(xmustard)"
    REPO_CONFIG_CANDIDATES = (".xmustard.yaml", ".xmustard.yml", ".xmustard.json")

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
        snapshot_path = self.store.snapshot_path(workspace_id)
        snapshot_is_oversized = snapshot_path.exists() and snapshot_path.stat().st_size > self.MAX_CACHED_SNAPSHOT_BYTES
        cached_snapshot = None if snapshot_is_oversized else self.store.load_snapshot(workspace_id)
        if cached_snapshot and cached_snapshot.scanner_version != self.SCANNER_VERSION:
            cached_snapshot = None
        if cached_snapshot and request.prefer_cached_snapshot and not snapshot_is_oversized:
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
        verification_profiles = self.list_verification_profiles(workspace_id)
        ticket_contexts = self.list_ticket_contexts(workspace_id)
        threat_models = self.list_threat_models(workspace_id)
        vulnerability_findings = self.list_vulnerability_findings(workspace_id)

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
        repo_map = build_repo_map(root, workspace_id)
        self.store.save_repo_map(workspace_id, repo_map)
        sources.extend(
            self._build_tracker_sources(
                workspace_id,
                tracker_issues,
                fixes,
                runbooks,
                run_reviews,
                verification_profiles,
                ticket_contexts,
                threat_models,
                vulnerability_findings,
            )
        )
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
            "ticket_contexts_total": len(ticket_contexts),
            "threat_models_total": len(threat_models),
            "vulnerability_findings_total": len(vulnerability_findings),
            "repo_map_files": repo_map.total_files,
            "tree_files": tree_summary["files"],
            "tree_directories": tree_summary["directories"],
        }
        snapshot = WorkspaceSnapshot(
            scanner_version=self.SCANNER_VERSION,
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

    def list_verification_profiles(self, workspace_id: str) -> list[VerificationProfileRecord]:
        workspace = self.get_workspace(workspace_id)
        saved = {item.profile_id: item for item in self.store.list_verification_profiles(workspace_id)}
        merged = {item.profile_id: item for item in self._default_verification_profiles(workspace)}
        merged.update(saved)
        return sorted(merged.values(), key=lambda item: (item.built_in, item.name.lower(), item.created_at))

    def save_verification_profile(self, workspace_id: str, request: VerificationProfileUpsertRequest) -> VerificationProfileRecord:
        self.get_workspace(workspace_id)
        existing = [item for item in self.store.list_verification_profiles(workspace_id) if item.profile_id != request.profile_id]
        profile_id = request.profile_id or self._slug_runbook_name(request.name)
        now = utc_now()
        profile = VerificationProfileRecord(
            profile_id=profile_id,
            workspace_id=workspace_id,
            name=request.name.strip(),
            description=request.description.strip(),
            test_command=request.test_command.strip(),
            coverage_command=request.coverage_command.strip() if request.coverage_command else None,
            coverage_report_path=request.coverage_report_path.strip() if request.coverage_report_path else None,
            coverage_format=request.coverage_format,
            max_runtime_seconds=max(1, int(request.max_runtime_seconds)),
            retry_count=max(0, int(request.retry_count)),
            source_paths=[item.strip() for item in request.source_paths if item.strip()][:8],
            checklist_items=self._normalize_text_list(request.checklist_items, limit=12),
            built_in=False,
            updated_at=now,
        )
        existing.append(profile)
        self.store.save_verification_profiles(workspace_id, existing)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"verification-profile:{profile_id}",
            action="verification_profile.saved",
            summary=f"Saved verification profile {profile.name}",
            actor=build_activity_actor("operator", "operator"),
            details={"profile_id": profile_id, "coverage_format": profile.coverage_format},
        )
        return profile

    def list_verification_profile_history(
        self,
        workspace_id: str,
        profile_id: Optional[str] = None,
        issue_id: Optional[str] = None,
    ) -> list[VerificationProfileExecutionResult]:
        self.get_workspace(workspace_id)
        items = self.store.list_verification_profile_history(workspace_id)
        if profile_id:
            items = [item for item in items if item.profile_id == profile_id]
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def list_verification_profile_reports(
        self,
        workspace_id: str,
        issue_id: Optional[str] = None,
    ) -> list[VerificationProfileReport]:
        profiles = self.list_verification_profiles(workspace_id)
        history = self.list_verification_profile_history(workspace_id, issue_id=issue_id)
        run_lookup: dict[str, Optional[RunRecord]] = {}
        for item in history:
            if item.run_id and item.run_id not in run_lookup:
                run_lookup[item.run_id] = self.store.load_run(workspace_id, item.run_id)
        history_by_profile: dict[str, list[VerificationProfileExecutionResult]] = {}
        for item in history:
            history_by_profile.setdefault(item.profile_id, []).append(item)

        reports: list[VerificationProfileReport] = []
        for profile in profiles:
            records = history_by_profile.get(profile.profile_id, [])
            total_runs = len(records)
            success_runs = sum(1 for item in records if item.success)
            failed_runs = total_runs - success_runs
            confidence_counts = {
                "high": sum(1 for item in records if item.confidence == "high"),
                "medium": sum(1 for item in records if item.confidence == "medium"),
                "low": sum(1 for item in records if item.confidence == "low"),
            }
            avg_attempt_count = round(sum(item.attempt_count for item in records) / total_runs, 2) if total_runs else 0.0
            checklist_total = sum(len(item.checklist_results) for item in records)
            checklist_passed = sum(sum(1 for result in item.checklist_results if result.passed) for item in records)
            latest = records[0] if records else None
            reports.append(
                VerificationProfileReport(
                    profile_id=profile.profile_id,
                    workspace_id=workspace_id,
                    profile_name=profile.name,
                    built_in=profile.built_in,
                    issue_id=issue_id,
                    total_runs=total_runs,
                    success_runs=success_runs,
                    failed_runs=failed_runs,
                    success_rate=round((success_runs / total_runs) * 100, 1) if total_runs else 0.0,
                    confidence_counts=confidence_counts,
                    avg_attempt_count=avg_attempt_count,
                    checklist_pass_rate=round((checklist_passed / checklist_total) * 100, 1) if checklist_total else 0.0,
                    last_run_at=latest.created_at if latest else None,
                    last_issue_id=latest.issue_id if latest else None,
                    last_run_id=latest.run_id if latest else None,
                    last_confidence=latest.confidence if latest else None,
                    last_success=latest.success if latest else None,
                    runtime_breakdown=self._build_verification_dimension_breakdown(
                        records,
                        run_lookup,
                        lambda item, run: (
                            (run.runtime, run.runtime) if run is not None else ("manual", "manual")
                        ),
                    ),
                    model_breakdown=self._build_verification_dimension_breakdown(
                        records,
                        run_lookup,
                        lambda item, run: (
                            (run.model, run.model) if run is not None else ("manual", "manual")
                        ),
                    ),
                    branch_breakdown=self._build_verification_dimension_breakdown(
                        records,
                        run_lookup,
                        lambda item, run: (
                            (run.worktree.branch, run.worktree.branch)
                            if run is not None and run.worktree and run.worktree.branch
                            else ("unknown", "unknown")
                        ),
                    ),
                )
            )
        return reports

    def _build_verification_dimension_breakdown(
        self,
        records: list[VerificationProfileExecutionResult],
        run_lookup: dict[str, Optional[RunRecord]],
        resolver,
    ) -> list[VerificationProfileDimensionSummary]:
        buckets: dict[str, VerificationProfileDimensionSummary] = {}
        for item in records:
            run = run_lookup.get(item.run_id) if item.run_id else None
            key, label = resolver(item, run)
            key = key or "unknown"
            label = label or key
            current = buckets.get(key)
            if current is None:
                current = VerificationProfileDimensionSummary(key=key, label=label)
                buckets[key] = current
            current.total_runs += 1
            if item.success:
                current.success_runs += 1
            else:
                current.failed_runs += 1
            current.success_rate = round((current.success_runs / current.total_runs) * 100, 1)
            if current.last_run_at is None or item.created_at > current.last_run_at:
                current.last_run_at = item.created_at
        return sorted(
            buckets.values(),
            key=lambda item: (-item.total_runs, item.label.lower(), item.key),
        )

    def delete_verification_profile(self, workspace_id: str, profile_id: str) -> None:
        self.get_workspace(workspace_id)
        existing = self.store.list_verification_profiles(workspace_id)
        remaining = [item for item in existing if item.profile_id != profile_id]
        if len(remaining) == len(existing):
            raise FileNotFoundError(profile_id)
        self.store.save_verification_profiles(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"verification-profile:{profile_id}",
            action="verification_profile.deleted",
            summary=f"Deleted verification profile {profile_id}",
            actor=build_activity_actor("operator", "operator"),
            details={"profile_id": profile_id},
        )

    def run_issue_verification_profile(
        self,
        workspace_id: str,
        issue_id: str,
        profile_id: str,
        request: VerificationProfileRunRequest,
    ) -> VerificationProfileExecutionResult:
        workspace = self.get_workspace(workspace_id)
        issue = self._get_issue(workspace_id, issue_id)
        profile = self._resolve_verification_profile(workspace_id, profile_id)
        execution = self._execute_verification_profile(
            Path(workspace.root_path),
            profile,
            request.run_id,
            issue_id,
        )
        execution = execution.model_copy(
            update={
                "profile_name": profile.name,
                "issue_id": issue_id,
                "run_id": request.run_id,
                "checklist_results": self._build_verification_checklist_results(profile, execution),
            }
        )
        execution = execution.model_copy(update={"confidence": self._score_verification_execution_confidence(execution)})

        if execution.coverage_result is not None:
            self._save_coverage_result(execution.coverage_result)
            self._record_activity(
                workspace_id=workspace_id,
                entity_type="issue",
                entity_id=issue_id,
                action="coverage.parsed",
                summary=f"Parsed coverage report: {execution.coverage_result.line_coverage:.1f}% lines",
                actor=build_activity_actor("system", "system"),
                issue_id=issue_id,
                run_id=request.run_id,
                details={
                    "profile_id": profile.profile_id,
                    "line_coverage": execution.coverage_result.line_coverage,
                    "files_covered": execution.coverage_result.files_covered,
                },
            )
        history = self.store.list_verification_profile_history(workspace_id)
        history.append(execution)
        self.store.save_verification_profile_history(workspace_id, history)

        verification_evidence = list(issue.verification_evidence)
        if execution.coverage_report_path:
            report_path = Path(execution.coverage_report_path)
            try:
                relative_path = str(report_path.resolve().relative_to(Path(workspace.root_path).resolve()))
            except ValueError:
                relative_path = str(report_path)
            coverage_excerpt = None
            if execution.coverage_result is not None:
                coverage_excerpt = (
                    f"coverage {execution.coverage_result.line_coverage:.2f}% "
                    f"({execution.coverage_result.lines_covered}/{execution.coverage_result.lines_total} lines)"
                )
            verification_evidence.append(EvidenceRef(path=relative_path, excerpt=coverage_excerpt))

        tests_passed = list(issue.tests_passed)
        if execution.success:
            tests_passed = self._normalize_text_list([*tests_passed, profile.test_command], limit=24)

        updated_issue = self._normalize_issue_evidence(
            Path(workspace.root_path),
            issue.model_copy(
                update={
                    "tests_passed": tests_passed,
                    "verification_evidence": verification_evidence,
                    "updated_at": utc_now(),
                }
            ),
        )
        self._persist_issue_snapshot_record(workspace_id, updated_issue)

        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="verification.profile_run",
            summary=f"Ran verification profile {profile.name} ({'passed' if execution.success else 'failed'})",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            run_id=request.run_id,
            details={
                "profile_id": profile.profile_id,
                "attempt_count": execution.attempt_count,
                "success": execution.success,
                "confidence": execution.confidence,
                "checklist_results": [item.model_dump(mode="json") for item in execution.checklist_results],
                "coverage_report_path": execution.coverage_report_path,
            },
        )
        return execution

    def list_ticket_contexts(self, workspace_id: str, issue_id: Optional[str] = None) -> list[TicketContextRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_ticket_contexts(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def save_ticket_context(
        self,
        workspace_id: str,
        issue_id: str,
        request: TicketContextUpsertRequest,
        *,
        actor: Optional[ActivityActor] = None,
        action: str = "ticket_context.saved",
    ) -> TicketContextRecord:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_ticket_contexts(workspace_id)
        context_id = request.context_id or self._slug_runbook_name(f"{request.provider}-{request.external_id or request.title}")
        previous = next((item for item in existing if item.context_id == context_id), None)
        now = utc_now()
        record = TicketContextRecord(
            context_id=context_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            provider=request.provider,
            external_id=request.external_id.strip() if request.external_id else None,
            title=request.title.strip(),
            summary=request.summary.strip(),
            acceptance_criteria=self._normalize_text_list(request.acceptance_criteria, limit=12),
            links=self._normalize_text_list(request.links, limit=8),
            labels=self._normalize_text_list(request.labels, limit=12),
            status=request.status.strip() if request.status else None,
            source_excerpt=request.source_excerpt.strip() if request.source_excerpt else None,
            created_at=previous.created_at if previous else now,
            updated_at=now,
        )
        remaining = [item for item in existing if item.context_id != context_id]
        remaining.append(record)
        self.store.save_ticket_contexts(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action=action,
            summary=f"Saved ticket context {record.title}",
            actor=actor or build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"context_id": context_id, "provider": record.provider, "external_id": record.external_id},
        )
        return record

    def delete_ticket_context(self, workspace_id: str, issue_id: str, context_id: str) -> None:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_ticket_contexts(workspace_id)
        remaining = [item for item in existing if item.context_id != context_id]
        if len(remaining) == len(existing):
            raise FileNotFoundError(context_id)
        self.store.save_ticket_contexts(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="ticket_context.deleted",
            summary=f"Deleted ticket context {context_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"context_id": context_id},
        )

    def list_threat_models(self, workspace_id: str, issue_id: Optional[str] = None) -> list[ThreatModelRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_threat_models(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def save_threat_model(
        self,
        workspace_id: str,
        issue_id: str,
        request: ThreatModelUpsertRequest,
        *,
        actor: Optional[ActivityActor] = None,
        action: str = "threat_model.saved",
    ) -> ThreatModelRecord:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_threat_models(workspace_id)
        threat_model_id = request.threat_model_id or self._slug_runbook_name(f"threat-{request.methodology}-{request.title}")
        previous = next((item for item in existing if item.threat_model_id == threat_model_id), None)
        now = utc_now()
        record = ThreatModelRecord(
            threat_model_id=threat_model_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            title=request.title.strip(),
            methodology=request.methodology,
            summary=request.summary.strip(),
            assets=self._normalize_text_list(request.assets, limit=12),
            entry_points=self._normalize_text_list(request.entry_points, limit=12),
            trust_boundaries=self._normalize_text_list(request.trust_boundaries, limit=12),
            abuse_cases=self._normalize_text_list(request.abuse_cases, limit=12),
            mitigations=self._normalize_text_list(request.mitigations, limit=12),
            references=self._normalize_text_list(request.references, limit=12),
            status=request.status,
            created_at=previous.created_at if previous else now,
            updated_at=now,
        )
        remaining = [item for item in existing if item.threat_model_id != threat_model_id]
        remaining.append(record)
        self.store.save_threat_models(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action=action,
            summary=f"Saved threat model {record.title}",
            actor=actor or build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"threat_model_id": threat_model_id, "methodology": record.methodology, "status": record.status},
        )
        return record

    def delete_threat_model(self, workspace_id: str, issue_id: str, threat_model_id: str) -> None:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_threat_models(workspace_id)
        remaining = [item for item in existing if item.threat_model_id != threat_model_id]
        if len(remaining) == len(existing):
            raise FileNotFoundError(threat_model_id)
        self.store.save_threat_models(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="threat_model.deleted",
            summary=f"Deleted threat model {threat_model_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"threat_model_id": threat_model_id},
        )

    def list_issue_context_replays(self, workspace_id: str, issue_id: Optional[str] = None) -> list[IssueContextReplayRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_context_replays(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def capture_issue_context_replay(
        self,
        workspace_id: str,
        issue_id: str,
        request: IssueContextReplayRequest,
    ) -> IssueContextReplayRecord:
        packet = self.build_issue_context(workspace_id, issue_id)
        replay = IssueContextReplayRecord(
            replay_id=f"ctx_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id,
            issue_id=issue_id,
            label=(request.label or f"{issue_id} context replay").strip(),
            prompt=packet.prompt,
            tree_focus=packet.tree_focus[:12],
            guidance_paths=[item.path for item in packet.guidance],
            verification_profile_ids=[item.profile_id for item in packet.available_verification_profiles[:8]],
            ticket_context_ids=[item.context_id for item in packet.ticket_contexts[:8]],
            browser_dump_ids=[item.dump_id for item in packet.browser_dumps[:8]],
        )
        replays = self.store.list_context_replays(workspace_id)
        replays.append(replay)
        self.store.save_context_replays(workspace_id, replays)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="context_replay.captured",
            summary=f"Captured issue context replay {replay.label}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"replay_id": replay.replay_id},
        )
        return replay

    def compare_issue_context_replay(
        self,
        workspace_id: str,
        issue_id: str,
        replay_id: str,
    ) -> IssueContextReplayComparison:
        packet = self.build_issue_context(workspace_id, issue_id)
        replay = next((item for item in self.list_issue_context_replays(workspace_id, issue_id) if item.replay_id == replay_id), None)
        if replay is None:
            raise FileNotFoundError(replay_id)

        current_tree_focus = packet.tree_focus[:12]
        current_guidance_paths = [item.path for item in packet.guidance]
        current_verification_profile_ids = [item.profile_id for item in packet.available_verification_profiles[:8]]
        current_ticket_context_ids = [item.context_id for item in packet.ticket_contexts[:8]]
        current_browser_dump_ids = [item.dump_id for item in packet.browser_dumps[:8]]

        added_tree_focus, removed_tree_focus = self._diff_ordered_strings(replay.tree_focus, current_tree_focus)
        added_guidance_paths, removed_guidance_paths = self._diff_ordered_strings(replay.guidance_paths, current_guidance_paths)
        added_verification_profile_ids, removed_verification_profile_ids = self._diff_ordered_strings(
            replay.verification_profile_ids,
            current_verification_profile_ids,
        )
        added_ticket_context_ids, removed_ticket_context_ids = self._diff_ordered_strings(
            replay.ticket_context_ids,
            current_ticket_context_ids,
        )
        added_browser_dump_ids, removed_browser_dump_ids = self._diff_ordered_strings(
            replay.browser_dump_ids,
            current_browser_dump_ids,
        )
        prompt_changed = replay.prompt != packet.prompt
        changed = prompt_changed or any(
            (
                added_tree_focus,
                removed_tree_focus,
                added_guidance_paths,
                removed_guidance_paths,
                added_verification_profile_ids,
                removed_verification_profile_ids,
                added_ticket_context_ids,
                removed_ticket_context_ids,
                added_browser_dump_ids,
                removed_browser_dump_ids,
            )
        )

        return IssueContextReplayComparison(
            replay=replay,
            current_prompt=packet.prompt,
            current_tree_focus=current_tree_focus,
            current_guidance_paths=current_guidance_paths,
            current_verification_profile_ids=current_verification_profile_ids,
            current_ticket_context_ids=current_ticket_context_ids,
            current_browser_dump_ids=current_browser_dump_ids,
            prompt_changed=prompt_changed,
            changed=changed,
            saved_prompt_length=len(replay.prompt),
            current_prompt_length=len(packet.prompt),
            added_tree_focus=added_tree_focus,
            removed_tree_focus=removed_tree_focus,
            added_guidance_paths=added_guidance_paths,
            removed_guidance_paths=removed_guidance_paths,
            added_verification_profile_ids=added_verification_profile_ids,
            removed_verification_profile_ids=removed_verification_profile_ids,
            added_ticket_context_ids=added_ticket_context_ids,
            removed_ticket_context_ids=removed_ticket_context_ids,
            added_browser_dump_ids=added_browser_dump_ids,
            removed_browser_dump_ids=removed_browser_dump_ids,
            summary=self._summarize_context_replay_comparison(
                prompt_changed=prompt_changed,
                added_tree_focus=added_tree_focus,
                removed_tree_focus=removed_tree_focus,
                added_guidance_paths=added_guidance_paths,
                removed_guidance_paths=removed_guidance_paths,
                added_verification_profile_ids=added_verification_profile_ids,
                removed_verification_profile_ids=removed_verification_profile_ids,
                added_ticket_context_ids=added_ticket_context_ids,
                removed_ticket_context_ids=removed_ticket_context_ids,
                added_browser_dump_ids=added_browser_dump_ids,
                removed_browser_dump_ids=removed_browser_dump_ids,
            ),
        )

    def list_browser_dumps(self, workspace_id: str, issue_id: Optional[str] = None) -> list[BrowserDumpRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_browser_dumps(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def save_browser_dump(
        self,
        workspace_id: str,
        issue_id: str,
        request: BrowserDumpUpsertRequest,
        *,
        actor: Optional[ActivityActor] = None,
        action: str = "browser_dump.saved",
    ) -> BrowserDumpRecord:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_browser_dumps(workspace_id)
        dump_id = request.dump_id or self._slug_runbook_name(f"browser-{request.label}")
        previous = next((item for item in existing if item.dump_id == dump_id), None)
        now = utc_now()
        record = BrowserDumpRecord(
            dump_id=dump_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            source=request.source,
            label=request.label.strip(),
            page_url=request.page_url.strip() if request.page_url else None,
            page_title=request.page_title.strip() if request.page_title else None,
            summary=request.summary.strip(),
            dom_snapshot=request.dom_snapshot.strip(),
            console_messages=self._normalize_text_list(request.console_messages, limit=20),
            network_requests=self._normalize_text_list(request.network_requests, limit=20),
            screenshot_path=request.screenshot_path.strip() if request.screenshot_path else None,
            notes=request.notes.strip() if request.notes else None,
            created_at=previous.created_at if previous else now,
            updated_at=now,
        )
        remaining = [item for item in existing if item.dump_id != dump_id]
        remaining.append(record)
        self.store.save_browser_dumps(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action=action,
            summary=f"Saved browser dump {record.label}",
            actor=actor or build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"dump_id": dump_id, "source": record.source, "page_url": record.page_url},
        )
        return record

    def delete_browser_dump(self, workspace_id: str, issue_id: str, dump_id: str) -> None:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_browser_dumps(workspace_id)
        remaining = [item for item in existing if item.dump_id != dump_id]
        if len(remaining) == len(existing):
            raise FileNotFoundError(dump_id)
        self.store.save_browser_dumps(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="browser_dump.deleted",
            summary=f"Deleted browser dump {dump_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"dump_id": dump_id},
        )

    def list_vulnerability_findings(self, workspace_id: str, issue_id: Optional[str] = None) -> list[VulnerabilityFindingRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_vulnerability_findings(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        severity_order = {"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}
        return sorted(items, key=lambda item: (severity_order.get(item.severity, 99), item.title.lower(), item.created_at))

    def save_vulnerability_finding(
        self,
        workspace_id: str,
        issue_id: str,
        request: VulnerabilityFindingUpsertRequest,
        *,
        actor: Optional[ActivityActor] = None,
        action: str = "vulnerability_finding.saved",
    ) -> VulnerabilityFindingRecord:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_vulnerability_findings(workspace_id)
        finding_id = request.finding_id or self._slug_runbook_name(f"vuln-{request.scanner}-{request.title}")
        previous = next((item for item in existing if item.finding_id == finding_id and item.issue_id == issue_id), None)
        now = utc_now()
        record = VulnerabilityFindingRecord(
            finding_id=finding_id,
            workspace_id=workspace_id,
            issue_id=issue_id,
            scanner=request.scanner.strip(),
            source=request.source,
            severity=request.severity,
            status=request.status,
            title=request.title.strip(),
            summary=request.summary.strip(),
            rule_id=request.rule_id.strip() if request.rule_id else None,
            location_path=request.location_path.strip() if request.location_path else None,
            location_line=request.location_line,
            cwe_ids=self._normalize_text_list(request.cwe_ids, limit=12),
            cve_ids=self._normalize_text_list(request.cve_ids, limit=12),
            references=self._normalize_text_list(request.references, limit=12),
            evidence=self._normalize_text_list(request.evidence, limit=12),
            raw_payload=request.raw_payload.strip() if request.raw_payload else None,
            created_at=previous.created_at if previous else now,
            updated_at=now,
        )
        remaining = [item for item in existing if not (item.finding_id == finding_id and item.issue_id == issue_id)]
        remaining.append(record)
        self.store.save_vulnerability_findings(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action=action,
            summary=f"Saved vulnerability finding {record.title}",
            actor=actor or build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"finding_id": finding_id, "scanner": record.scanner, "severity": record.severity, "status": record.status},
        )
        return record

    def delete_vulnerability_finding(self, workspace_id: str, issue_id: str, finding_id: str) -> None:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        existing = self.store.list_vulnerability_findings(workspace_id)
        remaining = [item for item in existing if not (item.finding_id == finding_id and item.issue_id == issue_id)]
        if len(remaining) == len(existing):
            raise FileNotFoundError(finding_id)
        self.store.save_vulnerability_findings(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="vulnerability_finding.deleted",
            summary=f"Deleted vulnerability finding {finding_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=issue_id,
            details={"finding_id": finding_id},
        )

    def import_sarif_vulnerability_findings(
        self,
        workspace_id: str,
        issue_id: str,
        payload: str,
    ) -> list[VulnerabilityFindingRecord]:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        try:
            sarif = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise ValueError(f"Invalid SARIF payload: {exc}")
        if not isinstance(sarif, dict):
            raise ValueError("SARIF payload must decode to an object")

        imported: list[VulnerabilityFindingRecord] = []
        runs = sarif.get("runs") if isinstance(sarif.get("runs"), list) else []
        for run in runs:
            if not isinstance(run, dict):
                continue
            driver = ((run.get("tool") or {}).get("driver") or {}) if isinstance(run.get("tool"), dict) else {}
            scanner = str(driver.get("name") or "sarif").strip() or "sarif"
            rules = driver.get("rules") if isinstance(driver.get("rules"), list) else []
            rule_lookup = {
                str(rule.get("id")): rule
                for rule in rules
                if isinstance(rule, dict) and rule.get("id")
            }
            results = run.get("results") if isinstance(run.get("results"), list) else []
            for result in results:
                if not isinstance(result, dict):
                    continue
                imported.append(
                    self.save_vulnerability_finding(
                        workspace_id,
                        issue_id,
                        self._build_sarif_vulnerability_request(scanner, result, rule_lookup),
                        actor=build_activity_actor("system", "system"),
                        action="vulnerability_finding.imported",
                    )
                )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="vulnerability_findings.imported",
            summary=f"Imported {len(imported)} vulnerability finding(s) from SARIF",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"source": "sarif", "count": len(imported)},
        )
        return imported

    def import_nessus_vulnerability_findings(
        self,
        workspace_id: str,
        issue_id: str,
        payload: str,
    ) -> list[VulnerabilityFindingRecord]:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        try:
            nessus = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise ValueError(f"Invalid Nessus payload: {exc}")
        if not isinstance(nessus, dict):
            raise ValueError("Nessus payload must decode to an object")

        imported: list[VulnerabilityFindingRecord] = []
        for item in self._extract_nessus_items(nessus):
            imported.append(
                self.save_vulnerability_finding(
                    workspace_id,
                    issue_id,
                    self._build_nessus_vulnerability_request(item),
                    actor=build_activity_actor("system", "system"),
                    action="vulnerability_finding.imported",
                )
            )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="vulnerability_findings.imported",
            summary=f"Imported {len(imported)} vulnerability finding(s) from Nessus JSON",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"source": "nessus-json", "count": len(imported)},
        )
        return imported

    def import_semgrep_vulnerability_findings(
        self,
        workspace_id: str,
        issue_id: str,
        payload: str,
    ) -> list[VulnerabilityFindingRecord]:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        try:
            semgrep = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise ValueError(f"Invalid Semgrep payload: {exc}")
        if not isinstance(semgrep, dict):
            raise ValueError("Semgrep payload must decode to an object")

        imported: list[VulnerabilityFindingRecord] = []
        results = semgrep.get("results") if isinstance(semgrep.get("results"), list) else []
        for result in results:
            if not isinstance(result, dict):
                continue
            imported.append(
                self.save_vulnerability_finding(
                    workspace_id,
                    issue_id,
                    self._build_semgrep_vulnerability_request(result),
                    actor=build_activity_actor("system", "system"),
                    action="vulnerability_finding.imported",
                )
            )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="vulnerability_findings.imported",
            summary=f"Imported {len(imported)} vulnerability finding(s) from Semgrep JSON",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"source": "semgrep-json", "count": len(imported)},
        )
        return imported

    def import_trivy_vulnerability_findings(
        self,
        workspace_id: str,
        issue_id: str,
        payload: str,
    ) -> list[VulnerabilityFindingRecord]:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        try:
            trivy = json.loads(payload)
        except json.JSONDecodeError as exc:
            raise ValueError(f"Invalid Trivy payload: {exc}")
        if not isinstance(trivy, dict):
            raise ValueError("Trivy payload must decode to an object")

        imported: list[VulnerabilityFindingRecord] = []
        results = trivy.get("Results") if isinstance(trivy.get("Results"), list) else []
        for result in results:
            if not isinstance(result, dict):
                continue
            target = str(result.get("Target") or result.get("target") or "").strip() or None
            for key in ("Vulnerabilities", "Misconfigurations", "Secrets"):
                items = result.get(key)
                if not isinstance(items, list):
                    continue
                for item in items:
                    if not isinstance(item, dict):
                        continue
                    imported.append(
                        self.save_vulnerability_finding(
                            workspace_id,
                            issue_id,
                            self._build_trivy_vulnerability_request(item, target=target, category=key),
                            actor=build_activity_actor("system", "system"),
                            action="vulnerability_finding.imported",
                        )
                    )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="vulnerability_findings.imported",
            summary=f"Imported {len(imported)} vulnerability finding(s) from Trivy JSON",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"source": "trivy-json", "count": len(imported)},
        )
        return imported

    def import_vulnerability_findings(self, workspace_id: str, issue_id: str, request: VulnerabilityImportRequest) -> list[VulnerabilityFindingRecord]:
        if request.source == "sarif":
            return self.import_sarif_vulnerability_findings(workspace_id, issue_id, request.payload)
        if request.source == "nessus-json":
            return self.import_nessus_vulnerability_findings(workspace_id, issue_id, request.payload)
        if request.source == "semgrep-json":
            return self.import_semgrep_vulnerability_findings(workspace_id, issue_id, request.payload)
        if request.source == "trivy-json":
            return self.import_trivy_vulnerability_findings(workspace_id, issue_id, request.payload)
        raise ValueError(f"Unsupported vulnerability import source: {request.source}")

    def _normalize_vulnerability_severity(self, value) -> str:
        if isinstance(value, (int, float)):
            numeric = float(value)
            if numeric >= 7:
                return "critical"
            if numeric >= 4:
                return "high"
            if numeric >= 1:
                return "medium"
            return "low"
        text = str(value or "").strip().lower()
        if re.fullmatch(r"\d+(?:\.\d+)?", text):
            return self._normalize_vulnerability_severity(float(text))
        if text in {"critical", "crit", "4", "very-high"}:
            return "critical"
        if text in {"high", "error", "3", "important"}:
            return "high"
        if text in {"medium", "moderate", "warning", "warn", "2"}:
            return "medium"
        if text in {"low", "note", "1"}:
            return "low"
        return "info"

    def _extract_vulnerability_taxonomy_ids(self, values: list[str]) -> tuple[list[str], list[str]]:
        cwe_ids: list[str] = []
        cve_ids: list[str] = []
        for raw in values:
            text = str(raw or "")
            for cwe in re.findall(r"CWE-\d+", text, flags=re.IGNORECASE):
                normalized = cwe.upper()
                if normalized not in cwe_ids:
                    cwe_ids.append(normalized)
            for cve in re.findall(r"CVE-\d{4}-\d+", text, flags=re.IGNORECASE):
                normalized = cve.upper()
                if normalized not in cve_ids:
                    cve_ids.append(normalized)
        return cwe_ids[:12], cve_ids[:12]

    def _stable_vulnerability_finding_id(
        self,
        source: str,
        scanner: str,
        rule_id: Optional[str],
        title: str,
        location_path: Optional[str],
        location_line: Optional[int],
    ) -> str:
        seed = "|".join(
            [
                source.strip().lower(),
                scanner.strip().lower(),
                (rule_id or "").strip().lower(),
                title.strip().lower(),
                (location_path or "").strip().lower(),
                str(location_line or ""),
            ]
        )
        return f"vf_{hashlib.sha1(seed.encode('utf-8')).hexdigest()[:12]}"

    def _build_sarif_vulnerability_request(
        self,
        scanner: str,
        result: dict,
        rule_lookup: dict[str, dict],
    ) -> VulnerabilityFindingUpsertRequest:
        rule_id = str(result.get("ruleId") or result.get("rule_id") or "").strip() or None
        rule = rule_lookup.get(rule_id or "", {}) if isinstance(rule_lookup, dict) else {}
        message = ((result.get("message") or {}).get("text") if isinstance(result.get("message"), dict) else None) or ""
        short_description = ((rule.get("shortDescription") or {}).get("text") if isinstance(rule.get("shortDescription"), dict) else None) or ""
        help_uri = str(rule.get("helpUri") or rule.get("help_uri") or "").strip() or None
        properties = result.get("properties") if isinstance(result.get("properties"), dict) else {}
        if not properties and isinstance(rule.get("properties"), dict):
            properties = rule.get("properties")
        tags = properties.get("tags") if isinstance(properties.get("tags"), list) else []
        location_path = None
        location_line = None
        locations = result.get("locations") if isinstance(result.get("locations"), list) else []
        if locations:
            first_location = locations[0] if isinstance(locations[0], dict) else {}
            physical = first_location.get("physicalLocation") if isinstance(first_location.get("physicalLocation"), dict) else {}
            artifact = physical.get("artifactLocation") if isinstance(physical.get("artifactLocation"), dict) else {}
            region = physical.get("region") if isinstance(physical.get("region"), dict) else {}
            location_path = str(artifact.get("uri") or artifact.get("uriBaseId") or "").strip() or None
            location_line = region.get("startLine") if isinstance(region.get("startLine"), int) else None
        severity = self._normalize_vulnerability_severity(
            properties.get("security-severity") or result.get("level") or properties.get("problem.severity")
        )
        cwe_ids, cve_ids = self._extract_vulnerability_taxonomy_ids([*tags, rule_id or "", message, short_description])
        title = short_description or rule_id or message or "SARIF finding"
        return VulnerabilityFindingUpsertRequest(
            finding_id=self._stable_vulnerability_finding_id("sarif", scanner, rule_id, title, location_path, location_line),
            scanner=scanner,
            source="sarif",
            severity=severity,
            status="open",
            title=title,
            summary=message.strip(),
            rule_id=rule_id,
            location_path=location_path,
            location_line=location_line,
            cwe_ids=cwe_ids,
            cve_ids=cve_ids,
            references=[item for item in [help_uri] if item],
            evidence=[item for item in [message.strip()] if item],
            raw_payload=json.dumps(result, sort_keys=True),
        )

    def _extract_nessus_items(self, payload: dict) -> list[dict]:
        items: list[dict] = []
        for key in ("findings", "vulnerabilities", "issues", "results"):
            value = payload.get(key)
            if isinstance(value, list):
                items.extend(item for item in value if isinstance(item, dict))
        report_hosts = payload.get("ReportHost")
        if isinstance(report_hosts, list):
            for host in report_hosts:
                if not isinstance(host, dict):
                    continue
                report_items = host.get("ReportItem")
                if isinstance(report_items, list):
                    for item in report_items:
                        if isinstance(item, dict):
                            enriched = dict(item)
                            if host.get("name") and not enriched.get("host"):
                                enriched["host"] = host.get("name")
                            items.append(enriched)
        hosts = payload.get("hosts")
        if isinstance(hosts, list):
            for host in hosts:
                if not isinstance(host, dict):
                    continue
                for key in ("findings", "items", "report_items", "vulnerabilities"):
                    value = host.get(key)
                    if isinstance(value, list):
                        for item in value:
                            if isinstance(item, dict):
                                enriched = dict(item)
                                if host.get("host") and not enriched.get("host"):
                                    enriched["host"] = host.get("host")
                                items.append(enriched)
        return items

    def _build_nessus_vulnerability_request(self, item: dict) -> VulnerabilityFindingUpsertRequest:
        plugin_id = item.get("plugin_id") or item.get("pluginID") or item.get("pluginId")
        plugin_name = str(item.get("plugin_name") or item.get("pluginName") or item.get("name") or item.get("synopsis") or "Nessus finding").strip()
        summary = str(item.get("synopsis") or item.get("description") or item.get("plugin_output") or item.get("output") or "").strip()
        severity = self._normalize_vulnerability_severity(item.get("risk_factor") or item.get("severity"))
        location_path = str(item.get("path") or item.get("file") or item.get("uri") or "").strip() or None
        location_line = item.get("line") if isinstance(item.get("line"), int) else None
        references = item.get("see_also") or item.get("references") or []
        if not isinstance(references, list):
            references = [str(references)] if references else []
        taxonomy_inputs = []
        for key in ("cwe", "cwe_ids", "cve", "cves"):
            value = item.get(key)
            if isinstance(value, list):
                taxonomy_inputs.extend(str(v) for v in value)
            elif value:
                taxonomy_inputs.append(str(value))
        cwe_ids, cve_ids = self._extract_vulnerability_taxonomy_ids(taxonomy_inputs)
        rule_id = f"plugin-{plugin_id}" if plugin_id is not None else None
        evidence_parts = [
            str(item.get("plugin_output") or "").strip(),
            str(item.get("description") or "").strip(),
            str(item.get("solution") or "").strip(),
        ]
        return VulnerabilityFindingUpsertRequest(
            finding_id=self._stable_vulnerability_finding_id("nessus-json", "nessus", rule_id, plugin_name, location_path, location_line),
            scanner="nessus",
            source="nessus-json",
            severity=severity,
            status="open",
            title=plugin_name,
            summary=summary,
            rule_id=rule_id,
            location_path=location_path,
            location_line=location_line,
            cwe_ids=cwe_ids,
            cve_ids=cve_ids,
            references=self._normalize_text_list([str(item) for item in references], limit=12),
            evidence=self._normalize_text_list([part for part in evidence_parts if part], limit=12),
            raw_payload=json.dumps(item, sort_keys=True),
        )

    def _build_semgrep_vulnerability_request(self, result: dict) -> VulnerabilityFindingUpsertRequest:
        extra = result.get("extra") if isinstance(result.get("extra"), dict) else {}
        metadata = extra.get("metadata") if isinstance(extra.get("metadata"), dict) else {}
        check_id = str(result.get("check_id") or result.get("checkId") or result.get("rule_id") or "").strip() or None
        title = str(metadata.get("title") or extra.get("message") or check_id or "Semgrep finding").strip()
        summary = str(extra.get("message") or metadata.get("description") or metadata.get("impact") or "").strip()
        severity = self._normalize_vulnerability_severity(extra.get("severity") or metadata.get("severity"))
        location_path = str(result.get("path") or extra.get("path") or "").strip() or None
        start = result.get("start") if isinstance(result.get("start"), dict) else {}
        location_line = start.get("line") if isinstance(start.get("line"), int) else None
        metadata_references = metadata.get("references")
        if not isinstance(metadata_references, list):
            metadata_references = [metadata_references] if metadata_references else []
        taxonomy_inputs: list[str] = []
        for key in ("cwe", "cve", "owasp", "owasp-top-ten"):
            value = metadata.get(key)
            if isinstance(value, list):
                taxonomy_inputs.extend(str(item) for item in value)
            elif value:
                taxonomy_inputs.append(str(value))
        taxonomy_inputs.extend([check_id or "", title, summary, *[str(item) for item in metadata_references]])
        cwe_ids, cve_ids = self._extract_vulnerability_taxonomy_ids(taxonomy_inputs)
        evidence_parts = [
            str(extra.get("message") or "").strip(),
            str(extra.get("lines") or "").strip(),
            str(metadata.get("description") or "").strip(),
            str(metadata.get("impact") or "").strip(),
            str(metadata.get("fix") or "").strip(),
        ]
        references = [
            *[str(item) for item in metadata_references],
            str(metadata.get("source") or "").strip(),
            str(metadata.get("shortlink") or "").strip(),
        ]
        return VulnerabilityFindingUpsertRequest(
            finding_id=self._stable_vulnerability_finding_id("semgrep-json", "semgrep", check_id, title, location_path, location_line),
            scanner="semgrep",
            source="semgrep-json",
            severity=severity,
            status="open",
            title=title,
            summary=summary,
            rule_id=check_id,
            location_path=location_path,
            location_line=location_line,
            cwe_ids=cwe_ids,
            cve_ids=cve_ids,
            references=self._normalize_text_list([item for item in references if item], limit=12),
            evidence=self._normalize_text_list([part for part in evidence_parts if part], limit=12),
            raw_payload=json.dumps(result, sort_keys=True),
        )

    def _build_trivy_vulnerability_request(
        self,
        item: dict,
        *,
        target: Optional[str],
        category: str,
    ) -> VulnerabilityFindingUpsertRequest:
        category_key = category.strip().lower()
        cause_metadata = item.get("CauseMetadata") if isinstance(item.get("CauseMetadata"), dict) else {}
        location_path = (
            str(cause_metadata.get("Resource") or item.get("Target") or target or "").strip()
            or None
        )
        location_line = cause_metadata.get("StartLine") if isinstance(cause_metadata.get("StartLine"), int) else None
        if location_line is None:
            location = item.get("Location") if isinstance(item.get("Location"), dict) else {}
            location_line = location.get("StartLine") if isinstance(location.get("StartLine"), int) else None

        references = item.get("References") or item.get("references") or item.get("URLs") or []
        if not isinstance(references, list):
            references = [references] if references else []
        primary_url = str(item.get("PrimaryURL") or item.get("primaryURL") or "").strip()
        datasource = item.get("DataSource") if isinstance(item.get("DataSource"), dict) else {}
        datasource_url = str(datasource.get("URL") or "").strip()

        if category_key == "misconfigurations":
            rule_id = str(item.get("ID") or item.get("AVDID") or item.get("RuleID") or "").strip() or None
            title = str(item.get("Title") or item.get("Message") or rule_id or "Trivy misconfiguration").strip()
            summary = str(item.get("Description") or item.get("Message") or "").strip()
            severity = self._normalize_vulnerability_severity(item.get("Severity") or item.get("severity"))
            taxonomy_inputs = [
                rule_id or "",
                title,
                summary,
                *[str(item) for item in references],
                primary_url,
                datasource_url,
            ]
            evidence_parts = [
                str(item.get("Message") or "").strip(),
                str(item.get("Resolution") or "").strip(),
                str(item.get("Query") or "").strip(),
            ]
        elif category_key == "secrets":
            rule_id = str(item.get("RuleID") or item.get("ID") or "").strip() or None
            title = str(item.get("Title") or rule_id or "Trivy secret").strip()
            summary = str(item.get("Description") or item.get("Message") or "").strip()
            severity = self._normalize_vulnerability_severity(item.get("Severity") or item.get("severity") or "high")
            taxonomy_inputs = [rule_id or "", title, summary, *[str(item) for item in references], primary_url, datasource_url]
            evidence_parts = [
                str(item.get("Match") or "").strip(),
                str(item.get("Code") or "").strip(),
                str(item.get("Message") or "").strip(),
            ]
        else:
            vulnerability_id = str(item.get("VulnerabilityID") or item.get("vulnerabilityID") or item.get("ID") or "").strip() or None
            package_name = str(item.get("PkgName") or item.get("PkgID") or "").strip()
            installed_version = str(item.get("InstalledVersion") or "").strip()
            rule_id_parts = [part for part in [vulnerability_id, package_name, installed_version] if part]
            rule_id = ":".join(rule_id_parts) or vulnerability_id
            title = str(item.get("Title") or vulnerability_id or item.get("PkgName") or "Trivy vulnerability").strip()
            summary = str(item.get("Description") or item.get("Title") or "").strip()
            severity = self._normalize_vulnerability_severity(item.get("Severity") or item.get("severity"))
            taxonomy_inputs = [
                vulnerability_id or "",
                title,
                summary,
                str(item.get("CweIDs") or "").strip(),
                str(item.get("CVSS") or "").strip(),
                *[str(item) for item in references],
                primary_url,
                datasource_url,
            ]
            evidence_parts = [
                f"Package: {item.get('PkgName')}" if item.get("PkgName") else "",
                f"Installed version: {item.get('InstalledVersion')}" if item.get("InstalledVersion") else "",
                f"Fixed version: {item.get('FixedVersion')}" if item.get("FixedVersion") else "",
                str(item.get("Status") or "").strip(),
                str(item.get("Layer") or "").strip(),
            ]

        cwe_ids, cve_ids = self._extract_vulnerability_taxonomy_ids(taxonomy_inputs)
        return VulnerabilityFindingUpsertRequest(
            finding_id=self._stable_vulnerability_finding_id("trivy-json", "trivy", rule_id, title, location_path, location_line),
            scanner="trivy",
            source="trivy-json",
            severity=severity,
            status="open",
            title=title,
            summary=summary,
            rule_id=rule_id,
            location_path=location_path,
            location_line=location_line,
            cwe_ids=cwe_ids,
            cve_ids=cve_ids,
            references=self._normalize_text_list([*map(str, references), primary_url, datasource_url], limit=12),
            evidence=self._normalize_text_list([part for part in evidence_parts if part], limit=12),
            raw_payload=json.dumps(item, sort_keys=True),
        )

    def list_eval_scenarios(self, workspace_id: str, issue_id: Optional[str] = None) -> list[EvalScenarioRecord]:
        self.get_workspace(workspace_id)
        items = self.store.list_eval_scenarios(workspace_id)
        if issue_id:
            items = [item for item in items if item.issue_id == issue_id]
        return items

    def _get_eval_scenario(self, workspace_id: str, scenario_id: str) -> EvalScenarioRecord:
        scenario = next(
            (item for item in self.store.list_eval_scenarios(workspace_id) if item.scenario_id == scenario_id),
            None,
        )
        if scenario is None:
            raise FileNotFoundError(scenario_id)
        return scenario

    def save_eval_scenario(self, workspace_id: str, request: EvalScenarioUpsertRequest) -> EvalScenarioRecord:
        self._get_issue_from_snapshot(workspace_id, request.issue_id)
        existing = self.store.list_eval_scenarios(workspace_id)
        scenario_id = request.scenario_id or self._slug_runbook_name(f"eval-{request.name}")
        previous = next((item for item in existing if item.scenario_id == scenario_id), None)
        now = utc_now()
        record = EvalScenarioRecord(
            scenario_id=scenario_id,
            workspace_id=workspace_id,
            issue_id=request.issue_id,
            name=request.name.strip(),
            description=request.description.strip() if request.description else None,
            baseline_replay_id=request.baseline_replay_id.strip() if request.baseline_replay_id else None,
            guidance_paths=self._normalize_text_list(request.guidance_paths, limit=12),
            ticket_context_ids=self._normalize_text_list(request.ticket_context_ids, limit=12),
            verification_profile_ids=self._normalize_text_list(request.verification_profile_ids, limit=12),
            run_ids=self._normalize_text_list(request.run_ids, limit=24),
            browser_dump_ids=self._normalize_text_list(request.browser_dump_ids, limit=12),
            notes=request.notes.strip() if request.notes else None,
            created_at=previous.created_at if previous else now,
            updated_at=now,
        )
        remaining = [item for item in existing if item.scenario_id != scenario_id]
        remaining.append(record)
        self.store.save_eval_scenarios(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=request.issue_id,
            action="eval_scenario.saved",
            summary=f"Saved eval scenario {record.name}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=request.issue_id,
            details={"scenario_id": record.scenario_id, "baseline_replay_id": record.baseline_replay_id},
        )
        return record

    def delete_eval_scenario(self, workspace_id: str, scenario_id: str) -> None:
        self.get_workspace(workspace_id)
        existing = self.store.list_eval_scenarios(workspace_id)
        scenario = next((item for item in existing if item.scenario_id == scenario_id), None)
        if not scenario:
            raise FileNotFoundError(scenario_id)
        remaining = [item for item in existing if item.scenario_id != scenario_id]
        self.store.save_eval_scenarios(workspace_id, remaining)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=scenario.issue_id,
            action="eval_scenario.deleted",
            summary=f"Deleted eval scenario {scenario.name}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=scenario.issue_id,
            details={"scenario_id": scenario_id},
        )

    def get_eval_report(self, workspace_id: str, scenario_id: Optional[str] = None) -> EvalWorkspaceReport:
        self.get_workspace(workspace_id)
        scenarios = self.list_eval_scenarios(workspace_id)
        if scenario_id:
            scenarios = [item for item in scenarios if item.scenario_id == scenario_id]
            if not scenarios:
                raise FileNotFoundError(scenario_id)

        scenario_reports = [self._build_eval_scenario_report(workspace_id, scenario) for scenario in scenarios]
        scenario_reports = self._attach_eval_baseline_comparisons(workspace_id, scenario_reports)
        scenario_reports = self._attach_eval_fresh_comparisons(scenario_reports)
        run_count = sum(len(item.run_metrics) for item in scenario_reports)
        success_runs = sum(item.success_runs for item in scenario_reports)
        failed_runs = sum(item.failed_runs for item in scenario_reports)
        total_estimated_cost = round(sum(item.total_estimated_cost for item in scenario_reports), 4)
        total_duration_ms = sum(sum(metric.duration_ms for metric in item.run_metrics) for item in scenario_reports)
        profile_runs = sum(
            sum(profile.total_runs for profile in item.verification_profile_reports)
            for item in scenario_reports
        )
        successful_profile_runs = sum(
            sum(profile.success_runs for profile in item.verification_profile_reports)
            for item in scenario_reports
        )
        replay_batches = self.store.list_eval_replay_batches(workspace_id)
        fresh_replay_rankings = self._build_eval_fresh_replay_rankings(scenario_reports)
        return EvalWorkspaceReport(
            workspace_id=workspace_id,
            scenario_count=len(scenario_reports),
            run_count=run_count,
            success_runs=success_runs,
            failed_runs=failed_runs,
            total_estimated_cost=total_estimated_cost,
            total_duration_ms=total_duration_ms,
            verification_success_rate=round((successful_profile_runs / profile_runs) * 100, 1) if profile_runs else 0.0,
            cost_summary=CostSummary.model_validate(self.get_workspace_cost_summary(workspace_id)),
            scenario_reports=scenario_reports,
            replay_batches=replay_batches,
            fresh_replay_rankings=fresh_replay_rankings,
            fresh_replay_trends=self._build_eval_fresh_replay_trends(
                workspace_id,
                scenarios,
                scenario_reports,
                fresh_replay_rankings,
                replay_batches,
            ),
            guidance_variant_rollups=self._build_eval_variant_rollups(workspace_id, scenarios, "guidance"),
            ticket_context_variant_rollups=self._build_eval_variant_rollups(workspace_id, scenarios, "ticket_context"),
        )

    def replay_eval_scenarios(
        self,
        workspace_id: str,
        issue_id: str,
        request: EvalScenarioReplayRequest,
    ) -> EvalScenarioReplayResult:
        self._get_issue_from_snapshot(workspace_id, issue_id)
        scenarios = self.list_eval_scenarios(workspace_id, issue_id=issue_id)
        if request.scenario_ids:
            allowed = set(request.scenario_ids)
            scenarios = [item for item in scenarios if item.scenario_id in allowed]
        if not scenarios:
            raise FileNotFoundError(issue_id)
        batch_id = f"evalbatch_{uuid.uuid4().hex[:12]}"
        queued_runs = [
            RunRecord.model_validate(
                self.start_issue_run(
                    workspace_id,
                    issue_id,
                    request.runtime,
                    request.model,
                    request.instruction,
                    request.runbook_id,
                    scenario.scenario_id,
                    batch_id,
                    request.planning,
                )
            )
            for scenario in scenarios
        ]
        self.store.save_eval_replay_batches(
            workspace_id,
            [
                *self.store.list_eval_replay_batches(workspace_id),
                EvalReplayBatchRecord(
                    batch_id=batch_id,
                    workspace_id=workspace_id,
                    issue_id=issue_id,
                    runtime=request.runtime,
                    model=request.model,
                    scenario_ids=[item.scenario_id for item in scenarios],
                    queued_run_ids=[item.run_id for item in queued_runs],
                    instruction=request.instruction,
                    runbook_id=request.runbook_id,
                    planning=request.planning,
                ),
            ],
        )
        return EvalScenarioReplayResult(
            workspace_id=workspace_id,
            issue_id=issue_id,
            runtime=request.runtime,
            model=request.model,
            batch_id=batch_id,
            scenario_ids=[item.scenario_id for item in scenarios],
            queued_runs=queued_runs,
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
                guidance_paths=[item.path for item in packet.guidance],
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

    def eval_issue_work(
        self,
        workspace_id: str,
        scenario_id: str,
        runbook_id: Optional[str] = None,
    ) -> IssueContextPacket:
        scenario = self._get_eval_scenario(workspace_id, scenario_id)
        packet = self.build_issue_context(workspace_id, scenario.issue_id)
        packet = self._apply_eval_scenario_to_packet(packet, scenario)
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

    def list_workspace_guidance(self, workspace_id: str) -> list[RepoGuidanceRecord]:
        workspace = self.get_workspace(workspace_id)
        return self._collect_workspace_guidance(Path(workspace.root_path), workspace_id)

    def get_workspace_guidance_health(self, workspace_id: str) -> RepoGuidanceHealth:
        workspace = self.get_workspace(workspace_id)
        root = Path(workspace.root_path)
        guidance = self._collect_workspace_guidance(root, workspace_id)
        present_files = [item.path for item in guidance]
        starters: list[GuidanceStarterRecord] = []
        stale_files: list[str] = []
        missing_files: list[str] = []

        for starter in self._guidance_starter_specs(workspace):
            candidate_path = root / starter["path"]
            exists = candidate_path.exists()
            stale = exists and self._guidance_file_is_stale(candidate_path)
            starters.append(
                GuidanceStarterRecord(
                    template_id=starter["template_id"],
                    title=starter["title"],
                    path=starter["path"],
                    description=starter["description"],
                    recommended=starter["recommended"],
                    exists=exists,
                    stale=stale,
                )
            )
            if stale:
                stale_files.append(starter["path"])
            if starter["recommended"] and not exists:
                missing_files.append(starter["path"])

        instruction_count = sum(1 for item in guidance if item.kind in {"agent_instructions", "conventions"})
        always_on_count = sum(1 for item in guidance if item.always_on)
        recommended_files = [item.path for item in starters if item.recommended]

        if stale_files:
            status = "stale"
            summary = "Starter guidance exists but still contains xMustard placeholders that should be customized."
        elif not guidance:
            status = "missing"
            summary = "No repository guidance files were found. Generate a starter AGENTS.md to ground issue context and runs."
        elif missing_files:
            status = "partial"
            summary = "Guidance is present, but the recommended starter set is incomplete."
        else:
            status = "healthy"
            summary = "Repository guidance is present and ready to shape issue context and runs."

        return RepoGuidanceHealth(
            workspace_id=workspace_id,
            status=status,
            summary=summary,
            guidance_count=len(guidance),
            always_on_count=always_on_count,
            instruction_count=instruction_count,
            present_files=present_files,
            missing_files=missing_files,
            stale_files=stale_files,
            recommended_files=recommended_files,
            starters=starters,
        )

    def generate_guidance_starter(self, workspace_id: str, request: GuidanceStarterRequest) -> GuidanceStarterResult:
        workspace = self.get_workspace(workspace_id)
        root = Path(workspace.root_path)
        spec = next((item for item in self._guidance_starter_specs(workspace) if item["template_id"] == request.template_id), None)
        if spec is None:
            raise ValueError(f"Unknown guidance starter template: {request.template_id}")
        relative_path = str(spec["path"])
        content = str(spec["content"])
        path = root / relative_path
        existed_before = path.exists()
        if existed_before and not request.overwrite:
            raise FileExistsError(relative_path)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="workspace",
            entity_id=workspace_id,
            action="guidance.starter_generated",
            summary=f"Generated guidance starter {relative_path}",
            actor=build_activity_actor("operator", "operator"),
            details={"template_id": request.template_id, "path": relative_path, "overwrite": request.overwrite},
        )
        return GuidanceStarterResult(
            workspace_id=workspace_id,
            template_id=request.template_id,
            path=relative_path,
            overwritten=existed_before and request.overwrite,
        )

    def read_repo_map(self, workspace_id: str) -> RepoMapSummary:
        workspace = self.get_workspace(workspace_id)
        cached = self.store.load_repo_map(workspace_id)
        if cached:
            return cached
        repo_map = build_repo_map(Path(workspace.root_path), workspace_id)
        self.store.save_repo_map(workspace_id, repo_map)
        return repo_map

    def read_workspace_repo_config(self, workspace_id: str) -> RepoConfigRecord:
        workspace = self.get_workspace(workspace_id)
        root = Path(workspace.root_path)
        for relative_path in self.REPO_CONFIG_CANDIDATES:
            candidate = root / relative_path
            if candidate.is_file():
                return self._load_repo_config_from_path(workspace_id, root, candidate)
        return RepoConfigRecord(workspace_id=workspace_id)

    def get_workspace_repo_config_health(self, workspace_id: str) -> RepoConfigHealth:
        config = self.read_workspace_repo_config(workspace_id)
        if not config.source_path:
            return RepoConfigHealth(
                workspace_id=workspace_id,
                status="missing",
                summary="No .xmustard config was found. Add one to define path-specific instructions, filters, and MCP/browser context guidance.",
            )
        summary_parts = [f"Loaded {config.source_path}"]
        if config.path_instructions:
            summary_parts.append(f"{len(config.path_instructions)} path instruction(s)")
        if config.path_filters:
            summary_parts.append(f"{len(config.path_filters)} path filter(s)")
        if config.mcp_servers:
            summary_parts.append(f"{len(config.mcp_servers)} MCP server hint(s)")
        return RepoConfigHealth(
            workspace_id=workspace_id,
            status="configured",
            source_path=config.source_path,
            summary=". ".join(summary_parts) + ".",
            path_instruction_count=len(config.path_instructions),
            path_filter_count=len(config.path_filters),
            code_guideline_count=len(config.code_guidelines),
            mcp_server_count=len(config.mcp_servers),
        )

    def _load_repo_config_from_path(self, workspace_id: str, root: Path, path: Path) -> RepoConfigRecord:
        text = path.read_text(encoding="utf-8", errors="ignore")
        payload: object
        if path.suffix == ".json":
            payload = json.loads(text or "{}")
        else:
            payload = yaml.safe_load(text) or {}
        if not isinstance(payload, dict):
            raise ValueError(f"Invalid xMustard repo config in {path.name}: top-level value must be an object")
        reviews = payload.get("reviews") if isinstance(payload.get("reviews"), dict) else {}
        instructions = reviews.get("path_instructions") if isinstance(reviews, dict) and isinstance(reviews.get("path_instructions"), list) else []
        filters = reviews.get("path_filters") if isinstance(reviews, dict) and isinstance(reviews.get("path_filters"), list) else []
        raw_guidelines = payload.get("code_guidelines") or payload.get("guidance") or []
        if isinstance(raw_guidelines, str):
            code_guidelines = [raw_guidelines]
        elif isinstance(raw_guidelines, list):
            code_guidelines = raw_guidelines
        else:
            code_guidelines = []
        raw_mcp_servers = payload.get("mcp_servers") or []
        mcp_servers = raw_mcp_servers if isinstance(raw_mcp_servers, list) else []
        source_path = str(path.relative_to(root))
        return RepoConfigRecord(
            workspace_id=workspace_id,
            source_path=source_path,
            description=str(payload.get("description") or payload.get("repository") or "").strip(),
            path_filters=[str(item).strip() for item in filters if str(item).strip()],
            path_instructions=[
                RepoPathInstructionRecord(
                    instruction_id="cfg_"
                    + hashlib.sha1(
                        f"{workspace_id}:{source_path}:{index}:{str(entry.get('path') or '').strip()}".encode("utf-8")
                    ).hexdigest()[:12],
                    path=str(entry.get("path") or "").strip(),
                    instructions=str(entry.get("instructions") or "").strip(),
                    title=str(entry.get("title") or "").strip() or None,
                    source_path=source_path,
                )
                for index, entry in enumerate(instructions)
                if isinstance(entry, dict) and str(entry.get("path") or "").strip() and str(entry.get("instructions") or "").strip()
            ],
            code_guidelines=[str(item).strip() for item in code_guidelines if str(item).strip()],
            mcp_servers=[
                RepoMCPServerRecord(
                    name=str(item.get("name") or "").strip(),
                    description=str(item.get("description") or "").strip(),
                    usage=str(item.get("usage") or "").strip(),
                )
                for item in mcp_servers
                if isinstance(item, dict) and str(item.get("name") or "").strip()
            ],
        )

    def _match_repo_path_instructions(
        self,
        config: RepoConfigRecord,
        candidate_paths: list[str],
    ) -> list[RepoPathInstructionMatch]:
        matches: list[RepoPathInstructionMatch] = []
        ordered_paths = self._dedupe_text(candidate_paths)
        for item in config.path_instructions:
            matched_paths = [path for path in ordered_paths if fnmatch(path, item.path)]
            if not matched_paths:
                continue
            matches.append(
                RepoPathInstructionMatch(
                    instruction_id=item.instruction_id,
                    path=item.path,
                    title=item.title,
                    instructions=item.instructions,
                    source_path=item.source_path,
                    matched_paths=matched_paths[:6],
                )
            )
        return matches[:6]

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
        verification_profiles = self.list_verification_profiles(workspace_id)
        ticket_contexts = self.list_ticket_contexts(workspace_id, issue_id=issue_id)
        threat_models = self.list_threat_models(workspace_id, issue_id=issue_id)
        browser_dumps = self.list_browser_dumps(workspace_id, issue_id=issue_id)
        vulnerability_findings = self.list_vulnerability_findings(workspace_id, issue_id=issue_id)
        repo_map = self.read_repo_map(workspace_id)
        repo_config = self.read_workspace_repo_config(workspace_id)
        related_paths = self._rank_related_paths(issue, tree_focus, ticket_contexts, vulnerability_findings, repo_map)
        matched_path_instructions = self._match_repo_path_instructions(
            repo_config,
            [*tree_focus, *related_paths, *(item.normalized_path or item.path for item in evidence_bundle)],
        )
        recent_fixes = self.list_fixes(workspace_id, issue_id=issue_id)[:5]
        recent_activity = [item for item in self.list_activity(workspace_id, issue_id=issue_id, limit=16) if item.entity_type == "issue"][:8]
        dynamic_context = self._build_dynamic_context(
            snapshot.workspace,
            issue,
            tree_focus,
            ticket_contexts,
            threat_models,
            browser_dumps,
            vulnerability_findings,
            recent_fixes,
            recent_activity,
            related_paths,
        )
        runbook = self._render_runbook_steps(default_runbook.template)
        guidance = self.list_workspace_guidance(workspace_id)[:self.GUIDANCE_LIMIT]
        prompt = self._build_prompt(
            snapshot.workspace,
            issue,
            tree_focus,
            recent_fixes,
            recent_activity,
            guidance,
            verification_profiles,
            ticket_contexts,
            threat_models,
            browser_dumps,
            vulnerability_findings,
            related_paths,
            repo_map,
            dynamic_context,
            repo_config,
            matched_path_instructions,
        )
        return IssueContextPacket(
            issue=issue,
            workspace=snapshot.workspace,
            tree_focus=tree_focus[:12],
            related_paths=related_paths,
            evidence_bundle=evidence_bundle[:20],
            recent_fixes=recent_fixes,
            recent_activity=recent_activity,
            guidance=guidance,
            runbook=runbook,
            available_runbooks=runbooks,
            available_verification_profiles=verification_profiles,
            ticket_contexts=ticket_contexts,
            threat_models=threat_models,
            browser_dumps=browser_dumps,
            vulnerability_findings=vulnerability_findings,
            repo_map=repo_map,
            dynamic_context=dynamic_context,
            repo_config=repo_config,
            matched_path_instructions=matched_path_instructions,
            worktree=self.read_worktree_status(workspace_id),
            prompt=prompt,
        )

    def _diff_ordered_strings(self, previous: list[str], current: list[str]) -> tuple[list[str], list[str]]:
        previous_set = set(previous)
        current_set = set(current)
        added = [item for item in current if item not in previous_set]
        removed = [item for item in previous if item not in current_set]
        return added, removed

    def _summarize_context_replay_comparison(
        self,
        *,
        prompt_changed: bool,
        added_tree_focus: list[str],
        removed_tree_focus: list[str],
        added_guidance_paths: list[str],
        removed_guidance_paths: list[str],
        added_verification_profile_ids: list[str],
        removed_verification_profile_ids: list[str],
        added_ticket_context_ids: list[str],
        removed_ticket_context_ids: list[str],
        added_browser_dump_ids: list[str],
        removed_browser_dump_ids: list[str],
    ) -> str:
        if not prompt_changed and not any(
            (
                added_tree_focus,
                removed_tree_focus,
                added_guidance_paths,
                removed_guidance_paths,
                added_verification_profile_ids,
                removed_verification_profile_ids,
                added_ticket_context_ids,
                removed_ticket_context_ids,
                added_browser_dump_ids,
                removed_browser_dump_ids,
            )
        ):
            return "No issue-context drift detected since this replay."

        changes: list[str] = []
        if prompt_changed:
            changes.append("prompt changed")
        for label, added, removed in (
            ("tree focus", added_tree_focus, removed_tree_focus),
            ("guidance", added_guidance_paths, removed_guidance_paths),
            ("verification profiles", added_verification_profile_ids, removed_verification_profile_ids),
            ("ticket contexts", added_ticket_context_ids, removed_ticket_context_ids),
            ("browser dumps", added_browser_dump_ids, removed_browser_dump_ids),
        ):
            if added:
                changes.append(f"+{len(added)} {label}")
            if removed:
                changes.append(f"-{len(removed)} {label}")
        return f"Context drift since this replay: {', '.join(changes)}."

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
        eval_scenario_id: Optional[str] = None,
        eval_replay_batch_id: Optional[str] = None,
        planning: bool = False,
    ) -> dict:
        packet = (
            self.eval_issue_work(workspace_id, eval_scenario_id, runbook_id=runbook_id)
            if eval_scenario_id
            else self.issue_work(workspace_id, issue_id, runbook_id=runbook_id)
        )
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
            guidance_paths=[item.path for item in packet.guidance],
            runbook_id=runbook_id,
            eval_scenario_id=eval_scenario_id,
            eval_replay_batch_id=eval_replay_batch_id,
            wait_for_approval=planning,
        )
        if eval_scenario_id:
            self._record_eval_scenario_run(workspace_id, eval_scenario_id, run.run_id)
        action = "run.planning" if planning else "run.queued"
        eval_summary = f" via eval scenario {eval_scenario_id}" if eval_scenario_id else ""
        batch_summary = f" in replay batch {eval_replay_batch_id}" if eval_replay_batch_id else ""
        summary = (
            f"Started {runtime} run with planning for {issue_id}{eval_summary}{batch_summary}"
            if planning
            else f"Queued {runtime} run for {issue_id}{eval_summary}{batch_summary}"
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run.run_id,
            action=action,
            summary=summary,
            actor=build_activity_actor("agent", runtime, runtime=runtime, model=model),
            issue_id=issue_id,
            run_id=run.run_id,
            details={
                "runtime": runtime,
                "model": model,
                "runbook_id": runbook_id,
                "planning": planning,
                "eval_scenario_id": eval_scenario_id,
                "eval_replay_batch_id": eval_replay_batch_id,
            },
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
        guidance = self.list_workspace_guidance(workspace_id)[:self.GUIDANCE_LIMIT]
        self.runtime_service.validate_runtime_model(runtime, model)
        run = self.runtime_service.start_issue_run(
            workspace_id=workspace_id,
            workspace_path=Path(workspace.root_path),
            issue_id="workspace-query",
            runtime=runtime,
            model=model,
            prompt=self._apply_guidance_to_prompt(trimmed_prompt, guidance),
            worktree=self.read_worktree_status(workspace_id),
            guidance_paths=[item.path for item in guidance],
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
            guidance_paths=run.guidance_paths,
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

    def get_run_session_insight(self, workspace_id: str, run_id: str) -> dict:
        self.get_workspace(workspace_id)
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)

        critique = self._load_critique(run_id)
        metrics = self.runtime_service.load_run_metrics(run_id)
        packet = self.build_issue_context(workspace_id, run.issue_id)
        excerpt_text = ""
        strengths: list[str] = []
        risks: list[str] = []
        recommendations: list[str] = []

        if run.status == "completed":
            strengths.append("Run completed successfully.")
        elif run.status == "failed":
            risks.append("Run failed before completing successfully.")
        elif run.status == "cancelled":
            risks.append("Run was cancelled before completion.")
        else:
            risks.append(f"Run is still {run.status}.")

        if run.guidance_paths:
            strengths.append(f"Run included repository guidance from {len(run.guidance_paths)} file(s).")
        else:
            risks.append("No repository guidance was attached to this run.")
            recommendations.append("Add AGENTS.md, CONVENTIONS.md, or reusable skills so runs start with stable repository context.")

        excerpt = None
        if run.summary and isinstance(run.summary, dict):
            total_events = run.summary.get("event_count", 0)
            tool_events = run.summary.get("tool_event_count", 0)
            excerpt = run.summary.get("text_excerpt")
            excerpt_text = excerpt or ""
            if total_events and tool_events / max(total_events, 1) > 0.8:
                risks.append("The run spent a high share of events in tool usage.")
                recommendations.append("Move repeated workflow guidance into always-on repo instructions or reusable skills to reduce tool churn.")

        if metrics and metrics.duration_ms > 0:
            strengths.append(f"Captured runtime metrics for cost and duration ({metrics.duration_ms} ms).")
        else:
            recommendations.append("Persist run metrics consistently so cost and duration trends can be reviewed later.")

        if critique:
            if critique.overall_quality in {"excellent", "good"} and not critique.issues_found:
                strengths.append("Patch critique did not find correctness issues.")
            for item in critique.issues_found[:3]:
                risks.append(item)
            high_severity = [item for item in critique.improvements if not item.dismissed and item.severity == "high"]
            if high_severity:
                risks.append(f"{len(high_severity)} high-severity improvement suggestion(s) remain open.")
                recommendations.append("Review the open high-severity improvement suggestions before treating the run as production-ready.")
        elif run.status == "completed":
            recommendations.append("Generate a critique for completed runs so review feedback is preserved as an artifact.")

        if not excerpt:
            recommendations.append("Capture a concise final summary in the run output so session review is easier.")

        acceptance_review = self._build_acceptance_review(packet, run, excerpt_text)
        scope_warnings = self._build_scope_warnings(packet, run)
        if acceptance_review.status == "met":
            strengths.append("Ticket acceptance criteria appear satisfied by the recorded run outcome.")
        elif acceptance_review.status == "partial":
            risks.append("Only part of the recorded acceptance criteria is reflected in the run evidence.")
            recommendations.append("Review the missing acceptance criteria before treating the run as ready.")
        elif acceptance_review.status == "not_met":
            risks.append("Recorded run evidence does not yet satisfy the linked acceptance criteria.")
        for warning in scope_warnings:
            risks.append(warning.message)

        insight = RunSessionInsight(
            workspace_id=workspace_id,
            run_id=run_id,
            issue_id=run.issue_id,
            status=run.status,
            headline=self._draft_summary_from_excerpt(excerpt or "", run.issue_id, run.run_id),
            summary=critique.summary if critique and critique.summary else excerpt or f"Run {run.run_id} for {run.issue_id} is {run.status}.",
            guidance_used=run.guidance_paths,
            strengths=self._dedupe_text(strengths),
            risks=self._dedupe_text(risks),
            recommendations=self._dedupe_text(recommendations),
            acceptance_review=acceptance_review,
            scope_warnings=scope_warnings,
        )
        return insight.model_dump(mode="json")

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

    def list_workspace_metrics(self, workspace_id: str) -> list[dict]:
        self.get_workspace(workspace_id)
        metrics_list = self.runtime_service.get_workspace_metrics(workspace_id)
        return [metrics.model_dump(mode="json") for metrics in metrics_list]

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
        normalized_severity = (issue.severity or "").strip().lower()
        has_severity = normalized_severity in {"p0", "p1", "p2", "p3", "critical", "high", "medium", "low"}
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

    def _persist_issue_snapshot_record(self, workspace_id: str, issue: IssueRecord) -> None:
        snapshot = self.store.load_snapshot(workspace_id)
        if not snapshot:
            raise FileNotFoundError(workspace_id)
        updated_snapshot = snapshot.model_copy(
            update={"issues": [issue if item.bug_id == issue.bug_id else item for item in snapshot.issues]}
        )
        self.store.save_snapshot(updated_snapshot)
        if issue.source == "tracker" or self._tracked_issue_exists(workspace_id, issue.bug_id):
            self._persist_tracked_issue(workspace_id, issue)
        else:
            self._persist_issue_override(workspace_id, issue)

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
        if os.environ.get("XMUSTARD_USE_RUST_COVERAGE") in {"1", "true", "TRUE", "yes", "on"}:
            rust_result = self._parse_coverage_file_via_rust(path, workspace_id, run_id, issue_id)
            if rust_result is not None:
                return rust_result
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
        if path.suffix in (".csv", ".txt", ".info", ""):
            fmt = "lcov"
            return self._parse_lcov(content, workspace_id, run_id, issue_id, fmt, str(path))
        return CoverageResult(
            result_id=f"cov_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id, run_id=run_id, issue_id=issue_id,
            format=fmt, raw_report_path=str(path),
        )

    def _parse_coverage_file_via_rust(
        self,
        path: Path,
        workspace_id: str,
        run_id: Optional[str],
        issue_id: Optional[str],
    ) -> Optional[CoverageResult]:
        repo_root = Path(__file__).resolve().parents[2]
        rust_core_dir = repo_root / "rust-core"
        if not rust_core_dir.exists():
            return None

        explicit_bin = os.environ.get("XMUSTARD_RUST_CORE_BIN")
        if explicit_bin:
            command = [explicit_bin, "parse-coverage", workspace_id, str(path)]
            if run_id:
                command.append(run_id)
            if issue_id:
                command.append(issue_id)
            cwd = repo_root
        else:
            command = ["cargo", "run", "--quiet", "--bin", "xmustard-core", "--", "parse-coverage", workspace_id, str(path)]
            if run_id:
                command.append(run_id)
            if issue_id:
                command.append(issue_id)
            cwd = rust_core_dir

        try:
            completed = subprocess.run(command, capture_output=True, text=True, check=False, cwd=cwd)
        except FileNotFoundError:
            return None

        if completed.returncode != 0:
            return None

        try:
            payload = json.loads(completed.stdout)
        except json.JSONDecodeError:
            return None
        if not isinstance(payload, dict):
            return None

        try:
            return CoverageResult.model_validate(payload)
        except Exception:
            return None

    def _run_verification_command(
        self,
        workspace_root: Path,
        command: str,
        timeout_seconds: int,
    ) -> VerificationCommandResult:
        timeout_seconds = max(1, int(timeout_seconds))
        if os.environ.get("XMUSTARD_USE_RUST_VERIFICATION") in {"1", "true", "TRUE", "yes", "on"}:
            rust_result = self._run_verification_command_via_rust(workspace_root, command, timeout_seconds)
            if rust_result is not None:
                return rust_result

        started = time.perf_counter()
        try:
            completed = subprocess.run(
                ["/bin/sh", "-lc", command],
                capture_output=True,
                text=True,
                check=False,
                cwd=workspace_root,
                timeout=timeout_seconds,
            )
            return VerificationCommandResult(
                command=command,
                cwd=str(workspace_root.resolve()),
                exit_code=completed.returncode,
                success=completed.returncode == 0,
                timed_out=False,
                duration_ms=int((time.perf_counter() - started) * 1000),
                stdout_excerpt=self._truncate_command_excerpt(completed.stdout),
                stderr_excerpt=self._truncate_command_excerpt(completed.stderr),
            )
        except subprocess.TimeoutExpired as exc:
            return VerificationCommandResult(
                command=command,
                cwd=str(workspace_root.resolve()),
                exit_code=None,
                success=False,
                timed_out=True,
                duration_ms=int((time.perf_counter() - started) * 1000),
                stdout_excerpt=self._truncate_command_excerpt(exc.stdout or ""),
                stderr_excerpt=self._truncate_command_excerpt(exc.stderr or ""),
            )

    def _run_verification_command_via_rust(
        self,
        workspace_root: Path,
        command: str,
        timeout_seconds: int,
    ) -> Optional[VerificationCommandResult]:
        repo_root = Path(__file__).resolve().parents[2]
        rust_core_dir = repo_root / "rust-core"
        if not rust_core_dir.exists():
            return None

        explicit_bin = os.environ.get("XMUSTARD_RUST_CORE_BIN")
        if explicit_bin:
            command_line = [explicit_bin, "run-verification-command", str(workspace_root), str(timeout_seconds), command]
            cwd = repo_root
        else:
            command_line = [
                "cargo",
                "run",
                "--quiet",
                "--bin",
                "xmustard-core",
                "--",
                "run-verification-command",
                str(workspace_root),
                str(timeout_seconds),
                command,
            ]
            cwd = rust_core_dir

        try:
            completed = subprocess.run(command_line, capture_output=True, text=True, check=False, cwd=cwd)
        except FileNotFoundError:
            return None

        if completed.returncode != 0:
            return None

        try:
            payload = json.loads(completed.stdout)
        except json.JSONDecodeError:
            return None
        if not isinstance(payload, dict):
            return None

        try:
            return VerificationCommandResult.model_validate(payload)
        except Exception:
            return None

    def _truncate_command_excerpt(self, text: str, limit: int = 4000) -> str:
        if len(text) <= limit:
            return text
        omitted = len(text) - limit
        return f"{text[:limit]}\n...[truncated {omitted} chars]"

    def _execute_verification_profile(
        self,
        workspace_root: Path,
        profile: VerificationProfileRecord,
        run_id: Optional[str] = None,
        issue_id: Optional[str] = None,
    ) -> VerificationProfileExecutionResult:
        if os.environ.get("XMUSTARD_USE_RUST_VERIFICATION") in {"1", "true", "TRUE", "yes", "on"}:
            rust_result = self._execute_verification_profile_via_rust(workspace_root, profile, run_id, issue_id)
            if rust_result is not None:
                return rust_result

        attempts: list[VerificationCommandResult] = []
        max_attempts = max(1, int(profile.retry_count) + 1)
        for _ in range(max_attempts):
            attempt = self._run_verification_command(workspace_root, profile.test_command, profile.max_runtime_seconds)
            attempts.append(attempt)
            if attempt.success:
                break

        coverage_command_result: Optional[VerificationCommandResult] = None
        coverage_result: Optional[CoverageResult] = None
        resolved_report_path = self._resolve_verification_report_path(workspace_root, profile.coverage_report_path)

        if attempts and attempts[-1].success and profile.coverage_command:
            coverage_command_result = self._run_verification_command(
                workspace_root,
                profile.coverage_command,
                profile.max_runtime_seconds,
            )

        coverage_ready = attempts and attempts[-1].success
        if coverage_command_result is not None:
            coverage_ready = coverage_ready and coverage_command_result.success

        if coverage_ready and resolved_report_path and resolved_report_path.exists():
            coverage_result = self._parse_coverage_file(
                resolved_report_path,
                profile.workspace_id,
                run_id,
                issue_id,
            )

        return VerificationProfileExecutionResult(
            profile_id=profile.profile_id,
            workspace_id=profile.workspace_id,
            attempts=attempts,
            attempt_count=len(attempts),
            success=bool(attempts and attempts[-1].success and (coverage_command_result is None or coverage_command_result.success)),
            coverage_command_result=coverage_command_result,
            coverage_result=coverage_result,
            coverage_report_path=str(resolved_report_path) if resolved_report_path else None,
        )

    def _execute_verification_profile_via_rust(
        self,
        workspace_root: Path,
        profile: VerificationProfileRecord,
        run_id: Optional[str],
        issue_id: Optional[str],
    ) -> Optional[VerificationProfileExecutionResult]:
        repo_root = Path(__file__).resolve().parents[2]
        rust_core_dir = repo_root / "rust-core"
        if not rust_core_dir.exists():
            return None

        with tempfile.NamedTemporaryFile("w", suffix=".json", encoding="utf-8", delete=False) as handle:
            profile_path = Path(handle.name)
            json.dump(profile.model_dump(mode="json"), handle)

        try:
            explicit_bin = os.environ.get("XMUSTARD_RUST_CORE_BIN")
            if explicit_bin:
                command_line = [explicit_bin, "run-verification-profile", str(workspace_root), str(profile_path)]
                if run_id:
                    command_line.append(run_id)
                if issue_id:
                    command_line.append(issue_id)
                cwd = repo_root
            else:
                command_line = [
                    "cargo",
                    "run",
                    "--quiet",
                    "--bin",
                    "xmustard-core",
                    "--",
                    "run-verification-profile",
                    str(workspace_root),
                    str(profile_path),
                ]
                if run_id:
                    command_line.append(run_id)
                if issue_id:
                    command_line.append(issue_id)
                cwd = rust_core_dir

            try:
                completed = subprocess.run(command_line, capture_output=True, text=True, check=False, cwd=cwd)
            except FileNotFoundError:
                return None

            if completed.returncode != 0:
                return None

            try:
                payload = json.loads(completed.stdout)
            except json.JSONDecodeError:
                return None
            if not isinstance(payload, dict):
                return None

            try:
                return VerificationProfileExecutionResult.model_validate(payload)
            except Exception:
                return None
        finally:
            profile_path.unlink(missing_ok=True)

    def _resolve_verification_report_path(self, workspace_root: Path, report_path: Optional[str]) -> Optional[Path]:
        if not report_path:
            return None
        candidate = Path(report_path)
        if candidate.is_absolute():
            return candidate
        return workspace_root / candidate

    def _build_verification_checklist_results(
        self,
        profile: VerificationProfileRecord,
        execution: VerificationProfileExecutionResult,
    ) -> list[VerificationChecklistResult]:
        latest_attempt = execution.attempts[-1] if execution.attempts else None
        coverage_available = bool(
            execution.coverage_result is not None
            or (execution.coverage_command_result is not None and execution.coverage_command_result.success)
        )
        results = [
            VerificationChecklistResult(
                item_id="system:test-command",
                title="Verification command passes",
                kind="system",
                passed=execution.success,
                details=(
                    "Latest verification attempt succeeded."
                    if execution.success
                    else latest_attempt.stderr_excerpt[:240] if latest_attempt and latest_attempt.stderr_excerpt else "Verification command failed."
                ),
            )
        ]
        if profile.coverage_command or profile.coverage_report_path:
            results.append(
                VerificationChecklistResult(
                    item_id="system:coverage-artifact",
                    title="Coverage artifact is produced",
                    kind="system",
                    passed=coverage_available,
                    details=(
                        execution.coverage_report_path
                        if coverage_available and execution.coverage_report_path
                        else "No coverage artifact was produced."
                    ),
                )
            )
        if profile.checklist_items:
            for index, item in enumerate(profile.checklist_items, start=1):
                normalized = item.lower()
                if "coverage" in normalized:
                    passed = coverage_available
                    details = "Coverage data was captured." if passed else "Coverage data is still missing."
                else:
                    passed = execution.success
                    details = "Verification completed successfully." if passed else "Verification did not complete cleanly."
                results.append(
                    VerificationChecklistResult(
                        item_id=f"custom:{index}",
                        title=item,
                        kind="custom",
                        passed=passed,
                        details=details,
                    )
                )
        return results

    def _score_verification_execution_confidence(self, execution: VerificationProfileExecutionResult) -> str:
        if not execution.success:
            return "low"
        failed_items = [item for item in execution.checklist_results if not item.passed]
        if failed_items:
            return "medium"
        if execution.attempt_count <= 1:
            return "high"
        return "medium"

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

    def generate_patch_critique(self, workspace_id: str, run_id: str) -> dict:
        run = self.store.load_run(workspace_id, run_id)
        if not run:
            raise FileNotFoundError(run_id)
        if run.status not in {"completed", "failed"}:
            raise ValueError(f"Cannot critique run in status {run.status}")
        workspace = self.get_workspace(workspace_id)
        packet = self.build_issue_context(workspace_id, run.issue_id)
        output_path = Path(run.output_path)
        output_text = ""
        if output_path.exists():
            output_text = output_path.read_text(encoding="utf-8")
        improvements = self._analyze_run_output(run, output_text)
        issues_found = self._detect_patch_issues(run, output_text)
        correctness = self._score_correctness(run, output_text, issues_found)
        completeness = self._score_completeness(run, output_text)
        style = self._score_style(output_text)
        safety = self._score_safety(run, output_text, issues_found)
        avg = (correctness + completeness + style + safety) / 4
        if avg >= 0.85:
            quality = "excellent"
        elif avg >= 0.7:
            quality = "good"
        elif avg >= 0.5:
            quality = "acceptable"
        elif avg >= 0.3:
            quality = "needs_work"
        else:
            quality = "poor"
        critique = PatchCritique(
            critique_id=f"crit_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id,
            run_id=run_id,
            issue_id=run.issue_id,
            overall_quality=quality,
            correctness=round(correctness * 100, 1),
            completeness=round(completeness * 100, 1),
            style=round(style * 100, 1),
            safety=round(safety * 100, 1),
            issues_found=issues_found,
            improvements=improvements,
            summary=self._critique_summary(quality, issues_found, improvements),
            acceptance_review=self._build_acceptance_review(packet, run, output_text),
            scope_warnings=self._build_scope_warnings(packet, run),
        )
        self._save_critique(critique)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=run_id,
            action="critique.generated",
            summary=f"Generated patch critique: {quality}",
            actor=build_activity_actor("system", "system"),
            issue_id=run.issue_id,
            run_id=run_id,
            details={"quality": quality, "issues": len(issues_found), "improvements": len(improvements)},
        )
        return critique.model_dump(mode="json")

    def get_patch_critique(self, workspace_id: str, run_id: str) -> Optional[dict]:
        critique = self._load_critique(run_id)
        if not critique:
            return None
        return critique.model_dump(mode="json")

    def dismiss_improvement(self, workspace_id: str, run_id: str, suggestion_id: str, request: DismissImprovementRequest) -> dict:
        critique = self._load_critique(run_id)
        if not critique:
            raise FileNotFoundError(f"No critique found for run {run_id}")
        updated_improvements = []
        found = False
        for imp in critique.improvements:
            if imp.suggestion_id == suggestion_id:
                updated_improvements.append(imp.model_copy(update={
                    "dismissed": True,
                    "dismissed_reason": request.reason,
                }))
                found = True
            else:
                updated_improvements.append(imp)
        if not found:
            raise FileNotFoundError(f"Improvement {suggestion_id} not found")
        updated = critique.model_copy(update={"improvements": updated_improvements})
        self._save_critique(updated)
        return updated.model_dump(mode="json")

    def get_run_improvements(self, workspace_id: str, run_id: str) -> list[dict]:
        critique = self._load_critique(run_id)
        if not critique:
            return []
        return [imp.model_dump(mode="json") for imp in critique.improvements if not imp.dismissed]

    def _analyze_run_output(self, run: RunRecord, output: str) -> list[ImprovementSuggestion]:
        improvements: list[ImprovementSuggestion] = []
        lines = output.splitlines()
        seen_files: set[str] = set()
        for i, line in enumerate(lines):
            stripped = line.strip()
            if not stripped:
                continue
            for pattern in ("TODO", "FIXME", "HACK", "XXX"):
                if pattern in stripped:
                    improvements.append(ImprovementSuggestion(
                        suggestion_id=f"imp_{uuid.uuid4().hex[:8]}",
                        file_path=self._extract_file_from_line(stripped) or "unknown",
                        line_start=i + 1,
                        category="maintainability",
                        severity="low",
                        description=f"Found {pattern} comment in generated output",
                    ))
            if any(kw in stripped.lower() for kw in ("password", "secret", "api_key", "token")):
                if "env(" not in stripped and "os.environ" not in stripped and "getenv" not in stripped:
                    improvements.append(ImprovementSuggestion(
                        suggestion_id=f"imp_{uuid.uuid4().hex[:8]}",
                        file_path=self._extract_file_from_line(stripped) or "unknown",
                        line_start=i + 1,
                        category="security",
                        severity="high",
                        description="Possible hardcoded secret or credential",
                    ))
        if run.summary and isinstance(run.summary, dict):
            tool_events = run.summary.get("tool_event_count", 0)
            total_events = run.summary.get("event_count", 0)
            if total_events > 0 and tool_events / total_events > 0.8:
                improvements.append(ImprovementSuggestion(
                    suggestion_id=f"imp_{uuid.uuid4().hex[:8]}",
                    file_path="general",
                    category="performance",
                    severity="low",
                    description="High ratio of tool events to total events - consider reducing tool calls",
                ))
        if len(lines) > 2000:
            improvements.append(ImprovementSuggestion(
                suggestion_id=f"imp_{uuid.uuid4().hex[:8]}",
                file_path="general",
                category="maintainability",
                severity="medium",
                description="Output is very large - consider breaking into smaller operations",
            ))
        return improvements[:20]

    def _extract_file_from_line(self, line: str) -> Optional[str]:
        for pattern in (r"([\w/.-]+\.\w+):", r"file[:=]\s*([\w/.-]+\.\w+)", r"([\w/.-]+\.\w+)\s*\|"):
            m = re.search(pattern, line)
            if m:
                return m.group(1)
        return None

    def _detect_patch_issues(self, run: RunRecord, output: str) -> list[str]:
        issues: list[str] = []
        if run.exit_code and run.exit_code != 0:
            issues.append(f"Run exited with non-zero code: {run.exit_code}")
        if run.error:
            issues.append(f"Run had error: {run.error[:200]}")
        lower = output.lower()
        if "traceback" in lower:
            issues.append("Python traceback found in output")
        if "exception" in lower and "caught" not in lower:
            issues.append("Uncaught exception detected in output")
        if "panic" in lower:
            issues.append("Panic detected in output")
        if "segmentation fault" in lower or "segfault" in lower:
            issues.append("Segmentation fault detected")
        if not output.strip():
            issues.append("Empty output - no changes generated")
        return issues

    def _score_correctness(self, run: RunRecord, output: str, issues: list[str]) -> float:
        score = 1.0
        if run.exit_code and run.exit_code != 0:
            score -= 0.3
        if run.error:
            score -= 0.2
        for issue in issues:
            if "traceback" in issue.lower() or "exception" in issue.lower():
                score -= 0.15
            if "empty output" in issue.lower():
                score -= 0.4
        return max(0.0, score)

    def _score_completeness(self, run: RunRecord, output: str) -> float:
        score = 0.0
        if output.strip():
            score += 0.3
        if run.summary and isinstance(run.summary, dict):
            if run.summary.get("text_excerpt"):
                score += 0.3
            if run.summary.get("tool_event_count", 0) > 0:
                score += 0.2
            if run.summary.get("event_count", 0) > 3:
                score += 0.2
        return min(1.0, score)

    def _score_style(self, output: str) -> float:
        if not output.strip():
            return 0.3
        score = 0.7
        lines = output.splitlines()
        if lines:
            avg_len = sum(len(l) for l in lines) / len(lines)
            if avg_len > 200:
                score -= 0.1
        very_long = sum(1 for l in lines if len(l) > 500)
        if very_long > 5:
            score -= 0.15
        return max(0.0, min(1.0, score))

    def _score_safety(self, run: RunRecord, output: str, issues: list[str]) -> float:
        score = 1.0
        lower = output.lower()
        if any(kw in lower for kw in ("rm -rf", "del /s", "format c:", "drop table", "delete from")):
            score -= 0.4
        if any(kw in lower for kw in ("os.system", "subprocess.call", "exec(", "eval(")):
            score -= 0.2
        for issue in issues:
            if "panic" in issue.lower() or "segfault" in issue.lower():
                score -= 0.3
        return max(0.0, score)

    def _critique_summary(self, quality: str, issues: list[str], improvements: list[ImprovementSuggestion]) -> str:
        parts = [f"Overall quality: {quality}"]
        if issues:
            parts.append(f"Found {len(issues)} issue(s)")
        active = [i for i in improvements if not i.dismissed]
        if active:
            high = sum(1 for i in active if i.severity == "high")
            if high:
                parts.append(f"{high} high-severity improvement(s) suggested")
        return ". ".join(parts)

    def _save_critique(self, critique: PatchCritique) -> None:
        crit_dir = self.store.data_dir / "critiques"
        crit_dir.mkdir(parents=True, exist_ok=True)
        path = crit_dir / f"{critique.run_id}.json"
        path.write_text(critique.model_dump_json(), encoding="utf-8")

    def _load_critique(self, run_id: str) -> Optional[PatchCritique]:
        crit_dir = self.store.data_dir / "critiques"
        path = crit_dir / f"{run_id}.json"
        if not path.exists():
            return None
        try:
            return PatchCritique.model_validate_json(path.read_text(encoding="utf-8"))
        except Exception:
            return None

    def configure_integration(self, workspace_id: str, provider: str, settings: dict) -> dict:
        config = IntegrationConfig(
            config_id=f"int_{uuid.uuid4().hex[:12]}",
            workspace_id=workspace_id,
            provider=provider,
            enabled=True,
            settings=settings,
        )
        self._save_integration_config(config)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="settings",
            entity_id=f"integration:{provider}",
            action="integration.configured",
            summary=f"Configured {provider} integration",
            actor=build_activity_actor("operator", "operator"),
            details={"provider": provider},
        )
        return config.model_dump(mode="json")

    def get_integration_configs(self, workspace_id: str) -> list[dict]:
        configs = self._load_integration_configs(workspace_id)
        return [c.model_dump(mode="json") for c in configs]

    def test_integration(self, request: IntegrationTestRequest) -> dict:
        result = self._test_provider(request.provider, request.settings)
        return result.model_dump(mode="json")

    def import_github_issues(self, workspace_id: str, repo: str, state: str = "open") -> list[dict]:
        config = self._get_integration_config(workspace_id, "github")
        token = config.settings.get("token", "") if config else ""
        headers = {"Accept": "application/vnd.github+json"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        url = f"https://api.github.com/repos/{repo}/issues?state={state}&per_page=100"
        req = urllib.request.Request(url, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                issues_data = json.loads(resp.read().decode("utf-8"))
        except urllib.error.URLError as exc:
            raise ValueError(f"GitHub API error: {exc}")
        imports = []
        for gh_issue in issues_data:
            if "pull_request" in gh_issue:
                continue
            imp = GitHubIssueImport(
                import_id=f"ghi_{uuid.uuid4().hex[:8]}",
                workspace_id=workspace_id,
                github_repo=repo,
                issue_number=gh_issue["number"],
                issue_id=f"GH-{gh_issue['number']}",
                title=gh_issue.get("title", ""),
                body=gh_issue.get("body"),
                labels=[l["name"] for l in gh_issue.get("labels", [])],
                state=gh_issue.get("state", "open"),
                html_url=gh_issue.get("html_url"),
            )
            imports.append(imp)
            existing = self.store.load_snapshot(workspace_id)
            if existing:
                for issue in existing.issues:
                    if issue.bug_id == imp.issue_id:
                        break
                else:
                    self.create_issue(workspace_id, IssueCreateRequest(
                        bug_id=imp.issue_id,
                        title=imp.title,
                        severity="medium",
                        summary=imp.body[:500] if imp.body else None,
                        labels=imp.labels,
                        source="tracker_issue",
                    ))
            self._save_imported_ticket_context(
                workspace_id,
                imp.issue_id,
                TicketContextUpsertRequest(
                    context_id=f"github-{imp.issue_number}",
                    provider="github",
                    external_id=f"{repo}#{imp.issue_number}",
                    title=imp.title,
                    summary=(imp.body or "")[:1600],
                    acceptance_criteria=self._parse_acceptance_criteria(imp.body),
                    labels=imp.labels,
                    links=[imp.html_url] if imp.html_url else [],
                    status=imp.state,
                    source_excerpt=(imp.body or "")[:500] or None,
                ),
                actor=build_activity_actor("system", "system"),
            )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="workspace",
            entity_id=workspace_id,
            action="github.imported",
            summary=f"Imported {len(imports)} issues from {repo}",
            actor=build_activity_actor("system", "system"),
            details={"repo": repo, "count": len(imports)},
        )
        return [i.model_dump(mode="json") for i in imports]

    def create_github_pr(self, workspace_id: str, request: GitHubPRCreate) -> dict:
        config = self._get_integration_config(workspace_id, "github")
        if not config:
            raise FileNotFoundError("GitHub integration not configured")
        token = config.settings.get("token", "")
        repo = config.settings.get("repo", "")
        if not token or not repo:
            raise ValueError("GitHub token and repo must be configured")
        run = self.store.load_run(workspace_id, request.run_id)
        pr_title = request.title or (f"Fix: {run.title}" if run else f"Fix for {request.issue_id}")
        pr_body = request.body or ""
        if run and isinstance(run.summary, dict):
            pr_body += f"\n\n**Run Summary:** {run.summary.get('text_excerpt', 'N/A')}"
        pr_body += f"\n\n**Issue:** {request.issue_id}"
        data = json.dumps({
            "title": pr_title,
            "body": pr_body,
            "head": request.head_branch,
            "base": request.base_branch,
            "draft": request.draft,
        }).encode("utf-8")
        headers = {
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        }
        url = f"https://api.github.com/repos/{repo}/pulls"
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                pr_data = json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise ValueError(f"GitHub PR creation failed ({exc.code}): {body[:500]}")
        result = GitHubPRResult(
            pr_id=f"pr_{uuid.uuid4().hex[:8]}",
            workspace_id=workspace_id,
            run_id=request.run_id,
            issue_id=request.issue_id,
            pr_number=pr_data["number"],
            html_url=pr_data.get("html_url", ""),
        )
        self._save_pr_result(result)
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="run",
            entity_id=request.run_id,
            action="github.pr_created",
            summary=f"Created PR #{pr_data['number']} for {request.issue_id}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=request.issue_id,
            run_id=request.run_id,
            details={"pr_number": pr_data["number"], "html_url": result.html_url},
        )
        return result.model_dump(mode="json")

    def send_slack_notification(self, workspace_id: str, event: str, message: Optional[str] = None) -> dict:
        config = self._get_integration_config(workspace_id, "slack")
        if not config:
            raise FileNotFoundError("Slack integration not configured")
        webhook_url = config.settings.get("webhook_url", "")
        channel = config.settings.get("channel")
        if not webhook_url:
            raise ValueError("Slack webhook_url must be configured")
        text = message or self._default_slack_message(workspace_id, event)
        payload = {"text": text}
        if channel:
            payload["channel"] = channel
        notification = SlackNotification(
            notification_id=f"sn_{uuid.uuid4().hex[:8]}",
            workspace_id=workspace_id,
            event=event,
            channel=channel,
            webhook_url=webhook_url,
            message=text,
            status="pending",
        )
        data = json.dumps(payload).encode("utf-8")
        headers = {"Content-Type": "application/json"}
        req = urllib.request.Request(webhook_url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                resp.read()
            notification = notification.model_copy(update={"status": "sent", "sent_at": utc_now()})
        except urllib.error.URLError as exc:
            notification = notification.model_copy(update={"status": "failed", "error": str(exc)})
        self._save_notification(notification)
        return notification.model_dump(mode="json")

    def sync_issue_to_linear(self, workspace_id: str, issue_id: str) -> dict:
        config = self._get_integration_config(workspace_id, "linear")
        if not config:
            raise FileNotFoundError("Linear integration not configured")
        api_key = config.settings.get("api_key", "")
        team_id = config.settings.get("team_id", "")
        if not api_key or not team_id:
            raise ValueError("Linear api_key and team_id must be configured")
        issue = self._get_issue(workspace_id, issue_id)
        query = {
            "query": """
            mutation CreateIssue($title: String!, $teamId: String!, $description: String, $priority: Float) {
                issueCreate(input: {title: $title, teamId: $teamId, description: $description, priority: $priority}) {
                    issue { id identifier title url }
                }
            }
            """,
            "variables": {
                "title": issue.title,
                "teamId": team_id,
                "description": issue.summary or "",
                "priority": self._severity_to_linear_priority(issue.severity),
            },
        }
        data = json.dumps(query).encode("utf-8")
        headers = {"Content-Type": "application/json", "Authorization": api_key}
        req = urllib.request.Request("https://api.linear.app/graphql", data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode("utf-8"))
        except urllib.error.URLError as exc:
            raise ValueError(f"Linear API error: {exc}")
        issue_data = result.get("data", {}).get("issueCreate", {}).get("issue", {})
        sync = LinearIssueSync(
            sync_id=f"lsync_{uuid.uuid4().hex[:8]}",
            workspace_id=workspace_id,
            issue_id=issue_id,
            linear_id=issue_data.get("id"),
            linear_team_id=team_id,
            linear_status=issue_data.get("identifier"),
            title=issue.title,
            description=issue.summary,
            labels=issue.labels,
            priority=issue.severity,
        )
        self._save_linear_sync(sync)
        self._save_imported_ticket_context(
            workspace_id,
            issue_id,
            TicketContextUpsertRequest(
                context_id=f"linear-{issue_data.get('id') or issue_id}",
                provider="linear",
                external_id=issue_data.get("identifier") or issue_data.get("id"),
                title=issue.title,
                summary=issue.summary or "",
                labels=issue.labels,
                links=[issue_data.get("url")] if issue_data.get("url") else [],
                status="synced",
            ),
            actor=build_activity_actor("system", "system"),
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="linear.synced",
            summary=f"Synced issue to Linear: {issue_data.get('identifier', 'unknown')}",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"linear_id": issue_data.get("id")},
        )
        return sync.model_dump(mode="json")

    def sync_issue_to_jira(self, workspace_id: str, issue_id: str) -> dict:
        config = self._get_integration_config(workspace_id, "jira")
        if not config:
            raise FileNotFoundError("Jira integration not configured")
        base_url = config.settings.get("base_url", "")
        email = config.settings.get("email", "")
        api_token = config.settings.get("api_token", "")
        project_key = config.settings.get("project_key", "")
        if not all([base_url, email, api_token, project_key]):
            raise ValueError("Jira base_url, email, api_token, and project_key must be configured")
        issue = self._get_issue(workspace_id, issue_id)
        import base64
        creds = base64.b64encode(f"{email}:{api_token}".encode()).decode()
        fields = {
            "project": {"key": project_key},
            "summary": issue.title,
            "description": {"type": "doc", "version": 1, "content": [{"type": "paragraph", "content": [{"type": "text", "text": issue.summary or ""}]}]},
            "issuetype": {"name": "Bug"},
            "labels": issue.labels[:10],
            "priority": {"name": self._severity_to_jira_priority(issue.severity)},
        }
        data = json.dumps({"fields": fields}).encode("utf-8")
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Basic {creds}",
        }
        url = f"{base_url.rstrip('/')}/rest/api/3/issue"
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                result = json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise ValueError(f"Jira API error ({exc.code}): {body[:500]}")
        sync = JiraIssueSync(
            sync_id=f"jsync_{uuid.uuid4().hex[:8]}",
            workspace_id=workspace_id,
            issue_id=issue_id,
            jira_key=result.get("key"),
            jira_project=project_key,
            summary=issue.title,
            description=issue.summary,
            labels=issue.labels,
            priority=issue.severity,
        )
        self._save_jira_sync(sync)
        jira_browse_url = f"{base_url.rstrip('/')}/browse/{result.get('key')}" if result.get("key") else None
        self._save_imported_ticket_context(
            workspace_id,
            issue_id,
            TicketContextUpsertRequest(
                context_id=f"jira-{result.get('key') or issue_id}",
                provider="jira",
                external_id=result.get("key"),
                title=issue.title,
                summary=issue.summary or "",
                labels=issue.labels,
                links=[jira_browse_url] if jira_browse_url else [],
                status="synced",
            ),
            actor=build_activity_actor("system", "system"),
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=issue_id,
            action="jira.synced",
            summary=f"Synced issue to Jira: {result.get('key', 'unknown')}",
            actor=build_activity_actor("system", "system"),
            issue_id=issue_id,
            details={"jira_key": result.get("key")},
        )
        return sync.model_dump(mode="json")

    def _save_integration_config(self, config: IntegrationConfig) -> None:
        cfg_dir = self.store.data_dir / "integrations"
        cfg_dir.mkdir(parents=True, exist_ok=True)
        path = cfg_dir / f"{config.workspace_id}_{config.provider}.json"
        path.write_text(config.model_dump_json(), encoding="utf-8")

    def _load_integration_configs(self, workspace_id: str) -> list[IntegrationConfig]:
        cfg_dir = self.store.data_dir / "integrations"
        if not cfg_dir.exists():
            return []
        configs = []
        for f in cfg_dir.glob(f"{workspace_id}_*.json"):
            try:
                configs.append(IntegrationConfig.model_validate_json(f.read_text(encoding="utf-8")))
            except Exception:
                continue
        return configs

    def _get_integration_config(self, workspace_id: str, provider: str) -> Optional[IntegrationConfig]:
        cfg_dir = self.store.data_dir / "integrations"
        path = cfg_dir / f"{workspace_id}_{provider}.json"
        if not path.exists():
            return None
        try:
            return IntegrationConfig.model_validate_json(path.read_text(encoding="utf-8"))
        except Exception:
            return None

    def _test_provider(self, provider: str, settings: dict) -> IntegrationTestResult:
        if provider == "github":
            return self._test_github(settings)
        if provider == "slack":
            return self._test_slack(settings)
        if provider == "linear":
            return self._test_linear(settings)
        if provider == "jira":
            return self._test_jira(settings)
        return IntegrationTestResult(provider=provider, ok=False, message=f"Unknown provider: {provider}")

    def _test_github(self, settings: dict) -> IntegrationTestResult:
        token = settings.get("token", "")
        headers = {"Accept": "application/vnd.github+json"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        try:
            req = urllib.request.Request("https://api.github.com/user", headers=headers)
            with urllib.request.urlopen(req, timeout=10) as resp:
                data = json.loads(resp.read().decode("utf-8"))
            return IntegrationTestResult(
                provider="github", ok=True,
                message=f"Connected as {data.get('login', 'unknown')}",
                details={"login": data.get("login"), "name": data.get("name")},
            )
        except Exception as exc:
            return IntegrationTestResult(provider="github", ok=False, message=str(exc))

    def _test_slack(self, settings: dict) -> IntegrationTestResult:
        webhook_url = settings.get("webhook_url", "")
        if not webhook_url:
            return IntegrationTestResult(provider="slack", ok=False, message="webhook_url required")
        try:
            data = json.dumps({"text": "xMustard integration test"}).encode("utf-8")
            req = urllib.request.Request(webhook_url, data=data, headers={"Content-Type": "application/json"}, method="POST")
            with urllib.request.urlopen(req, timeout=10):
                pass
            return IntegrationTestResult(provider="slack", ok=True, message="Webhook test sent successfully")
        except Exception as exc:
            return IntegrationTestResult(provider="slack", ok=False, message=str(exc))

    def _test_linear(self, settings: dict) -> IntegrationTestResult:
        api_key = settings.get("api_key", "")
        if not api_key:
            return IntegrationTestResult(provider="linear", ok=False, message="api_key required")
        try:
            query = {"query": "{ viewer { id name } }"}
            data = json.dumps(query).encode("utf-8")
            req = urllib.request.Request("https://api.linear.app/graphql", data=data, headers={"Content-Type": "application/json", "Authorization": api_key}, method="POST")
            with urllib.request.urlopen(req, timeout=10) as resp:
                result = json.loads(resp.read().decode("utf-8"))
            viewer = result.get("data", {}).get("viewer", {})
            return IntegrationTestResult(
                provider="linear", ok=True,
                message=f"Connected as {viewer.get('name', 'unknown')}",
                details={"name": viewer.get("name")},
            )
        except Exception as exc:
            return IntegrationTestResult(provider="linear", ok=False, message=str(exc))

    def _test_jira(self, settings: dict) -> IntegrationTestResult:
        base_url = settings.get("base_url", "")
        email = settings.get("email", "")
        api_token = settings.get("api_token", "")
        if not all([base_url, email, api_token]):
            return IntegrationTestResult(provider="jira", ok=False, message="base_url, email, and api_token required")
        import base64
        creds = base64.b64encode(f"{email}:{api_token}".encode()).decode()
        try:
            req = urllib.request.Request(
                f"{base_url.rstrip('/')}/rest/api/3/myself",
                headers={"Authorization": f"Basic {creds}"},
            )
            with urllib.request.urlopen(req, timeout=10) as resp:
                data = json.loads(resp.read().decode("utf-8"))
            return IntegrationTestResult(
                provider="jira", ok=True,
                message=f"Connected as {data.get('displayName', 'unknown')}",
                details={"displayName": data.get("displayName")},
            )
        except Exception as exc:
            return IntegrationTestResult(provider="jira", ok=False, message=str(exc))

    def _default_slack_message(self, workspace_id: str, event: str) -> str:
        event_labels = {
            "run.completed": "Run Completed",
            "run.failed": "Run Failed",
            "run.cancelled": "Run Cancelled",
            "verification.recorded": "Verification Recorded",
            "fix.applied": "Fix Applied",
            "plan.approved": "Plan Approved",
            "plan.rejected": "Plan Rejected",
        }
        return f"[xMustard] {event_labels.get(event, event)} (workspace: {workspace_id})"

    def _severity_to_linear_priority(self, severity: str) -> float:
        mapping = {"critical": 1, "high": 2, "medium": 3, "low": 4}
        return float(mapping.get(severity, 3))

    def _severity_to_jira_priority(self, severity: str) -> str:
        mapping = {"critical": "Highest", "high": "High", "medium": "Medium", "low": "Low"}
        return mapping.get(severity, "Medium")

    def _save_pr_result(self, result: GitHubPRResult) -> None:
        pr_dir = self.store.data_dir / "pull_requests"
        pr_dir.mkdir(parents=True, exist_ok=True)
        path = pr_dir / f"{result.pr_id}.json"
        path.write_text(result.model_dump_json(), encoding="utf-8")

    def _save_notification(self, notification: SlackNotification) -> None:
        n_dir = self.store.data_dir / "notifications"
        n_dir.mkdir(parents=True, exist_ok=True)
        path = n_dir / f"{notification.notification_id}.json"
        path.write_text(notification.model_dump_json(), encoding="utf-8")

    def _save_linear_sync(self, sync: LinearIssueSync) -> None:
        s_dir = self.store.data_dir / "linear_syncs"
        s_dir.mkdir(parents=True, exist_ok=True)
        path = s_dir / f"{sync.sync_id}.json"
        path.write_text(sync.model_dump_json(), encoding="utf-8")

    def _save_jira_sync(self, sync: JiraIssueSync) -> None:
        s_dir = self.store.data_dir / "jira_syncs"
        s_dir.mkdir(parents=True, exist_ok=True)
        path = s_dir / f"{sync.sync_id}.json"
        path.write_text(sync.model_dump_json(), encoding="utf-8")

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
            repo_map=self.read_repo_map(workspace_id),
            runs=self.store.list_runs(workspace_id),
            fixes=self.store.list_fix_records(workspace_id),
            run_reviews=self.store.list_run_reviews(workspace_id),
            runbooks=self.list_runbooks(workspace_id),
            verification_profiles=self.list_verification_profiles(workspace_id),
            verification_profile_history=self.list_verification_profile_history(workspace_id),
            ticket_contexts=self.list_ticket_contexts(workspace_id),
            threat_models=self.list_threat_models(workspace_id),
            context_replays=self.list_issue_context_replays(workspace_id),
            browser_dumps=self.list_browser_dumps(workspace_id),
            vulnerability_findings=self.list_vulnerability_findings(workspace_id),
            eval_scenarios=self.list_eval_scenarios(workspace_id),
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
        verification_profiles: list[VerificationProfileRecord],
        ticket_contexts: list[TicketContextRecord],
        threat_models: list[ThreatModelRecord],
        vulnerability_findings: list[VulnerabilityFindingRecord],
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
        profile_path = self.store.verification_profiles_path(workspace_id)
        if profile_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(profile_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="fix_record",
                    label=profile_path.name,
                    path=str(profile_path),
                    record_count=len(verification_profiles),
                    modified_at=utc_now(),
                    notes="Workspace verification profiles and saved test commands.",
                )
            )
        ticket_context_path = self.store.ticket_contexts_path(workspace_id)
        if ticket_context_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(ticket_context_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="ticket_context",
                    label=ticket_context_path.name,
                    path=str(ticket_context_path),
                    record_count=len(ticket_contexts),
                    modified_at=utc_now(),
                    notes="Imported and curated upstream ticket context with acceptance criteria.",
                )
            )
        threat_model_path = self.store.threat_models_path(workspace_id)
        if threat_model_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(threat_model_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="threat_model",
                    label=threat_model_path.name,
                    path=str(threat_model_path),
                    record_count=len(threat_models),
                    modified_at=utc_now(),
                    notes="Issue-level threat models with assets, trust boundaries, abuse paths, and mitigations.",
                )
            )
        vulnerability_findings_path = self.store.vulnerability_findings_path(workspace_id)
        if vulnerability_findings_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(vulnerability_findings_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="fix_record",
                    label=vulnerability_findings_path.name,
                    path=str(vulnerability_findings_path),
                    record_count=len(vulnerability_findings),
                    modified_at=utc_now(),
                    notes="Normalized vulnerability findings imported from scanners and linked to issues.",
                )
            )
        repo_map_path = self.store.repo_map_path(workspace_id)
        if repo_map_path.exists():
            sources.append(
                SourceRecord(
                    source_id=f"src_{hashlib.sha1(str(repo_map_path).encode('utf-8')).hexdigest()[:10]}",
                    kind="repo_map",
                    label=repo_map_path.name,
                    path=str(repo_map_path),
                    record_count=1,
                    modified_at=utc_now(),
                    notes="Workspace structural repo map and notable files.",
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

    def _default_verification_profiles(self, workspace: WorkspaceRecord) -> list[VerificationProfileRecord]:
        root = Path(workspace.root_path)
        guidance = self._collect_workspace_guidance(root, workspace.workspace_id)
        inferred = self._infer_verification_profiles_from_guidance(workspace.workspace_id, guidance, root)
        if inferred:
            return inferred
        return [
            VerificationProfileRecord(
                profile_id="manual-check",
                workspace_id=workspace.workspace_id,
                name="Manual verification",
                description="Fallback profile when no repo-specific test command could be inferred yet.",
                test_command="Document the exact command or test flow required for this repo.",
                built_in=True,
                source_paths=[],
            )
        ]

    def _resolve_runbook(self, workspace_id: str, runbook_id: str) -> RunbookRecord:
        for runbook in self.list_runbooks(workspace_id):
            if runbook.runbook_id == runbook_id:
                return runbook
        raise FileNotFoundError(runbook_id)

    def _resolve_verification_profile(self, workspace_id: str, profile_id: str) -> VerificationProfileRecord:
        for profile in self.list_verification_profiles(workspace_id):
            if profile.profile_id == profile_id:
                return profile
        raise FileNotFoundError(profile_id)

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

    def _normalize_text_list(self, values: list[str], limit: int = 12) -> list[str]:
        normalized: list[str] = []
        for value in values:
            item = str(value).strip()
            if not item or item in normalized:
                continue
            normalized.append(item)
            if len(normalized) >= limit:
                break
        return normalized

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
        guidance: list[RepoGuidanceRecord],
        verification_profiles: list[VerificationProfileRecord],
        ticket_contexts: list[TicketContextRecord],
        threat_models: list[ThreatModelRecord],
        browser_dumps: list[BrowserDumpRecord],
        vulnerability_findings: list[VulnerabilityFindingRecord],
        related_paths: list[str],
        repo_map: RepoMapSummary,
        dynamic_context: Optional[DynamicContextBundle],
        repo_config: Optional[RepoConfigRecord],
        matched_path_instructions: list[RepoPathInstructionMatch],
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
        guidance_lines = []
        for item in guidance[:self.GUIDANCE_LIMIT]:
            mode = "always-on" if item.always_on else "optional"
            guidance_lines.append(f"- {item.path} [{item.kind}, {mode}]: {item.summary or item.title}")
        verification_lines = []
        for profile in verification_profiles[:4]:
            coverage_bits = []
            if profile.coverage_command:
                coverage_bits.append(f"coverage command: {profile.coverage_command}")
            if profile.coverage_report_path:
                coverage_bits.append(f"report: {profile.coverage_report_path}")
            if profile.coverage_format != "unknown":
                coverage_bits.append(f"format: {profile.coverage_format}")
            coverage_summary = f" ({'; '.join(coverage_bits)})" if coverage_bits else ""
            verification_lines.append(f"- {profile.name}: {profile.test_command}{coverage_summary}")
        ticket_lines = []
        for context in ticket_contexts[:4]:
            header_bits = [context.provider]
            if context.external_id:
                header_bits.append(context.external_id)
            if context.status:
                header_bits.append(context.status)
            criteria = "; ".join(context.acceptance_criteria[:3]) or "No acceptance criteria recorded."
            ticket_lines.append(
                f"- {context.title} [{' / '.join(header_bits)}]: {context.summary or 'No summary.'} "
                f"Acceptance criteria: {criteria}"
            )
        threat_lines = []
        for threat_model in threat_models[:3]:
            assets = ", ".join(threat_model.assets[:3]) or "No assets listed."
            abuse_cases = "; ".join(threat_model.abuse_cases[:2]) or "No abuse cases listed."
            mitigations = "; ".join(threat_model.mitigations[:2]) or "No mitigations listed."
            threat_lines.append(
                f"- {threat_model.title} [{threat_model.methodology} / {threat_model.status}]: "
                f"{threat_model.summary or 'No summary.'} Assets: {assets}. Abuse cases: {abuse_cases}. "
                f"Mitigations: {mitigations}"
            )
        browser_lines = []
        for browser_dump in browser_dumps[:3]:
            page_bits = []
            if browser_dump.page_title:
                page_bits.append(browser_dump.page_title)
            if browser_dump.page_url:
                page_bits.append(browser_dump.page_url)
            page_summary = " | ".join(page_bits) if page_bits else "No page metadata recorded."
            console_excerpt = "; ".join(browser_dump.console_messages[:2]) or "No console messages recorded."
            network_excerpt = "; ".join(browser_dump.network_requests[:2]) or "No network requests recorded."
            dom_excerpt = browser_dump.dom_snapshot[:220].replace("\n", " ").strip() if browser_dump.dom_snapshot else "No DOM snapshot recorded."
            browser_lines.append(
                f"- {browser_dump.label} [{browser_dump.source}]: {browser_dump.summary or page_summary}. "
                f"Console: {console_excerpt}. Network: {network_excerpt}. DOM excerpt: {dom_excerpt}"
            )
        vulnerability_lines = []
        for finding in vulnerability_findings[:4]:
            location = finding.location_path or "No file location recorded."
            if finding.location_line:
                location = f"{location}:{finding.location_line}"
            rule_bits = []
            if finding.rule_id:
                rule_bits.append(finding.rule_id)
            if finding.cwe_ids:
                rule_bits.append(", ".join(finding.cwe_ids[:3]))
            if finding.cve_ids:
                rule_bits.append(", ".join(finding.cve_ids[:2]))
            rule_summary = " | ".join(rule_bits) if rule_bits else "No rule or taxonomy ids recorded."
            evidence_summary = "; ".join(finding.evidence[:2]) or "No scanner evidence recorded."
            vulnerability_lines.append(
                f"- {finding.title} [{finding.scanner} / {finding.source} / {finding.severity} / {finding.status}]: "
                f"{finding.summary or 'No summary.'} Location: {location}. IDs: {rule_summary}. Evidence: {evidence_summary}"
            )
        repo_config_lines = []
        if repo_config:
            if repo_config.description:
                repo_config_lines.append(f"- Description: {repo_config.description}")
            if repo_config.code_guidelines:
                repo_config_lines.append(f"- Code guidelines: {', '.join(repo_config.code_guidelines[:6])}")
            if repo_config.path_filters:
                repo_config_lines.append(f"- Path filters: {', '.join(repo_config.path_filters[:6])}")
            for server in repo_config.mcp_servers[:3]:
                detail = server.description or server.usage or "Configured MCP context source."
                repo_config_lines.append(f"- MCP {server.name}: {detail}")
        repo_dir_lines = [
            f"- {item.path}: {item.source_file_count} source files, {item.test_file_count} test files"
            for item in repo_map.top_directories[:5]
        ]
        related_lines = "\n".join(f"- {path}" for path in related_paths[:8]) or "- No related paths ranked yet."
        path_instruction_lines = []
        for item in matched_path_instructions[:6]:
            label = item.title or item.path
            path_instruction_lines.append(
                f"- {label} [{', '.join(item.matched_paths[:4])}]: {item.instructions}"
            )
        symbol_lines = []
        related_artifact_lines = []
        if dynamic_context:
            for symbol in dynamic_context.symbol_context[:6]:
                location = f"{symbol.path}:{symbol.line_start}" if symbol.line_start else symbol.path
                scope = f" in {symbol.enclosing_scope}" if symbol.enclosing_scope else ""
                reason = f" ({symbol.reason})" if symbol.reason else ""
                symbol_lines.append(f"- {symbol.kind} {symbol.symbol}{scope} @ {location}{reason}")
            for item in dynamic_context.related_context[:6]:
                matched = f" matches {', '.join(item.matched_terms[:3])}" if item.matched_terms else ""
                reason = f" {item.reason}" if item.reason else ""
                path = f" [{item.path}]" if item.path else ""
                related_artifact_lines.append(f"- {item.artifact_type} {item.title}{path}:{reason}{matched}".rstrip(":"))
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
            f"Ticket context:\n{chr(10).join(ticket_lines) if ticket_lines else '- No linked ticket context recorded.'}\n\n"
            f"Threat model:\n{chr(10).join(threat_lines) if threat_lines else '- No threat model recorded yet.'}\n\n"
            f"Vulnerability findings:\n{chr(10).join(vulnerability_lines) if vulnerability_lines else '- No vulnerability findings recorded yet.'}\n\n"
            f"Browser context:\n{chr(10).join(browser_lines) if browser_lines else '- No browser dumps recorded yet.'}\n\n"
            f"Repo config:\n{chr(10).join(repo_config_lines) if repo_config_lines else '- No .xmustard config loaded.'}\n\n"
            f"Path-specific guidance:\n{chr(10).join(path_instruction_lines) if path_instruction_lines else '- No path-specific instructions matched the current issue paths.'}\n\n"
            f"Structural context:\n{chr(10).join(repo_dir_lines) if repo_dir_lines else '- No repo map available.'}\n\n"
            f"Ranked related paths:\n{related_lines}\n\n"
            f"Symbol context:\n{chr(10).join(symbol_lines) if symbol_lines else '- No symbol context ranked yet.'}\n\n"
            f"Related artifacts:\n{chr(10).join(related_artifact_lines) if related_artifact_lines else '- No related artifacts ranked yet.'}\n\n"
            f"Repository guidance:\n{chr(10).join(guidance_lines) if guidance_lines else '- No repository guidance files were found.'}\n\n"
            f"Known verification profiles:\n{chr(10).join(verification_lines) if verification_lines else '- No verification profiles configured yet.'}\n\n"
            f"Priority files:\n{focus_lines}\n\n"
            "Required workflow:\n"
            "1. Reproduce or validate the bug against the current code.\n"
            "2. Make the minimal safe fix.\n"
            "3. Add or update tests.\n"
            "4. Record exact files changed, tests run, and how the fix works back into the tracker.\n"
            "Return a concise engineering result, not a conversation."
        )

    def _apply_guidance_to_prompt(self, prompt: str, guidance: list[RepoGuidanceRecord]) -> str:
        if not guidance:
            return prompt
        lines = [f"- {item.path}: {item.summary or item.title}" for item in guidance[:self.GUIDANCE_LIMIT]]
        return f"Repository guidance to respect:\n{chr(10).join(lines)}\n\n{prompt}"

    def _collect_workspace_guidance(self, root: Path, workspace_id: str) -> list[RepoGuidanceRecord]:
        candidates: list[tuple[Path, str, bool, int]] = []
        root_resolved = root.resolve()
        for name, kind, always_on, priority in [
            ("AGENTS.md", "agent_instructions", True, 10),
            ("agents.md", "agent_instructions", True, 10),
            ("CLAUDE.md", "agent_instructions", True, 12),
            ("GEMINI.md", "agent_instructions", True, 12),
            (".openhands/microagents/repo.md", "agent_instructions", True, 14),
            ("CONVENTIONS.md", "conventions", True, 20),
            (".clinerules", "conventions", True, 22),
            (".devin/wiki.json", "repo_index", True, 25),
            ("README.md", "workspace_overview", False, 60),
        ]:
            candidate = root / name
            if candidate.exists() and candidate.is_file():
                candidates.append((candidate, kind, always_on, priority))
        for pattern, priority in [
            (".openhands/microagents/**/*.md", 28),
            (".openhands/skills/*.md", 30),
            (".openhands/skills/**/*.md", 30),
            (".agents/skills/*.md", 32),
            (".agents/skills/**/*.md", 32),
            (".cursor/rules/*.mdc", 34),
        ]:
            candidates.extend((path, "skill", False, priority) for path in sorted(root.glob(pattern)) if path.is_file())

        items: list[RepoGuidanceRecord] = []
        seen_paths: set[str] = set()
        for path, kind, always_on, priority in candidates:
            resolved_path = path.resolve()
            resolved_key = str(resolved_path).lower()
            if resolved_key in seen_paths:
                continue
            seen_paths.add(resolved_key)
            items.append(self._build_guidance_record(workspace_id, root_resolved, resolved_path, kind, always_on, priority))
        return sorted(items, key=lambda item: (item.priority, item.path.lower()))

    def _guidance_starter_specs(self, workspace: WorkspaceRecord) -> list[dict[str, object]]:
        root = Path(workspace.root_path)
        checks = self._suggest_workspace_checks(root)
        check_lines = "\n".join(f"- `{command}`" for command in checks) or f"- `{self.GUIDANCE_PLACEHOLDER_MARKER}: add the commands that prove a change is safe in this repo.`"
        starter_block = self.GUIDANCE_PLACEHOLDER_MARKER
        return [
            {
                "template_id": "agents",
                "title": "AGENTS.md",
                "path": "AGENTS.md",
                "description": "Always-on repo instructions for local agents and issue-context prompts.",
                "recommended": True,
                "content": (
                    f"# AGENTS.md instructions for {root}\n\n"
                    f"<!-- {self.GUIDANCE_STARTER_MARKER}:agents -->\n\n"
                    "# Repository Guide\n\n"
                    f"This repository contains `{workspace.name}`. Use this file to keep bug work issue-first, evidence-first, and repo-specific.\n\n"
                    "## Product Focus\n\n"
                    "- keep issue context grounded in concrete evidence\n"
                    "- prefer minimal safe fixes over broad refactors\n"
                    "- record verification evidence and changed files after each fix\n\n"
                    "## Preferred Checks\n\n"
                    f"{check_lines}\n\n"
                    "## Local Conventions\n\n"
                    f"- `{starter_block}: replace this line with the repo's most important code style or architecture rule.`\n"
                    f"- `{starter_block}: add any review or testing expectations that should always shape agent runs.`\n"
                ),
            },
            {
                "template_id": "openhands_repo",
                "title": "OpenHands repo microagent",
                "path": ".openhands/microagents/repo.md",
                "description": "Short repo-scoped microagent instructions for planning and execution flows.",
                "recommended": True,
                "content": (
                    "---\n"
                    "name: repo\n"
                    "type: repo\n"
                    "---\n\n"
                    f"<!-- {self.GUIDANCE_STARTER_MARKER}:openhands_repo -->\n\n"
                    f"# Repository instructions for {workspace.name}\n\n"
                    "- keep changes scoped to the current bug unless the evidence forces broader work\n"
                    "- verify the issue before and after editing when a safe check exists\n"
                    f"- `{starter_block}: add the repo-specific paths, services, or subsystems agents should inspect first.`\n"
                    f"- `{starter_block}: add any hard constraints for risky areas, external APIs, auth, or data handling.`\n"
                ),
            },
            {
                "template_id": "conventions",
                "title": "CONVENTIONS.md",
                "path": "CONVENTIONS.md",
                "description": "Shared engineering conventions for style, structure, and review defaults.",
                "recommended": False,
                "content": (
                    f"# Conventions\n\n"
                    f"<!-- {self.GUIDANCE_STARTER_MARKER}:conventions -->\n\n"
                    "## Code\n\n"
                    f"- `{starter_block}: describe the preferred module or component structure.`\n"
                    f"- `{starter_block}: describe any naming, typing, or test-location rule that matters in this repo.`\n\n"
                    "## Reviews\n\n"
                    "- keep backend and frontend contracts in sync when both sides change\n"
                    "- record verification steps alongside code changes\n"
                    f"- `{starter_block}: add the repo's review expectations for migration, security, or performance-sensitive work.`\n"
                ),
            },
        ]

    def _suggest_workspace_checks(self, root: Path) -> list[str]:
        commands: list[str] = []
        if (root / "backend").is_dir():
            commands.extend(
                [
                    "cd backend && pytest -q",
                    "cd backend && PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app",
                ]
            )
        if (root / "frontend").is_dir():
            commands.extend(
                [
                    "cd frontend && npm run lint",
                    "cd frontend && npm run build",
                ]
            )
        if (root / "api-go").is_dir():
            commands.extend(
                [
                    "cd api-go && go test ./...",
                    "cd api-go && go build ./cmd/xmustard-api",
                ]
            )
        if (root / "rust-core").is_dir():
            commands.append("cd rust-core && cargo test")
        deduped: list[str] = []
        for command in commands:
            if command not in deduped:
                deduped.append(command)
        return deduped

    def _guidance_file_is_stale(self, path: Path) -> bool:
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            return False
        return self.GUIDANCE_PLACEHOLDER_MARKER in text

    def _build_guidance_record(
        self,
        workspace_id: str,
        root: Path,
        path: Path,
        kind: str,
        always_on: bool,
        priority: int,
    ) -> RepoGuidanceRecord:
        relative_path = str(path.relative_to(root))
        text = path.read_text(encoding="utf-8", errors="ignore")
        summary, excerpt, trigger_keywords = self._summarize_guidance_text(path, kind, text)
        title = self._guidance_title_from_text(path, text)
        guidance_id = f"guide_{hashlib.sha1(f'{workspace_id}:{relative_path}'.encode('utf-8')).hexdigest()[:12]}"
        updated_at = utc_now()
        try:
            updated_at = __import__("datetime").datetime.fromtimestamp(path.stat().st_mtime, tz=__import__("datetime").timezone.utc).isoformat()
        except OSError:
            pass
        return RepoGuidanceRecord(
            guidance_id=guidance_id,
            workspace_id=workspace_id,
            kind=kind,
            title=title,
            path=relative_path,
            always_on=always_on,
            priority=priority,
            summary=summary,
            excerpt=excerpt,
            trigger_keywords=trigger_keywords,
            updated_at=updated_at,
        )

    def _guidance_title_from_text(self, path: Path, text: str) -> str:
        for raw_line in text.splitlines():
            line = raw_line.strip()
            if line.startswith("#"):
                return line.lstrip("#").strip()
        return path.name

    def _summarize_guidance_text(self, path: Path, kind: str, text: str) -> tuple[str, Optional[str], list[str]]:
        if path.suffix == ".json":
            return self._summarize_guidance_json(text)

        summary_lines: list[str] = []
        trigger_keywords: list[str] = []
        in_frontmatter = False
        seen_frontmatter = False
        in_code_fence = False

        for index, raw_line in enumerate(text.splitlines()):
            line = raw_line.strip()
            if not line:
                continue
            if line == "---" and index == 0:
                in_frontmatter = True
                seen_frontmatter = True
                continue
            if in_frontmatter and line == "---":
                in_frontmatter = False
                continue
            if line.startswith("```"):
                in_code_fence = not in_code_fence
                continue
            if in_frontmatter:
                if line.startswith("keywords:"):
                    trigger_keywords.extend(self._extract_inline_keywords(line))
                continue
            if in_code_fence:
                continue
            if line.startswith("#"):
                continue
            normalized = line.lstrip("-*").strip()
            if re.match(r"^\d+\.", normalized):
                normalized = re.sub(r"^\d+\.\s*", "", normalized)
            if normalized and normalized not in summary_lines:
                summary_lines.append(normalized)
            if len(summary_lines) >= 4:
                break

        if kind == "skill" and seen_frontmatter and not trigger_keywords:
            trigger_keywords = self._extract_keyword_block(text)
        excerpt = "\n".join(summary_lines[:4])[:600] or None
        summary = " ".join(summary_lines[:3])[:280] if summary_lines else f"Repository guidance from {path.name}"
        return summary, excerpt, trigger_keywords[:8]

    def _summarize_guidance_json(self, text: str) -> tuple[str, Optional[str], list[str]]:
        try:
            payload = json.loads(text)
        except json.JSONDecodeError:
            compact = " ".join(line.strip() for line in text.splitlines() if line.strip())[:280]
            return compact or "Repository index configuration", None, []
        if not isinstance(payload, dict):
            compact = json.dumps(payload)[:280]
            return compact or "Repository index configuration", compact[:600] if compact else None, []

        include = payload.get("include") or payload.get("includes") or payload.get("include_paths") or []
        exclude = payload.get("exclude") or payload.get("excludes") or payload.get("exclude_paths") or []
        summary_parts: list[str] = []
        if payload.get("description"):
            summary_parts.append(str(payload["description"]))
        if isinstance(include, list) and include:
            summary_parts.append(f"Includes {', '.join(str(item) for item in include[:4])}")
        if isinstance(exclude, list) and exclude:
            summary_parts.append(f"Excludes {', '.join(str(item) for item in exclude[:4])}")
        excerpt = json.dumps(payload, indent=2)[:600]
        return ". ".join(summary_parts)[:280] or "Repository index configuration", excerpt, []

    def _extract_inline_keywords(self, line: str) -> list[str]:
        match = re.search(r"\[(.*?)\]", line)
        if not match:
            return []
        return [item.strip().strip("'\"") for item in match.group(1).split(",") if item.strip()]

    def _extract_keyword_block(self, text: str) -> list[str]:
        match = re.search(r"keywords:\s*\[(.*?)\]", text, flags=re.IGNORECASE | re.DOTALL)
        if not match:
            return []
        return [item.strip().strip("'\"") for item in match.group(1).split(",") if item.strip()]

    def _dedupe_text(self, items: list[str]) -> list[str]:
        seen: set[str] = set()
        ordered: list[str] = []
        for item in items:
            normalized = item.strip()
            if not normalized or normalized in seen:
                continue
            seen.add(normalized)
            ordered.append(normalized)
        return ordered[:6]

    def _context_tokens(
        self,
        issue: IssueRecord,
        ticket_contexts: list[TicketContextRecord],
        vulnerability_findings: Optional[list[VulnerabilityFindingRecord]] = None,
    ) -> list[str]:
        text_parts = [
            issue.title,
            issue.summary or "",
            issue.impact or "",
            " ".join(issue.labels),
        ]
        for context in ticket_contexts[:4]:
            text_parts.extend(
                [
                    context.title,
                    context.summary,
                    " ".join(context.labels),
                    " ".join(context.acceptance_criteria),
                ]
            )
        for finding in (vulnerability_findings or [])[:4]:
            text_parts.extend(
                [
                    finding.title,
                    finding.summary,
                    finding.scanner,
                    finding.rule_id or "",
                    " ".join(finding.cwe_ids),
                    " ".join(finding.cve_ids),
                    finding.location_path or "",
                    " ".join(finding.evidence),
                ]
            )
        tokens = re.findall(r"[a-z0-9_./-]{3,}", " ".join(text_parts).lower())
        stop_words = {"the", "and", "for", "with", "that", "this", "from", "issue", "bug", "current", "branch"}
        ordered: list[str] = []
        for token in tokens:
            if token in stop_words or token in ordered:
                continue
            ordered.append(token)
        return ordered[:24]

    def _rank_related_paths(
        self,
        issue: IssueRecord,
        tree_focus: list[str],
        ticket_contexts: list[TicketContextRecord],
        vulnerability_findings: list[VulnerabilityFindingRecord],
        repo_map: RepoMapSummary,
    ) -> list[str]:
        candidates = list(tree_focus)
        candidates.extend(item.path for item in repo_map.key_files)
        candidates.extend(item.path for item in repo_map.top_directories)
        for finding in vulnerability_findings[:6]:
            if finding.location_path:
                candidates.append(finding.location_path)
        tokens = self._context_tokens(issue, ticket_contexts, vulnerability_findings)
        scored: dict[str, int] = {}
        for path in candidates:
            if not path:
                continue
            score = 0
            lowered = path.lower()
            if path in tree_focus:
                score += 6
            for token in tokens:
                if token in lowered:
                    score += 2
            if lowered.endswith((".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java")):
                score += 1
            if "test" in lowered:
                score += 1
            if score > 0:
                scored[path] = max(scored.get(path, 0), score)
        ordered = [path for path, _ in sorted(scored.items(), key=lambda item: (-item[1], item[0]))]
        return ordered[:8]

    def _build_dynamic_context(
        self,
        workspace: WorkspaceRecord,
        issue: IssueRecord,
        tree_focus: list[str],
        ticket_contexts: list[TicketContextRecord],
        threat_models: list[ThreatModelRecord],
        browser_dumps: list[BrowserDumpRecord],
        vulnerability_findings: list[VulnerabilityFindingRecord],
        recent_fixes: list[FixRecord],
        recent_activity: list[ActivityRecord],
        related_paths: list[str],
    ) -> DynamicContextBundle:
        tokens = self._context_tokens(issue, ticket_contexts, vulnerability_findings)
        symbol_context = self._extract_symbol_context(Path(workspace.root_path), [*tree_focus, *related_paths], tokens)
        related_context = self._rank_related_artifacts(
            issue,
            ticket_contexts,
            threat_models,
            browser_dumps,
            vulnerability_findings,
            recent_fixes,
            recent_activity,
            tokens,
        )
        return DynamicContextBundle(
            symbol_context=symbol_context,
            related_context=related_context,
        )

    def _extract_symbol_context(self, root: Path, candidate_paths: list[str], tokens: list[str]) -> list[RepoMapSymbolRecord]:
        symbols: list[RepoMapSymbolRecord] = []
        seen: set[tuple[str, str]] = set()
        for path in self._dedupe_text(candidate_paths)[:10]:
            file_path = root / path
            if not file_path.is_file():
                continue
            if file_path.suffix.lower() not in {".py", ".ts", ".tsx", ".js", ".jsx", ".go", ".rs", ".java"}:
                continue
            try:
                lines = file_path.read_text(encoding="utf-8").splitlines()
            except OSError:
                continue
            current_scope: Optional[str] = None
            for index, raw_line in enumerate(lines, start=1):
                line = raw_line.strip()
                kind = None
                symbol_name = None
                if match := re.match(r"class\s+([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "class"
                    symbol_name = match.group(1)
                    current_scope = symbol_name
                elif match := re.match(r"def\s+([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "method" if current_scope else "function"
                    symbol_name = match.group(1)
                elif match := re.match(r"func\s+(?:\([^)]+\)\s*)?([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "method" if "(" in line.split("{", 1)[0] else "function"
                    symbol_name = match.group(1)
                elif match := re.match(r"fn\s+([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "function"
                    symbol_name = match.group(1)
                elif match := re.match(r"(?:export\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "function"
                    symbol_name = match.group(1)
                elif match := re.match(r"(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s*)?\(", line):
                    kind = "function"
                    symbol_name = match.group(1)
                elif match := re.match(r"struct\s+([A-Za-z_][A-Za-z0-9_]*)", line):
                    kind = "type"
                    symbol_name = match.group(1)
                    current_scope = symbol_name
                if not symbol_name or not kind:
                    continue
                symbol_key = (path, symbol_name)
                if symbol_key in seen:
                    continue
                lowered = f"{path.lower()} {symbol_name.lower()}"
                matched_terms = [token for token in tokens[:12] if token in lowered]
                score = len(matched_terms) * 2
                if path in candidate_paths[:4]:
                    score += 2
                if score <= 0:
                    continue
                seen.add(symbol_key)
                symbols.append(
                    RepoMapSymbolRecord(
                        path=path,
                        symbol=symbol_name,
                        kind=kind,
                        line_start=index,
                        enclosing_scope=current_scope if current_scope != symbol_name else None,
                        reason=f"Matches {', '.join(matched_terms[:3])}" if matched_terms else "Near ranked focus files.",
                        score=score,
                    )
                )
        symbols.sort(key=lambda item: (-item.score, item.path, item.symbol))
        return symbols[:8]

    def _rank_related_artifacts(
        self,
        issue: IssueRecord,
        ticket_contexts: list[TicketContextRecord],
        threat_models: list[ThreatModelRecord],
        browser_dumps: list[BrowserDumpRecord],
        vulnerability_findings: list[VulnerabilityFindingRecord],
        recent_fixes: list[FixRecord],
        recent_activity: list[ActivityRecord],
        tokens: list[str],
    ) -> list[RelatedContextRecord]:
        records: list[RelatedContextRecord] = []
        issue_terms = set(tokens[:12])

        def matched_terms(*parts: str) -> list[str]:
            haystack = " ".join(parts).lower()
            return [token for token in issue_terms if token in haystack][:4]

        for item in ticket_contexts[:4]:
            matches = matched_terms(item.title, item.summary, " ".join(item.acceptance_criteria))
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="ticket_context",
                        artifact_id=item.context_id,
                        title=item.title,
                        path="ticket_contexts.json",
                        reason="Shares acceptance-criteria or ticket language with the current issue.",
                        matched_terms=matches,
                        score=len(matches) * 2 + 2,
                    )
                )
        for item in threat_models[:3]:
            matches = matched_terms(item.title, item.summary, " ".join(item.assets), " ".join(item.abuse_cases))
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="threat_model",
                        artifact_id=item.threat_model_id,
                        title=item.title,
                        path="threat_models.json",
                        reason="Touches the same assets or abuse language as the issue context.",
                        matched_terms=matches,
                        score=len(matches) * 2 + 1,
                    )
                )
        for item in browser_dumps[:3]:
            matches = matched_terms(item.label, item.summary, item.page_title or "", item.page_url or "", item.dom_snapshot[:240])
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="browser_dump",
                        artifact_id=item.dump_id,
                        title=item.label,
                        path="browser_dumps.json",
                        reason="Captures a browser-state repro that overlaps with the current issue terms.",
                        matched_terms=matches,
                        score=len(matches) * 2 + 1,
                    )
                )
        for item in vulnerability_findings[:4]:
            matches = matched_terms(
                item.title,
                item.summary,
                item.scanner,
                item.rule_id or "",
                item.location_path or "",
                " ".join(item.cwe_ids),
                " ".join(item.cve_ids),
                " ".join(item.evidence),
            )
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="vulnerability_finding",
                        artifact_id=item.finding_id,
                        title=item.title,
                        path="vulnerability_findings.json",
                        reason="Scanner evidence overlaps with the current issue language or files.",
                        matched_terms=matches,
                        score=len(matches) * 2 + 2,
                    )
                )
        for item in recent_fixes[:4]:
            matches = matched_terms(item.summary, item.how or "", " ".join(item.changed_files))
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="fix_record",
                        artifact_id=item.fix_id,
                        title=item.summary,
                        path="fix_records.json",
                        reason="Previous fix history overlaps with this issue's files or terms.",
                        matched_terms=matches,
                        score=len(matches) * 2 + 1,
                    )
                )
        for item in recent_activity[:4]:
            matches = matched_terms(item.summary)
            if matches:
                records.append(
                    RelatedContextRecord(
                        artifact_type="activity",
                        artifact_id=item.activity_id,
                        title=item.summary,
                        path="activity.jsonl",
                        reason="Recent issue activity mentions the same issue language.",
                        matched_terms=matches,
                        score=len(matches) * 2,
                    )
                )
        records.sort(key=lambda item: (-item.score, item.artifact_type, item.title.lower()))
        return records[:8]

    def _build_acceptance_review(self, packet: IssueContextPacket, run: RunRecord, text: str) -> AcceptanceCriteriaReview:
        criteria = []
        for context in packet.ticket_contexts[:4]:
            criteria.extend(context.acceptance_criteria[:4])
        criteria = self._normalize_text_list(criteria, limit=12)
        if not criteria:
            return AcceptanceCriteriaReview(
                status="unknown",
                criteria=[],
                notes=["No ticket acceptance criteria are linked to this issue."],
            )
        corpus = " ".join(
            [
                run.title,
                run.prompt,
                text,
                " ".join(packet.issue.tests_passed),
                packet.issue.summary or "",
                packet.issue.impact or "",
            ]
        ).lower()
        matched: list[str] = []
        missing: list[str] = []
        for criterion in criteria:
            criterion_tokens = [
                token for token in re.findall(r"[a-z0-9_./-]{3,}", criterion.lower())
                if token not in {"the", "and", "for", "with", "that", "this", "from"}
            ]
            matched_count = sum(1 for token in criterion_tokens[:6] if token in corpus)
            threshold = max(1, min(2, len(criterion_tokens)))
            if matched_count >= threshold:
                matched.append(criterion)
            else:
                missing.append(criterion)
        status = "met" if matched and not missing else "partial" if matched else "not_met"
        notes = []
        if packet.issue.tests_passed:
            notes.append(f"Tracker already records {len(packet.issue.tests_passed)} passing test command(s).")
        if packet.issue.verification_evidence:
            notes.append(f"Tracker already carries {len(packet.issue.verification_evidence)} verification evidence reference(s).")
        return AcceptanceCriteriaReview(
            status=status,
            criteria=criteria,
            matched=matched,
            missing=missing,
            notes=notes[:4],
        )

    def _build_scope_warnings(self, packet: IssueContextPacket, run: RunRecord) -> list[ScopeWarning]:
        warnings: list[ScopeWarning] = []
        related_paths = packet.related_paths or packet.tree_focus
        dirty_paths = list(run.worktree.dirty_paths) if run.worktree and run.worktree.dirty_paths else []
        unrelated = [
            path for path in dirty_paths
            if not self._matches_related_path(path, related_paths)
        ]
        if unrelated:
            warnings.append(
                ScopeWarning(
                    kind="unrelated_change",
                    message=f"{len(unrelated)} worktree path(s) do not match the issue's ranked focus.",
                    paths=unrelated[:8],
                    severity="high" if len(unrelated) > 3 else "medium",
                )
            )
        drift_flags = [flag for flag in packet.issue.drift_flags if "review" not in flag.lower()]
        if drift_flags:
            warnings.append(
                ScopeWarning(
                    kind="scope_drift",
                    message=f"Issue drift flags remain open: {', '.join(drift_flags[:3])}.",
                    paths=related_paths[:6],
                    severity="medium",
                )
            )
        return warnings

    def _matches_related_path(self, candidate: str, related_paths: list[str]) -> bool:
        lowered = candidate.lower().strip()
        for path in related_paths[:12]:
            expected = path.lower().strip()
            if not expected:
                continue
            if lowered == expected or lowered.startswith(expected + "/") or expected.startswith(lowered + "/"):
                return True
            if Path(lowered).name == Path(expected).name:
                return True
        return False

    def _build_eval_scenario_report(self, workspace_id: str, scenario: EvalScenarioRecord) -> EvalScenarioReport:
        replays = self.list_issue_context_replays(workspace_id, issue_id=scenario.issue_id)
        baseline_replay = next((item for item in replays if item.replay_id == scenario.baseline_replay_id), None)
        latest_replay_comparison = None
        if baseline_replay is not None:
            latest_replay_comparison = self.compare_issue_context_replay(workspace_id, scenario.issue_id, baseline_replay.replay_id)
        packet = self.build_issue_context(workspace_id, scenario.issue_id)
        variant_diff = self._build_eval_variant_diff(scenario, packet)
        verification_reports = self.list_verification_profile_reports(workspace_id, issue_id=scenario.issue_id)
        if scenario.verification_profile_ids:
            verification_reports = [
                item for item in verification_reports if item.profile_id in set(scenario.verification_profile_ids)
            ]
        selected_runs = self._select_eval_runs(workspace_id, scenario)
        run_metrics = self._load_eval_run_metrics(selected_runs)
        latest_fresh_run = self._build_eval_fresh_run_summary(scenario, selected_runs, run_metrics)
        success_runs = sum(1 for run in selected_runs if run.status == "completed")
        failed_runs = sum(1 for run in selected_runs if run.status == "failed")
        total_cost = round(sum(item.estimated_cost for item in run_metrics), 4)
        avg_duration = int(sum(item.duration_ms for item in run_metrics) / len(run_metrics)) if run_metrics else 0
        verification_total = sum(item.total_runs for item in verification_reports)
        verification_success = sum(item.success_runs for item in verification_reports)
        summary_parts = [
            f"{success_runs} successful run(s)",
            f"{failed_runs} failed run(s)",
            f"{len(verification_reports)} verification profile report(s)",
        ]
        if latest_replay_comparison:
            summary_parts.append(latest_replay_comparison.summary)
        return EvalScenarioReport(
            scenario=scenario,
            baseline_replay=baseline_replay,
            latest_replay_comparison=latest_replay_comparison,
            variant_diff=variant_diff,
            verification_profile_reports=verification_reports,
            latest_fresh_run=latest_fresh_run,
            run_metrics=run_metrics,
            total_estimated_cost=total_cost,
            avg_duration_ms=avg_duration,
            success_runs=success_runs,
            failed_runs=failed_runs,
            verification_success_rate=round((verification_success / verification_total) * 100, 1) if verification_total else 0.0,
            summary="; ".join([*summary_parts, *( [variant_diff.summary] if variant_diff and variant_diff.summary else [] )]),
        )

    def _attach_eval_baseline_comparisons(
        self,
        workspace_id: str,
        reports: list[EvalScenarioReport],
    ) -> list[EvalScenarioReport]:
        baselines: dict[str, EvalScenarioReport] = {}
        for report in reports:
            current = baselines.get(report.scenario.issue_id)
            if current is None or self._eval_baseline_sort_key(report) < self._eval_baseline_sort_key(current):
                baselines[report.scenario.issue_id] = report
        return [
            report.model_copy(
                update={
                    "comparison_to_baseline": self._build_eval_scenario_baseline_comparison(
                        workspace_id,
                        report,
                        baselines.get(report.scenario.issue_id),
                    )
                }
            )
            for report in reports
        ]

    def _attach_eval_fresh_comparisons(self, reports: list[EvalScenarioReport]) -> list[EvalScenarioReport]:
        baselines: dict[str, EvalScenarioReport] = {}
        for report in reports:
            current = baselines.get(report.scenario.issue_id)
            if current is None or self._eval_baseline_sort_key(report) < self._eval_baseline_sort_key(current):
                baselines[report.scenario.issue_id] = report
        return [
            report.model_copy(
                update={
                    "fresh_comparison_to_baseline": self._build_eval_fresh_execution_comparison(
                        report,
                        baselines.get(report.scenario.issue_id),
                    )
                }
            )
            for report in reports
        ]

    def _eval_baseline_sort_key(self, report: EvalScenarioReport) -> tuple[int, str, str]:
        return (
            0 if report.scenario.baseline_replay_id else 1,
            report.scenario.created_at,
            report.scenario.scenario_id,
        )

    def _eval_fresh_status_rank(self, status: str) -> int:
        return {
            "completed": 4,
            "running": 3,
            "queued": 2,
            "planning": 1,
            "failed": 0,
            "cancelled": -1,
        }.get(status, -1)

    def _build_eval_fresh_run_summary(
        self,
        scenario: EvalScenarioRecord,
        runs: list[RunRecord],
        metrics: list[RunMetrics],
    ) -> Optional[EvalFreshRunSummary]:
        summaries = self._build_eval_fresh_run_summaries(scenario, runs, metrics)
        return summaries[0] if summaries else None

    def _build_eval_fresh_run_summaries(
        self,
        scenario: EvalScenarioRecord,
        runs: list[RunRecord],
        metrics: list[RunMetrics],
    ) -> list[EvalFreshRunSummary]:
        if not runs:
            return []
        metric_lookup = {item.run_id: item for item in metrics}
        ordered_runs = sorted(runs, key=lambda item: (item.created_at, item.run_id), reverse=True)
        return [
            EvalFreshRunSummary(
                scenario_id=scenario.scenario_id,
                scenario_name=scenario.name,
                run_id=run.run_id,
                status=run.status,
                runtime=run.runtime,
                model=run.model,
                created_at=run.created_at,
                estimated_cost=metric_lookup[run.run_id].estimated_cost if run.run_id in metric_lookup else 0.0,
                duration_ms=metric_lookup[run.run_id].duration_ms if run.run_id in metric_lookup else 0,
                command_preview=run.command_preview,
                planning=run.status == "planning",
            )
            for run in ordered_runs
        ]

    def _build_eval_fresh_execution_comparison(
        self,
        report: EvalScenarioReport,
        baseline: Optional[EvalScenarioReport],
    ) -> Optional[EvalFreshExecutionComparison]:
        if baseline is None or baseline.scenario.scenario_id == report.scenario.scenario_id:
            return None
        if report.latest_fresh_run is None or baseline.latest_fresh_run is None:
            return None
        scenario_rank = self._eval_fresh_status_rank(report.latest_fresh_run.status)
        baseline_rank = self._eval_fresh_status_rank(baseline.latest_fresh_run.status)
        cost_delta = round(report.latest_fresh_run.estimated_cost - baseline.latest_fresh_run.estimated_cost, 4)
        duration_delta = report.latest_fresh_run.duration_ms - baseline.latest_fresh_run.duration_ms
        preferred = "tie"
        preferred_scenario_id: Optional[str] = None
        preferred_scenario_name: Optional[str] = None
        preference_reasons: list[str] = []
        if scenario_rank > baseline_rank:
            preferred = "scenario"
            preferred_scenario_id = report.scenario.scenario_id
            preferred_scenario_name = report.scenario.name
            preference_reasons.append("better fresh run status")
        elif scenario_rank < baseline_rank:
            preferred = "baseline"
            preferred_scenario_id = baseline.scenario.scenario_id
            preferred_scenario_name = baseline.scenario.name
            preference_reasons.append("better fresh run status")
        else:
            if cost_delta < 0:
                preferred = "scenario"
                preferred_scenario_id = report.scenario.scenario_id
                preferred_scenario_name = report.scenario.name
                preference_reasons.append("lower fresh run cost")
            elif cost_delta > 0:
                preferred = "baseline"
                preferred_scenario_id = baseline.scenario.scenario_id
                preferred_scenario_name = baseline.scenario.name
                preference_reasons.append("lower fresh run cost")
            if duration_delta < 0:
                if preferred == "tie":
                    preferred = "scenario"
                    preferred_scenario_id = report.scenario.scenario_id
                    preferred_scenario_name = report.scenario.name
                preference_reasons.append("faster fresh run")
            elif duration_delta > 0:
                if preferred == "tie":
                    preferred = "baseline"
                    preferred_scenario_id = baseline.scenario.scenario_id
                    preferred_scenario_name = baseline.scenario.name
                if preferred == "baseline":
                    preference_reasons.append("faster fresh run")
        summary_parts = [
            f"Latest fresh run vs {baseline.scenario.name}",
            f"status {report.latest_fresh_run.status} vs {baseline.latest_fresh_run.status}",
        ]
        if cost_delta:
            summary_parts.append(f"{cost_delta:+.4f} cost")
        if duration_delta:
            summary_parts.append(f"{duration_delta:+d}ms duration")
        if preferred_scenario_name:
            summary_parts.append(f"preferred: {preferred_scenario_name}")
        return EvalFreshExecutionComparison(
            compared_to_scenario_id=baseline.scenario.scenario_id,
            compared_to_name=baseline.scenario.name,
            scenario_status=report.latest_fresh_run.status,
            baseline_status=baseline.latest_fresh_run.status,
            estimated_cost_delta=cost_delta,
            duration_ms_delta=duration_delta,
            preferred=preferred,
            preferred_scenario_id=preferred_scenario_id,
            preferred_scenario_name=preferred_scenario_name,
            preference_reasons=preference_reasons,
            summary="; ".join(summary_parts),
        )

    def _build_eval_fresh_replay_rankings(
        self,
        reports: list[EvalScenarioReport],
    ) -> list[EvalFreshReplayRanking]:
        grouped: dict[str, list[EvalScenarioReport]] = {}
        for report in reports:
            if report.latest_fresh_run is None:
                continue
            grouped.setdefault(report.scenario.issue_id, []).append(report)
        rankings: list[EvalFreshReplayRanking] = []
        for issue_id in sorted(grouped):
            fresh_reports = grouped[issue_id]
            if len(fresh_reports) < 2:
                continue
            baseline = min(fresh_reports, key=self._eval_baseline_sort_key)
            scored_entries = []
            for report in fresh_reports:
                wins = 0
                losses = 0
                ties = 0
                reasons: list[str] = []
                for other in fresh_reports:
                    if other.scenario.scenario_id == report.scenario.scenario_id:
                        continue
                    comparison = self._build_eval_fresh_execution_comparison(report, other)
                    if comparison is None:
                        continue
                    if comparison.preferred == "scenario":
                        wins += 1
                        reasons.extend(comparison.preference_reasons)
                    elif comparison.preferred == "baseline":
                        losses += 1
                    else:
                        ties += 1
                latest = report.latest_fresh_run
                assert latest is not None
                deduped_reasons = list(dict.fromkeys(reasons))
                scored_entries.append(
                    (
                        report,
                        wins,
                        losses,
                        ties,
                        deduped_reasons,
                        (
                            -wins,
                            losses,
                            -self._eval_fresh_status_rank(latest.status),
                            latest.estimated_cost,
                            latest.duration_ms,
                            report.scenario.created_at,
                            report.scenario.scenario_id,
                        ),
                    )
                )
            scored_entries.sort(key=lambda item: item[5])
            ranked_scenarios: list[EvalFreshReplayRankingEntry] = []
            for rank, (report, wins, losses, ties, reasons, _) in enumerate(scored_entries, start=1):
                latest = report.latest_fresh_run
                assert latest is not None
                summary_parts = [
                    f"{wins} pairwise win(s)",
                    f"{losses} loss(es)",
                    f"{ties} tie(s)",
                    f"status {latest.status}",
                    f"${latest.estimated_cost:.4f} estimated cost",
                    f"{latest.duration_ms}ms duration",
                ]
                if reasons:
                    summary_parts.append(f"reasons: {', '.join(reasons[:3])}")
                ranked_scenarios.append(
                    EvalFreshReplayRankingEntry(
                        rank=rank,
                        scenario_id=report.scenario.scenario_id,
                        scenario_name=report.scenario.name,
                        latest_fresh_run=latest,
                        pairwise_wins=wins,
                        pairwise_losses=losses,
                        pairwise_ties=ties,
                        preference_reasons=reasons,
                        summary="; ".join(summary_parts),
                    )
                )
            top = ranked_scenarios[0]
            ranking_summary = (
                f"Top fresh replay: {top.scenario_name} ranked 1/{len(ranked_scenarios)} "
                f"with {top.pairwise_wins} pairwise win(s)"
            )
            if baseline.scenario.scenario_id != top.scenario_id:
                ranking_summary += f"; baseline remains {baseline.scenario.name}"
            rankings.append(
                EvalFreshReplayRanking(
                    issue_id=issue_id,
                    baseline_scenario_id=baseline.scenario.scenario_id,
                    baseline_scenario_name=baseline.scenario.name,
                    ranked_scenarios=ranked_scenarios,
                    summary=ranking_summary,
                )
            )
        return rankings

    def _build_eval_fresh_replay_trends(
        self,
        workspace_id: str,
        scenarios: list[EvalScenarioRecord],
        reports: list[EvalScenarioReport],
        rankings: list[EvalFreshReplayRanking],
        replay_batches: list[EvalReplayBatchRecord],
    ) -> list[EvalFreshReplayTrend]:
        issue_batches: dict[str, list[EvalReplayBatchRecord]] = {}
        for batch in replay_batches:
            issue_batches.setdefault(batch.issue_id, []).append(batch)
        trends: list[EvalFreshReplayTrend] = []
        batch_driven_issues: set[str] = set()
        for issue_id, batches in issue_batches.items():
            ordered_batches = sorted(batches, key=lambda item: item.created_at, reverse=True)
            if len(ordered_batches) < 2:
                continue
            latest_batch = ordered_batches[0]
            previous_batch = ordered_batches[1]
            latest_ranking = self._build_eval_batch_ranking(workspace_id, issue_id, latest_batch, reports)
            previous_ranking = self._build_eval_batch_ranking(workspace_id, issue_id, previous_batch, reports)
            if latest_ranking is None or previous_ranking is None:
                continue
            previous_entries = {entry.scenario_id: entry for entry in previous_ranking.ranked_scenarios}
            entries: list[EvalFreshReplayTrendEntry] = []
            moved_count = 0
            for entry in latest_ranking.ranked_scenarios:
                previous_entry = previous_entries.get(entry.scenario_id)
                previous_rank = previous_entry.rank if previous_entry else None
                if previous_rank is None:
                    movement = "new"
                elif entry.rank < previous_rank:
                    movement = "up"
                elif entry.rank > previous_rank:
                    movement = "down"
                else:
                    movement = "same"
                if movement in {"up", "down"}:
                    moved_count += 1
                entries.append(
                    EvalFreshReplayTrendEntry(
                        scenario_id=entry.scenario_id,
                        scenario_name=entry.scenario_name,
                        current_rank=entry.rank,
                        previous_rank=previous_rank,
                        movement=movement,
                        latest_fresh_run=entry.latest_fresh_run,
                        previous_fresh_run=previous_entry.latest_fresh_run if previous_entry else None,
                        summary="; ".join(
                            [
                                f"current rank {entry.rank}",
                                *( [f"previous rank {previous_rank}"] if previous_rank is not None else [] ),
                                f"movement {movement}",
                                f"latest batch {latest_batch.batch_id}",
                                f"previous batch {previous_batch.batch_id}",
                            ]
                        ),
                    )
                )
            if entries:
                batch_driven_issues.add(issue_id)
                trends.append(
                    EvalFreshReplayTrend(
                        issue_id=issue_id,
                        latest_batch_id=latest_batch.batch_id,
                        previous_batch_id=previous_batch.batch_id,
                        entries=entries,
                        summary=(
                            f"{moved_count} scenario(s) changed rank between replay batches {previous_batch.batch_id} and {latest_batch.batch_id}"
                            if moved_count
                            else f"Replay batch ranks are unchanged between {previous_batch.batch_id} and {latest_batch.batch_id}"
                        ),
                    )
                )

        report_lookup = {item.scenario.scenario_id: item for item in reports}
        previous_reports: list[EvalScenarioReport] = []
        previous_run_lookup: dict[str, EvalFreshRunSummary] = {}
        for scenario in scenarios:
            report = report_lookup.get(scenario.scenario_id)
            if report is None:
                continue
            runs = self._select_eval_runs(workspace_id, scenario)
            metrics = self._load_eval_run_metrics(runs)
            fresh_runs = self._build_eval_fresh_run_summaries(scenario, runs, metrics)
            if len(fresh_runs) < 2:
                continue
            previous_run_lookup[scenario.scenario_id] = fresh_runs[1]
            previous_reports.append(report.model_copy(update={"latest_fresh_run": fresh_runs[1]}))
        previous_rankings = self._build_eval_fresh_replay_rankings(previous_reports)
        previous_lookup = {
            ranking.issue_id: {entry.scenario_id: entry for entry in ranking.ranked_scenarios}
            for ranking in previous_rankings
        }
        for ranking in rankings:
            if ranking.issue_id in batch_driven_issues:
                continue
            previous_entries = previous_lookup.get(ranking.issue_id, {})
            if not previous_entries:
                continue
            entries: list[EvalFreshReplayTrendEntry] = []
            for entry in ranking.ranked_scenarios:
                previous_entry = previous_entries.get(entry.scenario_id)
                previous_rank = previous_entry.rank if previous_entry else None
                if previous_rank is None:
                    movement = "new"
                elif entry.rank < previous_rank:
                    movement = "up"
                elif entry.rank > previous_rank:
                    movement = "down"
                else:
                    movement = "same"
                summary_parts = [f"current rank {entry.rank}"]
                if previous_rank is not None:
                    summary_parts.append(f"previous rank {previous_rank}")
                summary_parts.append(f"movement {movement}")
                entries.append(
                    EvalFreshReplayTrendEntry(
                        scenario_id=entry.scenario_id,
                        scenario_name=entry.scenario_name,
                        current_rank=entry.rank,
                        previous_rank=previous_rank,
                        movement=movement,
                        latest_fresh_run=entry.latest_fresh_run,
                        previous_fresh_run=previous_run_lookup.get(entry.scenario_id),
                        summary="; ".join(summary_parts),
                    )
                )
            if not entries:
                continue
            moved = [item for item in entries if item.movement in {"up", "down"}]
            summary = (
                f"{len(moved)} scenario(s) changed rank since the previous fresh replay snapshot"
                if moved else
                "Fresh replay ranks are unchanged from the previous snapshot"
            )
            trends.append(
                EvalFreshReplayTrend(
                    issue_id=ranking.issue_id,
                    latest_batch_id=None,
                    previous_batch_id=None,
                    entries=entries,
                    summary=summary,
                )
            )
        return trends

    def _build_eval_batch_ranking(
        self,
        workspace_id: str,
        issue_id: str,
        batch: EvalReplayBatchRecord,
        reports: list[EvalScenarioReport],
    ) -> Optional[EvalFreshReplayRanking]:
        report_lookup = {
            item.scenario.scenario_id: item for item in reports if item.scenario.issue_id == issue_id
        }
        runs = {item.run_id: item for item in self.store.list_runs(workspace_id)}
        metric_lookup = {
            run_id: self.runtime_service.load_run_metrics(run_id)
            for run_id in batch.queued_run_ids
        }
        batch_reports: list[EvalScenarioReport] = []
        for scenario_id in batch.scenario_ids:
            report = report_lookup.get(scenario_id)
            run = next(
                (
                    runs[run_id]
                    for run_id in batch.queued_run_ids
                    if run_id in runs and runs[run_id].eval_scenario_id == scenario_id
                ),
                None,
            )
            if report is None or run is None:
                continue
            metric = metric_lookup.get(run.run_id)
            latest = EvalFreshRunSummary(
                scenario_id=scenario_id,
                scenario_name=report.scenario.name,
                run_id=run.run_id,
                status=run.status,
                runtime=run.runtime,
                model=run.model,
                created_at=run.created_at,
                estimated_cost=metric.estimated_cost if metric else 0.0,
                duration_ms=metric.duration_ms if metric else 0,
                command_preview=run.command_preview,
                planning=run.status == "planning",
            )
            batch_reports.append(report.model_copy(update={"latest_fresh_run": latest}))
        if len(batch_reports) < 2:
            return None
        ranking = self._build_eval_fresh_replay_rankings(batch_reports)
        return next((item for item in ranking if item.issue_id == issue_id), None)

    def _build_eval_scenario_baseline_comparison(
        self,
        workspace_id: str,
        report: EvalScenarioReport,
        baseline: Optional[EvalScenarioReport],
    ) -> Optional[EvalScenarioBaselineComparison]:
        if baseline is None or baseline.scenario.scenario_id == report.scenario.scenario_id:
            return None
        issue = self._get_issue_from_snapshot(workspace_id, report.scenario.issue_id)
        guidance_only_in_scenario = sorted(set(report.scenario.guidance_paths) - set(baseline.scenario.guidance_paths))
        guidance_only_in_baseline = sorted(set(baseline.scenario.guidance_paths) - set(report.scenario.guidance_paths))
        ticket_only_in_scenario = sorted(set(report.scenario.ticket_context_ids) - set(baseline.scenario.ticket_context_ids))
        ticket_only_in_baseline = sorted(set(baseline.scenario.ticket_context_ids) - set(report.scenario.ticket_context_ids))
        browser_only_in_scenario = sorted(set(report.scenario.browser_dump_ids) - set(baseline.scenario.browser_dump_ids))
        browser_only_in_baseline = sorted(set(baseline.scenario.browser_dump_ids) - set(report.scenario.browser_dump_ids))
        profile_only_in_scenario = sorted(
            set(report.scenario.verification_profile_ids) - set(baseline.scenario.verification_profile_ids)
        )
        profile_only_in_baseline = sorted(
            set(baseline.scenario.verification_profile_ids) - set(report.scenario.verification_profile_ids)
        )
        verification_profile_deltas = self._build_eval_verification_profile_deltas(report, baseline)
        success_delta = report.success_runs - baseline.success_runs
        failed_delta = report.failed_runs - baseline.failed_runs
        verification_delta = round(report.verification_success_rate - baseline.verification_success_rate, 1)
        duration_delta = report.avg_duration_ms - baseline.avg_duration_ms
        cost_delta = round(report.total_estimated_cost - baseline.total_estimated_cost, 4)
        scenario_score = 0
        baseline_score = 0
        scenario_reasons: list[str] = []
        baseline_reasons: list[str] = []
        for metric, weight, scenario_wins, baseline_wins in [
            ("more successful runs", 2, success_delta > 0, success_delta < 0),
            ("fewer failed runs", 2, failed_delta < 0, failed_delta > 0),
            ("higher verification confidence", 2, verification_delta > 0, verification_delta < 0),
            ("lower estimated cost", 1, cost_delta < 0, cost_delta > 0),
            ("faster average duration", 1, duration_delta < 0, duration_delta > 0),
        ]:
            if scenario_wins:
                scenario_score += weight
                scenario_reasons.append(metric)
            elif baseline_wins:
                baseline_score += weight
                baseline_reasons.append(metric)
        preferred = "tie"
        preferred_scenario_id: Optional[str] = None
        preferred_scenario_name: Optional[str] = None
        preference_reasons: list[str] = []
        if scenario_score > baseline_score:
            preferred = "scenario"
            preferred_scenario_id = report.scenario.scenario_id
            preferred_scenario_name = report.scenario.name
            preference_reasons = scenario_reasons
        elif baseline_score > scenario_score:
            preferred = "baseline"
            preferred_scenario_id = baseline.scenario.scenario_id
            preferred_scenario_name = baseline.scenario.name
            preference_reasons = baseline_reasons
        summary_parts = [f"Compared to baseline {baseline.scenario.name}"]
        if success_delta:
            summary_parts.append(f"{success_delta:+d} successful run(s)")
        if failed_delta:
            summary_parts.append(f"{failed_delta:+d} failed run(s)")
        if verification_delta:
            summary_parts.append(f"{verification_delta:+.1f}% verification")
        if cost_delta:
            summary_parts.append(f"{cost_delta:+.4f} cost")
        if duration_delta:
            summary_parts.append(f"{duration_delta:+d}ms duration")
        if verification_profile_deltas:
            summary_parts.append(f"{len(verification_profile_deltas)} verification profile comparison(s)")
        if preferred != "tie" and preferred_scenario_name:
            summary_parts.append(f"preferred: {preferred_scenario_name}")
        if issue.title:
            summary_parts.append(f"issue: {issue.title}")
        return EvalScenarioBaselineComparison(
            compared_to_scenario_id=baseline.scenario.scenario_id,
            compared_to_name=baseline.scenario.name,
            guidance_only_in_scenario=guidance_only_in_scenario,
            guidance_only_in_baseline=guidance_only_in_baseline,
            ticket_context_only_in_scenario=ticket_only_in_scenario,
            ticket_context_only_in_baseline=ticket_only_in_baseline,
            browser_dump_only_in_scenario=browser_only_in_scenario,
            browser_dump_only_in_baseline=browser_only_in_baseline,
            verification_profile_only_in_scenario=profile_only_in_scenario,
            verification_profile_only_in_baseline=profile_only_in_baseline,
            verification_profile_deltas=verification_profile_deltas,
            success_runs_delta=success_delta,
            failed_runs_delta=failed_delta,
            verification_success_rate_delta=verification_delta,
            avg_duration_ms_delta=duration_delta,
            total_estimated_cost_delta=cost_delta,
            preferred=preferred,
            preferred_scenario_id=preferred_scenario_id,
            preferred_scenario_name=preferred_scenario_name,
            preference_reasons=preference_reasons,
            summary="; ".join(summary_parts),
        )

    def _build_eval_verification_profile_deltas(
        self,
        report: EvalScenarioReport,
        baseline: EvalScenarioReport,
    ) -> list[EvalScenarioVerificationProfileDelta]:
        scenario_reports = {item.profile_id: item for item in report.verification_profile_reports}
        baseline_reports = {item.profile_id: item for item in baseline.verification_profile_reports}
        deltas: list[EvalScenarioVerificationProfileDelta] = []
        for profile_id in sorted(set(scenario_reports) | set(baseline_reports)):
            scenario_item = scenario_reports.get(profile_id)
            baseline_item = baseline_reports.get(profile_id)
            profile_name = (
                scenario_item.profile_name if scenario_item is not None else baseline_item.profile_name if baseline_item is not None else profile_id
            )
            scenario_total_runs = scenario_item.total_runs if scenario_item is not None else 0
            baseline_total_runs = baseline_item.total_runs if baseline_item is not None else 0
            scenario_success_rate = scenario_item.success_rate if scenario_item is not None else 0.0
            baseline_success_rate = baseline_item.success_rate if baseline_item is not None else 0.0
            scenario_checklist_pass_rate = scenario_item.checklist_pass_rate if scenario_item is not None else 0.0
            baseline_checklist_pass_rate = baseline_item.checklist_pass_rate if baseline_item is not None else 0.0
            scenario_avg_attempt_count = scenario_item.avg_attempt_count if scenario_item is not None else 0.0
            baseline_avg_attempt_count = baseline_item.avg_attempt_count if baseline_item is not None else 0.0
            success_rate_delta = round(scenario_success_rate - baseline_success_rate, 1)
            checklist_pass_rate_delta = round(scenario_checklist_pass_rate - baseline_checklist_pass_rate, 1)
            avg_attempt_count_delta = round(scenario_avg_attempt_count - baseline_avg_attempt_count, 2)
            preferred = "tie"
            if success_rate_delta > 0 or checklist_pass_rate_delta > 0 or avg_attempt_count_delta < 0:
                preferred = "scenario"
            elif success_rate_delta < 0 or checklist_pass_rate_delta < 0 or avg_attempt_count_delta > 0:
                preferred = "baseline"
            summary_parts = [profile_name]
            if scenario_total_runs - baseline_total_runs:
                summary_parts.append(f"{scenario_total_runs - baseline_total_runs:+d} run(s)")
            if success_rate_delta:
                summary_parts.append(f"{success_rate_delta:+.1f}% success")
            if checklist_pass_rate_delta:
                summary_parts.append(f"{checklist_pass_rate_delta:+.1f}% checklist")
            if avg_attempt_count_delta:
                summary_parts.append(f"{avg_attempt_count_delta:+.2f} attempts")
            deltas.append(
                EvalScenarioVerificationProfileDelta(
                    profile_id=profile_id,
                    profile_name=profile_name,
                    present_in_scenario=scenario_item is not None,
                    present_in_baseline=baseline_item is not None,
                    scenario_total_runs=scenario_total_runs,
                    baseline_total_runs=baseline_total_runs,
                    total_runs_delta=scenario_total_runs - baseline_total_runs,
                    scenario_success_rate=scenario_success_rate,
                    baseline_success_rate=baseline_success_rate,
                    success_rate_delta=success_rate_delta,
                    scenario_checklist_pass_rate=scenario_checklist_pass_rate,
                    baseline_checklist_pass_rate=baseline_checklist_pass_rate,
                    checklist_pass_rate_delta=checklist_pass_rate_delta,
                    scenario_avg_attempt_count=scenario_avg_attempt_count,
                    baseline_avg_attempt_count=baseline_avg_attempt_count,
                    avg_attempt_count_delta=avg_attempt_count_delta,
                    scenario_confidence_counts=dict(scenario_item.confidence_counts) if scenario_item is not None else {},
                    baseline_confidence_counts=dict(baseline_item.confidence_counts) if baseline_item is not None else {},
                    preferred=preferred,
                    summary="; ".join(summary_parts),
                )
            )
        return deltas

    def _apply_eval_scenario_to_packet(
        self,
        packet: IssueContextPacket,
        scenario: EvalScenarioRecord,
    ) -> IssueContextPacket:
        guidance = packet.guidance
        if scenario.guidance_paths:
            guidance_lookup = {item.path: item for item in packet.guidance}
            guidance = [guidance_lookup[path] for path in scenario.guidance_paths if path in guidance_lookup]
        verification_profiles = packet.available_verification_profiles
        if scenario.verification_profile_ids:
            profile_lookup = {item.profile_id: item for item in packet.available_verification_profiles}
            verification_profiles = [
                profile_lookup[profile_id]
                for profile_id in scenario.verification_profile_ids
                if profile_id in profile_lookup
            ]
        ticket_contexts = packet.ticket_contexts
        if scenario.ticket_context_ids:
            ticket_lookup = {item.context_id: item for item in packet.ticket_contexts}
            ticket_contexts = [
                ticket_lookup[context_id]
                for context_id in scenario.ticket_context_ids
                if context_id in ticket_lookup
            ]
        browser_dumps = packet.browser_dumps
        if scenario.browser_dump_ids:
            dump_lookup = {item.dump_id: item for item in packet.browser_dumps}
            browser_dumps = [dump_lookup[dump_id] for dump_id in scenario.browser_dump_ids if dump_id in dump_lookup]
        scenario_prompt = self._build_prompt(
            packet.workspace,
            packet.issue,
            packet.tree_focus,
            packet.recent_fixes,
            packet.recent_activity,
            guidance,
            verification_profiles,
            ticket_contexts,
            packet.threat_models,
            browser_dumps,
            packet.vulnerability_findings,
            packet.related_paths,
            packet.repo_map,
            packet.dynamic_context,
            packet.repo_config,
            packet.matched_path_instructions,
        )
        summary_lines = [
            f"Evaluation scenario: {scenario.name}",
            f"Scenario id: {scenario.scenario_id}",
        ]
        if scenario.description:
            summary_lines.append(f"Description: {scenario.description}")
        if scenario.notes:
            summary_lines.append(f"Notes: {scenario.notes}")
        if scenario.guidance_paths:
            summary_lines.append(f"Pinned guidance: {', '.join(scenario.guidance_paths)}")
        if scenario.ticket_context_ids:
            summary_lines.append(f"Pinned ticket context: {', '.join(scenario.ticket_context_ids)}")
        if scenario.verification_profile_ids:
            summary_lines.append(f"Pinned verification profiles: {', '.join(scenario.verification_profile_ids)}")
        if scenario.browser_dump_ids:
            summary_lines.append(f"Pinned browser dumps: {', '.join(scenario.browser_dump_ids)}")
        return packet.model_copy(
            update={
                "guidance": guidance,
                "available_verification_profiles": verification_profiles,
                "ticket_contexts": ticket_contexts,
                "browser_dumps": browser_dumps,
                "prompt": f"{chr(10).join(summary_lines)}\n\n{scenario_prompt}",
            }
        )

    def _record_eval_scenario_run(self, workspace_id: str, scenario_id: str, run_id: str) -> None:
        scenarios = self.store.list_eval_scenarios(workspace_id)
        target = next((item for item in scenarios if item.scenario_id == scenario_id), None)
        if target is None:
            raise FileNotFoundError(scenario_id)
        if run_id in target.run_ids:
            return
        updated = target.model_copy(update={"run_ids": [*target.run_ids, run_id], "updated_at": utc_now()})
        self.store.save_eval_scenarios(
            workspace_id,
            [updated if item.scenario_id == scenario_id else item for item in scenarios],
        )
        self._record_activity(
            workspace_id=workspace_id,
            entity_type="issue",
            entity_id=target.issue_id,
            action="eval_scenario.executed",
            summary=f"Queued fresh run for eval scenario {target.name}",
            actor=build_activity_actor("operator", "operator"),
            issue_id=target.issue_id,
            run_id=run_id,
            details={"scenario_id": scenario_id, "run_id": run_id},
        )

    def _select_eval_runs(self, workspace_id: str, scenario: EvalScenarioRecord) -> list[RunRecord]:
        selected_runs = []
        allowed_ids = set(scenario.run_ids)
        for run in self.store.list_runs(workspace_id):
            if run.issue_id != scenario.issue_id:
                continue
            if allowed_ids and run.run_id not in allowed_ids:
                continue
            selected_runs.append(run)
        return selected_runs

    def _load_eval_run_metrics(self, runs: list[RunRecord]) -> list[RunMetrics]:
        metrics = []
        for run in runs:
            item = self.runtime_service.load_run_metrics(run.run_id)
            if item:
                metrics.append(item)
        return metrics

    def _build_eval_variant_diff(self, scenario: EvalScenarioRecord, packet: IssueContextPacket) -> EvalScenarioVariantDiff:
        current_guidance_paths = [item.path for item in packet.guidance[:8]]
        current_ticket_context_ids = [item.context_id for item in packet.ticket_contexts[:8]]
        added_guidance_paths, removed_guidance_paths = self._diff_ordered_strings(scenario.guidance_paths, current_guidance_paths)
        added_ticket_context_ids, removed_ticket_context_ids = self._diff_ordered_strings(
            scenario.ticket_context_ids,
            current_ticket_context_ids,
        )
        changed = bool(
            added_guidance_paths
            or removed_guidance_paths
            or added_ticket_context_ids
            or removed_ticket_context_ids
        )
        summary_parts = []
        if added_guidance_paths or removed_guidance_paths:
            summary_parts.append(
                f"guidance +{len(added_guidance_paths)} / -{len(removed_guidance_paths)}"
            )
        if added_ticket_context_ids or removed_ticket_context_ids:
            summary_parts.append(
                f"ticket context +{len(added_ticket_context_ids)} / -{len(removed_ticket_context_ids)}"
            )
        if not summary_parts:
            summary_parts.append("saved guidance and ticket-context variants still match the current issue packet")
        return EvalScenarioVariantDiff(
            selected_guidance_paths=scenario.guidance_paths,
            current_guidance_paths=current_guidance_paths,
            added_guidance_paths=added_guidance_paths,
            removed_guidance_paths=removed_guidance_paths,
            selected_ticket_context_ids=scenario.ticket_context_ids,
            current_ticket_context_ids=current_ticket_context_ids,
            added_ticket_context_ids=added_ticket_context_ids,
            removed_ticket_context_ids=removed_ticket_context_ids,
            changed=changed,
            summary="; ".join(summary_parts),
        )

    def _build_eval_variant_rollups(
        self,
        workspace_id: str,
        scenarios: list[EvalScenarioRecord],
        variant_kind: str,
    ) -> list[EvalVariantRollup]:
        buckets: dict[str, EvalVariantRollup] = {}
        bucket_runs: dict[str, list[RunRecord]] = {}
        bucket_metric_count: dict[str, int] = {}
        bucket_total_duration: dict[str, int] = {}
        bucket_verification_total: dict[str, int] = {}
        bucket_verification_success: dict[str, int] = {}
        bucket_verification_keys: dict[str, set[tuple[str, str]]] = {}
        for scenario in scenarios:
            selected_values = scenario.guidance_paths if variant_kind == "guidance" else scenario.ticket_context_ids
            variant_key = "|".join(selected_values) if selected_values else "__default__"
            current = buckets.get(variant_key)
            if current is None:
                current = EvalVariantRollup(
                    variant_kind=variant_kind,
                    variant_key=variant_key,
                    label=self._format_eval_variant_label(variant_kind, selected_values),
                    selected_values=list(selected_values),
                )
                buckets[variant_key] = current
                bucket_runs[variant_key] = []
                bucket_metric_count[variant_key] = 0
                bucket_total_duration[variant_key] = 0
                bucket_verification_total[variant_key] = 0
                bucket_verification_success[variant_key] = 0
                bucket_verification_keys[variant_key] = set()
            current.scenario_ids.append(scenario.scenario_id)
            current.scenario_names.append(scenario.name)
            current.scenario_count += 1
            selected_runs = self._dedupe_eval_runs(self._select_eval_runs(workspace_id, scenario))
            run_metrics = self._load_eval_run_metrics(selected_runs)
            bucket_runs[variant_key] = self._dedupe_eval_runs([*bucket_runs[variant_key], *selected_runs])
            current.run_count = len(bucket_runs[variant_key])
            current.success_runs = sum(1 for run in bucket_runs[variant_key] if run.status == "completed")
            current.failed_runs = sum(1 for run in bucket_runs[variant_key] if run.status == "failed")
            current.total_estimated_cost = round(current.total_estimated_cost + sum(item.estimated_cost for item in run_metrics), 4)
            bucket_metric_count[variant_key] += len(run_metrics)
            bucket_total_duration[variant_key] += sum(item.duration_ms for item in run_metrics)
            metric_count = bucket_metric_count[variant_key]
            current.avg_duration_ms = int(bucket_total_duration[variant_key] / metric_count) if metric_count else 0
            verification_reports = self.list_verification_profile_reports(workspace_id, issue_id=scenario.issue_id)
            if scenario.verification_profile_ids:
                verification_reports = [
                    item for item in verification_reports if item.profile_id in set(scenario.verification_profile_ids)
                ]
            new_reports = [
                item
                for item in verification_reports
                if (scenario.issue_id, item.profile_id) not in bucket_verification_keys[variant_key]
            ]
            bucket_verification_keys[variant_key].update((scenario.issue_id, item.profile_id) for item in new_reports)
            bucket_verification_total[variant_key] += sum(item.total_runs for item in new_reports)
            bucket_verification_success[variant_key] += sum(item.success_runs for item in new_reports)
            current.verification_success_rate = round(
                (bucket_verification_success[variant_key] / bucket_verification_total[variant_key]) * 100,
                1,
            ) if bucket_verification_total[variant_key] else 0.0
            current.runtime_breakdown = self._build_eval_run_dimension_breakdown(
                bucket_runs[variant_key],
                lambda run: (run.runtime or "unknown", run.runtime or "unknown"),
            )
            current.model_breakdown = self._build_eval_run_dimension_breakdown(
                bucket_runs[variant_key],
                lambda run: (run.model or "unknown", run.model or "unknown"),
            )
            current.summary = (
                f"{current.scenario_count} scenario(s); "
                f"{current.success_runs} successful run(s); "
                f"{current.failed_runs} failed run(s); "
                f"verification {current.verification_success_rate}%"
            )
        return sorted(buckets.values(), key=lambda item: (-item.scenario_count, item.label.lower(), item.variant_key))

    def _dedupe_eval_runs(self, runs: list[RunRecord]) -> list[RunRecord]:
        ordered: dict[str, RunRecord] = {}
        for run in runs:
            ordered[run.run_id] = run
        return list(ordered.values())

    def _build_eval_run_dimension_breakdown(self, runs: list[RunRecord], resolver) -> list[VerificationProfileDimensionSummary]:
        buckets: dict[str, VerificationProfileDimensionSummary] = {}
        for run in runs:
            key, label = resolver(run)
            key = key or "unknown"
            label = label or key
            current = buckets.get(key)
            if current is None:
                current = VerificationProfileDimensionSummary(key=key, label=label)
                buckets[key] = current
            current.total_runs += 1
            if run.status == "completed":
                current.success_runs += 1
            elif run.status == "failed":
                current.failed_runs += 1
            current.success_rate = round((current.success_runs / current.total_runs) * 100, 1) if current.total_runs else 0.0
            if current.last_run_at is None or run.created_at > current.last_run_at:
                current.last_run_at = run.created_at
        return sorted(buckets.values(), key=lambda item: (-item.total_runs, item.label.lower(), item.key))

    def _format_eval_variant_label(self, variant_kind: str, selected_values: list[str]) -> str:
        if not selected_values:
            return "Current defaults" if variant_kind == "guidance" else "Current ticket context"
        preview = ", ".join(selected_values[:3])
        if len(selected_values) > 3:
            preview += f" +{len(selected_values) - 3} more"
        return preview

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

    def _extract_coverage_report_path(self, text: str) -> Optional[str]:
        patterns = [
            r"(--cov-report=xml:?([^\s]+)?)",
            r"(coverage(?:/[\w./-]+)?\.xml)",
            r"(coverage\.info)",
            r"(build/reports/jacoco/test/jacocoTestReport\.(?:csv|xml))",
            r"(target/site/jacoco/jacoco\.(?:csv|xml))",
        ]
        for pattern in patterns:
            match = re.search(pattern, text, flags=re.IGNORECASE)
            if not match:
                continue
            groups = [item for item in match.groups() if item]
            if pattern.startswith("(--cov-report") and len(groups) > 1:
                report_target = groups[1]
                return report_target if report_target and report_target != "xml" else "coverage.xml"
            return groups[-1]
        return None

    def _infer_coverage_format(self, command: str, report_path: Optional[str]) -> str:
        haystack = f"{command} {report_path or ''}".lower()
        if "jacoco" in haystack:
            return "jacoco"
        if "lcov" in haystack or (report_path or "").endswith(".info"):
            return "lcov"
        if "go test -cover" in haystack or "coverprofile" in haystack:
            return "go"
        if "cov-report=xml" in haystack or (report_path or "").endswith(".xml"):
            return "cobertura"
        return "unknown"

    def _verification_profile_name(self, command: str) -> str:
        normalized = command.lower()
        if "pytest" in normalized:
            return "Pytest verification"
        if "npm run test:coverage" in normalized or "vitest" in normalized or "jest" in normalized:
            return "JavaScript coverage"
        if "npm test" in normalized or "pnpm test" in normalized or "yarn test" in normalized:
            return "JavaScript tests"
        if "cargo test" in normalized:
            return "Cargo tests"
        if "go test" in normalized:
            return "Go verification"
        return "Repository verification"

    def _parse_acceptance_criteria(self, text: Optional[str]) -> list[str]:
        if not text:
            return []
        lines = text.splitlines()
        criteria: list[str] = []
        collecting = False
        for raw_line in lines:
            line = raw_line.strip()
            if re.match(r"^#{1,6}\s+", line):
                heading = re.sub(r"^#{1,6}\s+", "", line).strip().lower()
                if "acceptance" in heading and "criteria" in heading:
                    collecting = True
                    continue
                if collecting and criteria:
                    break
                collecting = False
            if not collecting:
                continue
            bullet = re.match(r"^(?:[-*]\s+(?:\[[ xX]\]\s+)?)?(.*)$", line)
            numbered = re.match(r"^\d+\.\s+(.*)$", line)
            value = ""
            if numbered:
                value = numbered.group(1).strip()
            elif bullet:
                value = bullet.group(1).strip()
            if value:
                criteria.append(value)
            elif criteria:
                break
        if criteria:
            return self._normalize_text_list(criteria, limit=10)

        checklist_items: list[str] = []
        for raw_line in lines:
            match = re.match(r"^\s*[-*]\s+\[(?: |x|X)\]\s+(.*)$", raw_line)
            if match:
                checklist_items.append(match.group(1).strip())
        return self._normalize_text_list(checklist_items, limit=10)

    def _save_imported_ticket_context(
        self,
        workspace_id: str,
        issue_id: str,
        request: TicketContextUpsertRequest,
        *,
        actor: Optional[ActivityActor] = None,
    ) -> TicketContextRecord:
        context_id = request.context_id or self._slug_runbook_name(f"{request.provider}-{request.external_id or request.title}")
        return self.save_ticket_context(
            workspace_id,
            issue_id,
            request.model_copy(update={"context_id": context_id}),
            actor=actor or build_activity_actor("system", "system"),
            action="ticket_context.synced",
        )

    def _infer_verification_profiles_from_guidance(
        self,
        workspace_id: str,
        guidance: list[RepoGuidanceRecord],
        root: Path,
    ) -> list[VerificationProfileRecord]:
        profiles: list[VerificationProfileRecord] = []
        seen_commands: set[str] = set()
        for item in guidance:
            candidate_path = root / item.path
            if not candidate_path.exists() or not candidate_path.is_file():
                continue
            text = candidate_path.read_text(encoding="utf-8", errors="ignore")
            for command in self._extract_test_commands(text):
                normalized_command = " ".join(command.split())
                if normalized_command in seen_commands:
                    continue
                seen_commands.add(normalized_command)
                report_path = self._extract_coverage_report_path(text) or self._extract_coverage_report_path(normalized_command)
                coverage_format = self._infer_coverage_format(normalized_command, report_path)
                coverage_command = normalized_command if coverage_format != "unknown" or report_path else None
                profile_id = self._slug_runbook_name(f"inferred-{normalized_command}")
                profiles.append(
                    VerificationProfileRecord(
                        profile_id=profile_id,
                        workspace_id=workspace_id,
                        name=self._verification_profile_name(normalized_command),
                        description=f"Inferred from {item.path}.",
                        test_command=normalized_command,
                        coverage_command=coverage_command,
                        coverage_report_path=report_path,
                        coverage_format=coverage_format,
                        max_runtime_seconds=60,
                        retry_count=1,
                        source_paths=[item.path],
                        built_in=True,
                    )
                )
        return profiles[:6]

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
