from __future__ import annotations

from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException, Query
from fastapi.middleware.cors import CORSMiddleware

from .models import (
    AgentQueryRequest,
    AppSettings,
    BrowserDumpUpsertRequest,
    DismissImprovementRequest,
    EvalScenarioReplayRequest,
    EvalScenarioUpsertRequest,
    FixRecordRequest,
    FixUpdateRequest,
    GitHubPRCreate,
    GuidanceStarterRequest,
    IntegrationTestRequest,
    IssueCreateRequest,
    IssueContextReplayRequest,
    IssueUpdateRequest,
    PlanApproveRequest,
    PlanRejectRequest,
    PromoteSignalRequest,
    RunAcceptRequest,
    RuntimeProbeRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    RunRequest,
    SavedIssueViewRequest,
    ThreatModelUpsertRequest,
    TicketContextUpsertRequest,
    VulnerabilityFindingUpsertRequest,
    VulnerabilityImportRequest,
    TerminalOpenRequest,
    TerminalResizeRequest,
    TerminalWriteRequest,
    VerificationProfileRunRequest,
    VerificationProfileUpsertRequest,
    VerifyIssueRequest,
    WorkspaceLoadRequest,
)
from .service import TrackerService
from .store import FileStore

ROOT = Path(__file__).resolve().parents[1]
STORE = FileStore(ROOT / "data")
SERVICE = TrackerService(STORE)

