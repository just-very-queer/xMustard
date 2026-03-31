from __future__ import annotations

from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException, Query
from fastapi.middleware.cors import CORSMiddleware

from .models import (
    AgentQueryRequest,
    AppSettings,
    FixRecordRequest,
    FixUpdateRequest,
    IssueCreateRequest,
    IssueUpdateRequest,
    PromoteSignalRequest,
    RunAcceptRequest,
    RuntimeProbeRequest,
    RunReviewRequest,
    RunbookUpsertRequest,
    RunRequest,
    SavedIssueViewRequest,
    TerminalOpenRequest,
    TerminalResizeRequest,
    TerminalWriteRequest,
    WorkspaceLoadRequest,
)
from .service import TrackerService
from .store import FileStore

ROOT = Path(__file__).resolve().parents[1]
STORE = FileStore(ROOT / "data")
SERVICE = TrackerService(STORE)

app = FastAPI(title="Co Titan Bug Tracker", version="0.1.0")
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
        )
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