app = FastAPI(title="xMustard", version="0.1.0")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.get("/api/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


def _split_csv(value: Optional[str]) -> Optional[list[str]]:
    if value is None:
        return None
    items = [item.strip() for item in value.split(",") if item.strip()]
    return items or None


@app.get("/api/runtimes")
def runtimes():
    return [item.model_dump(mode="json") for item in SERVICE.runtime_service.detect_runtimes()]


@app.get("/api/settings")
def get_settings():
    return SERVICE.get_settings().model_dump(mode="json")


@app.post("/api/settings")
def update_settings(settings: AppSettings):
    return SERVICE.update_settings(settings).model_dump(mode="json")


@app.get("/api/agent/capabilities")
def local_agent_capabilities():
    return SERVICE.runtime_service.local_agent_capabilities().model_dump(mode="json")


@app.post("/api/workspaces/{workspace_id}/agent/probe")
def probe_runtime(workspace_id: str, request: RuntimeProbeRequest):
    try:
        return SERVICE.probe_runtime(workspace_id, request.runtime, request.model)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces")
def list_workspaces():
    return [item.model_dump(mode="json") for item in SERVICE.list_workspaces()]


@app.post("/api/workspaces/load")
def load_workspace(request: WorkspaceLoadRequest):
    root = Path(request.root_path).expanduser()
    if not root.exists():
        raise HTTPException(status_code=404, detail="Workspace path does not exist")
    snapshot = SERVICE.load_workspace(request)
    return snapshot.model_dump(mode="json") if snapshot else None


@app.get("/api/workspaces/{workspace_id}/snapshot")
def read_snapshot(workspace_id: str):
    snapshot = SERVICE.read_snapshot(workspace_id)
    if not snapshot:
        raise HTTPException(status_code=404, detail="Snapshot not found")
    return snapshot.model_dump(mode="json")


@app.get("/api/workspaces/{workspace_id}/activity")
def list_activity(
    workspace_id: str,
    issue_id: Optional[str] = Query(default=None),
    run_id: Optional[str] = Query(default=None),
    limit: int = Query(default=100),
):
    try:
        return [
            item.model_dump(mode="json")
            for item in SERVICE.list_activity(workspace_id, issue_id=issue_id, run_id=run_id, limit=limit)
        ]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/activity/overview")
def read_activity_overview(
    workspace_id: str,
    limit: int = Query(default=200),
):
    try:
        return SERVICE.read_activity_overview(workspace_id, limit=limit).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/issues")
def list_issues(
    workspace_id: str,
    q: str = Query(default=""),
    severity: Optional[str] = Query(default=None),
    issue_status: Optional[str] = Query(default=None),
    source: Optional[str] = Query(default=None),
    label: Optional[str] = Query(default=None),
    drift_only: bool = Query(default=False),
    needs_followup: Optional[bool] = Query(default=None),
    review_ready_only: bool = Query(default=False),
):
    try:
        return [
            item.model_dump(mode="json")
            for item in SERVICE.list_issues(
                workspace_id,
                query=q,
                severities=_split_csv(severity),
                issue_statuses=_split_csv(issue_status),
                sources=_split_csv(source),
                labels=_split_csv(label),
                drift_only=drift_only,
                needs_followup=needs_followup,
                review_ready_only=review_ready_only,
            )
        ]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Snapshot not found")


@app.post("/api/workspaces/{workspace_id}/issues")
def create_issue(workspace_id: str, request: IssueCreateRequest):
    try:
        return SERVICE.create_issue(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/views")
def list_saved_views(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_saved_views(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/views")
def create_saved_view(workspace_id: str, request: SavedIssueViewRequest):
    try:
        return SERVICE.create_saved_view(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.put("/api/workspaces/{workspace_id}/views/{view_id}")
def update_saved_view(workspace_id: str, view_id: str, request: SavedIssueViewRequest):
    try:
        return SERVICE.update_saved_view(workspace_id, view_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.delete("/api/workspaces/{workspace_id}/views/{view_id}")
def delete_saved_view(workspace_id: str, view_id: str):
    try:
        SERVICE.delete_saved_view(workspace_id, view_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True}


@app.patch("/api/workspaces/{workspace_id}/issues/{issue_id}")
def update_issue(workspace_id: str, issue_id: str, request: IssueUpdateRequest):
    try:
        return SERVICE.update_issue(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/drift")
def issue_drift(workspace_id: str, issue_id: str):
    try:
        return SERVICE.read_issue_drift(workspace_id, issue_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/signals")
def list_signals(
    workspace_id: str,
    q: str = Query(default=""),
    severity: Optional[str] = Query(default=None),
    promoted: Optional[bool] = Query(default=None),
):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_signals(workspace_id, q, severity, promoted)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Snapshot not found")


@app.get("/api/workspaces/{workspace_id}/fixes")
def list_fixes(
    workspace_id: str,
    issue_id: Optional[str] = Query(default=None),
):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_fixes(workspace_id, issue_id=issue_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/verifications")
def list_verifications(
    workspace_id: str,
    issue_id: Optional[str] = Query(default=None),
):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_verifications(workspace_id, issue_id=issue_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/review-queue")
def review_queue(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_review_queue(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/runbooks")
def list_runbooks(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_runbooks(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/runbooks")
def save_runbook(workspace_id: str, request: RunbookUpsertRequest):
    try:
        return SERVICE.save_runbook(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/verification-profiles")
def list_verification_profiles(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_verification_profiles(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/verification-profile-history")
def list_verification_profile_history(
    workspace_id: str,
    profile_id: Optional[str] = Query(default=None),
    issue_id: Optional[str] = Query(default=None),
):
    try:
        return [
            item.model_dump(mode="json")
            for item in SERVICE.list_verification_profile_history(workspace_id, profile_id=profile_id, issue_id=issue_id)
        ]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/verification-profile-reports")
def list_verification_profile_reports(
    workspace_id: str,
    issue_id: Optional[str] = Query(default=None),
):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_verification_profile_reports(workspace_id, issue_id=issue_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/eval-scenarios")
def list_eval_scenarios(
    workspace_id: str,
    issue_id: Optional[str] = Query(default=None),
):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_eval_scenarios(workspace_id, issue_id=issue_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/eval-scenarios")
def save_eval_scenario(workspace_id: str, request: EvalScenarioUpsertRequest):
    try:
        return SERVICE.save_eval_scenario(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.delete("/api/workspaces/{workspace_id}/eval-scenarios/{scenario_id}")
def delete_eval_scenario(workspace_id: str, scenario_id: str):
    try:
        SERVICE.delete_eval_scenario(workspace_id, scenario_id)
        return {"ok": True, "scenario_id": scenario_id}
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Eval scenario not found")


@app.get("/api/workspaces/{workspace_id}/eval-report")
def get_eval_report(
    workspace_id: str,
    scenario_id: Optional[str] = Query(default=None),
):
    try:
        return SERVICE.get_eval_report(workspace_id, scenario_id=scenario_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/eval-scenarios/replay")
def replay_eval_scenarios(workspace_id: str, issue_id: str, request: EvalScenarioReplayRequest):
    try:
        return SERVICE.replay_eval_scenarios(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/verification-profiles")
def save_verification_profile(workspace_id: str, request: VerificationProfileUpsertRequest):
    try:
        return SERVICE.save_verification_profile(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.delete("/api/workspaces/{workspace_id}/verification-profiles/{profile_id}")
def delete_verification_profile(workspace_id: str, profile_id: str):
    try:
        SERVICE.delete_verification_profile(workspace_id, profile_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "profile_id": profile_id}


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/verification-profiles/{profile_id}/run")
def run_verification_profile_for_issue(
    workspace_id: str,
    issue_id: str,
    profile_id: str,
    request: VerificationProfileRunRequest,
):
    try:
        return SERVICE.run_issue_verification_profile(workspace_id, issue_id, profile_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.delete("/api/workspaces/{workspace_id}/runbooks/{runbook_id}")
def delete_runbook(workspace_id: str, runbook_id: str):
    try:
        SERVICE.delete_runbook(workspace_id, runbook_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "runbook_id": runbook_id}


@app.get("/api/workspaces/{workspace_id}/sources")
def read_sources(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.read_sources(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Snapshot not found")


@app.get("/api/workspaces/{workspace_id}/drift")
def read_drift(workspace_id: str):
    try:
        return SERVICE.read_drift(workspace_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Snapshot not found")


@app.get("/api/workspaces/{workspace_id}/worktree")
def read_worktree(workspace_id: str):
    try:
        return SERVICE.read_worktree_status(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/scan")
def scan_workspace(workspace_id: str):
    try:
        snapshot = SERVICE.scan_workspace(workspace_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")
    return snapshot.model_dump(mode="json")


@app.get("/api/workspaces/{workspace_id}/tree")
def list_tree(workspace_id: str, relative_path: str = Query(default="")):
    try:
        return SERVICE.list_tree(workspace_id, relative_path)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/guidance")
def list_guidance(workspace_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_workspace_guidance(workspace_id)]
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/guidance/health")
def guidance_health(workspace_id: str):
    try:
        return SERVICE.get_workspace_guidance_health(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/guidance/starters")
def generate_guidance_starter(workspace_id: str, request: GuidanceStarterRequest):
    try:
        return SERVICE.generate_guidance_starter(workspace_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except FileExistsError as exc:
        raise HTTPException(status_code=409, detail=f"Starter already exists: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/repo-config")
def read_repo_config(workspace_id: str):
    try:
        return SERVICE.read_workspace_repo_config(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/repo-config/health")
def read_repo_config_health(workspace_id: str):
    try:
        return SERVICE.get_workspace_repo_config_health(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/repo-map")
def read_repo_map(workspace_id: str):
    try:
        return SERVICE.read_repo_map(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/context")
def issue_context(workspace_id: str, issue_id: str):
    try:
        return SERVICE.build_issue_context(workspace_id, issue_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/work")
def issue_work(workspace_id: str, issue_id: str, runbook_id: Optional[str] = Query(default=None)):
    try:
        return SERVICE.issue_work(workspace_id, issue_id, runbook_id=runbook_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context")
def list_ticket_context(workspace_id: str, issue_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_ticket_contexts(workspace_id, issue_id)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context")
def save_ticket_context(workspace_id: str, issue_id: str, request: TicketContextUpsertRequest):
    try:
        return SERVICE.save_ticket_context(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.delete("/api/workspaces/{workspace_id}/issues/{issue_id}/ticket-context/{context_id}")
def delete_ticket_context(workspace_id: str, issue_id: str, context_id: str):
    try:
        SERVICE.delete_ticket_context(workspace_id, issue_id, context_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "context_id": context_id}


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/threat-models")
def list_threat_models(workspace_id: str, issue_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_threat_models(workspace_id, issue_id)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/threat-models")
def save_threat_model(workspace_id: str, issue_id: str, request: ThreatModelUpsertRequest):
    try:
        return SERVICE.save_threat_model(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.delete("/api/workspaces/{workspace_id}/issues/{issue_id}/threat-models/{threat_model_id}")
def delete_threat_model(workspace_id: str, issue_id: str, threat_model_id: str):
    try:
        SERVICE.delete_threat_model(workspace_id, issue_id, threat_model_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "threat_model_id": threat_model_id}


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/context-replays")
def list_issue_context_replays(workspace_id: str, issue_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_issue_context_replays(workspace_id, issue_id)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/context-replays")
def capture_issue_context_replay(workspace_id: str, issue_id: str, request: IssueContextReplayRequest):
    try:
        return SERVICE.capture_issue_context_replay(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/context-replays/{replay_id}/compare")
def compare_issue_context_replay(workspace_id: str, issue_id: str, replay_id: str):
    try:
        return SERVICE.compare_issue_context_replay(workspace_id, issue_id, replay_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps")
def list_browser_dumps(workspace_id: str, issue_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_browser_dumps(workspace_id, issue_id)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps")
def save_browser_dump(workspace_id: str, issue_id: str, request: BrowserDumpUpsertRequest):
    try:
        return SERVICE.save_browser_dump(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.delete("/api/workspaces/{workspace_id}/issues/{issue_id}/browser-dumps/{dump_id}")
def delete_browser_dump(workspace_id: str, issue_id: str, dump_id: str):
    try:
        SERVICE.delete_browser_dump(workspace_id, issue_id, dump_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "dump_id": dump_id}


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-findings")
def list_vulnerability_findings(workspace_id: str, issue_id: str):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.list_vulnerability_findings(workspace_id, issue_id)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-report")
def read_vulnerability_finding_report(workspace_id: str, issue_id: str):
    try:
        return SERVICE.get_vulnerability_finding_report(workspace_id, issue_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/vulnerability-report")
def read_workspace_vulnerability_report(workspace_id: str):
    try:
        return SERVICE.get_workspace_vulnerability_report(workspace_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-findings")
def save_vulnerability_finding(workspace_id: str, issue_id: str, request: VulnerabilityFindingUpsertRequest):
    try:
        return SERVICE.save_vulnerability_finding(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-findings/import")
def import_vulnerability_findings(workspace_id: str, issue_id: str, request: VulnerabilityImportRequest):
    try:
        return [item.model_dump(mode="json") for item in SERVICE.import_vulnerability_findings(workspace_id, issue_id, request)]
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.delete("/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-findings/{finding_id}")
def delete_vulnerability_finding(workspace_id: str, issue_id: str, finding_id: str):
    try:
        SERVICE.delete_vulnerability_finding(workspace_id, issue_id, finding_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    return {"ok": True, "finding_id": finding_id}


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/runs")
def issue_run(workspace_id: str, issue_id: str, request: RunRequest):
    try:
        return SERVICE.start_issue_run(
            workspace_id,
            issue_id,
            request.runtime,
            request.model,
            request.instruction,
            request.runbook_id,
            request.eval_scenario_id,
            request.eval_replay_batch_id,
            request.planning,
        )
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/verify")
def verify_issue(workspace_id: str, issue_id: str, request: VerifyIssueRequest):
    try:
        return SERVICE.verify_issue_three_pass(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/fixes")
def record_fix(workspace_id: str, issue_id: str, request: FixRecordRequest):
    try:
        return SERVICE.record_fix(workspace_id, issue_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/fix-draft")
def suggest_fix_draft(workspace_id: str, issue_id: str, run_id: str = Query(...)):
    try:
        return SERVICE.suggest_fix_draft(workspace_id, issue_id, run_id).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/agent/query")
def agent_query(workspace_id: str, request: AgentQueryRequest):
    try:
        return SERVICE.start_agent_query(workspace_id, request.runtime, request.model, request.prompt)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/runs")
def list_runs(workspace_id: str):
    try:
        return SERVICE.list_runs(workspace_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}")
def get_run(workspace_id: str, run_id: str):
    try:
        return SERVICE.get_run(workspace_id, run_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Run not found")


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/insights")
def get_run_insights(workspace_id: str, run_id: str):
    try:
        return SERVICE.get_run_session_insight(workspace_id, run_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Run not found")


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/log")
def get_run_log(workspace_id: str, run_id: str, offset: int = Query(default=0)):
    try:
        return SERVICE.read_run_log(workspace_id, run_id, offset)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Run not found")


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/cancel")
def cancel_run(workspace_id: str, run_id: str):
    try:
        return SERVICE.cancel_run(workspace_id, run_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Run not found")


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/retry")
def retry_run(workspace_id: str, run_id: str):
    try:
        return SERVICE.retry_run(workspace_id, run_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Run not found")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/review")
def review_run(workspace_id: str, run_id: str, request: RunReviewRequest):
    try:
        return SERVICE.review_run(workspace_id, run_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/accept")
def accept_run(workspace_id: str, run_id: str, request: RunAcceptRequest):
    try:
        return SERVICE.accept_review_run(workspace_id, run_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/plan")
def generate_plan(workspace_id: str, run_id: str):
    try:
        return SERVICE.generate_run_plan(workspace_id, run_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/plan")
def get_plan(workspace_id: str, run_id: str):
    try:
        return SERVICE.get_run_plan(workspace_id, run_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/plan/approve")
def approve_plan(workspace_id: str, run_id: str, request: PlanApproveRequest):
    try:
        return SERVICE.approve_run_plan(workspace_id, run_id, request)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/plan/reject")
def reject_plan(workspace_id: str, run_id: str, request: PlanRejectRequest):
    try:
        return SERVICE.reject_run_plan(workspace_id, run_id, request)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/metrics")
def get_run_metrics(workspace_id: str, run_id: str):
    try:
        return SERVICE.get_run_metrics(workspace_id, run_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/metrics")
def list_workspace_metrics(workspace_id: str):
    try:
        return SERVICE.list_workspace_metrics(workspace_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/costs")
def get_workspace_cost_summary(workspace_id: str):
    return SERVICE.get_workspace_cost_summary(workspace_id)


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/quality")
def get_issue_quality(workspace_id: str, issue_id: str):
    try:
        return SERVICE.get_issue_quality(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/quality")
def score_issue_quality(workspace_id: str, issue_id: str):
    try:
        return SERVICE.score_issue_quality(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/quality/score-all")
def score_all_issues(workspace_id: str):
    try:
        return SERVICE.score_all_issues(workspace_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/duplicates")
def find_duplicates(workspace_id: str, issue_id: str):
    try:
        return SERVICE.find_duplicates(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/triage")
def triage_issue(workspace_id: str, issue_id: str):
    try:
        return SERVICE.triage_issue(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/triage/all")
def triage_all_issues(workspace_id: str):
    try:
        return SERVICE.triage_all_issues(workspace_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/coverage/parse")
def parse_coverage_report(workspace_id: str, report_path: str = Query(...), run_id: Optional[str] = Query(default=None), issue_id: Optional[str] = Query(default=None)):
    try:
        return SERVICE.parse_coverage_report(workspace_id, report_path, run_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/coverage")
def get_coverage(workspace_id: str, issue_id: Optional[str] = Query(default=None), run_id: Optional[str] = Query(default=None)):
    result = SERVICE.get_coverage(workspace_id, issue_id, run_id)
    if not result:
        raise HTTPException(status_code=404, detail="No coverage data found")
    return result


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/coverage-delta")
def get_coverage_delta(workspace_id: str, issue_id: str):
    return SERVICE.get_coverage_delta(workspace_id, issue_id)


@app.post("/api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions")
def generate_test_suggestions(workspace_id: str, issue_id: str):
    try:
        return SERVICE.generate_test_suggestions(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/issues/{issue_id}/test-suggestions")
def get_test_suggestions(workspace_id: str, issue_id: str):
    return SERVICE.get_test_suggestions(workspace_id, issue_id)


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/critique")
def generate_patch_critique(workspace_id: str, run_id: str):
    try:
        return SERVICE.generate_patch_critique(workspace_id, run_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/critique")
def get_patch_critique(workspace_id: str, run_id: str):
    result = SERVICE.get_patch_critique(workspace_id, run_id)
    if not result:
        raise HTTPException(status_code=404, detail="No critique found for this run")
    return result


@app.get("/api/workspaces/{workspace_id}/runs/{run_id}/improvements")
def get_run_improvements(workspace_id: str, run_id: str):
    return SERVICE.get_run_improvements(workspace_id, run_id)


@app.post("/api/workspaces/{workspace_id}/runs/{run_id}/improvements/{suggestion_id}/dismiss")
def dismiss_improvement(workspace_id: str, run_id: str, suggestion_id: str, request: DismissImprovementRequest):
    try:
        return SERVICE.dismiss_improvement(workspace_id, run_id, suggestion_id, request)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))


@app.patch("/api/workspaces/{workspace_id}/fixes/{fix_id}")
def update_fix(workspace_id: str, fix_id: str, request: FixUpdateRequest):
    try:
        return SERVICE.update_fix(workspace_id, fix_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.post("/api/workspaces/{workspace_id}/signals/{signal_id}/promote")
def promote_signal(workspace_id: str, signal_id: str, request: PromoteSignalRequest):
    try:
        return SERVICE.promote_signal(workspace_id, signal_id, request).model_dump(mode="json")
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=f"Missing resource: {exc}")


@app.get("/api/workspaces/{workspace_id}/export")
def export_workspace(workspace_id: str):
    try:
        return SERVICE.export_workspace(workspace_id).model_dump(mode="json")
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/workspaces/{workspace_id}/integrations")
def configure_integration(workspace_id: str, provider: str = Query(...), settings: dict = {}):
    return SERVICE.configure_integration(workspace_id, provider, settings)


@app.get("/api/workspaces/{workspace_id}/integrations")
def get_integration_configs(workspace_id: str):
    return SERVICE.get_integration_configs(workspace_id)


@app.post("/api/integrations/test")
def test_integration(request: IntegrationTestRequest):
    return SERVICE.test_integration(request)


@app.post("/api/workspaces/{workspace_id}/integrations/github/import")
def import_github_issues(workspace_id: str, repo: str = Query(...), state: str = Query(default="open")):
    try:
        return SERVICE.import_github_issues(workspace_id, repo, state)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/integrations/github/pr")
def create_github_pr(workspace_id: str, request: GitHubPRCreate):
    try:
        return SERVICE.create_github_pr(workspace_id, request)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/integrations/slack/notify")
def send_slack_notification(workspace_id: str, event: str = Query(...), message: Optional[str] = Query(default=None)):
    try:
        return SERVICE.send_slack_notification(workspace_id, event, message)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/integrations/linear/sync/{issue_id}")
def sync_issue_to_linear(workspace_id: str, issue_id: str):
    try:
        return SERVICE.sync_issue_to_linear(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/workspaces/{workspace_id}/integrations/jira/sync/{issue_id}")
def sync_issue_to_jira(workspace_id: str, issue_id: str):
    try:
        return SERVICE.sync_issue_to_jira(workspace_id, issue_id)
    except FileNotFoundError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))


@app.post("/api/terminal/open")
def terminal_open(request: TerminalOpenRequest):
    try:
        return SERVICE.open_terminal(request.workspace_id, request.cols, request.rows, request.terminal_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Workspace not found")


@app.post("/api/terminal/{terminal_id}/write")
def terminal_write(terminal_id: str, request: TerminalWriteRequest):
    try:
        SERVICE.terminal_write(terminal_id, request.data)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Terminal not found")
    return {"ok": True}


@app.post("/api/terminal/{terminal_id}/resize")
def terminal_resize(terminal_id: str, request: TerminalResizeRequest):
    try:
        SERVICE.terminal_resize(terminal_id, request.cols, request.rows)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Terminal not found")
    return {"ok": True}


@app.get("/api/terminal/{terminal_id}/read")
def terminal_read(terminal_id: str, workspace_id: str, offset: int = Query(default=0)):
    try:
        return SERVICE.terminal_read(workspace_id, terminal_id, offset)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Terminal not found")


@app.delete("/api/terminal/{terminal_id}")
def terminal_close(terminal_id: str):
    try:
        SERVICE.terminal_close(terminal_id)
    except FileNotFoundError:
        raise HTTPException(status_code=404, detail="Terminal not found")
    return {"ok": True}
